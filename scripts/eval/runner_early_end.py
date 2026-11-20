"""러너 상한 전에 일찍 끝난 회차의 분류(평가 프로토콜 §5.1·고정 초안 §26, 2026-11-21 사용자 결정).

분류는 셋이다. 판정은 끝 상태(제품 초안 상태·B 작업 상태)와 그 회차 모델 호출 기록을 같이 본다. 제품 초안에는 원인별 고정
코드가 아직 없어(감독 실패 사유·모델 예산 사유가 초안 칸에 안 실림) 끝 상태와 호출 근거로 가른다. 제품이 원인 고정 코드를 싣게
되면 그 코드 조건을 더한다(개발 계획 '측정 뒤 다음 단계').

1. 제공자 장애로 끝남(`provider_fault_end`, 무효, 제공자 멈춤과 같은 부류라 쪽별 상한에 같이 센다): 러너 상한 전에 O 제품이
   `failed`·`unknown`으로(B는 `failed`로) 끝났고, 끝 직전 마지막 모델 호출이 제공자 오류(위쪽 끊김·429·5xx)이며, 그 오류 뒤에
   성공한 모델 호출이 없다. 근거 호출이 없거나(합성 회차처럼 호출 기록이 없음) 마지막 호출이 제공자 오류가 아니면 빼지 않는다.
2. 설정 오류(`config_end`, 시도로 세지 않고 세션 멈춤): 모델 호출이 401·402·403·404를 받음(키·크레딧·권한·모델 이름),
   제품이 승인 예산 상한에 닿아 `budget_limited`로 마감(manifest 요청·비용 상한과 승인 장부 대조), 시작 거부(제품 고정 사유
   `approval_expired`·`approval_required` 등).
3. 그 밖에 일찍 끝남(`ended_valid`): 무효로 빼지 않고 유효로 둔다. 실제 끝 상태·사유는 first_limit(`engine_ended:<상태>`)과 이
   기록에 남는다.

기록에는 고정 칸만 남긴다(경로·본문·키 없음). 근거 호출은 순번·결정·상태·끝 모양·시각만이다.
"""

from __future__ import annotations

import re

from runner_fault import UPSTREAM_CUT_ENDS, FaultRecordError, _ns

EARLY_END_FORMAT = "abc2lab-eval-runner-early-end-v1"
EARLY_END_NAME = "early-end.json"
PROVIDER_FAULT_END_REASON = "provider_fault_end"
CONFIG_END_REASON = "config_end"
CLASS_PROVIDER_FAULT, CLASS_CONFIG, CLASS_ENDED_VALID = "provider_fault", "config", "ended_valid"
# 러너 상한 전에 끝난 회차의 first_limit 앞부분(runner.first_limit). 정상 완료(engine_completed)는 일찍 끝남이 아니다.
ENGINE_ENDED_PREFIX = "engine_ended:"
PRODUCT_BUDGET_LIMIT = "product_limit:budget_limited"
# 제공자 장애 끝으로 볼 수 있는 끝 상태. O는 제품 초안 상태, B는 원본 작업 상태다(B는 unknown이 없다).
PROVIDER_FAULT_END_STATES = {"baseline": frozenset({"failed"}), "ours": frozenset({"failed", "unknown"})}
# 위쪽 응답을 받았거나 위쪽이 끊긴 행. 봉인 때 게이트웨이가 끊은 행(cancelled_by_seal)과 호출자가 먼저 닫은 행은 제공자 오류가 아니다.
_PROVIDER_DECISIONS = frozenset({"forwarded", "upstream_error"})
_RATE_LIMITED = 429
_SERVER_ERRORS = range(500, 600)
_SUCCESS = range(200, 300)
# 키·크레딧·권한·모델 이름 문제(OpenRouter 401 잘못된 키, 402 크레딧, 403 권한·검열, 404 모델·엔드포인트 없음).
CONFIG_STATUSES = frozenset({401, 402, 403, 404})
# 403은 OpenRouter 검열(입력이 flagged)일 수도 있다. 검열은 내용 때문이라 키·manifest를 고쳐도 사라지지 않는다. 호출 기록에는
# 응답 본문이 없어 둘을 가르지 못하므로, 403이면 근거에 검열 가능 표시를 남겨 측정 뒤 검토 때 사람이 구분하게 한다(#831 리뷰).
MODERATION_POSSIBLE_STATUS = 403
# 제품 시작 전 거부 가운데 설정 문제인 고정 사유(diagnosisprep service_linux·url_preparation_runtime). busy·capacity·
# unavailable 같은 일시 거부는 넣지 않는다(시작 실패 무효로 센다).
START_CONFIG_CODES = frozenset({"approval_expired", "approval_required", "approval_changed", "budget_exhausted",
                                "target_not_approved", "path_not_approved", "target_approval_expired"})
