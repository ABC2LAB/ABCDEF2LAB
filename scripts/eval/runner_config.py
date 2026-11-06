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

from baseline_extract import MAX_INPUT_BYTES
from baseline_pages_collector import MAX_PAGE_SIZE
from preflight import MAX_CONFIG_BYTES, PreflightError, _image_reference, check_preflight
from runner_docker import container_file_ok

CONFIG_VERSION = 1
SIDES = ("baseline", "ours")
# 엔진 LLM 정책 스위치(ARTEX_LLM_POLICY_MODE, #125)가 있는 쪽. baseline(e53a79f)에는 없다.
POLICY_SWITCH_SIDE = "ours"
BASELINE_SIDE = "baseline"
REPO_ROOT = Path(__file__).resolve().parents[2]
COMPOSE_COMMON = REPO_ROOT / "deploy" / "engine-isolated" / "compose.yaml"
COMPOSE_LOCAL_LLM = REPO_ROOT / "deploy" / "engine-isolated" / "compose.local-llm.yaml"
COMPOSE_LLM_API = REPO_ROOT / "deploy" / "engine-isolated" / "compose.llm-api.yaml"
COMPOSE_EVAL = REPO_ROOT / "deploy" / "eval" / "compose.eval.yaml"
# 개선 후 회차의 E② 자리에 통과 전용 가짜 E²를 둔다(e2.ours_mode=stub일 때만, compose.eval.yaml 다음).
COMPOSE_E2_STUB = REPO_ROOT / "deploy" / "eval" / "compose.e2-stub.yaml"
# 제품 E² 자리. 러너 연결 전이라 아직 어떤 회차에도 붙지 않는다.
COMPOSE_E2_PRODUCT = REPO_ROOT / "deploy" / "eval" / "compose.e2-product.yaml"
METER_DIR_NAME = "meter"
# 게이트웨이 기록은 계측기와 따로 둔다(API 키를 가진 컨테이너가 계측 기록에 손대지 못하게).
LLM_DIR_NAME = "llm"
# 가짜 E²(e2.ours_mode=stub) 결정 행 디렉터리. 계측기 증거와 분리해 E²가 독립 계측 로그·seal 파일에 쓸 수 없게 한다.
E2_DIR_NAME = "e2"
# 상태 차이 사본(대상 SQLite) 디렉터리. 사본은 지문을 뜬 즉시 지운다(값이 든 파일을 남기지 않음).
STATE_DIR_NAME = "state"
# 상태 차이(프로토콜 §8.2) 설정. null이면 사본을 뜨지 않는다. 앱별 값(DB 경로·해결 신호 테이블)은 여기서만 받는다.
_STATE_FIELDS = ("sqlite_path", "signal_tables", "max_copy_bytes", "runtime_roots", "memory")
MAX_RUNTIME_ROOTS = 16
# 메모리 저장 읽기(null이면 안 읽음). 계측기 state-read 역할의 계획 한도와 같다(cmd/eval-meter/stateread.go).
_MEMORY_FIELDS = ("paths", "count_field", "max_body_bytes", "timeout_ms")
MAX_MEMORY_PATHS = 256
MAX_MEMORY_BODY_BYTES = 16 << 20
MAX_MEMORY_PLAN_BYTES = 64 << 10
MAX_MEMORY_TIMEOUT_MS = 600_000
MAX_STATE_COPY_BYTES = 1 << 30
MAX_SIGNAL_TABLES = 32
_TABLE_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]{0,63}\Z")
# compose가 비밀 env 파일에서 직접 읽는 이름. 러너는 이 값을 만들지도 읽지도 않는다.
# compose 파일의 나머지 ${VAR}는 전부 러너가 넘긴다(테스트로 집합 일치를 고정).
_BASE_SECRETS = frozenset({"EVAL_ENGINE_ADMIN_PASSWORD", "EVAL_ENGINE_DB_PASSWORD"})
_API_SECRETS = frozenset({"EVAL_LLM_API_KEY"})
SECRET_ENV_NAMES = _BASE_SECRETS | _API_SECRETS
# compose 서비스·망 키(deploy/ 파일과 같은 이름, 테스트로 고정). 엔진은 계측기 이름으로만 대상에 닿는다.
ENGINE_SERVICE = "engine"
ENGINE_DB_SERVICE = "engine-db"
TARGET_SERVICE = "target"
METER_SERVICE = "eval-meter"
OBS_SERVICE = "eval-obs"
LLM_GW_SERVICE = "eval-llm-gw"
API_FRONT_SERVICE = "eval-api"
# 가짜 E²(compose.e2-stub.yaml). engine_net에서 계측기 이름(METER_SERVICE)을 별칭으로 받는다.
E2_SERVICE = "eval-e2"
# 가짜 E²가 계측기에 닿는 별칭. eval-e2 자신이 METER_SERVICE 별칭을 가지므로 다른 이름이어야 한다.
METER_UPSTREAM_ALIAS = "eval-meter-upstream"
# 회차 메모리(tmpfs) 마운트 경로(deploy/eval/compose.eval.yaml과 같은 값, 테스트로 고정).
ENGINE_DB_DATA_PATH = "/var/lib/postgresql/data"
ENGINE_DATA_PATH = "/app/data"
ENGINE_NET = "engine_net"
TARGET_NET = "target_net"
LLM_EGRESS_NET = "llm_egress_net"
ENGINE_API_NET = "engine_api_net"
OBS_NET = "obs_net"
E2_NET = "e2_net"
# 엔진의 OpenAI 형식 클라이언트가 base URL 뒤에 붙이는 경로(agent/provider.go). 게이트웨이는 이 경로만 받는다.
CHAT_COMPLETIONS_SUFFIX = "/chat/completions"
MAX_VERIFY_ATTEMPTS = 100
MAX_VERIFY_INTERVAL_SECONDS = 600
MAX_HOST_LISTEN_ALLOW = 32
MAX_REASON_LENGTH = 200
# 점검 하나의 연결·해석 제한. 실패 기대 경로는 대부분 timeout으로 끝나므로 회차 준비 시간을 키운다.
MAX_PROBE_TIMEOUT_MS = 60_000

