import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import EntryScreen from "./EntryScreen";
import HistoryScreen from "./HistoryScreen";
import LiveScreen from "./LiveScreen";

vi.mock("../lib/engine", () => ({
  listTasks: vi.fn(),
  getTask: vi.fn(),
  listTaskFindings: vi.fn(),
  getCoverage: vi.fn(),
  getTokens: vi.fn(),
  getActivity: vi.fn(),
}));
const go = vi.fn();
const selectTask = vi.fn();
const task: engine.EngineTask = {
  id: "unknown-only",
  name: "미분류만 있는 점검",
  description: "",
  goal: "",
  status: "done",
  created_at: "2026-10-30T00:00:00Z",
  completed_at: "",
  goals_total: 0,
  goals_met: 0,
  in_flight: 0,
  // The list DTO omits unknown severities even when an actual finding exists.
  findings: { critical: 0, high: 0, medium: 0, low: 0 },
};
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(engine.listTasks).mockResolvedValue({ active: "", tasks: [task] });
  vi.mocked(engine.getTask).mockResolvedValue(task);
  vi.mocked(engine.listTaskFindings).mockResolvedValue([
    {
      id: "u",
      name: "미분류 발견",
      severity: "future-level",
      vulnclass: "future",
      status: "pending",
      summary: "",
      ts: "",
    },
  ]);
  vi.mocked(engine.getCoverage).mockResolvedValue(null);
  vi.mocked(engine.getTokens).mockResolvedValue({ input: 0, output: 0 });
  vi.mocked(engine.getActivity).mockResolvedValue([]);
});
afterEach(cleanup);
describe("partial severity summaries", () => {
  it.each(["entry", "history"] as const)("does not claim unknown-only work has no findings in %s", async (page) => {
    render(
      <NavContext.Provider value={{ screen: page, go }}>
        <SelectionContext.Provider value={{ taskId: task.id, selectTask, tasksVersion: 0, refreshTasks: vi.fn() }}>
          {page === "entry" ? <EntryScreen onStart={vi.fn()} /> : <HistoryScreen />}
        </SelectionContext.Provider>
      </NavContext.Provider>,
    );
    await screen.findByText(task.name);
    expect(screen.queryByText("발견 없음")).toBeNull();
    expect(screen.getByText("발견 목록 확인")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "보고서" }));
    expect(selectTask).toHaveBeenCalledWith(task.id);
    expect(go).toHaveBeenCalledWith("report");
  });
  it("labels the live list aggregate as classified counts while preserving actual unknown findings", async () => {
    const paused = { ...task, status: "paused" };
    vi.mocked(engine.listTasks).mockResolvedValue({ active: task.id, tasks: [paused] });
    vi.mocked(engine.getTask).mockResolvedValue(paused);
    render(
      <NavContext.Provider value={{ screen: "live", go }}>
        <SelectionContext.Provider value={{ taskId: task.id, selectTask, tasksVersion: 0, refreshTasks: vi.fn() }}>
          <LiveScreen target={{ url: "http://unrelated.test", host: "unrelated.test" }} />
        </SelectionContext.Provider>
      </NavContext.Provider>,
    );
    expect(await screen.findByText("미분류 발견")).toBeVisible();
    expect(screen.getByText(/위험도 분류 집계/)).toHaveTextContent("0건");
    expect(screen.queryByText(/전체 발견/)).toBeNull();
  });
});
