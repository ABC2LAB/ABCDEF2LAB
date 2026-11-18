"""탐색 분리 배치(개선 후 O) 회차 흐름 부품(탐색 분리 배치 결정안 §3·§3.1, 순서 4-7c-3).

O의 엔진 끝 신호는 엔진 작업 목록이 아니라 제품 초안 상태다(결정안 §3.1: 제품 실행 상태와 결과·마감 사유 칸). 러너는
B와 같은 감시 규칙(runner._EngineEndWatch: 창 동안 백그라운드 읽기, 첫 끝 상태, `down` 전 마지막 읽기, 읽기 실패는 셈만)을
제품 상태 읽기에 그대로 쓴다. 제품이 예산 마감(budget_limited)으로 닫히면 first_limit이 `product_limit:budget_limited`가 된다
(runner.first_limit).
"""

from __future__ import annotations

from runner import Clock, _EngineEndWatch
from runner_product import DRAFT_TERMINAL, ProductAPIError, ProductDriver

PRODUCT_END_FORMAT = "abc2lab-eval-runner-product-end-v1"
# 제품 초안 상태에서 서명 근거로 남길 칸(화면 파서 parseURLJob과 같은 이름). 끝 상태가 아니면 비어 있는 칸은 null이다.
PRODUCT_END_FIELDS = ("state", "phase", "reason_code", "outcome", "closing_reason", "drain_complete", "result_ready",
                      "intent_outcomes", "diagnostic_log_incomplete")


def product_status_view(driver: ProductDriver, draft_id: str) -> dict:
    """제품 초안 상태에서 끝 신호 칸만 꺼낸다. 없는 칸은 null(값을 만들지 않는다)."""
    draft = driver.status(draft_id)
    return {name: draft.get(name) for name in PRODUCT_END_FIELDS}


def product_end_watch(ports, driver: ProductDriver, draft_id: str, task_id: str, clock: Clock, started: float,
                      wait_seconds: float) -> _EngineEndWatch:
    """O 끝 신호 감시. 읽기 실패는 제품 구동기의 고정 code로 센다(비밀·응답 원문 없음)."""
    return _EngineEndWatch(ports, task_id, clock, started, wait_seconds,
                           read=lambda: product_status_view(driver, draft_id), fields=PRODUCT_END_FIELDS,
                           status_field="state", terminal=DRAFT_TERMINAL, terminal_extra=("closing_reason", "reason_code"),
                           fmt=PRODUCT_END_FORMAT, errors=(ProductAPIError,))


# ── O 회차 흐름(4-7c-3) ─────────────────────────────────────────────────────────────────────────────────────────
import hashlib  # noqa: E402
import json  # noqa: E402
import re  # noqa: E402
from dataclasses import dataclass, field  # noqa: E402
from datetime import timedelta  # noqa: E402
from typing import Protocol  # noqa: E402

from runner import (  # noqa: E402
    INVALID, RESULT_NAME, RunFailure, _check_subnets, _event_time, _meter_preflight, _ns, _stamp, _step, _stop_events, _stop_watch,
    _wait_budget, _wait_drained, check_meter_logs, first_limit,
)
from preservation_record import NAME as PRESERVATION_NAME, build_payload_v3 as preservation_payload_v3, digest  # noqa: E402
import preservation_v3_fields as v3f  # noqa: E402
from runner_checks import compare_snapshots  # noqa: E402
from runner_docker import HelperCleanupInterrupt  # noqa: E402
from runner_evidence import MeterFiles  # noqa: E402
from runner_llm import judge_product_llm  # noqa: E402
from runner_probe import PASS  # noqa: E402
from runner_product_llm import (  # noqa: E402
    ProductLLMError, call_record_lines, call_records_reasons, ledger_reservations, product_model_usage,
)
from runner_split_probe import SplitProbeOutcome  # noqa: E402
from runner_window import events_reasons, judge_events, watch_reasons, watch_summary  # noqa: E402

SPLIT_RESULT_FORMAT = "abc2lab-eval-runner-split-result-v1"
SPLIT_TASK_FORMAT = "abc2lab-eval-runner-split-task-v1"
PRODUCT_END_NAME = "product-end.json"
TOOL_SURFACE_NAME = "tool-surface.json"
PRODUCT_FILES_NAME = "product-files.json"
PRODUCT_FILES_FORMAT = "abc2lab-eval-runner-product-files-v1"
# 제어 정책 보기 상한 칸(url_live_views.go controlPolicyLimits). 제품 검증(e2proxy execution.go)은 모두 양수만 받는다.
CONTROL_LIMIT_FIELDS = ("max_requests", "max_attempts", "max_concurrent", "max_body_bytes", "max_response_body_bytes",
                        "request_timeout_ms")
