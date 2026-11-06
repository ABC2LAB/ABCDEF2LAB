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


FORMAT = "abc2lab-eval-profile-v6"
# 엔진 압축 기준 창(K 토큰). 엔진이 이 범위로 잘라 쓰므로(agent/provider.go) 범위 밖 선언은 적용값과 달라진다.
MIN_CONTEXT_WINDOW_K, MAX_CONTEXT_WINDOW_K = 32, 1000
PROFILE_VERSION = 5
MAX_INPUT_BYTES = 64 * 1024
MAX_PATH_LENGTH = 512
MAX_URL_LENGTH = 512
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
# 러너 v1은 전용 망 격리만 지원한다(평가 프로토콜 §2). 엔진은 계측기 origin으로만 대상에 닿는다.
_NETWORK_MODES = ("dedicated_network",)
_DNS_LABEL = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\Z")
_MODEL_SEGMENT = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*\Z")
# 제공자 slug. OpenRouter 엔드포인트 tag처럼 `slug/변형`(예: deepinfra/fp8)까지 받는다. 문법 검사일 뿐이다: OpenRouter slug는
# `provider[/variant][/region][/tier]` 접두 매칭이라 두 segment가 단일 엔드포인트·정밀도를 보장하지 않는다(평가 프로토콜 §6).
# 게이트웨이(cmd/eval-meter providerSlugRe)와 같은 문법이며 drift 테스트로 묶는다.
_PROVIDER = re.compile(r"[a-z0-9][a-z0-9._-]{0,63}(?:/[a-z0-9][a-z0-9._-]{0,63})?\Z")
# 로컬 LLM 고정 IP: llmpolicy/policy.go의 사설 대역 중 loopback(127/8·::1)을 제외한 4개.
# 엔진은 컨테이너라 호스트 loopback으로는 닿지 않으므로 제외한다. ipaddress.is_private는 문서용 대역까지 포함해 쓰지 않는다.
_LOCAL_NETWORKS = tuple(ipaddress.ip_network(net) for net in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"))
# 회차 메모리(tmpfs) 조건: 엔진 DB·엔진 데이터 크기와 구간 종료 뒤 남아야 할 최소 공간. compose tmpfs size 표기
# (1024배 k·m·g). 러너 설정과 같은 정의이며(drift 테스트), 하한이 다르면 같은 측정값으로도 완료·무효가 갈리므로
# 환경 지문에 넣는다(#140 리뷰).
_STORAGE_SIZE = re.compile(r"[1-9][0-9]{0,8}[kmg]?\Z")
_STORAGE_UNIT = {"k": 1024, "m": 1024 ** 2, "g": 1024 ** 3}
RUN_STORAGE_FIELDS = ("engine_db_tmpfs", "engine_data_tmpfs", "tmpfs_min_free")
# 봉인 뒤 drain 정책(초): 계측기가 열린 요청을 끊기까지의 유예와, 끊은 요청이 있을 때 post 관측 전 정착 대기.
# 둘 다 post 관측 시점과 취소 여부를 바꾸므로 비교 조건이다(#153 리뷰). 상한은 러너 설정·계측기와 같다(drift 테스트).
RUN_DRAIN_FIELDS = ("grace_seconds", "settle_seconds")
MAX_DRAIN_GRACE_SECONDS = 600
MAX_SETTLE_SECONDS = 3600
# temperature·seed 외 샘플링 고정값. 엔진은 샘플링을 보내지 않아 평가 LLM 게이트웨이가 요청마다 넣는다(평가 프로토콜 §6).
# 범위는 게이트웨이(cmd/eval-meter sampling.go)와 같다(drift 테스트).
SAMPLING_FIELDS = ("top_p", "top_k", "min_p", "presence_penalty", "repetition_penalty")
# local은 로컬 서버의 OpenAI 호환 경로가 실제로 적용하는 값만 받는다. top_k·min_p·repetition_penalty는
# 그 경로가 오류 없이 무시하므로 적으면 적용되지 않은 값을 기록하게 된다(profiles/evidence/ollama-openai-sampling).
LOCAL_SAMPLING_FIELDS = ("top_p", "presence_penalty", "frequency_penalty")
MAX_TOP_K = 1000
MAX_PENALTY = 2
# 백엔드별로 받는 키가 정확히 정해져 있다. 섞이거나 빠지면 거부한다(평가 프로토콜 §6).
_LLM_FIELDS = {
    "api": ("backend", "base_url", "model", "provider", "fallbacks"),
    # 태그만으로는 고정이 아니다. 가중치 digest·서버가 할당한 컨텍스트·서버 버전을 함께 선언한다(러너가 실제 값과 대조).
    "local": ("backend", "base_url", "model", "hostname", "host_ip", "model_digest", "context_length", "server_version"),
}
_MODEL_DIGEST = re.compile(r"[0-9a-f]{64}")
_SERVER_VERSION = re.compile(r"[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}")
MAX_CONTEXT_LENGTH = 4 * 1024 * 1024


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


# 아래 URL·LLM 판정은 preflight.py와 같은 정의다(drift 테스트로 묶음).
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
            and any(address in network for network in _LOCAL_NETWORKS)
            and isinstance(value["model_digest"], str) and bool(_MODEL_DIGEST.fullmatch(value["model_digest"]))
            and type(value["context_length"]) is int and 1 <= value["context_length"] <= MAX_CONTEXT_LENGTH
            and isinstance(value["server_version"], str) and bool(_SERVER_VERSION.fullmatch(value["server_version"])))


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
    if not _meter_origin(raw["target_origin"]):
        _fail("scope.target_origin", "INVALID_TARGET_ORIGIN")
    normalized = {"target_origin": raw["target_origin"]}
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


