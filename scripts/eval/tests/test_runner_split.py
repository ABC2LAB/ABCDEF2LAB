"""탐색 분리 배치 평가 회차의 주소·경계 설정(결정안 §2·§3 B(a), 4-7a) 시험."""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import hashlib  # noqa: E402
import re  # noqa: E402
import tempfile  # noqa: E402
from pathlib import Path  # noqa: E402

from runner_config import REPO_ROOT  # noqa: E402
from runner_split import (  # noqa: E402
    PRODUCT_MODEL_API, SCRIPTED_MODEL_BASE_URL, SCRIPTED_MODEL_IP, synthetic_local_model,
    BOUNDARY_EVIDENCE_DIR, EXPLORATION_BLOCK, METER_EXPLORE_IP, REGISTRATION_BASIS, WIDE_METHODS, WORKFLOW_DEFAULTS,
    SplitInputError, boundary_config, install_input, split_conditions, read_boundary_template, read_workflow_defaults, target_registration,
    to_eval_address, product_run_seconds,
)


class TestToEvalAddress(unittest.TestCase):
    def test_moves_known_product_prefixes_keeping_host_and_port(self):
        self.assertEqual(to_eval_address("172.31.248.10:3128"), "10.252.8.10:3128")
        self.assertEqual(to_eval_address("172.31.252.10:0"), "10.252.2.10:0")
        self.assertEqual(to_eval_address("172.31.250.12"), "10.252.0.12")

    def test_unknown_or_malformed_addresses_are_refused(self):
        for value in ("172.31.249.5:1", "10.0.0.1:80", "172.31.248.300:1", "172.31.248.10:x", "172.31.248.", "explore-direct:3129"):
            with self.subTest(value=value), self.assertRaises(SplitInputError) as caught:
                to_eval_address(value)
            self.assertEqual(str(caught.exception), "split_address_unknown")


class TestBoundaryConfig(unittest.TestCase):
    def setUp(self):
        self.template = read_boundary_template()

    def test_real_template_moves_addresses_keeps_limits_and_adds_eval_fields(self):
        config = boundary_config(self.template, 8081, 4 << 20)
        self.assertNotIn("172.31.", json.dumps(config))
        self.assertEqual((config["browser_listen"], config["direct_listen"]), ("10.252.8.10:3128", "10.252.7.10:3129"))
        self.assertEqual(config["control_endpoints"], [to_eval_address(v) for v in self.template["control_endpoints"]])
        # 계측기 본 입구(대상 이름이 풀리는 승인 주소)는 금지 목록에 넣지 않는다. 넣으면 승인 대상 판정이 막힌다.
        self.assertNotIn("10.252.3.20:0", config["control_endpoints"])
        self.assertEqual((config["evidence_dir"], config["evidence_max_bytes"], config["eval_upstream"]),
                         (BOUNDARY_EVIDENCE_DIR, 4 << 20, f"{METER_EXPLORE_IP}:8081"))
        # 주소가 아닌 칸(상한·경로·uid)은 제품 예시 그대로다(B(a): 상한은 제품 기본값).
        moved = {"browser_listen", "direct_listen", "control_endpoints", "evidence_dir", "evidence_max_bytes", "eval_upstream"}
        self.assertEqual({k: v for k, v in config.items() if k not in moved},
                         {k: v for k, v in self.template.items() if k not in moved})
        self.assertNotIn("evidence_dir", self.template)  # 틀은 바뀌지 않는다

    def test_bad_inputs_and_templates_use_fixed_codes(self):
        cases = {
            "port_zero": (self.template, 0, 1, "split_boundary_input_invalid"),
            "port_high": (self.template, 65536, 1, "split_boundary_input_invalid"),
            "port_bool": (self.template, True, 1, "split_boundary_input_invalid"),
            "max_zero": (self.template, 8081, 0, "split_boundary_input_invalid"),
            "wrong_format": (dict(self.template, format="x"), 8081, 1, "split_boundary_template_invalid"),
            "has_eval_field": (dict(self.template, eval_upstream="1.2.3.4:1"), 8081, 1, "split_boundary_template_invalid"),
            "listen_not_string": (dict(self.template, browser_listen=3128), 8081, 1, "split_boundary_template_invalid"),
            "endpoints_not_list": (dict(self.template, control_endpoints="172.31.252.10:0"), 8081, 1, "split_boundary_template_invalid"),
            "unknown_prefix": (dict(self.template, control_endpoints=["172.31.249.5:0"]), 8081, 1, "split_address_unknown"),
            "address_in_new_field": (dict(self.template, metrics_listen="172.31.248.10:9100"), 8081, 1, "split_address_unknown"),
        }
        for name, (template, port, max_bytes, code) in cases.items():
            with self.subTest(case=name), self.assertRaises(SplitInputError) as caught:
                boundary_config(template, port, max_bytes)
            self.assertEqual(str(caught.exception), code)