_IMAGE_ID = re.compile(r"sha256:[0-9a-f]{64}\Z")
_TMPFS_SIZE = re.compile(r"[1-9][0-9]{0,8}[kmg]?\Z")
_DURATION = re.compile(r"[1-9][0-9]{0,5}(?:ms|s|m)\Z")
# 이벤트 시각(데몬)과 러너 시각은 같은 호스트 시계지만 기록 시점이 조금 다르다.
LOOKBACK_MARGIN_SECONDS = 1.0
_DURATION_UNIT = {"ms": 0.001, "s": 1.0, "m": 60.0}
# compose tmpfs size 접미사(docker의 RAM 단위, 1024배).
_SIZE_UNIT = {"k": 1024, "m": 1024 ** 2, "g": 1024 ** 3}


def size_bytes(text: str) -> int:
    """compose tmpfs 크기 문자열(_TMPFS_SIZE 형식)을 바이트로 바꾼다."""
    if not isinstance(text, str) or not _TMPFS_SIZE.fullmatch(text):
        raise RunnerConfigError(["config.tmpfs: INVALID_SIZE"])
    if text[-1] in _SIZE_UNIT:
        return int(text[:-1]) * _SIZE_UNIT[text[-1]]
    return int(text)


def duration_seconds(text: str) -> float:
    """compose healthcheck 시간 문자열(_DURATION 형식)을 초로 바꾼다."""
    if not isinstance(text, str) or not _DURATION.fullmatch(text):
        raise RunnerConfigError(["config.health: INVALID_DURATION"])
    unit = "ms" if text.endswith("ms") else text[-1]
    return int(text[: -len(unit)]) * _DURATION_UNIT[unit]
