"""평가 프로세스(공식 세션·러너)의 멈춤 신호 처리.

공식 세션을 뒤에서 띄우면(비대화형 셸의 `cmd &`, `nohup … &`) 셸이 SIGINT를 무시(SIG_IGN) 상태로 넘겨 준다. 무시 상태는 exec 뒤
자식 프로세스까지 그대로 물려가서, 시한을 넘긴 러너에 보낸 SIGINT가 먹지 않고 정리(compose down·보호 입력 삭제) 없이 SIGKILL로
죽는다(2026-11-21 기존 세션 하네스에서 상한 멈춤·감시 멈춤이 안 먹음). 그래서 시작할 때 SIGINT를 기본 처리(KeyboardInterrupt)로
되돌린다. 처리기가 있는 신호는 exec 때 기본값으로 바뀌므로 그 뒤 띄우는 자식도 SIGINT를 받는다. SIGTERM도 같은 정리 경로
(KeyboardInterrupt)로 받는다 — 공식 세션이 SIGINT 다음 단계로 보낸다.
"""

from __future__ import annotations

import signal


def _interrupt(signum, frame):  # noqa: ARG001
    raise KeyboardInterrupt


def interrupt_on_stop_signals() -> None:
    """SIGINT를 기본 처리로 되돌리고 SIGTERM도 KeyboardInterrupt로 받는다. 주 스레드에서 시작할 때 한 번 부른다."""
    signal.signal(signal.SIGINT, signal.default_int_handler)
    signal.signal(signal.SIGTERM, _interrupt)
