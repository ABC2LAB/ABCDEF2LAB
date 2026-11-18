"""탐색 분리 배치 B 회차(원본 직접 배치)의 대상 이름 origin·목표 공통 문장(결정안 §6, 4-7c) 시험."""

from __future__ import annotations

import dataclasses
import json
import os
import subprocess
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner import build_spec, plan_task  # noqa: E402
from runner_config import (  # noqa: E402
    COMPOSE_COMMON, COMPOSE_EVAL, COMPOSE_LLM_API, COMPOSE_TARGET_ALIAS, RunnerConfigError, parse_runner_config, plan_run,
)
from runner_engine import EngineAPIError  # noqa: E402
from runner_goal import common_goal, common_goal_sha256  # noqa: E402
from runner_probe import POSITIVE, probe_expectations  # noqa: E402
from test_preflight import configuration  # noqa: E402
from test_preservation_record import model_options, options  # noqa: E402
from test_runner_config import runner_config  # noqa: E402
from test_runner_probe import make_snapshot  # noqa: E402
from test_runner_split_config import split_block  # noqa: E402


def split_config(with_split=True):
    payload = runner_config()
    payload.update(purpose="browser_acceptance", path_key_file="/srv/eval/path.key", preservation=model_options())
    if with_split:
        payload["split"] = split_block()
    return parse_runner_config(payload)


class TestPlan(unittest.TestCase):
    def test_baseline_gets_alias_overlay_and_task_origin(self):
        plan = plan_run(split_config(), configuration(), "baseline", "b1")
        self.assertEqual(plan.compose_files[:3], (COMPOSE_COMMON, COMPOSE_LLM_API, COMPOSE_EVAL))
        self.assertEqual(plan.compose_files[-1], COMPOSE_TARGET_ALIAS)
        self.assertEqual((plan.compose_env["EVAL_TARGET_NAME"], plan.task_origin), ("app.eval.test", "http://app.eval.test:8080"))
        # 선언의 target_origin(계측기 서비스 이름)은 그대로다. 지문·사전검사 규칙을 넓히지 않는다.
        self.assertEqual(configuration()["baseline"]["target_origin"], "http://eval-meter:8080")

    def test_without_split_nothing_changes(self):
        plan = plan_run(split_config(with_split=False), configuration(), "baseline", "b1")
        self.assertNotIn(COMPOSE_TARGET_ALIAS, plan.compose_files)
        self.assertNotIn("EVAL_TARGET_NAME", plan.compose_env)
        self.assertIsNone(plan.task_origin)

    def test_ours_does_not_run_the_direct_layout_when_split(self):
        with self.assertRaises(RunnerConfigError) as caught:
            plan_run(split_config(), configuration(), "ours", "o1")
        self.assertEqual(caught.exception.errors, ["preflight.ours: SPLIT_LAYOUT_PLAN"])


class TestTask(unittest.TestCase):
    TASK = {"description": "평가 대상 http://app.eval.test:8080 웹앱을 진단한다.", "goal": "설정 목표(분리 배치 B에서는 쓰지 않음)",
            "timeout_seconds": 1200, "plan_heartbeat_seconds": 600, "seed_first_intent": False, "coverage_enabled": True}

    def test_split_baseline_goal_is_the_product_common_goal(self):
        plan = plan_run(split_config(), configuration(), "baseline", "b1")
        request, digest = plan_task(self.TASK, plan, "http://eval-meter:8080")
        body = json.loads(request.body)
        expected = common_goal("http://app.eval.test:8080", ["/"], [])
        self.assertEqual(body["goal"], expected)
        self.assertEqual(digest, common_goal_sha256(expected))
        self.assertEqual({k: v for k, v in body.items() if k != "goal"}, {k: v for k, v in self.TASK.items() if k != "goal"})
        self.assertIn("http://app.eval.test:8080/", expected)  # 등록 경로(/) 목록 문장

    def test_split_baseline_description_must_use_the_target_name(self):
        plan = plan_run(split_config(), configuration(), "baseline", "b1")
        task = dict(self.TASK, description="평가 대상 http://eval-meter:8080 웹앱을 진단한다.")
        with self.assertRaises(EngineAPIError) as caught:
            plan_task(task, plan, "http://eval-meter:8080")
        self.assertEqual(str(caught.exception), "task_target_mismatch")

    def test_without_split_the_configured_goal_is_kept(self):
        plan = plan_run(split_config(with_split=False), configuration(), "baseline", "b1")
        task = dict(self.TASK, description="평가 대상 http://eval-meter:8080 웹앱을 진단한다.")
        request, digest = plan_task(task, plan, "http://eval-meter:8080")
        self.assertEqual((json.loads(request.body)["goal"], digest), (task["goal"], None))

    def test_spec_carries_origin_and_digest_together(self):
        config, preflight = split_config(), configuration()
        plan = plan_run(config, preflight, "baseline", "b1")
        spec = build_spec(config, plan, preflight, "t" * 64, "원본@35b", 0, path_key_ref="a" * 16, goal_common_sha256="g" * 64)
        self.assertEqual((spec.task_origin, spec.goal_common_sha256), ("http://app.eval.test:8080", "g" * 64))
        with self.assertRaises(RunnerConfigError) as caught:
            build_spec(config, plan, preflight, "t" * 64, "원본@35b", 0, path_key_ref="a" * 16, goal_common_sha256=None)
        self.assertEqual(caught.exception.errors, ["config.split: GOAL_MISMATCH"])
        plain = plan_run(split_config(with_split=False), preflight, "baseline", "b1")
        with self.assertRaises(RunnerConfigError):
            build_spec(config, plain, preflight, "t" * 64, "원본@35b", 0, path_key_ref="a" * 16, goal_common_sha256="g" * 64)


