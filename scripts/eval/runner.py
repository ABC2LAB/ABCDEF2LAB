"""평가 러너 회차 순서(평가 프로토콜 §2).

  기동(대상·계측기·관측·engine-db·엔진) → pre 점검(5b)·probe(5c) → 대상 준비 → pre scoreboard
  → 계측기 전달 0 확인 → 작업 생성(엔진 API, 생성=기동) → 시간·요청 예산 → 계측기 seal(USR1) → drained 확인
  → LLM 게이트웨이 seal → post 점검·probe → post scoreboard → 관측·계측 기록 대조
  → 게이트웨이 검증 마커 대기·응답 제공자 판정 → findings → down → 서명

어느 단계든 실패하면 고정 사유 코드로 "무효" 기록 하나만 남기고 성공 기록·서명은 만들지 않는다.
down은 이 회차가 up을 시도했다면 성공·실패와 무관하게 실행한다(down 실패도 사유). 자동 재시도는 없다.
재실행은 새 run_id다. 서명은 down까지 모두 통과한 뒤 마지막에 한 번, 회차 디렉터리 밖의 키로 한다.

요청 예산은 폴링 뒤 seal하므로 넘칠 수 있다. 실제 전달 건수와 초과분을 기록하며 "정확 준수"를 주장하지 않는다.
"""

from __future__ import annotations

import argparse
import getpass
import http.client
import os
import sys
import time
from contextlib import closing
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Protocol

# python3 -I(-P 포함)는 스크립트 디렉터리를 sys.path에 올리지 않는다(score.py와 같은 이유). 같은 디렉터리의
# 평가 모듈(우리 코드)만 명시적으로 올린다 — cwd·사용자 site-packages는 계속 차단된다.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from preflight import PreflightError, load_preflight  # noqa: E402
from run_record import read_runner_key, sign_run_record  # noqa: E402
from runner_checks import (  # noqa: E402
    ExpectedTopology, Listener, allowed_host_listens, check_host_listeners, check_other_publishes, check_topology,
    compare_snapshots, expected_topology, network_snapshot, parse_container, parse_network, policy_sha256,
    read_host_listeners,
)
from runner_config import (  # noqa: E402
    API_FRONT_SERVICE, ENGINE_DB_SERVICE, ENGINE_SERVICE, LLM_GW_SERVICE, METER_SERVICE, OBS_SERVICE, TARGET_SERVICE,
    RunDirectory, RunnerConfig,
    RunnerConfigError, RunPlan, compose_base_args, compose_environment, create_run_directory, load_runner_config,
    new_run_id, plan_run,
)
from runner_docker import Docker, DockerError, client_environment, run_docker  # noqa: E402
from runner_engine import (  # noqa: E402
    AdminSecret, EngineAPIError, collect_findings, load_vocabulary, start_task, task_request,
)
from runner_evidence import (  # noqa: E402
    DRAINED_MARKER, SEALED_MARKER, EvidenceDir, GatewayFiles, MeterFiles, sequence_complete,
)
from runner_llm import judge_llm  # noqa: E402
from runner_probe import PASS, ProbeOutcome, run_probe  # noqa: E402
from snapshot_collector import SnapshotEndpoint, collect_snapshot  # noqa: E402
from snapshot_writer import SnapshotContext  # noqa: E402

RESULT_FORMAT = "abc2lab-eval-runner-result-v1"
TASK_EVIDENCE_FORMAT = "abc2lab-eval-runner-task-v1"
# 회차 메모리(엔진 DB·엔진 데이터 tmpfs)의 seal 시점 사용량은 아직 재지 않는다. 잰 척하지 않고 미측정을 그대로
# 남긴다. 이 값인 회차는 상한 도달 여부를 알 수 없으므로 공식 비교에 쓰지 않는다(평가 프로토콜 §2).
TMPFS_NOT_MEASURED = "not_measured"
RESULT_NAME = "runner-result.json"
COMPLETE, INVALID = "complete", "invalid"
# 이미지 역할 → 그 이미지를 쓰는 compose 서비스. 기동 후 컨테이너 이미지가 설정의 내용 ID와 같아야 한다.
_IMAGE_SERVICES = {"engine": (ENGINE_SERVICE,), "engine_db": (ENGINE_DB_SERVICE,), "target": (TARGET_SERVICE,),
                   "meter": (METER_SERVICE, OBS_SERVICE, LLM_GW_SERVICE, API_FRONT_SERVICE)}


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
    def probe(self, snapshot: dict) -> ProbeOutcome: ...
    def wait_ready(self) -> int: ...
    def scoreboard(self, phase: str, snapshot: dict) -> dict: ...
    def start_task(self) -> str: ...
    def seal(self, meter_container_id: str) -> None: ...
    def seal_gateway(self, gateway_container_id: str) -> None: ...
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
    llm_provider_slug: str
    llm_provider_name: str
    llm_verify_timeout_seconds: int
    task_request_sha256: str


