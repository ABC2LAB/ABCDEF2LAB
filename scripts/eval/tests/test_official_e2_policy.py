"""공식 E② 정책 템플릿이 공식 프로파일·러너 예산·실행 절차와 묶여 있는지 본다."""

from __future__ import annotations

import hashlib
import json
import re
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from e2_prepare import E2RunResources, render_policy  # noqa: E402

DOCS = Path(__file__).resolve().parents[3] / "docs/evaluation"
TEMPLATE = DOCS / "profiles/official-e2-policy.json"
PROFILE = DOCS / "profiles/official-35b.json"
RUNBOOK = DOCS / "official-run.md"
DIGEST = "a" * 64
RESOURCES = E2RunResources("run-1", "2026-11-24T00:00:00Z", "2026-11-24T02:00:00Z", "/run/e2-state", "/app/e2/e2-bash-sandbox",
                           DIGEST, DIGEST, DIGEST, "172.30.9.10")


class OfficialE2PolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.policy = json.loads(TEMPLATE.read_text(encoding="utf-8"))
        cls.profile = json.loads(PROFILE.read_text(encoding="utf-8"))

    def test_template_accepts_run_values(self) -> None:
        filled = json.loads(render_policy(TEMPLATE, RESOURCES))
        self.assertEqual("run-1", filled["run_ref"])
        self.assertEqual("172.30.9.10", filled["rules"][0]["connect_ip"])

    def test_rule_is_the_profile_scope(self) -> None:
        scope = self.profile["scope"]
        self.assertEqual(1, len(self.policy["rules"]))
        rule = self.policy["rules"][0]
        self.assertEqual(scope["target_origin"], rule["origin"])
        self.assertEqual(sorted(scope["methods"]), sorted(rule["methods"]))
        self.assertEqual(scope["paths"], [rule["path_prefix"]])

    def test_modes_are_the_agreed_evaluation_modes(self) -> None:
        self.assertEqual("preauthorized_scope", self.policy["approval_mode"])
        self.assertEqual("evaluation_caller_passthrough", self.policy["header_mode"])
        self.assertEqual({"llm_backend": self.profile["environment"]["llm"]["backend"], "target_kind": "public_practice"}, self.policy["evaluation"])

    def test_limits_do_not_bind_before_the_runner(self) -> None:
        environment = self.profile["environment"]
        self.assertGreaterEqual(self.policy["max_requests"], environment["request_budget"])
        self.assertGreaterEqual(self.policy["limits"]["max_attempts"], environment["request_budget"])
        self.assertGreaterEqual(self.policy["runtime"]["worker_timeout_ms"], environment["time_budget_seconds"] * 1000)
        self.assertEqual(self.policy["limits"]["max_calls"], self.policy["runtime"]["max_tool_calls"])

    def test_runbook_pins_the_template(self) -> None:
        text = RUNBOOK.read_text(encoding="utf-8")
        pinned = re.search(r"official_e2_policy_sha256 = ([0-9a-f]{64})", text)
        self.assertIsNotNone(pinned)
        self.assertEqual(hashlib.sha256(TEMPLATE.read_bytes()).hexdigest(), pinned.group(1))
        self.assertRegex(text, r"official_e2_policy_fingerprint_sha256 = [0-9a-f]{64}")


if __name__ == "__main__":
    unittest.main()
