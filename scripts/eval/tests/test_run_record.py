"""프로토콜 §2·§4·§5의 pre/post·독립 해결 귀속·입력 중단 회귀."""

from __future__ import annotations

import itertools
import json
import os
import sys
import unittest
from unittest.mock import patch
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from aggregate import Bucket, ScanVerdict, classify, count_basis
from ground_truth import core_gt
from manifest import load_manifests
from run_record import RunRecordError, load_run_inputs, read_runner_key
from score import score_run
from test_score import _RunDir, _record

KEY = "basketAccessChallenge"
SOLID = [_record("confirmed", "rest:basket:1", traffic_evidence_refs=["cross"])]


class TestAttribution(_RunDir):
    def counts(self, run):
        scored = score_run(self.load([run])[0], self.correlate,
                           runner_key=self.runner_key, correlatable=self.correlatable)
        return count_basis(scored.result, core_gt()), scored.warnings

    def test_fixture_solve_without_report_is_not_missed(self):
        run = self.make_run("r0", [], pre={KEY: True}, solved={KEY: True})
        counts, warnings = self.counts(run)
        self.assertEqual((counts.confirmed, counts.missed, counts.undecided), (0, 0, 9))
        self.assertTrue(any("사전 해결" in warning for warning in warnings))

    def test_pre_solve_overrides_confirmed_and_traffic(self):
        run = self.make_run("r0", SOLID, pre={KEY: True}, solved={KEY: True})
        self.fixture.evidence(run, traffic={"cross": "rest:basket:1"})
        counts, _ = self.counts(run)
        self.assertEqual((counts.confirmed, counts.false_positive, counts.undecided), (0, 0, 9))

    def test_unknown_or_other_actor_never_gets_confirmed_or_missed(self):
        for actor, records in itertools.product((None, "unknown", "other"), ([], SOLID)):
            with self.subTest(actor=actor, finding=bool(records)):
                run = self.make_run(f"r{actor}-{len(records)}", records, solved={KEY: True}, actor=actor)
                self.fixture.evidence(run, traffic={"cross": "rest:basket:1"})
                counts, warnings = self.counts(run)
                self.assertEqual((counts.confirmed, counts.missed, counts.undecided), (0, 0, 9))
                self.assertTrue(any("귀속 미확인" in warning for warning in warnings))

    def test_direct_engine_event_without_finding_is_missed(self):
        run = self.make_run("r0", [], solved={KEY: True})
        counts, _ = self.counts(run)
        self.assertEqual((counts.missed, counts.undecided), (1, 8))

    def test_direct_event_and_solid_traffic_confirm(self):
        run = self.make_run("r0", SOLID, solved={KEY: True})
        self.fixture.evidence(run, traffic={"cross": "rest:basket:1"})
        counts, _ = self.counts(run)
        self.assertEqual((counts.confirmed, counts.undecided), (1, 8))
        code, out, _ = self.run_main([run])
        self.assertEqual(code, 0)
        self.assertIn("1 (n=1)", out)

    def test_confirmed_unsolved_still_false_positive(self):
        run = self.make_run("r0", SOLID)
        counts, _ = self.counts(run)
        self.assertEqual(counts.false_positive, 1)

    def test_coverage_needs_completed_engine_request(self):
        run = self.make_run("r0", [])
        self.fixture.evidence(run, covered=frozenset({KEY}))
        counts, _ = self.counts(run)
        self.assertEqual((counts.missed, counts.undecided), (1, 8))

    def test_unknown_pre_or_missing_attribution_fails_closed_in_pure_api(self):
        for verdict in ScanVerdict:
            for pre in (None, True):
                self.assertIs(classify(verdict, True, evidence_ok=True, covered=True,
                                       correlatable=True, pre_solved=pre, engine_attributed=True), Bucket.UNDECIDED)
            self.assertIs(classify(verdict, True, evidence_ok=True, covered=True,
                                   correlatable=True, pre_solved=False), Bucket.UNDECIDED)

    def test_entire_protocol_partition_with_pre_and_attribution(self):
        # 全 320 조합: pre 우선, 새 solve의 귀속 우선, 그 뒤 기존 증거/판정표.
        count = 0
        for verdict in ScanVerdict:
            for pre, post, attributed, evidence, covered, correlatable in itertools.product((False, True), repeat=6):
                expected = Bucket.UNDECIDED
                if not pre and (not post or attributed):
                    if verdict is ScanVerdict.CONFIRMED:
                        expected = Bucket.FALSE_POSITIVE if not post else Bucket.CONFIRMED if evidence else Bucket.UNDECIDED
                    elif verdict is ScanVerdict.NONE and correlatable and (post or covered):
                        expected = Bucket.MISSED
                actual = classify(verdict, post, pre_solved=pre, engine_attributed=attributed,
                                  evidence_ok=evidence, covered=covered, correlatable=correlatable)
                self.assertIs(actual, expected)
                count += 1
        self.assertEqual(count, 320)


