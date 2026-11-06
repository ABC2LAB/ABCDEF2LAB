"""판정 export를 엔진 API에서 받아 온다(판정 페이지 제안 §2·§3·§6). 러너 연결과 서버 API는 아직 없다.

명시 동결 호출로 export_ref를 받고, 그 값으로 모든 페이지를 받은 뒤 verdict_pages로 검증한다. 하나라도 어긋나면 고정 코드로
실패하고 부분 결과를 돌려주지 않는다. 호출자는 실패를 records=null(판정 미수집)로 기록한다. 0건 완료와 미수집을 섞지 않는다.

제안 문서의 경로·쿼리를 따른다. 와이어 세부는 서버 구현 때 다시 맞춘다. 동결 요청 본문은 제안에 없어서 서버와 정해야 한다.
"""

from __future__ import annotations

import http.client
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from urllib.parse import urlencode

# -I 실행에서도 현재 디렉터리 대신 이 스크립트의 동반 모듈만 사용한다.
_HERE = str(Path(__file__).resolve().parent)
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from runner_engine import AdminSecret, _get, _post, login  # noqa: E402
from verdict_pages import PURPOSE, ExportContext, VerdictExport, VerdictPagesError, merge_verdict_pages  # noqa: E402

EXPORTS_PATH = "/api/tasks/{task_id}/verdict-exports"
RECORDS_PATH = "/api/tasks/{task_id}/verdict-records"
MAX_EXPORT_RESPONSE_BYTES = 64 * 1024
_EXPORT_REF = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}\Z")
# 제안 §6의 상태 코드. 응답 본문은 믿지 않고 읽지도 않는다.
HTTP_INTEGRITY_FAILED = 422
_READ_FAILURES = {400: "invalid_export_request", 401: "unauthorized", 403: "unauthorized", 404: "export_not_found",
                  409: "export_snapshot_unavailable", 503: "export_unavailable"}


class VerdictCollectError(ValueError):
    """고정 코드만 담는다. 응답 원문·export_ref·cursor는 담지 않는다."""


class VerdictExportNotReady(VerdictCollectError):
    """서버가 아직 동결하지 못했다(writer 미종료·미지원). 판정 미수집이고 0건 완료가 아니다."""


@dataclass(frozen=True)
class CollectLimits:
    """판정 수집 전용 한도. findings 한도와 따로 둔다."""
    page_size: int
    timeout_seconds: float
    max_page_bytes: int
    max_total_bytes: int
    max_pages: int


@dataclass(frozen=True)
class CollectedExport:
    """pages는 서명 근거로 원본 그대로 보존한다. 채점 입력은 export.envelope다."""
    export_ref: str
    pages: tuple[dict, ...]
    export: VerdictExport


class VerdictIntegrityError(VerdictCollectError):
    """서버가 무결성 실패를 답했거나 응답이 계약과 어긋난다. 미수집이 아니므로 채점을 멈춘다(제안 §3·§6)."""


def _integrity(code: str) -> None:
    raise VerdictIntegrityError("verdict_collect: " + code) from None


def _fail(code: str) -> None:
    # except 안에서도 불린다. 전송 예외의 원문(status line 등)이 표시 traceback에 이어지지 않게 끊는다.
    raise VerdictCollectError("verdict_collect: " + code) from None


