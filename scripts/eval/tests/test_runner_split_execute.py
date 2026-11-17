"""탐색 분리 배치 O 회차 흐름(execute_split_run, 4-7c-3) 시험. 가짜 포트로 순서·무효 처리·정리를 본다."""

from __future__ import annotations

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
IMAGES = {"product": "sha256:" + "1" * 64, "meter": "sha256:" + "2" * 64}
HEALTH = {"preparation": "test -S /p/ipc/preparation.sock"}
START = datetime(2026, 11, 17, 7, 0, tzinfo=timezone.utc)
LEDGER = b"r1 10 20 30\nr1 10 20 30\n"
CALLS = b'{"seq":1}\n{"seq":2}\n'
GOAL = {"version": "goal-digest/v1", "common_sha256": "c" * 64, "product_sha256": "d" * 64}
SAMPLING = {"temperature": 1.0, "top_p": 0.95, "top_k": 20, "min_p": 0, "presence_penalty": 1.5, "repetition_penalty": 1.0,
            "seed": 42}


def spec(**changes):
    values = dict(run_id="ours-1", project="eval-ours-1", image_ids=dict(IMAGES), request_budget=10, time_budget_seconds=60.0,
                  poll_seconds=1.0, drain_timeout_seconds=5.0, events_lookback_seconds=10.0, host_watch_max_gap_seconds=15.0,
                  task_status_wait_seconds=2.0, healthchecks=dict(HEALTH), product_close_seconds=30.0)
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
        return {"inflight_at_drain": 0, "log_complete": True, "forced_cancelled_requests": 0}


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
                       "model": {"requests": 2, "sampling_overwrites": 0, "sampling": dict(SAMPLING)}}
        self.ledger, self.calls_file = LEDGER, CALLS

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
        return snapshot()

    def probes(self, phase, snap):
        self._call(f"probes:{phase}")
        return probe(self.probe_status[phase], "1" if phase == "pre" else "2")

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

    def run_record(self, task_id):
        self._call("run_record")
        return self.record

    def copy_boundary_evidence(self):
        self._call("copy_boundary_evidence")

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


ORDER_HEAD = ["preexisting", "image_ids", "host_spec", "planned_subnets", "up", "snapshot:pre", "probes:pre", "dns_token",
              "events:start", "watch:start", "scoreboard:pre", "prepare_round", "start_round"]
ORDER_TAIL = ["seal", "stop_boundary", "meter:drained", "stop_round", "wait_closed", "snapshot:post", "probes:post", "scoreboard:post", "watch:stop",
              "events:stop", "status", "run_record", "copy_boundary_evidence", "copy_product_records", "down", "remove_inputs"]


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
        self.assertEqual(set(evidence.files), {"subnet-check.json", "pre-probe.json", "post-probe.json", "task.json",
                                               "window-watch.json", "product-end.json", "product-model.json", RESULT_NAME})
        self.assertEqual(evidence.files[RESULT_NAME], result)
        self.assertEqual(set(evidence.files["pre-probe.json"]["origins"]), {"preparation"})
        task = evidence.files["task.json"]
        self.assertEqual((task["format"], task["draft_id"], task["task_id"]), ("abc2lab-eval-runner-split-task-v1", "draft-1", "42"))

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
        for name, code in (("up", "compose_up_failed"), ("dns_token", "dns_token_failed"), ("prepare_round", "product_prepare_failed"),
                           ("start_round", "product_start_failed"), ("seal", "seal_failed"), ("stop_boundary", "boundary_stop_failed"),
                           ("stop_round", "product_stop_failed"), ("wait_closed", "product_close_failed"),
                           ("run_record", "product_run_record_failed"), ("copy_boundary_evidence", "boundary_evidence_copy_failed"),
                           ("copy_product_records", "product_records_copy_failed")):
            with self.subTest(port=name):
                result, ports, _ = self.assert_invalid(code, lambda p, n=name: p.fail.update({n: RuntimeError("secret value")}))
                self.assertNotIn("secret", str(result))

    def test_cleanup_failures_are_reported(self):
        result, _, _ = run(lambda p: p.fail.update(down=RuntimeError("x")))
        self.assertEqual((result["status"], result["reasons"]), (INVALID, ["down_failed"]))
        result, _, _ = run(lambda p: p.fail.update(remove_inputs=RuntimeError("x")))
        self.assertEqual((result["status"], result["reasons"]), (INVALID, ["inputs_cleanup_failed"]))


if __name__ == "__main__":
    unittest.main()
