"""오프라인 설정 선언 검사. 대상 앱·모델·회차 원자료를 사용하지 않는다."""

from __future__ import annotations

import copy
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from preflight import MAX_CONFIG_BYTES, PreflightError, check_preflight, load_preflight


def api_llm():
    return {"backend": "api", "base_url": "https://llm-api.example/api/v1", "model": "example-org/example-model",
            "provider": "example-provider", "fallbacks": False}


def local_llm():
    return {"backend": "local", "base_url": "http://llm-host:11434/v1", "model": "example-model:fixed-tag",
            "hostname": "llm-host", "host_ip": "172.17.0.1"}


def configuration():
    shared = {
        "source_sha": "1" * 40,
        "target_image": "example/target:15.0.0",
        "network_mode": "dedicated_network",
        "llm": api_llm(),
        "temperature": 0,
        "seed": 42,
        "target_origin": "http://eval-meter:8080",
        "worker_count": 1,
        "time_budget_seconds": 1200,
        "request_budget": 100,
        "fixture_fingerprint": "a" * 64,
        "environment_fingerprint": "b" * 64,
        "scope_fingerprint": "c" * 64,
    }
    return {
        "pins": {"baseline_source_sha": shared["source_sha"], "target_image": shared["target_image"]},
        "baseline": shared.copy(),
        "ours": shared | {"source_sha": "2" * 40},
    }


