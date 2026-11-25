"""탐색 분리 배치(개선 후 O) 회차 흐름 부품(탐색 분리 배치 결정안 §3·§3.1, 순서 4-7c-3).

O의 엔진 끝 신호는 엔진 작업 목록이 아니라 제품 초안 상태다(결정안 §3.1: 제품 실행 상태와 결과·마감 사유 칸). 러너는
B와 같은 감시 규칙(runner._EngineEndWatch: 창 동안 백그라운드 읽기, 첫 끝 상태, `down` 전 마지막 읽기, 읽기 실패는 셈만)을
제품 상태 읽기에 그대로 쓴다. 제품이 예산 마감(budget_limited)으로 닫히면 first_limit이 `product_limit:budget_limited`가 된다
(runner.first_limit).
"""

from __future__ import annotations

from runner import Clock, _EngineEndWatch
from runner_engine import EngineAPIError
from runner_product import DRAFT_TERMINAL, ProductAPIError, ProductDriver

PRODUCT_END_FORMAT = "abc2lab-eval-runner-product-end-v1"
# 제품 초안 상태에서 서명 근거로 남길 칸(화면 파서 parseURLJob과 같은 이름). 끝 상태가 아니면 비어 있는 칸은 null이다.
PRODUCT_END_FIELDS = ("state", "phase", "reason_code", "outcome", "closing_reason", "drain_complete", "result_ready",
                      "intent_outcomes", "diagnostic_log_incomplete")


def product_status_view(driver: ProductDriver, draft_id: str) -> dict:
    """제품 초안 상태에서 끝 신호 칸만 꺼낸다. 없는 칸은 null(값을 만들지 않는다)."""
    draft = driver.status(draft_id)
    return {name: draft.get(name) for name in PRODUCT_END_FIELDS}


def product_drained(view: dict | None) -> bool:
    """O 끝 신호 뒤 무활동 유예의 조건: 제품이 회차를 정상 완료(completed)로 닫고 정리(drain)를 끝냈다고 알린 상태다.
    O 모델 호출은 창 동안 러너가 볼 수 없어(호출 기록은 창 뒤 복사) 제품의 정리 끝 표시를 진행 중 호출이 없다는 근거로 쓴다."""
    return isinstance(view, dict) and view.get("state") == "completed" and view.get("drain_complete") is True


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
    CLI_LINES_NAME, CLI_USAGE_NAME, INVALID, RECORD_NAME, RESULT_FORMAT, RESULT_NAME, RunFailure, _check_subnets, check_request_cap, _event_time, _meter_preflight,
    _ns, _stamp, _step, _stop_events, _stop_watch,
    _wait_budget, _wait_drained, check_meter_logs, first_limit,
    END_IDLE_NAME, STOP_ENGINE_IDLE, _EndIdle, end_idle_reason, inflight_at_stop,
)
from preservation_record import (  # noqa: E402
    LLM_NOT_APPLICABLE, LLM_VERIFIED, NAME as PRESERVATION_NAME, build_payload_v3 as preservation_payload_v3, digest,
)
import preservation_v3_fields as v3f  # noqa: E402
from runner_checks import (  # noqa: E402
    check_host_listeners, compare_snapshots, listeners_outside_loopback, policy_sha256,
)
from runner_record import (  # noqa: E402
    RecordContext, RecordError, canonical_sha256, descriptor, evidence_rows, isolation_evidence, isolation_proof, path_ref, run_record,
    solve_events, timeline,
)
from runner_docker import HelperCleanupError, HelperCleanupInterrupt  # noqa: E402
from runner_e2v3 import EVIDENCE_NAME as E2V3_EVIDENCE_NAME, E2V3Invalid  # noqa: E402
from runner_e3_traces import EVIDENCE_NAME as E3_TRACES_NAME, FORMAT as E3_TRACES_FORMAT, traces as e3_traces  # noqa: E402
from cli_usage import OBSERVED_LINES_FORMAT, count_usage, not_found_calls, observed_lines, product_command_record  # noqa: E402
from isolation_proof import E3_STATUSES  # noqa: E402
from run_record import OFFICIAL_E3_DISABLED_REASON  # noqa: E402
from runner_evidence import MeterFiles, forwarded_count  # noqa: E402
from runner_fault import (  # noqa: E402
    FAULT_STALL_FORMAT, FAULT_STALL_NAME, FaultRecordError, fault_stall_outcome, fault_stall_record,
)
from runner_llm import interrupted_generation_ids, judge_product_llm  # noqa: E402
from runner_probe import PASS  # noqa: E402
from runner_product_llm import (  # noqa: E402
    ProductLLMError, call_record_lines, call_records_reasons, ledger_reservations, ledger_totals, product_model_usage,
)
from runner_early_end import (  # noqa: E402
    CONFIG_END_REASON, EARLY_END_NAME, ENGINE_ENDED_PREFIX, PRODUCT_BUDGET_LIMIT, PROVIDER_FAULT_END_REASON, approval_budget,
    early_end_record,
    start_refusal_code, start_refusal_record,
)
from runner_split_probe import SplitProbeOutcome  # noqa: E402
from runner_window import events_reasons, judge_events, watch_reasons, watch_summary  # noqa: E402

# O 결과도 B와 같은 결과 형식이다(세션 집계·채점기가 같은 규칙으로 읽는다). 배치는 `layout`·`e2_mode`로 가른다.
SPLIT_RESULT_FORMAT = RESULT_FORMAT
LAYOUT = "exploration-split/v1"
# O의 E② 자리(설계안 §4): 제품 안 E②. 건수 칸은 모두 null이고 시도 대조는 E② 영수증·저널 v3 평가 내보내기 전까지 미수집이다.
E2_SPLIT_PRODUCT = "split_product"
OBS = "eval-obs"
PREPARATION = "preparation"
# O 판정 내보내기는 지금 없다(2026-11-19 사용자 결정: 공식 측정은 E③ 비활성이라 제품 판정 0건). records=null(판정 미수집).
VERDICT_NOT_COLLECTED = {"status": "not_collected", "reason": "product_verdict_export_unavailable"}
SPLIT_TASK_FORMAT = "abc2lab-eval-runner-split-task-v1"
PRODUCT_END_NAME = "product-end.json"
# 제품 작업의 끝 신호 칸(B engine-end.json의 작업 칸과 같은 6칸). 제품 회차가 왜 마감했는지(목표 달성 여부)를 가르는 근거다.
PRODUCT_TASK_END_NAME = "product-task-end.json"
# v2: 제품 DB에서 읽는다(상태·완료 시각·탐색 번호·목표/의도 상태별 수). v1(제품 API)은 관리 API가 중계하지 않아 늘 실패였다.
PRODUCT_TASK_END_FORMAT = "abc2lab-eval-runner-product-task-end-v2"
TOOL_SURFACE_NAME = "tool-surface.json"
PRODUCT_FILES_NAME = "product-files.json"
PRODUCT_FILES_FORMAT = "abc2lab-eval-runner-product-files-v1"
# 제어 정책 보기 상한 칸(url_live_views.go controlPolicyLimits). 제품 검증(e2proxy execution.go)은 모두 양수만 받는다.
CONTROL_LIMIT_FIELDS = ("max_requests", "max_attempts", "max_concurrent", "max_body_bytes", "max_response_body_bytes",
                        "request_timeout_ms")
# 확정 단계(E③) 몫 칸(회차 시작 때 E②가 떼어 두는 요청·시간). 이름은 E③ 판정 쪽 PR(#698 4b)의 제안이고, 확정되면 맞춘다.
# 제품 회차 정책이면 0이어도 늘 싣는다(제안). 칸이 없는 빌드는 "몫 0 확인 안 됨"이다(0과 같다고 보지 않는다).
E3_SHARE_FIELDS = ("e3_confirmation_requests", "e3_confirmation_timeout_ms")
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
    share = [name for name in E3_SHARE_FIELDS if isinstance(limits, dict) and name in limits]
    if (not isinstance(limits, dict) or set(limits) - set(share) != set(CONTROL_LIMIT_FIELDS) or share not in ([], list(E3_SHARE_FIELDS))
            or any(type(limits[name]) is not int or not 1 <= limits[name] <= MAX_INT64 for name in CONTROL_LIMIT_FIELDS)
            or any(type(limits[name]) is not int or not 0 <= limits[name] <= MAX_INT64 for name in share)):
        raise RunFailure("control_policy_limits_invalid")
    kept = CONTROL_LIMIT_FIELDS + tuple(share)
    return {"policy_ref": view["policy_ref"], "policy_revision": view["policy_revision"],
            "policy_fingerprint_sha256": view["policy_fingerprint_sha256"], "limits": {name: limits[name] for name in kept}}


