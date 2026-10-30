import "@testing-library/jest-dom/vitest";
import { useState } from "react";

import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext, type ScreenId } from "../nav";
import HistoryScreen from "./HistoryScreen";
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
const selectTask = vi.fn();
function task(id = "a", status = "paused"): engine.EngineTask {
  return {
    id,
    name: `점검 ${id}`,
    description: "",
    goal: "",
    status,
    created_at: "2026-10-30T00:00:00Z",
    completed_at: "",
    goals_total: 0,
    goals_met: 0,
    in_flight: 0,
    findings: { critical: 0, high: 0, medium: 0, low: 0 },
  };
}
function Harness({ id = "a", page = "live", history = false }: { id?: string; page?: ScreenId; history?: boolean }) {
  const [tasksVersion, setVersion] = useState(0);
  return (
    <NavContext.Provider value={{ screen: page, go }}>
      <SelectionContext.Provider
        value={{ taskId: id, selectTask, tasksVersion, refreshTasks: () => setVersion((v) => v + 1) }}
      >
        <LiveScreen target={{ url: "http://unrelated.test", host: "unrelated.test" }} />
        {history && <HistoryScreen />}
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(engine.getTask).mockImplementation(async (id) => task(id));
  vi.mocked(engine.listTasks).mockResolvedValue({ active: "", tasks: [] });
  vi.mocked(engine.listTaskFindings).mockResolvedValue([]);
  vi.mocked(engine.getCoverage).mockResolvedValue(null);
  vi.mocked(engine.getTokens).mockResolvedValue({ input: 0, output: 0 });
  vi.mocked(engine.getActivity).mockResolvedValue([]);
  vi.mocked(engine.archiveTask).mockResolvedValue({ id: "42", taskId: "a", state: "archive_queued" });
});
afterEach(cleanup);
async function openArchive() {
  await screen.findByText("점검 a");
  fireEvent.click(screen.getByRole("button", { name: /^(보관 요청|종료)$/ }));
  return screen.getByRole("dialog");
}

describe("task-bound controls", () => {
  it("does not offer mutation controls while the selected task is unresolved", () => {
    vi.mocked(engine.getTask).mockReturnValue(
      new Promise(() => {
        /* Deliberately pending. */
      }),
    );
    render(<Harness />);
    expect(screen.queryByRole("button", { name: "일시정지" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^(보관 요청|종료)$/ })).toBeNull();
  });
  it("requires pause before archive and permits pausing queued work", async () => {
    vi.mocked(engine.getTask).mockResolvedValue(task("a", "queued"));
    render(<Harness />);
    await screen.findByText("점검 a");
    expect(screen.getByRole("button", { name: "일시정지" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "보관 요청" })).toBeDisabled();
  });
  it("closes a task confirmation when selection changes", async () => {
    const view = render(<Harness />);
    await openArchive();
    view.rerender(<Harness id="b" />);
    await screen.findByText("점검 b");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(engine.archiveTask).not.toHaveBeenCalled();
  });
  it("keeps 202 acceptance distinct from archive completion and queries by archive id", async () => {
    vi.mocked(engine.getTaskArchive).mockResolvedValue({ id: "42", taskId: "a", state: "ready" });
    render(<Harness />);
    const dialog = await openArchive();
    fireEvent.click(within(dialog).getByRole("button", { name: "보관 요청" }));
    expect(await screen.findByText(/보관 #42/)).toBeVisible();
    expect(go).not.toHaveBeenCalledWith("history");
    fireEvent.click(screen.getByRole("button", { name: "보관 상태 새로고침" }));
    await waitFor(() => expect(engine.getTaskArchive).toHaveBeenCalledWith("42"));
    await waitFor(() => expect(go).toHaveBeenCalledWith("history"));
  });
  it("does not apply an old task's archive response to the new task", async () => {
    let finish!: (value: engine.EngineTaskArchive) => void;
    vi.mocked(engine.archiveTask).mockReturnValue(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    const view = render(<Harness />);
    const dialog = await openArchive();
    fireEvent.click(within(dialog).getByRole("button", { name: "보관 요청" }));
    view.rerender(<Harness id="b" />);
    await screen.findByText("점검 b");
    await act(async () => finish({ id: "42", taskId: "a", state: "archive_queued" }));
    expect(go).not.toHaveBeenCalled();
    expect(screen.queryByText(/보관 #42/)).toBeNull();
  });
  it("refreshes the already-mounted history after a control operation", async () => {
    let status = "running";
    vi.mocked(engine.getTask).mockImplementation(async (id) => task(id, status));
    vi.mocked(engine.listTasks).mockImplementation(async () => ({ active: "a", tasks: [task("a", status)] }));
    vi.mocked(engine.controlTask).mockImplementation(async () => {
      status = "paused";
    });
    const view = render(<Harness history />);
    await screen.findByRole("button", { name: "일시정지" });
    fireEvent.click(screen.getByRole("button", { name: "일시정지" }));
    await screen.findByRole("button", { name: "다시 시작" });
    view.rerender(<Harness history page="history" />);
    const history = view.container.querySelector('[data-screen="history"]') as HTMLElement;
    expect(await within(history).findByText(/일시중지/)).toBeVisible();
  });
});
