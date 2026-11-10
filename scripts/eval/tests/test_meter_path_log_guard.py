"""공식 회차 구성은 계측기 경로 기록을 켜지 않는다."""

import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]


class MeterPathLogGuardTests(unittest.TestCase):
    def test_official_wiring_never_enables_path_log(self):
        files = (
            sorted((REPO_ROOT / "deploy" / "eval").glob("compose*.yaml"))
            + sorted((REPO_ROOT / "deploy" / "engine-isolated").glob("compose*.yaml"))
            + sorted((REPO_ROOT / "scripts" / "eval").glob("runner*.py"))
            + sorted((REPO_ROOT / "docs" / "evaluation" / "profiles").glob("*.json"))
        )
        self.assertGreater(len(files), 3)
        for path in files:
            self.assertNotIn("path-log", path.read_text(encoding="utf-8"), str(path))


if __name__ == "__main__":
    unittest.main()
