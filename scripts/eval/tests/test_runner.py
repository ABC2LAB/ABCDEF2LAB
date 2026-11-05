"""회차 순서·실패 처리·근거·서명. docker·대상·엔진 없이 가짜 port로 본다."""

from __future__ import annotations

import copy
import hashlib
import json
import os
import re
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import runner
from runner import COMPLETE, INVALID, RESULT_NAME, Clock, DockerPorts, Facts, RunFailure, build_spec, execute_run
from runner_config import (
    COMPOSE_COMMON, COMPOSE_EVAL, COMPOSE_LLM_API, COMPOSE_LOCAL_LLM, RunnerConfigError, compose_environment,
    create_run_directory, parse_runner_config, plan_run,
)
from runner_engine import task_request
from runner_window import WatchSample
from runner_docker import DockerError
from runner_probe import PASS, ProbeOutcome
from test_preflight import configuration
from test_runner_checks import DAEMON, raw_world
from test_runner_config import _yaml_without_comments, runner_config
from test_runner_llm import generation as llm_generation
from test_runner_llm import marker as llm_marker

ROLE_OF = {"engine": "engine", "engine-db": "engine_db", "target": "target", "eval-meter": "meter", "eval-obs": "meter",
           "eval-llm-gw": "meter", "eval-api": "meter"}


def make_spec(side="ours", min_free=None, stream=None):
    preflight, payload = configuration(), runner_config()
    if stream is not None:
        payload["engine"]["llm_stream"] = stream
    if min_free is not None:  # 하한은 러너 설정과 사전검사 선언(환경 지문 입력)에 같이 바꿔야 한다
        payload["tmpfs"]["min_free"] = min_free
        for name in ("baseline", "ours"):
            preflight[name]["run_storage"]["tmpfs_min_free"] = min_free
    config = parse_runner_config(payload)
    plan = plan_run(config, preflight, side, f"{side}-1")
    return build_spec(config, plan, preflight, "t" * 64, "우리@35b", 0)


MIB = 1024 * 1024
# 러너가 띄운 probe 컨테이너 ID(--cidfile). 구간 감시의 허용 목록이 된다.
PROBE_IDS = {"pre": "a1" * 32, "post": "b2" * 32}

class FakeClock:
    def __init__(self):
        self.t = 0.0

    def clock(self):
        return Clock(lambda: datetime(2026, 11, 2, tzinfo=timezone.utc) + timedelta(seconds=self.t),
                     lambda: self.t, self.sleep)

    def sleep(self, seconds):
        self.t += seconds


class FakeMeter:
    """계측기 bind 디렉터리 흉내. seal 뒤에야 drained 마커가 생긴다."""

    def __init__(self, ports):
        self.ports = ports
        self.pre_forwarded = 0
        self.per_poll = 30
        self.polls = 0
        self.drained = {"role": "engine", "drained_at": "t", "inflight_at_drain": 0, "log_complete": True,
                        "rejected_after_seal": 2, "forced_closed_streams": 0, "forced_cancelled_requests": 0}
        self.observer_extra = 0
        self.rows_override = None
        self.drained_forwarded = None

    def _forwarded(self):
        return self.pre_forwarded if not self.ports.started else min(self.polls * self.per_poll, 10**6)

    def forwarded(self):
        if self.ports.started:
            self.polls += 1
        return self._forwarded()

    def engine_rows(self):
        if self.rows_override is not None:
            return self.rows_override
        n = self._forwarded()
        return [{"seq": i, "kind": "request", "outcome": "completed"} for i in range(1, n + 1)] + \
               [{"seq": n + 1, "kind": "rejected", "outcome": "rejected_after_seal"},
                {"seq": n + 2, "kind": "rejected", "outcome": "rejected_after_seal"}]

    def observer_rows(self):
        count = self.ports.ready_calls + 2 + self.observer_extra
        return [{"seq": i, "decision": "registered", "method": "GET", "status": 200, "started_at": f"s{i}",
                 "completed_at": f"c{i}", "response_bytes": 10} for i in range(1, count + 1)]

    def marker(self, name):
        if not self.ports.sealed:
            return None
        if name == "engine-drained.json":
            forwarded = self._forwarded() if self.drained_forwarded is None else self.drained_forwarded
            return dict(self.drained, forwarded_requests=forwarded)
        return {"role": "engine", "sealed_at": "t"}


class FakeGateway:
    """llm/ 디렉터리 흉내. 게이트웨이 seal 뒤에야 검증 마커가 생긴다."""

    def __init__(self, ports):
        self.ports = ports
        self.marker = llm_marker()

    def verified(self):
        return self.marker if "seal_gateway" in self.ports.calls else None


class FakeEvidence:
    def __init__(self):
        self.files = {}

    def write_json(self, name, payload):
        if name in self.files:
            raise AssertionError("evidence written twice")
        self.files[name] = copy.deepcopy(payload)
        return "h-" + name

    def digests(self):
        return {name: "h-" + name for name in self.files}

    def describe(self, name):
        return "h-" + name, len(json.dumps(self.files[name]))


class FakePorts:
    def __init__(self, spec):
        self.spec = spec
        self.calls = []
        self.fail = {}
        self.started = self.sealed = False
        self.on_start = None
        self.ready_calls = 1
        self.post_world = None
        self.probe_status = {"pre": PASS, "post": PASS}
        # 구간 전(pre)·후(post) 측정. 마운트 ID가 같아야 같은 회차 저장소를 잰 것이다.
        self.tmpfs_pre = {name: {"mount_id": mount_id, "size_bytes": 512 * MIB, "used_bytes": 0,
                                 "available_bytes": 512 * MIB}
                          for name, mount_id in (("engine_db", 101), ("engine_data", 102))}
        self.tmpfs = {name: {"mount_id": mount_id, "size_bytes": 512 * MIB, "used_bytes": 8 * MIB,
                             "available_bytes": 504 * MIB}
                      for name, mount_id in (("engine_db", 101), ("engine_data", 102))}
        self.tmpfs_error = None
        self.probe_phases = []
        self.phase = "pre"
        self.listeners = frozenset()
        self.running = []
        # 구간 감시: 정상 표본 하나(공백 0.5초)와 빈 이벤트가 기본.
        self.watch_stopped = (0.0, 1.0, [WatchSample(0.5, (), None)])
        self.events_list = []
        self.events_complete = True
        self.solved = {"pre": {"basket": False, "admin": True, "feedback": False},
                       "post": {"basket": True, "admin": True, "feedback": False}}
        self.host = {"cpu_model": "Example CPU", "logical_cpus": 8, "memory_kib": 16 * 1024 * 1024,
                     "kernel": "6.0.0-example", "docker_server_version": "29.1.3", "machine_id_sha256": "c" * 64}
        # 엔진 설정 응답(runner_engine이 프록시를 설정 여부로 바꾼 뒤): 선언 worker_count·모델과 같고 프록시 없음.
        self.settings = {"settings": {"workers": spec.worker_count, "web_search_proxy_set": False,
                                      "global_proxy_set": False, "llm_pool_enabled": False},
                         "llm": {"model": spec.model_tag, "proxy_set": False, "reasoning_effort": ""}}
        self.sign_calls = 0
        self.sign_fail_at = None
        self.watch_stops = 0
        self.events_stops = []

    def _call(self, name):
        self.calls.append(name)
        if name in self.fail:
            raise self.fail[name]

    def world(self):
        networks, containers = raw_world()
        for container in containers:
            container["image"] = self.spec.image_ids[ROLE_OF[container["service"]]]
        return networks, containers

    def preexisting(self):
        self._call("preexisting")
        return False

    def image_ids(self):
        self._call("image_ids")
        return dict(self.spec.image_ids)

    def up(self):
        self._call("up")

    def down(self):
        self._call("down")

    def facts(self):
        self._call(f"facts:{self.phase}")
        networks, containers = self.world()
        if self.phase == "post" and self.post_world:
            self.post_world(networks, containers)
        return Facts(DAEMON, networks, containers, copy.deepcopy(self.running))

    def host_listeners(self):
        self._call(f"listeners:{self.phase}")
        return self.listeners

    def probe(self, phase, snapshot):
        self._call(f"probe:{self.phase}")
        self.probe_phases.append(phase)
        status = self.probe_status[self.phase]
        rows = ({"id": "meter_tcp", "kind": "tcp", "target": "eval-meter:8080", "role": "positive", "expected": "success",
                 "accepted": ["connected"], "observed": "success", "code": "connected", "verdict": "match"},
                {"id": "dns:target", "kind": "dns", "target": "target", "role": "negative", "expected": "fail",
                 "accepted": ["not_found", "dns_error"], "observed": "fail", "code": "dns_error", "verdict": "match"})
        return ProbeOutcome(status, () if status == PASS else ("x",), rows, "a", "b", PROBE_IDS[phase])

    def wait_ready(self):
        self._call("wait_ready")
        return self.ready_calls

    def scoreboard(self, phase, snapshot):
        self._call(f"scoreboard:{phase}")
        context = {"run_id": self.spec.run_id, "model_tag": self.spec.model_tag,
                   "reset_generation": snapshot["services"]["target"]["id"],
                   "instance_id": snapshot["services"]["target"]["id"],
                   "execution_id": snapshot["services"]["engine"]["id"]}
        return {"raw": dict(context, path=f"{phase}/raw.json", sha256="r" + phase, size=10, phase=phase),
                "normalized": dict(context, path=f"{phase}/normalized.json", sha256="n" + phase, size=5, phase=phase),
                "requested_at": f"2026-11-02T00:00:0{1 if phase == 'pre' else 8}+00:00",
                "received_at": f"2026-11-02T00:00:0{2 if phase == 'pre' else 9}+00:00",
                "solved": dict(self.solved[phase])}

    def host_spec(self):
        self._call("host_spec")
        return dict(self.host)

    def engine_settings(self):
        self._call("engine_settings")
        return copy.deepcopy(self.settings)

    def start_task(self):
        self._call("start_task")
        if self.on_start:
            self.on_start()
        self.started = True
        return "17"

    def start_host_watch(self):
        self._call("watch:start")
        ports = self

        class Handle:
            def stop(self):
                ports._call("watch:stop")
                ports.watch_stops += 1
                return ports.watch_stopped
        return Handle()

    def start_events(self, since):
        self._call("events:start")
        self.events_since = since
        ports = self

        class Stream:
            def stop(self, until_ns):
                ports._call("events:stop")
                ports.events_stops.append(until_ns)
                return ports.events_list, ports.events_complete
        return Stream()

    def seal(self, meter_id):
        self._call("seal")
        self.sealed = True
        self.phase = "post"
        self.sealed_id = meter_id

    def seal_gateway(self, gateway_id):
        self._call("seal_gateway")
        self.gateway_sealed_id = gateway_id

    def tmpfs_usage(self, phase, snapshot):
        self._call(f"tmpfs:{phase}")
        if phase == "pre":
            return copy.deepcopy(self.tmpfs_pre)
        if self.tmpfs_error:
            raise self.tmpfs_error
        return copy.deepcopy(self.tmpfs)

    def findings(self):
        self._call("findings")
        return {"items": 0}

    def sign(self, payload):
        self.sign_calls += 1
        if self.sign_fail_at == self.sign_calls:
            self.calls.append("sign")
            raise RuntimeError("sign failed")
        self._call("sign")
        return dict(payload, signature="sig")


