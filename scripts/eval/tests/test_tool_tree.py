import contextlib
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import tool_tree  # noqa: E402
from runner_docker import BoundedResult, DockerError  # noqa: E402

BASE_ID = "sha256:" + "a" * 64
OURS_ID = "sha256:" + "b" * 64
HASH_1 = "1" * 64
HASH_2 = "2" * 64


def collected(curl="7.88.1", playwright="1.64.1", pip_version="24.0", curl_hash=HASH_1, link="/usr/bin/vim.basic"):
    npm = json.dumps({"dependencies": {"playwright": {"version": playwright}}})
    return "\n".join([
        "#section dpkg", f"curl={curl}", "jq=1.6",
        "#section npm_global", npm,
        "#section pip", f"pip=={pip_version}",
        "#section files", f"{HASH_1}  /etc/wgetrc", f"{curl_hash}  /usr/bin/curl",
        "#section links", f"/usr/bin/vi\t{link}",
        "",
    ])


class FakeDocker:
    def __init__(self, trees=None, ids=None, run_code=0, inspect_code=0):
        self.trees = trees if trees is not None else {BASE_ID: collected(), OURS_ID: collected()}
        self.ids = ids if ids is not None else {"base:tag": BASE_ID, "ours:tag": OURS_ID}
        self.run_code, self.inspect_code = run_code, inspect_code
        self.calls = []

    def __call__(self, args, timeout, max_stdout):
        self.calls.append((list(args), timeout, max_stdout))
        if args[0] == "image":
            return BoundedResult(self.inspect_code, self.ids.get(args[-1], "").encode() + b"\n", b"")
        image = args[args.index("sh") + 1]
        return BoundedResult(self.run_code, self.trees[image].encode(), b"")


def run_main(fake, directory, *extra):
    out = str(Path(directory) / "tree.json")
    stdout, stderr = io.StringIO(), io.StringIO()
    argv = ["--baseline", "base:tag", "--ours", "ours:tag", "--out", out, *extra]
    with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
        code = tool_tree.main(argv, run=fake)
    return code, out, stdout.getvalue(), stderr.getvalue()


class ParseTreeTest(unittest.TestCase):
    def test_reads_every_section(self):
        tree = tool_tree.parse_tree(collected())
        self.assertEqual(tree["dpkg"], {"curl": "7.88.1", "jq": "1.6"})
        self.assertEqual(tree["npm_global"], {"playwright": "1.64.1"})
        self.assertEqual(tree["pip"], {"pip": "24.0"})
        self.assertEqual(tree["files"], {"/etc/wgetrc": HASH_1, "/usr/bin/curl": HASH_1})
        self.assertEqual(tree["links"], {"/usr/bin/vi": "/usr/bin/vim.basic"})

    def test_keeps_a_version_with_an_epoch_and_a_path_with_a_space(self):
        text = collected().replace("jq=1.6", "jq=1:1.6-2+deb12u1").replace("/etc/wgetrc", "/etc/a b")
        tree = tool_tree.parse_tree(text)
        self.assertEqual(tree["dpkg"]["jq"], "1:1.6-2+deb12u1")
        self.assertIn("/etc/a b", tree["files"])

    def test_rejects_what_it_cannot_trust(self):
        cases = {
            "noise before the first section": "entrypoint noise\n" + collected(),
            "missing section": collected().replace("#section pip\npip==24.0\n", ""),
            "reordered sections": collected().replace("#section dpkg", "#section tmp")
                                             .replace("#section pip", "#section dpkg")
                                             .replace("#section tmp", "#section pip"),
            "unknown section": collected() + "#section extra\n",
            "empty section": collected().replace("jq=1.6\n", "").replace("curl=7.88.1\n", ""),
            "duplicate package": collected().replace("jq=1.6", "curl=8.0.0"),
            "duplicate file": collected().replace("/etc/wgetrc", "/usr/bin/curl"),
            "package without version": collected().replace("jq=1.6", "jq"),
            "bad npm json": collected().replace('{"dependencies"', '{"dependencies'),
            "npm without dependencies": collected().replace('"dependencies"', '"problems"'),
            "npm package without version": collected().replace('"version"', '"resolved"'),
            "short file hash": collected().replace(HASH_1 + "  /etc/wgetrc", "abc  /etc/wgetrc"),
            "relative file path": collected().replace("  /etc/wgetrc", "  etc/wgetrc"),
            "link without target": collected().replace("/usr/bin/vi\t/usr/bin/vim.basic", "/usr/bin/vi"),
            "relative link path": collected().replace("/usr/bin/vi\t", "usr/bin/vi\t"),
            "link with two targets": collected().replace("/usr/bin/vim.basic", "/usr/bin/a\t/usr/bin/b"),
        }
        for name, text in cases.items():
            with self.subTest(name), self.assertRaises(tool_tree.ToolTreeError):
                tool_tree.parse_tree(text)


