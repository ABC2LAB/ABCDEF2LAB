// 작업대 공유 상태 (zustand). 화면을 넘나드는 원본 전역 변수와 함수를 한곳에 모았다.
// 상태 전이 자체는 lib/*State.ts 의 순수 함수이고, 여기서는 그 결과를 끼워 넣기만 한다.
// 타이머는 여기 없다: crawl.sched · live.sched 예약을 엔진(engines.ts)이 보고 setTimeout 을 건다.
//
// 구독할 때 주의: zustand 5 에서는 선택 함수가 매번 새 객체·배열을 돌려주면 무한 렌더가 난다.
// 원시값이나 스토어 안 객체 그대로(`s => s.live`)를 고르고, 새로 만드는 계산은 선택 함수 밖에서 하거나 useShallow 를 쓴다.

import { create } from "zustand";

import {
  type CrawlFilter,
  type CrawlState,
  type CrawlStepOpts,
  type CrawlView,
  crawlCancel,
  crawlEnter,
  crawlInit,
  crawlReset,
  crawlSetFilter,
  crawlSetSmOpen,
  crawlSetView,
  crawlStep,
  crawlTogglePause,
} from "./lib/crawlState";
import { type Appr, type Decisions, type PathDecision, pending } from "./lib/decisions";
import { curHost } from "./lib/host";
import {
  type LiveMode,
  type LiveState,
  type LiveStepOpts,
  lelapsed,
  liveEnd,
  liveKick,
  liveReset,
  liveStep,
  liveToggleFeedAll,
  liveTogglePause,
} from "./lib/liveState";
import { nowSec } from "./lib/time";

/** 대시보드에서 고를 수 있는 항목 (원본 data-dsel / data-pick 값). */
export type DSel = "appr" | "path" | "und" | "cov" | "act-shop" | "act-api" | "done-blog" | "done-docs" | "done-app";

/** 레일 '진행 중' 맨 위 새 점검 줄의 단계. null 이면 숨김. 원본 `railNew(phase)`. */
export type RailPhase = "crawl" | "scope" | null;

/** 강조색 시안. petrol 이 기본(속성 없음). */
export type Accent = "petrol" | "cobalt" | "mono";

/** 다른 화면에서 새 점검 주소를 채워 달라는 요청. seq 가 바뀔 때마다 새 점검 화면이 한 번 반영한다. */
export interface Prefill {
  readonly url: string;
  readonly seq: number;
}

/**
 * 보고서 대상. 원본 setReportHost(kind) 가 그 순간 값으로 rHost · rMeta 를 고정해 그리던 것을 그대로 스냅숏으로 둔다.
 * seq 가 바뀌면 보고서 화면은 rMode='finding' · kgSel=null 로 되돌린다(원본처럼).
 */
export interface ReportSnap {
  /** blog = 끝난 예시 점검 · live = 방금 끝낸(또는 종료한) 점검 */
  readonly kind: "blog" | "live";
  /** blog.example.com 또는 그때의 curHost(target) */
  readonly host: string;
  /** live 이고 완료 전에 종료함 (원본 `live.ended && !live.done`) */
  readonly ended: boolean;
  /** live 의 시작 시각(초) — rMeta '시작 오늘 HH:MM' */
  readonly start: number;
  /** live 의 소요(분) — rMeta '소요 N분' */
  readonly mins: number;
  readonly seq: number;
}

export interface WbState {
  /** 사람이 내릴 결정 (원본 전역 S). 대기 수는 `pending(S)` · `usePending()`. */
  S: Decisions;
  /** 대시보드에서 고른 항목 (원본 dSel). 처음 'appr'. */
  dSel: DSel;
  /** 점검 대상 주소(origin). 처음 'https://shop.example.com'. host 는 `curHost(target)` · `useCurHost()`. */
  target: string;
  /** 새 점검 주소 채우기 요청 (원본 nPrefill). */
  prefill: Prefill | null;
  /** 레일의 새 점검 줄 단계 (원본 railNew). */
  railNew: RailPhase;
  /** 자산 탐색 재생 상태 (원본 crawl). */
  crawl: CrawlState;
  /** 범위 화면에서 바꾼 엔드포인트 포함 여부 (EP 위치 → on). 읽기는 `epOn(e, scopeOn)`. */
  scopeOn: Readonly<Record<number, boolean>>;
  /** 범위 화면의 '선택한 범위로 점검 시작'을 눌렀음 → 다음 진행 화면 진입 때 처음부터 (원본 liveFresh). */
  liveFresh: boolean;
  /** 점검 진행 재생 상태 (원본 live). 처음은 liveReset('resume'). */
  live: LiveState;
  /** 보고서 대상 (원본 rHost + setReportHost 가 그린 값). */
  report: ReportSnap;
  /** 강조색 시안. */
  accent: Accent;
}

