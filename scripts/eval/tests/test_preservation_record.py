"""보호된 합성 원자료의 재계산·변조·조건 혼입 시험. 모델·대상 네트워크는 사용하지 않는다."""
from __future__ import annotations

import copy
import hashlib
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from path_mask import key_ref, mask_path
from preservation import EXCLUSIONS_FORMAT, Round, summarize, Exclusions
from preservation_record import (NAME, OPTIONS_FORMAT, PreservationRecordError, assessment, build_payload,
                                 full_budget_from_pilots, load_record, preservation_verdict, validate_options, validate_series)
from run_record import sign_run_record
from runner_evidence import MeterFiles, EvidenceError, PATH_PROFILE
import boundary_fixture as bf

# 시험 전용 값이다. 어떤 실제 인증값도 fixture에 넣지 않는다.
PATH_KEY = bytes(range(32))
RUNNER_KEY = bytes(range(32, 64))


def options():
    return {"format": OPTIONS_FORMAT, "series_ref": "synthetic-preservation-1", "comm_mode": "full_local",
            "write_mode": "approved_writes", "mcp_version": "0.0.84", "mcp_tools_sha256": "1" * 64,
            "browser_image_id": "sha256:" + "2" * 64, "exploration_policy_sha256": "3" * 64,
            "declared_model_budget_sha256": "4" * 64, "execution_source": "synthetic_fixture"}


def conditions(side="baseline", **changes):
    return {"layout": "artex-direct/v1" if side == "baseline" else "exploration-split/v1",
            "request_budget": 3, "time_budget_seconds": 60, "model_pin_sha256": "5" * 64,
            "target_image_id": "sha256:" + "6" * 64, "initial_state_sha256": "7" * 64,
            "machine_sha256": "8" * 64, "runner_settings_sha256": "0" * 64, "comm_mode": "full_local", "write_mode": "approved_writes",
            "mcp_version": "0.0.84", "mcp_tools_sha256": "1" * 64, "browser_image_id": "sha256:" + "2" * 64,
            "exploration_policy_sha256": None if side == "baseline" else "3" * 64,
            "diagnosis_policy_sha256": None if side == "baseline" else "9" * 64,
            "declared_model_budget_sha256": "4" * 64, **changes}


