"""결정적이고 비민감한 평가 fixture 계약의 bounded property 회귀."""

from __future__ import annotations

import copy
import hashlib
import json
import os
import sys
import unittest
from dataclasses import asdict
from itertools import permutations
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import profile_fingerprint
from profile_fingerprint import FORMAT, ProfileError, generate_fingerprints


PRIVATE_MARKER = "SYNTHETIC-PRIVATE-FIXTURE-MARKER"


def declaration() -> dict:
    """실계정·환경·URL과 무관한 최소 유효 선언."""
    return {
        "version": 5,
        "fixture": {
            "preparation_id": "fixture_recipe",
            "preparation_revision": 7,
            "accounts": [
                {"label": "alpha", "role": "basic"},
                {"label": "beta", "role": "reviewer"},
            ],
            "resources": [
                {"label": "alpha_doc", "type": "record", "owner": "alpha", "count": 1,
                 "shared": False, "delegated": False},
                {"label": "beta_doc", "type": "record", "owner": "beta", "count": 2,
                 "shared": True, "delegated": False},
                {"label": "shared_note", "type": "document", "owner": "alpha", "count": 3,
                 "shared": True, "delegated": True},
            ],
        },
        "environment": {
            "network_mode": "dedicated_network",
            "llm": {"backend": "local", "base_url": "http://fixture-llm:11434/v1", "model": "fixture-model:fixed-tag",
                    "hostname": "fixture-llm", "host_ip": "10.0.0.5", "model_digest": "a" * 64,
                    "context_length": 32768, "server_version": "0.32.6"},
            "temperature": 0, "seed": 11, "target_image": "fixture/image:1.0.0",
            "worker_count": 1, "time_budget_seconds": 60, "request_budget": 10,
            "run_storage": {"engine_db_tmpfs": "1g", "engine_data_tmpfs": "512m", "tmpfs_min_free": "16m"},
            "run_drain": {"grace_seconds": 30, "settle_seconds": 2},
            "sampling": {"top_p": 0.95, "presence_penalty": 0.5, "frequency_penalty": 0.25},
        },
        "scope": {"target_origin": "http://eval-meter:8080", "methods": ["GET"],
                  "paths": ["/fixture/{item}"], "vuln_classes": ["idor"]},
    }


def independent_fixture_digest(fixture_literal: dict) -> str:
    """제품 정규화기를 호출하지 않고 이미 정렬된 선언 literal을 digest한다."""
    envelope = {"format": FORMAT, "kind": "fixture", "profile": fixture_literal}
    canonical = json.dumps(envelope, sort_keys=True, separators=(",", ":"), ensure_ascii=True)
    return hashlib.sha256(canonical.encode("ascii")).hexdigest()


