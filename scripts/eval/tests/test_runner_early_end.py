"""러너 상한 전에 일찍 끝난 회차의 분류(평가 프로토콜 §5.1·고정 초안 §26, 2026-11-21 사용자 결정) 시험."""

from __future__ import annotations

import dataclasses
import json
import os
import sys
import unittest
from datetime import timedelta

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from official_session import Outcome, Schedule, replay  # noqa: E402
from runner_early_end import (  # noqa: E402
    CLASS_CONFIG, CLASS_ENDED_VALID, CLASS_PROVIDER_FAULT, CONFIG_END_REASON, PROVIDER_FAULT_END_REASON, approval_budget,
    early_end_record, provider_error_kind, start_refusal_code,
)
from runner_product import ProductAPIError  # noqa: E402
from runner_product_llm import ProductLLMError, ledger_totals  # noqa: E402
import session_runs  # noqa: E402
import test_runner as b_harness  # noqa: E402
import test_runner_split_execute as o_harness  # noqa: E402

T0 = "2026-11-21T00:00:"


def at(second: float) -> str:
    return f"{T0}{second:06.3f}Z"


def row(seq, second, decision="forwarded", status=200, end=None):
    value = {"seq": seq, "decision": decision, "method": "POST", "status": status, "started_at": at(second - 0.5),
             "completed_at": at(second), "request_bytes": 10, "response_bytes": 5, "generation_id": f"g{seq}"}
    if end:
        value["unfinished_end"] = end
    return value


def ended(status, second=30.0, reason_code=None):
    return {"value": f"engine_ended:{status}"}, {"status": status, "at": at(second), "elapsed_seconds": second,
                                                   "reason_code": reason_code}


class TestProviderErrorKind(unittest.TestCase):
    def test_kinds(self):
        cases = [(row(1, 1, "upstream_error", 200, "upstream_cut_before_done"), "upstream_cut"),
                 (row(1, 1, "upstream_error", 200, "upstream_cut_after_done"), "upstream_cut"),
                 (row(1, 1, status=429), "rate_limited"), (row(1, 1, "upstream_error", 503), "server_error"),
                 (row(1, 1, status=500), "server_error"), (row(1, 1, status=599), "server_error"),
                 (row(1, 1), None), (row(1, 1, status=400), None), (row(1, 1, status=600), None),
                 # 끊김은 위쪽 오류 행에만 있다(완료된 forwarded 행에 끝 모양이 붙어도 끊김이 아니다, runner_fault와 같은 규칙)
                 (row(1, 1, "forwarded", 200, "upstream_cut_before_done"), None),
                 # 호출자가 먼저 닫은 행·봉인 때 게이트웨이가 끊은 행은 제공자 오류가 아니다
                 (row(1, 1, "upstream_error", 200, "client_closed_before_done"), None),
                 (row(1, 1, "cancelled_by_seal", 429), None), (row(1, 1, "rejected_sampling", 503), None)]
        for value, expected in cases:
            with self.subTest(value=value):
                self.assertEqual(provider_error_kind(value), expected)


