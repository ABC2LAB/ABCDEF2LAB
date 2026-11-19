"""LLM 게이트웨이 검증 마커의 제공자 판정(평가 프로토콜 §2·§6).

게이트웨이는 무엇이 누구에게서 서빙됐는지만 기록하고, 고정 조건과 맞는지는 여기서 판정한다.
- api: 모든 generation이 검증되고(미검증·id 없는 응답 0), 응답 제공자가 기대 표시 이름이며(불일치 0),
  응답한 제공자와 다른 제공자로 간 시도가 없어야(fallback 0) 통과한다. 같은 제공자 재시도는 fallback이
  아니다. 시도 목록이 비어 fallback 여부를 확인할 수 없으면 통과로 보지 않는다(fail-closed).
- api 샘플링: 게이트웨이가 실제로 넣은 값(마커 `sampling`)이 선언과 같아야 한다(선언이 아니라 적용값을 대조).
- 샘플링 칸 거부 0건(게이트웨이 표식): 게이트웨이는 엔진이 보낸 샘플링 칸을 거부하고, 개선 후(O) 제품 transport는
  같은 칸(temperature)을 덮어쓴다. 두 쪽이 같은 조건이라는 말은 엔진이 그 칸을 보내지 않을 때만 맞으므로, 게이트웨이가
  샘플링 칸 때문에 거부한 요청이 하나라도 있으면 그 회차는 고정 조건이 아니다(탐색 분리 배치 결정안 4-4). 제품 기록 표식
  (generation-verify)에는 이 거부가 없어 null이다. provider 칸 거부·429 수는 기록만 한다(429는 결정안 §3.2).
- local: 제공자 개념이 없어 요청 수만 기록한다. 조회 결과가 있으면 구성이 섞인 것이다. 샘플링은 아직 넣지 않는다.
엔드포인트 ID는 의미를 실측으로 확인하기 전까지 판정에 쓰지 않고 기록만 한다.
"""

from __future__ import annotations

import re
from datetime import datetime

_MARKER_FIELDS = frozenset({"role", "backend", "provider", "sampling", "sealed_at", "drained_at", "verified_at",
                            "forwarded_requests", "upstream_errors", "upstream_error_ends", "rejected_requests", "rejected_after_seal",
                            "cancelled_by_seal", "responses_without_generation_id", "generations", "log_complete",
                            "rejected_sampling", "rejected_provider", "rate_limited"})
# 표식 역할. 게이트웨이(B)는 샘플링·provider 칸 거부 수를 꼭 적고, 제품 기록 사후 검증(O)은 null로 둔다.
GATEWAY_ROLE = "llm-gateway"
_BODY_KEY_REJECTIONS = ("rejected_sampling", "rejected_provider")
# upstream_error 행에서 게이트웨이가 관찰한 것. 호출자 쪽 취소·쓰기 실패와 위쪽 응답의 종료 줄 읽기만 말한다.
# 엔진이 그 줄을 받았는지, 어느 쪽이 먼저 닫았는지, 끊긴 책임은 말하지 않는다.
UPSTREAM_ENDS = frozenset({"client_closed_after_done", "client_closed_before_done", "upstream_cut_after_done", "upstream_cut_before_done"})
_COUNTS = ("forwarded_requests", "upstream_errors", "rejected_requests", "rejected_after_seal", "cancelled_by_seal",
           "responses_without_generation_id", "rate_limited")
SAMPLING_KEYS = frozenset({"temperature", "top_p", "top_k", "min_p", "presence_penalty", "repetition_penalty", "seed"})
LOCAL_SAMPLING_KEYS = frozenset({"temperature", "top_p", "presence_penalty", "frequency_penalty", "seed"})


