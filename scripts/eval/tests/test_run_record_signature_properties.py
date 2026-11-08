"""v1 러너 기록의 서명·정규 JSON 표현 경계 생성형 회귀.

합성 회차만 사용한다. 이 시험은 HMAC 키의 운영 출처나 실제 러너 격리를 증명하지
않으며, reader가 잘못된 서명으로 입력을 반환하는 회귀를 막는다.
"""

from __future__ import annotations

import contextlib
import copy
import hashlib
import hmac
import io
import itertools
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from manifest import load_manifests
import run_record
from run_fixture import KEYS, RunFixture
from run_record import RunInputs, RunRecordError, load_run_inputs


KEY = b"signature-property-test-key-0123456789"


def _canonical(payload: dict) -> bytes:
    """독립 HMAC oracle: product signer를 호출하지 않고 계약을 재현한다."""
    return json.dumps(payload, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=False, allow_nan=False).encode("utf-8")


def _signature(payload: dict, key: bytes) -> str:
    return hmac.new(key, _canonical(payload), hashlib.sha256).hexdigest()


def _reverse_objects(value: object) -> object:
    if isinstance(value, dict):
        return {key: _reverse_objects(value[key]) for key in reversed(list(value))}
    if isinstance(value, list):
        return [_reverse_objects(item) for item in value]
    return value


def _tree_snapshot(root: Path) -> dict[str, tuple[str, object]]:
    """Capture every private-root entry, including directories and symlinks."""
    result: dict[str, tuple[str, object]] = {}
    for current, dirs, files in os.walk(root, followlinks=False):
        current_path = Path(current)
        for name in dirs + files:
            path = current_path / name
            relative = str(path.relative_to(root))
            if path.is_symlink():
                result[relative] = ("symlink", os.readlink(path))
            elif path.is_dir():
                result[relative] = ("dir", None)
            else:
                result[relative] = ("file", path.read_bytes())
    return result


