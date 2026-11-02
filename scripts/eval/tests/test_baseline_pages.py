"""다중 페이지 선언 검증: 고정 upstream 모양의 합성 JSON만 사용한다."""

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
from dataclasses import asdict
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import baseline_pages as merger
from baseline_extract import UPSTREAM_COMMIT
from test_baseline_extract import finding, vocabulary

MARKER = "SYNTHETIC-PRIVATE-DO-NOT-PRINT"


def row(source, standalone=None, binding=None):
    result = finding()
    result["id"] = str(source)
    result["finding_id"] = str(source + 100 if standalone is None else standalone)
    result["traffic_bindings"][0].update(id=str(source + 200 if binding is None else binding),
                                         finding_id=result["finding_id"])
    return result


def page(number, rows, *, total=3, size=2):
    return {"items": rows, "total": total, "page": number, "page_size": size}


def pages():
    return [page(1, [row(1), row(2)]), page(2, [row(3)])]


class TestMergeBaselinePages(unittest.TestCase):
    def merge(self, payload, **kwargs):
        return merger.merge_baseline_pages(payload, kwargs.pop("vocabulary", vocabulary()),
                                           source_sha=kwargs.pop("source_sha", UPSTREAM_COMMIT), **kwargs)

    def reject(self, payload, code=None, **kwargs):
        with self.assertRaises(merger.BaselinePagesError) as caught:
            self.merge(payload, **kwargs)
        message = str(caught.exception)
        if code:
            self.assertIn(code, message)
        self.assertNotIn(MARKER, message)
        return message

    def test_merges_all_pages_with_declared_metadata_and_neutral_findings(self):
        result = self.merge(pages())
        self.assertEqual((result.declared_source_sha, result.declared_total, result.page_size, result.page_count),
                         (UPSTREAM_COMMIT, 3, 2, 2))
        self.assertEqual([r.source_id for r in result.findings], ["1", "2", "3"])
        self.assertEqual([r.finding_id for r in result.findings], ["101", "102", "103"])
        self.assertTrue(all(r.source_status == "confirmed" for r in result.findings))
        self.assertTrue(all(r.vuln_class == "sqli" for r in result.findings))
        self.assertNotIn("oracle_value", json.dumps(asdict(result)))
        self.assertNotIn("complete", asdict(result))

    def test_page_input_order_is_normalized_and_item_order_is_preserved(self):
        payload = [page(1, [row(9), row(2)]), page(2, [row(1)])]
        expected = asdict(self.merge(payload))
        self.assertEqual(expected, asdict(self.merge(payload[::-1])))
        self.assertEqual([r.source_id for r in self.merge(payload).findings], ["9", "2", "1"])

    def test_three_pages_and_partial_last_page(self):
        payload = [page(3, [row(5)], total=5), page(1, [row(1), row(2)], total=5),
                   page(2, [row(3), row(4)], total=5)]
        self.assertEqual([r.source_id for r in self.merge(payload).findings], [str(n) for n in range(1, 6)])

    def test_exact_multiple_does_not_require_or_accept_extra_empty_page(self):
        payload = [page(1, [row(1), row(2)], total=4), page(2, [row(3), row(4)], total=4)]
        self.assertEqual(len(self.merge(payload).findings), 4)
        self.reject(payload + [page(3, [], total=4)], "INCOMPLETE_PAGES")

    def test_single_partial_or_full_page(self):
        for size in (2, 20):
            with self.subTest(size=size):
                result = self.merge([page(1, [row(1), row(2)], total=2, size=size)])
                self.assertEqual((result.declared_total, result.page_count, len(result.findings)), (2, 1, 2))

    def test_zero_total_requires_one_empty_first_page(self):
        for size in (1, 20):
            result = self.merge([page(1, [], total=0, size=size)])
            self.assertEqual((result.declared_total, result.page_count, result.findings), (0, 1, ()))
        self.reject([], "EXPECTED_NONEMPTY_PAGE_ARRAY")
        for number in (2, 99):
            self.reject([page(number, [], total=0)], "PAGE_SEQUENCE_GAP")
        self.reject([page(1, [row(1)], total=0)], "INCONSISTENT_COUNTS")

    def test_missing_first_middle_or_last_page_is_rejected(self):
        payload = [page(n, [row(n)], total=3, size=1) for n in (1, 2, 3)]
        for missing in range(3):
            with self.subTest(missing=missing):
                self.reject(payload[:missing] + payload[missing + 1:], "INCOMPLETE_PAGES")

    def test_duplicate_page_is_not_silently_deduplicated(self):
        self.reject([pages()[0], pages()[0]], "DUPLICATE_PAGE")
        self.reject(pages() + [pages()[1]], "DUPLICATE_PAGE")

    def test_page_number_gap_is_rejected_even_if_page_count_matches(self):
        self.reject([page(1, [row(1)], total=3, size=1), page(2, [row(2)], total=3, size=1),
                     page(99, [], total=3, size=1)], "PAGE_SEQUENCE_GAP")

    def test_total_and_page_size_must_match_all_pages(self):
        payload = pages()
        self.reject([payload[0], payload[1] | {"total": 4}], "PAGINATION_MISMATCH")
        self.reject([payload[0], payload[1] | {"page_size": 1}], "PAGINATION_MISMATCH")

    def test_underfull_middle_and_last_pages_are_not_filled(self):
        self.reject([page(1, [row(1)]), page(2, [row(3)])], "PAGE_LENGTH_MISMATCH")
        self.reject([pages()[0], page(2, [])], "PAGE_LENGTH_MISMATCH")
        self.reject([page(1, [], total=1)], "PAGE_LENGTH_MISMATCH")

    def test_overfull_pages_are_rejected_by_existing_extractor(self):
        self.reject([page(1, [row(1), row(2)], total=2, size=1)], "INCONSISTENT_COUNTS")
        self.reject([page(1, [row(1)], total=1), page(2, [row(2)], total=1)], "INCONSISTENT_COUNTS")

    def test_invalid_page_scalar_types_and_missing_fields_are_rejected(self):
        for field, values in (("total", (True, -1, 1.5, "3", None)),
                              ("page", (False, 0, -1, 1.5, "1", None)),
                              ("page_size", (True, 0, -1, 1.5, "2", None))):
            for value in values:
                with self.subTest(field=field, value=value):
                    self.reject([pages()[0] | {field: value}], "INVALID_INTEGER")
            raw = pages()[0]
            del raw[field]
            self.reject([raw], "INVALID_INTEGER")

    def test_nonpages_are_not_mixed_into_page_array(self):
        for payload in (None, True, {}, {"pages": pages()}, "pages", (pages()[0],)):
            self.reject(payload, "EXPECTED_NONEMPTY_PAGE_ARRAY")
        for raw in (row(1), [], None, True, {"data": []}):
            self.reject([raw], "EXPECTED_PAGE_OBJECT")
        self.reject([page(1, None)], "EXPECTED_ARRAY")

    def test_source_id_duplicates_across_pages_are_rejected(self):
        payload = [page(1, [row(1)], total=2, size=1),
                   page(2, [row(1, standalone=102, binding=202)], total=2, size=1)]
        self.reject(payload, "DUPLICATE_SOURCE_ID")

    def test_binding_id_duplicates_across_pages_are_rejected(self):
        payload = [page(1, [row(1, standalone=101, binding=201)], total=2, size=1),
                   page(2, [row(2, standalone=101, binding=201)], total=2, size=1)]
        self.reject(payload, "DUPLICATE_BINDING_ID")

    def test_shared_standalone_finding_and_asset_references_remain_allowed(self):
        payload = [page(1, [row(1, standalone=101, binding=201)], total=2, size=1),
                   page(2, [row(2, standalone=101, binding=202)], total=2, size=1)]
        result = self.merge(payload)
        self.assertEqual([r.finding_id for r in result.findings], ["101", "101"])
        self.assertEqual([r.asset_refs[0].id for r in result.findings], ["77", "77"])

    def test_existing_per_page_id_and_binding_owner_validation_is_preserved(self):
        self.reject([page(1, [row(1), row(1)], total=2)], "DUPLICATE")
        value = row(1)
        value["traffic_bindings"][0]["finding_id"] = "999"
        self.reject([page(1, [value], total=1)], "OWNER_MISMATCH")
        value = row(1)
        value["assets"] *= 2
        self.reject([page(1, [value], total=1)], "DUPLICATE")

    def test_null_missing_and_empty_optional_refs_keep_distinct_meanings(self):
        first, second, third = row(1), row(2), row(3)
        for field in ("finding_id", "assets", "traffic_bindings"):
            del first[field]
            second[field] = None
        third.update(assets=[], traffic_bindings=[])
        result = self.merge([page(1, [first, second, third], total=3, size=3)])
        self.assertEqual([(r.finding_id, r.asset_refs, r.traffic_binding_refs) for r in result.findings],
                         [(None, None, None), (None, None, None), ("103", (), ())])

    def test_unmapped_classification_and_unknown_severity_do_not_expose_raw_values(self):
        value = row(1)
        value.update(vulnclass=MARKER, severity=MARKER, summary=MARKER, evidence=MARKER)
        value["assets"][0].update(type=MARKER, label=MARKER)
        value["traffic_bindings"][0].update(note=MARKER, snapshot={"secret": MARKER})
        result = self.merge([page(1, [value], total=1)])
        item = result.findings[0]
        self.assertIsNone(item.vuln_class)
        self.assertIsNone(item.severity)
        self.assertIsNone(item.asset_refs[0].type)
        self.assertEqual(item.unmapped_fields, ("assets.type", "severity", "vulnclass"))
        self.assertNotIn(MARKER, json.dumps(asdict(result)))
        self.assertNotIn("snapshot", json.dumps(asdict(result)))

    def test_source_pin_and_vocabulary_are_checked_for_the_whole_batch(self):
        self.reject(pages(), "UNSUPPORTED_COMMIT", source_sha="0" * 40)
        self.reject(pages(), "EXPECTED_OBJECT", vocabulary={})
        self.reject(pages(), "INVALID_MAPPING", vocabulary={"vulnclasses": {MARKER: MARKER}, "asset_types": {}})
        empty = self.merge(pages(), vocabulary={"vulnclasses": {}, "asset_types": {}})
        self.assertTrue(all(r.vuln_class is None for r in empty.findings))

    def test_unknown_fields_and_late_failure_never_echo_untrusted_names(self):
        payload = pages()
        payload[1][MARKER] = MARKER
        self.reject(payload, "UNKNOWN_FIELDS")
        payload = pages()
        payload[1]["items"][0][MARKER] = MARKER
        self.reject(payload, "UNKNOWN_FIELDS")

    def test_input_is_not_modified_on_success_or_late_failure(self):
        payload, vocab = pages(), vocabulary()
        before = copy.deepcopy((payload, vocab))
        self.merge(payload, vocabulary=vocab)
        self.assertEqual((payload, vocab), before)
        payload[1]["items"][0]["id"] = "1"
        before = copy.deepcopy(payload)
        self.reject(payload)
        self.assertEqual(payload, before)

    def test_huge_declared_page_count_does_not_construct_a_range_of_that_size(self):
        self.reject([page(1, [row(1)], total=10 ** 300, size=1)], "INCOMPLETE_PAGES")

    def test_api_has_no_environment_network_or_process_dependency(self):
        with patch.object(socket, "socket", side_effect=AssertionError("no network")), \
                patch.object(subprocess, "Popen", side_effect=AssertionError("no process")), \
                patch.dict(os.environ, {}, clear=True):
            self.assertEqual(len(self.merge(pages()).findings), 3)


