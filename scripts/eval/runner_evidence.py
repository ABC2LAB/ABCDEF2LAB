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
LLM_VERIFIED_MARKER = "llm-verified.json"
MAX_METER_FILE_BYTES = 64 * 1024 * 1024
_NAME = re.compile(r"[a-z0-9][a-z0-9-]{0,62}\.json\Z")
_ENGINE_ROW = frozenset({"seq", "kind", "method", "status", "started_at", "completed_at", "response_bytes", "outcome",
                         "sent", "response", "attempt_id_state", "attempt_id"})
# 계측기 행의 요청 단계(cmd/eval-meter proxy.go와 같은 값). sent=대상까지 갔는지, response=대상이 답했는지이며
# 같은 시도의 진행 이력이라 배타적 건수가 아니다(평가 프로토콜 §8.3).
ENGINE_SENT = frozenset({"no", "uncertain", "yes"})
ENGINE_RESPONSE = frozenset({"none", "headers", "complete", "interrupted"})
# E② 시도 ID 헤더 검사 결과. 유효할 때만 값(소문자 hex 32자)이 있고, 나머지는 빈 문자열이다(잘못된 원문은 안 남김).
ATTEMPT_STATES = frozenset({"absent", "valid", "invalid", "multiple"})
_ATTEMPT_ID = re.compile(r"[0-9a-f]{32}\Z")
_OBSERVER_ROW = frozenset({"seq", "decision", "method", "status", "started_at", "completed_at", "response_bytes"})
# 통과 전용 가짜 E²(cmd/eval-meter e2stub.go)의 결정 행. 보내기 전에 쓰고, 경로·헤더·본문은 없다.
E2_STUB_LOG = "e2-stub-decisions.jsonl"
_E2_STUB_ROW = frozenset({"seq", "fixture", "decision", "attempt_id", "caller_attempt", "method", "decided_at"})
# allowed만 대상 쪽으로 보낸다. unsupported(Upgrade·CONNECT)와 unavailable(ID 발급 실패)은 보내지 않는다.
E2_STUB_DECISIONS = ("allowed", "unsupported", "unavailable")
# 결정 행의 메서드(HTTP token, RFC 9110)와 시각(cmd/eval-meter stamp: RFC 3339 UTC, 소수 초 9자리까지).
_HTTP_METHOD = re.compile(r"[!#$%&'*+.^_`|~0-9A-Za-z-]{1,32}\Z")
_STAMP = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?Z\Z")


# 계측기(cmd/eval-meter engineMeter)가 대상에 전달한 행 종류. upgrade(WebSocket 등)는 완료 행이 "stream"이고
# 계측기 전달 건수(forwarded)에 들어간다. "rejected"(seal 뒤 거부)는 전달이 아니다. Go 쪽과 같은지 테스트로 고정.
FORWARDED_KINDS = frozenset({"request", "stream"})
ENGINE_KINDS = FORWARDED_KINDS | {"rejected"}
# 봉인 뒤 유예(-drain-grace)가 지나 계측기가 대상 쪽으로 취소한 요청 행의 outcome. 건수는 drained 마커와 같아야 한다.
FORCED_CANCELLED_OUTCOME = "forced_cancelled"
COUNTED_METHODS = ("GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS")
OTHER_METHOD = "OTHER"


def e2_stub_counts(rows: list[dict]) -> dict:
    """검사한 가짜 E² 결정 행의 결정별 건수(격리 근거 e2_placement 행)."""
    return {"decision_rows": len(rows),
            **{decision: sum(1 for row in rows if row["decision"] == decision) for decision in E2_STUB_DECISIONS}}


def forwarded_count(rows: list[dict]) -> int:
    """계측기와 같은 기준의 전달 건수. 모르는 종류가 있으면 기록을 믿을 수 없으므로 거부한다."""
    if any(row.get("kind") not in ENGINE_KINDS for row in rows):
        raise EvidenceError("meter_log_invalid")
    return sum(1 for row in rows if row["kind"] in FORWARDED_KINDS)


def forwarded_by_method(rows: list[dict]) -> dict[str, int]:
    """전달 요청의 메서드별 건수(프로토콜 §8.1). 합은 forwarded_count와 같다.

    표로 나가는 값이라 메서드 토큰을 그대로 옮기지 않고 닫힌 목록으로 센다. 목록 밖(소문자 포함)은 OTHER_METHOD다.
    """
    counts = dict.fromkeys((*COUNTED_METHODS, OTHER_METHOD), 0)
    for row in rows:
        if row.get("kind") not in ENGINE_KINDS:
            raise EvidenceError("meter_log_invalid")
        if row["kind"] in FORWARDED_KINDS:
            method = row.get("method")
            counts[method if method in COUNTED_METHODS else OTHER_METHOD] += 1
    return counts


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
        self._sizes: dict[str, int] = {}

    def write_json(self, name: str, payload: object) -> str:
        if not _NAME.fullmatch(name):
            raise EvidenceError("evidence_name_invalid")
        try:
            data = json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=False,
                              allow_nan=False).encode("utf-8") + b"\n"
        except (TypeError, ValueError):
            raise EvidenceError("evidence_payload_invalid") from None
        return self.write_bytes(name, data)

    def write_bytes(self, name: str, data: bytes) -> str:
        """받은 바이트를 그대로 남긴다. 다시 직렬화하면 digest가 받은 것과 달라진다."""
        if not _NAME.fullmatch(name):
            raise EvidenceError("evidence_name_invalid")
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
        self._sizes[name] = len(data)
        return digest

    def digests(self) -> dict[str, str]:
        return dict(self._digests)

    def describe(self, name: str) -> tuple[str, int]:
        """쓴 파일의 (SHA-256, 바이트 수). 러너 기록 descriptor용."""
        if name not in self._digests:
            raise EvidenceError("evidence_unknown")
        return self._digests[name], self._sizes[name]


