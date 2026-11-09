// 대시보드: 결정(승인·새 경로)에 따라 칸·목록·상세가 원본처럼 바뀌는지, 줄을 고르면 상세가 바뀌는지.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { TODO } from "../data/dashboard";
import { apiActive, shopMeta, todoOpen, todoPill } from "../lib/dashboard";
import { liveEnd, liveReset } from "../lib/liveState";
import { type Shell, ShellContext } from "../shell";
import { createInitialState, useWb } from "../store";
import { Dashboard } from "../views/Dashboard";

const noop = () => undefined;

/** 테스트용 껍데기: dashPick 은 원본처럼 dSel 을 바꾸고, goTo 는 기록만. */
function renderDash() {
  const goTo = vi.fn();
  const dashPick = vi.fn((id: Parameters<Shell["dashPick"]>[0]) => useWb.getState().setDSel(id));
  const shell: Shell = {
    route: "dashboard",
    navSeq: 1,
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
    dashPick,
  };
  const r = render(
    <ShellContext.Provider value={shell}>
      <Dashboard />
    </ShellContext.Provider>,
  );
  const insp = () => r.container.querySelector<HTMLElement>("#dInsp") as HTMLElement;
  const row = (id: string) => r.container.querySelector<HTMLElement>(`[data-dsel="${id}"]`) as HTMLElement;
  return { ...r, goTo, dashPick, insp, row };
}