class TestCliUsageNames(unittest.TestCase):
    def test_only_split_baseline_counts(self):
        # B에만 있는 CLI 이름별 호출 수는 분리 배치 B 회차만 센다(결정안 §10). 분리 배치가 아니면 세지 않는다.
        config, preflight = split_config(), configuration()
        plan = plan_run(config, preflight, "baseline", "b1")
        spec = build_spec(config, plan, preflight, "t" * 64, "원본@35b", 0, path_key_ref="a" * 16, goal_common_sha256="g" * 64)
        self.assertEqual(spec.cli_usage_names, ("npx", "playwright-cli"))
        plain_config = split_config(with_split=False)
        plain = plan_run(plain_config, preflight, "baseline", "b1")
        self.assertEqual(build_spec(plain_config, plain, preflight, "t" * 64, "원본@35b", 0, path_key_ref="a" * 16).cli_usage_names, ())


class TestProbe(unittest.TestCase):
    def test_alias_positive_control_only_for_split_baseline(self):
        plan = plan_run(split_config(), configuration(), "baseline", "b1")
        rows = {row.id: row for row in probe_expectations(plan, make_snapshot())}
        self.assertEqual((rows["meter_alias_tcp"].target, rows["meter_alias_tcp"].role), ("app.eval.test:8080", POSITIVE))
        plain = dataclasses.replace(plan, task_origin=None)
        self.assertNotIn("meter_alias_tcp", {row.id for row in probe_expectations(plain, make_snapshot())})


def compose_available() -> bool:
    try:
        return subprocess.run(["docker", "compose", "version"], capture_output=True, timeout=30).returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


@unittest.skipUnless(compose_available(), "docker compose 없음")
class TestAliasRender(unittest.TestCase):
    def test_alias_on_the_engine_network_only(self):
        plan = plan_run(split_config(), configuration(), "baseline", "b1")
        env = {name: os.environ[name] for name in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG") if name in os.environ}
        env.update(plan.compose_env, EVAL_EVIDENCE_DIR="/srv/x/meter", EVAL_LLM_EVIDENCE_DIR="/srv/x/llm", EVAL_RUNNER_UID="1000",
                   EVAL_RUNNER_GID="1000", EVAL_ENGINE_ADMIN_PASSWORD="x", EVAL_ENGINE_DB_PASSWORD="x", EVAL_LLM_API_KEY="x",
                   EVAL_PATH_KEY_FILE="/srv/eval/path.key")
        args = ["docker", "compose", "-p", plan.project, "--env-file", "/dev/null"]
        for path in plan.compose_files:
            args += ["-f", str(path)]
        result = subprocess.run([*args, "config", "--format", "json"], env=env, capture_output=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors="replace")[-400:])
        networks = json.loads(result.stdout)["services"]["eval-meter"]["networks"]
        self.assertEqual(networks["engine_net"], {"aliases": ["app.eval.test"]})
        self.assertIn("target_net", networks)  # 기존 망은 그대로
        self.assertFalse((networks.get("target_net") or {}).get("aliases"))


if __name__ == "__main__":
    unittest.main()
