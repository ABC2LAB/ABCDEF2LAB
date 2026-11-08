"""엔진 로그인·findings 수집과 DTO 형식 동일성 가드. 실제 엔진·대상 없이 합성 loopback 서버로 본다."""

from __future__ import annotations

import hashlib
import json
import os
import re
import sys
import tempfile
import threading
import traceback
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from baseline_extract import MAX_VOCABULARY_BYTES, UPSTREAM_COMMIT
from baseline_pages import BaselinePageMerge
from runner_config import REPO_ROOT
import runner  # noqa: E402
from runner_engine import (
    _TARGET_DOMAIN, _TARGET_IP, _TARGET_URL, ADMIN_USERNAME, AdminSecret, EngineAPIError, TaskRequest, collect_findings,
    EngineLLMProfile, apply_engine_settings, engine_settings, findings_record, load_vocabulary, login, start_task,
    task_request,
)
from test_runner_config import runner_config
from test_baseline_extract import finding, vocabulary

PASSWORD = "SYNTHETIC-ADMIN-PASSWORD-CANARY"
TOKEN = "synthetic.token.canary-0000"
SETTINGS = {"page_size": 50, "timeout_seconds": 5, "max_page_bytes": 1 << 20, "max_total_bytes": 1 << 20,
            "max_pages": 10}

# 추출기가 고정한 원본 형식(upstream fdfb7a17)의 정의. 바뀌면 추출 경로를 다시 확인해야 한다.
DTO_SHAPES = {
    ("server/dto.go", "type FindingDTO struct"): "debf2905aa8761e1075e5f6126d6e4ebc7ba08ae09a076e21ca0280a24d4bf8e",
    ("server/dto.go", "type FindingAssetDTO struct"): "d4d656acca8c9d5445ea5dcc8c30b67b9cdc6d427d85fa3cb902490086f00834",
    ("db/finding_traffic.go", "type FindingTrafficBinding struct"):
        "ab164860bdc9b47eb4e6b0dbd4e9dad750bd902c0a201134bd65c836ccfb20a1",
    ("server/server.go", "func (s *Server) findings("): "92224a6934f83ea5e171453b9b5370b3c381535d8e0a645c938ac1288b8faffc",
    ("server/auth.go", "func (s *Server) authLogin("): "0aa9a58c985f5cbd24b313c081ea863f6fd9b9b77520a51d45ab60957868711b",
}

# 작업 생성 계약: 요청 구조체·핸들러·기동·대상 추출·응답 DTO·인증 미들웨어. upstream fdfb7a17·baseline e53a79f·
# 개선 후가 같다(직접 확인한 값). 다르면 baseline·개선 후가 같은 요청으로 같은 일을 시작한다는 전제를 다시 본다.
TASK_SHAPES = {
    ("server/server.go", "type createTaskReq struct"): "f07493c7ed7ca34b80af73f43b69b70207d61221e5fd2002f3cd514277b00f77",
    # baseline과 다른 곳은 설명이 빈 요청의 기본 이름(언어 전환)뿐이다. 러너는 빈 설명을 거부하므로 회차에서 타지 않는다.
    ("server/server.go", "func (s *Server) createTask("):
        "f05d1f004bc7c101827a08ffbe1d2b499e6b9547e748753d1ce8aa1aa3ea6f56",
    ("server/goals.go", "func (s *Server) launchTask("): "2a05e77a7e68b7aa3e5b94bce4bdc0e8e56416de1996c8f7d6a4ac8545e3e513",
    ("server/server.go", "func (s *Server) seed("): "7d0891d1bdcc78596ed2a3c8e40cd22b0be3866c78cc25962e948ed41430963a",
    ("server/server.go", "func parseTarget("): "4fa3c05b1d4ac4d24a387df3dda6fb87e566f45e9f16f0a2b79fa5e3b47bb3ea",
    ("server/dto.go", "type TaskDTO struct"): "119a0e91764c1e1e9c3a7701e0d0ddb8d63be7d700faf389d160eefadca56163",
    ("server/auth.go", "func (s *Server) requireAuth("): "1a66aca5c409d1b827327f5b6524438522821a1ba759508ad9c704750e790ef0",
    # 러너가 압축 기준 창을 넣는 경로(apply_engine_settings). baseline e53a79f와 같은 값을 직접 확인했다.
    ("server/server_mgmt.go", "func (s *Server) pgSaveProfile("):
        "bd1e80549787564149575354402cbc5ef17b23308d525a8be00e8633c7526d2e",
    ("server/server_mgmt.go", "func (s *Server) pgActivateProfile("):
        "636fcf4ea39ee15733a7d4cc224ff1553baefdad0f22f931a5e46850f089d85d",
    # 명시적 OpenRouter 모드에서만 연결·빈 응답·스트림 재시도를 끈다.
    # 외부 정책이 없는 기존 local-only·평가 경로와 task DTO 계약은 유지한다.
    ("server/server.go", "func (s *Server) loadLLMConfig("):
        "7094d6792af8cc9b2a487c9509b699a2b4b430e2fb8dd381bd29d45a3391a6a1",
    ("server/server.go", "func (s *Server) getLLM("):
        "3578d8622da0f87547261aea06ef17d3f46aa7215906fb122f8f8d3b53776436",
    ("db/config.go", "type LLMProfile struct"):
        "d94ed53b2203f42b0825da23763b92863794507bef5d3d97a0c47f1d8742a8b8",
}
TARGET = "http://eval-meter:8080"