def run(mutate=None, mutate_gateway=None, spec=None):
    spec = spec or make_spec()
    ports = FakePorts(spec)
    meter = FakeMeter(ports)
    gateway = FakeGateway(ports)
    evidence = FakeEvidence()
    clock = FakeClock()
    if mutate:
        mutate(ports, meter, clock)
    if mutate_gateway:
        mutate_gateway(gateway)
    result = execute_run(spec, ports, meter, gateway, evidence, clock.clock())
    return result, ports, meter, evidence


FULL_ORDER = ["preexisting", "image_ids", "host_spec", "up", "facts:pre", "listeners:pre", "probe:pre", "tmpfs:pre",
              "wait_ready", "events:start", "watch:start", "scoreboard:pre", "engine_settings", "start_task", "seal",
              "seal_gateway", "facts:post", "listeners:post", "probe:post", "scoreboard:post", "watch:stop",
              "events:stop", "tmpfs:post", "findings", "down", "sign", "sign"]


class TestCompleteRun(unittest.TestCase):
    def test_order_and_signed_result(self):
        result, ports, meter, evidence = run()
        self.assertEqual(ports.calls, FULL_ORDER)
        self.assertEqual(result["status"], COMPLETE)
        self.assertEqual(result["signature"], "sig")
        self.assertEqual(evidence.files[RESULT_NAME], result)
        self.assertEqual(set(evidence.files), {"pre-checks.json", "pre-probe.json", "post-checks.json",
                                               "post-probe.json", "meter-summary.json", "llm-summary.json",
                                               "findings.json", "task.json", "window-watch.json",
                                               "isolation-evidence.json", "solve-events.json", "run-record.json",
                                               "engine-settings.json", RESULT_NAME})
        # 서명 대상 근거 목록에는 결과 파일 자신이 없다(서명 뒤에 쓴다).
        self.assertEqual(set(result["evidence"]), set(evidence.files) - {RESULT_NAME})
        self.assertEqual(ports.sealed_id, "4" * 64)
        self.assertEqual(ports.gateway_sealed_id, "6" * 64)
        self.assertEqual(result["llm_backend"], "api")
        self.assertEqual((result["llm"]["pinned_provider"], result["llm"]["expected_provider_name"],
                          result["llm"]["generations"], result["llm"]["providers"]),
                         ("example-provider", "Example", 2, ["Example"]))
        self.assertEqual(evidence.files["llm-summary.json"]["reasons"], [])
        task = evidence.files["task.json"]
        self.assertEqual((task["format"], task["request_sha256"], task["task_id"]),
                         ("abc2lab-eval-runner-task-v1", "t" * 64, "17"))
        self.assertEqual(result["task_request_sha256"], "t" * 64)
        # 엔진 LLM 정책 모드는 선언값만 남는다(엔진이 드러내지 않음 → 관측 null).
        self.assertEqual(result["engine_llm_policy"], {"declared": "external", "observed": None})
        # baseline(e53a79f)에는 스위치가 없어 같은 compose 값이 넘어가도 선언하지 않는다(#133 리뷰).
        self.assertIsNone(make_spec("baseline").llm_policy_mode)
        self.assertEqual(make_spec("ours").llm_policy_mode, "external")
        # 구간이 닫힌 뒤 잰 tmpfs 사용량이 서명된 완료 기록에 남는다(구간 중 exec는 경계 변경이므로 그 뒤에 잰다).
        # 적용한 하한(바이트)도 서명되는 결과에 남는다(#140 리뷰: 기록만으로 통과 기준을 다시 확인).
        self.assertEqual(result["tmpfs_usage"], ports.tmpfs | {"min_free_bytes": 16 * MIB, "pre": ports.tmpfs_pre})
        self.assertLess(ports.calls.index("events:stop"), ports.calls.index("tmpfs:post"))
        self.assertLess(ports.calls.index("tmpfs:pre"), ports.calls.index("events:start"))  # 구간 전 측정

    def test_v11_record_binds_proof_events_and_evidence(self):
        from runner_record import canonical_sha256
        result, ports, _, evidence = run()
        record = evidence.files["run-record.json"]
        self.assertEqual((record["version"], record["signature"], record["engine"], record["condition"],
                          record["repeat_index"]), ("1.1", "sig", "ours", "우리@35b", 0))
        proof = record["isolation_proof"]
        events = evidence.files["solve-events.json"]["events"]
        # 신규 해결(basket: false→true)만, admin(사전 해결)·feedback(미해결)은 없음. 이벤트는 증명 digest로 묶인다.
        self.assertEqual([e["challenge"] for e in events], ["basket"])
        self.assertEqual(events[0]["isolation_proof_sha256"], canonical_sha256(proof))
        self.assertEqual(events[0]["sequence"], record["timeline"]["post"]["sequence"])
        artifacts = record["artifacts"]
        self.assertEqual((artifacts["records"], artifacts["traffic"], artifacts["coverage"]), (None, None, None))
        self.assertEqual(artifacts["isolation_evidence"]["sha256"], "h-isolation-evidence.json")
        self.assertEqual(proof["enforcement_evidence_sha256"], "h-isolation-evidence.json")
        self.assertEqual(artifacts["solve_events"]["sha256"], "h-solve-events.json")
        self.assertEqual((proof["snapshots"]["pre_sha256"], proof["snapshots"]["post_sha256"]), ("npre", "npost"))
        self.assertEqual(proof["observer"]["request_count"], 1)  # post 수집 GET만(pre 제외)
        rows = evidence.files["isolation-evidence.json"]["rows"]
        windows = [r["window"] for r in rows if r["kind"] == "observer_request"]
        self.assertEqual(windows, ["before_pre", "pre", "in_window"])
        self.assertEqual([r["seq"] for r in rows], list(range(1, len(rows) + 1)))
        kinds = {r["kind"] for r in rows}
        self.assertTrue({"boundary", "network_snapshot", "host_condition", "host_watch", "docker_events_window",
                         "preparation", "observer_readonly", "observer_request", "engine_meter_summary",
                         "drain"} <= kinds)
        self.assertEqual([r["event"] for r in rows if r["kind"] == "boundary"], ["applied", "maintained"])
        checks = [(r["phase"], r["probe_kind"]) for r in rows if r["kind"] == "connectivity_check"]
        self.assertEqual(checks, [("pre", "tcp"), ("pre", "dns"), ("post", "tcp"), ("post", "dns")])
        self.assertEqual([r["sequence"] for r in record["timeline"].values()], [1, 2, 3, 4, 5, 6])
        self.assertEqual(record["total_requests"], evidence.files["meter-summary.json"]["forwarded_requests"])
        # 결과 기록의 서명 대상 근거 목록에 v1.1 기록도 들어간다.
        self.assertIn("run-record.json", result["evidence"])
        # 해결 상태 맵(평가 전용 key)은 결과 요약에 넣지 않는다.
        self.assertNotIn("solved", result["scoreboards"]["pre"])
        self.assertNotIn("solved", result["scoreboards"]["post"])
        self.assertNotIn("basket", json.dumps(result))

    def test_invalid_runs_write_no_v11_record(self):
        result, _, _, evidence = run(failing("findings"))
        self.assertEqual(result["status"], INVALID)
        for name in ("run-record.json", "isolation-evidence.json", "solve-events.json"):
            self.assertNotIn(name, evidence.files)

    def test_budget_clock_starts_before_task_creation(self):
        # 생성 요청이 오래 걸려도 그 시간은 예산에 들어간다(엔진은 생성과 동시에 기동).
        def slow_start(ports, meter, clock):
            meter.per_poll = 0
            ports.on_start = lambda: clock.sleep(100)
        result, _, _, evidence = run(slow_start)
        summary, task = evidence.files["meter-summary.json"], evidence.files["task.json"]
        requested = datetime.fromisoformat(summary["task_requested_at"])
        created = datetime.fromisoformat(summary["task_created_at"])
        stopped = datetime.fromisoformat(summary["stopped_at"])
        self.assertEqual((created - requested).total_seconds(), 100)
        self.assertEqual((task["requested_at"], task["created_at"]),
                         (summary["task_requested_at"], summary["task_created_at"]))
        elapsed = (stopped - requested).total_seconds()
        self.assertGreaterEqual(elapsed, summary["time_budget_seconds"])
        self.assertLess(elapsed, summary["time_budget_seconds"] + 100)
        self.assertEqual(result["status"], COMPLETE)

    def test_request_budget_overshoot_recorded_not_hidden(self):
        result, _, _, evidence = run(lambda p, m, c: setattr(m, "per_poll", 35))
        summary = evidence.files["meter-summary.json"]
        self.assertEqual((summary["stop_reason"], summary["forwarded_requests"], summary["request_budget"],
                          summary["over_budget"]), ("request_budget", 105, 100, 5))
        self.assertEqual(summary["rejected_after_seal"], 2)
        self.assertEqual(result["meter"], summary)

    def test_time_budget_stop(self):
        result, _, meter, evidence = run(lambda p, m, c: setattr(m, "per_poll", 0))
        summary = evidence.files["meter-summary.json"]
        self.assertEqual((summary["stop_reason"], summary["forwarded_requests"], summary["over_budget"]),
                         ("time_budget", 0, 0))
        self.assertEqual(result["status"], COMPLETE)


