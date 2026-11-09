// 레일 live 모드: 점검 목록(tasks) · 승인 대기 배지 · 분석 모델(로컬 추정) · 누르면 고르고 이동. 팀 api 를 모의한다.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/abc2lab-ui/lib/api", async (importOriginal) => {
  const real = await importOriginal<typeof import("@/abc2lab-ui/lib/api")>();
  return {
    ...real,
    apiFetch: vi.fn(),
    readAuthStatus: vi.fn(async () => ({ initialized: true })),
    restoreSession: vi.fn(async () => true),
    subscribeAuthRequired: vi.fn(() => () => undefined),
  };
});

import * as teamApi from "@/abc2lab-ui/lib/api";
import { ApiError } from "@/abc2lab-ui/lib/api";

import { setLiveAuthState } from "../live/auth";
import { resetFeeds } from "../live/feeds";
import { useSource } from "../live/source";
import { createInitialState, useWb } from "../store";
import { Workbench } from "../Workbench";

const apiFetch = vi.mocked(teamApi.apiFetch);
const NOW = Math.floor(Date.now() / 1000);

const TASKS = {
  active: "11",
  tasks: [
    {
      id: "11",
      name: "쇼핑몰 점검",
      description: "대상 https://shop.example.com/ 접근 통제",
      goal: "IDOR",
      status: "running",
      created_unix: NOW - 600,
      goals_total: 5,
      goals_met: 2,
      findings: { critical: 0, high: 1, medium: 0, low: 0 },
    },
    {
      id: "12",
      name: "",
      description: "https://api.store-dev.com",
      goal: "",
      status: "paused",
      created_unix: NOW - 300,
      goals_total: 0,
      goals_met: 0,
    },
    {
      id: "8",
      name: "블로그",
      description: "https://blog.example.com",
      goal: "",
      status: "done",
      created_unix: NOW - 90_000,
      completed_unix: NOW - 86_000,
      findings: { critical: 1, high: 1, medium: 1, low: 0 },
    },
    {
      id: "7",
      name: "",
      description: "설명만 있는 점검",
      goal: "",
      status: "failed",
      created_unix: NOW - 200_000,
      completed_unix: NOW - 199_000,
      findings: { critical: 0, high: 0, medium: 0, low: 0 },
    },
  ],
};

type Routes = Record<string, unknown>;
function serve(extra: Routes = {}) {
  const table: Routes = {
    "/api/tasks": TASKS,
    "/api/intercept/pending": {
      pending: [
        { id: 1, status: "pending", tool_name: "http_request", task_id: "11" },
        { id: 2, status: "pending", tool_name: "http_request", task_id: "12" },
      ],
    },
    "/api/llm": {
      configured: true,
      provider: "openai",
      model: "qwen/qwen3.6-35b-a3b",
      base_url: "http://127.0.0.1:1234/v1",
    },
    ...extra,
  };
  apiFetch.mockImplementation(async (path: string) => {
    const v = table[path];
    if (v instanceof Error) throw v;
    if (v === undefined) throw new ApiError(404, path);
    return v as never;
  });
}

const rail = () => screen.getByRole("complementary", { name: "주 메뉴" });

