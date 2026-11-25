"""수용시험 보존 원자료의 별도 서명 계약. 공식 run-record v1.1은 변경하지 않는다.

이 파일은 GT를 읽지 않는다. 신뢰 러너가 마감·격리 확인 후 발행한 원자료 digest와 선언 조건을
검증하며, 서명을 실제 모델 실행·망 격리의 대체 증거로 취급하지 않는다.
"""
from __future__ import annotations

import hashlib
import hmac
import json
import math
import re
from pathlib import Path

import boundary_export
from run_record import sign_run_record
from runner_evidence import (DRAINED_MARKER, ENGINE_LOG, PATH_LOG, SEALED_MARKER, EvidenceError, MeterFiles,
                             _read_owned, drain_complete, final_log_consistent, ingress_counts, validate_ingress_layout)

# v2는 탐색 경계 원자료 요약(exploration_boundary)을 더했다. v1 기록은 읽되 경계 근거가 없는 것으로 본다.
FORMAT = "abc2lab.preservation-round/v2"
FORMAT_V1 = "abc2lab.preservation-round/v1"
NAME = "preservation-round.json"
OPTIONS_FORMAT = "abc2lab.preservation-options/v1"
_HEX = re.compile(r"[a-f0-9]{64}\Z")
_IMAGE = re.compile(r"sha256:[a-f0-9]{64}\Z")
_REF = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,127}\Z")
_VERSION = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+\Z")
RAW_FILES = (ENGINE_LOG, PATH_LOG, SEALED_MARKER, DRAINED_MARKER)
OPTION_FIELDS = frozenset({"format", "series_ref", "comm_mode", "write_mode", "mcp_version", "mcp_tools_sha256",
                          "browser_image_id", "exploration_policy_sha256", "declared_model_budget_sha256", "execution_source"})
CONDITION_FIELDS = frozenset({"layout", "request_budget", "time_budget_seconds", "model_pin_sha256", "target_image_id",
                             "initial_state_sha256", "machine_sha256", "runner_settings_sha256", "comm_mode", "write_mode", "mcp_version", "mcp_tools_sha256",
                             "browser_image_id", "exploration_policy_sha256", "diagnosis_policy_sha256", "declared_model_budget_sha256"})
TOP_FIELDS_V1 = frozenset({"format", "purpose", "series_ref", "run_id", "side", "repeat_index", "engine_source_sha", "engine_image_id",
                          "execution_source", "conditions", "stop_reason", "files", "forwarded_by_ingress", "signature"})
TOP_FIELDS = TOP_FIELDS_V1 | {"exploration_boundary"}


# v3(탐색 분리 배치 결정안 §3.3): 조건을 공통 대조(B·O가 같아야 짝)·쪽별(쪽 안에서만 고정)·회차별(근거만)로 나누고, O만
# 제품 블록을 둔다. 해당 없는 칸은 더미가 아니라 null이다. v1·v2 기록은 지금처럼 읽는다.
FORMAT_V3 = "abc2lab.preservation-round/v3"
COMMON_FIELDS_V3 = frozenset({"request_budget", "time_budget_seconds", "model_pin_sha256", "target_image_id", "initial_state_sha256",
                              "machine_sha256", "runner_settings_sha256", "goal_common_sha256", "comm_mode", "mcp_version",
                              "mcp_raw_tools_sha256"})
SIDE_FIELDS_V3 = frozenset({"layout", "driver_sha256", "driver_settings_sha256", "write_mode", "declared_model_budget_sha256",
                            "browser_image_id", "exploration_policy_sha256", "diagnosis_policy_sha256", "cli_surface_sha256",
                            "mcp_exposed_tools_sha256"})
RUN_FIELDS_V3 = frozenset({"stop_reason", "first_limit", "end_signal", "rate_limited"})
FIRST_LIMIT_FIELDS = frozenset({"value", "runner_limit", "runner_limit_elapsed_seconds", "engine_terminal_elapsed_seconds"})
# runner_engine_idle: 끝 신호 뒤 무활동 유예로 러너가 끝냄(stop_reason engine_completed, 고정 초안 §4 (다), 2026-11-25 사용자)
RUNNER_LIMITS = frozenset({"runner_request_budget", "runner_time_budget", "runner_engine_idle"})
# runner.first_limit과 같은 규칙(시험이 격자로 대조한다). 러너 종료 사유 → 러너 상한 이름.
_LIMIT_BY_STOP = {"request_budget": "runner_request_budget", "time_budget": "runner_time_budget",
                  "engine_completed": "runner_engine_idle"}
PRODUCT_FIELDS_V3 = frozenset({"source_sha", "image_ids", "file_sha256", "split_conditions", "sampling_overwrites", "call_records",
                               "llm_verification"})
# O 모델 호출 검증 상태(2026-11-18 사용자 결정). verified = OpenRouter 장부·호출 기록 대조와 사후 검증을 지났다.
# not_applicable_synthetic = 합성 모델 회차라 대조할 장부가 없다. 이 값은 합성(synthetic_fixture) 기록에서만 받고, 실제 모델
# 기록(model_run)에서 나오면 기록 전체를 무효로 본다.
LLM_VERIFIED = "verified"
LLM_NOT_APPLICABLE = "not_applicable_synthetic"
PRODUCT_IMAGES = frozenset({"product", "explore_browser", "fixture"})
PRODUCT_FILES = frozenset({"installation", "policy", "api_launch", "target_policy", "runtime_conditions", "control_limits"})
SPLIT_CONDITION_FIELDS = frozenset({"workflow_defaults_sha256", "workflow_defaults", "workflow", "registration"})
TOP_FIELDS_V3 = frozenset({"format", "purpose", "series_ref", "run_id", "side", "repeat_index", "engine_source_sha", "engine_image_id",
                           "execution_source", "common", "side_conditions", "run", "product", "files", "forwarded_by_ingress",
                           "exploration_boundary", "signature"})
