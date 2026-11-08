"""KG fixture 레지스트리의 정규 바이트·digest·policy version (Go `kgfixture`와 같은 규칙).

레지스트리 형식 검사(스키마)는 Go 로더 몫이다. 여기는 평가 쪽 도구가 같은 digest를 계산할 수
있도록 디코딩 규칙과 정규화만 옮긴다. 두 구현이 같은지는 공용 벡터
`kgfixture/testdata/vectors-v1.json`으로 고정한다(docs/contracts/kg-fixture-registry.md §3).

- 키·문자열은 출력 가능한 ASCII(0x20~0x7E)만. 이스케이프를 푼 뒤 같은 키가 두 번이면 거부.
- 숫자는 정수 표기만(소수·지수 거부), NaN·Infinity 거부.
- 정규 JSON = 키 정렬·공백 없음·`"`와 `\\`만 이스케이프(ASCII만이라 json.dumps ensure_ascii와 같음).
"""

from __future__ import annotations

import hashlib
import json
import re

POLICY_VERSION_PREFIX = "kg-fixture/v1:"

_INTEGER = re.compile(r"-?(0|[1-9][0-9]*)\Z")


class RegistryDecodeError(ValueError):
    """디코딩 규칙 위반. 메시지는 고정 코드만(파일 내용을 넣지 않음)."""


def _printable(text: str) -> bool:
    return all(0x20 <= ord(ch) <= 0x7E for ch in text)


def _object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    out: dict[str, object] = {}
    for key, value in pairs:
        if not _printable(key):
            raise RegistryDecodeError("registry_format")
        if key in out:
            raise RegistryDecodeError("registry_duplicate")
        out[key] = value
    return out


def _reject_number(_: str) -> object:
    raise RegistryDecodeError("registry_format")


def _integer(text: str) -> int:
    if not _INTEGER.match(text):
        raise RegistryDecodeError("registry_format")
    return int(text)


def _check_strings(value: object) -> None:
    if isinstance(value, str):
        if not _printable(value):
            raise RegistryDecodeError("registry_format")
    elif isinstance(value, dict):
        for item in value.values():
            _check_strings(item)
    elif isinstance(value, list):
        for item in value:
            _check_strings(item)


def strict_load(data: bytes) -> dict[str, object]:
    """레지스트리 바이트를 디코딩한다. 최상위는 객체여야 한다."""
    try:
        text = data.decode("utf-8")
        value = json.loads(text, object_pairs_hook=_object, parse_float=_reject_number,
                           parse_int=_integer, parse_constant=_reject_number)
    except RegistryDecodeError:
        raise
    except (UnicodeDecodeError, ValueError):
        raise RegistryDecodeError("registry_format") from None
    if not isinstance(value, dict):
        raise RegistryDecodeError("registry_format")
    _check_strings(value)
    return value


def canonical_bytes(value: dict[str, object]) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")


def digest(data: bytes) -> str:
    return hashlib.sha256(canonical_bytes(strict_load(data))).hexdigest()


def policy_version(snapshot_id: str, digest_hex: str) -> str:
    return f"{POLICY_VERSION_PREFIX}{snapshot_id}:{digest_hex}"
