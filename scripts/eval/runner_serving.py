"""로컬 LLM 서버의 실제 서빙 조건을 읽어 선언과 대조한다(평가 프로토콜 §6).

컨텍스트 크기는 OpenAI 호환 요청으로 바꿀 수 없고 넘치면 오류 없이 잘린다. 모델 태그는 같아도 가중치가
바뀔 수 있다. 그래서 선언만 믿지 않고 서버가 돌려주는 값을 회차 앞뒤로 본다. 지금은 Ollama API만 읽는다.

대상·엔진 경로가 아니라 러너와 고정 IP 로컬 서버 사이의 호출이다. 계측기를 거치지 않고 엔진 요청 수에도 들어가지 않는다.
"""

from __future__ import annotations

import http.client
import json
from contextlib import closing

MAX_RESPONSE_BYTES = 1024 * 1024
UNVERIFIED = "llm_serving_unverified"
MISMATCH = "llm_serving_mismatch"


class ServingError(ValueError):
    """서버 응답을 읽지 못했거나 형식이 다름. 값은 담지 않는다."""


def _request(host: str, port: int, method: str, path: str, body: dict | None, timeout: float) -> dict:
    # 직접 HTTPConnection을 써서 환경 프록시를 타지 않는다. 리다이렉트도 따르지 않는다.
    try:
        with closing(http.client.HTTPConnection(host, port, timeout=timeout)) as connection:
            payload = None if body is None else json.dumps(body).encode("utf-8")
            connection.request(method, path, body=payload, headers={"Content-Type": "application/json"})
            with closing(connection.getresponse()) as response:
                raw = response.read(MAX_RESPONSE_BYTES + 1)
                status = response.status
    except (OSError, http.client.HTTPException, ValueError):
        raise ServingError("llm_serving_read_failed") from None
    if status != 200 or len(raw) > MAX_RESPONSE_BYTES:
        raise ServingError("llm_serving_read_failed")
    try:
        decoded = json.loads(raw)
    except (ValueError, RecursionError):
        raise ServingError("llm_serving_read_failed") from None
    if not isinstance(decoded, dict):
        raise ServingError("llm_serving_read_failed")
    return decoded


def _optional_int(value: object) -> int | None:
    return value if type(value) is int else None


def read_serving(host: str, port: int, model: str, timeout: float, *, load: bool) -> dict:
    """서버 버전과 적재된 모델의 digest·컨텍스트를 읽는다. 적재돼 있지 않으면 digest가 None이다.

    load면 먼저 모델을 적재한다. 첫 회차만 적재 시간을 시간 예산에서 쓰지 않게 하려는 것이다.
    """
    if load and _request(host, port, "POST", "/api/generate", {"model": model, "stream": False}, timeout).get("done") is not True:
        raise ServingError("llm_serving_read_failed")
    version = _request(host, port, "GET", "/api/version", None, timeout).get("version")
    models = _request(host, port, "GET", "/api/ps", None, timeout).get("models")
    if not isinstance(version, str) or not isinstance(models, list) or not all(isinstance(m, dict) for m in models):
        raise ServingError("llm_serving_read_failed")
    mine = [m for m in models if m.get("name") == model]
    entry = mine[0] if len(mine) == 1 else {}
    digest = entry.get("digest")
    details = entry.get("details")
    quantization = details.get("quantization_level") if isinstance(details, dict) else None
    return {"server_version": version, "model_digest": digest if isinstance(digest, str) else None,
            "context_length": _optional_int(entry.get("context_length")),
            # CPU로 밀려났는지 볼 수 있게 남긴다. 판정에는 쓰지 않는다.
            "size": _optional_int(entry.get("size")), "size_vram": _optional_int(entry.get("size_vram")),
            "quantization_level": quantization if isinstance(quantization, str) else None,
            "other_models_loaded": len(models) - len(mine)}


def judge_serving(observed: dict, declared: dict) -> str | None:
    """선언과 다르면 사유 코드를, 같으면 None을 돌려준다."""
    if observed["model_digest"] is None or observed["context_length"] is None:
        return UNVERIFIED
    if any(observed[name] != declared[name] for name in ("server_version", "model_digest", "context_length")):
        return MISMATCH
    return None
