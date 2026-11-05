"""러너 기록 v1.1 채점 입력 회귀(run-record-v1.1.md §5 IW01~IW17, 프로토콜 §4-7). 합성 자료이며 실환경 격리 검증을 대신하지 않는다."""

from __future__ import annotations

import json
import os
import sys
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from aggregate import ScanVerdict
from isolation_proof import recompute_policy
from run_record import RunRecordError, load_run_inputs
from runner_checks import policy_sha256
from runner_record import _network_snapshot
from score import ComparabilityError, build_report, score_run
from run_fixture_v11 import make_v11, snapshot
from test_score import _RunDir, _record

KEY = "basketAccessChallenge"


def drop(rows, row):
    """행을 지우고 순번을 다시 매긴다(순번 공백이 아니라 그 행의 부재만 남긴다)."""
    rows.remove(row)
    for index, item in enumerate(rows, start=1):
        item["seq"] = index


def row_of(rows, kind, **match):
    return next(r for r in rows if r["kind"] == kind and all(r.get(k) == v for k, v in match.items()))


class _V11(_RunDir):
    def v11(self, name="a", **kwargs):
        return make_v11(Path(self._tmp.name), self.runner_key, name, **kwargs)

    def inputs(self, run):
        return load_run_inputs(self.load([run])[0], self.runner_key, self.correlate)

    def reject(self, pattern, name="a", **kwargs):
        with self.assertRaisesRegex(RunRecordError, pattern):
            self.inputs(self.v11(name, **kwargs))


class TestValidV11(_V11):
    def test_iw02_valid_proof_attributes_exactly_the_new_solves(self):
        inputs = self.inputs(self.v11(solved={KEY: True}))
        self.assertEqual((inputs.version, inputs.proof_id, inputs.engine_solved), ("1.1", "proof-run-a",
                                                                                  frozenset({KEY})))
        self.assertFalse(inputs.judgments_collected)  # records=null

    def test_iw13_empty_delta_and_unproven_run(self):
        self.assertEqual(self.inputs(self.v11()).engine_solved, frozenset())
        unproven = self.inputs(self.v11("b", solved={KEY: True}, proof=False))
        self.assertEqual((unproven.proof_id, unproven.engine_solved), (None, frozenset()))
        scored = score_run(self.load([self.v11("c", solved={KEY: True}, proof=False)])[0], self.correlate,
                           runner_key=self.runner_key, correlatable=self.correlatable)
        self.assertTrue(any("격리 증명 없음" in w for w in scored.warnings))
        self.assertTrue(any("해결 귀속 미확인" in w and KEY in w for w in scored.warnings))

    def test_iw16_uncollected_judgments_are_all_undecidable(self):
        scored = score_run(self.load([self.v11(solved={KEY: True})])[0], self.correlate,
                           runner_key=self.runner_key, correlatable=self.correlatable)
        self.assertTrue(set(scored.result.verdicts.values()) == {ScanVerdict.UNDECIDABLE})
        self.assertIn(KEY, scored.result.engine_solved)
        self.assertTrue(any("판정 미수집" in w for w in scored.warnings))
        # 판정 레코드가 있으면 기존 어댑터로 읽는다.
        collected = self.inputs(self.v11("b", records=[_record("confirmed", "rest:basket:1")]))
        self.assertTrue(collected.judgments_collected)

    def test_policy_recompute_matches_runner(self):
        snap = snapshot("drift")
        self.assertEqual(recompute_policy(_network_snapshot("pre", "t", snap)), policy_sha256(snap))


