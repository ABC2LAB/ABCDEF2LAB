"""근거 저장(O_EXCL·0600)과 계측기 기록 읽기. 실제 계측기 없이 합성 파일로 본다."""

from __future__ import annotations

import json
import re
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_evidence import (
    ATTEMPT_STATES, DRAINED_MARKER, ENGINE_KINDS, ENGINE_LOG, ENGINE_RESPONSE, ENGINE_SENT, LLM_VERIFIED_MARKER,
    OBSERVER_LOG, EvidenceDir, EvidenceError, GatewayFiles, MeterFiles, forwarded_count, sequence_complete,
)


def engine_row(seq, kind="request"):
    sent, response = {"rejected": ("no", "none"), "stream": ("yes", "headers")}.get(kind, ("yes", "complete"))
    return {"seq": seq, "kind": kind, "method": "GET", "status": 200, "started_at": "a", "completed_at": "b",
            "response_bytes": 1, "outcome": "completed", "sent": sent, "response": response,
            "attempt_id_state": "absent", "attempt_id": ""}


def observer_row(seq, decision="registered"):
    return {"seq": seq, "decision": decision, "method": "GET", "status": 200, "started_at": "a", "completed_at": "b",
            "response_bytes": 1}


@unittest.skipUnless(hasattr(os, "O_NOFOLLOW") and hasattr(os, "geteuid"), "POSIX 전용")
class TestEvidenceDir(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name, "run")
        self.dir.mkdir(mode=0o700)
        os.chmod(self.dir, 0o700)

    def tearDown(self):
        self.tmp.cleanup()

    def test_writes_once_private_and_remembers_digest(self):
        evidence = EvidenceDir(self.dir)
        digest = evidence.write_json("pre-checks.json", {"b": 1, "a": "한"})
        path = self.dir / "pre-checks.json"
        self.assertEqual(stat.S_IMODE(os.lstat(path).st_mode), 0o600)
        self.assertEqual(path.read_bytes(), '{"a":"한","b":1}\n'.encode())
        self.assertEqual(evidence.digests(), {"pre-checks.json": digest})
        with self.assertRaises(EvidenceError) as caught:
            evidence.write_json("pre-checks.json", {})
        self.assertEqual(str(caught.exception), "evidence_exists")
        self.assertEqual(path.read_bytes(), '{"a":"한","b":1}\n'.encode())

    def test_rejects_bad_names_payloads_and_public_dir(self):
        evidence = EvidenceDir(self.dir)
        for name in ("../x.json", "x.txt", "X.json", "a/b.json", ".json"):
            with self.assertRaises(EvidenceError):
                evidence.write_json(name, {})
        for payload in ({"x": float("nan")}, {"x": object()}):
            with self.assertRaises(EvidenceError):
                evidence.write_json("p.json", payload)
        self.assertEqual(list(self.dir.iterdir()), [])
        os.chmod(self.dir, 0o750)
        with self.assertRaises(EvidenceError) as caught:
            evidence.write_json("p.json", {})
        self.assertEqual(str(caught.exception), "evidence_dir_not_private")

    def test_symlink_target_not_followed(self):
        outside = Path(self.tmp.name, "outside.json")
        outside.write_text("keep")
        (self.dir / "p.json").symlink_to(outside)
        with self.assertRaises(EvidenceError):
            EvidenceDir(self.dir).write_json("p.json", {})
        self.assertEqual(outside.read_text(), "keep")


