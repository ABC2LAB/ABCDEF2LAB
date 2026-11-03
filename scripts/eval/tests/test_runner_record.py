"""러너 기록 v1.1 작성: 증명·근거 행·해결 이벤트·timeline (run-record-v1.1.md §3·§3.1·§4)."""

from __future__ import annotations

import hashlib
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_record import (
    EVIDENCE_FORMAT, RUN_RECORD_VERSION, STEPS, RecordContext, RecordError, canonical_sha256,
    descriptor, isolation_proof, path_ref, run_record, solve_events, timeline,
)

CONTEXT = RecordContext("ours-1", "model:1", "t" * 64, "t" * 64, "e" * 64)
STAMPS = {step: f"2026-11-03T00:00:0{i}+00:00" for i, step in enumerate(STEPS)}


class TestTimeline(unittest.TestCase):
    def test_six_steps_get_increasing_sequences(self):
        steps = timeline(STAMPS)
        self.assertEqual([steps[s]["sequence"] for s in STEPS], [1, 2, 3, 4, 5, 6])
        self.assertEqual(steps["post"]["at"], STAMPS["post"])

    def test_missing_or_extra_or_empty_steps_rejected(self):
        for stamps in ({k: v for k, v in STAMPS.items() if k != "drained"}, dict(STAMPS, extra="x"),
                       dict(STAMPS, pre=""), dict(STAMPS, pre=None)):
            with self.subTest(keys=sorted(stamps)[:2]), self.assertRaises(RecordError):
                timeline(stamps)


class TestProofAndEvents(unittest.TestCase):
    def proof(self, count=1):
        return isolation_proof(CONTEXT, timeline(STAMPS), "a" * 64, "b" * 64, "http://eval-meter:8080",
                               "compose:eval-ours-1", "p" * 64, "host-condition:ours-1", "c" * 64,
                               "eval-obs:" + "o" * 64, path_ref("/api/Challenges"), count)

    def test_proof_binds_context_window_snapshots_and_evidence(self):
        proof = self.proof()
        self.assertEqual({k: proof[k] for k in CONTEXT.fields()}, CONTEXT.fields())
        steps = timeline(STAMPS)
        self.assertEqual(proof["window"], {"pre": steps["pre"], "post": steps["post"]})
        self.assertEqual(proof["preparation"], {"sealed": steps["fixture_ready"], "actions_after_pre": 0})
        self.assertEqual(proof["snapshots"], {"pre_sha256": "a" * 64, "post_sha256": "b" * 64})
        self.assertEqual(proof["enforcement_evidence_sha256"], "c" * 64)
        self.assertEqual(proof["network"]["mode"], "dedicated_network")
        self.assertEqual(proof["network"]["allowed_actors"], ["engine", "observer"])  # 엔진(계측기 경유)·등록 관측기뿐
        self.assertEqual(proof["observer"]["registered_gets"], [path_ref("/api/Challenges")])
        self.assertEqual(proof["proof_id"], "proof-ours-1")
        # 관측 경로는 해시 참조로만 남는다.
        self.assertNotIn("/api/Challenges", json.dumps(proof))
        self.assertEqual(path_ref("/api/Challenges"), "sha256:" + hashlib.sha256(b"/api/Challenges").hexdigest())

    def test_observer_count_must_be_positive_int(self):
        for count in (0, -1, True, None, 1.0):
            with self.subTest(count=count), self.assertRaises(RecordError):
                self.proof(count)

    def test_events_are_exactly_the_new_solves_bound_to_the_proof(self):
        proof = self.proof()
        digest = canonical_sha256(proof)
        pre = {"a": False, "b": True, "c": False, "d": False}
        post = {"a": True, "b": True, "c": False, "d": True}
        events = solve_events(CONTEXT, pre, post, timeline(STAMPS)["post"], digest)
        self.assertEqual([e["challenge"] for e in events], ["a", "d"])
        for event in events:
            self.assertEqual((event["actor"], event["basis"], event["isolation_proof_sha256"], event["execution_id"],
                              event["sequence"], event["observed_at"]),
                             ("engine", "isolated_window", digest, CONTEXT.execution_id, 6, STAMPS["post"]))
        self.assertEqual(len({e["id"] for e in events}), 2)
        self.assertEqual(solve_events(CONTEXT, pre, pre, timeline(STAMPS)["post"], digest), [])

    def test_regressed_or_mismatched_snapshots_rejected(self):
        post = timeline(STAMPS)["post"]
        with self.assertRaises(RecordError):
            solve_events(CONTEXT, {"a": True}, {"a": False}, post, "d" * 64)
        with self.assertRaises(RecordError):
            solve_events(CONTEXT, {"a": False}, {"a": False, "b": True}, post, "d" * 64)

    def test_canonical_digest_follows_signing_serialization(self):
        value = {"b": [1, "가"], "a": {"z": None}}
        expected = hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"),
                                             ensure_ascii=False).encode("utf-8")).hexdigest()
        self.assertEqual(canonical_sha256(value), expected)
        self.assertNotEqual(canonical_sha256({"a": 1}), canonical_sha256({"a": 2}))


class TestObserverWindows(unittest.TestCase):
    def test_rows_must_match_the_runner_call_order(self):
        from runner_record import _observer_windows
        rows = [{"seq": i} for i in range(4)]
        self.assertEqual(_observer_windows(rows, 2), ["before_pre", "before_pre", "pre", "in_window"])
        for ready in (1, 3):  # 관측 행이 러너 호출 수보다 많거나 적으면 증명할 수 없다
            with self.subTest(ready=ready), self.assertRaises(RecordError):
                _observer_windows(rows, ready)


class TestConnectivityRows(unittest.TestCase):
    def test_probe_kind_does_not_collide_with_row_kind(self):
        from runner_record import _connectivity
        probe = {"id": "dns:target", "kind": "dns", "target": "target", "role": "negative", "expected": "fail",
                 "accepted": ["not_found", "dns_error"], "observed": "fail", "code": "dns_error", "verdict": "match"}
        row = _connectivity("pre", "t", probe)
        self.assertEqual((row["kind"], row["probe_kind"], row["phase"], row["id"]),
                         ("connectivity_check", "dns", "pre", "dns:target"))
        self.assertEqual(set(row) - set(probe), {"probe_kind", "phase", "at"})
        self.assertEqual(probe["kind"], "dns")  # 입력은 바꾸지 않는다


class TestRecord(unittest.TestCase):
    def test_record_fields_and_nulls(self):
        artifacts = {"records": None, "evidence": descriptor(CONTEXT, "x.json", "f" * 64, 3, "engine")}
        record = run_record(CONTEXT, engine="ours", condition="우리@35b", repeat_index=2, total_requests=7,
                            fixture_fingerprint="1" * 64, environment_fingerprint="2" * 64,
                            steps=timeline(STAMPS), artifacts=artifacts, proof={"proof_id": "p"})
        self.assertEqual((record["version"], record["engine"], record["condition"], record["repeat_index"]),
                         (RUN_RECORD_VERSION, "ours", "우리@35b", 2))
        self.assertEqual((record["elapsed_seconds"], record["blocked_attempts"], record["total_requests"],
                          record["requests_drained"]), (None, None, 7, True))
        self.assertEqual(record["artifacts"]["evidence"],
                         dict(CONTEXT.fields(), path="x.json", sha256="f" * 64, size=3, phase="engine"))
        self.assertEqual(RUN_RECORD_VERSION, "1.1")
        self.assertEqual(EVIDENCE_FORMAT, "abc2lab-isolation-evidence-v1")


if __name__ == "__main__":
    unittest.main()
