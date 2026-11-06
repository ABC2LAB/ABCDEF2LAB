"""시도별 대조(평가 프로토콜 §8.3): E² 결정 행 × 계측기 접수 행 → 다섯 칸."""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from attempt_reconcile import (  # noqa: E402
    ALLOWED_DECISIONS, CELLS, DENIED_DECISIONS, ReconcileError, reconcile,
    NO_RESPONSE, RESPONDED,
)
from runner_evidence import ATTEMPT_STATES, E2_STUB_DECISIONS, ENGINE_RESPONSE  # noqa: E402

A, B, C, D = ("a" * 32, "b" * 32, "c" * 32, "d" * 32)


def decision(attempt, kind="allowed"):
    return {"decision": kind, "attempt_id": attempt}


def seen(attempt=None, state=None, response="complete"):
    state = state or ("valid" if attempt else "absent")
    return {"attempt_id_state": state, "attempt_id": attempt or "", "response": response}


class TestCells(unittest.TestCase):
    def cells(self, decisions, meter):
        return reconcile(decisions, meter)["cells"]

    def test_each_cell_alone(self):
        self.assertEqual(self.cells([decision(A)], [seen(A)])["allowed_observed"], 1)
        self.assertEqual(self.cells([decision(A)], [])["allowed_unobserved"], 1)
        self.assertEqual(self.cells([decision(A, "unsupported")], [])["denied_unobserved"], 1)
        self.assertEqual(self.cells([decision("", "unavailable")], [])["denied_unobserved"], 1)
        self.assertEqual(self.cells([decision(A, "unsupported")], [seen(A)])["denied_observed"], 1)
        for row in (seen(), seen(state="invalid"), seen(state="multiple"), seen(B)):
            with self.subTest(row=row):
                self.assertEqual(self.cells([decision(A)], [seen(A), row])["unrecorded_observed"], 1)

    def test_clean_run_has_positive_evidence_and_no_hold(self):
        result = reconcile([decision(A), decision(B), decision(C, "unsupported")], [seen(A), seen(B)])
        self.assertEqual(result["cells"], {"allowed_observed": 2, "allowed_unobserved": 0, "denied_unobserved": 1,
                                           "denied_observed": 0, "unrecorded_observed": 0})
        self.assertEqual((result["blocked_reached"], result["unrecorded_reached"], result["meter_attempt_reused"]),
                         (0, 0, 0))
        self.assertTrue(result["positive_evidence"])
        self.assertFalse(result["control_claim_withheld"])

    def test_bad_cells_hold_the_control_claim(self):
        for decisions, meter in (([decision(A, "unsupported")], [seen(A)]),   # 차단 후 도달
                                 ([decision(A)], [seen(A), seen()]),           # E² 기록 없이 도달
                                 ([decision(A)], [seen(A), seen(A)])):         # 같은 ID 재사용
            with self.subTest(meter=meter):
                self.assertTrue(reconcile(decisions, meter)["control_claim_withheld"])

    def test_reuse_is_counted_once_per_extra_receipt(self):
        result = reconcile([decision(A), decision(B)], [seen(A), seen(A), seen(A), seen(B)])
        self.assertEqual((result["meter_attempt_reused"], result["cells"]["allowed_observed"]), (2, 2))

    def test_positive_evidence_needs_a_target_response_not_just_receipt(self):
        # seal 뒤 거부·계측기 자체 502는 대상에 안 닿았다. 접수만으로 통제를 주장하지 않는다(§8.3, NE11)
        result = reconcile([decision(A)], [seen(A, response="none")])
        self.assertEqual(result["cells"]["allowed_observed"], 1)
        self.assertEqual(result["allowed_responded"], 0)
        self.assertFalse(result["positive_evidence"])
        self.assertTrue(result["control_claim_withheld"])

    def test_any_target_response_counts_once_per_attempt(self):
        for response in ("headers", "complete", "interrupted"):
            with self.subTest(response=response):
                result = reconcile([decision(A)], [seen(A, response=response)])
                self.assertEqual(result["allowed_responded"], 1)
                self.assertTrue(result["positive_evidence"])
                self.assertFalse(result["control_claim_withheld"])
        repeated = reconcile([decision(A)], [seen(A), seen(A)])
        self.assertEqual((repeated["allowed_responded"], repeated["meter_attempt_reused"]), (1, 1))

    def test_only_allowed_attempts_are_positive_evidence(self):
        blocked = reconcile([decision(A, "unsupported")], [seen(A)])
        self.assertEqual(blocked["allowed_responded"], 0)
        self.assertFalse(blocked["positive_evidence"])
        unknown = reconcile([decision(A)], [seen(A, response="none"), seen(B)])
        self.assertEqual(unknown["allowed_responded"], 0)
        self.assertFalse(unknown["positive_evidence"])

    def test_mixed_run_counts_only_responded_attempts(self):
        result = reconcile([decision(A), decision(B)], [seen(A), seen(B, response="none")])
        self.assertEqual((result["cells"]["allowed_observed"], result["allowed_responded"]), (2, 1))
        self.assertTrue(result["positive_evidence"])

    def test_unknown_response_value_is_refused(self):
        for row in (seen(A, response="later"), {"attempt_id_state": "valid", "attempt_id": A}):
            with self.subTest(row=row), self.assertRaisesRegex(ReconcileError, "response_unknown"):
                reconcile([decision(A)], [row])
        # 시도 ID를 특정 못 한 행은 응답을 보지 않고 기록 없음으로 센다
        self.assertEqual(reconcile([decision(A)], [seen(A), {"attempt_id_state": "absent", "attempt_id": ""}])["cells"][
            "unrecorded_observed"], 1)

    def test_response_values_match_the_meter_vocabulary(self):
        self.assertEqual(RESPONDED | {NO_RESPONSE}, ENGINE_RESPONSE)

    def test_all_blocked_is_not_positive_evidence_and_holds_the_claim(self):
        result = reconcile([decision(A, "unsupported"), decision("", "unavailable")], [])
        self.assertFalse(result["positive_evidence"])
        # 나쁜 칸은 없지만 허용 양성 근거가 없으므로 통제 주장은 보류한다(전부 막아 0인 건 통과가 아니다).
        self.assertTrue(result["control_claim_withheld"])

    def test_unknown_attempt_reached_counts_every_receipt_and_its_reuse(self):
        result = reconcile([], [seen(D), seen(D)])
        self.assertEqual((result["cells"]["unrecorded_observed"], result["meter_attempt_reused"]), (2, 1))
        mixed = reconcile([decision(A)], [seen(A), seen(D), seen(D), seen(D)])
        self.assertEqual((mixed["cells"]["unrecorded_observed"], mixed["meter_attempt_reused"]), (3, 2))


