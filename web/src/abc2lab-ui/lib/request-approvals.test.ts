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
const run = {
  run_ref: "run1",
  phase: "running",
  outcome: "",
  stop_requested: false,
  context_cancelled: false,
  drain_complete: false,
  closure_failed: false,
};
const approval = {
  id: "a/b",
  task_id: 42,
  run_ref: "run1",
  intent_id: 1,
  call_ref: "call",
  attempt_id: "attempt",
  policy_revision: "policy",
  account_ref: "account",
  session_ref: "session",
  request_sha256: "a".repeat(64),
  method: "GET",
  target_origin: "http://127.0.0.1",
  path: "/",
  input_preview: {},
  body_bytes: 0,
  created_at: "2026-11-08T00:00:00Z",
  expires_at: "2026-11-10T00:00:00Z",
  state: "pending",
  consumed: false,
  execution_state: "not_sent",
  live: true,
  can_decide: true,
};
describe("actual request approval API", () => {
  it("reads a validated same-task saved result", async () => {
    const record = {
      version: 1,
      task_id: 42,
      saved_at: "2026-11-08T00:00:00Z",
      run: { ...run, phase: "closed", outcome: "completed", drain_complete: true },
    };
    vi.mocked(apiFetch).mockResolvedValue(record);
    expect(await readDiagnosisRun("42")).toBe(record);
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith("/api/tasks/42/diagnosis-run", { cache: "no-store" });
    await expect(readDiagnosisRun("99")).rejects.toThrow("식별자");
  });
  it("reads run state with zero approvals", async () => {
    const state = { approvals: [], run };
    vi.mocked(apiFetch).mockResolvedValue(state);
    expect(await readRequestApprovalState("42")).toEqual(state);
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith("/api/tasks/42/request-approvals", { cache: "no-store" });
  });
  it("stops only selected task without authentication retry", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ stopped: true });
    await stopRequestApprovalExecution("a/b");
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith(
      "/api/tasks/a%2Fb/request-approvals/stop",
      { method: "POST", body: "{}" },
      false,
    );
  });
  it("does not claim stop success for malformed response", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ stopped: false });
    await expect(stopRequestApprovalExecution("42")).rejects.toThrow("전송 중지 완료를 확인하지 못했어요.");
  });
  it("encodes detail IDs and validates list/run binding", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ approvals: [approval], run });
    expect(await listRequestApprovals("42")).toEqual([approval]);
    vi.mocked(apiFetch).mockResolvedValue(approval);
    expect(await getRequestApproval("42", "a/b")).toEqual(approval);
    expect(apiFetch).toHaveBeenLastCalledWith("/api/tasks/42/request-approvals/a%2Fb", { cache: "no-store" });
  });
  it("binds the exact digest without decision retries", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ ...approval, state: "allowed" });
    await decideRequestApproval("42", approval, "allow");
    expect(apiFetch).toHaveBeenCalledExactlyOnceWith(
      "/api/tasks/42/request-approvals/a%2Fb/decision",
      {
        method: "POST",
        body: JSON.stringify({ decision: "allow", request_sha256: approval.request_sha256 }),
      },
      false,
    );
  });
  it.each([
    { ...approval, task_id: 99 },
    { ...approval, request_sha256: "missing" },
    { ...approval, state: "success" },
    { ...approval, can_decide: "true" },
    { ...approval, input_preview: { nested: {} } },
  ])("rejects malformed or cross-task detail %j", async (value) => {
    vi.mocked(apiFetch).mockResolvedValue(value);
    await expect(getRequestApproval("42", "a/b")).rejects.toThrow("확인할 수 없어요");
  });
  it("rejects a list row from another run", async () => {
    vi.mocked(apiFetch).mockResolvedValue({ run, approvals: [{ ...approval, run_ref: "other" }] });
    await expect(readRequestApprovalState("42")).rejects.toThrow("확인할 수 없어요");
  });
  it.each([
    { version: 1, task_id: 42, run: null },
    { version: 1, task_id: 42, run },
    {
      version: 1,
      task_id: 42,
      run: { ...run, phase: "closed" },
      saved_at: "invalid",
    },
  ])("rejects unverified final record %j", async (value) => {
    vi.mocked(apiFetch).mockResolvedValue(value);
    await expect(readDiagnosisRun("42")).rejects.toThrow("확인할 수 없어요");
  });
});