def go_block(text: str, start: str) -> str:
    out, inside = [], False
    for line in text.splitlines():
        if line.startswith(start):
            inside = True
        if inside:
            out.append(line)
            if line == "}":
                break
    if not inside or out[-1] != "}":
        raise AssertionError(f"block not found: {start}")
    return "\n".join(out) + "\n"


def assert_no_visible_chain(case: unittest.TestCase, exc: BaseException) -> None:
    """원래 예외(소켓·JSON 오류와 그 입력)가 트레이스백에 따라 나오지 않아야 한다."""
    case.assertIsNone(exc.__cause__)
    case.assertTrue(exc.__context__ is None or exc.__suppress_context__)
    shown = "".join(traceback.format_exception(exc))
    case.assertNotIn("During handling", shown)
    case.assertNotIn(PASSWORD, shown)


class TestDTOFormatGuard(unittest.TestCase):
    def test_finding_shapes_and_handlers_match_the_extractor_format(self):
        # 같은 추출 경로를 baseline·개선 후에 쓰는 근거. 다르면 추출기와 형식 기준을 다시 확인한다.
        for (path, start), expected in DTO_SHAPES.items():
            text = (REPO_ROOT / path).read_text(encoding="utf-8")
            with self.subTest(block=start):
                self.assertEqual(hashlib.sha256(go_block(text, start).encode()).hexdigest(), expected)

    def test_task_creation_contract_matches(self):
        for (path, start), expected in TASK_SHAPES.items():
            text = (REPO_ROOT / path).read_text(encoding="utf-8")
            with self.subTest(block=start):
                self.assertEqual(hashlib.sha256(go_block(text, start).encode()).hexdigest(), expected)

    def test_target_patterns_are_the_engine_patterns(self):
        # 러너가 보는 대상 = 엔진 parseTarget이 보는 대상이어야 한다.
        text = (REPO_ROOT / "server/server.go").read_text(encoding="utf-8")
        for name, pattern in (("reURL", _TARGET_URL), ("reIPPort", _TARGET_IP), ("reDomain", _TARGET_DOMAIN)):
            with self.subTest(name=name):
                self.assertRegex(text, rf"\n\t{name} +=")
                literal = re.search(rf"\n\t{name} += regexp\.MustCompile\(`([^`]*)`\)", text).group(1)
                self.assertEqual(pattern.pattern, literal)

    def test_guard_detects_a_changed_field(self):
        text = (REPO_ROOT / "server/dto.go").read_text(encoding="utf-8")
        block = go_block(text, "type FindingDTO struct")
        changed = block.replace("\n}\n", '\n\tExtra string `json:"extra"`\n}\n')
        self.assertNotEqual(hashlib.sha256(changed.encode()).hexdigest(),
                            DTO_SHAPES[("server/dto.go", "type FindingDTO struct")])


