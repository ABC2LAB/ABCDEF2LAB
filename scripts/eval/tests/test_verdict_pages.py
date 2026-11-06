"""판정 페이지 어댑터(판정 페이지 제안 VRP04·05·08). 합성 페이지만 쓰며 서버·DB·실제 판정 없음."""

from __future__ import annotations

import copy
import json
import re
import sys
import traceback
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from collect import parse_scan_payload  # noqa: E402
from e3_canonical import canonical_sha256  # noqa: E402
from verdict_pages import ExportContext, VerdictPagesError, merge_verdict_pages  # noqa: E402

CONTEXT = ExportContext(task_id=17, run_id="run-1", model_tag="model-1", reset_generation="reset-1",
                        export_ref="export-1", limit=2)
SECRET_LIKE = "synthetic-record-value-7f3a"
AUTO_DIGEST = "auto"
PROPOSAL = Path(__file__).resolve().parents[3] / "docs/contracts/verdict-record-page-api-proposal.md"


def record(record_id=101, value="confirmed", **changes):
    row = {"id": record_id, "finding_id": None, "case_ref": f"case-{record_id}", "axis": "horizontal",
           "oracle_value": value, "vuln_verdict": None if value == "allowed" else value,
           "access_policy_result": {"allowed": "위반_미관찰", "undecidable": "판정_불가"}.get(value, "위반_후보"),
           "policy_check": "yes", "identity_check": "yes", "data_check": "yes", "reason_codes": ["policy_deny_resolved"],
           "reset_generation": "reset-1", "model_tag": "model-1", "vuln_class": "idor",
           "resource_ref": f"thing:{record_id}", "traffic_evidence_refs": [201, 202]}
    return row | changes


def check(record_id=101, status="verified", reasons=()):
    return {"record_id": record_id, "issuance_ref": f"issuance-{record_id}", "payload_sha256": AUTO_DIGEST,
            "kg_snapshot_ref": "snapshot-1", "status": status, "reason_codes": list(reasons)}


def page(records=(), checks=None, next_cursor=None, total=None, **changes):
    records = list(records)
    checks = [check(row["id"]) for row in records] if checks is None else [dict(item) for item in checks]
    # 시험은 정상 해시에서 출발한다. 값을 직접 준 경우에만 그대로 둔다.
    by_id = {row.get("id"): row for row in records if isinstance(row, dict)}
    for item in checks:
        if item.get("payload_sha256") == AUTO_DIGEST:
            source = by_id.get(item["record_id"])
            item["payload_sha256"] = "a" * 64 if source is None else _digest(source)
    base = {"schema_version": 1, "task_id": 17, "run_id": "run-1", "model_tag": "model-1", "reset_generation": "reset-1",
            "purpose": "initial", "export_ref": "export-1", "collection_status": "complete",
            "total": len(records) if total is None else total, "limit": 2, "next_cursor": next_cursor,
            "manual_confirmation_count": 0, "records": records, "record_checks": checks}
    return base | changes


def _digest(row):
    # 합성 입력에는 정본으로 표현 못 하는 값도 일부러 넣는다. 그런 행은 어댑터가 거부하는지 본다.
    try:
        return canonical_sha256({key: value for key, value in row.items() if key != "finding_id"})
    except ValueError:
        return "a" * 64


def two_pages():
    return [page([record(101), record(102, "suspected")], next_cursor="c1", total=3), page([record(103, "allowed")], total=3)]


