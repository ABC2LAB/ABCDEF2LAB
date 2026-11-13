"""러너 쪽 E② 제어 채널 클라이언트(docs/contracts/e2-runner-control-proposal.md). 제품 E②는 아직 없고 가짜 서버로만 시험했다.

연결 하나에 요청 한 줄과 응답 한 줄이다. 응답은 닫힌 필드로 검사하고 회차 값을 대조한다. 실패에는 고정 코드만 담는다.

- 작업 생성 전 실패는 호출자가 시작을 거부한다(기술적 무효).
- 종료 단계 실패는 결과로 돌려주고 제품 실패로 기록한다. 러너는 E②를 무한히 기다리지 않고 계측기 seal로 넘어간다(연결 제안 §4.3).
"""

from __future__ import annotations

import json
import math
import os
import re
import socket
import stat
import struct
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

from e2_collect import CloseAck
from e2_export import EVAL_HEADER_MODES, E2_LLM_BACKENDS, E2_REASONS, TARGET_PUBLIC_PRACTICE

FORMAT = "abc2lab.e2-runner-control/v1"
SOCKET_NAME = "e2-control.sock"
MAX_MESSAGE_BYTES = 8 * 1024
_PRIVATE_DIR_MODE = 0o700
_RECV_BYTES = 4096
_PEERCRED_FORMAT = "3i"
READY_STARTING, READY_SERVICE, READY_FAILED = "starting", "service_ready", "failed"
CLOSE_CLOSING, CLOSE_COMPLETE, CLOSE_INCOMPLETE, CLOSE_NO_EXECUTION = "closing", "complete", "incomplete", "no_execution"
# 채널 실패로 끝난 close의 상태. 제품 응답 어휘와 겹치지 않는다.
CLOSE_UNAVAILABLE = "unavailable"
_READY_STATES = frozenset({READY_STARTING, READY_SERVICE, READY_FAILED})
_CLOSE_STATES = frozenset({CLOSE_CLOSING, CLOSE_COMPLETE, CLOSE_INCOMPLETE, CLOSE_NO_EXECUTION})
_HEAD_STATES = frozenset({"no_execution", "running", "closing", "complete", "incomplete"})
_COMMON = frozenset({"format", "type", "eval_run_id", "reservation_ref"})
_RESPONSE_FIELDS = {
    "ready": frozenset({"state", "reason", "config_sha256", "header_mode", "evaluation", "policy_fingerprint_sha256"}),
    "bind": frozenset({"reset_generation"}),
    "head": frozenset({"state", "execution_ref", "run_ref", "engine_task_id", "policy_fingerprint_sha256", "checkpoint"}),
    "close": frozenset({"close_request_ref", "execution_ref", "admission_closed"}),
    "close_status": frozenset({"state", "reason", "close_request_ref", "execution_ref", "manifest_sha256", "checkpoint"}),
}
_EVALUATION_FIELDS = frozenset({"llm_backend", "target_kind"})
# 제어 채널 제안 §2의 고정 어휘. 여기 없는 값은 원문일 수 있어 결과로 옮기지 않는다.
READY_REASONS = frozenset({"config_rejected", "policy_rejected", "audit_unavailable", "startup_failed"})
CLOSE_REASONS = E2_REASONS | {"drain_timeout", "audit_incomplete", "publish_failed"}
ERROR_CODES = frozenset({"other_run", "bad_request", "not_ready", "bind_conflict", "close_ref_conflict", "closed", "unavailable"})
_CHECKPOINT_FIELDS = frozenset({"sequence", "mac"})
_SHA256 = re.compile(r"[0-9a-f]{64}\Z")
_REF = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}\Z")
_TASK_ID = re.compile(r"[1-9][0-9]{0,18}\Z")


# 호출자의 남은 시간(초). 단계 전체 시한을 소켓 읽기까지 전달한다.
Remaining = Callable[[], float]


def _NO_DEADLINE() -> float:
    return math.inf


class E2ControlError(RuntimeError):
    """고정 코드만 담는다. 응답 값·경로·OS 오류 문구는 담지 않는다."""

    def __init__(self, code: str):
        super().__init__("e2_control: " + code)
        self.code = code


class E2ControlUnavailable(E2ControlError):
    """채널에 닿지 못했거나 응답을 끝까지 못 받았다. 조회는 다시 물어도 된다."""


def _fail(code: str) -> None:
    raise E2ControlError(code) from None


def _unavailable(code: str) -> None:
    raise E2ControlUnavailable(code) from None


