"""공식 회차 세션 정리 — 세션 runs_root 하나를 읽어 시도·무효 집계와 채점 manifest를 만든다(평가 프로토콜 §5.1).

실행: python3 -I -B scripts/eval/session_runs.py <runs_root> --repeats N --runner-key-file <러너 키> [--manifest <새 파일>]

- 완료 회차는 `runner-result.json`과 `run-record.json`의 러너 서명(HMAC)을 먼저 검증하고, 그 뒤에야 값(작업 요청
  해시·조건·반복 번호)을 쓴다. 서명이 맞지 않으면 집계를 멈춘다(채점기는 회차 기록만 검증하므로 결과 파일은 여기서 본다).
  무효 결과는 러너가 서명하지 않는다.
- 회차 디렉터리마다 `runner-result.json`(완료·무효 모두 있음)을 읽는다. 형식이 틀린 결과는 집계를 멈춘다(조용히 빼지 않음).
  결과 파일이 없는 디렉터리(러너가 강제 종료된 시도)는 `result_missing` 무효 시도로 센다.
- 쪽별 시도·유효·무효(사유별)·무효율과 시작 시각 순서를 출력한다. 유효 회차 순서가 §5.1 교대 순서(N=3이면 B O O B B O)와
  다르거나, 쪽별 시도가 2N을 넘거나, 유효가 N개 미만이면 경고한다(stderr `경고[...]`).
- 완료 회차들의 작업 생성 요청 해시(러너 결과 `task_request_sha256`)가 하나가 아니면 멈춘다 — 작업 문구도 비교 조건이다.
  탐색 분리 배치 세션은 쪽마다 하나다: B는 엔진 작업 생성 본문, O는 제품 설치·등록 조건 지문이라 서로 다르고, B·O 목표
  동일성은 채점기가 서명된 공통 목표 SHA로 본다. 분리 배치 회차와 엔진 직접 배치 회차가 섞이면 멈춘다.
- 완료 회차만 manifest에 넣는다. 값은 서명된 `run-record.json`에서 그대로 옮기고, 서명 검증은 채점기(score.py)가 한다.
  판정 레코드가 없는 회차(records=null)의 `records_path`는 채점기가 읽지 않는다.
- 제공자 장애 멈춤(결정안 §7): 완료 회차는 모두 같은 T로 판정한 서명 결과(`fault_stall.judged`, `stall_seconds`)여야 한다(T가
  회차·쪽마다 다르거나 판정하지 않은 회차가 있으면 멈춘다, 스모크는 `--allow-unjudged-fault-stall`). 쪽별
  `provider_fault_stall` 무효 수를 0이어도 늘 출력하고, 한쪽이라도 상한(고정 초안 §3) 이상이면 측정 전체 실패다(manifest를
  쓰지 않는다). 이 상한은 2N 시도 상한(§5.1)과 따로 둔다(고정 초안 §0). 러너 상한 전에 제공자 장애로 끝난 회차
  (`provider_fault_end`, 평가 프로토콜 §5.1·고정 초안 §26)도 같은 부류라 같은 상한에 같이 센다.
- 설정 오류로 끝난 회차(`config_end`)는 시도로 세지 않는다(쪽별 줄의 시도·무효율에서 빼고 따로 적는다).
- 일찍 끝난 회차 규칙이 걸린 회차(제공자 장애 끝·설정 오류·그 밖에 일찍 끝나 유효로 둔 회차)를 회차 ID·끝 상태·사유·근거
  호출로 따로 목록에 적는다(회차 디렉터리의 `early-end.json`, 측정 뒤 사람이 하나씩 검토한다).
- 회차별 모델 호출 429(제공자 요청 한도) 표: 시도마다 전달한 모델 호출 수·429 수·비율을 적는다(회차의 `llm-summary.json`).
  완료 회차는 서명된 근거 목록(`evidence`)의 해시와 파일이 맞을 때만 값을 쓰고, 맞지 않거나 파일이 없으면 "—"다. 무효 회차는
  러너가 서명하지 않아 같은 파일 값을 "서명 밖"으로 표시해 적는다. 시간 상한 회차에서 429가 쓴 시간은 결과에 바로 닿아서
  (2026-11-26 NodeGoat 회차 2: 실제 동시 1에서 40%) 회차마다 남긴다. 집계·판정·manifest에는 쓰지 않는 보조 기록이다.

종료 코드: 0 집계·manifest 작성(경고가 있어도), 2 입력 오류(형식·중복·쪽 섞임·멈춤 판정 조건) 또는 manifest 파일이 이미 있음,
3 측정 실패(쪽별 멈춤 무효가 상한 이상, manifest를 쓰지 않음).
"""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import sys
from collections import Counter
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from aggregate import _optional_stat as optional_stat  # noqa: E402
from isolation_proof import E2_MODES, E2_SPLIT_PRODUCT  # noqa: E402
from run_record import RunRecordError, read_runner_key, sign_run_record  # noqa: E402
from runner import COMPLETE, INVALID, RECORD_NAME, RESULT_FORMAT, RESULT_NAME  # noqa: E402
from runner_early_end import CONFIG_END_REASON, EARLY_END_NAME, PROVIDER_FAULT_END_REASON  # noqa: E402
from runner_fault import FAULT_STALL_NAME, FAULT_STALL_REASON  # noqa: E402

