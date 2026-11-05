import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import DashboardScreen from "./DashboardScreen";

vi.mock("../lib/engine", () => ({
  listTasks: vi.fn(),
  listPendingApprovals: vi.fn(),
  getTokenUsage: vi.fn(),
  getLLM: vi.fn(),
  getInterceptRules: vi.fn(),
  getCoverage: vi.fn(),
}));

const H = 3600_000;
const iso = (ago: number) => new Date(Date.now() - ago).toISOString();
const day = (ago: number) => new Date(Date.now() - ago).toISOString().slice(0, 10);
const task = (id: string, status: string, ageH: number, findings = { critical: 0, high: 0, medium: 0, low: 0 }) => ({
  id,
  name: `점검 ${id}`,
  description: "",
  goal: "",
  status,
  created_at: iso(ageH * H),
  completed_at: status === "running" || status === "paused" ? "" : iso((ageH - 1) * H),
  goals_total: 4,
  goals_met: 1,
  in_flight: 0,
  findings,
});

function show() {
  return render(
    <NavContext.Provider value={{ screen: "dashboard", go: vi.fn() }}>
      <SelectionContext.Provider value={{ taskId: null, tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn() }}>
        <DashboardScreen />
      </SelectionContext.Provider>
    </NavContext.Provider>,
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(engine.listTasks).mockResolvedValue({
    active: null,
    tasks: [
      task("run", "running", 1),
      task("pause", "paused", 2),
      task("good", "done", 5, { critical: 3, high: 2, medium: 6, low: 1 }),
      task("thin", "done", 30),
      task("fail", "timeout", 48),
    ],
  } as unknown as engine.TaskListResponse);
  vi.mocked(engine.listPendingApprovals).mockResolvedValue([
    { id: 1, task_id: "run", agent_name: "work#1", tool_name: "http_request", status: "pending", created_at: iso(H) },
  ]);
  vi.mocked(engine.getTokenUsage).mockResolvedValue({
    byProfile: [],
    daily: [
      { day: day(2 * 24 * H), input: 3_000_000, output: 600_000, cacheRead: 0 },
      { day: day(40 * 24 * H), input: 2_500_000, output: 500_000, cacheRead: 0 },
    ],
  });
  vi.mocked(engine.getLLM).mockResolvedValue({
    provider: "openai",
    model: "qwen/qwen3.6-35b-a3b",
    base_url: "http://127.0.0.1:1234/v1",
    configured: true,
    key_set: false,
    context_window_k: 0,
  } as engine.EngineLLM);
  vi.mocked(engine.getInterceptRules).mockResolvedValue([]);
  vi.mocked(engine.getCoverage).mockImplementation(async (id: string) => ({
    enabled: true,
    scopeRows: 10,
    denominator: 10,
    tested: id === "thin" ? 6 : 9,
    pct: 0,
    byType: [],
  }));
});
afterEach(cleanup);

const tile = (label: string) => screen.getByText(label, { selector: ".tile .k" }).closest(".tile") as HTMLElement;

describe("DashboardScreen", () => {
  it("fills the four tiles from engine data", async () => {
    show();
    await screen.findAllByText("점검 run", { selector: ".recent-main .nm" });
    expect(within(tile("진행 중인 점검")).getByText("2")).toBeVisible();
    expect(within(tile("진행 중인 점검")).getByText("실행 1 · 일시중지 1")).toBeVisible();
    expect(await within(tile("승인 대기")).findByText("1")).toBeVisible();
    // 확인 필요 = 시간초과 1 + 커버리지 60% 1 (일시중지는 진행 중인 점검에서만 센다)
    expect(await within(tile("확인 필요")).findByText("2")).toBeVisible();
    expect(within(tile("확인 필요")).getByText("끝까지 못 감 · 미검사 영역")).toBeVisible();
    // 최근 30일 3.6M, 그 전 30일 3.0M → +20%
    expect(await within(tile("토큰 사용량 · 최근 30일")).findByText(/지난 30일 대비 \+20%/)).toBeVisible();
  });

  it("draws finding bars as a share of all findings, not of the largest row", async () => {
    show();
    expect(await screen.findByText(/총 12건/)).toBeVisible();
    const medium = screen.getAllByText("중간")[0].closest(".dash-sevrow") as HTMLElement;
    expect((medium.querySelector(".bar > i") as HTMLElement).style.width).toBe("50%");
  });

  it("lists the low-coverage scan as something to check", async () => {
    show();
    const row = (await screen.findByText("미검사 영역 있음 · 커버리지 60%")).closest(".recent-row") as HTMLElement;
    expect(within(row).getByText("점검 thin")).toBeVisible();
  });

  it("shows four to-dos first and lists the rest behind 더보기", async () => {
    vi.mocked(engine.listPendingApprovals).mockResolvedValue(
      [1, 2, 3, 4, 5].map((id) => ({
        id,
        task_id: "run",
        agent_name: "work#1",
        tool_name: "http_request",
        status: "pending",
        created_at: iso(H),
      })),
    );
    show();
    const todoRows = () =>
      within(screen.getByText("지금 처리할 일").closest("section") as HTMLElement).getAllByText(/승인 대기|확인 필요/, {
        selector: ".stag",
      });
    // 승인 5 + 시간초과 1 + 덜 본 점검 1 = 7건 중 4건만
    expect(await screen.findByText("7건")).toBeVisible();
    await waitFor(() => expect(todoRows()).toHaveLength(4));
    const more = screen.getByRole("button", { name: "더보기 · 3건 더" });
    expect(more).toHaveAttribute("aria-expanded", "false");

    fireEvent.click(more);
    await waitFor(() => expect(todoRows()).toHaveLength(7));
    // 승인 대기가 먼저 온다
    expect(todoRows()[0]).toHaveTextContent("승인 대기");
    expect(screen.getByRole("button", { name: "접기" })).toHaveAttribute("aria-expanded", "true");

    fireEvent.click(screen.getByRole("button", { name: "접기" }));
    await waitFor(() => expect(todoRows()).toHaveLength(4));
  });

  it("hides the 더보기 button when four or fewer to-dos remain", async () => {
    show();
    await waitFor(() => expect(screen.getByText("3건")).toBeVisible());
    expect(screen.queryByRole("button", { name: /더보기/ })).toBeNull();
  });

  it("counts a paused scan once, under 진행 중인 점검 only", async () => {
    show();
    const section = (title: string) =>
      screen.getByText(title, { selector: ".recent-head span" }).closest("section") as HTMLElement;
    await waitFor(() => expect(within(tile("확인 필요")).getByText("2")).toBeVisible());
    expect(within(tile("진행 중인 점검")).getByText("실행 1 · 일시중지 1")).toBeVisible();
    // 처리할 일 목록에는 없고, 진행 중인 점검 목록에 일시중지로 한 번만 나온다
    expect(within(section("지금 처리할 일")).queryByText("점검 pause")).toBeNull();
    const row = within(section("진행 중인 점검")).getByText("점검 pause").closest(".recent-row") as HTMLElement;
    expect(within(row).getByText("일시중지")).toBeVisible();
  });
});
