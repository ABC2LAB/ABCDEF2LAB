"""분리 배치 O 회차의 E② v3 저널 수집(평가 쪽). 회차별 감사 키를 만들어 보호 입력으로 넣고, 회차가 끝나면 제품 볼륨에서
저널과 봉인된 최종 head를 꺼내 `e2-audit-verify --journal-format e2-http-v3`로 검증한다.

동찬님 답(2026-11-19):
- 감사 키: 러너가 회차마다 만들어 보호 입력으로 제품과 검증기에 같은 키를 넣는다. 저장소·로그·결과물에 남기지 않고, 경로 가림
  키·digest 키와 분리한다. 제품 주입 경로는 동찬님 배선에 맞춰 정하므로 compose 변수 이름은 설정값이다.
- 검증: 봉인된 최종 head 기준. 현재성 확인에 실패하면 회차 무효가 아니라 "통제 대조 미수집"이다(고정 초안 §20).
- 저널·신뢰 저장소 head 내보내기 경로·파일명은 제품 Host 연결 때 확정되므로 설정값이다.

분류(e2_collect와 같은 두 갈래):
- 미수집(`uncollected`): 제품이 저널·head를 안 냈거나, 맞지 않거나, 검증기가 무결성·현재성 실패를 냈다. 회차는 유효하고 통제
  대조만 미수집으로 둔다.
- 러너 쪽 장애(`E2V3Invalid`): 키 파일·사본 디렉터리·검증기 실행이 러너 쪽에서 실패했다. 평가 환경 원인이라 회차를 쓰지 않는다.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

JOURNAL_FORMAT_V3 = "e2-http-v3"
EVIDENCE_NAME = "e2-v3-collection.json"
EVIDENCE_FORMAT = "abc2lab-eval-e2-v3-collection-v1"
OUTCOME_COLLECTED, OUTCOME_UNCOLLECTED = "collected", "uncollected"
# 감사 키(e2proxy journalMinKeyBytes 이상). 보호 입력 디렉터리 안 러너 쪽 파일 이름이고, 제품은 compose 변수로 경로를 받는다.
AUDIT_KEY_BYTES = 32
AUDIT_KEY_FILE = "e2-audit.key"
# e2-audit-verify의 checkpoint 상한과 e2proxy journalMaxBytes(e2_collect와 같은 값).
MAX_HEAD_BYTES = 4 * 1024
MAX_JOURNAL_BYTES = 256 << 20
_HEAD_FIELDS = frozenset({"Version", "JournalID", "Sequence", "MAC", "Scope"})
_SUMMARY_FIELDS = frozenset({"Checkpoint", "Events"})
# 검증기가 저널·head 내용 때문에 낸 코드(제품 쪽). 그 밖(입력 파일을 못 읽음·취소·플랫폼)은 러너 쪽 장애다.
_PRODUCT_CODES = frozenset({"audit_integrity_failed", "audit_input_invalid", "audit_verification_limit_exceeded"})
SETTINGS_FIELDS = frozenset({"audit_key_variable", "volume", "directory", "subpath", "journal_name", "head_name", "uid",
                             "verify_tool", "verify_timeout_seconds"})
DRAFT_PLACEHOLDER = "{draft_id}"
_VARIABLE = re.compile(r"[A-Z][A-Z0-9_]{0,63}\Z")
_VOLUME_KEY = re.compile(r"[a-z0-9][a-z0-9_-]{0,63}\Z")
_DIR_NAME = re.compile(r"(?:\.|[a-z0-9][a-z0-9_-]{0,31})\Z")
_PART = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}\Z")
_FILE_NAME = re.compile(r"(?!\.+\Z)[A-Za-z0-9._-]{1,128}\Z")
MAX_SUBPATH_PARTS = 2  # runner_docker.copy_volume_files와 같다
MAX_VERIFY_SECONDS = 3600
RunVerifier = Callable[[list[str]], "tuple[int, bytes, bytes]"]


class E2V3Invalid(RuntimeError):
    """러너 쪽 장애로 회차를 쓰지 않는다. 고정 코드만 담는다."""

    def __init__(self, code: str):
        super().__init__("e2_v3: " + code)
        self.code = code


@dataclass(frozen=True)
class E2V3Settings:
    audit_key_variable: str      # 제품 overlay가 감사 키 파일 경로를 받는 compose 변수(동찬님 배선)
    volume: str                  # 저널·head가 있는 compose 볼륨 키
    directory: str               # 볼륨 루트 아래 첫 디렉터리
    subpath: tuple[str, ...]     # 그 아래 조각. `{draft_id}`는 초안 ID로 바뀐다
    journal_name: str
    head_name: str               # 신뢰 저장소가 확인한 봉인된 최종 head(e2-audit-verify --checkpoint 형식)
    uid: int                     # 꺼내기 도우미가 쓸 uid(그 파일 소유자)
    verify_tool: str             # e2-audit-verify 절대 경로(러너 호스트)
    verify_timeout_seconds: int

    def resolved_subpath(self, draft_id: str) -> tuple[str, ...]:
        parts = tuple(part.replace(DRAFT_PLACEHOLDER, draft_id) for part in self.subpath)
        if not all(_PART.fullmatch(part) for part in parts):
            raise E2V3Invalid("e2_v3_subpath_invalid")
        return parts


def parse_settings(raw: object) -> E2V3Settings:
    """`split.e2_v3` 설정. 모든 칸을 명시해야 하고 기본값은 없다(제품 배선이 정해지면 그 값을 넣는다)."""
    if not isinstance(raw, dict) or set(raw) != SETTINGS_FIELDS:
        raise ValueError("config.split.e2_v3: FIELDS")
    subpath = raw["subpath"]
    checks = (
        isinstance(raw["audit_key_variable"], str) and _VARIABLE.fullmatch(raw["audit_key_variable"]) is not None,
        isinstance(raw["volume"], str) and _VOLUME_KEY.fullmatch(raw["volume"]) is not None,
        isinstance(raw["directory"], str) and _DIR_NAME.fullmatch(raw["directory"]) is not None,
        isinstance(subpath, list) and len(subpath) <= MAX_SUBPATH_PARTS
        and all(isinstance(part, str) and _PART.fullmatch(part.replace(DRAFT_PLACEHOLDER, "d")) for part in subpath),
        all(isinstance(raw[name], str) and _FILE_NAME.fullmatch(raw[name]) for name in ("journal_name", "head_name")),
        raw["journal_name"] != raw["head_name"],
        type(raw["uid"]) is int and 0 <= raw["uid"] <= 65535,
        isinstance(raw["verify_tool"], str) and raw["verify_tool"].startswith("/"),
        type(raw["verify_timeout_seconds"]) is int and 1 <= raw["verify_timeout_seconds"] <= MAX_VERIFY_SECONDS,
    )
    if not all(checks):
        raise ValueError("config.split.e2_v3: INVALID")
    return E2V3Settings(audit_key_variable=raw["audit_key_variable"], volume=raw["volume"], directory=raw["directory"],
                        subpath=tuple(subpath), journal_name=raw["journal_name"], head_name=raw["head_name"], uid=raw["uid"],
                        verify_tool=raw["verify_tool"], verify_timeout_seconds=raw["verify_timeout_seconds"])


def new_audit_key(random_bytes: Callable[[int], bytes], *, other_keys: tuple[bytes, ...]) -> bytes:
    """회차별 감사 키. 경로 가림 키 등 다른 키와 같은 값이면(난수원 고장) 쓰지 않는다."""
    key = random_bytes(AUDIT_KEY_BYTES)
    if type(key) is not bytes or len(key) != AUDIT_KEY_BYTES or key in other_keys:
        raise E2V3Invalid("e2_v3_audit_key_invalid")
    return key


def audit_key_ref(key: bytes) -> str:
    """서명 근거에 남기는 키 참조(SHA-256 앞 16 hex, 경로 가림 키의 path_key_ref와 같은 방식). 키 값은 남기지 않는다."""
    return hashlib.sha256(b"abc2lab-e2-audit-key-ref\n" + key).hexdigest()[:16]


def _private_key_file(path: Path) -> bool:
    try:
        info = os.lstat(path)
    except OSError:
        return False
    return (stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == 0o600
            and info.st_size == AUDIT_KEY_BYTES)


def _write_copy(directory: Path, name: str, data: bytes) -> Path:
    path = directory / name
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
    except OSError:
        raise E2V3Invalid("e2_v3_copy_write_failed") from None
    return path


def _head(data: bytes) -> dict | None:
    if len(data) > MAX_HEAD_BYTES:
        return None
    try:
        head = json.loads(data.decode("utf-8"))
    except (UnicodeError, ValueError):
        return None
    if (not isinstance(head, dict) or set(head) != _HEAD_FIELDS or head["Version"] != JOURNAL_FORMAT_V3
            or type(head["Sequence"]) is not int or head["Sequence"] < 0):
        return None
    return head


def collect(*, run_verifier: RunVerifier, copy_directory: Path, key_path: Path, journal: bytes | None, head: bytes | None,
            key_ref: str) -> dict:
    """꺼낸 저널·head를 러너 전용 사본(0700 디렉터리, 0600 파일)으로 옮겨 검증한다. 서명 근거에 넣을 비민감 기록을 돌려준다.
    journal·head가 None이면 제품이 내지 않은 것(또는 꺼내지 못한 것)이다."""

    def record(outcome: str, reason: str | None, sha: dict, events: int | None, checkpoint: dict | None) -> dict:
        return {"format": EVIDENCE_FORMAT, "journal_format": JOURNAL_FORMAT_V3, "outcome": outcome, "reason": reason,
                "audit_key_ref": key_ref, "sha256": sha, "events": events, "head": checkpoint}

    if not _private_key_file(key_path):
        raise E2V3Invalid("e2_v3_audit_key_unavailable")
    sha = {name: hashlib.sha256(data).hexdigest() for name, data in (("journal", journal), ("head", head)) if data is not None}
    if journal is None or head is None:
        return record(OUTCOME_UNCOLLECTED, "e2_v3_export_unavailable", sha, None, None)
    if len(journal) > MAX_JOURNAL_BYTES:
        return record(OUTCOME_UNCOLLECTED, "e2_v3_journal_too_large", sha, None, None)
    checkpoint = _head(head)
    if checkpoint is None:
        return record(OUTCOME_UNCOLLECTED, "e2_v3_head_invalid", sha, None, None)
    try:
        os.mkdir(copy_directory, 0o700)
        os.chmod(copy_directory, 0o700)
    except OSError:
        raise E2V3Invalid("e2_v3_copy_directory_failed") from None
    journal_path = _write_copy(copy_directory, "journal.jsonl", journal)
    head_path = _write_copy(copy_directory, "head.json", head)
    code, stdout, stderr = run_verifier(["--journal-format", JOURNAL_FORMAT_V3, "--journal", str(journal_path),
                                         "--checkpoint", str(head_path), "--key-file", str(key_path)])
    if code != 0:
        reason = stderr.strip().decode("ascii", "replace")
        if reason in _PRODUCT_CODES:
            # 무결성·현재성 실패(ACK 불명 꼬리 포함)는 통제 대조만 미수집이다(고정 초안 §20, 동찬님 답).
            return record(OUTCOME_UNCOLLECTED, "e2_v3_" + reason, sha, None, checkpoint)
        raise E2V3Invalid("e2_v3_verifier_failed")
    try:
        summary = json.loads(stdout.decode("utf-8"))
    except (UnicodeError, ValueError):
        raise E2V3Invalid("e2_v3_verifier_output_invalid") from None
    if (not isinstance(summary, dict) or set(summary) != _SUMMARY_FIELDS or summary["Checkpoint"] != checkpoint
            or type(summary["Events"]) is not int or summary["Events"] < 0):
        raise E2V3Invalid("e2_v3_verifier_output_invalid")
    return record(OUTCOME_COLLECTED, None, sha, summary["Events"], checkpoint)
