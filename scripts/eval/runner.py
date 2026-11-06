"""평가 러너 회차 순서(평가 프로토콜 §2).

  기동(대상·계측기·관측·engine-db·엔진) → pre 점검(5b)·probe(5c) → 대상 준비 → pre scoreboard
  → 계측기 전달 0 확인 → 작업 생성(엔진 API, 생성=기동) → 시간·요청 예산 → 계측기 seal(USR1) → drained 확인
  → LLM 게이트웨이 seal → post 점검·probe → post scoreboard → 구간 판정 → tmpfs 사용량 → 관측·계측 기록 대조
  → 게이트웨이 검증 마커 대기·응답 제공자 판정 → findings → down → 서명

어느 단계든 실패하면 고정 사유 코드로 "무효" 기록 하나만 남기고 성공 기록·서명은 만들지 않는다.
down은 이 회차가 up을 시도했다면 성공·실패와 무관하게 실행한다(down 실패도 사유). 자동 재시도는 없다.
재실행은 새 run_id다. 서명은 down까지 모두 통과한 뒤 마지막에 한 번, 회차 디렉터리 밖의 키로 한다.

요청 예산은 폴링 뒤 seal하므로 넘칠 수 있다. 실제 전달 건수와 초과분을 기록하며 "정확 준수"를 주장하지 않는다.
"""

from __future__ import annotations

import argparse
import calendar
import getpass
import hashlib
import json
import http.client
import os
import platform
import re
import sys
import time
from contextlib import closing
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Callable, Protocol

# python3 -I(-P 포함)는 스크립트 디렉터리를 sys.path에 올리지 않는다(score.py와 같은 이유). 같은 디렉터리의
# 평가 모듈(우리 코드)만 명시적으로 올린다 — cwd·사용자 site-packages는 계속 차단된다.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from preflight import PreflightError, load_preflight  # noqa: E402
from run_record import read_runner_key, sign_run_record  # noqa: E402
from runner_checks import (  # noqa: E402
    ExpectedTopology, Listener, allowed_host_listens, check_host_listeners, check_namespace_sharing,
    check_other_publishes, check_topology, compare_snapshots, expected_topology, listeners_outside_loopback,
    network_snapshot, parse_container, parse_network, policy_sha256, read_host_listeners,
)
from runner_config import (  # noqa: E402
    API_FRONT_SERVICE, E2_SERVICE, E2_STUB, ENGINE_DATA_PATH, ENGINE_DB_DATA_PATH, ENGINE_DB_SERVICE, ENGINE_SERVICE, LLM_GW_SERVICE,
    METER_SERVICE, OBS_SERVICE, TARGET_SERVICE, POLICY_SWITCH_SIDE, RunDirectory, RunnerConfig, RunnerConfigError,
    RunPlan, compose_base_args, compose_environment, create_run_directory, load_runner_config, new_run_id, plan_run,
)
from runner_docker import Docker, DockerError, client_environment, run_docker  # noqa: E402
from runner_engine import (  # noqa: E402
    AdminSecret, EngineAPIError, apply_engine_rules, collect_findings, engine_settings, load_engine_rules, load_vocabulary,
    start_task, task_request,
)
from runner_evidence import (  # noqa: E402
    DRAINED_MARKER, FORCED_CANCELLED_OUTCOME, SEALED_MARKER, EvidenceDir, EvidenceError, GatewayFiles, MeterFiles,
    e2_stub_counts, forwarded_by_method, forwarded_count, read_private_json, sequence_complete,
)
from attempt_reconcile import reconcile  # noqa: E402
from runner_state import copy_sqlite, discard_copy, memory_fingerprints, runtime_fingerprints  # noqa: E402
from state_diff import (  # noqa: E402
    RUN_KEY_BYTES, diff as state_difference, memory_diff, runtime_diff, snapshot as state_snapshot,
)
from runner_llm import judge_llm  # noqa: E402
from runner_probe import PASS, ProbeOutcome, run_probe  # noqa: E402
from runner_record import (  # noqa: E402
    RecordContext, canonical_sha256, descriptor, evidence_rows, isolation_evidence, isolation_proof, path_ref,
    run_record, solve_events, timeline,
)
from runner_window import (  # noqa: E402
    BackgroundWatch, HostWatch, events_reasons, judge_events, watch_reasons, watch_summary,
)
from snapshot_collector import SnapshotEndpoint, collect_snapshot  # noqa: E402
from snapshot_writer import SnapshotContext  # noqa: E402

RESULT_FORMAT = "abc2lab-eval-runner-result-v1"
TASK_EVIDENCE_FORMAT = "abc2lab-eval-runner-task-v1"
# 회차 메모리(엔진 DB·엔진 데이터 tmpfs) 사용량을 재기 전에 끝난 회차의 값. 잰 척하지 않고 미측정을 그대로 남긴다.
# 이 값인 회차는 상한 도달 여부를 알 수 없으므로 공식 비교에 쓰지 않는다(평가 프로토콜 §2).
TMPFS_NOT_MEASURED = "not_measured"
TMPFS_VOLUMES = ("engine_db", "engine_data")
RESULT_NAME = "runner-result.json"
RECORD_NAME = "run-record.json"  # v1.1 러너 기록(채점기 입력)
# 상태 차이 근거(프로토콜 §8.2, 러너 설정 state가 있을 때만). 건수·정수 id·테이블·열 이름·seq 값만 담는다.
STATE_DIFF_NAME = "state-diff.json"
STATE_DIFF_FORMAT = "abc2lab-state-diff-v1"
MAX_CONDITION_LENGTH = 100
COMPLETE, INVALID = "complete", "invalid"
# 이미지 역할 → 그 이미지를 쓰는 compose 서비스. 기동 후 컨테이너 이미지가 설정의 내용 ID와 같아야 한다.
# 회차에 없는 서비스(baseline의 E²)는 대조하지 않는다. 있어야 하는지는 망 구성 점검이 따로 본다.
_IMAGE_SERVICES = {"engine": (ENGINE_SERVICE,), "engine_db": (ENGINE_DB_SERVICE,), "target": (TARGET_SERVICE,),
                   "meter": (METER_SERVICE, OBS_SERVICE, LLM_GW_SERVICE, API_FRONT_SERVICE, E2_SERVICE)}


class RunFailure(Exception):
    """고정 사유 코드로 회차를 무효로 만든다."""

    def __init__(self, code: str):
        super().__init__(code)
        self.code = code


@dataclass(frozen=True)
class Facts:
    daemon: object
    networks: list
    containers: list
    running: list


class RunPorts(Protocol):
    def preexisting(self) -> bool: ...
    def image_ids(self) -> dict[str, str]: ...
    def up(self) -> None: ...
    def down(self) -> None: ...
    def facts(self) -> Facts: ...
    def host_listeners(self) -> frozenset[Listener]: ...
    def probe(self, phase: str, snapshot: dict) -> ProbeOutcome: ...
    def wait_ready(self) -> int: ...
    def scoreboard(self, phase: str, snapshot: dict) -> dict: ...
    def engine_settings(self) -> dict: ...
    def apply_engine_rules(self) -> dict: ...
    def host_spec(self) -> dict: ...
    def start_task(self) -> str: ...
    def start_host_watch(self) -> "WatchHandle": ...
    def start_events(self, since: str) -> "EventsHandle": ...
    def seal(self, meter_container_id: str) -> None: ...
    def seal_gateway(self, gateway_container_id: str) -> None: ...
    def tmpfs_usage(self, phase: str, snapshot: dict) -> dict: ...
    def state_copy(self, phase: str, snapshot: dict) -> Path: ...
    def state_trees(self, phase: str, snapshot: dict, key: bytes) -> dict: ...
    def state_memory(self, phase: str, snapshot: dict, key: bytes) -> list: ...
    def findings(self) -> dict: ...
    def sign(self, payload: dict) -> dict: ...


