"""Bounded property checks for ARTEX non-projected-field isolation."""

from __future__ import annotations

import copy
import io
import json
import os
import sys
import unittest
from contextlib import redirect_stderr, redirect_stdout
from dataclasses import asdict, replace
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import baseline_extract
from baseline_extract import BaselineExtractError, UPSTREAM_COMMIT, extract_baseline
from test_baseline_extract import finding, vocabulary


MARKER = "SYNTHETIC-PRIVATE-PROJECTION-MARKER"
ABSENT = object()
EXPECTED_FINDING = {
    "source_id": "456",
    "finding_id": "123",
    "vuln_class": "sqli",
    "severity": "high",
    "source_status": "confirmed",
    "asset_refs": ({"id": "77", "type": "endpoint"},),
    "traffic_binding_refs": ({"binding_id": "2", "finding_id": "123", "role": "proof"},),
    "unmapped_fields": (),
}


def nested_marker(depth: int):
    value = MARKER
    for level in range(depth):
        value = {"private": [value]} if level % 2 == 0 else [value, {"depth": level}]
    return value


def projected(result):
    return asdict(result)


class TestBaselineProjectionPrivacyProperties(unittest.TestCase):
    def setUp(self):
        self.network_guards = [
            patch("socket.socket", side_effect=AssertionError("network")),
            patch("socket.create_connection", side_effect=AssertionError("network")),
            patch("socket.getaddrinfo", side_effect=AssertionError("network")),
        ]
        self.process_guard = patch("subprocess.Popen", side_effect=AssertionError("process"))
        self.shell_guard = patch.object(os, "system", side_effect=AssertionError("process"))
        self.io_guard = patch("builtins.open", side_effect=AssertionError("file access"))
        for guard in self.network_guards:
            guard.start()
        self.process_guard.start()
        self.shell_guard.start()
        self.io_guard.start()
        self.addCleanup(self.io_guard.stop)
        self.addCleanup(self.shell_guard.stop)
        self.addCleanup(self.process_guard.stop)
        for guard in reversed(self.network_guards):
            self.addCleanup(guard.stop)

    def extract_checked(self, payload, vocab=None, expected_error=None):
        vocab = vocabulary() if vocab is None else vocab
        before_payload, before_vocab = copy.deepcopy(payload), copy.deepcopy(vocab)
        stdout, stderr = io.StringIO(), io.StringIO()
        caught = None
        result = None
        try:
            with redirect_stdout(stdout), redirect_stderr(stderr):
                try:
                    result = extract_baseline(payload, vocab, source_sha=UPSTREAM_COMMIT)
                except BaselineExtractError as error:
                    caught = error
        finally:
            self.assertEqual(payload, before_payload)
            self.assertEqual(vocab, before_vocab)
            self.assertEqual((stdout.getvalue(), stderr.getvalue()), ("", ""))
        if expected_error is not None:
            self.assertIsNotNone(caught)
            self.assertEqual(str(caught), expected_error)
            return caught
        if caught is not None:
            raise caught
        return result

    def assert_projection(self, payload, result, finding_expected=EXPECTED_FINDING, shape=None, pagination=None):
        if shape is None:
            shape = "array" if isinstance(payload, list) else "page" if "items" in payload else "detail"
        expected = {
            "declared_source_sha": UPSTREAM_COMMIT,
            "source_shape": shape,
            "pagination": pagination,
            "findings": (finding_expected,),
        }
        self.assertEqual(projected(result), expected)
        self.assertNotIn(MARKER, repr(result))
        self.assertNotIn(MARKER, json.dumps(projected(result), ensure_ascii=False))

    def valid_row(self):
        return finding()

    def shape_cases(self, raw):
        return (
            ([copy.deepcopy(raw)], "array", None),
            (copy.deepcopy(raw), "detail", None),
            ({"items": [copy.deepcopy(raw)], "total": 1, "page": 1, "page_size": 1}, "page",
             {"total": 1, "page": 1, "page_size": 1}),
        )

    def test_nonprojected_field_matrix_matches_independent_oracle(self):
        fields = (
            "name", "summary", "evidence", "report", "task_description", "ts", "intent_id",
            "param_id", "task_id", "source_task_id", "inherited", "traffic_count",
            "evidence_version", "report_evidence_version", "report_stale",
        )
        values = (ABSENT,) + tuple(nested_marker(depth) for depth in range(4)) + (None, False, 0)
        for field in fields:
            for index, value in enumerate(values):
                raw = self.valid_row()
                if value is ABSENT:
                    del raw[field]
                else:
                    raw[field] = value
                for payload, shape, pagination in self.shape_cases(raw):
                    with self.subTest(field=field, value=index, shape=shape):
                        result = self.extract_checked(payload)
                        self.assert_projection(payload, result, shape=shape, pagination=pagination)

    def test_asset_label_and_binding_metadata_matrix_is_never_projected(self):
        fields = ("label", "snapshot_id", "note", "position", "created_at", "snapshot")
        values = (ABSENT,) + tuple(nested_marker(depth) for depth in range(4)) + (None, False, 0)
        for field in fields:
            for index, value in enumerate(values):
                raw = self.valid_row()
                target = raw["assets"][0] if field == "label" else raw["traffic_bindings"][0]
                if value is ABSENT:
                    del target[field]
                else:
                    target[field] = value
                for payload, shape, pagination in self.shape_cases(raw):
                    with self.subTest(field=field, value=index, shape=shape):
                        result = self.extract_checked(payload)
                        self.assert_projection(payload, result, shape=shape, pagination=pagination)

    def test_array_detail_and_page_shapes_have_independent_expectations(self):
        row = self.valid_row()
        cases = (
            ([row], "array", None),
            (row, "detail", None),
            ({"items": [row], "total": 1, "page": 1, "page_size": 1}, "page", {"total": 1, "page": 1, "page_size": 1}),
        )
        for payload, shape, page in cases:
            with self.subTest(shape=shape):
                result = self.extract_checked(copy.deepcopy(payload))
                self.assert_projection(payload, result, shape=shape, pagination=page)

    def test_unknown_field_injection_uses_fixed_sanitized_error_paths(self):
        cases = []
        row = self.valid_row()
        row[MARKER] = MARKER
        cases.append((row, "finding: UNKNOWN_FIELDS"))
        row = self.valid_row()
        row["assets"][0][MARKER] = MARKER
        cases.append(([row], "finding.assets: UNKNOWN_FIELDS"))
        row = self.valid_row()
        row["traffic_bindings"][0][MARKER] = MARKER
        cases.append(([row], "finding.traffic_bindings: UNKNOWN_FIELDS"))
        cases.append(({"items": [self.valid_row()], "total": 1, "page": 1, "page_size": 1, MARKER: MARKER}, "page: UNKNOWN_FIELDS"))
        for payload, message in cases:
            with self.subTest(message=message):
                caught = self.extract_checked(payload, expected_error=message)
                self.assertNotIn(MARKER, str(caught))

    def test_mutant_narrative_class_inference_fails_same_full_projection_oracle(self):
        raw = self.valid_row()
        raw.update(vulnclass="SYNTHETIC-PRIVATE-UNMAPPED", name="SQL Injection")
        expected = dict(EXPECTED_FINDING, vuln_class=None, unmapped_fields=("vulnclass",))
        self.assert_projection([raw], self.extract_checked([raw]), finding_expected=expected)
        original = baseline_extract._finding

        def infer_from_name(value, classes, types):
            mutated = copy.deepcopy(value)
            mutated["vulnclass"] = mutated["name"]
            return original(mutated, classes, types)

        with patch.object(baseline_extract, "_finding", side_effect=infer_from_name):
            mutant = self.extract_checked([raw])
        self.assertEqual(mutant.findings[0].vuln_class, "sqli")
        with self.assertRaises(AssertionError):
            self.assert_projection([raw], mutant, finding_expected=expected)

    def test_mutant_asset_label_leak_fails_same_full_projection_oracle(self):
        raw = self.valid_row()
        raw["assets"][0]["label"] = MARKER
        self.assert_projection([raw], self.extract_checked([raw]))
        original = baseline_extract._finding

        def leak_label(value, classes, types):
            result = original(value, classes, types)
            asset = result.asset_refs[0]
            leaked = baseline_extract.AssetReference(value["assets"][0]["label"], asset.type)
            return replace(result, asset_refs=(leaked,))

        with patch.object(baseline_extract, "_finding", side_effect=leak_label):
            mutant = self.extract_checked([raw])
        self.assertEqual(mutant.findings[0].asset_refs[0].id, MARKER)
        with self.assertRaises(AssertionError):
            self.assert_projection([raw], mutant)

    def test_mutant_binding_snapshot_leak_fails_same_full_projection_oracle(self):
        raw = self.valid_row()
        raw["traffic_bindings"][0]["snapshot_id"] = MARKER
        self.assert_projection([raw], self.extract_checked([raw]))
        original = baseline_extract._finding

        def leak_snapshot(value, classes, types):
            result = original(value, classes, types)
            binding = result.traffic_binding_refs[0]
            leaked = baseline_extract.TrafficBindingReference(
                value["traffic_bindings"][0]["snapshot_id"], binding.finding_id, binding.role
            )
            return replace(result, traffic_binding_refs=(leaked,))

        with patch.object(baseline_extract, "_finding", side_effect=leak_snapshot):
            mutant = self.extract_checked([raw])
        self.assertEqual(mutant.findings[0].traffic_binding_refs[0].binding_id, MARKER)
        with self.assertRaises(AssertionError):
            self.assert_projection([raw], mutant)


if __name__ == "__main__":
    unittest.main()
