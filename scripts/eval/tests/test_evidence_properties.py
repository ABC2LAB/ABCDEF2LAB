"""평가 증거 참조의 유한 조합 회귀. 실제 traffic·GT 파일·I/O는 사용하지 않는다.

합성 traffic의 상태표로 기대 ref 집합을 정하고 공개 증거 API와 대조한다.
confirmed 선별·서명·실행/해결 귀속·실제 export는 상위 호출자의 별도 경계다.
제품 코드·기존 테스트·의존성은 변경하지 않는다.
"""

from __future__ import annotations

import copy
import itertools
import os
import sys
import unittest
from dataclasses import asdict
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import evidence
from aggregate import ScanVerdict
from collect import VerdictRecord
from correlation import CorrelationTable, Rule

_TARGET, _OTHER, _UNKNOWN = "syntheticTarget", "syntheticOther", "syntheticUnknown"
_KEYS = (_TARGET, _OTHER, _UNKNOWN)
_REFS = ("ref-z", "ref-a", "ref-m")
_KINDS = ("absent", "target", "other", "unmapped")
_ASSIGNMENTS = tuple(itertools.product(_KINDS, repeat=len(_REFS)))
_SUBSETS = tuple(tuple(ref for ref, enabled in zip(_REFS, flags) if enabled)
                 for flags in itertools.product((False, True), repeat=len(_REFS)))
_RESOURCES = {"target": "rest:cart:17", "other": "rest:note:29", "unmapped": "synthetic:outside:5"}
_HIT_KIND = {(_TARGET, "idor"): "target", (_OTHER, "xss"): "other"}
_CORRELATE = CorrelationTable((
    Rule(_TARGET, "horizontal", "idor", ("rest:cart",)),
    Rule(_OTHER, "vertical", "xss", ("rest:note",)),
)).correlate


def _record(refs=(), *, vuln_class="idor", resource_ref="claim:only:999", axis="horizontal"):
    return VerdictRecord("confirmed", "confirmed", vuln_class, resource_ref, axis,
                         ScanVerdict.CONFIRMED, tuple(refs))


def _traffic(kinds):
    return {ref: _RESOURCES[kind] for ref, kind in zip(_REFS, kinds) if kind != "absent"}


def _expected_sets(kinds, key, vuln_class):
    # 실제 resource_ref 파싱/상관 함수를 재구현하지 않고 합성 상태표의 라벨을 사용한다.
    hit_kind = _HIT_KIND.get((key, vuln_class))
    matching = frozenset(ref for ref, kind in zip(_REFS, kinds) if kind == hit_kind)
    resolved = frozenset(ref for ref, kind in zip(_REFS, kinds) if kind != "absent")
    return matching, resolved


def _ref_variants():
    variants = []
    for refs in _SUBSETS:
        for order in itertools.permutations(refs):
            variants.extend((order, order[:1] + order, order + order[-1:]))
    return tuple(dict.fromkeys(variants))


def _traffic_views(traffic):
    if traffic is None or len(traffic) < 2:
        return (traffic,)
    return traffic, dict(reversed(tuple(traffic.items())))


def _traffic_state(traffic):
    return None if traffic is None else tuple(traffic.items())


class TestEvidenceProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system", "builtins.open"):
            guard = patch(target, side_effect=AssertionError("offline synthetic evidence only"))
            guard.start()
            self.addCleanup(guard.stop)

    def assert_record_case(self, record, key, traffic, matching, resolved):
        before = copy.deepcopy(record), _traffic_state(traffic)
        selected = frozenset(record.traffic_evidence_refs)
        expected = bool(selected & matching)
        missing = () if traffic is None else tuple(sorted(selected - resolved))
        self.assertIs(evidence.has_evidence_refs(record), bool(selected))
        self.assertIs(evidence.record_has_evidence(record, key, traffic, _CORRELATE), expected)
        self.assertEqual(evidence.dangling_refs((record,), traffic), missing)
        for ref in _REFS:
            self.assertIs(evidence.ref_resolves(ref, traffic), ref in resolved)
            self.assertIs(evidence.ref_targets_challenge(ref, record, key, traffic, _CORRELATE), ref in matching)
        self.assertEqual((record, _traffic_state(traffic)), before)

    def assert_challenge_case(self, records, key, traffic, expected):
        before = copy.deepcopy(records), _traffic_state(traffic)
        for factory in (tuple, list, iter):
            supplied = factory(records)
            self.assertIs(evidence.challenge_has_evidence(supplied, key, traffic, _CORRELATE), expected)
            if isinstance(supplied, (list, tuple)):
                self.assertEqual(tuple(supplied), records)
        self.assertEqual((records, _traffic_state(traffic)), before)

    def test_state_table_matches_single_ref_and_record_decisions(self):
        for kinds, refs, vuln_class, key in itertools.product(_ASSIGNMENTS, _SUBSETS, ("idor", "xss"), _KEYS):
            with self.subTest(kinds=kinds, refs=refs, vuln_class=vuln_class, key=key):
                self.assert_record_case(_record(refs, vuln_class=vuln_class), key, _traffic(kinds),
                                        *_expected_sets(kinds, key, vuln_class))

    def test_ref_order_duplicates_and_traffic_order_do_not_change_results(self):
        states = (("absent", "other", "target"), ("target", "other", "unmapped"),
                  ("unmapped", "absent", "other"), ("absent", "absent", "absent"), None)
        for kinds, refs, vuln_class, key in itertools.product(states, _ref_variants(), ("idor", "xss"), _KEYS):
            sets = (frozenset(), frozenset()) if kinds is None else _expected_sets(kinds, key, vuln_class)
            for traffic in _traffic_views(None if kinds is None else _traffic(kinds)):
                with self.subTest(kinds=kinds, refs=refs, vuln_class=vuln_class, key=key,
                                  traffic_order=None if traffic is None else tuple(traffic)):
                    self.assert_record_case(_record(refs, vuln_class=vuln_class), key, traffic, *sets)

    def test_record_groups_use_any_witness_including_empty_and_repeated_groups(self):
        traffic = _traffic(("target", "other", "absent"))
        # 각 합성 record의 증거 대상을 미리 부여한다. 제품 판정으로 기대값을 만들지 않는다.
        cases = ((_record(), frozenset()), (_record((_REFS[2],)), frozenset()),
                 (_record((_REFS[1],)), frozenset()), (_record((_REFS[0],)), frozenset({_TARGET})),
                 (_record((_REFS[1],), vuln_class="xss"), frozenset({_OTHER})),
                 (_record((_REFS[0], _REFS[2])), frozenset({_TARGET})))
        for count in range(4):
            for indices in itertools.product(range(len(cases)), repeat=count):
                records = tuple(cases[index][0] for index in indices)
                witnesses = frozenset().union(*(cases[index][1] for index in indices))
                for key in _KEYS:
                    with self.subTest(indices=indices, key=key):
                        self.assert_challenge_case(records, key, traffic, key in witnesses)

    def test_dangling_refs_are_a_sorted_set_and_unknown_index_is_distinct_from_empty(self):
        for count in range(3):
            for chosen in itertools.product(_SUBSETS, repeat=count):
                records = tuple(_record(refs) for refs in chosen)
                selected = frozenset().union(*chosen)
                for present in (None, *_SUBSETS):
                    traffic = None if present is None else {ref: _RESOURCES["target"] for ref in present}
                    expected = () if present is None else tuple(sorted(selected - frozenset(present)))
                    with self.subTest(refs=chosen, present=present):
                        before = copy.deepcopy(records), _traffic_state(traffic)
                        for factory in (tuple, list, iter):
                            supplied = factory(records)
                            self.assertEqual(evidence.dangling_refs(supplied, traffic), expected)
                            if isinstance(supplied, (list, tuple)):
                                self.assertEqual(tuple(supplied), records)
                        self.assertEqual((records, _traffic_state(traffic)), before)

    def test_correlator_receives_only_resource_replacement_with_original_record_context(self):
        for vuln_class, axis, claimed, actual, key in itertools.product(
                ("idor", "xss", "sqli"), ("horizontal", "vertical"), tuple(_RESOURCES.values()),
                tuple(_RESOURCES.values()), _KEYS):
            with self.subTest(vuln_class=vuln_class, axis=axis, claimed=claimed, actual=actual, key=key):
                record = _record((_REFS[0],), vuln_class=vuln_class, resource_ref=claimed, axis=axis)
                traffic = {_REFS[0]: actual}
                before = asdict(record), _traffic_state(traffic)
                observed = []

                def correlate(projected):
                    observed.append(asdict(projected))
                    return key

                self.assertIs(evidence.ref_targets_challenge(_REFS[0], record, key, traffic, correlate), True)
                self.assertIs(evidence.record_has_evidence(record, key, traffic, correlate), True)
                self.assertTrue(observed)
                expected = asdict(record) | {"resource_ref": actual}
                for projected in observed:
                    self.assertEqual(projected, expected)
                self.assertEqual((asdict(record), _traffic_state(traffic)), before)

    def test_missing_index_or_ref_never_uses_claimed_resource_as_a_witness(self):
        def forbidden(_):
            raise AssertionError("a missing traffic target must not reach correlation")

        for refs, vuln_class, key, traffic in itertools.product(_SUBSETS, ("idor", "xss"), _KEYS, (None, {})):
            with self.subTest(refs=refs, vuln_class=vuln_class, key=key, traffic=traffic):
                record = _record(refs, vuln_class=vuln_class, resource_ref=_RESOURCES["target"])
                before = copy.deepcopy(record), _traffic_state(traffic)
                self.assertIs(evidence.record_has_evidence(record, key, traffic, forbidden), False)
                self.assertIs(evidence.challenge_has_evidence(iter((record,)), key, traffic, forbidden), False)
                for ref in _REFS:
                    self.assertIs(evidence.ref_targets_challenge(ref, record, key, traffic, forbidden), False)
                self.assertEqual((record, _traffic_state(traffic)), before)

    def test_assertions_detect_split_witness_all_aggregation_and_unknown_index_mutants(self):
        original_record = evidence.record_has_evidence
        traffic = _traffic(("target", "other", "absent"))
        split = _record((_REFS[1], _REFS[2]), resource_ref=_RESOURCES["target"])
        matching, resolved = _expected_sets(("target", "other", "absent"), _TARGET, "idor")
        records = (_record(), _record((_REFS[0],)))
        unindexed = _record((_REFS[0],))
        # 같은 기대값이 정상 구현에서 통과한 뒤에만 약화 변이의 실패를 인정한다.
        self.assert_record_case(split, _TARGET, traffic, matching, resolved)
        self.assert_challenge_case(records, _TARGET, traffic, True)
        self.assert_record_case(unindexed, _TARGET, None, frozenset(), frozenset())

        def split_witness(record, key, index, correlate):
            return any(evidence.ref_resolves(ref, index) for ref in record.traffic_evidence_refs) and correlate(record) == key

        with patch.object(evidence, "record_has_evidence", side_effect=split_witness):
            with self.assertRaises(AssertionError):
                self.assert_record_case(split, _TARGET, traffic, matching, resolved)

        def all_records(records, key, index, correlate):
            return all(original_record(record, key, index, correlate) for record in records)

        with patch.object(evidence, "challenge_has_evidence", side_effect=all_records):
            with self.assertRaises(AssertionError):
                self.assert_challenge_case(records, _TARGET, traffic, True)

        def unknown_index_passes(record, key, index, correlate):
            return bool(record.traffic_evidence_refs) if index is None else original_record(record, key, index, correlate)

        with patch.object(evidence, "record_has_evidence", side_effect=unknown_index_passes):
            with self.assertRaises(AssertionError):
                self.assert_record_case(unindexed, _TARGET, None, frozenset(), frozenset())


if __name__ == "__main__":
    unittest.main()
