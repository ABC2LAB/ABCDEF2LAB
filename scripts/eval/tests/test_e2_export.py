"""제품 E② 시도 행·manifest 검사(E②–러너 연결 제안 ER01·ER04의 러너 소비자 쪽). 합성 입력만 쓰며 제품 생산자·실제 저널 없음."""

from __future__ import annotations

import hashlib
import json
import re
import sys
import traceback
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from attempt_reconcile import reconcile  # noqa: E402
from e2_export import E2_DECISIONS, E2_HEADER_MODES, E2_REASONS, E2Expectation, E2ExportError, parse_attempts, parse_export, parse_manifest  # noqa: E402

ROOT = Path(__file__).resolve().parents[3]
A1, A2, A3 = "0" * 31 + "1", "0" * 31 + "2", "0" * 31 + "3"
CALL = "call_" + "0" * 32
SCOPE = {"TaskRef": "17", "CaseRef": "case-1", "ResetRef": "reset-1", "PolicyRef": "policy-1", "RunRef": "e2-run-1"}
FINGERPRINT = "f" * 64
SECRET_LIKE = "synthetic-value-41c2"
EXPECTED = E2Expectation(eval_run_id="eval-run-1", model_tag="model-1", reset_generation="reset-1", reservation_ref="reservation-1",
                         engine_task_id="17", policy_fingerprint_sha256=FINGERPRINT, header_mode="evaluation_caller_passthrough",
                         llm_backend="api")
# 파서가 따라가는 Go 정의. 바뀌면 파서를 다시 맞추고 값을 갱신한다.
GO_SHAPES = {"e2proxy/audit.go": {"func validJournalEvent(": "d765fb9f5c35ed6fe3c509e416f9335bd92484b8786e995e8ac335b97dd7bb5e", "func validJournalRef(": "19e535e1f750fa71646b37c58e528f0a2409c5d182d45e2f57889e7f2e26fb77", "func validJournalTime(": "ba9dc33173dcd0c3498013c1ddb3a9dfa93539240a5371212e42ed7b104740a6"},
             "e2proxy/types.go": {"type Receipt struct": "8cb1eb5347778bf51562a78ae1b2f69d87fd58fddaf1b1515263ad7850983ce8", "type Event struct": "b76400d2b19d095202ef7f2be6a32264645d6104f4f8aa274fe60701c23df7a2"}}


def go_block(text, start):
    out, inside = [], False
    for line in text.splitlines():
        inside = inside or line.startswith(start)
        if inside:
            out.append(line)
            # 한 줄 함수는 첫 줄에서 끝난다.
            if line == "}" or (len(out) == 1 and line.endswith("}")):
                return "\n".join(out) + "\n"
    raise AssertionError(start)


def common(index, stage, sequence, reason, not_sent=True):
    return {"index": index, "stage": stage, "intent_id": 22, "version": "e2-http-v1", "scope": dict(SCOPE), "policy_revision": "revision-1",
            "sequence": sequence, "reason": reason, "forward_attempted": False, "request_written": False, "response_received": False,
            "response_complete": False, "not_sent": not_sent, "audit_complete": True, "at": "2026-11-07T00:00:00.123456789Z"}


def attempt(index, stage, attempt_id, sequence, decision, reason="complete", **changes):
    base = common(index, stage, sequence, reason) | {"call_ref": CALL, "command_ref": "tool-call-1", "attempt_id": attempt_id,
                                                     "decision": decision, "method": "GET"}
    if decision == "allowed":
        base["rule_ref"] = "rule-1"
    return base | changes


SENT = {"forward_attempted": True, "request_written": True, "response_received": True, "response_complete": True, "not_sent": False, "status_code": 200}


def control(index, reason="complete", **changes):
    return common(index, "control", 0, reason) | changes


def closed(index, reason="complete", **changes):
    return common(index, "call_closed", 0, reason) | {"call_ref": CALL} | changes