MANIFEST = {"version": 3, "profile_id": 1, "approval_id": "synthetic-approval"}


class TestInstallInput(unittest.TestCase):
    def setUp(self):
        self.defaults, self.digest = read_workflow_defaults()

    def build(self, **changes):
        options = dict(admin_password="x" * 24, manifest=MANIFEST, meter_port=8080, workflow_defaults=self.defaults,
                       wide_writes=True, valid_for_hours=4, max_runs=2, run_seconds=2100)
        options.update(changes)
        return install_input(**options)

    def test_defaults_file_is_read_not_copied(self):
        self.assertEqual(self.digest, hashlib.sha256(WORKFLOW_DEFAULTS.read_bytes()).hexdigest())
        with tempfile.TemporaryDirectory() as tmp:
            for text in ('{"format": "x"}', "not json", "[]"):
                path = Path(tmp, "w.json")
                path.write_text(text, encoding="utf-8")
                with self.subTest(text=text), self.assertRaises(SplitInputError) as caught:
                    read_workflow_defaults(path)
                self.assertEqual(str(caught.exception), "split_workflow_defaults_invalid")

    def test_install_uses_product_defaults_and_eval_addresses(self):
        value = self.build()
        policy = value["target_policy"]
        self.assertEqual((value["format"], value["database_host"], value["database_name"]),
                         ("abc2lab.product-install/v2", "10.252.2.10", "abc2lab"))
        self.assertEqual((policy["format"], policy["allowed_ports"], policy["private_cidrs"], policy["resolver_address"]),
                         ("abc2lab.product-target-policy/v1", [8080], ["10.252.3.0/24"], "10.252.2.40:5353"))
        # B(a): 상한 칸은 넣지 않아 제품 기본값을 쓴다. 모델 키 칸도 없다(설치 뒤 API로 넣음).
        for name in ("max_requests", "max_targets"):
            self.assertNotIn(name, policy)
        for name in ("api_key_file", "local_model", "upstream_ca_file"):
            self.assertNotIn(name, value)
        # B(a)의 예외 하나(2026-11-25 사용자): 워크플로 회차 실행 시간만 넣고, 다른 상한 칸은 없다.
        self.assertEqual(value["runtime_limits"], {"workflow": {"run_seconds": 2100}})
        # 워크플로는 기본값 파일 그대로, 쓰기 허용만 회차 설정을 따른다.
        self.assertEqual(policy["workflow"], dict(self.defaults, allow_reversible_writes=True))
        self.assertEqual(self.build(wide_writes=False)["target_policy"]["workflow"], self.defaults)
        self.assertIs(self.defaults["allow_reversible_writes"], False)  # 입력 사전을 바꾸지 않는다
        self.assertEqual(policy["exploration"], EXPLORATION_BLOCK)
        self.assertEqual(value["openrouter_manifest"], MANIFEST)

    def test_install_inputs_are_checked(self):
        for change in ({"admin_password": "short"}, {"manifest": dict(MANIFEST, version=2)}, {"manifest": []},
                       {"meter_port": 0}, {"meter_port": True}, {"wide_writes": 1}, {"valid_for_hours": 0}, {"max_runs": 0},
                       {"workflow_defaults": dict(self.defaults, extra=1)},
                       {"run_seconds": 0}, {"run_seconds": 3601}, {"run_seconds": True}, {"run_seconds": 2100.0}):
            with self.subTest(change=change), self.assertRaises(SplitInputError):
                self.build(**change)
        self.assertEqual(self.build(run_seconds=3600)["runtime_limits"], {"workflow": {"run_seconds": 3600}})

    def test_product_run_seconds_is_the_time_budget_plus_margin(self):
        # 러너 시간 예산 1800초(고정 초안 §10) → O 설치 2100초. 제품 상한 3600을 넘으면 설정 오류다.
        self.assertEqual(product_run_seconds(1800), 2100)
        self.assertEqual(product_run_seconds(3300), 3600)
        for budget in (3301, 0, -1, True, 1800.0):
            with self.subTest(budget=budget), self.assertRaises(SplitInputError) as caught:
                product_run_seconds(budget)
            self.assertEqual(str(caught.exception), "split_run_seconds_invalid")

    def test_exploration_block_matches_the_product_readme(self):
        text = (REPO_ROOT / "deploy/product/README.md").read_text(encoding="utf-8")
        block = text[text.index('"target_policy": { "exploration": {'):]
        block = block[:block.index("```")]
        self.assertEqual(dict(re.findall(r'"(\w+)": "([^"]+)"', block)), EXPLORATION_BLOCK)