def check_official_e3_share(limits: dict) -> None:
    """공식 측정 E③ 비활성(평가 프로토콜 §5.1): 그 회차 E② 정책의 확정 몫이 0이어야 한다. 칸이 없으면 확인하지 못한 것이다.
    O 회차 흐름이 회차 목적이 공식(official)일 때 checked_control_policy 결과의 limits로 부른다."""
    if any(name not in limits for name in E3_SHARE_FIELDS):
        raise RunFailure("e3_share_unconfirmed")
    if any(limits[name] != 0 for name in E3_SHARE_FIELDS):
        raise RunFailure("e3_share_not_zero")


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


def checked_runtime_conditions(raw: bytes, draft_id: str, task_id: str, policy_revision: str, execution: dict) -> tuple[str, int]:
    """회차 실행 조건(e2_http_factory.go)이 이 초안·작업·정책 개정의 것인지 확인하고 (원본 바이트 SHA, 실제 Worker 수)를
    돌려준다. Worker 수는 제품이 그 회차에 결속한 값이다(외부 입력이 아님, e2_http_runtime.go)."""
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
    return hashlib.sha256(raw).hexdigest(), value["worker_count"]
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
    # 합성 모델 모드(평가 overlay 전용). 참이면 OpenRouter 장부·호출 기록 대조와 사후 검증이 '해당 없음(합성)'이다.
    synthetic: bool = False
    scripted_plan_sha256: str = ""  # 합성 모드에서 스크립트 모델이 읽은 계획 사본의 SHA
    fault_stall_seconds: int | None = None  # 멈춤 무효 판정 T(초, B와 같은 run.fault_stall_seconds). None이면 판정 안 함
    e2_v3: bool = False  # 설정 split.e2_v3가 있으면 E② v3 저널·봉인 head를 꺼내 검증한다(runner_e2v3)
    # B 전용 CLI 이름(split.cli_usage_names). 있으면 O 제품 활동의 Bash 호출에서 B와 같은 함수로 관측·이름 못 찾음 수를 센다.
    cli_usage_names: tuple[str, ...] = ()
    # v1.1 러너 기록(설계안 §2·§5). B(build_spec)와 같은 선언·설정 칸이다. 비면 기록을 만들지 않고 회차가 무효다.
    condition: str = ""
    fixture_fingerprint: str = ""
    environment_fingerprint: str = ""
    target_origin: str = ""  # 선언 target_origin(B 분리 배치 회차 증명과 같은 문자열)
    observer_path: str = ""
    observer_readonly_ref: str = ""
    allowed_listens: frozenset = frozenset()  # loopback 밖 호스트 리슨 허용(설정 허용 목록, B allowed_host_listens의 api 경우)
    # 화면 관찰 로그인 파일(split.observer_password_file)을 썼는지. 결과에 예/아니오만 남긴다(경로·값 없음, 설정 지문 밖).
    # 공식 O 회차는 모두 관찰하거나 모두 관찰하지 않는다(공식 회차 절차 6절, 2026-11-20 사용자).
    observer_login_file: bool = False
    # E③ 비활성 요구(설정 split.e3_disabled_required). 켜면 그 회차 정책의 확정 몫 0과 기록 disabled/declaration_missing을
    # 확인하고 회차 조건(서명 근거)에 남긴다. purpose와 따로다(2026-11-20 사용자).
    e3_disabled_required: bool = False
    # 승인 요청·비용 상한(manifest). 승인 예산에 닿은 budget_limited를 설정 오류로 가른다(고정 초안 §26). 합성 모델 모드는 None.
    approval_caps: tuple[int, int, int, int] | None = None


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
    def host_listeners(self) -> frozenset: ...
    def prepare_round(self, dns_token: str) -> dict: ...
    def start_round(self, prepared: dict) -> dict: ...
    def product_status(self, draft_id: str) -> dict: ...
    def stop_round(self, draft_id: str) -> dict: ...
    def wait_closed(self, draft_id: str, deadline_seconds: float) -> dict: ...
    def background(self, work) -> None: ...
    def seal(self, meter_container_id: str) -> None: ...
    def stop_boundary(self, boundary_container_id: str) -> None: ...
    def run_record(self, task_id: str) -> dict: ...
    def task_signal(self, task_id: str) -> dict: ...
    def task_exploration_id(self, task_id: str) -> int: ...
    def product_commands(self, exploration_id: int) -> list[dict]: ...
    def workflow_snapshot(self, draft_id: str) -> dict: ...
    def control_policy(self, task_id: str) -> dict: ...
    def product_files(self) -> dict: ...
    def runtime_conditions(self, draft_id: str) -> bytes: ...
    def e3_confirmation(self, draft_id: str) -> bytes: ...
    def e3_trace_sources(self, draft_id: str) -> dict: ...
    def tool_surface(self) -> dict: ...
    def split_conditions(self) -> dict: ...
    def boundary_directory(self): ...
    def sign(self, payload: dict) -> dict: ...
    def copy_boundary_evidence(self) -> None: ...
    def copy_product_records(self) -> tuple: ...
    def e2_v3_export(self, draft_id: str) -> tuple: ...
    def e2_v3_collect(self, journal: bytes | None, head: bytes | None) -> dict: ...
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
    probes: dict = field(default_factory=dict)  # 단계별 점검 행(출발점 이름을 행 ID 앞에 붙인 평면 목록)
    listeners: dict = field(default_factory=dict)  # 단계별 loopback 밖 호스트 리슨
    observer_calls: int = 0  # 러너가 관측기로 보낸 GET 수(pre·post 점수판)
    record: dict | None = None  # 서명 전 v1.1 러너 기록(내린 뒤 서명)
    task_request_sha256: str | None = None


class _StatusReader:
    """product_end_watch가 부르는 driver.status 자리(포트의 제품 상태 읽기)."""

    def __init__(self, ports: SplitRunPorts):
        self.ports = ports

    def status(self, draft_id: str) -> dict:
        return self.ports.product_status(draft_id)


def _split_check(spec: SplitRunSpec, ports: SplitRunPorts, evidence, state: _SplitState, phase: str) -> None:
    """배치 검사(망·publish·고정 주소·이미지·netns 공유·회차 밖 publish)와 호스트 리슨(B _check_phase와 같은 규칙). 구간 이전
    상태는 이벤트 감시로 안 잡히므로 pre/post에서 본다(#668 리뷰). 위반이면 무효. 리슨은 격리 근거 host_condition 행이 된다."""
    result = _step(state, f"{phase}_checks_failed", lambda: ports.check(phase))
    listeners = _step(state, f"{phase}_host_listeners_failed", ports.host_listeners)
    state.listeners[phase] = listeners_outside_loopback(listeners)
    host = check_host_listeners(listeners, spec.allowed_listens)
    _step(state, "evidence_write_failed", lambda: evidence.write_json(f"{phase}-checks.json", dict(result, host_violations=host)))
    if result["violations"]:
        raise RunFailure(f"{phase}_layout_violation")
    if host:
        raise RunFailure(f"{phase}_host_condition_violation")


def _split_probe(ports: SplitRunPorts, evidence, state: _SplitState, phase: str, snapshot: dict) -> None:
    outcome = _step(state, f"{phase}_probe_invalid", lambda: ports.probes(phase, snapshot))
    state.runner_containers.update(outcome.container_ids)
    # 격리 근거 connectivity_check 행(설계안 §4): 출발점마다 같은 경로 ID가 있으므로 출발점 이름을 앞에 붙인다.
    state.probes[phase] = [dict(row, id=f"{origin}/{row['id']}") for origin, o in sorted(outcome.origins.items())
                           for row in o.rows]
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