_DB_NAME = re.compile(r"[a-z_][a-z0-9_]{0,62}\Z")
# 게이트웨이가 chat completions 경로만 받으므로 엔진은 OpenAI 호환 형식만 쓴다(OpenRouter·Ollama 둘 다 지원).
_LLM_PROVIDERS = ("openai",)
# 엔진 LLM 정책 모드(#125, ARTEX_LLM_POLICY_MODE). 엔진 기본값은 local-only라 운영자 pin manifest와 그에 맞는 DB
# profile이 있어야 뜬다. 평가 회차는 LLM을 게이트웨이로만 내보내므로(망 격리) external만 받는다. local-only는 §6
# 외부 송신 차단 음성 시험용이며 러너가 manifest·profile을 준비하지 않는다. baseline(e53a79f)은 이 스위치가 없다.
_LLM_POLICY_MODES = ("external",)
# 개선 후 회차의 E② 자리. none = E² 없음(엔진 → 계측기), stub = 통과 전용 가짜 E²(fixture, 공식 결과 아님).
# 제품 E²가 들어오면 값이 하나 늘어난다. baseline은 항상 none이다(원본에는 E²가 없다).
E2_NONE, E2_STUB = "none", "stub"
E2_MODES = (E2_NONE, E2_STUB)
# 관측 경로 응답 형식. opaque는 점수판 없는 연습 대상용이고 그 회차는 채점기가 거부한다(docs/evaluation/practice-targets.md).
OBSERVER_SCOREBOARD = "challenge_scoreboard"
OBSERVER_OPAQUE = "opaque"
OBSERVER_FORMATS = (OBSERVER_SCOREBOARD, OBSERVER_OPAQUE)
_IMAGE_ROLES = ("engine_baseline", "engine_ours", "engine_db", "meter")
_PORTS = ("engine_api_host", "engine_container", "target", "obs", "obs_host", "llm_gateway", "api_front")
# expected_provider_name: generation 조회가 돌려주는 응답 제공자 표시 이름(고정 slug와 짝, 팀이 정함).
_GATEWAY_FIELDS = ("generation_path", "verify_attempts", "verify_interval_seconds", "ca_bundle", "expected_provider_name")
MAX_PROVIDER_NAME_LENGTH = 100
_TOP_FIELDS = ("version", "runs_root", "secrets_env_file", "images", "target_image_id", "ports",
               "observer_path", "observer_format", "engine", "engine_db", "tmpfs", "health", "docker_timeout_seconds",
               "host_listen_allowlist", "probe_timeout_ms", "runner_key_path", "run", "llm_gateway", "findings", "task",
               "observer_readonly_ref", "e2", "state", "engine_rules")
# findings 수집(기존 원본 수집기) 한도. vocabulary_path는 평가자가 검토한 분류 매핑 JSON(레포 밖).
_FINDINGS_FIELDS = ("vocabulary_path", "page_size", "timeout_seconds", "max_page_bytes", "max_total_bytes", "max_pages")
# 페이지 크기·바이트 상한은 수집기·추출기 한도를 그대로 쓴다(따로 두면 어긋난다).
MAX_FINDINGS_PAGES = 10_000
# 엔진 작업 생성 본문(POST /api/tasks의 createTaskReq 중 평가가 정하는 필드). 프로필·출처 작업·회사·분류·
# 가로채기 규칙은 받지 않는다(새 DB라 없고, LLM은 엔진 환경 설정 = 게이트웨이를 쓴다).
_TASK_FIELDS = ("description", "goal", "timeout_seconds", "plan_heartbeat_seconds", "seed_first_intent",
                "coverage_enabled")
MAX_TASK_TEXT_LENGTH = 4000
MAX_REFERENCE_LENGTH = 200
MAX_TASK_SECONDS = 24 * 3600
# 회차 진행 시간·크기 값(초). 실패 시 재시도하지 않으므로 각 값이 그 단계의 유일한 기회다.
_RUN_FIELDS = ("compose_wait_seconds", "down_timeout_seconds", "ready_timeout_seconds", "budget_poll_seconds",
               "drain_timeout_seconds", "snapshot_timeout_seconds", "snapshot_max_bytes", "llm_verify_timeout_seconds",
               "llm_load_timeout_seconds",
               "task_create_timeout_seconds", "host_watch_interval_seconds", "host_watch_max_gap_seconds",
               "drain_grace_seconds", "post_settle_seconds")
