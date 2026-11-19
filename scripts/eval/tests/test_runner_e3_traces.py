"""E③ 흔적·E② 저널 사건 수(runner_e3_traces) 시험. 값은 합성 사건이다."""

from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from runner_e3_traces import declaration_counts, journal_counts, result_writes, traces  # noqa: E402


def lines(*items) -> bytes:
    return b"".join(json.dumps(item).encode() + b"\n" for item in items)


CONTROL = {"index": 1, "stage": "control", "decision": "allow", "reason": "", "method": "GET", "call_ref": "c1"}
RESULT = {"index": 2, "stage": "result", "decision": "allow", "reason": "", "call_ref": "c1"}
E3_IDENTITY = {"index": 3, "stage": "result", "program": "e3_identity", "decision": "allow", "role": "owner"}
E3_AUDIT_ONLY = {"index": 4, "stage": "intent", "e3_binding": {"run_ref": "r"}, "decision": "allow"}


class TestJournalCounts(unittest.TestCase):
    def test_kinds_and_e3_events_are_counted_without_values(self):
        counts = journal_counts(lines(CONTROL, RESULT, dict(RESULT, index=5), E3_IDENTITY, E3_AUDIT_ONLY))
        self.assertEqual(counts["events"], 5)
        self.assertEqual(counts["e3_events"], 2)  # 프로그램 종류로 하나, E③ 감사 칸으로 하나
        self.assertEqual(counts["by_kind"], {"control|-|allow|-": 1, "result|-|allow|-": 2, "result|e3_identity|allow|-": 1,
                                             "intent|-|allow|-": 1})
        self.assertNotIn("c1", json.dumps(counts))  # 호출 참조·메서드 같은 값은 옮기지 않는다

    def test_non_vocabulary_values_are_counted_as_invalid(self):
        counts = journal_counts(lines(dict(CONTROL, reason="Bad Value/../x", decision=7)))
        self.assertEqual(counts["by_kind"], {"control|-|invalid|invalid": 1})
        self.assertNotIn("Bad", json.dumps(counts))

    def test_missing_empty_and_broken_journals(self):
        self.assertEqual(journal_counts(None), {"error": "not_found"})
        self.assertEqual(journal_counts(b""), {"events": 0, "e3_events": 0, "by_kind": {}})
        self.assertEqual(journal_counts(b"\n\n"), {"events": 0, "e3_events": 0, "by_kind": {}})
        for broken in (b"{not json}\n", b"[1]\n", lines(CONTROL) + b"x\n"):
            with self.subTest(broken=broken):
                self.assertEqual(journal_counts(broken), {"error": "journal_unparsable"})


class TestDeclarationCounts(unittest.TestCase):
    def record(self, payload: dict, sequence: int = 1) -> dict:
        return {"sequence": sequence, "previous": "", "payload": payload, "digest": "d"}

    def test_actions_and_write_objects(self):
        raw = lines(self.record({"action": "declare", "target_id": "t"}),
                    self.record({"action": "confirmation_writes", "job_ref": "j", "objects": [{"type": "book"}, {"type": "user"}]}, 2),
                    self.record({"action": "reset_confirmed"}, 3))
        self.assertEqual(declaration_counts(raw), {"records": 3, "write_objects": 2,
                                                   "by_action": {"confirmation_writes": 1, "declare": 1, "reset_confirmed": 1}})
        self.assertEqual(declaration_counts(b""), {"records": 0, "by_action": {}, "write_objects": 0})
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
        record = traces({"e2_host_journal": lines(CONTROL, RESULT), "e2_product_journal": lines(CONTROL, RESULT),
                         "declarations": b"", "e3_result": json.dumps({"writes": []}).encode()})
        self.assertEqual(record["summary"], {"e3_events": 0, "product_journal_e3_events": 0, "declaration_writes": 0, "result_writes": 0})
        self.assertEqual(record["e2_host_journal"]["by_kind"], record["e2_product_journal"]["by_kind"])

    def test_summary_shows_e3_traces_and_missing_sources(self):
        record = traces({"e2_host_journal": lines(CONTROL, E3_IDENTITY), "e2_product_journal": None,
                         "declarations": lines({"sequence": 1, "payload": {"action": "confirmation_writes", "objects": [{}]}}),
                         "e3_result": json.dumps({"writes": [{}]}).encode()})
        self.assertEqual(record["summary"], {"e3_events": 1, "product_journal_e3_events": None, "declaration_writes": 1,
                                             "result_writes": 1})
        self.assertEqual(record["e2_product_journal"], {"error": "not_found"})


if __name__ == "__main__":
    unittest.main()
