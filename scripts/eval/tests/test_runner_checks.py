"""망·바인딩·호스트 조건 점검. 실제 docker·/proc 없이 합성 사실로만 본다."""

from __future__ import annotations

import copy
import os
import re
import sys
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_checks import (
    Binding, CheckInputError, Listener, allowed_host_listens, check_host_listeners, check_other_publishes,
    check_topology, compare_snapshots, expected_topology, network_snapshot, parse_container, parse_network,
    parse_proc_net_tcp, policy_sha256, read_host_listeners,
)
from runner_config import COMPOSE_COMMON, COMPOSE_EVAL, COMPOSE_LOCAL_LLM, parse_runner_config, plan_run
from runner_docker import DaemonIdentity
from test_preflight import configuration, local_llm
from test_runner_config import _yaml_without_comments, runner_config

PROJECT = "eval-ours-1"
DAEMON = DaemonIdentity("29.1.3", "linux", "amd64", "Docker Engine - Community")
# 실제 ID처럼 앞 12자리(짧은 ID)가 서로 다르게.
SERVICE_IDS = {name: str(n) * 64 for n, name in enumerate(
    ("engine", "engine-db", "target", "eval-meter", "eval-obs", "eval-llm-gw", "eval-api"), 1)}
NET_IDS = {key: f"{n:064x}" for n, key in enumerate(
    ("engine_net", "target_net", "llm_egress_net", "engine_api_net", "obs_net"), 100)}
MEMBERS = {"engine_net": ("engine", "engine-db", "eval-meter", "eval-llm-gw", "eval-api"),
           "target_net": ("target", "eval-meter", "eval-obs"), "llm_egress_net": ("eval-llm-gw",),
           "engine_api_net": ("eval-api",), "obs_net": ("eval-obs",)}
INTERNAL = {"engine_net": True, "target_net": True, "llm_egress_net": False, "engine_api_net": False, "obs_net": False}


def plan_for(backend="api"):
    preflight = configuration()
    if backend == "local":
        for side in ("baseline", "ours"):
            preflight[side]["llm"] = local_llm()
    config = parse_runner_config(runner_config())
    return config, plan_run(config, preflight, "ours", "ours-1")


def raw_world():
    """기대 구성과 정확히 같은 합성 docker inspect 출력(망·컨테이너)."""
    networks = []
    for n, (key, services) in enumerate(MEMBERS.items()):
        networks.append({
            "Id": NET_IDS[key], "Name": f"{PROJECT}_{key}", "Internal": INTERNAL[key],
            "Labels": {"com.docker.compose.project": PROJECT, "com.docker.compose.network": key},
            "IPAM": {"Config": [{"Subnet": f"172.{30 + n}.0.0/16", "Gateway": f"172.{30 + n}.0.1"}]},
            "Containers": {SERVICE_IDS[s]: {"Name": f"{PROJECT}-{s}-1", "IPv4Address": f"172.{30 + n}.0.{i + 2}/16", "IPv6Address": ""}
                           for i, s in enumerate(services)},
        })
    ports = {"engine": {"8787/tcp": None, "8788/tcp": None},
             "eval-api": {"8091/tcp": [{"HostIp": "127.0.0.1", "HostPort": "18787"}]},
             "eval-llm-gw": {},
             "eval-obs": {"8081/tcp": [{"HostIp": "127.0.0.1", "HostPort": "18081"}]},
             "engine-db": {"5432/tcp": None}, "target": {"3000/tcp": None}, "eval-meter": {}}
    containers = []
    for service, container_id in SERVICE_IDS.items():
        attached = {f"{PROJECT}_{key}": {"NetworkID": NET_IDS[key], "IPAddress": "x"}
                    for key, members in MEMBERS.items() if service in members}
        containers.append({"id": container_id, "project": PROJECT, "service": service, "image": "sha256:" + "9" * 64,
                           "running": True, "started_at": "2026-11-02T01:00:00.123456789Z", "restart_count": 0,
                           "ports": ports[service], "networks": attached})
    return networks, containers


