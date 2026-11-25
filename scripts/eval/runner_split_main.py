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
from runner_checks import Listener
from runner_docker import Docker, DockerError, client_environment, run_docker
from runner_evidence import EvidenceDir, MeterFiles
from runner_product import ModelKey, ProductAPIError, ProductDriver, loopback_send
from runner_split import (
    EXPLORATION_BLOCK, SCRIPTED_MODEL_BASE_URL, SCRIPTED_MODEL_HOST, SCRIPTED_MODEL_IP, SplitInputError, product_run_seconds,
    read_workflow_defaults, synthetic_local_model,
)
from preservation_record import digest
from scripted_product_llm import PlanError, load_plan
from path_mask import key_ref
import preservation_v3_fields as v3f
from runner_split_config import (
    SplitConfigError, SplitPlan, plan_split_run, read_manifest, split_compose_args, split_compose_environment,
)
from runner_observer import ObserverFileError, remove_observer_password, write_observer_password
from runner_split_inputs import SplitInputsError, prepare_split_inputs, remove_split_inputs, run_openssl
from runner_split_ports import DockerSplitPorts
from runner_split_run import COMPLETE, SplitRunSpec, execute_split_run, missing_record_fields
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
    llm_model: str = ""  # 선언 모델(합성 모드는 스크립트 모델 이름)
    context_window_k: int = 0  # 합성 모드 local_model 칸(선언 context_window_k)
    # v1.1 러너 기록 칸(B build_spec과 같은 선언 값)
    target_origin: str = ""
    fixture_fingerprint: str = ""
    environment_fingerprint: str = ""


def split_declared(config: RunnerConfig, preflight: object, manifest: dict | None) -> SplitDeclared:
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
    context_k = 0
    if config.split.synthetic:
        # 합성 모델 모드: 선언은 로컬 백엔드이고 주소·포트는 overlay의 스크립트 모델 서비스 하나여야 한다(사용자 결정 조건).
        # 제품 local_model의 문맥 칸은 B 엔진 설정과 같은 선언 context_window_k다(사전검사가 서버 문맥 이하인지 본다).
        context_k = declared["context_window_k"] if type(declared.get("context_window_k")) is int else 0
        if (manifest is not None or llm.get("backend") != "local" or llm.get("hostname") != SCRIPTED_MODEL_HOST
                or llm.get("host_ip") != SCRIPTED_MODEL_IP or llm.get("base_url") != SCRIPTED_MODEL_BASE_URL or context_k < 1):
            errors.append(f"preflight.{POLICY_SWITCH_SIDE}.llm: SYNTHETIC_MODEL_MISMATCH")
    # base_url도 승인 값과 같아야 한다. 다르면 같은 모델 키를 다른 origin으로 사후 조회(llm_lookup_env)하게 된다(#674 리뷰).
    # 제품이 응답에서 검증할 제공자 표시 이름도 러너가 사후 판정에 쓰는 이름과 같아야 한다. 다르면 제품은 응답을 거부하거나
    # 러너가 맞는 응답을 불일치로 판정해 유료 회차가 결과 없이 무효가 된다(2026-11-18 VAmPI 유료 파일럿 O, 표시 이름 띄어쓰기 차이).
    elif (manifest is None or llm.get("backend") != "api" or llm.get("model") != manifest.get("model")
            or llm.get("provider") != manifest.get("provider_slug") or llm.get("base_url") != manifest.get("base_url")
            or manifest.get("expected_provider_name") != config.llm_gateway["expected_provider_name"]):
        errors.append(f"preflight.{POLICY_SWITCH_SIDE}.llm: MANIFEST_MISMATCH")
    if errors:
        raise RunnerConfigError(errors)
    return SplitDeclared(target_image=Image(declared["target_image"], config.target_image_id), meter_port=origin.port,
                         request_budget=declared["request_budget"], time_budget_seconds=declared["time_budget_seconds"],
                         llm_provider=llm.get("provider", ""), llm_sampling=json.loads(gateway_sampling(declared, POLICY_SWITCH_SIDE)),
                         meter_source_sha=declared["source_sha"], llm_model=llm["model"], context_window_k=context_k,
                         target_origin=declared["target_origin"], fixture_fingerprint=declared["fixture_fingerprint"],
                         environment_fingerprint=declared["environment_fingerprint"])


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
    """보존 회차만 쓰는 O 모델 예산 선언 지문. 승인 manifest에 예산 칸이 모자라면 설정 오류다. 합성 모델 모드는 제품 로컬
    설치 예산(제품이 계산)이라 선언된 local_model 칸의 지문이다."""
    if config.preservation is None:
        return ""
    if config.split is not None and config.split.synthetic:
        return digest({"synthetic_model": {"max_tokens": config.split.synthetic_max_tokens}})
    if manifest is None:
        return ""
    value = v3f.model_budget_sha256(manifest)
    if value is None:
        raise SplitConfigError(["split.manifest: BUDGET_INVALID"])
    return value


