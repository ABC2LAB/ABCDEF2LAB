"""분리 배치 O 합성 모델(scripted_product_llm): 계획 검사와 역할별 도구 호출."""

from __future__ import annotations

import json
import sys
import tempfile
import threading
import unittest
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from scripted_product_llm import (  # noqa: E402
    DONE_TEXT, EXPLORATION_TOOL, NAVIGATE_TOOL, PLAN_FORMAT, PLANNER_TOOL, WORKER_TOOL, PlanError, Script, load_plan, make_server,
)

ORIGIN = "http://vampi.eval.test:8080"


def plan(**changes) -> dict:
    value = {"format": PLAN_FORMAT, "model": "scripted/product:v1", "timeout_ms": 60000,
             "exploration": [{"navigate": "{origin}/"}, {"bash": "curl -s {origin}/a"}],
             "intents": ["Check {origin}/b", "Check {origin}/c"], "commands": ["curl -s {origin}/b", "curl -s {origin}/c"]}
    value.update(changes)
    return value


def tool(name: str, properties: dict | None = None) -> dict:
    return {"type": "function", "function": {"name": name, "parameters": {"type": "object", "properties": properties or {}}}}


def body(*tools: dict, results: int = 0, last_tool: bool | None = None) -> dict:
    messages = [{"role": "system", "content": "x"}, {"role": "user", "content": "go"}]
    for _ in range(results):
        messages += [{"role": "assistant", "content": None}, {"role": "tool", "content": "r"}]
    if last_tool is False:
        messages.append({"role": "user", "content": "next round"})
    return {"messages": messages, "tools": list(tools)}


class TestPlan(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)

    def tearDown(self):
        self._tmp.cleanup()

    def write(self, value) -> Path:
        path = self.root / "plan.json"
        path.write_text(json.dumps(value), encoding="utf-8")
        return path

    def test_origin_is_filled_into_every_item(self):
        loaded = load_plan(self.write(plan()), ORIGIN)
        self.assertEqual(loaded["intents"], [f"Check {ORIGIN}/b", f"Check {ORIGIN}/c"])
        self.assertEqual(loaded["commands"], [f"curl -s {ORIGIN}/b", f"curl -s {ORIGIN}/c"])
        self.assertEqual(loaded["exploration"], [{"navigate": f"{ORIGIN}/"}, {"bash": f"curl -s {ORIGIN}/a"}])
        self.assertNotIn("{origin}", json.dumps(loaded))

    def test_closed_plan_shape(self):
        bad = [plan(extra=1), {k: v for k, v in plan().items() if k != "commands"}, plan(format="v0"), plan(model=""),
               plan(intents=[]), plan(commands=[]), plan(commands=[""]), plan(commands=["a\x00b"]), plan(intents=[1]),
               plan(exploration=[{"click": "x"}]), plan(exploration=[{"navigate": "a", "bash": "b"}]), plan(exploration=[{}]),
               plan(exploration={"navigate": "a"}), plan(timeout_ms=0), plan(timeout_ms=600_001), plan(timeout_ms=True),
               plan(commands=["c"] * 65), [plan()]]
        for value in bad:
            with self.subTest(value=str(value)[:60]), self.assertRaisesRegex(PlanError, "plan_invalid"):
                load_plan(self.write(value), ORIGIN)
        for origin in ("ftp://x", "http://X.test", "http://a.test/path", "http://a.test:8080?x", ""):
            with self.subTest(origin=origin), self.assertRaisesRegex(PlanError, "origin_invalid"):
                load_plan(self.write(plan()), origin)
        (self.root / "big.json").write_bytes(b" " * (256 * 1024 + 1))
        with self.assertRaisesRegex(PlanError, "plan_invalid"):
            load_plan(self.root / "big.json", ORIGIN)
        with self.assertRaisesRegex(PlanError, "plan_unreadable"):
            load_plan(self.root / "missing.json", ORIGIN)