@dataclass(frozen=True)
class Clock:
    now: Callable[[], datetime]
    monotonic: Callable[[], float]
    sleep: Callable[[float], None]


@dataclass(frozen=True)
class RunSpec:
    run_id: str
    side: str
    project: str
    llm_backend: str
    expected: ExpectedTopology
    allowed_listens: frozenset[Listener]
    image_ids: dict[str, str]
    time_budget_seconds: float
    request_budget: int
    poll_seconds: int
    drain_timeout_seconds: int
    drain_grace_seconds: int
    post_settle_seconds: int
    llm_provider_slug: str
    llm_provider_name: str
    # 게이트웨이가 넣어야 할 샘플링(api, 선언에서 만든 값). local은 None.
    llm_sampling: dict | None
    llm_verify_timeout_seconds: int
    task_request_sha256: str
    host_watch_max_gap_seconds: float
    events_lookback_seconds: float
    # 선언한 엔진 LLM 정책 모드. baseline(e53a79f)에는 이 스위치가 없어 None(선언 없음)이다.
    llm_policy_mode: str | None
    # 러너 설정의 엔진 LLM 스트리밍(ARTEX_LLM_STREAM). 엔진 설정 API에 안 나와 설정 스냅샷 해시로는 차이를 못 잡는다.
    llm_stream: bool
    tmpfs_min_free_bytes: int
    worker_count: int
    condition: str
    repeat_index: int
    # 개선 후 회차의 E② 자리(runner_config E2_MODES). baseline은 항상 none이다.
    e2_mode: str
    model_tag: str
    fixture_fingerprint: str
    environment_fingerprint: str
    target_origin: str
    observer_path: str
    observer_readonly_ref: str
    # 상태 차이(프로토콜 §8.2)의 해결 신호 테이블. None이면 상태 사본을 뜨지 않는다(러너 설정 state=null).
    state_signal_tables: frozenset[str] | None = None
    # 상태 차이로 비교할 런타임 디렉터리. 비어 있으면 파일 사본을 뜨지 않는다.
    state_runtime_roots: tuple[str, ...] = ()
    # 메모리 저장 읽기를 할지(러너 설정 state.memory). 양쪽 회차에 같게 적용한다.
    state_memory_enabled: bool = False
    engine_rules_sha256: str | None = None  # 원본 ② 규칙 파일 해시. None이면 규칙을 넣지 않는다


class WatchHandle(Protocol):
    def stop(self) -> tuple: ...


class EventsHandle(Protocol):
    def stop(self, until_ns: int) -> tuple[list, bool]: ...


@dataclass
class _State:
    up_attempted: bool = False
    observer_calls: int = 0
    summary: dict = field(default_factory=dict)
    step: str = "start"
    watch: object | None = None
    events: object | None = None
    listeners: dict = field(default_factory=dict)
    probes: dict = field(default_factory=dict)
    # 러너가 띄운 probe 컨테이너 ID. 구간 감시가 이 ID의 생성·시작만 허용한다.
    runner_containers: set = field(default_factory=set)
    record: dict | None = None
    tmpfs_usage: object = TMPFS_NOT_MEASURED
    tmpfs_pre: dict | None = None
    host: dict | None = None
    engine_settings_sha256: str | None = None
    # 상태 차이: 회차 키(메모리에만)와 pre 사본 지문. 사본 파일은 지문을 뜬 즉시 지운다.
    state_key: bytes | None = None
    state_pre: dict | None = None
    state_trees_pre: dict = field(default_factory=dict)
    engine_rules: dict | None = None
    state_memory_pre: list = field(default_factory=list)


def _stamp(clock: Clock) -> str:
    return clock.now().astimezone(timezone.utc).isoformat()


def _event_time(moment: datetime) -> str:
    """docker events --since 형식(UTC, 나노초 자리까지)."""
    return moment.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f") + "000Z"


def _ns(moment: datetime) -> int:
    """docker 이벤트 timeNano와 같은 단위(UTC epoch 나노초). 마이크로초까지는 정확하다."""
    moment = moment.astimezone(timezone.utc)
    return (calendar.timegm(moment.utctimetuple()) * 1_000_000 + moment.microsecond) * 1000


def _stop_events(state: _State, until_ns: int) -> tuple:
    stream, state.events = state.events, None
    return stream.stop(until_ns)


def _stop_watch(state: _State) -> tuple:
    watch, state.watch = state.watch, None
    return watch.stop()


def _window(spec: RunSpec, evidence, state: _State, pre: dict, start: datetime, end: datetime,
            stopped: tuple, streamed: tuple) -> dict:
    """구간 감시 판정(runner_window). 위반이면 근거를 남긴 뒤 첫 사유로 무효."""
    started, finished, samples = stopped
    host = _step(state, "window_watch_failed",
                 lambda: watch_summary(started, finished, samples, spec.host_watch_max_gap_seconds))
    raw, stream_complete = streamed
    networks = {n["id"]: key for key, n in pre["networks"].items()}
    containers = {s["id"]: service for service, s in pre["services"].items()}
    events = _step(state, "window_events_failed", lambda: judge_events(
        raw, _ns(start), networks, containers, frozenset({METER_SERVICE, LLM_GW_SERVICE}),
        frozenset({ENGINE_DB_SERVICE}), frozenset(state.runner_containers), stream_complete))
    reasons = watch_reasons(host) + events_reasons(events)
    record = {"start": _event_time(start), "end": _event_time(end), "lookback_seconds": spec.events_lookback_seconds,
              "host_watch": host, "docker_events": events, "reasons": reasons}
    _step(state, "evidence_write_failed", lambda: evidence.write_json("window-watch.json", record))
    if reasons:
        raise RunFailure(reasons[0])
    return record


def _state_fingerprint(ports: RunPorts, key: bytes, phase: str, snapshot: dict) -> dict:
    copy = ports.state_copy(phase, snapshot)
    try:
        return state_snapshot(copy, key)
    finally:
        discard_copy(copy)


def _step(state: _State, code: str, action: Callable):
    """port 호출의 예외를 그 단계의 고정 사유로 바꾼다. 원래 메시지(경로·값 가능)는 버린다."""
    state.step = code
    try:
        return action()
    except RunFailure:
        raise
    except Exception:
        raise RunFailure(code) from None


