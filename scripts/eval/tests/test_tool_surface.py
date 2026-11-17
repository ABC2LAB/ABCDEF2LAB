"""도구 면 측정(tool_surface, 탐색 분리 배치 결정안 §4) 시험. Go 원문 대조·셸 찾기 분류·MCP stdio 세션·보고서."""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import tool_surface  # noqa: E402
from runner_docker import BoundedResult  # noqa: E402
from tool_surface import (  # noqa: E402
    BASELINE_MCP_ARGS, MCP_ALLOWED, SANDBOX_DIR_GLOBS, SANDBOX_FILE_GLOBS, SANDBOX_FIXED, SANDBOX_PATH, TRIPLETS,
    ToolSurfaceError, classify, compare_cli, mcp_capability_args, mcp_side, mcp_tools, sandbox_spec, tool_digest,
)

ROOT = Path(__file__).resolve().parents[3]
H = "a" * 64


class TestGoBinding(unittest.TestCase):
    """러너 사본이 Go 원문과 같은지(Go가 바뀌면 깨져서 같이 고치게 한다)."""

    def setUp(self):
        self.helper = (ROOT / "e2sandbox/helper_linux.go").read_text(encoding="utf-8")

    def test_sandbox_path_env(self):
        self.assertEqual(re.findall(r'"PATH=([^"]+)"', self.helper), [SANDBOX_PATH])

    def test_runtime_paths(self):
        body = re.search(r"func sandboxRuntimePaths\(.*?\n}\n", self.helper, re.S).group(0)
        fixed = re.search(r"paths := \[\]string\{(.*?)\n\t\}", body, re.S).group(1)
        items = [part.strip() for part in fixed.replace("\n", " ").split(",") if part.strip()]
        rendered = [re.sub(r'^"([^"]*)" \+ triplet$', r"\1{triplet}", item).strip('"') for item in items]
        self.assertEqual(tuple(rendered), SANDBOX_FIXED)
        self.assertEqual(re.findall(r'range \[\]string\{([^}]*)\}', body), [", ".join(f'"{g}"' for g in SANDBOX_DIR_GLOBS)])
        self.assertEqual(re.findall(r'shared, err := glob\("([^"]+)"\)', body), list(SANDBOX_FILE_GLOBS))
        self.assertEqual(dict(re.findall(r'case "(\w+)":\n\t\ttriplet = "([^"]+)"', self.helper)), TRIPLETS)

    def test_mcp_allowlist(self):
        source = (ROOT / "server/mcp_tool_policy.go").read_text(encoding="utf-8")
        block = re.search(r"var playwrightMCPAllowedTools = map\[string\]bool\{(.*?)\n\}", source, re.S).group(1)
        self.assertEqual(frozenset(re.findall(r'"(\w+)":\s+true', block)), MCP_ALLOWED)

    def test_original_browser_mcp_seed_is_disabled(self):
        # 원본(e53a79f)과 같은 시드다: stdio `npx @playwright/mcp --headless`, enabled=false. 러너는 이 표를 건드리지 않는다.
        source = (ROOT / "db/db.go").read_text(encoding="utf-8")
        self.assertIn("VALUES ('browser', 'stdio', 'npx', $1, '{}', false)", source)
        self.assertIn('`["@playwright/mcp","--headless"]`', source)
        self.assertEqual(BASELINE_MCP_ARGS, ("--headless",))
        runner_sources = " ".join(path.read_text(encoding="utf-8") for path in (ROOT / "scripts/eval").glob("runner*.py"))
        self.assertNotIn("mcp_servers", runner_sources)


def entry(name, directory, target=None, sha=H, shebang=None, interpreter=None):
    return {"name": name, "dir": directory, "target": target or f"{directory}/{name}", "sha256": sha, "shebang": shebang,
            "interpreter_target": interpreter}


MOUNTS = ["/usr/bin", "/usr/local/bin", "/usr/lib/x86_64-linux-gnu"]


