"""보존 sidecar v3(탐색 분리 배치 결정안 §3.3) 시험: 공통 대조·쪽별·회차별·제품 블록, v2와 섞기 거부, 서명 재검증."""
from __future__ import annotations

import copy
import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import synthetic_gt  # noqa: E402,F401  합성 정답 목록을 먼저 끼운다(정답표 노출 방지)
sys.path.insert(0, str(Path(__file__).resolve().parent))
from path_mask import key_ref  # noqa: E402
from preservation_record import (FORMAT_V3, NAME, PreservationRecordError, _validate_v3, build_payload_v3,  # noqa: E402
                                 expected_first_limit,
                                 load_record, validate_series)
from run_record import sign_run_record  # noqa: E402
from runner_evidence import MeterFiles  # noqa: E402
import boundary_fixture as bf  # noqa: E402
import test_preservation_record as v2  # noqa: E402  (클래스를 이름으로 가져오면 v2 시험이 이 모듈에서도 돈다)

PATH_KEY, RUNNER_KEY = v2.PATH_KEY, v2.RUNNER_KEY


def common(**changes):
    return {"request_budget": 3, "time_budget_seconds": 60, "model_pin_sha256": "5" * 64, "target_image_id": "sha256:" + "6" * 64,
            "initial_state_sha256": "7" * 64, "machine_sha256": "8" * 64, "runner_settings_sha256": "0" * 64,
            "goal_common_sha256": "c" * 64, "comm_mode": "external_allowed", "mcp_version": "0.0.84",
            "mcp_raw_tools_sha256": "1" * 64, **changes}


def side_conditions(side, **changes):
    baseline = side == "baseline"
    return {"layout": "artex-direct/v1" if baseline else "exploration-split/v1",
            "driver_sha256": ("d" if baseline else "e") * 64, "driver_settings_sha256": ("2" if baseline else "3") * 64,
            "write_mode": "approved_writes", "declared_model_budget_sha256": ("4" if baseline else "9") * 64,
            "browser_image_id": None if baseline else "sha256:" + "b" * 64,
            "exploration_policy_sha256": None if baseline else "f" * 64,
            "diagnosis_policy_sha256": None if baseline else "a" * 64,
            "cli_surface_sha256": ("1" if baseline else "2") * 64, "mcp_exposed_tools_sha256": ("3" if baseline else "4") * 64,
            **changes}


def run(side, **changes):
    base = {"stop_reason": "request_budget",
            "first_limit": {"value": "runner_request_budget", "runner_limit": "runner_request_budget",
                            "runner_limit_elapsed_seconds": 40.0, "engine_terminal_elapsed_seconds": None},
            "end_signal": None, "rate_limited": 0 if side == "baseline" else 2}
    return base | changes


def product(**changes):
    return {"source_sha": "a" * 40,
            "image_ids": {"product": "sha256:" + "1" * 64, "explore_browser": "sha256:" + "b" * 64, "fixture": "sha256:" + "3" * 64},
            "file_sha256": {name: "e" * 64 for name in ("installation", "policy", "api_launch", "target_policy", "runtime_conditions",
                                                         "control_limits")},
            "split_conditions": {"workflow_defaults_sha256": "f" * 64, "workflow_defaults": {"max_pages": 256}, "workflow": {},
                                 "registration": {"methods": ["GET"]}},
            "sampling_overwrites": {"execution": 0, "preparation": [0, None]},
            "call_records": {"reservations": 5, "lines": 5}, **changes}


