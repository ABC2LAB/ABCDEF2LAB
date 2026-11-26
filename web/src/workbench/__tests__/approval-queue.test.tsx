// 점검 진행 승인 요청 칸(사용자 11/25): 대기 요청은 먼저 온 하나만 펼쳐 보이고, 승인 · 거부하면 다음 요청이 같은 자리에 뜬다.
// 처리된 요청은 가운데에 남지 않고 머리 [처리된 요청 n]으로 넘어간다.

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

import type { RequestApproval } from "@/lib/diagnosis/request-approvals";

import {
  APPROVAL_CLOSED_REASON_TEXT,
  ApprovalFacts,
  ApprovalQueue,
  type Approvals,
  approvalTagText,
  precheck,
} from "../views/live/approvals";

afterEach(cleanup);

const row = (n: number, state: RequestApproval["state"] = "pending"): RequestApproval =>
  ({
    id: `ap-${n}`,
    task_id: 42,
    run_ref: "run-1",
    intent_id: 1,
    call_ref: `call-${n}`,
    attempt_id: `attempt-${n}`,
    policy_revision: "rev-1",
    account_ref: "a",
    session_ref: "s",
    request_sha256: "d".repeat(64),
    method: "POST",
    target_origin: "https://target.example",
    path: `/api/items/${n}`,
    input_preview: {},
    body_bytes: 19,
    created_at: `2026-11-11T01:00:0${n}Z`,
    expires_at: "2099-01-01T00:00:00Z",
    state,
    consumed: false,
    execution_state: "not_sent",
    live: state === "pending",
    can_decide: state === "pending",
  }) as RequestApproval;

const approvals = (rows: RequestApproval[]) =>
  ({ rows, busy: null, canDecide: () => true, decide: vi.fn() }) as unknown as Approvals;

const queue = (rows: RequestApproval[]) => (
  <ApprovalQueue a={approvals(rows)} planTitle={() => "계획"} doneShown={false} onShowDone={() => undefined} />
);

it("대기 요청은 먼저 온 하나만 펼쳐 보이고, 처리하면 다음 요청이 같은 자리에 뜬다", () => {
  const { rerender } = render(queue([row(3), row(1), row(2)]));
  const sec = () => screen.getByRole("region", { name: "승인 요청" });
  expect(within(sec()).getByText("대기 3")).toBeInTheDocument();
  expect(within(sec()).getAllByRole("listitem")).toHaveLength(1);
  expect(within(sec()).getByText("/api/items/1")).toBeInTheDocument();
  expect(within(sec()).getByRole("button", { name: /\/api\/items\/1/ })).toHaveAttribute("aria-expanded", "true");
  expect(within(sec()).getByRole("button", { name: "승인하고 보내기" })).toBeInTheDocument();
  // 첫 요청을 승인하면 다음(/api/items/2)이 같은 자리에, 처리된 것은 [처리된 요청 1]로
  rerender(queue([row(3), row(1, "allowed"), row(2)]));
  expect(within(sec()).getByText("대기 2")).toBeInTheDocument();
  expect(within(sec()).getAllByRole("listitem")).toHaveLength(1);
  expect(within(sec()).getByText("/api/items/2")).toBeInTheDocument();
  expect(within(sec()).queryByText("/api/items/1")).toBeNull();
  expect(within(sec()).getByRole("button", { name: "처리된 요청 1" })).toBeInTheDocument();
  // 다음도 처음부터 펼쳐져 있다
  expect(within(sec()).getByRole("button", { name: /\/api\/items\/2/ })).toHaveAttribute("aria-expanded", "true");
});

it("닫힌 까닭(closed_reason, #953): 줄 상태 글 · 승인 정보 · 결정 직전 안내에 서버 까닭을 붙인다", () => {
  const cancelled: RequestApproval = { ...row(1, "cancelled"), closed_reason: "model_stage_closed" };
  const expired: RequestApproval = { ...row(2, "expired"), closed_reason: "approval_expired" };
  expect(approvalTagText(cancelled)).toBe("취소 · 모델 단계 마감");
  expect(approvalTagText({ ...cancelled, closed_reason: "operator_stopped" })).toBe("취소 · 운영자 종료");
  // 만료는 '만료'가 곧 까닭이라 붙이지 않고, 이전 서버(칸 없음)는 상태만
  expect(approvalTagText(expired)).toBe("만료");
  expect(approvalTagText(row(3, "cancelled"))).toBe("취소");

  render(<ApprovalFacts row={cancelled} />);
  expect(screen.getByText("닫힌 까닭")).toBeInTheDocument();
  expect(screen.getByText(APPROVAL_CLOSED_REASON_TEXT.model_stage_closed)).toBeInTheDocument();
  expect(APPROVAL_CLOSED_REASON_TEXT.model_stage_closed).toContain("다른 요청이 취소한 것이 아닙니다");
  cleanup();
  render(<ApprovalFacts row={row(4, "cancelled")} />);
  expect(screen.queryByText("닫힌 까닭")).not.toBeInTheDocument();

  expect(precheck(row(1), cancelled)).toBe(
    `취소돼 닫힌 요청입니다. ${APPROVAL_CLOSED_REASON_TEXT.model_stage_closed} 결정을 보내지 않았습니다.`,
  );
  expect(precheck(row(3), row(3, "cancelled"))).toBe("취소돼 닫힌 요청입니다. 결정을 보내지 않았습니다.");
  expect(precheck(row(2), expired)).toBe("승인 기한이 지나 닫힌 요청입니다. 결정을 보내지 않았습니다.");
});
