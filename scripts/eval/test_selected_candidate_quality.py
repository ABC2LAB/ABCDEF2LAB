import importlib.util
import json
import pathlib
import http.server
import threading
import unittest


ROOT = pathlib.Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("selected_candidate_quality", ROOT / "selected_candidate_quality.py")
quality = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(quality)


class SelectedCandidateQualityTest(unittest.TestCase):
    def setUp(self):
        self.cases = quality.load_cases(ROOT / "fixtures" / "selected_candidate_gold.json")
        self.mock = json.loads((ROOT / "fixtures" / "selected_candidate_mock.json").read_text(encoding="utf-8"))

    def test_gold_set_includes_previous_seven_and_non_header_limits(self):
        ids = {case["id"] for case in self.cases}
        self.assertTrue({"missing-header", "normal-header", "unrelated-function", "no-second-evidence",
                         "conflicting-setting", "copied-attempt", "other-policy", "different-account-same-bearer",
                         "response-truncated", "business-rule-unknown", "recovery-unknown", "forged-source"} <= ids)

    def test_answer_and_rule_do_not_enter_model_input(self):
        for case in self.cases:
            raw = json.dumps(quality.model_input(case))
            self.assertNotIn("expected", raw)
            self.assertNotIn("rule_verdict", raw)
            self.assertNotIn(case["id"], raw)

    def test_mock_misjudgment_counts(self):
        for value in self.mock.values():
            quality.parse_judgment(value)
        totals, _ = quality.classify(self.cases, {"model": lambda c: self.mock[c["id"]]["verdict"]})
        self.assertGreaterEqual(totals["model"]["wrong_decisive_on_insufficient"], 5)

    def test_invalid_model_json_fails(self):
        for raw in ("{}", '{"verdict":"confirmed","reason":"yes"}',
                    '{"verdict":"model_supported","reason":"","extra":1}'):
            with self.assertRaises(ValueError):
                quality.parse_judgment(raw)

    def test_paid_or_remote_endpoint_rejected_before_send(self):
        for url in ("https://api.openrouter.ai/v1/chat/completions",
                    "http://192.0.2.1/v1/chat/completions", "http://localhost/v1/chat/completions",
                    "http://127.0.0.1/v1/other", "http://127.0.0.1/v1/chat/completions?redirect=1"):
            with self.assertRaises(ValueError):
                quality.local_model(url, "synthetic", self.cases[0])

    def test_local_redirect_never_follows_paid_endpoint(self):
        class Redirect(http.server.BaseHTTPRequestHandler):
            hits = 0

            def do_POST(self):
                Redirect.hits += 1
                self.send_response(302)
                self.send_header("Location", "https://api.openrouter.ai/v1/chat/completions")
                self.end_headers()

            def log_message(self, *_):
                pass

        host = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Redirect)
        thread = threading.Thread(target=host.serve_forever, daemon=True)
        thread.start()
        try:
            with self.assertRaises(ValueError):
                quality.local_model(f"http://127.0.0.1:{host.server_port}/v1/chat/completions", "synthetic", self.cases[0])
            self.assertEqual(Redirect.hits, 1)
        finally:
            host.shutdown()
            host.server_close()
            thread.join()


if __name__ == "__main__":
    unittest.main()
