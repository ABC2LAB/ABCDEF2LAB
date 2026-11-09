// live 점검 진행: 계획 순서·상태 · 지난 활동 5건/더 보기 · 가림 · 전송 통제(미지원 접힘) · 일시정지/재개 ·
// 발견 상세(finding_id 없으면 목록 값) · 숨긴 화면은 부르지 않음. live/api 를 모의한다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../live/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("../live/api")>();
  return {
    ...real,
    fetchTasks: vi.fn(),
    fetchPendingApprovals: vi.fn(),
    fetchTaskFindings: vi.fn(),
    fetchFinding: vi.fn(),
    fetchCoverage: vi.fn(),
    fetchIntents: vi.fn(),
    fetchActivity: vi.fn(),
    fetchRequestApprovals: vi.fn(),
    controlTask: vi.fn(),
  };
});

import { ApiError } from "@/abc2lab-ui/lib/api";

import type { LiveActivity, LiveFinding, LiveIntent, LiveTask } from "../live/api";
import * as api from "../live/api";
import { setLiveAuthState } from "../live/auth";
import { LiveFeeds, resetFeeds } from "../live/feeds";
import { useSource } from "../live/source";
import { type Shell, ShellContext, type View } from "../shell";
import { LiveLive } from "../views/live/LiveLive";

const m = {
  fetchTasks: vi.mocked(api.fetchTasks),
  fetchPendingApprovals: vi.mocked(api.fetchPendingApprovals),
  fetchTaskFindings: vi.mocked(api.fetchTaskFindings),
  fetchFinding: vi.mocked(api.fetchFinding),
  fetchCoverage: vi.mocked(api.fetchCoverage),
  fetchIntents: vi.mocked(api.fetchIntents),
  fetchActivity: vi.mocked(api.fetchActivity),
  fetchRequestApprovals: vi.mocked(api.fetchRequestApprovals),
  controlTask: vi.mocked(api.controlTask),
};

const NOW = Math.floor(Date.now() / 1000);

function mkTask(p: Partial<LiveTask>): LiveTask {
  return {
    id: "11",
    name: "쇼핑몰 점검",
    description: "대상 https://shop.example.com/ 접근 통제",
    goal: "",
    status: "running",
    created_at: "",
    created_unix: NOW - 600,
    completed_at: "",
    completed_unix: 0,
    last_activity_unix: 0,
    paused: false,
    queued: false,
    tokens: { input_tokens: 1_200_000, output_tokens: 300_000, cache_read_tokens: 0, cache_write_tokens: 0 },
    goals_total: 5,
    goals_met: 2,
    in_flight: 2,
    findings: { critical: 1, high: 0, medium: 1, low: 0 },
    llm_profile_ids: [],
    active_llm_profile_id: null,
    llm_failover_state: "",
    coverage_enabled: true,
    source_task_ids: [],
    ...p,
  };
}

function mkFinding(p: Partial<LiveFinding>): LiveFinding {
  return {
    id: "n0",
    finding_id: "",
    vulnclass: "IDOR",
    name: "",
    severity: "high",
    status: "pending",
    summary: "",
    evidence: "",
    traffic_count: 0,
    assets: [],
    task_id: "11",
    source_task_id: "",
    inherited: false,
    ts: "2026-11-09T10:00:00Z",
    ...p,
  };
}

const intent = (id: string, state: string, summary: string | null, raw?: string): LiveIntent => ({
  id,
  type: "explore",
  payload: raw ?? JSON.stringify({ summary }),
  priority: 0,
  state,
  origin: "",
  ts: "",
  source_task_id: "",
  inherited: false,
});

const act8: LiveActivity[] = Array.from({ length: 8 }, (_, i) => ({
  seq: i + 1,
  intent_id: "",
  worker: "w1",
  ts: `2026-11-09T10:00:0${i + 1}Z`,
  kind: i % 2 ? "tool_use" : "result",
  tool: "http_request",
  is_error: i + 1 === 3,
  summary: i + 1 === 8 ? "최신 활동 Bearer abcDEF123456 사용" : `활동 ${i + 1}`,
}));

const FINDINGS: LiveFinding[] = [
  mkFinding({
    id: "n1",
    finding_id: "",
    name: "주문 조회 IDOR",
    severity: "critical",
    status: "pending",
    summary: "B 계정 Authorization: Bearer abcDEF123456 로 A 주문 조회",
    evidence: "GET /api/orders/1\nCookie: session=xyz789",
    traffic_count: 2,
    assets: [{ id: "9", type: "endpoint", label: "GET https://shop.example.com/api/orders/1" }],
  }),
  mkFinding({
    id: "n2",
    finding_id: "77",
    vulnclass: "xss",
    severity: "medium",
    status: "confirmed",
    summary: "목록 요약",
  }),
];

