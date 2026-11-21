"""공식 회차 세션 실행(공식 회차 절차 6절). 쪽마다 유효 N회를 §5.1 교대 순서(N=3이면 B O O B B O)로 돌린다.

- 칸 하나 = (쪽, 그 쪽의 유효 회차 번호 `--repeat`). 완료면 다음 칸, 무효면 같은 칸을 같은 번호로 다시 돌린다.
- 그 쪽 시도가 2N에 닿았는데 유효가 N개 미만이면 그 쪽 칸을 더 시작하지 않고 멈춘다(`attempt_limit`, 원인부터 고친다).
- 쪽별 `provider_fault_stall` 무효가 상한(고정 초안 §3, 2회) 이상이면 측정 전체 실패로 멈춘다(`fault_stall_limit`).
  러너 상한 전에 제공자 장애로 끝난 회차(`provider_fault_end`, 평가 프로토콜 §5.1·고정 초안 §26)도 같은 부류라 같은 상한에 같이 센다.
- 설정 오류로 끝난 회차(`config_end`: 모델 호출 401~404, 승인 예산 상한에 닿은 budget_limited, 시작 거부)는 회차 디렉터리가
  생겼어도 시도로 세지 않고 멈춘다(`config_end:<쪽>`, 2026-11-21 사용자). 설정을 고친 뒤 `--resume`하면 같은 칸부터 잇는다.
  세션 기록에는 그 줄이 그대로 남는다(지우지 않는다).
- 일찍 끝난 회차 규칙이 걸린 회차(제공자 장애 끝·설정 오류·그 밖에 일찍 끝나 유효로 둔 회차)는 마지막 요약 줄의
  `early_end_runs`에 회차 ID·끝 상태·사유·근거 호출로 따로 모은다(측정 뒤 사람이 하나씩 검토한다).
- 러너 종료 코드 2(설정·입력 오류)는 시도로 세지 않고 멈춘다(`runner_config_error`).
- 대기는 이름 패턴이 아니라 PID(러너 프로세스)와 완료 파일(회차 디렉터리의 `runner-result.json`)로 건다. 대기마다 시한을 두고,
  넘으면 사유를 남기고 끝낸다(러너는 SIGINT → 유예 → SIGKILL, 2026-11-20 사용자 규칙).
- 회차 뒤 실행 중 컨테이너가 0이 될 때까지(시한 안) 기다린다. 남아 있으면 멈춘다(`containers_left`). 이어서 평가 compose 망
  (프로젝트 `eval-…`)이 다 지워질 때까지 기다린다. 남아 있으면 멈춘다(`networks_left`). 앞 회차 망이 남은 채로 다음 회차가
  같은 고정 대역의 망을 만들지 않게 막는 예방 조치다. 2026-11-20 `compose_up_failed`는 이것으로 설명되지 않는다(망 0에서도 남).
  유력한 원인 후보는 제품 postgres healthcheck가 초기화 임시 서버 동안 통과한 것이다(#833 재현·수정, 실패 회차 로그는 없음).
- **새 회차 디렉터리가 생겼으면 무슨 일이 있어도 시도 1회로 센다**(2026-11-20 사용자). 결과 파일이 있으면 그 상태를, 없으면
  무효(사유 = 멈춘 사유: `run_wait_timeout`·`result_missing`·`result_unreadable`)로 기록하고 그다음 멈춘다. 종료 코드와 상태가
  어긋나면 무효(`result_status_mismatch`)로 센다. 컨테이너가 남으면 그 회차의 실제 상태로 세고 멈춘다. 회차 디렉터리가 안 생긴
  경우(러너 종료 코드 2 등)만 시도로 세지 않는다. 측정 결과를 골라 버리는 길(멈춘 회차를 다시 돌려 기록에서 빼기)을 막는다.
- 회차마다 시작 전에 실행 중 컨테이너가 0개인지 본다(`--resume` 첫 회차 포함). 남아 있으면 시도로 세지 않고
  `containers_present`로 멈춘다(남은 컨테이너 때문에 다음 회차가 격리 검사에서 무효가 되어 시도가 날아가지 않게). 회차가 어떻게
  끝나든(시한 넘김·결과 없음 포함) 회차 뒤 컨테이너가 0이 될 때까지 기다리고, 안 내려가면 멈춘 사유에 `containers_left`를 같이
  적는다(2026-11-20 사용자).
- 선택: O 회차마다 manifest 틀에서 새 승인 ID·만료 시각으로 manifest를 다시 만든다(`--o-manifest-template`). 키 사용량
  상한(`--key-file`·`--usage-cap-usd`·`--min-remaining-usd`)을 넘으면 다음 회차를 시작하지 않는다. 키·승인 ID는 출력하지 않는다.
- 점검 모드(`--dry-run --outcomes …`): 러너·Docker·네트워크를 부르지 않고 결과를 차례로 흉내 내어 순서·무효 처리만 확인한다.
- 이어 돌리기(`--resume`): 멈춘 사유를 고친 뒤 같은 세션 디렉터리의 `session-log.jsonl`에 남은 시도(완료·무효)를 다시 세고
  남은 칸부터 잇는다. 멈춤(대기 시한 등)·설정 오류 줄은 시도로 세지 않는다. 점검 모드 기록과 섞이면 입력 오류다.
기록: 세션 디렉터리의 `session-log.jsonl`(시도마다 한 줄 + 마지막 요약). 집계·채점은 7절(`session_runs.py`·`score.py`)이 한다.
종료 코드: 0 쪽마다 유효 N회 완료, 3 측정 실패(멈춤 무효 상한), 4 멈춤(시도 상한·설정 오류·대기 시한·결과 없음·컨테이너·망
남음·사용량 상한), 2 입력 오류.
"""

