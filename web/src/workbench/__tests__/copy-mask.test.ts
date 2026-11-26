// live/copyMask: 클립보드로 나가는 글(실제 모양의 HTTP 원문 · JSON · 도구 입력)에 비밀이 남지 않는지(#975 이동찬 P2 — 화면은 안 가리고 복사만).

import { describe, expect, it } from "vitest";

import { maskForCopy, maskSecrets, REDACTED, renderedCopyText, UNVERIFIED_LINE } from "../live/copyMask";
import { MARKUP_HIDDEN_SECRETS } from "./mask-fixtures";

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

describe("maskForCopy — 클립보드로 나가는 글 (#709 리뷰)", () => {
  // #677 리뷰의 합성 fixture: 키 이름을 HTML 엔티티로 숨기면 maskSecrets 는 키를 못 알아본다
  const ENTITY_KEY = "&#112;&#97;&#115;&#115;&#119;&#111;&#114;&#100;";

  it("maskSecrets 만으로는 엔티티로 숨긴 키의 값이 남는다 (고치기 전 모양)", () => {
    expect(maskSecrets(`${ENTITY_KEY}=synthetic-entity-secret`)).toContain("synthetic-entity-secret");
  });

  it.each([
    ["10진 엔티티", `${ENTITY_KEY}=synthetic-entity-secret`, "synthetic-entity-secret"],
    ["16진 · 세미콜론 없는 엔티티", "&#x70;&#97ssword=synthetic-hex-secret", "synthetic-hex-secret"],
    ["이름 엔티티 구분자", "password&equals;synthetic-named-secret", "synthetic-named-secret"],
    ["두 겹 엔티티", "&amp;#112;assword=synthetic-double-secret", "synthetic-double-secret"],
    ["Markdown 역슬래시", "api\\_key=synthetic-escape-secret", "synthetic-escape-secret"],
    ["JSON 안 엔티티 키", `{"${ENTITY_KEY}":"synthetic-json-secret"}`, "synthetic-json-secret"],
  ])("붙여 넣은 곳이 풀 모양까지 가린다 — %s", (_, line, secret) => {
    const out = maskForCopy(`GET /api/orders/2\n${line}\n200 OK`);
    expect(out).not.toContain(secret);
    expect(out).toContain(REDACTED);
    // 다른 줄은 그대로
    expect(out.split("\n")[0]).toBe("GET /api/orders/2");
    expect(out.split("\n")[2]).toBe("200 OK");
  });

  it("풀어도 새로 가릴 게 없는 줄은 원래 모양(가린 것)을 그대로 둔다", () => {
    const escaped = '{"body":"{\\"password\\":\\"hunter2\\",\\"note\\":\\"ok\\"}"}';
    expect(maskForCopy(escaped)).toBe(maskSecrets(escaped));
    expect(maskForCopy(escaped)).not.toContain("hunter2");
    const plain = "GET /search?q=a&amp;b HTTP/1.1";
    expect(maskForCopy(plain)).toBe(plain);
  });

  it.each(MARKUP_HIDDEN_SECRETS)("상세 보고 카드와 같은 #677 fixture 도 가린다 — %s", (_, text, secret) => {
    expect(maskForCopy(text)).not.toContain(secret);
  });

  it("숨긴 게 없는 줄은 서식 · 태그 · 링크 글자가 있어도 그대로 둔다", () => {
    const line = "Accept: */* · `GET` <b>ok</b> [문서](/docs) user_id=7";
    expect(maskForCopy(line)).toBe(line);
  });

  it.each([
    ["10진 줄바꿈", "password:&#10;synthetic-lf-secret", "synthetic-lf-secret"],
    ["16진 줄바꿈", "password:&#x0A;synthetic-hex-lf-secret", "synthetic-hex-lf-secret"],
    ["캐리지 리턴", "password:&#13;synthetic-cr-secret", "synthetic-cr-secret"],
    ["이름 엔티티 줄바꿈", "password:&NewLine;synthetic-named-lf-secret", "synthetic-named-lf-secret"],
    ["줄 구분 문자", "password:&#x2028;synthetic-ls-secret", "synthetic-ls-secret"],
    ["헤더 값이 다음 줄", "Authorization:&#10;Bearer synthetic-header-secret", "synthetic-header-secret"],
    ["= 앞에서 갈림", "token&#10;=synthetic-eq-secret", "synthetic-eq-secret"],
  ])("풀면 줄이 갈려 키와 값이 다른 줄이 되어도 가린다 — %s (#709 리뷰)", (_, line, secret) => {
    const out = maskForCopy(`HTTP/1.1 200 OK\n${line}\n본문 끝`);
    expect(out).not.toContain(secret);
    expect(out).not.toMatch(/&#|&NewLine;/);
    // 이어 붙여 가린 한 줄이라 붙여 넣어도 다시 갈라지지 않고, 앞뒤 줄은 그대로다
    expect(out.split("\n")).toHaveLength(3);
    expect(out.split("\n")[0]).toBe("HTTP/1.1 200 OK");
    expect(out.split("\n")[2]).toBe("본문 끝");
  });

  it("숨긴 값 없는 엔티티 줄바꿈 · CRLF 줄은 원래 모양 그대로 둔다", () => {
    expect(maskForCopy("note:&#10;hello")).toBe("note:&#10;hello");
    expect(maskForCopy("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r")).toBe(
      "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r",
    );
  });

  it("너무 깊게 감싼 줄은 안전을 확인하지 못해 복사하지 않는다", () => {
    const deep = `&${"amp;".repeat(12)}#112;assword=synthetic-deep-secret`;
    const out = maskForCopy(`앞 줄\n${deep}`);
    expect(out).toBe(`앞 줄\n${UNVERIFIED_LINE}`);
  });

  it("빈 글은 그대로, 두 번 걸어도 같다", () => {
    expect(maskForCopy("")).toBe("");
    const once = maskForCopy(`${ENTITY_KEY}=synthetic-entity-secret`);
    expect(maskForCopy(once)).toBe(once);
  });
});

// #975: 화면은 가리지 않으므로 그려진 Markdown 상자의 글을 블록 · 표 줄 단위로 모아 가린다(innerText 에 기대지 않음)
describe("renderedCopyText — 그려진 상자 복사", () => {
  const box = (html: string) => {
    const el = document.createElement("div");
    el.innerHTML = html;
    return el;
  };
  it("문단 안 줄바꿈으로 갈린 키 · 값을 한 덩어리로 가린다", () => {
    const out = renderedCopyText(box("<p>password:\nsynthetic-copy-newline</p><p>다음 문단</p>"));
    expect(out).not.toContain("synthetic-copy-newline");
    expect(out).toContain(REDACTED);
    expect(out.split("\n")).toEqual(["password: [REDACTED]", "다음 문단"]);
  });
  it("표는 줄마다 칸을 탭으로 갈라 붙은 칸 글에 숨은 키도 가린다", () => {
    const out = renderedCopyText(
      box(
        "<table><thead><tr><th>이름</th><th>값</th></tr></thead><tbody><tr><td>a</td><td>api_key=synthetic-copy-cell</td></tr></tbody></table>",
      ),
    );
    expect(out).not.toContain("synthetic-copy-cell");
    expect(out.split("\n")).toEqual(["이름\t값", `a\tapi_key=${REDACTED}`]);
  });
  it("코드 상자는 줄 모양을 지키고 줄마다 가린다", () => {
    const out = renderedCopyText(box("<pre><code>GET / HTTP/1.1\nCookie: s=synthetic-copy-cookie\nX: y</code></pre>"));
    expect(out.split("\n")).toEqual(["GET / HTTP/1.1", `Cookie: s=${REDACTED}`, "X: y"]);
  });
  // #975 이동찬 P2: 하위 목록이 있는 목록 칸 — 하위 목록 앞의 인라인 글(서식으로 나뉜 키와 값)을 한 덩어리로 가린다
  it("하위 목록이 있는 목록 칸도 그 앞 글을 한 덩어리로 가리고, 하위 목록은 따로 본다", () => {
    const out = renderedCopyText(
      box(
        "<ul><li><strong>pass</strong>word=synthetic-nested-secret<ul><li>token=synthetic-child-secret</li><li>하위 항목</li></ul></li></ul>",
      ),
    );
    expect(out).not.toContain("synthetic-nested-secret");
    expect(out).not.toContain("synthetic-child-secret");
    expect(out.split("\n")).toEqual([`password=${REDACTED}`, `token=${REDACTED}`, "하위 항목"]);
  });
  it("목록 칸 안의 문단은 문단 단위로, 숨길 것 없는 글은 그대로", () => {
    const out = renderedCopyText(
      box("<h2>재현 요약</h2><ul><li><p>계정 B로 주문 조회</p></li><li>token=synthetic-copy-li</li></ul>"),
    );
    expect(out.split("\n")).toEqual(["재현 요약", "계정 B로 주문 조회", `token=${REDACTED}`]);
  });
});