def failing(code_or_name, error=None):
    return lambda p, m, c: p.fail.__setitem__(code_or_name, error or RuntimeError("boom /secret/path"))


class TestFailures(unittest.TestCase):
    def assert_invalid(self, result, ports, evidence, reason, *, down=True):
        self.assertEqual(result["status"], INVALID)
        self.assertEqual(result["reasons"], [reason] if isinstance(reason, str) else reason)
        self.assertEqual(evidence.files[RESULT_NAME], result)
        self.assertNotIn("sign", ports.calls)
        self.assertEqual(ports.calls.count("down"), 1 if down else 0)
        self.assertEqual(ports.calls.count("up"), 1 if "up" in ports.calls else 0)  # 재시도 없음
        for field in ("signature", "evidence", "meter", "images", "scoreboards", "policy_sha256"):
            self.assertNotIn(field, result)
        self.assertNotIn("/secret/path", repr(result))
        # 구간이 닫히기 전에 끝난 회차는 미측정, 잰 뒤 무효가 된 회차는 잰 값을 그대로 남긴다.
        measured = ("tmpfs:post" in ports.calls and ports.tmpfs_error is None and "tmpfs:post" not in ports.fail
                    and set(ports.tmpfs) == {"engine_db", "engine_data"})
        expected = (ports.tmpfs | {"min_free_bytes": ports.spec.tmpfs_min_free_bytes, "pre": ports.tmpfs_pre}
                    if measured else "not_measured")
        self.assertEqual(result["tmpfs_usage"], expected)

    def test_pre_requests_must_be_zero(self):
        result, ports, _, evidence = run(lambda p, m, c: setattr(m, "pre_forwarded", 1))
        self.assert_invalid(result, ports, evidence, "pre_requests_nonzero")
        self.assertNotIn("start_task", ports.calls)
        self.assertNotIn("meter-summary.json", evidence.files)

    def test_each_step_failure_is_invalid_with_down_and_no_signature(self):
        cases = [
            ("up", "compose_up_failed"), ("facts:pre", "pre_facts_failed"), ("listeners:pre", "pre_host_listeners_failed"),
            ("probe:pre", "pre_probe_invalid"), ("wait_ready", "target_not_ready"), ("scoreboard:pre", "pre_scoreboard_failed"),
            ("start_task", "task_start_failed"), ("seal", "seal_failed"), ("seal_gateway", "llm_seal_failed"),
            ("facts:post", "post_facts_failed"),
            ("probe:post", "post_probe_invalid"), ("scoreboard:post", "post_scoreboard_failed"),
            ("tmpfs:pre", "pre_tmpfs_measure_failed"), ("tmpfs:post", "tmpfs_measure_failed"),
            ("findings", "findings_failed"), ("engine_settings", "engine_settings_failed"),
        ]
        for name, reason in cases:
            with self.subTest(step=name):
                result, ports, _, evidence = run(failing(name))
                self.assert_invalid(result, ports, evidence, reason)
                self.assertEqual(ports.calls[-1], "down")

    def test_tmpfs_at_or_near_its_limit_invalidates_the_run(self):
        spec = make_spec()
        for name in ("engine_db", "engine_data"):
            for available, reason in ((spec.tmpfs_min_free_bytes - 1, "tmpfs_full"), (0, "tmpfs_full"),
                                      (spec.tmpfs_min_free_bytes, None)):
                with self.subTest(name=name, available=available):
                    result, ports, _, evidence = run(
                        lambda p, m, c, n=name, a=available: p.tmpfs[n].update(available_bytes=a))
                    if reason is None:  # 하한과 같으면 아직 닿지 않은 것
                        self.assertEqual(result["status"], COMPLETE)
                    else:
                        self.assert_invalid(result, ports, evidence, reason)
                        self.assertEqual(result["tmpfs_usage"][name]["available_bytes"], available)
                        self.assertNotIn("findings", ports.calls)
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "tmpfs_error", DockerError("docker_statfs_failed")))
        self.assert_invalid(result, ports, evidence, "tmpfs_measure_failed")
        self.assertEqual(result["tmpfs_usage"], "not_measured")
        # 측정 결과에 두 볼륨이 정확히 있어야 한다(빠지거나 더 있으면 측정 실패).
        result, ports, _, evidence = run(lambda p, m, c: p.tmpfs.pop("engine_data"))
        self.assert_invalid(result, ports, evidence, "tmpfs_measure_failed")
        self.assertEqual(result["tmpfs_usage"], "not_measured")

    def test_post_must_measure_the_same_mounts_as_pre(self):
        # #140 리뷰: 같은 경로라도 다른 tmpfs(부모 경로 변경·symlink·다시 마운트)를 쟀다면 회차 저장소 근거가 아니다.
        for name in ("engine_db", "engine_data"):
            with self.subTest(name=name):
                result, ports, _, evidence = run(lambda p, m, c, n=name: p.tmpfs[n].update(mount_id=999))
                self.assert_invalid(result, ports, evidence, "tmpfs_mount_changed")
                self.assertEqual(result["tmpfs_usage"][name]["mount_id"], 999)
                self.assertEqual(result["tmpfs_usage"]["pre"][name]["mount_id"], ports.tmpfs_pre[name]["mount_id"])
                self.assertNotIn("findings", ports.calls)
        result, ports, _, evidence = run(lambda p, m, c: p.tmpfs_pre.pop("engine_db"))
        self.assert_invalid(result, ports, evidence, "pre_tmpfs_measure_failed")
        self.assertNotIn("events:start", ports.calls)  # 구간 전에 멈춘다

    def test_threshold_is_recorded_and_bound_to_the_comparison(self):
        # #140 리뷰 재현: 같은 측정값(남은 20MiB)에 하한만 8m/32m로 바꾸면 완료·무효가 갈린다. 적용 하한이 서명되는
        # 결과에 남고, 하한은 사전검사 선언(환경 지문 입력)과 같아야만 시작된다.
        def twenty_mib_left(p, m, c):
            for usage in p.tmpfs.values():
                usage.update(used_bytes=usage["size_bytes"] - 20 * MIB, available_bytes=20 * MIB)
        loose, ports, _, _ = run(twenty_mib_left, spec=make_spec(min_free="8m"))
        self.assertEqual((loose["status"], loose["tmpfs_usage"]["min_free_bytes"]), (COMPLETE, 8 * MIB))
        strict, ports, _, evidence = run(twenty_mib_left, spec=make_spec(min_free="32m"))
        self.assert_invalid(strict, ports, evidence, "tmpfs_full")
        self.assertEqual(strict["tmpfs_usage"]["min_free_bytes"], 32 * MIB)
        preflight, payload = configuration(), runner_config()
        payload["tmpfs"]["min_free"] = "8m"  # 선언은 16m 그대로
        with self.assertRaises(RunnerConfigError) as caught:
            plan_run(parse_runner_config(payload), preflight, "ours", "ours-1")
        self.assertEqual(caught.exception.errors, ["preflight.ours.run_storage: CONFIG_MISMATCH"])

    def test_probe_statuses(self):
        for phase in ("pre", "post"):
            for status in ("violation", "invalid"):
                with self.subTest(phase=phase, status=status):
                    result, ports, _, evidence = run(lambda p, m, c: p.probe_status.__setitem__(phase, status))
                    self.assert_invalid(result, ports, evidence, f"{phase}_probe_{status}")
                    self.assertIn(f"{phase}-probe.json", evidence.files)

    def test_drain(self):
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "seal", lambda _id: p._call("seal")))
        self.assert_invalid(result, ports, evidence, "drain_timeout")
        for field, value in (("inflight_at_drain", 1), ("log_complete", False), ("forced_cancelled_requests", -1),
                             ("forced_cancelled_requests", True), ("forced_cancelled_requests", "1")):
            result, ports, _, evidence = run(lambda p, m, c: m.drained.__setitem__(field, value))
            self.assert_invalid(result, ports, evidence, "drain_incomplete")
        result, ports, _, evidence = run(lambda p, m, c: m.drained.pop("forced_cancelled_requests"))
        self.assert_invalid(result, ports, evidence, "drain_incomplete")

    def test_requests_cut_after_the_grace_are_counted_and_settled_before_post(self):
        slept = []

        def cut_one(p, m, c):
            m.per_poll, m.drained_forwarded = 0, 2
            m.drained["forced_cancelled_requests"] = 1
            m.rows_override = [{"seq": 1, "kind": "request", "outcome": "completed"},
                               {"seq": 2, "kind": "request", "outcome": "forced_cancelled"}]
            sleep = c.sleep
            c.sleep = lambda seconds: (slept.append((seconds, list(p.calls))), sleep(seconds))
        result, ports, _, evidence = run(cut_one)
        self.assertEqual(result["status"], COMPLETE)
        summary = evidence.files["meter-summary.json"]
        self.assertEqual((summary["forwarded_requests"], summary["forced_cancelled_requests"],
                          summary["drain_grace_seconds"], summary["post_settle_seconds"],
                          summary["post_settle_applied_seconds"]), (2, 1, 30, 2, 2))
        # 정착 대기는 drain 확인 뒤, 게이트웨이 봉인·post 점검 전에 한 번
        settles = [calls for seconds, calls in slept if seconds == 2]
        self.assertEqual(len(settles), 1)
        self.assertEqual(settles[0][-1], "seal")
        drain = next(row for row in evidence.files["isolation-evidence.json"]["rows"] if row["kind"] == "drain")
        self.assertEqual((drain["grace_seconds"], drain["settle_seconds"], drain["forced_cancelled"],
                          drain["settle_applied_seconds"]), (30, 2, 1, 2))
        self.assertEqual(evidence.files["run-record.json"]["total_requests"], 2)

    def test_run_conditions_are_recorded_and_signed(self):
        result, ports, _, evidence = run()
        self.assertEqual(result["status"], COMPLETE)
        conditions = result["run_conditions"]
        settings = evidence.files["engine-settings.json"]
        self.assertEqual(settings, ports.settings)
        self.assertEqual(conditions["engine_settings_sha256"], runner.canonical_sha256(settings))
        self.assertEqual(conditions["workers"], ports.spec.worker_count)
        self.assertIs(conditions["llm_stream"], ports.spec.llm_stream)
        # 선언값이 아니라 게이트웨이가 마커에 남긴 적용값이 기록된다.
        self.assertEqual(conditions["sampling"], evidence.files["llm-summary.json"]["sampling_applied"])
        self.assertEqual(conditions["machine_id_sha256"], ports.host["machine_id_sha256"])
        self.assertNotIn("machine_id_sha256", conditions["host"])
        row = next(r for r in evidence.files["isolation-evidence.json"]["rows"] if r["kind"] == "run_conditions")
        self.assertEqual({key: row[key] for key in conditions}, conditions)

    def test_llm_stream_setting_is_a_signed_run_condition(self):
        # 엔진 설정 API에 안 나오는 스트리밍도 러너 설정값 그대로 서명 근거에 남아야 회차끼리 대조된다(#174 리뷰).
        for stream in (True, False):
            with self.subTest(stream=stream):
                spec = make_spec(stream=stream)
                self.assertIs(spec.llm_stream, stream)
                result, _, _, evidence = run(spec=spec)
                self.assertEqual(result["status"], COMPLETE)
                self.assertIs(result["run_conditions"]["llm_stream"], stream)
                row = next(r for r in evidence.files["isolation-evidence.json"]["rows"] if r["kind"] == "run_conditions")
                self.assertIs(row["llm_stream"], stream)

    def test_engine_settings_must_match_the_declaration(self):
        changes = {"workers": lambda s: s["settings"].update(workers=s["settings"]["workers"] + 1),
                   "workers type": lambda s: s["settings"].update(workers=str(s["settings"]["workers"])),
                   "global proxy": lambda s: s["settings"].update(global_proxy_set=True),
                   "web search proxy": lambda s: s["settings"].update(web_search_proxy_set=True),
                   "llm proxy": lambda s: s["llm"].update(proxy_set=True),
                   "model": lambda s: s["llm"].update(model="other/model"),
                   "missing part": lambda s: s.pop("llm")}
        for label, change in changes.items():
            with self.subTest(label=label):
                result, ports, _, evidence = run(lambda p, m, c, f=change: f(p.settings))
                self.assert_invalid(result, ports, evidence, "engine_settings_mismatch")
                self.assertNotIn("start_task", ports.calls)

    def test_host_spec_must_be_complete(self):
        for label, change in {"missing": lambda h: h.pop("kernel"), "empty model": lambda h: h.update(cpu_model=""),
                              "zero cpus": lambda h: h.update(logical_cpus=0),
                              "bad id": lambda h: h.update(machine_id_sha256="x")}.items():
            with self.subTest(label=label):
                result, ports, _, evidence = run(lambda p, m, c, f=change: f(p.host))
                # 컨테이너를 띄우기 전에 멈추므로 내릴 것도 없다.
                self.assert_invalid(result, ports, evidence, "host_spec_failed", down=False)
                self.assertNotIn("up", ports.calls)

    def test_gateway_sampling_must_equal_the_declaration(self):
        result, ports, _, evidence = run(mutate_gateway=lambda g: g.marker["sampling"].update(temperature=0.7))
        self.assert_invalid(result, ports, evidence, "llm_sampling_mismatch")

    def test_large_integer_seed_mismatch_is_not_signed(self):
        # 선언 seed 2^53+1. 게이트웨이가 2^53을 넣었으면 float로 같아 보여도 무효이고 서명하지 않는다.
        preflight, payload = configuration(), runner_config()
        for name in ("baseline", "ours"):
            preflight[name]["seed"] = 2 ** 53 + 1
        config = parse_runner_config(payload)
        spec = build_spec(config, plan_run(config, preflight, "ours", "ours-1"), preflight, "t" * 64, "우리@35b", 0)
        self.assertEqual(spec.llm_sampling["seed"], 2 ** 53 + 1)
        result, ports, _, evidence = run(spec=spec, mutate_gateway=lambda g: g.marker.update(
            sampling=dict(spec.llm_sampling)))
        self.assertEqual((result["status"], ports.sign_calls), (COMPLETE, 2))
        result, ports, _, evidence = run(spec=spec, mutate_gateway=lambda g: g.marker.update(
            sampling=dict(spec.llm_sampling, seed=2 ** 53)))
        self.assert_invalid(result, ports, evidence, "llm_sampling_mismatch")
        self.assertEqual(ports.sign_calls, 0)

    def test_no_settle_without_cut_requests(self):
        slept = []
        result, _, _, evidence = run(lambda p, m, c: setattr(c, "sleep", lambda s: slept.append(s)))
        self.assertEqual(result["status"], COMPLETE)
        self.assertNotIn(2, slept)
        summary = evidence.files["meter-summary.json"]
        # 정책 값은 남고 적용 대기만 0이다.
        self.assertEqual((summary["post_settle_seconds"], summary["post_settle_applied_seconds"]), (2, 0))
        drain = next(row for row in evidence.files["isolation-evidence.json"]["rows"] if row["kind"] == "drain")
        self.assertEqual((drain["settle_seconds"], drain["settle_applied_seconds"]), (2, 0))

    def test_cut_count_must_match_the_meter_rows(self):
        def mismatch(p, m, c):
            m.per_poll, m.drained_forwarded = 0, 2
            m.drained["forced_cancelled_requests"] = 1
            m.rows_override = [{"seq": 1, "kind": "request", "outcome": "completed"},
                               {"seq": 2, "kind": "request", "outcome": "completed"}]
        result, ports, _, evidence = run(mismatch)
        self.assert_invalid(result, ports, evidence, "meter_log_inconsistent")

    def test_boundary_changes_between_pre_and_post(self):
        def restart_target(networks, containers):
            next(c for c in containers if c["service"] == "target")["restart_count"] = 1
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "post_world", restart_target))
        self.assert_invalid(result, ports, evidence, "boundary_changed")

    def test_post_topology_violation(self):
        def attach_engine(networks, containers):
            target_net = next(n for n in networks if n["Labels"]["com.docker.compose.network"] == "target_net")
            target_net["Containers"]["1" * 64] = {"Name": "x", "IPv4Address": "172.31.0.9/16"}
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "post_world", attach_engine))
        self.assert_invalid(result, ports, evidence, "post_topology_violation")
        self.assertEqual(evidence.files["post-checks.json"]["topology_violations"],
                         ["network.target_net: MEMBERS_MISMATCH", "service.engine: ATTACHMENT_INCONSISTENT"])

    def test_meter_and_observer_records_must_agree(self):
        result, ports, _, evidence = run(lambda p, m, c: setattr(m, "observer_extra", 1))
        self.assert_invalid(result, ports, evidence, "observer_count_mismatch")
        result, ports, _, evidence = run(lambda p, m, c: setattr(m, "rows_override", [
            {"seq": 2, "kind": "request", "outcome": "completed"}]))
        self.assert_invalid(result, ports, evidence, "meter_log_inconsistent")

    def test_meter_sequence_and_count_checked_separately(self):
        def gap(p, m, c):
            # 전달 건수는 drained와 같지만 순번 2가 빠짐(기록 유실)
            m.rows_override = [{"seq": 1, "kind": "request", "outcome": "completed"},
                               {"seq": 3, "kind": "request", "outcome": "completed"}]
            m.drained_forwarded = 2
        result, ports, _, evidence = run(gap)
        self.assert_invalid(result, ports, evidence, "meter_log_inconsistent")

        def count(p, m, c):
            # 순번은 완결이지만 drained 마커의 전달 건수와 다름
            m.rows_override = [{"seq": i, "kind": "request", "outcome": "completed"} for i in (1, 2, 3)]
            m.drained_forwarded = 5
        result, ports, _, evidence = run(count)
        self.assert_invalid(result, ports, evidence, "meter_log_inconsistent")

    def test_host_conditions_block_the_run(self):
        from runner_checks import Listener
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "listeners", frozenset({Listener("0.0.0.0", 8080)})))
        self.assert_invalid(result, ports, evidence, "pre_host_condition_violation")
        self.assertEqual(evidence.files["pre-checks.json"]["host_violations"], ["host.listen.0.0.0.0:8080: NOT_ALLOWED"])
        foreign = {"id": "a" * 64, "project": "dev", "service": "web", "image": "x", "running": True,
                   "started_at": "t", "restart_count": 0, "ports": {"80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "80"}]},
                   "networks": {}, "network_mode": "bridge", "pid_mode": ""}
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "running", [foreign]))
        self.assert_invalid(result, ports, evidence, "pre_host_condition_violation")
        # 구간 전부터 대상 netns를 같이 쓰던 컨테이너(망 이벤트·망 구성원에 안 보임)
        target = next(x for x in raw_world()[1] if x["service"] == "target")["id"]
        shared = dict(foreign, ports=None, network_mode="container:" + target)
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "running", [shared]))
        self.assert_invalid(result, ports, evidence, "pre_host_condition_violation")
        self.assertEqual(evidence.files["pre-checks.json"]["host_violations"],
                         ["host.container.aaaaaaaaaaaa: SHARES_NETWORK_NAMESPACE"])

    def test_llm_provider_conditions_decide_the_run(self):
        cases = [
            (lambda g: setattr(g, "marker", llm_marker(generations=[llm_generation("g", provider="Other")])),
             "llm_provider_mismatch"),
            (lambda g: setattr(g, "marker", llm_marker(generations=[llm_generation("g", attempts=[])])),
             "llm_fallback_unverifiable"),
            (lambda g: setattr(g, "marker", llm_marker(generations=[llm_generation("g", status="unverified")])),
             "llm_generation_unverified"),
            (lambda g: setattr(g, "marker", llm_marker(provider="other-provider")), "llm_pin_mismatch"),
            (lambda g: setattr(g, "marker", llm_marker(log_complete=False)), "llm_log_incomplete"),
            (lambda g: setattr(g, "marker", {"role": "llm-gateway"}), "llm_marker_invalid"),
            (lambda g: setattr(g, "verified", lambda: None), "llm_unverified"),
            (lambda g: setattr(g, "verified", lambda: (_ for _ in ()).throw(OSError("x"))), "llm_read_failed"),
        ]
        for mutate, reason in cases:
            with self.subTest(reason=reason):
                result, ports, _, evidence = run(mutate_gateway=mutate)
                self.assert_invalid(result, ports, evidence, reason)
                self.assertNotIn("findings", ports.calls)  # LLM 조건이 확인돼야 findings로 간다

    def test_llm_reasons_are_all_kept_in_evidence(self):
        bad = llm_marker(log_complete=False, generations=[llm_generation("g", provider="Other")])
        result, ports, _, evidence = run(mutate_gateway=lambda g: setattr(g, "marker", bad))
        self.assertEqual(result["reasons"], ["llm_log_incomplete"])
        self.assertEqual(evidence.files["llm-summary.json"]["reasons"], ["llm_log_incomplete", "llm_provider_mismatch"])

    def test_project_exists_or_image_mismatch_never_touches_compose(self):
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "preexisting", lambda: p._call("preexisting") or True))
        self.assert_invalid(result, ports, evidence, "project_exists", down=False)
        self.assertNotIn("up", ports.calls)

        def other_image(p, m, c):
            p.image_ids = lambda: dict(p.spec.image_ids, target="sha256:" + "f" * 64)
        result, ports, _, evidence = run(other_image)
        self.assert_invalid(result, ports, evidence, "image_id_mismatch", down=False)

    def test_running_container_image_must_match(self):
        def swap(networks, containers):
            next(c for c in containers if c["service"] == "engine")["image"] = "sha256:" + "f" * 64
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "post_world", swap))
        self.assert_invalid(result, ports, evidence, "post_topology_violation")

    def test_gateway_and_api_front_images_are_checked(self):
        for service in ("eval-llm-gw", "eval-api"):
            def swap(networks, containers, service=service):
                next(c for c in containers if c["service"] == service)["image"] = "sha256:" + "f" * 64
            result, ports, _, evidence = run(lambda p, m, c: setattr(p, "post_world", swap))
            with self.subTest(service=service):
                self.assert_invalid(result, ports, evidence, "post_topology_violation")
                self.assertIn(f"service.{service}: IMAGE_MISMATCH", evidence.files["post-checks.json"]["image_violations"])

    def test_down_failure_after_success_is_invalid_and_unsigned(self):
        result, ports, _, evidence = run(failing("down"))
        self.assert_invalid(result, ports, evidence, "down_failed")
        result, ports, _, evidence = run(lambda p, m, c: (p.fail.__setitem__("findings", RuntimeError()),
                                                          p.fail.__setitem__("down", RuntimeError())))
        self.assert_invalid(result, ports, evidence, ["findings_failed", "down_failed"])

    def test_sign_failure_is_invalid(self):
        # 서명은 v1.1 러너 기록 → 결과 기록 순서. 어느 쪽이 실패해도 무효이고 서명된 결과는 없다.
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "sign_fail_at", 1))
        self.assertEqual((result["status"], result["reasons"]), (INVALID, ["record_sign_failed"]))
        self.assertEqual(ports.calls[-2:], ["down", "sign"])
        self.assertNotIn("run-record.json", evidence.files)
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "sign_fail_at", 2))
        self.assertEqual((result["status"], result["reasons"]), (INVALID, ["sign_failed"]))
        self.assertEqual(ports.calls[-3:], ["down", "sign", "sign"])

    def test_interrupt_still_downs_and_records(self):
        spec = make_spec()
        ports = FakePorts(spec)
        ports.fail["start_task"] = KeyboardInterrupt()
        evidence = FakeEvidence()
        with self.assertRaises(KeyboardInterrupt):
            execute_run(spec, ports, FakeMeter(ports), FakeGateway(ports), evidence, FakeClock().clock())
        self.assertEqual(evidence.files[RESULT_NAME]["reasons"], ["interrupted"])
        self.assertEqual(ports.calls[-1], "down")

    def test_window_violations_invalidate_with_evidence(self):
        network = {"Type": "network", "Action": "connect", "Actor": {"ID": "n" * 64, "Attributes": {}}}

        def attach(p, m, c):
            # 엔진을 대상 망에 붙인 이벤트(스모크 고장 주입과 같은 모양). 망 ID는 pre 스냅샷에서 가져온다.
            target = next(n for n in raw_world()[0] if n["Labels"]["com.docker.compose.network"] == "target_net")
            engine = next(x for x in raw_world()[1] if x["service"] == "engine")
            p.events_list = [dict(network, Actor={"ID": target["Id"], "Attributes": {"container": engine["id"]}},
                                  timeNano=4_000_000_000_000_000_000)]
        cases = {
            "window_boundary_changed": attach,
            "window_host_condition_violation": lambda p, m, c: setattr(
                p, "watch_stopped", (0.0, 1.0, [WatchSample(0.5, ("host.listen.0.0.0.0:3000: NOT_ALLOWED",), None)])),
            "window_host_watch_gap": lambda p, m, c: setattr(p, "watch_stopped", (0.0, 100.0, [WatchSample(0.5, (), None)])),
            "window_events_incomplete": lambda p, m, c: setattr(p, "events_complete", False),
        }
        for code, mutate in cases.items():
            with self.subTest(code=code):
                result, ports, _, evidence = run(mutate)
                self.assert_invalid(result, ports, evidence, code)
                self.assertEqual(result["failed_step"], code)
                self.assertEqual(evidence.files["window-watch.json"]["reasons"][0], code)
                self.assertNotIn("findings", ports.calls)  # 구간이 깨진 회차는 그 뒤로 진행하지 않는다

    def test_expected_window_events_keep_the_run_complete(self):
        # 실제 회차에 늘 생기는 이벤트: 계측기·게이트웨이 seal(USR1)과 engine-db healthcheck. 이것만으로 무효가 되면 안 된다.
        from runner_window import ENGINE_DB_HEALTHCHECK

        late = 4_000_000_000_000_000_000  # 구간 안(가짜 시계 기준 한참 뒤)

        def normal(p, m, c):
            ids = {x["service"]: x["id"] for x in raw_world()[1]}
            p.events_list = [
                {"Type": "container", "Action": "kill", "Actor": {"ID": ids["eval-meter"], "Attributes": {"signal": "10"}},
                 "timeNano": late},
                {"Type": "container", "Action": "kill", "Actor": {"ID": ids["eval-llm-gw"], "Attributes": {"signal": "10"}},
                 "timeNano": late},
                {"Type": "container", "Action": "exec_create: " + ENGINE_DB_HEALTHCHECK,
                 "Actor": {"ID": ids["engine-db"], "Attributes": {"execID": "h1"}}, "timeNano": late},
                {"Type": "container", "Action": "exec_die", "Actor": {"ID": ids["engine-db"], "Attributes": {"execID": "h1"}},
                 "timeNano": late},
                {"Type": "container", "Action": "health_status: healthy", "Actor": {"ID": ids["engine-db"]},
                 "timeNano": late},
            ]
        result, ports, _, evidence = run(normal)
        self.assertEqual(result["status"], COMPLETE)
        self.assertEqual(evidence.files["window-watch.json"]["docker_events"]["boundary_events"], [])
        self.assertEqual(result["window"]["events"]["event_count"], 5)
        self.assertEqual(ports.probe_phases, ["pre", "post"])
        self.assertEqual(evidence.files["post-probe.json"]["container_id"], PROBE_IDS["post"])

    def test_only_the_runner_post_probe_may_start_a_container_in_the_window(self):
        # 구간 중 러너가 띄운 post probe(cidfile ID)의 생성·시작은 허용, 그 밖의 컨테이너 생성은 namespace 공유
        # 가능성이 있어 무효. 엔진 netns를 같이 쓰는 probe와 같은 모양(망 이벤트 없음)이다.
        late = 4_000_000_000_000_000_000

        def starts(container_id):
            return [{"Type": "container", "Action": action, "Actor": {"ID": container_id, "Attributes": {}},
                     "timeNano": late} for action in ("create", "attach", "start", "die", "destroy")]

        result, _, _, evidence = run(lambda p, m, c: setattr(p, "events_list", starts(PROBE_IDS["post"])))
        self.assertEqual(result["status"], COMPLETE)
        self.assertEqual(evidence.files["window-watch.json"]["docker_events"]["runner_container_events"], 5)
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "events_list", starts("f" * 64)))
        self.assert_invalid(result, ports, evidence, "window_boundary_changed")
        self.assertEqual(evidence.files["window-watch.json"]["docker_events"]["boundary_events"],
                         [{"type": "container", "action": "create", "service": "foreign"},
                          {"type": "container", "action": "start", "service": "foreign"}])

    def test_window_failures_of_the_watch_itself(self):
        for name, code in (("watch:start", "window_watch_failed"), ("watch:stop", "window_watch_failed"),
                           ("events:start", "window_events_failed"), ("events:stop", "window_events_failed")):
            with self.subTest(step=name):
                result, ports, _, evidence = run(failing(name))
                self.assert_invalid(result, ports, evidence, code)

    def test_watch_is_stopped_even_when_the_run_fails_midway(self):
        result, ports, _, _ = run(failing("start_task"))
        self.assertEqual(result["reasons"], ["task_start_failed"])
        self.assertEqual(ports.watch_stops, 1)
        self.assertEqual(len(ports.events_stops), 1)  # 스트림 프로세스도 남기지 않는다
        self.assertEqual(ports.calls[-3:], ["watch:stop", "events:stop", "down"])

    def test_events_window_covers_pre_to_post(self):
        result, ports, _, evidence = run()
        self.assertRegex(ports.events_since, r"\A\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z\Z")
        record = evidence.files["window-watch.json"]
        # 스트림은 구간 시작보다 healthcheck timeout+여유만큼 앞서 열고, 끝은 post 뒤 시각까지 받는다.
        start = datetime.fromisoformat(record["start"].replace("Z", "+00:00")[:26] + "+00:00")
        since = datetime.fromisoformat(ports.events_since.replace("Z", "")[:26] + "+00:00")
        self.assertEqual((start - since).total_seconds(), record["lookback_seconds"])
        self.assertEqual(record["lookback_seconds"], 3.0)  # health.timeout 2s + 1
        self.assertGreater(ports.events_stops[0], 0)
        self.assertEqual(result["window"]["events"], {"event_count": 0, "kinds": {}, "complete": True})
        self.assertEqual(result["window"]["host_watch"]["samples"], 1)
        self.assertEqual(evidence.files["window-watch.json"]["reasons"], [])

    def test_upgraded_stream_rows_are_forwarded_requests(self):
        # 계측기가 drain 마커에 센 전달 2건 = request 1 + stream 1. rejected는 빼고 대조해야 완료다.
        def mixed(p, m, c):
            m.per_poll = 0
            m.rows_override = [{"seq": 1, "kind": "request", "outcome": "completed"},
                               {"seq": 2, "kind": "stream", "outcome": "completed"},
                               {"seq": 3, "kind": "rejected", "outcome": "rejected_after_seal"}]
            m.drained_forwarded = 2
        result, _, _, evidence = run(mixed)
        self.assertEqual(result["status"], COMPLETE)
        self.assertEqual(evidence.files["meter-summary.json"]["forwarded_requests"], 2)
        result, _, _, _ = run(lambda p, m, c: (mixed(p, m, c), setattr(m, "rows_override", m.rows_override + [
            {"seq": 4, "kind": "upgrade", "outcome": "completed"}])))
        self.assertEqual(result["reasons"], ["meter_log_inconsistent"])

    def test_failed_step_names_the_failing_check(self):
        # 근거를 쓴 뒤 점검 결과로 실패해도 마지막 단계(근거 쓰기)가 아니라 실패한 점검이 남는다(스모크에서 발견).
        cases = ((lambda p, m, c: p.probe_status.__setitem__("pre", "invalid"), "pre_probe_invalid"),
                 (lambda p, m, c: setattr(m, "pre_forwarded", 1), "pre_requests_nonzero"))
        for mutate, code in cases:
            with self.subTest(code=code):
                result, _, _, _ = run(mutate)
                self.assertEqual((result["reasons"], result["failed_step"]), ([code], code))

    def test_unexpected_error_still_recorded(self):
        # 순서 코드 안에서 port가 아닌 곳의 예외(여기선 drained 마커 모양 이상)도 무효 기록·down으로 끝난다.
        result, ports, _, evidence = run(lambda p, m, c: setattr(m, "marker", lambda name: ["not", "dict"]))
        self.assertEqual(result["reasons"], ["unexpected_error"])
        self.assertEqual(result["failed_step"], "meter_read_failed")
        self.assertEqual(ports.calls[-1], "down")


