"""탐색 분리 배치 O 회차 진입점(runner_split_main, 4-7c) 시험. 선언 대조·spec 값·runner.main 분기·띄우기 전 실패 정리."""

from __future__ import annotations

import copy
import dataclasses
import hashlib
import dataclasses
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
from runner_checks import Listener  # noqa: E402
import runner_split_main  # noqa: E402
from runner_config import Image, RunnerConfigError, gateway_sampling, parse_runner_config, plan_run  # noqa: E402
from runner_docker import DockerError  # noqa: E402
from runner_evidence import MeterFiles  # noqa: E402
from runner_split_config import plan_split_run  # noqa: E402
from runner_split_main import SplitDeclared, llm_lookup_env, run_split_ours, split_declared, split_run_spec  # noqa: E402
from runner_split_ports import DockerSplitPorts  # noqa: E402
from runner_window import healthcheck_actions  # noqa: E402
from test_preflight import LOCAL_SAMPLING, configuration  # noqa: E402
from test_preservation_record import model_options, options  # noqa: E402
from test_runner_config import runner_config  # noqa: E402
from test_runner_split_config import split_block  # noqa: E402
from test_runner_split_inputs import FakeOpenssl  # noqa: E402

# 사전검사 픽스처(test_preflight.api_llm)와 같은 모델·제공자·base_url(실제 manifest는 openrouter.ai 고정이고, 선언도 그 값이어야 한다).
MANIFEST = {"version": 3, "model": "example-org/example-model", "provider_slug": "example-provider", "approval_id": "approval-test-1",
            "expected_provider_name": "Example",
            "base_url": "https://llm-api.example/api/v1", "max_requests": 400, "max_input_tokens": 2000000,
            "max_output_tokens": 200000, "max_cost_usd_micros": 1000000, "max_input_tokens_per_call": 120000,
            "max_output_tokens_per_call": 8192, "max_input_price_usd_micros_per_million": 200000,
            "max_output_price_usd_micros_per_million": 800000}


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
                       preservation=model_options(), split=split_block(manifest_path=str(self.manifest_path)))
        self.config = parse_runner_config(payload)
        self.runs = root / "runs"

    def tearDown(self):
        self._tmp.cleanup()


SYNTHETIC_LLM = {"backend": "local", "base_url": "http://eval-scripted-model:8080/v1", "model": "scripted-product:v1",
                 "hostname": "eval-scripted-model", "host_ip": "10.252.2.30", "model_digest": "a" * 64, "context_length": 32768,
                 "server_version": "0.0.1", "placement": "any"}


def synthetic_preflight(config, **llm_changes):
    declared = preflight_for(config)
    for side in ("baseline", "ours"):  # 사전검사는 두 쪽 LLM이 같아야 한다
        declared[side]["llm"] = dict(SYNTHETIC_LLM, **llm_changes)
        declared[side]["sampling"] = dict(LOCAL_SAMPLING)  # 로컬 백엔드 샘플링 묶음
    return declared


