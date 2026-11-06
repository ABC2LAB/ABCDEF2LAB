"""러너 설정·회차 계획·회차 디렉터리. docker·대상 앱·모델을 사용하지 않는다."""

from __future__ import annotations

import copy
import json
import os
import re
import stat
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_config import (
    COMPOSE_COMMON, COMPOSE_E2_PRODUCT, COMPOSE_E2_STUB, COMPOSE_EVAL, COMPOSE_LLM_API, COMPOSE_LOCAL_LLM, E2_SERVICE, ENGINE_DATA_PATH,
    ENGINE_DB_DATA_PATH, METER_UPSTREAM_ALIAS,
    MAX_DRAIN_GRACE_SECONDS, MAX_GATEWAY_TEMPERATURE, METER_SERVICE, REPO_ROOT, SECRET_ENV_NAMES, RunnerConfigError,
    compose_base_args, secret_env_names, size_bytes, compose_environment, create_run_directory, load_runner_config,
    new_run_id, parse_runner_config, plan_run,
)
from test_preflight import LOCAL_SAMPLING, configuration, local_llm


def image(name: str, digit: str) -> dict:
    return {"ref": f"example/{name}:fixed", "id": "sha256:" + digit * 64}


def runner_config() -> dict:
    return {
        "version": 1,
        "runs_root": "/srv/eval/runs",
        "secrets_env_file": "/srv/eval/eval.secrets.env",
        "images": {"engine_baseline": image("b", "1"), "engine_ours": image("o", "2"),
                   "engine_db": image("d", "3"), "meter": image("m", "4")},
        "target_image_id": "sha256:" + "5" * 64,
        "ports": {"engine_api_host": 18787, "engine_container": 8787, "target": 3000, "obs": 8081, "obs_host": 18081,
                  "llm_gateway": 8090, "api_front": 8091},
        "observer_path": "/api/records",
        "observer_format": "challenge_scoreboard",
        "engine": {"llm_provider": "openai", "llm_stream": True, "llm_policy_mode": "external"},
        "engine_db": {"user": "artex", "name": "artex"},
        "tmpfs": {"engine_db": "1g", "engine_data": "512m", "min_free": "16m"},
        "health": {"interval": "2s", "timeout": "2s", "retries": 30},
        "docker_timeout_seconds": 120,
        "host_listen_allowlist": [],
        "probe_timeout_ms": 2000,
        "runner_key_path": "/srv/eval-keys/runner.key",
        "run": {"compose_wait_seconds": 120, "down_timeout_seconds": 30, "ready_timeout_seconds": 120,
                "budget_poll_seconds": 5, "drain_timeout_seconds": 60, "snapshot_timeout_seconds": 30,
                "snapshot_max_bytes": 4194304, "llm_verify_timeout_seconds": 300, "llm_load_timeout_seconds": 300,
                "task_create_timeout_seconds": 20, "host_watch_interval_seconds": 2,
                "host_watch_max_gap_seconds": 15, "drain_grace_seconds": 30, "post_settle_seconds": 2},
        "llm_gateway": {"generation_path": "/api/v1/generation", "verify_attempts": 5, "verify_interval_seconds": 2,
                        "ca_bundle": "/etc/ssl/certs/ca-certificates.crt", "expected_provider_name": "Example"},
        "findings": {"vocabulary_path": "/srv/eval/vocabulary.json", "page_size": 100, "timeout_seconds": 30,
                     "max_page_bytes": 1048576, "max_total_bytes": 8388608, "max_pages": 100},
        "observer_readonly_ref": "verify:juice-shop-15.0.0:get-api-challenges",
        "e2": {"ours_mode": "none"},
        "state": None,
        "engine_rules": None,
        "task": {"description": "평가 대상 http://eval-meter:8080 웹앱의 접근 통제를 진단한다.\n로그인 계정은 엔진 설정을 쓴다.",
                 "goal": "다른 사용자의 자원에 접근되는지 확인한다", "timeout_seconds": 1200,
                 "plan_heartbeat_seconds": 600, "seed_first_intent": False, "coverage_enabled": True},
    }


