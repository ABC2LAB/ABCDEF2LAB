"""시도별 대조(평가 프로토콜 §8.3): E² 결정 행 × 계측기 접수 행 → 다섯 칸."""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from attempt_reconcile import (  # noqa: E402
    ALLOWED_DECISIONS, CELLS, DENIED_DECISIONS, ReconcileError, reconcile,
)
from runner_evidence import ATTEMPT_STATES, E2_STUB_DECISIONS  # noqa: E402

A, B, C, D = ("a" * 32, "b" * 32, "c" * 32, "d" * 32)


def decision(attempt, kind="allowed"):
    return {"decision": kind, "attempt_id": attempt}


def seen(attempt=None, state=None):
    state = state or ("valid" if attempt else "absent")
    return {"attempt_id_state": state, "attempt_id": attempt or ""}


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