STOP_REASONS = ("request_budget", "time_budget", "engine_completed", "cap_exhausted", "boundary_unavailable")


class PreservationRecordError(ValueError):
    """값·경로·키를 오류에 넣지 않는다."""


def _fail(code: str):
    raise PreservationRecordError(code)


def digest(value: object) -> str:
    try:
        return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False,
                                         allow_nan=False).encode()).hexdigest()
    except (TypeError, ValueError):
        _fail("preservation_json_invalid")


INITIAL_STATE_FORMAT = "abc2lab.preservation-initial-state/v1"


def initial_state_digest(*, solved: dict | None = None, observation_sha256: str | None = None,
                         observation_bytes: int | None = None) -> str:
    """회차 신원을 뺀 대상 시작 상태 지문이다. 점수판이면 해결 맵, opaque면 관측 원문의 hash·크기만 넣는다.

    관측 정규화 봉투(normalized.json)의 hash는 쓰지 않는다. 봉투에 run_id·대상/엔진 컨테이너 ID가 들어 있어 회차마다
    달라지므로, 같은 상태에서 시작한 짝도 preservation_conditions_mismatch로 멈춘다(2026-11-16 VAmPI e2-legacy 파일럿:
    관측 원문이 같은 두 회차가 서명 재계산에서 멈춤). 두 입력 중 하나만 받는다.
    """
    if solved is not None:
        if (observation_sha256 is not None or observation_bytes is not None or not isinstance(solved, dict) or not solved
                or any(not isinstance(key, str) or type(value) is not bool for key, value in solved.items())):
            _fail("preservation_initial_state_invalid")
        return digest({"format": INITIAL_STATE_FORMAT, "observer": "scoreboard", "solved": solved})
    if (not isinstance(observation_sha256, str) or not _HEX.fullmatch(observation_sha256)
            or type(observation_bytes) is not int or observation_bytes < 1):
        _fail("preservation_initial_state_invalid")
    return digest({"format": INITIAL_STATE_FORMAT, "observer": "opaque",
                   "observation_sha256": observation_sha256, "observation_bytes": observation_bytes})


def validate_options(raw: object) -> dict:
    if not isinstance(raw, dict) or set(raw) != OPTION_FIELDS or raw["format"] != OPTIONS_FORMAT:
        _fail("preservation_options_invalid")
    if (not isinstance(raw["series_ref"], str) or not _REF.fullmatch(raw["series_ref"])
            or raw["comm_mode"] not in ("full_local", "external_allowed")
            or raw["write_mode"] not in ("read_only", "approved_writes")
            or not isinstance(raw["mcp_version"], str) or not _VERSION.fullmatch(raw["mcp_version"])
            or raw["execution_source"] not in ("synthetic_fixture", "model_run")):
        _fail("preservation_options_invalid")
    for field in ("mcp_tools_sha256", "exploration_policy_sha256", "declared_model_budget_sha256"):
        if not isinstance(raw[field], str) or not _HEX.fullmatch(raw[field]):
            _fail("preservation_options_invalid")
    if not isinstance(raw["browser_image_id"], str) or not _IMAGE.fullmatch(raw["browser_image_id"]):
        _fail("preservation_options_invalid")
    return dict(raw)


def _raw_files(meter: MeterFiles) -> dict:
    out = {}
    for name in RAW_FILES:
        raw = _read_owned(meter.path, name)
        if raw is None:
            _fail("preservation_raw_missing")
        out[name] = {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)}
    return out


def _boundary(directory: Path, rows: list[dict], meter: MeterFiles, path_key_ref: str, task_id: str | None) -> dict:
    try:
        return boundary_export.verify(directory, rows, meter.path_rows(rows), path_key_ref=path_key_ref, task_id=task_id)
    except boundary_export.BoundaryExportError as exc:
        _fail(str(exc))


# 보존 기록의 회차 목적. 수용시험 회차와 분리 배치 공식 회차(2026-11-18 결정 (a), 공식 측정 프로토콜 §7 경로 틀)가 낸다.
# 목적은 서명되고, 한 묶음 안에서 섞이지 않는다(수용시험 표에 공식 회차가 섞이지 않게, #699 리뷰).
# 배선 확인 회차(wiring_check)도 분리 배치라 sidecar를 내지만 보존율 프로파일 판정(수용시험 전용)에는 쓰지 않는다.
PRESERVATION_PURPOSES = ("browser_acceptance", "official", "wiring_check")


