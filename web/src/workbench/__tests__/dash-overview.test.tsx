// 통합 대시보드(전체 보기): 보기 전환 · 끝까지 못 간 점검의 0건을 0 으로 세지 않기 · 추이를 대상별로 묶기 · 전송 통제 낱말 ·
// 같은 눈금 막대
import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import type { URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import { catalog } from "@/lib/diagnosis/diagnosis-url-fixtures";

import { resetUrl } from "../live/urlDiagnosis";
import { resetUrlRuns } from "../live/urlRunList";
import { type Shell, ShellContext } from "../shell";
import {
  cutOf,
  E2_PARTS,
  type EndedRun,
  type EndedStat,
  groupTrend,
  hostParts,
  ThinBar,
  TrendChart,
  type TrendCol,
  usableStat,
} from "../views/live/DashOverview";
import { UrlDashboardLive } from "../views/live/UrlDashboardLive";

const noop = () => undefined;
const shell: Shell = {
  route: "dashboard",
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
  foldRail: noop,
  dashPick: noop,
};
const mount = () =>
  render(
    <ShellContext.Provider value={shell}>
      <UrlDashboardLive />
    </ShellContext.Provider>,
  );
const modeRadio = (name: string) =>
  within(screen.getByRole("radiogroup", { name: "대시보드 보기" })).getByRole("radio", { name });

beforeEach(() => {
  localStorage.clear();
  resetUrl({ kind: "v2", catalog });
  resetUrlRuns();
  // 준비 서비스 health 는 이 시험이 보지 않는다(없는 서버처럼)
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response("", { status: 404 })),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("대시보드 보기: 처음엔 전체(통합 대시보드), 점검별로 바꾸면 점검 하나 대시보드 — 고른 보기는 이 브라우저에 기억한다", async () => {
  mount();
  expect(await screen.findByRole("region", { name: "지금 할 일" })).toBeInTheDocument();
  expect(modeRadio("전체")).toBeChecked();

  fireEvent.click(modeRadio("점검별"));
  expect(modeRadio("점검별")).toBeChecked();
  expect(screen.queryByRole("region", { name: "지금 할 일" })).toBeNull();
  expect(localStorage.getItem("abc2lab-dash-mode")).toBe("run");

  // 다시 열면 기억한 보기로
  cleanup();
  mount();
  expect(await screen.findByRole("radiogroup", { name: "대시보드 보기" })).toBeInTheDocument();
  expect(modeRadio("점검별")).toBeChecked();
  expect(screen.queryByRole("region", { name: "지금 할 일" })).toBeNull();

  fireEvent.click(modeRadio("전체"));
  expect(await screen.findByRole("region", { name: "지금 할 일" })).toBeInTheDocument();
  expect(localStorage.getItem("abc2lab-dash-mode")).toBe("all");
});

const stat = (n: Partial<Record<"confirmed" | "aiReview" | "suspected" | "undecided" | "allowed", number>> = {}) =>
  ({
    confirmed: 0,
    aiReview: 0,
    suspected: 0,
    undecided: 0,
    allowed: 0,
    sev: { crit: 0, hi: 0, mid: 0, lo: 0, info: 0, none: 0 },
    items: [],
    e2: null,
    ...n,
  }) as EndedStat;
const row = (id: string, state: string, at: string): URLRunSummary =>
  ({ id, state, finished_at: at, started_at: at, report_available: true }) as unknown as URLRunSummary;
const col = (id: string, state: string, at: string, host: string | null, st: EndedRun["stat"]): TrendCol => ({
  row: row(id, state, at),
  run: { host, stat: st },
});

it("끝까지 못 간 점검: 진단 실패 · 중지만 가른다", () => {
  expect(cutOf("failed" as URLRunSummary["state"])).toBe("failed");
  expect(cutOf("cancelled" as URLRunSummary["state"])).toBe("stopped");
  expect(cutOf("completed" as URLRunSummary["state"])).toBeNull();
});

it("끝까지 못 간 점검의 0건은 판정이 없는 것 — 0 으로 세지 않는다(규칙 5). 정상 종료의 0건은 0 이다", () => {
  const zero: EndedRun = { host: "a.example", stat: stat() };
  expect(usableStat({ state: "completed" } as URLRunSummary, zero)).toBe(zero.stat);
  expect(usableStat({ state: "failed" } as URLRunSummary, zero)).toBeNull();
  // 3초 만에 중지한 점검의 0건도 같다
  expect(usableStat({ state: "cancelled" } as URLRunSummary, zero)).toBeNull();
  // 남은 결과가 있으면 그 일부는 쓴다
  const some: EndedRun = { host: "a.example", stat: stat({ aiReview: 2 }) };
  expect(usableStat({ state: "failed" } as URLRunSummary, some)).toBe(some.stat);
  expect(usableStat({ state: "cancelled" } as URLRunSummary, some)).toBe(some.stat);
  // 결과 없음 · 못 읽음 · 읽기 전
  expect(usableStat({ state: "completed" } as URLRunSummary, { host: null, stat: "none" })).toBeNull();
  expect(usableStat({ state: "completed" } as URLRunSummary, { host: null, stat: "error" })).toBeNull();
  expect(usableStat({ state: "completed" } as URLRunSummary, undefined)).toBeNull();
});

it("추이는 같은 대상끼리 묶고(묶음 안은 시간 순), 가장 최근에 점검한 대상을 오른쪽 끝에 둔다", () => {
  const cols = [
    col("1", "completed", "2026-11-13T01:00:00Z", "shop.example", stat({ confirmed: 3 })),
    col("2", "completed", "2026-11-15T01:00:00Z", "api.example", stat({ confirmed: 2 })),
    col("3", "completed", "2026-11-17T01:00:00Z", "blog.example", stat({ confirmed: 2 })),
    col("4", "completed", "2026-11-20T01:00:00Z", "shop.example", stat({ confirmed: 2 })),
    col("5", "completed", "2026-11-25T01:00:00Z", "api.example", stat({ confirmed: 2 })),
    col("6", "completed", "2026-11-26T01:00:00Z", "shop.example", stat({ confirmed: 1 })),
  ];
  const groups = groupTrend(cols);
  expect(groups.map((g) => g.host)).toEqual(["blog.example", "api.example", "shop.example"]);
  expect(groups.map((g) => g.cols.map((c) => c.row.id))).toEqual([["3"], ["2", "5"], ["1", "4", "6"]]);
});

it("주소를 못 읽은 점검은 그 점검 하나가 한 묶음이다(다른 점검과 섞지 않는다)", () => {
  const a: TrendCol = {
    row: { ...row("x1", "completed", "2026-11-01T00:00:00Z"), display_name: "첫 점검" },
    run: undefined,
  };
  const b: TrendCol = {
    row: { ...row("x2", "completed", "2026-11-02T00:00:00Z"), display_name: "첫 점검" },
    run: undefined,
  };
  const groups = groupTrend([a, b]);
  expect(groups).toHaveLength(2);
  expect(groups.map((g) => g.key)).toEqual(["run:x1", "run:x2"]);
});

it("묶음 이름은 점 뒤 · 콜론 앞에서 꺾는다", () => {
  expect(hostParts("nodegoat.test:9443").map((p) => p.part)).toEqual(["nodegoat.", "test", ":9443"]);
  const at = hostParts("a.a.a").map((p) => p.at);
  expect(new Set(at).size).toBe(at.length);
  expect(hostParts("localhost").map((p) => p.part)).toEqual(["localhost"]);
});

it("추이 차트: 대상 이름 줄 · 끝까지 못 간 점검 표식(진단 실패 ✕ · 중지 ■) · 정상 종료 0건은 0", () => {
  const onOpen = vi.fn();
  const groups = groupTrend([
    col("1", "cancelled", "2026-11-22T01:00:00Z", "admin.example", stat()),
    col("2", "failed", "2026-11-23T01:00:00Z", "fail.example", stat()),
    col("3", "completed", "2026-11-24T01:00:00Z", "zero.example", stat()),
    col("4", "cancelled", "2026-11-25T01:00:00Z", "part.example", stat({ aiReview: 2 })),
    col("5", "completed", "2026-11-26T01:00:00Z", "shop.example", stat({ confirmed: 1, undecided: 1 })),
  ]);
  render(<TrendChart groups={groups} onOpen={onOpen} />);
  // 대상마다 묶음 하나, 이름은 묶음 밑에
  for (const host of ["admin.example", "fail.example", "zero.example", "part.example", "shop.example"])
    expect(screen.getByRole("listitem", { name: `${host} · 점검 1회` })).toBeInTheDocument();

  const stopped = screen.getByRole("button", { name: /admin\.example — 중지 · 판정 없음\(0으로 세지 않음\)/ });
  expect(within(stopped).getAllByText("■").length).toBeGreaterThan(0);
  expect(within(stopped).queryByText("0")).toBeNull();

  const failed = screen.getByRole("button", { name: /fail\.example — 진단 실패 · 판정 없음\(0으로 세지 않음\)/ });
  expect(within(failed).getAllByText("✕").length).toBeGreaterThan(0);

  // 정상으로 끝난 0건은 진짜 0 이다
  const zero = screen.getByRole("button", { name: /zero\.example — 확정 0 · 검토 필요 0/ });
  // 막대 자리의 0 하나 + 말풍선의 판정 네 갈래 수
  expect(within(zero).getAllByText("0")).toHaveLength(5);

  // 중지했어도 남은 결과는 그리고, 일부라는 것을 적는다
  const partial = screen.getByRole("button", { name: /part\.example — 중지\(일부 결과\) · 확정 0 · 검토 필요 2/ });
  expect(within(partial).getByText("중지 · 남은 결과만")).toBeInTheDocument();

  screen.getByRole("button", { name: /shop\.example — 확정 1/ }).click();
  expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: "5" }));
});