@dataclass
class _State:
    up_attempted: bool = False
    observer_calls: int = 0
    summary: dict = field(default_factory=dict)
    step: str = "start"


def _stamp(clock: Clock) -> str:
    return clock.now().astimezone(timezone.utc).isoformat()


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
    topology = check_topology(spec.expected, spec.project, networks, containers)
    host = check_other_publishes(spec.project, running) + check_host_listeners(listeners, spec.allowed_listens)
    snapshot = network_snapshot(facts.daemon, spec.project, networks, containers)
    images = sorted(f"service.{service}: IMAGE_MISMATCH" for role, services in _IMAGE_SERVICES.items()
                    for service in services
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
    outcome = _step(state, f"{phase}_probe_invalid", lambda: ports.probe(snapshot))
    _step(state, "evidence_write_failed", lambda: evidence.write_json(f"{phase}-probe.json", {
        "phase": phase, "status": outcome.status, "reasons": list(outcome.reasons), "rows": list(outcome.rows),
        "started_at": outcome.started_at, "finished_at": outcome.finished_at}))
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
    if drained.get("inflight_at_drain") != 0 or drained.get("log_complete") is not True:
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
    state.up_attempted = True
    _step(state, "compose_up_failed", ports.up)

    pre = _check_phase(spec, ports, evidence, state, "pre")
    _probe(ports, evidence, state, "pre", pre)
    state.observer_calls += _step(state, "target_not_ready", ports.wait_ready)
    state.observer_calls += 1
    pre_board = _step(state, "pre_scoreboard_failed", lambda: ports.scoreboard("pre", pre))
    pre_forwarded = _step(state, "meter_read_failed", meter.forwarded)
    if pre_forwarded != 0:
        raise RunFailure("pre_requests_nonzero")  # 작업 시작 전에 엔진이 대상으로 보냈다

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
    drained = _wait_drained(spec, meter, clock, state)
    # 엔진의 작업은 끝났다. 게이트웨이도 새 LLM 요청을 막고, drain 뒤 응답 제공자 조회를 시작한다.
    _step(state, "llm_seal_failed", lambda: ports.seal_gateway(pre["services"][LLM_GW_SERVICE]["id"]))

    post = _check_phase(spec, ports, evidence, state, "post")
    if compare_snapshots(pre, post):
        raise RunFailure("boundary_changed")
    _probe(ports, evidence, state, "post", post)
    state.observer_calls += 1
    post_board = _step(state, "post_scoreboard_failed", lambda: ports.scoreboard("post", post))

    observer = _step(state, "meter_read_failed", meter.observer_rows)
    if len(observer) != state.observer_calls or any(row["decision"] != "registered" for row in observer):
        raise RunFailure("observer_count_mismatch")
    rows = _step(state, "meter_read_failed", meter.engine_rows)
    sealed = _step(state, "meter_read_failed", lambda: meter.marker(SEALED_MARKER))
    forwarded = sum(1 for row in rows if row["kind"] == "request")
    if sealed is None or not sequence_complete(rows) or forwarded != drained.get("forwarded_requests"):
        raise RunFailure("meter_log_inconsistent")
    meter_summary = {
        "task_requested_at": requested_at, "task_created_at": created_at, "stopped_at": stopped_at,
        "stop_reason": stop_reason, "forwarded_at_stop": forwarded_at_stop,
        "sealed_at": sealed.get("sealed_at"), "drained_at": drained.get("drained_at"),
        "forwarded_requests": forwarded, "request_budget": spec.request_budget,
        # 폴링 뒤 seal이라 넘칠 수 있다. 정확 준수를 주장하지 않고 초과분을 그대로 남긴다.
        "over_budget": max(0, forwarded - spec.request_budget),
        "rejected_after_seal": drained.get("rejected_after_seal"),
        "forced_closed_streams": drained.get("forced_closed_streams"),
        "time_budget_seconds": spec.time_budget_seconds, "observer_requests": len(observer),
    }
    _step(state, "evidence_write_failed", lambda: evidence.write_json("meter-summary.json", meter_summary))
    verified = _wait_llm_verified(spec, gateway, clock, state)
    llm_reasons, llm_summary = _step(state, "llm_marker_invalid", lambda: judge_llm(
        verified, spec.llm_backend, spec.llm_provider_slug, spec.llm_provider_name))
    _step(state, "evidence_write_failed", lambda: evidence.write_json("llm-summary.json",
                                                                      dict(llm_summary, reasons=llm_reasons)))
    if llm_reasons:
        # 사유가 여럿이면 정렬 첫째를 회차 사유로, 전부는 llm-summary.json에 남는다.
        raise RunFailure(llm_reasons[0])
    findings = _step(state, "findings_failed", ports.findings)
    _step(state, "evidence_write_failed", lambda: evidence.write_json("findings.json", findings))
    state.summary = {"meter": meter_summary, "llm": llm_summary, "policy_sha256": policy_sha256(pre),
                     "task_request_sha256": spec.task_request_sha256,
                     "daemon": pre["daemon"], "scoreboards": {"pre": pre_board, "post": post_board}}


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
    except KeyboardInterrupt:
        reasons.append("interrupted")
        interrupted = True
    except Exception:
        reasons.append("unexpected_error")
    finally:
        if state.up_attempted:
            try:
                ports.down()
            except Exception:
                reasons.append("down_failed")
    base = {"format": RESULT_FORMAT, "run_id": spec.run_id, "side": spec.side, "project": spec.project,
            "llm_backend": spec.llm_backend, "tmpfs_usage": TMPFS_NOT_MEASURED,
            "started_at": started_at, "finished_at": _stamp(clock)}
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


def _read_text(path: str) -> str | None:
    try:
        with open(path, encoding="ascii") as source:
            return source.read()
    except FileNotFoundError:
        return None


class DockerPorts:
    def __init__(self, config: RunnerConfig, plan: RunPlan, directory: RunDirectory, docker: Docker,
                 findings: Callable[[], dict], start: Callable[[], str]):
        self.config, self.plan, self.directory, self.docker = config, plan, directory, docker
        self._findings, self._start = findings, start
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

    def probe(self, snapshot: dict) -> ProbeOutcome:
        return run_probe(self.docker, self.config.images["meter"].id, snapshot, self.plan,
                         self.config.probe_timeout_ms, os.geteuid(), os.getegid())

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
        return {"raw": collected.artifacts.raw, "normalized": collected.artifacts.normalized,
                "requested_at": collected.requested_at, "received_at": collected.received_at}

    def start_task(self) -> str:
        return self._start()

    def seal(self, meter_container_id: str) -> None:
        self.docker.signal(meter_container_id, "USR1")

    def seal_gateway(self, gateway_container_id: str) -> None:
        self.docker.signal(gateway_container_id, "USR1")

    def findings(self) -> dict:
        return self._findings()

    def sign(self, payload: dict) -> dict:
        # 키는 서명 직전에만 읽는다. 경로는 러너 설정(회차 디렉터리 밖)이며 compose·환경으로 넘기지 않는다.
        return sign_run_record(payload, read_runner_key(str(self.config.runner_key_path)))


def build_spec(config: RunnerConfig, plan: RunPlan, preflight: dict, task_request_sha256: str) -> RunSpec:
    declared = preflight[plan.side]
    return RunSpec(
        run_id=plan.run_id, side=plan.side, project=plan.project, llm_backend=plan.llm["backend"],
        expected=expected_topology(config, plan), allowed_listens=allowed_host_listens(config, plan),
        image_ids={role: image.id for role, image in plan.images.items()},
        time_budget_seconds=declared["time_budget_seconds"], request_budget=declared["request_budget"],
        poll_seconds=config.run["budget_poll_seconds"], drain_timeout_seconds=config.run["drain_timeout_seconds"],
        llm_provider_slug=plan.llm.get("provider", ""),
        llm_provider_name=config.llm_gateway["expected_provider_name"],
        llm_verify_timeout_seconds=config.run["llm_verify_timeout_seconds"],
        task_request_sha256=task_request_sha256)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="평가 회차 1회 실행(자동 재시도 없음)")
    parser.add_argument("--config", required=True)
    parser.add_argument("--preflight", required=True)
    parser.add_argument("--side", required=True, choices=("baseline", "ours"))
    args = parser.parse_args(argv)
    try:
        config = load_runner_config(args.config)
        preflight = load_preflight(args.preflight)
        plan = plan_run(config, preflight, args.side, new_run_id(args.side))
        task = task_request(config.task, preflight[plan.side]["target_origin"])
        vocabulary, vocabulary_sha256 = load_vocabulary(config.findings["vocabulary_path"])
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

    ports = DockerPorts(config, plan, directory, docker, findings, start)
    clock = Clock(lambda: datetime.now(timezone.utc), time.monotonic, time.sleep)
    result = execute_run(build_spec(config, plan, preflight, task.sha256), ports, MeterFiles(directory.meter),
                         GatewayFiles(directory.llm), EvidenceDir(directory.run), clock)
    print(f"{result['run_id']} {result['status']} {' '.join(result.get('reasons', []))}".rstrip())
    return 0 if result["status"] == COMPLETE else 1


if __name__ == "__main__":
    sys.exit(main())