def build_payload(*, meter: MeterFiles, options: dict, run_id: str, side: str, repeat_index: int,
                  engine_source_sha: str, engine_image_id: str, conditions: dict, stop_reason: str,
                  boundary: dict | None = None, purpose: str = "browser_acceptance") -> dict:
    """writer 정리 뒤 호출하는 서명 전 자료다. 최종 바이트를 읽고 기존 ports.sign으로 서명한다.

    layout은 호출자가 확인한 배치다. 현행 Docker 러너는 e2-legacy/v1만 발행하며, 탐색 경계가
    들어온 새 실행기는 자기 배치 검증 뒤 exploration-split/v1을 넘긴다. 옵션이 망을 열지 않는다.
    exploration-split/v1은 경계 원자료가 필수다(boundary = {"directory", "task_id", "path_key_ref"}, 경계 종료 뒤).
    다른 배치에는 경계 원자료를 붙이지 않는다.
    """
    options = validate_options(options)
    for field in ("comm_mode", "write_mode", "mcp_version", "mcp_tools_sha256", "browser_image_id", "declared_model_budget_sha256"):
        if not isinstance(conditions, dict) or conditions.get(field) != options[field]:
            _fail("preservation_options_conditions_mismatch")
    if side == "ours" and conditions.get("exploration_policy_sha256") != options["exploration_policy_sha256"]:
        _fail("preservation_options_conditions_mismatch")
    rows = meter.engine_rows()
    validate_ingress_layout(rows, conditions.get("layout"), require_ingress=True)
    meter.path_rows(rows)
    drained = meter.marker(DRAINED_MARKER)
    if drained is None or not drain_complete(drained) or not final_log_consistent(rows, meter.marker(SEALED_MARKER), drained):
        _fail("preservation_meter_incomplete")
    split = conditions.get("layout") == "exploration-split/v1"
    if split != (boundary is not None):
        _fail("preservation_boundary_missing" if split else "preservation_boundary_unexpected")
    if boundary is not None and (not isinstance(boundary, dict) or set(boundary) != {"directory", "task_id", "path_key_ref"}
                                 or not isinstance(boundary["task_id"], str) or not isinstance(boundary["path_key_ref"], str)):
        _fail("preservation_boundary_missing")
    block = None if boundary is None else _boundary(Path(boundary["directory"]), rows, meter, boundary["path_key_ref"], boundary["task_id"])
    payload = {"format": FORMAT, "purpose": purpose, "series_ref": options["series_ref"],
               "run_id": run_id, "side": side, "repeat_index": repeat_index,
               "engine_source_sha": engine_source_sha, "engine_image_id": engine_image_id,
               "execution_source": options["execution_source"], "conditions": dict(conditions), "stop_reason": stop_reason,
               "files": _raw_files(meter), "forwarded_by_ingress": ingress_counts(rows), "exploration_boundary": block}
    # metadata의 형식도 발행 전에 같은 검증기로 확인한다. 키 참조는 파일 행에서만 가져온다.
    _validate_payload(payload)
    return payload


def _validate_payload(p: object):
    top = {FORMAT: TOP_FIELDS, FORMAT_V1: TOP_FIELDS_V1}.get(p.get("format")) if isinstance(p, dict) else None
    if top is None or set(p) != top - {"signature"} or p["purpose"] not in PRESERVATION_PURPOSES:
        _fail("preservation_record_invalid")
    if (p["side"] not in ("baseline", "ours") or type(p["repeat_index"]) is not int or p["repeat_index"] < 0
            or p["execution_source"] not in ("synthetic_fixture", "model_run")
            or p["stop_reason"] not in ("request_budget", "time_budget", "engine_completed", "cap_exhausted", "boundary_unavailable")):
        _fail("preservation_record_invalid")
    for field in ("series_ref", "run_id"):
        if not isinstance(p[field], str) or not _REF.fullmatch(p[field]): _fail("preservation_record_invalid")
    if not isinstance(p["engine_source_sha"], str) or not re.fullmatch(r"[a-f0-9]{40}", p["engine_source_sha"]):
        _fail("preservation_record_invalid")
    if not isinstance(p["engine_image_id"], str) or not _IMAGE.fullmatch(p["engine_image_id"]): _fail("preservation_record_invalid")
    c = p["conditions"]
    if not isinstance(c, dict) or set(c) != CONDITION_FIELDS: _fail("preservation_conditions_invalid")
    if (c["layout"] not in ("artex-direct/v1", "e2-legacy/v1", "exploration-split/v1")
            or p["side"] == "baseline" and c["layout"] != "artex-direct/v1"
            or p["side"] == "ours" and c["layout"] == "artex-direct/v1"
            or type(c["request_budget"]) is not int or c["request_budget"] < 1
            or type(c["time_budget_seconds"]) not in (int, float) or not math.isfinite(c["time_budget_seconds"]) or c["time_budget_seconds"] <= 0
            or c["comm_mode"] not in ("full_local", "external_allowed") or c["write_mode"] not in ("read_only", "approved_writes")
            or not isinstance(c["mcp_version"], str) or not _VERSION.fullmatch(c["mcp_version"])):
        _fail("preservation_conditions_invalid")
    for field in ("model_pin_sha256", "initial_state_sha256", "machine_sha256", "runner_settings_sha256", "mcp_tools_sha256", "declared_model_budget_sha256"):
        if not isinstance(c[field], str) or not _HEX.fullmatch(c[field]): _fail("preservation_conditions_invalid")
    for field in ("target_image_id", "browser_image_id"):
        if not isinstance(c[field], str) or not _IMAGE.fullmatch(c[field]): _fail("preservation_conditions_invalid")
    for field in ("exploration_policy_sha256", "diagnosis_policy_sha256"):
        if c[field] is not None and (not isinstance(c[field], str) or not _HEX.fullmatch(c[field])): _fail("preservation_conditions_invalid")
    if p["side"] == "baseline" and any(c[f] is not None for f in ("exploration_policy_sha256", "diagnosis_policy_sha256")):
        _fail("preservation_conditions_invalid")
    if c["layout"] == "exploration-split/v1" and (c["exploration_policy_sha256"] is None or c["diagnosis_policy_sha256"] is None):
        _fail("preservation_conditions_invalid")
    if not isinstance(p["files"], dict) or set(p["files"]) != set(RAW_FILES): _fail("preservation_files_invalid")
    for row in p["files"].values():
        if (not isinstance(row, dict) or set(row) != {"sha256", "bytes"} or not isinstance(row["sha256"], str)
                or not _HEX.fullmatch(row["sha256"]) or type(row["bytes"]) is not int or row["bytes"] < 0): _fail("preservation_files_invalid")
    counts = p["forwarded_by_ingress"]
    if not isinstance(counts, dict) or set(counts) != {"main", "explore"} or any(type(n) is not int or n < 0 for n in counts.values()):
        _fail("preservation_ingress_invalid")
    if p["format"] == FORMAT:
        block = p["exploration_boundary"]
        if (c["layout"] == "exploration-split/v1") != (block is not None):
            _fail("preservation_boundary_invalid")
        if block is not None:
            try:
                boundary_export.validate_block(block)
            except boundary_export.BoundaryExportError:
                _fail("preservation_boundary_invalid")


