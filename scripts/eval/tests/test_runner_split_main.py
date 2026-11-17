"""탐색 분리 배치 O 회차 진입점(runner_split_main, 4-7c) 시험. 선언 대조·spec 값·runner.main 분기·띄우기 전 실패 정리."""

from __future__ import annotations

import copy
import io
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import runner  # noqa: E402
import runner_split_main  # noqa: E402
from runner_config import Image, RunnerConfigError, parse_runner_config  # noqa: E402
from runner_docker import DockerError  # noqa: E402
from runner_evidence import MeterFiles  # noqa: E402
from runner_split_config import plan_split_run  # noqa: E402
from runner_split_main import SplitDeclared, run_split_ours, split_declared, split_run_spec  # noqa: E402
from runner_split_ports import DockerSplitPorts  # noqa: E402
from runner_window import healthcheck_actions  # noqa: E402
from test_preflight import configuration  # noqa: E402
from test_preservation_record import options  # noqa: E402
from test_runner_config import runner_config  # noqa: E402
from test_runner_split_config import split_block  # noqa: E402
from test_runner_split_inputs import FakeOpenssl  # noqa: E402

MANIFEST = {"version": 3, "model": "example-org/example-model", "provider_slug": "example-provider", "approval_id": "approval-test-1",
            "base_url": "https://openrouter.ai/api/v1", "max_requests": 400}


def preflight_for(config, **ours):
    """runner_config의 실행 조건과 맞춘 사전검사 선언(봉인 뒤 유예·정착 대기는 비교 조건이다)."""
    declared = configuration()
    drain = {"grace_seconds": config.run["drain_grace_seconds"], "settle_seconds": config.run["post_settle_seconds"]}
    for side in ("baseline", "ours"):
        declared[side]["run_drain"] = dict(drain)
    declared["ours"].update(ours)
    return declared


def write_private(path: Path, data: bytes) -> Path:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.write(fd, data)
    os.close(fd)
    return path