class TestParseRunnerConfig(unittest.TestCase):
    def reject(self, payload, expected):
        with self.assertRaises(RunnerConfigError) as caught:
            parse_runner_config(payload)
        self.assertIn(expected, caught.exception.errors)

    def test_valid_config_parses(self):
        config = parse_runner_config(runner_config())
        self.assertEqual(config.images["engine_ours"].ref, "example/o:fixed")
        self.assertEqual(config.ports["obs_host"], 18081)
        self.assertEqual(config.docker_timeout_seconds, 120.0)

    def test_findings_limits_accept_the_collector_maximums(self):
        payload = runner_config()
        payload["findings"].update(page_size=200, max_page_bytes=8 * 1024 * 1024, max_total_bytes=8 * 1024 * 1024,
                                   max_pages=10_000)
        self.assertEqual(parse_runner_config(payload).findings["page_size"], 200)
        payload = runner_config()
        del payload["findings"]
        self.reject(payload, "config.findings: MISSING")
        payload = runner_config()
        del payload["findings"]["max_pages"]
        self.reject(payload, "config.findings.max_pages: MISSING")

    def test_observer_readonly_ref_is_a_required_plain_reference(self):
        for value in ("", " ref", "ref\n", "x" * 201, 7, None):
            with self.subTest(value=str(value)[:12]):
                self.reject(runner_config() | {"observer_readonly_ref": value}, "config.observer_readonly_ref: INVALID")
        payload = runner_config()
        del payload["observer_readonly_ref"]
        self.reject(payload, "config.observer_readonly_ref: MISSING")
        self.assertEqual(parse_runner_config(runner_config() | {"observer_readonly_ref": "x" * 200}).observer_readonly_ref,
                         "x" * 200)

    def test_task_takes_only_the_evaluation_fields(self):
        payload = runner_config()
        payload["task"].update(description="x" * 4000, timeout_seconds=0, plan_heartbeat_seconds=86400)
        self.assertEqual(parse_runner_config(payload).task["timeout_seconds"], 0)
        for extra in ("llm_profile_id", "llm_profile_ids", "source_task_ids", "company_ids", "intercept_rules", "name"):
            with self.subTest(extra=extra):
                payload = runner_config()
                payload["task"][extra] = []
                self.reject(payload, "config.task: UNKNOWN_FIELDS")
        payload = runner_config()
        del payload["task"]
        self.reject(payload, "config.task: MISSING")

    def test_missing_and_unknown_fields_rejected(self):
        payload = runner_config()
        del payload["observer_path"]
        self.reject(payload, "config.observer_path: MISSING")
        self.reject(runner_config() | {"extra": 1}, "config: UNKNOWN_FIELDS")
        payload = runner_config()
        payload["images"]["engine_ours"]["tag"] = "x"
        self.reject(payload, "config.images.engine_ours: UNKNOWN_FIELDS")
        payload = runner_config()
        del payload["images"]["meter"]
        self.reject(payload, "config.images.meter: MISSING")

    def test_version_must_be_exact_int(self):
        for version in (2, True, "1", 1.0):
            payload = runner_config() | {"version": version}
            self.reject(payload, "config.version: UNSUPPORTED")

    def test_paths_must_be_absolute_and_normal(self):
        for value in ("relative/runs", "/srv/../etc", "/srv/eval/", "/srv//eval", "", 5):
            self.reject(runner_config() | {"runs_root": value}, "config.runs_root: EXPECTED_ABSOLUTE_PATH")
            self.reject(runner_config() | {"secrets_env_file": value}, "config.secrets_env_file: EXPECTED_ABSOLUTE_PATH")

    def test_images_need_fixed_reference_and_content_id(self):
        for ref in ("example/o", "example/o:latest", "example/o:todo", "-x:1"):
            payload = runner_config()
            payload["images"]["engine_ours"]["ref"] = ref
            self.reject(payload, "config.images.engine_ours.ref: INVALID")
        for value in ("sha256:" + "A" * 64, "sha256:" + "a" * 63, "a" * 64, None):
            payload = runner_config()
            payload["images"]["meter"]["id"] = value
            self.reject(payload, "config.images.meter.id: INVALID")
            self.reject(runner_config() | {"target_image_id": value}, "config.target_image_id: INVALID")

    def test_ports(self):
        for value in (0, 65536, "8080", True, 80.0):
            payload = runner_config()
            payload["ports"]["obs"] = value
            self.reject(payload, "config.ports.obs: INVALID")
        payload = runner_config()
        payload["ports"]["obs_host"] = payload["ports"]["engine_api_host"]
        self.reject(payload, "config.ports: HOST_PORT_CONFLICT")

    def test_observer_format_is_explicit_and_closed(self):
        payload = runner_config()
        del payload["observer_format"]
        self.reject(payload, "config.observer_format: MISSING")
        for value in ("", "none", "OPAQUE", None, 1):
            self.reject(runner_config() | {"observer_format": value}, "config.observer_format: INVALID")
        for value in ("challenge_scoreboard", "opaque"):
            self.assertEqual(parse_runner_config(runner_config() | {"observer_format": value}).observer_format, value)

    def test_observer_path_follows_meter_rule(self):
        for value in ("api/x", "/a?b", "/a#b", "/a%2f", "/a\\b", "/a b", "//a", "/a/../b", "/a/./b", "/é", ""):
            self.reject(runner_config() | {"observer_path": value}, "config.observer_path: INVALID")
        parse_runner_config(runner_config() | {"observer_path": "/"})

    def test_engine_db_tmpfs_health_and_timeout(self):
        cases = [
            (("engine", "llm_provider"), "ollama", "config.engine.llm_provider: UNSUPPORTED"),
            (("engine", "llm_provider"), "anthropic", "config.engine.llm_provider: UNSUPPORTED"),
            (("llm_gateway", "generation_path"), "api/v1/generation", "config.llm_gateway.generation_path: INVALID"),
            (("llm_gateway", "verify_attempts"), 0, "config.llm_gateway.verify_attempts: INVALID"),
            (("llm_gateway", "verify_attempts"), 101, "config.llm_gateway.verify_attempts: INVALID"),
            (("llm_gateway", "verify_interval_seconds"), 601, "config.llm_gateway.verify_interval_seconds: INVALID"),
            (("llm_gateway", "verify_interval_seconds"), True, "config.llm_gateway.verify_interval_seconds: INVALID"),
            (("llm_gateway", "ca_bundle"), "certs/ca.crt", "config.llm_gateway.ca_bundle: EXPECTED_ABSOLUTE_PATH"),
            (("llm_gateway", "expected_provider_name"), " ", "config.llm_gateway.expected_provider_name: INVALID"),
            (("llm_gateway", "expected_provider_name"), "a\nb", "config.llm_gateway.expected_provider_name: INVALID"),
            (("llm_gateway", "expected_provider_name"), "x" * 101, "config.llm_gateway.expected_provider_name: INVALID"),
            (("llm_gateway", "expected_provider_name"), 7, "config.llm_gateway.expected_provider_name: INVALID"),
            (("run", "llm_load_timeout_seconds"), 0, "config.run.llm_load_timeout_seconds: INVALID"),
            (("run", "llm_verify_timeout_seconds"), 0, "config.run.llm_verify_timeout_seconds: INVALID"),
            (("findings", "vocabulary_path"), "vocabulary.json", "config.findings.vocabulary_path: EXPECTED_ABSOLUTE_PATH"),
            (("findings", "page_size"), 201, "config.findings.page_size: INVALID"),
            (("findings", "page_size"), 0, "config.findings.page_size: INVALID"),
            (("findings", "max_total_bytes"), 8 * 1024 * 1024 + 1, "config.findings.max_total_bytes: INVALID"),
            (("findings", "max_page_bytes"), 8 * 1024 * 1024 + 1, "config.findings.max_page_bytes: INVALID"),
            (("findings", "max_page_bytes"), 0, "config.findings.max_page_bytes: INVALID"),
            (("findings", "max_pages"), 10_001, "config.findings.max_pages: INVALID"),
            (("task", "description"), "   ", "config.task.description: INVALID"),
            (("task", "description"), "a\tb", "config.task.description: INVALID"),
            (("task", "goal"), "x" * 4001, "config.task.goal: INVALID"),
            (("task", "goal"), "a\x7fb", "config.task.goal: INVALID"),
            (("task", "goal"), 7, "config.task.goal: INVALID"),
            (("task", "timeout_seconds"), -1, "config.task.timeout_seconds: INVALID"),
            (("task", "timeout_seconds"), 86401, "config.task.timeout_seconds: INVALID"),
            (("task", "plan_heartbeat_seconds"), True, "config.task.plan_heartbeat_seconds: INVALID"),
            (("task", "seed_first_intent"), "false", "config.task.seed_first_intent: EXPECTED_BOOL"),
            (("task", "coverage_enabled"), 1, "config.task.coverage_enabled: EXPECTED_BOOL"),
            (("run", "task_create_timeout_seconds"), 0, "config.run.task_create_timeout_seconds: INVALID"),
            (("run", "host_watch_interval_seconds"), 0, "config.run.host_watch_interval_seconds: INVALID"),
            (("run", "host_watch_max_gap_seconds"), 2, "config.run.host_watch_max_gap_seconds: NOT_ABOVE_INTERVAL"),
            (("run", "host_watch_max_gap_seconds"), 1, "config.run.host_watch_max_gap_seconds: NOT_ABOVE_INTERVAL"),
            (("findings", "max_pages"), True, "config.findings.max_pages: INVALID"),
            (("findings", "timeout_seconds"), 3601, "config.findings.timeout_seconds: INVALID"),
            (("ports", "llm_gateway"), 0, "config.ports.llm_gateway: INVALID"),
            (("ports", "api_front"), 70000, "config.ports.api_front: INVALID"),
            (("engine", "llm_stream"), "true", "config.engine.llm_stream: EXPECTED_BOOL"),
            (("engine", "llm_policy_mode"), "local-only", "config.engine.llm_policy_mode: UNSUPPORTED"),
            (("engine", "llm_policy_mode"), " external", "config.engine.llm_policy_mode: UNSUPPORTED"),
            (("engine", "llm_policy_mode"), "", "config.engine.llm_policy_mode: UNSUPPORTED"),
            (("engine_db", "user"), "Artex", "config.engine_db.user: INVALID"),
            (("engine_db", "name"), "a;b", "config.engine_db.name: INVALID"),
            (("tmpfs", "engine_db"), "0", "config.tmpfs.engine_db: INVALID"),
            (("tmpfs", "engine_data"), "1G", "config.tmpfs.engine_data: INVALID"),
            (("tmpfs", "min_free"), "0", "config.tmpfs.min_free: INVALID"),
            (("tmpfs", "min_free"), 16, "config.tmpfs.min_free: INVALID"),
            (("tmpfs", "min_free"), "512m", "config.tmpfs.min_free: NOT_BELOW_SIZE"),  # engine_data와 같음
            (("tmpfs", "min_free"), "2g", "config.tmpfs.min_free: NOT_BELOW_SIZE"),
            (("health", "interval"), "2", "config.health.interval: INVALID"),
            (("health", "timeout"), "0s", "config.health.timeout: INVALID"),
            (("health", "retries"), 0, "config.health.retries: INVALID"),
        ]
        for (section, key), value, expected in cases:
            payload = runner_config()
            payload[section][key] = value
            self.reject(payload, expected)
        for value in (0, -1, 3601, True, "60", float("nan")):
            self.reject(runner_config() | {"docker_timeout_seconds": value}, "config.docker_timeout_seconds: INVALID")
        for value in (0, 60_001, 1.5, True, "2000"):
            self.reject(runner_config() | {"probe_timeout_ms": value}, "config.probe_timeout_ms: INVALID")
        self.assertEqual(parse_runner_config(runner_config() | {"probe_timeout_ms": 60_000}).probe_timeout_ms, 60_000)

    def test_host_listen_allowlist_is_specific_and_justified(self):
        entry = {"address": "10.255.255.254", "port": 53, "reason": "WSL DNS 터널링(lo의 /32 주소)"}
        config = parse_runner_config(runner_config() | {"host_listen_allowlist": [entry]})
        self.assertEqual([(e.address, e.port, e.reason) for e in config.host_listen_allowlist],
                         [("10.255.255.254", 53, entry["reason"])])
        cases = [
            (entry | {"address": "0.0.0.0"}, "[0].address: INVALID"),
            (entry | {"address": "::"}, "[0].address: INVALID"),
            (entry | {"address": "127.0.0.1"}, "[0].address: INVALID"),
            (entry | {"address": "::ffff:10.0.0.1"}, "[0].address: INVALID"),
            (entry | {"address": "::ffff:a00:1"}, "[0].address: INVALID"),
            (entry | {"address": "fd00:0::1"}, "[0].address: INVALID"),
            (entry | {"address": "host.local"}, "[0].address: INVALID"),
            (entry | {"port": 0}, "[0].port: INVALID"),
            (entry | {"port": True}, "[0].port: INVALID"),
            (entry | {"reason": " "}, "[0].reason: INVALID"),
            (entry | {"reason": "a\nb"}, "[0].reason: INVALID"),
            (entry | {"reason": "x" * 201}, "[0].reason: INVALID"),
            ({"address": "10.0.0.1", "port": 53}, "[0].reason: MISSING"),
            (entry | {"why": "x"}, "[0]: UNKNOWN_FIELDS"),
        ]
        for item, expected in cases:
            self.reject(runner_config() | {"host_listen_allowlist": [item]}, "config.host_listen_allowlist" + expected)
        self.reject(runner_config() | {"host_listen_allowlist": [entry, dict(entry)]},
                    "config.host_listen_allowlist[1]: DUPLICATE")
        self.reject(runner_config() | {"host_listen_allowlist": {}}, "config.host_listen_allowlist: INVALID")
        self.reject(runner_config() | {"host_listen_allowlist": [entry | {"port": p} for p in range(1, 34)]},
                    "config.host_listen_allowlist: INVALID")

    def test_runner_key_outside_runs_root_and_run_values(self):
        for value in ("/srv/eval/runs/key", "/srv/eval/runs", "/srv/eval/runs/a/b.key"):
            self.reject(runner_config() | {"runner_key_path": value}, "config.runner_key_path: INSIDE_RUNS_ROOT")
        parse_runner_config(runner_config() | {"runner_key_path": "/srv/eval/runs-keys/k"})
        self.reject(runner_config() | {"runner_key_path": "keys/k"}, "config.runner_key_path: EXPECTED_ABSOLUTE_PATH")
        for name, value in (("drain_timeout_seconds", 0), ("budget_poll_seconds", 3601), ("snapshot_max_bytes", True),
                            ("snapshot_max_bytes", 16 * 1024 * 1024 + 1), ("ready_timeout_seconds", 1.5)):
            payload = runner_config()
            payload["run"][name] = value
            self.reject(payload, f"config.run.{name}: INVALID")
        for name, value in (("drain_grace_seconds", 0), ("drain_grace_seconds", 601), ("post_settle_seconds", 0),
                            ("post_settle_seconds", True)):
            payload = runner_config()
            payload["run"][name] = value
            self.reject(payload, f"config.run.{name}: INVALID")
        payload = runner_config()
        del payload["run"]["down_timeout_seconds"]
        self.reject(payload, "config.run.down_timeout_seconds: MISSING")

    def test_drain_grace_ends_before_the_runner_gives_up(self):
        # 계측기가 끊기 전에 러너가 drain_timeout으로 끝내면 유예는 쓸모가 없다. 같은 값도 경합이라 거부한다.
        for grace in (60, 61):
            payload = runner_config()
            payload["run"]["drain_grace_seconds"] = grace
            self.reject(payload, "config.run.drain_grace_seconds: NOT_BELOW_DRAIN_TIMEOUT")
        payload = runner_config()
        payload["run"]["drain_grace_seconds"] = 59
        parse_runner_config(payload)

    def test_drain_grace_limit_matches_the_go_meter(self):
        text = (Path(__file__).resolve().parents[3] / "cmd/eval-meter/main.go").read_text(encoding="utf-8")
        match = re.search(r"maxDrainGrace = (\d+) \* time\.Minute", text)
        self.assertIsNotNone(match)
        self.assertEqual(int(match.group(1)) * 60, MAX_DRAIN_GRACE_SECONDS)
        compose = (Path(__file__).resolve().parents[3] / "deploy/eval/compose.eval.yaml").read_text(encoding="utf-8")
        self.assertIn("- -drain-grace=${EVAL_METER_DRAIN_GRACE:?}", compose)

    def test_load_rejects_duplicate_key_and_nonfinite(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "runner.json")
            path.write_text('{"version": 1, "version": 1}', encoding="utf-8")
            with self.assertRaises(RunnerConfigError) as caught:
                load_runner_config(path)
            self.assertEqual(caught.exception.errors, ["config: DUPLICATE_KEY"])
            path.write_text(json.dumps(runner_config()).replace("120", "NaN"), encoding="utf-8")
            with self.assertRaises(RunnerConfigError) as caught:
                load_runner_config(path)
            self.assertEqual(caught.exception.errors, ["config: NONFINITE_NUMBER"])
            path.write_text(json.dumps(runner_config()), encoding="utf-8")
            self.assertEqual(load_runner_config(path).observer_path, "/api/records")


