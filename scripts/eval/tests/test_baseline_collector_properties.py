"""ARTEX 페이지 수집 루프의 예산·중지 경계 조합 회귀.

실제 소켓/서버/토큰은 사용하지 않는다. 합성 페이지 bytes를 메모리 요청기로 넣어
페이지 순서, page/누적 byte cap, 실패 후 추가 조회 중지를 검증한다.
"""

from __future__ import annotations

import copy
import io
import json
import os
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import baseline_pages_collector as collector
from baseline_extract import UPSTREAM_COMMIT
from test_baseline_pages import page, row
from test_baseline_extract import vocabulary

_TOKEN = "synthetic-token-private"


class _MemoryResponse:
    """Minimal HTTPResponse shape consumed by the production framing reader."""

    def __init__(self, body: bytes, *, chunked: bool = False):
        self.status = 200
        if chunked:
            wire = (format(len(body), "x").encode("ascii") + b"\r\n" + body
                    + b"\r\n0\r\n\r\n")
            self.fp = io.BytesIO(wire)
            self._headers = {"Content-Type": "application/json",
                             "Content-Encoding": "identity",
                             "Transfer-Encoding": "chunked"}
        else:
            self.fp = io.BytesIO(body)
            self._headers = {"Content-Type": "application/json",
                             "Content-Encoding": "identity",
                             "Content-Length": str(len(body))}

    def getheader(self, name, default=None):
        return self._headers.get(name, default)


