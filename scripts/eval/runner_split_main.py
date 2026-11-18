"""탐색 분리 배치 O 회차 진입점(결정안 4-7c). runner.main이 `split` 설정 + `--side ours`일 때 부른다.

O는 엔진 직접 배치가 아니라 제품 compose + 평가 overlay로 돈다. 관리자 비밀번호는 회차마다 새로 만들어
보호 입력(runner_split_inputs)에만 두므로 묻지 않는다. 모델 키는 B 게이트웨이와 같은 비밀 env 파일의 같은 이름을 같은
compose 규칙으로 읽어(Docker.env_file_value) 설치 뒤 제품 API로만 넘긴다.

여기 결과는 서명 전 요약이다. 경계 원자료 검증·서명·보존 sidecar v3는 다음 단계(4-9)다.
"""

from __future__ import annotations

import hashlib
import json
import os
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from urllib.parse import urlsplit

from preflight import PreflightError, check_preflight
from runner import Clock, _remove_unstarted_run, acceptance_path_key
from runner_config import (
    METER_SERVICE, POLICY_SWITCH_SIDE, Image, RunnerConfig, RunnerConfigError, create_run_directory, gateway_sampling,
    new_run_id,
)
from runner_docker import Docker, DockerError, client_environment, run_docker
from runner_evidence import EvidenceDir, MeterFiles
from runner_product import ModelKey, ProductAPIError, ProductDriver, loopback_send
from runner_split import EXPLORATION_BLOCK, read_workflow_defaults
from path_mask import key_ref
import preservation_v3_fields as v3f
from runner_split_config import (
    SplitConfigError, SplitPlan, plan_split_run, read_manifest, split_compose_args, split_compose_environment,
)
from runner_split_inputs import SplitInputsError, prepare_split_inputs, remove_split_inputs, run_openssl
from runner_split_ports import DockerSplitPorts
from runner_split_run import COMPLETE, SplitRunSpec, execute_split_run
from runner_window import healthcheck_actions

MODEL_KEY_NAME = "EVAL_LLM_API_KEY"


@dataclass(frozen=True)
class SplitDeclared:
    """O 회차가 사전검사 선언에서 쓰는 값. 예산·대상·봉인 뒤 조건은 B와 같은 선언 규칙으로 읽는다."""
    target_image: Image
    meter_port: int
    request_budget: int
    time_budget_seconds: int
    llm_provider: str = ""
    meter_source_sha: str = ""  # 사전검사 선언의 개선 후 source_sha(계측기 기대 빌드)
    # B 게이트웨이가 넣는 샘플링(선언 §6). O 표식의 실제로 보낸 값과 대조한다.
    llm_sampling: dict | None = None


def split_declared(config: RunnerConfig, preflight: object, manifest: dict) -> SplitDeclared:
    """사전검사 선언의 O 쪽을 확인한다. 모델·제공자는 승인 manifest와 같아야 한다(모델 고정은 선언이 정본이고, 제품은
    manifest로만 모델을 부르므로 둘이 다르면 선언과 다른 모델로 돈다). 봉인 뒤 유예·정착 대기는 B와 같은 비교 조건이다."""
    try:
        check_preflight(preflight)
    except PreflightError as exc:
        raise RunnerConfigError(["preflight: " + error for error in exc.errors]) from None
    declared = preflight[POLICY_SWITCH_SIDE]
    origin = urlsplit(declared["target_origin"])
    if origin.hostname != METER_SERVICE or origin.port is None:
        raise RunnerConfigError([f"preflight.{POLICY_SWITCH_SIDE}.target_origin: NOT_EVAL_METER"])
    errors = []
    drain = {"grace_seconds": config.run["drain_grace_seconds"], "settle_seconds": config.run["post_settle_seconds"]}
    if declared["run_drain"] != drain:
        errors.append(f"preflight.{POLICY_SWITCH_SIDE}.run_drain: CONFIG_MISMATCH")
    llm = declared["llm"]
    # base_url도 승인 값과 같아야 한다. 다르면 같은 모델 키를 다른 origin으로 사후 조회(llm_lookup_env)하게 된다(#674 리뷰).
    if (llm.get("backend") != "api" or llm.get("model") != manifest.get("model") or llm.get("provider") != manifest.get("provider_slug")
            or llm.get("base_url") != manifest.get("base_url")):
        errors.append(f"preflight.{POLICY_SWITCH_SIDE}.llm: MANIFEST_MISMATCH")
    if errors:
        raise RunnerConfigError(errors)
    return SplitDeclared(target_image=Image(declared["target_image"], config.target_image_id), meter_port=origin.port,
                         request_budget=declared["request_budget"], time_budget_seconds=declared["time_budget_seconds"],
                         llm_provider=llm["provider"], llm_sampling=json.loads(gateway_sampling(declared, POLICY_SWITCH_SIDE)),
                         meter_source_sha=declared["source_sha"])


