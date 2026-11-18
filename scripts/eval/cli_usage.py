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
# 무엇을 실행할지 글이나 파일에서 정하는 명령(정적으로 풀지 않는다).
_INDIRECT = frozenset({"eval", "source", "."})
_HEREDOC_DELIMITER = re.compile(r"-?\s*(['\"]?)\\?([A-Za-z_][A-Za-z0-9_]*)\1")
# 셸 예약어. 단순 명령의 첫 낱말이어도 명령 이름이 아니다. 앞의 것은 건너뛰고 다음 낱말을 보고, 뒤의 것은 그 조각 전체가
# 명령이 아니다(반복 변수·패턴 목록).
_RESERVED_PREFIX = frozenset({"if", "then", "else", "elif", "while", "until", "do", "!", "{", "}", "fi", "done", "esac"})
_RESERVED_SEGMENT = frozenset({"for", "case", "select", "function", "in"})
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


def _heredoc_markers(line: str) -> list[tuple[str, bool]]:
    """한 줄에서 따옴표 밖의 heredoc 시작(`<<`, here-string `<<<`는 아님)마다 (구분어, 본문이 확장되는지).
    구분어에 따옴표·백슬래시가 있으면 본문은 확장되지 않는다(명령 치환도 일어나지 않는다)."""
    markers, quote, i = [], None, 0
    while i < len(line):
        char = line[i]
        if quote is not None:
            if char == quote:
                quote = None
            elif char == "\\" and quote == '"':
                i += 1
        elif char in "'\"":
            quote = char
        elif char == "\\":
            i += 1
        elif line.startswith("<<", i) and not line.startswith("<<<", i):
            match = _HEREDOC_DELIMITER.match(line, i + 2)
            if match:
                quoted = bool(match.group(1)) or "\\" in match.group(0)
                markers.append((match.group(2), not quoted))
                i = match.end()
                continue
        i += 1
    return markers


def _split_heredocs(command: str) -> tuple[str, list[str]]:
    """(heredoc 본문을 뺀 명령 글, 확장되는 본문들). 본문은 입력 글이라 명령이 아니지만, 따옴표 없는 구분어의 본문 안
    명령 치환은 실행된다(#682 리뷰). 데이터 속 `<<`(따옴표 안)는 heredoc이 아니다."""
    out, bodies, waiting = [], [], []
    for line in command.split("\n"):
        if waiting:
            delimiter, expands, buffer = waiting[0]
            if line.strip() == delimiter:
                waiting.pop(0)
                if expands:
                    bodies.append("\n".join(buffer))
            else:
                buffer.append(line)
            continue
        out.append(line)
        waiting = [(delimiter, expands, []) for delimiter, expands in _heredoc_markers(line)]
    for delimiter, expands, buffer in waiting:  # 구분어 줄이 없으면 끝까지가 본문이다
        if expands:
            bodies.append("\n".join(buffer))
    return "\n".join(out), bodies


def _drop_heredoc_bodies(command: str) -> str:
    return _split_heredocs(command)[0]


def _substitutions(command: str) -> tuple[list[str], bool]:
    """명령 치환(`$(…)`·백틱)의 안쪽 글. 작은따옴표 안은 치환이 아니고, 큰따옴표 안은 치환이다. 짝이 맞지 않으면
    두 번째 값이 True(정적으로 풀지 못함)."""
    inner, broken, quote, i = [], False, None, 0
    while i < len(command):
        char = command[i]
        if quote == "'":
            if char == "'":
                quote = None
        elif char == "\\":
            i += 1
        elif char == "'" and quote is None:
            quote = "'"
        elif char == '"':
            quote = None if quote == '"' else '"'
        elif command.startswith("$(", i) and not command.startswith("$((", i):
            depth, j, q = 1, i + 2, None
            while j < len(command) and depth:
                c = command[j]
                if q:
                    if c == q:
                        q = None
                    elif c == "\\" and q == '"':
                        j += 1
                elif c in "'\"":
                    q = c
                elif c == "\\":
                    j += 1
                elif c == "(":
                    depth += 1
                elif c == ")":
                    depth -= 1
                j += 1
            if depth:
                return inner, True
            inner.append(command[i + 2:j - 1])
            i = j
            continue
        elif char == "`":
            j = command.find("`", i + 1)
            if j < 0:
                return inner, True
            inner.append(command[i + 1:j])
            i = j + 1
            continue
        i += 1
    return inner, broken