_PRODUCT_REASON = re.compile(r":([a-z][a-z0-9_]{0,63})\Z")
_CALL_FIELDS = ("seq", "decision", "status", "unfinished_end", "started_at", "completed_at")


def provider_error_kind(row: dict) -> str | None:
    """모델 호출 행 하나가 제공자 오류인지(끊김·429·5xx). 아니면 None."""
    if row.get("decision") not in _PROVIDER_DECISIONS:
        return None
    if row["decision"] == "upstream_error" and row.get("unfinished_end") in UPSTREAM_CUT_ENDS:
        return "upstream_cut"
    if row.get("status") == _RATE_LIMITED:
        return "rate_limited"
    if row.get("status") in _SERVER_ERRORS:
        return "server_error"
    return None


def _succeeded(row: dict) -> bool:
    return row.get("decision") == "forwarded" and row.get("status") in _SUCCESS


def _call(row: dict) -> dict:
    return {name: row.get(name) for name in _CALL_FIELDS}


def start_refusal_code(error_code: str) -> str | None:
    """제품 구동기 오류 code(`product_rejected:start:approval_required`, `product_draft_not_ready:approval_expired` 등)의 끝에
    붙은 제품 고정 사유가 설정 문제면 그 사유. 아니면 None."""
    matched = _PRODUCT_REASON.search(error_code) if isinstance(error_code, str) else None
    return matched.group(1) if matched and matched.group(1) in START_CONFIG_CODES else None


def start_refusal_record(side: str, code: str) -> dict:
    """시작 거부(설정 오류) 기록. 회차가 시작되지 않아 끝 상태·호출 근거는 없다."""
    return {"format": EARLY_END_FORMAT, "side": side, "first_limit": None, "end_state": None, "reason_code": code,
            "class": CLASS_CONFIG, "reason": CONFIG_END_REASON, "evidence": {"kind": "start_refused", "code": code}}


def approval_budget(totals: dict | None, max_requests: int | None, max_cost_micros: int | None) -> dict | None:
    """승인 장부 합계(runner_product_llm.ledger_totals)와 manifest 상한으로 승인 예산에 닿았는지. 제품 전송 계층은 다음 호출의
    예약(요청 1·비용)이 남은 몫을 넘으면 거절한다(llmpolicy openrouter.go). 남은 비용이 이 회차에서 가장 큰 예약보다 작으면
    같은 크기의 다음 호출이 들어가지 않으므로 닿은 것으로 본다. 장부나 상한이 없으면(합성 회차) None."""
    if totals is None or max_requests is None or max_cost_micros is None:
        return None
    requests_reached = totals["requests"] >= max_requests
    cost_reached = totals["largest_reservation_cost"] > 0 and max_cost_micros - totals["cost"] < totals["largest_reservation_cost"]
    return {"requests": totals["requests"], "max_requests": max_requests, "cost_usd_micros": totals["cost"],
            "max_cost_usd_micros": max_cost_micros, "largest_reservation_usd_micros": totals["largest_reservation_cost"],
            "reached": requests_reached or cost_reached}