class TestClassify(unittest.TestCase):
    def test_full_path_first_wins(self):
        out = classify({"mounts": None, "entries": [entry("curl", "/usr/local/bin", sha="1" * 64), entry("curl", "/usr/bin")]},
                       "/usr/local/bin:/usr/bin")
        self.assertEqual(out["commands"], {"curl": {"dir": "/usr/local/bin", "sha256": "1" * 64}})
        self.assertNotIn("hidden", out)
        self.assertEqual(out["sha256"], tool_surface.canonical_sha256({"curl": "1" * 64}))

    def test_sandbox_hides_targets_outside_and_falls_through(self):
        entries = [
            entry("playwright-cli", "/usr/local/bin", target="/usr/local/lib/node_modules/@playwright/cli/cli.js",
                  shebang=["/usr/bin/env", "node"]),
            entry("tool", "/usr/local/bin", target="/opt/tool/bin/tool"),
            entry("tool", "/usr/bin", sha="2" * 64),
            entry("node", "/usr/local/bin"),
            entry("script", "/usr/bin", shebang=["/usr/bin/env", "-S", "node", "--x"]),
        ]
        out = classify({"mounts": MOUNTS, "entries": entries}, SANDBOX_PATH)
        self.assertEqual(set(out["commands"]), {"tool", "node", "script"})
        self.assertEqual(out["commands"]["tool"], {"dir": "/usr/bin", "sha256": "2" * 64})  # 앞 파일이 안 보이면 다음 디렉터리
        self.assertEqual(out["hidden"], {"playwright-cli": "target_outside_sandbox"})

    def test_interpreters(self):
        entries = [
            entry("py", "/usr/bin", shebang=["/usr/local/lib/pyenv/python"], interpreter="/usr/local/lib/pyenv/python"),
            entry("gone", "/usr/bin", shebang=["/nonexistent/sh"], interpreter=None),
            entry("needs", "/usr/bin", shebang=["/usr/bin/env", "hiddenexe"]),
            entry("hiddenexe", "/usr/local/bin", target="/opt/x"),
            entry("ok", "/usr/bin", shebang=["/bin/sh"], interpreter="/usr/bin/dash"),
            entry("loop", "/usr/bin", shebang=["/usr/bin/env", "loop"]),
        ]
        out = classify({"mounts": MOUNTS, "entries": entries}, SANDBOX_PATH)
        self.assertEqual(set(out["commands"]), {"ok"})
        self.assertEqual(out["hidden"], {"py": "interpreter_unavailable", "gone": "interpreter_missing",
                                         "needs": "interpreter_unavailable", "hiddenexe": "target_outside_sandbox",
                                         "loop": "interpreter_unavailable"})
        full = classify({"mounts": None, "entries": entries}, SANDBOX_PATH)
        self.assertEqual(set(full["commands"]), {"py", "needs", "hiddenexe", "ok"})  # 샌드박스 밖에서는 경로 조건이 없다

    def test_mount_prefix_is_a_path_boundary(self):
        out = classify({"mounts": ["/usr/bin"], "entries": [entry("x", "/usr/bin", target="/usr/binx/x")]}, "/usr/bin")
        self.assertEqual(out["hidden"], {"x": "target_outside_sandbox"})

    def test_invalid_collector_output(self):
        for bad in ({}, {"entries": "x"}, {"mounts": None, "entries": [entry("a", "/elsewhere")]},
                    {"mounts": None, "entries": [entry("a", "/usr/bin", sha="zz")]}, {"mounts": None, "entries": ["x"]}):
            with self.subTest(bad=bad), self.assertRaises(ToolSurfaceError):
                classify(bad, "/usr/bin")

    def test_sandbox_spec(self):
        spec = sandbox_spec("amd64")
        self.assertIn("/usr/lib/x86_64-linux-gnu", spec["fixed"])
        self.assertNotIn("{triplet}", " ".join(spec["fixed"]))
        with self.assertRaises(ToolSurfaceError):
            sandbox_spec("riscv64")


class FakeProcess:
    """stdout은 미리 채운 실제 pipe다(세션이 selectors·os.read로 읽는다). stdin은 보낸 줄을 모은다."""

    def __init__(self, responses: list, close: bool = True):
        read, write = os.pipe()
        for item in responses:
            os.write(write, item if isinstance(item, bytes) else json.dumps(item).encode() + b"\n")
        if close:
            os.close(write)
        else:
            self._write = write
        self.stdout = os.fdopen(read, "rb", buffering=0)
        self.sent: list[dict] = []
        outer = self

        class Stdin:
            def write(self, data):
                outer.sent.append(json.loads(data))

            def flush(self):
                pass

            def close(self):
                pass

        self.stdin = Stdin()
        self.waited = False

    def wait(self, timeout=None):
        self.waited = True
        return 0

    def kill(self):
        pass


def tool(name, **extra):
    return {"name": name, "description": f"{name} tool", "inputSchema": {"type": "object"}, **extra}


INIT = {"jsonrpc": "2.0", "id": 1, "result": {"protocolVersion": "2025-06-18", "serverInfo": {"name": "Playwright", "version": "9.9"}}}


