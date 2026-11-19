"""LLM 게이트웨이 검증 마커의 제공자 판정. 실제 게이트웨이·API 없이 합성 마커로 본다."""

from __future__ import annotations

import copy
import os
import sys
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_llm import PRODUCT_VERIFY_ROLE, LLMMarkerError, interrupted_generation_ids, judge_llm, judge_product_llm

SLUG, NAME = "example-provider", "Example"
# 사전검사 픽스처(test_preflight.configuration)의 temperature·sampling·seed로 만든 기대 적용값.
SAMPLING = {"temperature": 0, "top_p": 0.95, "top_k": 20, "min_p": 0.0, "presence_penalty": 1.5,
            "repetition_penalty": 1.0, "seed": 42}


def generation(gen_id, provider=NAME, attempts=None, status="verified", last=None, tries=1):
    if attempts is None:
        attempts = [{"provider_name": provider, "endpoint_id": "ep-1", "status": 200}]
    if last is None:
        last = "verified" if status == "verified" else "http_404"
    return {"id": gen_id, "status": status, "provider_name": provider if status == "verified" else "",
            "attempts": attempts if status == "verified" else [], "lookup": {"tries": tries, "last": last}}


NO_ENDS = {"client_closed_after_done": 0, "client_closed_before_done": 0, "upstream_cut_after_done": 0, "upstream_cut_before_done": 0}


def marker(**overrides):
    base = {"role": "llm-gateway", "backend": "api", "provider": SLUG, "sampling": dict(SAMPLING),
            "sealed_at": "a", "drained_at": "b",
            "verified_at": "c", "forwarded_requests": 3, "upstream_errors": 0, "upstream_error_ends": dict(NO_ENDS), "rejected_requests": 0,
            "rejected_after_seal": 1, "cancelled_by_seal": 0, "responses_without_generation_id": 0,
            "generations": [generation("gen-1"), generation("gen-2")], "log_complete": True,
            "rejected_sampling": 0, "rejected_provider": 0, "rate_limited": 0}
    return base | overrides


LOCAL_SAMPLING = {"temperature": 1.0, "top_p": 0.95, "presence_penalty": 0.5, "frequency_penalty": 0.25, "seed": 42}


def judge(m, backend="api"):
    return judge_llm(m, backend, SLUG if backend == "api" else "", NAME, SAMPLING if backend == "api" else LOCAL_SAMPLING)


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


class TestJudgeSampling(unittest.TestCase):
    def test_applied_sampling_must_equal_the_declaration(self):
        # 선언이 아니라 게이트웨이가 실제로 넣은 값을 대조한다. 표기(0과 0.0)는 같은 값으로 본다.
        self.assertEqual(judge(marker(sampling=dict(SAMPLING, temperature=0.0, top_k=20.0)))[0], [])
        reasons, summary = judge(marker())
        self.assertEqual((reasons, summary["sampling_applied"]), ([], SAMPLING))
        for name in SAMPLING:
            with self.subTest(field=name):
                changed = dict(SAMPLING, **{name: SAMPLING[name] + 1})
                self.assertEqual(judge(marker(sampling=changed))[0], ["llm_sampling_mismatch"])
        self.assertEqual(judge(marker(sampling=None))[0], ["llm_sampling_mismatch"])
        # 묶음이 백엔드와 다르면 구성이 섞인 것이다.
        self.assertEqual(judge(marker(backend="local", provider="", generations=[]), backend="local")[0],
                         ["llm_sampling_mismatch"])
        self.assertEqual(judge(marker(sampling=dict(LOCAL_SAMPLING)))[0], ["llm_sampling_mismatch"])

    def test_large_integer_seed_is_compared_without_float_rounding(self):
        # 2^53+1과 2^53은 float로 바꾸면 같아진다. 다른 seed가 적용됐으면 고정이 아니다.
        declared = dict(SAMPLING, seed=2 ** 53 + 1)
        same = judge_llm(marker(sampling=dict(declared)), "api", SLUG, NAME, declared)[0]
        rounded = judge_llm(marker(sampling=dict(declared, seed=2 ** 53)), "api", SLUG, NAME, declared)[0]
        self.assertEqual((same, rounded), ([], ["llm_sampling_mismatch"]))

    def test_sampling_shape_is_checked(self):
        for bad in ({}, dict(SAMPLING, extra=1), {k: v for k, v in SAMPLING.items() if k != "seed"},
                    dict(SAMPLING, seed="42"), dict(SAMPLING, top_p=True), [1]):
            with self.subTest(bad=bad):
                with self.assertRaises(LLMMarkerError):
                    judge(marker(sampling=bad))