MAX_INT64 = (1 << 63) - 1
RUNTIME_CONDITIONS_VERSION = "e2-runtime-conditions/v1"
MAX_RUNTIME_CONDITIONS_BYTES = 1 << 20
# 제품 참조 규칙: 적용 정책을 검사하는 server/e2_http_runtime.go e2HTTPHostRef와 같은 문자 집합(슬래시 없음, #693 리뷰).
_PRODUCT_REF = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}")
# 실행 조건 파일 칸(e2_http_factory.go가 쓰는 구조체 그대로)과 범위·프로세스 상한 칸(transmit.Scope, e2runner.ProcessLimits).
RUNTIME_CONDITIONS_FIELDS = frozenset({"version", "scope", "policy_revision", "worker_count", "process_limits",
                                       "queue_consumes_command_timeout"})
SCOPE_FIELDS = frozenset({"RunRef", "TaskRef", "CaseRef", "ResetRef", "PolicyRef"})
PROCESS_LIMIT_FIELDS = frozenset({"bash_slots", "container_tasks", "engine_reserve_tasks", "call_tasks", "command_tasks",
                                  "helper_reserve_tasks"})
RUN_RECORD_VERSION = 1  # first_diagnosis_run.go diagnosisRunRecord


def _ref(value) -> bool:
    return isinstance(value, str) and bool(_PRODUCT_REF.fullmatch(value))


def _count(value) -> bool:
    return type(value) is int and 0 <= value <= MAX_INT64


def _hex64(value) -> bool:
    return isinstance(value, str) and len(value) == 64 and all(c in "0123456789abcdef" for c in value)


def _run_refs(draft_id: str) -> dict:
    """그 초안의 실행에 제품이 발급하는 참조(url_preparation_linux.go: run-·policy-<Job ID>)."""
    return {"RunRef": f"run-{draft_id}", "PolicyRef": f"policy-{draft_id}"}


def checked_control_policy(view: dict, draft_id: str) -> dict:
    """제어 정책 보기 가운데 근거로 쓰는 칸만 계약대로 확인해 돌려준다(#693 리뷰). 그 초안의 정책이어야 하고, 내용 지문은
    64 hex, 상한은 여섯 칸 모두 양의 정수(불리언·문자열·추가 칸 없음)다."""
    if view.get("policy_ref") != _run_refs(draft_id)["PolicyRef"] or not _ref(view.get("policy_revision")):
        raise RunFailure("control_policy_unbound")  # 개정도 제품 참조 규칙 안의 값만 근거에 남긴다(#693 리뷰)
    if not _hex64(view.get("policy_fingerprint_sha256")):
        raise RunFailure("diagnosis_policy_fingerprint_missing")  # 자격 pin 규칙이면 제품이 비운다
    limits = view.get("limits")
    if (not isinstance(limits, dict) or set(limits) != set(CONTROL_LIMIT_FIELDS)
            or any(type(limits[name]) is not int or not 1 <= limits[name] <= MAX_INT64 for name in CONTROL_LIMIT_FIELDS)):
        raise RunFailure("control_policy_limits_invalid")
    return {"policy_ref": view["policy_ref"], "policy_revision": view["policy_revision"],
            "policy_fingerprint_sha256": view["policy_fingerprint_sha256"], "limits": {name: limits[name] for name in CONTROL_LIMIT_FIELDS}}


def _scope_bound(scope, draft_id: str, task_id: str) -> bool:
    return (isinstance(scope, dict) and set(scope) == SCOPE_FIELDS and all(_ref(value) for value in scope.values())
            and all(scope[key] == value for key, value in dict(_run_refs(draft_id), TaskRef=task_id).items()))


def diagnosis_entered(record: dict, draft_id: str, task_id: str, policy_revision: str) -> dict | None:
    """진단 실행이 붙었는지(실행 기록 run.execution, request_approvals.go runState). 실행 기록 자체가 이 회차의 것(형식
    판·작업 번호, 작업 승인 때 붙는 실행 참조 run-<초안 ID>, e2_http_runtime.go admitRun)이어야 실행 부재를 믿는다(#693
    리뷰). 붙었으면 그 실행이 이 초안·작업·정책 개정에 결속돼야 한다. 탐색 중에 예산이 끝나면 진단 실행이 없고 회차
    실행 조건 파일도 만들어지지 않는다."""
    run = record.get("run")
    if type(record.get("version")) is not int or record["version"] != RUN_RECORD_VERSION or not isinstance(run, dict):
        raise RunFailure("run_record_invalid")
    if type(record.get("task_id")) is not int or str(record["task_id"]) != task_id or run.get("run_ref") != _run_refs(draft_id)["RunRef"]:
        raise RunFailure("run_record_unbound")
    execution = run.get("execution")
    if execution is None:
        return None
    if (not isinstance(execution, dict) or not _scope_bound(execution.get("scope"), draft_id, task_id)
            or execution.get("policy_revision") != policy_revision):
        raise RunFailure("run_record_unbound")
    return execution  # 범위는 실행 조건 파일과 그대로 대조한다(checked_runtime_conditions)


