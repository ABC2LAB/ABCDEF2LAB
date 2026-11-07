"""러너 기록 v1.1 작성(run-record-v1.1.md §3·§3.1·§4, 평가 프로토콜 §4 격리 보증 구간).

완료 회차(모든 점검 통과)에서만 만든다. 러너가 실제로 확인한 근거(망 스냅샷·부정 연결 점검·호스트 조건·구간 감시·
관측 요청·계측 요약·drain)를 `isolation_evidence` 행으로 옮기고, 그 파일의 해시를 묶은 격리 증명과 pre→post
신규 해결 델타의 `isolated_window` 이벤트를 만든다. 서명은 러너가 down까지 통과한 뒤 기존 키로 한다.

판정 레코드(`records`)는 개선 후 회차에서 export를 모았을 때만 채우고, 아니면 null(판정 미수집)이다. traffic·coverage는 null이다.
챌린지 key는 평가 전용 자료이며 회차 디렉터리에만 둔다(엔진에 마운트하지 않음, 절대규칙 7).
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass

from attempt_reconcile import RESULT_FIELDS

RUN_RECORD_VERSION = "1.1"
EVIDENCE_FORMAT = "abc2lab-isolation-evidence-v1"
STEPS = ("fixture_ready", "pre", "engine_start", "engine_end", "drained", "post")
NETWORK_MODE = "dedicated_network"
# 대상에 닿을 수 있는 주체: 엔진(계측기 경유)과 등록 관측기(관측 front). 망 스냅샷·구간 이벤트로 그 밖이 없음을 보인다.
ALLOWED_ACTORS = ("engine", "observer")


class RecordError(ValueError):
    """기록 작성 입력이 계약과 맞지 않는다. 고정 코드만 담는다."""


@dataclass(frozen=True)
class RecordContext:
    run_id: str
    model_tag: str
    reset_generation: str
    instance_id: str
    execution_id: str

    def fields(self) -> dict:
        return {"run_id": self.run_id, "model_tag": self.model_tag, "reset_generation": self.reset_generation,
                "instance_id": self.instance_id, "execution_id": self.execution_id}


def canonical_sha256(value: object) -> str:
    """v1 서명 직렬화와 같은 규칙의 SHA-256(증명 digest)."""
    data = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False)
    return hashlib.sha256(data.encode("utf-8")).hexdigest()


def path_ref(path: str) -> str:
    """등록 관측 경로의 참조. 경로 자체 대신 해시를 쓴다."""
    return "sha256:" + hashlib.sha256(path.encode("utf-8")).hexdigest()


def timeline(stamps: dict[str, str]) -> dict:
    """6단계에 순번 1..6을 붙인다. 벽시각은 ISO 문자열 비교가 아니라 러너가 순서대로 찍은 값이어야 한다."""
    if set(stamps) != set(STEPS) or any(not isinstance(stamps[step], str) or not stamps[step] for step in STEPS):
        raise RecordError("record_timeline_invalid")
    return {step: {"at": stamps[step], "sequence": index} for index, step in enumerate(STEPS, start=1)}


def _network_snapshot(phase: str, at: str, snapshot: dict) -> dict:
    return {
        "kind": "network_snapshot", "at": at, "phase": phase,
        "networks": [{"name": key, "id": n["id"], "internal": n["internal"], "members": sorted(n["members"])}
                     for key, n in sorted(snapshot["networks"].items())],
        "publishes": [{"service": service, "host_ip": b[1], "host_port": b[2], "container_port": b[0]}
                      for service, s in sorted(snapshot["services"].items()) for b in s["bindings"]],
        "services": [{"service": service, "id": s["id"], "image": s["image"], "started_at": s["started_at"],
                      "restart_count": s["restart_count"]} for service, s in sorted(snapshot["services"].items())],
    }


def _host_condition(phase: str, at: str, ref: str, snapshot: dict, outside_loopback: list[str],
                    allowed: list[dict]) -> dict:
    return {"kind": "host_condition", "at": at, "phase": phase, "operating_condition_ref": ref,
            "daemon": dict(snapshot["daemon"]), "listeners_outside_loopback": sorted(outside_loopback),
            "allowed_listens": sorted(allowed, key=lambda item: (item["address"], item["port"])),
            "other_publishes": [], "satisfied": True}


def _connectivity(phase: str, at: str, row: dict) -> dict:
    """probe 행의 점검 종류(tcp·dns)는 공통 행 종류 `kind`와 겹치지 않게 `probe_kind`로 옮긴다(#127 리뷰)."""
    fields = {("probe_kind" if key == "kind" else key): value for key, value in row.items()}
    return dict(fields, kind="connectivity_check", at=at, phase=phase)


def _observer_windows(rows: list[dict], ready_calls: int) -> list[str]:
    """러너 호출 순서: 대상 준비 확인 ready_calls건 → pre 1건 → post 1건. 그 밖의 행이 있으면 기록이 맞지 않는다."""
    if len(rows) != ready_calls + 2:
        raise RecordError("record_observer_rows_invalid")
    return ["before_pre"] * ready_calls + ["pre", "in_window"]


def evidence_rows(*, pre_at: str, post_check_at: str, window: dict, pre: dict, post: dict, pre_probe: list,
                  post_probe: list, host_ref: str, pre_listeners: list[str], post_listeners: list[str],
                  allowed: list[dict], boundary_ref: str, policy: str, steps: dict, observer_rows: list[dict],
                  ready_calls: int, observer_path_ref: str, readonly_ref: str, meter: dict, drained: dict,
                  engine_rows: int, conditions: dict, e2: dict, reconciliation: dict | None = None,
                  verdict_collection: dict | None = None, e2_product: dict | None = None) -> list[dict]:
    """러너가 확인한 근거를 §3.1 행으로 옮긴다. seq는 이 함수가 붙인다."""
    network_ids = sorted(n["id"] for n in pre["networks"].values())
    rows: list[dict] = [
        {"kind": "boundary", "at": pre_at, "event": "applied", "boundary_ref": boundary_ref,
         "network_ids": network_ids, "policy_sha256": policy},
        _network_snapshot("pre", pre_at, pre),
        _host_condition("pre", pre_at, host_ref, pre, pre_listeners, allowed),
        # 회차 사이에 같아야 하는 실행 조건(엔진 설정 해시·적용 샘플링·머신). 채점기가 대조한다.
        {"kind": "run_conditions", "at": pre_at, **conditions},
    ]
    rows += [_connectivity("pre", pre_at, row) for row in pre_probe]
    rows.append({"kind": "preparation", "at": steps["fixture_ready"]["at"],
                 "sealed": dict(steps["fixture_ready"]), "actions_after_pre": 0, "channels": [],
                 "basis": ["network_snapshot", "docker_events_window", "observer_request"]})
    rows.append({"kind": "observer_readonly", "at": pre_at, "path_ref": observer_path_ref, "method": "GET",
                 "verification_ref": readonly_ref})
    for row, place in zip(observer_rows, _observer_windows(observer_rows, ready_calls)):
        rows.append({"kind": "observer_request", "at": row["completed_at"], "request_seq": row["seq"],
                     "method": row["method"], "path_ref": observer_path_ref, "sent_at": row["started_at"],
                     "completed_at": row["completed_at"], "status": row["status"], "decision": row["decision"],
                     "window": place})
    rows += [
        {"kind": "drain", "at": steps["drained"]["at"], "engine_end": dict(steps["engine_end"]),
         "sealed_at": meter["sealed_at"], "drained_at": meter["drained_at"],
         "inflight": drained.get("inflight_at_drain"), "grace_seconds": meter["drain_grace_seconds"],
         "settle_seconds": meter["post_settle_seconds"], "forced_cancelled": meter["forced_cancelled_requests"],
         "settle_applied_seconds": meter["post_settle_applied_seconds"]},
        {"kind": "engine_meter_summary", "at": steps["drained"]["at"],
         "forwarded_requests": meter["forwarded_requests"], "rejected_after_seal": meter["rejected_after_seal"],
         "forced_closed_streams": meter["forced_closed_streams"], "log_rows": engine_rows, "sequence_complete": True},
        # 개선 후 회차의 E② 자리(프로토콜 §2). none이면 건수 없음, stub이면 가짜 E² 결정 행 건수(통제 증명 아님).
        {"kind": "e2_placement", "at": steps["drained"]["at"], **e2},
    ]
    if reconciliation is not None:
        # 시도별 대조(프로토콜 §8.3) 건수. 서명 근거에 넣어 채점기가 통제 검증 표를 만든다. 시도 ID는 넣지 않는다.
        rows.append({"kind": "attempt_reconciliation", "at": steps["drained"]["at"],
                     **{field: reconciliation[field] for field in RESULT_FIELDS}})
    if e2_product is not None:
        # 제품 E② 수집 결과. 제품 실패도 유효 회차이고 대조 근거만 없다(연결 제안 §4.3).
        rows.append({"kind": "e2_product", "at": steps["drained"]["at"], **e2_product})
    if verdict_collection is not None:
        # 판정 export 수집 상태. 무결성 실패면 채점기가 표를 멈춘다(run-record-v1.1 §3.1).
        rows.append({"kind": "verdict_collection", "at": steps["drained"]["at"], **verdict_collection})
    rows += [
        _network_snapshot("post", post_check_at, post),
        _host_condition("post", post_check_at, host_ref, post, post_listeners, allowed),
    ]
    rows += [_connectivity("post", post_check_at, row) for row in post_probe]
    host, events = window["host_watch"], window["docker_events"]
    rows += [
        {"kind": "boundary", "at": post_check_at, "event": "maintained", "boundary_ref": boundary_ref,
         "network_ids": sorted(n["id"] for n in post["networks"].values()), "policy_sha256": policy},
        {"kind": "host_watch", "at": window["end"], "observed_from": window["start"], "observed_to": window["end"],
         "samples": host["samples"], "max_gap_seconds": host["max_gap_seconds"],
         "allowed_max_gap_seconds": host["allowed_max_gap_seconds"], "violations": len(host["violations"])},
        {"kind": "docker_events_window", "at": window["end"], "since": window["start"], "until": window["end"],
         "event_count": events["event_count"], "kinds": dict(events["kinds"]),
         "foreign_attach": sum(1 for e in events["boundary_events"] if e.get("service") == "foreign"),
         "run_container_restarts": sum(1 for e in events["boundary_events"] if e["type"] == "container"),
         "complete": events["complete"]},
    ]
    return [dict(row, seq=index) for index, row in enumerate(rows, start=1)]


def isolation_evidence(context: RecordContext, rows: list[dict]) -> dict:
    return dict(context.fields(), phase="engine", format=EVIDENCE_FORMAT, rows=rows)


def isolation_proof(context: RecordContext, steps: dict, pre_snapshot_sha: str, post_snapshot_sha: str,
                    target_origin: str, boundary_ref: str, policy: str, host_ref: str, evidence_sha: str,
                    observer_ref: str, observer_path_ref: str, request_count: int) -> dict:
    if type(request_count) is not int or request_count < 1:
        raise RecordError("record_observer_count_invalid")
    return dict(context.fields(),
                proof_id="proof-" + context.run_id,
                window={"pre": dict(steps["pre"]), "post": dict(steps["post"])},
                snapshots={"pre_sha256": pre_snapshot_sha, "post_sha256": post_snapshot_sha},
                network={"mode": NETWORK_MODE, "target_origin": target_origin, "boundary_ref": boundary_ref,
                         "policy_sha256": policy, "allowed_actors": list(ALLOWED_ACTORS)},
                host={"operating_condition_ref": host_ref},
                enforcement_evidence_sha256=evidence_sha,
                preparation={"sealed": dict(steps["fixture_ready"]), "actions_after_pre": 0},
                observer={"ref": observer_ref, "registered_gets": [observer_path_ref], "request_count": request_count})


def solve_events(context: RecordContext, pre_solved: dict, post_solved: dict, post_step: dict,
                 proof_sha: str) -> list[dict]:
    """신규 해결(pre=false → post=true) key마다 이벤트 하나. 델타 밖 상태(사전 해결·역행·key 불일치)는 오류."""
    if set(pre_solved) != set(post_solved):
        raise RecordError("record_snapshot_keys_mismatch")
    if any(pre_solved[key] and not post_solved[key] for key in pre_solved):
        raise RecordError("record_snapshot_regressed")
    keys = sorted(key for key in post_solved if post_solved[key] and not pre_solved[key])
    return [{"id": f"solve-{index}", "challenge": key, "actor": "engine", "execution_id": context.execution_id,
             "observed_at": post_step["at"], "sequence": post_step["sequence"], "basis": "isolated_window",
             "isolation_proof_sha256": proof_sha} for index, key in enumerate(keys, start=1)]


def descriptor(context: RecordContext, path: str, sha256: str, size: int, phase: str) -> dict:
    return dict(context.fields(), path=path, sha256=sha256, size=size, phase=phase)


def run_record(context: RecordContext, *, engine: str, condition: str, repeat_index: int, total_requests: int,
               fixture_fingerprint: str, environment_fingerprint: str, steps: dict, artifacts: dict,
               proof: dict, blocked_attempts: int | None = None) -> dict:
    """서명 전 v1.1 기록. 확정까지 시간(elapsed_seconds)은 독립 측정이 없어 null, 차단 시도 수도 null."""
    return dict(context.fields(), version=RUN_RECORD_VERSION, engine=engine, condition=condition,
                repeat_index=repeat_index, elapsed_seconds=None, total_requests=total_requests, blocked_attempts=blocked_attempts,
                fixture_fingerprint=fixture_fingerprint, environment_fingerprint=environment_fingerprint,
                requests_drained=True, timeline=steps, artifacts=artifacts, isolation_proof=proof)
