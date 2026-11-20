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
    DONE_TEXT, EXPLORATION_TOOL, FACT_TOOL, FACTS_TOOL, GOALS_TOOL, NAVIGATE_TOOL, PLAN_FORMAT, PLANNER_TOOL, PROVE_REASON,
    PROVE_TOOL, WORKER_TOOL, PlanError, Script, fact_ids, load_plan, make_server, open_goal_ids,
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
               plan(commands=["c"] * 65), [plan()], plan(finish="goal_met"), plan(finish=True)]
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


def conversation(*tools: dict, turns=(), user: str = "go", tail_user: bool = False, same_ids: bool = False) -> dict:
    """turns: (도구 이름, 결과 글) 순서. 모델이 부른 도구와 그 결과를 호출 ID로 짝지어 넣는다. same_ids면 이 서버처럼 모든
    호출 ID가 같다."""
    messages = [{"role": "system", "content": "x"}, {"role": "user", "content": user}]
    for index, (name, result) in enumerate(turns):
        call_id = "call_scripted" if same_ids else f"call_{index}"
        messages.append({"role": "assistant", "content": None,
                         "tool_calls": [{"id": call_id, "type": "function", "function": {"name": name, "arguments": "{}"}}]})
        messages.append({"role": "tool", "tool_call_id": call_id, "content": result})
    if tail_user:
        messages.append({"role": "user", "content": "next round"})
    return {"messages": messages, "tools": list(tools)}


OK_BASH = json.dumps({"tool_output": "status=200", "trusted": {"classification": "completed", "exit_code": 0, "final": True}})
GOALS = json.dumps([{"id": 3, "kind": "goal", "state": "open"}, {"id": 4, "kind": "goal", "state": "met"},
                    {"id": 5, "kind": "goal", "state": "open"}])
FACTS = json.dumps({"facts": [{"id": 9, "summary": "s"}, {"id": 8, "summary": "t"}], "total": 2, "has_more": False})


