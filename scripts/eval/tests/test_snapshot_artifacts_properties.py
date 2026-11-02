"""스냅샷 저장 산출물의 raw/정규화/descriptor 연결 조합 회귀.

합성 챌린지 응답 bytes와 임시 디렉터리만 사용한다. 러너 기록·서명·해결 귀속은
검증하지 않고, 저장기의 현재 파일·envelope·hash 계약만 독립 기대값으로 확인한다.
"""

from __future__ import annotations

import hashlib
import itertools
import json
import os
import sys
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import snapshot_writer
from ground_truth import bac_category, core_gt
from snapshot_writer import SnapshotContext, SnapshotWriteError

_KEYS = tuple(sorted({challenge.key for challenge in core_gt() + bac_category()}))
_LIMIT = 1024 * 1024
_EXTRA_KEY = "합성-extra"
_CONTEXT = SnapshotContext("synthetic-run", "synthetic-model", "synthetic-reset",
                          "synthetic-instance", "synthetic-execution")


def _rows(mask: int, *, reverse: bool = False, reverse_keys: bool = False,
          include_extra: bool = False) -> list[dict[str, object]]:
    rows = [{"key": key, "solved": bool(mask & (1 << index)), "name": "합성 이름"}
            for index, key in enumerate(_KEYS)]
    if include_extra:
        rows.append({"key": _EXTRA_KEY, "solved": bool(mask & 1), "name": "합성 추가"})
    if reverse_keys:
        rows = [dict(reversed(tuple(row.items()))) for row in rows]
    return list(reversed(rows)) if reverse else rows


def _raw(mask: int, style: int) -> bytes:
    rows = _rows(mask, reverse=bool(style & 1), reverse_keys=bool(style & 16),
                 include_extra=bool(style & 32))
    payload: object = rows if style & 2 else {"status": "ok", "data": rows, "synthetic_meta": "합성 값"}
    return json.dumps(payload, ensure_ascii=bool(style & 4),
                      indent=2 if style & 8 else None,
                      separators=None if style & 8 else (",", ":")).encode("utf-8")


