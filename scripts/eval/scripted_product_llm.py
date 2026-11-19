"""분리 배치 O 합성 모델(OpenAI 호환). 제품의 로컬 모델 설치(local_model)에 붙어, 계획 파일의 요청을 도구 호출로 돌려준다.

평가 overlay(deploy/eval/compose.split-synthetic.yaml)에서만 띄운다. 제품 설치 화면·기본 설정에는 없다. API 키 없이 제품
흐름(탐색 → 진단 계획 → Worker → E②)을 끝까지 돌려 러너·계측기·경계 원자료 배선을 확인하는 용도다.

- **판단하는 모델이 아니다.** 요청 수·발견 수를 효과 수치로 쓰지 않는다. 이 회차 기록은 `execution_source=synthetic_fixture`로
  서명되고 채점기·비교 표·보존율 계산이 모두 거부한다.
- 대상 앱의 경로·계정은 이 코드에 없다. 계획 파일(리뷰받은 고정 목록)이 담고, 대상 origin은 `--origin`으로 받아 `{origin}`
  자리에 넣는다. 평가 정답(챌린지 목록)은 계획에도 넣지 않는다.
- 요청 본문을 저장하지 않는다. 외부로 보내는 요청은 없다(이 서버는 받기만 한다).

역할은 요청에 실린 도구 이름으로 가른다(제품 도구 이름, agent/exploration.go·진단 Planner·Worker):
- 탐색(`report_discovery`가 있음): 계획 `exploration`의 단계를 그 대화의 도구 결과 수만큼 진행한다. 단계는 브라우저 이동
  (`navigate`, 도구 `mcp__explore_browser__browser_navigate`) 또는 직접 Bash(`bash`)다. 그 도구가 없으면 그 단계를 건너뛴다.
- 진단 Planner(`add_intent`가 있고 `Bash`는 없음): 마지막 메시지가 도구 결과가 아니면 다음 의도 하나를 낸다. 의도는 프로세스
  전체에서 한 번씩만 낸다(계획 라운드가 몇 번이든 같은 의도를 되풀이하지 않는다).
- Worker(`Bash`가 있고 `add_intent`·`report_discovery`는 없음): 계획 `commands`를 프로세스 전체 순서로 하나씩 낸다.
- 그 밖(목표 분해·압축·마무리 턴 등): 고정 문장으로 끝낸다. 목표 분해가 0개면 제품은 원래 목표로 계속한다(C11).

계획 `finish: "prove_goals"`(선택)면 목표 달성 선언 도구(C26)로 끝을 낸다. 제품이 빈 계획에서 바로 마감하지 않고 원본처럼
알림·심장박동 때 다시 계획하게 되면(마감 규칙 변경), 스크립트 회차가 빈 계획 뒤 오래 기다리지 않게 하려는 것이다.
- Worker: 계획 명령을 다 낸 뒤 그 대화에서 한 번 `record_fact`로 고정 관찰 사실 하나를 남긴다(의도 번호는 Worker 메시지의
  `Intent id: N`에서 읽는다. 제품은 다른 의도 번호의 사실을 거부한다). 이 대화에서 Bash를 한 번 이상 불렀고 그 결과가 모두
  E② 신뢰 결과 `classification: completed`·`exit_code: 0`일 때만 남긴다. 거부·실패·불명이나 명령을 하나도 안 낸 Worker는
  사실을 남기지 않아, 실패한 회차가 목표 달성으로 끝나지 않는다(#797 리뷰).
- Planner: 의도를 다 낸 뒤 `list_goals` → 열린 목표가 있으면 `list_facts` → 사실이 있으면 열린 목표 하나에 `prove_goal` →
  다시 `list_goals`를 되풀이한다. 목표마다 한 번만 증명을 시도한다. 열린 목표나 사실이 없으면 이번 라운드를 끝낸다.
  계획의 명령 항목마다 완료된 호출이 있고(위 Worker 기준, 같은 명령이 두 번이면 호출 둘 다) 실패한 호출이 없을 때만 이 순서를
  시작한다. 호출은 (대화, 그 대화 안 Bash 순번)으로 가린다. 하나라도 거부·실패·대기면 목표를 open으로 남겨 회차가 상한까지 가게
  한다.
  모든 목표가 met가 되면 엔진이 작업을 자동 완료한다(`agent/tools.go` prove_goal).

실행: python3 -I -B scripts/eval/scripted_product_llm.py --host <주소> --port <포트> --plan <계획 JSON> --origin <대상 origin>
종료 코드: 2 계획·인자 오류.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
import threading
from collections import Counter
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

PLAN_FORMAT = "abc2lab.scripted-product-plan/v1"
PLAN_FIELDS = frozenset({"format", "model", "exploration", "intents", "commands", "timeout_ms"})
PLAN_OPTIONAL = frozenset({"finish"})
FINISH_PROVE_GOALS = "prove_goals"
STEP_KINDS = frozenset({"navigate", "bash"})
EXPLORATION_TOOL = "report_discovery"
NAVIGATE_TOOL = "mcp__explore_browser__browser_navigate"
PLANNER_TOOL = "add_intent"
WORKER_TOOL = "Bash"
GOALS_TOOL, FACTS_TOOL, PROVE_TOOL, FACT_TOOL = "list_goals", "list_facts", "prove_goal", "record_fact"
FINISH_TOOLS = frozenset({GOALS_TOOL, FACTS_TOOL, PROVE_TOOL, FACT_TOOL})
FACT_FIELDS = {"summary": "합성 회차 고정 관찰: 이 의도의 계획 명령이 모두 완료됨", "detail": "scripted fixture (not a model judgment)",
               "evidence": "scripted fixture", "confidence": "observed"}
PROVE_REASON = "scripted fixture: plan finished"
_INTENT_ID = re.compile(r"Intent id: ([1-9][0-9]{0,18})\b")
DONE_TEXT = "No further action."
MAX_PLAN_BYTES = 256 * 1024
MAX_REQUEST_BYTES = 64 * 1024 * 1024
MAX_ITEMS = 64
MAX_TEXT = 4096
MAX_TIMEOUT_MS = 600_000
INTENT_PRIORITY = 5
ORIGIN_SLOT = "{origin}"
COMPLETION_ID = "gen-scripted-product"
USAGE = {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
_ORIGIN = re.compile(r"https?://[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?(?::[0-9]{1,5})?\Z")


class PlanError(ValueError):
    """계획 파일·인자가 형식에 맞지 않는다. 고정 code만 담는다."""


def _text(value: object) -> bool:
    return isinstance(value, str) and 0 < len(value) <= MAX_TEXT and "\x00" not in value


def load_plan(path: str | Path, origin: str) -> dict:
    """계획을 읽고 `{origin}` 자리를 채운다. 칸·모양이 다르면 PlanError."""
    if not isinstance(origin, str) or not _ORIGIN.fullmatch(origin):
        raise PlanError("origin_invalid")
    try:
        data = Path(path).read_bytes()
    except OSError:
        raise PlanError("plan_unreadable") from None
    try:
        if len(data) > MAX_PLAN_BYTES:
            raise ValueError
        plan = json.loads(data.decode("utf-8"))
    except (UnicodeError, ValueError, RecursionError):
        raise PlanError("plan_invalid") from None
    if (not isinstance(plan, dict) or not PLAN_FIELDS <= set(plan) <= PLAN_FIELDS | PLAN_OPTIONAL
            or plan["format"] != PLAN_FORMAT or not _text(plan["model"]) or plan.get("finish", FINISH_PROVE_GOALS) != FINISH_PROVE_GOALS):
        raise PlanError("plan_invalid")
    steps, intents, commands = plan["exploration"], plan["intents"], plan["commands"]
    if (not isinstance(steps, list) or not isinstance(intents, list) or not isinstance(commands, list)
            or not intents or not commands or any(len(items) > MAX_ITEMS for items in (steps, intents, commands))
            or not all(isinstance(step, dict) and len(step) == 1 and set(step) <= STEP_KINDS and _text(next(iter(step.values())))
                       for step in steps)
            or not all(_text(item) for item in intents + commands)
            or type(plan["timeout_ms"]) is not int or not 1 <= plan["timeout_ms"] <= MAX_TIMEOUT_MS):
        raise PlanError("plan_invalid")

    def fill(value: str) -> str:
        return value.replace(ORIGIN_SLOT, origin)
    return {"model": plan["model"], "timeout_ms": plan["timeout_ms"], "finish": "finish" in plan, "intents": [fill(i) for i in intents],
            "commands": [fill(c) for c in commands],
            "exploration": [{kind: fill(value) for kind, value in step.items()} for step in steps]}


def _tools(body: dict) -> dict[str, dict]:
    """요청의 도구 이름 → 매개변수 스키마."""
    found = {}
    for tool in body.get("tools") or []:
        function = tool.get("function") if isinstance(tool, dict) else None
        if isinstance(function, dict) and isinstance(function.get("name"), str):
            found[function["name"]] = function.get("parameters") if isinstance(function.get("parameters"), dict) else {}
    return found


def _messages(body: dict) -> list[dict]:
    return [m for m in body.get("messages") or [] if isinstance(m, dict)]


def _properties(schema: dict) -> dict:
    properties = schema.get("properties")
    return properties if isinstance(properties, dict) else {}


def _content(message: dict) -> str:
    content = message.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(part.get("text", "") for part in content if isinstance(part, dict) and isinstance(part.get("text"), str))
    return ""


def _calls(messages: list[dict]) -> list[tuple[str, str]]:
    """이 대화에서 모델이 부른 도구 (호출 ID, 이름), 순서대로."""
    found = []
    for message in messages:
        if message.get("role") != "assistant":
            continue
        for call in message.get("tool_calls") or []:
            function = call.get("function") if isinstance(call, dict) else None
            if isinstance(function, dict) and isinstance(function.get("name"), str):
                found.append((call.get("id") if isinstance(call.get("id"), str) else "", function["name"]))
    return found


def _last_result(messages: list[dict], name: str) -> str | None:
    """그 이름의 가장 최근 도구 호출에 대한 결과 글. 호출 바로 뒤의 도구 결과로 짝짓는다. 이 서버는 호출 ID를 늘 같은 값으로
    내므로(completion) ID로 찾으면 다른 도구의 결과가 잡힌다(2026-11-19 실제 제품 합성 회차)."""
    found = None
    for index, message in enumerate(messages):
        if message.get("role") != "assistant":
            continue
        names = [call.get("function", {}).get("name") for call in message.get("tool_calls") or [] if isinstance(call, dict)]
        if name in names:
            following = messages[index + 1] if index + 1 < len(messages) else None
            found = _content(following) if isinstance(following, dict) and following.get("role") == "tool" else None
    return found


def _json(text: str | None) -> object:
    try:
        return json.loads(text) if text else None
    except (ValueError, RecursionError):
        return None


def _ids(items: object, state: str | None = None) -> list[int]:
    out = []
    for item in items if isinstance(items, list) else []:
        if not isinstance(item, dict) or (state is not None and item.get("state") != state):
            continue
        value = item.get("id")
        if type(value) is int and value > 0:
            out.append(value)
        elif isinstance(value, str) and value.isdigit() and int(value) > 0:
            out.append(int(value))
    return out


def bash_outcomes(messages: list[dict]) -> list[tuple[str | None, bool | None]]:
    """이 대화의 Bash 호출마다 (명령, 성공). 성공은 호출 바로 뒤 도구 결과가 E② 신뢰 결과로 완료·종료 코드 0인지이고, 결과가
    아직 없으면 None이다."""
    outcomes = []
    for index, message in enumerate(messages):
        if message.get("role") != "assistant":
            continue
        for call in message.get("tool_calls") or []:
            function = call.get("function") if isinstance(call, dict) else None
            if not isinstance(function, dict) or function.get("name") != WORKER_TOOL:
                continue
            arguments = _json(function.get("arguments")) if isinstance(function.get("arguments"), str) else None
            command = arguments.get("command") if isinstance(arguments, dict) and isinstance(arguments.get("command"), str) else None
            following = messages[index + 1] if index + 1 < len(messages) else None
            if not isinstance(following, dict) or following.get("role") != "tool":
                outcomes.append((command, None))
                continue
            value = _json(_content(following))
            trusted = value.get("trusted") if isinstance(value, dict) else None
            outcomes.append((command, isinstance(trusted, dict) and trusted.get("classification") == "completed"
                             and type(trusted.get("exit_code")) is int and trusted["exit_code"] == 0))
    return outcomes


def conversation_key(messages: list[dict]) -> str:
    """대화를 가리는 값: 첫 모델 응답 앞의 메시지(system·첫 user, 의도 번호가 든 곳)의 지문. 같은 대화는 요청마다 이 앞부분이
    같다. 다른 대화가 같은 값을 가지면(같은 의도를 다시 보냄) 호출이 덜 세어져 목표를 증명하지 않는 쪽으로만 틀린다."""
    head = []
    for message in messages:
        if message.get("role") == "assistant":
            break
        head.append(message)
    return hashlib.sha256(json.dumps(head, sort_keys=True, ensure_ascii=False).encode("utf-8")).hexdigest()


def bash_succeeded(messages: list[dict]) -> bool:
    """이 대화의 Bash 호출이 하나 이상이고, 그 결과가 모두 E② 신뢰 결과로 완료·종료 코드 0인지."""
    outcomes = bash_outcomes(messages)
    return bool(outcomes) and all(ok is True for _, ok in outcomes)


def open_goal_ids(text: str | None) -> list[int]:
    """list_goals 결과(목표 노드 배열 또는 {goals: [...]})의 열린 목표 번호."""
    value = _json(text)
    return sorted(_ids(value.get("goals") if isinstance(value, dict) else value, "open"))


def fact_ids(text: str | None) -> list[int]:
    """list_facts 결과({facts: [...]})의 사실 번호."""
    value = _json(text)
    return _ids(value.get("facts") if isinstance(value, dict) else None)


class Script:
    """프로세스 전체 진행 상태(의도·명령 순번). 요청이 여러 스레드로 와도 한 번씩만 낸다."""

    def __init__(self, plan: dict):
        self.plan, self.lock = plan, threading.Lock()
        self.intents_sent = self.commands_sent = 0
        self.proved: set[int] = set()
        # 프로세스 전체의 계획 명령 결과(Worker 대화에서 본 것). 호출 하나를 (대화, 그 대화 안 Bash 순번)으로 가린다. Planner는
        # 계획의 명령 항목마다 완료된 호출이 있고 실패한 호출이 하나도 없을 때만 목표를 증명한다(#797 리뷰: 일부 Worker만 성공한
        # 회차나, 같은 명령이 계획에 두 번 있는데 한 번만 완료된 회차가 목표 달성으로 끝나 거부·대기 중인 요청을 가리지 않게).
        self.command_results: dict[tuple[str, int], tuple[str, bool]] = {}
        self.failed_commands: set[str] = set()  # 한 번이라도 실패하면 남는다(같은 자리를 다른 대화가 덮어도 지워지지 않게)
        self.counts = {"requests": 0, "exploration": 0, "planner": 0, "worker": 0, "finish": 0, "done": 0, "skipped": 0}

    def decide(self, body: dict) -> tuple[str, dict] | None:
        tools, messages = _tools(body), _messages(body)
        last_is_result = bool(messages) and messages[-1].get("role") == "tool"
        with self.lock:
            self.counts["requests"] += 1
            if EXPLORATION_TOOL in tools:
                call = self._exploration(tools, sum(1 for m in messages if m.get("role") == "tool"))
            elif PLANNER_TOOL in tools and WORKER_TOOL not in tools:
                if self.intents_sent < len(self.plan["intents"]):
                    call = None if last_is_result else self._planner(tools[PLANNER_TOOL])
                else:
                    call = self._finish_planner(tools, messages, last_is_result)
            elif WORKER_TOOL in tools:
                key = conversation_key(messages)
                for position, (command, ok) in enumerate(bash_outcomes(messages)):
                    if command is not None and ok is not None:
                        self.command_results[(key, position)] = (command, ok)
                        if not ok:
                            self.failed_commands.add(command)
                call = self._worker(tools[WORKER_TOOL])
                if call is None:
                    call = self._finish_worker(tools, messages)
            else:
                call = None
            self.counts["done" if call is None else ("exploration" if EXPLORATION_TOOL in tools else
                                                      "finish" if call[0] in FINISH_TOOLS else
                                                      "planner" if call[0] == PLANNER_TOOL else "worker")] += 1
            return call

    def plan_commands_completed(self) -> bool:
        """계획의 명령 항목마다(같은 명령이 두 번이면 두 번) 완료된 호출이 있고 실패한 호출이 없는지."""
        done = Counter(command for command, ok in self.command_results.values() if ok)
        return not self.failed_commands and all(done[command] >= need for command, need in Counter(self.plan["commands"]).items())

    def _finish_planner(self, tools: dict, messages: list[dict], last_is_result: bool) -> tuple[str, dict] | None:
        """의도를 다 낸 뒤 목표 달성 선언으로 끝낸다(계획 finish). 도구가 없거나 할 일이 없으면 이번 라운드를 끝낸다."""
        if not self.plan["finish"] or not {GOALS_TOOL, FACTS_TOOL, PROVE_TOOL} <= set(tools):
            return None
        if not self.plan_commands_completed():
            return None  # 계획 명령이 아직 다 끝나지 않았거나 실패가 있다: 목표를 open으로 둔다
        calls = _calls(messages)
        last = calls[-1][1] if last_is_result and calls else None
        if not last_is_result or last == PROVE_TOOL:
            return GOALS_TOOL, {}
        goals = [goal for goal in open_goal_ids(_last_result(messages, GOALS_TOOL)) if goal not in self.proved]
        if last == GOALS_TOOL:
            return (FACTS_TOOL, {"limit": 1} if "limit" in _properties(tools[FACTS_TOOL]) else {}) if goals else None
        if last == FACTS_TOOL:
            facts = fact_ids(_last_result(messages, FACTS_TOOL))
            if not goals or not facts:
                return None
            self.proved.add(goals[0])
            arguments = {"goal_id": goals[0], "evidence_id": facts[0]}
            if "reason" in _properties(tools[PROVE_TOOL]):
                arguments["reason"] = PROVE_REASON
            return PROVE_TOOL, arguments
        return None

    def _finish_worker(self, tools: dict, messages: list[dict]) -> tuple[str, dict] | None:
        """명령을 다 낸 Worker가 그 의도로 고정 관찰 사실 하나를 남긴다(대화마다 한 번)."""
        if (not self.plan["finish"] or FACT_TOOL not in tools or any(name == FACT_TOOL for _, name in _calls(messages))
                or not bash_succeeded(messages)):
            return None
        match = next((m for m in (_INTENT_ID.search(_content(message)) for message in messages
                                  if message.get("role") in ("system", "user")) if m), None)
        if match is None:
            return None
        properties = _properties(tools[FACT_TOOL])
        arguments = {"intent_id": int(match.group(1))}
        arguments.update({key: value for key, value in FACT_FIELDS.items() if key in properties})
        return FACT_TOOL, arguments

    def _exploration(self, tools: dict, results: int) -> tuple[str, dict] | None:
        # 그 대화의 도구 결과 수가 진행 위치다. 쓸 수 없는 단계(도구 없음)는 건너뛰고 센다.
        position = 0
        for step in self.plan["exploration"]:
            (kind, value), = step.items()
            tool = NAVIGATE_TOOL if kind == "navigate" else WORKER_TOOL
            if tool not in tools:
                continue
            if position == results:
                if kind == "navigate":
                    return NAVIGATE_TOOL, {"url": value}
                return WORKER_TOOL, self._bash_arguments(tools[WORKER_TOOL], value)
            position += 1
        return None

    def _planner(self, schema: dict) -> tuple[str, dict] | None:
        if self.intents_sent >= len(self.plan["intents"]):
            return None
        summary = self.plan["intents"][self.intents_sent]
        self.intents_sent += 1
        intent = {"summary": summary, "priority": INTENT_PRIORITY}
        # 제품 Planner는 의도 목록(`intents`)을 받고, 원본 도구는 의도 하나를 받는다. 스키마에 있는 모양으로 낸다.
        return PLANNER_TOOL, ({"intents": [intent]} if "intents" in _properties(schema) else intent)

    def _worker(self, schema: dict) -> tuple[str, dict] | None:
        if self.commands_sent >= len(self.plan["commands"]):
            return None
        command = self.plan["commands"][self.commands_sent]
        self.commands_sent += 1
        return WORKER_TOOL, self._bash_arguments(schema, command)

    def _bash_arguments(self, schema: dict, command: str) -> dict:
        """도구 스키마에 있는 칸만 보낸다(E② Bash는 모르는 칸을 입력 오류로 거절한다)."""
        properties = _properties(schema)
        arguments = {"command": command}
        if "description" in properties:
            arguments["description"] = "scripted"
        if "timeout_ms" in properties:
            arguments["timeout_ms"] = self.plan["timeout_ms"]
        return arguments


def completion(call: tuple[str, dict] | None, model: str) -> dict:
    if call is None:
        message: dict = {"role": "assistant", "content": DONE_TEXT}
    else:
        message = {"role": "assistant", "content": None, "tool_calls": [
            {"id": "call_scripted", "type": "function", "function": {"name": call[0], "arguments": json.dumps(call[1])}}]}
    return {"id": COMPLETION_ID, "object": "chat.completion", "model": model, "usage": USAGE,
            "choices": [{"index": 0, "finish_reason": "stop" if call is None else "tool_calls", "message": message}]}


def stream_chunks(call: tuple[str, dict] | None, model: str) -> list[dict]:
    reply = completion(call, model)["choices"][0]
    delta = dict(reply["message"])
    if call is not None:
        delta["tool_calls"] = [dict(delta["tool_calls"][0], index=0)]
    base = {"id": COMPLETION_ID, "object": "chat.completion.chunk", "model": model}
    return [dict(base, choices=[{"index": 0, "delta": delta, "finish_reason": None}]),
            dict(base, choices=[{"index": 0, "delta": {}, "finish_reason": reply["finish_reason"]}], usage=USAGE)]


def make_server(host: str, port: int, script: Script) -> ThreadingHTTPServer:
    model = script.plan["model"]

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            return

        def _json(self, status: int, payload: dict) -> None:
            data = json.dumps(payload).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            self._json(200, {"object": "list", "data": [{"id": model, "object": "model"}]})

        def do_POST(self):
            try:
                length = int(self.headers.get("Content-Length") or 0)
                if not 0 <= length <= MAX_REQUEST_BYTES:
                    raise ValueError
                body = json.loads(self.rfile.read(length) or b"{}")
                if not isinstance(body, dict):
                    raise ValueError
            except ValueError:
                self._json(400, {"error": {"message": "invalid request"}})
                return
            call = script.decide(body)
            if not body.get("stream"):
                self._json(200, completion(call, model))
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for chunk in stream_chunks(call, model):
                self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()

    return ThreadingHTTPServer((host, port), Handler)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="분리 배치 O 합성 모델(평가 overlay 전용)")
    parser.add_argument("--host", required=True)
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--plan", required=True)
    parser.add_argument("--origin", required=True)
    args = parser.parse_args(argv)
    try:
        plan = load_plan(args.plan, args.origin)
    except PlanError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    make_server(args.host, args.port, Script(plan)).serve_forever()
    return 0


if __name__ == "__main__":
    sys.exit(main())