# generation 조회 흔적(계측기 lookupTrace): 시도 수와 마지막 응답 종류만 있다. 미확인이 통계 지연(http_404·no_provider)인지
# 즉시 거부(http_401·http_403)인지 회차 뒤에 가른다(2026-11-18 VAmPI 유료 파일럿에서 원인을 가를 수 없었다).
# 시한 안에 한 번도 묻지 못한 것은 시도 0·deadline이다(계측기 lookupDeadline).
LOOKUP_LAST = frozenset({"verified", "network_error", "request_error", "body_invalid", "no_provider", "deadline"})
_LOOKUP_HTTP = re.compile(r"http_[1-5][0-9]{2}")
# 계측기가 발행할 수 있는 시도 수 상한(cmd/eval-meter maxVerifyAttempts, 러너 설정 MAX_VERIFY_ATTEMPTS와 같은 값). 이보다 큰
# 흔적은 계측기가 만들 수 없으므로 표식 오류다(#717 리뷰).
MAX_LOOKUP_TRIES = 100


def _lookup_ok(trace: object, status: str) -> bool:
    if not isinstance(trace, dict) or set(trace) != {"tries", "last"}:
        return False
    tries, last = trace["tries"], trace["last"]
    if (type(tries) is not int or not 0 <= tries <= MAX_LOOKUP_TRIES or not isinstance(last, str)
            or (tries == 0) != (last == "deadline")):
        return False
    if last not in LOOKUP_LAST and not _LOOKUP_HTTP.fullmatch(last):
        return False
    return (last == "verified") == (status == "verified")


class LLMMarkerError(ValueError):
    """검증 마커 구조가 계약과 다름. 고정 코드만 담는다."""


def _check_shape(marker: object) -> None:
    if not isinstance(marker, dict) or set(marker) != _MARKER_FIELDS:
        raise LLMMarkerError("llm_marker_invalid")
    sampling = marker["sampling"]
    if sampling is not None and (not isinstance(sampling, dict) or set(sampling) not in (SAMPLING_KEYS, LOCAL_SAMPLING_KEYS) or any(
            type(value) not in (int, float) for value in sampling.values())):
        raise LLMMarkerError("llm_marker_invalid")
    if (type(marker["log_complete"]) is not bool or not isinstance(marker["generations"], list)
            or any(type(marker[name]) is not int or marker[name] < 0 for name in _COUNTS)):
        raise LLMMarkerError("llm_marker_invalid")
    gateway = marker["role"] == GATEWAY_ROLE
    for name in _BODY_KEY_REJECTIONS:
        value = marker[name]
        if (value is None) == gateway or (value is not None and (type(value) is not int or value < 0)):
            raise LLMMarkerError("llm_marker_invalid")
    if gateway and marker["rejected_sampling"] + marker["rejected_provider"] > marker["rejected_requests"]:
        raise LLMMarkerError("llm_marker_invalid")
    ends = marker["upstream_error_ends"]
    if (not isinstance(ends, dict) or set(ends) != UPSTREAM_ENDS or any(type(value) is not int or value < 0 for value in ends.values())
            or sum(ends.values()) != marker["upstream_errors"]):
        raise LLMMarkerError("llm_marker_invalid")
    for generation in marker["generations"]:
        if (not isinstance(generation, dict) or set(generation) != {"id", "status", "provider_name", "attempts", "lookup"}
                or generation["status"] not in ("verified", "unverified") or not _lookup_ok(generation["lookup"], generation["status"])
                or not isinstance(generation["provider_name"], str) or not isinstance(generation["attempts"], list)):
            raise LLMMarkerError("llm_marker_invalid")
        for attempt in generation["attempts"]:
            if (not isinstance(attempt, dict) or set(attempt) != {"provider_name", "endpoint_id", "status"}
                    or not isinstance(attempt["provider_name"], str) or not isinstance(attempt["endpoint_id"], str)
                    or type(attempt["status"]) is not int):
                raise LLMMarkerError("llm_marker_invalid")


# 끊긴 호출(2026-11-19 사용자 결정, 평가 프로토콜 §6): 회차를 멈출 때 진행 중이던 호출은 응답이 끝나지 않아 제공자 통계가
# 끝내 생기지 않을 수 있다. 기록으로 증명된 것만 끊긴 호출로 본다. 세 조건이 모두 기록에 있어야 한다:
# (1) 시작이 정지 신호(B 게이트웨이 봉인) 전, (2) 응답 완료 기록 없음(결정이 forwarded가 아님), (3) 봉인으로 끊김(게이트웨이가
# 봉인 때 끊은 cancelled_by_seal). 정지와 상관없이 회차 중간에 실패한 호출은 여기에 들지 않는다. O는 보류(아래 함수 주석).