class TestSyntheticInstall(unittest.TestCase):
    """합성 모델 모드 설치 입력(2026-11-18 사용자 결정 (a)): local_model 주소 핀은 스크립트 모델 주소·포트 하나만이다."""

    def setUp(self):
        self.defaults, _ = read_workflow_defaults()

    def build(self, **changes):
        options = dict(admin_password="x" * 24, manifest=None, meter_port=8080, workflow_defaults=self.defaults,
                       wide_writes=True, valid_for_hours=4, max_runs=2, run_seconds=2100, local_model=synthetic_local_model("scripted/m:v1", 32, 1024))
        options.update(changes)
        return install_input(**options)

    def test_local_model_pins_one_address_and_port(self):
        value = self.build()
        self.assertNotIn("openrouter_manifest", value)
        self.assertEqual(value["local_model"], {"model": "scripted/m:v1", "base_url": SCRIPTED_MODEL_BASE_URL,
                                                "addresses": [SCRIPTED_MODEL_IP], "context_window_k": 32, "max_tokens": 1024})
        self.assertEqual(value["target_policy"]["allowed_ports"], [8080])  # 대상 정책은 실제 모델 회차와 같다

    def test_other_pins_and_mixed_models_are_refused(self):
        good = synthetic_local_model("scripted/m:v1", 32, 1024)
        bad = [dict(good, addresses=[SCRIPTED_MODEL_IP, "10.252.2.31"]), dict(good, addresses=["10.252.2.31"]),
               dict(good, base_url="http://eval-scripted-model:8081/v1"), dict(good, base_url="http://other:8080/v1"),
               dict(good, extra=1), {k: v for k, v in good.items() if k != "max_tokens"}, dict(good, context_window_k=0),
               dict(good, model="")]
        for local in bad:
            with self.subTest(local=local), self.assertRaisesRegex(SplitInputError, "split_install_input_invalid"):
                self.build(local_model=local)
        with self.assertRaisesRegex(SplitInputError, "split_install_input_invalid"):
            self.build(manifest=MANIFEST)  # 둘 다 주면 거부
        with self.assertRaisesRegex(SplitInputError, "split_install_input_invalid"):
            self.build(local_model=None)  # 둘 다 없으면 거부
        for args in (("", 32, 1), ("m", 0, 1), ("m", 32, 0), ("m", True, 1)):
            with self.subTest(args=args), self.assertRaisesRegex(SplitInputError, "split_synthetic_model_invalid"):
                synthetic_local_model(*args)

    def test_external_probe_sample_is_the_product_model_api(self):
        # 여러 출발점 점검의 외부 견본은 제품이 고정한 모델 API 주소(llmpolicy.OpenRouterBaseURL)와 같다.
        go = (REPO_ROOT / "llmpolicy" / "openrouter.go").read_text(encoding="utf-8")
        self.assertEqual(re.search(r'const OpenRouterBaseURL = "([^"]+)"', go).group(1), PRODUCT_MODEL_API)