def _hex(value) -> bool:
    return isinstance(value, str) and _HEX.fullmatch(value) is not None


def _image(value) -> bool:
    return isinstance(value, str) and _IMAGE.fullmatch(value) is not None


def _count(value) -> bool:
    return type(value) is int and value >= 0


def _seconds(value) -> bool:
    return type(value) in (int, float) and math.isfinite(value) and value >= 0


_ENGINE_SUCCESS = frozenset({"done", "completed"})  # runner.ENGINE_SUCCESS_STATUSES와 같다(시험이 대조)
_END_STATUS = re.compile(r"[a-z][a-z_]{0,31}\Z")


def expected_first_limit(stop_reason: str, runner_elapsed: float, time_budget_seconds: float, engine_elapsed: float | None,
                         closing_reason: str | None, runner_limit: str, end_status: str | None = None) -> str | None:
    """기록에 적힌 관측으로 first_limit 분류를 다시 계산한다(runner.first_limit과 같은 규칙). 러너 상한 이름·시각이 종료
    사유와 맞지 않으면 None(모순)이다. 엔진 끝 시각이 러너 상한 시각 이하일 때만 엔진이 먼저이고, 그 끝 상태의 마감
    사유가 budget_limited(O 제품 예산 마감)면 제품 상한이다."""
    if _LIMIT_BY_STOP.get(stop_reason) != runner_limit:
        return None
    if stop_reason == "time_budget" and runner_elapsed != round(time_budget_seconds, 3):
        return None
    # 유예 종료는 정상 완료 끝 신호를 읽은 뒤에만 일어난다. 끝 신호가 없거나, 늦거나, 정상 완료가 아니면 모순이다.
    if stop_reason == "engine_completed" and (engine_elapsed is None or engine_elapsed > runner_elapsed
                                              or end_status not in _ENGINE_SUCCESS):
        return None
    if engine_elapsed is not None and engine_elapsed <= runner_elapsed:
        if end_status not in _ENGINE_SUCCESS:
            return None if end_status is None or not _END_STATUS.fullmatch(end_status) else "engine_ended:" + end_status
        return "product_limit:budget_limited" if closing_reason == "budget_limited" else "engine_completed"
    return runner_limit