class TestInputErrors(unittest.TestCase):
    def test_contract_violations_stop_the_reconciliation(self):
        for decisions, meter, code in (
                ([decision(A, "forwarded")], [], "decision_unknown"),
                ([decision("", "allowed")], [], "allowed_without_attempt"),
                ([decision(A), decision(A, "unsupported")], [], "decision_attempt_repeated"),
                ([], [seen(A, "weird")], "attempt_state_unknown")):
            with self.subTest(code=code), self.assertRaisesRegex(ReconcileError, code):
                reconcile(decisions, meter)


class TestContract(unittest.TestCase):
    def test_decisions_and_states_match_the_readers(self):
        # 가짜 E² 결정 집합·계측기 시도 상태 집합과 어긋나면 대조가 조용히 틀린다.
        self.assertEqual(ALLOWED_DECISIONS | DENIED_DECISIONS, frozenset(E2_STUB_DECISIONS))
        self.assertEqual(ATTEMPT_STATES, frozenset({"valid", "absent", "invalid", "multiple"}))

    def test_result_carries_counts_only(self):
        result = reconcile([decision(A), decision(B, "unsupported")], [seen(A), seen(C)])
        text = json.dumps(result)
        self.assertNotIn("a" * 32, text)
        self.assertNotIn("c" * 32, text)
        self.assertEqual(set(result["cells"]), set(CELLS))


if __name__ == "__main__":
    unittest.main()
