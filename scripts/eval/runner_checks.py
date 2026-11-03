"""평가 러너의 망·바인딩·호스트 조건 점검(평가 프로토콜 §2).

입력은 docker inspect와 /proc/net에서 뽑은 사실이고, 기대값은 회차 계획(LLM 백엔드·구성)에서 도출한다.
도출은 expected_topology·allowed_host_listens 두 곳에만 둔다. 구성이 바뀌면(예: 평가용 LLM 게이트웨이)
점검 코드가 아니라 도출 규칙만 바꾼다. 점검은 어긋남 코드 목록만 돌려주고, 증명 발행 여부는 러너 순서가 정한다.

호스트 리슨 점검 범위는 러너가 도는 Linux 네트워크 namespace의 TCP LISTEN 소켓(/proc/net/tcp·tcp6)까지다.
WSL 바깥(Windows)·다른 VM(Docker Desktop의 데몬 VM 등)·UDP는 보지 않는다.
"""

from __future__ import annotations

import hashlib
import ipaddress
import json
import re
import sys
from dataclasses import dataclass
from typing import Callable, Iterable, Mapping
from urllib.parse import urlsplit

from runner_config import (
    API_FRONT_SERVICE, ENGINE_API_NET, ENGINE_DB_SERVICE, ENGINE_NET, ENGINE_SERVICE, LLM_EGRESS_NET, LLM_GW_SERVICE,
    METER_SERVICE, OBS_NET, OBS_SERVICE, TARGET_NET, TARGET_SERVICE, RunnerConfig, RunPlan,
)
from runner_docker import DaemonIdentity

PROJECT_LABEL = "com.docker.compose.project"
NETWORK_LABEL = "com.docker.compose.network"
LOOPBACK_V4 = "127.0.0.1"
_CONTAINER_ID = re.compile(r"[0-9a-f]{64}\Z")
_CONTAINER_FIELDS = frozenset({"id", "project", "service", "image", "running", "started_at", "restart_count",
                               "ports", "networks"})
_PORT_KEY = re.compile(r"[1-9][0-9]{0,4}/(?:tcp|udp|sctp)\Z")
_TCP_LISTEN = "0A"
PROC_TCP = "/proc/net/tcp"
PROC_TCP6 = "/proc/net/tcp6"


class CheckInputError(ValueError):
    """docker·/proc 출력이 예상 구조가 아님. 고정 코드만 담는다."""


@dataclass(frozen=True, order=True)
class Binding:
    container_port: str
    host_ip: str
    host_port: int


@dataclass(frozen=True)
class NetworkExpectation:
    internal: bool
    members: frozenset[str]


@dataclass(frozen=True)
class ExpectedTopology:
    networks: Mapping[str, NetworkExpectation]
    publishes: Mapping[str, frozenset[Binding]]

    @property
    def services(self) -> frozenset[str]:
        names = set(self.publishes)
        for network in self.networks.values():
            names |= network.members
        return frozenset(names)


@dataclass(frozen=True)
class ContainerFact:
    id: str
    project: str
    service: str
    image: str
    running: bool
    # 재시작해도 ID·이미지는 그대로라 이 둘로 회차 중 재시작(계측기 카운터 유실·대상 상태 초기화·tmpfs 소실)을 잡는다.
    started_at: str
    restart_count: int
    # 컨테이너가 여는 포트(EXPOSE·publish 모두, 예: "3000/tcp"). 부정 연결 점검의 목적지 포트로 쓴다.
    exposed: frozenset[str]
    bindings: frozenset[Binding]
    network_ids: frozenset[str]


@dataclass(frozen=True)
class NetworkFact:
    id: str
    name: str
    project: str
    key: str
    internal: bool
    members: Mapping[str, tuple[str, ...]]
    gateways: tuple[str, ...]
    # 컨테이너 이름(compose는 project 접두어를 붙인다). Docker 내장 DNS가 서비스 이름과 함께 해석하는 이름이다.
    member_names: Mapping[str, str]


@dataclass(frozen=True, order=True)
class Listener:
    address: str
    port: int


