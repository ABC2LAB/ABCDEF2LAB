// 범위 확인 화면 + API 상세: 개수 · 필터 · 검색 · 포함/제외 체크 · 선택 · 탭(화살표 키) · 요청·응답 예시 · 시작 버튼.
// 클래스 이름은 검사하지 않는다.

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { EP } from "../data/scope";
import { useApiTab } from "../lib/apiTab";
import { exFor } from "../lib/exFor";
import { type Shell, ShellContext } from "../shell";
import { createInitialState, useWb } from "../store";
import { ApiDetail } from "../views/ApiDetail";
import { Scope } from "../views/Scope";

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
      <Scope />
    </ShellContext.Provider>,
  );
}

const byId = (c: HTMLElement, id: string) => c.querySelector<HTMLElement>(`#${id}`);
const ep = (m: string, p: string) => {
  const e = EP.find((x) => x.m === m && x.p === p);
  if (!e) throw new Error(`${m} ${p} 없음`);
  return e;
};
const rows = () => screen.getAllByRole("checkbox");
/** 묶음을 모두 펼친다 (처음에는 모두 접혀 있다) */
const expandAll = () => fireEvent.click(screen.getByRole("button", { name: "모두 펼치기" }));
const groupHead = (label: string) => screen.getByRole("button", { name: new RegExp(`^${label}\\d`) });
const filterChip = (label: string) => screen.getByRole("button", { name: new RegExp(`^${label}\\d+$`) });
/** 표의 줄 버튼 (메서드 + 경로 + 기능 + 딱지 글자로 이름이 붙는다) */
const rowButton = (m: string, p: string) => {
  const b = byId(document.body, "sTable")?.querySelector<HTMLElement>(`[data-ssel="${ep(m, p).i}"]`);
  if (!b) throw new Error(`${m} ${p} 줄 없음`);
  return b;
};