class TestEarlyEndRecord(unittest.TestCase):
    def classify(self, side, status, rows, **kwargs):
        limit, terminal = ended(status, **kwargs)
        return early_end_record(side, limit, terminal, rows)

    def test_o_failed_or_unknown_after_provider_error_is_provider_fault(self):
        for status in ("failed", "unknown"):
            for last in (row(2, 20, status=503), row(2, 20, status=429), row(2, 20, "upstream_error", 200, "upstream_cut_before_done")):
                with self.subTest(status=status, last=last):
                    record = self.classify("ours", status, [row(1, 10), last], reason_code="planning_failed")
                    self.assertEqual((record["class"], record["reason"]), (CLASS_PROVIDER_FAULT, PROVIDER_FAULT_END_REASON))
                    self.assertEqual((record["end_state"], record["reason_code"]), (status, "planning_failed"))
                    self.assertEqual(record["evidence"]["call"]["seq"], 2)
                    self.assertFalse(record["evidence"]["success_after"])

    def test_b_only_failed_counts(self):
        record = self.classify("baseline", "failed", [row(1, 10, status=429)])
        self.assertEqual(record["class"], CLASS_PROVIDER_FAULT)
        record = self.classify("baseline", "unknown", [row(1, 10, status=429)])
        self.assertEqual((record["class"], record["reason"], record["evidence"]["kind"]), (CLASS_ENDED_VALID, None, "end_state_not_fault"))

    def test_other_end_states_stay_valid(self):
        for status in ("cancelled", "done", "timeout"):
            with self.subTest(status=status):
                record = self.classify("ours", status, [row(1, 10, status=503)])
                self.assertEqual((record["class"], record["reason"]), (CLASS_ENDED_VALID, None))
                self.assertEqual(record["first_limit"], f"engine_ended:{status}")

    def test_success_after_the_error_keeps_it_valid(self):
        # 오류 뒤 성공한 호출이 있으면(끝 뒤에 완료된 것까지) 제공자 장애로 끝났다고 하지 않는다
        for success in (row(3, 25), row(3, 40)):
            with self.subTest(success=success):
                rows = [row(1, 10), row(2, 20, status=503), success]
                limit, terminal = ended("failed", second=30.0 if success["seq"] == 3 and success["completed_at"] == at(25) else 30.0)
                record = early_end_record("ours", limit, terminal, rows)
                self.assertEqual(record["class"], CLASS_ENDED_VALID)

    def test_last_call_before_the_end_decides(self):
        # 앞선 오류가 있어도 끝 직전 마지막 호출이 성공이면 유효다
        record = self.classify("ours", "failed", [row(1, 10, status=503), row(2, 20)])
        self.assertEqual((record["class"], record["evidence"]["kind"]), (CLASS_ENDED_VALID, "last_call_not_provider_error"))
        # 끝 뒤에 완료된 오류 호출은 '끝 직전 마지막 호출'이 아니다
        record = self.classify("ours", "failed", [row(1, 10), row(2, 40, status=503)])
        self.assertEqual(record["class"], CLASS_ENDED_VALID)
        # 끝 전에 호출이 없으면 근거가 없다
        record = self.classify("ours", "failed", [row(2, 40, status=503)])
        self.assertEqual((record["class"], record["evidence"]["kind"]), (CLASS_ENDED_VALID, "no_call_before_end"))
        # 같은 시각에 끝난 호출은 끝 직전으로 본다
        record = self.classify("ours", "failed", [row(1, 30, status=503)])
        self.assertEqual(record["class"], CLASS_PROVIDER_FAULT)

    def test_no_or_unreadable_records_never_invalidate(self):
        limit, terminal = ended("failed")
        record = early_end_record("ours", limit, terminal, None)
        self.assertEqual((record["class"], record["reason"], record["evidence"]["kind"]), (CLASS_ENDED_VALID, None, "no_call_records"))
        record = early_end_record("ours", limit, terminal, [row(1, 10, status=503)], rows_error="call_records_unreadable")
        self.assertEqual((record["class"], record["evidence"]["kind"]), (CLASS_ENDED_VALID, "call_records_unreadable"))
        record = early_end_record("ours", limit, terminal, [{"seq": 1, "decision": "forwarded", "status": 503}])
        self.assertEqual((record["class"], record["evidence"]["kind"]), (CLASS_ENDED_VALID, "call_records_unreadable"))
        # 일찍 끝나지 않은 회차는 분류가 없고 읽기 실패만 남는다
        record = early_end_record("ours", {"value": "runner_time_budget"}, None, None, rows_error="call_records_unreadable")
        self.assertEqual((record["class"], record["evidence"]), (None, {"kind": "call_records_unreadable"}))

    def test_not_early_is_unclassified(self):
        for value in ("engine_completed", "runner_request_budget", "runner_time_budget", "product_limit:budget_limited"):
            with self.subTest(value=value):
                record = early_end_record("ours", {"value": value}, None, [row(1, 10, status=503)])
                self.assertEqual((record["class"], record["reason"], record["evidence"]), (None, None, None))

    def test_auth_statuses_are_config_anywhere(self):
        for status in (401, 402, 403, 404):
            for limit in ({"value": "runner_time_budget"}, {"value": "engine_ended:unknown"}):
                with self.subTest(status=status, limit=limit):
                    record = early_end_record("ours", limit, ended("unknown")[1], [row(1, 10), row(2, 20, status=status)])
                    self.assertEqual((record["class"], record["reason"]), (CLASS_CONFIG, CONFIG_END_REASON))
                    self.assertEqual((record["evidence"]["kind"], record["evidence"]["call"]["status"]), ("model_auth_status", status))
                    self.assertIs(record["evidence"]["moderation_possible"], status == 403)  # 403만 검열 가능 표시
        for value in (row(1, 10, status=400), row(1, 10, status=405), row(1, 10, "cancelled_by_seal", 401)):
            with self.subTest(value=value):
                record = early_end_record("ours", {"value": "runner_time_budget"}, None, [value])
                self.assertIsNone(record["class"])

    def test_approval_budget_reached_is_config_only_on_budget_limited(self):
        reached = {"reached": True, "requests": 10, "max_requests": 10}
        record = early_end_record("ours", {"value": "product_limit:budget_limited"}, None, [row(1, 10)], reached)
        self.assertEqual((record["class"], record["reason"], record["evidence"]["kind"]), (CLASS_CONFIG, CONFIG_END_REASON, "approval_budget"))
        record = early_end_record("ours", {"value": "product_limit:budget_limited"}, None, [row(1, 10)], dict(reached, reached=False))
        self.assertIsNone(record["class"])
        record = early_end_record("ours", {"value": "runner_time_budget"}, None, [row(1, 10)], reached)
        self.assertIsNone(record["class"])


