// 자산 탐색(live): 점검 고르기 · 숨은 화면 · 찾은 경로(묶음 · 종류 거르기 · 주소 구조 · 상세) · 요청 로그(호스트 기준 · 가림 · 꺼짐)
// · 일시정지/재개 · 미지원(403) 섹션 접기. live/api 를 모의한다. 클래스 이름은 검사하지 않는다.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../live/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("../live/api")>();
  return {
    ...real,
    fetchTasks: vi.fn(),
    fetchPendingApprovals: vi.fn(),
    fetchEndpoints: vi.fn(),
    fetchTraffic: vi.fn(),
    fetchTrafficExchange: vi.fn(),
    fetchAssetRefs: vi.fn(),
    controlTask: vi.fn(),
  };
});

import { ApiError } from "@/abc2lab-ui/lib/api";

import { useApiTab } from "../lib/apiTab";
import type { LiveEndpoint, LiveTask, TrafficPage } from "../live/api";
import * as api from "../live/api";
import { setLiveAuthState } from "../live/auth";
import { LiveFeeds, resetFeeds } from "../live/feeds";
import { useSource } from "../live/source";
import { type Shell, ShellContext } from "../shell";
import { CrawlLive } from "../views/live/CrawlLive";

const m = {
  fetchTasks: vi.mocked(api.fetchTasks),
  fetchPendingApprovals: vi.mocked(api.fetchPendingApprovals),
  fetchEndpoints: vi.mocked(api.fetchEndpoints),
  fetchTraffic: vi.mocked(api.fetchTraffic),
  fetchAssetRefs: vi.mocked(api.fetchAssetRefs),
  controlTask: vi.mocked(api.controlTask),
};