class TestSnapshotArtifactsProperties(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("snapshot writer must remain offline"))
            guard.start()
            self.addCleanup(guard.stop)

    def write(self, raw: bytes, *, phase: str = "pre", root: Path | None = None,
              context: SnapshotContext = _CONTEXT, limit: int = _LIMIT):
        return snapshot_writer.write_snapshot(root or self.root, phase, raw, context, max_bytes=limit)

    def files(self, root: Path | None = None) -> dict[str, bytes]:
        base = root or self.root
        return {str(path.relative_to(base)): path.read_bytes() for path in base.rglob("*") if path.is_file()}

    def assert_descriptor(self, descriptor: dict, data: bytes, *, phase: str, filename: str):
        self.assertEqual(descriptor["phase"], phase)
        self.assertEqual(descriptor["path"], f"{phase}/{filename}")
        self.assertEqual(descriptor["size"], len(data))
        # hashlib.new avoids sharing snapshot_writer.hashlib.sha256 when a mutant patches it.
        self.assertEqual(descriptor["sha256"], hashlib.new("sha256", data).hexdigest())
        for field in _CONTEXT.__dataclass_fields__:
            self.assertEqual(descriptor[field], getattr(_CONTEXT, field))

    def assert_solved_bools(self, solved: dict, expected: dict[str, bool]):
        self.assertEqual(solved, expected)
        for key, value in expected.items():
            self.assertIs(solved[key], value)

    def test_raw_formats_and_boolean_states_preserve_bytes_and_normalize_deterministically(self):
        normalized_by_input = {}
        masks = (0, 1, 2, 3, 5, 10, (1 << len(_KEYS)) - 2, (1 << len(_KEYS)) - 1)
        expected_descriptor_fields = set(_CONTEXT.__dataclass_fields__) | {
            "phase", "path", "size", "sha256"
        }
        for mask, style in itertools.product(masks, range(64)):
            raw = _raw(mask, style)
            with self.subTest(mask=mask, style=style):
                case_root = self.root / f"case-{mask}-{style}"
                case_root.mkdir()
                before = raw
                result = self.write(raw, root=case_root)
                self.assertEqual(raw, before)
                raw_bytes = (case_root / result.raw["path"]).read_bytes()
                normalized_bytes = (case_root / result.normalized["path"]).read_bytes()
                self.assertEqual(raw_bytes, raw)
                expected = {key: bool(mask & (1 << index)) for index, key in enumerate(_KEYS)}
                if style & 32:
                    expected[_EXTRA_KEY] = bool(mask & 1)
                envelope = json.loads(normalized_bytes)
                self.assertEqual(set(envelope), set(_CONTEXT.__dataclass_fields__) | {"phase", "solved"})
                self.assertEqual({field: envelope[field] for field in _CONTEXT.__dataclass_fields__},
                                 {field: getattr(_CONTEXT, field) for field in _CONTEXT.__dataclass_fields__})
                self.assertEqual(envelope["phase"], "pre")
                self.assert_solved_bools(envelope["solved"], expected)
                self.assertEqual((result.challenge_count, result.solved_count),
                                 (len(expected), sum(expected.values())))
                self.assert_descriptor(result.raw, raw, phase="pre", filename="raw.json")
                self.assert_descriptor(result.normalized, normalized_bytes, phase="pre", filename="normalized.json")
                self.assertEqual(set(result.raw), expected_descriptor_fields)
                self.assertEqual(set(result.normalized), expected_descriptor_fields)
                input_key = (mask, bool(style & 32))
                reference = normalized_by_input.setdefault(input_key, normalized_bytes)
                self.assertEqual(normalized_bytes, reference)

    def test_pre_post_are_separate_and_existing_phase_is_preserved(self):
        pre_raw = _raw(0, 0)
        post_raw = _raw((1 << len(_KEYS)) - 1, 9)
        pre = self.write(pre_raw, phase="pre")
        post = self.write(post_raw, phase="post")
        before = self.files()
        self.assertNotEqual(pre.raw["path"], post.raw["path"])
        self.assertNotEqual(pre.normalized["path"], post.normalized["path"])
        self.assertEqual(pre.solved_count, 0)
        self.assertEqual(post.solved_count, len(_KEYS))
        with self.assertRaises(SnapshotWriteError):
            self.write(_raw(3, 2), phase="pre")
        self.assertEqual(self.files(), before)
        self.assertEqual((self.root / pre.raw["path"]).read_bytes(), pre_raw)
        self.assertEqual((self.root / post.raw["path"]).read_bytes(), post_raw)

    def test_raw_limit_exact_below_and_above_is_fail_closed_without_new_files(self):
        for style in (0, 3, 12):
            raw = _raw(5, style)
            with self.subTest(style=style):
                exact_root = self.root / f"exact-{style}"
                exact_root.mkdir()
                result = self.write(raw, root=exact_root, limit=len(raw))
                self.assertEqual((exact_root / result.raw["path"]).read_bytes(), raw)

                below_root = self.root / f"below-{style}"
                below_root.mkdir()
                with self.assertRaises(SnapshotWriteError):
                    self.write(raw, root=below_root, limit=len(raw) - 1)
                self.assertEqual(self.files(below_root), {})
                self.assertEqual(list(below_root.iterdir()), [])

                above_root = self.root / f"above-{style}"
                above_root.mkdir()
                self.assertEqual(self.write(raw, root=above_root, limit=len(raw) + 1).challenge_count, len(_KEYS))

    def test_invalid_input_and_context_leave_output_directory_untouched(self):
        invalid = (
            b"", b"{", b"{\"data\": [], \"data\": []}",
            json.dumps({"data": _rows(0)[:-1]}).encode(),
            json.dumps({"data": [{"key": key, "solved": "false"} for key in _KEYS]}).encode(),
            json.dumps({"data": _rows(0), "token": "synthetic-secret"}).encode(),
        )
        for index, raw in enumerate(invalid):
            root = self.root / f"invalid-{index}"
            root.mkdir()
            with self.subTest(index=index):
                with self.assertRaises(SnapshotWriteError):
                    self.write(raw, root=root)
                self.assertEqual(self.files(root), {})
                self.assertEqual(list(root.iterdir()), [])
        for field in _CONTEXT.__dataclass_fields__:
            with self.subTest(context_field=field):
                root = self.root / f"context-{field}"
                root.mkdir()
                with self.assertRaises(SnapshotWriteError):
                    self.write(_raw(0, 0), root=root, context=replace(_CONTEXT, **{field: ""}))
                self.assertEqual(self.files(root), {})
                self.assertEqual(list(root.iterdir()), [])

    def test_input_and_existing_files_are_unchanged_after_success_and_failure(self):
        raw = _raw(9, 13)
        root = self.root / "preserve"
        root.mkdir()
        foreign = root / "foreign.txt"
        foreign.write_bytes(b"keep")
        before = raw, self.files(root)
        self.write(raw, root=root)
        self.assertEqual(raw, before[0])
        self.assertEqual((root / "foreign.txt").read_bytes(), b"keep")
        after_success = self.files(root)
        with self.assertRaises(SnapshotWriteError):
            self.write(raw, root=root, phase="pre")
        self.assertEqual(self.files(root), after_success)

    def test_regression_assertions_detect_corrupted_descriptor_and_written_raw_bytes(self):
        raw = _raw(7, 5)
        expected = self.write(raw)
        raw_data = (self.root / expected.raw["path"]).read_bytes()
        self.assert_descriptor(expected.raw, raw_data, phase="pre", filename="raw.json")

        original_hash = snapshot_writer.hashlib.sha256
        with patch.object(snapshot_writer.hashlib, "sha256", return_value=original_hash(b"constant")):
            with self.assertRaises(AssertionError):
                mutant_root = self.root / "hash-mutant"
                mutant_root.mkdir()
                actual = self.write(raw, root=mutant_root)
                data = (mutant_root / actual.raw["path"]).read_bytes()
                self.assert_descriptor(actual.raw, data, phase="pre", filename="raw.json")

        original_writer = snapshot_writer._write_file
        def corrupt_raw(directory, name, data, created_files):
            if name == "raw.json":
                data = data + b"\ncorrupted"
            return original_writer(directory, name, data, created_files)

        with patch.object(snapshot_writer, "_write_file", side_effect=corrupt_raw):
            with self.assertRaises(AssertionError):
                mutant_root = self.root / "raw-mutant"
                mutant_root.mkdir()
                actual = self.write(raw, root=mutant_root)
                data = (mutant_root / actual.raw["path"]).read_bytes()
                self.assertEqual(data, raw)

    def test_regression_assertion_detects_limit_bypass(self):
        raw = _raw(1, 0)
        normal_root = self.root / "normal-limit"
        normal_root.mkdir()
        with self.assertRaises(SnapshotWriteError):
            self.write(raw, root=normal_root, limit=len(raw) - 1)
        self.assertEqual(self.files(normal_root), {})
        self.assertEqual(list(normal_root.iterdir()), [])
        original = snapshot_writer.write_snapshot

        def bypass(root, phase, data, context, *, max_bytes):
            return original(root, phase, data, context, max_bytes=len(data))

        with patch.object(snapshot_writer, "write_snapshot", side_effect=bypass):
            root = self.root / "limit-mutant"
            root.mkdir()
            with self.assertRaises(AssertionError):
                with self.assertRaises(SnapshotWriteError):
                    self.write(raw, root=root, limit=len(raw) - 1)


if __name__ == "__main__":
    unittest.main()
