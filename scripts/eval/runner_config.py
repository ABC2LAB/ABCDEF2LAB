"""평가 러너 설정: 비민감 회차 설정을 검증하고 compose에 넘길 값과 회차 디렉터리를 만든다.

비밀(DB·관리자 비밀번호, API 키)은 러너가 읽지 않는다. compose가 비밀 env 파일에서 직접 읽고,
러너는 나머지 값을 프로세스 환경으로 넘긴다. compose 보간은 셸 환경을 env 파일보다 우선하므로
비밀 파일에 같은 이름이 있어도 러너 값이 쓰인다. 값은 전부 설정에서 오며 코드 기본값은 없다.
LLM 모델·주소·대상 이미지·계측기 origin은 사전검사 선언(preflight)에서 가져와 두 곳이 갈라지지 않게 한다.
"""

from __future__ import annotations

import ipaddress
import json
import os
import re
import secrets
import stat
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit

from preflight import MAX_CONFIG_BYTES, PreflightError, _image_reference, check_preflight

CONFIG_VERSION = 1
SIDES = ("baseline", "ours")
REPO_ROOT = Path(__file__).resolve().parents[2]
COMPOSE_COMMON = REPO_ROOT / "deploy" / "engine-isolated" / "compose.yaml"
COMPOSE_LOCAL_LLM = REPO_ROOT / "deploy" / "engine-isolated" / "compose.local-llm.yaml"
COMPOSE_EVAL = REPO_ROOT / "deploy" / "eval" / "compose.eval.yaml"
METER_DIR_NAME = "meter"
# compose가 비밀 env 파일에서 직접 읽는 이름. 러너는 이 값을 만들지도 읽지도 않는다.
# compose 파일의 나머지 ${VAR}는 전부 러너가 넘긴다(테스트로 집합 일치를 고정).
SECRET_ENV_NAMES = frozenset({"EVAL_ENGINE_ADMIN_PASSWORD", "EVAL_ENGINE_DB_PASSWORD",
                              "OPENAI_API_KEY", "ANTHROPIC_API_KEY"})
# compose 서비스·망 키(deploy/ 파일과 같은 이름, 테스트로 고정). 엔진은 계측기 이름으로만 대상에 닿는다.
ENGINE_SERVICE = "engine"
ENGINE_DB_SERVICE = "engine-db"
TARGET_SERVICE = "target"
METER_SERVICE = "eval-meter"
OBS_SERVICE = "eval-obs"
ENGINE_NET = "engine_net"
TARGET_NET = "target_net"
LLM_NET = "llm_net"
OBS_NET = "obs_net"
MAX_HOST_LISTEN_ALLOW = 32
MAX_REASON_LENGTH = 200
# 점검 하나의 연결·해석 제한. 실패 기대 경로는 대부분 timeout으로 끝나므로 회차 준비 시간을 키운다.
MAX_PROBE_TIMEOUT_MS = 60_000

_IMAGE_ID = re.compile(r"sha256:[0-9a-f]{64}\Z")
_TMPFS_SIZE = re.compile(r"[1-9][0-9]{0,8}[kmg]?\Z")
_DURATION = re.compile(r"[1-9][0-9]{0,5}(?:ms|s|m)\Z")
_DB_NAME = re.compile(r"[a-z_][a-z0-9_]{0,62}\Z")
_LLM_PROVIDERS = ("anthropic", "openai")
_IMAGE_ROLES = ("engine_baseline", "engine_ours", "engine_db", "meter")
_PORTS = ("engine_api_host", "engine_container", "target", "obs", "obs_host")
_TOP_FIELDS = ("version", "runs_root", "secrets_env_file", "images", "target_image_id", "ports",
               "observer_path", "engine", "engine_db", "tmpfs", "health", "docker_timeout_seconds",
               "host_listen_allowlist", "probe_timeout_ms")


class RunnerConfigError(ValueError):
    """설정 오류. 필드 이름과 이유 코드만 담고 값은 담지 않는다."""

    def __init__(self, errors: list[str]):
        super().__init__("; ".join(errors))
        self.errors = errors


@dataclass(frozen=True)
class Image:
    ref: str
    id: str


