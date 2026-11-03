"""스냅샷 저장 단계별 실패와 자기 생성물 정리의 bounded 회귀."""

from __future__ import annotations

import errno
import hashlib
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from ground_truth import bac_category, core_gt
from snapshot_writer import SnapshotContext, SnapshotWriteError, write_snapshot

KEYS = tuple(sorted({item.key for item in core_gt() + bac_category()}))
LIMIT = 1024 * 1024
CONTEXT = SnapshotContext("persist-run", "persist-model", "persist-reset", "persist-instance", "persist-execution")
SAVE_ERROR = "스냅샷 저장 실패(기존 자료/경로/파일 오류)"
CLEANUP_WARNING = SAVE_ERROR + "; 미완성 생성물 정리 확인 필요"


def raw_body() -> bytes:
    return json.dumps({"data": [{"key": key, "solved": False} for key in KEYS]}, separators=(",", ":"), ensure_ascii=False).encode()


class TestSnapshotPersistenceFailureProperties(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.raw = raw_body()
        for target in ("socket.socket", "socket.create_connection", "socket.getaddrinfo", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("persistence fixture must remain offline"))
            guard.start()
            self.addCleanup(guard.stop)

    def write(self, phase="pre", root=None):
        return write_snapshot(root or self.root, phase, self.raw, CONTEXT, max_bytes=LIMIT)

    def filesystem(self, root=None):
        root = root or self.root
        entries = {}
        for path in sorted(root.rglob("*")):
            key = str(path.relative_to(root))
            entries[key] = ("dir", None) if path.is_dir() else ("file", path.read_bytes())
        return entries

    def assert_failure_preserves(self, invoke, before, *, sentinel=None):
        with self.assertRaises(SnapshotWriteError) as caught:
            invoke()
        self.assertIn(str(caught.exception), (SAVE_ERROR, CLEANUP_WARNING))
        self.assertEqual(self.filesystem(), before)
        if sentinel is not None:
            self.assertEqual(sentinel.read_bytes(), b"keep")
        return str(caught.exception)

    def assert_descriptors_closed(self, descriptors):
        for fd in descriptors:
            with self.assertRaises(OSError) as caught:
                os.fstat(fd)
            self.assertEqual(caught.exception.errno, errno.EBADF)

    def assert_success(self, phase="pre", root=None):
        root = root or self.root
        result = self.write(phase, root)
        raw = root / result.raw["path"]
        normalized = root / result.normalized["path"]
        self.assertEqual(raw.read_bytes(), self.raw)
        normalized_bytes = normalized.read_bytes()
        self.assertEqual(result.raw["sha256"], hashlib.new("sha256", self.raw).hexdigest())
        self.assertEqual(result.normalized["sha256"], hashlib.new("sha256", normalized_bytes).hexdigest())
        self.assertEqual((result.raw["size"], result.normalized["size"]), (len(self.raw), len(normalized_bytes)))
        self.assertEqual((result.challenge_count, result.solved_count), (len(KEYS), 0))
        envelope = json.loads(normalized_bytes)
        self.assertEqual(envelope, {**vars(CONTEXT), "phase": phase,
                                    "solved": {key: False for key in KEYS}})
        for descriptor in (result.raw, result.normalized):
            self.assertEqual(descriptor["run_id"], CONTEXT.run_id)
            self.assertEqual(descriptor["model_tag"], CONTEXT.model_tag)
            self.assertEqual(descriptor["reset_generation"], CONTEXT.reset_generation)
            self.assertEqual(descriptor["instance_id"], CONTEXT.instance_id)
            self.assertEqual(descriptor["execution_id"], CONTEXT.execution_id)
            self.assertEqual(descriptor["phase"], phase)
        return result

    def test_success_and_retry_after_fd_role_failure_matrix(self):
        points = ("root_open", "phase_open", "raw_open", "normalized_open", "raw_write", "normalized_write",
                  "raw_flush", "normalized_flush", "raw_fsync", "normalized_fsync", "phase_fsync", "root_fsync")
        for phase in ("pre", "post"):
            for point in points:
                for failure in (errno.EIO, errno.ENOSPC):
                    with self.subTest(phase=phase, point=point, errno=failure):
                        case = self.root / f"{phase}-{point}-{failure}"
                        case.mkdir()
                        opposite = "post" if phase == "pre" else "pre"
                        self.assert_success(opposite, case)
                        sentinel = case / "sentinel"
                        sentinel.write_bytes(b"keep")
                        before = self.filesystem()
                        original_open, original_fdopen, original_fsync = os.open, os.fdopen, os.fsync
                        roles, hit, captured = {}, [], []

                        def tracked_open(path, flags, *args, **kwargs):
                            fd = original_open(path, flags, *args, **kwargs)
                            captured.append(fd)
                            if path == phase:
                                roles[fd] = "phase"
                            elif path == "raw.json":
                                roles[fd] = "raw"
                            elif path == "normalized.json":
                                roles[fd] = "normalized"
                            elif os.fspath(path) == str(case):
                                roles[fd] = "root"
                            return fd

                        class Wrapped:
                            def __init__(self, stream, role):
                                self.stream, self.role = stream, role
                            def __enter__(self):
                                return self
                            def __exit__(self, *args):
                                return self.stream.close()
                            def write(self, data):
                                if point == self.role + "_write":
                                    hit.append(point)
                                    raise OSError(failure, "synthetic write")
                                return self.stream.write(data)
                            def flush(self):
                                if point == self.role + "_flush":
                                    hit.append(point)
                                    raise OSError(failure, "synthetic flush")
                                return self.stream.flush()
                            def fileno(self):
                                return self.stream.fileno()

                        def tracked_fdopen(fd, mode, *args, **kwargs):
                            return Wrapped(original_fdopen(fd, mode, *args, **kwargs), roles.get(fd, "unknown"))
                        def tracked_fsync(fd):
                            role = roles.get(fd, "unknown")
                            if point == role + "_fsync":
                                hit.append(point)
                                raise OSError(failure, "synthetic fsync")
                            return original_fsync(fd)
                        def fail_open(path, flags, *args, **kwargs):
                            targets = {"root_open": case, "phase_open": phase,
                                       "raw_open": "raw.json", "normalized_open": "normalized.json"}
                            if point in targets and os.fspath(path) == os.fspath(targets[point]):
                                hit.append(point)
                                raise OSError(failure, "synthetic open")
                            return tracked_open(path, flags, *args, **kwargs)

                        with patch("snapshot_writer.os.open", side_effect=fail_open), patch("snapshot_writer.os.fdopen", side_effect=tracked_fdopen), patch("snapshot_writer.os.fsync", side_effect=tracked_fsync):
                            message = self.assert_failure_preserves(lambda: self.write(phase, case), before,
                                                                    sentinel=sentinel)
                        self.assertEqual(message, SAVE_ERROR)
                        self.assertEqual(hit, [point])
                        self.assert_descriptors_closed(captured)
                        self.assert_success(phase, case)

    def test_existing_phase_and_root_sentinel_survive_post_failure(self):
        self.assert_success("pre")
        sentinel = self.root / "root-sentinel"
        sentinel.write_bytes(b"keep")
        before = self.filesystem()
        with patch("snapshot_writer.os.fsync", side_effect=OSError(errno.ENOSPC, "synthetic sync")):
            self.assert_failure_preserves(lambda: self.write("post"), before, sentinel=sentinel)

    def test_real_short_writes_are_rejected_and_retry_succeeds(self):
        original_fdopen = os.fdopen
        for phase in ("pre", "post"):
            for target in ("raw", "normalized"):
                with self.subTest(phase=phase, target=target):
                    case = self.root / f"short-{phase}-{target}"
                    case.mkdir()
                    original_open = os.open
                    roles, hit = {}, []
                    def tracked_open(path, flags, *args, **kwargs):
                        fd = original_open(path, flags, *args, **kwargs)
                        if path in ("raw.json", "normalized.json"):
                            roles[fd] = Path(path).stem
                        return fd
                    class ShortFile:
                        def __init__(self, stream, role):
                            self.stream, self.role = stream, role
                        def __enter__(self):
                            return self
                        def __exit__(self, *args):
                            return self.stream.close()
                        def write(self, data):
                            if self.role == target:
                                hit.append(target)
                                return self.stream.write(data[:-1])
                            return self.stream.write(data)
                        def flush(self):
                            return self.stream.flush()
                        def fileno(self):
                            return self.stream.fileno()
                    def fdopen(fd, mode, *args, **kwargs):
                        return ShortFile(original_fdopen(fd, mode, *args, **kwargs), roles.get(fd, "unknown"))
                    before = self.filesystem()
                    with patch("snapshot_writer.os.open", side_effect=tracked_open), \
                         patch("snapshot_writer.os.fdopen", side_effect=fdopen):
                        self.assert_failure_preserves(lambda: self.write(phase, case), before)
                    self.assertEqual(hit, [target])
                    self.assert_success(phase, case)

    def test_replacement_directory_preserves_foreign_file_and_warns(self):
        original_open = os.open
        def replace_phase(path, flags, *args, **kwargs):
            if path == "pre":
                (self.root / "pre").rename(self.root / "original-pre")
                (self.root / "pre").mkdir()
                (self.root / "pre" / "foreign").write_bytes(b"keep")
                raise OSError(errno.EIO, "synthetic replacement")
            return original_open(path, flags, *args, **kwargs)
        with patch("snapshot_writer.os.open", side_effect=replace_phase):
            with self.assertRaisesRegex(SnapshotWriteError, "정리 확인"):
                self.write()
        self.assertEqual((self.root / "pre/foreign").read_bytes(), b"keep")
        self.assertTrue((self.root / "original-pre").is_dir())

    def test_cleanup_mutation_leaves_partial_file_and_same_oracle_catches_it(self):
        original_open, original_unlink = os.open, os.unlink
        def fail_normalized(path, flags, *args, **kwargs):
            if path == "normalized.json":
                raise OSError(errno.EIO, "synthetic normalized open")
            return original_open(path, flags, *args, **kwargs)
        def weak_unlink(path, *args, **kwargs):
            if path == "raw.json":
                return None
            return original_unlink(path, *args, **kwargs)
        original_before = self.filesystem()
        with patch("snapshot_writer.os.open", side_effect=fail_normalized):
            self.assert_failure_preserves(self.write, original_before)
        self.assertFalse((self.root / "pre").exists())
        with patch("snapshot_writer.os.open", side_effect=fail_normalized), patch("snapshot_writer.os.unlink", side_effect=weak_unlink):
            with self.assertRaises(AssertionError):
                self.assert_failure_preserves(self.write, original_before)
        self.assertTrue((self.root / "pre/raw.json").exists())
        self.assertFalse((self.root / "pre/normalized.json").exists())

    def test_foreign_file_deletion_mutation_fails_same_filesystem_oracle(self):
        original_open, original_unlink = os.open, os.unlink

        def assert_foreign_preserved(root):
            inserted = []

            def collide(path, flags, *args, **kwargs):
                if path == "normalized.json":
                    (root / "pre" / "normalized.json").write_bytes(b"foreign")
                    inserted.append(path)
                return original_open(path, flags, *args, **kwargs)

            with patch("snapshot_writer.os.open", side_effect=collide):
                with self.assertRaises(SnapshotWriteError) as caught:
                    self.write(root=root)
            self.assertEqual(inserted, ["normalized.json"])
            self.assertIn(str(caught.exception), (SAVE_ERROR, CLEANUP_WARNING))
            self.assertEqual(self.filesystem(root),
                             {"pre": ("dir", None), "pre/normalized.json": ("file", b"foreign")})
            return str(caught.exception)

        baseline = self.root / "baseline"
        baseline.mkdir()
        self.assertEqual(assert_foreign_preserved(baseline), CLEANUP_WARNING)

        def dangerous_unlink(path, *args, **kwargs):
            if path == "raw.json":
                original_unlink("normalized.json", *args, **kwargs)
            return original_unlink(path, *args, **kwargs)

        mutated = self.root / "mutated"
        mutated.mkdir()
        with patch("snapshot_writer.os.unlink", side_effect=dangerous_unlink):
            with self.assertRaises(AssertionError):
                assert_foreign_preserved(mutated)
        self.assertEqual(self.filesystem(mutated), {})
        self.assertEqual(self.filesystem(baseline),
                         {"pre": ("dir", None), "pre/normalized.json": ("file", b"foreign")})


if __name__ == "__main__":
    unittest.main()
