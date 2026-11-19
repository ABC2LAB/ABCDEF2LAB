"""Bash 도구 입력 원문에서 정해진 CLI 이름이 관측되는지 센다(탐색 분리 배치 결정안 §10, 2026-11-18 사용자 결정).

셸 문법(heredoc·연산자·치환)은 해석하지 않는다. 찾기 전에 싼 정규화 하나만 한다: 줄 이음(백슬래시+줄바꿈)을 붙이고 따옴표
문자(' ")를 지운다. 그래서 줄 이음이나 따옴표 이어 붙이기로 쪼갠 이름(`n\\⏎px`, `'n''px'`)도 이어진 이름으로 보인다. 그 뒤
이름이 낱말 경계로 나오면 센다.

결과는 실행 수가 아니라 원문에서 관측한 등장 수다. 실제로 실행됐는지는 이 값으로 정하지 않는다. 등장한 이름이 없으면
"원문에서 이름이 관측되지 않음(미확인)"이고, 있으면 그 회차의 그 명령만 사람이 열어 실제 실행인지 확인한다(결정안 §10).
변수·eval·인코딩(base64 등)을 거쳐 이름을 만드는 호출은 관측하지 못한다.

낱말 경계: 이름 바로 앞뒤 글자가 이름 글자(영숫자·`_` `.` `+` `-`)가 아니다(`/usr/bin/npx`·`$(npx …)`·`npx;`는 세고
`npx2`·`my-npx`는 세지 않는다).

B는 회차를 내리기 전에 원본 엔진 `GET /api/commands?task=`로 읽고, O는 활동 기록에서 읽는다. 명령 원문·출력은 메모리에서만
보고 어디에도 쓰지 않는다. 결과에는 이름·건수만 있다.
"""

from __future__ import annotations

import json
import re
from typing import Iterable, Mapping

FORMAT = "abc2lab.cli-usage/v3"
MAX_NAMES = 256
_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,127}\Z")
_NAME_CHARS = "A-Za-z0-9_.+-"
NOT_FOUND_EXIT_CODES = frozenset({126, 127})


class CliUsageError(ValueError):
    """고정 사유만 담는다(명령 원문을 싣지 않는다)."""


def validate_names(names: object) -> tuple[str, ...]:
    if (not isinstance(names, (list, tuple)) or len(names) > MAX_NAMES
            or any(not isinstance(name, str) or not _NAME.fullmatch(name) for name in names) or len(set(names)) != len(names)):
        raise CliUsageError("cli_usage_names_invalid")
    return tuple(sorted(names))


def _pattern(name: str) -> re.Pattern:
    return re.compile(rf"(?<![{_NAME_CHARS}]){re.escape(name)}(?![{_NAME_CHARS}])")


def normalize(command: str) -> str:
    """찾기 전 정규화: 줄 이음(백슬래시+줄바꿈)을 붙이고 따옴표 문자를 지운다. 문법은 해석하지 않는다."""
    return command.replace("\\\n", "").replace("'", "").replace('"', "")


def occurrences(command: str, name: str) -> int:
    """정규화한 입력에 이름이 낱말 경계로 나온 횟수(따옴표·주석 안도 센다). 실행 수가 아니라 관측 수다."""
    return len(_pattern(name).findall(normalize(command)))


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


# 셸이 명령을 찾지 못했을 때 남기는 표준 줄(bash `bash: line 1: x: command not found`, dash `/bin/sh: 1: x: not found`).
# 이름은 꺼내지 않고 있었는지만 본다(이름 자리에 모델이 넣은 아무 글이나 올 수 있어 기록하지 않는다).
_SHELL_NOT_FOUND = re.compile(r"^(?:\S*/)?(?:ba|da|z|k)?sh(?:: line \d+)?: (?:\d+: )?\S+: (?:command )?not found$", re.M)


