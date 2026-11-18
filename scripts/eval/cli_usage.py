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
# 셸 예약어. 단순 명령의 첫 낱말이어도 명령 이름이 아니다. 앞의 것은 건너뛰고 다음 낱말을 보고, 뒤의 것은 그 조각 전체가
# 명령이 아니다(반복 변수·패턴 목록).
_RESERVED_PREFIX = frozenset({"if", "then", "else", "elif", "while", "until", "do", "!", "{", "}", "fi", "done", "esac"})
_RESERVED_SEGMENT = frozenset({"for", "case", "select", "function", "in"})
# `{`·`}`는 낱말일 때만 예약어다(_RESERVED_PREFIX). 연산자로 보면 `${x}`·`${#x}`가 쪼개져 안쪽이 명령 이름이 된다.
_OPERATOR_CHARS = frozenset(";&|\n()`")
# 명령을 찾지 못하거나 실행하지 못한 셸 종료 코드(bash: 127 없음, 126 실행 불가).
NOT_FOUND_EXIT_CODES = frozenset({126, 127})


class CliUsageError(ValueError):
    """고정 사유만 담는다(명령 원문을 싣지 않는다)."""


def validate_names(names: object) -> tuple[str, ...]:
    if (not isinstance(names, (list, tuple)) or len(names) > MAX_NAMES
            or any(not isinstance(name, str) or not _NAME.fullmatch(name) for name in names) or len(set(names)) != len(names)):
        raise CliUsageError("cli_usage_names_invalid")
    return tuple(sorted(names))


def _command_position(current: str) -> bool:
    words = current.split()
    return not words or all(word in _RESERVED_PREFIX for word in words)


def _segments(command: str) -> list[tuple[str, str | None]]:
    """따옴표 밖의 셸 연산자로 나눈 단순 명령들과 그 앞 구분자(`&&`·`||`·`;;`·`$(`·한 글자 연산자, 처음은 None)."""
    out, current, quote, sep, i = [], [], None, None, 0

    def cut(next_sep: str) -> None:
        nonlocal current, sep
        out.append(("".join(current), sep))
        current, sep = [], next_sep
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
        elif char == "&" and ((current and current[-1] in "<>") or command.startswith("&>", i)):
            current.append(char)  # 리다이렉션(2>&1, >&2, &>파일)의 &는 구분자가 아니다
        elif command.startswith("$((", i) or (command.startswith("((", i) and _command_position("".join(current))):
            # 산술식은 명령이 아니다: `$((…))`는 낱말의 일부, 명령 자리(조각 처음·if 등 예약어 뒤)의 `((…))`는 산술 명령 전체
            arithmetic = command.startswith("$((", i)
            end = _arithmetic_end(command, i + (3 if arithmetic else 2), 2) or len(command)
            if arithmetic:
                current.append(command[i:end])
            i = end
            continue
        elif command.startswith("$(", i):
            cut("$(")
            i += 1
        elif command[i:i + 2] in ("&&", "||", ";;"):
            cut(command[i:i + 2])
            i += 1
        elif char in _OPERATOR_CHARS:
            cut(char)
        else:
            current.append(char)
        i += 1
    out.append(("".join(current), sep))
    return out


# 셸 메타 문자(bash). 따옴표·백슬래시로 막지 않은 이 문자나 공백 뒤가 낱말 경계이고, 경계에서 `#`로 시작하는 낱말이 주석이다.
_METACHARACTERS = frozenset("|&;()<>")


def _heredoc_word(command: str, i: int) -> tuple[int, str, bool] | None:
    """`<<`·`<<-` 뒤 구분어 낱말 하나(bash처럼 따옴표 없는 공백·메타 문자까지, 따옴표를 벗긴 글). (끝 위치, 구분어,
    본문이 확장되는지). 따옴표·백슬래시가 하나라도 있으면 본문은 확장되지 않는다. 낱말이 없거나 따옴표가 안 닫히면 None
    (정적으로 풀지 못함, #682 리뷰: `<<END-TEXT`를 `END`로 자르면 뒤 명령을 본문으로 뺀다)."""
    while i < len(command) and command[i] in " \t":
        i += 1
    word, quoted = [], False
    while i < len(command) and not (command[i].isspace() or command[i] in _METACHARACTERS):
        char = command[i]
        if char == "'":
            end = command.find("'", i + 1)
            if end < 0:
                return None
            word.append(command[i + 1:end])
            quoted, i = True, end + 1
            continue
        if char == '"':
            j, part = i + 1, []
            while j < len(command) and command[j] != '"':
                if command[j] == "\\" and j + 1 < len(command) and command[j + 1] in '"\\$`\n':
                    j += 1
                part.append(command[j])
                j += 1
            if j >= len(command):
                return None
            word.append("".join(part))
            quoted, i = True, j + 1
            continue
        if char == "\\":
            if i + 1 >= len(command):
                return None
            word.append(command[i + 1])
            quoted, i = True, i + 2
            continue
        word.append(char)
        i += 1
    delimiter = "".join(word)
    return (i, delimiter, not quoted) if delimiter else None