def expected_topology(config: RunnerConfig, plan: RunPlan) -> ExpectedTopology:
    """평가 프로토콜 §2 망 표를 회차 계획에서 도출한다. api·local은 같은 망 구성이다(차이는 게이트웨이 설정뿐).

    엔진은 internal 망 하나에만 붙고, 바깥 경로는 게이트웨이, 엔진 API publish는 API front가 맡는다.
    """
    if plan.llm["backend"] not in ("api", "local"):
        raise CheckInputError("llm_backend_unsupported")
    ports = config.ports
    return ExpectedTopology(
        networks={
            ENGINE_NET: NetworkExpectation(True, frozenset({ENGINE_SERVICE, ENGINE_DB_SERVICE, METER_SERVICE,
                                                            LLM_GW_SERVICE, API_FRONT_SERVICE})),
            TARGET_NET: NetworkExpectation(True, frozenset({TARGET_SERVICE, METER_SERVICE, OBS_SERVICE})),
            LLM_EGRESS_NET: NetworkExpectation(False, frozenset({LLM_GW_SERVICE})),
            ENGINE_API_NET: NetworkExpectation(False, frozenset({API_FRONT_SERVICE})),
            OBS_NET: NetworkExpectation(False, frozenset({OBS_SERVICE})),
        },
        publishes={
            ENGINE_SERVICE: frozenset(),
            ENGINE_DB_SERVICE: frozenset(),
            TARGET_SERVICE: frozenset(),
            METER_SERVICE: frozenset(),
            LLM_GW_SERVICE: frozenset(),
            API_FRONT_SERVICE: frozenset({Binding(f"{ports['api_front']}/tcp", LOOPBACK_V4, ports["engine_api_host"])}),
            OBS_SERVICE: frozenset({Binding(f"{ports['obs']}/tcp", LOOPBACK_V4, ports["obs_host"])}),
        },
    )


def _url_port(url: str) -> int:
    parts = urlsplit(url)
    return parts.port or {"http": 80, "https": 443}[parts.scheme]


def allowed_host_listens(config: RunnerConfig, plan: RunPlan) -> frozenset[Listener]:
    """loopback 밖 호스트 리슨 허용 집합. local이면 LLM 리슨 하나, api면 없음. 그 외는 설정 허용 목록뿐이다."""
    allowed = {Listener(entry.address, entry.port) for entry in config.host_listen_allowlist}
    if plan.llm["backend"] == "local":
        allowed.add(Listener(plan.llm["host_ip"], _url_port(plan.llm["base_url"])))
    elif plan.llm["backend"] != "api":
        raise CheckInputError("llm_backend_unsupported")
    return frozenset(allowed)


def _str(value: object, code: str) -> str:
    if not isinstance(value, str):
        raise CheckInputError(code)
    return value


def _ports(raw: object) -> tuple[frozenset[str], frozenset[Binding]]:
    if raw is None:
        return frozenset(), frozenset()
    if not isinstance(raw, dict):
        raise CheckInputError("container_ports_invalid")
    result = set()
    for key, entries in raw.items():
        if not isinstance(key, str) or not _PORT_KEY.fullmatch(key):
            raise CheckInputError("container_ports_invalid")
        if entries is None:
            continue  # EXPOSE만 하고 publish하지 않은 포트
        if not isinstance(entries, list):
            raise CheckInputError("container_ports_invalid")
        for entry in entries:
            if not isinstance(entry, dict):
                raise CheckInputError("container_ports_invalid")
            host_ip, host_port = entry.get("HostIp"), entry.get("HostPort")
            if not isinstance(host_ip, str) or not isinstance(host_port, str) or not host_port.isdigit():
                raise CheckInputError("container_ports_invalid")
            result.add(Binding(key, host_ip, int(host_port)))
    return frozenset(raw), frozenset(result)


def _label(value: object) -> str:
    """compose 라벨 값. compose 밖 컨테이너는 라벨이 없어 템플릿이 null을 낸다(11/03 실측) → 빈 문자열(회차 밖)."""
    return "" if value is None else _str(value, "container_fact_invalid")


