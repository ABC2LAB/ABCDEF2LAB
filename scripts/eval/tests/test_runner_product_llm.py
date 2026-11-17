"""탐색 분리 배치 O 회차 제품 모델 호출 사후 검증 부품(결정안 D(b), 4-11) 시험."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_docker import GENVERIFY_KEY_ENV, CommandResult, Docker, DockerError, client_environment, run_docker  # noqa: E402
from runner_product_llm import (  # noqa: E402
    CALL_LINE_MAX, ProductLLMError, call_record_lines, call_records_reasons, ledger_reservations, product_model_usage,
)

SAMPLING = {"temperature": 1.0, "top_p": 0.95, "top_k": 20, "min_p": 0, "presence_penalty": 1.5, "repetition_penalty": 1.0,
            "seed": 42}
KEY = "sk-or-v1-secret-key-value"
IMAGE = "sha256:" + "a" * 64
CID = "c" * 64
LLM_ENV = {"EVAL_LLM_UPSTREAM": "https://openrouter.ai", "EVAL_LLM_PROVIDER": "primeintellect/fp8",
           "EVAL_LLM_GENERATION_PATH": "/api/v1/generation", "EVAL_LLM_VERIFY_ATTEMPTS": "15", "EVAL_LLM_VERIFY_INTERVAL": "2s",
           "EVAL_CA_BUNDLE": "/etc/ssl/certs/ca-certificates.crt", "EVAL_LLM_SAMPLING": "{}"}


def call_line(seq: int) -> bytes:
    return json.dumps({"seq": seq, "decision": "forwarded", "method": "POST", "status": 200, "started_at": "t",
                       "completed_at": "t", "request_bytes": 10, "response_bytes": 20, "generation_id": f"gen-{seq}"}).encode() + b"\n"


class TestCallRecordCompleteness(unittest.TestCase):
    LEDGER = b"r1 100 200 300\ns1 100 200 300 10 20 30\nr1 5 6 7\nr1 5 6 0\n"

    def test_ledger_counts_reservations_only(self):
        self.assertEqual(ledger_reservations(self.LEDGER), 3)
        self.assertEqual(ledger_reservations(b""), 0)

    def test_ledger_shape_is_strict(self):
        for data in (b"r1 1 2 3", b"r1 1 2\n", b"r1 01 2 3\n", b"r1 -1 2 3\n", b"r1 +1 2 3\n", b"r2 1 2 3\n",
                     b"s1 1 2 3 4 5\n", b"r1 1 2 3 \n", b"r1  1 2 3\n", b"\n", "r1 1 2 ٣\n".encode(), b"used\n"):
            with self.subTest(data=data), self.assertRaises(ProductLLMError) as caught:
                ledger_reservations(data)
            self.assertEqual(str(caught.exception), "product_ledger_invalid")

    def test_calls_lines_and_bounds(self):
        self.assertEqual(call_record_lines(call_line(1) + call_line(2)), 2)
        self.assertEqual(call_record_lines(b""), 0)
        for data in (call_line(1)[:-1], b"\n", call_line(1) + b"\n", b"x" * CALL_LINE_MAX + b"\n", "é\n".encode()):
            with self.subTest(data=data[:20]), self.assertRaises(ProductLLMError) as caught:
                call_record_lines(data)
            self.assertEqual(str(caught.exception), "product_calls_invalid")
        self.assertEqual(call_record_lines(b"x" * (CALL_LINE_MAX - 1) + b"\n"), 1)

    def test_reasons_compare_reservations_with_records(self):
        calls = call_line(1) + call_line(2) + call_line(3)
        self.assertEqual(call_records_reasons(self.LEDGER, calls), [])
        # 기록 실패는 순번에 빈칸을 남기지 않으므로(줄 수 + 1) 장부와 대조해야 잡힌다.
        self.assertEqual(call_records_reasons(self.LEDGER, call_line(1) + call_line(2)), ["llm_call_records_incomplete"])
        self.assertEqual(call_records_reasons(self.LEDGER + b"r1 1 1 1\n", calls), ["llm_call_records_incomplete"])
        self.assertEqual(call_records_reasons(b"bad\n", calls), ["product_ledger_invalid"])
        self.assertEqual(call_records_reasons(self.LEDGER, b"x"), ["product_calls_invalid"])


def usage(requests=3, overwrites=0, sampling=SAMPLING, **extra):
    value = {"requests": requests, "errors": 0, "sampling_overwrites": overwrites, **extra}
    if sampling is not None:
        value["sampling"] = sampling
    return value


def record(model=None, preparation=(), **extra):
    value = {"version": 1, "task_id": 7, "run": {"phase": "closed"}, **extra}
    if model is not None:
        value["model"] = model
    value["model_preparation"] = [{"version": 1, "task_id": 7, "record_id": f"r{i}", "succeeded": True, "usage": u}
                                  for i, u in enumerate(preparation)]
    return value


class TestProductModelUsage(unittest.TestCase):
    def test_execution_and_preparation_are_reported_separately(self):
        value = product_model_usage(record(usage(10, 2), [usage(3, 0)]))
        self.assertEqual(value, {"sampling": SAMPLING, "sampling_overwrites": {"execution": 2, "preparation": [0]},
                                 "requests": {"execution": 10, "preparation": [3]}, "overwrote": True, "unknown": False})

    def test_no_overwrites_and_old_approvals(self):
        clean = product_model_usage(record(usage(10, 0), [usage(3, 0)]))
        self.assertEqual((clean["overwrote"], clean["unknown"]), (False, False))
        old = product_model_usage(record(usage(10, None, sampling=None), [usage(3, None, sampling=None)]))
        self.assertEqual((old["sampling"], old["overwrote"], old["unknown"], old["sampling_overwrites"]),
                         (None, False, True, {"execution": None, "preparation": [None]}))
        # 실행이 없는 기록(준비만): 실행 칸은 None이다.
        prep_only = product_model_usage({"version": 1, "task_id": 7, "run": None, "execution": "NOT_RUN",
                                         "model_preparation": record(preparation=[usage(2, 1)])["model_preparation"]})
        self.assertEqual((prep_only["requests"]["execution"], prep_only["overwrote"]), (None, True))
        # 보낸 호출이 없는 사용량은 샘플링 값이 없다. 다른 쪽 값이 쓰인다.
        self.assertEqual(product_model_usage(record(usage(0, 0, sampling=None), [usage(3, 0)]))["sampling"], SAMPLING)

    def test_shape_errors(self):
        cases = {
            "version": ({"version": 2}, "product_run_record_invalid"),
            "not_dict": ([], "product_run_record_invalid"),
            "prep_not_list": (dict(record(usage()), model_preparation={}), "product_run_record_invalid"),
            "missing_overwrites": (record({"requests": 1, "sampling": SAMPLING}), "product_model_usage_invalid"),
            "overwrites_over_requests": (record(usage(1, 2)), "product_model_usage_invalid"),
            "overwrites_negative": (record(usage(1, -1)), "product_model_usage_invalid"),
            "overwrites_bool": (record(usage(1, True)), "product_model_usage_invalid"),
            "requests_str": (record(usage("3")), "product_model_usage_invalid"),
            "sampling_bool": (record(usage(sampling=dict(SAMPLING, seed=True))), "product_model_usage_invalid"),
            "sampling_missing_key": (record(usage(sampling={k: v for k, v in SAMPLING.items() if k != "seed"})),
                                     "product_model_usage_invalid"),
            "prep_usage_missing": (dict(record(usage()), model_preparation=[{"version": 1}]), "product_model_usage_invalid"),
            "sampling_differs": (record(usage(), [usage(sampling=dict(SAMPLING, temperature=0.7))]), "product_sampling_inconsistent"),
        }
        for name, (value, code) in cases.items():
            with self.subTest(case=name), self.assertRaises(ProductLLMError) as caught:
                product_model_usage(value)
            self.assertEqual(str(caught.exception), code)


def compose_available() -> bool:
    try:
        return subprocess.run(["docker", "compose", "version"], capture_output=True, timeout=30).returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


class TestEnvFileValue(unittest.TestCase):
    """비밀 env 파일 값은 B 회차의 compose와 같은 규칙으로 얻는다(#654 리뷰: 따옴표 해석이 달라 다른 키가 되던 반례)."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self._tmp.name)
        self.env_file = self.dir / "eval.secrets.env"

    def tearDown(self):
        self._tmp.cleanup()

    def write(self, text):
        self.env_file.write_text(text, encoding="utf-8")
        os.chmod(self.env_file, 0o600)

    def fake(self, stdout, returncode=0):
        calls = []

        def run(args, env, stdin, timeout):
            spec = json.loads(Path(args[args.index("-f") + 1]).read_text(encoding="utf-8"))
            calls.append((list(args), dict(env), spec))
            return CommandResult(returncode, stdout)
        run.calls = calls
        return run

    def test_asks_compose_with_the_same_env_file_and_no_value_in_the_probe_file(self):
        fake = self.fake(json.dumps({"services": {"probe": {"environment": {"EVAL_LLM_API_KEY": KEY}}}}).encode())
        value = Docker(fake, {"DOCKER_HOST": "unix:///run/docker.sock"}, 30).env_file_value(self.env_file, "EVAL_LLM_API_KEY", self.dir)
        self.assertEqual(value, KEY)
        args, env, spec = fake.calls[0]
        self.assertEqual(args[:1] + args[args.index("--env-file"):], ["compose", "--env-file", str(self.env_file), "-p", "env-value",
                                                                     "config", "--format", "json"])
        self.assertEqual(spec, {"services": {"probe": {"image": "scratch", "environment": {"EVAL_LLM_API_KEY": "${EVAL_LLM_API_KEY:?}"}}}})
        self.assertEqual(env, {"DOCKER_HOST": "unix:///run/docker.sock"})
        self.assertEqual(os.listdir(self.dir), [])  # 임시 compose 파일은 지운다

    def test_failures_are_fixed_codes_and_clean_up(self):
        cases = {
            "compose_fails": (self.fake(b"", returncode=1), "docker_env_value_failed"),
            "no_value": (self.fake(json.dumps({"services": {"probe": {}}}).encode()), "docker_env_value_failed"),
            "empty_value": (self.fake(json.dumps({"services": {"probe": {"environment": {"EVAL_LLM_API_KEY": ""}}}}).encode()),
                            "docker_env_value_failed"),
            "not_json": (self.fake(b"oops"), "docker_env_value_failed_output_invalid"),
        }
        for name, (fake, code) in cases.items():
            with self.subTest(case=name), self.assertRaises(DockerError) as caught:
                Docker(fake, {}, 30).env_file_value(self.env_file, "EVAL_LLM_API_KEY", self.dir)
            self.assertEqual(str(caught.exception), code)
            self.assertEqual(os.listdir(self.dir), [])
        for name, scratch in (("bad name", self.dir), ("EVAL_LLM_API_KEY", Path("relative"))):
            with self.subTest(name=name), self.assertRaises(DockerError) as caught:
                Docker(self.fake(b"{}"), {}, 30).env_file_value(self.env_file, name, scratch)
            self.assertEqual(str(caught.exception), "docker_env_value_invalid")

    @unittest.skipUnless(compose_available(), "docker compose 없음")
    def test_real_compose_unquotes_like_the_gateway_sees(self):
        # 실제 compose 해석: 따옴표 없는 값·작은따옴표·큰따옴표 모두 같은 19자 키가 된다(리뷰 반례는 작은따옴표).
        docker = Docker(run_docker, client_environment(os.environ, {}), 60)
        for text in ("EVAL_LLM_API_KEY=synthetic-key-value\n", "EVAL_LLM_API_KEY='synthetic-key-value'\n",
                     'EVAL_LLM_API_KEY="synthetic-key-value"\n',
                     "EVAL_ENGINE_ADMIN_PASSWORD=x\nEVAL_LLM_API_KEY='synthetic-key-value'\nEVAL_ENGINE_DB_PASSWORD=y\n"):
            with self.subTest(text=text):
                self.write(text)
                self.assertEqual(docker.env_file_value(self.env_file, "EVAL_LLM_API_KEY", self.dir), "synthetic-key-value")
        self.write("EVAL_ENGINE_DB_PASSWORD=y\n")
        with self.assertRaises(DockerError) as caught:
            docker.env_file_value(self.env_file, "EVAL_LLM_API_KEY", self.dir)
        self.assertEqual(str(caught.exception), "docker_env_value_failed")


class TestGenerationVerifyCall(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.cidfile = Path(self._tmp.name) / "genverify.cid"
        self.out = Path(self._tmp.name) / "verify"

    def tearDown(self):
        self._tmp.cleanup()

    def executor(self, returncode=0):
        calls = []

        def run(args, env, stdin, timeout):
            calls.append((list(args), dict(env), stdin, timeout))
            Path(args[args.index("--cidfile") + 1]).write_bytes(CID.encode())
            return CommandResult(returncode, b"")
        run.calls = calls
        return run

    def call(self, executor, **changes):
        options = dict(key=KEY, llm_env=LLM_ENV, sampling=SAMPLING, out_dir=self.out, uid=1000, gid=1000, cidfile=self.cidfile,
                       timeout=300.0)
        options.update(changes)
        return Docker(executor, {"DOCKER_HOST": "unix:///run/docker.sock"}, 30).generation_verify(IMAGE, b"rows\n", **options)

    def test_key_only_in_env_and_same_lookup_settings_as_gateway(self):
        fake = self.executor()
        self.assertEqual(self.call(fake), CID)
        args, env, stdin, timeout = fake.calls[0]
        self.assertNotIn(KEY, " ".join(args))
        self.assertEqual((env[GENVERIFY_KEY_ENV], env["DOCKER_HOST"]), (KEY, "unix:///run/docker.sock"))
        self.assertEqual((stdin, timeout), (b"rows\n", 300.0))
        self.assertEqual(args[args.index("-e") + 1], GENVERIFY_KEY_ENV)
        tail = args[args.index("--") + 1:]
        self.assertEqual(tail, [IMAGE, "-role=generation-verify", "-upstream=https://openrouter.ai", "-provider=primeintellect/fp8",
                                "-generation-path=/api/v1/generation", "-verify-attempts=15", "-verify-interval=2s",
                                "-sampling=" + json.dumps(SAMPLING, separators=(",", ":"), sort_keys=True), "-log-dir=/evidence"])
        for flag in ("--read-only", "--rm", "-i"):
            self.assertIn(flag, args)
        self.assertEqual(args[args.index("--pull") + 1], "never")
        self.assertEqual(args[args.index("--cap-drop") + 1], "ALL")
        self.assertEqual(args[args.index("--user") + 1], "1000:1000")
        self.assertEqual(args[args.index("--log-driver") + 1], "none")
        mounts = [args[i + 1] for i, a in enumerate(args) if a == "--mount"]
        self.assertEqual(mounts, ["type=bind,source=/etc/ssl/certs/ca-certificates.crt,target=/etc/ssl/certs/ca-certificates.crt,readonly",
                                  f"type=bind,source={self.out},target=/evidence"])
        self.assertNotIn("--network", args)  # 회차 망 밖(기본 망)

    def test_without_product_sampling_no_flag(self):
        fake = self.executor()
        self.call(fake, sampling=None)
        self.assertFalse(any(a.startswith("-sampling") for a in fake.calls[0][0]))

    def test_refusals_before_docker_and_failure_after(self):
        cases = {
            "image_tag": ({"image": "meter:latest"}, "docker_genverify_image_not_id"),
            "uid": ({"uid": -1}, "docker_genverify_user_invalid"),
            "key_empty": ({"key": ""}, "docker_genverify_key_invalid"),
            "key_space": ({"key": "a b"}, "docker_genverify_key_invalid"),
            "env_missing": ({"llm_env": {k: v for k, v in LLM_ENV.items() if k != "EVAL_LLM_PROVIDER"}}, "docker_genverify_config_invalid"),
            "ca_relative": ({"llm_env": dict(LLM_ENV, EVAL_CA_BUNDLE="certs.pem")}, "docker_genverify_config_invalid"),
            "out_comma": ({"out_dir": Path("/tmp/a,b")}, "docker_genverify_config_invalid"),
            "sampling_list": ({"sampling": []}, "docker_genverify_config_invalid"),
        }
        for name, (change, code) in cases.items():
            fake = self.executor()
            with self.subTest(case=name), self.assertRaises(DockerError) as caught:
                if "image" in change:
                    Docker(fake, {}, 30).generation_verify(change["image"], b"", key=KEY, llm_env=LLM_ENV, sampling=None,
                                                           out_dir=self.out, uid=1, gid=1, cidfile=self.cidfile, timeout=1)
                else:
                    self.call(fake, **change)
            self.assertEqual((str(caught.exception), fake.calls), (code, []))
        with self.assertRaises(DockerError) as caught:
            self.call(self.executor(returncode=3))
        self.assertEqual(str(caught.exception), "docker_genverify_failed")


if __name__ == "__main__":
    unittest.main()
