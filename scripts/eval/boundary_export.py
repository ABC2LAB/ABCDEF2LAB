"""탐색 경계 원자료(abc2lab.explore-boundary-export/v1, docs/contracts/exploration-boundary.md §4.1)의 검증.

경계가 러너 근거 디렉터리에 쓴 두 파일을 경계 종료(`down`) 뒤 최종 바이트로 읽는다. 표식의 카운터는 믿지 않고 행에서
다시 센다. 차단 수는 E② 진단 차단·반복 차단 수와 따로 두고 합치지 않는다. 원자료를 못 쓴 회차(세션 없음·잘림)의
수는 0으로 채우지 않고 null이다.

계측기 대조(수용시험 P03)는 탐색 입구 트래픽이 경계를 지났는지의 장부 대조다. 네트워크 격리 증명이 아니다.
GT를 읽지 않는다.
"""
from __future__ import annotations

import hashlib
import json
import re
from collections import Counter
from pathlib import Path

from runner_evidence import FORWARDED_KINDS, PATH_PROFILE, MAX_METER_FILE_BYTES, _read_owned

FORMAT = "abc2lab.explore-boundary-export/v1"
ROWS = "boundary-records.jsonl"
MARKER = "boundary-sealed.json"
FILES = (ROWS, MARKER)

# 경계 차단 사유(exploreboundary/policy.go Reason*·session.go ReasonUnresolved·ReasonRequestCancelled). Go 쪽과 같은지
# 시험으로 고정한다.
REASONS = frozenset({
    "out_of_scope_origin", "out_of_scope_path", "method_not_approved", "risky_path", "internal_address", "metadata_address",
    "control_endpoint", "external_blocked_local", "external_direct", "external_navigation", "external_method", "cap_exhausted",
    "session_closed", "target_ended", "ws_read_only", "protocol_unsupported", "unresolved_host", "request_cancelled",
    "login_attempts_exhausted", "login_verification_pending", "browser_account_in_use"})
# 세션 마감 사유(exploration/session.go Reason). 봉인은 열린 세션을 cancelled로 닫으므로 봉인 표식의 세션은 늘 닫혀 있다.
CLOSE_REASONS = frozenset({"completed", "model_error", "runtime_unavailable", "cancelled", "target_ended", "cap_exhausted",
                           "boundary_unavailable"})
INGRESSES = ("browser", "direct")
DESTS = frozenset({"approved", "external", "internal", "metadata", "control"})
SEALED_BY = frozenset({"shutdown", "failure"})
# 세션이 닫힌 세부 원인(exploreboundary/session.go CloseCause*, 2026-11-24 세션 수명). 보통 마감은 칸이 없다.
CLOSED_BY = frozenset({"restart_cleanup", "control_lost"})

_ROW_REQUIRED = frozenset({"session_ref", "seq", "at", "decision", "ingress", "dest_class", "method", "has_auth_header"})
# Go Record의 omitempty 칸. 빠지면 0·빈 값이다.
_ROW_OPTIONAL = frozenset({"reason", "status", "bytes", "path_pattern", "external_host", "fetch_dest", "fetch_mode"})
_MARKER_FIELDS = frozenset({"format", "path_profile", "path_key_ref", "max_bytes", "session", "sealed_at", "sealed_by", "rows",
                            "bytes", "rows_sha256", "truncated", "rows_not_written", "inflight_at_seal", "log_complete"})
_BINDING_FIELDS = ("session_ref", "run_ref", "job_id", "task_id", "target_ref", "target_revision", "source_digest")
_SESSION_FIELDS = frozenset(_BINDING_FIELDS) | {"opened_at", "closed_at", "closed_reason", "rows_at_close", "closed_at_seal"}
_HEX = re.compile(r"[0-9a-f]{64}\Z")
_KEY_REF = re.compile(r"[0-9a-f]{16}\Z")
_REF = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}\Z")
_TASK = re.compile(r"[1-9][0-9]{0,18}\Z")
# Go time.Time JSON(RFC 3339, UTC로 쓴 값)이다.
_STAMP = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?Z\Z")
# 경계가 기록 직전에 줄인 메서드(record_bounds.go boundedMethod): 짧은 토큰이거나 INVALID.
_METHOD = re.compile(r"[A-Za-z_-]{1,16}\Z")
_TEXT_LIMIT = 1024


class BoundaryExportError(ValueError):
    """고정 코드만 담는다. 경로·값을 넣지 않는다."""


