// live/mask: 실제 모양의 HTTP 원문 · JSON · 도구 입력에서 비밀이 남지 않는지.

import { describe, expect, it } from "vitest";

import { isSensitiveKey, maskJson, maskParam, maskSecrets, REDACTED } from "../live/mask";

const JWT =
  "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJBUlRFWCIsImV4cCI6MTc5OTk5OTk5OX0.c2lnbmF0dXJlLWJ5dGVzLWhlcmU";

/** 원문에 있던 비밀 조각이 결과에 하나도 없어야 한다. */
function expectGone(out: string, secrets: string[]) {
  for (const x of secrets) expect(out).not.toContain(x);
}

describe("maskSecrets — HTTP 요청", () => {
  const req = [
    "POST /api/login?next=/home&access_token=qs-tok-123 HTTP/1.1",
    "Host: shop.example.com",
    "User-Agent: Mozilla/5.0",
    `Authorization: Bearer ${JWT}`,
    "Cookie: sid=s3ss10n-abc; theme=dark; csrftoken=csrf-xyz",
    "X-Api-Key: ak_live_9f8e7d",
    "X-CSRF-Token: hdr-csrf-777",
    "Content-Type: application/json",
    "",
    '{"email":"a@example.com","password":"hunter2","remember":true,"profile":{"api_key":"k-1","name":"A"}}',
  ].join("\n");

  it("헤더 값 · 쿼리 · JSON 본문의 비밀을 가리고 나머지는 둔다", () => {
    const out = maskSecrets(req);
    expectGone(out, [
      "qs-tok-123",
      JWT,
      "s3ss10n-abc",
      "csrf-xyz",
      "ak_live_9f8e7d",
      "hdr-csrf-777",
      "hunter2",
      '"k-1"',
    ]);
    expect(out).toContain("POST /api/login?next=/home&access_token=[REDACTED] HTTP/1.1");
    expect(out).toContain("Host: shop.example.com");
    expect(out).toContain("Authorization: Bearer [REDACTED]");
    // Cookie 는 이름을 남기고 값만
    expect(out).toContain("Cookie: sid=[REDACTED]; theme=[REDACTED]; csrftoken=[REDACTED]");
    expect(out).toContain("X-Api-Key: [REDACTED]");
    expect(out).toContain('"email":"a@example.com"');
    expect(out).toContain('"password":"[REDACTED]"');
    expect(out).toContain('"remember":true');
    expect(out).toContain('"name":"A"');
  });

  it("여러 번 걸어도 같다", () => {
    const once = maskSecrets(req);
    expect(maskSecrets(once)).toBe(once);
  });
});

describe("maskSecrets — HTTP 응답", () => {
  const resp = [
    "HTTP/1.1 200 OK",
    "Set-Cookie: session=abcDEF123; Path=/; HttpOnly; Secure; SameSite=Lax",
    "Set-Cookie: remember_me=rm-999; Expires=Wed, 21 Oct 2026 07:28:00 GMT",
    "Content-Type: application/json",
    "",
    "{",
    `  "token": "${JWT}",`,
    '  "refresh_token": "rf-456",',
    '  "session_id": 9876543,',
    '  "user": { "id": 1012, "role": "admin" }',
    "}",
  ].join("\n");

  it("Set-Cookie 는 첫 값만 가리고 속성은 둔다, JSON 토큰·숫자 세션도 가린다", () => {
    const out = maskSecrets(resp);
    expectGone(out, ["abcDEF123", "rm-999", JWT, "rf-456", "9876543"]);
    expect(out).toContain("Set-Cookie: session=[REDACTED]; Path=/; HttpOnly; Secure; SameSite=Lax");
    expect(out).toContain("Set-Cookie: remember_me=[REDACTED]; Expires=Wed, 21 Oct 2026 07:28:00 GMT");
    expect(out).toContain('"token": "[REDACTED]"');
    expect(out).toContain('"session_id": "[REDACTED]"');
    expect(out).toContain('"user": { "id": 1012, "role": "admin" }');
    expect(out.split("\n")).toHaveLength(resp.split("\n").length);
  });
});

describe("maskSecrets — 폼 · 로그 · 도구 입력", () => {
  it("폼 본문과 key: value", () => {
    const out = maskSecrets("username=alice&password=p%40ss&otp=123456\npasswd: s3cr3t\napi-key = nope");
    expectGone(out, ["p%40ss", "s3cr3t"]);
    expect(out).toContain("username=alice&password=[REDACTED]&otp=123456");
    expect(out).toContain("passwd: [REDACTED]");
    expect(out).toContain("api-key = [REDACTED]");
  });

  it("이스케이프된 JSON 과 curl 명령 안의 헤더", () => {
    const toolInput = JSON.stringify({
      command: `curl -s -H 'Authorization: Bearer sk-live-abc' -H "Cookie: PHPSESSID=php-123" 'https://x.test/a?token=t-1&id=5'`,
      body: JSON.stringify({ password: "inner-pw", note: "ok" }),
    });
    const out = maskSecrets(toolInput);
    expectGone(out, ["sk-live-abc", "php-123", "t-1", "inner-pw"]);
    expect(out).toContain("id=5");
    expect(out).toContain('\\"note\\":\\"ok\\"');
  });

  it("글 속 Bearer · JWT", () => {
    const out = maskSecrets(`토큰 Bearer abc.def-ghi 와 ${JWT} 를 받음`);
    expect(out).toBe(`토큰 Bearer ${REDACTED} 와 ${REDACTED} 를 받음`);
  });

  it("빈 글 · 비밀 없는 글은 그대로", () => {
    expect(maskSecrets("")).toBe("");
    const plain = "GET /api/products?page=2 HTTP/1.1\nHost: shop.example.com\n\n";
    expect(maskSecrets(plain)).toBe(plain);
  });
});

describe("maskParam · maskJson · isSensitiveKey", () => {
  it("이름이 민감하면 값 전체", () => {
    expect(isSensitiveKey("X-Auth-Token")).toBe(true);
    expect(isSensitiveKey("client_secret")).toBe(true);
    expect(isSensitiveKey("page")).toBe(false);
    expect(maskParam("sessionid", "abc")).toBe(REDACTED);
    expect(maskParam("q", "Bearer zzz")).toBe(`Bearer ${REDACTED}`);
    expect(maskParam("q", "")).toBe("");
  });

  it("maskJson 은 객체를 풀어 가린다", () => {
    const out = maskJson({ url: "https://x.test/?apikey=K1", headers: { Authorization: "Basic dXNlcjpwYXNz" } });
    expectGone(out, ["K1", "dXNlcjpwYXNz"]);
    expect(out).toContain('"url": "https://x.test/?apikey=[REDACTED]"');
    expect(maskJson(null)).toBe("");
    expect(maskJson("password=x")).toBe("password=[REDACTED]");
  });
});