def _shell_command_string(tokens: list[str], start: int) -> str | None:
    """`셸 [옵션…] -c 명령` 또는 묶인 옵션(`-ec`·`-lc`)의 명령 글."""
    i = start
    while i < len(tokens) and tokens[i][:1] in ("-", "+") and tokens[i] != "--":
        flag = tokens[i]
        if flag in ("-o", "+o"):
            i += 2
            continue
        if flag.startswith("-") and not flag.startswith("--") and "c" in flag[1:]:
            rest = [t for t in tokens[i + 1:] if t[:1] not in ("-", "+")]
            return rest[0] if rest else None
        i += 1
    return None


def analyze_command(command: str, depth: int = 0) -> tuple[list[str], bool]:
    """(실행되는 명령 이름들, 정적으로 다 풀지 못했는지). 풀지 못한 구문: eval·source·`.`, 변수로 된 명령 이름,
    셸 낱말 나누기 실패, 짝이 맞지 않는 치환, 깊이 상한을 넘는 중첩. 이런 호출은 이름 수에 0으로 남아도 "안 썼다"는
    근거가 아니다(uncertain_calls로 따로 센다)."""
    words, uncertain = [], False
    if depth > 3:
        return words, True
    body, expanding = _split_heredocs(command)
    substitutions, broken = _substitutions(body)
    uncertain |= broken
    for text in expanding:  # 따옴표 없는 heredoc 본문의 명령 치환도 실행된다
        found, unsure = _substitutions(text)
        substitutions += found
        uncertain |= unsure
    for inner in substitutions:
        found, unsure = analyze_command(inner, depth + 1)
        words += found
        uncertain |= unsure
    for segment in _segments(body):
        try:
            tokens = shlex.split(segment, posix=True)
        except ValueError:
            tokens, uncertain = segment.split(), True
        if tokens and tokens[0] in _RESERVED_SEGMENT:
            continue  # for·case 등: 이 조각의 낱말은 명령이 아니다(본문은 do·then 뒤 조각에서 센다)
        i = 0
        while i < len(tokens):
            token = tokens[i]
            if _ASSIGNMENT.match(token) or token in _RESERVED_PREFIX:
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
            if "$" in token or base in _INDIRECT:
                uncertain = True  # 변수로 된 명령 이름·eval·source는 무엇이 실행되는지 정적으로 모른다
            if base:
                words.append(base)
                if base in _SHELLS:
                    inner = _shell_command_string(tokens, i + 1)
                    if inner is not None:
                        found, unsure = analyze_command(inner, depth + 1)
                        words += found
                        uncertain |= unsure
            break
    # 치환은 바깥 단순 명령 나누기에서도 한 번 더 보일 수 있다. 세기는 이름 집합이라 처음 나온 순서로 한 번씩만 둔다.
    return list(dict.fromkeys(words)), uncertain


def command_words(command: str, depth: int = 0) -> list[str]:
    """단순 명령마다 실행되는 명령 이름(basename). 셸 `-c`(묶인 옵션 포함)와 명령 치환(큰따옴표 안 포함) 안도 센다."""
    return analyze_command(command, depth)[0]


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
    total = unreadable = uncertain = 0
    for record in records:
        total += 1
        command = tool_input_command(record.get("command"))
        if command is None:
            unreadable += 1
            continue
        found, unsure = analyze_command(command)
        if unsure:
            uncertain += 1
        used = wanted & set(found)
        for name in used:
            calls[name] += 1
            if not with_failures:
                continue
            output, exit_code = record.get("output"), record.get("exit_code")
            if not_found(name, output if isinstance(output, str) else None, exit_code if type(exit_code) is int else None):
                missing[name] += 1
            elif (type(exit_code) is int and exit_code != 0) or record.get("failed") is True:
                other[name] += 1
    # uncertain_calls: 정적으로 다 풀지 못한 호출 수. 0이 아니면 이름 수 0을 "안 썼다"로 읽지 않는다.
    out = {"format": FORMAT, "names": sorted(wanted), "bash_calls": total, "unreadable_calls": unreadable,
           "uncertain_calls": uncertain,
           "calls": {k: v for k, v in calls.items() if v}}
    if with_failures:
        out["not_found"] = {k: v for k, v in missing.items() if v}
        out["other_failures"] = {k: v for k, v in other.items() if v}
    return out