STATE = {"sqlite_path": "/app/data/target.sqlite", "signal_tables": ["Challenges"], "max_copy_bytes": 67108864,
         "runtime_roots": ["/app/ftp", "/app/uploads"],
         "memory": {"paths": ["/api/a/1", "/api/a/2"], "count_field": "data", "max_body_bytes": 1048576,
                    "timeout_ms": 5000}}


class TestStateDiff(unittest.TestCase):
    def test_state_block_is_required_null_or_closed(self):
        self.assertIsNone(parse_runner_config(runner_config()).state)
        self.assertEqual(parse_runner_config(runner_config() | {"state": STATE}).state,
                         {"sqlite_path": "/app/data/target.sqlite", "signal_tables": ("Challenges",),
                          "max_copy_bytes": 67108864, "runtime_roots": ("/app/ftp", "/app/uploads"),
                          "memory": {"paths": ("/api/a/1", "/api/a/2"), "count_field": "data",
                                     "max_body_bytes": 1048576, "timeout_ms": 5000}})
        self.assertIsNone(parse_runner_config(runner_config() | {"state": dict(STATE, memory=None)}).state["memory"])
        self.assertEqual(parse_runner_config(runner_config() | {"state": dict(STATE, runtime_roots=[])}).state[
            "runtime_roots"], ())
        self.assertEqual(parse_runner_config(runner_config() | {"state": dict(STATE, signal_tables=[])}).state[
            "signal_tables"], ())
        for payload, code in (
                ({k: v for k, v in runner_config().items() if k != "state"}, "config.state: MISSING"),
                (runner_config() | {"state": "on"}, "config.state: EXPECTED_OBJECT"),
                (runner_config() | {"state": dict(STATE, extra=1)}, "config.state: UNKNOWN_FIELDS"),
                (runner_config() | {"state": {k: v for k, v in STATE.items() if k != "max_copy_bytes"}},
                 "config.state.max_copy_bytes: MISSING"),
                (runner_config() | {"state": dict(STATE, sqlite_path="data/target.sqlite")},
                 "config.state.sqlite_path: INVALID"),
                (runner_config() | {"state": dict(STATE, sqlite_path="/app/../etc/target.sqlite")},
                 "config.state.sqlite_path: INVALID"),
                (runner_config() | {"state": dict(STATE, signal_tables="Challenges")}, "config.state.signal_tables: INVALID"),
                (runner_config() | {"state": dict(STATE, signal_tables=["A", "A"])}, "config.state.signal_tables: INVALID"),
                (runner_config() | {"state": dict(STATE, signal_tables=["x y"])}, "config.state.signal_tables: INVALID"),
                (runner_config() | {"state": dict(STATE, signal_tables=[1])}, "config.state.signal_tables: INVALID"),
                (runner_config() | {"state": dict(STATE, signal_tables=[f"T{i}" for i in range(33)])},
                 "config.state.signal_tables: INVALID"),
                (runner_config() | {"state": dict(STATE, max_copy_bytes=0)}, "config.state.max_copy_bytes: INVALID"),
                (runner_config() | {"state": dict(STATE, max_copy_bytes=(1 << 30) + 1)},
                 "config.state.max_copy_bytes: INVALID"),
                (runner_config() | {"state": dict(STATE, max_copy_bytes=True)}, "config.state.max_copy_bytes: INVALID"),
                (runner_config() | {"state": {k: v for k, v in STATE.items() if k != "runtime_roots"}},
                 "config.state.runtime_roots: MISSING"),
                (runner_config() | {"state": dict(STATE, runtime_roots="/app/ftp")}, "config.state.runtime_roots: INVALID"),
                (runner_config() | {"state": dict(STATE, runtime_roots=["app/ftp"])}, "config.state.runtime_roots: INVALID"),
                (runner_config() | {"state": dict(STATE, runtime_roots=["/app/../ftp"])},
                 "config.state.runtime_roots: INVALID"),
                (runner_config() | {"state": dict(STATE, runtime_roots=["/app/ftp", "/app/ftp"])},
                 "config.state.runtime_roots: INVALID"),
                (runner_config() | {"state": dict(STATE, runtime_roots=["/app/ftp", "/app/ftp/sub"])},
                 "config.state.runtime_roots: INVALID"),
                (runner_config() | {"state": dict(STATE, runtime_roots=[f"/r{i}" for i in range(17)])},
                 "config.state.runtime_roots: INVALID"),
                (runner_config() | {"state": {k: v for k, v in STATE.items() if k != "memory"}},
                 "config.state.memory: MISSING"),
                (runner_config() | {"state": dict(STATE, memory="on")}, "config.state.memory: EXPECTED_OBJECT"),
                (runner_config() | {"state": dict(STATE, memory=dict(STATE["memory"], extra=1))},
                 "config.state.memory: UNKNOWN_FIELDS"),
                (runner_config() | {"state": dict(STATE, memory={k: v for k, v in STATE["memory"].items() if k != "paths"})},
                 "config.state.memory.paths: MISSING"),
                *((runner_config() | {"state": dict(STATE, memory=dict(STATE["memory"], paths=paths))},
                   "config.state.memory.paths: INVALID")
                  for paths in ([], "/a", ["a"], ["/a?x=1"], ["/a/../b"], ["/a", "/a"], [1],
                                [f"/p{i}" for i in range(257)], ["/" + "x" * 33000])),
                *((runner_config() | {"state": dict(STATE, memory=dict(STATE["memory"], count_field=field))},
                   "config.state.memory.count_field: INVALID") for field in ("", "a b", 1)),
                *((runner_config() | {"state": dict(STATE, memory=dict(STATE["memory"], max_body_bytes=value))},
                   "config.state.memory.max_body_bytes: INVALID") for value in (0, (16 << 20) + 1, True)),
                *((runner_config() | {"state": dict(STATE, memory=dict(STATE["memory"], timeout_ms=value))},
                   "config.state.memory.timeout_ms: INVALID") for value in (0, 600001, True))):
            with self.subTest(code=code, payload=payload.get("state")), self.assertRaises(RunnerConfigError) as caught:
                parse_runner_config(payload)
            self.assertIn(code, caught.exception.errors)


