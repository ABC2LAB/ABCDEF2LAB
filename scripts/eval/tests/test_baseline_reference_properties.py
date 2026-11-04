"""Bounded property checks for ARTEX identifier and reference binding contracts."""

from __future__ import annotations

import copy
import json
import os
import socket
import subprocess
import sys
import unittest
from dataclasses import asdict
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import baseline_extract as extractor
from baseline_extract import BaselineExtractError, UPSTREAM_COMMIT, extract_baseline


MARKER = "SYNTHETIC-PRIVATE-IDENTIFIER-MARKER"


def vocabulary():
    return {"vulnclasses": {"SQL Injection": "sqli"}, "asset_types": {"endpoint": "endpoint"}}


_DEFAULT = object()


def finding(source_id="1", finding_id="101", *, assets=_DEFAULT, bindings=_DEFAULT, omit_finding_id=False):
    row = {
        "id": source_id,
        "vulnclass": "SQL Injection",
        "name": "synthetic",
        "severity": "high",
        "status": "confirmed",
        "summary": "synthetic",
        "evidence": "synthetic",
        "assets": [{"id": "201", "type": "endpoint", "label": "synthetic"}]
        if assets is _DEFAULT else assets,
        "traffic_bindings": (None if finding_id is None else
                             [{"id": "301", "finding_id": finding_id, "role": "proof"}])
        if bindings is _DEFAULT else bindings,
    }
    if not omit_finding_id:
        row["finding_id"] = finding_id
    return row


def project_expectation(source_id, finding_id, asset_refs=(("201", "endpoint"),),
                        binding_refs=(("301", "101", "proof"),)):
    # This oracle is declared from the fixture, independently of product output.
    return {
        "source_id": source_id,
        "finding_id": finding_id,
        "asset_refs": tuple(asset_refs),
        "binding_refs": tuple(binding_refs),
    }