# server/server.go settingsPayload·getLLM 응답 모양(합성 값). 키는 설정 여부 bool만 나오고 프록시는 URL 문자열이다.
ENGINE_SETTINGS = {"output_language": "ko", "traffic_capture": True, "web_search_enabled": False,
                   "web_search_backend": "",
            "brave_key_set": False, "tavily_key_set": False, "web_search_proxy": "", "global_proxy": "",
            "python_interpreter": "", "workers": 3, "task_concurrency_enabled": False, "task_concurrency_limit": 5,
            "llm_pool_enabled": False, "noa_compaction": False, "notify_digest_interval_min": 60}
ENGINE_LLM = {"configured": True, "provider": "openai", "model": "example-org/example-model",
                "base_url": "http://eval-llm-gw:8090/api/v1", "proxy": "", "key_set": True, "rate_per_second": 0,
                "rate_per_minute": 0, "context_window_k": 0, "thinking_type": "", "reasoning_effort": ""}


class FakeEngine:
    """로그인 1개와 findings 페이지 조회만 하는 합성 엔진 API."""

    def __init__(self, items=None, login_status=200, login_body=None, task_status=201, task_body=None,
                 settings=None, apply=None):
        self.items = [finding()] if items is None else items
        # 실행 설정 GET 응답: 경로 → (status, 본문 bytes). 없으면 정상 응답.
        self.settings = settings or {}
        # 실행 설정 쓰기 응답: 경로 → (status, 본문 bytes). 없으면 정상 응답.
        self.apply = apply or {}
        self.login_status, self.login_body = login_status, login_body
        self.task_status, self.task_body = task_status, task_body
        self.requests = []
        engine = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                engine.requests.append(("POST", self.path, dict(self.headers), body))
                if self.path in ("/api/llm/profiles", "/api/llm/profiles/active"):
                    self.apply_reply()
                    return
                if self.path == "/api/tasks" and engine.task_status is None:
                    self.close_connection = True  # 응답 없이 끊는다(생성 여부가 모호한 실패)
                    return
                if self.path == "/api/tasks":
                    authorized = self.headers.get("Authorization") == f"Bearer {TOKEN}"
                    status = engine.task_status if authorized else 401
                    payload = engine.task_body if engine.task_body is not None else json.dumps(
                        {"id": "17", "description": "echo", "status": "running"}).encode()
                else:
                    status = engine.login_status
                    payload = engine.login_body if engine.login_body is not None else json.dumps({"token": TOKEN}).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def do_PUT(self):
                engine.requests.append(("PUT", self.path, dict(self.headers),
                                        self.rfile.read(int(self.headers.get("Content-Length", "0")))))
                self.apply_reply()

            def apply_reply(self):
                body = b'{"id": 7}' if self.path == "/api/llm/profiles" else b'{"ok": true}'
                status, payload = engine.apply.get(self.path, (200, body))
                if self.headers.get("Authorization") != f"Bearer {TOKEN}":
                    status = 401
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def do_GET(self):
                engine.requests.append(("GET", self.path, dict(self.headers), b""))
                if self.path in ("/api/settings", "/api/llm"):
                    authorized = self.headers.get("Authorization") == f"Bearer {TOKEN}"
                    status, payload = engine.settings.get(self.path, (200, json.dumps(
                        ENGINE_SETTINGS if self.path == "/api/settings" else ENGINE_LLM).encode()))
                    self.send_response(status if authorized else 401)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)
                    return
                query = parse_qs(urlsplit(self.path).query)
                page, size = int(query["page"][0]), int(query["limit"][0])
                payload = json.dumps({"items": engine.items, "total": len(engine.items), "page": page,
                                      "page_size": size}).encode()
                self.send_response(200 if self.headers.get("Authorization") == f"Bearer {TOKEN}" else 401)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class TestLogin(unittest.TestCase):
    def engine(self, **kwargs):
        engine = FakeEngine(**kwargs)
        self.addCleanup(engine.close)
        return engine

    def test_login_posts_fixed_username_and_returns_token(self):
        engine = self.engine()
        self.assertEqual(login(engine.port, AdminSecret(PASSWORD), 5), TOKEN)
        method, path, headers, body = engine.requests[0]
        self.assertEqual((method, path), ("POST", "/api/auth/login"))
        self.assertEqual(json.loads(body), {"username": ADMIN_USERNAME, "password": PASSWORD})
        self.assertNotIn("Authorization", headers)

    def test_username_is_the_one_the_engine_checks(self):
        # 핸들러 본문은 DTO 가드가 고정한다. 상수가 그 핸들러가 비교하는 이름과 같은지 따로 본다.
        handler = go_block((REPO_ROOT / "server/auth.go").read_text(encoding="utf-8"), "func (s *Server) authLogin(")
        self.assertIn(f'req.Username != "{ADMIN_USERNAME}"', handler)
        self.assertEqual(ADMIN_USERNAME, "ARTEX")

    def test_failures_use_fixed_codes_without_secrets(self):
        cases = [
            ({"login_status": 401, "login_body": b'{"error":"bad"}'}, "engine_login_http_401"),
            ({"login_body": b"not json"}, "engine_login_response_invalid"),
            ({"login_body": b'{"tok":"x"}'}, "engine_login_response_invalid"),
            ({"login_body": json.dumps({"token": "bad token"}).encode()}, "engine_login_response_invalid"),
            ({"login_body": json.dumps({"token": 7}).encode()}, "engine_login_response_invalid"),
            # 상한까지만 읽으면 유효한 토큰 응답이 되는 크기 초과 본문: 잘라 읽은 앞부분을 믿지 않는다.
            ({"login_body": json.dumps({"token": TOKEN}).encode() + b" " * (64 * 1024)},
             "engine_login_response_invalid"),
        ]
        for kwargs, code in cases:
            with self.subTest(code=code, kwargs=str(kwargs)[:40]):
                engine = self.engine(**kwargs)
                with self.assertRaises(EngineAPIError) as caught:
                    login(engine.port, AdminSecret(PASSWORD), 5)
                self.assertEqual(str(caught.exception), code)
                self.assertNotIn(PASSWORD, repr(caught.exception))
                assert_no_visible_chain(self, caught.exception)

    def test_unreachable_engine(self):
        engine = FakeEngine()
        port = engine.port
        engine.close()
        with self.assertRaises(EngineAPIError) as caught:
            login(port, AdminSecret(PASSWORD), 1)
        self.assertEqual(str(caught.exception), "engine_login_unreachable")
        assert_no_visible_chain(self, caught.exception)

    def test_secret_is_never_shown(self):
        secret = AdminSecret(PASSWORD)
        self.assertNotIn(PASSWORD, repr(secret))
        self.assertNotIn(PASSWORD, str(secret))
        self.assertNotIn(PASSWORD, f"{secret}")
        for value in ("", "a\nb", "x" * 1025, None, b"pw"):
            with self.assertRaises(EngineAPIError):
                AdminSecret(value)