@dataclass(frozen=True)
class HostListen:
    """loopback 밖 호스트 리슨 중 허용할 것 하나. 근거(reason)를 함께 기록한다."""

    address: str
    port: int
    reason: str


@dataclass(frozen=True)
class RunnerConfig:
    runs_root: Path
    secrets_env_file: Path
    images: dict[str, Image]
    target_image_id: str
    ports: dict[str, int]
    observer_path: str
    llm_provider: str
    llm_stream: bool
    engine_db_user: str
    engine_db_name: str
    tmpfs_engine_db: str
    tmpfs_engine_data: str
    health_interval: str
    health_timeout: str
    health_retries: int
    docker_timeout_seconds: float
    host_listen_allowlist: tuple[HostListen, ...]
    probe_timeout_ms: int


@dataclass(frozen=True)
class RunPlan:
    """한 회차의 비민감 계획. compose_env에는 비밀이 없다."""

    side: str
    run_id: str
    project: str
    source_sha: str
    llm: dict
    target_image: Image
    engine_image: Image
    images: dict[str, Image]
    meter_port: int
    compose_files: tuple[Path, ...]
    secrets_env_file: Path
    compose_env: dict[str, str]


@dataclass(frozen=True)
class RunDirectory:
    run: Path
    meter: Path


def _exact(raw: object, names: tuple[str, ...], prefix: str, errors: list[str]) -> dict:
    if not isinstance(raw, dict):
        errors.append(f"{prefix}: EXPECTED_OBJECT")
        return {}
    if set(raw) - set(names):
        errors.append(f"{prefix}: UNKNOWN_FIELDS")
    for name in names:
        if name not in raw:
            errors.append(f"{prefix}.{name}: MISSING")
    return raw


def _int(value: object, low: int, high: int) -> bool:
    return type(value) is int and low <= value <= high


def _absolute_path(value: object) -> bool:
    return (isinstance(value, str) and value.startswith("/") and "\x00" not in value
            and os.path.normpath(value) == value)


def _observer_path(value: object) -> bool:
    # eval-meter의 -allow-path 규칙과 같다(cmd/eval-meter/main.go validAllowPath).
    if not isinstance(value, str) or not value.startswith("/") or "//" in value:
        return False
    if any(char in value for char in "?#%\\ ") or not all(0x21 <= ord(char) <= 0x7e for char in value):
        return False
    return all(segment not in (".", "..") for segment in value[1:].split("/"))


def _image(raw: object, prefix: str, errors: list[str]) -> Image | None:
    fields = _exact(raw, ("ref", "id"), prefix, errors)
    ok = True
    if "ref" in fields and not _image_reference(fields["ref"]):
        errors.append(f"{prefix}.ref: INVALID")
        ok = False
    if "id" in fields and not (isinstance(fields["id"], str) and _IMAGE_ID.fullmatch(fields["id"])):
        errors.append(f"{prefix}.id: INVALID")
        ok = False
    return Image(fields["ref"], fields["id"]) if ok and "ref" in fields and "id" in fields else None