def _arithmetic_end(command: str, i: int, opened: int) -> int | None:
    """`$((`·`((` 산술의 끝(닫는 `))` 다음 위치). 안의 `<<`는 시프트 연산이지 heredoc이 아니다."""
    depth, j = opened, i
    while j < len(command) and depth:
        if command[j] == "(":
            depth += 1
        elif command[j] == ")":
            depth -= 1
        j += 1
    return j if depth == 0 else None


def _continued(line: str) -> bool:
    """줄 끝이 막히지 않은 백슬래시(홀수 개)면 다음 줄로 잇는다."""
    return (len(line) - len(line.rstrip("\\"))) % 2 == 1


def _preprocess(command: str) -> tuple[str, list[str], bool]:
    """(주석·heredoc 본문을 뺀 명령 글, 확장되는 heredoc 본문들). 따옴표 상태를 줄 너머까지 들고 한 번에 훑는다(#682 리뷰:
    주석·heredoc·치환을 서로 다른 규칙으로 보면 주석 속 치환을 세거나 `)#` 뒤 주석의 `<<`로 다음 명령을 지운다).

    - 주석: 따옴표 밖에서 낱말 첫 글자인 `#`(처음·공백·메타 문자 뒤)부터 줄 끝까지. `$#`·`${#x}`·`a#b`는 주석이 아니다.
    - heredoc: 따옴표·주석 밖의 `<<`(here-string `<<<`는 아님) 뒤 구분어. 그 줄이 끝나면 구분어 줄까지가 본문이다. 본문은
      입력 글이라 명령이 아니지만, 따옴표 없는 구분어의 본문 안 명령 치환은 실행된다(본문의 `#`·따옴표는 글자).
    """
    # boundary: 지금 자리가 낱말 첫 글자인지. 앞 글자만 보면 `a\ #x`(막은 공백 뒤 `#`, 낱말 가운데)를 주석으로 잘못 본다.
    out, bodies, pending, quote, boundary, unsure, i = [], [], [], None, True, False, 0
    while i < len(command):
        char = command[i]
        if quote is not None:
            if char == "\\" and quote == '"' and command.startswith("\n", i + 1):
                i += 2  # 큰따옴표 안 줄 이음도 셸이 지운다(작은따옴표 안은 글자 그대로)
                continue
            out.append(char)
            if char == quote:
                quote = None
            elif char == "\\" and quote == '"' and i + 1 < len(command):
                out.append(command[i + 1])
                i += 1
            boundary = False
        elif char in "'\"":
            quote = char  # 다음 글자부터 따옴표 안이고, 거기서 경계가 꺼진다
            out.append(char)
        elif char == "\\" and command.startswith("\n", i + 1):
            # 줄 이음(백슬래시+줄바꿈)은 셸이 읽을 때 지운다(#682 리뷰: `n\⏎px`는 `npx` 한 낱말이다). 앞뒤 글자가 이어지고
            # 경계 상태도 바꾸지 않는다. 명령 이름 가운데·들여쓰기 앞·치환 안 모두 같다.
            i += 2
            continue
        elif char == "\\" and i + 1 < len(command):
            out += [char, command[i + 1]]
            boundary = False
            i += 1
        elif char == "#" and boundary:
            while i + 1 < len(command) and command[i + 1] != "\n":
                i += 1
        elif command.startswith("<<<", i):
            out.append("<<<")
            boundary = True
            i += 3
            continue
        elif command.startswith("$((", i) or (boundary and command.startswith("((", i)):
            start = i + (3 if char == "$" else 2)
            end = _arithmetic_end(command, start, 2)
            if end is None:
                unsure = True
                end = len(command)
            if char == "$":
                # 산술 확장은 값 하나로 바뀌는 낱말 조각이다. 안쪽 공백으로 낱말을 쪼개면 `x=$(( 1 + 2 )) npx`에서 `1`을 명령으로
                # 세고 뒤 명령을 놓친다(#682 리뷰 중 발견). 자리에는 값 하나를 두고, 안쪽 명령 치환은 실행되므로 따로 훑는다.
                bodies.append(command[start:end - 2] if command.startswith("))", end - 2) else command[start:end])
                out.append("0")
            else:
                out.append(command[i:end])
            boundary, i = False, end
            continue
        elif command.startswith("<<", i):
            strip_tabs = command.startswith("<<-", i)
            word = _heredoc_word(command, i + (3 if strip_tabs else 2))
            if word is None:
                unsure = True  # 구분어 없음·닫히지 않은 따옴표: 무엇이 본문인지 정적으로 모른다
                out.append(command[i:i + 2])
                boundary, i = True, i + 2
                continue
            end, delimiter, expands = word
            pending.append((delimiter, expands, strip_tabs))
            out.append(command[i:end])
            boundary, i = False, end
            continue
        elif char == "\n" and pending:
            out.append(char)
            i += 1
            for delimiter, expands, strip_tabs in pending:
                lines = []
                while i <= len(command):
                    stop = command.find("\n", i)
                    line = command[i:] if stop < 0 else command[i:stop]
                    i = len(command) + 1 if stop < 0 else stop + 1
                    # 확장되는 본문은 줄 끝 백슬래시가 다음 줄을 잇고, 구분어 비교도 이은 줄로 한다(bash). `<<-` 탭 지우기와
                    # 겹치면 정적으로 풀지 않는다.
                    while expands and _continued(line) and i <= len(command):
                        unsure = unsure or strip_tabs
                        stop = command.find("\n", i)
                        line = line[:-1] + (command[i:] if stop < 0 else command[i:stop])
                        i = len(command) + 1 if stop < 0 else stop + 1
                    if strip_tabs:
                        line = line.lstrip("\t")  # `<<-`는 줄 앞 탭만 지운다(공백은 아니다)
                    if line == delimiter:  # bash는 줄 전체가 구분어와 같을 때만 닫는다(#682 리뷰)
                        break
                    lines.append(line)
                if expands:  # 구분어 줄이 없으면 끝까지가 본문이다
                    bodies.append("\n".join(lines))
            pending, boundary = [], True
            continue
        else:
            out.append(char)
            boundary = char.isspace() or char in _METACHARACTERS
        i += 1
    return "".join(out), bodies, unsure


