"""러너 기록 v1.1 합성 회차. 러너 작성기(runner_record)로 근거 행·증명·이벤트를 만들어 채점기와 맞물리는지도 본다.

tamper_* 훅은 해당 단계의 객체를 바꾼 뒤 이후 단계(해시·digest·서명)를 그대로 다시 계산한다 — 한 결함만 남긴다.
"""

from __future__ import annotations

import copy
import hashlib
import json
from pathlib import Path

from run_record import sign_run_record
from runner_checks import policy_sha256
from runner_record import (
    RecordContext, canonical_sha256, descriptor, evidence_rows, isolation_evidence, isolation_proof, path_ref,
    run_record, solve_events, timeline,
)
from run_fixture import KEYS

OBSERVER_PATH = "/api/Challenges"
STAMPS = {"fixture_ready": "2026-10-30T00:00:00+00:00", "pre": "2026-10-30T00:00:01+00:00",
          "engine_start": "2026-10-30T00:00:02+00:00", "engine_end": "2026-10-30T00:00:08+00:00",
          "drained": "2026-10-30T00:00:09+00:00", "post": "2026-10-30T00:00:10+00:00"}


def snapshot(name: str) -> dict:
    """runner_checks.network_snapshot 모양의 합성 망 스냅샷(엔진·계측기·대상·관측 front)."""
    ids = {service: hashlib.sha256(f"{name}-{service}".encode()).hexdigest()
           for service in ("engine", "eval-meter", "target", "eval-obs")}
    members = {"engine_net": ["engine", "eval-meter"], "target_net": ["target", "eval-meter", "eval-obs"],
               "obs_net": ["eval-obs"]}
    return {
        "daemon": {"version": "29.1.3", "os": "linux", "arch": "amd64", "platform": ""},
        "project": f"eval-{name}",
        "networks": {key: {"id": hashlib.sha256(f"{name}-{key}".encode()).hexdigest(), "name": f"eval-{name}_{key}",
                           "internal": key != "obs_net", "gateways": [], "members": {s: ["x"] for s in services},
                           "names": {}} for key, services in members.items()},
        "services": {service: {"id": cid, "image": "sha256:" + "9" * 64, "running": True,
                               "started_at": "2026-10-30T00:00:00Z", "restart_count": 0, "exposed": [],
                               "bindings": [["8081/tcp", "127.0.0.1", "18081"]] if service == "eval-obs" else []}
                     for service, cid in ids.items()},
    }


PROBES = [
    {"id": "meter_tcp", "kind": "tcp", "target": "eval-meter:8080", "role": "positive", "expected": "success",
     "accepted": ["connected"], "observed": "success", "code": "connected", "verdict": "match"},
    {"id": "dns:target", "kind": "dns", "target": "target", "role": "negative", "expected": "fail",
     "accepted": ["not_found", "dns_error"], "observed": "fail", "code": "dns_error", "verdict": "match"},
]