def parse_runner_config(raw: object) -> RunnerConfig:
    errors: list[str] = []
    top = _exact(raw, _TOP_FIELDS, "config", errors)
    if "version" in top and not (type(top["version"]) is int and top["version"] == CONFIG_VERSION):
        errors.append("config.version: UNSUPPORTED")
    for name in ("runs_root", "secrets_env_file"):
        if name in top and not _absolute_path(top[name]):
            errors.append(f"config.{name}: EXPECTED_ABSOLUTE_PATH")
    images: dict[str, Image] = {}
    image_fields = _exact(top.get("images"), _IMAGE_ROLES, "config.images", errors) if "images" in top else {}
    for role in _IMAGE_ROLES:
        if role in image_fields:
            image = _image(image_fields[role], f"config.images.{role}", errors)
            if image:
                images[role] = image
    if "target_image_id" in top and not (isinstance(top["target_image_id"], str)
                                         and _IMAGE_ID.fullmatch(top["target_image_id"])):
        errors.append("config.target_image_id: INVALID")
    ports = _exact(top.get("ports"), _PORTS, "config.ports", errors) if "ports" in top else {}
    for name in _PORTS:
        if name in ports and not _int(ports[name], 1, 65535):
            errors.append(f"config.ports.{name}: INVALID")
    if all(_int(ports.get(name), 1, 65535) for name in ("engine_api_host", "obs_host")):
        if ports["engine_api_host"] == ports["obs_host"]:
            errors.append("config.ports: HOST_PORT_CONFLICT")
    if "observer_path" in top and not _observer_path(top["observer_path"]):
        errors.append("config.observer_path: INVALID")
    engine = _exact(top.get("engine"), ("llm_provider", "llm_stream"), "config.engine", errors) if "engine" in top else {}
    if "llm_provider" in engine and engine["llm_provider"] not in _LLM_PROVIDERS:
        errors.append("config.engine.llm_provider: UNSUPPORTED")
    if "llm_stream" in engine and type(engine["llm_stream"]) is not bool:
        errors.append("config.engine.llm_stream: EXPECTED_BOOL")
    db = _exact(top.get("engine_db"), ("user", "name"), "config.engine_db", errors) if "engine_db" in top else {}
    for name in ("user", "name"):
        if name in db and not (isinstance(db[name], str) and _DB_NAME.fullmatch(db[name])):
            errors.append(f"config.engine_db.{name}: INVALID")
    tmpfs = _exact(top.get("tmpfs"), ("engine_db", "engine_data"), "config.tmpfs", errors) if "tmpfs" in top else {}
    for name in ("engine_db", "engine_data"):
        if name in tmpfs and not (isinstance(tmpfs[name], str) and _TMPFS_SIZE.fullmatch(tmpfs[name])):
            errors.append(f"config.tmpfs.{name}: INVALID")
    health = _exact(top.get("health"), ("interval", "timeout", "retries"), "config.health", errors) if "health" in top else {}
    for name in ("interval", "timeout"):
        if name in health and not (isinstance(health[name], str) and _DURATION.fullmatch(health[name])):
            errors.append(f"config.health.{name}: INVALID")
    if "retries" in health and not _int(health["retries"], 1, 1000):
        errors.append("config.health.retries: INVALID")
    timeout = top.get("docker_timeout_seconds")
    if "docker_timeout_seconds" in top and not (type(timeout) in (int, float) and 0 < timeout <= 3600):
        errors.append("config.docker_timeout_seconds: INVALID")
    allowlist = _host_listen_allowlist(top.get("host_listen_allowlist"), errors) if "host_listen_allowlist" in top else ()
    if "probe_timeout_ms" in top and not _int(top["probe_timeout_ms"], 1, MAX_PROBE_TIMEOUT_MS):
        errors.append("config.probe_timeout_ms: INVALID")
    if errors:
        raise RunnerConfigError(errors)
    return RunnerConfig(
        runs_root=Path(top["runs_root"]), secrets_env_file=Path(top["secrets_env_file"]), images=images,
        target_image_id=top["target_image_id"], ports=dict(ports), observer_path=top["observer_path"],
        llm_provider=engine["llm_provider"], llm_stream=engine["llm_stream"],
        engine_db_user=db["user"], engine_db_name=db["name"],
        tmpfs_engine_db=tmpfs["engine_db"], tmpfs_engine_data=tmpfs["engine_data"],
        health_interval=health["interval"], health_timeout=health["timeout"], health_retries=health["retries"],
        docker_timeout_seconds=float(timeout), host_listen_allowlist=allowlist,
        probe_timeout_ms=top["probe_timeout_ms"])


