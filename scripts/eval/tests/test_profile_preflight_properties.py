"""프로파일→지문→사전검사 경계의 유한한 생성형 회귀(조합 858개).

합성 선언만 사용한다. 원래 프로파일은 preflight 입력이 아니므로 변경 시 지문을
재생성해 연결한다. 제품 코드·기존 테스트는 수정하지 않고 공개 API와 합성 fixture를
재사용한다. 실환경/계정/모델/GT는 사용하지 않는다.
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
from dataclasses import asdict
from decimal import Decimal
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import preflight
import profile_fingerprint as fingerprint
from test_profile_fingerprint import api_llm, profile

_ARRAYS = (("fixture", "accounts"), ("fixture", "resources"),
           ("scope", "methods"), ("scope", "paths"), ("scope", "vuln_classes"))
_ORDERS = tuple(itertools.product((False, True), repeat=len(_ARRAYS)))
_SAMPLED_ORDERS = (_ORDERS[0], *(flags for flags in _ORDERS if sum(flags) == 1), _ORDERS[-1])
_NUMBERS = (("0", "1200"), ("0.0", "1200.0"), ("-0.000", "1.2e3"), ("0e5", "120000e-2"))
_DIGESTS = ("fixture_fingerprint", "environment_fingerprint", "scope_fingerprint")
_ENV_CHANGES = {
    # 모델·제공자·base URL·fallback은 하나의 llm 객체로 비교된다(평가 프로토콜 §6).
    "llm": api_llm() | {"model": "example-org/other-model", "provider": "other-provider"},
    "temperature": 0.25, "seed": 43, "target_image": "example/target:16.0.0",
    "worker_count": 2, "context_window_k": 64, "time_budget_seconds": 600, "request_budget": 101,
    "run_storage": {"engine_db_tmpfs": "1g", "engine_data_tmpfs": "512m", "tmpfs_min_free": "32m"},
    "run_drain": {"grace_seconds": 31, "settle_seconds": 2},
    "sampling": {"top_p": 0.8, "top_k": 20, "min_p": 0.0, "presence_penalty": 1.5, "repetition_penalty": 1.0},
}
_SCOPE_CHANGES = {"target_origin": "http://eval-meter:8081", "methods": ["GET"],
                  "paths": ["/api/records/{object}"], "vuln_classes": ["idor"]}


def _reverse_keys(value):
    if isinstance(value, dict):
        return {key: _reverse_keys(item) for key, item in reversed(tuple(value.items()))}
    if isinstance(value, list):
        return [_reverse_keys(item) for item in value]
    return value


def _wire(payload, flags, reverse_keys=False, numbers=None):
    changed = copy.deepcopy(payload)
    for (section, field), reverse in zip(_ARRAYS, flags):
        if reverse:
            changed[section][field].reverse()
    if reverse_keys:
        changed = _reverse_keys(changed)
    if numbers is None:
        return json.dumps(changed, separators=(",", ":"))
    markers = ("SYNTHETIC_TEMPERATURE_LITERAL", "SYNTHETIC_TIME_LITERAL")
    for field, marker in zip(("temperature", "time_budget_seconds"), markers):
        changed["environment"][field] = marker
    wire = json.dumps(changed, separators=(",", ":"))
    for marker, literal in zip(markers, numbers):
        quoted = json.dumps(marker)
        if wire.count(quoted) != 1:
            raise AssertionError("numeric fixture marker is ambiguous")
        wire = wire.replace(quoted, literal)
    return wire


def _digest(wire):
    # 프로파일 API는 Decimal을 지원하고 CLI도 이 정밀도로 읽는다. SHA 계산은 제품에 맡긴다.
    return fingerprint.generate_fingerprints(json.loads(wire, parse_float=Decimal))


def _controls(wire, digests):
    # preflight의 JSON 계약은 int/float다. 이 경계를 일부러 분리해 정밀도 손실 사례도 검사한다.
    payload = json.loads(wire)
    result = payload["environment"] | {"target_origin": payload["scope"]["target_origin"]}
    return result | {field: digests[field] for field in _DIGESTS}


def _pair(left_wire, right_wire, left_digests=None, right_digests=None, *, pinned_image=None):
    left = _controls(left_wire, asdict(_digest(left_wire)) if left_digests is None else left_digests)
    right = _controls(right_wire, asdict(_digest(right_wire)) if right_digests is None else right_digests)
    return {
        "pins": {"baseline_source_sha": "1" * 40, "target_image": left["target_image"] if pinned_image is None else pinned_image},
        "baseline": left | {"source_sha": "1" * 40},
        "ours": right | {"source_sha": "2" * 40},
    }


def _changes():
    for family, values in (("environment", _ENV_CHANGES), ("scope", _SCOPE_CHANGES)):
        for field, value in values.items():
            changed = profile()
            changed[family][field] = copy.deepcopy(value)
            yield family, field, changed
    for field in ("revision", "role", "count"):
        changed = profile()
        if field == "revision":
            changed["fixture"]["preparation_revision"] = 2
        elif field == "role":
            changed["fixture"]["accounts"][0]["role"] = "privileged"
        else:
            changed["fixture"]["resources"][0]["count"] = 3
        yield "fixture", field, changed


def _expected_errors(family, field, side):
    if family == "fixture":
        return {"comparison.fixture_fingerprint: MISMATCH"}
    if family == "scope":
        result = {"comparison.environment_fingerprint: MISMATCH", "comparison.scope_fingerprint: MISMATCH"}
        if field == "target_origin":
            result.add("comparison.target_origin: MISMATCH")
        return result
    direct = (f"{side}.target_image: PIN_MISMATCH" if field == "target_image"
              else f"comparison.{field}: MISMATCH")
    return {direct, "comparison.environment_fingerprint: MISMATCH"}


class TestProfilePreflightProperties(unittest.TestCase):
    def setUp(self):
        for target in ("socket.socket", "socket.create_connection", "subprocess.Popen", "os.system"):
            guard = patch(target, side_effect=AssertionError("offline synthetic declarations only"))
            guard.start()
            self.addCleanup(guard.stop)

    def assert_passes(self, payload):
        before = copy.deepcopy(payload)
        self.assertIsNone(preflight.check_preflight(payload))
        self.assertEqual(payload, before)

    def assert_rejected(self, payload, expected):
        before = copy.deepcopy(payload)
        with self.assertRaises(preflight.PreflightError) as caught:
            preflight.check_preflight(payload)
        self.assertEqual(set(caught.exception.errors), expected)
        self.assertEqual(len(caught.exception.errors), len(expected))
        self.assertEqual(payload, before)
        for value in ("example-model", "other-model", "other-provider", "llm-api.example", "eval-meter", "/api/records"):
            self.assertNotIn(value, str(caught.exception))

    def assert_one_sided_change(self, family, field, changed_wire, side):
        original = _wire(profile(), _ORDERS[0])
        before, after = _digest(original), _digest(changed_wire)
        changed_fields = ({"fixture_fingerprint"} if family == "fixture" else
                          {"environment_fingerprint", "scope_fingerprint"} if family == "scope" else
                          {"environment_fingerprint"})
        for name in _DIGESTS:
            if name in changed_fields:
                self.assertNotEqual(getattr(before, name), getattr(after, name), name)
            else:
                self.assertEqual(getattr(before, name), getattr(after, name), name)
        wires = (changed_wire, original) if side == "baseline" else (original, changed_wire)
        payload = _pair(*wires, pinned_image=profile()["environment"]["target_image"])
        self.assert_rejected(payload, _expected_errors(family, field, side))

    def test_equivalent_profile_variations_keep_digests_and_pass_preflight(self):
        original = _wire(profile(), _ORDERS[0])
        reference = _digest(original)
        cases = 0
        for flags, reverse, numbers in itertools.product(_ORDERS, (False, True), _NUMBERS):
            with self.subTest(flags=flags, keys=reverse, numbers=numbers):
                wire = _wire(profile(), flags, reverse, numbers)
                self.assertEqual(_digest(wire), reference)
                self.assert_passes(_pair(original, wire))
                cases += 1
        self.assertEqual(cases, 256)

    def test_one_sided_generated_changes_have_exact_symmetric_rejections(self):
        cases = 0
        for (family, field, changed), flags, reverse, side in itertools.product(
                tuple(_changes()), _SAMPLED_ORDERS, (False, True), ("baseline", "ours")):
            with self.subTest(family=family, field=field, flags=flags, keys=reverse, side=side):
                self.assert_one_sided_change(family, field, _wire(changed, flags, reverse), side)
                cases += 1
        self.assertEqual(cases, 504)  # 변경 18종(환경 11·scope 4·fixture 3) × 순서 7 × 키 순서 2 × 쪽 2

    def test_equal_changes_on_both_sides_remain_comparable(self):
        cases = 0
        for (family, field, changed), flags, reverse in itertools.product(
                tuple(_changes()), _SAMPLED_ORDERS, (False, True)):
            with self.subTest(family=family, field=field, flags=flags, keys=reverse):
                left = _wire(changed, _ORDERS[0])
                right = _wire(changed, flags, reverse)
                self.assertEqual(_digest(left), _digest(right))
                self.assert_passes(_pair(left, right))
                cases += 1
        self.assertEqual(cases, 252)

    def test_digest_preserves_decimal_changes_hidden_by_float_controls(self):
        literals = ("0.1234567890123456789012345678901", "0.1234567890123456789012345678902")
        cases = 0
        for flags, side in itertools.product(_SAMPLED_ORDERS, ("baseline", "ours")):
            with self.subTest(flags=flags, side=side):
                left = _wire(profile(), _ORDERS[0], numbers=(literals[0], "1200"))
                right = _wire(profile(), flags, True, numbers=(literals[1], "1.2e3"))
                wires = (right, left) if side == "baseline" else (left, right)
                payload = _pair(*wires)
                self.assertEqual(payload["baseline"]["temperature"], payload["ours"]["temperature"])
                self.assertNotEqual(_digest(left).environment_fingerprint, _digest(right).environment_fingerprint)
                self.assert_rejected(payload, {"comparison.environment_fingerprint: MISMATCH"})
                cases += 1
        self.assertEqual(cases, 14)

    def test_real_cli_roundtrip_accepts_equivalence_and_rejects_regenerated_scope_change(self):
        def invoke(main, argv):
            out, err = io.StringIO(), io.StringIO()
            with redirect_stdout(out), redirect_stderr(err):
                code = main(argv)
            return code, out.getvalue(), err.getvalue()

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            paths = [root / "left.json", root / "right.json"]
            config_path = root / "preflight.json"
            left = _wire(profile(), _ORDERS[0])
            changed = profile()
            changed["scope"]["paths"] = ["/api/other/{object}"]
            for unequal, right in ((False, _wire(profile(), _ORDERS[-1], True, _NUMBERS[-1])),
                                   (True, _wire(changed, _ORDERS[-1], True, _NUMBERS[2]))):
                with self.subTest(changed_scope=unequal):
                    digests = []
                    for path, wire in zip(paths, (left, right)):
                        path.write_text(wire, encoding="utf-8")
                        code, out, err = invoke(fingerprint.main, [str(path)])
                        self.assertEqual((code, err), (0, ""))
                        self.assertNotIn("eval-meter", out)
                        digests.append(json.loads(out))
                    payload = _pair(left, right, *digests)
                    config_path.write_text(json.dumps(payload), encoding="utf-8")
                    before = {path.name: path.read_bytes() for path in root.iterdir()}
                    code, out, err = invoke(preflight.main, [str(config_path)])
                    if unequal:
                        self.assertEqual((code, out), (2, ""))
                        self.assertEqual(set(err.splitlines()), _expected_errors("scope", "paths", "ours"))
                    else:
                        self.assertEqual((code, err), (0, ""))
                        self.assertIn("통과", out)
                    self.assertEqual({path.name: path.read_bytes() for path in root.iterdir()}, before)

    def test_property_assertions_detect_three_weakened_boundary_guards(self):
        changed = profile()
        changed["scope"]["paths"] = ["/api/other/{object}"]
        wire = _wire(changed, _ORDERS[-1], True)
        original_generate = fingerprint.generate_fingerprints
        original_check = preflight.check_preflight
        constant = _digest(_wire(profile(), _ORDERS[0]))

        def omit_scope(payload):
            copied = copy.deepcopy(payload)
            copied["scope"] = profile()["scope"]
            return original_generate(copied)

        def omit_digest_comparison(payload):
            copied = copy.deepcopy(payload)
            for name in _DIGESTS:
                copied["ours"][name] = copied["baseline"][name]
            return original_check(copied)

        mutants = ((fingerprint, "generate_fingerprints", lambda _: constant),
                   (fingerprint, "generate_fingerprints", omit_scope),
                   (preflight, "check_preflight", omit_digest_comparison))
        for owner, name, weakened in mutants:
            with self.subTest(guard=weakened.__name__), patch.object(owner, name, side_effect=weakened):
                with self.assertRaises(AssertionError):
                    self.assert_one_sided_change("scope", "paths", wire, "ours")


if __name__ == "__main__":
    unittest.main()
