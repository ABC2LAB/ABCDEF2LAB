// 렌더 확인: 노란 배너, 기본 화면(대시보드), 공통 부품의 글자. CSS 모듈 클래스 이름은 검사하지 않는다.

import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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
    expect(screen.getByRole("link", { name: /shop\.example\.com/ })).toHaveAttribute("aria-current", "page");
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