def _host_listen_allowlist(raw: object, errors: list[str]) -> tuple[HostListen, ...]:
    """기본은 거부. 특정 주소만 받는다(0.0.0.0·:: 같은 전체 주소와 loopback은 허용 대상이 아님)."""
    prefix = "config.host_listen_allowlist"
    if not isinstance(raw, list) or len(raw) > MAX_HOST_LISTEN_ALLOW:
        errors.append(f"{prefix}: INVALID")
        return ()
    entries, seen = [], set()
    for index, item in enumerate(raw):
        label = f"{prefix}[{index}]"
        fields = _exact(item, ("address", "port", "reason"), label, errors)
        if len(fields) != 3 or set(fields) != {"address", "port", "reason"}:
            continue
        address, port, reason = fields["address"], fields["port"], fields["reason"]
        try:
            parsed = ipaddress.ip_address(address) if isinstance(address, str) else None
        except ValueError:
            parsed = None
        if (parsed is None or str(parsed) != address or parsed.is_loopback or parsed.is_unspecified
                or getattr(parsed, "ipv4_mapped", None) is not None):
            errors.append(f"{label}.address: INVALID")
            continue
        if not _int(port, 1, 65535):
            errors.append(f"{label}.port: INVALID")
            continue
        if (not isinstance(reason, str) or not reason.strip() or len(reason) > MAX_REASON_LENGTH
                or any(ord(char) < 32 or ord(char) == 127 for char in reason)):
            errors.append(f"{label}.reason: INVALID")
            continue
        if (address, port) in seen:
            errors.append(f"{label}: DUPLICATE")
            continue
        seen.add((address, port))
        entries.append(HostListen(address, port, reason))
    return tuple(entries)


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result = {}
    for key, value in pairs:
        if key in result:
            raise RunnerConfigError(["config: DUPLICATE_KEY"])
        result[key] = value
    return result


def _reject_constant(_: str) -> None:
    raise RunnerConfigError(["config: NONFINITE_NUMBER"])


def load_runner_config(path: str | Path) -> RunnerConfig:
    try:
        with open(path, "rb") as source:
            raw = source.read(MAX_CONFIG_BYTES + 1)
    except (OSError, ValueError):
        raise RunnerConfigError(["config: READ_FAILED"]) from None
    if len(raw) > MAX_CONFIG_BYTES:
        raise RunnerConfigError(["config: TOO_LARGE"])
    try:
        payload = json.loads(raw.decode("utf-8"), object_pairs_hook=_unique_object, parse_constant=_reject_constant)
    except RunnerConfigError:
        raise
    except (ValueError, UnicodeError, RecursionError):
        raise RunnerConfigError(["config: INVALID_JSON"]) from None
    return parse_runner_config(payload)


def new_run_id(side: str, now: datetime | None = None) -> str:
    """compose project 이름 규칙(소문자·숫자·-)을 지키고 회차마다 다르다."""
    if side not in SIDES:
        raise RunnerConfigError(["side: UNSUPPORTED"])
    moment = (now or datetime.now(timezone.utc)).astimezone(timezone.utc)
    return f"{side}-{moment:%Y%m%dt%H%M%Sz}-{secrets.token_hex(4)}"


def plan_run(config: RunnerConfig, preflight: object, side: str, run_id: str) -> RunPlan:
    """사전검사 선언과 러너 설정을 합쳐 회차 계획을 만든다. compose 경로·uid는 run 시점에 정한다."""
    if side not in SIDES:
        raise RunnerConfigError(["side: UNSUPPORTED"])
    try:
        check_preflight(preflight)
    except PreflightError as exc:
        raise RunnerConfigError(["preflight: " + error for error in exc.errors]) from None
    declared = preflight[side]
    llm = dict(declared["llm"])
    origin = urlsplit(declared["target_origin"])
    if origin.hostname != METER_SERVICE:
        raise RunnerConfigError([f"preflight.{side}.target_origin: NOT_EVAL_METER"])
    engine_image = config.images[f"engine_{side}"]
    target_image = Image(declared["target_image"], config.target_image_id)
    files = [COMPOSE_COMMON, COMPOSE_EVAL]
    env = {
        "EVAL_ENGINE_IMAGE": engine_image.ref,
        "EVAL_ENGINE_DB_IMAGE": config.images["engine_db"].ref,
        "EVAL_TARGET_IMAGE": target_image.ref,
        "EVAL_METER_IMAGE": config.images["meter"].ref,
        "EVAL_ENGINE_API_PORT": str(config.ports["engine_api_host"]),
        "EVAL_ENGINE_CONTAINER_PORT": str(config.ports["engine_container"]),
        "EVAL_ENGINE_DB_USER": config.engine_db_user,
        "EVAL_ENGINE_DB_NAME": config.engine_db_name,
        "EVAL_HEALTH_INTERVAL": config.health_interval,
        "EVAL_HEALTH_TIMEOUT": config.health_timeout,
        "EVAL_HEALTH_RETRIES": str(config.health_retries),
        "EVAL_ENGINE_DB_TMPFS_SIZE": config.tmpfs_engine_db,
        "EVAL_ENGINE_DATA_TMPFS_SIZE": config.tmpfs_engine_data,
        "ARTEX_LLM_PROVIDER": config.llm_provider,
        "ARTEX_LLM_MODEL": llm["model"],
        "ARTEX_LLM_BASE_URL": llm["base_url"],
        "ARTEX_LLM_STREAM": "true" if config.llm_stream else "false",
        "EVAL_TARGET_PORT": str(config.ports["target"]),
        "EVAL_METER_PORT": str(origin.port),
        "EVAL_OBS_PORT": str(config.ports["obs"]),
        "EVAL_OBS_HOST_PORT": str(config.ports["obs_host"]),
        "EVAL_OBS_PATH": config.observer_path,
    }
    if llm["backend"] == "local":
        files.insert(1, COMPOSE_LOCAL_LLM)
        env["EVAL_LLM_HOSTNAME"] = llm["hostname"]
        env["EVAL_LLM_HOST_IP"] = llm["host_ip"]
    images = {"engine": engine_image, "engine_db": config.images["engine_db"],
              "target": target_image, "meter": config.images["meter"]}
    return RunPlan(side=side, run_id=run_id, project=f"eval-{run_id}", source_sha=declared["source_sha"],
                   llm=llm, target_image=target_image, engine_image=engine_image, images=images,
                   meter_port=origin.port, compose_files=tuple(files),
                   secrets_env_file=config.secrets_env_file, compose_env=env)