from __future__ import annotations

import argparse
import datetime
import json
import math
import os
import secrets
import signal
import subprocess
import sys
import time
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

sys.path.insert(0, str(Path(__file__).resolve().parent))

from runner_early_end import CONFIG_END_REASON, EARLY_END_NAME, PROVIDER_FAULT_END_REASON  # noqa: E402
from runner_fault import FAULT_STALL_REASON  # noqa: E402
from session_runs import FAULT_STALL_LIMIT_PER_SIDE, expected_order  # noqa: E402

SIDE_OF = {"B": "baseline", "O": "ours"}
LETTER_OF = {v: k for k, v in SIDE_OF.items()}
COMPLETE, INVALID = "complete", "invalid"
RESULT_NAME = "runner-result.json"
LOG_NAME = "session-log.jsonl"
EXIT_DONE, EXIT_INPUT, EXIT_FAILED, EXIT_STOPPED = 0, 2, 3, 4
DEFAULT_CONDITIONS = {"baseline": "원본@35b", "ours": "우리@35b"}


@dataclass
class Outcome:
    """회차 하나의 결과. status: complete | invalid | config_error | stop(대기 시한·결과 없음 등, reason에 사유)."""
    status: str
    reasons: list[str] = field(default_factory=list)
    run_id: str | None = None
    # 시도로 센 뒤 세션을 멈출 사유(회차 디렉터리가 생긴 회차의 대기 시한·결과 없음·컨테이너 남음 등)
    stop_after: str | None = None
    # 일찍 끝난 회차 분류(회차 디렉터리의 early-end.json 요약). 분류가 없으면 None.
    early_end: dict | None = None


# 쪽별 멈춤 상한에 같이 세는 무효 사유(고정 초안 §3·§26).
FAULT_LIMIT_REASONS = frozenset({FAULT_STALL_REASON, PROVIDER_FAULT_END_REASON})


def config_end(outcome: Outcome) -> bool:
    """설정 오류로 끝난 회차인지(첫 사유). 시도로 세지 않는다."""
    return outcome.status == INVALID and bool(outcome.reasons) and outcome.reasons[0] == CONFIG_END_REASON