SIDES = ("baseline", "ours")
SIDE_LETTER = {"baseline": "B", "ours": "O"}
# 러너 run_id: <쪽>-<UTC 시각 YYYYMMDDtHHMMSSz>-<16진 8자리>
RUN_ID = re.compile(r"^(baseline|ours)-(\d{8}t\d{6}z)-[0-9a-f]{8}$")
RESULT_MISSING = "result_missing"
SHA256 = re.compile(r"^[0-9a-f]{64}$")
SIGNATURE = re.compile(r"^[0-9a-f]{64}$")
# 판정 레코드가 없는 회차의 자리값 — 채점기는 records=null이면 이 경로를 읽지 않는다(runner.md "실행").
RECORDS_UNCOLLECTED = "records-uncollected.json"
# 쪽별 provider_fault_stall 무효가 이 수 이상이면 측정 전체 실패(고정 초안 §3·§16, 2026-11-19 사용자 동의). 공식 결과를 본 뒤
# 바꾸지 않는다(바꾸면 다음 측정부터).
FAULT_STALL_LIMIT_PER_SIDE = 2
MEASUREMENT_FAILED = 3
# 러너가 회차마다 쓰는 모델 호출 요약(runner.py·runner_split_run.py가 같은 이름으로 쓴다). forwarded_requests·rate_limited를 읽는다.
LLM_SUMMARY_NAME = "llm-summary.json"


class SessionError(Exception):
    """입력 오류. 사유를 전부 모아 담는다."""

    def __init__(self, errors: list[str]):
        super().__init__("; ".join(errors))
        self.errors = errors


@dataclass(frozen=True)
class Attempt:
    run_id: str
    side: str
    started_at: datetime
    status: str
    reason: str | None  # 무효의 첫 사유(러너 결과의 정렬 첫째)
    record: dict | None  # 완료 회차의 run-record.json
    task_sha256: str | None = None  # 완료 회차의 작업 생성 요청 본문 해시(러너 결과, 서명됨)
    result: dict | None = None  # 완료 회차의 서명 검증한 runner-result.json
    early_end: dict | None = None  # 일찍 끝난 회차 분류(early-end.json, 분류가 있을 때만)
    # 멈춤 무효 회차에서 멈춤으로 판정된 장애 사건 때 모델 호출이 진행 중이었는지(fault-stall.json v3 inflight_model_calls_at_fault).
    # 참 = 하나라도 진행 중, 거짓 = 모두 0, None = 모름(칸 없음·못 읽음). 판정·상한 셈에는 쓰지 않는 공정성 기록이다(2026-11-22 사용자)
    stall_inflight: bool | None = None
    # 회차의 모델 호출 429 기록(llm-summary.json). 못 읽거나 서명 근거와 맞지 않으면 None — 표에서 "—"다.
    rate_limit: RateLimit | None = None


@dataclass(frozen=True)
class RateLimit:
    calls: int  # 계측 게이트웨이가 제공자로 전달한 모델 호출 수(429 포함)
    limited: int  # 그중 제공자 쪽이 429로 답한 수
    signed: bool  # 서명된 완료 결과의 근거 목록 해시와 맞췄으면 참. 무효 회차는 늘 거짓(서명 밖)


