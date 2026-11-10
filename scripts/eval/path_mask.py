"""계측기 경로 가림(cmd/eval-meter paths.go, abc2lab.path-mask/v2)의 Python 판.

보존 집계가 설계상 막히는 경로 규칙을 기록과 같은 형태로 바꿀 때 쓴다. 계측기는 경로 조각을 원문으로 남기지 않고
`{id}`나 키 HMAC 가명으로 남기므로, 규칙도 같은 키로 같은 가림을 거쳐야 비교할 수 있다.
Go 시험 벡터(cmd/eval-meter/testdata/path-mask-v2.json)로 같은 값을 내는지 시험한다.
소문자 변환은 ASCII만 한다. 비ASCII 조각은 Go와 확장자 판정이 다를 수 있어서 규칙 경로에는 받지 않는다.
"""

from __future__ import annotations

import hashlib
import hmac
import os
import re
import stat

from runner_evidence import PATH_PROFILE  # noqa: F401 (가림 판 이름은 한 곳에서 관리한다)

KEY_BYTES = 32
_PSEUDONYM_HEX = 16
_MAX_SEGMENTS = 32
_MAX_SEGMENT_LENGTH = 32
_MASKED = "{id}"
_TOO_LONG = "/{too_long}"
_KEY_REF_LABEL = b"abc2lab.path-key-ref/v1"
# 정적 자원과 API를 나눠 셀 수 있게 확장자만 남긴다(paths.go staticSuffixes와 같다).
STATIC_SUFFIXES = (".js", ".mjs", ".css", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp",
                   ".woff", ".woff2", ".ttf", ".otf")

_ESCAPED = re.compile(rb"(?:[^%]|%[0-9A-Fa-f]{2})*\Z", re.S)
_ESCAPE = re.compile(rb"%([0-9A-Fa-f]{2})")
_SAFE = re.compile(rb"[A-Za-z0-9._~-]+\Z")
_DIGITS = re.compile(rb"[0-9]+\Z")
_HEX = re.compile(rb"[0-9a-fA-F]{16,}\Z")
_ENCODED = re.compile(rb"[A-Za-z0-9_-]{20,}\Z")


class PathKeyError(RuntimeError):
    pass


def read_path_key(path: str) -> bytes:
    """계측기 -path-key-file과 같은 조건(일반 파일·소유자·0600·32바이트)의 키만 읽는다. 값은 어디에도 쓰지 않는다."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK)
    except OSError:
        raise PathKeyError("path_key_unavailable") from None
    # fdopen 전에 연 fd로 확인한다(디렉터리는 fdopen에서 다른 예외로 샌다).
    info = os.fstat(fd)
    if (not stat.S_ISREG(info.st_mode) or info.st_mode & 0o7777 != 0o600 or info.st_uid != os.geteuid()
            or info.st_size != KEY_BYTES):
        os.close(fd)
        raise PathKeyError("path_key_untrusted")
    with os.fdopen(fd, "rb") as stream:
        key = stream.read(KEY_BYTES + 1)
    if len(key) != KEY_BYTES:
        raise PathKeyError("path_key_untrusted")
    return key


def key_ref(key: bytes) -> str:
    """경로 행의 path_key_ref(paths.go pathKeyRef)."""
    return hmac.new(key, _KEY_REF_LABEL, hashlib.sha256).hexdigest()[:_PSEUDONYM_HEX]


def _static_suffix(segment: bytes) -> str:
    lower = segment.lower()
    for suffix in STATIC_SUFFIXES:
        if lower.endswith(suffix.encode("ascii")):
            return suffix
    return ""


def _looks_encoded(segment: bytes) -> bool:
    return bool(_ENCODED.match(segment)) and (
        any(48 <= byte <= 57 for byte in segment) or segment.lower() != segment and segment.upper() != segment)


def _mask_segment(raw: bytes, key: bytes) -> tuple[bytes, bool]:
    if not raw:
        return b"", False
    # Go url.PathUnescape: %XX가 아닌 %가 있으면 오류다. +는 그대로 둔다.
    if not _ESCAPED.match(raw):
        return _MASKED.encode() + _static_suffix(raw).encode(), True
    segment = _ESCAPE.sub(lambda m: bytes([int(m.group(1), 16)]), raw)
    if (len(segment) > _MAX_SEGMENT_LENGTH or not _SAFE.match(segment) or _DIGITS.match(segment)
            or _HEX.match(segment) or _looks_encoded(segment)):
        return _MASKED.encode() + _static_suffix(segment).encode(), True
    digest = hmac.new(key, segment, hashlib.sha256).hexdigest()[:_PSEUDONYM_HEX]
    return f"{{h:{digest}}}{_static_suffix(segment)}".encode(), False


def mask_path(escaped: str, key: bytes) -> tuple[str, int]:
    """paths.go maskPath와 같은 (패턴, 가린 조각 수)."""
    if len(key) != KEY_BYTES:
        raise PathKeyError("path_key_untrusted")
    segments = escaped.encode("utf-8").split(b"/")
    values = sum(1 for segment in segments if segment)
    if values > _MAX_SEGMENTS:
        return _TOO_LONG, values
    masked = 0
    out = []
    for raw in segments:
        segment, hit = _mask_segment(raw, key)
        out.append(segment)
        masked += hit
    return b"/".join(out).decode("utf-8"), masked
