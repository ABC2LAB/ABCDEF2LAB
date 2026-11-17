"""탐색 분리 배치(개선 후 O) 회차 설정과 회차 계획(탐색 분리 배치 결정안 §2·§3, 순서 4-7c-2).

러너 설정의 선택 블록 `split`(보존 측정 회차에만, 공식 금지)을 검사하고, 회차마다 compose 파일·프로필·비밀 없는 환경을
만든다. 계측기·대상·관측·봉인 뒤 유예 칸은 B 회차와 같은 러너 설정 값(ports·observer_path·run)을 그대로 쓴다(같은
계측기·대상·예산, 결정안 §1). 대상 이미지와 계측기 본 입구 포트는 호출자가 B 계획과 같은 값을 넘긴다.
비밀(설치 입력·비밀번호·경계 CA·경로 키)은 여기 없다. 회차 디렉터리에 만든 보호 입력의 경로만 실행 때 더한다
(runner_split_inputs.SplitInputs.compose_env).
"""

from __future__ import annotations

import json
import os
import re
import stat
from dataclasses import dataclass
from pathlib import Path

from runner_config import COMPOSE_SPLIT, REPO_ROOT, Image, RunnerConfig

PRODUCT_COMPOSE = (REPO_ROOT / "deploy" / "product" / "compose.yaml", REPO_ROOT / "deploy" / "product" / "compose.acceptance.yaml",
                   REPO_ROOT / "deploy" / "product" / "compose.exploration-acceptance.yaml")
COMPOSE_FILES = (*PRODUCT_COMPOSE, COMPOSE_SPLIT)
PROFILES = ("exploration",)
# 제품 compose가 태그로 부르는 이미지(deploy/product/compose*.yaml). 태그 하나(PRODUCT_TAG)로 셋을 정하고, 실행 전 ID를 대조한다.
IMAGE_REPOSITORIES = {"product": "abc2lab-product", "explore_browser": "abc2lab-explore-browser",
                      "fixture": "abc2lab-product-fixture"}
SPLIT_FIELDS = frozenset({"product_tag", "product_source_sha", "image_ids", "manifest_path", "target_name", "product_port",
                          "dns_control_port", "explore_port", "evidence_max_bytes", "install_valid_hours", "max_runs",
                          "registration_valid_hours", "wide_writes", "profile_id", "task_wait_seconds",
                          "poll_interval_seconds"})
MAX_MANIFEST_BYTES = 64 * 1024
MAX_EVIDENCE_BYTES = 1 << 30
MAX_HOURS = 24 * 30
MAX_WAIT_SECONDS = 3600
_TAG = re.compile(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}\Z")
_SHA = re.compile(r"[0-9a-f]{40}\Z")
_IMAGE_ID = re.compile(r"sha256:[0-9a-f]{64}\Z")
_PROFILE = re.compile(r"[a-z0-9][a-z0-9_-]{0,63}\Z")
_TARGET_NAME = re.compile(r"(?=.{1,253}\Z)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\Z")


class SplitConfigError(ValueError):
    """split 설정을 받을 수 없음. 칸 이름과 고정 사유만 담는다."""

    def __init__(self, errors: list[str]):
        super().__init__("; ".join(errors))
        self.errors = errors


@dataclass(frozen=True)
class SplitConfig:
    product_tag: str
    product_source_sha: str
    images: dict[str, Image]
    manifest_path: Path
    target_name: str
    product_port: int
    dns_control_port: int
    explore_port: int
    evidence_max_bytes: int
    install_valid_hours: int
    max_runs: int
    registration_valid_hours: int
    wide_writes: bool
    profile_id: str
    task_wait_seconds: int
    poll_interval_seconds: int


def _int(value: object, low: int, high: int) -> bool:
    return type(value) is int and low <= value <= high


