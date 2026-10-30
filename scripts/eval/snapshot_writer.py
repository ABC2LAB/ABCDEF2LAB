"""독립 평가 수집기가 받은 비민감 챌린지 응답을 pre/post 자료로 저장한다.

네트워크·엔진 제어·귀속 판정·서명은 수행하지 않는다. 신뢰 러너가 기록한 실제 수집
시각/sequence와 이 함수의 descriptor를 묶어야 PR34의 검증 입력이 된다.
"""

from __future__ import annotations

import hashlib
import json
import math
import os
import stat
from dataclasses import asdict, dataclass
from pathlib import Path

from ground_truth import bac_category, core_gt
from scoreboard import SnapshotError, normalize_challenges, require_basis_keys

_SENSITIVE_FIELDS = frozenset({"authorization", "cookie", "set-cookie", "password", "token", "secret",
                               "access_token", "refresh_token"})


class SnapshotWriteError(ValueError):
    """입력/저장 실패. 원본 내용과 민감값은 메시지에 포함하지 않는다."""


@dataclass(frozen=True)
class SnapshotContext:
    run_id: str
    model_tag: str
    reset_generation: str
    instance_id: str
    execution_id: str


@dataclass(frozen=True)
class SnapshotArtifacts:
    raw: dict
    normalized: dict
    challenge_count: int
    solved_count: int


def _validate_text(value: str) -> None:
    try:
        value.encode("utf-8")
    except UnicodeError:
        raise SnapshotWriteError("UTF-8로 표현할 수 없는 문자열") from None


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        _validate_text(key)
        if key in result:
            raise SnapshotWriteError("원본 JSON key 중복")
        result[key] = value
    return result


def _bad_constant(_):
    raise SnapshotWriteError("원본 JSON 비유한수 금지")


def _validate_raw(raw: bytes) -> dict[str, bool]:
    try:
        payload = json.loads(raw, object_pairs_hook=_unique_object, parse_constant=_bad_constant)
    except SnapshotWriteError:
        raise
    except (ValueError, UnicodeDecodeError, RecursionError) as exc:
        raise SnapshotWriteError("원본 JSON 파싱 실패") from exc
    pending = [payload]
    while pending:
        value = pending.pop()
        if isinstance(value, dict):
            for key, item in value.items():
                if key.lower() in _SENSITIVE_FIELDS and item not in (None, "[REDACTED]"):
                    raise SnapshotWriteError("원본에 민감 필드값이 있음")
                pending.append(item)
        elif isinstance(value, list):
            pending.extend(value)
        elif isinstance(value, str):
            _validate_text(value)
        elif isinstance(value, float) and not math.isfinite(value):
            raise SnapshotWriteError("원본 JSON 비유한수 금지")
    solved = normalize_challenges(payload)
    require_basis_keys(solved, core_gt() + bac_category())
    return solved


def _write_file(directory: int, name: str, data: bytes, created_files: list[str]) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC
    opened = os.open(name, flags, 0o600, dir_fd=directory)
    created_files.append(name)
    with os.fdopen(opened, "wb") as stream:
        if stream.write(data) != len(data):
            raise SnapshotWriteError("스냅샷 파일 쓰기 미완료")
        stream.flush()
        os.fsync(stream.fileno())