def _unique_object(pairs: list) -> dict:
    """같은 키가 두 번 나오면 거부한다(중첩 객체 포함). json 기본 동작은 마지막 값만 남겨 모순된 원본이 통과한다(#693 리뷰)."""
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate key")
        value[key] = item
    return value


def _reject_constant(_name: str):
    raise ValueError("non-finite number")


def checked_runtime_conditions(raw: bytes, draft_id: str, task_id: str, policy_revision: str, execution: dict) -> str:
    """회차 실행 조건(e2_http_factory.go)이 이 초안·작업·정책 개정의 것인지 확인하고 원본 바이트 SHA를 돌려준다."""
    try:
        if not isinstance(raw, bytes) or len(raw) > MAX_RUNTIME_CONDITIONS_BYTES:
            raise ValueError
        # 중복 키가 있는 원본은 범위를 대조하기 전에 거부한다. 마지막 값만 대조하고 모순된 원본 SHA를 남기면 안 된다.
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=_unique_object, parse_constant=_reject_constant)
    except (UnicodeError, ValueError, RecursionError):
        raise RunFailure("runtime_conditions_invalid") from None
    limits = value.get("process_limits") if isinstance(value, dict) else None
    # 생산자가 쓰는 칸·타입이 다 있어야 실행 조건을 잃은 회차가 정상처럼 남지 않는다(#693 리뷰).
    if (not isinstance(value, dict) or set(value) != RUNTIME_CONDITIONS_FIELDS or value["version"] != RUNTIME_CONDITIONS_VERSION
            or not _count(value["worker_count"]) or type(value["queue_consumes_command_timeout"]) is not bool
            or not isinstance(limits, dict) or set(limits) != PROCESS_LIMIT_FIELDS or not all(_count(v) for v in limits.values())):
        raise RunFailure("runtime_conditions_invalid")
    # 생산자는 실행 스냅숏 범위(execution.Snapshot().Scope)를 그대로 쓴다. 저장된 실행 기록 범위와 다섯 칸 모두 같아야 한다
    # (#693 리뷰: ResetRef·CaseRef만 달라도 다른 범위의 SHA가 남았다).
    if (not _scope_bound(value.get("scope"), draft_id, task_id) or value["scope"] != execution.get("scope")
            or value.get("policy_revision") != policy_revision):
        raise RunFailure("runtime_conditions_unbound")
    return hashlib.sha256(raw).hexdigest()
COMPLETE = "complete"
METER, BOUNDARY, TARGET = "eval-meter", "explore-boundary", "target"


@dataclass(frozen=True)
class SplitRunSpec:
    run_id: str
    project: str
    image_ids: dict
    request_budget: int
    time_budget_seconds: float
    poll_seconds: float
    drain_timeout_seconds: float
    events_lookback_seconds: float
    host_watch_max_gap_seconds: float
    task_status_wait_seconds: float
    healthchecks: dict  # runner_window.healthcheck_actions(렌더)
    product_close_seconds: float  # 제품 멈춤 뒤 끝 상태·drain 완료까지 기다리는 시한
    post_settle_seconds: int  # 강제 취소가 있으면 post 관측 전 정착 대기(B와 같은 run_drain 공통 조건)
    # 모델 호출 사후 검증 판정(B와 같은 judge_llm 규칙). 샘플링은 B 게이트웨이가 넣는 값(선언 §6), 제공자는 선언 핀.
    llm_sampling: dict | None = None
    llm_provider_slug: str = ""
    llm_provider_name: str = ""
    # 계측기 이미지의 기대 빌드 커밋(사전검사 선언의 개선 후 source_sha). B와 같은 회차 전 점검이 대조한다.
    meter_source_sha: str = ""
    # 경로 가림 키 지문(16 hex, 키 값이 아님). 분리 배치는 B·O가 같은 키로 경로를 기록하고 같은 검사(check_meter_logs)를 받는다.
    path_key_ref: str | None = None
    # 보존 sidecar v3 O 발행(결정안 §3.3). preservation이 없으면 발행하지 않는다(그때 아래 칸은 쓰지 않는다).
    preservation: dict | None = None
    purpose: str = "browser_acceptance"
    repeat_index: int = 0
    drain_grace_seconds: float = 0
    host_watch_interval_seconds: float = 0
    model_tag: str = ""
    product_source_sha: str = ""
    driver_settings: dict = field(default_factory=dict)  # O 구동 설정(preservation_v3_fields.O_DRIVER_SETTINGS)
    model_budget_sha256: str = ""
    exploration_policy_sha256: str = ""  # 러너가 만든 경계 설정(boundary.json) 바이트 SHA


