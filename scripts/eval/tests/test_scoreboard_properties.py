"""원본 행→정규화→저장 스냅샷→필수 key 검사의 유한한 조합 회귀.

합성 key와 임시 JSON만 사용한다. 저장 파서는 형식·회차 문맥을 검사하고,
key 누락은 require_basis_keys가 검사한다. 실제 GT·서명·해결 귀속은 검증하지 않는다.
"""

from __future__ import annotations

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

import scoreboard
from ground_truth import Axis, Challenge
from manifest import Engine, RunManifest

_KEYS = ("synthetic-z", "합성-a", "synthetic:middle", "synthetic_b")
_RUN, _RESET = "synthetic-run", "synthetic-reset"
_MISSING = object()
_BASIS = tuple(Challenge(key, key, Axis.HORIZONTAL, True, True) for key in _KEYS)


def _object_json(pairs):
    # dict로 변환하지 않아 같은 값의 중복 key도 JSON에 그대로 남긴다.
    return "{" + ",".join(json.dumps(key, ensure_ascii=False) + ":" + json.dumps(value, ensure_ascii=False)
                          for key, value in pairs) + "}"


def _envelope(solved):
    return {"run_id": _RUN, "reset_generation": _RESET, "solved": solved}


def _envelope_json(solved_json):
    return (f'{{"run_id": {json.dumps(_RUN)}, "reset_generation": {json.dumps(_RESET)}, '
            f'"solved": {solved_json}}}')


def _payload(rows, wrapped):
    return {"status": "success", "data": rows} if wrapped else rows


