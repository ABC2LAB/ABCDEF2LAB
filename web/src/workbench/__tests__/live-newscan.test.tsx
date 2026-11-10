// live 새 점검: 엔진이 받는 칸만으로 POST /api/tasks(createTask) 를 만들고, 엔진에 없는 칸은 막아 둔다.
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
    fetchLlmProfiles: vi.fn(),
    createTask: vi.fn(),
  };
});

import { ApiError } from "@/abc2lab-ui/lib/api";

import * as api from "../live/api";
import { setLiveAuthState } from "../live/auth";
import { recentTargets } from "../live/dashboard";
import { LiveFeeds, resetFeeds } from "../live/feeds";
import { initialLiveForm, liveScanView, liveTarget, normalizeLiveUrl } from "../live/newScan";
import { useSource } from "../live/source";
import { type Shell, ShellContext, type View } from "../shell";
import { createInitialState, useWb } from "../store";
import { NewScanLive } from "../views/live/NewScanLive";

const m = vi.mocked(api);
const NOW = Math.floor(Date.now() / 1000);

function task(over: Partial<api.LiveTask> & { id: string }): api.LiveTask {
  return {
    name: "",
    description: "",
    goal: "",
    status: "done",
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
  active: "",
  tasks: [
    task({ id: "1", description: "대상 https://old.example.com/a", created_unix: NOW - 9000 }),
    task({ id: "2", description: "대상 https://blog.example.com", created_unix: NOW - 100 }),
    task({ id: "3", description: "대상 https://blog.example.com/x", created_unix: NOW - 5000 }),
    task({ id: "4", description: "주소 없는 점검", created_unix: NOW - 50 }),
  ],
};

const PROFILES: api.LlmProfile[] = [
  {
    id: "2",
    name: "외부",
    format: "openai",
    base_url: "https://api.openai.com/v1",
    model: "gpt-x",
    is_default: false,
    priority: 2,
  },
  {
    id: "3",
    name: "로컬",
    format: "openai",
    base_url: "http://127.0.0.1:1234/v1",
    model: "qwen3",
    is_default: true,
    priority: 1,
  },
];

const noop = () => undefined;
function shellValue(over: Partial<Shell> = {}): Shell {
  return {
    route: "new",
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

function renderNew() {
  const goTo = vi.fn<(v: View) => void>();
  const r = render(
    <ShellContext.Provider value={shellValue({ goTo })}>
      <LiveFeeds />
      <NewScanLive />
    </ShellContext.Provider>,
  );
  const url = () => screen.getByLabelText(/점검할 사이트 주소/) as HTMLInputElement;
  const name = () => screen.getByLabelText(/작업 이름/) as HTMLInputElement;
  const starts = () => screen.getAllByRole("button", { name: /점검 시작|만드는 중/ });
  const type = (el: HTMLElement, value: string) => fireEvent.change(el, { target: { value } });
  const body = () => JSON.parse(r.container.querySelector("#lvBody")?.textContent ?? "{}") as Record<string, unknown>;
  return { ...r, goTo, url, name, starts, type, body };
}

beforeEach(() => {
  for (const f of [m.fetchTasks, m.fetchPendingApprovals, m.fetchLlmProfiles, m.createTask]) f.mockReset();
  m.fetchTasks.mockResolvedValue(TASKS);
  m.fetchPendingApprovals.mockResolvedValue([]);
  m.fetchLlmProfiles.mockResolvedValue(PROFILES);
  m.createTask.mockResolvedValue(task({ id: "21", status: "running" }));
  useSource.setState({ source: "live", taskId: null });
  setLiveAuthState("ready");
  resetFeeds();
  useWb.setState(createInitialState());
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("새 점검 계산", () => {
  it("대상: http(s) 만, 계정 정보 금지, 경로 유지 · 끝 / 는 뗌", () => {
    expect(liveTarget("").ok).toBe(false);
    expect(liveTarget("ftp://x.example.com")).toMatchObject({ ok: false, error: /http 또는 https/ });
    expect(liveTarget("https://u:p@x.example.com")).toMatchObject({ ok: false, error: /계정 정보/ });
    expect(liveTarget("shop.example.com")).toMatchObject({
      ok: true,
      internal: false,
      target: "https://shop.example.com",
    });
    expect(liveTarget("http://10.0.0.5:8080/app?x=1")).toMatchObject({
      ok: true,
      internal: true,
      target: "http://10.0.0.5:8080/app?x=1",
    });
    expect(normalizeLiveUrl("shop.example.com/app")).toBe("https://shop.example.com/app");
    expect(normalizeLiveUrl("https://shop.example.com")).toBeNull();
  });

  it("입력: 설명에 대상 URL, 기본 프로필, 제외 경로 → block fuzzy_url, 가운데 * 는 막음", () => {
    const v = liveScanView(
      { ...initialLiveForm(), url: "https://a.example.com", name: "a 점검", excl: "/admin*\n\n/x" },
      PROFILES,
    );
    expect(v.input).toEqual({
      name: "a 점검",
      description: "대상 https://a.example.com",
      goal: "",
      llm_profile_ids: ["3"],
      coverage_enabled: true,
      intercept_rules: api.excludePathRules(["/admin", "/x"]),
    });
    const bad = liveScanView({ ...initialLiveForm(), url: "https://a.example.com", name: "a", excl: "/a/*/b" }, null);
    expect(bad.input).toBeNull();
    expect(bad.why).toContain("제외 경로의 * 는 끝에만 쓸 수 있습니다");
  });

  it("최근 주소: hostname 으로 중복 제거, 최근 것부터", () => {
    expect(recentTargets(TASKS.tasks).map((r) => r.url)).toEqual([
      "https://blog.example.com",
      "https://old.example.com",
    ]);
  });
});

describe("live 새 점검", () => {
  it("입력값으로 createTask 를 부르고, 만든 점검을 골라 점검 진행으로 간다", async () => {
    const { url, name, starts, type, goTo, body } = renderNew();
    expect(starts()[0]).toBeDisabled();
    type(url(), "shop.example.com/app");
    fireEvent.blur(url());
    expect(url().value).toBe("https://shop.example.com/app");
    expect(name().value).toBe("shop.example.com 점검");
    expect(screen.getByText("외부 주소")).toBeInTheDocument();
    type(screen.getByLabelText(/점검 목표/), "  주문 IDOR 확인 ");
    type(screen.getByLabelText(/제외 경로/), "/api/admin/reset\n/logout*\n");
    // 기본 프로필(is_default) 이 골라져 있고 로컬(추정)
    const sel = (await screen.findByLabelText("LLM 프로필")) as HTMLSelectElement;
    expect(sel.value).toBe("3");
    expect(screen.getByText("로컬(추정)")).toBeInTheDocument();
    expect(body()).toEqual({
      name: "shop.example.com 점검",
      description: "대상 https://shop.example.com/app",
      goal: "주문 IDOR 확인",
      llm_profile_ids: [3],
      coverage_enabled: true,
      intercept_rules: [
        { enabled: true, action: "block", kind: "fuzzy_url", pattern: "/api/admin/reset", note: "작업대 제외 경로" },
        { enabled: true, action: "block", kind: "fuzzy_url", pattern: "/logout", note: "작업대 제외 경로" },
      ],
    });
    expect(screen.getByText("엔진은 만들자마자 점검을 시작합니다(범위 확인 단계 없음).")).toBeInTheDocument();
    // 일반 ARTEX 작업 생성임을 밝힌다(통제된 URL 진단과 구분)
    expect(document.querySelector("#lvKind")).toHaveTextContent("일반 ARTEX 작업(POST /api/tasks)");

    const tasksBefore = m.fetchTasks.mock.calls.length;
    await act(async () => {
      fireEvent.click(starts()[1]);
    });
    expect(m.createTask).toHaveBeenCalledTimes(1);
    const input = m.createTask.mock.calls[0][0];
    expect(input.description).toContain("https://shop.example.com/app");
    expect(input).toMatchObject({
      name: "shop.example.com 점검",
      goal: "주문 IDOR 확인",
      llm_profile_ids: ["3"],
      coverage_enabled: true,
      intercept_rules: api.excludePathRules(["/api/admin/reset", "/logout"]),
    });
    await waitFor(() => expect(goTo).toHaveBeenCalledWith("live"));
    expect(useSource.getState().taskId).toBe("21");
    await waitFor(() => expect(m.fetchTasks.mock.calls.length).toBeGreaterThan(tasksBefore));
  });

  it("보내는 중에는 두 번 보내지 않는다", async () => {
    let release: (t: api.LiveTask) => void = noop;
    m.createTask.mockImplementation(
      () =>
        new Promise<api.LiveTask>((res) => {
          release = res;
        }),
    );
    const { url, starts, type, goTo } = renderNew();
    type(url(), "https://shop.example.com");
    await screen.findByLabelText("LLM 프로필");
    fireEvent.click(starts()[0]);
    fireEvent.click(starts()[1]);
    expect(m.createTask).toHaveBeenCalledTimes(1);
    for (const b of starts()) {
      expect(b).toBeDisabled();
      expect(b).toHaveTextContent("만드는 중…");
    }
    await act(async () => {
      release(task({ id: "22", status: "running" }));
    });
    await waitFor(() => expect(goTo).toHaveBeenCalledWith("live"));
    expect(useSource.getState().taskId).toBe("22");
  });

  it("오류는 안내하고 머문다 (400 · 첫 진단 모드 403)", async () => {
    m.createTask.mockRejectedValueOnce(new ApiError(400, "LLM 프로필 없음"));
    const { url, starts, type, goTo } = renderNew();
    type(url(), "https://shop.example.com");
    await act(async () => {
      fireEvent.click(starts()[1]);
    });
    expect((await screen.findAllByText("엔진 오류 (HTTP 400): LLM 프로필 없음")).length).toBeGreaterThan(0);
    expect(goTo).not.toHaveBeenCalled();
    m.createTask.mockRejectedValueOnce(new ApiError(403, "first"));
    await act(async () => {
      fireEvent.click(starts()[1]);
    });
    expect((await screen.findAllByText("새 점검 만들기: 이 서버 모드에서는 제공되지 않음")).length).toBeGreaterThan(0);
    expect(useSource.getState().taskId).toBeNull();
  });

  it("엔진에 없는 칸은 한 묶음으로 막혀 있다", () => {
    const { container } = renderNew();
    const box = container.querySelector("#lvUnsupported") as HTMLElement;
    expect(within(box).getAllByText("엔진 미지원")).toHaveLength(5);
    for (const name of [
      "소유 확인 (DNS TXT)",
      "로그인 계정 · 세션",
      "전송 범위 (데이터를 바꾸는 요청)",
      "점검 깊이",
      "워커 수",
    ])
      expect(within(box).getByText(name, { exact: false })).toBeInTheDocument();
    const controls = box.querySelectorAll("input, select, button");
    expect(controls.length).toBeGreaterThan(5);
    for (const c of controls) expect(c).toBeDisabled();
    // 시안의 계정·세션 입력칸은 없다
    expect(screen.queryByLabelText("계정 A 비밀번호")).toBeNull();
    expect(screen.queryByText(/TXT 레코드를 도메인 DNS에 추가한 뒤/)).toBeNull();
  });

  it("제외 경로 안내는 요청 차단을 약속하지 않는다", () => {
    renderNew();
    const help = screen.getByText(/개별 HTTP 요청을 막는 장치는 아닙니다/);
    expect(help).toHaveTextContent("점검 대상(의도·자산)으로 올리지 않습니다");
    expect(screen.queryByText(/요청하지 않습니다/)).toBeNull();
  });

  it("프로필이 미지원(403)이면 접고 프로필 없이 보낸다", async () => {
    m.fetchLlmProfiles.mockRejectedValue(new ApiError(403, "first"));
    const { url, starts, type, body } = renderNew();
    expect(await screen.findByText("LLM 프로필: 이 서버 모드에서는 제공되지 않음")).toBeInTheDocument();
    type(url(), "http://localhost:3000");
    expect(screen.getByText("내부 주소")).toBeInTheDocument();
    expect(body()).not.toHaveProperty("llm_profile_ids");
    await act(async () => {
      fireEvent.click(starts()[1]);
    });
    expect(m.createTask.mock.calls[0][0]).toMatchObject({
      description: "대상 http://localhost:3000",
      llm_profile_ids: [],
    });
  });

  it("외부 프로필을 고르면 외부(추정) 경고, '지정 안 함' 은 프로필을 보내지 않는다", async () => {
    const { body } = renderNew();
    const sel = (await screen.findByLabelText("LLM 프로필")) as HTMLSelectElement;
    fireEvent.change(sel, { target: { value: "2" } });
    expect(screen.getByText("외부(추정)")).toBeInTheDocument();
    expect(screen.getByText(/외부 제공자로 전송될 수 있습니다/)).toBeInTheDocument();
    expect(body().llm_profile_ids).toEqual([2]);
    fireEvent.change(sel, { target: { value: "" } });
    expect(body()).not.toHaveProperty("llm_profile_ids");
    expect(screen.getByText(/엔진의 활성 LLM 설정으로 점검합니다/)).toBeInTheDocument();
  });

  it("막는 까닭: 주소 · 이름 · 계정 정보", () => {
    const { url, name, starts, type } = renderNew();
    expect(screen.getAllByText("점검할 사이트 주소를 입력하세요.").length).toBeGreaterThan(0);
    type(url(), "https://u:pw@shop.example.com");
    expect(starts()[0]).toBeDisabled();
    expect(screen.getAllByText(/계정 정보\(user:pass@\)/).length).toBeGreaterThan(0);
    type(url(), "https://shop.example.com");
    expect(starts()[0]).toBeEnabled();
    type(name(), " ");
    expect(starts()[0]).toBeDisabled();
    expect(screen.getAllByText("작업 이름을 입력하세요").length).toBeGreaterThan(0);
    // 이름을 고친 뒤에는 주소를 바꿔도 이름이 따라 바뀌지 않는다
    type(name(), "내 점검");
    type(url(), "https://other.example.com");
    expect(name().value).toBe("내 점검");
  });

  it("최근 점검한 주소 칩 · 다른 화면의 '이 주소로 다시 점검'", async () => {
    const { url, name } = renderNew();
    const chip = await screen.findByRole("button", { name: /blog\.example\.com/ });
    expect(screen.getAllByRole("button", { name: /\.example\.com/ })).toHaveLength(2);
    fireEvent.click(chip);
    expect(url().value).toBe("https://blog.example.com");
    expect(name().value).toBe("blog.example.com 점검");
    act(() => useWb.getState().nPrefill("https://docs.example.com"));
    expect(url().value).toBe("https://docs.example.com");
    expect(name().value).toBe("docs.example.com 점검");
  });
});