class TestCollectFindings(unittest.TestCase):
    def test_records_projection_with_format_and_build_sources_apart(self):
        engine = FakeEngine()
        self.addCleanup(engine.close)
        record = collect_findings(engine.port, AdminSecret(PASSWORD), SETTINGS, vocabulary(), "v" * 64,
                                  side="ours", build_source="2" * 40)
        self.assertEqual((record["dto_format_source"], record["build_source"], record["side"]),
                         (UPSTREAM_COMMIT, "2" * 40, "ours"))
        self.assertNotIn("declared_source_sha", record)
        self.assertEqual((record["declared_total"], record["page_count"], record["vocabulary_sha256"]), (1, 1, "v" * 64))
        self.assertEqual(record["findings"][0]["vuln_class"], "sqli")
        dumped = json.dumps(record)
        for secret in ("SYNTHETIC-PRIVATE", PASSWORD, TOKEN):
            self.assertNotIn(secret, dumped)
        # 로그인 먼저, 그다음 토큰을 단 조회만.
        kinds = [(method, urlsplit(path).path) for method, path, _, _ in engine.requests]
        self.assertEqual(kinds, [("POST", "/api/auth/login"), ("GET", "/api/exploration/findings")])
        self.assertEqual(engine.requests[1][2]["Authorization"], f"Bearer {TOKEN}")

    def test_baseline_uses_the_same_path(self):
        engine = FakeEngine(items=[])
        self.addCleanup(engine.close)
        record = collect_findings(engine.port, AdminSecret(PASSWORD), SETTINGS, vocabulary(), "v" * 64,
                                  side="baseline", build_source="e53a79fe4df0c95a5b09274de8db321fa038b00b")
        self.assertEqual((record["dto_format_source"], record["declared_total"], record["findings"]),
                         (UPSTREAM_COMMIT, 0, []))

    def test_record_refuses_other_format_source(self):
        merged = BaselinePageMerge(declared_source_sha="1" * 40, declared_total=0, page_size=1, page_count=1, findings=())
        with self.assertRaises(EngineAPIError):
            findings_record(merged, side="ours", build_source="2" * 40, vocabulary_sha256="v" * 64)


