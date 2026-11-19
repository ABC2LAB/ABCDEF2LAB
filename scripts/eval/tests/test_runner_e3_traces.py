"""E③ 흔적·E② 저널 사건 수(runner_e3_traces) 시험. 값은 합성 사건이다."""

from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from runner_e3_traces import (  # noqa: E402
    declaration_counts, evidence_checkpoints_summary, journal_counts, result_writes, seal_checks_summary, traces)


def lines(*items) -> bytes:
    return b"".join(json.dumps(item).encode() + b"\n" for item in items)


HEADER = {"Kind": "header", "Header": {"Version": "e2-http-v3", "JournalID": "j"}, "MAC": "m"}


def journal(*events) -> bytes:
    """e2proxy 저널 줄 모양(머리 줄 하나 + 사건 줄)."""
    return lines(HEADER, *({"Kind": "event", "Record": {"Index": i, "Previous": "p", "Event": e}, "MAC": "m"}
                           for i, e in enumerate(events, 1)))


CONTROL = {"index": 1, "stage": "control", "decision": "allow", "reason": "", "method": "GET", "call_ref": "c1"}
RESULT = {"index": 2, "stage": "result", "decision": "allow", "reason": "", "call_ref": "c1"}
E3_IDENTITY = {"index": 3, "stage": "result", "program": "e3_identity", "decision": "allow", "role": "owner"}
E3_AUDIT_ONLY = {"index": 4, "stage": "intent", "e3_binding": {"run_ref": "r"}, "decision": "allow"}


class TestJournalCounts(unittest.TestCase):
    def test_kinds_and_e3_events_are_counted_without_values(self):
        counts = journal_counts(journal(CONTROL, RESULT, dict(RESULT, index=5), E3_IDENTITY, E3_AUDIT_ONLY))
        self.assertEqual((counts["headers"], counts["events"]), (1, 5))
        self.assertEqual(counts["e3_events"], 2)  # 프로그램 종류로 하나, E③ 감사 칸으로 하나
        self.assertEqual(counts["by_kind"], {"control|-|allow|-": 1, "result|-|allow|-": 2, "result|e3_identity|allow|-": 1,
                                             "intent|-|allow|-": 1})
        self.assertNotIn("c1", json.dumps(counts))  # 호출 참조·메서드 같은 값은 옮기지 않는다

    def test_timeline_keeps_order_model_flag_and_time_only(self):
        # #809 확인: 목표 달성 뒤 감독자가 닫을 때까지 모델 쪽(Worker, 의도 번호 > 0) 사건이 없는지 시각 순서로 본다.
        events = (dict(CONTROL, intent_id=0, at="2026-11-19T18:53:20.1Z"),
                  {"stage": "intent", "decision": "allowed", "intent_id": 7, "at": "2026-11-19T18:53:29.5+00:00", "call_ref": "c9"},
                  {"stage": "result", "decision": "allowed", "intent_id": 7, "at": "2026-11-19T18:53:30Z"},
                  {"stage": "control", "reason": "closed", "intent_id": 0, "at": "not a time"},
                  {"stage": "result", "intent_id": True, "at": 5})
        timeline = journal_counts(journal(*events))["timeline"]
        self.assertEqual([(e["stage"], e["model"], e["at"]) for e in timeline],
                         [("control", False, "2026-11-19T18:53:20.1Z"), ("intent", True, "2026-11-19T18:53:29.5+00:00"),
                          ("result", True, "2026-11-19T18:53:30Z"), ("control", False, None), ("result", False, None)])
        self.assertEqual(set(timeline[0]), {"stage", "program", "decision", "reason", "model", "at"})
        self.assertEqual(timeline[3]["reason"], "closed")
        self.assertNotIn("c9", json.dumps(timeline))

    def test_timeline_is_capped_but_counts_are_not(self):
        import runner_e3_traces
        original = runner_e3_traces.MAX_TIMELINE
        runner_e3_traces.MAX_TIMELINE = 2
        try:
            counts = journal_counts(journal(CONTROL, RESULT, RESULT))
        finally:
            runner_e3_traces.MAX_TIMELINE = original
        self.assertEqual((counts["events"], len(counts["timeline"]), counts["timeline_truncated"]), (3, 2, True))
        self.assertFalse(journal_counts(journal(CONTROL))["timeline_truncated"])

    def test_non_scalar_vocabulary_values_do_not_raise(self):
        # #807 리뷰: program·stage·decision·reason이 배열·객체여도 예외 없이 invalid로 센다(고정 오류 경로 밖으로 새지 않게).
        for value in ([], ["e3_read"], {}, {"e3_read": 1}):
            with self.subTest(value=value):
                counts = journal_counts(journal(dict(CONTROL, program=value, stage=value, decision=value, reason=value)))
                self.assertEqual((counts["events"], counts["e3_events"]), (1, 0))
                self.assertEqual(counts["by_kind"], {"invalid|invalid|invalid|invalid": 1})
                self.assertEqual({e["program"] for e in counts["timeline"]}, {"invalid"})
        # E③ 감사 칸이 있으면 program 값과 상관없이 E③ 사건이다
        self.assertEqual(journal_counts(journal(dict(E3_AUDIT_ONLY, program=[])))["e3_events"], 1)
        # 선언 저널 동작 칸도 같다
        record = {"sequence": 1, "previous": "", "payload": {"action": ["confirmation_writes"]}, "digest": "d"}
        self.assertEqual(declaration_counts(lines(record))["by_action"], {"invalid": 1})

    def test_non_vocabulary_values_are_counted_as_invalid(self):
        counts = journal_counts(journal(dict(CONTROL, reason="Bad Value/../x", decision=7)))
        self.assertEqual(counts["by_kind"], {"control|-|invalid|invalid": 1})
        self.assertNotIn("Bad", json.dumps(counts))

    def test_missing_empty_and_broken_journals(self):
        self.assertEqual(journal_counts(None), {"error": "not_found"})
        empty = {"headers": 0, "events": 0, "e3_events": 0, "by_kind": {}, "timeline": [], "timeline_truncated": False}
        self.assertEqual(journal_counts(b""), empty)
        self.assertEqual(journal_counts(b"\n\n"), empty)
        self.assertEqual(journal_counts(journal())["headers"], 1)
        # 저널 줄 모양이 아니면(사건만 펼친 줄, 사건 칸 없음, 모르는 종류) 세지 않고 실패로 남긴다
        for broken in (b"{not json}\n", b"[1]\n", journal(CONTROL) + b"x\n", lines(CONTROL),
                       lines({"Kind": "event", "Record": {"Index": 1}}), lines({"Kind": "other", "Record": {"Event": CONTROL}}),
                       lines({"Kind": "header"})):
            with self.subTest(broken=broken):
                self.assertEqual(journal_counts(broken), {"error": "journal_unparsable"})


