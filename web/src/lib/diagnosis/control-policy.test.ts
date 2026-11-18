import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  apiFetch: vi.fn(),
}));

import { ApiError, apiFetch } from "./api";
import {
  CONTROL_POLICY_VIEW_FORMAT,
  type ControlPolicy,
  getControlPolicy,
  parseControlPolicyView,
  policyExpired,
  ruleAllowsWrite,
  rulePathText,
  rulesMissingMethods,
  summarize,
  validConnectIp,
  validOrigin,
} from "./control-policy";

afterEach(() => vi.mocked(apiFetch).mockReset());

const base: ControlPolicy = {
  policyRef: "p1",
  policyRevision: "r1",
  fingerprint: "",
  approvalMode: "workbench_selective_v1",
  headerMode: "preserve",
  validFrom: "2026-11-01T00:00:00Z",
  validUntil: "2026-12-01T00:00:00Z",
  rules: [
    {
      ref: "a",
      purpose: "",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathMatch: "prefix",
      pathPrefix: "/",
      pathScopeRef: null,
      methods: ["GET", "HEAD"],
      tlsPinned: false,
    },
    {
      ref: "b",
      purpose: "",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathMatch: "prefix",
      pathPrefix: "/api/",
      pathScopeRef: "s",
      methods: ["GET", "POST"],
      tlsPinned: false,
    },
  ],
  limits: {
    maxRequests: 100,
    maxAttempts: 200,
    maxConcurrent: 2,
    maxBodyBytes: 1024,
    maxResponseBodyBytes: 1024,
    requestTimeoutMs: 5000,
  },
  accounts: [],
  auditBound: true,
};

const at = (iso: string) => Date.parse(iso);

describe("policyExpired (fail-closed)", () => {
  it("유효기간 안은 false, 밖(만료·미발효)은 true", () => {
    expect(policyExpired(base, at("2026-11-15T00:00:00Z"))).toBe(false);
    expect(policyExpired(base, at("2026-12-02T00:00:00Z"))).toBe(true); // 만료
    expect(policyExpired(base, at("2026-10-31T00:00:00Z"))).toBe(true); // 미발효
  });
  it("날짜 파싱 불가는 안전쪽(만료)으로 본다", () => {
    expect(policyExpired({ ...base, validUntil: "nope" }, at("2026-11-15T00:00:00Z"))).toBe(true);
  });
});

describe("쓰기 범위·메서드", () => {
  it("ruleAllowsWrite: 읽기 전용(GET/HEAD)은 false, POST 포함은 true", () => {
    expect(ruleAllowsWrite(base.rules[0])).toBe(false);
    expect(ruleAllowsWrite(base.rules[1])).toBe(true);
  });
  it("rulesMissingMethods: 빈 methods 규칙만 집어낸다('전부 허용' 오인 방지)", () => {
    const p = { ...base, rules: [base.rules[0], { ...base.rules[1], methods: [] }] };
    expect(rulesMissingMethods(p).map((r) => r.ref)).toEqual(["b"]);
  });
  it("summarize: origin 중복 제거·쓰기 규칙 수·만료 집계", () => {
    const s = summarize(base, at("2026-11-15T00:00:00Z"));
    expect(s).toEqual({ rules: 2, origins: 1, writeRules: 1, expired: false });
  });
});

// 서버 응답 모양(docs/contracts/workbench-live-data.md §3 예시와 같은 칸)
const wire = (over: Record<string, unknown> = {}): Record<string, unknown> => ({
  format: CONTROL_POLICY_VIEW_FORMAT,
  policy_ref: "scope-target-1",
  policy_revision: "rev-1",
  policy_fingerprint_sha256: "ab".repeat(32),
  approval_mode: "preauthorized_scope",
  header_mode: "test_account_passthrough",
  valid_from: "2026-11-13T00:00:00Z",
  valid_until: "2026-11-13T04:00:00Z",
  rules: [
    {
      ref: "target-path-a",
      purpose: "",
      origin: "https://shop.example",
      connect_ip: "10.0.0.5",
      path_prefix: "/api",
      path_scope_ref: "readonly-path-a",
      methods: ["GET", "HEAD", "POST"],
      tls_pinned: true,
    },
  ],
  limits: {
    max_requests: 64,
    max_attempts: 200,
    max_concurrent: 1,
    max_body_bytes: 65536,
    max_response_body_bytes: 16777216,
    request_timeout_ms: 5000,
  },
  accounts: ["tester-a"],
  audit_bound: true,
  ...over,
});
const rule0 = (over: Record<string, unknown>) => [{ ...(wire().rules as Record<string, unknown>[])[0], ...over }];

