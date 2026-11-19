"""분리 배치 O 러너 기록 → 세션 집계 → 채점기 끝까지(O 공식 run-record 설계안 §8 1번).

O는 러너 회차 흐름(execute_split_run)을 가짜 포트로 실제 디렉터리에 돌려 러너 키로 서명한 묶음이다. B는 같은 작성기
(runner_record)로 만든 분리 배치 v1.1 기록이다. 합성 자료이며 실환경 격리 검증을 대신하지 않는다.
"""

from __future__ import annotations

import json
import os
import sys
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import synthetic_gt  # noqa: E402,F401  합성 정답 목록을 먼저 끼운다(정답표 노출 방지)
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from manifest import load_manifests  # noqa: E402
from run_fixture import KEYS  # noqa: E402
from run_fixture_v11 import CONDITIONS, make_v11  # noqa: E402
from run_record import load_run_inputs, sign_run_record  # noqa: E402
from runner_evidence import EvidenceDir  # noqa: E402
from runner_split_run import execute_split_run  # noqa: E402
from runner_window import WatchSample  # noqa: E402
from score import ComparabilityError, build_report  # noqa: E402
from session_runs import build_manifest, read_session  # noqa: E402
import test_runner_split_execute as o  # noqa: E402
from test_run_record_v11 import row_of  # noqa: E402
from test_score import _RunDir  # noqa: E402

RUN_ID = "ours-20261119t010000z-0000000a"
SOLVED_KEY = sorted(KEYS)[0]
PATH_KEY = "0123456789abcdef"
GOAL = "7" * 64
FIXTURE = __import__("hashlib").sha256(b"fixture-v1").hexdigest()
ENVIRONMENT = __import__("hashlib").sha256(b"environment-v1").hexdigest()


class _DiskPorts(o.FakePorts):
    """점수판 파일을 회차 디렉터리에 실제로 쓰고(러너 수집기와 같은 원본·정규화 모양) 러너 키로 서명한다."""

    def __init__(self, clock, run_dir: Path, key: bytes):
        super().__init__(clock)
        self.run_dir, self.key = run_dir, key
        self.board_at: dict[str, str] = {}
        self.record["goal"] = dict(o.GOAL, common_sha256=GOAL)

    def _write(self, relative: str, value: object) -> dict:
        data = json.dumps(value).encode()
        path = self.run_dir / relative
        path.parent.mkdir(mode=0o700, exist_ok=True)
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as out:
            out.write(data)
        return {"sha256": __import__("hashlib").sha256(data).hexdigest(), "size": len(data)}

    def start_host_watch(self):
        # 채점기는 구간 감시 표본이 둘 이상이어야 받는다
        self._call("watch:start")
        ports = self

        class Handle:
            def stop(self):
                ports.calls.append("watch:stop")
                return 0.0, 2.0, [WatchSample(0.5, (), None), WatchSample(1.5, (), None)]
        return Handle()

    def stamp(self) -> str:
        return (o.START + o.timedelta(seconds=self.clock.t)).isoformat()

    def scoreboard(self, phase, snap):
        self._call(f"scoreboard:{phase}")
        self.board_at[phase] = self.stamp()
        context = {"run_id": RUN_ID, "model_tag": "test-model", "reset_generation": snap["services"]["target"]["id"],
                   "instance_id": snap["services"]["target"]["id"], "execution_id": snap["services"]["preparation"]["id"]}
        solved = {key: phase == "post" and key == SOLVED_KEY for key in sorted(KEYS)}
        raw = self._write(f"{phase}/raw.json", {"data": [{"key": k, "solved": v} for k, v in solved.items()]})
        normalized = self._write(f"{phase}/normalized.json", dict(context, phase=phase, solved=solved))
        at = (o.START + o.timedelta(seconds=self.clock.t)).isoformat()
        return {"raw": dict(context, path=f"{phase}/raw.json", phase=phase, **raw),
                "normalized": dict(context, path=f"{phase}/normalized.json", phase=phase, **normalized),
                "requested_at": at, "received_at": at, "solved": solved, "state_sha256": "s" * 64}

    def sign(self, payload):
        self._call("sign")
        return sign_run_record(payload, self.key)


