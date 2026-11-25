// 점검 진행 승인 요청 칸(사용자 11/25): 대기 요청은 먼저 온 하나만 펼쳐 보이고, 승인 · 거부하면 다음 요청이 같은 자리에 뜬다.
// 처리된 요청은 가운데에 남지 않고 머리 [처리된 요청 n]으로 넘어간다.

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

import type { RequestApproval } from "@/lib/diagnosis/request-approvals";

import { ApprovalQueue, type Approvals } from "../views/live/approvals";

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
