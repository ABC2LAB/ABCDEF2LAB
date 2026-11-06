import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext } from "../nav";
import ReportScreen from "./ReportScreen";

vi.mock("../lib/engine", () => ({
  getTask: vi.fn(),
  listTaskFindings: vi.fn(),
  getCoverage: vi.fn(),
  getCoverageGraph: vi.fn(),
}));

function show() {
  return render(
    <NavContext.Provider value={{ screen: "report", go: vi.fn() }}>
      <SelectionContext.Provider
        value={{ taskId: "task-1", tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn() }}
      >
        <ReportScreen />
      </SelectionContext.Provider>
    </NavContext.Provider>,
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  // jsdom 에는 scrollIntoView 가 없다(발견 열기 시 그 줄로 스크롤)
  Element.prototype.scrollIntoView = vi.fn();
  vi.mocked(engine.getTask).mockResolvedValue({
    id: "task-1",
    name: "시험 점검",
    description: "",
    goal: "",
    status: "done",
    created_at: "2026-11-04T00:00:00Z",
    completed_at: "2026-11-04T00:10:00Z",
    goals_total: 0,
    goals_met: 0,
    in_flight: 0,
    findings: { critical: 0, high: 1, medium: 0, low: 0 },
  });
  vi.mocked(engine.getCoverage).mockResolvedValue(null);
  vi.mocked(engine.listTaskFindings).mockResolvedValue([
    {
      id: "f-1",
      vulnclass: "IDOR",
      name: "다른 사용자의 주문 조회",
      severity: "high",
      status: "pending",
      summary: "",
      assets: [{ id: "12", type: "endpoint", label: "GET /api/orders/{id}" }],
      ts: "2026-11-04T00:00:00Z",
    },
  ]);
});
afterEach(cleanup);

describe("report access map tab", () => {
  it("shows the matrix inline with short cell labels", async () => {
    show();
    fireEvent.click(screen.getByRole("button", { name: "접근통제 지도" }));
    const table = await screen.findByRole("table");
    // 확정(vuln)/의심(suspected)은 서로 다른 셀 클래스·라벨로 구분한다(색이 1차, §4.2(7)).
    const confirmed = within(table).getAllByText("확정");
    const suspected = within(table).getAllByText("의심");
    expect(confirmed.length).toBeGreaterThan(0);
    expect(suspected.length).toBeGreaterThan(0);
    const confCell = confirmed[0].closest("td");
    const susCell = suspected[0].closest("td");
    expect(confCell).toHaveClass("cell-vuln");
    expect(susCell).toHaveClass("cell-suspected");
    expect(confCell?.className).not.toBe(susCell?.className);
    expect(screen.getByText("예시 데이터예요. 소유·권한 KG 연동 후 실제 결과로 채워져요.")).toBeVisible();
    // 예시 데이터에 판정 불가 칸이 있으니 범례에도 나온다
    expect(screen.getByText("판정 불가", { selector: ".leg span" })).toBeVisible();
  });
});