class TestRejectedV11(_V11):
    def test_iw14_version_and_shape(self):
        self.reject("version", tamper_record=lambda r: r.update(version=1))
        self.reject("version", "b", tamper_record=lambda r: r.update(version="1.0"))
        self.reject("version", "c", tamper_record=lambda r: r.update(version=1.1))
        self.reject("version", "d", tamper_record=lambda r: r.pop("isolation_proof"))

    def test_iw04_partial_or_missing_proof_material(self):
        self.reject("둘 다", tamper_record=lambda r: r["artifacts"].update(isolation_evidence=None))
        self.reject("이벤트 배열", "b", tamper_record=lambda r: r["artifacts"].update(solve_events=None))
        self.reject("digest", "c", solved={KEY: True},
                    tamper_events=lambda e: e[0].update(isolation_proof_sha256="0" * 64))

    def test_iw04_tampered_after_signing(self):
        run = self.v11()
        path = Path(self._tmp.name, run["run_record_path"])
        record = json.loads(path.read_text())
        record["isolation_proof"]["observer"]["request_count"] = 9
        path.write_text(json.dumps(record))
        with self.assertRaisesRegex(RunRecordError, "서명"):
            self.inputs(run)

    def test_iw05_context_and_snapshot_binding(self):
        self.reject("문맥", tamper_proof=lambda p: p.update(run_id="other-run"))
        self.reject("해시", "b", tamper_proof=lambda p: p["snapshots"].update(pre_sha256="0" * 64))
        self.reject("window", "c", tamper_proof=lambda p: p["window"].update(pre={"at": "2026-10-30T00:00:01+00:00",
                                                                                  "sequence": 99}))

    def test_iw06_events_must_equal_the_delta(self):
        # 사전 해결 항목(pre=true)에 대한 이벤트: digest·시각은 올바르게 복사하고 키만 델타 밖으로.
        presolved = "feedbackChallenge"
        self.reject("델타", pre={presolved: True}, solved={KEY: True, presolved: True},
                    tamper_events=lambda e: e.append(dict(e[0], id="solve-x", challenge=presolved)))
        self.reject("델타", "b", solved={KEY: True}, tamper_events=lambda e: e.clear())
        self.reject("중복", "c", solved={KEY: True}, tamper_events=lambda e: e.append(dict(e[0], id="solve-y")))

    def test_iw03_iw11_event_shape(self):
        self.reject("isolated_window", solved={KEY: True}, tamper_events=lambda e: e[0].update(basis="direct_event"))
        self.reject("post", "b", solved={KEY: True},
                    tamper_events=lambda e: e[0].update(observed_at="2026-10-30T00:00:05+00:00"))
        self.reject("post", "c", solved={KEY: True}, tamper_events=lambda e: e[0].update(sequence=5))

    def test_iw07_host_and_daemon_window(self):
        self.reject("host_watch", tamper_rows=lambda rows: row_of(rows, "host_watch").update(max_gap_seconds=20))
        self.reject("host_watch", "b", tamper_rows=lambda rows: row_of(rows, "host_watch").update(
            observed_from="2026-10-30T00:00:01.500000000Z"))
        self.reject("host_condition", "c", tamper_rows=lambda rows: row_of(rows, "host_condition", phase="post").update(
            operating_condition_ref="host-condition:other"))
        self.reject("host_condition", "d", tamper_rows=lambda rows: row_of(rows, "host_condition", phase="pre").update(
            listeners_outside_loopback=["0.0.0.0:3000"]))
        self.reject("docker_events_window", "e",
                    tamper_rows=lambda rows: row_of(rows, "docker_events_window").update(complete=False))
        self.reject("docker_events_window", "f",
                    tamper_rows=lambda rows: row_of(rows, "docker_events_window").update(foreign_attach=1))

    def test_iw08_preparation(self):
        self.reject("preparation", tamper_rows=lambda rows: row_of(rows, "preparation").update(actions_after_pre=1))
        self.reject("preparation", "b", tamper_proof=lambda p: p["preparation"].update(actions_after_pre=False))
        self.reject("preparation", "c", tamper_rows=lambda rows: row_of(rows, "preparation").update(channels=["x"]))

    def test_iw09_observer_requests(self):
        self.reject("observer_request", tamper_rows=lambda rows: row_of(rows, "observer_request",
                                                                       window="in_window").update(method="POST"))
        self.reject("observer_request", "b", tamper_rows=lambda rows: row_of(rows, "observer_request",
                                                                            window="in_window").update(path_ref="x"))
        self.reject("observer_readonly", "c", tamper_rows=lambda rows: drop(rows, row_of(rows, "observer_readonly")))
        self.reject("구간 밖", "d", tamper_rows=lambda rows: row_of(rows, "observer_request", window="in_window").update(
            completed_at="2026-10-30T00:00:11Z"))

    def test_iw10_observer_count(self):
        self.reject("request_count", tamper_proof=lambda p: p["observer"].update(request_count=2))
        self.reject("request_count", "b", tamper_proof=lambda p: p["observer"].update(request_count=True))
        self.reject("request_count", "c", tamper_proof=lambda p: p["observer"].update(request_count=0))

    def test_iw12_engine_requests_and_drain(self):
        self.reject("engine_meter_summary", tamper_rows=lambda rows: row_of(rows, "engine_meter_summary").update(
            forwarded_requests=6))
        self.reject("drain", "b", tamper_rows=lambda rows: row_of(rows, "drain").update(inflight=1))
        # 유예 뒤 끊은 요청 수·적용 값은 정수로 남아야 한다. 끊은 요청이 있어도 전달된 요청이라 증명은 유지한다.
        # 유예·정착 대기는 정책이라 1 이상이다.
        for index, (name, value) in enumerate((("forced_cancelled", -1), ("forced_cancelled", True),
                                               ("grace_seconds", "30"), ("settle_seconds", None),
                                               ("grace_seconds", 0), ("settle_seconds", 0),
                                               ("settle_applied_seconds", -1), ("settle_applied_seconds", 2.0))):
            self.reject("drain", f"c{index}", tamper_rows=lambda rows, n=name, v=value: row_of(rows, "drain").update(
                {n: v}))
        for index, name in enumerate(("forced_cancelled", "settle_applied_seconds")):
            self.reject("drain", f"d{index}", tamper_rows=lambda rows, n=name: row_of(rows, "drain").pop(n))
        # 적용 대기는 끊은 요청이 있을 때만 정책 값이고 아니면 0이다(설정 정책과 적용 결과를 구분).
        for index, change in enumerate((dict(settle_applied_seconds=2), dict(forced_cancelled=1),
                                        dict(forced_cancelled=1, settle_applied_seconds=3))):
            self.reject("settle_applied_seconds", f"e{index}",
                        tamper_rows=lambda rows, c=change: row_of(rows, "drain").update(c))
        cut = self.inputs(self.v11("cut", tamper_rows=lambda rows: row_of(rows, "drain").update(
            forced_cancelled=1, settle_applied_seconds=2)))
        self.assertEqual((cut.version, cut.drain_policy), ("1.1", (30, 2)))

    def test_run_conditions_row_is_required_and_typed(self):
        self.reject("run_conditions", "r0", tamper_rows=lambda rows: drop(rows, row_of(rows, "run_conditions")))
        cases = {"engine_settings_sha256": "x", "machine_id_sha256": "E" * 64, "workers": 0,
                 "sampling": {"temperature": 1.0}, "host": {"cpu_model": "x"}, "llm_stream": "true"}
        for index, (name, value) in enumerate(cases.items()):
            self.reject("run_conditions", f"r{index + 1}",
                        tamper_rows=lambda rows, n=name, v=value: row_of(rows, "run_conditions").update({n: v}))
        self.reject("run_conditions", "r9", tamper_rows=lambda rows: row_of(rows, "run_conditions")["host"].update(
            logical_cpus=True))
        self.reject("run_conditions", "r10", tamper_rows=lambda rows: row_of(rows, "run_conditions")["sampling"].update(
            seed="42"))
        # local 백엔드는 샘플링을 넣지 않아 null이다.
        local = self.inputs(self.v11("loc", tamper_rows=lambda rows: row_of(rows, "run_conditions").update(
            sampling=None)))
        self.assertEqual(local.version, "1.1")
        self.assertIsNotNone(self.inputs(self.v11("ok")).run_conditions)
        # 스트리밍은 bool만 받는다(1·"true"를 true로 읽지 않음). 행에서 빠져도 거부.
        self.reject("run_conditions", "r11", tamper_rows=lambda rows: row_of(rows, "run_conditions").update(llm_stream=1))
        self.reject("run_conditions", "r12", tamper_rows=lambda rows: row_of(rows, "run_conditions").pop("llm_stream"))

    def test_iw12_drain_times_are_checked_against_the_window(self):
        # fixture: 봉인 00:08, drain 완료 00:09, timeline drained 00:09, post 00:10 — 경계값(같은 시각)은 통과
        self.assertEqual(self.inputs(self.v11()).version, "1.1")
        # 진행 요청 0에서 봉인하면 계측기는 봉인·drain 완료를 같은 시각으로 적는다(실측 회차 모양).
        self.assertEqual(self.inputs(self.v11("same", tamper_rows=lambda rows: row_of(rows, "drain").update(
            sealed_at="2026-10-30T00:00:09.000000000Z", drained_at="2026-10-30T00:00:09.000000000Z"))).version, "1.1")
        cases = {"drained_at 뒤 봉인": dict(sealed_at="2026-10-30T00:00:09.500Z"),
                 "timeline drained 뒤 drain 완료": dict(drained_at="2026-10-30T00:00:09.100Z"),
                 "post 뒤 drain 완료": dict(drained_at="2026-10-30T01:00:10Z"),
                 "timezone 없음": dict(drained_at="2026-10-30T00:00:09"),
                 "시각 아님": dict(sealed_at="sealed")}
        for index, (label, change) in enumerate(cases.items()):
            with self.subTest(label):
                self.reject("drain", f"t{index}", tamper_rows=lambda rows, c=change: row_of(rows, "drain").update(c))

    def test_iw13_unproven_run_cannot_claim_engine_solves(self):
        event = {"id": "solve-1", "challenge": KEY, "actor": "engine", "execution_id": "execution-a",
                 "observed_at": "2026-10-30T00:00:10+00:00", "sequence": 6, "basis": "isolated_window",
                 "isolation_proof_sha256": "0" * 64}
        self.reject("엔진 귀속", solved={KEY: True}, proof=False, tamper_events=lambda e: e.append(event))
        other = dict(event, actor="other", execution_id=None, basis=None, isolation_proof_sha256=None)
        inputs = self.inputs(self.v11("b", solved={KEY: True}, proof=False, tamper_events=lambda e: e.append(other)))
        self.assertEqual(inputs.engine_solved, frozenset())
        self.reject("null", "c", solved={KEY: True}, proof=False,
                    tamper_events=lambda e: e.append(dict(other, basis="isolated_window")))

    def test_iw17_evidence_rows(self):
        self.reject("모르는 행", tamper_rows=lambda rows: rows.append({"kind": "mystery", "seq": len(rows) + 1,
                                                                    "at": "2026-10-30T00:00:01Z"}))
        self.reject("순번", "b", tamper_rows=lambda rows: rows[3].update(seq=99))
        self.reject("connectivity_check", "c", tamper_rows=lambda rows: row_of(rows, "connectivity_check",
                                                                              phase="post").update(verdict="negative_succeeded"))

        def kind_instead_of_probe_kind(rows):
            row = row_of(rows, "connectivity_check", phase="pre")
            row["probe_kind_missing"] = row.pop("probe_kind")
        self.reject("필드", "d", tamper_rows=kind_instead_of_probe_kind)
        self.reject("경계 변경", "e", tamper_rows=lambda rows: row_of(rows, "network_snapshot", phase="post")[
            "services"][0].update(restart_count=1))

        def flip_internal(rows):
            for snap in (row_of(rows, "network_snapshot", phase="pre"), row_of(rows, "network_snapshot", phase="post")):
                snap["networks"][0]["internal"] = not snap["networks"][0]["internal"]
        self.reject("정책 지문", "f", tamper_rows=flip_internal)
        self.reject("boundary", "g", tamper_rows=lambda rows: row_of(rows, "boundary", event="maintained").update(
            policy_sha256="0" * 64))