class TestE2Mode(unittest.TestCase):
    def test_e2_block_is_required_and_closed(self):
        for payload, code in (
                ({k: v for k, v in runner_config().items() if k != "e2"}, "config.e2: MISSING"),
                (runner_config() | {"e2": {"ours_mode": "product"}}, "config.e2.ours_mode: UNSUPPORTED"),
                (runner_config() | {"e2": {"ours_mode": ""}}, "config.e2.ours_mode: UNSUPPORTED"),
                (runner_config() | {"e2": {}}, "config.e2.ours_mode: MISSING"),
                (runner_config() | {"e2": {"ours_mode": "stub", "port": 1}}, "config.e2: UNKNOWN_FIELDS"),
                (runner_config() | {"e2": "stub"}, "config.e2: EXPECTED_OBJECT")):
            with self.subTest(code=code), self.assertRaises(RunnerConfigError) as caught:
                parse_runner_config(payload)
            self.assertIn(code, caught.exception.errors)

    def test_stub_adds_the_overlay_to_the_ours_side_only(self):
        config = parse_runner_config(runner_config() | {"e2": {"ours_mode": "stub"}})
        ours = plan_run(config, configuration(), "ours", "ours-1")
        baseline = plan_run(config, configuration(), "baseline", "baseline-1")
        self.assertEqual((ours.e2_mode, ours.compose_files[-1]), ("stub", COMPOSE_E2_STUB))
        self.assertEqual(baseline.e2_mode, "none")
        self.assertNotIn(COMPOSE_E2_STUB, baseline.compose_files)
        # E²가 있어도 엔진이 보내는 이름·작업 문구는 그대로다(작업 요청 해시가 baseline과 같다).
        self.assertEqual(ours.meter_port, baseline.meter_port)
        none = plan_run(parse_runner_config(runner_config()), configuration(), "ours", "ours-2")
        self.assertEqual((none.e2_mode, none.compose_files), ("none", (COMPOSE_COMMON, COMPOSE_LLM_API, COMPOSE_EVAL)))

    def test_overlay_names_match_the_runner_constants(self):
        text = _yaml_without_comments(COMPOSE_E2_STUB.read_text(encoding="utf-8"))
        self.assertEqual(compose_services(COMPOSE_E2_STUB), {E2_SERVICE, METER_SERVICE})
        self.assertEqual(compose_services(COMPOSE_E2_PRODUCT), {E2_SERVICE, METER_SERVICE})
        self.assertIn(f"aliases: [{METER_SERVICE}]", text)  # 엔진은 계측기 이름으로 E²에 닿는다
        self.assertIn(f"aliases: [{METER_UPSTREAM_ALIAS}]", text)
        self.assertIn(f"-upstream=http://{METER_UPSTREAM_ALIAS}:${{EVAL_METER_PORT:?}}", text)
        self.assertIn("-role=e2-stub", text)
        self.assertIn("networks: !override", text)  # 계측기를 엔진 망에서 빼는 줄
        # E²는 따로 둔 증거 디렉터리에만 쓴다. 계측기 증거(meter/) 경로를 마운트하지 않는다.
        self.assertIn("- ${EVAL_E2_EVIDENCE_DIR:?}:/evidence", text)
        self.assertNotIn("EVAL_EVIDENCE_DIR", text)


