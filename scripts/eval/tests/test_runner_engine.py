"""엔진 로그인·findings 수집과 DTO 형식 동일성 가드. 실제 엔진·대상 없이 합성 loopback 서버로 본다."""

from __future__ import annotations

import hashlib
import json
import os
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
from runner_engine import (
    ADMIN_USERNAME, AdminSecret, EngineAPIError, collect_findings, findings_record, load_vocabulary, login,
)
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

    def test_guard_detects_a_changed_field(self):
        text = (REPO_ROOT / "server/dto.go").read_text(encoding="utf-8")
        block = go_block(text, "type FindingDTO struct")
        changed = block.replace("\n}\n", '\n\tExtra string `json:"extra"`\n}\n')
        self.assertNotEqual(hashlib.sha256(changed.encode()).hexdigest(),
                            DTO_SHAPES[("server/dto.go", "type FindingDTO struct")])


class FakeEngine:
    """로그인 1개와 findings 페이지 조회만 하는 합성 엔진 API."""

    def __init__(self, items=None, login_status=200, login_body=None):
        self.items = [finding()] if items is None else items
        self.login_status, self.login_body = login_status, login_body
        self.requests = []
        engine = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                engine.requests.append(("POST", self.path, dict(self.headers), body))
                payload = engine.login_body if engine.login_body is not None else json.dumps({"token": TOKEN}).encode()
                self.send_response(engine.login_status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def do_GET(self):
                engine.requests.append(("GET", self.path, dict(self.headers), b""))
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
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

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
