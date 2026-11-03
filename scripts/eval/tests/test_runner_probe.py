"""부정 연결 점검: 기대 도출·probe 계획·결과 판정. 실제 docker 없이 합성 스냅샷과 가짜 probe 출력으로 본다."""

from __future__ import annotations

import json
import os
import sys
import unittest
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_checks import network_snapshot
from runner_config import parse_runner_config, plan_run
from runner_docker import DockerError
from runner_probe import (
    INVALID, NEGATIVE, PASS, POSITIVE, VIOLATION, ProbeSetupError, evaluate_probe, probe_expectations, probe_plan,
    run_probe,
)
from test_preflight import configuration, local_llm
from test_runner_checks import DAEMON, PROJECT, SERVICE_IDS, by_key, by_service, facts, raw_world
from test_runner_config import runner_config


def make_plan(backend="api", **ports):
    preflight = configuration()
    if backend == "local":
        for side in ("baseline", "ours"):
            preflight[side]["llm"] = local_llm()
    payload = runner_config()
    payload["ports"].update(ports)
    return plan_run(parse_runner_config(payload), preflight, "ours", "ours-1")


def make_snapshot(networks=None, containers=None):
    world = raw_world()
    return network_snapshot(DAEMON, PROJECT, *facts(networks or world[0], containers or world[1]))


EXPECTED_API = {
    "meter_tcp": ("tcp", "eval-meter:8080", POSITIVE),
    "llm_gateway_tcp": ("tcp", "eval-llm-gw:8090", POSITIVE),
    "llm_upstream:llm-api.example:443": ("tcp", "llm-api.example:443", NEGATIVE),
    "target_ip:172.31.0.2:3000": ("tcp", "172.31.0.2:3000", NEGATIVE),
    "obs_ip:172.31.0.4:8081": ("tcp", "172.31.0.4:8081", NEGATIVE),
    "obs_ip:172.34.0.2:8081": ("tcp", "172.34.0.2:8081", NEGATIVE),
    "dns:target": ("dns", "target", NEGATIVE),
    "dns:eval-ours-1-target-1": ("dns", "eval-ours-1-target-1", NEGATIVE),
    "dns:333333333333": ("dns", "333333333333", NEGATIVE),
    "dns:eval-obs": ("dns", "eval-obs", NEGATIVE),
    "dns:eval-ours-1-eval-obs-1": ("dns", "eval-ours-1-eval-obs-1", NEGATIVE),
    "dns:555555555555": ("dns", "555555555555", NEGATIVE),
    "gateway_obs:172.30.0.1:18081": ("tcp", "172.30.0.1:18081", NEGATIVE),
}


def expected_report(expectations, **overrides):
    """기대대로 나온 probe 출력. overrides로 특정 id의 (observed, code)를 바꾼다."""
    natural = {"meter_tcp": ("success", "connected"), "llm_gateway_tcp": ("success", "connected")}
    results = []
    for e in expectations:
        if e.id in overrides:
            observed, code = overrides[e.id]
        elif e.id in natural:
            observed, code = natural[e.id]
        elif e.kind == "dns":
            observed, code = "fail", "not_found"
        elif e.id.startswith("gateway_obs:"):
            observed, code = "fail", "refused"
        else:
            observed, code = "fail", "timeout"
        results.append({"id": e.id, "kind": e.kind, "observed": observed, "code": code})
    return json.dumps({"results": results}).encode()


