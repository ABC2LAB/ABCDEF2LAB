"""평가 러너의 엔진 API 사용: 관리자 로그인과 findings 수집.

비밀번호는 러너 시작 때 운영자가 한 번 입력하고(getpass) 메모리에만 둔다. 토큰은 필요할 때마다 새로 받고
저장하지 않는다. 둘 다 argv·환경변수·docker 호출·회차 파일·로그·예외 문구에 넣지 않는다.
로그인·조회 대상은 호스트 루프백의 엔진 API front 하나다.

findings는 기존 수집기·추출기를 그대로 쓴다. 추출기는 원본 FindingDTO 형식(upstream fdfb7a17)으로 고정돼
있고, 개선 후 엔진의 FindingDTO·FindingAssetDTO·db.FindingTrafficBinding 정의와 목록·로그인 핸들러가
그 커밋과 같다(테스트로 고정). 그래서 baseline·개선 후 모두 같은 추출 경로에 DTO 형식 기준으로 fdfb7a17을
넘긴다. 이것은 형식 기준이지 빌드 출처가 아니므로, 기록에는 형식 기준(dto_format_source)과 빌드 출처
(build_source)를 따로 남긴다. 개선 후 엔진의 판정 레코드는 물리 형식이 정해진 뒤 별도 신호로 붙인다.
"""

from __future__ import annotations

import hashlib
import http.client
import json
import re
from contextlib import closing
from dataclasses import asdict
from pathlib import Path

from baseline_extract import MAX_VOCABULARY_BYTES, UPSTREAM_COMMIT, extract_baseline
from baseline_pages import BaselinePageMerge
from baseline_pages_collector import MAX_BEARER_LENGTH, BaselinePagesEndpoint, collect_baseline_pages

LOGIN_PATH = "/api/auth/login"
# 엔진이 고정한 관리자 계정 이름(server/auth.go authLogin). baseline·개선 후 같다.
ADMIN_USERNAME = "ARTEX"
MAX_LOGIN_RESPONSE_BYTES = 64 * 1024
MAX_PASSWORD_LENGTH = 1024
FINDINGS_FORMAT = "abc2lab-eval-runner-findings-v1"
_TOKEN = re.compile(r"[A-Za-z0-9._~+/-]+=*\Z")


class EngineAPIError(RuntimeError):
    """엔진 API 사용 실패. 고정 코드만 담고 응답 본문·비밀번호·토큰은 담지 않는다."""


class AdminSecret:
    """관리자 비밀번호를 담기만 한다. repr·str로 값이 드러나지 않는다."""

    __slots__ = ("_value",)

    def __init__(self, value: str):
        if (not isinstance(value, str) or not value or len(value) > MAX_PASSWORD_LENGTH
                or any(ord(char) < 32 or ord(char) == 127 for char in value)):
            raise EngineAPIError("engine_password_invalid")
        self._value = value

    def __repr__(self) -> str:
        return "AdminSecret(<redacted>)"

    __str__ = __repr__

    def reveal(self) -> str:
        return self._value


def login(port: int, secret: AdminSecret, timeout: float) -> str:
    """루프백 엔진 API에 로그인해 토큰을 돌려준다. 실패는 고정 코드다(본문을 읽어 옮기지 않는다)."""
    body = json.dumps({"username": ADMIN_USERNAME, "password": secret.reveal()}).encode("utf-8")
    try:
        # 환경 프록시·쿠키 없이 루프백에 직접 연결한다.
        with closing(http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)) as connection:
            connection.set_debuglevel(0)
            connection.request("POST", LOGIN_PATH, body=body, headers={
                "Content-Type": "application/json", "Accept": "application/json", "Connection": "close"})
            with closing(connection.getresponse()) as response:
                status = response.status
                data = response.read(MAX_LOGIN_RESPONSE_BYTES + 1)
    except (OSError, http.client.HTTPException, ValueError, OverflowError):
        raise EngineAPIError("engine_login_unreachable") from None
    if status != 200:
        raise EngineAPIError(f"engine_login_http_{status}")
    try:
        if len(data) > MAX_LOGIN_RESPONSE_BYTES:
            raise ValueError
        token = json.loads(data.decode("utf-8"))["token"]
    except (UnicodeError, ValueError, KeyError, TypeError):
        raise EngineAPIError("engine_login_response_invalid") from None
    if not isinstance(token, str) or not 0 < len(token) <= MAX_BEARER_LENGTH or not _TOKEN.fullmatch(token):
        raise EngineAPIError("engine_login_response_invalid")
    return token


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise EngineAPIError("findings_vocabulary_invalid")
        result[key] = value
    return result


def load_vocabulary(path: str | Path) -> tuple[dict, str]:
    """평가자가 검토한 분류 매핑. baseline·개선 후가 같은 매핑을 써야 하므로 원본 SHA-256을 함께 돌려준다."""
    try:
        with open(path, "rb") as source:
            raw = source.read(MAX_VOCABULARY_BYTES + 1)
        if len(raw) > MAX_VOCABULARY_BYTES:
            raise EngineAPIError("findings_vocabulary_invalid")
        vocabulary = json.loads(raw.decode("utf-8"), object_pairs_hook=_unique_object)
        extract_baseline([], vocabulary, source_sha=UPSTREAM_COMMIT)  # 송신 전에 매핑부터 검증
    except EngineAPIError:
        raise
    except Exception:
        raise EngineAPIError("findings_vocabulary_invalid") from None
    return vocabulary, hashlib.sha256(raw).hexdigest()


def findings_record(merged: BaselinePageMerge, *, side: str, build_source: str, vocabulary_sha256: str) -> dict:
    """비민감 투영만 담는다(추출기가 고른 필드). 추출기의 declared_source_sha는 형식 기준일 뿐이다."""
    if merged.declared_source_sha != UPSTREAM_COMMIT:
        raise EngineAPIError("findings_format_unexpected")
    return {
        "format": FINDINGS_FORMAT, "side": side,
        "dto_format_source": UPSTREAM_COMMIT, "build_source": build_source,
        "vocabulary_sha256": vocabulary_sha256, "declared_total": merged.declared_total,
        "page_size": merged.page_size, "page_count": merged.page_count,
        "findings": [asdict(finding) for finding in merged.findings],
    }


def collect_findings(port: int, secret: AdminSecret, settings: dict, vocabulary: dict, vocabulary_sha256: str, *,
                     side: str, build_source: str) -> dict:
    """findings 직전에 새로 로그인하고, 토큰은 이 호출 안에서만 쓴다."""
    token = login(port, secret, settings["timeout_seconds"])
    endpoint = BaselinePagesEndpoint(origin=f"http://127.0.0.1:{port}", page_size=settings["page_size"],
                                     timeout_seconds=settings["timeout_seconds"],
                                     max_page_bytes=settings["max_page_bytes"],
                                     max_total_bytes=settings["max_total_bytes"], max_pages=settings["max_pages"])
    merged = collect_baseline_pages(endpoint, vocabulary, source_sha=UPSTREAM_COMMIT, bearer_token=token)
    return findings_record(merged, side=side, build_source=build_source, vocabulary_sha256=vocabulary_sha256)
