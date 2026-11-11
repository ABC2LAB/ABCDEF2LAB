"""근거 저장(O_EXCL·0600)과 계측기 기록 읽기. 실제 계측기 없이 합성 파일로 본다."""

from __future__ import annotations

import hashlib
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
    OBSERVER_LOG, PATH_AUTH_FIELDS, PATH_LOG, PATH_PROFILE, EvidenceDir, EvidenceError, GatewayFiles, MeterFiles,
    drain_complete, final_log_consistent, forwarded_by_method, forwarded_count, sequence_complete,
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

    def test_write_bytes_keeps_the_exact_bytes(self):
        evidence = EvidenceDir(self.dir)
        data = b'{ "b":1,   "a":2 }'
        digest = evidence.write_bytes("projection.json", data)
        path = self.dir / "projection.json"
        self.assertEqual(path.read_bytes(), data)
        self.assertEqual(digest, hashlib.sha256(data).hexdigest())
        self.assertEqual(stat.S_IMODE(os.lstat(path).st_mode), 0o600)
        self.assertEqual(evidence.digests()["projection.json"], digest)
        with self.assertRaises(EvidenceError):
            evidence.write_bytes("projection.json", b"{}")
        with self.assertRaises(EvidenceError):
            evidence.write_bytes("../escape.json", b"{}")

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
        from runner_evidence import e2_stub_counts
        self.assertEqual(e2_stub_counts(self.meter.e2_stub_rows()),
                         {"decision_rows": 4, "allowed": 2, "unsupported": 1, "unavailable": 1})
        self.write_e2("")
        self.assertEqual(e2_stub_counts(self.meter.e2_stub_rows())["decision_rows"], 0)
        # 계측기 디렉터리에 같은 이름이 있어도 읽지 않는다(E²는 계측기 증거를 쓰지 않는다).
        self.write("e2-stub-decisions.jsonl", json.dumps(self.stub_row(1)) + "\n")
        self.assertEqual(e2_stub_counts(self.meter.e2_stub_rows())["decision_rows"], 0)
        self.assertRaisesRegex(Exception, "e2_stub_log_missing", MeterFiles(self.dir).e2_stub_rows)

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
            "method not a string": [self.stub_row(1, method={})],
            "method empty": [self.stub_row(1, method="")],
            "method with a space": [self.stub_row(1, method="GE T")],
            "time not a string": [self.stub_row(1, decided_at=123)],
            "time with an offset": [self.stub_row(1, decided_at="2026-11-05T09:00:00+09:00")],
            "time without seconds": [self.stub_row(1, decided_at="2026-11-05T00:00Z")],
        }
        for name, rows in cases.items():
            with self.subTest(name=name):
                self.stub_rows(*rows)
                with self.assertRaises(EvidenceError):
                    self.meter.e2_stub_rows()
        # 최종 읽기에서는 개행으로 끝나지 않은 꼬리(잘린 행)를 거부한다. 진행 중 로그처럼 버리면 잘린 행을 빼고 센다.
        good = json.dumps(self.stub_row(1))
        for name, text in (("only row without newline", good),
                           ("truncated second row", good + "\n" + '{"seq":2,"fixture":true')):
            with self.subTest(name=name):
                self.write_e2(text)
                with self.assertRaisesRegex(EvidenceError, "e2_stub_log_invalid"):
                    self.meter.e2_stub_rows()
        (self.e2_dir / "e2-stub-decisions.jsonl").unlink()
        with self.assertRaisesRegex(EvidenceError, "e2_stub_log_missing"):
            self.meter.e2_stub_rows()

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

    def test_method_counts_use_a_closed_list_and_add_up_to_forwarded(self):
        def row(seq, kind, method):
            return dict(engine_row(seq, kind), method=method)
        rows = [row(1, "request", "GET"), row(2, "request", "POST"), row(3, "stream", "GET"), row(4, "request", "get"),
                row(5, "request", "SECRETTOKEN"), row(6, "rejected", "DELETE"), row(7, "request", "OPTIONS")]
        counts = forwarded_by_method(rows)
        self.assertEqual(counts, {"GET": 2, "HEAD": 0, "POST": 1, "PUT": 0, "PATCH": 0, "DELETE": 0, "OPTIONS": 1,
                                  "OTHER": 2})
        self.assertEqual(sum(counts.values()), forwarded_count(rows))
        self.assertNotIn("SECRETTOKEN", json.dumps(counts))
        with self.assertRaisesRegex(EvidenceError, "meter_log_invalid"):
            forwarded_by_method([row(1, "upgrade", "GET")])

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



