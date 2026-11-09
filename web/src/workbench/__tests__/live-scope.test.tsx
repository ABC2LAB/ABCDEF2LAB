// 범위 확인(live): 점검 고르기 · 엔드포인트 표(묶음 · 종류 추정 · 거르기 · 검색 · 개수) · 포함/제외 미지원 ·
// 상세(관찰한 사실 · 관련 기록 가림 · 요청·응답 예시 가림 · 트래픽 꺼짐 · 맞는 기록 없음) · 미지원(403) · 점검 진행 보기.
// live/api 를 모의하고, 요청·응답 원문은 실제 fetchTrafficExchange(팀 apiFetch 모의)로 가림까지 확인한다.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/abc2lab-ui/lib/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/abc2lab-ui/lib/api")>();
  return { ...real, apiFetch: vi.fn() };
});

vi.mock("../live/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("../live/api")>();
  return {
    ...real,
    fetchTasks: vi.fn(),
    fetchPendingApprovals: vi.fn(),
    fetchEndpoints: vi.fn(),
    fetchTraffic: vi.fn(),
    fetchAssetRefs: vi.fn(),
  };
});

import * as teamApi from "@/abc2lab-ui/lib/api";
import { ApiError } from "@/abc2lab-ui/lib/api";

import { useApiTab } from "../lib/apiTab";
import type { LiveEndpoint, LiveTask, TrafficPage } from "../live/api";
import * as api from "../live/api";
import { setLiveAuthState } from "../live/auth";
import { LiveFeeds, resetFeeds } from "../live/feeds";
import { useSource } from "../live/source";
import { type Shell, ShellContext } from "../shell";
import { ScopeLive } from "../views/live/ScopeLive";

const apiFetch = vi.mocked(teamApi.apiFetch);
const m = {
  fetchTasks: vi.mocked(api.fetchTasks),
  fetchPendingApprovals: vi.mocked(api.fetchPendingApprovals),
  fetchEndpoints: vi.mocked(api.fetchEndpoints),
  fetchTraffic: vi.mocked(api.fetchTraffic),
  fetchAssetRefs: vi.mocked(api.fetchAssetRefs),
};

