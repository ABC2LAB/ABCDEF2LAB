"""탐색 분리 배치 O 회차 실제 포트(DockerSplitPorts, 4-7c-3) 시험. 가짜 Docker·가짜 구동기로 연결 값을 본다."""

from __future__ import annotations

import hashlib
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_config import Image, RunDirectory  # noqa: E402
from runner_product import ModelKey  # noqa: E402
from runner_split import read_workflow_defaults  # noqa: E402
from runner_split_config import parse_split, plan_split_run  # noqa: E402
from runner_split_inputs import prepare_split_inputs  # noqa: E402
from runner_checks import parse_container, parse_network  # noqa: E402
from runner_split_ports import (  # noqa: E402
    DockerSplitPorts, SplitReadyError, check_split_layout, product_record_names, ready_expectations, ready_state,
)
from test_runner_split_config import acceptance_config, split_block  # noqa: E402
from test_runner_split_inputs import FakeOpenssl, counter_bytes  # noqa: E402

TARGET = Image("example/target@sha256:" + "6" * 64, "sha256:" + "6" * 64)
MANIFEST = {"version": 3, "model": "qwen/qwen3.6-35b-a3b", "approval_id": "approval-test-1", "base_url": "https://openrouter.ai/api/v1",
            "max_requests": 400}


class FakeDocker:
    def __init__(self):
        self.calls = []
        self.copies: dict[str, dict] = {}

    def copy_volume_files(self, image, volume, directory, names, role, uid, gid, cidfile, max_bytes, timeout):
        self.calls.append(("copy", image, volume, directory, tuple(names), role, uid, gid, cidfile.parent, max_bytes))
        return dict(self.copies[role]), "c" * 64

    def signal(self, container_id, name):
        self.calls.append(("signal", container_id, name))

    def stop_container(self, container_id, seconds):
        self.calls.append(("stop", container_id, seconds))

    def compose_up_detached(self, args):
        self.calls.append(("up", tuple(args)))

    def container_states(self, ids):
        return [{"service": "a", "status": "running", "exit_code": 0, "health": None}]

    def compose_down(self, args, timeout):
        self.calls.append(("down", tuple(args), timeout))

    def project_network_ids(self, project):
        return []

    def project_container_ids(self, project):
        return ["x"] if project == "busy" else []

    def image_id(self, ref):
        return "sha256:" + hashlib.sha256(ref.encode()).hexdigest()


class FakeDriver:
    def __init__(self):
        self.calls = []

    def __getattr__(self, name):
        def record(*args, **kwargs):
            self.calls.append((name, args, kwargs))
            if name == "catalog_profile":
                return {"workflow": args[1], "workflow_request_limit": 400}
            if name == "register":
                return {"id": "target-" + "a" * 32, "challenge_name": "n", "challenge_value": "v"}
            if name == "approve":
                return {"id": "target-" + "a" * 32, "state": "approved"}
            if name == "create_draft":
                return {"id": "draft-1", "revision": 1, "scope": {"target_ref": "target-" + "a" * 32}}
            if name == "wait_task":
                return {"id": "draft-1", "task_id": "42", "state": "running"}
            return {"name": name}
        return record


class PortsCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        root = Path(self._tmp.name)
        self.config = acceptance_config()
        self.split = parse_split(split_block(), self.config)
        self.plan = plan_split_run(self.config, self.split, run_id="r1", target_image=TARGET, meter_port=8080)
        (root / "run").mkdir(mode=0o700)
        defaults, _ = read_workflow_defaults()
        self.inputs = prepare_split_inputs(root / "run", manifest=MANIFEST, meter_port=8080, explore_port=8082,
                                           evidence_max_bytes=4 << 20, workflow_defaults=defaults, wide_writes=True,
                                           valid_for_hours=4, max_runs=2, openssl=FakeOpenssl(), random_bytes=counter_bytes())
        self.directory = RunDirectory(run=root / "run", meter=root / "run" / "meter", llm=root / "run" / "llm", e2=root / "e2",
                                      state=root / "s", e2_config=root / "a", e2_control=root / "b", e2_copy=root / "c",
                                      e2_engine=root / "d", e2_state=root / "e")
        self.docker, self.driver = FakeDocker(), FakeDriver()
        self.ports = DockerSplitPorts(config=self.config, split=self.split, plan=self.plan, directory=self.directory,
                                      docker=self.docker, driver=self.driver, inputs=self.inputs, rendered={"services": {"a": {}}},
                                      manifest=MANIFEST, model_key=ModelKey("sk-or-v1-test"),
                                      exploration={"mcp_url": "http://explore-mcp:8931/mcp", "direct_proxy": "http://explore-direct:3129"})

    def tearDown(self):
        self._tmp.cleanup()