class TestJudgeLocal(unittest.TestCase):
    def test_local_records_counts_only(self):
        reasons, summary = judge(marker(backend="local", provider="", generations=[], sampling=dict(LOCAL_SAMPLING)),
                                 backend="local")
        self.assertEqual(reasons, [])
        self.assertEqual((summary["generations"], summary["forwarded_requests"], summary["sampling_applied"]),
                         (0, 3, LOCAL_SAMPLING))

    def test_sampling_set_of_the_other_backend_is_a_mismatch_even_with_equal_shared_values(self):
        # 겹치는 키의 값이 같아도 묶음이 다르면 고정이 아니다.
        shared = {key: LOCAL_SAMPLING[key] for key in LOCAL_SAMPLING if key != "frequency_penalty"}
        api_set = dict(shared, top_k=20, min_p=0.0, repetition_penalty=1.0)
        local = {"backend": "local", "provider": "", "generations": []}
        self.assertEqual(judge(marker(**local, sampling=api_set), backend="local")[0], ["llm_sampling_mismatch"])
        reasons, _ = judge_llm(marker(sampling=dict(LOCAL_SAMPLING)), "api", SLUG, NAME, api_set)
        self.assertEqual(reasons, ["llm_sampling_mismatch"])

    def test_local_sampling_must_be_the_declared_values(self):
        local = {"backend": "local", "provider": "", "generations": []}
        self.assertEqual(judge(marker(**local, sampling=None), backend="local")[0], ["llm_sampling_mismatch"])
        for name in LOCAL_SAMPLING:
            with self.subTest(name):
                changed = dict(LOCAL_SAMPLING, **{name: LOCAL_SAMPLING[name] + 1})
                self.assertEqual(judge(marker(**local, sampling=changed), backend="local")[0], ["llm_sampling_mismatch"])

    def test_local_with_lookups_or_pin_is_mixed_configuration(self):
        for m in (marker(backend="local", provider="", sampling=dict(LOCAL_SAMPLING)),
                  marker(backend="local", generations=[], sampling=dict(LOCAL_SAMPLING))):
            with self.subTest(m=m["provider"]):
                self.assertEqual(judge(m, backend="local")[0], ["llm_backend_mismatch"])


