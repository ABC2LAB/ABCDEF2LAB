import { describe, expect, it } from "vitest";

import { parseE2RequestDetail } from "@/workbench/live/e2Requests";

import { E2_LOGIN_REFUSALS, parseE2Flow } from "./e2flow";
import { e2Attempt, e2Flow } from "./e2flow-fixtures";

// 로그인 거절 상세 사유(workbench-live-data.md §4.2). E② 사유는 policy_denied 그대로이고 login_refusal이 더해진다.
const denied = {
  stage: "result" as const,
  method: "POST" as const,
  decision: "denied" as const,
  reason: "policy_denied" as const,
  forward_attempted: false,
  request_written: false,
  response_received: false,
  response_complete: false,
  status_code: 0,
};
const A1 = "a".repeat(32);
const detail = (over: Record<string, unknown> = {}) => ({
  format: "abc2lab.e2-flow-request/v1",
  call_ref: "call-3",
  attempt_id: A1,
  kind: "request",
  index: 3,
  at: "2026-11-25T01:00:00Z",
  method: "POST",
  path_detail_state: "unavailable",
  decision: "denied",
  reason: "policy_denied",
  forward_attempted: false,
  request_written: false,
  response_received: false,
  response_complete: false,
  response_truncated: false,
  request_headers: { state: "not_stored" },
  request_body: { state: "not_stored" },
  response_headers: { state: "not_stored" },
  response_body: { state: "not_stored" },
  ...over,
});

describe("로그인 거절 상세 사유", () => {
  it.each(E2_LOGIN_REFUSALS)("흐름 시도와 요청 상세가 같은 사유를 읽는다: %s", (code) => {
    const flow = e2Flow();
    flow.calls[0].attempts = [{ ...e2Attempt(denied), login_refusal: code }];
    const a = parseE2Flow(flow).calls[0].attempts[0];
    expect(a.reason).toBe("policy_denied");
    expect(a.login_refusal).toBe(code);
    expect(parseE2RequestDetail(detail({ login_refusal: code }), "call-3", A1).login_refusal).toBe(code);
  });

  it("칸이 없으면(이전 서버) 사유가 없고, 목록 밖 값은 버린다", () => {
    const flow = e2Flow();
    flow.calls[0].attempts = [e2Attempt(denied)];
    expect(parseE2Flow(flow).calls[0].attempts[0]).not.toHaveProperty("login_refusal");
    const unknown = e2Flow();
    unknown.calls[0].attempts = [{ ...e2Attempt(denied), login_refusal: "new_refusal" } as never];
    expect(parseE2Flow(unknown).calls[0].attempts[0]).not.toHaveProperty("login_refusal");
    expect(parseE2RequestDetail(detail({ login_refusal: "new_refusal" }), "call-3", A1)).not.toHaveProperty(
      "login_refusal",
    );
  });

  it.each([
    ["허용된 시도", { decision: "allowed", reason: "complete", forward_attempted: true }],
    ["GET 요청", { method: "GET" }],
    ["다른 거부 사유", { reason: "budget_exhausted" }],
    ["보낸 시도", { forward_attempted: true }],
    ["문자열 아님", { login_refusal: 3 }],
  ])("보내지 않은 policy_denied 결과가 아니면 받지 않는다: %s", (_name, change) => {
    const flow = e2Flow();
    flow.calls[0].attempts = [{ ...e2Attempt(denied), login_refusal: "login_attempts_exhausted", ...change } as never];
    expect(() => parseE2Flow(flow)).toThrow();
    expect(() =>
      parseE2RequestDetail(detail({ login_refusal: "login_attempts_exhausted", ...change }), "call-3", A1),
    ).toThrow();
  });
});
