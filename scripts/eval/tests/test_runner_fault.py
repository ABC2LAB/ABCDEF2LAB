"""제공자 장애 멈춤 기록(탐색 분리 배치 결정안 §7, 4-5) 시험."""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from runner_fault import FAULT_STALL_FORMAT, FaultRecordError, fault_stall_record  # noqa: E402

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
                                             "censored": True, "inflight_at_fault": 0, "engine_terminal_within": False}])
        self.assertEqual((record["max_silence_after_fault_seconds"], record["max_silence_after_fault_censored"]), (359.0, True))
        self.assertEqual(record["max_idle_gap_seconds"], 359.0)
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

    def test_empty_round(self):
        record = fault_stall_record([], [], START, stamp(12.5), None)
        self.assertEqual((record["max_idle_gap_seconds"], record["activity_starts"], record["events"]), (12.5, 0, []))


if __name__ == "__main__":
    unittest.main()