class TestMarkerShape(unittest.TestCase):
    def test_unfinished_stream_ends_are_reported_and_do_not_decide_validity(self):
        ends = NO_ENDS | {"client_closed_after_done": 2, "upstream_cut_before_done": 1}
        reasons, summary = judge_llm(marker(upstream_errors=3, upstream_error_ends=ends), "api", SLUG, NAME, SAMPLING)
        self.assertEqual(reasons, [])
        self.assertEqual((summary["upstream_errors"], summary["upstream_error_ends"]), (3, ends))
        self.assertEqual(list(summary["upstream_error_ends"]), sorted(ends))

    def test_contract_shape_enforced(self):
        bad = []
        m = marker()
        m["extra"] = 1
        bad.append(m)
        m = marker()
        del m["upstream_errors"]
        bad.append(m)
        m = marker()
        del m["upstream_error_ends"]
        bad.append(m)
        bad.append(marker(upstream_error_ends=None))
        bad.append(marker(upstream_error_ends={}))
        bad.append(marker(upstream_error_ends=NO_ENDS | {"other": 0}))
        bad.append(marker(upstream_error_ends=NO_ENDS | {"upstream_cut_after_done": -1}))
        bad.append(marker(upstream_error_ends=NO_ENDS | {"upstream_cut_after_done": True}))
        # 합이 upstream_errors와 달라도 구조 위반이다.
        bad.append(marker(upstream_errors=1))
        bad.append(marker(upstream_error_ends=NO_ENDS | {"client_closed_after_done": 1}))
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
        # generation 조회 흔적: 칸 두 개, 시도 1 이상 정수, 닫힌 응답 종류, 확인 상태와 마지막 응답이 맞아야 한다
        no_lookup = generation("g")
        del no_lookup["lookup"]
        bad.append(marker(generations=[no_lookup]))
        for trace in (None, {}, {"tries": 1}, {"tries": 1, "last": "verified", "x": 0}, {"tries": 0, "last": "verified"},
                      {"tries": True, "last": "verified"}, {"tries": "1", "last": "verified"}, {"tries": 1, "last": 404},
                      {"tries": 1, "last": "http_99"}, {"tries": 1, "last": "http_600"}, {"tries": 1, "last": "http_4044"},
                      {"tries": 1, "last": "timeout"}, {"tries": 0, "last": "http_404"}, {"tries": 2, "last": "deadline"},
                      {"tries": 101, "last": "http_404"}):
            # 미확인 generation에 붙여서, 확인 상태와 마지막 응답이 안 맞는다는 이유가 아니라 흔적 자체로 거부되는지 본다
            bad.append(marker(generations=[generation("g", status="unverified") | {"lookup": trace}]))
        bad.append(marker(generations=[generation("g", last="http_404")]))  # 확인됐는데 마지막 응답이 확인이 아니다
        bad.append(marker(generations=[generation("g", tries=101)]))  # 계측기가 발행할 수 없는 시도 수(상한 100)
        bad.append(marker(generations=[generation("g", status="unverified", last="verified")]))
        for m in bad:
            with self.subTest(m=str(m)[:60]), self.assertRaises(LLMMarkerError):
                judge(copy.deepcopy(m))

    def test_unverified_generations_are_counted_by_last_lookup_answer(self):
        # 미확인이 통계 지연(http_404 등)인지 즉시 거부(http_401)인지 요약에 응답 종류별 수로 남는다(값·본문 없음)
        m = marker(generations=[generation("a", tries=3), generation("b", status="unverified", last="http_401"),
                                generation("c", status="unverified", last="http_401", tries=2),
                                generation("d", status="unverified", last="no_provider", tries=60),
                                generation("e", status="unverified", last="network_error", tries=4)])
        reasons, summary = judge(m)
        self.assertEqual(reasons, ["llm_generation_unverified"])
        self.assertEqual((summary["unverified"], summary["unverified_last"], summary["lookup_tries_max"]),
                         (4, {"http_401": 2, "network_error": 1, "no_provider": 1}, 60))
        self.assertEqual((judge(marker())[1]["unverified_last"], judge(marker(generations=[]))[1]["lookup_tries_max"]), ({}, 0))
        # 시도 수는 계측기 발행 상한(100)까지 받는다
        top = marker(generations=[generation("a", tries=100), generation("b", status="unverified", last="http_404", tries=100)])
        self.assertEqual(judge(top)[1]["lookup_tries_max"], 100)

    def test_lookup_tries_limit_matches_the_meter_and_runner_config(self):
        import re as regex

        from runner_config import MAX_VERIFY_ATTEMPTS
        from runner_llm import MAX_LOOKUP_TRIES
        text = (Path(__file__).resolve().parents[3] / "cmd/eval-meter/main.go").read_text(encoding="utf-8")
        match = regex.search(r"maxVerifyAttempts = (\d+)", text)
        self.assertIsNotNone(match)
        self.assertEqual((int(match.group(1)), MAX_VERIFY_ATTEMPTS), (MAX_LOOKUP_TRIES, MAX_LOOKUP_TRIES))



class TestGatewayBodyKeyRejections(unittest.TestCase):
    """게이트웨이 샘플링 칸 거부 0건은 회차 유효 조건이다(4-4). provider 칸 거부·429는 기록만 한다."""

    def test_sampling_rejection_makes_the_round_unpinned(self):
        reasons, summary = judge(marker(rejected_requests=1, rejected_sampling=1))
        self.assertEqual(reasons, ["llm_sampling_rejected"])
        self.assertEqual(summary["rejected_sampling"], 1)

    def test_provider_rejection_and_rate_limits_are_recorded_only(self):
        reasons, summary = judge(marker(rejected_requests=2, rejected_provider=2, rate_limited=5))
        self.assertEqual(reasons, [])
        self.assertEqual((summary["rejected_provider"], summary["rate_limited"], summary["rejected_sampling"]), (2, 5, 0))

    def test_gateway_marker_must_count_body_key_rejections(self):
        for change in ({"rejected_sampling": None}, {"rejected_provider": None}, {"rejected_sampling": -1},
                       # 불리언·소수는 합 검사를 통과하는 값으로 둔다(타입 검사만 걸리게).
                       {"rejected_requests": 1, "rejected_sampling": True}, {"rejected_requests": 1, "rejected_provider": 1.0}, {"rate_limited": None},
                       {"rate_limited": -1}, {"rejected_requests": 1, "rejected_sampling": 1, "rejected_provider": 1}):
            with self.subTest(change=change), self.assertRaises(LLMMarkerError):
                judge(marker(**change))
        broken = marker()
        broken.pop("rate_limited")
        with self.assertRaises(LLMMarkerError):
            judge(broken)
        # 합이 전체 거부 수와 같은 것은 받는다(경계).
        self.assertEqual(judge(marker(rejected_requests=2, rejected_sampling=1, rejected_provider=1))[0],
                         ["llm_sampling_rejected"])