def task(**changes) -> dict:
    return dict(runner_config()["task"], **changes)


class TestTaskRequest(unittest.TestCase):
    def test_body_is_canonical_and_hash_is_of_the_bytes_sent(self):
        request = task_request(task(), TARGET)
        self.assertEqual(json.loads(request.body), task())
        self.assertEqual(request.body, json.dumps(task(), sort_keys=True, separators=(",", ":")).encode("ascii"))
        self.assertEqual(request.sha256, hashlib.sha256(request.body).hexdigest())
        # 키 순서가 달라도 같은 내용이면 같은 본문·해시다(baseline·개선 후 대조용).
        self.assertEqual(task_request(dict(reversed(list(task().items()))), TARGET), request)
        self.assertNotEqual(task_request(task(goal="다른 목표"), TARGET).sha256, request.sha256)

    def test_target_must_be_the_run_origin_only(self):
        accepted = [
            task(), task(description="대상 http://eval-meter:8080/ 진단", goal="권한"),
            task(description="대상 진단", goal=f"{TARGET} 의 자원"),
            task(description=f"{TARGET} 와 {TARGET}/ 둘 다 같은 대상"),
            task(description=f"{TARGET}'s 진단"),  # 엔진 정규식도 ' 앞에서 끊어 같은 대상을 본다
            # 엔진은 설명과 목표를 공백 하나로 잇는다. 설명이 origin으로 끝나도 목표 첫 글자와 붙지 않는다.
            task(description=f"대상 {TARGET}", goal="다른 사용자 자원"),
        ]
        for value in accepted:
            with self.subTest(text=value["description"][:30]):
                self.assertIsInstance(task_request(value, TARGET + "/"), TaskRequest)
        rejected = [
            task(description="대상 이름 없음"),
            task(description=f"{TARGET} 와 http://juice-shop:3000"),
            task(description="https://eval-meter:8080 진단"),
            task(description=f"{TARGET}/rest/user 진단"),
            task(description=f"{TARGET} 진단", goal="10.0.0.5:3000 도 본다"),
            task(description=f"{TARGET} 진단", goal="api.example.com 도 본다"),
            task(description=f"{TARGET} 진단", goal="index.html 확인"),
            # Go \s는 ASCII 공백만이라 엔진은 아래 URL을 origin 뒤까지 이어 읽는다(그러면 포트가 깨져 대상을 못 심는다).
            task(description=f"{TARGET}\u3000진단"), task(description=f"{TARGET}\xa0진단"),
        ]
        for value in rejected:
            with self.subTest(text=value["description"][:30] + value["goal"][:20]), \
                    self.assertRaises(EngineAPIError) as caught:
                task_request(value, TARGET)
            self.assertEqual(str(caught.exception), "task_target_mismatch")

    def test_patterns_match_like_go_ascii_classes(self):
        # Go regexp의 \b·\s는 ASCII다. 한글 사이에 붙은 대상도 엔진과 같이 본다.
        self.assertEqual(_TARGET_URL.findall(f"대상{TARGET}을"), [f"{TARGET}을"])
        self.assertIsNotNone(_TARGET_DOMAIN.search("가example.com"))