class TestProofFields(_V11):
    def test_proof_fields_each_bound(self):
        self.reject("allowed_actors", tamper_proof=lambda p: p["network"].update(
            allowed_actors=["engine", "observer", "other"]))
        self.reject("allowed_actors", "b", tamper_proof=lambda p: p["network"].update(allowed_actors=["engine"]))
        self.reject("enforcement_evidence_sha256", "c",
                    tamper_proof=lambda p: p.update(enforcement_evidence_sha256="0" * 64))
        self.reject("sealed", "d", tamper_proof=lambda p: p["preparation"].update(
            sealed={"at": "2026-10-30T00:00:00+00:00", "sequence": 9}))
        # 하한 가드 자체를 고정한다(0·bool은 개수 대조 전에 거부).
        self.reject("1 이상 정수", "e", tamper_proof=lambda p: p["observer"].update(request_count=0))
        self.reject("1 이상 정수", "f", tamper_proof=lambda p: p["observer"].update(request_count=True))

    def test_boundary_lifecycle_cardinality(self):
        def released(rows):
            maintained = row_of(rows, "boundary", event="maintained")
            rows.append(dict(maintained, event="released", seq=len(rows) + 1, at="2026-10-30T00:00:11+00:00"))
        self.assertEqual(self.inputs(self.v11(tamper_rows=released)).version, "1.1")  # 세 행 정상
        self.assertEqual(self.inputs(self.v11("b")).version, "1.1")  # 두 행 정상
        self.reject("released", "c", tamper_rows=lambda rows: (released(rows), released(rows)))
        self.reject("boundary", "d", tamper_rows=lambda rows: drop(rows, row_of(rows, "boundary", event="applied")))
        self.reject("boundary", "e", tamper_rows=lambda rows: drop(rows, row_of(rows, "boundary", event="maintained")))
        self.reject("boundary", "f", tamper_rows=lambda rows: row_of(rows, "boundary", event="applied").update(
            at="2026-10-30T00:00:05+00:00"))  # 적용이 pre 관측 뒤

    def test_connectivity_needs_both_roles_per_phase(self):
        def only_positive(rows):
            for row in [r for r in rows if r["kind"] == "connectivity_check" and r["phase"] == "post"
                        and r["role"] == "negative"]:
                drop(rows, row)
        self.reject("양성·부정", tamper_rows=only_positive)

    def test_connectivity_verdict_is_recomputed_not_trusted(self):
        # verdict="match"는 그대로 두고 실제 결과만 바꾼다(#126 리뷰 재현).
        negative = dict(phase="post", role="negative")
        cases = {"부정 경로 성공": (negative, dict(observed="success", code="connected")),
                 "부정 경로 성공(code는 accepted 안)": (negative, dict(observed="success")),
                 "accepted 밖 실패 종류": (negative, dict(code="refused")),
                 "양성 대조 실패": (dict(phase="pre", role="positive"), dict(observed="fail", code="timeout")),
                 "양성 accepted 밖": (dict(phase="pre", role="positive"), dict(code="refused")),
                 "역할과 기대 불일치": (negative, dict(expected="success"))}
        for index, (label, (match, change)) in enumerate(cases.items()):
            with self.subTest(label):
                self.reject("불일치 경로", f"v{index}", tamper_rows=lambda rows, m=match, c=change: row_of(
                    rows, "connectivity_check", **m).update(c))
        malformed = {"accepted 빈 목록": dict(accepted=[]), "accepted 문자열 아님": dict(accepted=[["dns_error"]]),
                     "accepted 중복": dict(accepted=["dns_error", "dns_error"]), "code 없음": dict(code=None),
                     "모르는 점검 종류": dict(probe_kind="http")}
        for index, (label, change) in enumerate(malformed.items()):
            with self.subTest(label):
                self.reject("점검 형식", f"m{index}", tamper_rows=lambda rows, c=change: row_of(
                    rows, "connectivity_check", **negative).update(c))
        self.reject("양성·부정", "role", tamper_rows=lambda rows: row_of(rows, "connectivity_check", **negative).update(
            role="neutral"))

    def test_v1_records_still_required(self):
        run = self.make_run("v1null", [])
        receipt = self.fixture.receipt(run)
        receipt["artifacts"]["records"] = None
        self.fixture._write_receipt(run, receipt)
        with self.assertRaisesRegex(RunRecordError, "필드|records|오류"):
            self.inputs(run)