DISCOVERY_SOURCES_FORMAT = "abc2lab-eval-runner-discovery-sources-v1"
DISCOVERY_SOURCES_NAME = "discovery-sources.json"
_SOURCE_NAME = re.compile(r"[a-z][a-z0-9_]{0,63}\Z")
# 구동기 오류 글의 앞부분(`product_http_503` 등). 뒤의 경로·초안 ID·제품 사유는 근거에 남기지 않는다(#725 리뷰).
_PRODUCT_CODE = re.compile(r"product_[a-z0-9_]{1,62}\Z")
# 제품 구동·작업 목록 해석의 고정 오류 코드(예: product_http_403:tasks, engine_task_status_missing). 그 밖은 일반 코드로.
_TASK_ERROR = re.compile(r"(product|engine)_[a-z0-9_]{1,62}(:[a-z0-9_./-]{1,64}){0,3}\Z")


def discovery_sources(snapshot: dict) -> dict:
    """O 회차 끝 워크플로 snapshot의 끝점 발견 출처별 건수(고정 초안 §8: MCP 출처 발견 비중). 출처 이름(프로그램 어휘)과
    건수만 남기고 경로·값은 남기지 않는다. 브라우저(MCP) 출처는 `browser_*`, API Recon은 `api_recon_*`이다. 목록이 공개 투영
    상한으로 잘렸으면(`counts.omitted`) 그 수도 같이 남긴다(잘린 끝점의 출처는 모른다)."""
    by_source: dict[str, int] = {}
    for endpoint in snapshot["endpoints"]:
        source = endpoint.get("source") if isinstance(endpoint, dict) else None
        name = source if isinstance(source, str) and _SOURCE_NAME.fullmatch(source) else "invalid"
        by_source[name] = by_source.get(name, 0) + 1
    # 제품은 counts.omitted를 omitempty로 낸다(diagnosisflow.Counts): 칸이 없으면 빠진 끝점이 없다는 뜻이라 0이다(#725 리뷰).
    # counts 자체가 객체가 아니거나 칸이 정수가 아니거나 음수면 모른다(null).
    counts = snapshot.get("counts")
    if isinstance(counts, dict):
        omitted = counts.get("omitted", 0)
        omitted = omitted if type(omitted) is int and omitted >= 0 else None
    else:
        omitted = None
    return {"format": DISCOVERY_SOURCES_FORMAT, "endpoints": len(snapshot["endpoints"]), "by_source": dict(sorted(by_source.items())),
            "browser": sum(n for name, n in by_source.items() if name.startswith("browser_")),
            "api_recon": sum(n for name, n in by_source.items() if name.startswith("api_recon_")),
            "omitted": omitted,
            "detail_truncated": snapshot.get("detail_truncated") is True}


def _discovery_sources(ports, draft_id: str) -> dict:
    """기록만 한다(회차 유효성에 쓰지 않음). 읽지 못하면 고정 코드만 남긴다."""
    try:
        return discovery_sources(ports.workflow_snapshot(draft_id))
    except ProductAPIError as exc:
        code = str(exc).split(":", 1)[0]
        return {"format": DISCOVERY_SOURCES_FORMAT, "error": code if _PRODUCT_CODE.fullmatch(code) else "discovery_sources_failed"}
    except Exception:
        return {"format": DISCOVERY_SOURCES_FORMAT, "error": "discovery_sources_failed"}


def _collect_e2_v3(ports, evidence, state, draft_id: str) -> None:
    """E② v3 저널과 신뢰 저장소의 봉인된 최종 head를 꺼내 검증한다. 제품이 안 냈거나 무결성·현재성 확인에 실패하면 회차는
    유효하고 통제 대조만 미수집이다(고정 초안 §20). 러너 쪽 장애(키 파일·사본·검증기 실행)는 평가 환경 원인이라 무효다."""
    journal, head = _step(state, "e2_v3_export_failed", lambda: ports.e2_v3_export(draft_id))
    state.step = "e2_v3_collect_failed"
    try:
        row = ports.e2_v3_collect(journal, head)
    except E2V3Invalid as exc:
        raise RunFailure("e2_v3_collect_failed", (exc.code,)) from None
    except Exception:
        raise RunFailure("e2_v3_collect_failed") from None
    _step(state, "evidence_write_failed", lambda: evidence.write_json(E2V3_EVIDENCE_NAME, row))
def _product_cli_usage(names: tuple[str, ...], ports, evidence, state, task_id: str) -> None:
    """O 제품 활동의 Bash 호출에서 B 전용 CLI 이름별 관측(B와 같은 count_usage·observed_lines)과, O에 없는 이름이라 찾지 못한
    호출 수(not_found)를 남긴다. 원문은 메모리에서만 보고 모양(이름·연산자만)만 쓴다. 이름과 상관없이 셸이 명령을 찾지 못한
    호출 수(not_found_calls)도 같이 둔다. B 명령 기록 읽기 실패처럼 회차를 무효로 한다(cli_usage_failed)."""
    exploration_id = _step(state, "cli_usage_failed", lambda: ports.task_exploration_id(task_id))
    rows = _step(state, "cli_usage_failed", lambda: ports.product_commands(exploration_id))
    bash = _step(state, "cli_usage_failed", lambda: [product_command_record(row) for row in rows])
    usage = _step(state, "cli_usage_failed", lambda: dict(count_usage(bash, names, with_failures=True),
                                                          exploration_id=exploration_id, not_found_calls=not_found_calls(bash)))
    if usage["names_seen"]:
        picked, observed = _step(state, "cli_usage_failed", lambda: observed_lines(bash, names))
        _step(state, "evidence_write_failed", lambda: evidence.write_json(CLI_LINES_NAME, {
            "format": OBSERVED_LINES_FORMAT, "observed_calls": observed, "lines": picked}))
        usage["observed_lines"] = len(picked)
    _step(state, "evidence_write_failed", lambda: evidence.write_json(CLI_USAGE_NAME, usage))


def _task_end(ports, task_id: str) -> dict:
    try:
        return {"format": PRODUCT_TASK_END_FORMAT, "task_id": task_id, "task": ports.task_signal(task_id)}
    except (ProductAPIError, EngineAPIError, ValueError) as exc:
        code = str(exc)
        return {"format": PRODUCT_TASK_END_FORMAT, "task_id": task_id,
                "error": code if _TASK_ERROR.fullmatch(code) else "product_task_signal_failed"}
    except Exception:
        return {"format": PRODUCT_TASK_END_FORMAT, "task_id": task_id, "error": "product_task_signal_failed"}


def product_fault_stall(calls: bytes, meter_rows: list[dict], window_start: str, window_end: str, engine_end: dict) -> dict:
    """O 회차의 제공자 장애 멈춤 판정 재료. 제품 호출 기록 줄을 게이트웨이 행 자리에 넣는다(칸·어휘가 같다, #629).
    B(runner._fault_stall)처럼 읽지 못하면 고정 코드만 남기고, T가 설정된 회차면 fault_stall_outcome이 판정 불가로 무효로 한다."""
    terminal = (engine_end.get("first_terminal") or {}).get("at")
    try:
        # 객체가 아닌 줄·빠진 칸은 fault_stall_record 안에서 TypeError·KeyError로 끝나 같은 고정 코드가 된다
        rows = [json.loads(line) for line in calls.decode("utf-8").splitlines() if line]
        return fault_stall_record(rows, meter_rows, window_start, window_end, terminal)
    except (UnicodeDecodeError, ValueError, KeyError, TypeError) as exc:
        code = str(exc) if isinstance(exc, FaultRecordError) else "fault_stall_rows_invalid"
        return {"format": FAULT_STALL_FORMAT, "judged": False, "error": code}


E3_RESULT_VERSION = "e3-confirmation-run/v1"  # e3contract.RunResultVersion
_E3_NO_REASON = ("completed", "interrupted")
_E3_LIST_FIELDS = ("verdicts", "skipped", "candidates", "writes")  # e3contract.RunResult 배열 칸
_E3_NOT_RUN = ("disabled", "start_refused")  # 확정 단계를 돌지 않은 상태(계약상 요청 0건)
_E3_CANDIDATE_LEVELS = ("suspected", "confirmed")  # e3contract.CandidateConfirmation.Level
_E3_REASON = re.compile(r"[a-z][a-z0-9_]{0,63}\Z")


