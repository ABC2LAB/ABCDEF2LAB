"""러너가 제품 E② 결과를 모아 감사 정본과 대조한다(E②–러너 연결 제안 §4.1·§4.3). 러너 연결과 제품 생산자는 아직 없다.

실패는 두 갈래다(제안 §4.3, 평가 프로토콜 §2).
- 제품 실패: E②가 결과를 못 냈거나 맞지 않게 냈다. 회차는 유효하고 서명하며, 통제 주장은 보류하고 판정은 판단불가로 둔다.
- 기술적 무효(`E2CollectionInvalid`): 회차 값·정책 지문 불일치, 배치 위반, 러너 쪽 수집 경로 장애다. 회차를 쓰지 않는다.

파일은 fd로 한 번만 읽어 러너 전용 사본으로 옮기고, 검증과 대조는 그 사본에만 한다. 결과 파일이 없다고 차단 0건이나 무취약으로 채우지 않는다.
여기 `complete`는 이 수집의 마감이고 실행 성공·무취약을 뜻하지 않는다. 키 내용은 읽지 않고 경로만 검증 명령에 넘긴다.
"""

from __future__ import annotations

import errno
import hashlib
import json
import os
import re
import stat
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Mapping

from e2_attempt_paths import ATTEMPT_PATHS_DONE_FILE, ATTEMPT_PATHS_FILE, MAX_ATTEMPT_PATHS_BYTES, MAX_DONE_BYTES
from e2_export import (ATTEMPTS_FILE, JOURNAL_VERSION, STATE_COMPLETE, E2Expectation, E2Export, E2ExportError, parse_attempts, parse_manifest,
                       strict_object)

OUTCOME_COMPLETE, OUTCOME_PRODUCT_FAILURE = "complete", "product_failure"
MANIFEST_FILE = "manifest.json"
_COPY_JOURNAL, _COPY_CHECKPOINT = "journal.jsonl", "checkpoint.json"
MAX_MANIFEST_BYTES = 64 * 1024
# e2-audit-verify의 checkpoint 상한과 e2proxy journalMaxBytes다. 시험이 Go 소스와 대조한다.
MAX_CHECKPOINT_BYTES = 4 * 1024
MAX_JOURNAL_BYTES = 256 << 20
_PRIVATE_DIR_MODE, _PRIVATE_FILE_MODE = 0o700, 0o600
_CHECKPOINT_FIELDS = frozenset({"Version", "JournalID", "Sequence", "MAC", "Scope"})
_SCOPE_KEYS = ("RunRef", "TaskRef", "CaseRef", "ResetRef", "PolicyRef")
_SUMMARY_FIELDS = frozenset({"Checkpoint", "Events"})
_JOURNAL_ID = re.compile(r"[0-9a-f]{32}\Z")
_MAC = re.compile(r"[0-9a-f]{64}\Z")
_INTEGRITY_FAILED = b"audit_integrity_failed"
JOURNAL_FORMAT_ARG = "e2-http-v1"
# 러너가 정한 값과 어긋난 것이다. E②가 잘못 낸 것이 아니라 회차를 묶은 조건이 틀렸으므로 무효로 둔다.
CONFIG_VIOLATIONS = frozenset({"EVAL_RUN_ID_MISMATCH", "MODEL_TAG_MISMATCH", "RESET_GENERATION_MISMATCH", "RESERVATION_REF_MISMATCH",
                               "ENGINE_TASK_ID_MISMATCH", "HEADER_MODE_MISMATCH", "POLICY_FINGERPRINT_MISMATCH", "TASK_REF_MISMATCH",
                               "RUN_REF_IS_EVAL_RUN", "EVALUATION_UNEXPECTED", "EVALUATION_INVALID", "EVALUATION_RUN_REF_MISMATCH",
                               "EVALUATION_BACKEND_MISMATCH", "EVALUATION_TARGET_MISMATCH", "EXPECTATION_INVALID"})
RunVerifier = Callable[[list[str]], "tuple[int, bytes, bytes]"]


class E2CollectionInvalid(RuntimeError):
    """회차를 기술적 무효로 만드는 실패. 고정 코드만 담는다."""

    def __init__(self, code: str):
        super().__init__("e2_collect: " + code)
        self.code = code


class _ProductFailure(Exception):
    def __init__(self, code: str):
        super().__init__(code)
        self.code = code


