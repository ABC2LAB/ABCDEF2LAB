"""E③ 정본 바이트: Go e3record와 같은 공용 벡터 파일로 평가 쪽 구현을 묶는다."""

from __future__ import annotations

import hashlib
import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import collect  # noqa: E402
from e3_canonical import (  # noqa: E402
    MAX_CANONICAL_BYTES, REJECTED, CanonicalError, canonical_bytes, canonical_sha256, canonical_time, loads_exact,
)

VECTORS = Path(__file__).resolve().parents[3] / "e3record/testdata/canonical-v1.json"


def load_vectors() -> list[dict]:
    suite = json.loads(VECTORS.read_text(encoding="utf-8"))
    assert suite["format_version"] == 1 and suite["vectors"], "공용 벡터 v1이 비어 있음"
    return suite["vectors"]


def encode(vector: dict) -> bytes:
    """벡터 입력 종류마다 Go 시험과 같은 방식으로 값을 만들어 정본으로 바꾼다."""
    kind, value = vector["input"]["kind"], vector["input"]["value"]
    if kind == "json":
        return canonical_bytes(value)
    if kind == "uint64":
        return canonical_bytes(int(value))
    if kind == "float64":
        return canonical_bytes(float(value))
    if kind == "utf8_hex":
        # 원시 문자열 바이트는 수신 JSON 안의 문자열로 들어온다. 대체 문자로 바꾸기 전에 거부해야 한다.
        return canonical_bytes(loads_exact(b'"' + bytes.fromhex(value) + b'"'))
    if kind == "repeat_string":
        return canonical_bytes(value * vector["input"]["count"])
    if kind == "time":
        return canonical_bytes(canonical_time(value))
    raise AssertionError(f"모르는 입력 종류: {kind}")


def expected_text(expected: dict) -> str | None:
    if "utf8" in expected:
        return expected["utf8"]
    if "utf8_repeat" in expected:
        part = expected["utf8_repeat"]
        return part["prefix"] + part["text"] * part["count"] + part["suffix"]
    return None


class TestSharedVectors(unittest.TestCase):
    def test_every_vector_matches_go(self):
        vectors = load_vectors()
        kinds = set()
        for vector in vectors:
            kinds.add(vector["input"]["kind"])
            with self.subTest(vector=vector["name"]):
                if "error" in vector:
                    self.assertEqual(vector["error"], REJECTED)
                    with self.assertRaises(CanonicalError) as caught:
                        encode(vector)
                    self.assertEqual(str(caught.exception), REJECTED)
                    continue
                data = encode(vector)
                expected = vector["expected"]
                text = expected_text(expected)
                if text is not None:
                    self.assertEqual(data, text.encode("utf-8"))
                self.assertEqual(len(data), expected["byte_length"])
                self.assertEqual(hashlib.sha256(data).hexdigest(), expected["sha256"])
        # 새 입력 종류가 생기면 여기서 먼저 걸린다(조용히 건너뛰지 않음).
        self.assertEqual(kinds, {"json", "uint64", "float64", "utf8_hex", "repeat_string", "time"})

    def test_limit_matches_the_vector_boundary(self):
        self.assertEqual(MAX_CANONICAL_BYTES, 65536)
        self.assertEqual(len(canonical_bytes("x" * (MAX_CANONICAL_BYTES - 2))), MAX_CANONICAL_BYTES)
        with self.assertRaises(CanonicalError):
            canonical_bytes("x" * (MAX_CANONICAL_BYTES - 1))

    def test_verdict_record_vector_is_a_valid_eval_record(self):
        # Go가 만든 정본 판정 레코드를 평가 수집기가 계약대로 받아들여야 두 쪽이 같은 레코드를 본다.
        record = next(v for v in load_vectors() if v["name"] == "confirmed-verdict-record")["input"]["value"]
        _, errors = collect._validate_record(0, record)
        self.assertEqual(errors, [])
        self.assertEqual(canonical_sha256(record), canonical_sha256(loads_exact(canonical_bytes(record))))


class TestStrictInput(unittest.TestCase):
    def reject(self, function, value):
        with self.assertRaises(CanonicalError) as caught:
            function(value)
        self.assertEqual(str(caught.exception), REJECTED)

    def test_values_outside_the_canonical_model_are_rejected(self):
        for value in (1.0, float("nan"), {"a": (1, 2)}, {1: "x"}, b"x", "\ud800", {"\udfff": 1},
                      [1 << 63], [-(1 << 63) - 1]):
            with self.subTest(value=repr(value)):
                self.reject(canonical_bytes, value)
        self.assertEqual(canonical_bytes([True, False, None, (1 << 63) - 1, -(1 << 63)]),
                         b"[true,false,null,9223372036854775807,-9223372036854775808]")

    def test_loads_exact_keeps_integers_and_rejects_floats_constants_duplicates_and_bad_utf8(self):
        self.assertEqual(loads_exact('{"n": 9223372036854775807}'), {"n": 9223372036854775807})
        for text in ('{"n": 1.0}', '{"n": 1e3}', '[NaN]', '[Infinity]', '[-Infinity]', '{"a": 1, "a": 2}',
                     b'"\xff"', b'"\xc3"', '{"a": ', '﻿{}'):
            with self.subTest(text=repr(text)):
                self.reject(loads_exact, text)

    def test_time_truncates_not_rounds_and_needs_an_exact_rfc3339_instant(self):
        self.assertEqual(canonical_time("2026-11-03T21:00:20.9999999+09:00"), "2026-11-03T12:00:20.999999Z")
        self.assertEqual(canonical_time("2026-11-03T12:00:20Z"), "2026-11-03T12:00:20.000000Z")
        for text in ("2026-11-03t12:00:20Z", "2026-11-03T12:00:20z", "2026-11-03T12:00:60Z",
                     "2026-11-03T12:00:20", "2026-11-03 12:00:20Z", "2026-11-03T12:00:20.1234567890Z",
                     "2026-02-30T00:00:00Z", "0001-01-01T00:00:00+09:00", 20261103):
            with self.subTest(text=text):
                self.reject(canonical_time, text)

    def test_time_accepts_only_the_v1_rfc3339_range(self):
        # 숫자는 ASCII만, offset은 시 00–23·분 00–59만. 수락 값은 Go time.Parse 결과와 같다.
        for text, expected in (("2026-11-03T12:00:20+00:59", "2026-11-03T11:01:20.000000Z"),
                               ("2026-11-03T12:00:20+23:59", "2026-11-02T12:01:20.000000Z"),
                               ("2026-11-03T12:00:20-23:59", "2026-11-04T11:59:20.000000Z")):
            with self.subTest(text=text):
                self.assertEqual(canonical_time(text), expected)
        # +00:99·+99:00·비ASCII 숫자는 Go도 거부한다. +00:60·+24:00은 Go가 받지만 RFC3339 밖이라 v1에서 거부.
        for text in ("2026-11-03T12:00:20+00:99", "2026-11-03T12:00:20+99:00", "2026-11-03T12:00:20+00:60",
                     "2026-11-03T12:00:20+24:00", "2026-11-03T12:00:20-24:00", "\uff12\uff10\uff12\uff16-11-03T12:00:20Z",
                     "2026-11-03T12:00:2\u0660Z", "2026-11-03T12:00:20.\uff11Z", "2026-11-03T12:00:20+0\u0669:00"):
            with self.subTest(text=text):
                self.reject(canonical_time, text)


if __name__ == "__main__":
    unittest.main()
