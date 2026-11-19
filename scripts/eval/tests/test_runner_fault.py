"""제공자 장애 멈춤 기록(탐색 분리 배치 결정안 §7, 4-5) 시험."""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from runner_fault import (  # noqa: E402
    FAULT_STALL_FORMAT, MAX_FAULT_STALL_SECONDS, FaultRecordError, fault_stall_outcome, fault_stall_record, judge_fault_stall,
)

START = "2026-11-17T00:00:00+00:00"


def stamp(seconds: float) -> str:
    whole, frac = divmod(round(seconds * 1000), 1000)
    minutes, sec = divmod(whole, 60)
    return f"2026-11-17T00:{minutes:02d}:{sec:02d}.{frac:03d}000000Z"  # 게이트웨이·계측기처럼 나노초 9자리


def llm(seq, began, done, decision="forwarded", status=200, end=None):
    row = {"seq": seq, "decision": decision, "method": "POST", "status": status, "started_at": stamp(began),
           "completed_at": stamp(done), "request_bytes": 10, "response_bytes": 5, "generation_id": ""}
    if end is not None:
        row["unfinished_end"] = end
    return row


def meter(seq, began, done):
    return {"seq": seq, "kind": "request", "method": "GET", "status": 200, "started_at": stamp(began),
            "completed_at": stamp(done)}


