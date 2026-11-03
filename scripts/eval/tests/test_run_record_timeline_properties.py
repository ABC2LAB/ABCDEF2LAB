"""합성 v1 기록의 시각·순서·요청 창·해결 이벤트 경계 생성형 회귀.

기존 reader의 수용 경계만 검사하며 실제 실행 관측·시간 측정이나 v1.1 정책을
구현하지 않는다. 내부 거부 지점을 검사하기 위해 합성 파일만 다시 서명한다.
"""

from __future__ import annotations

import contextlib
import copy
import datetime as dt
import hashlib
import io
import itertools
import json
import os
import re
import socket
import subprocess
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

KEY = "basketAccessChallenge"
STEPS = ("fixture_ready", "pre", "engine_start", "engine_end", "drained", "post")
BASE = {
    "fixture_ready": ("2026-10-30T00:00:00+00:00", 0),
    "pre": ("2026-10-30T00:00:01+00:00", 10),
    "engine_start": ("2026-10-30T00:00:02+00:00", 20),
    "engine_end": ("2026-10-30T00:00:08+00:00", 80),
    "drained": ("2026-10-30T00:00:09+00:00", 90),
    "post": ("2026-10-30T00:00:10+00:00", 100),
}


def _instant(text: str) -> dt.datetime:
    return dt.datetime.fromisoformat(text.replace("Z", "+00:00"))


def _iso(value: dt.datetime) -> str:
    return value.isoformat(timespec="microseconds")