class TestExpectations(unittest.TestCase):
    def test_api_paths_from_snapshot_and_backend(self):
        expectations = probe_expectations(make_plan(), make_snapshot())
        self.assertEqual({e.id: (e.kind, e.target, e.role) for e in expectations}, EXPECTED_API)
        accepted = {e.id: e.accepted for e in expectations}
        self.assertEqual(accepted["meter_tcp"], {"connected"})
        self.assertEqual(accepted["target_ip:172.31.0.2:3000"], {"timeout", "unreachable"})
        self.assertEqual(accepted["dns:target"], {"not_found"})
        self.assertEqual(accepted["gateway_obs:172.30.0.1:18081"], {"refused", "timeout", "unreachable"})
        self.assertEqual(accepted["llm_upstream:llm-api.example:443"], {"timeout", "unreachable", "not_found", "dns_error"})
        self.assertEqual(accepted["llm_gateway_tcp"], {"connected"})

    def test_local_backend_changes_only_the_llm_path(self):
        local_rows = probe_expectations(make_plan("local"), make_snapshot())
        local = {e.id: e.target for e in local_rows}
        api = {e.id: e.target for e in probe_expectations(make_plan(), make_snapshot())}
        # 로컬은 고정 IP로 직접 닿는지 본다(엔진에는 extra_hosts가 없다). 경로 없음만 허용한다.
        self.assertEqual(local.pop("llm_upstream:172.17.0.1:11434"), "172.17.0.1:11434")
        self.assertEqual(next(e.accepted for e in local_rows if e.id.startswith("llm_upstream:")), {"timeout", "unreachable"})
        api.pop("llm_upstream:llm-api.example:443")
        self.assertEqual(local, api)

    def test_target_ports_are_upstream_port_plus_snapshot_ports(self):
        # 계측기 upstream 포트(러너가 넘기는 값)는 합집합에 들어가고 스냅샷 EXPOSE 포트도 유지된다.
        moved = make_plan(target=9999, obs=9998, obs_host=29999)
        got = {e.id: e.target for e in probe_expectations(moved, make_snapshot())}
        want = {k: v[1] for k, v in EXPECTED_API.items()} | {"target_ip:172.31.0.2:9999": "172.31.0.2:9999"}
        self.assertEqual(got, want)

    def test_target_without_expose_still_checked_on_upstream_port(self):
        networks, containers = raw_world()
        by_service(containers, "target")["ports"] = None
        targets = [e.target for e in probe_expectations(make_plan(), make_snapshot(networks, containers))
                   if e.id.startswith("target_ip:")]
        self.assertEqual(targets, ["172.31.0.2:3000"])

    def test_destinations_follow_inspect(self):
        networks, containers = raw_world()
        by_key(networks, "target_net")["Containers"][SERVICE_IDS["target"]].update(
            IPv4Address="172.31.0.50/16", IPv6Address="fd00::50/64")
        by_service(containers, "target")["ports"] = {"3000/tcp": None, "3001/tcp": None, "53/udp": None}
        by_key(networks, "engine_net")["IPAM"]["Config"].append({"Subnet": "fd01::/64", "Gateway": "fd01::1"})
        targets = {e.target for e in probe_expectations(make_plan(), make_snapshot(networks, containers))}
        for target in ("172.31.0.50:3000", "172.31.0.50:3001", "[fd00::50]:3000", "[fd00::50]:3001",
                       "[fd01::1]:18081", "172.30.0.1:18081"):
            self.assertIn(target, targets)
        self.assertNotIn("172.31.0.50:53", targets)
        # 엔진이 붙지 않은 망의 게이트웨이(예: 게이트웨이의 출구 망)는 점검 대상이 아니다.
        self.assertNotIn("172.32.0.1:18081", targets)

    def test_engine_network_without_host_gateway_has_no_gateway_rows(self):
        networks, containers = raw_world()
        by_key(networks, "engine_net")["IPAM"] = {"Config": []}
        rows = probe_expectations(make_plan(), make_snapshot(networks, containers))
        self.assertEqual([e for e in rows if e.id.startswith("gateway_obs:")], [])
        self.assertIn("llm_gateway_tcp", {e.id for e in rows})

    def test_missing_destinations_are_setup_errors(self):
        cases = []
        networks, containers = raw_world()
        for network in networks:
            network["Containers"].pop(SERVICE_IDS["target"], None)
        cases.append((networks, containers, "target_address_unknown"))
        networks, containers = raw_world()
        by_service(containers, "eval-obs")["ports"] = {}
        cases.append((networks, containers, "obs_port_unknown"))
        networks, containers = raw_world()
        by_service(containers, "eval-obs")["ports"] = {"8081/tcp": None}
        cases.append((networks, containers, "obs_host_port_unknown"))
        networks, containers = raw_world()
        by_key(networks, "obs_net")["Containers"][SERVICE_IDS["eval-obs"]]["Name"] = "bad name;x"
        cases.append((networks, containers, "dns_name_invalid"))
        for networks, containers, code in cases:
            with self.subTest(code=code), self.assertRaises(ProbeSetupError) as caught:
                probe_expectations(make_plan(), make_snapshot(networks, containers))
            self.assertEqual(str(caught.exception), code)
        plan = make_plan()
        plan.llm["backend"] = "gateway"
        with self.assertRaises(ProbeSetupError):
            probe_expectations(plan, make_snapshot())

    def test_too_many_checks_is_setup_error(self):
        networks, containers = raw_world()
        by_service(containers, "target")["ports"] = {f"{3000 + n}/tcp": None for n in range(52)}
        self.assertEqual(len(probe_expectations(make_plan(), make_snapshot(networks, containers))), 64)
        by_service(containers, "target")["ports"] = {f"{3000 + n}/tcp": None for n in range(53)}
        with self.assertRaises(ProbeSetupError) as caught:
            probe_expectations(make_plan(), make_snapshot(networks, containers))
        self.assertEqual(str(caught.exception), "probe_plan_invalid")

    def test_name_checks_cover_service_container_name_and_short_id(self):
        names = {e.target for e in probe_expectations(make_plan(), make_snapshot()) if e.kind == "dns"}
        self.assertEqual(names, {"target", "eval-ours-1-target-1", "333333333333",
                                 "eval-obs", "eval-ours-1-eval-obs-1", "555555555555"})

    def test_plan_is_tcp_or_dns_only(self):
        expectations = probe_expectations(make_plan(), make_snapshot())
        plan = json.loads(probe_plan(expectations, 2000))
        self.assertEqual(plan["timeout_ms"], 2000)
        for check in plan["checks"]:
            self.assertIn(check["kind"], ("tcp", "dns"))
            self.assertEqual(set(check), {"id", "kind", "address"} if check["kind"] == "tcp" else {"id", "kind", "name"})
        # 계측기 경로는 TCP 연결만: HTTP 요청을 보내면 계측기 전달 건수에 섞인다.
        meter = next(c for c in plan["checks"] if c["id"] == "meter_tcp")
        self.assertEqual(meter, {"id": "meter_tcp", "kind": "tcp", "address": "eval-meter:8080"})


