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
MAX_URL_LENGTH = 512
_SHA = re.compile(r"[0-9a-f]{40}\Z")
_FINGERPRINT = re.compile(r"[0-9a-f]{64}\Z")
_REFERENCE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9./:_-]*\Z")
_TAG = re.compile(r"[A-Za-z0-9_][A-Za-z0-9_.-]*\Z")
_PLACEHOLDER = re.compile(r"(?:^|[/:._-])(?:todo|tbd|changeme|placeholder)(?:$|[/:._-])", re.I)
_SHARED = (
    "network_mode", "llm", "temperature", "seed", "target_origin",
    "worker_count", "time_budget_seconds", "request_budget", "run_storage",
    "fixture_fingerprint", "environment_fingerprint", "scope_fingerprint",
)
# 러너 v1은 전용 망 격리만 지원한다(평가 프로토콜 §2). 엔진은 계측기 origin으로만 대상에 닿는다.
_NETWORK_MODES = ("dedicated_network",)
_DNS_LABEL = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\Z")
_MODEL_SEGMENT = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*\Z")
_PROVIDER = re.compile(r"[a-z0-9][a-z0-9._-]{0,63}\Z")
# 로컬 LLM 고정 IP: llmpolicy/policy.go의 사설 대역 중 loopback(127/8·::1)을 제외한 4개.
# 엔진은 컨테이너라 호스트 loopback으로는 닿지 않으므로 제외한다. ipaddress.is_private는 문서용 대역까지 포함해 쓰지 않는다.
_LOCAL_NETWORKS = tuple(ipaddress.ip_network(net) for net in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"))
# 회차 메모리(tmpfs) 조건: 엔진 DB·엔진 데이터 크기와 구간 종료 뒤 남아야 할 최소 공간. compose tmpfs size 표기
# (1024배 k·m·g). 프로파일 지문·러너 설정과 같은 정의이며(drift 테스트), 하한이 다르면 같은 측정값으로도 완료·무효가 갈리므로
# 환경 지문에 넣는다(#140 리뷰).
_STORAGE_SIZE = re.compile(r"[1-9][0-9]{0,8}[kmg]?\Z")
_STORAGE_UNIT = {"k": 1024, "m": 1024 ** 2, "g": 1024 ** 3}
RUN_STORAGE_FIELDS = ("engine_db_tmpfs", "engine_data_tmpfs", "tmpfs_min_free")
# 백엔드별로 받는 키가 정확히 정해져 있다. 섞이거나 빠지면 거부한다(평가 프로토콜 §6).
_LLM_FIELDS = {
    "api": ("backend", "base_url", "model", "provider", "fallbacks"),
    "local": ("backend", "base_url", "model", "hostname", "host_ip"),
}
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


# 아래 URL·LLM 판정은 profile_fingerprint.py와 같은 정의다(drift 테스트로 묶음).
def _split_url(value: object):
    if not _text(value) or len(value) > MAX_URL_LENGTH or any(char in value for char in "?#%\\"):
        return None
    try:
        parsed = urlsplit(value)
        host, port = parsed.hostname, parsed.port
    except ValueError:
        return None
    if (parsed.scheme not in ("http", "https") or not host or parsed.netloc != parsed.netloc.lower()
            or parsed.username is not None or parsed.password is not None
            or port is not None and not 1 <= port <= 65535):
        return None
    return parsed


def _service_name(host: object) -> bool:
    return (isinstance(host, str) and bool(_DNS_LABEL.fullmatch(host))
            and host != "localhost" and not host.isdigit())


def _dns_name(host: str) -> bool:
    labels = host.split(".")
    return (len(host) <= 253 and len(labels) >= 2 and all(_DNS_LABEL.fullmatch(label) for label in labels)
            and not labels[-1].isdigit())


def _base_path(path: str) -> bool:
    return "//" not in path and not any(part in (".", "..") for part in path.split("/"))


def _meter_origin(value: object) -> bool:
    """전용 망에서 엔진이 보는 대상 origin = 계측기 서비스 이름과 명시 포트. loopback·IP는 아니다."""
    parsed = _split_url(value)
    return (parsed is not None and parsed.scheme == "http" and _service_name(parsed.hostname)
            and parsed.port is not None and parsed.path in ("", "/"))


def _api_model(value: object) -> bool:
    if not _text(value):
        return False
    name, separator, variant = value.partition(":")
    segments = name.split("/")
    parts = [*segments, variant] if separator else segments
    return (len(segments) >= 2 and all(_MODEL_SEGMENT.fullmatch(part) for part in parts)
            and all(part.casefold() != "latest" for part in parts))


def _llm(value: object) -> bool:
    if not isinstance(value, dict) or not isinstance(value.get("backend"), str) or value["backend"] not in _LLM_FIELDS:
        return False
    if set(value) != set(_LLM_FIELDS[value["backend"]]):
        return False
    parsed = _split_url(value["base_url"])
    if parsed is None or not _base_path(parsed.path):
        return False
    if value["backend"] == "api":
        provider = value["provider"]
        return (parsed.scheme == "https" and _dns_name(parsed.hostname) and _api_model(value["model"])
                and isinstance(provider, str) and bool(_PROVIDER.fullmatch(provider))
                and not _PLACEHOLDER.search(provider) and value["fallbacks"] is False)
    host_ip = value["host_ip"]
    try:
        address = ipaddress.ip_address(host_ip) if isinstance(host_ip, str) else None
    except ValueError:
        address = None
    return (_service_name(value["hostname"]) and parsed.hostname == value["hostname"]
            and _tagged_reference(value["model"]) and address is not None and host_ip == str(address)
            and any(address in network for network in _LOCAL_NETWORKS))


def _number(value: object, *, positive: bool) -> bool:
    try:
        return (type(value) in (int, float) and math.isfinite(value)
                and (value > 0 if positive else value >= 0))
    except OverflowError:
        return False


def _storage_bytes(text: str) -> int:
    return int(text[:-1]) * _STORAGE_UNIT[text[-1]] if text[-1] in _STORAGE_UNIT else int(text)


def _run_storage(value: object) -> bool:
    """세 크기가 모두 형식에 맞고 하한이 두 tmpfs보다 작아야 한다(아니면 모든 회차가 무효)."""
    if not isinstance(value, dict) or set(value) != set(RUN_STORAGE_FIELDS):
        return False
    if not all(isinstance(value[name], str) and _STORAGE_SIZE.fullmatch(value[name]) for name in RUN_STORAGE_FIELDS):
        return False
    return _storage_bytes(value["tmpfs_min_free"]) < min(_storage_bytes(value["engine_db_tmpfs"]),
                                                         _storage_bytes(value["engine_data_tmpfs"]))


def _valid(field: str, value: object) -> bool:
    if field in ("source_sha", "baseline_source_sha"):
        return isinstance(value, str) and bool(_SHA.fullmatch(value))
    if field.endswith("_fingerprint"):
        return isinstance(value, str) and bool(_FINGERPRINT.fullmatch(value))
    if field == "target_image":
        return _image_reference(value)
    if field == "network_mode":
        return value in _NETWORK_MODES
    if field == "target_origin":
        return _meter_origin(value)
    if field == "llm":
        return _llm(value)
    if field == "run_storage":
        return _run_storage(value)
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
