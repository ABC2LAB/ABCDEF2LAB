"""등록된 loopback ARTEX의 무필터 전역 findings를 Bearer GET으로 수집한다.

토큰·헤더·원본 body는 반환/저장/로그에 넣지 않는다. 페이지 JSON은 내부 메모리에서
검증·병합하고 비민감 BaselinePageMerge만 반환한다. 실제 출처/회차/완전 수집은 인증하지 않는다.
"""

from __future__ import annotations

import copy
import http.client
import json
import re
import sys
from contextlib import closing
from dataclasses import dataclass
from urllib.parse import urlencode

from baseline_extract import (
    MAX_INPUT_BYTES, MAX_VOCABULARY_BYTES, UPSTREAM_COMMIT, _finite_float,
    _reject_constant, _unique_object, extract_baseline,
)
from baseline_pages import BaselinePageMerge, merge_baseline_pages
from snapshot_collector import SnapshotEndpoint, _endpoint, _read_response

FINDINGS_PATH = "/api/exploration/findings"
MAX_PAGE_SIZE = 200
MAX_BEARER_LENGTH = 8192
_BEARER = re.compile(r"[A-Za-z0-9._~+/-]+=*\Z")


class BaselineCollectionError(ValueError):
    """원문 대신 고정 코드만 출력할 수 있는 수집 오류."""


@dataclass(frozen=True)
class BaselinePagesEndpoint:
    origin: str
    page_size: int
    timeout_seconds: float
    max_page_bytes: int
    max_total_bytes: int
    max_pages: int


def _fail(code: str) -> None:
    raise BaselineCollectionError(code)


def _configuration(config: BaselinePagesEndpoint, source_sha: str, token: str,
                   vocabulary: object) -> tuple[tuple[str, int, str], dict]:
    if not isinstance(config, BaselinePagesEndpoint):
        _fail("INVALID_CONFIG")
    if source_sha != UPSTREAM_COMMIT:
        _fail("UNSUPPORTED_SOURCE")
    if (not isinstance(token, str) or not 0 < len(token) <= MAX_BEARER_LENGTH
            or not _BEARER.fullmatch(token)):
        _fail("INVALID_BEARER")
    if type(config.page_size) is not int or not 1 <= config.page_size <= MAX_PAGE_SIZE:
        _fail("INVALID_PAGE_SIZE")
    if type(config.max_pages) is not int or not 0 < config.max_pages <= sys.maxsize:
        _fail("INVALID_PAGE_CAP")
    for limit in (config.max_page_bytes, config.max_total_bytes):
        if type(limit) is not int or not 0 < limit <= MAX_INPUT_BYTES:
            _fail("INVALID_BYTE_CAP")
    endpoint = _endpoint(SnapshotEndpoint(config.origin, FINDINGS_PATH, config.timeout_seconds,
                                          config.max_page_bytes))
    # 직렬화된 사본만 사용해 호출자가 수집 도중 바꾼 매핑이 뒤 페이지에 섞이지 않게 한다.
    copied_vocabulary = copy.deepcopy(vocabulary)
    extract_baseline([], copied_vocabulary, source_sha=source_sha)
    encoded = json.dumps(copied_vocabulary, ensure_ascii=True, allow_nan=False).encode("ascii")
    if len(encoded) > MAX_VOCABULARY_BYTES:
        _fail("VOCABULARY_TOO_LARGE")
    fixed_vocabulary = json.loads(encoded, object_pairs_hook=_unique_object)
    extract_baseline([], fixed_vocabulary, source_sha=source_sha)
    return endpoint, fixed_vocabulary


def _request(config: BaselinePagesEndpoint, endpoint: tuple[str, int, str], token: str,
             number: int, maximum: int) -> bytes:
    host, port, host_header = endpoint
    path = FINDINGS_PATH + "?" + urlencode({"page": number, "limit": config.page_size})
    with closing(http.client.HTTPConnection(host, port, timeout=config.timeout_seconds)) as connection:
        # 프로세스 전역 debuglevel 설정도 Authorization을 출력하지 못하게 한다.
        connection.set_debuglevel(0)
        connection.request("GET", path, headers={
            "Host": host_header, "Accept": "application/json", "Accept-Encoding": "identity",
            "Connection": "close", "Authorization": "Bearer " + token,
        })
        with closing(connection.getresponse()) as response:
            return _read_response(response, maximum)


def collect_baseline_pages(config: BaselinePagesEndpoint, vocabulary: object, *,
                           source_sha: str, bearer_token: str) -> BaselinePageMerge:
    """전역 페이지를 내부 메모리에서 모아 검증/병합한다. 실패 시 부분 결과가 없다.

    max_page_bytes는 framing 포함 wire 상한, max_total_bytes는 decoded body 합계
    상한이다. 잔여 합계를 wire 읽기에도 적용하므로 chunked 경계에서는 보수적으로
    거부할 수 있다. timeout_seconds는 socket 비활성 제한이며 전체 실행 예산은 아니다.
    """
    try:
        endpoint, fixed_vocabulary = _configuration(config, source_sha, bearer_token, vocabulary)
    except BaselineCollectionError:
        raise
    except (ValueError, TypeError, RuntimeError, OverflowError):
        raise BaselineCollectionError("INVALID_CONFIGURATION") from None

    pages, total, consumed, number, expected_pages = [], None, 0, 1, 1
    while number <= expected_pages:
        remaining = config.max_total_bytes - consumed
        if remaining <= 0:
            _fail("TOTAL_BYTE_CAP_EXCEEDED")
        try:
            raw = _request(config, endpoint, bearer_token, number, min(config.max_page_bytes, remaining))
        except (OSError, http.client.HTTPException, ValueError, RuntimeError, OverflowError):
            raise BaselineCollectionError("HTTP_RESPONSE_FAILED") from None
        consumed += len(raw)
        if consumed > config.max_total_bytes:
            _fail("TOTAL_BYTE_CAP_EXCEEDED")
        try:
            payload = json.loads(raw.decode("utf-8"), object_pairs_hook=_unique_object,
                                 parse_float=_finite_float, parse_constant=_reject_constant)
            if not isinstance(payload, dict) or "items" not in payload:
                _fail("EXPECTED_PAGE_OBJECT")
            extracted = extract_baseline(payload, fixed_vocabulary, source_sha=source_sha)
        except BaselineCollectionError:
            raise
        except (ValueError, TypeError, RuntimeError, OverflowError):
            raise BaselineCollectionError("INVALID_PAGE_JSON_OR_DTO") from None
        page = extracted.pagination
        if page.page != number or page.page_size != config.page_size:
            _fail("PAGE_ECHO_MISMATCH")
        if total is None:
            total = page.total
            expected_pages = max(1, (total + config.page_size - 1) // config.page_size)
            if expected_pages > config.max_pages:
                _fail("PAGE_CAP_EXCEEDED")
        elif page.total != total:
            _fail("PAGINATION_CHANGED")
        expected_length = min(config.page_size, max(0, total - (number - 1) * config.page_size))
        if len(extracted.findings) != expected_length:
            _fail("PAGE_LENGTH_MISMATCH")
        pages.append(payload)
        number += 1
    try:
        return merge_baseline_pages(pages, fixed_vocabulary, source_sha=source_sha)
    except (ValueError, TypeError, RuntimeError, OverflowError):
        raise BaselineCollectionError("INVALID_PAGE_SET") from None