class TestRecords(PortsCase):
    def test_record_names_follow_the_product_rule(self):
        approval = hashlib.sha256(b"approval-test-1").hexdigest()
        self.assertEqual(product_record_names("approval-test-1"),
                         (f".openrouter-{approval}.ledger", f".openrouter-{approval}.calls"))

    def test_dns_token_comes_from_the_fixture_volume_root(self):
        self.docker.copies["dns-token"] = {"control-token": b"tok\n"}
        self.assertEqual(self.ports.dns_token(), "tok")
        _, image, volume, directory, names, role, uid, gid, cid_parent, _ = self.docker.calls[0]
        self.assertEqual((image, volume, directory, names, role, uid, gid, cid_parent),
                         (self.split.images["fixture"].id, "eval-r1_target-tls", ".", ("control-token",), "dns-token", 1000, 1000,
                          self.directory.run))

    def test_boundary_evidence_is_copied_as_the_boundary_uid_into_a_private_dir(self):
        self.docker.copies["boundary-evidence"] = {"boundary-records.jsonl": b'{"seq":1}\n', "boundary-sealed.json": b"{}"}
        self.ports.copy_boundary_evidence()
        _, image, volume, directory, names, role, uid, _, _, max_bytes = self.docker.calls[0]
        self.assertEqual((image, volume, directory, names, role, uid, max_bytes),
                         (self.split.images["product"].id, "eval-r1_explore-evidence", "run",
                          ("boundary-records.jsonl", "boundary-sealed.json"), "boundary-evidence", 1001, self.split.evidence_max_bytes))
        target = self.directory.run / "boundary"
        self.assertEqual(stat.S_IMODE(os.stat(target).st_mode), 0o700)
        self.assertEqual((target / "boundary-records.jsonl").read_bytes(), b'{"seq":1}\n')
        self.assertEqual(stat.S_IMODE(os.stat(target / "boundary-sealed.json").st_mode), 0o600)

    def test_product_records_from_the_state_volume(self):
        ledger, calls = product_record_names("approval-test-1")
        self.docker.copies["product-records"] = {ledger: b"r1 1 1 1\n", calls: b"{}\n"}
        self.assertEqual(self.ports.copy_product_records(), (b"r1 1 1 1\n", b"{}\n"))
        _, image, volume, directory, names, role, uid, _, _, _ = self.docker.calls[0]
        self.assertEqual((image, volume, directory, names, role, uid),
                         (self.split.images["product"].id, "eval-r1_product-state", "config", (ledger, calls), "product-records", 1000))


class TestDrive(PortsCase):
    def test_prepare_registers_with_the_catalog_limit(self):
        prepared = self.ports.prepare_round("tok")
        names = [c[0] for c in self.driver.calls]
        self.assertEqual(names, ["login", "set_model_key", "catalog_profile", "register", "publish_challenge", "approve", "create_draft"])
        registration = self.driver.calls[3][1][0]
        self.assertEqual((registration["requests"], registration["url"], registration["valid_hours"], registration.get("reversible_writes")),
                         (400, "http://app.eval.test:8080/", self.split.registration_valid_hours, True))
        self.assertEqual(self.driver.calls[0][1][0], self.inputs.admin)
        self.assertEqual(self.driver.calls[1][1][:2], (self.split.profile_id, MANIFEST["model"]))
        self.assertEqual(self.driver.calls[4][1][0], "tok")
        self.assertEqual(self.driver.calls[2][1][1], self.inputs.install_public["target_policy"]["workflow"])
        self.assertEqual((prepared["draft"]["id"], prepared["registration"]), ("draft-1", registration))

    def test_start_waits_for_the_task_with_configured_wait(self):
        running = self.ports.start_round({"draft": {"id": "draft-1", "revision": 1}})
        self.assertEqual(running, {"draft_id": "draft-1", "task_id": "42", "state": "running"})
        self.assertEqual(self.driver.calls[1][1], ("draft-1", self.split.task_wait_seconds, self.split.poll_interval_seconds))
        self.ports.wait_closed("draft-1", 30.0)
        self.assertEqual(self.driver.calls[-1][1], ("draft-1", 30.0, self.split.poll_interval_seconds))