def _instant(value: object) -> datetime | None:
    if not isinstance(value, str):
        return None
    try:
        moment = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    return moment if moment.tzinfo is not None else None


def interrupted_generation_ids(rows: list[dict], stop_at: str, *, product: bool) -> frozenset[str]:
    """호출 행(B 게이트웨이 행·O 제품 호출 기록, 같은 칸·어휘)과 정지 신호 시각으로 기록상 끊긴 호출의 generation ID를 고른다.
    B의 증거는 게이트웨이가 봉인 때 직접 끊은 cancelled_by_seal 하나다. B 엔진은 봉인 때 멈추지 않으므로 B의 호출자 쪽 끊김은
    봉인 탓이라고 기록상 말할 수 없어 넣지 않는다. 시각을 읽을 수 없는 행은 끊긴 호출로 보지 않는다(추정으로 분류하지 않는다).
    O(product=True)는 끊긴 호출에 generation ID가 없어 늘 빈 집합이다(보류)."""
    if product:
        # O 예외는 보류한다(#735 리뷰). 제품 전송기는 응답을 끝까지 읽은 호출(forwarded)에만 generation ID를 남기고, 끊긴 호출
        # (upstream_error/client_closed_before_done)은 ID가 비어 있다(llmpolicy/openrouter_transport.go). 그래서 끊긴 호출과 미확인
        # generation을 잇는 기록 근거가 없다. 근거가 생길 때까지 O의 미확인은 모두 정상 완료 호출로 보고 무효 사유로 센다.
        # (B 게이트웨이는 스트림 앞부분에서 받은 ID를 끊긴 호출에도 남긴다, cmd/eval-meter/gateway.go.)
        return frozenset()
    stop = _instant(stop_at)
    if stop is None:
        return frozenset()
    ids = set()
    for row in rows:
        started, completed, gid = _instant(row.get("started_at")), _instant(row.get("completed_at")), row.get("generation_id")
        if not isinstance(gid, str) or not gid or started is None or completed is None or not started < stop <= completed:
            continue
        decision = row.get("decision")
        if decision == "cancelled_by_seal":
            ids.add(gid)
    return frozenset(ids)


