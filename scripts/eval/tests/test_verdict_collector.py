"""판정 export 수집(판정 페이지 제안 VRP01·02·04·05·07·10). 합성 loopback 서버만 쓰며 실제 엔진·DB·판정 없음."""

from __future__ import annotations

import http.client
import json
import sys
import threading
import traceback
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))

import verdict_collector  # noqa: E402
from runner_engine import AdminSecret, EngineAPIError  # noqa: E402
from test_verdict_pages import check, page, record  # noqa: E402
from verdict_collector import (  # noqa: E402
    VerdictIntegrityError,
    CollectLimits, VerdictCollectError, VerdictExportNotReady, collect_verdict_export,
)

PASSWORD = "synthetic-admin-password-3f1a"
TOKEN = "synthetic-token-9b2c"
LIMITS = CollectLimits(page_size=2, timeout_seconds=5, max_page_bytes=64 * 1024, max_total_bytes=256 * 1024, max_pages=5)
RUN = {"task_id": 17, "run_id": "run-1", "model_tag": "model-1", "reset_generation": "reset-1"}


class FakeServer:
    """로그인·동결·페이지만 하는 합성 엔진. pages는 cursor → (status, 본문)이다."""

    def __init__(self, pages, freeze=(200, b'{"export_ref": "export-1"}'), login_status=200):
        self.pages, self.freeze, self.login_status = pages, freeze, login_status
        self.requests = []
        server = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def reply(self, status, body):
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                server.requests.append(("POST", self.path, dict(self.headers), body))
                if self.path == "/api/auth/login":
                    self.reply(server.login_status, json.dumps({"token": TOKEN}).encode())
                else:
                    self.reply(*server.freeze)

            def do_GET(self):
                server.requests.append(("GET", self.path, dict(self.headers), b""))
                cursor = parse_qs(urlsplit(self.path).query).get("cursor", [None])[0]
                self.reply(*server.pages[cursor])

        self.http = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.http.server_address[1]
        threading.Thread(target=self.http.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()

    def close(self):
        self.http.shutdown()
        self.http.server_close()


def body(value):
    return json.dumps(value, ensure_ascii=False).encode("utf-8")


def two_pages():
    return {None: (200, body(page([record(101), record(102)], next_cursor="c1", total=3))),
            "c1": (200, body(page([record(103, "allowed")], total=3)))}


class TestCollect(unittest.TestCase):
    def server(self, pages=None, **kwargs):
        fake = FakeServer(two_pages() if pages is None else pages, **kwargs)
        self.addCleanup(fake.close)
        return fake

    def collect(self, fake, limits=LIMITS):
        return collect_verdict_export(fake.port, AdminSecret(PASSWORD), limits=limits, **RUN)

    def failed(self, fake, code, limits=LIMITS):
        with self.assertRaises(VerdictCollectError) as caught:
            self.collect(fake, limits)
        self.assertEqual(str(caught.exception), code)
        self.assertNotIn(PASSWORD, str(caught.exception))
        self.assertNotIn(TOKEN, str(caught.exception))
        self.assertIsNone(caught.exception.__cause__)

    def test_freezes_then_reads_every_page_with_the_same_export(self):
        fake = self.server()
        result = self.collect(fake)
        self.assertEqual((result.export_ref, result.export.total, len(result.pages)), ("export-1", 3, 2))
        self.assertEqual([(m, urlsplit(p).path) for m, p, _, _ in fake.requests],
                         [("POST", "/api/auth/login"), ("POST", "/api/tasks/17/verdict-exports"),
                          ("GET", "/api/tasks/17/verdict-records"), ("GET", "/api/tasks/17/verdict-records")])
        self.assertEqual(json.loads(fake.requests[1][3]), {"run_id": "run-1", "purpose": "initial"})
        first, second = (parse_qs(urlsplit(p).query) for _, p, _, _ in fake.requests[2:])
        expected = {"run_id": ["run-1"], "purpose": ["initial"], "export_ref": ["export-1"], "limit": ["2"]}
        self.assertEqual(first, expected)
        self.assertEqual(second, expected | {"cursor": ["c1"]})
        for _, _, headers, _ in fake.requests[1:]:
            self.assertEqual(headers["Authorization"], f"Bearer {TOKEN}")

    def test_raw_pages_are_kept_as_received(self):
        fake = self.server({None: (200, body(page([record(101)], [check(101, "undecidable", ["kg_snapshot_unavailable"])])))})
        result = self.collect(fake)
        self.assertEqual(result.pages[0]["records"][0]["oracle_value"], "confirmed")
        self.assertEqual(result.export.envelope["records"][0]["oracle_value"], "undecidable")
        self.assertEqual(result.export.converted, 1)

    def test_zero_records_is_a_complete_result(self):
        result = self.collect(self.server({None: (200, body(page()))}))
        self.assertEqual((result.export.total, result.export.envelope["records"]), (0, []))

    def test_not_ready_is_its_own_outcome_and_reads_no_pages(self):
        fake = self.server(freeze=(409, b'{"code": "export_not_ready"}'))
        with self.assertRaises(VerdictExportNotReady):
            self.collect(fake)
        self.assertFalse([request for request in fake.requests if request[0] == "GET"])

    def test_freeze_failures_have_fixed_codes(self):
        for freeze, code in (((500, b"{}"), "FREEZE_HTTP_500"), ((404, b"{}"), "FREEZE_EXPORT_NOT_FOUND"),
                             ((503, b"{}"), "FREEZE_EXPORT_UNAVAILABLE"), ((422, b"{}"), "FREEZE_INTEGRITY_FAILED"),
                             ((200, b"not json"), "RESPONSE_INVALID"), ((200, b"[]"), "RESPONSE_INVALID"),
                             ((200, b"{}"), "EXPORT_REF_INVALID"), ((200, b'{"export_ref": ""}'), "EXPORT_REF_INVALID"),
                             ((200, b'{"export_ref": "a b"}'), "EXPORT_REF_INVALID"), ((200, b'{"export_ref": 7}'), "EXPORT_REF_INVALID"),
                             ((200, b'{"export_ref": "a", "export_ref": "b"}'), "DUPLICATE_KEY")):
            with self.subTest(code=code, freeze=freeze):
                self.failed(self.server(freeze=freeze), f"verdict_collect: {code}")

    def test_page_failures_discard_everything(self):
        first = (200, body(page([record(101)], next_cursor="c1", total=2)))
        for reply, code in (((409, b"{}"), "PAGE_EXPORT_SNAPSHOT_UNAVAILABLE"), ((422, b"{}"), "PAGE_INTEGRITY_FAILED"),
                            ((503, b"{}"), "PAGE_EXPORT_UNAVAILABLE"), ((401, b"{}"), "PAGE_UNAUTHORIZED"),
                            ((400, b"{}"), "PAGE_INVALID_EXPORT_REQUEST"), ((404, b"{}"), "PAGE_EXPORT_NOT_FOUND"),
                            ((500, b"{}"), "PAGE_HTTP_500"), ((200, b"[]"), "RESPONSE_INVALID"),
                            ((200, b'{"next_cursor": null, "next_cursor": null}'), "DUPLICATE_KEY"),
                            ((200, b'{"total": NaN}'), "RESPONSE_INVALID")):
            with self.subTest(code=code):
                self.failed(self.server({None: first, "c1": reply}), f"verdict_collect: {code}")

    def test_integrity_failures_are_told_apart_from_uncollected(self):
        """무결성 실패는 채점 중단이고, 진짜 미수집만 판단불가로 남는다(제안 §3·§6)."""
        good = (200, body(page([record(101)], next_cursor="c1", total=2)))
        integrity = (({"freeze": (422, b"{}")}, None), ({"freeze": (200, b"not json")}, None), ({"freeze": (200, b"{}")}, None),
                     ({"freeze": (200, b'{"export_ref": "export-1", "export_ref": "x"}')}, None),
                     ({}, {None: good, "c1": (422, b"{}")}), ({}, {None: good, "c1": (200, b"[]")}),
                     ({}, {None: (200, body(page([record(101)], next_cursor=7, total=2)))}),
                     ({}, {None: good, "c1": (200, body(page([record(102)], next_cursor="c1", total=2)))}),
                     ({}, {None: (200, body(page([record(101)], total=9)))}),
                     ({}, {None: (200, body(page([record(101)], run_id="other")))}),
                     ({}, {None: (200, body(page([record(101, oracle_value="other")])))}))
        for kwargs, pages in integrity:
            with self.subTest(kwargs=kwargs, pages=None if pages is None else list(pages)):
                with self.assertRaises(VerdictIntegrityError) as caught:
                    self.collect(self.server(pages, **kwargs))
                self.assertIsNone(caught.exception.__cause__)
                self.assertTrue(caught.exception.__suppress_context__)
        uncollected = (({"freeze": (404, b"{}")}, None), ({"freeze": (503, b"{}")}, None), ({"freeze": (401, b"{}")}, None),
                       ({"freeze": (500, b"{}")}, None), ({}, {None: good, "c1": (503, b"{}")}), ({}, {None: good, "c1": (409, b"{}")}),
                       ({}, {None: good, "c1": (404, b"{}")}))
        for kwargs, pages in uncollected:
            with self.subTest(kwargs=kwargs, pages=None if pages is None else list(pages)):
                with self.assertRaises(VerdictCollectError) as caught:
                    self.collect(self.server(pages, **kwargs))
                self.assertNotIsInstance(caught.exception, VerdictIntegrityError)
        with self.assertRaises(VerdictExportNotReady) as caught:
            self.collect(self.server(freeze=(409, b"{}")))
        self.assertNotIsInstance(caught.exception, VerdictIntegrityError)

    def test_limits_stop_collection(self):
        pages = {None: (200, body(page([record(101)], next_cursor="c1", total=3))),
                 "c1": (200, body(page([record(102)], next_cursor="c2", total=3))),
                 "c2": (200, body(page([record(103)], total=3)))}
        self.assertEqual(self.collect(self.server(pages)).export.total, 3)
        fake = self.server(pages)
        self.failed(fake, "verdict_collect: TOO_MANY_PAGES", CollectLimits(2, 5, 64 * 1024, 256 * 1024, 2))
        self.assertEqual(len([r for r in fake.requests if r[0] == "GET"]), 2)
        self.failed(self.server(pages), "verdict_collect: PAGE_TOO_LARGE", CollectLimits(2, 5, 64, 256 * 1024, 5))
        self.failed(self.server(pages), "verdict_collect: TOTAL_TOO_LARGE", CollectLimits(2, 5, 64 * 1024, 1024, 5))

    def test_cursor_cycle_stops_before_the_page_limit(self):
        fake = self.server({None: (200, body(page([record(101)], next_cursor="c1", total=3))),
                            "c1": (200, body(page([record(102)], next_cursor="c1", total=3)))})
        self.failed(fake, "verdict_collect: CURSOR_CYCLE")
        self.assertEqual(len([r for r in fake.requests if r[0] == "GET"]), 2)

    def test_bad_cursor_type_fails(self):
        for value in (7, "", True):
            with self.subTest(value=value):
                self.failed(self.server({None: (200, body(page([record(101)]) | {"next_cursor": value}))}),
                            "verdict_collect: CURSOR_INVALID")

    def test_page_checks_reach_the_caller_as_codes_without_values(self):
        leaked = "synthetic-record-value-7f3a"
        self.failed(self.server({None: (200, body(page([record(101, token=leaked)])))}), "verdict_pages: RECORD_INVALID")
        self.failed(self.server({None: (200, body(page([record(101)]) | {"run_id": leaked}))}), "verdict_pages: RUN_ID_MISMATCH")
        self.failed(self.server({None: (200, body(page([record(101)], total=2)))}), "verdict_pages: TOTAL_MISMATCH")

    def test_transport_errors_do_not_show_raw_text_in_the_traceback(self):
        marker = "synthetic-status-line-5e1b"
        def transport_error(*_args, **_kwargs):
            raise http.client.BadStatusLine(marker)
        freeze_ok = (200, b'{"export_ref": "export-1"}')
        for target, side_effect, code in (("_post", transport_error, "FREEZE_UNREACHABLE"), ("_get", transport_error, "PAGE_UNREACHABLE")):
            with self.subTest(code=code):
                fake = self.server(freeze=freeze_ok)
                with patch.object(verdict_collector, target, side_effect=side_effect):
                    with self.assertRaises(VerdictCollectError) as caught:
                        collect_verdict_export(fake.port, AdminSecret(PASSWORD), limits=LIMITS, **RUN)
                self.assertEqual(str(caught.exception), f"verdict_collect: {code}")
                shown = "".join(traceback.format_exception(caught.exception))
                self.assertNotIn(marker, shown)

    def test_page_errors_do_not_show_record_values_in_the_traceback(self):
        leaked = "synthetic-record-value-7f3a"
        fake = self.server({None: (200, body(page([record(101, oracle_value=leaked)])))})
        with self.assertRaises(VerdictCollectError) as caught:
            self.collect(fake)
        self.assertNotIn(leaked, "".join(traceback.format_exception(caught.exception)))

    def test_login_failure_is_not_a_verdict_result(self):
        fake = self.server(login_status=500)
        with self.assertRaises(EngineAPIError):
            self.collect(fake)
        self.assertEqual(len(fake.requests), 1)

    def test_unreachable_server_has_fixed_codes(self):
        fake = FakeServer(two_pages())
        port = fake.port
        fake.close()
        with self.assertRaises(EngineAPIError):
            collect_verdict_export(port, AdminSecret(PASSWORD), limits=CollectLimits(2, 1, 1024, 2048, 2), **RUN)


if __name__ == "__main__":
    unittest.main()