MAX_RUN_SECONDS = 3600
# 계측기(cmd/eval-meter maxDrainGrace)가 받는 봉인 후 유예 상한과 같다(테스트로 고정).
MAX_DRAIN_GRACE_SECONDS = 600
# 게이트웨이(cmd/eval-meter sampling.go maxTemperature)가 받는 temperature 상한. 프로파일은 0 이상이면 받으므로
# 회차를 만들 때 여기서 먼저 걸러 compose 기동 뒤 게이트웨이 거부로 끝나지 않게 한다(drift 테스트).
MAX_GATEWAY_TEMPERATURE = 2
MAX_SNAPSHOT_BYTES = 16 * 1024 * 1024


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
    observer_format: str
    llm_provider: str
    llm_stream: bool
    llm_policy_mode: str
    engine_db_user: str
    engine_db_name: str
    tmpfs_engine_db: str
    tmpfs_engine_data: str
    # 구간 종료 뒤 tmpfs에 이만큼도 남지 않았으면 상한에 닿은 것으로 보고 회차를 무효로 한다.
    tmpfs_min_free: str
    health_interval: str
    health_timeout: str
    health_retries: int
    docker_timeout_seconds: float
    host_listen_allowlist: tuple[HostListen, ...]
    probe_timeout_ms: int
    runner_key_path: Path
    run: dict[str, int]
    llm_gateway: dict
    findings: dict
    task: dict
    observer_readonly_ref: str
    # 개선 후 회차의 E② 자리(E2_MODES). baseline 회차에는 쓰지 않는다.
    e2_ours_mode: str
    # 상태 차이 설정(_STATE_FIELDS) 또는 None(끔). 양쪽 회차에 같게 적용한다.
    state: dict | None
    # 원본 ② 규칙 파일 경로 또는 None(프로토콜 §8.1). 원본 회차에만 쓴다. 조건마다 설정 파일을 따로 둔다.
    engine_rules: str | None

    @property
    def tmpfs_min_free_bytes(self) -> int:
        return size_bytes(self.tmpfs_min_free)

    @property
    def health_timeout_seconds(self) -> float:
        """구간 이벤트 lookback(초): 구간 안에서 끝나는 healthcheck exec는 이 시간 안에 시작됐다(+여유)."""
        return duration_seconds(self.health_timeout) + LOOKBACK_MARGIN_SECONDS


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
    # 이 회차의 E② 자리(E2_MODES). baseline은 항상 E2_NONE.
    e2_mode: str


@dataclass(frozen=True)
class RunDirectory:
    run: Path
    meter: Path
    llm: Path
    e2: Path
    state: Path


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


def _reference(value: object) -> bool:
    return (isinstance(value, str) and 0 < len(value) <= MAX_REFERENCE_LENGTH and value.strip() == value
            and not any(ord(c) < 32 or ord(c) == 127 for c in value))


