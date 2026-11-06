"""독립 러너 기록과 pre/post 입력 검증. 키·GT는 엔진에 전달하지 않는다.

HMAC은 신뢰 러너가 발행한 기록의 출처/무결성을 검증한다. 실제 격리나 해결 관측을
대신하지 않는다. v1은 신뢰 관측자가 실행 ID에 직접 연결한 solve event만 귀속으로
인정한다. 독점 실행 boolean이나 manifest의 engine_solved 선언은 받지 않는다.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import math
import os
import stat
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

from aggregate import ScanVerdict
from collect import VerdictRecord
from ground_truth import bac_category, core_gt
from isolation_proof import (
    IsolationError, attempt_reconciliation, check_isolated_events, check_isolation, check_unproven_events,
    drain_policy, e2_identity, e2_mode, run_conditions, verdict_collection,
)
from manifest import RunManifest
from scoreboard import SnapshotError, normalize_challenges, require_basis_keys

MIN_KEY_BYTES = 32
_CONTEXT = ("run_id", "model_tag", "reset_generation", "instance_id", "execution_id")
_STEPS = ("fixture_ready", "pre", "engine_start", "engine_end", "drained", "post")
_REQUIRED_ARTIFACTS = ("pre_raw", "pre", "post_raw", "post", "records")
_OPTIONAL_ARTIFACTS = ("traffic", "coverage", "solve_events")
# v1.1(run-record-v1.1.md): 격리 근거 descriptor가 추가되고, records는 null(판정 미수집)일 수 있다.
V1, V11 = "1", "1.1"


class RunRecordError(ValueError):
    """회차 기록·원자료 검증 실패. 원자료 값이나 키를 오류 메시지에 싣지 않는다."""


@dataclass(frozen=True)
class RunInputs:
    pre: dict[str, bool]
    post: dict[str, bool]
    records: object
    traffic: dict[str, str] | None
    covered: frozenset[str] | None
    engine_solved: frozenset[str]
    execution_id: str
    fixture_fingerprint: str
    environment_fingerprint: str
    version: str = V1
    judgments_collected: bool = True  # v1.1 records=null이면 False(판정 미수집)
    proof_id: str | None = None  # v1.1 격리 증명(없으면 귀속 미확인)
    drain_policy: tuple[int, int] | None = None  # v1.1 증명 근거의 봉인 뒤 (유예, 정착 대기) 초
    run_conditions: tuple[str, str, str, str, str, str] | None = None  # (엔진 설정, 적용 샘플링, 머신 사양, 머신 ID) 해시와 LLM 스트리밍
    e2_mode: str | None = None  # v1.1 격리 근거의 E② 자리(none|stub|product). 증명이 없으면 None
    e2_identity: tuple[str, str] | None = None  # 제품 E² 회차의 (정책 지문, 이미지 ID). 그 밖에는 None
    attempt_reconciliation: dict | None = None  # E② 자리가 있는 회차의 시도별 대조 건수(§8.3). none·증명 없음이면 None


def _unique_object(pairs: list[tuple[str, object]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise RunRecordError("JSON key 중복")
        result[key] = value
    return result


def _decode(data: bytes) -> object:
    try:
        return json.loads(data, object_pairs_hook=_unique_object,
                          parse_constant=lambda _: _invalid_constant())
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise RunRecordError("회차 아티팩트 JSON 파싱 실패") from exc


def _invalid_constant():
    raise RunRecordError("JSON 비유한수 금지")


def _object(value: object, fields: set[str], label: str) -> dict:
    if not isinstance(value, dict) or set(value) != fields:
        raise RunRecordError(f"{label}: 필수/미등록 필드 오류")
    return value


def _text(value: object, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise RunRecordError(f"{label}: 비어 있지 않은 문자열 필요")
    return value


def _time(value: object, label: str) -> datetime:
    try:
        result = datetime.fromisoformat(_text(value, label).replace("Z", "+00:00"))
    except ValueError as exc:
        raise RunRecordError(f"{label}: 잘못된 시각") from exc
    if result.tzinfo is None:
        raise RunRecordError(f"{label}: timezone 필요")
    return result


def _sequence(value: object) -> int:
    if type(value) is not int or value < 0:
        raise RunRecordError("sequence: 0 이상 정수 필요")
    return value


def _key(key: bytes | None) -> bytes:
    if not isinstance(key, bytes) or len(key) < MIN_KEY_BYTES:
        raise RunRecordError("별도 신뢰 러너 키가 필요함(최소 32 bytes)")
    return key


def read_runner_key(path: str) -> bytes:
    """명령 인자에는 키 파일 경로만 받는다. 키 값은 출력·아티팩트에 기록하지 않는다."""
    if not all(hasattr(os, name) for name in ("O_NOFOLLOW", "O_CLOEXEC")):
        raise RunRecordError("안전한 러너 키 파일 읽기를 지원하는 환경 필요")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise RunRecordError("러너 키 파일은 일반 파일이고 소유자 전용 권한이어야 함")
        return _key(stream.read())


def sign_run_record(payload: dict, key: bytes) -> dict:
    """신뢰 러너용 발행 함수. 실제 관측/종료 확인을 마친 러너만 호출해야 한다."""
    if "signature" in payload:
        raise RunRecordError("서명 입력에 signature 금지")
    try:
        data = json.dumps(payload, sort_keys=True, separators=(",", ":"),
                          ensure_ascii=False, allow_nan=False).encode("utf-8")
    except (TypeError, ValueError) as exc:
        raise RunRecordError("러너 기록 서명 입력 오류") from exc
    return dict(payload, signature=hmac.new(_key(key), data, hashlib.sha256).hexdigest())


def _context(value: dict, expected: dict, label: str) -> None:
    for field in _CONTEXT:
        if value.get(field) != expected[field]:
            raise RunRecordError(f"{label}: {field} 불일치/부재")


def _snapshot(payload: object, receipt: dict, phase: str) -> dict[str, bool]:
    envelope = _object(payload, set(_CONTEXT) | {"phase", "solved"}, phase)
    _context(envelope, receipt, phase)
    if envelope["phase"] != phase:
        raise RunRecordError(f"{phase}: 단계 불일치")
    solved = envelope["solved"]
    if not isinstance(solved, dict):
        raise SnapshotError(f"{phase}: solved 객체 필요")
    normalized = normalize_challenges([{"key": key, "solved": value} for key, value in solved.items()])
    require_basis_keys(normalized, core_gt() + bac_category())
    return normalized


class _RunDirectory:
    """열린 디렉터리 FD 아래에서 각 경로 요소의 symlink를 거부한다."""

    def __init__(self, record_path: str):
        self.record_path = Path(record_path)
        self.base = self.record_path.parent.resolve(strict=True)
        self.fd = None

    def __enter__(self):
        if not all(hasattr(os, name) for name in ("O_NOFOLLOW", "O_DIRECTORY", "O_CLOEXEC", "O_NONBLOCK")):
            raise RunRecordError("안전한 dir_fd/O_NOFOLLOW 파일 읽기를 지원하는 환경 필요")
        self.fd = os.open(self.base, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        return self

    def __exit__(self, *_):
        os.close(self.fd)

    def read(self, relative: str) -> tuple[bytes, tuple[int, int]]:
        path = Path(_text(relative, "아티팩트 경로"))
        if path.is_absolute() or not path.parts or ".." in path.parts:
            raise RunRecordError("아티팩트 경로 이탈")
        current = os.dup(self.fd)
        try:
            for part in path.parts[:-1]:
                child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=current)
                os.close(current)
                current = child
            opened = os.open(path.parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK,
                             dir_fd=current)
            with os.fdopen(opened, "rb") as stream:
                info = os.fstat(stream.fileno())
                if not stat.S_ISREG(info.st_mode):
                    raise RunRecordError("아티팩트는 일반 파일이어야 함")
                return stream.read(), (info.st_dev, info.st_ino)
        finally:
            os.close(current)


def load_run_inputs(manifest: RunManifest, runner_key: bytes | None, correlate) -> RunInputs:
    """서명→회차→파일 해시→pre/post→종료/귀속 순서로 검사. 실패 시 채점하지 않는다."""
    try:
        with _RunDirectory(manifest.run_record_path) as directory:
            return _load_run_inputs(manifest, runner_key, correlate, directory)
    except (RunRecordError, SnapshotError):
        raise
    except (OSError, ValueError, RuntimeError, OverflowError) as exc:
        raise RunRecordError("회차 아티팩트 경로/파일 읽기 실패") from exc


def _load_run_inputs(manifest: RunManifest, runner_key: bytes | None, correlate,
                     directory: _RunDirectory) -> RunInputs:
    signed_bytes, _ = directory.read(directory.record_path.name)
    signed = _decode(signed_bytes)
    fields = set(_CONTEXT) | {"version", "engine", "fixture_fingerprint", "environment_fingerprint",
                              "requests_drained", "timeline", "artifacts", "signature", "condition",
                              "repeat_index", "elapsed_seconds", "total_requests", "blocked_attempts"}
    # 필드 집합(v1 또는 v1.1) → 서명 → 버전 순서. 서명 전에는 값을 해석하지 않는다.
    receipt = _object(signed, fields | {"isolation_proof"} if isinstance(signed, dict) and "isolation_proof" in signed
                      else fields, "러너 기록")
    signature = receipt["signature"]
    payload = {key: value for key, value in receipt.items() if key != "signature"}
    expected_signature = sign_run_record(payload, _key(runner_key))["signature"]
    if (not isinstance(signature, str) or len(signature) != 64
            or any(char not in "0123456789abcdef" for char in signature)
            or not hmac.compare_digest(signature, expected_signature)):
        raise RunRecordError("러너 기록 서명 불일치")
    # 버전은 느슨하게 바꾸지 않는다: v1은 정수 1, v1.1은 문자열 "1.1"만. v1.1만 격리 증명 필드를 가진다.
    if type(receipt["version"]) is int and receipt["version"] == 1 and "isolation_proof" not in receipt:
        version = V1
    elif type(receipt["version"]) is str and receipt["version"] == V11 and "isolation_proof" in receipt:
        version = V11
    else:
        raise RunRecordError("지원하지 않는 러너 기록 version")
    for field in _CONTEXT + ("fixture_fingerprint", "environment_fingerprint"):
        _text(receipt[field], field)
    for field in ("fixture_fingerprint", "environment_fingerprint"):
        if len(receipt[field]) != 64 or any(char not in "0123456789abcdef" for char in receipt[field]):
            raise RunRecordError(f"{field}: SHA-256 지문 필요")
    for field in ("run_id", "model_tag", "reset_generation"):
        if receipt[field] != getattr(manifest, field):
            raise RunRecordError(f"러너 기록 {field} 불일치")
    if receipt["engine"] != manifest.engine.value:
        raise RunRecordError("러너 기록 engine 불일치")
    _text(receipt["condition"], "condition")
    if type(receipt["repeat_index"]) is not int or receipt["repeat_index"] < 0:
        raise RunRecordError("러너 기록 repeat_index 부정")
    for field in ("total_requests", "blocked_attempts"):
        value = receipt[field]
        if value is not None and (type(value) is not int or value < 0):
            raise RunRecordError(f"러너 기록 {field} 부정")
    elapsed = receipt["elapsed_seconds"]
    if elapsed is not None and (type(elapsed) not in (int, float) or not math.isfinite(elapsed) or elapsed < 0):
        raise RunRecordError("러너 기록 elapsed_seconds 부정")
    for field in ("condition", "repeat_index", "elapsed_seconds", "total_requests", "blocked_attempts"):
        if receipt[field] != getattr(manifest, field):
            raise RunRecordError(f"러너 기록 {field} 불일치")
    if receipt["requests_drained"] is not True:
        raise RunRecordError("요청 종료 확인 없음")

    timeline = _object(receipt["timeline"], set(_STEPS), "timeline")
    times, sequences = {}, {}
    for step in _STEPS:
        item = _object(timeline[step], {"at", "sequence"}, step)
        times[step] = _time(item["at"], step)
        sequences[step] = _sequence(item["sequence"])
    for before, after in zip(_STEPS, _STEPS[1:]):
        if times[before] > times[after] or sequences[before] >= sequences[after]:
            raise RunRecordError("pre/엔진/요청 종료/post 순서 오류")

    optional = _OPTIONAL_ARTIFACTS + (("records", "isolation_evidence") if version == V11 else ())
    names = _REQUIRED_ARTIFACTS + _OPTIONAL_ARTIFACTS + (("isolation_evidence",) if version == V11 else ())
    artifacts = _object(receipt["artifacts"], set(names), "artifacts")
    base = directory.base
    paths: set[Path] = set()
    file_ids: set[tuple[int, int]] = set()

    def artifact(name: str) -> object | None:
        descriptor = artifacts[name]
        if descriptor is None and name in optional:
            return None
        descriptor = _object(descriptor, set(_CONTEXT) | {"path", "sha256", "size", "phase"}, name)
        _context(descriptor, receipt, name)
        phase = "pre" if name.startswith("pre") else "post" if name.startswith("post") else "engine"
        if descriptor["phase"] != phase:
            raise RunRecordError(f"{name}: 단계 불일치")
        relative = Path(_text(descriptor["path"], name + ".path"))
        path = base / relative
        if relative.is_absolute() or ".." in relative.parts or path in paths:
            raise RunRecordError(f"{name}: 경로 이탈/중복")
        paths.add(path)
        for artifact_name, manifest_path in (("records", manifest.records_path),
                                             ("pre", manifest.pre_snapshot_path),
                                             ("post", manifest.snapshot_path)):
            if name == artifact_name and path != Path(manifest_path).resolve():
                raise RunRecordError(f"{name}: manifest 경로 불일치")
        data, file_id = directory.read(str(relative))
        if file_id in file_ids:
            raise RunRecordError("아티팩트 파일 경로/역할 중복")
        file_ids.add(file_id)
        if type(descriptor["size"]) is not int or descriptor["size"] != len(data):
            raise RunRecordError(f"{name}: 파일 크기 불일치")
        if descriptor["sha256"] != hashlib.sha256(data).hexdigest():
            raise RunRecordError(f"{name}: 파일 해시 불일치")
        return _decode(data)

    loaded = {name: artifact(name) for name in names}
    pre = _snapshot(loaded["pre"], receipt, "pre")
    post = _snapshot(loaded["post"], receipt, "post")
    for phase, solved in (("pre", pre), ("post", post)):
        if normalize_challenges(loaded[phase + "_raw"]) != solved:
            raise RunRecordError(f"{phase}: 원본/정규화 불일치")
    if set(pre) != set(post):
        raise RunRecordError("pre/post key 집합 불일치")
    if any(pre[key] and not post[key] for key in pre):
        raise RunRecordError("해결 상태 역행(true→false)")

    def rows(name: str, field: str) -> list | None:
        value = loaded[name]
        if value is None:
            return None
        envelope = _object(value, set(_CONTEXT) | {"phase", field}, name)
        _context(envelope, receipt, name)
        if envelope["phase"] != "engine" or not isinstance(envelope[field], list):
            raise RunRecordError(f"{name}: 엔진 단계/배열 오류")
        return envelope[field]

    traffic_rows = rows("traffic", "requests")
    traffic = None if traffic_rows is None else {}
    traffic_records = {}
    for row in traffic_rows or []:
        row = _object(row, {"id", "resource_ref", "vuln_class", "sent_at", "completed_at"}, "traffic 요청")
        ref = _text(row["id"], "traffic.id")
        for field in ("resource_ref", "vuln_class"):
            _text(row[field], "traffic." + field)
        if ref in traffic:
            raise RunRecordError("traffic id 중복")
        sent, completed = _time(row["sent_at"], "sent_at"), _time(row["completed_at"], "completed_at")
        if not (times["engine_start"] <= sent <= times["engine_end"]
                and sent <= completed <= times["drained"]):
            raise RunRecordError("traffic 요청이 엔진 실행 구간 밖")
        traffic[ref] = row["resource_ref"]
        traffic_records[ref] = VerdictRecord("undecidable", "undecidable", row["vuln_class"],
                                            row["resource_ref"], None, ScanVerdict.UNDECIDABLE)

    inspections = rows("coverage", "inspections")
    covered = None if inspections is None else set()
    for row in inspections or []:
        row = _object(row, {"traffic_ref", "completed"}, "coverage 검사")
        ref = _text(row["traffic_ref"], "coverage.traffic_ref")
        if row["completed"] is not True or ref not in traffic_records:
            raise RunRecordError("coverage: 완료된 실제 엔진 요청 근거 없음")
        key = correlate(traffic_records[ref])
        if key is not None:
            covered.add(key)

    if version == V11:
        return _v11_inputs(receipt, artifacts, loaded, times, sequences, pre, post, traffic, covered, rows)
    events = rows("solve_events", "events")
    engine_solved, seen_ids, seen_keys = set(), set(), set()
    for event in events or []:
        event = _object(event, {"id", "challenge", "actor", "execution_id", "observed_at", "sequence"}, "solve event")
        event_id, key = _text(event["id"], "event.id"), _text(event["challenge"], "event.challenge")
        if event_id in seen_ids or key in seen_keys:
            raise RunRecordError("solve event 중복")
        seen_ids.add(event_id)
        seen_keys.add(key)
        if key not in post or pre[key] or not post[key]:
            raise RunRecordError("solve event와 pre/post 상태 불일치")
        at, sequence = _time(event["observed_at"], "event.observed_at"), _sequence(event["sequence"])
        if not times["pre"] <= at <= times["post"] or not sequences["pre"] < sequence < sequences["post"]:
            raise RunRecordError("solve event가 pre/post 구간 밖")
        if event["actor"] not in ("engine", "other", "unknown"):
            raise RunRecordError("solve event actor 부정")
        if event["actor"] == "engine":
            if event["execution_id"] != receipt["execution_id"]:
                raise RunRecordError("solve event execution_id 불일치")
            if not times["engine_start"] <= at <= times["drained"] or not sequences["engine_start"] < sequence < sequences["drained"]:
                raise RunRecordError("엔진 solve event가 실행 구간 밖")
            engine_solved.add(key)
        elif event["execution_id"] is not None:
            raise RunRecordError("엔진 이외 solve event의 execution_id는 null이어야 함")

    return RunInputs(pre, post, loaded["records"], traffic,
                     None if covered is None else frozenset(covered), frozenset(engine_solved),
                     receipt["execution_id"], receipt["fixture_fingerprint"], receipt["environment_fingerprint"])


def _v11_inputs(receipt: dict, artifacts: dict, loaded: dict, times: dict, sequences: dict, pre: dict, post: dict,
                traffic, covered, rows) -> RunInputs:
    """v1.1: 격리 증명이 있으면 isolated_window 이벤트만 귀속, 없으면 엔진 귀속 없음. records=null은 판정 미수집."""
    proof = receipt["isolation_proof"]
    if (proof is None) != (artifacts["isolation_evidence"] is None):
        raise RunRecordError("isolation_proof와 isolation_evidence는 둘 다 null이거나 둘 다 있어야 함")
    steps = {step: (times[step], sequences[step]) for step in _STEPS}
    try:
        if proof is None:
            check_unproven_events(rows("solve_events", "events"))
            engine_solved, proof_id, policy, conditions, e2, reconciled, identity = frozenset(), None, None, None, None, None, None
        else:
            proof = check_isolation(receipt, steps, artifacts, proof, loaded["isolation_evidence"])
            engine_solved = check_isolated_events(rows("solve_events", "events"), receipt, proof, steps, pre, post)
            proof_id = proof["proof_id"]
            policy = drain_policy(receipt, loaded["isolation_evidence"])
            conditions = run_conditions(receipt, loaded["isolation_evidence"])
            e2 = e2_mode(receipt, loaded["isolation_evidence"])
            identity = e2_identity(receipt, loaded["isolation_evidence"])
            reconciled = attempt_reconciliation(receipt, loaded["isolation_evidence"])
            verdict_collection(receipt, loaded["isolation_evidence"], loaded["records"])
    except IsolationError as exc:
        raise RunRecordError(str(exc)) from None
    return RunInputs(pre, post, loaded["records"], traffic,
                     None if covered is None else frozenset(covered), engine_solved,
                     receipt["execution_id"], receipt["fixture_fingerprint"], receipt["environment_fingerprint"],
                     version=V11, judgments_collected=loaded["records"] is not None, proof_id=proof_id,
                     drain_policy=policy, run_conditions=conditions, e2_mode=e2,
                     attempt_reconciliation=reconciled, e2_identity=identity)