class TestFinishWithGoals(unittest.TestCase):
    """계획 finish=prove_goals: 의도를 다 낸 뒤 목표 달성 선언 도구(C26)로 끝낸다(빈 계획 마감 규칙이 빠져도 오래 기다리지 않게)."""

    PLANNER = (tool(PLANNER_TOOL, {"intents": {}}), tool(GOALS_TOOL), tool(FACTS_TOOL, {"limit": {}, "before": {}, "q": {}}),
               tool(PROVE_TOOL, {"goal_id": {}, "evidence_id": {}, "reason": {}}))

    def script(self, **changes) -> Script:
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "p.json"
            path.write_text(json.dumps(plan(finish="prove_goals", **changes)), encoding="utf-8")
            loaded = load_plan(path, ORIGIN)
        script = Script(loaded)
        script.intents_sent = len(loaded["intents"])  # 의도는 다 냈다
        script.command_results = {("given", index): (command, True) for index, command in enumerate(loaded["commands"])}  # 모두 완료
        return script

    def test_planner_proves_each_open_goal_once_then_stops(self):
        script = self.script()
        self.assertEqual(script.decide(conversation(*self.PLANNER, tail_user=True)), (GOALS_TOOL, {}))
        self.assertEqual(script.decide(conversation(*self.PLANNER, turns=[(GOALS_TOOL, GOALS)])), (FACTS_TOOL, {"limit": 1}))
        self.assertEqual(script.decide(conversation(*self.PLANNER, turns=[(GOALS_TOOL, GOALS), (FACTS_TOOL, FACTS)])),
                         (PROVE_TOOL, {"goal_id": 3, "evidence_id": 9, "reason": PROVE_REASON}))
        done = [(GOALS_TOOL, GOALS), (FACTS_TOOL, FACTS), (PROVE_TOOL, "goal 3 marked met")]
        self.assertEqual(script.decide(conversation(*self.PLANNER, turns=done)), (GOALS_TOOL, {}))
        # 목표 3은 이미 증명을 시도했다: 아직 열려 있어도 다시 하지 않고 남은 목표 5로 간다
        again = done + [(GOALS_TOOL, GOALS), (FACTS_TOOL, FACTS)]
        self.assertEqual(script.decide(conversation(*self.PLANNER, turns=again))[1]["goal_id"], 5)
        met = json.dumps([{"id": 3, "state": "met"}, {"id": 5, "state": "met"}])
        self.assertIsNone(script.decide(conversation(*self.PLANNER, turns=[(GOALS_TOOL, met)])))  # 열린 목표 없음
        self.assertEqual(script.counts["finish"], 5)

    def test_results_are_paired_by_position_when_call_ids_repeat(self):
        # 2026-11-19 실제 제품 합성 회차: 이 서버는 호출 ID를 늘 call_scripted로 내서, ID로 찾으면 list_goals 결과 자리에
        # list_facts 결과가 잡혀 열린 목표가 0개로 읽혔다(prove_goal을 부르지 못함).
        script = self.script()
        turns = [(GOALS_TOOL, GOALS), (FACTS_TOOL, FACTS)]
        self.assertEqual(script.decide(conversation(*self.PLANNER, turns=turns, same_ids=True)),
                         (PROVE_TOOL, {"goal_id": 3, "evidence_id": 9, "reason": PROVE_REASON}))
        # 같은 도구를 두 번 불렀으면 가장 최근 결과를 쓴다: 두 번째 list_goals에서 3은 met, 5만 열려 있다
        later = json.dumps([{"id": 3, "state": "met"}, {"id": 5, "state": "open"}])
        turns = [(GOALS_TOOL, GOALS), (FACTS_TOOL, FACTS), (PROVE_TOOL, "ok"), (GOALS_TOOL, later), (FACTS_TOOL, FACTS)]
        self.assertEqual(self.script().decide(conversation(*self.PLANNER, turns=turns, same_ids=True))[1]["goal_id"], 5)

    def test_planner_stops_without_facts_tools_or_finish(self):
        script = self.script()
        self.assertIsNone(script.decide(conversation(*self.PLANNER, turns=[(GOALS_TOOL, GOALS), (FACTS_TOOL, '{"facts": []}')])))
        self.assertIsNone(script.decide(conversation(*self.PLANNER, turns=[(PLANNER_TOOL, "ok")])))  # 의도 결과 뒤는 라운드 끝
        self.assertIsNone(script.decide(conversation(tool(PLANNER_TOOL), tool(GOALS_TOOL), tail_user=True)))  # prove_goal 없음
        plain = Script(dict(script.plan, finish=False))
        plain.intents_sent = len(plain.plan["intents"])
        plain.command_results = {("given", index): (command, True) for index, command in enumerate(plain.plan["commands"])}
        self.assertIsNone(plain.decide(conversation(*self.PLANNER, tail_user=True)))  # finish 없는 계획은 지금처럼

    def test_worker_records_one_fact_for_its_intent_after_the_commands(self):
        script = self.script()
        script.commands_sent = len(script.plan["commands"])
        worker = (tool(WORKER_TOOL, {"command": {}}), tool(FACT_TOOL, {"intent_id": {}, "summary": {}, "confidence": {}}))
        message = "go\n\n[Your intent]:\n{}\nIntent id: 12 (pass it to record_fact / report_finding when writing back)"
        ran = [(WORKER_TOOL, OK_BASH), (WORKER_TOOL, OK_BASH)]
        self.assertEqual(script.decide(conversation(*worker, user=message, turns=ran)),
                         (FACT_TOOL, {"intent_id": 12, "summary": "합성 회차 고정 관찰: 이 의도의 계획 명령이 모두 완료됨",
                                      "confidence": "observed"}))
        self.assertIsNone(script.decide(conversation(*worker, user=message, turns=ran + [(FACT_TOOL, "ok")])))  # 대화마다 한 번
        self.assertIsNone(script.decide(conversation(*worker, user="no id here", turns=ran)))
        self.assertIsNone(script.decide(conversation(tool(WORKER_TOOL, {"command": {}}), user=message, turns=ran)))  # record_fact 없음

    def test_worker_records_nothing_unless_its_bash_calls_completed(self):
        # #797 리뷰: E② 거부·실패·불명이나 명령을 하나도 안 낸 Worker가 '완료' 사실을 남기면 실패 회차가 목표 달성으로 끝난다.
        script = self.script()
        script.commands_sent = len(script.plan["commands"])
        worker = (tool(WORKER_TOOL, {"command": {}}), tool(FACT_TOOL, {"intent_id": {}, "summary": {}}))
        message = "Intent id: 12 (pass it as intent_id to record_fact)."
        bad = {
            "denied_text": "error: request denied",
            "policy": json.dumps({"tool_output": "x", "trusted": {"classification": "policy_not_executed", "exit_code": 0}}),
            "exit": json.dumps({"tool_output": "x", "trusted": {"classification": "completed", "exit_code": 7}}),
            "unknown": json.dumps({"tool_output": "x", "trusted": {"classification": "result_unknown", "exit_code": 0}}),
            "exit_text": json.dumps({"tool_output": "x", "trusted": {"classification": "completed", "exit_code": "0"}}),
            "no_trusted": json.dumps({"tool_output": "completed"}),
        }
        for name, result in bad.items():
            with self.subTest(name=name):
                self.assertIsNone(script.decide(conversation(*worker, user=message, turns=[(WORKER_TOOL, OK_BASH), (WORKER_TOOL, result)])))
        # 다른 의도가 명령을 다 가져가 이 대화에서 Bash를 하나도 안 냈다
        self.assertIsNone(script.decide(conversation(*worker, user=message)))

    def test_goals_stay_open_unless_every_plan_command_completed(self):
        # #797 리뷰: Worker A 성공·Worker B 거부인 혼합 회차는 A의 사실 하나로 목표를 증명하면 안 된다.
        script = self.script()
        script.command_results = {}
        commands = script.plan["commands"]
        worker = (tool(WORKER_TOOL, {"command": {}}), tool(FACT_TOOL, {"intent_id": {}, "summary": {}}))
        denied = json.dumps({"tool_output": "x", "trusted": {"classification": "policy_not_executed", "exit_code": 0}})

        def bash_turns(*pairs):
            body = conversation(*worker, user="Intent id: 12", turns=[(WORKER_TOOL, result) for _, result in pairs])
            for message, (command, _) in zip([m for m in body["messages"] if m.get("tool_calls")], pairs):
                message["tool_calls"][0]["function"]["arguments"] = json.dumps({"command": command})
            return body

        script.decide(bash_turns((commands[0], OK_BASH)))   # Worker A: 성공(사실을 남긴다)
        script.decide(bash_turns((commands[1], denied)))    # Worker B: 거부
        finishing = conversation(*self.PLANNER, turns=[(GOALS_TOOL, GOALS), (FACTS_TOOL, FACTS)])
        self.assertIsNone(script.decide(finishing))  # 사실이 있어도 증명하지 않는다
        self.assertIsNone(script.decide(conversation(*self.PLANNER, tail_user=True)))  # 목표 조회도 시작하지 않는다
        # 아직 다 실행되지 않았을 때도 같다
        partial = self.script()
        partial.command_results = {("given", 0): (commands[0], True)}
        self.assertIsNone(partial.decide(conversation(*self.PLANNER, tail_user=True)))
        # 둘 다 완료되면 증명한다(Worker 대화에서 본 결과로 기억한다)
        done = self.script()
        done.command_results = {}
        done.decide(bash_turns((commands[0], OK_BASH), (commands[1], OK_BASH)))
        self.assertEqual(sorted(command for command, ok in done.command_results.values() if ok), sorted(commands))
        self.assertEqual(done.decide(finishing)[0], PROVE_TOOL)
        # 같은 명령이 계획에 두 번 있어 한 번은 성공·한 번은 거부면 증명하지 않는다(실패가 하나라도 있으면 open)
        twice = self.script()
        twice.command_results = {}
        twice.decide(bash_turns((commands[0], OK_BASH), (commands[1], OK_BASH), (commands[0], denied)))
        self.assertIsNone(twice.decide(finishing))
        # 결과가 아직 없는 호출은 실패로 세지 않는다(뒤에 완료 결과가 오면 증명한다)
        pending = self.script()
        pending.command_results = {}
        body = bash_turns((commands[0], OK_BASH), (commands[1], OK_BASH))
        cut = body["messages"][:-1] + [{"role": "user", "content": "still running"}]  # 마지막 Bash 결과가 아직 없다
        pending.decide(dict(body, messages=cut))
        self.assertEqual(pending.failed_commands, set())
        pending.decide(body)
        self.assertEqual(pending.decide(finishing)[0], PROVE_TOOL)

    def test_duplicate_plan_command_needs_each_call_completed(self):
        # #797 2차 리뷰: 계획에 같은 명령이 두 번이면 호출도 둘이다. 한 호출의 완료로 두 항목을 채우면 안 된다.
        same = "echo same"
        script = self.script(commands=[same, same])
        script.command_results = {}
        worker = (tool(WORKER_TOOL, {"command": {}}), tool(FACT_TOOL, {"intent_id": {}, "summary": {}}))
        denied = json.dumps({"tool_output": "x", "trusted": {"classification": "policy_not_executed", "exit_code": 0}})

        def worker_turn(intent: int, *results):
            body = conversation(*worker, user=f"Intent id: {intent}", turns=[(WORKER_TOOL, result) for result in results])
            for message in body["messages"]:
                if message.get("tool_calls"):
                    message["tool_calls"][0]["function"]["arguments"] = json.dumps({"command": same})
            return body

        finishing = conversation(*self.PLANNER, turns=[(GOALS_TOOL, GOALS), (FACTS_TOOL, FACTS)])
        a_done = worker_turn(12, OK_BASH)
        b_waiting = worker_turn(13, OK_BASH)
        b_waiting["messages"] = b_waiting["messages"][:-1] + [{"role": "user", "content": "still running"}]  # B 결과 대기
        script.decide(a_done)
        script.decide(b_waiting)
        script.decide(a_done)  # 같은 대화를 다시 보내도 한 호출로 센다
        a_later = dict(a_done, messages=a_done["messages"] + [  # 그 대화가 이어져(사실 기록) 길어져도 같은 호출이다
            {"role": "assistant", "content": None, "tool_calls": [{"id": "call_scripted", "type": "function",
                                                                   "function": {"name": FACT_TOOL, "arguments": "{}"}}]},
            {"role": "tool", "tool_call_id": "call_scripted", "content": "{}"}])
        script.decide(a_later)
        self.assertFalse(script.plan_commands_completed())
        self.assertIsNone(script.decide(finishing))  # 동찬님 반례: A 완료·B 대기면 open
        script.decide(worker_turn(13, OK_BASH))  # B도 완료
        self.assertTrue(script.plan_commands_completed())
        self.assertEqual(script.decide(finishing)[0], PROVE_TOOL)
        # 한 대화 안에서 같은 명령을 두 번 냈고 둘째가 거부면 open
        one = self.script(commands=[same, same])
        one.command_results = {}
        one.decide(worker_turn(12, OK_BASH, denied))
        self.assertIsNone(one.decide(finishing))
        # 두 대화에서 하나는 완료·하나는 거부면 open
        mixed = self.script(commands=[same, same])
        mixed.command_results = {}
        mixed.decide(worker_turn(12, OK_BASH))
        mixed.decide(worker_turn(13, denied))
        self.assertIsNone(mixed.decide(finishing))

    def test_result_parsers(self):
        self.assertEqual(open_goal_ids(GOALS), [3, 5])
        self.assertEqual(open_goal_ids(json.dumps({"goals": [{"id": "7", "state": "open"}, {"id": 0, "state": "open"}]})), [7])
        self.assertEqual(open_goal_ids("not json"), [])
        self.assertEqual(fact_ids(FACTS), [9, 8])
        self.assertEqual(fact_ids(json.dumps([{"id": 1}])), [])


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