def facts(networks, containers):
    return [parse_network(n) for n in networks], [parse_container(c) for c in containers]


def by_service(containers, service):
    return next(c for c in containers if c["service"] == service)


def by_key(networks, key):
    return next(n for n in networks if n["Labels"]["com.docker.compose.network"] == key)


def compose_layout(paths):
    """최소 YAML 해석: 서비스별 망 목록과 망별 internal. deploy/ 파일 모양에만 맞춘다."""
    service_nets: dict[str, set[str]] = {}
    internal: dict[str, bool] = {}
    for path in paths:
        section = service = net = None
        in_networks = False
        for line in _yaml_without_comments(Path(path).read_text(encoding="utf-8")).splitlines():
            if not line.strip():
                continue
            if not line.startswith(" "):
                section, service, net, in_networks = line.split(":")[0], None, None, False
                continue
            top = re.fullmatch(r"  ([A-Za-z0-9_.-]+):\s*(.*)", line)
            if top:
                if section == "services":
                    service, in_networks = top.group(1), False
                    service_nets.setdefault(service, set())
                elif section == "networks":
                    net = top.group(1)
                    internal.setdefault(net, False)
                continue
            if section == "services" and re.fullmatch(r"    networks:\s*", line):
                in_networks = True
                continue
            if section == "services" and re.fullmatch(r"    \S.*", line):
                in_networks = False
                continue
            item = re.fullmatch(r"      - ([A-Za-z0-9_.-]+)\s*", line)
            if in_networks and item:
                service_nets[service].add(item.group(1))
            if section == "networks" and re.fullmatch(r"    internal:\s*true\s*", line):
                internal[net] = True
    return service_nets, internal


class TestExpectations(unittest.TestCase):
    def test_topology_matches_compose_files_for_each_backend(self):
        for backend in ("api", "local"):
            config, plan = plan_for(backend)
            expected = expected_topology(config, plan)
            service_nets, internal = compose_layout(plan.compose_files)
            members = {}
            for service, nets in service_nets.items():
                for net in nets:
                    members.setdefault(net, set()).add(service)
            with self.subTest(backend=backend):
                self.assertEqual({k: (v.internal, set(v.members)) for k, v in expected.networks.items()},
                                 {k: (internal[k], members.get(k, set())) for k in internal})
                self.assertEqual(expected.services, frozenset(service_nets))

    def test_publishing_services_match_compose_files(self):
        # 기대 구성에서 publish하는 서비스 = compose에서 ports를 가진 서비스(엔진은 직접 publish하지 않는다).
        for backend in ("api", "local"):
            config, plan = plan_for(backend)
            expected = expected_topology(config, plan)
            with_ports = set()
            for path in plan.compose_files:
                section = service = None
                for line in _yaml_without_comments(Path(path).read_text(encoding="utf-8")).splitlines():
                    if line and not line.startswith(" "):
                        section = line.split(":")[0]
                        continue
                    top = re.fullmatch(r"  ([A-Za-z0-9_.-]+):.*", line)
                    if section == "services" and top:
                        service = top.group(1)
                    elif section == "services" and re.fullmatch(r"    ports:\s*", line):
                        with_ports.add(service)
            with self.subTest(backend=backend):
                self.assertEqual(with_ports, {s for s, bindings in expected.publishes.items() if bindings})

    def test_publishes_come_from_config_ports_on_loopback(self):
        config, plan = plan_for()
        expected = expected_topology(config, plan)
        # 엔진은 internal 망에만 있어 publish하지 않고, 엔진 API는 API front가 루프백에 publish한다.
        self.assertEqual(expected.publishes["eval-api"], {Binding("8091/tcp", "127.0.0.1", 18787)})
        self.assertEqual(expected.publishes["eval-obs"], {Binding("8081/tcp", "127.0.0.1", 18081)})
        for service in ("engine", "engine-db", "target", "eval-meter", "eval-llm-gw"):
            self.assertEqual(expected.publishes[service], frozenset())

    def test_engine_is_on_the_internal_network_only(self):
        config, plan = plan_for()
        expected = expected_topology(config, plan)
        on = {key for key, network in expected.networks.items() if "engine" in network.members}
        self.assertEqual(on, {"engine_net"})
        self.assertTrue(expected.networks["engine_net"].internal)
        gateway_nets = {key for key, network in expected.networks.items() if "eval-llm-gw" in network.members}
        self.assertEqual(gateway_nets, {"engine_net", "llm_egress_net"})

    def test_host_listen_allowance_by_backend(self):
        config, plan = plan_for("api")
        self.assertEqual(allowed_host_listens(config, plan), frozenset())
        config, plan = plan_for("local")
        self.assertEqual(allowed_host_listens(config, plan), {Listener("172.17.0.1", 11434)})
        payload = runner_config()
        payload["host_listen_allowlist"] = [{"address": "10.255.255.254", "port": 53, "reason": "WSL DNS 터널링"}]
        config = parse_runner_config(payload)
        plan = plan_run(config, configuration(), "ours", "ours-1")
        self.assertEqual(allowed_host_listens(config, plan), {Listener("10.255.255.254", 53)})

    def test_local_port_defaults_by_scheme(self):
        config, plan = plan_for("local")
        plan.llm["base_url"] = "http://llm-host/v1"
        self.assertEqual(allowed_host_listens(config, plan), {Listener("172.17.0.1", 80)})

    def test_unknown_backend_rejected(self):
        config, plan = plan_for()
        plan.llm["backend"] = "gateway"
        with self.assertRaises(CheckInputError):
            expected_topology(config, plan)
        with self.assertRaises(CheckInputError):
            allowed_host_listens(config, plan)