class TestStartTask(unittest.TestCase):
    def engine(self, **kwargs):
        engine = FakeEngine(**kwargs)
        self.addCleanup(engine.close)
        return engine

    def test_logs_in_then_creates_with_exact_body(self):
        engine = self.engine()
        request = task_request(task(), TARGET)
        self.assertEqual(start_task(engine.port, AdminSecret(PASSWORD), request, 5), "17")
        kinds = [(method, path) for method, path, _, _ in engine.requests]
        self.assertEqual(kinds, [("POST", "/api/auth/login"), ("POST", "/api/tasks")])
        _, _, headers, body = engine.requests[1]
        self.assertEqual(body, request.body)
        self.assertEqual(headers["Authorization"], f"Bearer {TOKEN}")
        self.assertNotIn(PASSWORD.encode(), body)

    def test_failures_use_fixed_codes_and_are_not_retried(self):
        request = task_request(task(), TARGET)
        cases = [
            ({"task_status": 400, "task_body": b'{"error":"bad json"}'}, "task_create_http_400"),
            ({"task_status": 200}, "task_create_http_200"),
            ({"task_body": b"not json"}, "task_create_response_invalid"),
            ({"task_body": b'{"name":"x"}'}, "task_create_response_invalid"),
            ({"task_body": b'{"id":17}'}, "task_create_response_invalid"),
            ({"task_body": b'{"id":"0"}'}, "task_create_response_invalid"),
            ({"task_body": b'{"id":"17a"}'}, "task_create_response_invalid"),
            ({"task_body": b'[{"id":"17"}]'}, "task_create_response_invalid"),
            ({"task_body": json.dumps({"id": "17"}).encode() + b" " * (1024 * 1024)}, "task_create_response_invalid"),
            ({"login_status": 401, "login_body": b"{}"}, "engine_login_http_401"),
        ]
        for kwargs, code in cases:
            with self.subTest(code=code, kwargs=str(kwargs)[:50]):
                engine = self.engine(**kwargs)
                with self.assertRaises(EngineAPIError) as caught:
                    start_task(engine.port, AdminSecret(PASSWORD), request, 5)
                self.assertEqual(str(caught.exception), code)
                assert_no_visible_chain(self, caught.exception)
                self.assertNotIn(TOKEN, "".join(traceback.format_exception(caught.exception)))
                creates = [path for _, path, _, _ in engine.requests if path == "/api/tasks"]
                self.assertLessEqual(len(creates), 1)

    def test_dropped_create_is_not_resent(self):
        engine = self.engine(task_status=None)
        with self.assertRaises(EngineAPIError) as caught:
            start_task(engine.port, AdminSecret(PASSWORD), task_request(task(), TARGET), 5)
        self.assertEqual(str(caught.exception), "task_create_unreachable")
        assert_no_visible_chain(self, caught.exception)
        self.assertEqual([path for _, path, _, _ in engine.requests].count("/api/tasks"), 1)

    def test_unreachable_engine(self):
        engine = FakeEngine()
        port = engine.port
        engine.close()
        with self.assertRaises(EngineAPIError) as caught:
            start_task(port, AdminSecret(PASSWORD), task_request(task(), TARGET), 1)
        self.assertEqual(str(caught.exception), "engine_login_unreachable")


class TestVocabulary(unittest.TestCase):
    def write(self, text: str | bytes) -> Path:
        directory = tempfile.mkdtemp()
        self.addCleanup(lambda: [os.remove(os.path.join(directory, f)) for f in os.listdir(directory)]
                        and os.rmdir(directory))
        path = Path(directory, "vocabulary.json")
        path.write_bytes(text if isinstance(text, bytes) else text.encode())
        return path

    def test_valid_vocabulary_with_digest_of_raw_bytes(self):
        raw = json.dumps(vocabulary(), indent=3)  # 재직렬화와 다른 원본 바이트
        loaded, digest = load_vocabulary(self.write(raw))
        self.assertEqual(loaded, vocabulary())
        self.assertEqual(digest, hashlib.sha256(raw.encode()).hexdigest())

    def test_invalid_vocabularies_rejected(self):
        for text in ('{"vulnclasses": {}, "vulnclasses": {}, "asset_types": {}}', '{"vulnclasses": {}}', "not json",
                     '{"vulnclasses": {"x": "Bad Label"}, "asset_types": {}}', b"\xff",
                     json.dumps(vocabulary()) + " " * MAX_VOCABULARY_BYTES):
            with self.subTest(text=str(text)[:30]), self.assertRaises(EngineAPIError) as caught:
                load_vocabulary(self.write(text))
            self.assertEqual(str(caught.exception), "findings_vocabulary_invalid")
        with self.assertRaises(EngineAPIError):
            load_vocabulary("/nonexistent/vocabulary.json")


if __name__ == "__main__":
    unittest.main()


