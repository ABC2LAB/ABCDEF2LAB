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
from runner_split_probe import SplitProbeOutcome  # noqa: E402
from runner_split_run import SplitRunSpec, execute_split_run  # noqa: E402
from runner_window import WatchSample  # noqa: E402
from test_runner import FakeEvidence  # noqa: E402

IDS = {"eval-meter": "m" * 64, "explore-boundary": "b" * 64, "preparation": "p" * 64, "target": "t" * 64}
IMAGES = {"product": "sha256:" + "1" * 64, "meter": "sha256:" + "2" * 64, "explore_browser": "sha256:" + "3" * 64,
          "fixture": "sha256:" + "4" * 64, "target": "sha256:" + "5" * 64}
FINGERPRINT = "f" * 64
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
                  "attempts": [{"provider_name": LLM_NAME, "endpoint_id": "ep-1", "status": 200}]}
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
                  path_key_ref=PATH_KEY_REF)
    values.update(changes)
    return SplitRunSpec(**values)


def snapshot():
    return {"networks": {"target": {"id": "n" * 64}}, "services": {name: {"id": cid} for name, cid in IDS.items()}}


def probe(status=PASS, cid="c"):
    origin = ProbeOutcome(status, () if status == PASS else ("negative_path_reachable",), (), "s", "f", cid * 64)
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
        self.ports = ports
        self.path_log = True
        self.path_key_ref = PATH_KEY_REF
        self.sealed = {"role": "engine", "sealed_at": "t"}
        self.path_rows_error = None
        self.log_complete = True

    def forwarded(self):
        return self.ports.forwarded

    def marker(self, name):
        if name == "engine-sealed.json":
            return self.sealed
        self.ports.calls.append("meter:drained")
        return {"inflight_at_drain": 0, "log_complete": self.log_complete, "forced_cancelled_requests": self.ports.forced,
                "forwarded_requests": self._total()}

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
        self.record = {"version": 1, "task_id": 42, "goal": dict(GOAL),
                       "model": {"requests": 2, "sampling_overwrites": 0, "sampling": dict(SAMPLING)},
                       "run": {"phase": "closed", "run_ref": "run-draft-1",
                               "execution": {"scope": dict(SCOPE), "policy_revision": "v1"}}}
        self.ledger, self.calls_file = LEDGER, CALLS
        self.forced = 0
        self.boundary_stopped = False
        self.snapshot_times: dict[str, float] = {}
        self.layout = {"pre": [], "post": []}
        self.snapshot_change: dict[str, dict] = {}
        self.marker: dict | None = product_marker()
        self.verify_args: list = []
        self.surface = surface()
        self.view = {"format": "abc2lab.control-policy-view/v1", "policy_ref": "policy-draft-1", "policy_revision": "v1",
                     "policy_fingerprint_sha256": FINGERPRINT, "limits": dict(LIMITS), "rules": []}
        self.conditions = conditions()
        self.files_args: list = []

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
        return {"cpu_model": "x"}

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
        return {"solved": {"a": False}, "state_sha256": "s" * 64}

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
        assert meter_id == IDS["eval-meter"]
        self.forwarded = self.forwarded  # 봉인 뒤 늘지 않는다

    def stop_boundary(self, boundary_id):
        self._call("stop_boundary")
        assert boundary_id == IDS["explore-boundary"]
        self.boundary_stopped = True

    def run_record(self, task_id):
        self._call("run_record")
        return self.record

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
ORDER_TAIL = ["seal", "meter:drained", "stop_round", "wait_closed", "snapshot:post", "check:post", "probes:post", "scoreboard:post",
              "stop_boundary", "watch:stop",
              "events:stop", "status", "run_record", "control_policy", "product_files", "runtime_conditions",
              "copy_boundary_evidence",
              "copy_product_records",
              "generation_verify", "llm_verified", "down", "remove_inputs"]


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
        self.assertEqual(set(evidence.files), {"meter-preflight.json", "subnet-check.json", "pre-checks.json", "post-checks.json",
                                               "pre-probe.json",
                                               "post-probe.json", "task.json", "llm-summary.json",
                                               "window-watch.json", "product-end.json", "product-model.json", "tool-surface.json",
                                               "product-files.json", RESULT_NAME})
        self.assertEqual(evidence.files[RESULT_NAME], result)
        # 결정안 §3.3 제품 블록 근거: 파일 원본 SHA 다섯 + 상한 스냅숏 지문, E② 정책 내용 지문, 도구 면 지문.
        from preservation_record import digest
        files = evidence.files["product-files.json"]
        self.assertEqual(files["file_sha256"], dict(PRODUCT_FILES, control_limits=digest(LIMITS),
                                                    runtime_conditions=hashlib.sha256(conditions()).hexdigest()))
        self.assertEqual((files["diagnosis_policy_sha256"], files["limits"], files["diagnosis_entered"]), (FINGERPRINT, LIMITS, True))
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
                                                                                    "status": 200}]}])))
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
        # 보존 옵션이 없으면 발행하지 않는다
        plain, ports, evidence = run()
        self.assertNotIn("sign", ports.calls)
        self.assertNotIn(NAME, evidence.files)


if __name__ == "__main__":
    unittest.main()