def _check_phase(spec: RunSpec, ports: RunPorts, evidence, state: _State, phase: str) -> dict:
    facts = _step(state, f"{phase}_facts_failed", ports.facts)
    networks = _step(state, f"{phase}_facts_failed", lambda: [parse_network(n) for n in facts.networks])
    containers = _step(state, f"{phase}_facts_failed", lambda: [parse_container(c) for c in facts.containers])
    running = _step(state, f"{phase}_facts_failed", lambda: [parse_container(c) for c in facts.running])
    listeners = _step(state, f"{phase}_host_listeners_failed", ports.host_listeners)
    state.listeners[phase] = listeners_outside_loopback(listeners)
    topology = check_topology(spec.expected, spec.project, networks, containers)
    host = (check_other_publishes(spec.project, running) + check_namespace_sharing(containers, running)
            + check_host_listeners(listeners, spec.allowed_listens))
    snapshot = network_snapshot(facts.daemon, spec.project, networks, containers)
    images = sorted(f"service.{service}: IMAGE_MISMATCH" for role, services in _IMAGE_SERVICES.items()
                    for service in services if service in spec.expected.services
                    if snapshot["services"].get(service, {}).get("image") != spec.image_ids[role])
    _step(state, "evidence_write_failed", lambda: evidence.write_json(f"{phase}-checks.json", {
        "phase": phase, "snapshot": snapshot, "policy_sha256": policy_sha256(snapshot),
        "topology_violations": topology, "host_violations": host, "image_violations": images}))
    if topology or images:
        raise RunFailure(f"{phase}_topology_violation")
    if host:
        raise RunFailure(f"{phase}_host_condition_violation")
    return snapshot


def _probe(ports: RunPorts, evidence, state: _State, phase: str, snapshot: dict) -> str:
    outcome = _step(state, f"{phase}_probe_invalid", lambda: ports.probe(phase, snapshot))
    state.probes[phase] = [dict(row) for row in outcome.rows]
    if outcome.container_id:
        state.runner_containers.add(outcome.container_id)
    _step(state, "evidence_write_failed", lambda: evidence.write_json(f"{phase}-probe.json", {
        "phase": phase, "status": outcome.status, "reasons": list(outcome.reasons), "rows": list(outcome.rows),
        "started_at": outcome.started_at, "finished_at": outcome.finished_at, "container_id": outcome.container_id}))
    if outcome.status != PASS:
        raise RunFailure(f"{phase}_probe_{outcome.status}")
    return outcome.status


def _wait_budget(spec: RunSpec, meter: MeterFiles, clock: Clock, state: _State, started: float) -> tuple[str, int]:
    while True:
        forwarded = _step(state, "meter_read_failed", meter.forwarded)
        if forwarded >= spec.request_budget:
            return "request_budget", forwarded
        if clock.monotonic() - started >= spec.time_budget_seconds:
            return "time_budget", forwarded
        clock.sleep(spec.poll_seconds)


def _wait_drained(spec: RunSpec, meter: MeterFiles, clock: Clock, state: _State) -> dict:
    deadline = clock.monotonic() + spec.drain_timeout_seconds
    while True:
        drained = _step(state, "meter_read_failed", lambda: meter.marker(DRAINED_MARKER))
        if drained is not None:
            break
        if clock.monotonic() >= deadline:
            raise RunFailure("drain_timeout")
        clock.sleep(spec.poll_seconds)
    cancelled = drained.get("forced_cancelled_requests")
    if (drained.get("inflight_at_drain") != 0 or drained.get("log_complete") is not True
            or type(cancelled) is not int or cancelled < 0):
        raise RunFailure("drain_incomplete")
    return drained


def _wait_llm_verified(spec: RunSpec, gateway: GatewayFiles, clock: Clock, state: _State) -> dict:
    """게이트웨이는 drain 뒤 generation마다 조회해서 마커를 쓴다. 시한 안에 없으면 그 회차의 LLM 조건은 미검증이다."""
    deadline = clock.monotonic() + spec.llm_verify_timeout_seconds
    while True:
        verified = _step(state, "llm_read_failed", gateway.verified)
        if verified is not None:
            return verified
        if clock.monotonic() >= deadline:
            raise RunFailure("llm_unverified")
        clock.sleep(spec.poll_seconds)