class TestApprovalBudget(unittest.TestCase):
    def totals(self, requests, cost, largest):
        return {"requests": requests, "cost": cost, "largest_reservation_cost": largest}

    def test_requests_or_cost_reach(self):
        self.assertTrue(approval_budget(self.totals(10, 0, 5), 10, 1000)["reached"])
        self.assertFalse(approval_budget(self.totals(9, 0, 5), 10, 1000)["reached"])
        # 남은 비용이 가장 큰 예약보다 작으면 같은 크기의 다음 호출이 들어가지 않는다
        self.assertTrue(approval_budget(self.totals(1, 996, 5), 10, 1000)["reached"])
        self.assertFalse(approval_budget(self.totals(1, 995, 5), 10, 1000)["reached"])
        self.assertFalse(approval_budget(self.totals(0, 0, 0), 10, 1000)["reached"])
        self.assertIsNone(approval_budget(None, 10, 1000))
        self.assertIsNone(approval_budget(self.totals(1, 1, 1), None, None))

    def test_ledger_totals_follow_the_settle_lines(self):
        self.assertEqual(ledger_totals(b""), {"requests": 0, "cost": 0, "largest_reservation_cost": 0})
        data = b"r1 10 20 30\nr1 10 20 50\ns1 10 20 30 5 6 7\n"
        self.assertEqual(ledger_totals(data), {"requests": 2, "cost": 30 + 50 - (30 - 7), "largest_reservation_cost": 50})
        for bad in (b"r1 10 20\n", b"x\n", b"s1 10 20 30 5 6 31\n", b"r1 10 20 30"):
            with self.subTest(bad=bad), self.assertRaises(ProductLLMError):
                ledger_totals(bad)


class TestStartRefusal(unittest.TestCase):
    def test_codes(self):
        cases = [("product_rejected:/api/product/workflows/drafts/x/start:approval_required", "approval_required"),
                 ("product_http_409:/api/product/workflows/drafts/x/start:approval_changed", "approval_changed"),
                 ("product_draft_not_ready:approval_expired", "approval_expired"),
                 ("product_draft_not_ready:target_not_approved", "target_not_approved"),
                 ("product_http_503:/api/product/workflows/drafts/x/start:busy", None),
                 ("product_draft_not_ready", None), ("product_unreachable:start", None), ("", None)]
        for code, expected in cases:
            with self.subTest(code=code):
                self.assertEqual(start_refusal_code(code), expected)