def early_end_record(side: str, limit: dict, first_terminal: dict | None, llm_rows: list[dict] | None,
                     budget: dict | None = None, rows_error: str | None = None) -> dict:
    """회차 하나의 분류 기록. llm_rows는 그 회차 모델 호출 행(B 게이트웨이 행, O 제품 호출 기록 줄)이고, 없으면(합성 O) None이다.
    rows_error는 행을 읽지 못한 고정 코드다. 행·시각을 읽지 못하면 설정 오류·제공자 장애의 근거가 없는 것이라 그 분류를 하지
    않는다(근거 없이 무효로 빼지 않는다, 2026-11-21 사용자). 읽기 실패 자체는 기록에 남는다."""
    status = None if first_terminal is None else first_terminal.get("status")
    value = limit["value"]
    record = {"format": EARLY_END_FORMAT, "side": side, "first_limit": value, "end_state": status,
              "reason_code": None if first_terminal is None else first_terminal.get("reason_code"),
              "class": None, "reason": None, "evidence": None}
    rows, terminal_ns = None, None
    if llm_rows is not None and rows_error is None:
        try:
            rows = sorted(llm_rows, key=lambda row: (_ns(row["completed_at"]), row.get("seq") or 0))
            terminal_ns = None if first_terminal is None else _ns(first_terminal["at"])
        except (FaultRecordError, KeyError, TypeError, AttributeError):
            rows, rows_error = None, "call_records_unreadable"
    if rows is not None:
        auth = next((row for row in rows if row.get("decision") in _PROVIDER_DECISIONS and row.get("status") in CONFIG_STATUSES), None)
        if auth is not None:
            return dict(record, **{"class": CLASS_CONFIG, "reason": CONFIG_END_REASON,
                                   "evidence": {"kind": "model_auth_status", "call": _call(auth),
                                                "moderation_possible": auth.get("status") == MODERATION_POSSIBLE_STATUS}})
    if value == PRODUCT_BUDGET_LIMIT and budget is not None and budget["reached"]:
        return dict(record, **{"class": CLASS_CONFIG, "reason": CONFIG_END_REASON,
                               "evidence": dict(budget, kind="approval_budget")})
    if not value.startswith(ENGINE_ENDED_PREFIX):
        return record if rows_error is None else dict(record, evidence={"kind": rows_error})
    record["class"] = CLASS_ENDED_VALID
    if rows is None:
        return dict(record, evidence={"kind": rows_error or "no_call_records"})
    if status not in PROVIDER_FAULT_END_STATES[side]:
        return dict(record, evidence={"kind": "end_state_not_fault"})
    before = [row for row in rows if _ns(row["completed_at"]) <= terminal_ns]
    if not before:
        return dict(record, evidence={"kind": "no_call_before_end"})
    last = before[-1]
    kind = provider_error_kind(last)
    last_ns = _ns(last["completed_at"])
    success_after = any(_succeeded(row) and _ns(row["completed_at"]) > last_ns for row in rows)
    evidence = {"kind": kind or "last_call_not_provider_error", "call": _call(last), "success_after": success_after}
    if kind is None or success_after:
        return dict(record, evidence=evidence)
    return dict(record, **{"class": CLASS_PROVIDER_FAULT, "reason": PROVIDER_FAULT_END_REASON, "evidence": evidence})


__all__ = ["EARLY_END_FORMAT", "EARLY_END_NAME", "PROVIDER_FAULT_END_REASON", "CONFIG_END_REASON", "CLASS_PROVIDER_FAULT",
           "CLASS_CONFIG", "CLASS_ENDED_VALID", "approval_budget", "early_end_record", "provider_error_kind",
           "start_refusal_code", "start_refusal_record", "FaultRecordError"]
