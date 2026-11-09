"""개선 후 회차끼리 E② 워커 프롬프트가 같았는지 가린다(프로토콜 §2, #312).

입력은 엔진의 프롬프트 생성 지점에서 나온 관측 행이다. 평가용 전달 형식은 아직 합의 전이라 여기서는 행에서
다섯 값만 읽는다. `system_digest`는 intent가 들어가서 비교하지 않는다.
한 회차의 관측을 최신 한 건으로 줄이지 않는다. 0건·누락·상충은 비교 성공으로 치지 않는다.
"""

from __future__ import annotations

import re
from collections.abc import Iterable, Mapping, Sequence

PROMPT_FIELDS = ("source", "template_digest", "guidance_digest", "control_digest", "tools_digest")
PROMPT_SOURCES = frozenset({"builtin", "override", "builtin_invalid_override"})
_DIGEST = re.compile(r"[0-9a-f]{64}\Z")

PromptIdentity = tuple[str, str, str, str, str]


class PromptIdentityError(ValueError):
    """고정 코드만 담는다. 관측값은 담지 않는다."""

    def __init__(self, code: str):
        super().__init__("e2_prompt_identity: " + code)
        self.code = code


def _identity(row: object) -> PromptIdentity:
    if not isinstance(row, Mapping):
        raise PromptIdentityError("observation_invalid")
    if any(name not in row for name in PROMPT_FIELDS):
        raise PromptIdentityError("field_missing")
    source = row["source"]
    if not isinstance(source, str) or source not in PROMPT_SOURCES:
        raise PromptIdentityError("source_invalid")
    digests = [row[name] for name in PROMPT_FIELDS[1:]]
    if not all(isinstance(value, str) and _DIGEST.fullmatch(value) for value in digests):
        raise PromptIdentityError("digest_invalid")
    return (source, digests[0], digests[1], digests[2], digests[3])


def run_prompt_identity(observations: Sequence[Mapping]) -> PromptIdentity:
    """한 회차의 관측 전부가 같은 프롬프트를 가리킬 때만 그 식별값을 돌려준다."""
    if not observations:
        raise PromptIdentityError("none_observed")
    found = {_identity(row) for row in observations}
    if len(found) > 1:
        raise PromptIdentityError("run_conflict")
    return found.pop()


def condition_prompt_identity(runs: Iterable[Sequence[Mapping]]) -> PromptIdentity:
    """한 조건의 회차가 모두 같은 프롬프트로 돌았을 때만 그 식별값을 돌려준다."""
    found = {run_prompt_identity(observations) for observations in runs}
    if not found:
        raise PromptIdentityError("no_runs")
    if len(found) > 1:
        raise PromptIdentityError("runs_differ")
    return found.pop()
