import "@testing-library/jest-dom/vitest";

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext, type ScreenId } from "../nav";
import LiveScreen from "./LiveScreen";

vi.mock("../lib/engine", () => ({
  getTask: vi.fn(),
  listTasks: vi.fn(),
  listTaskFindings: vi.fn(),
  getCoverage: vi.fn(),
  getTokens: vi.fn(),
  getActivity: vi.fn(),
  controlTask: vi.fn(),
  archiveTask: vi.fn(),
  getTaskArchive: vi.fn(),
}));

const go = vi.fn();
let approvalReads: number;

function page(screenId: ScreenId, taskId: string | null = "42") {
  return (
    <NavContext.Provider value={{ screen: screenId, go }}>
      <SelectionContext.Provider value={{ taskId, selectTask: vi.fn(), tasksVersion: 0, refreshTasks: vi.fn() }}>
        <LiveScreen target={{ url: "http://unrelated.test", host: "unrelated.test" }} />
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  go.mockReset();
  approvalReads = 0;
  localStorage.setItem("abc2lab.devToken", "valid");
  vi.mocked(engine.getTask).mockResolvedValue({
    id: "42",
    name: "점검 42",
    description: "",
    goal: "",
    status: "paused",
    created_at: "2026-11-08T00:00:00Z",
    completed_at: "",
    goals_total: 0,
    goals_met: 0,
    in_flight: 0,
    findings: { critical: 0, high: 0, medium: 0, low: 0 },
  });
  vi.mocked(engine.listTasks).mockResolvedValue({ active: "", tasks: [] });
  vi.mocked(engine.listTaskFindings).mockResolvedValue([]);
  vi.mocked(engine.getCoverage).mockResolvedValue(null);
  vi.mocked(engine.getTokens).mockResolvedValue({ input: 0, output: 0 });
  vi.mocked(engine.getActivity).mockResolvedValue([]);
  vi.stubGlobal("fetch", async (path: string) => {
    if (path.endsWith("/diagnosis-run")) return new Response(JSON.stringify({ error: "none" }), { status: 404 });
    approvalReads += 1;
    return new Response(
      JSON.stringify({
        approvals: [
          {
            id: "a1",
            task_id: 42,
            run_ref: "run-42",
            intent_id: 1,
            call_ref: "call",
            attempt_id: "a1",
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
          },
        ],
        run: {
          run_ref: "run-42",
          phase: "running",
          outcome: "",
          stop_requested: false,
          context_cancelled: false,
          drain_complete: false,
          closure_failed: false,
        },
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("진행 화면의 실행 상태", () => {
  it("선택한 task 의 실행 상태를 보여 주고 승인 화면으로 보낸다", async () => {
    render(page("live"));
    expect(await screen.findByText("run-42")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "승인 1건 보기" }));
    expect(go).toHaveBeenCalledWith("approval");
  });

  it("다른 화면을 보고 있으면 실행 상태를 읽지 않는다", async () => {
    vi.useFakeTimers();
    render(page("history"));
    await act(() => vi.advanceTimersByTimeAsync(3000));
    expect(approvalReads).toBe(0);
  });
});