def parse_split(raw: object, config: RunnerConfig) -> SplitConfig:
    """`split` 블록 검사. 보존 측정 회차(수용시험 목적·preservation 옵션)에서만 받는다. 오류는 모아서 낸다."""
    errors: list[str] = []
    if config.preservation is None:
        raise SplitConfigError(["config.split: PRESERVATION_ONLY"])
    if not isinstance(raw, dict) or set(raw) != SPLIT_FIELDS:
        raise SplitConfigError(["config.split: FIELDS"])
    if not isinstance(raw["product_tag"], str) or not _TAG.fullmatch(raw["product_tag"]):
        errors.append("config.split.product_tag: INVALID")
    if not isinstance(raw["product_source_sha"], str) or not _SHA.fullmatch(raw["product_source_sha"]):
        errors.append("config.split.product_source_sha: INVALID")
    ids = raw["image_ids"]
    if not isinstance(ids, dict) or set(ids) != set(IMAGE_REPOSITORIES) or not all(
            isinstance(value, str) and _IMAGE_ID.fullmatch(value) for value in ids.values()):
        errors.append("config.split.image_ids: INVALID")
    if not isinstance(raw["manifest_path"], str) or not raw["manifest_path"].startswith("/"):
        errors.append("config.split.manifest_path: INVALID")
    elif Path(raw["manifest_path"]).resolve().is_relative_to(Path(config.runs_root).resolve()):
        errors.append("config.split.manifest_path: INSIDE_RUNS_ROOT")  # 승인 manifest는 근거와 같이 복사되지 않게 밖에 둔다
    if not isinstance(raw["target_name"], str) or not _TARGET_NAME.fullmatch(raw["target_name"]):
        errors.append("config.split.target_name: INVALID")
    ports = [raw[name] for name in ("product_port", "dns_control_port", "explore_port")]
    if not all(_int(port, 1, 65535) for port in ports):
        errors.append("config.split.ports: INVALID")
    elif len(set(ports) | set(config.ports.values())) != len(ports) + len(set(config.ports.values())):
        errors.append("config.split.ports: OVERLAP")  # 같은 회차 안 다른 리슨·publish와 겹치면 compose가 뜨지 않거나 섞인다
    if not _int(raw["evidence_max_bytes"], 1, MAX_EVIDENCE_BYTES):
        errors.append("config.split.evidence_max_bytes: INVALID")
    for name in ("install_valid_hours", "registration_valid_hours"):
        if not _int(raw[name], 1, MAX_HOURS):
            errors.append(f"config.split.{name}: INVALID")
    if not _int(raw["max_runs"], 1, 1000):
        errors.append("config.split.max_runs: INVALID")
    if type(raw["wide_writes"]) is not bool:
        errors.append("config.split.wide_writes: INVALID")
    if not isinstance(raw["profile_id"], str) or not _PROFILE.fullmatch(raw["profile_id"]):
        errors.append("config.split.profile_id: INVALID")
    if not _int(raw["task_wait_seconds"], 1, MAX_WAIT_SECONDS) or not _int(raw["poll_interval_seconds"], 1, 60):
        errors.append("config.split.wait: INVALID")
    elif raw["poll_interval_seconds"] >= raw["task_wait_seconds"]:
        errors.append("config.split.wait: INTERVAL_NOT_BELOW_WAIT")
    if errors:
        raise SplitConfigError(errors)
    images = {name: Image(f"{repository}:{raw['product_tag']}", ids[name]) for name, repository in IMAGE_REPOSITORIES.items()}
    return SplitConfig(product_tag=raw["product_tag"], product_source_sha=raw["product_source_sha"], images=images,
                       manifest_path=Path(raw["manifest_path"]), target_name=raw["target_name"],
                       product_port=raw["product_port"], dns_control_port=raw["dns_control_port"],
                       explore_port=raw["explore_port"], evidence_max_bytes=raw["evidence_max_bytes"],
                       install_valid_hours=raw["install_valid_hours"], max_runs=raw["max_runs"],
                       registration_valid_hours=raw["registration_valid_hours"], wide_writes=raw["wide_writes"],
                       profile_id=raw["profile_id"], task_wait_seconds=raw["task_wait_seconds"],
                       poll_interval_seconds=raw["poll_interval_seconds"])


def read_manifest(path: Path) -> dict:
    """제품 OpenRouter 승인 manifest(v3)를 읽는다. 러너 uid 소유·0600·링크 아닌 일반 파일만 받는다. 내용은 오류에 넣지 않는다.

    manifest는 설치 입력 안에 그대로 들어간다(runner_split.install_input). 승인 ID 등 민감한 칸이 있어 근거·로그에 남기지 않는다.
    """
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK)
    except (OSError, TypeError, ValueError):
        raise SplitConfigError(["split.manifest: UNREADABLE"]) from None
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600:
        os.close(fd)
        raise SplitConfigError(["split.manifest: UNTRUSTED"])
    with os.fdopen(fd, "rb") as stream:
        data = stream.read(MAX_MANIFEST_BYTES + 1)
    try:
        if len(data) > MAX_MANIFEST_BYTES:
            raise ValueError
        manifest = json.loads(data.decode("utf-8"))
    except (UnicodeError, ValueError, RecursionError):
        raise SplitConfigError(["split.manifest: INVALID"]) from None
    if not isinstance(manifest, dict) or manifest.get("version") != 3 or not isinstance(manifest.get("model"), str):
        raise SplitConfigError(["split.manifest: INVALID"])
    return manifest