def _run_drain(value: object) -> bool:
    """유예·정착 대기가 정확히 두 키이고 각각 1 이상 상한 이하 정수여야 한다."""
    if not isinstance(value, dict) or set(value) != set(RUN_DRAIN_FIELDS):
        return False
    limits = {"grace_seconds": MAX_DRAIN_GRACE_SECONDS, "settle_seconds": MAX_SETTLE_SECONDS}
    return all(type(value[name]) is int and 1 <= value[name] <= limits[name] for name in RUN_DRAIN_FIELDS)


def _bounded(value: object, path: str, low: int, high: int, *, low_open: bool) -> str:
    """low~high(low_open이면 low 제외) 사이 유한 십진수를 _number와 같은 표기로 정규화한다. 음수도 받는다."""
    if type(value) not in (int, float, Decimal):
        _fail(path, "INVALID_NUMBER")
    try:
        number = value if isinstance(value, Decimal) else Decimal(str(value))
    except (ValueError, InvalidOperation):
        _fail(path, "INVALID_NUMBER")
    if not number.is_finite() or number < low or number > high or (low_open and number == low):
        _fail(path, "INVALID_NUMBER")
    if number == 0:
        return "0"
    parts = number.as_tuple()
    if len(parts.digits) > MAX_DECIMAL_DIGITS or abs(parts.exponent) > MAX_DECIMAL_EXPONENT:
        _fail(path, "NUMBER_TOO_LARGE")
    text = format(number, "f")
    return text.rstrip("0").rstrip(".") if "." in text else text


def _local_sampling(raw: object) -> dict:
    """local의 temperature·seed 외 세 값. 사전검사와 같은 정의다."""
    if not isinstance(raw, dict) or set(raw) != set(LOCAL_SAMPLING_FIELDS):
        _fail("environment.sampling", "INVALID_SAMPLING")
    return {
        "top_p": _bounded(raw["top_p"], "environment.sampling.top_p", 0, 1, low_open=True),
        "presence_penalty": _bounded(raw["presence_penalty"], "environment.sampling.presence_penalty",
                                     -MAX_PENALTY, MAX_PENALTY, low_open=False),
        "frequency_penalty": _bounded(raw["frequency_penalty"], "environment.sampling.frequency_penalty",
                                      -MAX_PENALTY, MAX_PENALTY, low_open=False),
    }