class TestIsolationOfRunDirectory(unittest.TestCase):
    """절대규칙 7: scoreboard(GT)는 회차 디렉터리에만. 엔진 컨테이너에는 어떤 회차 디렉터리도 마운트되지 않는다."""

    def services_referencing(self, needle):
        found = set()
        for path in (COMPOSE_COMMON, COMPOSE_LLM_API, COMPOSE_LOCAL_LLM, COMPOSE_EVAL):
            section = service = None
            for line in _yaml_without_comments(Path(path).read_text(encoding="utf-8")).splitlines():
                if line and not line.startswith(" "):
                    section = line.split(":")[0]
                    continue
                match = re.fullmatch(r"  ([A-Za-z0-9_.-]+):.*", line)
                if section == "services" and match:
                    service = match.group(1)
                elif section == "services" and needle in line:
                    found.add(service)
        return found

    def test_evidence_dir_only_in_meter_and_observer(self):
        self.assertEqual(self.services_referencing("EVAL_EVIDENCE_DIR"), {"eval-meter", "eval-obs"})
        text = Path(COMPOSE_EVAL).read_text(encoding="utf-8")
        self.assertRegex(text, r"(?m)^  engine:\n(?:    .*\n)*?    volumes: !reset \[\]")

    def test_api_key_reaches_only_the_gateway(self):
        # 엔진·계측기·관측 front에는 API 키가 없다. 엔진은 자리값만 받는다(게이트웨이가 버린다).
        self.assertEqual(self.services_referencing("EVAL_LLM_API_KEY"), {"eval-llm-gw"})
        for name in ("OPENAI_API_KEY}", "ANTHROPIC_API_KEY", "${OPENAI_API_KEY"):
            self.assertEqual(self.services_referencing(name), set())
        self.assertEqual(self.services_referencing("EVAL_LLM_EVIDENCE_DIR"), {"eval-llm-gw"})

    def test_meter_mount_is_a_subdirectory_apart_from_scoreboards(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp, "runs")
            root.mkdir(mode=0o700)
            os.chmod(root, 0o700)
            config = parse_runner_config(runner_config() | {"runs_root": str(root)})
            plan = plan_run(config, configuration(), "ours", "ours-1")
            directory = create_run_directory(root, "ours-1")
            env = compose_environment(plan, directory)
            self.assertEqual(env["EVAL_EVIDENCE_DIR"], str(directory.meter))
            self.assertNotEqual(directory.meter, directory.run)
            self.assertNotIn(str(config.runner_key_path), "\n".join(env.values()))
            ports = DockerPorts(config, plan, directory, docker=None, findings=dict, start=lambda: "1", settings=dict)
            snapshot = {"services": {"target": {"id": "3" * 64}, "engine": {"id": "1" * 64}}}
            with patch.object(runner, "collect_snapshot") as collect, \
                    patch.object(runner, "read_private_json", return_value={"solved": {"k": False}}) as read:
                collect.return_value.artifacts.raw = {"path": "pre/raw.json"}
                collect.return_value.artifacts.normalized = {"path": "pre/normalized.json"}
                board = ports.scoreboard("pre", snapshot)
            # 해결 상태 맵은 회차 디렉터리의 정규화본에서 읽는다(계측기 마운트 밖).
            self.assertEqual(read.call_args[0], (directory.run / "pre", "normalized.json"))
            self.assertEqual(board["solved"], {"k": False})
            endpoint, run_directory, phase, _ = collect.call_args[0]
            self.assertEqual((run_directory, phase), (directory.run, "pre"))
            self.assertEqual(endpoint.origin, "http://127.0.0.1:18081")
            self.assertFalse(str(run_directory).startswith(str(directory.meter)))


