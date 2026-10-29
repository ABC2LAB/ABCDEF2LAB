"""evidence — 증거 3조건과 판정 단위(ANY) 테스트(프로토콜 §4-2).

traffic 인덱스·GT 상관은 메모리 합성 값. 실제 traffic 기록은 쓰지 않는다(절대규칙 2).

실행: scripts/eval 에서  python3 -I -m unittest discover -s tests
"""

from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from aggregate import ScanVerdict  # noqa: E402
from collect import VerdictRecord  # noqa: E402
from correlation import CorrelationTable, Rule  # noqa: E402
from evidence import (  # noqa: E402
    challenge_has_evidence,
    dangling_refs,
    has_evidence_refs,
    record_has_evidence,
    ref_resolves,
    ref_targets_challenge,
)

_BASKET = "basketAccessChallenge"
_FEEDBACK = "forgedFeedbackChallenge"

# 합성 상관: basket은 vuln_class 무관, feedback은 vuln_class=idor일 때만.
_CORRELATE = CorrelationTable((
    Rule(challenge=_BASKET, axis="horizontal", vuln_class=None, types=("rest:basket",)),
    Rule(challenge=_FEEDBACK, axis="horizontal", vuln_class="idor", types=("rest:feedback",)),
)).correlate

# 합성 traffic 인덱스: traffic id → 요청 대상 resource_ref.
_TRAFFIC = {
    "t-basket-owner": "rest:basket:1",
    "t-basket-cross": "rest:basket:1",
    "t-login": "rest:user:login",
    "t-feedback": "rest:feedback:3",
}


def _confirmed(*refs: str, resource_ref: str = "rest:basket:1", vuln_class: str = "idor") -> VerdictRecord:
    return VerdictRecord(
        oracle_value="confirmed",
        vuln_verdict="confirmed",
        vuln_class=vuln_class,
        resource_ref=resource_ref,
        axis="horizontal",
        scan_verdict=ScanVerdict.CONFIRMED,
        traffic_evidence_refs=refs,
    )


class TestThreeConditions(unittest.TestCase):
    def test_a_refs_present(self) -> None:
        self.assertFalse(has_evidence_refs(_confirmed()))
        self.assertTrue(has_evidence_refs(_confirmed("t-basket-cross")))

    def test_b_ref_resolves_in_traffic(self) -> None:
        self.assertTrue(ref_resolves("t-basket-cross", _TRAFFIC))
        self.assertFalse(ref_resolves("t-gone", _TRAFFIC))
        self.assertFalse(ref_resolves("t-basket-cross", None))  # 인덱스 없음 = 확인 불가

    def test_c_ref_targets_challenge_resource(self) -> None:
        rec = _confirmed("t-basket-cross")
        self.assertTrue(ref_targets_challenge("t-basket-cross", rec, _BASKET, _TRAFFIC, _CORRELATE))
        self.assertFalse(ref_targets_challenge("t-login", rec, _BASKET, _TRAFFIC, _CORRELATE))  # 다른 자원
        self.assertFalse(ref_targets_challenge("t-gone", rec, _BASKET, _TRAFFIC, _CORRELATE))  # 끊긴 ref
        self.assertFalse(ref_targets_challenge("t-basket-cross", rec, _BASKET, None, _CORRELATE))

    def test_c_uses_same_correlation_including_vuln_class(self) -> None:
        # 요청 대상은 feedback이지만 레코드 vuln_class가 규칙과 다르면 같은 상관 기준에서 안 맞는다.
        rec = _confirmed("t-feedback", resource_ref="rest:feedback:3", vuln_class="xss")
        self.assertFalse(ref_targets_challenge("t-feedback", rec, _FEEDBACK, _TRAFFIC, _CORRELATE))
        ok = _confirmed("t-feedback", resource_ref="rest:feedback:3")
        self.assertTrue(ref_targets_challenge("t-feedback", ok, _FEEDBACK, _TRAFFIC, _CORRELATE))


class TestRecordUnit(unittest.TestCase):
    """레코드 증거 = (b)·(c)를 함께 만족하는 ref ≥ 1 (ALL 아님)."""

    def test_one_solid_ref_among_noise_is_enough(self) -> None:
        verbose = _confirmed("t-gone", "t-login", "t-basket-owner", "t-basket-cross")
        self.assertTrue(record_has_evidence(verbose, _BASKET, _TRAFFIC, _CORRELATE))

    def test_no_solid_ref_is_not_evidence(self) -> None:
        # 끊긴 ref·다른 자원 ref만 있으면 아무리 많아도 미충족 — 스프레이는 (c)가 막는다.
        noise = _confirmed("t-gone", "t-login", "t-gone-2")
        self.assertFalse(record_has_evidence(noise, _BASKET, _TRAFFIC, _CORRELATE))

    def test_resolved_but_off_target_and_target_but_unresolved_dont_combine(self) -> None:
        # (b)를 만족하는 ref와 (c)를 만족할 ref가 따로 있어도, 한 ref가 둘 다여야 한다.
        split = _confirmed("t-login", "t-missing-basket")
        traffic = dict(_TRAFFIC)  # t-missing-basket은 인덱스에 없음
        self.assertFalse(record_has_evidence(split, _BASKET, traffic, _CORRELATE))

    def test_empty_refs_or_no_index_is_not_evidence(self) -> None:
        self.assertFalse(record_has_evidence(_confirmed(), _BASKET, _TRAFFIC, _CORRELATE))
        self.assertFalse(record_has_evidence(_confirmed("t-basket-cross"), _BASKET, None, _CORRELATE))


class TestChallengeUnit(unittest.TestCase):
    """챌린지 증거 = 충족 레코드 ≥ 1 (ALL 아님)."""

    def test_verbose_records_one_evidenced_is_enough(self) -> None:
        records = [
            _confirmed(),
            _confirmed("t-gone"),
            _confirmed("t-login"),
            _confirmed("t-gone", "t-basket-cross"),
        ]
        self.assertTrue(challenge_has_evidence(records, _BASKET, _TRAFFIC, _CORRELATE))

    def test_no_evidenced_record_is_not_evidence(self) -> None:
        records = [_confirmed(), _confirmed("t-gone"), _confirmed("t-login")]
        self.assertFalse(challenge_has_evidence(records, _BASKET, _TRAFFIC, _CORRELATE))
        self.assertFalse(challenge_has_evidence([], _BASKET, _TRAFFIC, _CORRELATE))


class TestDanglingRefs(unittest.TestCase):
    def test_lists_refs_missing_from_traffic_sorted(self) -> None:
        records = [_confirmed("t-zz", "t-basket-cross"), _confirmed("t-aa", "t-zz")]
        self.assertEqual(dangling_refs(records, _TRAFFIC), ("t-aa", "t-zz"))

    def test_no_index_means_unknown_not_dangling(self) -> None:
        self.assertEqual(dangling_refs([_confirmed("t-zz")], None), ())


if __name__ == "__main__":
    unittest.main()
