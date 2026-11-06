"""Exercise the path gate with real commit ranges, deletions and renames."""

import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location(
    "web_changed", Path(__file__).with_name("web-changed.py")
)
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class WebChangedTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="web-gate-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.git("init", "-q")
        self.git("config", "user.name", "CI fixture")
        self.git("config", "user.email", "ci@example.invalid")
        self.write("web/src/page.tsx", "export default 1;\n")
        self.write("docs/example.md", "Example\n")
        self.base = self.commit()

    def git(self, *args):
        return subprocess.check_output(
            ["git", "-c", "commit.gpgsign=false", *args], cwd=self.root
        ).decode().strip()

    def write(self, path, text="changed\n"):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def commit(self):
        self.git("add", "-A")
        self.git("commit", "-qm", "test fixture")
        return self.git("rev-parse", "HEAD")

    def changed(self, base=None, head=None):
        head = head or self.git("rev-parse", "HEAD")
        run = subprocess.run

        def in_repo(*args, **kwargs):
            return run(*args, cwd=self.root, **kwargs)

        with mock.patch.object(gate.subprocess, "run", side_effect=in_repo):
            return gate.web_changed(base or self.base, head)

    def test_backend_docs_and_evaluation_only_skip(self):
        for path in ("server/example.go", "docs/guide.md", "scripts/eval/test.py"):
            with self.subTest(path=path):
                self.write(path)
                self.commit()
                self.assertFalse(self.changed())

    def test_web_source_assets_config_and_lockfile_build(self):
        for path in ("web/src/page.tsx", "web/public/icon.svg", "web/messages/ko.json",
                     "web/next.config.mjs", "web/package-lock.json"):
            with self.subTest(path=path):
                self.write(path)
                self.commit()
                self.assertTrue(self.changed())

    def test_entire_multi_commit_push_is_checked(self):
        self.write("web/src/page.tsx", "export default 2;\n")
        self.commit()
        self.write("server/example.go")
        self.commit()
        self.assertTrue(self.changed())

    def test_pr_merge_does_not_revalidate_base_only_web_change(self):
        self.git("checkout", "-qb", "feature")
        self.write("server/example.go")
        self.commit()
        self.git("checkout", "-qb", "dev", self.base)
        self.write("web/package-lock.json")
        current_base = self.commit()
        self.git("merge", "-q", "--no-ff", "feature", "-m", "test merge")
        self.assertFalse(self.changed(base=current_base))

    def test_deleted_web_file_builds(self):
        (self.root / "web/src/page.tsx").unlink()
        self.commit()
        self.assertTrue(self.changed())

    def test_rename_out_of_web_builds(self):
        self.git("mv", "web/src/page.tsx", "docs/moved.tsx")
        self.commit()
        self.assertTrue(self.changed())

    def test_rename_into_web_builds(self):
        self.git("mv", "docs/example.md", "web/example.md")
        self.commit()
        self.assertTrue(self.changed())

    def test_newline_in_filename_is_not_a_separator(self):
        self.write("web/src/line\nbreak.tsx")
        self.commit()
        self.assertTrue(self.changed())

    def test_root_build_inputs_build(self):
        for path in ("package-lock.json", ".npmrc", "pnpm-workspace.yaml"):
            with self.subTest(path=path):
                self.write(path)
                self.commit()
                self.assertTrue(self.changed())

    def test_gate_and_workflow_changes_build(self):
        for path in (".github/workflows/ci.yml", "scripts/ci/web-changed.py",
                     "scripts/ci/test_web_changed.py", "web/vercel.json"):
            with self.subTest(path=path):
                self.write(path)
                self.commit()
                self.assertTrue(self.changed())

    def test_unchanged_commit_skips(self):
        self.assertFalse(self.changed())

    def test_missing_zero_or_invalid_base_builds(self):
        for base in ("", "0" * 40, "--output=untrusted", "f" * 40):
            with self.subTest(base=base):
                self.assertTrue(gate.web_changed(base, self.base))

    def test_git_error_runs_validation(self):
        with mock.patch.object(gate.subprocess, "run") as run:
            run.return_value = subprocess.CompletedProcess([], 128, stdout=b"")
            self.assertTrue(gate.web_changed(self.base, self.base))


if __name__ == "__main__":
    unittest.main()
