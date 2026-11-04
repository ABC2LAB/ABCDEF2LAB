"""세션 전 제공자 재조회 대조(평가 프로토콜 §6): OpenRouter 엔드포인트 조회 응답을 커밋된 고정 근거와 비교한다.

실행(조회 응답은 새 빈 디렉터리에 받고, 그 디렉터리에서 코드를 실행하지 않는다):
  D=$(mktemp -d) && curl -sS --max-time 20 -o "$D/endpoints.json" \\
    https://openrouter.ai/api/v1/models/qwen/qwen3.6-35b-a3b/endpoints
  python3 -I -B scripts/eval/provider_recheck.py "$D/endpoints.json"

통과 조건: 응답 모델이 공식 프로파일 모델이고, 고정 slug가 엔드포인트 하나에만 매칭되며(접두 매칭 규칙), 그 엔드포인트의
제공자 표시 이름·정밀도가 고정 근거와 같고, 게이트웨이가 넣는 파라미터를 전부 지원한다고 표시한다.
하나라도 다르면 그 세션을 시작하지 않는다(§6). 종료 코드: 0 같음, 1 다름, 2 입력 오류.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path

DOCS = Path(__file__).resolve().parents[2] / "docs/evaluation/profiles"
PROFILE = DOCS / "official-35b.json"
ENDPOINTS_EVIDENCE = DOCS / "evidence/openrouter-endpoints-qwen3.6-35b-a3b-20261104.json"
PARAMS_EVIDENCE = DOCS / "evidence/openrouter-endpoint-params-qwen3.6-35b-a3b-20261104.json"
MAX_RESPONSE_BYTES = 4 << 20


def _matches(tag: object, slug: str) -> bool:
    # 근거 파일의 match_rule과 같다: tag == slug 또는 tag가 slug + "/"로 시작.
    return isinstance(tag, str) and (tag == slug or tag.startswith(slug + "/"))


def recheck(response: bytes, profile: dict, endpoints_evidence: dict, params_evidence: dict) -> list[str]:
    """다른 점 목록(비면 같음). 응답 형식이 틀리면 ValueError."""
    data = json.loads(response.decode("utf-8"))
    if not isinstance(data, dict) or not isinstance(data.get("data"), dict):
        raise ValueError("응답에 data 객체가 없음")
    endpoints = data["data"].get("endpoints")
    if not isinstance(endpoints, list) or not all(isinstance(e, dict) for e in endpoints):
        raise ValueError("응답에 endpoints 목록이 없음")
    llm = profile["environment"]["llm"]
    slug, model = llm["provider"], llm["model"]
    differences = []
    if data["data"].get("id") != model:
        differences.append(f"응답 모델이 {model}이 아님")
    pinned = endpoints_evidence["matched_endpoints"]
    if len(pinned) != 1 or endpoints_evidence["pinned_slug"] != slug or params_evidence["pinned_slug"] != slug:
        raise ValueError("고정 근거 파일이 공식 프로파일 slug 하나를 가리키지 않음")
    matched = [e for e in endpoints if _matches(e.get("tag"), slug)]
    if len(matched) != 1:
        differences.append(f"slug {slug}에 매칭되는 엔드포인트가 {len(matched)}개(고정 때 1개)")
    for endpoint in matched[:1]:
        for field in ("provider_name", "quantization"):
            if endpoint.get(field) != pinned[0][field]:
                differences.append(f"{field}가 고정 때 값과 다름")
        supported = endpoint.get("supported_parameters")
        missing = sorted(set(params_evidence["pinned_parameters"]) - set(supported if isinstance(supported, list) else []))
        if missing:
            differences.append(f"지원 표시가 빠진 고정 파라미터 {missing}")
    return differences


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="세션 전 OpenRouter 엔드포인트 재조회 대조 (평가 프로토콜 §6)")
    parser.add_argument("response", help="방금 받은 엔드포인트 조회 응답 JSON")
    args = parser.parse_args(argv)
    try:
        with open(args.response, "rb") as stream:
            raw = stream.read(MAX_RESPONSE_BYTES + 1)
        if len(raw) > MAX_RESPONSE_BYTES:
            raise ValueError("응답이 너무 큼")
        profile = json.loads(PROFILE.read_text(encoding="utf-8"))
        differences = recheck(raw, profile, json.loads(ENDPOINTS_EVIDENCE.read_text(encoding="utf-8")),
                              json.loads(PARAMS_EVIDENCE.read_text(encoding="utf-8")))
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f"재조회 대조 불가: {error}", file=sys.stderr)
        return 2
    digest = hashlib.sha256(raw).hexdigest()
    if differences:
        for line in differences:
            print(f"다름: {line}", file=sys.stderr)
        print(f"고정 때와 다름 — 이 세션을 시작하지 않는다(응답 sha256 {digest})")
        return 1
    print(f"고정 때와 같음(응답 sha256 {digest}) — 세션 기록에 이 해시를 남긴다")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