def _validate_v3(p: dict) -> None:
    """v3 모양 검사. 쪽(B=artex-direct, O=exploration-split)에 따라 null이어야 하는 칸과 있어야 하는 칸이 정해진다."""
    if set(p) != TOP_FIELDS_V3 - {"signature"} or p["purpose"] not in PRESERVATION_PURPOSES:
        _fail("preservation_record_invalid")
    if (p["side"] not in ("baseline", "ours") or type(p["repeat_index"]) is not int or p["repeat_index"] < 0
            or p["execution_source"] not in ("synthetic_fixture", "model_run")
            or not isinstance(p["engine_source_sha"], str) or not re.fullmatch(r"[a-f0-9]{40}", p["engine_source_sha"])
            or not _image(p["engine_image_id"])):
        _fail("preservation_record_invalid")
    for field in ("series_ref", "run_id"):
        if not isinstance(p[field], str) or not _REF.fullmatch(p[field]):
            _fail("preservation_record_invalid")
    baseline = p["side"] == "baseline"
    c = p["common"]
    if (not isinstance(c, dict) or set(c) != COMMON_FIELDS_V3
            or type(c["request_budget"]) is not int or c["request_budget"] < 1
            or not _seconds(c["time_budget_seconds"]) or c["time_budget_seconds"] <= 0
            or c["comm_mode"] not in ("full_local", "external_allowed")
            or not isinstance(c["mcp_version"], str) or not _VERSION.fullmatch(c["mcp_version"])
            or not _image(c["target_image_id"])
            or not all(_hex(c[f]) for f in ("model_pin_sha256", "initial_state_sha256", "machine_sha256", "runner_settings_sha256",
                                              "goal_common_sha256", "mcp_raw_tools_sha256"))):
        _fail("preservation_conditions_invalid")
    d = p["side_conditions"]
    split_only = ("browser_image_id", "exploration_policy_sha256", "diagnosis_policy_sha256")
    if (not isinstance(d, dict) or set(d) != SIDE_FIELDS_V3
            or d["layout"] != ("artex-direct/v1" if baseline else "exploration-split/v1")
            or d["write_mode"] not in ("read_only", "approved_writes")
            or not all(_hex(d[f]) for f in ("driver_sha256", "driver_settings_sha256", "declared_model_budget_sha256",
                                              "cli_surface_sha256", "mcp_exposed_tools_sha256"))
            or baseline and any(d[f] is not None for f in split_only)
            or not baseline and (not _image(d["browser_image_id"]) or not _hex(d["exploration_policy_sha256"])
                                 or not _hex(d["diagnosis_policy_sha256"]))):
        _fail("preservation_conditions_invalid")
    r = p["run"]
    if not isinstance(r, dict) or set(r) != RUN_FIELDS_V3 or r["stop_reason"] not in STOP_REASONS:
        _fail("preservation_run_invalid")
    f = r["first_limit"]
    if (not isinstance(f, dict) or set(f) != FIRST_LIMIT_FIELDS or f["runner_limit"] not in RUNNER_LIMITS
            or not isinstance(f["value"], str) or not (f["value"] in RUNNER_LIMITS or f["value"] == "engine_completed"
                                                       or f["value"].startswith("engine_ended:")
                                                       or not baseline and f["value"] == "product_limit:budget_limited")
            or not _seconds(f["runner_limit_elapsed_seconds"])
            or f["engine_terminal_elapsed_seconds"] is not None and not _seconds(f["engine_terminal_elapsed_seconds"])
            or (f["value"] in RUNNER_LIMITS) != (f["value"] == f["runner_limit"])):
        _fail("preservation_run_invalid")
    e = r["end_signal"]
    if e is not None and (not isinstance(e, dict) or set(e) != {"status", "elapsed_seconds", "closing_reason"}
                          or not isinstance(e["status"], str) or not e["status"] or not _seconds(e["elapsed_seconds"])
                          or e["closing_reason"] is not None and (baseline or not isinstance(e["closing_reason"], str))):
        _fail("preservation_run_invalid")
    if (e is None) != (f["engine_terminal_elapsed_seconds"] is None) or e is not None and e["elapsed_seconds"] != f["engine_terminal_elapsed_seconds"]:
        _fail("preservation_run_invalid")  # 끝 신호와 first_limit의 엔진 끝 시각은 같은 읽기다
    if f["value"] != expected_first_limit(r["stop_reason"], f["runner_limit_elapsed_seconds"], c["time_budget_seconds"],
                                         f["engine_terminal_elapsed_seconds"], None if e is None else e["closing_reason"],
                                         f["runner_limit"], None if e is None else e["status"]):
        _fail("preservation_run_invalid")  # 서명돼 있어도 관측 시각·마감 사유와 모순된 분류는 받지 않는다(#676 리뷰)
    if r["rate_limited"] is not None and not _count(r["rate_limited"]):
        _fail("preservation_run_invalid")
    _validate_product(p["product"], baseline, p["execution_source"])
    if not isinstance(p["files"], dict) or set(p["files"]) != set(RAW_FILES):
        _fail("preservation_files_invalid")
    for row in p["files"].values():
        if not isinstance(row, dict) or set(row) != {"sha256", "bytes"} or not _hex(row["sha256"]) or not _count(row["bytes"]):
            _fail("preservation_files_invalid")
    counts = p["forwarded_by_ingress"]
    if not isinstance(counts, dict) or set(counts) != {"main", "explore"} or not all(_count(n) for n in counts.values()):
        _fail("preservation_ingress_invalid")
    block = p["exploration_boundary"]
    if baseline != (block is None):
        _fail("preservation_boundary_invalid")
    if block is not None:
        try:
            boundary_export.validate_block(block)
        except boundary_export.BoundaryExportError:
            _fail("preservation_boundary_invalid")


def _validate_product(block, baseline: bool, execution_source: str) -> None:
    if baseline:
        if block is not None:
            _fail("preservation_product_invalid")
        return
    if not isinstance(block, dict) or set(block) != PRODUCT_FIELDS_V3:
        _fail("preservation_product_invalid")
    if block["llm_verification"] == LLM_NOT_APPLICABLE:
        # '해당 없음(합성)'은 합성 회차에서만이다. 실제 모델 회차에서 장부 대조가 빠지면 무효다.
        if execution_source != "synthetic_fixture" or block["call_records"] is not None:
            _fail("preservation_llm_verification_invalid")
        block = dict(block, call_records={"reservations": 0, "lines": 0})  # 아래 모양 검사는 그 밖의 칸만 본다
    elif block["llm_verification"] != LLM_VERIFIED:
        _fail("preservation_llm_verification_invalid")
    images, files, conditions = block["image_ids"], block["file_sha256"], block["split_conditions"]
    overwrites, records = block["sampling_overwrites"], block["call_records"]
    if (not isinstance(block["source_sha"], str) or not re.fullmatch(r"[a-f0-9]{40}", block["source_sha"])
            or not isinstance(images, dict) or set(images) != PRODUCT_IMAGES or not all(_image(v) for v in images.values())
            or not isinstance(files, dict) or set(files) != PRODUCT_FILES
            # 진단 실행이 붙지 않은 회차(탐색 중 예산 소진)는 실행 조건 파일이 없어 그 SHA만 null이다(#693)
            or not all(_hex(v) for name, v in files.items() if name != "runtime_conditions")
            or files["runtime_conditions"] is not None and not _hex(files["runtime_conditions"])
            or not isinstance(conditions, dict) or set(conditions) != SPLIT_CONDITION_FIELDS
            or not _hex(conditions["workflow_defaults_sha256"])
            or not isinstance(overwrites, dict) or set(overwrites) != {"execution", "preparation"}
            or overwrites["execution"] is not None and not _count(overwrites["execution"])
            or not isinstance(overwrites["preparation"], list)
            or any(v is not None and not _count(v) for v in overwrites["preparation"])
            or not isinstance(records, dict) or set(records) != {"reservations", "lines"}
            or not all(_count(v) for v in records.values()) or records["reservations"] != records["lines"]):
        _fail("preservation_product_invalid")