class TestReady(unittest.TestCase):
    """compose `--wait`는 제품 경계의 healthcheck `[NONE]`에서 실패해(실제 compose 2.40.3, 2026-11-17 스모크) 러너가 직접 기다린다."""

    RENDERED = {"services": {
        "install": {"restart": "no"}, "explore-init": {"restart": "no"},
        "preparation": {"depends_on": {"install": {"condition": "service_completed_successfully"}},
                        "healthcheck": {"test": ["CMD", "test", "-S", "/p/ipc/preparation.sock"]}},
        "explore-boundary": {"depends_on": {"explore-init": {"condition": "service_completed_successfully"}},
                             "healthcheck": {"test": ["NONE"]}},
        "explore-mcp": {"restart": "no"},
        "juice-shop": {"profiles": ["eval-split-unused"]}}}

    def test_expectations_from_render(self):
        self.assertEqual(ready_expectations(self.RENDERED), {"install": "completed", "explore-init": "completed",
                                                             "preparation": "healthy", "explore-boundary": "running",
                                                             "explore-mcp": "running"})

    def states(self, **overrides):
        base = {"install": ("exited", 0, None), "explore-init": ("exited", 0, None), "preparation": ("running", 0, "healthy"),
                "explore-boundary": ("running", 0, None), "explore-mcp": ("running", 0, None)}
        base.update(overrides)
        return [{"service": k, "status": v[0], "exit_code": v[1], "health": v[2]} for k, v in base.items() if v is not None]

    def test_ready_and_not_yet(self):
        expected = ready_expectations(self.RENDERED)
        self.assertTrue(ready_state(expected, self.states()))
        for change in ({"install": ("running", 0, None)}, {"preparation": ("running", 0, "starting")},
                       {"explore-mcp": ("created", 0, None)}, {"explore-boundary": None}):
            with self.subTest(change=change):
                self.assertFalse(ready_state(expected, self.states(**change)))

    def test_irrecoverable_states_stop_with_codes(self):
        expected = ready_expectations(self.RENDERED)
        cases = {"split_init_failed:install": {"install": ("exited", 1, None)},
                 "split_service_exited:explore-mcp": {"explore-mcp": ("exited", 0, None)},
                 "split_service_unhealthy:preparation": {"preparation": ("running", 0, "unhealthy")}}
        for code, change in cases.items():
            with self.subTest(code=code), self.assertRaises(SplitReadyError) as caught:
                ready_state(expected, self.states(**change))
            self.assertEqual(str(caught.exception), code)
        for extra in ([{"service": "intruder", "status": "running", "exit_code": 0, "health": None}],
                      self.states()[:1]):
            with self.assertRaises(SplitReadyError) as caught:
                ready_state(expected, self.states() + extra)
            self.assertEqual(str(caught.exception), "split_unexpected_container")