def e3_confirmation_row(raw: bytes) -> dict:
    """제품이 회차 디렉터리에 한 번 쓴 E③ 확정 단계 결과(e3contract.RunResult)에서 상태·사유·확정 요청 수를 꺼낸다(2026-11-19
    사용자 결정). 원본 SHA를 같이 남긴다. 판정·쓰기 칸은 옮기지 않는다(공식 측정은 E③ 비활성이라 판정 0건). 모양이 다르거나
    상태와 요청 수가 모순이면(비활성인데 확정 요청이 있음, #742 리뷰) 고정 코드 e3_confirmation_invalid다."""
    try:
        value = json.loads(raw)
    except ValueError:
        return {"error": "e3_confirmation_invalid"}
    if not isinstance(value, dict) or value.get("version") != E3_RESULT_VERSION or value.get("status") not in E3_STATUSES:
        return {"error": "e3_confirmation_invalid"}
    reason = value.get("reason")
    if value["status"] in _E3_NO_REASON:
        if reason not in (None, ""):
            return {"error": "e3_confirmation_invalid"}
        reason = None
    elif not isinstance(reason, str) or not _E3_REASON.fullmatch(reason):
        return {"error": "e3_confirmation_invalid"}
    requests = value.get("requests")
    # 계약상 비활성이면 확정 요청이 0건이다. 요청 수는 0 이상 정수만 받는다(bool·실수·문자열은 모양 오류).
    if type(requests) is not int or requests < 0 or (value["status"] == "disabled" and requests != 0):
        return {"error": "e3_confirmation_invalid"}
    # 목록 칸은 계약 모양(배열)이어야 하고, 확정 단계를 돌지 않은 상태(비활성·시작 거부, 요청 0건)는 판정·쓰기 기록이 비어 있어야
    # 한다. 판정이 있는 비활성 원본은 모순이라 비활성 근거로 쓰지 않는다(#742 리뷰).
    if not all(isinstance(value.get(name), list) for name in _E3_LIST_FIELDS):
        return {"error": "e3_confirmation_invalid"}
    if value["status"] in _E3_NOT_RUN and (value["verdicts"] or value["writes"] or requests != 0):
        return {"error": "e3_confirmation_invalid"}
    # 후보 단계는 suspected·confirmed뿐이고, confirmed는 연결된 CONFIRMED 판정이 있을 때만이다(E③ 계약 §9). 판정이 없는
    # 결과(돌지 않은 상태 포함)에 confirmed 후보가 있으면 모순이다(#742 리뷰). 비활성의 suspected 후보는 그대로 받는다.
    candidates = value["candidates"]
    if not all(isinstance(item, dict) and item.get("level") in _E3_CANDIDATE_LEVELS for item in candidates):
        return {"error": "e3_confirmation_invalid"}
    if any(item["level"] == "confirmed" for item in candidates) and (value["status"] in _E3_NOT_RUN or not value["verdicts"]):
        return {"error": "e3_confirmation_invalid"}
    return {"status": value["status"], "reason": reason, "requests": requests, "sha256": hashlib.sha256(raw).hexdigest()}


def _e3_confirmation(ports, draft_id: str) -> tuple[dict, bytes | None]:
    """회차 디렉터리(runs/<초안 ID>)의 e3-confirmation.json 행과 원본. 파일이 없으면(감독자 연결 전 제품 빌드) 복사 도우미가
    실패하므로 수집 못 함(e3_confirmation_not_collected)으로 남긴다. 공식 회차의 처리는 호출자가 한다."""
    try:
        raw = ports.e3_confirmation(draft_id)
    except HelperCleanupError:
        # 도우미가 보호 볼륨을 계속 잡고 있거나 정리를 확인하지 못했다. 파일 미수집과 달리 회차 정리를 믿을 수 없어 다른 복사
        # 단계처럼 무효다(#742 리뷰).
        raise RunFailure("e3_confirmation_copy_failed", ("docker_helper_cleanup_failed",)) from None
    except Exception:
        return {"error": "e3_confirmation_not_collected"}, None
    return e3_confirmation_row(raw), raw


def _e3_traces(ports, evidence, state, draft_id: str, result_raw: bytes | None) -> None:
    """E③ 흔적·E② 저널 사건 수(e3-traces.json, 개발 계획 Go 조건 2 근거). 기록만 한다: 원본을 못 읽어도 회차 유효성은 그대로이고
    고정 코드로 남긴다. 도우미 정리 실패는 다른 복사 단계처럼 무효다."""
    try:
        sources = ports.e3_trace_sources(draft_id)
    except HelperCleanupError:
        raise RunFailure("e3_traces_copy_failed", ("docker_helper_cleanup_failed",)) from None
    except Exception:
        record = {"format": E3_TRACES_FORMAT, "error": "e3_traces_copy_failed"}
    else:
        record = e3_traces(dict(sources, e3_result=result_raw))
    state.summary["e3_traces"] = record.get("summary", {"error": record.get("error")})
    _step(state, "evidence_write_failed", lambda: evidence.write_json(E3_TRACES_NAME, record))


PRODUCT_CALLS_NAME = "product-calls.jsonl"


# 러너 상한 전에 준비 단계에서 끝나 실행 기록이 없는 회차(제품 `diagnosis-run`이 `execution: NOT_RUN` 또는 404). 실행 기록의
# 목표 SHA·모델 사용량이 없어 완료 기록(B·O 목표 SHA 대조)을 만들 수 없다. 그래서 이 사유로 무효(시도로 셈, 2N)이고, 일찍 끝난
# 회차 목록에 따로 나온다(사용자 확정 2026-11-21, 고정 초안 §26).
PRODUCT_NOT_EXECUTED_REASON = "product_not_executed"
CLASS_NOT_EXECUTED = "ended_not_executed"


def _run_record_or_not_executed(spec, ports, evidence, state, task_id: str, limit: dict, engine_end: dict) -> dict:
    """제품 실행 기록을 읽는다. 제품이 러너 상한 전에 끝났고 실행 기록이 없으면(준비 단계에서 끝남) 실행 기록을 요구하기 전에
    일찍 끝난 회차 분류를 한다: 설정 오류·제공자 장애 끝은 그 사유로, 그 밖은 product_not_executed로 끝낸다."""
    state.step = "product_run_record_failed"
    try:
        record = ports.run_record(task_id)
        not_executed = isinstance(record, dict) and record.get("execution") == "NOT_RUN"
    except ProductAPIError as exc:
        record, not_executed = None, str(exc).startswith("product_http_404:")
    except Exception:
        raise RunFailure("product_run_record_failed") from None
    if not (not_executed and limit["value"].startswith(ENGINE_ENDED_PREFIX)):
        if record is None:
            raise RunFailure("product_run_record_failed")
        return record
    ledger = calls = None
    if not spec.synthetic:
        ledger, calls = _step(state, "product_records_copy_failed", ports.copy_product_records)
    early = _split_early_end(spec, evidence, state, limit, engine_end, calls, ledger, not_executed=True)
    if early["reason"] in (CONFIG_END_REASON, PROVIDER_FAULT_END_REASON):
        raise RunFailure(early["reason"], (PRODUCT_NOT_EXECUTED_REASON,))
    raise RunFailure(PRODUCT_NOT_EXECUTED_REASON)


def _start_step(evidence, state, code: str, action):
    """제품 준비·시작 단계. 제품이 설정 문제 고정 사유(승인 만료·승인 필요 등)로 거부하면 설정 오류(config_end)로, 그 밖은
    그 단계의 고정 사유로 바꾼다(_step과 같음). 설정 오류는 세션이 시도로 세지 않고 멈춘다(고정 초안 §26, 2026-11-21 사용자)."""
    state.step = code
    try:
        return action()
    except RunFailure:
        raise
    except ProductAPIError as exc:
        refused = start_refusal_code(str(exc))
        if refused is None:
            raise RunFailure(code) from None
        record = start_refusal_record("ours", refused)
        state.summary["early_end"] = record
        _step(state, "evidence_write_failed", lambda: evidence.write_json(EARLY_END_NAME, record))
        raise RunFailure(CONFIG_END_REASON, (f"start_refused:{refused}",)) from None
    except Exception as exc:
        raise RunFailure(code, ("docker_helper_cleanup_failed",) if isinstance(exc, HelperCleanupError) else ()) from None


