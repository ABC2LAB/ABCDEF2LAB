"""원본 ② 규칙 주입(프로토콜 §8.1): 파일 형식, 등록 뒤 재확인, 회차 순서·기록, 비교 경계."""

from __future__ import annotations

import hashlib
import json
import os
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_config import RunnerConfigError, parse_runner_config, plan_run  # noqa: E402
from runner_engine import (  # noqa: E402
    MAX_RULES, MAX_RULES_FILE_BYTES, MAX_RULES_RESPONSE_BYTES, RULES_EVIDENCE_FORMAT, RULES_FORMAT, AdminSecret, EngineAPIError, apply_engine_rules,
    intercept_summary, load_engine_rules,
)
from test_preflight import configuration  # noqa: E402
from test_runner_config import runner_config  # noqa: E402

TOKEN = "synthetic.rules.token"
PASSWORD = "SYNTHETIC-RULES-PASSWORD"
SEEDED = {"id": 1, "name": "seeded-delete", "enabled": True, "priority": 80, "match_target": "tool_input",
          "match_type": "regex", "pattern": "seeded-pattern", "action": "deny", "message": "seeded",
          "timeout_enabled": False, "timeout_seconds": 60, "timeout_action": "deny",
          "created_at": "2026-11-06T00:00:00Z", "updated_at": "2026-11-06T00:00:00Z"}
SEEDED_OFF = dict(SEEDED, id=2, name="seeded-off", enabled=False)
TOOLS = ["Bash", "WebFetch", "shell_open"]  # 엔진 기본 목록을 줄이지 않는 파일 목록


def rule(name="block-post", **changes) -> dict:
    return {"name": name, "priority": 100, "match_target": "tool_input", "match_type": "regex", "pattern": "POST",
            "action": "deny", "message": "synthetic", **changes}


def rule_file(**changes) -> dict:
    return {"format": RULES_FORMAT, "enabled_tools": None, "rules": [rule()], **changes}


