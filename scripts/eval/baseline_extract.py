"""고정 원본 ARTEX FindingDTO에서 평가용 비민감 참조만 추출한다. GT/판정 연결 없음."""

from __future__ import annotations

import argparse
import json
import math
import re
import sys
from dataclasses import asdict, dataclass
from pathlib import Path


UPSTREAM_COMMIT = "fdfb7a17b7da5e2de716c0e98475ec77a41c64c6"
MAX_INPUT_BYTES = 8 * 1024 * 1024
MAX_VOCABULARY_BYTES = 64 * 1024
_INT64_MAX = (1 << 63) - 1
_ID = re.compile(r"[1-9][0-9]{0,18}\Z")
_LABEL = re.compile(r"[a-z][a-z0-9_]{0,63}\Z")
_SEVERITIES = ("critical", "high", "medium", "low")
_STATUSES = ("pending", "in_progress", "confirmed", "resolved", "fixed", "false_positive",
             "ignored", "duplicate", "risk_accepted")
_ROLES = ("baseline", "proof", "verification", "supporting")
_FINDING_FIELDS = frozenset({
    "id", "finding_id", "vulnclass", "name", "severity", "status", "summary", "evidence", "report",
    "intent_id", "param_id", "task_id", "task_description", "source_task_id", "inherited", "assets", "ts",
    "traffic_count", "evidence_version", "report_evidence_version", "report_stale", "traffic_bindings",
})
_BINDING_FIELDS = frozenset({"id", "finding_id", "snapshot_id", "role", "note", "position", "created_at", "snapshot"})


class BaselineExtractError(ValueError):
    """고정 필드명·오류 코드만 담으며 원문 값·모르는 필드명은 출력하지 않는다."""


@dataclass(frozen=True)
class AssetReference:
    id: str
    type: str | None


@dataclass(frozen=True)
class TrafficBindingReference:
    binding_id: str
    finding_id: str
    role: str


@dataclass(frozen=True)
class ExtractedFinding:
    source_id: str
    finding_id: str | None
    vuln_class: str | None
    severity: str
    source_status: str
    asset_refs: tuple[AssetReference, ...] | None
    traffic_binding_refs: tuple[TrafficBindingReference, ...] | None
    unmapped_fields: tuple[str, ...]


@dataclass(frozen=True)
class Page:
    total: int
    page: int
    page_size: int


@dataclass(frozen=True)
class BaselineExtraction:
    declared_source_sha: str
    source_shape: str
    pagination: Page | None
    findings: tuple[ExtractedFinding, ...]


def _fail(path: str, code: str) -> None:
    raise BaselineExtractError(f"{path}: {code}")


def _object(raw: object, fields: frozenset[str], path: str) -> dict:
    if not isinstance(raw, dict):
        _fail(path, "EXPECTED_OBJECT")
    if any(key not in fields for key in raw):
        _fail(path, "UNKNOWN_FIELDS")
    return raw


def _id(value: object, path: str) -> str:
    if not isinstance(value, str) or not _ID.fullmatch(value) or int(value) > _INT64_MAX:
        _fail(path, "EXPECTED_POSITIVE_INT64_STRING")
    return value


def _enum(value: object, choices: tuple[str, ...], path: str) -> str:
    if not isinstance(value, str) or value not in choices:
        _fail(path, "UNSUPPORTED_VALUE")
    return value


def _classification(value: object, mapping: dict[str, str], path: str) -> str | None:
    if not isinstance(value, str):
        _fail(path, "EXPECTED_STRING")
    return mapping.get(value)


def _vocabulary(payload: object) -> tuple[dict[str, str], dict[str, str]]:
    raw = _object(payload, frozenset({"vulnclasses", "asset_types"}), "vocabulary")
    mappings = []
    for field in ("vulnclasses", "asset_types"):
        entries = raw.get(field)
        if not isinstance(entries, dict):
            _fail(f"vocabulary.{field}", "EXPECTED_OBJECT")
        for key, value in entries.items():
            if (not isinstance(key, str) or not key or len(key) > 128
                    or any(ord(char) < 32 or ord(char) == 127 for char in key)
                    or not isinstance(value, str) or not _LABEL.fullmatch(value)):
                _fail(f"vocabulary.{field}", "INVALID_MAPPING")
        mappings.append(dict(entries))
    return mappings[0], mappings[1]


def _assets(raw: object, mapping: dict[str, str], unmapped: set[str]) -> tuple[AssetReference, ...] | None:
    if raw is None:
        return None
    if not isinstance(raw, list):
        _fail("finding.assets", "EXPECTED_ARRAY")
    refs, seen = [], set()
    for item in raw:
        asset = _object(item, frozenset({"id", "type", "label"}), "finding.assets")
        identifier = _id(asset.get("id"), "finding.assets.id")
        if identifier in seen:
            _fail("finding.assets.id", "DUPLICATE")
        seen.add(identifier)
        kind = _classification(asset.get("type"), mapping, "finding.assets.type")
        if kind is None:
            unmapped.add("assets.type")
        refs.append(AssetReference(identifier, kind))
    return tuple(refs)


