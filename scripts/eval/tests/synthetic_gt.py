"""평가 스크립트 시험은 합성 정답 목록으로 돈다(2026-11-18 사용자 지시, 정답표 노출 방지).

이 모듈을 불러오면 ground_truth가 실제 정답 대신 tests/data/synthetic-gt.json(실제 목록과 같은 순서·묶음·축 구성, 이름만
합성)을 쓴다. 정답 목록에 기대는 시험 모듈은 맨 앞에서 이 모듈을 불러온다(모듈 수준에서 목록을 읽는 시험이 있어 순서가
중요하다). 실제 정답이 꼭 필요한 시험은 evaluation-gt/ 아래 시험 경로에 있다.
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import ground_truth  # noqa: E402

SYNTHETIC_GT = Path(__file__).resolve().parent / "data" / "synthetic-gt.json"
ground_truth.use_ground_truth_file(SYNTHETIC_GT)