@unittest.skipUnless(hasattr(os, "O_NOFOLLOW") and hasattr(os, "geteuid"), "POSIX 전용")
class TestMeterFiles(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name, "meter")
        self.dir.mkdir(mode=0o700)
        os.chmod(self.dir, 0o700)
        self.e2_dir = Path(self.tmp.name, "e2")
        self.e2_dir.mkdir(mode=0o700)
        os.chmod(self.e2_dir, 0o700)
        self.meter = MeterFiles(self.dir, self.e2_dir)

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, name, text):
        path = self.dir / name
        path.write_text(text)
        os.chmod(path, 0o600)

    def write_e2(self, text):
        path = self.e2_dir / "e2-stub-decisions.jsonl"
        path.write_text(text)
        os.chmod(path, 0o600)

    def stub_rows(self, *rows):
        self.write_e2("".join(json.dumps(r) + "\n" for r in rows))

    @staticmethod
    def stub_row(seq, decision="allowed", attempt=None, **extra):
        attempt = ("%032x" % seq) if attempt is None and decision != "unavailable" else (attempt or "")
        row = {"seq": seq, "fixture": True, "decision": decision, "attempt_id": attempt, "caller_attempt": False,
               "method": "GET", "decided_at": "2026-11-05T00:00:00Z"}
        row.update(extra)
        return row

    def test_e2_stub_summary_counts_decisions(self):
        self.stub_rows(self.stub_row(1), self.stub_row(2, "unsupported"), self.stub_row(3, "unavailable"),
                       self.stub_row(4))
        self.assertEqual(self.meter.e2_stub_summary(),
                         {"decision_rows": 4, "allowed": 2, "unsupported": 1, "unavailable": 1})
        self.write_e2("")
        self.assertEqual(self.meter.e2_stub_summary()["decision_rows"], 0)
        # 계측기 디렉터리에 같은 이름이 있어도 읽지 않는다(E²는 계측기 증거를 쓰지 않는다).
        self.write("e2-stub-decisions.jsonl", json.dumps(self.stub_row(1)) + "\n")
        self.assertEqual(self.meter.e2_stub_summary()["decision_rows"], 0)
        self.assertRaisesRegex(Exception, "e2_stub_log_missing", MeterFiles(self.dir).e2_stub_summary)

    def test_e2_stub_summary_rejects_bad_rows(self):
        from runner_evidence import EvidenceError
        cases = {
            "not a fixture": [self.stub_row(1, fixture=False)],
            "unknown decision": [self.stub_row(1, "forwarded")],
            "allowed without an id": [self.stub_row(1, attempt="")],
            "id with uppercase": [self.stub_row(1, attempt="A" * 32)],
            "unavailable with an id": [self.stub_row(1, "unavailable", attempt="a" * 32)],
            "repeated id": [self.stub_row(1, attempt="b" * 32), self.stub_row(2, attempt="b" * 32)],
            "sequence gap": [self.stub_row(1), self.stub_row(3)],
            "caller flag not bool": [self.stub_row(1, caller_attempt=1)],
            "extra field": [self.stub_row(1, path="/x")],
        }
        for name, rows in cases.items():
            with self.subTest(name=name):
                self.stub_rows(*rows)
                with self.assertRaises(EvidenceError):
                    self.meter.e2_stub_summary()
        (self.e2_dir / "e2-stub-decisions.jsonl").unlink()
        with self.assertRaisesRegex(EvidenceError, "e2_stub_log_missing"):
            self.meter.e2_stub_summary()

    def test_missing_log_counts_zero(self):
        self.assertEqual(self.meter.forwarded(), 0)
        self.assertEqual(self.meter.observer_rows(), [])
        self.assertIsNone(self.meter.marker(DRAINED_MARKER))

    def test_counts_only_forwarded_complete_lines(self):
        lines = [engine_row(2), engine_row(1), engine_row(3, "rejected")]
        partial = json.dumps(engine_row(4))[:20]
        self.write(ENGINE_LOG, "".join(json.dumps(r) + "\n" for r in lines) + partial)
        self.assertEqual(self.meter.forwarded(), 2)
        self.assertTrue(sequence_complete(self.meter.engine_rows()))

    def test_upgraded_streams_count_as_forwarded(self):
        # 동찬 #126 리뷰: 계측기는 upgrade도 전달로 세고 완료 행을 kind=stream으로 쓴다.
        lines = [engine_row(1), engine_row(2, "stream"), engine_row(3, "rejected")]
        self.write(ENGINE_LOG, "".join(json.dumps(r) + "\n" for r in lines))
        self.assertEqual(self.meter.forwarded(), 2)
        self.assertEqual(forwarded_count([engine_row(1, "stream")]), 1)
        with self.assertRaises(EvidenceError):
            forwarded_count([engine_row(1, "upgrade")])

    def test_kinds_match_the_go_meter(self):
        text = (Path(__file__).resolve().parents[3] / "cmd/eval-meter/proxy.go").read_text(encoding="utf-8")
        kinds = set(re.findall(r'Kind(?::|\s*=)\s*"([a-z]+)"', text))
        self.assertEqual(kinds, set(ENGINE_KINDS))
        # rejected가 아닌 경로는 전달 건수를 올린다(forwarded++ 한 곳, rejected 분기 뒤).
        self.assertEqual(text.count("m.forwarded++"), 1)
        self.assertLess(text.index('Kind: "rejected"'), text.index("m.forwarded++"))

    def test_stage_fields_are_checked(self):
        valid = "0123456789abcdef0123456789abcdef"
        good = [engine_row(1) | {"attempt_id_state": "valid", "attempt_id": valid},
                engine_row(2) | {"sent": "uncertain", "response": "none"},
                engine_row(3) | {"response": "interrupted", "attempt_id_state": "multiple"},
                engine_row(4, "rejected") | {"attempt_id_state": "invalid"}]
        self.write(ENGINE_LOG, "".join(json.dumps(r) + "\n" for r in good))
        self.assertEqual(len(self.meter.engine_rows()), 4)
        bad = {"모르는 sent": {"sent": "maybe"}, "모르는 response": {"response": "partial"},
               "모르는 상태": {"attempt_id_state": "weird"}, "유효인데 값 없음": {"attempt_id_state": "valid"},
               "유효인데 대문자": {"attempt_id_state": "valid", "attempt_id": valid.upper()},
               "무효인데 값 있음": {"attempt_id_state": "invalid", "attempt_id": valid},
               "값이 문자열 아님": {"attempt_id": 0},
               "안 보냈는데 응답": {"sent": "no", "response": "complete"}}
        for name, change in bad.items():
            with self.subTest(name=name):
                self.write(ENGINE_LOG, json.dumps(engine_row(1) | change) + "\n")
                with self.assertRaises(EvidenceError):
                    self.meter.engine_rows()
        # seal 뒤 거부는 대상에 안 갔다.
        self.write(ENGINE_LOG, json.dumps(engine_row(1, "rejected") | {"sent": "yes", "response": "none"}) + "\n")
        with self.assertRaises(EvidenceError):
            self.meter.forwarded()

    def test_stage_values_match_the_go_meter(self):
        text = (Path(__file__).resolve().parents[3] / "cmd/eval-meter/proxy.go").read_text(encoding="utf-8")
        for prefix, expected in (("sentNo", ENGINE_SENT), ("responseNone", ENGINE_RESPONSE),
                                 ("attemptAbsent", ATTEMPT_STATES)):
            line = next(line for line in text.splitlines() if line.strip().startswith(prefix + ","))
            self.assertEqual(set(re.findall(r'"([a-z]+)"', line)), set(expected), prefix)

    def test_rows_must_have_exact_fields_and_unique_seq(self):
        for rows in ([engine_row(1) | {"path": "/x"}], [{k: v for k, v in engine_row(1).items() if k != "kind"}],
                     [engine_row(1), engine_row(1)], [engine_row(1) | {"seq": "1"}]):
            self.write(ENGINE_LOG, "".join(json.dumps(r) + "\n" for r in rows))
            with self.assertRaises(EvidenceError):
                self.meter.engine_rows()
        self.write(ENGINE_LOG, "not json\n")
        with self.assertRaises(EvidenceError):
            self.meter.forwarded()
        self.write(OBSERVER_LOG, json.dumps(observer_row(1) | {"query": "x"}) + "\n")
        with self.assertRaises(EvidenceError):
            self.meter.observer_rows()

    def test_sequence_complete_detects_gaps(self):
        self.assertTrue(sequence_complete([]))
        self.assertTrue(sequence_complete([{"seq": 2}, {"seq": 1}]))
        self.assertFalse(sequence_complete([{"seq": 1}, {"seq": 3}]))
        self.assertFalse(sequence_complete([{"seq": 2}]))

    def test_marker_role_and_name(self):
        self.write(DRAINED_MARKER, json.dumps({"role": "engine", "inflight_at_drain": 0}))
        self.assertEqual(self.meter.marker(DRAINED_MARKER)["inflight_at_drain"], 0)
        self.write(DRAINED_MARKER, json.dumps({"role": "observer"}))
        with self.assertRaises(EvidenceError):
            self.meter.marker(DRAINED_MARKER)
        with self.assertRaises(EvidenceError):
            self.meter.marker("../x.json")

    def test_symlinked_or_non_regular_files_rejected(self):
        outside = Path(self.tmp.name, "outside.jsonl")
        outside.write_text(json.dumps(engine_row(1)) + "\n")
        (self.dir / ENGINE_LOG).symlink_to(outside)
        with self.assertRaises(EvidenceError):
            self.meter.forwarded()
        (self.dir / ENGINE_LOG).unlink()
        (self.dir / ENGINE_LOG).mkdir()
        with self.assertRaises(EvidenceError):
            self.meter.forwarded()

    def test_gateway_marker_reading(self):
        gateway = GatewayFiles(self.dir)
        self.assertIsNone(gateway.verified())
        self.write(LLM_VERIFIED_MARKER, json.dumps({"role": "llm-gateway", "backend": "api"}))
        self.assertEqual(gateway.verified()["backend"], "api")
        for text in ('{"role": "engine"}', "not json", "[]"):
            self.write(LLM_VERIFIED_MARKER, text)
            with self.assertRaises(EvidenceError):
                gateway.verified()
        (self.dir / LLM_VERIFIED_MARKER).unlink()
        outside = Path(self.tmp.name, "forged.json")
        outside.write_text(json.dumps({"role": "llm-gateway"}))
        (self.dir / LLM_VERIFIED_MARKER).symlink_to(outside)
        with self.assertRaises(EvidenceError):
            gateway.verified()

    def test_meter_dir_must_be_private(self):
        os.chmod(self.dir, 0o755)
        with self.assertRaises(EvidenceError):
            self.meter.forwarded()


if __name__ == "__main__":
    unittest.main()