def _task_text(value: object) -> bool:
    # 여러 줄은 허용하고 그 밖의 제어 문자는 거부한다(엔진 로그·UI에 그대로 찍힌다).
    return (isinstance(value, str) and bool(value.strip()) and len(value) <= MAX_TASK_TEXT_LENGTH
            and not any((ord(c) < 32 and c != "\n") or ord(c) == 127 for c in value))


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
    if "observer_format" in top and top["observer_format"] not in OBSERVER_FORMATS:
        errors.append("config.observer_format: INVALID")
    engine = (_exact(top.get("engine"), ("llm_provider", "llm_stream", "llm_policy_mode"), "config.engine", errors)
              if "engine" in top else {})
    if "llm_provider" in engine and engine["llm_provider"] not in _LLM_PROVIDERS:
        errors.append("config.engine.llm_provider: UNSUPPORTED")
    if "llm_stream" in engine and type(engine["llm_stream"]) is not bool:
        errors.append("config.engine.llm_stream: EXPECTED_BOOL")
    if "llm_policy_mode" in engine and engine["llm_policy_mode"] not in _LLM_POLICY_MODES:
        errors.append("config.engine.llm_policy_mode: UNSUPPORTED")
    e2 = _exact(top.get("e2"), ("ours_mode",), "config.e2", errors) if "e2" in top else {}
    if "engine_rules" in top and top["engine_rules"] is not None and not _absolute_path(top["engine_rules"]):
        errors.append("config.engine_rules: EXPECTED_ABSOLUTE_PATH")
    if "ours_mode" in e2 and e2["ours_mode"] not in E2_MODES:
        errors.append("config.e2.ours_mode: UNSUPPORTED")
    state = _state(top.get("state"), errors) if "state" in top else None
    db = _exact(top.get("engine_db"), ("user", "name"), "config.engine_db", errors) if "engine_db" in top else {}
    for name in ("user", "name"):
        if name in db and not (isinstance(db[name], str) and _DB_NAME.fullmatch(db[name])):
            errors.append(f"config.engine_db.{name}: INVALID")
    tmpfs_keys = ("engine_db", "engine_data", "min_free")
    tmpfs = _exact(top.get("tmpfs"), tmpfs_keys, "config.tmpfs", errors) if "tmpfs" in top else {}
    for name in tmpfs_keys:
        if name in tmpfs and not (isinstance(tmpfs[name], str) and _TMPFS_SIZE.fullmatch(tmpfs[name])):
            errors.append(f"config.tmpfs.{name}: INVALID")
    sizes = [tmpfs.get(name) for name in tmpfs_keys]
    if all(isinstance(v, str) and _TMPFS_SIZE.fullmatch(v) for v in sizes) and \
            size_bytes(tmpfs["min_free"]) >= min(size_bytes(tmpfs["engine_db"]), size_bytes(tmpfs["engine_data"])):
        errors.append("config.tmpfs.min_free: NOT_BELOW_SIZE")  # 이러면 모든 회차가 상한 도달로 무효가 된다
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
    if "runner_key_path" in top:
        key_path = top["runner_key_path"]
        if not _absolute_path(key_path):
            errors.append("config.runner_key_path: EXPECTED_ABSOLUTE_PATH")
        elif _absolute_path(top.get("runs_root")) and (key_path == top["runs_root"]
                                                       or key_path.startswith(top["runs_root"].rstrip("/") + "/")):
            # 서명 키는 회차 디렉터리 밖에 둔다. 회차 자료와 함께 복사·공유되지 않게.
            errors.append("config.runner_key_path: INSIDE_RUNS_ROOT")
    gateway = _exact(top.get("llm_gateway"), _GATEWAY_FIELDS, "config.llm_gateway", errors) if "llm_gateway" in top else {}
    if "generation_path" in gateway and not _observer_path(gateway["generation_path"]):
        errors.append("config.llm_gateway.generation_path: INVALID")
    if "verify_attempts" in gateway and not _int(gateway["verify_attempts"], 1, MAX_VERIFY_ATTEMPTS):
        errors.append("config.llm_gateway.verify_attempts: INVALID")
    if "verify_interval_seconds" in gateway and not _int(gateway["verify_interval_seconds"], 1,
                                                          MAX_VERIFY_INTERVAL_SECONDS):
        errors.append("config.llm_gateway.verify_interval_seconds: INVALID")
    if "ca_bundle" in gateway and not _absolute_path(gateway["ca_bundle"]):
        errors.append("config.llm_gateway.ca_bundle: EXPECTED_ABSOLUTE_PATH")
    name = gateway.get("expected_provider_name")
    if "expected_provider_name" in gateway and (not isinstance(name, str) or not name.strip()
                                                or len(name) > MAX_PROVIDER_NAME_LENGTH
                                                or any(ord(c) < 32 or ord(c) == 127 for c in name)):
        errors.append("config.llm_gateway.expected_provider_name: INVALID")
    findings = _exact(top.get("findings"), _FINDINGS_FIELDS, "config.findings", errors) if "findings" in top else {}
    if "vocabulary_path" in findings and not _absolute_path(findings["vocabulary_path"]):
        errors.append("config.findings.vocabulary_path: EXPECTED_ABSOLUTE_PATH")
    for name, high in (("page_size", MAX_PAGE_SIZE), ("timeout_seconds", MAX_RUN_SECONDS),
                       ("max_page_bytes", MAX_INPUT_BYTES), ("max_total_bytes", MAX_INPUT_BYTES),
                       ("max_pages", MAX_FINDINGS_PAGES)):
        if name in findings and not _int(findings[name], 1, high):
            errors.append(f"config.findings.{name}: INVALID")
    # 등록 관측 GET이 그 대상 버전에서 상태를 바꾸지 않음을 확인한 별도 기록의 참조(v1.1 §3 observer). 러너가 만들 수 없는 근거다.
    if "observer_readonly_ref" in top and not _reference(top["observer_readonly_ref"]):
        errors.append("config.observer_readonly_ref: INVALID")
    task = _exact(top.get("task"), _TASK_FIELDS, "config.task", errors) if "task" in top else {}
    for name in ("description", "goal"):
        if name in task and not _task_text(task[name]):
            errors.append(f"config.task.{name}: INVALID")
    for name in ("timeout_seconds", "plan_heartbeat_seconds"):
        if name in task and not _int(task[name], 0, MAX_TASK_SECONDS):
            errors.append(f"config.task.{name}: INVALID")
    for name in ("seed_first_intent", "coverage_enabled"):
        if name in task and type(task[name]) is not bool:
            errors.append(f"config.task.{name}: EXPECTED_BOOL")
    run = _exact(top.get("run"), _RUN_FIELDS, "config.run", errors) if "run" in top else {}
    for name in _RUN_FIELDS:
        limit = {"snapshot_max_bytes": MAX_SNAPSHOT_BYTES, "drain_grace_seconds": MAX_DRAIN_GRACE_SECONDS}.get(
            name, MAX_RUN_SECONDS)
        if name in run and not _int(run[name], 1, limit):
            errors.append(f"config.run.{name}: INVALID")
    # 공백 상한이 표본 간격 이하이면 정상 회차도 항상 공백 초과가 된다.
    if (_int(run.get("host_watch_interval_seconds"), 1, MAX_RUN_SECONDS)
            and _int(run.get("host_watch_max_gap_seconds"), 1, MAX_RUN_SECONDS)
            and run["host_watch_max_gap_seconds"] <= run["host_watch_interval_seconds"]):
        errors.append("config.run.host_watch_max_gap_seconds: NOT_ABOVE_INTERVAL")
    # 유예가 drain 시한 이상이면 계측기가 강제 종료하기 전에 러너가 먼저 drain_timeout으로 끝낸다.
    if (_int(run.get("drain_grace_seconds"), 1, MAX_DRAIN_GRACE_SECONDS)
            and _int(run.get("drain_timeout_seconds"), 1, MAX_RUN_SECONDS)
            and run["drain_grace_seconds"] >= run["drain_timeout_seconds"]):
        errors.append("config.run.drain_grace_seconds: NOT_BELOW_DRAIN_TIMEOUT")
    if errors:
        raise RunnerConfigError(errors)
    return RunnerConfig(
        runs_root=Path(top["runs_root"]), secrets_env_file=Path(top["secrets_env_file"]), images=images,
        target_image_id=top["target_image_id"], ports=dict(ports), observer_path=top["observer_path"],
        observer_format=top["observer_format"],
        llm_provider=engine["llm_provider"], llm_stream=engine["llm_stream"], llm_policy_mode=engine["llm_policy_mode"],
        engine_db_user=db["user"], engine_db_name=db["name"],
        tmpfs_engine_db=tmpfs["engine_db"], tmpfs_engine_data=tmpfs["engine_data"], tmpfs_min_free=tmpfs["min_free"],
        health_interval=health["interval"], health_timeout=health["timeout"], health_retries=health["retries"],
        docker_timeout_seconds=float(timeout), host_listen_allowlist=allowlist,
        probe_timeout_ms=top["probe_timeout_ms"], runner_key_path=Path(top["runner_key_path"]),
        run={name: run[name] for name in _RUN_FIELDS},
        llm_gateway={name: gateway[name] for name in _GATEWAY_FIELDS},
        findings={name: findings[name] for name in _FINDINGS_FIELDS},
        task={name: task[name] for name in _TASK_FIELDS},
        observer_readonly_ref=top["observer_readonly_ref"], e2_ours_mode=e2["ours_mode"], state=state,
        engine_rules=top["engine_rules"])