def parse_container(raw: object) -> ContainerFact:
    """runner_docker.CONTAINER_TEMPLATE 출력 1건. 환경변수 등 비밀이 들 수 있는 필드는 받지 않는다."""
    if not isinstance(raw, dict) or set(raw) != _CONTAINER_FIELDS:
        raise CheckInputError("container_fact_invalid")
    container_id = _str(raw["id"], "container_fact_invalid")
    started_at = _str(raw["started_at"], "container_fact_invalid")
    restart_count = raw["restart_count"]
    if (not _CONTAINER_ID.fullmatch(container_id) or type(raw["running"]) is not bool or not started_at
            or type(restart_count) is not int or restart_count < 0):
        raise CheckInputError("container_fact_invalid")
    networks = raw["networks"]
    if networks is None:
        networks = {}
    if not isinstance(networks, dict):
        raise CheckInputError("container_networks_invalid")
    network_ids = set()
    for attachment in networks.values():
        if not isinstance(attachment, dict) or not isinstance(attachment.get("NetworkID"), str):
            raise CheckInputError("container_networks_invalid")
        network_ids.add(attachment["NetworkID"])
    exposed, bindings = _ports(raw["ports"])
    return ContainerFact(container_id, _label(raw["project"]), _label(raw["service"]),
                         _str(raw["image"], "container_fact_invalid"),
                         raw["running"], started_at, restart_count, exposed, bindings, frozenset(network_ids))


def _address(value: str) -> str:
    """'172.18.0.2/16' 같은 docker 표기에서 주소만."""
    if not value:
        return ""
    try:
        return str(ipaddress.ip_interface(value).ip)
    except ValueError:
        raise CheckInputError("network_member_address_invalid") from None


def parse_network(raw: object) -> NetworkFact:
    if not isinstance(raw, dict):
        raise CheckInputError("network_fact_invalid")
    # docker는 빈 값을 null로도 준다. null만 빈 dict로 보고, 다른 타입은 거부한다.
    labels, containers, ipam = ({} if raw.get(name) is None else raw.get(name)
                                for name in ("Labels", "Containers", "IPAM"))
    if (not isinstance(labels, dict) or not isinstance(containers, dict) or not isinstance(ipam, dict)
            or type(raw.get("Internal")) is not bool):
        raise CheckInputError("network_fact_invalid")
    members, names = {}, {}
    for container_id, endpoint in containers.items():
        if not _CONTAINER_ID.fullmatch(container_id) or not isinstance(endpoint, dict):
            raise CheckInputError("network_fact_invalid")
        addresses = tuple(sorted(filter(None, (_address(_str(endpoint.get(name, ""), "network_fact_invalid"))
                                                for name in ("IPv4Address", "IPv6Address")))))
        members[container_id] = addresses
        names[container_id] = _str(endpoint.get("Name", ""), "network_fact_invalid").lstrip("/")
    gateways = []
    config = [] if ipam.get("Config") is None else ipam.get("Config")
    if not isinstance(config, list):
        raise CheckInputError("network_fact_invalid")
    for entry in config:
        if not isinstance(entry, dict):
            raise CheckInputError("network_fact_invalid")
        gateway = entry.get("Gateway", "")
        if gateway:
            gateways.append(_address(_str(gateway, "network_fact_invalid")))
    return NetworkFact(_str(raw.get("Id"), "network_fact_invalid"), _str(raw.get("Name"), "network_fact_invalid"),
                       _str(labels.get(PROJECT_LABEL, ""), "network_fact_invalid"),
                       _str(labels.get(NETWORK_LABEL, ""), "network_fact_invalid"),
                       raw["Internal"], members, tuple(sorted(gateways)), names)


def _is_loopback(address: str) -> bool:
    try:
        parsed = ipaddress.ip_address(address)
    except ValueError:
        return False  # 빈 값(전체 주소)·이상한 값은 loopback으로 보지 않는다(fail-closed)
    mapped = getattr(parsed, "ipv4_mapped", None)
    return (mapped or parsed).is_loopback