def read_private_json(directory: Path, name: str) -> object:
    """러너가 쓴 회차 파일(러너 uid 소유 일반 파일)을 JSON으로 읽는다. 없거나 해석 불가면 오류."""
    data = _read_owned(directory, name)
    if data is None:
        raise EvidenceError("evidence_missing")
    try:
        return json.loads(data.decode("utf-8"))
    except (UnicodeError, ValueError):
        raise EvidenceError("evidence_invalid") from None


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


def _engine_stages_valid(row: dict) -> bool:
    """단계 값이 알려진 집합이고 서로 모순되지 않는지. 거부 행은 대상에 안 갔고, 안 간 요청에 대상 응답은 없다."""
    state, attempt = row["attempt_id_state"], row["attempt_id"]
    if row["sent"] not in ENGINE_SENT or row["response"] not in ENGINE_RESPONSE or state not in ATTEMPT_STATES:
        return False
    if not isinstance(attempt, str) or (state == "valid") != bool(_ATTEMPT_ID.match(attempt)) or (
            state != "valid" and attempt != ""):
        return False
    if row["kind"] == "rejected" and (row["sent"], row["response"]) != ("no", "none"):
        return False
    return row["sent"] != "no" or row["response"] == "none"


def sequence_complete(rows: list[dict]) -> bool:
    """계측기는 요청이 끝난 순서로 쓴다. 진행 중 요청이 없을 때(drain 뒤)는 순번이 1..n으로 빠짐없어야 한다."""
    return sorted(row["seq"] for row in rows) == list(range(1, len(rows) + 1))


class MeterFiles:
    """계측기 bind 디렉터리 읽기. 러너는 쓰지 않는다. e2_path는 가짜 E²의 따로 둔 bind 디렉터리다."""

    def __init__(self, path: Path, e2_path: Path | None = None):
        self.path = path
        self.e2_path = e2_path

    def engine_rows(self) -> list[dict]:
        rows = _rows(_read_owned(self.path, ENGINE_LOG), _ENGINE_ROW)
        if not all(_engine_stages_valid(row) for row in rows):
            raise EvidenceError("meter_row_invalid")
        return rows

    def forwarded(self) -> int:
        """대상에 전달한 요청 수(stream 포함, seal 후 거부 건 제외)."""
        return forwarded_count(self.engine_rows())

    def observer_rows(self) -> list[dict]:
        return _rows(_read_owned(self.path, OBSERVER_LOG), _OBSERVER_ROW)

    def e2_stub_rows(self) -> list[dict]:
        """가짜 E² 결정 행. fixture 표식이어야 하고, ID는 발급된 결정에만 있으며 겹치지 않는다.

        파일이 없으면(가짜 E²가 뜨지 않음) 회차 근거가 없는 것이다.
        """
        data = None if self.e2_path is None else _read_owned(self.e2_path, E2_STUB_LOG)
        if data is None:
            raise EvidenceError("e2_stub_log_missing")
        # drain 뒤의 최종 읽기다. 진행 중 로그와 달리 개행으로 끝나지 않은 꼬리는 잘린 기록이므로 거부한다
        # (_rows는 쓰는 중인 마지막 줄을 버리므로 그대로 쓰면 잘린 행을 빼고 센다, #209 리뷰).
        if data and not data.endswith(b"\n"):
            raise EvidenceError("e2_stub_log_invalid")
        rows = _rows(data, _E2_STUB_ROW)
        if not sequence_complete(rows):
            raise EvidenceError("e2_stub_log_invalid")
        ids = []
        for row in rows:
            issued = row["decision"] != "unavailable"
            attempt = row["attempt_id"]
            if (row["fixture"] is not True or row["decision"] not in E2_STUB_DECISIONS
                    or type(row["caller_attempt"]) is not bool or not isinstance(attempt, str)
                    or not isinstance(row["method"], str) or not _HTTP_METHOD.match(row["method"])
                    or not isinstance(row["decided_at"], str) or not _STAMP.match(row["decided_at"])
                    or bool(_ATTEMPT_ID.fullmatch(attempt)) != issued or (not issued and attempt != "")):
                raise EvidenceError("e2_stub_log_invalid")
            if issued:
                ids.append(attempt)
        if len(ids) != len(set(ids)):
            raise EvidenceError("e2_stub_log_invalid")
        return rows

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


class GatewayFiles:
    """LLM 게이트웨이 bind 디렉터리 읽기(계측기와 따로 둔 llm/). 러너는 쓰지 않는다."""

    def __init__(self, path: Path):
        self.path = path

    def verified(self) -> dict | None:
        """seal·drain 뒤 게이트웨이가 쓰는 검증 마커. 아직 없으면 None."""
        data = _read_owned(self.path, LLM_VERIFIED_MARKER)
        if data is None:
            return None
        try:
            marker = json.loads(data.decode("utf-8"))
        except (UnicodeError, ValueError, RecursionError):
            raise EvidenceError("llm_marker_invalid") from None
        if not isinstance(marker, dict) or marker.get("role") != "llm-gateway":
            raise EvidenceError("llm_marker_invalid")
        return marker