def _rate_limit(directory: Path, result: dict | None) -> RateLimit | None:
    """회차의 llm-summary.json에서 전달 호출·429 수. 완료 결과(result)가 있으면 그 서명된 근거 해시와 파일이 같을 때만 읽는다.
    result가 None이면(무효·결과 없음) 서명 밖 값으로 읽는다. 형식이 틀리면 None이다(0으로 세지 않음)."""
    path = directory / LLM_SUMMARY_NAME
    try:
        raw = path.read_bytes()
    except OSError:
        return None
    if result is not None:
        evidence = result.get("evidence")
        expected = evidence.get(LLM_SUMMARY_NAME) if isinstance(evidence, dict) else None
        if not isinstance(expected, str) or not SHA256.match(expected) or hashlib.sha256(raw).hexdigest() != expected:
            return None
    try:
        summary = json.loads(raw)
    except ValueError:
        return None
    if not isinstance(summary, dict):
        return None
    calls, limited = summary.get("forwarded_requests"), summary.get("rate_limited")
    if (type(calls) is not int or type(limited) is not int or calls < 0 or limited < 0 or limited > calls):
        return None
    return RateLimit(calls, limited, result is not None)


def expected_order(repeats: int) -> list[str]:
    """§5.1 교대 순서: 짝마다 먼저 도는 쪽을 바꾼다(B O, O B, B O, …)."""
    order: list[str] = []
    for index in range(repeats):
        order += ["B", "O"] if index % 2 == 0 else ["O", "B"]
    return order


def _load_json(path: Path, errors: list[str]) -> dict | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        errors.append(f"{path.parent.name}: {path.name} 읽기·형식 오류")
        return None
    if not isinstance(value, dict):
        errors.append(f"{path.parent.name}: {path.name}이 객체가 아님")
        return None
    return value


def _stamp(value: object) -> datetime | None:
    if not isinstance(value, str):
        return None
    try:
        moment = datetime.fromisoformat(value)
    except ValueError:
        return None
    return moment if moment.tzinfo is not None else None


def _signed(value: dict, key: bytes) -> bool:
    """러너가 sign_run_record로 붙인 HMAC이 맞는지. 채점기(run_record)와 같은 계산이다."""
    signature = value.get("signature")
    if not isinstance(signature, str) or not SIGNATURE.match(signature):
        return False
    try:
        expected = sign_run_record({k: v for k, v in value.items() if k != "signature"}, key)["signature"]
    except RunRecordError:
        return False
    return hmac.compare_digest(signature, expected)


def _attempt(directory: Path, key: bytes, errors: list[str], allow_fixture_e2: bool = False) -> Attempt | None:
    match = RUN_ID.match(directory.name)
    if not match:
        errors.append(f"{directory.name}: 러너 회차 디렉터리 이름이 아님")
        return None
    side = match.group(1)
    result_path = directory / RESULT_NAME
    if not result_path.exists():
        started = datetime.strptime(match.group(2), "%Y%m%dt%H%M%Sz").replace(tzinfo=timezone.utc)
        return Attempt(directory.name, side, started, INVALID, RESULT_MISSING, None, rate_limit=_rate_limit(directory, None))
    result = _load_json(result_path, errors)
    if result is None:
        return None
    # 완료라고 적힌 결과는 서명부터 본다. 서명 전에는 그 안의 값을 믿지 않는다.
    if result.get("status") == COMPLETE and not _signed(result, key):
        errors.append(f"{directory.name}: {RESULT_NAME} 서명 불일치")
        return None
    started = _stamp(result.get("started_at"))
    status = result.get("status")
    if (result.get("format") != RESULT_FORMAT or result.get("run_id") != directory.name or result.get("side") != side
            or started is None or status not in (COMPLETE, INVALID)):
        errors.append(f"{directory.name}: {RESULT_NAME}의 형식·run_id·side·started_at·status가 맞지 않음")
        return None
    if status == INVALID:
        reasons = result.get("reasons")
        if not isinstance(reasons, list) or not reasons or not all(isinstance(r, str) and r for r in reasons):
            errors.append(f"{directory.name}: 무효 결과에 사유가 없음")
            return None
        return Attempt(directory.name, side, started, INVALID, reasons[0], None, early_end=_early_end(directory),
                       stall_inflight=_stall_inflight(directory) if reasons[0] == FAULT_STALL_REASON else None,
                       rate_limit=_rate_limit(directory, None))
    # 서명된 E② 자리. 가짜 E²(stub) 회차는 배치 확인용이라 시범·공식 세션 집계에 넣지 않는다(프로토콜 §2).
    e2_mode = result.get("e2_mode")
    if e2_mode not in E2_MODES:
        errors.append(f"{directory.name}: 완료 결과의 e2_mode가 none|stub|product|split_product가 아님")
        return None
    if e2_mode == "stub" and not allow_fixture_e2:
        errors.append(f"{directory.name}: 가짜 E② 회차(e2_mode=stub)는 세션 집계에 넣지 않음 — 배치 확인용(프로토콜 §2), "
                      "스모크 확인이면 --allow-fixture-e2")
        return None
    task_sha256 = result.get("task_request_sha256")
    if not isinstance(task_sha256, str) or not SHA256.match(task_sha256):
        errors.append(f"{directory.name}: 완료 결과에 작업 요청 해시가 없음")
        return None
    record = _load_json(directory / RECORD_NAME, errors)
    if record is None:
        return None
    if not _signed(record, key):
        errors.append(f"{directory.name}: {RECORD_NAME} 서명 불일치")
        return None
    if (record.get("run_id") != directory.name or record.get("engine") != side
            or not isinstance(record.get("condition"), str) or not record["condition"]
            or not isinstance(record.get("repeat_index"), int) or isinstance(record.get("repeat_index"), bool)):
        errors.append(f"{directory.name}: {RECORD_NAME}의 run_id·engine·condition·repeat_index가 맞지 않음")
        return None
    artifacts = record.get("artifacts")
    if not isinstance(artifacts, dict) or not all(
            isinstance(artifacts.get(phase), dict) and isinstance(artifacts[phase].get("path"), str)
            for phase in ("pre", "post")):
        errors.append(f"{directory.name}: {RECORD_NAME}에 pre·post 스냅샷 경로가 없음")
        return None
    return Attempt(directory.name, side, started, COMPLETE, None, record, task_sha256, result, early_end=_early_end(directory),
                   rate_limit=_rate_limit(directory, result))


