"""제품 E② 회차 준비 파일을 쓴다(제어 채널 제안 §4). 러너 연결과 제품 entrypoint는 아직 없다.

준비 디렉터리에 설정·정책·회차 감사 키를 쓴다. 이 디렉터리는 러너가 만들고 `eval-e2`에만 읽기 전용으로 mount한다.
키 값은 파일로만 두고 돌려주지도 기록하지도 않는다. 다시 쓰지 않으므로 같은 디렉터리에 두 번 준비하면 실패한다.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import secrets
import stat
from dataclasses import dataclass
from pathlib import Path

from e2_export import E2_HEADER_MODES, E2_LLM_BACKENDS, HEADER_PASSTHROUGH, TARGET_PUBLIC_PRACTICE

CONFIG_FORMAT = "abc2lab.e2-runner-config/v1"
CONFIG_NAME, POLICY_NAME, KEY_NAME = "e2-config.json", "policy.json", "audit.key"
CONTAINER_EVIDENCE_DIR, CONTAINER_CONTROL_DIR = "/evidence", "/run/e2-control"
AUDIT_KEY_BYTES = 32
# e2config.MaxPolicyBytes와 같아야 한다. 더 크면 제품 쪽 로더가 거부한다.
MAX_POLICY_BYTES = 256 * 1024
# 평가 회차에는 요청마다 승인할 사람이 없다. 확인 모드면 첫 요청에서 시한까지 멈춘다.
EVAL_APPROVAL_MODE = "preauthorized_scope"
_PRIVATE_DIR_MODE, _PRIVATE_FILE_MODE = 0o700, 0o600
_REF = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}\Z")
_REF_TOKEN_BYTES = 16
_MIN_PORT, _MAX_PORT = 1, 65535
_UPSTREAM_HOST = "eval-meter-upstream"


class E2PrepareError(RuntimeError):
    """고정 코드만 담는다. 경로·값·OS 오류 문구는 담지 않는다."""

    def __init__(self, code: str):
        super().__init__("e2_prepare: " + code)
        self.code = code


def _fail(code: str) -> None:
    raise E2PrepareError(code) from None


@dataclass(frozen=True)
class E2RunValues:
    eval_run_id: str
    model_tag: str
    header_mode: str
    evaluation: dict | None
    port: int


@dataclass(frozen=True)
class E2Prepared:
    config_sha256: str
    policy_sha256: str
    reservation_ref: str
    # 러너가 `e2-audit-verify --key-file`에 넘길 경로. 값은 담지 않는다.
    key_path: Path


def new_reference(prefix: str) -> str:
    """회차마다 새로 만드는 불투명 참조(reservation·close 요청). 승인 권한이 아니다."""
    return f"{prefix}-{secrets.token_hex(_REF_TOKEN_BYTES)}"


def _ref(value: object) -> bool:
    return isinstance(value, str) and bool(_REF.fullmatch(value)) and "://" not in value


def _check_values(values: E2RunValues) -> None:
    if not _ref(values.eval_run_id) or not _ref(values.model_tag):
        _fail("run_values_invalid")
    if not isinstance(values.header_mode, str) or values.header_mode not in E2_HEADER_MODES:
        _fail("header_mode_invalid")
    if type(values.port) is not int or not _MIN_PORT <= values.port <= _MAX_PORT:
        _fail("port_invalid")
    evaluation = values.evaluation
    if values.header_mode != HEADER_PASSTHROUGH:
        # 평가 선언 없음과 빈 선언을 가른다(연결 제안 §4.1).
        if evaluation is not None:
            _fail("evaluation_unexpected")
        return
    if not isinstance(evaluation, dict) or set(evaluation) != {"llm_backend", "target_kind"}:
        _fail("evaluation_invalid")
    if not isinstance(evaluation["llm_backend"], str) or evaluation["llm_backend"] not in E2_LLM_BACKENDS:
        _fail("evaluation_invalid")
    if evaluation["target_kind"] != TARGET_PUBLIC_PRACTICE:
        _fail("evaluation_invalid")


def _read_policy(source: Path) -> bytes:
    """검토·동결한 정책 파일을 한 번 읽는다. 링크·특수 파일은 받지 않는다."""
    try:
        fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK)
    except OSError:
        _fail("policy_unavailable")
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            _fail("policy_not_regular")
        data = os.read(fd, MAX_POLICY_BYTES + 1)
    except OSError:
        _fail("policy_unavailable")
    finally:
        os.close(fd)
    if len(data) > MAX_POLICY_BYTES:
        _fail("policy_too_large")
    return data


def _no_duplicate_keys(pairs: list) -> dict:
    document: dict = {}
    for key, value in pairs:
        if key in document:
            raise ValueError("duplicate key")
        document[key] = value
    return document


def _read_document(policy: bytes) -> dict:
    """검토·동결한 정책을 읽는다. 다시 직렬화하면 중복 키가 조용히 사라지므로 여기서 거부한다."""
    try:
        document = json.loads(policy, object_pairs_hook=_no_duplicate_keys)
    except (ValueError, RecursionError):
        _fail("policy_invalid")
    if not isinstance(document, dict) or document.get("approval_mode") != EVAL_APPROVAL_MODE:
        _fail("policy_approval_mode")
    if "audit_key" in document:
        _fail("policy_audit_key_preset")
    return document


def _pinned_policy(document: dict, key: bytes) -> bytes:
    """회차마다 새 감사 키를 만들므로 정책의 pin도 회차마다 채운다. 정책 지문에는 감사 키가 들어가지 않는다."""
    pinned = dict(document, audit_key={"file": KEY_NAME, "sha256": hashlib.sha256(key).hexdigest()})
    try:
        data = json.dumps(pinned, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode("utf-8") + b"\n"
    except (ValueError, RecursionError):
        _fail("policy_invalid")
    if len(data) > MAX_POLICY_BYTES:
        _fail("policy_too_large")
    return data


def _open_private_dir(directory: Path) -> int:
    try:
        fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    except OSError:
        _fail("directory_unavailable")
    try:
        info = os.fstat(fd)
    except OSError:
        os.close(fd)
        _fail("directory_unavailable")
    if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != _PRIVATE_DIR_MODE:
        os.close(fd)
        _fail("directory_not_private")
    return fd


def _write_new(directory: int, name: str, data: bytes) -> None:
    try:
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, _PRIVATE_FILE_MODE, dir_fd=directory)
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
    except FileExistsError:
        _fail("already_prepared")
    except OSError:
        _fail("write_failed")


def prepare_e2_config(directory: Path, values: E2RunValues, policy_source: Path) -> E2Prepared:
    """설정·정책·키를 쓰고 러너가 대조할 값을 돌려준다. `config_sha256`은 쓴 설정 파일의 정확한 바이트 digest다."""
    _check_values(values)
    key = secrets.token_bytes(AUDIT_KEY_BYTES)
    policy = _pinned_policy(_read_document(_read_policy(policy_source)), key)
    reservation_ref = new_reference("reservation")
    config = {"format": CONFIG_FORMAT, "eval_run_id": values.eval_run_id, "reservation_ref": reservation_ref, "model_tag": values.model_tag,
              "header_mode": values.header_mode, "evaluation": values.evaluation, "policy_file": POLICY_NAME, "audit_key_file": KEY_NAME,
              "listen": f":{values.port}", "upstream": f"http://{_UPSTREAM_HOST}:{values.port}",
              "evidence_dir": CONTAINER_EVIDENCE_DIR, "control_dir": CONTAINER_CONTROL_DIR}
    data = json.dumps(config, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode("utf-8") + b"\n"
    fd = _open_private_dir(directory)
    try:
        _write_new(fd, KEY_NAME, key)
        _write_new(fd, POLICY_NAME, policy)
        _write_new(fd, CONFIG_NAME, data)
        try:
            os.fsync(fd)
        except OSError:
            _fail("write_failed")
    finally:
        os.close(fd)
    return E2Prepared(hashlib.sha256(data).hexdigest(), hashlib.sha256(policy).hexdigest(), reservation_ref, directory / KEY_NAME)
