"""탐색 분리 배치 O 회차 제품 보호 입력 생성·정리(결정안 §3 B(a), 4-7c-1) 시험."""

from __future__ import annotations

import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_split import SplitInputError, read_workflow_defaults  # noqa: E402
from runner_split_inputs import (  # noqa: E402
    CA_VALID_DAYS, COMPOSE_FILE_ENV, FILES, PRIVATE_DIRECTORY, SplitInputsError, prepare_split_inputs, remove_split_inputs, run_openssl,
)

MANIFEST = {"version": 3, "profile_id": 1, "approval_id": "synthetic-approval"}


def counter_bytes():
    state = {"n": 0}

    def take(size):
        state["n"] += 1
        return bytes([state["n"]]) * size
    return take


class FakeOpenssl:
    """openssl req처럼 -keyout·-out에 파일을 만든다(인증서는 umask대로 0644)."""

    def __init__(self, code=0, create=True, link_key=False):
        self.code, self.create, self.link_key = code, create, link_key
        self.calls = []

    def __call__(self, args):
        self.calls.append(list(args))
        if self.create:
            key, cert = Path(args[args.index("-keyout") + 1]), Path(args[args.index("-out") + 1])
            if self.link_key:
                key.symlink_to("/etc/hostname")
            else:
                key.write_bytes(b"-----BEGIN PRIVATE KEY-----\n")
                os.chmod(key, 0o600)
            cert.write_bytes(b"-----BEGIN CERTIFICATE-----\n")
            os.chmod(cert, 0o644)
        return self.code