def _state(raw: object, errors: list[str]) -> dict | None:
    if raw is None:
        return None
    state = _exact(raw, _STATE_FIELDS, "config.state", errors)
    if "sqlite_path" in state and not container_file_ok(state["sqlite_path"]):
        errors.append("config.state.sqlite_path: INVALID")
    tables = state.get("signal_tables")
    if "signal_tables" in state and not (
            isinstance(tables, list) and len(tables) <= MAX_SIGNAL_TABLES and len(set(map(str, tables))) == len(tables)
            and all(isinstance(t, str) and _TABLE_NAME.fullmatch(t) for t in tables)):
        errors.append("config.state.signal_tables: INVALID")
    if "max_copy_bytes" in state and not _int(state["max_copy_bytes"], 1, MAX_STATE_COPY_BYTES):
        errors.append("config.state.max_copy_bytes: INVALID")
    roots = state.get("runtime_roots")
    # 겹치는 디렉터리는 같은 파일을 두 번 센다.
    if "runtime_roots" in state and not (
            isinstance(roots, list) and len(roots) <= MAX_RUNTIME_ROOTS and all(container_file_ok(r) for r in roots)
            and len(set(roots)) == len(roots)
            and not any(a != b and b.startswith(a + "/") for a in roots for b in roots)):
        errors.append("config.state.runtime_roots: INVALID")
    memory = _state_memory(state.get("memory"), errors) if "memory" in state else None
    if set(state) != set(_STATE_FIELDS) or any(e.startswith("config.state") for e in errors):
        return None
    return {"sqlite_path": state["sqlite_path"], "signal_tables": tuple(tables),
            "max_copy_bytes": state["max_copy_bytes"], "runtime_roots": tuple(roots), "memory": memory}


