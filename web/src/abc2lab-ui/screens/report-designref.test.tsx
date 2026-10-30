import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import ReportScreen from "./ReportScreen";

vi.mock("../lib/engine", () => ({
  getTask: vi.fn(),
  listTaskFindings: vi.fn(),
  getCoverage: vi.fn(),
}));

const task = {
  id: "task-1",
  name: "시험 점검",
  description: "",
  goal: "",
  status: "done" as const,
  created_at: "2026-10-30T00:00:00Z",
  completed_at: "2026-10-30T00:01:00Z",
  goals_total: 0,
  goals_met: 0,
  in_flight: 0,
  findings: { critical: 0, high: 0, medium: 0, low: 1 },
};

function show(taskId = "task-1") {
  return render(
    <NavContext.Provider value={{ screen: "report", go: vi.fn() }}>
      <SelectionContext.Provider value={{ taskId, tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn() }}>
        <ReportScreen />
      </SelectionContext.Provider>
    </NavContext.Provider>,
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(engine.getTask).mockResolvedValue(task);
  vi.mocked(engine.getCoverage).mockResolvedValue({
    enabled: true,
    scopeRows: 1,
    denominator: 2,
    tested: 1,
    pct: 0.5,
    byType: [],
  });
  vi.mocked(engine.listTaskFindings).mockResolvedValue([
    {
      id: "finding-low",
      vulnclass: "xss",
      name: "낮은 발견",
      severity: "low",
      status: "pending",
      summary: "",
      ts: "2026-10-30T00:00:00Z",
    },
    {
      id: "finding-unknown",
      vulnclass: "future",
      name: "미분류 발견",
      severity: "future-level",
      status: "pending",
      summary: "",
      ts: "2026-10-30T00:00:00Z",
    },
  ]);
});
afterEach(cleanup);

describe("ReportScreen design-ref regressions", () => {
  it("keeps unknown severity in graph and table totals", async () => {
    show();
    await screen.findByText("발견 한눈에 보기");
    fireEvent.click(screen.getByRole("button", { name: "표" }));

    const row = screen.getByText("미분류", { selector: "td" }).closest("tr");
    expect(row).not.toBeNull();
    expect(row).toHaveTextContent("1건");
    expect(row).toHaveTextContent("50%");
    expect(screen.getByText(/같은 발견 2건/)).toBeVisible();
  });

  it("shows coverage failure and disabled states in the summary", async () => {
    vi.mocked(engine.getCoverage).mockRejectedValueOnce(new Error("coverage unavailable"));
    show();
    expect(await screen.findByText("커버리지를 불러오지 못했어요.")).toBeVisible();

    cleanup();
    vi.mocked(engine.getCoverage).mockResolvedValueOnce({
      enabled: false,
      scopeRows: 1,
      denominator: 2,
      tested: 1,
      pct: 0.5,
      byType: [],
    });
    show();
    expect(await screen.findByText("커버리지 집계가 꺼져 있어요.")).toBeVisible();

    cleanup();
    vi.mocked(engine.getCoverage).mockResolvedValueOnce(null);
    show();
    expect(await screen.findByText("커버리지 집계 전이거나 확인할 수 없어요.")).toBeVisible();
  });

  it("does not report a findings failure while the list is still loading", () => {
    vi.mocked(engine.listTaskFindings).mockImplementationOnce(
      () =>
        new Promise<engine.EngineFinding[]>((_resolve) => {
          // Keep the request pending to exercise the loading state.
        }),
    );
    show();
    expect(screen.getAllByText("발견 목록을 불러오는 중이에요.").length).toBeGreaterThan(0);
    expect(screen.queryByText("발견 목록 조회에 실패해 그래프를 확인할 수 없어요.")).toBeNull();
  });

  it("resets list accordion and graph selection when the selected task changes", async () => {
    let selectedTaskId = "task-1";
    const view = render(
      <NavContext.Provider value={{ screen: "report", go: vi.fn() }}>
        <SelectionContext.Provider
          value={{ taskId: selectedTaskId, tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn() }}
        >
          <ReportScreen />
        </SelectionContext.Provider>
      </NavContext.Provider>,
    );
    const { rerender } = view;
    fireEvent.click(screen.getByRole("button", { name: /발견 목록/ }));
    await screen.findByText("미분류 발견");
    fireEvent.click(screen.getByText("미분류 발견"));
    expect(
      screen.getByText("구조화 증거(요청/응답 · canary)와 확정/의심/판단 불가 판정은 E③ 오라클 연동 후 채워져요."),
    ).toBeVisible();

    selectedTaskId = "task-2";
    rerender(
      <NavContext.Provider value={{ screen: "report", go: vi.fn() }}>
        <SelectionContext.Provider
          value={{ taskId: selectedTaskId, tasksVersion: 1, selectTask: vi.fn(), refreshTasks: vi.fn() }}
        >
          <ReportScreen />
        </SelectionContext.Provider>
      </NavContext.Provider>,
    );
    await waitFor(() =>
      expect(
        screen.queryByText("구조화 증거(요청/응답 · canary)와 확정/의심/판단 불가 판정은 E③ 오라클 연동 후 채워져요."),
      ).toBeNull(),
    );
    expect(screen.getByText("한눈에")).toBeVisible();
  });
});
