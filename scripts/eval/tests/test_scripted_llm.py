"""스모크 도구: 스크립트 LLM의 도구 호출 순서·형식과 합성 토큰 검사의 판정."""

from __future__ import annotations

import contextlib
import http.client
import io
import json
import os
import sys
import tempfile
import threading
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from canary_scan import main as scan_main  # noqa: E402
from scripted_llm import (  # noqa: E402
    DONE_TEXT, MAX_PLAN_BYTES, PlanError, Stats, decide, load_plan, main as llm_main, make_server,
)

PLAN = {"model": "example-model:1", "intent_summary": "example intent", "commands": ["curl one", "curl two"]}


def tools(*names):
    return [{"type": "function", "function": {"name": name}} for name in names]


def request(names, results=0):
    return {"tools": tools(*names), "messages": [{"role": "user", "content": "x"}] + [{"role": "tool"}] * results}


class TestDecide(unittest.TestCase):
    def test_planner_adds_one_intent_then_stops(self):
        self.assertEqual(decide(request(["add_intent", "Bash"]), PLAN), ("add_intent", {"summary": "example intent", "priority": 5}))
        self.assertIsNone(decide(request(["add_intent", "Bash"], results=1), PLAN))

    def test_worker_walks_the_commands_in_order_then_stops(self):
        self.assertEqual(decide(request(["Bash"]), PLAN)[1]["command"], "curl one")
        self.assertEqual(decide(request(["Bash"], results=1), PLAN)[1]["command"], "curl two")
        self.assertIsNone(decide(request(["Bash"], results=2), PLAN))

    def test_other_requests_get_no_tool_call(self):
        self.assertIsNone(decide(request(["add_task_scope"]), PLAN))
        self.assertIsNone(decide({}, PLAN))


class TestPlan(unittest.TestCase):
    def write(self, payload):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        path = Path(temp.name) / "plan.json"
        path.write_bytes(payload if isinstance(payload, bytes) else json.dumps(payload).encode())
        return path

    def test_valid_plan_loads(self):
        self.assertEqual(load_plan(self.write(PLAN)), PLAN)

    def test_invalid_plans_are_refused(self):
        cases = {"not_json": b"{", "not_utf8": b"\xff", "not_object": [], "missing": {"model": "m"},
                 "extra": dict(PLAN, secret="x"), "model": dict(PLAN, model=""), "model_type": dict(PLAN, model=1),
                 "summary": dict(PLAN, intent_summary=""), "summary_type": dict(PLAN, intent_summary=1),
                 "no_commands": dict(PLAN, commands=[]), "commands_type": dict(PLAN, commands="curl"),
                 "empty_command": dict(PLAN, commands=[""]), "command_type": dict(PLAN, commands=[1]),
                 "too_big": json.dumps(PLAN).encode() + b" " * MAX_PLAN_BYTES}
        for label, payload in cases.items():
            with self.subTest(label), self.assertRaisesRegex(PlanError, "plan_invalid"):
                load_plan(self.write(payload))

    def test_missing_file_has_its_own_code(self):
        with self.assertRaisesRegex(PlanError, "plan_unreadable"):
            load_plan("/nonexistent/plan.json")

    def test_cli_exits_two_on_a_bad_plan(self):
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            code = llm_main(["--host", "127.0.0.1", "--port", "0", "--plan", "/nonexistent/plan.json"])
        self.assertEqual((code, err.getvalue().strip()), (2, "plan_unreadable"))


