"""고정 원본 API 형식을 따른 합성 FindingDTO만 사용한다. 실제 원자료·DB·HTTP 없음."""

from __future__ import annotations

import copy
import json
import os
import subprocess
import sys
import tempfile
import unittest
from dataclasses import asdict
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from baseline_extract import MAX_INPUT_BYTES, MAX_VOCABULARY_BYTES, UPSTREAM_COMMIT, BaselineExtractError, extract_baseline

_DEFAULT_VOCABULARY = object()


def vocabulary():
    return {"vulnclasses": {"SQL Injection": "sqli"}, "asset_types": {"endpoint": "endpoint"}}


def finding():
    return {
        "id": "456", "finding_id": "123", "vulnclass": "SQL Injection", "severity": "high",
        "status": "confirmed", "summary": "SYNTHETIC-PRIVATE-SUMMARY", "evidence": "SYNTHETIC-PRIVATE-EVIDENCE",
        "name": "SYNTHETIC-PRIVATE-NAME", "report": "SYNTHETIC-PRIVATE-REPORT",
        "task_description": "SYNTHETIC-PRIVATE-TASK", "task_id": "9", "intent_id": "8", "param_id": "7",
        "source_task_id": "6", "inherited": False, "ts": "2026-10-30T00:00:00Z",
        "traffic_count": 1, "evidence_version": 1, "report_evidence_version": 0, "report_stale": True,
        "assets": [{"id": "77", "type": "endpoint", "label": "http://SYNTHETIC-PRIVATE.invalid/account"}],
        "traffic_bindings": [{
            "id": "2", "finding_id": "123", "role": "proof", "snapshot_id": "SYNTHETIC-PRIVATE-SNAPSHOT",
            "note": "SYNTHETIC-PRIVATE-NOTE", "position": 0, "created_at": "2026-10-30T00:00:00Z",
            "snapshot": {"id": "SYNTHETIC-PRIVATE-SNAPSHOT", "source_traffic_id": "SYNTHETIC-PRIVATE-TRAFFIC",
                         "url": "http://SYNTHETIC-PRIVATE.invalid/token", "method": "GET", "status": 200,
                         "content_type": "application/json", "req_head": "Authorization: SYNTHETIC-PRIVATE",
                         "resp_head": "Set-Cookie: SYNTHETIC-PRIVATE", "req_hash": "a" * 64,
                         "resp_hash": "b" * 64, "req_len": 0, "resp_len": 1, "captured_at": 0,
                         "created_at": "2026-10-30T00:00:00Z"},
        }],
    }


