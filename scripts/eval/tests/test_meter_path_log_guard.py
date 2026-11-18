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
        # 경로 기록 overlay 두 파일만 예외다. 러너는 경로 기록이 필요한 회차(수용시험, 그리고 2026-11-18 결정 (a)로 분리 배치
        # 공식 회차)에만 compose.acceptance.yaml을 얹고(runner_config.path_log_required, test_runner_config.TestAcceptanceWiring),
        # 탐색 분리 배치 overlay는 split 설정이 있을 때만 쓴다. 분리 배치가 아닌 공식 회차(엔진 직접 배치)는 경로 키·보존 옵션을
        # 설정에서 거부하고(OFFICIAL_FORBIDDEN), 경로 기록이 있으면 회차 무효다(meter_path_log_enabled).
        allowed = {REPO_ROOT / "deploy" / "eval" / name for name in ("compose.acceptance.yaml", "compose.split.yaml")}
        for path in allowed:
            self.assertIn(path, files)
            self.assertIn("-path-log", path.read_text(encoding="utf-8"))
        for path in files:
            if path not in allowed:
                self.assertNotIn("path-log", path.read_text(encoding="utf-8"), str(path))


if __name__ == "__main__":
    unittest.main()