class TestMainKeepsTheAdminPasswordInMemory(unittest.TestCase):
    """비밀번호는 getpass로 한 번 받아 작업 생성·findings 수집에만 넘긴다. argv·환경·docker 호출·출력에 없다."""

    def inputs(self, tmp, change=None, origin=None):
        from test_baseline_extract import vocabulary
        root = Path(tmp, "runs")
        root.mkdir(mode=0o700)
        os.chmod(root, 0o700)
        vocab = Path(tmp, "vocabulary.json")
        vocab.write_text(json.dumps(vocabulary()))
        payload = runner_config() | {"runs_root": str(root), "runner_key_path": str(Path(tmp, "k"))}
        payload["findings"] = dict(payload["findings"], vocabulary_path=str(vocab))
        if change:
            change(payload)
        preflight = configuration()
        if origin:
            for side in ("baseline", "ours"):
                preflight[side]["target_origin"] = origin
        config_path, preflight_path = Path(tmp, "runner.json"), Path(tmp, "preflight.json")
        config_path.write_text(json.dumps(payload))
        preflight_path.write_text(json.dumps(preflight))
        return root, ["--config", str(config_path), "--preflight", str(preflight_path), "--side", "ours",
                      "--condition", "우리@35b", "--repeat", "0"]

    def test_password_reaches_only_task_start_and_findings(self):
        import contextlib
        import io
        from test_runner_engine import PASSWORD
        with tempfile.TemporaryDirectory() as tmp:
            root, argv = self.inputs(tmp)
            seen = {}

            def fake_docker(executor, env, timeout):
                seen["docker_env"] = dict(env)
                return object()

            def fake_start(port, secret, request, timeout):
                seen["start"] = (port, secret.reveal(), request.sha256, timeout)
                return "17"

            def fake_collect(port, secret, settings, vocabulary_obj, digest, *, side, build_source):
                seen["collect"] = (port, secret.reveal(), side, build_source)
                return {"findings": []}

            def fake_execute(spec, ports, meter, gateway, evidence, clock):
                seen["spec_task"] = spec.task_request_sha256
                seen["task_id"] = ports.start_task()
                ports.findings()
                return {"run_id": spec.run_id, "status": INVALID, "reasons": ["x"]}

            out, err = io.StringIO(), io.StringIO()
            with patch.object(runner.getpass, "getpass", return_value=PASSWORD), \
                    patch.object(runner, "Docker", side_effect=fake_docker), \
                    patch.object(runner, "start_task", side_effect=fake_start), \
                    patch.object(runner, "collect_findings", side_effect=fake_collect), \
                    patch.object(runner, "execute_run", side_effect=fake_execute), \
                    contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                code = runner.main(argv)
            self.assertEqual(code, 1)
            expected = task_request(runner_config()["task"], configuration()["ours"]["target_origin"])
            self.assertEqual(seen["start"], (18787, PASSWORD, expected.sha256, 20))
            self.assertEqual((seen["spec_task"], seen["task_id"]), (expected.sha256, "17"))
            self.assertEqual(seen["collect"], (18787, PASSWORD, "ours", "2" * 40))
            self.assertNotIn(PASSWORD, "\n".join(seen["docker_env"].values()))
            self.assertNotIn(PASSWORD, out.getvalue() + err.getvalue())
            self.assertNotIn(PASSWORD, "\n".join(os.environ.values()))
            for written in root.rglob("*"):
                if written.is_file():
                    self.assertNotIn(PASSWORD, written.read_text(errors="ignore"))

    def test_task_target_is_checked_against_this_run_origin(self):
        # 계측기 포트가 다른 구성이면 작업 대상도 그 origin이어야 한다(고정 문자열과 비교하지 않는다).
        def use_8081(payload):
            payload["task"]["description"] = "평가 대상 http://eval-meter:8081 진단"

        cases = ((use_8081, "http://eval-meter:8081", None), (None, "http://eval-meter:8081", "task_target_mismatch"))
        for change, origin, code in cases:
            with self.subTest(code=code), tempfile.TemporaryDirectory() as tmp:
                root, argv = self.inputs(tmp, change, origin)
                with patch.object(runner.getpass, "getpass", side_effect=KeyboardInterrupt) as ask, \
                        patch("sys.stderr") as err:
                    if code is None:
                        with self.assertRaises(KeyboardInterrupt):
                            runner.main(argv)
                        ask.assert_called_once()  # 작업 대상 검사를 통과해 비밀번호까지 왔다
                    else:
                        self.assertEqual(runner.main(argv), 2)
                        ask.assert_not_called()
                self.assertEqual(list(root.iterdir()), [])

    def test_condition_and_repeat_are_checked_first(self):
        import contextlib
        import io
        for flag, value, code in (("--condition", "", "condition: INVALID"), ("--condition", " 우리", "condition: INVALID"),
                                  ("--condition", "x" * 101, "condition: INVALID"), ("--repeat", "-1", "repeat: INVALID")):
            with self.subTest(flag=flag, value=value[:5]), tempfile.TemporaryDirectory() as tmp:
                root, argv = self.inputs(tmp)
                argv[argv.index(flag) + 1] = value
                err = io.StringIO()
                with patch.object(runner.getpass, "getpass") as ask, contextlib.redirect_stderr(err):
                    self.assertEqual(runner.main(argv), 2)
                ask.assert_not_called()
                self.assertEqual(err.getvalue().strip(), code)
                self.assertEqual(list(root.iterdir()), [])

    def test_bad_inputs_stop_before_asking_or_creating_a_run(self):
        import contextlib
        import io

        def missing_vocabulary(payload):
            payload["findings"]["vocabulary_path"] = "/nonexistent/vocabulary.json"

        def other_target(payload):
            payload["task"]["goal"] = "http://juice-shop:3000 에서 인가 우회를 찾는다"

        for change, code in ((missing_vocabulary, "findings_vocabulary_invalid"), (other_target, "task_target_mismatch")):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as tmp:
                root, argv = self.inputs(tmp, change)
                err = io.StringIO()
                with patch.object(runner.getpass, "getpass") as ask, contextlib.redirect_stderr(err):
                    self.assertEqual(runner.main(argv), 2)
                ask.assert_not_called()
                self.assertEqual(err.getvalue().strip(), code)
                self.assertEqual(list(root.iterdir()), [])


