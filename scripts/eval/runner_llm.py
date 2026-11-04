"""LLM 게이트웨이 검증 마커의 제공자 판정(평가 프로토콜 §2·§6).

게이트웨이는 무엇이 누구에게서 서빙됐는지만 기록하고, 고정 조건과 맞는지는 여기서 판정한다.
- api: 모든 generation이 검증되고(미검증·id 없는 응답 0), 응답 제공자가 기대 표시 이름이며(불일치 0),
  응답한 제공자와 다른 제공자로 간 시도가 없어야(fallback 0) 통과한다. 같은 제공자 재시도는 fallback이
  아니다. 시도 목록이 비어 fallback 여부를 확인할 수 없으면 통과로 보지 않는다(fail-closed).
- local: 제공자 개념이 없어 요청 수만 기록한다. 조회 결과가 있으면 구성이 섞인 것이다.
엔드포인트 ID는 의미를 실측으로 확인하기 전까지 판정에 쓰지 않고 기록만 한다.
"""

from __future__ import annotations

_MARKER_FIELDS = frozenset({"role", "backend", "provider", "sealed_at", "drained_at", "verified_at",
                            "forwarded_requests", "upstream_errors", "rejected_requests", "rejected_after_seal",
                            "cancelled_by_seal", "responses_without_generation_id", "generations", "log_complete"})
_COUNTS = ("forwarded_requests", "upstream_errors", "rejected_requests", "rejected_after_seal", "cancelled_by_seal",
           "responses_without_generation_id")


class LLMMarkerError(ValueError):
    """검증 마커 구조가 계약과 다름. 고정 코드만 담는다."""


def _check_shape(marker: object) -> None:
    if not isinstance(marker, dict) or set(marker) != _MARKER_FIELDS:
        raise LLMMarkerError("llm_marker_invalid")
    if (type(marker["log_complete"]) is not bool or not isinstance(marker["generations"], list)
            or any(type(marker[name]) is not int or marker[name] < 0 for name in _COUNTS)):
        raise LLMMarkerError("llm_marker_invalid")
    for generation in marker["generations"]:
        if (not isinstance(generation, dict) or set(generation) != {"id", "status", "provider_name", "attempts"}
                or generation["status"] not in ("verified", "unverified")
                or not isinstance(generation["provider_name"], str) or not isinstance(generation["attempts"], list)):
            raise LLMMarkerError("llm_marker_invalid")
        for attempt in generation["attempts"]:
            if (not isinstance(attempt, dict) or set(attempt) != {"provider_name", "endpoint_id", "status"}
                    or not isinstance(attempt["provider_name"], str) or not isinstance(attempt["endpoint_id"], str)
                    or type(attempt["status"]) is not int):
                raise LLMMarkerError("llm_marker_invalid")


def judge_llm(marker: object, backend: str, provider_slug: str, provider_name: str) -> tuple[list[str], dict]:
    """(사유 목록, 기록용 요약). 사유가 없어야 그 회차의 LLM 조건을 고정됐다고 말할 수 있다."""
    _check_shape(marker)
    reasons: set[str] = set()
    if marker["backend"] != backend:
        reasons.add("llm_backend_mismatch")
    if not marker["log_complete"]:
        reasons.add("llm_log_incomplete")
    generations = marker["generations"]
    unverified = mismatched = fallback = fallback_unknown = 0
    providers, endpoints = set(), set()
    if backend == "api":
        if marker["provider"] != provider_slug:
            reasons.add("llm_pin_mismatch")  # 게이트웨이가 다른 slug를 고정했다
        if marker["responses_without_generation_id"]:
            reasons.add("llm_generation_unverifiable")
        for generation in generations:
            if generation["status"] != "verified":
                unverified += 1
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
        for count, reason in ((unverified, "llm_generation_unverified"), (mismatched, "llm_provider_mismatch"),
                              (fallback, "llm_fallback_attempted"), (fallback_unknown, "llm_fallback_unverifiable")):
            if count:
                reasons.add(reason)
    elif generations or marker["provider"]:
        reasons.add("llm_backend_mismatch")  # local 회차에 제공자 조회가 섞임
    summary = {
        "backend": marker["backend"], "pinned_provider": marker["provider"], "expected_provider_name": provider_name,
        **{name: marker[name] for name in _COUNTS}, "generations": len(generations), "unverified": unverified,
        "provider_mismatch": mismatched, "fallback_attempted": fallback, "fallback_unverifiable": fallback_unknown,
        "providers": sorted(providers), "endpoint_ids": sorted(endpoints),
        "sealed_at": marker["sealed_at"], "drained_at": marker["drained_at"], "verified_at": marker["verified_at"],
    }
    return sorted(reasons), summary
