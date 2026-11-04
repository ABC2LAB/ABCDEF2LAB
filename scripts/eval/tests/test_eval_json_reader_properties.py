"""평가 입력 JSON reader의 bounded·strict 경계를 검증하는 결정적 fuzz 회귀."""

from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import baseline_extract
import preflight
import profile_fingerprint
from baseline_extract import BaselineExtractError
from preflight import PreflightError
from profile_fingerprint import ProfileError


def _write(root: Path, name: str, raw: bytes) -> Path:
    path = root / name
    path.write_bytes(raw)
    return path


class _SpyStream:
    def __init__(self, raw: bytes, error: Exception | None = None):
        self.raw = raw
        self.error = error
        self.read_sizes = []

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False

    def read(self, size=-1):
        self.read_sizes.append(size)
        if self.error is not None:
            raise self.error
        return self.raw if size < 0 else self.raw[:size]


class TestEvalJsonReaderProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "socket.getaddrinfo",
                       "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("JSON readers are offline"))
            guard.start()
            self.addCleanup(guard.stop)

    def _reject(self, loader, path, error_type, code):
        before = path.read_bytes() if isinstance(path, Path) and path.is_file() else None
        with self.assertRaises(error_type) as caught:
            loader(path)
        self.assertEqual(str(caught.exception), code)
        self.assertNotIn("SYNTHETIC-PRIVATE", str(caught.exception))
        if before is not None:
            self.assertEqual(path.read_bytes(), before)

    def test_duplicate_keys_are_rejected_at_root_and_nested_escaped_positions(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for key in ("SYNTHETIC-PRIVATE", "é-SYNTHETIC-PRIVATE", "한글-SYNTHETIC-PRIVATE"):
                literal = json.dumps(key, ensure_ascii=False)
                escaped = '"' + ''.join(f'\\u{ord(char):04x}' for char in key) + '"'
                for spelling in (literal, escaped):
                    for location in ((), ("object",), ("array",), ("object", "array", "object"),
                                     ("array", "object", "array")):
                        wire = "{" + literal + ":1," + spelling + ":2}"
                        for kind in location:
                            wire = '{"outer":' + wire + '}' if kind == "object" else '[' + wire + ']'
                        path = _write(root, "duplicate.json", wire.encode("utf-8"))
                        readers = (("baseline", lambda value: baseline_extract._read_json(value, 4096),
                                    BaselineExtractError, "json: DUPLICATE_KEY"),
                                   ("profile", profile_fingerprint._read_profile, ProfileError, "json: DUPLICATE_KEY"),
                                   ("preflight", preflight.load_preflight, PreflightError, "config: DUPLICATE_KEY"))
                        for name, loader, error, code in readers:
                            with self.subTest(key=key, spelling=spelling, location=location, reader=name):
                                self._reject(loader, path, error, code)

    def test_valid_nested_unicode_and_key_order_are_preserved_as_json_values(self):
        payload = {"message": "합성\u2603", "nested": [{"x": 1}, {"y": [True, None, "끝"]}]}
        raw = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        with tempfile.TemporaryDirectory() as tmp:
            path = _write(Path(tmp), "valid.json", raw)
            self.assertEqual(baseline_extract._read_json(path, len(raw)), payload)
            self.assertEqual(profile_fingerprint._read_profile(path), payload)
            self.assertEqual(preflight.load_preflight(path), payload)

    def test_invalid_utf8_malformed_json_and_nonfinite_constants_have_fixed_errors(self):
        cases = ((b"{\"SYNTHETIC-PRIVATE\":\xff}", "utf8"), (b"{\"SYNTHETIC-PRIVATE\":", "malformed"),
                 (b"{\"x\":NaN}", "nan"), (b"{\"x\":Infinity}", "infinity"))
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for raw, kind in cases:
                with self.subTest(kind=kind):
                    path = _write(root, "invalid.json", raw)
                    self._reject(lambda value: baseline_extract._read_json(value, 4096), path,
                                 BaselineExtractError, "json: " + ("NONFINITE_NUMBER" if kind in ("nan", "infinity") else "INVALID_JSON"))
                    self._reject(profile_fingerprint._read_profile, path, ProfileError,
                                 "json: " + ("NONFINITE_NUMBER" if kind in ("nan", "infinity") else "INVALID_JSON"))
                    self._reject(preflight.load_preflight, path, PreflightError,
                                 "config: " + ("NONFINITE_NUMBER" if kind in ("nan", "infinity") else "INVALID_JSON"))

    def test_each_reader_enforces_exact_bytes_cap_without_reading_unbounded_input(self):
        loaders = (
            ("baseline", lambda path, maximum: baseline_extract._read_json(path, maximum),
             BaselineExtractError, "json: TOO_LARGE", 96),
            ("profile", lambda path, maximum: profile_fingerprint._read_profile(path),
             ProfileError, "json: TOO_LARGE", profile_fingerprint.MAX_INPUT_BYTES),
            ("preflight", lambda path, maximum: preflight.load_preflight(path),
             PreflightError, "config: TOO_LARGE", preflight.MAX_CONFIG_BYTES),
        )
        with tempfile.TemporaryDirectory() as tmp:
            for name, loader, error_type, code, maximum in loaders:
                for value in ("SYNTHETIC-PRIVATE", "합성-é-SYNTHETIC-PRIVATE"):
                    base = json.dumps({"safe": value}, ensure_ascii=False).encode("utf-8")
                    for size in (maximum - 1, maximum, maximum + 1):
                        with self.subTest(reader=name, size=size, value=value):
                            raw = base + b" " * (size - len(base))
                            self.assertEqual(len(raw), size)
                            path = _write(Path(tmp), "bounded.json", raw)
                            if size <= maximum:
                                self.assertEqual(loader(path, maximum), {"safe": value})
                                self.assertEqual(path.read_bytes(), raw)
                            else:
                                self._reject(lambda value, fn=loader, cap=maximum: fn(value, cap),
                                             path, error_type, code)

    def test_loader_specific_number_contracts_are_explicit(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            finite = _write(root, "finite.json", b"{\"n\":1e3}")
            huge = _write(root, "huge.json", b"{\"n\":1e999}")
            self.assertEqual(baseline_extract._read_json(finite, 4096)["n"], 1000.0)
            self.assertEqual(profile_fingerprint._read_profile(finite)["n"], profile_fingerprint.Decimal("1e3"))
            self.assertEqual(preflight.load_preflight(finite)["n"], 1000.0)
            self._reject(lambda value: baseline_extract._read_json(value, 4096), huge,
                         BaselineExtractError, "json: NONFINITE_NUMBER")
            self.assertEqual(profile_fingerprint._read_profile(huge)["n"], profile_fingerprint.Decimal("1e999"))
            self.assertEqual(preflight.load_preflight(huge)["n"], float("inf"))

    def test_reader_failures_do_not_mutate_files_or_execute_side_effects(self):
        raw = b"{\"safe\": [1, 2, 3]}"
        with tempfile.TemporaryDirectory() as tmp:
            path = _write(Path(tmp), "safe.json", raw)
            before = path.read_bytes()
            baseline_extract._read_json(path, len(raw))
            profile_fingerprint._read_profile(path)
            preflight.load_preflight(path)
            self.assertEqual(path.read_bytes(), before)

    def test_read_is_bounded_and_read_failures_have_fixed_errors(self):
        readers = (
            ("baseline", baseline_extract._read_json, BaselineExtractError, "json: READ_FAILED", 17),
            ("profile", profile_fingerprint._read_profile, ProfileError, "json: READ_FAILED", profile_fingerprint.MAX_INPUT_BYTES),
            ("preflight", preflight.load_preflight, PreflightError, "config: READ_FAILED", preflight.MAX_CONFIG_BYTES),
        )
        for name, loader, error_type, code, maximum in readers:
            with self.subTest(reader=name):
                stream = _SpyStream(b"{}")
                with patch("builtins.open", return_value=stream):
                    result = loader("synthetic.json") if name != "baseline" else loader("synthetic.json", maximum)
                self.assertEqual(result, {})
                self.assertEqual(stream.read_sizes, [maximum + 1])
                for failure in (OSError("SYNTHETIC-PRIVATE"), ValueError("SYNTHETIC-PRIVATE")):
                    stream = _SpyStream(b"", failure)
                    with patch("builtins.open", return_value=stream):
                        if name == "baseline":
                            self._reject(lambda value, fn=loader, cap=maximum: fn(value, cap), "synthetic.json", error_type, code)
                        else:
                            self._reject(loader, "synthetic.json", error_type, code)
                    self.assertEqual(stream.read_sizes, [maximum + 1])
                self._reject(loader if name != "baseline" else lambda value: loader(value, maximum),
                             "/definitely/missing/synthetic.json", error_type, code)

    def test_generated_depth_preserves_values_or_sanitizes_parser_depth_errors(self):
        before = sys.getrecursionlimit()
        readers = (("baseline", lambda path: baseline_extract._read_json(path, 16 * 1024),
                    BaselineExtractError, "json: INVALID_JSON"),
                   ("profile", profile_fingerprint._read_profile, ProfileError, "json: INVALID_JSON"),
                   ("preflight", preflight.load_preflight, PreflightError, "config: INVALID_JSON"))
        with tempfile.TemporaryDirectory() as tmp:
            for depth in (0, 1, 2, 8, 64, 128, 512, 4096):
                raw = ("[" * depth + "0" + "]" * depth).encode()
                path = _write(Path(tmp), "depth.json", raw)
                for name, loader, error, code in readers:
                    with self.subTest(reader=name, depth=depth):
                        try:
                            value = loader(path)
                        except error as caught:
                            # JSON's depth ceiling depends on the interpreter, rather than a reader policy.
                            self.assertGreater(depth, 128)
                            self.assertEqual(str(caught), code)
                        else:
                            observed = 0
                            while isinstance(value, list):
                                self.assertEqual(len(value), 1)
                                observed += 1
                                value = value[0]
                            self.assertEqual((observed, value), (depth, 0))
                        self.assertEqual(path.read_bytes(), raw)
                        self.assertEqual(sys.getrecursionlimit(), before)
            # Explicitly exercise the error wrapper even on interpreters that accept every bounded depth above.
            path = _write(Path(tmp), "injected-depth.json", b"[]")
            for name, loader, error, code in readers:
                with self.subTest(reader=name, injected_depth_error=True):
                    with patch("json.loads", side_effect=RecursionError("SYNTHETIC-PRIVATE")):
                        self._reject(loader, path, error, code)
        self.assertEqual(sys.getrecursionlimit(), before)

    def test_duplicate_key_mutation_oracle_detects_weak_json_loaders(self):
        raw = b"{\"safe\":1,\"safe\":2}"
        with tempfile.TemporaryDirectory() as tmp:
            path = _write(Path(tmp), "mutant.json", raw)
            self._reject(lambda value: baseline_extract._read_json(value, 4096), path,
                         BaselineExtractError, "json: DUPLICATE_KEY")
            with patch.object(baseline_extract, "_unique_object", side_effect=lambda pairs: dict(pairs)):
                self.assertEqual(baseline_extract._read_json(path, 4096), {"safe": 2})
            with self.assertRaises(AssertionError):
                with patch.object(baseline_extract, "_unique_object", side_effect=lambda pairs: dict(pairs)):
                    self._reject(lambda value: baseline_extract._read_json(value, 4096), path,
                                 BaselineExtractError, "json: DUPLICATE_KEY")

    def test_size_oracle_detects_a_reader_that_omits_the_overflow_sentinel(self):
        with tempfile.TemporaryDirectory() as tmp:
            valid_raw = json.dumps({"x": list(range(20)), "last": 1}, separators=(",", ":")).encode() + b" "
            path = _write(Path(tmp), "large.json", valid_raw)
            maximum = len(valid_raw) - 1
            self._reject(lambda value: baseline_extract._read_json(value, maximum), path,
                         BaselineExtractError, "json: TOO_LARGE")

            class TruncatingStream(_SpyStream):
                def read(self, size=-1):
                    self.read_sizes.append(size)
                    return self.raw[:max(0, size - 1)]

            # Models a reader returning cap bytes rather than cap+1, losing the overflow sentinel.
            with patch("baseline_extract.open", return_value=TruncatingStream(valid_raw)):
                self.assertEqual(baseline_extract._read_json(path, maximum)["last"], 1)
            with self.assertRaises(AssertionError):
                with patch("baseline_extract.open", return_value=TruncatingStream(valid_raw)):
                    self._reject(lambda value: baseline_extract._read_json(value, maximum), path,
                                 BaselineExtractError, "json: TOO_LARGE")