const NOW = Math.floor(Date.now() / 1000);
function mkTask(over: Partial<LiveTask> = {}): LiveTask {
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

function mkEp(id: number, method: string, url: string, params: string[] = []): LiveEndpoint {
  return {
    id,
    method,
    url,
    domain: "shop.example.com",
    root_domain: "example.com",
    port: 443,
    params: params.map((name) => ({ name, location: "query", value: "[REDACTED]", type: "" })),
    last_seen: new Date(Date.now() - 5 * 60_000).toISOString(),
    task_ids: [11, 12],
    status_code: 200,
    page_title: "",
    task_source: "",
    task_source_summary: "",
  };
}

const EPS: LiveEndpoint[] = [
  mkEp(1, "GET", "https://shop.example.com/api/orders/{id}", ["id", "token"]),
  mkEp(2, "POST", "https://shop.example.com/api/orders"),
  mkEp(3, "GET", "https://shop.example.com/"),
  mkEp(4, "POST", "https://shop.example.com/api/login"),
  mkEp(5, "GET", "https://shop.example.com/api/logout"),
  mkEp(6, "GET", "https://shop.example.com/products"),
];

const RAW_REQ =
  "GET /api/orders/42 HTTP/1.1\nHost: shop.example.com\nCookie: sid=s3cr3t-session; theme=dark\nAuthorization: Bearer abc.def.ghi\n";
const RAW_RESP = 'HTTP/1.1 200 OK\nContent-Type: application/json\n\n{"id":42,"access_token":"tok-999"}';

function traffic(over: Partial<TrafficPage> = {}): TrafficPage {
  const ts = new Date().toISOString();
  return {
    enabled: true,
    proxy: "",
    count: 10,
    total: 3,
    page: 0,
    size: 50,
    exchanges: [
      // 더 깊은 경로 · 다른 호스트는 맞지 않는다 (서버는 부분 일치)
      {
        id: "x3",
        ts,
        host: "shop.example.com",
        method: "GET",
        url: "https://shop.example.com/api/orders/42/items",
        status: 200,
        content_type: "",
        resp_len: 1,
      },
      {
        id: "x2",
        ts,
        host: "api.shop.example.com",
        method: "GET",
        url: "https://api.shop.example.com/api/orders/42",
        status: 200,
        content_type: "",
        resp_len: 1,
      },
      {
        id: "x1",
        ts,
        host: "shop.example.com",
        method: "GET",
        url: "https://shop.example.com/api/orders/42?x=1",
        status: 200,
        content_type: "application/json",
        resp_len: 40,
      },
    ],
    ...over,
  };
}

const noop = () => undefined;
function shell(over: Partial<Shell> = {}): Shell {
  return {
    route: "scope",
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

function renderScope(over: Partial<Shell> = {}) {
  return render(
    <ShellContext.Provider value={shell(over)}>
      <LiveFeeds />
      <ScopeLive />
    </ShellContext.Provider>,
  );
}

const insp = () => screen.getByRole("complementary", { name: "선택한 경로 상세" });
/** 표의 줄 버튼 (엔드포인트 id 로) */
const rowBtn = (id: number) => {
  const b = document.querySelector<HTMLElement>(`[data-lsel="${id}"]`);
  if (!b) throw new Error(`줄 ${id} 없음`);
  return b;
};
const chip = (label: string) => screen.getByRole("button", { name: new RegExp(`^${label}\\d+$`) });

beforeEach(() => {
  for (const f of Object.values(m)) f.mockReset();
  apiFetch.mockReset();
  m.fetchTasks.mockResolvedValue({ active: "11", tasks: [mkTask()] });
  m.fetchPendingApprovals.mockResolvedValue([]);
  m.fetchEndpoints.mockResolvedValue({ total: EPS.length, assets: EPS, truncated: false });
  m.fetchTraffic.mockResolvedValue(traffic());
  m.fetchAssetRefs.mockResolvedValue({ intents: [], facts: [], findings: [] });
  apiFetch.mockImplementation(async (path: string) => {
    if (path === "/api/traffic/exchange?id=x1") return { req: RAW_REQ, resp: RAW_RESP } as never;
    throw new ApiError(404, path);
  });
  resetFeeds();
  setLiveAuthState("ready");
  useSource.setState({ source: "live", taskId: "11" });
  useApiTab.setState({ tab: "info" });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("범위 확인(live): 점검 고르기 · 숨은 화면", () => {
  it("고른 점검이 없으면 점검 고르기", async () => {
    useSource.setState({ taskId: null });
    renderScope();
    expect(await screen.findByRole("heading", { name: "점검을 고르세요" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "점검 진행 보기" })).toBeNull();
    expect(m.fetchEndpoints).not.toHaveBeenCalled();
  });

  it("지금 화면이 아니면 부르지 않는다", async () => {
    renderScope({ route: "crawl" });
    await screen.findAllByText("shop.example.com");
    expect(m.fetchEndpoints).not.toHaveBeenCalled();
    expect(m.fetchAssetRefs).not.toHaveBeenCalled();
  });
});

describe("범위 확인(live): 엔드포인트 표", () => {
  it("묶음 · 종류(추정) · 개수 · 거르기 · 검색, 포함/제외는 엔진 미지원", async () => {
    renderScope();
    await screen.findByText("/api");
    const boxes = screen.getAllByRole("checkbox");
    expect(boxes).toHaveLength(6);
    for (const b of boxes) {
      expect(b).toBeDisabled();
      expect((b as HTMLInputElement).indeterminate).toBe(true);
    }
    expect(
      screen.getAllByText("엔드포인트별 포함·제외 저장: 엔진 미지원 — 백엔드에 아직 없는 기능입니다."),
    ).toHaveLength(1);
    expect(screen.getByText("엔드포인트").parentElement).toHaveTextContent(
      "엔드포인트6조회3데이터 변경1로그인1제외 후보1추정",
    );
    expect(rowBtn(1)).toHaveTextContent("파라미터 2 · 마지막 관찰 5분 전");

    fireEvent.click(chip("데이터 변경"));
    expect(screen.getAllByRole("checkbox")).toHaveLength(1);
    expect(rowBtn(2)).toBeInTheDocument();
    fireEvent.click(chip("ID 포함"));
    expect(screen.getAllByRole("checkbox")).toHaveLength(1);
    expect(rowBtn(1)).toBeInTheDocument();

    fireEvent.click(chip("전체"));
    fireEvent.change(screen.getByRole("searchbox", { name: "경로 검색" }), { target: { value: "ORDERS" } });
    expect(screen.getAllByRole("checkbox")).toHaveLength(2);
    // 파라미터 이름으로도 찾는다
    fireEvent.change(screen.getByRole("searchbox", { name: "경로 검색" }), { target: { value: "token" } });
    expect(screen.getAllByRole("checkbox")).toHaveLength(1);
    fireEvent.change(screen.getByRole("searchbox", { name: "경로 검색" }), { target: { value: "없는경로" } });
    expect(screen.getByText("조건에 맞는 경로가 없습니다.")).toBeInTheDocument();
  });

  it("잘린 목록이면 알린다", async () => {
    m.fetchEndpoints.mockResolvedValue({ total: 1200, assets: EPS, truncated: true });
    renderScope();
    expect(await screen.findByText(/서버 전체 1,200개 중 6개만 받았습니다/)).toBeInTheDocument();
  });

  it("엔드포인트가 403 이면 표 자리만 접는다", async () => {
    m.fetchEndpoints.mockRejectedValue(new ApiError(403, "forbidden"));
    renderScope();
    expect(await screen.findByText("엔드포인트: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "범위 확인" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("'점검 진행 보기'는 점검 진행으로 이동만 한다", async () => {
    const goTo = vi.fn();
    renderScope({ goTo });
    fireEvent.click(await screen.findByRole("button", { name: "점검 진행 보기" }));
    expect(goTo).toHaveBeenCalledWith("live");
    expect(screen.getByText(/엔진은 점검을 만들 때 바로 시작했습니다/)).toBeInTheDocument();
  });
});

describe("범위 확인(live): 상세", () => {
  it("처음엔 표의 첫 줄, 고르면 관찰한 사실과 관련 기록(가림)", async () => {
    m.fetchAssetRefs.mockImplementation(async (_t, id) =>
      id === 1
        ? {
            intents: [
              {
                id: 5,
                kind: "intent",
                state: "open",
                summary: "password=hunter2 로 로그인",
                source_task_id: "",
                inherited: false,
              },
            ],
            facts: [
              { id: 6, kind: "fact", state: "done", summary: "주문 조회 가능", source_task_id: "3", inherited: true },
            ],
            findings: [
              {
                id: 7,
                kind: "finding",
                state: "open",
                summary: "Authorization: Bearer zzz.yyy 노출",
                source_task_id: "",
                inherited: false,
              },
            ],
          }
        : { intents: [], facts: [], findings: [] },
    );
    renderScope();
    // "/" 묶음이 먼저 → 첫 줄은 GET /
    await waitFor(() => expect(m.fetchAssetRefs).toHaveBeenCalledWith("11", 3));
    expect(await within(insp()).findByText("이 경로에 걸린 의도·사실·발견이 아직 없습니다.")).toBeInTheDocument();

    fireEvent.click(rowBtn(1));
    expect(rowBtn(1)).toHaveAttribute("aria-current", "true");
    const p = insp();
    expect(within(p).getByRole("heading", { level: 2 })).toHaveTextContent("GET /api/orders/{id}");
    expect(p).toHaveTextContent("도메인shop.example.com:443");
    expect(p).toHaveTextContent("파라미터2개 · id (query), token (query)");
    expect(p).toHaveTextContent("마지막 관찰5분 전");
    expect(p).toHaveTextContent("관련 점검2개");
    expect(p).not.toHaveTextContent("[REDACTED] (query)");
    expect(await within(p).findByText(/로 로그인/)).toHaveTextContent("password=[REDACTED] 로 로그인");
    expect(within(p).getByText("대기")).toBeInTheDocument();
    expect(within(p).getByText(/주문 조회 가능/)).toHaveTextContent("주문 조회 가능 · 이어받음");
    // 헤더 값은 줄 끝까지 가린다 (덜 가리는 것보다 낫다)
    expect(within(p).getByText(/^Authorization:/)).toHaveTextContent("Authorization: Bearer [REDACTED]");
    expect(p).not.toHaveTextContent("zzz");
    expect(p).not.toHaveTextContent("hunter2");
    expect(p).toHaveTextContent("화면 표시용 가림 — 서버 원문은 가려지지 않습니다");
  });

  it("요청·응답 예시: 맞는 가장 최근 기록을 찾아 원문을 가려 보인다", async () => {
    renderScope();
    await screen.findByText("/api");
    fireEvent.click(rowBtn(1));
    expect(m.fetchTraffic).not.toHaveBeenCalled();
    fireEvent.click(within(insp()).getByRole("tab", { name: "요청·응답 예시" }));
    await waitFor(() =>
      expect(m.fetchTraffic).toHaveBeenCalledWith({
        host: "shop.example.com",
        method: "GET",
        path: "/api/orders/",
        size: 50,
      }),
    );
    const p = insp();
    const req = await within(p).findByText(/GET \/api\/orders\/42 HTTP\/1\.1/, { selector: "span" });
    const code = req.closest("pre") as HTMLElement;
    expect(code).toHaveTextContent("Cookie: sid=[REDACTED]; theme=[REDACTED]");
    expect(code).toHaveTextContent("Authorization: Bearer [REDACTED]");
    expect(p).not.toHaveTextContent("s3cr3t");
    expect(p).not.toHaveTextContent("tok-999");
    expect(p).toHaveTextContent('"access_token":"[REDACTED]"');
    expect(apiFetch).toHaveBeenCalledWith("/api/traffic/exchange?id=x1");
    expect(p).toHaveTextContent("화면 표시용 가림 — 서버 원문은 가려지지 않습니다");
    expect(within(p).getByRole("heading", { name: "요청 예시" })).toBeInTheDocument();
    expect(within(p).getByRole("heading", { name: "응답 예시" })).toBeInTheDocument();
  });

  it("맞는 기록이 없거나 기록이 꺼져 있으면 그렇게 말한다", async () => {
    m.fetchTraffic.mockResolvedValue(traffic({ enabled: false, exchanges: [] }));
    useApiTab.setState({ tab: "ex" });
    renderScope();
    expect(
      await within(insp()).findByText("트래픽 기록이 꺼져 있습니다 (설정에서 traffic_capture)"),
    ).toBeInTheDocument();
    expect(within(insp()).getByText("이 경로와 맞는 기록된 요청이 없습니다 (최근 50건 기준).")).toBeInTheDocument();
    expect(apiFetch).not.toHaveBeenCalled();
  });

  it("관련 기록 · 요청 예시가 403 이면 그 칸만 접는다", async () => {
    m.fetchAssetRefs.mockRejectedValue(new ApiError(403, "forbidden"));
    m.fetchTraffic.mockRejectedValue(new ApiError(403, "forbidden"));
    renderScope();
    expect(await within(insp()).findByText("관련 기록: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    fireEvent.click(within(insp()).getByRole("tab", { name: "요청·응답 예시" }));
    expect(await within(insp()).findByText("요청·응답 예시: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    expect(within(insp()).getByRole("heading", { name: "관찰한 사실", hidden: true })).toBeInTheDocument();
  });
});
