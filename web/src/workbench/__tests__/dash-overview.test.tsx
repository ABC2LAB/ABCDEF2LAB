// 통합 대시보드(전체 보기): 보기 전환 · 끝까지 못 간 점검의 0건을 0 으로 세지 않기 · 추이를 대상별로 묶기 · 전송 통제 낱말 ·
// 같은 눈금 막대
import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { ApiError } from "@/lib/diagnosis/api";
import type { URLRunSummary } from "@/lib/diagnosis/diagnosis-runs";
import { catalog } from "@/lib/diagnosis/diagnosis-url-fixtures";
import type { RequestApproval } from "@/lib/diagnosis/request-approvals";

import { resetUrl } from "../live/urlDiagnosis";
import { resetUrlRuns } from "../live/urlRunList";
import { type Shell, ShellContext } from "../shell";
import {
  type ActiveInfo,
  APPROVAL_ABSENT_STATUSES,
  activeE2Text,
  activeInfoOf,
  cutOf,
  E2_PARTS,
  type EndedRun,
  type EndedStat,
  e2CardSubOf,
  e2ChipOf,
  e2ReadOf,
  e2TallyOf,
  endedStatOf,
  groupTrend,
  hostParts,
  journalText,
  mapLimit,
  needsRetry,
  partialNoteOf,
  pendingTallyOf,
  pendingTileOf,
  scopeUnreadOf,
  sourceOf,
  ThinBar,
  TrendChart,
  type TrendCol,
  targetGroupsOf,
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
    e2: "absent",
    pv: "ok",
    ...n,
  }) as EndedStat;
const row = (id: string, state: string, at: string): URLRunSummary =>
  ({ id, state, finished_at: at, started_at: at, report_available: true }) as unknown as URLRunSummary;
const col = (id: string, state: string, at: string, host: string | null, st: EndedRun["stat"]): TrendCol => ({
  row: row(id, state, at),
  run: { host, hostFailed: false, stat: st },
});

it("끝까지 못 간 점검: 진단 실패 · 중지만 가른다", () => {
  expect(cutOf("failed" as URLRunSummary["state"])).toBe("failed");
  expect(cutOf("cancelled" as URLRunSummary["state"])).toBe("stopped");
  expect(cutOf("completed" as URLRunSummary["state"])).toBeNull();
});