def _unique_object(pairs: list[tuple[str, object]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            _fail("response_duplicate_key")
        result[key] = value
    return result


def _reject_constant(_value: str) -> None:
    _fail("response_invalid")


def _ref(value: object) -> bool:
    return isinstance(value, str) and bool(_REF.fullmatch(value)) and "://" not in value


def _sha256(value: object) -> bool:
    return isinstance(value, str) and bool(_SHA256.fullmatch(value))


def _member(value: object, allowed: frozenset[str]) -> bool:
    return isinstance(value, str) and value in allowed


def _all_null(response: dict, names: tuple[str, ...]) -> bool:
    return all(response[name] is None for name in names)


def _check_checkpoint(value: object) -> None:
    if value is None:
        return
    if not isinstance(value, dict) or set(value) != _CHECKPOINT_FIELDS or type(value["sequence"]) is not int or value["sequence"] < 0 \
            or not _sha256(value["mac"]):
        _fail("checkpoint_invalid")


def _check_ready(response: dict) -> None:
    state, evaluation = response["state"], response["evaluation"]
    if not _member(state, _READY_STATES):
        _fail("ready_state_invalid")
    if response["reason"] is not None and not _member(response["reason"], READY_REASONS):
        _fail("ready_reason_invalid")
    if state == READY_SERVICE:
        if not _sha256(response["config_sha256"]) or not _sha256(response["policy_fingerprint_sha256"]) \
                or not _member(response["header_mode"], EVAL_HEADER_MODES):
            _fail("ready_fields_missing")
        if evaluation is not None and (not isinstance(evaluation, dict) or set(evaluation) != _EVALUATION_FIELDS
                                       or not _member(evaluation["llm_backend"], E2_LLM_BACKENDS)
                                       or evaluation["target_kind"] != TARGET_PUBLIC_PRACTICE):
            _fail("ready_evaluation_invalid")
    elif not _all_null(response, ("config_sha256", "header_mode", "evaluation", "policy_fingerprint_sha256")):
        _fail("ready_fields_early")


def _check_head(response: dict) -> None:
    if not _member(response["state"], _HEAD_STATES):
        _fail("head_state_invalid")
    _check_checkpoint(response["checkpoint"])
    names = ("execution_ref", "run_ref", "engine_task_id", "policy_fingerprint_sha256")
    if response["state"] == "no_execution":
        if not _all_null(response, names) or response["checkpoint"] is not None:
            _fail("head_fields_early")
        return
    if not _ref(response["execution_ref"]) or not _ref(response["run_ref"]) or not _sha256(response["policy_fingerprint_sha256"]) \
            or not isinstance(response["engine_task_id"], str) or not _TASK_ID.fullmatch(response["engine_task_id"]):
        _fail("head_fields_missing")


def _check_close(response: dict, close_request_ref: str) -> None:
    if response["close_request_ref"] != close_request_ref:
        _fail("close_ref_mismatch")
    if type(response["admission_closed"]) is not bool:
        _fail("close_admission_invalid")
    if response["execution_ref"] is not None and not _ref(response["execution_ref"]):
        _fail("close_execution_invalid")


def _check_close_status(response: dict, close_request_ref: str) -> None:
    state = response["state"]
    if response["close_request_ref"] != close_request_ref:
        _fail("close_ref_mismatch")
    if not _member(state, _CLOSE_STATES):
        _fail("close_state_invalid")
    if response["reason"] is not None and not _member(response["reason"], CLOSE_REASONS):
        _fail("close_reason_invalid")
    _check_checkpoint(response["checkpoint"])
    if response["execution_ref"] is not None and not _ref(response["execution_ref"]):
        _fail("close_execution_invalid")
    if response["manifest_sha256"] is not None and not _sha256(response["manifest_sha256"]):
        _fail("close_manifest_invalid")
    if state == CLOSE_COMPLETE:
        if response["execution_ref"] is None or response["manifest_sha256"] is None or response["checkpoint"] is None:
            _fail("close_fields_missing")
    elif state == CLOSE_INCOMPLETE:
        # 미완료가 성공 head를 말하면 안 된다.
        if response["reason"] is None or response["checkpoint"] is not None:
            _fail("close_incomplete_invalid")
    elif not _all_null(response, ("manifest_sha256", "checkpoint")) or (state == CLOSE_NO_EXECUTION and response["execution_ref"] is not None):
        _fail("close_fields_early")


class ControlChannel:
    """회차 하나의 제어 채널. 디렉터리는 러너가 만들고 `eval-e2`에만 mount한 0700이다."""

    def __init__(self, directory: Path, eval_run_id: str, reservation_ref: str, timeout_seconds: float):
        self.directory, self.eval_run_id, self.reservation_ref = directory, eval_run_id, reservation_ref
        self.timeout_seconds = timeout_seconds

    def _open_directory(self) -> int:
        try:
            fd = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        except OSError:
            _fail("control_dir_unavailable")
        try:
            info = os.fstat(fd)
        except OSError:
            os.close(fd)
            _fail("control_dir_unavailable")
        if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != _PRIVATE_DIR_MODE:
            os.close(fd)
            _fail("control_dir_not_private")
        return fd

    def _exchange(self, line: bytes, remaining: Remaining) -> bytes:
        """한 번의 주고받기. 연결·보내기·받기 전부 이 호출의 상한과 호출자의 남은 시간 안에 끝내고, 시한 뒤 도착은 받지 않는다."""
        started = time.monotonic()

        def left() -> float:
            seconds = min(self.timeout_seconds - (time.monotonic() - started), remaining())
            if seconds <= 0:
                _unavailable("control_deadline")
            return seconds

        directory = self._open_directory()
        try:
            try:
                sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            except OSError:
                _unavailable("control_setup_failed")
            with sock:
                try:
                    sock.settimeout(left())
                    # 경로 길이 상한(sun_path)을 넘지 않도록 열어 둔 디렉터리를 거쳐 연결한다.
                    sock.connect(f"/proc/self/fd/{directory}/{SOCKET_NAME}")
                except TimeoutError:
                    _unavailable("control_deadline")
                except OSError:
                    _unavailable("control_unreachable")
                self._check_peer(sock)
                try:
                    sock.settimeout(left())
                    sock.sendall(line)
                    data = self._read_line(sock, left)
                except TimeoutError:
                    _unavailable("control_deadline")
                except OSError:
                    _unavailable("control_io_failed")
                left()
                return data
        finally:
            os.close(directory)

    @staticmethod
    def _check_peer(sock: socket.socket) -> None:
        try:
            _pid, uid, _gid = struct.unpack(_PEERCRED_FORMAT, sock.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED,
                                                                              struct.calcsize(_PEERCRED_FORMAT)))
        except (OSError, struct.error):
            _unavailable("control_setup_failed")
        if uid != os.geteuid():
            _fail("control_peer_uid_mismatch")

    @staticmethod
    def _read_line(sock: socket.socket, left: Callable[[], float]) -> bytes:
        data = bytearray()
        while b"\n" not in data:
            sock.settimeout(left())
            chunk = sock.recv(_RECV_BYTES)
            if not chunk:
                _unavailable("response_truncated")
            data += chunk
            if len(data) > MAX_MESSAGE_BYTES:
                _fail("response_too_large")
        end = data.index(b"\n")
        if end != len(data) - 1:
            _fail("response_invalid")
        return bytes(data[:end])

    def ask(self, request_type: str, remaining: Remaining = _NO_DEADLINE, **fields: object) -> dict:
        request = {"format": FORMAT, "type": request_type, "eval_run_id": self.eval_run_id, "reservation_ref": self.reservation_ref, **fields}
        line = json.dumps(request, ensure_ascii=False, separators=(",", ":")).encode("utf-8") + b"\n"
        if len(line) > MAX_MESSAGE_BYTES:
            _fail("request_too_large")
        return self._checked(self._exchange(line, remaining), request_type)

    def _checked(self, data: bytes, request_type: str) -> dict:
        try:
            response = json.loads(data.decode("utf-8"), object_pairs_hook=_unique_object, parse_constant=_reject_constant)
        except (UnicodeError, ValueError, RecursionError) as exc:
            if isinstance(exc, E2ControlError):
                raise
            _fail("response_invalid")
        if not isinstance(response, dict) or response.get("format") != FORMAT:
            _fail("response_format")
        if response.get("eval_run_id") != self.eval_run_id or response.get("reservation_ref") != self.reservation_ref:
            _fail("response_other_run")
        if response.get("type") == "error":
            if set(response) != _COMMON | {"code"} or not _member(response["code"], ERROR_CODES):
                _fail("response_error_invalid")
            _fail("refused_" + response["code"])
        if response.get("type") != request_type or set(response) != _COMMON | _RESPONSE_FIELDS[request_type]:
            _fail("response_fields")
        return response

    def ready(self, remaining: Remaining = _NO_DEADLINE) -> dict:
        response = self.ask("ready", remaining)
        _check_ready(response)
        return response

    def bind(self, reset_generation: str, remaining: Remaining = _NO_DEADLINE) -> None:
        """대상 컨테이너 ID를 넘긴다. compose 기동 뒤에야 나오므로 설정 파일에 넣을 수 없다."""
        response = self.ask("bind", remaining, reset_generation=reset_generation)
        if response["reset_generation"] != reset_generation:
            _fail("bind_mismatch")

    def head(self, remaining: Remaining = _NO_DEADLINE) -> dict:
        response = self.ask("head", remaining)
        _check_head(response)
        return response

    def close(self, close_request_ref: str, remaining: Remaining = _NO_DEADLINE) -> dict:
        response = self.ask("close", remaining, close_request_ref=close_request_ref)
        _check_close(response, close_request_ref)
        return response

    def close_status(self, close_request_ref: str, remaining: Remaining = _NO_DEADLINE) -> dict:
        response = self.ask("close_status", remaining, close_request_ref=close_request_ref)
        _check_close_status(response, close_request_ref)
        return response


@dataclass(frozen=True)
class ServiceExpectation:
    """러너가 정한 값. 자기 신고가 아니라 이 값과 대조한다. evaluation은 평가 전달 모드가 아니면 None이다."""
    config_sha256: str
    header_mode: str
    evaluation: dict | None
    policy_fingerprint_sha256: str


@dataclass(frozen=True)
class Waiting:
    """시한 대기에 쓰는 시계와 간격."""
    monotonic: Callable[[], float]
    sleep: Callable[[float], None]
    poll_seconds: float


def _pause_or_stop(waiting: Waiting, deadline: float) -> bool:
    """남은 시간까지만 쉬고, 시한이 지났으면 True. 시한 뒤에는 새 요청을 보내지 않는다."""
    waiting.sleep(min(waiting.poll_seconds, max(0, deadline - waiting.monotonic())))
    return waiting.monotonic() >= deadline


def wait_service_ready(channel: ControlChannel, expected: ServiceExpectation, waiting: Waiting, timeout_seconds: float) -> dict:
    """작업 생성 전 대조. 어떤 실패든 E2ControlError로 올리고 호출자가 시작을 거부한다."""
    deadline = waiting.monotonic() + timeout_seconds
    while True:
        try:
            status = channel.ready(lambda: deadline - waiting.monotonic())
        except E2ControlUnavailable:
            status = None
        if status is not None:
            if status["state"] == READY_FAILED:
                _fail("service_failed")
            if status["state"] == READY_SERVICE:
                break
        if _pause_or_stop(waiting, deadline):
            _fail("service_ready_timeout")
    for name in ("config_sha256", "header_mode", "evaluation", "policy_fingerprint_sha256"):
        if status[name] != getattr(expected, name):
            _fail(name + "_mismatch")
    return status


@dataclass(frozen=True)
class CloseResult:
    """종료 결과. state가 complete일 때만 ack가 있다. 실패도 이 결과로 돌려주고 호출자가 제품 실패로 기록한다."""
    state: str
    reason: str | None
    admission_closed: bool
    execution_ref: str | None
    manifest_sha256: str | None
    ack: CloseAck | None


def _close_result(state: str, reason: str | None, admission_closed: bool, execution_ref: str | None = None,
                  manifest_sha256: str | None = None, ack: CloseAck | None = None) -> CloseResult:
    return CloseResult(state, reason, admission_closed, execution_ref, manifest_sha256, ack)


def _ask_admission_closed(channel: ControlChannel, close_request_ref: str, waiting: Waiting, timeout_seconds: float) -> CloseResult | None:
    deadline = waiting.monotonic() + timeout_seconds
    while True:
        try:
            reply = channel.close(close_request_ref, lambda: deadline - waiting.monotonic())
        except E2ControlUnavailable as exc:
            if _pause_or_stop(waiting, deadline):
                return _close_result(CLOSE_UNAVAILABLE, exc.code, False)
            continue
        except E2ControlError as exc:
            return _close_result(CLOSE_UNAVAILABLE, exc.code, False)
        if reply["admission_closed"]:
            return None
        return _close_result(CLOSE_UNAVAILABLE, "close_admission_not_closed", False, reply["execution_ref"])


def close_execution(channel: ControlChannel, close_request_ref: str, waiting: Waiting, ack_timeout_seconds: float,
                    close_timeout_seconds: float) -> CloseResult:
    """예산이 끝난 뒤 E②를 닫는다. 어떤 실패든 결과로 돌려주며 시한을 넘겨 기다리지 않는다."""
    refused = _ask_admission_closed(channel, close_request_ref, waiting, ack_timeout_seconds)
    if refused is not None:
        return refused
    deadline = waiting.monotonic() + close_timeout_seconds
    while True:
        try:
            status = channel.close_status(close_request_ref, lambda: deadline - waiting.monotonic())
        except E2ControlUnavailable as exc:
            if _pause_or_stop(waiting, deadline):
                return _close_result(CLOSE_UNAVAILABLE, exc.code, True)
            continue
        except E2ControlError as exc:
            return _close_result(CLOSE_UNAVAILABLE, exc.code, True)
        if status["state"] != CLOSE_CLOSING:
            break
        if _pause_or_stop(waiting, deadline):
            return _close_result(CLOSE_UNAVAILABLE, "close_timeout", True, status["execution_ref"])
    ack = None
    if status["state"] == CLOSE_COMPLETE:
        checkpoint = status["checkpoint"]
        ack = CloseAck(close_request_ref, checkpoint["sequence"], checkpoint["mac"], status["manifest_sha256"])
    return _close_result(status["state"], status["reason"], True, status["execution_ref"], status["manifest_sha256"], ack)
