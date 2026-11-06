import "@testing-library/jest-dom/vitest";
import { useState } from "react";

import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CRAWL_EVENTS } from "../lib/crawl-demo";
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
    expect(tile("방문 대기").querySelector(".v")).toHaveTextContent("0");
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

  it("finds exactly the 37 paths that the 범위 확인 mockup lists", () => {
    // vitest 는 web/ 에서 돈다(저장소 루트에서 돌려도 찾게 둘 다 본다)
    const rel = "src/abc2lab-ui/screens/ScopeScreen.tsx";
    const file = [resolve(process.cwd(), rel), resolve(process.cwd(), "web", rel)].find((f) => existsSync(f)) ?? rel;
    const src = readFileSync(file, "utf8");
    const block = src.slice(src.indexOf("const EP: Endpoint[] = ["));
    const scope = [...block.slice(0, block.indexOf("\n];")).matchAll(/g: "(\w+)",\s*m: "(\w+)",\s*p: "([^"]+)"/g)].map(
      ([, g, m, p]) => `${m} ${p} ${g}`,
    );
    const found = CRAWL_EVENTS.flatMap((e) => e.found.map((f) => `${f.m} ${f.p} ${f.g}`));
    expect(scope).toHaveLength(37);
    expect(new Set(found).size).toBe(found.length);
    expect([...found].sort()).toEqual([...scope].sort());
  });
});