class TestParse(unittest.TestCase):
    def test_world_parses(self):
        networks, containers = facts(*raw_world())
        engine = next(c for c in containers if c.service == "engine")
        self.assertEqual(engine.bindings, frozenset())
        self.assertEqual(engine.network_ids, {NET_IDS["engine_net"]})
        self.assertEqual(engine.exposed, {"8787/tcp", "8788/tcp"})
        self.assertEqual(next(c for c in containers if c.service == "target").exposed, {"3000/tcp"})
        self.assertEqual(next(c for c in containers if c.service == "eval-meter").exposed, frozenset())
        target_net = next(n for n in networks if n.key == "target_net")
        self.assertEqual(target_net.members[SERVICE_IDS["target"]], ("172.31.0.2",))
        self.assertEqual(target_net.gateways, ("172.31.0.1",))
        self.assertEqual(target_net.member_names[SERVICE_IDS["target"]], f"{PROJECT}-target-1")

    def test_container_fact_shape_is_closed(self):
        _, containers = raw_world()
        for mutate in (lambda c: c.update(env=["K=V"]), lambda c: c.update(id="X" * 64),
                       lambda c: c.update(running="true"), lambda c: c.pop("image"),
                       lambda c: c.pop("started_at"), lambda c: c.update(started_at=""),
                       lambda c: c.update(started_at=None), lambda c: c.update(restart_count=-1),
                       lambda c: c.update(restart_count=True), lambda c: c.update(restart_count="0"),
                       lambda c: c.update(ports={"8787/tcp": [{"HostIp": "127.0.0.1", "HostPort": "x"}]}),
                       lambda c: c.update(ports={"8787": None}), lambda c: c.update(ports=[]),
                       lambda c: c.update(networks={"n": {"IPAddress": "1"}})):
            raw = copy.deepcopy(containers[0])
            mutate(raw)
            with self.assertRaises(CheckInputError):
                parse_container(raw)
        raw = copy.deepcopy(containers[0]) | {"networks": None, "ports": None}
        fact = parse_container(raw)
        self.assertEqual((fact.network_ids, fact.bindings), (frozenset(), frozenset()))

    def test_network_fact_shape(self):
        networks, _ = raw_world()
        for mutate in (lambda n: n.update(Internal="false"), lambda n: n.update(Containers={"short": {}}),
                       lambda n: n.update(Labels=[]), lambda n: n.update(Containers=[]), lambda n: n.update(IPAM=""),
                       lambda n: n.update(IPAM={"Config": {}}), lambda n: n.update(Id=None),
                       lambda n: n["Containers"][SERVICE_IDS["engine"]].update(IPv4Address="nope")):
            raw = copy.deepcopy(networks[0])
            mutate(raw)
            with self.assertRaises(CheckInputError):
                parse_network(raw)


