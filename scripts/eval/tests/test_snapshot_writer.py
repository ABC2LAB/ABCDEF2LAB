"""스냅샷 producer: 합성 bytes와 임시 디렉터리만 사용한다."""

from __future__ import annotations

import errno
import hashlib
import json
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from dataclasses import replace
from ground_truth import bac_category, core_gt
from run_record import load_run_inputs
from snapshot_writer import SnapshotContext, SnapshotWriteError, write_snapshot
from test_score import _RunDir

KEYS = sorted({challenge.key for challenge in core_gt() + bac_category()})
LIMIT = 1024 * 1024


class TestSnapshotWriter(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.context = SnapshotContext("run-test", "model-test", "reset-test", "instance-test", "execution-test")
        self.rows = [{"key": key, "solved": False} for key in KEYS]
        self.raw = json.dumps({"data": self.rows}, indent=2).encode()

    def write(self, phase="pre", raw=None, **kwargs):
        return write_snapshot(self.root, phase, self.raw if raw is None else raw,
                              kwargs.pop("context", self.context), max_bytes=kwargs.pop("max_bytes", LIMIT))

    def assert_input_rejected(self, raw, fragment=None):
        with self.assertRaises(SnapshotWriteError) as caught:
            self.write(raw=raw)
        if fragment:
            self.assertIn(fragment, str(caught.exception))
        self.assertEqual(list(self.root.iterdir()), [])

    def test_preserves_exact_raw_bytes_and_publishes_matching_hashes(self):
        result = self.write()
        self.assertEqual((self.root / result.raw["path"]).read_bytes(), self.raw)
        for descriptor in (result.raw, result.normalized):
            data = (self.root / descriptor["path"]).read_bytes()
            self.assertEqual(descriptor["sha256"], hashlib.sha256(data).hexdigest())
            self.assertEqual(descriptor["size"], len(data))
            self.assertEqual(descriptor["run_id"], self.context.run_id)
        envelope = json.loads((self.root / result.normalized["path"]).read_bytes())
        self.assertEqual(envelope["solved"], {key: False for key in KEYS})
        self.assertEqual((result.challenge_count, result.solved_count), (len(KEYS), 0))

    def test_files_private_and_pre_post_stored_separately(self):
        pre, post = self.write(), self.write(phase="post")
        self.assertNotEqual(pre.raw["path"], post.raw["path"])
        for phase in ("pre", "post"):
            self.assertEqual(stat.S_IMODE((self.root / phase).stat().st_mode), 0o700)
            for path in (self.root / phase).iterdir():
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_existing_snapshot_is_never_overwritten(self):
        first = self.write()
        before = {str(path.relative_to(self.root)): path.read_bytes() for path in (self.root / "pre").iterdir()}
        with self.assertRaises(SnapshotWriteError):
            self.write()
        after = {str(path.relative_to(self.root)): path.read_bytes() for path in (self.root / "pre").iterdir()}
        self.assertEqual(before, after)
        self.assertEqual(first.solved_count, 0)

    def test_invalid_phase_creates_nothing(self):
        for phase in ("engine", "../post", "", None):
            with self.subTest(phase=phase), self.assertRaises(SnapshotWriteError):
                self.write(phase=phase)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_missing_identity_creates_nothing(self):
        for field in self.context.__dataclass_fields__:
            with self.subTest(field=field), self.assertRaises(SnapshotWriteError):
                self.write(context=replace(self.context, **{field: ""}))
        self.assertEqual(list(self.root.iterdir()), [])

    def test_requires_bytes_and_explicit_positive_limit(self):
        for raw in (b"", "not-bytes", None):
            if raw is None:
                with self.assertRaises(SnapshotWriteError):
                    write_snapshot(self.root, "pre", raw, self.context, max_bytes=LIMIT)
            else:
                self.assert_input_rejected(raw)
        for limit in (0, -1, True, 1.5):
            with self.assertRaises(SnapshotWriteError):
                self.write(max_bytes=limit)
        with self.assertRaises(SnapshotWriteError):
            self.write(max_bytes=len(self.raw) - 1)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_broken_or_ambiguous_json_rejected(self):
        for raw in (b"{", b"\xff", b'{"data": [], "data": []}', b'{"data": [], "number": NaN}',
                    b'{"data": [], "number": 1e999}'):
            with self.subTest(raw=raw):
                self.assert_input_rejected(raw)

    def test_missing_gt_nonbool_and_invalid_rows_rejected(self):
        values = (self.rows[:-1], [dict(row, solved="false") for row in self.rows],
                  [dict(row, solved=0) for row in self.rows], ["not-a-row"], [])
        for rows in values:
            with self.subTest(rows_count=len(rows)):
                self.assert_input_rejected(json.dumps(rows).encode(), "검증 실패")

    def test_sensitive_field_values_are_rejected_before_any_write(self):
        for field in ("Authorization", "cookie", "token", "password", "refresh_token"):
            payload = {"data": self.rows, "nested": [{field: "SYNTHETIC-SECRET"}]}
            self.assert_input_rejected(json.dumps(payload).encode(), "민감 필드")

    def test_redacted_field_names_may_be_preserved(self):
        raw = json.dumps({"data": self.rows, "token": "[REDACTED]", "cookie": None}).encode()
        result = self.write(raw=raw)
        self.assertEqual((self.root / result.raw["path"]).read_bytes(), raw)

    def test_root_symlink_and_existing_phase_symlink_are_rejected(self):
        outside = self.root / "outside"
        outside.mkdir()
        link = self.root / "linked"
        link.symlink_to(outside, target_is_directory=True)
        with self.assertRaises(SnapshotWriteError):
            write_snapshot(link, "pre", self.raw, self.context, max_bytes=LIMIT)
        (self.root / "pre").symlink_to(outside, target_is_directory=True)
        with self.assertRaises(SnapshotWriteError):
            self.write()
        self.assertEqual(list(outside.iterdir()), [])
        self.assertTrue((self.root / "pre").is_symlink())

    def test_disk_sync_failure_rolls_back_only_new_phase(self):
        existing = self.root / "unrelated"
        existing.write_bytes(b"keep")
        with patch("snapshot_writer.os.fsync", side_effect=OSError(errno.ENOSPC, "synthetic full disk")):
            with self.assertRaises(SnapshotWriteError):
                self.write()
        self.assertFalse((self.root / "pre").exists())
        self.assertEqual(existing.read_bytes(), b"keep")

    def test_second_file_open_failure_removes_own_first_file(self):
        actual = os.open

        def fail_second(path, flags, *args, **kwargs):
            if path == "normalized.json":
                raise OSError(errno.EIO, "synthetic I/O failure")
            return actual(path, flags, *args, **kwargs)

        with patch("snapshot_writer.os.open", side_effect=fail_second):
            with self.assertRaises(SnapshotWriteError):
                self.write()
        self.assertEqual(list(self.root.iterdir()), [])

    def test_cleanup_preserves_foreign_file_created_during_write(self):
        actual = os.open

        def collide(path, flags, *args, **kwargs):
            if path == "normalized.json":
                (self.root / "pre" / "normalized.json").write_bytes(b"foreign")
            return actual(path, flags, *args, **kwargs)

        with patch("snapshot_writer.os.open", side_effect=collide):
            with self.assertRaisesRegex(SnapshotWriteError, "정리 확인"):
                self.write()
        self.assertEqual((self.root / "pre" / "normalized.json").read_bytes(), b"foreign")
        self.assertFalse((self.root / "pre" / "raw.json").exists())

    def test_lone_surrogate_in_raw_or_identity_is_rejected_before_write(self):
        raw = json.dumps({"data": self.rows + [{"key": chr(0xD800), "solved": False}]}).encode()
        self.assert_input_rejected(raw, "UTF-8")
        with self.assertRaisesRegex(SnapshotWriteError, "UTF-8"):
            self.write(context=replace(self.context, run_id=chr(0xD800)))
        self.assertEqual(list(self.root.iterdir()), [])

    def test_phase_open_failure_removes_empty_directory_and_allows_retry(self):
        actual = os.open

        def fail_phase(path, flags, *args, **kwargs):
            if path == "pre":
                raise OSError(errno.EIO, "synthetic phase-open failure")
            return actual(path, flags, *args, **kwargs)

        with patch("snapshot_writer.os.open", side_effect=fail_phase):
            with self.assertRaises(SnapshotWriteError):
                self.write()
        self.assertEqual(list(self.root.iterdir()), [])
        self.assertEqual(self.write().challenge_count, len(KEYS))

    def test_phase_open_failure_keeps_replacement_directory(self):
        actual = os.open

        def replace_phase(path, flags, *args, **kwargs):
            if path == "pre":
                (self.root / "pre").rename(self.root / "original-pre")
                (self.root / "pre").mkdir()
                (self.root / "pre" / "foreign").write_bytes(b"keep")
                raise OSError(errno.EIO, "synthetic replacement")
            return actual(path, flags, *args, **kwargs)

        with patch("snapshot_writer.os.open", side_effect=replace_phase):
            with self.assertRaisesRegex(SnapshotWriteError, "정리 확인"):
                self.write()
        self.assertEqual((self.root / "pre" / "foreign").read_bytes(), b"keep")
        self.assertTrue((self.root / "original-pre").is_dir())

    def test_normalized_output_is_deterministic(self):
        first = self.write()
        second = self.write(phase="post")
        before = json.loads((self.root / first.normalized["path"]).read_bytes())
        after = json.loads((self.root / second.normalized["path"]).read_bytes())
        after["phase"] = "pre"
        self.assertEqual(before, after)


class TestSnapshotWriterReader(_RunDir):
    def test_written_pre_post_are_accepted_by_signed_record_validator(self):
        run = self.make_run("r0", [], solved={"basketAccessChallenge": True})
        receipt = self.fixture.receipt(run)
        context = SnapshotContext(**{field: receipt[field] for field in SnapshotContext.__dataclass_fields__})
        root = Path(self._path(run["run_record_path"])).parent
        for phase, manifest_field in (("pre", "pre_snapshot_path"), ("post", "snapshot_path")):
            raw = (root / receipt["artifacts"][phase + "_raw"]["path"]).read_bytes()
            result = write_snapshot(root, phase, raw, context, max_bytes=LIMIT)
            receipt["artifacts"][phase + "_raw"] = result.raw
            receipt["artifacts"][phase] = result.normalized
            run[manifest_field] = str(root / result.normalized["path"])
        self.fixture._write_receipt(run, receipt)
        inputs = load_run_inputs(self.load([run])[0], self.runner_key, self.correlate)
        self.assertFalse(inputs.pre["basketAccessChallenge"])
        self.assertTrue(inputs.post["basketAccessChallenge"])
        self.assertIn("basketAccessChallenge", inputs.engine_solved)
        code, out, _ = self.run_main([run])
        self.assertEqual(code, 0)
        self.assertIn("코어 GT", out)


if __name__ == "__main__":
    unittest.main()