it("얇은 막대: max 를 주면 그 목록의 가장 큰 합에 대한 비율로(같은 눈금), 안 주면 꽉 찬 길이", () => {
  const parts = [
    { cls: "ov-vc", n: 2 },
    { cls: "ov-vr", n: 4 },
  ];
  const { container, rerender } = render(<ThinBar parts={parts} max={12} />);
  const fill = () => container.firstElementChild?.firstElementChild as HTMLElement;
  expect(fill().style.width).toBe("50%");
  rerender(<ThinBar parts={parts} />);
  expect(fill().style.width).toBe("100%");
  // 합이 max 보다 커도 넘치지 않는다
  rerender(<ThinBar parts={parts} max={3} />);
  expect(fill().style.width).toBe("100%");
  // 0건이면 채움 없이 바탕만
  rerender(<ThinBar parts={[{ cls: "ov-vc", n: 0 }]} max={12} />);
  expect(container.firstElementChild?.firstElementChild).toBeNull();
});

it("전송 통제 낱말은 '막음'으로 하나다(줄의 '막음 n' = 정책으로 막음 + 미지원 경로 막음)", () => {
  const names = E2_PARTS.map(([, name]) => name);
  expect(names).toEqual(["허용(응답 받음)", "정책으로 막음", "미지원 경로 막음", "결과 불명 · 실패"]);
  expect(names.join(" ")).not.toMatch(/거부|차단/);
});
