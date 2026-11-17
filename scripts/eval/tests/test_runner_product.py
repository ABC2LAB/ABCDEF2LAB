"""탐색 분리 배치 O 회차 제품 API 구동기(결정안 §3 B(a), 4-7b) 시험. 가짜 제품 API로 순서·확인·비밀 비노출을 본다."""

from __future__ import annotations

import http.server
import json
import os
import re
import sys
import threading
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from runner_engine import AdminSecret  # noqa: E402
from runner_product import (  # noqa: E402
    DRAFTS_PATH, MAX_RESPONSE_BYTES, ModelKey, ProductAPIError, ProductDriver, loopback_send, start_split_round,
)

PASSWORD = "admin-password-for-test-1"
KEY = "sk-or-v1-model-key-for-test"
DNS_TOKEN = "dnscontroltokenfortest"
PROFILE = "product-model-openrouter"
MODEL = "qwen/qwen3.6-35b-a3b"
WORKFLOW = {"format": "abc2lab.workbench-flow/v1", "max_pages": 256, "max_endpoints": 256, "max_response_bytes": 2097152,
            "allow_reversible_writes": True}
REGISTRATION = {"url": "http://app.eval.test:8080/", "path_prefixes": ["/"], "methods": ["GET", "HEAD", "POST", "PUT", "PATCH"],
                "readonly_basis": "평가 회차 전용 연습 대상: 회차마다 새 컨테이너로 띄우고 회차 뒤 지운다(리셋 대상).",
                "requests": 400, "valid_hours": 2, "reversible_writes": True}
TARGET_ID = "target-" + "a" * 32
API_PORT, DNS_PORT = 18787, 19083


class FakeProduct:
    """제품 API·시험 DNS 제어의 최소 가짜. overrides[경로 이름] = (status, body 또는 함수)로 응답을 바꾼다."""

    def __init__(self, polls_before_task: int = 1):
        self.calls: list[dict] = []
        self.overrides: dict[str, object] = {}
        self.polls_before_task = polls_before_task
        self.polls = 0
        self.published: list[dict] = []
        self.raise_on: str = ""

    def target(self, state: str) -> dict:
        target = {"id": TARGET_ID, "origin": "http://app.eval.test:8080", "state": state, "revision": 1 if state == "pending" else 2,
                  "path_prefixes": ["/"], "methods": list(REGISTRATION["methods"]), "requests": 400, "port": 8080}
        if state == "pending":
            target.update(challenge_name="_abc2lab-challenge.app.eval.test", challenge_value="abc2lab-proof-1")
        else:
            target.update(addresses=["10.252.3.20"], tls_spki_sha256="", approved_at="2026-11-17T05:00:00Z",
                          expires_at="2026-11-17T07:00:00Z")
        return target

    def draft(self, state: str, task: str | None = None) -> dict:
        draft = {"id": "draft-1", "state": state, "revision": 1, "authority_state": "not_issued",
                 "scope": {"target_ref": TARGET_ID, "origin": "http://app.eval.test:8080"}}
        if task:
            draft["task_id"] = task
        return draft

    def default(self, method: str, port: int, name: str, body: dict | None) -> tuple[int, object]:
        if port == DNS_PORT:
            self.published.append(body)
            return 204, b""
        if name == "/api/auth/login":
            return 200, {"token": "product-token-1"}
        if name == "/api/product/model-credentials":
            if method == "POST":
                return 200, {"credential": {"profile_id": PROFILE, "model": MODEL, "configured": True}}
            return 200, {"credential": {"profile_id": PROFILE, "model": MODEL, "configured": True}}
        if name == "/api/diagnoses/v2/catalog":
            return 200, {"version": "diagnosis-api.v2", "owner_context": "x", "profiles": [
                {"id": "product-model-local_only", "workflow": {}, "workflow_request_limit": 1},
                {"id": PROFILE, "workflow": dict(WORKFLOW), "workflow_request_limit": 400}]}
        if name == "/api/product/targets":
            return 200, {"target": self.target("pending")}
        if name == "/api/product/targets/{id}/approve":
            return 200, {"target": self.target("approved")}
        if name == DRAFTS_PATH:
            return 201, {"draft": self.draft("ready")}
        if name == DRAFTS_PATH + "/{id}/start":
            return 200, {"draft": self.draft("preparing")}
        if name == DRAFTS_PATH + "/{id}/status":
            self.polls += 1
            return 200, {"draft": self.draft("running", "42") if self.polls > self.polls_before_task else self.draft("preparing")}
        return 404, {"error": "not_found"}

    def __call__(self, method, port, path, body, headers):
        name = re.sub(r"/target-[a-f0-9]{32}", "/{id}", re.sub(r"/drafts/[^/]+", "/drafts/{id}", path.split("?", 1)[0]))
        decoded = None if body is None else json.loads(body)
        self.calls.append({"method": method, "port": port, "path": path, "name": name, "body": decoded, "headers": dict(headers)})
        if self.raise_on == name:
            raise OSError("connection refused " + PASSWORD)
        status, value = self.default(method, port, name, decoded)
        if name in self.overrides:
            override = self.overrides[name]
            status, value = override(status, value) if callable(override) else override
        return status, value if isinstance(value, bytes) else json.dumps(value).encode()