class TestServer(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.stats_path = Path(temp.name) / "stats.json"
        self.server = make_server("127.0.0.1", 0, PLAN, Stats(str(self.stats_path)))
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def post(self, body):
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=5)
        self.addCleanup(connection.close)
        connection.request("POST", "/v1/chat/completions", body=body if isinstance(body, bytes) else json.dumps(body))
        response = connection.getresponse()
        return response.status, response.read()

    def test_model_list(self):
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=5)
        self.addCleanup(connection.close)
        connection.request("GET", "/v1/models")
        self.assertEqual(json.loads(connection.getresponse().read())["data"][0]["id"], "example-model:1")

    def test_plain_tool_call_and_done(self):
        status, data = self.post(request(["Bash"]))
        choice = json.loads(data)["choices"][0]
        self.assertEqual((status, choice["finish_reason"]), (200, "tool_calls"))
        call = choice["message"]["tool_calls"][0]["function"]
        self.assertEqual((call["name"], json.loads(call["arguments"])["command"]), ("Bash", "curl one"))
        _, data = self.post(request(["Bash"], results=2))
        choice = json.loads(data)["choices"][0]
        self.assertEqual((choice["finish_reason"], choice["message"]["content"]), ("stop", DONE_TEXT))

    def test_stream_carries_the_call_and_ends_with_done_marker(self):
        status, data = self.post(dict(request(["Bash"]), stream=True))
        events = [line[6:] for line in data.decode().split("\n\n") if line.startswith("data: ")]
        self.assertEqual((status, events[-1]), (200, "[DONE]"))
        first, last = json.loads(events[0]), json.loads(events[1])
        call = first["choices"][0]["delta"]["tool_calls"][0]["function"]
        self.assertEqual(json.loads(call["arguments"])["command"], "curl one")
        self.assertEqual(last["choices"][0]["finish_reason"], "tool_calls")
        _, data = self.post(dict(request(["Bash"], results=2), stream=True))
        first = json.loads(data.decode().split("\n\n")[0][6:])
        self.assertEqual(first["choices"][0]["delta"]["content"], DONE_TEXT)

    def test_bad_requests_are_refused_and_not_counted(self):
        for body in (b"{", b"[]"):
            with self.subTest(body=body):
                self.assertEqual(self.post(body)[0], 400)
        self.assertFalse(self.stats_path.exists())

    def test_stats_hold_counts_and_no_request_text(self):
        self.post({"tools": tools("Bash"), "messages": [{"role": "user", "content": "SECRET-PROMPT"}]})
        self.post(request(["add_intent"]))
        self.post(request(["Bash"], results=2))
        text = self.stats_path.read_text(encoding="utf-8")
        self.assertEqual(json.loads(text), {"requests": 3, "planner_calls": 1, "worker_calls": 1, "done": 1})
        self.assertNotIn("SECRET", text)


