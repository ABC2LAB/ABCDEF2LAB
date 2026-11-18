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


OBSERVED_LINES_FORMAT = "abc2lab.cli-observed-lines/v1"
MAX_OBSERVED_LINES = 64
MAX_OBSERVED_LINE_CHARS = 1000
# 가림에 넘기는 한 줄의 바이트 상한(계측기 redact-text maxRedactLineBytes와 같은 값). 이보다 긴 줄만 여기서 자른다.
# 가린 뒤 MAX_OBSERVED_LINE_CHARS로 다시 자르므로, 이 경계에 걸린 자격 조각은 남지 않는다(#736 리뷰).
MAX_REDACT_LINE_BYTES = 64 * 1024
_TRUNCATED_MARK = " [...]"


def clip_for_redaction(line: str) -> str:
    """가림 입력 상한에 맞게 바이트로 자른다(UTF-8 글자 중간이면 그 글자를 뺀다)."""
    data = line.encode("utf-8")
    return line if len(data) <= MAX_REDACT_LINE_BYTES else data[:MAX_REDACT_LINE_BYTES].decode("utf-8", "ignore")


def clip_redacted(line: str) -> str:
    """가린 뒤의 줄을 근거 상한(MAX_OBSERVED_LINE_CHARS)으로 자른다. 자르기는 늘 가림 뒤다."""
    return line if len(line) <= MAX_OBSERVED_LINE_CHARS else line[:MAX_OBSERVED_LINE_CHARS] + _TRUNCATED_MARK


def observed_lines(records: Iterable[Mapping], names: Iterable[str]) -> tuple[list[dict], int]:
    """이름이 관측된 Bash 호출마다 원문 한 줄을 고른다(가림 전, 2026-11-19 사용자 결정 4). 줄 이음만 붙인 원문을 줄로 나눠
    정규화했을 때 관측된 이름이 나오는 첫 줄이고, 없으면 첫 줄이다. 항목은 MAX_OBSERVED_LINES까지다. 줄은 여기서
    근거 상한으로 자르지 않는다(가림 입력 상한 MAX_REDACT_LINE_BYTES만 맞춘다). 반환: ([{"names": [...], "line": 원문 한 줄}],
    관측된 호출 수). 호출자가 제품과 같은 자격 가림(계측기 redact-text)을 거친 뒤 clip_redacted로 잘라 근거에 쓴다."""
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
        lines = command.replace("\\\n", "").splitlines() or [""]
        line = next((raw for raw in lines if any(patterns[name].search(normalize(raw)) for name in seen)), lines[0])
        out.append({"names": seen, "line": clip_for_redaction(line)})
    return out, observed