def shell_not_found(output: str | None, exit_code: int | None) -> bool:
    """이름과 상관없이 셸이 명령을 찾지 못했거나 실행하지 못한 흔적(종료 코드 126·127 또는 셸 표준 줄)."""
    if exit_code in NOT_FOUND_EXIT_CODES:
        return True
    return bool(output) and _SHELL_NOT_FOUND.search(output) is not None


def product_command_record(row: Mapping) -> dict:
    """제품 활동 표의 Bash 호출 한 줄({"cmd": 도구 입력, "res": 결과 글, "is_error": bool})을 count_usage 입력으로 바꾼다.
    E② Worker 결과는 신뢰 결과 JSON(`trusted.exit_code`·`tool_output`)이고, 탐색 직접 Bash 결과는 글 그대로다."""
    res = row.get("res")
    exit_code = None
    output = res if isinstance(res, str) else None
    if isinstance(res, str):
        try:
            parsed = json.loads(res)
        except ValueError:
            parsed = None
        if isinstance(parsed, dict) and isinstance(parsed.get("trusted"), dict):
            code = parsed["trusted"].get("exit_code")
            exit_code = code if type(code) is int else None
            tool_output = parsed.get("tool_output")
            output = tool_output if isinstance(tool_output, str) else json.dumps(tool_output)
    return {"command": row.get("cmd"), "output": output, "exit_code": exit_code, "failed": row.get("is_error") is True}


def count_usage(records: Iterable[Mapping], names: Iterable[str], *, with_failures: bool) -> dict:
    """records: {"command": Bash 도구 입력(JSON 문자열/객체), "output": 결과 글 또는 None, "exit_code": 정수 또는 None,
    "failed": 도구가 실패로 돌려줬는지(bool) 또는 None}. Bash 호출만 넘긴다.

    occurrences는 이름별 관측 등장 수(정규화한 입력의 등장 횟수 합), names_seen은 하나라도 관측된 이름이다. with_failures면 이름이
    등장한 호출 가운데 그 이름을 찾지 못한 것(not_found)과 그 밖의 실패(종료 코드 ≠ 0 또는 failed)를 이름별 호출 수로 센다."""
    wanted = validate_names(list(names))
    patterns = {name: _pattern(name) for name in wanted}
    seen = {name: 0 for name in wanted}
    missing = {name: 0 for name in wanted}
    other = {name: 0 for name in wanted}
    total = unreadable = 0
    for record in records:
        total += 1
        command = tool_input_command(record.get("command"))
        if command is None:
            unreadable += 1
            continue
        command = normalize(command)
        for name, pattern in patterns.items():
            count = len(pattern.findall(command))
            if not count:
                continue
            seen[name] += count
            if not with_failures:
                continue
            output, exit_code = record.get("output"), record.get("exit_code")
            if not_found(name, output if isinstance(output, str) else None, exit_code if type(exit_code) is int else None):
                missing[name] += 1
            elif (type(exit_code) is int and exit_code != 0) or record.get("failed") is True:
                other[name] += 1
    # 등장 0은 "원문에서 관측되지 않음(미확인)"이다. 입력을 읽지 못한 호출(unreadable_calls)은 그 관측 범위 밖이라 따로 센다.
    out = {"format": FORMAT, "names": list(wanted), "bash_calls": total, "unreadable_calls": unreadable,
           "occurrences": {k: v for k, v in seen.items() if v}, "names_seen": [k for k, v in seen.items() if v]}
    if with_failures:
        out["not_found"] = {k: v for k, v in missing.items() if v}
        out["other_failures"] = {k: v for k, v in other.items() if v}
    return out


def not_found_calls(records: Iterable[Mapping]) -> int:
    """목록과 상관없이 셸이 명령을 찾지 못한 Bash 호출 수(이름은 남기지 않는다)."""
    return sum(1 for record in records
               if shell_not_found(record.get("output") if isinstance(record.get("output"), str) else None,
                                  record.get("exit_code") if type(record.get("exit_code")) is int else None))


