"""페이지 수집: 소유 loopback 합성 서버·합성 토큰/DTO만 사용한다."""

from __future__ import annotations

import copy
import io
import json
import os
import socket
import subprocess
import sys
import threading
import time
import traceback
import unittest
from contextlib import redirect_stderr, redirect_stdout
from dataclasses import asdict, replace
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import baseline_pages_collector as collector
from baseline_extract import MAX_INPUT_BYTES, UPSTREAM_COMMIT
from test_baseline_pages import page, pages, row
from test_baseline_extract import vocabulary

TOKEN = "synthetic.header.signature"
MARKER = "SYNTHETIC-PRIVATE-DO-NOT-PRINT"
LIMIT = 64 * 1024


class TestBaselinePagesCollector(unittest.TestCase):
    def setUp(self):
        self.origin, self.state = self.server()
        self.config = collector.BaselinePagesEndpoint(self.origin, 2, 1.0, LIMIT, LIMIT, 10)
        self.vocabulary = vocabulary()

    def server(self):
        state = {"requests": [], "payloads": pages(), "status": 200, "headers": {}, "delay": 0,
                 "chunked": False, "no_length": False, "length": None, "trailing": b"",
                 "wire": None, "raw": None, "on_request": None}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_GET(self):
                # 토큰/응답 원문은 수신 요약에도 기록하지 않는다.
                state["requests"].append(dict(method=self.command, path=self.path,
                    auth_matches=self.headers.get("Authorization") == "Bearer " + TOKEN,
                    cookie="Cookie" in self.headers, host=self.headers.get("Host"),
                    encoding=self.headers.get("Accept-Encoding")))
                index = len(state["requests"]) - 1
                if state["on_request"] is not None:
                    state["on_request"](index)
                payload = state["payloads"][min(index, len(state["payloads"]) - 1)]
                body = json.dumps(payload, separators=(",", ":")).encode() if state["raw"] is None else state["raw"]
                self.send_response(state["status"])
                for name, value in ({"Content-Type": "application/json"} | state["headers"]).items():
                    if value is not None:
                        self.send_header(name, value)
                if state["chunked"]:
                    self.send_header("Transfer-Encoding", "chunked")
                elif not state["no_length"]:
                    self.send_header("Content-Length", str(len(body) if state["length"] is None else state["length"]))
                self.end_headers()
                if state["delay"]:
                    time.sleep(state["delay"])
                if state["chunked"]:
                    body = f"{len(body):x}\r\n".encode() + body + b"\r\n0\r\n\r\n"
                wire = body if state["wire"] is None else state["wire"]
                try:
                    self.wfile.write(wire + state["trailing"])
                except (BrokenPipeError, ConnectionResetError):
                    pass  # 상한/타임아웃 때문에 먼저 닫힌 합성 클라이언트.

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server.daemon_threads = True
        thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=0.01), daemon=True)
        thread.start()

        def stop():
            server.shutdown()
            server.server_close()
            thread.join(timeout=1)

        self.addCleanup(stop)
        return f"http://127.0.0.1:{server.server_port}", state

    def collect(self, config=None, **kwargs):
        return collector.collect_baseline_pages(self.config if config is None else config,
            kwargs.pop("vocabulary", self.vocabulary), source_sha=kwargs.pop("source_sha", UPSTREAM_COMMIT),
            bearer_token=kwargs.pop("bearer_token", TOKEN), **kwargs)

    def failure(self, config=None, count=None, code=None, **kwargs):
        with self.assertRaises(collector.BaselineCollectionError) as caught:
            self.collect(config, **kwargs)
        if count is not None:
            self.assertEqual(len(self.state["requests"]), count)
        text = "".join(traceback.format_exception(caught.exception))
        self.assertNotIn(TOKEN, text)
        self.assertNotIn(MARKER, text)
        if code:
            self.assertEqual(str(caught.exception), code)
        return caught.exception

    def test_collects_only_global_page_queries_and_returns_merged_neutral_result(self):
        result = self.collect()
        self.assertEqual([r["path"] for r in self.state["requests"]], [
            "/api/exploration/findings?page=1&limit=2", "/api/exploration/findings?page=2&limit=2"])
        self.assertTrue(all(r["method"] == "GET" and r["auth_matches"] and not r["cookie"]
                            and r["encoding"] == "identity" for r in self.state["requests"]))
        self.assertEqual((result.declared_total, result.page_count, result.page_size), (3, 2, 2))
        self.assertEqual([r.source_id for r in result.findings], ["1", "2", "3"])
        output = json.dumps(asdict(result)) + repr(result) + repr(self.config)
        self.assertNotIn(TOKEN, output)
        self.assertNotIn("SYNTHETIC-PRIVATE", output)
        self.assertNotIn("oracle_value", output)

    def test_zero_total_still_fetches_one_first_page(self):
        self.state["payloads"] = [page(1, [], total=0)]
        result = self.collect()
        self.assertEqual((result.declared_total, result.page_count, result.findings), (0, 1, ()))
        self.assertEqual(len(self.state["requests"]), 1)

    def test_exact_multiple_stops_without_an_extra_page(self):
        self.state["payloads"] = [page(1, [row(1), row(2)], total=2)]
        self.assertEqual(len(self.collect().findings), 2)
        self.assertEqual(len(self.state["requests"]), 1)

    def test_maximum_supported_limit_is_sent_without_server_defaulting(self):
        self.state["payloads"] = [page(1, [row(1)], total=1, size=200)]
        self.assertEqual(len(self.collect(replace(self.config, page_size=200)).findings), 1)
        self.assertTrue(self.state["requests"][0]["path"].endswith("page=1&limit=200"))

    def test_invalid_origin_is_rejected_before_any_connection(self):
        for origin in ("http://example.invalid", "http://192.0.2.1", "https://127.0.0.1", "http://127.0.0.1:0",
                       self.origin + "/api", self.origin + "?task=1", self.origin + "?token=" + TOKEN,
                       self.origin + "#fragment", self.origin.replace("//", "//user:password@"),
                       "http://127.0.0.1%25zone", self.origin + "/\r\n" + MARKER):
            with self.subTest(origin_kind=origin.split(":")[0]), \
                    patch.object(collector.http.client, "HTTPConnection") as connection:
                self.failure(replace(self.config, origin=origin), count=0)
                connection.assert_not_called()

    def test_bad_numeric_configuration_and_tokens_send_zero_requests(self):
        values = (("page_size", (0, 201, True, "2", 2.5)), ("max_pages", (0, -1, True, "2")),
                  ("max_page_bytes", (0, True, MAX_INPUT_BYTES + 1)),
                  ("max_total_bytes", (0, -1, True, MAX_INPUT_BYTES + 1)),
                  ("timeout_seconds", (0, True, "1", float("inf"), float("nan"))))
        for field, bad in values:
            for value in bad:
                with self.subTest(field=field):
                    self.failure(replace(self.config, **{field: value}), count=0)
        for token in ("", None, 123, "Bearer " + TOKEN, TOKEN + "\r\nCookie: injected", "secret word", "한글",
                      "x" * (collector.MAX_BEARER_LENGTH + 1)):
            self.failure(bearer_token=token, count=0, code="INVALID_BEARER")

    def test_source_and_vocabulary_are_checked_before_authentication_is_sent(self):
        self.failure(source_sha="0" * 40, count=0, code="UNSUPPORTED_SOURCE")
        for vocab in ({}, {"vulnclasses": {1: "sqli"}, "asset_types": {}},
                      {"vulnclasses": {MARKER: MARKER}, "asset_types": {}}, {"vulnclasses": {}, "asset_types": {}, MARKER: TOKEN}):
            self.failure(vocabulary=vocab, count=0)
        huge = {str(n) + "x" * 120: "sqli" for n in range(600)}
        self.failure(vocabulary={"vulnclasses": huge, "asset_types": {}}, count=0, code="VOCABULARY_TOO_LARGE")

    def test_task_filter_and_arbitrary_headers_are_not_public_arguments(self):
        for kwargs in ({"task": "1"}, {"query": "token=" + TOKEN}, {"headers": {"Cookie": TOKEN}}):
            with self.assertRaises(TypeError):
                self.collect(**kwargs)
        self.assertEqual(self.state["requests"], [])

    def test_page_cap_is_enforced_before_second_get_even_for_huge_total(self):
        self.failure(replace(self.config, max_pages=1), count=1, code="PAGE_CAP_EXCEEDED")
        self.state["requests"].clear()
        self.state["payloads"][0]["total"] = 10 ** 300
        self.failure(count=1, code="PAGE_CAP_EXCEEDED")

    def test_page_and_limit_echo_mismatch_stops_collection_immediately(self):
        for change in ({"page": 99, "items": []}, {"page_size": 3}):
            self.state["requests"].clear()
            self.state["payloads"] = pages()
            self.state["payloads"][0].update(change)
            self.failure(count=1, code="PAGE_ECHO_MISMATCH")

    def test_total_drift_and_short_page_do_not_return_partial_results(self):
        self.state["payloads"][1]["total"] = 4
        self.failure(count=2, code="PAGINATION_CHANGED")
        self.state["requests"].clear()
        self.state["payloads"] = pages()
        self.state["payloads"][0]["items"] = [row(1)]
        self.failure(count=1, code="PAGE_LENGTH_MISMATCH")

    def test_merger_rejects_source_and_binding_collisions_after_collection(self):
        self.state["payloads"][1]["items"][0]["id"] = "1"
        self.failure(count=2, code="INVALID_PAGE_SET")
        self.state["requests"].clear()
        self.state["payloads"] = [page(1, [row(1, standalone=101, binding=201)], total=2, size=1),
                                  page(2, [row(2, standalone=101, binding=201)], total=2, size=1)]
        self.failure(replace(self.config, page_size=1), count=2, code="INVALID_PAGE_SET")

    def test_shared_finding_and_asset_references_remain_allowed(self):
        self.state["payloads"] = [page(1, [row(1, standalone=101, binding=201)], total=2, size=1),
                                  page(2, [row(2, standalone=101, binding=202)], total=2, size=1)]
        result = self.collect(replace(self.config, page_size=1))
        self.assertEqual([r.finding_id for r in result.findings], ["101", "101"])
        self.assertEqual([r.asset_refs[0].id for r in result.findings], ["77", "77"])

    def test_vocabulary_snapshot_is_fixed_before_the_first_request(self):
        original = copy.deepcopy(self.vocabulary)

        def mutate(index):
            if index == 0:
                self.vocabulary["vulnclasses"]["SQL Injection"] = "changed"

        self.state["on_request"] = mutate
        self.assertTrue(all(r.vuln_class == "sqli" for r in self.collect().findings))
        self.assertEqual(original["vulnclasses"]["SQL Injection"], "sqli")
        self.assertEqual(self.vocabulary["vulnclasses"]["SQL Injection"], "changed")

    def test_success_does_not_mutate_the_supplied_vocabulary(self):
        original = copy.deepcopy(self.vocabulary)
        self.collect()
        self.assertEqual(self.vocabulary, original)

    def test_proxy_environment_and_cookie_headers_are_never_forwarded(self):
        proxy, proxy_state = self.server()
        self.state["headers"] = {"Set-Cookie": MARKER + "=" + TOKEN}
        with patch.dict(os.environ, {"HTTP_PROXY": proxy, "http_proxy": proxy, "HTTPS_PROXY": proxy,
                                    "ALL_PROXY": proxy, "NO_PROXY": "", "no_proxy": "", "BEARER_TOKEN": MARKER}):
            self.collect()
        self.assertEqual(proxy_state["requests"], [])
        self.assertTrue(all(r["auth_matches"] and not r["cookie"] for r in self.state["requests"]))

    def test_redirect_destination_receives_no_bearer_or_requests(self):
        destination, state = self.server()
        self.state.update(status=302, headers={"Location": destination + "/" + MARKER})
        self.failure(count=1, code="HTTP_RESPONSE_FAILED")
        self.assertEqual(state["requests"], [])

    def test_auth_errors_server_errors_and_not_found_are_never_retried(self):
        for status in (401, 403, 404, 429, 500):
            self.state["status"] = status
            self.state["requests"].clear()
            self.failure(count=1, code="HTTP_RESPONSE_FAILED")

    def test_localhost_connects_only_to_pinned_numeric_loopback(self):
        real_lookup = socket.getaddrinfo

        def lookup(host, *args, **kwargs):
            self.assertEqual(host, "127.0.0.1")
            return real_lookup(host, *args, **kwargs)

        config = replace(self.config, origin=self.origin.replace("127.0.0.1", "localhost"))
        with patch.object(socket, "getaddrinfo", side_effect=lookup):
            self.collect(config)
        self.assertTrue(all(r["host"].startswith("localhost:") for r in self.state["requests"]))

    def test_non_json_compressed_and_invalid_framing_responses_are_rejected(self):
        for headers in ({"Content-Type": "text/html"}, {"Content-Encoding": "gzip"},
                        {"Transfer-Encoding": "gzip"}, {"Content-Length": "duplicate"}):
            self.state["requests"].clear()
            self.state["headers"] = headers
            self.failure(count=1, code="HTTP_RESPONSE_FAILED")

    def test_short_long_and_trailing_content_length_bodies_are_rejected(self):
        for length, trailing in ((1, b""), (LIMIT, b""), (None, b"extra")):
            self.state.update(length=length, trailing=trailing, requests=[])
            self.failure(count=1, code="HTTP_RESPONSE_FAILED")

    def test_chunked_and_connection_close_responses_use_existing_reader(self):
        self.state["chunked"] = True
        self.assertEqual(len(self.collect().findings), 3)
        self.state.update(requests=[], chunked=False, no_length=True)
        self.assertEqual(len(self.collect().findings), 3)

    def test_invalid_chunk_framing_trailers_and_trailing_bytes_are_rejected(self):
        for wire in (b"Z\r\nabc\r\n0\r\n\r\n", b"4\r\nabc\r\n0\r\n\r\n", b"0\r\nX: value\r\n\r\n",
                     b"0\r\n\r\ntrailing"):
            self.state.update(requests=[], chunked=True, wire=wire)
            self.failure(count=1, code="HTTP_RESPONSE_FAILED")

    def test_wire_and_total_body_caps_are_applied_before_further_pages(self):
        self.failure(replace(self.config, max_page_bytes=32), count=1, code="HTTP_RESPONSE_FAILED")
        self.state["requests"].clear()
        sizes = [len(json.dumps(p, separators=(",", ":")).encode()) for p in self.state["payloads"]]
        self.failure(replace(self.config, max_total_bytes=sum(sizes) - 1), count=2, code="HTTP_RESPONSE_FAILED")
        self.state["requests"].clear()
        result = self.collect(replace(self.config, max_total_bytes=sum(sizes)))
        self.assertEqual(len(result.findings), 3)

    def test_no_remaining_budget_prevents_the_next_connection(self):
        first_size = len(json.dumps(self.state["payloads"][0], separators=(",", ":")).encode())
        self.failure(replace(self.config, max_total_bytes=first_size), count=1, code="TOTAL_BYTE_CAP_EXCEEDED")

    def test_chunk_overhead_may_conservatively_exhaust_a_decoded_body_budget(self):
        self.state["chunked"] = True
        sizes = [len(json.dumps(p, separators=(",", ":")).encode()) for p in self.state["payloads"]]
        self.failure(replace(self.config, max_total_bytes=sum(sizes)), count=2, code="HTTP_RESPONSE_FAILED")

    def test_socket_inactivity_timeout_is_not_retried(self):
        self.state["delay"] = 0.15
        self.failure(replace(self.config, timeout_seconds=0.02), count=1, code="HTTP_RESPONSE_FAILED")

    def test_strict_json_and_page_shape_errors_stop_after_first_response(self):
        values = (b"{", b"\xff", b"[NaN]", b"[1e999]", b'{"items":[],"items":[],"total":0,"page":1,"page_size":2}')
        for raw in values:
            self.state.update(requests=[], raw=raw)
            self.failure(count=1, code="INVALID_PAGE_JSON_OR_DTO")
        for payload in ([], row(1), {"data": []}):
            self.state.update(requests=[], raw=json.dumps(payload).encode())
            self.failure(count=1, code="EXPECTED_PAGE_OBJECT")

    def test_sensitive_reflections_and_unknown_labels_do_not_escape_result(self):
        for payload in self.state["payloads"]:
            for finding in payload["items"]:
                finding.update(vulnclass=TOKEN, severity=TOKEN, summary=TOKEN, evidence=MARKER)
                finding["traffic_bindings"][0]["snapshot"] = {"req_head": TOKEN, "resp_body": MARKER}
        result = self.collect()
        self.assertTrue(all(r.vuln_class is None and r.severity is None for r in result.findings))
        self.assertNotIn(TOKEN, repr(result))
        self.assertNotIn(MARKER, json.dumps(asdict(result)))

    def test_http_debugging_does_not_print_authorization_or_responses(self):
        out, err = io.StringIO(), io.StringIO()
        with patch.object(collector.http.client.HTTPConnection, "debuglevel", 1), \
                redirect_stdout(out), redirect_stderr(err):
            self.collect()
        self.assertEqual((out.getvalue(), err.getvalue()), ("", ""))

    def test_underlying_errors_are_replaced_and_no_files_or_processes_are_written(self):
        with patch.object(collector, "_request", side_effect=OSError(TOKEN + MARKER)):
            self.failure(count=0, code="HTTP_RESPONSE_FAILED")
        with patch.object(Path, "write_bytes", side_effect=AssertionError("no writes")), \
                patch.object(Path, "write_text", side_effect=AssertionError("no writes")), \
                patch.object(subprocess, "Popen", side_effect=AssertionError("no subprocess")):
            self.collect()


if __name__ == "__main__":
    unittest.main()
