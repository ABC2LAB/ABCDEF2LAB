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
  it("admitted 는 단계·쓰기와 무관하게 allow 다", () => {
    expect(decisionClass(attempt({ decision: "admitted", stage: "sent", requestWrite: "complete" }))).toBe("allow");
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
  ])("거부 사유 %s 는 송신 전(pre_send)·쓰기 없음일 때만 deny 다", (d) => {
    expect(decisionClass(attempt({ decision: d, stage: "pre_send", requestWrite: "none" }))).toBe("deny");
  });

  // 윤재상 리뷰(#196): 송신 뒤 감사 실패를 "차단"으로 세지 않는다 — 이미 대상에 갔을 수 있음.
  it("audit_unavailable + sent(쓰기 partial) 은 deny 아니라 unknown 이다", () => {
    expect(decisionClass(attempt({ decision: "audit_unavailable", stage: "sent", requestWrite: "partial" }))).toBe(
      "unknown",
    );
  });

  it("거부 사유라도 쓰기가 관측되면(write≠none) 차단으로 단정하지 않고 unknown 이다", () => {
    expect(decisionClass(attempt({ decision: "revoked", stage: "reserved", requestWrite: "partial" }))).toBe("unknown");
  });

  it("거부 사유라도 송신/마감 단계면 unknown 이다(pre_send·reserved 만 deny)", () => {
    expect(decisionClass(attempt({ decision: "tls_rejected", stage: "sent", requestWrite: "none" }))).toBe("unknown");
    expect(decisionClass(attempt({ decision: "revoked", stage: "closed", requestWrite: "none" }))).toBe("unknown");
  });

  it("drain_unknown·unknown 은 allow 로 추정하지 않고 unknown 이다", () => {
    expect(decisionClass(attempt({ decision: "drain_unknown" }))).toBe("unknown");
    expect(decisionClass(attempt({ decision: "unknown" }))).toBe("unknown");
  });

  it("미등록 집행값은 색·허용 추정 없이 unknown 이다(fail-closed)", () => {
    const base = { stage: "pre_send", requestWrite: "none" } as const;
    expect(decisionClass(attempt({ decision: "confirmed" as E2Decision, ...base }))).toBe("unknown");
    expect(decisionClass(attempt({ decision: "" as E2Decision, ...base }))).toBe("unknown");
    expect(decisionClass(attempt({ decision: "constructor" as E2Decision, ...base }))).toBe("unknown");
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
        {
          callRef: "c1",
          tool: "Bash",
          finalized: true,
          // admitted → allow, revoked(송신 전·쓰기 없음) → deny
          attempts: [attempt(), attempt({ decision: "revoked", stage: "pre_send", requestWrite: "none" })],
        },
        { callRef: "c2", tool: "Bash", finalized: false, attempts: [attempt({ decision: "drain_unknown" })] },
      ]),
    );
    expect(s).toEqual({ calls: 2, attempts: 3, allow: 1, deny: 1, unknown: 1, pending: 1 });
  });

  it("송신 뒤 감사 실패(audit_unavailable + sent)는 deny 가 아니라 unknown 으로 집계한다", () => {
    const s = summarize(
      flow([
        {
          callRef: "c",
          tool: "Bash",
          finalized: false,
          attempts: [attempt({ decision: "audit_unavailable", stage: "sent", requestWrite: "partial" })],
        },
      ]),
    );
    expect(s.deny).toBe(0);
    expect(s.unknown).toBe(1);
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

  it("목업의 송신 뒤 감사 실패 시도는 차단이 아니라 unknown 으로 분류된다", async () => {
    const flow = await getE2Flow(null);
    const sentAudit = flow.calls.flatMap((c) => c.attempts).filter((a) => a.decision === "audit_unavailable");
    expect(sentAudit.length).toBeGreaterThan(0);
    for (const a of sentAudit) expect(decisionClass(a)).toBe("unknown");
  });

  it("목업 시도 ID 는 #189 §6.1 형식(소문자 hex 32자)이다", async () => {
    const flow = await getE2Flow(null);
    for (const a of flow.calls.flatMap((c) => c.attempts)) {
      expect(a.attemptId).toMatch(/^[0-9a-f]{32}$/);
    }
  });
});
