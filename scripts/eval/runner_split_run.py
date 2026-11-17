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
from dataclasses import dataclass, field  # noqa: E402
from datetime import timedelta  # noqa: E402
from typing import Protocol  # noqa: E402

from runner import (  # noqa: E402
    INVALID, RESULT_NAME, RunFailure, _check_subnets, _event_time, _ns, _stamp, _step, _stop_events, _stop_watch,
    _wait_budget, _wait_drained, first_limit,
)
from runner_evidence import MeterFiles  # noqa: E402
from runner_probe import PASS  # noqa: E402
from runner_product_llm import ProductLLMError, call_records_reasons, product_model_usage  # noqa: E402
from runner_split_probe import SplitProbeOutcome  # noqa: E402
from runner_window import events_reasons, judge_events, watch_reasons, watch_summary  # noqa: E402

SPLIT_RESULT_FORMAT = "abc2lab-eval-runner-split-result-v1"
SPLIT_TASK_FORMAT = "abc2lab-eval-runner-split-task-v1"
PRODUCT_END_NAME = "product-end.json"
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


class SplitRunPorts(Protocol):
    def preexisting(self) -> bool: ...
    def image_ids(self) -> dict: ...
    def host_spec(self) -> dict: ...
    def planned_subnets(self) -> list: ...
    def host_subnets(self) -> list: ...
    def up(self) -> None: ...
    def snapshot(self, phase: str) -> dict: ...
    def probes(self, phase: str, snapshot: dict) -> SplitProbeOutcome: ...
    def dns_token(self) -> str: ...
    def start_events(self, since: str): ...
    def start_host_watch(self): ...
    def scoreboard(self, phase: str, snapshot: dict) -> dict: ...
    def prepare_round(self, dns_token: str) -> dict: ...
    def start_round(self, prepared: dict) -> dict: ...
    def product_status(self, draft_id: str) -> dict: ...
    def background(self, work) -> None: ...
    def seal(self, meter_container_id: str) -> None: ...
    def stop_boundary(self, boundary_container_id: str) -> None: ...
    def run_record(self, task_id: str) -> dict: ...
    def copy_boundary_evidence(self) -> None: ...
    def copy_product_records(self) -> tuple: ...
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


class _StatusReader:
    """product_end_watch가 부르는 driver.status 자리(포트의 제품 상태 읽기)."""

    def __init__(self, ports: SplitRunPorts):
        self.ports = ports

    def status(self, draft_id: str) -> dict:
        return self.ports.product_status(draft_id)


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
    state.summary["host"] = _step(state, "host_spec_failed", ports.host_spec)
    _check_subnets(ports, evidence, state)
    state.up_attempted = True
    _step(state, "compose_up_failed", ports.up)

    pre = _step(state, "pre_facts_failed", lambda: ports.snapshot("pre"))
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
    # 경계는 계측기 봉인 뒤 SIGTERM 정지 한 번으로 봉인 표식까지 쓰고 끝난다(계약 §4.1 결정 B). 구간 감시가 그 정지만 허용한다.
    _step(state, "boundary_stop_failed", lambda: ports.stop_boundary(pre["services"][BOUNDARY]["id"]))
    drained = _wait_drained(spec, meter, clock, state)

    post = _step(state, "post_facts_failed", lambda: ports.snapshot("post"))
    _split_probe(ports, evidence, state, "post", post)
    post_board = _step(state, "post_scoreboard_failed", lambda: ports.scoreboard("post", post))
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

    _step(state, "boundary_evidence_copy_failed", ports.copy_boundary_evidence)
    ledger, calls = _step(state, "product_records_copy_failed", ports.copy_product_records)
    record_reasons = call_records_reasons(ledger, calls)
    _step(state, "evidence_write_failed", lambda: evidence.write_json("product-model.json", {
        "usage": usage, "call_records": {"reasons": record_reasons}}))
    if record_reasons:
        raise RunFailure(record_reasons[0])
    state.summary.update(stop_reason=stop_reason, forwarded_at_stop=forwarded_at_stop, first_limit=limit,
                         drained=drained, pre_solved=pre_board.get("solved"), post_solved=post_board.get("solved"),
                         goal_common_sha256=goal["common_sha256"], window_reasons=window["reasons"],
                         sampling_overwrites=usage["sampling_overwrites"], task_id=task_id, draft_id=draft_id)


def execute_split_run(spec: SplitRunSpec, ports: SplitRunPorts, meter: MeterFiles, evidence, clock: Clock) -> dict:
    """O 회차 1회. 실패해도 감시를 멈추고, 띄웠으면 내리고, 보호 입력은 늘 지운다. 결과 기록을 정확히 하나 쓴다.

    서명·보존 sidecar v3·경계 원자료 검증·generation-verify 연결은 다음 단계(4-7c-4·4-9)다. 여기 결과는 서명 전 요약이다.
    """
    state = _SplitState()
    started_at = _stamp(clock)
    reasons: list[str] = []
    try:
        _run_split_steps(spec, ports, meter, evidence, clock, state)
    except RunFailure as exc:
        reasons.append(exc.code)
        state.step = exc.code
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
    base = {"format": SPLIT_RESULT_FORMAT, "run_id": spec.run_id, "side": "ours", "layout": "exploration-split/v1",
            "project": spec.project, "started_at": started_at, "finished_at": _stamp(clock)}
    if reasons:
        result = dict(base, status=INVALID, reasons=reasons, failed_step=state.step)
    else:
        result = dict(base, status=COMPLETE, images=dict(spec.image_ids), **state.summary)
    evidence.write_json(RESULT_NAME, result)
    return result
