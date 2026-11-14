"""공식 회차 준비 도구: 공식 프로파일→사전검사 선언(official_preflight.py), 세션 전 제공자 재조회 대조(provider_recheck.py)."""

from __future__ import annotations

import contextlib
import copy
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from official_preflight import ENVIRONMENT_FIELDS, build_preflight, main as preflight_main  # noqa: E402
from profile_fingerprint import ProfileError, generate_fingerprints  # noqa: E402
from provider_recheck import main as recheck_main, recheck  # noqa: E402

DOCS = Path(__file__).resolve().parents[3] / "docs/evaluation"
PROFILE = DOCS / "profiles/official-35b.json"
ENDPOINTS = DOCS / "profiles/evidence/openrouter-endpoints-qwen3.6-35b-a3b-20261114.json"
PARAMS = DOCS / "profiles/evidence/openrouter-endpoint-params-qwen3.6-35b-a3b-20261114.json"
BASE, OURS = "1" * 40, "2" * 40


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


class TempDir(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.dir = Path(temp.name)


class TestOfficialPreflight(TempDir):
    def test_declaration_is_the_committed_profile_with_its_fingerprints(self):
        profile = load(PROFILE)
        payload = build_preflight(PROFILE, BASE, OURS)
        fingerprints = generate_fingerprints(profile)
        for side, sha in (("baseline", BASE), ("ours", OURS)):
            declared = payload[side]
            self.assertEqual(declared["source_sha"], sha)
            for field in ENVIRONMENT_FIELDS:
                self.assertEqual(declared[field], profile["environment"][field], field)
            self.assertEqual(declared["target_origin"], profile["scope"]["target_origin"])
            self.assertEqual((declared["fixture_fingerprint"], declared["environment_fingerprint"],
                              declared["scope_fingerprint"]),
                             (fingerprints.fixture_fingerprint, fingerprints.environment_fingerprint,
                              fingerprints.scope_fingerprint))
        self.assertEqual(payload["pins"], {"baseline_source_sha": BASE, "target_image": profile["environment"]["target_image"]})

    def test_values_come_from_the_profile_not_from_the_tool(self):
        profile = load(PROFILE)
        profile["environment"]["request_budget"] = 555
        changed = self.dir / "official-x.json"
        changed.write_text(json.dumps(profile), encoding="utf-8")
        payload = build_preflight(changed, BASE, OURS)
        self.assertEqual((payload["baseline"]["request_budget"], payload["ours"]["request_budget"]), (555, 555))
        self.assertNotEqual(payload["ours"]["environment_fingerprint"], build_preflight(PROFILE, BASE, OURS)["ours"]["environment_fingerprint"])

    def test_source_shas_must_be_full_lowercase_commits(self):
        # 사전검사도 SHA 형식을 막지만, 이 도구가 먼저 어느 인자인지 알려야 한다(하류 가드에 가려지지 않게 문구까지 단언).
        for bad in ("1" * 39, "A" * 40, "e53a79f", ""):
            with self.subTest(bad=bad), self.assertRaisesRegex(ValueError, "^--baseline-sha: 40자리"):
                build_preflight(PROFILE, bad, OURS)
        with self.assertRaisesRegex(ValueError, "^--ours-sha: 40자리"):
            build_preflight(PROFILE, BASE, "z" * 40)

    def test_invalid_profile_and_failing_preflight_write_nothing(self):
        profile = load(PROFILE)
        profile["version"] = 4
        old = self.dir / "old.json"
        old.write_text(json.dumps(profile), encoding="utf-8")
        with self.assertRaises(ProfileError):
            build_preflight(old, BASE, OURS)
        out = self.dir / "preflight.json"
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(preflight_main([str(old), "--baseline-sha", BASE, "--ours-sha", OURS, "--out", str(out)]), 2)
        self.assertFalse(out.exists())

    def test_main_writes_once_and_never_overwrites(self):
        out = self.dir / "preflight.json"
        args = [str(PROFILE), "--baseline-sha", BASE, "--ours-sha", OURS, "--out", str(out)]
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(preflight_main(args), 0)
        first = out.read_text(encoding="utf-8")
        self.assertEqual(json.loads(first), build_preflight(PROFILE, BASE, OURS))
        with contextlib.redirect_stderr(io.StringIO()) as err:
            self.assertEqual(preflight_main(args[:-1] + [str(out)]), 2)
        self.assertIn("이미 있음", err.getvalue())
        self.assertEqual(out.read_text(encoding="utf-8"), first)


def required_parameters() -> list:
    """게이트웨이가 넣는 샘플링 값 + 엔진이 보내는 파라미터(근거 파일)."""
    evidence = load(PARAMS)
    return evidence["pinned_parameters"] + evidence["engine_parameters"]


def without(name: str) -> list:
    return [p for p in required_parameters() + ["tool_choice"] if p != name]


def endpoint(tag: str = "primeintellect/fp8", provider: str = "PrimeIntellect", quantization: str = "fp8",
             parameters: list | None = None) -> dict:
    params = required_parameters() + ["tool_choice"] if parameters is None else parameters
    return {"tag": tag, "provider_name": provider, "quantization": quantization, "supported_parameters": params}


def response(*endpoints: dict, model: str = "qwen/qwen3.6-35b-a3b") -> bytes:
    return json.dumps({"data": {"id": model, "endpoints": list(endpoints)}}).encode()


class TestProviderRecheck(TempDir):
    def differences(self, raw: bytes) -> list[str]:
        return recheck(raw, load(PROFILE), load(ENDPOINTS), load(PARAMS))

    def test_same_single_endpoint_passes(self):
        others = [endpoint("deepinfra/fp8", "DeepInfra"), endpoint("primeintellect", "PrimeIntellect", "bf16"),
                  endpoint("primeintellect/fp8x", "PrimeIntellect")]
        # "primeintellect"·"primeintellect/fp8x"는 "primeintellect/fp8"에 매칭되지 않는다(같거나 slug + "/"로 시작해야 함).
        self.assertEqual(self.differences(response(endpoint(), *others)), [])

    def test_each_change_since_pin_time_is_reported(self):
        cases = {
            "model": response(endpoint(), model="qwen/other"),
            "none": response(endpoint("deepinfra/fp8")),
            "two": response(endpoint(), endpoint("primeintellect/fp8/turbo")),
            "quantization": response(endpoint(quantization="fp4")),
            "provider": response(endpoint(provider="PrimeIntellect2")),
            "parameter": response(endpoint(parameters=without("top_k"))),
            "tools": response(endpoint(parameters=without("tools"))),
            "not-a-list": response(endpoint(parameters="top_k")),
        }
        expected = {"model": "응답 모델", "none": "0개", "two": "2개", "quantization": "quantization",
                    "provider": "provider_name", "parameter": "['top_k']", "tools": "['tools']",
                    "not-a-list": "지원 표시가 빠진"}
        for name, raw in cases.items():
            with self.subTest(name=name):
                found = self.differences(raw)
                self.assertTrue(any(expected[name] in d for d in found), found)

    def test_every_engine_parameter_is_required(self):
        for name in load(PARAMS)["engine_parameters"]:
            with self.subTest(name=name):
                found = self.differences(response(endpoint(parameters=without(name))))
                self.assertEqual(found, [f"지원 표시가 빠진 파라미터 ['{name}']"])

    def test_malformed_response_is_an_input_error(self):
        for raw in (b"[]", b'{"data": []}', b'{"data": {"id": "x"}}', b'{"data": {"endpoints": [1]}}'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                self.differences(raw)

    def test_evidence_must_point_at_the_profile_slug_once(self):
        endpoints, params = load(ENDPOINTS), load(PARAMS)
        for name, change in (("slug", lambda e, p: e.update(pinned_slug="other/fp8")),
                             ("params-slug", lambda e, p: p.update(pinned_slug="other/fp8")),
                             ("two-pinned", lambda e, p: e["matched_endpoints"].append(e["matched_endpoints"][0]))):
            with self.subTest(name=name):
                e, p = copy.deepcopy(endpoints), copy.deepcopy(params)
                change(e, p)
                with self.assertRaisesRegex(ValueError, "고정 근거 파일"):
                    recheck(response(endpoint()), load(PROFILE), e, p)

    def test_main_exit_codes(self):
        path = self.dir / "endpoints.json"
        # tools만 빠져도 "같음"(0)이 아니라 다름(1)이어야 한다 — 엔진이 매 요청에 tools를 보낸다.
        cases = ((response(endpoint()), 0), (response(endpoint(quantization="fp4")), 1),
                 (response(endpoint(parameters=without("tools"))), 1), (b"not json", 2))
        for raw, code in cases:
            with self.subTest(code=code):
                path.write_bytes(raw)
                with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(recheck_main([str(path)]), code)



if __name__ == "__main__":
    unittest.main()