class V3Fixture(unittest.TestCase):
    def setUp(self):
        # v2 fixture의 계측기·경계 원자료 작성기를 그대로 쓴다(시험 메서드는 가져오지 않는다).
        self.helper = v2.SignedFixture(methodName="fixture")
        self.helper.setUp()
        self.addCleanup(self.helper.doCleanups)

    def write(self, directory, name, value):
        self.helper.write(directory, name, value)

    def load(self, directory, meter):
        return self.helper.load(directory, meter)

    def fixture(self, side="baseline", repeat=0):
        return self.helper.fixture(side, repeat)

    def fixture_v3(self, side="baseline", repeat=0, **parts):
        directory, meter, _ = self.fixture(side, repeat)  # 같은 계측기·경계 원자료(v2 fixture가 만든 파일)를 쓴다
        boundary = None
        if side == "ours":
            boundary = {"directory": directory / "boundary", "task_id": bf.TASK_ID, "path_key_ref": key_ref(PATH_KEY)}
        payload = build_payload_v3(
            meter=meter, series_ref="synthetic-preservation-1", execution_source="synthetic_fixture",
            run_id=f"v3-{side}-{self.helper.serial}", side=side, repeat_index=repeat,
            engine_source_sha=("a" if side == "baseline" else "b") * 40,
            engine_image_id="sha256:" + ("a" if side == "baseline" else "b") * 64,
            common=parts.get("common", common()), side_conditions=parts.get("side_conditions", side_conditions(side)),
            run=parts.get("run", run(side)), product=parts.get("product", None if side == "baseline" else product()),
            boundary=parts.get("boundary", boundary), purpose=parts.get("purpose", "browser_acceptance"))
        self.write(directory, NAME, sign_run_record(payload, RUNNER_KEY))
        return directory, meter, payload

    def series_v3(self, n=2, change=None):
        baseline, improved = [], []
        for i in range(n):
            for side, records in (("baseline", baseline), ("ours", improved)):
                parts = change(side, i) if change else {}
                directory, meter, _ = self.fixture_v3(side, i, **parts)
                records.append(self.load(directory, meter))
        return baseline, improved


class TestBuildAndLoad(V3Fixture):
    def test_round_trip_and_series(self):
        b, o = self.series_v3()
        self.assertEqual((b[0]["format"], o[0]["product"]["call_records"]), (FORMAT_V3, {"reservations": 5, "lines": 5}))
        self.assertIsNone(b[0]["product"])
        self.assertIsNone(b[0]["exploration_boundary"])
        self.assertEqual(o[0]["exploration_boundary"]["status"], "verified")
        verified = validate_series(b, o, request_budget=3, read_only=False)
        self.assertEqual((verified["format"], verified["verified_signed_rounds"], verified["layout"], verified["purpose"]),
                         (FORMAT_V3, 4, "exploration-split/v1", b[0]["purpose"]))
        self.assertEqual(verified["exploration_boundary"]["status"], "PASS")
        self.assertEqual({row["value"] for row in verified["first_limits"]}, {"runner_request_budget"})

    def test_round_purpose_is_signed_and_never_mixed(self):
        # #699 리뷰: 분리 배치 공식 회차의 v3 보존 기록은 official로 서명되고, 묶음 안에서 목적이 섞이면 거부한다.
        b, o = self.series_v3(change=lambda side, i: {"purpose": "official"})
        self.assertEqual({r["purpose"] for r in b + o}, {"official"})
        validate_series(b, o, request_budget=3, read_only=False)
        b, o = self.series_v3(change=lambda side, i: {"purpose": "official"} if side == "ours" else {})
        with self.assertRaisesRegex(PreservationRecordError, "preservation_series_mismatch"):
            validate_series(b, o, request_budget=3, read_only=False)
        for bad in ("acceptance", None):
            with self.subTest(purpose=bad), self.assertRaisesRegex(PreservationRecordError, "preservation_record_invalid"):
                self.fixture_v3("baseline", 0, purpose=bad)

    def test_signature_and_raw_bytes(self):
        directory, meter, _ = self.fixture_v3("ours")
        signed = json.loads((directory / NAME).read_text())
        signed["side_conditions"]["driver_sha256"] = "0" * 64
        self.write(directory, NAME, signed)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_signature_invalid"):
            self.load(directory, meter)
        directory, meter, _ = self.fixture_v3("baseline")
        path = directory / "engine-paths.jsonl"
        path.write_bytes(path.read_bytes() + b"\n")
        with self.assertRaisesRegex(PreservationRecordError, "preservation_raw_digest_mismatch"):
            self.load(directory, meter)

    def test_signed_but_malformed_v3_is_rejected_on_load(self):
        # 서명 키를 가진 쪽이 잘못된 모양을 서명해도 읽기에서 모양 검사를 다시 한다(B에 제품 블록).
        directory, meter, payload = self.fixture_v3("baseline")
        bad = dict(payload, product=product())
        self.write(directory, NAME, sign_run_record(bad, RUNNER_KEY))
        with self.assertRaisesRegex(PreservationRecordError, "preservation_product_invalid"):
            self.load(directory, meter)

    def test_signed_contradicting_first_limit_is_rejected_on_load(self):
        # #676 리뷰: 끝 관측이 없는데 엔진 우선으로 적힌 서명 기록은 읽기에서 거부한다.
        directory, meter, payload = self.fixture_v3("baseline")
        bad = dict(payload, run=dict(payload["run"], first_limit=dict(payload["run"]["first_limit"], value="engine_completed")))
        self.write(directory, NAME, sign_run_record(bad, RUNNER_KEY))
        with self.assertRaisesRegex(PreservationRecordError, "preservation_run_invalid"):
            self.load(directory, meter)

    def test_boundary_required_only_for_ours(self):
        with self.assertRaisesRegex(PreservationRecordError, "preservation_boundary_missing"):
            self.fixture_v3("ours", boundary=None)
        directory, _, _ = self.fixture_v3("ours")
        with self.assertRaisesRegex(PreservationRecordError, "preservation_boundary_unexpected"):
            self.fixture_v3("baseline", boundary={"directory": directory / "boundary", "task_id": bf.TASK_ID,
                                                  "path_key_ref": key_ref(PATH_KEY)})