class SyntheticFixture(unittest.TestCase):
    """합성 모델 모드 설정(2026-11-18 사용자 결정 (a))."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        root = Path(self._tmp.name)
        (root / "runs").mkdir(mode=0o700)
        self.path_key = write_private(root / "path.key", bytes(range(32)))
        self.plan_path = root / "plan.json"
        self.plan_path.write_text(json.dumps({
            "format": "abc2lab.scripted-product-plan/v1", "model": "scripted-product:v1", "timeout_ms": 60000,
            "exploration": [{"navigate": "{origin}/"}], "intents": ["Check {origin}/a"], "commands": ["curl -s {origin}/a"]}))
        payload = runner_config()
        payload.update(runs_root=str(root / "runs"), purpose="browser_acceptance", path_key_file=str(self.path_key),
                       preservation=options(), split=split_block(manifest_path=None, synthetic_model={
                           "plan_path": str(self.plan_path), "max_tokens": 2048}))
        self.config = parse_runner_config(payload)
        self.root = root

    def tearDown(self):
        self._tmp.cleanup()


class TestSyntheticDeclared(SyntheticFixture):
    def test_declaration_must_point_at_the_scripted_model(self):
        declared = split_declared(self.config, synthetic_preflight(self.config), None)
        ours = synthetic_preflight(self.config)["ours"]
        self.assertEqual((declared.llm_model, declared.context_window_k, declared.llm_provider),
                         ("scripted-product:v1", ours["context_window_k"], ""))
        for change in ({"host_ip": "10.252.2.31"}, {"hostname": "other-model", "base_url": "http://other-model:8080/v1"},
                       {"base_url": "http://eval-scripted-model:8081/v1"}):
            with self.subTest(change=change), self.assertRaises(RunnerConfigError) as caught:
                split_declared(self.config, synthetic_preflight(self.config, **change), None)
            self.assertIn("preflight.ours.llm: SYNTHETIC_MODEL_MISMATCH", caught.exception.errors)
        # 실제 모델(api) 선언으로는 합성 모드를 돌리지 않는다
        with self.assertRaises(RunnerConfigError) as caught:
            split_declared(self.config, preflight_for(self.config), None)
        self.assertIn("preflight.ours.llm: SYNTHETIC_MODEL_MISMATCH", caught.exception.errors)

    def test_plan_copy_is_checked_and_bound_to_the_declared_model(self):
        run = self.root / "run"
        run.mkdir(mode=0o700)
        target, sha = runner_split_main._copy_scripted_plan(self.plan_path, run, "http://app.eval.test:8080", "scripted-product:v1")
        self.assertEqual((target.read_bytes(), sha), (self.plan_path.read_bytes(), hashlib.sha256(self.plan_path.read_bytes()).hexdigest()))
        self.assertEqual(os.stat(target).st_mode & 0o777, 0o600)
        other = self.root / "run2"
        other.mkdir(mode=0o700)
        with self.assertRaises(runner_split_main.SplitConfigError) as caught:
            runner_split_main._copy_scripted_plan(self.plan_path, other, "http://app.eval.test:8080", "other:v1")
        self.assertEqual(caught.exception.errors, ["split.synthetic_model.plan_path: MODEL_NOT_DECLARED"])
        self.plan_path.write_text("{}")
        third = self.root / "run3"
        third.mkdir(mode=0o700)
        with self.assertRaises(runner_split_main.SplitConfigError) as caught:
            runner_split_main._copy_scripted_plan(self.plan_path, third, "http://app.eval.test:8080", "scripted-product:v1")
        self.assertEqual(caught.exception.errors, ["split.synthetic_model.plan_path: plan_invalid"])

    def test_synthetic_budget_digest_and_no_model_key(self):
        budget = runner_split_main._model_budget(None, self.config)
        self.assertEqual(budget, runner_split_main.digest({"synthetic_model": {"max_tokens": 2048}}))
        # 합성 모드는 비밀 env 파일의 모델 키를 읽지 않는다: 키 읽기를 막아 두고 띄우기 전까지 간다
        docker_calls = []

        def docker_runner(args, **kwargs):
            docker_calls.append(args)
            raise DockerError("stop_here")
        with mock.patch.object(runner_split_main.Docker, "env_file_value", side_effect=AssertionError("key read")), \
                mock.patch.object(runner_split_main, "prepare_split_inputs", wraps=runner_split_main.prepare_split_inputs) as prepared, \
                redirect_stderr(io.StringIO()):
            code = run_split_ours(self.config, synthetic_preflight(self.config), docker_runner=docker_runner, condition="우리@test")
        self.assertEqual(code, 2)
        local = prepared.call_args.kwargs["local_model"]
        self.assertEqual((prepared.call_args.kwargs["manifest"], local["addresses"], local["base_url"], local["context_window_k"]),
                         (None, ["10.252.2.30"], "http://eval-scripted-model:8080/v1",
                          synthetic_preflight(self.config)["ours"]["context_window_k"]))
        # O 설치 회차 실행 시간은 선언된 러너 시간 예산 + 300초다(2026-11-25 사용자, 고정 초안 §10).
        self.assertEqual(prepared.call_args.kwargs["run_seconds"],
                         synthetic_preflight(self.config)["ours"]["time_budget_seconds"] + 300)
        self.assertEqual(list(self.config.runs_root.iterdir()), [])  # 띄우기 전 실패는 회차 디렉터리를 남기지 않는다


class TestDeclared(Fixture):
    def test_reads_ours_declaration(self):
        declared = split_declared(self.config, preflight_for(self.config), MANIFEST)
        ours = configuration()["ours"]
        self.assertEqual(declared, SplitDeclared(target_image=Image(ours["target_image"], self.config.target_image_id),
                                                 meter_port=8080, request_budget=ours["request_budget"],
                                                 time_budget_seconds=ours["time_budget_seconds"], llm_provider="example-provider",
                                                 llm_sampling=json.loads(gateway_sampling(ours, "ours")),
                                                 meter_source_sha=ours["source_sha"], llm_model=ours["llm"]["model"],
                                                 target_origin=ours["target_origin"], fixture_fingerprint=ours["fixture_fingerprint"],
                                                 environment_fingerprint=ours["environment_fingerprint"]))

    def test_lookup_env_and_sampling_match_the_b_gateway(self):
        # O 사후 검증의 조회 설정·샘플링 기대값은 같은 선언으로 만든 B 게이트웨이 compose 값과 같아야 한다.
        preflight = preflight_for(self.config)
        b_plan = plan_run(self.config, preflight, "baseline", "b1")
        env = llm_lookup_env(self.config, preflight)
        self.assertEqual(set(env), {"EVAL_LLM_UPSTREAM", "EVAL_LLM_PROVIDER", "EVAL_LLM_GENERATION_PATH", "EVAL_LLM_VERIFY_ATTEMPTS",
                                    "EVAL_LLM_VERIFY_INTERVAL", "EVAL_CA_BUNDLE"})
        self.assertEqual(env, {name: b_plan.compose_env[name] for name in env})
        declared = split_declared(self.config, preflight, MANIFEST)
        self.assertEqual(declared.llm_sampling, json.loads(b_plan.compose_env["EVAL_LLM_SAMPLING"]))

    def test_model_and_provider_must_be_the_manifest(self):
        for change in ({"model": "other/model"}, {"provider_slug": "other-provider"},
                       {"base_url": "https://other-api.example/api/v1"},  # #674 리뷰: 다른 origin으로 키를 보내지 않게
                       {"expected_provider_name": "Exam ple"}, {"expected_provider_name": "example"}):  # 러너 판정 이름과 다르면 거부
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
        preflight = preflight_for(self.config)
        for side in ("baseline", "ours"):  # 두 쪽이 같으면 사전검사는 받는다. O는 계측기 origin만 받는다.
            preflight[side]["target_origin"] = "http://other-service:8080"
        with self.assertRaises(RunnerConfigError) as caught:
            split_declared(self.config, preflight, MANIFEST)
        self.assertEqual(caught.exception.errors, ["preflight.ours.target_origin: NOT_EVAL_METER"])
        with self.assertRaises(RunnerConfigError) as caught:
            split_declared(self.config, {"not": "a declaration"}, MANIFEST)
        self.assertTrue(all(error.startswith("preflight: ") for error in caught.exception.errors))


class TestSpec(Fixture):
    def test_spec_values_come_from_the_b_fields(self):
        declared = split_declared(self.config, preflight_for(self.config), MANIFEST)
        plan = plan_split_run(self.config, self.config.split, run_id="ours-r1", target_image=declared.target_image,
                              meter_port=declared.meter_port, request_cap=100)
        rendered = {"services": {"api": {"healthcheck": {"test": ["CMD", "true"]}}, "target": {}}}
        spec = split_run_spec(self.config, plan, declared, rendered, condition="우리@test")
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
        self.assertEqual((spec.llm_sampling, spec.llm_provider_slug, spec.llm_provider_name),
                         (declared.llm_sampling, "example-provider", self.config.llm_gateway["expected_provider_name"]))
        self.assertEqual(spec.meter_source_sha, configuration()["ours"]["source_sha"])  # 계측기 기대 빌드
        # 멈춤 무효 판정 T는 B와 같은 run 칸에서 온다(설정에 없으면 판정 안 함)
        self.assertIsNone(spec.fault_stall_seconds)
        judged = split_run_spec(dataclasses.replace(self.config, fault_stall_seconds=180), plan, declared, rendered,
                                condition="ours@35b")
        self.assertEqual(judged.fault_stall_seconds, 180)
        # v1.1 러너 기록 칸은 B build_spec과 같은 출처(사전검사 O 선언·러너 설정)다
        ours = preflight_for(self.config)["ours"]
        self.assertEqual((spec.fixture_fingerprint, spec.environment_fingerprint, spec.target_origin),
                         (ours["fixture_fingerprint"], ours["environment_fingerprint"], ours["target_origin"]))
        self.assertEqual((spec.observer_path, spec.observer_readonly_ref),
                         (self.config.observer_path, self.config.observer_readonly_ref))
        self.assertEqual(spec.allowed_listens,
                         frozenset(Listener(e.address, e.port) for e in self.config.host_listen_allowlist))
        from runner_config import HostListen
        allowed = dataclasses.replace(self.config, host_listen_allowlist=(HostListen("10.0.0.5", 11434, "local model"),))
        self.assertEqual(split_run_spec(allowed, plan, declared, rendered, condition="우리@test").allowed_listens, frozenset({Listener("10.0.0.5", 11434)}))
        self.assertEqual(split_run_spec(self.config, plan, declared, rendered, condition="ours@35b").condition, "ours@35b")
        # E③ 비활성 요구는 설정 칸에서 온다(purpose와 따로, 2026-11-20 사용자)
        self.assertIs(spec.e3_disabled_required, False)
        required = dataclasses.replace(self.config, split=dataclasses.replace(self.config.split, e3_disabled_required=True))
        self.assertIs(split_run_spec(required, plan, declared, rendered, condition="우리@test").e3_disabled_required, True)
        # 실행 입력을 만드는 시점에 빈 기록 칸을 설정 오류로 거부한다(종료 코드 2, 회차를 만들지 않음, #745 리뷰)
        for condition in ("", "   "):
            with self.subTest(condition=condition), self.assertRaises(RunnerConfigError) as caught:
                split_run_spec(self.config, plan, declared, rendered, condition=condition)
            self.assertEqual(caught.exception.errors, ["split_run_spec.condition: MISSING"])


class TestMainDispatch(Fixture):
    def call_main(self, side, repeat="0"):
        with mock.patch.object(runner, "load_runner_config", return_value=self.config), \
                mock.patch.object(runner, "load_preflight", return_value=preflight_for(self.config)), \
                mock.patch.object(runner, "check_key_files", return_value="a" * 16), \
                mock.patch.object(runner_split_main, "run_split_ours", return_value=7) as split_run, \
                mock.patch.object(runner.getpass, "getpass", side_effect=AssertionError("O는 비밀번호를 묻지 않는다")), \
                redirect_stderr(io.StringIO()):
            code = runner.main(["--config", "c.json", "--preflight", "p.json", "--side", side, "--condition", "x", "--repeat", repeat])
        return code, split_run

    def test_ours_with_split_goes_to_the_product_layout(self):
        code, split_run = self.call_main("ours")
        self.assertEqual(code, 7)
        split_run.assert_called_once()
        self.assertIs(split_run.call_args.args[0], self.config)
        # 반복 순번은 B와 같이 --repeat에서 받는다(보존 sidecar v3의 repeat_index)
        _, split_run = self.call_main("ours", repeat="2")
        self.assertEqual(split_run.call_args.kwargs["repeat_index"], 2)
        # 측정 매트릭스 행 이름도 B처럼 --condition에서 받아 v1.1 기록에 서명된다
        self.assertEqual(split_run.call_args.kwargs["condition"], "x")

    def test_baseline_stays_on_the_direct_layout(self):
        with mock.patch.object(runner, "plan_run", side_effect=RunnerConfigError(["stop-here"])):
            code, split_run = self.call_main("baseline")
        self.assertEqual(code, 2)
        split_run.assert_not_called()


class TestBeforeUp(Fixture):
    def run_ours(self, condition="우리@test", **patches):
        stdout, stderr = io.StringIO(), io.StringIO()
        defaults = {"run_openssl": FakeOpenssl()}
        defaults.update(patches)
        with mock.patch.multiple(runner_split_main, **defaults), redirect_stdout(stdout), redirect_stderr(stderr):
            code = run_split_ours(self.config, preflight_for(self.config), environ={}, condition=condition)
        return code, stderr.getvalue()

    def test_empty_condition_is_a_config_error_before_anything_starts(self):
        # 조건 이름 없이 부르면 실행 입력을 만들 때 설정 오류다(종료 코드 2, 회차를 시작하지 않고 회차 디렉터리도 지움, #745 리뷰)
        rendered = {"services": {"api": {"healthcheck": {"test": ["CMD-SHELL", "true"]}}}}
        with mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: copy.deepcopy(rendered)), \
                mock.patch.object(runner_split_main.Docker, "env_file_value", lambda docker, path, name, scratch: "sk-or-v1-test"):
            code, stderr = self.run_ours(execute_split_run=mock.Mock(side_effect=AssertionError("started")), condition="")
        self.assertEqual(code, 2)
        self.assertIn("split_run_spec.condition: MISSING", stderr)
        self.assertEqual(list(self.runs.iterdir()), [])

    def test_render_failure_removes_inputs_and_the_run_directory(self):
        def compose_config(self, args):
            raise DockerError("docker_compose_config_failed")
        with mock.patch.object(runner_split_main.Docker, "compose_config", compose_config):
            code, err = self.run_ours()
        self.assertEqual((code, err.strip()), (2, "docker_compose_config_failed"))
        self.assertEqual(list(self.runs.iterdir()), [])  # 시도가 아니다(B와 같이 디렉터리를 지운다)

    def test_inputs_are_removed_even_if_the_run_directory_stays(self):
        # 디렉터리 삭제가 실패해도 보호 입력(관리자·DB 비밀번호·CA 키)은 먼저 지워져 있어야 한다.
        def compose_config(self, args):
            raise DockerError("docker_compose_config_failed")
        with mock.patch.object(runner_split_main.Docker, "compose_config", compose_config), \
                mock.patch.object(runner.shutil, "rmtree", side_effect=OSError("busy")):
            code, err = self.run_ours()
        self.assertEqual(code, 2)
        self.assertIn("run_directory: REMOVE_FAILED", err)
        run_dir = next(self.runs.iterdir())
        self.assertFalse((run_dir / "private").exists())

    def test_bad_model_key_and_interrupt_still_remove_inputs(self):
        # #674 리뷰: 보호 입력을 만든 뒤 모델 키 모양 오류(ProductAPIError)·운영자 중단이 나도 보호 입력과 회차 디렉터리를 지운다.
        rendered = {"services": {}}
        with mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: rendered), \
                mock.patch.object(runner_split_main.Docker, "env_file_value", lambda self, path, name, scratch: "has space"):
            code, err = self.run_ours()
        self.assertEqual((code, err.strip()), (2, "product_model_key_invalid"))
        self.assertEqual(list(self.runs.iterdir()), [])

        def interrupt(self, args):
            raise KeyboardInterrupt
        with mock.patch.object(runner_split_main.Docker, "compose_config", interrupt), self.assertRaises(KeyboardInterrupt):
            self.run_ours()
        self.assertEqual(list(self.runs.iterdir()), [])

        def surprise(self, args):
            raise RuntimeError("value-that-must-not-print")
        with mock.patch.object(runner_split_main.Docker, "compose_config", surprise):
            code, err = self.run_ours()
        self.assertEqual((code, err.strip()), (2, "split_prepare_failed"))
        self.assertEqual(list(self.runs.iterdir()), [])

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
            seen["boundary_sha"] = hashlib.sha256((private / "boundary.json").read_bytes()).hexdigest()
            return {"run_id": spec.run_id, "status": "complete"}

        rendered = {"services": {"api": {"healthcheck": {"test": ["CMD-SHELL", "true"]}}}}
        with mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: copy.deepcopy(rendered)), \
                mock.patch.object(runner_split_main.Docker, "env_file_value", lambda docker, path, name, scratch: (
                    "sk-or-v1-test" if (path, name) == (self.config.secrets_env_file, "EVAL_LLM_API_KEY") else "wrong-key")):
            code, _ = self.run_ours(execute_split_run=execute)
        self.assertEqual(code, 0)
        self.assertIsInstance(seen["ports"], DockerSplitPorts)
        self.assertEqual(seen["ports"].llm_env, llm_lookup_env(self.config, preflight_for(self.config)))
        self.assertEqual(seen["ports"].model_key.reveal(), "sk-or-v1-test")
        self.assertEqual(seen["path_key"], self.path_key.read_bytes())  # 경계 키 = B 계측기 키
        # 보존 v3 쪽별 칸: 경계 설정 지문은 보호 입력 boundary.json 바이트, 예산 지문은 manifest 예산 값만
        import preservation_v3_fields as v3f
        self.assertEqual(seen["spec"].exploration_policy_sha256, seen["boundary_sha"])
        self.assertEqual(seen["spec"].model_budget_sha256, v3f.model_budget_sha256(MANIFEST))
        # 계측기 기록 대조·서명 조건에 쓰는 키 지문(값이 아님)은 경계에 준 바로 그 바이트에서 뜬다(#699 리뷰)
        from path_mask import key_ref
        self.assertEqual(seen["spec"].path_key_ref, key_ref(seen["path_key"]))
        self.assertRegex(seen["spec"].path_key_ref, r"^[0-9a-f]{16}$")
        self.assertIsInstance(seen["meter"], MeterFiles)
        self.assertEqual(seen["spec"].healthchecks, healthcheck_actions(rendered))
        run_dir = next(self.runs.iterdir())
        self.assertEqual(seen["meter"].path, run_dir / "meter")
        self.assertTrue(run_dir.name.startswith("ours-"))

    def test_key_fingerprint_follows_the_copied_bytes_not_a_second_read(self):
        # #699 리뷰: 경계에 준 키와 지문을 따로 읽으면, 사이에 키 파일이 바뀌어도 성공했다.
        from path_mask import key_ref
        seen = {}

        def execute(spec, ports, meter, evidence, clock):
            seen.update(ref=spec.path_key_ref, key=(Path(ports.inputs.directory) / "path.key").read_bytes())
            return {"run_id": spec.run_id, "status": "complete"}
        swapped = bytes(reversed(range(32)))
        rendered = {"services": {"api": {"healthcheck": {"test": ["CMD-SHELL", "true"]}}}}
        with mock.patch.object(runner_split_main, "acceptance_path_key", return_value=swapped), \
                mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: copy.deepcopy(rendered)), \
                mock.patch.object(runner_split_main.Docker, "env_file_value", lambda docker, path, name, scratch: "sk-or-v1-test"):
            code, _ = self.run_ours(execute_split_run=execute)
        self.assertEqual(code, 0)
        self.assertEqual((seen["key"], seen["ref"]), (swapped, key_ref(swapped)))
        self.assertNotEqual(seen["ref"], key_ref(self.path_key.read_bytes()))

    def test_manifest_without_budget_values_is_a_config_error(self):
        import preservation_v3_fields as v3f
        broken = {k: v for k, v in MANIFEST.items() if k != "max_cost_usd_micros"}
        self.assertIsNone(v3f.model_budget_sha256(broken))
        self.assertIsNone(v3f.model_budget_sha256(dict(MANIFEST, max_requests=True)))
        self.assertIsNone(v3f.model_budget_sha256(dict(MANIFEST, max_requests=0)))
        with mock.patch.object(runner_split_main, "read_manifest", return_value=broken), \
                mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: {"services": {}}), \
                mock.patch.object(runner_split_main.Docker, "env_file_value", lambda docker, path, name, scratch: "sk-or-v1-test"):
            code, err = self.run_ours(execute_split_run=lambda *a: self.fail("회차를 시작하면 안 된다"))
        self.assertEqual((code, err.strip()), (2, "split.manifest: BUDGET_INVALID"))

    def test_approval_caps_carry_the_manifest_limits_and_prices(self):
        self.assertEqual(runner_split_main._approval_caps(MANIFEST), (400, 1000000, 200000, 800000))
        self.assertIsNone(runner_split_main._approval_caps(None))  # 합성 모델 모드

    def test_manifest_without_prices_is_a_config_error(self):
        # 일찍 끝난 회차의 승인 비용 대조(가장 작은 예약)에 단가가 필요하다(#831 리뷰)
        for name in ("max_input_price_usd_micros_per_million", "max_output_price_usd_micros_per_million"):
            broken = {k: v for k, v in MANIFEST.items() if k != name}
            with self.subTest(name=name), mock.patch.object(runner_split_main, "read_manifest", return_value=broken), \
                    mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: {"services": {}}), \
                    mock.patch.object(runner_split_main.Docker, "env_file_value", lambda docker, path, name, scratch: "sk-or-v1-test"):
                code, err = self.run_ours(execute_split_run=lambda *a: self.fail("회차를 시작하면 안 된다"))
                self.assertEqual((code, err.strip()), (2, "split.manifest: BUDGET_INVALID"))

    def test_invalid_run_exits_one(self):
        with mock.patch.object(runner_split_main.Docker, "compose_config", lambda self, args: {"services": {}}), \
                mock.patch.object(runner_split_main.Docker, "env_file_value", lambda self, path, name, scratch: "sk-or-v1-test"):
            code, _ = self.run_ours(execute_split_run=lambda *a: {"run_id": "r", "status": "invalid", "reasons": ["x"]})
        self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
