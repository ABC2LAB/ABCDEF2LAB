"""시험용 합성 경계 원자료(abc2lab.explore-boundary-export/v1). Go 경계(exploreboundary/evidence.go)가 쓰는 모양을 따른다.

실제 대상·인증값은 넣지 않는다. 경로는 계측기 fixture와 같은 키로 가린다.
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path

from boundary_export import FORMAT, MARKER, ROWS
from path_mask import key_ref, mask_path
from runner_evidence import PATH_PROFILE

TASK_ID = "7"
SESSION_REF = "session-1"
STAMP = "2026-11-16T00:00:00.5Z"


def approved(seq, raw_path, key, *, status=200, ingress="browser", method="GET"):
    row = {"session_ref": SESSION_REF, "seq": seq, "at": STAMP, "decision": "forwarded", "ingress": ingress,
           "dest_class": "approved", "method": method, "path_pattern": mask_path(raw_path, key)[0], "has_auth_header": False}
    if status:
        row.update(status=status, bytes=10)
    return row


def denied(seq, reason, *, dest="approved", ingress="direct"):
    return {"session_ref": SESSION_REF, "seq": seq, "at": STAMP, "decision": "denied", "reason": reason, "ingress": ingress,
            "dest_class": dest, "method": "GET", "has_auth_header": False}


def external(seq, host="cdn.example"):
    return {"session_ref": SESSION_REF, "seq": seq, "at": STAMP, "decision": "forwarded", "ingress": "browser",
            "dest_class": "external", "method": "GET", "status": 200, "bytes": 5, "external_host": host, "has_auth_header": False}


def write(directory: Path, rows: list[dict] | None, key: bytes, *, session=True, marker_changes=None, session_changes=None,
          raw_rows: bytes | None = None, rows_at_close=None) -> Path:
    """rows가 None이면 행 파일 없음, marker_changes에 MISSING을 주면 표식 없음."""
    directory.mkdir(mode=0o700, exist_ok=True)
    directory.chmod(0o700)
    data = raw_rows if raw_rows is not None else b"".join(json.dumps(r, separators=(",", ":")).encode() + b"\n" for r in rows or [])
    if rows is not None or raw_rows is not None:
        (directory / ROWS).write_bytes(data)
        (directory / ROWS).chmod(0o600)
    count = data.count(b"\n")
    sess = None
    if session:
        sess = {"session_ref": SESSION_REF, "run_ref": "run-1", "job_id": "job-1", "task_id": TASK_ID, "target_ref": "target-1",
                "target_revision": 2, "source_digest": "cd" * 32, "opened_at": STAMP, "closed_at": STAMP,
                "closed_reason": "completed", "rows_at_close": count if rows_at_close is None else rows_at_close, "closed_at_seal": False}
        sess.update(session_changes or {})
    marker = {"format": FORMAT, "path_profile": PATH_PROFILE, "path_key_ref": key_ref(key), "max_bytes": 1 << 20, "session": sess,
              "sealed_at": STAMP, "sealed_by": "shutdown", "rows": count, "bytes": len(data),
              "rows_sha256": hashlib.sha256(data).hexdigest(), "truncated": False, "rows_not_written": 0,
              "inflight_at_seal": 0, "log_complete": True}
    marker.update(marker_changes or {})
    if marker_changes != MISSING:
        (directory / MARKER).write_bytes(json.dumps(marker).encode() + b"\n")
        (directory / MARKER).chmod(0o600)
    return directory


MISSING = {"__missing__": True}