class TestBaselineReferenceProperties(unittest.TestCase):
    def setUp(self):
        self._guards = [
            patch("builtins.open", side_effect=AssertionError("file")),
            patch.object(socket, "socket", side_effect=AssertionError("network")),
            patch.object(socket, "create_connection", side_effect=AssertionError("network")),
            patch.object(subprocess, "Popen", side_effect=AssertionError("process")),
            patch("os.system", side_effect=AssertionError("shell")),
        ]
        for guard in self._guards:
            guard.start()

    def tearDown(self):
        for guard in reversed(self._guards):
            guard.stop()

    def extract(self, payload, vocab=None):
        actual_vocab = vocabulary() if vocab is None else vocab
        original_payload = copy.deepcopy(payload)
        original_vocab = copy.deepcopy(actual_vocab)
        try:
            return extract_baseline(payload, actual_vocab, source_sha=UPSTREAM_COMMIT)
        finally:
            self.assertEqual(payload, original_payload)
            self.assertEqual(actual_vocab, original_vocab)

    def reject(self, payload, code):
        with self.assertRaises(BaselineExtractError) as caught:
            self.extract(payload)
        message = str(caught.exception)
        self.assertIn(code, message)
        self.assertNotIn(MARKER, message)
        return message

    def assert_references(self, result, expected):
        row = result.findings[0]
        self.assertEqual((row.source_id, row.finding_id),
                         (expected["source_id"], expected["finding_id"]))
        self.assertEqual(tuple((asset.id, asset.type) for asset in row.asset_refs),
                         expected["asset_refs"])
        self.assertEqual(tuple((binding.binding_id, binding.finding_id, binding.role)
                               for binding in row.traffic_binding_refs), expected["binding_refs"])

    def test_id_min_max_and_namespace_cross_product(self):
        values = ("1", "9223372036854775806", "9223372036854775807")
        for position in ("source", "finding", "asset", "binding"):
            for value in values:
                source_id, finding_id = "1", "101"
                asset_id, binding_id = "201", "301"
                if position == "source":
                    source_id = value
                elif position == "finding":
                    finding_id = value
                elif position == "asset":
                    asset_id = value
                else:
                    binding_id = value
                with self.subTest(position=position, value=value):
                    raw = finding(source_id, finding_id,
                                  assets=[{"id": asset_id, "type": "endpoint", "label": "x"}],
                                  bindings=[{"id": binding_id, "finding_id": finding_id, "role": "proof"}])
                    result = self.extract([raw])
                    self.assert_references(result, project_expectation(
                        source_id, finding_id, ((asset_id, "endpoint"),),
                        ((binding_id, finding_id, "proof"),)))

    def test_all_binding_roles_and_shared_finding_namespace_are_preserved(self):
        rows = []
        expected = []
        for index, role in enumerate(("baseline", "proof", "verification", "supporting"), 1):
            source_id = str(index)
            shared_finding_id = "900"
            binding_id = str(900 + index)
            rows.append(finding(source_id, shared_finding_id,
                                assets=[{"id": "77", "type": "endpoint", "label": "x"}],
                                bindings=[{"id": binding_id, "finding_id": shared_finding_id, "role": role}]))
            expected.append(project_expectation(source_id, shared_finding_id,
                            (("77", "endpoint"),), ((binding_id, shared_finding_id, role),)))
        result = self.extract(rows)
        self.assertEqual(len(result.findings), len(expected))
        for actual, declared in zip(result.findings, expected):
            self.assertEqual((actual.source_id, actual.finding_id),
                             (declared["source_id"], declared["finding_id"]))
            self.assertEqual(tuple((ref.id, ref.type) for ref in actual.asset_refs), declared["asset_refs"])
            self.assertEqual(tuple((ref.binding_id, ref.finding_id, ref.role)
                                   for ref in actual.traffic_binding_refs), declared["binding_refs"])

    def test_invalid_identifier_types_and_spellings_are_rejected_at_each_reference_position(self):
        invalid = ("0", "01", "+1", "-1", "1.0", "1e2", " 1", "1 ", "١",
                   "9223372036854775808", 1, 1.0, True, None, [], MARKER)
        for position in ("source", "finding", "asset", "binding", "owner"):
            for value in invalid:
                if position == "finding" and value is None:
                    # A null finding_id is the documented legacy form.
                    continue
                with self.subTest(position=position, value=repr(value)):
                    raw = finding("1", "101")
                    if position == "source":
                        raw["id"] = value
                    elif position == "finding":
                        raw["finding_id"] = value
                    elif position == "asset":
                        raw["assets"][0]["id"] = value
                    elif position == "binding":
                        raw["traffic_bindings"][0]["id"] = value
                    else:
                        raw["traffic_bindings"][0]["finding_id"] = value
                    self.reject([raw], "EXPECTED_POSITIVE_INT64_STRING")

    def test_legacy_null_or_missing_references_and_empty_lists_are_preserved(self):
        for label, raw in (
            ("null", finding("1", None, assets=None, bindings=None)),
            ("missing", finding("2", "102", assets=None, bindings=[], omit_finding_id=True)),
        ):
            with self.subTest(finding_id=label):
                row = self.extract([raw]).findings[0]
                self.assertIsNone(row.finding_id)
                self.assertIsNone(row.asset_refs)
                self.assertEqual(row.traffic_binding_refs, None if label == "null" else ())
        raw = finding("2", "102", assets=[], bindings=[])
        row = self.extract([raw]).findings[0]
        self.assertEqual((row.asset_refs, row.traffic_binding_refs), ((), ()))
        for bindings in ([{"id": "301", "finding_id": "102", "role": "proof"}],):
            raw = finding("3", "103", assets=None, bindings=bindings, omit_finding_id=True)
            self.reject([raw], "OWNER_MISMATCH")

    def test_binding_owner_and_duplicate_namespace_constraints(self):
        raw = finding("1", "101")
        raw["traffic_bindings"][0]["finding_id"] = "102"
        self.reject([raw], "OWNER_MISMATCH")

        shared_asset = [{"id": "7", "type": "endpoint", "label": "x"}]
        first = finding("1", "101", assets=shared_asset,
                        bindings=[{"id": "11", "finding_id": "101", "role": "proof"}])
        second = finding("2", "102", assets=copy.deepcopy(shared_asset),
                         bindings=[{"id": "12", "finding_id": "102", "role": "proof"}])
        result = self.extract([first, second])
        self.assertEqual([row.source_id for row in result.findings], ["1", "2"])
        self.assertEqual([row.asset_refs[0].id for row in result.findings], ["7", "7"])
        duplicate_binding = copy.deepcopy(second)
        duplicate_binding["traffic_bindings"][0]["id"] = "11"
        self.reject([first, duplicate_binding], "DUPLICATE")
        duplicate_source = copy.deepcopy(second)
        duplicate_source["id"] = "1"
        self.reject([first, duplicate_source], "DUPLICATE")

        same_finding_assets = finding("3", "103", assets=[
            {"id": "8", "type": "endpoint", "label": "first"},
            {"id": "8", "type": "endpoint", "label": "second"},
        ], bindings=[])
        self.reject([same_finding_assets], "DUPLICATE")
        same_finding_bindings = finding("4", "104", assets=[], bindings=[
            {"id": "18", "finding_id": "104", "role": "proof"},
            {"id": "18", "finding_id": "104", "role": "supporting"},
        ])
        self.reject([same_finding_bindings], "DUPLICATE")

    def test_array_detail_and_page_shapes_share_reference_oracle_only(self):
        raw = finding("8", "108", assets=[{"id": "208", "type": "endpoint", "label": "x"}],
                      bindings=[{"id": "308", "finding_id": "108", "role": "verification"}])
        expected = project_expectation("8", "108", (("208", "endpoint"),),
                                      (("308", "108", "verification"),))
        for shape, payload in (
            ("array", [copy.deepcopy(raw)]),
            ("detail", copy.deepcopy(raw)),
            ("page", {"items": [copy.deepcopy(raw)], "total": 1, "page": 1, "page_size": 1}),
        ):
            with self.subTest(shape=shape):
                result = self.extract(payload)
                self.assertEqual(result.source_shape, shape)
                self.assert_references(result, expected)

    def test_input_and_network_process_boundaries_are_unchanged(self):
        payload = [finding("1", "101")]
        result = self.extract(payload)
        self.assertEqual(result.findings[0].source_id, "1")
        self.assertNotIn(MARKER, json.dumps(asdict(result)))

    def test_mutation_weak_id_is_caught_by_failure_oracle(self):
        raw = finding(MARKER, "101")
        self.reject([raw], "EXPECTED_POSITIVE_INT64_STRING")
        with patch.object(extractor, "_id", side_effect=lambda value, path: value):
            mutated = self.extract([raw])
            self.assertEqual(mutated.findings[0].source_id, MARKER)
            with self.assertRaises(AssertionError):
                self.reject([raw], "EXPECTED_POSITIVE_INT64_STRING")

    def test_mutation_owner_binding_is_caught_by_failure_oracle(self):
        raw = finding("1", "101")
        raw["traffic_bindings"][0]["finding_id"] = "999"
        self.reject([raw], "OWNER_MISMATCH")
        original_bindings = extractor._bindings

        def weak_bindings(value, finding_id):
            if value is None:
                return None
            return tuple(extractor.TrafficBindingReference(str(item["id"]), str(item["finding_id"]), item["role"])
                         for item in value)

        with patch.object(extractor, "_bindings", side_effect=weak_bindings):
            mutated = self.extract([raw])
            self.assertEqual(mutated.findings[0].traffic_binding_refs[0].finding_id, "999")
            with self.assertRaises(AssertionError):
                self.reject([raw], "OWNER_MISMATCH")
        self.assertIs(extractor._bindings, original_bindings)


if __name__ == "__main__":
    unittest.main()
