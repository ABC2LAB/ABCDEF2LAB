"""평가 정답 로더(ground_truth._load)의 형식 검사 시험. 합성 자료만 쓴다(2026-11-18, 정답표 노출 방지).

실제 정답 목록·상관 규칙·사람용 표를 대조하는 시험은 evaluation-gt/tests/test_ground_truth_real.py에 있다.
"""

from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import synthetic_gt  # noqa: E402,F401  합성 정답 목록을 먼저 끼운다(정답표 노출 방지)

import ground_truth as gt  # noqa: E402


class TestLoader(unittest.TestCase):
    def load(self, value) -> tuple:
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "gt.json"
            path.write_text(json.dumps(value), encoding="utf-8")
            return gt._load(path)

    def item(self, key="k-one", group="bac_category", core=True, axis="H"):
        return {"key": key, "name": "n", "axis": axis, "group": group, "in_core_gt": core,
                "in_bac_category": group == "bac_category"}

    def test_valid_and_closed_shape(self):
        doc = {"format": gt.GT_FORMAT, "challenges": [self.item(), self.item("k-two", "other_category_authz", axis="V"),
                                                      self.item("k-three", "business_flow", core=False, axis="BF")]}
        self.assertEqual([c.key for c in self.load(doc)], ["k-one", "k-two", "k-three"])
        bad = [dict(doc, format="v0"), dict(doc, challenges=[]), dict(doc, challenges=[self.item(), self.item()]),
               dict(doc, challenges=[self.item("a", "other_category_authz"), self.item("b")]),  # 묶음 순서
               dict(doc, challenges=[self.item("a", "business_flow", core=True, axis="BF")]),  # 별도 축은 코어 아님
               dict(doc, challenges=[dict(self.item(), in_bac_category=False)]),
               dict(doc, challenges=[dict(self.item(), axis="X")]), dict(doc, challenges=[dict(self.item(), extra=1)]),
               dict(doc, challenges=[dict(self.item(), in_core_gt=1)]), dict(doc, challenges=[dict(self.item(), key="")])]
        for value in bad:
            with self.subTest(value=json.dumps(value)[:80]), self.assertRaisesRegex(gt.GroundTruthError, "ground_truth_invalid"):
                self.load(value)
        with self.assertRaisesRegex(gt.GroundTruthError, "ground_truth_unreadable"):
            gt._load(Path("/nonexistent/gt.json"))

    def test_duplicate_fields_are_refused(self):
        # #712 리뷰: 같은 칸을 두 번 적으면 마지막 값만 남아 코어 분모가 조용히 바뀐다. 파싱 때 거부한다(내용은 오류에 없다).
        item = json.dumps(self.item())
        duplicated = item[:-1] + ', "in_core_gt": false}'
        for text in ('{"format": "%s", "challenges": [%s]}' % (gt.GT_FORMAT, duplicated),
                     '{"format": "%s", "format": "%s", "challenges": [%s]}' % (gt.GT_FORMAT, gt.GT_FORMAT, item)):
            with tempfile.TemporaryDirectory() as root:
                path = Path(root) / "gt.json"
                path.write_text(text, encoding="utf-8")
                with self.assertRaises(gt.GroundTruthError) as caught:
                    gt._load(path)
            self.assertEqual(str(caught.exception), "ground_truth_unreadable")
        with tempfile.TemporaryDirectory() as root:  # 중복 없는 같은 내용은 받는다(입력 대조)
            path = Path(root) / "gt.json"
            path.write_text('{"format": "%s", "challenges": [%s]}' % (gt.GT_FORMAT, item), encoding="utf-8")
            self.assertEqual(len(gt._load(path)), 1)


if __name__ == "__main__":
    unittest.main()
