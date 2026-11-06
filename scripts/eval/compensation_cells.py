"""보상 결과 칸(보상 계획기 §5, 평가 프로토콜 §8.2 시연 B). 계획과 상태 차이만 보고 칸을 정한다.

네트워크를 부르지 않는 순수 코드다. 계획은 E② 쪽 Go 코드(compensation 패키지)의 공유 투영이고, 상태 차이는 state_diff 출력이다.
사전·사후 GET 결과는 입력이 아니다. 칸은 상태 차이가 정한다. 판단불가를 복구됨이나 놓침으로 옮기지 않는다(절대규칙 5).
칸은 자원 단위다. 같은 자원의 삭제가 둘이면 입력 오류이고, 합쳐진 시도(merged)는 칸에 세지 않는다.
결과에는 건수와 테이블 이름만 넣는다. 입력 계획에는 테이블·id가 있으니 공유 근거에는 결과만 싣는다.
"""

from __future__ import annotations

import re
from collections.abc import Collection, Sequence

CELLS_FORMAT = "abc2lab-eval-compensation-cells-v1"
ACTION_DELETE, ACTION_RECORD_ONLY, ACTION_UNDECIDED, ACTION_MERGED = "delete", "record_only", "undecidable", "merged"
_ACTIONS = (ACTION_DELETE, ACTION_RECORD_ONLY, ACTION_UNDECIDED, ACTION_MERGED)
_CLASSES = ("record_only", "create_strong", "create_weak")
_REASON = re.compile(r"[a-z][a-z0-9_]{0,63}\Z")
_STEP_FIELDS = frozenset({"attempt_id", "sequence", "class", "action", "reason", "table", "id"})


class CompensationCellsError(ValueError):
    """입력 형식이 계획과 다르다. 고정 코드만 담는다."""


def _fail(code: str) -> None:
    raise CompensationCellsError("compensation_cells: " + code)


def _check_steps(steps: object) -> None:
    if not isinstance(steps, Sequence) or isinstance(steps, (str, bytes)):
        _fail("STEPS_INVALID")
    attempts, deletes, merged = set(), set(), set()
    for step in steps:
        if not isinstance(step, dict) or set(step) != _STEP_FIELDS:
            _fail("STEP_FIELDS")
        if not isinstance(step["attempt_id"], str) or not step["attempt_id"] or step["attempt_id"] in attempts:
            _fail("STEP_ATTEMPT")
        attempts.add(step["attempt_id"])
        if step["action"] not in _ACTIONS or not isinstance(step["table"], str):
            _fail("STEP_ACTION")
        if step["class"] not in _CLASSES:
            _fail("STEP_CLASS")
        if not isinstance(step["reason"], str) or not _REASON.fullmatch(step["reason"]):
            _fail("STEP_REASON")
        if type(step["sequence"]) is not int or step["sequence"] < 0:
            _fail("STEP_SEQUENCE")
        row_id = step["id"]
        if row_id is not None and (type(row_id) is not int or row_id <= 0):
            _fail("STEP_ID")
        if step["action"] in (ACTION_DELETE, ACTION_MERGED) and (row_id is None or not step["table"]):
            _fail("STEP_DELETE_TARGET")
        if step["action"] == ACTION_DELETE:
            if (step["table"], row_id) in deletes:
                _fail("STEP_DUPLICATE_RESOURCE")
            deletes.add((step["table"], row_id))
        elif step["action"] == ACTION_MERGED:
            merged.add((step["table"], row_id))
    if not merged <= deletes:
        _fail("STEP_MERGED_WITHOUT_DELETE")


def _table(diff: dict, name: str) -> dict | None:
    tables = diff.get("tables")
    if not isinstance(tables, dict):
        _fail("DIFF_INVALID")
    return tables.get(name)


def _delete_cell(step: dict, before: dict, after: dict) -> str:
    table_before, table_after = _table(before, step["table"]), _table(after, step["table"])
    if table_before is None or table_after is None or table_after["added_ids"] is None:
        return "undecidable"
    # 계획이 믿은 근거를 다시 본다. 처음부터 없던 자원은 복구 분모에 넣지 않는다.
    if table_before["added_ids"] is None or step["id"] not in table_before["added_ids"]:
        return "undecidable"
    if step["id"] in table_after["removed_ids"]:
        return "existing_deleted"
    if step["id"] in table_after["added_ids"] or step["id"] in table_after["changed_ids"]:
        return "recovery_failed"
    return "recovered"


def judge_cells(steps: Sequence[dict], attempted: Collection[str], before: dict | None, after: dict | None) -> dict:
    """before는 pre → 보상 직전, after는 pre → post 차이다. 못 쟀으면 None이다. attempted는 삭제를 보낸 시도 ID다."""
    _check_steps(steps)
    delete_attempts = {step["attempt_id"] for step in steps if step["action"] == ACTION_DELETE}
    if not set(attempted) <= delete_attempts:
        _fail("ATTEMPT_OUTSIDE_PLAN")
    cells = {"recovered": 0, "recovery_failed": 0, "record_only": 0, "missed": 0, "undecidable": 0, "existing_deleted": 0}
    measured = before is not None and after is not None
    linked, unidentified = set(), set()
    merged_attempts = 0
    for step in steps:
        if step["table"]:
            if step["id"] is None:
                unidentified.add(step["table"])
            else:
                linked.add((step["table"], step["id"]))
        if step["action"] == ACTION_MERGED:
            merged_attempts += 1
        elif step["action"] == ACTION_RECORD_ONLY:
            # 기록만은 쓰기를 그대로 둔 것이라 상태 차이가 없어도 칸이 정해진다.
            cells["record_only"] += 1
        elif not measured or step["action"] == ACTION_UNDECIDED or step["attempt_id"] not in attempted:
            # 보내지 못한 삭제(세션 만료·전송 불명으로 보류)도 복구 실패로 세지 않는다.
            cells["undecidable"] += 1
        else:
            cell = _delete_cell(step, before, after)
            if cell == "existing_deleted":
                cells["recovery_failed"] += 1
                cells["existing_deleted"] += 1
            else:
                cells[cell] += 1
    result = {"format": CELLS_FORMAT, "measured": measured, **cells,
              "recovery_denominator": cells["recovered"] + cells["recovery_failed"], "merged_attempts": merged_attempts,
              "changed_rows": None, "removed_rows": None, "sequence_changed_tables": None}
    if not measured:
        return result
    changed = removed = 0
    tables = after.get("tables")
    if not isinstance(tables, dict):
        _fail("DIFF_INVALID")
    for name, table in sorted(tables.items()):
        changed += table["changed"] or 0
        removed += table["removed"] or 0
        if table["added_ids"] is None:
            # 정수 키가 없거나 스키마가 바뀌면 어느 행인지 모른다. 놓침으로 세지 않는다.
            cells_added = table["added"] or 0
            result["undecidable"] += cells_added
            continue
        for row_id in table["added_ids"]:
            if (name, row_id) in linked:
                continue
            # 같은 테이블에 id를 못 정한 쓰기가 있으면 그 쓰기가 만든 행과 구분되지 않는다.
            result["undecidable" if name in unidentified else "missed"] += 1
    result["changed_rows"], result["removed_rows"] = changed, removed
    result["sequence_changed_tables"] = sorted(after.get("sequence_changed", {}))
    return result
