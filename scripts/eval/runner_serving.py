"""로컬 LLM 서버의 실제 서빙 조건을 읽어 선언과 대조한다(평가 프로토콜 §6).

컨텍스트 크기는 OpenAI 호환 요청으로 바꿀 수 없고 넘치면 오류 없이 잘린다. 모델 태그는 같아도 가중치가
바뀔 수 있다. 그래서 선언만 믿지 않고 서버가 돌려주는 값을 회차 앞뒤로 본다. 지금은 Ollama API만 읽는다.

대상·엔진 경로가 아니라 러너와 고정 IP 로컬 서버 사이의 호출이다. 계측기를 거치지 않고 엔진 요청 수에도 들어가지 않는다.
"""

from __future__ import annotations

import http.client
import json
import re
import subprocess
from collections.abc import Callable
from contextlib import closing
from datetime import datetime, timezone

MAX_RESPONSE_BYTES = 1024 * 1024
UNVERIFIED = "llm_serving_unverified"
MISMATCH = "llm_serving_mismatch"
KEEP_ALIVE_SHORT = "llm_serving_keep_alive_short"
NOT_ON_GPU = "llm_serving_not_on_gpu"
PLACEMENT_GPU = "gpu"
# 서버의 size_vram은 자기 보고다. 버퍼 통계가 없으면 모델 파일 크기로 채워 전량 GPU처럼 보일 수 있다(Ollama llm MemorySize).
# 그래서 GPU 쪽 판독을 따로 받는다. 지원 backend는 nvidia-smi가 있는 NVIDIA뿐이고 그 밖은 확인 못 함으로 남긴다.
GPU_APPS_COMMAND = ("nvidia-smi", "--query-compute-apps=used_memory", "--format=csv,noheader,nounits")
GPU_READ_TIMEOUT_SECONDS = 10
# 드라이버 집계와 서버 집계는 단위·반올림이 달라 정확히 같지 않다. 실제 GPU로 맞춘 값이 아니어서 시범에서 확인한다.
GPU_ACCOUNTING_MIN_RATIO = 0.9
MIB = 1024 * 1024
# 적재 완료와 /api/ps 조회 사이의 여유. 남은 시간이 이보다 더 줄었으면 서버가 요청한 유지 기간을 따르지 않은 것이다.
KEEP_ALIVE_SLACK_SECONDS = 30
_EXPIRES_AT = re.compile(r"([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?:\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})")


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


def _remaining_seconds(expires_at: object, now: datetime) -> int | None:
    # 서버 시각은 나노초까지 오고 형식이 바뀔 수 있다. 모르는 형식은 값으로 읽지 않는다.
    match = _EXPIRES_AT.fullmatch(expires_at) if isinstance(expires_at, str) else None
    if match is None:
        return None
    offset = "+00:00" if match.group(2) == "Z" else match.group(2)
    try:
        return int((datetime.fromisoformat(match.group(1) + offset) - now).total_seconds())
    except ValueError:
        return None


def read_serving(host: str, port: int, model: str, timeout: float, *, keep_alive_seconds: int | None,
                 now: Callable[[], datetime] = lambda: datetime.now(timezone.utc)) -> dict:
    """서버 버전과 적재된 모델의 digest·컨텍스트·남은 유지 시간을 읽는다. 적재돼 있지 않으면 digest가 None이다.

    keep_alive_seconds를 주면 먼저 그 유지 기간으로 모델을 적재한다. 기본 유지 기간(5분)이면 compose 기동이나 긴 도구
    실행 중에 모델이 내려가 적재 시간이 시간 예산에 섞이고, 회차 뒤 조회도 "확인 못 함"이 된다. 이 값은 이후의 OpenAI
    호환 요청에도 이어진다(근거 ollama-keep-alive-20261106.json).
    """
    if keep_alive_seconds is not None and _request(
            host, port, "POST", "/api/generate",
            {"model": model, "stream": False, "keep_alive": f"{keep_alive_seconds}s"}, timeout).get("done") is not True:
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
            "keep_alive_remaining_seconds": _remaining_seconds(entry.get("expires_at"), now()),
            # CPU로 밀려났는지 본다. placement가 gpu일 때만 판정에 쓴다.
            "size": _optional_int(entry.get("size")), "size_vram": _optional_int(entry.get("size_vram")),
            "quantization_level": quantization if isinstance(quantization, str) else None,
            "other_models_loaded": len(models) - len(mine)}


def parse_gpu_apps_memory(text: str) -> int | None:
    """compute 프로세스들의 GPU 메모리 합(바이트). 프로세스가 없으면 0, 값을 못 읽으면(`[N/A]` 등) None이다."""
    total = 0
    for line in text.splitlines():
        value = line.strip()
        if not value:
            continue
        if not value.isascii() or not value.isdigit():
            return None
        total += int(value) * MIB
    return total


def read_gpu_used_bytes(run: Callable[[tuple[str, ...]], str] | None = None) -> int | None:
    """GPU 쪽 판독. 도구가 없거나 실패하면 None이고, 호출자는 확인 못 함으로 처리한다."""
    def default(command: tuple[str, ...]) -> str:
        return subprocess.run(command, capture_output=True, text=True, timeout=GPU_READ_TIMEOUT_SECONDS, check=True).stdout
    try:
        return parse_gpu_apps_memory((run or default)(GPU_APPS_COMMAND))
    except (OSError, subprocess.SubprocessError, UnicodeError):
        return None


def unload_model(host: str, port: int, model: str, timeout: float) -> None:
    """모델을 내린다. 회차마다 같은 상태에서 시작하게 하고, 끝난 회차의 모델이 메모리에 남지 않게 한다."""
    if _request(host, port, "POST", "/api/generate", {"model": model, "stream": False, "keep_alive": 0},
                timeout).get("done") is not True:
        raise ServingError("llm_unload_failed")


def judge_serving(observed: dict, declared: dict, *, min_keep_alive_seconds: int | None = None) -> str | None:
    """선언과 다르면 사유 코드를, 같으면 None을 돌려준다. 유지 시간은 적재 직후에만 본다(그 뒤로는 줄어드는 값이다)."""
    if observed["model_digest"] is None or observed["context_length"] is None:
        return UNVERIFIED
    if any(observed[name] != declared[name] for name in ("server_version", "model_digest", "context_length")):
        return MISMATCH
    # 모델 일부가 CPU로 밀려도 오류가 나지 않는다. 느려진 만큼 시간 예산 안의 진척이 줄어 한쪽만 그러면 비교가 깨진다.
    if declared["placement"] == PLACEMENT_GPU:
        size, in_gpu, gpu_used = observed["size"], observed["size_vram"], observed["gpu_used_bytes"]
        # size_vram이 size보다 크면 Ollama CLI도 Unknown으로 본다.
        if size is None or in_gpu is None or size <= 0 or in_gpu > size or gpu_used is None:
            return UNVERIFIED
        # GPU 판독은 프로세스 합계다. 다른 모델이 떠 있으면 그 메모리로 이 모델의 적재를 대신할 수 있다.
        if observed["other_models_loaded"] > 0:
            return UNVERIFIED
        if in_gpu < size or gpu_used < in_gpu * GPU_ACCOUNTING_MIN_RATIO:
            return NOT_ON_GPU
    if min_keep_alive_seconds is not None:
        remaining = observed["keep_alive_remaining_seconds"]
        if remaining is None:
            return UNVERIFIED
        if remaining < min_keep_alive_seconds:
            return KEEP_ALIVE_SHORT
    return None
