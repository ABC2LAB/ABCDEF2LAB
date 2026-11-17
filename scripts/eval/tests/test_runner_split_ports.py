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
from runner_split_ports import DockerSplitPorts, product_record_names  # noqa: E402
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

    def compose_up(self, args, wait):
        self.calls.append(("up", tuple(args), wait))

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
                                      docker=self.docker, driver=self.driver, inputs=self.inputs, rendered={},
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


class TestDocker(PortsCase):
    def test_compose_seal_and_stop(self):
        self.ports.up()
        self.ports.down()
        self.ports.seal("m" * 64)
        self.ports.stop_boundary("b" * 64)
        up, down, seal, stop = self.docker.calls
        self.assertEqual(up[1][:5], ("compose", "-p", "eval-r1", "--env-file", "/dev/null"))
        self.assertEqual((up[2], down[2]), (self.config.run["compose_wait_seconds"], self.config.run["down_timeout_seconds"]))
        self.assertEqual(seal, ("signal", "m" * 64, "USR1"))
        self.assertEqual(stop, ("stop", "b" * 64, self.config.run["down_timeout_seconds"]))
        self.assertFalse(self.ports.preexisting())
        self.assertEqual(set(self.ports.image_ids()), set(self.plan.images))

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