OBSERVED_LINES_FORMAT = "abc2lab.cli-observed-lines/v2"
MAX_OBSERVED_LINES = 64
MAX_OBSERVED_LINE_CHARS = 1000
# 모양을 뜰 명령 원문의 바이트 상한. 넘으면 줄을 남기지 않는다(보류 표시만).
MAX_SHAPE_INPUT_BYTES = 64 * 1024
_TRUNCATED_MARK = " [...]"
WITHHELD_TOO_LONG = "shape_input_too_long"
WITHHELD_UNPARSEABLE = "shape_unparseable"
# UTF-8로 나타낼 수 없는 원문(JSON 이스케이프로 들어온 단독 surrogate 등). 모양을 만들지 않고 보류만 남긴다(#736 리뷰).
WITHHELD_INVALID = "shape_input_invalid"
# 낱말 자리표시(사람이 읽는 근거라 한국어). 관측된 CLI 이름과 셸 연산자 말고는 무엇이든 이것으로 바꾼다.
PLACEHOLDER = "<인자>"
PATH_PREFIX = "<경로>/"
# 원문 대신 남기는 것은 "모양"뿐이다(#736 리뷰, 2026-11-19 사용자 결정). 비밀을 골라 가리지 않고, 남겨도 되는 것만 남긴다:
# 따옴표 밖 낱말이 관측된 B 전용 CLI 이름이면 그 이름(경로의 마지막 조각이 그 이름이면 `<경로>/이름`), 따옴표 밖 기호 묶음이
# 셸 연산자 목록에 있으면 그 연산자. 그 밖의 낱말(옵션 이름·값·경로·따옴표 문자열·목록 밖 기호 묶음)은 전부 PLACEHOLDER다.
# 사람은 이 모양으로 "그 이름이 명령 자리에 있나"만 본다. 남는 낱말이 이름·연산자·자리표시뿐이라 인자 값은 남을 수 없다.
_PUNCT = frozenset(";&|<>()")
# 그대로 남기는 셸 연산자(따옴표 밖에서 기호 묶음으로 나온 것만). 그 밖의 기호 묶음은 PLACEHOLDER다.
SHELL_OPERATORS = frozenset({";", "&", "&&", "|", "||", "|&", ";;", "<", ">", ">>", "<<", "<<<", "<&", ">&", "&>", "&>>", ">|",
                             "<>", "(", ")"})


def shell_tokens(line: str) -> list[tuple[str, bool, bool]]:
    """한 줄을 셸 낱말로 나눈다: (낱말, 따옴표·역슬래시가 들었나, 따옴표 밖 기호 묶음인가). 작은따옴표·큰따옴표(안에서는 역슬래시
    뒤 큰따옴표·역슬래시·달러·백틱만 탈출)·역슬래시를 셸처럼 다루고, 따옴표 밖 기호(;&|<>())는 이어진 묶음 하나로 끊는다(shlex punctuation_chars와 같은
    경계). shlex는 따옴표를 떼어 '&&'와 && 를 가를 수 없어서 따로 둔다(#736 리뷰). 닫히지 않은 따옴표·끝 역슬래시는 ValueError."""
    tokens: list[tuple[str, bool, bool]] = []
    buf: list[str] = []
    quoted = started = False

    def flush() -> None:
        nonlocal quoted, started
        if started:
            tokens.append(("".join(buf), quoted, False))
        buf.clear()
        quoted = started = False

    i, n = 0, len(line)
    while i < n:
        c = line[i]
        if c in " \t\n":
            flush()
            i += 1
        elif c == "'":
            end = line.find("'", i + 1)
            if end < 0:
                raise ValueError("unclosed quote")
            buf.append(line[i + 1:end])
            quoted = started = True
            i = end + 1
        elif c == '"':
            i += 1
            while i < n and line[i] != '"':
                if line[i] == "\\" and i + 1 < n and line[i + 1] in '"\\$`':
                    buf.append(line[i + 1])
                    i += 2
                else:
                    buf.append(line[i])
                    i += 1
            if i >= n:
                raise ValueError("unclosed quote")
            quoted = started = True
            i += 1
        elif c == "\\":
            if i + 1 >= n:
                raise ValueError("trailing escape")
            buf.append(line[i + 1])
            quoted = started = True
            i += 2
        elif c in _PUNCT:
            flush()
            end = i
            while end < n and line[end] in _PUNCT:
                end += 1
            tokens.append((line[i:end], False, True))
            i = end
        else:
            buf.append(c)
            started = True
            i += 1
    flush()
    return tokens