def _run_steps(spec: RunSpec, ports: RunPorts, meter: MeterFiles, gateway: GatewayFiles, evidence, clock: Clock,
               state: _State) -> None:
    if _step(state, "project_check_failed", ports.preexisting):
        # 회차 project 이름은 회차마다 새로 만든다. 이미 있으면 이 러너 것이 아니므로 건드리지 않는다(down도 안 함).
        raise RunFailure("project_exists")
    found = _step(state, "image_id_failed", ports.image_ids)
    if found != spec.image_ids:
        raise RunFailure("image_id_mismatch")
    # baseline·개선 후가 같은 머신에서 돌았는지 채점기가 대조한다(평가 프로토콜 §5.1).
    state.host = _step(state, "host_spec_failed", ports.host_spec)
    if not _host_spec_ok(state.host):
        raise RunFailure("host_spec_failed")
    state.up_attempted = True
    _step(state, "compose_up_failed", ports.up)

    pre = _check_phase(spec, ports, evidence, state, "pre")
    _probe(ports, evidence, state, "pre", pre)
    # 구간 전에 회차 tmpfs의 마운트 ID를 잡아 둔다. post에서 같은 마운트 객체를 쟀는지 대조하는 기준이다(#140 리뷰).
    state.tmpfs_pre = _step(state, "pre_tmpfs_measure_failed", lambda: ports.tmpfs_usage("pre", pre))
    if set(state.tmpfs_pre) != set(TMPFS_VOLUMES):
        raise RunFailure("pre_tmpfs_measure_failed")
    pre_checked_at = _stamp(clock)  # 경계 적용·점검 완료(pre 관측 전)
    ready_calls = _step(state, "target_not_ready", ports.wait_ready)
    state.observer_calls += ready_calls
    fixture_ready_at = _stamp(clock)  # 러너에는 준비 채널이 없다: 대상 준비 확인이 준비 봉인 시점
    if spec.state_signal_tables is not None:
        # 상태 차이 pre 사본(§8.2). 대상 pause가 Docker 이벤트를 만드므로 구간 시작 전에 뜬다(구간 시작 전 lookback
        # 이벤트는 판정하지 않는다). 기동 쓰기는 대상 준비 확인 뒤라 끝났다고 본다.
        state.state_key = os.urandom(RUN_KEY_BYTES)
        state.state_pre = _step(state, "state_copy_failed",
                                lambda: _state_fingerprint(ports, state.state_key, "pre", pre))
        if spec.state_runtime_roots:
            state.state_trees_pre = _step(state, "state_copy_failed",
                                          lambda: ports.state_trees("pre", pre, state.state_key))
        if spec.state_memory_enabled:
            state.state_memory_pre = _step(state, "state_copy_failed",
                                           lambda: ports.state_memory("pre", pre, state.state_key))
    # 격리 구간은 pre 관측 직전부터 post 관측 완료까지다. 그 동안 호스트 리슨을 주기적으로 보고, 끝나면 데몬 이벤트를 받는다.
    window_start = clock.now()
    state.events = _step(state, "window_events_failed", lambda: ports.start_events(
        _event_time(window_start - timedelta(seconds=spec.events_lookback_seconds))))
    state.watch = _step(state, "window_watch_failed", ports.start_host_watch)
    state.observer_calls += 1
    pre_board = _step(state, "pre_scoreboard_failed", lambda: ports.scoreboard("pre", pre))
    pre_solved = pre_board.pop("solved")
    pre_forwarded = _step(state, "meter_read_failed", meter.forwarded)
    if pre_forwarded != 0:
        raise RunFailure("pre_requests_nonzero")  # 작업 시작 전에 엔진이 대상으로 보냈다
    # 작업 생성 전 실행 설정 전체. 선언과 다르거나 송신 경로를 바꾸는 값이 있으면 그 회차는 같은 조건이 아니다.
    if spec.engine_rules_sha256 is not None:
        # 원본 ② 규칙(프로토콜 §8.1). 실행 설정 스냅샷보다 먼저 넣어 스냅샷이 작업 직전 상태가 되게 한다.
        state.engine_rules = _step(state, "engine_rules_failed", ports.apply_engine_rules)
        if state.engine_rules.get("file_sha256") != spec.engine_rules_sha256:
            raise RunFailure("engine_rules_failed")
        _step(state, "evidence_write_failed", lambda: evidence.write_json("engine-rules.json", state.engine_rules))
    settings = _step(state, "engine_settings_failed", ports.engine_settings)
    if not _settings_match(settings, spec):
        raise RunFailure("engine_settings_mismatch")
    state.engine_settings_sha256 = canonical_sha256(settings)
    _step(state, "evidence_write_failed", lambda: evidence.write_json("engine-settings.json", settings))

    # 예산 시계는 생성 요청 직전부터 잰다(엔진은 생성과 동시에 기동하므로 요청 지연도 예산에 넣는다).
    requested_at, started = _stamp(clock), clock.monotonic()
    task_id = _step(state, "task_start_failed", ports.start_task)
    created_at = _stamp(clock)
    _step(state, "evidence_write_failed", lambda: evidence.write_json("task.json", {
        "format": TASK_EVIDENCE_FORMAT, "request_sha256": spec.task_request_sha256, "task_id": task_id,
        "requested_at": requested_at, "created_at": created_at}))
    stop_reason, forwarded_at_stop = _wait_budget(spec, meter, clock, state, started)
    stopped_at = _stamp(clock)
    _step(state, "seal_failed", lambda: ports.seal(pre["services"][METER_SERVICE]["id"]))
    engine_end_at = _stamp(clock)
    drained = _wait_drained(spec, meter, clock, state)
    drained_at = _stamp(clock)
    # 계측기가 끊은 요청은 대상 쪽 처리가 아직 끝나지 않았을 수 있다. post 관측 전에 설정한 만큼 기다리되, 그사이
    # 끝난 처리를 다 잡는다고 주장하지 않는다(평가 프로토콜 §2). 끊은 요청이 없으면 기다리지 않는다.
    settled = spec.post_settle_seconds if drained["forced_cancelled_requests"] else 0
    if settled:
        clock.sleep(settled)
    # 엔진의 작업은 끝났다. 게이트웨이도 새 LLM 요청을 막고, drain 뒤 응답 제공자 조회를 시작한다.
    _step(state, "llm_seal_failed", lambda: ports.seal_gateway(pre["services"][LLM_GW_SERVICE]["id"]))

    post = _check_phase(spec, ports, evidence, state, "post")
    post_check_at = _stamp(clock)
    if compare_snapshots(pre, post):
        raise RunFailure("boundary_changed")
    _probe(ports, evidence, state, "post", post)
    state.observer_calls += 1
    post_board = _step(state, "post_scoreboard_failed", lambda: ports.scoreboard("post", post))
    post_solved = post_board.pop("solved")
    stopped = _step(state, "window_watch_failed", lambda: _stop_watch(state))
    window_end = clock.now()
    streamed = _step(state, "window_events_failed", lambda: _stop_events(state, _ns(window_end)))
    window = _window(spec, evidence, state, pre, window_start, window_end, stopped, streamed)
    # 구간이 닫힌 뒤에 잰다: 구간 중 회차 컨테이너 exec는 경계 변경이다. seal 이후 값이므로 그사이 사용량이 줄었을
    # 가능성은 남는다(평가 프로토콜 §2). 남은 공간이 하한 미만이면 쓰기 실패를 엔진이 삼켰을 수 있어 무효다.
    measured = _step(state, "tmpfs_measure_failed", lambda: ports.tmpfs_usage("post", post))
    if set(measured) != set(TMPFS_VOLUMES):
        raise RunFailure("tmpfs_measure_failed")
    # 적용한 하한과 구간 전 측정도 서명되는 결과에 남겨 어떤 기준·어떤 마운트로 통과·무효였는지 다시 확인할 수 있게 한다.
    state.tmpfs_usage = dict(measured, min_free_bytes=spec.tmpfs_min_free_bytes, pre=state.tmpfs_pre)
    # 같은 경로라도 다른 마운트(부모 경로 변경·symlink·다시 마운트)를 쟀다면 회차 저장소의 근거가 아니다.
    if any(measured[name]["mount_id"] != state.tmpfs_pre[name]["mount_id"] for name in TMPFS_VOLUMES):
        raise RunFailure("tmpfs_mount_changed")
    if any(measured[name]["available_bytes"] < spec.tmpfs_min_free_bytes for name in TMPFS_VOLUMES):
        raise RunFailure("tmpfs_full")
    if spec.state_signal_tables is not None:
        # post 사본도 구간이 닫힌 뒤에 뜬다. 값 없이 건수·정수 id·이름만 근거로 남긴다(§8.2).
        post_state = _step(state, "state_copy_failed",
                           lambda: _state_fingerprint(ports, state.state_key, "post", post))
        post_trees = {}
        if spec.state_runtime_roots:
            post_trees = _step(state, "state_copy_failed", lambda: ports.state_trees("post", post, state.state_key))
        post_memory = []
        if spec.state_memory_enabled:
            post_memory = _step(state, "state_copy_failed", lambda: ports.state_memory("post", post, state.state_key))
        difference = _step(state, "state_diff_failed",
                           lambda: state_difference(state.state_pre, post_state, spec.state_signal_tables))
        runtime = _step(state, "state_diff_failed", lambda: runtime_diff(state.state_trees_pre, post_trees))
        memory = _step(state, "state_diff_failed", lambda: memory_diff(state.state_memory_pre, post_memory))
        # content_changed는 SQLite 쪽만이다. 전체 변화 여부는 state_changed로 본다.
        _step(state, "evidence_write_failed", lambda: evidence.write_json(STATE_DIFF_NAME, dict(
            difference, **runtime, **memory, format=STATE_DIFF_FORMAT,
            state_changed=(difference["content_changed"] or runtime["runtime_changed"]
                           or memory["memory_changed"]))))

    observer = _step(state, "meter_read_failed", meter.observer_rows)
    if len(observer) != state.observer_calls or any(row["decision"] != "registered" for row in observer):
        raise RunFailure("observer_count_mismatch")
    rows = _step(state, "meter_read_failed", meter.engine_rows)
    sealed = _step(state, "meter_read_failed", lambda: meter.marker(SEALED_MARKER))
    forwarded = _step(state, "meter_log_inconsistent", lambda: forwarded_count(rows))
    cancelled_rows = sum(1 for row in rows if row["outcome"] == FORCED_CANCELLED_OUTCOME)
    if (sealed is None or not sequence_complete(rows) or forwarded != drained.get("forwarded_requests")
            or cancelled_rows != drained["forced_cancelled_requests"]):
        raise RunFailure("meter_log_inconsistent")
    meter_summary = {
        "task_requested_at": requested_at, "task_created_at": created_at, "stopped_at": stopped_at,
        "stop_reason": stop_reason, "forwarded_at_stop": forwarded_at_stop,
        "sealed_at": sealed.get("sealed_at"), "drained_at": drained.get("drained_at"),
        "forwarded_requests": forwarded, "forwarded_by_method": forwarded_by_method(rows),
        "request_budget": spec.request_budget,
        # 폴링 뒤 seal이라 넘칠 수 있다. 정확 준수를 주장하지 않고 초과분을 그대로 남긴다.
        "over_budget": max(0, forwarded - spec.request_budget),
        "rejected_after_seal": drained.get("rejected_after_seal"),
        "forced_closed_streams": drained.get("forced_closed_streams"),
        # 봉인 뒤 유예가 지나 계측기가 끊은 요청. 전달은 됐으므로 forwarded_requests에 들어 있다.
        "forced_cancelled_requests": drained["forced_cancelled_requests"],
        # 유예·정착 대기는 선언과 대조한 정책 값, 적용 대기는 이 회차 결과(끊은 요청이 없으면 0)다.
        "drain_grace_seconds": spec.drain_grace_seconds, "post_settle_seconds": spec.post_settle_seconds,
        "post_settle_applied_seconds": settled,
        "time_budget_seconds": spec.time_budget_seconds, "observer_requests": len(observer),
    }
    _step(state, "evidence_write_failed", lambda: evidence.write_json("meter-summary.json", meter_summary))
    # E② 자리 기록. 가짜 E²면 결정 행 형식을 검사하고 결정별 건수를 남긴다(통제 증명이 아니라 배치 확인).
    e2 = {"mode": spec.e2_mode, "decision_rows": None, "allowed": None, "unsupported": None, "unavailable": None}
    reconciliation = None
    if spec.e2_mode == E2_STUB:
        decisions = _step(state, "e2_stub_log_invalid", meter.e2_stub_rows)
        e2.update(e2_stub_counts(decisions))
        # 시도별 대조(프로토콜 §8.3). 나쁜 칸이 나와도 회차를 무효로 하지 않는다(성능표에 포함, 통제 주장만 보류).
        reconciliation = _step(state, "e2_stub_log_invalid", lambda: reconcile(decisions, rows))
        _step(state, "evidence_write_failed",
              lambda: evidence.write_json("attempt-reconciliation.json", dict(reconciliation, fixture=True)))
    verified = _wait_llm_verified(spec, gateway, clock, state)
    llm_reasons, llm_summary = _step(state, "llm_marker_invalid", lambda: judge_llm(
        verified, spec.llm_backend, spec.llm_provider_slug, spec.llm_provider_name, spec.llm_sampling))
    _step(state, "evidence_write_failed", lambda: evidence.write_json("llm-summary.json",
                                                                      dict(llm_summary, reasons=llm_reasons)))
    if llm_reasons:
        # 사유가 여럿이면 정렬 첫째를 회차 사유로, 전부는 llm-summary.json에 남는다.
        raise RunFailure(llm_reasons[0])
    findings = _step(state, "findings_failed", ports.findings)
    _step(state, "evidence_write_failed", lambda: evidence.write_json("findings.json", findings))
    steps = timeline({"fixture_ready": fixture_ready_at, "pre": pre_board["received_at"], "engine_start": requested_at,
                      "engine_end": engine_end_at, "drained": drained_at, "post": post_board["received_at"]})
    # 채점기가 회차 사이에 대조하는 실행 조건: 엔진 설정 해시·적용 샘플링·머신·LLM 스트리밍.
    # 스트리밍은 엔진이 SSE와 비스트리밍 경로를 고르는 값이라 시간·응답 처리 조건이 달라진다(#174 리뷰). 선언값이다.
    conditions = {"engine_settings_sha256": state.engine_settings_sha256, "engine_rules_sha256": spec.engine_rules_sha256,
                  "workers": spec.worker_count,
                  "llm_stream": spec.llm_stream,
                  "sampling": llm_summary["sampling_applied"],
                  "host": {key: value for key, value in state.host.items() if key != "machine_id_sha256"},
                  "machine_id_sha256": state.host["machine_id_sha256"]}
    state.record = _step(state, "record_build_failed", lambda: _build_record(
        spec, evidence, state, pre, post, pre_board, post_board, pre_solved, post_solved, steps, window,
        observer, ready_calls, meter_summary, drained, len(rows), pre_checked_at, post_check_at, conditions, e2,
        reconciliation))
    state.summary = {"meter": meter_summary, "llm": llm_summary, "policy_sha256": policy_sha256(pre),
                     "run_conditions": conditions, "engine_rules": state.engine_rules,
                     "task_request_sha256": spec.task_request_sha256,
                     # 엔진이 현재 모드를 드러내지 않아 관측값은 없다(선언만). baseline은 스위치가 없어 선언도 null.
                     "engine_llm_policy": {"declared": spec.llm_policy_mode, "observed": None},
                     "window": {"host_watch": window["host_watch"], "events": {
                         key: window["docker_events"][key] for key in ("event_count", "kinds", "complete")}},
                     "daemon": pre["daemon"], "scoreboards": {"pre": pre_board, "post": post_board}}