# ── O 회차 흐름 ─────────────────────────────────────────────────────────────────────────────────────────────
def o_round(status, rows, reason_code="planning_failed", **spec_changes):
    """시작 직후 계획된 제품 호출 기록과 끝 상태로 O 회차 하나를 돌린다. 장부 예약 줄 수는 호출 줄 수와 맞춘다."""
    def mutate(ports):
        ports.statuses = [{"id": "draft-1", "state": "running"},
                          {"id": "draft-1", "state": status, "reason_code": reason_code}]
        ports.ledger = b"r1 10 20 30\n" * len(rows)
        start_round = ports.start_round

        def start(prepared):
            base = o_harness.START + timedelta(seconds=ports.clock.t)
            stamp = lambda s: (base + timedelta(seconds=s)).strftime("%Y-%m-%dT%H:%M:%S.%fZ")  # noqa: E731
            ports.calls_file = b"".join(json.dumps(dict(r, started_at=stamp(r["s"] - 0.4), completed_at=stamp(r["s"])),
                                                   ).encode() + b"\n" for r in rows)
            return start_round(prepared)
        ports.start_round = start
    return o_harness.run(mutate, **spec_changes)


def call(seq, s, status=200, decision="forwarded", end=None):
    value = {"seq": seq, "decision": decision, "method": "POST", "status": status, "s": s, "request_bytes": 1, "response_bytes": 1,
             "generation_id": ""}
    if end:
        value["unfinished_end"] = end
    return value


class TestORound(unittest.TestCase):
    def test_provider_error_before_failed_end_is_invalid(self):
        result, _, evidence = o_round("failed", [call(1, 0.2), call(2, 0.3, status=503)])
        self.assertEqual((result["status"], result["reasons"][0]), ("invalid", PROVIDER_FAULT_END_REASON), result)
        record = evidence.files["early-end.json"]
        self.assertEqual((record["class"], record["end_state"], record["reason_code"]), (CLASS_PROVIDER_FAULT, "failed", "planning_failed"))
        self.assertEqual((record["evidence"]["kind"], record["evidence"]["call"]["seq"]), ("server_error", 2))

    def test_other_early_end_stays_valid_with_the_real_end_state(self):
        result, _, evidence = o_round("failed", [call(1, 0.2), call(2, 0.3)], reason_code="workflow_failed")
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual(result["first_limit"]["value"], "engine_ended:failed")
        self.assertEqual(result["early_end"], evidence.files["early-end.json"])
        self.assertEqual((result["early_end"]["class"], result["early_end"]["reason_code"]), (CLASS_ENDED_VALID, "workflow_failed"))

    def test_auth_status_is_a_config_end(self):
        result, _, evidence = o_round("unknown", [call(1, 0.2), call(2, 0.3, status=401)], reason_code="model_usage_unknown")
        self.assertEqual((result["status"], result["reasons"][0]), ("invalid", CONFIG_END_REASON), result)
        self.assertEqual(evidence.files["early-end.json"]["evidence"]["kind"], "model_auth_status")

    def test_approval_budget_end_is_a_config_end(self):
        def budget(ports):
            ports.statuses = [{"id": "draft-1", "state": "running"},
                              {"id": "draft-1", "state": "completed", "closing_reason": "budget_limited", "outcome": "partial"}]
        result, _, evidence = o_harness.run(budget, approval_caps=(2, 1000))  # 장부 예약 2줄 = 요청 상한
        self.assertEqual((result["status"], result["reasons"][0]), ("invalid", CONFIG_END_REASON), result)
        self.assertEqual(evidence.files["early-end.json"]["evidence"]["kind"], "approval_budget")
        result, _, _ = o_harness.run(budget, approval_caps=(3, 1000))  # 상한에 안 닿음 = 제품 예산 마감(통제 비용)
        self.assertEqual(result["status"], "complete", result)
        self.assertIsNone(result["early_end"]["class"])

    def test_start_refusal_is_a_config_end(self):
        def refuse(ports):
            def start(prepared):
                raise ProductAPIError("product_rejected:/api/product/workflows/drafts/draft-1/start:approval_required")
            ports.start_round = start
        result, ports, evidence = o_harness.run(refuse)
        self.assertEqual((result["status"], result["reasons"]), ("invalid", [CONFIG_END_REASON, "start_refused:approval_required"]))
        self.assertEqual(evidence.files["early-end.json"]["evidence"], {"kind": "start_refused", "code": "approval_required"})
        self.assertIn("down", ports.calls)

        def busy(ports):
            def start(prepared):
                raise ProductAPIError("product_http_503:/api/product/workflows/drafts/draft-1/start:busy")
            ports.start_round = start
        result, _, evidence = o_harness.run(busy)
        self.assertEqual(result["reasons"], ["product_start_failed"])
        self.assertNotIn("early-end.json", evidence.files)

    def test_synthetic_round_records_the_end_without_call_records(self):
        def failed(ports):
            ports.statuses = [{"id": "draft-1", "state": "running"}, {"id": "draft-1", "state": "failed", "reason_code": "planning_failed"}]
        result, _, evidence = o_harness.run(failed, synthetic=True)
        self.assertEqual(result["first_limit"]["value"], "engine_ended:failed")
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual((evidence.files["early-end.json"]["class"], evidence.files["early-end.json"]["evidence"]["kind"]),
                         (CLASS_ENDED_VALID, "no_call_records"))