class TestDeclarationCounts(unittest.TestCase):
    def record(self, payload: dict, sequence: int = 1) -> dict:
        return {"sequence": sequence, "previous": "", "payload": payload, "digest": "d"}

    def test_actions_and_write_objects(self):
        raw = lines(self.record({"format": "diagnosis-preparation-journal.v1"}, 0),
                    self.record({"action": "declare", "target_id": "t"}),
                    self.record({"action": "confirmation_writes", "job_ref": "j", "objects": [{"type": "book"}, {"type": "user"}]}, 2),
                    self.record({"action": "reset_confirmed"}, 3))
        self.assertEqual(declaration_counts(raw), {"format_records": 1, "records": 3, "write_objects": 2,
                                                   "by_action": {"confirmation_writes": 1, "declare": 1, "reset_confirmed": 1}})
        self.assertEqual(declaration_counts(b""), {"format_records": 0, "records": 0, "by_action": {}, "write_objects": 0})
        # 새 설치에서 선언이 없으면 형식 줄 하나뿐이다(2026-11-19 합성 회차)
        self.assertEqual(declaration_counts(lines(self.record({"format": "diagnosis-preparation-journal.v1"}))),
                         {"format_records": 1, "records": 0, "by_action": {}, "write_objects": 0})
        # 동작 없는 다른 줄은 형식 줄로 보지 않는다
        self.assertEqual(declaration_counts(lines(self.record({"target_id": "t"})))["by_action"], {"-": 1})
        self.assertEqual(declaration_counts(None), {"error": "not_found"})
        self.assertEqual(declaration_counts(lines({"sequence": 1, "payload": "x"})), {"error": "journal_unparsable"})