@dataclass(frozen=True)
class CloseAck:
    """인증된 제어 채널의 최종 close ACK. 러너가 보낸 close 참조와 E②가 고정한 head·manifest."""
    close_request_ref: str
    sequence: int
    mac: str
    manifest_sha256: str


@dataclass(frozen=True)
class E2Collection:
    outcome: str
    reason: str
    export: E2Export | None
    sha256: Mapping[str, str]
    checkpoint: Mapping[str, object] | None

    def evidence(self) -> dict:
        """서명 근거에 넣을 비민감 기록. 제품 실패와 수집 결과를 함께 남긴다."""
        return {"format": "abc2lab-eval-e2-collection-v1", "outcome": self.outcome, "reason": self.reason,
                "export": self.export.summary() if self.export is not None else None, "sha256": dict(self.sha256),
                "checkpoint": dict(self.checkpoint) if self.checkpoint is not None else None}


def subprocess_verifier(path: str, timeout_seconds: float) -> RunVerifier:
    """환경 변수를 넘기지 않고 `e2-audit-verify`를 돌린다. 실행 자체가 안 되면 러너 쪽 장애다."""

    def run(args: list[str]) -> tuple[int, bytes, bytes]:
        try:
            done = subprocess.run([path, *args], env={}, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  timeout=timeout_seconds, check=False)
        except (OSError, subprocess.SubprocessError, ValueError):
            raise E2CollectionInvalid("audit_verifier_unavailable") from None
        return done.returncode, done.stdout, done.stderr

    return run


def _open_copy_dir(path: Path) -> int:
    try:
        fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    except OSError:
        raise E2CollectionInvalid("copy_dir_unavailable") from None
    info = os.fstat(fd)
    if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != _PRIVATE_DIR_MODE:
        os.close(fd)
        raise E2CollectionInvalid("copy_dir_not_private")
    return fd


def _open_export_dir(path: Path) -> int:
    try:
        fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    except FileNotFoundError:
        raise _ProductFailure("export_missing") from None
    except OSError as exc:
        if exc.errno in (errno.ELOOP, errno.ENOTDIR):
            raise _ProductFailure("export_untrusted") from None
        raise E2CollectionInvalid("export_unavailable") from None
    info = os.fstat(fd)
    if info.st_uid != os.geteuid():
        os.close(fd)
        raise E2CollectionInvalid("export_owner_mismatch")
    if stat.S_IMODE(info.st_mode) != _PRIVATE_DIR_MODE:
        os.close(fd)
        raise _ProductFailure("export_not_private")
    return fd


def _file_problem(info: os.stat_result) -> Exception | None:
    if not stat.S_ISREG(info.st_mode):
        return _ProductFailure("export_untrusted")
    if info.st_uid != os.geteuid():
        return E2CollectionInvalid("export_owner_mismatch")
    if stat.S_IMODE(info.st_mode) != _PRIVATE_FILE_MODE:
        return _ProductFailure("export_not_private")
    return None


def _read_file(directory: int, name: str, maximum: int, missing: str) -> bytes:
    try:
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, dir_fd=directory)
    except FileNotFoundError:
        raise _ProductFailure(missing) from None
    except OSError as exc:
        if exc.errno == errno.ELOOP:
            raise _ProductFailure("export_untrusted") from None
        raise E2CollectionInvalid("export_unavailable") from None
    # fdopen 전에 연 fd로 확인한다(디렉터리는 fdopen에서 다른 예외로 샌다).
    problem = _file_problem(os.fstat(fd))
    if problem is not None:
        os.close(fd)
        raise problem
    with os.fdopen(fd, "rb") as stream:
        try:
            data = stream.read(maximum + 1)
        except OSError:
            raise E2CollectionInvalid("export_unavailable") from None
    if len(data) > maximum:
        raise _ProductFailure("export_too_large")
    return data


class _Copies:
    """읽은 바이트를 러너 전용 사본으로 옮기고 digest를 기억한다."""

    def __init__(self, path: Path):
        self.path = path
        self.fd = _open_copy_dir(path)
        self.sha256: dict[str, str] = {}

    def close(self) -> None:
        os.close(self.fd)

    def keep(self, name: str, data: bytes) -> str:
        try:
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, _PRIVATE_FILE_MODE, dir_fd=self.fd)
            with os.fdopen(fd, "wb") as stream:
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
            os.fsync(self.fd)
        except OSError:
            raise E2CollectionInvalid("copy_write_failed") from None
        digest = hashlib.sha256(data).hexdigest()
        self.sha256[name] = digest
        return digest


