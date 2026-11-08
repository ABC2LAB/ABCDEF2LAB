"""스냅샷 수집 시각과 실제 산출물 연결의 bounded 회귀."""

from __future__ import annotations

import hashlib
import io
import json
import os
import sys
import tempfile
import unittest
from dataclasses import asdict
from datetime import datetime, timezone
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import snapshot_collector
from ground_truth import bac_category, core_gt
from snapshot_collector import SnapshotCollectionError, SnapshotEndpoint, collect_snapshot
from snapshot_writer import SnapshotContext


KEYS = tuple(sorted({item.key for item in core_gt() + bac_category()}))
CONTEXT = SnapshotContext("time-run", "time-model", "time-reset", "time-instance", "time-execution")


def body(values: tuple[bool, ...], *, reverse: bool = False) -> bytes:
    keys = tuple(reversed(KEYS)) if reverse else KEYS
    return json.dumps({"data": [{"key": key, "solved": values[index % len(values)]}
                                 for index, key in enumerate(keys)]},
                      separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def solved_map(values: tuple[bool, ...], *, reverse: bool = False) -> dict[str, bool]:
    keys = tuple(reversed(KEYS)) if reverse else KEYS
    return {key: values[index % len(values)] for index, key in enumerate(keys)}


def filesystem(root: Path):
    return {str(path.relative_to(root)): ("dir", None) if path.is_dir() else ("file", path.read_bytes())
            for path in root.rglob("*")}


class MemoryResponse:
    status = 200

    def __init__(self, raw: bytes, *, framing: str = "length"):
        self.fp = MemoryBody(raw)
        self.headers = {"Content-Type": "application/json"}
        if framing == "length":
            self.headers["Content-Length"] = str(len(raw))

    def getheader(self, name, default=None):
        return self.headers.get(name, default)

    def close(self):
        self.fp.close()


class MemoryBody(io.BytesIO):
    def read(self, maximum=-1):
        MemoryConnection.events.append("read")
        return super().read(maximum)


class MemoryConnection:
    requests: list[tuple[str, str, dict]] = []
    events: list[str] = []
    response: MemoryResponse | None = None

    def __init__(self, host, port, timeout):
        self.host, self.port, self.timeout = host, port, timeout

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False

    def request(self, method, path, headers):
        self.events.append("request")
        self.requests.append((method, path, headers))

    def getresponse(self):
        return self.response

    def close(self):
        return None


class FrozenClock:
    values: list[datetime] = []
    calls: list[object] = []

    @classmethod
    def now(cls, tz=None):
        cls.calls.append(tz)
        MemoryConnection.events.append("clock")
        return cls.values.pop(0)


class TestSnapshotCollectionTimeProperties(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.raw = body((False, True))
        self.config = SnapshotEndpoint("http://127.0.0.1:38123", "/instrumentation/state", 1.0, 1024 * 1024)
        MemoryConnection.requests = []
        MemoryConnection.events = []
        for target in ("socket.socket", "socket.create_connection", "socket.getaddrinfo",
                       "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("time fixture is offline"))
            guard.start()
            self.addCleanup(guard.stop)

    def run_capture(self, requested: datetime, received: datetime, *, phase="pre", raw=None,
                    framing="length", root=None, writer_override=None):
        run_root = self.root if root is None else Path(root)
        MemoryConnection.response = MemoryResponse(self.raw if raw is None else raw, framing=framing)
        MemoryConnection.requests = []
        MemoryConnection.events = []
        FrozenClock.values = [requested, received]
        FrozenClock.calls = []
        original_writer = (snapshot_collector.write_snapshot
                           if writer_override is None else writer_override)

        def traced_writer(*args, **kwargs):
            MemoryConnection.events.append("write")
            return original_writer(*args, **kwargs)

        with patch.object(snapshot_collector.http.client, "HTTPConnection", MemoryConnection), \
             patch.object(snapshot_collector, "datetime", FrozenClock), \
             patch.object(snapshot_collector, "write_snapshot", traced_writer):
            return collect_snapshot(self.config, run_root, phase, CONTEXT)

    def assert_artifacts(self, result, root, phase, requested, received, raw, expected_solved):
        root = Path(root)
        self.assertEqual(result.requested_at, requested.isoformat())
        self.assertEqual(result.received_at, received.isoformat())
        self.assertEqual(FrozenClock.calls, [timezone.utc, timezone.utc])
        self.assertEqual(MemoryConnection.events, ["clock", "request", "read", "clock", "write"])
        self.assertEqual(len(MemoryConnection.requests), 1)
        self.assertEqual(MemoryConnection.requests[0][0:2], ("GET", self.config.path))
        raw_path = root / result.artifacts.raw["path"]
        normalized_path = root / result.artifacts.normalized["path"]
        self.assertEqual(raw_path.read_bytes(), raw)
        normalized = json.loads(normalized_path.read_bytes())
        self.assertEqual(normalized, {**asdict(CONTEXT), "phase": phase,
                                      "solved": expected_solved})
        for key, value in expected_solved.items():
            self.assertIs(normalized["solved"][key], value)
        for descriptor, filename, data in (
                (result.artifacts.raw, "raw.json", raw),
                (result.artifacts.normalized, "normalized.json", normalized_path.read_bytes())):
            self.assertEqual({key: descriptor[key] for key in asdict(CONTEXT)}, asdict(CONTEXT))
            self.assertEqual(descriptor["phase"], phase)
            self.assertEqual(descriptor["path"], f"{phase}/{filename}")
            self.assertEqual(descriptor["size"], len(data))
            self.assertEqual((root / descriptor["path"]).resolve(), (root / f"{phase}/{filename}").resolve())
            self.assertEqual((root / descriptor["path"]).read_bytes(), data)
            self.assertEqual(descriptor["sha256"], hashlib.new("sha256", data).hexdigest())
        self.assertEqual(result.artifacts.raw["sha256"], hashlib.new("sha256", raw).hexdigest())
        self.assertEqual(result.artifacts.raw["size"], len(raw))
        self.assertEqual(result.artifacts.normalized["sha256"],
                         hashlib.new("sha256", normalized_path.read_bytes()).hexdigest())
        self.assertEqual(result.artifacts.challenge_count, len(expected_solved))
        self.assertEqual(result.artifacts.solved_count, sum(expected_solved.values()))

    def test_time_phase_framing_pattern_and_row_matrix_preserve_exact_artifacts(self):
        pairs = (
            (datetime(2026, 1, 1, tzinfo=timezone.utc), datetime(2026, 1, 1, tzinfo=timezone.utc)),
            (datetime(2026, 1, 1, 0, 0, 0, 1, tzinfo=timezone.utc),
             datetime(2026, 1, 1, 0, 0, 1, 1, tzinfo=timezone.utc)),
            (datetime(2026, 1, 1, 0, 0, 0, 1, tzinfo=timezone.utc),
             datetime(2026, 1, 1, 0, 0, 0, 2, tzinfo=timezone.utc)),
            (datetime(2026, 6, 30, 23, 59, 59, tzinfo=timezone.utc),
             datetime(2026, 7, 1, tzinfo=timezone.utc)),
            (datetime(2026, 12, 31, 23, 59, 59, tzinfo=timezone.utc),
             datetime(2027, 1, 1, tzinfo=timezone.utc)),
        )
        patterns = ((False,), (False, True), (True, False, True))
        for phase in ("pre", "post"):
            for requested, received in pairs:
                for framing in ("length", "eof"):
                    for pattern in patterns:
                        for reverse in (False, True):
                            with self.subTest(phase=phase, requested=requested.isoformat(),
                                              framing=framing, pattern=pattern, reverse=reverse), \
                                 tempfile.TemporaryDirectory() as directory:
                                root = Path(directory)
                                raw = body(pattern, reverse=reverse)
                                result = self.run_capture(requested, received, phase=phase,
                                                          raw=raw, framing=framing, root=root)
                                self.assert_artifacts(result, root, phase, requested, received, raw,
                                                       solved_map(pattern, reverse=reverse))

    def test_raw_input_and_opposite_phase_are_preserved(self):
        requested = datetime(2026, 2, 2, tzinfo=timezone.utc)
        received = datetime(2026, 2, 2, 0, 0, 1, tzinfo=timezone.utc)
        first = self.run_capture(requested, received, phase="pre")
        self.assert_artifacts(first, self.root, "pre", requested, received, self.raw,
                              solved_map((False, True)))
        before = filesystem(self.root)
        second = self.run_capture(requested, received, phase="post")
        self.assert_artifacts(second, self.root, "post", requested, received, self.raw,
                              solved_map((False, True)))
        self.assertEqual({key: value for key, value in filesystem(self.root).items()
                          if key == "pre" or key.startswith("pre/")}, before)

    def test_eof_framing_preserves_exact_artifact(self):
        requested = datetime(2026, 2, 3, tzinfo=timezone.utc)
        received = datetime(2026, 2, 3, 0, 0, 1, tzinfo=timezone.utc)
        result = self.run_capture(requested, received, framing="eof")
        self.assert_artifacts(result, self.root, "pre", requested, received, self.raw,
                               solved_map((False, True)))

    def test_solved_permutation_is_bound_to_normalized_artifact(self):
        requested = datetime(2026, 2, 4, tzinfo=timezone.utc)
        received = datetime(2026, 2, 4, 0, 0, 1, tzinfo=timezone.utc)
        raw = body((True, False, True))
        result = self.run_capture(requested, received, phase="post", raw=raw)
        self.assert_artifacts(result, self.root, "post", requested, received, raw,
                               solved_map((True, False, True)))

    def test_clock_regression_reads_once_and_writes_nothing(self):
        pairs = (
            (datetime(2026, 3, 2, 0, 0, 0, 1, tzinfo=timezone.utc),
             datetime(2026, 3, 2, tzinfo=timezone.utc)),
            (datetime(2026, 3, 2, 0, 0, 1, tzinfo=timezone.utc),
             datetime(2026, 3, 2, tzinfo=timezone.utc)),
            (datetime(2027, 1, 1, tzinfo=timezone.utc),
             datetime(2026, 12, 31, 23, 59, 59, tzinfo=timezone.utc)),
        )
        for phase in ("pre", "post"):
            for framing in ("length", "eof"):
                for requested, received in pairs:
                    with self.subTest(phase=phase, framing=framing, requested=requested.isoformat()), \
                         tempfile.TemporaryDirectory() as directory:
                        root = Path(directory)
                        (root / "sentinel").write_bytes(b"keep")
                        opposite = "post" if phase == "pre" else "pre"
                        snapshot_collector.write_snapshot(root, opposite, self.raw, CONTEXT,
                                                          max_bytes=self.config.max_bytes)
                        before = filesystem(root)
                        with self.assertRaises(SnapshotCollectionError) as caught:
                            self.run_capture(requested, received, phase=phase, framing=framing, root=root)
                        self.assertEqual(str(caught.exception), "수집 시계 역행")
                        self.assertEqual(FrozenClock.calls, [timezone.utc, timezone.utc])
                        self.assertEqual(MemoryConnection.events, ["clock", "request", "read", "clock"])
                        self.assertEqual(len(MemoryConnection.requests), 1)
                        self.assertEqual(filesystem(root), before)

    def test_mutation_swapping_times_fails_same_clock_and_artifact_oracle(self):
        requested = datetime(2026, 4, 1, tzinfo=timezone.utc)
        received = datetime(2026, 4, 1, 0, 0, 1, tzinfo=timezone.utc)
        expected = self.run_capture(requested, received)
        self.assert_artifacts(expected, self.root, "pre", requested, received, self.raw,
                              solved_map((False, True)))
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            original_factory = snapshot_collector.CollectedSnapshot

            def swapped_factory(artifacts, requested_at, received_at):
                return original_factory(artifacts, received_at, requested_at)

            with patch.object(snapshot_collector, "CollectedSnapshot", swapped_factory):
                result = self.run_capture(requested, received, root=root)
            with self.assertRaises(AssertionError):
                self.assert_artifacts(result, root, "pre", requested, received, self.raw,
                                       solved_map((False, True)))
            self.assertEqual(result.requested_at, received.isoformat())
            self.assertEqual(result.received_at, requested.isoformat())
            self.assertTrue((root / "pre/raw.json").exists())
            self.assertTrue((root / "pre/normalized.json").exists())
            self.assertEqual(len(MemoryConnection.requests), 1)
        self.assertEqual(expected.requested_at, requested.isoformat())

    def test_mutation_binding_wrong_phase_fails_artifact_oracle(self):
        requested = datetime(2026, 5, 1, tzinfo=timezone.utc)
        received = datetime(2026, 5, 1, 0, 0, 1, tzinfo=timezone.utc)
        original = snapshot_collector.write_snapshot

        def wrong_phase(root, phase, raw, context, *, max_bytes, opaque=False):
            return original(root, "post" if phase == "pre" else "pre", raw, context, max_bytes=max_bytes, opaque=opaque)

        baseline = self.run_capture(requested, received, phase="pre")
        self.assert_artifacts(baseline, self.root, "pre", requested, received, self.raw,
                               solved_map((False, True)))
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = self.run_capture(requested, received, phase="pre", root=root,
                                      writer_override=wrong_phase)
            with self.assertRaises(AssertionError):
                self.assert_artifacts(result, root, "pre", requested, received, self.raw,
                                       solved_map((False, True)))
            self.assertEqual(result.artifacts.raw["phase"], "post")
            self.assertEqual(result.artifacts.normalized["phase"], "post")
            self.assertEqual(result.artifacts.raw["path"], "post/raw.json")
            self.assertEqual(result.artifacts.normalized["path"], "post/normalized.json")
            self.assertTrue((root / "post/raw.json").exists())
            self.assertTrue((root / "post/normalized.json").exists())


if __name__ == "__main__":
    unittest.main()