def _stall_seconds(attempt: Attempt) -> int | None:
    """서명된 완료 결과의 멈춤 판정 T. 판정하지 않은 결과면 None."""
    record = attempt.result.get("fault_stall") if attempt.result else None
    if not isinstance(record, dict) or record.get("judged") is not True:
        return None
    value = record.get("stall_seconds")
    return value if type(value) is int else None


def _split(attempt: Attempt) -> bool:
    """서명된 결과에서 탐색 분리 배치 회차인지 본다. O는 E② 자리(split_product), B는 실행 조건의 분리 배치 표시다."""
    result = attempt.result or {}
    conditions = result.get("run_conditions")
    return result.get("e2_mode") == E2_SPLIT_PRODUCT or (isinstance(conditions, dict) and conditions.get("split") is True)


def read_session(runs_root: Path, key: bytes, allow_fixture_e2: bool = False,
                 allow_unjudged_fault_stall: bool = False) -> list[Attempt]:
    """runs_root 바로 아래 회차 디렉터리를 시작 시각 순으로 읽는다. key는 러너 서명 키."""
    if not runs_root.is_dir():
        raise SessionError([f"{runs_root}: 디렉터리가 아님"])
    errors: list[str] = []
    attempts = [a for d in sorted(runs_root.iterdir())
                if d.is_dir() and (a := _attempt(d, key, errors, allow_fixture_e2)) is not None]
    complete = [a for a in attempts if a.record is not None]
    # 작업 설명·목표·옵션도 비교 조건이다. 같은 설정이면 요청 본문 해시가 같다(baseline·개선 후 공통).
    # 분리 배치 세션은 쪽마다 하나다(B 작업 생성 본문·O 설치·등록 조건 지문, 목표 동일성은 채점기가 공통 목표 SHA로 본다).
    splits = {_split(a) for a in complete}
    if len(splits) > 1:
        errors.append("분리 배치 회차와 엔진 직접 배치 회차가 섞임 — 한 세션은 한 배치로만")
    groups = [[a for a in complete if a.side == side] for side in SIDES] if splits == {True} else [complete]
    for group in groups:
        tasks = sorted({a.task_sha256 for a in group})
        if len(tasks) > 1:
            errors.append(f"작업 생성 요청이 회차마다 다름(해시 {len(tasks)}종) — 같은 runner.json task로만 비교")
    for side in SIDES:
        mine = [a.record for a in complete if a.side == side]
        conditions = {r["condition"] for r in mine}
        if len(conditions) > 1:
            errors.append(f"{side}: 조건 이름이 섞임 {sorted(conditions)}")
        repeats = Counter(r["repeat_index"] for r in mine)
        duplicated = sorted(k for k, n in repeats.items() if n > 1)
        if duplicated:
            errors.append(f"{side}: 같은 repeat_index의 완료 회차가 둘 이상 {duplicated}")
    # 멈춤 무효는 B·O 같은 T로 판정해야 비교가 된다(결정안 §7). 판정하지 않은 완료 회차는 멈춤 여부를 모르는 회차다.
    unjudged = sorted(a.run_id for a in complete if _stall_seconds(a) is None)
    if unjudged and not allow_unjudged_fault_stall:
        errors.append(f"멈춤 무효를 판정하지 않은 완료 회차 {unjudged} — 설정 run.fault_stall_seconds로 판정한 회차만 집계한다"
                      "(스모크면 --allow-unjudged-fault-stall)")
    stall_values = sorted({value for a in complete if (value := _stall_seconds(a)) is not None})
    if len(stall_values) > 1:
        errors.append(f"멈춤 무효 판정 T가 회차마다 다름 {stall_values} — B·O 같은 T로만 비교")
    if errors:
        raise SessionError(errors)
    return sorted(attempts, key=lambda a: (a.started_at, a.run_id))