class TestTargetRegistration(unittest.TestCase):
    def test_wide_and_read_scopes(self):
        wide = target_registration(target_name="app.eval.test", meter_port=8080, requests=400, valid_hours=2, wide_writes=True)
        self.assertEqual(wide, {"url": "http://app.eval.test:8080/", "path_prefixes": ["/"], "methods": list(WIDE_METHODS),
                                "readonly_basis": REGISTRATION_BASIS, "requests": 400, "valid_hours": 2, "reversible_writes": True})
        read = target_registration(target_name="app.eval.test", meter_port=8080, requests=400, valid_hours=2, wide_writes=False)
        self.assertEqual((read["methods"], "reversible_writes" in read), (["GET", "HEAD"], False))
        self.assertTrue(20 <= len(REGISTRATION_BASIS) <= 1024)

    def test_wide_methods_are_the_product_registration_maximum(self):
        # 제품 등록 검사(diagnosisprep/product_contract.go)가 받는 메서드와 같다. 제품이 바뀌면 이 시험이 깨진다.
        source = (REPO_ROOT / "diagnosisprep/product_contract.go").read_text(encoding="utf-8")
        line = next(l for l in source.splitlines() if 'm != "GET" && m != "HEAD"' in l and "ReversibleWrites" in l)
        self.assertEqual(set(re.findall(r'"([A-Z]+)"', line)), set(WIDE_METHODS))
        self.assertIn("len(s.Methods) > 5", source)
        self.assertEqual(len(WIDE_METHODS), 5)

    def test_registration_inputs_are_checked(self):
        base = dict(target_name="app.eval.test", meter_port=8080, requests=400, valid_hours=2, wide_writes=True)
        for change in ({"target_name": "App.eval.test"}, {"target_name": "localhost"}, {"target_name": "10.0.0.1"},
                       {"target_name": "app.eval.test:80"}, {"target_name": "a_b.test"}, {"meter_port": 70000},
                       {"requests": 0}, {"valid_hours": 0}, {"wide_writes": "yes"}):
            with self.subTest(change=change), self.assertRaises(SplitInputError) as caught:
                target_registration(**dict(base, **change))
            self.assertEqual(str(caught.exception), "split_registration_invalid")



class TestSplitConditions(unittest.TestCase):
    def setUp(self):
        self.defaults, self.digest = read_workflow_defaults()

    def pair(self, wide_install, wide_registration):
        install = install_input(admin_password="x" * 24, manifest=MANIFEST, meter_port=8080, workflow_defaults=self.defaults,
                                wide_writes=wide_install, valid_for_hours=4, max_runs=2, run_seconds=2100)
        registration = target_registration(target_name="app.eval.test", meter_port=8080, requests=400, valid_hours=2,
                                           wide_writes=wide_registration)
        return install, registration

    def test_records_defaults_digest_workflow_and_scope(self):
        install, registration = self.pair(True, True)
        value = split_conditions(workflow_defaults=self.defaults, workflow_sha256=self.digest, install=install,
                                 registration=registration)
        self.assertEqual(value, {"workflow_defaults_sha256": self.digest, "workflow_defaults": self.defaults,
                                 "workflow": dict(self.defaults, allow_reversible_writes=True),
                                 "registration": {"path_prefixes": ["/"], "excluded_paths": [], "methods": list(WIDE_METHODS),
                                                  "reversible_writes": True, "requests": 400, "valid_hours": 2}})
        read_install, read_registration = self.pair(False, False)
        read = split_conditions(workflow_defaults=self.defaults, workflow_sha256=self.digest, install=read_install,
                                registration=read_registration)
        self.assertEqual((read["registration"]["methods"], read["registration"]["reversible_writes"]), (["GET", "HEAD"], False))

    def test_install_and_registration_must_agree(self):
        for wide_install, wide_registration in ((True, False), (False, True)):
            install, registration = self.pair(wide_install, wide_registration)
            with self.subTest(install=wide_install), self.assertRaises(SplitInputError):
                split_conditions(workflow_defaults=self.defaults, workflow_sha256=self.digest, install=install,
                                 registration=registration)
        install, registration = self.pair(True, True)
        install["target_policy"]["workflow"]["max_pages"] = 8  # 기본값 파일과 다른 워크플로
        with self.assertRaises(SplitInputError):
            split_conditions(workflow_defaults=self.defaults, workflow_sha256=self.digest, install=install,
                             registration=registration)


if __name__ == "__main__":
    unittest.main()
