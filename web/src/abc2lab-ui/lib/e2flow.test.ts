import { describe, expect, it } from "vitest";

import {
  type AuditStore,
  decisionClass,
  type E2Attempt,
  type E2Decision,
  type E2Flow,
  type E2Stage,
  getE2Flow,
  normalizeAttempt,
  summarize,
} from "./e2flow";

// E② 전송 통제 흐름 투영의 fail-closed 규칙 검증(#183 §5·#189 §5).
// 목업/계약 shape 에 대한 로직 테스트이며, 실제 집행 결과 건수를 증명하는 자료는 아니다.

function attempt(over: Partial<E2Attempt> = {}): E2Attempt {
  return {
    attemptId: "a",
    stage: "closed",
    decision: "admitted",
    observedAt: 1,
    reserved: true,
    requestWrite: "complete",
    response: "observed",
    auditStore: "acked",
    ...over,
  };
}

describe("decisionClass", () => {
  it("admitted 만 allow 다", () => {
    expect(decisionClass("admitted")).toBe("allow");
  });

  it.each<E2Decision>([
    "unsupported_path",
    "approval_unavailable",
    "revoked",
    "expired",
    "binding_mismatch",
    "budget_unavailable",
    "budget_exhausted",
    "audit_unavailable",
    "tls_rejected",
  ])("거부 사유 %s 는 deny 다", (d) => {
    expect(decisionClass(d)).toBe("deny");
  });

  it("drain_unknown·unknown 은 allow 로 추정하지 않고 unknown 이다", () => {
    expect(decisionClass("drain_unknown")).toBe("unknown");
    expect(decisionClass("unknown")).toBe("unknown");
  });

  it("미등록 집행값은 색·허용 추정 없이 unknown 이다(fail-closed)", () => {
    expect(decisionClass("confirmed" as E2Decision)).toBe("unknown");
    expect(decisionClass("" as E2Decision)).toBe("unknown");
    expect(decisionClass("constructor" as E2Decision)).toBe("unknown");
  });
});

describe("normalizeAttempt", () => {
  it("정상 값은 그대로 둔다", () => {
    const a = attempt();
    expect(normalizeAttempt(a)).toEqual(a);
  });

  it("미등록 decision 은 unknown 으로 내린다(허용으로 추정 금지)", () => {
    expect(normalizeAttempt(attempt({ decision: "safe" as E2Decision })).decision).toBe("unknown");
  });

  it("미등록 stage 는 단계 불명(unknown)으로 내린다", () => {
    expect(normalizeAttempt(attempt({ stage: "weird" as E2Stage })).stage).toBe("unknown");
  });

  it("미등록 감사 저장 상태는 절대 acked 로 올리지 않고 불명으로 내린다", () => {
    expect(normalizeAttempt(attempt({ auditStore: "stored" as AuditStore })).auditStore).toBe("unknown");
  });

  it("reserved 는 엄격히 true 일 때만 true (truthy 추정 금지)", () => {
    expect(normalizeAttempt(attempt({ reserved: 1 as unknown as boolean })).reserved).toBe(false);
  });

  it("관측시각이 숫자가 아니면 0 으로 둔다(NaN 방지)", () => {
    expect(normalizeAttempt(attempt({ observedAt: Number.NaN })).observedAt).toBe(0);
  });
});

describe("summarize", () => {
  const flow = (calls: E2Flow["calls"]): E2Flow => ({
    runRef: "r",
    policyVersion: "t",
    mock: true,
    calls,
  });

  it("3분류와 미완료(중간) 호출 수를 센다", () => {
    const s = summarize(
      flow([
        { callRef: "c1", tool: "Bash", finalized: true, attempts: [attempt(), attempt({ decision: "revoked" })] },
        { callRef: "c2", tool: "Bash", finalized: false, attempts: [attempt({ decision: "drain_unknown" })] },
      ]),
    );
    expect(s).toEqual({ calls: 2, attempts: 3, allow: 1, deny: 1, unknown: 1, pending: 1 });
  });

  it("미등록 집행값이 섞여도 합계가 숫자로 유지되고 unknown 으로 내려간다", () => {
    const s = summarize(
      flow([
        {
          callRef: "c1",
          tool: "Bash",
          finalized: true,
          attempts: [attempt({ decision: "confirmed" as E2Decision }), attempt({ decision: "" as E2Decision })],
        },
      ]),
    );
    expect(Number.isFinite(s.attempts)).toBe(true);
    expect(s.unknown).toBe(2);
    expect(s.allow).toBe(0);
  });
});

describe("getE2Flow mock", () => {
  it("예시 흐름은 통과·차단·불명을 모두 담고, 미완료 호출(배수/감사 불명)을 노출한다", async () => {
    const s = summarize(await getE2Flow(null));
    expect(s.allow).toBeGreaterThan(0);
    expect(s.deny).toBeGreaterThan(0);
    expect(s.unknown).toBeGreaterThan(0);
    expect(s.pending).toBeGreaterThan(0);
  });
});
