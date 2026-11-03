"""분류 vocabulary의 정확 매칭과 확장/축소 불변성 bounded fuzz 회귀.

실계정·네트워크·GT를 사용하지 않고, 입력 row와 vocabulary 선언으로부터 독립
기대값을 계산한다. 매핑은 원문 class/type 문자열에만 적용되며 서술 필드로
추론하거나 서로 다른 source를 합치지 않는 계약을 고정한다.
"""

from __future__ import annotations

import copy
import json
import os
import sys
import unittest
from dataclasses import asdict
from itertools import product
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import baseline_extract
from baseline_extract import BaselineExtractError, UPSTREAM_COMMIT, extract_baseline
from test_baseline_extract import finding, vocabulary


MARKER = "SYNTHETIC-PRIVATE-VOCABULARY-MARKER"


def row(source: int, vulnclass: str, asset_type: str, *, name: str | None = None) -> dict:
    value = finding()
    value["id"] = str(source)
    value["traffic_bindings"][0]["id"] = str(source + 1000)
    value["vulnclass"] = vulnclass
    value["assets"] = [{"id": str(source + 100), "type": asset_type, "label": f"fixture-{source}"}]
    if name is not None:
        value["name"] = name
        value["summary"] = name
    return value


def independent_projection(rows: list[dict], classes: dict[str, str], types: dict[str, str]):
    """제품 정규화기를 호출하지 않는 최소 참조 oracle."""
    expected = []
    for raw in rows:
        class_value = classes.get(raw["vulnclass"])
        type_value = types.get(raw["assets"][0]["type"]) if raw.get("assets") is not None else None
        unmapped = []
        if class_value is None:
            unmapped.append("vulnclass")
        if raw.get("assets") is not None and type_value is None:
            unmapped.append("assets.type")
        expected.append({
            "source_id": raw["id"],
            "finding_id": raw.get("finding_id"),
            "vuln_class": classes.get(raw["vulnclass"]),
            "severity": raw["severity"] if raw["severity"] in {"critical", "high", "medium", "low"} else None,
            "source_status": raw["status"],
            "asset_refs": ([{"id": raw["assets"][0]["id"], "type": type_value}]
                           if raw.get("assets") is not None else None),
            "traffic_binding_refs": [{"binding_id": raw["traffic_bindings"][0]["id"],
                                       "finding_id": raw["traffic_bindings"][0]["finding_id"],
                                       "role": raw["traffic_bindings"][0]["role"]}],
            "unmapped_fields": tuple(sorted(unmapped)),
        })
    return expected


class TestVocabularyExtensionProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "socket.getaddrinfo",
                       "subprocess.Popen", "os.system", "builtins.open"):
            guard = patch(target, side_effect=AssertionError("offline vocabulary fixture only"))
            guard.start()
            self.addCleanup(guard.stop)

    def extract(self, rows, vocab):
        before = copy.deepcopy((rows, vocab))
        try:
            return extract_baseline(rows, vocab, source_sha=UPSTREAM_COMMIT)
        finally:
            self.assertEqual((rows, vocab), before)

    def projection(self, result):
        return [{
            "source_id": item.source_id,
            "finding_id": item.finding_id,
            "vuln_class": item.vuln_class,
            "severity": item.severity,
            "source_status": item.source_status,
            "asset_refs": None if item.asset_refs is None else [asdict(ref) for ref in item.asset_refs],
            "traffic_binding_refs": None if item.traffic_binding_refs is None else [
                asdict(ref) for ref in item.traffic_binding_refs],
            "unmapped_fields": item.unmapped_fields,
        } for item in result.findings]

    def test_exact_key_matrix_maps_only_declared_class_and_asset_strings(self):
        keys = ("SQL Injection", "Broken Access Control", "ß class", "  padded key  ", "case-Key")
        asset_keys = ("endpoint", "Document", "资源", "asset type", "API-Key")
        classes = {key: f"class_{index}" for index, key in enumerate(keys)}
        types = {key: f"asset_{index}" for index, key in enumerate(asset_keys)}
        rows = [row(index + 1, class_key, asset_key)
                for index, (class_key, asset_key) in enumerate(product(keys, asset_keys))]
        vocab = {"vulnclasses": classes, "asset_types": types}
        result = self.extract(rows, vocab)
        self.assertEqual(self.projection(result), independent_projection(rows, classes, types))
        self.assertEqual([item.source_id for item in result.findings], [item["id"] for item in rows])

    def test_case_prefix_suffix_normalization_and_narrative_inference_are_rejected(self):
        rows = [row(1, "sql injection", "Endpoint", name="SQL Injection"),
                row(2, "prefix:SQL Injection", "endpoint-v2", name="endpoint"),
                row(3, "SQL Injection ", " endpoint", name="SQL Injection")]
        vocab = {"vulnclasses": {"SQL Injection": "sqli"}, "asset_types": {"endpoint": "endpoint"}}
        result = self.extract(rows, vocab)
        self.assertEqual([item.vuln_class for item in result.findings], [None, None, None])
        self.assertEqual([item.asset_refs[0].type for item in result.findings], [None, None, None])
        self.assertEqual([item.source_id for item in result.findings], ["1", "2", "3"])
        self.assertTrue(all("vulnclass" in item.unmapped_fields and "assets.type" in item.unmapped_fields
                            for item in result.findings))

    def test_mapping_removal_changes_only_exact_projected_values(self):
        rows = [row(1, "SQL Injection", "endpoint"), row(2, "Broken Access Control", "document"),
                row(3, "SQL Injection", "endpoint")]
        full = {"vulnclasses": {"SQL Injection": "sqli", "Broken Access Control": "bac"},
                "asset_types": {"endpoint": "endpoint", "document": "document"}}
        reduced = {"vulnclasses": {"SQL Injection": "sqli"}, "asset_types": {"endpoint": "endpoint"}}
        before = self.extract(rows, full)
        after = self.extract(rows, reduced)
        self.assertEqual([item.source_id for item in before.findings], ["1", "2", "3"])
        self.assertEqual([item.source_status for item in before.findings],
                         [item.source_status for item in after.findings])
        self.assertEqual([item.finding_id for item in before.findings],
                         [item.finding_id for item in after.findings])
        self.assertEqual([item.vuln_class for item in after.findings], ["sqli", None, "sqli"])
        self.assertEqual([item.asset_refs[0].type for item in after.findings], ["endpoint", None, "endpoint"])

    def test_mapping_subsets_row_permutations_and_mapping_order_match_full_oracle(self):
        class_keys = ("Class A", "Class B", "분류 C")
        asset_keys = ("type-a", "type-b", "자산-c")
        classes = {key: f"class_{index}" for index, key in enumerate(class_keys)}
        types = {key: f"asset_{index}" for index, key in enumerate(asset_keys)}
        rows = [row(index + 1, class_key, asset_key)
                for index, (class_key, asset_key) in enumerate(zip(class_keys, asset_keys))]
        for index, value in enumerate(rows):
            value["status"] = ("pending", "false_positive", "resolved")[index]
            value["severity"] = ("critical", "high", "low")[index]
            value["traffic_bindings"][0]["role"] = ("baseline", "proof", "verification")[index]
        for class_mask in range(1 << len(class_keys)):
            for asset_mask in range(1 << len(asset_keys)):
                selected_classes = {key: classes[key] for index, key in enumerate(class_keys)
                                    if class_mask & (1 << index)}
                selected_types = {key: types[key] for index, key in enumerate(asset_keys)
                                  if asset_mask & (1 << index)}
                for order in (tuple(range(3)), (2, 0, 1), (1, 2, 0)):
                    for reverse in (False, True):
                        with self.subTest(class_mask=class_mask, asset_mask=asset_mask, order=order, reverse=reverse):
                            ordered_rows = [copy.deepcopy(rows[index]) for index in order]
                            classes_order = list(selected_classes.items())
                            types_order = list(selected_types.items())
                            vocab = {"vulnclasses": dict(classes_order[::-1] if reverse else classes_order),
                                     "asset_types": dict(types_order[::-1] if reverse else types_order)}
                            actual = self.projection(self.extract(ordered_rows, vocab))
                            self.assertEqual(actual, independent_projection(ordered_rows, selected_classes, selected_types))

    def test_unreferenced_only_expansion_preserves_whole_result_and_referenced_addition_is_local(self):
        rows = [row(1, "Class A", "type-a"), row(2, "Unknown", "type-b"), row(3, "Class A", "Unknown")]
        base = {"vulnclasses": {"Class A": "class_a"}, "asset_types": {"type-a": "asset_a"}}
        unused = copy.deepcopy(base)
        unused["vulnclasses"].update({"Unused": "unused_class"})
        unused["asset_types"].update({"Unused type": "unused_asset"})
        self.assertEqual(self.projection(self.extract(rows, base)), self.projection(self.extract(rows, unused)))
        expanded = copy.deepcopy(unused)
        expanded["vulnclasses"]["Unknown"] = "class_unknown"
        expanded["asset_types"]["type-b"] = "asset_b"
        expected = independent_projection(rows, expanded["vulnclasses"], expanded["asset_types"])
        self.assertEqual(self.projection(self.extract(rows, expanded)), expected)
        self.assertEqual([item["source_id"] for item in expected], ["1", "2", "3"])

    def test_exact_match_mutation_matrix_uses_independent_unmapped_expectations(self):
        variants = ("SQL Injection", "sql injection", "SQL Injection ", "prefix SQL Injection",
                    " SQL Injection", "SQL  Injection", "SＱL Injection", "e\u0301", "é")
        vocab = {"vulnclasses": {"SQL Injection": "sqli", "é": "accent"},
                 "asset_types": {"endpoint": "endpoint", "e\u0301": "combining"}}
        for index, variant in enumerate(variants):
            asset = ("endpoint" if index % 2 == 0 else "e\u0301")
            value = row(index + 1, variant, asset, name="SQL Injection")
            with self.subTest(variant=variant, asset=asset):
                self.assertEqual(self.projection(self.extract([value], vocab)),
                                 independent_projection([value], vocab["vulnclasses"], vocab["asset_types"]))

    def test_key_and_label_length_boundaries_and_controls_are_generated_for_both_maps(self):
        valid_keys = ("k", " ", "k" * 127, "k" * 128, "유" * 128)
        for location in ("vulnclasses", "asset_types"):
            for key in valid_keys:
                vocab = {"vulnclasses": {"SQL Injection": "sqli"}, "asset_types": {"endpoint": "endpoint"}}
                vocab[location][key] = "label_x"
                with self.subTest(location=location, kind="valid", length=len(key)):
                    self.extract([row(1, "SQL Injection", "endpoint")], vocab)
            invalid_keys = ("", "k" * 129, *("bad" + chr(code) + "key" for code in (*range(32), 127)))
            for key in invalid_keys:
                vocab = {"vulnclasses": {"SQL Injection": "sqli"}, "asset_types": {"endpoint": "endpoint"}}
                vocab[location][key] = "label_x"
                with self.subTest(location=location, kind="invalid-key", value=repr(key)):
                    with self.assertRaises(BaselineExtractError):
                        self.extract([row(1, "SQL Injection", "endpoint")], vocab)
            for label in ("a", "z" * 63, "z" * 64, "z" * 65, "bad-label", "A", "1label", "label\n"):
                vocab = {"vulnclasses": {"SQL Injection": "sqli"}, "asset_types": {"endpoint": "endpoint"}}
                vocab[location]["generated"] = label
                with self.subTest(location=location, kind="label", value=repr(label)):
                    if label in {"a", "z" * 63, "z" * 64}:
                        self.extract([row(1, "SQL Injection", "endpoint")], vocab)
                    else:
                        with self.assertRaises(BaselineExtractError):
                            self.extract([row(1, "SQL Injection", "endpoint")], vocab)

    def test_aliases_sharing_a_canonical_label_do_not_merge_sources_or_assets(self):
        rows = [row(11, "SQL Injection", "endpoint"), row(12, "SQLi alias", "route"),
                row(13, "SQL Injection", "endpoint")]
        vocab = {"vulnclasses": {"SQL Injection": "sqli", "SQLi alias": "sqli"},
                 "asset_types": {"endpoint": "endpoint", "route": "endpoint"}}
        result = self.extract(rows, vocab)
        self.assertEqual([item.source_id for item in result.findings], ["11", "12", "13"])
        self.assertEqual([item.vuln_class for item in result.findings], ["sqli", "sqli", "sqli"])
        self.assertEqual([item.asset_refs[0].id for item in result.findings], ["111", "112", "113"])

    def test_mapping_key_boundaries_and_unicode_are_accepted_without_new_policy(self):
        keys = ("a", "x" * 128, "유니코드 분류", "key\u0301", "  leading and trailing  ")
        asset_keys = ("b", "y" * 128, "資産", "type\u0301", "  asset  ")
        classes = {key: f"label_{index}" for index, key in enumerate(keys)}
        types = {key: f"asset_{index}" for index, key in enumerate(asset_keys)}
        rows = [row(index + 1, key, asset_keys[index]) for index, key in enumerate(keys)]
        result = self.extract(rows, {"vulnclasses": classes, "asset_types": types})
        self.assertEqual(self.projection(result), independent_projection(rows, classes, types))

    def test_invalid_mapping_controls_or_labels_fail_without_echoing_marker(self):
        base = vocabulary()
        invalid = [
            {"vulnclasses": {"bad\nkey": "sqli"}, "asset_types": base["asset_types"]},
            {"vulnclasses": {"key": "Bad Label"}, "asset_types": base["asset_types"]},
            {"vulnclasses": {"key": "label"}, "asset_types": {"asset\x7f": "endpoint"}},
            {"vulnclasses": {MARKER: MARKER}, "asset_types": base["asset_types"]},
        ]
        for vocab in invalid:
            with self.subTest(vocab=repr(vocab)[:30]):
                payload = [row(1, "SQL Injection", "endpoint")]
                before = copy.deepcopy((payload, vocab))
                with self.assertRaises(BaselineExtractError) as caught:
                    self.extract(payload, vocab)
                self.assertIn("INVALID_MAPPING", str(caught.exception))
                self.assertNotIn(MARKER, str(caught.exception))
                self.assertEqual((payload, vocab), before)

    def test_input_and_vocabulary_are_unchanged_and_output_is_non_sensitive(self):
        rows = [row(1, "SQL Injection", "endpoint", name=MARKER), row(2, "unknown", "unknown")]
        vocab = vocabulary()
        before = copy.deepcopy((rows, vocab))
        with patch("builtins.open", side_effect=AssertionError("no files")):
            result = self.extract(rows, vocab)
        self.assertEqual((rows, vocab), before)
        text = json.dumps(asdict(result), ensure_ascii=True)
        self.assertNotIn(MARKER, text)

    def test_independent_oracle_detects_casefold_alias_dedupe_and_fallback_mutations(self):
        vocab = {"vulnclasses": {"SQL Injection": "sqli", "SQLi alias": "sqli", "Other": "bac"},
                 "asset_types": {"endpoint": "endpoint", "route": "endpoint", "document": "document"}}
        original = baseline_extract._classification

        def casefold_mutant(value, mapping, path):
            if isinstance(value, str):
                return {key.casefold(): mapped for key, mapped in mapping.items()}.get(value.casefold())
            return original(value, mapping, path)

        case_rows = [row(1, "sql injection", "Endpoint", name="SQL Injection")]
        expected = independent_projection(case_rows, vocab["vulnclasses"], vocab["asset_types"])
        self.assertEqual(self.projection(self.extract(case_rows, vocab)), expected)
        with patch.object(baseline_extract, "_classification", side_effect=casefold_mutant):
            mutated = self.projection(self.extract(case_rows, vocab))
            self.assertEqual(mutated[0]["vuln_class"], "sqli")
            self.assertEqual(mutated[0]["asset_refs"][0]["type"], "endpoint")
            with self.assertRaises(AssertionError):
                self.assertEqual(mutated, expected)

        alias_rows = [row(11, "SQL Injection", "endpoint"), row(12, "SQLi alias", "route"),
                      row(13, "Other", "document")]
        expected = independent_projection(alias_rows, vocab["vulnclasses"], vocab["asset_types"])
        self.assertEqual(self.projection(self.extract(alias_rows, vocab)), expected)
        original_extract = extract_baseline

        def dedupe_mutant(*args, **kwargs):
            result = original_extract(*args, **kwargs)
            seen, retained = set(), []
            for item in result.findings:
                if item.vuln_class not in seen:
                    retained.append(item)
                    seen.add(item.vuln_class)
            return baseline_extract.BaselineExtraction(result.declared_source_sha, result.source_shape,
                                                       result.pagination, tuple(retained))

        with patch.object(sys.modules[__name__], "extract_baseline", side_effect=dedupe_mutant):
            mutated = self.projection(self.extract(alias_rows, vocab))
            self.assertEqual([item["source_id"] for item in mutated], ["11", "13"])
            with self.assertRaises(AssertionError):
                self.assertEqual(mutated, expected)

        unknown_rows = [row(3, "Unknown", "Unknown")]
        expected = independent_projection(unknown_rows, vocab["vulnclasses"], vocab["asset_types"])
        self.assertEqual(self.projection(self.extract(unknown_rows, vocab)), expected)

        def fallback_mutant(value, mapping, path):
            if isinstance(value, str) and value not in mapping:
                return next(iter(mapping.values()), None)
            return original(value, mapping, path)

        with patch.object(baseline_extract, "_classification", side_effect=fallback_mutant):
            mutated = self.projection(self.extract(unknown_rows, vocab))
            self.assertEqual(mutated[0]["vuln_class"], "sqli")
            self.assertEqual(mutated[0]["asset_refs"][0]["type"], "endpoint")
            with self.assertRaises(AssertionError):
                self.assertEqual(mutated, expected)


if __name__ == "__main__":
    unittest.main()
