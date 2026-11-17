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
        # 수용시험 전용 overlay 두 파일만 예외다. 러너는 purpose=browser_acceptance일 때만 compose.acceptance.yaml을 얹고
        # (test_runner_config.TestAcceptanceWiring), 탐색 분리 배치 overlay는 split 설정이 있을 때만 쓰는데 split은 보존 회차
        # 전용(PRESERVATION_ONLY, test_runner_split_config)이고 보존 옵션은 공식 회차에서 거부된다(OFFICIAL_FORBIDDEN).
        allowed = {REPO_ROOT / "deploy" / "eval" / name for name in ("compose.acceptance.yaml", "compose.split.yaml")}
        for path in allowed:
            self.assertIn(path, files)
            self.assertIn("-path-log", path.read_text(encoding="utf-8"))
        for path in files:
            if path not in allowed:
                self.assertNotIn("path-log", path.read_text(encoding="utf-8"), str(path))


if __name__ == "__main__":
    unittest.main()
