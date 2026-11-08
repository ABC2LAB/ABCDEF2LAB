"""평가 집계 순수부 테이블 테스트.

GT 개수(코어9·수평5·수직4, BAC10), §4 분류, §5 집계 수학, 표 렌더를 검증한다.
I/O(collect.py)는 경계라 여기서 테스트하지 않는다. 표준 unittest만 쓴다.

실행: scripts/eval 에서  python -I -m unittest discover -s tests
"""

from __future__ import annotations

import itertools
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


_V = ScanVerdict
_ANY = None  # 판정표의 "(무관)"

# 프로토콜 §4 판정표를 데이터로 옮긴 것: (판정, 해결, 증거 충족, 커버리지, 상관 가능) → 칸.
_PROTOCOL_TABLE = (
    (_V.CONFIRMED, True, True, _ANY, _ANY, Bucket.CONFIRMED),
    (_V.CONFIRMED, True, False, _ANY, _ANY, Bucket.UNDECIDED),
    (_V.CONFIRMED, False, _ANY, _ANY, _ANY, Bucket.FALSE_POSITIVE),
    (_V.SUSPECTED, _ANY, _ANY, _ANY, _ANY, Bucket.UNDECIDED),
    (_V.UNDECIDABLE, _ANY, _ANY, _ANY, _ANY, Bucket.UNDECIDED),
    (_V.ALLOWED, _ANY, _ANY, _ANY, _ANY, Bucket.UNDECIDED),
    # 판정 없음의 미탐 두 행(커버리지·해결)은 각주 ¹ — 상관 가능할 때만, 아니면 판단불가.
    (_V.NONE, False, _ANY, True, True, Bucket.MISSED),
    (_V.NONE, False, _ANY, True, False, Bucket.UNDECIDED),
    (_V.NONE, False, _ANY, False, _ANY, Bucket.UNDECIDED),
    (_V.NONE, True, _ANY, _ANY, True, Bucket.MISSED),
    (_V.NONE, True, _ANY, _ANY, False, Bucket.UNDECIDED),
)


def _cls(verdict, solved, evidence_ok=False, covered=False, correlatable=False):
    return classify(verdict, solved, evidence_ok=evidence_ok, covered=covered, correlatable=correlatable,
                    pre_solved=False, engine_attributed=True)


class TestClassify(unittest.TestCase):
    def test_every_combination_matches_exactly_one_protocol_row(self):
        n = 0
        for verdict in ScanVerdict:
            for inputs in itertools.product((True, False), repeat=4):
                rows = [
                    r for r in _PROTOCOL_TABLE
                    if r[0] is verdict and all(want is _ANY or want == got for want, got in zip(r[1:5], inputs))
                ]
                self.assertEqual(len(rows), 1, f"판정표가 분할이 아님: {verdict} {inputs}")
                self.assertIs(_cls(verdict, *inputs), rows[0][5],
                              f"{verdict} (해결, 증거, 커버리지, 상관 가능)={inputs}")
                n += 1
        self.assertEqual(n, 80)

    def test_t4_confirmed_solved_without_evidence_is_not_confirmed(self):
        # T4 — 해결됐어도 증거 3조건을 못 채우면 확정이 아니라 판단불가(오탐도 아님, §4-2).
        for covered in (True, False):
            self.assertIs(_cls(_V.CONFIRMED, True, evidence_ok=False, covered=covered), Bucket.UNDECIDED)
        self.assertIs(_cls(_V.CONFIRMED, True, evidence_ok=True), Bucket.CONFIRMED)

    def test_false_positive_does_not_depend_on_evidence(self):
        # 증거 부족이 오탐 회피 수단이 되지 않는다.
        for evidence_ok in (True, False):
            self.assertIs(_cls(_V.CONFIRMED, False, evidence_ok=evidence_ok), Bucket.FALSE_POSITIVE)

    def test_t3_unsolved_no_finding_without_coverage_is_not_missed(self):
        # T3② — GT·판정 없음·미해결: 커버리지 없으면 판단불가, 있어야 미탐(§4-4). 상관 가능한 챌린지 기준.
        self.assertIs(_cls(_V.NONE, False, covered=False, correlatable=True), Bucket.UNDECIDED)
        self.assertIs(_cls(_V.NONE, False, covered=True, correlatable=True), Bucket.MISSED)

    def test_coverage_on_uncorrelatable_challenge_is_undecided(self):
        # 커버리지 행 게이트 — 커버리지가 있어도 상관 불가면 '판정 없음'을 모른다 → 판단불가.
        self.assertIs(_cls(_V.NONE, False, covered=True, correlatable=False), Bucket.UNDECIDED)

    def test_solve_without_finding_is_attributable_miss(self):
        # 판정 없음 + 해결 + 상관 가능 = solve가 커버리지 증거 → 미탐(커버리지 기록 없어도).
        for covered in (True, False):
            self.assertIs(_cls(_V.NONE, True, covered=covered, correlatable=True), Bucket.MISSED)

    def test_solve_on_uncorrelatable_challenge_is_undecided(self):
        # A12 게이트 — type이 없으면 레코드가 붙을 길이 없어 '판정 없음'을 모른다 → 판단불가(커버리지 무관).
        for covered in (True, False):
            self.assertIs(_cls(_V.NONE, True, covered=covered, correlatable=False), Bucket.UNDECIDED)

    def test_allowed_is_undecided_not_missed(self):
        # GT 챌린지에 ALLOWED = KG·정책 오류 신호 → 판단불가, 해결 여부와 무관(§4-6).
        for solved in (True, False):
            self.assertIs(_cls(_V.ALLOWED, solved), Bucket.UNDECIDED)


