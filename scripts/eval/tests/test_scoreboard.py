"""scoreboard — 스코어보드 스냅샷 정규화·파싱·GT key 검사 테스트(T8).

fixture는 전부 손으로 만든 합성 값이고 임시 디렉터리에만 쓴다 — 실제 스캔 결과·
스냅샷은 쓰지 않는다(절대규칙 2).

실행: scripts/eval 에서  python3 -I -m unittest discover -s tests
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from ground_truth import core_gt  # noqa: E402
from scoreboard import (  # noqa: E402
    SnapshotError,
    normalize_challenges,
    parse_scoreboard_snapshot,
    require_basis_keys,
)


class TestNormalize(unittest.TestCase):
    def test_rows_and_data_envelope(self) -> None:
        rows = [{"key": "a", "solved": True, "name": "무시되는 필드"}, {"key": "b", "solved": False}]
        self.assertEqual(normalize_challenges(rows), {"a": True, "b": False})
        self.assertEqual(normalize_challenges({"status": "success", "data": rows}), {"a": True, "b": False})

    def test_object_without_data_rejected(self) -> None:
        with self.assertRaises(SnapshotError):
            normalize_challenges({"status": "success"})

    def test_string_solved_not_coerced_to_true(self) -> None:
        # bool("false") == True 였던 경로를 막는다.
        with self.assertRaisesRegex(SnapshotError, "불리언 아님"):
            normalize_challenges([{"key": "a", "solved": "false"}])

    def test_int_and_missing_solved_rejected(self) -> None:
        for row in ({"key": "a", "solved": 1}, {"key": "a"}):
            with self.assertRaises(SnapshotError):
                normalize_challenges([row])

    def test_missing_or_empty_key_rejected(self) -> None:
        for row in ({"solved": True}, {"key": "", "solved": True}):
            with self.assertRaisesRegex(SnapshotError, "key 부재"):
                normalize_challenges([row])

    def test_duplicate_key_rejected(self) -> None:
        with self.assertRaisesRegex(SnapshotError, "중복"):
            normalize_challenges([{"key": "a", "solved": True}, {"key": "a", "solved": False}])

    def test_errors_aggregated(self) -> None:
        with self.assertRaises(SnapshotError) as ctx:
            normalize_challenges(["행 아님", {"key": "a", "solved": "yes"}])
        self.assertIn("2건", str(ctx.exception))


class TestParseSnapshot(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)

    def write(self, text: str) -> str:
        path = os.path.join(self._tmp.name, "snapshot.json")
        with open(path, "w", encoding="utf-8") as f:
            f.write(text)
        return path

    def test_valid_normalized(self) -> None:
        path = self.write(json.dumps({"a": True, "b": False}))
        self.assertEqual(parse_scoreboard_snapshot(path), {"a": True, "b": False})

    def test_broken_json(self) -> None:
        with self.assertRaisesRegex(SnapshotError, "JSON 파싱"):
            parse_scoreboard_snapshot(self.write("{not json"))

    def test_top_level_array_rejected(self) -> None:
        # 원본(행 배열)을 정규화본 자리에 넣은 실수도 막힌다.
        with self.assertRaisesRegex(SnapshotError, "객체가 아님"):
            parse_scoreboard_snapshot(self.write(json.dumps([{"key": "a", "solved": True}])))

    def test_non_bool_values_rejected(self) -> None:
        for value in ('"true"', "1", "null", '{"solved": true}'):
            with self.assertRaisesRegex(SnapshotError, "불리언 아님"):
                parse_scoreboard_snapshot(self.write('{"a": %s}' % value))

    def test_duplicate_key_in_object_rejected(self) -> None:
        # dict로 읽으면 마지막 값으로 조용히 덮이는 경우.
        with self.assertRaisesRegex(SnapshotError, "중복"):
            parse_scoreboard_snapshot(self.write('{"a": true, "a": false}'))

    def test_empty_key_rejected(self) -> None:
        with self.assertRaisesRegex(SnapshotError, "빈 key"):
            parse_scoreboard_snapshot(self.write('{"": true}'))


class TestRequireBasisKeys(unittest.TestCase):
    def test_missing_gt_key_is_error_not_unsolved(self) -> None:
        solved = {c.key: False for c in core_gt()}
        dropped = core_gt()[0].key
        del solved[dropped]
        with self.assertRaisesRegex(SnapshotError, dropped):
            require_basis_keys(solved, core_gt())

    def test_all_present_with_extra_keys_ok(self) -> None:
        solved = {c.key: False for c in core_gt()} | {"notInGtChallenge": True}
        require_basis_keys(solved, core_gt())


if __name__ == "__main__":
    unittest.main()
