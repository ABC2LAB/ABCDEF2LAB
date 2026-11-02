"""스냅샷 민감 필드의 계층별 별칭·안전값 조합 회귀."""

from __future__ import annotations

import copy
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import snapshot_collector
import snapshot_writer
from ground_truth import bac_category, core_gt
from snapshot_writer import SnapshotContext, SnapshotWriteError, write_snapshot


LIMIT = 1024 * 1024
MARKER = "SYNTHETIC-SENSITIVE-FIXTURE-MARKER"
KEYS = sorted({challenge.key for challenge in core_gt() + bac_category()})


def valid_raw(extra: dict | None = None) -> bytes:
    payload = {"data": [{"key": key, "solved": False} for key in KEYS]}
    if extra:
        payload.update(extra)
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


class TestSnapshotSensitiveFieldsProperties(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.context = SnapshotContext("run-sensitive", "model-sensitive", "reset-sensitive",
                                       "instance-sensitive", "execution-sensitive")
        self.raw = valid_raw()
        for target in ("socket.socket", "socket.create_connection", "socket.getaddrinfo",
                       "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("offline snapshot fixture only"))
            guard.start()
            self.addCleanup(guard.stop)

    def write(self, raw=None, phase="pre"):
        return write_snapshot(self.root, phase, self.raw if raw is None else raw,
                              self.context, max_bytes=LIMIT)

    def assert_writer_rejected(self, raw, *, message="원본에 민감 필드값이 있음"):
        with self.assertRaises(SnapshotWriteError) as caught:
            self.write(raw)
        self.assertEqual(str(caught.exception), message)
        self.assertNotIn(MARKER, str(caught.exception))
        self.assertEqual(list(self.root.iterdir()), [])

    def assert_collector_rejected(self, raw):
        with self.assertRaises(snapshot_collector.SnapshotCollectionError) as caught:
            snapshot_collector._reject_token_fields(raw)
        self.assertEqual(str(caught.exception), "스냅샷 body에 인증 필드값이 있음")
        self.assertNotIn(MARKER, str(caught.exception))
        self.assertEqual(list(self.root.iterdir()), [])

    def checked_write(self, raw):
        snapshot_collector._reject_token_fields(raw)
        return self.write(raw)

    def clear_phase(self):
        for child in (self.root / "pre").iterdir():
            child.unlink()
        (self.root / "pre").rmdir()

    def assert_saved(self, raw, *, checked=False):
        before_context = copy.deepcopy(self.context)
        result = self.checked_write(raw) if checked else self.write(raw)
        try:
            self.assertEqual((self.root / result.raw["path"]).read_bytes(), raw)
            expected = vars(self.context) | {"phase": "pre", "solved": {key: False for key in KEYS}}
            self.assertEqual(json.loads((self.root / result.normalized["path"]).read_bytes()), expected)
            self.assertEqual((result.challenge_count, result.solved_count), (len(KEYS), 0))
            self.assertEqual(self.context, before_context)
            self.assertEqual(sorted(path.name for path in (self.root / "pre").iterdir()),
                             ["normalized.json", "raw.json"])
        finally:
            self.clear_phase()

    def assert_chain(self, raw, *, writer_rejects, collector_rejects):
        if writer_rejects or collector_rejects:
            error = snapshot_collector.SnapshotCollectionError if collector_rejects else SnapshotWriteError
            message = "스냅샷 body에 인증 필드값이 있음" if collector_rejects else "원본에 민감 필드값이 있음"
            with self.assertRaises(error) as caught:
                self.checked_write(raw)
            self.assertEqual(str(caught.exception), message)
            self.assertNotIn(MARKER, str(caught.exception))
            self.assertEqual(list(self.root.iterdir()), [])
        else:
            self.assert_saved(raw, checked=True)

    @staticmethod
    def nested(value, depth, *, arrays_first=False):
        for level in range(depth):
            value = [value] if (level % 2 == 0) == arrays_first else {f"layer_{level}": value}
        return value

    def test_layer_literal_alias_expectations_cover_near_misses(self):
        expected = {
            "authorization": (True, False), "Authorization": (True, False),
            " cookie ": (False, False), "COOKIE": (True, False),
            "set-cookie": (True, False), "password": (True, False),
            "token": (True, False), " token ": (False, False),
            "secret": (True, False), "access_token": (True, True),
            "ACCESS_TOKEN": (True, True), "access-token": (False, True),
            "refresh_token": (True, True), "refresh-token": (False, True),
            "x-api-key": (False, True), "api_key": (False, True),
            "apikey": (False, True), "xapikey": (False, True),
            "credential": (False, False),
        }
        for name, (writer_rejects, collector_rejects) in expected.items():
            with self.subTest(name=name):
                raw = valid_raw({"nested": [{name: MARKER}]})
                if writer_rejects:
                    self.assert_writer_rejected(raw)
                else:
                    self.assert_saved(raw)
                if collector_rejects:
                    self.assert_collector_rejected(raw)
                else:
                    snapshot_collector._reject_token_fields(raw)
                self.assert_chain(raw, writer_rejects=writer_rejects, collector_rejects=collector_rejects)

    def test_sensitive_alias_value_matrix_covers_nested_depth_and_types(self):
        names = (("password", True, False), ("access_token", True, True), ("x-api-key", False, True))
        values = (None, "[REDACTED]", MARKER, 0, False, "", [], {})
        for name, writer_blocks, collector_blocks in names:
            for depth in range(5):
                for arrays_first in (False, True):
                    for index, value in enumerate(values):
                        with self.subTest(name=name, depth=depth, arrays_first=arrays_first, value=index):
                            raw = valid_raw({"extra": self.nested({name: value}, depth, arrays_first=arrays_first)})
                            safe = index < 2
                            writer_rejects = writer_blocks and not safe
                            collector_rejects = collector_blocks and not safe
                            if writer_rejects:
                                self.assert_writer_rejected(raw)
                            else:
                                self.assert_saved(raw)
                            self.assert_chain(raw, writer_rejects=writer_rejects,
                                              collector_rejects=collector_rejects)

    def test_writer_case_insensitive_names_and_safe_values_are_explicit(self):
        names = ("Authorization", "COOKIE", "Set-Cookie", "Password", "TOKEN", "Secret",
                 "ACCESS_TOKEN", "Refresh_Token")
        for name in names:
            for safe in (None, "[REDACTED]"):
                with self.subTest(name=name, safe=safe):
                    raw = valid_raw({"nested": [{name: safe}]})
                    self.assert_saved(raw, checked=True)

    def test_collector_alias_normalization_rejects_nested_unsafe_values(self):
        aliases = ("x-api-key", "api-key", "apikey", "xapikey", "access-token", "refresh-token")
        variants = ("X-API-KEY", " api_key ", "Access_Token", "refresh-token")
        for name in aliases + variants:
            with self.subTest(name=name):
                self.assert_collector_rejected(
                    json.dumps({"outer": [{"inner": {name: MARKER}}]}).encode())

    def test_collector_redacted_and_null_alias_values_are_safe(self):
        aliases = ("x-api-key", "api-key", "apikey", "xapikey", "access-token", "refresh-token")
        for name in aliases:
            for value in (None, "[REDACTED]"):
                with self.subTest(name=name, value=value):
                    snapshot_collector._reject_token_fields(
                        json.dumps({"nested": [{name: value}]}).encode())

    def test_collector_last_wins_can_hide_duplicate_but_writer_rejects_it(self):
        prefix = b'{"data":' + json.dumps([{"key": key, "solved": False} for key in KEYS]).encode()
        for name in (b"api-key", b"access_token"):
            for safe in (b"null", b'"[REDACTED]"'):
                with self.subTest(name=name, safe=safe):
                    raw = (prefix + b',"nested":{"' + name + b'":"' + MARKER.encode()
                           + b'","' + name + b'":' + safe + b'}}')
                    snapshot_collector._reject_token_fields(raw)
                    self.assert_writer_rejected(raw, message="원본 JSON key 중복")
                    with self.assertRaises(SnapshotWriteError) as caught:
                        self.checked_write(raw)
                    self.assertEqual(str(caught.exception), "원본 JSON key 중복")
                    self.assertEqual(list(self.root.iterdir()), [])

    def test_input_bytes_and_context_are_unchanged_after_safe_write(self):
        raw = valid_raw({"nested": [{"api-key": "[REDACTED]", "token": None}]})
        original_raw, original_context = raw[:], copy.deepcopy(self.context)
        self.write(raw)
        self.assertEqual(raw, original_raw)
        self.assertEqual(self.context, original_context)

    def test_mutant_writer_that_skips_sensitive_check_fails_same_rejection_oracle(self):
        unsafe = valid_raw({"nested": [{"password": MARKER}]})
        self.assert_writer_rejected(unsafe)
        with patch.object(snapshot_writer, "_SENSITIVE_FIELDS", frozenset()):
            with self.assertRaises(AssertionError):
                self.assert_writer_rejected(unsafe)
        # 약화 구현은 실제로 파일을 만들었어야 같은 oracle이 검출한 것이다.
        self.assertTrue((self.root / "pre" / "raw.json").exists())
        self.assertEqual((self.root / "pre" / "raw.json").read_bytes(), unsafe)
        self.clear_phase()
        self.assertEqual(list(self.root.iterdir()), [])

    def test_mutant_collector_that_bypasses_alias_guard_is_caught_by_rejection_oracle(self):
        unsafe = json.dumps({"nested": [{"access_token": MARKER}]}).encode()

        def mutant(_raw):
            return None

        self.assert_collector_rejected(unsafe)
        with patch.object(snapshot_collector, "_reject_token_fields", side_effect=mutant):
            self.assertIsNone(snapshot_collector._reject_token_fields(unsafe))
            with self.assertRaises(AssertionError):
                self.assert_collector_rejected(unsafe)

    def test_sensitive_validation_has_no_process_or_network_side_effect(self):
        safe = valid_raw({"nested": [{"x-api-key": "[REDACTED]", "password": None}]})
        self.write(safe)
        self.assertEqual(len(list((self.root / "pre").iterdir())), 2)


if __name__ == "__main__":
    unittest.main()
