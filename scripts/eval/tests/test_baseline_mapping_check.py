"""분류 매핑 진단: 합성 DTO·매핑만 사용하고 원문/GT/판정을 출력하지 않는다."""

from __future__ import annotations

import copy
import io
import json
import os
import socket
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from dataclasses import asdict, replace
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import baseline_mapping_check as check
from baseline_extract import UPSTREAM_COMMIT, extract_baseline
from test_baseline_extract import vocabulary
from test_baseline_pages import page, pages, row

MARKER = "SYNTHETIC-PRIVATE-DO-NOT-PRINT"


class TestBaselineMappingCheck(unittest.TestCase):
    def diagnose(self, payload, **kwargs):
        return check.diagnose_baseline_mapping(payload, kwargs.pop("vocabulary", vocabulary()),
            source_sha=kwargs.pop("source_sha", UPSTREAM_COMMIT), **kwargs)

    def fields(self, result):
        return {field.field: (field.source_count, field.source_ids) for field in result.fields}

    def reject(self, payload, fragment=None, **kwargs):
        with self.assertRaises(check.MappingCheckError) as caught:
            self.diagnose(payload, **kwargs)
        if fragment:
            self.assertIn(fragment, str(caught.exception))
        self.assertNotIn(MARKER, str(caught.exception))

    def test_registered_values_produce_three_zero_field_reports(self):
        result = self.diagnose([row(1)])
        self.assertEqual((result.declared_source_sha, result.source_shape, result.input_source_count,
                          result.unmapped_source_count, result.asset_refs_not_collected_count),
                         (UPSTREAM_COMMIT, "array", 1, 0, 0))
        self.assertEqual([(r.field, r.source_count, r.source_ids) for r in result.fields],
                         [("vulnclass", 0, ()), ("assets.type", 0, ()), ("severity", 0, ())])

    def test_field_counts_and_union_count_have_different_meanings(self):
        first, second, third = row(1), row(2), row(3)
        first.update(vulnclass=MARKER, severity=MARKER)
        first["assets"][0]["type"] = MARKER
        second["vulnclass"] = MARKER
        result = self.diagnose([first, second, third])
        self.assertEqual(self.fields(result), {"vulnclass": (2, ("1", "2")),
                         "assets.type": (1, ("1",)), "severity": (1, ("1",))})
        self.assertEqual((result.input_source_count, result.unmapped_source_count), (3, 2))
        self.assertEqual(sum(r.source_count for r in result.fields), 4)

    def test_multiple_unknown_asset_types_count_a_source_only_once(self):
        value = row(1)
        value["assets"] = [{"id": str(n), "type": MARKER, "label": MARKER} for n in (77, 78, 79)]
        result = self.diagnose([value])
        self.assertEqual(self.fields(result)["assets.type"], (1, ("1",)))
        self.assertEqual(result.unmapped_source_count, 1)

    def test_unavailable_assets_are_separate_from_empty_and_unmapped_assets(self):
        values = [row(n) for n in range(1, 5)]
        del values[0]["assets"]
        values[1]["assets"] = None
        values[2]["assets"] = []
        values[3]["assets"][0]["type"] = MARKER
        result = self.diagnose(values)
        self.assertEqual((result.asset_refs_not_collected_count, result.asset_refs_not_collected_source_ids),
                         (2, ("1", "2")))
        self.assertEqual(self.fields(result)["assets.type"], (1, ("4",)))
        self.assertEqual(result.unmapped_source_count, 1)

    def test_source_lists_are_numeric_sorted_and_response_order_independent(self):
        values = [row(n) for n in (10, 2, 3)]
        for value in values:
            value.update(vulnclass=MARKER, assets=None)
        result = self.diagnose(values)
        self.assertEqual(self.fields(result)["vulnclass"], (3, ("2", "3", "10")))
        self.assertEqual(result.asset_refs_not_collected_source_ids, ("2", "3", "10"))
        self.assertEqual(result, self.diagnose(values[::-1]))

    def test_shared_standalone_findings_are_distinct_source_rows(self):
        values = [row(1, standalone=123), row(2, standalone=123)]
        for value in values:
            value["vulnclass"] = MARKER
        result = self.diagnose(values)
        self.assertEqual((result.input_source_count, result.unmapped_source_count), (2, 2))
        self.assertEqual(self.fields(result)["vulnclass"], (2, ("1", "2")))

    def test_legacy_without_standalone_or_bindings_keeps_source_namespace(self):
        value = row(456)
        for field in ("finding_id", "traffic_bindings"):
            del value[field]
        value["vulnclass"] = MARKER
        self.assertEqual(self.fields(self.diagnose(value))["vulnclass"], (1, ("456",)))

    def test_array_detail_and_single_partial_page_are_explicit_shapes(self):
        for payload, shape in (([row(1)], "array"), (row(1), "detail"),
                               (page(2, [row(1)], total=100, size=20), "page")):
            with self.subTest(shape=shape):
                result = self.diagnose(payload)
                self.assertEqual((result.source_shape, result.input_source_count), (shape, 1))
                self.assertNotIn("complete", asdict(result))
                self.assertNotIn("ready", asdict(result))

    def test_page_array_requires_explicit_mode_and_reuses_merger(self):
        payload = pages()
        payload[1]["items"][0]["vulnclass"] = MARKER
        result = self.diagnose(payload, pages=True)
        self.assertEqual((result.source_shape, result.input_source_count), ("pages", 3))
        self.assertEqual(self.fields(result)["vulnclass"], (1, ("3",)))
        self.assertEqual(result, self.diagnose(payload[::-1], pages=True))
        self.reject(payload)
        self.reject([row(1)], pages=True)

    def test_empty_observed_inputs_are_not_turned_into_readiness(self):
        for payload, mode in (([], False), (page(1, [], total=0), False), ([page(1, [], total=0)], True)):
            result = self.diagnose(payload, pages=mode)
            self.assertEqual((result.input_source_count, result.unmapped_source_count), (0, 0))
            self.assertTrue(all(not r.source_ids and r.source_count == 0 for r in result.fields))
            self.assertNotIn("ready", asdict(result))
        self.reject([], pages=True)

    def test_incomplete_duplicate_and_inconsistent_page_arrays_fail(self):
        self.reject(pages()[:1], "INCOMPLETE_PAGES", pages=True)
        self.reject([pages()[0], pages()[0]], "DUPLICATE_PAGE", pages=True)
        payload = pages()
        payload[1]["total"] = 4
        self.reject(payload, "PAGINATION_MISMATCH", pages=True)
        payload = pages()
        payload[1]["items"][0]["id"] = "1"
        self.reject(payload, "DUPLICATE_SOURCE_ID", pages=True)

    def test_invalid_dtos_and_bindings_are_not_lowered_to_unmapped(self):
        for change in ({"id": "0"}, {"vulnclass": None}, {"severity": None}, {"status": MARKER},
                       {"unmapped_fields": []}, {MARKER: MARKER}):
            self.reject([row(1) | change])
        value = row(1)
        value["traffic_bindings"][0]["finding_id"] = "999"
        self.reject([value], "OWNER_MISMATCH")
        self.reject([row(1), row(1)], "DUPLICATE")

    def test_extracted_json_and_untrusted_flags_are_not_accepted_as_input(self):
        projected = asdict(extract_baseline([row(1)], vocabulary(), source_sha=UPSTREAM_COMMIT))
        self.reject(projected)
        self.reject(row(1) | {"unmapped_fields": [MARKER]})

    def test_original_classification_mapping_status_and_narratives_are_not_output(self):
        value = row(1)
        value.update(vulnclass=MARKER, severity=MARKER, summary=MARKER, evidence=MARKER)
        value["assets"][0].update(type=MARKER, label=MARKER)
        value["traffic_bindings"][0].update(note=MARKER, snapshot={"req_head": MARKER})
        output = json.dumps(asdict(self.diagnose([value])))
        for text in (MARKER, "SQL Injection", "sqli", "confirmed", "snapshot", "oracle_value", "finding_id"):
            self.assertNotIn(text, output)

    def test_source_pin_mapping_and_boolean_mode_are_validated(self):
        self.reject([row(1)], "UNSUPPORTED_COMMIT", source_sha=MARKER)
        self.reject([row(1)], vocabulary={})
        self.reject([row(1)], vocabulary={"vulnclasses": {MARKER: MARKER}, "asset_types": {}})
        for mode in (1, 0, "false", None):
            self.reject([row(1)], "EXPECTED_BOOL", pages=mode)

    def test_empty_mapping_reports_unmapped_classification_but_known_severity(self):
        result = self.diagnose([row(1)], vocabulary={"vulnclasses": {}, "asset_types": {}})
        self.assertEqual(self.fields(result), {"vulnclass": (1, ("1",)),
                         "assets.type": (1, ("1",)), "severity": (0, ())})

    def test_input_and_mapping_are_unchanged_on_success_and_failure(self):
        payload, vocab = pages(), vocabulary()
        before = copy.deepcopy((payload, vocab))
        self.diagnose(payload, vocabulary=vocab, pages=True)
        self.assertEqual((payload, vocab), before)
        payload[1][MARKER] = MARKER
        before = copy.deepcopy((payload, vocab))
        self.reject(payload, vocabulary=vocab, pages=True)
        self.assertEqual((payload, vocab), before)

    def test_future_unknown_extractor_flag_is_not_silently_ignored(self):
        original = extract_baseline([row(1)], vocabulary(), source_sha=UPSTREAM_COMMIT)
        changed = replace(original, findings=(replace(original.findings[0], unmapped_fields=(MARKER,)),))
        with patch.object(check, "extract_baseline", return_value=changed):
            self.reject([row(1)], "UNSUPPORTED_UNMAPPED_FIELD")

    def test_pure_diagnosis_needs_no_environment_network_process_or_writes(self):
        with patch.object(socket, "socket", side_effect=AssertionError("no network")), \
                patch.object(subprocess, "Popen", side_effect=AssertionError("no process")), \
                patch.object(Path, "write_bytes", side_effect=AssertionError("no writes")), \
                patch.dict(os.environ, {}, clear=True):
            self.assertEqual(self.diagnose([row(1)]).input_source_count, 1)