class TestPlanRun(unittest.TestCase):
    def setUp(self):
        self.config = parse_runner_config(runner_config())

    def test_gateway_sampling_comes_from_the_declaration(self):
        # 엔진은 샘플링을 보내지 않으므로 게이트웨이가 넣을 일곱 값을 선언(temperature·sampling·seed)에서 만든다.
        preflight = configuration()
        plan = plan_run(self.config, preflight, "ours", "ours-1")
        declared = preflight["ours"]
        self.assertEqual(json.loads(plan.compose_env["EVAL_LLM_SAMPLING"]),
                         {"temperature": declared["temperature"], **declared["sampling"], "seed": declared["seed"]})
        preflight["baseline"]["temperature"] = preflight["ours"]["temperature"] = 2.5
        with self.assertRaises(RunnerConfigError) as caught:
            plan_run(self.config, preflight, "ours", "ours-1")
        self.assertEqual(caught.exception.errors, ["preflight.ours.temperature: ABOVE_GATEWAY_LIMIT"])
        go = (REPO_ROOT / "cmd/eval-meter/sampling.go").read_text(encoding="utf-8")
        self.assertIn(f"maxTemperature       = {MAX_GATEWAY_TEMPERATURE}\n", go)
        compose = (REPO_ROOT / "deploy/engine-isolated/compose.llm-api.yaml").read_text(encoding="utf-8")
        self.assertIn("- -sampling=${EVAL_LLM_SAMPLING:?}", compose)

    def test_drain_policy_must_equal_the_declaration(self):
        # 봉인 뒤 유예·정착 대기는 환경 지문에 들어간 비교 조건이다(#153 리뷰). 설정이 선언과 다르면 시작하지 않는다.
        plan_run(self.config, configuration(), "ours", "ours-1")
        for name, value in (("drain_grace_seconds", 29), ("post_settle_seconds", 3)):
            with self.subTest(name=name):
                payload = runner_config()
                payload["run"][name] = value
                with self.assertRaises(RunnerConfigError) as caught:
                    plan_run(parse_runner_config(payload), configuration(), "ours", "ours-1")
                self.assertEqual(caught.exception.errors, ["preflight.ours.run_drain: CONFIG_MISMATCH"])
        # 한쪽 선언만 다르면 사전검사가 먼저 비교 조건 불일치로 거부한다.
        preflight = configuration()
        preflight["baseline"]["run_drain"] = {"grace_seconds": 31, "settle_seconds": 2}
        with self.assertRaises(RunnerConfigError) as caught:
            plan_run(self.config, preflight, "ours", "ours-1")
        self.assertTrue(any("run_drain" in error for error in caught.exception.errors), caught.exception.errors)

    def test_api_plan_takes_llm_and_target_from_preflight(self):
        preflight = configuration()
        plan = plan_run(self.config, preflight, "ours", "ours-20261102t000000z-00000000")
        self.assertEqual(plan.project, "eval-ours-20261102t000000z-00000000")
        self.assertEqual(plan.compose_files, (COMPOSE_COMMON, COMPOSE_LLM_API, COMPOSE_EVAL))
        self.assertEqual(plan.source_sha, "2" * 40)
        self.assertEqual(plan.engine_image.ref, "example/o:fixed")
        self.assertEqual(plan.target_image.ref, preflight["pins"]["target_image"])
        self.assertEqual(plan.compose_env["ARTEX_LLM_MODEL"], preflight["ours"]["llm"]["model"])
        # 엔진은 선언의 엔드포인트 대신 게이트웨이에 닿는다. 경로(/api/v1)는 그대로 붙인다.
        self.assertEqual(plan.compose_env["ARTEX_LLM_BASE_URL"], "http://eval-llm-gw:8090/api/v1")
        self.assertEqual(plan.compose_env["EVAL_LLM_UPSTREAM"], "https://llm-api.example")
        self.assertEqual(plan.compose_env["EVAL_LLM_CHAT_PATH"], "/api/v1/chat/completions")
        self.assertEqual(plan.compose_env["EVAL_LLM_PROVIDER"], preflight["ours"]["llm"]["provider"])
        self.assertEqual((plan.compose_env["EVAL_LLM_GENERATION_PATH"], plan.compose_env["EVAL_LLM_VERIFY_ATTEMPTS"],
                          plan.compose_env["EVAL_LLM_VERIFY_INTERVAL"], plan.compose_env["EVAL_CA_BUNDLE"]),
                         ("/api/v1/generation", "5", "2s", "/etc/ssl/certs/ca-certificates.crt"))
        self.assertEqual((plan.compose_env["EVAL_LLM_GW_PORT"], plan.compose_env["EVAL_API_FRONT_PORT"]), ("8090", "8091"))
        self.assertEqual(plan.compose_env["EVAL_METER_PORT"], "8080")
        self.assertEqual(plan.compose_env["EVAL_METER_DRAIN_GRACE"], "30s")
        self.assertEqual(plan.compose_env["ARTEX_LLM_STREAM"], "true")
        no_stream = runner_config()
        no_stream["engine"]["llm_stream"] = False
        self.assertEqual(plan_run(parse_runner_config(no_stream), preflight, "ours", "o").compose_env["ARTEX_LLM_STREAM"],
                         "false")
        self.assertNotIn("EVAL_LLM_HOST_IP", plan.compose_env)
        self.assertEqual(plan_run(self.config, preflight, "baseline", "b").engine_image.ref, "example/b:fixed")

    def test_local_plan_adds_overlay_and_fixed_ip(self):
        preflight = configuration()
        for side in ("baseline", "ours"):
            preflight[side]["llm"] = local_llm()
            preflight[side]["sampling"] = dict(LOCAL_SAMPLING)
        plan = plan_run(self.config, preflight, "baseline", "b")
        self.assertEqual(plan.compose_files, (COMPOSE_COMMON, COMPOSE_LOCAL_LLM, COMPOSE_EVAL))
        self.assertEqual(plan.compose_env["EVAL_LLM_HOSTNAME"], "llm-host")
        self.assertEqual(plan.compose_env["EVAL_LLM_HOST_IP"], "172.17.0.1")
        self.assertEqual(plan.compose_env["EVAL_LLM_UPSTREAM"], "http://llm-host:11434")
        self.assertEqual(plan.compose_env["EVAL_LLM_CHAT_PATH"], "/v1/chat/completions")
        self.assertEqual(plan.compose_env["ARTEX_LLM_BASE_URL"], "http://eval-llm-gw:8090/v1")
        for name in ("EVAL_LLM_PROVIDER", "EVAL_LLM_GENERATION_PATH", "EVAL_LLM_VERIFY_ATTEMPTS", "EVAL_CA_BUNDLE"):
            self.assertNotIn(name, plan.compose_env)

    def test_gateway_paths_handle_root_and_trailing_slash(self):
        for base_url, chat, engine in (("https://llm-api.example", "/chat/completions", "http://eval-llm-gw:8090"),
                                       ("https://llm-api.example/api/v1/", "/api/v1/chat/completions",
                                        "http://eval-llm-gw:8090/api/v1")):
            preflight = configuration()
            for side in ("baseline", "ours"):
                preflight[side]["llm"]["base_url"] = base_url
            plan = plan_run(self.config, preflight, "ours", "o")
            with self.subTest(base_url=base_url):
                self.assertEqual(plan.compose_env["EVAL_LLM_CHAT_PATH"], chat)
                self.assertEqual(plan.compose_env["ARTEX_LLM_BASE_URL"], engine)

    def test_compose_env_never_carries_secret_names(self):
        plan = plan_run(self.config, configuration(), "ours", "o")
        for name in ("EVAL_ENGINE_DB_PASSWORD", "EVAL_ENGINE_ADMIN_PASSWORD", "EVAL_LLM_API_KEY", "OPENAI_API_KEY",
                     "ANTHROPIC_API_KEY"):
            self.assertNotIn(name, plan.compose_env)
        args = compose_base_args(plan)
        self.assertEqual(args[:5], ["compose", "-p", plan.project, "--env-file", "/srv/eval/eval.secrets.env"])

    def test_invalid_preflight_or_side_rejected(self):
        preflight = configuration()
        preflight["ours"]["seed"] = 7
        with self.assertRaises(RunnerConfigError) as caught:
            plan_run(self.config, preflight, "ours", "o")
        self.assertIn("preflight: comparison.seed: MISMATCH", caught.exception.errors)
        with self.assertRaises(RunnerConfigError):
            plan_run(self.config, configuration(), "both", "o")

    def test_target_origin_must_name_the_meter_service(self):
        preflight = configuration()
        for side in ("baseline", "ours"):
            preflight[side]["target_origin"] = "http://target:8080"
        with self.assertRaises(RunnerConfigError) as caught:
            plan_run(self.config, preflight, "ours", "o")
        self.assertEqual(caught.exception.errors, ["preflight.ours.target_origin: NOT_EVAL_METER"])

    def test_plan_does_not_mutate_preflight(self):
        preflight = configuration()
        before = copy.deepcopy(preflight)
        plan_run(self.config, preflight, "ours", "o")
        self.assertEqual(preflight, before)


