"""탐색 분리 배치 여러 출발점 망 점검(결정안 §2.1, 4-8) 시험."""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_docker import DockerError  # noqa: E402
from runner_probe import NEGATIVE, POSITIVE, RECORD, PathExpectation, ProbeSetupError, evaluate_probe, probe_plan  # noqa: E402
from runner_split_probe import (  # noqa: E402
    BOUNDARY, BROWSER, MCP, ORIGINS, PREPARATION, SplitPorts, run_split_probes, split_ports, split_probe_expectations,
)

IMAGE = "sha256:" + "a" * 64
EXTERNAL = "openrouter.ai:443"
RENDERED = {"services": {
    "eval-meter": {"command": ["-role=engine", "-listen=10.252.3.20:8080", "-explore-listen=10.252.11.20:8081",
                               "-upstream=http://target:3000", "-log-dir=/evidence"]},
    "explore-mcp": {"command": ["--headless", "--proxy-server", "http://explore-boundary:3128", "--cdp-endpoint",
                                "http://10.252.5.20:9222", "--port", "8931", "--host", "10.252.4.20"]},
    "explore-browser": {"command": ["--proxy-server", "http://explore-boundary:3128", "--cdp-listen", "10.252.5.20:9222"]},
}}
# 망 소속은 실제 렌더와 같은 모양(scripts/ci/test_product_eval_split.py 렌더에서 확인).
NETWORKS = {
    "preparation": {"control": {}, "explore_control": {}, "explore_direct": {}, "frontend": {}, "target": {}},
    "explore-boundary": {"eval_explore_ingress": {}, "explore_browser_ingress": {}, "frontend": {}, "target": {},
                         "explore_direct": {"aliases": ["explore-direct"]}},
    "explore-mcp": {"explore_cdp": {}, "explore_control": {}},
    "explore-browser": {"explore_browser_ingress": {}, "explore_cdp": {}},
    "postgres": {"control": {}},
}
for _name, _networks in NETWORKS.items():
    RENDERED["services"].setdefault(_name, {})["networks"] = _networks
BOUNDARY_CONFIG = {"browser_listen": "10.252.8.10:3128", "direct_listen": "10.252.7.10:3129"}
EXPLORATION = {"mcp_url": "http://explore-mcp:8931/mcp", "direct_proxy": "http://explore-direct:3129"}


def snapshot(**changes) -> dict:
    networks = {
        "target": {"members": {"eval-meter": ["10.252.3.20"], "preparation": ["10.252.3.12"], "explore-boundary": ["10.252.3.2"]}},
        "eval_explore_ingress": {"members": {"eval-meter": ["10.252.11.20"], "explore-boundary": ["10.252.11.10"]}},
        "explore_browser_ingress": {"members": {"explore-boundary": ["10.252.8.10"], "explore-browser": ["10.252.8.2"]}},
        "explore_cdp": {"members": {"explore-browser": ["10.252.5.20"], "explore-mcp": ["10.252.5.30"]}},
        "eval_target_back": {"members": {"target": ["10.252.10.21"], "eval-meter": ["10.252.10.20"]}},
    }
    networks.update(changes)
    services = {name: {"id": f"{index:x}" * 64, "exposed": []} for index, name in enumerate(ORIGINS, start=1)}
    services["target"] = {"id": "f" * 64, "exposed": ["3000/tcp", "9000/tcp", "53/udp"]}
    return {"networks": networks, "services": services}


def ports() -> SplitPorts:
    return split_ports(RENDERED, BOUNDARY_CONFIG, EXPLORATION)


class FakeDocker:
    """출발점 컨테이너 ID별로 결과를 돌려준다. fail[origin] = {검사 id: (observed, code)}로 기본 결과를 바꾼다."""

    def __init__(self, snap: dict, fail: dict | None = None, error_on: str = ""):
        self.by_id = {s["id"]: name for name, s in snap["services"].items()}
        self.fail = fail or {}
        self.error_on = error_on
        self.calls: list[tuple[str, str, str]] = []

    def probe(self, image, container_id, plan, uid, gid, cidfile):
        origin = self.by_id[container_id]
        self.calls.append((image, origin, cidfile.name))
        if origin == self.error_on:
            raise DockerError("docker_probe_failed")
        results = []
        for check in json.loads(plan)["checks"]:
            role = self.roles[origin][check["id"]]
            observed, code = ("success", "connected") if role in (POSITIVE, RECORD) else ("fail", "timeout")
            observed, code = self.fail.get(origin, {}).get(check["id"], (observed, code))
            results.append({"id": check["id"], "kind": check["kind"], "observed": observed, "code": code})
        return json.dumps({"results": results}).encode(), "c" * 64 + origin[-1]