PROJECT = "eval-r1"
# 분리 배치 렌더를 줄인 모양: 일회성(install), healthcheck(preparation), 선언된 netns 공유(api → preparation),
# loopback publish(api), 고정 주소(eval-meter), 쓰지 않는 서비스(juice-shop, eval-split-unused 프로필).
LAYOUT = {
    "networks": {"control": {"internal": True}, "frontend": {}, "target": {"internal": True}, "fixture-control": {"internal": True},
                 "unused_net": {}},
    "services": {
        "install": {"image": "product", "restart": "no", "networks": {"fixture-control": None}},
        "preparation": {"image": "product", "depends_on": {"install": {"condition": "service_completed_successfully"}},
                        "healthcheck": {"test": ["CMD", "true"]}, "networks": {"control": None, "frontend": None}},
        "api": {"image": "product", "network_mode": "service:preparation",
                "ports": [{"target": 8787, "published": "28787", "host_ip": "127.0.0.1", "protocol": "tcp"}]},
        "eval-meter": {"image": "meter", "networks": {"target": {"ipv4_address": "10.252.3.20"}, "control": None}},
        "target": {"image": "target", "networks": {"target": None}},
        "juice-shop": {"image": "juice", "profiles": ["eval-split-unused"], "networks": {"unused_net": None}},
    },
}
IMAGES = {"product": "sha256:" + "1" * 64, "meter": "sha256:" + "2" * 64, "target": "sha256:" + "3" * 64}


def layout_world(rendered=LAYOUT, project=PROJECT):
    """렌더 결과와 정확히 맞는 합성 docker inspect 출력(망·회차 컨테이너·호스트 실행 중 컨테이너)과 기대 이미지."""
    live = ready_expectations(rendered)
    ids = {name: f"{n + 1:064x}" for n, name in enumerate(sorted(live))}
    images = {name: IMAGES.get(rendered["services"][name]["image"], "sha256:" + "9" * 64) for name in live}
    one_shot = {name for name, want in live.items() if want == "completed"}
    networks, net_ids = [], {}
    for n, key in enumerate(sorted(rendered["networks"])):
        users = [name for name in live if key in (rendered["services"][name].get("networks") or {})]
        if not users:
            continue
        net_ids[key] = f"{1000 + n:064x}"
        members = {}
        for i, name in enumerate(sorted(set(users) - one_shot)):
            attached = rendered["services"][name]["networks"][key]
            address = (attached or {}).get("ipv4_address") or f"10.252.{n}.{i + 2}"
            members[ids[name]] = {"Name": f"{project}-{name}-1", "IPv4Address": f"{address}/24", "IPv6Address": ""}
        networks.append({"Id": net_ids[key], "Name": f"{project}_{key}", "Internal": bool(rendered["networks"][key].get("internal")),
                         "Labels": {"com.docker.compose.project": project, "com.docker.compose.network": key},
                         "IPAM": {"Config": [{"Subnet": f"10.252.{n}.0/24", "Gateway": f"10.252.{n}.1"}]}, "Containers": members})
    containers = []
    for name in sorted(live):
        service = rendered["services"][name]
        mode = service.get("network_mode", "")
        attached = {} if mode or name in one_shot else {
            f"{project}_{key}": {"NetworkID": net_ids[key], "IPAddress": "x"} for key in service.get("networks") or {}}
        ports = {f"{p['target']}/tcp": [{"HostIp": p.get("host_ip", ""), "HostPort": str(p["published"])}]
                 for p in service.get("ports") or []}
        containers.append({"id": ids[name], "project": project, "service": name, "image": images[name],
                           "running": name not in one_shot, "started_at": "2026-11-17T01:00:00.123456789Z", "restart_count": 0,
                           "ports": ports, "networks": attached,
                           "network_mode": "container:" + ids[mode[len("service:"):]] if mode.startswith("service:") else f"{project}_x",
                           "pid_mode": ""})
    return networks, containers, images


def judge(networks, containers, running, images, rendered=LAYOUT):
    nets = [parse_network(n) for n in networks]
    mine = [parse_container(c) for c in containers]
    return check_split_layout(rendered, PROJECT, nets, mine, [parse_container(c) for c in running], images)


def stranger(**fields):
    raw = {"id": "f" * 64, "project": "", "service": "", "image": "sha256:" + "8" * 64, "running": True,
           "started_at": "2026-11-17T01:00:00Z", "restart_count": 0, "ports": {}, "networks": {}, "network_mode": "bridge", "pid_mode": ""}
    raw.update(fields)
    return raw