class TestMappingCheckCLI(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.input, self.vocab = self.root / "findings.json", self.root / "vocabulary.json"
        self.input.write_text(json.dumps([row(1)]), encoding="utf-8")
        self.vocab.write_text(json.dumps(vocabulary()), encoding="utf-8")
        self.argv = [str(self.input), str(self.vocab), "--source-sha", UPSTREAM_COMMIT]

    def call(self, argv=None):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = check.main(self.argv if argv is None else argv)
        return code, out.getvalue(), err.getvalue()

    def assert_failure(self, argv=None, fragment=None):
        code, out, err = self.call(argv)
        self.assertEqual((code, out), (2, ""))
        self.assertNotIn(MARKER, err)
        if fragment:
            self.assertIn(fragment, err)

    def test_real_isolated_cli_uses_sibling_modules_and_writes_no_files(self):
        (self.root / "baseline_extract.py").write_text("raise RuntimeError('untrusted cwd')\n")
        before = {p.name: p.read_bytes() for p in self.root.iterdir()}
        result = subprocess.run([sys.executable, "-I", "-B", str(Path(check.__file__)), *self.argv],
                                cwd=self.root, capture_output=True, text=True, timeout=10, check=False)
        self.assertEqual((result.returncode, result.stderr), (0, ""))
        self.assertEqual(json.loads(result.stdout)["input_source_count"], 1)
        self.assertNotIn("SYNTHETIC-PRIVATE", result.stdout)
        self.assertEqual({p.name: p.read_bytes() for p in self.root.iterdir()}, before)

    def test_page_mode_and_numeric_id_order_are_deterministic(self):
        payload = pages()
        for part in payload:
            for value in part["items"]:
                value["vulnclass"] = MARKER
        self.input.write_text(json.dumps(payload), encoding="utf-8")
        first = self.call(self.argv + ["--pages"])
        self.assertEqual((first[0], first[2]), (0, ""))
        self.input.write_text(json.dumps(payload[::-1]), encoding="utf-8")
        self.assertEqual(self.call(self.argv + ["--pages"]), first)
        self.assert_failure()

    def test_late_error_discards_all_counts_and_unknown_cli_text(self):
        first = row(1)
        first["vulnclass"] = MARKER
        self.input.write_text(json.dumps([first, row(2) | {MARKER: MARKER}]), encoding="utf-8")
        self.assert_failure(fragment="UNKNOWN_FIELDS")
        for argv in ([], self.argv + ["--secret=" + MARKER], self.argv + [MARKER]):
            self.assert_failure(argv, "cli: INVALID_ARGUMENTS")

    def test_strict_json_and_invalid_encoding_fail_without_raw_errors(self):
        for raw in (b"{", b"\xff", b"[NaN]", b"[1e999]", b'{"id":"1","id":"2"}'):
            self.input.write_bytes(raw)
            self.assert_failure()
        self.input.write_text(json.dumps([row(1)]), encoding="utf-8")
        self.vocab.write_bytes(b'{"vulnclasses":{},"vulnclasses":{},"asset_types":{}}')
        self.assert_failure(fragment="DUPLICATE_KEY")

    def test_file_and_mapping_size_limits_and_missing_paths(self):
        with patch.object(check, "MAX_INPUT_BYTES", self.input.stat().st_size - 1):
            self.assert_failure(fragment="TOO_LARGE")
        with patch.object(check, "MAX_VOCABULARY_BYTES", self.vocab.stat().st_size - 1):
            self.assert_failure(fragment="TOO_LARGE")
        self.assert_failure([str(self.root / MARKER), *self.argv[1:]], "READ_FAILED")


if __name__ == "__main__":
    unittest.main()
