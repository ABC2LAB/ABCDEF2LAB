"""합성 러너 자료만 임시 디렉터리에 생성한다. 실제 스캔·비밀은 사용하지 않는다."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path

from ground_truth import bac_category, core_gt
from run_record import sign_run_record

KEYS = sorted({c.key for c in core_gt() + bac_category()})
RESOURCE = {"basketAccessChallenge": "rest:basket:1", "forgedFeedbackChallenge": "rest:feedback:1"}
CONTEXT = ("run_id", "model_tag", "reset_generation", "instance_id", "execution_id")


class RunFixture:
    def __init__(self, root: str, key: bytes):
        self.root = Path(root)
        self.key = key

    def make(self, name: str, records: list[dict], solved=None, drop_keys=(), *,
             pre=None, actor="engine", **overrides) -> dict:
        folder = self.root / name
        folder.mkdir()
        run = dict(condition="우리@test", engine="ours", run_id=f"run-{name}", model_tag="test-model",
                   reset_generation=f"gen-{name}", repeat_index=0, records_path=f"{name}/records.json",
                   snapshot_path=f"{name}/snapshot.json", pre_snapshot_path=f"{name}/pre.json",
                   run_record_path=f"{name}/runner.json")
        run.update(overrides)
        context = {field: run[field] for field in ("run_id", "model_tag", "reset_generation")}
        context.update(instance_id=f"instance-{name}", execution_id=f"execution-{name}")
        receipt = dict(context, version=1, engine=run["engine"],
                       **{field: run.get(field) for field in ("condition", "repeat_index", "elapsed_seconds",
                                                            "total_requests", "blocked_attempts")}, fixture_fingerprint=hashlib.sha256(b"fixture-v1").hexdigest(),
                       environment_fingerprint=hashlib.sha256(b"environment-v1").hexdigest(), requests_drained=True,
                       timeline={step: {"at": f"2026-10-30T00:00:{second:02d}+00:00", "sequence": second * 10}
                                 for step, second in (("fixture_ready", 0), ("pre", 1), ("engine_start", 2),
                                                      ("engine_end", 8), ("drained", 9), ("post", 10))},
                       artifacts={field: None for field in ("traffic", "coverage", "solve_events")})
        before = {key: False for key in KEYS} | (pre or {})
        after = {key: False for key in KEYS} | (solved or {})
        for key in drop_keys:
            del after[key]
        for phase, states in (("pre", before), ("post", after)):
            filename = "pre.json" if phase == "pre" else "snapshot.json"
            self._add(folder, receipt, phase, dict(context, phase=phase, solved=states), filename)
            self._add(folder, receipt, phase + "_raw",
                      {"data": [{"key": key, "solved": value} for key, value in states.items()]}, phase + "-raw.json")
        stamped = [dict(record, reset_generation=record.get("reset_generation", run["reset_generation"]))
                   for record in records]
        self._add(folder, receipt, "records", dict(run_id=run["run_id"], model_tag=run["model_tag"],
                                                 reset_generation=run["reset_generation"], records=stamped), "records.json")
        if actor is not None:
            events = [dict(id=f"event-{index}", challenge=key, actor=actor,
                           execution_id=context["execution_id"] if actor == "engine" else None,
                           observed_at="2026-10-30T00:00:05+00:00", sequence=50 + index)
                      for index, key in enumerate(KEYS) if not before[key] and after.get(key)]
            self._add(folder, receipt, "solve_events", dict(context, phase="engine", events=events), "events.json")
        self._write_receipt(run, receipt)
        return run

    def receipt(self, run: dict) -> dict:
        payload = json.loads((self.root / run["run_record_path"]).read_bytes())
        payload.pop("signature", None)
        return payload

    def _write_receipt(self, run: dict, receipt: dict) -> None:
        (self.root / run["run_record_path"]).write_text(json.dumps(sign_run_record(receipt, self.key)))

    def _add(self, folder: Path, receipt: dict, name: str, value: object, filename: str) -> None:
        data = json.dumps(value).encode()
        (folder / filename).write_bytes(data)
        phase = "pre" if name.startswith("pre") else "post" if name.startswith("post") else "engine"
        receipt["artifacts"][name] = dict(
            {field: receipt[field] for field in CONTEXT}, phase=phase,
            path=filename, sha256=hashlib.sha256(data).hexdigest(), size=len(data))

    def reseal(self, run: dict) -> None:
        """내부 검증 회귀를 위해 의도적으로 변경한 합성 파일 해시를 다시 기록한다."""
        receipt = self.receipt(run)
        folder = (self.root / run["run_record_path"]).parent
        for descriptor in receipt["artifacts"].values():
            if descriptor is not None:
                data = (folder / descriptor["path"]).read_bytes()
                descriptor.update(size=len(data), sha256=hashlib.sha256(data).hexdigest())
        self._write_receipt(run, receipt)

    def evidence(self, run: dict, traffic=None, covered=None) -> None:
        receipt = self.receipt(run)
        folder = (self.root / run["run_record_path"]).parent
        context = {field: receipt[field] for field in CONTEXT}
        refs = dict(traffic or {})
        for index, key in enumerate(sorted(covered or ())):
            refs[f"coverage-{index}"] = RESOURCE.get(key, f"unmapped:{key}:1")
        if traffic is not None or covered is not None:
            requests = [dict(id=ref, resource_ref=resource, vuln_class="idor",
                             sent_at="2026-10-30T00:00:03+00:00", completed_at="2026-10-30T00:00:04+00:00")
                        for ref, resource in refs.items()]
            self._add(folder, receipt, "traffic", dict(context, phase="engine", requests=requests), "traffic.json")
        if covered is not None:
            inspections = [dict(traffic_ref=f"coverage-{index}", completed=True)
                           for index, _ in enumerate(sorted(covered))]
            self._add(folder, receipt, "coverage", dict(context, phase="engine", inspections=inspections), "coverage.json")
        self._write_receipt(run, receipt)