class TestEvaluate(unittest.TestCase):
    def setUp(self):
        self.expectations = probe_expectations(make_plan(), make_snapshot())

    def evaluate(self, **overrides):
        return evaluate_probe(self.expectations, expected_report(self.expectations, **overrides))

    def test_all_expected_passes(self):
        outcome = self.evaluate()
        self.assertEqual((outcome.status, outcome.reasons), (PASS, ()))
        self.assertTrue(all(row["verdict"] == "match" for row in outcome.rows))
        self.assertEqual(len(outcome.rows), len(EXPECTED_API))

    def test_any_negative_success_is_violation(self):
        for check_id in [k for k, v in EXPECTED_API.items() if v[2] == NEGATIVE]:
            observed = ("success", "resolved") if check_id.startswith("dns:") else ("success", "connected")
            with self.subTest(check_id=check_id):
                outcome = self.evaluate(**{check_id: observed})
                self.assertEqual((outcome.status, outcome.reasons), (VIOLATION, ("negative_path_reachable",)))
                row = next(r for r in outcome.rows if r["id"] == check_id)
                self.assertEqual(row["verdict"], "negative_succeeded")

    def test_expected_failure_must_have_expected_kind(self):
        for check_id, observed in (("target_ip:172.31.0.2:3000", ("fail", "refused")),
                                   ("obs_ip:172.34.0.2:8081", ("fail", "error")),
                                   ("dns:target", ("fail", "timeout")),
                                   ("dns:eval-obs", ("fail", "dns_error")),
                                   ("gateway_obs:172.30.0.1:18081", ("fail", "error")),
                                   ("llm_upstream:llm-api.example:443", ("fail", "refused"))):
            with self.subTest(check_id=check_id):
                outcome = self.evaluate(**{check_id: observed})
                self.assertEqual((outcome.status, outcome.reasons), (INVALID, ("failure_kind_mismatch",)))
        for check_id, observed in (("target_ip:172.31.0.2:3000", ("fail", "unreachable")),
                                   ("gateway_obs:172.30.0.1:18081", ("fail", "timeout")),
                                   ("gateway_obs:172.30.0.1:18081", ("fail", "unreachable")),
                                   ("llm_upstream:llm-api.example:443", ("fail", "not_found")),
                                   ("llm_upstream:llm-api.example:443", ("fail", "dns_error"))):
            self.assertEqual(self.evaluate(**{check_id: observed}).status, PASS)

    def test_positive_control_failure_invalidates_whole_run(self):
        for check_id in ("meter_tcp", "llm_gateway_tcp"):
            for observed in (("fail", "timeout"), ("fail", "refused"), ("success", "connected_close_error")):
                with self.subTest(check_id=check_id, observed=observed):
                    outcome = self.evaluate(**{check_id: observed})
                    self.assertEqual((outcome.status, outcome.reasons), (INVALID, ("positive_control_failed",)))
        # 양성 대조가 죽은 실행에서는 위반 관측도 판정을 대신하지 않지만 이유와 행에는 남는다.
        outcome = self.evaluate(meter_tcp=("fail", "timeout"), **{"dns:target": ("success", "resolved")})
        self.assertEqual(outcome.status, INVALID)
        self.assertEqual(outcome.reasons, ("negative_path_reachable", "positive_control_failed"))

    def test_probe_output_errors_are_invalid_not_pass(self):
        good = json.loads(expected_report(self.expectations))
        bad_outputs = [
            b"", b"not json", b"\xff", json.dumps({"results": good["results"], "extra": 1}).encode(),
            json.dumps({"results": good["results"][:-1]}).encode(),
            json.dumps({"results": good["results"] + [good["results"][0]]}).encode(),
            json.dumps({"results": good["results"] + [{"id": "other", "kind": "tcp", "observed": "fail",
                                                        "code": "timeout"}]}).encode(),
            json.dumps({"results": [r | {"kind": "dns"} if r["id"] == "meter_tcp" else r
                                    for r in good["results"]]}).encode(),
            json.dumps({"results": [r | {"observed": "maybe"} if r["id"] == "dns:target" else r
                                    for r in good["results"]]}).encode(),
            json.dumps({"results": [r | {"code": "Timeout!"} if r["id"] == "dns:target" else r
                                    for r in good["results"]]}).encode(),
            json.dumps({"results": [r | {"extra": 1} if r["id"] == "dns:target" else r
                                    for r in good["results"]]}).encode(),
            # 끝 공백은 JSON으로는 유효하다. 크기 상한만으로 거부되는지 본다.
            expected_report(self.expectations) + b" " * (64 * 1024),
        ]
        for output in bad_outputs:
            with self.subTest(output=output[:40]):
                outcome = evaluate_probe(self.expectations, output)
                self.assertEqual((outcome.status, outcome.reasons), (INVALID, ("probe_output_invalid",)))
                self.assertTrue(all(row["verdict"] == "not_observed" and row["observed"] is None
                                    for row in outcome.rows))


