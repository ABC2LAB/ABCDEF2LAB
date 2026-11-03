"""회차 순서·실패 처리·근거·서명. docker·대상·엔진 없이 가짜 port로 본다."""

from __future__ import annotations

import copy
import os
import re
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import runner
from runner import COMPLETE, INVALID, RESULT_NAME, Clock, DockerPorts, Facts, RunFailure, build_spec, execute_run
from runner_config import (
    COMPOSE_COMMON, COMPOSE_EVAL, COMPOSE_LLM_API, COMPOSE_LOCAL_LLM, compose_environment, create_run_directory,
    parse_runner_config, plan_run,
)
from runner_probe import PASS, ProbeOutcome
from test_preflight import configuration
from test_runner_checks import DAEMON, raw_world
from test_runner_config import _yaml_without_comments, runner_config

ROLE_OF = {"engine": "engine", "engine-db": "engine_db", "target": "target", "eval-meter": "meter", "eval-obs": "meter",
           "eval-llm-gw": "meter", "eval-api": "meter"}


def make_spec():
    preflight = configuration()
    config = parse_runner_config(runner_config())
    plan = plan_run(config, preflight, "ours", "ours-1")
    return build_spec(config, plan, preflight)


class FakeClock:
    def __init__(self):
        self.t = 0.0

    def clock(self):
        return Clock(lambda: datetime(2026, 11, 2, tzinfo=timezone.utc) + timedelta(seconds=self.t),
                     lambda: self.t, self.sleep)

    def sleep(self, seconds):
        self.t += seconds


class FakeMeter:
    """계측기 bind 디렉터리 흉내. seal 뒤에야 drained 마커가 생긴다."""

    def __init__(self, ports):
        self.ports = ports
        self.pre_forwarded = 0
        self.per_poll = 30
        self.polls = 0
        self.drained = {"role": "engine", "drained_at": "t", "inflight_at_drain": 0, "log_complete": True,
                        "rejected_after_seal": 2, "forced_closed_streams": 0}
        self.observer_extra = 0
        self.rows_override = None
        self.drained_forwarded = None

    def _forwarded(self):
        return self.pre_forwarded if not self.ports.started else min(self.polls * self.per_poll, 10**6)

    def forwarded(self):
        if self.ports.started:
            self.polls += 1
        return self._forwarded()

    def engine_rows(self):
        if self.rows_override is not None:
            return self.rows_override
        n = self._forwarded()
        return [{"seq": i, "kind": "request"} for i in range(1, n + 1)] + \
               [{"seq": n + 1, "kind": "rejected"}, {"seq": n + 2, "kind": "rejected"}]

    def observer_rows(self):
        count = self.ports.ready_calls + 2 + self.observer_extra
        return [{"seq": i, "decision": "registered"} for i in range(1, count + 1)]

    def marker(self, name):
        if not self.ports.sealed:
            return None
        if name == "engine-drained.json":
            forwarded = self._forwarded() if self.drained_forwarded is None else self.drained_forwarded
            return dict(self.drained, forwarded_requests=forwarded)
        return {"role": "engine", "sealed_at": "t"}


class FakeEvidence:
    def __init__(self):
        self.files = {}

    def write_json(self, name, payload):
        if name in self.files:
            raise AssertionError("evidence written twice")
        self.files[name] = copy.deepcopy(payload)
        return "h-" + name

    def digests(self):
        return {name: "h-" + name for name in self.files}


