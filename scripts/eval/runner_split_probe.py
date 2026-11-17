"""탐색 분리 배치(개선 후 O) 회차의 여러 출발점 망 점검(탐색 분리 배치 결정안 §2.1, 순서 4-8).

현행 러너 점검(runner_probe)은 엔진 netns 하나에서만 본다. 이 배치는 제품 엔진(preparation)·탐색 경계·브라우저·MCP가 각자
다른 망에 붙으므로, 출발점마다 그 컨테이너 netns에 점검 컨테이너를 붙여(`docker run --network container:<출발>`) 결정안 표의
기대값과 대조한다. 판정 규칙(양성 대조 실패 = 무효, 음성 경로 성공 = 위반, 실패 종류 불일치 = 무효)은 runner_probe와 같다.

목적지는 두 곳에서 읽고 서로 맞는지 본다(설정값을 그대로 목적지로 쓰면 실제로 떠 있는 것과 다른 곳을 점검할 수 있다):
- 주소: 떠 있는 컨테이너 스냅샷(docker inspect, runner_checks.network_snapshot).
- 포트·이름: overlay 렌더 결과(compose config)의 명령, 그리고 그 명령과 짝인 보호 입력(경계 설정·설치 탐색 블록). 짝끼리 다르면
  점검을 만들지 않는다(그 상태로는 제품이 다른 곳에 연결한다).
제품 설계상 닿는 경로(제품 엔진·경계의 외부, 경계의 계측기 본 입구)는 판정하지 않고 결과만 남긴다(RECORD).
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable
from urllib.parse import urlsplit

from runner_docker import Docker, DockerError
from runner_probe import (
    INVALID, MAX_PROBE_CHECKS, NEGATIVE, PASS, POSITIVE, RECORD, TCP, VIOLATION, PathExpectation, ProbeOutcome,
    ProbeSetupError, _CONNECTED, _IMAGE_ID, _NO_EXTERNAL, _NO_PATH, _hostport, _invalid, evaluate_probe, probe_plan,
)

PREPARATION, BOUNDARY, BROWSER, MCP = "preparation", "explore-boundary", "explore-browser", "explore-mcp"
ORIGINS = (PREPARATION, BOUNDARY, BROWSER, MCP)
METER, TARGET = "eval-meter", "target"
# 계측기 두 입구가 붙는 망(overlay deploy/eval/compose.split.yaml). 망 이름은 compose 망 키다.
METER_MAIN_NETWORK, METER_EXPLORE_NETWORK = "target", "eval_explore_ingress"
BROWSER_INGRESS_NETWORK, CDP_NETWORK = "explore_browser_ingress", "explore_cdp"
_ANY = frozenset()  # RECORD 행은 허용 결과를 따지지 않는다


@dataclass(frozen=True)
class SplitPorts:
    """렌더·보호 입력에서 읽은 입구(서로 대조한 값)."""
    meter_main: tuple[str, int]      # 계측기 본 입구(주소, 포트)
    meter_explore: tuple[str, int]   # 계측기 탐색 입구
    target_upstream: int             # 계측기가 대상에 넘기는 포트
    mcp: tuple[str, int]             # 제품 엔진이 부르는 MCP(이름, 포트)
    direct: tuple[str, int]          # 제품 엔진이 부르는 경계 직접 입구(이름, 포트)
    browser_ingress: tuple[str, int]  # 브라우저가 부르는 경계 브라우저 입구(이름, 포트)
    browser_listen: tuple[str, int]  # 경계 브라우저 입구 리슨(주소, 포트)
    cdp: tuple[str, int]             # MCP가 부르는 브라우저 CDP(주소, 포트)


def _command(rendered: dict, service: str) -> list[str]:
    command = rendered.get("services", {}).get(service, {}).get("command")
    if not isinstance(command, list) or not all(isinstance(item, str) for item in command):
        raise ProbeSetupError(f"split_command_missing:{service}")
    return command


def _flag(command: list[str], name: str, service: str) -> str:
    """`-name=value` 또는 `--name value` 하나. 둘 이상이거나 없으면 거부한다."""
    found = [item.split("=", 1)[1] for item in command if item.startswith(name + "=")]
    found += [command[i + 1] for i, item in enumerate(command[:-1]) if item == name]
    if len(found) != 1:
        raise ProbeSetupError(f"split_flag_invalid:{service}:{name.lstrip('-')}")
    return found[0]


def _host_port(value: str, code: str) -> tuple[str, int]:
    host, sep, port = value.rpartition(":")
    if not sep or not host or not port.isdecimal() or not 0 < int(port) < 65536:
        raise ProbeSetupError(code)
    return host, int(port)


def _url(value: object, code: str) -> tuple[str, int]:
    if not isinstance(value, str):
        raise ProbeSetupError(code)
    parts = urlsplit(value)
    try:
        port = parts.port
    except ValueError:
        port = None
    if parts.scheme != "http" or not parts.hostname or port is None:
        raise ProbeSetupError(code)
    return parts.hostname, port


def split_ports(rendered: dict, boundary: dict, exploration: dict) -> SplitPorts:
    """overlay 렌더 결과·경계 설정·설치 탐색 블록에서 입구를 읽고 짝끼리 대조한다."""
    meter = _command(rendered, METER)
    meter_main = _host_port(_flag(meter, "-listen", METER), "split_meter_listen_invalid")
    meter_explore = _host_port(_flag(meter, "-explore-listen", METER), "split_meter_listen_invalid")
    target_upstream = _url(_flag(meter, "-upstream", METER), "split_meter_upstream_invalid")[1]
    mcp = _url(exploration.get("mcp_url"), "split_exploration_invalid")
    if int(_flag(_command(rendered, MCP), "--port", MCP)) != mcp[1]:
        raise ProbeSetupError("split_mcp_port_mismatch")
    direct = _url(exploration.get("direct_proxy"), "split_exploration_invalid")
    if _host_port(str(boundary.get("direct_listen")), "split_boundary_invalid")[1] != direct[1]:
        raise ProbeSetupError("split_direct_port_mismatch")
    browser_ingress = _url(_flag(_command(rendered, BROWSER), "--proxy-server", BROWSER), "split_browser_proxy_invalid")
    browser_listen = _host_port(str(boundary.get("browser_listen")), "split_boundary_invalid")
    if browser_listen[1] != browser_ingress[1]:
        raise ProbeSetupError("split_browser_port_mismatch")
    cdp = _url(_flag(_command(rendered, MCP), "--cdp-endpoint", MCP), "split_cdp_invalid")
    if _host_port(_flag(_command(rendered, BROWSER), "--cdp-listen", BROWSER), "split_cdp_invalid") != cdp:
        raise ProbeSetupError("split_cdp_mismatch")
    return SplitPorts(meter_main, meter_explore, target_upstream, mcp, direct, browser_ingress, browser_listen, cdp)


def _members(snapshot: dict, network: str, service: str) -> list[str]:
    found = snapshot["networks"].get(network, {}).get("members", {}).get(service, [])
    return sorted(found)


def _one(snapshot: dict, network: str, service: str, expected: str) -> str:
    """스냅샷에서 그 망의 서비스 주소 하나. 렌더 주소와 다르면(실제로 다른 곳에 떠 있음) 점검을 만들지 않는다."""
    found = _members(snapshot, network, service)
    if found != [expected]:
        raise ProbeSetupError(f"split_address_mismatch:{service}:{network}")
    return expected


def _target_rows(snapshot: dict, ports: SplitPorts) -> list[PathExpectation]:
    addresses = sorted({a for network in snapshot["networks"].values() for a in network["members"].get(TARGET, [])})
    if not addresses:
        raise ProbeSetupError("target_address_unknown")
    exposed = {int(key.split("/")[0]) for key in snapshot["services"].get(TARGET, {}).get("exposed", []) if key.endswith("/tcp")}
    return [PathExpectation(f"target_ip:{_hostport(a, p)}", TCP, _hostport(a, p), NEGATIVE, _NO_PATH)
            for a in addresses for p in sorted(exposed | {ports.target_upstream})]


def split_probe_expectations(snapshot: dict, ports: SplitPorts, external: str) -> dict[str, tuple[PathExpectation, ...]]:
    """출발점별 기대값(결정안 §2.1 표). external은 외부 견본 목적지(호스트:포트, 모델 API 주소)다."""
    _host_port(external, "split_external_invalid")
    meter_main = _hostport(_one(snapshot, METER_MAIN_NETWORK, METER, ports.meter_main[0]), ports.meter_main[1])
    meter_explore = _hostport(_one(snapshot, METER_EXPLORE_NETWORK, METER, ports.meter_explore[0]), ports.meter_explore[1])
    ingress = _hostport(_one(snapshot, BROWSER_INGRESS_NETWORK, BOUNDARY, ports.browser_listen[0]), ports.browser_listen[1])
    cdp = _hostport(_one(snapshot, CDP_NETWORK, BROWSER, ports.cdp[0]), ports.cdp[1])
    targets = _target_rows(snapshot, ports)
    no_meter = [PathExpectation("meter_main", TCP, meter_main, NEGATIVE, _NO_PATH),
                PathExpectation("meter_explore", TCP, meter_explore, NEGATIVE, _NO_PATH)]
    no_external = [PathExpectation("external", TCP, external, NEGATIVE, _NO_EXTERNAL)]
    plan = {
        PREPARATION: [
            PathExpectation("meter_main", TCP, meter_main, POSITIVE, _CONNECTED),
            PathExpectation("mcp", TCP, _hostport(*ports.mcp), POSITIVE, _CONNECTED),
            PathExpectation("boundary_direct", TCP, _hostport(*ports.direct), POSITIVE, _CONNECTED),
            PathExpectation("meter_explore", TCP, meter_explore, NEGATIVE, _NO_PATH),
            PathExpectation("boundary_browser_ingress", TCP, ingress, NEGATIVE, _NO_PATH),
            PathExpectation("browser_cdp", TCP, cdp, NEGATIVE, _NO_PATH),
            *targets,
            # 모델 호출 경로(결정안 D(b)): 제품 설계라 판정하지 않고 남긴다.
            PathExpectation("external", TCP, external, RECORD, _ANY),
        ],
        BOUNDARY: [
            PathExpectation("meter_explore", TCP, meter_explore, POSITIVE, _CONNECTED),
            *targets,
            # 제품 compose가 경계를 target·frontend 망에도 붙인다. 평가 upstream 모드에서 쓰지 않는 경로라 결과만 남긴다.
            PathExpectation("meter_main", TCP, meter_main, RECORD, _ANY),
            PathExpectation("external", TCP, external, RECORD, _ANY),
        ],
        BROWSER: [PathExpectation("boundary_browser_ingress", TCP, _hostport(*ports.browser_ingress), POSITIVE, _CONNECTED),
                  *no_meter, *targets, *no_external],
        MCP: [PathExpectation("browser_cdp", TCP, cdp, POSITIVE, _CONNECTED), *no_meter, *targets, *no_external],
    }
    for origin, rows in plan.items():
        if len(rows) > MAX_PROBE_CHECKS or len({row.id for row in rows}) != len(rows):
            raise ProbeSetupError(f"probe_plan_invalid:{origin}")
    return {origin: tuple(rows) for origin, rows in plan.items()}


@dataclass(frozen=True)
class SplitProbeOutcome:
    status: str
    reasons: tuple[str, ...]
    origins: dict[str, ProbeOutcome]

    @property
    def container_ids(self) -> tuple[str, ...]:
        """러너가 띄운 점검 컨테이너 ID들(구간 감시가 러너 것으로 허용)."""
        return tuple(o.container_id for o in self.origins.values() if o.container_id)


def _stamp(now: Callable[[], datetime]) -> str:
    return now().astimezone(timezone.utc).isoformat()


def run_split_probes(docker: Docker, meter_image_id: str, snapshot: dict, ports: SplitPorts, external: str, timeout_ms: int,
                     uid: int, gid: int, cid_dir: Path,
                     now: Callable[[], datetime] = lambda: datetime.now(timezone.utc)) -> SplitProbeOutcome:
    """출발점마다 점검 1회. 하나라도 무효면 무효, 무효 없이 위반이 있으면 위반이다. 도출 실패는 전부 무효다."""
    if not isinstance(meter_image_id, str) or not _IMAGE_ID.fullmatch(meter_image_id):
        return SplitProbeOutcome(INVALID, ("probe_image_not_id",), {})
    try:
        plan = split_probe_expectations(snapshot, ports, external)
    except ProbeSetupError as exc:
        return SplitProbeOutcome(INVALID, (str(exc),), {})
    outcomes: dict[str, ProbeOutcome] = {}
    for origin in ORIGINS:
        expectations = plan[origin]
        started = _stamp(now)
        container = snapshot["services"].get(origin, {}).get("id", "")
        container_id = None
        try:
            output, container_id = docker.probe(meter_image_id, container, probe_plan(expectations, timeout_ms), uid, gid,
                                                cid_dir / f"probe-{origin}.cid")
        except DockerError:
            outcome = _invalid(expectations, "probe_run_failed")
        else:
            outcome = evaluate_probe(expectations, output)
        outcomes[origin] = ProbeOutcome(outcome.status, outcome.reasons, outcome.rows, started, _stamp(now), container_id)
    statuses = {o.status for o in outcomes.values()}
    status = INVALID if INVALID in statuses else VIOLATION if VIOLATION in statuses else PASS
    reasons = tuple(sorted({f"{origin}:{reason}" for origin, o in outcomes.items() for reason in o.reasons}))
    return SplitProbeOutcome(status, reasons, outcomes)
