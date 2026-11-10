// live 대시보드: 요약 칸(30일 발견 · 결정 대기 · 토큰) · 지금 처리할 일(도구 호출 승인) · 점검 목록 · 상세(승인 · 점검).
// live/api 를 모의한다(공용 조회 LiveFeeds 도 같은 모의를 쓴다).

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../live/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("../live/api")>();
  return {
    ...real,
    fetchTasks: vi.fn(),
    fetchPendingApprovals: vi.fn(),
    fetchFindings: vi.fn(),
    fetchTokenUsage: vi.fn(),
    fetchPendingApproval: vi.fn(),
    decidePendingApproval: vi.fn(),
  };
});

import { ApiError } from "@/abc2lab-ui/lib/api";

import * as api from "../live/api";
import { setLiveAuthState } from "../live/auth";
import { summarizeFindings, tokenSummary } from "../live/dashboard";
import { LiveFeeds, resetFeeds } from "../live/feeds";
import { useSource } from "../live/source";
import { type Shell, ShellContext, type View } from "../shell";
import { createInitialState, useWb } from "../store";
import { DashboardLive } from "../views/live/DashboardLive";

const m = vi.mocked(api);
const NOW_MS = Date.now();
const NOW = Math.floor(NOW_MS / 1000);
const DAY = 86_400;
const iso = (sec: number) => new Date(sec * 1000).toISOString();

