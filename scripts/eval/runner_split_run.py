"""탐색 분리 배치(개선 후 O) 회차 흐름 부품(탐색 분리 배치 결정안 §3·§3.1, 순서 4-7c-3).

O의 엔진 끝 신호는 엔진 작업 목록이 아니라 제품 초안 상태다(결정안 §3.1: 제품 실행 상태와 결과·마감 사유 칸). 러너는
B와 같은 감시 규칙(runner._EngineEndWatch: 창 동안 백그라운드 읽기, 첫 끝 상태, `down` 전 마지막 읽기, 읽기 실패는 셈만)을
제품 상태 읽기에 그대로 쓴다. 제품이 예산 마감(budget_limited)으로 닫히면 first_limit이 `product_limit:budget_limited`가 된다
(runner.first_limit).
"""

from __future__ import annotations

from runner import Clock, _EngineEndWatch
from runner_product import DRAFT_TERMINAL, ProductAPIError, ProductDriver

PRODUCT_END_FORMAT = "abc2lab-eval-runner-product-end-v1"
# 제품 초안 상태에서 서명 근거로 남길 칸(화면 파서 parseURLJob과 같은 이름). 끝 상태가 아니면 비어 있는 칸은 null이다.
PRODUCT_END_FIELDS = ("state", "phase", "reason_code", "outcome", "closing_reason", "drain_complete", "result_ready",
                      "intent_outcomes", "diagnostic_log_incomplete")


def product_status_view(driver: ProductDriver, draft_id: str) -> dict:
    """제품 초안 상태에서 끝 신호 칸만 꺼낸다. 없는 칸은 null(값을 만들지 않는다)."""
    draft = driver.status(draft_id)
    return {name: draft.get(name) for name in PRODUCT_END_FIELDS}


def product_end_watch(ports, driver: ProductDriver, draft_id: str, task_id: str, clock: Clock, started: float,
                      wait_seconds: float) -> _EngineEndWatch:
    """O 끝 신호 감시. 읽기 실패는 제품 구동기의 고정 code로 센다(비밀·응답 원문 없음)."""
    return _EngineEndWatch(ports, task_id, clock, started, wait_seconds,
                           read=lambda: product_status_view(driver, draft_id), fields=PRODUCT_END_FIELDS,
                           status_field="state", terminal=DRAFT_TERMINAL, terminal_extra=("closing_reason", "reason_code"),
                           fmt=PRODUCT_END_FORMAT, errors=(ProductAPIError,))
