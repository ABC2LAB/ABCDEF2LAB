"""보상 결과 칸(보상 계획기 §5·§8 CP14~CP25, CP30). 합성 계획·합성 상태 차이만 쓴다."""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from compensation_cells import CompensationCellsError, judge_cells  # noqa: E402


def table(added=(), removed=(), changed=(), keyed=True):
    if not keyed:
        return {"added": len(added), "removed": len(removed), "changed": None, "added_ids": None, "removed_ids": None,
                "changed_ids": None}
    return {"added": len(added), "removed": len(removed), "changed": len(changed), "added_ids": list(added),
            "removed_ids": list(removed), "changed_ids": list(changed)}


def diff(sequence=None, **tables):
    return {"tables": tables, "sequence_changed": sequence or {}}


def step(attempt="a1", action="delete", table_name="Things", row_id=7):
    return {"attempt_id": attempt, "sequence": 1, "class": "create_strong", "action": action, "reason": "r", "table": table_name,
            "id": row_id}


class TestCells(unittest.TestCase):
    def cells(self, steps, attempted, before, after):
        result = judge_cells(steps, attempted, before, after)
        return {k: result[k] for k in ("recovered", "recovery_failed", "record_only", "missed", "undecidable", "existing_deleted")}

    def expect(self, steps, attempted, before, after, **expected):
        base = {"recovered": 0, "recovery_failed": 0, "record_only": 0, "missed": 0, "undecidable": 0, "existing_deleted": 0}
        self.assertEqual(self.cells(steps, attempted, before, after), base | expected)

    def test_cp14_deleted_and_gone_is_recovered_with_trace_reported(self):
        result = judge_cells([step()], ["a1"], diff(Things=table(added=[7])),
                             diff({"Things": {"pre": 5, "post": 6}}, Things=table()))
        self.assertEqual((result["recovered"], result["recovery_denominator"]), (1, 1))
        self.assertEqual(result["sequence_changed_tables"], ["Things"])

    def test_cp15_still_present_is_recovery_failed(self):
        self.expect([step()], ["a1"], diff(Things=table(added=[7])), diff(Things=table(added=[7])), recovery_failed=1)

    def test_changed_after_is_recovery_failed(self):
        self.expect([step()], ["a1"], diff(Things=table(added=[7])), diff(Things=table(changed=[7])), recovery_failed=1)

    def test_cp16_added_without_record_is_missed(self):
        self.expect([], [], diff(Things=table()), diff(Things=table(added=[9])), missed=1)

    def test_cp17_cp23_undecided_steps_stay_undecidable(self):
        self.expect([step(action="undecidable", row_id=None, table_name="")], [], diff(), diff(), undecidable=1)

    def test_cp18_unmeasured_run_is_all_undecidable(self):
        for before, after in ((None, None), (diff(), None), (None, diff())):
            with self.subTest(before=before, after=after):
                result = judge_cells([step(), step("a2", "undecidable")], [], before, after)
                self.assertEqual((result["undecidable"], result["measured"], result["missed"]), (2, False, 0))
                self.assertEqual((result["recovered"], result["changed_rows"], result["sequence_changed_tables"]), (0, None, None))

    def test_cp18_sent_delete_without_a_diff_is_still_undecidable(self):
        for before, after in ((None, None), (diff(Things=table(added=[7])), None), (None, diff(Things=table()))):
            with self.subTest(before=before, after=after):
                result = judge_cells([step()], ["a1"], before, after)
                self.assertEqual((result["undecidable"], result["recovered"], result["recovery_failed"]), (1, 0, 0))

    def test_cp20_not_in_before_stays_out_of_the_denominator(self):
        result = judge_cells([step()], ["a1"], diff(Things=table()), diff(Things=table()))
        self.assertEqual((result["undecidable"], result["recovered"], result["recovery_denominator"]), (1, 0, 0))

    def test_cp21_upsert_is_record_only(self):
        self.expect([step(action="record_only", row_id=9)], [], diff(Things=table(changed=[9])), diff(Things=table(changed=[9])),
                    record_only=1)

    def test_cp22_unkeyed_table_is_undecidable(self):
        self.expect([step()], ["a1"], diff(Things=table(keyed=False)), diff(Things=table(keyed=False)), undecidable=1)
        self.expect([step()], ["a1"], diff(Things=table(added=[7])), diff(Things=table(keyed=False)), undecidable=1)
        self.expect([], [], diff(Things=table()), diff(Things=table(added=[1, 2], keyed=False)), undecidable=2)

    def test_cp24_removed_existing_row_counts_in_both_rows(self):
        result = judge_cells([step()], ["a1"], diff(Things=table(added=[7])), diff(Things=table(removed=[7])))
        self.assertEqual((result["recovery_failed"], result["existing_deleted"], result["recovered"]), (1, 1, 0))
        self.assertEqual(result["recovery_denominator"], 1)

    def test_cp25_unidentified_write_turns_missed_into_undecidable(self):
        unidentified = step("a2", "record_only", row_id=None)
        self.expect([unidentified], [], diff(Things=table()), diff(Things=table(added=[9])), record_only=1, undecidable=1)
        # 다른 테이블의 놓침은 그대로 센다.
        self.expect([unidentified], [], diff(Things=table(), Others=table()), diff(Things=table(), Others=table(added=[3])),
                    record_only=1, missed=1)

    def test_linked_rows_are_not_missed(self):
        self.expect([step(action="record_only", row_id=9)], [], diff(Things=table()), diff(Things=table(added=[9])), record_only=1)

    def test_cp30_unsent_delete_is_undecidable_not_failed(self):
        self.expect([step()], [], diff(Things=table(added=[7])), diff(Things=table(added=[7])), undecidable=1)

    def test_other_changes_are_counts_only(self):
        result = judge_cells([], [], diff(Things=table()), diff(Things=table(removed=[1], changed=[2, 3]), Others=table(changed=[4])))
        self.assertEqual((result["changed_rows"], result["removed_rows"]), (3, 1))
        self.assertEqual((result["missed"], result["existing_deleted"]), (0, 0))

    def test_denominator_is_recovered_plus_failed_only(self):
        steps = [step("a1", row_id=1), step("a2", row_id=2), step("a3", row_id=3), step("a4", "record_only", row_id=4)]
        result = judge_cells(steps, ["a1", "a2"], diff(Things=table(added=[1, 2, 3])), diff(Things=table(added=[2, 3, 4])))
        self.assertEqual((result["recovered"], result["recovery_failed"], result["undecidable"], result["record_only"]), (1, 1, 1, 1))
        self.assertEqual(result["recovery_denominator"], 2)

    def test_result_has_counts_and_table_names_only(self):
        result = judge_cells([step()], ["a1"], diff(Things=table(added=[7])), diff(Things=table()))
        self.assertNotIn("7", [str(v) for v in result.values()])
        self.assertEqual(set(result), {"format", "measured", "recovered", "recovery_failed", "record_only", "missed", "undecidable",
                                       "existing_deleted", "recovery_denominator", "merged_attempts", "changed_rows", "removed_rows",
                                       "sequence_changed_tables"})

    def test_merged_attempt_is_not_a_second_resource(self):
        steps = [step("a2"), step("a1", "merged")]
        result = judge_cells(steps, ["a2"], diff(Things=table(added=[7])), diff(Things=table()))
        self.assertEqual((result["recovered"], result["recovery_denominator"], result["undecidable"], result["merged_attempts"]),
                         (1, 1, 0, 1))
        # 합쳐진 시도의 행은 연결된 행이다. 놓침으로 세지 않는다.
        kept = judge_cells(steps, ["a2"], diff(Things=table(added=[7])), diff(Things=table(added=[7])))
        self.assertEqual((kept["recovery_failed"], kept["missed"], kept["merged_attempts"]), (1, 0, 1))
        # 삭제를 보내지 못했을 때도 자원 하나다.
        unsent = judge_cells(steps, [], diff(Things=table(added=[7])), diff(Things=table(added=[7])))
        self.assertEqual((unsent["undecidable"], unsent["recovery_denominator"]), (1, 0))

    def test_merged_attempt_cannot_be_sent(self):
        with self.assertRaises(CompensationCellsError):
            judge_cells([step("a2"), step("a1", "merged")], ["a1"], diff(), diff())

    def test_malformed_plans_are_rejected(self):
        cases = [("x", "STEPS_INVALID"), ([1], "STEP_FIELDS"), ([step() | {"raw_target": "/a"}], "STEP_FIELDS"),
                 ([step(attempt="")], "STEP_ATTEMPT"), ([step(), step()], "STEP_ATTEMPT"),
                 ([step(action="other")], "STEP_ACTION"), ([step(row_id=0)], "STEP_ID"), ([step(row_id=True)], "STEP_ID"),
                 ([step(row_id=None)], "STEP_DELETE_TARGET"), ([step(table_name="")], "STEP_DELETE_TARGET"),
                 ([step("a1", "merged", row_id=None)], "STEP_DELETE_TARGET"),
                 ([step("a1"), step("a2")], "STEP_DUPLICATE_RESOURCE"),
                 ([step("a1", "merged")], "STEP_MERGED_WITHOUT_DELETE"),
                 ([step() | {"class": "none"}], "STEP_CLASS"), ([step() | {"class": None}], "STEP_CLASS"),
                 ([step() | {"reason": "Not Fixed"}], "STEP_REASON"), ([step() | {"reason": 1}], "STEP_REASON"),
                 ([step() | {"sequence": -1}], "STEP_SEQUENCE"), ([step() | {"sequence": True}], "STEP_SEQUENCE")]
        for steps, code in cases:
            with self.subTest(code=code, steps=steps):
                with self.assertRaises(CompensationCellsError) as caught:
                    judge_cells(steps, [], diff(), diff())
                self.assertEqual(str(caught.exception), f"compensation_cells: {code}")

    def test_attempt_outside_the_plan_is_rejected(self):
        for steps, attempted in (([], ["a1"]), ([step(action="record_only")], ["a1"])):
            with self.subTest(steps=steps), self.assertRaises(CompensationCellsError):
                judge_cells(steps, attempted, diff(), diff())


if __name__ == "__main__":
    unittest.main()
