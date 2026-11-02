"""합성 finding stream의 페이지 분할·도착 순서에 대한 유한 조합 회귀.

기존 추출기의 배열 결과와 원래 ID 순서를 기준으로 병합 불변성을 검사한다.
페이지 수치는 분할별로 비교한다. 실제 수집 완전성·회차·출처를 인증하지 않는다.
제품 코드·기존 테스트·의존성은 변경하지 않고 합성 fixture만 재사용한다.
"""

from __future__ import annotations

import copy
import io
import itertools
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from dataclasses import asdict, replace
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import baseline_pages as merger
from baseline_extract import UPSTREAM_COMMIT, extract_baseline
from test_baseline_extract import finding, vocabulary

_SOURCE_IDS = (91, 2, 730, 11, 4, 60, 18, 3)
_SIZES = range(1, 10)
_STATUSES = ("pending", "in_progress", "confirmed", "resolved", "fixed",
             "false_positive", "ignored", "duplicate")
_MARKER = "SYNTHETIC-PRIVATE"


def _stream(total, *, optional=False):
    rows = []
    for index, source in enumerate(_SOURCE_IDS[:total]):
        row = finding()
        row.update(id=str(source), finding_id=str(501 + index % 2), status=_STATUSES[index])
        row["assets"].append({"id": "78", "type": "endpoint", "label": _MARKER})
        binding = row["traffic_bindings"][0]
        binding.update(id=str(1000 + index * 2), finding_id=row["finding_id"])
        row["traffic_bindings"].append(binding | {"id": str(1001 + index * 2), "role": "supporting"})
        if index % 2:
            row["assets"].reverse()
            row["traffic_bindings"].reverse()
        if optional:
            if index % 4 == 0:
                for field in ("finding_id", "assets", "traffic_bindings"):
                    del row[field]
            elif index % 4 == 1:
                row.update(finding_id=None, assets=None, traffic_bindings=None)
            elif index % 4 == 2:
                row.update(assets=[], traffic_bindings=[])
            else:
                row.update(vulnclass=_MARKER, severity=_MARKER)
                row["assets"][0]["type"] = _MARKER
        rows.append(row)
    return rows


def _partition(rows, size):
    starts = tuple(range(0, len(rows), size)) or (0,)
    return [{"items": copy.deepcopy(rows[start:start + size]), "total": len(rows),
             "page": number, "page_size": size} for number, start in enumerate(starts, 1)]


def _sampled_orders(count):
    order = tuple(range(count))
    return tuple(dict.fromkeys((order, order[::-1], order[1:] + order[:1])))


def _all_bounded_orders(count):
    # 4페이지 이하는 전 순열, 5~8페이지는 정·역방향의 모든 회전만 생성한다.
    if count <= 4:
        return tuple(itertools.permutations(range(count)))
    forward = tuple(range(count))
    return tuple(dict.fromkeys(order[offset:] + order[:offset]
                               for order in (forward, forward[::-1]) for offset in range(count)))


class TestBaselinePagesProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("offline synthetic pages only"))
            guard.start()
            self.addCleanup(guard.stop)

    def assert_merged(self, pages, rows, size):
        vocab = vocabulary()
        before = copy.deepcopy((pages, vocab))
        reference = extract_baseline(rows, vocab, source_sha=UPSTREAM_COMMIT).findings
        result = merger.merge_baseline_pages(pages, vocab, source_sha=UPSTREAM_COMMIT)
        self.assertEqual(result.findings, reference)
        self.assertEqual(tuple(item.source_id for item in result.findings), tuple(row["id"] for row in rows))
        self.assertEqual((result.declared_source_sha, result.declared_total, result.page_size, result.page_count),
                         (UPSTREAM_COMMIT, len(rows), size, len(range(0, len(rows), size)) or 1))
        self.assertEqual((pages, vocab), before)
        self.assertNotIn(_MARKER, json.dumps(asdict(result)))
        return result

    def assert_rejected(self, pages, message):
        vocab = vocabulary()
        before = copy.deepcopy((pages, vocab))
        with self.assertRaises(merger.BaselinePagesError) as caught:
            merger.merge_baseline_pages(pages, vocab, source_sha=UPSTREAM_COMMIT)
        self.assertEqual(str(caught.exception), message)
        self.assertEqual((pages, vocab), before)
        self.assertNotIn(_MARKER, str(caught.exception))

    def test_all_bounded_partitions_and_arrivals_preserve_stream_and_references(self):
        for optional, total, size in itertools.product((False, True), range(9), _SIZES):
            rows = _stream(total, optional=optional)
            pages = _partition(rows, size)
            for order in _all_bounded_orders(len(pages)):
                with self.subTest(optional=optional, total=total, size=size, order=order):
                    self.assert_merged([pages[index] for index in order], rows, size)

    def test_removing_each_page_is_rejected_in_sampled_arrival_orders(self):
        for total, size in itertools.product(range(9), _SIZES):
            pages = _partition(_stream(total, optional=True), size)
            for removed in range(len(pages)):
                changed = pages[:removed] + pages[removed + 1:]
                message = "pages: INCOMPLETE_PAGES" if changed else "pages: EXPECTED_NONEMPTY_PAGE_ARRAY"
                for order in _sampled_orders(len(changed)):
                    with self.subTest(total=total, size=size, removed=removed, order=order):
                        self.assert_rejected([changed[index] for index in order], message)

    def test_removing_each_row_is_rejected_without_filling_page_gaps(self):
        for total, size in itertools.product(range(1, 9), _SIZES):
            pages = _partition(_stream(total), size)
            for page_index, page in enumerate(pages):
                for row_index in range(len(page["items"])):
                    changed = copy.deepcopy(pages)
                    del changed[page_index]["items"][row_index]
                    for order in _sampled_orders(len(changed)):
                        with self.subTest(total=total, size=size, page=page_index, row=row_index, order=order):
                            self.assert_rejected([changed[index] for index in order], "pages: PAGE_LENGTH_MISMATCH")

    def test_appending_or_replacing_with_each_duplicate_page_is_rejected(self):
        for total, size in itertools.product(range(9), _SIZES):
            pages = _partition(_stream(total), size)
            for repeated in range(len(pages)):
                variants = [pages + [copy.deepcopy(pages[repeated])]]
                if len(pages) > 1:
                    replaced = copy.deepcopy(pages)
                    replaced[(repeated + 1) % len(pages)] = copy.deepcopy(pages[repeated])
                    variants.append(replaced)
                for mode, changed in enumerate(variants):
                    for order in _sampled_orders(len(changed)):
                        with self.subTest(total=total, size=size, repeated=repeated, mode=mode, order=order):
                            self.assert_rejected([changed[index] for index in order], "pages: DUPLICATE_PAGE")

    def assert_cross_page_duplicates(self, field):
        for total in range(2, 9):
            for size in range(1, total):
                pages = _partition(_stream(total), size)
                for left, right in itertools.combinations(range(len(pages)), 2):
                    changed = copy.deepcopy(pages)
                    source, target = changed[left]["items"][-1], changed[right]["items"][0]
                    if field == "source":
                        target["id"] = source["id"]
                    else:
                        target["traffic_bindings"][-1]["id"] = source["traffic_bindings"][-1]["id"]
                    # 각 페이지는 여전히 단독 유효하다. 중복은 집합 검증에서만 드러난다.
                    for page in changed:
                        extract_baseline(page, vocabulary(), source_sha=UPSTREAM_COMMIT)
                    for order in _sampled_orders(len(changed)):
                        with self.subTest(field=field, total=total, size=size, pair=(left, right), order=order):
                            self.assert_rejected([changed[index] for index in order],
                                                 "pages: DUPLICATE_" + field.upper() + "_ID")

    def test_source_duplicates_between_every_page_pair_are_rejected(self):
        self.assert_cross_page_duplicates("source")

    def test_binding_duplicates_between_every_page_pair_are_rejected(self):
        self.assert_cross_page_duplicates("binding")

    def test_cli_preserves_repartitioned_results_and_has_no_partial_output_on_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            input_path, vocab_path = root / "pages.json", root / "vocabulary.json"
            vocab_path.write_text(json.dumps(vocabulary()), encoding="utf-8")
            for total, size in itertools.product((0, 8), (1, 3, 8, 9)):
                rows = _stream(total, optional=True)
                pages = _partition(rows, size)
                for order in _sampled_orders(len(pages)):
                    payload = [pages[index] for index in order]
                    expected = self.assert_merged(payload, rows, size)
                    bad = copy.deepcopy(payload)
                    if total:
                        next(page for page in bad if page["items"])["items"].pop()
                        message = "pages: PAGE_LENGTH_MISMATCH"
                    else:
                        bad = []
                        message = "pages: EXPECTED_NONEMPTY_PAGE_ARRAY"
                    for valid, value in ((True, payload), (False, bad)):
                        with self.subTest(total=total, size=size, order=order, valid=valid):
                            input_path.write_text(json.dumps(value), encoding="utf-8")
                            before = {path.name: path.read_bytes() for path in root.iterdir()}
                            out, err = io.StringIO(), io.StringIO()
                            with redirect_stdout(out), redirect_stderr(err):
                                code = merger.main([str(input_path), str(vocab_path), "--source-sha", UPSTREAM_COMMIT])
                            if valid:
                                self.assertEqual((code, err.getvalue()), (0, ""))
                                self.assertEqual(json.loads(out.getvalue()), json.loads(json.dumps(asdict(expected))))
                            else:
                                self.assertEqual((code, out.getvalue(), err.getvalue()), (2, "", message + "\n"))
                            self.assertNotIn(_MARKER, out.getvalue() + err.getvalue())
                            self.assertEqual({path.name: path.read_bytes() for path in root.iterdir()}, before)

    def test_assertions_detect_changed_order_and_disabled_batch_guards(self):
        rows = _stream(3)
        pages = _partition(rows, 1)
        original_merge, original_fail = merger.merge_baseline_pages, merger._fail

        def reorder_by_source(payload, vocab, *, source_sha):
            result = original_merge(payload, vocab, source_sha=source_sha)
            return replace(result, findings=tuple(sorted(result.findings, key=lambda row: int(row.source_id))))

        with patch.object(merger, "merge_baseline_pages", side_effect=reorder_by_source):
            with self.assertRaises(AssertionError):
                self.assert_merged(pages[::-1], rows, 1)

        missing = pages[:-1]
        short = copy.deepcopy(pages)
        short[-1]["items"].clear()
        source, binding = copy.deepcopy(pages), copy.deepcopy(pages)
        source[-1]["items"][0]["id"] = source[0]["items"][0]["id"]
        binding[-1]["items"][0]["traffic_bindings"][-1]["id"] = binding[0]["items"][0]["traffic_bindings"][-1]["id"]
        for code, payload in (("INCOMPLETE_PAGES", missing), ("PAGE_LENGTH_MISMATCH", short),
                              ("DUPLICATE_SOURCE_ID", source), ("DUPLICATE_BINDING_ID", binding)):
            def omit_one_guard(value):
                if value != code:
                    original_fail(value)
            with self.subTest(guard=code), patch.object(merger, "_fail", side_effect=omit_one_guard):
                with self.assertRaises(AssertionError):
                    self.assert_rejected(payload, "pages: " + code)


if __name__ == "__main__":
    unittest.main()