def write_snapshot(run_directory: str | Path, phase: str, raw: bytes, context: SnapshotContext,
                   *, max_bytes: int) -> SnapshotArtifacts:
    """새 phase 디렉터리에 두 파일을 완성한 뒤 descriptor를 반환한다. 기존 자료는 거부한다.

    raw는 비민감 챌린지 응답 body만 받는다. max_bytes는 수집기 설정으로 명시한다.
    요청 종료나 해결 주체에 관한 근거는 이 함수가 만들지 않는다.
    """
    if not isinstance(run_directory, (str, os.PathLike)):
        raise SnapshotWriteError("run_directory 경로 필요")
    if phase not in ("pre", "post"):
        raise SnapshotWriteError("phase는 pre 또는 post여야 함")
    if not isinstance(context, SnapshotContext):
        raise SnapshotWriteError("SnapshotContext 필요")
    metadata = asdict(context)
    if any(not isinstance(value, str) or not value.strip() for value in metadata.values()):
        raise SnapshotWriteError("회차 신원 필드가 비어 있거나 문자열이 아님")
    for value in metadata.values():
        _validate_text(value)
    if type(max_bytes) is not int or max_bytes <= 0:
        raise SnapshotWriteError("max_bytes는 양의 정수여야 함")
    if not isinstance(raw, bytes) or not raw or len(raw) > max_bytes:
        raise SnapshotWriteError("원본 bytes 부재/크기 상한 초과")
    try:
        solved = _validate_raw(raw)
    except SnapshotError as exc:
        raise SnapshotWriteError("원본 챌린지 구조/GT key 검증 실패") from None
    envelope = dict(metadata, phase=phase, solved=solved)
    try:
        normalized = json.dumps(envelope, ensure_ascii=False, sort_keys=True,
                                separators=(",", ":"), allow_nan=False).encode("utf-8")
    except (ValueError, RecursionError):
        raise SnapshotWriteError("정규화 JSON 직렬화 실패") from None
    if not all(hasattr(os, name) for name in ("O_DIRECTORY", "O_NOFOLLOW", "O_CLOEXEC")):
        raise SnapshotWriteError("안전한 dir_fd/O_NOFOLLOW 저장을 지원하는 환경 필요")

    root_fd = child_fd = None
    created = False
    created_files: list[str] = []
    created_identity = None
    try:
        root_fd = os.open(run_directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        os.mkdir(phase, 0o700, dir_fd=root_fd)
        created = True
        created_stat = os.stat(phase, dir_fd=root_fd, follow_symlinks=False)
        created_identity = (created_stat.st_dev, created_stat.st_ino)
        child_fd = os.open(phase, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC,
                           dir_fd=root_fd)
        opened_stat = os.fstat(child_fd)
        if (opened_stat.st_dev, opened_stat.st_ino) != created_identity:
            raise SnapshotWriteError("단계 디렉터리 경로가 교체됨")
        _write_file(child_fd, "raw.json", raw, created_files)
        _write_file(child_fd, "normalized.json", normalized, created_files)
        os.fsync(child_fd)
        os.fsync(root_fd)
    except (OSError, ValueError, RuntimeError) as exc:
        cleanup_failed = False
        if created:
            if child_fd is not None:
                # 다른 프로세스가 경로를 바꿨더라도 열린 자신의 디렉터리 안에서만 정리한다.
                for name in created_files:
                    try:
                        os.unlink(name, dir_fd=child_fd)
                    except FileNotFoundError:
                        pass
                    except OSError:
                        cleanup_failed = True
            try:
                current = os.stat(phase, dir_fd=root_fd, follow_symlinks=False)
                if (created_identity != (current.st_dev, current.st_ino)
                        or not stat.S_ISDIR(current.st_mode)):
                    cleanup_failed = True
                else:
                    os.rmdir(phase, dir_fd=root_fd)
            except OSError:
                cleanup_failed = True
        message = "스냅샷 저장 실패(기존 자료/경로/파일 오류)"
        if cleanup_failed:
            message += "; 미완성 생성물 정리 확인 필요"
        raise SnapshotWriteError(message) from exc
    finally:
        if child_fd is not None:
            os.close(child_fd)
        if root_fd is not None:
            os.close(root_fd)

    def descriptor(filename: str, data: bytes) -> dict:
        return dict(metadata, phase=phase, path=f"{phase}/{filename}", size=len(data),
                    sha256=hashlib.sha256(data).hexdigest())

    return SnapshotArtifacts(descriptor("raw.json", raw), descriptor("normalized.json", normalized),
                             len(solved), sum(solved.values()))