def _fail(code: str):
    raise BoundaryExportError(code)


def _unique(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            _fail("boundary_export_invalid")
        out[key] = value
    return out


def _json(raw: bytes):
    try:
        return json.loads(raw.decode("utf-8"), object_pairs_hook=_unique,
                          parse_constant=lambda _: _fail("boundary_export_invalid"))
    except BoundaryExportError:
        raise
    except (UnicodeError, ValueError):
        _fail("boundary_export_invalid")


def _count(value) -> bool:
    return type(value) is int and value >= 0


def _digest(value) -> str:
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()


def _enum(value, allowed) -> bool:
    """닫힌 어휘 칸은 문자열일 때만 집합에 묻는다. 배열·객체가 오면 unhashable TypeError로 새지 않고 형식 오류가 된다."""
    return isinstance(value, str) and value in allowed


def _marker(raw: bytes) -> dict:
    m = _json(raw)
    if (not isinstance(m, dict) or set(m) != _MARKER_FIELDS or m["format"] != FORMAT or m["path_profile"] != PATH_PROFILE
            or not isinstance(m["path_key_ref"], str) or not _KEY_REF.fullmatch(m["path_key_ref"])
            or type(m["max_bytes"]) is not int or not 0 < m["max_bytes"] <= MAX_METER_FILE_BYTES
            or not isinstance(m["sealed_at"], str) or not _STAMP.fullmatch(m["sealed_at"]) or not _enum(m["sealed_by"], SEALED_BY)
            or not all(_count(m[k]) for k in ("rows", "bytes", "rows_not_written", "inflight_at_seal"))
            or not isinstance(m["rows_sha256"], str) or not _HEX.fullmatch(m["rows_sha256"])
            or type(m["truncated"]) is not bool or type(m["log_complete"]) is not bool
            or m["truncated"] != (m["rows_not_written"] > 0) or m["bytes"] > m["max_bytes"]):
        _fail("boundary_export_invalid")
    s = m["session"]
    if s is None:
        return m
    if (not isinstance(s, dict) or set(s) - {"closed_by"} != _SESSION_FIELDS or ("closed_by" in s and not _enum(s["closed_by"], CLOSED_BY))
            or any(not isinstance(s[k], str) or not _REF.fullmatch(s[k]) for k in ("session_ref", "run_ref", "job_id", "target_ref"))
            or not isinstance(s["task_id"], str) or not _TASK.fullmatch(s["task_id"])
            or type(s["target_revision"]) is not int or s["target_revision"] < 1
            or not isinstance(s["source_digest"], str) or not _HEX.fullmatch(s["source_digest"])
            or not isinstance(s["opened_at"], str) or not _STAMP.fullmatch(s["opened_at"])
            # 봉인은 열린 세션을 닫은 뒤 쓰므로 표식의 세션은 늘 닫혀 있다.
            or not isinstance(s["closed_at"], str) or not _STAMP.fullmatch(s["closed_at"]) or not _enum(s["closed_reason"], CLOSE_REASONS)
            or not _count(s["rows_at_close"]) or s["rows_at_close"] > m["rows"] + m["rows_not_written"]
            or type(s["closed_at_seal"]) is not bool):
        _fail("boundary_export_invalid")
    return m


def _row(row, session_ref: str) -> dict:
    if not isinstance(row, dict) or not _ROW_REQUIRED <= set(row) <= _ROW_REQUIRED | _ROW_OPTIONAL:
        _fail("boundary_export_invalid")
    forwarded = row["decision"] == "forwarded"
    if (row["session_ref"] != session_ref or type(row["seq"]) is not int or row["seq"] < 1
            or not isinstance(row["at"], str) or not _STAMP.fullmatch(row["at"])
            or not _enum(row["decision"], ("forwarded", "denied")) or not _enum(row["ingress"], INGRESSES)
            or not _enum(row["dest_class"], DESTS)
            or not isinstance(row["method"], str) or not _METHOD.fullmatch(row["method"]) or type(row["has_auth_header"]) is not bool
            or any(k in row and (not isinstance(row[k], str) or not row[k] or len(row[k].encode()) > _TEXT_LIMIT)
                   for k in ("path_pattern", "external_host", "fetch_dest", "fetch_mode"))
            or "status" in row and (type(row["status"]) is not int or not 100 <= row["status"] <= 999)
            or "bytes" in row and (type(row["bytes"]) is not int or row["bytes"] < 1)):
        _fail("boundary_export_invalid")
    if forwarded:
        # 전달은 승인 대상·외부 자원만이다. 승인 대상 전달 행은 가린 경로가, 외부 자원 행은 호스트가 있다.
        if ("reason" in row or row["dest_class"] not in ("approved", "external")
                or row["dest_class"] == "approved" and ("path_pattern" not in row or "external_host" in row)
                or row["dest_class"] == "external" and ("external_host" not in row or "path_pattern" in row)):
            _fail("boundary_export_invalid")
    elif not _enum(row.get("reason"), REASONS) or "status" in row or "bytes" in row:
        _fail("boundary_export_invalid")
    return row


def _rows(raw: bytes, session_ref: str | None) -> list[dict]:
    if raw and not raw.endswith(b"\n"):
        _fail("boundary_export_invalid")
    if raw and session_ref is None:
        # 세션을 열지 않았는데 행이 있으면 어느 결속의 행인지 알 수 없다.
        _fail("boundary_binding_mismatch")
    rows = [_row(_json(line), session_ref) for line in raw.splitlines()] if raw else []
    if len({row["seq"] for row in rows}) != len(rows):
        _fail("boundary_export_invalid")
    return rows


def _counters(rows: list[dict], rows_at_close: int) -> dict:
    forwarded = {dest: {ingress: 0 for ingress in INGRESSES} for dest in ("approved", "external")}
    by_reason = {reason: 0 for reason in sorted(REASONS)}
    denied_by_ingress = {ingress: 0 for ingress in INGRESSES}
    for row in rows:
        if row["decision"] == "forwarded":
            forwarded[row["dest_class"]][row["ingress"]] += 1
        else:
            by_reason[row["reason"]] += 1
            denied_by_ingress[row["ingress"]] += 1
    approved = [r for r in rows if r["decision"] == "forwarded" and r["dest_class"] == "approved"]
    return {"forwarded": forwarded,
            "forwarded_no_response": sum("status" not in r for r in approved),
            "denied": {"total": sum(by_reason.values()), "by_reason": by_reason, "by_ingress": denied_by_ingress},
            "external_hosts": len({r["external_host"] for r in rows if r["decision"] == "forwarded" and r["dest_class"] == "external"}),
            "auth_header_rows": sum(r["has_auth_header"] for r in approved),
            "rows_after_close": max(0, len(rows) - rows_at_close)}


def _reconcile(rows: list[dict], meter_rows: list[dict], meter_paths: list[dict]) -> dict:
    """수용시험 P03. 탐색 요청에는 E② 시도 ID가 없어 (메서드, 가린 경로)별 건수로 맞춘다.

    - 계측기에만 있는 탐색 입구 행이 하나라도 있으면 경계를 거치지 않은 것이다.
    - 경계에만 있는 승인 대상 전달 행은 경계가 응답을 못 받은 전달(status 없음)과 계측기 봉인 뒤 거부(경로 행이 없어
      건수로만)로만 설명된다. 응답을 받은 행은 봉인 뒤 거부로만 설명된다(같은 묶음의 응답 없는 행을 먼저 뺀다).
    """
    approved = [r for r in rows if r["decision"] == "forwarded" and r["dest_class"] == "approved"]
    boundary = Counter((r["method"], r["path_pattern"]) for r in approved)
    no_response = Counter((r["method"], r["path_pattern"]) for r in approved if "status" not in r)
    meter = Counter((r["method"], r["path_pattern"]) for r in meter_paths if r.get("ingress") == "explore")
    rejected = sum(r.get("ingress") == "explore" and r["kind"] not in FORWARDED_KINDS for r in meter_rows)
    if sum((meter - boundary).values()):
        _fail("boundary_meter_mismatch")
    unmatched = boundary - meter
    only = sum(unmatched.values())
    responded_only = sum(max(0, count - no_response[key]) for key, count in unmatched.items())
    if only > sum(no_response.values()) + rejected or responded_only > rejected:
        _fail("boundary_meter_mismatch")
    return {"meter_explore_forwarded": sum(meter.values()), "meter_explore_rejected": rejected, "boundary_only_forwarded": only}


def verify(directory: Path, meter_rows: list[dict], meter_paths: list[dict], *, path_key_ref: str, task_id: str | None) -> dict:
    """경계 원자료 한 회차의 서명 전 요약. task_id는 러너가 만든 작업이다(재검증은 서명된 결속 hash로 대신 맞춘다)."""
    raw_rows, raw_marker = (_read_owned(directory, name) for name in FILES)
    if raw_marker is None:
        # 행 파일만 있으면 경계가 봉인 전에 멈춘 것이다.
        _fail("boundary_export_incomplete" if raw_rows is not None else "boundary_export_missing")
    if raw_rows is None:
        _fail("boundary_export_missing")
    marker = _marker(raw_marker)
    if (marker["rows_sha256"] != hashlib.sha256(raw_rows).hexdigest() or marker["bytes"] != len(raw_rows)
            or marker["rows"] != raw_rows.count(b"\n")):
        _fail("boundary_export_invalid")
    if not marker["log_complete"]:
        _fail("boundary_sink_failed")
    if marker["sealed_by"] != "shutdown" or marker["inflight_at_seal"] != 0:
        _fail("boundary_export_incomplete")
    if marker["path_key_ref"] != path_key_ref or any(r["path_key_ref"] != path_key_ref for r in meter_paths):
        _fail("boundary_path_key_mismatch")
    session = marker["session"]
    rows = _rows(raw_rows, None if session is None else session["session_ref"])
    files = {name: {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)} for name, raw in ((ROWS, raw_rows), (MARKER, raw_marker))}
    explore_rows = sum(r.get("ingress") == "explore" for r in meter_rows)
    base = {"format": FORMAT, "files": files, "path_key_ref": path_key_ref}
    if session is None:
        # 세션을 못 연 회차에 탐색 입구 트래픽이 있으면 경계를 거치지 않은 것이다.
        if explore_rows:
            _fail("boundary_meter_mismatch")
        return base | {"status": "not_opened", "binding_sha256": None, "closed_reason": None, "counters": None, "reconciliation": None}
    if task_id is not None and session["task_id"] != task_id:
        _fail("boundary_binding_mismatch")
    out = base | {"binding_sha256": _digest({k: session[k] for k in _BINDING_FIELDS}), "closed_reason": session["closed_reason"]}
    if marker["truncated"]:
        # 잘린 원자료로는 차단 수도 대조도 확정할 수 없다. 0으로 채우지 않는다.
        return out | {"status": "truncated", "counters": None, "reconciliation": None}
    # 잘리지 않고 진행 중 0으로 봉인한 원자료에는 경계가 세션에서 준 순번(1부터 연속)이 모두 있어야 한다. 전달 행은
    # 끝날 때 쓰므로 물리적 순서는 동시 완료로 바뀔 수 있어 집합으로 본다. 빠진 번호는 행 손실이다(#617 리뷰: 중간
    # 차단 행을 빼고 행 수·hash를 맞춘 원자료가 차단 수를 줄인 채 verified로 통과했다).
    if {row["seq"] for row in rows} != set(range(1, len(rows) + 1)):
        _fail("boundary_export_incomplete")
    return out | {"status": "verified", "counters": _counters(rows, session["rows_at_close"]),
                  "reconciliation": _reconcile(rows, meter_rows, meter_paths)}