def run(fail=None, error_on="", snap=None, image=IMAGE):
    snap = snap or snapshot()
    docker = FakeDocker(snap, fail, error_on)
    docker.roles = {o: {e.id: e.role for e in rows} for o, rows in split_probe_expectations(snap, ports(), EXTERNAL).items()}
    with tempfile.TemporaryDirectory() as tmp:
        outcome = run_split_probes(docker, image, snap, ports(), EXTERNAL, 500, 1000, 1000, Path(tmp))
    return outcome, docker


class TestSplitPorts(unittest.TestCase):
    def test_reads_and_pairs_ports(self):
        self.assertEqual(ports(), SplitPorts(("10.252.3.20", 8080), ("10.252.11.20", 8081), 3000, ("explore-mcp", 8931),
                                             ("explore-direct", 3129), ("explore-boundary", 3128), ("10.252.8.10", 3128),
                                             ("10.252.5.20", 9222)))

    def test_mismatched_or_missing_pairs_are_refused(self):
        def render(service, old, new):
            value = json.loads(json.dumps(RENDERED))
            value["services"][service]["command"] = [new if item == old else item for item in value["services"][service]["command"]]
            return value
        cases = {
            "mcp_port": (render("explore-mcp", "8931", "8932"), BOUNDARY_CONFIG, EXPLORATION, "split_mcp_port_mismatch"),
            "direct_port": (RENDERED, dict(BOUNDARY_CONFIG, direct_listen="10.252.7.10:3130"), EXPLORATION, "split_direct_port_mismatch"),
            "browser_port": (RENDERED, dict(BOUNDARY_CONFIG, browser_listen="10.252.8.10:3127"), EXPLORATION,
                             "split_browser_port_mismatch"),
            "cdp": (render("explore-browser", "10.252.5.20:9222", "10.252.5.21:9222"), BOUNDARY_CONFIG, EXPLORATION,
                    "split_cdp_mismatch"),
            "mcp_https": (RENDERED, BOUNDARY_CONFIG, dict(EXPLORATION, mcp_url="https://explore-mcp:8931/mcp"),
                          "split_exploration_invalid"),
            "mcp_no_port": (RENDERED, BOUNDARY_CONFIG, dict(EXPLORATION, mcp_url="http://explore-mcp/mcp"), "split_exploration_invalid"),
            "dup_flag": (render("eval-meter", "-log-dir=/evidence", "-listen=10.252.3.21:8080"), BOUNDARY_CONFIG, EXPLORATION,
                         "split_flag_invalid:eval-meter:listen"),
            "no_flag": (render("explore-browser", "--cdp-listen", "--cdp-x"), BOUNDARY_CONFIG, EXPLORATION,
                        "split_flag_invalid:explore-browser:cdp-listen"),
            "no_command": ({"services": {}}, BOUNDARY_CONFIG, EXPLORATION, "split_command_missing:eval-meter"),
            # 리뷰 반례: 포트를 같게 맞춰도 다른 서비스(postgres)를 MCP 자리에 둘 수 없다.
            "mcp_other_service_same_port": (render("explore-mcp", "8931", "5432"), BOUNDARY_CONFIG,
                                            dict(EXPLORATION, mcp_url="http://postgres:5432/mcp"), "split_mcp_host_mismatch"),
            "mcp_host_only": (RENDERED, BOUNDARY_CONFIG, dict(EXPLORATION, mcp_url="http://postgres:8931/mcp"),
                              "split_mcp_host_mismatch"),
            # 경계 서비스 이름은 preparation과 망을 여럿 같이 써서 직접 입구 망 주소로 풀린다는 보장이 없다(별칭만).
            "direct_service_name": (RENDERED, BOUNDARY_CONFIG, dict(EXPLORATION, direct_proxy="http://explore-boundary:3129"),
                                    "split_direct_host_mismatch"),
            "direct_other_host": (RENDERED, BOUNDARY_CONFIG, dict(EXPLORATION, direct_proxy="http://explore-mcp:3129"),
                                  "split_direct_host_mismatch"),
            "browser_other_host": (render("explore-browser", "http://explore-boundary:3128", "http://explore-mcp:3128"),
                                   BOUNDARY_CONFIG, EXPLORATION, "split_browser_host_mismatch"),
            "browser_alias_other_net": (render("explore-browser", "http://explore-boundary:3128", "http://explore-direct:3128"),
                                        BOUNDARY_CONFIG, EXPLORATION, "split_browser_host_mismatch"),
            "listen_port": (render("eval-meter", "-listen=10.252.3.20:8080", "-listen=10.252.3.20:0"), BOUNDARY_CONFIG,
                            EXPLORATION, "split_meter_listen_invalid"),
        }
        for name, (rendered, boundary, exploration, code) in cases.items():
            with self.subTest(case=name), self.assertRaises(ProbeSetupError) as caught:
                split_ports(rendered, boundary, exploration)
            self.assertEqual(str(caught.exception), code)