beforeEach(() => {
  useWb.setState(createInitialState());
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("대시보드 계산", () => {
  it("todoPill · todoOpen 은 결정에 따라 바뀐다", () => {
    const none = { appr: null, path: null } as const;
    expect(todoPill("appr", none)).toEqual({ cls: "wait", label: "승인 대기" });
    expect(todoPill("appr", { appr: "ok", path: null })).toEqual({ cls: "ok", label: "승인함" });
    expect(todoPill("appr", { appr: "no", path: null })).toEqual({ cls: "", label: "거부함" });
    expect(todoPill("path", none)).toEqual({ cls: "wait", label: "결정 대기" });
    expect(todoPill("path", { appr: null, path: "in" })).toEqual({ cls: "ok", label: "포함함" });
    expect(todoPill("path", { appr: null, path: "ex" })).toEqual({ cls: "", label: "제외함" });
    expect(todoPill("und", none)).toEqual({ cls: "", label: "결과 확인" });
    expect(todoOpen(TODO, none)).toBe(4);
    expect(todoOpen(TODO, { appr: "no", path: "in" })).toBe(2);
  });

  it("진행 중인 점검 줄의 글", () => {
    const l = liveReset("resume");
    expect(shopMeta(l)).toMatch(/^오늘 10:42 시작 · 약 \d+분 남음 · 발견 \d+$/);
    expect(shopMeta(liveEnd(l))).toMatch(/^오늘 10:42 시작 · 종료됨 · 발견 \d+$/);
    expect(apiActive({ appr: null, path: null })).toEqual({
      pill: { cls: "wait", label: "승인 대기" },
      meta: "승인 대기로 멈춤",
      wait: true,
    });
    expect(apiActive({ appr: "ok", path: null }).meta).toBe("결제 흐름 점검 재개");
    expect(apiActive({ appr: "no", path: null }).meta).toBe("결제 흐름 요청 거부 · 판단 불가로 남김");
  });
});

describe("<Dashboard />", () => {
  it("처음: 결정 대기 2 · 처리할 일 4건 · 승인 요청 상세", () => {
    const { container, insp, row } = renderDash();
    expect(screen.getByRole("heading", { level: 1, name: "대시보드" })).toHaveAttribute("tabindex", "-1");
    expect(container.querySelector("#dDecideN")).toHaveTextContent("2");
    expect(container.querySelector("#dDecideSub")).toHaveTextContent("승인 1 · 새 경로 1");
    expect(container.querySelector("#dTodoN")).toHaveTextContent("4건");
    expect(row("appr")).toHaveAttribute("aria-current", "true");
    expect(row("path")).not.toHaveAttribute("aria-current");
    expect(row("path")).toHaveTextContent("shop.example.com새로 찾은 경로 /admin/export");
    expect(row("act-shop")).toHaveTextContent(/오늘 10:42 시작 · 약 \d+분 남음 · 발견 \d+점검 중58%/);
    expect(row("act-api")).toHaveTextContent("api.store-dev.com오늘 09:15 시작 · 승인 대기로 멈춤승인 대기35%");
    expect(row("done-blog")).toHaveTextContent("blog.example.com10/25 · 28분 · 커버리지 91%심각 1높음 1발견 6");
    const d = within(insp());
    expect(d.getByRole("heading", { level: 2, name: "결제 금액 변경 요청" })).toBeInTheDocument();
    expect(insp().querySelector("pre")?.textContent).toBe(
      'POST /api/checkout HTTP/1.1\nHost: api.store-dev.com\nCookie: session=[REDACTED]\nContent-Type: application/json\n\n{"order_id": 2041, "amount": 1, "method": "card"}',
    );
    expect(d.getByRole("button", { name: "상세 닫기" })).toBeInTheDocument();
  });

  it("조회 시각을 마운트 뒤와 새로고침 때 채운다", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 10, 9, 9, 5));
    const { container } = renderDash();
    expect(container.querySelector("#dSnap")).toHaveTextContent("조회 09:05");
    vi.setSystemTime(new Date(2026, 10, 9, 14, 30));
    fireEvent.click(screen.getByRole("button", { name: "새로고침" }));
    expect(container.querySelector("#dSnap")).toHaveTextContent("조회 14:30");
    vi.useRealTimers();
  });

  it("승인하고 보내기 → 결과 상자, 칸·목록·진행 줄이 바뀐다", () => {
    const { container, insp, row } = renderDash();
    fireEvent.click(within(insp()).getByRole("button", { name: "승인하고 보내기" }));
    expect(useWb.getState().S.appr).toBe("ok");
    expect(insp()).toHaveTextContent("승인했습니다. 이 요청 1건을 보내고 결제 흐름 점검을 이어 갑니다.");
    expect(within(insp()).queryByRole("button", { name: "거부" })).toBeNull();
    expect(container.querySelector("#dDecideN")).toHaveTextContent("1");
    expect(container.querySelector("#dDecideSub")).toHaveTextContent("새 경로 1");
    expect(container.querySelector("#dTodoN")).toHaveTextContent("3건");
    expect(row("appr")).toHaveTextContent("승인함");
    expect(row("act-api")).toHaveTextContent("오늘 09:15 시작 · 결제 흐름 점검 재개점검 중35%");
  });

  it("거부 → 거부 결과", () => {
    const { insp } = renderDash();
    fireEvent.click(within(insp()).getByRole("button", { name: "거부" }));
    expect(useWb.getState().S.appr).toBe("no");
    expect(insp()).toHaveTextContent(
      "거부했습니다. 이 요청은 보내지 않고, 결제 금액 변경 항목은 판단 불가로 남깁니다.",
    );
  });

  it("새 경로 줄을 고르고 제외 → 결정이 스토어에 남고 모두 결정함", () => {
    const { container, insp, row, dashPick } = renderDash();
    fireEvent.click(row("path"));
    expect(dashPick).toHaveBeenCalledWith("path");
    expect(row("path")).toHaveAttribute("aria-current", "true");
    expect(row("appr")).not.toHaveAttribute("aria-current");
    const d = within(insp());
    expect(d.getByRole("heading", { level: 2 })).toHaveTextContent("GET /admin/export");
    expect(insp()).toHaveTextContent("새로 찾은 경로 · shop.example.com");
    expect(insp()).toHaveTextContent("처리 규칙관리자 경로 · 처음 보는 권한 영역은 확인 요청");
    fireEvent.click(d.getByRole("button", { name: "점검에 포함" }));
    expect(useWb.getState().S.path).toBe("in");
    expect(insp()).toHaveTextContent("포함했습니다. 다음 계획 단계에서 세 가지 권한으로 확인합니다.");
    expect(row("path")).toHaveTextContent("포함함");
    act(() => useWb.getState().setAppr("no"));
    expect(container.querySelector("#dDecideN")).toHaveTextContent("0");
    expect(container.querySelector("#dDecideSub")).toHaveTextContent("모두 결정함");
    expect(container.querySelector("#dTodoN")).toHaveTextContent("2건");
  });

  it("판단 불가 상세의 '이 주소로 다시 점검' → 주소 채우기 요청 후 새 점검으로", () => {
    const { insp, row, goTo } = renderDash();
    fireEvent.click(row("done-app"));
    expect(within(insp()).getByRole("heading", { level: 2, name: "판단 불가 2건" })).toBeInTheDocument();
    fireEvent.click(within(insp()).getByRole("button", { name: "이 주소로 다시 점검" }));
    expect(useWb.getState().prefill).toEqual({ url: "https://app.sample.io", seq: 1 });
    expect(goTo).toHaveBeenCalledWith("new");
    fireEvent.click(row("cov"));
    expect(within(insp()).getByRole("heading", { level: 2, name: "미검사 영역 있음" })).toBeInTheDocument();
    fireEvent.click(within(insp()).getByRole("button", { name: "이 주소로 다시 점검" }));
    expect(useWb.getState().prefill).toEqual({ url: "https://docs.example.com", seq: 2 });
  });

  it("진행 중 점검(shop) 상세는 재생 상태를 따른다", () => {
    const { insp, row } = renderDash();
    fireEvent.click(row("act-shop"));
    const d = within(insp());
    expect(d.getByRole("heading", { level: 2, name: "점검 진행 58%" })).toBeInTheDocument();
    expect(insp()).toHaveTextContent("진행 중 · shop.example.com");
    expect(insp()).toHaveTextContent(/시작오늘 10:42남은 시간약 \d+분/);
    expect(d.getByRole("link", { name: "진행 화면 열기" })).toHaveAttribute("href", "#live");
    act(() => useWb.getState().liveEnd());
    expect(insp()).toHaveTextContent("점검을 종료했습니다.");
    expect(insp()).toHaveTextContent("남은 시간—");
    expect(row("act-shop")).toHaveTextContent("종료됨");
  });

  it("api 상세의 '승인 요청 보기' 는 승인 요청을 고르고, 승인 뒤에는 사라진다", () => {
    const { insp, row, dashPick } = renderDash();
    fireEvent.click(row("act-api"));
    expect(insp()).toHaveTextContent("결제 흐름 점검 전 승인 대기 · POST /api/checkout");
    fireEvent.click(within(insp()).getByRole("button", { name: "승인 요청 보기" }));
    expect(dashPick).toHaveBeenLastCalledWith("appr");
    expect(useWb.getState().dSel).toBe("appr");
    act(() => {
      useWb.getState().setAppr("ok");
      useWb.getState().setDSel("act-api");
    });
    expect(insp()).toHaveTextContent("결제 흐름 점검을 이어 가는 중입니다.");
    expect(within(insp()).queryByRole("button", { name: "승인 요청 보기" })).toBeNull();
  });

  it("끝난 blog 상세의 '보고서 열기' 는 보고서 대상을 blog 로 바꾼다", () => {
    const { insp, row } = renderDash();
    fireEvent.click(row("done-blog"));
    const link = within(insp()).getByRole("link", { name: "보고서 열기" });
    expect(link).toHaveAttribute("href", "#report");
    expect(insp()).toHaveTextContent("판정확정 3 · 의심 2 · 판단 불가 1");
    fireEvent.click(link);
    expect(useWb.getState().report).toMatchObject({ kind: "blog", host: "blog.example.com", seq: 1 });
  });
});