class TestCommandLine(unittest.TestCase):
    def test_isolated_python_runs_the_documented_command(self):
        # 운영 문서의 실행 형태(python3 -I, 다른 cwd). -I는 스크립트 디렉터리를 sys.path에 올리지 않는다.
        import subprocess
        script = Path(runner.__file__).resolve()
        with tempfile.TemporaryDirectory() as tmp:
            done = subprocess.run([sys.executable, "-I", "-B", str(script), "--help"], cwd=tmp, capture_output=True,
                                  text=True, timeout=60, check=False)
        self.assertEqual(done.returncode, 0, done.stderr[-500:])
        self.assertIn("--side", done.stdout)


class TestSignatureKey(unittest.TestCase):
    def test_key_read_only_at_sign_time_from_config_path(self):
        config = parse_runner_config(runner_config())
        plan = plan_run(config, configuration(), "ours", "ours-1")
        ports = DockerPorts(config, plan, None, docker=None, findings=dict, start=lambda: "1", settings=dict)
        with patch.object(runner, "read_runner_key", return_value=b"k" * 32) as read_key:
            signed = ports.sign({"run_id": "ours-1"})
        read_key.assert_called_once_with("/srv/eval-keys/runner.key")
        self.assertRegex(signed["signature"], r"^[0-9a-f]{64}$")