def _split_early_end(spec, evidence, state, limit: dict, engine_end: dict, calls: bytes | None, ledger: bytes | None,
                     not_executed: bool = False) -> dict:
    """일찍 끝난 회차 분류 기록을 만들어 요약과 근거에 남긴다. 호출 기록이 없으면(합성) 근거 없음으로 분류한다. not_executed면
    (준비 단계에서 끝나 실행 기록 없음) 설정 오류·제공자 장애 끝이 아닌 분류를 실행 전 끝남(product_not_executed)으로 둔다."""
    rows = None if calls is None else _call_rows(calls)
    budget = None
    if ledger is not None and spec.approval_caps is not None and limit["value"] == PRODUCT_BUDGET_LIMIT:
        totals = _step(state, "early_end_failed", lambda: ledger_totals(ledger))
        budget = approval_budget(totals, spec.approval_caps)
    record = _step(state, "early_end_failed", lambda: early_end_record("ours", limit, engine_end["first_terminal"], rows, budget))
    if not_executed and record["reason"] not in (CONFIG_END_REASON, PROVIDER_FAULT_END_REASON):
        record = dict(record, **{"class": CLASS_NOT_EXECUTED, "reason": PRODUCT_NOT_EXECUTED_REASON})
    state.summary["early_end"] = record
    _step(state, "evidence_write_failed", lambda: evidence.write_json(EARLY_END_NAME, record))
    return record


def _call_rows(calls: bytes) -> list[dict]:
    """제품 호출 기록 줄. 읽지 못한 줄은 버린다(끊긴 호출로 분류하지 않을 뿐, 줄 수 대조는 call_records_reasons가 이미 했다)."""
    rows = []
    for line in calls.decode("utf-8", "replace").splitlines():
        try:
            row = json.loads(line)
        except ValueError:
            continue
        if isinstance(row, dict):
            rows.append(row)
    return rows