class TestExpectations(unittest.TestCase):
    def test_table_per_origin(self):
        plan = split_probe_expectations(snapshot(), ports(), EXTERNAL)
        table = {origin: {(e.id, e.target, e.role) for e in rows} for origin, rows in plan.items()}
        targets = {(f"target_ip:10.252.10.21:{p}", f"10.252.10.21:{p}", NEGATIVE) for p in (3000, 9000)}
        no_meter = {("meter_main", "10.252.3.20:8080", NEGATIVE), ("meter_explore", "10.252.11.20:8081", NEGATIVE)}
        self.assertEqual(table[PREPARATION], {
            ("meter_main", "10.252.3.20:8080", POSITIVE), ("mcp", "explore-mcp:8931", POSITIVE),
            ("boundary_direct", "explore-direct:3129", POSITIVE), ("meter_explore", "10.252.11.20:8081", NEGATIVE),
            ("boundary_browser_ingress", "10.252.8.10:3128", NEGATIVE), ("browser_cdp", "10.252.5.20:9222", NEGATIVE),
            ("external", EXTERNAL, RECORD)} | targets)
        self.assertEqual(table[BOUNDARY], {("meter_explore", "10.252.11.20:8081", POSITIVE), ("meter_main", "10.252.3.20:8080", RECORD),
                                           ("external", EXTERNAL, RECORD)} | targets)
        self.assertEqual(table[BROWSER], {("boundary_browser_ingress", "explore-boundary:3128", POSITIVE),
                                          ("external", EXTERNAL, NEGATIVE)} | no_meter | targets)
        self.assertEqual(table[MCP], {("browser_cdp", "10.252.5.20:9222", POSITIVE), ("external", EXTERNAL, NEGATIVE)}
                         | no_meter | targets)

    def test_live_addresses_must_match_the_render(self):
        cases = {
            "meter_moved": ({"target": {"members": {"eval-meter": ["10.252.3.99"]}}}, "split_address_mismatch:eval-meter:target"),
            "meter_two": ({"target": {"members": {"eval-meter": ["10.252.3.20", "10.252.3.21"]}}},
                          "split_address_mismatch:eval-meter:target"),
            "boundary_missing": ({"explore_browser_ingress": {"members": {}}},
                                 "split_address_mismatch:explore-boundary:explore_browser_ingress"),
            "cdp_moved": ({"explore_cdp": {"members": {"explore-browser": ["10.252.5.21"]}}},
                          "split_address_mismatch:explore-browser:explore_cdp"),
            "no_target": ({"eval_target_back": {"members": {}}}, "target_address_unknown"),
        }
        for name, (change, code) in cases.items():
            with self.subTest(case=name), self.assertRaises(ProbeSetupError) as caught:
                split_probe_expectations(snapshot(**change), ports(), EXTERNAL)
            self.assertEqual(str(caught.exception), code)
        with self.assertRaises(ProbeSetupError):
            split_probe_expectations(snapshot(), ports(), "openrouter.ai")


