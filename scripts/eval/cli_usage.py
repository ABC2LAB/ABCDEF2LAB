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
import shlex
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


OBSERVED_LINES_FORMAT = "abc2lab.cli-observed-lines/v2"
MAX_OBSERVED_LINES = 64
MAX_OBSERVED_LINE_CHARS = 1000
# 모양을 뜰 명령 원문의 바이트 상한. 넘으면 줄을 남기지 않는다(보류 표시만).
MAX_SHAPE_INPUT_BYTES = 64 * 1024
_TRUNCATED_MARK = " [...]"
WITHHELD_TOO_LONG = "shape_input_too_long"
WITHHELD_UNPARSEABLE = "shape_unparseable"
ARG = "<arg>"
# 원문 대신 남기는 것은 "모양"뿐이다(#736 리뷰). 자격 가림(dlp.CredentialText)은 알려진 자격 형식만 가려 `-p 값`처럼 별도
# 옵션 인자로 준 자격을 남긴다. 그래서 줄의 낱말 가운데 안전을 확인할 수 있는 것만 그대로 두고 나머지는 ARG로 바꾼다:
# 관측된 CLI 이름(그 이름 그대로, 또는 경로의 마지막 조각이 그 이름), 셸 연산자, 값이 붙지 않은 옵션 이름(한 글자 짧은
# 옵션 `-x`, 긴 옵션 `--name`; `--name=값`은 `--name=<arg>`). 사람은 이 모양으로 그 이름이 명령 자리에서 실행됐는지 본다.
_SHORT_FLAG = re.compile(r"-[A-Za-z]\Z")
_LONG_FLAG = re.compile(r"(--[a-z][a-z0-9-]{0,31})(=.*)?\Z", re.DOTALL)
_OPERATOR = re.compile(r"[;&|<>()]+\Z")


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
    lexer = shlex.shlex(line, posix=True, punctuation_chars=";&|<>()")
    lexer.whitespace_split = True
    lexer.commenters = ""
    try:
        tokens = list(lexer)
    except ValueError:
        return None
    out = []
    for token in tokens:
        long_flag = _LONG_FLAG.match(token)
        if token in wanted or _OPERATOR.match(token) or _SHORT_FLAG.match(token):
            out.append(token)
        elif "/" in token and token.rsplit("/", 1)[1] in wanted:
            out.append("<path>/" + token.rsplit("/", 1)[1])
        elif long_flag:
            out.append(long_flag.group(1) + ("" if long_flag.group(2) is None else "=" + ARG))
        else:
            out.append(ARG)
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
        if len(command.encode("utf-8")) > MAX_SHAPE_INPUT_BYTES:
            out.append({"names": seen, "shape": None, "withheld": WITHHELD_TOO_LONG})
            continue
        shape = command_shape(pick_line(command, seen), seen)
        out.append({"names": seen, "shape": clip_shape(shape)} if shape is not None
                   else {"names": seen, "shape": None, "withheld": WITHHELD_UNPARSEABLE})
    return out, observed
