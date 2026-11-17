"""탐색 분리 배치 O 회차 끝 신호·first_limit(결정안 §3·§3.1, 4-7c-3) 시험."""

from __future__ import annotations

import os
import sys
import unittest
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner import FIRST_LIMIT_ENGINE, Clock, first_limit  # noqa: E402
from runner_product import ProductAPIError  # noqa: E402
from runner_split_run import PRODUCT_END_FIELDS, PRODUCT_END_FORMAT, product_end_watch, product_status_view  # noqa: E402


class FakeClock:
    def __init__(self):
        self.t = 0.0

    def clock(self) -> Clock:
        return Clock(lambda: datetime(2026, 11, 17, tzinfo=timezone.utc) + timedelta(seconds=self.t),
                     lambda: self.t, self.sleep)

    def sleep(self, seconds):
        self.t += seconds


class FakePorts:
    def background(self, work):
        work()  # 시험에서는 바로 돈다(거두는 순서는 poll이 정한다)


class FakeDriver:
    def __init__(self, states):
        self.states = list(states)
        self.calls = []

    def status(self, draft_id):
        self.calls.append(draft_id)
        item = self.states.pop(0) if len(self.states) > 1 else self.states[0]
        if isinstance(item, Exception):
            raise item
        return item


def draft(state, **extra):
    return {"id": "draft-1", "state": state, "revision": 2, "phase": "execution", **extra}


class TestProductStatusView(unittest.TestCase):
    def test_only_end_fields_and_null_for_missing(self):
        view = product_status_view(FakeDriver([draft("running", task_id="42", scope={"x": 1})]), "draft-1")
        self.assertEqual(set(view), set(PRODUCT_END_FIELDS))
        self.assertEqual((view["state"], view["phase"], view["outcome"], view["closing_reason"]), ("running", "execution", None, None))


class TestProductEndWatch(unittest.TestCase):
    def watch(self, states):
        clock = FakeClock()
        driver = FakeDriver(states)
        watch = product_end_watch(FakePorts(), driver, "draft-1", "42", clock.clock(), 0.0, 5.0)
        return watch, clock, driver

    def test_first_terminal_carries_closing_reason_and_final_read(self):
        done = draft("completed", outcome="partial", closing_reason="budget_limited", reason_code=None, drain_complete=True,
                     result_ready=True)
        watch, clock, driver = self.watch([draft("running"), draft("running"), done])
        for _ in range(3):
            watch.poll()
            clock.sleep(2)
        watch.poll()  # 마지막 결과를 거둔다
        record = watch.record()
        self.assertEqual(record["format"], PRODUCT_END_FORMAT)
        self.assertEqual(record["task_id"], "42")
        self.assertEqual((record["first_terminal"]["status"], record["first_terminal"]["closing_reason"],
                          record["first_terminal"]["reason_code"], record["first_terminal"]["elapsed_seconds"]),
                         ("completed", "budget_limited", None, 4.0))
        self.assertEqual((record["final"]["state"], record["final"]["outcome"]), ("completed", "partial"))
        self.assertEqual(record["window_read_failures"], 0)
        self.assertTrue(all(call == "draft-1" for call in driver.calls))

    def test_read_failures_are_counted_with_fixed_codes(self):
        watch, clock, _ = self.watch([RuntimeError("boom secret"), ProductAPIError("product_draft_mismatch"), draft("running")])
        watch.poll()
        watch.poll()
        watch.poll()
        record = watch.record()
        self.assertEqual(record["window_read_failures"], 2)
        self.assertEqual(record["window_last_error"], "product_draft_mismatch")  # 제품 구동기의 고정 code 그대로
        self.assertNotIn("secret", str(record))
        self.assertIsNone(record["first_terminal"])
        self.assertEqual(record["final"]["state"], "running")

    def test_non_terminal_states_never_set_first_terminal(self):
        watch, _, _ = self.watch([draft("preparing"), draft("stopping"), draft("running")])
        for _ in range(4):
            watch.poll()
        self.assertIsNone(watch.record()["first_terminal"])
        for state in ("failed", "cancelled", "unknown"):
            with self.subTest(state=state):
                watch, _, _ = self.watch([draft(state, reason_code="target_dns_mismatch")])
                watch.poll()
                watch.poll()
                self.assertEqual(watch.record()["first_terminal"]["status"], state)


class TestFirstLimit(unittest.TestCase):
    def test_product_budget_closing_before_runner_limit(self):
        terminal = {"status": "completed", "elapsed_seconds": 50.0, "closing_reason": "budget_limited", "reason_code": None}
        self.assertEqual(first_limit("time_budget", 600.0, 600.0, terminal)["value"], "product_limit:budget_limited")
        self.assertEqual(first_limit("request_budget", 40.0, 600.0, terminal)["value"], "runner_request_budget")  # 러너가 먼저

    def test_other_closings_and_b_terminal_stay_engine_completed(self):
        for closing in (None, "complete", "BUDGET_LIMITED", "budget_limited "):
            with self.subTest(closing=closing):
                terminal = {"status": "completed", "elapsed_seconds": 50.0, "closing_reason": closing}
                self.assertEqual(first_limit("time_budget", 600.0, 600.0, terminal)["value"], FIRST_LIMIT_ENGINE)
        b_terminal = {"status": "done", "at": "t", "elapsed_seconds": 50.0}
        self.assertEqual(first_limit("time_budget", 600.0, 600.0, b_terminal)["value"], FIRST_LIMIT_ENGINE)
        self.assertEqual(first_limit("time_budget", 600.0, 600.0, None)["value"], "runner_time_budget")


if __name__ == "__main__":
    unittest.main()