def tally(attempts: list[Attempt], repeats: int) -> tuple[list[str], list[str]]:
    """출력 줄과 경고 줄."""
    lines = ["| 쪽 | 조건 | 시도 | 유효 | 무효 | 무효율 | 무효 사유 |", "|---|---|---|---|---|---|---|"]
    warnings: list[str] = []
    for side in SIDES:
        # 설정 오류로 끝난 회차는 시도가 아니다(고정 초안 §26). 시도·무효율에서 빼고 따로 적는다.
        config = sum(1 for a in attempts if a.side == side and a.reason == CONFIG_END_REASON)
        mine = [a for a in attempts if a.side == side and a.reason != CONFIG_END_REASON]
        valid = [a for a in mine if a.status == COMPLETE]
        invalid = Counter(a.reason for a in mine if a.status == INVALID)
        condition = valid[0].record["condition"] if valid else "—"
        rate = f"{100 * sum(invalid.values()) / len(mine):.0f}%" if mine else "—"
        reasons = ", ".join(f"{r} {n}" for r, n in sorted(invalid.items())) or "—"
        lines.append(f"| {side} | {condition} | {len(mine)} | {len(valid)} | {sum(invalid.values())} | {rate} | {reasons} |")
        if config:
            warnings.append(f"경고[설정 오류] {side}: {CONFIG_END_REASON} {config}회 — 시도로 세지 않았다. 설정을 고친 뒤 이어 돌린다")
        if len(mine) > 2 * repeats:
            warnings.append(f"경고[시도 상한] {side}: 시도 {len(mine)}회 > 2N({2 * repeats}) — 더 돌리지 말고 원인부터 고친다")
        if len(valid) < repeats:
            warnings.append(f"경고[유효 부족] {side}: 유효 {len(valid)}회 < N({repeats}) — 결과는 미완")
        zero = sum(1 for a in valid if a.record.get("total_requests") == 0)
        if zero:
            warnings.append(f"경고[대상 요청 0] {side}: 유효 회차 {zero}개가 대상 요청 없이 끝남 — 원인을 결과 문서에 적는다")
    marks = " ".join(SIDE_LETTER[a.side] + ("✓" if a.status == COMPLETE else "✗") for a in attempts)
    lines.append(f"시도 순서(시작 시각): {marks or '—'}")
    valid_order = [SIDE_LETTER[a.side] for a in attempts if a.status == COMPLETE]
    expected = expected_order(repeats)
    matches = valid_order == expected[: len(valid_order)] and len(valid_order) <= len(expected)
    lines.append(f"유효 순서: {' '.join(valid_order) or '—'} (기대 {' '.join(expected)}: {'일치' if matches else '불일치'})")
    if not matches:
        warnings.append("경고[교대 순서] 유효 회차 순서가 §5.1 교대 순서와 다름 — 결과 문서에 그대로 적는다")
    return lines, warnings