class TestCheckPreflight(unittest.TestCase):
    def reject(self, payload, expected):
        with self.assertRaises(PreflightError) as caught:
            check_preflight(payload)
        self.assertIn(expected, caught.exception.errors)

    def test_matching_controls_and_distinct_sources_pass_without_mutating_input(self):
        payload = configuration()
        original = copy.deepcopy(payload)
        check_preflight(payload)
        self.assertEqual(payload, original)

    def test_same_source_sha_is_allowed_but_proves_no_engine_difference_or_valid_comparison(self):
        payload = configuration()
        payload["ours"]["source_sha"] = payload["baseline"]["source_sha"]
        check_preflight(payload)

    def test_every_required_field_must_be_explicit(self):
        for section in ("pins", "baseline", "ours"):
            for field in configuration()[section]:
                with self.subTest(section=section, field=field):
                    payload = configuration()
                    del payload[section][field]
                    self.reject(payload, f"{section}.{field}: REQUIRED")

    def test_invalid_root_and_section_shapes_fail(self):
        for value in (None, [], 1, "SYNTHETIC-PRIVATE"):
            self.reject(value, "config: EXPECTED_OBJECT")
            for section in ("pins", "baseline", "ours"):
                payload = configuration()
                payload[section] = value
                self.reject(payload, f"{section}: EXPECTED_OBJECT")
        for section in ("pins", "baseline", "ours"):
            payload = configuration()
            del payload[section]
            self.reject(payload, f"{section}: EXPECTED_OBJECT")

    def test_unknown_fields_are_rejected_at_every_level_without_echoing_names(self):
        for section in (None, "pins", "baseline", "ours"):
            payload = configuration()
            owner = payload if section is None else payload[section]
            owner["SYNTHETIC-PRIVATE-KEY"] = "SYNTHETIC-PRIVATE-VALUE"
            with self.assertRaises(PreflightError) as caught:
                check_preflight(payload)
            self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))
            self.assertIn(f"{section or 'config'}: UNKNOWN_FIELDS", caught.exception.errors)

    def test_all_shared_conditions_must_match(self):
        changes = {
            "llm": api_llm() | {"provider": "other-provider"},
            "temperature": 0.2, "seed": 43, "target_origin": "http://eval-meter:8081",
            "worker_count": 2, "time_budget_seconds": 600, "request_budget": 200,
            "fixture_fingerprint": "d" * 64, "environment_fingerprint": "e" * 64,
            "scope_fingerprint": "f" * 64,
        }
        for field, value in changes.items():
            with self.subTest(field=field):
                payload = configuration()
                payload["ours"][field] = value
                self.reject(payload, f"comparison.{field}: MISMATCH")

    def test_source_and_target_must_match_agreed_pins_even_if_runs_agree(self):
        payload = configuration()
        payload["baseline"]["source_sha"] = "3" * 40
        self.reject(payload, "baseline.source_sha: PIN_MISMATCH")
        for section in ("baseline", "ours"):
            payload[section]["target_image"] = "example/target:16.0.0"
        self.reject(payload, "baseline.target_image: PIN_MISMATCH")
        self.reject(payload, "ours.target_image: PIN_MISMATCH")

    def test_sha_and_fingerprint_formats_reject_short_or_placeholder_values(self):
        for section in ("pins", "baseline", "ours"):
            field = "baseline_source_sha" if section == "pins" else "source_sha"
            for value in (None, "", "1234567", "g" * 40, "TODO", 1):
                payload = configuration()
                payload[section][field] = value
                self.reject(payload, f"{section}.{field}: INVALID")
        for field in ("fixture_fingerprint", "environment_fingerprint", "scope_fingerprint"):
            for value in (None, "", "f" * 63, "z" * 64, "TODO"):
                payload = configuration()
                payload["ours"][field] = value
                self.reject(payload, f"ours.{field}: INVALID")

    def test_mutable_or_unset_image_references_fail(self):
        for value in (None, "", "example", "example:latest", "example:LATEST", "example:TODO",
                      "<fixed-tag>", "example:${TAG}", "example:tag with spaces", "example:tag\n"):
            payload = configuration()
            payload["ours"]["target_image"] = value
            self.reject(payload, "ours.target_image: INVALID")
        payload = configuration()
        payload["pins"]["target_image"] = "example:latest"
        self.reject(payload, "pins.target_image: INVALID")

    def test_image_digest_and_local_model_namespace_are_supported(self):
        payload = configuration()
        image = "registry.example:5000/team/target@sha256:" + "a" * 64
        payload["pins"]["target_image"] = image
        for section in ("baseline", "ours"):
            payload[section]["target_image"] = image
        check_preflight(payload)

    def test_latest_tag_cannot_hide_behind_image_digest(self):
        for tag in ("latest", "LATEST", "TODO"):
            payload = configuration()
            image = f"example/target:{tag}@sha256:" + "a" * 64
            payload["pins"]["target_image"] = image
            for section in ("baseline", "ours"):
                payload[section]["target_image"] = image
            self.reject(payload, "pins.target_image: INVALID")
            self.reject(payload, "baseline.target_image: INVALID")
            self.reject(payload, "ours.target_image: INVALID")

    def test_version_tag_with_digest_passes_but_malformed_digest_fails(self):
        payload = configuration()
        image = "example/target:15.0.0@sha256:" + "a" * 64
        payload["pins"]["target_image"] = image
        for section in ("baseline", "ours"):
            payload[section]["target_image"] = image
        check_preflight(payload)
        for image in ("example/target@sha256:" + "a" * 63,
                      "example/target@sha256:" + "z" * 64,
                      "example/target:@sha256:" + "a" * 64,
                      "example/target@@sha256:" + "a" * 64):
            payload["ours"]["target_image"] = image
            self.reject(payload, "ours.target_image: INVALID")

    def test_target_origin_is_the_meter_service_origin_only(self):
        invalid = (None, "", "http://127.0.0.1:3000", "http://localhost:3000", "http://192.0.2.1:8080",
                   "https://eval-meter:8080", "http://eval-meter", "http://eval-meter:8080/v1",
                   "http://eval-meter.example:8080", "http://Eval-Meter:8080", "http://eval_meter:8080",
                   "http://8080:8080", "http://user:SYNTHETIC-PRIVATE@eval-meter:8080",
                   "http://eval-meter:8080?token=SYNTHETIC-PRIVATE", "http://eval-meter:8080#f",
                   "http://eval-meter:0", "http://eval-meter:65536", "http://eval-meter:8080\n",
                   "http://[::1]:8080", "file:///tmp/config", "http://eval-meter:8080" + "/" * 600)
        for value in invalid:
            payload = configuration()
            payload["ours"]["target_origin"] = value
            self.reject(payload, "ours.target_origin: INVALID")
        payload = configuration()
        for section in ("baseline", "ours"):
            payload[section]["target_origin"] = "http://eval-meter:8080/"
        check_preflight(payload)

    def test_only_dedicated_network_mode_is_supported(self):
        for value in (None, "", "loopback_only", "DEDICATED_NETWORK", 1, ["dedicated_network"]):
            payload = configuration()
            payload["ours"]["network_mode"] = value
            self.reject(payload, "ours.network_mode: INVALID")

    def test_api_provider_may_pin_one_endpoint_variant(self):
        # OpenRouter 엔드포인트 tag(`slug/변형`, 예: deepinfra/fp8)는 그 정밀도 엔드포인트 하나로 고정한다.
        for provider in ("deepinfra", "deepinfra/fp8", "atlas-cloud/fp8", "deepinfra/turbo", "a.b_c-d/e.f_g-h"):
            with self.subTest(provider=provider):
                payload = configuration()
                for side in ("baseline", "ours"):
                    payload[side]["llm"] = api_llm() | {"provider": provider}
                check_preflight(payload)
        payload = configuration()
        payload["ours"]["llm"] = api_llm() | {"provider": "example-provider/fp8"}  # 변형만 달라도 다른 조건
        self.reject(payload, "comparison.llm: MISMATCH")

    def test_api_backend_requires_pinned_model_provider_and_disabled_fallbacks(self):
        invalid = [
            api_llm() | {"base_url": "http://llm-api.example/api/v1"},
            api_llm() | {"base_url": "https://203.0.113.5/api/v1"},
            api_llm() | {"base_url": "https://localhost/api/v1"},
            api_llm() | {"base_url": "https://user:SYNTHETIC-PRIVATE@llm-api.example/v1"},
            api_llm() | {"base_url": "https://llm-api.example/v1?key=SYNTHETIC-PRIVATE"},
            api_llm() | {"base_url": "https://llm-api.example/../v1"},
            api_llm() | {"model": "example-model"},
            api_llm() | {"model": "example-org/latest"},
            api_llm() | {"model": "example-org/example-model:latest"},
            api_llm() | {"model": "example-org/todo"},
            api_llm() | {"model": "example-org/<model>"},
            api_llm() | {"provider": ""},
            api_llm() | {"provider": "Example-Provider"},
            api_llm() | {"provider": "tbd"},
            api_llm() | {"provider": None},
            # 엔드포인트 변형은 한 단계만: 빈 변형·두 단계·대문자·공백·변형만은 거부
            api_llm() | {"provider": "deepinfra/"}, api_llm() | {"provider": "deepinfra/fp8/turbo"},
            api_llm() | {"provider": "deepinfra/FP8"}, api_llm() | {"provider": "/fp8"},
            api_llm() | {"provider": "deepinfra /fp8"}, api_llm() | {"provider": "deepinfra/todo"},
            api_llm() | {"fallbacks": True},
            api_llm() | {"fallbacks": None},
            api_llm() | {"fallbacks": 0},
            api_llm() | {"fallbacks": "false"},
            {key: value for key, value in api_llm().items() if key != "provider"},
            {key: value for key, value in api_llm().items() if key != "fallbacks"},
            api_llm() | {"hostname": "llm-host"},
            {key: value for key, value in api_llm().items() if key != "backend"},
            api_llm() | {"backend": "API"},
            api_llm() | {"backend": ["api"]},
            api_llm() | {"backend": None},
            None, "https://llm-api.example/api/v1", [],
        ]
        for value in invalid:
            with self.subTest(value=value):
                payload = configuration()
                payload["ours"]["llm"] = value
                self.reject(payload, "ours.llm: INVALID")
        payload = configuration()
        for section in ("baseline", "ours"):
            payload[section]["llm"] = api_llm() | {"model": "example-org/example-model:variant",
                                                   "base_url": "https://llm-api.example:8443/"}
        check_preflight(payload)

    def test_local_backend_requires_fixed_private_ip_and_matching_hostname(self):
        invalid = [
            local_llm() | {"base_url": "http://other-host:11434/v1"},
            local_llm() | {"base_url": "http://172.17.0.1:11434/v1"},
            local_llm() | {"hostname": "localhost", "base_url": "http://localhost:11434/v1"},
            local_llm() | {"host_ip": "127.0.0.1"},
            local_llm() | {"host_ip": "0.0.0.0"},
            local_llm() | {"host_ip": "169.254.1.1"},
            local_llm() | {"host_ip": "203.0.113.5"},
            local_llm() | {"host_ip": "224.0.0.1"},
            local_llm() | {"host_ip": "172.017.0.1"},
            local_llm() | {"host_ip": "fe80::1"},
            local_llm() | {"host_ip": 1},
            local_llm() | {"model": "example-model:latest"},
            local_llm() | {"model": "example-model"},
            local_llm() | {"provider": "example-provider"},
            {key: value for key, value in local_llm().items() if key != "host_ip"},
        ]
        for value in invalid:
            with self.subTest(value=value):
                payload = configuration()
                payload["ours"]["llm"] = value
                self.reject(payload, "ours.llm: INVALID")
        payload = configuration()
        for section in ("baseline", "ours"):
            payload[section]["llm"] = local_llm()
        check_preflight(payload)
        payload["ours"]["llm"] = local_llm() | {"host_ip": "fd00::1"}
        self.reject(payload, "comparison.llm: MISMATCH")

    def test_each_url_guard_rejects_on_its_own(self):
        # 다른 검사에 먼저 걸리지 않게, 해당 가드 하나만 어기는 입력을 쓴다.
        long_path = "/" + "a" * 600
        cases = (
            ("target_origin", "http://user:secret@eval-meter:8080"),
            ("llm", api_llm() | {"base_url": "https://user:secret@llm-api.example/v1"}),
            ("llm", api_llm() | {"base_url": "https://llm-api.example" + long_path}),
            ("llm", local_llm() | {"host_ip": "fd00:0::1"}),
            ("llm", local_llm() | {"host_ip": "FD00::1"}),
        )
        for field, value in cases:
            with self.subTest(field=field, value=value):
                payload = configuration()
                payload["ours"][field] = value
                self.reject(payload, f"ours.{field}: INVALID")
        payload = configuration()
        for section in ("baseline", "ours"):
            payload[section]["llm"] = local_llm() | {"host_ip": "fd00::1"}
        check_preflight(payload)

    def test_backend_kind_must_match_between_baseline_and_ours(self):
        payload = configuration()
        payload["ours"]["llm"] = local_llm()
        self.reject(payload, "comparison.llm: MISMATCH")

    def test_unknown_sampling_values_and_boolean_or_nonfinite_numbers_fail(self):
        values = {
            "temperature": (None, True, "0", -0.1, float("nan"), float("inf"), 10**1000),
            "seed": (None, True, "42", -1, 1.5),
            "time_budget_seconds": (None, False, "1200", 0, -1, float("nan"), float("inf"), 10**1000),
            "request_budget": (None, False, "100", 0, -1, 1.5),
            "worker_count": (None, True, "1", 0, -1, 1.5),
        }
        for field, invalid in values.items():
            for value in invalid:
                payload = configuration()
                payload["ours"][field] = value
                self.reject(payload, f"ours.{field}: INVALID")

    def test_numeric_zero_sampling_and_equal_float_values_pass(self):
        payload = configuration()
        payload["baseline"]["seed"] = payload["ours"]["seed"] = 0
        payload["ours"]["temperature"] = 0.0
        payload["ours"]["time_budget_seconds"] = 1200.0
        check_preflight(payload)

    def test_multiple_errors_have_deterministic_fixed_paths_and_no_input_values(self):
        payload = configuration()
        payload["ours"].update(llm=api_llm() | {"model": "SYNTHETIC-PRIVATE"},
                               target_origin="https://SYNTHETIC-PRIVATE.invalid")
        with self.assertRaises(PreflightError) as caught:
            check_preflight(payload)
        self.assertEqual(caught.exception.errors, ("ours.llm: INVALID", "ours.target_origin: INVALID"))
        self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))