class TestBaselinePagesCLI(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.input = self.root / "pages.json"
        self.vocab = self.root / "vocabulary.json"
        self.input.write_text(json.dumps(pages()), encoding="utf-8")
        self.vocab.write_text(json.dumps(vocabulary()), encoding="utf-8")
        self.argv = [str(self.input), str(self.vocab), "--source-sha", UPSTREAM_COMMIT]

    def call(self, argv=None):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = merger.main(self.argv if argv is None else argv)
        return code, out.getvalue(), err.getvalue()

    def assert_failure(self, argv=None, fragment=None):
        code, out, err = self.call(argv)
        self.assertEqual((code, out), (2, ""))
        self.assertNotIn(MARKER, err)
        if fragment:
            self.assertIn(fragment, err)

    def test_real_cli_outputs_only_neutral_json_and_does_not_write_files(self):
        (self.root / "baseline_extract.py").write_text("raise RuntimeError('untrusted cwd module')\n")
        before = {p.name: p.read_bytes() for p in self.root.iterdir()}
        result = subprocess.run([sys.executable, "-I", "-B", str(Path(merger.__file__)), *self.argv],
                                cwd=self.root, capture_output=True, text=True, check=False, timeout=10)
        self.assertEqual((result.returncode, result.stderr), (0, ""))
        payload = json.loads(result.stdout)
        self.assertEqual((payload["declared_total"], payload["page_count"]), (3, 2))
        self.assertNotIn("SYNTHETIC-PRIVATE", result.stdout)
        self.assertEqual({p.name: p.read_bytes() for p in self.root.iterdir()}, before)

    def test_invalid_page_after_valid_page_has_empty_stdout(self):
        payload = pages()
        payload[1][MARKER] = MARKER
        self.input.write_text(json.dumps(payload), encoding="utf-8")
        self.assert_failure(fragment="UNKNOWN_FIELDS")

    def test_parser_errors_do_not_repeat_raw_arguments(self):
        for argv in ([], self.argv + ["--secret=" + MARKER], [MARKER], self.argv + [MARKER]):
            with self.subTest(argument_count=len(argv)):
                self.assert_failure(argv, "cli: INVALID_ARGUMENTS")

    def test_missing_input_path_does_not_leak_filename(self):
        self.assert_failure([str(self.root / MARKER), *self.argv[1:]], "READ_FAILED")

    def test_strict_json_rejects_duplicates_invalid_encoding_and_nonfinite_values(self):
        for raw in (b"{", b"\xff", b'[{"items":[],"items":[],"page":1,"page_size":1,"total":0}]',
                    b"[NaN]", b"[Infinity]", b"[1e999]"):
            self.input.write_bytes(raw)
            self.assert_failure()
        self.input.write_text(json.dumps(pages()), encoding="utf-8")
        self.vocab.write_bytes(b'{"vulnclasses":{},"vulnclasses":{},"asset_types":{}}')
        self.assert_failure(fragment="DUPLICATE_KEY")

    def test_batch_and_vocabulary_size_limits_are_enforced(self):
        with patch.object(merger, "MAX_INPUT_BYTES", self.input.stat().st_size - 1):
            self.assert_failure(fragment="TOO_LARGE")
        with patch.object(merger, "MAX_VOCABULARY_BYTES", self.vocab.stat().st_size - 1):
            self.assert_failure(fragment="TOO_LARGE")

    def test_output_is_deterministic_when_pages_arrive_in_reverse_order(self):
        first = self.call()
        self.input.write_text(json.dumps(pages()[::-1], sort_keys=True), encoding="utf-8")
        self.assertEqual(self.call(), first)


if __name__ == "__main__":
    unittest.main()
