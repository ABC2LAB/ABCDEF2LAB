"""평가 집계 순수부 테이블 테스트.

GT 개수(코어9·수평5·수직4, BAC10), §4 분류, §5 집계 수학, 표 렌더를 검증한다.
I/O(collect.py)는 경계라 여기서 테스트하지 않는다. 표준 unittest만 쓴다.

실행: scripts/eval 에서  python -I -m unittest discover -s tests
"""

from __future__ import annotations

import os
import sys
import unittest

# 같은 패키지(scripts/eval)의 모듈을 import 경로에 올린다.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from aggregate import (  # noqa: E402
    Bucket,
    RepeatResult,
    ScanVerdict,
    Stat,
    _stat,
    aggregate_condition,
    classify,
    count_basis,
    render_matrix,
)
from ground_truth import (  # noqa: E402
    Axis,
    axis_counts,
    bac_category,
    by_key,
    core_gt,
)


class TestGroundTruth(unittest.TestCase):
    def test_core_gt_is_nine(self):
        self.assertEqual(len(core_gt()), 9)

    def test_core_gt_axis_split(self):
        counts = axis_counts(core_gt())
        self.assertEqual(counts[Axis.HORIZONTAL], 5)
        self.assertEqual(counts[Axis.VERTICAL], 4)
        # 코어 GT에 업무흐름/범위밖은 없다.
        self.assertNotIn(Axis.BUSINESS_FLOW, counts)
        self.assertNotIn(Axis.OUT_OF_SCOPE, counts)

    def test_bac_category_is_ten(self):
        self.assertEqual(len(bac_category()), 10)

    def test_bac_has_seven_core(self):
        self.assertEqual(sum(1 for c in bac_category() if c.in_core_gt), 7)

    def test_register_and_export_are_core_not_bac(self):
        for key in ("registerAdminChallenge", "dataExportChallenge"):
            ch = by_key(key)
            self.assertIsNotNone(ch)
            self.assertTrue(ch.in_core_gt)
            self.assertFalse(ch.in_bac_category)

    def test_business_flow_excluded_from_both(self):
        for key in ("negativeOrderChallenge", "freeDeluxeChallenge", "deprecatedInterfaceChallenge"):
            ch = by_key(key)
            self.assertIsNotNone(ch)
            self.assertFalse(ch.in_core_gt)
            self.assertFalse(ch.in_bac_category)
            self.assertIs(ch.axis, Axis.BUSINESS_FLOW)

    def test_unknown_key_is_none(self):
        self.assertIsNone(by_key("notAChallenge"))


class TestClassify(unittest.TestCase):
    def test_confirmed_and_solved_is_confirmed(self):
        self.assertIs(classify(ScanVerdict.CONFIRMED, True), Bucket.CONFIRMED)

    def test_confirmed_but_not_solved_is_false_positive(self):
        self.assertIs(classify(ScanVerdict.CONFIRMED, False), Bucket.FALSE_POSITIVE)

    def test_suspected_and_undecidable_are_undecided(self):
        for v in (ScanVerdict.SUSPECTED, ScanVerdict.UNDECIDABLE):
            for solved in (True, False):
                self.assertIs(classify(v, solved), Bucket.UNDECIDED)

    def test_allowed_is_undecided_not_missed(self):
        # GT 챌린지에 ALLOWED = KG·정책 오류 신호 → 판단불가, 해결 여부와 무관(§4-6).
        for solved in (True, False):
            self.assertIs(classify(ScanVerdict.ALLOWED, solved), Bucket.UNDECIDED)

    def test_none_is_missed(self):
        for solved in (True, False):
            self.assertIs(classify(ScanVerdict.NONE, solved), Bucket.MISSED)


class TestCountBasis(unittest.TestCase):
    def test_all_confirmed_solved_gives_nine_core(self):
        core = core_gt()
        repeat = RepeatResult(
            condition="우리@32b",
            elapsed_seconds=10.0,
            total_requests=100,
            verdicts={c.key: ScanVerdict.CONFIRMED for c in core},
            solved={c.key: True for c in core},
        )
        counts = count_basis(repeat, core)
        self.assertEqual(counts.confirmed, 9)
        self.assertEqual(counts.missed, 0)
        self.assertEqual(counts.false_positive, 0)

    def test_missing_keys_count_as_missed(self):
        core = core_gt()
        repeat = RepeatResult("원본@7B", 5.0, 50)  # verdicts/solved 비어있음
        counts = count_basis(repeat, core)
        self.assertEqual(counts.missed, 9)

    def test_out_of_list_counted_separately_not_false_positive(self):
        core = core_gt()
        repeat = RepeatResult(
            "우리@7B", 8.0, 80,
            out_of_list_keys=("someOtherFinding", "anotherOne"),
        )
        counts = count_basis(repeat, core)
        self.assertEqual(counts.out_of_list, 2)
        self.assertEqual(counts.false_positive, 0)  # §4.3: out-of-list ≠ 즉시 오탐


class TestStat(unittest.TestCase):
    def test_single_value_has_no_stdev(self):
        s = _stat([3.0])
        self.assertEqual(s.mean, 3.0)
        self.assertIsNone(s.stdev)
        self.assertIn("n=1", s.render())

    def test_multiple_values_mean_and_stdev(self):
        s = _stat([2.0, 4.0])
        self.assertEqual(s.mean, 3.0)
        self.assertAlmostEqual(s.stdev, 1.4142135, places=5)

    def test_empty_renders_dash(self):
        self.assertEqual(_stat([]).render(), "—")


class TestAggregateAndRender(unittest.TestCase):
    def _two_repeats(self):
        core = core_gt()
        r1 = RepeatResult(
            "우리@32b", 10.0, 100,
            verdicts={c.key: ScanVerdict.CONFIRMED for c in core},
            solved={c.key: True for c in core},
        )
        r2 = RepeatResult(
            "우리@32b", 20.0, 120,
            verdicts={c.key: ScanVerdict.CONFIRMED for c in core},
            solved={c.key: True for c in core},
        )
        return core, [r1, r2]

    def test_aggregate_means(self):
        core, repeats = self._two_repeats()
        agg = aggregate_condition("우리@32b", repeats, core)
        self.assertEqual(agg.elapsed_seconds.mean, 15.0)
        self.assertEqual(agg.total_requests.mean, 110.0)
        self.assertEqual(agg.confirmed.mean, 9.0)
        self.assertEqual(agg.confirmed.stdev, 0.0)  # 둘 다 9 → 편차 0

    def test_render_contains_condition_and_basis(self):
        core, repeats = self._two_repeats()
        agg = aggregate_condition("우리@32b", repeats, core)
        out = render_matrix([agg], "코어 GT(인가 9)")
        self.assertIn("우리@32b", out)
        self.assertIn("코어 GT(인가 9)", out)
        self.assertIn("확정 수", out)

    def test_unmeasured_repeat_renders_dash_not_partial_mean(self):
        # 한 회차라도 미측정(None)이면 남은 회차만으로 평균 내지 않고 "—".
        core, (r1, r2) = self._two_repeats()
        r2 = RepeatResult("우리@32b", None, None, verdicts=r2.verdicts, solved=r2.solved)
        agg = aggregate_condition("우리@32b", [r1, r2], core)
        self.assertEqual(agg.elapsed_seconds.render(), "—")
        self.assertEqual(agg.total_requests.render(), "—")
        self.assertEqual(agg.confirmed.mean, 9.0)  # 판정 칸은 영향 없음


if __name__ == "__main__":
    unittest.main()