def _open_private_dir(path: Path) -> int:
    """러너 uid 소유·정확히 0700·심볼릭 링크 아님을 연 fd 기준으로 확인한다."""
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    info = os.fstat(fd)
    if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
        os.close(fd)
        raise RunnerConfigError(["runs_root: NOT_PRIVATE"])
    return fd


def _make_private_child(parent_fd: int, name: str) -> int:
    os.mkdir(name, 0o700, dir_fd=parent_fd)
    fd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent_fd)
    # umask와 무관하게 정확히 0700. 계측기는 0700이 아니면 시작하지 않는다(fail-closed).
    os.fchmod(fd, 0o700)
    return fd


def create_run_directory(runs_root: Path, run_id: str) -> RunDirectory:
    """회차 디렉터리와 계측기 bind 디렉터리를 러너 uid·0700으로 먼저 만든다.

    Docker가 없는 bind 경로를 자동 생성하면 root·0755가 되어 계측기가 거부하므로, 반드시 up 전에 만든다.
    계측기에는 meter/만 마운트하고 pre/post 스냅샷·근거는 회차 디렉터리 쪽에 둔다.
    """
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,62}", run_id):
        raise RunnerConfigError(["run_id: INVALID"])
    try:
        root_fd = _open_private_dir(runs_root)
    except RunnerConfigError:
        raise
    except OSError:
        raise RunnerConfigError(["runs_root: UNAVAILABLE"]) from None
    try:
        run_fd = _make_private_child(root_fd, run_id)
        try:
            os.close(_make_private_child(run_fd, METER_DIR_NAME))
        finally:
            os.close(run_fd)
    except FileExistsError:
        raise RunnerConfigError(["run_directory: EXISTS"]) from None
    except OSError:
        raise RunnerConfigError(["run_directory: CREATE_FAILED"]) from None
    finally:
        os.close(root_fd)
    run = runs_root / run_id
    return RunDirectory(run=run, meter=run / METER_DIR_NAME)


def compose_environment(plan: RunPlan, directory: RunDirectory) -> dict[str, str]:
    env = dict(plan.compose_env)
    env["EVAL_EVIDENCE_DIR"] = str(directory.meter)
    env["EVAL_RUNNER_UID"] = str(os.geteuid())
    env["EVAL_RUNNER_GID"] = str(os.getegid())
    return env


def compose_base_args(plan: RunPlan) -> list[str]:
    args = ["compose", "-p", plan.project, "--env-file", str(plan.secrets_env_file)]
    for path in plan.compose_files:
        args += ["-f", str(path)]
    return args
