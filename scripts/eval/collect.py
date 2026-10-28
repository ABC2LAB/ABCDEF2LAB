"""측정 원자료 수집 — I/O 어댑터.

RepeatResult를 만들기 위한 바깥 세계와의 접점만 모은다. 순수 계산은 aggregate.py에
있다. 여기는 테스트 대상이 아니라 '경계'라서 얇게 두고, 의존하는 포맷이 아직
정해지지 않은 곳은 명시적 TODO로 막는다(조용한 빈 성공 금지, CLAUDE.md 절대규칙 5).

의존성: 표준 라이브러리만(urllib). 새 패키지는 승인 후.
"""

from __future__ import annotations

import json
import os
import urllib.request
from dataclasses import dataclass
from urllib.parse import urlparse

from aggregate import RepeatResult, ScanVerdict  # noqa: F401 (RepeatResult는 반환 타입)

# 랩 전용 허용 호스트. 대상 origin은 .env로 고정하고, 그 호스트가 이 집합에 들 때만
# 요청한다(CLAUDE.md 절대규칙 1: 설정의 대상 origin만, localhost 랩). 외부 대상 없음.
_ALLOWED_HOSTS = frozenset({"127.0.0.1", "localhost", "::1"})


@dataclass(frozen=True)
class EvalConfig:
    """평가 환경 설정. 값은 .env에서 온다 — 코드 하드코딩 금지(프로토콜 §2·§6)."""

    target_origin: str  # 예: http://127.0.0.1:3000 (EVAL_TARGET_ORIGIN)
    llm_base_url: str | None  # ARTEX_LLM_BASE_URL (Ollama)
    model_tag: str | None  # baseline·개선 후 동일해야 하는 정확한 태그 (§6)


def load_config() -> EvalConfig:
    """환경변수에서 평가 설정을 읽는다. .env는 compose/셸이 로드한 것을 전제."""
    origin = os.environ.get("EVAL_TARGET_ORIGIN")
    if not origin:
        raise RuntimeError(
            "EVAL_TARGET_ORIGIN 미설정 — .env에 랩 대상 origin을 고정하라 (예 http://127.0.0.1:3000)"
        )
    _reject_non_lab_origin(origin)
    return EvalConfig(
        target_origin=origin.rstrip("/"),
        llm_base_url=os.environ.get("ARTEX_LLM_BASE_URL"),
        model_tag=os.environ.get("EVAL_MODEL_TAG"),
    )


def _reject_non_lab_origin(origin: str) -> None:
    """origin이 랩(localhost) 밖이면 거부. 리다이렉트 후 URL도 호출부에서 재검사."""
    host = urlparse(origin).hostname
    if host not in _ALLOWED_HOSTS:
        raise RuntimeError(f"대상 origin이 랩 호스트가 아님: {host!r} — 외부 대상 금지(절대규칙 1)")


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """리다이렉트를 따라가지 않는다 — 대상 origin 밖으로 새지 않게(절대규칙 1)."""

    def redirect_request(self, *args, **kwargs):  # noqa: D401, ANN002, ANN003
        raise RuntimeError("대상 origin이 리다이렉트를 시도함 — 랩 밖 가능성, 차단")


def read_solved_state(config: EvalConfig, timeout_seconds: float = 10.0) -> dict[str, bool]:
    """Juice Shop `/api/Challenges`에서 챌린지별 실제 해결 여부를 읽는다.

    집계의 독립 정답(solved)이다. 엔진 출력과 분리된 경로로, 엔진 주장의 검증에만 쓴다.
    challenges.yml/`/api/Challenges`의 각 항목은 key와 solved(bool)를 가진다.
    """
    _reject_non_lab_origin(config.target_origin)
    url = f"{config.target_origin}/api/Challenges"
    opener = urllib.request.build_opener(_NoRedirect)
    req = urllib.request.Request(url, headers={"Accept": "application/json"})
    with opener.open(req, timeout=timeout_seconds) as resp:
        final_host = urlparse(resp.geturl()).hostname
        if final_host not in _ALLOWED_HOSTS:
            raise RuntimeError(f"응답 최종 호스트가 랩 밖: {final_host!r}")
        payload = json.load(resp)
    rows = payload.get("data", payload) if isinstance(payload, dict) else payload
    solved: dict[str, bool] = {}
    for row in rows:
        key = row.get("key")
        if key:
            solved[key] = bool(row.get("solved", False))
    return solved


def parse_scan_output(path: str) -> tuple[dict[str, ScanVerdict], tuple[str, ...]]:
    """스캔 출력 → (챌린지별 판정, out-of-list finding 키들).

    TODO(이동찬): 개선 후(우리 포크)의 finding/E③ 판정 레코드 포맷 확정 후 구현.
    TODO(윤재상): baseline(원본 ARTEX) 스캔 출력 포맷 확정 후 구현(또는 어댑터 분기).
    포맷이 정해지기 전에는 빈 성공으로 넘기지 않고 명시적으로 막는다(절대규칙 5).
    """
    raise NotImplementedError(
        f"스캔 출력 파서 미구현 — 엔진/baseline finding 포맷 확정 후 (경로: {path})"
    )