def driver(fake: FakeProduct, clock: list[float] | None = None) -> ProductDriver:
    now = clock if clock is not None else [0.0]

    def sleep(seconds):
        now[0] += seconds

    return ProductDriver(api_port=API_PORT, dns_port=DNS_PORT, send=fake, sleep=sleep, monotonic=lambda: now[0])


def run(fake: FakeProduct, **changes):
    options = dict(secret=AdminSecret(PASSWORD), model_key=ModelKey(KEY), profile_id=PROFILE, model=MODEL, workflow=WORKFLOW,
                   registration=REGISTRATION, dns_token=DNS_TOKEN, task_wait_seconds=10, poll_interval_seconds=1)
    options.update(changes)
    return start_split_round(driver(fake), **options)


class TestHappyPath(unittest.TestCase):
    def test_steps_in_product_user_order_and_record(self):
        fake = FakeProduct(polls_before_task=2)
        start = run(fake)
        self.assertEqual([c["name"] for c in fake.calls], [
            "/api/auth/login", "/api/product/model-credentials", "/api/product/model-credentials", "/api/diagnoses/v2/catalog",
            "/api/product/targets", "/control", "/api/product/targets/{id}/approve", DRAFTS_PATH, DRAFTS_PATH + "/{id}/start",
            DRAFTS_PATH + "/{id}/status", DRAFTS_PATH + "/{id}/status", DRAFTS_PATH + "/{id}/status"])
        self.assertEqual((start.draft_id, start.task_id, start.state), ("draft-1", "42", "running"))
        self.assertEqual(start.catalog_profile, {"workflow": WORKFLOW, "workflow_request_limit": 400})
        self.assertEqual(start.target["id"], TARGET_ID)
        self.assertNotIn("challenge_value", start.target)
        self.assertEqual(fake.published, [{"name": "_abc2lab-challenge.app.eval.test", "values": ["abc2lab-proof-1"]}])
        draft_body = fake.calls[7]["body"]
        self.assertEqual(draft_body, {"version": "diagnosis-api.v2", "input": {
            "target_url": "http://app.eval.test:8080/", "profile_id": PROFILE, "workflow_mode": "ai_auto_v1"}})
        self.assertEqual(fake.calls[8]["body"], {"version": "diagnosis-api.v2", "expected_revision": 1})
        self.assertEqual(fake.calls[4]["body"], REGISTRATION)

    def test_headers_ports_and_secret_placement(self):
        fake = FakeProduct()
        start = run(fake)
        for index, call in enumerate(fake.calls):
            with self.subTest(call=call["name"], index=index):
                headers = call["headers"]
                dns = call["name"] == "/control"
                self.assertEqual(call["port"], DNS_PORT if dns else API_PORT)
                # 로그인 뒤 제품 API에만 토큰을 싣는다. DNS 제어에는 제어 토큰만 싣는다.
                self.assertEqual(headers.get("Authorization"), None if dns or index == 0 else "Bearer product-token-1")
                self.assertEqual(headers.get("X-Fixture-Control"), DNS_TOKEN if dns else None)
                key = headers.get("Idempotency-Key")
                if call["name"] in (DRAFTS_PATH, DRAFTS_PATH + "/{id}/start"):
                    self.assertRegex(key, r"\A[0-9a-f]{32}\Z")
                else:
                    self.assertIsNone(key)
                self.assertEqual(headers.get("Content-Type"), None if call["body"] is None else "application/json")
                text = json.dumps(call["body"])
                self.assertEqual(PASSWORD in text, index == 0)
                self.assertEqual(KEY in text, index == 1)
        keys = [c["headers"]["Idempotency-Key"] for c in fake.calls if "Idempotency-Key" in c["headers"]]
        self.assertEqual(len(set(keys)), 2)
        record = json.dumps(start.__dict__)
        for secret in (PASSWORD, KEY, DNS_TOKEN, "product-token-1"):
            self.assertNotIn(secret, record)