def make_v11(root: Path, key: bytes, name: str, *, pre=None, solved=None, records=None, proof=True,
             total_requests=5, tamper_rows=None, tamper_proof=None, tamper_events=None, tamper_record=None,
             **overrides) -> dict:
    """v1.1 회차 디렉터리와 manifest 항목. records=None이면 판정 미수집, proof=False면 증명·근거 null."""
    folder = root / name
    folder.mkdir()
    run = dict(condition="우리@test", engine="ours", run_id=f"run-{name}", model_tag="test-model",
               reset_generation=f"gen-{name}", repeat_index=0, records_path=f"{name}/records.json",
               snapshot_path=f"{name}/snapshot.json", pre_snapshot_path=f"{name}/pre.json",
               run_record_path=f"{name}/runner.json", total_requests=total_requests)
    run.update(overrides)
    context = RecordContext(run["run_id"], run["model_tag"], run["reset_generation"], f"instance-{name}",
                            f"execution-{name}")
    steps = timeline(STAMPS)
    before = {k: False for k in KEYS} | (pre or {})
    after = {k: False for k in KEYS} | (solved or {})
    artifacts: dict = {"records": None, "traffic": None, "coverage": None, "solve_events": None,
                       "isolation_evidence": None}

    def add(artifact: str, value: object, filename: str, phase: str) -> dict:
        data = json.dumps(value).encode()
        (folder / filename).write_bytes(data)
        artifacts[artifact] = descriptor(context, filename, hashlib.sha256(data).hexdigest(), len(data), phase)
        return artifacts[artifact]

    for phase, states, filename in (("pre", before, "pre.json"), ("post", after, "snapshot.json")):
        add(phase, dict(context.fields(), phase=phase, solved=states), filename, phase)
        add(phase + "_raw", {"data": [{"key": k, "solved": v} for k, v in states.items()]}, f"{phase}-raw.json", phase)
    if records is not None:
        add("records", dict(run_id=run["run_id"], model_tag=run["model_tag"], reset_generation=run["reset_generation"],
                            records=[dict(r, reset_generation=run["reset_generation"]) for r in records]),
            "records.json", "engine")
    proof_obj = None
    if proof:
        snap = snapshot(name)
        policy = policy_sha256(snap)
        window = {"start": "2026-10-30T00:00:00.900000000Z", "end": "2026-10-30T00:00:10.500000000Z",
                  "host_watch": {"samples": 6, "max_gap_seconds": 2.0, "allowed_max_gap_seconds": 15,
                                 "violations": []},
                  "docker_events": {"event_count": 10, "kinds": {"container.kill": 2}, "boundary_events": [],
                                    "complete": True}}
        observer = [{"seq": i + 1, "decision": "registered", "method": "GET", "status": 200, "started_at": s,
                     "completed_at": c, "response_bytes": 10}
                    for i, (s, c) in enumerate((("2026-10-30T00:00:00.200Z", "2026-10-30T00:00:00.300Z"),
                                                ("2026-10-30T00:00:00.950Z", "2026-10-30T00:00:01Z"),
                                                ("2026-10-30T00:00:09.500Z", "2026-10-30T00:00:10Z")))]
        meter = {"forwarded_requests": total_requests, "rejected_after_seal": 0, "forced_closed_streams": 0,
                 "forced_cancelled_requests": 0, "drain_grace_seconds": 30, "post_settle_seconds": 2,
                 "post_settle_applied_seconds": 0,
                 "sealed_at": "2026-10-30T00:00:08Z", "drained_at": "2026-10-30T00:00:09Z"}
        rows = evidence_rows(
            pre_at="2026-10-30T00:00:00.800000+00:00", post_check_at="2026-10-30T00:00:09.200000+00:00",
            window=window, pre=snap, post=copy.deepcopy(snap), pre_probe=PROBES, post_probe=PROBES,
            host_ref=f"host-condition:{run['run_id']}", pre_listeners=[], post_listeners=[], allowed=[],
            boundary_ref=f"compose:eval-{name}", policy=policy, steps=steps, observer_rows=observer, ready_calls=1,
            observer_path_ref=path_ref(OBSERVER_PATH), readonly_ref="verify:juice-shop-15:get-challenges",
            meter=meter, drained={"inflight_at_drain": 0}, engine_rows=total_requests)
        if tamper_rows:
            tamper_rows(rows)
        evidence = add("isolation_evidence", isolation_evidence(context, rows), "isolation-evidence.json", "engine")
        in_window = sum(1 for row in rows if row["kind"] == "observer_request" and row["window"] == "in_window")
        proof_obj = isolation_proof(context, steps, artifacts["pre"]["sha256"], artifacts["post"]["sha256"],
                                    "http://eval-meter:8080", f"compose:eval-{name}", policy,
                                    f"host-condition:{run['run_id']}", evidence["sha256"], "eval-obs:obs",
                                    path_ref(OBSERVER_PATH), max(in_window, 1))
        if tamper_proof:
            tamper_proof(proof_obj)
        events = solve_events(context, before, after, steps["post"], canonical_sha256(proof_obj))
    else:
        events = []
    if tamper_events:
        tamper_events(events)
    if proof or events:
        add("solve_events", dict(context.fields(), phase="engine", events=events), "solve-events.json", "engine")
    record = run_record(context, engine=run["engine"], condition=run["condition"], repeat_index=run["repeat_index"],
                        total_requests=total_requests, fixture_fingerprint=hashlib.sha256(b"fixture-v1").hexdigest(),
                        environment_fingerprint=hashlib.sha256(b"environment-v1").hexdigest(), steps=steps,
                        artifacts=artifacts, proof=proof_obj)
    if tamper_record:
        tamper_record(record)
    (folder / "runner.json").write_text(json.dumps(sign_run_record(record, key)))
    return run
