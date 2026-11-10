// 자산 탐색 재생 상태와 순수 전이 함수. 원본 전역 crawl 객체와 crawlReset · step · schedule · cPause 처리.
// 타이머는 여기서 돌리지 않는다: "다음 단계를 ms 뒤에" 라는 예약(sched)만 상태에 남기고, 엔진(useCrawlEngine)이 setTimeout 을 건다.

import { AT, EV, type FoundSeed, PCT } from "../data/crawl";
import { kind } from "./kind";

/** 예약: ms 뒤에 다음 단계. 객체가 바뀔 때마다 엔진이 타이머를 새로 건다(원본 clearTimeout + setTimeout). */
export interface Sched {
  readonly ms: number;
  readonly n: number;
}

let schedN = 0;
/** 새 예약 객체 (같은 ms 라도 매번 다른 객체 → 엔진이 다시 건다). */
export function sched(ms: number): Sched {
  schedN += 1;
  return { ms, n: schedN };
}

/** 찾은 경로 + 방금 찾았는지(fresh). 원본 found 항목. */
export interface Found extends FoundSeed {
  /** 바로 직전 단계에서 찾음 → 화면이 `!reduce` 일 때 fresh 클래스(나타나는 효과). */
  readonly fresh: boolean;
}

export type CrawlView = "card" | "tree";
export type CrawlFilter = "all" | "ap" | "ex";

/** 원본 전역 `crawl` 객체 (timer → sched). */
export interface CrawlState {
  /** 이번 탐색을 시작했는가. false 면 다음에 자산 탐색 화면에 들어올 때 처음부터 다시 시작 */
  readonly started: boolean;
  /** 다음에 재생할 EV 위치 (= 로그 줄 수) */
  readonly i: number;
  /** 다음 단계 예약 (null = 타이머 없음) */
  readonly sched: Sched | null;
  readonly paused: boolean;
  readonly done: boolean;
  readonly found: readonly Found[];
  /** 보낸 요청 수 (req·login) */
  readonly sent: number;
  /** 요청 안 함 수 (skip) */
  readonly skip: number;
  /** 원본에 있던 자리 (쓰이지 않음, 재시작해도 유지) */
  readonly open: Readonly<Record<string, boolean>>;
  /** 찾은 경로 보기: 묶음별(card) · 주소 구조(tree). 재시작해도 유지 */
  readonly view: CrawlView;
  /** 찾은 경로 거르기. 재시작하면 all */
  readonly kf: CrawlFilter;
  /** 주소 구조 마디별 펼침 (키 = 마디 key, false 면 접힘). 재시작해도 유지 */
  readonly smOpen: Readonly<Record<string, boolean>>;
  /** 로그 줄 i 가 추가될 때 나타나는 효과를 줄지 (원본: !REDUCE && 지금 화면이 자산 탐색) */
  readonly rowFresh: readonly boolean[];
  /** 재시작 횟수. 화면이 바뀌면 열린 API 상세를 닫는 등 (원본 crawlReset 의 cApiClose) */
  readonly run: number;
}

/** 원본 초기값 (아직 시작 안 함). */
export function crawlInit(): CrawlState {
  return {
    started: false,
    i: 0,
    sched: null,
    paused: false,
    done: false,
    found: [],
    sent: 0,
    skip: 0,
    open: {},
    view: "card",
    kf: "all",
    smOpen: {},
    rowFresh: [],
    run: 0,
  };
}

/** 원본 `crawlReset()`: 처음부터 다시. 타이머는 지운다(예약 없음). view·open·smOpen 은 유지, kf 는 all. */
export function crawlReset(c: CrawlState): CrawlState {
  return {
    ...crawlInit(),
    started: true,
    open: c.open,
    view: c.view,
    smOpen: c.smOpen,
    run: c.run + 1,
  };
}

/** 원본 `crawlEnter()`: 시작 안 했으면 재시작 후 300ms 뒤 첫 단계. (railNew('crawl') 은 스토어가 함께 처리) */
export function crawlEnter(c: CrawlState): CrawlState {
  if (c.started) return c;
  return { ...crawlReset(c), sched: sched(300) };
}

