"""E③ 판정 레코드 정본 바이트(저장 게이트 §5.5, v1)의 평가 쪽 구현. 파일·망 I/O는 없다.

Go `e3record`와 같은 공용 벡터(`e3record/testdata/canonical-v1.json`)로 묶는다. 판정 페이지를 수집할 때
레코드의 해시를 평가 쪽에서 다시 계산해 대조하려면 두 구현이 바이트 단위로 같아야 한다.

규칙(벡터 파일 `rules`): UTF-8, 객체 키는 Unicode 코드포인트 순, 압축 구분자, ASCII 이스케이프 없음,
Unicode 정규화 없음, 배열 순서 유지, 정수는 signed int64만. 실수·NaN·Inf·잘못된 UTF-8은 거부하고, 정본은
65536바이트까지다. 거부는 고정 코드 하나로만 알린다(입력 값을 메시지에 넣지 않음).
"""

from __future__ import annotations

import hashlib
import json
import re
from datetime import datetime, timedelta, timezone

MAX_CANONICAL_BYTES = 65536
REJECTED = "e3_record_invalid"
_INT64_MIN, _INT64_MAX = -(1 << 63), (1 << 63) - 1
# RFC3339 instant(v1 허용 범위). 대문자 T·Z, ASCII 숫자만 받는다(정규식 \d는 전각 등 다른 숫자도 받아서 쓰지 않음).
# offset은 RFC3339 §5.6 범위(시 00–23, 분 00–59)만 받는다. Go time.Parse·JSON 디코딩은 +24:00·+00:60도
# 받지만(더 관대함) v1 입력 범위 밖이라 평가 쪽은 거부한다. Go가 거부하는 +00:99·+99:00·비ASCII 숫자는 양쪽 다 거부.
_RFC3339 = re.compile(
    r"([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?(Z|[+-][0-9]{2}:[0-9]{2})\Z")
_MAX_OFFSET_HOURS, _MAX_OFFSET_MINUTES = 23, 59


class CanonicalError(ValueError):
    """정본으로 만들 수 없는 입력. 메시지는 고정 코드뿐이다."""

    def __init__(self) -> None:
        super().__init__(REJECTED)


def canonical_bytes(value: object) -> bytes:
    """JSON 값의 정본 UTF-8 바이트."""
    _check(value)
    text = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"), allow_nan=False)
    try:
        data = text.encode("utf-8")
    except UnicodeEncodeError:  # 짝 없는 surrogate: 올바른 UTF-8로 만들 수 없다
        raise CanonicalError() from None
    if len(data) > MAX_CANONICAL_BYTES:
        raise CanonicalError()
    return data


def canonical_sha256(value: object) -> str:
    return hashlib.sha256(canonical_bytes(value)).hexdigest()


def canonical_time(text: str) -> str:
    """RFC3339 시각을 UTC·마이크로초 6자리·Z 형식으로 바꾼다(1마이크로초 미만은 버림)."""
    match = _RFC3339.fullmatch(text) if isinstance(text, str) else None
    if match is None:
        raise CanonicalError()
    year, month, day, hour, minute, second, fraction, zone = match.groups()
    micro = int((fraction or "").ljust(6, "0")[:6])
    hours, minutes = (0, 0) if zone == "Z" else (int(zone[1:3]), int(zone[4:6]))
    # 범위 밖 offset을 timedelta로 넘기면 분이 시로 이월돼 다른 정상 시각이 된다(+00:99 → 1시간 39분).
    if hours > _MAX_OFFSET_HOURS or minutes > _MAX_OFFSET_MINUTES:
        raise CanonicalError()
    # 시 상한은 timezone()도 막지만(±24시간 미만) v1 범위를 드러내려고 같이 둔다.
    offset = (-1 if zone[0] == "-" else 1) * timedelta(hours=hours, minutes=minutes)
    try:
        local = datetime(int(year), int(month), int(day), int(hour), int(minute), int(second), micro,
                         tzinfo=timezone(offset))
        utc = local.astimezone(timezone.utc)
    except (ValueError, OverflowError):
        # 범위 밖(예: 0001-01-01의 양수 offset)은 Go와 표현이 갈릴 수 있어 정본으로 만들지 않는다.
        raise CanonicalError() from None
    # strftime의 %Y는 플랫폼에 따라 1년을 "1"로 찍어 직접 0을 채운다(공용 벡터 utc-first-year).
    return (f"{utc.year:04d}-{utc.month:02d}-{utc.day:02d}T{utc.hour:02d}:{utc.minute:02d}:{utc.second:02d}"
            f".{utc.microsecond:06d}Z")


def loads_exact(text: str | bytes) -> object:
    """수신 JSON을 정수 손실 없이 읽는다. 잘못된 UTF-8·실수·NaN·Inf·중복 키는 거부한다."""
    if isinstance(text, (bytes, bytearray)):
        try:
            text = bytes(text).decode("utf-8")
        except UnicodeDecodeError:  # 대체 문자로 바꾸기 전에 거부한다
            raise CanonicalError() from None

    def reject(_value: str) -> object:
        raise CanonicalError()

    def unique(pairs: list[tuple[str, object]]) -> dict:
        result: dict = {}
        for key, item in pairs:
            if key in result:
                raise CanonicalError()
            result[key] = item
        return result

    try:
        return json.loads(text, parse_float=reject, parse_constant=reject, object_pairs_hook=unique)
    except json.JSONDecodeError:
        raise CanonicalError() from None


def _check(value: object) -> None:
    if value is None or isinstance(value, (bool, str)):
        return
    if isinstance(value, int):
        if not _INT64_MIN <= value <= _INT64_MAX:
            raise CanonicalError()
        return
    if isinstance(value, list):
        for item in value:
            _check(item)
        return
    if isinstance(value, dict):
        for key, item in value.items():
            if not isinstance(key, str):
                raise CanonicalError()
            _check(item)
        return
    raise CanonicalError()  # float·tuple·bytes 등 정본에 없는 타입