# ── B 회차 흐름 ─────────────────────────────────────────────────────────────────────────────────────────────
def b_round(view_status, rows):
    holder = {}

    def setup(ports, meter, clock):
        ports.task_views = [b_harness.task_view("running"), b_harness.task_view(view_status)]

        def started():
            base = b_harness.datetime(2026, 11, 2, tzinfo=b_harness.timezone.utc) + timedelta(seconds=clock.t)
            stamp = lambda s: (base + timedelta(seconds=s)).strftime("%Y-%m-%dT%H:%M:%S.%f000Z")  # noqa: E731
            holder["gateway"].llm_rows = [dict({k: v for k, v in r.items() if k != "s"}, started_at=stamp(r["s"] - 0.4),
                                               completed_at=stamp(r["s"])) for r in rows]
        ports.on_start = started
    from test_runner_checks import PROJECT
    return b_harness.run(setup, mutate_gateway=lambda g: holder.__setitem__("gateway", g),
                         spec=dataclasses.replace(b_harness.make_spec(side="baseline"), project=PROJECT))


class TestBRound(unittest.TestCase):
    def test_failed_after_rate_limit_is_invalid(self):
        result, _, _, evidence = b_round("failed", [call(1, 0.2), call(2, 0.3, status=429)])
        self.assertEqual((result["status"], result["reasons"][0]), (b_harness.INVALID, PROVIDER_FAULT_END_REASON), result)
        self.assertEqual(evidence.files["early-end.json"]["class"], CLASS_PROVIDER_FAULT)

    def test_auth_status_is_a_config_end_first(self):
        result, _, _, evidence = b_round("failed", [call(1, 0.2), call(2, 0.3, status=401)])
        self.assertEqual((result["status"], result["reasons"][0]), (b_harness.INVALID, CONFIG_END_REASON), result)
        self.assertEqual(evidence.files["early-end.json"]["evidence"]["kind"], "model_auth_status")

    def test_failed_after_success_stays_valid(self):
        result, _, _, evidence = b_round("failed", [call(1, 0.2), call(2, 0.3)])
        self.assertEqual(result["status"], b_harness.COMPLETE, result)
        self.assertEqual((result["first_limit"]["value"], result["early_end"]["class"]), ("engine_ended:failed", CLASS_ENDED_VALID))