export interface WbActions {
  /** 결제 요청 승인/거부 (원본 data-appr 클릭). */
  setAppr: (v: Appr) => void;
  /** 새 경로 포함/제외 (원본 data-pathd 클릭). */
  setPath: (v: PathDecision) => void;
  /** 대시보드 선택만 바꿈. 화면 이동·서랍까지 하려면 `useShell().dashPick(id)`. */
  setDSel: (id: DSel) => void;
  /** 새 점검 주소 채우기 요청 (원본 nPrefill). 보통 뒤이어 `goTo("new")`. */
  nPrefill: (url: string) => void;
  /**
   * 새 점검 시작 (원본 startScan 의 상태 부분): target=origin, crawl.started=false, railNew('crawl').
   * 바로 뒤에 `goTo("crawl")` 을 불러야 한다 — 실제 재시작(crawlReset + 300ms 뒤 첫 단계)은 자산 탐색 진입(crawlEnter) 때 일어난다.
   */
  startScan: (origin: string) => void;
  /** 레일 새 점검 줄 단계 바꾸기 (원본 railNew). */
  setRailNew: (phase: RailPhase) => void;
  /** 자산 탐색 화면에 들어옴 (원본 crawlEnter, 라우터가 부름): 시작 안 했으면 재시작 + railNew('crawl') + 300ms 뒤 시작. */
  crawlEnter: () => void;
  /** 처음부터 다시 (원본 crawlReset). 타이머도 지운다. */
  crawlReset: () => void;
  /** 한 단계 재생 (엔진이 부름). 끝나면 railNew('crawl'). */
  crawlStep: (opts?: CrawlStepOpts) => void;
  /** 일시정지/다시 시작 (원본 cPause). 끝났으면 무시. railNew('crawl'). */
  crawlTogglePause: () => void;
  /** 점검 취소 (원본 cCancel 의 상태 부분). 뒤이어 `goTo("dashboard")`. */
  crawlCancel: () => void;
  /** '범위 확인' (원본 cNext): 탐색이 끝났을 때만 railNew('scope'). 링크(href="#scope")가 화면을 옮긴다. */
  crawlNext: () => void;
  /** 찾은 경로 보기 바꾸기 (원본 cView). */
  crawlSetView: (view: CrawlView) => void;
  /** 찾은 경로 거르기 (원본 cKf). */
  crawlSetFilter: (kf: CrawlFilter) => void;
  /** 주소 구조 마디 펼침 (원본 data-smt). */
  crawlSetSmOpen: (key: string, open: boolean) => void;
  /** 엔드포인트 포함 여부 바꾸기 (원본 data-son 체크). lock 인 것은 화면에서 막는다. */
  setEpOn: (i: number, on: boolean) => void;
  /** '선택한 범위로 점검 시작' (원본 sStart): railNew(null), 탐색 타이머 해제, liveFresh=true. 링크(href="#live")가 화면을 옮긴다. */
  scopeStart: () => void;
  /** 진행 화면에 들어옴 (원본 liveEnter, 라우터가 부름): liveFresh 면 지금 시각으로 처음부터, 그리고 멈춘 게 아니면 재생. */
  liveEnter: (startSec?: number) => void;
  /** 원본 liveReset(mode). 'fresh' 의 시작 시각은 startSec(기본 지금). */
  liveReset: (mode: LiveMode, startSec?: number) => void;
  /** 한 작업 재생 (엔진이 부름). */
  liveStep: (opts?: LiveStepOpts) => void;
  /** 일시정지/다시 시작 (원본 lPause). 끝났거나 종료했으면 무시. */
  liveTogglePause: () => void;
  /** 점검 종료 확정 (원본 lEndYes 의 상태 부분). */
  liveEnd: () => void;
  /** 지난 활동 전부/최근 5건 (원본 lFeedMore). */
  liveToggleFeedAll: () => void;
  /** 보고서 대상 바꾸기 (원본 data-rhost 클릭 → setReportHost). 링크의 onClick 에서 부르고 이동은 href 가 한다. */
  setReportHost: (kind: "blog" | "live") => void;
  /** 강조색 바꾸기 (localStorage 저장은 레일이 한다). */
  setAccent: (k: Accent) => void;
}

export type WbStore = WbState & WbActions;

