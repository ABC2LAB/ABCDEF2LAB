"""러너 기록 v1.1의 격리 증명·격리 근거·isolated_window 이벤트 검증(run-record-v1.1.md §3·§3.1·§4).

채점기 쪽 검증이다. 러너 코드(runner_*)를 가져다 쓰지 않고 계약 문서대로 따로 확인한다. 같은 버그를 양쪽이
공유하면 검증이 무의미해지기 때문이다(정책 지문 재계산이 러너와 같은지는 테스트로만 대조한다).
오류 메시지에는 필드 이름과 이유만 담고 값(경로·키·ID)은 담지 않는다.
"""

from __future__ import annotations

import hashlib
import json
import re
from datetime import datetime

_CONTEXT = ("run_id", "model_tag", "reset_generation", "instance_id", "execution_id")
EVIDENCE_FORMAT = "abc2lab-isolation-evidence-v1"
NETWORK_MODES = ("loopback_only", "dedicated_network")
ALLOWED_ACTORS = frozenset({"engine", "observer"})
# 양성 대조는 연결 성공, 부정 경로는 실패가 기대값이다.
_PROBE_EXPECTED = {"positive": "success", "negative": "fail"}
_HEX64 = re.compile(r"[0-9a-f]{64}\Z")
_PROOF_FIELDS = frozenset(_CONTEXT) | {"proof_id", "window", "snapshots", "network", "host",
                                       "enforcement_evidence_sha256", "preparation", "observer"}
_ROW_FIELDS = {
    "boundary": {"event", "boundary_ref", "network_ids", "policy_sha256"},
    "network_snapshot": {"phase", "networks", "publishes", "services"},
    "connectivity_check": {"phase", "id", "probe_kind", "target", "role", "expected", "accepted", "observed", "code",
                           "verdict"},
    "host_condition": {"phase", "operating_condition_ref", "daemon", "listeners_outside_loopback", "allowed_listens",
                       "other_publishes", "satisfied"},
    "host_watch": {"observed_from", "observed_to", "samples", "max_gap_seconds", "allowed_max_gap_seconds",
                   "violations"},
    "docker_events_window": {"since", "until", "event_count", "kinds", "foreign_attach", "run_container_restarts",
                             "complete"},
    "preparation": {"sealed", "actions_after_pre", "channels", "basis"},
    "observer_readonly": {"path_ref", "method", "verification_ref"},
    "observer_request": {"request_seq", "method", "path_ref", "sent_at", "completed_at", "status", "decision",
                         "window"},
    "engine_meter_summary": {"forwarded_requests", "rejected_after_seal", "forced_closed_streams", "log_rows",
                             "sequence_complete"},
    "drain": {"engine_end", "sealed_at", "drained_at", "inflight", "grace_seconds", "settle_seconds",
              "forced_cancelled", "settle_applied_seconds"},
    "run_conditions": {"engine_settings_sha256", "engine_rules_sha256", "workers", "sampling", "host", "machine_id_sha256",
                       "llm_stream"},
    "e2_placement": {"mode", "decision_rows", "allowed", "unsupported", "unavailable"},
    "verdict_collection": {"status", "reason"},
    "attempt_reconciliation": {"cells", "allowed_responded", "meter_attempt_reused", "blocked_reached", "unrecorded_reached",
                               "positive_evidence", "control_claim_withheld"},
}
# 개선 후 회차의 E② 자리(러너 e2.ours_mode). 회차 조건(run_conditions)과 달리 baseline·개선 후가 일부러 다르므로
# 회차 사이 동일성 대조가 아니라 조건별로 섞이지 않는지만 채점기가 본다.
E2_MODES = ("none", "stub")
_E2_COUNTS = ("decision_rows", "allowed", "unsupported", "unavailable")
# 시도별 대조 다섯 칸(평가 프로토콜 §8.3). 러너 대조 코드와 따로 계약대로 다시 확인한다.
RECONCILE_CELLS = ("allowed_observed", "allowed_unobserved", "denied_unobserved", "denied_observed",
                   "unrecorded_observed")