def _parsed_manifest(data: bytes, expected: E2Expectation) -> dict:
    try:
        return parse_manifest(data, expected)
    except E2ExportError as exc:
        if exc.code in CONFIG_VIOLATIONS:
            raise E2CollectionInvalid(exc.code.lower()) from None
        raise _ProductFailure(exc.code.lower()) from None


def _parsed_checkpoint(data: bytes, manifest: dict) -> dict:
    try:
        checkpoint = strict_object(data)
    except E2ExportError:
        raise _ProductFailure("checkpoint_invalid") from None
    if set(checkpoint) != _CHECKPOINT_FIELDS or checkpoint["Version"] != JOURNAL_VERSION or type(checkpoint["Sequence"]) is not int or \
            not isinstance(checkpoint["JournalID"], str) or not _JOURNAL_ID.fullmatch(checkpoint["JournalID"]) or \
            not isinstance(checkpoint["MAC"], str) or not _MAC.fullmatch(checkpoint["MAC"]):
        raise _ProductFailure("checkpoint_invalid")
    if checkpoint["Sequence"] != manifest["rows"]:
        raise _ProductFailure("checkpoint_sequence_mismatch")
    # 행이 없으면 저널이 head에 scope를 아직 안 정한 상태다.
    scope = manifest["scope"] if manifest["rows"] else {key: "" for key in _SCOPE_KEYS}
    if checkpoint["Scope"] != scope:
        raise _ProductFailure("checkpoint_scope_mismatch")
    return checkpoint


def _check_close_ack(ack: CloseAck | None, manifest: dict, manifest_sha256: str, checkpoint: dict) -> None:
    """파일끼리 맞아도 과거 head일 수 있다. 최신성은 제어 채널의 ACK로만 확인한다."""
    if ack is None:
        raise _ProductFailure("close_ack_missing")
    if ack.close_request_ref != manifest["close_request_ref"]:
        raise _ProductFailure("close_request_mismatch")
    if (ack.sequence, ack.mac) != (checkpoint["Sequence"], checkpoint["MAC"]):
        raise _ProductFailure("close_ack_mismatch")
    if ack.manifest_sha256 != manifest_sha256:
        raise _ProductFailure("close_manifest_mismatch")


def _verify_audit(run_verifier: RunVerifier, copies: _Copies, key_path: str, checkpoint: dict, rows: int) -> None:
    code, stdout, stderr = run_verifier(["--journal-format", JOURNAL_FORMAT_ARG, "--journal", str(copies.path / _COPY_JOURNAL),
                                         "--checkpoint", str(copies.path / _COPY_CHECKPOINT), "--key-file", key_path])
    if code != 0:
        if stderr.strip() == _INTEGRITY_FAILED:
            raise _ProductFailure("audit_integrity_failed")
        raise E2CollectionInvalid("audit_verifier_failed")
    try:
        summary = strict_object(stdout)
    except E2ExportError:
        raise E2CollectionInvalid("audit_verifier_output_invalid") from None
    if set(summary) != _SUMMARY_FIELDS or summary["Checkpoint"] != checkpoint or summary["Events"] != rows:
        raise E2CollectionInvalid("audit_verifier_output_invalid")


def _check_projection(journal: bytes, attempts: bytes) -> None:
    """투영 행은 검증된 저널 Event와 빠짐없이 같아야 한다. 첫 줄이 헤더인지는 검증 명령이 이미 확인했다."""
    try:
        lines = [strict_object(line) for line in journal.splitlines()]
        rows = [strict_object(line) for line in attempts.splitlines()]
    except E2ExportError:
        raise _ProductFailure("projection_mismatch") from None
    events = []
    for line in lines[1:]:
        record = line.get("Record")
        if line.get("Kind") != "event" or not isinstance(record, dict):
            raise _ProductFailure("projection_mismatch")
        events.append(record.get("Event"))
    if events != rows:
        raise _ProductFailure("projection_mismatch")


@dataclass
class _Progress:
    export: E2Export | None = None
    checkpoint: dict | None = None


def _present(directory: int, name: str) -> bool:
    try:
        os.stat(name, dir_fd=directory, follow_symlinks=False)
    except FileNotFoundError:
        return False
    except OSError:
        raise E2CollectionInvalid("export_unavailable") from None
    return True


