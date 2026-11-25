"""탐색 분리 배치 O 회차 흐름(execute_split_run, 4-7c-3) 시험. 가짜 포트로 순서·무효 처리·정리를 본다."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import sys
import unittest
from unittest import mock
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner import RESULT_NAME, Clock  # noqa: E402
from runner_probe import INVALID, PASS, VIOLATION, ProbeOutcome  # noqa: E402
from runner_product import ProductAPIError  # noqa: E402
from runner_split_probe import SplitProbeOutcome  # noqa: E402
from runner_split_run import SplitRunSpec, execute_split_run  # noqa: E402
from runner_window import WatchSample  # noqa: E402
from runner_checks import Listener  # noqa: E402
from test_runner import FakeEvidence  # noqa: E402

IDS = {"eval-meter": "m" * 64, "explore-boundary": "b" * 64, "preparation": "p" * 64, "target": "t" * 64, "eval-obs": "o" * 64}
DAEMON = {"version": "29.1.3", "os": "linux", "arch": "amd64", "platform": "Docker Engine - Community"}
# 점검 행(runner_probe._row 모양): 출발점마다 양성·부정 하나씩과 판정하지 않는 기록 행 하나(분리 배치 §2.1 표)
PROBE_ROWS = (
    {"id": "meter_ip:10.252.3.2:8080", "kind": "tcp", "target": "10.252.3.2:8080", "role": "positive", "expected": "success",
     "accepted": ["connected"], "observed": "success", "code": "connected", "verdict": "match"},
    {"id": "target_ip:10.252.5.3:3000", "kind": "tcp", "target": "10.252.5.3:3000", "role": "negative", "expected": "fail",
     "accepted": ["timeout", "unreachable"], "observed": "fail", "code": "timeout", "verdict": "match"},
    {"id": "external", "kind": "tcp", "target": "openrouter.ai:443", "role": "record", "expected": "any", "accepted": [],
     "observed": "success", "code": "connected", "verdict": "recorded"},
)
TARGET_ORIGIN = "http://eval-meter:8080"
HOST = {"cpu_model": "x", "logical_cpus": 8, "memory_kib": 16 * 1024 * 1024, "kernel": "6.18", "docker_server_version": "29.1.3",
        "machine_id_sha256": "8" * 64}
OBSERVER_PATH = "/api/Challenges"
SEALED_AT, DRAINED_AT = "2026-11-17T07:01:00.100000+00:00", "2026-11-17T07:01:00.200000+00:00"
IMAGES = {"product": "sha256:" + "1" * 64, "meter": "sha256:" + "2" * 64, "explore_browser": "sha256:" + "3" * 64,
          "fixture": "sha256:" + "4" * 64, "target": "sha256:" + "5" * 64}
FINGERPRINT = "f" * 64
E3_SHARE_OFF = {"e3_confirmation_requests": 0, "e3_confirmation_timeout_ms": 0}
LIMITS = {"max_requests": 10, "max_attempts": 30, "max_concurrent": 2, "max_body_bytes": 1024, "max_response_body_bytes": 1024,
          "request_timeout_ms": 1000}
PRODUCT_FILES = {"installation": "a" * 64, "policy": "b" * 64, "api_launch": "c" * 64, "target_policy": "d" * 64}
PROCESS_LIMITS = {"bash_slots": 3, "container_tasks": 512, "engine_reserve_tasks": 64, "call_tasks": 128, "command_tasks": 96,
                  "helper_reserve_tasks": 8}
SCOPE = {"RunRef": "run-draft-1", "TaskRef": "42", "CaseRef": "first-diagnosis", "ResetRef": "r", "PolicyRef": "policy-draft-1"}


def conditions(**changes) -> bytes:
    value = {"version": "e2-runtime-conditions/v1", "scope": dict(SCOPE), "policy_revision": "v1", "worker_count": 3,
             "process_limits": dict(PROCESS_LIMITS), "queue_consumes_command_timeout": True}
    value.update(changes)
    return (json.dumps(value) + "\n").encode()


def surface(product=IMAGES["product"], browser=IMAGES["explore_browser"]):
    return {"format": "abc2lab.tool-surface-side/v1", "side": "ours", "image_id": product, "browser_image_id": browser,
            "cli": {"sha256": "5" * 64, "count": 1, "commands": {}},
            "mcp": {"package_version": "0.0.84", "raw": {"names": ["a"], "sha256": "6" * 64},
                    "exposed": {"names": ["a"], "sha256": "7" * 64}}}
HEALTH = {"preparation": "test -S /p/ipc/preparation.sock"}
START = datetime(2026, 11, 17, 7, 0, tzinfo=timezone.utc)
LEDGER = b"r1 10 20 30\nr1 10 20 30\n"
CALLS = b'{"seq":1}\n{"seq":2}\n'
GOAL = {"version": "goal-digest/v1", "common_sha256": "c" * 64, "product_sha256": "d" * 64}
SAMPLING = {"temperature": 1.0, "top_p": 0.95, "top_k": 20, "min_p": 0, "presence_penalty": 1.5, "repetition_penalty": 1.0,
            "seed": 42}


LLM_SLUG, LLM_NAME = "example-provider", "Example"


def product_marker(**overrides):
    """제품 호출 기록 사후 검증 표식(eval-meter -role generation-verify). 게이트웨이 표식과 같은 모양, 거부 두 칸은 null."""
    generation = {"id": "gen-1", "status": "verified", "provider_name": LLM_NAME,
                  "attempts": [{"provider_name": LLM_NAME, "endpoint_id": "ep-1", "status": 200}],
                  "lookup": {"tries": 1, "last": "verified"}}
    base = {"role": "generation-verify", "backend": "api", "provider": LLM_SLUG, "sampling": dict(SAMPLING), "sealed_at": "a",
            "drained_at": "a", "verified_at": "b", "forwarded_requests": 2, "upstream_errors": 0,
            "upstream_error_ends": {"client_closed_after_done": 0, "client_closed_before_done": 0, "upstream_cut_after_done": 0,
                                    "upstream_cut_before_done": 0},
            "rejected_requests": 0, "rejected_after_seal": 0, "cancelled_by_seal": 0, "responses_without_generation_id": 0,
            "generations": [generation], "log_complete": True, "rejected_sampling": None, "rejected_provider": None,
            "rate_limited": 0}
    return base | overrides


def spec(**changes):
    values = dict(run_id="ours-1", project="eval-ours-1", image_ids=dict(IMAGES), request_budget=10, time_budget_seconds=60.0,
                  poll_seconds=1.0, drain_timeout_seconds=5.0, events_lookback_seconds=10.0, host_watch_max_gap_seconds=15.0,
                  task_status_wait_seconds=2.0, healthchecks=dict(HEALTH), product_close_seconds=30.0, post_settle_seconds=2,
                  llm_sampling=dict(SAMPLING), llm_provider_slug=LLM_SLUG, llm_provider_name=LLM_NAME, meter_source_sha="a" * 40,
                  path_key_ref=PATH_KEY_REF, condition="ours@35b", fixture_fingerprint="e" * 64,
                  environment_fingerprint="9" * 64, target_origin=TARGET_ORIGIN, observer_path=OBSERVER_PATH,
                  observer_readonly_ref="readonly-1", model_tag="model-x", drain_grace_seconds=30,
                  driver_settings={"task_wait_seconds": 30.0, "poll_interval_seconds": 1.0, "registration_valid_hours": 24})
    values.update(changes)
    return SplitRunSpec(**values)


def snapshot():
    """runner_checks.network_snapshot 모양(격리 근거 행·정책 지문이 읽는 칸)."""
    return {"daemon": dict(DAEMON), "project": "eval-ours-1",
            "networks": {"eval_target_back": {"id": "n" * 64, "name": "eval-ours-1_eval_target_back", "internal": True, "gateways": [],
                                              "members": {"eval-meter": ["10.252.5.2"], "target": ["10.252.5.3"]}, "names": {}}},
            "services": {name: {"id": cid, "image": IMAGES["product"], "running": True, "started_at": "2026-11-17T06:59:00Z",
                                "restart_count": 0, "exposed": [], "bindings": []} for name, cid in IDS.items()}}


def probe(status=PASS, cid="c"):
    origin = ProbeOutcome(status, () if status == PASS else ("negative_path_reachable",), PROBE_ROWS, "s", "f", cid * 64)
    return SplitProbeOutcome(status, () if status == PASS else ("preparation:negative_path_reachable",), {"preparation": origin})


class FakeClockState:
    def __init__(self):
        self.t = 0.0

    def clock(self):
        return Clock(lambda: START + timedelta(seconds=self.t), lambda: self.t, self.sleep)

    def sleep(self, seconds):
        self.t += seconds


PATH_KEY_REF = "a" * 16


class FakeMeter:
    """분리 배치 계측기 bind 흉내. 경로 기록은 늘 켜져 있다(compose.split.yaml)."""

    def __init__(self, ports):
        self.request_cap = 10
        self.sealed_cap = None
        self.ports = ports
        self.path_log = True
        self.path_key_ref = PATH_KEY_REF
        self.sealed = {"role": "engine", "sealed_at": SEALED_AT}
        self.observer_extra = 0
        self.path_rows_error = None
        self.log_complete = True

    def forwarded(self):
        return self.ports.forwarded

    def marker(self, name):
        if name == "engine-sealed.json":
            # 같은 계측기의 봉인 표식도 상한 값을 적는다(drain 표식과 같아야 한다, #734 리뷰)
            if self.sealed is None:
                return None
            return dict(self.sealed, request_cap=self.request_cap if self.sealed_cap is None else self.sealed_cap,
                        rejected_over_cap=0)
        self.ports.calls.append("meter:drained")
        self.ports.clock.t += self.ports.drain_delay  # drain 확인에 걸린 시간(타임라인 engine_end·drained 구분 시험용)
        # 계측기 하드 상한: 기본 시험 계획의 공통 요청 예산(10)이 계측기에 걸린 모양이다.
        return {"inflight_at_drain": 0, "log_complete": self.log_complete, "forced_cancelled_requests": self.ports.forced,
                "forwarded_requests": self._total(), "drained_at": DRAINED_AT, "rejected_after_seal": 0, "forced_closed_streams": 0,
                "request_cap": self.request_cap, "rejected_over_cap": 0}

    def observer_rows(self):
        # 러너가 관측기로 보낸 pre·post 점수판 GET 두 건(대상 준비 확인 GET은 O에 없다)
        count = 2 + self.observer_extra
        return [{"seq": i, "decision": "registered", "method": "GET", "status": 200, "started_at": f"s{i}",
                 "completed_at": f"c{i}", "response_bytes": 10} for i in range(1, count + 1)]

    def _total(self):
        return max(self.ports.forwarded, self.ports.forced)  # 강제 취소된 요청도 전달 행이다

    def engine_rows(self):
        n, forced = self._total(), self.ports.forced
        return [{"seq": i, "method": "GET", "kind": "request", "outcome": "forced_cancelled" if i <= forced else "completed",
                 "response": "complete", "started_at": "t", "completed_at": "t"} for i in range(1, n + 1)]

    def path_log_present(self):
        return self.path_log

    def path_rows(self, rows):
        if self.path_rows_error:
            raise self.path_rows_error
        return [{"seq": row["seq"], "path_key_ref": self.path_key_ref} for row in rows]


class FakePorts:
    def __init__(self, clock: FakeClockState):
        self.clock = clock
        self.calls: list[str] = []
        self.forwarded = 0
        self.fail: dict[str, Exception] = {}
        self.probe_status = {"pre": PASS, "post": PASS}
        self.events_raw: list = []
        self.statuses = [{"id": "draft-1", "state": "running", "phase": "execution"}]
        # 제품 snapshot 모양 그대로: 빠진 끝점이 없으면 counts.omitted 칸이 없다(omitempty)
        self.workflow = {"job_id": "draft-1", "endpoints": [{"source": "browser_navigation"}, {"source": "api_recon_runtime"},
                                                            {"source": "html_link"}, {"source": "html_link"}],
                         "counts": {"pages": 2}}
        self.record = {"version": 1, "task_id": 42, "goal": dict(GOAL),
                       "model": {"requests": 2, "sampling_overwrites": 0, "sampling": dict(SAMPLING)},
                       "run": {"phase": "closed", "run_ref": "run-draft-1",
                               "execution": {"scope": dict(SCOPE), "policy_revision": "v1"}}}
        self.ledger, self.calls_file = LEDGER, CALLS
        self.e2_v3_row = {"format": "abc2lab-eval-e2-v3-collection-v1", "outcome": "uncollected", "reason": "e2_v3_export_unavailable"}
        self.e2_v3_files = (b"journal", b"head")
        # 제품 활동 표의 Bash 호출 줄(cmd·res·is_error). 기본은 O에 없는 python을 부른 Worker 호출 하나와 보통 호출 하나.
        self.commands = [
            {"cmd": json.dumps({"command": "python -c 'print(1)'"}), "is_error": True,
             "res": json.dumps({"trusted": {"exit_code": 127}, "tool_output": "bash: line 1: python: command not found"})},
            {"cmd": json.dumps({"command": "curl -s http://app.eval.test:8080/"}), "is_error": False,
             "res": json.dumps({"trusted": {"exit_code": 0}, "tool_output": "ok"})},
        ]
        self.task_end = {"status": "done", "completed_at": "2026-11-17T07:01:37Z", "llm_failover_state": "default", "goals_total": 3,
                         "goals_met": 3, "goal_states": {"met": 3}}
        self.forced = 0
        self.boundary_stopped = False
        self.snapshot_times: dict[str, float] = {}
        self.layout = {"pre": [], "post": []}
        self.snapshot_change: dict[str, dict] = {}
        self.marker: dict | None = product_marker()
        self.verify_args: list = []
        self.surface = surface()
        # #716 뒤 제품 보기 모양: 확정 몫 칸은 꺼짐이어도 0으로 늘 있다
        self.view = {"format": "abc2lab.control-policy-view/v1", "policy_ref": "policy-draft-1", "policy_revision": "v1",
                     "policy_fingerprint_sha256": FINGERPRINT, "limits": dict(LIMITS, **E3_SHARE_OFF), "rules": []}
        self.conditions = conditions()
        self.files_args: list = []
        self.solved = {"pre": {"a": False, "b": False}, "post": {"a": True, "b": False}}
        self.drain_delay = 0.0
        self.listeners: set = set()
        # 제품 회차 디렉터리의 E③ 확정 단계 결과(e3contract.RunResult). 기본은 공식 기대값(선언 없음으로 비활성)이다.
        self.trace_args: list[str] = []
        self.trace_sources = {"e2_host_journal": b"", "e2_product_journal": b"", "declarations": b""}
        self.e3_file = json.dumps({"version": "e3-confirmation-run/v1", "status": "disabled", "reason": "declaration_missing",
                                   "verdicts": [], "skipped": [], "candidates": [], "requests": 0, "writes": []}).encode()

    def _call(self, name):
        self.calls.append(name)
        if name in self.fail:
            raise self.fail[name]

    def preexisting(self):
        self._call("preexisting")
        return False

    def image_ids(self):
        self._call("image_ids")
        return dict(IMAGES)

    def meter_describe(self):
        self._call("meter_describe")
        from meter_describe import EXPECTED, FORMAT
        if getattr(self, "describe_override", "unset") != "unset":
            return self.describe_override
        return {"format": FORMAT, "markers": {k: {"required": list(v["required"]), "optional": list(v["optional"])}
                                              for k, v in EXPECTED.items()},
                "build": {"source_revision": "a" * 40, "vcs_revision": "", "vcs_modified": None, "go_version": "go1.26.3"}}

    def host_spec(self):
        self._call("host_spec")
        return dict(HOST)

    def planned_subnets(self):
        self._call("planned_subnets")
        return ["10.252.0.0/20"]

    def host_subnets(self):
        return []

    def up(self):
        self._call("up")

    def snapshot(self, phase):
        self._call(f"snapshot:{phase}")
        self.snapshot_times[phase] = self.clock.t
        snap = snapshot()
        snap["services"].update(self.snapshot_change.get(phase, {}))
        return snap

    def generation_verify(self, calls, sampling):
        self._call("generation_verify")
        self.verify_args.append((calls, sampling))

    def llm_verified(self):
        self._call("llm_verified")
        return self.marker

    def check(self, phase):
        self._call(f"check:{phase}")
        return {"phase": phase, "violations": list(self.layout[phase]), "containers": 15, "running": 20}

    def probes(self, phase, snap):
        self._call(f"probes:{phase}")
        # 실제 경계가 멈추면 두 입구·netns가 없어 양성 대조가 실패한다(#665 리뷰). 그 효과를 흉내 낸다.
        status = INVALID if self.boundary_stopped else self.probe_status[phase]
        return probe(status, "1" if phase == "pre" else "2")

    def dns_token(self):
        self._call("dns_token")
        return "token"

    def start_events(self, since):
        self._call("events:start")
        ports = self

        class Stream:
            def stop(self, until_ns):
                ports.calls.append("events:stop")
                return list(ports.events_raw), True
        return Stream()

    def start_host_watch(self):
        self._call("watch:start")
        ports = self

        class Handle:
            def stop(self):
                ports.calls.append("watch:stop")
                return 0.0, 1.0, [WatchSample(0.5, (), None)]
        return Handle()

    def scoreboard(self, phase, snap):
        self._call(f"scoreboard:{phase}")
        context = {"run_id": "ours-1", "model_tag": "model-x", "reset_generation": snap["services"]["target"]["id"],
                   "instance_id": snap["services"]["target"]["id"], "execution_id": snap["services"]["preparation"]["id"]}
        at = START + timedelta(seconds=self.clock.t)
        return {"raw": dict(context, path=f"{phase}/raw.json", sha256=("1" if phase == "pre" else "2") * 64, size=10, phase=phase),
                "normalized": dict(context, path=f"{phase}/normalized.json", sha256=("3" if phase == "pre" else "4") * 64, size=5,
                                   phase=phase),
                "requested_at": at.isoformat(), "received_at": at.isoformat(), "solved": dict(self.solved[phase]),
                "state_sha256": "s" * 64}

    def host_listeners(self):
        return frozenset(self.listeners)

    def prepare_round(self, token):
        self._call("prepare_round")
        assert token == "token"
        return {"target": {"id": "target-" + "a" * 32}, "catalog_profile": {"workflow_request_limit": 400}, "draft": {"id": "draft-1"}}

    def start_round(self, prepared):
        self._call("start_round")
        return {"draft_id": "draft-1", "task_id": "42", "state": "running"}

    def product_status(self, draft_id):
        self.calls.append("status")
        item = self.statuses.pop(0) if len(self.statuses) > 1 else self.statuses[0]
        return item

    def background(self, work):
        work()

    def stop_round(self, draft_id):
        self._call("stop_round")
        assert draft_id == "draft-1"
        self.stopped = True
        return {"id": draft_id, "state": "stopping"}

    def wait_closed(self, draft_id, deadline):
        self._call("wait_closed")
        assert deadline == 30.0 and getattr(self, "stopped", False)  # 멈춘 뒤에만 마감을 기다린다
        return {"id": draft_id, "state": "cancelled", "drain_complete": True, "result_ready": True, "closing_reason": None}

    def seal(self, meter_id):
        self._call("seal")
        self.sealed_t = self.clock.t
        assert meter_id == IDS["eval-meter"]
        self.forwarded = self.forwarded  # 봉인 뒤 늘지 않는다

    def stop_boundary(self, boundary_id):
        self._call("stop_boundary")
        assert boundary_id == IDS["explore-boundary"]
        self.boundary_stopped = True

    def run_record(self, task_id):
        self._call("run_record")
        return self.record

    def e2_v3_export(self, draft_id):
        self._call("e2_v3_export")
        if isinstance(self.e2_v3_files, Exception):
            raise self.e2_v3_files
        return self.e2_v3_files

    def e2_v3_collect(self, journal, head):
        self._call("e2_v3_collect")
        if isinstance(self.e2_v3_row, Exception):
            raise self.e2_v3_row
        return self.e2_v3_row
    def task_exploration_id(self, task_id):
        self._call("task_exploration_id")
        return 7

    def product_commands(self, exploration_id):
        self._call("product_commands")
        if isinstance(self.commands, Exception):
            raise self.commands
        return self.commands

    def task_signal(self, task_id):
        self._call("task_signal")
        if isinstance(self.task_end, Exception):
            raise self.task_end
        return self.task_end

    def workflow_snapshot(self, draft_id):
        self._call("workflow_snapshot")
        if isinstance(self.workflow, Exception):
            raise self.workflow
        return self.workflow

    def copy_boundary_evidence(self):
        self._call("copy_boundary_evidence")

    def tool_surface(self):
        self._call("tool_surface")
        return self.surface

    def control_policy(self, task_id):
        self._call("control_policy")
        assert task_id == "42"
        return self.view

    def product_files(self):
        self._call("product_files")
        return dict(PRODUCT_FILES)

    def split_conditions(self):
        self._call("split_conditions")
        return {"workflow_defaults_sha256": "a" * 64, "workflow_defaults": {}, "workflow": {}, "registration": {}}

    def boundary_directory(self):
        return Path("/runs/ours-1/boundary")

    def sign(self, payload):
        self._call("sign")
        return dict(payload, signature="s")

    def e3_confirmation(self, draft_id):
        self._call("e3_confirmation")
        if isinstance(self.e3_file, Exception):
            raise self.e3_file
        return self.e3_file

    def e3_trace_sources(self, draft_id):
        self._call("e3_trace_sources")
        self.trace_args.append(draft_id)
        if isinstance(self.trace_sources, Exception):
            raise self.trace_sources
        return self.trace_sources

    def runtime_conditions(self, draft_id):
        self._call("runtime_conditions")
        self.files_args.append(draft_id)
        if isinstance(self.conditions, Exception):
            raise self.conditions
        return self.conditions

    def copy_product_records(self):
        self._call("copy_product_records")
        return self.ledger, self.calls_file

    def down(self):
        self._call("down")

    def remove_inputs(self):
        self._call("remove_inputs")


def run(mutate=None, **spec_changes):
    clock = FakeClockState()
    ports = FakePorts(clock)
    if mutate:
        mutate(ports)
    evidence = FakeEvidence()
    result = execute_split_run(spec(**spec_changes), ports, FakeMeter(ports), evidence, clock.clock())
    return result, ports, evidence


ORDER_HEAD = ["preexisting", "image_ids", "meter_describe", "tool_surface", "host_spec", "planned_subnets", "up", "snapshot:pre", "check:pre", "probes:pre", "dns_token",
              "events:start", "watch:start", "scoreboard:pre", "prepare_round", "start_round"]
ORDER_TAIL = ["seal", "meter:drained", "stop_round", "wait_closed", "workflow_snapshot", "snapshot:post", "check:post", "probes:post", "scoreboard:post",
              "stop_boundary", "watch:stop",
              "events:stop", "status", "run_record", "task_signal", "control_policy", "product_files", "runtime_conditions", "e3_confirmation",
              "e3_trace_sources", "copy_boundary_evidence", "split_conditions",
              "copy_product_records",
              "generation_verify", "llm_verified", "down", "remove_inputs", "sign", "sign"]  # 내린 뒤 러너 기록·결과 서명


class TestObserverFlag(unittest.TestCase):
    def test_flag_is_in_complete_and_invalid_results_without_path(self):
        result, _, evidence = run(observer_login_file=True)
        self.assertEqual((result["status"], result["observer_login_file"]), ("complete", True))
        self.assertNotIn("observer_password", json.dumps(evidence.files, default=str))

        def drop(ports):
            ports.view["policy_fingerprint_sha256"] = ""
        bad, _, _ = run(drop, observer_login_file=True)
        self.assertEqual((bad["status"], bad["observer_login_file"]), ("invalid", True))


class TestOrder(unittest.TestCase):
    def test_complete_run_order_and_result(self):
        result, ports, evidence = run()
        self.assertEqual(ports.calls[:len(ORDER_HEAD)], ORDER_HEAD)
        tail = [c for c in ports.calls[len(ORDER_HEAD):] if c != "status"]
        self.assertEqual(tail, [c for c in ORDER_TAIL if c != "status"])
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual((result["stop_reason"], result["first_limit"]["value"], result["goal_common_sha256"]),
                         ("time_budget", "runner_time_budget", "c" * 64))
        self.assertEqual(result["sampling_overwrites"], {"execution": 0, "preparation": []})
        self.assertIs(result["observer_login_file"], False)  # 화면 관찰 로그인 파일: 예/아니오만(기본 아니오)
        self.assertEqual(set(evidence.files), {"meter-preflight.json", "subnet-check.json", "pre-checks.json", "post-checks.json",
                                               "pre-probe.json",
                                               "post-probe.json", "task.json", "llm-summary.json",
                                               "window-watch.json", "product-end.json", "product-task-end.json", "product-model.json",
                                               "tool-surface.json",
                                               "product-files.json", "discovery-sources.json", "fault-stall.json", "early-end.json", "product-calls.jsonl",
                                               "e3-traces.json",
                                               RESULT_NAME, "isolation-evidence.json", "solve-events.json", "run-record.json"})
        self.assertEqual(evidence.files["discovery-sources.json"], {
            "format": "abc2lab-eval-runner-discovery-sources-v1", "endpoints": 4,
            "by_source": {"api_recon_runtime": 1, "browser_navigation": 1, "html_link": 2}, "browser": 1, "api_recon": 1,
            "omitted": 0, "detail_truncated": False})
        self.assertEqual(result["discovery_sources"], evidence.files["discovery-sources.json"])
        self.assertEqual(evidence.files[RESULT_NAME], result)
        # 멈춤 집계는 B처럼 기록만 한다: 이 가짜 기록(시각 칸 없음)은 고정 코드로 남고 회차는 그대로 완료다
        self.assertEqual(evidence.files["fault-stall.json"], {"format": "abc2lab-eval-runner-fault-stall-v3", "judged": False,
                                                              "error": "fault_stall_rows_invalid"})
        self.assertEqual(result["fault_stall"], evidence.files["fault-stall.json"])
        # 결정안 §3.3 제품 블록 근거: 파일 원본 SHA 다섯 + 상한 스냅숏 지문, E② 정책 내용 지문, 도구 면 지문.
        from preservation_record import digest
        files = evidence.files["product-files.json"]
        self.assertEqual(files["file_sha256"], dict(PRODUCT_FILES, control_limits=digest(dict(LIMITS, **E3_SHARE_OFF)),
                                                    runtime_conditions=hashlib.sha256(conditions()).hexdigest()))
        self.assertEqual((files["diagnosis_policy_sha256"], files["limits"], files["diagnosis_entered"]), (FINGERPRINT, dict(LIMITS, **E3_SHARE_OFF), True))
        self.assertEqual((files["policy_ref"], files["policy_revision"]), ("policy-draft-1", "v1"))
        self.assertEqual((result["product_file_sha256"], result["diagnosis_policy_sha256"]), (files["file_sha256"], FINGERPRINT))
        self.assertEqual(ports.files_args, ["draft-1"])  # 실행 디렉터리 이름 = 초안 ID
        self.assertTrue(result["diagnosis_entered"])
        self.assertEqual(result["tool_surface"], {"cli_sha256": "5" * 64, "mcp_raw_sha256": "6" * 64, "mcp_exposed_sha256": "7" * 64,
                                                  "mcp_version": "0.0.84"})
        self.assertEqual(evidence.files["tool-surface.json"], ports.surface)
        self.assertEqual(set(evidence.files["pre-probe.json"]["origins"]), {"preparation"})
        task = evidence.files["task.json"]
        self.assertEqual((task["format"], task["draft_id"], task["task_id"]), ("abc2lab-eval-runner-split-task-v1", "draft-1", "42"))

    def test_policy_fingerprint_and_surface_images_are_required(self):
        # 내용 지문이 없으면(자격 pin 규칙이면 제품이 비운다) v3 짝 대조 칸을 못 만든다 → 무효.
        for value in ("", None, "F" * 64, "f" * 63, "g" * 64):
            def drop(ports, value=value):
                ports.view["policy_fingerprint_sha256"] = value
            result, ports, _ = run(drop)
            with self.subTest(value=value):
                self.assertEqual((result["status"], result["reasons"]), ("invalid", ["diagnosis_policy_fingerprint_missing"]))
                self.assertNotIn("product_files", ports.calls)
                self.assertIn("down", ports.calls)
        # 상한 스냅숏은 여섯 칸 모두 양의 정수만(#693 리뷰 ①). 추가 칸·불리언·음수·숫자 문자열·빈 상한은 무효다.
        for case, limits in {"empty": {}, "bool": dict(LIMITS, max_concurrent=True), "negative": dict(LIMITS, max_attempts=-1),
                             "zero": dict(LIMITS, max_requests=0), "string": dict(LIMITS, max_body_bytes="1024"),
                             "extra": dict(LIMITS, more={"x": 1}), "missing": {k: v for k, v in LIMITS.items() if k != "max_requests"},
                             "too_big": dict(LIMITS, request_timeout_ms=1 << 63), "float": dict(LIMITS, max_requests=1.0),
                             "not_object": [1]}.items():
            def bad_limits(ports, limits=limits):
                ports.view["limits"] = limits
            result, ports, evidence = run(bad_limits)
            with self.subTest(limits=case):
                self.assertEqual((result["status"], result["reasons"]), ("invalid", ["control_policy_limits_invalid"]))
                self.assertNotIn("product-files.json", evidence.files)
        # 보기의 다른 칸은 근거·지문에 들어가지 않는다(허용된 상한 여섯 칸만).
        def extra_view(ports):
            ports.view["rules"] = [{"ref": "x"}]
            ports.view["note"] = "not evidence"
        result, _, evidence = run(extra_view)
        self.assertEqual(result["status"], "complete", result)
        self.assertNotIn("note", json.dumps(evidence.files["product-files.json"]))
        # 도구 면을 다른 이미지로 쟀으면 회차 이미지와 맞지 않는다 → 띄우기 전에 무효.
        for wrong in ({"product": "sha256:" + "9" * 64}, {"browser": "sha256:" + "9" * 64}):
            def swap(ports, wrong=wrong):
                ports.surface = surface(**wrong)
            result, ports, _ = run(swap)
            with self.subTest(wrong=wrong):
                self.assertEqual((result["status"], result["reasons"]), ("invalid", ["tool_surface_image_mismatch"]))
                self.assertNotIn("up", ports.calls)

    def test_official_round_checks_the_e3_share_of_the_policy_it_used(self):
        # 회차 흐름 연결: E③ 비활성 요구(설정 칸)가 켜져 있으면 제품이 그 회차에 쓴 정책의 확정 몫을 본다. 꺼져 있으면 몫 칸을
        # 요구하지 않는다. purpose는 이 검사를 걸지 않는다(2026-11-20 사용자: 배선 확인 회차도 확정 단계를 켤 수 있다).
        def limits(share):
            return lambda p: p.view.update(limits=dict(LIMITS, **share))
        for purpose, required, share, status, reasons in (
                ("official", True, E3_SHARE_OFF, "complete", None),
                ("official", True, {}, "invalid", ["e3_share_unconfirmed"]),  # #716 전 제품 보기 모양
                ("official", True, dict(E3_SHARE_OFF, e3_confirmation_requests=2), "invalid", ["e3_share_not_zero"]),
                ("wiring_check", True, dict(E3_SHARE_OFF, e3_confirmation_requests=2), "invalid", ["e3_share_not_zero"]),
                ("wiring_check", False, dict(E3_SHARE_OFF, e3_confirmation_requests=2), "complete", None),
                ("browser_acceptance", False, {}, "complete", None),
                ("browser_acceptance", False, dict(E3_SHARE_OFF, e3_confirmation_timeout_ms=1000), "complete", None)):
            with self.subTest(purpose=purpose, required=required, share=share):
                result, _, _ = run(limits(share), purpose=purpose, e3_disabled_required=required)
                self.assertEqual((result["status"], result.get("reasons")), (status, reasons), result)

    def test_official_e3_share_must_be_present_and_zero(self):
        # 공식 측정 E③ 비활성(평가 프로토콜 §5.1, 2026-11-18 합의): 그 회차 E② 정책의 확정 몫 칸이 있고 0이어야 한다.
        from runner_split_run import RunFailure, check_official_e3_share, checked_control_policy

        def view(**share):
            return {"format": "abc2lab.control-policy-view/v1", "policy_ref": "policy-draft-1", "policy_revision": "v1",
                    "policy_fingerprint_sha256": FINGERPRINT, "limits": dict(LIMITS, **share), "rules": []}
        checked = checked_control_policy(view(e3_confirmation_requests=0, e3_confirmation_timeout_ms=0), "draft-1")
        check_official_e3_share(checked["limits"])
        self.assertEqual(checked["limits"]["e3_confirmation_timeout_ms"], 0)  # 근거·지문에 몫 칸도 남는다
        for share, reason in (({}, "e3_share_unconfirmed"), ({"e3_confirmation_requests": 1, "e3_confirmation_timeout_ms": 0},
                                                           "e3_share_not_zero"),
                              ({"e3_confirmation_requests": 0, "e3_confirmation_timeout_ms": 5000}, "e3_share_not_zero")):
            limits = checked_control_policy(view(**share), "draft-1")["limits"]
            with self.subTest(share=share), self.assertRaises(RunFailure) as caught:
                check_official_e3_share(limits)
            self.assertEqual(caught.exception.code, reason)
        for share in ({"e3_confirmation_requests": 0}, {"e3_confirmation_requests": -1, "e3_confirmation_timeout_ms": 0},
                      {"e3_confirmation_requests": True, "e3_confirmation_timeout_ms": 0}):
            with self.subTest(share=share), self.assertRaises(RunFailure) as caught:
                checked_control_policy(view(**share), "draft-1")  # 몫 칸은 둘 다, 0 이상 정수로만
            self.assertEqual(caught.exception.code, "control_policy_limits_invalid")
        # 몫 칸이 없는 정책 보기(확정 몫 구현 전 빌드)도 보존 회차에서는 그대로 받는다
        self.assertEqual(run()[0]["status"], "complete")

    def test_policy_and_run_conditions_are_bound_to_this_round(self):
        # #693 리뷰 ②: 다른 초안의 정책·다른 작업의 실행 조건·JSON 아닌 실행 조건을 받으면 무효다.
        cases = {
            "other_policy_ref": (lambda p: p.view.update(policy_ref="policy-draft-2"), "control_policy_unbound"),
            "empty_revision": (lambda p: p.view.update(policy_revision=""), "control_policy_unbound"),
            "conditions_other_task": (lambda p: setattr(p, "conditions", conditions(scope=dict(SCOPE, TaskRef="43"))),
                                      "runtime_conditions_unbound"),
            "conditions_other_run": (lambda p: setattr(p, "conditions", conditions(scope=dict(SCOPE, RunRef="run-draft-2"))),
                                     "runtime_conditions_unbound"),
            "conditions_other_policy": (lambda p: setattr(p, "conditions", conditions(scope=dict(SCOPE, PolicyRef="policy-x"))),
                                        "runtime_conditions_unbound"),
            "conditions_other_revision": (lambda p: setattr(p, "conditions", conditions(policy_revision="v2")),
                                          "runtime_conditions_unbound"),
            "conditions_not_json": (lambda p: setattr(p, "conditions", b"not json"), "runtime_conditions_invalid"),
            "conditions_not_utf8": (lambda p: setattr(p, "conditions", b"\xff"), "runtime_conditions_invalid"),
            "conditions_other_version": (lambda p: setattr(p, "conditions", conditions(version="v0")), "runtime_conditions_invalid"),
            "conditions_list": (lambda p: setattr(p, "conditions", b"[]"), "runtime_conditions_invalid"),
            "execution_other_task": (lambda p: p.record["run"]["execution"].update(scope=dict(SCOPE, TaskRef="43")),
                                     "run_record_unbound"),
            "execution_other_revision": (lambda p: p.record["run"]["execution"].update(policy_revision="v2"), "run_record_unbound"),
            "execution_not_object": (lambda p: p.record["run"].update(execution="x"), "run_record_unbound"),
            "run_missing": (lambda p: p.record.pop("run"), "run_record_invalid"),
            # #693 리뷰 2차: 실행 기록 자체가 이 회차의 것이어야 실행 부재를 믿는다
            "other_run_no_execution": (lambda p: p.record.update(task_id=43) or p.record["run"].update(run_ref="run-draft-2", execution=None),
                                       "run_record_unbound"),
            "other_run_ref_same_task": (lambda p: p.record["run"].update(run_ref="run-draft-2", execution=None), "run_record_unbound"),
            "task_id_string": (lambda p: p.record.update(task_id="42"), "run_record_unbound"),
            "version_other": (lambda p: p.record.update(version=2), "product_run_record_invalid"),  # 사용량 해석이 먼저 거부
            "version_bool": (lambda p: p.record.update(version=True), "run_record_invalid"),
            "revision_newline": (lambda p: p.view.update(policy_revision="v1\nx"), "control_policy_unbound"),
            "revision_url": (lambda p: p.view.update(policy_revision="http://x"), "control_policy_unbound"),
            "revision_slash": (lambda p: p.view.update(policy_revision="v1/synthetic"), "control_policy_unbound"),
            "scope_slash": (lambda p: setattr(p, "conditions", conditions(scope=dict(SCOPE, ResetRef="a/b"))),
                            "runtime_conditions_unbound"),
            # #693 리뷰: 작업·회차·정책 참조가 같아도 ResetRef·CaseRef가 저장된 실행 범위와 다르면 다른 실행의 조건이다
            "scope_other_reset": (lambda p: setattr(p, "conditions", conditions(scope=dict(SCOPE, ResetRef="other-reset"))),
                                  "runtime_conditions_unbound"),
            "scope_other_case": (lambda p: setattr(p, "conditions", conditions(scope=dict(SCOPE, CaseRef="other-case"))),
                                 "runtime_conditions_unbound"),
            "revision_long": (lambda p: p.view.update(policy_revision="v" * 129), "control_policy_unbound"),
            "scope_extra": (lambda p: setattr(p, "conditions", conditions(scope=dict(SCOPE, More="x"))), "runtime_conditions_unbound"),
            "scope_bad_ref": (lambda p: setattr(p, "conditions", conditions(scope=dict(SCOPE, CaseRef="a b"))),
                              "runtime_conditions_unbound"),
        }
        for name, (mutate, reason) in cases.items():
            result, ports, evidence = run(mutate)
            with self.subTest(case=name):
                self.assertEqual((result["status"], result["reasons"]), ("invalid", [reason]))
                self.assertNotIn("product-files.json", evidence.files)
                self.assertIn("down", ports.calls)

    def test_run_conditions_with_duplicate_keys_are_refused_before_scope_check(self):
        # #693 리뷰: 같은 키를 두 번 넣으면 json 기본 동작은 마지막 값만 남긴다. 앞 값이 다른 범위여도 맞는 마지막 값으로
        # 대조를 지나고 모순된 원본의 SHA가 남았다. 중첩 객체까지 중복을 거부하고 SHA를 남기지 않는다.
        good = json.dumps(dict(SCOPE))
        other_reset = json.dumps(dict(SCOPE, ResetRef="other-reset"))
        head = '{"version": "e2-runtime-conditions/v1", '
        tail = (', "policy_revision": "v1", "worker_count": 3, "process_limits": ' + json.dumps(PROCESS_LIMITS)
                + ', "queue_consumes_command_timeout": true}\n')
        nested = good[:-1] + ', "ResetRef": "r"}'  # ResetRef 두 번(같은 값이어도 원본이 모순인지 판단하지 않는다)
        cases = {
            "scope_twice": head + f'"scope": {other_reset}, "scope": {good}' + tail,
            "reset_twice": head + '"scope": ' + json.dumps(dict(SCOPE, ResetRef="other-reset"))[:-1] + ', "ResetRef": "r"}' + tail,
            "case_twice": head + '"scope": ' + json.dumps(dict(SCOPE, CaseRef="other-case"))[:-1] + ', "CaseRef": "first-diagnosis"}' + tail,
            "same_value_twice": head + '"scope": ' + nested + tail,
            "limits_twice": head + f'"scope": {good}' + tail.replace('"call_tasks"', '"call_tasks": 1, "call_tasks"', 1),
            "top_twice": head + f'"scope": {good}, "policy_revision": "v2"' + tail,
        }
        for name, text in cases.items():
            def swap(ports, text=text):
                ports.conditions = text.encode()
            result, ports, evidence = run(swap)
            with self.subTest(case=name):
                self.assertEqual((result["status"], result["reasons"]), ("invalid", ["runtime_conditions_invalid"]))
                self.assertNotIn("product-files.json", evidence.files)
                self.assertIn("down", ports.calls)
        # 중복 없는 같은 내용은 그대로 받는다(시험 입력 자체가 맞는지 대조).
        def plain(ports):
            ports.conditions = (head + f'"scope": {good}' + tail).encode()
        result, _, _ = run(plain)
        self.assertEqual(result["status"], "complete", result)

    def test_run_conditions_need_every_producer_field(self):
        # #693 리뷰 2차: 맞는 버전·개정·참조만 두고 나머지 칸을 뺀 실행 조건을 받으면 안 된다.
        full = json.loads(conditions())
        variants = {name: {k: v for k, v in full.items() if k != name}
                    for name in ("worker_count", "process_limits", "queue_consumes_command_timeout")}
        variants.update({"extra": dict(full, more=1), "worker_bool": dict(full, worker_count=True),
                         "worker_negative": dict(full, worker_count=-1), "queue_string": dict(full, queue_consumes_command_timeout="true"),
                         "limits_missing": dict(full, process_limits={k: v for k, v in PROCESS_LIMITS.items() if k != "call_tasks"}),
                         "limits_float": dict(full, process_limits=dict(PROCESS_LIMITS, bash_slots=1.5)),
                         "limits_list": dict(full, process_limits=[1])})
        for name, value in variants.items():
            def swap(ports, value=value):
                ports.conditions = (json.dumps(value) + "\n").encode()
            result, _, evidence = run(swap)
            with self.subTest(case=name):
                self.assertEqual((result["status"], result["reasons"]), ("invalid", ["runtime_conditions_invalid"]))
                self.assertNotIn("product-files.json", evidence.files)

    def test_round_without_diagnosis_has_no_run_conditions(self):
        # #693 리뷰 ③: 탐색 중 예산이 끝나 진단 실행이 붙지 않은 회차는 실행 조건 파일이 없다. 진단 미진입(run.execution 없음)이
        # 확인될 때만 null로 남기고 first_limit은 그대로다. 진단에 들어간 회차에서 파일이 없으면 계속 실패다.
        baseline, _, _ = run()

        def not_entered(ports):
            ports.record["run"].pop("execution")
            ports.conditions = OSError("no such file")
        result, ports, evidence = run(not_entered)
        self.assertEqual(result["status"], "complete", result)
        self.assertNotIn("runtime_conditions", ports.calls)
        files = evidence.files["product-files.json"]
        self.assertEqual((files["diagnosis_entered"], files["file_sha256"]["runtime_conditions"]), (False, None))
        self.assertEqual((result["diagnosis_entered"], result["first_limit"]), (False, baseline["first_limit"]))
        self.assertEqual(files["diagnosis_policy_sha256"], FINGERPRINT)  # 설치된 정책은 진단 전에도 있다

        def entered_but_missing(ports):
            ports.conditions = OSError("no such file")
        result, _, _ = run(entered_but_missing)
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["runtime_conditions_failed"]))

    def test_budget_clock_starts_after_preparation(self):
        # 로그인~초안(준비)은 예산 시계 밖이다. B의 작업 생성 직전과 같은 뜻으로 시작 요청 직전부터 잰다.
        def mutate(ports):
            original = ports.prepare_round

            def slow(token):
                ports.clock.t += 30.0
                return original(token)
            ports.prepare_round = slow
        result, _, evidence = run(mutate)
        self.assertEqual(evidence.files["task.json"]["requested_at"], (START + timedelta(seconds=30)).isoformat())
        self.assertEqual(result["finished_at"], (START + timedelta(seconds=90)).isoformat())

    def test_runner_budget_end_stops_the_running_product_before_reading_records(self):
        # 리뷰 반례: 러너 예산으로 끝났는데 제품은 아직 running이다. 멈추고 닫힐 때까지 본 뒤에 실행 기록·장부를 읽는다.
        result, ports, _ = run()
        self.assertEqual(result["status"], "complete", result)
        order = [c for c in ports.calls if c != "status"]
        self.assertLess(order.index("stop_round"), order.index("wait_closed"))
        self.assertLess(order.index("wait_closed"), order.index("run_record"))
        self.assertLess(order.index("wait_closed"), order.index("copy_product_records"))
        self.assertEqual((result["product_closed"]["state"], result["product_closed"]["drain_complete"]), ("cancelled", True))
        # 창 동안의 첫 끝 상태는 없고(제품이 러너 예산까지 실행 중), first_limit은 러너 상한이다.
        self.assertEqual(result["first_limit"]["value"], "runner_time_budget")

    def test_post_probes_run_while_the_boundary_is_alive(self):
        # 리뷰 반례: 경계를 먼저 멈추면 사후 점검이 무효가 된다. 정상 회차는 점검 뒤에 멈춘다.
        result, ports, _ = run()
        self.assertEqual(result["status"], "complete", result)
        order = [c for c in ports.calls if c != "status"]
        self.assertLess(order.index("probes:post"), order.index("stop_boundary"))
        self.assertLess(order.index("stop_boundary"), order.index("watch:stop"))  # 정지는 구간 안(감시가 허용 대상으로 본다)

    def test_forced_cancellation_waits_the_common_settle(self):
        result, ports, _ = run(lambda p: setattr(p, "forced", 1))
        self.assertEqual((result["post_settle_seconds"], result["post_settle_applied_seconds"]), (2, 2))
        plain, plain_ports, _ = run()
        self.assertEqual(plain["post_settle_applied_seconds"], 0)
        self.assertEqual(ports.snapshot_times["post"] - plain_ports.snapshot_times["post"], 2)

    def test_product_budget_closing_is_first_limit(self):
        def mutate(ports):
            ports.statuses = [{"id": "draft-1", "state": "running"},
                              {"id": "draft-1", "state": "completed", "closing_reason": "budget_limited", "outcome": "partial"}]
        result, _, evidence = run(mutate)
        self.assertEqual(result["first_limit"]["value"], "product_limit:budget_limited")
        self.assertEqual(evidence.files["product-end.json"]["first_terminal"]["closing_reason"], "budget_limited")

    def test_request_budget_stops_the_run(self):
        result, ports, _ = run(lambda p: setattr(p, "forwarded", 0))
        self.assertEqual(result["stop_reason"], "time_budget")

        def mutate(ports):
            original = ports.start_round

            def start(prepared):
                out = original(prepared)
                ports.forwarded = 10
                return out
            ports.start_round = start
        result, _, _ = run(mutate)
        self.assertEqual((result["stop_reason"], result["forwarded_at_stop"]), ("request_budget", 10))


class TestInvalid(unittest.TestCase):
    def assert_invalid(self, reason, mutate, *, up=True):
        result, ports, evidence = run(mutate)
        self.assertEqual((result["status"], result["reasons"][0]), (INVALID, reason), result)
        self.assertEqual("down" in ports.calls, up)
        self.assertEqual(ports.calls[-1], "remove_inputs")  # 보호 입력은 늘 지운다
        self.assertNotIn("first_limit", result)
        return result, ports, evidence

    def test_stale_meter_image_is_refused_before_up(self):
        _, ports, evidence = self.assert_invalid("meter_describe_unavailable", lambda p: setattr(p, "describe_override", None), up=False)
        self.assertNotIn("prepare_round", ports.calls)
        self.assertEqual(evidence.files["meter-preflight.json"]["expected_source_sha"], "a" * 40)

    def test_before_up(self):
        def exists(ports):
            ports.preexisting = lambda: (ports.calls.append("preexisting"), True)[1]
        self.assert_invalid("project_exists", exists, up=False)
        self.assert_invalid("image_id_mismatch", lambda p: setattr(p, "image_ids", lambda: {"product": "x"}), up=False)
        self.assert_invalid("subnet_overlap", lambda p: setattr(p, "host_subnets", lambda: ["10.252.3.0/24"]), up=False)

    def test_probe_failures(self):
        self.assert_invalid("pre_probe_violation", lambda p: p.probe_status.update(pre=VIOLATION))
        _, ports, _ = self.assert_invalid("post_probe_invalid", lambda p: p.probe_status.update(post=INVALID))
        self.assertIn("watch:stop", ports.calls)  # 감시는 정리한다

    def test_product_calls_are_verified_like_the_gateway(self):
        # 제품이 실제로 보낸 샘플링(실행 기록)과 복사한 호출 기록을 넘기고, 표식은 B와 같은 규칙으로 판정한다(4-11).
        result, ports, evidence = run()
        self.assertEqual(ports.verify_args, [(CALLS, dict(SAMPLING))])
        self.assertEqual((evidence.files["llm-summary.json"]["reasons"], result["status"]), ([], "complete"))
        self.assert_invalid("llm_unverified", lambda p: setattr(p, "marker", None))
        _, _, evidence = self.assert_invalid("llm_sampling_mismatch",
                                             lambda p: setattr(p, "marker", product_marker(sampling=dict(SAMPLING, seed=7))))
        self.assertEqual(evidence.files["llm-summary.json"]["reasons"], ["llm_sampling_mismatch"])
        self.assert_invalid("llm_provider_mismatch", lambda p: setattr(p, "marker", product_marker(generations=[
            {"id": "g", "status": "verified", "provider_name": "Other", "attempts": [{"provider_name": "Other", "endpoint_id": "e",
                                                                                    "status": 200}],
             "lookup": {"tries": 1, "last": "verified"}}])))
        # 게이트웨이 표식을 O 자리에 넣지 못한다
        # (모양이 온전한 게이트웨이 표식이어도 역할이 다르면 거부한다: 거부 두 칸이 0인 B 표식)
        self.assert_invalid("llm_marker_invalid", lambda p: setattr(p, "marker", product_marker(
            role="llm-gateway", rejected_sampling=0, rejected_provider=0)))

    def test_container_replaced_between_pre_and_post(self):
        # #668 리뷰: 같은 이미지·주소의 새 컨테이너로 바뀌면 배치 검사는 통과하지만 B와 같은 스냅숏 대조가 잡는다.
        _, ports, _ = self.assert_invalid("boundary_changed", lambda p: p.snapshot_change.update(post={"target": {"id": "9" * 64}}))
        self.assertNotIn("check:post", ports.calls)  # 대조가 배치 검사보다 먼저다

    def test_layout_violations(self):
        # #668 리뷰: 구간 이전부터 있던 배치 어긋남(직접 publish·netns 공유)은 이벤트로 안 잡히므로 pre/post 검사가 무효로 만든다.
        _, _, evidence = self.assert_invalid("pre_layout_violation",
                                             lambda p: p.layout.update(pre=["service.target: PUBLISH_MISMATCH"]))
        self.assertEqual(evidence.files["pre-checks.json"]["violations"], ["service.target: PUBLISH_MISMATCH"])
        _, ports, _ = self.assert_invalid("post_layout_violation",
                                          lambda p: p.layout.update(post=["host.container.ffffffffffff: SHARES_NETWORK_NAMESPACE"]))
        self.assertIn("watch:stop", ports.calls)

    def test_window_violation(self):
        event = {"Type": "container", "Action": "exec_create: /bin/sh -c id", "Actor": {"ID": IDS["preparation"],
                 "Attributes": {"execID": "x"}}, "timeNano": int((START + timedelta(seconds=5)).timestamp() * 1e9)}
        self.assert_invalid("window_boundary_changed", lambda p: setattr(p, "events_raw", [event]))

    def test_window_allows_declared_healthcheck_and_boundary_stop(self):
        at = int((START + timedelta(seconds=5)).timestamp() * 1e9)
        events = [{"Type": "container", "Action": "exec_create: " + HEALTH["preparation"],
                   "Actor": {"ID": IDS["preparation"], "Attributes": {"execID": "h1"}}, "timeNano": at},
                  {"Type": "container", "Action": "kill", "Actor": {"ID": IDS["explore-boundary"], "Attributes": {"signal": "15"}},
                   "timeNano": at + 1},
                  {"Type": "container", "Action": "kill", "Actor": {"ID": IDS["eval-meter"], "Attributes": {"signal": "10"}},
                   "timeNano": at + 2}]
        result, _, _ = run(lambda p: setattr(p, "events_raw", events))
        self.assertEqual(result["status"], "complete", result)

    def test_probe_containers_are_allowed_only_by_their_ids(self):
        at = int((START + timedelta(seconds=5)).timestamp() * 1e9)
        lifecycle = [{"Type": "container", "Action": action, "Actor": {"ID": cid * 64, "Attributes": {}}, "timeNano": at + i}
                     for i, (cid, action) in enumerate((("2", "create"), ("2", "start"), ("2", "die"), ("2", "destroy")))]
        result, _, _ = run(lambda p: setattr(p, "events_raw", lifecycle))
        self.assertEqual(result["status"], "complete", result)
        foreign = [dict(lifecycle[0], Actor={"ID": "9" * 64, "Attributes": {}})]
        self.assert_invalid("window_boundary_changed", lambda p: setattr(p, "events_raw", foreign))

    def test_requests_before_the_task_are_invalid(self):
        self.assert_invalid("pre_requests_nonzero", lambda p: setattr(p, "forwarded", 3))

    def test_goal_missing_and_record_problems(self):
        self.assert_invalid("goal_digest_missing", lambda p: p.record.pop("goal"))
        self.assert_invalid("product_model_usage_invalid", lambda p: p.record["model"].pop("sampling_overwrites"))
        self.assert_invalid("llm_call_records_incomplete", lambda p: setattr(p, "calls_file", b'{"seq":1}\n'))
        self.assert_invalid("product_ledger_invalid", lambda p: setattr(p, "ledger", b"bad\n"))

    def test_port_failures_have_step_codes(self):
        for name, code in (("up", "compose_up_failed"), ("dns_token", "dns_token_failed"), ("check:pre", "pre_checks_failed"),
                           ("check:post", "post_checks_failed"), ("prepare_round", "product_prepare_failed"),
                           ("start_round", "product_start_failed"), ("seal", "seal_failed"), ("stop_boundary", "boundary_stop_failed"),
                           ("stop_round", "product_stop_failed"), ("wait_closed", "product_close_failed"),
                           ("run_record", "product_run_record_failed"), ("copy_boundary_evidence", "boundary_evidence_copy_failed"),
                           ("copy_product_records", "product_records_copy_failed"), ("generation_verify", "llm_verify_failed"),
                           ("llm_verified", "llm_read_failed")):
            with self.subTest(port=name):
                result, ports, _ = self.assert_invalid(code, lambda p, n=name: p.fail.update({n: RuntimeError("secret value")}))
                self.assertNotIn("secret", str(result))

    def test_operator_interrupt_still_writes_one_result_and_reraises(self):
        for port in ("start_round", "wait_closed"):
            with self.subTest(port=port):
                clock = FakeClockState()
                ports = FakePorts(clock)
                ports.fail[port] = KeyboardInterrupt()
                evidence = FakeEvidence()
                with self.assertRaises(KeyboardInterrupt):
                    execute_split_run(spec(), ports, FakeMeter(ports), evidence, clock.clock())
                self.assertEqual((evidence.files[RESULT_NAME]["status"], evidence.files[RESULT_NAME]["reasons"]), (INVALID, ["interrupted"]))
                self.assertEqual(ports.calls[-2:], ["down", "remove_inputs"])
        # 복사 도우미 정리까지 실패한 중단은 그 사유도 따로 남는다(#693 리뷰)
        from runner_docker import HelperCleanupInterrupt
        clock = FakeClockState()
        ports = FakePorts(clock)
        ports.fail["product_files"] = HelperCleanupInterrupt()
        evidence = FakeEvidence()
        with self.assertRaises(KeyboardInterrupt):
            execute_split_run(spec(), ports, FakeMeter(ports), evidence, clock.clock())
        self.assertEqual(evidence.files[RESULT_NAME]["reasons"], ["interrupted", "docker_helper_cleanup_failed"])

    def test_helper_cleanup_failure_is_kept_beside_the_step_code(self):
        # #693 리뷰: 복사 시간 초과 뒤 정리도 실패하면 단계 사유(product_files_failed)만 남고 정리 실패가 사라졌다.
        from runner_docker import HelperCleanupError
        for port, step in (("product_files", "product_files_failed"), ("runtime_conditions", "runtime_conditions_failed")):
            def broken(ports, port=port):
                ports.fail[port] = HelperCleanupError()
            result, ports, _ = run(broken)
            with self.subTest(port=port):
                self.assertEqual((result["status"], result["reasons"]), ("invalid", [step, "docker_helper_cleanup_failed"]))
                self.assertIn("down", ports.calls)
        # 다른 Docker 오류는 단계 사유 하나뿐이다
        from runner_docker import DockerError
        result, _, _ = run(lambda ports: ports.fail.update(product_files=DockerError("docker_helper_copy_failed")))
        self.assertEqual(result["reasons"], ["product_files_failed"])

    def test_cleanup_failures_are_reported(self):
        result, _, _ = run(lambda p: p.fail.update(down=RuntimeError("x")))
        self.assertEqual((result["status"], result["reasons"]), (INVALID, ["down_failed"]))
        result, _, _ = run(lambda p: p.fail.update(remove_inputs=RuntimeError("x")))
        self.assertEqual((result["status"], result["reasons"]), (INVALID, ["inputs_cleanup_failed"]))


class TestMeterLogParity(unittest.TestCase):
    """2026-11-18 결정 (a) 조건 3: 계측기 기록 실패는 B·O에 같은 사유로 걸린다(같은 check_meter_logs·_wait_drained)."""

    def test_same_meter_failure_invalidates_b_and_o_alike(self):
        import dataclasses as dc
        import test_runner as b
        from runner_evidence import EvidenceError
        b_spec = dc.replace(b.make_spec(), split=True, path_key_ref=b.PATH_KEY_REF)
        cases = {
            "meter_path_log_missing": lambda meter: setattr(meter, "path_log", False),
            "drain_incomplete": lambda meter: (setattr(meter, "log_complete", False) if hasattr(meter, "log_complete")
                                               else meter.drained.update(log_complete=False)),
            "meter_path_log_invalid": lambda meter: setattr(meter, "path_rows_error", EvidenceError("meter_path_log_invalid")),
            "meter_path_key_mismatch": lambda meter: setattr(meter, "path_key_ref", "b" * 16),
            "meter_log_inconsistent": lambda meter: setattr(meter, "sealed", None) if hasattr(meter, "sealed") else None,
        }
        for reason, broken in cases.items():
            def b_mutate(ports, meter, clock, broken=broken, reason=reason):
                meter.path_log = True
                if reason == "meter_log_inconsistent":
                    original = meter.marker
                    meter.marker = lambda name: None if name == "engine-sealed.json" else original(name)
                else:
                    broken(meter)
            b_result, _, _, _ = b.run(b_mutate, spec=b_spec)
            clock = FakeClockState()
            ports = FakePorts(clock)
            original_start = ports.start_round

            def start(prepared, ports=ports, original_start=original_start):
                out = original_start(prepared)
                ports.forwarded = 3  # 시작 뒤 대상에 전달한 요청(경로 행이 생긴다)
                return out
            ports.start_round = start
            meter = FakeMeter(ports)
            broken(meter)
            o_result = execute_split_run(spec(), ports, meter, FakeEvidence(), clock.clock())
            with self.subTest(reason=reason):
                self.assertEqual(b_result["reasons"], [reason])
                self.assertEqual(o_result["reasons"], [reason])
        # 정상이면 둘 다 완료
        self.assertEqual(b.run(lambda p, m, c: setattr(m, "path_log", True), spec=b_spec)[0]["status"], "complete")
        self.assertEqual(run()[0]["status"], "complete")



class TestPreservationV3(unittest.TestCase):
    """O 보존 sidecar v3 발행(결정안 §3.3): 칸을 모아 내린 뒤 빌드·서명한다. 공통 칸은 B와 같은 정의다."""

    def v3_spec(self, **changes):
        from test_preservation_record import options
        values = dict(preservation=options(), purpose="official", model_tag="example-org/example-model",
                      product_source_sha="b" * 40, drain_grace_seconds=5, host_watch_interval_seconds=2,
                      driver_settings={"task_wait_seconds": 120, "poll_interval_seconds": 2, "registration_valid_hours": 4},
                      model_budget_sha256="9" * 64, exploration_policy_sha256="8" * 64, repeat_index=1)
        values.update(changes)
        return values

    def run_v3(self, mutate=None, **changes):
        import runner_split_run as module
        seen = {}

        def capture(**kwargs):
            seen.update(kwargs)
            return {"format": "test-unsigned-v3"}
        with mock.patch.object(module, "preservation_payload_v3", side_effect=capture):
            result, ports, evidence = run(mutate, **self.v3_spec(**changes))
        return result, ports, evidence, seen

    def test_signed_sidecar_write_failure_is_a_fixed_reason(self):
        # #710 리뷰: 서명 파일 저장이 실패해도(이미 있음·쓰기 실패) 예외가 빠져나가지 않고 고정 사유와 결과 기록을 남긴다.
        from preservation_record import NAME
        from runner_evidence import EvidenceError
        from test_runner import FakeEvidence
        original = FakeEvidence.write_json

        def failing(self, name, payload):
            if name == NAME:
                raise EvidenceError("evidence_exists")
            return original(self, name, payload)
        with mock.patch.object(FakeEvidence, "write_json", failing):
            result, ports, evidence, _ = self.run_v3()
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["preservation_write_failed"]))
        self.assertEqual(evidence.files[RESULT_NAME]["reasons"], ["preservation_write_failed"])  # 결과 기록은 남는다
        self.assertNotIn(NAME, evidence.files)
        self.assertIn("sign", ports.calls)

    def test_parts_pass_the_v3_shape_and_are_signed_after_down(self):
        import preservation_v3_fields as v3f
        from preservation_record import FORMAT_V3, NAME, _validate_v3
        result, ports, evidence, seen = self.run_v3()
        self.assertEqual(result["status"], "complete", result)
        self.assertLess(ports.calls.index("down"), ports.calls.index("sign"))
        self.assertEqual(evidence.files[NAME], {"format": "test-unsigned-v3", "signature": "s"})
        self.assertEqual(seen["boundary"], {"directory": Path("/runs/ours-1/boundary"), "task_id": "42", "path_key_ref": PATH_KEY_REF})
        self.assertEqual((seen["side"], seen["repeat_index"], seen["purpose"], seen["engine_image_id"]),
                         ("ours", 1, "official", IMAGES["product"]))
        common, side, run_block, product = seen["common"], seen["side_conditions"], seen["run"], seen["product"]
        self.assertEqual(set(common), set(__import__("preservation_record").COMMON_FIELDS_V3))
        self.assertEqual(common["model_pin_sha256"], v3f.model_pin_sha256(
            backend="api", model="example-org/example-model", provider_slug=LLM_SLUG, provider_name=LLM_NAME,
            sampling=SAMPLING, serving=None))  # B와 같은 정의(게이트웨이 대신 제품이 실제로 보낸 샘플링)
        self.assertEqual((side["layout"], side["browser_image_id"], side["diagnosis_policy_sha256"], side["exploration_policy_sha256"]),
                         ("exploration-split/v1", IMAGES["explore_browser"], FINGERPRINT, "8" * 64))
        self.assertEqual(run_block["end_signal"]["closing_reason"] if run_block["end_signal"] else None, None)
        self.assertEqual(product["call_records"], {"reservations": 2, "lines": 2})
        self.assertEqual(set(product["image_ids"]), {"product", "explore_browser", "fixture"})
        payload = {"format": FORMAT_V3, **{k: seen[k] for k in ("purpose", "series_ref", "run_id", "side", "repeat_index",
                   "engine_source_sha", "engine_image_id", "execution_source", "common", "side_conditions", "run", "product")},
                   "files": {name: {"sha256": "0" * 64, "bytes": 0} for name in ("engine-requests.jsonl", "engine-paths.jsonl",
                                                                              "engine-sealed.json", "engine-drained.json")},
                   "forwarded_by_ingress": {"main": 0, "explore": 0},
                   "exploration_boundary": {"status": "complete"}}
        payload["common"] = dict(common, initial_state_sha256="5" * 64)
        try:
            _validate_v3(payload)
        except Exception as exc:  # 경계 블록 모양은 빌더가 채운다. 칸 모양 오류만 실패로 본다
            self.assertNotIn(str(exc), {"preservation_conditions_invalid", "preservation_side_invalid", "preservation_run_invalid",
                                        "preservation_product_invalid", "preservation_record_invalid"}, str(exc))

    def test_synthetic_run_skips_llm_verification_and_marks_it(self):
        # 합성 모델 모드(2026-11-18 사용자 결정 (a)): OpenRouter 장부·호출 기록 대조와 사후 검증을 부르지 않고 '해당 없음(합성)'
        # 으로 남긴다. 실행 출처는 synthetic_fixture다. 실제 모델 회차는 그대로 검증한다(대조: 같은 흐름에서 호출이 있다).
        import preservation_v3_fields as v3f
        from preservation_record import LLM_NOT_APPLICABLE, LLM_VERIFIED
        result, ports, evidence, seen = self.run_v3(synthetic=True, scripted_plan_sha256="3" * 64)
        self.assertEqual(result["status"], "complete", result)
        for name in ("copy_product_records", "generation_verify", "llm_verified"):
            self.assertNotIn(name, ports.calls)
        self.assertEqual((result["execution_source"], result["llm_verification"], result["scripted_plan_sha256"]),
                         ("synthetic_fixture", LLM_NOT_APPLICABLE, "3" * 64))
        self.assertEqual(evidence.files["llm-summary.json"]["verification"], LLM_NOT_APPLICABLE)
        self.assertIsNone(evidence.files["product-model.json"]["call_records"])
        product = seen["product"]
        self.assertEqual((product["call_records"], product["llm_verification"]), (None, LLM_NOT_APPLICABLE))
        self.assertIsNone(seen["run"]["rate_limited"])
        self.assertEqual(seen["common"]["model_pin_sha256"], v3f.model_pin_sha256(
            backend="local", model="example-org/example-model", provider_slug=LLM_SLUG, provider_name=LLM_NAME,
            sampling=SAMPLING, serving=None))
        real, real_ports, _, real_seen = self.run_v3()
        self.assertEqual((real["execution_source"], real["llm_verification"], real["scripted_plan_sha256"]),
                         ("model_run", LLM_VERIFIED, None))
        self.assertIn("generation_verify", real_ports.calls)
        self.assertEqual(real_seen["product"]["llm_verification"], LLM_VERIFIED)

    def test_runner_settings_digest_matches_the_b_definition(self):
        # 공통 러너 설정 칸은 B와 같은 이름·같은 값이면 같은 지문이다(짝 대조, 결정안 §3.3).
        import preservation_v3_fields as v3f
        _, _, _, seen = self.run_v3()
        values = {name: getattr(spec(**self.v3_spec()), name) for name in v3f.COMMON_RUNNER_SETTINGS}
        self.assertEqual(seen["common"]["runner_settings_sha256"], v3f.settings_sha256(values, v3f.COMMON_RUNNER_SETTINGS))

    def test_product_end_signal_keeps_the_closing_reason(self):
        # O 끝 신호는 제품 초안 끝 상태다(B는 closing_reason null). 예산 마감 사유가 회차별 칸에 남는다.
        def closing(ports):
            ports.statuses = [{"id": "draft-1", "state": "running"},
                              {"id": "draft-1", "state": "completed", "closing_reason": "budget_limited", "outcome": "partial"}]
        _, _, _, seen = self.run_v3(closing)
        self.assertEqual(seen["run"]["end_signal"]["closing_reason"], "budget_limited")

    def test_round_without_diagnosis_publishes_null_run_conditions(self):
        def not_entered(ports):
            ports.record["run"].pop("execution")
        result, _, _, seen = self.run_v3(not_entered)
        self.assertEqual(result["status"], "complete", result)
        self.assertIsNone(seen["product"]["file_sha256"]["runtime_conditions"])

    def test_build_and_sign_failures_invalidate(self):
        import runner_split_run as module
        with mock.patch.object(module, "preservation_payload_v3", side_effect=ValueError("x")):
            result, _, _ = run(None, **self.v3_spec())
        self.assertEqual(result["reasons"], ["preservation_record_failed"])

        def broken_sign(ports):
            ports.fail["sign"] = OSError("key")
        result, _, evidence, _ = self.run_v3(broken_sign)
        self.assertEqual(result["reasons"], ["preservation_sign_failed"])
        from preservation_record import NAME
        self.assertNotIn(NAME, evidence.files)
        # 보존 옵션이 없으면 발행하지 않는다(서명은 러너 기록·결과 둘뿐)
        plain, ports, evidence = run()
        self.assertEqual(ports.calls.count("sign"), 2)
        self.assertNotIn(NAME, evidence.files)



class TestDiscoverySources(unittest.TestCase):
    """O 끝 워크플로 snapshot의 발견 출처별 건수(고정 초안 §8). 기록만 하고 회차 유효성에 쓰지 않는다."""

    def test_counts_by_source_and_groups(self):
        from runner_split_run import discovery_sources
        snapshot = {"endpoints": [{"source": "browser_xhr"}, {"source": "browser_form"}, {"source": "api_recon_static"},
                                  {"source": "bundle_string", "path": "/secret-looking"}, {"source": "Bad Source"}, {}, "x"],
                    "counts": {"omitted": 3}, "detail_truncated": True}
        got = discovery_sources(snapshot)
        self.assertEqual(got, {"format": "abc2lab-eval-runner-discovery-sources-v1", "endpoints": 7,
                               "by_source": {"api_recon_static": 1, "browser_form": 1, "browser_xhr": 1, "bundle_string": 1,
                                             "invalid": 3},
                               "browser": 2, "api_recon": 1, "omitted": 3, "detail_truncated": True})
        self.assertNotIn("secret-looking", json.dumps(got))  # 경로는 남기지 않는다
        # counts.omitted는 omitempty라 칸이 없으면 0, 정수가 아니거나 음수거나 counts가 객체가 아니면 모름(null)
        for counts, want in (({}, 0), ({"pages": 3}, 0), ({"omitted": 0}, 0), ({"omitted": 5}, 5), ({"omitted": -1}, None),
                             ({"omitted": "3"}, None), ({"omitted": True}, None), ({"omitted": None}, None)):
            with self.subTest(counts=counts):
                self.assertEqual(discovery_sources({"endpoints": [], "counts": counts})["omitted"], want)
        self.assertIsNone(discovery_sources({"endpoints": []})["omitted"])
        self.assertIsNone(discovery_sources({"endpoints": [], "counts": []})["omitted"])

    def test_read_failure_is_recorded_not_fatal(self):
        result, ports, evidence = run(lambda p: setattr(p, "workflow", ProductAPIError("product_workflow_invalid")))
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual(evidence.files["discovery-sources.json"],
                         {"format": "abc2lab-eval-runner-discovery-sources-v1", "error": "product_workflow_invalid"})

    def test_failure_records_only_a_fixed_code(self):
        # 구동기 오류 글에는 경로·초안 ID·제품 사유가 붙는다(예: HTTP 503). 근거·요약에는 앞부분 고정 코드만 남는다(#725 리뷰).
        for message, code in (("product_http_503:/api/product/workflows/draft-1", "product_http_503"),
                              ("product_http_409:/api/product/workflows/draft-1:workflow_busy", "product_http_409"),
                              ("product_draft_mismatch", "product_draft_mismatch"),
                              ("draft-1 leaked", "discovery_sources_failed"), ("", "discovery_sources_failed")):
            with self.subTest(message=message):
                result, _, evidence = run(lambda p, m=message: setattr(p, "workflow", ProductAPIError(m)))
                recorded = evidence.files["discovery-sources.json"]
                self.assertEqual(recorded, {"format": "abc2lab-eval-runner-discovery-sources-v1", "error": code})
                self.assertNotIn("draft-1", json.dumps(recorded) + json.dumps(result["discovery_sources"]))


class TestE2V3Collection(unittest.TestCase):
    """E② v3 저널 수집(설정이 있을 때만). 미수집은 회차 유효·통제 대조만 미수집, 러너 쪽 장애는 무효(고정 초안 §20)."""

    def test_uncollected_keeps_the_run_and_is_recorded_before_down(self):
        result, ports, evidence = run(e2_v3=True)
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual(evidence.files["e2-v3-collection.json"]["outcome"], "uncollected")
        self.assertLess(ports.calls.index("e2_v3_export"), ports.calls.index("e2_v3_collect"))
        self.assertLess(ports.calls.index("e2_v3_collect"), ports.calls.index("down"))

    def test_off_by_default(self):
        result, ports, evidence = run()
        self.assertEqual(result["status"], "complete", result)
        self.assertNotIn("e2_v3_export", ports.calls)
        self.assertNotIn("e2-v3-collection.json", evidence.files)

    def test_order_after_task_end_and_cli_usage_before_down(self):
        # #785 재상 봇: #780·#782와 같은 자리라 내리기 전 순서를 하나로 고정한다(작업 끝 신호 → CLI 관측 → v3 수집 → 호출 기록).
        result, ports, _ = run(e2_v3=True, cli_usage_names=("python",))
        self.assertEqual(result["status"], "complete", result)
        order = [ports.calls.index(name) for name in ("run_record", "task_signal", "product_commands", "e2_v3_export",
                                                        "e2_v3_collect", "copy_product_records", "down")]
        self.assertEqual(order, sorted(order))

    def test_export_failure_invalidates(self):
        from runner_docker import DockerError
        result, ports, evidence = run(lambda p: setattr(p, "e2_v3_files", DockerError("docker_timeout")), e2_v3=True)
        self.assertEqual((result["status"], result["reasons"][0]), ("invalid", "e2_v3_export_failed"), result)
        self.assertNotIn("e2_v3_collect", ports.calls)
        self.assertIn("remove_inputs", ports.calls)

    def test_runner_side_failure_invalidates_with_its_code(self):
        from runner_e2v3 import E2V3Invalid
        for error, extra in ((E2V3Invalid("e2_v3_verifier_failed"), ["e2_v3_verifier_failed"]), (RuntimeError("x"), [])):
            with self.subTest(error=str(error)):
                result, ports, evidence = run(lambda p, e=error: setattr(p, "e2_v3_row", e), e2_v3=True)
                self.assertEqual((result["status"], result["reasons"]), ("invalid", ["e2_v3_collect_failed", *extra]), result)
                self.assertIn("down", ports.calls)
                self.assertIn("remove_inputs", ports.calls)
                self.assertNotIn("e2-v3-collection.json", evidence.files)
class TestProductTaskEnd(unittest.TestCase):
    """마감 뒤 제품 작업의 끝 신호 칸(목표 수·달성 수 등). 제품 회차가 왜 마감했는지 가르는 근거라 판정에 쓰지 않는다."""

    def test_task_end_signal_is_kept_after_the_run_record(self):
        result, ports, evidence = run()
        self.assertEqual(result["status"], "complete", result)
        record = evidence.files["product-task-end.json"]
        self.assertEqual((record["format"], record["task_id"], record["task"]["goals_total"], record["task"]["goals_met"]),
                         ("abc2lab-eval-runner-product-task-end-v2", "42", 3, 3))
        self.assertLess(ports.calls.index("run_record"), ports.calls.index("task_signal"))  # 마감 뒤 실행 기록 다음
        self.assertLess(ports.calls.index("task_signal"), ports.calls.index("down"))  # 내리기 전

    def test_read_failure_leaves_a_fixed_code_and_keeps_the_run(self):
        from runner_engine import EngineAPIError
        from runner_product import ProductAPIError
        for error, code in ((ProductAPIError("product_http_403:tasks"), "product_http_403:tasks"),
                            (ValueError("product_task_invalid"), "product_task_invalid"),
                            (EngineAPIError("engine_task_status_missing"), "engine_task_status_missing"),
                            (ProductAPIError("/secret/path value"), "product_task_signal_failed"),
                            (RuntimeError("boom"), "product_task_signal_failed")):
            with self.subTest(code=code):
                result, _, evidence = run(lambda p, e=error: setattr(p, "task_end", e))
                self.assertEqual(result["status"], "complete", result)
                self.assertEqual(evidence.files["product-task-end.json"]["error"], code)
                self.assertNotIn("task", evidence.files["product-task-end.json"])


class TestProductCliUsage(unittest.TestCase):
    """O 제품 활동의 Bash 호출을 B와 같은 함수로 센다(B 전용 CLI 관측·O에서 이름을 찾지 못한 호출)."""

    def test_names_and_not_found_are_counted_after_the_task_signal(self):
        result, ports, evidence = run(cli_usage_names=("python", "pip"))
        self.assertEqual(result["status"], "complete", result)
        usage = evidence.files["cli-usage.json"]
        self.assertEqual((usage["bash_calls"], usage["names_seen"], usage["not_found"], usage["not_found_calls"],
                          usage["exploration_id"], usage["observed_lines"]), (2, ["python"], {"python": 1}, 1, 7, 1))
        lines = evidence.files["cli-observed-lines.json"]
        self.assertEqual([line["shape"] for line in lines["lines"]], ["python <인자> <인자>"])  # 원문 인자는 남지 않는다
        self.assertNotIn("print", json.dumps(evidence.files["cli-observed-lines.json"]))
        order = ports.calls
        self.assertLess(order.index("task_signal"), order.index("task_exploration_id"))
        self.assertLess(order.index("product_commands"), order.index("down"))

    def test_no_names_means_no_read(self):
        result, ports, evidence = run()
        self.assertEqual(result["status"], "complete", result)
        self.assertNotIn("product_commands", ports.calls)
        self.assertNotIn("cli-usage.json", evidence.files)

    def test_no_observation_writes_no_lines(self):
        result, _, evidence = run(lambda p: setattr(p, "commands", p.commands[1:]), cli_usage_names=("python",))
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual((evidence.files["cli-usage.json"]["names_seen"], evidence.files["cli-usage.json"]["not_found_calls"]), ([], 0))
        self.assertNotIn("cli-observed-lines.json", evidence.files)

    def test_read_failure_invalidates_like_b(self):
        for bad in (RuntimeError("boom"), ValueError("product_commands_invalid")):
            with self.subTest(bad=str(bad)):
                result, ports, evidence = run(lambda p, b=bad: setattr(p, "commands", b), cli_usage_names=("python",))
                self.assertEqual((result["status"], result["reasons"][0]), ("invalid", "cli_usage_failed"), result)
                self.assertIn("down", ports.calls)
                self.assertNotIn("cli-usage.json", evidence.files)


class TestProductFaultStall(unittest.TestCase):
    """O 멈춤 집계(결정안 §7): 제품 호출 기록 줄을 B 게이트웨이 행 자리에 넣고 B와 같은 함수·창으로 센다."""

    def calls(self, *rows) -> bytes:
        return b"".join(json.dumps(row).encode() + b"\n" for row in rows)

    def test_faults_and_gaps_come_from_product_calls_and_meter_rows(self):
        from runner_split_run import product_fault_stall
        t = lambda second: f"2026-11-18T10:00:{second:02d}Z"  # noqa: E731
        calls = self.calls(
            {"seq": 1, "decision": "forwarded", "status": 200, "started_at": t(1), "completed_at": t(2)},
            {"seq": 2, "decision": "forwarded", "status": 429, "started_at": t(3), "completed_at": t(4)},
            {"seq": 3, "decision": "upstream_error", "status": 200, "unfinished_end": "upstream_cut_before_done",
             "started_at": t(5), "completed_at": t(6)})
        meter_rows = [{"started_at": t(20), "completed_at": t(21)}]
        record = product_fault_stall(calls, meter_rows, t(0), t(40), {"first_terminal": None})
        self.assertEqual((record["faults"], record["activity_starts"], record["judged"]),
                         ({"upstream_cut": 1, "rate_limited": 1}, 4, False))
        # 429(4초) 뒤 첫 활동은 5초 호출, 끊김(6초) 뒤 첫 활동은 20초 대상 요청: 무활동 14초
        self.assertEqual([(e["kind"], e["silence_seconds"], e["censored"]) for e in record["events"]],
                         [("rate_limited", 1.0, False), ("upstream_cut", 14.0, False)])
        self.assertEqual(record["max_idle_gap_seconds"], 19.0)  # 21초~40초(봉인)

    def stall_calls(self, ports):
        """시작 직후 위쪽 끊김, 그 뒤 봉인까지 활동 없음(B 1차 멈춤 모양)인 제품 호출 기록 두 줄(장부 두 줄과 맞음)."""
        start_round = ports.start_round

        def start(prepared):
            base = START + timedelta(seconds=ports.clock.t)
            at = lambda seconds: (base + timedelta(seconds=seconds)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")  # noqa: E731
            ports.calls_file = self.calls(
                {"seq": 1, "decision": "forwarded", "status": 200, "started_at": at(0.5), "completed_at": at(1.0)},
                {"seq": 2, "decision": "upstream_error", "status": 200, "unfinished_end": "upstream_cut_before_done",
                 "started_at": at(1.5), "completed_at": at(2.0)})
            return start_round(prepared)
        ports.start_round = start

    def test_configured_t_judges_o_like_b(self):
        # B와 같은 T·같은 판정: 멈춤이면 provider_fault_stall이 첫 사유, T에 못 미치면 판정 기록과 함께 완료
        result, _, evidence = run(self.stall_calls, fault_stall_seconds=1)
        self.assertEqual((result["status"], result["reasons"][0]), ("invalid", "provider_fault_stall"))
        record = evidence.files["fault-stall.json"]
        self.assertEqual((record["judged"], record["stall_seconds"], record["stalls"]), (True, 1, 1))
        result, _, evidence = run(self.stall_calls, fault_stall_seconds=3600)
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual((result["fault_stall"]["judged"], result["fault_stall"]["stalls"]), (True, 0))
        # T가 있는데 판정 재료가 없으면(가짜 기본 기록은 시각 칸이 없음) 멈춤 없음으로 두지 않는다
        result, _, evidence = run(fault_stall_seconds=180)
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["fault_stall_unjudgeable"]))
        self.assertEqual(evidence.files["fault-stall.json"]["stall_seconds"], 180)

    def goal_met_round(self, ports, *, late_end=None):
        """제품이 진행 중 → 목표 달성 마감(#894 이후 completed, intent_outcomes.goal_met, 정리 끝)으로 가는 O 회차. 호출 기록 두 줄은
        시작 직후 정상 호출이라 멈춤이 없다. late_end를 주면 두 번째 호출이 그 초(시작 기준)에 끝난다(유예 종료 뒤에 끝난 호출)."""
        start_round = ports.start_round

        def start(prepared):
            base = START + timedelta(seconds=ports.clock.t)
            at = lambda seconds: (base + timedelta(seconds=seconds)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")  # noqa: E731
            ports.calls_file = self.calls(
                {"seq": 1, "decision": "forwarded", "status": 200, "started_at": at(0.5), "completed_at": at(1.0)},
                {"seq": 2, "decision": "forwarded", "status": 200, "started_at": at(1.5),
                 "completed_at": at(2.0 if late_end is None else late_end)})
            return start_round(prepared)
        ports.start_round = start
        done = {"id": "draft-1", "state": "completed", "phase": "report", "outcome": "complete", "closing_reason": "complete",
                "reason_code": None, "drain_complete": True, "result_ready": True,
                "intent_outcomes": {"done": 1, "policy_limited": 0, "incomplete": 0, "unknown": 0, "stopped": 0, "goal_met": 2}}
        ports.statuses = [{"id": "draft-1", "state": "running", "phase": "execution"}] * 3 + [done]

    def test_goal_met_o_ends_by_the_end_idle_grace(self):
        # 사용자 2026-11-25: 목표 달성으로 끝난 O(#894 이후 completed)도 끝 신호 뒤 무활동 유예로 끝난다.
        result, ports, evidence = run(self.goal_met_round, fault_stall_seconds=5)
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual(result["stop_reason"], "engine_completed")
        self.assertEqual((result["first_limit"]["value"], result["first_limit"]["runner_limit"]),
                         ("engine_completed", "runner_engine_idle"))
        idle = evidence.files["end-idle.json"]
        self.assertEqual((idle["stopped"], idle["drain_required"], idle["model_calls_watched"], idle["inflight_at_stop"]),
                         (True, True, False, 0))
        self.assertEqual(round(idle["stopped_elapsed_seconds"] - idle["last_activity_elapsed_seconds"], 3), 5.0)
        self.assertLessEqual(idle["end_signal_elapsed_seconds"], idle["last_activity_elapsed_seconds"])

    def test_o_call_still_running_at_the_grace_stop_invalidates(self):
        result, _, evidence = run(lambda ports: self.goal_met_round(ports, late_end=3600), fault_stall_seconds=5)
        self.assertEqual((result["status"], result["reasons"][0]), ("invalid", "end_idle_inflight_model_call"))
        self.assertEqual(evidence.files["end-idle.json"]["inflight_at_stop"], 1)

    def test_o_stall_is_the_first_reason_with_llm_reasons_after(self):
        def both(ports):
            self.stall_calls(ports)
            ports.marker = dict(ports.marker, log_complete=False)
        result, _, _ = run(both, fault_stall_seconds=1)
        self.assertEqual(result["reasons"][0], "provider_fault_stall")
        self.assertGreater(len(result["reasons"]), 1)

    def test_unreadable_records_leave_a_fixed_code(self):
        from runner_split_run import product_fault_stall
        for calls in (b"\xff\n", b"not json\n", b"[1]\n", b'{"seq":1}\n'):
            with self.subTest(calls=calls):
                record = product_fault_stall(calls, [], "2026-11-18T10:00:00Z", "2026-11-18T10:00:10Z", {})
                self.assertEqual(record, {"format": "abc2lab-eval-runner-fault-stall-v3", "judged": False,
                                          "error": "fault_stall_rows_invalid"})
        record = product_fault_stall(b"", [], "2026-11-18T10:00:10Z", "2026-11-18T10:00:00Z", {})
        self.assertEqual(record["error"], "fault_stall_time_invalid")  # 창이 거꾸로면 B와 같은 코드



class TestE3ConfirmationRow(unittest.TestCase):
    """O 회차의 E③ 확정 단계 결과(runs/<초안>/e3-confirmation.json) 상태·사유 행(2026-11-19 사용자 결정)."""

    def result(self, **fields):
        base = {"version": "e3-confirmation-run/v1", "status": "disabled", "reason": "declaration_missing", "verdicts": [],
                "skipped": [], "candidates": [], "requests": 0, "writes": []}
        return json.dumps(dict(base, **fields)).encode()

    def test_rows_and_shape(self):
        from runner_split_run import e3_confirmation_row
        import hashlib
        raw = self.result()
        self.assertEqual(e3_confirmation_row(raw), {"status": "disabled", "reason": "declaration_missing", "requests": 0,
                                                    "sha256": hashlib.sha256(raw).hexdigest()})
        self.assertEqual(e3_confirmation_row(self.result(status="completed", reason=None, requests=7))["requests"], 7)
        self.assertEqual(e3_confirmation_row(self.result(status="completed", reason=None))["reason"], None)
        # 비활성의 suspected 후보는 받는다(후보는 판정 없이 SUSPECTED로 남는다). confirmed 후보는 판정이 있는 결과에서만.
        suspected = self.result(candidates=[{"candidate_ref": "1", "level": "suspected", "reason": "link_none"}])
        self.assertEqual(e3_confirmation_row(suspected)["status"], "disabled")
        confirmed = self.result(status="completed", reason=None, requests=5, verdicts=[{"type": "x"}],
                                candidates=[{"candidate_ref": "1", "level": "confirmed"}])
        self.assertEqual(e3_confirmation_row(confirmed)["status"], "completed")
        for raw in (b"not json", b"[]", self.result(version="v0"), self.result(status="maybe"),
                    self.result(status="completed", reason="x"), self.result(reason="Bad Reason"), self.result(reason=None),
                    # 비활성인데 확정 요청이 있음(모순, #742 리뷰)·요청 수 모양 오류
                    self.result(requests=1), self.result(requests=True), self.result(requests=0.0), self.result(requests=-1),
                    self.result(requests="0"), self.result(requests=None),
                    self.result(status="completed", reason=None, requests=True),  # bool은 요청 수가 아니다
                    # 돌지 않은 상태인데 판정·쓰기 기록·요청이 있음(모순, #742 리뷰)과 배열이 아닌 목록 칸
                    self.result(verdicts=[{"type": "x"}]), self.result(writes=[{"id_known": False}]),
                    self.result(status="start_refused", reason="policy_source_unavailable", verdicts=[{"type": "x"}]),
                    self.result(status="start_refused", reason="policy_source_unavailable", requests=1),
                    self.result(verdicts=None), self.result(skipped={}), self.result(candidates="x"), self.result(writes=None),
                    # 판정 없는 confirmed 후보(E③ 계약 §9, #742 리뷰)·후보 모양 오류
                    self.result(candidates=[{"candidate_ref": "1", "level": "confirmed"}]),
                    self.result(status="start_refused", reason="policy_source_unavailable",
                                candidates=[{"candidate_ref": "1", "level": "confirmed"}]),
                    self.result(status="completed", reason=None, candidates=[{"candidate_ref": "1", "level": "confirmed"}]),
                    self.result(candidates=[{"candidate_ref": "1", "level": "CONFIRMED"}]), self.result(candidates=["1"])):
            with self.subTest(raw=raw[:40]):
                self.assertEqual(e3_confirmation_row(raw), {"error": "e3_confirmation_invalid"})

    def test_official_round_needs_disabled_declaration_missing(self):
        def file(value):
            return lambda p: setattr(p, "e3_file", value)
        result, _, evidence = run(purpose="official", e3_disabled_required=True)
        self.assertEqual(result["status"], "complete", result)
        e3 = evidence.files["product-files.json"]["e3_confirmation"]
        self.assertEqual((e3["status"], e3["reason"], result["e3_confirmation"]["status"]), ("disabled", "declaration_missing", "disabled"))
        # 비활성 회차의 suspected 후보는 공식 회차를 막지 않는다
        suspected = file(self.result(candidates=[{"candidate_ref": "1", "level": "suspected", "reason": "link_none"}]))
        self.assertEqual(run(suspected, purpose="official", e3_disabled_required=True)[0]["status"], "complete")
        for mutate, reason in ((file(self.result(reason="not_wired")), "e3_official_not_disabled"),
                               (file(self.result(status="completed", reason=None)), "e3_official_not_disabled"),
                               # 비활성·요청 0이어도 판정이 있으면 모순이라 비활성 근거가 아니다(#742 리뷰)
                               (file(self.result(verdicts=[{"type": "x"}])), "e3_confirmation_invalid"),
                               (file(self.result(candidates=[{"candidate_ref": "1", "level": "confirmed"}])), "e3_confirmation_invalid"),
                               (file(RuntimeError("docker_helper_copy_failed")), "e3_status_not_collected"),
                               (file(b"{}"), "e3_confirmation_invalid"),
                               (file(self.result(requests=1)), "e3_confirmation_invalid")):
            with self.subTest(reason=reason):
                result, _, _ = run(mutate, purpose="official", e3_disabled_required=True)
                self.assertEqual((result["status"], result["reasons"]), ("invalid", [reason]))

    def test_disabled_requirement_follows_the_setting_and_is_signed(self):
        # 2026-11-20 사용자: 두 검사는 설정 칸으로 건다. 배선 확인 회차가 칸을 끄면 확정 단계가 돈 기록도 완료이고, 칸을 켜면
        # 공식과 같이 disabled/declaration_missing만 받는다. 칸 값은 회차 조건(서명 근거)에 남는다.
        def file(value):
            return lambda p: setattr(p, "e3_file", value)
        completed = file(self.result(status="completed", reason=None))
        on, _, evidence = run(completed, purpose="wiring_check", e3_disabled_required=True)
        self.assertEqual((on["status"], on["reasons"]), ("invalid", ["e3_official_not_disabled"]))
        off, _, evidence = run(completed, purpose="wiring_check", e3_disabled_required=False)
        self.assertEqual(off["status"], "complete", off)
        conditions = [r for r in evidence.files["isolation-evidence.json"]["rows"] if r["kind"] == "run_conditions"][0]
        self.assertIs(conditions["e3_disabled_required"], False)
        signed, _, evidence = run(purpose="official", e3_disabled_required=True)
        conditions = [r for r in evidence.files["isolation-evidence.json"]["rows"] if r["kind"] == "run_conditions"][0]
        self.assertEqual((signed["status"], conditions["e3_disabled_required"]), ("complete", True))

    def test_official_time_budget_round_reads_the_stop_record(self):
        # 러너 예산이 먼저 끝나 제품 회차를 멈추는 경로(#742 :559 리뷰). #738 중지 기록 뒤 제품은 이 경로에서도 결과 파일을
        # 남긴다(e3contract.RunStopped: 비활성 근거가 있으면 disabled(사유), 확인 못 하면 start_refused, 돌 수 있었으면
        # interrupted). 공식 회차는 disabled/declaration_missing만 유효하고, 파일이 없으면 비활성으로 추정하지 않는다.
        def file(value):
            return lambda p: setattr(p, "e3_file", value)
        result, _, evidence = run(purpose="official", e3_disabled_required=True)
        self.assertEqual((result["status"], result["product_closed"]["state"], result["first_limit"]["value"]),
                         ("complete", "cancelled", "runner_time_budget"))
        self.assertEqual(evidence.files["product-files.json"]["e3_confirmation"]["reason"], "declaration_missing")
        for mutate, reasons in ((file(self.result(status="interrupted", reason="")), ["e3_official_not_disabled"]),
                                (file(self.result(status="start_refused", reason="policy_source_unavailable")),
                                 ["e3_official_not_disabled"]),
                                (file(self.result(reason="not_wired")), ["e3_official_not_disabled"]),
                                (file(RuntimeError("docker_helper_copy_failed")), ["e3_status_not_collected"])):
            with self.subTest(reasons=reasons, mutate=mutate):
                result, ports, _ = run(mutate, purpose="official", e3_disabled_required=True)
                self.assertEqual((result["status"], result["reasons"]), ("invalid", reasons))
                self.assertIn("stop_round", ports.calls)  # 같은 시간 상한 경로(제품 회차를 멈춤)에서 읽었다

    def test_helper_cleanup_failure_is_not_a_missing_file(self):
        # 복사 도우미 정리 실패는 보호 볼륨이 잡혀 있을 수 있다는 뜻이라 목적과 상관없이 무효다(#742 리뷰). 일반 복사 실패(파일
        # 없음)는 수용시험이면 기록만, 공식이면 수집 실패다.
        from runner_docker import HelperCleanupError
        for required in (False, True):
            with self.subTest(required=required):
                result, _, _ = run(lambda p: setattr(p, "e3_file", HelperCleanupError()), e3_disabled_required=required)
                self.assertEqual((result["status"], result.get("reasons")),
                                 ("invalid", ["e3_confirmation_copy_failed", "docker_helper_cleanup_failed"]))
        result, _, _ = run(lambda p: setattr(p, "e3_file", RuntimeError("docker_helper_copy_failed")))
        self.assertEqual(result["status"], "complete")

    def test_e3_traces_are_recorded_for_go_condition_2(self):
        # 선언 없는 회차가 E③를 건드리지 않았는지(개발 계획 Go 조건 2): 제품을 멈춘 뒤 세 저널을 세어 e3-traces.json에 남긴다.
        def line(event):
            return json.dumps({"Kind": "event", "Record": {"Index": 1, "Event": event}, "MAC": "m"}).encode() + b"\n"
        event = line({"stage": "control", "decision": "allow"})
        e3_event = line({"stage": "result", "program": "e3_read", "decision": "allow"})
        sources = {"e2_host_journal": event * 2 + e3_event, "e2_product_journal": event * 2, "declarations": b""}
        result, ports, evidence = run(lambda p: setattr(p, "trace_sources", sources))
        self.assertEqual(ports.trace_args, ["draft-1"])
        record = evidence.files["e3-traces.json"]
        self.assertEqual((record["format"], record["e2_host_journal"]["events"], record["e2_product_journal"]["by_kind"]),
                         ("abc2lab.e3-traces/v1", 3, {"control|-|allow|-": 2}))
        # 결과 파일(쓰기 기록 0건)도 같이 센다
        self.assertEqual(result["e3_traces"], {"e3_events": 1, "product_journal_e3_events": 0, "declaration_writes": 0,
                                               "result_writes": 0, "host_seal_phase": None, "host_seal_checks": None,
                                               "host_evidence_checkpoints": 0})
        self.assertEqual(result["status"], "complete")
        # 원본을 못 읽으면 기록만 하고(고정 코드) 회차는 그대로다. 공식 회차도 같다.
        for purpose in ("browser_acceptance", "official"):
            with self.subTest(purpose=purpose):
                result, _, evidence = run(lambda p: setattr(p, "trace_sources", RuntimeError("docker_timeout")), purpose=purpose)
                self.assertEqual(result["status"], "complete", result)
                self.assertEqual(evidence.files["e3-traces.json"], {"format": "abc2lab.e3-traces/v1", "error": "e3_traces_copy_failed"})
                self.assertEqual(result["e3_traces"], {"error": "e3_traces_copy_failed"})
        # 결과 파일을 못 읽었으면 결과 쓰기 수는 null이다
        result, _, _ = run(lambda p: setattr(p, "e3_file", RuntimeError("missing")))
        self.assertIsNone(result["e3_traces"]["result_writes"])

    def test_e3_traces_helper_cleanup_failure_is_invalid(self):
        from runner_docker import HelperCleanupError
        result, ports, _ = run(lambda p: setattr(p, "trace_sources", HelperCleanupError()))
        self.assertEqual((result["status"], result.get("reasons")), ("invalid", ["e3_traces_copy_failed", "docker_helper_cleanup_failed"]))
        self.assertIn("down", ports.calls)

    def test_other_purposes_record_without_invalidating(self):
        # 수용시험 목적은 기록만 한다(감독자 연결 전 빌드는 파일이 없어 수집 못 함으로 남는다)
        result, _, evidence = run(lambda p: setattr(p, "e3_file", RuntimeError("missing")))
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual(evidence.files["product-files.json"]["e3_confirmation"], {"error": "e3_confirmation_not_collected"})



class TestRunRecord(unittest.TestCase):
    """O v1.1 러너 기록(설계안 R1~R3): 회차 문맥·타임라인·점수판 파일·격리 근거·증명·해결 이벤트·서명."""

    def rows(self, evidence, kind):
        return [row for row in evidence.files["isolation-evidence.json"]["rows"] if row["kind"] == kind]

    def test_record_context_timeline_and_artifacts(self):
        from runner import RESULT_FORMAT
        from preservation_record import digest
        result, ports, evidence = run(lambda p: setattr(p, "drain_delay", 0.5))
        self.assertEqual(result["status"], "complete", result)
        record = evidence.files["run-record.json"]
        self.assertEqual(record["signature"], "s")  # 내린 뒤 러너 키로 서명(가짜 서명)
        context = {key: record[key] for key in ("run_id", "model_tag", "reset_generation", "instance_id", "execution_id")}
        self.assertEqual(context, {"run_id": "ours-1", "model_tag": "model-x", "reset_generation": IDS["target"],
                                   "instance_id": IDS["target"], "execution_id": IDS["preparation"]})
        self.assertEqual((record["version"], record["engine"], record["condition"], record["repeat_index"]), ("1.1", "ours", "ours@35b", 0))
        self.assertEqual((record["fixture_fingerprint"], record["environment_fingerprint"]), ("e" * 64, "9" * 64))
        self.assertEqual((record["total_requests"], record["blocked_attempts"], record["elapsed_seconds"]), (0, None, None))
        steps = record["timeline"]
        self.assertEqual([steps[s]["sequence"] for s in ("fixture_ready", "pre", "engine_start", "engine_end", "drained", "post")],
                         [1, 2, 3, 4, 5, 6])
        self.assertEqual(steps["pre"]["at"], (START + timedelta(seconds=0)).isoformat())  # pre 점수판 받은 시각
        # engine_end는 계측기 봉인 직후(B와 같은 자리), drained는 drain 확인 뒤다
        self.assertEqual(steps["engine_end"]["at"], (START + timedelta(seconds=ports.sealed_t)).isoformat())
        self.assertLess(steps["engine_end"]["at"], steps["drained"]["at"])
        artifacts = record["artifacts"]
        self.assertEqual((artifacts["records"], artifacts["traffic"], artifacts["coverage"]), (None, None, None))
        self.assertEqual(artifacts["pre"]["sha256"], "3" * 64)
        self.assertEqual(record["isolation_proof"]["snapshots"], {"pre_sha256": "3" * 64, "post_sha256": "4" * 64})
        network = record["isolation_proof"]["network"]
        self.assertEqual((network["mode"], network["target_origin"], network["allowed_actors"]),
                         ("dedicated_network", TARGET_ORIGIN, ["engine", "observer"]))
        self.assertEqual(record["isolation_proof"]["observer"]["ref"], "eval-obs:" + IDS["eval-obs"])
        # 결과: B와 같은 형식, E② 자리·배치·작업 요청 지문(설치·등록 조건) 서명
        self.assertEqual((result["format"], result["e2_mode"], result["layout"], result["signature"]),
                         (RESULT_FORMAT, "split_product", "exploration-split/v1", "s"))
        self.assertEqual(result["task_request_sha256"], digest(ports.split_conditions()))
        self.assertEqual(set(result["evidence"]), set(evidence.files) - {RESULT_NAME})

    def test_isolation_rows_carry_o_sources(self):
        _, _, evidence = run()
        (placement,) = self.rows(evidence, "e2_placement")
        self.assertEqual({k: placement[k] for k in ("mode", "decision_rows", "allowed", "unsupported", "unavailable")},
                         {"mode": "split_product", "decision_rows": None, "allowed": None, "unsupported": None, "unavailable": None})
        self.assertEqual(self.rows(evidence, "attempt_reconciliation"), [])  # 대조 미수집(설계안 §4)
        (verdicts,) = self.rows(evidence, "verdict_collection")
        self.assertEqual((verdicts["status"], verdicts["reason"]), ("not_collected", "product_verdict_export_unavailable"))
        (e3,) = self.rows(evidence, "e3_confirmation")
        self.assertEqual((e3["status"], e3["reason"]), ("disabled", "declaration_missing"))
        (conditions,) = self.rows(evidence, "run_conditions")
        self.assertEqual({k: conditions[k] for k in ("split", "execution_source", "e3_status_expected", "goal_common_sha256", "workers",
                                                     "output_language", "llm_stream", "engine_rules_sha256", "path_key_ref", "sampling")},
                         {"split": True, "execution_source": "model_run", "e3_status_expected": True, "goal_common_sha256": "c" * 64,
                          "workers": 3, "output_language": None, "llm_stream": False, "engine_rules_sha256": None,
                          "path_key_ref": PATH_KEY_REF, "sampling": SAMPLING})
        self.assertEqual(conditions["host"], {k: v for k, v in HOST.items() if k != "machine_id_sha256"})
        checks = self.rows(evidence, "connectivity_check")
        self.assertEqual(sorted(row["id"] for row in checks if row["phase"] == "pre"),
                         sorted("preparation/" + row["id"] for row in PROBE_ROWS))
        self.assertEqual({row["role"] for row in checks}, {"positive", "negative", "record"})
        observer = self.rows(evidence, "observer_request")
        self.assertEqual([row["window"] for row in observer], ["pre", "in_window"])
        (drain,) = self.rows(evidence, "drain")
        self.assertEqual((drain["sealed_at"], drain["drained_at"], drain["inflight"], drain["grace_seconds"]),
                         (SEALED_AT, DRAINED_AT, 0, 30))

    def test_new_solves_become_isolated_window_events(self):
        from runner_record import canonical_sha256
        _, _, evidence = run()
        record = evidence.files["run-record.json"]
        events = evidence.files["solve-events.json"]["events"]
        self.assertEqual([(e["challenge"], e["basis"], e["actor"]) for e in events], [("a", "isolated_window", "engine")])
        self.assertEqual(events[0]["isolation_proof_sha256"], canonical_sha256(record["isolation_proof"]))
        self.assertEqual(events[0]["execution_id"], IDS["preparation"])

        def regress(ports):
            ports.solved = {"pre": {"a": True, "b": False}, "post": {"a": False, "b": False}}
        result, _, _ = run(regress)
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["record_build_failed"]))

    def test_unknown_or_unobserved_values_stay_null(self):
        # 진단에 들어가지 않은 회차는 Worker 수를 모른다. 합성 회차는 제품 OpenRouter transport를 지나지 않아 스트리밍을 모른다.
        def explore_only(ports):
            ports.record["run"].pop("execution")
            ports.conditions = OSError("no such file")
        _, _, evidence = run(explore_only)
        (conditions,) = self.rows(evidence, "run_conditions")
        self.assertIsNone(conditions["workers"])
        _, _, evidence = run(synthetic=True)
        (conditions,) = self.rows(evidence, "run_conditions")
        self.assertEqual((conditions["execution_source"], conditions["llm_stream"]), ("synthetic_fixture", None))

    def test_e3_row_only_when_collected(self):
        result, _, evidence = run(lambda p: setattr(p, "e3_file", RuntimeError("missing")))
        self.assertEqual(result["status"], "complete")
        self.assertEqual(self.rows(evidence, "e3_confirmation"), [])
        (conditions,) = self.rows(evidence, "run_conditions")
        self.assertTrue(conditions["e3_status_expected"])  # 행이 없으면 채점기가 수집 실패로 본다

    def test_invalid_cases(self):
        def listener(ports):
            ports.listeners = {Listener("0.0.0.0", 9999)}
        result, _, evidence = run(listener)
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["pre_host_condition_violation"]))
        self.assertNotIn("run-record.json", evidence.files)
        self.assertNotIn("signature", result)

    def test_empty_record_fields_stop_before_anything_starts(self):
        # 기록 칸이 비면 회차를 띄우지 않는다: 제품 시작·모델 호출 사후 검증 없이 끝난다(#745 리뷰, 유료 회차를 쓰지 않게)
        from runner_split_run import RECORD_SPEC_FIELDS
        for name, value in [(name, value) for name in RECORD_SPEC_FIELDS for value in ("", "   ", "\t")]:
            with self.subTest(field=name, value=value):
                result, ports, evidence = run(**{name: value})
                self.assertEqual((result["status"], result["reasons"]), ("invalid", ["record_spec_incomplete"]))
                self.assertFalse({"preexisting", "up", "start_round", "generation_verify"} & set(ports.calls), ports.calls)
                self.assertNotIn("run-record.json", evidence.files)
                self.assertNotIn("signature", result)

    def test_observer_count_and_listener_allowance(self):
        clock = FakeClockState()
        ports = FakePorts(clock)
        meter = FakeMeter(ports)
        meter.observer_extra = 1
        result = execute_split_run(spec(), ports, meter, FakeEvidence(), clock.clock())
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["observer_count_mismatch"]))
        allowed = Listener("10.0.0.5", 11434)
        result, _, evidence = run(lambda p: setattr(p, "listeners", {allowed}), allowed_listens=frozenset({allowed}))
        self.assertEqual(result["status"], "complete", result)
        (pre,) = [row for row in self.rows(evidence, "host_condition") if row["phase"] == "pre"]
        self.assertEqual(pre["listeners_outside_loopback"], ["10.0.0.5:11434"])
        self.assertEqual(pre["allowed_listens"], [{"address": "10.0.0.5", "port": 11434}])

    def test_complete_flow_without_a_record_is_invalid(self):
        # 방어선: 흐름이 끝났는데 서명할 기록이 없으면 완료로 쓰지 않는다
        import runner_split_run as module
        with mock.patch.object(module, "_split_record", lambda *args, **kwargs: None):
            result, _, _ = run()
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["record_build_failed"]))

    def test_record_sign_failure_leaves_unsigned_invalid_result(self):
        def fail_first_sign(ports):
            original = ports.sign
            def sign(payload):
                if "isolation_proof" in payload:
                    raise OSError("key")
                return original(payload)
            ports.sign = sign
        result, _, evidence = run(fail_first_sign)
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["record_sign_failed"]))
        self.assertNotIn("run-record.json", evidence.files)
        self.assertNotIn("signature", result)


class TestInterruptedCallsO(unittest.TestCase):
    """O: 제품 호출 기록·정지 요청 시각은 회차 근거에 남기되, 끊긴 호출 예외는 보류한다(#735 리뷰). 제품 전송기는 끊긴 호출에
    generation ID를 남기지 않아(응답을 끝까지 읽은 forwarded에만 있음) 끊김과 미확인 ID를 잇는 근거가 없다."""

    def ports(self, verified_late: bool):
        def mutate(p):
            # 제품이 실제로 쓰는 모양: 완료 호출은 ID가 있고, 정지로 끊긴 호출은 ID가 비어 있다(openrouter_transport.go)
            rows = [{"seq": 1, "decision": "forwarded", "method": "POST", "status": 200, "started_at": "2000-01-01T00:00:00Z",
                     "completed_at": "2000-01-01T00:00:01Z", "request_bytes": 1, "response_bytes": 1, "generation_id": "gen-1"},
                    {"seq": 2, "decision": "upstream_error", "method": "POST", "status": 200, "started_at": "2000-01-01T00:00:02Z",
                     "completed_at": "2999-01-01T00:00:00Z", "request_bytes": 1, "response_bytes": 0, "generation_id": "",
                     "unfinished_end": "client_closed_before_done"},
                    {"seq": 3, "decision": "forwarded", "method": "POST", "status": 200, "started_at": "2000-01-01T00:00:03Z",
                     "completed_at": "2999-01-01T00:00:00Z", "request_bytes": 1, "response_bytes": 1, "generation_id": "late"}]
            p.calls_file = b"".join(json.dumps(row).encode() + b"\n" for row in rows)
            p.ledger = b"r1 10 20 30\n" * len(rows)  # 장부 예약 줄 수 = 호출 기록 줄 수
            late = product_marker()["generations"][0] | {"id": "late"}
            if not verified_late:
                late = {"id": "late", "status": "unverified", "provider_name": "", "attempts": [],
                        "lookup": {"tries": 40, "last": "http_404"}}
            p.marker = product_marker(generations=[product_marker()["generations"][0], late])
        return mutate

    def test_records_are_kept_and_the_cut_call_without_an_id_does_not_count(self):
        result, ports, evidence = run(self.ports(verified_late=True))
        self.assertEqual(result["status"], "complete", result)
        summary = evidence.files["llm-summary.json"]
        self.assertEqual((summary["unverified_interrupted"], summary["interrupted_calls"]), (0, 0))
        self.assertEqual(evidence.files["product-calls.jsonl"], ports.calls_file.decode())  # 받은 바이트 그대로
        self.assertEqual(result["stop_requested_at"], summary["stop_requested_at"])  # 정지 요청 시각도 근거에 남는다

    def test_unverified_completed_call_near_the_stop_still_invalidates(self):
        # 정지 순간 즈음 끝난 호출이라도 ID가 있으면 응답을 끝까지 읽은 완료 호출이다. 미확인이면 지금처럼 무효다.
        from runner_llm import interrupted_generation_ids
        result, _, evidence = run(self.ports(verified_late=False))
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["llm_generation_unverified"]))
        summary = evidence.files["llm-summary.json"]
        self.assertEqual(summary["unverified_interrupted"], 0)
        rows = [json.loads(line) for line in evidence.files["product-calls.jsonl"].splitlines() if line]
        # 저장된 기록·시각으로 다시 계산해도 같다(보류 중이라 빈 집합)
        self.assertEqual(interrupted_generation_ids(rows, summary["stop_requested_at"], product=True), frozenset())


class TestRequestCap(unittest.TestCase):
    """O도 B와 같은 검사(runner.check_request_cap): 계측기 상한이 이 회차 예산이고 전달이 넘지 않았어야 한다."""

    def test_cap_must_be_the_budget_and_hold(self):
        result, _, _ = run()
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual((result["drained"]["request_cap"], result["drained"]["rejected_over_cap"]), (10, 0))
        for cap in (0, 9, 11):
            with self.subTest(cap=cap):
                result, _, _ = run_with_meter(lambda m, c=cap: setattr(m, "request_cap", c))
                self.assertEqual((result["status"], result["reasons"]), ("invalid", ["meter_request_cap_mismatch"]))
        # 예산 1에서 표식 상한이 JSON true·1.0이면 값 비교로는 같아 보이지만 정수가 아니라 무효다(#734 리뷰)
        for value in (True, 1.0):
            with self.subTest(value=value):
                result, _, _ = run_with_meter(lambda m, v=value: setattr(m, "request_cap", v), request_budget=1)
                self.assertEqual((result["status"], result.get("reasons")), ("invalid", ["meter_request_cap_mismatch"]))
        self.assertEqual(run_with_meter(lambda m: setattr(m, "request_cap", 1), request_budget=1)[0]["status"], "complete")
        # 봉인 표식의 상한이 drain 표식과 다르면 같은 계측기의 상한 적용을 증명하지 못한다(#734 리뷰)
        result, _, _ = run_with_meter(lambda m: setattr(m, "sealed_cap", 0))
        self.assertEqual((result["status"], result.get("reasons")), ("invalid", ["meter_request_cap_mismatch"]))
        def overshoot(ports):  # 시작 뒤 계측기가 예산보다 많이 전달한 모양(상한이 깨진 계측기)
            original = ports.start_round

            def start(prepared):
                out = original(prepared)
                ports.forwarded = 12
                return out
            ports.start_round = start
        result, _, _ = run_with_meter(lambda m: setattr(m, "request_cap", 10), overshoot)
        self.assertEqual((result["status"], result["reasons"]), ("invalid", ["meter_request_cap_exceeded"]))


def run_with_meter(mutate_meter, mutate_ports=None, **spec_changes):
    clock = FakeClockState()
    ports = FakePorts(clock)
    if mutate_ports:
        mutate_ports(ports)
    meter = FakeMeter(ports)
    mutate_meter(meter)
    evidence = FakeEvidence()
    result = execute_split_run(spec(**spec_changes), ports, meter, evidence, clock.clock())
    return result, ports, evidence


if __name__ == "__main__":
    unittest.main()