class TestTraces(unittest.TestCase):
    def test_result_writes(self):
        self.assertEqual(result_writes(json.dumps({"writes": [{}, {}]}).encode()), 2)
        self.assertEqual(result_writes(json.dumps({"writes": []}).encode()), 0)
        for raw in (None, b"x", b"[]", json.dumps({"writes": None}).encode()):
            with self.subTest(raw=raw):
                self.assertIsNone(result_writes(raw))

    def test_summary_of_a_run_without_e3(self):
        record = traces({"e2_host_journal": journal(CONTROL, RESULT), "e2_product_journal": journal(CONTROL, RESULT),
                         "declarations": b"", "e3_result": json.dumps({"writes": []}).encode()})
        self.assertEqual(record["summary"], {"e3_events": 0, "product_journal_e3_events": 0, "declaration_writes": 0, "result_writes": 0,
                                             "host_seal_phase": None, "host_seal_checks": None, "host_evidence_checkpoints": 0})
        self.assertEqual(record["e2_host_journal"]["by_kind"], record["e2_product_journal"]["by_kind"])

    def test_summary_shows_e3_traces_and_missing_sources(self):
        record = traces({"e2_host_journal": journal(CONTROL, E3_IDENTITY), "e2_product_journal": None,
                         "declarations": lines({"sequence": 1, "payload": {"action": "confirmation_writes", "objects": [{}]}}),
                         "e3_result": json.dumps({"writes": [{}]}).encode()})
        self.assertEqual(record["summary"], {"e3_events": 1, "product_journal_e3_events": None, "declaration_writes": 1,
                                             "result_writes": 1, "host_seal_phase": None, "host_seal_checks": None,
                                             "host_evidence_checkpoints": 0})
        self.assertEqual(record["e2_product_journal"], {"error": "not_found"})
        # 선언 저널을 못 읽었으면 쓰기 기록 수를 0으로 단정하지 않는다
        missing = traces({"e2_host_journal": journal(), "e2_product_journal": journal(), "declarations": None, "e3_result": None})
        self.assertEqual(missing["summary"], {"e3_events": 0, "product_journal_e3_events": 0, "declaration_writes": None,
                                              "result_writes": None, "host_seal_phase": None, "host_seal_checks": None,
                                              "host_evidence_checkpoints": 0})


class TestHostSealFiles(unittest.TestCase):
    """#810 뒤 Host 실행 디렉터리의 봉인 포함 확인·근거 체크포인트 기록. 값(MAC·head)은 옮기지 않는다."""

    SEAL = {"version": "jindo.e2host-seal-checks/v1", "phase": "sealed", "head": {"MAC": "secret-mac"},
            "checks": [{"sequence": 3, "mac": "m1", "included": True}, {"sequence": 5, "mac": "m2", "included": False}]}

    def test_seal_checks(self):
        summary = seal_checks_summary(json.dumps(self.SEAL).encode())
        self.assertEqual(summary, {"version": "jindo.e2host-seal-checks/v1", "phase": "sealed", "checks": 2, "included": 1})
        self.assertNotIn("m1", json.dumps(summary))
        self.assertEqual(seal_checks_summary(json.dumps(dict(self.SEAL, checks=[])).encode())["checks"], 0)
        self.assertEqual(seal_checks_summary(json.dumps(dict(self.SEAL, phase="Bad Phase", version="X Y")).encode())
                         | {"checks": 0, "included": 0}, {"version": "invalid", "phase": "invalid", "checks": 0, "included": 0})
        self.assertEqual(seal_checks_summary(None), {"error": "not_found"})
        for broken in (b"x", b"[]", json.dumps({"phase": "sealed"}).encode(), json.dumps(dict(self.SEAL, checks=[1])).encode()):
            with self.subTest(broken=broken):
                self.assertEqual(seal_checks_summary(broken), {"error": "unparsable"})

    def test_evidence_checkpoints(self):
        raw = json.dumps({"version": "jindo.e2host-evidence-checkpoints/v1", "checkpoints": [{"MAC": "m"}, {"MAC": "n"}]}).encode()
        self.assertEqual(evidence_checkpoints_summary(raw), {"version": "jindo.e2host-evidence-checkpoints/v1", "checkpoints": 2})
        self.assertEqual(evidence_checkpoints_summary(None), {"error": "not_found"})
        for broken in (b"x", b"{}", json.dumps({"checkpoints": 1}).encode()):
            with self.subTest(broken=broken):
                self.assertEqual(evidence_checkpoints_summary(broken), {"error": "unparsable"})

    def test_summary_for_a_run_without_declaration(self):
        # 선언 없는 회차(#810 확인 항목): 근거 기록 없음, 봉인 확인 sealed·0개
        record = traces({"e2_host_journal": b"", "e2_product_journal": b"", "declarations": b"", "e3_result": None,
                         "host_seal_checks": json.dumps(dict(self.SEAL, checks=[])).encode(), "host_evidence_checkpoints": None})
        self.assertEqual((record["summary"]["host_seal_phase"], record["summary"]["host_seal_checks"],
                          record["summary"]["host_evidence_checkpoints"]), ("sealed", 0, 0))
        self.assertEqual(record["host_evidence_checkpoints"], {"error": "not_found"})
        # 근거 기록이 있으면 그 수, 모양이 틀리면 null(없음과 구분)
        with_issued = traces({"host_evidence_checkpoints": json.dumps({"version": "v1", "checkpoints": [{}]}).encode()})
        self.assertEqual(with_issued["summary"]["host_evidence_checkpoints"], 1)
        self.assertIsNone(traces({"host_evidence_checkpoints": b"x"})["summary"]["host_evidence_checkpoints"])


if __name__ == "__main__":
    unittest.main()
