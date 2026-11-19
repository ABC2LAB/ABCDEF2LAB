"""분리 배치 O 회차의 E② v3 저널 수집(runner_e2v3) 시험.

동찬님 답(2026-11-19): 회차별 감사 키를 러너가 만들어 제품과 검증기에 같은 키를 넣고, 저장소·로그·결과물에 남기지 않는다.
봉인된 최종 head 기준으로 검증하고, 현재성 확인에 실패하면 회차 무효가 아니라 통제 대조 미수집이다(고정 초안 §20).
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_e2v3 import (  # noqa: E402
    AUDIT_KEY_BYTES, MAX_HEAD_BYTES, MAX_JOURNAL_BYTES, OUTCOME_COLLECTED, OUTCOME_UNCOLLECTED, E2V3Invalid, audit_key_ref, collect,
    new_audit_key, parse_settings,
)

HEAD = {"Version": "e2-http-v3", "JournalID": "a" * 32, "Sequence": 3, "MAC": "b" * 64,
        "Scope": {"RunRef": "run-d1", "TaskRef": "42", "CaseRef": "", "ResetRef": "", "PolicyRef": "policy-d1"}}
SETTINGS = {"audit_key_variable": "PRODUCT_E2_AUDIT_KEY_FILE", "volume": "product-state", "directory": "runs",
            "subpath": ["{draft_id}", "e2"], "journal_name": "events.jsonl", "head_name": "sealed-head.json", "uid": 1000,
            "verify_tool": "/opt/abc2lab/e2-audit-verify", "verify_timeout_seconds": 120}


class TestSettings(unittest.TestCase):
    def test_all_fields_are_explicit(self):
        settings = parse_settings(SETTINGS)
        self.assertEqual((settings.audit_key_variable, settings.resolved_subpath("draft-1")),
                         ("PRODUCT_E2_AUDIT_KEY_FILE", ("draft-1", "e2")))
        bad = {
            "missing": {k: v for k, v in SETTINGS.items() if k != "uid"},
            "extra": dict(SETTINGS, journal_path="/x"),
            "variable": dict(SETTINGS, audit_key_variable="lower"),
            "volume": dict(SETTINGS, volume="../x"),
            "directory": dict(SETTINGS, directory="a/b"),
            "subpath_dots": dict(SETTINGS, subpath=[".."]),
            "subpath_long": dict(SETTINGS, subpath=["a", "b", "c"]),
            "same_names": dict(SETTINGS, head_name="events.jsonl"),
            "name": dict(SETTINGS, journal_name="a/b"),
            "uid": dict(SETTINGS, uid=True),
            "tool_relative": dict(SETTINGS, verify_tool="e2-audit-verify"),
            "timeout": dict(SETTINGS, verify_timeout_seconds=0),
            "not_dict": [],
        }
        for name, raw in bad.items():
            with self.subTest(name=name):
                with self.assertRaises(ValueError):
                    parse_settings(raw)

    def test_draft_id_must_make_a_plain_part(self):
        with self.assertRaises(E2V3Invalid):
            parse_settings(SETTINGS).resolved_subpath("a/b")


class TestAuditKey(unittest.TestCase):
    def test_new_key_is_fresh_and_never_another_key(self):
        path_key = bytes(range(32))
        key = new_audit_key(lambda n: bytes([7]) * n, other_keys=(path_key,))
        self.assertEqual(len(key), AUDIT_KEY_BYTES)
        for source in (lambda n: path_key, lambda n: b"short", lambda n: "x" * n):
            with self.subTest(source=source):
                with self.assertRaises(E2V3Invalid):
                    new_audit_key(source, other_keys=(path_key,))

    def test_reference_is_not_the_key(self):
        key = bytes([7]) * 32
        ref = audit_key_ref(key)
        self.assertRegex(ref, r"\A[0-9a-f]{16}\Z")
        self.assertNotIn(key.hex()[:16], ref)
        self.assertNotEqual(ref, audit_key_ref(bytes([8]) * 32))


class CollectCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.key = self.root / "e2-audit.key"
        fd = os.open(self.key, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as stream:
            stream.write(bytes([9]) * AUDIT_KEY_BYTES)
        self.calls = []
        self.reply = (0, json.dumps({"Checkpoint": HEAD, "Events": 3}).encode() + b"\n", b"")

    def tearDown(self):
        self._tmp.cleanup()

    def verifier(self, args):
        self.calls.append(args)
        return self.reply

    def collect(self, journal=b'{"Kind":"header"}\n', head=None, copy="copy"):
        return collect(run_verifier=self.verifier, copy_directory=self.root / copy, key_path=self.key, journal=journal,
                       head=json.dumps(HEAD).encode() if head is None else head, key_ref="0123456789abcdef")


class TestCollect(CollectCase):
    def test_verified_against_the_sealed_head(self):
        row = self.collect()
        self.assertEqual((row["outcome"], row["reason"], row["events"], row["head"]), (OUTCOME_COLLECTED, None, 3, HEAD))
        (args,) = self.calls
        copy = self.root / "copy"
        self.assertEqual(args, ["--journal-format", "e2-http-v3", "--journal", str(copy / "journal.jsonl"),
                                "--checkpoint", str(copy / "head.json"), "--key-file", str(self.key)])
        self.assertEqual(oct(os.stat(copy).st_mode & 0o777), "0o700")
        self.assertEqual({oct(os.stat(copy / n).st_mode & 0o777) for n in ("journal.jsonl", "head.json")}, {"0o600"})
        self.assertNotIn((bytes([9]) * AUDIT_KEY_BYTES).hex(), json.dumps(row))  # 키 값은 기록에 없다
        self.assertEqual(row["audit_key_ref"], "0123456789abcdef")

    def test_product_side_gaps_leave_the_run_valid(self):
        cases = {
            "no_journal": (dict(journal=None), "e2_v3_export_unavailable"),
            "no_head": (dict(head=None), None),
            "too_large": (dict(journal=b"x" * (MAX_JOURNAL_BYTES + 1)), "e2_v3_journal_too_large"),
            "head_v2": (dict(head=json.dumps(dict(HEAD, Version="e2-http-v2")).encode()), "e2_v3_head_invalid"),
            "head_extra": (dict(head=json.dumps(dict(HEAD, Epoch=1)).encode()), "e2_v3_head_invalid"),
            "head_big": (dict(head=b" " * (MAX_HEAD_BYTES + 1)), "e2_v3_head_invalid"),
            "head_text": (dict(head=b"\xff"), "e2_v3_head_invalid"),
            "head_negative": (dict(head=json.dumps(dict(HEAD, Sequence=-1)).encode()), "e2_v3_head_invalid"),
        }
        for name, (changes, reason) in cases.items():
            with self.subTest(name=name):
                self.calls.clear()
                if name == "no_head":
                    row = collect(run_verifier=self.verifier, copy_directory=self.root / name, key_path=self.key,
                                  journal=b"{}", head=None, key_ref="r")
                    reason = "e2_v3_export_unavailable"
                else:
                    row = self.collect(copy=name, **changes)
                self.assertEqual((row["outcome"], row["reason"]), (OUTCOME_UNCOLLECTED, reason))
                self.assertEqual(self.calls, [])  # 검증기를 부르지 않는다

    def test_integrity_or_currency_failure_is_uncollected(self):
        for code in ("audit_integrity_failed", "audit_input_invalid", "audit_verification_limit_exceeded"):
            with self.subTest(code=code):
                self.reply = (1, b"", code.encode() + b"\n")
                row = self.collect(copy=code)
                self.assertEqual((row["outcome"], row["reason"], row["head"]), (OUTCOME_UNCOLLECTED, "e2_v3_" + code, HEAD))

    def test_runner_side_failures_invalidate(self):
        bad_summary = [json.dumps({"Checkpoint": dict(HEAD, Sequence=2), "Events": 3}).encode(),
                       json.dumps({"Checkpoint": HEAD, "Events": -1}).encode(), json.dumps({"Checkpoint": HEAD}).encode(), b"x"]
        replies = [(1, b"", b"audit_input_unavailable\n"), (1, b"", b"audit_verification_canceled\n"), (2, b"", b"usage")]
        replies += [(0, body, b"") for body in bad_summary]
        for index, reply in enumerate(replies):
            with self.subTest(index=index):
                self.reply = reply
                with self.assertRaises(E2V3Invalid):
                    self.collect(copy=f"c{index}")

    def test_key_file_must_be_private_and_the_right_size(self):
        os.chmod(self.key, 0o640)
        with self.assertRaises(E2V3Invalid):
            self.collect()
        os.chmod(self.key, 0o600)
        with open(self.key, "ab") as stream:
            stream.write(b"x")
        with self.assertRaises(E2V3Invalid):
            self.collect(copy="c2")
        link = self.root / "link.key"
        os.symlink(self.key, link)
        with self.assertRaises(E2V3Invalid):
            collect(run_verifier=self.verifier, copy_directory=self.root / "c3", key_path=link, journal=b"{}",
                    head=json.dumps(HEAD).encode(), key_ref="r")

    def test_copy_directory_must_be_new(self):
        (self.root / "copy").mkdir()
        with self.assertRaises(E2V3Invalid):
            self.collect()


if __name__ == "__main__":
    unittest.main()