def _run_split_steps(spec: SplitRunSpec, ports: SplitRunPorts, meter: MeterFiles, evidence, clock: Clock,
                     state: _SplitState) -> None:
    # 기록에 필요한 칸이 비었으면 아무것도 띄우지 않는다(제품 시작·모델 호출 전에 끝낸다, #745 리뷰)
    if missing_record_fields(spec):
        raise RunFailure("record_spec_incomplete")
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
    _split_check(spec, ports, evidence, state, "pre")
    _split_probe(ports, evidence, state, "pre", pre)
    pre_checked_at = _stamp(clock)  # 경계 적용·점검 완료(pre 관측 전)
    token = _step(state, "dns_token_failed", ports.dns_token)
    # 러너에는 대상 준비 채널이 없다(대상 준비는 띄울 때 끝남). 제품 준비(로그인~초안)는 대상 요청이 아니다(설계안 §3).
    fixture_ready_at = _stamp(clock)

    window_start = clock.now()
    state.events = _step(state, "window_events_failed", lambda: ports.start_events(
        _event_time(window_start - timedelta(seconds=spec.events_lookback_seconds))))
    state.watch = _step(state, "window_watch_failed", ports.start_host_watch)
    state.observer_calls += 1
    pre_board = _step(state, "pre_scoreboard_failed", lambda: ports.scoreboard("pre", pre))
    if _step(state, "meter_read_failed", meter.forwarded) != 0:
        raise RunFailure("pre_requests_nonzero")

    # 제품 사용자 순서: 로그인~초안까지는 준비, 예산 시계는 B의 작업 생성 직전과 같은 뜻으로 시작 요청 직전부터 잰다.
    prepared = _start_step(evidence, state, "product_prepare_failed", lambda: ports.prepare_round(token))
    requested_at, started = _stamp(clock), clock.monotonic()
    running = _start_step(evidence, state, "product_start_failed", lambda: ports.start_round(prepared))
    task_id, draft_id = running["task_id"], running["draft_id"]
    _step(state, "evidence_write_failed", lambda: evidence.write_json("task.json", {
        "format": SPLIT_TASK_FORMAT, "draft_id": draft_id, "task_id": task_id, "target": prepared["target"],
        "catalog_profile": prepared["catalog_profile"], "requested_at": requested_at, "created_at": _stamp(clock)}))
    watch = product_end_watch(ports, _StatusReader(ports), draft_id, task_id, clock, started, spec.task_status_wait_seconds)
    # 끝 신호 뒤 무활동 유예(B와 같은 T·같은 규칙). O는 제품 정리 끝(drain_complete)이 있어야 끝낸다.
    idle = None if spec.fault_stall_seconds is None else _EndIdle(spec.fault_stall_seconds, clock, started,
                                                                    ready=product_drained)
    stop_reason, forwarded_at_stop = _wait_budget(spec, meter, clock, state, started, watch, idle)
    idle_stopped_at = _stamp(clock)  # 유예로 끝냈을 때 진행 중 호출을 셀 기준 시각
    stop_elapsed = clock.monotonic() - started
    _step(state, "seal_failed", lambda: ports.seal(pre["services"][METER]["id"]))
    sealed_at = _stamp(clock)  # 멈춤 집계 창 끝(B의 engine_end_at과 같은 자리: 계측기 봉인 직후)
    drained = _wait_drained(spec, meter, clock, state)
    drained_at = _stamp(clock)
    # 강제 취소된 요청이 있으면 B와 같은 공통 조건(run_drain.settle_seconds)만큼 기다린 뒤 관측한다(runner.py와 같은 규칙).
    settled = spec.post_settle_seconds if drained["forced_cancelled_requests"] else 0
    if settled:
        clock.sleep(settled)
    # 계측기 기록 마감 대조는 B와 같은 함수·같은 사유다(경로 기록 필수·키 지문·전달 요청과 한 행씩, 2026-11-18 결정 (a)).
    meter_rows = _step(state, "meter_read_failed", meter.engine_rows)
    sealed = check_meter_logs(meter, state, meter_rows, drained, spec.path_key_ref)
    check_request_cap(sealed, drained, spec.request_budget)
    # B는 drain 뒤 게이트웨이를 봉인해 새 모델 요청을 막는다(#634). O는 같은 자리에서 제품 회차를 멈춘다. 러너 예산으로 먼저
    # 끝난 회차는 제품이 아직 실행 중이라 실행 기록(마감 때 저장)이 없고 모델 호출이 계속된다. 끝 상태·drain 완료를 본 뒤에야
    # 실행 기록·장부·호출 기록을 읽는다(#665 리뷰).
    stop_requested_at = _stamp(clock)  # 끊긴 호출 증명의 정지 신호 시각(평가 프로토콜 §6)
    _step(state, "product_stop_failed", lambda: ports.stop_round(draft_id))
    closed = _step(state, "product_close_failed", lambda: ports.wait_closed(draft_id, spec.product_close_seconds))
    # 끝난 회차의 발견 출처별 건수(고정 초안 §8의 MCP 출처 비중). 제품이 멈춘 뒤라 snapshot이 더 바뀌지 않는다.
    state.summary["discovery_sources"] = _discovery_sources(ports, draft_id)
    _step(state, "evidence_write_failed", lambda: evidence.write_json(DISCOVERY_SOURCES_NAME, state.summary["discovery_sources"]))

    post = _step(state, "post_facts_failed", lambda: ports.snapshot("post"))
    # pre~post 사이 데몬·망·구성원·주소·바인딩·컨테이너 교체·재시작(B와 같은 compare_snapshots, #668 리뷰). 배치 검사는
    # 기대 배치와의 일치만 보므로 같은 이미지·주소의 새 컨테이너로 바뀐 것은 이 대조가 잡는다.
    if compare_snapshots(pre, post):
        raise RunFailure("boundary_changed")
    # 사후 점검은 경계의 두 입구·netns가 살아 있어야 한다(#653 기대값). 그래서 경계 정지는 post 관측·점검 뒤, 구간이 끝나기
    # 전이다. 경계는 SIGTERM 정지 한 번으로 봉인 표식까지 쓰고 끝난다(계약 §4.1 결정 B). 구간 감시가 그 정지만 허용한다.
    _split_check(spec, ports, evidence, state, "post")
    post_check_at = _stamp(clock)
    _split_probe(ports, evidence, state, "post", post)
    state.observer_calls += 1
    post_board = _step(state, "post_scoreboard_failed", lambda: ports.scoreboard("post", post))
    _step(state, "boundary_stop_failed", lambda: ports.stop_boundary(pre["services"][BOUNDARY]["id"]))
    window_end = clock.now()
    stopped = _step(state, "window_watch_failed", lambda: _stop_watch(state))
    streamed = _step(state, "window_events_failed", lambda: _stop_events(state, _ns(window_end)))
    window = _split_window(spec, evidence, state, pre, window_start, window_end, stopped, streamed)
    # 관측기 GET은 러너가 보낸 pre·post 두 건뿐이어야 한다(B와 같은 규칙, 대상 준비 확인 GET은 O에 없다).
    observer = _step(state, "meter_read_failed", meter.observer_rows)
    if len(observer) != state.observer_calls or any(row["decision"] != "registered" for row in observer):
        raise RunFailure("observer_count_mismatch")

    engine_end = watch.record()
    _step(state, "evidence_write_failed", lambda: evidence.write_json(PRODUCT_END_NAME, engine_end))
    limit = first_limit(stop_reason, stop_elapsed, spec.time_budget_seconds, engine_end["first_terminal"])

    record = _run_record_or_not_executed(spec, ports, evidence, state, task_id, limit, engine_end)
    # 마감 뒤 제품 작업의 목표 칸(목표 수·달성 수 등)을 남긴다. 판정에 쓰지 않는 근거라 읽지 못해도 회차를 무효로 하지 않고
    # 고정 코드만 남긴다(B의 끝 신호 읽기 실패와 같은 처리).
    _step(state, "evidence_write_failed", lambda: evidence.write_json(PRODUCT_TASK_END_NAME, _task_end(ports, task_id)))
    if spec.cli_usage_names:
        _product_cli_usage(spec.cli_usage_names, ports, evidence, state, task_id)
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
    if spec.e3_disabled_required:
        # E③ 비활성 요구(공식 측정은 늘 켬, 평가 프로토콜 §5.1): 그 회차가 실제로 쓴 정책의 확정 몫이 0으로 적혀 있어야 한다.
        check_official_e3_share(policy["limits"])
    fingerprint = policy["policy_fingerprint_sha256"]
    execution = diagnosis_entered(record, draft_id, task_id, policy["policy_revision"])
    entered = execution is not None
    files = dict(_step(state, "product_files_failed", ports.product_files), control_limits=digest(policy["limits"]))
    # 진단에 들어가지 않은 회차(탐색 중 예산 소진 등)는 실행 조건 파일이 없다. 그때만 null로 남기고 first_limit은 그대로다.
    # 진단에 들어간 회차에서 빠지거나 다른 회차의 것이면 실패다.
    runtime = checked_runtime_conditions(
        _step(state, "runtime_conditions_failed", lambda: ports.runtime_conditions(draft_id)), draft_id, task_id,
        policy["policy_revision"], execution) if entered else None
    files["runtime_conditions"] = None if runtime is None else runtime[0]
    # E③ 확정 단계 결과(2026-11-19 사용자 결정): 제품을 멈춘 뒤 회차 디렉터리에서 복사해 상태·사유·원본 SHA를 남긴다.
    # 보존 sidecar의 제품 파일 칸(닫힌 목록)과 섞지 않고 따로 둔다. 공식 회차는 비활성(선언 없음)이어야 한다(평가 프로토콜 §5.1).
    e3, e3_raw = _e3_confirmation(ports, draft_id)
    state.summary["e3_confirmation"] = e3
    _e3_traces(ports, evidence, state, draft_id, e3_raw)
    _step(state, "evidence_write_failed", lambda: evidence.write_json(PRODUCT_FILES_NAME, {
        "format": PRODUCT_FILES_FORMAT, "file_sha256": files, "diagnosis_entered": entered, "diagnosis_policy_sha256": fingerprint,
        "policy_ref": policy["policy_ref"], "policy_revision": policy["policy_revision"], "limits": policy["limits"],
        "e3_confirmation": e3}))
    if spec.e3_disabled_required:
        if "error" in e3:
            # 복사하지 못함(연결 전 빌드 등)과 모양·모순 오류를 사유로 가른다
            raise RunFailure("e3_status_not_collected" if e3["error"] == "e3_confirmation_not_collected" else "e3_confirmation_invalid")
        if (e3["status"], e3["reason"]) != ("disabled", OFFICIAL_E3_DISABLED_REASON):
            raise RunFailure("e3_official_not_disabled")
    _step(state, "boundary_evidence_copy_failed", ports.copy_boundary_evidence)
    # 설치·등록 조건(서명 근거). 같은 설정이면 같은 값이라 O 쪽 작업 요청 지문으로 쓴다(B는 작업 생성 본문 SHA).
    conditions = _step(state, "split_conditions_failed", ports.split_conditions)
    state.task_request_sha256 = digest(conditions)
    record_inputs = dict(pre=pre, post=post, pre_board=pre_board, post_board=post_board, window=window, observer=observer,
                         meter_rows=meter_rows, sealed=sealed, drained=drained, settled=settled, goal=goal, files=files,
                         workers=None if runtime is None else runtime[1],
                         steps={"fixture_ready": fixture_ready_at, "pre": pre_board["received_at"], "engine_start": requested_at,
                                "engine_end": sealed_at, "drained": drained_at, "post": post_board["received_at"]},
                         pre_checked_at=pre_checked_at, post_check_at=post_check_at)
    if spec.synthetic:
        # 합성 모델 회차: 제품은 로컬 모델 설치로 스크립트 모델을 불러 OpenRouter 장부·호출 기록이 없다. 사후 검증·장부 대조는
        # '해당 없음(합성)'이고, 이 표시는 합성 회차에서만 허용된다(실제 모델 회차에서 나오면 sidecar 검증이 무효로 본다).
        llm_summary = {"verification": LLM_NOT_APPLICABLE, "sampling_applied": usage["sampling"], "rate_limited": None}
        _step(state, "evidence_write_failed", lambda: evidence.write_json("product-model.json", {
            "usage": usage, "call_records": None, "verification": LLM_NOT_APPLICABLE}))
        _step(state, "evidence_write_failed", lambda: evidence.write_json("llm-summary.json", dict(llm_summary, reasons=[])))
        # 합성 회차는 호출 기록이 없어 제공자 장애·설정 오류 근거가 없다. 일찍 끝났으면 유효로 두고 끝 상태만 남는다.
        _split_early_end(spec, evidence, state, limit, engine_end, None, None)
        if idle is not None:  # 호출 기록이 없어 진행 중 호출은 모름(null), 근거는 제품 정리 끝뿐이다
            _step(state, "evidence_write_failed", lambda: evidence.write_json(END_IDLE_NAME, idle.record(None)))
        _finish_split_summary(spec, state, stop_reason, forwarded_at_stop, limit, drained, settled, pre_board, post_board,
                              goal, window, usage, task_id, draft_id, files, fingerprint, entered, closed)
        _split_record(spec, evidence, state, llm_summary, **record_inputs)
        if spec.preservation is not None:
            state.preservation = _preservation_v3(spec, state, pre_board["state_sha256"], stop_reason, limit,
                                                  engine_end["first_terminal"], llm_summary, files, fingerprint, conditions,
                                                  usage["sampling_overwrites"], None, None, task_id)
        return
    if spec.e2_v3:
        _collect_e2_v3(ports, evidence, state, draft_id)
    ledger, calls = _step(state, "product_records_copy_failed", ports.copy_product_records)
    record_reasons = call_records_reasons(ledger, calls)
    _step(state, "evidence_write_failed", lambda: evidence.write_json("product-model.json", {
        "usage": usage, "call_records": {"reasons": record_reasons}}))
    if record_reasons:
        raise RunFailure(record_reasons[0])
    # 제공자 장애 멈춤 판정(결정안 §7). B와 같은 함수·같은 창(시작 요청 직전~계측기 봉인 직후)·같은 T이고, 모델 호출 행은
    # 제품 호출 기록(게이트웨이 행과 같은 칸·어휘)이다. B처럼 모델 호출 검증 사유보다 앞의 첫 사유로 둔다.
    state.summary["fault_stall"], stall_reason = fault_stall_outcome(
        product_fault_stall(calls, meter_rows, requested_at, sealed_at, engine_end), spec.fault_stall_seconds)
    _step(state, "evidence_write_failed", lambda: evidence.write_json(FAULT_STALL_NAME, state.summary["fault_stall"]))
    # 러너 상한 전에 일찍 끝난 회차의 분류(평가 프로토콜 §5.1·고정 초안 §26). 근거 호출은 제품 호출 기록, 승인 예산은 장부와 manifest다.
    early = _split_early_end(spec, evidence, state, limit, engine_end, calls, ledger)
    # 유예로 끝낸 회차면 그 시각에 진행 중이던 모델 호출을 제품 호출 기록으로 센다(B 게이트웨이 행과 같은 칸).
    idle_inflight = inflight_at_stop(_call_rows(calls), idle_stopped_at) if stop_reason == STOP_ENGINE_IDLE else None
    if idle is not None:
        _step(state, "evidence_write_failed", lambda: evidence.write_json(END_IDLE_NAME, idle.record(idle_inflight)))
    idle_reason = end_idle_reason(stop_reason, idle_inflight)
    if early["reason"] == CONFIG_END_REASON:
        # 설정 오류는 세션이 시도로 세지 않고 멈춘다. 사후 검증(제공자 조회)을 하지 않고 여기서 끝낸다.
        raise RunFailure(CONFIG_END_REASON, (stall_reason,) if stall_reason else ())
    # 제품 호출 기록을 B 게이트웨이와 같은 조회 코드로 사후 검증하고(eval-meter -role generation-verify) B와 같은 규칙으로
    # 판정한다(결정안 D(b), 4-11). 샘플링은 제품이 실제로 보낸 값을 표식에 싣고 B 게이트웨이 적용값(선언)과 대조한다.
    _step(state, "llm_verify_failed", lambda: ports.generation_verify(calls, usage["sampling"]))
    verified = _step(state, "llm_read_failed", ports.llm_verified)
    if verified is None:
        raise RunFailure("llm_unverified")
    # 제품 호출 기록을 회차 근거로 남긴다(칸: 순번·결정·메서드·상태·시각·바이트·generation ID·끝 모양, 본문·경로·키 없음).
    # 미확인 generation이 기록상 끊긴 호출인지 대조하는 근거다(2026-11-19 사용자 결정).
    _step(state, "evidence_write_failed", lambda: evidence.write_bytes(PRODUCT_CALLS_NAME, calls))
    interrupted = interrupted_generation_ids(_call_rows(calls), stop_requested_at, product=True)
    llm_reasons, llm_summary = _step(state, "llm_marker_invalid", lambda: judge_product_llm(
        verified, spec.llm_sampling, spec.llm_provider_slug, spec.llm_provider_name, interrupted))
    # 끊긴 호출 분류에 실제로 쓴 정지 요청 시각을 근거에 남긴다(평가 프로토콜 §6: 회차 뒤에도 product-calls.jsonl과 이 시각으로
    # 같은 분류를 다시 계산해 예외 적용을 재검증한다, #735 리뷰). 계측기 봉인 시각은 그 사이 drain·정착 대기가 있어 대신 못 쓴다.
    state.summary["stop_requested_at"] = stop_requested_at
    _step(state, "evidence_write_failed", lambda: evidence.write_json("llm-summary.json", dict(
        llm_summary, reasons=llm_reasons, stop_requested_at=stop_requested_at)))
    if stall_reason:
        raise RunFailure(stall_reason, tuple(llm_reasons))
    if early["reason"] == PROVIDER_FAULT_END_REASON:
        # 제공자 장애로 끝남은 멈춤과 같은 부류다(B와 같은 순서: 멈춤 다음, 모델 호출 검증 사유 앞).
        raise RunFailure(PROVIDER_FAULT_END_REASON, tuple(llm_reasons))
    if idle_reason:
        raise RunFailure(idle_reason, tuple(llm_reasons))
    if llm_reasons:
        raise RunFailure(llm_reasons[0])
    llm_summary = dict(llm_summary, verification=LLM_VERIFIED)
    _finish_split_summary(spec, state, stop_reason, forwarded_at_stop, limit, drained, settled, pre_board, post_board,
                          goal, window, usage, task_id, draft_id, files, fingerprint, entered, closed)
    _split_record(spec, evidence, state, llm_summary, **record_inputs)
    if spec.preservation is not None:
        # 보존 sidecar v3 O 칸(결정안 §3.3). 해시는 내린 뒤 build_payload_v3가 최종 바이트로 뜬다(경계 원자료 검증 포함).
        state.preservation = _preservation_v3(spec, state, pre_board["state_sha256"], stop_reason, limit,
                                              engine_end["first_terminal"], llm_summary, files, fingerprint, conditions,
                                              usage["sampling_overwrites"], ledger_reservations(ledger), call_record_lines(calls),
                                              task_id)


