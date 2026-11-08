import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../lib/api";
import * as approvals from "../lib/request-approvals";
import { RequestApprovalPanel } from "./RequestApprovalPanel";

vi.mock("../lib/request-approvals", () => ({
  listRequestApprovals: vi.fn(),
  getRequestApproval: vi.fn(),
  decideRequestApproval: vi.fn(),
  stopRequestApprovalExecution: vi.fn(),
}));
const record = (extra: Partial<approvals.RequestApproval> = {}): approvals.RequestApproval => ({
  id: "attempt1",
  task_id: 42,
  run_ref: "run1",
  intent_id: 1,
  call_ref: "call1",
  attempt_id: "attempt1",
  policy_revision: "policy1",
  account_ref: "account-a",
  session_ref: "protected-session-ref",
  request_sha256: "fixed-digest",
  method: "POST",
  target_origin: "https://local.test",
  path: "/item",
  input_preview: { value: "<img src=x onerror=alert(1)>" },
  body_bytes: 40,
  created_at: new Date().toISOString(),
  expires_at: new Date(Date.now() + 60000).toISOString(),
  state: "pending",
  consumed: false,
  execution_state: "not_sent",
  live: true,
  can_decide: true,
  ...extra,
});
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(approvals.listRequestApprovals).mockResolvedValue([record()]);
  vi.mocked(approvals.getRequestApproval).mockResolvedValue(record());
});
afterEach(cleanup);
async function open() {
  fireEvent.click(await screen.findByRole("button", { name: "요청 상세 attempt1" }));
  await screen.findByRole("button", { name: "이 HTTP 요청만 허용" });
}
describe("actual request approval panel", () => {
  it("stops the current transmission once and refreshes cancelled pending requests", async () => {
    let resolve!: () => void;
    vi.mocked(approvals.stopRequestApprovalExecution).mockReturnValue(
      new Promise<void>((done) => {
        resolve = done;
      }),
    );
    render(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    await open();
    const stop = screen.getByRole("button", { name: "이 점검의 현재 전송 중지" });
    fireEvent.click(stop);
    fireEvent.click(stop);
    expect(approvals.stopRequestApprovalExecution).toHaveBeenCalledExactlyOnceWith("42");
    expect(screen.getByRole("button", { name: "이 HTTP 요청만 허용" })).toBeDisabled();
    const cancelled = record({ state: "cancelled", can_decide: false, live: false });
    vi.mocked(approvals.listRequestApprovals).mockResolvedValue([cancelled]);
    vi.mocked(approvals.getRequestApproval).mockResolvedValue(cancelled);
    await act(async () => {
      resolve();
    });
    expect(
      await screen.findByText(
        "현재 전송 경계를 중지했어요. 전체 점검의 종료나 이미 전송한 요청의 취소를 뜻하지 않아요.",
      ),
    ).toBeVisible();
    await waitFor(() => expect(screen.getAllByText("취소됨")).toHaveLength(2));
    expect(screen.getByText("미전송")).toBeVisible();
    expect(stop).toBeDisabled();
    expect(approvals.decideRequestApproval).not.toHaveBeenCalled();
  });
  it.each([
    new ApiError(403, "forbidden"),
    new Error("connection lost"),
  ])("never claims a failed stop succeeded or retries it: %s", async (error) => {
    vi.mocked(approvals.stopRequestApprovalExecution).mockRejectedValue(error);
    render(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    await open();
    fireEvent.click(screen.getByRole("button", { name: "이 점검의 현재 전송 중지" }));
    await screen.findByText(
      error instanceof ApiError ? /전송 중지 요청이 거부됐어요/ : /전송 중지 결과를 확인하지 못했어요/,
    );
    expect(screen.queryByText(/^현재 전송 경계를 중지했어요/)).toBeNull();
    const stop = screen.getByRole("button", { name: "이 점검의 현재 전송 중지" });
    expect(stop).toBeDisabled();
    fireEvent.click(stop);
    expect(approvals.stopRequestApprovalExecution).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(approvals.getRequestApproval).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("button", { name: "이 HTTP 요청만 허용" })).toBeDisabled();
  });
  it("preserves an uncertain stop after hiding the screen during the request", async () => {
    let reject!: (error: Error) => void;
    vi.mocked(approvals.stopRequestApprovalExecution).mockReturnValue(
      new Promise((_, fail) => {
        reject = fail;
      }),
    );
    const view = render(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    await open();
    fireEvent.click(screen.getByRole("button", { name: "이 점검의 현재 전송 중지" }));
    view.rerender(<RequestApprovalPanel taskId="42" active={false} refreshKey={0} />);
    await act(async () => {
      reject(new Error("connection lost"));
    });
    view.rerender(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    expect(await screen.findByText(/전송 중지 결과를 확인하지 못했어요/)).toBeVisible();
    await screen.findByRole("button", { name: "이 HTTP 요청만 허용" });
    expect(screen.getByRole("button", { name: "이 점검의 현재 전송 중지" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "이 HTTP 요청만 허용" })).toBeDisabled();
    expect(approvals.stopRequestApprovalExecution).toHaveBeenCalledTimes(1);
  });

  it("does not offer a stop for a different task or an inactive run", async () => {
    vi.mocked(approvals.listRequestApprovals).mockResolvedValue([
      record({ task_id: 99 }),
      record({ id: "old", live: false }),
    ]);
    render(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    await screen.findByRole("button", { name: "요청 상세 old" });
    const stop = screen.getByRole("button", { name: "이 점검의 현재 전송 중지" });
    expect(stop).toBeDisabled();
    fireEvent.click(stop);
    expect(approvals.stopRequestApprovalExecution).not.toHaveBeenCalled();
  });
  it("does not apply the old task stop response to a newly selected task", async () => {
    let resolve!: () => void;
    vi.mocked(approvals.stopRequestApprovalExecution).mockReturnValue(
      new Promise<void>((done) => {
        resolve = done;
      }),
    );
    const view = render(<RequestApprovalPanel key="42" taskId="42" active refreshKey={0} />);
    await open();
    fireEvent.click(screen.getByRole("button", { name: "이 점검의 현재 전송 중지" }));
    vi.mocked(approvals.listRequestApprovals).mockResolvedValue([record({ task_id: 99, id: "new-attempt" })]);
    view.rerender(<RequestApprovalPanel key="99" taskId="99" active refreshKey={0} />);
    await act(async () => {
      resolve();
    });
    await screen.findByRole("button", { name: "요청 상세 new-attempt" });
    expect(screen.queryByText(/^현재 전송 경계를 중지했어요/)).toBeNull();
    expect(screen.getByRole("button", { name: "이 점검의 현재 전송 중지" })).toBeEnabled();
    expect(approvals.stopRequestApprovalExecution).toHaveBeenCalledExactlyOnceWith("42");
  });
  it("renders target and protected refs as text, then sends only one digest-bound decision", async () => {
    let resolve!: (r: approvals.RequestApproval) => void;
    vi.mocked(approvals.decideRequestApproval).mockReturnValue(
      new Promise((r) => {
        resolve = r;
      }),
    );
    const { container } = render(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    await open();
    expect(screen.getByText("https://local.test/item")).toBeVisible();
    expect(screen.getByText(/protected-session-ref/)).toBeVisible();
    expect(container.querySelector("img")).toBeNull();
    const allow = screen.getByRole("button", { name: "이 HTTP 요청만 허용" });
    fireEvent.click(allow);
    fireEvent.click(allow);
    expect(approvals.decideRequestApproval).toHaveBeenCalledTimes(1);
    expect(approvals.decideRequestApproval).toHaveBeenCalledWith(
      "42",
      expect.objectContaining({ id: "attempt1", request_sha256: "fixed-digest" }),
      "allow",
    );
    const allowed = record({ state: "allowed", can_decide: false, execution_state: "awaiting_dispatch" });
    vi.mocked(approvals.getRequestApproval).mockResolvedValue(allowed);
    resolve(allowed);
    expect(await screen.findByText("허용 결정을 저장했어요. 실제 전송 결과는 아래에서 따로 확인해요.")).toBeVisible();
    expect(await screen.findByText("현재 정책·권한·예산 재검사 대기")).toBeVisible();
    expect(screen.getByRole("button", { name: "이 HTTP 요청만 허용" })).toBeDisabled();
  });
  it.each([
    { state: "denied" as const },
    { state: "expired" as const },
    { state: "cancelled" as const },
    { execution_state: "unknown" as const },
    { consumed: true },
    { live: false },
    { can_decide: false },
    { task_id: 99 },
    { expires_at: new Date(0).toISOString() },
    { expires_at: "invalid" },
  ])("does not decide inactive or stale detail %j", async (extra) => {
    vi.mocked(approvals.getRequestApproval).mockResolvedValue(record(extra));
    render(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    fireEvent.click(await screen.findByRole("button", { name: "요청 상세 attempt1" }));
    await waitFor(() => expect(approvals.getRequestApproval).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByText("최신 요청 내용을 불러오는 중…")).toBeNull());
    const allow = screen.queryByRole("button", { name: "이 HTTP 요청만 허용" });
    if (allow) {
      expect(allow).toBeDisabled();
      fireEvent.click(allow);
    }
    expect(approvals.decideRequestApproval).not.toHaveBeenCalled();
  });
  it("preserves unknown dispatch state without claiming success or retrying", async () => {
    vi.mocked(approvals.getRequestApproval).mockResolvedValue(
      record({ state: "allowed", consumed: true, can_decide: false, execution_state: "unknown" }),
    );
    render(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    await open();
    expect(screen.getByText("실행 결과 불명 · 기존 승인으로 자동 재전송하지 않음")).toBeVisible();
    expect(screen.getByRole("button", { name: "이 HTTP 요청만 허용" })).toBeDisabled();
  });
  it("does not retry a network-uncertain decision even when refreshed detail still says pending", async () => {
    vi.mocked(approvals.decideRequestApproval).mockRejectedValue(new Error("connection lost"));
    render(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    await open();
    fireEvent.click(screen.getByRole("button", { name: "이 HTTP 요청 거절" }));
    expect(await screen.findByText(/결정 저장 결과를 확인하지 못했어요/)).toBeVisible();
    await waitFor(() => expect(screen.getByRole("button", { name: "이 HTTP 요청 거절" })).toBeDisabled());
    expect(approvals.decideRequestApproval).toHaveBeenCalledTimes(1);
  });
  it.each(["close", "hide"])("preserves uncertain decisions after %s and reentry", async (navigation) => {
    let reject!: (error: Error) => void;
    vi.mocked(approvals.decideRequestApproval).mockReturnValue(
      new Promise((_, fail) => {
        reject = fail;
      }),
    );
    const view = render(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
    await open();
    fireEvent.click(screen.getByRole("button", { name: "이 HTTP 요청만 허용" }));
    if (navigation === "close") {
      fireEvent.click(screen.getByRole("button", { name: "요청 상세 닫기" }));
    } else {
      view.rerender(<RequestApprovalPanel taskId="42" active={false} refreshKey={0} />);
    }
    await act(async () => {
      reject(new Error("connection lost after navigation"));
    });
    expect(screen.queryByText(/결정 저장 결과를 확인하지 못했어요/)).toBeNull();
    if (navigation === "close") {
      await open();
    } else {
      view.rerender(<RequestApprovalPanel taskId="42" active refreshKey={0} />);
      await screen.findByRole("button", { name: "이 HTTP 요청만 허용" });
    }
    const allow = screen.getByRole("button", { name: "이 HTTP 요청만 허용" });
    expect(allow).toBeDisabled();
    fireEvent.click(allow);
    expect(approvals.decideRequestApproval).toHaveBeenCalledTimes(1);
  });
  it("discards stale detail when a new task mounts and makes no calls while hidden", async () => {
    let resolve!: (r: approvals.RequestApproval) => void;
    vi.mocked(approvals.getRequestApproval).mockReturnValue(
      new Promise((r) => {
        resolve = r;
      }),
    );
    const view = render(<RequestApprovalPanel key="42" taskId="42" active refreshKey={0} />);
    fireEvent.click(await screen.findByRole("button", { name: "요청 상세 attempt1" }));
    view.rerender(<RequestApprovalPanel key="99" taskId="99" active={false} refreshKey={0} />);
    resolve(record());
    await waitFor(() => expect(screen.queryByRole("button", { name: "이 HTTP 요청만 허용" })).toBeNull());
    expect(approvals.listRequestApprovals).toHaveBeenCalledTimes(1);
    expect(approvals.decideRequestApproval).not.toHaveBeenCalled();
  });
});