class SplitRunPorts(Protocol):
    def preexisting(self) -> bool: ...
    def image_ids(self) -> dict: ...
    def meter_describe(self) -> object: ...
    def host_spec(self) -> dict: ...
    def planned_subnets(self) -> list: ...
    def host_subnets(self) -> list: ...
    def up(self) -> None: ...
    def snapshot(self, phase: str) -> dict: ...
    def check(self, phase: str) -> dict: ...
    def probes(self, phase: str, snapshot: dict) -> SplitProbeOutcome: ...
    def dns_token(self) -> str: ...
    def start_events(self, since: str): ...
    def start_host_watch(self): ...
    def scoreboard(self, phase: str, snapshot: dict) -> dict: ...
    def prepare_round(self, dns_token: str) -> dict: ...
    def start_round(self, prepared: dict) -> dict: ...
    def product_status(self, draft_id: str) -> dict: ...
    def stop_round(self, draft_id: str) -> dict: ...
    def wait_closed(self, draft_id: str, deadline_seconds: float) -> dict: ...
    def background(self, work) -> None: ...
    def seal(self, meter_container_id: str) -> None: ...
    def stop_boundary(self, boundary_container_id: str) -> None: ...
    def run_record(self, task_id: str) -> dict: ...
    def control_policy(self, task_id: str) -> dict: ...
    def product_files(self) -> dict: ...
    def runtime_conditions(self, draft_id: str) -> bytes: ...
    def tool_surface(self) -> dict: ...
    def split_conditions(self) -> dict: ...
    def boundary_directory(self): ...
    def sign(self, payload: dict) -> dict: ...
    def copy_boundary_evidence(self) -> None: ...
    def copy_product_records(self) -> tuple: ...
    def generation_verify(self, calls: bytes, sampling: dict | None) -> None: ...
    def llm_verified(self) -> dict | None: ...
    def down(self) -> None: ...
    def remove_inputs(self) -> None: ...


@dataclass
class _SplitState:
    """runner의 단계 부품(_step·_wait_budget 등)이 쓰는 칸과 O 회차 기록."""
    step: str = "start"
    watch: object | None = None
    events: object | None = None
    up_attempted: bool = False
    runner_containers: set = field(default_factory=set)
    summary: dict = field(default_factory=dict)
    preservation: dict | None = None  # 내린 뒤 build_payload_v3에 넘길 칸(서명 전)


class _StatusReader:
    """product_end_watch가 부르는 driver.status 자리(포트의 제품 상태 읽기)."""

    def __init__(self, ports: SplitRunPorts):
        self.ports = ports

    def status(self, draft_id: str) -> dict:
        return self.ports.product_status(draft_id)


def _split_check(ports: SplitRunPorts, evidence, state: _SplitState, phase: str) -> None:
    """배치 검사(망·publish·고정 주소·이미지·netns 공유·회차 밖 publish). 구간 이전 상태는 이벤트 감시로 안 잡히므로
    pre/post에서 본다(#668 리뷰). 위반이면 무효."""
    result = _step(state, f"{phase}_checks_failed", lambda: ports.check(phase))
    _step(state, "evidence_write_failed", lambda: evidence.write_json(f"{phase}-checks.json", result))
    if result["violations"]:
        raise RunFailure(f"{phase}_layout_violation")


def _split_probe(ports: SplitRunPorts, evidence, state: _SplitState, phase: str, snapshot: dict) -> None:
    outcome = _step(state, f"{phase}_probe_invalid", lambda: ports.probes(phase, snapshot))
    state.runner_containers.update(outcome.container_ids)
    _step(state, "evidence_write_failed", lambda: evidence.write_json(f"{phase}-probe.json", {
        "phase": phase, "status": outcome.status, "reasons": list(outcome.reasons),
        "origins": {origin: {"status": o.status, "reasons": list(o.reasons), "rows": list(o.rows),
                             "started_at": o.started_at, "finished_at": o.finished_at, "container_id": o.container_id}
                    for origin, o in outcome.origins.items()}}))
    if outcome.status != PASS:
        raise RunFailure(f"{phase}_probe_{outcome.status}")


def _split_window(spec: SplitRunSpec, evidence, state: _SplitState, pre: dict, start, end, stopped: tuple,
                  streamed: tuple) -> dict:
    """구간 감시 판정. 계측기 봉인(USR1), 경계 정지(SIGTERM 한 번), 렌더의 서비스별 healthcheck, 러너 점검 컨테이너만 허용한다."""
    started, finished, samples = stopped
    host = _step(state, "window_watch_failed",
                 lambda: watch_summary(started, finished, samples, spec.host_watch_max_gap_seconds))
    raw, stream_complete = streamed
    networks = {n["id"]: key for key, n in pre["networks"].items()}
    containers = {s["id"]: service for service, s in pre["services"].items()}
    events = _step(state, "window_events_failed", lambda: judge_events(
        raw, _ns(start), networks, containers, frozenset({METER}), frozenset(), frozenset(state.runner_containers),
        stream_complete, stoppable=frozenset({BOUNDARY}), healthchecks=spec.healthchecks))
    reasons = watch_reasons(host) + events_reasons(events)
    record = {"start": _event_time(start), "end": _event_time(end), "lookback_seconds": spec.events_lookback_seconds,
              "host_watch": host, "docker_events": events, "reasons": reasons}
    _step(state, "evidence_write_failed", lambda: evidence.write_json("window-watch.json", record))
    if reasons:
        raise RunFailure(reasons[0])
    return record


