"""제공자 장애 멈춤 기록(탐색 분리 배치 결정안 §7, 순서 4-5).

§7 무효 조건은 셋이다: (1) 모델 호출이 위쪽 스트림 끊김으로 끝났거나 429를 받았다(제공자 장애 기록), (2) 그 뒤 T초 동안
모델 호출 0·대상 요청 0, (3) 그동안 엔진 끝 신호가 없다. T와 무효 비율 상한은 파일럿 기록으로 정해 측정 전에 고정하므로,
지금은 판정하지 않고(`judged: false`) 판정에 쓸 값만 남긴다. 장애마다 그 뒤 첫 활동까지의 무활동 길이, 창 끝까지 활동이
없었는지(censored), 그 사이 엔진 끝 신호가 있었는지, 장애 시점에 진행 중이던 요청 수를 남긴다. T 후보를 고르는 근거로 창 전체의
최장 무활동 길이(진행 중인 요청이 하나도 없는 구간)도 남긴다.

활동은 게이트웨이 행(엔진의 모든 모델 호출 시도, 거부 포함)과 계측기 행(두 입구의 대상 요청 시도)의 시작이다. 시각은 모두
작업 생성 요청 직전(창 시작)부터 잰 초다. 창 끝은 러너가 계측기를 봉인한 시각이다. 판정이 쓰는 글은 저장하지 않는다(행의
경로·본문은 원래 없다).
"""

from __future__ import annotations

from datetime import datetime

FAULT_STALL_FORMAT = "abc2lab-eval-runner-fault-stall-v1"
FAULT_STALL_NAME = "fault-stall.json"
UPSTREAM_CUT_ENDS = frozenset({"upstream_cut_after_done", "upstream_cut_before_done"})
# 위쪽 응답을 받은 행(게이트웨이가 거부한 행은 위쪽 상태가 없다).
_UPSTREAM_DECISIONS = frozenset({"forwarded", "upstream_error", "cancelled_by_seal"})
_RATE_LIMITED = 429


class FaultRecordError(ValueError):
    """시각을 읽을 수 없어 기록을 만들 수 없음. 고정 코드만 담는다."""


def _at(value: object) -> datetime:
    if not isinstance(value, str):
        raise FaultRecordError("fault_stall_time_invalid")
    try:
        moment = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        raise FaultRecordError("fault_stall_time_invalid") from None
    if moment.tzinfo is None:
        raise FaultRecordError("fault_stall_time_invalid")
    return moment


def _fault_kind(row: dict) -> str | None:
    if row["decision"] == "upstream_error" and row.get("unfinished_end") in UPSTREAM_CUT_ENDS:
        return "upstream_cut"
    if row["decision"] in _UPSTREAM_DECISIONS and row["status"] == _RATE_LIMITED:
        return "rate_limited"
    return None


def _seconds(later: datetime, earlier: datetime) -> float:
    return round((later - earlier).total_seconds(), 3)


def fault_stall_record(llm_rows: list[dict], meter_rows: list[dict], window_start: str, window_end: str,
                       engine_terminal_at: str | None) -> dict:
    """게이트웨이 행·계측기 행·창 시각·엔진 첫 끝 상태 시각으로 §7 판정 재료를 만든다(판정은 하지 않음)."""
    start, end = _at(window_start), _at(window_end)
    if end < start:
        raise FaultRecordError("fault_stall_time_invalid")
    terminal = None if engine_terminal_at is None else _at(engine_terminal_at)
    spans = [(_at(row["started_at"]), _at(row["completed_at"])) for row in [*llm_rows, *meter_rows]]
    if any(done < began for began, done in spans):
        raise FaultRecordError("fault_stall_time_invalid")
    starts = sorted(began for began, _ in spans if start <= began < end)
    counts = {"upstream_cut": 0, "rate_limited": 0}
    events = []
    for row in llm_rows:
        kind = _fault_kind(row)
        at = _at(row["completed_at"])
        if kind is None or not start <= at < end:
            continue
        counts[kind] += 1
        following = next((began for began in starts if began > at), None)
        silence_end = end if following is None else following
        events.append({
            "kind": kind, "at_seconds": _seconds(at, start), "silence_seconds": _seconds(silence_end, at),
            # 창 끝까지 활동이 없었다(실제 무활동은 이보다 길 수 있다).
            "censored": following is None,
            # 장애 시점에 진행 중이던 요청(긴 명령 등). 무활동 판정을 해석할 때 본다.
            "inflight_at_fault": sum(1 for began, done in spans if began < at < done),
            "engine_terminal_within": terminal is not None and at <= terminal <= silence_end,
        })
    events.sort(key=lambda event: event["at_seconds"])
    longest = max(events, key=lambda event: event["silence_seconds"], default=None)
    return {"format": FAULT_STALL_FORMAT, "judged": False, "window_seconds": _seconds(end, start),
            "faults": counts, "events": events,
            "max_silence_after_fault_seconds": None if longest is None else longest["silence_seconds"],
            "max_silence_after_fault_censored": None if longest is None else longest["censored"],
            "max_idle_gap_seconds": _max_idle_gap(spans, start, end), "activity_starts": len(starts)}


def _max_idle_gap(spans: list[tuple[datetime, datetime]], start: datetime, end: datetime) -> float:
    """창 안에서 진행 중인 요청이 하나도 없는 가장 긴 구간(창 양 끝 포함)."""
    clipped = sorted((max(began, start), min(done, end)) for began, done in spans if began < end and done > start)
    longest, cursor = 0.0, start
    for began, done in clipped:
        if began > cursor:
            longest = max(longest, (began - cursor).total_seconds())
        cursor = max(cursor, done)
    longest = max(longest, (end - cursor).total_seconds())
    return round(longest, 3)
