import "@testing-library/jest-dom/vitest";
import { type ReactNode, useState } from "react";

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import * as engine from "../lib/engine";
import { SelectionContext } from "../lib/selection";
import { NavContext, type ScreenId } from "../nav";
import FindingScreen from "./FindingScreen";
import ReportScreen from "./ReportScreen";

vi.mock("../lib/engine", () => ({
  getTask: vi.fn(),
  listTaskFindings: vi.fn(),
  getCoverage: vi.fn(),
  getFinding: vi.fn(),
  getFindingTraffic: vi.fn(),
  listFindingRetests: vi.fn(),
}));

/** ABC2LabUI 처럼 화면 전환 · 선택한 발견을 실제 상태로 들고 있는 틀 */
function Harness({ start, children }: { start: ScreenId; children: ReactNode }) {
  const [screenId, setScreen] = useState<ScreenId>(start);
  const [finding, setFinding] = useState<engine.EngineFinding | null>(null);
  return (
    <NavContext.Provider value={{ screen: screenId, go: setScreen }}>
      <SelectionContext.Provider
        value={{
          taskId: "7",
          tasksVersion: 0,
          selectTask: vi.fn(),
          refreshTasks: vi.fn(),
          finding,
          selectFinding: setFinding,
        }}
      >
        {children}
      </SelectionContext.Provider>
    </NavContext.Provider>
  );
}

function show(start: ScreenId = "report") {
  return render(
    <Harness start={start}>
      <ReportScreen />
      <FindingScreen />
    </Harness>,
  );
}

const ts = "2026-11-05T01:00:00Z";
const idor: engine.EngineFinding = {
  id: "node-31",
  finding_id: "31",
  vulnclass: "IDOR",
  name: "다른 사용자 주문 조회",
  severity: "high",
  status: "pending",
  summary: "주문 번호만 바꾸면 다른 사용자의 주문이 보여요.",
  evidence: "GET /api/orders/1012\nCookie: session=secret-abc\n→ 200, 계정 A의 주문",
  assets: [{ id: "a1", type: "endpoint", label: "GET /api/orders/{id}" }],
  task_id: "7",
  ts,
};
const noRow: engine.EngineFinding = {
  id: "node-40",
  vulnclass: "xss",
  name: "표에 없는 발견",
  severity: "low",
  status: "pending",
  summary: "검색어가 결과 화면에 그대로 보여요.",
  evidence: "",
  task_id: "7",
  ts,
};