class TestMerge(unittest.TestCase):
    def rejected(self, pages, code):
        with self.assertRaises(VerdictPagesError) as caught:
            merge_verdict_pages(pages, CONTEXT)
        self.assertEqual(str(caught.exception), f"verdict_pages: {code}")

    def test_two_pages_merge_into_the_closed_envelope(self):
        result = merge_verdict_pages(two_pages(), CONTEXT)
        self.assertEqual(set(result.envelope), {"run_id", "model_tag", "reset_generation", "records"})
        self.assertEqual([row["id"] for row in result.envelope["records"]], [101, 102, 103])
        self.assertEqual((result.total, result.page_count, result.verified, result.converted), (3, 2, 3, 0))

    def test_output_passes_the_scorer_parser_and_keeps_allowed_null(self):
        result = merge_verdict_pages(two_pages(), CONTEXT)
        parsed = parse_scan_payload(result.envelope, lambda _: None, manifest=CONTEXT)
        self.assertEqual(parsed.verdicts, {})
        allowed = result.envelope["records"][2]
        self.assertEqual((allowed["oracle_value"], allowed["vuln_verdict"]), ("allowed", None))

    def test_zero_records_is_a_complete_empty_export(self):
        result = merge_verdict_pages([page()], CONTEXT)
        self.assertEqual((result.envelope["records"], result.total, result.page_count), ([], 0, 1))

    def test_last_page_may_be_empty_when_the_previous_page_was_full(self):
        pages = [page([record(101), record(102)], next_cursor="c1", total=2), page([], total=2)]
        self.assertEqual(merge_verdict_pages(pages, CONTEXT).total, 2)

    def test_undecidable_check_changes_only_the_scoring_copy(self):
        # 평가 담당 결정: 채점 사본만 판단불가로 바꾸고 원값은 원본 페이지에 남는다. 원값이 무엇이든 그대로 채점하지 않는다.
        for value in ("confirmed", "suspected", "allowed", "undecidable"):
            with self.subTest(value=value):
                source = [page([record(101, value)], [check(101, "undecidable", ["kg_snapshot_unavailable"])])]
                before = copy.deepcopy(source)
                result = merge_verdict_pages(source, CONTEXT)
                row = result.envelope["records"][0]
                self.assertEqual((row["oracle_value"], row["vuln_verdict"], row["access_policy_result"]),
                                 ("undecidable", "undecidable", "판정_불가"))
                self.assertEqual(source, before)
                self.assertEqual((result.converted, result.verified), (1, 0))
                self.assertEqual(result.converted_reasons, (("kg_snapshot_unavailable", 1),))

    def test_verified_rows_are_not_changed(self):
        source = two_pages()
        result = merge_verdict_pages(source, CONTEXT)
        self.assertEqual(result.envelope["records"], source[0]["records"] + source[1]["records"])
        self.assertIsNot(result.envelope["records"][0], source[0]["records"][0])

    def test_summary_has_counts_only(self):
        source = [page([record(101)], [check(101, "undecidable", ["b_reason", "a_reason"])], manual_confirmation_count=4)]
        summary = merge_verdict_pages(source, CONTEXT).summary()
        self.assertEqual(summary, {"format": "abc2lab-eval-verdict-export-v1", "export_ref": "export-1", "total": 1, "page_count": 1,
                                   "manual_confirmation_count": 4, "verified": 0, "converted": 1,
                                   "converted_reasons": {"a_reason": 1, "b_reason": 1}})
        self.assertNotIn("records", summary)

    def test_summary_reports_the_export_it_verified(self):
        other = ExportContext(task_id=17, run_id="run-1", model_tag="model-1", reset_generation="reset-1",
                              export_ref="export-2", limit=2)
        result = merge_verdict_pages([page([record(101)], export_ref="export-2")], other)
        self.assertEqual((result.export_ref, result.summary()["export_ref"]), ("export-2", "export-2"))

    def test_proposal_example_page_passes_with_its_real_digest(self):
        # 제안 문서 §4의 예시 해시는 실제 정본 해시다. 문서와 검증기가 어긋나면 여기서 잡힌다.
        text = PROPOSAL.read_text(encoding="utf-8")
        example = next(json.loads(block) for block in re.findall(r"```json\n(.*?)\n```", text, re.S)
                       if '"records": [\n    {' in block)
        context = ExportContext(task_id=example["task_id"], run_id=example["run_id"], model_tag=example["model_tag"],
                                reset_generation=example["reset_generation"], export_ref=example["export_ref"],
                                limit=example["limit"])
        self.assertEqual(merge_verdict_pages([example], context).verified, 1)

    def test_changed_row_fails_the_digest_for_either_check_status(self):
        for status, reasons in (("verified", ()), ("undecidable", ("kg_snapshot_unavailable",))):
            with self.subTest(status=status):
                pinned = check(101, status, reasons) | {"payload_sha256": _digest(record(101))}
                self.assertEqual(merge_verdict_pages([page([record(101)], [pinned])], CONTEXT).total, 1)
                self.rejected([page([record(101, case_ref="other-case")], [pinned])], "CHECK_DIGEST_MISMATCH")

    def test_only_finding_id_may_change_without_the_digest(self):
        pinned = check(101) | {"payload_sha256": _digest(record(101))}
        self.assertEqual(merge_verdict_pages([page([record(101, finding_id=55)], [pinned])], CONTEXT).total, 1)

    def test_row_that_cannot_be_canonical_is_rejected(self):
        self.rejected([page([record(101, score=0.5)])], "RECORD_NOT_CANONICAL")

    def test_broken_original_does_not_hide_behind_an_undecidable_check(self):
        # 재확인이 undecidable이어도 원본이 깨졌으면 정상 KG 부재가 아니다.
        undecidable = ["kg_snapshot_unavailable"]
        for label, broken in (("모르는 값", record(101, oracle_value="not-a-verdict")),
                              ("판정 불일치", record(101, vuln_verdict="suspected")),
                              ("allowed인데 값 있음", record(101, "allowed", vuln_verdict="allowed")),
                              ("다른 reset", record(101, reset_generation="reset-2")),
                              ("계약 밖 필드", record(101, challenge="x"))):
            with self.subTest(label):
                self.rejected([page([broken], [check(101, "undecidable", undecidable)])], "RECORD_INVALID")

    def test_manual_count_stays_out_of_the_records(self):
        result = merge_verdict_pages([page([record(101)], manual_confirmation_count=9)], CONTEXT)
        self.assertEqual((result.total, result.manual_confirmation_count, len(result.envelope["records"])), (1, 9, 1))

    def test_each_page_field_must_match_the_run(self):
        cases = {"schema_version": (2, "SCHEMA_VERSION"), "task_id": (18, "TASK_MISMATCH"), "run_id": ("run-2", "RUN_ID_MISMATCH"),
                 "model_tag": ("model-2", "MODEL_TAG_MISMATCH"), "reset_generation": ("reset-2", "RESET_GENERATION_MISMATCH"),
                 "export_ref": ("export-2", "EXPORT_REF_MISMATCH"), "purpose": ("retest", "PURPOSE"),
                 "collection_status": ("partial", "COLLECTION_STATUS"), "limit": (3, "LIMIT_MISMATCH"),
                 "total": (-1, "COUNT_INVALID"), "manual_confirmation_count": (-1, "COUNT_INVALID")}
        for name, (value, code) in cases.items():
            for position in (0, 1):
                with self.subTest(name=name, position=position):
                    pages = two_pages()
                    pages[position][name] = value
                    self.rejected(pages, code)

    def test_boolean_and_string_numbers_do_not_pass_as_numbers(self):
        for name, value, code in (("schema_version", True, "SCHEMA_VERSION"), ("task_id", "17", "TASK_MISMATCH"),
                                  ("limit", 2.0, "LIMIT_MISMATCH"), ("total", True, "COUNT_INVALID"),
                                  ("manual_confirmation_count", "0", "COUNT_INVALID")):
            with self.subTest(name=name):
                self.rejected([page([record(101)]) | {name: value}], code)

    def test_page_fields_are_closed(self):
        extra = page([record(101)]) | {"challenge": "x"}
        self.rejected([extra], "PAGE_FIELDS")
        missing = page([record(101)])
        del missing["record_checks"]
        self.rejected([missing], "PAGE_FIELDS")

    def test_total_and_manual_count_cannot_change_between_pages(self):
        pages = two_pages()
        pages[1]["total"] = 4
        self.rejected(pages, "PAGE_COUNTS_CHANGED")
        pages = two_pages()
        pages[1]["manual_confirmation_count"] = 1
        self.rejected(pages, "PAGE_COUNTS_CHANGED")

    def test_cursor_and_page_shape_failures(self):
        early = two_pages()
        early[0]["next_cursor"] = None
        self.rejected(early, "EARLY_LAST_PAGE")
        open_end = two_pages()
        open_end[1]["next_cursor"] = "c2"
        self.rejected(open_end, "NOT_LAST_PAGE")
        cycle = [page([record(101)], next_cursor="c1", total=3), page([record(102)], next_cursor="c1", total=3),
                 page([record(103)], total=3)]
        self.rejected(cycle, "CURSOR_CYCLE")
        hollow = [page([], next_cursor="c1", total=1), page([record(101)], total=1)]
        self.rejected(hollow, "EMPTY_MIDDLE_PAGE")
        self.rejected([page([record(101), record(102), record(103)], total=3)], "PAGE_OVER_LIMIT")

    def test_counts_must_add_up_without_duplicates(self):
        self.rejected([page([record(101)], total=2)], "TOTAL_MISMATCH")
        self.rejected([page([record(101), record(102)], total=1)], "TOTAL_MISMATCH")
        repeated = [page([record(101)], next_cursor="c1", total=2), page([record(101)], total=2)]
        self.rejected(repeated, "DUPLICATE_RECORD")

    def test_every_record_needs_exactly_one_check(self):
        self.rejected([page([record(101)], [])], "CHECK_RECORD_PAIRING")
        self.rejected([page([record(101)], [check(102)])], "CHECK_RECORD_PAIRING")
        self.rejected([page([], [check(101)])], "CHECK_RECORD_PAIRING")
        self.rejected([page([record(101)], [check(101), check(101)])], "CHECK_ID")
        self.rejected([page([record(101), record(101)], [check(101)], total=2)], "CHECK_RECORD_PAIRING")

    def test_check_rows_are_closed_and_checked(self):
        def changed(**fields):
            return [page([record(101)], [check(101) | fields])]
        self.rejected(changed(status="unknown"), "CHECK_STATUS")
        self.rejected(changed(status=None), "CHECK_STATUS")
        self.rejected(changed(extra=1), "CHECK_FIELDS")
        self.rejected(changed(payload_sha256="A" * 64), "CHECK_DIGEST")
        self.rejected(changed(payload_sha256=None), "CHECK_DIGEST")
        self.rejected(changed(payload_sha256="b" * 64), "CHECK_DIGEST_MISMATCH")
        self.rejected(changed(issuance_ref=""), "CHECK_REF")
        self.rejected(changed(kg_snapshot_ref=None), "CHECK_REF")
        self.rejected(changed(record_id=True), "CHECK_ID")
        self.rejected(changed(reason_codes="x"), "CHECK_REASONS")
        self.rejected(changed(reason_codes=["not fixed code"]), "CHECK_REASONS")
        self.rejected(changed(reason_codes=["stale"]), "CHECK_REASONS")
        self.rejected(changed(status="undecidable"), "CHECK_REASONS")
        self.rejected(changed(status="undecidable", reason_codes=["a", "a"]), "CHECK_REASONS")

    def test_record_ids_must_be_positive_integers(self):
        for value in (0, -1, True, "101", None):
            with self.subTest(value=value):
                self.rejected([page([record(101, id=value)], [check(101)])], "RECORD_ID")

    def test_rows_the_scorer_would_reject_fail_here(self):
        for label, changes in (("other reset", {"reset_generation": "reset-2"}), ("other model", {"model_tag": "model-2"}),
                               ("unknown field", {"challenge": "x"}), ("token value", {"token": SECRET_LIKE}),
                               ("mismatch", {"vuln_verdict": "suspected"}), ("bad value", {"oracle_value": "fixed"}),
                               ("no class", {"vuln_class": ""})):
            with self.subTest(label):
                self.rejected([page([record(101, **changes)])], "RECORD_INVALID")

    def test_traceback_does_not_show_record_values(self):
        with self.assertRaises(VerdictPagesError) as caught:
            merge_verdict_pages([page([record(101, token=SECRET_LIKE)])], CONTEXT)
        self.assertNotIn(SECRET_LIKE, "".join(traceback.format_exception(caught.exception)))

    def test_errors_never_carry_input_values(self):
        for pages in ([page([record(101, token=SECRET_LIKE)])], [page([record(101)]) | {"run_id": SECRET_LIKE}]):
            with self.assertRaises(VerdictPagesError) as caught:
                merge_verdict_pages(pages, CONTEXT)
            self.assertNotIn(SECRET_LIKE, str(caught.exception))
            self.assertIsNone(caught.exception.__cause__)

    def test_input_must_be_a_list_of_objects(self):
        for pages in ([], (), "pages", b"x", None, 3):
            with self.subTest(pages=pages):
                self.rejected(pages, "NO_PAGES")
        self.rejected([1], "PAGE_INVALID")
        self.rejected([page([record(101)], next_cursor="c1", total=2), 1], "PAGE_INVALID")

    def test_output_is_json_serializable(self):
        result = merge_verdict_pages(two_pages(), CONTEXT)
        self.assertEqual(json.loads(json.dumps(result.envelope, ensure_ascii=False)), result.envelope)


if __name__ == "__main__":
    unittest.main()