def build_payload_v3(*, meter: MeterFiles, series_ref: str, execution_source: str, run_id: str, side: str, repeat_index: int,
                     engine_source_sha: str, engine_image_id: str, common: dict, side_conditions: dict, run: dict,
                     product: dict | None, boundary: dict | None, purpose: str = "browser_acceptance") -> dict:
    """v3 서명 전 자료. 계측기 writer 정리 뒤 최종 바이트를 읽는다. O(exploration-split/v1)는 경계 원자료가 필수다
    (boundary = {"directory", "task_id", "path_key_ref"}, 경계 정지 뒤). B는 경계·제품 블록이 없다."""
    rows = meter.engine_rows()
    layout = side_conditions.get("layout") if isinstance(side_conditions, dict) else None
    validate_ingress_layout(rows, layout, require_ingress=True)
    meter.path_rows(rows)
    drained = meter.marker(DRAINED_MARKER)
    if drained is None or not drain_complete(drained) or not final_log_consistent(rows, meter.marker(SEALED_MARKER), drained):
        _fail("preservation_meter_incomplete")
    split = layout == "exploration-split/v1"
    if split != (boundary is not None):
        _fail("preservation_boundary_missing" if split else "preservation_boundary_unexpected")
    if boundary is not None and (not isinstance(boundary, dict) or set(boundary) != {"directory", "task_id", "path_key_ref"}
                                 or not isinstance(boundary["task_id"], str) or not isinstance(boundary["path_key_ref"], str)):
        _fail("preservation_boundary_missing")
    block = None if boundary is None else _boundary(Path(boundary["directory"]), rows, meter, boundary["path_key_ref"], boundary["task_id"])
    payload = {"format": FORMAT_V3, "purpose": purpose, "series_ref": series_ref, "run_id": run_id, "side": side,
               "repeat_index": repeat_index, "engine_source_sha": engine_source_sha, "engine_image_id": engine_image_id,
               "execution_source": execution_source, "common": dict(common), "side_conditions": dict(side_conditions),
               "run": dict(run), "product": None if product is None else dict(product), "files": _raw_files(meter),
               "forwarded_by_ingress": ingress_counts(rows), "exploration_boundary": block}
    _validate_v3(payload)
    return payload


def validate_series_v3(baseline: list[dict], improved: list[dict], *, request_budget: int, read_only: bool) -> dict:
    """v3 짝 묶기. 공통 대조 칸만 B·O 전체에서 같아야 하고(다르면 preservation_conditions_mismatch), 쪽별 칸은 쪽 안에서만
    같아야 한다(다르면 preservation_layout_or_policy_mixed). 회차별 칸은 대조하지 않는다."""
    if not baseline or len(baseline) != len(improved):
        _fail("preservation_pairs_invalid")
    all_records = baseline + improved
    for record in all_records:
        if not isinstance(record, dict) or record.get("format") != FORMAT_V3:
            _fail("preservation_series_mismatch")
        _validate_v3(record)
    if any(len({r[f] for r in all_records}) != 1 for f in ("series_ref", "execution_source", "purpose")):
        _fail("preservation_series_mismatch")
    for field in COMMON_FIELDS_V3:
        if len({digest(r["common"][field]) for r in all_records}) != 1:
            _fail("preservation_conditions_mismatch")
    for records, side in ((baseline, "baseline"), (improved, "ours")):
        if any(r["side"] != side for r in records):
            _fail("preservation_side_mismatch")
        for field in ("engine_source_sha", "engine_image_id"):
            if len({r[field] for r in records}) != 1:
                _fail("preservation_engine_mixed")
        for field in SIDE_FIELDS_V3:
            if len({digest(r["side_conditions"][field]) for r in records}) != 1:
                _fail("preservation_layout_or_policy_mixed")
        if len({r["run_id"] for r in records}) != len(records):
            _fail("preservation_run_reused")
    if {r["run_id"] for r in baseline} & {r["run_id"] for r in improved}:
        _fail("preservation_run_reused")
    if any(b["repeat_index"] != o["repeat_index"] for b, o in zip(baseline, improved)) or len({r["repeat_index"] for r in baseline}) != len(baseline):
        _fail("preservation_pair_order_mismatch")
    if any(r["common"]["request_budget"] != request_budget for r in all_records):
        _fail("preservation_budget_mismatch")
    if (any(r["side_conditions"]["write_mode"] != "approved_writes" for r in baseline)
            or any(r["side_conditions"]["write_mode"] != ("read_only" if read_only else "approved_writes") for r in improved)):
        _fail("preservation_write_mode_mismatch")
    return {"format": FORMAT_V3, "verified_signed_rounds": len(all_records), "series_ref": baseline[0]["series_ref"],
            "execution_source": baseline[0]["execution_source"], "purpose": baseline[0]["purpose"],
            "layout": improved[0]["side_conditions"]["layout"],
            "mcp_version": improved[0]["common"]["mcp_version"], "exploration_boundary": _boundary_series(improved),
            "first_limits": [{"repeat_index": r["repeat_index"], "side": r["side"], "value": r["run"]["first_limit"]["value"]}
                             for r in all_records],
            "round_digests": [digest(r) for r in all_records]}


def _unique(pairs):
    out = {}
    for key, value in pairs:
        if key in out: _fail("preservation_duplicate_key")
        out[key] = value
    return out


