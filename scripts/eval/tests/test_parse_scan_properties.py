"""판정 파서의 파일·메모리 입력 조합 회귀. 합성 임시 JSON만 사용한다.

판정/충돌은 집합 규칙으로, 목록 밖 자원/confirmed 레코드는 입력 순서로 검증한다.
manifest는 파서 문맥만 제공한다. 실제 서명·실행 귀속·GT·채점·러너는 검증하지 않는다.
제품 코드·기존 테스트·의존성은 변경하지 않는다.
"""

from __future__ import annotations

import copy
import io
import itertools
import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from types import MappingProxyType
from typing import NamedTuple
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import collect
from aggregate import ScanVerdict
from manifest import Engine, RunManifest
from test_parse_scan_output import _record

_RUN, _MODEL, _RESET = "synthetic-run", "synthetic-model", "synthetic-reset"
_Z, _A = "syntheticZ", "syntheticA"
_OWNERS = {"synthetic:z/1": _Z, "synthetic:z/2": _Z, "synthetic:a/1": _A}
_RESOURCES = (*_OWNERS, "outside/z", "outside/a")
_VALUES = ("allowed", "undecidable", "suspected", "confirmed")
_SCANS = (ScanVerdict.ALLOWED, ScanVerdict.UNDECIDABLE, ScanVerdict.SUSPECTED, ScanVerdict.CONFIRMED)
_SCAN_BY_VALUE = dict(zip(_VALUES, _SCANS))
# bits: allowed=1, undecidable=2, suspected=4, confirmed=8. 집합별 기대값을 고정한다.
_WINNERS = (None, _SCANS[0], _SCANS[1], _SCANS[1], _SCANS[2], _SCANS[2], _SCANS[2], _SCANS[2]) + (_SCANS[3],) * 8
_CONFLICT_MASKS = frozenset({5, 7, 9, 11, 13, 15})
_MISSING = object()
_REFS = (("missing", _MISSING, ()), ("null", None, ()), ("empty", [], ()),
         ("mixed", [7, "7", "t-z", "t-a", "t-z"], ("7", "t-z", "t-a")),
         ("reversed", ["t-a", 7, "t-a", "t-z", "7"], ("t-a", "7", "t-z")))
_AXES = (("horizontal", "horizontal"), ("vertical", "vertical"), (None, None), (1, None))
_SECRET = "SYNTHETIC_PRIVATE_TOKEN_DO_NOT_ECHO"


class _Case(NamedTuple):
    raw: dict
    normalized: collect.VerdictRecord
    owner: str | None


def _case(value, resource, refs=_REFS[0], axis=_AXES[0]):
    raw = _record(value, resource, model_tag=_MODEL, reset_generation=_RESET, axis=axis[0])
    if refs[1] is not _MISSING:
        raw["traffic_evidence_refs"] = copy.deepcopy(refs[1])
    normalized = collect.VerdictRecord(value, None if value == "allowed" else value, "idor", resource,
                                       axis[1], _SCAN_BY_VALUE[value], refs[2])
    return _Case(raw, normalized, _OWNERS.get(resource))


def _correlate(record):
    return _OWNERS.get(record.resource_ref)


def _envelope(rows):
    return {"run_id": _RUN, "model_tag": _MODEL, "reset_generation": _RESET, "records": copy.deepcopy(rows)}


def _orders(count):
    order = tuple(range(count))
    return tuple(dict.fromkeys((order, order[::-1], order[1:] + order[:1])))