beforeEach(() => {
  useWb.setState(createInitialState());
  useApiTab.setState({ tab: "info" });
  window.location.hash = "";
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("범위 확인: 개수와 필터", () => {
  it("처음: 엔드포인트 37 · 포함 33 · 제외 4 · 승인 필요 10, 필터 개수", () => {
    const { container } = renderScope();
    expect(screen.getByRole("heading", { level: 1, name: "범위 확인" })).toHaveAttribute("tabindex", "-1");
    expect(byId(container, "sCount")).toHaveTextContent("엔드포인트37포함33제외4승인 필요10로그인 영역예시");
    expect(filterChip("전체")).toHaveTextContent("전체37");
    expect(filterChip("전체")).toHaveAttribute("aria-pressed", "true");
    expect(filterChip("데이터 변경")).toHaveTextContent("13");
    expect(filterChip("ID 포함")).toHaveTextContent("8");
    expect(filterChip("로그인 필요")).toHaveTextContent("23");
    expect(filterChip("제외됨")).toHaveTextContent("4");
    // 처음에는 묶음이 모두 접혀 있다
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    // 묶음 머리 (원본 SG 순서) — 접혀 있어도 개수·포함·승인 필요 요약이 보인다
    const heads = [...(byId(container, "sTable")?.querySelectorAll("[data-sg]") ?? [])].map((x) => x.textContent);
    expect(heads).toEqual([
      "계정·인증7포함 5 · 승인 필요 2AI 추정 묶음",
      "장바구니·주문7포함 6 · 승인 필요 3AI 추정 묶음",
      "결제·쿠폰3포함 3 · 승인 필요 2AI 추정 묶음",
      "관리자8포함 7 · 승인 필요 2AI 추정 묶음",
      "상품·리뷰6포함 6 · 승인 필요 1AI 추정 묶음",
      "공개 페이지6포함 6AI 추정 묶음",
    ]);
    expandAll();
    expect(rows()).toHaveLength(37);
    expect(screen.getByRole("button", { name: "모두 접기" })).toBeInTheDocument();
  });

  it("묶음 머리를 누르면 그 묶음만 열리고 다시 누르면 닫힌다", () => {
    renderScope();
    const acct = groupHead("계정·인증");
    expect(acct).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(acct);
    expect(acct).toHaveAttribute("aria-expanded", "true");
    expect(rows()).toHaveLength(7);
    expect(groupHead("장바구니·주문")).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(acct);
    expect(acct).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    // 필터를 쓰면 결과가 있는 묶음이 펼쳐지고, '전체'로 돌아오면 다시 접힌다
    fireEvent.click(filterChip("제외됨"));
    expect(rows()).toHaveLength(4);
    fireEvent.click(filterChip("전체"));
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
  });

  it("필터를 누르면 그 조건의 줄만, 누른 필터만 aria-pressed", () => {
    renderScope();
    fireEvent.click(filterChip("제외됨"));
    expect(filterChip("제외됨")).toHaveAttribute("aria-pressed", "true");
    expect(filterChip("전체")).toHaveAttribute("aria-pressed", "false");
    expect(rows().map((r) => r.getAttribute("aria-label"))).toEqual([
      "GET /api/logout 점검에 포함",
      "POST /api/password/reset 점검에 포함",
      "DELETE /api/cart/{id} 점검에 포함",
      "DELETE /admin/delete-user/{id} 점검에 포함",
    ]);
    fireEvent.click(filterChip("ID 포함"));
    expect(rows()).toHaveLength(8);
  });

  it("검색은 앞뒤 공백을 빼고 대소문자 없이 메서드·경로·기능에서 찾는다", () => {
    const { container } = renderScope();
    const q = screen.getByRole("searchbox", { name: "경로 검색" });
    fireEvent.change(q, { target: { value: "  COUPON " } });
    expect(rows().map((r) => r.getAttribute("aria-label"))).toEqual([
      "GET /api/coupons 점검에 포함",
      "POST /api/coupons/apply 점검에 포함",
    ]);
    expect(byId(container, "sTable")).toHaveTextContent(/^결제·쿠폰2포함 2/);
    // 기능 이름으로도
    fireEvent.change(q, { target: { value: "장바구니" } });
    expect(rows()).toHaveLength(3);
    // 필터와 함께
    fireEvent.click(filterChip("데이터 변경"));
    expect(rows().map((r) => r.getAttribute("aria-label"))).toEqual([
      "POST /api/cart/add 점검에 포함",
      "DELETE /api/cart/{id} 점검에 포함",
    ]);
    fireEvent.change(q, { target: { value: "없는경로" } });
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    expect(screen.getByText("조건에 맞는 경로가 없습니다.")).toBeInTheDocument();
  });
});

describe("범위 확인: 포함 여부와 선택", () => {
  it("기본 제외를 체크하면 포함·제외·승인 필요 수가 바뀌고 그 줄이 선택된다", () => {
    const { container } = renderScope();
    expandAll();
    const logout = screen.getByRole("checkbox", { name: "GET /api/logout 점검에 포함" });
    expect(logout).not.toBeChecked();
    fireEvent.click(logout);
    expect(logout).toBeChecked();
    expect(useWb.getState().scopeOn[ep("GET", "/api/logout").i]).toBe(true);
    expect(byId(container, "sCount")).toHaveTextContent("포함34제외3승인 필요10");
    expect(filterChip("제외됨")).toHaveTextContent("3");
    // 보낼 때 승인이 필요한 변경 요청을 넣으면 승인 필요도 는다
    const reset = screen.getByRole("checkbox", { name: "POST /api/password/reset 점검에 포함" });
    fireEvent.click(reset);
    expect(byId(container, "sCount")).toHaveTextContent("포함35제외2승인 필요11");
    // 체크한 줄이 선택되고, 같은 체크 상자에 포커스가 남는다(원본: 다시 그린 뒤 포커스)
    reset.focus();
    fireEvent.click(reset);
    expect(document.activeElement).toBe(reset);
    expect(rowButton("POST", "/api/password/reset")).toHaveAttribute("aria-current", "true");
    const insp = byId(container, "sInsp") as HTMLElement;
    expect(within(insp).getByRole("heading", { level: 2 })).toHaveTextContent("POST /api/password/reset");
    expect(insp).toHaveTextContent(
      "보내면 실제 메일이 발송됩니다. 그래서 기본으로 제외했습니다. 범위 확인에서 체크하면 포함하고, 보낼 때 승인을 받습니다.",
    );
    expect(within(insp).getByText("제외 · 메일 발송")).toBeInTheDocument();
  });

  it("직접 제외하면 '제외' 딱지와 설명, 전역 제외는 체크 상자가 막혀 있다", () => {
    const { container } = renderScope();
    expandAll();
    fireEvent.click(screen.getByRole("checkbox", { name: "GET /products 점검에 포함" }));
    const insp = byId(container, "sInsp") as HTMLElement;
    expect(within(insp).getByRole("heading", { level: 2 })).toHaveTextContent("GET /products");
    expect(insp).toHaveTextContent("직접 제외했습니다. 체크하면 다시 포함합니다.");
    expect(within(rowButton("GET", "/products")).getByText("제외")).toBeInTheDocument();
    const lock = screen.getByRole("checkbox", { name: "DELETE /admin/delete-user/{id} 점검에 포함" });
    expect(lock).toBeDisabled();
    expect(lock).not.toBeChecked();
  });

  it("줄을 누르면 선택이 옮겨 가고 상세 서랍을 연다", () => {
    const openInsp = vi.fn();
    const { container } = renderScope({ openInsp });
    // 처음 선택은 GET /api/orders/{id}
    expect(rowButton("GET", "/api/orders/{id}")).toHaveAttribute("aria-current", "true");
    const insp = byId(container, "sInsp") as HTMLElement;
    expect(within(insp).getByRole("heading", { level: 2 })).toHaveTextContent("GET /api/orders/{id}");
    fireEvent.click(rowButton("POST", "/api/checkout"));
    expect(openInsp).toHaveBeenCalledTimes(1);
    expect(rowButton("POST", "/api/checkout")).toHaveAttribute("aria-current", "true");
    expect(rowButton("GET", "/api/orders/{id}")).not.toHaveAttribute("aria-current");
    expect(within(insp).getByRole("heading", { level: 2 })).toHaveTextContent("POST /api/checkout");
    expect(within(insp).getByText("찾은 경로 · 결제·쿠폰")).toBeInTheDocument();
    expect(within(insp).getByText("포함 · 보낼 때 승인")).toBeInTheDocument();
    expect(within(insp).getByText("로그인 필요")).toBeInTheDocument();
    expect(insp).toHaveTextContent("응답200찾은 곳/checkout 화면요청 횟수1회근거요청 본문에 금액·결제 수단");
    expect(insp).toHaveTextContent("데이터를 바꾸는 요청이라 범위에 넣어도 보낼 때마다 승인을 받습니다.");
  });

  it("'선택한 범위로 점검 시작': 레일 새 점검 줄을 숨기고 진행 화면을 처음부터", () => {
    useWb.setState({ railNew: "scope" });
    renderScope();
    const start = screen.getByRole("link", { name: "선택한 범위로 점검 시작" });
    expect(start).toHaveAttribute("href", "#live");
    fireEvent.click(start);
    expect(useWb.getState().liveFresh).toBe(true);
    expect(useWb.getState().railNew).toBeNull();
  });
});

describe("API 상세", () => {
  it("탭: 클릭 · 좌우 화살표로 옮기고(끝에서 돌아감) 포커스, 고른 탭만 tabindex 0", () => {
    const { container } = renderScope();
    const info = screen.getByRole("tab", { name: "기본 정보" });
    const ex = screen.getByRole("tab", { name: "요청·응답 예시" });
    expect(info).toHaveAttribute("aria-selected", "true");
    expect(ex).toHaveAttribute("aria-selected", "false");
    expect(ex).toHaveAttribute("tabindex", "-1");
    expect(byId(container, "s-p-info")).toBeVisible();
    expect(byId(container, "s-p-ex")).not.toBeVisible();
    info.focus();
    fireEvent.keyDown(info, { key: "ArrowRight" });
    expect(ex).toHaveAttribute("aria-selected", "true");
    expect(info).toHaveAttribute("tabindex", "-1");
    expect(document.activeElement).toBe(ex);
    expect(byId(container, "s-p-ex")).toBeVisible();
    expect(byId(container, "s-p-info")).not.toBeVisible();
    fireEvent.keyDown(ex, { key: "ArrowRight" });
    expect(document.activeElement).toBe(info);
    expect(info).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(info, { key: "ArrowLeft" });
    expect(document.activeElement).toBe(ex);
    // 다른 키는 무시
    fireEvent.keyDown(ex, { key: "ArrowDown" });
    expect(ex).toHaveAttribute("aria-selected", "true");
    fireEvent.click(info);
    expect(byId(container, "s-p-info")).toBeVisible();
    expect(info).toHaveAttribute("aria-controls", "s-p-info");
    expect(byId(container, "s-p-info")).toHaveAttribute("aria-labelledby", "s-t-info");
  });

  it("고른 탭은 두 화면의 API 상세가 함께 쓴다 (원본 전역 apiTab)", () => {
    render(
      <>
        <ApiDetail e={EP[0]} px="s" />
        <ApiDetail e={EP[1]} px="c" />
      </>,
    );
    fireEvent.click(screen.getAllByRole("tab", { name: "요청·응답 예시" })[1]);
    for (const t of screen.getAllByRole("tab", { name: "요청·응답 예시" })) {
      expect(t).toHaveAttribute("aria-selected", "true");
    }
  });

  it("요청·응답 예시: 관찰한 조회 요청은 실제 모양 그대로 (자리표시자 포함)", () => {
    const { container } = renderScope();
    fireEvent.click(screen.getByRole("tab", { name: "요청·응답 예시" }));
    const panel = byId(container, "s-p-ex") as HTMLElement;
    expect(panel).toHaveTextContent(
      "탐색에서 실제로 주고받은 요청과 응답입니다. 세션, 토큰, 개인정보는 <자리표시자>로 바꿔 저장했습니다.",
    );
    expect(within(panel).getAllByText("예시 · 관찰")).toHaveLength(2);
    expect(byId(container, "s-req")?.textContent).toBe(
      "GET /api/orders/1012 HTTP/1.1\nHost: shop.example.com\nAccept: application/json\nCookie: session=<계정 A 세션>",
    );
    expect(byId(container, "s-res")?.textContent).toBe(
      'HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\n  "order_id": 1012,\n  "status": "배송 중",\n  "items": 2,\n  "amount": "<금액>",\n  "address": "<배송지>"\n}',
    );
  });

  it("응답 본문의 HTML(<script> 포함)은 글자로만 보인다", () => {
    const { container } = renderScope();
    fireEvent.click(rowButton("GET", "/products/{id}"));
    fireEvent.click(screen.getByRole("tab", { name: "요청·응답 예시" }));
    const res = byId(container, "s-res") as HTMLElement;
    expect(res.textContent).toContain('<script src="/static/js/app.3f2a.js"></script>');
    expect(res.querySelector("script")).toBeNull();
    expect(byId(container, "s-req")).toHaveTextContent("GET /products/12 HTTP/1.1");
    expect(byId(container, "s-req")).toHaveTextContent("Accept: text/html");
  });

  it("보내지 않은 변경 요청 · 기본 제외 · 전역 제외의 안내와 출처", () => {
    const { container } = renderScope();
    fireEvent.click(screen.getByRole("tab", { name: "요청·응답 예시" }));
    const panel = byId(container, "s-p-ex") as HTMLElement;
    fireEvent.click(rowButton("PATCH", "/api/orders/{id}"));
    expect(panel).toHaveTextContent("아직 보내지 않은 요청입니다.");
    expect(within(panel).getByText("예시 · 찾은 모양")).toBeInTheDocument();
    expect(within(panel).getByText("예시 · AI 추정")).toBeInTheDocument();
    expect(byId(container, "s-req")?.textContent).toBe(
      'PATCH /api/orders/1012 HTTP/1.1\nHost: shop.example.com\nAccept: application/json\nCookie: session=<계정 A 세션>\nContent-Type: application/json\n\n{\n  "status": "cancelled"\n}',
    );
    fireEvent.click(rowButton("GET", "/api/logout"));
    expect(panel).toHaveTextContent("기본으로 제외한 경로라 보내지 않았습니다(세션이 끊김).");
    // 302: 본문·Content-Type 없이 헤더만
    expect(byId(container, "s-res")?.textContent).toBe(
      "HTTP/1.1 302 Found\nLocation: /\nSet-Cookie: session=; Max-Age=0",
    );
    fireEvent.click(rowButton("DELETE", "/admin/delete-user/{id}"));
    expect(panel).toHaveTextContent("전역 제외 규칙 /admin/delete*에 걸려 보내지 않는 경로입니다.");
    expect(within(panel).getByText("/admin/delete*").tagName).toBe("CODE");
    expect(byId(container, "s-res")).toBeNull();
    expect(within(panel).getByText("없음")).toBeInTheDocument();
    expect(within(panel).getByText("보내지 않는 요청이라 응답이 없습니다.")).toBeInTheDocument();
  });

  it("exFor: 질의 문자열 · 받는 형식 · {host} 바꾸기 · 로그인 응답 헤더", () => {
    const host = "test.example.com";
    expect(exFor(ep("GET", "/search"), host).req.split("\n")[0]).toBe("GET /search?q=test HTTP/1.1");
    expect(exFor(ep("GET", "/api/reviews"), host).req.split("\n")[0]).toBe("GET /api/reviews?product=12 HTTP/1.1");
    expect(exFor(ep("GET", "/robots.txt"), host).req).toContain("Accept: */*");
    expect(exFor(ep("GET", "/health"), host).req).toContain("Accept: application/json");
    expect(exFor(ep("GET", "/sitemap.xml"), host).res).toBe(
      "HTTP/1.1 200 OK\nContent-Type: application/xml\n\n<urlset>\n  <url><loc>https://test.example.com/products</loc></url>\n  <url><loc>https://test.example.com/categories</loc></url>\n  <!-- 경로 21개 -->\n</urlset>",
    );
    const login = exFor(ep("POST", "/api/login"), host);
    expect(login.sent).toBe(true);
    expect(login.res).toBe(
      'HTTP/1.1 200 OK\nContent-Type: application/json\nSet-Cookie: session=<세션>; Path=/\n\n{\n  "ok": true,\n  "user_id": 1024\n}',
    );
    expect(exFor(ep("DELETE", "/api/cart/{id}"), host).res).toBe("HTTP/1.1 204 No Content");
    expect(exFor(ep("GET", "/admin"), host).res).toBe(
      "HTTP/1.1 403 Forbidden\nContent-Type: text/html; charset=utf-8\n\n<h1>403 Forbidden</h1>",
    );
  });
});