def _bindings(raw: object, finding_id: str | None) -> tuple[TrafficBindingReference, ...] | None:
    if raw is None:
        return None
    if not isinstance(raw, list):
        _fail("finding.traffic_bindings", "EXPECTED_ARRAY")
    refs, seen = [], set()
    for item in raw:
        binding = _object(item, _BINDING_FIELDS, "finding.traffic_bindings")
        identifier = _id(binding.get("id"), "finding.traffic_bindings.id")
        owner = _id(binding.get("finding_id"), "finding.traffic_bindings.finding_id")
        if finding_id is None or owner != finding_id:
            _fail("finding.traffic_bindings.finding_id", "OWNER_MISMATCH")
        if identifier in seen:
            _fail("finding.traffic_bindings.id", "DUPLICATE")
        seen.add(identifier)
        role = _enum(binding.get("role"), _ROLES, "finding.traffic_bindings.role")
        refs.append(TrafficBindingReference(identifier, owner, role))
    return tuple(refs)


def _finding(raw: object, classes: dict[str, str], types: dict[str, str]) -> ExtractedFinding:
    row = _object(raw, _FINDING_FIELDS, "finding")
    source_id = _id(row.get("id"), "finding.id")
    finding_id = None if row.get("finding_id") is None else _id(row["finding_id"], "finding.finding_id")
    unmapped: set[str] = set()
    vuln_class = _classification(row.get("vulnclass"), classes, "finding.vulnclass")
    if vuln_class is None:
        unmapped.add("vulnclass")
    severity = _enum(row.get("severity"), _SEVERITIES, "finding.severity")
    status = _enum(row.get("status"), _STATUSES, "finding.status")
    assets = _assets(row.get("assets"), types, unmapped)
    bindings = _bindings(row.get("traffic_bindings"), finding_id)
    return ExtractedFinding(source_id, finding_id, vuln_class, severity, status, assets, bindings, tuple(sorted(unmapped)))


def extract_baseline(payload: object, vocabulary: object, *, source_sha: str) -> BaselineExtraction:
    """고정 FindingDTO만 투영한다. source_sha는 호출자의 선언이며 출처 인증을 대신하지 않는다."""
    if source_sha != UPSTREAM_COMMIT:
        _fail("source_sha", "UNSUPPORTED_COMMIT")
    classes, types = _vocabulary(vocabulary)
    page = None
    if isinstance(payload, list):
        shape, items = "array", payload
    elif isinstance(payload, dict) and "items" in payload:
        raw = _object(payload, frozenset({"items", "total", "page", "page_size"}), "page")
        if not isinstance(raw["items"], list):
            _fail("page.items", "EXPECTED_ARRAY")
        for field in ("total", "page", "page_size"):
            if type(raw.get(field)) is not int or raw[field] < (0 if field == "total" else 1):
                _fail(f"page.{field}", "INVALID_INTEGER")
        page = Page(raw["total"], raw["page"], raw["page_size"])
        items = raw["items"]
        if len(items) > page.page_size or items and (page.page - 1) * page.page_size + len(items) > page.total:
            _fail("page", "INCONSISTENT_COUNTS")
        shape = "page"
    elif isinstance(payload, dict):
        shape, items = "detail", [payload]
    else:
        _fail("findings", "EXPECTED_API_RESPONSE")
    findings, seen, seen_bindings = [], set(), set()
    for raw in items:
        finding = _finding(raw, classes, types)
        if finding.source_id in seen:
            _fail("finding.id", "DUPLICATE")
        seen.add(finding.source_id)
        for binding in finding.traffic_binding_refs or ():
            if binding.binding_id in seen_bindings:
                _fail("finding.traffic_bindings.id", "DUPLICATE")
            seen_bindings.add(binding.binding_id)
        findings.append(finding)
    return BaselineExtraction(UPSTREAM_COMMIT, shape, page, tuple(findings))


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result = {}
    for key, value in pairs:
        if key in result:
            _fail("json", "DUPLICATE_KEY")
        result[key] = value
    return result


def _reject_constant(_: str) -> None:
    _fail("json", "NONFINITE_NUMBER")


def _finite_float(text: str) -> float:
    value = float(text)
    if not math.isfinite(value):
        _fail("json", "NONFINITE_NUMBER")
    return value


def _read_json(path: str | Path, maximum: int) -> object:
    try:
        with open(path, "rb") as stream:
            raw = stream.read(maximum + 1)
    except (OSError, ValueError):
        _fail("json", "READ_FAILED")
    if len(raw) > maximum:
        _fail("json", "TOO_LARGE")
    try:
        return json.loads(raw.decode("utf-8"), object_pairs_hook=_unique_object,
                          parse_constant=_reject_constant, parse_float=_finite_float)
    except BaselineExtractError:
        raise
    except (ValueError, UnicodeError, RecursionError):
        raise BaselineExtractError("json: INVALID_JSON") from None


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="고정 원본 ARTEX finding의 비민감 분류·참조 추출 (오프라인)")
    parser.add_argument("findings", help="FindingDTO 배열/페이지/단건 JSON")
    parser.add_argument("vocabulary", help="평가자가 등록한 비민감 분류 매핑 JSON")
    parser.add_argument("--source-sha", required=True, help="원본 소스 SHA 선언")
    args = parser.parse_args(argv)
    try:
        vocabulary = _read_json(args.vocabulary, MAX_VOCABULARY_BYTES)
        payload = _read_json(args.findings, MAX_INPUT_BYTES)
        result = extract_baseline(payload, vocabulary, source_sha=args.source_sha)
        output = json.dumps(asdict(result), ensure_ascii=True, allow_nan=False)
    except BaselineExtractError as error:
        print(str(error), file=sys.stderr)
        return 2
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