function task(over: Partial<api.LiveTask> & { id: string }): api.LiveTask {
  return {
    name: "",
    description: "",
    goal: "",
    status: "running",
    created_at: "",
    created_unix: NOW - 600,
    completed_at: "",
    completed_unix: 0,
    last_activity_unix: 0,
    paused: false,
    queued: false,
    tokens: { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
    goals_total: 0,
    goals_met: 0,
    in_flight: 0,
    findings: { critical: 0, high: 0, medium: 0, low: 0 },
    llm_profile_ids: [],
    active_llm_profile_id: null,
    llm_failover_state: "",
    coverage_enabled: true,
    source_task_ids: [],
    ...over,
  };
}

const TASKS: api.LiveTaskList = {
  active: "11",
  tasks: [
    task({
      id: "11",
      name: "쇼핑몰 점검",
      description: "대상 https://shop.example.com",
      goals_total: 5,
      goals_met: 2,
      in_flight: 2,
      findings: { critical: 0, high: 1, medium: 0, low: 0 },
      tokens: { input_tokens: 1_200_000, output_tokens: 300_000, cache_read_tokens: 0, cache_write_tokens: 0 },
    }),
    task({ id: "12", description: "https://api.store-dev.com", status: "paused", created_unix: NOW - 300 }),
    task({
      id: "8",
      name: "블로그",
      description: "https://blog.example.com/",
      status: "done",
      created_unix: NOW - 2 * DAY,
      completed_unix: NOW - 2 * DAY + 1680,
      findings: { critical: 1, high: 1, medium: 1, low: 0 },
    }),
    task({
      id: "7",
      description: "오래된 점검 https://old.example.com",
      status: "failed",
      created_unix: NOW - 40 * DAY,
      completed_unix: NOW - 40 * DAY + 60,
    }),
  ],
};

function finding(over: Partial<api.LiveFinding> & { id: string }): api.LiveFinding {
  return {
    finding_id: over.id,
    vulnclass: "idor",
    name: "",
    severity: "medium",
    status: "pending",
    summary: "",
    evidence: "",
    traffic_count: 0,
    assets: [],
    task_id: "11",
    source_task_id: "",
    inherited: false,
    ts: iso(NOW - DAY),
    ...over,
  };
}

const FINDINGS: api.LiveFinding[] = [
  finding({ id: "1", severity: "critical" }),
  finding({ id: "2", severity: "high", ts: iso(NOW - 2 * DAY) }),
  finding({ id: "3", severity: "medium" }),
  finding({ id: "4", severity: "Medium" }),
  finding({ id: "5", severity: "info" }),
  // 30일 밖 · 시각 없음 → 세지 않음
  finding({ id: "6", severity: "low", ts: iso(NOW - 40 * DAY) }),
  finding({ id: "7", severity: "low", ts: "" }),
];

const TOKENS: api.TokenUsage = {
  days: 30,
  byProfile: [],
  daily: [
    { day: "2026-11-01", input: 2_000_000, output: 400_000, cacheRead: 9_000_000 },
    { day: "2026-11-08", input: 1_000_000, output: 200_000, cacheRead: 0 },
  ],
  total: { input: 3_000_000, output: 600_000, cacheRead: 9_000_000 },
};

const PENDING: api.PendingApproval[] = [
  {
    id: 1,
    task_id: "11",
    rule_id: 4,
    agent_name: "worker-1",
    tool_name: "http_request",
    status: "pending",
    created_at: iso(NOW - 300),
  },
  { id: 2, task_id: "99", rule_id: null, agent_name: "", tool_name: "shell", status: "pending", created_at: "" },
];

/** 가리지 않은 원문을 일부러 넣어 화면이 한 번 더 가리는지 본다. */
const DETAIL: api.PendingApprovalDetail = {
  ...PENDING[0],
  toolInputMasked:
    '{\n  "url": "https://shop.example.com/login",\n  "headers": {"Authorization": "Bearer abcdef123"},\n  "password": "hunter2"\n}',
  reason: "로그인 요청 · token=zzz999",
  decision_source: "",
  decided_at: null,
};

const noop = () => undefined;
function shellValue(over: Partial<Shell> = {}): Shell {
  return {
    route: "dashboard",
    navSeq: 1,
    narrow: false,
    phone: false,
    reduce: false,
    inspOpen: false,
    railOpen: false,
    railNewAtRoute: false,
    goTo: noop,
    openInsp: noop,
    closeInsp: noop,
    openRail: noop,
    closeRail: noop,
    dashPick: noop,
    ...over,
  };
}

function renderDash() {
  const goTo = vi.fn<(v: View) => void>();
  const openInsp = vi.fn();
  const r = render(
    <ShellContext.Provider value={shellValue({ goTo, openInsp })}>
      <LiveFeeds />
      <DashboardLive />
    </ShellContext.Provider>,
  );
  const cell = (k: string) => screen.getByText(k).parentElement as HTMLElement;
  const insp = () => screen.getByRole("complementary", { name: "선택한 항목 상세" });
  /** 선택자 요소가 나올 때까지 기다린다. */
  const el = (sel: string) =>
    waitFor(() => {
      const e = r.container.querySelector<HTMLElement>(sel);
      if (!e) throw new Error(`없음: ${sel}`);
      return e;
    });
  return { ...r, goTo, openInsp, cell, insp, el };
}

function serve() {
  m.fetchTasks.mockResolvedValue(TASKS);
  m.fetchPendingApprovals.mockResolvedValue(PENDING);
  m.fetchFindings.mockResolvedValue(FINDINGS);
  m.fetchTokenUsage.mockResolvedValue(TOKENS);
  m.fetchPendingApproval.mockResolvedValue(DETAIL);
  m.decidePendingApproval.mockResolvedValue(undefined);
}

beforeEach(() => {
  for (const f of [
    m.fetchTasks,
    m.fetchPendingApprovals,
    m.fetchFindings,
    m.fetchTokenUsage,
    m.fetchPendingApproval,
    m.decidePendingApproval,
  ])
    f.mockReset();
  useSource.setState({ source: "live", taskId: null });
  setLiveAuthState("ready");
  resetFeeds();
  useWb.setState(createInitialState());
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("대시보드 계산", () => {
  it("30일 발견: ts 로 거르고 심각도별 · 기타 · 상한", () => {
    const sm = summarizeFindings(FINDINGS, NOW_MS);
    expect(sm).toEqual({ total: 5, bySev: { critical: 1, high: 1, medium: 2, low: 0 }, other: 1, capped: false });
    const many = Array.from({ length: 500 }, (_, i) => finding({ id: String(i) }));
    expect(summarizeFindings(many, NOW_MS).capped).toBe(true);
  });

  it("토큰: 입력+출력 합, 점검당 평균은 30일 안에 만든 점검 수로", () => {
    const t = tokenSummary(TOKENS, TASKS.tasks, NOW_MS);
    expect(t).toEqual({ total: 3_600_000, tasks: 3, avg: 1_200_000 });
    expect(tokenSummary(TOKENS, [], NOW_MS).avg).toBeNull();
  });
});

describe("live 대시보드", () => {
  it("요약 칸 · 할 일 · 진행 중 · 끝난 점검을 엔진 값으로 그린다", async () => {
    serve();
    const { cell, container } = renderDash();
    await waitFor(() => expect(cell("발견된 취약점 · 최근 30일")).toHaveTextContent("5"));
    expect(cell("발견된 취약점 · 최근 30일")).toHaveTextContent("심각 1 · 높음 1 · 중간 2 · 낮음 0 · 기타 1");
    await waitFor(() => expect(cell("결정 대기")).toHaveTextContent(/^결정 대기2/));
    expect(cell("결정 대기")).toHaveTextContent("도구 호출 승인만 셈");
    expect(cell("결과 확인")).toHaveTextContent("엔진 미지원");
    expect(cell("결과 확인")).not.toHaveTextContent(/\d/);
    await waitFor(() => expect(cell("토큰 사용량 · 최근 30일")).toHaveTextContent("3.6M"));
    expect(cell("토큰 사용량 · 최근 30일")).toHaveTextContent("점검당 평균 1.2M");
    expect(within(cell("토큰 사용량 · 최근 30일")).getByText("추정")).toBeInTheDocument();

    // 지금 처리할 일: 승인 대기 두 줄 (점검 이름 · 도구)
    const todo = container.querySelector("#lvTodo") as HTMLElement;
    const rows = within(todo).getAllByRole("button");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("shop.example.com");
    expect(rows[0]).toHaveTextContent("http_request 호출");
    expect(rows[0]).toHaveTextContent("worker-1 · 실행 전 승인 필요");
    expect(rows[1]).toHaveTextContent("점검 #99");
    expect(screen.getByText(/새로 찾은 경로 · 판단 불가 항목은/)).toBeInTheDocument();

    // 진행 중 (최근 시작한 것부터) · 끝난 점검
    const active = container.querySelector("#lvActive") as HTMLElement;
    const act2 = within(active).getAllByRole("button");
    expect(act2[0]).toHaveTextContent("api.store-dev.com");
    expect(act2[0]).toHaveTextContent("일시정지됨");
    expect(act2[1]).toHaveTextContent("shop.example.com");
    expect(act2[1]).toHaveTextContent("목표 2/5");
    expect(act2[1].querySelector("i")).toHaveStyle({ width: "40%" });
    expect(act2[0].querySelector("i")).toBeNull();
    const done = container.querySelector("#lvDone") as HTMLElement;
    const dn = within(done).getAllByRole("button");
    expect(dn[0]).toHaveTextContent("blog.example.com");
    expect(dn[0]).toHaveTextContent("심각 1");
    expect(dn[0]).toHaveTextContent("발견 3");
    expect(dn[1]).toHaveTextContent("old.example.com");
    expect(dn[1]).toHaveTextContent("실패");
    expect(dn[1]).toHaveTextContent("발견 없음");
    expect(screen.getByText("전체 기록 2건")).toBeInTheDocument();
    // 상세는 비어 있다
    expect(screen.getByText("고른 항목 없음")).toBeInTheDocument();
    // 시안 숫자는 없다
    expect(screen.queryByText("4.2")).toBeNull();
    expect(screen.queryByText("판단 불가 2건")).toBeNull();
  });

  it("승인: 가린 도구 입력을 보이고 허용하면 decidePendingApproval 후 다시 부른다", async () => {
    serve();
    let release: () => void = noop;
    m.decidePendingApproval.mockImplementation(
      () =>
        new Promise<void>((res) => {
          release = res;
        }),
    );
    const { container, insp, openInsp, el } = renderDash();
    const row = await el('[data-appr="1"]');
    fireEvent.click(row);
    expect(openInsp).toHaveBeenCalled();
    expect(row).toHaveAttribute("aria-current", "true");
    await waitFor(() => expect(m.fetchPendingApproval).toHaveBeenCalledWith(1));
    const pre = await within(insp()).findByText(/shop\.example\.com\/login/);
    expect(pre).toHaveTextContent("[REDACTED]");
    expect(insp()).not.toHaveTextContent("hunter2");
    expect(insp()).not.toHaveTextContent("abcdef123");
    expect(insp()).not.toHaveTextContent("zzz999");
    expect(insp()).toHaveTextContent("화면 표시용 가림 — 서버 원문은 가려지지 않습니다");
    expect(insp()).toHaveTextContent("worker-1");
    expect(insp()).toHaveTextContent("shop.example.com (#11)");

    const before = m.fetchPendingApprovals.mock.calls.length;
    fireEvent.click(within(insp()).getByRole("button", { name: "허용" }));
    expect(m.decidePendingApproval).toHaveBeenCalledWith(1, "allowed");
    expect(within(insp()).getByRole("button", { name: "보내는 중…" })).toBeDisabled();
    expect(within(insp()).getByRole("button", { name: "거부" })).toBeDisabled();
    fireEvent.click(within(insp()).getByRole("button", { name: "거부" }));
    expect(m.decidePendingApproval).toHaveBeenCalledTimes(1);

    m.fetchPendingApproval.mockResolvedValue({ ...DETAIL, status: "allowed", decided_at: iso(NOW) });
    m.fetchPendingApprovals.mockResolvedValue(PENDING.slice(1));
    await act(async () => {
      release();
    });
    expect(await within(insp()).findByText(/허용했습니다/)).toBeInTheDocument();
    await waitFor(() => expect(m.fetchPendingApprovals.mock.calls.length).toBeGreaterThan(before));
    await waitFor(() => expect(container.querySelector('[data-appr="1"]')).toBeNull());
    expect(within(insp()).queryByRole("button", { name: "허용" })).toBeNull();
  });

  it("이미 결정된 요청(409)은 그렇게 알린다", async () => {
    serve();
    m.decidePendingApproval.mockRejectedValue(new ApiError(409, "already decided"));
    const { insp, el } = renderDash();
    fireEvent.click(await el('[data-appr="1"]'));
    fireEvent.click(await within(insp()).findByRole("button", { name: "거부" }));
    expect(await within(insp()).findByText("이미 결정된 요청입니다.")).toBeInTheDocument();
    expect(m.decidePendingApproval).toHaveBeenCalledWith(1, "denied");
  });

  it("미지원(403 · 503) 섹션만 접고 나머지는 그린다", async () => {
    serve();
    m.fetchFindings.mockRejectedValue(new ApiError(403, "first"));
    m.fetchTokenUsage.mockRejectedValue(new ApiError(503, "no db"));
    m.fetchPendingApprovals.mockRejectedValue(new ApiError(403, "first"));
    const { cell, container } = renderDash();
    await waitFor(() =>
      expect(cell("발견된 취약점 · 최근 30일")).toHaveTextContent("이 서버 모드에서는 제공되지 않음"),
    );
    await waitFor(() => expect(cell("토큰 사용량 · 최근 30일")).toHaveTextContent("이 서버 모드에서는 제공되지 않음"));
    await waitFor(() => expect(cell("결정 대기")).toHaveTextContent("이 서버 모드에서는 제공되지 않음"));
    expect(cell("결정 대기")).toHaveTextContent("—");
    expect(screen.getByText("도구 호출 승인: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    expect(container.querySelector("#lvTodo")).toBeNull();
    // 점검 목록은 그대로
    expect(await screen.findByText("shop.example.com")).toBeInTheDocument();
  });

  it("점검 줄을 누르면 고르고 상세 요약, 버튼으로 점검 진행 · 보고서 · 다시 점검", async () => {
    serve();
    const { container, insp, goTo, el } = renderDash();
    const shop = await el('#lvActive [data-task="11"]');
    fireEvent.click(shop);
    expect(useSource.getState().taskId).toBe("11");
    expect(insp()).toHaveTextContent("쇼핑몰 점검");
    expect(insp()).toHaveTextContent("2/5 달성");
    expect(insp()).toHaveTextContent("심각 0 · 높음 1 · 중간 0 · 낮음 0");
    expect(insp()).toHaveTextContent("입력 1.2M · 출력 300K");
    expect(insp()).toHaveTextContent("2개");
    expect(within(insp()).getByText("추정")).toBeInTheDocument();
    fireEvent.click(within(insp()).getByRole("button", { name: /점검 진행 열기/ }));
    expect(goTo).toHaveBeenLastCalledWith("live");

    fireEvent.click(container.querySelector('#lvDone [data-task="8"]') as HTMLElement);
    expect(useSource.getState().taskId).toBe("8");
    fireEvent.click(within(insp()).getByRole("button", { name: /보고서 열기/ }));
    expect(goTo).toHaveBeenLastCalledWith("report");
    fireEvent.click(within(insp()).getByRole("button", { name: "이 주소로 다시 점검" }));
    expect(useWb.getState().prefill?.url).toBe("https://blog.example.com");
    expect(goTo).toHaveBeenLastCalledWith("new");
  });

  it("새로고침은 점검 · 승인 대기 · 발견 · 토큰을 다시 부른다", async () => {
    serve();
    renderDash();
    await waitFor(() => expect(m.fetchFindings).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByText(/^조회 \d\d:\d\d:\d\d$/)).toBeInTheDocument());
    const calls = [m.fetchTasks, m.fetchPendingApprovals, m.fetchTokenUsage].map((f) => f.mock.calls.length);
    fireEvent.click(screen.getByRole("button", { name: "새로고침" }));
    await waitFor(() => expect(m.fetchFindings).toHaveBeenCalledTimes(2));
    expect(m.fetchTasks.mock.calls.length).toBeGreaterThan(calls[0]);
    expect(m.fetchPendingApprovals.mock.calls.length).toBeGreaterThan(calls[1]);
    expect(m.fetchTokenUsage.mock.calls.length).toBeGreaterThan(calls[2]);
    expect(m.fetchTokenUsage).toHaveBeenCalledWith(30);
  });
});
