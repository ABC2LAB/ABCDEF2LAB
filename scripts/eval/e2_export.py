"""제품 E②가 내는 시도 행·manifest를 러너가 검사한다(E②–러너 연결 제안 §4). 러너 연결과 제품 생산자는 아직 없다.

네트워크·파일을 건드리지 않는 순수 코드다. 후보 형식을 따르며 생산자가 구현되면 다시 맞춘다. 여기 통과는 파일끼리와 러너가 아는 값이
맞는다는 뜻이다. 진위·최신성은 `e2-audit-verify`와 close ACK 대조로 따로 확인하고, manifest 자기 선언을 근거로 쓰지 않는다.

- 행 하나의 규칙은 `e2proxy.validJournalEvent`와 같다. 시험의 해시 대조가 그 함수의 변경을 알려 준다.
- index는 행 순서, sequence는 시도 순서라서 서로 바꿔 쓰지 않고 행 수를 요청 수로 세지 않는다.
- 거부된 시도는 result 행만 있을 수 있다. 허용된 시도는 intent 행이 먼저 있고, 그 뒤 result가 없으면 전달됐을 수 있어 허용으로 둔다.
- 시도 ID 없는 행은 control·call_closed뿐이다. 시도별 대조에 넣지 않고 따로 센다.
- 오류에는 고정 코드만 담는다. 입력 값은 담지 않는다.
"""

from __future__ import annotations

import hashlib
import json
import re
from collections import Counter
from dataclasses import dataclass
from datetime import datetime

MANIFEST_FORMAT = "abc2lab.e2-runner-manifest/v1"
# 새 회차는 v2로 쓴다. v1은 C16 전에 저장한 회차를 다시 읽을 때만 받는다(withheld·response_truncated 없음).
JOURNAL_VERSION = "e2-http-v2"
JOURNAL_VERSION_V1 = "e2-http-v1"
JOURNAL_VERSIONS = frozenset({JOURNAL_VERSION, JOURNAL_VERSION_V1})
WITHHELD = "withheld"
ATTEMPTS_FILE = "attempts.jsonl"
STATE_COMPLETE, STATE_INCOMPLETE = "complete", "incomplete"
STAGE_INTENT, STAGE_RESULT, STAGE_CONTROL, STAGE_CALL_CLOSED = "intent", "result", "control", "call_closed"
ALLOWED = "allowed"
E2_DECISIONS = frozenset({ALLOWED, "denied", "unsupported"})
E2_REASONS = frozenset({"complete", "unsupported", "policy_denied", "closed", "audit_failed", "expired", "revoked", "budget_exhausted",
                        "transport_failed", "unknown", "attempt_limit", "denial_limit", "transient_limit", "unknown_limit", "invalid",
                        WITHHELD})
HEADER_PASSTHROUGH = "evaluation_caller_passthrough"
HEADER_TEST_ACCOUNT = "test_account_passthrough"
# Go e2proxy.HeaderMode 값 전체다(형식 대조용).
E2_HEADER_MODES = frozenset({"preserve", "protected_session_proposal", HEADER_PASSTHROUGH, HEADER_TEST_ACCOUNT})
# 평가 회차가 받는 값이다. 시험 계정 모드는 제품의 프로그램 로그인(인증 준비)과 묶인 모드라 평가 조건(프로토콜 §2의 caller
# 자격 통과 모드) 밖이다.
EVAL_HEADER_MODES = E2_HEADER_MODES - {HEADER_TEST_ACCOUNT}
E2_LLM_BACKENDS = frozenset({"local", "api"})
TARGET_PUBLIC_PRACTICE = "public_practice"
_SCOPE_KEYS = frozenset({"RunRef", "TaskRef", "CaseRef", "ResetRef", "PolicyRef"})
_REQUIRED_ROW = frozenset({"index", "stage", "intent_id", "version", "scope", "policy_revision", "sequence", "reason", "forward_attempted",
                           "request_written", "response_received", "response_complete", "not_sent", "audit_complete", "at"})