class TestLayout(unittest.TestCase):
    """#668 리뷰: pre/post에서 배치·이미지·실행 중 컨테이너 전체를 대조한다(구간 이전 상태는 이벤트 감시로 안 잡힌다)."""

    def world(self):
        networks, containers, images = layout_world()
        running = [c for c in containers if c["running"]]
        return networks, containers, running, images

    def test_matching_world_passes(self):
        self.assertEqual(judge(*self.world()), [])

    def test_review_counterexamples(self):
        # 1) 대상에 loopback 직접 publish(계측기 우회 경로)
        networks, containers, running, images = self.world()
        target = next(c for c in containers if c["service"] == "target")
        target["ports"] = {"5000/tcp": [{"HostIp": "127.0.0.1", "HostPort": "39001"}]}
        self.assertIn("service.target: PUBLISH_MISMATCH", judge(networks, containers, running, images))
        # 2) 구간 전부터 떠 있는 회차 밖 컨테이너가 대상 netns를 같이 씀
        networks, containers, running, images = self.world()
        target_id = next(c["id"] for c in containers if c["service"] == "target")
        sharer = stranger(network_mode="container:" + target_id)
        self.assertEqual(judge(networks, containers, running + [sharer], images),
                         [f"host.container.{'f' * 12}: SHARES_NETWORK_NAMESPACE"])
        # PID namespace 공유도 같다
        self.assertEqual(judge(networks, containers, running + [stranger(pid_mode="container:" + target_id)], images),
                         [f"host.container.{'f' * 12}: SHARES_PID_NAMESPACE"])

    def test_declared_sharing_only_to_its_own_service(self):
        networks, containers, running, images = self.world()
        api = next(c for c in containers if c["service"] == "api")
        api["network_mode"] = "container:" + next(c["id"] for c in containers if c["service"] == "target")
        result = judge(networks, containers, [c for c in containers if c["running"]], images)
        self.assertIn("service.api: SHARED_NAMESPACE_MISMATCH", result)
        self.assertIn(f"host.container.{api['id'][:12]}: SHARES_NETWORK_NAMESPACE", result)

    def test_images_one_shots_and_foreign_publishes(self):
        networks, containers, running, images = self.world()
        self.assertEqual(judge(networks, containers, running, dict(images, target="sha256:" + "7" * 64)),
                         ["service.target: IMAGE_MISMATCH"])
        self.assertEqual(judge(networks, containers, running, {k: v for k, v in images.items() if k != "api"}),
                         ["service.api: IMAGE_MISMATCH"])
        install = next(c for c in containers if c["service"] == "install")
        install["running"] = True
        self.assertIn("service.install: ONE_SHOT_RUNNING", judge(networks, containers, running, images))
        install["running"] = False
        exposed = stranger(ports={"80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "80"}]})
        self.assertEqual(judge(networks, containers, running + [exposed], images), [f"host.container.{'f' * 12}: NON_LOOPBACK_PUBLISH"])
        loopback = stranger(ports={"80/tcp": [{"HostIp": "127.0.0.1", "HostPort": "80"}]})
        self.assertEqual(judge(networks, containers, running + [loopback], images), [])  # 다른 스택의 loopback publish는 받는다

    def test_topology_mismatches_from_check_topology(self):
        networks, containers, running, images = self.world()
        target_net = next(n for n in networks if n["Labels"]["com.docker.compose.network"] == "target")
        target_net["Internal"] = False
        self.assertIn("network.target: INTERNAL_MISMATCH", judge(networks, containers, running, images))
        networks, containers, running, images = self.world()
        meter_id = next(c["id"] for c in containers if c["service"] == "eval-meter")
        target_net = next(n for n in networks if n["Labels"]["com.docker.compose.network"] == "target")
        target_net["Containers"][meter_id]["IPv4Address"] = "10.252.3.21/24"
        self.assertIn("network.target: ADDRESS_MISMATCH", judge(networks, containers, running, images))


class TestLayoutRender(unittest.TestCase):
    """실제 분리 배치 렌더(제품 compose + overlay)로 같은 대조를 돌린다."""

    def test_real_render(self):
        sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "ci"))
        try:
            import test_product_eval_split as split_render
        finally:
            sys.path.pop(0)
        try:
            rendered = split_render.render((*split_render.PRODUCT_FILES, split_render.OVERLAY), split_render.SYNTHETIC)
        except (OSError, RuntimeError, AssertionError) as exc:  # compose 없는 환경
            self.skipTest(f"docker compose 렌더 불가: {type(exc).__name__}")
        networks, containers, images = layout_world(rendered)
        running = [c for c in containers if c["running"]]
        self.assertEqual(check_split_layout(rendered, PROJECT, [parse_network(n) for n in networks],
                                            [parse_container(c) for c in containers], [parse_container(c) for c in running],
                                            images), [])
        target = next(c for c in containers if c["service"] == "target")
        target["ports"] = {"5000/tcp": [{"HostIp": "127.0.0.1", "HostPort": "39001"}]}
        sharer = stranger(network_mode="container:" + target["id"])
        result = check_split_layout(rendered, PROJECT, [parse_network(n) for n in networks],
                                    [parse_container(c) for c in containers],
                                    [parse_container(c) for c in running + [sharer]], images)
        self.assertEqual(result, sorted(["service.target: PUBLISH_MISMATCH", f"host.container.{'f' * 12}: SHARES_NETWORK_NAMESPACE"]))


