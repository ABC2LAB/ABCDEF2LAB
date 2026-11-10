"""서버 판정 페이지를 검증해 채점 입력으로 바꾼다(판정 페이지 제안 §3·§5의 평가 담당 결정).

네트워크를 부르지 않는 순수 코드다. 페이지 묶음이 회차·export·건수·행 대응까지 전부 맞을 때만 결과를 돌려주고,
하나라도 어긋나면 고정 코드로 거부한다. 부분 결과는 없다. 서버 API와 러너 연결은 아직 없고, 와이어 세부는
서버 구현 때 다시 맞춘다.

- 채점 입력은 run_id·model_tag·reset_generation·records 네 필드뿐이다. 재확인 결과와 변환 건수는 넣지 않고 summary로 낸다.
- 원본 페이지는 호출자가 서명 근거로 보존한다. 입력을 고치지 않는다.
- 원본 행을 먼저 검증한다. 재확인이 undecidable인 행은 그 뒤 채점 사본에서만 판단불가로 바꾼다. 깨진 행을 판단불가로 숨기지 않고,
  원값을 그대로 채점하는 경로도 없다.
- 행과 payload_sha256의 정본 대응을 다시 계산해 본다(finding_id만 제외). 서버의 저장 정본 검증을 대신하지는 않는다.
- GT 상관을 하지 않는다(절대규칙 7). 형식 검사에 넘기는 상관 함수는 항상 None이다.
"""

from __future__ import annotations

import copy
import re
import sys
from collections import Counter
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path

# -I 실행에서도 현재 디렉터리 대신 이 스크립트의 동반 모듈만 사용한다.
_HERE = str(Path(__file__).resolve().parent)
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from collect import ScanOutputError, parse_scan_payload  # noqa: E402
from e3_canonical import CanonicalError, canonical_sha256  # noqa: E402

SUMMARY_FORMAT = "abc2lab-eval-verdict-export-v1"
SCHEMA_VERSION = 1
PURPOSE = "initial"
COLLECTION_COMPLETE = "complete"
CHECK_VERIFIED, CHECK_UNDECIDABLE = "verified", "undecidable"
UNDECIDABLE = "undecidable"
# 계약 verdict-record §2(b)의 undecidable 쪽 값. 채점 사본이 스스로 어긋나지 않게 같이 바꾼다.
ACCESS_POLICY_UNDECIDABLE = "판정_불가"
_PAGE_FIELDS = frozenset({"schema_version", "task_id", "run_id", "model_tag", "reset_generation", "purpose", "export_ref",
                          "collection_status", "total", "limit", "next_cursor", "manual_confirmation_count", "records",
                          "record_checks"})
_CHECK_FIELDS = frozenset({"record_id", "issuance_ref", "payload_sha256", "kg_snapshot_ref", "status", "reason_codes"})
_SHA256 = re.compile(r"[0-9a-f]{64}\Z")
_REASON = re.compile(r"[a-z][a-z0-9_]{0,63}\Z")


class VerdictPagesError(ValueError):
    """입력 값은 담지 않고 고정 코드만 담는다. 레코드 값이 근거 파일 밖으로 나가지 않게 한다."""


@dataclass(frozen=True)
class ExportContext:
    """러너가 아는 회차 사실. 페이지가 말하는 값이 아니라 이 값과 대조한다."""
    task_id: int
    run_id: str
    model_tag: str
    reset_generation: str
    export_ref: str
    limit: int


@dataclass(frozen=True)
class VerdictExport:
    export_ref: str
    envelope: dict
    total: int
    page_count: int
    manual_confirmation_count: int
    verified: int
    converted: int
    converted_reasons: tuple[tuple[str, int], ...]

    def summary(self) -> dict:
        """서명 근거에 넣을 비민감 요약. 레코드는 없다. 수동 건수는 자동 판정 지표와 따로 둔다."""
        return {"format": SUMMARY_FORMAT, "export_ref": self.export_ref, "total": self.total, "page_count": self.page_count,
                "manual_confirmation_count": self.manual_confirmation_count, "verified": self.verified,
                "converted": self.converted, "converted_reasons": dict(self.converted_reasons)}


def _fail(code: str) -> None:
    # except 안에서도 불린다. 채점기 오류 문구에는 레코드 값이 들어 있어 표시 traceback에 이어지지 않게 끊는다.
    raise VerdictPagesError("verdict_pages: " + code) from None


def _count(value: object) -> bool:
    return type(value) is int and value >= 0


def _check_meta(page: dict, context: ExportContext) -> None:
    if set(page) != _PAGE_FIELDS:
        _fail("PAGE_FIELDS")
    if type(page["schema_version"]) is not int or page["schema_version"] != SCHEMA_VERSION:
        _fail("SCHEMA_VERSION")
    if type(page["task_id"]) is not int or page["task_id"] != context.task_id:
        _fail("TASK_MISMATCH")
    for name in ("run_id", "model_tag", "reset_generation", "export_ref"):
        if page[name] != getattr(context, name):
            _fail(name.upper() + "_MISMATCH")
    if page["purpose"] != PURPOSE:
        _fail("PURPOSE")
    if page["collection_status"] != COLLECTION_COMPLETE:
        _fail("COLLECTION_STATUS")
    if type(page["limit"]) is not int or page["limit"] != context.limit:
        _fail("LIMIT_MISMATCH")
    if not _count(page["total"]) or not _count(page["manual_confirmation_count"]):
        _fail("COUNT_INVALID")


