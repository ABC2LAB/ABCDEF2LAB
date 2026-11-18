import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, apiFetch } from "./api";
import {
  type ControlPolicy,
  getControlPolicy,
  parseControlPolicy,
  policyExpired,
  ruleAllowsWrite,
  rulesMissingMethods,
  summarize,
} from "./control-policy";
import { serverPolicy } from "./control-policy-fixtures";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => vi.resetAllMocks());

const rule = (i: number, over: Record<string, unknown>) =>
  serverPolicy({
    rules: (serverPolicy().rules as Record<string, unknown>[]).map((r, n) => (n === i ? { ...r, ...over } : r)),
  });
const base: ControlPolicy = parseControlPolicy(serverPolicy());
const at = (iso: string) => Date.parse(iso);

describe("parseControlPolicy (계약 §3)", () => {
  it("서버 칸을 그대로 옮긴다 — 경로 맞춤 생략은 접두, 계정은 라벨만", () => {
    expect(base).toEqual({
      policyRef: "scope-target-1",
      policyRevision: "rev-1",
      fingerprint: "a".repeat(64),
      approvalMode: "preauthorized_scope",
      headerMode: "preserve",
      validFrom: "2026-11-01T00:00:00Z",
      validUntil: "2026-12-01T00:00:00Z",
      rules: [
        {
          ref: "target-path-a",
          purpose: "",
          origin: "https://shop.example",
          connectIp: "10.0.0.5",
          pathMatch: "prefix",
          pathPrefix: "/api",
          pathScopeRef: "readonly-path-a",
          methods: ["GET", "HEAD"],
          tlsPinned: true,
        },
        expect.objectContaining({ ref: "target-path-b", pathScopeRef: "", methods: ["GET", "POST"] }),
      ],
      limits: {
        maxRequests: 64,
        maxAttempts: 200,
        maxConcurrent: 1,
        maxBodyBytes: 65536,
        maxResponseBodyBytes: 16777216,
        requestTimeoutMs: 5000,
      },
      accounts: ["tester-a"],
      auditBound: true,
    });
  });
  it("정확 경로 · 인증 준비 규칙 · 지문 생략 · 시간대 있는 시각을 받는다", () => {
    const { policy_fingerprint_sha256: _, ...noPrint } = rule(1, {
      path_match: "exact",
      purpose: "auth_prep",
      methods: ["POST"],
    });
    const p = parseControlPolicy({ ...noPrint, valid_from: "2026-11-01T09:00:00.123456789+09:00" });
    expect(p.fingerprint).toBeNull();
    expect(p.rules[1]).toMatchObject({ pathMatch: "exact", purpose: "auth_prep", methods: ["POST"] });
  });
  it.each([
    ["모르는 최상위 키", serverPolicy({ mock: true })],
    ["빠진 칸", (({ accounts: _, ...rest }) => rest)(serverPolicy())],
    ["다른 형식", serverPolicy({ format: "abc2lab.control-policy-view/v2" })],
    ["빈 정책 참조", serverPolicy({ policy_ref: "" })],
    ["모르는 승인 방식", serverPolicy({ approval_mode: "confirm_every" })],
    ["모르는 자격 헤더 처리", serverPolicy({ header_mode: "passthrough" })],
    ["깨진 시각", serverPolicy({ valid_until: "2026-13-01" })],
    ["규칙 null", serverPolicy({ rules: null })],
    ["계정 값이 문자열이 아님", serverPolicy({ accounts: [{ label: "a", password: "x" }] })],
    ["짧은 지문", serverPolicy({ policy_fingerprint_sha256: "abc" })],
    ["규칙의 모르는 키", rule(0, { credential: "Bearer synthetic" })],
    ["규칙의 다른 경로 맞춤", rule(0, { path_match: "prefix" })],
    ["규칙의 모르는 목적", rule(0, { purpose: "model" })],
    ["소문자 메서드", rule(0, { methods: ["get"] })],
    ["경로가 / 로 시작하지 않음", rule(0, { path_prefix: "api" })],
    ["origin 에 경로", rule(0, { origin: "https://shop.example/api" })],
    ["음수 상한", serverPolicy({ limits: { ...(serverPolicy().limits as object), max_requests: -1 } })],
    ["상한의 모르는 키", serverPolicy({ limits: { ...(serverPolicy().limits as object), budget_ref: "b" } })],
  ])("계약과 다르면 응답 전체를 거부한다 — %s", (_, raw) => {
    expect(() => parseControlPolicy(raw)).toThrow(ApiError);
  });
  it("규칙 하나만 깨져도 나머지를 보이지 않는다(일부만 보이면 범위가 달리 읽힌다)", () => {
    expect(() => parseControlPolicy(rule(1, { methods: "GET" }))).toThrow("invalid_control_policy_contract");
  });
});

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
    const p = parseControlPolicy(rule(1, { methods: [] }));
    expect(rulesMissingMethods(p).map((r) => r.ref)).toEqual(["target-path-b"]);
  });
  it("summarize: origin 중복 제거·쓰기 규칙 수·만료 집계", () => {
    expect(summarize(base, at("2026-11-15T00:00:00Z"))).toEqual({
      rules: 2,
      origins: 1,
      writeRules: 1,
      expired: false,
    });
  });
});

describe("getControlPolicy", () => {
  it("현재 task 의 정책 경로를 캐시 없이 · 리다이렉트 없이 읽는다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(serverPolicy());
    const signal = new AbortController().signal;
    await expect(getControlPolicy("42", signal)).resolves.toEqual(base);
    expect(apiFetch).toHaveBeenCalledWith(
      "/api/tasks/42/control-policy",
      { cache: "no-store", redirect: "error", signal },
      false,
    );
  });
  it.each([
    "",
    "0",
    "042",
    "4.2",
    "1e3",
    "task-42",
    "9007199254740993",
  ])("정규 양의 정수가 아닌 task(%s)는 부르지 않는다", async (task) => {
    await expect(getControlPolicy(task)).rejects.toThrow(ApiError);
    expect(apiFetch).not.toHaveBeenCalled();
  });
  it.each([404, 409, 403, 503])("HTTP %s 는 빈 정책으로 바꾸지 않고 그대로 던진다", async (status) => {
    vi.mocked(apiFetch).mockRejectedValue(new ApiError(status, "x"));
    await expect(getControlPolicy("42")).rejects.toMatchObject({ status });
  });
});
