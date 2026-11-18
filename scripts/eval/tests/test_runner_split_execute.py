"""탐색 분리 배치 O 회차 흐름(execute_split_run, 4-7c-3) 시험. 가짜 포트로 순서·무효 처리·정리를 본다."""

from __future__ import annotations

import hashlib
import json
import os
import sys
import unittest
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner import RESULT_NAME, Clock  # noqa: E402
from runner_probe import INVALID, PASS, VIOLATION, ProbeOutcome  # noqa: E402
from runner_split_probe import SplitProbeOutcome  # noqa: E402
from runner_split_run import SplitRunSpec, execute_split_run  # noqa: E402
from runner_window import WatchSample  # noqa: E402
from test_runner import FakeEvidence  # noqa: E402

IDS = {"eval-meter": "m" * 64, "explore-boundary": "b" * 64, "preparation": "p" * 64, "target": "t" * 64}
IMAGES = {"product": "sha256:" + "1" * 64, "meter": "sha256:" + "2" * 64, "explore_browser": "sha256:" + "3" * 64}
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
                  llm_sampling=dict(SAMPLING), llm_provider_slug=LLM_SLUG, llm_provider_name=LLM_NAME, meter_source_sha="a" * 40)
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


class FakeMeter:
    def __init__(self, ports):
        self.ports = ports

    def forwarded(self):
        return self.ports.forwarded

    def marker(self, name):
        self.ports.calls.append("meter:drained")
        return {"inflight_at_drain": 0, "log_complete": True, "forced_cancelled_requests": self.ports.forced}


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


if __name__ == "__main__":
    unittest.main()