def load_record(directory: Path, meter: MeterFiles, key: bytes, boundary_dir: Path | None = None) -> dict:
    """먼저 서명, 그다음 엄격한 조건과 원자료 해시를 확인한다. 파일 경로는 계약의 고정 이름뿐이다.

    경계 원자료 요약이 있는 기록은 boundary_dir의 최종 파일로 같은 요약을 다시 계산해 서명된 값과 맞춘다.
    """
    raw = _read_owned(directory, NAME)
    if raw is None or len(raw) > 65536: _fail("preservation_record_missing_or_large")
    try:
        signed = json.loads(raw, object_pairs_hook=_unique, parse_constant=lambda _: _fail("preservation_json_invalid"))
    except PreservationRecordError:
        raise
    except (ValueError, UnicodeError): _fail("preservation_record_invalid")
    top = {FORMAT: TOP_FIELDS, FORMAT_V3: TOP_FIELDS_V3}.get(signed.get("format")) if isinstance(signed, dict) else None
    if top is None or set(signed) != top: _fail("preservation_record_invalid")
    payload = {k: v for k, v in signed.items() if k != "signature"}
    expected = sign_run_record(payload, key)["signature"]
    # compare_digest(str,str)는 비 ASCII에서 TypeError를 낸다. 서명은 애초에 64 hex만 허용한다.
    if not isinstance(signed["signature"], str) or not _HEX.fullmatch(signed["signature"]) or not hmac.compare_digest(signed["signature"], expected):
        _fail("preservation_signature_invalid")
    if payload["format"] == FORMAT_V3:
        _validate_v3(payload)
        layout = payload["side_conditions"]["layout"]
    else:
        _validate_payload(payload)
        layout = payload["conditions"]["layout"]
    if _raw_files(meter) != payload["files"]: _fail("preservation_raw_digest_mismatch")
    rows = meter.engine_rows()
    validate_ingress_layout(rows, layout, require_ingress=True); meter.path_rows(rows)
    drained = meter.marker(DRAINED_MARKER)
    if drained is None or not drain_complete(drained) or not final_log_consistent(rows, meter.marker(SEALED_MARKER), drained):
        _fail("preservation_meter_incomplete")
    if ingress_counts(rows) != payload["forwarded_by_ingress"]: _fail("preservation_ingress_mismatch")
    block = payload.get("exploration_boundary")
    if block is None:
        if boundary_dir is not None: _fail("preservation_boundary_unexpected")
        return payload
    if boundary_dir is None: _fail("preservation_boundary_files_missing")
    # 작업 결속은 발행 때 러너가 확인했다. 재검증은 결속 hash까지 포함한 요약 전체가 같은지로 본다.
    if _boundary(boundary_dir, rows, meter, block["path_key_ref"], None) != block: _fail("preservation_boundary_mismatch")
    return payload


def validate_series(baseline: list[dict], improved: list[dict], *, request_budget: int, read_only: bool) -> dict:
    """서로 다른 배치·모델·모드·정책을 같은 짝/평균으로 섞지 않는다. SHA와 공개 메타데이터만 반환한다.

    v3 기록이 하나라도 있으면 v3 규칙(validate_series_v3)으로 본다. v2와 섞이면 거부한다."""
    if any(isinstance(r, dict) and r.get("format") == FORMAT_V3 for r in list(baseline) + list(improved)):
        return validate_series_v3(baseline, improved, request_budget=request_budget, read_only=read_only)
    if not baseline or len(baseline) != len(improved): _fail("preservation_pairs_invalid")
    all_records = baseline + improved
    for record in all_records:
        _validate_payload(record)
    common = ("format", "series_ref", "execution_source", "purpose")
    if any(len({r[f] for r in all_records}) != 1 for f in common): _fail("preservation_series_mismatch")
    differing = {"layout", "exploration_policy_sha256", "diagnosis_policy_sha256", "write_mode"}
    for field in CONDITION_FIELDS - differing:
        if len({digest(r["conditions"][field]) for r in all_records}) != 1: _fail("preservation_conditions_mismatch")
    for records, side in ((baseline, "baseline"), (improved, "ours")):
        if any(r["side"] != side for r in records): _fail("preservation_side_mismatch")
        for field in ("engine_source_sha", "engine_image_id"):
            if len({r[field] for r in records}) != 1: _fail("preservation_engine_mixed")
        for field in differing:
            if len({digest(r["conditions"][field]) for r in records}) != 1: _fail("preservation_layout_or_policy_mixed")
        if len({r["run_id"] for r in records}) != len(records): _fail("preservation_run_reused")
    if set(r["run_id"] for r in baseline) & set(r["run_id"] for r in improved): _fail("preservation_run_reused")
    if any(b["repeat_index"] != o["repeat_index"] for b, o in zip(baseline, improved)): _fail("preservation_pair_order_mismatch")
    if len({r["repeat_index"] for r in baseline}) != len(baseline): _fail("preservation_pair_order_mismatch")
    if any(r["conditions"]["request_budget"] != request_budget for r in all_records): _fail("preservation_budget_mismatch")
    if (any(r["conditions"]["write_mode"] != "approved_writes" for r in baseline)
            or any(r["conditions"]["write_mode"] != ("read_only" if read_only else "approved_writes") for r in improved)):
        _fail("preservation_write_mode_mismatch")
    return {"format": FORMAT, "verified_signed_rounds": len(all_records), "series_ref": baseline[0]["series_ref"],
            "execution_source": baseline[0]["execution_source"], "purpose": baseline[0]["purpose"],
            "layout": improved[0]["conditions"]["layout"],
            "mcp_version": improved[0]["conditions"]["mcp_version"],
            "exploration_boundary": _boundary_series(improved),
            "round_digests": [digest(r) for r in all_records]}


