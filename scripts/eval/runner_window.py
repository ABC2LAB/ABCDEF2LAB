"""격리 구간(pre 관측 직전 ~ post 관측 완료) 감시와 판정.

pre·post 두 시점 점검은 끝까지 남는 변화(재시작·구성원 변경)만 잡는다. 구간 중 잠깐 붙었다 떨어진 연결이나
대상 컨테이너에 대한 exec는 두 시점 사이에 지나가므로, 러너 기록 v1.1 §3.1의 "연속 관측"을 두 가지로 채운다.

- 호스트 리슨: 이벤트가 없으므로 구간 동안 주기적으로 /proc/net을 읽는다. 표본 사이 최대 공백이 허용값을
  넘거나 허용되지 않은 리슨이 보이면 그 구간은 증명할 수 없다.
- 데몬 이벤트: 구간 시작에 `docker events --since <시작 − healthcheck timeout>` 스트림을 열어 두고 post 뒤에
  닫는다(데몬은 지난 이벤트를 256개만 보관해 구간 끝 일괄 조회로는 긴 구간을 못 덮는다). 회차 망·회차
  컨테이너에 생긴 일을 보며, 허용하는 것은 계측기·게이트웨이 seal(USR1)과 engine-db의 선언된 healthcheck뿐이다.
  회차 밖 컨테이너는 생성할 때 `--network container:<회차 컨테이너>`로 회차 컨테이너의 망 namespace를 같이 쓸 수
  있고 그때는 망 연결 이벤트가 없다. 이벤트에는 망 모드가 없으므로 구간 중 회차 밖 컨테이너의 생성·시작은 러너
  자신의 probe(--cidfile로 받은 ID)를 빼고 전부 경계 변경으로 본다(전용 평가 호스트, 프로토콜 §2). probe도
  `docker run --rm`의 정상 생명주기(생성·attach·시작·종료·삭제 각 한 번)만 허용한다.
  구간 시작 전(lookback) 이벤트는 판정하지 않고, 구간에 걸친 healthcheck exec의 ID를 알아 두는 데만 쓴다.
  스트림이 끊기거나 해석할 수 없거나, 시작 전 재생분이 버퍼 크기에 닿으면 불완전이다.

같은 호스트의 다른 프로세스까지 막았다는 뜻은 아니다(평가 프로토콜 §2 호스트 운영 조건).
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field
from typing import Callable, Iterable

from runner_checks import Listener, check_host_listeners
from runner_docker import RUNNER_HELPER_LABEL

# moby daemon/events: 지난 이벤트를 보관하는 버퍼 크기(eventsLimit, 11/03 실측 256). 스트림을 열 때 재생되는
# 구간 시작 전 이벤트가 이 수에 닿으면 앞쪽이 버려졌을 수 있다.
DOCKER_EVENTS_BUFFER = 256
# 계측기·게이트웨이 seal 신호(Linux SIGUSR1). docker events의 kill 이벤트 signal 속성은 숫자 문자열이다.
SEAL_SIGNAL = "10"
# compose의 engine-db healthcheck(CMD-SHELL). compose 파일과 같은지 테스트로 고정한다.
ENGINE_DB_HEALTHCHECK = '/bin/sh -c pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
_HEALTHY = "health_status: healthy"
_EXEC_CREATE, _EXEC_START, _EXEC_DIE = "exec_create: ", "exec_start: ", "exec_die"
# 회차 밖 컨테이너가 이 동작을 하면 새 namespace 공유가 생길 수 있다(공유는 생성 때 정해지고 시작으로 살아난다).
_FOREIGN_START = frozenset({"create", "start", "restart"})
# 러너 probe(`docker run --rm -i`)의 정상 생명주기. 같은 ID의 exec·kill·restart나 두 번째 시작은 러너가 하지 않는
# 일이므로 경계 변경이다(#126 리뷰).
_PROBE_LIFECYCLE = frozenset({"create", "attach", "start", "die", "destroy"})
# 러너 도우미 컨테이너(경계 원자료 꺼내기 등) 라벨은 runner_docker가 붙인다. 라벨만으로는 예외가 되지 않는다: 러너가 띄운
# ID(--cidfile)이면서 라벨 값이 그 ID에 정한 역할과 같아야 한다(다른 컨테이너가 같은 라벨을 달아도 예외가 아님).
# 생명주기는 probe와 같다.
# 러너가 구간 안에서 정지하는 회차 서비스(경계 원자료 꺼내기 전 경계 정지, 계약 §4.1)의 정상 정지. docker stop은
# kill(SIGTERM) → die → stop을 내고, 멈춘 컨테이너는 망에서 떨어진다. SIGKILL(시한 초과)·다시 시작은 위반이다.
STOP_SIGNAL = "15"
_STOP_LIFECYCLE = frozenset({"kill", "die", "stop"})


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


def healthcheck_actions(rendered: dict) -> dict[str, str]:
    """compose 렌더 결과(`docker compose config --format json`)에서 서비스마다 Docker가 healthcheck exec 이벤트에 적는 명령.

    Docker(Moby daemon/health.go)는 이벤트 동작을 `실행 파일 + " " + 인자들을 공백으로 이은 것`으로 적는다. 그래서 CMD
    [x, a, b]는 `x a b`, 인자 없는 CMD [x]는 끝 공백이 붙은 `x `, CMD-SHELL은 실행 파일 /bin/sh와 인자 [-c, 명령]이라
    `/bin/sh -c <명령>`이다(ENGINE_DB_HEALTHCHECK와 같은 규칙). NONE이거나 disable이면 healthcheck가 없다. 모양이 다르면
    거부한다(허용 목록이 실제와 어긋나면 정상 healthcheck가 위반으로 보인다)."""
    services = rendered.get("services") if isinstance(rendered, dict) else None
    if not isinstance(services, dict):
        raise WindowError("healthcheck_render_invalid")
    actions = {}
    for name, service in services.items():
        check = service.get("healthcheck") if isinstance(service, dict) else None
        if check is None or check.get("disable") is True:
            continue
        test = check.get("test")
        if (not isinstance(test, list) or not test or not all(isinstance(part, str) for part in test)):
            raise WindowError("healthcheck_render_invalid")
        if test == ["NONE"]:
            continue
        if test[0] == "CMD" and len(test) > 1:
            actions[name] = test[1] + " " + " ".join(test[2:])
        elif test[0] == "CMD-SHELL" and len(test) == 2:
            actions[name] = "/bin/sh" + " " + " ".join(["-c", test[1]])
        else:
            raise WindowError("healthcheck_render_invalid")
    return actions


def judge_events(events: list, window_start_ns: int, run_networks: dict[str, str], run_containers: dict[str, str],
                 sealable: frozenset[str], healthchecked: frozenset[str], runner_containers: frozenset[str],
                 stream_complete: bool, *, runner_helpers: dict[str, str] | None = None,
                 stoppable: frozenset[str] = frozenset(), healthchecks: dict[str, str] | None = None) -> dict:
    """구간 이벤트를 판정한다. run_networks·run_containers는 ID → 서비스(망 이름)다.

    window_start_ns보다 앞선 이벤트(lookback)는 healthcheck exec ID만 알아 두고 판정하지 않는다.
    sealable: seal 신호(USR1)를 받는 서비스. healthchecked: 선언된 healthcheck exec를 허용하는 서비스.
    healthchecks: 서비스 → 허용하는 healthcheck exec 명령(렌더 결과에서 healthcheck_actions로 만든다). 주면 healthchecked
    대신 이 표를 쓴다(탐색 분리 배치 O: 제품 서비스마다 healthcheck 명령이 다르다). 주지 않으면 healthchecked의 서비스에
    엔진 DB 명령(ENGINE_DB_HEALTHCHECK)만 허용한다(B 배치 그대로).
    runner_containers: 러너가 구간 중 직접 띄운 컨테이너 ID(post probe). 그 밖의 회차 밖 컨테이너 생성·시작은 위반.
    runner_helpers: 러너가 구간 중 띄운 도우미 ID → 역할. 그 ID이면서 RUNNER_HELPER_LABEL 값이 역할과 같을 때만 정상 생명주기를
    허용한다(라벨만 맞고 ID가 다르면 회차 밖 컨테이너로 본다). 도우미의 망 이벤트는 'none' 망 연결·분리 한 번씩만 허용하고,
    다른 망(회차 밖 망 포함)에 붙거나 떨어지면 위반이다.
    stoppable: 러너가 구간 중 정지하는 회차 서비스. SIGTERM 정지 한 번(kill·die·stop 각 한 번)과 그 컨테이너의 망 분리만 허용한다.
    결과에는 종류별 건수와 위반 요약(서비스 이름·동작)만 남기고 이벤트 원문은 남기지 않는다.
    """
    if not isinstance(events, list) or type(window_start_ns) is not int:
        raise WindowError("docker_events_invalid")
    checks = {service: ENGINE_DB_HEALTHCHECK for service in healthchecked} if healthchecks is None else dict(healthchecks)
    if not all(isinstance(command, str) and command for command in checks.values()):
        raise WindowError("docker_events_invalid")
    kinds: dict[str, int] = {}
    boundary: list[dict] = []
    allowed_exec_ids: set[str] = set()
    probe_seen: set[tuple[str, str]] = set()
    helpers = {} if runner_helpers is None else runner_helpers
    stopped: set[tuple[str, str]] = set()
    lookback = runner_events = 0
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
            command = checks.get(run_containers.get(actor_id, ""))
            if (kind == "container" and command is not None
                    and action in (_EXEC_CREATE + command, _EXEC_START + command)
                    and isinstance(attributes.get("execID"), str) and attributes["execID"]):
                allowed_exec_ids.add(attributes["execID"])
            continue
        kinds[f"{kind}.{label}"] = kinds.get(f"{kind}.{label}", 0) + 1
        if kind == "network":
            container = attributes.get("container")
            if container in helpers:
                # 도우미는 망 없이(--network none) 돈다. Docker가 그 null 망 연결·분리를 이벤트로 낼 수 있어 'none' 망만 한 번씩
                # 허용한다('none'은 미리 정의된 이름이라 같은 이름 망을 새로 만들 수 없다). 그 밖의 망은 회차 밖이어도 위반이다.
                if (label in ("connect", "disconnect") and attributes.get("name") == "none"
                        and attributes.get("type", "null") == "null" and (container, "net:" + label) not in probe_seen):
                    probe_seen.add((container, "net:" + label))
                    runner_events += 1
                else:
                    boundary.append({"type": kind, "action": label, "network": run_networks.get(actor_id, "foreign"),
                                     "service": "runner_helper"})
                continue
            if (label == "disconnect" and container in run_containers and run_containers[container] in stoppable
                    and (container, "die") in stopped and actor_id in run_networks
                    and (container, "net:" + actor_id) not in stopped):
                stopped.add((container, "net:" + actor_id))  # 정지한 서비스가 회차 망에서 떨어지는 것(망마다 한 번)
                continue
            if actor_id in run_networks or container in run_containers:
                who = run_containers.get(container, "foreign") if container else None
                boundary.append({"type": kind, "action": label, "network": run_networks.get(actor_id, "foreign"),
                                 "service": who})
            continue
        if kind != "container":
            continue
        if actor_id not in run_containers:
            if actor_id in helpers:
                if (attributes.get(RUNNER_HELPER_LABEL) == helpers[actor_id] and action in _PROBE_LIFECYCLE
                        and (actor_id, action) not in probe_seen):
                    probe_seen.add((actor_id, action))
                    runner_events += 1
                else:
                    boundary.append({"type": kind, "action": label, "service": "runner_helper"})
            elif actor_id in runner_containers:
                if action in _PROBE_LIFECYCLE and (actor_id, action) not in probe_seen:
                    probe_seen.add((actor_id, action))
                    runner_events += 1
                else:
                    boundary.append({"type": kind, "action": label, "service": "runner_probe"})
            elif label in _FOREIGN_START:
                boundary.append({"type": kind, "action": label, "service": "foreign"})
            continue
        service = run_containers[actor_id]
        if label == "kill" and service in sealable and attributes.get("signal") == SEAL_SIGNAL:
            continue
        if (service in stoppable and label in _STOP_LIFECYCLE and (actor_id, label) not in stopped
                and (label != "kill" or attributes.get("signal") == STOP_SIGNAL)
                and (label == "kill" or (actor_id, "kill") in stopped)):
            stopped.add((actor_id, label))  # SIGTERM 다음에만 die·stop
            continue
        if service in checks:
            if action in (_EXEC_CREATE + checks[service], _EXEC_START + checks[service]):
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
            "runner_container_events": runner_events, "boundary_events": boundary, "stream_complete": stream_complete,
            "complete": stream_complete and lookback < DOCKER_EVENTS_BUFFER}


def events_reasons(summary: dict) -> list[str]:
    reasons = []
    if summary["boundary_events"]:
        reasons.append("window_boundary_changed")
    if not summary["complete"]:
        reasons.append("window_events_incomplete")
    return reasons
