"""비민감 평가 조건 선언의 결정적 SHA-256. 파일 읽기 외 I/O와 실행 허가는 없다."""

from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import re
import sys
from dataclasses import asdict, dataclass
from decimal import Decimal, InvalidOperation
from pathlib import Path
from urllib.parse import urlsplit


FORMAT = "abc2lab-eval-profile-v1"
MAX_INPUT_BYTES = 64 * 1024
MAX_PATH_LENGTH = 512
MAX_REFERENCE_LENGTH = 256
MAX_DECIMAL_DIGITS = 128
MAX_DECIMAL_EXPONENT = 128
_INT64_MAX = (1 << 63) - 1
_LABEL = re.compile(r"[a-z][a-z0-9_]{0,63}\Z")
_STATIC_SEGMENT = re.compile(r"[A-Za-z0-9._~-]+\Z")
_REFERENCE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9./:_-]*\Z")
_TAG = re.compile(r"[A-Za-z0-9_][A-Za-z0-9_.-]*\Z")
_HEX = re.compile(r"[0-9a-f]{64}\Z")
_PLACEHOLDER = re.compile(r"(?:^|[/:._-])(?:todo|tbd|changeme|placeholder)(?:$|[/:._-])", re.I)
_METHODS = ("GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE", "TRACE")


class ProfileError(ValueError):
    """고정 필드명·오류 코드만 포함하며 원문 값·모르는 필드명은 출력하지 않는다."""


class _ArgumentParser(argparse.ArgumentParser):
    def error(self, _message: str) -> None:
        _fail("cli", "INVALID_ARGUMENTS")


@dataclass(frozen=True)
class ProfileFingerprints:
    fixture_fingerprint: str
    environment_fingerprint: str
    scope_fingerprint: str
    format: str = FORMAT


def _fail(path: str, code: str) -> None:
    raise ProfileError(f"{path}: {code}")


def _object(raw: object, fields: tuple[str, ...], path: str) -> dict:
    if not isinstance(raw, dict):
        _fail(path, "EXPECTED_OBJECT")
    if any(key not in fields for key in raw):
        _fail(path, "UNKNOWN_FIELDS")
    if any(key not in raw for key in fields):
        _fail(path, "MISSING_FIELDS")
    return raw


def _label(value: object, path: str) -> str:
    if not isinstance(value, str) or not _LABEL.fullmatch(value):
        _fail(path, "EXPECTED_LOGICAL_LABEL")
    return value


def _integer(value: object, path: str, *, zero: bool = False) -> int:
    if type(value) is not int or not (0 if zero else 1) <= value <= _INT64_MAX:
        _fail(path, "INVALID_INTEGER")
    return value


def _number(value: object, path: str, *, positive: bool) -> str:
    if type(value) not in (int, float, Decimal):
        _fail(path, "INVALID_NUMBER")
    try:
        number = value if isinstance(value, Decimal) else Decimal(str(value))
    except (ValueError, InvalidOperation):
        _fail(path, "INVALID_NUMBER")
    if not number.is_finite() or (number <= 0 if positive else number < 0):
        _fail(path, "INVALID_NUMBER")
    if number == 0:
        return "0"
    parts = number.as_tuple()
    if len(parts.digits) > MAX_DECIMAL_DIGITS or abs(parts.exponent) > MAX_DECIMAL_EXPONENT:
        _fail(path, "NUMBER_TOO_LARGE")
    # Decimal.normalize()는 현재 precision으로 반올림하므로 원래 숫자를 직접 정규화한다.
    text = format(number, "f")
    return text.rstrip("0").rstrip(".") if "." in text else text


def _ascii(value: object) -> bool:
    return isinstance(value, str) and bool(value) and value.isascii() and all(32 < ord(char) < 127 for char in value)


def _tagged(value: str) -> bool:
    name, separator, tag = value.rpartition(":")
    return bool(separator and _REFERENCE_NAME.fullmatch(name) and _TAG.fullmatch(tag)
                and tag.casefold() != "latest" and not _PLACEHOLDER.search(value))