class LayoutDocker:
    """DockerSplitPorts.check가 부르는 Docker 조회만 합성 상태로 답한다."""

    def __init__(self, networks, containers, running, images):
        self.networks, self.containers, self.running, self.images = networks, containers, running, images

    def project_network_ids(self, project):
        return [n["Id"] for n in self.networks if n["Labels"]["com.docker.compose.project"] == project]

    def inspect_networks(self, ids):
        return [n for n in self.networks if n["Id"] in ids]

    def project_container_ids(self, project):
        return [c["id"] for c in self.containers if c["project"] == project]

    def running_container_ids(self):
        return [c["id"] for c in self.running]

    def inspect_containers(self, ids):
        everything = {c["id"]: c for c in self.containers + self.running}
        return [everything[i] for i in ids]

    def image_id(self, reference):
        return self.images[reference]


class TestCheckPort(PortsCase):
    """실제 포트(DockerSplitPorts.check) → check_split_layout 연결. 리뷰 반례를 같은 경로로 넣는다."""

    def ports_for(self, mutate=None):
        networks, containers, images = layout_world(project=self.plan.project)
        running = [c for c in containers if c["running"]]
        if mutate:
            running = mutate(containers, running)
        by_image = {LAYOUT["services"][name]["image"]: image for name, image in images.items()}
        docker = LayoutDocker(networks, containers, running, by_image)
        return DockerSplitPorts(config=self.config, split=self.split, plan=self.plan, directory=self.directory, docker=docker,
                                driver=self.driver, inputs=self.inputs, rendered=LAYOUT, manifest=MANIFEST,
                                model_key=ModelKey("sk-or-v1-test"), exploration={})

    def test_clean_and_counterexamples(self):
        result = self.ports_for().check("pre")
        self.assertEqual((result["phase"], result["violations"], result["containers"]), ("pre", [], 5))

        def publish(containers, running):
            next(c for c in containers if c["service"] == "target")["ports"] = {
                "5000/tcp": [{"HostIp": "127.0.0.1", "HostPort": "39001"}]}
            return running

        def share(containers, running):
            return running + [stranger(network_mode="container:" + next(c["id"] for c in containers if c["service"] == "target"))]

        self.assertEqual(self.ports_for(publish).check("post")["violations"], ["service.target: PUBLISH_MISMATCH"])
        self.assertEqual(self.ports_for(share).check("post")["violations"], [f"host.container.{'f' * 12}: SHARES_NETWORK_NAMESPACE"])