def _sampling(raw: object) -> dict:
    """temperature·seed 외 다섯 값. 하나라도 빠지면 제공자 기본값으로 조용히 돌아가므로 정확히 다섯 키를 요구한다."""
    if not isinstance(raw, dict) or set(raw) != set(SAMPLING_FIELDS):
        _fail("environment.sampling", "INVALID_SAMPLING")
    top_k = raw["top_k"]
    if type(top_k) is not int or not 1 <= top_k <= MAX_TOP_K:
        _fail("environment.sampling.top_k", "INVALID_NUMBER")
    return {
        "top_p": _bounded(raw["top_p"], "environment.sampling.top_p", 0, 1, low_open=True),
        "top_k": top_k,
        "min_p": _bounded(raw["min_p"], "environment.sampling.min_p", 0, 1, low_open=False),
        "presence_penalty": _bounded(raw["presence_penalty"], "environment.sampling.presence_penalty",
                                     -MAX_PENALTY, MAX_PENALTY, low_open=False),
        "repetition_penalty": _bounded(raw["repetition_penalty"], "environment.sampling.repetition_penalty",
                                       0, MAX_PENALTY, low_open=True),
    }


def _environment(raw: object) -> dict:
    raw = _object(raw, ("network_mode", "llm", "temperature", "sampling", "seed", "target_image",
                        "worker_count", "context_window_k", "time_budget_seconds", "request_budget", "run_storage",
                        "run_drain"),
                  "environment")
    if raw["network_mode"] not in _NETWORK_MODES:
        _fail("environment.network_mode", "UNSUPPORTED_NETWORK_MODE")
    if not _llm(raw["llm"]):
        _fail("environment.llm", "INVALID_LLM")
    if not _run_storage(raw["run_storage"]):
        _fail("environment.run_storage", "INVALID_RUN_STORAGE")
    if not _run_drain(raw["run_drain"]):
        _fail("environment.run_drain", "INVALID_RUN_DRAIN")
    window = raw["context_window_k"]
    if type(window) is not int or not MIN_CONTEXT_WINDOW_K <= window <= MAX_CONTEXT_WINDOW_K:
        _fail("environment.context_window_k", "INVALID_CONTEXT_WINDOW")
    # 로컬 서버는 넘친 입력을 오류 없이 잘라낸다. 엔진이 압축하기 전에 잘리면 안 된다.
    if raw["llm"]["backend"] == "local" and window * 1000 > raw["llm"]["context_length"]:
        _fail("environment.context_window_k", "ABOVE_SERVER_CONTEXT")
    return {
        "network_mode": raw["network_mode"],
        # API 고정 조건(모델·제공자·fallback)과 로컬 고정 IP가 모두 환경 지문에 들어간다.
        "llm": dict(raw["llm"]),
        "temperature": _number(raw["temperature"], "environment.temperature", positive=False),
        "sampling": (_local_sampling(raw["sampling"]) if raw["llm"]["backend"] == "local"
                     else _sampling(raw["sampling"])),
        "seed": _integer(raw["seed"], "environment.seed", zero=True),
        "target_image": _reference(raw["target_image"], "environment.target_image", image=True),
        "worker_count": _integer(raw["worker_count"], "environment.worker_count"),
        "context_window_k": window,
        "time_budget_seconds": _number(raw["time_budget_seconds"], "environment.time_budget_seconds", positive=True),
        "request_budget": _integer(raw["request_budget"], "environment.request_budget"),
        "run_storage": dict(raw["run_storage"]),
        "run_drain": dict(raw["run_drain"]),
    }

def _digest(kind: str, profile: dict) -> str:
    envelope = {"format": FORMAT, "kind": kind, "profile": profile}
    canonical = json.dumps(envelope, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False)
    return hashlib.sha256(canonical.encode("ascii")).hexdigest()


def generate_fingerprints(payload: object) -> ProfileFingerprints:
    """허용된 선언을 모두 검증한 후 지문화한다. 실환경 일치나 비밀 부재를 인증하지 않는다."""
    raw = _object(payload, ("version", "fixture", "environment", "scope"), "profile")
    if type(raw["version"]) is not int or raw["version"] != PROFILE_VERSION:
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
