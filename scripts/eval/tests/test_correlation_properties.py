"""합성 상관표의 type·id·분류 조합 회귀. 제품 코드와 실제 GT 파일은 수정하지 않는다.

선언한 type 소유자와 허용 분류 집합을 기대값으로 사용하며 resource_ref 파서를
재구현하지 않는다. 로더에는 메모리 JSON만 제공한다. 실제 대상·스캔·러너 I/O 없음.
"""

from __future__ import annotations

import copy
import io
import itertools
import json
import os
import sys
import unittest
from collections.abc import Mapping
from types import MappingProxyType, SimpleNamespace
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import correlation
from test_correlation import _rules

_KEYS = tuple(rule["challenge"] for rule in _rules({}))
_ROOTS = tuple(prefix + str(number) for prefix, number in itertools.product(("syn", "Syn", "합성_"), (0, 1)))
_SUFFIXES = ("", ":item", ":item:detail", "::item")
_CLASSES = (None, "idor", "xss", "sqli", "unknown")
_CONSTRAINTS = (None, "idor", "xss", None, "sqli", "idor")
_ACCEPTED = {None: frozenset(_CLASSES), "idor": frozenset({"idor"}),
             "xss": frozenset({"xss"}), "sqli": frozenset({"sqli"})}
_IDS = ("1", "999", "opaque-01", "합성값")  # 콜론이 추가되면 ID 값 변경이 아니라 type 경계 변경이다.
_MISSING = object()


def _fixture():
    rules = _rules({})
    for rule, constraint in zip(rules, _CONSTRAINTS):
        rule["vuln_class"] = constraint
    owners = {}
    for family, root in enumerate(_ROOTS):
        for depth, suffix in enumerate(_SUFFIXES):
            kind = root + suffix
            owner = rules[(family + depth) % len(_CONSTRAINTS)]
            owner["types"].append(kind)
            owners[kind] = owner["challenge"], owner["vuln_class"]
    return {"version": 1, "rules": rules}, owners


_PAYLOAD, _OWNERS = _fixture()


def _expected(kind, vuln_class):
    owner = _OWNERS.get(kind)
    return owner[0] if owner is not None and vuln_class in _ACCEPTED[owner[1]] else None


def _orders(count):
    forward = tuple(range(count))
    return forward, forward[::-1], forward[1:] + forward[:1]


def _fields(record):
    return dict(record) if isinstance(record, Mapping) else vars(record)


class TestCorrelationProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system", "builtins.open"):
            guard = patch(target, side_effect=AssertionError("synthetic correlation only"))
            guard.start()
            self.addCleanup(guard.stop)

    def load(self, payload):
        raw = json.dumps(payload, ensure_ascii=False)
        with patch.object(correlation, "open", side_effect=lambda *args, **kwargs: io.StringIO(raw), create=True):
            return correlation.load_correlation("synthetic-only.json")

    def assert_match(self, table, resource_ref, vuln_class, expected):
        before_table = copy.deepcopy(table)
        for axis in ("horizontal", "vertical", None):
            fields = {"axis": axis, "title": "SYNTHETIC display label", "challenge": _KEYS[0]}
            if resource_ref is not _MISSING:
                fields["resource_ref"] = resource_ref
            if vuln_class is not _MISSING:
                fields["vuln_class"] = vuln_class
            factories = (dict, lambda values: SimpleNamespace(**values), MappingProxyType)
            for factory in factories:
                record = factory(copy.deepcopy(fields))
                before = copy.deepcopy(_fields(record))
                self.assertEqual(table.correlate(record), expected, (type(record).__name__, axis))
                self.assertEqual(_fields(record), before)
        self.assertEqual(table, before_table)

    def assert_load_collision(self, payload, shared):
        with self.assertRaises(correlation.CorrelationError) as caught:
            self.load(payload)
        self.assertIn("GT 상관 데이터 오류 1건:", str(caught.exception))
        self.assertIn("type " + repr(shared), str(caught.exception))
        self.assertIn("모호", str(caught.exception))

    def assert_runtime_ambiguity(self, table, resource_ref, vuln_class):
        with self.assertRaises(correlation.CorrelationError):
            table.correlate({"resource_ref": resource_ref, "vuln_class": vuln_class})

    def test_generated_types_ids_classes_and_record_shapes_follow_declared_owners(self):
        table = self.load(_PAYLOAD)
        for kind, identifier, vuln_class in itertools.product(_OWNERS, _IDS, _CLASSES):
            with self.subTest(kind=kind, identifier=identifier, vuln_class=vuln_class):
                self.assert_match(table, kind + ":" + identifier, vuln_class, _expected(kind, vuln_class))

    def test_rule_and_alias_orders_preserve_loaded_matches_and_operability(self):
        operative = frozenset(owner[0] for owner in _OWNERS.values())
        for order, reverse_aliases in itertools.product(_orders(len(_KEYS)), (False, True)):
            payload = copy.deepcopy(_PAYLOAD)
            payload["rules"] = [payload["rules"][index] for index in order]
            if reverse_aliases:
                for rule in payload["rules"]:
                    rule["types"].reverse()
            table = self.load(payload)
            self.assertEqual(table.operative, operative)
            self.assertEqual(frozenset(table.unmapped), frozenset(_KEYS) - operative)
            for kind, (_, required) in _OWNERS.items():
                for vuln_class in (required or "idor", "unknown"):
                    with self.subTest(order=order, aliases=reverse_aliases, kind=kind, vuln_class=vuln_class):
                        self.assert_match(table, kind + ":17", vuln_class, _expected(kind, vuln_class))

    def test_added_colons_select_the_new_exact_type_prefix(self):
        table = self.load(_PAYLOAD)
        for prefix, extension, vuln_class in itertools.product(_OWNERS, ("unregistered", "item", "item:detail", ""), _CLASSES):
            kind = prefix + ":" + extension
            with self.subTest(prefix=prefix, extension=extension, vuln_class=vuln_class):
                self.assert_match(table, kind + ":17", vuln_class, _expected(kind, vuln_class))

    def test_unregistered_or_missing_key_fields_do_not_fall_back_to_display_fields(self):
        table = self.load(_PAYLOAD)
        for registered, vuln_class in itertools.product(_OWNERS, _CLASSES):
            kind = "unregistered:" + registered
            self.assertNotIn(kind, _OWNERS)
            with self.subTest(unknown_type=kind, vuln_class=vuln_class):
                self.assert_match(table, kind + ":17", vuln_class, None)
        for resource_ref, vuln_class in itertools.product((None, True, 1, {}, [], "", ":17", "noColon", _MISSING), _CLASSES):
            with self.subTest(invalid_ref=resource_ref, vuln_class=vuln_class):
                self.assert_match(table, resource_ref, vuln_class, None)
        for kind in _OWNERS:
            with self.subTest(missing_class=kind):
                self.assert_match(table, kind + ":17", _MISSING, _expected(kind, None))

    def test_all_type_presence_masks_partition_operative_and_unmapped_challenges(self):
        for enabled in itertools.product((False, True), repeat=len(_KEYS)):
            rules = _rules({})
            for index, active in enumerate(enabled):
                rules[index]["types"] = [f"syntheticActive{index}", f"syntheticActive{index}:nested"] if active else []
            expected = frozenset(key for key, active in zip(_KEYS, enabled) if active)
            for reverse in (False, True):
                with self.subTest(enabled=enabled, reverse=reverse):
                    table = self.load({"rules": rules[::-1] if reverse else rules})
                    self.assertEqual(table.operative, expected)
                    self.assertEqual(frozenset(table.unmapped), frozenset(_KEYS) - expected)
                    self.assertFalse(table.operative & frozenset(table.unmapped))
                    self.assertEqual(table.operative | frozenset(table.unmapped), frozenset(_KEYS))
                    for index, active in enumerate(enabled):
                        record = {"resource_ref": f"syntheticActive{index}:17", "vuln_class": "idor"}
                        self.assertEqual(table.correlate(record), _KEYS[index] if active else None)

    def test_shared_types_are_rejected_for_every_challenge_pair_even_with_disjoint_classes(self):
        for (left, right), shared, constraints, order in itertools.product(
                itertools.combinations(range(len(_KEYS)), 2), tuple(_OWNERS)[:4],
                ((None, None), ("idor", "idor"), ("idor", "xss")), _orders(len(_KEYS))):
            rules = _rules({})
            for index, required in zip((left, right), constraints):
                rules[index].update(types=[shared], vuln_class=required)
            with self.subTest(pair=(left, right), shared=shared, constraints=constraints, order=order):
                self.assert_load_collision({"rules": [rules[index] for index in order]}, shared)

    def test_manually_constructed_tables_reject_multiple_hits_without_first_rule_bias(self):
        # 로더를 거치지 않은 공개 Table의 모호성도 검사한다. 기대값은 독립된 진리표다.
        cases = ((None, None, "idor", "ambiguous"), (None, "idor", "idor", "ambiguous"),
                 ("idor", "idor", "idor", "ambiguous"), (None, None, None, "ambiguous"),
                 ("idor", "xss", "idor", _KEYS[0]), ("idor", "xss", "xss", _KEYS[1]),
                 ("idor", "xss", "sqli", None), ("idor", "xss", None, None))
        for kind, (first, second, vuln_class, expected), reverse in itertools.product(tuple(_OWNERS)[:4], cases, (False, True)):
            rules = (correlation.Rule(_KEYS[0], "horizontal", first, (kind,)),
                     correlation.Rule(_KEYS[1], "vertical", second, (kind,)))
            table = correlation.CorrelationTable(rules[::-1] if reverse else rules)
            with self.subTest(kind=kind, constraints=(first, second), vuln_class=vuln_class, reverse=reverse):
                if expected == "ambiguous":
                    self.assert_runtime_ambiguity(table, kind + ":17", vuln_class)
                else:
                    self.assert_match(table, kind + ":17", vuln_class, expected)

    def test_assertions_detect_first_colon_ignored_class_and_first_hit_mutants(self):
        table = self.load(_PAYLOAD)
        kind = _ROOTS[0] + ":item"
        expected = _expected(kind, "idor")
        self.assertIsNotNone(expected)
        ambiguous = correlation.CorrelationTable((
            correlation.Rule(_KEYS[0], "horizontal", None, (kind,)),
            correlation.Rule(_KEYS[1], "vertical", None, (kind,)),
        ))
        # 정상 구현에 같은 oracle을 먼저 적용해 기대값 자체의 실패를 배제한다.
        self.assert_match(table, kind + ":17", "idor", expected)
        self.assert_match(table, kind + ":17", "unknown", None)
        self.assert_runtime_ambiguity(ambiguous, kind + ":17", "idor")

        def first_colon(resource_ref):
            return resource_ref.partition(":")[0] if ":" in resource_ref else None

        with patch.object(correlation, "_type_of", side_effect=first_colon):
            with self.assertRaises(AssertionError):
                self.assert_match(table, kind + ":17", "idor", expected)

        with patch.object(correlation, "_rule_matches", side_effect=lambda rule, rtype, value: rtype in rule.types):
            with self.assertRaises(AssertionError):
                self.assert_match(table, kind + ":17", "unknown", None)

        original = correlation.CorrelationTable.correlate

        def first_hit(instance, record):
            for rule in instance.rules:
                hit = original(correlation.CorrelationTable((rule,)), record)
                if hit is not None:
                    return hit
            return None

        with patch.object(correlation.CorrelationTable, "correlate", first_hit):
            with self.assertRaises(AssertionError):
                self.assert_runtime_ambiguity(ambiguous, kind + ":17", "idor")


if __name__ == "__main__":
    unittest.main()