def _reference(value: object, path: str, *, image: bool = False) -> str:
    valid = False
    if _ascii(value) and len(value) <= MAX_REFERENCE_LENGTH:
        if image and "@" in value:
            name, _, digest = value.partition("@sha256:")
            tagged = ":" in name.rsplit("/", 1)[-1]
            valid = bool(_REFERENCE_NAME.fullmatch(name) and _HEX.fullmatch(digest)
                         and (not tagged or _tagged(name)) and not _PLACEHOLDER.search(name))
        else:
            valid = _tagged(value)
    if not valid:
        _fail(path, "EXPECTED_PINNED_REFERENCE")
    return value


def _url(value: object, path: str, *, base_url: bool = False) -> str:
    if not _ascii(value) or len(value) > MAX_PATH_LENGTH or any(char in value for char in "?#%\\"):
        _fail(path, "INVALID_LOCAL_URL")
    try:
        parsed = urlsplit(value)
        host, port = parsed.hostname, parsed.port
        if (parsed.scheme not in ("http", "https") or not host
                or parsed.username is not None or parsed.password is not None
                or port is not None and not 1 <= port <= 65535
                or host != "localhost" and not ipaddress.ip_address(host).is_loopback):
            _fail(path, "INVALID_LOCAL_URL")
        if base_url:
            if "//" in parsed.path or any(part in (".", "..") for part in parsed.path.split("/")):
                _fail(path, "INVALID_LOCAL_URL")
        elif parsed.path not in ("", "/"):
            _fail(path, "INVALID_LOCAL_URL")
    except ProfileError:
        raise
    except ValueError:
        raise ProfileError(f"{path}: INVALID_LOCAL_URL") from None
    return value


def _path(value: object) -> str:
    if not _ascii(value) or len(value) > MAX_PATH_LENGTH or not value.startswith("/"):
        _fail("scope.paths", "INVALID_PATH_TEMPLATE")
    if value == "/":
        return value
    parts = value[1:].split("/")
    if parts[-1] == "":
        parts.pop()
    for part in parts:
        template = part.startswith("{") and part.endswith("}") and _LABEL.fullmatch(part[1:-1])
        if not part or part in (".", "..") or not (template or _STATIC_SEGMENT.fullmatch(part)):
            _fail("scope.paths", "INVALID_PATH_TEMPLATE")
    return value


def _scope(raw: object) -> dict:
    raw = _object(raw, ("target_origin", "methods", "paths", "vuln_classes"), "scope")
    normalized = {"target_origin": _url(raw["target_origin"], "scope.target_origin")}
    for field in ("methods", "paths", "vuln_classes"):
        values = raw[field]
        if not isinstance(values, list) or not values:
            _fail(f"scope.{field}", "EXPECTED_NONEMPTY_ARRAY")
        checked = []
        for value in values:
            if field == "methods":
                if not isinstance(value, str) or value not in _METHODS:
                    _fail("scope.methods", "INVALID_METHOD")
                checked.append(value)
            elif field == "paths":
                checked.append(_path(value))
            else:
                checked.append(_label(value, "scope.vuln_classes"))
        if len(set(checked)) != len(checked):
            _fail(f"scope.{field}", "DUPLICATE")
        normalized[field] = sorted(checked)
    return normalized


def _fixture(raw: object) -> dict:
    raw = _object(raw, ("preparation_id", "preparation_revision", "accounts", "resources"), "fixture")
    normalized = {"preparation_id": _label(raw["preparation_id"], "fixture.preparation_id"),
                  "preparation_revision": _integer(raw["preparation_revision"], "fixture.preparation_revision")}
    for field in ("accounts", "resources"):
        if not isinstance(raw[field], list):
            _fail(f"fixture.{field}", "EXPECTED_ARRAY")
    accounts = {}
    for item in raw["accounts"]:
        row = _object(item, ("label", "role"), "fixture.accounts")
        label = _label(row["label"], "fixture.accounts.label")
        role = _label(row["role"], "fixture.accounts.role")
        if label in accounts:
            _fail("fixture.accounts.label", "DUPLICATE")
        accounts[label] = {"label": label, "role": role}
    resources = {}
    for item in raw["resources"]:
        row = _object(item, ("label", "type", "owner", "count", "shared", "delegated"), "fixture.resources")
        label = _label(row["label"], "fixture.resources.label")
        kind = _label(row["type"], "fixture.resources.type")
        owner = _label(row["owner"], "fixture.resources.owner")
        if label in resources:
            _fail("fixture.resources.label", "DUPLICATE")
        if owner not in accounts:
            _fail("fixture.resources.owner", "UNKNOWN_ACCOUNT_LABEL")
        count = _integer(row["count"], "fixture.resources.count")
        if type(row["shared"]) is not bool or type(row["delegated"]) is not bool:
            _fail("fixture.resources", "EXPECTED_BOOL")
        resources[label] = {"label": label, "type": kind, "owner": owner, "count": count,
                            "shared": row["shared"], "delegated": row["delegated"]}
    normalized["accounts"] = [accounts[key] for key in sorted(accounts)]
    normalized["resources"] = [resources[key] for key in sorted(resources)]
    return normalized