def split_run_conditions(spec: SplitRunSpec, state: _SplitState, llm_summary: dict, goal: dict, files: dict,
                         workers: int | None) -> dict:
    """O 회차의 run_conditions 행(설계안 §6). B와 같은 칸 이름에 O 출처의 값을 넣는다.

    - 엔진 설정: 제품 구동 설정(v3 쪽별 driver_settings와 같은 정의)과 그 회차 정책 상한 지문을 묶은 지문.
    - 엔진 규칙 파일: 없음(원본 ② 규칙은 B 쪽 장치). 도구 트리: 도구 면 지문(tool-surface.json의 요약 네 칸).
    - Worker 수: 제품이 그 회차에 결속한 값(실행 조건 파일). 진단에 들어가지 않은 회차는 파일이 없어 모른다(null).
    - 표시 언어: 러너가 제품 엔진 설정을 읽을 경로가 없어 모른다(null). 스트리밍: 제품 OpenRouter transport는 stream:true를
      거부하므로(openrouter_transport.go) 실제 모델 회차는 거짓이다. 합성 회차는 그 transport를 지나지 않아 모른다(null).
    """
    host = state.summary["host"]
    return {"purpose": spec.purpose, "path_key_ref": spec.path_key_ref, "split": True,
            "execution_source": "synthetic_fixture" if spec.synthetic else "model_run",
            # 제품 회차는 E③ 상태를 늘 기대한다(감독자 연결 전 빌드는 행이 없어 수집 실패로 드러난다, #713)
            "e3_status_expected": True, "goal_common_sha256": goal["common_sha256"],
            "e3_disabled_required": spec.e3_disabled_required,
            "engine_settings_sha256": digest({"driver_settings_sha256": v3f.settings_sha256(spec.driver_settings, v3f.O_DRIVER_SETTINGS),
                                              "control_limits_sha256": files["control_limits"]}),
            "engine_rules_sha256": None, "output_language": None, "tool_tree_sha256": digest(state.summary["tool_surface"]),
            "workers": workers, "llm_stream": None if spec.synthetic else False, "sampling": llm_summary["sampling_applied"],
            "host": {key: value for key, value in host.items() if key != "machine_id_sha256"},
            "machine_id_sha256": host["machine_id_sha256"]}


RECORD_SPEC_FIELDS = ("condition", "fixture_fingerprint", "environment_fingerprint", "target_origin", "observer_path",
                      "observer_readonly_ref", "model_tag")


def missing_record_fields(spec: SplitRunSpec) -> list[str]:
    """v1.1 러너 기록에 필요한 선언·설정 칸 중 빈 것. 회차를 띄우기 전에 보고(#745 리뷰: 빈 입력으로 유료 회차를 쓰지 않게),
    기록을 만들 때 다시 본다."""
    # 서명 기록 로더와 같은 비공백 규칙이다(공백뿐인 값은 로더가 거부하므로 회차 전에 막는다, #745 리뷰)
    return [name for name in RECORD_SPEC_FIELDS if not isinstance(getattr(spec, name), str) or not getattr(spec, name).strip()]