def _check_pair(page: dict, limit: int) -> dict[int, dict]:
    """한 페이지의 레코드와 재확인 행이 한 행씩 대응하는지 보고 재확인 행을 id별로 돌려준다."""
    records, checks = page["records"], page["record_checks"]
    if not isinstance(records, list) or not isinstance(checks, list):
        _fail("ROWS_INVALID")
    if len(records) > limit:
        _fail("PAGE_OVER_LIMIT")
    by_id: dict[int, dict] = {}
    for check in checks:
        if not isinstance(check, dict) or set(check) != _CHECK_FIELDS:
            _fail("CHECK_FIELDS")
        record_id = check["record_id"]
        if type(record_id) is not int or record_id in by_id:
            _fail("CHECK_ID")
        if not isinstance(check["payload_sha256"], str) or not _SHA256.fullmatch(check["payload_sha256"]):
            _fail("CHECK_DIGEST")
        for name in ("issuance_ref", "kg_snapshot_ref"):
            if not isinstance(check[name], str) or not check[name]:
                _fail("CHECK_REF")
        reasons = check["reason_codes"]
        if not isinstance(reasons, list) or not all(isinstance(r, str) and _REASON.fullmatch(r) for r in reasons):
            _fail("CHECK_REASONS")
        status = check["status"]
        if status == CHECK_VERIFIED:
            if reasons:
                _fail("CHECK_REASONS")
        elif status == CHECK_UNDECIDABLE:
            if not reasons or len(set(reasons)) != len(reasons):
                _fail("CHECK_REASONS")
        else:
            _fail("CHECK_STATUS")
        by_id[record_id] = check
    ids = [record.get("id") if isinstance(record, dict) else None for record in records]
    if any(type(record_id) is not int or record_id <= 0 for record_id in ids):
        _fail("RECORD_ID")
    if len(set(ids)) != len(ids) or set(ids) != set(by_id):
        _fail("CHECK_RECORD_PAIRING")
    return by_id


def _check_digest(record: dict, check: dict) -> None:
    """변경 가능한 finding_id만 빼고 정본 해시를 맞춘다. 전달 중 바뀐 행을 정상 판정으로 받지 않는다."""
    try:
        digest = canonical_sha256({key: value for key, value in record.items() if key != "finding_id"})
    except CanonicalError:
        _fail("RECORD_NOT_CANONICAL")
    if digest != check["payload_sha256"]:
        _fail("CHECK_DIGEST_MISMATCH")


def _parse_records(records: list[dict], context: ExportContext) -> None:
    """채점기가 뒤에 거부할 행을 지금 거른다. 상관은 하지 않는다."""
    envelope = {"run_id": context.run_id, "model_tag": context.model_tag, "reset_generation": context.reset_generation,
                "records": records}
    try:
        parse_scan_payload(envelope, lambda _record: None, manifest=context)
    except ScanOutputError:
        _fail("RECORD_INVALID")


def _scoring_copy(record: dict, check: dict) -> dict:
    row = copy.deepcopy(record)
    if check["status"] == CHECK_UNDECIDABLE:
        row["oracle_value"], row["vuln_verdict"] = UNDECIDABLE, UNDECIDABLE
        row["access_policy_result"] = ACCESS_POLICY_UNDECIDABLE
    return row


def merge_verdict_pages(pages: Sequence[object], context: ExportContext) -> VerdictExport:
    """받은 순서대로 페이지를 검증해 하나로 합친다. 커서로 받아 온 순서 전체를 넘겨야 한다."""
    if not isinstance(pages, Sequence) or isinstance(pages, (str, bytes)) or not pages:
        _fail("NO_PAGES")
    first = pages[0]
    if not isinstance(first, dict):
        _fail("PAGE_INVALID")
    seen_ids: set[int] = set()
    seen_cursors: set[str] = set()
    originals: list[dict] = []
    checks_by_id: dict[int, dict] = {}
    reasons: Counter[str] = Counter()
    verified = converted = 0
    for index, page in enumerate(pages):
        if not isinstance(page, dict):
            _fail("PAGE_INVALID")
        _check_meta(page, context)
        if page["total"] != first["total"] or page["manual_confirmation_count"] != first["manual_confirmation_count"]:
            _fail("PAGE_COUNTS_CHANGED")
        last = index == len(pages) - 1
        cursor = page["next_cursor"]
        if last:
            if cursor is not None:
                _fail("NOT_LAST_PAGE")
        else:
            if not isinstance(cursor, str) or not cursor:
                _fail("EARLY_LAST_PAGE")
            if cursor in seen_cursors:
                _fail("CURSOR_CYCLE")
            seen_cursors.add(cursor)
            # 마지막이 아닌 빈 페이지는 커서 순환·누락과 구분되지 않는다.
            if not page["records"]:
                _fail("EMPTY_MIDDLE_PAGE")
        checks = _check_pair(page, context.limit)
        for record in page["records"]:
            record_id = record["id"]
            if record_id in seen_ids:
                _fail("DUPLICATE_RECORD")
            seen_ids.add(record_id)
            check = checks[record_id]
            _check_digest(record, check)
            originals.append(record)
            checks_by_id[record_id] = check
            if check["status"] == CHECK_UNDECIDABLE:
                converted += 1
                reasons.update(check["reason_codes"])
            else:
                verified += 1
    if len(originals) != first["total"]:
        _fail("TOTAL_MISMATCH")
    # 원본을 먼저 본다. 사본만 보면 깨진 행이 재확인 undecidable 뒤에 숨어 정상 판단불가로 지나간다.
    _parse_records(originals, context)
    rows = [_scoring_copy(record, checks_by_id[record["id"]]) for record in originals]
    _parse_records(rows, context)
    envelope = {"run_id": context.run_id, "model_tag": context.model_tag, "reset_generation": context.reset_generation,
                "records": rows}
    return VerdictExport(context.export_ref, envelope, len(rows), len(pages), first["manual_confirmation_count"], verified, converted,
                         tuple(sorted(reasons.items())))