class TestExtractBaseline(unittest.TestCase):
    def extract(self, payload, vocab=_DEFAULT_VOCABULARY, source_sha=UPSTREAM_COMMIT):
        return extract_baseline(payload, vocabulary() if vocab is _DEFAULT_VOCABULARY else vocab, source_sha=source_sha)

    def reject(self, payload, expected, **kwargs):
        with self.assertRaises(BaselineExtractError) as caught:
            self.extract(payload, **kwargs)
        self.assertIn(expected, str(caught.exception))
        self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))

    def test_task_node_and_standalone_finding_ids_remain_distinct(self):
        result = self.extract([finding()])
        row = result.findings[0]
        self.assertEqual((row.source_id, row.finding_id), ("456", "123"))
        self.assertEqual((row.vuln_class, row.severity, row.source_status), ("sqli", "high", "confirmed"))
        self.assertEqual(asdict(row.asset_refs[0]), {"id": "77", "type": "endpoint"})
        self.assertEqual(asdict(row.traffic_binding_refs[0]), {"binding_id": "2", "finding_id": "123", "role": "proof"})
        self.assertEqual(row.unmapped_fields, ())
        self.assertEqual((result.source_shape, result.pagination, result.declared_source_sha), ("array", None, UPSTREAM_COMMIT))

    def test_global_detail_with_equal_ids_is_supported(self):
        raw = finding()
        raw["id"] = raw["finding_id"]
        result = self.extract(raw)
        self.assertEqual(result.source_shape, "detail")
        self.assertEqual((result.findings[0].source_id, result.findings[0].finding_id), ("123", "123"))

    def test_partial_page_metadata_is_retained_without_claiming_complete_coverage(self):
        result = self.extract({"items": [finding()], "total": 100, "page": 2, "page_size": 20})
        self.assertEqual(result.source_shape, "page")
        self.assertEqual(asdict(result.pagination), {"total": 100, "page": 2, "page_size": 20})
        self.assertEqual(len(result.findings), 1)
        self.assertNotIn("complete", asdict(result))

    def test_empty_array_and_out_of_range_empty_page_preserve_observation_only(self):
        self.assertEqual(self.extract([]).findings, ())
        result = self.extract({"items": [], "total": 1, "page": 99, "page_size": 20})
        self.assertEqual((result.findings, result.pagination.total), ((), 1))

    def test_legacy_node_without_finding_id_can_be_extracted_without_bindings(self):
        raw = finding()
        del raw["finding_id"], raw["traffic_bindings"], raw["assets"]
        row = self.extract(raw).findings[0]
        self.assertEqual(row.source_id, "456")
        self.assertIsNone(row.finding_id)
        self.assertIsNone(row.asset_refs)
        self.assertIsNone(row.traffic_binding_refs)

    def test_uncollected_and_explicitly_empty_refs_are_different(self):
        raw = finding()
        raw.update(assets=None, traffic_bindings=None)
        row = self.extract(raw).findings[0]
        self.assertIsNone(row.asset_refs)
        self.assertIsNone(row.traffic_binding_refs)
        raw.update(assets=[], traffic_bindings=[])
        row = self.extract(raw).findings[0]
        self.assertEqual((row.asset_refs, row.traffic_binding_refs), ((), ()))

    def test_unregistered_class_and_asset_type_are_null_and_marked_without_echo(self):
        raw = finding()
        raw["vulnclass"] = "SYNTHETIC-PRIVATE-CLASS"
        raw["assets"][0]["type"] = "SYNTHETIC-PRIVATE-TYPE"
        row = self.extract(raw).findings[0]
        self.assertIsNone(row.vuln_class)
        self.assertIsNone(row.asset_refs[0].type)
        self.assertEqual(row.asset_refs[0].id, "77")
        self.assertEqual(row.unmapped_fields, ("assets.type", "vulnclass"))
        self.assertNotIn("SYNTHETIC-PRIVATE", json.dumps(asdict(row)))

    def test_explicit_empty_vocabulary_does_not_invent_a_mapping(self):
        row = self.extract(finding(), vocab={"vulnclasses": {}, "asset_types": {}}).findings[0]
        self.assertIsNone(row.vuln_class)
        self.assertEqual(row.unmapped_fields, ("assets.type", "vulnclass"))

    def test_mapping_is_exact_and_not_based_on_titles_or_case_folding(self):
        raw = finding()
        raw.update(vulnclass="sql injection", name="SQL Injection", summary="SQL Injection")
        self.assertIsNone(self.extract(raw).findings[0].vuln_class)

    def test_all_narrative_url_header_note_and_snapshot_fields_are_excluded(self):
        payload, vocab = [finding()], vocabulary()
        original, original_vocab = copy.deepcopy(payload), copy.deepcopy(vocab)
        with patch("socket.create_connection", side_effect=AssertionError("network")), \
                patch("subprocess.Popen", side_effect=AssertionError("process")), \
                patch("os.system", side_effect=AssertionError("shell")):
            result = self.extract(payload, vocab=vocab)
        text = json.dumps(asdict(result))
        self.assertNotIn("SYNTHETIC-PRIVATE", text)
        self.assertNotIn("SQL Injection", text)
        for field in ("summary", "evidence", "report", "url", "note", "snapshot_id", "source_traffic_id", "req_head"):
            self.assertNotIn('"' + field + '"', text)
        self.assertEqual((payload, vocab), (original, original_vocab))

    def test_source_confirmed_or_false_positive_status_never_becomes_an_evaluation_verdict(self):
        for status in ("pending", "in_progress", "confirmed", "resolved", "fixed", "false_positive", "ignored", "duplicate", "risk_accepted"):
            for severity in ("critical", "high", "medium", "low"):
                raw = finding()
                raw.update(status=status, severity=severity)
                row = self.extract(raw).findings[0]
                self.assertEqual((row.source_status, row.severity), (status, severity))
                for field in ("oracle_value", "verdict", "vuln_verdict", "resource_ref", "challenge", "actor"):
                    self.assertNotIn(field, asdict(row))

    def test_only_fixed_upstream_commit_is_supported(self):
        for sha in (None, "", "SYNTHETIC-PRIVATE", "f" * 40, True):
            self.reject(finding(), "UNSUPPORTED_COMMIT", source_sha=sha)

    def test_invalid_root_shapes_or_invented_envelopes_are_rejected(self):
        for raw in (None, True, 1, "SYNTHETIC-PRIVATE", {}, {"data": []}, {"findings": []}):
            self.reject(raw, ": ")
        self.reject([finding(), None], "EXPECTED_OBJECT")

    def test_page_shape_types_counts_and_mixed_shapes_are_rejected(self):
        base = {"items": [finding()], "total": 1, "page": 1, "page_size": 20}
        for field in ("total", "page", "page_size"):
            for value in (None, True, 1.5, "1", -1):
                self.reject(base | {field: value}, "INVALID_INTEGER")
            raw = dict(base)
            del raw[field]
            self.reject(raw, "INVALID_INTEGER")
        self.reject(base | {"items": {}}, "EXPECTED_ARRAY")
        self.reject(base | {"id": "1"}, "UNKNOWN_FIELDS")
        self.reject(base | {"page_size": 0}, "INVALID_INTEGER")
        self.reject(base | {"total": 0}, "INCONSISTENT_COUNTS")
        self.reject(base | {"page": 2}, "INCONSISTENT_COUNTS")
        self.reject(base | {"items": [finding(), finding()], "total": 2, "page_size": 1}, "INCONSISTENT_COUNTS")

    def test_decimal_string_ids_are_strict_positive_int64_not_uuid_or_json_numbers(self):
        for field in ("id", "finding_id"):
            for value in (None, True, 1, "", "0", "01", "-1", "1.0", "1\n", "SYNTHETIC-PRIVATE", "9223372036854775808"):
                if field == "finding_id" and value is None:
                    continue
                raw = finding()
                raw[field] = value
                self.reject(raw, "EXPECTED_POSITIVE_INT64_STRING")
        raw = finding()
        raw["id"] = "9223372036854775807"
        self.assertEqual(self.extract(raw).findings[0].source_id, raw["id"])

    def test_wrong_or_missing_classification_severity_status_types_fail_safely(self):
        for field in ("vulnclass", "severity", "status"):
            for value in (None, True, [], {}, 1):
                self.reject(finding() | {field: value}, ": ")
            raw = finding()
            del raw[field]
            self.reject(raw, ": ")
        for field in ("severity", "status"):
            self.reject(finding() | {field: "SYNTHETIC-PRIVATE"}, "UNSUPPORTED_VALUE")

    def test_asset_reference_shape_id_type_and_duplicates_are_checked(self):
        for raw in ({}, [None], [{"id": 77, "type": "endpoint"}], [{"id": "77", "type": 1}],
                    [{"id": "77", "type": "endpoint", "SYNTHETIC-PRIVATE": "value"}]):
            self.reject(finding() | {"assets": raw}, ": ")
        assets = finding()["assets"] * 2
        self.reject(finding() | {"assets": assets}, "DUPLICATE")

    def test_bindings_must_belong_to_explicit_standalone_finding_id(self):
        raw = finding()
        raw["traffic_bindings"][0]["finding_id"] = "456"
        self.reject(raw, "OWNER_MISMATCH")
        raw = finding()
        del raw["finding_id"]
        self.reject(raw, "OWNER_MISMATCH")

    def test_binding_roles_ids_types_and_duplicate_ids_are_checked(self):
        for role in ("baseline", "proof", "verification", "supporting"):
            raw = finding()
            raw["traffic_bindings"][0]["role"] = role
            self.assertEqual(self.extract(raw).findings[0].traffic_binding_refs[0].role, role)
        for field, value in (("role", ""), ("role", []), ("id", 2), ("finding_id", "0")):
            raw = finding()
            raw["traffic_bindings"][0][field] = value
            self.reject(raw, ": ")
        for value in ({}, [None]):
            self.reject(finding() | {"traffic_bindings": value}, ": ")
        self.reject(finding() | {"traffic_bindings": finding()["traffic_bindings"] * 2}, "DUPLICATE")

    def test_duplicate_source_ids_and_unknown_request_or_db_fields_are_rejected(self):
        self.reject([finding(), finding()], "DUPLICATE")
        for field in ("traffic_refs", "oracle_value", "vuln_class", "SYNTHETIC-PRIVATE-KEY"):
            self.reject(finding() | {field: "SYNTHETIC-PRIVATE"}, "UNKNOWN_FIELDS")

    def test_binding_id_cannot_be_reused_across_different_findings(self):
        second = finding()
        second.update(id="457", finding_id="124")
        second["traffic_bindings"][0]["finding_id"] = "124"
        self.reject([finding(), second], "finding.traffic_bindings.id: DUPLICATE")

    def test_shared_asset_is_allowed_across_distinct_findings_and_bindings(self):
        second = finding()
        second.update(id="457", finding_id="124")
        second["traffic_bindings"][0].update(id="3", finding_id="124")
        result = self.extract([finding(), second])
        self.assertEqual([row.asset_refs[0].id for row in result.findings], ["77", "77"])
        self.assertEqual([row.traffic_binding_refs[0].binding_id for row in result.findings], ["2", "3"])

    def test_vocabulary_shape_and_canonical_labels_are_restricted(self):
        for raw in (None, {}, [], {"vulnclasses": [], "asset_types": {}}, vocabulary() | {"SYNTHETIC-PRIVATE": {}}):
            self.reject(finding(), ": ", vocab=raw)
        for value in (None, "", "http://SYNTHETIC-PRIVATE.invalid", "Uppercase", "with space", "x" * 65):
            self.reject(finding(), "INVALID_MAPPING", vocab={"vulnclasses": {"SQL Injection": value}, "asset_types": {}})
        for key in ("", "x" * 129, "with\nnewline"):
            self.reject(finding(), "INVALID_MAPPING", vocab={"vulnclasses": {key: "sqli"}, "asset_types": {}})


