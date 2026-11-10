// 자산 탐색 화면: 진행 상태 표시 · 일시정지 · 범위 확인 · 탐색 기록 · 찾은 경로(묶음별·주소 구조·거르기) · API 상세 열고 닫기.
// 재생은 엔진 대신 crawlStep 을 직접 불러 민다. 클래스 이름은 검사하지 않는다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { EV } from "../data/crawl";
import { useApiTab } from "../lib/apiTab";
import { type Shell, ShellContext } from "../shell";
import { createInitialState, useWb } from "../store";
import { Crawl } from "../views/Crawl";

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
      <Crawl />
    </ShellContext.Provider>,
  );
}

/** 자산 탐색 진입(재시작) 뒤 n 단계 재생. n 을 빼면 끝까지. */
function play(n = Number.POSITIVE_INFINITY) {
  act(() => {
    useWb.getState().crawlEnter();
    for (let k = 0; k < n && !useWb.getState().crawl.done; k++) useWb.getState().crawlStep({ rowFresh: false });
  });
}

const byId = (c: HTMLElement, id: string) => c.querySelector<HTMLElement>(`#${id}`);
const tree = (c: HTMLElement) => byId(c, "cTree") as HTMLElement;
const pathButtons = (c: HTMLElement) => within(tree(c)).queryAllByRole("button", { name: /요청·응답 예시 보기$/ });