class FakePorts:
    def __init__(self, spec):
        self.spec = spec
        self.calls = []
        self.fail = {}
        self.started = self.sealed = False
        self.ready_calls = 1
        self.post_world = None
        self.probe_status = {"pre": PASS, "post": PASS}
        self.phase = "pre"
        self.listeners = frozenset()
        self.running = []

    def _call(self, name):
        self.calls.append(name)
        if name in self.fail:
            raise self.fail[name]

    def world(self):
        networks, containers = raw_world()
        for container in containers:
            container["image"] = self.spec.image_ids[ROLE_OF[container["service"]]]
        return networks, containers

    def preexisting(self):
        self._call("preexisting")
        return False

    def image_ids(self):
        self._call("image_ids")
        return dict(self.spec.image_ids)

    def up(self):
        self._call("up")

    def down(self):
        self._call("down")

    def facts(self):
        self._call(f"facts:{self.phase}")
        networks, containers = self.world()
        if self.phase == "post" and self.post_world:
            self.post_world(networks, containers)
        return Facts(DAEMON, networks, containers, copy.deepcopy(self.running))

    def host_listeners(self):
        self._call(f"listeners:{self.phase}")
        return self.listeners

    def probe(self, snapshot):
        self._call(f"probe:{self.phase}")
        status = self.probe_status[self.phase]
        return ProbeOutcome(status, () if status == PASS else ("x",), (), "a", "b")

    def wait_ready(self):
        self._call("wait_ready")
        return self.ready_calls

    def scoreboard(self, phase, snapshot):
        self._call(f"scoreboard:{phase}")
        return {"raw": {"sha256": phase}, "normalized": {"sha256": phase}}

    def confirm_start(self):
        self._call("confirm_start")
        self.started = True

    def seal(self, meter_id):
        self._call("seal")
        self.sealed = True
        self.phase = "post"
        self.sealed_id = meter_id

    def findings(self):
        self._call("findings")
        return {"items": 0}

    def sign(self, payload):
        self._call("sign")
        return dict(payload, signature="sig")


def run(mutate=None):
    spec = make_spec()
    ports = FakePorts(spec)
    meter = FakeMeter(ports)
    evidence = FakeEvidence()
    clock = FakeClock()
    if mutate:
        mutate(ports, meter, clock)
    result = execute_run(spec, ports, meter, evidence, clock.clock())
    return result, ports, meter, evidence


FULL_ORDER = ["preexisting", "image_ids", "up", "facts:pre", "listeners:pre", "probe:pre", "wait_ready",
              "scoreboard:pre", "confirm_start", "seal", "facts:post", "listeners:post", "probe:post",
              "scoreboard:post", "findings", "down", "sign"]


class TestCompleteRun(unittest.TestCase):
    def test_order_and_signed_result(self):
        result, ports, meter, evidence = run()
        self.assertEqual(ports.calls, FULL_ORDER)
        self.assertEqual(result["status"], COMPLETE)
        self.assertEqual(result["signature"], "sig")
        self.assertEqual(evidence.files[RESULT_NAME], result)
        self.assertEqual(set(evidence.files), {"pre-checks.json", "pre-probe.json", "post-checks.json",
                                               "post-probe.json", "meter-summary.json", "findings.json", RESULT_NAME})
        # 서명 대상 근거 목록에는 결과 파일 자신이 없다(서명 뒤에 쓴다).
        self.assertEqual(set(result["evidence"]), set(evidence.files) - {RESULT_NAME})
        self.assertEqual(ports.sealed_id, "4" * 64)
        self.assertEqual(result["llm_backend"], "api")

    def test_request_budget_overshoot_recorded_not_hidden(self):
        result, _, _, evidence = run(lambda p, m, c: setattr(m, "per_poll", 35))
        summary = evidence.files["meter-summary.json"]
        self.assertEqual((summary["stop_reason"], summary["forwarded_requests"], summary["request_budget"],
                          summary["over_budget"]), ("request_budget", 105, 100, 5))
        self.assertEqual(summary["rejected_after_seal"], 2)
        self.assertEqual(result["meter"], summary)

    def test_time_budget_stop(self):
        result, _, meter, evidence = run(lambda p, m, c: setattr(m, "per_poll", 0))
        summary = evidence.files["meter-summary.json"]
        self.assertEqual((summary["stop_reason"], summary["forwarded_requests"], summary["over_budget"]),
                         ("time_budget", 0, 0))
        self.assertEqual(result["status"], COMPLETE)


def failing(code_or_name, error=None):
    return lambda p, m, c: p.fail.__setitem__(code_or_name, error or RuntimeError("boom /secret/path"))


