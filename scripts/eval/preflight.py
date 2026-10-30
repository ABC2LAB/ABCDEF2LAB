"""평가 전 비민감 설정 선언의 고정값·비교 조건 검사. 파일 읽기 외 I/O 없음."""

from __future__ import annotations

import argparse
import ipaddress
import json
import math
import re
import sys
from pathlib import Path
from urllib.parse import urlsplit


MAX_CONFIG_BYTES = 64 * 1024
_SHA = re.compile(r"[0-9a-f]{40}\Z")
_FINGERPRINT = re.compile(r"[0-9a-f]{64}\Z")
_REFERENCE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9./:_-]*\Z")
_TAG = re.compile(r"[A-Za-z0-9_][A-Za-z0-9_.-]*\Z")
_PLACEHOLDER = re.compile(r"(?:^|[/:._-])(?:todo|tbd|changeme|placeholder)(?:$|[/:._-])", re.I)
_SHARED = (
    "model_tag", "llm_base_url", "temperature", "seed", "target_origin",
    "worker_count", "time_budget_seconds", "request_budget",
    "fixture_fingerprint", "environment_fingerprint", "scope_fingerprint",
)
_RUN_FIELDS = ("source_sha", "target_image", *_SHARED)
_PIN_FIELDS = ("baseline_source_sha", "target_image")


class PreflightError(ValueError):
    """고정 필드명과 이유 코드만 포함한다. 입력값·알 수 없는 키 이름은 출력하지 않는다."""

    def __init__(self, errors: list[str]):
        self.errors = tuple(errors)
        super().__init__("\n".join(errors))


def _text(value: object) -> bool:
    return (isinstance(value, str) and bool(value) and value.isascii()
            and all(32 < ord(char) < 127 for char in value)
            and not any(char in value for char in "<>{}$") and not _PLACEHOLDER.search(value))


def _tagged_reference(value: object) -> bool:
    if not _text(value):
        return False
    name, separator, tag = value.rpartition(":")
    return bool(separator and _REFERENCE_NAME.fullmatch(name) and _TAG.fullmatch(tag)
                and tag.casefold() != "latest")


def _image_reference(value: object) -> bool:
    if _text(value) and "@" in value:
        name, _, digest = value.partition("@sha256:")
        tagged = ":" in name.rsplit("/", 1)[-1]
        return bool(_REFERENCE_NAME.fullmatch(name) and _FINGERPRINT.fullmatch(digest)
                    and (not tagged or _tagged_reference(name)))
    return _tagged_reference(value)


def _local_url(value: object, *, base_url: bool) -> bool:
    if not _text(value) or any(char in value for char in "?#%\\"):
        return False
    try:
        parsed = urlsplit(value)
        host, port = parsed.hostname, parsed.port
        if (parsed.scheme not in ("http", "https") or not host
                or parsed.username is not None or parsed.password is not None
                or port is not None and not 1 <= port <= 65535):
            return False
        if host != "localhost" and not ipaddress.ip_address(host).is_loopback:
            return False
        if base_url:
            return "//" not in parsed.path and not any(part in (".", "..") for part in parsed.path.split("/"))
        return parsed.path in ("", "/")
    except ValueError:
        return False


def _number(value: object, *, positive: bool) -> bool:
    try:
        return (type(value) in (int, float) and math.isfinite(value)
                and (value > 0 if positive else value >= 0))
    except OverflowError:
        return False


def _valid(field: str, value: object) -> bool:
    if field in ("source_sha", "baseline_source_sha"):
        return isinstance(value, str) and bool(_SHA.fullmatch(value))
    if field.endswith("_fingerprint"):
        return isinstance(value, str) and bool(_FINGERPRINT.fullmatch(value))
    if field == "target_image":
        return _image_reference(value)
    if field == "model_tag":
        return _tagged_reference(value)
    if field in ("target_origin", "llm_base_url"):
        return _local_url(value, base_url=field == "llm_base_url")
    if field == "temperature":
        return _number(value, positive=False)
    if field == "time_budget_seconds":
        return _number(value, positive=True)
    return type(value) is int and value >= (0 if field == "seed" else 1)


def _fields(raw: object, names: tuple[str, ...], prefix: str, errors: list[str]) -> set[str]:
    if not isinstance(raw, dict):
        errors.append(f"{prefix}: EXPECTED_OBJECT")
        return set()
    if any(key not in names for key in raw):
        errors.append(f"{prefix}: UNKNOWN_FIELDS")
    valid = set()
    for name in names:
        if name not in raw:
            errors.append(f"{prefix}.{name}: REQUIRED")
        elif not _valid(name, raw[name]):
            errors.append(f"{prefix}.{name}: INVALID")
        else:
            valid.add(name)
    return valid


def check_preflight(payload: object) -> None:
    """설정 선언의 일관성만 검사한다. 실환경의 버전·설정 적용·실행 가능성은 별도 확인한다."""
    errors: list[str] = []
    if not isinstance(payload, dict):
        raise PreflightError(["config: EXPECTED_OBJECT"])
    if any(key not in ("pins", "baseline", "ours") for key in payload):
        errors.append("config: UNKNOWN_FIELDS")
    pins, baseline, ours = (payload.get(key) for key in ("pins", "baseline", "ours"))
    valid_pins = _fields(pins, _PIN_FIELDS, "pins", errors)
    valid_baseline = _fields(baseline, _RUN_FIELDS, "baseline", errors)
    valid_ours = _fields(ours, _RUN_FIELDS, "ours", errors)
    if "baseline_source_sha" in valid_pins and "source_sha" in valid_baseline:
        if pins["baseline_source_sha"] != baseline["source_sha"]:
            errors.append("baseline.source_sha: PIN_MISMATCH")
    for label, config, valid in (("baseline", baseline, valid_baseline), ("ours", ours, valid_ours)):
        if "target_image" in valid_pins and "target_image" in valid:
            if pins["target_image"] != config["target_image"]:
                errors.append(f"{label}.target_image: PIN_MISMATCH")
    for field in _SHARED:
        if field in valid_baseline and field in valid_ours and baseline[field] != ours[field]:
            errors.append(f"comparison.{field}: MISMATCH")
    if errors:
        raise PreflightError(errors)


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result = {}
    for key, value in pairs:
        if key in result:
            raise PreflightError(["config: DUPLICATE_KEY"])
        result[key] = value
    return result


def _reject_constant(_: str) -> None:
    raise PreflightError(["config: NONFINITE_NUMBER"])


def load_preflight(path: str | Path) -> object:
    try:
        with open(path, "rb") as source:
            raw = source.read(MAX_CONFIG_BYTES + 1)
    except (OSError, ValueError):
        raise PreflightError(["config: READ_FAILED"]) from None
    if len(raw) > MAX_CONFIG_BYTES:
        raise PreflightError(["config: TOO_LARGE"])
    try:
        return json.loads(raw.decode("utf-8"), object_pairs_hook=_unique_object, parse_constant=_reject_constant)
    except PreflightError:
        raise
    except (ValueError, UnicodeError, RecursionError):
        raise PreflightError(["config: INVALID_JSON"]) from None


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="평가 조건 선언의 고정값·baseline/ours 일치 검사 (오프라인)")
    parser.add_argument("config", help="비민감 사전검사 JSON 파일")
    args = parser.parse_args(argv)
    try:
        check_preflight(load_preflight(args.config))
    except PreflightError as error:
        print(str(error), file=sys.stderr)
        return 2
    print("선언된 평가 비교 조건 검사 통과 (실환경 확인은 별도)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