def normal_rows():
    return [control(1), attempt(2, "intent", A1, 1, "allowed"), attempt(3, "result", A1, 1, "allowed", **SENT),
            attempt(4, "result", A2, 2, "denied", "policy_denied"), attempt(5, "result", A3, 3, "unsupported", "unsupported", method=""),
            closed(6)]


def encode(rows):
    return b"".join(json.dumps(r, ensure_ascii=False).encode() + b"\n" for r in rows)


def manifest_for(data=b"", rows=0, last_index=0, last_sequence=0, **changes):
    base = {"format": "abc2lab.e2-runner-manifest/v1", "run_ref": "e2-run-1", "eval_run_id": "eval-run-1", "model_tag": "model-1",
            "reset_generation": "reset-1", "reservation_ref": "reservation-1", "engine_task_id": "17", "scope": dict(SCOPE),
            "policy_revision": "revision-1", "policy_fingerprint_sha256": FINGERPRINT, "header_mode": "evaluation_caller_passthrough",
            "evaluation": {"run_ref": "e2-run-1", "llm_backend": "api", "target_kind": "public_practice"}, "state": "complete",
            "attempts_file": "attempts.jsonl", "attempts_sha256": hashlib.sha256(data).hexdigest(), "rows": rows,
            "last_event_index": last_index, "last_attempt_sequence": last_sequence, "audit_journal_file": "e2.audit",
            "audit_checkpoint_file": "e2.checkpoint", "audit_checkpoint_sha256": "c" * 64, "close_request_ref": "close-1"}
    return base | changes


def good():
    data = encode(normal_rows())
    return manifest_for(data, 6, 6, 3), data