_EVENT_FIELDS = frozenset({"id", "challenge", "actor", "execution_id", "observed_at", "sequence", "basis",
                           "isolation_proof_sha256"})


class IsolationError(ValueError):
    """격리 증명·근거·이벤트가 계약과 맞지 않는다(입력 오류 → 채점 중단)."""


def canonical_sha256(value: object) -> str:
    """v1 서명 직렬화와 같은 규칙의 SHA-256."""
    data = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False)
    return hashlib.sha256(data.encode("utf-8")).hexdigest()


def _fields(value: object, fields: set[str] | frozenset[str], label: str) -> dict:
    if not isinstance(value, dict) or set(value) != set(fields):
        raise IsolationError(f"{label}: 필수/미등록 필드 오류")
    return value


def _text(value: object, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise IsolationError(f"{label}: 비어 있지 않은 문자열 필요")
    return value


def _int(value: object, label: str, low: int = 0) -> int:
    if type(value) is not int or value < low:
        raise IsolationError(f"{label}: {low} 이상 정수 필요")
    return value


def _time(value: object, label: str) -> datetime:
    try:
        result = datetime.fromisoformat(_text(value, label).replace("Z", "+00:00"))
    except ValueError:
        raise IsolationError(f"{label}: 잘못된 시각") from None
    if result.tzinfo is None:
        raise IsolationError(f"{label}: timezone 필요")
    return result


def _step(value: object, label: str) -> tuple[datetime, int]:
    item = _fields(value, {"at", "sequence"}, label)
    return _time(item["at"], label + ".at"), _int(item["sequence"], label + ".sequence")


def _hex(value: object, label: str) -> str:
    if not isinstance(value, str) or not _HEX64.fullmatch(value):
        raise IsolationError(f"{label}: SHA-256 hex 필요")
    return value


def recompute_policy(snapshot_row: dict) -> str:
    """network_snapshot 행에서 망 정책 지문(망·internal·구성 서비스·바인딩)을 다시 계산한다."""
    bindings: dict[str, list] = {item["service"]: [] for item in snapshot_row["services"]}
    for item in snapshot_row["publishes"]:
        if item["service"] not in bindings:
            raise IsolationError("network_snapshot: publish 서비스가 서비스 목록에 없음")
        bindings[item["service"]].append([item["container_port"], item["host_ip"], item["host_port"]])
    policy = {"networks": {n["name"]: {"internal": n["internal"], "members": sorted(n["members"])}
                           for n in snapshot_row["networks"]},
              "bindings": bindings}
    data = json.dumps(policy, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")
    return hashlib.sha256(data).hexdigest()


def _rows(evidence: object, receipt: dict) -> dict[str, list[dict]]:
    envelope = _fields(evidence, set(_CONTEXT) | {"phase", "format", "rows"}, "isolation_evidence")
    if any(envelope[field] != receipt[field] for field in _CONTEXT):
        raise IsolationError("isolation_evidence: 회차 문맥 불일치")
    if envelope["phase"] != "engine" or envelope["format"] != EVIDENCE_FORMAT or not isinstance(envelope["rows"], list):
        raise IsolationError("isolation_evidence: 단계·형식 오류")
    by_kind: dict[str, list[dict]] = {kind: [] for kind in _ROW_FIELDS}
    for index, row in enumerate(envelope["rows"], start=1):
        if not isinstance(row, dict) or row.get("kind") not in _ROW_FIELDS:
            raise IsolationError("isolation_evidence: 모르는 행 종류")
        kind = row["kind"]
        _fields(row, _ROW_FIELDS[kind] | {"kind", "seq", "at"}, f"isolation_evidence.{kind}")
        if row["seq"] != index or type(row["seq"]) is not int:
            raise IsolationError("isolation_evidence: 행 순번 공백·중복")
        _time(row["at"], f"isolation_evidence.{kind}.at")
        by_kind[kind].append(row)
    return by_kind


def _one(rows: dict[str, list[dict]], kind: str, **match) -> dict:
    found = [row for row in rows[kind] if all(row.get(k) == v for k, v in match.items())]
    if len(found) != 1:
        raise IsolationError(f"isolation_evidence.{kind}: 정확히 한 행 필요")
    return found[0]


def check_isolation(receipt: dict, steps: dict[str, tuple[datetime, int]], artifacts: dict, proof: object,
                    evidence: object) -> dict:
    """§3 증명과 §3.1 근거 행을 대조한다. 통과하면 증명 객체를 돌려준다."""
    proof = _fields(proof, _PROOF_FIELDS, "isolation_proof")
    if any(proof[field] != receipt[field] for field in _CONTEXT):
        raise IsolationError("isolation_proof: 회차 문맥 불일치")
    _text(proof["proof_id"], "isolation_proof.proof_id")
    timeline = receipt["timeline"]
    window = _fields(proof["window"], {"pre", "post"}, "isolation_proof.window")
    if window["pre"] != timeline["pre"] or window["post"] != timeline["post"]:
        raise IsolationError("isolation_proof.window: timeline pre/post와 불일치")
    snapshots = _fields(proof["snapshots"], {"pre_sha256", "post_sha256"}, "isolation_proof.snapshots")
    if snapshots["pre_sha256"] != artifacts["pre"]["sha256"] or snapshots["post_sha256"] != artifacts["post"]["sha256"]:
        raise IsolationError("isolation_proof.snapshots: pre/post 정규화 파일 해시와 불일치")
    network = _fields(proof["network"], {"mode", "target_origin", "boundary_ref", "policy_sha256", "allowed_actors"},
                      "isolation_proof.network")
    if network["mode"] not in NETWORK_MODES:
        raise IsolationError("isolation_proof.network.mode: 미지원")
    origin = _text(network["target_origin"], "isolation_proof.network.target_origin")
    if not re.fullmatch(r"https?://[^/@?#\s]+/?", origin):
        raise IsolationError("isolation_proof.network.target_origin: origin만(자격정보·경로·query 금지)")
    if network["mode"] == "loopback_only" and not re.fullmatch(r"https?://127\.0\.0\.1(:\d+)?/?", origin):
        raise IsolationError("isolation_proof.network.target_origin: loopback 모드는 127.0.0.1만")
    _text(network["boundary_ref"], "isolation_proof.network.boundary_ref")
    _hex(network["policy_sha256"], "isolation_proof.network.policy_sha256")
    actors = network["allowed_actors"]
    if not isinstance(actors, list) or len(set(actors)) != len(actors) or set(actors) != ALLOWED_ACTORS:
        raise IsolationError("isolation_proof.network.allowed_actors: 엔진과 등록 관측기만")
    host = _fields(proof["host"], {"operating_condition_ref"}, "isolation_proof.host")
    host_ref = _text(host["operating_condition_ref"], "isolation_proof.host.operating_condition_ref")
    if artifacts.get("isolation_evidence") is None or proof["enforcement_evidence_sha256"] != artifacts[
            "isolation_evidence"]["sha256"]:
        raise IsolationError("isolation_proof.enforcement_evidence_sha256: 근거 파일 해시와 불일치")
    preparation = _fields(proof["preparation"], {"sealed", "actions_after_pre"}, "isolation_proof.preparation")
    if preparation["sealed"] != timeline["fixture_ready"]:
        raise IsolationError("isolation_proof.preparation.sealed: fixture_ready와 불일치")
    if type(preparation["actions_after_pre"]) is not int or preparation["actions_after_pre"] != 0:
        raise IsolationError("isolation_proof.preparation.actions_after_pre: 정수 0 필요")
    observer = _fields(proof["observer"], {"ref", "registered_gets", "request_count"}, "isolation_proof.observer")
    _text(observer["ref"], "isolation_proof.observer.ref")
    gets = observer["registered_gets"]
    if not isinstance(gets, list) or not gets or len(set(gets)) != len(gets):
        raise IsolationError("isolation_proof.observer.registered_gets: 비어 있지 않은 고유 목록 필요")
    for item in gets:
        _text(item, "isolation_proof.observer.registered_gets")
    request_count = _int(observer["request_count"], "isolation_proof.observer.request_count", low=1)

    rows = _rows(evidence, receipt)
    pre_at, post_at = steps["pre"][0], steps["post"][0]
    _check_boundary(rows, network, pre_at, post_at)
    _check_snapshots(rows, network["policy_sha256"])
    _check_connectivity(rows)
    _check_host(rows, host_ref, pre_at, post_at)
    _check_preparation(rows, timeline)
    _check_observer(rows, gets, request_count, pre_at, post_at)
    summary = _one(rows, "engine_meter_summary")
    if summary["sequence_complete"] is not True or summary["forwarded_requests"] != receipt["total_requests"]:
        raise IsolationError("isolation_evidence.engine_meter_summary: 기록 불완전·요청 수 불일치")
    drain = _one(rows, "drain")
    if drain["engine_end"] != timeline["engine_end"] or type(drain["inflight"]) is not int or drain["inflight"] != 0:
        raise IsolationError("isolation_evidence.drain: engine_end 불일치 또는 진행 요청 남음")
    # 유예 뒤 끊은 요청은 전달된 요청이라 증명은 유지하되 건수·적용 값이 정수로 남아 있어야 한다(조용히 빼지 않음).
    # 유예·정착 대기는 정책(1 이상), 적용 대기는 끊은 요청이 있을 때만 정책 값이고 아니면 0이다.
    _int(drain["grace_seconds"], "isolation_evidence.drain.grace_seconds", low=1)
    settle = _int(drain["settle_seconds"], "isolation_evidence.drain.settle_seconds", low=1)
    cancelled = _int(drain["forced_cancelled"], "isolation_evidence.drain.forced_cancelled")
    applied = _int(drain["settle_applied_seconds"], "isolation_evidence.drain.settle_applied_seconds")
    if applied != (settle if cancelled else 0):
        raise IsolationError("isolation_evidence.drain.settle_applied_seconds: 끊은 요청이 있을 때만 정책 값")
    sealed_at = _time(drain["sealed_at"], "isolation_evidence.drain.sealed_at")
    drained_at = _time(drain["drained_at"], "isolation_evidence.drain.drained_at")
    if not sealed_at <= drained_at <= steps["drained"][0] <= post_at:
        raise IsolationError("isolation_evidence.drain: 봉인 ≤ drain 완료 ≤ timeline drained ≤ post 관측이어야 함")
    _check_conditions(rows)
    return proof


_SAMPLING_KEYS = frozenset({"temperature", "top_p", "top_k", "min_p", "presence_penalty", "repetition_penalty", "seed"})
_LOCAL_SAMPLING_KEYS = frozenset({"temperature", "top_p", "presence_penalty", "frequency_penalty", "seed"})
_HOST_KEYS = {"cpu_model": str, "logical_cpus": int, "memory_kib": int, "kernel": str, "docker_server_version": str}


def _check_conditions(rows: dict[str, list[dict]]) -> dict:
    row = _one(rows, "run_conditions")
    _hex(row["engine_settings_sha256"], "isolation_evidence.run_conditions.engine_settings_sha256")
    _hex(row["machine_id_sha256"], "isolation_evidence.run_conditions.machine_id_sha256")
    if row["engine_rules_sha256"] is not None:
        _hex(row["engine_rules_sha256"], "isolation_evidence.run_conditions.engine_rules_sha256")
    _int(row["workers"], "isolation_evidence.run_conditions.workers", low=1)
    if type(row["llm_stream"]) is not bool:
        raise IsolationError("isolation_evidence.run_conditions.llm_stream: bool")
    sampling = row["sampling"]
    if sampling is not None and (not isinstance(sampling, dict) or set(sampling) not in (_SAMPLING_KEYS, _LOCAL_SAMPLING_KEYS) or any(
            type(value) not in (int, float) for value in sampling.values())):
        raise IsolationError("isolation_evidence.run_conditions.sampling: 백엔드별 키의 숫자 또는 null")
    host = row["host"]
    if not isinstance(host, dict) or set(host) != set(_HOST_KEYS) or any(
            type(host[name]) is not kind or (kind is str and not host[name]) or (kind is int and host[name] < 1)
            for name, kind in _HOST_KEYS.items()):
        raise IsolationError("isolation_evidence.run_conditions.host: 머신 사양 형식 오류")
    return row


def e2_mode(receipt: dict, evidence: object) -> str:
    """격리 근거의 E② 자리 행. none이면 건수가 없고, stub이면 결정별 건수의 합이 결정 행 수와 같아야 한다."""
    row = _one(_rows(evidence, receipt), "e2_placement")
    counts = [row[key] for key in _E2_COUNTS]
    if row["mode"] == "none":
        if any(count is not None for count in counts):
            raise IsolationError("isolation_evidence.e2_placement: none에는 건수가 없어야 함")
    elif row["mode"] == "stub":
        if not all(type(count) is int and count >= 0 for count in counts) or counts[0] != sum(counts[1:]):
            raise IsolationError("isolation_evidence.e2_placement: 결정별 건수 형식 오류")
    else:
        raise IsolationError("isolation_evidence.e2_placement.mode: none|stub")
    return row["mode"]


def verdict_collection(receipt: dict, evidence: object, records: object) -> None:
    """판정 export 수집 상태를 records와 맞춰 보고, 무결성 실패면 채점을 멈춘다. 수집을 안 켠 회차에는 행이 없다.

    무결성 실패를 records=null과 같게 두면 판정 미수집(판단불가)으로 숨는다(판정 페이지 제안 §3·§6).
    """
    rows = _rows(evidence, receipt)["verdict_collection"]
    if not rows:
        return
    if len(rows) > 1:
        raise IsolationError("isolation_evidence.verdict_collection: 행은 하나")
    status, reason = rows[0]["status"], rows[0]["reason"]
    if status == "collected":
        if reason is not None or records is None:
            raise IsolationError("isolation_evidence.verdict_collection: 수집했는데 records가 없음")
    elif status in ("not_collected", "integrity_failed"):
        if not isinstance(reason, str) or not reason or records is not None:
            raise IsolationError("isolation_evidence.verdict_collection: 미수집인데 records가 있거나 사유가 없음")
    else:
        raise IsolationError("isolation_evidence.verdict_collection.status: collected|not_collected|integrity_failed")
    if status == "integrity_failed":
        raise IsolationError("판정 export 무결성 실패: 채점 중단")


def _count(value: object) -> bool:
    return type(value) is int and value >= 0


def attempt_reconciliation(receipt: dict, evidence: object) -> dict | None:
    """E② 자리가 있는 회차의 시도별 대조 건수(§8.3). E② 없음(none) 회차에는 이 행이 없어야 한다.

    파생 건수(차단 후 도달·기록 없이 도달·허용 양성 근거)와 통제 주장 보류가 다섯 칸·재사용에서 계약대로 나오는지,
    칸 합이 e2_placement 결정별 건수와 같은지 본다. 나쁜 칸이 있어도 오류가 아니다(성능표에서 빼지 않음).
    """
    mode = e2_mode(receipt, evidence)
    rows = _rows(evidence, receipt)
    if mode == "none":
        if rows["attempt_reconciliation"]:
            raise IsolationError("isolation_evidence.attempt_reconciliation: E② 없는 회차에는 없어야 함")
        return None
    row = _one(rows, "attempt_reconciliation")
    cells, reused = row["cells"], row["meter_attempt_reused"]
    if (not isinstance(cells, dict) or set(cells) != set(RECONCILE_CELLS)
            or not all(_count(cells[cell]) for cell in RECONCILE_CELLS)
            or not all(_count(row[key]) for key in ("allowed_responded", "meter_attempt_reused", "blocked_reached",
                                                    "unrecorded_reached"))
            or type(row["positive_evidence"]) is not bool or type(row["control_claim_withheld"]) is not bool):
        raise IsolationError("isolation_evidence.attempt_reconciliation: 건수 형식 오류")
    # 나쁜 칸·재사용이 있거나 허용 양성 근거가 없으면(전부 막아 0) 통제 주장을 보류한다.
    withheld = (cells["denied_observed"] > 0 or cells["unrecorded_observed"] > 0 or reused > 0
                or row["allowed_responded"] == 0)
    if (row["blocked_reached"] != cells["denied_observed"] or row["unrecorded_reached"] != cells["unrecorded_observed"]
            or row["allowed_responded"] > cells["allowed_observed"]
            or row["positive_evidence"] != (row["allowed_responded"] > 0) or row["control_claim_withheld"] != withheld):
        raise IsolationError("isolation_evidence.attempt_reconciliation: 파생 건수·보류 판정이 칸과 다름")
    placement = _one(rows, "e2_placement")
    if (cells["allowed_observed"] + cells["allowed_unobserved"] != placement["allowed"]
            or cells["denied_unobserved"] + cells["denied_observed"]
            != placement["unsupported"] + placement["unavailable"]):
        raise IsolationError("isolation_evidence.attempt_reconciliation: 칸 합이 E② 결정 건수와 다름")
    return {"cells": dict(cells), "allowed_responded": row["allowed_responded"], "meter_attempt_reused": reused,
            "blocked_reached": row["blocked_reached"],
            "unrecorded_reached": row["unrecorded_reached"], "positive_evidence": row["positive_evidence"],
            "control_claim_withheld": withheld}


def run_conditions(receipt: dict, evidence: object) -> tuple[str, str, str, str, str, str]:
    """check_isolation을 통과한 근거의 실행 조건. 채점기가 회차 사이 같은지 대조한다(평가 프로토콜 §5.1·§6)."""
    row = _one(_rows(evidence, receipt), "run_conditions")
    return (row["engine_settings_sha256"], canonical_sha256(row["sampling"]), canonical_sha256(row["host"]),
            row["machine_id_sha256"], "true" if row["llm_stream"] else "false", row["engine_rules_sha256"] or "없음")


def drain_policy(receipt: dict, evidence: object) -> tuple[int, int]:
    """check_isolation을 통과한 근거의 봉인 뒤 정책(유예, 정착 대기). 채점기가 회차 사이 같은지 대조한다."""
    drain = _one(_rows(evidence, receipt), "drain")
    return drain["grace_seconds"], drain["settle_seconds"]


def _check_boundary(rows: dict, network: dict, pre_at: datetime, post_at: datetime) -> None:
    applied, maintained = _one(rows, "boundary", event="applied"), _one(rows, "boundary", event="maintained")
    if any(row["event"] not in ("applied", "maintained", "released") for row in rows["boundary"]):
        raise IsolationError("isolation_evidence.boundary: 모르는 event")
    if sum(1 for row in rows["boundary"] if row["event"] == "released") > 1:
        raise IsolationError("isolation_evidence.boundary: released는 선택이며 한 행까지")
    for row in rows["boundary"]:
        if (row["boundary_ref"] != network["boundary_ref"] or row["policy_sha256"] != network["policy_sha256"]
                or row["network_ids"] != applied["network_ids"]):
            raise IsolationError("isolation_evidence.boundary: 경계·정책·망 ID 변경")
    if not (_time(applied["at"], "boundary.at") <= pre_at and _time(maintained["at"], "boundary.at") <= post_at):
        raise IsolationError("isolation_evidence.boundary: 적용은 pre 관측 전, 유지 확인은 post 관측 전이어야 함")


def _check_snapshots(rows: dict, policy: str) -> None:
    pre, post = _one(rows, "network_snapshot", phase="pre"), _one(rows, "network_snapshot", phase="post")
    if len(rows["network_snapshot"]) != 2:
        raise IsolationError("isolation_evidence.network_snapshot: pre·post 두 행만")
    if {k: pre[k] for k in ("networks", "publishes", "services")} != {k: post[k] for k in ("networks", "publishes",
                                                                                         "services")}:
        raise IsolationError("isolation_evidence.network_snapshot: 구간 중 경계 변경")
    try:
        recomputed = recompute_policy(pre)
    except (KeyError, TypeError):
        raise IsolationError("isolation_evidence.network_snapshot: 형식 오류") from None
    if recomputed != policy:
        raise IsolationError("isolation_evidence.network_snapshot: 정책 지문 재계산 불일치")


def _check_connectivity(rows: dict) -> None:
    for phase in ("pre", "post"):
        checks = [row for row in rows["connectivity_check"] if row["phase"] == phase]
        roles = {row["role"] for row in checks}
        if not checks or roles != {"positive", "negative"}:
            raise IsolationError("isolation_evidence.connectivity_check: 단계마다 양성·부정 점검 필요")
        if any(row["verdict"] != "match" or not _probe_matches(row) for row in checks):
            raise IsolationError("isolation_evidence.connectivity_check: 불일치 경로 있음")
    if any(row["phase"] not in ("pre", "post") for row in rows["connectivity_check"]):
        raise IsolationError("isolation_evidence.connectivity_check: 모르는 단계")


def _probe_matches(row: dict) -> bool:
    """러너가 적은 verdict를 믿지 않고 역할·기대·관측·실패 종류로 다시 판정한다(#126 리뷰).

    accepted 목록 자체가 경로에 맞는지는 러너 기대표 소관이라 여기서는 형식만 본다.
    """
    accepted = row["accepted"]
    if (row["probe_kind"] not in ("tcp", "dns") or row["role"] not in _PROBE_EXPECTED
            or not isinstance(accepted, list) or not accepted
            or any(not isinstance(code, str) or not code for code in accepted) or len(set(accepted)) != len(accepted)
            or not isinstance(row["code"], str)):
        raise IsolationError("isolation_evidence.connectivity_check: 점검 형식 오류")
    expected = _PROBE_EXPECTED[row["role"]]
    return row["expected"] == expected and row["observed"] == expected and row["code"] in accepted


def _check_host(rows: dict, host_ref: str, pre_at: datetime, post_at: datetime) -> None:
    if len(rows["host_condition"]) != 2:
        raise IsolationError("isolation_evidence.host_condition: pre·post 두 행만")
    for phase in ("pre", "post"):
        row = _one(rows, "host_condition", phase=phase)
        allowed = {f"{item['address']}:{item['port']}" for item in row["allowed_listens"]}
        if (row["operating_condition_ref"] != host_ref or row["satisfied"] is not True or row["other_publishes"] != []
                or not set(row["listeners_outside_loopback"]) <= allowed):
            raise IsolationError("isolation_evidence.host_condition: 참조 불일치 또는 조건 불충족")
    watch = _one(rows, "host_watch")
    if not (_time(watch["observed_from"], "host_watch.observed_from") <= pre_at
            and _time(watch["observed_to"], "host_watch.observed_to") >= post_at):
        raise IsolationError("isolation_evidence.host_watch: pre 관측~post 완료를 덮지 못함")
    if (_int(watch["samples"], "host_watch.samples", low=2) < 2 or watch["violations"] != 0
            or not watch["max_gap_seconds"] <= watch["allowed_max_gap_seconds"]):
        raise IsolationError("isolation_evidence.host_watch: 표본 공백 초과 또는 위반")
    events = _one(rows, "docker_events_window")
    if not (_time(events["since"], "docker_events_window.since") <= pre_at
            and _time(events["until"], "docker_events_window.until") >= post_at):
        raise IsolationError("isolation_evidence.docker_events_window: 구간을 덮지 못함")
    if events["complete"] is not True or events["foreign_attach"] != 0 or events["run_container_restarts"] != 0:
        raise IsolationError("isolation_evidence.docker_events_window: 불완전 또는 경계 변경")


def _check_preparation(rows: dict, timeline: dict) -> None:
    row = _one(rows, "preparation")
    if (row["sealed"] != timeline["fixture_ready"] or type(row["actions_after_pre"]) is not int
            or row["actions_after_pre"] != 0 or row["channels"] != []):
        raise IsolationError("isolation_evidence.preparation: 봉인 불일치 또는 pre 이후 준비")


def _check_observer(rows: dict, gets: list, request_count: int, pre_at: datetime, post_at: datetime) -> None:
    verified = {row["path_ref"] for row in rows["observer_readonly"]
                if row["method"] == "GET" and isinstance(row["verification_ref"], str) and row["verification_ref"]}
    if not set(gets) <= verified:
        raise IsolationError("isolation_evidence.observer_readonly: 등록 GET의 읽기 전용 검증 근거 없음")
    requests = rows["observer_request"]
    if len({row["request_seq"] for row in requests}) != len(requests):
        raise IsolationError("isolation_evidence.observer_request: 요청 순번 중복")
    places = [row["window"] for row in requests]
    if places.count("pre") != 1 or any(place not in ("before_pre", "pre", "in_window") for place in places):
        raise IsolationError("isolation_evidence.observer_request: 구간 분류 오류")
    for row in requests:
        if (row["method"] != "GET" or row["path_ref"] not in gets or row["decision"] != "registered"
                or type(row["status"]) is not int):
            raise IsolationError("isolation_evidence.observer_request: 등록 밖 요청 또는 미완료")
        sent, completed = _time(row["sent_at"], "observer.sent_at"), _time(row["completed_at"], "observer.completed_at")
        if completed < sent:
            raise IsolationError("isolation_evidence.observer_request: 시각 역전")
        if row["window"] == "in_window" and not (pre_at <= sent and completed <= post_at):
            raise IsolationError("isolation_evidence.observer_request: 구간 밖 송신")
    if places.count("in_window") != request_count:
        raise IsolationError("isolation_evidence.observer_request: request_count와 구간 요청 수 불일치")


def check_isolated_events(events: object, receipt: dict, proof: dict, steps: dict, pre: dict[str, bool],
                          post: dict[str, bool]) -> frozenset[str]:
    """유효 증명 아래 이벤트: 전부 engine·isolated_window·증명 digest·post 시각, 키 집합 = 신규 해결 델타."""
    if not isinstance(events, list):
        raise IsolationError("solve_events: 증명이 있으면 이벤트 배열 필요")
    digest = canonical_sha256(proof)
    post_at, post_seq = steps["post"]
    keys, ids = set(), set()
    for event in events:
        event = _fields(event, _EVENT_FIELDS, "solve event")
        if (event["actor"] != "engine" or event["basis"] != "isolated_window"
                or event["isolation_proof_sha256"] != digest or event["execution_id"] != receipt["execution_id"]):
            raise IsolationError("solve event: engine·isolated_window·증명 digest·실행 ID 필요")
        if _time(event["observed_at"], "event.observed_at") != post_at or event["sequence"] != post_seq:
            raise IsolationError("solve event: post 관측 시각·순번이어야 함")
        key, event_id = _text(event["challenge"], "event.challenge"), _text(event["id"], "event.id")
        if key in keys or event_id in ids:
            raise IsolationError("solve event 중복")
        keys.add(key)
        ids.add(event_id)
    delta = {key for key in post if post[key] and not pre.get(key, True)}
    if keys != delta:
        raise IsolationError("solve event 집합이 신규 해결 델타와 다름")
    return frozenset(keys)


def check_unproven_events(events: object) -> None:
    """증명 없는 v1.1: engine 귀속 이벤트 거부. other/unknown만, basis·digest·실행 ID는 null."""
    if events is None:
        return
    if not isinstance(events, list):
        raise IsolationError("solve_events: 배열 필요")
    for event in events:
        event = _fields(event, _EVENT_FIELDS, "solve event")
        if event["actor"] not in ("other", "unknown"):
            raise IsolationError("solve event: 격리 증명 없는 엔진 귀속 거부")
        if event["basis"] is not None or event["isolation_proof_sha256"] is not None or event["execution_id"] is not None:
            raise IsolationError("solve event: other/unknown은 basis·digest·실행 ID가 null")