class FinalMeterChecksTests(unittest.TestCase):
    """러너와 보존 집계가 같이 쓰는 drain·마감 확인(#376 리뷰)."""

    DRAINED = {"role": "engine", "inflight_at_drain": 0, "log_complete": True, "forced_cancelled_requests": 1,
               "forwarded_requests": 2}

    def rows(self):
        cancelled = dict(engine_row(2), outcome="forced_cancelled")
        return [engine_row(1), cancelled, engine_row(3, "rejected")]

    def test_drain_complete(self):
        self.assertTrue(drain_complete(self.DRAINED))
        for field, value in (("inflight_at_drain", 5), ("inflight_at_drain", False), ("inflight_at_drain", 0.0),
                             ("log_complete", 1), ("forced_cancelled_requests", -1),
                             ("forced_cancelled_requests", True), ("forced_cancelled_requests", None)):
            with self.subTest(field=field, value=value):
                self.assertFalse(drain_complete(dict(self.DRAINED, **{field: value})))
        self.assertFalse(drain_complete({k: v for k, v in self.DRAINED.items() if k != "inflight_at_drain"}))

    def test_final_log_consistent(self):
        sealed = {"role": "engine"}
        self.assertTrue(final_log_consistent(self.rows(), sealed, self.DRAINED))
        self.assertFalse(final_log_consistent(self.rows(), None, self.DRAINED))
        self.assertFalse(final_log_consistent(self.rows()[1:], sealed, dict(self.DRAINED, forwarded_requests=1)))
        for value in (3, 2.0, True, None, "2"):
            with self.subTest(forwarded=value):
                self.assertFalse(final_log_consistent(self.rows(), sealed, dict(self.DRAINED, forwarded_requests=value)))
        self.assertFalse(final_log_consistent(self.rows(), sealed, dict(self.DRAINED, forced_cancelled_requests=0)))
        with self.assertRaises(EvidenceError):
            final_log_consistent(self.rows() + [engine_row(4, "other")], sealed, self.DRAINED)


class MeterPathLogTests(unittest.TestCase):
    def test_path_log_presence_follows_the_meter_file_name(self):
        go = (Path(__file__).resolve().parents[3] / "cmd/eval-meter/paths.go").read_text(encoding="utf-8")
        self.assertIn('enginePathLogName = "%s"' % PATH_LOG, go)
        with tempfile.TemporaryDirectory() as raw:
            meter = MeterFiles(Path(raw))
            self.assertFalse(meter.path_log_present())
            (Path(raw) / PATH_LOG).write_text("", encoding="utf-8")
            self.assertTrue(meter.path_log_present())

    def test_path_row_format_matches_the_meter(self):
        go = (Path(__file__).resolve().parents[3] / "cmd/eval-meter/paths.go").read_text(encoding="utf-8")
        self.assertRegex(go, r'pathMaskProfile\s*=\s*"%s"' % re.escape(PATH_PROFILE))
        for field in ("seq", "method", "path_pattern", "masked_segments", "path_profile_ref", "path_key_ref",
                      *sorted(PATH_AUTH_FIELDS)):
            self.assertIn('`json:"%s"`' % field, go)
        self.assertEqual(PATH_AUTH_FIELDS, {"has_authorization", "has_cookie"})


def path_row(seq, method="GET", pattern="/{h:0123456789abcdef}", masked=0, profile=PATH_PROFILE,
             key_ref="00112233445566ff", auth=None):
    """auth가 (Authorization, Cookie) 값 쌍이면 인증 헤더 칸이 있는 행, None이면 그 칸이 생기기 전 계측기의 행이다."""
    row = {"seq": seq, "method": method, "path_pattern": pattern, "masked_segments": masked,
           "path_profile_ref": profile, "path_key_ref": key_ref}
    if auth is not None:
        row.update(has_authorization=auth[0], has_cookie=auth[1])
    return row


