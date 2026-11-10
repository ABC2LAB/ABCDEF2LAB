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
from urllib.parse import quote, urlencode

from baseline_extract import MAX_VOCABULARY_BYTES, UPSTREAM_COMMIT, extract_baseline
from baseline_pages import BaselinePageMerge
from baseline_pages_collector import MAX_BEARER_LENGTH, BaselinePagesEndpoint, collect_baseline_pages
from e2_prompt_collect import MAX_PROJECTION_BYTES, E2PromptNotReady, E2PromptUnverified

LOGIN_PATH = "/api/auth/login"
TASKS_PATH = "/api/tasks"
# 작업 생성 전에 읽는 실행 설정. 두 응답은 baseline(e53a79f)과 개선 후가 같은 코드로 만든다(평가 프로토콜 §6).
SETTINGS_PATHS = {"settings": "/api/settings", "llm": "/api/llm"}
LLM_PROFILES_PATH = "/api/llm/profiles"
LLM_ACTIVE_PROFILE_PATH = "/api/llm/profiles/active"
ENGINE_PROFILE_NAME = "default"
MAX_SETTINGS_RESPONSE_BYTES = 64 * 1024
# 프록시 URL에는 자격정보가 들어갈 수 있어 값 대신 설정 여부만 남긴다. 평가 회차에서는 비어 있어야 한다
# (설정되면 엔진 송신 경로가 계측기·게이트웨이 밖으로 바뀐다).
PROXY_FIELDS = {"settings": ("web_search_proxy", "global_proxy"), "llm": ("proxy",)}
# 엔진이 고정한 관리자 계정 이름(server/auth.go authLogin). baseline·개선 후 같다.
ADMIN_USERNAME = "ARTEX"
MAX_LOGIN_RESPONSE_BYTES = 64 * 1024
MAX_PASSWORD_LENGTH = 1024
FINDINGS_FORMAT = "abc2lab-eval-runner-findings-v1"
MAX_TASK_RESPONSE_BYTES = 1024 * 1024
INTERCEPT_RULES_PATH = "/api/intercept/rules"
INTERCEPT_TOOLS_PATH = "/api/intercept/tool-config"
RULES_FORMAT = "abc2lab.engine-intercept-rules/v1"
RULES_EVIDENCE_FORMAT = "abc2lab-eval-engine-rules-v1"
MAX_RULES_FILE_BYTES = 1024 * 1024
MAX_RULES = 512
MAX_RULES_RESPONSE_BYTES = 4 * 1024 * 1024
_RULES_FILE_FIELDS = {"format", "enabled_tools", "rules"}
_RULE_FIELDS = ("name", "priority", "match_target", "match_type", "pattern", "action", "message")
_RULE_TARGETS = ("tool_name", "tool_input")
_RULE_TYPES = ("string", "regex")
# ask는 사람이 답해야 끝나므로 자동 회차에 쓸 수 없다.
_RULE_ACTIONS = ("allow", "deny")
RULE_TIMEOUT_SECONDS = 60
RULE_TIMEOUT_ACTION = "deny"
_RULE_VIEW_FIELDS = (*_RULE_FIELDS, "enabled", "timeout_enabled", "timeout_seconds", "timeout_action")
INTERCEPT_HISTORY_PATH = "/api/intercept/history"
INTERCEPT_SUMMARY_FORMAT = "abc2lab-eval-intercept-summary-v1"
INTERCEPT_PAGE_SIZE = 100  # 원본 API가 받는 최대 쪽 크기
MAX_INTERCEPT_PAGES = 1000
MAX_INTERCEPT_PAGE_BYTES = 32 * 1024 * 1024  # 행마다 도구 입력 원문이 들어 있어 크다. 세기만 하고 버린다
_INTERCEPT_STATUSES = ("allowed", "denied", "pending", "timeout")
_INTERCEPT_TOOL = re.compile(r"[A-Za-z0-9_.:-]{1,64}\Z")
OTHER_TOOL = "OTHER"
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


def _put(port: int, path: str, body: bytes, headers: dict, timeout: float, limit: int) -> tuple[int, bytes]:
    """루프백 엔진 API에 PUT 한 번. _post와 같은 연결 규칙이다."""
    with closing(http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)) as connection:
        connection.set_debuglevel(0)
        connection.request("PUT", path, body=body, headers={
            "Content-Type": "application/json", "Accept": "application/json", "Connection": "close", **headers})
        with closing(connection.getresponse()) as response:
            return response.status, response.read(limit + 1)


