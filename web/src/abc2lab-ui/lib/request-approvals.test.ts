import { beforeEach, describe, expect, it, vi } from "vitest";

import { apiFetch } from "./api";
import {
  decideRequestApproval,
  getRequestApproval,
  listRequestApprovals,
  readDiagnosisRun,
  readRequestApprovalState,
  stopRequestApprovalExecution,
} from "./request-approvals";

vi.mock("./api", () => ({ apiFetch: vi.fn() }));
beforeEach(() => vi.resetAllMocks());
describe("actual request approval API", () => {
  it("reads the saved final result without inventing completion or retrying a start", async () => {
    const record = {
      version: 1,
      task_id: 42,
      saved_at: "2026-11-08T00:00:00Z",
      run: { phase: "closed", outcome: "completed" },
    };
    vi.mocked(apiFetch).mockResolvedValue(record);
    expect(await readDiagnosisRun("a/b")).toBe(record);
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith("/api/tasks/a%2Fb/diagnosis-run", { cache: "no-store" });
  });

  it("reads server-owned run state even without any pending approval", async () => {
    const state = { approvals: [], run: { phase: "closed", outcome: "cancelled", drain_complete: true } };
    vi.mocked(apiFetch).mockResolvedValue(state);
    expect(await readRequestApprovalState("42")).toBe(state);
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith("/api/tasks/42/request-approvals", { cache: "no-store" });
  });

  it("stops only the selected task without an authentication retry", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ stopped: true });
    await stopRequestApprovalExecution("a/b");
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith(
      "/api/tasks/a%2Fb/request-approvals/stop",
      {
        method: "POST",
        body: "{}",
      },
      false,
    );
  });
  it("does not claim stop success for an invalid response", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ stopped: false });
    await expect(stopRequestApprovalExecution("42")).rejects.toThrow("전송 중지 완료를 확인하지 못했어요.");
  });
  it("uses task-scoped encoded URLs and the server response", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ approvals: [{ id: "attempt" }] });
    expect(await listRequestApprovals("a/b")).toEqual([{ id: "attempt" }]);
    expect(apiFetch).toHaveBeenCalledWith("/api/tasks/a%2Fb/request-approvals");
    await getRequestApproval("42", "a/b");
    expect(apiFetch).toHaveBeenLastCalledWith("/api/tasks/42/request-approvals/a%2Fb");
  });
  it("binds the exact displayed digest and never retries a decision after authentication failure", async () => {
    await decideRequestApproval("42", { id: "attempt", request_sha256: "digest" }, "allow");
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith(
      "/api/tasks/42/request-approvals/attempt/decision",
      {
        method: "POST",
        body: JSON.stringify({ decision: "allow", request_sha256: "digest" }),
      },
      false,
    );
  });
});
