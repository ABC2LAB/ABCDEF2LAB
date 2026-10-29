"""평가 회차 manifest — 저장된 raw 한 회차를 채점기에 넘기는 메타데이터.

회차 1개 = 조건 1개 × 반복 1회(스캔 1회 = Juice Shop 리셋 1회, 프로토콜 §2·§4-5).
판정 레코드·스코어보드 정규화 스냅샷의 경로와 통제변수(model_tag·reset_generation)를
싣는다. 확정까지 시간·총 요청 수·차단 시도 수는 러너가 측정해 줄 때만 싣고, 없으면
None — 표에는 "—"로 나가고 값을 지어내지 않는다.

파일은 JSON: `{"runs": [회차, ...]}` 또는 회차 배열. 경로가 상대경로면 manifest 파일
위치 기준으로 푼다. 계약 밖 필드·타입 오류는 전부 모아 ManifestError(fail-closed).
"""

from __future__ import annotations

import json
import math
import os
from dataclasses import dataclass
from enum import Enum


class Engine(Enum):
    """회차를 낸 엔진. 채점에서 갈리는 건 '레코드 → 판정' 어댑터 하나뿐이다(score.py)."""

    OURS = "ours"  # 우리 포크 — 판정 레코드(verdict-record) 출력
    BASELINE = "baseline"  # 원본 ARTEX — 어댑터 미구현(A5 보류)


_ENGINES = {e.value: e for e in Engine}


class ManifestError(ValueError):
    """manifest 모양·타입 오류. 메시지에 회차별 사유를 전부 모아 담는다."""


@dataclass(frozen=True)
class RunManifest:
    """회차 1개. 경로는 절대경로로 풀어 둔다."""

    condition: str  # 매트릭스 행 이름, 예 "우리@35b"
    engine: Engine
    model_tag: str  # 통제변수 — 비교 대상 전 회차가 같은 문자열이어야(§6)
    reset_generation: str  # Juice Shop reset 세대 — 회차마다 달라야(§4-5)
    repeat_index: int
    records_path: str  # 판정 레코드 JSON (verdict-record)
    snapshot_path: str  # 스코어보드 정규화 스냅샷 (§2)
    elapsed_seconds: float | None = None  # 확정까지 시간 — 미측정이면 None
    # 총 요청 수 = 실제로 대상에 전송된 HTTP만(traffic 기록분). E² 차단 시도·LLM 호출 제외(§5).
    total_requests: int | None = None
    blocked_attempts: int | None = None  # E²가 막은 전송 시도 수 — 별도 지표, 총 요청 수에 섞지 않음(§5)


_REQUIRED_STR = ("condition", "model_tag", "reset_generation", "records_path", "snapshot_path")
_COUNTS = ("total_requests", "blocked_attempts")
_FIELDS = frozenset(_REQUIRED_STR + _COUNTS + ("engine", "repeat_index", "elapsed_seconds"))


def load_manifests(path: str) -> list[RunManifest]:
    """manifest 파일을 읽어 회차 목록으로. 하나라도 틀리면 전부 모아 ManifestError."""
    with open(path, encoding="utf-8") as f:
        try:
            payload = json.load(f)
        except json.JSONDecodeError as e:
            raise ManifestError(f"manifest JSON 파싱 실패: {e}") from e
    if isinstance(payload, dict):
        extra = sorted(k for k in payload if k != "runs")
        if extra or "runs" not in payload:
            raise ManifestError(f"최상위 객체는 'runs'만 가져야 함 (모르는 키: {extra})")
        payload = payload["runs"]
    if not isinstance(payload, list) or not payload:
        raise ManifestError("회차 배열이 아니거나 비어 있음")

    base = os.path.dirname(os.path.abspath(path))
    errors: list[str] = []
    runs: list[RunManifest] = []
    for i, raw in enumerate(payload):
        run, run_errors = _parse_run(i, raw, base)
        errors.extend(run_errors)
        if run is not None:
            runs.append(run)
    if errors:
        raise ManifestError(f"manifest 오류 {len(errors)}건:\n  " + "\n  ".join(errors))
    return runs


def _parse_run(index: int, raw: object, base: str) -> tuple[RunManifest | None, list[str]]:
    if not isinstance(raw, dict):
        return None, [f"[{index}] 회차가 객체가 아님: {type(raw).__name__}"]
    errors = [f"[{index}] 모르는 필드: '{k}'" for k in raw if k not in _FIELDS]

    for name in _REQUIRED_STR:
        value = raw.get(name)
        if not isinstance(value, str) or not value:
            errors.append(f"[{index}] {name} 부재/빈값: {value!r}")

    engine = _ENGINES.get(raw.get("engine"))
    if engine is None:
        errors.append(f"[{index}] engine 부정: {raw.get('engine')!r} (허용: {sorted(_ENGINES)})")

    repeat_index = raw.get("repeat_index")
    if not _is_int(repeat_index) or repeat_index < 0:
        errors.append(f"[{index}] repeat_index는 0 이상 정수: {repeat_index!r}")

    elapsed = raw.get("elapsed_seconds")
    if elapsed is not None and (not _is_finite_number(elapsed) or elapsed < 0):
        errors.append(f"[{index}] elapsed_seconds는 null 또는 0 이상 유한수: {elapsed!r}")

    for name in _COUNTS:
        count = raw.get(name)
        if count is not None and (not _is_int(count) or count < 0):
            errors.append(f"[{index}] {name}는 null 또는 0 이상 정수: {count!r}")

    if errors:
        return None, errors
    return RunManifest(
        condition=raw["condition"],
        engine=engine,
        model_tag=raw["model_tag"],
        reset_generation=raw["reset_generation"],
        repeat_index=repeat_index,
        records_path=os.path.normpath(os.path.join(base, raw["records_path"])),
        snapshot_path=os.path.normpath(os.path.join(base, raw["snapshot_path"])),
        elapsed_seconds=None if elapsed is None else float(elapsed),
        total_requests=raw.get("total_requests"),
        blocked_attempts=raw.get("blocked_attempts"),
    ), []


def _is_int(value: object) -> bool:
    # bool은 int의 하위형이라 True가 1로 새지 않게 따로 막는다.
    return isinstance(value, int) and not isinstance(value, bool)


def _is_finite_number(value: object) -> bool:
    # json.load는 NaN·Infinity를 받아들인다 — 측정값으로는 거부한다.
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)
