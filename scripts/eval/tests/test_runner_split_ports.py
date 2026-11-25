"""탐색 분리 배치 O 회차 실제 포트(DockerSplitPorts, 4-7c-3) 시험. 가짜 Docker·가짜 구동기로 연결 값을 본다."""

from __future__ import annotations

import dataclasses
import json
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

    def copy_volume_files(self, image, volume, directory, names, role, uid, gid, cidfile, max_bytes, timeout, subpath=()):
        self.calls.append(("copy", image, volume, directory, tuple(names), role, uid, gid, cidfile.parent, max_bytes)
                          + ((tuple(subpath),) if subpath else ()))
        return dict(self.copies[role]), "c" * 64

    def run_bounded(self, args, timeout, max_stdout):
        raise AssertionError("시험은 도구 면 측정을 바꿔 끼운다")

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
        self.plan = plan_split_run(self.config, self.split, run_id="r1", target_image=TARGET, meter_port=8080, request_cap=100)
        (root / "run").mkdir(mode=0o700)
        defaults, _ = read_workflow_defaults()
        self.inputs = prepare_split_inputs(root / "run", manifest=MANIFEST, meter_port=8080, explore_port=8082,
                                           evidence_max_bytes=4 << 20, workflow_defaults=defaults, wide_writes=True,
                                           valid_for_hours=4, max_runs=2, run_seconds=2100, path_key=bytes(32), openssl=FakeOpenssl(),
                                           random_bytes=counter_bytes())
        self.directory = RunDirectory(run=root / "run", meter=root / "run" / "meter", llm=root / "run" / "llm", e2=root / "e2",
                                      state=root / "s", e2_config=root / "a", e2_control=root / "b", e2_copy=root / "c",
                                      e2_engine=root / "d", e2_state=root / "e")
        self.docker, self.driver = FakeDocker(), FakeDriver()
        self.ports = DockerSplitPorts(config=self.config, split=self.split, plan=self.plan, directory=self.directory,
                                      docker=self.docker, driver=self.driver, inputs=self.inputs, rendered={"services": {"a": {}}},
                                      manifest=MANIFEST, model_key=ModelKey("sk-or-v1-test"),
                                      exploration={"mcp_url": "http://explore-mcp:8931/mcp", "direct_proxy": "http://explore-direct:3129"},
                                      workflow_defaults=read_workflow_defaults())

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


class TestV3Inputs(PortsCase):
    def test_product_files_hash_config_only(self):
        # 결정안 §3.3 제품 블록 근거. 내용은 지문만 돌려주고 회차 디렉터리에 남기지 않는다(설치 입력에 비밀 핀).
        config = {"installation.json": b"i", "policy.json": b"p", "api-launch.json": b"a", "target-policy.json": b"t"}
        self.docker.copies["product-files"] = config
        before = sorted(os.listdir(self.directory.run))
        sha = lambda data: hashlib.sha256(data).hexdigest()
        self.assertEqual(self.ports.product_files(), {"installation": sha(b"i"), "policy": sha(b"p"), "api_launch": sha(b"a"),
                                                      "target_policy": sha(b"t")})
        (call,) = self.docker.calls
        self.assertEqual(call[1:7], (self.split.images["product"].id, "eval-r1_product-state", "config",
                                     ("installation.json", "policy.json", "api-launch.json", "target-policy.json"), "product-files", 1000))
        self.assertEqual(len(call), 10)  # 하위 경로 없음
        self.assertEqual(sorted(os.listdir(self.directory.run)), before)

    def test_runtime_conditions_are_read_below_the_draft_run_directory(self):
        self.docker.copies["product-run-conditions"] = {"runtime-conditions.json": b"{}\n"}
        self.assertEqual(self.ports.runtime_conditions("prep-0a1b"), b"{}\n")
        (call,) = self.docker.calls
        self.assertEqual(call[1:7] + call[10:], (self.split.images["product"].id, "eval-r1_product-state", "runs",
                                                 ("runtime-conditions.json",), "product-run-conditions", 1000, ("prep-0a1b", "state")))

    def test_e3_confirmation_is_read_from_the_draft_run_directory(self):
        self.docker.copies["product-e3-confirmation"] = {"e3-confirmation.json": b'{"version":"x"}'}
        self.assertEqual(self.ports.e3_confirmation("prep-0a1b"), b'{"version":"x"}')
        (call,) = self.docker.calls
        self.assertEqual(call[1:7] + call[10:], (self.split.images["product"].id, "eval-r1_product-state", "runs",
                                                 ("e3-confirmation.json",), "product-e3-confirmation", 1000, ("prep-0a1b",)))

    def test_control_policy_and_tool_surface_go_to_the_product_and_images(self):
        self.ports.control_policy("42")
        self.assertEqual(self.driver.calls[-1][:2], ("control_policy", ("42",)))
        with mock.patch("runner_split_ports.ours_side", return_value={"x": 1}) as side:
            self.assertEqual(self.ports.tool_surface(), {"x": 1})
        args = side.call_args.args
        self.assertEqual(args[:3], (self.split.images["product"].id, self.split.images["explore_browser"].id, self.docker.run_bounded))