@dataclass
class Schedule:
    """§5.1 교대 순서와 무효 처리(순수 함수 부분). next_slot()이 None이면 끝(완료 또는 멈춤, stop_reason)."""
    repeats: int
    fault_limit: int = FAULT_STALL_LIMIT_PER_SIDE
    order: list[str] = field(init=False)
    position: int = 0
    attempts: dict = field(default_factory=lambda: {"baseline": 0, "ours": 0})
    valid: dict = field(default_factory=lambda: {"baseline": 0, "ours": 0})
    stalls: dict = field(default_factory=lambda: {"baseline": 0, "ours": 0})
    early_end_runs: list = field(default_factory=list)
    stop_reason: str | None = None
    failed: bool = False

    def __post_init__(self) -> None:
        if type(self.repeats) is not int or self.repeats < 1:
            raise ValueError("repeats")
        self.order = [SIDE_OF[letter] for letter in expected_order(self.repeats)]

    def next_slot(self) -> tuple[str, int] | None:
        if self.stop_reason is not None or self.position >= len(self.order):
            return None
        side = self.order[self.position]
        if self.attempts[side] >= 2 * self.repeats and self.valid[side] < self.repeats:
            self.stop_reason = f"attempt_limit:{side}"
            return None
        return side, self.valid[side]

    def record(self, side: str, outcome: Outcome) -> None:
        if outcome.status == "config_error":
            self.stop_reason = "runner_config_error"  # 시도로 세지 않는다(회차를 만들지 않음)
            return
        if outcome.status == "stop":
            self.stop_reason = outcome.reasons[0] if outcome.reasons else "stopped"
            return
        if outcome.early_end is not None:
            self.early_end_runs.append(dict(outcome.early_end, run_id=outcome.run_id, side=LETTER_OF[side],
                                            counted=not config_end(outcome)))
        if config_end(outcome):
            self.stop_reason = f"config_end:{side}"  # 시도로 세지 않는다(설정을 고친 뒤 같은 칸부터)
            return
        self.attempts[side] += 1
        if outcome.status == COMPLETE:
            self.valid[side] += 1
            self.position += 1
        elif any(reason in FAULT_LIMIT_REASONS for reason in outcome.reasons):
            self.stalls[side] += 1
            if self.stalls[side] >= self.fault_limit:
                self.stop_reason, self.failed = f"fault_stall_limit:{side}", True
        if outcome.stop_after is not None and self.stop_reason is None:
            self.stop_reason = outcome.stop_after

    def done(self) -> bool:
        return self.stop_reason is None and self.position >= len(self.order)

    def summary(self) -> dict:
        return {"repeats": self.repeats, "order": [LETTER_OF[s] for s in self.order], "attempts": self.attempts, "valid": self.valid,
                "fault_stalls": self.stalls, "complete": self.done(), "stop_reason": self.stop_reason, "measurement_failed": self.failed,
                "early_end_runs": self.early_end_runs}


# ── 실제 실행 ───────────────────────────────────────────────────────────────────────────────────────────────
def run_dirs(runs_root: Path) -> set[str]:
    return {p.name for p in runs_root.iterdir() if p.is_dir()} if runs_root.is_dir() else set()


def wait_process(proc: subprocess.Popen, timeout: float, grace: float) -> int | None:
    """PID로 기다린다. 시한을 넘으면 프로세스 그룹에 SIGINT → 유예 → SIGKILL을 보내고 None."""
    try:
        return proc.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        for sig, wait in ((signal.SIGINT, grace), (signal.SIGKILL, grace)):
            try:
                os.killpg(proc.pid, sig)
            except ProcessLookupError:
                return None
            try:
                proc.wait(timeout=wait)
                return None
            except subprocess.TimeoutExpired:
                continue
        return None


def running_containers() -> int | None:
    try:
        done = subprocess.run(["docker", "ps", "-q"], capture_output=True, text=True, timeout=60, check=False)
    except (OSError, subprocess.SubprocessError):
        return None
    return len(done.stdout.split()) if done.returncode == 0 else None


# 평가 회차 compose 프로젝트 이름의 앞부분(runner_config·runner_split_config: project = f"eval-{run_id}").
EVAL_PROJECT_PREFIX = "eval-"
_PROJECT_LABEL = "com.docker.compose.project"


def eval_networks() -> int | None:
    """평가 회차 compose 프로젝트가 만든 망 수(compose 프로젝트 라벨이 `eval-`로 시작). 읽지 못하면 None."""
    try:
        done = subprocess.run(["docker", "network", "ls", "--filter", f"label={_PROJECT_LABEL}",
                               "--format", '{{.Label "%s"}}' % _PROJECT_LABEL],
                              capture_output=True, text=True, timeout=60, check=False)
    except (OSError, subprocess.SubprocessError):
        return None
    if done.returncode != 0:
        return None
    return sum(1 for project in done.stdout.split() if project.startswith(EVAL_PROJECT_PREFIX))