def llm_lookup_env(config: RunnerConfig, preflight: dict) -> dict[str, str]:
    """O 호출 기록 사후 검증(generation-verify)의 조회 설정. B 게이트웨이 compose 값(runner_config.plan_run)과 같은 식이고,
    같은지는 시험이 plan_run 결과와 대조한다. 선언 두 쪽의 LLM은 사전검사가 같게 묶는다."""
    llm = preflight[POLICY_SWITCH_SIDE]["llm"]
    upstream = urlsplit(llm["base_url"])
    gateway = config.llm_gateway
    return {"EVAL_LLM_UPSTREAM": f"{upstream.scheme}://{upstream.netloc}", "EVAL_LLM_PROVIDER": llm["provider"],
            "EVAL_LLM_GENERATION_PATH": gateway["generation_path"], "EVAL_LLM_VERIFY_ATTEMPTS": str(gateway["verify_attempts"]),
            "EVAL_LLM_VERIFY_INTERVAL": f"{gateway['verify_interval_seconds']}s", "EVAL_CA_BUNDLE": gateway["ca_bundle"]}


def _model_budget(manifest: dict | None, config: RunnerConfig) -> str:
    """보존 회차만 쓰는 O 모델 예산 선언 지문. 승인 manifest에 예산 칸이 모자라면 설정 오류다."""
    if manifest is None or config.preservation is None:
        return ""
    value = v3f.model_budget_sha256(manifest)
    if value is None:
        raise SplitConfigError(["split.manifest: BUDGET_INVALID"])
    return value


def split_run_spec(config: RunnerConfig, plan: SplitPlan, declared: SplitDeclared, rendered: dict,
                   path_key_ref: str | None = None, *, manifest: dict | None = None, repeat_index: int = 0,
                   exploration_policy_sha256: str = "") -> SplitRunSpec:
    """회차 시한·간격은 B(build_spec)와 같은 설정 칸에서 읽는다. 제품 멈춤 뒤 끝 상태 대기는 제품 상태 대기(split.task_wait_seconds)."""
    return SplitRunSpec(
        run_id=plan.run_id, project=plan.project, image_ids={role: image.id for role, image in plan.images.items()},
        request_budget=declared.request_budget, time_budget_seconds=declared.time_budget_seconds,
        poll_seconds=config.run["budget_poll_seconds"], drain_timeout_seconds=config.run["drain_timeout_seconds"],
        events_lookback_seconds=config.health_timeout_seconds,
        host_watch_max_gap_seconds=config.run["host_watch_max_gap_seconds"],
        task_status_wait_seconds=config.run["task_create_timeout_seconds"], healthchecks=healthcheck_actions(rendered),
        product_close_seconds=config.split.task_wait_seconds, post_settle_seconds=config.run["post_settle_seconds"],
        llm_sampling=declared.llm_sampling, llm_provider_slug=declared.llm_provider,
        llm_provider_name=config.llm_gateway["expected_provider_name"], meter_source_sha=declared.meter_source_sha,
        path_key_ref=path_key_ref, preservation=config.preservation, purpose=config.purpose, repeat_index=repeat_index,
        drain_grace_seconds=config.run["drain_grace_seconds"],
        host_watch_interval_seconds=config.run["host_watch_interval_seconds"],
        model_tag="" if manifest is None else manifest["model"], product_source_sha=config.split.product_source_sha,
        driver_settings={"task_wait_seconds": config.split.task_wait_seconds,
                         "poll_interval_seconds": config.split.poll_interval_seconds,
                         "registration_valid_hours": config.split.registration_valid_hours},
        model_budget_sha256=_model_budget(manifest, config),
        exploration_policy_sha256=exploration_policy_sha256)