class SignatureProperties(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self._guards = contextlib.ExitStack()
        self.addCleanup(self._guards.close)
        self._guards.enter_context(patch("socket.socket", side_effect=AssertionError("socket")))
        self._guards.enter_context(patch("socket.create_connection", side_effect=AssertionError("connect")))
        self._guards.enter_context(patch("socket.getaddrinfo", side_effect=AssertionError("dns")))
        self._guards.enter_context(patch("subprocess.Popen", side_effect=AssertionError("process")))
        self._guards.enter_context(patch("os.system", side_effect=AssertionError("system")))
        self.fixture = RunFixture(self.tmp.name, KEY)
        self.run = self.fixture.make("sig", [], solved={})
        self.manifest_path = Path(self.tmp.name) / "manifest.json"
        self._write_manifest()

    def _write_manifest(self):
        self.manifest_path.write_text(json.dumps({"runs": [self.run]}), encoding="utf-8")
        self.manifest = load_manifests(str(self.manifest_path))[0]

    def _receipt_path(self) -> Path:
        return Path(self.tmp.name) / self.run["run_record_path"]

    def _payload(self) -> dict:
        return self.fixture.receipt(self.run)

    def _write_signed(self, payload: dict, key: bytes = KEY, *, ensure_ascii=False,
                      sort_keys=False, separators=(",", ":")) -> bytes:
        signed = dict(payload, signature=_signature(payload, key))
        data = json.dumps(signed, ensure_ascii=ensure_ascii, sort_keys=sort_keys,
                          separators=separators).encode("utf-8")
        self._receipt_path().write_bytes(data)
        return data

    def _guarded_load(self, key: bytes = KEY):
        stdout, stderr = io.StringIO(), io.StringIO()
        before = _tree_snapshot(Path(self.tmp.name))
        manifest_before, run_before = copy.deepcopy(self.manifest), copy.deepcopy(self.run)
        try:
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                return load_run_inputs(self.manifest, key, lambda _record: None)
        finally:
            self.assertEqual(stdout.getvalue(), "")
            self.assertEqual(stderr.getvalue(), "")
            self.assertEqual(_tree_snapshot(Path(self.tmp.name)), before)
            self.assertEqual(self.manifest, manifest_before)
            self.assertEqual(self.run, run_before)

    def _checked_sign(self, payload, key=KEY):
        before = copy.deepcopy(payload)
        out, err = io.StringIO(), io.StringIO()
        try:
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                return run_record.sign_run_record(payload, key)
        finally:
            self.assertEqual(payload, before)
            self.assertEqual(out.getvalue(), "")
            self.assertEqual(err.getvalue(), "")

    def _assert_signed(self, payload, key, expected):
        signed = self._checked_sign(payload, key)
        self.assertEqual(signed, dict(payload, signature=expected))
        return signed

    def _assert_success(self, key=KEY):
        actual = self._guarded_load(key)
        self.assertEqual(actual, self._literal_expected())
        for states in (actual.pre, actual.post):
            for value in states.values():
                self.assertIs(value, False)
        return actual

    def _assert_rejected(self, key=KEY, message="러너 기록 서명 불일치"):
        with self.assertRaisesRegex(RunRecordError, message):
            self._guarded_load(key)

    def _literal_expected(self) -> RunInputs:
        false_state = {key: False for key in KEYS}
        return RunInputs(
            pre=false_state,
            post=dict(false_state),
            records={"run_id": "run-sig", "model_tag": "test-model",
                     "reset_generation": "gen-sig", "records": []},
            traffic=None,
            covered=None,
            engine_solved=frozenset(),
            execution_id="execution-sig",
            fixture_fingerprint=hashlib.sha256(b"fixture-v1").hexdigest(),
            environment_fingerprint=hashlib.sha256(b"environment-v1").hexdigest(),
        )

    def test_independent_hmac_oracle_matches_product_signature(self):
        payload = self._payload()
        before = json.loads(json.dumps(payload))
        self._assert_signed(payload, KEY, _signature(payload, KEY))
        self.assertEqual(payload, before)
        self.assertNotIn("signature", payload)

    def test_key_length_and_type_boundaries_are_explicit(self):
        payload = self._payload()
        for length in (31, 32, 33, 64):
            with self.subTest(length=length):
                key = bytes([length % 251]) * length
                if length < 32:
                    with self.assertRaisesRegex(RunRecordError, "최소 32 bytes"):
                        self._checked_sign(payload, key)
                else:
                    self._assert_signed(payload, key, _signature(payload, key))
                    self._write_signed(payload, key)
                    self._assert_success(key)
        self._write_signed(payload, KEY)
        with self.assertRaisesRegex(RunRecordError, "최소 32"):
            self._guarded_load(bytes(31))
        for bad_key in (None, bytearray(KEY), "key"):
            with self.subTest(type=type(bad_key).__name__):
                with self.assertRaisesRegex(RunRecordError, "최소 32 bytes"):
                    self._checked_sign(payload, bad_key)
                self._assert_rejected(bad_key, "최소 32 bytes")

    def test_equivalent_object_order_whitespace_and_escapes_load_identically(self):
        payload = self._payload()
        variants = itertools.product(
            ("우리@test", "café", "😀/\"\\", "e\u0301"), (False, True),
            (False, True), (False, True), ((",", ":"), (", ", ": "), (",\n", " : ")))
        for condition, reverse, nested_reverse, ensure_ascii, separators in variants:
            with self.subTest(condition=condition, reverse=reverse, nested_reverse=nested_reverse,
                              ensure_ascii=ensure_ascii, separators=separators):
                self.run["condition"] = condition
                self._write_manifest()
                top = dict(payload, condition=condition)
                variant = dict(reversed(list(top.items()))) if reverse else top
                if nested_reverse:
                    variant = dict(variant, timeline=_reverse_objects(variant["timeline"]),
                                   artifacts=_reverse_objects(variant["artifacts"]))
                self._assert_signed(variant, KEY, _signature(top, KEY))
                self._write_signed(variant, ensure_ascii=ensure_ascii,
                                   sort_keys=False, separators=separators)
                self._assert_success()

    def test_same_unicode_code_points_with_json_escapes_have_same_oracle(self):
        payload = self._payload()
        # condition contains Korean text. Escaping changes wire bytes, not decoded values.
        raw = json.dumps(dict(payload, condition=payload["condition"]),
                         ensure_ascii=True, separators=(",", ":"))
        self._receipt_path().write_bytes(
            (raw[:-1] + ',"signature":"' + _signature(payload, KEY) + '"}').encode("utf-8"))
        expected = self._literal_expected()
        self.assertEqual(self._guarded_load(), expected)
        self.assertEqual(json.loads(self._receipt_path().read_bytes())["condition"], payload["condition"])

    def test_unresigned_semantic_tamper_hits_signature_guard(self):
        payload = self._payload()
        self._assert_success()
        changes = (("condition", "changed"), ("fixture_fingerprint", "a" * 64),
                   ("environment_fingerprint", "b" * 64), ("repeat_index", 1),
                   ("requests_drained", False), ("version", 2))
        for field, value in changes:
            with self.subTest(field=field):
                signed = dict(payload, signature=_signature(payload, KEY))
                signed[field] = value
                self._receipt_path().write_text(json.dumps(signed), encoding="utf-8")
                self._assert_rejected()

    def test_signature_shape_and_wrong_key_rejected_with_same_oracle(self):
        payload = self._payload()
        expected = _signature(payload, KEY)
        self.assertTrue(hmac.compare_digest(expected, _signature(payload, KEY)))
        wrong_key = b"wrong-signature-key-012345678901234"
        self.assertFalse(hmac.compare_digest(expected, _signature(payload, wrong_key)))
        for signature in ("", "0" * 63, "0" * 65, "0" * 64, expected.upper(),
                          "g" * 64, "한" * 64, 123, True, None, [], {}):
            with self.subTest(signature=repr(signature)):
                self._receipt_path().write_text(
                    json.dumps(dict(payload, signature=signature)), encoding="utf-8")
                self._assert_rejected()
        self._write_signed(payload)
        self._assert_rejected(wrong_key)

    def test_duplicate_receipt_key_is_rejected_before_semantic_read(self):
        payload = self._payload()
        encoded = json.dumps(dict(payload, signature=_signature(payload, KEY)),
                             separators=(",", ":"))
        for field, value in (("signature", _signature(payload, KEY)),
                             ("size", payload["artifacts"]["pre"]["size"]),
                             ("sequence", 0)):
            with self.subTest(field=field):
                needle = json.dumps(field) + ":" + json.dumps(value)
                self.assertIn(needle, encoded)
                self._receipt_path().write_text(encoded.replace(needle, needle + "," + needle, 1),
                                                encoding="utf-8")
                self._assert_rejected(message="JSON key 중복")

    def test_unsigned_receipt_and_existing_signature_are_rejected(self):
        payload = self._payload()
        self._receipt_path().write_text(json.dumps(payload), encoding="utf-8")
        self._assert_rejected(message="러너 기록: 필수/미등록 필드 오류")
        payload["signature"] = "0" * 64
        with self.assertRaisesRegex(RunRecordError, "서명 입력에 signature 금지"):
            self._checked_sign(payload)

    def test_valid_receipt_preserves_all_case_files_and_no_external_calls(self):
        paths = [path for path in Path(self.tmp.name).rglob("*") if path.is_file()]
        before = {path: path.read_bytes() for path in paths}
        self._assert_success()
        self.assertEqual({path: path.read_bytes() for path in paths}, before)

    def test_mutant_compare_digest_bypass_is_caught_by_independent_oracle(self):
        payload = self._payload()
        self._write_signed(payload)
        wrong_key = b"wrong-signature-key-012345678901234"
        self._assert_success()
        self._assert_rejected(wrong_key)
        self.assertFalse(hmac.compare_digest(_signature(payload, KEY), _signature(payload, wrong_key)))
        with patch("run_record.hmac.compare_digest", return_value=True):
            self._assert_success(wrong_key)
            with self.assertRaises(AssertionError):
                self._assert_rejected(wrong_key)

    def test_mutant_unsorted_canonicalization_is_caught_by_independent_oracle(self):
        payload = self._payload()
        expected = _signature(payload, KEY)
        self._assert_signed(payload, KEY, expected)
        unsorted = json.dumps(payload, separators=(",", ":"), ensure_ascii=False,
                              allow_nan=False).encode("utf-8")
        original_new = hmac.new

        def hash_unsorted(key, msg, digestmod):
            return original_new(key, unsorted, digestmod)

        with patch("run_record.hmac.new", side_effect=hash_unsorted):
            self.assertNotEqual(self._checked_sign(payload)["signature"], expected)
            with self.assertRaises(AssertionError):
                self._assert_signed(payload, KEY, expected)


if __name__ == "__main__":
    unittest.main()
