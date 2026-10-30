"""검토된 비민감 선언의 합성 프로파일만 사용한다. 실제 계정·대상·모델은 사용하지 않는다."""

from __future__ import annotations

import copy
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from dataclasses import asdict
from decimal import Decimal, localcontext
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from profile_fingerprint import FORMAT, MAX_INPUT_BYTES, ProfileError, generate_fingerprints


def profile():
    return {
        "version": 1,
        "fixture": {
            "preparation_id": "read_fixture", "preparation_revision": 1,
            "accounts": [{"label": "owner", "role": "basic"}, {"label": "peer", "role": "basic"}],
            "resources": [
                {"label": "owned_record", "type": "record", "owner": "owner", "count": 1,
                 "shared": False, "delegated": False},
                {"label": "peer_record", "type": "record", "owner": "peer", "count": 2,
                 "shared": False, "delegated": False},
            ],
        },
        "environment": {
            "model_tag": "example-model:fixed-tag", "llm_base_url": "http://127.0.0.1:11434/v1",
            "temperature": 0, "seed": 42, "target_image": "example/target:15.0.0",
            "worker_count": 1, "time_budget_seconds": 1200, "request_budget": 100,
        },
        "scope": {"target_origin": "http://127.0.0.1:3000", "methods": ["GET", "HEAD"],
                  "paths": ["/api/records/{object}", "/api/status"], "vuln_classes": ["idor", "bola"]},
    }


