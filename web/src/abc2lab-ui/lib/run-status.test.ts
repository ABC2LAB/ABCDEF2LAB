import { describe, expect, it } from "vitest";

import type { RequestApprovalRunState } from "./request-approvals";
import { describeRun } from "./run-status";

function run(overrides: Partial<RequestApprovalRunState>): RequestApprovalRunState {
  return {
    run_ref: "run-1",
    phase: "running",
    outcome: "",
    stop_requested: false,
    context_cancelled: false,
    drain_complete: false,
    closure_failed: false,
    ...overrides,
  };
}

describe("실행 단계 표시 (first-diagnosis-launch.md 의 run 상태표)", () => {
  it.each([
    ["prepared", "실행 준비됨"],
    ["running", "실행 중"],
    ["stopping", "중지·마감 중"],
  ] as const)("%s 는 %s 로 표시한다", (phase, label) => {
    expect(describeRun(run({ phase })).label).toBe(label);
  });

  it("정상 마감을 목표 달성이나 취약점 없음으로 말하지 않는다", () => {
    const summary = describeRun(run({ phase: "closed", outcome: "completed", drain_complete: true }));
    expect(summary.label).toBe("실행 마감");
    expect(summary.note).toBe("이번 실행과 감사가 마감됐어요. 목표 달성이나 취약점 없음을 뜻하지 않아요.");
  });

  it("drain 이 확인되지 않은 closed 는 마감으로 표시하지 않는다", () => {
    expect(describeRun(run({ phase: "closed", outcome: "completed", drain_complete: false })).label).toBe(
      "마감 미확인",
    );
  });

  it("사용자 중지 마감은 롤백이 아니라고 알린다", () => {
    const summary = describeRun(run({ phase: "closed", outcome: "cancelled", drain_complete: true }));
    expect(summary.label).toBe("중지 후 마감");
    expect(summary.note).toBe("이미 보낸 요청이나 대상 변경은 되돌리지 않아요.");
  });

  it("실패한 outcome 의 마감은 실패로 표시한다", () => {
    expect(describeRun(run({ phase: "closed", outcome: "failed", drain_complete: true })).label).toBe("실패로 마감");
  });

  it("outcome 이 없는 마감을 완료로 추정하지 않는다", () => {
    expect(describeRun(run({ phase: "closed", outcome: "", drain_complete: true })).label).toBe("마감 결과 미확인");
  });

  it("자원 마감 실패만 별도로 경고한다", () => {
    expect(describeRun(run({ phase: "failed", closure_failed: true })).note).toBe(
      "자원 마감까지 실패했어요. 종료됐다고 보지 마세요.",
    );
    expect(describeRun(run({ phase: "failed", closure_failed: false })).note).toBeUndefined();
  });

  it("모르는 단계는 그대로 드러낸다", () => {
    const phase = "paused" as unknown as RequestApprovalRunState["phase"];
    expect(describeRun(run({ phase })).label).toBe("알 수 없는 상태 (paused)");
  });
});
