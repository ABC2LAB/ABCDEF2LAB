import { describe, expect, it } from "vitest";

import {
  type ControlPolicy,
  getControlPolicy,
  normalizePolicy,
  policyExpired,
  ruleAllowsWrite,
  rulesMissingMethods,
  summarize,
} from "./control-policy";

const base: ControlPolicy = {
  policyRef: "p1",
  policyRevision: "r1",
  approvalMode: "workbench_v1",
  validFrom: "2026-11-01T00:00:00Z",
  validUntil: "2026-12-01T00:00:00Z",
  rules: [
    {
      ref: "a",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathPrefix: "/",
      pathScopeRef: null,
      methods: ["GET", "HEAD"],
      credentialHeaderNames: ["Authorization"],
      tlsPinned: false,
    },
    {
      ref: "b",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathPrefix: "/api/",
      pathScopeRef: "s",
      methods: ["GET", "POST"],
      credentialHeaderNames: [],
      tlsPinned: false,
    },
  ],
  limits: { maxRequests: 100, maxConcurrent: 2, outputLimit: 1024, budgetRef: "b1" },
  auditBound: true,
};

const at = (iso: string) => Date.parse(iso);

/** 비null 기대를 좁혀서 결과를 쓴다(입력이 unknown 이라 narrow 가 필요). */
function mustNormalize(raw: unknown): ControlPolicy {
  const p = normalizePolicy(raw);
  if (p === null) throw new Error("expected non-null policy");
  return p;
}

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

describe("normalizePolicy (fail-closed)", () => {
  it("policyRef 없으면 null", () => {
    expect(normalizePolicy(null)).toBeNull();
    expect(normalizePolicy({ rules: [] })).toBeNull();
  });
  it("깨진 규칙은 버리고, 알 수 없는 승인 방식은 가장 보수적(요청마다 확인)으로", () => {
    const n = mustNormalize({
      policyRef: "x",
      approvalMode: "weird",
      rules: [{ origin: "http://h" }, { ref: "ok", origin: "http://h", methods: ["get"] }],
    });
    expect(n.approvalMode).toBe("confirm_every");
    expect(n.rules).toHaveLength(1); // ref 없는 규칙 탈락
    expect(n.rules[0].methods).toEqual(["GET"]); // 대문자 정규화
  });
  it("음수·비정상 limit 은 0 으로 떨어진다", () => {
    expect(mustNormalize({ policyRef: "x", limits: { maxRequests: -5 } }).limits.maxRequests).toBe(0);
  });
});

describe("getControlPolicy (예시)", () => {
  it("mock 정책을 돌려주고 비밀 값은 없다(자격 헤더는 이름만)", async () => {
    const p = await getControlPolicy();
    expect(p.mock).toBe(true);
    for (const r of p.rules) {
      for (const h of r.credentialHeaderNames) {
        // 헤더 '이름'만 — 값(토큰/쿠키 문자열)이 아니다
        expect(h).toMatch(/^[A-Za-z-]+$/);
      }
    }
  });
});