class TestProfileFingerprint(unittest.TestCase):
    def reject(self, payload, expected):
        with self.assertRaises(ProfileError) as caught:
            generate_fingerprints(payload)
        self.assertIn(expected, str(caught.exception))
        self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))

    def test_v1_fixed_canonical_vectors(self):
        self.assertEqual(asdict(generate_fingerprints(profile())), {
            "format": "abc2lab-eval-profile-v1",
            "fixture_fingerprint": "6a8961b760c86d504d5b4dbb195667e0201ae81f19bf9d75df65b3d617d814c7",
            "environment_fingerprint": "a7e7b7f4d76df9cf2489eee85a9a3dfc17220788b03f2883e6d94ccdf08be2d9",
            "scope_fingerprint": "da1d4647a93b44dfedfd9418f7dd230d97691acea9fd0ca9a1e24eb7fbaf1f91",
        })

    def test_digest_fields_fit_current_preflight_without_changing_source_pins(self):
        from preflight import check_preflight

        payload = profile()
        fingerprints = asdict(generate_fingerprints(payload))
        controls = payload["environment"] | {"target_origin": payload["scope"]["target_origin"]}
        controls.update({key: value for key, value in fingerprints.items() if key.endswith("_fingerprint")})
        check_preflight({
            "pins": {"baseline_source_sha": "1" * 40, "target_image": controls["target_image"]},
            "baseline": controls | {"source_sha": "1" * 40},
            "ours": controls | {"source_sha": "2" * 40},
        })

    def test_valid_profile_returns_only_typed_digests_and_does_not_mutate_or_execute(self):
        payload = profile()
        before = copy.deepcopy(payload)
        with patch("socket.create_connection", side_effect=AssertionError("network")), \
                patch("subprocess.Popen", side_effect=AssertionError("process")), \
                patch("os.system", side_effect=AssertionError("shell")):
            result = generate_fingerprints(payload)
        self.assertEqual(result.format, FORMAT)
        self.assertEqual(payload, before)
        digests = [result.fixture_fingerprint, result.environment_fingerprint, result.scope_fingerprint]
        self.assertEqual(len(set(digests)), 3)
        self.assertTrue(all(re.fullmatch("[0-9a-f]{64}", digest) for digest in digests))
        text = json.dumps(asdict(result))
        self.assertNotIn("example-model", text)
        self.assertNotIn("127.0.0.1", text)

    def test_dictionary_and_set_array_order_do_not_affect_fingerprints(self):
        payload = profile()
        changed = json.loads(json.dumps(payload), object_pairs_hook=lambda pairs: dict(reversed(pairs)))
        for field in ("accounts", "resources"):
            changed["fixture"][field].reverse()
        for field in ("methods", "paths", "vuln_classes"):
            changed["scope"][field].reverse()
        self.assertEqual(generate_fingerprints(payload), generate_fingerprints(changed))

    def test_equivalent_numeric_representations_match_without_rounding_context(self):
        payload = profile()
        changed = copy.deepcopy(payload)
        changed["environment"].update(temperature=Decimal("-0.000"), time_budget_seconds=Decimal("1.200E+3"))
        self.assertEqual(generate_fingerprints(payload), generate_fingerprints(changed))
        payload["environment"].update(temperature=Decimal("0.1234567890123456789012345678901"))
        expected = generate_fingerprints(payload)
        with localcontext() as context:
            context.prec = 2
            self.assertEqual(expected, generate_fingerprints(payload))
        changed = copy.deepcopy(payload)
        changed["environment"]["temperature"] = Decimal("0.1234567890123456789012345678902")
        self.assertNotEqual(expected.environment_fingerprint, generate_fingerprints(changed).environment_fingerprint)

    def test_each_environment_condition_changes_only_environment_digest(self):
        expected = generate_fingerprints(profile())
        changes = {"model_tag": "other-model:fixed-tag", "llm_base_url": "http://localhost:11434/v1",
                   "temperature": 0.25, "seed": 43, "target_image": "example/target:16.0.0",
                   "worker_count": 2, "time_budget_seconds": 600, "request_budget": 101}
        for field, value in changes.items():
            payload = profile()
            payload["environment"][field] = value
            result = generate_fingerprints(payload)
            self.assertNotEqual(expected.environment_fingerprint, result.environment_fingerprint, field)
            self.assertEqual(expected.fixture_fingerprint, result.fixture_fingerprint)
            self.assertEqual(expected.scope_fingerprint, result.scope_fingerprint)

    def test_each_scope_condition_changes_scope_and_environment(self):
        expected = generate_fingerprints(profile())
        changes = {"target_origin": "http://127.0.0.1:3001", "methods": ["GET"],
                   "paths": ["/api/records/{object}"], "vuln_classes": ["idor"]}
        for field, value in changes.items():
            payload = profile()
            payload["scope"][field] = value
            result = generate_fingerprints(payload)
            self.assertNotEqual(expected.scope_fingerprint, result.scope_fingerprint, field)
            self.assertNotEqual(expected.environment_fingerprint, result.environment_fingerprint, field)
            self.assertEqual(expected.fixture_fingerprint, result.fixture_fingerprint)

    def test_recipe_role_and_resource_conditions_change_fixture(self):
        expected = generate_fingerprints(profile())
        changes = []
        for field, value in (("preparation_id", "other_recipe"), ("preparation_revision", 2)):
            payload = profile()
            payload["fixture"][field] = value
            changes.append(payload)
        payload = profile()
        payload["fixture"]["accounts"][1]["role"] = "privileged"
        changes.append(payload)
        for field, value in (("label", "other_record"), ("type", "document"), ("owner", "peer"),
                             ("count", 2), ("shared", True), ("delegated", True)):
            payload = profile()
            payload["fixture"]["resources"][0][field] = value
            changes.append(payload)
        for payload in changes:
            result = generate_fingerprints(payload)
            self.assertNotEqual(expected.fixture_fingerprint, result.fixture_fingerprint)
            self.assertEqual(expected.environment_fingerprint, result.environment_fingerprint)

    def test_explicit_anonymous_fixture_without_resources_is_supported(self):
        payload = profile()
        payload["fixture"].update(accounts=[], resources=[])
        self.assertRegex(generate_fingerprints(payload).fixture_fingerprint, "[0-9a-f]{64}")

    def test_missing_or_wrong_root_sections_and_versions_fail(self):
        for value in (None, True, 1, [], "SYNTHETIC-PRIVATE"):
            self.reject(value, "EXPECTED_OBJECT")
        for field in profile():
            payload = profile()
            del payload[field]
            self.reject(payload, "MISSING_FIELDS")
        for value in (None, True, 0, 2, "1"):
            self.reject(profile() | {"version": value}, "UNSUPPORTED_VERSION")
        for field in ("fixture", "environment", "scope"):
            payload = profile()
            payload[field] = []
            self.reject(payload, "EXPECTED_OBJECT")

    def test_missing_fields_at_each_schema_boundary_fail(self):
        for section in ("fixture", "environment", "scope"):
            for field in profile()[section]:
                payload = profile()
                del payload[section][field]
                self.reject(payload, "MISSING_FIELDS")
        for section in ("accounts", "resources"):
            for field in profile()["fixture"][section][0]:
                payload = profile()
                del payload["fixture"][section][0][field]
                self.reject(payload, "MISSING_FIELDS")

    def test_forbidden_metadata_and_private_fields_fail_before_any_hash(self):
        forbidden = ("password", "token", "cookie", "authorization", "username", "account_id", "canary",
                     "source_sha", "run_id", "reset_generation", "instance_id", "execution_id", "artifact_path",
                     "SYNTHETIC-PRIVATE-KEY")
        for section in (None, "fixture", "environment", "scope", "accounts", "resources"):
            for field in forbidden:
                payload = profile()
                if section is None:
                    owner = payload
                elif section in ("accounts", "resources"):
                    owner = payload["fixture"][section][0]
                else:
                    owner = payload[section]
                owner[field] = "SYNTHETIC-PRIVATE-VALUE"
                with patch("profile_fingerprint.hashlib.sha256", side_effect=AssertionError("hashed invalid data")):
                    self.reject(payload, "UNKNOWN_FIELDS")

    def test_bad_accounts_resources_and_reference_links_are_rejected(self):
        for field in ("accounts", "resources"):
            for value in (None, {}, [None]):
                payload = profile()
                payload["fixture"][field] = value
                self.reject(payload, "EXPECTED_")
            payload = profile()
            payload["fixture"][field].append(payload["fixture"][field][0].copy())
            self.reject(payload, "DUPLICATE")
        payload = profile()
        payload["fixture"]["resources"][0]["owner"] = "unknown"
        self.reject(payload, "UNKNOWN_ACCOUNT_LABEL")
        payload["fixture"]["resources"][0]["owner"] = "owner@example.invalid"
        self.reject(payload, "EXPECTED_LOGICAL_LABEL")
        for field in ("shared", "delegated"):
            for value in (None, 0, "false"):
                payload = profile()
                payload["fixture"]["resources"][0][field] = value
                self.reject(payload, "EXPECTED_BOOL")

    def test_logical_labels_exclude_original_account_urls_and_control_characters(self):
        for value in (None, 1, "", "user@example.invalid", "account/123", "http://target.invalid", "with space",
                      "UPPERCASE", "x" * 65, "bad\nlabel", "한글"):
            payload = profile()
            payload["fixture"]["accounts"][0]["label"] = value
            self.reject(payload, "EXPECTED_LOGICAL_LABEL")

    def test_scope_arrays_must_be_nonempty_valid_and_unique(self):
        for field in ("methods", "paths", "vuln_classes"):
            for value in (None, [], {}, "GET"):
                payload = profile()
                payload["scope"][field] = value
                self.reject(payload, "EXPECTED_NONEMPTY_ARRAY")
            payload = profile()
            payload["scope"][field].append(payload["scope"][field][0])
            self.reject(payload, "DUPLICATE")
        for value in (None, [], "get", "CONNECT", "SYNTHETIC-PRIVATE"):
            payload = profile()
            payload["scope"]["methods"] = [value]
            self.reject(payload, "INVALID_METHOD")

    def test_paths_are_explicit_templates_without_query_credentials_or_traversal(self):
        for value in (None, "", "relative", "//host/path", "/x?token=SYNTHETIC-PRIVATE", "/x#fragment",
                      "/x%2fy", "/../x", "/x/./y", "/x\\y", "/x/*", "/x/{BAD}", "/x/{id}.json",
                      "/x//y", "/x//", "/x\n", "/x/" + "a" * 512):
            payload = profile()
            payload["scope"]["paths"] = [value]
            self.reject(payload, "INVALID_PATH_TEMPLATE")
        payload = profile()
        payload["scope"]["paths"] = ["/", "/v1/items/{item}/", "/static-name.json"]
        generate_fingerprints(payload)

    def test_url_literals_are_preserved_and_unsupported_or_private_urls_rejected(self):
        for section, field in (("scope", "target_origin"), ("environment", "llm_base_url")):
            for value in (None, "", "http://example.invalid", "http://192.0.2.1", "http://127.1",
                          "file:///tmp/data", "http://user:SYNTHETIC-PRIVATE@127.0.0.1", "http://127.0.0.1?",
                          "http://127.0.0.1#", "http://[::1%zone]", "http://127.0.0.1:0", "http://127.0.0.1:65536",
                          "http://127.0.0.1/../v1", "http://127.0.0.1//v1", "http://127.0.0.1\n"):
                payload = profile()
                payload[section][field] = value
                self.reject(payload, "INVALID_LOCAL_URL")
        payload = profile()
        payload["scope"]["target_origin"] = "https://[::1]:3000/"
        payload["environment"]["llm_base_url"] = "http://localhost:11434/v1/"
        generate_fingerprints(payload)
        payload["scope"]["target_origin"] += "extra"
        self.reject(payload, "INVALID_LOCAL_URL")

    def test_model_and_image_are_explicit_non_latest_references(self):
        for field in ("model_tag", "target_image"):
            for value in (None, "", "example", "example:latest", "example:LATEST", "example:TODO", "<model>", "example:with space"):
                payload = profile()
                payload["environment"][field] = value
                self.reject(payload, "EXPECTED_PINNED_REFERENCE")
        for image in ("example:latest@sha256:" + "a" * 64, "example@sha256:" + "a" * 63):
            payload = profile()
            payload["environment"]["target_image"] = image
            self.reject(payload, "EXPECTED_PINNED_REFERENCE")
        payload = profile()
        payload["environment"]["target_image"] = "registry.example:5000/target:15.0.0@sha256:" + "a" * 64
        generate_fingerprints(payload)

    def test_integer_fields_reject_bool_float_negative_and_out_of_range(self):
        for field in ("seed", "worker_count", "request_budget"):
            for value in (None, True, "1", -1, 1.0, 1 << 63):
                payload = profile()
                payload["environment"][field] = value
                self.reject(payload, "INVALID_INTEGER")
        for path in ("preparation_revision", "count"):
            payload = profile()
            owner = payload["fixture"] if path == "preparation_revision" else payload["fixture"]["resources"][0]
            owner[path] = 0
            self.reject(payload, "INVALID_INTEGER")
        payload = profile()
        payload["environment"]["seed"] = 0
        generate_fingerprints(payload)

    def test_numeric_fields_reject_unknown_nonfinite_and_excessive_precision(self):
        for field in ("temperature", "time_budget_seconds"):
            for value in (None, True, "0.1", -1, float("nan"), float("inf"), Decimal("NaN"),
                          Decimal("1e9999"), Decimal("1e-9999"), Decimal("0." + "1" * 129)):
                payload = profile()
                payload["environment"][field] = value
                self.reject(payload, ": ")
        payload = profile()
        payload["environment"]["time_budget_seconds"] = 0
        self.reject(payload, "INVALID_NUMBER")


