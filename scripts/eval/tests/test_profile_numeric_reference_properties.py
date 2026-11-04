"""고정 reference·숫자의 프로파일/사전검사 계약 차이를 보존하는 bounded fuzz."""

from __future__ import annotations

import copy
import hashlib
import json
import os
import sys
import unittest
from dataclasses import asdict
from decimal import Decimal, localcontext
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import preflight
import profile_fingerprint as fingerprint
from test_profile_fingerprint import profile


HEX = "a" * 64
MARKER = "SYNTHETIC-PRIVATE-NUMERIC-MARKER"
INT64 = (1 << 63) - 1


def digest(kind, value):
    wire = json.dumps({"format": fingerprint.FORMAT, "kind": kind, "profile": value},
                      sort_keys=True, separators=(",", ":"), ensure_ascii=True)
    return hashlib.new("sha256", wire.encode("ascii")).hexdigest()


def expected_environment(payload, numbers=None):
    raw = payload["scope"]
    scope = {"target_origin": raw["target_origin"], "methods": sorted(raw["methods"]),
             "paths": sorted(raw["paths"]), "vuln_classes": sorted(raw["vuln_classes"])}
    scope_digest = digest("scope", scope)
    # Canonical numbers are supplied as literal expectations by the case table, not normalized here.
    environment = dict(payload["environment"], temperature="0", time_budget_seconds="1200",
                       scope_fingerprint=scope_digest)
    environment.update(numbers or {})
    return scope_digest, digest("environment", environment)


class TestProfileNumericReferenceProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "socket.getaddrinfo",
                       "subprocess.Popen", "os.system", "builtins.open"):
            guard = patch(target, side_effect=AssertionError("declarations are offline"))
            guard.start()
            self.addCleanup(guard.stop)

    def generate(self, payload):
        before = repr(payload)  # NaN/sNaN-safe: no numeric equality or signaling comparisons.
        try:
            return fingerprint.generate_fingerprints(payload)
        finally:
            self.assertEqual(repr(payload), before)

    def assert_canonical(self, payload, numbers=None, result=None):
        result = self.generate(payload) if result is None else result
        expected_scope, expected_env = expected_environment(payload, numbers)
        self.assertEqual(result.scope_fingerprint, expected_scope)
        self.assertEqual(result.environment_fingerprint, expected_env)
        return result

    def reject_profile(self, payload, field, code):
        with self.assertRaises(fingerprint.ProfileError) as caught:
            self.generate(payload)
        self.assertEqual(str(caught.exception), f"environment.{field}: {code}")
        self.assertNotIn(MARKER, str(caught.exception))

    def config(self):
        payload = profile()
        result = self.assert_canonical(payload)
        controls = dict(payload["environment"], target_origin=payload["scope"]["target_origin"],
                        source_sha="1" * 40)
        controls.update({key: value for key, value in asdict(result).items() if key.endswith("_fingerprint")})
        return {"pins": {"baseline_source_sha": "1" * 40, "target_image": controls["target_image"]},
                "baseline": copy.deepcopy(controls), "ours": copy.deepcopy(controls)}

    def check_preflight(self, field, value, valid):
        self.assertIs(preflight._valid(field, value), valid)
        config = self.config()
        config["baseline"][field] = config["ours"][field] = value
        expected = [f"baseline.{field}: INVALID", f"ours.{field}: INVALID"]
        if field == "target_image":
            config["pins"][field] = value
            expected.insert(0, "pins.target_image: INVALID")
        before = repr(config)
        try:
            if valid:
                self.assertIsNone(preflight.check_preflight(config))
            else:
                with self.assertRaises(preflight.PreflightError) as caught:
                    preflight.check_preflight(config)
                self.assertEqual(set(caught.exception.errors), set(expected))
                self.assertEqual(len(caught.exception.errors), len(expected))
                self.assertNotIn(MARKER, str(caught.exception))
        finally:
            self.assertEqual(repr(config), before)

    def test_tag_digest_and_placeholder_reference_matrix_has_explicit_policy(self):
        cases = []
        # v2에서 모델 참조는 llm.model 규칙(백엔드별, test_preflight·test_profile_fingerprint)으로 옮겨 갔다.
        for field in ("target_image",):
            cases.extend((field, value, True, True) for value in
                         ("repo:fixed-tag", "registry.example:5000/repo:v1", "repo:latestish", "repo:todoish"))
            cases.extend((field, "repo:" + tag, False, False) for tag in
                         ("latest", "LATEST", "LaTeSt", "todo", "TODO", "tbd", "ChangeMe", "placeholder"))
            cases.extend((field, value, False, False) for value in
                         (None, True, 1, "", MARKER, "repo:with space", "한글:v1", "repo:v1\n"))
            for size in (255, 256, 257):
                cases.append((field, "x" * (size - 2) + ":t", size <= 256, True))
        for length in (63, 64, 65):
            for tagged in (False, True):
                name = "repo:v1" if tagged else "repo"
                value = name + "@sha256:" + "a" * length
                cases.append(("target_image", value, length == 64, length == 64))
        cases.extend((
            ("target_image", "repo:latest@sha256:" + HEX, False, False),
            ("target_image", "repo:LaTeSt@sha256:" + HEX, False, False),
            ("target_image", "repo@sha256:" + "A" * 64, False, False),
            ("target_image", "repo/placeholder:v1@sha256:" + HEX, False, False),
        ))
        for index, (field, value, profile_valid, preflight_valid) in enumerate(cases):
            with self.subTest(case=index, field=field):
                payload = profile()
                payload["environment"][field] = value
                if profile_valid:
                    self.assert_canonical(payload)
                else:
                    self.reject_profile(payload, field, "EXPECTED_PINNED_REFERENCE")
                self.check_preflight(field, value, preflight_valid)

    def test_integer_field_matrix_preserves_type_and_range_policy_differences(self):
        # Expected columns: profile positive / profile seed / preflight positive / preflight seed.
        cases = (
            ("zero", 0, False, True, False, True), ("one", 1, True, True, True, True),
            ("max-1", INT64 - 1, True, True, True, True), ("max", INT64, True, True, True, True),
            ("max+1", INT64 + 1, False, False, True, True),
            ("large", 1 << 100, False, False, True, True),
            ("true", True, False, False, False, False), ("false", False, False, False, False, False),
            ("float", 1.0, False, False, False, False), ("negative", -1, False, False, False, False),
            ("string", "1", False, False, False, False), ("null", None, False, False, False, False),
        )
        for field in ("seed", "worker_count", "request_budget"):
            for name, value, positive, seed, preflight_positive, preflight_seed in cases:
                with self.subTest(field=field, case=name):
                    payload = profile()
                    payload["environment"][field] = value
                    if seed if field == "seed" else positive:
                        self.assert_canonical(payload)
                    else:
                        self.reject_profile(payload, field, "INVALID_INTEGER")
                    self.check_preflight(field, value, preflight_seed if field == "seed" else preflight_positive)

    def test_number_literal_matrix_checks_exact_canonical_digest_and_errors(self):
        # Values below are data declarations; expected text is not derived from _number or a clone.
        cases = [
            ("int", 1, "1", None, True), ("float", 1.0, "1", None, True),
            ("fraction", 0.25, "0.25", None, True), ("decimal", Decimal("1.00"), "1", None, False),
            ("precise", Decimal("0.1234567890123456789012345678901"),
             "0.1234567890123456789012345678901", None, False),
            ("nan", float("nan"), None, "INVALID_NUMBER", False),
            ("infinity", float("inf"), None, "INVALID_NUMBER", False),
            ("decimal_nan", Decimal("NaN"), None, "INVALID_NUMBER", False),
            ("signaling_nan", Decimal("sNaN"), None, "INVALID_NUMBER", False),
            ("decimal_inf", Decimal("-Infinity"), None, "INVALID_NUMBER", False),
            ("true", True, None, "INVALID_NUMBER", False),
            ("false", False, None, "INVALID_NUMBER", False),
            ("negative", -1, None, "INVALID_NUMBER", False),
            ("string", "0.1", None, "INVALID_NUMBER", False), ("null", None, None, "INVALID_NUMBER", False),
            ("huge_integer", 10 ** 400, None, "NUMBER_TOO_LARGE", False),
        ]
        for size in (127, 128, 129):
            expected = "1" * size if size <= 128 else None
            cases.append((f"digits-{size}", Decimal("1" * size), expected,
                          None if expected is not None else "NUMBER_TOO_LARGE", False))
            for sign in (-1, 1):
                text = ("0." + "0" * (size - 1) + "1") if sign < 0 else "1" + "0" * size
                cases.append((f"exponent-{sign * size}", Decimal(f"1e{sign * size}"),
                              text if size <= 128 else None,
                              None if size <= 128 else "NUMBER_TOO_LARGE", False))
        for field in ("temperature", "time_budget_seconds"):
            zero_allowed = field == "temperature"
            zero_cases = [(name, value, "0" if zero_allowed else None,
                           None if zero_allowed else "INVALID_NUMBER", accepted and zero_allowed)
                          for name, value, accepted in
                          (("zero", 0, True), ("float_zero", -0.0, True),
                           ("decimal_zero", Decimal("-0.000"), False),
                           ("zero_exponent", Decimal("0e9999"), False))]
            for name, value, text, code, preflight_valid in cases + zero_cases:
                with self.subTest(field=field, case=name):
                    payload = profile()
                    payload["environment"][field] = value
                    if text is None:
                        self.reject_profile(payload, field, code)
                    else:
                        self.assert_canonical(payload, {field: text})
                    self.check_preflight(field, value, preflight_valid)

    def test_decimal_context_and_equivalent_literals_do_not_round_digest(self):
        values = (Decimal("1.2e3"), Decimal("1200.000"), Decimal("120000e-2"))
        reference = None
        for value in values:
            for precision in (2, 28, 64):
                with self.subTest(value=str(value), precision=precision):
                    payload = profile()
                    payload["environment"]["time_budget_seconds"] = value
                    with localcontext() as context:
                        context.prec = precision
                        before = context.copy()
                        result = self.assert_canonical(payload, {"time_budget_seconds": "1200"})
                        self.assertEqual(repr(context), repr(before))
                    if reference is None:
                        reference = result
                    self.assertEqual(result, reference)

    def test_preflight_invalid_fields_do_not_add_comparison_errors(self):
        config = self.config()
        config["baseline"].update(temperature=True, request_budget=0)
        config["ours"]["time_budget_seconds"] = float("inf")
        with self.assertRaises(preflight.PreflightError) as caught:
            preflight.check_preflight(config)
        self.assertEqual(set(caught.exception.errors),
                         {"baseline.temperature: INVALID", "baseline.request_budget: INVALID",
                          "ours.time_budget_seconds: INVALID"})
        changed = self.config()
        changed["ours"]["temperature"] = 0.25
        with self.assertRaises(preflight.PreflightError) as caught:
            preflight.check_preflight(changed)
        self.assertEqual(caught.exception.errors, ("comparison.temperature: MISMATCH",))

    def test_bool_integer_mutation_fails_same_rejection_oracle(self):
        payload = profile()
        payload["environment"]["request_budget"] = True
        self.reject_profile(payload, "request_budget", "INVALID_INTEGER")
        original = fingerprint._integer

        def allow_bool(value, path, *, zero=False):
            return int(value) if type(value) is bool else original(value, path, zero=zero)

        with patch.object(fingerprint, "_integer", side_effect=allow_bool):
            mutated = self.generate(payload)
            valid = profile()
            valid["environment"]["request_budget"] = 1
            self.assert_canonical(valid, result=mutated)
            with self.assertRaises(AssertionError) as caught:
                self.reject_profile(payload, "request_budget", "INVALID_INTEGER")
            self.assertIn("ProfileError not raised", str(caught.exception))

    def test_latest_digest_mutation_fails_same_rejection_oracle(self):
        payload = profile()
        payload["environment"]["target_image"] = "repo:latest@sha256:" + HEX
        self.reject_profile(payload, "target_image", "EXPECTED_PINNED_REFERENCE")
        original = fingerprint._reference

        def accept_latest(value, path, *, image=False):
            if image and value.startswith("repo:latest@sha256:"):
                return value
            return original(value, path, image=image)

        with patch.object(fingerprint, "_reference", side_effect=accept_latest):
            self.assert_canonical(payload)  # Shows actual acceptance through the public generator.
            with self.assertRaises(AssertionError) as caught:
                self.reject_profile(payload, "target_image", "EXPECTED_PINNED_REFERENCE")
            self.assertIn("ProfileError not raised", str(caught.exception))

    def test_float_rounding_mutation_collides_distinct_literals_and_fails_same_oracle(self):
        first, second = profile(), profile()
        left, right = "0.1234567890123456789012345678901", "0.1234567890123456789012345678902"
        first["environment"]["temperature"] = Decimal(left)
        second["environment"]["temperature"] = Decimal(right)
        a, b = self.assert_canonical(first, {"temperature": left}), self.assert_canonical(second, {"temperature": right})
        self.assertNotEqual(a.environment_fingerprint, b.environment_fingerprint)
        original = fingerprint._number

        def rounded(value, path, *, positive):
            return original(float(value) if isinstance(value, Decimal) else value, path, positive=positive)

        with patch.object(fingerprint, "_number", side_effect=rounded):
            a, b = self.generate(first), self.generate(second)
            self.assertEqual(a.environment_fingerprint, b.environment_fingerprint)
            with self.assertRaises(AssertionError):
                self.assert_canonical(first, {"temperature": left}, result=a)


if __name__ == "__main__":
    unittest.main()
