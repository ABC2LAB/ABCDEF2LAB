"""탐색 분리 배치 평가 회차의 주소·경계 설정(결정안 §2·§3 B(a), 4-7a) 시험."""

from __future__ import annotations

import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_split import (  # noqa: E402
    BOUNDARY_EVIDENCE_DIR, METER_EXPLORE_IP, SplitInputError, boundary_config, read_boundary_template, to_eval_address,
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


if __name__ == "__main__":
    unittest.main()