def _environment(raw: object) -> dict:
    raw = _object(raw, ("model_tag", "llm_base_url", "temperature", "seed", "target_image",
                        "worker_count", "time_budget_seconds", "request_budget"), "environment")
    return {
        "model_tag": _reference(raw["model_tag"], "environment.model_tag"),
        "llm_base_url": _url(raw["llm_base_url"], "environment.llm_base_url", base_url=True),
        "temperature": _number(raw["temperature"], "environment.temperature", positive=False),
        "seed": _integer(raw["seed"], "environment.seed", zero=True),
        "target_image": _reference(raw["target_image"], "environment.target_image", image=True),
        "worker_count": _integer(raw["worker_count"], "environment.worker_count"),
        "time_budget_seconds": _number(raw["time_budget_seconds"], "environment.time_budget_seconds", positive=True),
        "request_budget": _integer(raw["request_budget"], "environment.request_budget"),
    }


def _digest(kind: str, profile: dict) -> str:
    envelope = {"format": FORMAT, "kind": kind, "profile": profile}
    canonical = json.dumps(envelope, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False)
    return hashlib.sha256(canonical.encode("ascii")).hexdigest()


def generate_fingerprints(payload: object) -> ProfileFingerprints:
    """허용된 선언을 모두 검증한 후 지문화한다. 실환경 일치나 비밀 부재를 인증하지 않는다."""
    raw = _object(payload, ("version", "fixture", "environment", "scope"), "profile")
    if type(raw["version"]) is not int or raw["version"] != 1:
        _fail("profile.version", "UNSUPPORTED_VERSION")
    fixture = _fixture(raw["fixture"])
    scope = _scope(raw["scope"])
    environment = _environment(raw["environment"])
    scope_digest = _digest("scope", scope)
    environment["scope_fingerprint"] = scope_digest
    return ProfileFingerprints(_digest("fixture", fixture), _digest("environment", environment), scope_digest)


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result = {}
    for key, value in pairs:
        if key in result:
            _fail("json", "DUPLICATE_KEY")
        result[key] = value
    return result


def _reject_constant(_: str) -> None:
    _fail("json", "NONFINITE_NUMBER")


def _read_profile(path: str | Path) -> object:
    try:
        with open(path, "rb") as stream:
            raw = stream.read(MAX_INPUT_BYTES + 1)
    except (OSError, ValueError):
        raise ProfileError("json: READ_FAILED") from None
    if len(raw) > MAX_INPUT_BYTES:
        _fail("json", "TOO_LARGE")
    try:
        return json.loads(raw.decode("utf-8"), object_pairs_hook=_unique_object,
                          parse_constant=_reject_constant, parse_float=Decimal)
    except ProfileError:
        raise
    except (ValueError, UnicodeError, InvalidOperation, RecursionError):
        raise ProfileError("json: INVALID_JSON") from None


def main(argv: list[str] | None = None) -> int:
    parser = _ArgumentParser(description="비민감 평가 프로파일의 결정적 SHA-256 생성 (오프라인)")
    parser.add_argument("profile", help="검토된 비민감 프로파일 JSON")
    try:
        args = parser.parse_args(argv)
        result = generate_fingerprints(_read_profile(args.profile))
    except ProfileError as error:
        print(str(error), file=sys.stderr)
        return 2
    print(json.dumps(asdict(result), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