class TestE3TraceSources(PortsCase):
    """E③ 흔적 원본 셋: Host v3 저널(Host uid, 실행 참조 해시 디렉터리), 제품 쪽 사본, 대상 선언 보호 저널."""

    def test_sources_are_copied_from_the_three_places(self):
        import hashlib
        from runner_e2v3 import MAX_JOURNAL_BYTES
        from runner_split_ports import MAX_PRODUCT_FILE_BYTES
        self.docker.copies.update({"e3trace-host": {"events.jsonl": b"h"}, "e3trace-product": {"events.jsonl": b"p"},
                                   "e3trace-declarations": {"preparation.journal": b"d"}, "e3trace-seal": {"seal-checks.json": b"s"},
                                   "e3trace-issued": {"evidence-checkpoints.json": b"i"}})
        self.assertEqual(self.ports.e3_trace_sources("prep-0a1b"),
                         {"e2_host_journal": b"h", "e2_product_journal": b"p", "declarations": b"d", "host_seal_checks": b"s",
                          "host_evidence_checkpoints": b"i"})
        run_directory = hashlib.sha256(b"run-prep-0a1b").hexdigest()  # 제품 실행 참조 = "run-" + 초안(작업) ID
        got = [(c[1], c[2], c[3], c[4], c[5], c[6], c[9], c[10]) for c in self.docker.calls]
        product = self.split.images["product"].id
        self.assertEqual(got, [
            (product, "eval-r1_e2-host-state", "journals", ("events.jsonl",), "e3trace-host", 1002, MAX_JOURNAL_BYTES, (run_directory,)),
            (product, "eval-r1_product-state", "runs", ("events.jsonl",), "e3trace-product", 1000, MAX_JOURNAL_BYTES, ("prep-0a1b", "state")),
            (product, "eval-r1_product-state", "state", ("preparation.journal",), "e3trace-declarations", 1000, MAX_PRODUCT_FILE_BYTES,
             ("targets", "declarations")),
            # Host 실행 디렉터리의 봉인 포함 확인·근거 체크포인트 기록(Host 저널 옆)
            (product, "eval-r1_e2-host-state", "journals", ("seal-checks.json",), "e3trace-seal", 1002, MAX_PRODUCT_FILE_BYTES,
             (run_directory,)),
            (product, "eval-r1_e2-host-state", "journals", ("evidence-checkpoints.json",), "e3trace-issued", 1002,
             MAX_PRODUCT_FILE_BYTES, (run_directory,))])

    def test_only_a_missing_file_is_none_and_other_failures_are_raised(self):
        from runner_docker import HELPER_FILE_MISSING, DockerError, HelperCleanupError
        real = FakeDocker.copy_volume_files.__get__(self.docker)
        self.docker.copies["e3trace-product"] = {"events.jsonl": b"p"}
        self.docker.copy_volume_files = mock.Mock(side_effect=lambda *a, **k: (real(*a, **k) if a[4] == "e3trace-product"
                                                                              else (_ for _ in ()).throw(DockerError(HELPER_FILE_MISSING))))
        self.assertEqual(self.ports.e3_trace_sources("prep-0a1b"),
                         {"e2_host_journal": None, "e2_product_journal": b"p", "declarations": None, "host_seal_checks": None,
                          "host_evidence_checkpoints": None})
        for error in (DockerError("docker_timeout"), DockerError("docker_helper_copy_failed"), HelperCleanupError()):
            with self.subTest(error=str(error)):
                self.docker.copy_volume_files = mock.Mock(side_effect=error)
                with self.assertRaises(DockerError):
                    self.ports.e3_trace_sources("prep-0a1b")


