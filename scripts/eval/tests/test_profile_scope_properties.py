"""프로파일 scope 경로·집합과 선언 digest 결속의 bounded 생성형 회귀."""

from __future__ import annotations

import copy
import hashlib
import json
import os
import sys
import unittest
from itertools import permutations, product
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import profile_fingerprint as fingerprint
from test_profile_fingerprint import profile


def digest(kind, value):
    envelope = {"format": fingerprint.FORMAT, "kind": kind, "profile": value}
    wire = json.dumps(envelope, sort_keys=True, separators=(",", ":"), ensure_ascii=True)
    return hashlib.new("sha256", wire.encode("ascii")).hexdigest()


def expected_digests(payload):
    scope = payload["scope"]
    canonical_scope = {
        "target_origin": scope["target_origin"], "methods": sorted(scope["methods"]),
        "paths": sorted(scope["paths"]), "vuln_classes": sorted(scope["vuln_classes"]),
    }
    scope_digest = digest("scope", canonical_scope)
    # These tests vary only scope; numeric strings are independent literals for the fixed fixture.
    environment = dict(payload["environment"], temperature="0", time_budget_seconds="1200",
                       scope_fingerprint=scope_digest)
    return scope_digest, digest("environment", environment)


class TestProfileScopeProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "socket.getaddrinfo",
                       "subprocess.Popen", "os.system", "builtins.open"):
            guard = patch(target, side_effect=AssertionError("scope declarations are offline"))
            guard.start()
            self.addCleanup(guard.stop)

    def generate(self, payload):
        before = copy.deepcopy(payload)
        try:
            return fingerprint.generate_fingerprints(payload)
        finally:
            self.assertEqual(payload, before)

    def assert_canonical(self, payload, result=None):
        result = self.generate(payload) if result is None else result
        scope, environment = expected_digests(payload)
        self.assertEqual(result.scope_fingerprint, scope)
        self.assertEqual(result.environment_fingerprint, environment)
        return result

    def reject(self, payload, field, code):
        with self.assertRaises(fingerprint.ProfileError) as caught:
            self.generate(payload)
        self.assertEqual(str(caught.exception), f"scope.{field}: {code}")
        self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))

    def test_generated_static_template_and_trailing_paths_match_exact_digests(self):
        segments = ("api", "v1", "item-1.json", "{item}")
        paths = ["/", "/.well-known", "/..x", "/x.."]
        for width in (1, 2, 3):
            for parts in product(segments, repeat=width):
                paths.extend(("/" + "/".join(parts), "/" + "/".join(parts) + "/"))
        for path in paths:
            with self.subTest(path=path):
                payload = profile()
                payload["scope"]["paths"] = [path]
                self.assert_canonical(payload)

    def test_path_and_template_label_length_boundaries(self):
        for kind, lengths in (("path", (511, 512, 513)), ("label", (63, 64, 65))):
            for size in lengths:
                path = "/" + "a" * (size - 1) if kind == "path" else "/records/{" + "a" * size + "}"
                payload = profile()
                payload["scope"]["paths"] = [path]
                with self.subTest(kind=kind, size=size):
                    if size <= (512 if kind == "path" else 64):
                        self.assert_canonical(payload)
                    else:
                        self.reject(payload, "paths", "INVALID_PATH_TEMPLATE")

    def test_invalid_segments_types_and_control_insertions_are_rejected(self):
        invalid = [None, True, 7, {}, [], "", "records", "//records", "/records//item",
                   "/.", "/..", "/records/.", "/records/..", "/records/{bad-label}",
                   "/records/{Item}", "/records/{item", "/records/item}", "/records?q=1",
                   "/records#fragment", "/records/%2Fitem", "/records/é", "/records\\item"]
        for code in (*range(33), 127):
            for position in (0, 4, 8):
                invalid.append("/records"[:position] + chr(code) + "/records"[position:])
        for index, value in enumerate(invalid):
            with self.subTest(case=index):
                payload = profile()
                payload["scope"]["paths"] = [value]
                self.reject(payload, "paths", "INVALID_PATH_TEMPLATE")

    def test_all_small_set_permutations_and_object_orders_preserve_exact_digests(self):
        choices = (("GET", "PATCH", "TRACE"), ("/", "/api/{item}", "/health/"), ("idor", "bola", "sqli"))
        fields = ("methods", "paths", "vuln_classes")
        reference = None
        for orders in product(*(tuple(permutations(values)) for values in choices)):
            for reverse in (False, True):
                with self.subTest(orders=orders, reverse=reverse):
                    payload = profile()
                    for field, order in zip(fields, orders):
                        payload["scope"][field] = list(order)
                    if reverse:
                        payload["scope"] = dict(reversed(tuple(payload["scope"].items())))
                    result = self.assert_canonical(payload)
                    if reference is None:
                        reference = result
                    self.assertEqual(result, reference)

    def test_array_shapes_elements_and_duplicate_positions_have_exact_errors(self):
        choices = {"methods": ("GET", "POST"), "paths": ("/a", "/b"), "vuln_classes": ("idor", "bola")}
        element_errors = {"methods": "INVALID_METHOD", "paths": "INVALID_PATH_TEMPLATE",
                          "vuln_classes": "EXPECTED_LOGICAL_LABEL"}
        for field, values in choices.items():
            cases = [(raw, "EXPECTED_NONEMPTY_ARRAY") for raw in (None, {}, "", (), [])]
            cases += [([raw], element_errors[field]) for raw in (None, True, 1, "SYNTHETIC-PRIVATE")]
            for position in range(3):
                duplicated = list(values)
                duplicated.insert(position, values[0])
                cases.append((duplicated, "DUPLICATE"))
            for index, (raw, code) in enumerate(cases):
                with self.subTest(field=field, case=index):
                    payload = profile()
                    payload["scope"][field] = raw
                    self.reject(payload, field, code)

    def test_scope_field_changes_bind_environment_and_preserve_fixture(self):
        base = self.assert_canonical(profile())
        changes = {"target_origin": "http://eval-meter:8081/", "methods": ["GET"],
                   "paths": ["/api/other/{item}"], "vuln_classes": ["idor"]}
        for field, value in changes.items():
            with self.subTest(field=field):
                payload = profile()
                payload["scope"][field] = value
                result = self.assert_canonical(payload)
                self.assertNotEqual(result.scope_fingerprint, base.scope_fingerprint)
                self.assertNotEqual(result.environment_fingerprint, base.environment_fingerprint)
                self.assertEqual(result.fixture_fingerprint, base.fixture_fingerprint)

    def test_duplicate_ignore_mutation_fails_same_rejection_oracle(self):
        payload = profile()
        payload["scope"]["methods"] = ["GET", "GET"]
        self.reject(payload, "methods", "DUPLICATE")
        original = fingerprint._scope

        def ignore_duplicate(raw):
            changed = copy.deepcopy(raw)
            changed["methods"] = list(dict.fromkeys(changed["methods"]))
            return original(changed)

        with patch.object(fingerprint, "_scope", side_effect=ignore_duplicate):
            mutated = self.generate(payload)
            expected = copy.deepcopy(payload)
            expected["scope"]["methods"] = ["GET"]
            self.assert_canonical(expected, mutated)
            with self.assertRaises(AssertionError) as caught:
                self.reject(payload, "methods", "DUPLICATE")
            self.assertIn("ProfileError not raised", str(caught.exception))

    def test_trailing_path_collapse_mutation_fails_same_canonical_oracle(self):
        payload = profile()
        payload["scope"]["paths"] = ["/api/{item}/"]
        baseline = self.assert_canonical(payload)
        original = fingerprint._path
        with patch.object(fingerprint, "_path", side_effect=lambda value: original(value).rstrip("/") or "/"):
            mutated = self.generate(payload)
            self.assertNotEqual(mutated.scope_fingerprint, baseline.scope_fingerprint)
            with self.assertRaises(AssertionError):
                self.assert_canonical(payload, mutated)

    def test_environment_unbinding_mutation_fails_same_canonical_oracle(self):
        payload = profile()
        changed = copy.deepcopy(payload)
        changed["scope"]["paths"] = ["/api/changed/{item}"]
        before, after = self.assert_canonical(payload), self.assert_canonical(changed)
        self.assertNotEqual(before.environment_fingerprint, after.environment_fingerprint)
        original = fingerprint._digest

        def omit_binding(kind, value):
            value = dict(value)
            if kind == "environment":
                value.pop("scope_fingerprint")
            return original(kind, value)

        with patch.object(fingerprint, "_digest", side_effect=omit_binding):
            first, second = self.generate(payload), self.generate(changed)
            self.assertEqual(first.environment_fingerprint, second.environment_fingerprint)
            self.assertNotEqual(first.scope_fingerprint, second.scope_fingerprint)
            with self.assertRaises(AssertionError):
                self.assert_canonical(changed, second)


if __name__ == "__main__":
    unittest.main()