class TestJudgeProduct(unittest.TestCase):
    """O(제품 OpenRouter 경로) 호출 기록 사후 검증 표식은 B 게이트웨이 표식과 같은 규칙으로 판정하고, 샘플링은 B 적용값과 대조한다."""

    def product(self, **overrides):
        # 제품 기록 표식은 샘플링·provider 칸 거부가 null이다(게이트웨이에만 있는 판정, eval-meter genverify.go).
        return marker(**({"role": PRODUCT_VERIFY_ROLE, "rejected_after_seal": 0, "rejected_sampling": None,
                          "rejected_provider": None} | overrides))

    def test_product_marker_has_no_body_key_rejections(self):
        # 제품 기록 표식에 0을 적으면(게이트웨이 표식을 흉내 내거나 칸을 잘못 채움) 모양 오류다. 전제 확인은 제품 쪽 기록 몫이다.
        for name in ("rejected_sampling", "rejected_provider"):
            with self.subTest(field=name), self.assertRaises(LLMMarkerError):
                judge_product_llm(self.product(**{name: 0}), dict(SAMPLING), SLUG, NAME)
        reasons, summary = judge_product_llm(self.product(rate_limited=3), dict(SAMPLING), SLUG, NAME)
        self.assertEqual(reasons, [])
        self.assertEqual((summary["rate_limited"], summary["rejected_sampling"], summary["rejected_provider"]), (3, None, None))

    def test_same_sampling_as_baseline_gateway_passes(self):
        reasons, summary = judge_product_llm(self.product(), dict(SAMPLING), SLUG, NAME)
        self.assertEqual(reasons, [])
        self.assertEqual(summary["sampling_applied"], SAMPLING)

    def test_sampling_is_compared_with_the_baseline_not_a_declaration(self):
        # O가 보낸 값이 B 게이트웨이 적용값과 다르면(한 칸이라도) 불일치다. v1·v2 승인(null)도 불일치다.
        other = dict(SAMPLING, seed=7)
        self.assertIn("llm_sampling_mismatch", judge_product_llm(self.product(sampling=other), dict(SAMPLING), SLUG, NAME)[0])
        self.assertIn("llm_sampling_mismatch", judge_product_llm(self.product(sampling=None), dict(SAMPLING), SLUG, NAME)[0])
        self.assertEqual(judge_product_llm(self.product(sampling=other), other, SLUG, NAME)[0], [])

    def test_same_provider_rules_as_gateway(self):
        cut = dict(NO_ENDS, upstream_cut_before_done=1)
        reasons, summary = judge_product_llm(self.product(generations=[generation("gen-1", provider="Other"), generation("gen-2", status="unverified")],
                                                          responses_without_generation_id=1, upstream_errors=1, upstream_error_ends=cut),
                                             dict(SAMPLING), SLUG, NAME)
        self.assertEqual(reasons, sorted({"llm_provider_mismatch", "llm_generation_unverified", "llm_generation_unverifiable"}))
        self.assertEqual(summary["upstream_error_ends"]["upstream_cut_before_done"], 1)

    def test_role_and_baseline_shape(self):
        for bad in (marker(), self.product(role="llm-gateway"), None):
            with self.subTest(role=None if bad is None else bad["role"]), self.assertRaises(LLMMarkerError):
                judge_product_llm(bad, dict(SAMPLING), SLUG, NAME)
        for baseline in (None, {"temperature": 1.0}, dict(SAMPLING, seed="42"), dict(SAMPLING, frequency_penalty=0)):
            with self.subTest(baseline=baseline), self.assertRaises(LLMMarkerError):
                judge_product_llm(self.product(), baseline, SLUG, NAME)