class TestBaselineCLI(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.input = self.root / "findings.json"
        self.vocab = self.root / "vocabulary.json"
        self.input.write_text(json.dumps([finding()]), encoding="utf-8")
        self.vocab.write_text(json.dumps(vocabulary()), encoding="utf-8")

    def cli(self, sha=UPSTREAM_COMMIT):
        script = Path(__file__).resolve().parents[1] / "baseline_extract.py"
        return subprocess.run([sys.executable, "-I", "-B", str(script), str(self.input), str(self.vocab), "--source-sha", sha],
                              capture_output=True, text=True, check=False)

    def test_cli_prints_only_projected_json_and_does_not_write_files(self):
        before = {path.name: path.read_bytes() for path in self.root.iterdir()}
        result = self.cli()
        self.assertEqual((result.returncode, result.stderr), (0, ""))
        self.assertNotIn("SYNTHETIC-PRIVATE", result.stdout)
        payload = json.loads(result.stdout)
        self.assertEqual(payload["findings"][0]["source_status"], "confirmed")
        self.assertEqual({path.name: path.read_bytes() for path in self.root.iterdir()}, before)

    def test_any_invalid_row_suppresses_all_output_and_exits_two(self):
        self.input.write_text(json.dumps([finding(), finding() | {"id": "SYNTHETIC-PRIVATE"}]), encoding="utf-8")
        result = self.cli()
        self.assertEqual((result.returncode, result.stdout), (2, ""))
        self.assertNotIn("SYNTHETIC-PRIVATE", result.stderr)
        self.assertNotIn("Traceback", result.stderr)

    def test_unknown_commit_is_a_sanitized_failure(self):
        result = self.cli("SYNTHETIC-PRIVATE")
        self.assertEqual((result.returncode, result.stdout), (2, ""))
        self.assertEqual(result.stderr, "source_sha: UNSUPPORTED_COMMIT\n")

    def test_both_json_inputs_reject_duplicates_nonfinite_and_invalid_utf8(self):
        for path in (self.input, self.vocab):
            original = path.read_bytes()
            for raw in (b'{"SYNTHETIC-PRIVATE":1,"SYNTHETIC-PRIVATE":2}', b'{"x":NaN}',
                        b'{"x":Infinity}', b'{"x":1e9999}', b"\xff", b'{"SYNTHETIC-PRIVATE":'):
                path.write_bytes(raw)
                result = self.cli()
                self.assertEqual((result.returncode, result.stdout), (2, ""))
                self.assertNotIn("SYNTHETIC-PRIVATE", result.stderr)
                self.assertNotIn("Traceback", result.stderr)
            path.write_bytes(original)

    def test_oversized_inputs_and_missing_files_are_sanitized_failures(self):
        for path, limit in ((self.input, MAX_INPUT_BYTES), (self.vocab, MAX_VOCABULARY_BYTES)):
            original = path.read_bytes()
            path.write_bytes(b" " * (limit + 1))
            result = self.cli()
            self.assertEqual((result.returncode, result.stdout, result.stderr), (2, "", "json: TOO_LARGE\n"))
            path.unlink()
            result = self.cli()
            self.assertEqual((result.returncode, result.stdout, result.stderr), (2, "", "json: READ_FAILED\n"))
            path.write_bytes(original)


if __name__ == "__main__":
    unittest.main()