class TestDocker(PortsCase):
    def test_up_waits_until_ready_then_times_out(self):
        clock = {"t": 0.0}
        sequence = [[{"service": "a", "status": "created", "exit_code": 0, "health": None}],
                    [{"service": "a", "status": "running", "exit_code": 0, "health": None}]]
        self.docker.container_states = lambda ids: sequence.pop(0) if len(sequence) > 1 else sequence[0]
        with mock.patch("runner_split_ports.time.monotonic", lambda: clock["t"]), \
                mock.patch("runner_split_ports.time.sleep", lambda s: clock.__setitem__("t", clock["t"] + s)):
            self.ports.up()
            self.assertEqual(clock["t"], self.config.run["budget_poll_seconds"])  # 한 번 기다린 뒤 준비됨
            self.docker.container_states = lambda ids: [{"service": "a", "status": "created", "exit_code": 0, "health": None}]
            clock["t"] = 0.0
            with self.assertRaises(SplitReadyError) as caught:
                self.ports.up()
        self.assertEqual(str(caught.exception), "split_ready_timeout")
        self.assertGreaterEqual(clock["t"], self.config.run["compose_wait_seconds"])

    def test_compose_seal_and_stop(self):
        self.ports.up()
        self.ports.down()
        self.ports.seal("m" * 64)
        self.ports.stop_boundary("b" * 64)
        up, down, seal, stop = self.docker.calls
        self.assertEqual(up[1][:5], ("compose", "-p", "eval-r1", "--env-file", "/dev/null"))
        self.assertEqual(down[2], self.config.run["down_timeout_seconds"])
        self.assertEqual(seal, ("signal", "m" * 64, "USR1"))
        self.assertEqual(stop, ("stop", "b" * 64, self.split.boundary_stop_seconds))
        self.assertFalse(self.ports.preexisting())
        self.assertEqual(set(self.ports.image_ids()), set(self.plan.images))

    def test_boundary_stop_bounds_reach_the_real_docker_helper(self):
        # 리뷰 반례: 설정이 받은 값이 실제 Docker 부품에서 거부되지 않는다(경계값 1·600을 실제 stop_container로 보낸다).
        from runner_docker import CommandResult, Docker
        for seconds in (1, 600):
            with self.subTest(seconds=seconds):
                sent = []
                docker = Docker(lambda args, env, stdin, timeout: (sent.append(list(args)), CommandResult(0, b""))[1], {}, 1)
                ports = DockerSplitPorts(config=self.config, split=parse_split(split_block(boundary_stop_seconds=seconds), self.config),
                                         plan=self.plan, directory=self.directory, docker=docker, driver=self.driver,
                                         inputs=self.inputs, rendered={}, manifest=MANIFEST, model_key=ModelKey("sk-or-v1-test"),
                                         exploration={})
                ports.stop_boundary("b" * 64)
                self.assertEqual(sent, [["stop", "--time", str(seconds), "b" * 64]])

    def test_probes_use_a_fresh_cid_dir_per_phase_and_the_model_host(self):
        seen = []

        def fake(docker, image, snapshot, ports, external, timeout, uid, gid, cid_dir):
            seen.append((image, external, cid_dir))
            return "outcome"
        with mock.patch("runner_split_ports.run_split_probes", fake), mock.patch("runner_split_ports.split_ports", lambda *a: "ports"):
            self.assertEqual(self.ports.probes("pre", {}), "outcome")
            self.ports.probes("post", {})
            with self.assertRaises(FileExistsError):
                self.ports.probes("pre", {})  # 같은 단계 cid 디렉터리를 다시 쓰지 않는다
        self.assertEqual([(s[0], s[1], s[2].name) for s in seen],
                         [(self.config.images["meter"].id, "openrouter.ai:443", "pre-probe-cid"),
                          (self.config.images["meter"].id, "openrouter.ai:443", "post-probe-cid")])
        self.assertEqual(stat.S_IMODE(os.stat(seen[0][2]).st_mode), 0o700)

    def test_remove_inputs_is_idempotent(self):
        self.ports.remove_inputs()
        self.assertFalse(self.inputs.directory.exists())
        self.ports.remove_inputs()


if __name__ == "__main__":
    unittest.main()