class TestRoles(unittest.TestCase):
    def script(self, **changes) -> Script:
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "p.json"
            path.write_text(json.dumps(plan(**changes)), encoding="utf-8")
            return Script(load_plan(path, ORIGIN))

    def test_exploration_follows_the_conversation_and_skips_missing_tools(self):
        script = self.script()
        tools = (tool(EXPLORATION_TOOL), tool(NAVIGATE_TOOL), tool(WORKER_TOOL, {"command": {}, "timeout_ms": {}}))
        self.assertEqual(script.decide(body(*tools)), (NAVIGATE_TOOL, {"url": f"{ORIGIN}/"}))
        self.assertEqual(script.decide(body(*tools, results=1)),
                         (WORKER_TOOL, {"command": f"curl -s {ORIGIN}/a", "timeout_ms": 60000}))
        self.assertIsNone(script.decide(body(*tools, results=2)))
        # 브라우저 도구가 없는 탐색(직접 Bash만)은 이동 단계를 건너뛴다
        only_bash = (tool(EXPLORATION_TOOL), tool(WORKER_TOOL, {"command": {}}))
        self.assertEqual(script.decide(body(*only_bash)), (WORKER_TOOL, {"command": f"curl -s {ORIGIN}/a"}))
        self.assertIsNone(script.decide(body(*only_bash, results=1)))
        # 마무리 턴(report_discovery만)은 끝낸다
        self.assertIsNone(script.decide(body(tool(EXPLORATION_TOOL))))

    def test_planner_sends_each_intent_once_in_the_schema_shape(self):
        script = self.script()
        product = tool(PLANNER_TOOL, {"intents": {}})
        self.assertEqual(script.decide(body(product)),
                         (PLANNER_TOOL, {"intents": [{"summary": f"Check {ORIGIN}/b", "priority": 5}]}))
        self.assertIsNone(script.decide(body(product, results=1)))  # 도구 결과 뒤에는 이번 라운드를 끝낸다
        original = tool(PLANNER_TOOL, {"summary": {}, "priority": {}})
        self.assertEqual(script.decide(body(original, results=1, last_tool=False)),
                         (PLANNER_TOOL, {"summary": f"Check {ORIGIN}/c", "priority": 5}))
        self.assertIsNone(script.decide(body(product)))  # 다 냈으면 같은 의도를 되풀이하지 않는다

    def test_worker_sends_each_command_once_with_schema_fields_only(self):
        script = self.script()
        bash = tool(WORKER_TOOL, {"command": {}, "description": {}, "timeout_ms": {}})
        self.assertEqual(script.decide(body(bash)),
                         (WORKER_TOOL, {"command": f"curl -s {ORIGIN}/b", "description": "scripted", "timeout_ms": 60000}))
        self.assertEqual(script.decide(body(tool(WORKER_TOOL, {"command": {}}), results=1)),
                         (WORKER_TOOL, {"command": f"curl -s {ORIGIN}/c"}))
        self.assertIsNone(script.decide(body(bash, results=2)))
        # Bash와 add_intent가 같이 있으면 Planner가 아니라 Worker다(원본 루프 규칙)
        self.assertIsNone(script.decide(body(bash, tool(PLANNER_TOOL))))

    def test_other_requests_end_with_the_fixed_text(self):
        script = self.script()
        self.assertIsNone(script.decide(body(tool("set_goals"))))
        self.assertIsNone(script.decide({"messages": "x", "tools": "y"}))
        self.assertEqual(script.counts["requests"], 2)
        self.assertEqual(script.counts["done"], 2)


class TestServer(unittest.TestCase):
    def test_json_and_stream_replies(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "p.json"
            path.write_text(json.dumps(plan()), encoding="utf-8")
            server = make_server("127.0.0.1", 0, Script(load_plan(path, ORIGIN)))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            url = f"http://127.0.0.1:{server.server_address[1]}/v1/chat/completions"

            def post(payload: dict) -> bytes:
                request = urllib.request.Request(url, data=json.dumps(payload).encode(), method="POST",
                                                 headers={"Content-Type": "application/json"})
                with urllib.request.urlopen(request, timeout=5) as response:
                    return response.read()
            reply = json.loads(post(body(tool(WORKER_TOOL, {"command": {}}))))
            call = reply["choices"][0]["message"]["tool_calls"][0]["function"]
            self.assertEqual((call["name"], json.loads(call["arguments"])), (WORKER_TOOL, {"command": f"curl -s {ORIGIN}/b"}))
            self.assertEqual(reply["model"], "scripted/product:v1")
            streamed = post(dict(body(tool("x")), stream=True)).decode()
            self.assertIn(DONE_TEXT, streamed)
            self.assertTrue(streamed.rstrip().endswith("data: [DONE]"))
            bad = urllib.request.Request(url, data=b"[1]", method="POST")
            with self.assertRaises(urllib.error.HTTPError) as caught:
                urllib.request.urlopen(bad, timeout=5)
            self.assertEqual(caught.exception.code, 400)
        finally:
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    unittest.main()
