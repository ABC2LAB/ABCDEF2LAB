"""분류 매핑 진단 연결의 유한 조합 회귀.

합성 FindingDTO와 vocabulary만 사용한다. 추출기/페이지 병합기의 결과를 기대값으로
재사용하지 않고, 입력에 선언한 미등록 field와 source id에서 진단 기대값을 만든다.
"""

from __future__ import annotations

import copy
import itertools
import json
import os
import socket
import subprocess
import sys
import unittest
from dataclasses import asdict, replace
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import baseline_mapping_check as check
from baseline_extract import UPSTREAM_COMMIT
from test_baseline_extract import vocabulary
from test_baseline_pages import page, row

_MARKER = "SYNTHETIC-PRIVATE-DO-NOT-PRINT"
_FIELDS = ("vulnclass", "assets.type", "severity")


def _rows(source_ids: tuple[int, ...], masks: tuple[int, ...], *, assets_mode: str = "known") -> list[dict]:
    result = []
    for source, mask in zip(source_ids, masks):
        value = row(source)
        if mask & 1:
            value["vulnclass"] = _MARKER
        if mask & 2:
            value["assets"][0]["type"] = _MARKER
        if mask & 4:
            value["severity"] = _MARKER
        if assets_mode == "absent":
            del value["assets"]
        elif assets_mode == "null":
            value["assets"] = None
        elif assets_mode == "empty":
            value["assets"] = []
        elif assets_mode == "multiple-unknown":
            value["assets"] = [{"id": str(source * 10 + n), "type": _MARKER, "label": _MARKER}
                                 for n in (1, 2, 3)]
        result.append(value)
    return result


class TestBaselineMappingProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("offline synthetic mapping only"))
            guard.start()
            self.addCleanup(guard.stop)

    def diagnose(self, payload, *, vocab=None, pages=False):
        return check.diagnose_baseline_mapping(payload, vocabulary() if vocab is None else vocab,
                                               source_sha=UPSTREAM_COMMIT, pages=pages)

    def fields(self, result):
        return {entry.field: (entry.source_count, entry.source_ids) for entry in result.fields}

    def expected(self, source_ids: tuple[str, ...], masks: tuple[int, ...], *, unavailable=()):
        self.assertEqual(len(source_ids), len(masks))
        by_field = {field: [] for field in _FIELDS}
        union = set()
        for source, mask in zip(source_ids, masks):
            for index, field in enumerate(_FIELDS):
                if mask & (1 << index):
                    by_field[field].append(source)
                    union.add(source)
        ordered = lambda values: tuple(sorted(values, key=int))
        fields = {field: (len(values), ordered(values)) for field, values in by_field.items()}
        return fields, len(union), (len(unavailable), ordered(unavailable))

    def assert_report(self, result, source_ids, masks, *, unavailable=(), shape="array"):
        self.assertEqual(len(source_ids), len(masks))
        expected_fields, expected_union, expected_unavailable = self.expected(source_ids, masks, unavailable=unavailable)
        self.assertEqual(result.source_shape, shape)
        self.assertEqual((result.input_source_count, result.unmapped_source_count),
                         (len(source_ids), expected_union))
        self.assertEqual(self.fields(result), expected_fields)
        self.assertEqual((result.asset_refs_not_collected_count, result.asset_refs_not_collected_source_ids),
                         expected_unavailable)

    def test_all_field_masks_keep_field_counts_distinct_from_union(self):
        source_ids = ("1", "2", "3", "4")
        for masks in itertools.product(range(8), repeat=len(source_ids)):
            with self.subTest(masks=masks):
                result = self.diagnose(_rows(tuple(map(int, source_ids)), masks))
                self.assert_report(result, source_ids, masks)
                self.assertEqual(result.declared_source_sha, UPSTREAM_COMMIT)
        clean = self.diagnose(_rows((1, 2, 3, 4), (0, 0, 0, 0)))
        self.assert_report(clean, source_ids, (0, 0, 0, 0))

    def test_multiple_unknown_assets_count_one_source_once_and_preserve_other_fields(self):
        result = self.diagnose(_rows((7,), (1,), assets_mode="multiple-unknown"))
        self.assert_report(result, ("7",), (3,))
        self.assertEqual(self.fields(result)["assets.type"], (1, ("7",)))

    def test_asset_absent_null_empty_and_unknown_have_separate_meanings(self):
        modes = ("absent", "null", "empty", "multiple-unknown")
        for mode in modes:
            with self.subTest(mode=mode):
                result = self.diagnose(_rows((1,), (0,), assets_mode=mode))
                if mode in ("absent", "null"):
                    self.assert_report(result, ("1",), (0,), unavailable=("1",))
                elif mode == "empty":
                    self.assert_report(result, ("1",), (0,))
                else:
                    self.assert_report(result, ("1",), (2,))

    def test_numeric_source_ids_and_response_order_do_not_change_report(self):
        source_ids = ("10", "2", "3", "1")
        masks = (1, 2, 4, 7)
        payload = _rows(tuple(map(int, source_ids)), masks)
        first = self.diagnose(payload)
        for order in (tuple(reversed(range(4))), (1, 3, 0, 2), (2, 0, 3, 1)):
            with self.subTest(order=order):
                actual = self.diagnose([copy.deepcopy(payload[index]) for index in order])
                self.assertEqual(actual, first)
        self.assertEqual(self.fields(first)["vulnclass"], (2, ("1", "10")))
        self.assertEqual(self.fields(first)["assets.type"], (2, ("1", "2")))
        self.assertEqual(self.fields(first)["severity"], (2, ("1", "3")))

    def test_valid_page_partitions_and_arrival_orders_preserve_mapping_report(self):
        source_ids = ("1", "2", "3", "10", "20")
        masks = (3, 3, 4, 5, 6)
        rows = _rows(tuple(map(int, source_ids)), masks)
        expected_fields, expected_union, _ = self.expected(source_ids, masks)
        row_orders = (tuple(range(5)), tuple(reversed(range(5))), (2, 3, 4, 0, 1))
        for size in (1, 2, 3, 5):
            for row_order in row_orders:
                ordered_rows = [rows[index] for index in row_order]
                chunks = [ordered_rows[start:start + size] for start in range(0, len(ordered_rows), size)]
                pages = [page(number, chunk, total=5, size=size) for number, chunk in enumerate(chunks, start=1)]
                page_orders = tuple(itertools.permutations(range(len(pages)))) if len(pages) == 3 else (
                    tuple(range(len(pages))), tuple(reversed(range(len(pages)))),
                    tuple(range(1, len(pages))) + (0,) if len(pages) > 1 else (0,))
                for page_order in dict.fromkeys(page_orders):
                    with self.subTest(size=size, row_order=row_order, page_order=page_order):
                        result = self.diagnose([copy.deepcopy(pages[index]) for index in page_order], pages=True)
                        self.assert_report(result, source_ids, masks, shape="pages")
                        self.assertEqual(self.fields(result), expected_fields)
                        self.assertEqual(result.unmapped_source_count, expected_union)
                array_result = self.diagnose(copy.deepcopy(ordered_rows))
                self.assert_report(array_result, source_ids, masks)
                self.assertEqual(self.fields(array_result), expected_fields)

    def test_source_shapes_are_reported_without_claiming_same_shape(self):
        value = _rows((9,), (1,))[0]
        for payload, mode, shape in (([copy.deepcopy(value)], False, "array"),
                                     (copy.deepcopy(value), False, "detail"),
                                     (page(1, [copy.deepcopy(value)], total=1, size=2), False, "page"),
                                     ([page(1, [copy.deepcopy(value)], total=1, size=2)], True, "pages")):
            with self.subTest(shape=shape):
                result = self.diagnose(payload, pages=mode)
                self.assert_report(result, ("9",), (1,), shape=shape)

    def test_input_vocabulary_and_failure_outputs_remain_private_and_unchanged(self):
        payload = _rows((1, 2, 3), (1, 2, 4))
        vocab = vocabulary()
        before = copy.deepcopy((payload, vocab))
        result = self.diagnose(payload, vocab=vocab)
        self.assertEqual((payload, vocab), before)
        output = json.dumps(asdict(result))
        self.assertNotIn(_MARKER, output)
        bad = copy.deepcopy(payload)
        bad[1][_MARKER] = _MARKER
        bad_before = copy.deepcopy((bad, vocab))
        with self.assertRaises(check.MappingCheckError) as caught:
            self.diagnose(bad, vocab=vocab)
        self.assertIn("UNKNOWN_FIELDS", str(caught.exception))
        self.assertNotIn(_MARKER, str(caught.exception))
        self.assertEqual((bad, vocab), bad_before)

    def test_invalid_dto_or_binding_is_rejected_instead_of_counted_as_unmapped(self):
        changes = ({"id": "0"}, {"vulnclass": None}, {"severity": None}, {"status": _MARKER},
                   {"unmapped_fields": []}, {_MARKER: _MARKER})
        for change in changes:
            with self.subTest(change=tuple(change)):
                with self.assertRaises(check.MappingCheckError):
                    self.diagnose([_rows((1,), (0,))[0] | change])
        value = _rows((1,), (0,))[0]
        value["traffic_bindings"][0]["finding_id"] = "999"
        with self.assertRaisesRegex(check.MappingCheckError, "OWNER_MISMATCH"):
            self.diagnose([value])

    def test_pure_diagnosis_does_not_use_network_process_or_environment(self):
        with patch.object(socket, "socket", side_effect=AssertionError("network")), \
                patch.object(subprocess, "Popen", side_effect=AssertionError("process")), \
                patch.dict(os.environ, {}, clear=True):
            result = self.diagnose(_rows((1, 2), (1, 0)))
            self.assertEqual(result.input_source_count, 2)

    def test_independent_expectations_detect_weakened_diagnosis_guards(self):
        payload = _rows((1, 2, 3), (3, 3, 4))
        expected = self.diagnose(payload)
        self.assert_report(expected, ("1", "2", "3"), (3, 3, 4))
        original = check.diagnose_baseline_mapping

        def omit_assets(*args, **kwargs):
            result = original(*args, **kwargs)
            fields = tuple(entry for entry in result.fields if entry.field != "assets.type")
            return replace(result, fields=fields)

        self.assert_report(self.diagnose(payload), ("1", "2", "3"), (3, 3, 4))
        with patch.object(check, "diagnose_baseline_mapping", side_effect=omit_assets):
            with self.assertRaises(AssertionError):
                self.assert_report(self.diagnose(payload), ("1", "2", "3"), (3, 3, 4))

        def collapse_union(*args, **kwargs):
            result = original(*args, **kwargs)
            total_fields = sum(entry.source_count for entry in result.fields)
            return replace(result, unmapped_source_count=total_fields)

        self.assert_report(self.diagnose(payload), ("1", "2", "3"), (3, 3, 4))
        with patch.object(check, "diagnose_baseline_mapping", side_effect=collapse_union):
            with self.assertRaises(AssertionError):
                self.assert_report(self.diagnose(payload), ("1", "2", "3"), (3, 3, 4))

        payload = _rows((1, 2, 3), (3, 3, 2))
        def unsorted_ids(*args, **kwargs):
            result = original(*args, **kwargs)
            fields = tuple(replace(entry, source_ids=tuple(reversed(entry.source_ids)))
                           for entry in result.fields)
            return replace(result, fields=fields)

        self.assert_report(self.diagnose(payload), ("1", "2", "3"), (3, 3, 2))
        with patch.object(check, "diagnose_baseline_mapping", side_effect=unsorted_ids):
            with self.assertRaises(AssertionError):
                self.assert_report(self.diagnose(payload), ("1", "2", "3"), (3, 3, 2))


if __name__ == "__main__":
    unittest.main()