class TestCountBasis(unittest.TestCase):
    def test_all_confirmed_solved_gives_nine_core(self):
        core = core_gt()
        repeat = RepeatResult(
            condition="우리@32b",
            elapsed_seconds=10.0,
            total_requests=100,
            verdicts={c.key: ScanVerdict.CONFIRMED for c in core},
            pre_solved={c.key: False for c in core},
            engine_solved=frozenset(c.key for c in core),
            solved={c.key: True for c in core},
            evidence_ok={c.key: True for c in core},
        )
        counts = count_basis(repeat, core)
        self.assertEqual(counts.confirmed, 9)
        self.assertEqual(counts.missed, 0)
        self.assertEqual(counts.false_positive, 0)

    def test_confirmed_solved_without_evidence_counts_undecided(self):
        # T4(집계) — evidence_ok가 없으면 확정 0, 전부 판단불가.
        core = core_gt()
        repeat = RepeatResult(
            "우리@35b", None, None,
            verdicts={c.key: ScanVerdict.CONFIRMED for c in core},
            pre_solved={c.key: False for c in core},
            engine_solved=frozenset(c.key for c in core),
            solved={c.key: True for c in core},
        )
        counts = count_basis(repeat, core)
        self.assertEqual((counts.confirmed, counts.undecided), (0, 9))

    def test_missing_keys_without_coverage_are_undecided(self):
        # T3②(집계) — verdicts/solved 비어 있고 커버리지 출처 없음(None) → 미탐 0, 판단불가 9.
        core = core_gt()
        counts = count_basis(RepeatResult("원본@7B", 5.0, 50), core)
        self.assertEqual((counts.missed, counts.undecided), (0, 9))

    def test_missing_keys_with_coverage_are_missed(self):
        core = core_gt()
        keys = frozenset(c.key for c in core)
        repeat = RepeatResult("원본@7B", 5.0, 50, covered=keys, correlatable=keys,
                              pre_solved={key: False for key in keys})
        self.assertEqual(count_basis(repeat, core).missed, 9)

    def test_coverage_without_correlatable_info_is_undecided(self):
        # 상관 가능 정보가 없으면(None) 커버리지가 있어도 미탐을 주지 않는다(fail-closed).
        core = core_gt()
        repeat = RepeatResult("원본@7B", 5.0, 50, covered=frozenset(c.key for c in core))
        self.assertEqual((count_basis(repeat, core).missed, count_basis(repeat, core).undecided), (0, 9))

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
            pre_solved={c.key: False for c in core},
            engine_solved=frozenset(c.key for c in core),
            solved={c.key: True for c in core},
            evidence_ok={c.key: True for c in core},
        )
        r2 = RepeatResult(
            "우리@32b", 20.0, 120,
            verdicts={c.key: ScanVerdict.CONFIRMED for c in core},
            pre_solved={c.key: False for c in core},
            engine_solved=frozenset(c.key for c in core),
            solved={c.key: True for c in core},
            evidence_ok={c.key: True for c in core},
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

    def test_blocked_attempts_separate_from_sent_requests(self):
        # A7 — 차단 시도는 총 요청 수(전송 HTTP)에 섞지 않고 따로 집계·렌더한다.
        core = core_gt()
        repeats = [
            RepeatResult("우리@35b", 1.0, 100, blocked_attempts=7),
            RepeatResult("우리@35b", 1.0, 120, blocked_attempts=9),
        ]
        agg = aggregate_condition("우리@35b", repeats, core)
        self.assertEqual(agg.total_requests.mean, 110.0)
        self.assertEqual(agg.blocked_attempts.mean, 8.0)
        out = render_matrix([agg], "코어 GT(인가 9)")
        self.assertIn("| 총 요청 수 | 차단 시도 수 |", out)
        self.assertIn(f"| 우리@35b | {agg.elapsed_seconds.render()} | 110 ± ", out)
        self.assertIn(f"| {agg.blocked_attempts.render()} |", out)

    def test_unmeasured_blocked_attempts_renders_dash(self):
        agg = aggregate_condition("우리@35b", [RepeatResult("우리@35b", 1.0, 100)], core_gt())
        self.assertEqual(agg.blocked_attempts.render(), "—")
        self.assertEqual(agg.total_requests.render(), "100 (n=1)")

    def test_unmeasured_repeat_renders_dash_not_partial_mean(self):
        # 한 회차라도 미측정(None)이면 남은 회차만으로 평균 내지 않고 "—".
        core, (r1, r2) = self._two_repeats()
        r2 = RepeatResult("우리@32b", None, None, verdicts=r2.verdicts, solved=r2.solved, evidence_ok=r2.evidence_ok,
                          pre_solved=r2.pre_solved, engine_solved=r2.engine_solved)
        agg = aggregate_condition("우리@32b", [r1, r2], core)
        self.assertEqual(agg.elapsed_seconds.render(), "—")
        self.assertEqual(agg.total_requests.render(), "—")
        self.assertEqual(agg.confirmed.mean, 9.0)  # 판정 칸은 영향 없음


if __name__ == "__main__":
    unittest.main()