class TestRunIdentity(unittest.TestCase):
    def test_run_id_is_compose_safe_and_unique(self):
        moment = datetime(2026, 11, 2, 1, 2, 3, tzinfo=timezone.utc)
        first, second = new_run_id("ours", moment), new_run_id("ours", moment)
        self.assertRegex(first, r"^ours-20261102t010203z-[0-9a-f]{8}$")
        self.assertNotEqual(first, second)
        with self.assertRaises(RunnerConfigError):
            new_run_id("both", moment)


@unittest.skipUnless(hasattr(os, "O_NOFOLLOW") and hasattr(os, "geteuid"), "POSIX 전용")
class TestRunDirectory(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name, "runs")
        self.root.mkdir(mode=0o700)
        os.chmod(self.root, 0o700)
        self.old_umask = os.umask(0o022)

    def tearDown(self):
        os.umask(self.old_umask)
        self.tmp.cleanup()

    def test_creates_private_run_and_meter_directories(self):
        directory = create_run_directory(self.root, "ours-1")
        for path in (directory.run, directory.meter, directory.llm, directory.e2, directory.state):
            info = os.lstat(path)
            self.assertTrue(stat.S_ISDIR(info.st_mode))
            self.assertEqual(stat.S_IMODE(info.st_mode), 0o700)
            self.assertEqual(info.st_uid, os.geteuid())
        self.assertEqual(directory.meter.parent, directory.run)
        self.assertNotEqual(directory.e2, directory.meter)  # E² 증거는 계측기 증거와 다른 bind 디렉터리

    def test_mode_is_exact_even_when_umask_strips_owner_bits(self):
        os.umask(0o277)
        directory = create_run_directory(self.root, "ours-1")
        for path in (directory.run, directory.meter, directory.llm, directory.e2, directory.state):
            self.assertEqual(stat.S_IMODE(os.lstat(path).st_mode), 0o700)

    def test_existing_run_directory_rejected(self):
        create_run_directory(self.root, "ours-1")
        with self.assertRaises(RunnerConfigError) as caught:
            create_run_directory(self.root, "ours-1")
        self.assertEqual(caught.exception.errors, ["run_directory: EXISTS"])

    def test_root_must_be_exactly_private_and_not_a_symlink(self):
        os.chmod(self.root, 0o750)
        with self.assertRaises(RunnerConfigError) as caught:
            create_run_directory(self.root, "ours-1")
        self.assertEqual(caught.exception.errors, ["runs_root: NOT_PRIVATE"])
        os.chmod(self.root, 0o700)
        link = Path(self.tmp.name, "link")
        link.symlink_to(self.root)
        with self.assertRaises(RunnerConfigError) as caught:
            create_run_directory(link, "ours-1")
        self.assertEqual(caught.exception.errors, ["runs_root: UNAVAILABLE"])

    def test_run_id_shape_checked_before_touching_disk(self):
        for run_id in ("../x", "Ours", "", "a/b", "-a"):
            with self.assertRaises(RunnerConfigError):
                create_run_directory(self.root, run_id)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_compose_environment_adds_meter_dir_and_runner_ids(self):
        plan = plan_run(parse_runner_config(runner_config()), configuration(), "ours", "ours-1")
        directory = create_run_directory(self.root, "ours-1")
        env = compose_environment(plan, directory)
        self.assertEqual(env["EVAL_EVIDENCE_DIR"], str(directory.meter))
        self.assertEqual(env["EVAL_LLM_EVIDENCE_DIR"], str(directory.llm))
        self.assertNotEqual(directory.llm, directory.meter)
        self.assertEqual(env["EVAL_RUNNER_UID"], str(os.geteuid()))
        self.assertEqual(env["EVAL_RUNNER_GID"], str(os.getegid()))
        self.assertNotIn("EVAL_EVIDENCE_DIR", plan.compose_env)