class TestRunInputGuards(_RunDir):
    def artifact(self, run, name):
        receipt = self.fixture.receipt(run)
        return Path(self._path(run["run_record_path"])).parent / receipt["artifacts"][name]["path"]

    def alter(self, run, name, change, *, reseal=True):
        path = self.artifact(run, name)
        payload = json.loads(path.read_bytes())
        change(payload)
        path.write_text(json.dumps(payload))
        if reseal:
            self.fixture.reseal(run)

    def receipt_change(self, run, change):
        receipt = self.fixture.receipt(run)
        change(receipt)
        self.fixture._write_receipt(run, receipt)

    def stops(self, run, fragment):
        code, out, err = self.run_main([run])
        self.assertEqual((code, out), (2, ""))
        self.assertIn(fragment, err)

    def test_pre_required_and_old_single_post_manifest_rejected(self):
        run = self.make_run("r0", [])
        del run["pre_snapshot_path"]
        self.stops(run, "pre_snapshot_path")

    def test_missing_pre_file_stops_without_table(self):
        run = self.make_run("r0", [])
        self.artifact(run, "pre").unlink()
        self.stops(run, "채점 중단")

    def test_gt_key_required_in_pre_and_post(self):
        for phase in ("pre", "post"):
            run = self.make_run(phase, [])
            self.alter(run, phase, lambda data: data["solved"].pop(KEY))
            self.stops(run, KEY)

    def test_non_bool_pre_rejected(self):
        run = self.make_run("r0", [])
        self.alter(run, "pre", lambda data: data["solved"].update({KEY: "false"}))
        self.stops(run, "불리언")

    def test_same_size_unresigned_change_fails_hash_check(self):
        run = self.make_run("r0", [])
        path = self.artifact(run, "post")
        original = path.read_bytes()
        changed = original.replace(f'"{KEY}": false'.encode(), f'"{KEY}": true '.encode())
        self.assertNotEqual(original, changed)
        self.assertEqual(len(original), len(changed))
        path.write_bytes(changed)
        with self.assertRaisesRegex(RunRecordError, "^post: 파일 해시 불일치$"):
            load_run_inputs(self.load([run])[0], self.runner_key, self.correlate)
        self.stops(run, "post: 파일 해시 불일치")

    def test_unresigned_size_change_fails_size_check(self):
        run = self.make_run("r0", [])
        path = self.artifact(run, "post")
        path.write_bytes(path.read_bytes() + b" ")
        with self.assertRaisesRegex(RunRecordError, "^post: 파일 크기 불일치$"):
            load_run_inputs(self.load([run])[0], self.runner_key, self.correlate)

    def test_raw_and_normalized_disagreement_rejected(self):
        run = self.make_run("r0", [])
        self.alter(run, "post", lambda data: data["solved"].update({KEY: True}))
        self.stops(run, "원본/정규화 불일치")

    def test_true_to_false_and_mid_run_reset_rejected(self):
        run = self.make_run("r0", [], pre={KEY: True})
        self.stops(run, "역행")
        run = self.make_run("r1", [])
        self.alter(run, "post", lambda data: data.update(reset_generation="other"))
        self.stops(run, "reset_generation")

    def test_pre_post_non_gt_key_set_must_match(self):
        run = self.make_run("r0", [])
        self.alter(run, "post", lambda data: data["solved"].update(extra=False))
        self.alter(run, "post_raw", lambda data: data["data"].append(dict(key="extra", solved=False)))
        self.stops(run, "key 집합 불일치")

    def test_runner_context_must_match_manifest(self):
        for field in ("run_id", "model_tag", "reset_generation", "engine"):
            run = self.make_run(field, [])
            receipt = self.fixture.receipt(run)
            receipt[field] = "other"
            for descriptor in receipt["artifacts"].values():
                if descriptor is None:
                    continue
                if field in descriptor:
                    descriptor[field] = "other"
                path = Path(self._path(run["run_record_path"])).parent / descriptor["path"]
                payload = json.loads(path.read_bytes())
                if field in payload:
                    payload[field] = "other"
                    path.write_text(json.dumps(payload))
            self.fixture._write_receipt(run, receipt)
            self.fixture.reseal(run)
            with self.assertRaisesRegex(RunRecordError, f"^러너 기록 {field} 불일치$"):
                load_run_inputs(self.load([run])[0], self.runner_key, self.correlate)
            self.stops(run, f"러너 기록 {field} 불일치")

    def test_all_artifact_metadata_and_phase_bound_to_run(self):
        for name in ("pre", "post_raw", "records", "traffic", "coverage", "solve_events"):
            run = self.make_run(name, [])
            self.fixture.evidence(run, traffic={}, covered=frozenset())
            self.receipt_change(run, lambda data: data["artifacts"][name].update(run_id="other"))
            self.stops(run, "run_id")
        run = self.make_run("phase", [])
        self.receipt_change(run, lambda data: data["artifacts"]["pre"].update(phase="post"))
        self.stops(run, "단계")

    def test_timeline_order_sequence_and_timezone_rejected(self):
        for field, value in (("at", "2026-10-29T00:00:00+00:00"), ("sequence", 10),
                             ("sequence", True), ("at", "2026-10-30T00:00:02")):
            run = self.make_run(f"r{len(os.listdir(self._tmp.name))}", [])
            self.receipt_change(run, lambda data: data["timeline"]["engine_start"].update({field: value}))
            self.stops(run, "채점 중단")

    def test_request_drain_confirmation_required(self):
        run = self.make_run("r0", [])
        self.receipt_change(run, lambda data: data.update(requests_drained=False))
        self.stops(run, "종료 확인")

    def test_manifest_self_declared_attribution_rejected(self):
        run = self.make_run("r0", [], actor=None)
        run["engine_solved"] = [KEY]
        self.stops(run, "모르는 필드")

    def test_unsigned_receipt_and_wrong_key_rejected(self):
        run = self.make_run("r0", [])
        path = Path(self._path(run["run_record_path"]))
        signed = json.loads(path.read_bytes())
        signed["signature"] = "한" * 64
        path.write_text(json.dumps(signed))
        self.stops(run, "서명 불일치")
        self.fixture._write_receipt(run, self.fixture.receipt(run))
        Path(self.key_path).write_bytes(os.urandom(32))
        self.stops(run, "서명 불일치")

    def test_missing_short_or_wrong_key_is_not_silent_undecided(self):
        run = self.make_run("r0", [])
        with self.assertRaisesRegex(RunRecordError, "러너 키"):
            load_run_inputs(self.load([run])[0], None, self.correlate)
        Path(self.key_path).write_bytes(b"short")
        self.stops(run, "러너 키")

    def test_runner_key_requires_owner_only_regular_file(self):
        run = self.make_run("r0", [])
        os.chmod(self.key_path, 0o644)
        self.stops(run, "소유자 전용 권한")
        os.chmod(self.key_path, 0o600)
        self.assertEqual(read_runner_key(self.key_path), self.runner_key)
        link = self._path("key-link")
        Path(link).symlink_to(self.key_path)
        with self.assertRaises(OSError):
            read_runner_key(link)

    def test_path_escape_duplicate_and_symlink_rejected(self):
        for path in ("../outside.json", "/tmp/outside.json", "records.json"):
            run = self.make_run(f"r{len(os.listdir(self._tmp.name))}", [])
            self.receipt_change(run, lambda data: data["artifacts"]["post"].update(path=path))
            self.stops(run, "경로")
        run = self.make_run("symlink", [])
        link = self.artifact(run, "pre")
        link.unlink()
        link.symlink_to(self.key_path)
        self.stops(run, "경로")

    def test_hardlinked_artifact_roles_rejected(self):
        run = self.make_run("r0", [])
        source, target = self.artifact(run, "pre_raw"), self.artifact(run, "post_raw")
        self.assertEqual(source.read_bytes(), target.read_bytes())
        target.unlink()
        os.link(source, target)
        self.assertEqual(source.stat().st_ino, target.stat().st_ino)
        with self.assertRaisesRegex(RunRecordError, "^아티팩트 파일 경로/역할 중복$"):
            load_run_inputs(self.load([run])[0], self.runner_key, self.correlate)

    def test_manifest_paths_allow_symlinked_run_directory(self):
        run = self.make_run("r0", [])
        alias = Path(self._path("run-alias"))
        alias.symlink_to(self._path("r0"), target_is_directory=True)
        for field in ("records_path", "pre_snapshot_path", "snapshot_path", "run_record_path"):
            run[field] = run[field].replace("r0/", "run-alias/", 1)
        inputs = load_run_inputs(self.load([run])[0], self.runner_key, self.correlate)
        self.assertFalse(inputs.post[KEY])

    def test_duplicate_receipt_json_keys_rejected(self):
        run = self.make_run("r0", [])
        path = Path(self._path(run["run_record_path"]))
        path.write_text(path.read_text().replace('"version": 1', '"version": 2, "version": 1'))
        self.stops(run, "key 중복")

    def test_event_execution_window_and_actor_rejected(self):
        changes = ({"execution_id": "other"}, {"observed_at": "2026-10-30T00:00:11Z"},
                   {"sequence": 5}, {"actor": "exclusive_window"})
        for index, change in enumerate(changes):
            run = self.make_run(f"r{index}", [], solved={KEY: True})
            self.alter(run, "solve_events", lambda data: data["events"][0].update(change))
            self.stops(run, "solve event")

    def test_event_cannot_claim_unsolved_or_already_solved_item(self):
        run = self.make_run("r0", [], solved={KEY: True})
        self.alter(run, "solve_events", lambda data: data["events"][0].update(challenge="forgedFeedbackChallenge"))
        self.stops(run, "pre/post 상태")

    def test_duplicate_or_conflicting_events_rejected(self):
        run = self.make_run("r0", [], solved={KEY: True})
        self.alter(run, "solve_events", lambda data: data["events"].append(dict(data["events"][0], id="second", actor="other")))
        self.stops(run, "중복")

    def test_other_run_evidence_envelope_rejected(self):
        for name in ("traffic", "coverage", "solve_events"):
            run = self.make_run(name, [])
            self.fixture.evidence(run, covered=frozenset({KEY}))
            self.alter(run, name, lambda data: data.update(execution_id="old"))
            self.stops(run, "execution_id")

    def test_setup_blocked_or_uncompleted_request_not_coverage(self):
        run = self.make_run("r0", [])
        self.fixture.evidence(run, covered=frozenset({KEY}))
        self.alter(run, "coverage", lambda data: data["inspections"][0].update(completed=False))
        self.stops(run, "실제 엔진 요청")
        run = self.make_run("r1", [])
        self.fixture.evidence(run, covered=frozenset({KEY}))
        self.alter(run, "traffic", lambda data: data.update(phase="fixture"))
        self.stops(run, "엔진 단계")

    def test_traffic_before_start_after_stop_or_after_drain_rejected(self):
        for field, at in (("sent_at", "00"), ("sent_at", "09"), ("completed_at", "11")):
            run = self.make_run(f"r{len(os.listdir(self._tmp.name))}", [])
            self.fixture.evidence(run, traffic={"cross": "rest:basket:1"})
            self.alter(run, "traffic", lambda data: data["requests"][0].update({field: f"2026-10-30T00:00:{at}Z"}))
            self.stops(run, "실행 구간")

    def test_raw_traffic_or_coverage_parameter_cannot_bypass_receipt(self):
        run = self.make_run("r0", [])
        with self.assertRaises(TypeError):
            score_run(self.load([run])[0], self.correlate, runner_key=self.runner_key, covered=frozenset({KEY}))

    def test_different_fixture_environment_or_pre_stops_comparison(self):
        for field in ("fixture_fingerprint", "environment_fingerprint", "pre"):
            left = self.make_run(field + "0", [])
            right = self.make_run(field + "1", [], repeat_index=1,
                                  **({"pre": {KEY: True}, "solved": {KEY: True}} if field == "pre" else {}))
            if field != "pre":
                self.receipt_change(right, lambda data: data.update({field: "f" * 64}))
            code, out, err = self.run_main([left, right])
            self.assertEqual((code, out), (2, ""))
            self.assertIn("동일 조건 비교 중단", err)

    def test_manifest_cannot_relabel_or_change_signed_metrics(self):
        fields = ("condition", "repeat_index", "elapsed_seconds", "total_requests", "blocked_attempts")
        for field in fields:
            run = self.make_run(field, [])
            run[field] = "misleading label" if field == "condition" else 5
            self.stops(run, field)

    def test_receipt_rejects_exclusive_window_self_declaration(self):
        run = self.make_run("r0", [])
        self.receipt_change(run, lambda data: data.update(exclusive_window=True))
        self.stops(run, "필수/미등록")

    def test_snapshot_instance_and_execution_must_match_runner(self):
        for field in ("instance_id", "execution_id"):
            run = self.make_run(field, [])
            self.alter(run, "pre", lambda data: data.update({field: "other"}))
            self.stops(run, field)

    def test_engine_event_inside_pre_post_but_outside_execution_rejected(self):
        for index, at in enumerate(("2026-10-30T00:00:01.500Z", "2026-10-30T00:00:09.500Z")):
            run = self.make_run(f"r{index}", [], solved={KEY: True})
            self.alter(run, "solve_events", lambda data: data["events"][0].update(observed_at=at))
            self.stops(run, "실행 구간")

    def test_nonengine_actor_cannot_keep_engine_execution_id(self):
        run = self.make_run("r0", [], solved={KEY: True})
        self.alter(run, "solve_events", lambda data: data["events"][0].update(actor="other"))
        self.stops(run, "null")

    def test_malformed_fingerprint_and_signed_metric_type_rejected(self):
        for field, value in (("fixture_fingerprint", "same"), ("environment_fingerprint", "z" * 64),
                             ("total_requests", True), ("elapsed_seconds", -1)):
            run = self.make_run(field, [])
            self.receipt_change(run, lambda data: data.update({field: value}))
            self.stops(run, field)

    def test_invalid_nul_or_symlink_loop_path_stops_without_traceback(self):
        for index, path in enumerate(("\x00", "loop/pre.json")):
            run = self.make_run(f"r{index}", [])
            if index:
                (Path(self._path(run["run_record_path"])).parent / "loop").symlink_to("loop")
            self.receipt_change(run, lambda data: data["artifacts"]["pre_raw"].update(path=path))
            self.stops(run, "경로/파일")

    def test_symlink_swap_at_open_cannot_escape_directory(self):
        run = self.make_run("r0", [])
        target = self.artifact(run, "pre")
        actual_open = os.open
        swapped = False

        def race(path, flags, *args, **kwargs):
            nonlocal swapped
            if str(path) == "pre.json" and not swapped:
                target.unlink()
                target.symlink_to(self.key_path)
                swapped = True
            return actual_open(path, flags, *args, **kwargs)

        with patch("run_record.os.open", side_effect=race):
            self.stops(run, "경로/파일")
        self.assertTrue(swapped)

    def test_execution_replay_across_distinct_runs_rejected(self):
        left, right = self.make_run("r0", []), self.make_run("r1", [], repeat_index=1)
        receipt = self.fixture.receipt(right)
        reused = self.fixture.receipt(left)["execution_id"]
        receipt["execution_id"] = reused
        folder = Path(self._path(right["run_record_path"])).parent
        for descriptor in receipt["artifacts"].values():
            if descriptor is not None:
                descriptor["execution_id"] = reused
                path = folder / descriptor["path"]
                payload = json.loads(path.read_bytes())
                if isinstance(payload, dict) and "execution_id" in payload:
                    payload["execution_id"] = reused
                    path.write_text(json.dumps(payload))
        self.fixture._write_receipt(right, receipt)
        self.fixture.reseal(right)
        code, out, err = self.run_main([left, right])
        self.assertEqual((code, out), (2, ""))
        self.assertIn("execution_id 중복", err)

    def test_missing_optional_evidence_is_explicitly_none(self):
        run = self.make_run("r0", [], actor=None)
        inputs = load_run_inputs(self.load([run])[0], self.runner_key, self.correlate)
        self.assertIsNone(inputs.traffic)
        self.assertIsNone(inputs.covered)
        self.assertEqual(inputs.engine_solved, frozenset())


if __name__ == "__main__":
    unittest.main()