def _state_memory(raw: object, errors: list[str]) -> dict | None:
    if raw is None:
        return None
    memory = _exact(raw, _MEMORY_FIELDS, "config.state.memory", errors)
    paths = memory.get("paths")
    if "paths" in memory and not (
            isinstance(paths, list) and 0 < len(paths) <= MAX_MEMORY_PATHS and all(_observer_path(p) for p in paths)
            and len(set(paths)) == len(paths) and len(json.dumps(paths)) <= MAX_MEMORY_PLAN_BYTES // 2):
        errors.append("config.state.memory.paths: INVALID")
    field = memory.get("count_field")
    if "count_field" in memory and not (isinstance(field, str) and _TABLE_NAME.fullmatch(field)):
        errors.append("config.state.memory.count_field: INVALID")
    if "max_body_bytes" in memory and not _int(memory["max_body_bytes"], 1, MAX_MEMORY_BODY_BYTES):
        errors.append("config.state.memory.max_body_bytes: INVALID")
    if "timeout_ms" in memory and not _int(memory["timeout_ms"], 1, MAX_MEMORY_TIMEOUT_MS):
        errors.append("config.state.memory.timeout_ms: INVALID")
    if set(memory) != set(_MEMORY_FIELDS) or any(e.startswith("config.state.memory") for e in errors):
        return None
    return {"paths": tuple(paths), "count_field": field, "max_body_bytes": memory["max_body_bytes"],
            "timeout_ms": memory["timeout_ms"]}


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
    # 원본 ②의 규칙은 원본 자체 기능이다. 개선 후의 통제는 E② 자리로만 다룬다(프로토콜 §8.1).
    if config.engine_rules is not None and side != BASELINE_SIDE:
        raise RunnerConfigError(["config.engine_rules: BASELINE_ONLY"])
    declared = preflight[side]
    llm = dict(declared["llm"])
    origin = urlsplit(declared["target_origin"])
    if origin.hostname != METER_SERVICE:
        raise RunnerConfigError([f"preflight.{side}.target_origin: NOT_EVAL_METER"])
    # tmpfs 크기·하한은 비교 조건이다(환경 지문에 들어감). 러너 설정이 선언과 한 글자라도 다르면 시작하지 않는다.
    storage = {"engine_db_tmpfs": config.tmpfs_engine_db, "engine_data_tmpfs": config.tmpfs_engine_data,
               "tmpfs_min_free": config.tmpfs_min_free}
    if declared["run_storage"] != storage:
        raise RunnerConfigError([f"preflight.{side}.run_storage: CONFIG_MISMATCH"])
    # 봉인 뒤 유예·정착 대기도 비교 조건이다(#153 리뷰). 선언과 다른 값으로 돌면 같은 지문으로 다른 회차가 된다.
    drain = {"grace_seconds": config.run["drain_grace_seconds"], "settle_seconds": config.run["post_settle_seconds"]}
    if declared["run_drain"] != drain:
        raise RunnerConfigError([f"preflight.{side}.run_drain: CONFIG_MISMATCH"])
    engine_image = config.images[f"engine_{side}"]
    target_image = Image(declared["target_image"], config.target_image_id)
    # 선언의 base_url은 비교 조건(어느 LLM 엔드포인트인가)이고, 엔진은 그 대신 게이트웨이에 닿는다.
    # 게이트웨이 주소는 평가 구성의 내부 사항이라 선언·지문에 넣지 않고 여기서 만든다.
    upstream = urlsplit(llm["base_url"])
    base_path = upstream.path.rstrip("/")
    gateway_port = config.ports["llm_gateway"]
    files = [COMPOSE_COMMON, COMPOSE_LLM_API if llm["backend"] == "api" else COMPOSE_LOCAL_LLM, COMPOSE_EVAL]
    e2_mode = config.e2_ours_mode if side == POLICY_SWITCH_SIDE else E2_NONE
    if e2_mode == E2_STUB:
        files.append(COMPOSE_E2_STUB)
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
        "ARTEX_LLM_BASE_URL": f"http://{LLM_GW_SERVICE}:{gateway_port}{base_path}",
        "ARTEX_LLM_STREAM": "true" if config.llm_stream else "false",
        "EVAL_ENGINE_LLM_POLICY_MODE": config.llm_policy_mode,
        "EVAL_TARGET_PORT": str(config.ports["target"]),
        "EVAL_METER_PORT": str(origin.port),
        "EVAL_METER_DRAIN_GRACE": f"{config.run['drain_grace_seconds']}s",
        "EVAL_OBS_PORT": str(config.ports["obs"]),
        "EVAL_OBS_HOST_PORT": str(config.ports["obs_host"]),
        "EVAL_OBS_PATH": config.observer_path,
        "EVAL_API_FRONT_PORT": str(config.ports["api_front"]),
        "EVAL_LLM_GW_PORT": str(gateway_port),
        "EVAL_LLM_UPSTREAM": f"{upstream.scheme}://{upstream.netloc}",
        "EVAL_LLM_CHAT_PATH": base_path + CHAT_COMPLETIONS_SUFFIX,
        "EVAL_LLM_SAMPLING": gateway_sampling(declared, side),
    }
    if llm["backend"] == "api":
        env["EVAL_LLM_PROVIDER"] = llm["provider"]
        env["EVAL_LLM_GENERATION_PATH"] = config.llm_gateway["generation_path"]
        env["EVAL_LLM_VERIFY_ATTEMPTS"] = str(config.llm_gateway["verify_attempts"])
        env["EVAL_LLM_VERIFY_INTERVAL"] = f"{config.llm_gateway['verify_interval_seconds']}s"
        env["EVAL_CA_BUNDLE"] = config.llm_gateway["ca_bundle"]
    else:
        env["EVAL_LLM_HOSTNAME"] = llm["hostname"]
        env["EVAL_LLM_HOST_IP"] = llm["host_ip"]
    images = {"engine": engine_image, "engine_db": config.images["engine_db"],
              "target": target_image, "meter": config.images["meter"]}
    return RunPlan(side=side, run_id=run_id, project=f"eval-{run_id}", source_sha=declared["source_sha"],
                   llm=llm, target_image=target_image, engine_image=engine_image, images=images,
                   meter_port=origin.port, compose_files=tuple(files),
                   secrets_env_file=config.secrets_env_file, compose_env=env, e2_mode=e2_mode)