class TestFaultStallRecord(unittest.TestCase):
    def test_pilot_shaped_stall_is_recorded_not_judged(self):
        # 2026-11-16 1차 B 모양: 활동이 이어지다 위쪽 끊김 뒤 봉인까지 아무것도 없음, 끝 신호도 없음.
        rows = [llm(1, 0.5, 3.0), llm(2, 3.5, 20.0, decision="upstream_error", status=200, end="upstream_cut_before_done")]
        target = [meter(1, 4.0, 4.4), meter(2, 6.0, 6.1)]
        record = fault_stall_record(rows, target, START, stamp(379.0), None)
        self.assertEqual(record["format"], FAULT_STALL_FORMAT)
        self.assertFalse(record["judged"])
        self.assertEqual(record["faults"], {"upstream_cut": 1, "rate_limited": 0})
        self.assertEqual(record["events"], [{"kind": "upstream_cut", "at_seconds": 20.0, "silence_seconds": 359.0,
                                             "censored": True, "inflight_at_fault": 0, "engine_terminal_within": False,
                                             "idle_nanoseconds": 359_000_000_000, "idle_seconds": 359.0, "idle_censored": True,
                                             "engine_terminal_before_idle_end": False}])
        self.assertEqual((record["max_silence_after_fault_seconds"], record["max_silence_after_fault_censored"]), (359.0, True))
        self.assertEqual(record["max_idle_gap_seconds"], 359.0)
        self.assertEqual(record["max_idle_gap_before_end_seconds"], 359.0)  # 끝 신호가 없으면 창 끝까지
        self.assertEqual((record["window_seconds"], record["activity_starts"]), (379.0, 4))

    def test_fault_followed_by_activity_and_end_signal(self):
        # 429 뒤 2초 만에 다음 모델 호출(엔진 재시도). 위쪽 끊김 뒤에는 대상 요청이 이어진다. 끝 신호는 그 사이에 있다.
        rows = [llm(1, 1.0, 1.2, status=429), llm(2, 3.2, 5.0),
                llm(3, 6.0, 8.0, decision="upstream_error", end="upstream_cut_after_done")]
        target = [meter(1, 9.5, 9.6)]
        record = fault_stall_record(rows, target, START, stamp(30.0), stamp(9.0))
        self.assertEqual(record["faults"], {"upstream_cut": 1, "rate_limited": 1})
        self.assertEqual([(e["kind"], e["silence_seconds"], e["censored"], e["engine_terminal_within"]) for e in record["events"]],
                         [("rate_limited", 2.0, False, False), ("upstream_cut", 1.5, False, True)])
        self.assertEqual(record["max_silence_after_fault_seconds"], 2.0)
        self.assertEqual(record["max_idle_gap_seconds"], 20.4)  # 마지막 활동 끝(9.6) → 창 끝(30)
        # 활동 중 최장 무활동은 엔진 끝 상태(9.0)까지만 잰다: 1.2→3.2의 2.0초(끝 뒤 9.6→30은 넣지 않는다)
        self.assertEqual(record["max_idle_gap_before_end_seconds"], 2.0)

    def test_not_faults(self):
        # 호출자 쪽 끊김·게이트웨이 자신의 거부(상태가 429여도)·정상 응답은 제공자 장애가 아니다. 창 밖 장애도 세지 않는다.
        rows = [llm(1, 1.0, 2.0, decision="upstream_error", end="client_closed_before_done"),
                llm(2, 2.0, 2.1, decision="rejected_body", status=429), llm(3, 3.0, 4.0),
                llm(4, 31.0, 32.0, status=429)]
        record = fault_stall_record(rows, [], START, stamp(30.0), None)
        self.assertEqual((record["faults"], record["events"], record["max_silence_after_fault_seconds"]),
                         ({"upstream_cut": 0, "rate_limited": 0}, [], None))
        self.assertIsNone(record["max_silence_after_fault_censored"])

    def test_inflight_request_at_fault_is_counted(self):
        # 긴 대상 요청이 걸려 있는 동안 429가 오면, 무활동 길이와 별도로 진행 중 요청 수를 남긴다.
        rows = [llm(1, 5.0, 5.5, status=429)]
        target = [meter(1, 1.0, 25.0)]
        record = fault_stall_record(rows, target, START, stamp(30.0), None)
        event = record["events"][0]
        self.assertEqual((event["inflight_at_fault"], event["silence_seconds"], event["censored"]), (1, 24.5, True))
        self.assertEqual(record["max_idle_gap_seconds"], 5.0)  # 진행 중인 요청이 있던 구간은 무활동이 아니다

    def test_activity_exactly_at_fault_time_is_not_following(self):
        # 다음 활동은 장애 시각 "뒤"에 시작한 것만이다(같은 시각 시작은 장애와 겹친 시도).
        rows = [llm(1, 1.0, 2.0, status=429), llm(2, 2.0, 2.5), llm(3, 4.0, 4.5)]
        record = fault_stall_record(rows, [], START, stamp(10.0), None)
        self.assertEqual(record["events"][0]["silence_seconds"], 2.0)

    def test_terminal_outside_the_silence_is_not_within(self):
        rows = [llm(1, 1.0, 2.0, status=429), llm(2, 5.0, 6.0)]
        for terminal, within in ((1.5, False), (2.0, True), (5.0, True), (5.5, False)):
            with self.subTest(terminal=terminal):
                record = fault_stall_record(rows, [], START, stamp(10.0), stamp(terminal))
                self.assertEqual(record["events"][0]["engine_terminal_within"], within)

    def test_bad_times_are_refused_with_a_fixed_code(self):
        good = [llm(1, 1.0, 2.0)]
        for args in (([llm(1, 3.0, 2.0)], START, stamp(10.0)), (good, "not-a-time", stamp(10.0)),
                     (good, START, "2026-11-17T00:00:10"), (good, stamp(10.0), START),
                     ([dict(llm(1, 1.0, 2.0), started_at=17)], START, stamp(10.0))):
            with self.subTest(args=args[1:]), self.assertRaises(FaultRecordError) as caught:
                fault_stall_record(args[0], [], args[1], args[2], None)
            self.assertEqual(str(caught.exception), "fault_stall_time_invalid")
        with self.assertRaises(FaultRecordError):
            fault_stall_record(good, [], START, stamp(10.0), "later")

    def test_longest_silence_is_not_just_the_first_fault(self):
        # 첫 장애는 바로 다시 시도, 나중 장애 뒤가 가장 길다. 최장 값은 나중 것이다.
        rows = [llm(1, 1.0, 1.5, status=429), llm(2, 2.0, 3.0),
                llm(3, 4.0, 5.0, decision="upstream_error", end="upstream_cut_before_done"), llm(4, 12.0, 12.5)]
        record = fault_stall_record(rows, [], START, stamp(20.0), None)
        self.assertEqual([e["silence_seconds"] for e in record["events"]], [0.5, 7.0])
        self.assertEqual((record["max_silence_after_fault_seconds"], record["max_silence_after_fault_censored"]), (7.0, False))

    def test_attempts_after_the_window_do_not_end_the_silence(self):
        # 봉인 뒤 거부된 시도(창 끝 뒤 시작)는 무활동을 끝내지 않는다. 장애 뒤 창 끝까지 이어진 것으로 남긴다.
        rows = [llm(1, 1.0, 2.0, decision="upstream_error", end="upstream_cut_before_done"),
                llm(2, 10.5, 10.6, decision="rejected_after_seal", status=503)]
        target = [meter(1, 11.0, 11.0)]
        record = fault_stall_record(rows, target, START, stamp(10.0), None)
        self.assertEqual((record["events"][0]["silence_seconds"], record["events"][0]["censored"]), (8.0, True))
        self.assertEqual(record["activity_starts"], 1)

    def test_empty_round(self):
        record = fault_stall_record([], [], START, stamp(12.5), None)
        self.assertEqual((record["max_idle_gap_seconds"], record["activity_starts"], record["events"]), (12.5, 0, []))