def _collect_attempt_paths(source: int, copies: _Copies, expected: bool) -> None:
    """수용시험 회차(설정 v2)의 시도 경로 기록과 완료 표식을 사본으로 옮긴다. 검증은 e2_attempt_paths가 한다.

    공식 회차(v1)에 있으면 경로 기록을 막는 조건을 어긴 것이므로 회차를 무효로 둔다. 수용시험 회차에 없으면
    (마감 실패 등) 회차는 유효하고 그 지표만 NOT_RUN이 되도록 옮기지 않는다.
    """
    for name, maximum in ((ATTEMPT_PATHS_FILE, MAX_ATTEMPT_PATHS_BYTES), (ATTEMPT_PATHS_DONE_FILE, MAX_DONE_BYTES)):
        if not _present(source, name):
            continue
        if not expected:
            raise E2CollectionInvalid("attempt_paths_unexpected")
        try:
            data = _read_file(source, name, maximum, "attempt_paths_missing")
        except _ProductFailure:
            # 경로 투영은 측정용이다. 읽을 수 없으면 옮기지 않고(NOT_RUN) E② 결과 판정은 그대로 둔다.
            continue
        copies.keep(name, data)


def _collect(source: int, copies: _Copies, expected: E2Expectation, close_ack: CloseAck | None, key_path: str, run_verifier: RunVerifier,
             progress: _Progress, attempt_paths: bool = False) -> None:
    manifest_data = _read_file(source, MANIFEST_FILE, MAX_MANIFEST_BYTES, "manifest_missing")
    manifest_sha256 = copies.keep(MANIFEST_FILE, manifest_data)
    manifest = _parsed_manifest(manifest_data, expected)
    attempts = _read_file(source, ATTEMPTS_FILE, MAX_JOURNAL_BYTES, "attempts_missing")
    copies.keep(ATTEMPTS_FILE, attempts)
    try:
        progress.export = parse_attempts(attempts, manifest)
    except E2ExportError as exc:
        raise _ProductFailure(exc.code.lower()) from None
    if manifest["state"] != STATE_COMPLETE:
        raise _ProductFailure("export_incomplete")
    journal = _read_file(source, manifest["audit_journal_file"], MAX_JOURNAL_BYTES, "journal_missing")
    copies.keep(_COPY_JOURNAL, journal)
    checkpoint_data = _read_file(source, manifest["audit_checkpoint_file"], MAX_CHECKPOINT_BYTES, "checkpoint_missing")
    if copies.keep(_COPY_CHECKPOINT, checkpoint_data) != manifest["audit_checkpoint_sha256"]:
        raise _ProductFailure("checkpoint_digest_mismatch")
    checkpoint = _parsed_checkpoint(checkpoint_data, manifest)
    _check_close_ack(close_ack, manifest, manifest_sha256, checkpoint)
    _verify_audit(run_verifier, copies, key_path, checkpoint, manifest["rows"])
    _check_projection(journal, attempts)
    progress.checkpoint = {"sequence": checkpoint["Sequence"], "mac": checkpoint["MAC"]}
    _collect_attempt_paths(source, copies, attempt_paths)


def collect_e2_export(source_dir: Path, copy_dir: Path, expected: E2Expectation, close_ack: CloseAck | None, key_path: str,
                      run_verifier: RunVerifier, attempt_paths: bool = False) -> E2Collection:
    """E② 실행 디렉터리를 읽어 판정한다. 제품 실패는 결과로 돌려주고, 기술적 무효는 E2CollectionInvalid로 올린다.

    attempt_paths는 수용시험 회차(경로 키를 넘긴 회차)인지다. 아니면 시도 경로 기록이 있을 때 무효다.
    """
    copies = _Copies(copy_dir)
    progress = _Progress()
    reason, outcome = "", OUTCOME_COMPLETE
    try:
        source = _open_export_dir(source_dir)
        try:
            _collect(source, copies, expected, close_ack, key_path, run_verifier, progress, attempt_paths)
        finally:
            os.close(source)
    except _ProductFailure as failure:
        reason, outcome = failure.code, OUTCOME_PRODUCT_FAILURE
    finally:
        copies.close()
    return E2Collection(outcome, reason, progress.export, dict(copies.sha256), progress.checkpoint)