describe("parseControlPolicyView (fail-closed)", () => {
  it("계약 §3 응답을 화면 모양으로 바꾼다", () => {
    const p = parseControlPolicyView(wire());
    expect(p).toMatchObject({
      policyRef: "scope-target-1",
      approvalMode: "preauthorized_scope",
      headerMode: "test_account_passthrough",
      accounts: ["tester-a"],
      auditBound: true,
      limits: { maxRequests: 64, maxAttempts: 200, maxResponseBodyBytes: 16777216, requestTimeoutMs: 5000 },
    });
    expect(p?.rules[0]).toMatchObject({ connectIp: "10.0.0.5", pathMatch: "prefix", tlsPinned: true });
    expect(p?.rules[0].methods).toEqual(["GET", "HEAD", "POST"]);
    expect(p?.mock).toBeUndefined();
  });
  it("path_match exact 는 =/… 로 보이고, 생략은 접두", () => {
    const p = parseControlPolicyView(wire({ rules: rule0({ path_match: "exact", path_prefix: "/rest/basket/1" }) }));
    expect(p?.rules[0].pathMatch).toBe("exact");
    expect(p && rulePathText(p.rules[0])).toBe("=/rest/basket/1");
    expect(rulePathText(base.rules[1])).toBe("/api/");
  });
  it.each([
    ["형식이 다름", { format: "abc2lab.control-policy-view/v2" }],
    ["모르는 승인 방식", { approval_mode: "weird" }],
    ["모르는 헤더 방식", { header_mode: "strip" }],
    ["policy_ref 없음", { policy_ref: "" }],
    ["상한 음수", { limits: { ...(wire().limits as object), max_requests: -1 } }],
    ["상한 칸 빠짐", { limits: { max_requests: 1 } }],
    ["감사 결속 칸 없음", { audit_bound: undefined }],
    ["계정이 문자열 배열 아님", { accounts: [{ label: "a" }] }],
    ["모르는 최상위 칸", { approved_by: "someone" }],
    ["상한에 모르는 칸", { limits: { ...(wire().limits as object), max_bytes_total: 1 } }],
    ["지문이 SHA-256 16진 아님", { policy_fingerprint_sha256: "AB".repeat(32) }],
    ["지문이 빈 문자열", { policy_fingerprint_sha256: "" }],
    ["유효 시작이 시각 아님", { valid_from: "soon" }],
    ["유효 끝이 시간대 없는 시각", { valid_until: "2026-11-13T04:00:00" }],
  ])("%s → null (보수적으로 추정해 그리지 않는다)", (_name, over) => {
    expect(parseControlPolicyView(wire(over))).toBeNull();
  });
  it.each([
    ["미지원 path_match", { path_match: "regex" }],
    ["null path_match", { path_match: null }],
    ["ref 없음", { ref: "" }],
    ["methods 배열 아님", { methods: "GET" }],
    ["tls_pinned 없음", { tls_pinned: undefined }],
    ["모르는 purpose", { purpose: "admin_only" }],
    ["purpose null", { purpose: null }],
    ["빈 경로(넓은 범위로 보일 수 있음)", { path_prefix: "" }],
    ["origin 에 경로", { origin: "https://shop.example/api" }],
    ["origin scheme 밖", { origin: "ftp://shop.example" }],
    ["경로가 / 로 시작 안 함", { path_prefix: "api" }],
    ["소문자 메서드", { methods: ["get"] }],
    ["path_scope_ref 없음", { path_scope_ref: undefined }],
    ["규칙에 모르는 칸", { header_value: "x" }],
    // 서버(e2proxy compileRules)가 실행에 올릴 수 없는 규칙 — #689 리뷰
    [
      "auth_prep 에 정확 경로 칸",
      { purpose: "auth_prep", path_match: "exact", path_prefix: "/rest/user/login", methods: ["POST"] },
    ],
    ["auth_prep 에 GET", { purpose: "auth_prep", path_prefix: "/rest/user/login", methods: ["GET"] }],
    ["auth_prep 에 POST 말고 더", { purpose: "auth_prep", path_prefix: "/rest/user/login", methods: ["POST", "PUT"] }],
    ["auth_prep 이 루트 경로", { purpose: "auth_prep", path_prefix: "/", methods: ["POST"] }],
    ["auth_prep 이 접두 경로(/로 끝남)", { purpose: "auth_prep", path_prefix: "/rest/", methods: ["POST"] }],
    ["IP 형식 아닌 connect_ip", { connect_ip: "shop.example" }],
    ["빈 connect_ip", { connect_ip: "" }],
    ["IPv4 앞자리 0", { connect_ip: "010.0.0.5" }],
    ["IPv4 범위 밖", { connect_ip: "10.0.0.256" }],
    ["미지정 IPv4", { connect_ip: "0.0.0.0" }],
    ["미지정 IPv6", { connect_ip: "::" }],
    ["멀티캐스트 IPv4", { connect_ip: "224.0.0.1" }],
    ["멀티캐스트 IPv6", { connect_ip: "ff02::1" }],
    ["zone 붙은 IPv6", { connect_ip: "fe80::1%eth0" }],
    ["E② 지원 밖 대문자 메서드", { methods: ["TRACE"] }],
    ["E② 지원 밖 대문자 메서드(CONNECT)", { methods: ["GET", "CONNECT"] }],
    ["빈 메서드", { methods: [] }],
    ["중복 메서드", { methods: ["GET", "GET"] }],
    ["경로에 .. 조각", { path_prefix: "/api/../admin" }],
    ["경로에 // ", { path_prefix: "/api//x" }],
    ["경로에 %", { path_prefix: "/api/%2e" }],
    ["http 대상에 TLS 핀", { origin: "http://shop.example", tls_pinned: true }],
    // 2차 리뷰 반례: 서버 canonicalAuthority·validRef 가 거부하는 값
    ["origin 에 사용자 정보", { origin: "https://user:pw@shop.example" }],
    ["origin 에 사용자 이름만", { origin: "https://user@shop.example" }],
    ["빈 path_scope_ref", { path_scope_ref: "" }],
    ["path_scope_ref 에 ://", { path_scope_ref: "scope://x" }],
    ["ref 가 영숫자로 시작 안 함", { ref: "-a" }],
    ["origin 호스트에 밑줄", { origin: "https://shop_example.test" }],
    ["origin 포트 0", { origin: "https://shop.example:0" }],
    ["origin 포트 범위 밖", { origin: "https://shop.example:65536" }],
    ["origin 에 query", { origin: "https://shop.example/?a" }],
  ])("규칙 하나가 깨지면(%s) 일부만 그리지 않고 정책 전체를 null", (_name, over) => {
    expect(parseControlPolicyView(wire({ rules: [...rule0({}), ...rule0({ ref: "b", ...over })] }))).toBeNull();
  });
  it.each([
    ["IPv6", "2001:db8::5"],
    ["IPv4 대응 IPv6", "::ffff:10.0.0.5"],
    ["루프백 IPv4", "127.0.0.1"],
  ])("서버가 받는 연결 IP(%s)는 그대로 통과", (_name, ip) => {
    expect(parseControlPolicyView(wire({ rules: rule0({ connect_ip: ip }) }))?.rules[0].connectIp).toBe(ip);
  });
  it("인증 준비 규칙은 로그인 경로 하나에 POST 만이면 받는다", () => {
    const p = parseControlPolicyView(
      wire({ rules: rule0({ purpose: "auth_prep", path_prefix: "/rest/user/login", methods: ["POST"] }) }),
    );
    expect(p?.rules[0]).toMatchObject({ purpose: "auth_prep", pathMatch: "prefix", methods: ["POST"] });
  });
  it("멀티캐스트 대응 IPv6(::ffff:224.0.0.1)도 거부", () => {
    expect(validConnectIp("::ffff:224.0.0.1")).toBe(false);
    expect(validConnectIp("::ffff:0.0.0.0")).toBe(false);
  });
  it("지문 생략(자격 pin 규칙)은 받고 빈 지문으로 둔다", () => {
    const { policy_fingerprint_sha256: _omit, ...rest } = wire();
    expect(parseControlPolicyView(rest)?.fingerprint).toBe("");
  });
  it("빈 규칙 목록은 '확인 불가'(서버는 규칙 없는 정책을 실행에 올리지 않는다)", () => {
    expect(parseControlPolicyView(wire({ rules: [] }))).toBeNull();
  });
  it("규칙 참조가 겹치면 '확인 불가'", () => {
    expect(parseControlPolicyView(wire({ rules: [...rule0({ ref: "dup" }), ...rule0({ ref: "dup" })] }))).toBeNull();
  });
  it("규칙이 129개면 '확인 불가'(서버 상한 128)", () => {
    const many = Array.from({ length: 129 }, (_, i) => rule0({ ref: `r${i}` })[0]);
    expect(parseControlPolicyView(wire({ rules: many }))).toBeNull();
    expect(parseControlPolicyView(wire({ rules: many.slice(0, 128) }))?.rules).toHaveLength(128);
  });
  it.each([
    ["끝 슬래시", "https://shop.example/"],
    ["포트", "http://localhost:3000"],
    ["대괄호 IPv6", "http://[::1]:8080"],
    ["대문자 호스트", "https://Shop.Example"],
  ])("서버가 받는 origin(%s)은 통과", (_name, origin) => {
    expect(validOrigin(origin)).toBe(true);
    // http 대상은 TLS 핀이 결속될 수 없으므로 핀 없이 본다
    expect(parseControlPolicyView(wire({ rules: rule0({ origin, tls_pinned: false }) }))?.rules[0].origin).toBe(origin);
  });
  it("null·배열은 null", () => {
    expect(parseControlPolicyView(null)).toBeNull();
    expect(parseControlPolicyView([])).toBeNull();
  });
});