def _get(port: int, path: str, headers: dict, timeout: float, limit: int) -> tuple[int, bytes]:
    """루프백 엔진 API에 GET 한 번. _post와 같은 연결 규칙이다."""
    with closing(http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)) as connection:
        connection.set_debuglevel(0)
        connection.request("GET", path, headers={"Accept": "application/json", "Connection": "close", **headers})
        with closing(connection.getresponse()) as response:
            return response.status, response.read(limit + 1)


def e2_prompt_projection(port: int, secret: AdminSecret, task_id: str, run_ref: str, timeout: float) -> bytes:
    """실제 E② run_ref의 프롬프트 투영을 받은 바이트 그대로 돌려준다. 해석은 e2_prompt_collect가 한다."""
    try:
        token = login(port, secret, timeout)
        status, data = _get(port, f"/api/tasks/{quote(task_id, safe='')}/e2-prompt-projection?{urlencode({'run_ref': run_ref})}",
                            {"Authorization": "Bearer " + token}, timeout, MAX_PROJECTION_BYTES)
    except (EngineAPIError, OSError, http.client.HTTPException, ValueError, OverflowError):
        raise E2PromptNotReady("engine_unreachable") from None
    if status == 503:
        raise E2PromptNotReady("engine_unavailable")
    if status != 200:
        raise E2PromptUnverified(f"http_{status}")
    return data


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