@dataclass(frozen=True)
class SplitPlan:
    """한 O 회차의 비민감 계획. compose_env에는 비밀이 없다(보호 입력 경로는 실행 때 더한다)."""
    run_id: str
    project: str
    compose_files: tuple[Path, ...]
    profiles: tuple[str, ...]
    compose_env: dict[str, str]
    images: dict[str, Image]
    target_origin: str
    meter_port: int
    explore_port: int


def plan_split_run(config: RunnerConfig, split: SplitConfig, *, run_id: str, target_image: Image, meter_port: int) -> SplitPlan:
    """O 회차 계획. 계측기·대상·관측 칸은 B와 같은 러너 설정 값이다. 대상 이름은 계측기 본 입구로 풀린다(overlay)."""
    if not _int(meter_port, 1, 65535) or meter_port in (split.product_port, split.dns_control_port, split.explore_port):
        raise SplitConfigError(["split.meter_port: INVALID"])
    env = {
        "PRODUCT_TAG": split.product_tag,
        "PRODUCT_SOURCE_SHA": split.product_source_sha,
        "PRODUCT_PORT": str(split.product_port),
        "PRODUCT_DNS_CONTROL_PORT": str(split.dns_control_port),
        "EVAL_TARGET_NAME": split.target_name,
        "EVAL_TARGET_IMAGE": target_image.ref,
        "EVAL_METER_IMAGE": config.images["meter"].ref,
        "EVAL_METER_PORT": str(meter_port),
        "EVAL_METER_EXPLORE_PORT": str(split.explore_port),
        "EVAL_TARGET_PORT": str(config.ports["target"]),
        "EVAL_METER_DRAIN_GRACE": f"{config.run['drain_grace_seconds']}s",
        "EVAL_OBS_PORT": str(config.ports["obs"]),
        "EVAL_OBS_HOST_PORT": str(config.ports["obs_host"]),
        "EVAL_OBS_PATH": config.observer_path,
        # B 회차 계측기와 같은 경로 가림 키(보존 회차는 수용시험 목적이라 늘 있다).
        "EVAL_PATH_KEY_FILE": str(config.path_key_file),
    }
    images = {**split.images, "meter": config.images["meter"], "target": target_image}
    return SplitPlan(run_id=run_id, project=f"eval-{run_id}", compose_files=COMPOSE_FILES, profiles=PROFILES,
                     compose_env=env, images=images, target_origin=f"http://{split.target_name}:{meter_port}",
                     meter_port=meter_port, explore_port=split.explore_port)


def split_compose_environment(plan: SplitPlan, *, evidence_dir: Path, protected_env: dict[str, str]) -> dict[str, str]:
    """실행 때 compose 환경: 계획 + 계측기 근거 디렉터리 + 러너 uid·gid + 보호 입력 파일 경로. 겹치는 이름은 거부한다."""
    extra = {"EVAL_EVIDENCE_DIR": str(evidence_dir), "EVAL_RUNNER_UID": str(os.geteuid()), "EVAL_RUNNER_GID": str(os.getegid())}
    if set(extra) & set(plan.compose_env) or set(protected_env) & (set(plan.compose_env) | set(extra)):
        raise SplitConfigError(["split.compose_env: OVERLAP"])
    return {**plan.compose_env, **extra, **protected_env}


def split_compose_args(plan: SplitPlan) -> list[str]:
    """`docker compose` 앞부분 인자(파일·프로필·project). 비밀 env 파일은 넘기지 않는다(O 제품은 키를 설치 뒤 API로 받는다).

    `--env-file /dev/null`: 주지 않으면 compose가 프로젝트 디렉터리(deploy/product)의 `.env`를 저절로 읽어 계획 밖 값이 섞인다.
    """
    args = ["compose", "-p", plan.project, "--env-file", "/dev/null"]
    for path in plan.compose_files:
        args += ["-f", str(path)]
    for profile in plan.profiles:
        args += ["--profile", profile]
    return args