beforeEach(() => {
  useWb.setState(createInitialState());
  useApiTab.setState({ tab: "info" });
  window.location.hash = "";
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("자산 탐색: 진행 상태", () => {
  it("시작 직후: 탐색 중 · 대상 연결 확인 중 · 범위 확인은 막힘 · 찾은 경로 없음", () => {
    play(0);
    const { container } = renderCrawl();
    expect(screen.getByRole("heading", { level: 1, name: "자산 탐색" })).toHaveAttribute("tabindex", "-1");
    expect(byId(container, "cState")).toHaveTextContent("탐색 중");
    expect(byId(container, "cNow")).toHaveTextContent("대상 연결 확인 중");
    expect(screen.getByRole("link", { name: "범위 확인" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("button", { name: "일시정지" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("progressbar", { name: "탐색 진행" })).toHaveAttribute("aria-valuenow", "0");
    expect(tree(container)).toHaveTextContent("아직 찾은 경로가 없습니다.");
    expect(screen.getByText("shop.example.com")).toBeInTheDocument();
  });

  it("일시정지하면 상태와 버튼이 바뀌고, 다시 시작하면 돌아온다", () => {
    play(3);
    const { container } = renderCrawl();
    fireEvent.click(screen.getByRole("button", { name: "일시정지" }));
    expect(byId(container, "cState")).toHaveTextContent("일시정지됨");
    const resume = screen.getByRole("button", { name: "다시 시작" });
    expect(resume).toHaveAttribute("aria-pressed", "true");
    // 멈춘 동안에는 단계가 진행되지 않는다
    act(() => useWb.getState().crawlStep());
    expect(useWb.getState().crawl.i).toBe(3);
    fireEvent.click(resume);
    expect(byId(container, "cState")).toHaveTextContent("탐색 중");
    expect(screen.getByRole("button", { name: "일시정지" })).toHaveAttribute("aria-pressed", "false");
  });

  it("두 단계 뒤: 지금 보는 곳 · 방문 숫자 · 아직 찾지 못한 묶음", () => {
    play(2);
    const { container } = renderCrawl();
    expect(byId(container, "cNow")).toHaveTextContent("GET /");
    expect(byId(container, "cVis")).toHaveTextContent("1");
    expect(byId(container, "cKnown")).toHaveTextContent("14");
    expect(byId(container, "cQueue")).toHaveTextContent("13");
    expect(byId(container, "tCount")).toHaveTextContent("6");
    expect(screen.getByText("아직 찾지 못한 묶음 · 계정·인증, 장바구니·주문, 결제·쿠폰, 관리자")).toBeInTheDocument();
  });

  it("끝까지: 찾은 경로 37 · 보낸 요청 40 · 요청 안 함 3 · 승인 후 전송 10, 범위 확인이 열린다", () => {
    play();
    const { container } = renderCrawl();
    expect(byId(container, "tCount")).toHaveTextContent("37");
    expect(byId(container, "cFound")).toHaveTextContent("37");
    expect(byId(container, "cFoundSub")).toHaveTextContent("조회 23 · 데이터 변경 14");
    expect(byId(container, "cSent")).toHaveTextContent("40");
    expect(byId(container, "cSkip")).toHaveTextContent("3");
    expect(byId(container, "cAp")).toHaveTextContent("10");
    expect(byId(container, "cStat")).toHaveTextContent("자동 포함 22로그인 1승인 후 전송 10제외 4");
    expect(byId(container, "cState")).toHaveTextContent("탐색 완료 · 확인 대기");
    expect(byId(container, "cNow")).toHaveTextContent("새 경로가 더 나오지 않아 탐색을 마쳤습니다");
    expect(byId(container, "cPause")).not.toBeVisible();
    expect(screen.getByRole("progressbar", { name: "탐색 진행" })).toHaveAttribute("aria-valuenow", "100");
    // 묶음 카드 (원본 GORD 순서, 숫자는 묶음 전체)
    const cards = within(tree(container)).getAllByRole("region");
    expect(cards.map((x) => x.getAttribute("aria-label"))).toEqual([
      "공개 페이지 6개",
      "상품·리뷰 6개",
      "계정·인증 7개",
      "장바구니·주문 7개",
      "결제·쿠폰 3개",
      "관리자 8개",
    ]);
    expect(pathButtons(container)).toHaveLength(37);
    expect(screen.queryByText(/아직 찾지 못한 묶음/)).toBeNull();
    // 범위 확인: 끝났으니 막히지 않고, 누르면 레일 줄이 '범위 확인 대기'로
    const next = screen.getByRole("link", { name: "범위 확인" });
    expect(next).toHaveAttribute("aria-disabled", "false");
    expect(next).toHaveAttribute("href", "#scope");
    fireEvent.click(next);
    expect(useWb.getState().railNew).toBe("scope");
  });

  it("탐색 중에 범위 확인을 눌러도 레일 단계가 바뀌지 않는다", () => {
    play(5);
    renderCrawl();
    fireEvent.click(screen.getByRole("link", { name: "범위 확인" }));
    expect(useWb.getState().railNew).toBe("crawl");
  });

  it("점검 취소: 다음에 처음부터 · 레일 줄 숨김 · 대시보드로", () => {
    play(4);
    const goTo = vi.fn();
    renderCrawl({ goTo });
    fireEvent.click(screen.getByRole("button", { name: "점검 취소" }));
    expect(useWb.getState().crawl.started).toBe(false);
    expect(useWb.getState().crawl.sched).toBeNull();
    expect(useWb.getState().railNew).toBeNull();
    expect(goTo).toHaveBeenCalledWith("dashboard");
  });
});

describe("자산 탐색: 탐색 기록", () => {
  it("각 줄은 시각 · 메서드 · 경로 · 결과 · 설명, 첫 줄은 대상 연결 안내", () => {
    play();
    const { container } = renderCrawl();
    const log = byId(container, "cLog") as HTMLElement;
    const rows = [...log.children].slice(1); // 첫 칸은 머리줄
    expect(rows).toHaveLength(EV.length);
    expect(rows[0]).toHaveTextContent("10:42:05https://shop.example.com 연결 확인 · 탐색 시작");
    expect(rows[1]).toHaveTextContent("10:42:05GET/200첫 화면 · 링크 14개");
    // 기록만 한 변경 요청은 '승인 후 전송', 기본 제외는 '제외 예정', 규칙으로 막은 것은 '요청 안 함'
    expect(rows[8]).toHaveTextContent("POST/api/cart/add승인 후 전송상품 상세의 담기 버튼에서 발견");
    expect(rows[17]).toHaveTextContent("POST/api/password/reset제외 예정");
    expect(rows[22]).toHaveTextContent("GET/api/logout요청 안 함");
    expect(rows[41]).toHaveTextContent("GET/admin403계정 A 접근 거부");
    expect(rows[rows.length - 1]).toHaveTextContent("새 경로가 더 나오지 않아 탐색을 마칩니다");
  });

  it("맨 아래 근처를 보고 있을 때만 새 줄을 따라 내려간다", () => {
    play(1);
    const { container } = renderCrawl();
    const log = byId(container, "cLog") as HTMLElement;
    // jsdom 에는 배치가 없어 크기를 흉내 낸다
    let top = 0;
    Object.defineProperty(log, "scrollHeight", { configurable: true, get: () => 1000 });
    Object.defineProperty(log, "clientHeight", { configurable: true, get: () => 200 });
    Object.defineProperty(log, "scrollTop", {
      configurable: true,
      get: () => top,
      set: (v: number) => {
        top = v;
      },
    });
    act(() => useWb.getState().crawlStep());
    expect(top).toBe(1000);
    // 위로 올려 두면 따라가지 않는다
    top = 100;
    fireEvent.scroll(log);
    act(() => useWb.getState().crawlStep());
    expect(top).toBe(100);
    // 다시 맨 아래 근처(48px 안)로 오면 따라간다
    top = 760;
    fireEvent.scroll(log);
    act(() => useWb.getState().crawlStep());
    expect(top).toBe(1000);
  });
});

describe("자산 탐색: 찾은 경로", () => {
  it("거르기: 승인 필요 10 · 제외 4, 탐색을 다시 하면 전체로", () => {
    play();
    const { container } = renderCrawl();
    fireEvent.click(screen.getByRole("radio", { name: "승인 필요" }));
    expect(useWb.getState().crawl.kf).toBe("ap");
    const ap = pathButtons(container);
    expect(ap).toHaveLength(10);
    for (const b of ap) expect(b.getAttribute("aria-label")).toMatch(/, 승인 후 전송, 요청·응답 예시 보기$/);
    fireEvent.click(screen.getByRole("radio", { name: "제외" }));
    // 묶음 순서(GORD) → 묶음 안에서는 메서드 순서
    expect(pathButtons(container).map((b) => b.getAttribute("aria-label"))).toEqual([
      "GET /api/logout, 제외, 요청·응답 예시 보기",
      "POST /api/password/reset, 제외, 요청·응답 예시 보기",
      "DELETE /api/cart/{id}, 제외, 요청·응답 예시 보기",
      "DELETE /admin/delete-user/{id}, 제외, 요청·응답 예시 보기",
    ]);
    act(() => useWb.getState().crawlReset());
    expect(screen.getByRole("radio", { name: "전체" })).toBeChecked();
  });

  it("아직 해당 경로가 없으면 거르기별 빈 안내", () => {
    play(3);
    const { container } = renderCrawl();
    fireEvent.click(screen.getByRole("radio", { name: "승인 필요" }));
    expect(tree(container)).toHaveTextContent("승인이 필요한 경로가 아직 없습니다.");
    fireEvent.click(screen.getByRole("radio", { name: "제외" }));
    expect(tree(container)).toHaveTextContent("제외한 경로가 아직 없습니다.");
  });

  it("주소 구조: host 가 뿌리, 마디를 접으면 아래 개수, 거르는 중에는 모두 펼침", () => {
    play();
    const { container } = renderCrawl();
    fireEvent.click(screen.getByRole("radio", { name: "주소 구조" }));
    expect(screen.getByRole("radio", { name: "주소 구조" })).toBeChecked();
    expect(within(tree(container)).getByText("shop.example.com")).toBeInTheDocument();
    expect(pathButtons(container)).toHaveLength(37);
    const t = within(tree(container)).getByRole("button", { name: "/api 접기" });
    expect(t).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(t);
    // 같은 버튼이 남아 포커스를 잃지 않는다
    const t2 = within(tree(container)).getByRole("button", { name: "/api 펼치기" });
    expect(t2).toBe(t);
    expect(t2).toHaveAttribute("aria-expanded", "false");
    expect(useWb.getState().crawl.smOpen["/api"]).toBe(false);
    expect(t2.closest("li")).toHaveTextContent(/^\/api19$/);
    expect(pathButtons(container)).toHaveLength(18);
    // 거르는 중에는 접어 둔 마디도 펼쳐 보인다
    fireEvent.click(screen.getByRole("radio", { name: "승인 필요" }));
    expect(within(tree(container)).getByRole("button", { name: "/api 접기" })).toHaveAttribute("aria-expanded", "true");
    // 같은 주소의 메서드는 한 줄에, DELETE 는 DEL
    fireEvent.click(screen.getByRole("radio", { name: "전체" }));
    fireEvent.click(within(tree(container)).getByRole("button", { name: "/api 펼치기" }));
    const cart = within(tree(container)).getByRole("button", {
      name: "DELETE /api/cart/{id}, 제외, 요청·응답 예시 보기",
    });
    expect(cart).toHaveTextContent("DEL");
  });

  it("경로를 누르면 같은 패널에 API 상세, '찾은 경로 목록'으로 돌아오면 그 경로로 포커스", () => {
    play();
    const openInsp = vi.fn();
    const { container } = renderCrawl({ openInsp });
    const pill = screen.getByRole("button", { name: "GET /api/orders/{id}, 요청·응답 예시 보기" });
    fireEvent.click(pill);
    expect(openInsp).toHaveBeenCalledTimes(1);
    const back = screen.getByRole("button", { name: "찾은 경로 목록" });
    expect(document.activeElement).toBe(back);
    expect(byId(container, "cTreeWrap")).not.toBeVisible();
    const api = byId(container, "cApi") as HTMLElement;
    expect(api).toBeVisible();
    expect(within(api).getByRole("heading", { level: 2 })).toHaveTextContent("GET /api/orders/{id}");
    expect(within(api).getByText("찾은 경로 · 장바구니·주문")).toBeInTheDocument();
    expect(byId(container, "c-req")).toHaveTextContent("GET /api/orders/1012 HTTP/1.1");
    fireEvent.click(back);
    expect(byId(container, "cTreeWrap")).toBeVisible();
    expect(api).not.toBeVisible();
    expect(byId(container, "c-req")).toBeNull();
    expect(document.activeElement).toBe(pill);
  });

  it("탐색을 처음부터 다시 하면 열린 API 상세가 닫힌다", () => {
    play();
    const { container } = renderCrawl();
    fireEvent.click(screen.getByRole("button", { name: "POST /api/checkout, 승인 후 전송, 요청·응답 예시 보기" }));
    expect(byId(container, "cApi")).toBeVisible();
    act(() => useWb.getState().crawlReset());
    expect(byId(container, "cApi")).not.toBeVisible();
    expect(byId(container, "cTreeWrap")).toBeVisible();
    expect(tree(container)).toHaveTextContent("아직 찾은 경로가 없습니다.");
  });
});
