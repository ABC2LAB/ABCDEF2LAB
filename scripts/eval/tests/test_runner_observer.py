"""O 회차 화면 관찰용 관리자 로그인 파일(runner_observer, 2026-11-20 사용자)."""

from __future__ import annotations

import copy
import dataclasses
import io
import os
import stat
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import runner_split_main  # noqa: E402
from runner_config import RunnerConfigError, parse_runner_config  # noqa: E402
from runner_docker import DockerError  # noqa: E402
from runner_engine import AdminSecret  # noqa: E402
from runner_observer import ObserverFileError, remove_observer_password, write_observer_password  # noqa: E402
from runner_split_config import SplitConfigError  # noqa: E402
from runner_split_main import run_split_ours  # noqa: E402
from test_preservation_record import model_options  # noqa: E402
from test_runner_config import runner_config  # noqa: E402
from test_runner_split_config import split_block  # noqa: E402
from test_runner_split_inputs import FakeOpenssl  # noqa: E402
from test_runner_split_main import Fixture, preflight_for  # noqa: E402

SECRET = "observer-secret-value-1"


class TestObserverFile(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self._tmp.name) / "observer"
        self.dir.mkdir(mode=0o700)
        self.path = self.dir / "admin.txt"

    def tearDown(self):
        self._tmp.cleanup()

    def test_writes_a_private_file_and_removes_it(self):
        write_observer_password(self.path, AdminSecret(SECRET))
        info = os.lstat(self.path)
        self.assertEqual(stat.S_IMODE(info.st_mode), 0o600)
        self.assertEqual(self.path.read_text(encoding="utf-8"), f"아이디: ABC2LAB\n비밀번호: {SECRET}\n")
        remove_observer_password(self.path)
        self.assertFalse(self.path.exists())
        remove_observer_password(self.path)  # 이미 없으면 그대로
        remove_observer_password(None)

    def test_stale_file_from_a_killed_run_is_replaced(self):
        self.path.write_text("old")
        write_observer_password(self.path, AdminSecret(SECRET))
        self.assertIn(SECRET, self.path.read_text(encoding="utf-8"))
        self.assertEqual(stat.S_IMODE(os.lstat(self.path).st_mode), 0o600)

    def test_untrusted_places_are_refused(self):
        os.chmod(self.dir, 0o755)
        with self.assertRaisesRegex(ObserverFileError, "observer_directory_not_private"):
            write_observer_password(self.path, AdminSecret(SECRET))
        os.chmod(self.dir, 0o700)
        with self.assertRaisesRegex(ObserverFileError, "observer_directory_not_private"):
            write_observer_password(self.dir / "missing" / "admin.txt", AdminSecret(SECRET))
        # 링크는 따라가지 않는다(가리키는 파일을 지우거나 덮어쓰지 않음)
        target = Path(self._tmp.name) / "elsewhere"
        target.write_text("keep")
        self.path.symlink_to(target)
        with self.assertRaisesRegex(ObserverFileError, "observer_file_untrusted"):
            write_observer_password(self.path, AdminSecret(SECRET))
        with self.assertRaisesRegex(ObserverFileError, "observer_file_untrusted"):
            remove_observer_password(self.path)
        self.assertEqual(target.read_text(), "keep")
        self.path.unlink()
        (self.dir / "admin.txt").mkdir()
        with self.assertRaisesRegex(ObserverFileError, "observer_file_untrusted"):
            write_observer_password(self.path, AdminSecret(SECRET))

    def test_errors_carry_no_value_or_path(self):
        os.chmod(self.dir, 0o755)
        try:
            write_observer_password(self.path, AdminSecret(SECRET))
        except ObserverFileError as exc:
            self.assertNotIn(SECRET, str(exc))
            self.assertNotIn(str(self.dir), str(exc))


