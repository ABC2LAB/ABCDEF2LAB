"""러너와 제품 E②를 잇는 회차 하나의 연결(제어 채널 제안 §3). 회차 값을 여기서 묶고 러너 순서는 runner.py가 정한다."""

from __future__ import annotations

import re
import time
from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

from e2_collect import OUTCOME_PRODUCT_FAILURE, E2Collection, collect_e2_export, subprocess_verifier
from e2_control import CloseResult, ControlChannel, ServiceExpectation, Waiting, close_execution, wait_service_ready
from e2_export import E2Expectation, HEADER_PASSTHROUGH, TARGET_PUBLIC_PRACTICE
from e2_prepare import (CONTAINER_STATE_DIR, E2Prepared, E2RunResources, E2RunValues, approval_window, issue_run_ca, new_reference,
                        prepare_e2_config, render_policy)

CLOSE_REFERENCE_PREFIX = "close"
# 엔진 쪽 실행 참조. 제품 backend가 평가 eval_run_id와 같으면 거부한다.
RUN_REFERENCE_PREFIX = "e2run"
# 게시 디렉터리 안의 한 단계 이름만 받는다. 제어 응답 값으로 경로가 벗어나지 않게 한다.
_EXECUTION_DIR = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}\Z")


def evaluation_declaration(header_mode: str, llm_backend: str) -> dict | None:
    """평가 전달 모드의 선언. 다른 모드에는 없다."""
    if header_mode != HEADER_PASSTHROUGH:
        return None
    return {"llm_backend": llm_backend, "target_kind": TARGET_PUBLIC_PRACTICE}


@dataclass(frozen=True)
class E2LinkPaths:
    config: Path
    evidence: Path
    control: Path
    copy: Path


def _utc_now() -> datetime:
    return datetime.now(timezone.utc)


class E2Link:
    def __init__(self, product: dict, paths: E2LinkPaths, run_id: str, model_tag: str, llm_backend: str, port: int, now: Callable[[], datetime] = _utc_now):
        self.product, self.paths, self.run_id, self.model_tag, self.llm_backend = product, paths, run_id, model_tag, llm_backend
        self.evaluation = evaluation_declaration(product["header_mode"], llm_backend)
        self.run_ref = run_ref = new_reference(RUN_REFERENCE_PREFIX)
        valid_from, valid_until = approval_window(now(), product["window_seconds"], product["approval_valid_until"])
        cert_sha256, key_sha256 = issue_run_ca(Path(product["policy_tool"]), paths.config, run_ref, valid_until, product["policy_tool_timeout_seconds"])
        policy = render_policy(Path(product["policy_path"]), E2RunResources(run_ref, valid_from, valid_until, CONTAINER_STATE_DIR, product["helper"],
                                                                            product["helper_sha256"], cert_sha256, key_sha256, product["meter_ip"]))
        self.prepared: E2Prepared = prepare_e2_config(paths.config, E2RunValues(run_id, model_tag, product["header_mode"], self.evaluation, port), policy)
        self.channel = ControlChannel(paths.control, run_id, self.prepared.reservation_ref, product["exchange_timeout_seconds"])
        self.waiting = Waiting(time.monotonic, time.sleep, product["poll_seconds"])

    def service_ready(self) -> dict:
        expected = ServiceExpectation(self.prepared.config_sha256, self.product["header_mode"], self.evaluation,
                                      self.product["policy_fingerprint_sha256"])
        return wait_service_ready(self.channel, expected, self.waiting, self.product["ready_timeout_seconds"])

    def bind(self, reset_generation: str) -> None:
        self.channel.bind(reset_generation)

    def close(self) -> CloseResult:
        return close_execution(self.channel, new_reference(CLOSE_REFERENCE_PREFIX), self.waiting, self.product["close_ack_timeout_seconds"],
                               self.product["close_timeout_seconds"])

    def collect(self, close: CloseResult, task_id: str, reset_generation: str) -> E2Collection:
        if not isinstance(close.execution_ref, str) or not _EXECUTION_DIR.fullmatch(close.execution_ref):
            return E2Collection(OUTCOME_PRODUCT_FAILURE, "execution_ref_invalid", None, {}, None)
        expected = E2Expectation(eval_run_id=self.run_id, model_tag=self.model_tag, reset_generation=reset_generation,
                                 reservation_ref=self.prepared.reservation_ref, engine_task_id=task_id,
                                 policy_fingerprint_sha256=self.product["policy_fingerprint_sha256"], header_mode=self.product["header_mode"],
                                 llm_backend=self.llm_backend)
        return collect_e2_export(self.paths.evidence / close.execution_ref, self.paths.copy, expected, close.ack, str(self.prepared.key_path),
                                 subprocess_verifier(self.product["verify_tool"], self.product["verify_timeout_seconds"]))