describe("report asset graph tab", () => {
  it("summarises coverage, links findings and switches to the table", async () => {
    vi.mocked(engine.getCoverageGraph).mockResolvedValue({
      nodes: [
        {
          key: "a:1",
          kind: "service",
          label: "https://shop.test",
          url: "https://shop.test",
          tested: true,
          in_scope: true,
          asset_id: 1,
        },
        {
          key: "a:11",
          kind: "endpoint",
          label: "",
          url: "https://shop.test/api/orders",
          tested: true,
          in_scope: true,
          asset_id: 11,
        },
        {
          key: "a:12",
          kind: "endpoint",
          label: "",
          url: "https://shop.test/api/orders/{id}",
          tested: true,
          in_scope: true,
          asset_id: 12,
        },
        {
          key: "a:13",
          kind: "endpoint",
          label: "",
          url: "https://shop.test/admin",
          tested: false,
          in_scope: true,
          asset_id: 13,
        },
      ],
      edges: [],
    });
    show();
    fireEvent.click(screen.getByRole("button", { name: "자산 그래프" }));
    expect(await screen.findByText("3개 중 2개 검사")).toBeVisible();
    expect(screen.getByText("shop.test:443")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "목록" }));
    const row = screen.getByText("/admin", { selector: "td" }).closest("tr") as HTMLElement;
    expect(within(row).getByText("미검사")).toBeVisible();
    // 발견 칩을 누르면 발견 목록 탭에서 그 항목이 펼쳐진다
    fireEvent.click(screen.getByRole("button", { name: "발견 열기: 다른 사용자의 주문 조회 · 위험도 높음" }));
    expect(await screen.findByRole("button", { name: /다른 사용자의 주문 조회/, expanded: true })).toBeVisible();
  });

  it.each([
    ["error", "자산 그래프를 불러오지 못했어요."],
    ["disabled", "자산 저장소가 꺼져 있어 그래프를 그릴 수 없어요."],
    ["empty", "아직 범위 안에 집계된 자산이 없어요."],
  ])("keeps the %s state distinct", async (state, text) => {
    if (state === "error") vi.mocked(engine.getCoverageGraph).mockRejectedValue(new Error("boom"));
    else vi.mocked(engine.getCoverageGraph).mockResolvedValue(state === "disabled" ? null : { nodes: [], edges: [] });
    show();
    fireEvent.click(screen.getByRole("button", { name: "자산 그래프" }));
    expect(await screen.findByText(text)).toBeVisible();
  });

  it("folds a large group into '+ N개 더' and lists that group when asked", async () => {
    const pages = Array.from({ length: 15 }, (_, i) => ({
      key: `a:${200 + i}`,
      kind: "endpoint",
      label: "",
      url: `https://shop.test/page-${String(i).padStart(2, "0")}`,
      tested: i !== 14,
      in_scope: true,
      asset_id: 200 + i,
    }));
    vi.mocked(engine.getCoverageGraph).mockResolvedValue({ nodes: pages, edges: [] });
    show();
    fireEvent.click(screen.getByRole("button", { name: "자산 그래프" }));
    // 미검사 1개는 반드시 그리고, 검사함 3개를 줄인다
    expect(await screen.findByText("/page-14")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "/ 묶음의 나머지 3개를 목록으로 보기" }));
    expect(screen.getByText(/묶음 15개만 보는 중/)).toBeVisible();
    const rows = screen.getAllByRole("row").slice(1);
    expect(rows).toHaveLength(15);
    // 목록은 중요한 순서 — 미검사가 맨 위
    expect(within(rows[0]).getByText("미검사")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "전체 보기" }));
    expect(screen.queryByText(/묶음 15개만 보는 중/)).toBeNull();
  });
});

describe("report coverage tab → full coverage", () => {
  it("'전체 커버리지 열기' 버튼이 go('coverage')로 이동한다", async () => {
    const go = vi.fn();
    render(
      <NavContext.Provider value={{ screen: "report", go }}>
        <SelectionContext.Provider
          value={{ taskId: "task-1", tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn() }}
        >
          <ReportScreen />
        </SelectionContext.Provider>
      </NavContext.Provider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "커버리지" }));
    fireEvent.click(await screen.findByRole("button", { name: /전체 커버리지 열기/ }));
    expect(go).toHaveBeenCalledWith("coverage");
  });
});

describe("report access map tab → full map", () => {
  it("'전체 접근통제 지도 열기' 버튼이 go('access-map')로 이동한다", async () => {
    const go = vi.fn();
    render(
      <NavContext.Provider value={{ screen: "report", go }}>
        <SelectionContext.Provider
          value={{ taskId: "task-1", tasksVersion: 0, selectTask: vi.fn(), refreshTasks: vi.fn() }}
        >
          <ReportScreen />
        </SelectionContext.Provider>
      </NavContext.Provider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "접근통제 지도" }));
    fireEvent.click(await screen.findByRole("button", { name: /전체 접근통제 지도 열기/ }));
    expect(go).toHaveBeenCalledWith("access-map");
  });
});