def containers_stop(count: Callable[[], int | None], networks: Callable[[], int | None] = lambda: 0) -> str | None:
    """회차 시작 전 확인: 실행 중 컨테이너나 평가 compose 망이 남아 있으면 멈출 사유(시도로 세지 않음)."""
    present = count()
    if present is None:
        return "containers_unknown"
    if present != 0:
        return "containers_present"
    left = networks()
    if left is None:
        return "networks_unknown"
    return None if left == 0 else "networks_present"


def wait_containers_gone(count: Callable[[], int | None], timeout: float, interval: float,
                         sleep: Callable[[float], None] = time.sleep, clock: Callable[[], float] = time.monotonic) -> bool:
    deadline = clock() + timeout
    while True:
        if count() == 0:
            return True
        if clock() >= deadline:
            return False
        sleep(interval)


def key_usage(key_file: Path) -> dict:
    """OpenRouter 키 사용량(USD)과 남은 한도. 키는 출력하지 않는다."""
    key = key_file.read_text(encoding="utf-8").strip()
    req = urllib.request.Request("https://openrouter.ai/api/v1/key", headers={"Authorization": f"Bearer {key}"})
    with urllib.request.urlopen(req, timeout=30) as response:
        data = json.load(response)["data"]
    return {"usage": data.get("usage"), "limit_remaining": data.get("limit_remaining")}


def _usd(value: object) -> float | None:
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value) else None


@dataclass
class UsageGuard:
    """유료 회차 시작 전 키 사용량 확인. 세션 첫 확인의 사용량이 기준이다. 사용량·남은 한도를 읽지 못하거나 숫자가 아니면
    확인하지 못한 것이라 시작하지 않는다(#819 리뷰: 확인 못 한 채 유료 회차를 시작하지 않게)."""
    read: Callable[[], dict]
    cap_usd: float
    floor_usd: float
    base: float | None = None
    # 기준값을 처음 정할 때 부른다(세션 기록에 남겨 --resume 뒤에도 세션 전체 기준으로 건다)
    on_base: Callable[[float], None] = lambda base: None

    def check(self) -> str | None:
        try:
            now = self.read()
        except Exception:
            return "usage_read_failed"
        usage = _usd(now.get("usage")) if isinstance(now, dict) else None
        remaining = _usd(now.get("limit_remaining")) if isinstance(now, dict) else None
        if usage is None:
            return "usage_unknown"
        if remaining is None:
            return "remaining_unknown"  # 한도 없는 키·응답 이상: 하한을 확인할 수 없다
        if self.base is None:
            self.base = usage
            self.on_base(usage)
        if usage - self.base >= self.cap_usd:
            return "usage_cap"
        if remaining <= self.floor_usd:
            return "remaining_floor"
        return None