def _build_record(spec: RunSpec, evidence, state: _State, pre: dict, post: dict, pre_board: dict, post_board: dict,
                  pre_solved: dict, post_solved: dict, steps: dict, window: dict, observer: list, ready_calls: int,
                  meter_summary: dict, drained: dict, engine_rows: int, pre_checked_at: str,
                  post_check_at: str, conditions: dict, e2: dict, reconciliation: dict | None) -> dict:
    """v1.1 러너 기록(서명 전)과 그 근거 파일 둘(격리 근거·해결 이벤트)을 만든다. 서명은 down 뒤."""
    context = RecordContext(run_id=spec.run_id, model_tag=spec.model_tag,
                            reset_generation=pre["services"][TARGET_SERVICE]["id"],
                            instance_id=pre["services"][TARGET_SERVICE]["id"],
                            execution_id=pre["services"][ENGINE_SERVICE]["id"])
    boundary_ref, host_ref = "compose:" + spec.project, "host-condition:" + spec.run_id
    get_ref = path_ref(spec.observer_path)
    policy = policy_sha256(pre)
    allowed = [{"address": item.address, "port": item.port} for item in spec.allowed_listens]
    rows = evidence_rows(
        pre_at=pre_checked_at, post_check_at=post_check_at, window=window, pre=pre, post=post,
        pre_probe=state.probes["pre"], post_probe=state.probes["post"], host_ref=host_ref,
        pre_listeners=state.listeners["pre"], post_listeners=state.listeners["post"], allowed=allowed,
        boundary_ref=boundary_ref, policy=policy, steps=steps, observer_rows=observer, ready_calls=ready_calls,
        observer_path_ref=get_ref, readonly_ref=spec.observer_readonly_ref, meter=meter_summary,
        drained=drained, engine_rows=engine_rows, conditions=conditions, e2=e2, reconciliation=reconciliation)
    evidence.write_json("isolation-evidence.json", isolation_evidence(context, rows))
    evidence_sha, evidence_size = evidence.describe("isolation-evidence.json")
    in_window = sum(1 for row in rows if row["kind"] == "observer_request" and row["window"] == "in_window")
    proof = isolation_proof(context, steps, pre_board["normalized"]["sha256"], post_board["normalized"]["sha256"],
                            spec.target_origin, boundary_ref, policy, host_ref, evidence_sha,
                            "eval-obs:" + pre["services"][OBS_SERVICE]["id"], get_ref, in_window)
    events = solve_events(context, pre_solved, post_solved, steps["post"], canonical_sha256(proof))
    evidence.write_json("solve-events.json", dict(context.fields(), phase="engine", events=events))
    events_sha, events_size = evidence.describe("solve-events.json")
    artifacts = {
        "pre_raw": pre_board["raw"], "pre": pre_board["normalized"],
        "post_raw": post_board["raw"], "post": post_board["normalized"],
        "records": None, "traffic": None, "coverage": None,  # 판정 미수집(run-record-v1.1 §3)
        "solve_events": descriptor(context, "solve-events.json", events_sha, events_size, "engine"),
        "isolation_evidence": descriptor(context, "isolation-evidence.json", evidence_sha, evidence_size, "engine"),
    }
    return run_record(context, engine=spec.side, condition=spec.condition, repeat_index=spec.repeat_index,
                      total_requests=meter_summary["forwarded_requests"],
                      fixture_fingerprint=spec.fixture_fingerprint,
                      environment_fingerprint=spec.environment_fingerprint, steps=steps, artifacts=artifacts,
                      proof=proof)