_OPTIONAL_TEXT = ("call_ref", "command_ref", "attempt_id", "rule_ref", "method", "decision")
_OPTIONAL_ROW = frozenset(_OPTIONAL_TEXT) | {"status_code", "response_truncated"}
_TRANSIT_BITS = ("forward_attempted", "request_written", "response_received", "response_complete")
_ROW_BITS = _TRANSIT_BITS + ("not_sent", "audit_complete")
# evaluation은 평가 전달 모드에서만 있다. 없음과 빈 선언을 가른다.
_REQUIRED_MANIFEST = frozenset({"format", "run_ref", "eval_run_id", "model_tag", "reset_generation", "reservation_ref", "engine_task_id", "scope",
                                "policy_revision", "policy_fingerprint_sha256", "header_mode", "state", "attempts_file", "attempts_sha256", "rows",
                                "last_event_index", "last_attempt_sequence", "audit_journal_file", "audit_checkpoint_file",
                                "audit_checkpoint_sha256", "close_request_ref"})
_MANIFEST_FIELDS = _REQUIRED_MANIFEST | {"evaluation"}
_EVALUATION_FIELDS = frozenset({"run_ref", "llm_backend", "target_kind"})
_SHA256 = re.compile(r"[0-9a-f]{64}\Z")
_ATTEMPT_ID = re.compile(r"[0-9a-f]{32}\Z")
_FILE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}\Z")
# journalMaxRefBytes·journalMaxMethodBytes와 같은 상한이다.
_REF = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}\Z")
_METHOD = re.compile(r"[A-Z-]{0,16}\Z")
_TIME = re.compile(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})\Z")
_ZERO_TIME = "0001-01-01T00:00:00"
_STATUS_MIN, _STATUS_MAX = 100, 599
MAX_INT = (1 << 63) - 1


class E2ExportError(ValueError):
    """고정 코드만 담는다."""

    def __init__(self, code: str):
        super().__init__("e2_export: " + code)
        self.code = code


def _fail(code: str) -> None:
    raise E2ExportError(code) from None


@dataclass(frozen=True)
class E2Expectation:
    """러너가 기동 때 정한 값. manifest가 말하는 값이 아니라 이 값과 대조한다."""
    eval_run_id: str
    model_tag: str
    reset_generation: str
    reservation_ref: str
    engine_task_id: str
    policy_fingerprint_sha256: str
    header_mode: str
    llm_backend: str


@dataclass(frozen=True)
class E2Export:
    state: str
    manifest: dict
    # attempt_reconcile.reconcile에 넣는 시도별 결정. 시도 하나에 한 행이다.
    decisions: tuple[dict, ...]
    rows: int
    attempts_without_result: int
    allowed_not_sent: int
    administrative_rows: int
    administrative_by_reason: tuple[tuple[str, int], ...]
    denied_by_reason: tuple[tuple[str, int], ...]

    def summary(self) -> dict:
        """서명 근거에 넣을 비민감 요약. 시도 ID·참조는 없다."""
        decided = Counter(row["decision"] for row in self.decisions)
        return {"format": "abc2lab-eval-e2-export-v1", "state": self.state, "rows": self.rows, "attempts": len(self.decisions),
                "decisions": dict(sorted(decided.items())), "attempts_without_result": self.attempts_without_result,
                "allowed_not_sent": self.allowed_not_sent, "administrative_rows": self.administrative_rows,
                "administrative_by_reason": dict(self.administrative_by_reason), "denied_by_reason": dict(self.denied_by_reason)}