beforeEach(() => {
  apiFetch.mockReset();
  localStorage.clear();
  useSource.setState({ source: "live", taskId: null });
  setLiveAuthState("checking");
  resetFeeds();
  useWb.setState(createInitialState());
  window.location.hash = "";
  vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("레일 live 모드", () => {
  it("진행 중 · 최근 끝난 점검 · 배지 · 분석 모델", async () => {
    serve();
    render(<Workbench />);
    const r = rail();
    const shop = await within(r).findByRole("button", { name: /shop\.example\.com/ });
    expect(shop).toHaveTextContent("점검 중 · 목표 2/5");
    expect(within(r).getByRole("button", { name: /api\.store-dev\.com/ })).toHaveTextContent("일시정지됨");
    expect(within(r).getByRole("button", { name: /blog\.example\.com/ })).toHaveTextContent("발견 3");
    // 실패한 점검은 상태를 앞에, 호스트가 없으면 설명
    expect(within(r).getByRole("button", { name: /설명만 있는 점검/ })).toHaveTextContent(/실패 · .* · 발견 없음/);
    await waitFor(() => expect(r.querySelector("#railBadge")).toHaveTextContent("2"));
    expect(r.querySelector("#railBadge")).not.toHaveAttribute("hidden");
    await waitFor(() => expect(r.querySelector("#railModel")).toHaveTextContent("분석 모델 · 로컬(추정)"));
    expect(r.querySelector("#railModel")).toHaveTextContent("qwen/qwen3.6-35b-a3b");
    // 시안 줄은 없다
    expect(r.querySelector("#railShopSub")).toBeNull();
    expect(within(r).queryByText("완전 로컬 모드")).toBeNull();
  });

  it("진행 중을 누르면 고르고 점검 진행으로, 끝난 것은 보고서로", async () => {
    serve();
    render(<Workbench />);
    const r = rail();
    fireEvent.click(await within(r).findByRole("button", { name: /shop\.example\.com/ }));
    expect(useSource.getState().taskId).toBe("11");
    expect(window.location.hash).toBe("#live");
    await waitFor(() =>
      expect(within(r).getByRole("button", { name: /shop\.example\.com/ })).toHaveAttribute("aria-current", "page"),
    );
    fireEvent.click(within(r).getByRole("button", { name: /blog\.example\.com/ }));
    expect(useSource.getState().taskId).toBe("8");
    expect(window.location.hash).toBe("#report");
  });

  it("승인 대기가 미지원(첫 진단 모드 403)이면 배지를 숨기고, 모델 줄은 확인 불가", async () => {
    serve({ "/api/intercept/pending": new ApiError(403, "first"), "/api/llm": new ApiError(403, "first") });
    render(<Workbench />);
    const r = rail();
    await within(r).findByRole("button", { name: /shop\.example\.com/ });
    await waitFor(() => expect(r.querySelector("#railModel")).toHaveTextContent("분석 모델 · 확인 불가"));
    expect(r.querySelector("#railBadge")).toHaveAttribute("hidden");
  });

  it("외부 모델은 외부(추정), 미설정은 미설정", async () => {
    serve({ "/api/llm": { configured: true, model: "gpt-x", base_url: "https://api.openai.com/v1" } });
    render(<Workbench />);
    await waitFor(() => expect(rail().querySelector("#railModel")).toHaveTextContent("분석 모델 · 외부(추정)gpt-x"));
  });

  it("목록이 비면 빈 안내, 로그인 전에는 로그인 필요", async () => {
    serve({ "/api/tasks": { active: "", tasks: [] } });
    render(<Workbench />);
    expect(await within(rail()).findByText("진행 중인 점검 없음")).toBeInTheDocument();
    expect(within(rail()).getByText("끝난 점검 없음")).toBeInTheDocument();
    cleanup();

    vi.mocked(teamApi.restoreSession).mockResolvedValueOnce(false);
    setLiveAuthState("checking");
    resetFeeds();
    render(<Workbench />);
    expect(await within(rail()).findByText("로그인 필요")).toBeInTheDocument();
  });

  it("demo 로 바꾸면 시안 레일 그대로", async () => {
    serve();
    useSource.setState({ source: "demo" });
    const { container } = render(<Workbench />);
    expect(container.querySelector("#railShopSub")).toHaveTextContent("점검 중 · 58%");
    expect(container.querySelector("#railBadge")).toHaveTextContent("2");
    expect(within(rail()).getByText("완전 로컬 모드", { exact: false })).toBeInTheDocument();
    expect(apiFetch).not.toHaveBeenCalled();
  });
});
