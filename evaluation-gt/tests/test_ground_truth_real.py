"""실제 평가 정답이 꼭 필요한 시험(2026-11-18, 정답표 노출 방지). 평가 스크립트 시험(scripts/eval/tests)은 합성 정답 목록으로
돌고, 실제 목록·상관 규칙·사람용 표를 대조하는 시험만 여기서 실제 목록으로 돈다.

실패해도 정답 내용(키·이름·표)을 출력하지 않는다: 참·거짓과 건수만 보인다.
실행: python3 -I -B -m unittest discover -s evaluation-gt/tests
"""

from __future__ import annotations

import json
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "scripts" / "eval"))

import correlation  # noqa: E402
import ground_truth as gt  # noqa: E402

GT_DIR = REPO / "evaluation-gt" / "juice-shop-v15"
# 2~5자 대문자 약어 이름(취약점 분류 이름)은 제품 코드에 일반어로 쓰여 낱말 검사에서 뺀다. 키는 모두 본다.
MIN_NAME = 6


def _gt_words() -> set[str]:
    data = json.loads((GT_DIR / "challenges.json").read_text(encoding="utf-8"))
    return {c["key"] for c in data["challenges"]} | {c["name"] for c in data["challenges"] if len(c["name"]) >= MIN_NAME}


class TestRealGroundTruth(unittest.TestCase):
    def setUp(self):
        gt.use_ground_truth_file(gt.GT_FILE)  # 같은 프로세스에서 다른 시험이 바꿔 두었어도 실제 목록으로 본다

    def test_scorer_reads_the_gt_directory(self):
        self.assertEqual(gt.GT_FILE, GT_DIR / "challenges.json")
        self.assertEqual(Path(correlation.DEFAULT_PATH), GT_DIR / "gt_correlation.json")
        self.assertEqual((len(gt.ALL_CHALLENGES), len(gt.core_gt()), len(gt.bac_category())), (15, 9, 10))
        self.assertEqual(gt.axis_counts(gt.core_gt()), {gt.Axis.HORIZONTAL: 5, gt.Axis.VERTICAL: 4})

    def test_correlation_rules_cover_the_core_nine(self):
        rules = {r.challenge for r in correlation.load_correlation().rules}
        self.assertTrue(rules == {c.key for c in gt.core_gt()}, "상관 규칙과 코어 목록이 다름")

    def test_human_table_and_machine_list_agree(self):
        table = (GT_DIR / "challenges.md").read_text(encoding="utf-8")
        rows = {line.split("|")[1].strip() for line in table.splitlines() if line.startswith("| ") and line.count("|") > 2}
        missing = [index for index, challenge in enumerate(gt.ALL_CHALLENGES) if challenge.key not in rows]
        self.assertEqual(missing, [], "표에 없는 정답 순번")

    def test_gt_words_appear_only_under_the_gt_directory(self):
        # 저장소에서 evaluation-gt/ 밖에 정답 키·이름이 0건이어야 한다(세션·에이전트 검색에 보이지 않게).
        words = _gt_words()
        listed = subprocess.run(["git", "-C", str(REPO), "ls-files", "--cached", "--others", "--exclude-standard"],
                                capture_output=True, text=True, check=True).stdout.split()
        hits = 0
        for name in listed:
            if name.startswith("evaluation-gt/"):
                continue
            try:
                text = (REPO / name).read_text(encoding="utf-8")
            except (UnicodeError, OSError):
                continue
            hits += sum(text.count(word) for word in words)
        self.assertEqual(hits, 0, "evaluation-gt/ 밖의 정답 낱말 건수")

    def test_gt_directory_is_excluded_from_ripgrep(self):
        self.assertIn("/evaluation-gt/", (REPO / ".ignore").read_text(encoding="utf-8").splitlines())
        rg = shutil.which("rg")
        if rg is None:
            self.skipTest("ripgrep 없음")
        listed = subprocess.run([rg, "--files"], cwd=REPO, capture_output=True, text=True, timeout=60).stdout.splitlines()
        self.assertTrue(listed)
        self.assertFalse([name for name in listed if name.startswith("evaluation-gt/")])


if __name__ == "__main__":
    unittest.main()