class TestLoadAndCLI(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "preflight.json"
        self.path.write_text(json.dumps(configuration()), encoding="utf-8")

    def cli(self):
        script = Path(__file__).resolve().parents[1] / "preflight.py"
        return subprocess.run([sys.executable, "-I", "-B", str(script), str(self.path)],
                              text=True, capture_output=True, check=False)

    def test_reading_and_validation_do_not_send_execute_or_write(self):
        before = self.path.read_bytes()
        with patch("socket.create_connection", side_effect=AssertionError("network")), \
                patch("subprocess.Popen", side_effect=AssertionError("process")), \
                patch("os.system", side_effect=AssertionError("shell")):
            check_preflight(load_preflight(self.path))
        self.assertEqual(self.path.read_bytes(), before)
        self.assertEqual(list(self.path.parent.iterdir()), [self.path])

    def test_duplicate_keys_nonfinite_utf8_invalid_or_deep_json_rejected_safely(self):
        for raw in (b'{"secret":"SYNTHETIC-PRIVATE","secret":null}', b'{"x":NaN}', b'{"x":Infinity}',
                    b"\xff", b'{"SYNTHETIC-PRIVATE":', b"[" * 2000 + b"]" * 2000):
            self.path.write_bytes(raw)
            with self.assertRaises(PreflightError) as caught:
                check_preflight(load_preflight(self.path))
            self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))

    def test_duplicate_valid_seed_is_rejected_before_last_value_wins(self):
        payload = configuration()
        sections = {
            name: json.dumps(payload[name], separators=(",", ":"))
            for name in ("pins", "baseline")
        }
        ours = json.dumps(payload["ours"], separators=(",", ":"))
        ours = ours.replace(
            '"seed":42',
            '"seed":43,"seed":42',
            1,
        )
        self.path.write_text(
            '{"pins":' + sections["pins"] + ',"baseline":' + sections["baseline"] + ',"ours":' + ours + '}',
            encoding="utf-8",
        )
        with self.assertRaises(PreflightError) as caught:
            load_preflight(self.path)
        self.assertEqual(caught.exception.errors, ("config: DUPLICATE_KEY",))

    def test_size_limit_and_unreadable_file_report_sanitized_errors(self):
        raw = self.path.read_bytes()
        self.path.write_bytes(raw + b" " * (MAX_CONFIG_BYTES - len(raw)))
        check_preflight(load_preflight(self.path))
        with self.path.open("ab") as stream:
            stream.write(b" ")
        with self.assertRaisesRegex(PreflightError, "TOO_LARGE"):
            load_preflight(self.path)
        self.path.unlink()
        with self.assertRaisesRegex(PreflightError, "READ_FAILED"):
            load_preflight(self.path)

    def test_cli_success_has_no_values_and_exit_zero(self):
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        self.assertIn("검사 통과", result.stdout)
        self.assertNotIn("example-model", result.stdout)
        self.assertNotIn("llm-api.example", result.stdout)

    def test_cli_failure_exits_two_without_success_output_or_private_data(self):
        payload = configuration()
        payload["ours"]["llm"] = api_llm() | {"base_url": "https://user:SYNTHETIC-PRIVATE@llm-api.example/v1"}
        self.path.write_text(json.dumps(payload), encoding="utf-8")
        result = self.cli()
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("ours.llm: INVALID", result.stderr)
        self.assertNotIn("SYNTHETIC-PRIVATE", result.stderr)

    def test_cli_missing_file_exits_two_without_traceback(self):
        self.path.unlink()
        result = self.cli()
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertEqual(result.stderr, "config: READ_FAILED\n")


if __name__ == "__main__":
    unittest.main()