def _pages(total: int, size: int) -> list[dict]:
    rows = [row(number) for number in range(1, total + 1)]
    return [page(number, rows[start:start + size], total=total, size=size)
            for number, start in ((number, (number - 1) * size)
                                  for number in range(1, max(1, (total + size - 1) // size) + 1))]


def _body(payload: dict) -> bytes:
    payload = copy.deepcopy(payload)
    if payload["items"]:
        payload["items"][0]["summary"] = "합성-UTF8-경계"
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


class TestBaselineCollectorProperties(unittest.TestCase):
    def setUp(self):
        self.vocab = vocabulary()
        self.origin = "http://127.0.0.1:3000"
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("collector properties must stay offline"))
            guard.start()
            self.addCleanup(guard.stop)

    def config(self, *, page_size=2, max_page_bytes=65536, max_total_bytes=65536, max_pages=10):
        return collector.BaselinePagesEndpoint(
            self.origin, page_size, 1.0, max_page_bytes, max_total_bytes, max_pages
        )

    def collect(self, config, pages, *, vocab=None, mutate_vocab=False, chunked=False):
        requests = []
        pages_by_number = {payload["page"]: _body(payload)
                           for payload in pages}
        original_vocab = self.vocab if vocab is None else vocab
        before_pages = copy.deepcopy(pages)
        before_config = copy.deepcopy(config)
        before_vocab = copy.deepcopy(original_vocab)

        def request(_config, _endpoint, _token, number, maximum):
            requests.append((number, maximum))
            if mutate_vocab and number == 1:
                original_vocab["vulnclasses"].clear()
                original_vocab["asset_types"].clear()
            body = pages_by_number[number]
            return collector._read_response(_MemoryResponse(body, chunked=chunked), maximum)

        with patch.object(collector, "_request", side_effect=request):
            result = collector.collect_baseline_pages(
                config, original_vocab, source_sha=UPSTREAM_COMMIT, bearer_token=_TOKEN
            )
        self.assertEqual(pages, before_pages)
        self.assertEqual(config, before_config)
        if not mutate_vocab:
            self.assertEqual(original_vocab, before_vocab)
        return result, requests, pages_by_number

    def collect_failure(self, config, pages, *, vocab=None, chunked=False):
        requests = []
        pages_by_number = {payload["page"]: _body(payload) for payload in pages}
        original_vocab = self.vocab if vocab is None else vocab
        before_pages = copy.deepcopy(pages)
        before_config = copy.deepcopy(config)
        before_vocab = copy.deepcopy(original_vocab)

        def request(_config, _endpoint, _token, number, maximum):
            requests.append((number, maximum))
            return collector._read_response(_MemoryResponse(pages_by_number[number], chunked=chunked), maximum)

        with patch.object(collector, "_request", side_effect=request):
            try:
                collector.collect_baseline_pages(config, original_vocab,
                                                 source_sha=UPSTREAM_COMMIT,
                                                 bearer_token=_TOKEN)
            except collector.BaselineCollectionError as error:
                self.assertEqual(pages, before_pages)
                self.assertEqual(config, before_config)
                self.assertEqual(original_vocab, before_vocab)
                self.assertNotIn(_TOKEN, str(error))
                return error, requests
        self.fail("expected a bounded collection failure")

    def test_page_size_matrix_requests_each_page_once_and_merges_declared_rows(self):
        for size, total in ((1, 1), (1, 4), (2, 5), (3, 7), (5, 5)):
            with self.subTest(size=size, total=total):
                payloads = _pages(total, size)
                result, calls, _ = self.collect(self.config(page_size=size), payloads)
                self.assertEqual([number for number, _ in calls], list(range(1, len(payloads) + 1)))
                self.assertEqual([finding.source_id for finding in result.findings],
                                 [str(number) for number in range(1, total + 1)])
                self.assertEqual((result.declared_total, result.page_size, result.page_count),
                                 (total, size, len(payloads)))

    def test_finite_total_page_size_and_page_cap_matrix_has_exact_boundaries(self):
        for total in range(8):
            for size in (1, 2, 4):
                payloads = _pages(total, size)
                required = len(payloads)
                for max_pages in sorted({max(0, required - 1), required, required + 1}):
                    with self.subTest(total=total, page_size=size, max_pages=max_pages):
                        config = self.config(page_size=size, max_pages=max_pages)
                        if max_pages == 0:
                            with self.assertRaisesRegex(collector.BaselineCollectionError,
                                                         "INVALID_PAGE_CAP"):
                                self.collect(config, payloads)
                            continue
                        if max_pages < required:
                            error, calls = self.collect_failure(config, payloads)
                            self.assertEqual(str(error), "PAGE_CAP_EXCEEDED")
                            self.assertEqual([number for number, _ in calls], [1])
                            continue
                        result, calls, _ = self.collect(config, payloads)
                        self.assertEqual([number for number, _ in calls],
                                         list(range(1, required + 1)))
                        self.assertEqual([finding.source_id for finding in result.findings],
                                         [str(number) for number in range(1, total + 1)])

    def test_page_and_total_byte_caps_use_exact_utf8_wire_boundaries(self):
        payloads = _pages(4, 2)
        bodies = [_body(payload) for payload in payloads]
        for cap in (len(bodies[0]) - 1, len(bodies[0]), len(bodies[0]) + 1):
            with self.subTest(cap_kind="page", cap=cap):
                config = self.config(page_size=2, max_page_bytes=cap,
                                     max_total_bytes=sum(map(len, bodies)))
                if cap < len(bodies[0]):
                    error, calls = self.collect_failure(config, payloads)
                    self.assertEqual(str(error), "HTTP_RESPONSE_FAILED")
                    self.assertEqual([number for number, _ in calls], [1])
                else:
                    result, calls, _ = self.collect(config, payloads)
                    self.assertEqual(len(result.findings), 4)
                    self.assertEqual([number for number, _ in calls], [1, 2])

        total_boundary = len(bodies[0]) + len(bodies[1])
        for cap in (total_boundary - 1, total_boundary, total_boundary + 1):
            with self.subTest(cap_kind="total", cap=cap):
                config = self.config(page_size=2, max_page_bytes=max(map(len, bodies)),
                                     max_total_bytes=cap)
                if cap < total_boundary:
                    error, calls = self.collect_failure(config, payloads)
                    self.assertEqual(str(error), "HTTP_RESPONSE_FAILED")
                    self.assertEqual([number for number, _ in calls], [1, 2])
                else:
                    result, calls, _ = self.collect(config, payloads)
                    self.assertEqual(len(result.findings), 4)
                    self.assertEqual([number for number, _ in calls], [1, 2])
                    self.assertEqual(calls[0][1], min(config.max_page_bytes, cap))
                    self.assertEqual(calls[1][1], min(config.max_page_bytes,
                                                      cap - len(bodies[0])))

    def test_invalid_configuration_has_zero_requests_and_reader_rejects_first_cap(self):
        payloads = _pages(1, 1)
        for config in (self.config(page_size=0), self.config(max_pages=0),
                       self.config(max_page_bytes=0), self.config(max_total_bytes=0)):
            with self.subTest(config=config):
                calls = []
                with patch.object(collector, "_request", side_effect=lambda *args: calls.append(args)):
                    with self.assertRaises(collector.BaselineCollectionError):
                        collector.collect_baseline_pages(config, self.vocab,
                                                         source_sha=UPSTREAM_COMMIT,
                                                         bearer_token=_TOKEN)
                self.assertEqual(calls, [])
        body_len = len(_body(payloads[0]))
        error, calls = self.collect_failure(self.config(max_total_bytes=body_len - 1), payloads)
        self.assertEqual(str(error), "HTTP_RESPONSE_FAILED")
        self.assertEqual([number for number, _ in calls], [1])

    def test_chunked_wire_overhead_is_separate_from_decoded_total_budget(self):
        payloads = _pages(2, 1)
        bodies = [_body(payload) for payload in payloads]
        wires = [format(len(body), "x").encode("ascii") + b"\r\n" + body
                 + b"\r\n0\r\n\r\n" for body in bodies]
        decoded_cap = sum(map(len, bodies))
        page_cap = max(map(len, wires))

        result, calls, _ = self.collect(
            self.config(page_size=1, max_page_bytes=page_cap,
                        max_total_bytes=decoded_cap), payloads)
        self.assertEqual(len(result.findings), 2)
        self.assertEqual([number for number, _ in calls], [1, 2])

        error, calls = self.collect_failure(
            self.config(page_size=1, max_page_bytes=page_cap,
                        max_total_bytes=decoded_cap), payloads, chunked=True)
        self.assertEqual(str(error), "HTTP_RESPONSE_FAILED")
        self.assertEqual([number for number, _ in calls], [1, 2])

        wire_cap = sum(map(len, wires))
        result, calls, _ = self.collect(
            self.config(page_size=1, max_page_bytes=page_cap,
                        max_total_bytes=wire_cap), payloads, chunked=True)
        self.assertEqual(len(result.findings), 2)
        self.assertEqual([number for number, _ in calls], [1, 2])
        self.assertEqual(calls[1][1], min(page_cap, wire_cap - len(bodies[0])))

        one_page = _pages(1, 1)
        one_body = _body(one_page[0])
        one_wire = format(len(one_body), "x").encode("ascii") + b"\r\n" + one_body
        one_wire += b"\r\n0\r\n\r\n"
        for cap in (len(one_wire) - 1, len(one_wire), len(one_wire) + 1):
            with self.subTest(chunked_page_wire_cap=cap):
                config = self.config(page_size=1, max_page_bytes=cap,
                                     max_total_bytes=cap + 1)
                if cap < len(one_wire):
                    error, calls = self.collect_failure(config, one_page, chunked=True)
                    self.assertEqual(str(error), "HTTP_RESPONSE_FAILED")
                    self.assertEqual([number for number, _ in calls], [1])
                else:
                    result, calls, _ = self.collect(config, one_page, chunked=True)
                    self.assertEqual(len(result.findings), 1)
                    self.assertEqual([number for number, _ in calls], [1])

    def test_later_page_shape_failures_at_each_position_stop_without_retry(self):
        for total, size in ((5, 2), (7, 2)):
            payloads = _pages(total, size)
            for failure_page in range(2, len(payloads) + 1):
                malformed = copy.deepcopy(payloads)
                malformed[failure_page - 1]["total"] = total + 99
                with self.subTest(total=total, failure_page=failure_page):
                    error, calls = self.collect_failure(self.config(page_size=size), malformed)
                    self.assertEqual(str(error), "PAGINATION_CHANGED")
                    self.assertEqual([number for number, _ in calls],
                                     list(range(1, failure_page + 1)))

    def test_vocabulary_snapshot_and_payload_inputs_remain_unchanged(self):
        vocab = vocabulary()
        before_vocab = copy.deepcopy(vocab)
        payloads = _pages(3, 2)
        before_payloads = copy.deepcopy(payloads)
        result, calls, _ = self.collect(self.config(page_size=2), payloads,
                                        vocab=vocab, mutate_vocab=True)
        self.assertEqual(vocab["vulnclasses"], {})
        self.assertEqual(vocab["asset_types"], {})
        self.assertEqual(before_vocab, vocabulary())
        self.assertEqual(payloads, before_payloads)
        self.assertEqual([finding.vuln_class for finding in result.findings], ["sqli"] * 3)
        self.assertEqual([number for number, _ in calls], [1, 2])

    def test_mutation_max_pages_bypass_is_caught_with_live_memory_reader(self):
        payloads = _pages(5, 2)
        config = self.config(page_size=2, max_pages=2)
        control_error, control_calls = self.collect_failure(config, payloads)
        self.assertEqual(str(control_error), "PAGE_CAP_EXCEEDED")
        self.assertEqual([number for number, _ in control_calls], [1])
        original = collector.collect_baseline_pages
        calls = []

        def request(_config, _endpoint, _token, number, maximum):
            calls.append((number, maximum))
            return collector._read_response(_MemoryResponse(_body(payloads[number - 1])), maximum)

        with patch.object(collector, "_request", side_effect=request):
            with patch.object(collector, "collect_baseline_pages",
                              side_effect=lambda cfg, vocab, **kwargs: original(
                                  self.config(page_size=2, max_pages=10), vocab, **kwargs)):
                result = collector.collect_baseline_pages(config, self.vocab,
                                                          source_sha=UPSTREAM_COMMIT,
                                                          bearer_token=_TOKEN)
        self.assertEqual([finding.source_id for finding in result.findings],
                         [str(number) for number in range(1, 6)])
        self.assertEqual([number for number, _ in calls], [1, 2, 3])

        def failure_oracle():
            with patch.object(collector, "_request", side_effect=request):
                with patch.object(collector, "collect_baseline_pages",
                                  side_effect=lambda cfg, vocab, **kwargs: original(
                                      self.config(page_size=2, max_pages=10), vocab, **kwargs)):
                    with self.assertRaisesRegex(collector.BaselineCollectionError,
                                                 "PAGE_CAP_EXCEEDED"):
                        collector.collect_baseline_pages(config, self.vocab,
                                                         source_sha=UPSTREAM_COMMIT,
                                                         bearer_token=_TOKEN)
        with self.assertRaises(AssertionError):
            failure_oracle()

    def test_mutation_total_cap_bypass_is_caught_after_control(self):
        payloads = _pages(5, 2)
        bodies = [_body(payload) for payload in payloads]
        cap = len(bodies[0]) + len(bodies[1]) - 1
        config = self.config(page_size=2, max_total_bytes=cap)
        control_error, control_calls = self.collect_failure(config, payloads)
        self.assertEqual(str(control_error), "HTTP_RESPONSE_FAILED")
        self.assertEqual([number for number, _ in control_calls], [1, 2])
        original = collector.collect_baseline_pages
        calls = []

        def request(_config, _endpoint, _token, number, maximum):
            calls.append((number, maximum))
            return collector._read_response(_MemoryResponse(bodies[number - 1]), maximum)

        with patch.object(collector, "_request", side_effect=request):
            with patch.object(collector, "collect_baseline_pages",
                              side_effect=lambda cfg, vocab, **kwargs: original(
                                  self.config(page_size=2, max_total_bytes=sum(map(len, bodies))),
                                  vocab, **kwargs)):
                result = collector.collect_baseline_pages(config, self.vocab,
                                                          source_sha=UPSTREAM_COMMIT,
                                                          bearer_token=_TOKEN)
        self.assertEqual([finding.source_id for finding in result.findings],
                         [str(number) for number in range(1, 6)])
        self.assertEqual([number for number, _ in calls], [1, 2, 3])
        self.assertEqual(control_calls[1][1], cap - len(bodies[0]))
        self.assertEqual(calls[1][1], sum(map(len, bodies)) - len(bodies[0]))

        def failure_oracle():
            with patch.object(collector, "_request", side_effect=request):
                with patch.object(collector, "collect_baseline_pages",
                                  side_effect=lambda cfg, vocab, **kwargs: original(
                                      self.config(page_size=2, max_total_bytes=sum(map(len, bodies))),
                                      vocab, **kwargs)):
                    with self.assertRaisesRegex(collector.BaselineCollectionError,
                                                 "HTTP_RESPONSE_FAILED"):
                        collector.collect_baseline_pages(config, self.vocab,
                                                         source_sha=UPSTREAM_COMMIT,
                                                         bearer_token=_TOKEN)
        with self.assertRaises(AssertionError):
            failure_oracle()


if __name__ == "__main__":
    unittest.main()
