import "@testing-library/jest-dom/vitest";
import type { ReactNode } from "react";

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext, type ScreenId } from "../nav";
import HistoryScreen from "./HistoryScreen";
import LiveScreen from "./LiveScreen";
import ReportScreen from "./ReportScreen";
import SettingsScreen from "./SettingsScreen";

vi.mock("../lib/engine", () => ({
  getTask: vi.fn(),
  listTasks: vi.fn(),
  listTaskFindings: vi.fn(),
  getCoverage: vi.fn(),
  getTokens: vi.fn(),
  getActivity: vi.fn(),
  getInterceptRules: vi.fn(),
  getLLM: vi.fn(),
}));

function show(id: ScreenId, child: ReactNode) {
  return render(
    <NavContext.Provider value={{ screen: id, go: vi.fn() }}>
      <SelectionContext.Provider value={{ taskId: "task-1", selectTask: vi.fn() }}>{child}</SelectionContext.Provider>
    </NavContext.Provider>,
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(engine.getTask).mockResolvedValue({
    id: "task-1",
    name: "시험 점검",
    description: "",
    goal: "",
    status: "running",
    created_at: "2026-10-30T00:00:00Z",
    completed_at: "",
    goals_total: 0,
    goals_met: 0,
    in_flight: 0,
    findings: { critical: 0, high: 0, medium: 0, low: 0 },
  });
  vi.mocked(engine.listTaskFindings).mockResolvedValue([]);
  vi.mocked(engine.getCoverage).mockResolvedValue(null);
  vi.mocked(engine.getTokens).mockResolvedValue({ input: 0, output: 0 });
  vi.mocked(engine.getActivity).mockResolvedValue([]);
  vi.mocked(engine.getInterceptRules).mockResolvedValue([]);
  vi.mocked(engine.getLLM).mockResolvedValue({
    configured: true,
    provider: "local",
    model: "test-model",
    base_url: "http://localhost:11434/v1",
    key_set: false,
    context_window_k: 0,
  });
});
afterEach(cleanup);

describe("read screen contracts", () => {
  it("shows an ask rule as requiring approval", async () => {
    vi.mocked(engine.getInterceptRules).mockResolvedValue([
      {
        id: 1,
        name: "수동 검토 규칙",
        enabled: true,
        priority: 1,
        match_target: "tool_input",
        match_type: "regex",
        pattern: ".*",
        action: "ask",
      },
    ]);
    show("settings", <SettingsScreen />);
    const name = await screen.findByText("수동 검토 규칙");
    const row = name.closest(".frow");
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText("승인 필요")).toBeInTheDocument();
  });

  it.each([
    "https://localhost.attacker.example/v1",
    "https://remote.example/v1?name=ollama",
  ])("does not label %s internal", async (base_url) => {
    vi.mocked(engine.getLLM).mockResolvedValue({
      configured: true,
      provider: "test",
      model: "test-model",
      base_url,
      key_set: false,
      context_window_k: 0,
    });
    show("settings", <SettingsScreen />);
    expect(await screen.findByText("외부 주소")).toBeInTheDocument();
  });

  it("does not report no findings after the report lookup fails", async () => {
    vi.mocked(engine.listTaskFindings).mockRejectedValue(new Error("synthetic lookup failure"));
    show("report", <ReportScreen />);
    fireEvent.click(screen.getByRole("button", { name: /발견 목록/ }));
    expect(await screen.findByText("발견 목록을 불러오지 못했어요.")).toBeVisible();
    expect(screen.queryByText("이 점검에서 발견된 항목이 없어요.")).toBeNull();
  });

  it("does not report no findings after the dashboard lookup fails", async () => {
    vi.mocked(engine.listTaskFindings).mockRejectedValue(new Error("synthetic lookup failure"));
    show("live", <LiveScreen target={{ url: "http://example.test", host: "example.test" }} />);
    expect(await screen.findByText("발견 목록을 불러오지 못했어요.")).toBeVisible();
    expect(screen.queryByText("아직 발견이 없어요.")).toBeNull();
  });

  it("does not turn explored assets into a clean vulnerability verdict", async () => {
    vi.mocked(engine.getCoverage).mockResolvedValue({
      enabled: true,
      scopeRows: 1,
      denominator: 3,
      tested: 2,
      pct: 2 / 3,
      byType: [],
    });
    show("live", <LiveScreen target={{ url: "http://example.test", host: "example.test" }} />);
    await waitFor(() => expect(engine.getCoverage).toHaveBeenCalledWith("task-1"));
    expect(await screen.findByText("탐색됨 · 판정 미연결")).toBeVisible();
    expect(screen.queryByText("검사 · 이상 없음")).toBeNull();
  });
});

describe("engine state fidelity", () => {
  const finding: engine.EngineFinding = {
    id: "finding-unknown",
    vulnclass: "test",
    name: "미분류 발견",
    severity: "future-level",
    status: "pending",
    summary: "test",
    ts: "2026-10-30T00:00:00Z",
  };
  it.each(["report", "live"] as const)("keeps unknown severity unclassified in %s", async (id) => {
    vi.mocked(engine.listTaskFindings).mockResolvedValue([finding]);
    show(
      id,
      id === "report" ? <ReportScreen /> : <LiveScreen target={{ url: "http://example.test", host: "example.test" }} />,
    );
    if (id === "report") fireEvent.click(screen.getByRole("button", { name: /발견 목록/ }));
    expect(await screen.findByText("미분류 발견")).toBeVisible();
    const badge = screen.getByText("미분류 발견").closest(".find, .frow")?.querySelector(".sev");
    expect(badge).toHaveTextContent("미분류");
    expect(badge).not.toHaveClass("low");
  });
  it("counts queued tasks and opens timeout reports as incomplete", async () => {
    const task = await engine.getTask("task-1");
    vi.mocked(engine.listTasks).mockResolvedValue({
      active: "queued",
      tasks: [
        { ...task, id: "queued", name: "대기 점검", status: "queued" },
        { ...task, id: "timeout", name: "중단 점검", status: "timeout" },
      ],
    });
    show("history", <HistoryScreen />);
    const timedOut = (await screen.findByText("중단 점검")).closest(".hcard") as HTMLElement;
    expect(within(timedOut).getByRole("button", { name: "보고서" })).toBeVisible();
    expect(within(timedOut).getByText("시간초과 · 미완료")).toBeVisible();
    expect(screen.getByText("진행·대기 1")).toBeVisible();
    expect(screen.queryByText("발견 없음")).toBeNull();
  });
  it("offers the report after a live task times out", async () => {
    const task = await engine.getTask("task-1");
    vi.mocked(engine.getTask).mockResolvedValue({ ...task, status: "timeout" });
    show("live", <LiveScreen target={{ url: "http://example.test", host: "example.test" }} />);
    expect(await screen.findByRole("button", { name: "보고서" })).toBeVisible();
  });
});