class TestE2V3Export(PortsCase):
    """E② v3 저널·봉인 head 꺼내기와 검증 연결. 위치는 설정값, 상한은 파일마다 다르다."""

    def setUp(self):
        super().setUp()
        from runner_e2v3 import parse_settings
        raw = {"audit_key_variable": "PRODUCT_E2_AUDIT_KEY_FILE", "volume": "product-state", "directory": "runs",
               "subpath": ["{draft_id}", "e2"], "journal_name": "events.jsonl", "head_name": "sealed-head.json", "uid": 1000,
               "verify_tool": "/opt/abc2lab/e2-audit-verify", "verify_timeout_seconds": 120}
        self.ports.split = dataclasses.replace(self.split, e2_v3=parse_settings(raw))
        # 설정이 있으면 보호 입력이 감사 키를 만든다(runner_split_main이 변수 이름을 넘김).
        (self.directory.run / "v3").mkdir(mode=0o700)
        self.inputs = prepare_split_inputs(self.directory.run / "v3", manifest=MANIFEST, meter_port=8080, explore_port=8082,
                                           evidence_max_bytes=4 << 20, workflow_defaults=read_workflow_defaults()[0],
                                           wide_writes=True, valid_for_hours=4, max_runs=2, run_seconds=2100, path_key=bytes(32),
                                           openssl=FakeOpenssl(), random_bytes=counter_bytes(),
                                           e2_audit_key_variable="PRODUCT_E2_AUDIT_KEY_FILE")
        self.ports.inputs = self.inputs

    def test_journal_and_head_are_copied_separately_below_the_draft(self):
        from runner_e2v3 import MAX_HEAD_BYTES, MAX_JOURNAL_BYTES
        self.docker.copies["e2v3-journal"] = {"events.jsonl": b"j"}
        self.docker.copies["e2v3-head"] = {"sealed-head.json": b"h"}
        self.assertEqual(self.ports.e2_v3_export("prep-0a1b"), (b"j", b"h"))
        got = [(c[2], c[3], c[4], c[5], c[6], c[9], c[10]) for c in self.docker.calls]
        self.assertEqual(got, [("eval-r1_product-state", "runs", ("events.jsonl",), "e2v3-journal", 1000, MAX_JOURNAL_BYTES, ("prep-0a1b", "e2")),
                               ("eval-r1_product-state", "runs", ("sealed-head.json",), "e2v3-head", 1000, MAX_HEAD_BYTES, ("prep-0a1b", "e2"))])

    def test_only_a_missing_file_is_none_and_runner_failures_are_raised(self):
        # #785 리뷰: 시간 초과·출력 상한·tar 손상 같은 러너 장애가 '제품이 안 냄'(미수집)으로 가려지면 안 된다.
        from runner_docker import HELPER_FILE_MISSING, DockerError, HelperCleanupError
        self.docker.copy_volume_files = mock.Mock(side_effect=DockerError(HELPER_FILE_MISSING))
        self.assertEqual(self.ports.e2_v3_export("prep-0a1b"), (None, None))
        self.docker.copies["e2v3-journal"] = {"events.jsonl": b"j"}
        real = FakeDocker.copy_volume_files.__get__(self.docker)
        self.docker.copy_volume_files = mock.Mock(side_effect=lambda *a, **k: (real(*a, **k) if a[4] == "e2v3-journal"
                                                                              else (_ for _ in ()).throw(DockerError(HELPER_FILE_MISSING))))
        self.assertEqual(self.ports.e2_v3_export("prep-0a1b"), (b"j", None))
        for error in (DockerError("docker_timeout"), DockerError("docker_copy_too_large"), DockerError("docker_helper_output_invalid"),
                      DockerError("docker_helper_copy_failed"), HelperCleanupError()):
            with self.subTest(error=str(error)):
                self.docker.copy_volume_files = mock.Mock(side_effect=error)
                with self.assertRaises(DockerError):
                    self.ports.e2_v3_export("prep-0a1b")

    def test_collect_uses_the_protected_key_and_the_configured_verifier(self):
        with mock.patch("runner_split_ports.subprocess_verifier", return_value="verifier") as verifier, \
                mock.patch("runner_split_ports.e2v3_collect", return_value={"outcome": "collected"}) as collect:
            self.assertEqual(self.ports.e2_v3_collect(b"j", b"h"), {"outcome": "collected"})
        verifier.assert_called_once_with("/opt/abc2lab/e2-audit-verify", 120)
        kwargs = collect.call_args.kwargs
        self.assertEqual((kwargs["run_verifier"], kwargs["key_path"], kwargs["journal"], kwargs["head"], kwargs["copy_directory"],
                          kwargs["key_ref"]),
                         ("verifier", self.inputs.files["e2_audit_key"], b"j", b"h", self.directory.run / "e2v3-copy",
                          self.inputs.audit_key_ref))
        self.assertIsNotNone(self.inputs.audit_key_ref)
