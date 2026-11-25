"""끝 신호 뒤 무활동 유예(고정 초안 §4 (다), 2026-11-25 사용자) 시험.

정상 완료 끝 신호를 읽은 뒤 T초 동안 모델 호출·대상 요청이 없으면 러너가 회차를 끝낸다(stop_reason engine_completed, 러너 상한
runner_engine_idle). 정상 완료가 아닌 끝 상태와 끝 신호 없는 회차는 지금처럼 러너 상한까지 간다. O는 제품 정리 끝(drain_complete)이
있어야 끝내고, 목표 달성 마감(#894 이후 completed)도 같은 정상 완료로 유예 종료에 들어간다.
"""

from __future__ import annotations

import os
import sys
import unittest
from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import runner  # noqa: E402
from runner import (  # noqa: E402
    END_IDLE_FORMAT, END_IDLE_INFLIGHT_REASON, FIRST_LIMIT_ENGINE_IDLE, STOP_ENGINE_IDLE, Clock, _EndIdle, _wait_budget,
    end_idle_reason, first_limit, inflight_at_stop,
)
from preservation_record import RUNNER_LIMITS, expected_first_limit  # noqa: E402
from runner_split_run import product_drained, product_end_watch  # noqa: E402

T = 180


class FakeClock:
    def __init__(self):
        self.t = 0.0

    def clock(self) -> Clock:
        return Clock(lambda: datetime(2026, 11, 25, tzinfo=timezone.utc) + timedelta(seconds=self.t),
                     lambda: self.t, self.sleep)

    def sleep(self, seconds):
        self.t += seconds


class FakeWatch:
    """_EngineEndWatch의 두 칸(first_terminal·last_view)만 흉내 낸다."""

    def __init__(self):
        self.first_terminal = None
        self.last_view = None

    def poll(self):
        pass

    def end(self, status, at, **view):
        self.first_terminal = {"status": status, "at": "t", "elapsed_seconds": at}
        self.last_view = dict(view, state=status)


def step(idle, watch, clock, forwarded, until, poll=1.0):
    """until초까지 1초마다 due를 부르고, 처음 참이 된 시각(없으면 None)을 돌려준다."""
    while clock.t < until:
        if idle.due(watch, forwarded() if callable(forwarded) else forwarded):
            return clock.t
        clock.sleep(poll)
    return None


