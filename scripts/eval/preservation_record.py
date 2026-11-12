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

from run_record import sign_run_record
from runner_evidence import (DRAINED_MARKER, ENGINE_LOG, PATH_LOG, SEALED_MARKER, EvidenceError, MeterFiles,
                             _read_owned, drain_complete, final_log_consistent, ingress_counts, validate_ingress_layout)

FORMAT = "abc2lab.preservation-round/v1"
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
TOP_FIELDS = frozenset({"format", "purpose", "series_ref", "run_id", "side", "repeat_index", "engine_source_sha", "engine_image_id",
                       "execution_source", "conditions", "stop_reason", "files", "forwarded_by_ingress", "signature"})


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


def build_payload(*, meter: MeterFiles, options: dict, run_id: str, side: str, repeat_index: int,
                  engine_source_sha: str, engine_image_id: str, conditions: dict, stop_reason: str) -> dict:
    """writer 정리 뒤 호출하는 서명 전 자료다. 최종 바이트를 읽고 기존 ports.sign으로 서명한다.

    layout은 호출자가 확인한 배치다. 현행 Docker 러너는 e2-legacy/v1만 발행하며, 탐색 경계가
    들어온 새 실행기는 자기 배치 검증 뒤 exploration-split/v1을 넘긴다. 옵션이 망을 열지 않는다.
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
    payload = {"format": FORMAT, "purpose": "browser_acceptance", "series_ref": options["series_ref"],
               "run_id": run_id, "side": side, "repeat_index": repeat_index,
               "engine_source_sha": engine_source_sha, "engine_image_id": engine_image_id,
               "execution_source": options["execution_source"], "conditions": dict(conditions), "stop_reason": stop_reason,
               "files": _raw_files(meter), "forwarded_by_ingress": ingress_counts(rows)}
    # metadata의 형식도 발행 전에 같은 검증기로 확인한다. 키 참조는 파일 행에서만 가져온다.
    _validate_payload(payload)
    return payload


def _validate_payload(p: object):
    if not isinstance(p, dict) or set(p) != TOP_FIELDS - {"signature"} or p["format"] != FORMAT or p["purpose"] != "browser_acceptance":
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


def _unique(pairs):
    out = {}
    for key, value in pairs:
        if key in out: _fail("preservation_duplicate_key")
        out[key] = value
    return out


def load_record(directory: Path, meter: MeterFiles, key: bytes) -> dict:
    """먼저 서명, 그다음 엄격한 조건과 원자료 해시를 확인한다. 파일 경로는 계약의 고정 이름뿐이다."""
    raw = _read_owned(directory, NAME)
    if raw is None or len(raw) > 65536: _fail("preservation_record_missing_or_large")
    try:
        signed = json.loads(raw, object_pairs_hook=_unique, parse_constant=lambda _: _fail("preservation_json_invalid"))
    except PreservationRecordError:
        raise
    except (ValueError, UnicodeError): _fail("preservation_record_invalid")
    if not isinstance(signed, dict) or set(signed) != TOP_FIELDS: _fail("preservation_record_invalid")
    payload = {k: v for k, v in signed.items() if k != "signature"}
    expected = sign_run_record(payload, key)["signature"]
    # compare_digest(str,str)는 비 ASCII에서 TypeError를 낸다. 서명은 애초에 64 hex만 허용한다.
    if not isinstance(signed["signature"], str) or not _HEX.fullmatch(signed["signature"]) or not hmac.compare_digest(signed["signature"], expected):
        _fail("preservation_signature_invalid")
    _validate_payload(payload)
    if _raw_files(meter) != payload["files"]: _fail("preservation_raw_digest_mismatch")
    rows = meter.engine_rows()
    validate_ingress_layout(rows, payload["conditions"]["layout"], require_ingress=True); meter.path_rows(rows)
    drained = meter.marker(DRAINED_MARKER)
    if drained is None or not drain_complete(drained) or not final_log_consistent(rows, meter.marker(SEALED_MARKER), drained):
        _fail("preservation_meter_incomplete")
    if ingress_counts(rows) != payload["forwarded_by_ingress"]: _fail("preservation_ingress_mismatch")
    return payload


def validate_series(baseline: list[dict], improved: list[dict], *, request_budget: int, read_only: bool) -> dict:
    """서로 다른 배치·모델·모드·정책을 같은 짝/평균으로 섞지 않는다. SHA와 공개 메타데이터만 반환한다."""
    if not baseline or len(baseline) != len(improved): _fail("preservation_pairs_invalid")
    all_records = baseline + improved
    for record in all_records:
        _validate_payload(record)
    common = ("series_ref", "execution_source")
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
            "execution_source": baseline[0]["execution_source"], "layout": improved[0]["conditions"]["layout"],
            "mcp_version": improved[0]["conditions"]["mcp_version"],
            "exploration_boundary": {"status": "NOT_RUN", "reason": "verified_boundary_export_unavailable"},
            "round_digests": [digest(r) for r in all_records]}


def assessment(summary: dict, profile: str, evidence: dict) -> dict:
    """기존 수용선만 적용한다. 합성·기존 배치와 R-P는 실제 복원 완료로 판정하지 않는다."""
    if profile not in ("R-P", "R-F", "R-R"): _fail("preservation_profile_invalid")
    if evidence["execution_source"] != "model_run" or evidence["layout"] != "exploration-split/v1":
        return {"profile": profile, "status": "NOT_RUN", "reason": "real_split_model_measurement_not_run"}
    if evidence["mcp_version"] != "0.0.84":
        return {"profile": profile, "status": "UNDECIDABLE", "reason": "mcp_version_not_reviewed"}
    if profile == "R-P":
        return {"profile": profile, "status": "UNDECIDABLE", "reason": "report_only_not_completion_criterion"}
    if evidence.get("exploration_boundary", {}).get("status") != "PASS":
        return {"profile": profile, "status": "NOT_RUN", "reason": "verified_boundary_export_unavailable"}
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
