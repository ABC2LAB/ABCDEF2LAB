"""탐색 분리 배치(개선 후 O) 회차의 제품 보호 입력 생성·정리(탐색 분리 배치 결정안 §3 B(a), 순서 4-7c-1).

제품 compose는 저장소 밖 보호 입력 여섯 개를 파일로 받는다(deploy/product/README.md): 설치 입력(install.json), postgres
비밀번호, 탐색 경계 설정(boundary.json), 경계 CA 인증서·개인키, 경로 가림 키. 러너는 회차마다 이것을 새로 만든다.
- 위치: 회차 디렉터리 안 private 하위 디렉터리(러너 uid 0700). 근거 디렉터리가 아니며 서명하지 않는다. down 뒤 지운다.
- 파일: 새로 만들고(O_EXCL) 0600. 이미 있으면 거부한다(이전 회차 입력을 다시 쓰지 않는다).
- 관리자 비밀번호·postgres 비밀번호·경로 가림 키는 회차마다 무작위로 만든다. 관리자 비밀번호는 제품 구동기 로그인에만 쓴다.
- 경계 CA는 README 절차와 같은 openssl 인자(P-256, CA:TRUE, keyCertSign)로 만들되 유효 기간은 회차용으로 짧게 둔다.
서명 기록에는 비밀이 없는 부분만 남긴다(설치 입력에서 관리자 비밀번호를 뺀 것, 경계 설정). 모델 키는 여기 넣지 않는다
(설치 뒤 제품 API로 넣는다, runner_product).
"""

from __future__ import annotations

import base64
import json
import os
import stat
import subprocess
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from runner_engine import AdminSecret
from runner_e2v3 import AUDIT_KEY_FILE, E2V3Invalid, audit_key_ref, new_audit_key
from runner_split import boundary_config, install_input, read_boundary_template

PRIVATE_DIRECTORY = "private"
FILES = {"install": "install.json", "postgres_password": "postgres-password", "boundary": "boundary.json",
         "ca_cert": "explore-ca.pem", "ca_key": "explore-ca.key", "path_key": "path.key"}
# 선택 보호 입력: E② v3 감사 키(split.e2_v3가 있을 때만 만든다, runner_e2v3). 지울 때는 늘 알려진 이름으로 본다.
OPTIONAL_FILES = {"e2_audit_key": AUDIT_KEY_FILE}
# compose가 읽는 변수(deploy/product/compose.yaml secrets) ← 위 파일.
COMPOSE_FILE_ENV = {"PRODUCT_INSTALL_FILE": "install", "PRODUCT_POSTGRES_PASSWORD_FILE": "postgres_password",
                    "EXPLORE_BOUNDARY_CONFIG_FILE": "boundary", "EXPLORE_CA_CERT_FILE": "ca_cert",
                    "EXPLORE_CA_KEY_FILE": "ca_key", "EXPLORE_PATH_KEY_FILE": "path_key"}
PASSWORD_BYTES = 24
PATH_KEY_BYTES = 32
# 경계 CA 유효 기간(일). 회차 길이를 넉넉히 덮으면 된다(README 예시는 운영용 365일).
CA_VALID_DAYS = 2
CA_SUBJECT = "/CN=JinDo explore boundary CA"

Openssl = Callable[[list[str]], int]
OPENSSL_TIMEOUT_SECONDS = 60


def run_openssl(args: list[str]) -> int:
    """호스트 openssl을 셸 없이 한 번 실행한다. 출력은 버린다(키 내용을 내보내지 않는 명령만 쓴다)."""
    try:
        return subprocess.run(["openssl", *args], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                              stderr=subprocess.DEVNULL, timeout=OPENSSL_TIMEOUT_SECONDS,
                              env={"PATH": os.environ.get("PATH", "/usr/bin:/bin")}).returncode
    except (OSError, subprocess.SubprocessError):
        return -1


class SplitInputsError(RuntimeError):
    """보호 입력을 만들거나 지울 수 없음. 고정 code만 담는다(비밀 값·경로는 담지 않는다)."""


@dataclass(frozen=True)
class SplitInputs:
    directory: Path
    files: dict[str, Path]
    compose_env: dict[str, str]
    admin: AdminSecret          # 제품 구동기 로그인용(repr에 값이 없다)
    install_public: dict        # 관리자 비밀번호를 뺀 설치 입력(서명 기록용)
    boundary: dict              # 경계 설정(비밀 없음)
    audit_key_ref: str | None = None  # E② v3 감사 키 참조(값 아님). 키를 만들지 않았으면 None


def _secret_text(random_bytes: Callable[[int], bytes], size: int) -> str:
    return base64.urlsafe_b64encode(random_bytes(size)).decode("ascii").rstrip("=")


def _write_new(path: Path, data: bytes) -> None:
    """새 파일을 0600으로 만든다. 이미 있으면(링크 포함) 거부한다."""
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    except OSError:
        raise SplitInputsError("split_input_write_failed") from None
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
    except OSError:
        raise SplitInputsError("split_input_write_failed") from None


def _private_regular(path: Path) -> bool:
    try:
        info = os.lstat(path)
    except OSError:
        return False
    return stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == 0o600