def _yaml_without_comments(text: str) -> str:
    return "\n".join(re.sub(r"(^|\s)#.*$", "", line) for line in text.splitlines())


def compose_variables(paths) -> set[str]:
    """compose 보간 대상 ${VAR} 이름. $$는 compose의 리터럴 $ 이스케이프라 제외한다."""
    names = set()
    for path in paths:
        text = _yaml_without_comments(Path(path).read_text(encoding="utf-8")).replace("$$", "")
        bare = re.findall(r"\$(?!\{)", text)
        if bare:
            raise AssertionError(f"{Path(path).name}: 중괄호 없는 $VAR 참조")
        names |= set(re.findall(r"\$\{([A-Za-z_][A-Za-z0-9_]*)", text))
    return names


def compose_services(path) -> set[str]:
    services, inside = set(), False
    for line in _yaml_without_comments(Path(path).read_text(encoding="utf-8")).splitlines():
        if not line.strip():
            continue
        if not line.startswith(" "):
            inside = line.rstrip() == "services:"
            continue
        match = re.fullmatch(r"  ([A-Za-z0-9_.-]+):\s*", line)
        if inside and match:
            services.add(match.group(1))
    return services


@unittest.skipUnless(hasattr(os, "O_NOFOLLOW") and hasattr(os, "geteuid"), "POSIX 전용")
class TestComposeContract(unittest.TestCase):
    """compose가 보간하는 이름 = 러너가 넘기는 이름 ∪ 비밀 이름. 그래야 비밀 파일에 비밀 아닌
    키가 섞여도 셸 환경(러너 값)이 항상 이긴다. 러너는 비밀 파일을 읽지 않는다."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name, "runs")
        self.root.mkdir(mode=0o700)
        os.chmod(self.root, 0o700)

    def tearDown(self):
        self.tmp.cleanup()

    def test_gateway_sampling_is_declared_values_for_each_backend(self):
        config = parse_runner_config(runner_config())
        api = plan_run(config, configuration(), "ours", "ours-api")
        self.assertEqual(json.loads(api.compose_env["EVAL_LLM_SAMPLING"]),
                         {"temperature": configuration()["ours"]["temperature"], **configuration()["ours"]["sampling"],
                          "seed": configuration()["ours"]["seed"]})
        preflight = configuration()
        for side in ("baseline", "ours"):
            preflight[side]["llm"] = local_llm()
            preflight[side]["sampling"] = dict(LOCAL_SAMPLING)
        local = plan_run(config, preflight, "ours", "ours-local")
        self.assertEqual(json.loads(local.compose_env["EVAL_LLM_SAMPLING"]),
                         {"temperature": preflight["ours"]["temperature"], **LOCAL_SAMPLING,
                          "seed": preflight["ours"]["seed"]})

    def test_compose_variables_equal_runner_env_plus_secrets_per_backend(self):
        config = parse_runner_config(runner_config())
        for backend, llm in (("api", None), ("local", local_llm())):
            preflight = configuration()
            if llm:
                for side in ("baseline", "ours"):
                    preflight[side]["llm"] = llm
                    preflight[side]["sampling"] = dict(LOCAL_SAMPLING)
            plan = plan_run(config, preflight, "ours", f"ours-{backend}")
            env = compose_environment(plan, create_run_directory(self.root, f"ours-{backend}"))
            stub = plan_run(parse_runner_config(runner_config() | {"e2": {"ours_mode": "stub"}}), preflight, "ours",
                            f"stub-{backend}")
            stub_env = compose_environment(stub, create_run_directory(self.root, f"stub-{backend}"))
            with self.subTest(backend=backend, e2="stub"):
                self.assertEqual(compose_variables(stub.compose_files), set(stub_env) | secret_env_names(backend))
            with self.subTest(backend=backend):
                self.assertEqual(set(env) & SECRET_ENV_NAMES, set())
                self.assertEqual(compose_variables(plan.compose_files), set(env) | secret_env_names(backend))

    def test_tmpfs_sizes_and_lower_bound(self):
        self.assertEqual([size_bytes(v) for v in ("16", "16k", "16m", "1g")], [16, 16 * 1024, 16 * 1024 ** 2, 1024 ** 3])
        for bad in ("0", "1G", "1.5m", "", None, 16):
            with self.subTest(bad=bad), self.assertRaises(RunnerConfigError):
                size_bytes(bad)
        self.assertEqual(parse_runner_config(runner_config()).tmpfs_min_free_bytes, 16 * 1024 ** 2)
        payload = runner_config()
        del payload["tmpfs"]["min_free"]
        with self.assertRaises(RunnerConfigError) as caught:
            parse_runner_config(payload)
        self.assertIn("config.tmpfs.min_free: MISSING", caught.exception.errors)

    def test_tmpfs_mount_paths_match_the_eval_compose(self):
        # 러너가 df로 재는 경로가 compose가 tmpfs로 덮는 경로와 다르면 이미지 볼륨·디스크를 재게 된다.
        text = _yaml_without_comments(COMPOSE_EVAL.read_text(encoding="utf-8"))
        for path, size_var in ((ENGINE_DB_DATA_PATH, "EVAL_ENGINE_DB_TMPFS_SIZE"),
                               (ENGINE_DATA_PATH, "EVAL_ENGINE_DATA_TMPFS_SIZE")):
            with self.subTest(path=path):
                self.assertIn(f"- {path}:size=${{{size_var}:?}}", text)

    def test_every_compose_file_is_covered(self):
        files = {COMPOSE_COMMON, COMPOSE_LLM_API, COMPOSE_LOCAL_LLM, COMPOSE_EVAL, COMPOSE_E2_STUB, COMPOSE_E2_PRODUCT}
        on_disk = set((REPO_ROOT / "deploy" / "engine-isolated").glob("compose*.yaml"))
        on_disk |= set((REPO_ROOT / "deploy" / "eval").glob("compose*.yaml"))
        self.assertEqual(on_disk, files)

    def test_secrets_example_lists_exactly_the_secret_names(self):
        text = (REPO_ROOT / "deploy" / "eval" / "eval.secrets.env.example").read_text(encoding="utf-8")
        keys = {line.split("=", 1)[0] for line in text.splitlines() if line and not line.startswith("#")}
        self.assertEqual(keys, set(SECRET_ENV_NAMES))
        self.assertNotRegex(text, r"(?m)^[A-Z_]+=.+$")

    def test_no_restart_policy_anywhere(self):
        # 회차 중 자동 재시작은 계측기 카운터·대상 상태·엔진 tmpfs를 조용히 초기화한다. 기본값(no)에 맡긴다.
        for path in (COMPOSE_COMMON, COMPOSE_LLM_API, COMPOSE_LOCAL_LLM, COMPOSE_EVAL, COMPOSE_E2_STUB, COMPOSE_E2_PRODUCT):
            text = _yaml_without_comments(Path(path).read_text(encoding="utf-8"))
            with self.subTest(path=path.name):
                self.assertNotRegex(text, r"(?m)^\s*restart\s*:")

    def test_every_publish_is_loopback_only(self):
        for path in (COMPOSE_COMMON, COMPOSE_LLM_API, COMPOSE_LOCAL_LLM, COMPOSE_EVAL, COMPOSE_E2_STUB, COMPOSE_E2_PRODUCT):
            lines = _yaml_without_comments(Path(path).read_text(encoding="utf-8")).splitlines()
            entries = []
            for index, line in enumerate(lines):
                if re.fullmatch(r"    ports:\s*", line):
                    entries += [l.strip()[2:] for l in lines[index + 1:] if l.startswith("      - ")][:1]
            with self.subTest(path=path.name):
                for entry in entries:
                    self.assertTrue(entry.strip('"').startswith("127.0.0.1:"), entry)

    def test_meter_service_matches_origin_check(self):
        self.assertIn(METER_SERVICE, compose_services(COMPOSE_EVAL))
        self.assertEqual(compose_services(COMPOSE_EVAL), {"engine-db", "engine", "target", METER_SERVICE, "eval-obs"})
        self.assertEqual(compose_services(COMPOSE_COMMON), {"engine", "engine-db", "eval-llm-gw", "eval-api"})

    def test_variable_scanner_sees_escapes_and_comments(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "c.yaml")
            path.write_text('a: "$$X ${Y:?} ${Z:-}"  # ${COMMENTED}\n', encoding="utf-8")
            self.assertEqual(compose_variables([path]), {"Y", "Z"})
            path.write_text("a: $BARE\n", encoding="utf-8")
            with self.assertRaises(AssertionError):
                compose_variables([path])


if __name__ == "__main__":
    unittest.main()
