"""탐색 분리 배치 O 회차 설정·계획(결정안 §2·§3, 4-7c-2) 시험."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_config import COMPOSE_SPLIT, REPO_ROOT, Image, RunnerConfigError, parse_runner_config  # noqa: E402
from runner_split_config import (  # noqa: E402
    COMPOSE_FILES, COMPOSE_SYNTHETIC, SCRIPTED_MODEL_SCRIPT, SplitConfigError, parse_split, plan_split_run, read_manifest, split_compose_args,
    split_compose_environment,
)
from test_preservation_record import model_options, options  # noqa: E402
from test_runner_config import runner_config  # noqa: E402

TARGET = Image("example/target@sha256:" + "6" * 64, "sha256:" + "6" * 64)


def acceptance_config():
    payload = runner_config()
    payload.update(purpose="browser_acceptance", path_key_file="/srv/eval/path.key", preservation=model_options())
    return parse_runner_config(payload)


def split_block(**changes):
    block = {"product_tag": "eval-split-0123456789ab", "product_source_sha": "a" * 40,
             "image_ids": {"product": "sha256:" + "7" * 64, "explore_browser": "sha256:" + "8" * 64, "fixture": "sha256:" + "9" * 64},
             "manifest_path": "/srv/eval-secrets/openrouter-manifest.json", "target_name": "app.eval.test",
             "product_port": 28787, "dns_control_port": 29083, "explore_port": 8082, "evidence_max_bytes": 4 << 20,
             "install_valid_hours": 4, "max_runs": 2, "registration_valid_hours": 2, "wide_writes": True,
             "profile_id": "product-model-openrouter", "task_wait_seconds": 120, "poll_interval_seconds": 2,
             "boundary_stop_seconds": 30}
    block.update(changes)
    return block


class TestParseSplit(unittest.TestCase):
    def setUp(self):
        self.config = acceptance_config()

    def test_valid_block(self):
        split = parse_split(split_block(), self.config)
        self.assertEqual({name: image.ref for name, image in split.images.items()},
                         {"product": "abc2lab-product:eval-split-0123456789ab",
                          "explore_browser": "abc2lab-explore-browser:eval-split-0123456789ab",
                          "fixture": "abc2lab-product-fixture:eval-split-0123456789ab"})
        self.assertEqual(split.images["product"].id, "sha256:" + "7" * 64)
        self.assertIs(split.wide_writes, True)

    def test_boundary_stop_bounds_match_docker(self):
        # docker stop_container가 받는 경계값(1·600)은 설정도 받는다(#668 리뷰: 설정이 받은 값을 정지가 거부하지 않게).
        for seconds in (1, 600):
            with self.subTest(seconds=seconds):
                self.assertEqual(parse_split(split_block(boundary_stop_seconds=seconds), self.config).boundary_stop_seconds, seconds)

    def test_only_for_preservation_runs(self):
        official = parse_runner_config(runner_config())
        with self.assertRaises(SplitConfigError) as caught:
            parse_split(split_block(), official)
        self.assertEqual(caught.exception.errors, ["config.split: PRESERVATION_ONLY"])

    def test_field_errors(self):
        cases = {
            "tag": ({"product_tag": "-bad"}, "config.split.product_tag: INVALID"),
            "sha_short": ({"product_source_sha": "a" * 12}, "config.split.product_source_sha: INVALID"),
            "image_missing": ({"image_ids": {"product": "sha256:" + "7" * 64}}, "config.split.image_ids: INVALID"),
            "image_tag": ({"image_ids": {"product": "abc:1", "explore_browser": "sha256:" + "8" * 64,
                                         "fixture": "sha256:" + "9" * 64}}, "config.split.image_ids: INVALID"),
            "manifest_relative": ({"manifest_path": "manifest.json"}, "config.split.manifest_path: INVALID"),
            "manifest_in_runs": ({"manifest_path": "/srv/eval/runs/m.json"}, "config.split.manifest_path: INSIDE_RUNS_ROOT"),
            "target_single_label": ({"target_name": "eval-meter"}, "config.split.target_name: INVALID"),
            "port_zero": ({"product_port": 0}, "config.split.ports: INVALID"),
            "port_same": ({"dns_control_port": 28787}, "config.split.ports: OVERLAP"),
            "port_used_by_b": ({"explore_port": 18081}, "config.split.ports: OVERLAP"),  # B 설정의 관측 publish 포트
            "evidence": ({"evidence_max_bytes": 0}, "config.split.evidence_max_bytes: INVALID"),
            "hours": ({"registration_valid_hours": 0}, "config.split.registration_valid_hours: INVALID"),
            "runs": ({"max_runs": 0}, "config.split.max_runs: INVALID"),
            "writes": ({"wide_writes": 1}, "config.split.wide_writes: INVALID"),
            "profile": ({"profile_id": "Product Model"}, "config.split.profile_id: INVALID"),
            "wait": ({"poll_interval_seconds": 120}, "config.split.wait: INVALID"),
            "stop_zero": ({"boundary_stop_seconds": 0}, "config.split.boundary_stop_seconds: INVALID"),
            "stop_over_docker": ({"boundary_stop_seconds": 601}, "config.split.boundary_stop_seconds: INVALID"),
            "stop_bool": ({"boundary_stop_seconds": True}, "config.split.boundary_stop_seconds: INVALID"),
            "interval_not_below": ({"task_wait_seconds": 5, "poll_interval_seconds": 5}, "config.split.wait: INTERVAL_NOT_BELOW_WAIT"),
        }
        for name, (change, error) in cases.items():
            with self.subTest(case=name), self.assertRaises(SplitConfigError) as caught:
                parse_split(split_block(**change), self.config)
            self.assertIn(error, caught.exception.errors)
        for raw in (None, dict(split_block(), extra=1), {k: v for k, v in split_block().items() if k != "max_runs"}):
            with self.subTest(raw=type(raw).__name__), self.assertRaises(SplitConfigError) as caught:
                parse_split(raw, self.config)
            self.assertEqual(caught.exception.errors, ["config.split: FIELDS"])


class TestRunnerConfigBlock(unittest.TestCase):
    def test_split_block_is_parsed_with_the_runner_config(self):
        payload = runner_config()
        payload.update(purpose="browser_acceptance", path_key_file="/srv/eval/path.key", preservation=model_options(), split=split_block())
        config = parse_runner_config(payload)
        self.assertEqual(config.split.target_name, "app.eval.test")
        self.assertIsNone(acceptance_config().split)
        payload["split"] = split_block(product_port=0)
        with self.assertRaises(RunnerConfigError) as caught:
            parse_runner_config(payload)
        self.assertEqual(caught.exception.errors, ["config.split.ports: INVALID"])
        # 2026-11-18 결정 (a): 분리 배치는 공식 회차에서도 받는다. 대신 경로 가림 키·보존 옵션이 있어야 한다.
        official = runner_config()
        official["split"] = split_block()
        with self.assertRaises(RunnerConfigError) as caught:
            parse_runner_config(official)
        self.assertEqual(caught.exception.errors, ["config.path_key_file: MISSING"])
        official["path_key_file"] = "/srv/eval/path.key"
        with self.assertRaises(RunnerConfigError) as caught:
            parse_runner_config(official)
        self.assertEqual(caught.exception.errors, ["config.split: PRESERVATION_ONLY"])
        official["preservation"] = model_options()
        config = parse_runner_config(official)
        self.assertEqual((config.purpose, config.split.target_name), ("official", "app.eval.test"))


class TestReadManifest(unittest.TestCase):
    def test_protected_v3_only(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp, "manifest.json")
            path.write_text(json.dumps({"version": 3, "model": "qwen/qwen3.6-35b-a3b", "approval_id": "secret-approval"}))
            os.chmod(path, 0o600)
            self.assertEqual(read_manifest(path)["version"], 3)
            cases = {"mode": (0o644, {"version": 3, "model": "m"}, "split.manifest: UNTRUSTED"),
                     "v2": (0o600, {"version": 2, "model": "m"}, "split.manifest: INVALID"),
                     "no_model": (0o600, {"version": 3}, "split.manifest: INVALID")}
            for name, (mode, value, error) in cases.items():
                with self.subTest(case=name):
                    path.write_text(json.dumps(dict(value, approval_id="secret-approval")))
                    os.chmod(path, mode)
                    with self.assertRaises(SplitConfigError) as caught:
                        read_manifest(path)
                    self.assertEqual(caught.exception.errors, [error])
                    self.assertNotIn("secret-approval", str(caught.exception))
            link = Path(tmp, "link.json")
            link.symlink_to(path)
            with self.assertRaises(SplitConfigError) as caught:
                read_manifest(link)
            self.assertEqual(caught.exception.errors, ["split.manifest: UNREADABLE"])


class TestPlan(unittest.TestCase):
    def setUp(self):
        self.config = acceptance_config()
        self.split = parse_split(split_block(), self.config)
        self.plan = plan_split_run(self.config, self.split, run_id="run1", target_image=TARGET, meter_port=8080)

    def test_plan_reuses_b_meter_target_and_observer_values(self):
        env = self.plan.compose_env
        self.assertEqual(env, {
            "PRODUCT_TAG": "eval-split-0123456789ab", "PRODUCT_SOURCE_SHA": "a" * 40, "PRODUCT_PORT": "28787",
            "PRODUCT_DNS_CONTROL_PORT": "29083", "EVAL_TARGET_NAME": "app.eval.test", "EVAL_TARGET_IMAGE": TARGET.ref,
            "EVAL_METER_IMAGE": "example/m:fixed", "EVAL_METER_PORT": "8080", "EVAL_METER_EXPLORE_PORT": "8082",
            "EVAL_TARGET_PORT": "3000", "EVAL_METER_DRAIN_GRACE": "30s", "EVAL_OBS_PORT": "8081", "EVAL_OBS_HOST_PORT": "18081",
            "EVAL_OBS_PATH": "/api/records", "EVAL_PATH_KEY_FILE": "/srv/eval/path.key"})
        self.assertEqual((self.plan.project, self.plan.target_origin, self.plan.profiles),
                         ("eval-run1", "http://app.eval.test:8080", ("exploration",)))
        self.assertEqual(self.plan.compose_files[-1], COMPOSE_SPLIT)
        self.assertEqual(set(self.plan.images), {"product", "explore_browser", "fixture", "meter", "target"})
        args = split_compose_args(self.plan)
        self.assertEqual(args[:5], ["compose", "-p", "eval-run1", "--env-file", "/dev/null"])
        self.assertEqual(args[-2:], ["--profile", "exploration"])
        self.assertNotIn(str(self.config.secrets_env_file), args)

    def test_meter_port_must_not_collide(self):
        for port in (0, 28787, 8082):
            with self.subTest(port=port), self.assertRaises(SplitConfigError):
                plan_split_run(self.config, self.split, run_id="r", target_image=TARGET, meter_port=port)

    def test_runtime_env_adds_evidence_uid_and_protected_paths_without_overlap(self):
        protected = {"PRODUCT_INSTALL_FILE": "/runs/r/private/install.json"}
        env = split_compose_environment(self.plan, evidence_dir=Path("/runs/r/meter"), protected_env=protected)
        self.assertEqual((env["EVAL_EVIDENCE_DIR"], env["EVAL_RUNNER_UID"], env["PRODUCT_INSTALL_FILE"]),
                         ("/runs/r/meter", str(os.geteuid()), "/runs/r/private/install.json"))
        for bad in ({"PRODUCT_TAG": "x"}, {"EVAL_EVIDENCE_DIR": "/x"}):
            with self.subTest(bad=bad), self.assertRaises(SplitConfigError):
                split_compose_environment(self.plan, evidence_dir=Path("/runs/r/meter"), protected_env=bad)


def synthetic_config(**changes):
    payload = runner_config()
    payload.update(purpose="browser_acceptance", path_key_file="/srv/eval/path.key", preservation=options(), **changes)
    return parse_runner_config(payload)


class TestSyntheticModelBlock(unittest.TestCase):
    """합성 모델 모드(2026-11-18 사용자 결정 (a)): 보존 회차에서만, 실행 출처 synthetic_fixture로만, manifest 없이 받는다."""

    def block(self, **changes):
        values = {"manifest_path": None, "synthetic_model": {"plan_path": "/srv/eval/plans/vampi.json", "max_tokens": 2048}}
        values.update(changes)
        return split_block(**values)

    def test_valid_synthetic_block_adds_the_overlay(self):
        config = synthetic_config()
        split = parse_split(self.block(), config)
        self.assertTrue(split.synthetic)
        self.assertEqual((split.manifest_path, split.synthetic_plan_path, split.synthetic_max_tokens),
                         (None, Path("/srv/eval/plans/vampi.json"), 2048))
        plan = plan_split_run(config, split, run_id="s1", target_image=TARGET, meter_port=8080)
        self.assertEqual(plan.compose_files, (*COMPOSE_FILES, COMPOSE_SYNTHETIC))
        self.assertEqual(plan.compose_env["EVAL_SCRIPTED_MODEL_SCRIPT"], str(SCRIPTED_MODEL_SCRIPT))
        env = split_compose_environment(plan, evidence_dir=Path("/r/meter"), protected_env={},
                                        scripted_plan=Path("/r/scripted-plan.json"))
        self.assertEqual(env["EVAL_SCRIPTED_MODEL_PLAN"], "/r/scripted-plan.json")
        # 합성 overlay가 있으면 계획 사본이 꼭 있어야 하고, 실제 모델 계획에는 줄 수 없다
        with self.assertRaises(SplitConfigError):
            split_compose_environment(plan, evidence_dir=Path("/r/meter"), protected_env={})
        real = plan_split_run(acceptance_config(), parse_split(split_block(), acceptance_config()), run_id="s2",
                              target_image=TARGET, meter_port=8080)
        self.assertNotIn(COMPOSE_SYNTHETIC, real.compose_files)
        self.assertNotIn("EVAL_SCRIPTED_MODEL_SCRIPT", real.compose_env)
        with self.assertRaises(SplitConfigError):
            split_compose_environment(real, evidence_dir=Path("/r/meter"), protected_env={}, scripted_plan=Path("/r/p.json"))

    def test_synthetic_conditions(self):
        cases = {
            "manifest_given": (synthetic_config(), self.block(manifest_path="/srv/m.json"), "SYNTHETIC_HAS_NO_MANIFEST"),
            "model_run_source": (acceptance_config(), self.block(), "EXECUTION_SOURCE_NOT_SYNTHETIC"),
            "relative_plan": (synthetic_config(), self.block(synthetic_model={"plan_path": "p.json", "max_tokens": 1}), "INVALID"),
            "no_tokens": (synthetic_config(), self.block(synthetic_model={"plan_path": "/p.json"}), "INVALID"),
            "zero_tokens": (synthetic_config(), self.block(synthetic_model={"plan_path": "/p.json", "max_tokens": 0}), "INVALID"),
            "extra": (synthetic_config(), self.block(synthetic_model={"plan_path": "/p.json", "max_tokens": 1, "x": 1}), "INVALID"),
            "not_object": (synthetic_config(), self.block(synthetic_model="on"), "INVALID"),
        }
        for name, (config, block, reason) in cases.items():
            with self.subTest(case=name), self.assertRaises(SplitConfigError) as caught:
                parse_split(block, config)
            self.assertTrue(any(reason in error for error in caught.exception.errors), caught.exception.errors)
        # 실제 모델(OpenRouter) O 회차를 합성으로 표시하면 거부한다(실측이 채점·보존율에서 빠지지 않게)
        with self.assertRaises(SplitConfigError) as caught:
            parse_split(split_block(), synthetic_config())
        self.assertIn("config.split: EXECUTION_SOURCE_NOT_MODEL_RUN", caught.exception.errors)
        # 합성 모드도 실제 모델 모드처럼 manifest null은 합성 블록이 있을 때만이다
        with self.assertRaises(SplitConfigError):
            parse_split(split_block(manifest_path=None), acceptance_config())

    def test_official_purpose_is_refused(self):
        # 공식 회차에는 합성 모드를 쓸 수 없다(러너 설정이 보존 옵션부터 거부하고, split 검사도 따로 막는다).
        payload = runner_config()
        payload.update(purpose="official", path_key_file="/srv/eval/path.key", preservation=options(), split=self.block())
        with self.assertRaises(RunnerConfigError) as caught:
            parse_runner_config(payload)
        self.assertTrue(any("OFFICIAL_FORBIDDEN" in error for error in caught.exception.errors), caught.exception.errors)


def compose_available() -> bool:
    try:
        return subprocess.run(["docker", "compose", "version"], capture_output=True, timeout=30).returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


@unittest.skipUnless(compose_available(), "docker compose 없음")
class TestPlanRendersWithCompose(unittest.TestCase):
    """계획의 인자·환경으로 실제 compose가 분리 배치를 렌더한다(컨테이너는 띄우지 않는다)."""

    def test_render(self):
        config = acceptance_config()
        split = parse_split(split_block(), config)
        plan = plan_split_run(config, split, run_id="render1", target_image=TARGET, meter_port=8080)
        with tempfile.TemporaryDirectory() as tmp:
            protected = {}
            for env_name in ("PRODUCT_INSTALL_FILE", "PRODUCT_POSTGRES_PASSWORD_FILE", "EXPLORE_BOUNDARY_CONFIG_FILE",
                             "EXPLORE_CA_CERT_FILE", "EXPLORE_CA_KEY_FILE", "EXPLORE_PATH_KEY_FILE"):
                path = Path(tmp, env_name.lower())
                path.write_text("x")
                protected[env_name] = str(path)
            env = split_compose_environment(plan, evidence_dir=Path(tmp, "meter"), protected_env=protected)
            keep = {name: os.environ[name] for name in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG") if name in os.environ}
            result = subprocess.run(["docker", *split_compose_args(plan), "config", "--format", "json"], cwd=REPO_ROOT,
                                    env={**keep, **env}, capture_output=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors="replace")[-400:])
        rendered = json.loads(result.stdout)
        services = rendered["services"]
        self.assertEqual(rendered["name"], "eval-render1")
        self.assertEqual(services["preparation"]["image"], "abc2lab-product:eval-split-0123456789ab")
        self.assertEqual(services["explore-mcp"]["image"], "abc2lab-explore-browser:eval-split-0123456789ab")
        self.assertEqual(services["fixture-dns"]["image"], "abc2lab-product-fixture:eval-split-0123456789ab")
        self.assertEqual(services["target"]["image"], TARGET.ref)
        self.assertIn("-listen=10.252.3.20:8080", services["eval-meter"]["command"])
        self.assertIn("-explore-listen=10.252.11.20:8082", services["eval-meter"]["command"])
        self.assertEqual(services["eval-meter"]["networks"]["target"]["aliases"], ["app.eval.test"])
        # 호스트 publish는 셋뿐이고 전부 루프백이다: 제품 API(preparation), 시험 DNS 제어, 관측 front.
        published = {name: [(port["host_ip"], port["published"], port["target"]) for port in service["ports"]]
                     for name, service in services.items() if service.get("ports")}
        self.assertEqual(published, {"preparation": [("127.0.0.1", "28787", 8787)], "fixture-dns": [("127.0.0.1", "29083", 9083)],
                                     "eval-obs": [("127.0.0.1", "18081", 8081)]})
        secrets = {name: value["file"] for name, value in rendered["secrets"].items()}
        self.assertEqual(secrets["product_install"], protected["PRODUCT_INSTALL_FILE"])
        self.assertEqual(secrets["explore_ca_key"], protected["EXPLORE_CA_KEY_FILE"])
        self.assertTrue(all(name in services for name in ("explore-boundary", "explore-browser", "explore-mcp", "explore-evidence-init")))
        self.assertTrue(all(name not in services for name in ("juice-shop", "target-front", "synthetic-model")))


if __name__ == "__main__":
    unittest.main()