class TestFaults(unittest.TestCase):
    def check(self, code, *, overrides=None, raise_on="", polls=1, **changes):
        fake = FakeProduct(polls_before_task=polls)
        fake.overrides.update(overrides or {})
        fake.raise_on = raise_on
        with self.assertRaises(ProductAPIError) as caught:
            run(fake, **changes)
        message = str(caught.exception)
        self.assertEqual(message, code)
        for secret in (PASSWORD, KEY, DNS_TOKEN, "product-token-1", TARGET_ID, "abc2lab-proof-1"):
            self.assertNotIn(secret, message)
        return fake

    def test_each_product_answer_is_checked(self):
        approve = "/api/product/targets/{id}/approve"
        status = DRAFTS_PATH + "/{id}/status"
        cases = {
            "login_token": ("product_login_response_invalid", {"/api/auth/login": (200, {"token": "bad token"})}),
            "login_status": ("product_http_401:/api/auth/login:invalid_credentials",
                             {"/api/auth/login": (401, {"error": "invalid_credentials"})}),
            "key_not_set": ("product_model_key_not_configured", {"/api/product/model-credentials": lambda s, v: (
                200, {"credential": {"profile_id": PROFILE, "model": MODEL, "configured": False}})}),
            "key_echoed": ("product_model_key_not_configured", {"/api/product/model-credentials": lambda s, v: (
                200, {"credential": {"profile_id": PROFILE, "model": MODEL, "configured": True, "api_key": KEY}})}),
            "profile_missing": ("product_profile_missing", {"/api/diagnoses/v2/catalog": (200, {"profiles": []})}),
            "workflow_other": ("product_workflow_mismatch", {"/api/diagnoses/v2/catalog": (200, {"profiles": [
                {"id": PROFILE, "workflow": dict(WORKFLOW, max_endpoints=32), "workflow_request_limit": 400}]})}),
            "limit_invalid": ("product_request_limit_invalid", {"/api/diagnoses/v2/catalog": (200, {"profiles": [
                {"id": PROFILE, "workflow": dict(WORKFLOW), "workflow_request_limit": "400"}]})}),
            "limit_other": ("product_registration_requests_mismatch", {"/api/diagnoses/v2/catalog": (200, {"profiles": [
                {"id": PROFILE, "workflow": dict(WORKFLOW), "workflow_request_limit": 64}]})}),
            "register_origin": ("product_target_invalid", {"/api/product/targets": lambda s, v: (
                200, {"target": dict(v["target"], origin="http://other.test:8080")})}),
            "register_id": ("product_target_invalid", {"/api/product/targets": lambda s, v: (
                200, {"target": dict(v["target"], id="target-x")})}),
            "register_methods": ("product_target_scope_mismatch", {"/api/product/targets": lambda s, v: (
                200, {"target": dict(v["target"], methods=["GET", "HEAD"])})}),
            "register_requests": ("product_target_scope_mismatch", {"/api/product/targets": lambda s, v: (
                200, {"target": dict(v["target"], requests=64)})}),
            "challenge_name": ("product_challenge_invalid", {"/api/product/targets": lambda s, v: (
                200, {"target": dict(v["target"], challenge_name="_abc2lab-challenge.other.test")})}),
            "dns_forbidden": ("product_http_403:/control", {"/control": (403, b"forbidden")}),
            "approve_reason": ("product_rejected:/api/product/targets/{id}/approve:ownership_not_verified",
                               {approve: (200, {"reason": "ownership_not_verified"})}),
            "approve_pending": ("product_target_invalid", {approve: lambda s, v: (200, {"target": dict(v["target"], state="pending")})}),
            "approve_pin_on_http": ("product_approval_invalid", {approve: lambda s, v: (
                200, {"target": dict(v["target"], tls_spki_sha256="b" * 64)})}),
            "approve_no_address": ("product_approval_invalid", {approve: lambda s, v: (200, {"target": dict(v["target"], addresses=[])})}),
            "approve_no_expiry": ("product_approval_invalid", {approve: lambda s, v: (200, {"target": dict(v["target"], expires_at="")})}),
            "draft_not_ready": ("product_draft_not_ready", {DRAFTS_PATH: (201, {"draft": FakeProduct().draft("awaiting_approval")})}),
            "draft_other_target": ("product_draft_not_ready", {DRAFTS_PATH: lambda s, v: (
                201, {"draft": dict(v["draft"], scope={"target_ref": "target-" + "b" * 32})})}),
            "draft_status_200": ("product_http_200:/api/diagnoses/v2/drafts", {DRAFTS_PATH: lambda s, v: (200, v)}),
            "draft_bad_state": ("product_draft_invalid", {DRAFTS_PATH: lambda s, v: (201, {"draft": dict(v["draft"], state="done")})}),
            "start_conflict": ("product_http_409:/api/diagnoses/v2/drafts/{id}/start:draft_revision_conflict",
                               {DRAFTS_PATH + "/{id}/start": (409, {"error": "draft_revision_conflict"})}),
            "failed_before_task": ("product_draft_failed_before_task:target_dns_mismatch", {status: lambda s, v: (
                200, {"draft": dict(FakeProduct().draft("failed"), reason_code="target_dns_mismatch")})}),
            "bad_task_id": ("product_draft_invalid", {status: lambda s, v: (200, {"draft": FakeProduct().draft("running", "4x2")})}),
            "too_large": ("product_response_too_large:/api/diagnoses/v2/catalog",
                          {"/api/diagnoses/v2/catalog": (200, b"{" + b" " * MAX_RESPONSE_BYTES + b"}")}),
            "not_json": ("product_response_invalid:/api/diagnoses/v2/catalog", {"/api/diagnoses/v2/catalog": (200, b"<html>")}),
            "not_object": ("product_response_invalid:/api/diagnoses/v2/catalog", {"/api/diagnoses/v2/catalog": (200, [])}),
        }
        for name, (code, overrides) in cases.items():
            with self.subTest(case=name):
                self.check(code, overrides=overrides)

    def test_transport_failure_and_wait_timeout(self):
        self.check("product_unreachable:/api/product/targets/{id}/approve", raise_on="/api/product/targets/{id}/approve")
        fake = self.check("product_task_wait_timeout", polls=100)
        self.assertEqual(sum(c["name"].endswith("/status") for c in fake.calls), 11)  # 0초부터 10초까지 1초 간격

    def test_server_reason_equal_to_a_secret_is_dropped(self):
        # 제품이 사유 칸에 비밀 값을 되돌려도(고정 code 모양이어도) 오류에 옮기지 않는다.
        self.check("product_http_403:/control", overrides={"/control": (403, {"error": DNS_TOKEN})})

    def test_baseline_admin_is_refused(self):
        self.check("product_username_invalid", secret=AdminSecret(PASSWORD, username="ARTEX"))


