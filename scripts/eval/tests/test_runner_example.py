"""공식 러너 설정 예시(runner.example.json)와 실행 절차(official-run.md)가 공식 고정값·러너 검증기와 맞는지."""

from __future__ import annotations

import copy
import json
import re
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from official_preflight import build_preflight  # noqa: E402
from runner_config import parse_runner_config, plan_run  # noqa: E402
from runner_engine import task_request  # noqa: E402

DOCS = Path(__file__).resolve().parents[3] / "docs/evaluation"
PROFILE = DOCS / "profiles/official-35b.json"
ENDPOINTS = DOCS / "profiles/evidence/openrouter-endpoints-qwen3.6-35b-a3b-20261114.json"
EXAMPLE = DOCS / "runner.example.json"
RUNBOOK = DOCS / "official-run.md"
BASE, OURS = "1" * 40, "2" * 40


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


class TestRunnerExample(unittest.TestCase):
    def filled(self) -> dict:
        """운영자가 채우는 자리(이미지 태그·내용 ID)만 합성 값으로 바꾼다."""
        config = load(EXAMPLE)
        for index, (role, image) in enumerate(sorted(config["images"].items())):
            image["ref"] = image["ref"].replace("<동결 커밋 12자리>", "abcdef123456")
            if image["id"].startswith("sha256:<"):
                image["id"] = "sha256:" + str(index) * 64
        return config

    def test_only_image_placeholders_are_left_for_the_operator(self):
        config = load(EXAMPLE)
        without_images = dict(config, images=None)
        self.assertNotIn("<", json.dumps(without_images, ensure_ascii=False))

    def test_example_parses_and_plans_with_the_official_declaration(self):
        config = parse_runner_config(self.filled())
        preflight = build_preflight(PROFILE, BASE, OURS)
        for side in ("baseline", "ours"):
            with self.subTest(side=side):
                plan_run(config, copy.deepcopy(preflight), side, f"{side}-20261110t100000z-0000abcd")

    def test_example_matches_the_pinned_provider_target_and_drain(self):
        config, profile = load(EXAMPLE), load(PROFILE)
        self.assertEqual(config["llm_gateway"]["expected_provider_name"], load(ENDPOINTS)["matched_endpoints"][0]["provider_name"])
        self.assertEqual(config["target_image_id"], "sha256:" + profile["environment"]["target_image"].split("@sha256:")[1])
        drain = profile["environment"]["run_drain"]
        self.assertEqual((config["run"]["drain_grace_seconds"], config["run"]["post_settle_seconds"]),
                         (drain["grace_seconds"], drain["settle_seconds"]))

    def test_observer_readonly_ref_points_to_evidence_for_this_target_and_path(self):
        config, profile = load(EXAMPLE), load(PROFILE)
        evidence = load(DOCS.parents[1] / config["observer_readonly_ref"])
        self.assertEqual(evidence["observer_path"], config["observer_path"])
        self.assertEqual(evidence["target_image"], profile["environment"]["target_image"])
        self.assertTrue(evidence["runs"] and all(r["engine_requests"] == 0 and r["pre_solved"] == r["post_solved"] == 0
                                                 for r in evidence["runs"]))

    def test_runbook_pins_the_task_request_of_the_example(self):
        pinned = re.search(r"official_task_request_sha256 = ([0-9a-f]{64})", RUNBOOK.read_text(encoding="utf-8"))
        self.assertIsNotNone(pinned)
        request = task_request(load(EXAMPLE)["task"], load(PROFILE)["scope"]["target_origin"])
        self.assertEqual(request.sha256, pinned.group(1))



if __name__ == "__main__":
    unittest.main()
