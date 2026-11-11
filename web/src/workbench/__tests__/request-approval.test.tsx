import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { apiFetch } from "@/lib/diagnosis/api";
import { running } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { resetUrl, useUrl } from "../live/urlDiagnosis";
import { ApprovalActions, useRequestApprovals } from "../views/live/approvals";

vi.mock("@/lib/diagnosis/api", async (original) => ({
  ...(await original<typeof import("@/lib/diagnosis/api")>()),
  apiFetch: vi.fn(),
}));
const row = {
  id: "approval-owned",
  task_id: 42,
  run_ref: "owned-run",
  intent_id: 1,
  call_ref: "call-owned",
  attempt_id: "attempt-owned",
  policy_revision: "revision-owned",
  account_ref: "anonymous",
  session_ref: "anonymous-session",
  request_sha256: "a".repeat(64),
  method: "POST",
  target_origin: "https://owned.test",
  path: "/api/Users",
  input_preview: {},
  body_bytes: 24,
  created_at: "2026-11-10T00:00:00Z",
  expires_at: "2099-01-01T00:00:00Z",
  state: "pending",
  consumed: false,
  execution_state: "not_sent",
  live: true,
  can_decide: true,
};
const list = {
  approvals: [row],
  run: {
    run_ref: "owned-run",
    phase: "running",
    outcome: "",
    stop_requested: false,
    context_cancelled: false,
    drain_complete: false,
    closure_failed: false,
  },
};
beforeEach(() => {
  vi.resetAllMocks();
  resetUrl();
  useUrl.setState({ job: { ...running, task_id: "42" }, verified: true });
  vi.mocked(apiFetch).mockImplementation(async (path, init) => {
    if (init?.method === "POST")
      return { ...row, state: JSON.parse(String(init.body)).decision === "allow" ? "allowed" : "denied" };
    if (String(path).endsWith(row.id)) return row;
    return list;
  });
});
afterEach(cleanup);
/** 화면들이 쓰는 그대로: 승인 훅 + 이 요청 승인 · 거부 버튼 + 목록 다시 조회. */
function Harness() {
  const a = useRequestApprovals("42", true);
  const pending = a.pending[0];
  return (
    <>
      {pending ? <ApprovalActions a={a} row={pending} /> : null}
      <button type="button" onClick={a.poll.reload}>
        승인 목록 다시 조회
      </button>
    </>
  );
}
async function mount() {
  render(<Harness />);
  const button = await screen.findByRole("button", { name: "이 요청 승인" });
  await waitFor(() => expect(button).toBeEnabled());
  return button;
}
it.each(["allow", "deny"])("rechecks exact approval fingerprint before %s and never retries POST", async (decision) => {
  await mount();
  fireEvent.click(screen.getByRole("button", { name: decision === "allow" ? "이 요청 승인" : "거부" }));
  await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.some(([, i]) => i?.method === "POST")).toBe(true));
  const calls = vi.mocked(apiFetch).mock.calls,
    index = calls.findIndex(([, i]) => i?.method === "POST");
  expect(String(calls[index - 1][0])).toContain(row.id);
  expect(JSON.parse(String(calls[index][1]?.body))).toEqual({ decision, request_sha256: row.request_sha256 });
  expect(calls[index][2]).toBe(false);
});
it.each(["run_ref", "attempt_id", "request_sha256"])("changed %s in fresh GET blocks decision", async (field) => {
  const button = await mount();
  vi.mocked(apiFetch).mockResolvedValueOnce({
    ...row,
    [field]: field === "request_sha256" ? "b".repeat(64) : "foreign",
  });
  fireEvent.click(button);
  await screen.findByText(/결정 결과 불명/);
  expect(vi.mocked(apiFetch).mock.calls.filter(([, i]) => i?.method === "POST")).toHaveLength(0);
});
it.each([
  "terminal",
  "task",
  "uncertain",
  "paused",
])("state change %s while reading approval prevents POST", async (which) => {
  const button = await mount();
  let finish: (v: unknown) => void = () => undefined;
  vi.mocked(apiFetch).mockReturnValueOnce(
    new Promise((r) => {
      finish = r;
    }),
  );
  fireEvent.click(button);
  act(() => {
    if (which === "terminal") useUrl.setState({ job: { ...running, task_id: "42", state: "completed" } });
    if (which === "task") useUrl.setState({ job: { ...running, task_id: "99" } });
    if (which === "uncertain") useUrl.setState({ uncertain: true });
    if (which === "paused")
      useUrl.setState({
        flow: { task_id: "42", run_ref: "owned-run", paused: true } as NonNullable<
          ReturnType<typeof useUrl.getState>["flow"]
        >,
        flowVerified: true,
      });
  });
  await act(async () => finish(row));
  expect(vi.mocked(apiFetch).mock.calls.filter(([, i]) => i?.method === "POST")).toHaveLength(0);
});
it("lost decision response keeps resend disabled even after pending GET", async () => {
  const button = await mount();
  vi.mocked(apiFetch).mockResolvedValueOnce(row).mockRejectedValueOnce(new TypeError("owned response loss"));
  fireEvent.click(button);
  await screen.findByText(/결정 결과 불명/);
  fireEvent.click(screen.getByRole("button", { name: "승인 목록 다시 조회" }));
  await waitFor(() => expect(vi.mocked(apiFetch).mock.calls.filter(([, i]) => i?.method !== "POST").length).toBe(3));
  expect(button).toBeDisabled();
  fireEvent.click(button);
  expect(vi.mocked(apiFetch).mock.calls.filter(([, i]) => i?.method === "POST")).toHaveLength(1);
});
