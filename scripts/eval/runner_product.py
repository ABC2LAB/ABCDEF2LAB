"""탐색 분리 배치(개선 후 O) 평가 회차의 제품 API 구동기(탐색 분리 배치 결정안 §3 B(a), 순서 4-7b).

O 회차는 원본처럼 엔진 작업 API를 직접 부르지 않고, 제품 사용자가 작업대에서 하는 순서를 제품 API로 그대로 밟는다:
로그인 → 모델 키 등록 → 카탈로그(워크플로 상한·회차 요청 상한) → 대상 등록 → 시험 DNS에 소유 증명 TXT 게시 → 대상 승인 →
진단 초안 → 시작 → 작업 번호가 붙을 때까지 상태 조회. 각 응답은 화면이 확인하는 것과 같은 조건으로 확인하고, 어긋나면 고정
code로 멈춘다(제품이 다른 범위로 승인했는데 회차가 그대로 도는 일을 막는다).

제품 API와 시험 DNS 제어는 compose가 루프백에만 publish한 포트로만 부른다. 관리자 비밀번호·모델 키·토큰·DNS 제어 토큰은
돌려주는 기록과 오류에 넣지 않는다. 회차 연결(포트 고르기·보호 입력 읽기·서명 기록)은 4-7c다.
"""

from __future__ import annotations

import http.client
import json
import re
import secrets
from collections.abc import Callable
from contextlib import closing
from dataclasses import dataclass
from urllib.parse import quote, urlsplit

from runner_engine import ADMIN_USERNAME, AdminSecret

LOGIN_PATH = "/api/auth/login"
MODEL_CREDENTIALS_PATH = "/api/product/model-credentials"
CATALOG_PATH = "/api/diagnoses/v2/catalog"
TARGETS_PATH = "/api/product/targets"
DRAFTS_PATH = "/api/diagnoses/v2/drafts"
CONTROL_POLICY_FORMAT = "abc2lab.control-policy-view/v1"
DNS_CONTROL_PATH = "/control"
API_VERSION = "diagnosis-api.v2"
WORKFLOW_MODE = "ai_auto_v1"
# 화면 파서와 같은 상태 집합(web/src/lib/diagnosis/diagnosis-url.ts urlStateLabels). 끝 상태는 작업 번호 없이 끝날 수 있다.
DRAFT_STATES = frozenset({"awaiting_approval", "ready", "preparing", "running", "stopping", "completed", "cancelled", "failed",
                          "unknown"})
DRAFT_TERMINAL = frozenset({"completed", "cancelled", "failed", "unknown"})
MAX_RESPONSE_BYTES = 2 << 20
_TOKEN = re.compile(r"[A-Za-z0-9._~+/-]+=*\Z")
_TARGET_ID = re.compile(r"target-[a-f0-9]{32}\Z")
_REF = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,127}\Z")
_REASON = re.compile(r"[a-z][a-z0-9_]{0,63}\Z")
_TASK_ID = re.compile(r"[0-9]{1,19}\Z")

Send = Callable[[str, int, str, "bytes | None", dict], "tuple[int, bytes]"]


class ProductAPIError(RuntimeError):
    """제품 구동 실패. 고정 code와 제품이 돌려준 고정 사유 code만 담는다(응답 본문·비밀 값은 담지 않는다)."""


class ModelKey:
    """모델 키를 담기만 한다. repr·str로 값이 드러나지 않는다."""

    __slots__ = ("_value",)

    def __init__(self, value: str):
        if not isinstance(value, str) or not 0 < len(value) <= 512 or any(ord(c) < 33 or ord(c) == 127 for c in value):
            raise ProductAPIError("product_model_key_invalid")
        self._value = value

    def __repr__(self) -> str:
        return "ModelKey(<redacted>)"

    __str__ = __repr__

    def reveal(self) -> str:
        return self._value


def _ordered(values: object) -> list[str] | None:
    """문자열 목록을 정렬한 사본. 목록이 아니거나 문자열이 아닌 항목이 있으면 None(어떤 목록과도 같지 않다)."""
    if not isinstance(values, list) or not all(isinstance(value, str) for value in values):
        return None
    return sorted(values)


def _path_name(path: str) -> str:
    """오류 code에 쓰는 경로 이름. query와 대상·초안 ID는 빼고 {id}로 둔다."""
    name = path.split("?", 1)[0]
    for prefix in (TARGETS_PATH, DRAFTS_PATH):
        if name.startswith(prefix + "/"):
            rest = name[len(prefix) + 1:].split("/")
            return "/".join([prefix, "{id}", *rest[1:]])
    return name