class TestMcpSession(unittest.TestCase):
    def run_session(self, responses, **kw):
        process = FakeProcess(responses, close=kw.pop("close", True))
        calls = []

        def popen(command, **options):
            calls.append((command, options))
            return process

        return mcp_tools("sha256:" + "4" * 64, ["--headless"], popen, **kw), process, calls

    def test_pages_and_notifications(self):
        responses = [{"jsonrpc": "2.0", "method": "notifications/message", "params": {}}, INIT,
                     {"jsonrpc": "2.0", "id": 2, "result": {"tools": [tool("b")], "nextCursor": "c1"}},
                     {"jsonrpc": "2.0", "id": 3, "result": {"tools": [tool("a")]}}]
        result, process, calls = self.run_session(responses)
        self.assertEqual(([t["name"] for t in result["tools"]], result["server_version"]), (["b", "a"], "9.9"))
        self.assertEqual([m.get("method") for m in process.sent], ["initialize", "notifications/initialized", "tools/list", "tools/list"])
        self.assertEqual(process.sent[3]["params"], {"cursor": "c1"})
        command = calls[0][0]
        for flag in (["--network", "none"], ["--read-only"], ["--entrypoint", "playwright-mcp"], ["--cap-drop", "ALL"]):
            self.assertTrue(any(command[i:i + len(flag)] == flag for i in range(len(command))), flag)
        self.assertEqual(command[-1], "--headless")
        self.assertTrue(process.waited)

    def test_failures_have_fixed_codes(self):
        cases = {
            "mcp_request_failed": [INIT, {"jsonrpc": "2.0", "id": 2, "error": {"code": -1, "message": "secret"}}],
            "mcp_session_closed": [INIT],
            "mcp_output_invalid": [INIT, b"not json\n"],
        }
        for code, responses in cases.items():
            with self.subTest(code=code), self.assertRaises(ToolSurfaceError) as caught:
                self.run_session(responses)
            self.assertEqual(str(caught.exception), code)
        with self.assertRaises(ToolSurfaceError) as caught:
            self.run_session([INIT], close=False, timeout=0.2)
        self.assertEqual(str(caught.exception), "mcp_timeout")
        with mock.patch.object(tool_surface, "MAX_MCP_BYTES", 64), self.assertRaises(ToolSurfaceError) as caught:
            self.run_session([INIT, {"jsonrpc": "2.0", "id": 2, "result": {"tools": [tool("x" * 100)]}}])
        self.assertEqual(str(caught.exception), "mcp_output_too_large")
        pages = [INIT] + [{"jsonrpc": "2.0", "id": 2 + i, "result": {"tools": [tool(f"t{i}")], "nextCursor": "n"}} for i in range(20)]
        with self.assertRaises(ToolSurfaceError) as caught:
            self.run_session(pages)
        self.assertEqual(str(caught.exception), "mcp_too_many_pages")
        with self.assertRaises(ToolSurfaceError):
            mcp_tools("not-an-id", [], lambda *a, **k: None)


