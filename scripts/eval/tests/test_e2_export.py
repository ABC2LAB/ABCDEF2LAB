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
from e2_export import PROGRAM_CALL_KINDS, E2_DECISIONS, E2_HEADER_MODES, E2_REASONS, E2Expectation, E2ExportError, parse_attempts, parse_export, parse_manifest  # noqa: E402

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
GO_SHAPES = {"e2proxy/audit.go": {"func validJournalEvent(": "003ec100acf7d859ee76ad421a19d2c8291304b1b46774e1f764a47864bb6dc0", "func validJournalRef(": "19e535e1f750fa71646b37c58e528f0a2409c5d182d45e2f57889e7f2e26fb77", "func validJournalTime(": "ba9dc33173dcd0c3498013c1ddb3a9dfa93539240a5371212e42ed7b104740a6"},
             "e2proxy/types.go": {"type Receipt struct": "a874afbd266b6e51e5162a0a48a8ad08cf49e32af805ef619615c26834db825d", "type Event struct": "b76400d2b19d095202ef7f2be6a32264645d6104f4f8aa274fe60701c23df7a2", 'func ValidCallAttribution(': '30a25655f3e9b634a1209da141699ae569b8f0863239e76227db428a8d2b5c0c', 'func (k ProgramCallKind) Valid()': 'cc402b4a31aea9a9e4caee05f65859590c0356a2e4e8d6122379e15f0cfa539d'}}


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
    return {"index": index, "stage": stage, "intent_id": 22, "version": "e2-http-v2", "scope": dict(SCOPE), "policy_revision": "revision-1",
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
        self.assertEqual(caught.exception.code, code)

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
        for changes in ({"llm_backend": "other"}, {"header_mode": "other"}, {"header_mode": "test_account_passthrough"}, {"eval_run_id": ""}, {"model_tag": ""}, {"reset_generation": ""},
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

    def test_program_attribution_is_explicit_and_bound(self):
        for kind in ("auth_prep", "refresh_session", "login_contrast"):
            rows = [attempt(1,"intent",A1,1,"allowed",intent_id=0,program=kind),
                    attempt(2,"result",A1,1,"allowed",intent_id=0,program=kind,**SENT),
                    closed(3,intent_id=0,program=kind)]
            self.assertEqual(len(self.parse(rows).decisions), 1)
            self.rejected([rows[0] | {"intent_id":1}], "ROW_ATTEMPT_FIELDS")
            self.rejected([rows[0] | {"program":"unknown"}], "ROW_ATTEMPT_FIELDS")
            self.rejected([rows[0] | {"version":"e2-http-v1"}], "ROW_ATTRIBUTION")
            self.rejected([control(1,intent_id=0,program=kind)], "ROW_ADMINISTRATIVE_FIELDS")
            self.rejected([rows[0],rows[1] | {"program":("refresh_session" if kind=="auth_prep" else "auth_prep")}], "ATTEMPT_BINDING_MISMATCH")

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

    def test_withheld_rows_follow_the_v2_shape(self):
        intent = attempt(1, "intent", A1, 1, "allowed", not_sent=False)
        withheld = SENT | {"response_complete": False, "reason": "withheld"}
        result = self.parse([intent, attempt(2, "result", A1, 1, "allowed", **withheld)])
        self.assertEqual([row["decision"] for row in result.decisions], ["allowed"])
        self.parse([intent, attempt(2, "result", A1, 1, "allowed", **(withheld | {"response_truncated": True}))])
        for changes, code in (({"response_complete": True}, "ROW_WITHHELD_INVALID"),
                              ({"response_received": False, "response_complete": False, "status_code": 0}, "ROW_WITHHELD_INVALID"),
                              ({"response_truncated": False}, "ROW_BITS"), ({"response_truncated": 1}, "ROW_BITS")):
            with self.subTest(changes=changes):
                self.rejected([intent, attempt(2, "result", A1, 1, "allowed", **(withheld | changes))], code)
        # 완전 응답에 자른 표시를 붙이거나 보류를 거부 결정에 붙이지 않는다.
        self.rejected([intent, attempt(2, "result", A1, 1, "allowed", **(SENT | {"response_truncated": True}))], "ROW_WITHHELD_INVALID")
        self.rejected([attempt(1, "result", A1, 1, "denied", reason="withheld")], "ROW_WITHHELD_INVALID")
        self.rejected([attempt(1, "intent", A1, 1, "allowed", not_sent=False, reason="withheld")], "ROW_WITHHELD_INVALID")
        # call_closed는 call의 마지막 사유(보류)를 요약으로 싣는다.
        closed = common(3, "call_closed", 0, "withheld") | {"call_ref": CALL}
        result = self.parse([intent, attempt(2, "result", A1, 1, "allowed", **withheld), closed])
        self.assertEqual(result.administrative_by_reason, (("withheld", 1),))

    def test_stored_v1_rows_still_parse_without_v2_fields(self):
        v1 = {"version": "e2-http-v1"}
        self.parse([attempt(1, "intent", A1, 1, "allowed", not_sent=False, **v1), attempt(2, "result", A1, 1, "allowed", **(SENT | v1))])
        withheld = SENT | v1 | {"response_complete": False, "reason": "withheld"}
        self.rejected([attempt(1, "intent", A1, 1, "allowed", not_sent=False, **v1), attempt(2, "result", A1, 1, "allowed", **withheld)], "ROW_REASON")
        self.rejected([attempt(1, "intent", A1, 1, "allowed", not_sent=False), attempt(2, "result", A1, 1, "allowed", **(SENT | v1))],
                      "ROW_VERSION_MIXED")

    def test_each_row_is_closed_and_bound_to_the_execution(self):
        base = attempt(1, "result", A1, 1, "denied")
        for changes, code in (({"extra": 1}, "ROW_FIELDS"), ({"version": "e2-http-v4"}, "ROW_VERSION"), ({"stage": "other"}, "ROW_STAGE"),
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
        self.assertEqual(set(re.findall(r'\bProgramCallKind\s*=\s*"([a-z_]+)"', types)), set(PROGRAM_CALL_KINDS))
        audit = (ROOT / "e2proxy/audit.go").read_text(encoding="utf-8")
        self.assertEqual(set(re.findall(r'event\.Decision != "([a-z]+)"', audit)), set(E2_DECISIONS))

    def test_followed_go_blocks_are_unchanged(self):
        for path, shapes in GO_SHAPES.items():
            text = (ROOT / path).read_text(encoding="utf-8")
            for start, expected in shapes.items():
                with self.subTest(start=start):
                    self.assertEqual(hashlib.sha256(go_block(text, start).encode()).hexdigest(), expected)



V3 = {"version": "e2-http-v3"}
E3_BIND = {"role": "owner", "account_label": "tester-a", "account_binding_ref": "binding-1",
           "e3_binding": {"job_ref": "job-1", "generation_ref": "gen-1", "instance_ref": "inst-1",
                          "policy": {"kind": "product_declaration", "ref": "declaration-job-1", "digest_sha256": "d" * 64,
                                     "target_approval_revision": 3}}}
DIGEST = {"normal": "yes", "body_digest_sha256": "b" * 64, "body_complete": True}
SUBJECT, OBJECT = "5" * 64, "6" * 64  # 합성 digest(e3contract digestPattern 모양)


def e3(index, stage, attempt_id, sequence, decision, program, reason="complete", **changes):
    """v3 E③ 프로그램 호출 시도 행(v3 계약 §2.2·§2.3)."""
    extra = dict(E3_BIND)
    if program != "e3_identity":
        extra["resource_type"] = "basket"
    if program == "e3_read":
        extra["create_ref"] = {"call_ref": CALL, "attempt_id": A1, "sequence": 1}
    return attempt(index, stage, attempt_id, sequence, decision, reason, intent_id=0, program=program, **V3) | extra | changes


class TestV3E3Rows(unittest.TestCase):
    """E③ 프로그램 호출 행(e2-http-v3). 평가 읽기는 송신 판정 칸과 결속 칸을 단계 규칙대로 보고, 프로그램 호출을 따로 센다."""

    parse = TestAttempts.parse
    rejected = TestAttempts.rejected

    def calls(self):
        identity = [e3(1, "intent", A1, 1, "allowed", "e3_identity"),
                    e3(2, "result", A1, 1, "allowed", "e3_identity", **SENT, **DIGEST, subject_digest=SUBJECT, request_marker_free=True)]
        refused = [e3(3, "result", A2, 2, "denied", "e3_create", "policy_denied", not_sent_reason="policy_denied")]
        read = [e3(4, "intent", A3, 3, "allowed", "e3_read"),
                e3(5, "result", A3, 3, "allowed", "e3_read", **SENT, **DIGEST, request_marker_free=True,
                   marker={"status": "complete", "presence": "absent", "method": "exact", "field": "f1"})]
        return identity, refused, read

    def test_e3_calls_are_read_and_counted_apart(self):
        identity, refused, read = self.calls()
        model = [attempt(6, "intent", "0" * 31 + "4", 4, "allowed", **V3), attempt(7, "result", "0" * 31 + "4", 4, "allowed", **V3, **SENT)]
        result = self.parse(identity + refused + read + model)
        self.assertEqual(len(result.decisions), 4)  # 시도 대조에는 모든 시도가 들어간다(평가 모드 E²는 프로그램 호출에도 시도 ID를 싣는다)
        self.assertEqual(dict(result.attempts_by_program), {"e3_identity": 1, "e3_create": 1, "e3_read": 1, "model": 1})
        self.assertEqual(result.e3_program_attempts, 3)
        self.assertEqual(result.summary()["attempts_by_program"], {"e3_create": 1, "e3_identity": 1, "e3_read": 1, "model": 1})
        # 끊긴 송신(결과 불명): 응답 칸 없이 timed_out
        cut = e3(2, "result", A1, 1, "allowed", "e3_identity", forward_attempted=True, request_written=True, not_sent=False, timed_out=True)
        self.assertEqual(self.parse([identity[0], cut]).e3_program_attempts, 1)
        # 생성은 객체 ID digest를 꺼냈을 때만 싣는다
        made = e3(2, "result", A1, 1, "allowed", "e3_create", **SENT, **DIGEST, object_id_digest=OBJECT)
        self.assertEqual(self.parse([e3(1, "intent", A1, 1, "allowed", "e3_create"), made]).e3_program_attempts, 1)
        # v2 회차에는 E③ 호출이 없다
        self.assertEqual(self.parse(normal_rows()).e3_program_attempts, 0)

    def test_attribution_and_fields_by_version(self):
        identity, refused, read = self.calls()
        self.rejected([identity[0] | {"version": "e2-http-v2"}], "ROW_FIELDS")  # v3 칸은 v3 행에만
        bare = {k: v for k, v in identity[0].items() if k not in E3_BIND} | {"version": "e2-http-v2"}
        self.rejected([bare], "ROW_ATTEMPT_FIELDS")  # v2에는 E③ 프로그램이 없다
        self.rejected([attempt(1, "intent", A1, 1, "allowed", **V3) | {"role": "owner"}], "ROW_E3_FIELDS")  # 모델 요청 행에 E③ 칸
        self.rejected([control(1, **V3) | {"role": "owner"}], "ROW_E3_FIELDS")
        self.rejected([identity[0] | {"intent_id": 3}], "ROW_ATTEMPT_FIELDS")  # program과 intent_id 둘 다
        for name in ("role", "account_label", "account_binding_ref", "e3_binding"):
            with self.subTest(missing=name):
                self.rejected([{k: v for k, v in identity[0].items() if k != name}], "ROW_E3_FIELDS")
        for changes in ({"role": "admin"}, {"account_label": ""}, {"account_binding_ref": "a b"}, {"e3_binding": {"job_ref": ""}},
                        {"e3_binding": "job-1"}):
            with self.subTest(changes=changes):
                self.rejected([identity[0] | changes], "ROW_E3_BINDING")
        self.rejected([identity[0] | {"resource_type": "basket"}], "ROW_E3_RESOURCE")
        for value in ("Basket", "a-b", "", [], 1):  # e3contract typePattern 밖
            with self.subTest(resource_type=value):
                self.rejected([refused[0] | {"index": 1, "resource_type": value}], "ROW_E3_RESOURCE")
        self.rejected([{k: v for k, v in refused[0].items() if k != "resource_type"}], "ROW_E3_RESOURCE")
        self.rejected([{k: v for k, v in read[0].items() if k != "create_ref"}], "ROW_E3_CREATE_REF")
        self.rejected([refused[0] | {"create_ref": read[0]["create_ref"]}], "ROW_E3_CREATE_REF")
        for bad in ({"call_ref": CALL, "attempt_id": A1}, {"call_ref": CALL, "attempt_id": "x", "sequence": 1},
                    {"call_ref": CALL, "attempt_id": A1, "sequence": 0}):
            with self.subTest(create=bad):
                self.rejected([read[0] | {"create_ref": bad}], "ROW_E3_CREATE_REF")

    def parse_like(self, changed, rows):
        """manifest 건수는 바꾸기 전 행으로 정한다(바꾼 칸이 건수 계산에 쓰이는 칸이어도 검사기가 고정 코드로 끝내는지 본다)."""
        data = encode(changed)
        counts = {"rows": len(rows), "last_index": rows[-1]["index"],
                  "last_sequence": max((r["sequence"] for r in rows if "attempt_id" in r), default=0)}
        return parse_attempts(data, manifest_for(data, **counts))

    def test_any_malformed_field_ends_with_a_fixed_code(self):
        # 칸마다 배열·객체·null·실수를 넣어도 TypeError 등이 아니라 고정 오류 코드로 끝난다(#758 리뷰: role·normal에 []).
        identity, refused, read = self.calls()
        made = [e3(1, "intent", A1, 1, "allowed", "e3_create"),
                e3(2, "result", A1, 1, "allowed", "e3_create", **SENT, **DIGEST, object_id_digest=OBJECT)]
        samples = [identity, [refused[0] | {"index": 1}], [read[0] | {"index": 1}, read[1] | {"index": 2}], made, normal_rows()]
        for rows in samples:
            for position, row in enumerate(rows):
                for key in sorted(row):
                    for bad in ([], {}, None, 1.5):
                        if row[key] == bad:
                            continue
                        changed = [dict(r) for r in rows]
                        changed[position][key] = bad
                        with self.subTest(key=key, bad=bad, position=position):
                            with self.assertRaises(E2ExportError):
                                self.parse_like(changed, rows)
                if "marker" in row:
                    for sub in sorted(row["marker"]):
                        for bad in ([], {}, None):
                            changed = [dict(r) for r in rows]
                            changed[position]["marker"] = dict(row["marker"], **{sub: bad})
                            with self.subTest(marker=sub, bad=bad), self.assertRaises(E2ExportError):
                                self.parse_like(changed, rows)

    def test_send_judgment_fields_follow_the_stage(self):
        identity, refused, read = self.calls()
        # 송신 전 거부: not_sent_reason 넷 중 하나, 허용이 아님
        self.rejected([{k: v for k, v in refused[0].items() if k != "not_sent_reason"}], "ROW_E3_NOT_SENT")
        self.rejected([refused[0] | {"not_sent_reason": "transport_failed"}], "ROW_E3_NOT_SENT")
        allowed_refusal = e3(1, "intent", A2, 2, "allowed", "e3_create")
        self.rejected([allowed_refusal, e3(2, "result", A2, 2, "allowed", "e3_create", not_sent_reason="policy_denied")],
                      "ROW_E3_NOT_SENT")
        # intent 뒤 송신 전 중단(계약 §2.3): 허용 result, reason=closed, not_sent, 사유·응답 칸 없음. v2처럼 허용·미송신으로 센다.
        stopped = e3(2, "result", A2, 2, "allowed", "e3_create", "closed")
        result = self.parse([allowed_refusal, stopped])
        self.assertEqual((result.allowed_not_sent, result.e3_program_attempts), (1, 1))
        self.rejected([allowed_refusal, stopped | {"reason": "complete"}], "ROW_E3_NOT_SENT")  # 중단 사유가 아니면 위반
        self.rejected([allowed_refusal, stopped | {"not_sent_reason": "policy_denied"}], "ROW_E3_NOT_SENT")
        self.rejected([allowed_refusal, stopped | {"normal": "yes"}], "ROW_E3_RESPONSE_FIELDS")
        self.rejected([stopped | {"index": 1}], "ATTEMPT_ALLOWED_WITHOUT_INTENT")  # intent 없는 중단은 앞 행이 빠진 것
        # 송신 전 거부는 intent 없이 result 하나다: intent(허용) 뒤 거부 result는 E③ 시도에서 위반(#758 리뷰)
        denied_after = e3(2, "result", A2, 2, "denied", "e3_create", "policy_denied", not_sent_reason="policy_denied")
        self.rejected([allowed_refusal, denied_after], "ATTEMPT_E3_DENIED_AFTER_INTENT")
        # v2 모델 요청은 intent 기록 뒤 거부가 남을 수 있어 그대로 받는다(공통 규칙은 바꾸지 않음)
        model_intent = attempt(1, "intent", A2, 2, "allowed")
        model_denied = attempt(2, "result", A2, 2, "denied", "audit_failed")
        self.assertEqual(self.parse([model_intent, model_denied]).denied_by_reason, (("audit_failed", 1),))
        for reason in ("policy_denied", "approval_denied", "approval_timeout", "confirmation_budget_exhausted"):
            with self.subTest(reason=reason):
                self.assertEqual(self.parse([refused[0] | {"index": 1, "not_sent_reason": reason}]).e3_program_attempts, 1)
        # 송신 전 허용(intent)·송신 뒤(result)에는 not_sent_reason이 없다
        self.rejected([identity[0] | {"not_sent_reason": "policy_denied"}], "ROW_E3_NOT_SENT")
        self.rejected([identity[0], identity[1] | {"not_sent_reason": "policy_denied"}], "ROW_E3_NOT_SENT")
        # timed_out은 보낸 뒤만, bool만
        self.rejected([refused[0] | {"timed_out": False}], "ROW_E3_TIMED_OUT")
        self.rejected([identity[0], identity[1] | {"timed_out": 1}], "ROW_E3_TIMED_OUT")
        # 응답 칸: 응답을 끝까지 받았을 때만, 목적별 필수 칸
        self.rejected([identity[0] | {"normal": "yes"}], "ROW_E3_RESPONSE_FIELDS")
        self.rejected([identity[0], {k: v for k, v in identity[1].items() if k != "subject_digest"}], "ROW_E3_RESPONSE_FIELDS")
        self.rejected([read[0], {k: v for k, v in read[1].items() if k != "marker"}], "ROW_E3_RESPONSE_FIELDS")
        self.rejected([identity[0], identity[1] | {"object_id_digest": OBJECT}], "ROW_E3_RESPONSE_FIELDS")
        for changes in ({"normal": "maybe"}, {"body_digest_sha256": "B" * 64}, {"body_complete": 1}, {"request_marker_free": "yes"},
                        {"subject_digest": ""}, {"subject_digest": "secret"}, {"subject_digest": "A" * 64}, {"subject_digest": []}):
            with self.subTest(changes=changes):
                self.rejected([identity[0], identity[1] | changes], "ROW_E3_RESPONSE")
        for marker in ({"status": "complete", "presence": "absent", "method": "exact"},
                       {"status": "done", "presence": "absent", "method": "exact", "field": "f"},
                       {"status": "complete", "presence": "maybe", "method": "exact", "field": "f"}):
            with self.subTest(marker=marker):
                self.rejected([read[0], read[1] | {"marker": marker}], "ROW_E3_RESPONSE")
        # 한 저널은 한 판으로만 쓴다(v2·v3 섞임)
        self.rejected([control(1), identity[0] | {"index": 2}], "ROW_VERSION_MIXED")

if __name__ == "__main__":
    unittest.main()