def _run_split_steps(spec: SplitRunSpec, ports: SplitRunPorts, meter: MeterFiles, evidence, clock: Clock,
                     state: _SplitState) -> None:
    if _step(state, "project_check_failed", ports.preexisting):
        raise RunFailure("project_exists")
    if _step(state, "image_id_failed", ports.image_ids) != spec.image_ids:
        raise RunFailure("image_id_mismatch")
    _meter_preflight(spec, ports, evidence, state)  # 띄우기 전(모델 호출 전) 계측기 표식 칸·빌드 점검
    # 도구 면(결정안 §4): 회차 망을 띄우기 전에 1회용 컨테이너로 잰다(B는 tool_tree 다음 같은 자리).
    surface = _step(state, "tool_surface_failed", ports.tool_surface)
    if surface.get("image_id") != spec.image_ids["product"] or surface.get("browser_image_id") != spec.image_ids["explore_browser"]:
        raise RunFailure("tool_surface_image_mismatch")
    _step(state, "evidence_write_failed", lambda: evidence.write_json(TOOL_SURFACE_NAME, surface))
    state.summary["tool_surface"] = {"cli_sha256": surface["cli"]["sha256"], "mcp_raw_sha256": surface["mcp"]["raw"]["sha256"],
                                     "mcp_exposed_sha256": surface["mcp"]["exposed"]["sha256"],
                                     "mcp_version": surface["mcp"]["package_version"]}
    state.summary["host"] = _step(state, "host_spec_failed", ports.host_spec)
    _check_subnets(ports, evidence, state)
    state.up_attempted = True
    _step(state, "compose_up_failed", ports.up)

    pre = _step(state, "pre_facts_failed", lambda: ports.snapshot("pre"))
    _split_check(ports, evidence, state, "pre")
    _split_probe(ports, evidence, state, "pre", pre)
    token = _step(state, "dns_token_failed", ports.dns_token)

    window_start = clock.now()
    state.events = _step(state, "window_events_failed", lambda: ports.start_events(
        _event_time(window_start - timedelta(seconds=spec.events_lookback_seconds))))
    state.watch = _step(state, "window_watch_failed", ports.start_host_watch)
    pre_board = _step(state, "pre_scoreboard_failed", lambda: ports.scoreboard("pre", pre))
    if _step(state, "meter_read_failed", meter.forwarded) != 0:
        raise RunFailure("pre_requests_nonzero")

    # 제품 사용자 순서: 로그인~초안까지는 준비, 예산 시계는 B의 작업 생성 직전과 같은 뜻으로 시작 요청 직전부터 잰다.
    prepared = _step(state, "product_prepare_failed", lambda: ports.prepare_round(token))
    requested_at, started = _stamp(clock), clock.monotonic()
    running = _step(state, "product_start_failed", lambda: ports.start_round(prepared))
    task_id, draft_id = running["task_id"], running["draft_id"]
    _step(state, "evidence_write_failed", lambda: evidence.write_json("task.json", {
        "format": SPLIT_TASK_FORMAT, "draft_id": draft_id, "task_id": task_id, "target": prepared["target"],
        "catalog_profile": prepared["catalog_profile"], "requested_at": requested_at, "created_at": _stamp(clock)}))
    watch = product_end_watch(ports, _StatusReader(ports), draft_id, task_id, clock, started, spec.task_status_wait_seconds)
    stop_reason, forwarded_at_stop = _wait_budget(spec, meter, clock, state, started, watch)
    stop_elapsed = clock.monotonic() - started
    _step(state, "seal_failed", lambda: ports.seal(pre["services"][METER]["id"]))
    drained = _wait_drained(spec, meter, clock, state)
    # 강제 취소된 요청이 있으면 B와 같은 공통 조건(run_drain.settle_seconds)만큼 기다린 뒤 관측한다(runner.py와 같은 규칙).
    settled = spec.post_settle_seconds if drained["forced_cancelled_requests"] else 0
    if settled:
        clock.sleep(settled)
    # 계측기 기록 마감 대조는 B와 같은 함수·같은 사유다(경로 기록 필수·키 지문·전달 요청과 한 행씩, 2026-11-18 결정 (a)).
    meter_rows = _step(state, "meter_read_failed", meter.engine_rows)
    check_meter_logs(meter, state, meter_rows, drained, spec.path_key_ref)
    # B는 drain 뒤 게이트웨이를 봉인해 새 모델 요청을 막는다(#634). O는 같은 자리에서 제품 회차를 멈춘다. 러너 예산으로 먼저
    # 끝난 회차는 제품이 아직 실행 중이라 실행 기록(마감 때 저장)이 없고 모델 호출이 계속된다. 끝 상태·drain 완료를 본 뒤에야
    # 실행 기록·장부·호출 기록을 읽는다(#665 리뷰).
    _step(state, "product_stop_failed", lambda: ports.stop_round(draft_id))
    closed = _step(state, "product_close_failed", lambda: ports.wait_closed(draft_id, spec.product_close_seconds))

    post = _step(state, "post_facts_failed", lambda: ports.snapshot("post"))
    # pre~post 사이 데몬·망·구성원·주소·바인딩·컨테이너 교체·재시작(B와 같은 compare_snapshots, #668 리뷰). 배치 검사는
    # 기대 배치와의 일치만 보므로 같은 이미지·주소의 새 컨테이너로 바뀐 것은 이 대조가 잡는다.
    if compare_snapshots(pre, post):
        raise RunFailure("boundary_changed")
    # 사후 점검은 경계의 두 입구·netns가 살아 있어야 한다(#653 기대값). 그래서 경계 정지는 post 관측·점검 뒤, 구간이 끝나기
    # 전이다. 경계는 SIGTERM 정지 한 번으로 봉인 표식까지 쓰고 끝난다(계약 §4.1 결정 B). 구간 감시가 그 정지만 허용한다.
    _split_check(ports, evidence, state, "post")
    _split_probe(ports, evidence, state, "post", post)
    post_board = _step(state, "post_scoreboard_failed", lambda: ports.scoreboard("post", post))
    _step(state, "boundary_stop_failed", lambda: ports.stop_boundary(pre["services"][BOUNDARY]["id"]))
    window_end = clock.now()
    stopped = _step(state, "window_watch_failed", lambda: _stop_watch(state))
    streamed = _step(state, "window_events_failed", lambda: _stop_events(state, _ns(window_end)))
    window = _split_window(spec, evidence, state, pre, window_start, window_end, stopped, streamed)

    engine_end = watch.record()
    _step(state, "evidence_write_failed", lambda: evidence.write_json(PRODUCT_END_NAME, engine_end))
    limit = first_limit(stop_reason, stop_elapsed, spec.time_budget_seconds, engine_end["first_terminal"])

    record = _step(state, "product_run_record_failed", lambda: ports.run_record(task_id))
    goal = record.get("goal") if isinstance(record, dict) else None
    if not isinstance(goal, dict) or not isinstance(goal.get("common_sha256"), str):
        raise RunFailure("goal_digest_missing")  # #657 이전 제품 빌드이거나 실행 기록이 목표 SHA를 잃었다
    try:
        usage = product_model_usage(record)
    except ProductLLMError as exc:
        raise RunFailure(str(exc)) from None

    # 제품 블록 근거(결정안 §3.3): 그 회차에 제품이 쓴 E② 정책의 내용 지문·상한 스냅숏과 설치·실행 파일 원본 SHA.
    # 제품을 멈춘 뒤라 파일이 더 바뀌지 않는다. 정책·실행 조건은 이 초안·작업에 결속돼야 한다(#693 리뷰). 내용 지문이 비면
    # (자격 pin 규칙 등) 짝 대조 칸을 만들 수 없어 무효다.
    policy = checked_control_policy(_step(state, "control_policy_failed", lambda: ports.control_policy(task_id)), draft_id)
    fingerprint = policy["policy_fingerprint_sha256"]
    execution = diagnosis_entered(record, draft_id, task_id, policy["policy_revision"])
    entered = execution is not None
    files = dict(_step(state, "product_files_failed", ports.product_files), control_limits=digest(policy["limits"]))
    # 진단에 들어가지 않은 회차(탐색 중 예산 소진 등)는 실행 조건 파일이 없다. 그때만 null로 남기고 first_limit은 그대로다.
    # 진단에 들어간 회차에서 빠지거나 다른 회차의 것이면 실패다.
    files["runtime_conditions"] = checked_runtime_conditions(
        _step(state, "runtime_conditions_failed", lambda: ports.runtime_conditions(draft_id)), draft_id, task_id,
        policy["policy_revision"], execution) if entered else None
    _step(state, "evidence_write_failed", lambda: evidence.write_json(PRODUCT_FILES_NAME, {
        "format": PRODUCT_FILES_FORMAT, "file_sha256": files, "diagnosis_entered": entered, "diagnosis_policy_sha256": fingerprint,
        "policy_ref": policy["policy_ref"], "policy_revision": policy["policy_revision"], "limits": policy["limits"]}))
    _step(state, "boundary_evidence_copy_failed", ports.copy_boundary_evidence)
    ledger, calls = _step(state, "product_records_copy_failed", ports.copy_product_records)
    record_reasons = call_records_reasons(ledger, calls)
    _step(state, "evidence_write_failed", lambda: evidence.write_json("product-model.json", {
        "usage": usage, "call_records": {"reasons": record_reasons}}))
    if record_reasons:
        raise RunFailure(record_reasons[0])
    # 제품 호출 기록을 B 게이트웨이와 같은 조회 코드로 사후 검증하고(eval-meter -role generation-verify) B와 같은 규칙으로
    # 판정한다(결정안 D(b), 4-11). 샘플링은 제품이 실제로 보낸 값을 표식에 싣고 B 게이트웨이 적용값(선언)과 대조한다.
    _step(state, "llm_verify_failed", lambda: ports.generation_verify(calls, usage["sampling"]))
    verified = _step(state, "llm_read_failed", ports.llm_verified)
    if verified is None:
        raise RunFailure("llm_unverified")
    llm_reasons, llm_summary = _step(state, "llm_marker_invalid", lambda: judge_product_llm(
        verified, spec.llm_sampling, spec.llm_provider_slug, spec.llm_provider_name))
    _step(state, "evidence_write_failed", lambda: evidence.write_json("llm-summary.json", dict(llm_summary, reasons=llm_reasons)))
    if llm_reasons:
        raise RunFailure(llm_reasons[0])
    state.summary.update(stop_reason=stop_reason, forwarded_at_stop=forwarded_at_stop, first_limit=limit,
                         drained=drained, post_settle_seconds=spec.post_settle_seconds, post_settle_applied_seconds=settled,
                         pre_solved=pre_board.get("solved"), post_solved=post_board.get("solved"),
                         goal_common_sha256=goal["common_sha256"], window_reasons=window["reasons"],
                         sampling_overwrites=usage["sampling_overwrites"], task_id=task_id, draft_id=draft_id,
                         product_file_sha256=files, diagnosis_policy_sha256=fingerprint, diagnosis_entered=entered,
                         product_closed={name: closed.get(name) for name in PRODUCT_END_FIELDS})
    if spec.preservation is not None:
        # 보존 sidecar v3 O 칸(결정안 §3.3). 해시는 내린 뒤 build_payload_v3가 최종 바이트로 뜬다(경계 원자료 검증 포함).
        conditions = _step(state, "split_conditions_failed", ports.split_conditions)
        state.preservation = _preservation_v3(spec, state, pre_board["state_sha256"], stop_reason, limit,
                                              engine_end["first_terminal"], llm_summary, files, fingerprint, conditions,
                                              usage["sampling_overwrites"], ledger_reservations(ledger), call_record_lines(calls),
                                              task_id)