class TestRun(unittest.TestCase):
    def test_all_origins_pass_with_own_containers(self):
        outcome, docker = run()
        self.assertEqual((outcome.status, outcome.reasons), ("pass", ()))
        self.assertEqual([c[1] for c in docker.calls], list(ORIGINS))
        self.assertEqual(len({c[2] for c in docker.calls}), 4)
        self.assertTrue(all(c[0] == IMAGE for c in docker.calls))
        self.assertEqual(len(outcome.container_ids), 4)
        recorded = [r for r in outcome.origins[PREPARATION].rows if r["id"] == "external"]
        self.assertEqual((recorded[0]["verdict"], recorded[0]["observed"], recorded[0]["expected"]), ("recorded", "success", "any"))

    def test_record_rows_do_not_judge(self):
        outcome, _ = run(fail={PREPARATION: {"external": ("fail", "timeout")}, BOUNDARY: {"meter_main": ("fail", "refused")}})
        self.assertEqual(outcome.status, "pass")

    def test_violation_invalid_and_errors(self):
        cases = {
            "browser_reaches_meter": ({BROWSER: {"meter_main": ("success", "connected")}}, "", "violation",
                                      ("explore-browser:negative_path_reachable",)),
            "prep_reaches_target": ({PREPARATION: {"target_ip:10.252.10.21:3000": ("success", "connected")}}, "", "violation",
                                    ("preparation:negative_path_reachable",)),
            "mcp_external_dns": ({MCP: {"external": ("fail", "dns_error")}}, "", "pass", ()),
            "boundary_no_upstream": ({BOUNDARY: {"meter_explore": ("fail", "timeout")}}, "", "invalid",
                                     ("explore-boundary:positive_control_failed",)),
            "target_refused": ({MCP: {"target_ip:10.252.10.21:9000": ("fail", "refused")}}, "", "invalid",
                               ("explore-mcp:failure_kind_mismatch",)),
            "violation_and_invalid": ({BROWSER: {"meter_main": ("success", "connected")},
                                       MCP: {"browser_cdp": ("fail", "timeout")}}, "", "invalid",
                                      ("explore-browser:negative_path_reachable", "explore-mcp:positive_control_failed")),
            "docker_error_first": (None, PREPARATION, "invalid", ("preparation:probe_run_failed",)),
            "docker_error_last": (None, MCP, "invalid", ("explore-mcp:probe_run_failed",)),
        }
        for name, (fail, error_on, status, reasons) in cases.items():
            with self.subTest(case=name):
                outcome, docker = run(fail=fail, error_on=error_on)
                self.assertEqual((outcome.status, outcome.reasons), (status, reasons))
                self.assertEqual(len(docker.calls), 4)  # 한 출발점이 실패해도 나머지 근거를 남긴다

    def test_setup_failures_do_not_start_containers(self):
        outcome, docker = run(image="abc2lab-eval-meter:latest")
        self.assertEqual((outcome.status, outcome.reasons, docker.calls), ("invalid", ("probe_image_not_id",), []))
        snap = snapshot()
        docker = FakeDocker(snap)
        with tempfile.TemporaryDirectory() as tmp:
            outcome = run_split_probes(docker, IMAGE, snapshot(target={"members": {}}), ports(), EXTERNAL, 500, 1000, 1000, Path(tmp))
        self.assertEqual((outcome.status, outcome.reasons, docker.calls),
                         ("invalid", ("split_address_mismatch:eval-meter:target",), []))


class TestRecordRole(unittest.TestCase):
    def test_record_is_reported_but_never_judged(self):
        rows = (PathExpectation("p", "tcp", "a:1", POSITIVE, frozenset({"connected"})),
                PathExpectation("r", "tcp", "b:1", RECORD, frozenset()))
        for observed, code in (("success", "connected"), ("fail", "timeout"), ("fail", "refused")):
            output = json.dumps({"results": [{"id": "p", "kind": "tcp", "observed": "success", "code": "connected"},
                                             {"id": "r", "kind": "tcp", "observed": observed, "code": code}]}).encode()
            with self.subTest(observed=observed, code=code):
                outcome = evaluate_probe(rows, output)
                self.assertEqual(outcome.status, "pass")
                self.assertEqual([(r["verdict"], r["expected"]) for r in outcome.rows], [("match", "success"), ("recorded", "any")])
        self.assertIn(b'"r"', probe_plan(rows, 500))


if __name__ == "__main__":
    unittest.main()
