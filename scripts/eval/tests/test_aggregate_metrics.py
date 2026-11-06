"""실측 통계 집계의 결측·순서·표 출력 조합 회귀.

합성 RepeatResult만 사용하고 빈 basis를 넘겨 판정·GT·귀속 규칙과 분리한다.
실제 0은 측정값으로 유지하고 None은 미측정으로 표시하는 세 지표의 계약만 검증한다.
"""

from __future__ import annotations

import itertools
import os
import socket
import subprocess
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import aggregate

_EMPTY_BASIS = ()
_PROFILES = (
    (0.0, 0, 0),
    (10.0, 100, 7),
    (20.0, 200, 9),
)
_EXPECTED_MEAN = (10.0, 100.0, 16.0 / 3.0)
_EXPECTED_STDEV = (10.0, 100.0, 4.7258156263)


def _repeat(condition: str, values: tuple[float | None, int | None, int | None]) -> aggregate.RepeatResult:
    elapsed, requests, blocked = values
    return aggregate.RepeatResult(condition, elapsed, requests, blocked_attempts=blocked)


class TestAggregateMetricsProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("offline synthetic aggregate only"))
            guard.start()
            self.addCleanup(guard.stop)

    def aggregate(self, values, condition="synthetic"):
        return aggregate.aggregate_condition(
            condition, [_repeat(condition, value) for value in values], _EMPTY_BASIS
        )

    def assert_stat(self, stat, *, mean, stdev, n, rendered):
        self.assertEqual(stat.n, n)
        self.assertEqual(stat.mean, mean)
        if stdev is None:
            self.assertIsNone(stat.stdev)
        else:
            self.assertAlmostEqual(stat.stdev, stdev, places=9)
        self.assertEqual(stat.render(), rendered)

    def test_measured_zero_and_values_are_aggregated_independently_of_repeat_order(self):
        for order in itertools.permutations(range(3)):
            values = tuple(_PROFILES[index] for index in order)
            with self.subTest(order=order):
                result = self.aggregate(values)
                self.assert_stat(result.elapsed_seconds, mean=_EXPECTED_MEAN[0], stdev=_EXPECTED_STDEV[0],
                                 n=3, rendered="10 ± 10")
                self.assert_stat(result.total_requests, mean=_EXPECTED_MEAN[1], stdev=_EXPECTED_STDEV[1],
                                 n=3, rendered="100 ± 100")
                self.assert_stat(result.blocked_attempts, mean=_EXPECTED_MEAN[2], stdev=_EXPECTED_STDEV[2],
                                 n=3, rendered="5.33333 ± 4.72582")
                self.assertEqual(result.confirmed.n, 3)
                self.assertEqual(result.confirmed.mean, 0.0)

    def test_each_missingness_mask_affects_only_its_metric_and_never_partial_means(self):
        complete = _PROFILES
        orders = ((0, 1, 2), (2, 1, 0), (1, 2, 0))
        for masks, order in itertools.product(itertools.product(range(8), repeat=3), orders):
            values = []
            for index in order:
                profile = complete[index]
                values.append(tuple(None if masks[metric] & (1 << index) else profile[metric]
                                    for metric in range(3)))
            with self.subTest(masks=masks, order=order):
                result = self.aggregate(tuple(values))
                stats = (result.elapsed_seconds, result.total_requests, result.blocked_attempts)
                for metric, stat in enumerate(stats):
                    if masks[metric]:
                        self.assert_stat(stat, mean=0.0, stdev=None, n=0, rendered="—")
                    else:
                        self.assertEqual(stat.n, 3)
                        self.assertEqual(stat.mean, _EXPECTED_MEAN[metric])
                        self.assertAlmostEqual(stat.stdev, _EXPECTED_STDEV[metric], places=9)
                        self.assertNotEqual(stat.render(), "—")

    def test_empty_and_single_repeat_have_explicit_sample_sizes(self):
        empty = self.aggregate(())
        for stat in (empty.elapsed_seconds, empty.total_requests, empty.blocked_attempts):
            self.assert_stat(stat, mean=0.0, stdev=None, n=0, rendered="—")

        single = self.aggregate(((0.0, 0, 0),))
        for stat in (single.elapsed_seconds, single.total_requests, single.blocked_attempts):
            self.assert_stat(stat, mean=0.0, stdev=None, n=1, rendered="0 (n=1)")

    def test_zero_request_runs_are_noted_under_the_table_per_condition(self):
        original = self.aggregate([(1.0, 0, 0), (2.0, 5, 0), (3.0, None, 0)], condition="원본")
        ours = self.aggregate([(1.0, 7, 0), (2.0, 8, 0)], condition="우리")
        self.assertEqual((original.zero_request_runs, original.measured_request_runs), (1, 2))
        self.assertEqual((ours.zero_request_runs, ours.measured_request_runs), (0, 2))
        rendered = aggregate.render_matrix([original, ours], "synthetic metrics")
        self.assertIn("대상 요청 0 회차: 원본 1/2. 대상 요청이 없던 회차", rendered)
        self.assertNotIn("우리 0/", rendered)

    def test_unmeasured_requests_are_not_zero_request_runs(self):
        row = self.aggregate([(1.0, None, 0), (2.0, None, 0)], condition="원본")
        self.assertEqual((row.zero_request_runs, row.measured_request_runs), (0, 0))
        self.assertNotIn("대상 요청 0 회차", aggregate.render_matrix([row], "synthetic metrics"))

    def test_render_matrix_preserves_condition_order_and_measurement_columns(self):
        first = self.aggregate(((0.0, 0, 0), (10.0, 100, 5)), condition="first")
        second = self.aggregate(((None, 2, None),), condition="second")
        rendered = aggregate.render_matrix([first, second], "synthetic metrics")
        lines = rendered.splitlines()
        self.assertEqual(lines[0], "### 측정 매트릭스 — synthetic metrics 기준")
        self.assertEqual(lines[4].count("|"), 10)
        self.assertEqual(lines[5].count("|"), 10)
        self.assertTrue(lines[4].startswith("| first |"))
        self.assertTrue(lines[5].startswith("| second |"))
        self.assertIn("| first | 5 ± 7.07107 | 50 ± 70.7107 | 2.5 ± 3.53553 |", lines[4])
        self.assertIn("| second | — | 2 (n=1) | — |", lines[5])
        self.assertEqual(rendered[-1], "\n")

    def test_inputs_are_unchanged_and_aggregation_is_pure_offline(self):
        values = tuple(_PROFILES)
        repeats = [_repeat("synthetic", value) for value in values]
        before = tuple((repeat.condition, repeat.elapsed_seconds, repeat.total_requests,
                        repeat.blocked_attempts, repeat.verdicts.copy(), repeat.solved.copy())
                       for repeat in repeats)
        with patch("builtins.open", side_effect=AssertionError("aggregate must not open files")), \
                patch.object(socket, "socket", side_effect=AssertionError("aggregate must not use network")), \
                patch.object(subprocess, "Popen", side_effect=AssertionError("aggregate must not spawn")):
            first = aggregate.aggregate_condition("synthetic", repeats, _EMPTY_BASIS)
            second = aggregate.aggregate_condition("synthetic", repeats, _EMPTY_BASIS)
        self.assertEqual(first, second)
        self.assertEqual(before, tuple((repeat.condition, repeat.elapsed_seconds, repeat.total_requests,
                                        repeat.blocked_attempts, repeat.verdicts, repeat.solved)
                                       for repeat in repeats))

    def test_regression_assertion_detects_partial_mean_weakening(self):
        values = ((_PROFILES[0][0], None, _PROFILES[0][2]),
                  (_PROFILES[1][0], 100, _PROFILES[1][2]))
        expected = self.aggregate(values)
        self.assert_stat(expected.total_requests, mean=0.0, stdev=None, n=0, rendered="—")
        original = aggregate._optional_stat

        def weakened(items):
            return original([value for value in items if value is not None])

        with patch.object(aggregate, "_optional_stat", side_effect=weakened):
            actual = self.aggregate(values)
            with self.assertRaises(AssertionError):
                self.assert_stat(actual.total_requests, mean=0.0, stdev=None, n=0, rendered="—")

    def test_regression_assertions_detect_zero_as_missing_and_population_sd(self):
        expected = self.aggregate(_PROFILES)
        self.assert_stat(expected.elapsed_seconds, mean=10.0, stdev=10.0, n=3, rendered="10 ± 10")
        original_optional = aggregate._optional_stat

        def zero_is_missing(items):
            return original_optional([None if value == 0 else value for value in items])

        with patch.object(aggregate, "_optional_stat", side_effect=zero_is_missing):
            actual = self.aggregate(_PROFILES)
            with self.assertRaises(AssertionError):
                self.assert_stat(actual.elapsed_seconds, mean=10.0, stdev=10.0, n=3, rendered="10 ± 10")

        with patch.object(aggregate.statistics, "stdev", side_effect=aggregate.statistics.pstdev):
            actual = self.aggregate(_PROFILES)
            with self.assertRaises(AssertionError):
                self.assert_stat(actual.elapsed_seconds, mean=10.0, stdev=10.0, n=3, rendered="10 ± 10")


if __name__ == "__main__":
    unittest.main()