def _preservation_v3(spec: SplitRunSpec, state: _SplitState, initial_state: str, stop_reason: str, limit: dict,
                     first_terminal: dict | None, llm_summary: dict, files: dict, fingerprint: str, conditions: dict,
                     overwrites: dict, reservations: int, lines: int, task_id: str) -> dict:
    """O 회차의 v3 칸(공통·쪽별·회차별·제품 블록). 공통 칸은 B(runner._preservation_v3)와 같은 정의로 뜬다."""
    options, surface = spec.preservation, state.summary["tool_surface"]
    common = {"request_budget": spec.request_budget, "time_budget_seconds": spec.time_budget_seconds,
              "model_pin_sha256": v3f.model_pin_sha256(backend="api", model=spec.model_tag, provider_slug=spec.llm_provider_slug,
                                                       provider_name=spec.llm_provider_name,
                                                       sampling=llm_summary["sampling_applied"], serving=None),
              "target_image_id": spec.image_ids["target"], "initial_state_sha256": initial_state,
              "machine_sha256": digest(state.summary["host"]),
              "runner_settings_sha256": v3f.settings_sha256(vars(spec), v3f.COMMON_RUNNER_SETTINGS),
              "goal_common_sha256": state.summary["goal_common_sha256"], "comm_mode": options["comm_mode"],
              "mcp_version": surface["mcp_version"], "mcp_raw_tools_sha256": surface["mcp_raw_sha256"]}
    side = {"layout": "exploration-split/v1", "driver_sha256": v3f.driver_sha256(v3f.O_DRIVER_FILES),
            "driver_settings_sha256": v3f.settings_sha256(spec.driver_settings, v3f.O_DRIVER_SETTINGS),
            "write_mode": options["write_mode"], "declared_model_budget_sha256": spec.model_budget_sha256,
            "browser_image_id": spec.image_ids["explore_browser"], "exploration_policy_sha256": spec.exploration_policy_sha256,
            "diagnosis_policy_sha256": fingerprint, "cli_surface_sha256": surface["cli_sha256"],
            "mcp_exposed_tools_sha256": surface["mcp_exposed_sha256"]}
    run = {"stop_reason": stop_reason, "first_limit": limit, "end_signal": v3f.end_signal(first_terminal, product=True),
           "rate_limited": llm_summary.get("rate_limited")}
    product = {"source_sha": spec.product_source_sha,
               "image_ids": {name: spec.image_ids[name] for name in ("product", "explore_browser", "fixture")},
               "file_sha256": dict(files), "split_conditions": conditions, "sampling_overwrites": overwrites,
               "call_records": {"reservations": reservations, "lines": lines}}
    return dict(series_ref=options["series_ref"], execution_source=options["execution_source"], run_id=spec.run_id, side="ours",
                repeat_index=spec.repeat_index, engine_source_sha=spec.product_source_sha,
                engine_image_id=spec.image_ids["product"], common=common, side_conditions=side, run=run, product=product,
                boundary={"task_id": task_id, "path_key_ref": spec.path_key_ref}, purpose=spec.purpose)