def execute_run(spec: RunSpec, ports: RunPorts, meter: MeterFiles, gateway: GatewayFiles, evidence,
                clock: Clock) -> dict:
    """회차 1회. 결과 기록(완료·서명 또는 무효)을 정확히 하나 쓰고 그 내용을 돌려준다."""
    state = _State()
    started_at = _stamp(clock)
    reasons: list[str] = []
    interrupted = False
    try:
        _run_steps(spec, ports, meter, gateway, evidence, clock, state)
    except RunFailure as exc:
        reasons.append(exc.code)
        # 점검 결과로 직접 실패한 경우(probe 무효·pre 요청 등) 마지막으로 시작한 단계가 아니라 그 사유의 단계다.
        state.step = exc.code
    except KeyboardInterrupt:
        reasons.append("interrupted")
        interrupted = True
    except Exception:
        reasons.append("unexpected_error")
    finally:
        if state.watch is not None:
            try:
                _stop_watch(state)  # 실패로 끝난 회차에서도 감시 스레드를 남기지 않는다
            except Exception:
                # 이미 무효 사유가 있다. 정리 실패는 기록을 바꾸지 않되 운영자에게 고정 코드로 알린다.
                print("window_watch_stop_failed", file=sys.stderr)
        if state.events is not None:
            try:
                _stop_events(state, 0)  # 이벤트 스트림 프로세스도 남기지 않는다(결과는 쓰지 않음)
            except Exception:
                print("window_events_stop_failed", file=sys.stderr)
        if state.up_attempted:
            try:
                ports.down()
            except Exception:
                reasons.append("down_failed")
    base = {"format": RESULT_FORMAT, "run_id": spec.run_id, "side": spec.side, "project": spec.project,
            "llm_backend": spec.llm_backend, "tmpfs_usage": state.tmpfs_usage,
            # E② 자리. 완료 결과에서는 서명된다. 세션 집계·채점이 가짜 E² 회차를 공식 결과에서 뺄 근거다.
            "e2_mode": spec.e2_mode,
            "started_at": started_at, "finished_at": _stamp(clock)}
    if not reasons and state.record is not None:
        # v1.1 러너 기록도 down까지 통과한 뒤에만 서명한다(엔진 감사 키와 별도인 러너 키).
        try:
            evidence.write_json(RECORD_NAME, ports.sign(state.record))
        except Exception:
            reasons.append("record_sign_failed")
    if not reasons:
        payload = dict(base, status=COMPLETE, images=dict(spec.image_ids), evidence=evidence.digests(),
                       **state.summary)
        try:
            result = ports.sign(payload)
        except Exception:
            reasons.append("sign_failed")
    if reasons:
        # 무효 기록에는 성공 필드(측정값·근거 목록·서명)를 넣지 않는다. 어느 단계였는지와 사유만.
        result = dict(base, status=INVALID, reasons=reasons, failed_step=state.step)
    evidence.write_json(RESULT_NAME, result)
    if interrupted:
        raise KeyboardInterrupt
    return result


# ---- 실제 연결(docker·HTTP·운영자). 판정 규칙은 위와 runner_checks·runner_probe에 있다. ----


HOST_SPEC_FIELDS = frozenset({"cpu_model", "logical_cpus", "memory_kib", "kernel", "docker_server_version",
                              "machine_id_sha256"})
_MACHINE_ID = re.compile(r"[0-9a-f]{32}\Z")
_HEX64 = re.compile(r"[0-9a-f]{64}\Z")
# 머신 ID 원문은 남기지 않는다. 같은 머신인지 대조할 해시만 남기고, 다른 용도의 해시와 섞이지 않게 접두를 붙인다.
MACHINE_ID_DOMAIN = "abc2lab-eval-machine-v1:"
# 엔진 송신 경로를 바꾸는 프록시 설정(runner_engine이 설정 여부로 바꾼 키).
_PROXY_SET = {"settings": ("web_search_proxy_set", "global_proxy_set"), "llm": ("proxy_set",)}


def read_host_spec(read_text: Callable[[str], str | None], docker_version: str, cpu_count: int | None,
                   kernel: str) -> dict:
    """평가 머신 사양. CPU 모델·논리 CPU·메모리·커널·Docker 버전과 머신 ID 해시. 읽지 못하면 고정 코드로 실패한다."""
    cpuinfo, meminfo, machine_id = (read_text(path) for path in ("/proc/cpuinfo", "/proc/meminfo", "/etc/machine-id"))
    models = [line.split(":", 1)[1].strip() for line in (cpuinfo or "").splitlines()
              if line.startswith("model name") and ":" in line]
    memory = [line.split()[1] for line in (meminfo or "").splitlines()
              if line.startswith("MemTotal:") and len(line.split()) == 3 and line.split()[2] == "kB"]
    machine = (machine_id or "").strip()
    if not models or not models[0] or len(memory) != 1 or not memory[0].isdigit() or not _MACHINE_ID.fullmatch(machine):
        raise RunFailure("host_spec_failed")
    return {"cpu_model": models[0], "logical_cpus": cpu_count, "memory_kib": int(memory[0]), "kernel": kernel,
            "docker_server_version": docker_version,
            "machine_id_sha256": hashlib.sha256((MACHINE_ID_DOMAIN + machine).encode("ascii")).hexdigest()}


def _host_spec_ok(host: object) -> bool:
    return (isinstance(host, dict) and set(host) == HOST_SPEC_FIELDS
            and all(isinstance(host[name], str) and host[name]
                    for name in ("cpu_model", "kernel", "docker_server_version"))
            and all(type(host[name]) is int and host[name] >= 1 for name in ("logical_cpus", "memory_kib"))
            and isinstance(host["machine_id_sha256"], str) and bool(_HEX64.fullmatch(host["machine_id_sha256"])))