/** 처음 상태 (원본 init 블록: liveReset('resume') 등). 테스트에서 `useWb.setState(createInitialState())` 로 되돌린다. */
export function createInitialState(): WbState {
  return {
    S: { appr: null, path: null },
    dSel: "appr",
    target: "https://shop.example.com",
    prefill: null,
    railNew: null,
    crawl: crawlInit(),
    scopeOn: {},
    liveFresh: false,
    live: liveReset("resume"),
    report: { kind: "blog", host: "blog.example.com", ended: false, start: 0, mins: 0, seq: 0 },
    accent: "petrol",
  };
}

/** 작업대 스토어. `useWb(s => s.live)` 처럼 고르고, 이벤트 안에서는 `useWb.getState().액션()` 도 된다. */
export const useWb = create<WbStore>()((set, get) => ({
  ...createInitialState(),

  setAppr: (v) => set((s) => ({ S: { ...s.S, appr: v } })),
  setPath: (v) => set((s) => ({ S: { ...s.S, path: v } })),
  setDSel: (id) => set({ dSel: id }),
  nPrefill: (url) => set((s) => ({ prefill: { url, seq: (s.prefill?.seq ?? 0) + 1 } })),
  startScan: (origin) => set((s) => ({ target: origin, crawl: { ...s.crawl, started: false }, railNew: "crawl" })),
  setRailNew: (phase) => set({ railNew: phase }),

  crawlEnter: () => {
    const c = get().crawl;
    if (c.started) return;
    set({ crawl: crawlEnter(c), railNew: "crawl" });
  },
  crawlReset: () => set((s) => ({ crawl: crawlReset(s.crawl) })),
  crawlStep: (opts) => {
    const prev = get().crawl;
    const next = crawlStep(prev, opts);
    if (next === prev) return;
    set(next.done ? { crawl: next, railNew: "crawl" } : { crawl: next });
  },
  crawlTogglePause: () => {
    const c = get().crawl;
    if (c.done) return;
    set({ crawl: crawlTogglePause(c), railNew: "crawl" });
  },
  crawlCancel: () => set((s) => ({ crawl: crawlCancel(s.crawl), railNew: null })),
  crawlNext: () => {
    if (get().crawl.done) set({ railNew: "scope" });
  },
  crawlSetView: (view) => set((s) => ({ crawl: crawlSetView(s.crawl, view) })),
  crawlSetFilter: (kf) => set((s) => ({ crawl: crawlSetFilter(s.crawl, kf) })),
  crawlSetSmOpen: (key, open) => set((s) => ({ crawl: crawlSetSmOpen(s.crawl, key, open) })),

  setEpOn: (i, on) => set((s) => ({ scopeOn: { ...s.scopeOn, [i]: on } })),
  scopeStart: () => set((s) => ({ railNew: null, crawl: { ...s.crawl, sched: null }, liveFresh: true })),

  liveEnter: (startSec) => {
    const s = get();
    let l = s.live;
    if (s.liveFresh) l = liveReset("fresh", startSec ?? nowSec(), l.run + 1);
    l = liveKick(l);
    if (l !== s.live || s.liveFresh) set({ live: l, liveFresh: false });
  },
  liveReset: (mode, startSec) => set((s) => ({ live: liveReset(mode, startSec ?? nowSec(), s.live.run + 1) })),
  liveStep: (opts) => {
    const prev = get().live;
    const next = liveStep(prev, opts);
    if (next !== prev) set({ live: next });
  },
  liveTogglePause: () => set((s) => ({ live: liveTogglePause(s.live) })),
  liveEnd: () => set((s) => ({ live: liveEnd(s.live) })),
  liveToggleFeedAll: () => set((s) => ({ live: liveToggleFeedAll(s.live) })),

  setReportHost: (kind) =>
    set((s) => {
      const seq = s.report.seq + 1;
      if (kind === "live") {
        const l = s.live;
        return {
          report: {
            kind,
            host: curHost(s.target),
            ended: l.ended && !l.done,
            start: l.start,
            mins: lelapsed(l),
            seq,
          },
        };
      }
      return { report: { kind, host: "blog.example.com", ended: false, start: 0, mins: 0, seq } };
    }),
  setAccent: (k) => set({ accent: k }),
}));

/** 아직 안 내린 결정 수 (레일 배지 · 대시보드). */
export function usePending(): number {
  return useWb((s) => pending(s.S));
}

/** 점검 대상 host (레일 · 크럼의 t-host, 진행 화면, API 예시의 Host:). */
export function useCurHost(): string {
  return useWb((s) => curHost(s.target));
}