def _settings_object(pairs: list[tuple[str, object]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate key")
        result[key] = value
    return result


def engine_settings(port: int, secret: AdminSecret, timeout: float) -> dict:
    """작업 생성 전 엔진 실행 설정 스냅샷. 값은 평평한 JSON 스칼라만 받고 프록시 URL은 설정 여부로 바꾼다.

    API 키는 응답에 없다(설정 여부 bool만 나온다). 모르는 형태가 오면 고정 코드로 거부한다.
    """
    token = login(port, secret, timeout)
    snapshot = {}
    for name, path in SETTINGS_PATHS.items():
        try:
            status, data = _get(port, path, {"Authorization": f"Bearer {token}"}, timeout, MAX_SETTINGS_RESPONSE_BYTES)
        except (OSError, http.client.HTTPException, ValueError, OverflowError):
            raise EngineAPIError("engine_settings_unreachable") from None
        if status != 200:
            raise EngineAPIError(f"engine_settings_http_{status}")
        try:
            if len(data) > MAX_SETTINGS_RESPONSE_BYTES:
                raise ValueError
            values = json.loads(data.decode("utf-8"), object_pairs_hook=_settings_object)
        except (UnicodeError, ValueError):
            raise EngineAPIError("engine_settings_response_invalid") from None
        if (not isinstance(values, dict) or not values
                or any(not isinstance(value, (str, bool, int, float)) and value is not None
                       for value in values.values())):
            raise EngineAPIError("engine_settings_response_invalid")
        for field in PROXY_FIELDS[name]:
            if not isinstance(values.get(field), str):
                raise EngineAPIError("engine_settings_response_invalid")
            values[f"{field}_set"] = values.pop(field).strip() != ""
        snapshot[name] = values
    return snapshot


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


@dataclass(frozen=True)
class EngineRules:
    """원본 ② 규칙 파일(프로토콜 §8.1). 내용은 규칙 담당이 챌린지 목록을 보지 않고 쓰고, 러너는 형식만 본다."""
    sha256: str
    rules: tuple[dict, ...]
    enabled_tools: tuple[str, ...] | None  # None이면 엔진 기본 도구 목록을 그대로 둔다


def load_engine_rules(path: str | Path) -> EngineRules:
    """규칙 파일을 읽어 형식을 검사한다. 정규식 문법은 엔진(RE2)이 등록 때 본다."""
    try:
        with open(path, "rb") as source:
            data = source.read(MAX_RULES_FILE_BYTES + 1)
    except OSError:
        raise EngineAPIError("engine_rules_file_unreadable") from None
    try:
        if len(data) > MAX_RULES_FILE_BYTES:
            raise ValueError
        payload = json.loads(data.decode("utf-8"), object_pairs_hook=_settings_object)
    except (UnicodeError, ValueError):
        raise EngineAPIError("engine_rules_file_invalid") from None
    if (not isinstance(payload, dict) or set(payload) != _RULES_FILE_FIELDS or payload["format"] != RULES_FORMAT
            or not _rules_valid(payload["rules"]) or not _tools_valid(payload["enabled_tools"])):
        raise EngineAPIError("engine_rules_file_invalid")
    tools = payload["enabled_tools"]
    return EngineRules(hashlib.sha256(data).hexdigest(), tuple(payload["rules"]), None if tools is None else tuple(tools))


def _rule_valid(rule: object) -> bool:
    return (isinstance(rule, dict) and set(rule) == set(_RULE_FIELDS)
            and isinstance(rule["name"], str) and bool(rule["name"]) and type(rule["priority"]) is int
            and rule["match_target"] in _RULE_TARGETS and rule["match_type"] in _RULE_TYPES
            and isinstance(rule["pattern"], str) and bool(rule["pattern"]) and rule["action"] in _RULE_ACTIONS
            and isinstance(rule["message"], str))


def _rules_valid(rules: object) -> bool:
    if not isinstance(rules, list) or not 0 < len(rules) <= MAX_RULES or not all(_rule_valid(rule) for rule in rules):
        return False
    return len({rule["name"] for rule in rules}) == len(rules)  # 등록 뒤 이름으로 맞춰 본다


def _tools_valid(tools: object) -> bool:
    if tools is None:
        return True
    return (isinstance(tools, list) and bool(tools) and all(isinstance(tool, str) and bool(tool) for tool in tools)
            and len(set(tools)) == len(tools))


def _rule_view(rule: object) -> dict:
    """비교·해시에 쓰는 규칙 내용. 엔진이 매기는 id·시각은 뺀다. 모르는 형태면 멈춘다."""
    if not isinstance(rule, dict) or any(field not in rule for field in _RULE_VIEW_FIELDS):
        raise EngineAPIError("engine_rules_response_invalid")
    return {field: rule[field] for field in _RULE_VIEW_FIELDS}


def _engine_json(status_data: tuple[int, bytes], expected: int, limit: int) -> object:
    status, data = status_data
    if status != expected:
        raise EngineAPIError(f"engine_rules_http_{status}")
    try:
        if len(data) > limit:
            raise ValueError
        return json.loads(data.decode("utf-8"))
    except (UnicodeError, ValueError):
        raise EngineAPIError("engine_rules_response_invalid") from None


def _active_rules(port: int, headers: dict, timeout: float) -> list[dict]:
    payload = _engine_json(_get(port, INTERCEPT_RULES_PATH, headers, timeout, MAX_RULES_RESPONSE_BYTES), 200,
                           MAX_RULES_RESPONSE_BYTES)
    if not isinstance(payload, dict) or not isinstance(payload.get("rules"), list):
        raise EngineAPIError("engine_rules_response_invalid")
    return [_rule_view(rule) for rule in payload["rules"]]


def _active_tools(port: int, headers: dict, timeout: float) -> list[str]:
    payload = _engine_json(_get(port, INTERCEPT_TOOLS_PATH, headers, timeout, MAX_RULES_RESPONSE_BYTES), 200,
                           MAX_RULES_RESPONSE_BYTES)
    tools = payload.get("enabled_tools") if isinstance(payload, dict) else None
    if not isinstance(tools, list) or not all(isinstance(tool, str) for tool in tools):
        raise EngineAPIError("engine_rules_response_invalid")
    return tools


def _rule_body(rule: dict) -> dict:
    # 사람이 답해야 하는 ask와 시간 제한은 자동 회차에서 쓰지 않는다. 꺼져 있어 쓰이지 않는 값은 내장 규칙과 같게 둔다.
    return dict(rule, enabled=True, timeout_enabled=False, timeout_seconds=RULE_TIMEOUT_SECONDS,
                timeout_action=RULE_TIMEOUT_ACTION)


@dataclass(frozen=True)
class EngineLLMProfile:
    """러너가 양쪽 엔진에 넣는 LLM 실행 설정. api_key는 게이트웨이 뒤에서 쓰지 않는 자리값이다."""
    format: str
    model: str
    base_url: str
    api_key: str
    context_window_k: int
    streaming: bool


def apply_engine_settings(port: int, secret: AdminSecret, profile: EngineLLMProfile, workers: int, timeout: float) -> None:
    """압축 기준 창과 worker 수를 작업 전에 넣는다. 결과 확인은 뒤의 실행 설정 스냅샷이 한다.

    `/api/llm`이 아니라 프로파일 경로를 쓴다. `/api/llm`은 저장값과 달리 실행 중 설정을 스트리밍으로
    되돌려 `llm_stream=false` 조건이 조용히 바뀐다. 이 경로는 baseline·개선 후 코드가 같다(시험 가드).
    """
    headers = {"Authorization": f"Bearer {login(port, secret, timeout)}"}
    body = {"name": ENGINE_PROFILE_NAME, "format": profile.format, "model": profile.model, "base_url": profile.base_url,
            "api_key": profile.api_key, "context_window_k": profile.context_window_k, "streaming": profile.streaming}
    try:
        status, data = _post(port, LLM_PROFILES_PATH, json.dumps(body).encode("utf-8"), headers, timeout,
                             MAX_SETTINGS_RESPONSE_BYTES)
        if status != 200:
            raise EngineAPIError(f"engine_settings_apply_http_{status}")
        try:
            profile_id = json.loads(data.decode("utf-8")).get("id")
        except (UnicodeError, ValueError, AttributeError):
            raise EngineAPIError("engine_settings_apply_response_invalid") from None
        if type(profile_id) is not int or profile_id <= 0:
            raise EngineAPIError("engine_settings_apply_response_invalid")
        status, _ = _post(port, LLM_ACTIVE_PROFILE_PATH, json.dumps({"id": profile_id}).encode("utf-8"), headers, timeout,
                          MAX_SETTINGS_RESPONSE_BYTES)
        if status != 200:
            raise EngineAPIError(f"engine_settings_apply_http_{status}")
        status, _ = _put(port, SETTINGS_PATHS["settings"], json.dumps({"workers": workers}).encode("utf-8"), headers, timeout,
                         MAX_SETTINGS_RESPONSE_BYTES)
        if status != 200:
            raise EngineAPIError(f"engine_settings_apply_http_{status}")
    except (OSError, http.client.HTTPException, ValueError, OverflowError):
        raise EngineAPIError("engine_settings_apply_unreachable") from None


def apply_engine_rules(port: int, secret: AdminSecret, rules: EngineRules, timeout: float) -> dict:
    """새 엔진 DB에 규칙을 넣고 다시 읽어 그대로인지 확인한다. 실패해도 다시 보내지 않는다(회차 무효).

    원본 ②는 원본 ① 기본 설정에 규칙을 더한 것이다. 그래서 내장 규칙을 건드리지 않고, 규칙을 타는 도구를 줄이는 파일은
    아무것도 쓰기 전에 거부한다. 앞서는 allow는 예외가 필요할 수 있어 막지 않고 건수를 근거에 남긴다.
    """
    token = login(port, secret, timeout)
    headers = {"Authorization": f"Bearer {token}"}
    try:
        seeded = _active_rules(port, headers, timeout)
        if {rule["name"] for rule in seeded} & {rule["name"] for rule in rules.rules}:
            raise EngineAPIError("engine_rules_name_collision")
        tools_before = _active_tools(port, headers, timeout)
        if rules.enabled_tools is not None and not set(tools_before) <= set(rules.enabled_tools):
            raise EngineAPIError("engine_rules_tools_narrowed")
        for rule in rules.rules:
            _engine_json(_post(port, INTERCEPT_RULES_PATH, json.dumps(_rule_body(rule)).encode("utf-8"), headers, timeout,
                               MAX_RULES_RESPONSE_BYTES), 200, MAX_RULES_RESPONSE_BYTES)
        if rules.enabled_tools is not None:
            body = json.dumps({"enabled_tools": list(rules.enabled_tools)}).encode("utf-8")
            _engine_json(_put(port, INTERCEPT_TOOLS_PATH, body, headers, timeout, MAX_RULES_RESPONSE_BYTES), 200,
                         MAX_RULES_RESPONSE_BYTES)
        active = _active_rules(port, headers, timeout)
        tools = _active_tools(port, headers, timeout)
    except (OSError, http.client.HTTPException, ValueError, OverflowError):
        raise EngineAPIError("engine_rules_unreachable") from None
    expected = seeded + [_rule_body(rule) for rule in rules.rules]
    if (_rules_sorted(active) != _rules_sorted(expected)
            or (rules.enabled_tools is not None and tools != list(rules.enabled_tools))):
        raise EngineAPIError("engine_rules_not_applied")
    return {"format": RULES_EVIDENCE_FORMAT, "file_sha256": rules.sha256, "injected": len(rules.rules),
            "seeded": len(seeded), "seeded_enabled": sum(1 for rule in seeded if rule["enabled"] is True),
            "seeded_sha256": _rules_sha256(seeded), "active_sha256": _rules_sha256(active), "tools_before": tools_before,
            "enabled_tools": tools, "allow_over_seeded": _allow_over_seeded(rules.rules, seeded)}


def _allow_over_seeded(rules: tuple[dict, ...], seeded: list[dict]) -> int:
    """켜진 내장 통제(deny·ask) 중 하나라도 앞지를 수 있는 파일 allow 수. 패턴이 실제로 겹치는지는 보지 않는다.

    엔진은 priority가 큰 규칙부터, 같으면 먼저 등록한 규칙부터 본다. 내장 규칙이 먼저 등록되므로 같은 priority는 앞지르지 못한다.
    """
    controls = [rule["priority"] for rule in seeded if rule["enabled"] is True and rule["action"] != "allow"]
    if not controls:
        return 0
    return sum(1 for rule in rules if rule["action"] == "allow" and rule["priority"] > min(controls))


def _count_intercept_row(row: object, file_rules: frozenset[str], summary: dict) -> None:
    """기록 한 행을 센다. 도구 입력·사유 원문은 읽지 않는다."""
    if not isinstance(row, dict) or row.get("status") not in _INTERCEPT_STATUSES:
        raise EngineAPIError("engine_intercept_response_invalid")
    summary["by_status"][row["status"]] += 1
    if row["status"] != "denied":
        return
    source = row.get("decision_source")
    if source == "rule":
        origin = "file_rule" if row.get("rule_name") in file_rules else "seeded_rule"
    else:
        origin = "model" if source == "model" else "other"
    summary["denied_by_origin"][origin] += 1
    tool = row.get("tool_name")
    name = tool if isinstance(tool, str) and _INTERCEPT_TOOL.fullmatch(tool) else OTHER_TOOL
    summary["denied_by_tool"][name] = summary["denied_by_tool"].get(name, 0) + 1


def intercept_summary(port: int, secret: AdminSecret, file_rules: frozenset[str], timeout: float) -> dict:
    """규칙이 어떤 도구 호출을 막았는지 건수로 낸다(프로토콜 §8.1). 원본 ①·개선 후의 내장 규칙 차단도 같이 잡는다.

    응답에는 도구 입력 원문이 들어 있지만 세기만 하고 남기지 않는다(절대규칙 2).
    """
    token = login(port, secret, timeout)
    headers = {"Authorization": f"Bearer {token}"}
    summary = {"format": INTERCEPT_SUMMARY_FORMAT, "total": 0, "by_status": dict.fromkeys(_INTERCEPT_STATUSES, 0),
               "denied_by_origin": {"file_rule": 0, "seeded_rule": 0, "model": 0, "other": 0}, "denied_by_tool": {}}
    seen = 0
    for page in range(1, MAX_INTERCEPT_PAGES + 1):
        path = f"{INTERCEPT_HISTORY_PATH}?page={page}&size={INTERCEPT_PAGE_SIZE}"
        try:
            status, data = _get(port, path, headers, timeout, MAX_INTERCEPT_PAGE_BYTES)
        except (OSError, http.client.HTTPException, ValueError, OverflowError):
            raise EngineAPIError("engine_intercept_unreachable") from None
        if status != 200:
            raise EngineAPIError(f"engine_intercept_http_{status}")
        try:
            if len(data) > MAX_INTERCEPT_PAGE_BYTES:
                raise ValueError
            payload = json.loads(data.decode("utf-8"))
        except (UnicodeError, ValueError):
            raise EngineAPIError("engine_intercept_response_invalid") from None
        items = payload.get("items") if isinstance(payload, dict) else None
        total = payload.get("total") if isinstance(payload, dict) else None
        if not isinstance(items, list) or type(total) is not int or (not items and seen < total):
            raise EngineAPIError("engine_intercept_response_invalid")
        for row in items:
            _count_intercept_row(row, file_rules, summary)
        seen += len(items)
        if seen > total:
            raise EngineAPIError("engine_intercept_response_invalid")
        if seen == total:
            summary["total"] = total
            return summary
    raise EngineAPIError("engine_intercept_too_many")


def _rules_sorted(rules: list[dict]) -> list[str]:
    return sorted(json.dumps(rule, sort_keys=True, ensure_ascii=False) for rule in rules)


def _rules_sha256(rules: list[dict]) -> str:
    return hashlib.sha256("\n".join(_rules_sorted(rules)).encode("utf-8")).hexdigest()


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