class CompareTest(unittest.TestCase):
    def tree(self, **fields):
        return tool_tree.parse_tree(collected(**fields))

    def test_same_trees_have_no_differences_and_the_same_hash(self):
        self.assertEqual(tool_tree.compare_trees(self.tree(), self.tree()), {})
        self.assertEqual(tool_tree.tree_sha256(self.tree()), tool_tree.tree_sha256(self.tree()))

    def test_hash_does_not_depend_on_the_order_find_printed(self):
        reordered = collected().replace(f"{HASH_1}  /etc/wgetrc\n{HASH_1}  /usr/bin/curl",
                                        f"{HASH_1}  /usr/bin/curl\n{HASH_1}  /etc/wgetrc")
        self.assertNotEqual(reordered, collected())
        self.assertEqual(tool_tree.tree_sha256(tool_tree.parse_tree(reordered)), tool_tree.tree_sha256(self.tree()))

    def test_each_section_reports_its_own_change(self):
        cases = {
            "dpkg": ({"curl": "8.0.0"}, {"key": "curl", "baseline": "7.88.1", "ours": "8.0.0"}),
            "npm_global": ({"playwright": "1.65.0"}, {"key": "playwright", "baseline": "1.64.1", "ours": "1.65.0"}),
            "pip": ({"pip_version": "25.0"}, {"key": "pip", "baseline": "24.0", "ours": "25.0"}),
            "files": ({"curl_hash": HASH_2}, {"key": "/usr/bin/curl", "baseline": HASH_1, "ours": HASH_2}),
            "links": ({"link": "/usr/bin/vim.tiny"},
                      {"key": "/usr/bin/vi", "baseline": "/usr/bin/vim.basic", "ours": "/usr/bin/vim.tiny"}),
        }
        for section, (fields, row) in cases.items():
            with self.subTest(section):
                ours = self.tree(**fields)
                self.assertEqual(tool_tree.compare_trees(self.tree(), ours), {section: [row]})
                self.assertNotEqual(tool_tree.tree_sha256(self.tree()), tool_tree.tree_sha256(ours))

    def test_an_item_on_one_side_only_is_a_difference(self):
        baseline = self.tree()
        ours = tool_tree.parse_tree(collected().replace("jq=1.6", "wget=1.21"))
        self.assertEqual(tool_tree.compare_trees(baseline, ours)["dpkg"], [
            {"key": "jq", "baseline": "1.6", "ours": None},
            {"key": "wget", "baseline": None, "ours": "1.21"},
        ])


class CollectTest(unittest.TestCase):
    def test_runs_without_network_or_pull_on_the_content_id(self):
        fake = FakeDocker()
        tool_tree.collect_tree(BASE_ID, fake)
        args, timeout, max_stdout = fake.calls[0]
        self.assertEqual(args[:15], ["run", "--rm", "--pull", "never", "--network", "none", "--read-only",
                                     "--tmpfs", "/tmp", "--env", "npm_config_cache=/tmp", "--cap-drop", "ALL",
                                     "--security-opt", "no-new-privileges:true"])
        self.assertEqual(args[15:18], ["--entrypoint", "sh", BASE_ID])
        self.assertEqual(timeout, tool_tree.COLLECT_TIMEOUT_SECONDS)
        self.assertEqual(max_stdout, tool_tree.MAX_TREE_BYTES)

    def test_the_image_script_covers_tool_code_and_skips_docker_and_secret_files(self):
        fake = FakeDocker()
        tool_tree.collect_tree(BASE_ID, fake)
        args = fake.calls[0][0]
        script = args[args.index("-c") + 1]
        self.assertEqual(script, tool_tree.collect_script())
        for root in ("/usr", "/etc", "/opt", "/root/.cache", "/root/.bashrc", "/root/.profile"):
            self.assertIn(root, tool_tree.TREE_ROOTS)
        self.assertNotIn("/app", tool_tree.TREE_ROOTS)
        for path in ("/etc/hostname", "/etc/hosts", "/etc/resolv.conf", "/etc/shadow", "/etc/shadow-",
                     "/etc/gshadow", "/etc/gshadow-", "'/etc/ssl/private/*'"):
            self.assertEqual(script.count(f"! -path {path} "), 2, path)

    def test_refuses_a_tag_where_a_content_id_is_needed(self):
        fake = FakeDocker()
        with self.assertRaises(tool_tree.ToolTreeError):
            tool_tree.collect_tree("base:tag", fake)
        self.assertEqual(fake.calls, [])

    def test_nonzero_exit_is_an_error_even_with_good_output(self):
        with self.assertRaises(tool_tree.ToolTreeError):
            tool_tree.collect_tree(BASE_ID, FakeDocker(run_code=1))

    def test_non_utf8_output_is_an_error(self):
        with self.assertRaises(tool_tree.ToolTreeError):
            tool_tree.collect_tree(BASE_ID, lambda *_: BoundedResult(0, b"\xff", b""))