class TestComparison(_V11):
    def test_iw01_versions_must_not_mix_and_proofs_not_reused(self):
        v1 = self.make_run("old", [], repeat_index=1)
        with self.assertRaisesRegex(ComparabilityError, "혼합"):
            build_report(self.load([self.v11(), v1]), self.correlate, self.correlatable, runner_key=self.runner_key)
        first = self.v11("p1")
        second = self.v11("p2", repeat_index=1, tamper_proof=lambda p: p.update(proof_id="proof-run-p1"))
        with self.assertRaisesRegex(ComparabilityError, "proof_id"):
            build_report(self.load([first, second]), self.correlate, self.correlatable, runner_key=self.runner_key)

    def test_runs_with_different_drain_policies_are_not_compared(self):
        # 같은 환경 지문이라도 서명 근거의 유예·정착 대기가 다르면 비교하지 않는다(#153 리뷰).
        same = [self.v11("s1"), self.v11("s2", repeat_index=1)]
        build_report(self.load(same), self.correlate, self.correlatable, runner_key=self.runner_key)
        for index, change in enumerate((dict(grace_seconds=10), dict(settle_seconds=20))):
            with self.subTest(change=change):
                other = self.v11(f"g{index}", repeat_index=1,
                                 tamper_rows=lambda rows, c=change: row_of(rows, "drain").update(c))
                with self.assertRaisesRegex(ComparabilityError, "drain 유예·정착 대기 정책 불일치"):
                    build_report(self.load([self.v11(f"b{index}"), other]), self.correlate, self.correlatable,
                                 runner_key=self.runner_key)
        # 끊은 건수·적용 대기는 결과라 회차마다 달라도 비교한다.
        cut = self.v11("c1", repeat_index=1, tamper_rows=lambda rows: row_of(rows, "drain").update(
            forced_cancelled=1, settle_applied_seconds=2))
        build_report(self.load([self.v11("c0"), cut]), self.correlate, self.correlatable, runner_key=self.runner_key)

    def test_runs_with_different_run_conditions_are_not_compared(self):
        # 엔진 실행 설정·게이트웨이가 넣은 샘플링·머신 사양·머신이 하나라도 다르면 같은 조건이 아니다(§5.1·§6).
        build_report(self.load([self.v11("k1"), self.v11("k2", repeat_index=1)]), self.correlate, self.correlatable,
                     runner_key=self.runner_key)
        changes = {"엔진 설정": lambda row: row.update(engine_settings_sha256="f" * 64),
                   "적용 샘플링": lambda row: row["sampling"].update(temperature=0.7),
                   "머신 사양": lambda row: row["host"].update(memory_kib=1),
                   "머신": lambda row: row.update(machine_id_sha256="0" * 64),
                   "LLM 스트리밍": lambda row: row.update(llm_stream=False)}
        for index, (label, change) in enumerate(changes.items()):
            with self.subTest(label=label):
                other = self.v11(f"m{index}", repeat_index=1,
                                 tamper_rows=lambda rows, c=change: c(row_of(rows, "run_conditions")))
                with self.assertRaisesRegex(ComparabilityError, f"회차 조건 불일치\\({label}\\)"):
                    build_report(self.load([self.v11(f"n{index}"), other]), self.correlate, self.correlatable,
                                 runner_key=self.runner_key)

    def test_v1_only_comparison_is_flagged_not_official(self):
        report = build_report(self.load([self.make_run("v1only", [])]), self.correlate, self.correlatable,
                              runner_key=self.runner_key)
        self.assertTrue(any("공식 비교 아님" in w for w in report.warnings))
        report = build_report(self.load([self.v11("x")]), self.correlate, self.correlatable,
                              runner_key=self.runner_key)
        self.assertFalse(any("공식 비교 아님" in w for w in report.warnings))


if __name__ == "__main__":
    unittest.main()
