"""제공자 장애 멈춤 기록(탐색 분리 배치 결정안 §7, 순서 4-5).

§7 무효 조건은 셋이다: (1) 모델 호출이 위쪽 스트림 끊김으로 끝났거나 429를 받았다(제공자 장애 기록), (2) 그 뒤 T초 동안
모델 호출 0·대상 요청 0, (3) 그동안 엔진 끝 신호가 없다. `fault_stall_record`는 판정 재료만 만든다(`judged: false`). 장애마다
그 뒤 첫 시작까지의 무활동 길이(silence), 창 끝까지 활동이 없었는지(censored), 그 사이 엔진 끝 신호가 있었는지, 장애 시점에
진행 중이던 요청 수를 남긴다. T 후보를 고르는 근거로 창 전체의 최장 무활동 길이(진행 중인 요청이 하나도 없는 구간)도 남긴다.

`judge_fault_stall`이 설정한 T로 판정한다(`judged: true`). 조건 (2)의 무활동은 장애 뒤 **다음 활동 사건까지의 길이**(idle)다
(2026-11-19 사용자 결정). 활동 사건 = 요청 시작 + 응답 완료(오류 응답 포함), 모델 호출 행과 대상 요청 행 모두, B·O 같게.
진행 중인 요청은 활동으로 세지 않는다: 그러면 제공자 쪽에서 매달린 호출을 잡지 못하는데 T의 목적이 그것이다. 스트리밍 조각도
세지 않는다(행에 조각 기록이 없고, 세면 비스트리밍인 O와 같은 길이의 호출이 B에서만 공백이 아니게 된다). T를 고를 근거로
같은 정의의 활동 중 최장 공백(`max_event_gap_before_end_seconds`)도 남긴다.

활동은 게이트웨이 행(엔진의 모든 모델 호출 시도, 거부 포함)과 계측기 행(두 입구의 대상 요청 시도)의 시작이다. 시각은 모두
작업 생성 요청 직전(창 시작)부터 잰 초다. 창 끝은 러너가 계측기를 봉인한 시각이다. 판정이 쓰는 글은 저장하지 않는다(행의
경로·본문은 원래 없다).
"""

from __future__ import annotations

import re
from datetime import datetime, timedelta, timezone

# v2: 장애별 첫 무활동 구간(idle)과 판정 칸(judged·stall_seconds·stall·stalls)을 더함
FAULT_STALL_FORMAT = "abc2lab-eval-runner-fault-stall-v2"
FAULT_STALL_REASON = "provider_fault_stall"
FAULT_STALL_UNJUDGEABLE = "fault_stall_unjudgeable"
# 판정 T의 코드 상한(초). 회차 시간 상한(runner_config.MAX_RUN_SECONDS)과 같다. 값 자체는 설정(run.fault_stall_seconds)이다.
MAX_FAULT_STALL_SECONDS = 3600
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


_STAMP_PARTS = re.compile(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})\Z")
_EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)
_NS_PER_SECOND = 10 ** 9


def _ns(value: object) -> int:
    """시각 문자열의 정확한 나노초(반올림·부동소수 없이). 판정 비교는 이 값으로 한다(#756 리뷰: 기록용 반올림으로 T 미만
    공백이 T로 보이던 것). 행 시각은 나노초 9자리까지다(게이트웨이·계측기·제품 호출 기록)."""
    matched = _STAMP_PARTS.fullmatch(value) if isinstance(value, str) else None
    if matched is None:
        raise FaultRecordError("fault_stall_time_invalid")
    zone = "+00:00" if matched.group(3) == "Z" else matched.group(3)
    try:
        whole = datetime.fromisoformat(matched.group(1) + zone)
    except ValueError:
        raise FaultRecordError("fault_stall_time_invalid") from None
    return (whole - _EPOCH) // timedelta(seconds=1) * _NS_PER_SECOND + int((matched.group(2) or "").ljust(9, "0"))


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
    # 활동 사건: 모든 행의 시작과 완료(오류 응답 포함). 진행 중 구간은 사건이 아니다. 판정용은 나노초 정수다.
    moments = sorted({moment for span in spans for moment in span})
    rows = [*llm_rows, *meter_rows]
    moments_ns = sorted({_ns(row[name]) for row in rows for name in ("started_at", "completed_at")})
    end_ns, terminal_ns = _ns(window_end), None if engine_terminal_at is None else _ns(engine_terminal_at)
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
        at_ns = _ns(row["completed_at"])
        following_ns = next((moment for moment in moments_ns if at_ns < moment < end_ns), None)
        idle_to_ns, idle_censored = (end_ns, True) if following_ns is None else (following_ns, False)
        events.append({
            "kind": kind, "at_seconds": _seconds(at, start), "silence_seconds": _seconds(silence_end, at),
            # 창 끝까지 활동이 없었다(실제 무활동은 이보다 길 수 있다).
            "censored": following is None,
            # 장애 시점에 진행 중이던 요청(긴 명령 등). 무활동 판정을 해석할 때 본다.
            "inflight_at_fault": sum(1 for began, done in spans if began < at < done),
            "engine_terminal_within": terminal is not None and at <= terminal <= silence_end,
            # 장애 뒤 다음 활동 사건(요청 시작·응답 완료)까지. 판정은 나노초 정수(idle_nanoseconds)를 T와 비교하고,
            # idle_seconds는 기록용 반올림 값이다.
            "idle_nanoseconds": idle_to_ns - at_ns, "idle_seconds": round((idle_to_ns - at_ns) / _NS_PER_SECOND, 3),
            "idle_censored": idle_censored,
            # 그 구간이 끝나기 전에 엔진이 끝났으면(먼저 끝난 경우 포함) 조건 (3)이 서지 않는다.
            "engine_terminal_before_idle_end": terminal_ns is not None and terminal_ns <= idle_to_ns,
        })
    events.sort(key=lambda event: event["at_seconds"])
    longest = max(events, key=lambda event: event["silence_seconds"], default=None)
    return {"format": FAULT_STALL_FORMAT, "judged": False, "window_seconds": _seconds(end, start),
            "faults": counts, "events": events,
            "max_silence_after_fault_seconds": None if longest is None else longest["silence_seconds"],
            "max_silence_after_fault_censored": None if longest is None else longest["censored"],
            "max_idle_gap_seconds": _max_idle_gap(spans, start, end),
            # 활동 중 최장 무활동(2026-11-19 사용자 결정 5): 엔진이 창 안에서 먼저 끝난 회차는 엔진 끝 상태까지만 잰다.
            # 끝 뒤 남은 시간은 §7 무효 조건(3)에서 빠지는 구간이라 T를 정하는 근거에 넣지 않는다.
            "max_idle_gap_before_end_seconds": _max_idle_gap(spans, start, terminal if terminal is not None and start <= terminal < end
                                                             else end),
            # 판정과 같은 정의(활동 사건 = 요청 시작 + 응답 완료)의 활동 중 최장 공백. T를 고르는 근거다.
            "max_event_gap_before_end_seconds": _max_event_gap(moments, start, terminal if terminal is not None
                                                               and start <= terminal < end else end),
            # 마지막 활동 사건부터 끝(엔진 끝 상태, 없으면 창 끝)까지. 시간 상한 마감을 '작업 중'과 '빈 계획 대기 중'으로 가르는
            # 근거다(고정 초안 §21, 2026-11-19 사용자). 활동이 없었으면 창 시작부터 잰다. T와 비교할 때는 나노초 정수
            # (trailing_event_gap_nanoseconds)를 쓰고, 초 값은 기록용 반올림이다(#794 리뷰, #756 판정과 같은 방식).
            **_trailing_event_gap(moments_ns, _ns(window_start), terminal_ns if terminal is not None and start <= terminal < end
                                  else end_ns),
            "activity_starts": len(starts)}


