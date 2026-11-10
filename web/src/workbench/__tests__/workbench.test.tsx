// 렌더 확인: 노란 배너, 기본 화면(대시보드), 공통 부품의 글자. CSS 모듈 클래스 이름은 검사하지 않는다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { railKey } from "../lib/rail";
import { createInitialState, useWb } from "../store";
import { CodeCard, Ep, hlHttp } from "../ui/parts";
import { Workbench } from "../Workbench";

beforeEach(() => {
  useWb.setState(createInitialState());
  window.location.hash = "";
  vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("<Workbench />", () => {
  it("배너를 보이고 기본으로 대시보드만 보인다", () => {
    const { container } = render(<Workbench />);
    expect(
      screen.getByText(
        "작동하지 않는 UI 시안입니다 — 모든 숫자·기록은 예시이며, 실제 점검·소유 확인·세션 저장을 수행하지 않습니다.",
      ),
    ).toBeInTheDocument();
    const views = [...container.querySelectorAll<HTMLElement>("section[data-view]")];
    expect(views.map((v) => v.dataset.view)).toEqual(["dashboard", "new", "crawl", "scope", "live", "report"]);
    expect(views.filter((v) => !v.hidden).map((v) => v.dataset.view)).toEqual(["dashboard"]);
    expect(screen.getByRole("heading", { level: 1, name: "대시보드" })).toBeVisible();
    // 레일: 결정 대기 2, 진행 중 점검 58%, 대시보드 링크가 현재 화면
    expect(container.querySelector("#railBadge")).toHaveTextContent("2");
    expect(container.querySelector("#railShopSub")).toHaveTextContent("점검 중 · 58%");
    expect(screen.getByRole("link", { name: /대시보드/ })).toHaveAttribute("aria-current", "page");
    // 대시보드 처음 고른 항목(승인 요청)은 api.store-dev.com 의 기록 → 그 줄에 빨간 점
    const rail = within(container.querySelector("#rail") as HTMLElement);
    expect(rail.getByRole("button", { name: /api\.store-dev\.com/ })).toHaveAttribute("aria-current", "true");
  });

  it("해시가 바뀌면 그 화면으로 가고 h1 에 포커스, 진행 화면이면 재생을 시작한다", () => {
    const { container } = render(<Workbench />);
    act(() => {
      window.location.hash = "live";
      window.dispatchEvent(new HashChangeEvent("hashchange"));
    });
    const live = container.querySelector<HTMLElement>('section[data-view="live"]');
    expect(live?.hidden).toBe(false);
    expect(document.activeElement).toBe(live?.querySelector("h1"));
    expect(useWb.getState().live.playing).toBe(true);
    expect(screen.getByRole("link", { name: /shop\.example\.com/ })).toHaveAttribute("aria-current", "true");
  });

  it("레일 점검 줄: 상태 색 점은 없고, 오른쪽에 보이는 기록의 줄 하나에만 빨간 점", () => {
    const { container } = render(<Workbench />);
    const marked = () =>
      [...container.querySelectorAll<HTMLElement>('#rail [aria-current="true"]')].map((el) => el.textContent ?? "");
    // 예전 상태 점(id)이 없다
    for (const id of ["railNewDot", "railShopDot", "railApiDot"]) expect(container.querySelector(`#${id}`)).toBeNull();
    expect(marked()).toEqual([expect.stringContaining("api.store-dev.com")]);
    // 대시보드에서 다른 기록을 고르면 점이 그 줄로 옮겨 간다
    const rail = within(container.querySelector("#rail") as HTMLElement);
    fireEvent.click(rail.getByRole("button", { name: /docs\.example\.com/ }));
    expect(useWb.getState().dSel).toBe("done-docs");
    expect(marked()).toEqual([expect.stringContaining("docs.example.com")]);
    fireEvent.click(rail.getByRole("button", { name: /app\.sample\.io/ }));
    expect(marked()).toEqual([expect.stringContaining("app.sample.io")]);
  });
});

describe("railKey (레일 빨간 점 줄)", () => {
  it("화면과 고른 항목으로 줄을 정한다", () => {
    expect(railKey("live", "appr", "blog", false)).toBe("shop");
    expect(railKey("crawl", "appr", "blog", true)).toBe("new");
    expect(railKey("scope", "appr", "blog", false)).toBeNull();
    expect(railKey("report", "appr", "blog", false)).toBe("blog");
    expect(railKey("report", "appr", "live", false)).toBe("shop");
    expect(railKey("dashboard", "appr", "blog", false)).toBe("api");
    expect(railKey("dashboard", "path", "blog", false)).toBe("shop");
    expect(railKey("dashboard", "und", "blog", false)).toBe("app");
    expect(railKey("dashboard", "cov", "blog", false)).toBe("docs");
    expect(railKey("new", "appr", "blog", false)).toBeNull();
  });
});

describe("공통 부품", () => {
  it("Ep 는 {param} 을 i 로 감싼다", () => {
    const { container } = render(<Ep m="GET" p="/api/orders/{id}" />);
    expect(container.textContent).toBe("GET /api/orders/{id}");
    expect(container.querySelector("i")).toHaveTextContent("{id}");
  });

  it("CodeCard 는 글자를 그대로 두고 강조 표시(⟦⟧)만 걷어 내며 줄 번호를 단다", () => {
    const text = 'GET /search?q=test HTTP/1.1\nHost: <호스트>\n\n<h2>검색어: ⟦test⟧</h2>\n{\n  "a": "<값>"\n}';
    const { container } = render(<CodeCard title="요청 예시" src="예시 · 관찰" text={text} id="t-req" />);
    const pre = container.querySelector("#t-req");
    expect(pre?.textContent).toBe(text.replace(/[⟦⟧]/g, ""));
    expect(container.querySelector('pre[aria-hidden="true"]')?.textContent).toBe("1\n2\n3\n4\n5\n6\n7");
    expect(screen.getByRole("button", { name: "복사" })).toBeInTheDocument();
  });

  it("hlHttp 는 줄 사이 개행을 지킨다", () => {
    const { container } = render(<pre>{hlHttp("HTTP/1.1 200 OK\nContent-Type: text/html\n\nbody")}</pre>);
    expect(container.textContent).toBe("HTTP/1.1 200 OK\nContent-Type: text/html\n\nbody");
  });
});