def _split_record(spec: SplitRunSpec, evidence, state: _SplitState, llm_summary: dict, *, pre: dict, post: dict, pre_board: dict,
                  post_board: dict, window: dict, observer: list, meter_rows: list, sealed: dict, drained: dict, settled: int,
                  goal: dict, files: dict, workers: int | None, steps: dict, pre_checked_at: str, post_check_at: str) -> None:
    """O v1.1 러너 기록(서명 전)과 근거 파일 둘(격리 근거·해결 이벤트). B(runner._build_record)와 같은 부품·같은 행 종류를
    쓰고 출처만 O다(설계안 §2~§5). 서명은 내린 뒤 한다."""
    def build() -> dict:
        if missing_record_fields(spec):
            raise RecordError("record_spec_incomplete")
        target = pre["services"][TARGET]["id"]
        context = RecordContext(run_id=spec.run_id, model_tag=spec.model_tag, reset_generation=target, instance_id=target,
                                execution_id=pre["services"][PREPARATION]["id"])
        boundary_ref, host_ref = "compose:" + spec.project, "host-condition:" + spec.run_id
        get_ref = path_ref(spec.observer_path)
        policy = policy_sha256(pre)
        allowed = [{"address": item.address, "port": item.port} for item in spec.allowed_listens]
        meter = {"sealed_at": sealed.get("sealed_at"), "drained_at": drained.get("drained_at"),
                 "drain_grace_seconds": spec.drain_grace_seconds, "post_settle_seconds": spec.post_settle_seconds,
                 "forced_cancelled_requests": drained["forced_cancelled_requests"], "post_settle_applied_seconds": settled,
                 "forwarded_requests": forwarded_count(meter_rows), "rejected_after_seal": drained.get("rejected_after_seal"),
                 "forced_closed_streams": drained.get("forced_closed_streams")}
        e3 = state.summary.get("e3_confirmation") or {"error": "e3_confirmation_not_collected"}
        timeline_steps = timeline(steps)
        rows = evidence_rows(
            pre_at=pre_checked_at, post_check_at=post_check_at, window=window, pre=pre, post=post,
            pre_probe=state.probes["pre"], post_probe=state.probes["post"], host_ref=host_ref,
            pre_listeners=state.listeners["pre"], post_listeners=state.listeners["post"], allowed=allowed,
            boundary_ref=boundary_ref, policy=policy, steps=timeline_steps, observer_rows=observer, ready_calls=0,
            observer_path_ref=get_ref, readonly_ref=spec.observer_readonly_ref, meter=meter, drained=drained,
            engine_rows=len(meter_rows), conditions=split_run_conditions(spec, state, llm_summary, goal, files, workers),
            e2={"mode": E2_SPLIT_PRODUCT, "decision_rows": None, "allowed": None, "unsupported": None, "unavailable": None},
            verdict_collection=dict(VERDICT_NOT_COLLECTED),
            e3_confirmation=None if "error" in e3 else {"status": e3["status"], "reason": e3["reason"]})
        evidence.write_json("isolation-evidence.json", isolation_evidence(context, rows))
        evidence_sha, evidence_size = evidence.describe("isolation-evidence.json")
        in_window = sum(1 for row in rows if row["kind"] == "observer_request" and row["window"] == "in_window")
        proof = isolation_proof(context, timeline_steps, pre_board["normalized"]["sha256"], post_board["normalized"]["sha256"],
                                spec.target_origin, boundary_ref, policy, host_ref, evidence_sha,
                                f"{OBS}:" + pre["services"][OBS]["id"], get_ref, in_window)
        events = solve_events(context, pre_board["solved"], post_board["solved"], timeline_steps["post"], canonical_sha256(proof))
        evidence.write_json("solve-events.json", dict(context.fields(), phase="engine", events=events))
        events_sha, events_size = evidence.describe("solve-events.json")
        artifacts = {
            "pre_raw": pre_board["raw"], "pre": pre_board["normalized"],
            "post_raw": post_board["raw"], "post": post_board["normalized"],
            "records": None, "traffic": None, "coverage": None,  # 판정 미수집(run-record-v1.1 §3)
            "solve_events": descriptor(context, "solve-events.json", events_sha, events_size, "engine"),
            "isolation_evidence": descriptor(context, "isolation-evidence.json", evidence_sha, evidence_size, "engine"),
        }
        # 차단 시도 수는 미측정(대조 미수집, 설계안 §4): 0이 아니라 null이다.
        return run_record(context, engine="ours", condition=spec.condition, repeat_index=spec.repeat_index,
                          total_requests=meter["forwarded_requests"], fixture_fingerprint=spec.fixture_fingerprint,
                          environment_fingerprint=spec.environment_fingerprint, steps=timeline_steps, artifacts=artifacts,
                          proof=proof, blocked_attempts=None)
    state.record = _step(state, "record_build_failed", build)


def _finish_split_summary(spec: SplitRunSpec, state: _SplitState, stop_reason, forwarded_at_stop, limit, drained, settled,
                          pre_board: dict, post_board: dict, goal: dict, window: dict, usage: dict, task_id: str, draft_id: str,
                          files: dict, fingerprint: str, entered: bool, closed: dict) -> None:
    """완료 회차 요약 칸(실제·합성 모델 공통). 실행 출처와 모델 호출 검증 상태를 같이 남긴다."""
    state.summary.update(stop_reason=stop_reason, forwarded_at_stop=forwarded_at_stop, first_limit=limit,
                         drained=drained, post_settle_seconds=spec.post_settle_seconds, post_settle_applied_seconds=settled,
                         pre_solved=pre_board.get("solved"), post_solved=post_board.get("solved"),
                         goal_common_sha256=goal["common_sha256"], window_reasons=window["reasons"],
                         sampling_overwrites=usage["sampling_overwrites"], task_id=task_id, draft_id=draft_id,
                         product_file_sha256=files, diagnosis_policy_sha256=fingerprint, diagnosis_entered=entered,
                         product_closed={name: closed.get(name) for name in PRODUCT_END_FIELDS},
                         execution_source="synthetic_fixture" if spec.synthetic else "model_run",
                         llm_verification=LLM_NOT_APPLICABLE if spec.synthetic else LLM_VERIFIED,
                         scripted_plan_sha256=spec.scripted_plan_sha256 or None)


def _preservation_v3(spec: SplitRunSpec, state: _SplitState, initial_state: str, stop_reason: str, limit: dict,
                     first_terminal: dict | None, llm_summary: dict, files: dict, fingerprint: str, conditions: dict,
                     overwrites: dict, reservations: int | None, lines: int | None, task_id: str) -> dict:
    """O 회차의 v3 칸(공통·쪽별·회차별·제품 블록). 공통 칸은 B(runner._preservation_v3)와 같은 정의로 뜬다."""
    options, surface = spec.preservation, state.summary["tool_surface"]
    common = {"request_budget": spec.request_budget, "time_budget_seconds": spec.time_budget_seconds,
              "model_pin_sha256": v3f.model_pin_sha256(backend="local" if spec.synthetic else "api", model=spec.model_tag,
                                                       provider_slug=spec.llm_provider_slug,
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
               "call_records": None if spec.synthetic else {"reservations": reservations, "lines": lines},
               "llm_verification": llm_summary["verification"]}
    return dict(series_ref=options["series_ref"], execution_source=options["execution_source"], run_id=spec.run_id, side="ours",
                repeat_index=spec.repeat_index, engine_source_sha=spec.product_source_sha,
                engine_image_id=spec.image_ids["product"], common=common, side_conditions=side, run=run, product=product,
                boundary={"task_id": task_id, "path_key_ref": spec.path_key_ref}, purpose=spec.purpose)


def execute_split_run(spec: SplitRunSpec, ports: SplitRunPorts, meter: MeterFiles, evidence, clock: Clock) -> dict:
    """O 회차 1회. 실패해도 감시를 멈추고, 띄웠으면 내리고, 보호 입력은 늘 지운다. 결과 기록을 정확히 하나 쓴다.

    완료 회차는 내린 뒤 보존 sidecar v3(경계 원자료 검증 포함)·v1.1 러너 기록·결과를 러너 키로 서명한다(B execute_run과
    같은 순서·같은 결과 형식). 무효 결과는 서명하지 않는다.
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
    if not reasons and state.record is not None:
        # v1.1 러너 기록도 내린 뒤에만 서명한다(B와 같은 러너 키).
        try:
            evidence.write_json(RECORD_NAME, ports.sign(state.record))
        except Exception:
            reasons.append("record_sign_failed")
    elif not reasons:
        reasons.append("record_build_failed")  # 완료까지 왔는데 기록이 없으면 서명할 것이 없다
    base = {"format": SPLIT_RESULT_FORMAT, "run_id": spec.run_id, "side": "ours", "layout": LAYOUT, "e2_mode": E2_SPLIT_PRODUCT,
            "project": spec.project, "started_at": started_at, "finished_at": _stamp(clock),
            "observer_login_file": spec.observer_login_file}
    result = None
    if not reasons:
        payload = dict(base, status=COMPLETE, images=dict(spec.image_ids), task_request_sha256=state.task_request_sha256,
                       evidence=evidence.digests(), **state.summary)
        try:
            result = ports.sign(payload)
        except Exception:
            reasons.append("sign_failed")
    if reasons:
        result = dict(base, status=INVALID, reasons=reasons, failed_step=state.step)
    evidence.write_json(RESULT_NAME, result)
    if interrupted:
        raise KeyboardInterrupt
    return result
