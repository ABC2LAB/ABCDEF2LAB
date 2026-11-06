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
from run_fixture_v11 import E2_STUB_PLACEMENT, STUB_RECONCILIATION, make_v11, snapshot
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

    def test_e2_placement_row_is_required_and_typed(self):
        self.assertEqual(self.inputs(self.v11("en")).e2_mode, "none")
        self.assertEqual(self.inputs(self.v11("es", e2=E2_STUB_PLACEMENT)).e2_mode, "stub")
        self.reject("e2_placement", "e0", tamper_rows=lambda rows: drop(rows, row_of(rows, "e2_placement")))
        bad = [{"mode": "other"}, {"decision_rows": 0}, {"mode": "stub"}, {"mode": "product", "decision_rows": 0},
               {"mode": "stub", "decision_rows": 1, "allowed": 1, "unsupported": 0, "unavailable": 1},
               {"mode": "stub", "decision_rows": 0, "allowed": 0, "unsupported": 0, "unavailable": -1},
               {"mode": "stub", "decision_rows": True, "allowed": 1, "unsupported": 0, "unavailable": 0}]
        for index, change in enumerate(bad):
            self.reject("e2_placement", f"e{index + 1}",
                        tamper_rows=lambda rows, c=change: row_of(rows, "e2_placement").update(c))

    def _verdict_row(self, status, reason):
        def add(rows):
            rows.append({"kind": "verdict_collection", "seq": len(rows) + 1, "at": rows[-1]["at"], "status": status, "reason": reason})
        return add

    def test_verdict_integrity_failure_stops_grading(self):
        """무결성 실패가 records=null(판정 미수집)로 숨지 않는다."""
        self.reject("판정 export 무결성 실패: 채점 중단", "vi",
                    tamper_rows=self._verdict_row("integrity_failed", "verdict_collect: PAGE_INTEGRITY_FAILED"))
        run = self.v11("vs", solved={KEY: True}, tamper_rows=self._verdict_row("integrity_failed", "verdict_collect: X"))
        with self.assertRaisesRegex(RunRecordError, "채점 중단"):
            score_run(self.load([run])[0], self.correlate, runner_key=self.runner_key, correlatable=self.correlatable)

    def test_verdict_uncollected_stays_undecidable(self):
        run = self.v11("vn", solved={KEY: True}, tamper_rows=self._verdict_row("not_collected", "verdict_collect: FREEZE_EXPORT_NOT_FOUND"))
        scored = score_run(self.load([run])[0], self.correlate, runner_key=self.runner_key, correlatable=self.correlatable)
        self.assertEqual(set(scored.result.verdicts.values()), {ScanVerdict.UNDECIDABLE})
        self.assertIn(KEY, scored.result.engine_solved)

    def test_verdict_collection_row_must_agree_with_records(self):
        collected = self.inputs(self.v11("vc", records=[_record("confirmed", "rest:basket:1")],
                                         tamper_rows=self._verdict_row("collected", None)))
        self.assertTrue(collected.judgments_collected)
        self.reject("수집했는데 records가 없음", "v1", tamper_rows=self._verdict_row("collected", None))
        self.reject("수집했는데 records가 없음", "v2", records=[_record("confirmed", "rest:basket:1")],
                    tamper_rows=self._verdict_row("collected", "verdict_collect: X"))
        self.reject("미수집인데 records가 있거나", "v3", records=[_record("confirmed", "rest:basket:1")],
                    tamper_rows=self._verdict_row("not_collected", "verdict_collect: X"))
        self.reject("미수집인데 records가 있거나", "v4", tamper_rows=self._verdict_row("not_collected", None))
        self.reject("미수집인데 records가 있거나", "v5", tamper_rows=self._verdict_row("not_collected", ""))
        self.reject("미수집인데 records가 있거나", "v9", tamper_rows=self._verdict_row("not_collected", 7))
        self.reject("미수집인데 records가 있거나", "v6", records=[_record("confirmed", "rest:basket:1")],
                    tamper_rows=self._verdict_row("integrity_failed", "verdict_collect: X"))
        self.reject("collected\\|not_collected", "v7", tamper_rows=self._verdict_row("other", "x"))

    def test_verdict_collection_row_is_at_most_one(self):
        def twice(rows):
            self._verdict_row("not_collected", "a")(rows)
            self._verdict_row("not_collected", "a")(rows)
        self.reject("행은 하나", "v8", tamper_rows=twice)

    PRODUCT = {"mode": "product", "decision_rows": None, "allowed": None, "unsupported": None, "unavailable": None}

    def _product_row(self, **changes):
        row = {"close_state": "complete", "close_reason": None, "collection_outcome": "complete", "collection_reason": None, "rows": 7, "attempts": 5,
               "decisions": {"allowed": 4, "denied": 1, "unsupported": 0}, "attempts_without_result": 0, "allowed_not_sent": 0, "checkpoint_sequence": 7,
               "config_sha256": "1" * 64, "policy_fingerprint_sha256": "2" * 64, "manifest_sha256": "3" * 64,
               "image_id": "sha256:" + "4" * 64} | changes

        def add(rows):
            rows.append({"kind": "e2_product", "seq": len(rows) + 1, "at": rows[-1]["at"], **row})
        return add

    def test_product_run_uses_its_own_decision_counts(self):
        inputs = self.inputs(self.v11("p1", e2=self.PRODUCT, tamper_rows=self._product_row()))
        self.assertEqual((inputs.e2_mode, inputs.attempt_reconciliation), ("product", STUB_RECONCILIATION))

    def test_product_failure_has_no_reconciliation_and_stays_a_run(self):
        def failed(rows):
            drop(rows, row_of(rows, "attempt_reconciliation"))
            self._product_row(close_state="unavailable", close_reason="close_timeout", collection_outcome="product_failure", collection_reason="close_timeout",
                              rows=None, attempts=None, decisions=None, checkpoint_sequence=None, manifest_sha256=None)(rows)
        inputs = self.inputs(self.v11("p2", e2=self.PRODUCT, tamper_rows=failed))
        self.assertEqual((inputs.e2_mode, inputs.attempt_reconciliation), ("product", None))
        self.reject("제품 실패 회차에는 없어야", "p3", e2=self.PRODUCT, tamper_rows=self._product_row(
            close_state="unavailable", collection_outcome="product_failure", collection_reason="close_timeout", decisions=None, attempts=None))

    def test_no_execution_reconciles_against_zero_decisions(self):
        zero = {"cells": {"allowed_observed": 0, "allowed_unobserved": 0, "denied_unobserved": 0, "denied_observed": 0, "unrecorded_observed": 2},
                "meter_attempt_reused": 0, "blocked_reached": 0, "unrecorded_reached": 2, "positive_evidence": False, "control_claim_withheld": True,
                "allowed_responded": 0}
        row = self._product_row(close_state="no_execution", collection_outcome="no_execution", rows=None, attempts=None, decisions=None, checkpoint_sequence=None, manifest_sha256=None)
        inputs = self.inputs(self.v11("p4", e2=self.PRODUCT, reconciliation=zero, tamper_rows=row))
        self.assertEqual(inputs.attempt_reconciliation["unrecorded_reached"], 2)
        self.assertTrue(inputs.attempt_reconciliation["control_claim_withheld"])

    def test_product_row_must_exist_only_on_product_runs_and_be_consistent(self):
        self.reject("e2_product: 정확히 한 행", "q0", e2=self.PRODUCT)
        self.reject("e2_product: 제품 E² 회차에만", "q1", tamper_rows=self._product_row())
        self.reject("e2_product: 제품 E² 회차에만", "q2", e2=E2_STUB_PLACEMENT, tamper_rows=self._product_row())
        def twice(rows):
            self._product_row()(rows)
            self._product_row()(rows)
        self.reject("e2_product: 정확히 한 행", "q3", e2=self.PRODUCT, tamper_rows=twice)
        cases = (({"close_state": "other"}, "상태 형식 오류"), ({"collection_outcome": "other"}, "상태 형식 오류"), ({"close_reason": 7}, "상태 형식 오류"),
                 ({"collection_reason": "x"}, "완료 상태가 맞지 않음"), ({"close_state": "incomplete"}, "완료 상태가 맞지 않음"),
                 ({"close_state": "no_execution"}, "완료 상태가 맞지 않음"), ({"collection_outcome": "no_execution"}, "실행 없음 상태가 맞지 않음"),
                 ({"close_state": "no_execution", "collection_outcome": "no_execution", "collection_reason": "x"}, "실행 없음 상태가 맞지 않음"),
                 ({"collection_outcome": "product_failure"}, "제품 실패 사유 없음"), ({"collection_outcome": "product_failure", "collection_reason": ""}, "제품 실패 사유 없음"),
                 ({"collection_outcome": "product_failure", "collection_reason": 7}, "제품 실패 사유 없음"),
                 ({"decisions": None}, "완료인데 결정 건수 없음"), ({"decisions": {}}, "결정 건수 형식 오류"), ({"decisions": 7}, "결정 건수 형식 오류"),
                 ({"decisions": {"allowed": 4, "denied": 1}}, "결정 건수 형식 오류"), ({"decisions": {"allowed": -1, "denied": 6, "unsupported": 0}}, "결정 건수 형식 오류"),
                 ({"attempts": 4}, "결정 건수 형식 오류"), ({"attempts": True}, "결정 건수 형식 오류"),
                 # 합이 1이면 True도 합과 같아 보인다.
                 ({"attempts": True, "decisions": {"allowed": 1, "denied": 0, "unsupported": 0}}, "결정 건수 형식 오류"),
                 ({"config_sha256": "x"}, "대조 값 형식 오류"), ({"policy_fingerprint_sha256": None}, "대조 값 형식 오류"),
                 # 숫자는 문자열로 바꾸면 hex처럼 보인다.
                 ({"policy_fingerprint_sha256": int("2" * 64)}, "대조 값 형식 오류"), ({"config_sha256": int("1" * 64)}, "대조 값 형식 오류"),
                 ({"policy_fingerprint_sha256": ["2" * 64]}, "대조 값 형식 오류"), ({"policy_fingerprint_sha256": "A" * 64}, "대조 값 형식 오류"),
                 ({"image_id": "abc"}, "대조 값 형식 오류"), ({"image_id": "4" * 64}, "대조 값 형식 오류"), ({"image_id": None}, "대조 값 형식 오류"))
        for index, (changes, pattern) in enumerate(cases):
            with self.subTest(changes=changes):
                self.reject(pattern, f"w{index}", e2=self.PRODUCT, tamper_rows=self._product_row(**changes))

    def _product_run(self, name, repeat=0, **changes):
        return self.v11(name, repeat_index=repeat, e2=self.PRODUCT, tamper_rows=self._product_row(**changes))

    def test_identity_is_read_only_from_product_runs(self):
        self.assertEqual(self.inputs(self._product_run("i1")).e2_identity, ("2" * 64, "sha256:" + "4" * 64))
        self.assertIsNone(self.inputs(self.v11("i2")).e2_identity)
        self.assertIsNone(self.inputs(self.v11("i3", e2=E2_STUB_PLACEMENT)).e2_identity)

    def test_one_condition_must_use_one_policy_and_image(self):
        for index, changes in enumerate(({"policy_fingerprint_sha256": "5" * 64}, {"image_id": "sha256:" + "6" * 64})):
            with self.subTest(changes=changes):
                runs = [self._product_run(f"x{index}a"), self._product_run(f"x{index}b", 1, **changes)]
                with self.assertRaisesRegex(ComparabilityError, "E② 정책 지문·이미지 불일치"):
                    build_report(self.load(runs), self.correlate, self.correlatable, runner_key=self.runner_key)

    def test_other_conditions_may_use_another_policy(self):
        # 대조는 조건 안에서만 한다.
        runs = [self.v11("oa", condition="우리@a", e2=self.PRODUCT, tamper_rows=self._product_row()),
                self.v11("ob", condition="우리@b", e2=self.PRODUCT, tamper_rows=self._product_row(policy_fingerprint_sha256="5" * 64))]
        build_report(self.load(runs), self.correlate, self.correlatable, runner_key=self.runner_key)

    def test_same_policy_and_image_are_compared_together(self):
        # 설정 digest는 회차 값이 들어가 회차마다 다르다. 대조하지 않는다.
        runs = [self._product_run("sa"), self._product_run("sb", 1, config_sha256="7" * 64, manifest_sha256="8" * 64)]
        build_report(self.load(runs), self.correlate, self.correlatable, runner_key=self.runner_key)

    def test_product_cells_must_add_up_to_its_decisions(self):
        self.reject("칸 합이 E② 결정 건수와 다름", "c1", e2=self.PRODUCT, tamper_rows=self._product_row(decisions={"allowed": 3, "denied": 2, "unsupported": 0}))
        # 차단은 denied와 unsupported를 합쳐서 본다.
        inputs = self.inputs(self.v11("c2", e2=self.PRODUCT, tamper_rows=self._product_row(decisions={"allowed": 4, "denied": 0, "unsupported": 1})))
        self.assertEqual(inputs.e2_mode, "product")

    def test_attempt_reconciliation_row_follows_the_e2_placement(self):
        self.assertIsNone(self.inputs(self.v11("rn")).attempt_reconciliation)
        self.assertEqual(self.inputs(self.v11("rs", e2=E2_STUB_PLACEMENT)).attempt_reconciliation, STUB_RECONCILIATION)
        self.reject("E② 없는 회차에는 없어야", "r0", reconciliation=STUB_RECONCILIATION)
        self.reject("attempt_reconciliation: 정확히 한 행", "r1", e2=E2_STUB_PLACEMENT,
                    tamper_rows=lambda rows: drop(rows, row_of(rows, "attempt_reconciliation")))

    def test_attempt_reconciliation_counts_are_typed(self):
        cells = STUB_RECONCILIATION["cells"]
        bad = [{"cells": {k: v for k, v in cells.items() if k != "denied_observed"}},
               {"cells": dict(cells, extra=0)},
               {"cells": dict(cells, denied_observed=-1)},
               {"cells": dict(cells, allowed_observed=True, allowed_unobserved=3)},
               {"cells": list(cells.values())},
               {"meter_attempt_reused": -1}, {"blocked_reached": "0"}, {"unrecorded_reached": None},
               {"allowed_responded": -1}, {"allowed_responded": True},
               {"positive_evidence": 1}, {"control_claim_withheld": 0}]
        for index, change in enumerate(bad):
            with self.subTest(change=change):
                self.reject("attempt_reconciliation: 건수 형식 오류", f"t{index}", e2=E2_STUB_PLACEMENT,
                            reconciliation=dict(STUB_RECONCILIATION, **change))

    def test_attempt_reconciliation_derived_counts_follow_the_cells(self):
        cells = STUB_RECONCILIATION["cells"]
        # 바꾼 칸마다 다른 파생 값은 맞춰 두고 한 값만 틀리게 해서, 파생 규칙 항 하나씩만 걸리게 한다.
        bad = [{"blocked_reached": 1}, {"unrecorded_reached": 1}, {"positive_evidence": False},
               {"control_claim_withheld": True},
               {"meter_attempt_reused": 1},
               {"cells": dict(cells, allowed_observed=0, allowed_unobserved=4), "allowed_responded": 0},
               # 접수는 있는데 대상 응답이 없으면 양성 근거가 없고 통제 주장은 보류다
               {"allowed_responded": 0},
               {"allowed_responded": 0, "positive_evidence": False},
               {"allowed_responded": 0, "control_claim_withheld": True},
               {"allowed_responded": 5},
               {"cells": dict(cells, denied_observed=1, denied_unobserved=0), "blocked_reached": 1},
               {"cells": dict(cells, unrecorded_observed=1), "unrecorded_reached": 1}]
        for index, change in enumerate(bad):
            with self.subTest(change=change):
                self.reject("파생 건수·보류 판정이 칸과 다름", f"d{index}", e2=E2_STUB_PLACEMENT,
                            reconciliation=dict(STUB_RECONCILIATION, **change))

    def test_attempt_reconciliation_cells_add_up_to_the_e2_decisions(self):
        cells = STUB_RECONCILIATION["cells"]
        for index, change in enumerate(({"allowed_unobserved": 1}, {"denied_unobserved": 2},
                                        {"allowed_observed": 3, "allowed_unobserved": 0})):
            with self.subTest(change=change):
                self.reject("칸 합이 E② 결정 건수와 다름", f"s{index}", e2=E2_STUB_PLACEMENT,
                            reconciliation=dict(STUB_RECONCILIATION, allowed_responded=3, cells=dict(cells, **change)))
        # 차단 쪽 합에는 미지원·사용 불가가 둘 다 들어간다.
        unavailable = dict(E2_STUB_PLACEMENT, unsupported=0, unavailable=1)
        self.assertEqual(self.inputs(self.v11("su", e2=unavailable)).attempt_reconciliation, STUB_RECONCILIATION)

    def test_bad_cells_are_recorded_not_rejected(self):
        # 차단 후 도달·기록 없이 도달은 회차 오류가 아니다. 성능표에서 빼지 않고 통제 주장만 보류한다(§8.3).
        reached = dict(STUB_RECONCILIATION, blocked_reached=1, unrecorded_reached=2, control_claim_withheld=True,
                       cells=dict(STUB_RECONCILIATION["cells"], denied_observed=1, denied_unobserved=0,
                                  unrecorded_observed=2))
        got = self.inputs(self.v11("bc", e2=E2_STUB_PLACEMENT, reconciliation=reached)).attempt_reconciliation
        self.assertEqual(got, reached)

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
        # 예전 local 회차는 샘플링을 넣지 않아 null이다.
        local = self.inputs(self.v11("loc", tamper_rows=lambda rows: row_of(rows, "run_conditions").update(
            sampling=None)))
        self.assertEqual(local.version, "1.1")
        # 지금 local은 자기 묶음을 쓴다. 두 묶음이 섞이면 거부한다.
        applied = {"temperature": 1.0, "top_p": 0.95, "presence_penalty": 0.5, "frequency_penalty": 0.25, "seed": 42}
        local = self.inputs(self.v11("loc2", tamper_rows=lambda rows: row_of(rows, "run_conditions").update(
            sampling=dict(applied))))
        self.assertEqual(local.version, "1.1")
        self.reject("run_conditions", "mixed-sampling", tamper_rows=lambda rows: row_of(rows, "run_conditions").update(
            sampling=dict(applied, top_k=20)))
        self.assertIsNotNone(self.inputs(self.v11("ok")).run_conditions)
        # 스트리밍은 bool만 받는다(1·"true"를 true로 읽지 않음). 행에서 빠져도 거부.
        self.reject("run_conditions", "r11", tamper_rows=lambda rows: row_of(rows, "run_conditions").update(llm_stream=1))
        self.reject("run_conditions", "r12", tamper_rows=lambda rows: row_of(rows, "run_conditions").pop("llm_stream"))
        # 규칙 파일 해시는 없으면 null, 있으면 64자 hex다. 행에서 빠져도 거부.
        self.reject("run_conditions", "r13", tamper_rows=lambda rows: row_of(rows, "run_conditions").update(
            engine_rules_sha256="x"))
        self.reject("run_conditions", "r14", tamper_rows=lambda rows: row_of(rows, "run_conditions").pop(
            "engine_rules_sha256"))
        ruled = self.inputs(self.v11("rul", tamper_rows=lambda rows: row_of(rows, "run_conditions").update(
            engine_rules_sha256="a" * 64)))
        self.assertIn("a" * 64, ruled.run_conditions)

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

    def test_e2_mode_mixed_within_a_condition_is_not_compared(self):
        none, stub = self.v11("e1"), self.v11("e2", repeat_index=1, e2=E2_STUB_PLACEMENT)
        with self.assertRaisesRegex(ComparabilityError, "E② 자리 혼합"):
            build_report(self.load([none, stub]), self.correlate, self.correlatable, runner_key=self.runner_key)

    def test_stub_runs_are_refused_unless_allowed_then_flagged(self):
        with self.assertRaisesRegex(ComparabilityError, "가짜 E② 회차"):
            build_report(self.load([self.v11("s1", e2=E2_STUB_PLACEMENT),
                                    self.v11("s2", repeat_index=1, e2=E2_STUB_PLACEMENT)]),
                         self.correlate, self.correlatable, runner_key=self.runner_key)
        report = build_report(self.load([self.v11("s3", e2=E2_STUB_PLACEMENT),
                                         self.v11("s4", repeat_index=1, e2=E2_STUB_PLACEMENT)]),
                              self.correlate, self.correlatable, runner_key=self.runner_key, allow_fixture_e2=True)
        self.assertTrue(any("가짜 E②" in w for w in report.warnings), report.warnings)
        plain = build_report(self.load([self.v11("p1"), self.v11("p2", repeat_index=1)]), self.correlate,
                             self.correlatable, runner_key=self.runner_key)
        self.assertFalse(any("가짜 E②" in w for w in plain.warnings))

    def test_control_table_sums_each_row_per_condition(self):
        # 행마다 합계가 서로 다르고(행이 뒤바뀌면 드러남) 두 회차 값이 둘 다 0이 아닌 행이 있다(합 대신 최댓값이면 드러남).
        clean = dict(STUB_RECONCILIATION, allowed_responded=2,
                     cells=dict(STUB_RECONCILIATION["cells"], allowed_observed=3, allowed_unobserved=1))
        bad = {"cells": {"allowed_observed": 0, "allowed_unobserved": 4, "denied_unobserved": 0, "denied_observed": 1,
                         "unrecorded_observed": 2},
               "allowed_responded": 0, "meter_attempt_reused": 3, "blocked_reached": 1, "unrecorded_reached": 2,
               "positive_evidence": False, "control_claim_withheld": True}
        report = build_report(self.load([self.v11("c1", e2=E2_STUB_PLACEMENT, reconciliation=clean),
                                         self.v11("c2", repeat_index=1, e2=E2_STUB_PLACEMENT, reconciliation=bad)]),
                              self.correlate, self.correlatable, runner_key=self.runner_key, allow_fixture_e2=True)
        table = report.text.split("### 개선 후 통제 검증 — 시도별 대조(프로토콜 §8.3)\n", 1)[1]
        for line in ("| 항목 | 우리@test |", "| 회차 수 | 2 |", "| E② 자리 | stub |",
                     "| 허용·관측됨 | 3 (3, 0) |", "| 허용·대상 응답 수신(허용 양성 근거) | 2 (2, 0) |", "| 허용·관측되지 않음 | 5 (1, 4) |", "| 차단·관측되지 않음 | 1 (1, 0) |",
                     "| 차단 후 대상 도달(기대 0) | 1 (0, 1) |", "| E② 기록 없이 대상 도달(기대 0) | 2 (0, 2) |",
                     "| 같은 시도 ID 재사용(기대 0) | 3 (0, 3) |", "| 허용 양성 근거 있는 회차 | 1/2 |",
                     "| 통제 주장 | 보류(회차 1/2) — 가짜 E², 통제 증명 아님 |"):
            self.assertIn(line + "\n", table)
        self.assertIn("| 대조 근거 없는 회차(증명 없음, 기대 0) | 0 |\n", table)
        self.assertIn("경고[통제 주장 보류] 우리@test: 회차 1/2 — 대조 근거 없는 회차 0, 차단 후 도달 1, "
                      "기록 없이 도달 2, 재사용 3, 허용 양성 근거 없는 회차 1", "\n".join(report.warnings))

    def test_run_without_reconciliation_evidence_holds_the_claim(self):
        # 증명 없는 회차는 대조 근거가 없다. 표에서 빠지면 나머지 회차만으로 '조건 충족'이 되므로 분모에 남기고 보류로
        # 센다(§8.3 미완 기록·미결속은 수용 보류, #225 리뷰). 성능표는 두 회차를 그대로 쓴다.
        report = build_report(self.load([self.v11("u1", e2=E2_STUB_PLACEMENT, total_requests=5),
                                         self.v11("u2", repeat_index=1, proof=False, e2=E2_STUB_PLACEMENT,
                                                  total_requests=9)]),
                              self.correlate, self.correlatable, runner_key=self.runner_key, allow_fixture_e2=True)
        table = report.text.split("### 개선 후 통제 검증 — 시도별 대조(프로토콜 §8.3)\n", 1)[1]
        for line in ("| 회차 수 | 2 |", "| 대조 근거 없는 회차(증명 없음, 기대 0) | 1 |", "| 허용·관측됨 | 4 (4, —) |",
                     "| 허용 양성 근거 있는 회차 | 1/2 |", "| 통제 주장 | 보류(회차 1/2) — 가짜 E², 통제 증명 아님 |"):
            self.assertIn(line + "\n", table)
        self.assertIn("경고[통제 주장 보류] 우리@test: 회차 1/2 — 대조 근거 없는 회차 1,", "\n".join(report.warnings))
        matrix = report.text.split("### 개선 후 통제 검증", 1)[0]
        self.assertIn("| 7 ± 2.82843 |", matrix)  # 총 요청 수 5·9 두 회차 평균 ± SD

    def test_control_table_only_for_conditions_with_e2(self):
        clean = build_report(self.load([self.v11("k1", e2=E2_STUB_PLACEMENT),
                                        self.v11("k2", repeat_index=1, e2=E2_STUB_PLACEMENT)]),
                             self.correlate, self.correlatable, runner_key=self.runner_key, allow_fixture_e2=True)
        self.assertIn("| 통제 주장 | 조건 충족 — 가짜 E², 통제 증명 아님 |\n", clean.text)
        self.assertFalse(any("통제 주장 보류" in w for w in clean.warnings))
        # E² 자리가 없는 조건(baseline·E² 없음)에는 대조를 적용하지 않는다(§8.3 첫 문장). 표도 내지 않는다.
        plain = build_report(self.load([self.v11("k3"), self.v11("k4", repeat_index=1)]), self.correlate,
                             self.correlatable, runner_key=self.runner_key)
        self.assertNotIn("통제 검증", plain.text)

    def test_runs_with_different_run_conditions_are_not_compared(self):
        # 엔진 실행 설정·게이트웨이가 넣은 샘플링·머신 사양·머신이 하나라도 다르면 같은 조건이 아니다(§5.1·§6).
        build_report(self.load([self.v11("k1"), self.v11("k2", repeat_index=1)]), self.correlate, self.correlatable,
                     runner_key=self.runner_key)
        changes = {"엔진 설정": lambda row: row.update(engine_settings_sha256="f" * 64),
                   "적용 샘플링": lambda row: row["sampling"].update(temperature=0.7),
                   "머신 사양": lambda row: row["host"].update(memory_kib=1),
                   "머신": lambda row: row.update(machine_id_sha256="0" * 64),
                   "LLM 스트리밍": lambda row: row.update(llm_stream=False),
                   "엔진 규칙 파일": lambda row: row.update(engine_rules_sha256="a" * 64)}
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