beforeEach(() => {
  vi.resetAllMocks();
  vi.spyOn(window, "requestAnimationFrame").mockReturnValue(0);
  vi.mocked(engine.getTask).mockResolvedValue({
    id: "7",
    name: "쇼핑몰 점검",
    description: "",
    goal: "",
    status: "done",
    created_at: ts,
    completed_at: ts,
    goals_total: 0,
    goals_met: 0,
    in_flight: 0,
    findings: { critical: 0, high: 1, medium: 0, low: 1 },
  });
  vi.mocked(engine.getCoverage).mockResolvedValue(null);
  vi.mocked(engine.listTaskFindings).mockResolvedValue([idor, noRow]);
  vi.mocked(engine.getFinding).mockResolvedValue({
    ...idor,
    evidence: idor.evidence ?? "",
    status: "confirmed",
    report: "## 요약\nAuthorization: Bearer tok-123\n주문 주인을 확인하지 않아요.",
    report_stale: true,
    traffic_count: 2,
  });
  vi.mocked(engine.getFindingTraffic).mockResolvedValue([
    {
      id: "b1",
      role: "baseline",
      note: "계정 A 본인 주문",
      method: "GET",
      url: "https://shop.test/api/orders/1012",
      status: 200,
      contentType: "application/json",
      reqLen: 0,
      respLen: 2048,
      capturedAt: 0,
    },
    {
      id: "b2",
      role: "proof",
      note: "계정 B로 계정 A의 주문",
      method: "GET",
      url: "https://shop.test/api/orders/1012?token=leak-999",
      status: 200,
      contentType: "application/json",
      reqLen: 0,
      respLen: 2048,
      capturedAt: 0,
    },
  ]);
  vi.mocked(engine.listFindingRetests).mockResolvedValue([
    {
      id: 1,
      status: "completed",
      verdict: "reproduced",
      summary: "같은 요청이 여전히 200",
      error: "",
      createdAt: ts,
      finishedAt: ts,
    },
  ]);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

/** 보고서 [발견 목록] 탭에서 항목을 펼치고 [자세히 보기]를 누른다 */
async function openFromReport(name: string) {
  fireEvent.click(screen.getByRole("button", { name: /발견 목록/ }));
  fireEvent.click(await screen.findByText(name));
  fireEvent.click(screen.getByRole("button", { name: "자세히 보기" }));
}

const detail = () => screen.getByRole("heading", { level: 1 }).closest("section") as HTMLElement;

describe("발견 상세", () => {
  it("opens the chosen finding from the report and shows what the engine recorded", async () => {
    show();
    await openFromReport("다른 사용자 주문 조회");

    expect(await screen.findByRole("heading", { level: 1, name: "다른 사용자 주문 조회" })).toBeVisible();
    // 목록의 node id 가 아니라 발견 표 id 로, 이 점검 문맥에서 조회한다
    expect(engine.getFinding).toHaveBeenCalledWith("31", "7");
    expect(await within(detail()).findByText("처리 상태 · 검토에서 취약점으로 확인")).toBeVisible();

    // 증거 · 보고서의 쿠키 · 토큰 값은 가려서 보여 준다
    expect(within(detail()).getByText(/Cookie: \[REDACTED\]/)).toBeVisible();
    expect(within(detail()).getByText(/Authorization: \[REDACTED\]/)).toBeVisible();
    expect(within(detail()).getByText("증거가 바뀐 뒤 아직 다시 쓰이지 않은 보고서예요.")).toBeVisible();
    expect(detail()).not.toHaveTextContent(/secret-abc|tok-123|leak-999/);

    // 요청 · 응답 기록과 재검증 이력
    expect(engine.getFindingTraffic).toHaveBeenCalledWith("31", "7");
    expect(await within(detail()).findByText("기준 요청")).toBeVisible();
    expect(within(detail()).getByText("증명")).toBeVisible();
    expect(within(detail()).getByText(/token=\[REDACTED\]/)).toBeVisible();
    expect(engine.listFindingRetests).toHaveBeenCalledWith("31");
    expect(await within(detail()).findByText("재현됨")).toBeVisible();

    // 처리 상태를 판정 배지(확정 · 의심 · 판단 불가)로 꾸며 보이지 않는다
    expect(within(detail()).queryByText("확정")).toBeNull();
  });

  it("shows the list record without engine lookups when the finding has no findings-table row", async () => {
    show();
    await openFromReport("표에 없는 발견");

    expect(await screen.findByRole("heading", { level: 1, name: "표에 없는 발견" })).toBeVisible();
    expect(within(detail()).getByText("검색어가 결과 화면에 그대로 보여요.")).toBeVisible();
    expect(within(detail()).getByText("에이전트가 남긴 증거가 없어요.")).toBeVisible();
    expect(within(detail()).getByText(/발견 표에 등록되지 않은 발견이라/)).toBeVisible();
    expect(engine.getFinding).not.toHaveBeenCalled();
    expect(engine.listFindingRetests).not.toHaveBeenCalled();
  });

  it("skips the traffic lookup when nothing is bound and says why it may be empty", async () => {
    vi.mocked(engine.getFinding).mockResolvedValue({ ...idor, evidence: idor.evidence ?? "", traffic_count: 0 });
    vi.mocked(engine.listFindingRetests).mockResolvedValue([]);
    show();
    await openFromReport("다른 사용자 주문 조회");

    expect(await within(detail()).findByText(/이 발견에 묶인 요청 기록이 없어요/)).toBeVisible();
    expect(engine.getFindingTraffic).not.toHaveBeenCalled();
    expect(await within(detail()).findByText("아직 재검증한 적이 없어요.")).toBeVisible();
    expect(within(detail()).getByText("아직 작성된 보고서가 없어요.")).toBeVisible();
  });

  it("guides back to the report when no finding has been chosen", () => {
    show("finding");
    expect(screen.getByRole("heading", { level: 1, name: "발견 상세" })).toBeVisible();
    expect(screen.getByText(/\[자세히 보기\]를 누르면 여기서 열려요/)).toBeVisible();
    expect(engine.getFinding).not.toHaveBeenCalled();
  });
});