def validate_block(block) -> None:
    """서명된 sidecar 안 요약 블록의 모양. 값의 의미는 verify 재계산과 같음으로 확인한다."""
    keys = {"format", "files", "path_key_ref", "status", "binding_sha256", "closed_reason", "counters", "reconciliation"}
    if (not isinstance(block, dict) or set(block) != keys or block["format"] != FORMAT
            or block["status"] not in ("verified", "not_opened", "truncated")
            or not isinstance(block["path_key_ref"], str) or not _KEY_REF.fullmatch(block["path_key_ref"])
            or not isinstance(block["files"], dict) or set(block["files"]) != set(FILES)
            or any(not isinstance(v, dict) or set(v) != {"sha256", "bytes"} or not isinstance(v["sha256"], str)
                   or not _HEX.fullmatch(v["sha256"]) or not _count(v["bytes"]) for v in block["files"].values())
            or (block["status"] == "verified") != isinstance(block["counters"], dict)
            or (block["status"] == "verified") != isinstance(block["reconciliation"], dict)
            or (block["status"] == "not_opened") != (block["binding_sha256"] is None)
            or block["binding_sha256"] is not None and (not isinstance(block["binding_sha256"], str) or not _HEX.fullmatch(block["binding_sha256"]))):
        _fail("boundary_export_invalid")