export interface CrawlStepOpts {
  /** 추가되는 로그 줄에 나타나는 효과를 줄지 (엔진: !reduce && route === "crawl") */
  rowFresh: boolean;
}

/** 원본 `step()`: EV 한 단계 재생. 끝나면 done, 아니면 다음 단계까지의 간격으로 예약. */
export function crawlStep(c: CrawlState, o: CrawlStepOpts = { rowFresh: false }): CrawlState {
  if (c.paused || c.done) return c;
  const i = c.i;
  if (i >= EV.length) return c;
  const e = EV[i];
  const ni = i + 1;
  let found = c.found;
  // 찾은 경로 목록을 다시 그리면 이전 fresh 는 지워지고 이번 것만 fresh (원본 renderTree)
  if (e.found.length) {
    found = [
      ...c.found.map((f) => (f.fresh ? { ...f, fresh: false } : f)),
      ...e.found.map((f) => ({ ...f, fresh: true })),
    ];
  }
  const done = ni >= EV.length;
  return {
    ...c,
    i: ni,
    sent: c.sent + (e.k === "req" || e.k === "login" ? 1 : 0),
    skip: c.skip + (e.k === "skip" ? 1 : 0),
    found,
    done,
    rowFresh: [...c.rowFresh, o.rowFresh],
    sched: done ? null : sched(AT[ni] - AT[i] || 120),
  };
}

/** 원본 cPause 클릭: 끝났으면 무시. 멈추면 타이머 해제, 다시 시작하면 250ms 뒤. */
export function crawlTogglePause(c: CrawlState): CrawlState {
  if (c.done) return c;
  const paused = !c.paused;
  return { ...c, paused, sched: paused ? null : sched(250) };
}

/** 원본 cCancel 클릭: 타이머 해제, 다음에 들어오면 처음부터. (railNew(null) 은 스토어) */
export function crawlCancel(c: CrawlState): CrawlState {
  return { ...c, sched: null, started: false };
}

/** found 의 fresh 를 모두 끔 (원본: 목록을 다시 그리면 fresh 가 사라짐). */
function unfresh(found: readonly Found[]): readonly Found[] {
  return found.some((f) => f.fresh) ? found.map((f) => (f.fresh ? { ...f, fresh: false } : f)) : found;
}

export function crawlSetView(c: CrawlState, view: CrawlView): CrawlState {
  return { ...c, view, found: unfresh(c.found) };
}

export function crawlSetFilter(c: CrawlState, kf: CrawlFilter): CrawlState {
  return { ...c, kf, found: unfresh(c.found) };
}

/** 주소 구조 마디 펼치기/접기 (원본 data-smt 클릭: 지금 aria-expanded 의 반대로). */
export function crawlSetSmOpen(c: CrawlState, key: string, open: boolean): CrawlState {
  return { ...c, smOpen: { ...c.smOpen, [key]: open }, found: unfresh(c.found) };
}

// ---------- 화면 표시용 계산 (원본 crawlRender) ----------

/** 지금까지의 진행률(%). */
export function crawlPct(c: CrawlState): number {
  return c.i ? PCT[c.i - 1] : 0;
}

/** 마지막으로 재생한 단계 (없으면 null). */
export function crawlLast(c: CrawlState) {
  return c.i ? EV[c.i - 1] : null;
}

/** 경과 초 (원본 cEl). */
export function crawlElapsedSec(c: CrawlState): number {
  return Math.round((c.i ? AT[c.i - 1] : 0) / 1000);
}

/** 방문 대기 수 (원본 cQueue). 알게 된 경로 = sent + 대기. */
export function crawlQueue(c: CrawlState): number {
  const e = crawlLast(c);
  return e ? e.q : 0;
}

/** 찾은 경로 중 조회(GET) 수와 승인 필요(ap) 수. */
export function crawlCounts(c: CrawlState): { get: number; ap: number } {
  let get = 0;
  let ap = 0;
  for (const f of c.found) {
    if (f.m === "GET") get++;
    if (kind(f.m, f.p) === "ap") ap++;
  }
  return { get, ap };
}