class Fixture(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        root = Path(self._tmp.name)
        (root / "runs").mkdir(mode=0o700)
        (root / "secrets").mkdir(mode=0o700)
        self.manifest_path = write_private(root / "secrets" / "manifest.json", json.dumps(MANIFEST).encode())
        self.path_key = write_private(root / "secrets" / "path.key", bytes(range(32)))
        payload = runner_config()
        payload.update(runs_root=str(root / "runs"), purpose="browser_acceptance", path_key_file=str(self.path_key),
                       preservation=options(), split=split_block(manifest_path=str(self.manifest_path)))
        self.config = parse_runner_config(payload)
        self.runs = root / "runs"

    def tearDown(self):
        self._tmp.cleanup()


class TestDeclared(Fixture):
    def test_reads_ours_declaration(self):
        declared = split_declared(self.config, preflight_for(self.config), MANIFEST)
        ours = configuration()["ours"]
        self.assertEqual(declared, SplitDeclared(target_image=Image(ours["target_image"], self.config.target_image_id),
                                                 meter_port=8080, request_budget=ours["request_budget"],
                                                 time_budget_seconds=ours["time_budget_seconds"]))

    def test_model_and_provider_must_be_the_manifest(self):
        for change in ({"model": "other/model"}, {"provider_slug": "other-provider"}):
            with self.subTest(change=change), self.assertRaises(RunnerConfigError) as caught:
                split_declared(self.config, preflight_for(self.config), dict(MANIFEST, **change))
            self.assertEqual(caught.exception.errors, ["preflight.ours.llm: MANIFEST_MISMATCH"])

    def test_drain_conditions_and_origin(self):
        preflight = preflight_for(self.config)
        for side in ("baseline", "ours"):  # 선언 두 쪽은 같아야 사전검사를 지난다. 러너 설정과만 다르다.
            preflight[side]["run_drain"] = {"grace_seconds": 1, "settle_seconds": 1}
        with self.assertRaises(RunnerConfigError) as caught:
            split_declared(self.config, preflight, MANIFEST)
        self.assertEqual(caught.exception.errors, ["preflight.ours.run_drain: CONFIG_MISMATCH"])
        with self.assertRaises(RunnerConfigError) as caught:
            split_declared(self.config, {"not": "a declaration"}, MANIFEST)
        self.assertTrue(all(error.startswith("preflight: ") for error in caught.exception.errors))


class TestSpec(Fixture):
    def test_spec_values_come_from_the_b_fields(self):
        declared = split_declared(self.config, preflight_for(self.config), MANIFEST)
        plan = plan_split_run(self.config, self.config.split, run_id="ours-r1", target_image=declared.target_image,
                              meter_port=declared.meter_port)
        rendered = {"services": {"api": {"healthcheck": {"test": ["CMD", "true"]}}, "target": {}}}
        spec = split_run_spec(self.config, plan, declared, rendered)
        run = self.config.run
        self.assertEqual((spec.run_id, spec.project), ("ours-r1", "eval-ours-r1"))
        self.assertEqual((spec.request_budget, spec.time_budget_seconds), (declared.request_budget, declared.time_budget_seconds))
        self.assertEqual((spec.poll_seconds, spec.drain_timeout_seconds, spec.host_watch_max_gap_seconds, spec.post_settle_seconds,
                          spec.task_status_wait_seconds),
                         (run["budget_poll_seconds"], run["drain_timeout_seconds"], run["host_watch_max_gap_seconds"],
                          run["post_settle_seconds"], run["task_create_timeout_seconds"]))
        self.assertEqual(spec.events_lookback_seconds, self.config.health_timeout_seconds)
        self.assertEqual(spec.product_close_seconds, self.config.split.task_wait_seconds)
        self.assertEqual(spec.healthchecks, healthcheck_actions(rendered))
        self.assertEqual(set(spec.healthchecks), {"api"})
        self.assertEqual(spec.image_ids, {role: image.id for role, image in plan.images.items()})


class TestMainDispatch(Fixture):
    def call_main(self, side):
        with mock.patch.object(runner, "load_runner_config", return_value=self.config), \
                mock.patch.object(runner, "load_preflight", return_value=preflight_for(self.config)), \
                mock.patch.object(runner, "check_key_files", return_value="a" * 16), \
                mock.patch.object(runner_split_main, "run_split_ours", return_value=7) as split_run, \
                mock.patch.object(runner.getpass, "getpass", side_effect=AssertionError("O는 비밀번호를 묻지 않는다")), \
                redirect_stderr(io.StringIO()):
            code = runner.main(["--config", "c.json", "--preflight", "p.json", "--side", side, "--condition", "x", "--repeat", "0"])
        return code, split_run

    def test_ours_with_split_goes_to_the_product_layout(self):
        code, split_run = self.call_main("ours")
        self.assertEqual(code, 7)
        split_run.assert_called_once()
        self.assertIs(split_run.call_args.args[0], self.config)

    def test_baseline_stays_on_the_direct_layout(self):
        with mock.patch.object(runner, "plan_run", side_effect=RunnerConfigError(["stop-here"])):
            code, split_run = self.call_main("baseline")
        self.assertEqual(code, 2)
        split_run.assert_not_called()


class TestBeforeUp(Fixture):
    def run_ours(self, **patches):
        stdout, stderr = io.StringIO(), io.StringIO()
        defaults = {"run_openssl": FakeOpenssl()}
        defaults.update(patches)
        with mock.patch.multiple(runner_split_main, **defaults), redirect_stdout(stdout), redirect_stderr(stderr):
            code = run_split_ours(self.config, preflight_for(self.config), environ={})
        return code, stderr.getvalue()

    def test_render_failure_removes_inputs_and_the_run_directory(self):
        def compose_config(self, args):
            raise DockerError("docker_compose_config_failed")
        with mock.patch.object(runner_split_main.Docker, "compose_config", compose_config):
            code, err = self.run_ours()
        self.assertEqual((code, err.strip()), (2, "docker_compose_config_failed"))
        self.assertEqual(list(self.runs.iterdir()), [])  # 시도가 아니다(B와 같이 디렉터리를 지운다)

    def test_config_errors_print_codes_only(self):
        os.chmod(self.manifest_path, 0o644)
        code, err = self.run_ours()
        self.assertEqual((code, err.strip()), (2, "split.manifest: UNTRUSTED"))  # 0600 아님
        self.assertEqual(list(self.runs.iterdir()), [])

    def test_wiring_into_the_flow(self):
        seen = {}

        def execute(spec, ports, meter, evidence, clock):
            seen.update(spec=spec, ports=ports, meter=meter, evidence=evidence)
            private = Path(ports.inputs.directory)
            seen["path_key"] = (private / "path.key").read_bytes()
            return {"run_id": spec.run_id, "status": "complete"}

        rendered = {"services": {"api": {"healthcheck": {"test": ["CMD-SHELL", "true"]}}}}
        with mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: copy.deepcopy(rendered)), \
                mock.patch.object(runner_split_main.Docker, "env_file_value", lambda docker, path, name, scratch: (
                    "sk-or-v1-test" if (path, name) == (self.config.secrets_env_file, "EVAL_LLM_API_KEY") else "wrong-key")):
            code, _ = self.run_ours(execute_split_run=execute)
        self.assertEqual(code, 0)
        self.assertIsInstance(seen["ports"], DockerSplitPorts)
        self.assertEqual(seen["ports"].model_key.reveal(), "sk-or-v1-test")
        self.assertEqual(seen["path_key"], self.path_key.read_bytes())  # 경계 키 = B 계측기 키
        self.assertIsInstance(seen["meter"], MeterFiles)
        self.assertEqual(seen["spec"].healthchecks, healthcheck_actions(rendered))
        run_dir = next(self.runs.iterdir())
        self.assertEqual(seen["meter"].path, run_dir / "meter")
        self.assertTrue(run_dir.name.startswith("ours-"))

    def test_invalid_run_exits_one(self):
        with mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: {"services": {}}), \
                mock.patch.object(runner_split_main.Docker, "env_file_value", lambda self, path, name, scratch: "sk-or-v1-test"):
            code, _ = self.run_ours(execute_split_run=lambda *a: {"run_id": "r", "status": "invalid", "reasons": ["x"]})
        self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