def write_manifest(template: Path, target: Path, hours: int) -> None:
    """O 회차마다 새 승인 ID·만료 시각으로 manifest를 만든다(0600, 덮어씀). 값은 출력하지 않는다."""
    value = json.loads(template.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError("manifest_template")
    value["approval_id"] = "official-o-" + secrets.token_hex(8)
    value["expires_at"] = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=hours)).replace(
        microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")
    if target.exists():
        target.unlink()
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        json.dump(value, f, indent=2)


_EARLY_END_KEYS = ("class", "reason", "first_limit", "end_state", "reason_code", "evidence")


def early_end_summary(path: Path) -> dict | None:
    """회차 디렉터리의 early-end.json에서 분류가 있는 경우만 요약(분류·사유·끝 상태·근거)을 돌려준다. 없거나 못 읽으면 None
    (못 읽음은 러너 결과의 사유가 이미 무효로 남긴다)."""
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    if not isinstance(value, dict) or value.get("class") is None:
        return None
    return {key: value.get(key) for key in _EARLY_END_KEYS}


@dataclass
class RealRunner:
    """러너 한 회차를 띄우고 PID·완료 파일로 기다린다."""
    tree: Path
    config: Path
    preflight: Path
    runs_root: Path
    admin_password_file: Path | None
    conditions: dict
    run_timeout: float
    grace: float
    post_timeout: float
    log_dir: Path

    def __call__(self, side: str, repeat: int) -> Outcome:
        before = run_dirs(self.runs_root)
        args = [sys.executable, "-I", "-B", str(self.tree / "scripts/eval/runner.py"), "--config", str(self.config),
                "--preflight", str(self.preflight), "--side", side, "--condition", self.conditions[side], "--repeat", str(repeat)]
        if side == "baseline":
            args += ["--admin-password-file", str(self.admin_password_file)]
        stem = self.log_dir / f"{LETTER_OF[side]}{repeat}-{int(time.time())}"
        with open(f"{stem}.stdout", "w") as out, open(f"{stem}.stderr", "w") as err:
            proc = subprocess.Popen(args, cwd=self.tree, stdout=out, stderr=err, start_new_session=True)
            code = wait_process(proc, self.run_timeout, self.grace)
        slot = f"{LETTER_OF[side]}{repeat}"
        new = sorted(run_dirs(self.runs_root) - before)
        outcome = self._judge(slot, code, new[-1] if new else None)
        # 회차가 어떻게 끝나든 컨테이너가 내려갈 때까지 기다린다. 안 내려가면 멈추고 사유에 같이 적는다.
        left = None
        if outcome.status != "config_error":
            if not wait_containers_gone(running_containers, self.post_timeout, 5):
                left = f"containers_left:{slot}"
            elif not wait_containers_gone(eval_networks, self.post_timeout, 5):
                left = f"networks_left:{slot}"  # 다음 회차 망과 대역이 겹치지 않게 앞 회차 망이 다 지워질 때까지
        if left is not None:
            if outcome.status == "stop":
                outcome.reasons.append(left)
            else:
                outcome.stop_after = left if outcome.stop_after is None else f"{outcome.stop_after},{left}"
        return outcome

    def _judge(self, slot: str, code: int | None, run_id: str | None) -> Outcome:
        if run_id is None:
            # 회차 디렉터리가 안 생겼다: 회차를 만들지 않은 것이라 시도로 세지 않는다
            if code == 2:
                return Outcome("config_error", ["runner_exit_2"])
            return Outcome("stop", [f"run_wait_timeout:{slot}" if code is None else f"no_run_directory:{slot}"])
        if code is None:
            reason = f"run_wait_timeout:{slot}"
            return Outcome(INVALID, [reason], run_id, stop_after=reason)
        result = self.runs_root / run_id / RESULT_NAME
        if not result.is_file():
            reason = f"result_missing:{slot}"
            return Outcome(INVALID, [reason], run_id, stop_after=reason)
        try:
            value = json.loads(result.read_text(encoding="utf-8"))
            status, reasons = value.get("status"), list(value.get("reasons") or [])
        except (OSError, ValueError, AttributeError, TypeError):
            reason = f"result_unreadable:{slot}"
            return Outcome(INVALID, [reason], run_id, stop_after=reason)
        if status not in (COMPLETE, INVALID) or (code == 0) != (status == COMPLETE):
            reason = f"result_status_mismatch:{slot}"
            return Outcome(INVALID, [reason] + [r for r in reasons if isinstance(r, str)], run_id, stop_after=reason)
        return Outcome(status, reasons, run_id, early_end=early_end_summary(self.runs_root / run_id / EARLY_END_NAME))


def run_session(schedule: Schedule, run_one: Callable[[str, int], Outcome], log: Callable[[dict], None],
                before_run: Callable[[str, int], str | None] = lambda side, repeat: None) -> Schedule:
    """칸마다 before_run(멈출 사유 또는 None) → run_one → 일정 기록. 끝나거나 멈추면 돌려준다."""
    while (slot := schedule.next_slot()) is not None:
        side, repeat = slot
        stop = before_run(side, repeat)
        if stop is not None:
            schedule.record(side, Outcome("stop", [stop]))
            log({"event": "stopped_before_run", "side": LETTER_OF[side], "repeat": repeat, "reason": stop})
            break
        started = datetime.datetime.now(datetime.timezone.utc).isoformat()
        outcome = run_one(side, repeat)
        schedule.record(side, outcome)
        log({"event": "attempt", "side": LETTER_OF[side], "repeat": repeat, "status": outcome.status, "reasons": outcome.reasons,
             "run_id": outcome.run_id, "stop_after": outcome.stop_after, "early_end": outcome.early_end, "started_at": started,
             "attempts": dict(schedule.attempts), "valid": dict(schedule.valid)})
    log(dict(event="summary", **schedule.summary()))
    return schedule


def replay(schedule: Schedule, lines: list[dict]) -> int:
    """세션 기록의 시도 줄(완료·무효)을 일정에 다시 넣는다. 넣은 수를 돌려준다. 기록 순서가 일정과 어긋나면 ValueError."""
    count = 0
    for line in lines:
        if line.get("event") != "attempt" or line.get("status") not in (COMPLETE, INVALID):
            continue
        if line.get("dry_run"):
            raise ValueError("resume_dry_run_log")
        outcome = Outcome(line["status"], list(line.get("reasons") or []), line.get("run_id"),
                          early_end=line.get("early_end") if isinstance(line.get("early_end"), dict) else None)
        slot = schedule.next_slot()
        side = SIDE_OF.get(line.get("side"))
        # 설정 오류 줄도 그때의 칸(쪽·번호)과 맞아야 한다. 시도로 세지 않을 뿐 순서 검사는 같다(#831 리뷰: 건너뛰면 다른 칸의
        # 줄이 끼어든 기록 오류를 숨긴다).
        if slot is None or slot != (side, line.get("repeat")):
            raise ValueError(f"resume_order:{line.get('side')}{line.get('repeat')}")
        if config_end(outcome):
            # 설정 오류 회차는 시도가 아니다. 목록에만 다시 넣고(검토용) 멈춤은 다시 걸지 않는다: 고친 뒤 잇는 것이다.
            if outcome.early_end is not None:
                schedule.early_end_runs.append(dict(outcome.early_end, run_id=outcome.run_id, side=line.get("side"), counted=False))
            continue
        # 시도 뒤 멈춤(stop_after)은 다시 걸지 않는다: 사유를 고친 뒤 잇는 것이다. 시도는 그대로 센다.
        schedule.record(side, outcome)
        count += 1
    return count


def parse_outcomes(text: str) -> list[Outcome]:
    """점검 모드 결과 목록: 쉼표로 나눈 `complete` · `invalid:<사유>[+<사유>…]` · `config_error` · `stop:<사유>`."""
    outcomes = []
    for item in [part.strip() for part in text.split(",") if part.strip()]:
        status, _, rest = item.partition(":")
        if status not in (COMPLETE, INVALID, "config_error", "stop") or (status == INVALID and not rest):
            raise ValueError(f"outcome: {item}")
        outcomes.append(Outcome(status, [r for r in rest.split("+") if r]))
    return outcomes


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="공식 회차 세션 실행(B O O B B O, 2N 규칙)")
    ap.add_argument("--repeats", type=int, default=3)
    ap.add_argument("--session-dir", type=Path, required=True)
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--resume", action="store_true", help="같은 세션 기록의 시도를 다시 세고 남은 칸부터 잇는다")
    ap.add_argument("--outcomes", default="", help="점검 모드에서 시도마다 차례로 쓸 결과(모자라면 complete)")
    ap.add_argument("--tree", type=Path, help="동결 작업 트리(러너 실행 위치)")
    ap.add_argument("--config", type=Path)
    ap.add_argument("--preflight", type=Path)
    ap.add_argument("--admin-password-file", type=Path)
    ap.add_argument("--run-timeout", type=float, default=3600, help="회차 하나 대기 시한(초)")
    ap.add_argument("--grace", type=float, default=120, help="시한 뒤 SIGINT·SIGKILL 유예(초)")
    ap.add_argument("--post-timeout", type=float, default=300, help="회차 뒤 컨테이너가 0이 되길 기다리는 시한(초)")
    ap.add_argument("--o-manifest-template", type=Path)
    ap.add_argument("--manifest-hours", type=int, default=6)
    ap.add_argument("--key-file", type=Path)
    ap.add_argument("--usage-cap-usd", type=float)
    ap.add_argument("--min-remaining-usd", type=float, default=0.30)
    a = ap.parse_args(argv)
    try:
        schedule = Schedule(a.repeats)
        outcomes = parse_outcomes(a.outcomes) if a.dry_run else []
    except ValueError as exc:
        print(f"입력 오류: {exc}", file=sys.stderr)
        return EXIT_INPUT
    a.session_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    log_path = a.session_dir / LOG_NAME
    resumed_base: float | None = None
    if log_path.exists() and not a.resume:
        print("입력 오류: 세션 기록이 이미 있음(이어 돌리려면 --resume)", file=sys.stderr)
        return EXIT_INPUT
    if a.resume:
        try:
            lines = [json.loads(l) for l in log_path.read_text(encoding="utf-8").splitlines() if l.strip()] if log_path.exists() else []
            if a.dry_run:
                raise ValueError("resume_with_dry_run")
            replayed = replay(schedule, lines)
            bases = [l.get("usage_usd") for l in lines if l.get("event") == "usage_base"]
            resumed_base = _usd(bases[0]) if bases else None
            if bases and resumed_base is None:
                raise ValueError("resume_usage_base_invalid")
        except (ValueError, OSError) as exc:
            print(f"입력 오류: {exc}", file=sys.stderr)
            return EXIT_INPUT
        if schedule.stop_reason is not None:
            # 기록만으로 이미 멈춘 세션(시도 상한·멈춤 무효 상한)은 잇지 않는다
            print(json.dumps(dict(event="summary", resumed=replayed, **schedule.summary()), ensure_ascii=False))
            return EXIT_FAILED if schedule.failed else EXIT_STOPPED

    def log(entry: dict) -> None:
        entry = dict(entry, dry_run=a.dry_run, resumed=a.resume, at=datetime.datetime.now(datetime.timezone.utc).isoformat())
        with open(log_path, "a", encoding="utf-8") as f:
            f.write(json.dumps(entry, ensure_ascii=False) + "\n")
        print(json.dumps(entry, ensure_ascii=False))

    if a.dry_run:
        pending = list(outcomes)
        run_session(schedule, lambda side, repeat: pending.pop(0) if pending else Outcome(COMPLETE), log)
    else:
        missing = [n for n in ("tree", "config", "preflight", "admin_password_file") if getattr(a, n) is None]
        if missing:
            print(f"입력 오류: 필요한 칸 {missing}", file=sys.stderr)
            return EXIT_INPUT
        if (a.key_file is None) != (a.usage_cap_usd is None):
            print("입력 오류: --key-file과 --usage-cap-usd는 같이 준다", file=sys.stderr)
            return EXIT_INPUT
        config = json.loads(a.config.read_text(encoding="utf-8"))
        runs_root = Path(config["runs_root"])
        manifest_path = Path(config["split"]["manifest_path"]) if isinstance(config.get("split"), dict) else None
        guard = None
        if a.key_file:
            guard = UsageGuard(lambda: key_usage(a.key_file), a.usage_cap_usd, a.min_remaining_usd,
                               on_base=lambda base: log({"event": "usage_base", "usage_usd": base}))
            if resumed_base is not None:
                guard.base = resumed_base

        def before_run(side: str, repeat: int) -> str | None:
            # 시도로 세지 않는다(남은 컨테이너가 격리 검사를 깨거나 남은 망이 대역을 막아 시도가 날아가지 않게)
            stop = containers_stop(running_containers, eval_networks)
            if stop is not None:
                return stop
            if guard is not None:
                stop = guard.check()
                if stop is not None:
                    return stop
            if side == "ours" and a.o_manifest_template is not None:
                if manifest_path is None:
                    return "manifest_path_missing"
                try:
                    write_manifest(a.o_manifest_template, manifest_path, a.manifest_hours)
                except (OSError, ValueError):
                    return "manifest_write_failed"
            return None

        runner = RealRunner(a.tree, a.config, a.preflight, runs_root, a.admin_password_file, DEFAULT_CONDITIONS, a.run_timeout,
                            a.grace, a.post_timeout, a.session_dir)
        run_session(schedule, runner, log, before_run)
    if schedule.done():
        return EXIT_DONE
    return EXIT_FAILED if schedule.failed else EXIT_STOPPED


if __name__ == "__main__":
    sys.exit(main())
