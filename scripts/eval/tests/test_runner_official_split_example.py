"""공식 분리 배치 러너 설정 예시(runner.official-split.example.json): 운영자는 표시된 칸만 채우면 되고, 채우면 러너 설정
검사를 통과하며, 공통 칸은 엔진 직접 배치 예시(runner.example.json)와 같다."""

from __future__ import annotations

import json
import re
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from runner_config import parse_runner_config  # noqa: E402

DOCS = Path(__file__).resolve().parents[3] / "docs/evaluation"
EXAMPLE = DOCS / "runner.official-split.example.json"
BASE_EXAMPLE = DOCS / "runner.example.json"
# 운영자가 세션마다 채우는 칸(이미지 태그·내용 ID, 세션 이름·날짜, 도구 면 측정 결과). 그 밖에 `<`가 있으면 안 된다.
FILL_PATHS = {"images.engine_baseline.id", "images.engine_ours.ref", "images.engine_ours.id", "images.engine_db.id", "images.meter.ref",
              "images.meter.id", "path_key_file", "preservation.series_ref", "preservation.browser_image_id", "split.product_tag",
              "split.product_source_sha", "split.image_ids.product", "split.image_ids.explore_browser", "split.image_ids.fixture",
              "split.cli_usage_names[0]"}


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def strings(value, path=""):
    if isinstance(value, dict):
        for key, item in value.items():
            yield from strings(item, f"{path}.{key}" if path else key)
    elif isinstance(value, list):
        for index, item in enumerate(value):
            yield from strings(item, f"{path}[{index}]")
    elif isinstance(value, str):
        yield path, value


def filled() -> dict:
    """채울 칸을 합성 값으로 바꾼다(실제 회차 값이 아니다)."""
    text = EXAMPLE.read_text(encoding="utf-8")
    text = text.replace("<동결 커밋 40자리>", "2" * 40).replace("<동결 커밋 12자리>", "2" * 12)
    text = text.replace("<세션 이름>", "official-20261127").replace("<세션 날짜 YYYYMMDD>", "20261127")
    text = text.replace('"<4절 2번 도구 면 측정의 cli_differences.baseline_only 이름들>"', '"2to3", "corepack"')
    counter = iter(range(1, 100))
    return json.loads(re.sub(r"sha256:<[^>]*>", lambda _: "sha256:" + str(next(counter) % 10) * 64, text))


class TestOfficialSplitExample(unittest.TestCase):
    def test_only_marked_fields_are_left_for_the_operator(self):
        marked = {path for path, value in strings(load(EXAMPLE)) if "<" in value}
        self.assertEqual(marked, FILL_PATHS)

    def test_filled_example_passes_the_runner_config_checks(self):
        config = parse_runner_config(filled())
        self.assertEqual(config.purpose, "official")
        self.assertIsNotNone(config.split)
        self.assertEqual((config.split.product_port, config.split.dns_control_port), (28787, 29083))
        self.assertEqual(config.fault_stall_seconds, 180)  # 멈춤 무효 T(고정 초안 §2)

    def test_common_fields_match_the_engine_direct_example(self):
        example, base = load(EXAMPLE), load(BASE_EXAMPLE)
        added = {"path_key_file", "preservation", "split"}
        self.assertEqual({key: value for key, value in example.items() if key not in added and key != "run"},
                         {key: value for key, value in base.items() if key != "run"})
        self.assertEqual({key: value for key, value in example["run"].items() if key != "fault_stall_seconds"}, base["run"])

    def test_no_secret_values(self):
        # 키·비밀번호는 회차 시작 때 따로 만든 파일이다. 예시에는 그 파일 경로만 있고 값은 없다.
        text = EXAMPLE.read_text(encoding="utf-8")
        for pattern in (r"sk-or-", r"BEGIN [A-Z ]*PRIVATE KEY", r"EVAL_LLM_API_KEY=", r"password\"\s*:\s*\"[^/<]"):
            self.assertIsNone(re.search(pattern, text), pattern)


if __name__ == "__main__":
    unittest.main()