def _substitutions(command: str, quotes: bool = True) -> tuple[list[str], bool]:
    """명령 치환(`$(…)`·백틱)의 안쪽 글. 작은따옴표 안은 치환이 아니고, 큰따옴표 안은 치환이다. 짝이 맞지 않으면
    두 번째 값이 True(정적으로 풀지 못함). quotes=False는 따옴표 없는 heredoc 본문이다: 본문의 따옴표는 그냥 글자라
    `'$(…)'`도 실행된다(#682 리뷰). 백슬래시로 막은 달러 기호만 치환이 아니다."""
    inner, broken, quote, i = [], False, None, 0
    while i < len(command):
        char = command[i]
        if quote == "'":
            if char == "'":
                quote = None
        elif char == "\\":
            i += 1
        elif not quotes and char in "'\"":
            pass
        elif char == "'" and quote is None:
            quote = "'"
        elif char == '"':
            quote = None if quote == '"' else '"'
        elif command.startswith("$(", i) and not command.startswith("$((", i):
            # 안쪽은 따로 된 셸 글이다. 주석 속 괄호는 세지 않는다(#682 리뷰: 큰따옴표 속 치환의 주석 `)`로 일찍 닫았다).
            depth, j, q, at_word = 1, i + 2, None, True
            while j < len(command) and depth:
                c = command[j]
                if q:
                    if c == q:
                        q = None
                    elif c == "\\" and q == '"':
                        j += 1
                    at_word = False
                elif c in "'\"":
                    q, at_word = c, False
                elif c == "\\":
                    j += 1
                    at_word = False
                elif c == "#" and at_word:
                    while j + 1 < len(command) and command[j + 1] != "\n":
                        j += 1
                elif c == "(":
                    depth += 1
                    at_word = True
                elif c == ")":
                    depth -= 1
                    at_word = True
                else:
                    at_word = c.isspace() or c in _METACHARACTERS
                j += 1
            if depth:
                return inner, True
            text = command[i + 2:j - 1]
            # 안쪽의 case 패턴 `a)`·heredoc 본문의 괄호는 이 괄호 세기로 끝을 정확히 못 찾는다 → 정적으로 다 풀지 못함
            if re.search(r"(?:^|[\s;&|(])case\s", text) or re.search(r"(?<!<)<<(?!<)", text):
                broken = True
            inner.append(text)
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


