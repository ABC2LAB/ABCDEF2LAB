"""Bash 도구 호출에서 정해진 CLI 이름의 사용 수를 센다(탐색 분리 배치 결정안 §10, 2026-11-17 사용자 결정).

B에만 있는 CLI(도구 면 측정 `cli_differences.baseline_only`)를 모델이 실제로 썼는지 보려는 것이다. B는 회차를 내리기 전에
원본 엔진 `GET /api/commands?task=`로 읽어 이름별 호출 수만 남기고, O는 활동 기록에서 같은 이름의 호출 수와 그중 실행 실패
수를 센다. 명령 원문·출력은 메모리에서만 보고 어디에도 쓰지 않는다. 결과에는 이름·건수만 있다.

세는 기준(근사): 셸 명령을 따옴표 밖의 연산자(`;` `&` `|` 줄바꿈 괄호 중괄호 백틱 `$(`)로 나눈 단순 명령마다 첫 단어(앞의
`VAR=값`과 sudo·env·timeout 같은 감싸기 명령은 건너뜀)의 basename을 명령 이름으로 본다. 따옴표 안 문자열·heredoc 본문의
언급은 명령으로 보지 않으려 하지만, heredoc 본문 줄은 줄바꿈으로 나뉘어 단어가 잡힐 수 있다.
"""

from __future__ import annotations

import json
import re
import shlex
from typing import Iterable, Mapping

FORMAT = "abc2lab.cli-usage/v1"
MAX_NAMES = 256
_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,127}\Z")
_ASSIGNMENT = re.compile(r"[A-Za-z_][A-Za-z0-9_]*=")
# 다른 명령을 실행하는 감싸기 명령. 뒤의 옵션(-로 시작)은 건너뛰고, timeout은 시간 인자 하나를 더 건너뛴다.
_WRAPPERS = frozenset({"sudo", "env", "nohup", "exec", "time", "command", "builtin", "nice", "stdbuf", "xargs", "timeout"})
# 값을 하나 받는 감싸기 명령 옵션(그 값까지 건너뛴다).
_WRAPPER_VALUE_OPTIONS = {"sudo": frozenset({"-u", "-g", "-C", "-p", "-h", "-U"}), "env": frozenset({"-u", "-C", "-S"}),
                          "nice": frozenset({"-n"}), "xargs": frozenset({"-I", "-n", "-P", "-L", "-d", "-s", "-a", "-E"})}
# `셸 -c '명령'`이면 그 명령 글 안을 다시 센다.
_SHELLS = frozenset({"bash", "sh", "dash", "zsh"})
_HEREDOC = re.compile(r"<<-?\s*(['\"]?)([A-Za-z_][A-Za-z0-9_]*)\1")
_OPERATOR_CHARS = frozenset(";&|\n(){}`")
# 명령을 찾지 못하거나 실행하지 못한 셸 종료 코드(bash: 127 없음, 126 실행 불가).
NOT_FOUND_EXIT_CODES = frozenset({126, 127})


class CliUsageError(ValueError):
    """고정 사유만 담는다(명령 원문을 싣지 않는다)."""


def validate_names(names: object) -> tuple[str, ...]:
    if (not isinstance(names, (list, tuple)) or len(names) > MAX_NAMES
            or any(not isinstance(name, str) or not _NAME.fullmatch(name) for name in names) or len(set(names)) != len(names)):
        raise CliUsageError("cli_usage_names_invalid")
    return tuple(sorted(names))


def _segments(command: str) -> list[str]:
    """따옴표 밖의 셸 연산자로 나눈 단순 명령들."""
    out, current, quote, i = [], [], None, 0
    while i < len(command):
        char = command[i]
        if quote is not None:
            current.append(char)
            if char == quote:
                quote = None
            elif char == "\\" and quote == '"' and i + 1 < len(command):
                current.append(command[i + 1])
                i += 1
        elif char in "'\"":
            quote = char
            current.append(char)
        elif char == "\\" and i + 1 < len(command):
            current += [char, command[i + 1]]
            i += 1
        elif char == "#" and (not current or current[-1].isspace()):
            # 주석: 줄 끝까지 버린다(줄바꿈은 다음 차례에 구분자로 처리)
            while i + 1 < len(command) and command[i + 1] != "\n":
                i += 1
        elif char == "&" and ((current and current[-1] in "<>") or command.startswith("&>", i)):
            current.append(char)  # 리다이렉션(2>&1, >&2, &>파일)의 &는 구분자가 아니다
        elif command.startswith("$(", i):
            out.append("".join(current))
            current = []
            i += 1
        elif char in _OPERATOR_CHARS:
            out.append("".join(current))
            current = []
        else:
            current.append(char)
        i += 1
    out.append("".join(current))
    return out