class TestFailures(unittest.TestCase):
    def assert_invalid(self, result, ports, evidence, reason, *, down=True):
        self.assertEqual(result["status"], INVALID)
        self.assertEqual(result["reasons"], [reason] if isinstance(reason, str) else reason)
        self.assertEqual(evidence.files[RESULT_NAME], result)
        self.assertNotIn("sign", ports.calls)
        self.assertEqual(ports.calls.count("down"), 1 if down else 0)
        self.assertEqual(ports.calls.count("up"), 1 if "up" in ports.calls else 0)  # 재시도 없음
        for field in ("signature", "evidence", "meter", "images", "scoreboards", "policy_sha256"):
            self.assertNotIn(field, result)
        self.assertNotIn("/secret/path", repr(result))

    def test_pre_requests_must_be_zero(self):
        result, ports, _, evidence = run(lambda p, m, c: setattr(m, "pre_forwarded", 1))
        self.assert_invalid(result, ports, evidence, "pre_requests_nonzero")
        self.assertNotIn("confirm_start", ports.calls)
        self.assertNotIn("meter-summary.json", evidence.files)

    def test_each_step_failure_is_invalid_with_down_and_no_signature(self):
        cases = [
            ("up", "compose_up_failed"), ("facts:pre", "pre_facts_failed"), ("listeners:pre", "pre_host_listeners_failed"),
            ("probe:pre", "pre_probe_invalid"), ("wait_ready", "target_not_ready"), ("scoreboard:pre", "pre_scoreboard_failed"),
            ("confirm_start", "start_not_confirmed"), ("seal", "seal_failed"), ("facts:post", "post_facts_failed"),
            ("probe:post", "post_probe_invalid"), ("scoreboard:post", "post_scoreboard_failed"),
            ("findings", "findings_failed"),
        ]
        for name, reason in cases:
            with self.subTest(step=name):
                result, ports, _, evidence = run(failing(name))
                self.assert_invalid(result, ports, evidence, reason)
                self.assertEqual(ports.calls[-1], "down")

    def test_probe_statuses(self):
        for phase in ("pre", "post"):
            for status in ("violation", "invalid"):
                with self.subTest(phase=phase, status=status):
                    result, ports, _, evidence = run(lambda p, m, c: p.probe_status.__setitem__(phase, status))
                    self.assert_invalid(result, ports, evidence, f"{phase}_probe_{status}")
                    self.assertIn(f"{phase}-probe.json", evidence.files)

    def test_drain(self):
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "seal", lambda _id: p._call("seal")))
        self.assert_invalid(result, ports, evidence, "drain_timeout")
        for field, value in (("inflight_at_drain", 1), ("log_complete", False)):
            result, ports, _, evidence = run(lambda p, m, c: m.drained.__setitem__(field, value))
            self.assert_invalid(result, ports, evidence, "drain_incomplete")

    def test_boundary_changes_between_pre_and_post(self):
        def restart_target(networks, containers):
            next(c for c in containers if c["service"] == "target")["restart_count"] = 1
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "post_world", restart_target))
        self.assert_invalid(result, ports, evidence, "boundary_changed")

    def test_post_topology_violation(self):
        def attach_engine(networks, containers):
            target_net = next(n for n in networks if n["Labels"]["com.docker.compose.network"] == "target_net")
            target_net["Containers"]["1" * 64] = {"Name": "x", "IPv4Address": "172.31.0.9/16"}
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "post_world", attach_engine))
        self.assert_invalid(result, ports, evidence, "post_topology_violation")
        self.assertEqual(evidence.files["post-checks.json"]["topology_violations"],
                         ["network.target_net: MEMBERS_MISMATCH", "service.engine: ATTACHMENT_INCONSISTENT"])

    def test_meter_and_observer_records_must_agree(self):
        result, ports, _, evidence = run(lambda p, m, c: setattr(m, "observer_extra", 1))
        self.assert_invalid(result, ports, evidence, "observer_count_mismatch")
        result, ports, _, evidence = run(lambda p, m, c: setattr(m, "rows_override", [{"seq": 2, "kind": "request"}]))
        self.assert_invalid(result, ports, evidence, "meter_log_inconsistent")

    def test_meter_sequence_and_count_checked_separately(self):
        def gap(p, m, c):
            # 전달 건수는 drained와 같지만 순번 2가 빠짐(기록 유실)
            m.rows_override = [{"seq": 1, "kind": "request"}, {"seq": 3, "kind": "request"}]
            m.drained_forwarded = 2
        result, ports, _, evidence = run(gap)
        self.assert_invalid(result, ports, evidence, "meter_log_inconsistent")

        def count(p, m, c):
            # 순번은 완결이지만 drained 마커의 전달 건수와 다름
            m.rows_override = [{"seq": i, "kind": "request"} for i in (1, 2, 3)]
            m.drained_forwarded = 5
        result, ports, _, evidence = run(count)
        self.assert_invalid(result, ports, evidence, "meter_log_inconsistent")

    def test_host_conditions_block_the_run(self):
        from runner_checks import Listener
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "listeners", frozenset({Listener("0.0.0.0", 8080)})))
        self.assert_invalid(result, ports, evidence, "pre_host_condition_violation")
        self.assertEqual(evidence.files["pre-checks.json"]["host_violations"], ["host.listen.0.0.0.0:8080: NOT_ALLOWED"])
        foreign = {"id": "a" * 64, "project": "dev", "service": "web", "image": "x", "running": True,
                   "started_at": "t", "restart_count": 0, "ports": {"80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "80"}]},
                   "networks": {}}
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "running", [foreign]))
        self.assert_invalid(result, ports, evidence, "pre_host_condition_violation")

    def test_project_exists_or_image_mismatch_never_touches_compose(self):
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "preexisting", lambda: p._call("preexisting") or True))
        self.assert_invalid(result, ports, evidence, "project_exists", down=False)
        self.assertNotIn("up", ports.calls)

        def other_image(p, m, c):
            p.image_ids = lambda: dict(p.spec.image_ids, target="sha256:" + "f" * 64)
        result, ports, _, evidence = run(other_image)
        self.assert_invalid(result, ports, evidence, "image_id_mismatch", down=False)

    def test_running_container_image_must_match(self):
        def swap(networks, containers):
            next(c for c in containers if c["service"] == "engine")["image"] = "sha256:" + "f" * 64
        result, ports, _, evidence = run(lambda p, m, c: setattr(p, "post_world", swap))
        self.assert_invalid(result, ports, evidence, "post_topology_violation")

    def test_gateway_and_api_front_images_are_checked(self):
        for service in ("eval-llm-gw", "eval-api"):
            def swap(networks, containers, service=service):
                next(c for c in containers if c["service"] == service)["image"] = "sha256:" + "f" * 64
            result, ports, _, evidence = run(lambda p, m, c: setattr(p, "post_world", swap))
            with self.subTest(service=service):
                self.assert_invalid(result, ports, evidence, "post_topology_violation")
                self.assertIn(f"service.{service}: IMAGE_MISMATCH", evidence.files["post-checks.json"]["image_violations"])

    def test_down_failure_after_success_is_invalid_and_unsigned(self):
        result, ports, _, evidence = run(failing("down"))
        self.assert_invalid(result, ports, evidence, "down_failed")
        result, ports, _, evidence = run(lambda p, m, c: (p.fail.__setitem__("findings", RuntimeError()),
                                                          p.fail.__setitem__("down", RuntimeError())))
        self.assert_invalid(result, ports, evidence, ["findings_failed", "down_failed"])

    def test_sign_failure_is_invalid(self):
        result, ports, _, evidence = run(failing("sign"))
        self.assertEqual((result["status"], result["reasons"]), (INVALID, ["sign_failed"]))
        self.assertEqual(ports.calls[-2:], ["down", "sign"])

    def test_interrupt_still_downs_and_records(self):
        spec = make_spec()
        ports = FakePorts(spec)
        ports.fail["confirm_start"] = KeyboardInterrupt()
        evidence = FakeEvidence()
        with self.assertRaises(KeyboardInterrupt):
            execute_run(spec, ports, FakeMeter(ports), evidence, FakeClock().clock())
        self.assertEqual(evidence.files[RESULT_NAME]["reasons"], ["interrupted"])
        self.assertEqual(ports.calls[-1], "down")

    def test_unexpected_error_still_recorded(self):
        # 순서 코드 안에서 port가 아닌 곳의 예외(여기선 drained 마커 모양 이상)도 무효 기록·down으로 끝난다.
        result, ports, _, evidence = run(lambda p, m, c: setattr(m, "marker", lambda name: ["not", "dict"]))
        self.assertEqual(result["reasons"], ["unexpected_error"])
        self.assertEqual(result["failed_step"], "meter_read_failed")
        self.assertEqual(ports.calls[-1], "down")