class SignedFixture(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.serial = 0

    def write(self, directory, name, value):
        path = directory / name
        path.write_bytes(value if isinstance(value, bytes) else json.dumps(value, sort_keys=True).encode() + b"\n")
        path.chmod(0o600)

    def fixture(self, side="baseline", repeat=0, *, meta=None, source="synthetic_fixture", boundary=None):
        self.serial += 1
        directory = self.root / str(self.serial)
        directory.mkdir(mode=0o700)
        rows, paths = [], []
        for seq, (raw_path, ingress) in enumerate((("/a", "main"), ("/b", "explore"), ("/c", "explore")), 1):
            if side == "baseline": ingress = "main"
            rows.append({"seq": seq, "kind": "request", "method": "GET", "status": 200, "started_at": "a",
                         "completed_at": "b", "response_bytes": 0, "outcome": "completed", "sent": "yes",
                         "response": "complete", "attempt_id_state": "absent", "attempt_id": "", "ingress": ingress})
            pattern, count = mask_path(raw_path, PATH_KEY)
            paths.append({"seq": seq, "method": "GET", "path_pattern": pattern, "masked_segments": count,
                          "path_profile_ref": PATH_PROFILE, "path_key_ref": key_ref(PATH_KEY),
                          "has_authorization": False, "has_cookie": False, "ingress": ingress})
        self.write(directory, "engine-requests.jsonl", b"".join(json.dumps(row).encode()+b"\n" for row in rows))
        self.write(directory, "engine-paths.jsonl", b"".join(json.dumps(row).encode()+b"\n" for row in paths))
        self.write(directory, "engine-sealed.json", {"role": "engine"})
        self.write(directory, "engine-drained.json", {"role": "engine", "forwarded_requests": 3,
                   "log_complete": True, "inflight_at_drain": 0, "forced_cancelled_requests": 0})
        meter = MeterFiles(directory)
        want = boundary if boundary is not None else side == "ours" and (meta or {}).get("layout", "exploration-split/v1") == "exploration-split/v1"
        boundary = None
        if want:
            # 계측기 탐색 입구 행(/b·/c)과 맞는 경계 원자료. 차단·외부 자원 행은 대조에 들지 않는다.
            rows = [bf.approved(1, "/b", PATH_KEY), bf.approved(2, "/c", PATH_KEY, ingress="direct"),
                    bf.denied(3, "out_of_scope_path"), bf.external(4)]
            boundary = {"directory": bf.write(directory / "boundary", rows, PATH_KEY), "task_id": bf.TASK_ID,
                        "path_key_ref": key_ref(PATH_KEY)}
        payload = build_payload(meter=meter, options=options() | {"execution_source": source},
            run_id=f"{side}-{self.serial}", side=side, repeat_index=repeat,
            engine_source_sha=("a" if side == "baseline" else "b") * 40,
            engine_image_id="sha256:" + ("a" if side == "baseline" else "b") * 64,
            conditions=conditions(side, **(meta or {})), stop_reason="request_budget", boundary=boundary)
        self.write(directory, NAME, sign_run_record(payload, RUNNER_KEY))
        return directory, meter, payload

    def load(self, directory, meter):
        boundary = directory / "boundary"
        return load_record(directory, meter, RUNNER_KEY, boundary if boundary.exists() else None)

    def series(self):
        baseline, improved = [], []
        for i in range(3):
            for side, records in (("baseline", baseline), ("ours", improved)):
                directory, meter, _ = self.fixture(side, i)
                records.append(self.load(directory, meter))
        return baseline, improved

    def test_signed_raw_round_traces_both_ingresses_and_synthetic_not_run(self):
        b, o = self.series()
        self.assertEqual(o[0]["forwarded_by_ingress"], {"main": 1, "explore": 2})
        verified = validate_series(b, o, request_budget=3, read_only=False)
        self.assertEqual(verified["verified_signed_rounds"], 6)
        # 합성 경계 원자료도 장부 대조는 통과하지만, 합성 자료는 실측으로 승격하지 않는다.
        self.assertEqual(verified["exploration_boundary"]["status"], "PASS")
        self.assertEqual(assessment({}, "R-F", verified), {"profile": "R-F", "status": "NOT_RUN", "reason": "real_split_model_measurement_not_run"})
        self.assertNotIn("api_key", json.dumps(verified))

    def test_raw_bytes_tamper_even_semantically_equal_json_is_rejected(self):
        directory, meter, _ = self.fixture()
        path = directory / "engine-requests.jsonl"
        path.write_bytes(path.read_bytes().replace(b'"seq": 1', b'"seq":  1'))
        with self.assertRaisesRegex(PreservationRecordError, "preservation_raw_digest_mismatch"):
            self.load(directory, meter)

    def test_signature_and_duplicate_json_rejected(self):
        directory, meter, payload = self.fixture()
        self.write(directory, NAME, sign_run_record(payload | {"stop_reason": "time_budget"}, b"x" * 32))
        with self.assertRaisesRegex(PreservationRecordError, "preservation_signature_invalid"):
            self.load(directory, meter)
        self.write(directory, NAME, b'{"format":1,"format":2}')
        with self.assertRaisesRegex(PreservationRecordError, "preservation_duplicate_key"):
            self.load(directory, meter)

    def test_missing_final_files_or_public_directory_rejected(self):
        directory, meter, _ = self.fixture()
        directory.chmod(0o750)
        with self.assertRaises(EvidenceError): self.load(directory, meter)
        directory.chmod(0o700)
        (directory / "engine-drained.json").unlink()
        with self.assertRaisesRegex(PreservationRecordError, "preservation_raw_missing"):
            self.load(directory, meter)

    def test_hashed_but_inconsistent_drain_and_ingress_are_rejected(self):
        for field, change in (("engine-drained.json", {"role": "engine", "forwarded_requests": 9,
            "log_complete": True, "inflight_at_drain": 0, "forced_cancelled_requests": 0}),
            ("engine-paths.jsonl", None)):
            directory, meter, payload = self.fixture("ours")
            if change is None:
                raw = (directory / field).read_bytes().replace(b'"ingress": "explore"', b'"ingress": "main"')
                self.write(directory, field, raw)
            else: self.write(directory, field, change)
            # 재서명된 hash도 마감/입구 모순을 유효하게 만들지 않는다.
            import hashlib
            raw = (directory / field).read_bytes()
            payload["files"][field] = {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)}
            self.write(directory, NAME, sign_run_record(payload, RUNNER_KEY))
            with self.assertRaises((PreservationRecordError, EvidenceError)):
                self.load(directory, meter)

    def test_reader_rejects_signed_ingress_counts_that_disagree_with_raw_rows(self):
        directory, meter, payload = self.fixture("ours")
        payload["forwarded_by_ingress"] = {"main": 2, "explore": 1}
        self.write(directory, NAME, sign_run_record(payload, RUNNER_KEY))
        with self.assertRaisesRegex(PreservationRecordError, "preservation_ingress_mismatch"):
            self.load(directory, meter)

    def test_ours_cannot_impersonate_artex_direct_even_with_all_main_rows(self):
        directory, meter, payload = self.fixture("baseline")
        payload["side"] = "ours"
        self.write(directory, NAME, sign_run_record(payload, RUNNER_KEY))
        with self.assertRaisesRegex(PreservationRecordError, "preservation_conditions_invalid"):
            self.load(directory, meter)

    def test_reader_requires_ingress_before_any_later_count_comparison(self):
        directory, meter, payload = self.fixture("baseline")
        path = directory / "engine-requests.jsonl"
        rows = [json.loads(line) for line in path.read_bytes().splitlines()]
        for row in rows: del row["ingress"]
        self.write(directory, path.name, b"".join(json.dumps(row).encode() + b"\n" for row in rows))
        raw = path.read_bytes()
        payload["files"][path.name] = {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)}
        self.write(directory, NAME, sign_run_record(payload, RUNNER_KEY))
        with self.assertRaisesRegex(EvidenceError, "meter_ingress_missing_or_mixed"):
            self.load(directory, meter)

    def test_legacy_sidecar_cannot_hide_exploration_forwarding(self):
        directory, meter, payload = self.fixture("ours")
        payload["conditions"]["layout"] = "e2-legacy/v1"
        payload["exploration_boundary"] = None
        self.write(directory, NAME, sign_run_record(payload, RUNNER_KEY))
        with self.assertRaisesRegex(EvidenceError, "meter_explore_ingress_unexpected"):
            self.load(directory, meter)

    def test_initial_state_takes_exactly_one_well_formed_target_state(self):
        from preservation_record import initial_state_digest
        good = initial_state_digest(solved={"k": False})
        self.assertNotEqual(good, initial_state_digest(solved={"k": True}))
        self.assertNotEqual(initial_state_digest(observation_sha256="a" * 64, observation_bytes=3),
                            initial_state_digest(observation_sha256="a" * 64, observation_bytes=4))
        for kwargs in ({}, {"solved": {}}, {"solved": {"k": 1}}, {"solved": {"k": "false"}}, {"solved": [("k", False)]},
                       {"solved": {"k": False}, "observation_sha256": "a" * 64, "observation_bytes": 3},
                       {"observation_sha256": "a" * 63, "observation_bytes": 3},
                       {"observation_sha256": "a" * 64, "observation_bytes": 0},
                       {"observation_sha256": "a" * 64, "observation_bytes": True},
                       {"observation_sha256": "a" * 64}):
            with self.subTest(kwargs=sorted(kwargs)), self.assertRaisesRegex(PreservationRecordError,
                                                                             "preservation_initial_state_invalid"):
                initial_state_digest(**kwargs)

    def test_model_pins_modes_policy_mcp_and_layout_cannot_be_pooled(self):
        b, o = self.series()
        changes = {"model_pin_sha256": "a" * 64, "comm_mode": "external_allowed", "write_mode": "read_only",
                   "exploration_policy_sha256": "a" * 64, "diagnosis_policy_sha256": "a" * 64,
                   "mcp_version": "0.0.85", "mcp_tools_sha256": "a" * 64, "browser_image_id": "sha256:" + "a" * 64,
                   "declared_model_budget_sha256": "a" * 64, "initial_state_sha256": "a" * 64, "runner_settings_sha256": "a" * 64,
                   "layout": "e2-legacy/v1"}
        for field, value in changes.items():
            with self.subTest(field=field):
                changed = copy.deepcopy(o)
                changed[1]["conditions"][field] = value
                with self.assertRaises(PreservationRecordError): validate_series(b, changed, request_budget=3, read_only=False)

    def test_pair_identity_budget_and_source_cannot_be_mixed(self):
        b, o = self.series()
        for field, value in (("run_id", b[0]["run_id"]), ("repeat_index", 8), ("engine_source_sha", "c" * 40),
                             ("execution_source", "model_run"), ("series_ref", "different-series")):
            with self.subTest(field=field):
                changed = copy.deepcopy(o); changed[1][field] = value
                with self.assertRaises(PreservationRecordError): validate_series(b, changed, request_budget=3, read_only=False)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_budget_mismatch"):
            validate_series(b, o, request_budget=6, read_only=False)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_write_mode_mismatch"):
            validate_series(b, o, request_budget=3, read_only=True)

    def test_readonly_comparison_uses_full_original_baseline_without_mixing_modes(self):
        b, o = self.series()
        for record in o: record["conditions"]["write_mode"] = "read_only"
        self.assertEqual(validate_series(b, o, request_budget=3, read_only=True)["verified_signed_rounds"], 6)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_write_mode_mismatch"):
            validate_series(b, o, request_budget=3, read_only=False)

    def test_declarations_do_not_claim_boundary_or_original_model_measurement(self):
        b, o = self.series()
        for record in b + o: record["execution_source"] = "model_run"
        # 경계 원자료 요약이 없는(v1) 개선 후 기록은 선언만으로 경계를 주장하지 못한다.
        for record in o:
            record["format"] = "abc2lab.preservation-round/v1"
            del record["exploration_boundary"]
        for record in b:
            record["format"] = "abc2lab.preservation-round/v1"
            del record["exploration_boundary"]
        evidence = validate_series(b, o, request_budget=3, read_only=False)
        self.assertEqual(assessment({}, "R-F", evidence), {"profile": "R-F", "status": "NOT_RUN",
                                                        "reason": "verified_boundary_export_unavailable"})

    def test_split_layout_requires_boundary_and_other_layouts_refuse_it(self):
        with self.assertRaisesRegex(PreservationRecordError, "preservation_boundary_missing"):
            self.fixture("ours", meta={"layout": "exploration-split/v1"}, boundary=False)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_boundary_unexpected"):
            self.fixture("baseline", boundary=True)

    def test_signed_boundary_block_must_match_final_boundary_files(self):
        directory, meter, payload = self.fixture("ours")
        # 표식과 행을 서로 맞게 다시 써도(차단 사유를 바꿈) 서명된 요약과 다르면 거부한다.
        bf.write(directory / "boundary_new", [bf.approved(1, "/b", PATH_KEY), bf.approved(2, "/c", PATH_KEY, ingress="direct"),
                                               bf.denied(3, "risky_path"), bf.external(4)], PATH_KEY)
        for name in (bf.ROWS, bf.MARKER):
            (directory / "boundary_new" / name).replace(directory / "boundary" / name)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_boundary_mismatch"):
            self.load(directory, meter)
        with self.assertRaisesRegex(PreservationRecordError, "preservation_boundary_files_missing"):
            load_record(directory, meter, RUNNER_KEY)
        b, bm, _ = self.fixture("baseline")
        with self.assertRaisesRegex(PreservationRecordError, "preservation_boundary_unexpected"):
            load_record(b, bm, RUNNER_KEY, directory / "boundary")

    def test_boundary_block_on_non_split_layout_is_invalid(self):
        directory, meter, payload = self.fixture("ours")
        payload["conditions"]["layout"] = "e2-legacy/v1"
        self.write(directory, NAME, sign_run_record(payload, RUNNER_KEY))
        with self.assertRaisesRegex(PreservationRecordError, "preservation_boundary_invalid"):
            self.load(directory, meter)

    def test_series_boundary_status_and_assessment_reason(self):
        b, o = self.series()
        for record in b + o: record["execution_source"] = "model_run"
        not_opened = {"status": "not_opened", "binding_sha256": None, "closed_reason": None, "counters": None, "reconciliation": None}
        truncated = {"status": "truncated", "counters": None, "reconciliation": None}
        for change, status, reason in ((not_opened, "NOT_RUN", "boundary_session_not_opened"),
                                       (truncated, "UNDECIDABLE", "boundary_export_truncated")):
            with self.subTest(reason=reason):
                changed = copy.deepcopy(o)
                changed[1]["exploration_boundary"] |= change
                evidence = validate_series(b, changed, request_budget=3, read_only=False)
                boundary = evidence["exploration_boundary"]
                self.assertEqual((boundary["status"], boundary["reason"]), (status, reason))
                # 원자료를 못 쓴 회차의 카운터는 0이 아니라 null이다.
                self.assertIsNone(boundary["rounds"][1]["counters"])
                self.assertEqual(assessment({}, "R-F", evidence), {"profile": "R-F", "status": "NOT_RUN", "reason": reason})
        evidence = validate_series(b, o, request_budget=3, read_only=False)
        self.assertEqual((evidence["exploration_boundary"]["status"], evidence["exploration_boundary"]["reason"]), ("PASS", "boundary_export_verified"))
        self.assertEqual([r["counters"]["denied"]["total"] for r in evidence["exploration_boundary"]["rounds"]], [1, 1, 1])
        # 경계가 PASS여도 판정은 기존 수용선(유효 짝 3개 이상)만 쓴다.
        summary = {"read_only": False, "pair_ratios": [1.0, 1.0], "pair_mean": 1.0}
        self.assertEqual(assessment(summary, "R-F", evidence)["reason"], "valid_pairs_lt_3")

    def test_v1_and_v2_records_cannot_be_pooled(self):
        b, o = self.series()
        changed = copy.deepcopy(o)
        changed[0]["format"] = "abc2lab.preservation-round/v1"
        del changed[0]["exploration_boundary"]
        changed[0]["conditions"]["layout"] = "exploration-split/v1"
        with self.assertRaisesRegex(PreservationRecordError, "preservation_series_mismatch"):
            validate_series(b, changed, request_budget=3, read_only=False)

    def test_options_schema_is_closed_and_contains_no_free_secret_fields(self):
        for value in (options() | {"api_key": "not-a-secret"}, options() | {"mcp_version": "latest"},
                      options() | {"execution_source": "fallback"}, options() | {"comm_mode": "anything"}):
            with self.assertRaises(PreservationRecordError): validate_options(value)

    def test_cli_refuses_a_signed_summary_of_synthetic_rounds(self):
        # 합성 fixture 기록은 서명·형식 검증까지만 하고 보존율 요약은 내지 않는다(2026-11-18 사용자 결정). 출력은 비어 있다.
        b, _, _ = self.fixture("baseline")
        o, _, _ = self.fixture("ours")
        self.write(self.root, "path.key", PATH_KEY)
        self.write(self.root, "runner.key", RUNNER_KEY)
        self.write(self.root, "exclusions.json", {"format": EXCLUSIONS_FORMAT, "methods": [], "path_prefixes": []})
        signed = [sys.executable, "-I", str(Path(__file__).resolve().parents[1] / "preservation.py"),
                  "--baseline", str(b), "--improved", str(o), "--budget", "3", "--exclusions", str(self.root / "exclusions.json"),
                  "--path-key-file", str(self.root / "path.key"), "--baseline-record-dir", str(b), "--improved-record-dir", str(o),
                  "--improved-boundary-dir", str(o / "boundary"), "--runner-key-file", str(self.root / "runner.key"),
                  "--profile", "R-F"]
        result = subprocess.run(signed, capture_output=True)
        self.assertEqual((result.returncode, result.stdout), (2, b""))
        self.assertEqual(result.stderr, b"preservation: preservation_synthetic_fixture\n")

    def test_cli_signed_rounds_and_historical_summary_are_distinct(self):
        b, bm, _ = self.fixture("baseline", source="model_run")
        o, om, _ = self.fixture("ours", source="model_run")
        self.write(self.root, "path.key", PATH_KEY)
        self.write(self.root, "runner.key", RUNNER_KEY)
        self.write(self.root, "exclusions.json", {"format": EXCLUSIONS_FORMAT, "methods": [], "path_prefixes": []})
        args = [sys.executable, "-I", str(Path(__file__).resolve().parents[1] / "preservation.py"),
                "--baseline", str(b), "--improved", str(o), "--budget", "3", "--exclusions", str(self.root / "exclusions.json"),
                "--path-key-file", str(self.root / "path.key")]
        raw = subprocess.run(args, capture_output=True)
        self.assertEqual(raw.returncode, 0)
        self.assertEqual(json.loads(raw.stdout)["format"], "abc2lab.preservation-summary/v9")
        signed = args + ["--baseline-record-dir", str(b), "--improved-record-dir", str(o), "--improved-boundary-dir", str(o / "boundary"),
                         "--runner-key-file", str(self.root / "runner.key"), "--profile", "R-F"]
        result = subprocess.run(signed, capture_output=True)
        self.assertEqual(result.returncode, 0)
        data = json.loads(result.stdout)
        self.assertEqual(data["format"], "abc2lab.preservation-summary/v10")
        self.assertEqual((data["assessment"]["status"], data["assessment"]["reason"]), ("UNDECIDABLE", "valid_pairs_lt_3"))  # 실측 출처라 수용선까지 가고, 짝이 1개라 판단불가
        self.assertEqual(data["signed_evidence"]["exploration_boundary"]["status"], "PASS")
        self.assertEqual(data["signed_evidence"]["verified_signed_rounds"], 2)
        # 타입이 잘못되거나 비 ASCII인 서명도 traceback 없이 같은 validation 코드로 닫힌다.
        record = json.loads((o / NAME).read_text())
        for signature in ("한" * 64, "é" * 64, "0" * 63, 123):
            self.write(o, NAME, record | {"signature": signature})
            invalid = subprocess.run(signed, capture_output=True)
            self.assertEqual((invalid.returncode, invalid.stdout), (2, b""))
            self.assertEqual(invalid.stderr, b"preservation: preservation_signature_invalid\n")
        self.write(o, NAME, record)
        # 두 키 각각 근거 디렉터리 안에 있을 때만 거부하는 가드 자체를 검증한다.
        for key_name, flag, key in (("path.key", "--path-key-file", PATH_KEY), ("runner.key", "--runner-key-file", RUNNER_KEY)):
            self.write(b, key_name, key)
            invalid = subprocess.run(signed + [flag, str(b / key_name)], capture_output=True)
            self.assertEqual((invalid.returncode, invalid.stdout), (2, b""))
            self.assertEqual(invalid.stderr, b"preservation: key_inside_evidence_directory\n")
        for profile, reason in (("R-R", "preservation_profile_domain_mismatch"), ("R-P", "preservation_profile_budget_mismatch")):
            invalid_profile = subprocess.run(signed[:-1] + [profile], capture_output=True)
            self.assertEqual(invalid_profile.returncode, 2)
            self.assertEqual(invalid_profile.stdout, b"")
            self.assertEqual(invalid_profile.stderr, ("preservation: " + reason + "\n").encode())
        # 잘못된 조합에서 원자료/경로/키값을 출력하지 않는다.
        bad = subprocess.run(args + ["--profile", "R-F"], capture_output=True)
        self.assertEqual((bad.returncode, bad.stdout), (2, b""))
        self.assertEqual(bad.stderr, b"preservation: signed_round_arguments_incomplete\n")