def execute_split_run(spec: SplitRunSpec, ports: SplitRunPorts, meter: MeterFiles, evidence, clock: Clock) -> dict:
    """O 회차 1회. 실패해도 감시를 멈추고, 띄웠으면 내리고, 보호 입력은 늘 지운다. 결과 기록을 정확히 하나 쓴다.

    서명·보존 sidecar v3·경계 원자료 검증은 다음 단계(4-9)다. 여기 결과는 서명 전 요약이다.
    """
    state = _SplitState()
    started_at = _stamp(clock)
    reasons: list[str] = []
    interrupted = False
    try:
        _run_split_steps(spec, ports, meter, evidence, clock, state)
    except RunFailure as exc:
        reasons.append(exc.code)
        reasons.extend(exc.also)
        state.step = exc.code
    except KeyboardInterrupt as exc:
        # 운영자 중단도 결과 기록 한 건(무효)을 남기고, 정리 뒤 중단 신호를 다시 올린다(execute_run과 같다). 중단 중 복사
        # 도우미 정리까지 실패했으면 그 사유를 따로 남긴다(#693 리뷰).
        reasons.append("interrupted")
        if isinstance(exc, HelperCleanupInterrupt):
            reasons.append("docker_helper_cleanup_failed")
        interrupted = True
    except Exception:
        reasons.append("unexpected_error")
    finally:
        # 실패로 끝난 회차에서도 감시 스레드·이벤트 스트림을 남기지 않는다. 이미 무효 사유가 있으면 정리 실패는 사유를 바꾸지 않는다.
        for stop in (lambda: _stop_watch(state) if state.watch is not None else None,
                     lambda: _stop_events(state, 0) if state.events is not None else None):
            try:
                stop()
            except Exception:
                if not reasons:
                    reasons.append("window_stop_failed")
        if state.up_attempted:
            try:
                ports.down()
            except Exception:
                reasons.append("down_failed")
        try:
            ports.remove_inputs()  # 비밀(설치 입력·비밀번호·CA 키)은 회차가 어떻게 끝나도 남기지 않는다
        except Exception:
            reasons.append("inputs_cleanup_failed")
    if not reasons and state.preservation is not None:
        # 내린 뒤에는 계측기 writer가 없다. 최종 바이트로 hash를 뜨고 경계 원자료(계약 §4.1)를 계측기 행과 대조한 뒤 서명한다.
        try:
            parts = dict(state.preservation)
            parts["boundary"] = dict(parts["boundary"], directory=ports.boundary_directory())
            payload = _step(state, "preservation_record_failed", lambda: preservation_payload_v3(meter=meter, **parts))
            signed = _step(state, "preservation_sign_failed", lambda: ports.sign(payload))
            # 저장 실패(이미 있는 파일·쓰기 실패)도 고정 사유로 남기고 결과 기록까지 간다(#710 리뷰)
            _step(state, "preservation_write_failed", lambda: evidence.write_json(PRESERVATION_NAME, signed))
        except RunFailure as exc:
            reasons.append(exc.code)
            reasons.extend(exc.also)
    base = {"format": SPLIT_RESULT_FORMAT, "run_id": spec.run_id, "side": "ours", "layout": "exploration-split/v1",
            "project": spec.project, "started_at": started_at, "finished_at": _stamp(clock)}
    if reasons:
        result = dict(base, status=INVALID, reasons=reasons, failed_step=state.step)
    else:
        result = dict(base, status=COMPLETE, images=dict(spec.image_ids), **state.summary)
    evidence.write_json(RESULT_NAME, result)
    if interrupted:
        raise KeyboardInterrupt
    return result