class TestPlannerError(unittest.TestCase):
    """계획 planner_error_status: 진단 Planner 요청에만 제공자 오류 상태를 돌려준다(고정 초안 §26 합성 확인)."""

    def load(self, **changes):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "p.json"
            path.write_text(json.dumps(plan(**changes)), encoding="utf-8")
            return load_plan(path, ORIGIN)

    def test_plan_field(self):
        self.assertIsNone(self.load()["planner_error_status"])
        for status in (429, 500, 502, 503):
            self.assertEqual(self.load(planner_error_status=status)["planner_error_status"], status)
        for bad in (200, 401, 504, "503", True, None):
            with self.subTest(bad=bad), self.assertRaises(PlanError):
                self.load(planner_error_status=bad)

    def test_only_planner_requests_get_the_error(self):
        script = Script(self.load(planner_error_status=503))
        self.assertEqual(script.planner_error(body(tool(PLANNER_TOOL))), 503)
        self.assertIsNone(script.planner_error(body(tool(WORKER_TOOL, {"command": {}}))))
        self.assertIsNone(script.planner_error(body(tool(PLANNER_TOOL), tool(WORKER_TOOL))))
        self.assertIsNone(script.planner_error(body(tool(EXPLORATION_TOOL), tool(PLANNER_TOOL))))
        self.assertIsNone(script.planner_error(body(tool("x"))))
        self.assertEqual(script.counts["provider_error"], 1)
        self.assertIsNone(Script(self.load()).planner_error(body(tool(PLANNER_TOOL))))

    def test_server_replies_with_the_status(self):
        server = make_server("127.0.0.1", 0, Script(self.load(planner_error_status=502)))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            url = f"http://127.0.0.1:{server.server_address[1]}/v1/chat/completions"
            request = urllib.request.Request(url, data=json.dumps(body(tool(PLANNER_TOOL))).encode(), method="POST",
                                             headers={"Content-Type": "application/json"})
            with self.assertRaises(urllib.error.HTTPError) as caught:
                urllib.request.urlopen(request, timeout=5)
            self.assertEqual(caught.exception.code, 502)
            self.assertEqual(json.loads(caught.exception.read())["error"]["code"], 502)
            worker = urllib.request.Request(url, data=json.dumps(body(tool(WORKER_TOOL, {"command": {}}))).encode(), method="POST",
                                            headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(worker, timeout=5) as response:
                self.assertEqual(response.status, 200)
        finally:
            server.shutdown()
            server.server_close()

if __name__ == "__main__":
    unittest.main()
