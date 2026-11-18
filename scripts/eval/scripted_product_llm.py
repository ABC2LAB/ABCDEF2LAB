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

실행: python3 -I -B scripts/eval/scripted_product_llm.py --host <주소> --port <포트> --plan <계획 JSON> --origin <대상 origin>
종료 코드: 2 계획·인자 오류.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

PLAN_FORMAT = "abc2lab.scripted-product-plan/v1"
PLAN_FIELDS = frozenset({"format", "model", "exploration", "intents", "commands", "timeout_ms"})
STEP_KINDS = frozenset({"navigate", "bash"})
EXPLORATION_TOOL = "report_discovery"
NAVIGATE_TOOL = "mcp__explore_browser__browser_navigate"
PLANNER_TOOL = "add_intent"
WORKER_TOOL = "Bash"
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
    if not isinstance(plan, dict) or set(plan) != PLAN_FIELDS or plan["format"] != PLAN_FORMAT or not _text(plan["model"]):
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
    return {"model": plan["model"], "timeout_ms": plan["timeout_ms"], "intents": [fill(i) for i in intents],
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


class Script:
    """프로세스 전체 진행 상태(의도·명령 순번). 요청이 여러 스레드로 와도 한 번씩만 낸다."""

    def __init__(self, plan: dict):
        self.plan, self.lock = plan, threading.Lock()
        self.intents_sent = self.commands_sent = 0
        self.counts = {"requests": 0, "exploration": 0, "planner": 0, "worker": 0, "done": 0, "skipped": 0}

    def decide(self, body: dict) -> tuple[str, dict] | None:
        tools, messages = _tools(body), _messages(body)
        last_is_result = bool(messages) and messages[-1].get("role") == "tool"
        with self.lock:
            self.counts["requests"] += 1
            if EXPLORATION_TOOL in tools:
                call = self._exploration(tools, sum(1 for m in messages if m.get("role") == "tool"))
            elif PLANNER_TOOL in tools and WORKER_TOOL not in tools:
                call = None if last_is_result else self._planner(tools[PLANNER_TOOL])
            elif WORKER_TOOL in tools:
                call = self._worker(tools[WORKER_TOOL])
            else:
                call = None
            self.counts["done" if call is None else ("exploration" if EXPLORATION_TOOL in tools else
                                                      "planner" if call[0] == PLANNER_TOOL else "worker")] += 1
            return call

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