class ExistingAcceptanceContract(unittest.TestCase):
    def test_full_and_readonly_thresholds_and_three_valid_pairs_are_unchanged(self):
        for profile, threshold in (("R-F", 0.90), ("R-R", 0.95)):
            summary = {"read_only": profile == "R-R", "pair_ratios": [threshold] * 3, "pair_mean": threshold}
            self.assertEqual(preservation_verdict(summary, profile)["status"], "PASS")
            self.assertEqual(preservation_verdict(summary | {"pair_mean": threshold - 0.00001}, profile)["status"], "FAIL")
            self.assertEqual(preservation_verdict(summary | {"pair_ratios": [1, 1]}, profile)["status"], "UNDECIDABLE")

    def test_zero_denominator_does_not_become_an_extra_valid_pair(self):
        one = Round(frozenset({("GET", "/a")}), 3, 0, 3, 0, 0, reached_budget=True)
        zero = Round(frozenset(), 3, 0, 3, 0, 0, reached_budget=True)
        summary = summarize([one, zero, one], [one, one, one], Exclusions(frozenset(), ()), False)
        self.assertEqual(len(summary["pair_ratios"]), 2)
        self.assertEqual(preservation_verdict(summary, "R-F")["status"], "UNDECIDABLE")

    def test_first_valid_time_pilot_n0_times_two_not_best_later_result(self):
        def complete(reason, count):
            return {"format": "abc2lab-eval-runner-result-v1", "status": "complete",
                    "meter": {"stop_reason": reason, "forwarded_requests": count}}
        invalid = {"format": "abc2lab-eval-runner-result-v1", "status": "invalid",
                   "reasons": ["meter_log_inconsistent", "down_failed"], "failed_step": "meter_log_inconsistent"}
        pilots = [complete("cap_exhausted", 1000), invalid, complete("time_budget", 17), complete("time_budget", 999)]
        result = full_budget_from_pilots(pilots)
        self.assertEqual((result["selected_index"], result["n0"], result["request_budget"]), (3, 17, 34))
        self.assertEqual(result["discarded_stop_reasons"], ["cap_exhausted", "invalid"])
        self.assertEqual(result["discarded_pilots"][1], {"attempt_index": 2, "status": "invalid", "stop_reason": None,
            "discard_reason": "invalid", "reasons": ["meter_log_inconsistent", "down_failed"], "failed_step": "meter_log_inconsistent"})
        with self.assertRaisesRegex(PreservationRecordError, "preservation_pilot_count_invalid"):
            full_budget_from_pilots([complete("time_budget", 0), pilots[2]])
        with self.assertRaisesRegex(PreservationRecordError, "preservation_valid_time_pilot_missing"):
            full_budget_from_pilots(pilots[:2])
        with self.assertRaisesRegex(PreservationRecordError, "preservation_pilot_meter_missing"):
            full_budget_from_pilots([{"format": "abc2lab-eval-runner-result-v1", "status": "complete",
                                     "stop_reason": "time_budget", "forwarded_requests": 99}])
        # 무효 회차에 meter 잔재가 있더라도 N0로 선택하거나 실패 이유를 지우지 않는다.
        with_meter = invalid | {"meter": {"stop_reason": "time_budget", "forwarded_requests": 999}}
        chosen = full_budget_from_pilots([with_meter, pilots[2]])
        self.assertEqual(chosen["n0"], 17)
        self.assertEqual(chosen["discarded_pilots"][0]["reasons"], invalid["reasons"])


if __name__ == "__main__": unittest.main()