def prepare_split_inputs(run_directory: Path, *, manifest: dict | None, meter_port: int, explore_port: int, evidence_max_bytes: int,
                         workflow_defaults: dict, wide_writes: bool, valid_for_hours: int, max_runs: int, run_seconds: int,
                         path_key: bytes,
                         openssl: Openssl, random_bytes: Callable[[int], bytes] = os.urandom,
                         local_model: dict | None = None, e2_audit_key_variable: str | None = None) -> SplitInputs:
    """회차 디렉터리 안에 보호 입력을 만든다. 실패하면 만든 것을 지우고 고정 code로 멈춘다.

    path_key는 계측기 -path-key-file과 같은 키(러너 설정 path_key_file을 path_mask.read_path_key로 읽은 값)다. 경계는 이
    사본으로 경로를 가린다(계약 §4.1). 회차마다 새로 만들면 경계·계측기 행을 (메서드, 가린 경로)로 맞출 수 없다.

    e2_audit_key_variable이 있으면 회차별 E② v3 감사 키를 새로 만들어 보호 입력으로 두고, 제품 overlay가 읽는 그 compose
    변수에 경로를 건다(주입 경로는 동찬님 배선, 설정값). 검증기는 같은 파일을 --key-file로 받는다. 경로 가림 키와 같으면 거부한다.
    """
    if type(path_key) is not bytes or len(path_key) != PATH_KEY_BYTES:
        raise SplitInputsError("split_path_key_invalid")
    if e2_audit_key_variable is not None and e2_audit_key_variable in COMPOSE_FILE_ENV:
        raise SplitInputsError("split_audit_key_variable_invalid")
    directory = Path(run_directory) / PRIVATE_DIRECTORY
    try:
        os.mkdir(directory, 0o700)
        os.chmod(directory, 0o700)
    except OSError:
        raise SplitInputsError("split_input_directory_failed") from None
    names = dict(FILES, **(OPTIONAL_FILES if e2_audit_key_variable is not None else {}))
    files = {name: directory / file_name for name, file_name in names.items()}
    key_ref = None
    try:
        admin = AdminSecret(_secret_text(random_bytes, PASSWORD_BYTES))
        install = install_input(admin_password=admin.reveal(), manifest=manifest, meter_port=meter_port,
                                workflow_defaults=workflow_defaults, wide_writes=wide_writes,
                                valid_for_hours=valid_for_hours, max_runs=max_runs, run_seconds=run_seconds,
                                local_model=local_model)
        boundary = boundary_config(read_boundary_template(), explore_port, evidence_max_bytes)
        _write_new(files["install"], json.dumps(install, ensure_ascii=False, sort_keys=True).encode("utf-8"))
        _write_new(files["postgres_password"], _secret_text(random_bytes, PASSWORD_BYTES).encode("ascii"))
        _write_new(files["boundary"], json.dumps(boundary, sort_keys=True).encode("utf-8"))
        _write_new(files["path_key"], path_key)
        if e2_audit_key_variable is not None:
            try:
                audit_key = new_audit_key(random_bytes, other_keys=(path_key,))
            except E2V3Invalid:
                raise SplitInputsError("split_audit_key_failed") from None
            _write_new(files["e2_audit_key"], audit_key)
            key_ref = audit_key_ref(audit_key)
            del audit_key
        code = openssl(["req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
                        "-days", str(CA_VALID_DAYS), "-subj", CA_SUBJECT,
                        "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign",
                        "-keyout", str(files["ca_key"]), "-out", str(files["ca_cert"])])
        if code != 0:
            raise SplitInputsError("split_ca_failed")
        for name in ("ca_cert", "ca_key"):
            # openssl은 umask대로 만든다. 링크가 아닌 일반 파일인지 보고 0600으로 맞춘다.
            if os.path.islink(files[name]) or not files[name].is_file():
                raise SplitInputsError("split_ca_failed")
            os.chmod(files[name], 0o600)
        if not all(_private_regular(path) for path in files.values()):
            raise SplitInputsError("split_input_write_failed")
    except BaseException:
        remove_split_inputs(directory, missing_ok=True)
        raise
    public = {key: value for key, value in install.items() if key != "admin_password"}
    compose_env = {env: str(files[name]) for env, name in COMPOSE_FILE_ENV.items()}
    if e2_audit_key_variable is not None:
        compose_env[e2_audit_key_variable] = str(files["e2_audit_key"])
    return SplitInputs(directory=directory, files=files, compose_env=compose_env,
                       admin=admin, install_public=public, boundary=boundary, audit_key_ref=key_ref)


def remove_split_inputs(directory: Path, *, missing_ok: bool = False) -> None:
    """보호 입력 디렉터리를 지운다. 알려진 파일 이름만 지우고, 그 밖의 것이 남으면 디렉터리를 남기고 실패한다."""
    directory = Path(directory)
    if not directory.exists():
        if missing_ok:
            return
        raise SplitInputsError("split_input_cleanup_failed")
    known = set(FILES.values()) | set(OPTIONAL_FILES.values())
    try:
        for entry in os.listdir(directory):
            if entry in known:
                os.unlink(directory / entry)
        os.rmdir(directory)
    except OSError:
        raise SplitInputsError("split_input_cleanup_failed") from None