class TestProfileFixtureProperties(unittest.TestCase):
    def setUp(self):
        self.expected_fixture = {
            "preparation_id": "fixture_recipe", "preparation_revision": 7,
            "accounts": [{"label": "alpha", "role": "basic"},
                         {"label": "beta", "role": "reviewer"}],
            "resources": [
                {"label": "alpha_doc", "type": "record", "owner": "alpha", "count": 1,
                 "shared": False, "delegated": False},
                {"label": "beta_doc", "type": "record", "owner": "beta", "count": 2,
                 "shared": True, "delegated": False},
                {"label": "shared_note", "type": "document", "owner": "alpha", "count": 3,
                 "shared": True, "delegated": True},
            ],
        }
        for target in ("socket.socket", "socket.create_connection", "socket.getaddrinfo",
                       "subprocess.Popen", "os.system", "builtins.open"):
            guard = patch(target, side_effect=AssertionError("offline fixture declarations only"))
            guard.start()
            self.addCleanup(guard.stop)

    def guarded_generate(self, payload: dict):
        before = copy.deepcopy(payload)
        try:
            return generate_fingerprints(payload)
        finally:
            self.assertEqual(payload, before)

    def assert_fixture_matches_literal(self, result, literal: dict):
        self.assertEqual(result.fixture_fingerprint, independent_fixture_digest(literal))

    def reject(self, payload: dict, expected: str) -> None:
        with self.assertRaises(ProfileError) as caught:
            self.guarded_generate(payload)
        self.assertIn(expected, str(caught.exception))
        self.assertNotIn(PRIVATE_MARKER, str(caught.exception))

    def test_array_and_object_permutations_have_independent_expected_digest(self):
        payload = declaration()
        before = copy.deepcopy(payload)
        changed = copy.deepcopy(payload)
        changed["fixture"]["accounts"].reverse()
        changed["fixture"]["resources"].reverse()
        for row in changed["fixture"]["accounts"] + changed["fixture"]["resources"]:
            reordered = {key: row[key] for key in reversed(tuple(row))}
            row.clear()
            row.update(reordered)
        first = self.guarded_generate(payload)
        second = self.guarded_generate(changed)
        self.assertEqual(first, second)
        self.assert_fixture_matches_literal(first, self.expected_fixture)
        self.assertEqual(payload, before)

    def test_bounded_owner_order_and_key_permutations(self):
        payload = declaration()
        observed = 0
        for owner_bits in range(8):
            for account_reversed in (False, True):
                for permutation in permutations(range(3)):
                    for key_reversed in (False, True):
                        changed = copy.deepcopy(payload)
                        owners = ("alpha", "beta")
                        for index, row in enumerate(changed["fixture"]["resources"]):
                            row["owner"] = owners[(owner_bits >> index) & 1]
                        if account_reversed:
                            changed["fixture"]["accounts"].reverse()
                        rows = changed["fixture"]["resources"]
                        changed["fixture"]["resources"] = [rows[index] for index in permutation]
                        for row in changed["fixture"]["accounts"] + changed["fixture"]["resources"]:
                            if key_reversed:
                                reordered = {key: row[key] for key in reversed(tuple(row))}
                                row.clear()
                                row.update(reordered)
                        with self.subTest(owner_bits=owner_bits, account_reversed=account_reversed,
                                          resource_permutation=permutation, key_reversed=key_reversed):
                            expected = copy.deepcopy(self.expected_fixture)
                            owner_by_label = {
                                row["label"]: row["owner"] for row in changed["fixture"]["resources"]
                            }
                            for row in expected["resources"]:
                                row["owner"] = owner_by_label[row["label"]]
                            result = self.guarded_generate(changed)
                            self.assert_fixture_matches_literal(result, expected)
                        observed += 1
        self.assertEqual(observed, 192)

    def test_empty_anonymous_fixture_is_valid(self):
        payload = declaration()
        payload["fixture"]["accounts"] = []
        payload["fixture"]["resources"] = []
        result = self.guarded_generate(payload)
        expected = copy.deepcopy(self.expected_fixture)
        expected.update(accounts=[], resources=[])
        self.assert_fixture_matches_literal(result, expected)

    def test_valid_boolean_and_int64_boundary_controls(self):
        maximum = (1 << 63) - 1
        for shared, delegated in ((False, False), (False, True), (True, False), (True, True)):
            for revision, count in ((1, 1), (1, maximum - 1), (1, maximum),
                                    (maximum - 1, 1), (maximum, 1)):
                payload = declaration()
                payload["fixture"]["preparation_revision"] = revision
                payload["fixture"]["resources"][0].update(count=count, shared=shared, delegated=delegated)
                with self.subTest(shared=shared, delegated=delegated, revision=revision, count=count):
                    result = self.guarded_generate(payload)
                    expected = copy.deepcopy(self.expected_fixture)
                    expected["preparation_revision"] = revision
                    expected["resources"][0].update(count=count, shared=shared, delegated=delegated)
                    self.assert_fixture_matches_literal(result, expected)

    def test_duplicate_and_owner_binding_mutations_are_rejected(self):
        cases = []
        payload = declaration()
        payload["fixture"]["accounts"].append(copy.deepcopy(payload["fixture"]["accounts"][0]))
        cases.append((payload, "DUPLICATE"))
        payload = declaration()
        payload["fixture"]["resources"].append(copy.deepcopy(payload["fixture"]["resources"][0]))
        cases.append((payload, "DUPLICATE"))
        for owner in ("missing_account", "account@example.invalid", PRIVATE_MARKER):
            payload = declaration()
            payload["fixture"]["resources"][0]["owner"] = owner
            cases.append((payload, "UNKNOWN_ACCOUNT_LABEL" if owner == "missing_account" else "EXPECTED_LOGICAL_LABEL"))
        for case_index, (payload, expected) in enumerate(cases):
            with self.subTest(case=case_index, expected=expected):
                self.reject(payload, expected)

    def test_integer_and_strict_boolean_boundaries_are_rejected(self):
        for value in (True, False, 0, -1, 1.0, "1", None, 1 << 63):
            payload = declaration()
            payload["fixture"]["preparation_revision"] = value
            with self.subTest(field="preparation_revision", value=repr(value)):
                self.reject(payload, "INVALID_INTEGER")
        for value in (True, False, 0, -1, 1.0, "1", None, 1 << 63):
            payload = declaration()
            payload["fixture"]["resources"][0]["count"] = value
            with self.subTest(field="count", value=repr(value)):
                self.reject(payload, "INVALID_INTEGER")
        for field in ("shared", "delegated"):
            for value in (None, 0, 1, "false", [], {}):
                payload = declaration()
                payload["fixture"]["resources"][0][field] = value
                with self.subTest(field=field, value=repr(value)):
                    self.reject(payload, "EXPECTED_BOOL")

    def test_logical_shape_missing_and_unknown_fields_are_rejected(self):
        for value in (None, True, 1, "", "UPPER", "x-y", "1alpha", "x" * 65, "한글"):
            payload = declaration()
            payload["fixture"]["accounts"][0]["label"] = value
            with self.subTest(field="label", value=repr(value)):
                self.reject(payload, "EXPECTED_LOGICAL_LABEL")
        for section, row in (("accounts", declaration()["fixture"]["accounts"][0]),
                             ("resources", declaration()["fixture"]["resources"][0])):
            for field in row:
                payload = declaration()
                del payload["fixture"][section][0][field]
                with self.subTest(section=section, missing=field):
                    self.reject(payload, "MISSING_FIELDS")
            payload = declaration()
            payload["fixture"][section][0][PRIVATE_MARKER] = PRIVATE_MARKER
            with self.subTest(section=section, unknown=PRIVATE_MARKER):
                self.reject(payload, "UNKNOWN_FIELDS")
        for field in ("preparation_id", "preparation_revision", "accounts", "resources"):
            payload = declaration()
            del payload["fixture"][field]
            with self.subTest(section="fixture", missing=field):
                self.reject(payload, "MISSING_FIELDS")
        payload = declaration()
        payload["fixture"][PRIVATE_MARKER] = PRIVATE_MARKER
        with self.subTest(section="fixture", unknown=PRIVATE_MARKER):
            self.reject(payload, "UNKNOWN_FIELDS")

    def test_fixture_only_changes_isolate_fixture_digest(self):
        baseline = self.guarded_generate(declaration())
        changes = (("preparation_revision", 8), ("accounts", None), ("resource_count", 4))
        for field, value in changes:
            payload = declaration()
            expected = copy.deepcopy(self.expected_fixture)
            if field == "accounts":
                payload["fixture"]["accounts"][1]["role"] = "admin"
                expected["accounts"][1]["role"] = "admin"
            elif field == "resource_count":
                payload["fixture"]["resources"][0]["count"] = value
                expected["resources"][0]["count"] = value
            else:
                payload["fixture"][field] = value
                expected[field] = value
            with self.subTest(change=field):
                result = self.guarded_generate(payload)
                self.assert_fixture_matches_literal(result, expected)
                self.assertNotEqual(result.fixture_fingerprint, baseline.fixture_fingerprint, field)
                self.assertEqual(result.environment_fingerprint, baseline.environment_fingerprint)
                self.assertEqual(result.scope_fingerprint, baseline.scope_fingerprint)

    def test_mutation_oracles_expose_unsorted_fixture_and_integer_coercion(self):
        payload = declaration()
        payload["fixture"]["accounts"].reverse()
        payload["fixture"]["resources"].reverse()
        normal = self.guarded_generate(payload)
        self.assert_fixture_matches_literal(normal, self.expected_fixture)
        with patch.object(profile_fingerprint, "_fixture", side_effect=lambda raw: raw):
            mutated = self.guarded_generate(payload)
        with self.assertRaises(AssertionError):
            self.assert_fixture_matches_literal(mutated, self.expected_fixture)

        invalid = declaration()
        invalid["fixture"]["preparation_revision"] = True
        self.reject(invalid, "INVALID_INTEGER")
        with patch.object(profile_fingerprint, "_integer", side_effect=lambda value, path, zero=False: int(value)):
            weakened = self.guarded_generate(invalid)
        self.assertRegex(weakened.fixture_fingerprint, r"\A[0-9a-f]{64}\Z")
        with patch.object(profile_fingerprint, "_integer", side_effect=lambda value, path, zero=False: int(value)):
            with self.assertRaises(AssertionError):
                self.reject(invalid, "INVALID_INTEGER")

    def test_validation_does_not_use_network_files_or_processes(self):
        payload = declaration()
        result = self.guarded_generate(payload)
        self.assertEqual(result.format, FORMAT)
        self.assertNotIn(PRIVATE_MARKER, json.dumps(asdict(result)))


if __name__ == "__main__":
    unittest.main()