def judge_llm(marker: object, backend: str, provider_slug: str, provider_name: str,
              sampling: dict | None, interrupted: frozenset[str] = frozenset()) -> tuple[list[str], dict]:
    """(사유 목록, 기록용 요약). 사유가 없어야 그 회차의 LLM 조건을 고정됐다고 말할 수 있다.

    sampling은 선언에서 만든 기대 적용값이다. 백엔드마다 키 묶음이 다르다.
    """
    _check_shape(marker)
    reasons: set[str] = set()
    # 숫자 표기(1과 1.0)는 같은 값으로 본다. 키·값이 하나라도 다르거나 빠지면 고정이 아니다.
    # float로 바꾸지 않고 그대로 비교한다: 파이썬의 int·float 비교는 정확해서 2^53을 넘는 seed도 구분한다.
    applied = marker["sampling"]
    if (applied is None) != (sampling is None) or (
            applied is not None and (set(applied) != set(sampling)
                                     or any(applied[key] != sampling[key] for key in sampling))):
        reasons.add("llm_sampling_mismatch")
    if marker["backend"] != backend:
        reasons.add("llm_backend_mismatch")
    if not marker["log_complete"]:
        reasons.add("llm_log_incomplete")
    if marker["rejected_sampling"]:
        reasons.add("llm_sampling_rejected")
    generations = marker["generations"]
    unverified = mismatched = fallback = fallback_unknown = unverified_interrupted = 0
    providers, endpoints = set(), set()
    unverified_last: dict[str, int] = {}
    if backend == "api":
        if marker["provider"] != provider_slug:
            reasons.add("llm_pin_mismatch")  # 게이트웨이가 다른 slug를 고정했다
        if marker["responses_without_generation_id"]:
            reasons.add("llm_generation_unverifiable")
        for generation in generations:
            if generation["status"] != "verified":
                unverified += 1
                if generation["id"] in interrupted:
                    unverified_interrupted += 1  # 기록상 끊긴 호출: 따로 세고 회차를 무효로 하지 않는다
                last = generation["lookup"]["last"]
                unverified_last[last] = unverified_last.get(last, 0) + 1
                continue
            providers.add(generation["provider_name"])
            if generation["provider_name"] != provider_name:
                mismatched += 1
            attempts = generation["attempts"]
            if not attempts:
                fallback_unknown += 1
            elif any(attempt["provider_name"] != generation["provider_name"] for attempt in attempts):
                fallback += 1  # 라우팅이 다른 제공자를 거쳤다(불일치와 별개로 센다)
            endpoints.update(attempt["endpoint_id"] for attempt in attempts if attempt["endpoint_id"])
        for count, reason in ((unverified - unverified_interrupted, "llm_generation_unverified"), (mismatched, "llm_provider_mismatch"),
                              (fallback, "llm_fallback_attempted"), (fallback_unknown, "llm_fallback_unverifiable")):
            if count:
                reasons.add(reason)
    elif generations or marker["provider"]:
        reasons.add("llm_backend_mismatch")  # local 회차에 제공자 조회가 섞임
    summary = {
        "backend": marker["backend"], "pinned_provider": marker["provider"], "expected_provider_name": provider_name,
        "sampling_applied": applied,
        **{name: marker[name] for name in _COUNTS}, "upstream_error_ends": dict(sorted(marker["upstream_error_ends"].items())),
        **{name: marker[name] for name in _BODY_KEY_REJECTIONS},
        "generations": len(generations), "unverified": unverified, "unverified_last": dict(sorted(unverified_last.items())),
        "unverified_interrupted": unverified_interrupted, "interrupted_calls": len(interrupted),
        "lookup_tries_max": max((generation["lookup"]["tries"] for generation in generations), default=0),
        "provider_mismatch": mismatched, "fallback_attempted": fallback, "fallback_unverifiable": fallback_unknown,
        "providers": sorted(providers), "endpoint_ids": sorted(endpoints),
        "sealed_at": marker["sealed_at"], "drained_at": marker["drained_at"], "verified_at": marker["verified_at"],
    }
    return sorted(reasons), summary


# 제품(O) OpenRouter 호출 기록 사후 검증 표식(eval-meter -role generation-verify)의 역할 값. 게이트웨이 표식과 같은 모양이고
# 같은 조회 코드로 만든다(탐색 분리 배치 결정안 D(b)).
PRODUCT_VERIFY_ROLE = "generation-verify"


def judge_product_llm(marker: object, baseline_applied: dict | None, provider_slug: str,
                      provider_name: str, interrupted: frozenset[str] = frozenset()) -> tuple[list[str], dict]:
    """O의 호출 기록 사후 검증 표식을 B와 같은 규칙(judge_llm)으로 판정한다.

    샘플링은 선언이 아니라 같은 회차 묶음의 원본(B) 게이트웨이가 실제로 넣은 값(B 표식의 `sampling`)과 대조한다. O 표식의
    `sampling`은 제품 transport가 실제로 보낸 바이트에서 읽은 값이다(manifest v3). v1·v2 승인이면 null이라 불일치로 남는다.
    게이트웨이 표식을 O 자리에 넣지 못하게 역할을 본다. 제품이 기록을 남기지 못한 수는 이 표식 밖이라 호출자가 따로 본다.
    """
    if not isinstance(marker, dict) or marker.get("role") != PRODUCT_VERIFY_ROLE:
        raise LLMMarkerError("llm_marker_invalid")
    if not isinstance(baseline_applied, dict) or set(baseline_applied) != SAMPLING_KEYS or any(
            type(value) not in (int, float) for value in baseline_applied.values()):
        raise LLMMarkerError("llm_marker_invalid")
    return judge_llm(marker, "api", provider_slug, provider_name, baseline_applied, interrupted)