class TestShape(V3Fixture):
    def setUp(self):
        super().setUp()
        _, _, self.b = self.fixture_v3("baseline")
        _, _, self.o = self.fixture_v3("ours")

    def reject(self, record, code, edit):
        bad = copy.deepcopy(record)
        edit(bad)
        with self.assertRaisesRegex(PreservationRecordError, code):
            _validate_v3(bad)

    def test_side_nulls(self):
        for field in ("browser_image_id", "exploration_policy_sha256", "diagnosis_policy_sha256"):
            with self.subTest(field=field):
                self.reject(self.b, "preservation_conditions_invalid", lambda r: r["side_conditions"].__setitem__(field, "a" * 64))
                self.reject(self.o, "preservation_conditions_invalid", lambda r: r["side_conditions"].__setitem__(field, None))
        self.reject(self.b, "preservation_conditions_invalid", lambda r: r["side_conditions"].__setitem__("layout", "e2-legacy/v1"))
        self.reject(self.o, "preservation_conditions_invalid", lambda r: r["side_conditions"].__setitem__("layout", "e2-legacy/v1"))
        self.reject(self.b, "preservation_product_invalid", lambda r: r.__setitem__("product", product()))
        self.reject(self.o, "preservation_product_invalid", lambda r: r.__setitem__("product", None))
        self.reject(self.b, "preservation_boundary_invalid", lambda r: r.__setitem__("exploration_boundary", self.o["exploration_boundary"]))
        self.reject(self.o, "preservation_boundary_invalid", lambda r: r.__setitem__("exploration_boundary", None))

    def test_common_fields_strict(self):
        self.reject(self.b, "preservation_conditions_invalid", lambda r: r["common"].pop("goal_common_sha256"))
        self.reject(self.b, "preservation_conditions_invalid", lambda r: r["common"].__setitem__("mcp_raw_tools_sha256", None))
        self.reject(self.b, "preservation_conditions_invalid", lambda r: r["common"].__setitem__("goal_common_sha256", None))
        self.reject(self.o, "preservation_conditions_invalid", lambda r: r["common"].__setitem__("goal_common_sha256", "x" * 64))
        self.reject(self.b, "preservation_conditions_invalid", lambda r: r["common"].__setitem__("request_budget", True))
        self.reject(self.b, "preservation_record_invalid", lambda r: r.__setitem__("conditions", {}))  # v2 칸이 섞이면 거부

    def test_run_block(self):
        engine = {"value": "engine_completed", "runner_limit": "runner_time_budget", "runner_limit_elapsed_seconds": 60,
                  "engine_terminal_elapsed_seconds": 42.5}
        ok_b = copy.deepcopy(self.b)
        ok_b["run"] = run("baseline", stop_reason="time_budget", first_limit=engine,
                          end_signal={"status": "done", "elapsed_seconds": 42.5, "closing_reason": None})
        _validate_v3(ok_b)
        ok_o = copy.deepcopy(self.o)
        ok_o["run"] = run("ours", stop_reason="time_budget", first_limit=dict(engine, value="product_limit:budget_limited"),
                          end_signal={"status": "completed", "elapsed_seconds": 42.5, "closing_reason": "budget_limited"})
        _validate_v3(ok_o)
        self.reject(ok_b, "preservation_run_invalid", lambda r: r["run"]["first_limit"].__setitem__("value", "product_limit:budget_limited"))
        self.reject(ok_b, "preservation_run_invalid", lambda r: r["run"]["end_signal"].__setitem__("closing_reason", "x"))
        self.reject(ok_b, "preservation_run_invalid", lambda r: r["run"]["end_signal"].__setitem__("elapsed_seconds", 41))
        self.reject(ok_b, "preservation_run_invalid", lambda r: r["run"].__setitem__("end_signal", None))
        self.reject(self.b, "preservation_run_invalid", lambda r: r["run"]["first_limit"].__setitem__("value", "runner_time_budget"))
        self.reject(self.b, "preservation_run_invalid", lambda r: r["run"].__setitem__("rate_limited", -1))
        self.reject(self.b, "preservation_run_invalid", lambda r: r["run"].__setitem__("stop_reason", "operator"))
        b_null = copy.deepcopy(self.b)
        b_null["run"]["rate_limited"] = None
        _validate_v3(b_null)

    def test_first_limit_must_match_the_observations(self):
        # #676 리뷰 반례: 서명돼 있어도 관측 시각·마감 사유와 모순된 분류는 거부한다.
        def first(value, runner_at, engine_at, limit="runner_request_budget"):
            return {"value": value, "runner_limit": limit, "runner_limit_elapsed_seconds": runner_at,
                    "engine_terminal_elapsed_seconds": engine_at}

        def end(at, closing=None):
            return None if at is None else {"status": "done", "elapsed_seconds": at, "closing_reason": closing}

        cases = [
            ("baseline", first("engine_completed", 40.0, None), end(None)),           # 끝 관측 없이 엔진 우선
            ("baseline", first("engine_completed", 40.0, 60.0), end(60.0)),           # 엔진이 늦게 끝남
            ("baseline", first("runner_request_budget", 40.0, 20.0), end(20.0)),      # 엔진이 먼저 끝났는데 러너 상한
            ("ours", first("product_limit:budget_limited", 40.0, 20.0), end(20.0)),   # 마감 사유 없이 제품 상한
            ("ours", first("engine_completed", 40.0, 20.0), end(20.0, "budget_limited")),  # 제품 예산 마감인데 엔진 완료
            ("baseline", first("runner_time_budget", 40.0, None, "runner_time_budget"), end(None)),  # 종료 사유와 상한 이름
        ]
        for side, limit, signal in cases:
            with self.subTest(side=side, limit=limit):
                record = copy.deepcopy(self.b if side == "baseline" else self.o)
                record["run"] = run(side, first_limit=limit, end_signal=signal)
                with self.assertRaisesRegex(PreservationRecordError, "preservation_run_invalid"):
                    _validate_v3(record)
        timed = copy.deepcopy(self.b)  # 시간 예산 종료면 러너 상한 시각은 시간 예산 값 그대로다
        timed["run"] = run("baseline", stop_reason="time_budget", first_limit=first("runner_time_budget", 59.0, None,
                                                                                      "runner_time_budget"))
        with self.assertRaisesRegex(PreservationRecordError, "preservation_run_invalid"):
            _validate_v3(timed)

    def test_first_limit_rule_matches_the_runner(self):
        # 기록 검사의 재계산이 러너가 회차 때 쓰는 runner.first_limit과 같은 결과다(격자 대조).
        import runner
        for stop in ("request_budget", "time_budget"):
            # 반올림 경계(#676 리뷰): 정지 원래 시각이 저장 정밀도의 직전·직후여도 회차 분류와 기록 재계산이 같아야 한다.
            for stop_elapsed in (10.0, 40.0, 39.9994, 39.9995, 39.9996, 40.0004, 40.0005, 59.9996):
                for engine in (None, 5.0, 39.999, 40.0, 40.001, 70.0, 60.0):
                    for closing in (None, "budget_limited", "complete"):
                        terminal = None if engine is None else {"status": "x", "elapsed_seconds": engine, "closing_reason": closing}
                        got = runner.first_limit(stop, stop_elapsed, 60.0, terminal)
                        with self.subTest(stop=stop, stop_elapsed=stop_elapsed, engine=engine, closing=closing):
                            # 기록에 남는 값(러너 출력)만으로 다시 계산한다
                            self.assertEqual(expected_first_limit(stop, got["runner_limit_elapsed_seconds"], 60.0,
                                                                  got["engine_terminal_elapsed_seconds"],
                                                                  None if engine is None else closing, got["runner_limit"]),
                                             got["value"])

    def test_review_boundary_case_is_accepted(self):
        # #676 리뷰 예: 엔진 끝 읽기 39.99955초 → 기록 40.0, 요청 예산 정지 39.9996초. 회차 분류와 기록 재계산이 같다.
        import runner
        got = runner.first_limit("request_budget", 39.9996, 60.0, {"status": "done", "elapsed_seconds": 39.99955})  # 반올림 전 값도 같다
        self.assertEqual((got["value"], got["runner_limit_elapsed_seconds"], got["engine_terminal_elapsed_seconds"]),
                         ("engine_completed", 40.0, 40.0))
        record = copy.deepcopy(self.b)
        record["run"] = run("baseline", first_limit=got, end_signal={"status": "done", "elapsed_seconds": 40.0, "closing_reason": None})
        _validate_v3(record)

    def test_product_block(self):
        self.reject(self.o, "preservation_product_invalid", lambda r: r["product"]["call_records"].__setitem__("lines", 4))
        self.reject(self.o, "preservation_product_invalid", lambda r: r["product"]["file_sha256"].pop("policy"))
        self.reject(self.o, "preservation_product_invalid", lambda r: r["product"]["image_ids"].__setitem__("fixture", "x"))
        self.reject(self.o, "preservation_product_invalid", lambda r: r["product"]["sampling_overwrites"].__setitem__("execution", -1))
        self.reject(self.o, "preservation_product_invalid", lambda r: r["product"]["split_conditions"].pop("registration"))