class TestTopology(unittest.TestCase):
    def setUp(self):
        self.config, self.plan = plan_for()
        self.expected = expected_topology(self.config, self.plan)
        self.networks, self.containers = raw_world()

    def check(self):
        return check_topology(self.expected, PROJECT, *facts(self.networks, self.containers))

    def test_exact_world_passes(self):
        self.assertEqual(self.check(), [])

    def test_engine_attached_to_target_net_is_caught(self):
        # 고장 주입: 엔진을 대상 망에 붙인 변형(스모크 고장 주입과 같은 모양)
        by_key(self.networks, "target_net")["Containers"][SERVICE_IDS["engine"]] = {"IPv4Address": "172.31.0.9/16"}
        by_service(self.containers, "engine")["networks"]["t"] = {"NetworkID": NET_IDS["target_net"]}
        self.assertEqual(self.check(), ["network.target_net: MEMBERS_MISMATCH"])

    def test_one_sided_attachment_is_caught(self):
        by_service(self.containers, "engine")["networks"]["t"] = {"NetworkID": NET_IDS["target_net"]}
        self.assertEqual(self.check(), ["service.engine: ATTACHMENT_INCONSISTENT"])
        self.networks, self.containers = raw_world()
        by_key(self.networks, "obs_net")["Containers"][SERVICE_IDS["eval-meter"]] = {"IPv4Address": "172.33.0.9/16"}
        self.assertEqual(self.check(), ["network.obs_net: MEMBERS_MISMATCH", "service.eval-meter: ATTACHMENT_INCONSISTENT"])

    def test_foreign_member_and_foreign_network(self):
        by_key(self.networks, "engine_net")["Containers"]["f" * 64] = {"IPv4Address": "172.30.0.99/16"}
        self.assertEqual(self.check(), ["network.engine_net: FOREIGN_MEMBER"])
        self.networks, self.containers = raw_world()
        by_service(self.containers, "engine")["networks"]["bridge"] = {"NetworkID": "e" * 64}
        self.assertEqual(self.check(), ["service.engine: FOREIGN_NETWORK"])

    def test_internal_flag_and_network_set(self):
        by_key(self.networks, "target_net")["Internal"] = False
        self.assertEqual(self.check(), ["network.target_net: INTERNAL_MISMATCH"])
        self.networks, self.containers = raw_world()
        extra = copy.deepcopy(by_key(self.networks, "obs_net"))
        extra.update(Id="d" * 64, Containers={})
        extra["Labels"]["com.docker.compose.network"] = "default"
        self.networks.append(extra)
        self.assertEqual(self.check(), ["network.default: UNEXPECTED"])
        dup = copy.deepcopy(by_key(self.networks, "llm_egress_net"))
        dup.update(Id="c" * 64, Containers={})
        self.networks = [n for n in self.networks if n is not extra] + [dup]
        self.assertIn("network.llm_egress_net: DUPLICATE", self.check())
        self.networks = [n for n in raw_world()[0] if n["Labels"]["com.docker.compose.network"] != "obs_net"]
        self.assertIn("network.obs_net: MISSING", self.check())

    def test_foreign_project_records_rejected(self):
        self.networks[0]["Labels"]["com.docker.compose.project"] = "other"
        self.assertIn("network: FOREIGN_PROJECT", self.check())
        self.networks, self.containers = raw_world()
        self.containers[0]["project"] = "other"
        self.assertIn("service: FOREIGN_PROJECT", self.check())

    def test_services_exact(self):
        self.containers = [c for c in self.containers if c["service"] != "eval-obs"]
        self.assertIn("service.eval-obs: MISSING", self.check())
        self.networks, self.containers = raw_world()
        self.containers.append(copy.deepcopy(by_service(self.containers, "target")) | {"id": "b" * 64})
        self.assertIn("service.target: DUPLICATE", self.check())
        self.networks, self.containers = raw_world()
        self.containers.append(copy.deepcopy(by_service(self.containers, "target")) | {"id": "b" * 64, "service": "debug",
                                                                                         "networks": {}})
        self.assertEqual(self.check(), ["service.debug: UNEXPECTED"])
        self.networks, self.containers = raw_world()
        by_service(self.containers, "eval-meter")["running"] = False
        self.assertEqual(self.check(), ["service.eval-meter: NOT_RUNNING"])

    def test_publishes_exact(self):
        cases = [
            ("engine", {"8787/tcp": [{"HostIp": "127.0.0.1", "HostPort": "18787"}]}),
            ("eval-api", {"8091/tcp": [{"HostIp": "0.0.0.0", "HostPort": "18787"}]}),
            ("eval-api", {"8091/tcp": [{"HostIp": "127.0.0.1", "HostPort": "18787"}, {"HostIp": "::", "HostPort": "18787"}]}),
            ("eval-api", {"8091/tcp": [{"HostIp": "127.0.0.1", "HostPort": "18788"}]}),
            ("eval-llm-gw", {"8090/tcp": [{"HostIp": "127.0.0.1", "HostPort": "8090"}]}),
            ("target", {"3000/tcp": [{"HostIp": "127.0.0.1", "HostPort": "3000"}]}),
            ("eval-obs", {}),
        ]
        for service, ports in cases:
            self.networks, self.containers = raw_world()
            by_service(self.containers, service)["ports"] = ports
            with self.subTest(service=service, ports=ports):
                self.assertEqual(self.check(), [f"service.{service}: PUBLISH_MISMATCH"])