@unittest.skipUnless(hasattr(os, "O_NOFOLLOW") and hasattr(os, "mkfifo"), "POSIX 전용")
class MeterPathRowsTests(unittest.TestCase):
    """수용시험 회차의 경로 기록 내용 검사(#372 리뷰)."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name, "meter")
        self.dir.mkdir(mode=0o700)
        os.chmod(self.dir, 0o700)
        self.meter = MeterFiles(self.dir)
        # 1·3은 전달, 2는 stream(전달), 4는 봉인 뒤 거부(경로 행 없음).
        self.engine = [engine_row(1), engine_row(2, "stream"), engine_row(3), engine_row(4, "rejected")]
        self.engine[2]["method"] = "POST"

    def write(self, rows, tail=""):
        (self.dir / PATH_LOG).write_text("".join(json.dumps(row) + "\n" for row in rows) + tail, encoding="utf-8")

    def check(self, code):
        with self.assertRaises(EvidenceError) as caught:
            self.meter.path_rows(self.engine)
        self.assertEqual(str(caught.exception), code)

    def test_one_row_per_forwarded_request(self):
        good = [path_row(1), path_row(2), path_row(3, "POST", "/{h:0123456789abcdef}/{id}", 1)]
        self.write(good)
        self.assertEqual(self.meter.path_rows(self.engine), good)
        # 인증 헤더 칸이 있는 계측기의 행도 받는다.
        with_auth = [path_row(1, auth=(True, False)), path_row(2, auth=(False, True)),
                     path_row(3, "POST", auth=(False, False))]
        self.write(with_auth)
        self.assertEqual(self.meter.path_rows(self.engine), with_auth)

    def test_missing_or_untrusted_file_is_rejected(self):
        self.check("meter_path_log_missing")
        (self.dir / PATH_LOG).mkdir()
        self.check("meter_file_untrusted")
        (self.dir / PATH_LOG).rmdir()
        os.mkfifo(self.dir / PATH_LOG, 0o600)
        self.check("meter_file_untrusted")

    def test_contents_must_match_the_request_log(self):
        good = [path_row(1), path_row(2), path_row(3, "POST")]
        for name, rows, tail in (
                ("empty", [], ""),
                # 행 수가 맞아도 잘린 꼬리가 있으면 거부한다(_rows가 그 줄을 버리므로 대조만으로는 못 잡는다).
                ("truncated tail", good, '{"seq": 4'),
                ("missing row", good[:2], ""),
                ("rejected request has a row", good + [path_row(4)], ""),
                ("method differs", [path_row(1), path_row(2), path_row(3, "GET")], ""),
                ("other profile", [path_row(1), path_row(2), path_row(3, "POST", profile="abc2lab.path-mask/v1")], ""),
                ("empty pattern", [path_row(1), path_row(2, pattern=""), path_row(3, "POST")], ""),
                ("negative masked", [path_row(1, masked=-1), path_row(2), path_row(3, "POST")], ""),
                ("bool masked", [path_row(1, masked=True), path_row(2), path_row(3, "POST")], ""),
                ("bad key ref", [path_row(1, key_ref="00112233445566FF"), path_row(2, key_ref="00112233445566FF"),
                                 path_row(3, "POST", key_ref="00112233445566FF")], ""),
                ("two keys", [path_row(1, key_ref="ffffffffffffffff"), path_row(2), path_row(3, "POST")], ""),
                # 칸이 있는 행과 없는 행이 섞이면 어느 회차 판인지 알 수 없다.
                ("mixed auth shape", [path_row(1, auth=(True, True)), path_row(2), path_row(3, "POST")], ""),
                ("int authorization", [path_row(1, auth=(1, False)), path_row(2, auth=(False, False)),
                                       path_row(3, "POST", auth=(False, False))], ""),
                ("null cookie", [path_row(1, auth=(False, False)), path_row(2, auth=(False, None)),
                                 path_row(3, "POST", auth=(False, False))], ""),
                ("string cookie", [path_row(1, auth=(False, False)), path_row(2, auth=(False, False)),
                                   path_row(3, "POST", auth=(False, "true"))], "")):
            with self.subTest(name):
                self.write(rows, tail)
                self.check("meter_path_log_invalid")
        self.write([dict(path_row(1), extra=1), path_row(2), path_row(3, "POST")])
        self.check("meter_row_invalid")
        # 칸 묶음의 한쪽만 있는 행은 어느 판의 모양도 아니다.
        self.write([dict(path_row(1), has_cookie=False), path_row(2), path_row(3, "POST")])
        self.check("meter_row_invalid")
        self.write([path_row(1), path_row(1), path_row(3, "POST")])
        self.check("meter_seq_invalid")


if __name__ == "__main__":
    unittest.main()
