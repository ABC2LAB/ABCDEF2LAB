import "@testing-library/jest-dom/vitest";
import { StrictMode, useState } from "react";

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CRAWL_EVENTS, crawlPathKind } from "../lib/crawl-demo";
import { NavContext, type ScreenId } from "../nav";
import CrawlScreen from "./CrawlScreen";
import JobScreen from "./JobScreen";
import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";

const target = { url: "https://shop.example.com", host: "shop.example.com" };

/** ABC2LabUI 처럼 화면 전환과 [점검 시작] 횟수를 실제 상태로 들고 있는 틀 */
function Harness({ start }: { start: ScreenId }) {
  const [screenId, setScreen] = useState<ScreenId>(start);
  const [run, setRun] = useState(0);
  return (
    <NavContext.Provider value={{ screen: screenId, go: setScreen }}>
      <JobScreen target={target} onStart={() => setRun((n) => n + 1)} />
      <CrawlScreen target={target} run={run} />
      <output data-testid="where">{screenId}</output>
      <button type="button" onClick={() => setScreen("crawl")}>
        자산 탐색으로 돌아가기
      </button>
      <button type="button" onClick={() => setScreen("dashboard")}>
        다른 화면으로
      </button>
    </NavContext.Provider>
  );
}

/** 가장 긴 줄 대기보다 길게 — 한 번에 한 줄씩 진행된다 */
async function tick(times = 1) {
  for (let n = 0; n < times; n++) {
    await act(async () => {
      vi.advanceTimersByTime(1000);
    });
  }
}
const where = () => screen.getByTestId("where").textContent;
const crawl = () => document.querySelector('[data-screen="crawl"]') as HTMLElement;
const rows = () => crawl().querySelectorAll(".cr-log .cr-row").length;
const tile = (k: string) =>
  [...crawl().querySelectorAll(".cr-tiles .tile")].find((t) => t.querySelector(".k")?.textContent === k) as HTMLElement;

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("자산 탐색 (시안)", () => {
  it("starts from 점검 시작, collects every path, then opens 범위 확인", async () => {
    render(<Harness start="job" />);
    fireEvent.click(screen.getByRole("button", { name: "점검 시작" }));
    expect(where()).toBe("crawl");
    expect(screen.getByRole("heading", { level: 1, name: "자산 탐색" })).toBeVisible();
    expect(screen.getByRole("button", { name: "탐색 중…" })).toBeDisabled();

    await tick(5);
    expect(rows()).toBe(5);
    expect(screen.getByRole("button", { name: "탐색 중…" })).toBeDisabled();

    await tick(CRAWL_EVENTS.length);
    expect(rows()).toBe(CRAWL_EVENTS.length);
    expect(tile("찾은 경로").querySelector(".v")).toHaveTextContent("37");
    expect(tile("찾은 경로").querySelector(".sub")).toHaveTextContent("조회 23 · 데이터 변경 14");
    expect(tile("보낸 요청").querySelector(".v")).toHaveTextContent("40");
    expect(tile("보낸 요청").querySelector(".sub")).toHaveTextContent("로그인 2회 포함");
    expect(tile("요청 안 함").querySelector(".v")).toHaveTextContent("3");
    // 보내기 전에 승인이 필요한 경로 — 범위 확인의 '승인 후 전송 10'과 같다
    expect(tile("승인 후 전송").querySelector(".v")).toHaveTextContent("10");
    expect(tile("승인 후 전송")).toHaveClass("attn-warn");
    expect(crawl().querySelector(".scope-bar .sb-n")).toHaveTextContent(
      "찾은 경로 37 · 승인 후 전송 10 · 로그인 영역 확인됨",
    );
    // 기록: 폼·버튼에서 찾은 경로 10줄 = 승인 후 전송 8 + 제외 예정 2, 규칙에 막힌 링크 3
    expect(crawl().querySelectorAll(".cr-log .dtag.ap")).toHaveLength(8);
    expect(crawl().querySelectorAll(".cr-log .dtag.ex")).toHaveLength(2);
    expect(crawl().querySelectorAll(".cr-log .dtag.out")).toHaveLength(3);
    // 찾은 경로: 승인 표시 10개, 제외(점선) 4개, 묶음 머리에 묶음별 승인 수
    expect(crawl().querySelectorAll(".cr-chip.ap .cr-apb")).toHaveLength(10);
    expect(crawl().querySelectorAll(".cr-chip.off, .cr-chip.lock")).toHaveLength(4);
    const orderGroup = [...crawl().querySelectorAll(".cr-g")].find((g) => g.textContent?.includes("장바구니·주문"));
    expect(orderGroup?.querySelector(".cr-gap")).toHaveTextContent("승인 3");
    expect(screen.getByRole("progressbar", { name: "탐색 진행" })).toHaveAttribute("aria-valuenow", "100");
    expect(screen.getByText("⏸ 탐색 완료 · 확인 대기")).toBeVisible();
    expect(screen.queryByRole("button", { name: "일시정지" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "범위 확인" }));
    expect(where()).toBe("scope");
    // 범위 확인에서 돌아오면 다시 돌지 않고 끝난 모습 그대로
    fireEvent.click(screen.getByRole("button", { name: "자산 탐색으로 돌아가기" }));
    await tick(3);
    expect(rows()).toBe(CRAWL_EVENTS.length);
    expect(screen.getByRole("button", { name: "범위 확인" })).toBeEnabled();
  });

  it("stops adding lines while paused and picks up where it left off", async () => {
    render(<Harness start="job" />);
    fireEvent.click(screen.getByRole("button", { name: "점검 시작" }));
    await tick(8);
    expect(rows()).toBe(8);

    fireEvent.click(screen.getByRole("button", { name: "일시정지" }));
    expect(screen.getByRole("button", { name: "다시 시작" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("일시정지됨")).toBeVisible();
    await tick(5);
    expect(rows()).toBe(8);

    fireEvent.click(screen.getByRole("button", { name: "다시 시작" }));
    await tick(2);
    expect(rows()).toBe(10);
  });

  it("stops when cancelled and starts over on the next 점검 시작", async () => {
    render(<Harness start="job" />);
    fireEvent.click(screen.getByRole("button", { name: "점검 시작" }));
    await tick(6);
    fireEvent.click(screen.getByRole("button", { name: "점검 취소" }));
    expect(where()).toBe("entry");
    await tick(4);
    expect(rows()).toBe(6);

    // 작업 설정에서 다시 시작하면 처음부터
    act(() => {
      (screen.getByRole("button", { hidden: true, name: "점검 시작" }) as HTMLButtonElement).click();
    });
    expect(where()).toBe("crawl");
    expect(rows()).toBe(0);
    await tick(2);
    expect(rows()).toBe(2);
  });

  it("starts by itself when opened directly by address", async () => {
    render(<Harness start="crawl" />);
    await tick(3);
    expect(rows()).toBe(3);
  });

  it("finds exactly the 37 paths that the 범위 확인 mockup lists, with the same approval split", () => {
    // vitest 는 web/ 에서 돈다(저장소 루트에서 돌려도 찾게 둘 다 본다)
    const rel = "src/abc2lab-ui/screens/ScopeScreen.tsx";
    const file = [resolve(process.cwd(), rel), resolve(process.cwd(), "web", rel)].find((f) => existsSync(f)) ?? rel;
    const src = readFileSync(file, "utf8");
    const block = src.slice(src.indexOf("const EP: Endpoint[] = ["));
    const list = block.slice(0, block.indexOf("\n];"));
    const scope = [...list.matchAll(/g: "(\w+)",\s*m: "(\w+)",\s*p: "([^"]+)"/g)].map(
      ([, g, m, p]) => `${m} ${p} ${g}`,
    );
    const found = CRAWL_EVENTS.flatMap((e) => e.found.map((f) => `${f.m} ${f.p} ${f.g}`));
    expect(scope).toHaveLength(37);
    expect(new Set(found).size).toBe(found.length);
    expect([...found].sort()).toEqual([...scope].sort());

    // 범위 확인의 '승인 후 전송' = GET 이 아니고 로그인 · 제외(off) · 전역 제외(lock)가 아닌 경로
    const scopeApprovals = list
      .split(/\n {2}\{/)
      .slice(1)
      .filter((chunk) => !/m: "GET"/.test(chunk) && !/login: true|off: "|lock: "/.test(chunk))
      .map((chunk) => {
        const [, m, p] = chunk.match(/m: "(\w+)",\s*p: "([^"]+)"/) ?? [];
        return `${m} ${p}`;
      });
    const crawlApprovals = CRAWL_EVENTS.flatMap((e) => e.found)
      .filter((f) => crawlPathKind(f.m, f.p) === "ap")
      .map((f) => `${f.m} ${f.p}`);
    expect(scopeApprovals).toHaveLength(10);
    expect([...crawlApprovals].sort()).toEqual([...scopeApprovals].sort());
  });

  it("slides each new line open while the screen is shown, but not while it is hidden", async () => {
    // jsdom 에는 Web Animations 가 없어서 펼치기 호출만 지켜본다
    const animate = vi.fn(() => ({ onfinish: null, oncancel: null }));
    Object.defineProperty(Element.prototype, "animate", { value: animate, configurable: true, writable: true });
    const opens = () =>
      animate.mock.calls.filter(
        (call) => (call as unknown[])[0] && ((call as unknown[])[0] as Keyframe[])[0].height === "0px",
      ).length;
    try {
      // 개발 모드처럼 효과가 두 번 돌아도 줄마다 한 번만 펼친다
      render(
        <StrictMode>
          <Harness start="job" />
        </StrictMode>,
      );
      fireEvent.click(screen.getByRole("button", { name: "점검 시작" }));
      await tick(3);
      expect(rows()).toBe(3);
      expect(opens()).toBe(3);

      // 다른 화면을 보는 동안 쌓이는 줄은 움직이지 않는다(돌아왔을 때 한꺼번에 깜빡이지 않게)
      fireEvent.click(screen.getByRole("button", { name: "다른 화면으로" }));
      await tick(4);
      expect(rows()).toBe(7);
      expect(opens()).toBe(3);

      fireEvent.click(screen.getByRole("button", { name: "자산 탐색으로 돌아가기" }));
      await tick(1);
      expect(opens()).toBe(4);
    } finally {
      // jsdom 원래 상태(animate 없음)로 되돌린다
      delete (Element.prototype as { animate?: unknown }).animate;
    }
  });
});