class TestDigestsAndReport(unittest.TestCase):
    def test_tool_digest_order_free_and_strict(self):
        a = tool_digest([tool("b"), tool("a")])
        self.assertEqual((a["names"], a["sha256"]), (["a", "b"], tool_digest([tool("a"), tool("b")])["sha256"]))
        self.assertNotEqual(a["sha256"], tool_digest([tool("a"), tool("b", description="changed")])["sha256"])
        for bad in ([tool("a"), tool("a")], [{"description": "x"}], ["x"]):
            with self.subTest(bad=bad), self.assertRaises(ToolSurfaceError):
                tool_digest(bad)

    def test_exposed_lists(self):
        listed = {"server_version": "v", "tools": [tool("browser_click"), tool("browser_run_code_unsafe")]}
        ours = mcp_side("sha256:x", ["--headless"], "0.0.84", listed, MCP_ALLOWED, "product_allowlist")
        self.assertEqual((ours["raw"]["names"], ours["exposed"]["names"]), (["browser_click", "browser_run_code_unsafe"], ["browser_click"]))
        base = mcp_side("sha256:x", ["--headless"], "0.0.84", listed, None, "original_seed_disabled")
        self.assertEqual((base["raw"]["sha256"], base["exposed"]["names"]), (ours["raw"]["sha256"], []))

    def test_compare_cli(self):
        b = {"commands": {"a": {"sha256": "1"}, "b": {"sha256": "1"}, "c": {"sha256": "1"}}}
        o = {"commands": {"b": {"sha256": "1"}, "c": {"sha256": "2"}, "d": {"sha256": "1"}}}
        self.assertEqual(compare_cli(b, o), {"baseline_only": ["a"], "ours_only": ["d"], "content_differs": ["c"]})

    def test_capability_args(self):
        command = ["--headless", "--isolated", "--proxy-server", "http://p:3128", "--port", "8931", "--new-flag"]
        self.assertEqual(mcp_capability_args(command), ["--headless", "--isolated", "--new-flag"])
        with self.assertRaises(ToolSurfaceError):
            mcp_capability_args(["--port"])

    def test_build_report_wires_both_sides(self):
        ids = {"b": "sha256:" + "1" * 64, "o": "sha256:" + "2" * 64, "w": "sha256:" + "3" * 64}
        seen = []

        def run(args, timeout, max_stdout):
            seen.append(list(args))
            if args[:2] == ["image", "inspect"]:
                fmt, ref = args[3], args[4]
                if fmt == "{{.Id}}":
                    return BoundedResult(0, ids[ref].encode(), b"")
                if fmt == "{{.Architecture}}":
                    return BoundedResult(0, b"amd64\n", b"")
                return BoundedResult(0, json.dumps(["PATH=/usr/bin:/bin", "HOME=/x"]).encode(), b"")
            if args[-1] == "--version":
                return BoundedResult(0, b"Version 0.0.84\n", b"")
            config = json.loads(args[-1])
            mounts = None if config["sandbox"] is None else ["/usr/bin"]
            entries = [entry("curl", "/usr/bin")] + ([entry("npx", "/usr/bin", target="/usr/lib/node_modules/npm/npx.js")]
                                                    if mounts is None else [])
            return BoundedResult(0, json.dumps({"mounts": mounts, "entries": entries}).encode(), b"")

        listed = [INIT, {"jsonrpc": "2.0", "id": 2, "result": {"tools": [tool("browser_click"), tool("browser_drop")]}}]
        report = tool_surface.build_report("b", "o", "w", run, lambda command, **o: FakeProcess(listed), {})
        self.assertEqual(report["format"], "abc2lab.tool-surface/v1")
        self.assertEqual((report["baseline"]["cli"]["path"], report["ours"]["cli"]["path"]), ("/usr/bin:/bin", SANDBOX_PATH))
        self.assertEqual(report["cli_differences"]["baseline_only"], ["npx"])
        self.assertEqual((report["baseline"]["mcp"]["exposed"]["names"], report["ours"]["mcp"]["exposed"]["names"]),
                         ([], ["browser_click"]))
        self.assertEqual(report["common"], {"mcp_raw_same": True, "mcp_version_same": True})
        self.assertEqual((report["ours"]["mcp"]["image_id"], report["ours"]["mcp"]["args"]), (ids["w"], list(tool_surface.OURS_MCP_ARGS)))
        collect = [a for a in seen if a and a[0] == "run" and "python3" in a]
        self.assertEqual(len(collect), 2)
        sandbox = json.loads(collect[1][-1])["sandbox"]
        self.assertIn("/usr/lib/x86_64-linux-gnu", sandbox["fixed"])
        for args in (a for a in seen if a and a[0] == "run"):
            self.assertIn("none", args)  # 측정 컨테이너는 늘 망 없음


class TestCollectorScript(unittest.TestCase):
    """컨테이너 안 수집 스크립트를 이 머신의 임시 디렉터리로 돌려 본다(Docker 없이)."""

    def test_collects_links_scripts_and_mounts(self):
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "bin").mkdir()
            (root / "lib").mkdir()
            (root / "outside").mkdir()
            real = root / "outside" / "cli.js"
            real.write_text("#!/usr/bin/env node\nconsole.log(1)\n")
            real.chmod(0o755)
            (root / "bin" / "cli").symlink_to(real)
            exe = root / "bin" / "exe"
            exe.write_bytes(b"\x7fELF")
            exe.chmod(0o755)
            (root / "bin" / "plain").write_text("not executable")
            config = {"path": f"{root / 'bin'}:{root / 'missing'}",
                      "sandbox": {"fixed": [str(root / "bin"), str(root / "nope")], "dir_globs": [str(root / "li*")], "file_globs": []}}
            out = subprocess.run([sys.executable, "-I", "-c", tool_surface.COLLECTOR, json.dumps(config)], capture_output=True, check=True)
            collected = json.loads(out.stdout)
        self.assertEqual(collected["mounts"], sorted([os.path.realpath(root / "bin"), os.path.realpath(root / "lib")]))
        rows = {e["name"]: e for e in collected["entries"]}
        self.assertEqual(set(rows), {"cli", "exe"})
        self.assertEqual((rows["cli"]["target"], rows["cli"]["shebang"]), (os.path.realpath(real), ["/usr/bin/env", "node"]))
        self.assertIsNone(rows["exe"]["shebang"])
        self.assertEqual(classify(collected, config["path"])["hidden"], {"cli": "target_outside_sandbox"})


if __name__ == "__main__":
    unittest.main()
