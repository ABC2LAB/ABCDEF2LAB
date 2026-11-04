"""자신의 loopback 합성 서버만 이용해 평가 계측 수신 건수와 저장 경계를 검증한다."""

from __future__ import annotations

import json
import http.client
import os
import sys
import tempfile
import threading
import time
import unittest
from dataclasses import replace
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from ground_truth import bac_category, core_gt
from snapshot_collector import SnapshotCollectionError, SnapshotEndpoint, collect_snapshot
from snapshot_writer import SnapshotContext

LIMIT = 1024 * 1024
KEYS = sorted({row.key for row in core_gt() + bac_category()})


class TestSnapshotCollector(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.raw = json.dumps({"data": [{"key": key, "solved": False} for key in KEYS]}, indent=2).encode()
        self.context = SnapshotContext("test-run", "test-model", "test-reset", "test-instance", "test-execution")
        self.origin, self.state = self.server()
        self.config = SnapshotEndpoint(self.origin, "/instrumentation/state", 1.0, LIMIT)

    def server(self):
        state = {"requests": [], "status": 200, "body": self.raw, "headers": {}, "delay": 0,
                 "no_length": False, "chunked": False, "declared_length": None, "trailing": b"", "wire_body": None}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_GET(self):
                state["requests"].append((self.command, self.path, dict(self.headers)))
                self.send_response(state["status"])
                headers = {"Content-Type": "application/json"} | state["headers"]
                for name, value in headers.items():
                    if value is not None:
                        self.send_header(name, value)
                if state["chunked"]:
                    self.send_header("Transfer-Encoding", "chunked")
                elif not state["no_length"]:
                    length = len(state["body"]) if state["declared_length"] is None else state["declared_length"]
                    self.send_header("Content-Length", str(length))
                self.end_headers()
                if state["delay"]:
                    time.sleep(state["delay"])
                body = state["body"]
                if state["chunked"]:
                    body = f"{len(body):x}\r\n".encode() + body + b"\r\n0\r\n\r\n"
                body = body if state["wire_body"] is None else state["wire_body"]
                body += state["trailing"]
                try:
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    # 상한·타임아웃으로 먼저 닫은 합성 클라이언트의 정상 시험 경로.
                    pass

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

    def collect(self, config=None, phase="pre", context=None, root=None):
        return collect_snapshot(config or self.config, self.root if root is None else root,
                                phase, self.context if context is None else context)

    def assert_failure(self, config=None):
        with self.assertRaises(SnapshotCollectionError):
            self.collect(config=config)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_one_registered_get_persists_exact_body_and_normalized_snapshot(self):
        result = self.collect()
        self.assertEqual(len(self.state["requests"]), 1)
        method, path, headers = self.state["requests"][0]
        self.assertEqual((method, path), ("GET", self.config.path))
        self.assertEqual(headers["Accept-Encoding"], "identity")
        self.assertNotIn("Authorization", headers)
        self.assertNotIn("Cookie", headers)
        self.assertEqual((self.root / result.artifacts.raw["path"]).read_bytes(), self.raw)
        self.assertEqual(result.artifacts.challenge_count, len(KEYS))
        self.assertLessEqual(datetime.fromisoformat(result.requested_at), datetime.fromisoformat(result.received_at))

    def test_localhost_is_pinned_to_loopback_and_host_header_preserved(self):
        config = replace(self.config, origin=self.origin.replace("127.0.0.1", "localhost"))
        with patch("snapshot_collector.http.client.HTTPConnection", wraps=http.client.HTTPConnection) as connection:
            self.collect(config)
        self.assertEqual(connection.call_args.args[0], "127.0.0.1")
        self.assertEqual(self.state["requests"][0][2]["Host"], config.origin.split("//", 1)[1])

    def test_proxy_environment_receives_zero_requests(self):
        proxy_origin, proxy_state = self.server()
        with patch.dict(os.environ, {"HTTP_PROXY": proxy_origin, "http_proxy": proxy_origin,
                                    "HTTPS_PROXY": proxy_origin, "ALL_PROXY": proxy_origin,
                                    "NO_PROXY": "", "no_proxy": ""}):
            self.collect()
        self.assertEqual(len(self.state["requests"]), 1)
        self.assertEqual(proxy_state["requests"], [])

    def test_redirect_destinations_receive_zero_requests(self):
        destination, state = self.server()
        self.state.update(status=302, headers={"Location": destination + "/SYNTHETIC-PRIVATE"})
        with self.assertRaises(SnapshotCollectionError) as caught:
            self.collect()
        self.assertEqual(len(self.state["requests"]), 1)
        self.assertEqual(state["requests"], [])
        self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))
        self.assertEqual(list(self.root.iterdir()), [])

    def test_response_cookie_is_not_forwarded_or_written(self):
        self.state["headers"] = {"Set-Cookie": "session=SYNTHETIC; Path=/"}
        self.collect()
        self.collect(phase="post")
        self.assertEqual(len(self.state["requests"]), 2)
        for _, _, headers in self.state["requests"]:
            self.assertNotIn("Cookie", headers)
            self.assertNotIn("Authorization", headers)
        for file in self.root.rglob("*.json"):
            self.assertNotIn(b"SYNTHETIC", file.read_bytes())

    def test_invalid_origin_or_credentials_send_nothing(self):
        invalid = ("http://example.invalid", "http://192.0.2.1", "http://[2001:db8::1]",
                   "https://127.0.0.1", "file:///tmp/snapshot", self.origin + "/other",
                   self.origin + "?token=SYNTHETIC", self.origin + "#fragment",
                   "http://user:SYNTHETIC@127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536",
                   self.origin + "\n", "http://127.1", "http://[::1%zone]")
        with patch("snapshot_collector.http.client.HTTPConnection") as connection:
            for origin in invalid:
                with self.subTest(origin=origin):
                    self.assert_failure(replace(self.config, origin=origin))
            connection.assert_not_called()
        self.assertEqual(self.state["requests"], [])

    def test_invalid_path_or_header_injection_send_nothing(self):
        invalid = ("https://example.invalid/x", "//example.invalid/x", "relative", "/x?token=SYNTHETIC",
                   "/x#frag", "/x\r\nX-Injected: value", "/space here", "/unicode-한글", "/x\\y", "/x?")
        for path in invalid:
            with self.subTest(path=path):
                self.assert_failure(replace(self.config, path=path))
        self.assertEqual(self.state["requests"], [])

    def test_invalid_limits_send_nothing(self):
        for timeout in (0, -1, True, float("nan"), float("inf"), 10**1000):
            self.assert_failure(replace(self.config, timeout_seconds=timeout))
        for maximum in (0, -1, True, 1.5, 10**1000):
            self.assert_failure(replace(self.config, max_bytes=maximum))
        self.assertEqual(self.state["requests"], [])

    def test_bad_output_context_sends_nothing(self):
        for phase in ("engine", "../pre", ""):
            with self.assertRaises(SnapshotCollectionError):
                self.collect(phase=phase)
        for field in self.context.__dataclass_fields__:
            with self.assertRaises(SnapshotCollectionError):
                self.collect(context=replace(self.context, **{field: ""}))
        with self.assertRaises(SnapshotCollectionError):
            self.collect(context=replace(self.context, run_id=chr(0xD800)))
        self.assertEqual(self.state["requests"], [])

    def test_existing_phase_or_invalid_root_sends_nothing(self):
        (self.root / "pre").mkdir()
        with self.assertRaises(SnapshotCollectionError):
            self.collect()
        with self.assertRaises(SnapshotCollectionError):
            self.collect(root=self.root / "absent")
        self.assertEqual(self.state["requests"], [])
        self.assertEqual(list((self.root / "pre").iterdir()), [])

    def test_symlink_output_root_sends_nothing(self):
        destination = self.root / "destination"
        destination.mkdir()
        link = self.root / "linked"
        link.symlink_to(destination, target_is_directory=True)
        with self.assertRaises(SnapshotCollectionError):
            self.collect(root=link)
        self.assertEqual(self.state["requests"], [])
        self.assertEqual(list(destination.iterdir()), [])

    def test_error_status_and_wrong_content_type_create_no_files(self):
        self.state["status"] = 503
        self.assert_failure()
        self.state.update(status=200, headers={"Content-Type": "text/html"})
        self.assert_failure()
        self.state["headers"] = {"Content-Type": None}
        self.assert_failure()

    def test_compressed_response_is_rejected(self):
        self.state["headers"] = {"Content-Encoding": "gzip"}
        self.assert_failure()

    def test_content_length_over_limit_is_rejected(self):
        self.state["declared_length"] = LIMIT + 1
        self.assert_failure()

    def test_unframed_response_over_limit_is_rejected(self):
        self.state.update(no_length=True, body=b"x" * (len(self.raw) + 1))
        self.assert_failure(replace(self.config, max_bytes=len(self.raw)))

    def test_chunked_valid_response_supported_and_over_limit_rejected(self):
        self.state["chunked"] = True
        self.assert_failure(replace(self.config, max_bytes=len(self.raw) - 1))
        result = self.collect()
        self.assertEqual((self.root / result.artifacts.raw["path"]).read_bytes(), self.raw)

    def test_truncated_and_malformed_lengths_create_no_files(self):
        self.state["declared_length"] = len(self.raw) + 5
        self.assert_failure()
        for value in (-1, "wrong", "1, 2"):
            self.state["declared_length"] = value
            self.assert_failure()

    def test_ambiguous_transfer_framing_is_rejected(self):
        self.state["headers"] = {"Transfer-Encoding": "chunked"}
        with self.assertRaisesRegex(SnapshotCollectionError, "^응답 전송 framing 부정$"):
            self.collect()
        self.assertEqual(list(self.root.iterdir()), [])

    def test_incomplete_chunk_is_rejected_by_reader_before_json_validation(self):
        self.state.update(chunked=True, body=b"not-json", wire_body=b"3\r\nabc\r\n")
        with self.assertRaisesRegex(SnapshotCollectionError, "^chunked 응답 길이 미완료$"):
            self.collect()
        self.assertEqual(list(self.root.iterdir()), [])

    def test_read_timeout_does_not_retry_or_write(self):
        self.state["delay"] = 0.1
        self.assert_failure(replace(self.config, timeout_seconds=0.02))
        self.assertEqual(len(self.state["requests"]), 1)

    def test_non_json_and_sensitive_body_never_persist(self):
        for body in (b"not JSON", b"\xff", json.dumps({"data": [], "password": "SYNTHETIC"}).encode()):
            self.state["body"] = body
            self.assert_failure()

    def test_bytes_after_declared_length_are_rejected(self):
        self.state.update(declared_length=len(self.raw), trailing=b"UNEXPECTED")
        self.assert_failure()

    def test_bytes_after_final_chunk_are_rejected(self):
        self.state.update(chunked=True, trailing=b"UNEXPECTED")
        self.assert_failure()

    def test_malformed_chunks_and_unsupported_trailers_are_rejected(self):
        self.state["chunked"] = True
        for wire in (b"g\r\nbody\r\n0\r\n\r\n", b"3\r\nabc!\r\n0\r\n\r\n",
                     b"3\r\nabc\r\n", b"0\r\nX-Trailer: value\r\n\r\n"):
            self.state["wire_body"] = wire
            self.assert_failure()

    def test_api_auth_aliases_are_rejected_without_persisting_values(self):
        rows = [{"key": key, "solved": False} for key in KEYS]
        for field in ("x-api-key", "X-API-Key", "API_KEY", "api-key", "access-token", "refresh-token"):
            self.state["body"] = json.dumps({"data": rows, "nested": [{field: "SYNTHETIC-PRIVATE"}]}).encode()
            with self.assertRaises(SnapshotCollectionError) as caught:
                self.collect()
            self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))
            self.assertEqual(list(self.root.iterdir()), [])

    def test_redacted_auth_alias_can_be_preserved(self):
        rows = [{"key": key, "solved": False} for key in KEYS]
        self.state["body"] = json.dumps({"data": rows, "x-api-key": "[REDACTED]"}).encode()
        self.assertEqual(self.collect().artifacts.challenge_count, len(KEYS))

    def test_clock_regression_is_rejected_before_write(self):
        start = datetime(2026, 10, 30, tzinfo=timezone.utc)
        with patch("snapshot_collector.datetime") as clock:
            clock.now.side_effect = [start, start - timedelta(seconds=1)]
            self.assert_failure()

    def test_pre_post_capture_and_writer_preserve_stage(self):
        first = self.collect()
        self.state["body"] = json.dumps({"data": [{"key": key, "solved": key == KEYS[0]} for key in KEYS]}).encode()
        second = self.collect(phase="post")
        self.assertEqual(len(self.state["requests"]), 2)
        self.assertEqual((first.artifacts.solved_count, second.artifacts.solved_count), (0, 1))
        self.assertEqual((first.artifacts.normalized["phase"], second.artifacts.normalized["phase"]), ("pre", "post"))


if __name__ == "__main__":
    unittest.main()