class CollectScriptOnRealFilesTest(unittest.TestCase):
    """이미지 대신 임시 트리에서 수집 스크립트를 실제 셸로 돌린다. 패키지 도구만 대역이다."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.stubs = self.root / "stubs"
        self.stubs.mkdir()
        self.stub("dpkg-query", "echo 'curl=7.88.1'")
        self.stub("npm", """echo '{"dependencies": {"playwright": {"version": "1.65.0"}}}'""")
        self.stub("python3", "echo 'pip==24.0'")
        self.usr, self.etc = self.root / "usr", self.root / "etc"
        self.code = self.usr / "lib/node_modules/playwright/cli.js"
        self.code.parent.mkdir(parents=True)
        self.code.write_text("first build", encoding="utf-8")
        (self.usr / "bin").mkdir()
        (self.usr / "bin/playwright").symlink_to("../lib/node_modules/playwright/cli.js")
        (self.etc / "ssl/private").mkdir(parents=True)
        (self.etc / "wgetrc").write_text("timeout=1", encoding="utf-8")
        (self.etc / "shadow").write_text("root:secret", encoding="utf-8")
        (self.etc / "ssl/private/key.pem").write_text("secret", encoding="utf-8")

    def stub(self, name, body):
        path = self.stubs / name
        path.write_text(f"#!/bin/sh\n{body}\n", encoding="utf-8")
        path.chmod(0o755)

    def run_script(self, roots=None):
        roots = [str(self.usr), str(self.etc), str(self.root / "missing")] if roots is None else roots
        excluded = [str(self.etc / "shadow"), str(self.etc / "ssl/private") + "/*"]
        env = {**os.environ, "PATH": f"{self.stubs}{os.pathsep}{os.environ['PATH']}"}
        return subprocess.run(["sh", "-c", tool_tree.collect_script(roots, excluded)], env=env, text=True,
                              capture_output=True, timeout=30, check=False)

    def collect(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        return tool_tree.parse_tree(result.stdout)

    def test_same_versions_and_links_with_different_code_is_a_difference(self):
        first = self.collect()
        self.code.write_text("second build", encoding="utf-8")
        second = self.collect()
        for section in ("dpkg", "npm_global", "pip", "links"):
            self.assertEqual(first[section], second[section], section)
        self.assertEqual(first["links"][str(self.usr / "bin/playwright")], "../lib/node_modules/playwright/cli.js")
        differences = tool_tree.compare_trees(first, second)
        self.assertEqual(list(differences), ["files"])
        self.assertEqual([row["key"] for row in differences["files"]], [str(self.code)])
        self.assertNotEqual(tool_tree.tree_sha256(first), tool_tree.tree_sha256(second))

    def test_secret_files_are_left_out_and_settings_are_kept(self):
        files = self.collect()["files"]
        self.assertIn(str(self.etc / "wgetrc"), files)
        self.assertNotIn(str(self.etc / "shadow"), files)
        self.assertNotIn(str(self.etc / "ssl/private/key.pem"), files)

    def test_names_with_spaces_and_equals_survive(self):
        (self.usr / "lib/a b=c").write_text("x", encoding="utf-8")
        (self.usr / "bin/d e=f").symlink_to("a b=c")
        tree = self.collect()
        self.assertIn(str(self.usr / "lib/a b=c"), tree["files"])
        self.assertEqual(tree["links"][str(self.usr / "bin/d e=f")], "a b=c")

    def test_a_failed_step_stops_the_script(self):
        self.stub("npm", "exit 3")
        result = self.run_script()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("#section files", result.stdout)

    def test_no_existing_root_is_a_failure_not_an_empty_scan(self):
        result = self.run_script([str(self.root / "missing")])
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("#section files", result.stdout)


class ResolveTest(unittest.TestCase):
    def test_refuses_failed_or_malformed_inspect(self):
        cases = {"exit with a valid id": FakeDocker(inspect_code=1, ids={"x": BASE_ID}),
                 "not an id": FakeDocker(ids={"x": "sha256:short"}), "missing": FakeDocker(ids={})}
        for name, fake in cases.items():
            with self.subTest(name), self.assertRaises(tool_tree.ToolTreeError):
                tool_tree.resolve_image_id("x", fake)


class MainTest(unittest.TestCase):
    def test_same_trees_exit_zero_and_write_a_private_report(self):
        with tempfile.TemporaryDirectory() as directory:
            code, out, stdout, _ = run_main(FakeDocker(), directory)
            self.assertEqual(code, 0)
            self.assertIn("도구 트리 같음", stdout)
            report = json.loads(Path(out).read_text(encoding="utf-8"))
            self.assertEqual(os.stat(out).st_mode & 0o777, 0o600)
        self.assertEqual(report["format"], tool_tree.REPORT_FORMAT)
        self.assertIs(report["same"], True)
        self.assertEqual(report["differences"], {})
        self.assertEqual(report["baseline"]["image_id"], BASE_ID)
        self.assertEqual(report["ours"]["image_id"], OURS_ID)
        self.assertEqual(report["baseline"]["tree_sha256"], report["ours"]["tree_sha256"])

    def test_different_trees_exit_one_and_name_the_package(self):
        fake = FakeDocker(trees={BASE_ID: collected(), OURS_ID: collected(playwright="1.65.0", curl_hash=HASH_2)})
        with tempfile.TemporaryDirectory() as directory:
            code, out, stdout, _ = run_main(fake, directory)
            report = json.loads(Path(out).read_text(encoding="utf-8"))
        self.assertEqual(code, 1)
        self.assertIs(report["same"], False)
        self.assertEqual(set(report["differences"]), {"npm_global", "files"})
        self.assertIn("npm_global 1, files 1", stdout)
        self.assertIn("playwright: 1.64.1 → 1.65.0", stdout)

    def test_the_same_image_on_both_sides_proves_nothing(self):
        fake = FakeDocker(ids={"base:tag": BASE_ID, "ours:tag": BASE_ID})
        with tempfile.TemporaryDirectory() as directory:
            code, out, _, stderr = run_main(fake, directory)
            self.assertFalse(os.path.exists(out))
        self.assertEqual(code, 2)
        self.assertIn("같은 이미지", stderr)
        self.assertFalse([call for call in fake.calls if call[0][0] == "run"])

    def test_collect_failure_exits_two_without_a_report(self):
        with tempfile.TemporaryDirectory() as directory:
            code, out, _, _ = run_main(FakeDocker(run_code=125), directory)
            self.assertFalse(os.path.exists(out))
        self.assertEqual(code, 2)

    def test_docker_failure_exits_two(self):
        def broken(*_):
            raise DockerError("docker_timeout")
        with tempfile.TemporaryDirectory() as directory:
            code, _, _, stderr = run_main(broken, directory)
        self.assertEqual(code, 2)
        self.assertIn("docker_timeout", stderr)

    def test_existing_report_is_not_overwritten_or_followed(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "tree.json"
            out.write_text("keep", encoding="utf-8")
            fake = FakeDocker()
            code, _, _, stderr = run_main(fake, directory)
            self.assertEqual(out.read_text(encoding="utf-8"), "keep")
            self.assertEqual(code, 2)
            self.assertIn("이미 있음", stderr)
            self.assertEqual(fake.calls, [])
            out.unlink()
            target = Path(directory) / "target"
            out.symlink_to(target)
            fake = FakeDocker()
            code, _, _, _ = run_main(fake, directory)
            self.assertEqual(code, 2)
            self.assertFalse(target.exists())
            self.assertEqual(fake.calls, [])  # 재기 전에 거부한다

    def test_report_appearing_during_collection_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "tree.json"

            def appear(args, timeout, max_stdout):
                if args[0] == "run" and not out.exists():
                    out.write_text("keep", encoding="utf-8")
                return fake(args, timeout, max_stdout)
            fake = FakeDocker()
            code, _, _, stderr = run_main(appear, directory)
            self.assertEqual(out.read_text(encoding="utf-8"), "keep")
        self.assertEqual(code, 2)
        self.assertIn("이미 있음", stderr)


if __name__ == "__main__":
    unittest.main()