class TestSeries(V3Fixture):
    def test_common_mismatch_breaks_pairs(self):
        for field, value in (("goal_common_sha256", "d" * 64), ("mcp_raw_tools_sha256", "2" * 64), ("model_pin_sha256", "6" * 64)):
            with self.subTest(field=field):
                b, o = self.series_v3(change=lambda side, i: {"common": common(**{field: value})} if side == "ours" else {})
                with self.assertRaisesRegex(PreservationRecordError, "preservation_conditions_mismatch"):
                    validate_series(b, o, request_budget=3, read_only=False)

    def test_side_fields_may_differ_across_sides_but_not_within(self):
        b, o = self.series_v3()
        self.assertNotEqual(b[0]["side_conditions"]["driver_sha256"], o[0]["side_conditions"]["driver_sha256"])
        validate_series(b, o, request_budget=3, read_only=False)
        b, o = self.series_v3(change=lambda side, i: {"side_conditions": side_conditions(side, cli_surface_sha256="9" * 64)}
                              if side == "ours" and i == 1 else {})
        with self.assertRaisesRegex(PreservationRecordError, "preservation_layout_or_policy_mixed"):
            validate_series(b, o, request_budget=3, read_only=False)

    def test_run_fields_are_not_compared(self):
        b, o = self.series_v3(change=lambda side, i: {"run": run(side, rate_limited=i * 7)})
        validate_series(b, o, request_budget=3, read_only=False)

    def test_v2_and_v3_do_not_mix(self):
        b, o = self.series_v3(n=1)
        directory, meter, _ = self.fixture("ours", 0)
        v2 = self.load(directory, meter)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_series_mismatch"):
            validate_series(b, [v2], request_budget=3, read_only=False)

    def test_budget_and_write_mode(self):
        b, o = self.series_v3(n=1)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_budget_mismatch"):
            validate_series(b, o, request_budget=4, read_only=False)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_write_mode_mismatch"):
            validate_series(b, o, request_budget=3, read_only=True)


if __name__ == "__main__":
    unittest.main()
