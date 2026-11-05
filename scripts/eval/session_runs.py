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
- 완료 회차만 manifest에 넣는다. 값은 서명된 `run-record.json`에서 그대로 옮기고, 서명 검증은 채점기(score.py)가 한다.
  판정 레코드가 없는 회차(records=null)의 `records_path`는 채점기가 읽지 않는다.

종료 코드: 0 집계·manifest 작성(경고가 있어도), 2 입력 오류(형식·중복·쪽 섞임) 또는 manifest 파일이 이미 있음.
"""

from __future__ import annotations

import argparse
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

from isolation_proof import E2_MODES  # noqa: E402
from run_record import RunRecordError, read_runner_key, sign_run_record  # noqa: E402
from runner import COMPLETE, INVALID, RECORD_NAME, RESULT_FORMAT, RESULT_NAME  # noqa: E402

SIDES = ("baseline", "ours")
SIDE_LETTER = {"baseline": "B", "ours": "O"}
# 러너 run_id: <쪽>-<UTC 시각 YYYYMMDDtHHMMSSz>-<16진 8자리>
RUN_ID = re.compile(r"^(baseline|ours)-(\d{8}t\d{6}z)-[0-9a-f]{8}$")
RESULT_MISSING = "result_missing"
SHA256 = re.compile(r"^[0-9a-f]{64}$")
SIGNATURE = re.compile(r"^[0-9a-f]{64}$")
# 판정 레코드가 없는 회차의 자리값 — 채점기는 records=null이면 이 경로를 읽지 않는다(runner.md "실행").
RECORDS_UNCOLLECTED = "records-uncollected.json"


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
        return Attempt(directory.name, side, started, INVALID, RESULT_MISSING, None)
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
        return Attempt(directory.name, side, started, INVALID, reasons[0], None)
    # 서명된 E② 자리. 가짜 E²(stub) 회차는 배치 확인용이라 시범·공식 세션 집계에 넣지 않는다(프로토콜 §2).
    e2_mode = result.get("e2_mode")
    if e2_mode not in E2_MODES:
        errors.append(f"{directory.name}: 완료 결과의 e2_mode가 none|stub이 아님")
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
    return Attempt(directory.name, side, started, COMPLETE, None, record, task_sha256)


def read_session(runs_root: Path, key: bytes, allow_fixture_e2: bool = False) -> list[Attempt]:
    """runs_root 바로 아래 회차 디렉터리를 시작 시각 순으로 읽는다. key는 러너 서명 키."""
    if not runs_root.is_dir():
        raise SessionError([f"{runs_root}: 디렉터리가 아님"])
    errors: list[str] = []
    attempts = [a for d in sorted(runs_root.iterdir())
                if d.is_dir() and (a := _attempt(d, key, errors, allow_fixture_e2)) is not None]
    complete = [a for a in attempts if a.record is not None]
    # 작업 설명·목표·옵션도 비교 조건이다. 같은 설정이면 요청 본문 해시가 같다(baseline·개선 후 공통).
    tasks = sorted({a.task_sha256 for a in complete})
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
    if errors:
        raise SessionError(errors)
    return sorted(attempts, key=lambda a: (a.started_at, a.run_id))


def tally(attempts: list[Attempt], repeats: int) -> tuple[list[str], list[str]]:
    """출력 줄과 경고 줄."""
    lines = ["| 쪽 | 조건 | 시도 | 유효 | 무효 | 무효율 | 무효 사유 |", "|---|---|---|---|---|---|---|"]
    warnings: list[str] = []
    for side in SIDES:
        mine = [a for a in attempts if a.side == side]
        valid = [a for a in mine if a.status == COMPLETE]
        invalid = Counter(a.reason for a in mine if a.status == INVALID)
        condition = valid[0].record["condition"] if valid else "—"
        rate = f"{100 * sum(invalid.values()) / len(mine):.0f}%" if mine else "—"
        reasons = ", ".join(f"{r} {n}" for r, n in sorted(invalid.items())) or "—"
        lines.append(f"| {side} | {condition} | {len(mine)} | {len(valid)} | {sum(invalid.values())} | {rate} | {reasons} |")
        if len(mine) > 2 * repeats:
            warnings.append(f"경고[시도 상한] {side}: 시도 {len(mine)}회 > 2N({2 * repeats}) — 더 돌리지 말고 원인부터 고친다")
        if len(valid) < repeats:
            warnings.append(f"경고[유효 부족] {side}: 유효 {len(valid)}회 < N({repeats}) — 결과는 미완")
    marks = " ".join(SIDE_LETTER[a.side] + ("✓" if a.status == COMPLETE else "✗") for a in attempts)
    lines.append(f"시도 순서(시작 시각): {marks or '—'}")
    valid_order = [SIDE_LETTER[a.side] for a in attempts if a.status == COMPLETE]
    expected = expected_order(repeats)
    matches = valid_order == expected[: len(valid_order)] and len(valid_order) <= len(expected)
    lines.append(f"유효 순서: {' '.join(valid_order) or '—'} (기대 {' '.join(expected)}: {'일치' if matches else '불일치'})")
    if not matches:
        warnings.append("경고[교대 순서] 유효 회차 순서가 §5.1 교대 순서와 다름 — 결과 문서에 그대로 적는다")
    return lines, warnings


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
        attempts = read_session(runs_root, key, args.allow_fixture_e2)
        payload = build_manifest(runs_root, attempts)
    except SessionError as error:
        print(f"세션 읽기 중단: {error}", file=sys.stderr)
        return 2
    lines, warnings = tally(attempts, args.repeats)
    print("\n".join(lines))
    for warning in warnings:
        print(warning, file=sys.stderr)
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