describe("getControlPolicy", () => {
  it("task 가 없으면 예시(mock)를 보내지 않고 돌려준다", async () => {
    const r = await getControlPolicy(null);
    expect(r.state === "ready" && r.policy.mock).toBe(true);
    expect(apiFetch).not.toHaveBeenCalled();
  });
  it("task 의 control-policy 경로를 읽어 정규화한다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(wire());
    const r = await getControlPolicy("42");
    expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe("/api/tasks/42/control-policy");
    expect(r.state === "ready" && r.policy.policyRef).toBe("scope-target-1");
  });
  it("숫자가 아닌 task 는 보내지 않고 invalid", async () => {
    expect(await getControlPolicy("../1")).toEqual({ state: "invalid" });
    expect(apiFetch).not.toHaveBeenCalled();
  });
  it("404 는 결속 전(unbound), 409 는 승인과 다름(mismatch), 깨진 본문은 invalid", async () => {
    vi.mocked(apiFetch).mockRejectedValueOnce(new ApiError(404, "run_not_bound"));
    expect(await getControlPolicy("1")).toEqual({ state: "unbound" });
    vi.mocked(apiFetch).mockRejectedValueOnce(new ApiError(409, "policy_mismatch"));
    expect(await getControlPolicy("1")).toEqual({ state: "mismatch" });
    vi.mocked(apiFetch).mockResolvedValueOnce({ format: CONTROL_POLICY_VIEW_FORMAT });
    expect(await getControlPolicy("1")).toEqual({ state: "invalid" });
  });
  it("그 밖의 실패는 그대로 던진다(화면이 '불러오지 못함')", async () => {
    vi.mocked(apiFetch).mockRejectedValueOnce(new ApiError(500, "x"));
    await expect(getControlPolicy("1")).rejects.toThrow("x");
  });
});