class FakeRulesEngine:
    """규칙 API를 원본과 같은 모양으로 흉내 낸다. 고장 모드로 조용한 누락·변조를 재현한다."""

    def __init__(self, seeded=(SEEDED, SEEDED_OFF), post_status=200, put_status=200, list_status=200, drop_rules=False,
                 change_rule=False, ignore_tools=False, list_body=None, tools_body=None, disconnect_post=False):
        self.rules = [dict(item) for item in seeded]
        self.tools = ["Bash", "WebFetch"]
        self.calls = []
        self.authorized = []
        engine = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                return

            def reply(self, status, payload):
                data = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def body(self):
                return json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")

            def do_POST(self):
                body = self.body()
                if self.path == "/api/auth/login":
                    self.reply(200, {"token": TOKEN})
                    return
                engine.calls.append(("POST", self.path, body))
                engine.authorized.append(self.headers.get("Authorization") == f"Bearer {TOKEN}")
                if disconnect_post:
                    self.close_connection = True
                    return
                if post_status != 200:
                    self.reply(post_status, {"error": "x"})
                    return
                created = dict(body, id=len(engine.rules) + 1, created_at="t", updated_at="t")
                if change_rule:
                    created["pattern"] = "changed-by-engine"
                if not drop_rules:
                    engine.rules.append(created)
                self.reply(200, created)

            def do_PUT(self):
                body = self.body()
                engine.calls.append(("PUT", self.path, body))
                engine.authorized.append(self.headers.get("Authorization") == f"Bearer {TOKEN}")
                if put_status == 200 and not ignore_tools:
                    engine.tools = body["enabled_tools"]
                self.reply(put_status, {"ok": True})

            def do_GET(self):
                engine.calls.append(("GET", self.path, None))
                engine.authorized.append(self.headers.get("Authorization") == f"Bearer {TOKEN}")
                if self.path == "/api/intercept/rules":
                    self.reply(list_status, list_body if list_body is not None else {"rules": engine.rules})
                else:
                    self.reply(200, tools_body if tools_body is not None else {"enabled_tools": engine.tools})

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class FileCase(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.dir = Path(temp.name)

    def write(self, payload: object, name="rules.json") -> Path:
        path = self.dir / name
        path.write_bytes(payload if isinstance(payload, bytes) else json.dumps(payload).encode())
        return path


class TestRuleFile(FileCase):
    def test_valid_file_keeps_its_exact_bytes_hash(self):
        data = json.dumps(rule_file(enabled_tools=["Bash"], rules=[rule(), rule("second", action="allow")]),
                          indent=2).encode()
        loaded = load_engine_rules(self.write(data))
        self.assertEqual(loaded.sha256, hashlib.sha256(data).hexdigest())
        self.assertEqual([item["name"] for item in loaded.rules], ["block-post", "second"])
        self.assertEqual(loaded.enabled_tools, ("Bash",))
        self.assertIsNone(load_engine_rules(self.write(rule_file())).enabled_tools)

    def test_invalid_files_are_refused(self):
        too_many = [rule(f"r{index}") for index in range(MAX_RULES + 1)]
        no_message = rule()
        del no_message["message"]
        cases = {
            "not_json": b"{",
            "not_utf8": b"\xff\xfe",
            "not_object": [],
            "format": rule_file(format="other"),
            "extra_key": dict(rule_file(), judge=None),
            "missing_key": {"format": RULES_FORMAT, "rules": [rule()]},
            "no_rules": rule_file(rules=[]),
            "rules_not_list": rule_file(rules={}),
            "too_many": rule_file(rules=too_many),
            "duplicate_name": rule_file(rules=[rule(), rule()]),
            "rule_not_object": rule_file(rules=["x"]),
            "rule_extra_key": rule_file(rules=[rule(enabled=False)]),
            "rule_missing_key": rule_file(rules=[no_message]),
            "empty_name": rule_file(rules=[rule("")]),
            "name_not_text": rule_file(rules=[rule(7)]),
            "priority_text": rule_file(rules=[rule(priority="1")]),
            "priority_bool": rule_file(rules=[rule(priority=True)]),
            "target": rule_file(rules=[rule(match_target="url")]),
            "type": rule_file(rules=[rule(match_type="glob")]),
            "empty_pattern": rule_file(rules=[rule(pattern="")]),
            "pattern_not_text": rule_file(rules=[rule(pattern=1)]),
            "ask_needs_a_person": rule_file(rules=[rule(action="ask")]),
            "message_not_text": rule_file(rules=[rule(message=None)]),
            "tools_empty": rule_file(enabled_tools=[]),
            "tools_not_list": rule_file(enabled_tools="Bash"),
            "tools_empty_name": rule_file(enabled_tools=[""]),
            "tools_not_text": rule_file(enabled_tools=[1]),
            "tools_duplicate": rule_file(enabled_tools=["Bash", "Bash"]),
        }
        for label, payload in cases.items():
            with self.subTest(label), self.assertRaisesRegex(EngineAPIError, "engine_rules_file_invalid"):
                load_engine_rules(self.write(payload))

    def test_duplicate_json_keys_and_oversize_are_refused(self):
        # 중복만 틀린 파일이다. 마지막 값으로 읽으면 통과하는 내용이어야 이 가드를 본다.
        duplicate = b'{"format": "x", "format": "%s", "enabled_tools": null, "rules": %s}' % (
            RULES_FORMAT.encode(), json.dumps([rule()]).encode())
        big = json.dumps(rule_file()).encode() + b" " * (MAX_RULES_FILE_BYTES + 1)
        for label, data in (("duplicate", duplicate), ("big", big)):
            with self.subTest(label), self.assertRaisesRegex(EngineAPIError, "engine_rules_file_invalid"):
                load_engine_rules(self.write(data))

    def test_size_limit_is_inclusive(self):
        data = json.dumps(rule_file()).encode()
        loaded = load_engine_rules(self.write(data + b" " * (MAX_RULES_FILE_BYTES - len(data))))
        self.assertEqual(len(loaded.rules), 1)

    def test_rule_count_limit_is_inclusive(self):
        rules = [rule(f"r{index}") for index in range(MAX_RULES)]
        self.assertEqual(len(load_engine_rules(self.write(rule_file(rules=rules))).rules), MAX_RULES)

    def test_missing_file_has_its_own_code(self):
        with self.assertRaisesRegex(EngineAPIError, "engine_rules_file_unreadable"):
            load_engine_rules(self.dir / "absent.json")


class TestApply(FileCase):
    def apply(self, payload=None, **engine_options):
        engine = FakeRulesEngine(**engine_options)
        self.addCleanup(engine.close)
        rules = load_engine_rules(self.write(rule_file() if payload is None else payload))
        return engine, apply_engine_rules(engine.port, AdminSecret(PASSWORD), rules, 5)

    def test_rules_are_added_on_top_of_seeded_and_read_back(self):
        engine, result = self.apply(rule_file(enabled_tools=["WebFetch", "Bash", "shell_open"],
                                              rules=[rule(), rule("second", priority=90)]))
        self.assertEqual([call[0] for call in engine.calls], ["GET", "GET", "POST", "POST", "PUT", "GET", "GET"])
        self.assertTrue(all(engine.authorized))
        self.assertEqual(result["format"], RULES_EVIDENCE_FORMAT)
        self.assertEqual((result["injected"], result["seeded"], result["seeded_enabled"]), (2, 2, 1))
        self.assertEqual(result["tools_before"], ["Bash", "WebFetch"])
        self.assertEqual(result["enabled_tools"], ["WebFetch", "Bash", "shell_open"])
        self.assertEqual([item["name"] for item in engine.rules], ["seeded-delete", "seeded-off", "block-post", "second"])
        self.assertNotEqual(result["seeded_sha256"], result["active_sha256"])
        created = engine.calls[2][2]
        self.assertEqual((created["enabled"], created["timeout_enabled"], created["action"]), (True, False, "deny"))

    def test_tools_are_left_alone_when_the_file_does_not_set_them(self):
        engine, result = self.apply()
        self.assertNotIn("PUT", [call[0] for call in engine.calls])
        self.assertEqual(result["enabled_tools"], ["Bash", "WebFetch"])

    def test_hashes_ignore_engine_ids_and_times(self):
        _, one = self.apply()
        moved = [dict(SEEDED_OFF, id=9, created_at="other"), dict(SEEDED, id=8, updated_at="other")]
        _, two = self.apply(seeded=moved)
        self.assertEqual(one["seeded_sha256"], two["seeded_sha256"])
        self.assertEqual(one["active_sha256"], two["active_sha256"])

    def test_evidence_has_no_rule_text(self):
        _, result = self.apply(rule_file(rules=[rule("SECRET-NAME", pattern="SECRET-PATTERN", message="SECRET-MSG")]))
        self.assertNotIn("SECRET", json.dumps(result))

    def test_silent_engine_failures_are_caught_by_reading_back(self):
        cases = {"dropped": {"drop_rules": True}, "changed": {"change_rule": True}, "tools_ignored": {"ignore_tools": True}}
        for label, options in cases.items():
            with self.subTest(label), self.assertRaisesRegex(EngineAPIError, "engine_rules_not_applied"):
                self.apply(rule_file(enabled_tools=TOOLS), **options)

    def test_narrowing_the_tools_is_refused_before_any_write(self):
        # 도구를 빼면 내장 규칙이 그 도구에 안 걸려 원본 ②가 원본 ①보다 약해진다.
        for tools in (["Bash"], ["WebFetch", "shell_open"]):
            with self.subTest(tools=tools):
                engine = FakeRulesEngine()
                self.addCleanup(engine.close)
                rules = load_engine_rules(self.write(rule_file(enabled_tools=tools)))
                with self.assertRaisesRegex(EngineAPIError, "engine_rules_tools_narrowed"):
                    apply_engine_rules(engine.port, AdminSecret(PASSWORD), rules, 5)
                self.assertEqual([call[0] for call in engine.calls], ["GET", "GET"])
                self.assertEqual(engine.tools, ["Bash", "WebFetch"])
                self.assertEqual([item["name"] for item in engine.rules], ["seeded-delete", "seeded-off"])

    def test_same_tools_in_another_order_are_not_narrowing(self):
        _, result = self.apply(rule_file(enabled_tools=["WebFetch", "Bash"]))
        self.assertEqual(result["enabled_tools"], ["WebFetch", "Bash"])

    def test_allow_rules_that_can_get_ahead_of_seeded_controls_are_counted(self):
        low = dict(SEEDED, id=3, name="seeded-low", priority=50, action="ask")
        high = dict(SEEDED, id=4, name="seeded-high", priority=90)
        cases = (("above", (SEEDED,), [rule(action="allow", priority=81)], 1),
                 ("tie_seeded_goes_first", (SEEDED,), [rule(action="allow", priority=80)], 0),
                 ("below", (SEEDED,), [rule(action="allow", priority=79)], 0),
                 ("deny_does_not_count", (SEEDED,), [rule(action="deny", priority=200)], 0),
                 ("disabled_seeded_is_not_a_control", (SEEDED_OFF,), [rule(action="allow", priority=200)], 0),
                 ("seeded_allow_is_not_a_control", (dict(SEEDED, action="allow"),), [rule(action="allow", priority=200)], 0),
                 ("no_seeded", (), [rule(action="allow", priority=200)], 0),
                 ("ahead_of_the_lowest_control_only", (low, high), [rule(action="allow", priority=60)], 1),
                 ("two", (SEEDED,), [rule("a", action="allow", priority=81), rule("b", action="allow", priority=82),
                                     rule("c", priority=83)], 2))
        for label, seeded, rules, expected in cases:
            with self.subTest(label):
                _, result = self.apply(rule_file(rules=rules), seeded=seeded)
                self.assertEqual(result["allow_over_seeded"], expected)

    def test_name_colliding_with_a_seeded_rule_stops_before_any_write(self):
        engine = FakeRulesEngine()
        self.addCleanup(engine.close)
        rules = load_engine_rules(self.write(rule_file(rules=[rule("seeded-delete")])))
        with self.assertRaisesRegex(EngineAPIError, "engine_rules_name_collision"):
            apply_engine_rules(engine.port, AdminSecret(PASSWORD), rules, 5)
        self.assertEqual([call[0] for call in engine.calls], ["GET"])

    def test_engine_errors_have_fixed_codes(self):
        cases = (({"post_status": 400}, "engine_rules_http_400"), ({"put_status": 500}, "engine_rules_http_500"),
                 ({"list_status": 503}, "engine_rules_http_503"),
                 ({"list_body": b"not json"}, "engine_rules_response_invalid"),
                 ({"list_body": {"items": []}}, "engine_rules_response_invalid"),
                 ({"list_body": {"rules": [{"name": "x"}]}}, "engine_rules_response_invalid"),
                 ({"tools_body": {"enabled_tools": "Bash"}}, "engine_rules_response_invalid"),
                 ({"tools_body": []}, "engine_rules_response_invalid"),
                 ({"disconnect_post": True}, "engine_rules_unreachable"))
        for options, code in cases:
            with self.subTest(code=code, options=sorted(options)), self.assertRaisesRegex(EngineAPIError, code):
                self.apply(rule_file(enabled_tools=TOOLS), **options)

    def test_failed_write_is_not_retried(self):
        engine = FakeRulesEngine(post_status=500)
        self.addCleanup(engine.close)
        rules = load_engine_rules(self.write(rule_file(rules=[rule(), rule("second")])))
        with self.assertRaises(EngineAPIError):
            apply_engine_rules(engine.port, AdminSecret(PASSWORD), rules, 5)
        self.assertEqual([call[0] for call in engine.calls].count("POST"), 1)

    def test_oversized_engine_reply_is_refused(self):
        with self.assertRaisesRegex(EngineAPIError, "engine_rules_response_invalid"):
            self.apply(list_body=b'{"rules": []}' + b" " * MAX_RULES_RESPONSE_BYTES)


def history_row(status="denied", source="rule", rule_name="block-post", tool="Bash"):
    return {"id": 1, "rule_id": 7, "conversation_id": None, "task_id": "1", "agent_name": "w", "tool_name": tool,
            "tool_input": {"command": "SECRET-TOOL-INPUT"}, "status": status, "decision_source": source,
            "reason": "SECRET-REASON", "decided_at": None, "created_at": "t", "conv_title": "SECRET-TITLE",
            "conv_agent_key": "k", "rule_name": rule_name}


class FakeHistory:
    """승인 기록 API. 쪽 크기를 지켜 나눠 준다."""

    def __init__(self, rows, status=200, body=None, total=None, page_limit=None):
        self.requests = []
        history = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                return

            def reply(self, code, data):
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                self.reply(200, json.dumps({"token": TOKEN}).encode())

            def do_GET(self):
                query = parse_qs(urlsplit(self.path).query)
                page, size = int(query["page"][0]), int(query["size"][0])
                history.requests.append((urlsplit(self.path).path, page, size, self.headers.get("Authorization")))
                if body is not None:
                    self.reply(status, body)
                    return
                chunk = rows[(page - 1) * size:page * size]
                self.reply(status, json.dumps({"items": chunk, "total": len(rows) if total is None else total,
                                               "page": page, "page_size": size}).encode())

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class TestInterceptSummary(unittest.TestCase):
    def summarize(self, rows, file_rules=("block-post",), **options):
        history = FakeHistory(rows, **options)
        self.addCleanup(history.close)
        return history, intercept_summary(history.port, AdminSecret(PASSWORD), frozenset(file_rules), 5)

    def test_counts_by_origin_status_and_tool(self):
        rows = [history_row(), history_row(tool="WebFetch"), history_row(rule_name="seeded-delete"),
                history_row(source="model", rule_name=""), history_row(source="unknown", rule_name=""),
                history_row(status="allowed"), history_row(status="pending"), history_row(status="timeout")]
        history, summary = self.summarize(rows)
        self.assertEqual(summary["total"], 8)
        self.assertEqual(summary["by_status"], {"allowed": 1, "denied": 5, "pending": 1, "timeout": 1})
        self.assertEqual(summary["denied_by_origin"], {"file_rule": 2, "seeded_rule": 1, "model": 1, "other": 1})
        self.assertEqual(summary["denied_by_tool"], {"Bash": 4, "WebFetch": 1})
        self.assertEqual(history.requests, [("/api/intercept/history", 1, 100, f"Bearer {TOKEN}")])

    def test_every_page_is_read(self):
        rows = [history_row() for _ in range(205)]
        history, summary = self.summarize(rows)
        self.assertEqual([request[1] for request in history.requests], [1, 2, 3])
        self.assertEqual((summary["total"], summary["denied_by_tool"]), (205, {"Bash": 205}))

    def test_no_rows_is_a_measured_zero(self):
        _, summary = self.summarize([])
        self.assertEqual((summary["total"], summary["denied_by_tool"]), (0, {}))

    def test_tool_input_and_reasons_never_reach_the_summary(self):
        _, summary = self.summarize([history_row(), history_row(tool="bad tool name/\n")])
        text = json.dumps(summary)
        self.assertNotIn("SECRET", text)
        self.assertNotIn("bad tool", text)
        self.assertEqual(summary["denied_by_tool"], {"Bash": 1, "OTHER": 1})

    def test_without_file_rules_every_rule_denial_is_seeded(self):
        _, summary = self.summarize([history_row()], file_rules=())
        self.assertEqual(summary["denied_by_origin"]["seeded_rule"], 1)

    def test_engine_failures_have_fixed_codes(self):
        cases = (({"status": 500}, "engine_intercept_http_500"),
                 ({"body": b"not json"}, "engine_intercept_response_invalid"),
                 ({"body": b"[]"}, "engine_intercept_response_invalid"),
                 ({"body": b'{"items": {}, "total": 0}'}, "engine_intercept_response_invalid"),
                 ({"body": b'{"items": [], "total": "0"}'}, "engine_intercept_response_invalid"),
                 ({"body": b'{"items": [], "total": -1}'}, "engine_intercept_response_invalid"),
                 ({"body": b'{"items": [], "total": 3}'}, "engine_intercept_response_invalid"),
                 ({"body": b'{"items": ["x"], "total": 1}'}, "engine_intercept_response_invalid"),
                 ({"body": json.dumps({"items": [history_row(status="new")], "total": 1}).encode()},
                  "engine_intercept_response_invalid"),
                 ({"body": json.dumps({"items": [history_row()], "total": 0}).encode()},
                  "engine_intercept_response_invalid"))
        for options, code in cases:
            with self.subTest(code=code, options=str(options)[:40]), self.assertRaisesRegex(EngineAPIError, code):
                self.summarize([], **options)

    def test_page_limit_stops_instead_of_undercounting(self):
        with patch("runner_engine.MAX_INTERCEPT_PAGES", 2), self.assertRaisesRegex(EngineAPIError, "engine_intercept_too_many"):
            self.summarize([history_row() for _ in range(201)])

    def test_oversized_page_is_refused(self):
        body = b'{"items": [], "total": 0}' + b" " * 64
        with patch("runner_engine.MAX_INTERCEPT_PAGE_BYTES", 32), self.assertRaisesRegex(
                EngineAPIError, "engine_intercept_response_invalid"):
            self.summarize([], body=body)


class TestConfig(unittest.TestCase):
    def config(self, value):
        return parse_runner_config(dict(runner_config(), engine_rules=value))

    def test_key_is_required_and_path_must_be_absolute(self):
        payload = runner_config()
        del payload["engine_rules"]
        with self.assertRaises(RunnerConfigError) as caught:
            parse_runner_config(payload)
        self.assertIn("config.engine_rules: MISSING", caught.exception.errors)
        for value in ("relative.json", "/a/../b.json", 1):
            with self.subTest(value=value), self.assertRaises(RunnerConfigError) as caught:
                self.config(value)
            self.assertIn("config.engine_rules: EXPECTED_ABSOLUTE_PATH", caught.exception.errors)
        self.assertIsNone(self.config(None).engine_rules)
        self.assertEqual(self.config("/srv/eval/rules.json").engine_rules, "/srv/eval/rules.json")

    def test_rules_are_for_the_original_engine_only(self):
        config = self.config("/srv/eval/rules.json")
        self.assertEqual(plan_run(config, configuration(), "baseline", "baseline-1").side, "baseline")
        with self.assertRaises(RunnerConfigError) as caught:
            plan_run(config, configuration(), "ours", "ours-1")
        self.assertEqual(caught.exception.errors, ["config.engine_rules: BASELINE_ONLY"])
        self.assertEqual(plan_run(self.config(None), configuration(), "ours", "ours-2").side, "ours")


if __name__ == "__main__":
    unittest.main()
