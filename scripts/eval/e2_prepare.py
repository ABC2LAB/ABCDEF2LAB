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
import subprocess
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path

from e2_export import EVAL_HEADER_MODES, E2_LLM_BACKENDS, HEADER_PASSTHROUGH, TARGET_PUBLIC_PRACTICE

CONFIG_FORMAT = "abc2lab.e2-runner-config/v1"
# v1에 수용시험 경로 가림 키 파일을 더한 판(e2config.FormatPathKey). 공식 회차는 v1만 쓴다.
CONFIG_FORMAT_PATH_KEY = "abc2lab.e2-runner-config/v2"
CONFIG_NAME, POLICY_NAME, KEY_NAME = "e2-config.json", "policy.json", "audit.key"
PATH_KEY_NAME = "path.key"
CA_CERT_NAME, CA_KEY_NAME = "ca.pem", "ca.key"
CONTAINER_EVIDENCE_DIR, CONTAINER_CONTROL_DIR, CONTAINER_STATE_DIR = "/evidence", "/run/e2-control", "/run/e2-state"
AUDIT_KEY_BYTES = 32
# e2config.MaxPolicyBytes와 같아야 한다. 더 크면 제품 쪽 로더가 거부한다.
MAX_POLICY_BYTES = 256 * 1024
MAX_DECLARED_DIFFERENCES_BYTES = 64 * 1024
DECLARED_DIFFERENCES_FORMAT = "abc2lab.e2-declared-differences/v1"
DECLARED_DIFFERENCES_STATUSES = ("proposed", "confirmed")
# 평가 회차에는 요청마다 승인할 사람이 없다. 확인 모드면 첫 요청에서 시한까지 멈춘다.
EVAL_APPROVAL_MODE = "preauthorized_scope"
# 제품 정책 로더가 PEM 파일을 읽는 상한과 같다.
_MAX_CA_FILE_BYTES = 1 << 20
TIME_FORMAT = "%Y-%m-%dT%H:%M:%SZ"
# 검토한 의미 템플릿에 있으면 안 되는 필드. 회차마다 prepare 단계가 채운다.
_RUN_FILLED_FIELDS = frozenset({"run_ref", "valid_from", "valid_until", "state_dir", "helper", "helper_sha256", "ca_cert", "ca_key"})
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
class E2RunResources:
    """의미 템플릿에 채우는 회차 값. 전부 정책 내용 지문 밖의 필드다."""

    run_ref: str
    valid_from: str
    valid_until: str
    state_dir: str
    helper: str
    helper_sha256: str
    ca_cert_sha256: str
    ca_key_sha256: str
    connect_ip: str


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
    if not isinstance(values.header_mode, str) or values.header_mode not in EVAL_HEADER_MODES:
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


@dataclass(frozen=True)
class DeclaredDifferences:
    sha256: str
    status: str


def read_declared_differences(source: Path) -> DeclaredDifferences:
    """선언된 차이 목록의 해시와 확정 상태. 링크·특수 파일은 받지 않는다."""
    try:
        fd = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK)
    except OSError:
        _fail("declared_differences_unavailable")
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            _fail("declared_differences_not_regular")
        data = os.read(fd, MAX_DECLARED_DIFFERENCES_BYTES + 1)
    except OSError:
        _fail("declared_differences_unavailable")
    finally:
        os.close(fd)
    if len(data) > MAX_DECLARED_DIFFERENCES_BYTES:
        _fail("declared_differences_too_large")
    try:
        document = json.loads(data, object_pairs_hook=_no_duplicate_keys)
    except (ValueError, RecursionError):
        _fail("declared_differences_invalid")
    if not isinstance(document, dict) or document.get("format") != DECLARED_DIFFERENCES_FORMAT or document.get("status") not in DECLARED_DIFFERENCES_STATUSES:
        _fail("declared_differences_invalid")
    return DeclaredDifferences(hashlib.sha256(data).hexdigest(), document["status"])


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