class TestInputs(unittest.TestCase):
    def test_model_key_is_redacted_and_checked(self):
        key = ModelKey(KEY)
        self.assertNotIn(KEY, repr(key) + str(key))
        for value in ("", "a b", "x" * 513, 7, "line\n"):
            with self.subTest(value=value), self.assertRaises(ProductAPIError):
                ModelKey(value)

    def test_ports_and_dns_token_are_checked(self):
        for api, dns in ((0, 1), (1, 65536), (True, 1)):
            with self.subTest(api=api, dns=dns), self.assertRaises(ProductAPIError):
                ProductDriver(api_port=api, dns_port=dns, send=FakeProduct(), sleep=lambda s: None, monotonic=lambda: 0.0)
        for token in ("", "a b", None):
            with self.subTest(token=token), self.assertRaises(ProductAPIError) as caught:
                driver(FakeProduct()).publish_challenge(token, {"challenge_name": "n", "challenge_value": "v"})
            self.assertEqual(str(caught.exception), "product_dns_token_invalid")


class TestLoopbackSend(unittest.TestCase):
    def test_does_not_follow_redirects_and_caps_reads(self):
        seen = []

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                seen.append((self.path, self.headers.get("Authorization"), self.rfile.read(int(self.headers["Content-Length"]))))
                self.send_response(302)
                self.send_header("Location", "http://127.0.0.1:1/elsewhere")
                self.end_headers()

            def do_GET(self):
                seen.append((self.path, None, b""))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"x" * (MAX_RESPONSE_BYTES + 10))

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            port = server.server_address[1]
            status, data = loopback_send("POST", port, "/api/x", b"{}", {"Authorization": "Bearer t"}, timeout=5)
            self.assertEqual((status, data), (302, b""))
            status, data = loopback_send("GET", port, "/big", None, {}, timeout=5)
            self.assertEqual((status, len(data)), (200, MAX_RESPONSE_BYTES + 1))
        finally:
            server.shutdown()
            server.server_close()
        self.assertEqual(seen, [("/api/x", "Bearer t", b"{}"), ("/big", None, b"")])


if __name__ == "__main__":
    unittest.main()