def _settings_match(settings: object, spec: RunSpec) -> bool:
    """실행 설정 스냅샷이 선언과 맞고 송신 경로를 바꾸는 값이 없는지. workers는 선언 worker_count와 같아야 한다."""
    if not isinstance(settings, dict) or set(settings) != set(_PROXY_SET):
        return False
    engine, llm = settings["settings"], settings["llm"]
    proxies = [part[name] for part, names in ((engine, _PROXY_SET["settings"]), (llm, _PROXY_SET["llm"]))
               for name in names]
    return (type(engine.get("workers")) is int and engine["workers"] == spec.worker_count
            and llm.get("model") == spec.model_tag and all(value is False for value in proxies))


def _read_text(path: str) -> str | None:
    try:
        with open(path, encoding="ascii") as source:
            return source.read()
    except FileNotFoundError:
        return None


class DockerPorts:
    def __init__(self, config: RunnerConfig, plan: RunPlan, directory: RunDirectory, docker: Docker,
                 findings: Callable[[], dict], start: Callable[[], str], settings: Callable[[], dict],
                 rules: Callable[[], dict] | None = None):
        self.config, self.plan, self.directory, self.docker = config, plan, directory, docker
        self._findings, self._start, self._settings, self._rules = findings, start, settings, rules
        self.base_args = compose_base_args(plan)
        self.obs_origin = f"http://127.0.0.1:{config.ports['obs_host']}"

    def preexisting(self) -> bool:
        return bool(self.docker.project_network_ids(self.plan.project) or
                    self.docker.project_container_ids(self.plan.project))

    def image_ids(self) -> dict[str, str]:
        return {role: self.docker.image_id(image.ref) for role, image in self.plan.images.items()}

    def up(self) -> None:
        self.docker.compose_up(self.base_args, self.config.run["compose_wait_seconds"])

    def down(self) -> None:
        self.docker.compose_down(self.base_args, self.config.run["down_timeout_seconds"])

    def facts(self) -> Facts:
        daemon = self.docker.daemon()
        networks = self.docker.inspect_networks(self.docker.project_network_ids(self.plan.project))
        containers = self.docker.inspect_containers(self.docker.project_container_ids(self.plan.project))
        running = self.docker.inspect_containers(self.docker.running_container_ids())
        return Facts(daemon, networks, containers, running)

    def host_listeners(self) -> frozenset[Listener]:
        return read_host_listeners(_read_text)

    def probe(self, phase: str, snapshot: dict) -> ProbeOutcome:
        # cidfile은 회차 디렉터리(러너 uid·0700) 안 단계별 새 경로다. docker는 이미 있는 파일이면 실행을 거부한다.
        return run_probe(self.docker, self.config.images["meter"].id, snapshot, self.plan,
                         self.config.probe_timeout_ms, os.geteuid(), os.getegid(),
                         self.directory.run / f"{phase}-probe.cid")

    def wait_ready(self) -> int:
        """관측 front로 등록 GET을 200이 올 때까지. 응답을 받은 호출 수를 돌려준다(관측 로그 대조용)."""
        deadline = time.monotonic() + self.config.run["ready_timeout_seconds"]
        responses = 0
        while time.monotonic() < deadline:
            try:
                with closing(http.client.HTTPConnection("127.0.0.1", self.config.ports["obs_host"],
                                                        timeout=self.config.run["snapshot_timeout_seconds"])) as conn:
                    conn.request("GET", self.config.observer_path, headers={"Connection": "close"})
                    response = conn.getresponse()
                    responses += 1
                    response.read(self.config.run["snapshot_max_bytes"])
                    if response.status == 200:
                        return responses
            except (OSError, http.client.HTTPException):
                pass  # 관측 front가 아직 안 떴으면 연결 거부 — 관측 로그에도 안 남는다
            time.sleep(self.config.run["budget_poll_seconds"])
        raise RunFailure("target_not_ready")

    def scoreboard(self, phase: str, snapshot: dict) -> dict:
        context = SnapshotContext(run_id=self.plan.run_id, model_tag=self.plan.llm["model"],
                                  reset_generation=snapshot["services"][TARGET_SERVICE]["id"],
                                  instance_id=snapshot["services"][TARGET_SERVICE]["id"],
                                  execution_id=snapshot["services"][ENGINE_SERVICE]["id"])
        endpoint = SnapshotEndpoint(self.obs_origin, self.config.observer_path,
                                    self.config.run["snapshot_timeout_seconds"], self.config.run["snapshot_max_bytes"])
        collected = collect_snapshot(endpoint, self.directory.run, phase, context)
        # 해결 상태 맵(평가 전용 key)은 v1.1 이벤트 발행에만 쓰고 결과 요약에는 넣지 않는다.
        normalized = read_private_json(self.directory.run / phase, "normalized.json")
        if not isinstance(normalized, dict) or not isinstance(normalized.get("solved"), dict):
            raise EvidenceError("snapshot_normalized_invalid")
        return {"raw": collected.artifacts.raw, "normalized": collected.artifacts.normalized,
                "requested_at": collected.requested_at, "received_at": collected.received_at,
                "solved": normalized["solved"]}

    def engine_settings(self) -> dict:
        return self._settings()

    def apply_engine_rules(self) -> dict:
        return self._rules()

    def host_spec(self) -> dict:
        return read_host_spec(_read_text, self.docker.daemon().version, os.cpu_count(), platform.release())

    def start_task(self) -> str:
        return self._start()

    def start_host_watch(self) -> BackgroundWatch:
        watch = HostWatch(self.host_listeners, allowed_host_listens(self.config, self.plan), time.monotonic)
        return BackgroundWatch(watch, self.config.run["host_watch_interval_seconds"]).start()

    def start_events(self, since: str):
        return self.docker.event_stream(since)

    def seal(self, meter_container_id: str) -> None:
        self.docker.signal(meter_container_id, "USR1")

    def seal_gateway(self, gateway_container_id: str) -> None:
        self.docker.signal(gateway_container_id, "USR1")

    def tmpfs_usage(self, phase: str, snapshot: dict) -> dict:
        # 엔진 안의 df 대신 계측기 이미지의 statfs 역할로 잰다(엔진 도구가 바꿀 수 있는 실행파일을 믿지 않음).
        # 단계 구분 없이 같은 방법으로 재며, pre·post 대조는 호출자(_run_steps)가 한다.
        services, meter = snapshot["services"], self.config.images["meter"].id
        (engine_db,) = self.docker.statfs(meter, services[ENGINE_DB_SERVICE]["id"], [ENGINE_DB_DATA_PATH])
        (engine_data,) = self.docker.statfs(meter, services[ENGINE_SERVICE]["id"], [ENGINE_DATA_PATH])
        return {"engine_db": engine_db, "engine_data": engine_data}

    def state_copy(self, phase: str, snapshot: dict) -> Path:
        # 단계마다 새 0700 디렉터리. 사본 파일은 호출자가 지문을 뜬 즉시 지운다.
        dest = self.directory.state / phase
        os.mkdir(dest, 0o700)
        os.chmod(dest, 0o700)
        return copy_sqlite(self.docker, snapshot["services"][TARGET_SERVICE]["id"], self.config.state["sqlite_path"],
                           dest, self.config.state["max_copy_bytes"])

    def state_trees(self, phase: str, snapshot: dict, key: bytes) -> dict:
        return runtime_fingerprints(self.docker, snapshot["services"][TARGET_SERVICE]["id"],
                                    self.config.state["runtime_roots"], self.config.state["max_copy_bytes"], key,
                                    required=phase == "pre")

    def state_memory(self, phase: str, snapshot: dict, key: bytes) -> list:
        # 계측기 이미지의 state-read 역할이 대상 netns에서 읽는다(대상 안의 실행파일을 쓰지 않음).
        return memory_fingerprints(self.docker, self.config.images["meter"].id,
                                   snapshot["services"][TARGET_SERVICE]["id"], self.config.state["memory"],
                                   self.config.ports["target"], os.geteuid(), os.getegid(), key,
                                   required=phase == "pre")

    def findings(self) -> dict:
        return self._findings()

    def sign(self, payload: dict) -> dict:
        # 키는 서명 직전에만 읽는다. 경로는 러너 설정(회차 디렉터리 밖)이며 compose·환경으로 넘기지 않는다.
        return sign_run_record(payload, read_runner_key(str(self.config.runner_key_path)))