def check_topology(expected: ExpectedTopology, project: str, networks: Iterable[NetworkFact],
                   containers: Iterable[ContainerFact]) -> list[str]:
    """회차 project의 망·컨테이너가 기대 구성과 정확히 같은지. 어긋남 코드를 정렬해 돌려준다."""
    violations: set[str] = set()
    networks, containers = list(networks), list(containers)
    by_service: dict[str, list[ContainerFact]] = {}
    for container in containers:
        if container.project != project:
            violations.add("service: FOREIGN_PROJECT")
            continue
        by_service.setdefault(container.service, []).append(container)
    service_by_id = {c.id: c.service for c in containers if c.project == project}
    for service in sorted(set(by_service) - expected.services):
        violations.add(f"service.{service}: UNEXPECTED")
    for service in sorted(expected.services):
        found = by_service.get(service, [])
        if not found:
            violations.add(f"service.{service}: MISSING")
        elif len(found) > 1:
            violations.add(f"service.{service}: DUPLICATE")
        for container in found:
            if not container.running:
                violations.add(f"service.{service}: NOT_RUNNING")
            if container.bindings != expected.publishes.get(service, frozenset()):
                violations.add(f"service.{service}: PUBLISH_MISMATCH")

    by_key: dict[str, list[NetworkFact]] = {}
    for network in networks:
        if network.project != project:
            violations.add("network: FOREIGN_PROJECT")
            continue
        by_key.setdefault(network.key, []).append(network)
    for key in sorted(set(by_key) - set(expected.networks)):
        violations.add(f"network.{key or '-'}: UNEXPECTED")
    project_network_ids = {n.id for n in networks if n.project == project}
    attached: dict[str, set[str]] = {}
    for key, want in expected.networks.items():
        found = by_key.get(key, [])
        if not found:
            violations.add(f"network.{key}: MISSING")
            continue
        if len(found) > 1:
            violations.add(f"network.{key}: DUPLICATE")
        for network in found:
            if network.internal != want.internal:
                violations.add(f"network.{key}: INTERNAL_MISMATCH")
            services = set()
            for container_id in network.members:
                if container_id not in service_by_id:
                    violations.add(f"network.{key}: FOREIGN_MEMBER")
                    continue
                services.add(service_by_id[container_id])
                attached.setdefault(container_id, set()).add(network.id)
            if services != set(want.members):
                violations.add(f"network.{key}: MEMBERS_MISMATCH")
    for container in containers:
        if container.project != project:
            continue
        if container.network_ids - project_network_ids:
            violations.add(f"service.{container.service}: FOREIGN_NETWORK")
        # 컨테이너 쪽 연결 목록과 망 쪽 구성원 목록이 서로 맞아야 한다(한쪽만 바뀐 상태 배제).
        if container.network_ids & project_network_ids != attached.get(container.id, set()):
            violations.add(f"service.{container.service}: ATTACHMENT_INCONSISTENT")
    return sorted(violations)


def check_other_publishes(project: str, containers: Iterable[ContainerFact]) -> list[str]:
    """회차 밖 컨테이너가 loopback 밖으로 publish하면 엔진이 엔진 망의 호스트 쪽 게이트웨이 주소로 닿을 수 있다."""
    violations = set()
    for container in containers:
        if container.project == project:
            continue
        if any(not _is_loopback(binding.host_ip) for binding in container.bindings):
            violations.add(f"host.container.{container.id[:12]}: NON_LOOPBACK_PUBLISH")
    return sorted(violations)


def _proc_address(hex_address: str, family: int) -> str:
    if family == 4:
        if len(hex_address) != 8:
            raise CheckInputError("proc_net_invalid")
        packed = int(hex_address, 16).to_bytes(4, sys.byteorder)
    else:
        if len(hex_address) != 32:
            raise CheckInputError("proc_net_invalid")
        # 커널은 32비트 단어 4개를 각각 호스트 바이트 순서로 찍는다.
        packed = b"".join(int(hex_address[i:i + 8], 16).to_bytes(4, sys.byteorder) for i in range(0, 32, 8))
    address = ipaddress.ip_address(packed)
    mapped = getattr(address, "ipv4_mapped", None)
    return str(mapped or address)


def parse_proc_net_tcp(text: str, family: int) -> frozenset[Listener]:
    if family not in (4, 6):
        raise CheckInputError("proc_net_invalid")
    lines = text.splitlines()
    if not lines or not lines[0].split() or lines[0].split()[0] != "sl":
        raise CheckInputError("proc_net_invalid")
    listeners = set()
    for line in lines[1:]:
        fields = line.split()
        if not fields:
            continue
        if len(fields) < 4 or ":" not in fields[1]:
            raise CheckInputError("proc_net_invalid")
        if fields[3] != _TCP_LISTEN:
            continue
        hex_address, hex_port = fields[1].rsplit(":", 1)
        try:
            listeners.add(Listener(_proc_address(hex_address, family), int(hex_port, 16)))
        except ValueError:
            raise CheckInputError("proc_net_invalid") from None
    return frozenset(listeners)


