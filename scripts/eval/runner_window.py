"""격리 구간(pre 관측 직전 ~ post 관측 완료) 감시와 판정.

pre·post 두 시점 점검은 끝까지 남는 변화(재시작·구성원 변경)만 잡는다. 구간 중 잠깐 붙었다 떨어진 연결이나
대상 컨테이너에 대한 exec는 두 시점 사이에 지나가므로, 러너 기록 v1.1 §3.1의 "연속 관측"을 두 가지로 채운다.

- 호스트 리슨: 이벤트가 없으므로 구간 동안 주기적으로 /proc/net을 읽는다. 표본 사이 최대 공백이 허용값을
  넘거나 허용되지 않은 리슨이 보이면 그 구간은 증명할 수 없다.
- 데몬 이벤트: 구간 시작에 `docker events --since <시작 − healthcheck timeout>` 스트림을 열어 두고 post 뒤에
  닫는다(데몬은 지난 이벤트를 256개만 보관해 구간 끝 일괄 조회로는 긴 구간을 못 덮는다). 회차 망·회차
  컨테이너에 생긴 일을 보며, 허용하는 것은 계측기·게이트웨이 seal(USR1)과 engine-db의 선언된 healthcheck뿐이다.
  구간 시작 전(lookback) 이벤트는 판정하지 않고, 구간에 걸친 healthcheck exec의 ID를 알아 두는 데만 쓴다.
  스트림이 끊기거나 해석할 수 없거나, 시작 전 재생분이 버퍼 크기에 닿으면 불완전이다.

같은 호스트의 다른 프로세스까지 막았다는 뜻은 아니다(평가 프로토콜 §2 호스트 운영 조건).
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field
from typing import Callable, Iterable

from runner_checks import Listener, check_host_listeners

# moby daemon/events: 지난 이벤트를 보관하는 버퍼 크기(eventsLimit, 11/03 실측 256). 스트림을 열 때 재생되는
# 구간 시작 전 이벤트가 이 수에 닿으면 앞쪽이 버려졌을 수 있다.
DOCKER_EVENTS_BUFFER = 256
# 계측기·게이트웨이 seal 신호(Linux SIGUSR1). docker events의 kill 이벤트 signal 속성은 숫자 문자열이다.
SEAL_SIGNAL = "10"
# compose의 engine-db healthcheck(CMD-SHELL). compose 파일과 같은지 테스트로 고정한다.
ENGINE_DB_HEALTHCHECK = '/bin/sh -c pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
_HEALTHY = "health_status: healthy"
_EXEC_CREATE, _EXEC_START, _EXEC_DIE = "exec_create: ", "exec_start: ", "exec_die"


class WindowError(ValueError):
    """구간 감시 입력이 예상한 모양이 아니다. 고정 코드만 담는다."""


@dataclass(frozen=True)
class WatchSample:
    monotonic: float
    violations: tuple[str, ...]
    error: str | None


@dataclass
class HostWatch:
    """구간 동안 호스트 리슨을 표본으로 모은다. sample()은 스레드 하나에서만 부른다."""

    read_listeners: Callable[[], frozenset[Listener]]
    allowed: frozenset[Listener]
    monotonic: Callable[[], float]
    samples: list[WatchSample] = field(default_factory=list)

    def sample(self) -> None:
        at = self.monotonic()
        try:
            violations = tuple(check_host_listeners(self.read_listeners(), self.allowed))
            self.samples.append(WatchSample(at, violations, None))
        except Exception:
            # 읽지 못한 표본은 확인한 것이 아니다. 공백으로 두지 않고 오류로 남겨 증명을 막는다.
            self.samples.append(WatchSample(at, (), "host_listen_read_failed"))


class BackgroundWatch:
    """HostWatch를 스레드에서 주기적으로 돌린다. stop()은 마지막 표본을 하나 더 찍고 멈춘다."""

    def __init__(self, watch: HostWatch, interval_seconds: float):
        if not interval_seconds > 0:
            raise WindowError("host_watch_interval_invalid")
        self.watch, self.interval = watch, interval_seconds
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._loop, name="host-watch", daemon=True)
        self.started_at: float | None = None

    def start(self) -> "BackgroundWatch":
        self.started_at = self.watch.monotonic()
        self.watch.sample()
        self._thread.start()
        return self

    def _loop(self) -> None:
        while not self._stop.wait(self.interval):
            self.watch.sample()

    def stop(self) -> tuple[float, float, list[WatchSample]]:
        self._stop.set()
        self._thread.join()
        self.watch.sample()  # 구간 끝 = 마지막 표본 시각
        return self.started_at, self.watch.samples[-1].monotonic, list(self.watch.samples)


def watch_summary(started: float, finished: float, samples: Iterable[WatchSample], allowed_max_gap: float) -> dict:
    """표본 사이와 구간 양끝까지의 최대 공백, 위반·오류를 요약한다. 시각은 단조 시계(초)다."""
    samples = sorted(samples, key=lambda item: item.monotonic)
    points = [started, *(item.monotonic for item in samples), finished]
    gaps = [later - earlier for earlier, later in zip(points, points[1:])]
    if any(gap < 0 for gap in gaps) or not samples:
        raise WindowError("host_watch_samples_invalid")
    violations = sorted({violation for item in samples for violation in item.violations})
    errors = sorted({item.error for item in samples if item.error})
    return {"samples": len(samples), "window_seconds": round(finished - started, 3),
            "max_gap_seconds": round(max(gaps), 3), "allowed_max_gap_seconds": allowed_max_gap,
            "violations": violations, "errors": errors}


def watch_reasons(summary: dict) -> list[str]:
    reasons = []
    if summary["violations"] or summary["errors"]:
        reasons.append("window_host_condition_violation")
    if summary["max_gap_seconds"] > summary["allowed_max_gap_seconds"]:
        reasons.append("window_host_watch_gap")
    return reasons


def _text(value: object, code: str) -> str:
    if not isinstance(value, str):
        raise WindowError(code)
    return value


def judge_events(events: list, window_start_ns: int, run_networks: dict[str, str], run_containers: dict[str, str],
                 sealable: frozenset[str], healthchecked: frozenset[str], stream_complete: bool) -> dict:
    """구간 이벤트를 판정한다. run_networks·run_containers는 ID → 서비스(망 이름)다.

    window_start_ns보다 앞선 이벤트(lookback)는 healthcheck exec ID만 알아 두고 판정하지 않는다.
    sealable: seal 신호(USR1)를 받는 서비스. healthchecked: 선언된 healthcheck exec를 허용하는 서비스.
    결과에는 종류별 건수와 위반 요약(서비스 이름·동작)만 남기고 이벤트 원문은 남기지 않는다.
    """
    if not isinstance(events, list) or type(window_start_ns) is not int:
        raise WindowError("docker_events_invalid")
    kinds: dict[str, int] = {}
    boundary: list[dict] = []
    allowed_exec_ids: set[str] = set()
    lookback = 0
    for event in events:  # 데몬이 시각 순서로 보낸다(재생분 다음 실시간)
        if not isinstance(event, dict):
            raise WindowError("docker_events_invalid")
        kind = _text(event.get("Type"), "docker_events_invalid")
        action = _text(event.get("Action"), "docker_events_invalid")
        actor = event.get("Actor")
        if not isinstance(actor, dict):
            raise WindowError("docker_events_invalid")
        actor_id = _text(actor.get("ID"), "docker_events_invalid")
        attributes = actor.get("Attributes")
        attributes = {} if attributes is None else attributes  # 속성 없는 이벤트는 키가 빠질 수 있다
        if not isinstance(attributes, dict):
            raise WindowError("docker_events_invalid")
        stamp = event.get("timeNano")
        if type(stamp) is not int:
            raise WindowError("docker_events_invalid")
        label = action.split(":", 1)[0]
        if stamp < window_start_ns:
            lookback += 1
            if (kind == "container" and run_containers.get(actor_id) in healthchecked
                    and action in (_EXEC_CREATE + ENGINE_DB_HEALTHCHECK, _EXEC_START + ENGINE_DB_HEALTHCHECK)
                    and isinstance(attributes.get("execID"), str) and attributes["execID"]):
                allowed_exec_ids.add(attributes["execID"])
            continue
        kinds[f"{kind}.{label}"] = kinds.get(f"{kind}.{label}", 0) + 1
        if kind == "network":
            container = attributes.get("container")
            if actor_id in run_networks or container in run_containers:
                who = run_containers.get(container, "foreign") if container else None
                boundary.append({"type": kind, "action": label, "network": run_networks.get(actor_id, "foreign"),
                                 "service": who})
            continue
        if kind != "container" or actor_id not in run_containers:
            continue
        service = run_containers[actor_id]
        if label == "kill" and service in sealable and attributes.get("signal") == SEAL_SIGNAL:
            continue
        if service in healthchecked:
            if action in (_EXEC_CREATE + ENGINE_DB_HEALTHCHECK, _EXEC_START + ENGINE_DB_HEALTHCHECK):
                exec_id = attributes.get("execID")
                if isinstance(exec_id, str) and exec_id:
                    allowed_exec_ids.add(exec_id)
                    continue
            if action == _EXEC_DIE and attributes.get("execID") in allowed_exec_ids:
                continue
            if action == _HEALTHY:
                continue
        boundary.append({"type": kind, "action": label, "service": service})
    return {"event_count": len(events) - lookback, "lookback_events": lookback, "kinds": dict(sorted(kinds.items())),
            "boundary_events": boundary, "stream_complete": stream_complete,
            "complete": stream_complete and lookback < DOCKER_EVENTS_BUFFER}


def events_reasons(summary: dict) -> list[str]:
    reasons = []
    if summary["boundary_events"]:
        reasons.append("window_boundary_changed")
    if not summary["complete"]:
        reasons.append("window_events_incomplete")
    return reasons
