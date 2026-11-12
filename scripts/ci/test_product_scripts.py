"""Product scripts: no Docker/DB calls, real shell guards and secret handoff."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ProductScripts(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.repo = root / "repo"
        (self.repo / "scripts").mkdir(parents=True)
        for name in ["reset-password.sh", "scripts/product-compose.sh"]:
            shutil.copyfile(ROOT / name, self.repo / name)
            (self.repo / name).chmod(0o700)
        self.install = root / "install.json"
        self.password = root / "postgres-password"
        for path in [self.install, self.password]:
            path.write_text("owned-synthetic-fixture")
            path.chmod(0o600)
        self.log = root / "calls.jsonl"
        bindir = root / "bin"
        bindir.mkdir()
        docker = bindir / "docker"
        docker.write_text('''#!/usr/bin/env python3
import json, os, sys
args=sys.argv[1:]
record={"args":args,"plaintext_env":"ABC2LAB_RESET_PASSWORD" in os.environ}
if args[-1:]==["product-password-hash"]:
 data=sys.stdin.read();record["stdin_is_secret"]=data=="synthetic-reset-password"
 print("$2a$12$"+"A"*53)
elif "exec" in args:
 sql=sys.stdin.read();record["sql_has_extension"]="CREATE EXTENSION" in sql
 record["sql_uses_hash"]="\\\\getenv newhash ABC2LAB_RESET_HASH" in sql and ":'newhash'" in sql
 record["hash_present"]=bool(os.environ.get("ABC2LAB_RESET_HASH"))
with open(os.environ["FIXTURE_CALLS"],"a") as out:out.write(json.dumps(record)+"\\n")
''')
        docker.chmod(0o700)
        self.env = {**os.environ, "PATH": str(bindir) + os.pathsep + os.environ["PATH"],
                    "PRODUCT_SOURCE_SHA": "a" * 40, "PRODUCT_INSTALL_FILE": str(self.install),
                    "PRODUCT_POSTGRES_PASSWORD_FILE": str(self.password), "FIXTURE_CALLS": str(self.log)}

    def run_script(self, name="scripts/product-compose.sh", data="", args=("config", "--quiet")):
        return subprocess.run([str(self.repo / name), *args], input=data, text=True,
                              capture_output=True, env=self.env, timeout=20)

    def calls(self):
        return [json.loads(s) for s in self.log.read_text().splitlines()] if self.log.exists() else []

    def test_private_files_outside_repository_are_accepted(self):
        self.assertEqual(self.run_script().returncode, 0)
        self.assertEqual(len(self.calls()), 1)

    def test_unprotected_or_repository_inputs_never_invoke_docker(self):
        inside = self.repo / "private.json"
        inside.write_text("fixture")
        inside.chmod(0o600)
        symlink = Path(self.tmp.name) / "outside-link"
        symlink.symlink_to(inside)
        for path, permissions in [(self.install, 0o644), (self.install, 0o640), (inside, 0o600), (symlink, None)]:
            with self.subTest(path=path.name, mode=permissions):
                if permissions is not None:
                    path.chmod(permissions)
                self.env["PRODUCT_INSTALL_FILE"] = str(path)
                self.assertNotEqual(self.run_script().returncode, 0)
                self.assertEqual(self.calls(), [])

    def test_reset_only_writes_hash_without_schema_or_plaintext_env(self):
        result = self.run_script("reset-password.sh", "synthetic-reset-password\n" * 2, ())
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        self.assertEqual(len(calls), 3)
        self.assertTrue(calls[1]["stdin_is_secret"])
        self.assertTrue(calls[2]["sql_uses_hash"])
        self.assertTrue(calls[2]["hash_present"])
        self.assertFalse(calls[2]["sql_has_extension"])
        self.assertFalse(any(c["plaintext_env"] for c in calls))
        self.assertNotIn("synthetic-reset-password", result.stdout + result.stderr + json.dumps(calls))