class TestHostSpec(unittest.TestCase):
    FILES = {"/proc/cpuinfo": "processor\t: 0\nmodel name\t: Example CPU @ 2.0GHz\nprocessor\t: 1\n"
                              "model name\t: Example CPU @ 2.0GHz\n",
             "/proc/meminfo": "MemTotal:       16384000 kB\nMemFree:        1 kB\n",
             "/etc/machine-id": "0123456789abcdef0123456789abcdef\n"}

    def spec(self, files):
        return runner.read_host_spec(files.get, "29.1.3", 8, "6.0.0-example")

    def test_reads_cpu_memory_kernel_docker_and_hashes_the_machine_id(self):
        host = self.spec(self.FILES)
        self.assertEqual({k: v for k, v in host.items() if k != "machine_id_sha256"},
                         {"cpu_model": "Example CPU @ 2.0GHz", "logical_cpus": 8, "memory_kib": 16384000,
                          "kernel": "6.0.0-example", "docker_server_version": "29.1.3"})
        # 원문 머신 ID는 남기지 않고, 접두를 붙인 해시만 남긴다.
        self.assertEqual(host["machine_id_sha256"], hashlib.sha256(
            b"abc2lab-eval-machine-v1:0123456789abcdef0123456789abcdef").hexdigest())
        self.assertNotIn("0123456789abcdef", json.dumps(host))
        self.assertTrue(runner._host_spec_ok(host))

    def test_missing_or_malformed_sources_fail(self):
        cases = {"no cpuinfo": {"/proc/cpuinfo": None}, "no model": {"/proc/cpuinfo": "processor\t: 0\n"},
                 "no meminfo": {"/proc/meminfo": None}, "mem unit": {"/proc/meminfo": "MemTotal: 16 MB\n"},
                 "no machine id": {"/etc/machine-id": None}, "short id": {"/etc/machine-id": "abc\n"},
                 "upper id": {"/etc/machine-id": "0123456789ABCDEF0123456789ABCDEF\n"}}
        for label, change in cases.items():
            with self.subTest(label=label):
                with self.assertRaises(RunFailure) as caught:
                    self.spec(dict(self.FILES, **change))
                self.assertEqual(caught.exception.code, "host_spec_failed")
        self.assertFalse(runner._host_spec_ok(dict(self.spec(self.FILES), logical_cpus=None)))

if __name__ == "__main__":
    unittest.main()