class TestActiveGap(unittest.TestCase):
    def test_terminal_outside_the_window_falls_back_to_the_window_end(self):
        rows = [llm(1, 1.0, 2.0)]
        within = fault_stall_record(rows, [], START, stamp(30.0), stamp(5.0))
        after = fault_stall_record(rows, [], START, stamp(30.0), stamp(31.0))
        self.assertEqual((within["max_idle_gap_before_end_seconds"], within["max_idle_gap_seconds"]), (3.0, 28.0))
        self.assertEqual(after["max_idle_gap_before_end_seconds"], 28.0)  # 창 밖 끝 상태는 쓰지 않는다



class TestFaultStallJudge(unittest.TestCase):
    """설정한 T로 §7 판정(멈춤 무효). 무활동은 장애 뒤 첫 무활동 구간이다(진행 중이던 요청은 활동)."""

    PILOT = ([llm(1, 0.5, 3.0), llm(2, 3.5, 20.0, decision="upstream_error", end="upstream_cut_before_done")],
             [meter(1, 4.0, 4.4), meter(2, 6.0, 6.1)])

    def test_pilot_stall_is_judged_against_t(self):
        record = fault_stall_record(*self.PILOT, START, stamp(379.0), None)
        judged = judge_fault_stall(record, 180)
        self.assertEqual((judged["judged"], judged["stall_seconds"], judged["stalls"], judged["events"][0]["stall"]),
                         (True, 180, 1, True))
        self.assertFalse(record["judged"])  # 원래 기록은 바꾸지 않는다
        # 창이 T 전에 끝나 무활동이 T에 못 미치면(359초 < 360초) 멈춤이라고 하지 않는다
        self.assertEqual(judge_fault_stall(record, 360)["stalls"], 0)
        self.assertEqual(judge_fault_stall(record, 359)["stalls"], 1)  # 경계: T초 이상이면 멈춤

    def test_activity_is_request_start_and_response_completion(self):
        # 활동 사건 = 요청 시작 + 응답 완료(2026-11-19 사용자 결정). 진행 중 구간은 활동이 아니다.
        cut = llm(2, 8.0, 10.0, decision="upstream_error", end="upstream_cut_before_done")
        # 장애(10초) 때 진행 중이던 긴 호출(5~200초)은 완료(200초)가 다음 사건이다: 무활동 190초
        record = fault_stall_record([llm(1, 5.0, 200.0), cut, llm(3, 300.0, 301.0)], [], START, stamp(400.0), None)
        event = record["events"][0]
        self.assertEqual((event["silence_seconds"], event["idle_seconds"], event["idle_censored"], event["inflight_at_fault"]),
                         (290.0, 190.0, False, 1))
        self.assertEqual(judge_fault_stall(record, 180)["stalls"], 1)
        self.assertEqual(judge_fault_stall(record, 191)["stalls"], 0)
        # 제공자 쪽에서 매달린 호출(창이 끝날 때까지 완료 없음): 진행 중이어도 활동이 아니라 멈춤으로 잡힌다
        hung = fault_stall_record([llm(1, 5.0, 500.0), cut], [], START, stamp(400.0), None)["events"][0]
        self.assertEqual((hung["idle_seconds"], hung["idle_censored"]), (390.0, True))
        # 오류 응답 완료도 활동이다(장애 때 진행 중이던 다른 호출이 500으로 끝남)
        error_done = fault_stall_record([llm(1, 5.0, 60.0, status=500), cut], [], START, stamp(400.0), None)["events"][0]
        self.assertEqual(error_done["idle_seconds"], 50.0)
        # 대상 요청의 시작·완료도 활동이다
        target = fault_stall_record([cut], [meter(1, 9.0, 50.0), meter(2, 70.0, 71.0)], START, stamp(400.0), None)["events"][0]
        self.assertEqual(target["idle_seconds"], 40.0)
        # 같은 순간 끝난 다른 호출은 장애와 함께 일어난 것이라 다음 사건이 아니다
        same = fault_stall_record([llm(1, 5.0, 10.0), cut, llm(3, 30.0, 31.0)], [], START, stamp(400.0), None)["events"][0]
        self.assertEqual(same["idle_seconds"], 20.0)

    def test_max_event_gap_uses_the_same_definition(self):
        # T를 고를 근거: 활동 사건 사이 최장 간격. 긴 호출 하나(10~200초)는 사건 둘이라 그 사이 190초가 공백이다.
        record = fault_stall_record([llm(1, 0.0, 10.0), llm(2, 10.0, 200.0), llm(3, 205.0, 206.0)], [], START, stamp(220.0), None)
        self.assertEqual(record["max_event_gap_before_end_seconds"], 190.0)
        self.assertEqual(record["max_idle_gap_before_end_seconds"], 14.0)  # 진행 중을 활동으로 본 값(#737)은 그대로 남는다
        # 엔진이 먼저 끝나면 끝 상태까지만 잰다
        ended = fault_stall_record([llm(1, 0.0, 10.0)], [], START, stamp(500.0), stamp(30.0))
        self.assertEqual(ended["max_event_gap_before_end_seconds"], 20.0)
        empty = fault_stall_record([], [], START, stamp(12.5), None)
        self.assertEqual(empty["max_event_gap_before_end_seconds"], 12.5)

    def test_trailing_gap_is_last_activity_to_the_end(self):
        # 시간 상한 마감 구분(고정 초안 §21): 마지막 활동 사건부터 끝까지. 중간의 긴 공백(190초)이 아니라 끝 공백이다.
        record = fault_stall_record([llm(1, 0.0, 10.0), llm(2, 10.0, 200.0), llm(3, 205.0, 206.0)], [], START, stamp(220.0), None)
        self.assertEqual(record["trailing_event_gap_seconds"], 14.0)
        # 빈 계획 대기: 마지막 호출 뒤 창 끝(1500초)까지 아무 활동이 없다
        idle = fault_stall_record([llm(1, 0.0, 10.0), llm(2, 300.0, 310.0)], [], START, stamp(1500.0), None)
        self.assertEqual((idle["trailing_event_gap_seconds"], idle["max_event_gap_before_end_seconds"]), (1190.0, 1190.0))
        # 엔진이 먼저 끝나면 끝 상태까지만 잰다. 창 밖 사건은 세지 않는다
        ended = fault_stall_record([llm(1, 0.0, 10.0), llm(2, 40.0, 41.0)], [], START, stamp(500.0), stamp(30.0))
        self.assertEqual(ended["trailing_event_gap_seconds"], 20.0)
        self.assertEqual(fault_stall_record([], [], START, stamp(12.5), None)["trailing_event_gap_seconds"], 12.5)

    def test_engine_end_before_idle_end_is_not_a_stall(self):
        rows = [llm(1, 1.0, 2.0, decision="upstream_error", end="upstream_cut_before_done")]
        for terminal, stalls in ((None, 1), (1.0, 0), (100.0, 0), (300.0, 0), (300.5, 1)):
            with self.subTest(terminal=terminal):
                record = fault_stall_record(rows, [], START, stamp(300.0), None if terminal is None else stamp(terminal))
                self.assertEqual(judge_fault_stall(record, 180)["stalls"], stalls)

    def test_rate_limit_followed_by_retry_is_not_a_stall(self):
        rows = [llm(1, 1.0, 1.2, status=429), llm(2, 3.2, 5.0), llm(3, 200.0, 201.0)]
        judged = judge_fault_stall(fault_stall_record(rows, [], START, stamp(300.0), None), 180)
        self.assertEqual([(e["kind"], e["idle_seconds"], e["stall"]) for e in judged["events"]], [("rate_limited", 2.0, False)])

    def test_judgment_uses_exact_time_not_the_rounded_record(self):
        # #756 리뷰 재현: 장애가 1초에 끝나고 창이 1.9996초에 끝나면 공백은 0.9996초다. 기록용 idle_seconds는 1.0이지만
        # T=1 판정은 반올림 전 나노초로 해 멈춤이 아니다.
        rows = [llm(1, 0.5, 1.0, decision="upstream_error", end="upstream_cut_before_done")]
        record = fault_stall_record(rows, [], START, "2026-11-17T00:00:01.999600000Z", None)
        event = record["events"][0]
        self.assertEqual((event["idle_seconds"], event["idle_nanoseconds"]), (1.0, 999_600_000))
        self.assertEqual(judge_fault_stall(record, 1)["stalls"], 0)
        # 나노초 경계: 1초에서 1나노초 모자라면 아님, 정확히 1초면 멈춤
        for end, stalls in (("2026-11-17T00:00:01.999999999Z", 0), ("2026-11-17T00:00:02.000000000Z", 1)):
            with self.subTest(end=end):
                self.assertEqual(judge_fault_stall(fault_stall_record(rows, [], START, end, None), 1)["stalls"], stalls)
        # 다음 사건도 나노초로 고른다(같은 마이크로초 안 뒤 사건)
        rows2 = rows + [{**llm(2, 1.0, 1.0), "started_at": "2026-11-17T00:00:01.000000500Z",
                         "completed_at": "2026-11-17T00:00:01.000000600Z"}]
        self.assertEqual(fault_stall_record(rows2, [], START, stamp(10.0), None)["events"][0]["idle_nanoseconds"], 500)
        # 나노초로 못 읽는 시각은 기록 오류다
        for bad in ("2026-11-17T00:00:01.0000000001Z", "2026-11-17 00:00:01Z", "2026-11-17T00:00:01"):
            with self.subTest(bad=bad), self.assertRaises(FaultRecordError):
                fault_stall_record(rows, [], START, bad, None)

    def test_unjudgeable_inputs_are_refused(self):
        record = fault_stall_record(*self.PILOT, START, stamp(379.0), None)
        for value in (0, MAX_FAULT_STALL_SECONDS + 1, True, 180.0, "180", None):
            with self.subTest(t=value), self.assertRaises(FaultRecordError):
                judge_fault_stall(record, value)
        self.assertEqual(judge_fault_stall(record, MAX_FAULT_STALL_SECONDS)["stall_seconds"], MAX_FAULT_STALL_SECONDS)
        for bad in ({"format": FAULT_STALL_FORMAT, "judged": False, "error": "llm_row_invalid"},
                    judge_fault_stall(record, 180), dict(record, format="abc2lab-eval-runner-fault-stall-v1")):
            with self.subTest(bad=bad.get("error") or bad["judged"] or bad["format"]), self.assertRaises(FaultRecordError):
                judge_fault_stall(bad, 180)

    def test_outcome_reasons(self):
        record = fault_stall_record(*self.PILOT, START, stamp(379.0), None)
        self.assertEqual(fault_stall_outcome(record, None), (record, None))  # T가 없으면 판정하지 않는다
        judged, reason = fault_stall_outcome(record, 180)
        self.assertEqual((judged["judged"], reason), (True, "provider_fault_stall"))
        judged, reason = fault_stall_outcome(record, 600)
        self.assertEqual((judged["judged"], judged["stalls"], reason), (True, 0, None))
        error = {"format": FAULT_STALL_FORMAT, "judged": False, "error": "llm_row_invalid"}
        self.assertEqual(fault_stall_outcome(error, 180), (dict(error, stall_seconds=180), "fault_stall_unjudgeable"))
        self.assertEqual(fault_stall_outcome(error, None), (error, None))

if __name__ == "__main__":
    unittest.main()
