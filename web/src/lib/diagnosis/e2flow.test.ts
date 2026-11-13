import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiFetch } from "./api";
import { decisionClass, type E2Reason, getE2Flow, mergeE2Flow, parseE2Flow, REASON_LABEL } from "./e2flow";
import { e2Attempt, e2Flow } from "./e2flow-fixtures";
vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => vi.resetAllMocks());
describe("계약 §4 사유·단계와 송신 관측", () => {
  it("16개 정본 사유를 정확히 유지한다", () => expect(Object.keys(REASON_LABEL)).toHaveLength(16));
  it.each<E2Reason>([
    "policy_denied",
    "unsupported",
    "invalid",
    "budget_exhausted",
    "attempt_limit",
    "denial_limit",
    "transient_limit",
    "unknown_limit",
    "closed",
    "expired",
    "revoked",
  ])("%s는 실제 송신 전 차단만 차단이다", (reason) => {
    const a = e2Attempt({
      reason,
      decision: "denied",
      forward_attempted: false,
      request_written: false,
      response_received: false,
      response_complete: false,
      status_code: 0,
    });
    expect(decisionClass(a)).toBe("deny");
    expect(decisionClass({ ...a, forward_attempted: true })).toBe("unknown");
    expect(decisionClass({ ...a, request_written: true })).toBe("unknown");
    expect(decisionClass({ ...a, stage: "unknown" })).toBe("unknown");
  });
  it.each<E2Reason>(["complete", "withheld"])("%s 허용을 응답·완전 쓰기 증거로 만들지 않는다", (reason) => {
    expect(decisionClass(e2Attempt({ reason }))).toBe("allow");
    expect(decisionClass(e2Attempt({ reason, decision: "unknown" }))).toBe("unknown");
  });
  it("C02 연결 실패는 송신 실패, 송신 뒤 실패는 불명이다", () => {
    const a = e2Attempt({
      reason: "transport_failed",
      forward_attempted: false,
      request_written: false,
      response_received: false,
    });
    expect(decisionClass(a)).toBe("transient");
    expect(decisionClass({ ...a, forward_attempted: true })).toBe("unknown");
  });
  it.each(["audit_failed", "unknown"] as const)("%s는 성공·차단으로 승격하지 않는다", (reason) =>
    expect(decisionClass(e2Attempt({ reason }))).toBe("unknown"));
  it("알 수 없는 reason/stage/decision을 원문 표시 없이 불명으로 정규화한다", () => {
    const value = e2Flow();
    Object.assign(value.calls[0].attempts[0], {
      reason: "secret-new-reason",
      stage: "new-stage",
      decision: "new-decision",
    });
    const a = parseE2Flow(value).calls[0].attempts[0];
    expect(a.reason).toBe("unknown");
    expect(a.stage).toBe("unknown");
    expect(decisionClass(a)).toBe("unknown");
  });
  it("가린 경로가 없으면 빈 값이며 원문 query를 거절한다", () => {
    const v = e2Flow();
    v.calls[0].attempts = [e2Attempt({ path_pattern: "" })];
    expect(parseE2Flow(v).calls[0].attempts[0].path_pattern).toBe("");
    v.calls[0].attempts = [e2Attempt({ path_pattern: "/orders?token=private" })];
    expect(() => parseE2Flow(v)).toThrow();
  });
});
describe("사건 번호 커서와 증분 병합", () => {
  it("첫 조회는 after 없이, 다음은 sequence가 아닌 last_index로 요청한다", async () => {
    vi.mocked(apiFetch).mockResolvedValue(e2Flow({ last_index: 51, omitted_calls: 3 }));
    const first = await getE2Flow("42");
    await getE2Flow("42", first.last_index);
    expect(vi.mocked(apiFetch).mock.calls[0][0]).toBe("/api/tasks/42/e2-flow");
    expect(vi.mocked(apiFetch).mock.calls[1][0]).toBe("/api/tasks/42/e2-flow?after=51");
    expect(vi.mocked(apiFetch).mock.calls[0][2]).toBe(false);
  });
  it("같은 시도의 큰 index로 교체하며 같은 call을 합치고 응답 누계를 더하지 않는다", () => {
    const first = e2Flow({ last_index: 10, omitted_calls: 4 });
    first.calls[0].attempts = [e2Attempt({ index: 9, stage: "intent", response_received: false })];
    const next = e2Flow({ last_index: 11 });
    next.calls[0].attempts = [e2Attempt({ index: 11 })];
    const merged = mergeE2Flow(first, next);
    expect(merged.calls).toHaveLength(1);
    expect(merged.calls[0].attempts).toHaveLength(1);
    expect(merged.calls[0].attempts[0].index).toBe(11);
    expect(merged.counts.attempts).toBe(1);
    expect(merged.omitted_calls).toBe(4);
    expect(mergeE2Flow(merged, next).calls[0].attempts).toHaveLength(1);
  });
  it("역행 커서와 다른 intent의 같은 call은 거절한다", () => {
    expect(() => mergeE2Flow(e2Flow({ last_index: 11 }), e2Flow())).toThrow();
    const other = e2Flow();
    other.calls[0].intent_id = 10;
    expect(() => mergeE2Flow(e2Flow(), other)).toThrow();
  });
  it("닫힘 사건만 와도 기존 시도와 마감을 보존한다", () => {
    const closed = e2Flow({
      last_index: 3,
      calls: [{ call_ref: "call-1", intent_id: 9, closed: true, closed_index: 3, attempts: [] }],
    });
    const merged = mergeE2Flow(e2Flow(), parseE2Flow(closed));
    expect(merged.calls[0].closed).toBe(true);
    expect(merged.calls[0].attempts).toHaveLength(1);
  });
  it("invalid 저널은 이전 사건·커서를 모두 지운다", () => {
    const invalid = parseE2Flow(e2Flow({ journal: "invalid", last_index: 123, more: true }));
    expect(mergeE2Flow(e2Flow(), invalid)).toMatchObject({ journal: "invalid", calls: [], last_index: 0, more: false });
  });
  it.each([null, "", "042", "42/43", "-1"])("신뢰할 task가 없으면 API·예시를 호출하지 않는다: %s", async (task) => {
    await expect(getE2Flow(task)).rejects.toThrow();
    expect(apiFetch).not.toHaveBeenCalled();
  });
  it("서버 거절은 합성 흐름으로 바꾸지 않는다", async () => {
    vi.mocked(apiFetch).mockRejectedValue(new Error("fixed-failure"));
    await expect(getE2Flow("42")).rejects.toThrow();
  });
});
