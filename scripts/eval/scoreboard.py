"""스코어보드 스냅샷 — Juice Shop `/api/Challenges` 해결 상태의 정규화·검증.

평가 프로토콜 §2: 스캔 직후(컨테이너 제거 전) `/api/Challenges` 응답을 **원본**과
**정규화**(`{챌린지 key: solved(bool)}`) 두 벌로 저장하고, 채점은 정규화본만 읽는다.
정규화는 원본에서 결정적으로 만든다 — 그 규칙이 `normalize_challenges`이고, 실시간
reader(collect.read_solved_state)도 같은 함수로 행을 파싱한다.

fail-closed(절대규칙 5): 모양이 어긋나거나 key 중복·solved가 불리언이 아니면 조용히
미해결로 넘기지 않고 SnapshotError. GT 챌린지 key가 빠진 스냅샷도 에러다 — 빠진 걸
미해결로 보면 깨진 스냅샷이 미탐으로 숨는다(§2).
"""

from __future__ import annotations

import json
from collections.abc import Iterable

from ground_truth import Challenge
from manifest import RunManifest


class SnapshotError(ValueError):
    """스냅샷 모양·내용 오류. 메시지에 사유를 전부 모아 담는다."""


class _Pairs(list):
    """json object_pairs_hook 결과 — JSON 객체를 (key, value) 목록으로 보존한다.

    dict로 바로 받으면 중복 key가 조용히 마지막 값으로 덮인다. 배열과 구분하려고 별도 타입.
    """


def normalize_challenges(payload: object) -> dict[str, bool]:
    """`/api/Challenges` 응답 본문 → `{key: solved}`. 실시간 reader와 저장 원본이 공유한다.

    최상위는 행 배열 또는 `{"data": [...]}`. 각 행은 비지 않은 문자열 `key`와 불리언
    `solved`를 가져야 한다 — `"false"` 같은 문자열을 bool()로 참으로 바꾸지 않는다.
    """
    if isinstance(payload, dict):
        if "data" not in payload:
            raise SnapshotError("응답 객체에 'data' 행 배열이 없음")
        rows = payload["data"]
    else:
        rows = payload
    if not isinstance(rows, list):
        raise SnapshotError(f"챌린지 행이 배열이 아님: {type(rows).__name__}")

    errors: list[str] = []
    solved: dict[str, bool] = {}
    for i, row in enumerate(rows):
        if not isinstance(row, dict):
            errors.append(f"[{i}] 행이 객체가 아님: {type(row).__name__}")
            continue
        key, state = row.get("key"), row.get("solved")
        if not isinstance(key, str) or not key:
            errors.append(f"[{i}] key 부재/빈값: {key!r}")
            continue
        if not isinstance(state, bool):
            errors.append(f"[{i}] '{key}' solved가 불리언 아님: {state!r}")
            continue
        if key in solved:
            errors.append(f"[{i}] key 중복: '{key}'")
            continue
        solved[key] = state
    if errors:
        raise SnapshotError(f"챌린지 행 오류 {len(errors)}건:\n  " + "\n  ".join(errors))
    return solved


def parse_scoreboard_snapshot(path: str, manifest: RunManifest | None = None) -> dict[str, bool]:
    """정규화 스냅샷을 읽는다. 채점 시 회차 envelope도 manifest와 대조한다."""
    with open(path, encoding="utf-8") as f:
        try:
            payload = json.load(f, object_pairs_hook=_Pairs)
        except json.JSONDecodeError as e:
            raise SnapshotError(f"스냅샷 JSON 파싱 실패: {e}") from e
    if not isinstance(payload, _Pairs):
        raise SnapshotError(f"정규화 스냅샷 최상위가 객체가 아님: {type(payload).__name__}")

    if manifest is not None:
        envelope: dict[str, object] = {}
        errors = []
        for key, value in payload:
            if key in envelope:
                errors.append(f"snapshot envelope key 중복: '{key}'")
            elif key not in {"run_id", "reset_generation", "solved"}:
                errors.append(f"snapshot envelope 모르는 필드: '{key}'")
            else:
                envelope[key] = value
        for name in ("run_id", "reset_generation"):
            expected = getattr(manifest, name)
            if envelope.get(name) != expected:
                errors.append(f"snapshot envelope {name} 불일치/부재: manifest={expected!r}, snapshot={envelope.get(name)!r}")
        if not isinstance(envelope.get("solved"), _Pairs):
            errors.append("snapshot envelope solved 객체 부재/부정")
        if errors:
            raise SnapshotError("; ".join(errors))
        payload = envelope["solved"]

    errors: list[str] = []
    solved: dict[str, bool] = {}
    for key, state in payload:
        if not key:
            errors.append("빈 key")
            continue
        if not isinstance(state, bool):
            errors.append(f"'{key}' solved가 불리언 아님: {state!r}")
            continue
        if key in solved:
            errors.append(f"key 중복: '{key}'")
            continue
        solved[key] = state
    if errors:
        raise SnapshotError(f"정규화 스냅샷 오류 {len(errors)}건:\n  " + "\n  ".join(errors))
    return solved


def require_basis_keys(solved: dict[str, bool], basis: Iterable[Challenge]) -> None:
    """채점 분모의 GT 챌린지 key가 스냅샷에 전부 있는지. 빠지면 에러(미해결로 간주 X, §2)."""
    missing = sorted({c.key for c in basis} - solved.keys())
    if missing:
        raise SnapshotError(f"스냅샷에 GT 챌린지 key 누락: {missing} — 미해결로 간주하지 않음")