def run_split_ours(config: RunnerConfig, preflight: object, *, repeat_index: int = 0, docker_runner=run_docker, environ=None,
                   send=loopback_send, now=None) -> int:
    """O 회차 1회(자동 재시도 없음). 설정·입력 단계 실패는 2, 회차 무효는 1, 완료는 0."""
    environ = os.environ if environ is None else environ
    split = config.split
    directory = None
    inputs = None
    try:
        manifest = read_manifest(split.manifest_path)
        declared = split_declared(config, preflight, manifest)
        plan = plan_split_run(config, split, run_id=new_run_id(POLICY_SWITCH_SIDE, now), target_image=declared.target_image,
                              meter_port=declared.meter_port)
        defaults, defaults_sha = read_workflow_defaults()
        # 경계는 계측기·B 회차와 같은 경로 가림 키를 받는다(계약 §4.1). runner.main이 check_key_files로 이미 확인했다.
        path_key = acceptance_path_key(config)
        # 키 지문은 경계 보호 입력에 넣는 바로 그 바이트에서 뜬다(#699 리뷰: 따로 읽으면 사이에 키 파일이 바뀌어도 통과).
        # 계측기는 같은 파일을 직접 읽으므로, 그 행의 지문이 이 값과 다르면 check_meter_logs가 meter_path_key_mismatch로 막는다.
        path_key_ref = None if path_key is None else key_ref(path_key)
        directory = create_run_directory(config.runs_root, plan.run_id)
        inputs = prepare_split_inputs(directory.run, manifest=manifest, meter_port=plan.meter_port, explore_port=split.explore_port,
                                      evidence_max_bytes=split.evidence_max_bytes, workflow_defaults=defaults,
                                      wide_writes=split.wide_writes, valid_for_hours=split.install_valid_hours,
                                      max_runs=split.max_runs, path_key=path_key, openssl=run_openssl)
        env = split_compose_environment(plan, evidence_dir=directory.meter, protected_env=inputs.compose_env)
        docker = Docker(docker_runner, client_environment(environ, env), config.docker_timeout_seconds)
        rendered = docker.compose_config(split_compose_args(plan))
        # B 게이트웨이와 같은 env 파일·같은 compose 규칙으로 읽는다. 값은 메모리에만 두고 제품 모델 키 API로만 보낸다.
        model_key = ModelKey(docker.env_file_value(config.secrets_env_file, MODEL_KEY_NAME, directory.run))
        driver = ProductDriver(api_port=split.product_port, dns_port=split.dns_control_port,
                               send=lambda method, port, path, body, headers: send(
                                   method, port, path, body, headers, timeout=config.run["task_create_timeout_seconds"]),
                               sleep=time.sleep, monotonic=time.monotonic)
        ports = DockerSplitPorts(config=config, split=split, plan=plan, directory=directory, docker=docker, driver=driver,
                                 inputs=inputs, rendered=rendered, manifest=manifest, model_key=model_key,
                                 exploration=dict(EXPLORATION_BLOCK), llm_env=llm_lookup_env(config, preflight),
                                 workflow_defaults=(defaults, defaults_sha))
        # 경계 설정(boundary.json)은 회차 값 없이 정렬된 JSON이라 같은 설정이면 같은 지문이다(결정안 §3.3)
        boundary_sha = hashlib.sha256(inputs.files["boundary"].read_bytes()).hexdigest()
        spec = split_run_spec(config, plan, declared, rendered, path_key_ref, manifest=manifest, repeat_index=repeat_index,
                              exploration_policy_sha256=boundary_sha)
    except BaseException as exc:
        # 회차 흐름(execute_split_run) 전의 어떤 실패·중단에서도 보호 입력을 먼저 지운다(#674 리뷰: 모델 키 모양 오류·
        # 운영자 중단이 정리를 건너뛰었다). 고정 code 오류는 code만 내고 종료 코드 2, 중단은 정리 뒤 다시 올린다.
        _abandon(directory, inputs)
        if isinstance(exc, (RunnerConfigError, SplitConfigError)):
            print("\n".join(exc.errors), file=sys.stderr)
            return 2
        if isinstance(exc, (SplitInputsError, DockerError, ProductAPIError)):
            print(str(exc), file=sys.stderr)
            return 2
        if isinstance(exc, Exception):
            print("split_prepare_failed", file=sys.stderr)  # 예외 글은 싣지 않는다(값이 섞일 수 있음)
            return 2
        raise
    clock = Clock(lambda: datetime.now(timezone.utc), time.monotonic, time.sleep)
    result = execute_split_run(spec, ports, MeterFiles(directory.meter), EvidenceDir(directory.run), clock)
    print(f"{result['run_id']} {result['status']} {' '.join(result.get('reasons', []))}".rstrip())
    return 0 if result["status"] == COMPLETE else 1


def _abandon(directory, inputs) -> None:
    """띄우기 전 실패는 시도가 아니다(B와 같이 exit 2, 회차 디렉터리 삭제). 보호 입력(관리자·DB 비밀번호·CA 키)을 먼저
    지워 디렉터리 삭제가 실패해도 비밀이 남지 않게 한다."""
    if inputs is not None:
        try:
            remove_split_inputs(inputs.directory, missing_ok=True)
        except (OSError, SplitInputsError):
            print("split_inputs: REMOVE_FAILED — 회차 디렉터리의 private/를 직접 지워야 한다", file=sys.stderr)
    _remove_unstarted_run(directory)