def _encode_policy(document: dict) -> bytes:
    try:
        data = json.dumps(document, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode("utf-8") + b"\n"
    except (ValueError, RecursionError):
        _fail("policy_invalid")
    if len(data) > MAX_POLICY_BYTES:
        _fail("policy_too_large")
    return data


def _pinned_policy(document: dict, key: bytes) -> bytes:
    """회차마다 새 감사 키를 만들므로 정책의 pin도 회차마다 채운다. 정책 지문에는 감사 키가 들어가지 않는다."""
    return _encode_policy(dict(document, audit_key={"file": KEY_NAME, "sha256": hashlib.sha256(key).hexdigest()}))


def approval_window(now: datetime, seconds: int, limit: str) -> tuple[str, str]:
    """이번 회차의 승인 기간. 상위 기한을 넘으면 늘리거나 줄이지 않고 준비를 멈춘다."""
    start = now.astimezone(timezone.utc).replace(microsecond=0)
    end = start + timedelta(seconds=seconds)
    try:
        bound = datetime.strptime(limit, TIME_FORMAT).replace(tzinfo=timezone.utc)
    except (TypeError, ValueError):
        _fail("approval_limit_invalid")
    if end > bound:
        _fail("approval_window_exceeded")
    return start.strftime(TIME_FORMAT), end.strftime(TIME_FORMAT)


def render_policy(template: Path, resources: E2RunResources) -> bytes:
    """검토한 의미 템플릿에 회차 값을 채운다. 템플릿에 회차 값이 이미 있으면 어느 쪽이 맞는지 모르므로 거부한다."""
    document = _read_document(_read_policy(template))
    rules = document.get("rules")
    if not isinstance(rules, list) or not rules or not all(isinstance(rule, dict) for rule in rules):
        _fail("template_rules_invalid")
    evaluation = document.get("evaluation")
    if _RUN_FILLED_FIELDS & set(document) or any("connect_ip" in rule for rule in rules) or (isinstance(evaluation, dict) and "run_ref" in evaluation):
        _fail("template_has_run_value")
    filled = dict(document, run_ref=resources.run_ref, valid_from=resources.valid_from, valid_until=resources.valid_until,
                  state_dir=resources.state_dir, helper=resources.helper, helper_sha256=resources.helper_sha256,
                  ca_cert={"file": CA_CERT_NAME, "sha256": resources.ca_cert_sha256},
                  ca_key={"file": CA_KEY_NAME, "sha256": resources.ca_key_sha256},
                  rules=[dict(rule, connect_ip=resources.connect_ip) for rule in rules])
    if isinstance(evaluation, dict):
        filled["evaluation"] = dict(evaluation, run_ref=resources.run_ref)
    return _encode_policy(filled)


def _private_file_digest(directory: int, name: str) -> str:
    try:
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, dir_fd=directory)
    except OSError:
        _fail("ca_file_invalid")
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != _PRIVATE_FILE_MODE or info.st_uid != os.geteuid() or info.st_nlink != 1:
            _fail("ca_file_invalid")
        data = os.read(fd, _MAX_CA_FILE_BYTES + 1)
    except OSError:
        _fail("ca_file_invalid")
    finally:
        os.close(fd)
    if not data or len(data) > _MAX_CA_FILE_BYTES:
        _fail("ca_file_invalid")
    return hashlib.sha256(data).hexdigest()


def issue_run_ca(tool: Path, directory: Path, run_ref: str, expires: str, timeout: int) -> tuple[str, str]:
    """회차 CA를 준비 디렉터리에 만들고 인증서·개인키 파일의 digest를 돌려준다. 파일은 digest 계산에만 읽고 보관·출력하지 않는다."""
    command = [str(tool), "ca", "--cert-file", str(directory / CA_CERT_NAME), "--key-file", str(directory / CA_KEY_NAME), "--run-ref", run_ref, "--expires", expires]
    try:
        subprocess.run(command, check=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={}, timeout=timeout)
    except (OSError, ValueError, subprocess.SubprocessError):
        _fail("ca_tool_failed")
    fd = _open_private_dir(directory)
    try:
        return _private_file_digest(fd, CA_CERT_NAME), _private_file_digest(fd, CA_KEY_NAME)
    finally:
        os.close(fd)


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


def prepare_e2_config(directory: Path, values: E2RunValues, policy_source: Path | bytes,
                      path_key: bytes | None = None) -> E2Prepared:
    """설정·정책·키를 쓰고 러너가 대조할 값을 돌려준다. `config_sha256`은 쓴 설정 파일의 정확한 바이트 digest다.

    path_key는 수용시험 회차의 경로 가림 키(계측기 -path-key-file과 같은 키)다. 주면 v2 설정과 키 파일을 쓰고,
    eval-e2가 E② 시도 경로를 같은 규칙으로 가려 남긴다. 공식 회차는 주지 않는다(v1 그대로).
    """
    _check_values(values)
    if path_key is not None and (type(path_key) is not bytes or len(path_key) != AUDIT_KEY_BYTES):
        _fail("path_key_invalid")
    key = secrets.token_bytes(AUDIT_KEY_BYTES)
    if path_key is not None and key == path_key:
        _fail("path_key_invalid")
    source = policy_source if isinstance(policy_source, bytes) else _read_policy(policy_source)
    policy = _pinned_policy(_read_document(source), key)
    reservation_ref = new_reference("reservation")
    config = {"format": CONFIG_FORMAT, "eval_run_id": values.eval_run_id, "reservation_ref": reservation_ref, "model_tag": values.model_tag,
              "header_mode": values.header_mode, "evaluation": values.evaluation, "policy_file": POLICY_NAME, "audit_key_file": KEY_NAME,
              "listen": f":{values.port}", "upstream": f"http://{_UPSTREAM_HOST}:{values.port}",
              "evidence_dir": CONTAINER_EVIDENCE_DIR, "control_dir": CONTAINER_CONTROL_DIR}
    if path_key is not None:
        config.update(format=CONFIG_FORMAT_PATH_KEY, path_key_file=PATH_KEY_NAME)
    data = json.dumps(config, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode("utf-8") + b"\n"
    fd = _open_private_dir(directory)
    try:
        _write_new(fd, KEY_NAME, key)
        if path_key is not None:
            _write_new(fd, PATH_KEY_NAME, path_key)
        _write_new(fd, POLICY_NAME, policy)
        _write_new(fd, CONFIG_NAME, data)
        try:
            os.fsync(fd)
        except OSError:
            _fail("write_failed")
    finally:
        os.close(fd)
    return E2Prepared(hashlib.sha256(data).hexdigest(), hashlib.sha256(policy).hexdigest(), reservation_ref, directory / KEY_NAME)
