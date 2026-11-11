"""E② 시도 경로 투영(server/e2_attempt_paths.go, abc2lab.e2-attempt-paths/v1)의 검증.

수용시험 회차(eval-e2 설정 v2)에만 있고, 러너가 E② 내보내기에서 사본으로 옮긴다. 이 모듈은 사본을 읽어
(1) 완료 표식이 있고 행 수·바이트 SHA-256이 기록과 같으며 (2) 행마다 정확한 8칸·같은 가림 판·같은 키 참조이고
(3) 행의 순번·결정이 감사 내보내기(attempts.jsonl)의 결과 시도와 1:1로 같은지 본다. 하나라도 어긋나면 그 회차의
경로 투영을 쓰지 않는다(진단 요청 반복 차단 수는 NOT_RUN). 경로 원문은 어디에도 없고, 결과에도 싣지 않는다.
"""

from __future__ import annotations

import hashlib
import json
import re

from path_mask import PATH_PROFILE

ATTEMPT_PATHS_FILE = "e2-attempt-paths.jsonl"
ATTEMPT_PATHS_DONE_FILE = "e2-attempt-paths.done.json"
ATTEMPT_PATHS_FORMAT = "abc2lab.e2-attempt-paths/v1"
MAX_ATTEMPT_PATHS_BYTES = 64 * 1024 * 1024
MAX_DONE_BYTES = 4 * 1024
_ROW_FIELDS = frozenset({"sequence", "decision", "reason", "method", "path_pattern", "masked_segments", "path_profile_ref", "path_key_ref"})
_DONE_FIELDS = frozenset({"format", "rows", "sha256"})
_SHA256 = re.compile(r"[0-9a-f]{64}\Z")
_KEY_REF = re.compile(r"[0-9a-f]{16}\Z")
_DECISIONS = frozenset({"allowed", "denied", "unsupported"})
# 감사 내보내기 행의 결과 단계(e2_export STAGE_RESULT와 같다).
_STAGE_RESULT = "result"


class AttemptPathsError(ValueError):
    """고정 코드만 담는다. 경로·값은 담지 않는다."""


def _fail(code: str) -> None:
    raise AttemptPathsError(code)


def _strict(line: bytes, code: str) -> dict:
    def unique(pairs):
        seen = {}
        for key, value in pairs:
            if key in seen:
                _fail(code)
            seen[key] = value
        return seen
    try:
        value = json.loads(line.decode("utf-8"), object_pairs_hook=unique, parse_constant=lambda _: _fail(code))
    except (UnicodeError, ValueError, RecursionError) as exc:
        if isinstance(exc, AttemptPathsError):
            raise
        _fail(code)
    if not isinstance(value, dict):
        _fail(code)
    return value


def _count(value: object) -> bool:
    return type(value) is int and value >= 0


def _result_decisions(attempts: bytes) -> dict[int, str]:
    """감사 내보내기의 결과 시도: 순번 → 결정. 내보내기 형식 검사는 e2_export.parse_attempts가 이미 했다."""
    decisions: dict[int, str] = {}
    for line in attempts.split(b"\n"):
        if not line:
            continue
        row = _strict(line, "attempt_paths_audit_invalid")
        if row.get("stage") == _STAGE_RESULT:
            sequence = row.get("sequence")
            if not _count(sequence) or sequence in decisions:
                _fail("attempt_paths_audit_invalid")
            decisions[sequence] = row.get("decision", "")
    return decisions


def parse_attempt_paths(rows: bytes | None, done: bytes | None, attempts: bytes, path_key_ref: str) -> list[dict]:
    """검증한 행 목록(순번순). 표식이 없으면(마감 실패 포함) 받지 않는다."""
    if rows is None or done is None:
        _fail("attempt_paths_missing")
    if type(path_key_ref) is not str or not _KEY_REF.match(path_key_ref):
        _fail("attempt_paths_key_ref_invalid")
    if len(rows) > MAX_ATTEMPT_PATHS_BYTES or len(done) > MAX_DONE_BYTES:
        _fail("attempt_paths_too_large")
    if not done.endswith(b"\n") or done.count(b"\n") != 1:
        _fail("attempt_paths_done_invalid")
    marker = _strict(done[:-1], "attempt_paths_done_invalid")
    if (set(marker) != _DONE_FIELDS or marker["format"] != ATTEMPT_PATHS_FORMAT or not _count(marker["rows"])
            or not isinstance(marker["sha256"], str) or not _SHA256.match(marker["sha256"])):
        _fail("attempt_paths_done_invalid")
    if hashlib.sha256(rows).hexdigest() != marker["sha256"]:
        _fail("attempt_paths_digest_mismatch")
    if rows and not rows.endswith(b"\n"):
        _fail("attempt_paths_truncated")
    parsed = [_strict(line, "attempt_paths_row_invalid") for line in rows.split(b"\n")[:-1]] if rows else []
    if len(parsed) != marker["rows"]:
        _fail("attempt_paths_row_count_mismatch")
    for row in parsed:
        pattern = row.get("path_pattern")
        if (set(row) != _ROW_FIELDS or not _count(row["sequence"]) or row["decision"] not in _DECISIONS
                or not isinstance(row["reason"], str) or not row["reason"] or not isinstance(row["method"], str)
                or not (pattern is None or isinstance(pattern, str) and pattern.startswith("/"))
                or not _count(row["masked_segments"]) or (pattern is None and row["masked_segments"] != 0)
                or row["path_profile_ref"] != PATH_PROFILE):
            _fail("attempt_paths_row_invalid")
        if row["path_key_ref"] != path_key_ref:
            _fail("attempt_paths_key_mismatch")
    by_sequence = {row["sequence"]: row["decision"] for row in parsed}
    if len(by_sequence) != len(parsed):
        _fail("attempt_paths_sequence_repeated")
    if by_sequence != _result_decisions(attempts):
        _fail("attempt_paths_audit_mismatch")
    return sorted(parsed, key=lambda row: row["sequence"])