# 명령 앞 리다이렉션(`</dev/null npx`, `2>/dev/null npx`, `> out npx`). 대상이 같은 낱말에 붙었으면 그 낱말만, 연산자만
# 있으면 다음 낱말까지가 리다이렉션이다(#682 리뷰).
_REDIRECTION = re.compile(r"(?:[0-9]+|&)?(?:<<<|<<-?|<>|<&|>&|>>|>\||<|>)(.*)", re.S)


class _Level:
    """복합 명령·묶음 한 겹. cond가 참이면 그 안의 명령은 실행이 조건에 달렸다."""

    def __init__(self, kind: str, cond: bool):
        self.kind, self.cond = kind, cond


def analyze_command_detail(command: str, depth: int = 0) -> tuple[list[str], list[str], bool]:
    """(늘 실행되는 명령 이름들, 실행이 조건에 달린 명령 이름들, 정적으로 다 풀지 못했는지).

    조건에 달린 자리: `&&`·`||` 뒤, if·while·until·for의 then/do/else/elif 본문, case 갈래, 그 안의 묶음·치환, 함수 본문
    (정의만으로는 실행되지 않는다, #682 리뷰). 이런 이름은 실제로 실행됐는지 정적으로 모른다. 풀지 못한 구문: eval·source·
    `.`, 변수로 된 명령 이름, 셸 낱말 나누기 실패, 짝이 맞지 않는 치환, 함수 정의, 깊이 상한을 넘는 중첩."""
    definite, conditional, uncertain = [], [], False
    if depth > 3:
        return definite, conditional, True
    body, expanding, unsure = _preprocess(command)  # 주석·heredoc 본문을 뺀 글(나누기·치환은 이 글에서만)
    uncertain |= unsure
    substitutions, broken = _substitutions(body)
    uncertain |= broken
    for text in expanding:  # 따옴표 없는 heredoc 본문의 명령 치환도 실행된다(본문의 따옴표는 글자)
        found, unsure = _substitutions(text, quotes=False)
        substitutions += found
        uncertain |= unsure
    nested_definite, nested_conditional = [], []
    for inner in substitutions:
        sure, maybe, unsure = analyze_command_detail(inner, depth + 1)
        nested_definite += sure
        nested_conditional += maybe
        uncertain |= unsure

    stack: list[_Level] = []
    and_or = function_seen = any_conditional = continued = False
    segments = _segments(body)
    for index, (segment, sep) in enumerate(segments):
        # `&&`·`||`·`|` 바로 뒤 줄바꿈(사이에 주석·빈 줄 포함)은 같은 목록이 다음 줄로 이어지는 것이다(#682 리뷰)
        carried = continued
        continued = not segment.strip() and (sep in ("&&", "||", "|") or (sep == "\n" and carried))
        if sep == "\n" and carried:
            pass
        elif sep in (None, ";", "\n", "&", ";;"):
            and_or = False
        elif sep in ("&&", "||"):
            and_or = True
        elif sep in ("(", "$("):
            stack.append(_Level("group", and_or))
            and_or = False
        elif sep == ")" and stack and stack[-1].kind == "group":
            stack.pop()  # case 갈래 패턴의 `)`는 맨 위가 case라 닫지 않는다
        # 함수 정의 `name() { … }`·`function name`: 본문은 호출될 때만 실행된다
        following = segments[index + 1:index + 3]
        if (re.fullmatch(r"\s*[A-Za-z_][\w.-]*\s*", segment) and len(following) == 2 and following[0][1] == "("
                and not following[0][0].strip() and following[1][1] == ")"):
            function_seen = uncertain = True
            continue
        try:
            tokens = shlex.split(segment, posix=True)
        except ValueError:
            tokens, uncertain = segment.split(), True

        def conditional_here() -> bool:
            return and_or or function_seen or any(level.cond for level in stack)
        i = 0
        # 조각 앞 예약어: 복합 명령의 겹을 따라간다
        while i < len(tokens) and tokens[i] in _RESERVED_PREFIX | _RESERVED_SEGMENT:
            word = tokens[i]
            if word in ("if", "while", "until"):
                stack.append(_Level(word, conditional_here()))
            elif word in ("for", "select"):
                stack.append(_Level("loop", conditional_here()))
            elif word == "case":
                stack.append(_Level("case", True))
            elif word in ("then", "do", "else", "elif") and stack:
                stack[-1].cond = True
            elif word in ("fi", "done", "esac", "}") and stack:
                stack.pop()
            elif word == "{":
                stack.append(_Level("brace", and_or))
                and_or = False
            elif word == "function":
                function_seen = uncertain = True
            if word in _RESERVED_SEGMENT:
                break  # for·case·select·function·in: 이 조각의 남은 낱말은 명령이 아니다
            i += 1
        if i < len(tokens) and tokens[i] in _RESERVED_SEGMENT:
            continue
        target = conditional if conditional_here() else definite
        any_conditional |= conditional_here()
        while i < len(tokens):
            token = tokens[i]
            redirect = _REDIRECTION.fullmatch(token)
            if redirect:
                i += 1 if redirect.group(1) else 2
                continue
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
                target.append(base)
                if base in _SHELLS:
                    inner = _shell_command_string(tokens, i + 1)
                    if inner is not None:
                        sure, maybe, unsure = analyze_command_detail(inner, depth + 1)
                        if target is definite:
                            definite += sure
                        conditional += maybe if target is definite else sure + maybe
                        uncertain |= unsure
            break
    # 치환·heredoc 본문의 이름: 바깥에 조건 자리가 하나라도 있으면 어느 자리의 치환인지 이 단계에서 가르지 않고 조건으로 둔다
    if any_conditional or function_seen:
        conditional = nested_definite + nested_conditional + conditional
    else:
        definite = nested_definite + definite  # 치환 안 이름을 먼저 둔다(이름 집합이라 순서는 표시용)
        conditional = nested_conditional + conditional
    definite = list(dict.fromkeys(definite))
    conditional = [word for word in dict.fromkeys(conditional) if word not in definite]
    return definite, conditional, uncertain