def _approval_caps(manifest: dict | None) -> tuple[int, int, int, int] | None:
    """승인 요청·비용 상한과 단가(manifest max_requests·max_cost_usd_micros·입력/출력 단가). 일찍 끝난 회차가 승인 예산에
    닿았는지 대조한다(runner_early_end.approval_budget). 합성 모델 모드는 manifest가 없어 None이다. 칸이 양의 정수가 아니면
    설정 오류다."""
    if manifest is None:
        return None
    caps = (manifest.get("max_requests"), manifest.get("max_cost_usd_micros"),
            manifest.get("max_input_price_usd_micros_per_million"), manifest.get("max_output_price_usd_micros_per_million"))
    if any(type(cap) is not int or cap <= 0 for cap in caps):
        raise SplitConfigError(["split.manifest: BUDGET_INVALID"])
    return caps


def split_run_spec(config: RunnerConfig, plan: SplitPlan, declared: SplitDeclared, rendered: dict,
                   path_key_ref: str | None = None, *, manifest: dict | None = None, repeat_index: int = 0,
                   exploration_policy_sha256: str = "", scripted_plan_sha256: str = "", condition: str = "") -> SplitRunSpec:
    """회차 시한·간격은 B(build_spec)와 같은 설정 칸에서 읽는다. 제품 멈춤 뒤 끝 상태 대기는 제품 상태 대기(split.task_wait_seconds)."""
    spec = SplitRunSpec(
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
        e2_v3=config.split.e2_v3 is not None,
        cli_usage_names=config.split.cli_usage_names,
        drain_grace_seconds=config.run["drain_grace_seconds"],
        host_watch_interval_seconds=config.run["host_watch_interval_seconds"],
        model_tag=declared.llm_model if manifest is None else manifest["model"], product_source_sha=config.split.product_source_sha,
        driver_settings={"task_wait_seconds": config.split.task_wait_seconds,
                         "poll_interval_seconds": config.split.poll_interval_seconds,
                         "registration_valid_hours": config.split.registration_valid_hours},
        model_budget_sha256=_model_budget(manifest, config),
        exploration_policy_sha256=exploration_policy_sha256, synthetic=config.split.synthetic,
        scripted_plan_sha256=scripted_plan_sha256, fault_stall_seconds=config.fault_stall_seconds,
        # v1.1 러너 기록(설계안 §2·§5): 측정 매트릭스 행 이름·선언 지문·대상 origin·관측 GET은 B(build_spec)와 같은 출처다.
        # 호스트 리슨 허용은 api 백엔드의 B 규칙(allowed_host_listens)과 같은 설정 허용 목록이다(O는 모델 리슨이 없다).
        condition=condition, fixture_fingerprint=declared.fixture_fingerprint,
        environment_fingerprint=declared.environment_fingerprint, target_origin=declared.target_origin,
        observer_path=config.observer_path, observer_readonly_ref=config.observer_readonly_ref,
        allowed_listens=frozenset(Listener(entry.address, entry.port) for entry in config.host_listen_allowlist),
        observer_login_file=config.split.observer_password_file is not None,
        e3_disabled_required=config.split.e3_disabled_required,
        approval_caps=_approval_caps(manifest))
    # 실행 입력을 만드는 시점에 v1.1 기록 칸을 본다. 비면 설정 오류(종료 코드 2, 회차를 만들지 않음)다(#745 리뷰).
    missing = missing_record_fields(spec)
    if missing:
        raise RunnerConfigError([f"split_run_spec.{name}: MISSING" for name in missing])
    return spec


