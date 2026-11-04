"""LLM 게이트웨이 검증 마커의 제공자 판정. 실제 게이트웨이·API 없이 합성 마커로 본다."""

from __future__ import annotations

import copy
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_llm import LLMMarkerError, judge_llm

SLUG, NAME = "example-provider", "Example"


def generation(gen_id, provider=NAME, attempts=None, status="verified"):
    if attempts is None:
        attempts = [{"provider_name": provider, "endpoint_id": "ep-1", "status": 200}]
    return {"id": gen_id, "status": status, "provider_name": provider if status == "verified" else "",
            "attempts": attempts if status == "verified" else []}


def marker(**overrides):
    base = {"role": "llm-gateway", "backend": "api", "provider": SLUG, "sealed_at": "a", "drained_at": "b",
            "verified_at": "c", "forwarded_requests": 3, "upstream_errors": 0, "rejected_requests": 0,
            "rejected_after_seal": 1, "cancelled_by_seal": 0, "responses_without_generation_id": 0,
            "generations": [generation("gen-1"), generation("gen-2")], "log_complete": True}
    return base | overrides


def judge(m, backend="api"):
    return judge_llm(m, backend, SLUG if backend == "api" else "", NAME)


class TestJudgeApi(unittest.TestCase):
    def test_pinned_and_verified_passes_with_summary(self):
        reasons, summary = judge(marker())
        self.assertEqual(reasons, [])
        self.assertEqual((summary["generations"], summary["unverified"], summary["providers"], summary["endpoint_ids"]),
                         (2, 0, [NAME], ["ep-1"]))
        self.assertEqual((summary["pinned_provider"], summary["expected_provider_name"], summary["rejected_after_seal"]),
                         (SLUG, NAME, 1))

    def test_each_failure_has_its_own_reason(self):
        cases = [
            (marker(generations=[generation("g", provider="Other")]), "llm_provider_mismatch"),
            (marker(generations=[generation("g", attempts=[{"provider_name": "Other", "endpoint_id": "e", "status": 500},
                                                           {"provider_name": NAME, "endpoint_id": "e1", "status": 200}])]),
             "llm_fallback_attempted"),
            (marker(generations=[generation("g", attempts=[])]), "llm_fallback_unverifiable"),
            (marker(generations=[generation("g", status="unverified")]), "llm_generation_unverified"),
            (marker(responses_without_generation_id=1), "llm_generation_unverifiable"),
            (marker(provider="other-provider"), "llm_pin_mismatch"),
            (marker(log_complete=False), "llm_log_incomplete"),
            (marker(backend="local"), "llm_backend_mismatch"),
        ]
        for m, reason in cases:
            with self.subTest(reason=reason):
                self.assertEqual(judge(m)[0], [reason])

    def test_mismatch_and_fallback_are_counted_separately(self):
        # 처음부터 다른 제공자로 간 것은 불일치일 뿐 fallback이 아니다.
        self.assertEqual(judge(marker(generations=[generation("g", provider="Other")]))[0], ["llm_provider_mismatch"])
        # 기대 제공자에서 실패하고 다른 제공자가 응답하면 둘 다다.
        both = [{"provider_name": NAME, "endpoint_id": "e1", "status": 500},
                {"provider_name": "Other", "endpoint_id": "e2", "status": 200}]
        self.assertEqual(judge(marker(generations=[generation("g", provider="Other", attempts=both)]))[0],
                         ["llm_fallback_attempted", "llm_provider_mismatch"])

    def test_same_provider_retry_is_not_fallback(self):
        retry = [{"provider_name": NAME, "endpoint_id": "e1", "status": 503},
                 {"provider_name": NAME, "endpoint_id": "e1", "status": 200}]
        self.assertEqual(judge(marker(generations=[generation("g", attempts=retry)]))[0], [])

    def test_reasons_accumulate_sorted(self):
        m = marker(log_complete=False, generations=[generation("g", provider="Other"), generation("h", status="unverified")])
        self.assertEqual(judge(m)[0], ["llm_generation_unverified", "llm_log_incomplete", "llm_provider_mismatch"])

    def test_counts_in_summary(self):
        m = marker(generations=[generation("a"), generation("b", provider="Other"), generation("c", status="unverified"),
                                generation("d", attempts=[])])
        summary = judge(m)[1]
        self.assertEqual((summary["unverified"], summary["provider_mismatch"], summary["fallback_unverifiable"]),
                         (1, 1, 1))
        self.assertEqual(summary["providers"], [NAME, "Other"])


class TestJudgeLocal(unittest.TestCase):
    def test_local_records_counts_only(self):
        reasons, summary = judge(marker(backend="local", provider="", generations=[]), backend="local")
        self.assertEqual(reasons, [])
        self.assertEqual((summary["generations"], summary["forwarded_requests"]), (0, 3))

    def test_local_with_lookups_or_pin_is_mixed_configuration(self):
        for m in (marker(backend="local", provider=""), marker(backend="local", generations=[])):
            with self.subTest(m=m["provider"]):
                self.assertEqual(judge(m, backend="local")[0], ["llm_backend_mismatch"])


class TestMarkerShape(unittest.TestCase):
    def test_contract_shape_enforced(self):
        bad = []
        m = marker()
        m["extra"] = 1
        bad.append(m)
        m = marker()
        del m["upstream_errors"]
        bad.append(m)
        bad.append(marker(log_complete="true"))
        bad.append(marker(forwarded_requests=-1))
        bad.append(marker(cancelled_by_seal=True))
        bad.append(marker(generations={}))
        bad.append(marker(generations=[generation("g") | {"x": 1}]))
        bad.append(marker(generations=[generation("g") | {"status": "maybe"}]))
        bad.append(marker(generations=[generation("g", attempts=[{"provider_name": NAME, "endpoint_id": "e"}])]))
        bad.append(marker(generations=[generation("g", attempts=[{"provider_name": NAME, "endpoint_id": "e",
                                                                  "status": "200"}])]))
        bad.append([])
        for m in bad:
            with self.subTest(m=str(m)[:60]), self.assertRaises(LLMMarkerError):
                judge(copy.deepcopy(m))


if __name__ == "__main__":
    unittest.main()