class TestEndIdleUnit(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock()
        self.watch = FakeWatch()

    def idle(self, **options):
        return _EndIdle(T, self.clock.clock(), 0.0, **options)

    def test_no_end_signal_never_stops(self):
        idle = self.idle(model_calls=lambda: 0)
        self.assertIsNone(step(idle, self.watch, self.clock, 5, 2000))
        self.assertFalse(idle.record(None)["stopped"])

    def test_non_success_end_keeps_waiting(self):
        # 정상 완료가 아닌 끝 상태는 engine_completed로 적지 않게 유예 종료를 하지 않는다(러너 상한까지).
        for status in ("failed", "timeout", "cancelled", "unknown"):
            with self.subTest(status=status):
                clock, watch = FakeClock(), FakeWatch()
                idle = _EndIdle(T, clock.clock(), 0.0, model_calls=lambda: 0)
                clock.t = 10.0
                watch.end(status, 10.0)
                self.assertIsNone(step(idle, watch, clock, 5, 2000))

    def test_stops_t_seconds_after_the_end_signal_without_activity(self):
        idle = self.idle(model_calls=lambda: 7)
        self.clock.t = 100.0
        self.watch.end("done", 100.0)
        self.assertEqual(step(idle, self.watch, self.clock, 12, 2000), 100.0 + T)
        record = idle.record(0)
        self.assertEqual(record, {"format": END_IDLE_FORMAT, "seconds": T, "stopped": True, "end_signal_elapsed_seconds": 100.0,
                                  "last_activity_elapsed_seconds": 100.0, "stopped_elapsed_seconds": 280.0,
                                  "model_calls_watched": True, "model_read_failures": 0, "drain_required": False,
                                  "inflight_at_stop": 0})

    def test_counts_from_the_first_sample_after_the_end_signal(self):
        # #951 이동찬 P2: 끝 신호 100초, 대상 요청 103초, 첫 표본 105초. 103초 요청은 첫 표본 값에 묻혀 수 변화로 안 보이므로
        # 끝 신호부터 세면 280초(마지막 요청 뒤 177초)에 끝난다. 첫 표본 시각부터 세야 마지막 활동 뒤 T 이상 기다린다.
        idle = self.idle(model_calls=lambda: 0)
        self.clock.t = 105.0
        self.watch.end("done", 100.0)
        stopped = step(idle, self.watch, self.clock, 4, 2000)  # 끝 신호 때 3이었고 103초에 4가 됐다(첫 표본은 4)
        self.assertEqual(stopped, 105.0 + T)
        self.assertGreaterEqual(stopped - 103.0, T)
        record = idle.record(0)
        self.assertEqual((record["end_signal_elapsed_seconds"], record["last_activity_elapsed_seconds"]), (100.0, 105.0))

    def test_model_call_between_the_end_signal_and_the_first_sample_is_covered(self):
        # 같은 경계에서 모델 호출이 끝난 경우(게이트웨이 행이 첫 표본 전에 하나 늘어남)도 첫 표본부터 센다.
        idle = self.idle(model_calls=lambda: 8)
        self.clock.t = 101.5
        self.watch.end("done", 100.0)
        self.assertEqual(step(idle, self.watch, self.clock, 0, 2000), 101.5 + T)

    def test_target_request_after_the_end_restarts_the_grace(self):
        idle = self.idle(model_calls=lambda: 0)
        self.clock.t = 100.0
        self.watch.end("done", 100.0)
        forwarded = lambda: 3 if self.clock.t < 150 else 4  # 150초에 대상 요청 하나
        self.assertEqual(step(idle, self.watch, self.clock, forwarded, 2000), 150.0 + T)
        self.assertEqual(idle.record(0)["last_activity_elapsed_seconds"], 150.0)

    def test_model_call_after_the_end_restarts_the_grace(self):
        idle = self.idle(model_calls=lambda: 1 if self.clock.t < 200 else 2)
        self.clock.t = 100.0
        self.watch.end("done", 100.0)
        self.assertEqual(step(idle, self.watch, self.clock, 9, 2000), 200.0 + T)

    def test_unreadable_model_calls_count_as_activity(self):
        # 모델 호출 수를 못 읽으면 무활동을 확인할 수 없어 그 폴링을 활동으로 본다(유예가 짧아지지 않게).
        def calls():
            if self.clock.t < 300:
                raise RuntimeError("llm_row_invalid")
            return 4
        idle = self.idle(model_calls=calls)
        self.clock.t = 100.0
        self.watch.end("done", 100.0)
        stopped = step(idle, self.watch, self.clock, 1, 2000)
        self.assertEqual(stopped, 299.0 + 1 + T)  # 300초에 처음 읽힘 → 그 폴링도 수가 바뀐 활동
        self.assertEqual(idle.record(0)["model_read_failures"], 200)

    def test_drain_required_until_the_product_says_drained(self):
        idle = self.idle(ready=product_drained)
        self.clock.t = 50.0
        self.watch.end("completed", 50.0, drain_complete=False)
        self.assertIsNone(step(idle, self.watch, self.clock, 0, 400))
        self.watch.last_view = dict(self.watch.last_view, drain_complete=True)
        start = self.clock.t
        self.assertEqual(step(idle, self.watch, self.clock, 0, 2000), start - 1 + T)
        self.assertTrue(idle.record(None)["drain_required"])

    def test_product_drained_needs_completed_and_true(self):
        self.assertTrue(product_drained({"state": "completed", "drain_complete": True}))
        for view in (None, {}, {"state": "completed", "drain_complete": False}, {"state": "completed", "drain_complete": 1},
                     {"state": "failed", "drain_complete": True}, {"state": "completed"}):
            with self.subTest(view=view):
                self.assertFalse(product_drained(view))


class TestWaitBudget(unittest.TestCase):
    def spec(self, request_budget=300, time_budget=1800):
        return SimpleNamespace(request_budget=request_budget, time_budget_seconds=time_budget, poll_seconds=1.0)

    def meter(self, values):
        return SimpleNamespace(forwarded=values)

    def test_budgets_come_before_the_grace(self):
        clock, watch = FakeClock(), FakeWatch()
        watch.end("done", 0.0)
        idle = _EndIdle(T, clock.clock(), 0.0, model_calls=lambda: 0)
        self.assertEqual(_wait_budget(self.spec(request_budget=1), self.meter(lambda: 1), clock.clock(), runner._State(),
                                      0.0, watch, idle), ("request_budget", 1))
        clock, watch = FakeClock(), FakeWatch()
        watch.end("done", 0.0)
        idle = _EndIdle(T, clock.clock(), 0.0, model_calls=lambda: 0)
        self.assertEqual(_wait_budget(self.spec(time_budget=100), self.meter(lambda: 0), clock.clock(), runner._State(),
                                      0.0, watch, idle), ("time_budget", 0))

    def test_grace_stop_and_first_limit(self):
        clock, watch = FakeClock(), FakeWatch()
        watch.poll = lambda: watch.end("done", 400.0) if clock.t >= 400 and watch.first_terminal is None else None
        idle = _EndIdle(T, clock.clock(), 0.0, model_calls=lambda: 3)
        stop, forwarded = _wait_budget(self.spec(), self.meter(lambda: 42), clock.clock(), runner._State(), 0.0, watch, idle)
        self.assertEqual((stop, forwarded, clock.t), (STOP_ENGINE_IDLE, 42, 400.0 + T))
        limit = first_limit(stop, clock.t, 1800, watch.first_terminal)
        self.assertEqual(limit, {"value": "engine_completed", "runner_limit": FIRST_LIMIT_ENGINE_IDLE,
                                 "runner_limit_elapsed_seconds": 580.0, "engine_terminal_elapsed_seconds": 400.0})

    def test_without_t_there_is_no_grace(self):
        clock, watch = FakeClock(), FakeWatch()
        watch.end("done", 0.0)
        self.assertEqual(_wait_budget(self.spec(time_budget=900), self.meter(lambda: 0), clock.clock(), runner._State(),
                                      0.0, watch, None), ("time_budget", 0))


class TestGoalMetO(unittest.TestCase):
    """O 목표 달성 마감(#894 이후 제품 초안 completed, intent_outcomes.goal_met)도 정상 완료로 유예 종료에 들어간다."""

    def run_o(self, states, time_budget=1800):
        clock = FakeClock()

        class Driver:
            def __init__(self, items):
                self.items = list(items)

            def status(self, draft_id):
                return self.items.pop(0) if len(self.items) > 1 else self.items[0]

        ports = SimpleNamespace(background=lambda work: work())
        watch = product_end_watch(ports, Driver(states), "draft-1", "42", clock.clock(), 0.0, 5.0)
        idle = _EndIdle(T, clock.clock(), 0.0, ready=product_drained)
        spec = SimpleNamespace(request_budget=300, time_budget_seconds=time_budget, poll_seconds=1.0)
        stop = _wait_budget(spec, SimpleNamespace(forwarded=lambda: 17), clock.clock(), runner._State(), 0.0, watch, idle)
        return stop, clock.t, watch, idle

    @staticmethod
    def draft(state, **extra):
        return {"id": "draft-1", "state": state, "revision": 2, "phase": "execution", **extra}

    def goal_met(self, drained=True):
        return self.draft("completed", phase="report", outcome="complete", closing_reason="complete", reason_code=None,
                          drain_complete=drained, result_ready=True,
                          intent_outcomes={"done": 1, "policy_limited": 0, "incomplete": 0, "unknown": 0, "stopped": 0,
                                           "goal_met": 2})

    def test_goal_met_completion_ends_after_the_grace(self):
        running = [self.draft("running")] * 300
        stop, at, watch, idle = self.run_o(running + [self.goal_met()])
        self.assertEqual(watch.first_terminal["status"], "completed")
        self.assertEqual(stop, (STOP_ENGINE_IDLE, 17))
        end = watch.first_terminal["elapsed_seconds"]
        first = idle.record(0)["last_activity_elapsed_seconds"]  # 끝 신호를 거둔 폴링(상태 읽기 한 번 뒤)
        self.assertTrue(end <= first <= end + 1.0)
        self.assertEqual(at, first + T)
        limit = first_limit(stop[0], at, 1800, watch.first_terminal)
        self.assertEqual((limit["value"], limit["runner_limit"]), ("engine_completed", FIRST_LIMIT_ENGINE_IDLE))
        self.assertEqual(expected_first_limit(stop[0], limit["runner_limit_elapsed_seconds"], 1800,
                                              limit["engine_terminal_elapsed_seconds"], "complete", FIRST_LIMIT_ENGINE_IDLE,
                                              "completed"), "engine_completed")

    def test_goal_met_without_drain_waits_for_the_runner_limit(self):
        stop, at, _, _ = self.run_o([self.draft("running")] * 10 + [self.goal_met(drained=False)], time_budget=900)
        self.assertEqual((stop, at), (("time_budget", 17), 900.0))

    def test_failed_or_cancelled_o_waits_for_the_runner_limit(self):
        for state in ("failed", "cancelled", "unknown"):
            with self.subTest(state=state):
                stop, at, _, _ = self.run_o([self.draft("running")] * 10 + [self.draft(state, drain_complete=True)],
                                            time_budget=900)
                self.assertEqual((stop, at), (("time_budget", 17), 900.0))

    def test_product_budget_closing_is_also_completed(self):
        closing = self.draft("completed", outcome="partial", closing_reason="budget_limited", drain_complete=True)
        stop, at, watch, _ = self.run_o([self.draft("running")] * 10 + [closing])
        self.assertEqual(stop[0], STOP_ENGINE_IDLE)
        self.assertEqual(first_limit(stop[0], at, 1800, watch.first_terminal)["value"], "product_limit:budget_limited")


class TestInflightAtStop(unittest.TestCase):
    STOP = "2026-11-25T00:10:00+00:00"

    @staticmethod
    def row(start, end, decision="forwarded"):
        return {"seq": 1, "decision": decision, "started_at": start, "completed_at": end}

    def test_counts_calls_running_across_the_stop(self):
        rows = [self.row("2026-11-25T00:09:00Z", "2026-11-25T00:09:30Z"),            # 끝남
                self.row("2026-11-25T00:09:50Z", "2026-11-25T00:10:05Z"),            # 걸침
                self.row("2026-11-25T00:09:59.5Z", "2026-11-25T00:10:00.2Z", "cancelled_by_seal"),
                self.row("2026-11-25T00:10:01Z", "2026-11-25T00:10:02Z"),            # 뒤에 시작(봉인 전 새 호출)
                self.row("2026-11-25T00:09:10Z", "2026-11-25T00:09:20Z", "cancelled_by_seal")]
        self.assertEqual(inflight_at_stop(rows, self.STOP), 3)
        self.assertEqual(inflight_at_stop([], self.STOP), 0)

    def test_unreadable_is_unknown(self):
        for rows in (None, [{"started_at": "x", "completed_at": "y"}], [{"completed_at": "2026-11-25T00:09:00Z"}], [None]):
            with self.subTest(rows=rows):
                self.assertIsNone(inflight_at_stop(rows, self.STOP))

    def test_reason(self):
        self.assertIsNone(end_idle_reason("time_budget", 5))
        self.assertIsNone(end_idle_reason(STOP_ENGINE_IDLE, 0))
        self.assertEqual(end_idle_reason(STOP_ENGINE_IDLE, 1), END_IDLE_INFLIGHT_REASON)
        self.assertEqual(end_idle_reason(STOP_ENGINE_IDLE, None), END_IDLE_INFLIGHT_REASON)
        self.assertIsNone(end_idle_reason(STOP_ENGINE_IDLE, None, records_expected=False))


class TestPreservationFirstLimit(unittest.TestCase):
    def test_engine_idle_limit_is_known_and_checked(self):
        self.assertIn(FIRST_LIMIT_ENGINE_IDLE, RUNNER_LIMITS)
        ok = expected_first_limit("engine_completed", 580.0, 1800, 400.0, None, FIRST_LIMIT_ENGINE_IDLE, "done")
        self.assertEqual(ok, "engine_completed")
        # 끝 신호 없음·늦음·정상 완료 아님·러너 상한 이름 어긋남은 모순(None)이다.
        for args in ((580.0, 1800, None, None, FIRST_LIMIT_ENGINE_IDLE, None),
                     (580.0, 1800, 600.0, None, FIRST_LIMIT_ENGINE_IDLE, "done"),
                     (580.0, 1800, 400.0, None, FIRST_LIMIT_ENGINE_IDLE, "failed"),
                     (580.0, 1800, 400.0, None, "runner_time_budget", "done")):
            with self.subTest(args=args):
                self.assertIsNone(expected_first_limit("engine_completed", *args))


if __name__ == "__main__":
    unittest.main()
