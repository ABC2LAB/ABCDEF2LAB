"""B 본문 목표 문장이 제품 Go 렌더와 바이트까지 같은지(탐색 분리 배치 결정안 §6)."""

from __future__ import annotations

import hashlib
import os
import sys
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_goal import common_goal, common_goal_sha256

ROOT = Path(__file__).resolve().parents[3]
# 제품 Go 시험(TestURLDiagnosisCommonGoalIsSharedAndPinned)이 같은 입력의 Go 렌더 + 끝 줄바꿈 하나로 고정한 파일이다.
PINNED = ROOT / "cmd/first-controlled-diagnosis/testdata/goal-common-example.txt"
EXAMPLE = ("https://owned.example:8443", ["/api/v1", "/openapi.json", "=/"], ["tester-a", "tester-b"])


class TestCommonGoal(unittest.TestCase):
    def test_matches_the_product_pinned_bytes(self):
        self.assertEqual((common_goal(*EXAMPLE) + "\n").encode("utf-8"), PINNED.read_bytes())

    def test_parts_follow_the_product_order_and_rules(self):
        text = common_goal(*EXAMPLE)
        paths, objective, labels, report = (text.index(" Approved paths"), text.index(" Objective:"),
                                            text.index(" The host logs in"), text.index(" Report each"))
        self.assertLess(paths, objective)
        self.assertLess(objective, labels)
        self.assertLess(labels, report)
        self.assertIn("https://owned.example:8443/ (exact) .", text)
        # 경로가 없으면 승인 경로 문장이 없고, 라벨이 없으면 라벨 문장이 없다(공식 프로파일은 익명이다).
        bare = common_goal("https://owned.example:8443", [], [])
        self.assertTrue(bare.startswith(" Objective:"))
        self.assertNotIn("The host logs in", bare)
        self.assertNotIn("Approved paths", common_goal("", ["/x"], []))

    def test_sha_is_of_the_exact_bytes(self):
        text = common_goal(*EXAMPLE)
        self.assertEqual(common_goal_sha256(text), hashlib.sha256(text.encode("utf-8")).hexdigest())
        self.assertNotEqual(common_goal_sha256(text), common_goal_sha256(text.strip()))


if __name__ == "__main__":
    unittest.main()