class TestApplyEngineSettings(unittest.TestCase):
    PROFILE = EngineLLMProfile("openai", "example-org/example-model", "http://eval-llm-gw:8090/api/v1",
                               "gateway-holds-the-key", 64, False)

    def engine(self, **kwargs):
        engine = FakeEngine(**kwargs)
        self.addCleanup(engine.close)
        return engine

    def test_saves_activates_then_sets_workers_with_one_login(self):
        engine = self.engine()
        apply_engine_settings(engine.port, AdminSecret(PASSWORD), self.PROFILE, 2, 5)
        self.assertEqual([(method, path) for method, path, _, _ in engine.requests],
                         [("POST", "/api/auth/login"), ("POST", "/api/llm/profiles"),
                          ("POST", "/api/llm/profiles/active"), ("PUT", "/api/settings")])
        _, _, _, saved = engine.requests[1]
        # 스트리밍 값을 그대로 넘겨야 한다. /api/llm 경로는 실행 중 설정을 스트리밍으로 되돌린다.
        self.assertEqual(json.loads(saved), {"name": "default", "format": "openai", "model": "example-org/example-model",
                                             "base_url": "http://eval-llm-gw:8090/api/v1", "api_key": "gateway-holds-the-key",
                                             "context_window_k": 64, "streaming": False})
        self.assertEqual(json.loads(engine.requests[2][3]), {"id": 7})
        self.assertEqual(json.loads(engine.requests[3][3]), {"workers": 2})
        for _, _, headers, body in engine.requests[1:]:
            self.assertEqual(headers["Authorization"], f"Bearer {TOKEN}")
            self.assertNotIn(PASSWORD.encode(), body)

    def test_each_failure_has_a_fixed_code_and_stops_there(self):
        cases = [
            ({"/api/llm/profiles": (500, b"{}")}, "engine_settings_apply_http_500", 2),
            ({"/api/llm/profiles": (200, b"not json")}, "engine_settings_apply_response_invalid", 2),
            ({"/api/llm/profiles": (200, b"[]")}, "engine_settings_apply_response_invalid", 2),
            ({"/api/llm/profiles": (200, b'{"id": 0}')}, "engine_settings_apply_response_invalid", 2),
            ({"/api/llm/profiles": (200, b'{"id": "7"}')}, "engine_settings_apply_response_invalid", 2),
            ({"/api/llm/profiles": (200, b'{"id": true}')}, "engine_settings_apply_response_invalid", 2),
            ({"/api/llm/profiles/active": (500, b"{}")}, "engine_settings_apply_http_500", 3),
            ({"/api/settings": (400, b"{}")}, "engine_settings_apply_http_400", 4),
        ]
        for apply, code, requests in cases:
            with self.subTest(code=code, apply=apply):
                engine = self.engine(apply=apply)
                with self.assertRaises(EngineAPIError) as caught:
                    apply_engine_settings(engine.port, AdminSecret(PASSWORD), self.PROFILE, 2, 5)
                self.assertEqual(str(caught.exception), code)
                self.assertEqual(len(engine.requests), requests)
                self.assertNotIn(PASSWORD, str(caught.exception))

    def test_unreachable_engine_has_a_fixed_code(self):
        engine = self.engine()
        port = engine.port
        engine.close()
        self.doCleanups()
        with self.assertRaises(EngineAPIError):
            apply_engine_settings(port, AdminSecret(PASSWORD), self.PROFILE, 2, 1)

    def test_placeholder_key_matches_compose(self):
        # 엔진이 키를 지우면 LLM이 꺼지므로 같은 자리값을 넣는다. 진짜 키가 아니어야 한다.
        compose = (Path(__file__).resolve().parents[3] / "deploy/engine-isolated/compose.yaml").read_text(encoding="utf-8")
        self.assertIn(f"OPENAI_API_KEY: {runner.ENGINE_LLM_PLACEHOLDER_KEY}", compose)