class TestIsolationOfRunDirectory(unittest.TestCase):
    """절대규칙 7: scoreboard(GT)는 회차 디렉터리에만. 엔진 컨테이너에는 어떤 회차 디렉터리도 마운트되지 않는다."""

    def services_referencing(self, needle):
        found = set()
        for path in (COMPOSE_COMMON, COMPOSE_LLM_API, COMPOSE_LOCAL_LLM, COMPOSE_EVAL):
            section = service = None
            for line in _yaml_without_comments(Path(path).read_text(encoding="utf-8")).splitlines():
                if line and not line.startswith(" "):
                    section = line.split(":")[0]
                    continue
                match = re.fullmatch(r"  ([A-Za-z0-9_.-]+):.*", line)
                if section == "services" and match:
                    service = match.group(1)
                elif section == "services" and needle in line:
                    found.add(service)
        return found

    def test_evidence_dir_only_in_meter_and_observer(self):
        self.assertEqual(self.services_referencing("EVAL_EVIDENCE_DIR"), {"eval-meter", "eval-obs"})
        text = Path(COMPOSE_EVAL).read_text(encoding="utf-8")
        self.assertRegex(text, r"(?m)^  engine:\n(?:    .*\n)*?    volumes: !reset \[\]")

    def test_api_key_reaches_only_the_gateway(self):
        # 엔진·계측기·관측 front에는 API 키가 없다. 엔진은 자리값만 받는다(게이트웨이가 버린다).
        self.assertEqual(self.services_referencing("EVAL_LLM_API_KEY"), {"eval-llm-gw"})
        for name in ("OPENAI_API_KEY}", "ANTHROPIC_API_KEY", "${OPENAI_API_KEY"):
            self.assertEqual(self.services_referencing(name), set())
        self.assertEqual(self.services_referencing("EVAL_LLM_EVIDENCE_DIR"), {"eval-llm-gw"})

    def test_meter_mount_is_a_subdirectory_apart_from_scoreboards(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp, "runs")
            root.mkdir(mode=0o700)
            os.chmod(root, 0o700)
            config = parse_runner_config(runner_config() | {"runs_root": str(root)})
            plan = plan_run(config, configuration(), "ours", "ours-1")
            directory = create_run_directory(root, "ours-1")
            env = compose_environment(plan, directory)
            self.assertEqual(env["EVAL_EVIDENCE_DIR"], str(directory.meter))
            self.assertNotEqual(directory.meter, directory.run)
            self.assertNotIn(str(config.runner_key_path), "\n".join(env.values()))
            ports = DockerPorts(config, plan, directory, docker=None, findings=dict, confirm=lambda: None)
            snapshot = {"services": {"target": {"id": "3" * 64}, "engine": {"id": "1" * 64}}}
            with patch.object(runner, "collect_snapshot") as collect:
                collect.return_value.artifacts.raw = {"path": "pre/raw.json"}
                collect.return_value.artifacts.normalized = {"path": "pre/normalized.json"}
                ports.scoreboard("pre", snapshot)
            endpoint, run_directory, phase, _ = collect.call_args[0]
            self.assertEqual((run_directory, phase), (directory.run, "pre"))
            self.assertEqual(endpoint.origin, "http://127.0.0.1:18081")
            self.assertFalse(str(run_directory).startswith(str(directory.meter)))


class TestSignatureKey(unittest.TestCase):
    def test_key_read_only_at_sign_time_from_config_path(self):
        config = parse_runner_config(runner_config())
        plan = plan_run(config, configuration(), "ours", "ours-1")
        ports = DockerPorts(config, plan, None, docker=None, findings=dict, confirm=lambda: None)
        with patch.object(runner, "read_runner_key", return_value=b"k" * 32) as read_key:
            signed = ports.sign({"run_id": "ours-1"})
        read_key.assert_called_once_with("/srv/eval-keys/runner.key")
        self.assertRegex(signed["signature"], r"^[0-9a-f]{64}$")


if __name__ == "__main__":
    unittest.main()