def analyze_command(command: str, depth: int = 0) -> tuple[list[str], bool]:
    """(실행될 수 있는 명령 이름들(늘 + 조건), 정적으로 다 풀지 못했는지). 세기는 count_usage가 둘을 가른다."""
    definite, conditional, uncertain = analyze_command_detail(command, depth)
    return list(dict.fromkeys(definite + conditional)), uncertain


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

    calls는 그 이름이 늘 실행되는 자리에 있던 Bash 호출 수(한 호출에 여러 번 나와도 1). 실행이 조건에 달린 자리(`&&`·
    `||` 뒤, if·loop 본문, case 갈래, 함수 본문)에만 있던 호출은 conditional_calls로 따로 세고 uncertain_calls에도 넣는다
    (실제로 실행됐는지 정적으로 모른다, #682 리뷰). with_failures면 calls 가운데 이름을 찾지 못한 것(not_found)과 그
    밖의 실패(종료 코드 ≠ 0 또는 failed)를 따로 센다."""
    wanted = frozenset(validate_names(list(names)))
    calls = {name: 0 for name in sorted(wanted)}
    maybe_calls = {name: 0 for name in sorted(wanted)}
    missing = {name: 0 for name in sorted(wanted)}
    other = {name: 0 for name in sorted(wanted)}
    total = unreadable = uncertain = 0
    for record in records:
        total += 1
        command = tool_input_command(record.get("command"))
        if command is None:
            unreadable += 1
            continue
        found, maybe, unsure = analyze_command_detail(command)
        used = wanted & set(found)
        possible = (wanted & set(maybe)) - used
        for name in possible:
            maybe_calls[name] += 1
        if unsure or possible:
            uncertain += 1
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
           "calls": {k: v for k, v in calls.items() if v},
           "conditional_calls": {k: v for k, v in maybe_calls.items() if v}}
    if with_failures:
        out["not_found"] = {k: v for k, v in missing.items() if v}
        out["other_failures"] = {k: v for k, v in other.items() if v}
    return out