def run_split_ours(config: RunnerConfig, preflight: object, *, repeat_index: int = 0, condition: str = "", docker_runner=run_docker, environ=None,
                   send=loopback_send, now=None) -> int:
    """O 회차 1회(자동 재시도 없음). 설정·입력 단계 실패는 2, 회차 무효는 1, 완료는 0."""
    environ = os.environ if environ is None else environ
    split = config.split
    directory = None
    inputs = None
    try:
        manifest = None if split.synthetic else read_manifest(split.manifest_path)
        declared = split_declared(config, preflight, manifest)
        plan = plan_split_run(config, split, run_id=new_run_id(POLICY_SWITCH_SIDE, now), target_image=declared.target_image,
                              meter_port=declared.meter_port, request_cap=declared.request_budget)
        defaults, defaults_sha = read_workflow_defaults()
        # 경계는 계측기·B 회차와 같은 경로 가림 키를 받는다(계약 §4.1). runner.main이 check_key_files로 이미 확인했다.
        path_key = acceptance_path_key(config)
        # 키 지문은 경계 보호 입력에 넣는 바로 그 바이트에서 뜬다(#699 리뷰: 따로 읽으면 사이에 키 파일이 바뀌어도 통과).
        # 계측기는 같은 파일을 직접 읽으므로, 그 행의 지문이 이 값과 다르면 check_meter_logs가 meter_path_key_mismatch로 막는다.
        path_key_ref = None if path_key is None else key_ref(path_key)
        directory = create_run_directory(config.runs_root, plan.run_id)
        scripted_plan, scripted_sha, local_model = None, "", None
        if split.synthetic:
            scripted_plan, scripted_sha = _copy_scripted_plan(split.synthetic_plan_path, directory.run, plan.target_origin,
                                                              declared.llm_model)
            local_model = synthetic_local_model(declared.llm_model, declared.context_window_k, split.synthetic_max_tokens)
        inputs = prepare_split_inputs(directory.run, manifest=manifest, meter_port=plan.meter_port, explore_port=split.explore_port,
                                      evidence_max_bytes=split.evidence_max_bytes, workflow_defaults=defaults,
                                      wide_writes=split.wide_writes, valid_for_hours=split.install_valid_hours,
                                      max_runs=split.max_runs, run_seconds=product_run_seconds(declared.time_budget_seconds),
                                      path_key=path_key, openssl=run_openssl, local_model=local_model,
                                      e2_audit_key_variable=None if split.e2_v3 is None else split.e2_v3.audit_key_variable)
        if split.observer_password_file is not None:
            write_observer_password(split.observer_password_file, inputs.admin)
        env = split_compose_environment(plan, evidence_dir=directory.meter, protected_env=inputs.compose_env,
                                        scripted_plan=scripted_plan)
        docker = Docker(docker_runner, client_environment(environ, env), config.docker_timeout_seconds)
        rendered = docker.compose_config(split_compose_args(plan))
        # B 게이트웨이와 같은 env 파일·같은 compose 규칙으로 읽는다. 값은 메모리에만 두고 제품 모델 키 API로만 보낸다.
        # 합성 모델 모드는 키가 없다(제품 로컬 모델 설치가 스크립트 모델을 부른다). 비밀 env 파일을 읽지 않는다.
        model_key = None if split.synthetic else ModelKey(docker.env_file_value(config.secrets_env_file, MODEL_KEY_NAME, directory.run))
        driver = ProductDriver(api_port=split.product_port, dns_port=split.dns_control_port,
                               send=lambda method, port, path, body, headers: send(
                                   method, port, path, body, headers, timeout=config.run["task_create_timeout_seconds"]),
                               sleep=time.sleep, monotonic=time.monotonic)
        ports = DockerSplitPorts(config=config, split=split, plan=plan, directory=directory, docker=docker, driver=driver,
                                 inputs=inputs, rendered=rendered, manifest=manifest, model_key=model_key,
                                 exploration=dict(EXPLORATION_BLOCK),
                                 llm_env=None if split.synthetic else llm_lookup_env(config, preflight),
                                 model_tag=declared.llm_model if split.synthetic else None,
                                 workflow_defaults=(defaults, defaults_sha))
        # 경계 설정(boundary.json)은 회차 값 없이 정렬된 JSON이라 같은 설정이면 같은 지문이다(결정안 §3.3)
        boundary_sha = hashlib.sha256(inputs.files["boundary"].read_bytes()).hexdigest()
        spec = split_run_spec(config, plan, declared, rendered, path_key_ref, manifest=manifest, repeat_index=repeat_index,
                              condition=condition,
                              exploration_policy_sha256=boundary_sha, scripted_plan_sha256=scripted_sha)
    except BaseException as exc:
        # 회차 흐름(execute_split_run) 전의 어떤 실패·중단에서도 보호 입력을 먼저 지운다(#674 리뷰: 모델 키 모양 오류·
        # 운영자 중단이 정리를 건너뛰었다). 고정 code 오류는 code만 내고 종료 코드 2, 중단은 정리 뒤 다시 올린다.
        _abandon(directory, inputs, split.observer_password_file)
        if isinstance(exc, (RunnerConfigError, SplitConfigError)):
            print("\n".join(exc.errors), file=sys.stderr)
            return 2
        if isinstance(exc, (SplitInputsError, SplitInputError, DockerError, ProductAPIError, ObserverFileError)):
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