def foreign(ports, project="other"):
    return parse_container({"id": "a" * 64, "project": project, "service": "web", "image": "x", "running": True,
                            "started_at": "2026-11-02T00:00:00Z", "restart_count": 0, "ports": ports, "networks": {}})


class TestHostConditions(unittest.TestCase):
    def test_other_containers_must_publish_on_loopback_only(self):
        for host_ip in ("127.0.0.1", "::1", "127.0.0.53", "::ffff:127.0.0.1"):
            self.assertEqual(check_other_publishes(PROJECT, [foreign({"80/tcp": [{"HostIp": host_ip, "HostPort": "8080"}]})]), [])
        for host_ip in ("0.0.0.0", "::", "", "172.20.118.46", "not-an-ip"):
            got = check_other_publishes(PROJECT, [foreign({"80/tcp": [{"HostIp": host_ip, "HostPort": "8080"}]})])
            self.assertEqual(got, ["host.container.aaaaaaaaaaaa: NON_LOOPBACK_PUBLISH"])
        own = foreign({"80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}]}, project=PROJECT)
        self.assertEqual(check_other_publishes(PROJECT, [own]), [])

    def test_listeners_outside_loopback_need_exact_allowance(self):
        listeners = {Listener("127.0.0.1", 11434), Listener("127.0.0.53", 53), Listener("::1", 631),
                     Listener("0.0.0.0", 8080), Listener("::", 3000), Listener("10.255.255.254", 53),
                     Listener("172.17.0.1", 11434)}
        self.assertEqual(check_host_listeners(listeners, frozenset()), [
            "host.listen.0.0.0.0:8080: NOT_ALLOWED", "host.listen.10.255.255.254:53: NOT_ALLOWED",
            "host.listen.172.17.0.1:11434: NOT_ALLOWED", "host.listen.:::3000: NOT_ALLOWED"])
        allowed = frozenset({Listener("10.255.255.254", 53), Listener("172.17.0.1", 11434)})
        self.assertEqual(check_host_listeners(listeners, allowed),
                         ["host.listen.0.0.0.0:8080: NOT_ALLOWED", "host.listen.:::3000: NOT_ALLOWED"])
        self.assertEqual(check_host_listeners({Listener("172.17.0.1", 11435)}, allowed),
                         ["host.listen.172.17.0.1:11435: NOT_ALLOWED"])
        # 같은 포트라도 전체 주소 리슨은 특정 주소 허용으로 통과하지 않는다.
        self.assertEqual(check_host_listeners({Listener("0.0.0.0", 11434)}, allowed),
                         ["host.listen.0.0.0.0:11434: NOT_ALLOWED"])


TCP_HEADER = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode"


@unittest.skipUnless(sys.byteorder == "little", "합성 /proc 표기는 little-endian 기준")
class TestProcNet(unittest.TestCase):
    def test_ipv4_listen_rows(self):
        text = "\n".join([TCP_HEADER,
                          "   0: 0100007F:2CAA 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1",
                          "   1: FEFFFF0A:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2",
                          "   2: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3",
                          "   3: 2E7614AC:D2F0 0100007F:2CAA 01 00000000:00000000 00:00000000 00000000  1000        0 4"])
        self.assertEqual(parse_proc_net_tcp(text, 4), {Listener("127.0.0.1", 11434), Listener("10.255.255.254", 53),
                                                       Listener("0.0.0.0", 8080)})

    def test_ipv6_listen_rows_and_mapped(self):
        text = "\n".join([TCP_HEADER,
                          "   0: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 0 0 0 0 0 0 1",
                          "   1: 00000000000000000000000001000000:0277 00000000000000000000000000000000:0000 0A 0 0 0 0 0 0 2",
                          "   2: 0000000000000000FFFF00000100007F:1F90 00000000000000000000000000000000:0000 0A 0 0 0 0 0 0 3"])
        self.assertEqual(parse_proc_net_tcp(text, 6), {Listener("::", 3000), Listener("::1", 631),
                                                       Listener("127.0.0.1", 8080)})

    def test_malformed_tables_rejected(self):
        for text, family in (("", 4), ("garbage", 4), (TCP_HEADER + "\n 0: 0100007F 0 0A", 4),
                             (TCP_HEADER + "\n 0: 01007F:0050 00000000:0000 0A", 4),
                             (TCP_HEADER + "\n 0: ZZ00007F:0050 00000000:0000 0A", 4),
                             (TCP_HEADER + "\n 0: 0100007F:0050 00000000:0000 0A", 6),
                             (TCP_HEADER, 5)):
            with self.assertRaises(CheckInputError):
                parse_proc_net_tcp(text, family)

    def test_reader_requires_ipv4_table(self):
        row = "   0: 00000000:1F90 00000000:0000 0A 0 0 0 0 0 0 1"
        tables = {"/proc/net/tcp": TCP_HEADER + "\n" + row}
        self.assertEqual(read_host_listeners(tables.get), {Listener("0.0.0.0", 8080)})
        with self.assertRaises(CheckInputError) as caught:
            read_host_listeners({}.get)
        self.assertEqual(str(caught.exception), "proc_net_unavailable")


class TestSnapshots(unittest.TestCase):
    def snapshot(self, networks=None, containers=None, daemon=DAEMON):
        world = raw_world()
        return network_snapshot(daemon, PROJECT, *facts(networks or world[0], containers or world[1]))

    def test_identical_snapshots_compare_clean_and_policy_ignores_ids(self):
        first, second = self.snapshot(), self.snapshot()
        self.assertEqual(compare_snapshots(first, second), [])
        networks, containers = raw_world()
        for network in networks:
            network["Id"] = network["Id"].replace("0", "d")
        for container in containers:
            container["networks"] = {k: {"NetworkID": v["NetworkID"].replace("0", "d")} for k, v in container["networks"].items()}
        moved = self.snapshot(networks, containers)
        self.assertEqual(policy_sha256(moved), policy_sha256(first))
        self.assertEqual(compare_snapshots(first, moved), ["snapshot.networks: CHANGED"])
        self.assertRegex(policy_sha256(first), r"^[0-9a-f]{64}$")

    def test_policy_changes_with_bindings_members_or_internal(self):
        base = policy_sha256(self.snapshot())
        networks, containers = raw_world()
        by_service(containers, "eval-api")["ports"] = {"8091/tcp": [{"HostIp": "0.0.0.0", "HostPort": "18787"}]}
        self.assertNotEqual(policy_sha256(self.snapshot(networks, containers)), base)
        networks, containers = raw_world()
        by_key(networks, "llm_egress_net")["Internal"] = True
        self.assertNotEqual(policy_sha256(self.snapshot(networks, containers)), base)
        networks, containers = raw_world()
        del by_key(networks, "obs_net")["Containers"][SERVICE_IDS["eval-obs"]]
        self.assertNotEqual(policy_sha256(self.snapshot(networks, containers)), base)

    def test_changes_between_pre_and_post_detected(self):
        pre = self.snapshot()
        self.assertEqual(compare_snapshots(pre, self.snapshot(daemon=DaemonIdentity("29.1.4", "linux", "amd64", "x"))),
                         ["snapshot.daemon: CHANGED"])
        networks, containers = raw_world()
        by_service(containers, "target")["id"] = "b" * 64  # 대상 컨테이너가 재생성됨
        for network in networks:
            if SERVICE_IDS["target"] in network["Containers"]:
                network["Containers"]["b" * 64] = network["Containers"].pop(SERVICE_IDS["target"])
        # 망 구성원은 서비스 이름 기준이라 그대로고, 교체는 services의 컨테이너 ID로 잡힌다.
        self.assertEqual(compare_snapshots(pre, self.snapshot(networks, containers)), ["snapshot.services: CHANGED"])
        networks, containers = raw_world()
        by_key(networks, "target_net")["Containers"][SERVICE_IDS["target"]]["IPv4Address"] = "172.31.0.77/16"
        self.assertEqual(compare_snapshots(pre, self.snapshot(networks, containers)), ["snapshot.networks: CHANGED"])
        networks, containers = raw_world()
        by_key(networks, "engine_net")["IPAM"]["Config"][0]["Gateway"] = "172.30.0.254"
        self.assertEqual(compare_snapshots(pre, self.snapshot(networks, containers)), ["snapshot.networks: CHANGED"])
        # 재시작: ID·이미지는 같고 시작 시각·재시작 횟수만 바뀐다. 계측기·대상·엔진 각각.
        for service in ("eval-meter", "target", "engine"):
            networks, containers = raw_world()
            by_service(containers, service)["started_at"] = "2026-11-02T01:05:00.000000001Z"
            with self.subTest(service=service, field="started_at"):
                self.assertEqual(compare_snapshots(pre, self.snapshot(networks, containers)), ["snapshot.services: CHANGED"])
            networks, containers = raw_world()
            by_service(containers, service)["restart_count"] = 1
            with self.subTest(service=service, field="restart_count"):
                self.assertEqual(compare_snapshots(pre, self.snapshot(networks, containers)), ["snapshot.services: CHANGED"])
        networks, containers = raw_world()
        by_service(containers, "engine")["image"] = "sha256:" + "8" * 64
        self.assertEqual(compare_snapshots(pre, self.snapshot(networks, containers)), ["snapshot.services: CHANGED"])

    def test_snapshot_ignores_other_projects(self):
        networks, containers = raw_world()
        containers.append({"id": "a" * 64, "project": "other", "service": "web", "image": "x", "running": True,
                           "started_at": "2026-11-02T00:00:00Z", "restart_count": 0, "ports": None, "networks": {}})
        self.assertEqual(compare_snapshots(self.snapshot(), self.snapshot(networks, containers)), [])


if __name__ == "__main__":
    unittest.main()