def _unique_object(pairs: list[tuple[str, object]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            _fail("DUPLICATE_KEY")
        result[key] = value
    return result


def _reject_constant(_value: str) -> None:
    _fail("JSON_INVALID")


def strict_object(data: bytes) -> dict:
    """중복 키·NaN·깨진 UTF-8을 거부하는 JSON 객체 해석."""
    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=_unique_object, parse_constant=_reject_constant)
    except (UnicodeError, ValueError, RecursionError) as exc:
        if isinstance(exc, E2ExportError):
            raise
        _fail("JSON_INVALID")
    if not isinstance(value, dict):
        _fail("JSON_INVALID")
    return value


def _count(value: object) -> bool:
    return type(value) is int and 0 <= value <= MAX_INT


def _ref(value: object) -> bool:
    return isinstance(value, str) and bool(_REF.fullmatch(value)) and "://" not in value


def _time(value: object) -> bool:
    if not isinstance(value, str):
        return False
    matched = _TIME.fullmatch(value)
    if matched is None or matched.group(1) == _ZERO_TIME:
        return False
    try:
        datetime.fromisoformat(matched.group(1))
    except ValueError:
        return False
    return True


def _scope(value: object) -> dict:
    if not isinstance(value, dict) or set(value) != _SCOPE_KEYS or not all(_ref(item) for item in value.values()):
        _fail("SCOPE_INVALID")
    return value


def _check_expectation(expected: E2Expectation) -> None:
    values = (expected.eval_run_id, expected.model_tag, expected.reset_generation, expected.reservation_ref, expected.engine_task_id)
    if not all(isinstance(value, str) and value for value in values):
        _fail("EXPECTATION_INVALID")
    if not isinstance(expected.policy_fingerprint_sha256, str) or not _SHA256.fullmatch(expected.policy_fingerprint_sha256):
        _fail("EXPECTATION_INVALID")
    if expected.header_mode not in EVAL_HEADER_MODES or expected.llm_backend not in E2_LLM_BACKENDS:
        _fail("EXPECTATION_INVALID")


def parse_manifest(data: bytes, expected: E2Expectation) -> dict:
    """형식과 러너가 아는 값을 대조한다. 다르면 설정 위반이다(프로토콜 §2). 파일 이름은 실행 디렉터리 안의 한 단계만 받는다."""
    _check_expectation(expected)
    manifest = strict_object(data)
    if not _REQUIRED_MANIFEST <= set(manifest) or set(manifest) - _MANIFEST_FIELDS:
        _fail("MANIFEST_FIELDS")
    if manifest["format"] != MANIFEST_FORMAT:
        _fail("MANIFEST_FORMAT")
    for name in ("eval_run_id", "model_tag", "reset_generation", "reservation_ref", "engine_task_id", "header_mode"):
        if manifest[name] != getattr(expected, name):
            _fail(name.upper() + "_MISMATCH")
    if manifest["policy_fingerprint_sha256"] != expected.policy_fingerprint_sha256:
        _fail("POLICY_FINGERPRINT_MISMATCH")
    scope = _scope(manifest["scope"])
    if scope["RunRef"] != manifest["run_ref"]:
        _fail("RUN_REF_MISMATCH")
    # TaskRef는 실제 서버 task ID다. 회차 참조로 뜻을 바꾸지 않는다.
    if scope["TaskRef"] != expected.engine_task_id:
        _fail("TASK_REF_MISMATCH")
    if manifest["run_ref"] == manifest["eval_run_id"]:
        _fail("RUN_REF_IS_EVAL_RUN")
    if not _ref(manifest["policy_revision"]):
        _fail("POLICY_REVISION_INVALID")
    _check_evaluation(manifest, expected)
    if manifest["state"] not in (STATE_COMPLETE, STATE_INCOMPLETE):
        _fail("STATE_INVALID")
    if manifest["attempts_file"] != ATTEMPTS_FILE:
        _fail("ATTEMPTS_FILE_INVALID")
    for name in ("audit_journal_file", "audit_checkpoint_file"):
        if not isinstance(manifest[name], str) or not _FILE_NAME.fullmatch(manifest[name]):
            _fail("FILE_NAME_INVALID")
    for name in ("attempts_sha256", "audit_checkpoint_sha256"):
        if not isinstance(manifest[name], str) or not _SHA256.fullmatch(manifest[name]):
            _fail("DIGEST_INVALID")
    for name in ("rows", "last_event_index", "last_attempt_sequence"):
        if not _count(manifest[name]):
            _fail("COUNT_INVALID")
    if not _ref(manifest["close_request_ref"]):
        _fail("CLOSE_REQUEST_INVALID")
    return manifest


def _check_evaluation(manifest: dict, expected: E2Expectation) -> None:
    evaluation = manifest.get("evaluation")
    if expected.header_mode != HEADER_PASSTHROUGH:
        if evaluation is not None:
            _fail("EVALUATION_UNEXPECTED")
        return
    if not isinstance(evaluation, dict) or set(evaluation) != _EVALUATION_FIELDS:
        _fail("EVALUATION_INVALID")
    if evaluation["run_ref"] != manifest["run_ref"]:
        _fail("EVALUATION_RUN_REF_MISMATCH")
    if evaluation["llm_backend"] != expected.llm_backend:
        _fail("EVALUATION_BACKEND_MISMATCH")
    if evaluation["target_kind"] != TARGET_PUBLIC_PRACTICE:
        _fail("EVALUATION_TARGET_MISMATCH")


def _check_stage(row: dict) -> None:
    stage, decision, attempt, call_ref = row["stage"], row.get("decision", ""), row.get("attempt_id", ""), row.get("call_ref", "")
    if stage in (STAGE_INTENT, STAGE_RESULT):
        if row["intent_id"] <= 0 or row["sequence"] == 0 or not _ref(call_ref):
            _fail("ROW_ATTEMPT_FIELDS")
        if not _ATTEMPT_ID.fullmatch(attempt):
            _fail("ROW_ATTEMPT_ID")
        if decision not in E2_DECISIONS:
            _fail("ROW_DECISION")
        if decision == ALLOWED and not row.get("rule_ref"):
            _fail("ROW_RULE_MISSING")
        if decision != ALLOWED and not row["not_sent"]:
            _fail("ROW_DENIED_SENT")
        if stage == STAGE_INTENT and (decision != ALLOWED or any(row[name] for name in _TRANSIT_BITS)):
            _fail("ROW_INTENT_INVALID")
    elif stage in (STAGE_CONTROL, STAGE_CALL_CLOSED):
        if attempt or row["sequence"] != 0 or decision:
            _fail("ROW_ADMINISTRATIVE_FIELDS")
        if row["intent_id"] < 0 or (stage == STAGE_CALL_CLOSED and (row["intent_id"] == 0 or not _ref(call_ref))):
            _fail("ROW_ADMINISTRATIVE_FIELDS")
    else:
        _fail("ROW_STAGE")


def _check_observation(row: dict) -> None:
    if (row["not_sent"] and row["forward_attempted"]) or (row["request_written"] and not row["forward_attempted"]) or \
            (row["response_received"] and not row["forward_attempted"]) or (row["response_complete"] and not row["response_received"]) or \
            (row["forward_attempted"] and row.get("decision", "") != ALLOWED):
        _fail("ROW_BITS_INCONSISTENT")
    # 보류(v2)는 허용해서 보냈고 대상이 답했지만 표현을 주지 않은 결과다. 자른 응답 표시는 보류에만 붙는다.
    truncated = row.get("response_truncated", False)
    if type(truncated) is not bool or ("response_truncated" in row and not truncated):
        _fail("ROW_BITS")
    # call_closed 행은 call의 마지막 사유를 요약으로 실으므로 withheld일 수 있다.
    withheld = row["reason"] == WITHHELD
    if (withheld or truncated) and row["version"] == JOURNAL_VERSION_V1:
        _fail("ROW_REASON")
    if (withheld and row["stage"] == STAGE_INTENT) or (truncated and (row["stage"] != STAGE_RESULT or not withheld)) or \
            (withheld and row["stage"] == STAGE_RESULT and (row.get("decision", "") != ALLOWED or not row["response_received"] or row["response_complete"])):
        _fail("ROW_WITHHELD_INVALID")
    status = row.get("status_code", 0)
    if type(status) is not int:
        _fail("ROW_STATUS")
    if row["response_received"]:
        if not _STATUS_MIN <= status <= _STATUS_MAX:
            _fail("ROW_STATUS")
    elif status != 0:
        _fail("ROW_STATUS")


def _row(line: bytes, manifest: dict) -> dict:
    row = strict_object(line)
    keys = set(row)
    if not _REQUIRED_ROW <= keys or keys - _REQUIRED_ROW - _OPTIONAL_ROW:
        _fail("ROW_FIELDS")
    if row["version"] not in JOURNAL_VERSIONS:
        _fail("ROW_VERSION")
    if not _count(row["index"]) or row["index"] == 0 or not _count(row["sequence"]) or type(row["intent_id"]) is not int:
        _fail("ROW_COUNT_INVALID")
    if abs(row["intent_id"]) > MAX_INT:
        _fail("ROW_COUNT_INVALID")
    if row["reason"] not in E2_REASONS:
        _fail("ROW_REASON")
    if any(type(row[name]) is not bool for name in _ROW_BITS):
        _fail("ROW_BITS")
    if not _time(row["at"]):
        _fail("ROW_TIME")
    if any(not isinstance(row.get(name, ""), str) for name in _OPTIONAL_TEXT):
        _fail("ROW_TEXT")
    if any(row.get(name, "") and not _ref(row[name]) for name in ("call_ref", "command_ref", "rule_ref")):
        _fail("ROW_REF")
    if not _METHOD.fullmatch(row.get("method", "")):
        _fail("ROW_METHOD")
    _check_stage(row)
    _check_observation(row)
    # 모든 행이 같은 실행·정책에 묶여야 한다.
    if _scope(row["scope"]) != manifest["scope"]:
        _fail("ROW_SCOPE_MISMATCH")
    if row["policy_revision"] != manifest["policy_revision"]:
        _fail("ROW_POLICY_MISMATCH")
    return row


def _attempt_final(rows: list[dict]) -> tuple[dict, bool]:
    """시도의 마지막 행과 result 유무. 단계마다 한 행, intent가 먼저, 같은 시도 결속이어야 한다."""
    stages = [row["stage"] for row in rows]
    if stages not in ([STAGE_RESULT], [STAGE_INTENT], [STAGE_INTENT, STAGE_RESULT]):
        _fail("ATTEMPT_ROWS_INVALID")
    if len({(row["sequence"], row["call_ref"], row["intent_id"]) for row in rows}) != 1:
        _fail("ATTEMPT_BINDING_MISMATCH")
    final = rows[-1]
    # 허용은 intent 저장이 끝난 뒤에만 나간다. 허용 result만 있으면 앞 행이 빠진 것이다.
    if stages == [STAGE_RESULT] and final["decision"] == ALLOWED:
        _fail("ATTEMPT_ALLOWED_WITHOUT_INTENT")
    return final, final["stage"] == STAGE_RESULT


def parse_attempts(data: bytes, manifest: dict) -> E2Export:
    """manifest가 고정한 바이트·행 수·순번과 시도 행을 대조해 시도별 결정으로 묶는다."""
    if hashlib.sha256(data).hexdigest() != manifest["attempts_sha256"]:
        _fail("ATTEMPTS_DIGEST_MISMATCH")
    if data and not data.endswith(b"\n"):
        _fail("ATTEMPTS_TRUNCATED")
    rows = [_row(line, manifest) for line in data.split(b"\n")[:-1]] if data else []
    if len(rows) != manifest["rows"]:
        _fail("ROW_COUNT_MISMATCH")
    # 한 저널은 한 판으로만 쓴다.
    if len({row["version"] for row in rows}) > 1:
        _fail("ROW_VERSION_MIXED")
    # index는 저널 행 순서다. 빠진 행이 있으면 대조가 부족해 보이지 않게 막는다.
    if [row["index"] for row in rows] != list(range(1, len(rows) + 1)):
        _fail("ROW_INDEX_GAP")
    if (rows[-1]["index"] if rows else 0) != manifest["last_event_index"]:
        _fail("LAST_INDEX_MISMATCH")
    by_attempt: dict[str, list[dict]] = {}
    administrative: Counter[str] = Counter()
    for row in rows:
        if row["stage"] in (STAGE_INTENT, STAGE_RESULT):
            by_attempt.setdefault(row["attempt_id"], []).append(row)
        else:
            administrative[row["reason"]] += 1
    decisions, without_result, allowed_not_sent = [], 0, 0
    denied: Counter[str] = Counter()
    sequences = set()
    for attempt, group in by_attempt.items():
        final, has_result = _attempt_final(group)
        if final["sequence"] in sequences:
            _fail("SEQUENCE_REPEATED")
        sequences.add(final["sequence"])
        decisions.append({"attempt_id": attempt, "decision": final["decision"]})
        if not has_result:
            without_result += 1
        elif final["decision"] == ALLOWED:
            allowed_not_sent += 1 if final["not_sent"] else 0
        else:
            denied[final["reason"]] += 1
    if (max(sequences) if sequences else 0) != manifest["last_attempt_sequence"]:
        _fail("LAST_SEQUENCE_MISMATCH")
    # complete는 result 기록까지 마쳤다는 선언이다(제안 §4.2).
    if manifest["state"] == STATE_COMPLETE and without_result:
        _fail("COMPLETE_WITH_OPEN_ATTEMPT")
    return E2Export(manifest["state"], manifest, tuple(decisions), len(rows), without_result, allowed_not_sent,
                    sum(administrative.values()), tuple(sorted(administrative.items())), tuple(sorted(denied.items())))


def parse_export(manifest_data: bytes, attempts_data: bytes, expected: E2Expectation) -> E2Export:
    return parse_attempts(attempts_data, parse_manifest(manifest_data, expected))