METER_ID = "sha256:" + "4" * 64


class FakeDocker:
    def __init__(self, output=None, error=None):
        self.output, self.error, self.calls = output, error, []

    def probe(self, image, engine, plan, uid, gid):
        self.calls.append((image, engine, plan, uid, gid))
        if self.error:
            raise self.error
        return self.output(json.loads(plan)) if callable(self.output) else self.output


def clock():
    moments = iter(datetime(2026, 11, 2, 1, 0, s, tzinfo=timezone.utc) for s in range(60))
    return lambda: next(moments)


class TestRunProbe(unittest.TestCase):
    def test_runs_in_engine_netns_with_meter_image(self):
        snapshot, plan = make_snapshot(), make_plan()
        expectations = probe_expectations(plan, snapshot)
        docker = FakeDocker(lambda _plan: expected_report(expectations))
        outcome = run_probe(docker, METER_ID, snapshot, plan, 2000, 1000, 1000, clock())
        self.assertEqual(outcome.status, PASS)
        image, engine, sent, uid, gid = docker.calls[0]
        self.assertEqual((image, engine, uid, gid), (METER_ID, SERVICE_IDS["engine"], 1000, 1000))
        self.assertEqual(sent, probe_plan(expectations, 2000))
        self.assertEqual((outcome.started_at, outcome.finished_at),
                         ("2026-11-02T01:00:00+00:00", "2026-11-02T01:00:01+00:00"))

    def test_probe_image_must_be_content_id(self):
        for image in ("example/m:fixed", "example/m@sha256:" + "4" * 64, "4" * 64, "sha256:" + "A" * 64, None):
            docker = FakeDocker(b"{}")
            outcome = run_probe(docker, image, make_snapshot(), make_plan(), 2000, 1, 1, clock())
            self.assertEqual((outcome.status, outcome.reasons), (INVALID, ("probe_image_not_id",)))
            self.assertEqual(docker.calls, [])

    def test_probe_run_failure_is_invalid(self):
        outcome = run_probe(FakeDocker(error=DockerError("docker_probe_failed")), METER_ID, make_snapshot(),
                            make_plan(), 2000, 1, 1, clock())
        self.assertEqual((outcome.status, outcome.reasons), (INVALID, ("probe_run_failed",)))
        self.assertTrue(outcome.rows and all(r["verdict"] == "not_observed" for r in outcome.rows))

    def test_setup_failure_is_invalid_without_running_probe(self):
        networks, containers = raw_world()
        by_service(containers, "eval-obs")["ports"] = {}
        docker = FakeDocker(b"{}")
        outcome = run_probe(docker, METER_ID, make_snapshot(networks, containers), make_plan(), 2000, 1, 1, clock())
        self.assertEqual((outcome.status, outcome.reasons, outcome.rows), (INVALID, ("obs_port_unknown",), ()))
        self.assertEqual(docker.calls, [])


if __name__ == "__main__":
    unittest.main()