function shell(route: View, goTo = vi.fn()): Shell {
  const noop = () => undefined;
  return {
    route,
    navSeq: 0,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: false,
    railOpen: false,
    railNewAtRoute: false,
    goTo,
    openInsp: noop,
    closeInsp: noop,
    openRail: noop,
    closeRail: noop,
    dashPick: noop,
  };
}

function renderLive(route: View = "live", goTo = vi.fn()) {
  return render(
    <ShellContext.Provider value={shell(route, goTo)}>
      <LiveFeeds />
      <LiveLive />
    </ShellContext.Provider>,
  );
}

function serve(tasks: LiveTask[] = [mkTask({})]) {
  m.fetchTasks.mockResolvedValue({ active: "11", tasks });
}

beforeEach(() => {
  for (const f of Object.values(m)) f.mockReset();
  serve();
  m.fetchPendingApprovals.mockResolvedValue([]);
  m.fetchTaskFindings.mockResolvedValue(FINDINGS);
  m.fetchCoverage.mockResolvedValue({
    enabled: true,
    scopeRows: 1,
    denominator: 40,
    tested: 12,
    pct: 0.3,
    byType: [{ type: "endpoint", total: 40, tested: 12 }],
  });
  m.fetchIntents.mockResolvedValue([]);
  m.fetchActivity.mockImplementation(async (_task, _limit, since = 0) =>
    since ? { items: [], cursor: since } : { items: act8, cursor: 8 },
  );
  m.fetchRequestApprovals.mockRejectedValue(new ApiError(503, "request approvals unavailable"));
  setLiveAuthState("ready");
  resetFeeds();
  useSource.setState({ source: "live", taskId: "11" });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("live 점검 진행", () => {
  it("진행 띠: 목표 기준 진행률 · 토큰 · 커버리지(엔드포인트) · 종료는 엔진 미지원", async () => {
    const { container } = renderLive();
    await waitFor(() => expect(container.querySelector("#lvMeta")).toHaveTextContent("12/40 엔드포인트 · 30%"));
    expect(container.querySelector("#lvPct")).toHaveTextContent("40%");
    expect(screen.getByRole("progressbar", { name: "목표 달성률" })).toHaveAttribute("aria-valuenow", "40");
    const meta = container.querySelector("#lvMeta");
    expect(meta).toHaveTextContent("진행률 기준 목표 달성");
    expect(meta).toHaveTextContent("목표 2/5");
    expect(meta).toHaveTextContent("토큰 1.5M (입력+출력)");
    expect(meta).toHaveTextContent("대상 shop.example.com 추정");
    expect(meta).not.toHaveTextContent("남은 시간");
    expect(screen.getByRole("heading", { level: 1, name: "점검 진행" })).toBeInTheDocument();
    expect(screen.getByText("점검 중")).toBeInTheDocument();
    const end = screen.getByTitle("일반 모드 엔진에는 점검 종료(stop) API 가 없습니다");
    expect(end).toHaveTextContent("종료 엔진 미지원");
    expect(screen.getByRole("button", { name: /발견 2/ })).toBeInTheDocument();
  });

  it("점검 계획: 진행 중 → 대기 → 완료 → 그 밖(원문), 요약은 가림", async () => {
    m.fetchIntents.mockResolvedValue([
      intent("5", "done", "로그인 폼 확인"),
      intent("4", "open", "주문 API IDOR 확인"),
      intent("3", "running", "관리자 경로 탐색 token=abc123"),
      intent("2", "blocked", "결제 변경"),
      intent("1", "open", null, "깨진 JSON"),
    ]);
    const { container } = renderLive();
    await waitFor(() => expect(container.querySelector("#lvPlan")).not.toBeNull());
    const rows = within(container.querySelector("#lvPlan") as HTMLElement).getAllByRole("listitem");
    expect(rows.map((li) => li.textContent)).toEqual([
      "관리자 경로 탐색 token=[REDACTED] · 진행 중",
      "주문 API IDOR 확인 · 대기",
      "깨진 JSON · 대기",
      "로그인 폼 확인 · 완료",
      "결제 변경 · blocked",
    ]);
    expect(container.querySelector("#lvPlanN")).toHaveTextContent("1/5 완료");
    expect(screen.getByText(/엔진의 의도 목록입니다\(고정 단계 아님\)/)).toBeInTheDocument();
    // 지금 하는 일 = 진행 중 의도
    expect(container.querySelector("#lvTask")).toHaveTextContent("관리자 경로 탐색 token=[REDACTED]");
    expect(container).not.toHaveTextContent("abc123");
  });

  it("지난 활동: 최신부터 5건 + 더 보기 → 전체(스크롤), 요약의 Bearer 토큰은 가림, 오류 줄 표시", async () => {
    const { container } = renderLive();
    await waitFor(() => expect(container.querySelector("#lvFeedN")).toHaveTextContent("8건"));
    const feed = container.querySelector("#lvFeed") as HTMLElement;
    let items = within(feed).getAllByRole("listitem");
    expect(items).toHaveLength(5);
    expect(items[0]).toHaveTextContent("최신 활동 Bearer [REDACTED] 사용");
    expect(container).not.toHaveTextContent("abcDEF123456");
    expect(items[1]).toHaveTextContent("활동 7");
    const more = screen.getByRole("button", { name: "이전 활동 3건 더 보기" });
    expect(more).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(more);
    items = within(feed).getAllByRole("listitem");
    expect(items).toHaveLength(8);
    expect(items[5]).toHaveTextContent("활동 3결과 · 오류");
    const less = screen.getByRole("button", { name: "최근 5건만 보기" });
    expect(less).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(less);
    expect(within(feed).getAllByRole("listitem")).toHaveLength(5);
  });

  it("전송 통제: run.execution.counts 와 보류 수로 네 칸, 미지원(503)이면 그 섹션만 접힌다", async () => {
    m.fetchRequestApprovals.mockResolvedValue({
      approvals: [{ state: "pending" }, { state: "pending" }, { state: "allowed" }],
      run: {
        execution: {
          counts: {
            attempts: 20,
            allowed: 12,
            denied: 2,
            unsupported: 1,
            forward_attempted: 12,
            responses: 11,
            unknown: 3,
            calls: 4,
          },
        },
      },
    } as never);
    const { container, unmount } = renderLive();
    await waitFor(() => expect(container.querySelector("#lvSent")).toHaveTextContent("12"));
    expect(container.querySelector("#lvBlk")).toHaveTextContent("3");
    expect(container.querySelector("#lvHold")).toHaveTextContent("2");
    expect(container.querySelector("#lvUnk")).toHaveTextContent("3");
    expect(screen.getByText("요청 접수 20 · 대상 전송 시도 12")).toBeInTheDocument();
    unmount();

    m.fetchRequestApprovals.mockReset();
    m.fetchRequestApprovals.mockRejectedValue(new ApiError(503, "unavailable"));
    const r2 = renderLive();
    await waitFor(() => expect(screen.getByText("전송 통제: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument());
    expect(r2.container.querySelector("#lvSent")).toBeNull();
    expect(screen.getByText("요청별 전송 통제는 첫 진단 모드에서만 제공됩니다.")).toBeInTheDocument();
    // 다른 섹션은 그대로
    expect(r2.container.querySelector("#lvFeed")).not.toBeNull();
  });

  it("일시정지 → controlTask(pause) 뒤 목록 다시, 일시정지된 점검은 다시 시작(resume), 409 는 서버 글", async () => {
    m.controlTask.mockResolvedValue({ id: "11", paused: true, queued: false, status: "paused" });
    const { unmount } = renderLive();
    const pause = await screen.findByRole("button", { name: "일시정지" });
    const before = m.fetchTasks.mock.calls.length;
    await act(async () => {
      fireEvent.click(pause);
    });
    expect(m.controlTask).toHaveBeenCalledWith("11", "pause");
    await waitFor(() => expect(m.fetchTasks.mock.calls.length).toBeGreaterThan(before));
    unmount();

    resetFeeds();
    serve([mkTask({ status: "paused", paused: true })]);
    m.controlTask.mockReset();
    m.controlTask.mockRejectedValue(new ApiError(409, "작업이 이미 일시정지되어 있습니다"));
    renderLive();
    const resume = await screen.findByRole("button", { name: "다시 시작" });
    expect(resume).toHaveAttribute("aria-pressed", "true");
    await act(async () => {
      fireEvent.click(resume);
    });
    expect(m.controlTask).toHaveBeenCalledWith("11", "resume");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "지금 상태에서는 바꿀 수 없습니다: 작업이 이미 일시정지되어 있습니다",
    );
  });

  it("끝난 점검: 일시정지·종료 없음, 보고서 보기 → goTo(report)", async () => {
    serve([mkTask({ status: "done", completed_unix: NOW - 60 })]);
    const goTo = vi.fn();
    renderLive("live", goTo);
    const btn = await screen.findByRole("button", { name: /보고서 보기/ });
    expect(screen.queryByRole("button", { name: "일시정지" })).toBeNull();
    expect(screen.queryByTitle("일반 모드 엔진에는 점검 종료(stop) API 가 없습니다")).toBeNull();
    fireEvent.click(btn);
    expect(goTo).toHaveBeenCalledWith("report");
  });

  it("발견 상세: finding_id 가 없으면 목록 값(조회 안 함), 있으면 fetchFinding 상세 · 처리 상태 · 가림", async () => {
    m.fetchFinding.mockResolvedValue({
      ...FINDINGS[1],
      summary: "상세 요약",
      report: "## 보고서\n재현 password=hunter2",
      evidence_version: 1,
      report_stale: false,
    });
    const { container } = renderLive();
    const insp = () => container.querySelector("#lvInsp") as HTMLElement;
    await waitFor(() => expect(within(insp()).getByRole("heading", { name: "발견 2건" })).toBeInTheDocument());
    expect(insp()).toHaveTextContent("오른쪽 딱지는 처리 상태입니다(판정 아님)");

    fireEvent.click(within(insp()).getByRole("button", { name: /주문 조회 IDOR/ }));
    expect(within(insp()).getByRole("heading", { level: 2, name: "주문 조회 IDOR" })).toBeInTheDocument();
    expect(insp()).toHaveTextContent("독립 발견 기록(finding_id)이 없어 목록 값으로 보입니다.");
    // 헤더 값은 줄 끝까지 가린다(넘치게 가리는 쪽)
    expect(insp()).toHaveTextContent("B 계정 Authorization: Bearer [REDACTED]");
    expect(insp()).toHaveTextContent("Cookie: session=[REDACTED]");
    expect(insp()).not.toHaveTextContent("abcDEF123456");
    expect(insp()).not.toHaveTextContent("xyz789");
    expect(insp()).toHaveTextContent("화면 표시용 가림 — 서버 원문은 가려지지 않습니다");
    expect(insp()).toHaveTextContent("처리 상태미검토");
    expect(insp()).toHaveTextContent("판정(확정·의심·판단 불가) · 재현 명령: 엔진 미지원");
    expect(m.fetchFinding).not.toHaveBeenCalled();

    fireEvent.click(within(insp()).getByRole("button", { name: "발견 목록" }));
    fireEvent.click(within(insp()).getByRole("button", { name: /xss/ }));
    await waitFor(() => expect(insp()).toHaveTextContent("상세 요약"));
    expect(m.fetchFinding).toHaveBeenCalledWith("77");
    expect(insp()).toHaveTextContent("재현 password=[REDACTED]");
    expect(insp()).not.toHaveTextContent("hunter2");
    expect(insp()).toHaveTextContent("검토 확인");
  });

  it("고른 점검이 없으면 진행 중 점검만 고르기, 숨긴 화면은 아무것도 부르지 않는다", async () => {
    serve([mkTask({}), mkTask({ id: "8", description: "https://blog.example.com", status: "done" })]);
    useSource.setState({ taskId: null });
    const { unmount } = renderLive();
    const pick = await screen.findByRole("button", { name: /shop\.example\.com/ });
    expect(screen.queryByRole("button", { name: /blog\.example\.com/ })).toBeNull();
    fireEvent.click(pick);
    expect(useSource.getState().taskId).toBe("11");
    await waitFor(() => expect(m.fetchTaskFindings).toHaveBeenCalledWith("11"));
    unmount();

    for (const f of [m.fetchTaskFindings, m.fetchCoverage, m.fetchIntents, m.fetchActivity, m.fetchRequestApprovals])
      f.mockClear();
    resetFeeds();
    renderLive("report");
    await waitFor(() => expect(screen.getByRole("button", { name: /발견 2/ })).toBeInTheDocument());
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30));
    });
    expect(m.fetchTaskFindings).not.toHaveBeenCalled();
    expect(m.fetchCoverage).not.toHaveBeenCalled();
    expect(m.fetchIntents).not.toHaveBeenCalled();
    expect(m.fetchActivity).not.toHaveBeenCalled();
    expect(m.fetchRequestApprovals).not.toHaveBeenCalled();
  });
});