SCRIPTED_PLAN_NAME = "scripted-plan.json"


def _copy_scripted_plan(source, run_directory, origin: str, model: str):
    """합성 모델 계획을 회차 디렉터리에 0600으로 복사하고 그 사본을 검사한다(overlay가 이 사본을 읽기 전용으로 붙인다).
    사본 SHA가 결과에 남는다. 계획 모델은 선언 모델과 같아야 한다."""
    try:
        data = source.read_bytes()
    except OSError:
        raise SplitConfigError(["split.synthetic_model.plan_path: UNREADABLE"]) from None
    target = run_directory / SCRIPTED_PLAN_NAME
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(data)
    try:
        loaded = load_plan(target, origin)
    except PlanError as exc:
        raise SplitConfigError([f"split.synthetic_model.plan_path: {exc}"]) from None
    if loaded["model"] != model:
        raise SplitConfigError(["split.synthetic_model.plan_path: MODEL_NOT_DECLARED"])
    return target, hashlib.sha256(data).hexdigest()


def _abandon(directory, inputs, observer_file=None) -> None:
    """띄우기 전 실패는 시도가 아니다(B와 같이 exit 2, 회차 디렉터리 삭제). 보호 입력(관리자·DB 비밀번호·CA 키)과 관찰
    로그인 파일을 먼저 지워 디렉터리 삭제가 실패해도 비밀이 남지 않게 한다."""
    try:
        remove_observer_password(observer_file)
    except ObserverFileError:
        print("observer_file: REMOVE_FAILED — 관찰 로그인 파일을 직접 지워야 한다", file=sys.stderr)
    if inputs is not None:
        try:
            remove_split_inputs(inputs.directory, missing_ok=True)
        except (OSError, SplitInputsError):
            print("split_inputs: REMOVE_FAILED — 회차 디렉터리의 private/를 직접 지워야 한다", file=sys.stderr)
    _remove_unstarted_run(directory)
