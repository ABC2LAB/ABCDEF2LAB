"""보존 sidecar v3 칸을 회차 값에서 만드는 공용 부품(탐색 분리 배치 결정안 §3.3). B 러너(runner.py)와 O 러너가 같은 정의를 쓴다.

여기서는 값을 모으기만 한다. 모양 검사·서명·짝 대조는 preservation_record(build_payload_v3·validate_series_v3)가 한다.
"""

from __future__ import annotations

import hashlib
from pathlib import Path
from typing import Mapping, Sequence

from preservation_record import digest

EVAL_DIR = Path(__file__).resolve().parent
# 공통 러너 설정(B·O가 같아야 짝): 폴링·drain 시한·봉인 뒤 유예·정착 대기·호스트 감시 간격(실제 표본 주기와 허용 최장
# 간격 둘 다, #696 리뷰)·이벤트 lookback.
COMMON_RUNNER_SETTINGS = ("poll_seconds", "drain_timeout_seconds", "drain_grace_seconds", "post_settle_seconds",
                          "host_watch_interval_seconds", "host_watch_max_gap_seconds", "events_lookback_seconds")
# B 쪽 구동 설정(쪽 안에서만 고정): 작업 요청 SHA·LLM 스트리밍·tmpfs 하한·worker·컨텍스트·LLM 검증 시한.
B_DRIVER_SETTINGS = ("task_request_sha256", "llm_stream", "tmpfs_min_free_bytes", "worker_count", "context_window_k",
                     "llm_verify_timeout_seconds")
# O 쪽 구동 설정(쪽 안에서만 고정, 결정안 §3.3): 작업 번호 대기·상태 폴링 간격·등록 유효 시간.
O_DRIVER_SETTINGS = ("task_wait_seconds", "poll_interval_seconds", "registration_valid_hours")
# O 모델 예산 선언(결정안 §3.3): 제품 설치 예산 값만. 설치마다 바뀌는 유효 시각·승인 ID는 넣지 않는다.
MODEL_BUDGET_FIELDS = ("max_requests", "max_input_tokens", "max_output_tokens", "max_cost_usd_micros",
                       "max_input_tokens_per_call", "max_output_tokens_per_call")
# 쪽별 구동 코드: B는 원본 엔진 API를 직접 부르는 모듈, O는 제품 API 구동 모듈.
B_DRIVER_FILES = ("runner_engine.py",)
O_DRIVER_FILES = ("runner_product.py",)


def driver_sha256(names: Sequence[str], root: Path = EVAL_DIR) -> str:
    """구동 코드 파일 바이트의 지문(이름과 내용 SHA를 함께 묶는다)."""
    return digest({name: hashlib.sha256((root / name).read_bytes()).hexdigest() for name in names})


def settings_sha256(values: Mapping, names: Sequence[str]) -> str:
    return digest({name: values[name] for name in names})


def model_pin_sha256(*, backend: str, model: str, provider_slug: str, provider_name: str, sampling, serving) -> str:
    """공통 대조 칸 model_pin_sha256. B(게이트웨이 적용 샘플링)·O(제품이 실제로 보낸 샘플링)가 같은 정의로 뜬다."""
    return digest({"backend": backend, "model": model, "provider_slug": provider_slug, "provider_name": provider_name,
                   "sampling": sampling, "serving": serving})


def model_budget_sha256(manifest: Mapping) -> str | None:
    """O 모델 예산 선언 지문(제품 설치 예산 값만). 칸이 빠졌거나 양의 정수가 아니면 None(호출자가 설정 오류로 막는다)."""
    if any(type(manifest.get(name)) is not int or manifest[name] <= 0 for name in MODEL_BUDGET_FIELDS):
        return None
    return digest({name: manifest[name] for name in MODEL_BUDGET_FIELDS})


def end_signal(first_terminal: dict | None, *, product: bool) -> dict | None:
    """엔진 끝 신호(§3.1)의 v3 모양. closing_reason은 O(제품 초안 끝 상태)만 있고 B는 null이다."""
    if first_terminal is None:
        return None
    return {"status": first_terminal["status"], "elapsed_seconds": first_terminal["elapsed_seconds"],
            "closing_reason": first_terminal.get("closing_reason") if product else None}


def surface_fields(surface: dict) -> dict:
    """도구 면 측정(tool_surface) 한 쪽의 결과에서 v3 칸 넷."""
    mcp = surface["mcp"]
    return {"mcp_version": mcp["package_version"], "mcp_raw_tools_sha256": mcp["raw"]["sha256"],
            "cli_surface_sha256": surface["cli"]["sha256"], "mcp_exposed_tools_sha256": mcp["exposed"]["sha256"]}