class TestParseScanProperties(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.path = Path(tmp.name) / "records.json"
        self.manifest = RunManifest(
            condition="synthetic", engine=Engine.OURS, run_id=_RUN, model_tag=_MODEL,
            reset_generation=_RESET, repeat_index=0, records_path=str(self.path),
            snapshot_path=str(self.path.parent / "unused-post.json"),
            pre_snapshot_path=str(self.path.parent / "unused-pre.json"),
            run_record_path=str(self.path.parent / "unused-runner.json"),
        )
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("offline synthetic parser only"))
            guard.start()
            self.addCleanup(guard.stop)

    def memory(self, payload):
        with patch("builtins.open", side_effect=AssertionError("memory parser must not reopen files")):
            return collect.parse_scan_payload(payload, _correlate, manifest=self.manifest)

    def assert_inputs(self, payload, before):
        self.assertEqual((payload, self.manifest, self.path.read_bytes()), before)

    def assert_result(self, actual, cases, verdicts, conflicts):
        outside = tuple(dict.fromkeys(case.normalized.resource_ref for case in cases if case.owner is None))
        confirmed = {}
        for key in (_A, _Z):
            records = tuple(case.normalized for case in cases
                            if case.owner == key and case.normalized.scan_verdict is ScanVerdict.CONFIRMED)
            if records:
                confirmed[key] = records
        self.assertEqual(actual.verdicts, verdicts)
        self.assertEqual(actual.out_of_list, outside)
        self.assertEqual(actual.conflicts, conflicts)
        self.assertEqual(dict(actual.confirmed_records), confirmed)

    def assert_parse(self, cases, verdicts, conflicts=()):
        payload = _envelope([case.raw for case in cases])
        self.path.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
        before = copy.deepcopy(payload), copy.deepcopy(self.manifest), self.path.read_bytes()
        file_result = collect.parse_scan_output(str(self.path), _correlate, manifest=self.manifest)
        memory_result = self.memory(payload)
        self.assertEqual(file_result, memory_result)
        self.assert_result(file_result, cases, verdicts, conflicts)
        self.assert_result(memory_result, cases, verdicts, conflicts)
        self.assert_inputs(payload, before)

    def reject_payload(self, payload):
        self.path.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
        before = copy.deepcopy(payload), copy.deepcopy(self.manifest), self.path.read_bytes()
        messages = []
        for parse in (lambda: collect.parse_scan_output(str(self.path), _correlate, manifest=self.manifest),
                      lambda: self.memory(payload)):
            out, err = io.StringIO(), io.StringIO()
            with redirect_stdout(out), redirect_stderr(err), self.assertRaises(collect.ScanOutputError) as caught:
                parse()
            self.assertEqual((out.getvalue(), err.getvalue()), ("", ""))
            messages.append(str(caught.exception))
        self.assertEqual(messages[0], messages[1])
        self.assertNotIn(_SECRET, messages[0])
        self.assert_inputs(payload, before)
        return messages[0]

    def assert_record_errors(self, rows, expected):
        message = self.reject_payload(_envelope(rows))
        count = sum(len(fragments) for fragments in expected.values())
        self.assertEqual(message.splitlines()[0], f"판정 레코드 {count}건 오류 (레코드 {len(rows)}개 중):")
        lines = message.splitlines()[1:]
        self.assertEqual(len(lines), count)
        for index, fragments in expected.items():
            indexed = [line for line in lines if line.startswith(f"  [{index}] ")]
            self.assertEqual(len(indexed), len(fragments))
            for fragment in fragments:
                self.assertTrue(any(fragment in line for line in indexed), fragment)

    def test_verdict_masks_orders_and_duplicates_preserve_precedence_and_conflicts(self):
        noise = (_case("confirmed", "outside/z"), _case("allowed", "outside/a"), _case("suspected", "outside/z"))
        for zmask, amask, duplicate in itertools.product(range(16), range(16), (False, True)):
            cases = tuple(_case(value, resource, _REFS[3]) for mask, resource in
                          ((zmask, "synthetic:z/1"), (amask, "synthetic:a/1"))
                          for bit, value in enumerate(_VALUES) if mask & (1 << bit)) + noise
            if duplicate:
                cases = tuple(case for case in cases for _ in range(2))
            expected = {key: _WINNERS[mask] for key, mask in ((_Z, zmask), (_A, amask)) if _WINNERS[mask] is not None}
            conflicts = tuple(key for key, mask in ((_A, amask), (_Z, zmask)) if mask in _CONFLICT_MASKS)
            for order in _orders(len(cases)):
                with self.subTest(zmask=zmask, amask=amask, duplicate=duplicate, order=order):
                    self.assert_parse(tuple(cases[index] for index in order), expected, conflicts)

    def test_all_small_record_permutations_preserve_confirmed_and_outside_input_order(self):
        cases = (_case("confirmed", "synthetic:z/1", _REFS[3]), _case("confirmed", "synthetic:z/2", _REFS[4]),
                 _case("confirmed", "synthetic:a/1"), _case("confirmed", "outside/z"), _case("allowed", "outside/a"))
        for order, duplicate in itertools.product(itertools.permutations(range(len(cases))), (False, True)):
            ordered = tuple(cases[index] for index in order)
            if duplicate:
                ordered += ordered[:1]
            with self.subTest(order=order, duplicate=duplicate):
                self.assert_parse(ordered, {_Z: ScanVerdict.CONFIRMED, _A: ScanVerdict.CONFIRMED})

    def test_evidence_ref_and_axis_normalization_agree_between_adapters(self):
        self.assert_parse((), {})
        for refs, axis, value, resource in itertools.product(_REFS, _AXES, _VALUES, _RESOURCES):
            with self.subTest(refs=refs[0], axis=axis[0], value=value, resource=resource):
                case = _case(value, resource, refs, axis)
                verdicts = {} if case.owner is None else {case.owner: _SCAN_BY_VALUE[value]}
                self.assert_parse((case,), verdicts)

    def test_bad_record_pairs_at_all_positions_aggregate_without_partial_results(self):
        base = _case("confirmed", "synthetic:z/1").raw
        missing = copy.deepcopy(base)
        del missing["vuln_verdict"]
        bad = ((missing, ("vuln_verdict 필드 부재",)),
               (_case("allowed", "synthetic:z/1").raw | {"vuln_verdict": "confirmed"}, ("allowed인데",)),
               (base | {"resource_ref": None}, ("resource_ref 부재/빈값",)),
               (base | {"traffic_evidence_refs": ["ok", True, None, ""]},
                ("잘못된 traffic ref: True", "잘못된 traffic ref: None", "잘못된 traffic ref: ''")),
               (base | {"token": _SECRET}, ("민감 필드 'token'",)),
               ("not-a-record", ("레코드가 객체가 아님",)),
               (base | {"challenge_key": "synthetic"}, ("계약에 없는 필드",)))
        for left, right in itertools.combinations(range(len(bad)), 2):
            entries = ((base, ()), bad[left], (_case("allowed", "synthetic:a/1").raw, ()), bad[right])
            for order in itertools.permutations(range(4)):
                with self.subTest(pair=(left, right), order=order):
                    rows = [entries[index][0] for index in order]
                    expected = {position: entries[index][1] for position, index in enumerate(order) if entries[index][1]}
                    self.assert_record_errors(rows, expected)

    def test_manifest_context_mismatches_are_rejected_by_both_adapters(self):
        fields = ("run_id", "model_tag", "reset_generation")
        base = _case("confirmed", "synthetic:z/1").raw
        for mask, missing in itertools.product(range(1, 8), (False, True)):
            payload = _envelope([base])
            selected = tuple(field for bit, field in enumerate(fields) if mask & (1 << bit))
            for field in selected:
                if missing:
                    del payload[field]
                else:
                    payload[field] = "different-context"
            with self.subTest(envelope=selected, missing=missing):
                message = self.reject_payload(payload)
                for field in selected:
                    self.assertIn(f"records envelope {field} 불일치/부재", message)
        for mask, missing, position in itertools.product(range(1, 4), (False, True), range(3)):
            changed = copy.deepcopy(base)
            selected = tuple(field for bit, field in enumerate(fields[1:]) if mask & (1 << bit))
            for field in selected:
                if missing:
                    del changed[field]
                else:
                    changed[field] = "different-context"
            rows = [base, _case("allowed", "synthetic:a/1").raw]
            rows.insert(position, changed)
            with self.subTest(record=selected, missing=missing, position=position):
                self.assert_record_errors(rows, {position: tuple(field + " 불일치/부재" for field in selected)})
        for payload in (None, [], "not-envelope", True):
            with self.subTest(root=payload):
                self.assertIn("envelope가 필요", self.reject_payload(payload))
        for records in (None, {}, False, "not-records"):
            with self.subTest(records=records):
                self.assertIn("레코드 배열이 아님", self.reject_payload(_envelope(records)))

    def test_memory_parser_uses_supplied_payload_after_artifact_replacement(self):
        for original, replacement, refs in itertools.product(_VALUES, _VALUES, _REFS[3:]):
            first = _case(original, "synthetic:z/1", refs)
            second = _case(replacement, "synthetic:a/1", _REFS[0])
            payload = _envelope([first.raw])
            self.path.write_text(json.dumps(_envelope([second.raw])), encoding="utf-8")
            before = copy.deepcopy(payload), copy.deepcopy(self.manifest), self.path.read_bytes()
            with self.subTest(original=original, replacement=replacement, refs=refs[0]):
                self.assert_result(self.memory(payload), (first,), {_Z: _SCAN_BY_VALUE[original]}, ())
                file_result = collect.parse_scan_output(str(self.path), _correlate, manifest=self.manifest)
                self.assert_result(file_result, (second,), {_A: _SCAN_BY_VALUE[replacement]}, ())
                self.assert_inputs(payload, before)

    def test_assertions_detect_weakened_precedence_conflicts_collection_and_validation(self):
        cases = (_case("confirmed", "synthetic:z/1", _REFS[3]), _case("allowed", "synthetic:z/1"),
                 _case("confirmed", "synthetic:z/2", _REFS[4]))
        expected, conflicts = {_Z: ScanVerdict.CONFIRMED}, (_Z,)
        bad = cases[0].raw | {"vuln_verdict": None}
        self.assert_parse(cases, expected, conflicts)
        self.assert_record_errors([cases[0].raw, bad], {1: ("≠ oracle_value",)})

        with patch.object(collect, "_PRECEDENCE", {verdict: index for index, verdict in enumerate(reversed(_SCANS))}):
            with self.assertRaises(AssertionError):
                self.assert_parse(cases, expected, conflicts)
        with patch.object(collect, "_VIOLATION_CANDIDATE", frozenset()):
            with self.assertRaises(AssertionError):
                self.assert_parse(cases, expected, conflicts)

        original_parse = collect.parse_scan_payload

        def first_confirmed_only(payload, correlate, *, manifest):
            result = original_parse(payload, correlate, manifest=manifest)
            return result._replace(confirmed_records=MappingProxyType({key: rows[:1] for key, rows in result.confirmed_records.items()}))

        with patch.object(collect, "parse_scan_payload", side_effect=first_confirmed_only):
            with self.assertRaises(AssertionError):
                self.assert_parse(cases, expected, conflicts)

        original_validate = collect._validate_record

        def omit_errors(index, raw, manifest=None):
            record, _ = original_validate(index, raw, manifest)
            return record, []

        with patch.object(collect, "_validate_record", side_effect=omit_errors):
            with self.assertRaises(AssertionError):
                self.assert_record_errors([cases[0].raw, bad], {1: ("≠ oracle_value",)})


if __name__ == "__main__":
    unittest.main()