def clip_shape(line: str) -> str:
    """모양 줄을 근거 상한(MAX_OBSERVED_LINE_CHARS)으로 자른다(모양에는 원문 인자가 없다)."""
    return line if len(line) <= MAX_OBSERVED_LINE_CHARS else line[:MAX_OBSERVED_LINE_CHARS] + _TRUNCATED_MARK


def pick_line(command: str, names: Iterable[str]) -> str:
    """명령 원문에서 한 줄을 고른다. 줄 이음만 붙여 줄로 나눈 뒤 정규화했을 때 관측된 이름이 나오는 첫 줄이고, 없으면
    첫 줄이다."""
    patterns = [_pattern(name) for name in names]
    lines = command.replace("\\\n", "").splitlines() or [""]
    return next((raw for raw in lines if any(pattern.search(normalize(raw)) for pattern in patterns)), lines[0])


def command_shape(line: str, names: Iterable[str]) -> str | None:
    """한 줄의 모양. 셸 낱말로 나누지 못하면(닫히지 않은 따옴표 등) None이다(안전을 확인할 수 없어 남기지 않음)."""
    wanted = set(names)
    try:
        tokens = shell_tokens(line)
    except ValueError:
        return None
    out = []
    for token, quoted, punct in tokens:
        if punct:
            out.append(token if token in SHELL_OPERATORS else PLACEHOLDER)
        elif quoted:
            out.append(PLACEHOLDER)  # 따옴표·역슬래시가 든 낱말은 문자열 인자다(이름·연산자 모양이어도)
        elif token in wanted:
            out.append(token)
        elif "/" in token and token.rsplit("/", 1)[1] in wanted:
            out.append(PATH_PREFIX + token.rsplit("/", 1)[1])
        else:
            out.append(PLACEHOLDER)
    return " ".join(out)


def observed_lines(records: Iterable[Mapping], names: Iterable[str]) -> tuple[list[dict], int]:
    """이름이 관측된 Bash 호출마다 한 줄의 모양을 남긴다(2026-11-19 사용자 결정 4, #736 리뷰로 원문 대신 모양). 항목은
    MAX_OBSERVED_LINES까지다. 반환: ([{"names": [...], "shape": 모양} 또는 {"names", "shape": None, "withheld": 사유}],
    관측된 호출 수). 원문은 메모리에서만 보고 남기지 않는다."""
    wanted = validate_names(list(names))
    patterns = {name: _pattern(name) for name in wanted}
    out: list[dict] = []
    observed = 0
    for record in records:
        command = tool_input_command(record.get("command"))
        if command is None:
            continue
        seen = [name for name, pattern in patterns.items() if pattern.search(normalize(command))]
        if not seen:
            continue
        observed += 1
        if len(out) >= MAX_OBSERVED_LINES:
            continue
        try:
            size = len(command.encode("utf-8"))
        except UnicodeEncodeError:
            out.append({"names": seen, "shape": None, "withheld": WITHHELD_INVALID})
            continue
        if size > MAX_SHAPE_INPUT_BYTES:
            out.append({"names": seen, "shape": None, "withheld": WITHHELD_TOO_LONG})
            continue
        shape = command_shape(pick_line(command, seen), seen)
        out.append({"names": seen, "shape": clip_shape(shape)} if shape is not None
                   else {"names": seen, "shape": None, "withheld": WITHHELD_UNPARSEABLE})
    return out, observed