def gateway_sampling(declared: dict, side: str) -> str:
    """선언의 temperature·sampling·seed를 게이트웨이 -sampling 인자로 만든다. 엔진은 샘플링을 보내지 않는다(§6)."""
    if declared["temperature"] > MAX_GATEWAY_TEMPERATURE:
        raise RunnerConfigError([f"preflight.{side}.temperature: ABOVE_GATEWAY_LIMIT"])
    values = {"temperature": declared["temperature"], **declared["sampling"], "seed": declared["seed"]}
    return json.dumps(values, separators=(",", ":"))


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
            os.close(_make_private_child(run_fd, LLM_DIR_NAME))
            os.close(_make_private_child(run_fd, E2_DIR_NAME))
            os.close(_make_private_child(run_fd, STATE_DIR_NAME))
        finally:
            os.close(run_fd)
    except FileExistsError:
        raise RunnerConfigError(["run_directory: EXISTS"]) from None
    except OSError:
        raise RunnerConfigError(["run_directory: CREATE_FAILED"]) from None
    finally:
        os.close(root_fd)
    run = runs_root / run_id
    return RunDirectory(run=run, meter=run / METER_DIR_NAME, llm=run / LLM_DIR_NAME, e2=run / E2_DIR_NAME,
                        state=run / STATE_DIR_NAME)


def compose_environment(plan: RunPlan, directory: RunDirectory) -> dict[str, str]:
    env = dict(plan.compose_env)
    env["EVAL_EVIDENCE_DIR"] = str(directory.meter)
    env["EVAL_LLM_EVIDENCE_DIR"] = str(directory.llm)
    if plan.e2_mode == E2_STUB:
        env["EVAL_E2_EVIDENCE_DIR"] = str(directory.e2)
    env["EVAL_RUNNER_UID"] = str(os.geteuid())
    env["EVAL_RUNNER_GID"] = str(os.getegid())
    return env

def secret_env_names(backend: str) -> frozenset[str]:
    """그 백엔드의 compose가 비밀 env 파일에서 읽는 이름. local에는 API 키가 없다."""
    return _BASE_SECRETS | (_API_SECRETS if backend == "api" else frozenset())


def compose_base_args(plan: RunPlan) -> list[str]:
    args = ["compose", "-p", plan.project, "--env-file", str(plan.secrets_env_file)]
    for path in plan.compose_files:
        args += ["-f", str(path)]
    return args