class TestEngineSettings(unittest.TestCase):
    def engine(self, **kwargs):
        engine = FakeEngine(**kwargs)
        self.addCleanup(engine.close)
        return engine

    def test_reads_both_settings_with_one_login_and_replaces_proxies_with_flags(self):
        engine = self.engine()
        snapshot = engine_settings(engine.port, AdminSecret(PASSWORD), 5)
        kinds = [(method, path) for method, path, _, _ in engine.requests]
        self.assertEqual(kinds, [("POST", "/api/auth/login"), ("GET", "/api/settings"), ("GET", "/api/llm")])
        for _, path, headers, _ in engine.requests[1:]:
            self.assertEqual(headers["Authorization"], f"Bearer {TOKEN}")
        expected_settings = {k: v for k, v in ENGINE_SETTINGS.items() if k not in ("web_search_proxy", "global_proxy")}
        expected_settings.update(web_search_proxy_set=False, global_proxy_set=False)
        expected_llm = {k: v for k, v in ENGINE_LLM.items() if k != "proxy"} | {"proxy_set": False}
        self.assertEqual(snapshot, {"settings": expected_settings, "llm": expected_llm})

    def test_proxy_values_never_reach_the_snapshot(self):
        secret_proxy = "http://user:synthetic-secret@proxy.example:3128"
        engine = self.engine(settings={
            "/api/settings": (200, json.dumps(dict(ENGINE_SETTINGS, global_proxy=secret_proxy)).encode()),
            "/api/llm": (200, json.dumps(dict(ENGINE_LLM, proxy=" " + secret_proxy)).encode())})
        snapshot = engine_settings(engine.port, AdminSecret(PASSWORD), 5)
        self.assertTrue(snapshot["settings"]["global_proxy_set"] and snapshot["llm"]["proxy_set"])
        self.assertNotIn("synthetic-secret", json.dumps(snapshot))

    def test_unexpected_shapes_fail_with_fixed_codes(self):
        cases = [
            ({"/api/settings": (500, b"{}")}, "engine_settings_http_500"),
            ({"/api/llm": (404, b"{}")}, "engine_settings_http_404"),
            ({"/api/settings": (200, b"not json")}, "engine_settings_response_invalid"),
            ({"/api/settings": (200, b"[]")}, "engine_settings_response_invalid"),
            ({"/api/settings": (200, b"{}")}, "engine_settings_response_invalid"),
            ({"/api/settings": (200, b'{"workers":3,"workers":4,"web_search_proxy":"","global_proxy":""}')},
             "engine_settings_response_invalid"),
            ({"/api/settings": (200, json.dumps(dict(ENGINE_SETTINGS, nested={"a": 1})).encode())},
             "engine_settings_response_invalid"),
            ({"/api/llm": (200, json.dumps({k: v for k, v in ENGINE_LLM.items() if k != "proxy"}).encode())},
             "engine_settings_response_invalid"),
            ({"/api/settings": (200, json.dumps(dict(ENGINE_SETTINGS, global_proxy=None)).encode())},
             "engine_settings_response_invalid"),
            ({"/api/settings": (200, json.dumps(ENGINE_SETTINGS).encode() + b" " * (64 * 1024))},
             "engine_settings_response_invalid"),
        ]
        for settings, code in cases:
            with self.subTest(code=code, settings=list(settings)):
                engine = self.engine(settings=settings)
                with self.assertRaises(EngineAPIError) as caught:
                    engine_settings(engine.port, AdminSecret(PASSWORD), 5)
                self.assertEqual(str(caught.exception), code)
                self.assertNotIn(PASSWORD, str(caught.exception))

    def test_the_engine_responses_still_carry_the_fields_the_runner_rewrites(self):
        # 러너는 프록시 URL을 설정 여부로 바꾸고 나머지 값은 그대로 남긴다. 핸들러가 키 원문을 내기 시작하거나 프록시
        # 키 이름이 바뀌면 스냅샷이 비밀을 담거나 대조를 놓치므로 여기서 먼저 걸린다.
        server = (REPO_ROOT / "server/server.go").read_text(encoding="utf-8")
        settings = go_block(server, "func (s *Server) settingsPayload(")
        llm = go_block(server, "func (s *Server) getLLM(")
        for key in ('"web_search_proxy":', '"global_proxy":', '"workers":'):
            self.assertIn(key, settings)
        for key in ('"proxy":', '"model":', '"key_set":'):
            self.assertIn(key, llm)
        for block in (settings, llm):
            self.assertNotRegex(block, r'"(api_key|brave_search_api_key|tavily_search_api_key|password|token)":')