def read_host_listeners(read_text: Callable[[str], str | None]) -> frozenset[Listener]:
    """read_text(path)는 내용 또는 없음(None)을 돌려준다. IPv4 표가 없으면 점검 불가(fail-closed)."""
    v4 = read_text(PROC_TCP)
    if v4 is None:
        raise CheckInputError("proc_net_unavailable")
    v6 = read_text(PROC_TCP6)  # IPv6가 꺼진 커널에는 없다
    listeners = set(parse_proc_net_tcp(v4, 4))
    if v6 is not None:
        listeners |= parse_proc_net_tcp(v6, 6)
    return frozenset(listeners)


def listeners_outside_loopback(listeners: Iterable[Listener]) -> list[str]:
    """격리 근거용: loopback이 아닌 호스트 리슨(주소:포트, 정렬)."""
    return sorted({f"{item.address}:{item.port}" for item in listeners if not _is_loopback(item.address)})


def check_host_listeners(listeners: Iterable[Listener], allowed: frozenset[Listener]) -> list[str]:
    """loopback이 아닌 주소(0.0.0.0·[::]·특정 IP 전부)의 리슨은 허용 집합에 정확히 있어야 한다."""
    violations = set()
    for listener in listeners:
        if _is_loopback(listener.address) or listener in allowed:
            continue
        violations.add(f"host.listen.{listener.address}:{listener.port}: NOT_ALLOWED")
    return sorted(violations)


def network_snapshot(daemon: DaemonIdentity, project: str, networks: Iterable[NetworkFact],
                     containers: Iterable[ContainerFact]) -> dict:
    """pre·post 비교와 격리 근거용 정규화 기록. ID·주소까지 넣어 교체·재생성도 변경으로 잡는다."""
    containers = [c for c in containers if c.project == project]
    service_by_id = {c.id: c.service for c in containers}
    return {
        "daemon": {"version": daemon.version, "os": daemon.os, "arch": daemon.arch, "platform": daemon.platform},
        "project": project,
        "networks": {
            n.key: {"id": n.id, "name": n.name, "internal": n.internal, "gateways": list(n.gateways),
                    "members": {service_by_id.get(cid, "?" + cid[:12]): list(addrs)
                                for cid, addrs in sorted(n.members.items())},
                    "names": {service_by_id.get(cid, "?" + cid[:12]): name
                              for cid, name in sorted(n.member_names.items())}}
            for n in sorted((n for n in networks if n.project == project), key=lambda n: (n.key, n.id))
        },
        "services": {
            c.service: {"id": c.id, "image": c.image, "running": c.running,
                        "started_at": c.started_at, "restart_count": c.restart_count, "exposed": sorted(c.exposed),
                        "bindings": [[b.container_port, b.host_ip, b.host_port] for b in sorted(c.bindings)]}
            for c in sorted(containers, key=lambda c: (c.service, c.id))
        },
    }


def policy_sha256(snapshot: dict) -> str:
    """망 정책(망·internal·구성 서비스·바인딩)의 지문. 회차마다 바뀌는 ID·주소는 넣지 않는다."""
    policy = {
        "networks": {key: {"internal": n["internal"], "members": sorted(n["members"])}
                     for key, n in snapshot["networks"].items()},
        "bindings": {service: s["bindings"] for service, s in snapshot["services"].items()},
    }
    data = json.dumps(policy, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")
    return hashlib.sha256(data).hexdigest()


def compare_snapshots(pre: dict, post: dict) -> list[str]:
    """pre~post 사이 데몬·망·구성원·주소·바인딩·컨테이너 교체·재시작을 잡는다(§2: 바뀌면 증명 발행 안 함)."""
    violations = []
    for field in ("daemon", "project", "networks", "services"):
        if pre.get(field) != post.get(field):
            violations.append(f"snapshot.{field}: CHANGED")
    return violations