class TestScoreboardProperties(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.path = Path(tmp.name) / "snapshot.json"
        self.manifest = RunManifest(
            condition="synthetic", engine=Engine.OURS, run_id=_RUN, model_tag="synthetic-model",
            reset_generation=_RESET, repeat_index=0, records_path=str(self.path.parent / "unused-records.json"),
            snapshot_path=str(self.path), pre_snapshot_path=str(self.path.parent / "unused-pre.json"),
            run_record_path=str(self.path.parent / "unused-runner.json"),
        )
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("offline synthetic scoreboard only"))
            guard.start()
            self.addCleanup(guard.stop)

    def normalize(self, payload):
        before = json.dumps(payload, ensure_ascii=False)
        try:
            with patch("builtins.open", side_effect=AssertionError("normalization must not read files")):
                return scoreboard.normalize_challenges(payload)
        finally:
            self.assertEqual(json.dumps(payload, ensure_ascii=False), before)

    def parse(self, wire, manifest=None):
        self.path.write_text(wire, encoding="utf-8")
        before = self.path.read_bytes(), replace(self.manifest)
        supplied_before = replace(manifest) if manifest is not None else None
        try:
            return scoreboard.parse_scoreboard_snapshot(str(self.path), manifest=manifest)
        finally:
            self.assertEqual((self.path.read_bytes(), self.manifest), before)
            self.assertEqual(manifest, supplied_before)

    def assert_states(self, actual, expected):
        self.assertEqual(actual, expected)
        for key, state in expected.items():
            self.assertIs(actual[key], state)  # 0/1은 False/True와 같아도 불리언이 아니다.

    def assert_snapshot_rejected(self, wire, message, manifest=None):
        with self.assertRaisesRegex(scoreboard.SnapshotError, message):
            self.parse(wire, manifest)

    def assert_basis(self, solved, basis, missing):
        before = solved.copy()
        try:
            if missing:
                with self.assertRaisesRegex(scoreboard.SnapshotError, "GT 챌린지 key 누락") as caught:
                    scoreboard.require_basis_keys(solved, basis)
                self.assertIn(repr(sorted(missing)), str(caught.exception))
            else:
                self.assertIsNone(scoreboard.require_basis_keys(solved, basis))
        finally:
            self.assert_states(solved, before)

    def test_all_boolean_states_and_row_orders_roundtrip_both_snapshot_formats(self):
        for states, order, wrapped in itertools.product(
                itertools.product((False, True), repeat=4), itertools.permutations(range(4)), (False, True)):
            expected = dict(zip(_KEYS, states))
            rows = [{"key": _KEYS[i], "solved": states[i], "name": "ignored synthetic field"} for i in order]
            with self.subTest(states=states, order=order, wrapped=wrapped):
                normalized = self.normalize(_payload(rows, wrapped))
                self.assert_states(normalized, expected)
                for pairs in (tuple(normalized.items()), tuple(reversed(tuple(normalized.items())))):
                    wire = _object_json(pairs)
                    self.assert_states(self.parse(wire), expected)
                    self.assert_states(self.parse(_envelope_json(wire), self.manifest), expected)
        for wrapped in (False, True):
            with self.subTest(empty=True, wrapped=wrapped):
                normalized = self.normalize(_payload([], wrapped))
                self.assert_states(normalized, {})
                self.assert_states(self.parse(json.dumps(normalized)), {})
                self.assert_states(self.parse(json.dumps(_envelope(normalized)), self.manifest), {})

    def test_generated_basis_subsets_distinguish_missing_keys_from_false_and_extras(self):
        profiles = ((False,) * 4, (True,) * 4, (False, True, False, True))
        for available, required, states, extra in itertools.product(range(16), range(16), profiles, (False, True)):
            expected = {key: states[i] for i, key in enumerate(_KEYS) if available & (1 << i)}
            if extra:
                expected["outside-synthetic-basis"] = False
            basis = tuple(challenge for i, challenge in enumerate(_BASIS) if required & (1 << i))
            missing = {key for i, key in enumerate(_KEYS) if required & (1 << i) and not available & (1 << i)}
            with self.subTest(available=available, required=required, states=states, extra=extra):
                rows = [{"key": key, "solved": state} for key, state in reversed(tuple(expected.items()))]
                normalized = self.normalize(_payload(rows, True))
                self.assert_states(normalized, expected)
                solved = self.parse(json.dumps(_envelope(normalized)), self.manifest)
                self.assert_states(solved, expected)
                for iterable in (basis, list(basis), iter(basis)):
                    self.assert_basis(solved, iterable, missing)

    def test_non_boolean_or_absent_solved_never_becomes_a_boolean(self):
        invalid = (0, 1, "true", "false", None, [], {}, 1.5, _MISSING)
        for key, bad, background, wrapped in itertools.product(_KEYS, invalid, (False, True), (False, True)):
            expected = dict.fromkeys(_KEYS, background)
            rows = [{"key": item, "solved": background} for item in _KEYS]
            changed = rows[_KEYS.index(key)]
            if bad is _MISSING:
                del changed["solved"]
                del expected[key]
            else:
                changed["solved"] = bad
                expected[key] = bad
            with self.subTest(key=key, bad="missing" if bad is _MISSING else repr(bad),
                              background=background, wrapped=wrapped):
                with self.assertRaisesRegex(scoreboard.SnapshotError, "solved가 불리언 아님"):
                    self.normalize(_payload(rows, wrapped))
                wire = json.dumps(expected)
                for stored, manifest in ((wire, None), (_envelope_json(wire), self.manifest)):
                    if bad is _MISSING:
                        # 정규화 객체의 key 누락은 저장 형식 오류가 아니다. 분모 검사에서 거부한다.
                        solved = self.parse(stored, manifest)
                        self.assert_states(solved, expected)
                        self.assert_basis(solved, _BASIS, {key})
                    else:
                        self.assert_snapshot_rejected(stored, "solved가 불리언 아님", manifest)

    def test_duplicate_keys_are_rejected_for_every_state_position_and_format(self):
        for key, original, duplicate, position, wrapped in itertools.product(
                _KEYS, (False, True), (False, True), range(5), (False, True)):
            pairs = [(item, original if item == key else False) for item in _KEYS]
            pairs.insert(position, (key, duplicate))
            rows = [{"key": item, "solved": state} for item, state in pairs]
            with self.subTest(key=key, original=original, duplicate=duplicate, position=position, wrapped=wrapped):
                with self.assertRaisesRegex(scoreboard.SnapshotError, "key 중복"):
                    self.normalize(_payload(rows, wrapped))
                wire = _object_json(pairs)
                self.assert_snapshot_rejected(wire, "key 중복")
                self.assert_snapshot_rejected(_envelope_json(wire), "key 중복", self.manifest)

    def test_envelope_field_orders_context_mismatches_duplicates_and_shapes(self):
        solved = dict(zip(_KEYS, (False, True, False, True)))
        envelope = _envelope(solved)
        for order in itertools.permutations(envelope):
            with self.subTest(order=order):
                self.assert_states(self.parse(_object_json([(key, envelope[key]) for key in order]), self.manifest), solved)
        fields = ("run_id", "reset_generation")
        for mask, mode in itertools.product(range(1, 4), ("missing", "different", "null")):
            changed = envelope.copy()
            selected = tuple(field for bit, field in enumerate(fields) if mask & (1 << bit))
            for field in selected:
                if mode == "missing":
                    del changed[field]
                else:
                    changed[field] = None if mode == "null" else "different-context"
            with self.subTest(fields=selected, mode=mode):
                for field in selected:
                    self.assert_snapshot_rejected(json.dumps(changed), field + " 불일치/부재", self.manifest)
        for field in fields:
            with self.subTest(manifest_field=field):
                self.assert_snapshot_rejected(json.dumps(envelope), field + " 불일치/부재",
                                              replace(self.manifest, **{field: "other-context"}))
        for field, position, mode in itertools.product(envelope, range(4), ("same", "different", "null")):
            value = envelope[field] if mode == "same" else None if mode == "null" else "different-value"
            pairs = list(envelope.items())
            pairs.insert(position, (field, value))
            with self.subTest(duplicate=field, position=position, value=mode):
                self.assert_snapshot_rejected(_object_json(pairs), "envelope key 중복", self.manifest)
        for bad in (_MISSING, None, True, 0, [], ["not-object"], "not-object"):
            changed = envelope.copy()
            if bad is _MISSING:
                del changed["solved"]
            else:
                changed["solved"] = bad
            with self.subTest(solved="missing" if bad is _MISSING else repr(bad)):
                self.assert_snapshot_rejected(json.dumps(changed), "solved 객체 부재/부정", self.manifest)
        for field in ("", "model_tag", "phase"):
            with self.subTest(unknown=field):
                self.assert_snapshot_rejected(json.dumps(envelope | {field: True}), "모르는 필드", self.manifest)

    def test_raw_and_stored_roles_and_malformed_raw_rows_are_not_interchangeable(self):
        rows = [{"key": _KEYS[0], "solved": False}, {"key": _KEYS[1], "solved": True}]
        solved = {_KEYS[0]: False, _KEYS[1]: True}
        self.assert_states(self.normalize(rows), solved)
        self.assert_states(self.parse(json.dumps(solved)), solved)
        self.assert_states(self.parse(json.dumps(_envelope(solved)), self.manifest), solved)
        for wrapped, manifest in itertools.product((False, True), (None, self.manifest)):
            with self.subTest(raw_wrapped=wrapped, manifest=manifest is not None):
                self.assert_snapshot_rejected(json.dumps(_payload(rows, wrapped)), "객체가 아님|불리언 아님|envelope", manifest)
        for payload in (solved, _envelope(solved)):
            with self.subTest(wrong_normalizer_role=tuple(payload)):
                with self.assertRaisesRegex(scoreboard.SnapshotError, "'data' 행 배열이 없음"):
                    self.normalize(payload)
        self.assert_snapshot_rejected(json.dumps(solved), "envelope", self.manifest)
        self.assert_snapshot_rejected(json.dumps(_envelope(solved)), "불리언 아님")
        for payload, manifest in itertools.product((None, True, 7, "snapshot", []), (None, self.manifest)):
            with self.subTest(stored_root=repr(payload), manifest=manifest is not None):
                self.assert_snapshot_rejected(json.dumps(payload), "최상위가 객체가 아님", manifest)
        for payload in (None, True, {}, {"data": None}, {"data": {}}, {"data": True}):
            with self.subTest(raw_root=repr(payload)):
                with self.assertRaises(scoreboard.SnapshotError):
                    self.normalize(payload)
        for bad, wrapped in itertools.product((_MISSING, "", None, 0, False, [], {}), (False, True)):
            changed = {"solved": True} if bad is _MISSING else {"key": bad, "solved": True}
            with self.subTest(raw_key="missing" if bad is _MISSING else repr(bad), wrapped=wrapped):
                with self.assertRaisesRegex(scoreboard.SnapshotError, "key 부재/빈값"):
                    self.normalize(_payload([rows[0], changed, rows[1]], wrapped))
        for bad, position, wrapped in itertools.product((None, True, 0, "row", [], ["row"]), range(3), (False, True)):
            changed = rows.copy()
            changed.insert(position, bad)
            with self.subTest(raw_row=repr(bad), position=position, wrapped=wrapped):
                with self.assertRaisesRegex(scoreboard.SnapshotError, "행이 객체가 아님"):
                    self.normalize(_payload(changed, wrapped))

    def test_assertions_detect_coercion_duplicate_loss_context_bypass_and_missing_key_bypass(self):
        bad_rows = [{"key": _KEYS[0], "solved": "false"}]
        duplicate = _object_json([(_KEYS[0], False), (_KEYS[0], True)])
        wrong_run = json.dumps(_envelope({_KEYS[0]: False}) | {"run_id": "other-run"})

        def assert_non_boolean_rejected():
            with self.assertRaisesRegex(scoreboard.SnapshotError, "solved가 불리언 아님"):
                self.normalize(bad_rows)

        # 각 약화 전에 정상 대조와 원래 실패 검사를 실행해 검사기 자체의 오류를 배제한다.
        self.assert_states(self.normalize([{"key": _KEYS[0], "solved": False}]), {_KEYS[0]: False})
        assert_non_boolean_rejected()
        with patch.object(scoreboard, "normalize_challenges", side_effect=lambda rows: {row["key"]: bool(row["solved"]) for row in rows}):
            with self.assertRaises(AssertionError):
                assert_non_boolean_rejected()

        self.assert_states(self.parse(_object_json([(_KEYS[0], False), (_KEYS[1], True)])),
                           {_KEYS[0]: False, _KEYS[1]: True})
        self.assert_snapshot_rejected(duplicate, "key 중복")

        original_load = json.load

        def discard_duplicate_keys(source, **kwargs):
            original_hook = kwargs["object_pairs_hook"]
            kwargs["object_pairs_hook"] = lambda pairs: original_hook(dict(pairs).items())
            return original_load(source, **kwargs)

        with patch.object(scoreboard.json, "load", side_effect=discard_duplicate_keys):
            with self.assertRaises(AssertionError):
                self.assert_snapshot_rejected(duplicate, "key 중복")

        self.assert_states(self.parse(json.dumps(_envelope({_KEYS[0]: False})), self.manifest), {_KEYS[0]: False})
        self.assert_snapshot_rejected(wrong_run, "run_id 불일치/부재", self.manifest)

        def ignore_manifest(path, manifest=None):
            with open(path, encoding="utf-8") as stream:
                return json.load(stream)["solved"]

        with patch.object(scoreboard, "parse_scoreboard_snapshot", side_effect=ignore_manifest):
            with self.assertRaises(AssertionError):
                self.assert_snapshot_rejected(wrong_run, "run_id 불일치/부재", self.manifest)

        self.assert_basis({_KEYS[0]: False}, _BASIS[:1], set())
        self.assert_basis({}, _BASIS[:1], {_KEYS[0]})
        with patch.object(scoreboard, "require_basis_keys", return_value=None):
            with self.assertRaises(AssertionError):
                self.assert_basis({}, _BASIS[:1], {_KEYS[0]})


if __name__ == "__main__":
    unittest.main()
