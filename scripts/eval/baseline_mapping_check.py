"""원본 DTO의 미등록 분류 건수와 검토 source ID만 집계한다. GT/판정 연결 없음."""

from __future__ import annotations

import argparse
import json
import sys
from dataclasses import asdict, dataclass
from pathlib import Path

_HERE = str(Path(__file__).resolve().parent)
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

from baseline_extract import (
    MAX_INPUT_BYTES, MAX_VOCABULARY_BYTES, BaselineExtractError, _read_json, extract_baseline,
)
from baseline_pages import merge_baseline_pages

_FIELDS = ("vulnclass", "assets.type", "severity")


class MappingCheckError(BaselineExtractError):
    """미등록 원문이나 알 수 없는 이름을 포함하지 않는 진단 오류."""


@dataclass(frozen=True)
class MappingFieldDiagnosis:
    field: str
    source_count: int
    source_ids: tuple[str, ...]


@dataclass(frozen=True)
class BaselineMappingDiagnosis:
    declared_source_sha: str
    source_shape: str
    input_source_count: int
    unmapped_source_count: int
    asset_refs_not_collected_count: int
    asset_refs_not_collected_source_ids: tuple[str, ...]
    fields: tuple[MappingFieldDiagnosis, ...]


def diagnose_baseline_mapping(payload: object, vocabulary: object, *, source_sha: str,
                              pages: bool = False) -> BaselineMappingDiagnosis:
    """검증된 원본 DTO의 고정 필드만 집계한다. 입력은 수정하지 않는다.

    건수는 unique source 행 기준이며 자원 참조 미수집을 미등록과 구분한다.
    집계0/빈 입력은 전체 수집·매핑/실검사 완료나 평가 실행 허가가 아니다.
    """
    if type(pages) is not bool:
        raise MappingCheckError("mode: EXPECTED_BOOL")
    try:
        if pages:
            merged = merge_baseline_pages(payload, vocabulary, source_sha=source_sha)
            source_shape = "pages"
            findings, declared_source = merged.findings, merged.declared_source_sha
        else:
            extracted = extract_baseline(payload, vocabulary, source_sha=source_sha)
            source_shape = extracted.source_shape
            findings, declared_source = extracted.findings, extracted.declared_source_sha
    except BaselineExtractError as error:
        raise MappingCheckError(str(error)) from None

    by_field = {field: set() for field in _FIELDS}
    unmapped, uncollected_assets = set(), set()
    for finding in findings:
        if any(field not in by_field for field in finding.unmapped_fields):
            # 추출기 계약이 확장되면 미등록 필드를 조용히 빼고 통과시키지 않는다.
            raise MappingCheckError("classification: UNSUPPORTED_UNMAPPED_FIELD")
        for field in finding.unmapped_fields:
            by_field[field].add(finding.source_id)
            unmapped.add(finding.source_id)
        if finding.asset_refs is None:
            uncollected_assets.add(finding.source_id)
    fields = tuple(MappingFieldDiagnosis(field, len(by_field[field]),
                   tuple(sorted(by_field[field], key=int))) for field in _FIELDS)
    return BaselineMappingDiagnosis(declared_source, source_shape, len(findings),
        len(unmapped), len(uncollected_assets), tuple(sorted(uncollected_assets, key=int)), fields)


class _ArgumentParser(argparse.ArgumentParser):
    def error(self, message):
        raise MappingCheckError("cli: INVALID_ARGUMENTS")


def main(argv: list[str] | None = None) -> int:
    parser = _ArgumentParser(description="원본 ARTEX 분류 매핑의 고정 필드·source 건수 진단 (오프라인)")
    parser.add_argument("findings", help="원본 FindingDTO 응답 JSON")
    parser.add_argument("vocabulary", help="평가자가 검토한 분류 매핑 JSON")
    parser.add_argument("--source-sha", required=True, help="원본 소스 SHA 선언")
    parser.add_argument("--pages", action="store_true", help="입력이 페이지 객체들의 배열임을 명시")
    try:
        args = parser.parse_args(argv)
        payload = _read_json(args.findings, MAX_INPUT_BYTES)
        vocabulary = _read_json(args.vocabulary, MAX_VOCABULARY_BYTES)
        result = diagnose_baseline_mapping(payload, vocabulary, source_sha=args.source_sha, pages=args.pages)
        output = json.dumps(asdict(result), ensure_ascii=True, allow_nan=False, sort_keys=True)
    except BaselineExtractError as error:
        print(str(error), file=sys.stderr)
        return 2
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
