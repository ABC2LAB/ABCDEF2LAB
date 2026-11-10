"""제품 E② 회차의 프롬프트 관측을 읽어 한 회차의 식별값으로 줄인다(docs/contracts/e2-prompt-projection.md).

`complete`인 투영만 비교 입력이다. 그 밖의 상태·형식 오류는 비교 성공으로 치지 않고, 수집 중만 다시 물어볼 수 있다.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass

from e2_prompt_identity import PromptIdentity, PromptIdentityError, run_prompt_identity

PROJECTION_FORMAT = "abc2lab.e2-prompt-projection/v1"
PROMPT_VERSION = "e2-worker-prompt/v1"
PROMPT_KEY = "e2_worker"
# 서버(e2PromptMaxResponseBytes)와 원장(e2prompt.MaxObservations)의 상한과 같다.
MAX_PROJECTION_BYTES = 8 << 20
MAX_OBSERVATIONS = 10_000
STATE_COLLECTING, STATE_COMPLETE = "collecting", "complete"
_STATES = frozenset({STATE_COLLECTING, STATE_COMPLETE, "incomplete", "empty", "invalid", "conflict"})
_TOP_FIELDS = frozenset({"format", "task_id", "exploration_id", "run_ref", "state", "complete", "expected_count", "observed_count", "snapshot_sequence", "observations"})
_OBSERVATION_FIELDS = frozenset({"sequence", "activity_id", "intent_id", "worker", "version", "key", "source", "template_digest", "guidance_digest", "control_digest", "tools_digest"})
_WORKER = re.compile(r"work#[1-9][0-9]{0,5}\Z")


class E2PromptNotReady(RuntimeError):
    """시한 안에 다시 물어봐도 되는 상태. 고정 코드만 담는다."""

    def __init__(self, code: str):
        super().__init__("e2_prompt: " + code)
        self.code = code


class E2PromptUnverified(RuntimeError):
    """이 회차의 프롬프트 동일성을 확인할 수 없다. 고정 코드만 담는다."""

    def __init__(self, code: str):
        super().__init__("e2_prompt: " + code)
        self.code = code


@dataclass(frozen=True)
class E2PromptResult:
    exploration_id: int
    expected_count: int
    projection_sha256: str
    identity: PromptIdentity


def _no_duplicates(pairs: list) -> dict:
    document: dict = {}
    for key, value in pairs:
        if key in document:
            raise ValueError("duplicate key")
        document[key] = value
    return document


def _reject_constant(_: str) -> None:
    raise ValueError("non-finite number")


def _integer(value: object, low: int, high: int) -> bool:
    return type(value) is int and low <= value <= high


def _invalid() -> E2PromptUnverified:
    return E2PromptUnverified("projection_invalid")


def _load(raw: bytes) -> dict:
    if len(raw) > MAX_PROJECTION_BYTES:
        raise _invalid()
    try:
        document = json.loads(raw.decode("utf-8"), object_pairs_hook=_no_duplicates, parse_constant=_reject_constant)
    except (UnicodeError, ValueError, RecursionError):
        raise _invalid() from None
    if not isinstance(document, dict) or set(document) != _TOP_FIELDS:
        raise _invalid()
    return document


def _check_observations(rows: list) -> None:
    activities = set()
    for number, row in enumerate(rows, start=1):
        if (not isinstance(row, dict) or set(row) != _OBSERVATION_FIELDS or type(row["sequence"]) is not int or row["sequence"] != number
                or not _integer(row["activity_id"], 1, 2**63 - 1) or row["activity_id"] in activities or not _integer(row["intent_id"], 1, 2**63 - 1)
                or not isinstance(row["worker"], str) or not _WORKER.fullmatch(row["worker"]) or row["version"] != PROMPT_VERSION or row["key"] != PROMPT_KEY):
            raise _invalid()
        activities.add(row["activity_id"])


def parse_projection(raw: bytes, task_id: str, run_ref: str) -> E2PromptResult:
    """`task_id`·`run_ref`는 러너가 아는 예상값이다. 응답에서 채우지 않는다."""
    document = _load(raw)
    expected, observed, snapshot = document["expected_count"], document["observed_count"], document["snapshot_sequence"]
    state, rows = document["state"], document["observations"]
    if (document["format"] != PROJECTION_FORMAT or document["task_id"] != int(task_id) or type(document["task_id"]) is not int
            or not _integer(document["exploration_id"], 1, 2**63 - 1) or document["run_ref"] != run_ref or state not in _STATES
            or type(document["complete"]) is not bool or document["complete"] != (state == STATE_COMPLETE) or not _integer(expected, 0, MAX_OBSERVATIONS)
            or not _integer(observed, 0, expected) or snapshot != expected or type(snapshot) is not int or not isinstance(rows, list)):
        raise _invalid()
    if state == STATE_COLLECTING:
        raise E2PromptNotReady(STATE_COLLECTING)
    if state != STATE_COMPLETE:
        raise E2PromptUnverified("state_" + state)
    if expected < 1 or observed != expected or len(rows) != expected:
        raise _invalid()
    _check_observations(rows)
    try:
        identity = run_prompt_identity(rows)
    except PromptIdentityError as exc:
        raise E2PromptUnverified(exc.code) from None
    return E2PromptResult(document["exploration_id"], expected, hashlib.sha256(raw).hexdigest(), identity)
