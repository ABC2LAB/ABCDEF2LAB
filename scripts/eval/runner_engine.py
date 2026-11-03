"""평가 러너의 엔진 API 사용: 관리자 로그인, 작업 생성, findings 수집.

비밀번호는 러너 시작 때 운영자가 한 번 입력하고(getpass) 메모리에만 둔다. 토큰은 필요할 때마다 새로 받고
저장하지 않는다. 둘 다 argv·환경변수·docker 호출·회차 파일·로그·예외 문구에 넣지 않는다.
로그인·조회 대상은 호스트 루프백의 엔진 API front 하나다.

작업은 러너 설정의 내용으로 엔진 API(`POST /api/tasks`)가 만든다. 이 엔드포인트는 생성과 동시에 작업을 기동하고,
요청 구조체·핸들러·기동·대상 추출(parseTarget)이 upstream fdfb7a17·baseline e53a79f·개선 후에서 같다(테스트로 고정).
그래서 baseline·개선 후에 같은 요청 본문을 보내고 그 SHA-256을 회차 기록에 남긴다.

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
from dataclasses import asdict, dataclass
from pathlib import Path

from baseline_extract import MAX_VOCABULARY_BYTES, UPSTREAM_COMMIT, extract_baseline
from baseline_pages import BaselinePageMerge
from baseline_pages_collector import MAX_BEARER_LENGTH, BaselinePagesEndpoint, collect_baseline_pages

LOGIN_PATH = "/api/auth/login"
TASKS_PATH = "/api/tasks"
# 엔진이 고정한 관리자 계정 이름(server/auth.go authLogin). baseline·개선 후 같다.
ADMIN_USERNAME = "ARTEX"
MAX_LOGIN_RESPONSE_BYTES = 64 * 1024
MAX_PASSWORD_LENGTH = 1024
FINDINGS_FORMAT = "abc2lab-eval-runner-findings-v1"
MAX_TASK_RESPONSE_BYTES = 1024 * 1024
_TOKEN = re.compile(r"[A-Za-z0-9._~+/-]+=*\Z")
# 엔진 작업 ID(server/manager.go가 int64를 10진 문자열로 낸다).
_TASK_ID = re.compile(r"[1-9][0-9]{0,18}\Z")
# 엔진 parseTarget(server/server.go)의 정규식 그대로. Go regexp의 \b·\s는 ASCII 기준이라 re.ASCII로 맞춘다.
_TARGET_URL = re.compile(r"""https?://[^\s'"]+""", re.ASCII)
_TARGET_IP = re.compile(r"""\b((?:\d{1,3}\.){3}\d{1,3})(?::(\d{1,5}))?""", re.ASCII)
_TARGET_DOMAIN = re.compile(r"""\b((?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,})(?::(\d{1,5}))?""", re.ASCII)


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


def _post(port: int, path: str, body: bytes, headers: dict, timeout: float, limit: int) -> tuple[int, bytes]:
    """루프백 엔진 API에 POST 한 번. 환경 프록시·쿠키 없이 직접 연결하고 응답은 limit+1까지만 읽는다."""
    with closing(http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)) as connection:
        connection.set_debuglevel(0)
        connection.request("POST", path, body=body, headers={
            "Content-Type": "application/json", "Accept": "application/json", "Connection": "close", **headers})
        with closing(connection.getresponse()) as response:
            return response.status, response.read(limit + 1)


def login(port: int, secret: AdminSecret, timeout: float) -> str:
    """루프백 엔진 API에 로그인해 토큰을 돌려준다. 실패는 고정 코드다(본문을 읽어 옮기지 않는다)."""
    body = json.dumps({"username": ADMIN_USERNAME, "password": secret.reveal()}).encode("utf-8")
    try:
        status, data = _post(port, LOGIN_PATH, body, {}, timeout, MAX_LOGIN_RESPONSE_BYTES)
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


@dataclass(frozen=True)
class TaskRequest:
    """엔진에 보낼 작업 생성 본문(정규 JSON)과 그 SHA-256. 같은 설정이면 baseline·개선 후가 같은 바이트다."""

    body: bytes
    sha256: str


def task_request(task: dict, target_origin: str) -> TaskRequest:
    """러너 설정의 작업 내용을 생성 요청 본문으로 만든다.

    엔진은 설명+" "+목표에서 parseTarget으로 대상을 뽑아 자산으로 심는다. 그 규칙으로 보이는 대상이 회차의 대상
    origin 하나뿐이어야 한다: URL은 전부 그 origin(끝 '/' 허용)이고, URL 밖에 IP·도메인 꼴이 없어야 한다.
    """
    origin = target_origin.rstrip("/")
    text = task["description"] + " " + task["goal"]  # 엔진 createTask가 seed에 넘기는 문자열과 같은 꼴
    urls = _TARGET_URL.findall(text)
    if not urls or any(url not in (origin, origin + "/") for url in urls):
        raise EngineAPIError("task_target_mismatch")
    rest = _TARGET_URL.sub(" ", text)
    if _TARGET_IP.search(rest) or _TARGET_DOMAIN.search(rest):
        raise EngineAPIError("task_target_mismatch")
    body = json.dumps(task, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False).encode("ascii")
    return TaskRequest(body, hashlib.sha256(body).hexdigest())


def start_task(port: int, secret: AdminSecret, request: TaskRequest, timeout: float) -> str:
    """작업 직전에 새로 로그인하고 작업을 만든다(엔진은 생성과 동시에 기동). 작업 ID를 돌려준다.

    실패해도 다시 보내지 않는다. 시한 초과처럼 결과가 모호하면 엔진에 작업이 생겼을 수 있으므로 회차를 무효로 한다.
    """
    token = login(port, secret, timeout)
    try:
        status, data = _post(port, TASKS_PATH, request.body, {"Authorization": f"Bearer {token}"}, timeout,
                             MAX_TASK_RESPONSE_BYTES)
    except (OSError, http.client.HTTPException, ValueError, OverflowError):
        raise EngineAPIError("task_create_unreachable") from None
    if status != 201:
        raise EngineAPIError(f"task_create_http_{status}")
    try:
        if len(data) > MAX_TASK_RESPONSE_BYTES:
            raise ValueError
        task_id = json.loads(data.decode("utf-8"))["id"]
    except (UnicodeError, ValueError, KeyError, TypeError):
        raise EngineAPIError("task_create_response_invalid") from None
    if not isinstance(task_id, str) or not _TASK_ID.fullmatch(task_id):
        raise EngineAPIError("task_create_response_invalid")
    return task_id


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