class TestManifest(unittest.TestCase):
    def rejected(self, manifest, code, expected=EXPECTED):
        with self.assertRaises(E2ExportError) as caught:
            parse_manifest(json.dumps(manifest).encode(), expected)
        self.assertEqual(str(caught.exception), f"e2_export: {code}")

    def test_good_manifest_passes(self):
        self.assertEqual(parse_manifest(json.dumps(good()[0]).encode(), EXPECTED)["state"], "complete")

    def test_values_the_runner_knows_must_match(self):
        for name, code in (("eval_run_id", "EVAL_RUN_ID_MISMATCH"), ("model_tag", "MODEL_TAG_MISMATCH"),
                           ("reset_generation", "RESET_GENERATION_MISMATCH"), ("reservation_ref", "RESERVATION_REF_MISMATCH"),
                           ("engine_task_id", "ENGINE_TASK_ID_MISMATCH"), ("header_mode", "HEADER_MODE_MISMATCH"),
                           ("policy_fingerprint_sha256", "POLICY_FINGERPRINT_MISMATCH")):
            with self.subTest(name=name):
                self.rejected(good()[0] | {name: "other"}, code)

    def test_fields_are_closed(self):
        self.rejected(good()[0] | {"extra": 1}, "MANIFEST_FIELDS")
        missing = good()[0]
        del missing["rows"]
        self.rejected(missing, "MANIFEST_FIELDS")
        self.rejected(good()[0] | {"format": "other/v2"}, "MANIFEST_FORMAT")

    def test_duplicate_keys_and_non_objects_are_rejected(self):
        for data, code in ((b'{"format": "a", "format": "b"}', "DUPLICATE_KEY"), (b"[]", "JSON_INVALID"), (b"not json", "JSON_INVALID"),
                           (b'{"rows": NaN}', "JSON_INVALID"), (b"\xff", "JSON_INVALID")):
            with self.subTest(data=data), self.assertRaises(E2ExportError) as caught:
                parse_manifest(data, EXPECTED)
            self.assertEqual(str(caught.exception), f"e2_export: {code}")

    def test_run_identifiers_stay_on_their_own_axes(self):
        self.rejected(good()[0] | {"run_ref": "other"}, "RUN_REF_MISMATCH")
        self.rejected(good()[0] | {"scope": SCOPE | {"TaskRef": "18"}}, "TASK_REF_MISMATCH")
        # E② 실행 참조에 평가 회차 ID를 쓰지 않는다.
        same = good()[0] | {"run_ref": "eval-run-1", "scope": SCOPE | {"RunRef": "eval-run-1"},
                            "evaluation": {"run_ref": "eval-run-1", "llm_backend": "api", "target_kind": "public_practice"}}
        self.rejected(same, "RUN_REF_IS_EVAL_RUN")
        self.rejected(good()[0] | {"scope": {"TaskRef": "17"}}, "SCOPE_INVALID")
        self.rejected(good()[0] | {"scope": SCOPE | {"CaseRef": "http://x"}}, "SCOPE_INVALID")
        self.rejected(good()[0] | {"run_ref": 7}, "RUN_REF_MISMATCH")

    def test_passthrough_declaration_is_compared_field_by_field(self):
        evaluation = good()[0]["evaluation"]
        self.rejected(good()[0] | {"evaluation": None}, "EVALUATION_INVALID")
        self.rejected(good()[0] | {"evaluation": {}}, "EVALUATION_INVALID")
        self.rejected(good()[0] | {"evaluation": 7}, "EVALUATION_INVALID")
        self.rejected(good()[0] | {"evaluation": evaluation | {"run_ref": "other"}}, "EVALUATION_RUN_REF_MISMATCH")
        self.rejected(good()[0] | {"evaluation": evaluation | {"llm_backend": "local"}}, "EVALUATION_BACKEND_MISMATCH")
        self.rejected(good()[0] | {"evaluation": evaluation | {"target_kind": "customer"}}, "EVALUATION_TARGET_MISMATCH")
        missing = good()[0]
        del missing["evaluation"]
        self.rejected(missing, "EVALUATION_INVALID")

    def test_other_modes_have_no_declaration_and_an_empty_one_differs(self):
        plain = E2Expectation(**{**EXPECTED.__dict__, "header_mode": "preserve"})
        manifest = good()[0] | {"header_mode": "preserve", "evaluation": None}
        self.assertIsNone(parse_manifest(json.dumps(manifest).encode(), plain)["evaluation"])
        absent = dict(manifest)
        del absent["evaluation"]
        self.assertNotIn("evaluation", parse_manifest(json.dumps(absent).encode(), plain))
        self.rejected(manifest | {"evaluation": {}}, "EVALUATION_UNEXPECTED", plain)

    def test_expectation_itself_must_be_usable(self):
        # 러너 값이 비었으면 manifest의 빈 값과 일치해 버린다.
        for changes in ({"llm_backend": "other"}, {"header_mode": "other"}, {"eval_run_id": ""}, {"model_tag": ""}, {"reset_generation": ""},
                        {"reservation_ref": ""}, {"engine_task_id": ""}, {"policy_fingerprint_sha256": "short"}):
            with self.subTest(changes=changes):
                manifest = good()[0] | {key: value for key, value in changes.items() if key != "llm_backend"}
                self.rejected(manifest, "EXPECTATION_INVALID", E2Expectation(**{**EXPECTED.__dict__, **changes}))

    def test_file_names_cannot_leave_the_execution_directory(self):
        self.rejected(good()[0] | {"attempts_file": "../attempts.jsonl"}, "ATTEMPTS_FILE_INVALID")
        self.rejected(good()[0] | {"attempts_file": "other.jsonl"}, "ATTEMPTS_FILE_INVALID")
        for name in ("audit_journal_file", "audit_checkpoint_file"):
            for value in ("../x", "a/b", "/abs", "", ".hidden", 7):
                with self.subTest(name=name, value=value):
                    self.rejected(good()[0] | {name: value}, "FILE_NAME_INVALID")

    def test_other_shapes(self):
        for name, value, code in (("state", "partial", "STATE_INVALID"), ("attempts_sha256", "A" * 64, "DIGEST_INVALID"),
                                  ("audit_checkpoint_sha256", None, "DIGEST_INVALID"), ("rows", -1, "COUNT_INVALID"),
                                  ("rows", True, "COUNT_INVALID"), ("last_event_index", "6", "COUNT_INVALID"),
                                  ("last_attempt_sequence", 1 << 63, "COUNT_INVALID"), ("close_request_ref", "", "CLOSE_REQUEST_INVALID"),
                                  ("policy_revision", "", "POLICY_REVISION_INVALID")):
            with self.subTest(name=name, value=value):
                self.rejected(good()[0] | {name: value}, code)