class _DiskMeter(o.FakeMeter):
    """봉인·drain 표식과 관측기 GET 행의 시각을 시험 시계로 낸다(채점기가 시각 순서를 다시 본다)."""

    def marker(self, name):
        value = super().marker(name)
        if name == "engine-sealed.json":
            return dict(value, sealed_at=(o.START + o.timedelta(seconds=self.ports.sealed_t)).isoformat())
        return dict(value, drained_at=self.ports.stamp())

    def observer_rows(self):
        return [{"seq": i, "decision": "registered", "method": "GET", "status": 200, "started_at": self.ports.board_at[phase],
                 "completed_at": self.ports.board_at[phase], "response_bytes": 10}
                for i, phase in enumerate(("pre", "post"), start=1)]


class TestSplitRecordEndToEnd(_RunDir):
    def o_run(self, root: Path) -> dict:
        run_dir = root / RUN_ID
        run_dir.mkdir(mode=0o700)
        clock = o.FakeClockState()
        ports = _DiskPorts(clock, run_dir, self.runner_key)
        ports.solved = {"pre": {}, "post": {}}
        ports.host_spec = lambda: dict(CONDITIONS["host"], machine_id_sha256=CONDITIONS["machine_id_sha256"])
        spec = o.spec(run_id=RUN_ID, purpose="official", path_key_ref=PATH_KEY, model_tag="test-model", condition="우리@test",
                      fixture_fingerprint=FIXTURE, environment_fingerprint=ENVIRONMENT, drain_grace_seconds=30, post_settle_seconds=2,
                      llm_sampling=dict(CONDITIONS["sampling"]))
        ports.marker = o.product_marker(sampling=dict(CONDITIONS["sampling"]))
        ports.record["model"]["sampling"] = dict(CONDITIONS["sampling"])
        meter = _DiskMeter(ports)
        meter.path_key_ref = PATH_KEY
        return execute_split_run(spec, ports, meter, EvidenceDir(run_dir), clock.clock())

    def b_run(self, stream: bool, name: str = "b", repeat_index: int = 0) -> dict:
        def tamper(rows):
            row_of(rows, "run_conditions").update(split=True, path_key_ref=PATH_KEY, goal_common_sha256=GOAL, llm_stream=stream)
        return make_v11(Path(self._tmp.name), self.runner_key, name, condition="원본@test", engine="baseline", tamper_rows=tamper,
                        repeat_index=repeat_index)

    def o_manifest(self):
        root = Path(self._tmp.name) / "runs"
        root.mkdir(mode=0o700)
        result = self.o_run(root)
        self.assertEqual(result["status"], "complete", result)
        # 세션 집계: 서명된 결과·기록을 읽고 manifest를 만든다(경로는 회차 디렉터리 기준 절대경로)
        path = Path(self._tmp.name) / "o-manifest.json"
        path.write_text(json.dumps(build_manifest(root, read_session(root, self.runner_key))))
        (manifest,) = load_manifests(str(path))
        return manifest

    def test_runner_signed_o_record_reaches_the_scorer(self):
        o_manifest = self.o_manifest()
        inputs = load_run_inputs(o_manifest, self.runner_key, self.correlate)
        # 격리 검증 통과, 신규 해결 하나가 isolated_window로 엔진에 귀속된다
        self.assertEqual((inputs.e2_mode, inputs.attempt_reconciliation, inputs.engine_solved),
                         ("split_product", None, frozenset({SOLVED_KEY})))
        self.assertEqual((inputs.purpose, inputs.e3, inputs.goal_common_sha256),
                         ("official", (True, ("disabled", "declaration_missing")), GOAL))
        # 실제 짝: B 원본 기본값 스트리밍(참)과 제품 비스트리밍(거짓). LLM 스트리밍은 선언된 차이라 조건 안에서만 같으면 된다
        # (2026-11-19 사용자 결정, 고정 초안 §18) → 표가 나오고 O 통제 표는 대조 미수집이다
        report = build_report(self.load([self.b_run(True)]) + [o_manifest], self.correlate, self.correlatable,
                              runner_key=self.runner_key)
        self.assertIn("대조 미수집 — 통제 주장 보류(회차 1/1)", report.text)

    def test_streaming_must_match_within_a_condition(self):
        # 같은 쪽(B) 안에서 스트리밍이 섞이면 조건 안 비교에서 거부한다
        o_manifest = self.o_manifest()
        b_runs = self.load([self.b_run(True), self.b_run(False, "b2", 1)])
        with self.assertRaisesRegex(ComparabilityError, "LLM 스트리밍, 원본@test 안"):
            build_report(b_runs + [o_manifest], self.correlate, self.correlatable, runner_key=self.runner_key)


if __name__ == "__main__":
    unittest.main()