class TestProductCommands(PortsCase):
    """O 제품 활동 표의 Bash 호출을 제품 DB 컨테이너 안 읽기 전용 세션으로 읽는다(공개 활동 API는 명령·결과를 지운다)."""

    def setUp(self):
        super().setUp()
        from types import SimpleNamespace
        from runner_docker import BoundedResult
        self.BoundedResult = BoundedResult
        self.ports.rendered = {"services": {"postgres": {"environment": {"POSTGRES_USER": "postgres", "POSTGRES_DB": "abc2lab"}}}}
        self.containers = [SimpleNamespace(id="db1", project="eval-r1", service="postgres", running=True),
                           SimpleNamespace(id="api1", project="eval-r1", service="api", running=True),
                           SimpleNamespace(id="db9", project="other", service="postgres", running=True)]
        self.docker.project_container_ids = lambda project: ["id"]
        self.docker.inspect_containers = lambda ids: self.containers
        self.bounded = []
        self.output = self.BoundedResult(0, b'{"cmd": "{}", "res": null, "is_error": false}\n\n', b"")
        self.docker.run_bounded = lambda args, timeout, limit: (self.bounded.append((list(args), timeout, limit)), self.output)[1]
        patcher = mock.patch("runner_split_ports.parse_container", side_effect=lambda c: c)
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_reads_only_that_exploration_in_a_read_only_session(self):
        self.assertEqual(self.ports.product_commands(7), [{"cmd": "{}", "res": None, "is_error": False}])
        ((args, timeout, limit),) = self.bounded
        self.assertEqual(args[:4], ["exec", "-e", "PGOPTIONS=-c default_transaction_read_only=on", "db1"])
        self.assertEqual(args[4:13], ["psql", "-X", "-A", "-t", "-q", "-v", "ON_ERROR_STOP=1", "-U", "postgres"])
        self.assertEqual(args[13:15], ["-d", "abc2lab"])
        self.assertIn("u.exploration_id = 7 order by u.id", args[-1])
        self.assertIn("u.tool = 'Bash'", args[-1])
        self.assertEqual((timeout, limit), (self.config.docker_timeout_seconds, self.config.findings["max_total_bytes"]))

    def test_refuses_what_it_cannot_pin(self):
        def db_name():
            self.ports.rendered["services"]["postgres"]["environment"]["POSTGRES_DB"] = "a;b"

        def stopped():
            self.containers[0].running = False

        def failed():
            self.output = self.BoundedResult(1, b"", b"x")

        def shape():
            self.output = self.BoundedResult(0, b'{"cmd": "{}"}\n', b"")

        for name, exploration, change in (("exploration_zero", 0, None), ("exploration_bool", True, None), ("db_name", 7, db_name),
                                          ("db_stopped", 7, stopped), ("psql_failed", 7, failed), ("row_shape", 7, shape)):
            with self.subTest(name=name):
                self.setUp()
                if change:
                    change()
                with self.assertRaises(ValueError):
                    self.ports.product_commands(exploration)

    TASK_ROW = {"task_id": 42, "status": "done", "exploration_id": 7, "completed_at": "2026-11-19T08:35:20Z",
                "goal_states": {"met": 2, "open": 1}, "intent_states": {"done": 1, "exhausted": 1}}

    def test_task_state_comes_from_the_product_db(self):
        # 제품 관리 API는 작업 단건·목표 목록을 중계하지 않는다(목록 307, 단건 403, 2026-11-19 O 진단 회차).
        self.output = self.BoundedResult(0, json.dumps(self.TASK_ROW).encode() + b"\n", b"")
        self.assertEqual(self.ports.task_signal("42"), {
            "status": "done", "completed_at": "2026-11-19T08:35:20Z", "exploration_id": 7, "goals_total": 3, "goals_met": 2,
            "goal_states": {"met": 2, "open": 1}, "intent_states": {"done": 1, "exhausted": 1}})
        ((args, _, limit),) = self.bounded
        self.assertEqual(args[:4], ["exec", "-e", "PGOPTIONS=-c default_transaction_read_only=on", "db1"])
        self.assertIn("from tasks t where t.id = 42", args[-1])
        self.assertIn("kind = 'goal'", args[-1])
        self.assertEqual(limit, 64 * 1024)
        self.assertEqual(self.ports.task_exploration_id("42"), 7)
        self.assertEqual(self.driver.calls, [])  # 제품 API는 부르지 않는다

    def test_task_state_refusals(self):
        bad_rows = {
            "extra": dict(self.TASK_ROW, name="x"),
            "status": dict(self.TASK_ROW, status="finished"),
            "exploration_zero": dict(self.TASK_ROW, exploration_id=0),
            "exploration_bool": dict(self.TASK_ROW, exploration_id=True),
            "stamp": dict(self.TASK_ROW, completed_at="2026-11-19 08:35"),
            "goal_state": dict(self.TASK_ROW, goal_states={"done": 1}),
            "goal_count": dict(self.TASK_ROW, goal_states={"met": 0}),
            "intent_state": dict(self.TASK_ROW, intent_states={"finished": 1}),
            "intent_count": dict(self.TASK_ROW, intent_states={"done": "1"}),
        }
        for name, row in bad_rows.items():
            with self.subTest(name=name):
                self.output = self.BoundedResult(0, json.dumps(row).encode() + b"\n", b"")
                with self.assertRaises(ValueError):
                    self.ports.task_signal("42")
        row_line = (json.dumps(self.TASK_ROW) + "\n").encode()
        for name, output in (("missing", b""), ("two", row_line * 2)):
            with self.subTest(name=name):
                self.output = self.BoundedResult(0, output, b"")
                with self.assertRaises(ValueError):
                    self.ports.task_signal("42")
        for task_id in ("0", "4/2", "", "1; drop"):
            with self.subTest(task_id=task_id):
                self.bounded.clear()
                with self.assertRaises(ValueError):
                    self.ports.task_signal(task_id)
                self.assertEqual(self.bounded, [])  # 조회하지 않는다


