"""스모크 전용 스크립트 LLM(OpenAI 호환). 정해진 명령을 Bash 도구 호출로 돌려줘서 API 키 없이 실제 엔진을 끝까지 돌린다.

실행: python3 -I -B scripts/eval/scripted_llm.py --host <주소> --port <포트> --plan <계획 JSON> [--stats <통계 파일>]

- 계획 파일이 대상별 명령을 담는다. 이 코드에는 대상 앱의 경로·계정이 없다.
  `{"model": "...", "intent_summary": "...", "commands": ["curl ...", ...]}`
- 플래너(도구에 `add_intent`가 있는 요청)에는 의도 하나를, 워커(`Bash`가 있고 `add_intent`가 없는 요청)에는 앞선 도구 결과 수만큼
  진행한 다음 명령을 돌려준다. 명령이 끝나면 고정 문장으로 끝낸다.
- **판단하는 모델이 아니다.** 장치가 끝까지 맞물리는지만 본다. 요청 수·건수를 효과 수치로 쓰지 않는다.
- 요청 본문을 저장하지 않는다. 통계에는 호출 수만 남긴다. 외부로 보내는 요청은 없다.

종료 코드: 2 계획 파일 오류.
"""

from __future__ import annotations

import argparse
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

PLANNER_TOOL = "add_intent"
WORKER_TOOL = "Bash"
DONE_TEXT = "No further action."
PLAN_FIELDS = {"model", "intent_summary", "commands"}
MAX_PLAN_BYTES = 1024 * 1024
MAX_REQUEST_BYTES = 64 * 1024 * 1024
INTENT_PRIORITY = 5
CALL_ID = "call_scripted"
COMPLETION_ID = "gen-scripted"
USAGE = {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}


class PlanError(ValueError):
    """계획 파일이 형식에 맞지 않는다."""


def load_plan(path: str | Path) -> dict:
    try:
        data = Path(path).read_bytes()
    except OSError:
        raise PlanError("plan_unreadable") from None
    try:
        if len(data) > MAX_PLAN_BYTES:
            raise ValueError
        plan = json.loads(data.decode("utf-8"))
    except (UnicodeError, ValueError):
        raise PlanError("plan_invalid") from None
    if not isinstance(plan, dict) or set(plan) != PLAN_FIELDS \
            or not isinstance(plan["model"], str) or not plan["model"] \
            or not isinstance(plan["intent_summary"], str) or not plan["intent_summary"] \
            or not isinstance(plan["commands"], list) or not plan["commands"] \
            or not all(isinstance(command, str) and command for command in plan["commands"]):
        raise PlanError("plan_invalid")
    return plan


def decide(body: dict, plan: dict) -> tuple[str, dict] | None:
    """이번 요청에 돌려줄 도구 호출. 없으면 None(고정 문장으로 끝낸다)."""
    tools = {tool.get("function", {}).get("name") for tool in body.get("tools") or [] if isinstance(tool, dict)}
    results = sum(1 for message in body.get("messages") or [] if isinstance(message, dict) and message.get("role") == "tool")
    if PLANNER_TOOL in tools:
        return (PLANNER_TOOL, {"summary": plan["intent_summary"], "priority": INTENT_PRIORITY}) if results == 0 else None
    if WORKER_TOOL in tools and results < len(plan["commands"]):
        return WORKER_TOOL, {"command": plan["commands"][results], "description": "scripted"}
    return None


def completion(call: tuple[str, dict] | None, model: str) -> dict:
    if call is None:
        message: dict = {"role": "assistant", "content": DONE_TEXT}
    else:
        message = {"role": "assistant", "content": None, "tool_calls": [
            {"id": CALL_ID, "type": "function", "function": {"name": call[0], "arguments": json.dumps(call[1])}}]}
    return {"id": COMPLETION_ID, "object": "chat.completion", "model": model, "usage": USAGE,
            "choices": [{"index": 0, "finish_reason": "stop" if call is None else "tool_calls", "message": message}]}


def stream_chunks(call: tuple[str, dict] | None, model: str) -> list[dict]:
    if call is None:
        delta: dict = {"role": "assistant", "content": DONE_TEXT}
    else:
        delta = {"role": "assistant", "content": None, "tool_calls": [
            {"index": 0, "id": CALL_ID, "type": "function", "function": {"name": call[0], "arguments": json.dumps(call[1])}}]}
    base = {"id": COMPLETION_ID, "object": "chat.completion.chunk", "model": model}
    return [dict(base, choices=[{"index": 0, "delta": delta, "finish_reason": None}]),
            dict(base, choices=[{"index": 0, "delta": {}, "finish_reason": "stop" if call is None else "tool_calls"}], usage=USAGE)]


class Stats:
    """호출 수만 센다. 요청 내용은 받지 않는다."""

    def __init__(self, path: str | None):
        self.path, self.lock = path, threading.Lock()
        self.counts = {"requests": 0, "planner_calls": 0, "worker_calls": 0, "done": 0}

    def add(self, call: tuple[str, dict] | None) -> None:
        with self.lock:
            self.counts["requests"] += 1
            key = "done" if call is None else "planner_calls" if call[0] == PLANNER_TOOL else "worker_calls"
            self.counts[key] += 1
            if self.path:
                Path(self.path).write_text(json.dumps(self.counts, sort_keys=True), encoding="utf-8")


def make_server(host: str, port: int, plan: dict, stats: Stats) -> ThreadingHTTPServer:
    model = plan["model"]

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
            call = decide(body, plan)
            stats.add(call)
            if not body.get("stream"):
                self._json(200, completion(call, model))
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for chunk in stream_chunks(call, model):
                self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
                self.wfile.flush()
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()

    return ThreadingHTTPServer((host, port), Handler)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="스모크 전용 스크립트 LLM")
    parser.add_argument("--host", required=True)
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--plan", required=True)
    parser.add_argument("--stats")
    args = parser.parse_args(argv)
    try:
        plan = load_plan(args.plan)
    except PlanError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    make_server(args.host, args.port, plan, Stats(args.stats)).serve_forever()
    return 0


if __name__ == "__main__":
    sys.exit(main())
