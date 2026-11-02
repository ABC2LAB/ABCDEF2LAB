"""원본 ARTEX 페이지 선언을 대조하고 비민감 추출 결과를 병합한다.

입력 페이지 집합의 수치/참조 일관성만 검사한다. 같은 조회에서 왔는지, 서버 자료가
실제로 전부 수집됐는지, 근거가 실재하는지 또는 해결 주체가 누구인지는 인증하지 않는다.
"""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import asdict, dataclass
from pathlib import Path

# -I 실행에서도 현재 디렉터리 대신 이 스크립트의 동반 모듈만 사용한다.
_HERE = str(Path(__file__).resolve().parent)
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from baseline_extract import (
    MAX_INPUT_BYTES, MAX_VOCABULARY_BYTES, BaselineExtractError, ExtractedFinding,
    _read_json, extract_baseline,
)


class BaselinePagesError(BaselineExtractError):
    """입력 원문 대신 고정 필드명/오류 코드만 반환한다."""


@dataclass(frozen=True)
class BaselinePageMerge:
    declared_source_sha: str
    declared_total: int
    page_size: int
    page_count: int
    findings: tuple[ExtractedFinding, ...]


def _fail(code: str) -> None:
    raise BaselinePagesError("pages: " + code)


def merge_baseline_pages(pages: object, vocabulary: object, *, source_sha: str) -> BaselinePageMerge:
    """페이지 1..N을 정확히 한 번씩 요구한다. 입력은 수정하지 않고 번호순으로 병합한다.

    source_sha와 분류 매핑은 기존 추출기로 검증한다. 반환 declared_total은 원본의
    선언값이며, 전체 finding 수가 이 선언과 같은지 확인한 결과만 제공한다.
    """
    if not isinstance(pages, list) or not pages:
        _fail("EXPECTED_NONEMPTY_PAGE_ARRAY")
    extracted = []
    for raw in pages:
        if not isinstance(raw, dict) or "items" not in raw:
            _fail("EXPECTED_PAGE_OBJECT")
        try:
            extracted.append(extract_baseline(raw, vocabulary, source_sha=source_sha))
        except BaselineExtractError as error:
            raise BaselinePagesError(str(error)) from None

    first = extracted[0].pagination
    seen_pages = set()
    for result in extracted:
        page = result.pagination
        if (page.total, page.page_size) != (first.total, first.page_size):
            _fail("PAGINATION_MISMATCH")
        if page.page in seen_pages:
            _fail("DUPLICATE_PAGE")
        seen_pages.add(page.page)
    expected_count = max(1, (first.total + first.page_size - 1) // first.page_size)
    if len(extracted) != expected_count:
        _fail("INCOMPLETE_PAGES")

    ordered = sorted(extracted, key=lambda result: result.pagination.page)
    merged, sources, bindings = [], set(), set()
    for number, result in enumerate(ordered, start=1):
        if result.pagination.page != number:
            _fail("PAGE_SEQUENCE_GAP")
        expected_length = min(first.page_size, max(0, first.total - (number - 1) * first.page_size))
        if len(result.findings) != expected_length:
            _fail("PAGE_LENGTH_MISMATCH")
        for finding in result.findings:
            if finding.source_id in sources:
                _fail("DUPLICATE_SOURCE_ID")
            sources.add(finding.source_id)
            for binding in finding.traffic_binding_refs or ():
                if binding.binding_id in bindings:
                    _fail("DUPLICATE_BINDING_ID")
                bindings.add(binding.binding_id)
            merged.append(finding)
    return BaselinePageMerge(extracted[0].declared_source_sha, first.total, first.page_size,
                             len(ordered), tuple(merged))


class _ArgumentParser(argparse.ArgumentParser):
    def error(self, message):
        # argparse 기본 메시지는 알 수 없는 인자의 원문을 포함할 수 있다.
        raise BaselinePagesError("cli: INVALID_ARGUMENTS")


def main(argv: list[str] | None = None) -> int:
    parser = _ArgumentParser(description="원본 ARTEX 페이지 집합의 선언 일관성 검사·비민감 병합 (오프라인)")
    parser.add_argument("pages", help="FindingDTO 페이지 객체들의 JSON 배열")
    parser.add_argument("vocabulary", help="평가자가 등록한 비민감 분류 매핑 JSON")
    parser.add_argument("--source-sha", required=True, help="원본 소스 SHA 선언")
    try:
        args = parser.parse_args(argv)
        vocabulary = _read_json(args.vocabulary, MAX_VOCABULARY_BYTES)
        pages = _read_json(args.pages, MAX_INPUT_BYTES)
        result = merge_baseline_pages(pages, vocabulary, source_sha=args.source_sha)
        output = json.dumps(asdict(result), ensure_ascii=True, allow_nan=False, sort_keys=True)
    except BaselineExtractError as error:
        print(str(error), file=sys.stderr)
        return 2
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
