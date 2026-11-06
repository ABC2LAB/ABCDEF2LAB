"""로컬 서빙 조건 읽기·대조. 합성 HTTP 서버만 쓴다."""

from __future__ import annotations

import json
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from runner_serving import MAX_RESPONSE_BYTES, MISMATCH, UNVERIFIED, ServingError, judge_serving, read_serving  # noqa: E402

MODEL = "example-model:fixed-tag"
DIGEST = "a" * 64
DECLARED = {"server_version": "0.32.6", "model_digest": DIGEST, "context_length": 32768}


class Case(unittest.TestCase):
    def setUp(self):
        state = self.state = {"calls": [], "status": 200, "version": {"version": "0.32.6"}, "done": {"done": True},
                              "ps": {"models": [{"name": MODEL, "digest": DIGEST, "context_length": 32768, "size": 100,
                                                 "size_vram": 80, "details": {"quantization_level": "Q4"}}]},
                              "raw": None}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def _send(self, payload):
                body = state["raw"] if state["raw"] is not None else json.dumps(payload).encode()
                self.send_response(state["status"])
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                state["calls"].append(("GET", self.path, None))
                self._send({"/api/version": state["version"], "/api/ps": state["ps"]}[self.path])

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                state["calls"].append(("POST", self.path, body))
                self._send(state["done"])

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server.daemon_threads = True
        thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=0.01), daemon=True)
        thread.start()
        self.addCleanup(lambda: (server.shutdown(), server.server_close(), thread.join(timeout=1)))
        self.port = server.server_port

    def read(self, load=False):
        return read_serving("127.0.0.1", self.port, MODEL, 2, load=load)


class TestRead(Case):
    def test_reads_declared_fields_and_records_the_rest(self):
        observed = self.read()
        self.assertEqual(observed, {"server_version": "0.32.6", "model_digest": DIGEST, "context_length": 32768,
                                    "size": 100, "size_vram": 80, "quantization_level": "Q4", "other_models_loaded": 0})
        self.assertEqual([c[:2] for c in self.state["calls"]], [("GET", "/api/version"), ("GET", "/api/ps")])

    def test_load_asks_for_the_model_first_and_only_when_requested(self):
        self.read(load=True)
        self.assertEqual(self.state["calls"][0], ("POST", "/api/generate", {"model": MODEL, "stream": False}))

    def test_load_that_did_not_finish_fails(self):
        self.state["done"] = {"done": False}
        with self.assertRaises(ServingError):
            self.read(load=True)

    def test_model_not_loaded_has_no_digest(self):
        self.state["ps"] = {"models": [{"name": "other:tag", "digest": "b" * 64, "context_length": 1}]}
        observed = self.read()
        self.assertIsNone(observed["model_digest"])
        self.assertIsNone(observed["context_length"])
        self.assertEqual(observed["other_models_loaded"], 1)

    def test_bad_field_types_do_not_read_as_values(self):
        self.state["ps"]["models"][0].update(digest=1, context_length=True, size="x", details=[])
        observed = self.read()
        self.assertEqual((observed["model_digest"], observed["context_length"], observed["size"],
                          observed["quantization_level"]), (None, None, None, None))

    def test_unreadable_responses_fail_closed(self):
        for change in ({"status": 500}, {"raw": b"not json"}, {"raw": b"[]"}, {"raw": b"x" * (MAX_RESPONSE_BYTES + 1)},
                       {"version": {"version": 1}}, {"ps": {"models": "x"}}, {"ps": {"models": ["x"]}}, {"ps": {}}):
            saved = dict(self.state)
            self.state.update(change)
            with self.subTest(change=str(change)[:40]), self.assertRaises(ServingError):
                self.read()
            self.state.update(saved)

    def test_connection_failure_fails_closed(self):
        with self.assertRaises(ServingError):
            read_serving("127.0.0.1", 1, MODEL, 0.2, load=False)


class TestJudge(unittest.TestCase):
    def observed(self, **change):
        return dict(DECLARED, size=1, size_vram=0, quantization_level=None, other_models_loaded=3) | change

    def test_same_values_pass_and_recorded_fields_are_not_judged(self):
        self.assertIsNone(judge_serving(self.observed(), DECLARED))

    def test_each_declared_field_is_compared(self):
        for name, value in (("server_version", "0.33.0"), ("model_digest", "b" * 64), ("context_length", 4096)):
            with self.subTest(name):
                self.assertEqual(judge_serving(self.observed(**{name: value}), DECLARED), MISMATCH)

    def test_unloaded_model_is_unverified_not_mismatch(self):
        self.assertEqual(judge_serving(self.observed(model_digest=None), DECLARED), UNVERIFIED)
        self.assertEqual(judge_serving(self.observed(context_length=None), DECLARED), UNVERIFIED)


if __name__ == "__main__":
    unittest.main()
