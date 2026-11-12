"""진단 요청 반복 차단 수(docs/evaluation/browser-acceptance.md §6, 개선 후 회차만).

러너가 옮긴 E② 수집 사본(회차 디렉터리의 `e2-copy/`)을 읽는다.
- 주 지표: E② 시도 경로 투영(e2_attempt_paths)의 거부 행(decision이 denied·unsupported)을 (사유, 메서드, 가린 경로 패턴)으로 묶어,
  2회 이상 나온 묶음 수와 그 묶음들의 거부 수 합을 낸다. 요청 URL이 없던 거부(경로 패턴 null)는 묶지 않고 '경로 미확보'로 센다.
  투영이 없거나 검증되지 않으면 NOT_RUN이다(호스트가 같다고 추정하지 않는다).
- 보조 지표: 감사 내보내기(attempts.jsonl)의 결과 시도 중 거부를 (사유, 메서드, 규칙 참조)로 같은 방식으로 묶는다.
  경로를 보지 않아 거친 값이고, 투영이 없는 회차에도 낼 수 있다.
E②까지 온 진단 요청의 거부만 센다. 탐색 경계 차단은 이 사본에 없다. 결과에는 건수만 싣고 경로 패턴·규칙 참조 값은 싣지 않는다.
"""

from __future__ import annotations

import os
import stat
from collections import Counter
from pathlib import Path

from e2_attempt_paths import ATTEMPT_PATHS_DONE_FILE, ATTEMPT_PATHS_FILE, MAX_ATTEMPT_PATHS_BYTES, MAX_DONE_BYTES, AttemptPathsError, parse_attempt_paths
from e2_export import ATTEMPTS_FILE, STAGE_RESULT, STATE_COMPLETE, E2ExportError, parse_attempts, strict_object

ATTEMPTS_COPY = ATTEMPTS_FILE
MANIFEST_COPY = "manifest.json"
MAX_ATTEMPTS_BYTES = 256 << 20
MAX_MANIFEST_BYTES = 64 * 1024
REFUSALS = frozenset({"denied", "unsupported"})
NOT_RUN = "NOT_RUN"
OK = "ok"


def _read(directory: Path, name: str, maximum: int) -> bytes | None:
    """링크·특수 파일은 받지 않는다. 없으면 None이다."""
    try:
        fd = os.open(directory / name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK)
    except FileNotFoundError:
        return None
    except OSError:
        raise AttemptPathsError("e2_copy_unreadable") from None
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise AttemptPathsError("e2_copy_unreadable")
        data = os.read(fd, maximum + 1)
    finally:
        os.close(fd)
    if len(data) > maximum:
        raise AttemptPathsError("e2_copy_too_large")
    return data


def _repeats(keys: list[tuple]) -> tuple[int, int]:
    counts = Counter(keys)
    repeated = [count for count in counts.values() if count >= 2]
    return len(repeated), sum(repeated)


def _not_run(reason: str, paths: bool = False) -> dict:
    result = {"status": NOT_RUN, "reason": reason, "refusals": None, "repeated_groups": None, "repeated_refusals": None}
    if paths:
        result["path_unavailable"] = None
    return result


def _audit_refusals(attempts: bytes, manifest_data: bytes) -> list[dict]:
    """감사 내보내기의 결과 시도 중 거부. 형식·필수 칸·열거값·순번·digest는 러너 수집과 같은 계약(e2_export)으로 다시 본다.

    검증하지 못한 감사를 '거부 0건'으로 세지 않는다. 결과가 complete가 아닌 내보내기도 받지 않는다.
    """
    try:
        manifest = strict_object(manifest_data)
        complete = manifest.get("state") == STATE_COMPLETE
        parse_attempts(attempts, manifest)
        rows = [strict_object(line) for line in attempts.split(b"\n") if line]
    except (E2ExportError, KeyError, TypeError, ValueError):
        raise AttemptPathsError("e2_attempts_invalid") from None
    if not complete:
        raise AttemptPathsError("e2_export_incomplete")
    return [row for row in rows if row["stage"] == STAGE_RESULT and row["decision"] in REFUSALS]


def summarize_round(copy_dir: Path | None, path_key_ref: str) -> dict:
    """회차 하나의 반복 차단 수. paths(주 지표)와 rule_refs(보조 지표)는 따로 NOT_RUN일 수 있다."""
    if copy_dir is None:
        return {"paths": _not_run("e2_copy_not_given", True), "rule_refs": _not_run("e2_copy_not_given")}
    try:
        attempts = _read(copy_dir, ATTEMPTS_COPY, MAX_ATTEMPTS_BYTES)
        manifest = _read(copy_dir, MANIFEST_COPY, MAX_MANIFEST_BYTES)
        if attempts is None or manifest is None:
            raise AttemptPathsError("e2_attempts_missing")
        audit = _audit_refusals(attempts, manifest)
    except AttemptPathsError as exc:
        return {"paths": _not_run(str(exc), True), "rule_refs": _not_run(str(exc))}
    groups, total = _repeats([(row.get("reason", ""), row.get("method", ""), row.get("rule_ref", "")) for row in audit])
    rule_refs = {"status": OK, "reason": None, "refusals": len(audit), "repeated_groups": groups, "repeated_refusals": total}
    try:
        rows = parse_attempt_paths(_read(copy_dir, ATTEMPT_PATHS_FILE, MAX_ATTEMPT_PATHS_BYTES),
                                   _read(copy_dir, ATTEMPT_PATHS_DONE_FILE, MAX_DONE_BYTES), attempts, path_key_ref)
    except AttemptPathsError as exc:
        return {"paths": _not_run(str(exc), True), "rule_refs": rule_refs}
    refusals = [row for row in rows if row["decision"] in REFUSALS]
    located = [row for row in refusals if row["path_pattern"] is not None]
    groups, total = _repeats([(row["reason"], row["method"], row["path_pattern"]) for row in located])
    paths = {"status": OK, "reason": None, "refusals": len(refusals), "repeated_groups": groups, "repeated_refusals": total,
             "path_unavailable": len(refusals) - len(located)}
    return {"paths": paths, "rule_refs": rule_refs}