class TestCanaryScan(unittest.TestCase):
    CANARY = "SYNTHETICCANARY0a1b2c3d"

    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name) / "run"
        (self.root / "meter").mkdir(parents=True)
        (self.root / "a.json").write_text("{}", encoding="utf-8")

    def run_it(self, *args, canary=None):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = scan_main([*args, "--canary", canary or self.CANARY])
        return code, out.getvalue(), err.getvalue()

    def test_clean_directory_passes(self):
        code, out, _ = self.run_it(str(self.root))
        self.assertEqual((code, out.strip()), (0, "files=1 hits=0 unreadable=0"))

    def test_a_hit_fails_and_names_the_file_not_the_value(self):
        (self.root / "meter" / "log.jsonl").write_bytes(b"\x00binary " + self.CANARY.encode() + b" tail")
        code, out, _ = self.run_it(str(self.root))
        self.assertEqual(code, 1)
        self.assertIn("files=2 hits=1", out)
        self.assertIn(f"hit: {os.path.join('meter', 'log.jsonl')}", out)
        self.assertNotIn(self.CANARY, out)

    def test_unreadable_file_is_not_a_pass(self):
        target = self.root / "private.json"
        target.write_text(self.CANARY, encoding="utf-8")
        target.chmod(0)
        self.addCleanup(target.chmod, 0o600)
        if os.access(target, os.R_OK):
            self.skipTest("이 환경은 권한 0 파일도 읽는다")
        code, out, _ = self.run_it(str(self.root))
        self.assertEqual(code, 2)
        self.assertIn("unreadable: private.json", out)

    def test_directory_that_cannot_be_entered_is_not_a_pass(self):
        locked = self.root / "locked"
        locked.mkdir()
        (locked / "inner.txt").write_text(self.CANARY, encoding="utf-8")
        locked.chmod(0)
        self.addCleanup(locked.chmod, 0o700)
        if os.access(locked, os.R_OK):
            self.skipTest("이 환경은 권한 0 디렉터리도 읽는다")
        code, out, _ = self.run_it(str(self.root))
        self.assertEqual(code, 2)
        self.assertIn("unreadable: locked", out)

    def test_value_in_a_name_or_link_target_is_a_hit_and_stays_masked(self):
        cases = {"file_name": lambda: (self.root / f"x-{self.CANARY}.json").write_text("{}", encoding="utf-8"),
                 "directory_name": lambda: (self.root / f"d-{self.CANARY}").mkdir(),
                 "link_name": lambda: (self.root / f"l-{self.CANARY}").symlink_to("a.json"),
                 "link_target": lambda: (self.root / "link").symlink_to(f"/nonexistent/{self.CANARY}"),
                 "directory_link_name": lambda: (self.root / f"dl-{self.CANARY}").symlink_to(self.root / "meter"),
                 "directory_link_target": lambda: (self.root / "dl").symlink_to(self.named_outside())}
        for label, make in cases.items():
            with self.subTest(label):
                make()
                code, out, _ = self.run_it(str(self.root))
                self.assertEqual(code, 1)
                self.assertIn("hits=1", out)
                self.assertNotIn(self.CANARY, out)
                for path in list(self.root.iterdir()):
                    if path.name not in ("a.json", "meter"):
                        path.rmdir() if path.is_dir() and not path.is_symlink() else path.unlink()

    def named_outside(self) -> Path:
        outside = self.root.parent / f"outside-{self.CANARY}"
        outside.mkdir(exist_ok=True)
        return outside

    def test_directory_links_are_not_entered(self):
        # 링크 대상 경로에 값이 없으면 그 안의 파일은 회차 디렉터리 밖이므로 보지 않는다
        outside = self.root.parent / "outside"
        outside.mkdir()
        (outside / "inner.txt").write_text(self.CANARY, encoding="utf-8")
        (self.root / "linked").symlink_to(outside)
        code, out, _ = self.run_it(str(self.root))
        self.assertEqual((code, out.strip()), (0, "files=1 hits=0 unreadable=0"))

    def test_name_and_content_in_one_file_count_once(self):
        (self.root / f"x-{self.CANARY}.json").write_text(self.CANARY, encoding="utf-8")
        code, out, _ = self.run_it(str(self.root))
        self.assertEqual(code, 1)
        self.assertIn("hits=1", out)
        self.assertIn("hit: x-[CANARY].json", out)

    def test_unreadable_path_output_is_masked(self):
        target = self.root / f"p-{self.CANARY}.json"
        target.write_text("{}", encoding="utf-8")
        target.chmod(0)
        self.addCleanup(target.chmod, 0o600)
        if os.access(target, os.R_OK):
            self.skipTest("이 환경은 권한 0 파일도 읽는다")
        _, out, _ = self.run_it(str(self.root))
        self.assertIn("unreadable: p-[CANARY].json", out)
        self.assertNotIn(self.CANARY, out)

    def test_symlinks_are_not_followed(self):
        outside = self.root.parent / "outside.txt"
        outside.write_text(self.CANARY, encoding="utf-8")
        (self.root / "link").symlink_to(outside)
        code, out, _ = self.run_it(str(self.root))
        self.assertEqual((code, out.strip()), (0, "files=1 hits=0 unreadable=0"))

    def test_input_errors(self):
        code, _, err = self.run_it(str(self.root), canary="short")
        self.assertEqual((code, err.strip()), (2, "canary_invalid"))
        code, _, err = self.run_it(str(self.root), canary="비아스키값" * 5)
        self.assertEqual((code, err.strip()), (2, "canary_invalid"))
        code, _, err = self.run_it(str(self.root / "missing"))
        self.assertEqual((code, err.strip()), (2, "root_not_directory"))

    def test_empty_directory_is_not_a_pass(self):
        empty = self.root.parent / "empty"
        empty.mkdir()
        self.assertEqual(self.run_it(str(empty))[0], 2)


if __name__ == "__main__":
    unittest.main()
