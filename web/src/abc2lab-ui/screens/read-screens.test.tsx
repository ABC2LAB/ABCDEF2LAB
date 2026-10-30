import "@testing-library/jest-dom/vitest";
import type { ReactNode } from "react";

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext, type ScreenId } from "../nav";
import AuditScreen from "./AuditScreen";
import HistoryScreen from "./HistoryScreen";
import LiveScreen from "./LiveScreen";
import ReportScreen from "./ReportScreen";
import SettingsScreen from "./SettingsScreen";

vi.mock("../lib/engine", () => ({
  getTask: vi.fn(),
  getControlLog: vi.fn(),
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
  vi.mocked(engine.listTasks).mockResolvedValue({ active: "", tasks: [] });
  vi.mocked(engine.getControlLog).mockResolvedValue([]);
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

describe("unavailable progress data", () => {
  it.each(["queued", "failed", "timeout"])("does not infer completed phases from %s", async (status) => {
    const task = await engine.getTask("task-1");
    vi.mocked(engine.getTask).mockResolvedValue({ ...task, status });
    const view = show("live", <LiveScreen target={{ url: "http://example.test", host: "example.test" }} />);
    await waitFor(() => expect(engine.getTask).toHaveBeenCalled());
    expect(view.container.querySelectorAll(".pipe .done, .pipe .now, .pipe .on")).toHaveLength(0);
    expect(screen.getByText("점검 흐름 예시 · 단계별 진행 미연결")).toBeVisible();
  });
  it.each(["missing", "disabled", "failed"])("does not show zero percent for %s coverage", async (state) => {
    if (state === "failed") vi.mocked(engine.getCoverage).mockRejectedValue(new Error("synthetic coverage error"));
    if (state === "disabled")
      vi.mocked(engine.getCoverage).mockResolvedValue({
        enabled: false,
        denominator: 3,
        tested: 2,
        pct: 2 / 3,
        scopeRows: 1,
        byType: [],
      });
    show("live", <LiveScreen target={{ url: "http://example.test", host: "example.test" }} />);
    await waitFor(() => expect(engine.getCoverage).toHaveBeenCalled());
    if (state === "disabled") expect(await screen.findByText("커버리지 기능이 비활성화돼 있어요.")).toBeVisible();
    if (state === "failed") expect(await screen.findByText("커버리지를 불러오지 못했어요.")).toBeVisible();
    expect(screen.queryByText("0%")).toBeNull();
    expect(screen.getByText(/^검사 범위/).textContent).toContain("—");
    fireEvent.click(screen.getByRole("button", { name: "상세 보기" }));
    expect(screen.getByText(/^탐색 범위 미확인/)).toBeVisible();
  });
});

it("keeps a successful zero coverage measurement as zero", async () => {
  vi.mocked(engine.getCoverage).mockResolvedValue({
    enabled: true,
    denominator: 3,
    tested: 0,
    pct: 0,
    scopeRows: 1,
    byType: [],
  });
  show("live", <LiveScreen target={{ url: "http://example.test", host: "example.test" }} />);
  expect(await screen.findByText("0%")).toBeVisible();
});

describe("read state boundaries before conditional cleanup", () => {
  it("keeps settings lookup errors distinct from empty rules and unconfigured models", async () => {
    vi.mocked(engine.getInterceptRules).mockRejectedValue(new Error("rules unavailable"));
    vi.mocked(engine.getLLM).mockRejectedValue(new Error("model unavailable"));
    show("settings", <SettingsScreen />);
    expect(await screen.findByText(/통제 규칙을 불러오지 못했어요/)).toBeVisible();
    expect(await screen.findByText(/모델 설정을 불러오지 못했어요/)).toBeVisible();
    expect(screen.queryByText("등록된 통제 규칙이 없어요.")).toBeNull();
    expect(screen.queryByText("아직 모델이 연결되지 않았어요")).toBeNull();
  });
  it("keeps loading settings distinct from unconfigured settings", () => {
    vi.mocked(engine.getInterceptRules).mockReturnValue(
      new Promise(() => {
        /* Keep this request pending to verify the loading state. */
      }),
    );
    vi.mocked(engine.getLLM).mockReturnValue(
      new Promise(() => {
        /* Keep this request pending to verify the loading state. */
      }),
    );
    show("settings", <SettingsScreen />);
    expect(screen.getAllByText("불러오는 중…").length).toBeGreaterThan(0);
    expect(screen.queryByText("등록된 통제 규칙이 없어요.")).toBeNull();
    expect(screen.queryByText("아직 모델이 연결되지 않았어요")).toBeNull();
  });
  it("shows successfully empty settings without a lookup error", async () => {
    vi.mocked(engine.getLLM).mockResolvedValue({
      configured: false,
      provider: "",
      model: "",
      base_url: "",
      key_set: false,
      context_window_k: 0,
    });
    show("settings", <SettingsScreen />);
    expect(await screen.findByText("등록된 통제 규칙이 없어요.")).toBeVisible();
    expect(await screen.findByText("아직 모델이 연결되지 않았어요")).toBeVisible();
  });
  it.each(["loading", "error", "empty"])("keeps %s task history truthful", async (state) => {
    if (state === "loading")
      vi.mocked(engine.listTasks).mockReturnValue(
        new Promise(() => {
          /* Keep this request pending to verify the loading state. */
        }),
      );
    if (state === "error") vi.mocked(engine.listTasks).mockRejectedValue(new Error("history unavailable"));
    show("history", <HistoryScreen />);
    if (state === "loading") expect(screen.getByText("불러오는 중…")).toBeVisible();
    if (state === "error") expect(await screen.findByText("불러오기 실패")).toBeVisible();
    if (state === "empty") expect(await screen.findByText("아직 점검 기록이 없어요.")).toBeVisible();
    else expect(screen.queryByText("아직 점검 기록이 없어요.")).toBeNull();
  });
  it.each(["loading", "error", "empty"])("keeps %s control history truthful", async (state) => {
    if (state === "loading")
      vi.mocked(engine.getControlLog).mockReturnValue(
        new Promise(() => {
          /* Keep this request pending to verify the loading state. */
        }),
      );
    if (state === "error") vi.mocked(engine.getControlLog).mockRejectedValue(new Error("control unavailable"));
    show("audit", <AuditScreen />);
    if (state === "loading") expect(screen.getByText("불러오는 중…")).toBeVisible();
    if (state === "error") expect(await screen.findByText(/통제 기록을 불러오지 못했어요/)).toBeVisible();
    if (state === "empty") expect(await screen.findByText("이 점검에서 통제가 개입한 기록이 없어요.")).toBeVisible();
    else expect(screen.queryByText("이 점검에서 통제가 개입한 기록이 없어요.")).toBeNull();
  });
  it.each(["error", "disabled", "empty", "measured"])("keeps %s report coverage distinct", async (state) => {
    if (state === "error") vi.mocked(engine.getCoverage).mockRejectedValue(new Error("coverage unavailable"));
    else
      vi.mocked(engine.getCoverage).mockResolvedValue({
        enabled: state !== "disabled",
        denominator: state === "measured" ? 4 : 0,
        tested: state === "measured" ? 1 : 0,
        pct: 0,
        scopeRows: 1,
        byType: [],
      });
    show("report", <ReportScreen />);
    fireEvent.click(screen.getByRole("button", { name: "커버리지" }));
    const labels = {
      error: "커버리지를 불러오지 못했어요.",
      disabled: "이 점검은 커버리지 집계가 꺼져 있어요.",
      empty: "아직 범위(scope) 안에 집계된 자산이 없어요.",
      measured: "1/4 · 25%",
    };
    expect(await screen.findByText(labels[state as keyof typeof labels])).toBeVisible();
  });
  it.each(["loading", "error", "empty"])("keeps %s activity log distinct", async (state) => {
    if (state === "loading")
      vi.mocked(engine.getActivity).mockReturnValue(
        new Promise(() => {
          /* Keep this request pending to verify the loading state. */
        }),
      );
    if (state === "error") vi.mocked(engine.getActivity).mockRejectedValue(new Error("activity unavailable"));
    show("live", <LiveScreen target={{ url: "http://example.test", host: "example.test" }} />);
    fireEvent.click(screen.getByRole("button", { name: "상세 보기" }));
    const labels = {
      loading: "활동 기록을 불러오는 중이에요.",
      error: "활동 기록을 불러오지 못했어요.",
      empty: "기록된 활동이 없어요.",
    };
    expect(await screen.findByText(labels[state as keyof typeof labels])).toBeVisible();
    if (state !== "empty") expect(screen.queryByText("기록된 활동이 없어요.")).toBeNull();
  });
});