class TestPrepare(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.run_dir = Path(self._tmp.name)
        self.defaults, _ = read_workflow_defaults()

    def tearDown(self):
        self._tmp.cleanup()

    def prepare(self, openssl=None, **changes):
        options = dict(manifest=MANIFEST, meter_port=8080, explore_port=8081, evidence_max_bytes=4 << 20,
                       workflow_defaults=self.defaults, wide_writes=True, valid_for_hours=4, max_runs=2,
                       openssl=openssl or FakeOpenssl(), random_bytes=counter_bytes())
        options.update(changes)
        return prepare_split_inputs(self.run_dir, **options)

    def test_files_permissions_and_compose_env(self):
        openssl = FakeOpenssl()
        inputs = self.prepare(openssl)
        directory = self.run_dir / PRIVATE_DIRECTORY
        self.assertEqual((inputs.directory, stat.S_IMODE(os.stat(directory).st_mode)), (directory, 0o700))
        self.assertEqual(sorted(os.listdir(directory)), sorted(FILES.values()))
        for name, path in inputs.files.items():
            with self.subTest(file=name):
                info = os.lstat(path)
                self.assertTrue(stat.S_ISREG(info.st_mode))
                self.assertEqual((stat.S_IMODE(info.st_mode), info.st_uid), (0o600, os.geteuid()))
        self.assertEqual(inputs.compose_env, {env: str(directory / FILES[name]) for env, name in COMPOSE_FILE_ENV.items()})
        self.assertEqual(set(inputs.compose_env), {"PRODUCT_INSTALL_FILE", "PRODUCT_POSTGRES_PASSWORD_FILE",
                                                   "EXPLORE_BOUNDARY_CONFIG_FILE", "EXPLORE_CA_CERT_FILE",
                                                   "EXPLORE_CA_KEY_FILE", "EXPLORE_PATH_KEY_FILE"})
        # README 절차와 같은 CA 인자(P-256·CA:TRUE·keyCertSign), 유효 기간만 회차용.
        args = openssl.calls[0]
        self.assertEqual(args[:7], ["req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes"])
        self.assertEqual(args[args.index("-days") + 1], str(CA_VALID_DAYS))
        self.assertIn("basicConstraints=critical,CA:TRUE", args)
        self.assertIn("keyUsage=critical,keyCertSign", args)

    def test_contents_and_secrets(self):
        inputs = self.prepare()
        install = json.loads(inputs.files["install"].read_text(encoding="utf-8"))
        self.assertEqual(install["admin_password"], inputs.admin.reveal())
        self.assertEqual({k: v for k, v in install.items() if k != "admin_password"}, inputs.install_public)
        self.assertNotIn("admin_password", inputs.install_public)
        self.assertNotIn(inputs.admin.reveal(), repr(inputs))
        self.assertEqual(install["target_policy"]["workflow"], dict(self.defaults, allow_reversible_writes=True))
        self.assertEqual(json.loads(inputs.files["boundary"].read_text(encoding="utf-8")), inputs.boundary)
        self.assertEqual(inputs.boundary["eval_upstream"], "10.252.11.20:8081")
        password = inputs.files["postgres_password"].read_bytes()
        self.assertTrue(password and b"\n" not in password)
        self.assertNotEqual(password.decode(), inputs.admin.reveal())  # 비밀번호 둘은 서로 다른 무작위 값
        self.assertEqual(len(inputs.files["path_key"].read_bytes()), 32)
        self.assertGreaterEqual(len(inputs.admin.reveal()), 32)

    def test_failures_leave_nothing_behind(self):
        cases = {
            "openssl_fails": (FakeOpenssl(code=1), {}, SplitInputsError, "split_ca_failed"),
            "openssl_no_files": (FakeOpenssl(create=False), {}, SplitInputsError, "split_ca_failed"),
            "openssl_key_link": (FakeOpenssl(link_key=True), {}, SplitInputsError, "split_ca_failed"),
            "bad_manifest": (FakeOpenssl(), {"manifest": dict(MANIFEST, version=2)}, SplitInputError, "split_install_input_invalid"),
            "bad_port": (FakeOpenssl(), {"explore_port": 0}, SplitInputError, "split_boundary_input_invalid"),
        }
        for name, (openssl, changes, error, code) in cases.items():
            with self.subTest(case=name):
                with self.assertRaises(error) as caught:
                    self.prepare(openssl, **changes)
                self.assertEqual(str(caught.exception), code)
                self.assertFalse((self.run_dir / PRIVATE_DIRECTORY).exists())

    @unittest.skipIf(shutil.which("openssl") is None, "openssl 없음")
    def test_real_openssl_makes_a_p256_ca(self):
        inputs = self.prepare(run_openssl)
        shown = subprocess.run(["openssl", "x509", "-noout", "-text", "-in", str(inputs.files["ca_cert"])],
                               capture_output=True, text=True, check=True).stdout
        self.assertIn("prime256v1", shown)
        self.assertIn("CA:TRUE", shown)
        self.assertIn("Certificate Sign", shown)
        self.assertIn("CN = JinDo explore boundary CA", shown)
        key = subprocess.run(["openssl", "pkey", "-noout", "-text", "-in", str(inputs.files["ca_key"])],
                             capture_output=True, text=True, check=True).stdout
        self.assertIn("prime256v1", key)
        self.assertEqual(stat.S_IMODE(os.stat(inputs.files["ca_key"]).st_mode), 0o600)

    def test_existing_directory_is_refused(self):
        (self.run_dir / PRIVATE_DIRECTORY).mkdir()
        (self.run_dir / PRIVATE_DIRECTORY / "install.json").write_text("old")
        with self.assertRaises(SplitInputsError) as caught:
            self.prepare()
        self.assertEqual(str(caught.exception), "split_input_directory_failed")
        self.assertEqual((self.run_dir / PRIVATE_DIRECTORY / "install.json").read_text(), "old")  # 손대지 않는다


class TestRemove(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.directory = Path(self._tmp.name) / PRIVATE_DIRECTORY
        self.directory.mkdir(mode=0o700)
        for name in FILES.values():
            (self.directory / name).write_bytes(b"x")

    def tearDown(self):
        self._tmp.cleanup()

    def test_removes_known_files_and_directory(self):
        remove_split_inputs(self.directory)
        self.assertFalse(self.directory.exists())
        remove_split_inputs(self.directory, missing_ok=True)
        with self.assertRaises(SplitInputsError):
            remove_split_inputs(self.directory)

    def test_unknown_file_keeps_directory(self):
        (self.directory / "other").write_bytes(b"y")
        with self.assertRaises(SplitInputsError) as caught:
            remove_split_inputs(self.directory)
        self.assertEqual(str(caught.exception), "split_input_cleanup_failed")
        self.assertEqual(os.listdir(self.directory), ["other"])  # 알려진 파일만 지우고 남은 것은 그대로


if __name__ == "__main__":
    unittest.main()