def _unique_object(pairs: list[tuple[str, object]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            _integrity("DUPLICATE_KEY")
        result[key] = value
    return result


def _reject_constant(_value: str) -> None:
    _integrity("RESPONSE_INVALID")


def _json_object(data: bytes) -> dict:
    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=_unique_object, parse_constant=_reject_constant)
    except (UnicodeError, ValueError, RecursionError) as exc:
        if isinstance(exc, VerdictCollectError):
            raise
        _integrity("RESPONSE_INVALID")
    if not isinstance(value, dict):
        _integrity("RESPONSE_INVALID")
    return value


def _freeze(port: int, task_id: int, run_id: str, headers: dict, limits: CollectLimits) -> str:
    body = json.dumps({"run_id": run_id, "purpose": PURPOSE}).encode("utf-8")
    try:
        status, data = _post(port, EXPORTS_PATH.format(task_id=task_id), body, headers, limits.timeout_seconds,
                             MAX_EXPORT_RESPONSE_BYTES)
    except (OSError, http.client.HTTPException, ValueError, OverflowError):
        _fail("FREEZE_UNREACHABLE")
    if status == 409:
        raise VerdictExportNotReady("verdict_collect: EXPORT_NOT_READY")
    if status == HTTP_INTEGRITY_FAILED:
        _integrity("FREEZE_INTEGRITY_FAILED")
    if status not in (200, 201):
        _fail("FREEZE_" + _READ_FAILURES.get(status, f"http_{status}").upper())
    if len(data) > MAX_EXPORT_RESPONSE_BYTES:
        _fail("RESPONSE_TOO_LARGE")
    export_ref = _json_object(data).get("export_ref")
    if not isinstance(export_ref, str) or not _EXPORT_REF.fullmatch(export_ref):
        _integrity("EXPORT_REF_INVALID")
    return export_ref


def _fetch_pages(port: int, context: ExportContext, headers: dict, limits: CollectLimits) -> tuple[dict, ...]:
    path = RECORDS_PATH.format(task_id=context.task_id)
    query = {"run_id": context.run_id, "purpose": PURPOSE, "export_ref": context.export_ref, "limit": context.limit}
    pages: list[dict] = []
    seen_cursors: set[str] = set()
    total_bytes = 0
    cursor = None
    while True:
        if len(pages) >= limits.max_pages:
            _fail("TOO_MANY_PAGES")
        target = path + "?" + urlencode(query if cursor is None else {**query, "cursor": cursor})
        try:
            status, data = _get(port, target, headers, limits.timeout_seconds, limits.max_page_bytes)
        except (OSError, http.client.HTTPException, ValueError, OverflowError):
            _fail("PAGE_UNREACHABLE")
        if status == HTTP_INTEGRITY_FAILED:
            _integrity("PAGE_INTEGRITY_FAILED")
        if status != 200:
            _fail("PAGE_" + _READ_FAILURES.get(status, f"http_{status}").upper())
        if len(data) > limits.max_page_bytes:
            _fail("PAGE_TOO_LARGE")
        total_bytes += len(data)
        if total_bytes > limits.max_total_bytes:
            _fail("TOTAL_TOO_LARGE")
        page = _json_object(data)
        pages.append(page)
        cursor = page.get("next_cursor")
        if cursor is None:
            return tuple(pages)
        if not isinstance(cursor, str) or not cursor:
            _integrity("CURSOR_INVALID")
        # 순환하면 상한까지 헛돌지 않고 바로 멈춘다.
        if cursor in seen_cursors:
            _integrity("CURSOR_CYCLE")
        seen_cursors.add(cursor)


def collect_verdict_export(port: int, secret: AdminSecret, *, task_id: int, run_id: str, model_tag: str, reset_generation: str,
                           limits: CollectLimits) -> CollectedExport:
    """회차 값은 러너가 아는 값이다. 페이지가 말하는 값으로 채우지 않고 대조한다."""
    token = login(port, secret, limits.timeout_seconds)
    headers = {"Authorization": f"Bearer {token}"}
    export_ref = _freeze(port, task_id, run_id, headers, limits)
    context = ExportContext(task_id=task_id, run_id=run_id, model_tag=model_tag, reset_generation=reset_generation,
                            export_ref=export_ref, limit=limits.page_size)
    pages = _fetch_pages(port, context, headers, limits)
    try:
        export = merge_verdict_pages(pages, context)
    except VerdictPagesError as exc:
        # 코드만 옮긴다. 원인 체인에 레코드 값이 남지 않게 한다.
        raise VerdictIntegrityError(str(exc)) from None
    return CollectedExport(export_ref, pages, export)