def _drop_heredoc_bodies(command: str) -> str:
    """heredoc 본문 줄(구분어 줄까지)을 뺀다. 본문은 명령이 아니라 입력 글이다."""
    lines, out, waiting = command.split("\n"), [], []
    for line in lines:
        if waiting:
            if line.strip() == waiting[0]:
                waiting.pop(0)
            continue
        out.append(line)
        waiting = [match.group(2) for match in _HEREDOC.finditer(line)]
    return "\n".join(out)


def command_words(command: str, depth: int = 0) -> list[str]:
    """단순 명령마다 실행되는 명령 이름(basename). `bash -c '…'`은 안쪽 명령도 센다(깊이 3까지)."""
    words = []
    for segment in _segments(_drop_heredoc_bodies(command)):
        try:
            tokens = shlex.split(segment, posix=True)
        except ValueError:
            tokens = segment.split()
        i = 0
        while i < len(tokens):
            token = tokens[i]
            if _ASSIGNMENT.match(token):
                i += 1
                continue
            base = token.rsplit("/", 1)[-1]
            if base in _WRAPPERS:
                i += 1
                takes_value = _WRAPPER_VALUE_OPTIONS.get(base, frozenset())
                while i < len(tokens) and tokens[i].startswith("-"):
                    i += 2 if tokens[i] in takes_value else 1
                if base == "timeout" and i < len(tokens) and re.fullmatch(r"[0-9.]+[smhd]?", tokens[i]):
                    i += 1
                continue
            if base:
                words.append(base)
                if base in _SHELLS and depth < 3 and "-c" in tokens[i + 1:]:
                    inner = tokens.index("-c", i + 1) + 1
                    if inner < len(tokens):
                        words += command_words(tokens[inner], depth + 1)
            break
    return words


def tool_input_command(raw: object) -> str | None:
    """Bash 도구 입력(JSON 문자열 또는 객체)의 command 칸."""
    value = raw
    if isinstance(raw, str):
        try:
            value = json.loads(raw)
        except ValueError:
            return None
    if isinstance(value, dict) and isinstance(value.get("command"), str):
        return value["command"]
    return None


def not_found(name: str, output: str | None, exit_code: int | None) -> bool:
    """이 이름의 명령을 찾지 못했거나 실행하지 못한 흔적. 출력 문구는 bash·dash·env의 표준 문구다."""
    if exit_code in NOT_FOUND_EXIT_CODES:
        return True
    if not output:
        return False
    escaped = re.escape(name)
    return re.search(rf"(?:^|[\s/:]){escaped}: (?:command )?not found|(?:^|[\s/:]){escaped}: No such file or directory|"
                     rf"env: [‘'\"]?{escaped}[’'\"]?: No such file or directory", output, re.M) is not None


def count_usage(records: Iterable[Mapping], names: Iterable[str], *, with_failures: bool) -> dict:
    """records: {"command": Bash 도구 입력(JSON 문자열/객체), "output": 결과 글 또는 None, "exit_code": 정수 또는 None,
    "failed": 도구가 실패로 돌려줬는지(bool) 또는 None}. Bash 호출만 넘긴다.

    calls는 그 이름이 명령으로 실행된 Bash 호출 수(한 호출에 여러 번 나와도 1). with_failures면 그 호출 가운데 이름을
    찾지 못한 것(not_found)과 그 밖의 실패(종료 코드 ≠ 0 또는 failed)를 따로 센다."""
    wanted = frozenset(validate_names(list(names)))
    calls = {name: 0 for name in sorted(wanted)}
    missing = {name: 0 for name in sorted(wanted)}
    other = {name: 0 for name in sorted(wanted)}
    total = unreadable = 0
    for record in records:
        total += 1
        command = tool_input_command(record.get("command"))
        if command is None:
            unreadable += 1
            continue
        used = wanted & set(command_words(command))
        for name in used:
            calls[name] += 1
            if not with_failures:
                continue
            output, exit_code = record.get("output"), record.get("exit_code")
            if not_found(name, output if isinstance(output, str) else None, exit_code if type(exit_code) is int else None):
                missing[name] += 1
            elif (type(exit_code) is int and exit_code != 0) or record.get("failed") is True:
                other[name] += 1
    out = {"format": FORMAT, "names": sorted(wanted), "bash_calls": total, "unreadable_calls": unreadable,
           "calls": {k: v for k, v in calls.items() if v}}
    if with_failures:
        out["not_found"] = {k: v for k, v in missing.items() if v}
        out["other_failures"] = {k: v for k, v in other.items() if v}
    return out