it("끝까지 못 간 점검의 0건은 판정이 없는 것 — 0 으로 세지 않는다(규칙 5). 정상 종료의 0건은 0 이다", () => {
  const zero: EndedRun = { host: "a.example", hostFailed: false, stat: stat() };
  expect(usableStat({ state: "completed" } as URLRunSummary, zero)).toBe(zero.stat);
  expect(usableStat({ state: "failed" } as URLRunSummary, zero)).toBeNull();
  // 3초 만에 중지한 점검의 0건도 같다
  expect(usableStat({ state: "cancelled" } as URLRunSummary, zero)).toBeNull();
  // 남은 결과가 있으면 그 일부는 쓴다
  const some: EndedRun = { host: "a.example", hostFailed: false, stat: stat({ aiReview: 2 }) };
  expect(usableStat({ state: "failed" } as URLRunSummary, some)).toBe(some.stat);
  expect(usableStat({ state: "cancelled" } as URLRunSummary, some)).toBe(some.stat);
  // 결과 없음 · 못 읽음 · 읽기 전
  expect(
    usableStat({ state: "completed" } as URLRunSummary, { host: null, hostFailed: false, stat: "none" }),
  ).toBeNull();
  expect(
    usableStat({ state: "completed" } as URLRunSummary, { host: null, hostFailed: false, stat: "error" }),
  ).toBeNull();
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

// ── #978 리뷰(이동찬 P2 · 이경준 P2-1~4): 읽기 실패를 0 · 정상으로 보이지 않는다(절대 규칙 5) ──

const run = (st: EndedRun["stat"], host: string | null = "a.example", hostFailed = false): EndedRun => ({
  host,
  hostFailed,
  stat: st,
});
const e2Counts = (attempts: number, denied = 0) => ({
  attempts,
  allowed: attempts - denied,
  denied,
  unsupported: 0,
  forward_attempted: attempts - denied,
  responses: attempts - denied,
  unknown: 0,
  transient: 0,
  withheld: 0,
  calls: attempts,
});
const flowOf = (journal: "verified" | "invalid" | "none", attempts = 0) =>
  ({
    format: "abc2lab.e2-flow-view/v1",
    journal,
    last_index: 0,
    more: false,
    omitted_calls: 0,
    counts: e2Counts(attempts),
    calls: [],
  }) as never;

it("출처 읽기: 그 경로가 없는 서버(404)만 '없음', 503 · 409 등 나머지는 '조회 실패'로 가른다(이동찬 P2 · 이경준 #978 머지 뒤 P2)", async () => {
  expect(await sourceOf(Promise.resolve(7))).toEqual({ state: "ok", value: 7 });
  expect(await sourceOf(Promise.reject(new ApiError(404, "x")))).toEqual({ state: "absent" });
  // 이 서버는 읽기 실패(DB · 봉인 확인 · 승인 기록 읽기)를 503 으로 답한다 — 경로 없음이 아니다
  for (const status of [403, 409, 500, 501, 503])
    expect(await sourceOf(Promise.reject(new ApiError(status, "x")))).toEqual({ state: "failed" });
  // 승인 대기 목록은 실행 결속이 없는 점검(403)까지 '없음'이다
  expect(await sourceOf(Promise.reject(new ApiError(403, "x")), APPROVAL_ABSENT_STATUSES)).toEqual({ state: "absent" });
  expect(await sourceOf(Promise.reject(new ApiError(503, "x")), APPROVAL_ABSENT_STATUSES)).toEqual({ state: "failed" });
  expect(await sourceOf(Promise.reject(new TypeError("fetch failed")))).toEqual({ state: "failed" });
  expect(await sourceOf(Promise.reject(new Error("shape")))).toEqual({ state: "failed" });
  // 중단(다른 점검으로 바뀜)은 실패로 세지 않고 그대로 올린다
  await expect(sourceOf(Promise.reject(new DOMException("aborted", "AbortError")))).rejects.toThrow();
});

it("제품 판정 조회가 실패하면 확정 0건으로 합치지 않고 부분 결과로 표시하고 다시 읽는다(이동찬 P2 · 이경준 P2-1)", () => {
  const rem = { task_id: 1, entries: [] } as never;
  const failed = endedStatOf(rem, { state: "failed" }, { state: "ok", value: flowOf("verified", 3) });
  expect(failed.pv).toBe("failed");
  expect(needsRetry(run(failed))).toBe(true);
  // 경로가 없는 서버(미지원)는 실패가 아니라 '없음'이다 — 부분 결과가 아니고 다시 읽지 않는다
  const absent = endedStatOf(rem, { state: "absent" }, { state: "ok", value: flowOf("verified", 3) });
  expect(absent.pv).toBe("absent");
  expect(needsRetry(run(absent))).toBe(false);
  // 지금 판정 칸의 안내: 못 읽은 점검이 든 합계는 부분 결과다
  const rows = [row("2", "completed", "2026-11-26T01:00:00Z"), row("1", "completed", "2026-11-25T01:00:00Z")];
  const runs = new Map([
    ["2", run(failed, "a.example")],
    ["1", run(absent, "b.example")],
  ]);
  const { groups } = targetGroupsOf(rows, (r) => runs.get(r.id));
  expect(partialNoteOf(groups)).toBe("제품 판정을 못 읽은 대상 1곳 — 합계는 부분 결과");
  runs.set("2", run(absent, "a.example"));
  expect(partialNoteOf(targetGroupsOf(rows, (r) => runs.get(r.id)).groups)).toBeNull();
});

it("승인 대기 조회가 실패하면 '승인 대기 0'으로 보이지 않는다 — 못 읽은 점검 수를 따로 센다(이경준 P2-2)", () => {
  const approval = {
    id: "ap-1",
    state: "pending",
    created_at: "2026-11-26T01:00:00Z",
    expires_at: "2026-11-26T01:10:00Z",
  } as unknown as RequestApproval;
  const info = (over: Partial<ActiveInfo>): ActiveInfo => ({
    job: null,
    jobFailed: false,
    flow: null,
    pending: null,
    pendingFailed: false,
    flowFailed: false,
    e2: "absent",
    ...over,
  });
  const r1 = row("1", "running", "2026-11-26T01:00:00Z");
  const r2 = row("2", "running", "2026-11-26T01:00:00Z");
  const r3 = row("3", "running", "2026-11-26T01:00:00Z");
  // 한 점검만 승인 대기 조회 실패: 대기 0 이 아니라 '확인 못 함'
  const oneFailed = pendingTallyOf([{ row: r1, info: info({ pendingFailed: true }) }]);
  expect(oneFailed).toEqual({ pending: [], unread: 1 });
  expect(pendingTileOf(oneFailed, true)).toEqual({ n: null, note: "못 읽은 점검 1건 · 승인 대기가 있을 수 있음" });
  // 상태 조회 실패도 같다(어느 모드인지 몰라 승인 대기를 읽지 못함)
  expect(pendingTallyOf([{ row: r2, info: info({ jobFailed: true }) }]).unread).toBe(1);
  // 읽은 점검의 대기는 세고, 못 읽은 점검은 따로 붙인다
  const mixed = pendingTallyOf([
    { row: r1, info: info({ pending: [approval] }) },
    { row: r2, info: info({ pendingFailed: true }) },
    { row: r3, info: info({ pending: [] }) },
  ]);
  expect(mixed.pending).toHaveLength(1);
  expect(pendingTileOf(mixed, true)).toEqual({ n: 1, note: "못 읽은 점검 1건" });
  // 다 읽었고 대기가 없으면 진짜 0 이다. 아직 읽기 전이면 수를 적지 않는다
  expect(pendingTileOf(pendingTallyOf([{ row: r3, info: info({ pending: [] }) }]), true)).toEqual({ n: 0, note: null });
  expect(pendingTileOf(pendingTallyOf([{ row: r3, info: undefined }]), false)).toEqual({ n: null, note: null });
});

it("전송 기록 칩: 분모는 끝난 점검 수, 못 읽은 점검이 있으면 초록이 아니다(이경준 P2-3)", () => {
  const rows = ["5", "4", "3", "2", "1"].map((id) => row(id, "completed", `2026-11-2${id}T01:00:00Z`));
  const ok = () =>
    endedStatOf(
      { task_id: 1, entries: [] } as never,
      { state: "absent" },
      { state: "ok", value: flowOf("verified", 4) },
    );
  const unread = () => endedStatOf({ task_id: 1, entries: [] } as never, { state: "absent" }, { state: "failed" });
  const runs = new Map<string, EndedRun>([
    ["5", run(ok())],
    ["4", run(ok())],
    ["3", run(ok())],
    ["2", run(unread())],
    ["1", run("error")],
  ]);
  const tally = e2TallyOf(rows, (r) => runs.get(r.id));
  expect([tally.total, tally.verified.length, tally.invalid, tally.unread, tally.absent]).toEqual([5, 3, 0, 2, 0]);
  expect(e2ChipOf(tally)).toEqual({ tone: "unk", text: "전송 기록 검증 3 / 5 · 못 읽음 2" });
  // 다 읽고 다 검증됐을 때만 초록
  runs.set("2", run(ok()));
  runs.set("1", run(ok()));
  expect(e2ChipOf(e2TallyOf(rows, (r) => runs.get(r.id)))).toEqual({ tone: "ok", text: "전송 기록 검증 5 / 5" });
  // 검증 실패가 하나라도 있으면 경고, 기록이 없는 점검(진단 요청 전에 끝남 · 결과 없음)은 따로 적는다
  runs.set(
    "5",
    run(
      endedStatOf({ task_id: 1, entries: [] } as never, { state: "absent" }, { state: "ok", value: flowOf("invalid") }),
    ),
  );
  runs.set("4", run("none"));
  expect(e2ChipOf(e2TallyOf(rows, (r) => runs.get(r.id)))).toEqual({
    tone: "warn",
    text: "전송 기록 검증 실패 1 / 5 · 기록 없음 1",
  });
  // 아직 읽기 전
  expect(e2ChipOf(e2TallyOf(rows, () => undefined))).toEqual({ tone: "unk", text: "전송 기록 읽기 전" });
});

it("상태를 못 읽은 점검은 다른 대상으로 갈라 두 번 합치지 않는다 — 대상 합계에서 빼고 따로 센다(이경준 P2-4)", () => {
  const rows = [row("2", "completed", "2026-11-26T01:00:00Z"), row("1", "completed", "2026-11-20T01:00:00Z")];
  const runs = new Map<string, EndedRun>([
    ["2", run(stat({ confirmed: 2 }), "a.example")],
    // 같은 대상의 예전 점검인데 상태 조회만 실패해 주소를 모른다
    ["1", run(stat({ confirmed: 3 }), null, true)],
  ]);
  const { groups, unknownTargets } = targetGroupsOf(rows, (r) => runs.get(r.id));
  expect(groups.map((g) => g.key)).toEqual(["a.example"]);
  expect(groups.reduce((n, g) => n + (g.stat?.confirmed ?? 0), 0)).toBe(2);
  expect(unknownTargets.map((r) => r.id)).toEqual(["1"]);
  expect(needsRetry(runs.get("1") as EndedRun)).toBe(true);
});

it("끝난 점검 읽기는 한 번에 정해진 수만 보낸다(이경준 P3)", async () => {
  let active = 0;
  let peak = 0;
  const out = await mapLimit([1, 2, 3, 4, 5, 6, 7, 8, 9], 3, async (n) => {
    active += 1;
    peak = Math.max(peak, active);
    await new Promise((done) => setTimeout(done, 1));
    active -= 1;
    return n * 2;
  });
  expect(out).toEqual([2, 4, 6, 8, 10, 12, 14, 16, 18]);
  expect(peak).toBe(3);
});

// ── 저널 없음(journal "none", #983): E②를 잡기 전이거나 잡지 않고 끝난 점검. 검증 실패가 아니라 진단 요청 없음이다 ──

const endedWith = (journal: "verified" | "invalid" | "none", attempts = 0) =>
  run(
    endedStatOf(
      { task_id: 1, entries: [] } as never,
      { state: "absent" },
      { state: "ok", value: flowOf(journal, attempts) },
    ),
  );

it("끝난 점검의 저널 없음은 '진단 요청 없음'으로 따로 센다 — 검증으로 세지 않아 초록이 아니다(#978 머지 뒤 이경준 리뷰 P2)", () => {
  const rows = [row("2", "completed", "2026-11-27T01:00:00Z"), row("1", "completed", "2026-11-26T01:00:00Z")];
  const runs = new Map<string, EndedRun>([
    ["2", endedWith("none")],
    ["1", endedWith("verified", 3)],
  ]);
  const tally = e2TallyOf(rows, (r) => runs.get(r.id));
  expect([tally.total, tally.verified.length, tally.none, tally.invalid]).toEqual([2, 1, 1, 0]);
  // 취득 기록이 생기기 전 판의 회차는 지워진 저널과 none 을 가르지 못하므로(계약 §4 한계) '검증됨'으로 세지 않는다
  expect(e2ChipOf(tally)).toEqual({ tone: "unk", text: "전송 기록 검증 1 / 2 · 진단 요청 없음 1" });
  expect(e2CardSubOf(tally)).toBe("진단 요청 · 끝난 점검 2건 중 1건 · 진단 요청 없음 1건");
  expect(journalText(e2ReadOf({ state: "ok", value: flowOf("none") }))).toBe("진단 요청 없음");
});

it("검증 실패는 저널이 검증되지 않은 것(invalid)만이다 — 저널 없음과 섞지 않는다", () => {
  const rows = [row("2", "completed", "2026-11-27T01:00:00Z"), row("1", "completed", "2026-11-26T01:00:00Z")];
  const runs = new Map<string, EndedRun>([
    ["2", endedWith("none")],
    ["1", endedWith("invalid")],
  ]);
  const tally = e2TallyOf(rows, (r) => runs.get(r.id));
  expect([tally.verified.length, tally.none, tally.invalid]).toEqual([0, 1, 1]);
  expect(e2ChipOf(tally)).toEqual({ tone: "warn", text: "전송 기록 검증 실패 1 / 2 · 진단 요청 없음 1" });
  expect(journalText(e2ReadOf({ state: "ok", value: flowOf("invalid") }))).toBe("검증 실패");
});

it("전송 통제 카드 머리는 끝난 점검 수 · 검증된 수 · 진단 요청 없음 · 뺀 것을 적는다", () => {
  const rows = ["5", "4", "3", "2", "1"].map((id) => row(id, "completed", `2026-11-2${id}T01:00:00Z`));
  const runs = new Map<string, EndedRun>([
    ["5", endedWith("verified", 1)],
    ["4", endedWith("verified", 2)],
    ["3", endedWith("verified", 3)],
    ["2", endedWith("none")],
    ["1", endedWith("none")],
  ]);
  expect(e2CardSubOf(e2TallyOf(rows, (r) => runs.get(r.id)))).toBe(
    "진단 요청 · 끝난 점검 5건 중 3건 · 진단 요청 없음 2건",
  );
  runs.set("2", endedWith("invalid"));
  runs.set("1", run("error"));
  expect(e2CardSubOf(e2TallyOf(rows, (r) => runs.get(r.id)))).toBe(
    "진단 요청 · 끝난 점검 5건 중 3건 · 검증 실패 1건 뺌 · 못 읽음 1건 뺌",
  );
});

it("진행 중 점검의 저널 없음은 '진단 요청 전'이고 '검증 실패'가 보이지 않는다", () => {
  const none = activeE2Text(e2ReadOf({ state: "ok", value: flowOf("none") }));
  expect(none).toBe("진단 요청 전");
  expect(none ?? "").not.toMatch(/검증 실패/);
  // 검증 실패 · 검증된 기록은 지금처럼
  expect(activeE2Text(e2ReadOf({ state: "ok", value: flowOf("invalid") }))).toBe("전송 기록 검증 실패");
  expect(activeE2Text(e2ReadOf({ state: "ok", value: flowOf("verified", 2) }))).toBe("허용 2 · 막음 0");
});

// ── #978 머지 뒤 리뷰(이경준): 503 은 조회 실패 · 흐름을 못 읽은 점검 ──

const absent = { state: "absent" } as const;
const rejected = (status: number) => Promise.reject(new ApiError(status, "x"));

it("제품 판정 목록이 503 이면 확정 0건이 아니라 부분 결과이고 다시 읽는다", async () => {
  const pv = await sourceOf(rejected(503));
  const st = endedStatOf({ task_id: 1, entries: [] } as never, pv, absent);
  expect(st.pv).toBe("failed");
  expect(needsRetry(run(st))).toBe(true);
  const rows = [row("1", "completed", "2026-11-27T01:00:00Z")];
  expect(partialNoteOf(targetGroupsOf(rows, () => run(st)).groups)).toBe(
    "제품 판정을 못 읽은 대상 1곳 — 합계는 부분 결과",
  );
});

it("승인 대기 목록이 503 이면 '승인 대기 0'으로 보이지 않는다 — 403(실행 결속 없음)은 대기가 없는 것이다", async () => {
  const r1 = row("1", "running", "2026-11-27T01:00:00Z");
  const failed = activeInfoOf({
    status: absent,
    flow: absent,
    approvals: await sourceOf(rejected(503), APPROVAL_ABSENT_STATUSES),
    e2: absent,
  });
  expect(failed.pendingFailed).toBe(true);
  expect(pendingTileOf(pendingTallyOf([{ row: r1, info: failed }]), true)).toEqual({
    n: null,
    note: "못 읽은 점검 1건 · 승인 대기가 있을 수 있음",
  });
  const unbound = activeInfoOf({
    status: absent,
    flow: absent,
    approvals: await sourceOf(rejected(403), APPROVAL_ABSENT_STATUSES),
    e2: absent,
  });
  expect(unbound.pendingFailed).toBe(false);
  expect(pendingTileOf(pendingTallyOf([{ row: r1, info: unbound }]), true)).toEqual({ n: 0, note: null });
});

it("탐색 흐름을 못 읽은 진행 중 점검은 '범위 확인 대기'를 확인하지 못한 점검으로 센다", async () => {
  const failed = activeInfoOf({ status: absent, flow: await sourceOf(rejected(503)), approvals: absent, e2: absent });
  expect(failed.flowFailed).toBe(true);
  expect(scopeUnreadOf([{ info: failed }])).toBe(1);
  // 흐름이 아직 시작 전(읽은 값이 null)인 점검은 못 읽은 것이 아니다
  const notStarted = activeInfoOf({
    status: absent,
    flow: { state: "ok", value: null },
    approvals: absent,
    e2: absent,
  });
  expect(notStarted.flowFailed).toBe(false);
  expect(scopeUnreadOf([{ info: notStarted }])).toBe(0);
});
