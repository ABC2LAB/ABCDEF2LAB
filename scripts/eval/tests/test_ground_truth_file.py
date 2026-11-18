"""평가 정답(GT) 파일 분리(2026-11-18): 정답표·상관 규칙은 검색에서 빠지는 평가 정답 디렉터리에만 있고, 채점기는 거기서 읽는다.

이 시험은 정답 키·이름을 코드에 적지 않는다. 정답 파일에서 읽어 비교만 한다.
"""

from __future__ import annotations

import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import correlation  # noqa: E402
import ground_truth as gt  # noqa: E402

REPO = Path(__file__).resolve().parents[3]
GT_DIR = REPO / "evaluation-gt" / "juice-shop-v15"


def _gt_words() -> set[str]:
    data = json.loads((GT_DIR / "challenges.json").read_text(encoding="utf-8"))
    words = {c["key"] for c in data["challenges"]} | {c["name"] for c in data["challenges"]}
    return {w for w in words if len(w) >= 6}  # 짧은 일반 낱말은 오탐이 커서 뺀다


class TestGroundTruthFile(unittest.TestCase):
    def test_scorer_reads_the_gt_directory(self):
        self.assertEqual(gt.GT_FILE, GT_DIR / "challenges.json")
        self.assertEqual(Path(correlation.DEFAULT_PATH), GT_DIR / "gt_correlation.json")
        self.assertEqual((len(gt.ALL_CHALLENGES), len(gt.core_gt()), len(gt.bac_category())), (15, 9, 10))
        self.assertEqual(gt.axis_counts(gt.core_gt()), {gt.Axis.HORIZONTAL: 5, gt.Axis.VERTICAL: 4})
        self.assertEqual(sorted(r.challenge for r in correlation.load_correlation().rules), sorted(c.key for c in gt.core_gt()))

    def test_human_table_and_machine_list_agree(self):
        # 실패해도 정답 내용(키·표)을 출력하지 않는다: 순번과 참·거짓만 보인다.
        table = (GT_DIR / "challenges.md").read_text(encoding="utf-8")
        rows = {line.split("|")[1].strip() for line in table.splitlines() if line.startswith("| ") and line.count("|") > 2}
        missing = [index for index, challenge in enumerate(gt.ALL_CHALLENGES) if challenge.key not in rows]
        self.assertEqual(missing, [], "표에 없는 정답 순번")

    def test_gt_words_are_not_in_the_protocol_or_scorer_code(self):
        # 세션·에이전트가 프로토콜이나 채점 코드를 읽거나 검색하다가 정답을 보지 않게 한다.
        words = _gt_words()
        for path in (REPO / "docs/evaluation/evaluation-protocol.md", REPO / "scripts/eval/ground_truth.py",
                     REPO / "scripts/eval/correlation.py"):
            text = path.read_text(encoding="utf-8")
            with self.subTest(path=path.name):
                self.assertEqual(sum(text.count(w) for w in words), 0)

    def test_gt_directory_is_excluded_from_ripgrep(self):
        self.assertIn("/evaluation-gt/", (REPO / ".ignore").read_text(encoding="utf-8").splitlines())
        rg = shutil.which("rg")
        if rg is None:
            self.skipTest("ripgrep 없음")
        listed = subprocess.run([rg, "--files"], cwd=REPO, capture_output=True, text=True, timeout=60).stdout.splitlines()
        self.assertTrue(listed)
        self.assertFalse([name for name in listed if name.startswith("evaluation-gt/")])


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


if __name__ == "__main__":
    unittest.main()