def fault_stall_tally(attempts: list[Attempt]) -> tuple[list[str], list[str]]:
    """쪽별 provider_fault_stall·provider_fault_end 무효 수(0이어도 늘)와 측정 실패 줄(둘의 합이 한쪽이라도 상한 이상)."""
    lines = ["| 쪽 | provider_fault_stall 무효 | provider_fault_end 무효 | 합 | 측정 실패 상한 | 멈춤 때 모델 호출 진행 중(모름) |",
             "|---|---|---|---|---|---|"]
    failures: list[str] = []
    for side in SIDES:
        stalls = sum(1 for a in attempts if a.side == side and a.status == INVALID and a.reason == FAULT_STALL_REASON)
        ends = sum(1 for a in attempts if a.side == side and a.status == INVALID and a.reason == PROVIDER_FAULT_END_REASON)
        count = stalls + ends
        stalled = [a for a in attempts if a.side == side and a.status == INVALID and a.reason == FAULT_STALL_REASON]
        inflight = sum(1 for a in stalled if a.stall_inflight is True)
        unknown = sum(1 for a in stalled if a.stall_inflight is None)
        lines.append(f"| {side} | {stalls} | {ends} | {count} | {FAULT_STALL_LIMIT_PER_SIDE} | {inflight} ({unknown}) |")
        if count >= FAULT_STALL_LIMIT_PER_SIDE:
            failures.append(f"측정 실패[멈춤 무효] {side}: {FAULT_STALL_REASON} {stalls}회 + {PROVIDER_FAULT_END_REASON} {ends}회 ≥ "
                            f"{FAULT_STALL_LIMIT_PER_SIDE} — 남은 회차를 골라 쓰지 않고 측정 전체를 실패로 둔다(결정안 §7, 고정 초안 §3)")
    return lines, failures


