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
      methods: ["GET", "HEAD", "post"],
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
    expect(p?.rules[0].methods).toEqual(["GET", "HEAD", "POST"]); // 대문자 정규화
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
  ])("%s → null (보수적으로 추정해 그리지 않는다)", (_name, over) => {
    expect(parseControlPolicyView(wire(over))).toBeNull();
  });
  it.each([
    ["미지원 path_match", { path_match: "regex" }],
    ["null path_match", { path_match: null }],
    ["ref 없음", { ref: "" }],
    ["methods 배열 아님", { methods: "GET" }],
    ["tls_pinned 없음", { tls_pinned: undefined }],
  ])("규칙 하나가 깨지면(%s) 일부만 그리지 않고 정책 전체를 null", (_name, over) => {
    expect(parseControlPolicyView(wire({ rules: [...rule0({}), ...rule0({ ref: "b", ...over })] }))).toBeNull();
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
