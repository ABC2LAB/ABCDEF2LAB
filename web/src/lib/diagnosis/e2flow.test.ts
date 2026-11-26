import { beforeEach, describe, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import {
  decisionClass,
  type E2Reason,
  type E2TargetRequests,
  getE2Flow,
  mergeE2Flow,
  parseE2Flow,
  REASON_LABEL,
  sentRequestsOf,
} from "./e2flow";
import { e2Attempt, e2Flow } from "./e2flow-fixtures";

vi.mock("./api", async (original) => ({ ...(await original<typeof import("./api")>()), apiFetch: vi.fn() }));
beforeEach(() => vi.resetAllMocks());
describe("계약 §4 사유·단계와 송신 관측", () => {
  it("17개 정본 사유를 정확히 유지한다", () => expect(Object.keys(REASON_LABEL)).toHaveLength(17));
  it.each<E2Reason>([
    "policy_denied",
    "workflow_paused",
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
    expect(decisionClass({ ...a, forward_attempted: true })).toBe("transient");
    expect(decisionClass({ ...a, forward_attempted: true, request_written: true })).toBe("unknown");
    expect(decisionClass({ ...a, forward_attempted: true, response_received: true })).toBe("unknown");
  });
  it.each(["audit_failed", "unknown"] as const)("%s는 성공·차단으로 승격하지 않는다", (reason) =>
    expect(decisionClass(e2Attempt({ reason }))).toBe("unknown"));
  it("일시정지 차단 사유를 읽고 화면에 구분한다", () => {
    const value = e2Flow();
    const a = e2Attempt({
      reason: "workflow_paused",
      decision: "denied",
      forward_attempted: false,
      request_written: false,
      response_received: false,
      response_complete: false,
      status_code: 0,
    });
    value.calls[0].attempts = [a];
    const parsed = parseE2Flow(value).calls[0].attempts[0];
    expect(parsed.reason).toBe("workflow_paused");
    expect(REASON_LABEL[parsed.reason]).toBe("진단 흐름 일시정지");
    expect(decisionClass(parsed)).toBe("deny");
  });
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

describe("실제 intent/마감 사건과 계약 엄격성", () => {
  it("allowed+complete intent는 송신 전 기록이며 응답 성공이 아니다", () => {
    expect(
      decisionClass(
        e2Attempt({
          stage: "intent",
          reason: "complete",
          decision: "allowed",
          forward_attempted: false,
          request_written: false,
          response_received: false,
          response_complete: false,
          status_code: 0,
        }),
      ),
    ).toBe("unknown");
  });
  it("intent 뒤 allowed+closed 결과가 송신 전이면 실행 닫힘 차단이다", () => {
    expect(
      decisionClass(
        e2Attempt({
          stage: "result",
          reason: "closed",
          decision: "allowed",
          forward_attempted: false,
          request_written: false,
          response_received: false,
          response_complete: false,
          status_code: 0,
        }),
      ),
    ).toBe("deny");
  });
  it("결과 행도 응답 관측 없이는 응답 성공으로 올리지 않는다", () =>
    expect(decisionClass(e2Attempt({ response_received: false, response_complete: false, status_code: 0 }))).toBe(
      "unknown",
    ));
  it.each([
    "attempt-extra",
    "counts-extra",
    "future-index",
    "future-close-index",
  ])("계약 밖 키/사건 번호를 거절한다: %s", (kind) => {
    const f = e2Flow();
    if (kind === "attempt-extra") Object.assign(f.calls[0].attempts[0], { command: "private" });
    if (kind === "counts-extra") Object.assign(f.counts, { extra: 1 });
    if (kind === "future-index") f.calls[0].attempts[0].index = f.last_index + 1;
    if (kind === "future-close-index") Object.assign(f.calls[0], { closed: true, closed_index: f.last_index + 1 });
    expect(() => parseE2Flow(f)).toThrow();
  });
  it("중복 이전 사건은 최신 시도를 덮지 않고 마감도 되돌리지 않는다", () => {
    const closed = e2Flow({ last_index: 3 });
    Object.assign(closed.calls[0], { closed: true, closed_index: 3 });
    closed.calls[0].attempts = [e2Attempt({ index: 2, path_pattern: "/latest" })];
    const late = e2Flow({ last_index: 4 });
    late.calls[0].attempts = [e2Attempt({ index: 1, path_pattern: "/old" })];
    const merged = mergeE2Flow(closed, late);
    expect(merged.calls[0]).toMatchObject({ closed: true, closed_index: 3 });
    expect(merged.calls[0].attempts[0].path_pattern).toBe("/latest");
  });
});

// #929: 검증된 응답의 target_requests(대상 요청 내역). 서버 url_target_requests.go 규칙과 같게 닫는다
describe("target_requests — 대상 요청 내역(계약 workbench-live-data §4)", () => {
  const tr = (over: Partial<E2TargetRequests> = {}): E2TargetRequests => ({
    model: 50,
    login: 6,
    confirmation: 0,
    unclassified: 0,
    exploration: 0,
    exploration_state: "none",
    total: 56,
    ...over,
  });
  const withTR = (t: unknown) => ({ ...e2Flow(), target_requests: t });

  it.each([
    ["탐색 없음(none) — 11/24 회차 2와 같은 모양", tr()],
    ["탐색 열지 않음(not_run)", tr({ exploration_state: "not_run" })],
    ["탐색 확정(closed)", tr({ exploration: 30, exploration_state: "closed", total: 86 })],
    ["탐색 중 경계 수 있음", tr({ exploration: 12, exploration_state: "running", total: 68 })],
    ["탐색 중 경계 수 없음 → 총계 모름", tr({ exploration: null, exploration_state: "running", total: null })],
    ["모름(unknown) → 총계 모름", tr({ exploration: null, exploration_state: "unknown", total: null })],
    ["분류 안 된 프로그램 요청 → 총계 모름", tr({ unclassified: 2, total: null })],
  ])("받는다: %s", (_, value) => {
    expect(parseE2Flow(withTR(value)).target_requests).toEqual(value);
  });

  it("내역 칸이 없는 응답(#929 이전 서버)도 받는다", () => {
    expect(parseE2Flow(e2Flow()).target_requests).toBeUndefined();
  });

  it.each([
    ["총계가 합과 다름", tr({ total: 50 })],
    ["탐색 수를 모르는데 총계 있음", tr({ exploration: null, exploration_state: "running", total: 50 })],
    ["분류 안 된 요청이 있는데 총계 있음", tr({ unclassified: 1, total: 57 })],
    ["none 인데 탐색 수가 0 이 아님", tr({ exploration: 5, total: 61 })],
    ["not_run 인데 탐색 수 모름", tr({ exploration_state: "not_run", exploration: null, total: null })],
    ["closed 인데 탐색 수 모름", tr({ exploration_state: "closed", exploration: null, total: null })],
    ["unknown 인데 탐색 수 있음", tr({ exploration_state: "unknown", exploration: 3, total: 59 })],
    ["모르는 탐색 상태", { ...tr(), exploration_state: "paused" }],
    ["음수", tr({ model: -1, total: 5 })],
    ["모르는 칸", { ...tr(), extra: 1 }],
    ["칸 빠짐", { model: 50, login: 6, confirmation: 0, exploration: 0, exploration_state: "none", total: 56 }],
    ["문자 수", { ...tr(), model: "50" }],
    ["배열", [tr()]],
  ])("계약 밖은 거부한다: %s", (_, value) => {
    expect(() => parseE2Flow(withTR(value))).toThrow();
  });

  it("커서로 이어 읽으면 새 응답의 내역을 쓴다", () => {
    const first = parseE2Flow(withTR(tr()));
    const next = parseE2Flow({ ...withTR(tr({ model: 52, total: 58 })), last_index: 3 });
    expect(mergeE2Flow(first, next).target_requests?.total).toBe(58);
  });
});

describe("sentRequestsOf — '보낸 요청' 칸(사용자 11/25: 총계 + 마우스 올리면 내역)", () => {
  const flowWith = (t: E2TargetRequests) => parseE2Flow({ ...e2Flow(), target_requests: t });

  it("숫자는 대상에 나간 요청 총계, 내역은 0 이 아닌 갈래만(모델은 늘)", () => {
    expect(
      sentRequestsOf(
        flowWith({
          model: 50,
          login: 6,
          confirmation: 0,
          unclassified: 0,
          exploration: 0,
          exploration_state: "none",
          total: 56,
        }),
      ),
    ).toEqual({ value: "56건", detail: "모델 50 · 로그인 6" });
    expect(
      sentRequestsOf(
        flowWith({
          model: 50,
          login: 6,
          confirmation: 2,
          unclassified: 0,
          exploration: 30,
          exploration_state: "closed",
          total: 88,
        }),
      ),
    ).toEqual({ value: "88건", detail: "모델 50 · 로그인 6 · 확정 2 · 탐색 30" });
  });

  it("총계를 모르면 0 · 모델 수로 대신하지 않고 '—'와 까닭", () => {
    expect(
      sentRequestsOf(
        flowWith({
          model: 50,
          login: 6,
          confirmation: 0,
          unclassified: 0,
          exploration: null,
          exploration_state: "unknown",
          total: null,
        }),
      ),
    ).toEqual({ value: "—", detail: "탐색 요청 수를 확인하지 못해 총계를 모릅니다 · 모델 50 · 로그인 6 · 탐색 모름" });
    expect(
      sentRequestsOf(
        flowWith({
          model: 3,
          login: 0,
          confirmation: 0,
          unclassified: 1,
          exploration: 0,
          exploration_state: "none",
          total: null,
        }),
      )?.detail,
    ).toBe("분류 안 된 프로그램 요청이 있어 총계를 모릅니다 · 모델 3 · 분류 안 된 프로그램 요청 1");
  });

  it("검증 실패는 '검증 실패', 내역 칸이 없거나 응답이 없으면 null", () => {
    expect(sentRequestsOf(parseE2Flow({ ...e2Flow(), journal: "invalid", calls: [] }))?.value).toBe("검증 실패");
    expect(sentRequestsOf(parseE2Flow(e2Flow()))).toBeNull();
    expect(sentRequestsOf(null)).toBeNull();
  });
});

describe("저널 없음(journal none — 계약 workbench-live-data §4, 2026-11-27)", () => {
  const zero = {
    attempts: 0,
    allowed: 0,
    denied: 0,
    unsupported: 0,
    forward_attempted: 0,
    responses: 0,
    unknown: 0,
    transient: 0,
    withheld: 0,
    calls: 0,
  };
  const none = (over: Record<string, unknown> = {}) => ({
    format: "abc2lab.e2-flow-view/v1",
    journal: "none",
    last_index: 0,
    more: false,
    omitted_calls: 0,
    counts: zero,
    calls: [],
    target_requests: {
      model: 0,
      login: 0,
      confirmation: 0,
      unclassified: 0,
      exploration: 37,
      exploration_state: "closed",
      total: 37,
    },
    ...over,
  });

  it("받고, '보낸 요청'은 탐색 요청 수로 보인다(E②를 거치지 않고 끝난 회차)", () => {
    const flow = parseE2Flow(none());
    expect(flow.journal).toBe("none");
    expect(flow.calls).toEqual([]);
    expect(sentRequestsOf(flow)).toEqual({ value: "37건", detail: "모델 0 · 탐색 37" });
  });

  it.each([
    ["사건이 실려 옴", none({ calls: [{ call_ref: "call-1", intent_id: 9, closed: false, attempts: [e2Attempt()] }] })],
    ["누계가 0 이 아님", none({ counts: { ...zero, attempts: 1 } })],
    ["커서가 0 이 아님", none({ last_index: 3 })],
    ["더 있음", none({ more: true })],
    [
      "진단 갈래 수가 있음",
      none({
        target_requests: {
          model: 2,
          login: 0,
          confirmation: 0,
          unclassified: 0,
          exploration: 37,
          exploration_state: "closed",
          total: 39,
        },
      }),
    ],
    ["모델 쓰기가 0 이 아님", none({ model_writes: { responded_2xx: 1, unverified: 0 } })],
  ])("계약 밖은 거부한다: %s", (_, value) => {
    expect(() => parseE2Flow(value)).toThrow();
  });

  it("앞 응답이 verified 여도 none 이면 그대로 none 이다(지어낸 병합 없음)", () => {
    const merged = mergeE2Flow(e2Flow(), parseE2Flow(none()));
    expect(merged.journal).toBe("none");
    expect(merged.calls).toEqual([]);
  });
});
