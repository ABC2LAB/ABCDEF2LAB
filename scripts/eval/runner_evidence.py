"""회차 근거 저장과 계측기 기록 읽기.

근거 파일은 회차 디렉터리(0700) 안에 O_EXCL·0600으로 한 번만 쓴다. 같은 이름을 다시 쓰면 실패다.
계측기·관측 front가 쓴 파일은 러너 uid 소유 일반 파일만 읽는다(심볼릭 링크·다른 소유자 거부).
값은 비민감 메타데이터뿐이다. 경로·쿼리·헤더·본문·토큰은 계측기가 애초에 기록하지 않는다.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
from pathlib import Path

ENGINE_LOG = "engine-requests.jsonl"
SEALED_MARKER = "engine-sealed.json"
DRAINED_MARKER = "engine-drained.json"
TERMINATED_MARKER = "engine-terminated.json"
OBSERVER_LOG = "observer-requests.jsonl"
MAX_METER_FILE_BYTES = 64 * 1024 * 1024
_NAME = re.compile(r"[a-z0-9][a-z0-9-]{0,62}\.json\Z")
_ENGINE_ROW = frozenset({"seq", "kind", "method", "status", "started_at", "completed_at", "response_bytes", "outcome"})
_OBSERVER_ROW = frozenset({"seq", "decision", "method", "status", "started_at", "completed_at", "response_bytes"})


class EvidenceError(RuntimeError):
    """근거 저장·읽기 실패. 고정 코드만 담는다."""


def _open_dir(path: Path) -> int:
    try:
        fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    except OSError:
        raise EvidenceError("evidence_dir_unavailable") from None
    info = os.fstat(fd)
    if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
        os.close(fd)
        raise EvidenceError("evidence_dir_not_private")
    return fd


class EvidenceDir:
    """회차 디렉터리에 근거 JSON을 한 번씩만 쓴다. 쓴 파일의 SHA-256을 기억해 서명 대상 목록을 만든다."""

    def __init__(self, path: Path):
        self.path = path
        self._digests: dict[str, str] = {}

    def write_json(self, name: str, payload: object) -> str:
        if not _NAME.fullmatch(name):
            raise EvidenceError("evidence_name_invalid")
        try:
            data = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False,
                              allow_nan=False).encode("utf-8") + b"\n"
        except (TypeError, ValueError):
            raise EvidenceError("evidence_payload_invalid") from None
        directory = _open_dir(self.path)
        try:
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600,
                         dir_fd=directory)
            with os.fdopen(fd, "wb") as stream:
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
            os.fsync(directory)
        except FileExistsError:
            raise EvidenceError("evidence_exists") from None
        except OSError:
            raise EvidenceError("evidence_write_failed") from None
        finally:
            os.close(directory)
        digest = hashlib.sha256(data).hexdigest()
        self._digests[name] = digest
        return digest

    def digests(self) -> dict[str, str]:
        return dict(self._digests)


def _read_owned(directory: Path, name: str) -> bytes | None:
    """러너 uid 소유 일반 파일만. 없으면 None."""
    dir_fd = _open_dir(directory)
    try:
        try:
            fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, dir_fd=dir_fd)
        except FileNotFoundError:
            return None
        except OSError:
            raise EvidenceError("meter_file_unavailable") from None
        # fdopen 전에 연 fd로 확인한다(디렉터리·FIFO는 fdopen·read에서 다른 예외로 샌다).
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_size > MAX_METER_FILE_BYTES:
            os.close(fd)
            raise EvidenceError("meter_file_untrusted")
        with os.fdopen(fd, "rb") as stream:
            return stream.read(MAX_METER_FILE_BYTES + 1)
    finally:
        os.close(dir_fd)


def _rows(data: bytes | None, fields: frozenset[str]) -> list[dict]:
    """완결된 줄(\\n으로 끝남)만 센다. 쓰는 중인 마지막 줄은 다음 읽기로 미룬다."""
    if not data:
        return []
    complete = data[:data.rfind(b"\n") + 1]
    rows = []
    for line in complete.splitlines():
        try:
            row = json.loads(line.decode("utf-8"))
        except (UnicodeError, ValueError):
            raise EvidenceError("meter_row_invalid") from None
        if not isinstance(row, dict) or set(row) != fields or type(row.get("seq")) is not int:
            raise EvidenceError("meter_row_invalid")
        rows.append(row)
    if len({row["seq"] for row in rows}) != len(rows):
        raise EvidenceError("meter_seq_invalid")
    return rows


def sequence_complete(rows: list[dict]) -> bool:
    """계측기는 요청이 끝난 순서로 쓴다. 진행 중 요청이 없을 때(drain 뒤)는 순번이 1..n으로 빠짐없어야 한다."""
    return sorted(row["seq"] for row in rows) == list(range(1, len(rows) + 1))


class MeterFiles:
    """계측기 bind 디렉터리 읽기. 러너는 쓰지 않는다."""

    def __init__(self, path: Path):
        self.path = path

    def engine_rows(self) -> list[dict]:
        return _rows(_read_owned(self.path, ENGINE_LOG), _ENGINE_ROW)

    def forwarded(self) -> int:
        """대상에 전달한 요청 수(seal 후 거부 건 제외)."""
        return sum(1 for row in self.engine_rows() if row["kind"] == "request")

    def observer_rows(self) -> list[dict]:
        return _rows(_read_owned(self.path, OBSERVER_LOG), _OBSERVER_ROW)

    def marker(self, name: str) -> dict | None:
        if name not in (SEALED_MARKER, DRAINED_MARKER, TERMINATED_MARKER):
            raise EvidenceError("meter_marker_unknown")
        data = _read_owned(self.path, name)
        if data is None:
            return None
        try:
            marker = json.loads(data.decode("utf-8"))
        except (UnicodeError, ValueError):
            raise EvidenceError("meter_marker_invalid") from None
        if not isinstance(marker, dict) or marker.get("role") != "engine":
            raise EvidenceError("meter_marker_invalid")
        return marker