class _TimelineCase(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.key = b"timeline-property-key-0123456789abcdef"
        self.fixture = RunFixture(self.tmp.name, self.key)
        for module, name in ((socket, "socket"), (socket, "create_connection"),
                             (socket, "getaddrinfo"), (subprocess, "Popen")):
            patcher = patch.object(module, name, side_effect=AssertionError(name))
            patcher.start()
            self.addCleanup(patcher.stop)
        patcher = patch("os.system", side_effect=AssertionError("system"))
        patcher.start()
        self.addCleanup(patcher.stop)

    def make(self, name: str, *, solved=None, actor="engine"):
        return self.fixture.make(name, [], solved=solved, actor=actor)

    def manifest(self, run):
        path = Path(self.tmp.name) / "manifest.json"
        path.write_text(json.dumps({"runs": [run]}), encoding="utf-8")
        return load_manifests(str(path))[0], path.read_bytes()

    def _tree(self):
        root = Path(self.tmp.name)
        result = {}
        for path in sorted(root.rglob("*")):
            rel = str(path.relative_to(root))
            if path.is_symlink():
                result[rel] = ("link", os.readlink(path))
            elif path.is_dir():
                result[rel] = ("dir",)
            else:
                result[rel] = ("file", path.read_bytes())
        return result

    def load_observed(self, run, *, expect_error=None):
        manifest, manifest_bytes = self.manifest(run)
        before_run, before_manifest, before_tree = copy.deepcopy(run), copy.deepcopy(manifest), self._tree()
        out, err = io.StringIO(), io.StringIO()

        def read():
            try:
                with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                    return load_run_inputs(manifest, self.key, lambda _record: None)
            finally:
                self.assertEqual(out.getvalue(), "")
                self.assertEqual(err.getvalue(), "")
                self.assertEqual(run, before_run)
                self.assertEqual(manifest, before_manifest)
                self.assertEqual((Path(self.tmp.name) / "manifest.json").read_bytes(), manifest_bytes)
                self.assertEqual(self._tree(), before_tree)

        if expect_error is not None:
            with self.assertRaisesRegex(RunRecordError, re.escape(expect_error)):
                read()
            return None
        return read()

    def receipt_change(self, run, change):
        receipt = self.fixture.receipt(run)
        change(receipt)
        self.fixture._write_receipt(run, receipt)

    def artifact_path(self, run, name):
        receipt = self.fixture.receipt(run)
        return (Path(self.tmp.name) / run["run_record_path"]).parent / receipt["artifacts"][name]["path"]

    def artifact_change(self, run, name, change):
        path = self.artifact_path(run, name)
        payload = json.loads(path.read_bytes())
        change(payload)
        path.write_text(json.dumps(payload), encoding="utf-8")
        self.fixture.reseal(run)

    def assert_valid_oracle(self, run, *, solved=None, traffic=None, actor="engine"):
        result = self.load_observed(run)
        expected_pre = {key: False for key in KEYS}
        expected_post = dict(expected_pre)
        expected_post.update(solved or {})
        run_name = run["run_record_path"].split("/")[0]
        credited = (frozenset(key for key, value in (solved or {}).items() if value is True)
                    if actor == "engine" else frozenset())
        expected = RunInputs(expected_pre, expected_post, {
            "run_id": "run-" + run_name, "model_tag": "test-model",
            "reset_generation": "gen-" + run_name, "records": []}, traffic, None,
            credited,
            "execution-" + run_name, hashlib.sha256(b"fixture-v1").hexdigest(),
            hashlib.sha256(b"environment-v1").hexdigest())
        self.assertEqual(result, expected)
        for states, expected_states in ((result.pre, expected_pre), (result.post, expected_post)):
            for key in KEYS:
                self.assertIs(states[key], expected_states[key])
        return result

    def test_timeline_equal_and_one_microsecond_matrix(self):
        offsets = (dt.timezone.utc, dt.timezone(dt.timedelta(hours=9)),
                   dt.timezone(dt.timedelta(hours=-5)))
        for before, after in zip(STEPS, STEPS[1:]):
            for delta, should_pass in ((0, True), (1, True), (-1, False)):
                for offset in offsets:
                    with self.subTest(before=before, after=after, delta=delta, offset=offset):
                        run = self.make(f"timeline-{before}-{delta}-{offset.utcoffset(None).total_seconds()}")
                        instant = _instant(BASE[before][0]) + dt.timedelta(microseconds=delta)
                        text = _iso(instant.astimezone(offset))
                        self.receipt_change(run, lambda receipt, after=after, text=text:
                                            receipt["timeline"][after].update(at=text))
                        if should_pass:
                            self.assert_valid_oracle(run)
                        else:
                            self.load_observed(run, expect_error="순서 오류")

    def test_timeline_sequence_strict_matrix_and_all_step_types(self):
        for before, after in zip(STEPS, STEPS[1:]):
            for delta in (1, 0, -1):
                with self.subTest(pair=(before, after), delta=delta):
                    run = self.make(f"seq-{before}-{delta}")
                    self.receipt_change(run, lambda receipt, before=before, after=after, delta=delta:
                                        receipt["timeline"][after].update(
                                            sequence=receipt["timeline"][before]["sequence"] + delta))
                    if delta == 1:
                        self.assert_valid_oracle(run)
                    else:
                        expected = "sequence" if delta < 0 and BASE[before][1] + delta < 0 else "순서 오류"
                        self.load_observed(run, expect_error=expected)
        for step in STEPS:
            for index, value in enumerate((True, False, -1, 20.0, "20", None)):
                with self.subTest(step=step, value=value):
                    run = self.make(f"seq-type-{step}-{index}")
                    self.receipt_change(run, lambda receipt, step=step, value=value:
                                        receipt["timeline"][step].update(sequence=value))
                    self.load_observed(run, expect_error="sequence")

    def test_timezone_and_naive_time_contract(self):
        for offset in (dt.timezone.utc, dt.timezone(dt.timedelta(hours=9)),
                       dt.timezone(dt.timedelta(hours=-5))):
            run = self.make("timezone-" + str(offset.utcoffset(None).total_seconds()))
            text = _iso(_instant(BASE["engine_start"][0]).astimezone(offset))
            self.receipt_change(run, lambda receipt, text=text:
                                receipt["timeline"]["engine_start"].update(at=text))
            self.assert_valid_oracle(run)
        run = self.make("naive")
        self.receipt_change(run, lambda receipt: receipt["timeline"]["engine_start"].update(
            at="2026-10-30T00:00:02"))
        self.load_observed(run, expect_error="timezone")

    def test_traffic_sent_completed_cartesian_boundaries(self):
        run = self.make("traffic")
        self.fixture.evidence(run, traffic={"request": "rest:basket:1"})
        self.assert_valid_oracle(run, traffic={"request": "rest:basket:1"})
        # Explicit validity flags are independent of the reader's comparisons.
        sent_cases = (("01.999999", False), ("02.000000", True),
                      ("08.000000", True), ("08.000001", False))
        completed_cases = (("before_sent", False), ("at_sent", True),
                           ("at_drained", True), ("after_drained", False))
        for (second, sent_ok), (kind, completed_ok) in itertools.product(sent_cases, completed_cases):
            with self.subTest(sent=second, completed=kind):
                sent = _instant("2026-10-30T00:00:" + second + "+00:00")
                completed = {
                    "before_sent": sent - dt.timedelta(microseconds=1),
                    "at_sent": sent,
                    "at_drained": _instant("2026-10-30T00:00:09+00:00"),
                    "after_drained": _instant("2026-10-30T00:00:09.000001+00:00"),
                }[kind]
                self.artifact_change(run, "traffic", lambda payload:
                                     payload["requests"][0].update(
                                         sent_at=_iso(sent), completed_at=_iso(completed)))
                if sent_ok and completed_ok:
                    self.assert_valid_oracle(run, traffic={"request": "rest:basket:1"})
                else:
                    self.load_observed(run, expect_error="traffic 요청이 엔진 실행 구간 밖")

    def test_solve_event_time_sequence_actor_cartesian_boundaries(self):
        run = self.make("event-boundaries", solved={KEY: True})
        self.assert_valid_oracle(run, solved={KEY: True})
        # (seconds, inside pre/post time, inside engine/drained time).
        times = (("00.999999", False, False), ("01.000000", True, False),
                 ("01.999999", True, False), ("02.000000", True, True),
                 ("08.000000", True, True), ("08.500000", True, True),
                 ("09.000000", True, True), ("09.000001", True, False),
                 ("10.000000", True, False), ("10.000001", False, False))
        # (sequence, strictly inside pre/post, strictly inside engine/drained).
        sequences = ((10, False, False), (11, True, False), (20, True, False),
                     (21, True, True), (89, True, True), (90, True, False),
                     (99, True, False), (100, False, False))
        for (second, outer_time, engine_time), (sequence, outer_seq, engine_seq), actor in itertools.product(
                times, sequences, ("engine", "other", "unknown")):
            with self.subTest(second=second, sequence=sequence, actor=actor):
                execution = "execution-event-boundaries" if actor == "engine" else None
                self.artifact_change(run, "solve_events", lambda payload:
                                     payload["events"][0].update(
                                         observed_at="2026-10-30T00:00:" + second + "+00:00",
                                         sequence=sequence, actor=actor, execution_id=execution))
                if not outer_time or not outer_seq:
                    self.load_observed(run, expect_error="solve event가 pre/post 구간 밖")
                elif actor == "engine" and (not engine_time or not engine_seq):
                    self.load_observed(run, expect_error="엔진 solve event가 실행 구간 밖")
                else:
                    self.assert_valid_oracle(run, solved={KEY: True}, actor=actor)

    def test_actor_and_execution_id_matrix(self):
        run = self.make("actor", solved={KEY: True})
        self.assert_valid_oracle(run, solved={KEY: True})
        for actor, execution in itertools.product(
                ("engine", "other", "unknown"), (None, "wrong", "execution-actor")):
            with self.subTest(actor=actor, execution=execution):
                self.artifact_change(run, "solve_events", lambda payload:
                                     payload["events"][0].update(actor=actor, execution_id=execution))
                if (actor == "engine" and execution == "execution-actor") or (
                        actor != "engine" and execution is None):
                    self.assert_valid_oracle(run, solved={KEY: True}, actor=actor)
                else:
                    message = ("solve event execution_id 불일치" if actor == "engine" else
                               "엔진 이외 solve event의 execution_id는 null이어야 함")
                    self.load_observed(run, expect_error=message)

    def test_event_time_weakening_mutant_fails_same_rejection_oracle(self):
        run = self.make("mutant-time", solved={KEY: True})
        self.assert_valid_oracle(run, solved={KEY: True})
        outside = "2026-10-30T00:00:01.999999+00:00"
        self.artifact_change(run, "solve_events", lambda payload:
                             payload["events"][0].update(observed_at=outside))
        self.load_observed(run, expect_error="엔진 solve event가 실행 구간 밖")
        original_time = run_record._time

        def clip_event_time(value, label):
            if label == "event.observed_at" and value == outside:
                return _instant("2026-10-30T00:00:02+00:00")
            return original_time(value, label)

        with patch("run_record._time", clip_event_time):
            self.assert_valid_oracle(run, solved={KEY: True})
            with self.assertRaises(AssertionError):
                self.load_observed(run, expect_error="엔진 solve event가 실행 구간 밖")

    def test_boolean_sequence_weakening_mutant_fails_same_rejection_oracle(self):
        run = self.make("mutant-bool", solved={KEY: True})
        self.assert_valid_oracle(run, solved={KEY: True})
        self.artifact_change(run, "solve_events", lambda payload:
                             payload["events"][0].update(sequence=True))
        self.load_observed(run, expect_error="sequence: 0 이상 정수 필요")
        original_sequence = run_record._sequence

        def accept_boolean(value):
            return 50 if value is True else original_sequence(value)

        with patch("run_record._sequence", accept_boolean):
            self.assert_valid_oracle(run, solved={KEY: True})
            with self.assertRaises(AssertionError):
                self.load_observed(run, expect_error="sequence: 0 이상 정수 필요")


if __name__ == "__main__":
    unittest.main()