const NOW = Math.floor(Date.now() / 1000);
function mkTask(over: Partial<LiveTask>): LiveTask {
  return {
    id: "11",
    name: "쇼핑몰",
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
    tokens: { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
    goals_total: 0,
    goals_met: 0,
    in_flight: 2,
    findings: { critical: 0, high: 0, medium: 0, low: 0 },
    llm_profile_ids: [],
    active_llm_profile_id: null,
    llm_failover_state: "",
    coverage_enabled: true,
    source_task_ids: [],
    ...over,
  };
}

function mkEp(id: number, method: string, url: string, params: string[] = []): LiveEndpoint {
  return {
    id,
    method,
    url,
    domain: "shop.example.com",
    root_domain: "example.com",
    port: null,
    params: params.map((name) => ({ name, location: "path", value: "", type: "" })),
    last_seen: new Date(Date.now() - 120_000).toISOString(),
    task_ids: [11],
    status_code: 200,
    page_title: "",
    task_source: "",
    task_source_summary: "",
  };
}

const EPS: LiveEndpoint[] = [
  mkEp(1, "GET", "https://shop.example.com/api/orders/{id}", ["id"]),
  mkEp(2, "POST", "https://shop.example.com/api/orders"),
  mkEp(3, "GET", "https://shop.example.com/"),
  mkEp(4, "POST", "https://shop.example.com/api/login"),
  mkEp(5, "GET", "https://shop.example.com/api/logout"),
  mkEp(6, "DELETE", "https://shop.example.com/admin/delete/{id}"),
  mkEp(7, "GET", "https://shop.example.com/products"),
];

function traffic(over: Partial<TrafficPage> = {}): TrafficPage {
  return {
    enabled: true,
    proxy: "",
    count: 90,
    total: 2,
    page: 0,
    size: 50,
    exchanges: [
      {
        id: "x2",
        ts: new Date().toISOString(),
        host: "shop.example.com",
        method: "GET",
        url: "https://shop.example.com/reset/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl?token=abc",
        status: 302,
        content_type: "text/html",
        resp_len: 2048,
      },
      {
        id: "x1",
        ts: new Date().toISOString(),
        host: "api.shop.example.com",
        method: "POST",
        url: "https://api.shop.example.com/api/orders",
        status: 201,
        content_type: "application/json",
        resp_len: 120,
      },
    ],
    ...over,
  };
}

const noop = () => undefined;
function shell(over: Partial<Shell> = {}): Shell {
  return {
    route: "crawl",
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

function renderCrawl(over: Partial<Shell> = {}) {
  return render(
    <ShellContext.Provider value={shell(over)}>
      <LiveFeeds />
      <CrawlLive />
    </ShellContext.Provider>,
  );
}

let tasks: LiveTask[] = [];
const panel = () => screen.getByRole("complementary", { name: "찾은 경로" });
const pills = () => within(panel()).queryAllByRole("button", { name: /상세 보기$/ });

beforeEach(() => {
  for (const f of Object.values(m)) f.mockReset();
  vi.mocked(api.fetchTrafficExchange).mockReset();
  tasks = [mkTask({})];
  m.fetchTasks.mockImplementation(async () => ({ active: "11", tasks }));
  m.fetchPendingApprovals.mockResolvedValue([]);
  m.fetchEndpoints.mockResolvedValue({ total: EPS.length, assets: EPS, truncated: false });
  m.fetchTraffic.mockResolvedValue(traffic());
  m.fetchAssetRefs.mockResolvedValue({ intents: [], facts: [], findings: [] });
  m.controlTask.mockResolvedValue({ id: "11", paused: true, queued: false, status: "paused" });
  resetFeeds();
  setLiveAuthState("ready");
  useSource.setState({ source: "live", taskId: "11" });
  useApiTab.setState({ tab: "info" });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("자산 탐색(live): 점검 고르기 · 숨은 화면", () => {
  it("고른 점검이 없으면 점검 고르기를 보이고, 고르면 그 점검을 부른다", async () => {
    useSource.setState({ taskId: null });
    renderCrawl();
    expect(await screen.findByRole("heading", { name: "점검을 고르세요" })).toBeInTheDocument();
    expect(m.fetchEndpoints).not.toHaveBeenCalled();
    fireEvent.click(await screen.findByRole("button", { name: /shop\.example\.com/ }));
    await waitFor(() => expect(m.fetchEndpoints).toHaveBeenCalledWith("11"));
    expect(useSource.getState().taskId).toBe("11");
  });

  it("목록에 없는 점검이면 안내와 점검 고르기, 부르지 않는다", async () => {
    useSource.setState({ taskId: "99" });
    renderCrawl();
    expect(await screen.findByText(/고른 점검을 엔진 목록에서 찾지 못했습니다/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "점검을 고르세요" })).toBeInTheDocument();
    expect(m.fetchEndpoints).not.toHaveBeenCalled();
  });

  it("지금 화면이 아니면(route 다름) 엔드포인트·트래픽을 부르지 않는다", async () => {
    renderCrawl({ route: "dashboard" });
    await waitFor(() => expect(m.fetchTasks).toHaveBeenCalled());
    await screen.findAllByText("shop.example.com");
    expect(m.fetchEndpoints).not.toHaveBeenCalled();
    expect(m.fetchTraffic).not.toHaveBeenCalled();
  });
});

describe("자산 탐색(live): 찾은 경로", () => {
  it("첫 경로 조각으로 묶고, 종류(추정)로 거르고, 주소 구조로 본다", async () => {
    renderCrawl();
    const apiCard = await within(panel()).findByRole("region", { name: "/api 4개" });
    expect(within(panel()).getByRole("region", { name: "/ 1개" })).toBeInTheDocument();
    expect(within(panel()).getByRole("region", { name: "/admin 1개" })).toBeInTheDocument();
    expect(within(panel()).getByRole("region", { name: "/products 1개" })).toBeInTheDocument();
    expect(within(apiCard).getAllByRole("button")).toHaveLength(4);
    expect(pills()).toHaveLength(7);
    // 추정 딱지는 범례에 한 번
    expect(within(panel()).getAllByText("추정")).toHaveLength(1);
    expect(panel()).toHaveTextContent("데이터 변경 1");
    expect(panel()).toHaveTextContent("제외 후보 2");

    fireEvent.click(within(panel()).getByRole("radio", { name: "데이터 변경" }));
    expect(pills().map((b) => b.getAttribute("aria-label"))).toEqual([
      "POST /api/orders, 데이터 변경(추정), 상세 보기",
    ]);
    fireEvent.click(within(panel()).getByRole("radio", { name: "제외 후보" }));
    expect(pills()).toHaveLength(2);

    fireEvent.click(within(panel()).getByRole("radio", { name: "전체" }));
    fireEvent.click(within(panel()).getByRole("radio", { name: "주소 구조" }));
    expect(within(panel()).getByText("shop.example.com")).toBeInTheDocument();
    expect(pills()).toHaveLength(7);
    fireEvent.click(within(panel()).getByRole("button", { name: "/api 접기" }));
    expect(within(panel()).getByRole("button", { name: "/api 펼치기" })).toHaveAttribute("aria-expanded", "false");
    expect(pills()).toHaveLength(3);
  });

  it("경로를 누르면 같은 패널에 상세(관찰한 사실 · 관련 기록), 목록으로 돌아온다", async () => {
    m.fetchAssetRefs.mockResolvedValue({
      intents: [
        {
          id: 9,
          kind: "intent",
          state: "running",
          summary: "?token=abc123 로 조회",
          source_task_id: "",
          inherited: false,
        },
      ],
      facts: [],
      findings: [],
    });
    renderCrawl();
    await within(panel()).findByRole("region", { name: "/api 4개" });
    fireEvent.click(within(panel()).getByRole("button", { name: "GET /api/orders/{id}, 조회(추정), 상세 보기" }));
    const back = within(panel()).getByRole("button", { name: "찾은 경로 목록" });
    expect(back).toHaveFocus();
    expect(within(panel()).queryByRole("region", { name: "/api 4개" })).toBeNull();
    expect(panel()).toHaveTextContent("id (path)");
    expect(panel()).toHaveTextContent("관련 점검1개");
    await waitFor(() => expect(m.fetchAssetRefs).toHaveBeenCalledWith("11", 1));
    expect(await within(panel()).findByText(/로 조회/)).toHaveTextContent("?token=[REDACTED] 로 조회");
    expect(within(panel()).getByText("진행 중")).toBeInTheDocument();
    expect(panel()).not.toHaveTextContent("abc123");

    fireEvent.click(back);
    expect(within(panel()).getByRole("region", { name: "/api 4개" })).toBeInTheDocument();
    expect(within(panel()).getByRole("button", { name: "GET /api/orders/{id}, 조회(추정), 상세 보기" })).toHaveFocus();
  });

  it("엔드포인트가 잘렸으면 받은 수를 알린다", async () => {
    m.fetchEndpoints.mockResolvedValue({ total: 900, assets: EPS, truncated: true });
    renderCrawl();
    expect(await within(panel()).findByText("서버 전체 900개 중 7개만 받았습니다.")).toBeInTheDocument();
  });
});

describe("자산 탐색(live): 요청 로그", () => {
  it("대상 호스트로 거른 최근 50건을 가려서 보이고 숫자를 채운다", async () => {
    renderCrawl();
    await waitFor(() => expect(m.fetchTraffic).toHaveBeenCalledWith({ host: "shop.example.com", size: 50 }));
    const log = screen.getByRole("region", { name: "요청 로그" });
    expect(await within(log).findByText("/reset/[REDACTED]")).toBeInTheDocument();
    expect(log).not.toHaveTextContent("eyJhbGci");
    expect(log).toHaveTextContent("호스트 기준");
    expect(within(log).getByText("302")).toBeInTheDocument();
    expect(log).toHaveTextContent("text/html · 2.0 KB");
    // 부분 일치로 섞인 다른 호스트는 둘째 줄에 호스트를 붙인다
    expect(log).toHaveTextContent("api.shop.example.com · application/json · 120 B");
    expect(screen.getByText("기록된 요청 · 호스트 기준")).toBeInTheDocument();
    expect(screen.getByText(/찾은 엔드포인트 · 조회 3 · 데이터 변경 1 \(추정\)/)).toBeInTheDocument();
    expect(
      screen.getByText("탐색 진행률(엔진에 탐색 단계 진행률이 없음): 엔진 미지원 — 백엔드에 아직 없는 기능입니다."),
    ).toBeInTheDocument();
  });

  it("트래픽 기록이 꺼져 있으면 안내하고 요청 수 칸을 숨긴다", async () => {
    m.fetchTraffic.mockResolvedValue(traffic({ enabled: false, total: 0, count: 0, exchanges: [] }));
    renderCrawl();
    expect(await screen.findByText("트래픽 기록이 꺼져 있습니다 (설정에서 traffic_capture)")).toBeInTheDocument();
    expect(screen.getByText("이 호스트로 기록된 요청이 없습니다.")).toBeInTheDocument();
    expect(screen.queryByText(/기록된 요청 · 호스트 기준/)).toBeNull();
  });

  it("설명에서 호스트를 못 뽑으면 트래픽을 부르지 않는다", async () => {
    tasks = [mkTask({ description: "주소 없음", name: "이름만" })];
    renderCrawl();
    expect(await screen.findByText(/대상 호스트를 찾지 못해/)).toBeInTheDocument();
    expect(m.fetchTraffic).not.toHaveBeenCalled();
  });
});

describe("자산 탐색(live): 일시정지/재개", () => {
  it("점검 중이면 일시정지를 보내고 목록을 다시 부른다", async () => {
    renderCrawl();
    const b = await screen.findByRole("button", { name: "일시정지" });
    await waitFor(() => expect(b).toBeEnabled());
    const before = m.fetchTasks.mock.calls.length;
    fireEvent.click(b);
    await waitFor(() => expect(m.controlTask).toHaveBeenCalledWith("11", "pause"));
    await waitFor(() => expect(m.fetchTasks.mock.calls.length).toBeGreaterThan(before));
    // 취소는 엔진 미지원
    const cancel = screen.getByRole("button", { name: /점검 취소/ });
    expect(cancel).toBeDisabled();
    expect(cancel).toHaveTextContent("엔진 미지원");
  });

  it("일시정지된 점검이면 다시 시작(resume), 끝난 점검이면 막힌다", async () => {
    tasks = [mkTask({ status: "paused", paused: true })];
    const { unmount } = renderCrawl();
    const b = await screen.findByRole("button", { name: "다시 시작" });
    await waitFor(() => expect(b).toBeEnabled());
    expect(b).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(b);
    await waitFor(() => expect(m.controlTask).toHaveBeenCalledWith("11", "resume"));
    unmount();

    resetFeeds();
    tasks = [mkTask({ status: "done", completed_unix: NOW })];
    renderCrawl();
    await screen.findByText("점검 완료");
    expect(screen.getByRole("button", { name: "일시정지" })).toBeDisabled();
  });

  it("이 서버 모드에서 안 되면(403) 짧게 알리고 버튼을 막는다", async () => {
    m.controlTask.mockRejectedValue(new ApiError(403, "forbidden"));
    renderCrawl();
    const b = await screen.findByRole("button", { name: "일시정지" });
    await waitFor(() => expect(b).toBeEnabled());
    fireEvent.click(b);
    expect(await screen.findByText("일시정지·재개: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "일시정지" })).toBeDisabled();
  });
});

describe("자산 탐색(live): 미지원 섹션", () => {
  it("엔드포인트·트래픽이 403 이면 그 섹션만 접는다", async () => {
    m.fetchEndpoints.mockRejectedValue(new ApiError(403, "forbidden"));
    m.fetchTraffic.mockRejectedValue(new ApiError(403, "forbidden"));
    renderCrawl();
    expect(await screen.findByText("찾은 경로: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    expect(await screen.findByText("요청 로그: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "자산 탐색" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