class TestV3Publishing(PortsCase):
    def test_split_conditions_use_the_installed_workflow_and_registration(self):
        from runner_split import read_workflow_defaults, split_conditions
        self.ports.prepare_round("tok")
        defaults, sha = read_workflow_defaults()
        got = self.ports.split_conditions()
        self.assertEqual(got, split_conditions(workflow_defaults=defaults, workflow_sha256=sha,
                                               install=self.inputs.install_public, registration=self.ports.registration))
        self.assertEqual(got["workflow_defaults_sha256"], sha)
        # #710 리뷰: 서명하는 SHA는 설치 입력을 만들 때 읽은 값이다. 회차 끝에 파일을 다시 읽지 않는다.
        self.ports.workflow_defaults = (defaults, "e" * 64)
        with mock.patch("runner_split_ports.read_workflow_defaults", side_effect=AssertionError("re-read")):
            self.assertEqual(self.ports.split_conditions()["workflow_defaults_sha256"], "e" * 64)
        self.ports.workflow_defaults = None
        with self.assertRaises(ValueError):
            self.ports.split_conditions()

    def test_boundary_directory_and_signature_use_the_runner_key(self):
        from run_record import sign_run_record
        self.assertEqual(self.ports.boundary_directory(), self.directory.run / "boundary")
        key = Path(self._tmp.name) / "runner.key"
        key.write_bytes(bytes(range(32)))
        key.chmod(0o600)
        self.ports.config = dataclasses.replace(self.config, runner_key_path=key)
        signed = self.ports.sign({"format": "x"})
        self.assertEqual(signed, sign_run_record({"format": "x"}, bytes(range(32))))


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

    def test_synthetic_mode_has_no_model_key_and_uses_the_declared_model(self):
        # 합성 모델 모드(2026-11-18 사용자 결정 (a)): manifest·키 없이 선언된 스크립트 모델 이름을 쓰고, 키를 넣지 않는다.
        ports = DockerSplitPorts(config=self.config, split=self.split, plan=self.plan, directory=self.directory, docker=self.docker,
                                 driver=self.driver, inputs=self.inputs, rendered={"services": {"a": {}}}, manifest=None,
                                 model_key=None, exploration={"mcp_url": "http://explore-mcp:8931/mcp",
                                                              "direct_proxy": "http://explore-direct:3129"},
                                 model_tag="scripted-product:v1")
        self.assertEqual(ports.model_tag, "scripted-product:v1")
        ports.prepare_round("tok")
        self.assertEqual([c[0] for c in self.driver.calls],
                         ["login", "catalog_profile", "register", "publish_challenge", "approve", "create_draft"])
        # 실제 모델·합성 모델 칸이 섞이면 만들지 않는다
        for manifest, key, tag in ((MANIFEST, None, None), (None, ModelKey("sk-or-v1-test"), "m"), (None, None, None),
                                   (MANIFEST, ModelKey("sk-or-v1-test"), "m")):
            with self.subTest(manifest=manifest is not None, key=key is not None, tag=tag), self.assertRaises(ValueError):
                DockerSplitPorts(config=self.config, split=self.split, plan=self.plan, directory=self.directory, docker=self.docker,
                                 driver=self.driver, inputs=self.inputs, rendered={"services": {}}, manifest=manifest,
                                 model_key=key, exploration={}, model_tag=tag)

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

    def test_declared_netns_does_not_hide_pid_sharing(self):
        # #668 리뷰 반례: 선언된 api→preparation 망 공유는 그대로 두고 api에 대상 PID namespace 공유만 더한다.
        networks, containers, running, images = self.world()
        api = next(c for c in containers if c["service"] == "api")
        api["pid_mode"] = "container:" + next(c["id"] for c in containers if c["service"] == "target")
        self.assertEqual(judge(networks, containers, [c for c in containers if c["running"]], images),
                         [f"host.container.{api['id'][:12]}: SHARES_PID_NAMESPACE"])

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

    def test_publish_address_comes_from_the_render(self):
        # 렌더의 host_ip를 그대로 기대값으로 쓴다(127.0.0.1로 가정하지 않는다).
        import copy
        rendered = copy.deepcopy(LAYOUT)
        rendered["services"]["api"]["ports"][0]["host_ip"] = "127.0.0.2"
        networks, containers, images = layout_world(rendered)
        running = [c for c in containers if c["running"]]
        self.assertEqual(judge(networks, containers, running, images, rendered), [])
        api = next(c for c in containers if c["service"] == "api")
        api["ports"]["8787/tcp"][0]["HostIp"] = "0.0.0.0"
        self.assertEqual(judge(networks, containers, running, images, rendered), ["service.api: PUBLISH_MISMATCH"])

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

        seen = {}

        def api_pid(containers, running):
            api = next(c for c in containers if c["service"] == "api")
            api["pid_mode"] = "container:" + next(c["id"] for c in containers if c["service"] == "target")
            seen["api"] = api["id"]
            return running

        violations = self.ports_for(api_pid).check("post")["violations"]
        self.assertEqual(violations, [f"host.container.{seen['api'][:12]}: SHARES_PID_NAMESPACE"])


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

    def test_generation_verify_uses_the_meter_image_key_and_lookup_env(self):
        from runner_docker import CommandResult, Docker
        sent = []
        docker = Docker(lambda args, env, stdin, timeout: (sent.append((list(args), dict(env), stdin, timeout)), CommandResult(0, b""))[1],
                        {}, 1)
        llm_env = {"EVAL_LLM_UPSTREAM": "https://llm.example", "EVAL_LLM_PROVIDER": "p/fp8", "EVAL_LLM_GENERATION_PATH": "/g",
                   "EVAL_LLM_VERIFY_ATTEMPTS": "3", "EVAL_LLM_VERIFY_INTERVAL": "2s", "EVAL_CA_BUNDLE": "/etc/ssl/ca.pem"}
        ports = DockerSplitPorts(config=self.config, split=self.split, plan=self.plan, directory=self.directory, docker=docker,
                                 driver=self.driver, inputs=self.inputs, rendered={"services": {}}, manifest=MANIFEST,
                                 model_key=ModelKey("sk-or-v1-secret"), exploration={}, llm_env=llm_env)
        cid = self.directory.run / "generation-verify.cid"
        with mock.patch("runner_docker._read_cidfile", return_value="c" * 64):
            ports.generation_verify(b'{"seq":1}\n', {"seed": 42})
        (args, env, stdin, timeout), = sent
        self.assertEqual(args[args.index("--") + 1], self.plan.images["meter"].id)
        self.assertEqual(args[args.index("--cidfile") + 1], str(cid))
        self.assertIn("-provider=p/fp8", args)
        self.assertIn('-sampling={"seed":42}', args)
        self.assertIn(f"type=bind,source={self.directory.llm},target=/evidence", args)
        self.assertNotIn("sk-or-v1-secret", " ".join(args))  # 키는 환경으로만
        self.assertEqual((env["EVAL_LLM_API_KEY"], stdin, timeout),
                         ("sk-or-v1-secret", b'{"seq":1}\n', self.config.run["llm_verify_timeout_seconds"]))

    def test_llm_verified_reads_the_generation_verify_marker_from_the_llm_dir(self):
        # #674 리뷰 P1: 실제 reader(GatewayFiles)로 generation-verify 표식을 읽는다. 게이트웨이 표식은 O 자리에서 받지 않는다.
        import json as _json
        from runner_evidence import EvidenceError
        self.directory.llm.mkdir(mode=0o700, exist_ok=True)
        marker = self.directory.llm / "llm-verified.json"
        self.assertIsNone(self.ports.llm_verified())  # 아직 없음
        for role, ok in (("generation-verify", True), ("llm-gateway", False)):
            with self.subTest(role=role):
                if marker.exists():
                    marker.unlink()
                fd = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
                os.write(fd, _json.dumps({"role": role, "backend": "api"}).encode())
                os.close(fd)
                if ok:
                    self.assertEqual(self.ports.llm_verified()["role"], "generation-verify")
                else:
                    with self.assertRaises(EvidenceError):
                        self.ports.llm_verified()

    def test_meter_describe_runs_the_meter_image_without_network(self):
        from runner_docker import BoundedResult, Docker
        sent = []
        for code, out, want in ((0, b'{"format": "abc2lab.eval-meter-describe/v1"}', {"format": "abc2lab.eval-meter-describe/v1"}),
                                (2, b"", None)):  # 옛 이미지: describe 역할 없음 → None
            docker = Docker(lambda *a, **k: None, {}, 1)
            docker.run_bounded = lambda args, timeout, limit, code=code, out=out: (sent.append(list(args)), BoundedResult(code, out, b""))[1]
            ports = DockerSplitPorts(config=self.config, split=self.split, plan=self.plan, directory=self.directory, docker=docker,
                                     driver=self.driver, inputs=self.inputs, rendered={"services": {}}, manifest=MANIFEST,
                                     model_key=ModelKey("sk-or-v1-test"), exploration={})
            self.assertEqual(ports.meter_describe(), want)
        self.assertIn("none", sent[0])
        self.assertEqual(sent[0][-3:], [self.plan.images["meter"].id, "-role", "describe"])

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