class TestAttempts(unittest.TestCase):
    def parse(self, attempt_rows, /, **changes):
        data = encode(attempt_rows)
        counts = {"rows": len(attempt_rows), "last_index": attempt_rows[-1]["index"] if attempt_rows else 0,
                  "last_sequence": max((r["sequence"] for r in attempt_rows if "attempt_id" in r), default=0)}
        return parse_attempts(data, manifest_for(data, **(counts | changes)))

    def rejected(self, attempt_rows, code, /, **changes):
        with self.assertRaises(E2ExportError) as caught:
            self.parse(attempt_rows, **changes)
        self.assertEqual(str(caught.exception), f"e2_export: {code}")

    def test_rows_group_into_one_decision_per_attempt(self):
        result = self.parse(normal_rows())
        self.assertEqual(result.decisions, ({"attempt_id": A1, "decision": "allowed"}, {"attempt_id": A2, "decision": "denied"},
                                            {"attempt_id": A3, "decision": "unsupported"}))
        self.assertEqual((result.rows, result.attempts_without_result, result.allowed_not_sent, result.administrative_rows),
                         (6, 0, 0, 2))
        self.assertEqual(dict(result.denied_by_reason), {"policy_denied": 1, "unsupported": 1})
        self.assertEqual(dict(result.administrative_by_reason), {"complete": 2})

    def test_stage_rows_are_not_counted_as_requests(self):
        summary = self.parse(normal_rows()).summary()
        self.assertEqual((summary["attempts"], summary["rows"]), (3, 6))

    def test_output_feeds_the_attempt_reconcile(self):
        meter = [{"attempt_id_state": "valid", "attempt_id": A1, "response": "complete"}]
        result = reconcile(list(self.parse(normal_rows()).decisions), meter)
        self.assertEqual(result["cells"], {"allowed_observed": 1, "allowed_unobserved": 0, "denied_unobserved": 2, "denied_observed": 0,
                                           "unrecorded_observed": 0})
        self.assertFalse(result["control_claim_withheld"])

    def test_denied_attempt_seen_at_the_meter_is_a_bad_cell(self):
        meter = [{"attempt_id_state": "valid", "attempt_id": A1, "response": "complete"},
                 {"attempt_id_state": "valid", "attempt_id": A2, "response": "complete"}]
        result = reconcile(list(self.parse(normal_rows()).decisions), meter)
        self.assertEqual(result["blocked_reached"], 1)
        self.assertTrue(result["control_claim_withheld"])

    def test_intent_without_result_stays_allowed_and_only_an_incomplete_export_may_have_it(self):
        rows = [attempt(1, "intent", A1, 1, "allowed")]
        self.rejected(rows, "COMPLETE_WITH_OPEN_ATTEMPT")
        result = self.parse(rows, state="incomplete")
        self.assertEqual((result.decisions[0]["decision"], result.attempts_without_result), ("allowed", 1))

    def test_allowed_but_closed_before_sending_is_counted(self):
        result = self.parse([attempt(1, "intent", A1, 1, "allowed"), attempt(2, "result", A1, 1, "allowed", "closed")])
        self.assertEqual((result.decisions[0]["decision"], result.allowed_not_sent), ("allowed", 1))
        self.assertEqual(result.denied_by_reason, ())

    def test_intent_then_denied_result_uses_the_result(self):
        result = self.parse([attempt(1, "intent", A1, 1, "allowed"), attempt(2, "result", A1, 1, "denied", "audit_failed")])
        self.assertEqual(result.decisions[0]["decision"], "denied")
        self.assertEqual(dict(result.denied_by_reason), {"audit_failed": 1})

    def test_rows_without_an_attempt_are_counted_apart(self):
        result = self.parse([control(1, "closed"), control(2, "policy_denied"), closed(3)])
        self.assertEqual((result.decisions, result.administrative_rows), ((), 3))
        self.assertEqual(dict(result.administrative_by_reason), {"closed": 1, "complete": 1, "policy_denied": 1})

    def test_empty_export_is_valid_and_distinct_from_missing(self):
        result = self.parse([])
        self.assertEqual((result.rows, result.decisions), (0, ()))

    def test_incomplete_state_is_kept(self):
        data = encode(normal_rows())
        result = parse_attempts(data, manifest_for(data, 6, 6, 3, state="incomplete"))
        self.assertEqual((result.state, result.summary()["state"]), ("incomplete", "incomplete"))

    def test_bytes_must_match_what_the_manifest_froze(self):
        manifest, data = good()
        for changed in (data + b"\n", data[:-1], b""):
            with self.subTest(changed=len(changed)), self.assertRaises(E2ExportError) as caught:
                parse_attempts(changed, manifest)
            self.assertEqual(str(caught.exception), "e2_export: ATTEMPTS_DIGEST_MISMATCH")

    def test_truncated_tail_is_rejected_even_when_the_digest_matches(self):
        data = encode(normal_rows())[:-1]
        with self.assertRaises(E2ExportError) as caught:
            parse_attempts(data, manifest_for(data, 6, 6, 3))
        self.assertEqual(str(caught.exception), "e2_export: ATTEMPTS_TRUNCATED")

    def test_counts_and_order_must_match(self):
        self.rejected(normal_rows(), "ROW_COUNT_MISMATCH", rows=5)
        self.rejected(normal_rows(), "LAST_INDEX_MISMATCH", last_index=5)
        self.rejected(normal_rows(), "LAST_SEQUENCE_MISMATCH", last_sequence=2)
        gap = normal_rows()
        gap[2]["index"] = 9
        self.rejected(gap, "ROW_INDEX_GAP")
        self.rejected([control(2)], "ROW_INDEX_GAP")

    def test_last_sequence_is_the_highest_not_the_count(self):
        # 시도 순번은 행 수가 아니고, incomplete 저널에는 빈 번호가 있을 수 있다.
        self.assertEqual(self.parse([attempt(1, "result", A1, 5, "denied")], state="incomplete").rows, 1)

    def test_attempt_shape(self):
        intent = attempt(1, "intent", A1, 1, "allowed")
        self.rejected([intent, attempt(2, "intent", A1, 1, "allowed")], "ATTEMPT_ROWS_INVALID")
        self.rejected([attempt(1, "result", A1, 1, "denied"), attempt(2, "result", A1, 1, "denied")], "ATTEMPT_ROWS_INVALID")
        self.rejected([attempt(1, "result", A1, 1, "denied"), attempt(2, "intent", A1, 1, "allowed")], "ATTEMPT_ROWS_INVALID")
        self.rejected([intent, attempt(2, "result", A1, 2, "allowed")], "ATTEMPT_BINDING_MISMATCH")
        self.rejected([intent, attempt(2, "result", A1, 1, "allowed", call_ref="call_other")], "ATTEMPT_BINDING_MISMATCH")
        self.rejected([intent, attempt(2, "result", A1, 1, "allowed", intent_id=23)], "ATTEMPT_BINDING_MISMATCH")
        self.rejected([attempt(1, "result", A1, 1, "allowed")], "ATTEMPT_ALLOWED_WITHOUT_INTENT")
        self.rejected([attempt(1, "result", A1, 1, "denied"), attempt(2, "result", A2, 1, "denied")], "SEQUENCE_REPEATED")

    def test_each_row_is_closed_and_bound_to_the_execution(self):
        base = attempt(1, "result", A1, 1, "denied")
        for changes, code in (({"extra": 1}, "ROW_FIELDS"), ({"version": "e2-http-v2"}, "ROW_VERSION"), ({"stage": "other"}, "ROW_STAGE"),
                              ({"index": 0}, "ROW_COUNT_INVALID"), ({"index": -1}, "ROW_COUNT_INVALID"), ({"sequence": True}, "ROW_COUNT_INVALID"),
                              ({"intent_id": "22"}, "ROW_COUNT_INVALID"), ({"intent_id": 1 << 63}, "ROW_COUNT_INVALID"), ({"reason": "other"}, "ROW_REASON"),
                              ({"not_sent": 1}, "ROW_BITS"), ({"audit_complete": None}, "ROW_BITS"), ({"at": ""}, "ROW_TIME"), ({"at": 1}, "ROW_TIME"),
                              ({"at": "2026-13-07T00:00:00Z"}, "ROW_TIME"), ({"at": "2026-11-07 00:00:00Z"}, "ROW_TIME"),
                              ({"at": "0001-01-01T00:00:00Z"}, "ROW_TIME"), ({"method": 7}, "ROW_TEXT"), ({"decision": 7}, "ROW_TEXT"),
                              ({"attempt_id": None}, "ROW_TEXT"), ({"method": "get"}, "ROW_METHOD"), ({"method": "A" * 17}, "ROW_METHOD"),
                              ({"call_ref": "http://x"}, "ROW_REF"), ({"command_ref": "-x"}, "ROW_REF"), ({"rule_ref": "r" * 129}, "ROW_REF"),
                              ({"scope": SCOPE | {"RunRef": "other"}}, "ROW_SCOPE_MISMATCH"), ({"scope": {}}, "SCOPE_INVALID"),
                              ({"policy_revision": "other"}, "ROW_POLICY_MISMATCH")):
            with self.subTest(changes=changes):
                self.rejected([base | changes], code)
        broken = dict(base)
        del broken["reason"]
        self.rejected([broken], "ROW_FIELDS")

    def test_stage_rules_match_the_journal_validator(self):
        for row, code in ((attempt(1, "result", A1, 0, "denied"), "ROW_ATTEMPT_FIELDS"),
                          (attempt(1, "result", A1, 1, "denied", intent_id=0), "ROW_ATTEMPT_FIELDS"),
                          (attempt(1, "result", A1, 1, "denied", call_ref=""), "ROW_ATTEMPT_FIELDS"),
                          (attempt(1, "result", "short", 1, "denied"), "ROW_ATTEMPT_ID"),
                          (attempt(1, "result", "A" * 32, 1, "denied"), "ROW_ATTEMPT_ID"),
                          (attempt(1, "result", "", 1, "denied"), "ROW_ATTEMPT_ID"),
                          (attempt(1, "result", A1, 1, ""), "ROW_DECISION"), (attempt(1, "result", A1, 1, "other"), "ROW_DECISION"),
                          (attempt(1, "result", A1, 1, "allowed", rule_ref=""), "ROW_RULE_MISSING"),
                          (attempt(1, "result", A1, 1, "denied", not_sent=False), "ROW_DENIED_SENT"),
                          (attempt(1, "intent", A1, 1, "denied"), "ROW_INTENT_INVALID"),
                          (attempt(1, "intent", A1, 1, "allowed", forward_attempted=True, not_sent=False), "ROW_INTENT_INVALID"),
                          (control(1, attempt_id=A1), "ROW_ADMINISTRATIVE_FIELDS"), (control(1, sequence=1), "ROW_ADMINISTRATIVE_FIELDS"),
                          (control(1, decision="denied"), "ROW_ADMINISTRATIVE_FIELDS"), (control(1, intent_id=-1), "ROW_ADMINISTRATIVE_FIELDS"),
                          (closed(1, intent_id=0), "ROW_ADMINISTRATIVE_FIELDS"), (closed(1, call_ref=""), "ROW_ADMINISTRATIVE_FIELDS")):
            with self.subTest(row=(row["stage"], code)):
                self.rejected([row], code, state="incomplete")
        self.assertEqual(self.parse([control(1, intent_id=0)]).administrative_rows, 1)

    def test_observation_bits_match_the_journal_validator(self):
        allowed = attempt(2, "result", A1, 1, "allowed")
        intent = attempt(1, "intent", A1, 1, "allowed")
        for changes, code in (({"forward_attempted": True}, "ROW_BITS_INCONSISTENT"),
                              ({"request_written": True, "not_sent": False}, "ROW_BITS_INCONSISTENT"),
                              ({"response_received": True, "not_sent": False, "status_code": 200}, "ROW_BITS_INCONSISTENT"),
                              ({"forward_attempted": True, "not_sent": False, "response_complete": True}, "ROW_BITS_INCONSISTENT"),
                              ({"forward_attempted": True, "not_sent": False, "response_received": True}, "ROW_STATUS"),
                              ({"forward_attempted": True, "not_sent": False, "response_received": True, "status_code": 99}, "ROW_STATUS"),
                              ({"forward_attempted": True, "not_sent": False, "response_received": True, "status_code": 600}, "ROW_STATUS"),
                              ({"forward_attempted": True, "not_sent": False, "response_received": True, "status_code": "200"}, "ROW_STATUS"),
                              ({"status_code": 200}, "ROW_STATUS")):
            with self.subTest(changes=changes):
                self.rejected([intent, allowed | changes], code)
        # 결정 없는 행은 전달 시도를 말할 수 없다.
        self.rejected([control(1, forward_attempted=True, not_sent=False)], "ROW_BITS_INCONSISTENT")
        for status in (100, 599):
            sent = {"forward_attempted": True, "not_sent": False, "response_received": True, "status_code": status}
            self.assertEqual(self.parse([intent, allowed | sent]).rows, 2)

    def test_summary_has_no_references(self):
        summary = self.parse(normal_rows()).summary()
        text = json.dumps(summary)
        for value in (A1, CALL, "e2-run-1", "reservation-1", "revision-1", "rule-1"):
            self.assertNotIn(value, text)
        self.assertEqual(summary["decisions"], {"allowed": 1, "denied": 1, "unsupported": 1})

    def test_errors_and_tracebacks_do_not_carry_input_values(self):
        data = b'{"format": "' + SECRET_LIKE.encode() + b'", "x": \xff}'
        with self.assertRaises(E2ExportError) as caught:
            parse_manifest(data, EXPECTED)
        self.assertNotIn(SECRET_LIKE, "".join(traceback.format_exception(caught.exception)))
        self.assertIsNone(caught.exception.__cause__)
        self.assertTrue(caught.exception.__suppress_context__)