class TestProfileCLI(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "profile.json"
        self.path.write_text(json.dumps(profile()), encoding="utf-8")

    def cli(self, *extra, include_profile=True):
        script = Path(__file__).resolve().parents[1] / "profile_fingerprint.py"
        arguments = [sys.executable, "-I", "-B", str(script)]
        if include_profile:
            arguments.append(str(self.path))
        return subprocess.run([*arguments, *extra],
                              capture_output=True, text=True, check=False)

    def test_invalid_cli_arguments_never_echo_values(self):
        cases = [self.cli("--token=SYNTHETIC-PRIVATE"), self.cli("SYNTHETIC-PRIVATE"), self.cli(include_profile=False)]
        for result in cases:
            self.assertEqual((result.returncode, result.stdout, result.stderr), (2, "", "cli: INVALID_ARGUMENTS\n"))

    def test_cli_only_outputs_digests_and_does_not_write_or_modify_files(self):
        before = self.path.read_bytes()
        result = self.cli()
        self.assertEqual((result.returncode, result.stderr), (0, ""))
        self.assertEqual(json.loads(result.stdout), asdict(generate_fingerprints(profile())))
        self.assertNotIn("example-model", result.stdout)
        self.assertNotIn("127.0.0.1", result.stdout)
        self.assertEqual(self.path.read_bytes(), before)
        self.assertEqual(list(self.path.parent.iterdir()), [self.path])

    def test_cli_retains_decimal_precision_beyond_float_precision(self):
        text = self.path.read_text(encoding="utf-8")
        self.path.write_text(text.replace('"temperature": 0', '"temperature": 0.1234567890123456789012345678901'), encoding="utf-8")
        expected = profile()
        expected["environment"]["temperature"] = Decimal("0.1234567890123456789012345678901")
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), asdict(generate_fingerprints(expected)))

    def test_cli_never_prints_partial_digests_or_private_input_on_failure(self):
        payload = profile()
        payload["environment"]["SYNTHETIC-PRIVATE-KEY"] = "SYNTHETIC-PRIVATE-VALUE"
        self.path.write_text(json.dumps(payload), encoding="utf-8")
        result = self.cli()
        self.assertEqual((result.returncode, result.stdout), (2, ""))
        self.assertEqual(result.stderr, "environment: UNKNOWN_FIELDS\n")

    def test_duplicate_nonfinite_invalid_utf8_or_malformed_json_is_sanitized(self):
        for raw in (b'{"SYNTHETIC-PRIVATE":1,"SYNTHETIC-PRIVATE":2}', b'{"x":NaN}', b'{"x":Infinity}',
                    b"\xff", b'{"SYNTHETIC-PRIVATE":', b"[" * 2000 + b"]" * 2000):
            self.path.write_bytes(raw)
            result = self.cli()
            self.assertEqual((result.returncode, result.stdout), (2, ""))
            self.assertNotIn("SYNTHETIC-PRIVATE", result.stderr)
            self.assertNotIn("Traceback", result.stderr)

    def test_size_limit_and_missing_files_return_sanitized_exit_two(self):
        before = self.path.read_bytes()
        self.path.write_bytes(before + b" " * (MAX_INPUT_BYTES - len(before)))
        self.assertEqual(self.cli().returncode, 0)
        with self.path.open("ab") as stream:
            stream.write(b" ")
        result = self.cli()
        self.assertEqual((result.returncode, result.stdout, result.stderr), (2, "", "json: TOO_LARGE\n"))
        self.path.unlink()
        result = self.cli()
        self.assertEqual((result.returncode, result.stdout, result.stderr), (2, "", "json: READ_FAILED\n"))


if __name__ == "__main__":
    unittest.main()