def build_spec(config: RunnerConfig, plan: RunPlan, preflight: dict, task_request_sha256: str, condition: str,
               repeat_index: int, engine_rules_sha256: str | None = None) -> RunSpec:
    declared = preflight[plan.side]
    return RunSpec(
        run_id=plan.run_id, side=plan.side, project=plan.project, llm_backend=plan.llm["backend"],
        expected=expected_topology(config, plan), allowed_listens=allowed_host_listens(config, plan),
        image_ids={role: image.id for role, image in plan.images.items()},
        time_budget_seconds=declared["time_budget_seconds"], request_budget=declared["request_budget"],
        poll_seconds=config.run["budget_poll_seconds"], drain_timeout_seconds=config.run["drain_timeout_seconds"],
        drain_grace_seconds=config.run["drain_grace_seconds"], post_settle_seconds=config.run["post_settle_seconds"],
        llm_provider_slug=plan.llm.get("provider", ""),
        llm_provider_name=config.llm_gateway["expected_provider_name"],
        llm_sampling=(json.loads(plan.compose_env["EVAL_LLM_SAMPLING"]) if plan.llm["backend"] == "api" else None),
        llm_verify_timeout_seconds=config.run["llm_verify_timeout_seconds"],
        task_request_sha256=task_request_sha256,
        host_watch_max_gap_seconds=config.run["host_watch_max_gap_seconds"],
        events_lookback_seconds=config.health_timeout_seconds,
        # compose는 두 엔진에 같은 값을 넘기지만 baseline은 읽지 않는다. 결과 기록에 external로 적으면 정책이 있는 것처럼
        # 읽히므로 선언하지 않는다(#133 리뷰).
        llm_policy_mode=config.llm_policy_mode if plan.side == POLICY_SWITCH_SIDE else None,
        llm_stream=config.llm_stream,
        tmpfs_min_free_bytes=config.tmpfs_min_free_bytes, worker_count=declared["worker_count"],
        condition=condition, repeat_index=repeat_index, e2_mode=plan.e2_mode, model_tag=plan.llm["model"],
        fixture_fingerprint=declared["fixture_fingerprint"], environment_fingerprint=declared["environment_fingerprint"],
        target_origin=declared["target_origin"], observer_path=config.observer_path,
        observer_readonly_ref=config.observer_readonly_ref,
        state_signal_tables=None if config.state is None else frozenset(config.state["signal_tables"]),
        state_runtime_roots=() if config.state is None else config.state["runtime_roots"],
        state_memory_enabled=config.state is not None and config.state["memory"] is not None,
        engine_rules_sha256=engine_rules_sha256)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="평가 회차 1회 실행(자동 재시도 없음)")
    parser.add_argument("--config", required=True)
    parser.add_argument("--preflight", required=True)
    parser.add_argument("--side", required=True, choices=("baseline", "ours"))
    # 측정 매트릭스 행 이름(예 "우리@35b")과 반복 번호. v1.1 기록에 서명되고 채점 manifest와 정확히 같아야 한다.
    parser.add_argument("--condition", required=True)
    parser.add_argument("--repeat", required=True, type=int)
    args = parser.parse_args(argv)
    if (not 0 < len(args.condition) <= MAX_CONDITION_LENGTH or args.condition.strip() != args.condition
            or any(ord(c) < 32 or ord(c) == 127 for c in args.condition)):
        print("condition: INVALID", file=sys.stderr)
        return 2
    if args.repeat < 0:
        print("repeat: INVALID", file=sys.stderr)
        return 2
    try:
        config = load_runner_config(args.config)
        preflight = load_preflight(args.preflight)
        plan = plan_run(config, preflight, args.side, new_run_id(args.side))
        task = task_request(config.task, preflight[plan.side]["target_origin"])
        vocabulary, vocabulary_sha256 = load_vocabulary(config.findings["vocabulary_path"])
        rules = None if config.engine_rules is None else load_engine_rules(config.engine_rules)
        # 설정이 통과한 뒤에만 묻는다. 비밀번호는 이 프로세스 메모리에만 있고 어디에도 쓰지 않는다.
        secret = AdminSecret(getpass.getpass("엔진 관리자 비밀번호(화면에 표시되지 않음): "))
        directory = create_run_directory(config.runs_root, plan.run_id)
    except (RunnerConfigError, PreflightError) as exc:
        print("\n".join(getattr(exc, "errors", ["config: INVALID"])), file=sys.stderr)
        return 2
    except EngineAPIError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    docker = Docker(run_docker, client_environment(os.environ, compose_environment(plan, directory)),
                    config.docker_timeout_seconds)

    def start() -> str:
        return start_task(config.ports["engine_api_host"], secret, task, config.run["task_create_timeout_seconds"])

    def findings() -> dict:
        return collect_findings(config.ports["engine_api_host"], secret, config.findings, vocabulary,
                                vocabulary_sha256, side=plan.side, build_source=plan.source_sha)

    def settings() -> dict:
        return engine_settings(config.ports["engine_api_host"], secret, config.run["task_create_timeout_seconds"])

    def apply_rules() -> dict:
        return apply_engine_rules(config.ports["engine_api_host"], secret, rules, config.run["task_create_timeout_seconds"])

    ports = DockerPorts(config, plan, directory, docker, findings, start, settings, apply_rules)
    clock = Clock(lambda: datetime.now(timezone.utc), time.monotonic, time.sleep)
    result = execute_run(build_spec(config, plan, preflight, task.sha256, args.condition, args.repeat,
                                    None if rules is None else rules.sha256), ports, MeterFiles(directory.meter, directory.e2),
                         GatewayFiles(directory.llm), EvidenceDir(directory.run), clock)
    print(f"{result['run_id']} {result['status']} {' '.join(result.get('reasons', []))}".rstrip())
    return 0 if result["status"] == COMPLETE else 1


if __name__ == "__main__":
    sys.exit(main())