def call(gid, decision, started, completed, end=None):
    row = {"seq": 1, "decision": decision, "method": "POST", "status": 200, "started_at": started, "completed_at": completed,
           "generation_id": gid}
    if end is not None:
        row["unfinished_end"] = end
    return row


STOP = "2026-11-19T00:00:10Z"


class TestInterruptedCalls(unittest.TestCase):
    """끊긴 호출(평가 프로토콜 §6, 2026-11-19): 기록으로 증명된 것만. 세 조건이 모두 기록에 있어야 한다."""

    def test_b_counts_only_gateway_seal_cancellations_spanning_the_seal(self):
        def sealed(gid, started, completed, end="sealed_before_done"):
            row = call(gid, "cancelled_by_seal", started, completed)
            if end is not None:
                row["unfinished_end"] = end
            return row
        rows = [sealed("cut", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z"),
                # 종료 이벤트 줄을 읽은 뒤 봉인(엔진이 출력을 썼을 수 있음, #735 리뷰)·끝 모양 칸이 없는 이전 행은 넣지 않는다
                sealed("after-done", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z", "sealed_after_done"),
                sealed("old-row", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z", None),
                sealed("late", "2026-11-19T00:00:10.5Z", "2026-11-19T00:00:11Z"),  # 정지 신호 뒤 시작
                call("done", "forwarded", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z"),  # 응답 완료 기록
                call("client", "upstream_error", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z", "client_closed_before_done"),
                sealed("", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z"),  # ID 없음
                sealed("bad", "yesterday", "2026-11-19T00:00:11Z")]  # 시각을 못 읽음
        self.assertEqual(interrupted_generation_ids(rows, STOP, product=False), frozenset({"cut"}))
        self.assertEqual(interrupted_generation_ids(rows, "not-a-time", product=False), frozenset())

    def test_an_id_shared_with_a_non_exempt_call_is_not_exempt(self):
        # 게이트웨이 표식은 같은 ID를 한 번만 싣는다. 같은 ID가 정상 완료 호출에도 있으면 미확인을 예외로 빼지 않는다(#735 리뷰).
        cut = call("same", "cancelled_by_seal", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z")
        cut["unfinished_end"] = "sealed_before_done"
        for other in (call("same", "forwarded", "2026-11-19T00:00:01Z", "2026-11-19T00:00:02Z"),
                      call("same", "upstream_error", "2026-11-19T00:00:01Z", "2026-11-19T00:00:02Z", "client_closed_before_done"),
                      call("same", "forwarded", "bad-time", "2026-11-19T00:00:02Z")):  # 시각을 못 읽는 행도 예외가 아니다
            with self.subTest(other=other["decision"]):
                for rows in ([cut, other], [other, cut]):
                    self.assertEqual(interrupted_generation_ids(rows, STOP, product=False), frozenset())
        # 판정까지: 같은 ID의 http_404 미확인은 무효 사유로 남는다
        interrupted = interrupted_generation_ids([cut, call("same", "forwarded", "2026-11-19T00:00:01Z", "2026-11-19T00:00:02Z")],
                                                 STOP, product=False)
        m = marker(generations=[generation("same", status="unverified", last="http_404", tries=40)])
        reasons, summary = judge_llm(m, "api", SLUG, NAME, SAMPLING, interrupted)
        self.assertEqual((reasons, summary["unverified_interrupted"]), (["llm_generation_unverified"], 0))
        # 봉인 중단 행만 둘이면(같은 ID) 예외 그대로
        self.assertEqual(interrupted_generation_ids([cut, dict(cut)], STOP, product=False), frozenset({"same"}))

    def test_rows_must_match_the_marker(self):
        from runner_llm import rows_match_marker
        rows = [call("gen-1", "forwarded", "2026-11-19T00:00:01Z", "2026-11-19T00:00:02Z"),
                call("cut", "cancelled_by_seal", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z"),
                call("", "rejected_body", "2026-11-19T00:00:03Z", "2026-11-19T00:00:03Z"),
                call("", "rejected_after_seal", "2026-11-19T00:00:12Z", "2026-11-19T00:00:12Z")]
        for seq, row in enumerate(rows, 1):
            row["seq"] = seq
        m = marker(forwarded_requests=1, cancelled_by_seal=1, rejected_requests=1, rejected_after_seal=1,
                   generations=[generation("gen-1"), generation("cut")])
        self.assertTrue(rows_match_marker(rows, m))
        bad = [("dropped", rows[1:], m), ("seq_gap", [dict(r, seq=r["seq"] * 2) for r in rows], m),
               ("seq_bool", [dict(rows[0], seq=True)] + rows[1:], m),
               ("count", rows, dict(m, cancelled_by_seal=2)), ("count_type", rows, dict(m, forwarded_requests=1.0)),
               ("rejected", rows, dict(m, rejected_requests=0)), ("after_seal", rows, dict(m, rejected_after_seal=2)),
               ("ids", rows, dict(m, generations=[generation("gen-1")])), ("extra_id", rows, dict(m, generations=m["generations"] + [generation("x")])),
               ("decision_changed", [dict(rows[0], decision="upstream_error")] + rows[1:], m),
               ("not_marker", rows, None), ("missing_field", [{k: v for k, v in rows[0].items() if k != "decision"}] + rows[1:], m)]
        for label, got_rows, got_marker in bad:
            with self.subTest(label=label):
                self.assertFalse(rows_match_marker(got_rows, got_marker))
        # 표식을 쓴 뒤 엔진이 보낸 요청은 순번이 더 큰 봉인 뒤 거부 행으로만 붙는다(확인 쌍 B: 행 85·표식 76). 대조는 표식
        # 시점의 행(순번 1..N)까지이고, N 뒤 봉인 뒤 거부 행은 받는다.
        late = [dict(call("", "rejected_after_seal", "2026-11-19T00:00:12Z", "2026-11-19T00:00:12Z"), seq=seq) for seq in (5, 6, 7)]
        self.assertTrue(rows_match_marker(rows + late, m))
        self.assertTrue(rows_match_marker(list(reversed(rows + late)), m))  # 줄 순서는 상관없다(순번으로 본다)
        for label, got_rows in (("late_forwarded", rows + [dict(late[0], decision="forwarded", generation_id="x")]),
                                ("late_cancelled", rows + [dict(late[0], decision="cancelled_by_seal", generation_id="")]),
                                ("late_gap", rows + [late[1]]),  # 순번 5 없이 6
                                ("short", rows[:3])):  # 표식 셈보다 행이 적음
            with self.subTest(label=label):
                self.assertFalse(rows_match_marker(got_rows, m))
        self.assertFalse(rows_match_marker(rows, dict(m, rejected_after_seal=-1)))

    def test_o_exception_is_on_hold(self):
        # 제품 전송기는 끊긴 호출(upstream_error/client_closed_before_done)에 generation ID를 남기지 않고, ID는 응답을 끝까지
        # 읽은 forwarded에만 있다(#735 리뷰). 끊김과 ID를 잇는 근거가 없어 O 예외는 보류: 어떤 행이든 빈 집합이다.
        rows = [call("", "upstream_error", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z", "client_closed_before_done"),  # 실제 모양
                call("late", "forwarded", "2026-11-19T00:00:05Z", "2026-11-19T00:00:10Z"),  # 정지 순간에 끝난 완료 호출
                call("synthetic", "upstream_error", "2026-11-19T00:00:05Z", "2026-11-19T00:00:10Z", "client_closed_before_done"),
                call("seal", "cancelled_by_seal", "2026-11-19T00:00:05Z", "2026-11-19T00:00:11Z")]
        self.assertEqual(interrupted_generation_ids(rows, STOP, product=True), frozenset())

    def test_interrupted_unverified_is_counted_apart_and_does_not_invalidate(self):
        m = marker(generations=[generation("gen-1"), generation("cut", status="unverified", last="http_404", tries=40)])
        reasons, summary = judge_llm(m, "api", SLUG, NAME, SAMPLING, frozenset({"cut"}))
        self.assertEqual(reasons, [])
        self.assertEqual((summary["unverified"], summary["unverified_interrupted"], summary["interrupted_calls"]), (1, 1, 1))
        # 정상 완료 호출의 미확인은 지금처럼 무효다(끊긴 호출 집합에 없으면)
        reasons, summary = judge_llm(m, "api", SLUG, NAME, SAMPLING, frozenset({"other"}))
        self.assertEqual(reasons, ["llm_generation_unverified"])
        self.assertEqual(summary["unverified_interrupted"], 0)


if __name__ == "__main__":
    unittest.main()
