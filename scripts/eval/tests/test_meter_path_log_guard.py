"""공식 회차 구성은 계측기 경로 기록을 켜지 않는다."""

import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]


class MeterPathLogGuardTests(unittest.TestCase):
    def test_official_wiring_never_enables_path_log(self):
        # 글자 검색은 첫 겹이다. 환경 변수로 들어온 플래그는 러너가 기록 파일을 보고 회차를 무효로 한다.
        files = [
            path
            for root in ("deploy", "scripts/eval", "docs/evaluation/profiles")
            for path in sorted((REPO_ROOT / root).rglob("*"))
            if path.is_file() and "tests" not in path.parts
            and (path.suffix in {".yaml", ".yml", ".py", ".json", ".sh"} or path.name.endswith("Dockerfile"))
        ]
        self.assertGreater(len(files), 20)
        self.assertTrue(any(path.name == "meter.Dockerfile" for path in files))
        # 수용시험 전용 overlay 한 파일만 예외다. 러너는 purpose=browser_acceptance일 때만 그 파일을 얹는다
        # (test_runner_config.TestAcceptanceWiring).
        acceptance = REPO_ROOT / "deploy" / "eval" / "compose.acceptance.yaml"
        self.assertIn(acceptance, files)
        self.assertIn("-path-log", acceptance.read_text(encoding="utf-8"))
        for path in files:
            if path != acceptance:
                self.assertNotIn("path-log", path.read_text(encoding="utf-8"), str(path))


if __name__ == "__main__":
    unittest.main()