def _max_event_gap(moments: list[datetime], start: datetime, end: datetime) -> float:
    """창 안 활동 사건(요청 시작·응답 완료) 사이의 가장 긴 간격(창 양 끝 포함)."""
    longest, cursor = 0.0, start
    for moment in moments:
        if start <= moment < end:
            longest = max(longest, (moment - cursor).total_seconds())
            cursor = moment
    return round(max(longest, (end - cursor).total_seconds()), 3)


def _trailing_event_gap(moments_ns: list[int], start_ns: int, end_ns: int) -> dict:
    """창 안 마지막 활동 사건(요청 시작·응답 완료)부터 끝까지의 간격(나노초 정수와 기록용 초). 활동이 없으면 창 전체다."""
    last = max((moment for moment in moments_ns if start_ns <= moment < end_ns), default=start_ns)
    gap = end_ns - last
    return {"trailing_event_gap_nanoseconds": gap, "trailing_event_gap_seconds": round(gap / _NS_PER_SECOND, 3)}


def judge_fault_stall(record: dict, stall_seconds: int) -> dict:
    """설정한 T로 §7을 판정한다. 장애 뒤 다음 활동 사건까지가 T초 이상이고 그 전에 엔진 끝 신호가 없으면 그 장애는
    멈춤(stall)이다. 창이 T초 전에 끝나 무활동이 T에 못 미치면 멈춤이라고 하지 않는다. 판정 재료가 없거나 이미 판정한
    기록이면 판정하지 않는다(FaultRecordError) — 판정할 수 없는 회차를 멈춤 없음으로 두지 않는다."""
    if (type(stall_seconds) is not int or not 1 <= stall_seconds <= MAX_FAULT_STALL_SECONDS or record.get("error") is not None
            or record.get("judged") is not False or record.get("format") != FAULT_STALL_FORMAT):
        raise FaultRecordError("fault_stall_unjudgeable")
    events = [dict(event, stall=event["idle_nanoseconds"] >= stall_seconds * _NS_PER_SECOND
                   and not event["engine_terminal_before_idle_end"])
              for event in record["events"]]
    return dict(record, judged=True, stall_seconds=stall_seconds, events=events,
                stalls=sum(1 for event in events if event["stall"]))


def fault_stall_outcome(record: dict, stall_seconds: int | None) -> tuple[dict, str | None]:
    """회차에 쓸 기록과 무효 사유. T가 없으면 기록 그대로·사유 없음(판정 안 함). T가 있으면 판정하고, 멈춘 장애가 하나라도 있으면
    provider_fault_stall이다. 판정 재료를 만들지 못했으면(시각·행 오류) 멈춤이 없다고 할 수 없어 fault_stall_unjudgeable이다."""
    if stall_seconds is None:
        return record, None
    try:
        judged = judge_fault_stall(record, stall_seconds)
    except FaultRecordError:
        return dict(record, stall_seconds=stall_seconds), FAULT_STALL_UNJUDGEABLE
    return judged, (FAULT_STALL_REASON if judged["stalls"] else None)


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