def loopback_send(method: str, port: int, path: str, body: bytes | None, headers: dict, *, timeout: float) -> tuple[int, bytes]:
    """루프백에 요청 하나. 환경 프록시·쿠키·리다이렉트 추적 없이 직접 연결하고 응답은 상한+1까지만 읽는다."""
    with closing(http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)) as connection:
        connection.request(method, path, body=body, headers={"Accept": "application/json", "Connection": "close", **headers})
        with closing(connection.getresponse()) as response:
            return response.status, response.read(MAX_RESPONSE_BYTES + 1)


@dataclass(frozen=True)
class SplitRoundStart:
    """서명 기록에 남길 구동 결과. 비밀 값은 없다."""
    target: dict          # 승인된 대상(제품 응답의 범위 칸만: id·origin·state·경로·메서드·요청 수·주소·포트·TLS 고정값·만료)
    catalog_profile: dict  # 카탈로그 프로필의 워크플로·회차 요청 상한
    draft_id: str
    task_id: str
    state: str


class ProductDriver:
    """제품 API를 루프백 포트로 부른다. send는 시험에서 가짜로 바꾼다."""

    def __init__(self, *, api_port: int, dns_port: int, send: Send, sleep: Callable[[float], None],
                 monotonic: Callable[[], float]):
        for port in (api_port, dns_port):
            if type(port) is not int or not 0 < port < 65536:
                raise ProductAPIError("product_port_invalid")
        self._api_port, self._dns_port = api_port, dns_port
        self._send, self._sleep, self._monotonic = send, sleep, monotonic
        self._token = ""
        self._secrets: list[str] = []

    # ── 요청 한 번 ─────────────────────────────────────────────────────────────────────────────────────────────
    def _call(self, method: str, path: str, body: object = None, *, expected: int = 200, key: str = "",
              port: int | None = None, headers: dict | None = None) -> dict | None:
        name = _path_name(path)
        sent = {} if headers is None else dict(headers)
        if body is not None:
            sent["Content-Type"] = "application/json"
        if self._token and port is None:
            sent["Authorization"] = "Bearer " + self._token
        if key:
            sent["Idempotency-Key"] = key
        raw = None if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
        try:
            status, data = self._send(method, self._api_port if port is None else port, path, raw, sent)
        except (OSError, http.client.HTTPException, ValueError, OverflowError):
            raise ProductAPIError(f"product_unreachable:{name}") from None
        if len(data) > MAX_RESPONSE_BYTES:
            raise ProductAPIError(f"product_response_too_large:{name}")
        if status != expected:
            raise ProductAPIError(f"product_http_{status}:{name}{self._reason(data)}")
        if status == 204:
            return None
        try:
            value = json.loads(data.decode("utf-8"))
        except (UnicodeError, ValueError):
            raise ProductAPIError(f"product_response_invalid:{name}") from None
        if not isinstance(value, dict):
            raise ProductAPIError(f"product_response_invalid:{name}")
        if isinstance(value.get("reason"), str) and value["reason"]:
            # 제품은 거절을 200 + reason으로도 돌려준다(화면 TargetApprovalPanel과 같다).
            raise ProductAPIError(f"product_rejected:{name}{self._reason(data)}")
        return value

    def _reason(self, data: bytes) -> str:
        """응답 본문의 reason·error 칸을 사유 투영(_safe_reason)으로 붙인다."""
        try:
            value = json.loads(data.decode("utf-8"))
        except (UnicodeError, ValueError):
            return ""
        return self._safe_reason(value.get("reason") or value.get("error") if isinstance(value, dict) else None)

    def _safe_reason(self, reason: object) -> str:
        """제품이 돌려준 고정 사유 code(소문자·밑줄)만 ':code'로 붙인다. 알려진 비밀 값(관리자 비밀번호·모델 키·로그인 토큰·
        DNS 제어 토큰)과 같으면 버린다. 오류에 사유를 붙이는 모든 경로가 이 함수를 쓴다."""
        if isinstance(reason, str) and _REASON.fullmatch(reason) and reason not in self._secrets:
            return ":" + reason
        return ""

    # ── 단계 ───────────────────────────────────────────────────────────────────────────────────────────────────
    def login(self, secret: AdminSecret) -> None:
        if secret.username != ADMIN_USERNAME:
            raise ProductAPIError("product_username_invalid")  # 제품 관리자는 하나다(원본 관리자 이름 아님)
        self._secrets.append(secret.reveal())
        value = self._call("POST", LOGIN_PATH, {"username": secret.username, "password": secret.reveal()})
        token = value.get("token")
        if not isinstance(token, str) or not 0 < len(token) <= 8192 or not _TOKEN.fullmatch(token):
            raise ProductAPIError("product_login_response_invalid")
        self._token = token
        self._secrets.append(token)

    def set_model_key(self, profile_id: str, model: str, key: ModelKey) -> None:
        """설치 뒤 모델 키를 넣고, 키 칸 없이 '설정됨'·모델만 돌아오는지 확인한다(화면과 같은 투영)."""
        self._secrets.append(key.reveal())
        self._call("POST", MODEL_CREDENTIALS_PATH, {"profile_id": profile_id, "model": model, "api_key": key.reveal()})
        value = self._call("GET", MODEL_CREDENTIALS_PATH + "?profile_id=" + quote(profile_id, safe=""))
        credential = value.get("credential")
        if (not isinstance(credential, dict) or set(credential) != {"profile_id", "model", "configured"}
                or credential != {"profile_id": profile_id, "model": model, "configured": True}):
            raise ProductAPIError("product_model_key_not_configured")

    def catalog_profile(self, profile_id: str, workflow: dict) -> dict:
        """카탈로그에서 프로필의 워크플로 상한·회차 요청 상한을 읽는다. 워크플로는 설치 입력에 넣은 값과 같아야 한다."""
        value = self._call("GET", CATALOG_PATH)
        profiles = value.get("profiles")
        found = [p for p in profiles if isinstance(p, dict) and p.get("id") == profile_id] if isinstance(profiles, list) else []
        if len(found) != 1:
            raise ProductAPIError("product_profile_missing")
        profile = found[0]
        limit = profile.get("workflow_request_limit")
        if profile.get("workflow") != workflow:
            raise ProductAPIError("product_workflow_mismatch")  # 설치가 기본값 파일과 다른 워크플로를 쓰고 있다
        if type(limit) is not int or limit < 1:
            raise ProductAPIError("product_request_limit_invalid")
        return {"workflow": dict(workflow), "workflow_request_limit": limit}

    def register(self, registration: dict) -> dict:
        target = self._target(self._call("POST", TARGETS_PATH, registration), registration, "pending")
        host = urlsplit(registration["url"]).hostname
        if (target.get("challenge_name") != "_abc2lab-challenge." + host or not isinstance(target.get("challenge_value"), str)
                or not target["challenge_value"]):
            raise ProductAPIError("product_challenge_invalid")
        return target

    def publish_challenge(self, dns_token: str, target: dict) -> None:
        """시험 DNS(overlay fixture DNS)에 소유 증명 TXT를 게시한다. 제어 토큰은 회차마다 fixture-init이 만든 값이다."""
        if not isinstance(dns_token, str) or not dns_token or any(ord(c) < 33 or ord(c) == 127 for c in dns_token):
            raise ProductAPIError("product_dns_token_invalid")
        self._secrets.append(dns_token)
        self._call("POST", DNS_CONTROL_PATH, {"name": target["challenge_name"], "values": [target["challenge_value"]]},
                   expected=204, port=self._dns_port, headers={"X-Fixture-Control": dns_token})

    def approve(self, target: dict, registration: dict) -> dict:
        approved = self._target(self._call("POST", f"{TARGETS_PATH}/{target['id']}/approve", {}), registration, "approved")
        if approved["id"] != target["id"]:
            raise ProductAPIError("product_target_mismatch")
        addresses = approved.get("addresses")
        pin = approved.get("tls_spki_sha256") or ""
        # http 대상(C22)은 TLS 고정값이 비어 있어야 하고, https 대상은 64자 16진이어야 한다(화면 targetReply와 같다).
        pin_ok = pin == "" if approved["origin"].startswith("http://") else bool(re.fullmatch(r"[a-f0-9]{64}", pin))
        if not isinstance(addresses, list) or not addresses or not pin_ok or not approved.get("expires_at"):
            raise ProductAPIError("product_approval_invalid")
        keep = ("id", "origin", "state", "revision", "path_prefixes", "methods", "requests", "addresses", "port",
                "tls_spki_sha256", "approved_at", "expires_at")
        return {name: approved[name] for name in keep if name in approved}

    def _target(self, value: dict, registration: dict, state: str) -> dict:
        target = value.get("target")
        parts = urlsplit(registration["url"])
        origin = f"{parts.scheme}://{parts.netloc}"
        if (not isinstance(target, dict) or not isinstance(target.get("id"), str) or not _TARGET_ID.fullmatch(target["id"])
                or target.get("origin") != origin or target.get("state") != state):
            raise ProductAPIError("product_target_invalid")
        # 제품이 등록 범위를 바꿔 기록하면(경로·메서드·요청 수) 회차 조건과 달라진다. 제품은 경로·메서드를 정렬해 저장하므로
        # (product_targets.go register의 sort.Strings) 양쪽을 같은 순서로 맞춰 비교한다. 중복·누락·추가는 그대로 어긋난다.
        if (_ordered(target.get("path_prefixes")) != _ordered(registration["path_prefixes"])
                or _ordered(target.get("methods")) != _ordered(registration["methods"])
                or target.get("requests") != registration["requests"]):
            raise ProductAPIError("product_target_scope_mismatch")
        return target

    def create_draft(self, target_url: str, profile_id: str, target_id: str) -> dict:
        value = self._call("POST", DRAFTS_PATH, {"version": API_VERSION, "input": {
            "target_url": target_url, "profile_id": profile_id, "workflow_mode": WORKFLOW_MODE}},
            expected=201, key=secrets.token_hex(16))
        draft = self._draft(value)
        scope = draft.get("scope")
        if draft["state"] != "ready" or not isinstance(scope, dict) or scope.get("target_ref") != target_id:
            # 제품이 준비 안 됨 사유(approval_expired 등)를 고정 code로 붙인다. 설정 오류 분류(runner_early_end)가 쓴다.
            raise ProductAPIError("product_draft_not_ready" + self._safe_reason(draft.get("reason_code")))
        return draft

    def start(self, draft: dict) -> None:
        """시작. 범위의 모델이 외부 전송(OpenRouter)이면 외부 전송 확인을 같이 보낸다(화면 commandURLDiagnosis와 같다).
        제품은 그 확인 없이는 외부 모델 회차 시작을 `approval_required`로 거부한다(diagnosisprep service_linux Start).
        평가 회차는 외부 API 사용을 프로토콜 §6이 허용하고, 실제 대상이 아닌 공개 연습 대상이다."""
        scope = draft.get("scope") if isinstance(draft.get("scope"), dict) else {}
        model = scope.get("model") if isinstance(scope.get("model"), dict) else {}
        body = {"version": API_VERSION, "expected_revision": draft["revision"]}
        if model.get("external_transfer") is True:
            body["external_transfer_confirmed"] = True
        self._call("POST", f"{DRAFTS_PATH}/{draft['id']}/start", body, key=secrets.token_hex(16))

    def stop(self, draft_id: str) -> dict:
        """회차를 멈춘다(화면의 중지와 같은 명령). 상태와 상관없이 현재 revision으로 보낸다: 제품 Stop은 활성 상태가 아니면
        작업을 그대로 돌려주고(충돌 아님), `unknown`이면 살아 있는 실행 문맥을 취소한다(diagnosisprep service_linux Stop,
        #665 리뷰). B의 게이트웨이 봉인(drain 뒤 새 모델 요청 막기)과 같은 자리에서 O의 새 모델 호출·전송을 멈추려는 것이다."""
        draft = self.status(draft_id)
        return self._draft(self._call("POST", f"{DRAFTS_PATH}/{draft_id}/stop",
                                      {"version": API_VERSION, "expected_revision": draft["revision"]}))

    def wait_closed(self, draft_id: str, deadline_seconds: float, interval_seconds: float) -> dict:
        """끝 상태이고 drain이 끝날 때까지 본다. 그 뒤에야 실행 기록·장부·호출 기록이 더 바뀌지 않는다(진행 중 호출이 기록을
        남기는 중에 읽지 않게). 시한 안에 닫히지 않으면 고정 code로 멈춘다."""
        end = self._monotonic() + deadline_seconds
        while True:
            draft = self.status(draft_id)
            if draft["state"] in DRAFT_TERMINAL and draft.get("drain_complete") is True:
                return draft
            if self._monotonic() >= end:
                raise ProductAPIError("product_close_timeout")
            self._sleep(interval_seconds)

    def run_record(self, task_id: str) -> dict:
        """그 작업의 저장된 실행 기록(`GET /api/tasks/{id}/diagnosis-run`): 실행·준비 모델 사용량, 목표 SHA(goal), 결과.
        해석은 runner_product_llm.product_model_usage가 한다. 작업 번호는 숫자만 받는다."""
        if not isinstance(task_id, str) or not _TASK_ID.fullmatch(task_id):
            raise ProductAPIError("product_task_invalid")
        return self._call("GET", f"/api/tasks/{task_id}/diagnosis-run")

    def control_policy(self, task_id: str) -> dict:
        """그 작업이 실제로 쓴 E② 정책의 작업대 보기(`GET /api/tasks/{id}/control-policy`, 계약 workbench-live-data §3).
        `policy_fingerprint_sha256`은 실행 factory와 같은 검증기(ValidateE2HTTPRunPolicyDocument)가 낸 내용 지문이다."""
        if not isinstance(task_id, str) or not _TASK_ID.fullmatch(task_id):
            raise ProductAPIError("product_task_invalid")
        view = self._call("GET", f"/api/tasks/{task_id}/control-policy")
        if not isinstance(view, dict) or view.get("format") != CONTROL_POLICY_FORMAT or not isinstance(view.get("limits"), dict):
            raise ProductAPIError("product_control_policy_invalid")
        return view

    def workflow(self, draft_id: str) -> dict:
        """그 초안 Job의 워크플로 snapshot(`GET /api/product/workflows/{job}`, 계약 workbench-live-data §5). 끝점마다 발견
        출처(`source`)가 있다. 응답 Job ID가 요청한 초안과 다르면 거부한다."""
        if not isinstance(draft_id, str) or not _REF.fullmatch(draft_id):
            raise ProductAPIError("product_draft_invalid")
        value = self._call("GET", f"/api/product/workflows/{draft_id}")
        snapshot = value.get("workflow") if isinstance(value, dict) else None
        if not isinstance(snapshot, dict) or not isinstance(snapshot.get("endpoints"), list):
            raise ProductAPIError("product_workflow_invalid")
        if snapshot.get("job_id") != draft_id:
            raise ProductAPIError("product_draft_mismatch")
        return snapshot

    def status(self, draft_id: str) -> dict:
        """그 초안의 상태. 응답 초안 ID가 요청한 ID와 다르면(다른 작업을 관찰·서명하게 됨) 거부한다."""
        draft = self._draft(self._call("GET", f"{DRAFTS_PATH}/{draft_id}/status"))
        if draft["id"] != draft_id:
            raise ProductAPIError("product_draft_mismatch")
        return draft

    def wait_task(self, draft_id: str, deadline_seconds: float, interval_seconds: float) -> dict:
        """작업 번호가 붙을 때까지 상태를 본다. 그 전에 끝 상태로 끝나면 제품 사유 code와 함께 멈춘다."""
        end = self._monotonic() + deadline_seconds
        while True:
            draft = self.status(draft_id)
            if draft.get("task_id"):
                return draft
            if draft["state"] in DRAFT_TERMINAL:
                raise ProductAPIError(f"product_draft_{draft['state']}_before_task{self._safe_reason(draft.get('reason_code'))}")
            if self._monotonic() >= end:
                raise ProductAPIError("product_task_wait_timeout")
            self._sleep(interval_seconds)

    def _draft(self, value: dict) -> dict:
        draft = value.get("draft")
        if (not isinstance(draft, dict) or not isinstance(draft.get("id"), str) or not _REF.fullmatch(draft["id"])
                or draft.get("state") not in DRAFT_STATES or type(draft.get("revision")) is not int):
            raise ProductAPIError("product_draft_invalid")
        task = draft.get("task_id")
        if task is not None and (not isinstance(task, str) or not _TASK_ID.fullmatch(task)):
            raise ProductAPIError("product_draft_invalid")
        return draft


def start_split_round(driver: ProductDriver, *, secret: AdminSecret, model_key: ModelKey, profile_id: str, model: str,
                      workflow: dict, registration: dict, dns_token: str, task_wait_seconds: float,
                      poll_interval_seconds: float) -> SplitRoundStart:
    """제품 사용자 순서대로 O 회차를 시작하고 작업 번호를 받는다. 등록 요청 수는 카탈로그 회차 요청 상한이어야 한다."""
    driver.login(secret)
    driver.set_model_key(profile_id, model, model_key)
    profile = driver.catalog_profile(profile_id, workflow)
    if registration.get("requests") != profile["workflow_request_limit"]:
        raise ProductAPIError("product_registration_requests_mismatch")  # 화면처럼 설치 회차 상한으로 등록한다
    pending = driver.register(registration)
    driver.publish_challenge(dns_token, pending)
    target = driver.approve(pending, registration)
    draft = driver.create_draft(registration["url"], profile_id, target["id"])
    driver.start(draft)
    running = driver.wait_task(draft["id"], task_wait_seconds, poll_interval_seconds)
    return SplitRoundStart(target=target, catalog_profile=profile, draft_id=draft["id"], task_id=running["task_id"],
                           state=running["state"])