def _stall_inflight(directory: Path) -> bool | None:
    """멈춤 무효 회차의 fault-stall.json(러너 근거, 무효 결과는 서명이 없어 근거 파일을 그대로 읽는다)에서 멈춤 사건 때 진행 중인
    모델 호출이 있었는지. B는 스트리밍 조각을 활동으로 세지 않아 긴 스트리밍 호출 하나로 B만 멈춤 무효가 날 수 있어, 그 경우를
    공정성 기록에서 가른다. 칸이 없거나(v3 전) 숫자가 아니거나 못 읽으면 모름(None)이다."""
    try:
        record = json.loads((directory / FAULT_STALL_NAME).read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    events = record.get("events") if isinstance(record, dict) else None
    stalls = [e for e in events if isinstance(e, dict) and e.get("stall") is True] if isinstance(events, list) else []
    counts = [e.get("inflight_model_calls_at_fault") for e in stalls]
    if not stalls or any(type(c) is not int for c in counts):
        return None
    return any(c > 0 for c in counts)


def _early_end(directory: Path) -> dict | None:
    """회차 디렉터리의 early-end.json에서 분류가 있을 때만. 없거나 못 읽으면 None(못 읽음은 러너 결과 사유가 이미 무효로 남긴다)."""
    try:
        value = json.loads((directory / EARLY_END_NAME).read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    return value if isinstance(value, dict) and value.get("class") is not None else None


def _call_text(call: object) -> str:
    if not isinstance(call, dict):
        return "—"
    end = f" {call.get('unfinished_end')}" if call.get("unfinished_end") else ""
    return f"#{call.get('seq')} {call.get('decision')} {call.get('status')}{end} {call.get('completed_at')}"


def early_end_list(attempts: list[Attempt]) -> list[str]:
    """일찍 끝난 회차 규칙이 걸린 회차 목록(측정 뒤 사람이 하나씩 검토한다). 없으면 '없음' 한 줄."""
    picked = [a for a in attempts if a.early_end is not None]
    if not picked:
        return ["없음"]
    lines = ["| 회차 | 쪽 | 분류 | 사유 | 먼저 걸린 것 | 끝 상태 | 제품 사유 | 근거 |", "|---|---|---|---|---|---|---|---|"]
    for a in picked:
        e = a.early_end
        evidence = e.get("evidence") if isinstance(e.get("evidence"), dict) else {}
        kind = evidence.get("kind", "—")
        detail = _call_text(evidence["call"]) + (" (검열 가능: 403)" if evidence.get("moderation_possible") else "") if "call" in evidence else (
            evidence.get("code") or (f"요청 {evidence.get('requests')}/{evidence.get('max_requests')} · 비용 "
                                     f"{evidence.get('cost_usd_micros')}/{evidence.get('max_cost_usd_micros')}µ$"
                                     if kind == "approval_budget" else ""))
        lines.append(f"| {a.run_id} | {a.side} | {e.get('class')} | {e.get('reason') or '유효'} | {e.get('first_limit') or '—'} | "
                     f"{e.get('end_state') or '—'} | {e.get('reason_code') or '—'} | {kind} {detail} |".rstrip())
    return lines


def build_manifest(runs_root: Path, attempts: list[Attempt]) -> dict:
    """완료 회차만, 시작 시각 순. 경로는 회차 디렉터리 기준 절대경로."""
    runs = []
    for attempt in attempts:
        record = attempt.record
        if record is None:
            continue
        directory = (runs_root / attempt.run_id).resolve()
        artifacts = record.get("artifacts") or {}
        run = {key: record.get(key) for key in ("condition", "engine", "run_id", "model_tag", "reset_generation",
                                                "repeat_index", "total_requests", "blocked_attempts",
                                                "elapsed_seconds")}
        run.update(
            run_record_path=str(directory / RECORD_NAME),
            pre_snapshot_path=str(directory / artifacts["pre"]["path"]),
            snapshot_path=str(directory / artifacts["post"]["path"]),
            records_path=str(directory / (artifacts["records"]["path"] if artifacts.get("records")
                                          else RECORDS_UNCOLLECTED)),
        )
        runs.append(run)
    return {"runs": runs}


def _engine_seconds(record: dict) -> float | None:
    """서명된 기록의 작업 생성~엔진 종료 시간. 시각을 못 읽으면 None이고 표에서 "—"가 된다."""
    try:
        start = datetime.fromisoformat(record["timeline"]["engine_start"]["at"].replace("Z", "+00:00"))
        end = datetime.fromisoformat(record["timeline"]["engine_end"]["at"].replace("Z", "+00:00"))
        return (end - start).total_seconds()
    except (KeyError, TypeError, AttributeError, ValueError):
        return None


def auxiliary(attempts: list[Attempt]) -> list[str]:
    """§5 보조 지표(유효 회차, 쪽별). 값이 빠진 회차는 0으로 세지 않아 그 칸이 "—"가 된다."""
    lines = ["| 쪽 | 유효 회차 | 종료 사유 | 예산 초과분 | 구간 소요 시간(초) |", "|---|---|---|---|---|"]
    for side in SIDES:
        valid = [a for a in attempts if a.side == side and a.status == COMPLETE]
        meters = [meter if isinstance(meter := a.result.get("meter"), dict) else {} for a in valid]
        # 분리 배치 O는 계측기 요약(meter)이 없고 종료 사유를 회차 요약 맨 위(stop_reason, runner_split_run._finish_split_summary)에
        # 남긴다(2026-11-23 사용자: '미기록'으로 나오던 결과 도구 버그). 예산 초과분은 O 요약에 칸이 없어 모름("—")으로 둔다.
        stops = Counter(str(meter.get("stop_reason", a.result.get("stop_reason", "미기록")) if meter else
                            a.result.get("stop_reason", "미기록")) for a, meter in zip(valid, meters))
        reasons = ", ".join(f"{reason} {count}" for reason, count in sorted(stops.items())) or "—"
        over = [value if type(value := meter.get("over_budget")) is int else None for meter in meters]
        seconds = [_engine_seconds(a.record) for a in valid]
        lines.append(f"| {side} | {len(valid)} | {reasons} | {optional_stat(over).render()} "
                     f"| {optional_stat(seconds).render()} |")
    return lines


def _ratio(limited: int, calls: int) -> str:
    return f"{100 * limited / calls:.1f}%" if calls else "—"


def rate_limit_table(attempts: list[Attempt]) -> list[str]:
    """회차별 모델 호출 429 표(시작 순서, 무효·결과 없음 포함)와 쪽별 유효 회차 합. 값이 없는 회차는 "—"이고 합에서 빠진다."""
    lines = ["| 순서 | 쪽 | 회차 | 시작(UTC) | 결과 | 모델 호출 | 429 | 429 비율 |", "|---|---|---|---|---|---|---|---|"]
    for index, attempt in enumerate(sorted(attempts, key=lambda a: (a.started_at, a.run_id)), 1):
        outcome = "유효" if attempt.status == COMPLETE else f"무효 `{attempt.reason}`"
        rl = attempt.rate_limit
        if rl is None:
            values = "— | — | —"
        else:
            mark = "" if rl.signed else " (서명 밖)"
            values = f"{rl.calls} | {rl.limited} | {_ratio(rl.limited, rl.calls)}{mark}"
        lines.append(f"| {index} | {SIDE_LETTER[attempt.side]} | `{attempt.run_id}` | {attempt.started_at.strftime('%Y-%m-%d %H:%M')} "
                     f"| {outcome} | {values} |")
    lines += ["", "| 쪽 | 유효 회차 | 429 기록 있는 유효 회차 | 모델 호출 합 | 429 합 | 429 비율 | 회차별 비율 최대 |", "|---|---|---|---|---|---|---|"]
    for side in SIDES:
        valid = [a for a in attempts if a.side == side and a.status == COMPLETE]
        known = [a.rate_limit for a in valid if a.rate_limit is not None]
        calls, limited = sum(r.calls for r in known), sum(r.limited for r in known)
        worst = max((r.limited / r.calls for r in known if r.calls), default=None)
        lines.append(f"| {SIDE_LETTER[side]} | {len(valid)} | {len(known)} | {calls if known else '—'} | {limited if known else '—'} "
                     f"| {_ratio(limited, calls) if known else '—'} | {'—' if worst is None else f'{100 * worst:.1f}%'} |")
    return lines


def write_new(path: Path, payload: dict) -> None:
    """새 파일로만 쓴다(덮어쓰기 거부)."""
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o644)
    with os.fdopen(fd, "w", encoding="utf-8") as out:
        json.dump(payload, out, ensure_ascii=False, indent=1)
        out.write("\n")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="공식 회차 세션의 시도·무효 집계와 채점 manifest (평가 프로토콜 §5.1)")
    parser.add_argument("runs_root", help="이 세션만 담은 러너 runs_root")
    parser.add_argument("--repeats", type=int, required=True, help="조건당 유효 회차 수 N(§5.1, 3 이상)")
    parser.add_argument("--runner-key-file", required=True, help="러너 서명 키 파일(채점에 쓰는 것과 같은 키)")
    parser.add_argument("--manifest", help="완료 회차 채점 manifest를 쓸 새 파일(있으면 거부)")
    parser.add_argument("--allow-fixture-e2", action="store_true",
                        help="가짜 E②(e2_mode=stub) 회차도 읽는다 — 배치 스모크 확인용, 시범·공식 세션에는 쓰지 않음")
    parser.add_argument("--allow-unjudged-fault-stall", action="store_true",
                        help="멈춤 무효를 판정하지 않은 완료 회차도 읽는다 — 스모크·T 확정 전 확인용, 공식 세션에는 쓰지 않음")
    args = parser.parse_args(argv)
    if args.repeats < 3:
        print("--repeats는 3 이상이다(§5.1)", file=sys.stderr)
        return 2
    runs_root = Path(args.runs_root)
    try:
        key = read_runner_key(args.runner_key_file)
    except (OSError, RunRecordError) as error:
        print(f"러너 키를 읽지 못함: {error}", file=sys.stderr)
        return 2
    try:
        attempts = read_session(runs_root, key, args.allow_fixture_e2, args.allow_unjudged_fault_stall)
        payload = build_manifest(runs_root, attempts)
    except SessionError as error:
        print(f"세션 읽기 중단: {error}", file=sys.stderr)
        return 2
    lines, warnings = tally(attempts, args.repeats)
    print("\n".join(lines))
    print("\n".join(["", "보조 지표(프로토콜 §5, 효율 주장의 근거로 쓰지 않음):", *auxiliary(attempts)]))
    for warning in warnings:
        print(warning, file=sys.stderr)
    print("\n".join(["", "회차별 모델 호출 429(제공자 요청 한도, 보조 기록 — 집계·판정에 쓰지 않음):", *rate_limit_table(attempts)]))
    stall_lines, failures = fault_stall_tally(attempts)
    print("\n".join(["", "제공자 장애 무효(멈춤·일찍 끝남, 결정안 §7, 0이어도 늘):", *stall_lines]))
    print("\n".join(["", "일찍 끝난 회차 규칙이 걸린 회차(평가 프로토콜 §5.1·고정 초안 §26, 측정 뒤 하나씩 검토):", *early_end_list(attempts)]))
    if failures:
        for failure in failures:
            print(failure, file=sys.stderr)
        return MEASUREMENT_FAILED  # 측정 전체 실패: 채점 manifest를 쓰지 않는다
    if args.manifest:
        try:
            write_new(Path(args.manifest), payload)
        except FileExistsError:
            print(f"{args.manifest}: 이미 있음 — 덮어쓰지 않는다", file=sys.stderr)
            return 2
        print(f"manifest: 완료 회차 {len(payload['runs'])}개 → {args.manifest}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