# ── 세션 ─────────────────────────────────────────────────────────────────────────────────────────────────
class TestSession(unittest.TestCase):
    def test_config_end_is_not_an_attempt_and_stops(self):
        schedule = Schedule(3)
        schedule.record("baseline", Outcome("complete", run_id="b1"))
        early = {"class": CLASS_CONFIG, "reason": CONFIG_END_REASON, "end_state": "unknown"}
        schedule.record("ours", Outcome("invalid", [CONFIG_END_REASON], "o1", early_end=early))
        self.assertEqual((schedule.attempts["ours"], schedule.stop_reason), (0, "config_end:ours"))
        self.assertEqual(schedule.summary()["early_end_runs"], [dict(early, run_id="o1", side="O", counted=False)])

    def test_resume_skips_config_end_lines(self):
        lines = [{"event": "attempt", "side": "B", "repeat": 0, "status": "complete", "reasons": [], "run_id": "b1"},
                 {"event": "attempt", "side": "O", "repeat": 0, "status": "invalid", "reasons": [CONFIG_END_REASON], "run_id": "o1",
                  "early_end": {"class": CLASS_CONFIG}},
                 {"event": "attempt", "side": "O", "repeat": 0, "status": "complete", "reasons": [], "run_id": "o2"}]
        schedule = Schedule(3)
        self.assertEqual(replay(schedule, lines), 2)
        self.assertEqual((schedule.attempts, schedule.stop_reason), ({"baseline": 1, "ours": 1}, None))
        self.assertEqual([item["run_id"] for item in schedule.early_end_runs], ["o1"])

    def test_provider_fault_end_counts_with_stalls(self):
        schedule = Schedule(3)
        schedule.record("baseline", Outcome("invalid", [PROVIDER_FAULT_END_REASON], "b1", early_end={"class": CLASS_PROVIDER_FAULT}))
        self.assertIsNone(schedule.stop_reason)
        schedule.record("baseline", Outcome("invalid", ["provider_fault_stall"], "b2"))
        self.assertEqual((schedule.stop_reason, schedule.failed, schedule.attempts["baseline"]), ("fault_stall_limit:baseline", True, 2))

    def test_session_runs_tables(self):
        def attempt(run_id, side, status, reason, early=None):
            return session_runs.Attempt(run_id, side, b_harness.datetime(2026, 11, 21, tzinfo=b_harness.timezone.utc), status, reason,
                                        None, early_end=early)
        attempts = [attempt("baseline-1", "baseline", "invalid", PROVIDER_FAULT_END_REASON,
                            {"class": CLASS_PROVIDER_FAULT, "reason": PROVIDER_FAULT_END_REASON, "first_limit": "engine_ended:failed",
                             "end_state": "failed", "reason_code": None,
                             "evidence": {"kind": "rate_limited", "call": {"seq": 2, "decision": "forwarded", "status": 429,
                                                                          "completed_at": "t"}}}),
                    attempt("baseline-2", "baseline", "invalid", "provider_fault_stall"),
                    attempt("ours-1", "ours", "invalid", CONFIG_END_REASON,
                            {"class": CLASS_CONFIG, "reason": CONFIG_END_REASON, "first_limit": None, "end_state": None,
                             "reason_code": "approval_required", "evidence": {"kind": "start_refused", "code": "approval_required"}})]
        lines, failures = session_runs.fault_stall_tally(attempts)
        self.assertIn("| baseline | 1 | 1 | 2 | 2 |", lines)
        self.assertEqual(len(failures), 1)
        listed = session_runs.early_end_list(attempts)
        self.assertEqual(len(listed), 4)
        self.assertIn("rate_limited #2 forwarded 429 t", listed[2])
        self.assertIn("start_refused approval_required", listed[3])
        moderated = dataclasses.replace(attempts[0], early_end=dict(attempts[0].early_end, evidence={
            "kind": "model_auth_status", "moderation_possible": True,
            "call": {"seq": 3, "decision": "forwarded", "status": 403, "completed_at": "t"}}))
        self.assertIn("(검열 가능: 403)", session_runs.early_end_list([moderated])[2])
        self.assertEqual(session_runs.early_end_list(attempts[1:2]), ["없음"])
        tally_lines, warnings = session_runs.tally(attempts, 3)
        self.assertIn("| ours | — | 0 | 0 | 0 | — | — |", tally_lines)  # 설정 오류는 시도가 아니다
        self.assertTrue(any(w.startswith("경고[설정 오류] ours") for w in warnings))


if __name__ == "__main__":
    unittest.main()