class TestConfig(unittest.TestCase):
    def config(self, root: Path, value):
        payload = runner_config()
        payload.update(runs_root=str(root / "runs"), purpose="browser_acceptance", path_key_file=str(root / "path.key"),
                       preservation=model_options(), split=split_block(manifest_path=str(root / "m.json"),
                                                                       observer_password_file=value))
        return parse_runner_config(payload)

    def test_optional_field(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertEqual(self.config(root, str(root / "o" / "admin.txt")).split.observer_password_file, root / "o" / "admin.txt")
            for value, code in ((7, "INVALID"), ("relative/admin.txt", "INVALID"), (str(root / "o") + "/", "INVALID"),
                                (str(root / "runs" / "admin.txt"), "INSIDE_RUNS_ROOT"), (str(root / "m.json"), "SAME_AS_MANIFEST")):
                with self.subTest(value=value):
                    with self.assertRaises((RunnerConfigError, SplitConfigError)) as caught:
                        self.config(root, value)
                    self.assertIn(f"config.split.observer_password_file: {code}", str(caught.exception))

    def test_absent_by_default(self):
        with tempfile.TemporaryDirectory() as tmp:
            payload = runner_config()
            payload.update(runs_root=str(Path(tmp) / "runs"), purpose="browser_acceptance", path_key_file=str(Path(tmp) / "k"),
                           preservation=model_options(), split=split_block(manifest_path=str(Path(tmp) / "m.json")))
            self.assertIsNone(parse_runner_config(payload).split.observer_password_file)


class TestFlow(Fixture):
    def setUp(self):
        super().setUp()
        self.observer_dir = Path(self._tmp.name) / "observer"
        self.observer_dir.mkdir(mode=0o700)
        self.observer = self.observer_dir / "admin.txt"
        self.config = dataclasses.replace(self.config, split=dataclasses.replace(self.config.split, observer_password_file=self.observer))

    def run_ours(self, **patches):
        stdout, stderr = io.StringIO(), io.StringIO()
        defaults = {"run_openssl": FakeOpenssl()}
        defaults.update(patches)
        rendered = {"services": {"api": {"healthcheck": {"test": ["CMD-SHELL", "true"]}}}}
        with mock.patch.multiple(runner_split_main, **defaults), redirect_stdout(stdout), redirect_stderr(stderr), \
                mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: copy.deepcopy(rendered)), \
                mock.patch.object(runner_split_main.Docker, "env_file_value", lambda docker, path, name, scratch: "sk-or-v1-test"):
            code = run_split_ours(self.config, preflight_for(self.config), environ={}, condition="우리@test")
        return code, stdout.getvalue() + stderr.getvalue()

    def test_file_exists_during_the_run_holds_the_run_password_and_goes_with_the_inputs(self):
        seen = {}

        def execute(spec, ports, meter, evidence, clock):
            install = (Path(ports.inputs.directory) / "install.json").read_text(encoding="utf-8")
            seen["observer"] = self.observer.read_text(encoding="utf-8")
            seen["spec"] = spec
            seen["install"] = install
            seen["mode"] = stat.S_IMODE(os.lstat(self.observer).st_mode)
            ports.remove_inputs()  # 회차 흐름(execute_split_run)이 끝에 늘 부른다
            seen["after"] = self.observer.exists()
            return {"run_id": spec.run_id, "status": "complete"}

        code, out = self.run_ours(execute_split_run=execute)
        self.assertEqual(code, 0)
        self.assertEqual(seen["mode"], 0o600)
        self.assertIs(seen["spec"].observer_login_file, True)  # 결과에는 예/아니오만(경로·값 없음)
        password = seen["observer"].split("비밀번호: ", 1)[1].strip()
        self.assertIn(f'"{password}"', seen["install"])  # 설치 입력의 그 회차 관리자 비밀번호
        self.assertFalse(seen["after"])
        self.assertNotIn(password, out)  # 출력에 값이 없다
        run_dir = next(self.runs.iterdir())
        for path in run_dir.rglob("*"):  # 회차 디렉터리(근거)에 관찰 파일 사본이 없다
            if path.is_file():
                self.assertNotIn(password.encode(), path.read_bytes(), path)

    def test_removed_when_preparation_fails_after_writing(self):
        def compose_config(self, args):
            raise DockerError("docker_compose_config_failed")
        stderr = io.StringIO()
        with mock.patch.multiple(runner_split_main, run_openssl=FakeOpenssl()), redirect_stderr(stderr), \
                mock.patch.object(runner_split_main.Docker, "compose_config", compose_config):
            code = run_split_ours(self.config, preflight_for(self.config), environ={}, condition="우리@test")
        self.assertEqual(code, 2)
        self.assertFalse(self.observer.exists())
        self.assertEqual(list(self.runs.iterdir()), [])

    def test_not_set_means_no(self):
        seen = {}

        def execute(spec, ports, meter, evidence, clock):
            seen["spec"] = spec
            return {"run_id": spec.run_id, "status": "complete"}
        self.config = dataclasses.replace(self.config, split=dataclasses.replace(self.config.split, observer_password_file=None))
        code, _ = self.run_ours(execute_split_run=execute)
        self.assertEqual(code, 0)
        self.assertIs(seen["spec"].observer_login_file, False)

    def test_unwritable_place_stops_before_up_with_a_code(self):
        os.chmod(self.observer_dir, 0o755)
        code, out = self.run_ours(execute_split_run=mock.Mock(side_effect=AssertionError("started")))
        self.assertEqual(code, 2)
        self.assertIn("observer_directory_not_private", out)
        self.assertEqual(list(self.runs.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