class TestParseExport(unittest.TestCase):
    def test_manifest_and_rows_together(self):
        manifest, data = good()
        result = parse_export(json.dumps(manifest).encode(), data, EXPECTED)
        self.assertEqual((result.state, len(result.decisions)), ("complete", 3))


class TestGoDefinitions(unittest.TestCase):
    def test_word_lists_match_the_go_source(self):
        types = (ROOT / "e2proxy/types.go").read_text(encoding="utf-8")
        self.assertEqual(set(re.findall(r'\bReason\s*=\s*"([a-z_]+)"', types)), set(E2_REASONS))
        self.assertEqual(set(re.findall(r'\bHeaderMode\s*=\s*"([a-z_]+)"', types)), set(E2_HEADER_MODES))
        audit = (ROOT / "e2proxy/audit.go").read_text(encoding="utf-8")
        self.assertEqual(set(re.findall(r'event\.Decision != "([a-z]+)"', audit)), set(E2_DECISIONS))

    def test_followed_go_blocks_are_unchanged(self):
        for path, shapes in GO_SHAPES.items():
            text = (ROOT / path).read_text(encoding="utf-8")
            for start, expected in shapes.items():
                with self.subTest(start=start):
                    self.assertEqual(hashlib.sha256(go_block(text, start).encode()).hexdigest(), expected)


if __name__ == "__main__":
    unittest.main()