def _boundary_series(improved: list[dict]) -> dict:
    """개선 후 회차의 경계 원자료 상태. 회차마다 카운터를 따로 싣고 합산·0 채우기를 하지 않는다.

    PASS는 모든 회차의 원자료가 검증되고 계측기 탐색 입구와 맞았다는 뜻이다. 네트워크 격리 증명이 아니다.
    """
    blocks = [r.get("exploration_boundary") for r in improved]
    if any(block is None for block in blocks):
        return {"status": "NOT_RUN", "reason": "verified_boundary_export_unavailable"}
    rounds = [{"repeat_index": r["repeat_index"], "status": b["status"], "closed_reason": b["closed_reason"],
               "counters": b["counters"], "reconciliation": b["reconciliation"]} for r, b in zip(improved, blocks)]
    if any(b["status"] == "not_opened" for b in blocks):
        status, reason = "NOT_RUN", "boundary_session_not_opened"
    elif any(b["status"] == "truncated" for b in blocks):
        status, reason = "UNDECIDABLE", "boundary_export_truncated"
    else:
        status, reason = "PASS", "boundary_export_verified"
    return {"status": status, "reason": reason, "rounds": rounds}


def assessment(summary: dict, profile: str, evidence: dict) -> dict:
    """기존 수용선만 적용한다. 합성·기존 배치와 R-P는 실제 복원 완료로 판정하지 않는다."""
    if profile not in ("R-P", "R-F", "R-R"): _fail("preservation_profile_invalid")
    if evidence["execution_source"] != "model_run" or evidence["layout"] != "exploration-split/v1":
        return {"profile": profile, "status": "NOT_RUN", "reason": "real_split_model_measurement_not_run"}
    if evidence["mcp_version"] != "0.0.84":
        return {"profile": profile, "status": "UNDECIDABLE", "reason": "mcp_version_not_reviewed"}
    if profile == "R-P":
        return {"profile": profile, "status": "UNDECIDABLE", "reason": "report_only_not_completion_criterion"}
    boundary = evidence.get("exploration_boundary", {})
    if boundary.get("status") != "PASS":
        return {"profile": profile, "status": "NOT_RUN", "reason": boundary.get("reason", "verified_boundary_export_unavailable")}
    return preservation_verdict(summary, profile)


def preservation_verdict(summary: dict, profile: str) -> dict:
    """이미 유효성 검증된 집계의 기존 수용선. 이 함수만으로 실측·경계 완료를 주장하지 않는다."""
    if profile not in ("R-F", "R-R"): _fail("preservation_profile_invalid")
    if summary["read_only"] != (profile == "R-R"): _fail("preservation_profile_domain_mismatch")
    if len(summary["pair_ratios"]) < 3:
        return {"profile": profile, "status": "UNDECIDABLE", "reason": "valid_pairs_lt_3"}
    threshold = 0.95 if profile == "R-R" else 0.90
    return {"profile": profile, "status": "PASS" if summary["pair_mean"] >= threshold else "FAIL",
            "threshold": threshold, "valid_pairs": len(summary["pair_ratios"])}


def full_budget_from_pilots(pilots: list[dict]) -> dict:
    """검증한 runner-result를 시간순으로 받는다. 자동 호출·서명·세션 완결성 검증은 하지 않는다."""
    discarded = []
    stop_reasons = ("request_budget", "time_budget", "engine_completed", "cap_exhausted", "boundary_unavailable")
    for i, pilot in enumerate(pilots, 1):
        if (not isinstance(pilot, dict) or pilot.get("format") != "abc2lab-eval-runner-result-v1"
                or pilot.get("status") not in ("complete", "invalid")):
            _fail("preservation_pilot_record_invalid")
        status = pilot["status"]
        meter = pilot.get("meter")
        stop = meter.get("stop_reason") if isinstance(meter, dict) else None
        if stop is not None and stop not in stop_reasons: _fail("preservation_pilot_stop_invalid")
        if status == "complete" and not isinstance(meter, dict): _fail("preservation_pilot_meter_missing")
        if status == "complete" and stop not in stop_reasons: _fail("preservation_pilot_stop_invalid")
        if status == "complete" and stop == "time_budget":
            count = meter.get("forwarded_requests")
            if type(count) is not int or count < 1: _fail("preservation_pilot_count_invalid")
            return {"pilot_attempts": i, "discarded_stop_reasons": [r["discard_reason"] for r in discarded],
                    "discarded_pilots": discarded, "n0": count, "request_budget": 2 * count,
                    "selected_index": i, "stop_reason": "time_budget"}
        entry = {"attempt_index": i, "status": status, "stop_reason": stop,
                 "discard_reason": "invalid" if status == "invalid" else stop or "stop_reason_missing"}
        if status == "invalid":
            reasons = pilot.get("reasons")
            step = pilot.get("failed_step")
            code = re.compile(r"[a-z][a-z0-9_]{0,127}\Z")
            if (not isinstance(reasons, list) or not reasons or len(reasons) > 64
                    or any(not isinstance(v, str) or not code.fullmatch(v) for v in reasons)
                    or not isinstance(step, str) or not code.fullmatch(step)):
                _fail("preservation_pilot_invalid_reasons")
            entry.update(reasons=list(reasons), failed_step=step)
        discarded.append(entry)
    _fail("preservation_valid_time_pilot_missing")
