// 점검 진행 재생 상태와 순수 전이 함수. 원본 전역 live 객체와 liveReset · liveApply · liveSchedule · liveStep,
// 그리고 lpct · lremain · lcur · livePill · vcount 같은 계산.
// 타이머는 여기서 돌리지 않는다: 예약(sched)만 상태에 남기고 엔진(useLiveEngine)이 setTimeout 을 건다.

import type { FindingId, LiveVerdict } from "../data/findings";
import { LCUM, LIVE_MS, type LiveTask, LRESUME, LT, LTOTAL } from "../data/live";
import { type Sched, sched } from "./crawlState";
import { hms } from "./time";

/** 지난 활동 한 줄 (원본 live.feed 항목). 최신이 앞. */
export interface FeedItem {
  /** LT 위치 */
  readonly k: number;
  /** 끝난 시각 "HH:MM:SS" */
  readonly at: string;
  readonly t: LiveTask;
}

/** 원본 전역 `live` 객체 (timer → sched). */
export interface LiveState {
  /** 다음(지금) 작업 위치 */
  readonly i: number;
  /** 타이머가 걸려 재생 중 */
  readonly playing: boolean;
  readonly paused: boolean;
  /** 사람이 종료함 */
  readonly ended: boolean;
  /** 계획을 모두 마침 */
  readonly done: boolean;
  /** 다음 작업 예약 (null = 타이머 없음) */
  readonly sched: Sched | null;
  /** 지난 활동 전부 보기 */
  readonly feedAll: boolean;
  readonly feed: readonly FeedItem[];
  /** 발견별 지금 판정 */
  readonly fv: Readonly<Partial<Record<FindingId, LiveVerdict>>>;
  /** 발견 순서 (찾은 순) */
  readonly forder: readonly FindingId[];
  readonly sent: number;
  readonly blk: number;
  readonly ep: number;
  readonly hold: number;
  /** /admin/export 결정이 필요해짐 */
  readonly decide: boolean;
  /** 방금 끝나 나타나는 효과를 줄 지난 활동의 k (-1 = 없음). 엔진이 !reduce && 진행 화면일 때만 넣는다 */
  readonly fresh: number;
  /** 시작 시각 (하루 기준 초) */
  readonly start: number;
  /** 재시작 횟수. 화면이 바뀌면 선택한 발견(lSel)을 지우는 등 (원본 liveReset 의 lSel=null) */
  readonly run: number;
}

export type LiveMode = "resume" | "fresh";

/** 원본 `liveApply(k)`: 작업 k 의 결과를 반영. */
export function liveApply(l: LiveState, k: number): LiveState {
  const t = LT[k];
  let forder = l.forder;
  let fv = l.fv;
  if (t.f) {
    forder = [...forder, t.f];
    fv = { ...fv, [t.f]: t.fv ?? "판정 중" };
  }
  if (t.v) fv = { ...fv, [t.v[0]]: t.v[1] };
  return {
    ...l,
    feed: [{ k, at: hms(l.start + Math.round(LCUM[k + 1] * 60)), t }, ...l.feed],
    sent: l.sent + (t.sent ?? 0),
    blk: l.blk + (t.blk ?? 0),
    ep: l.ep + (t.ep ?? 0),
    hold: l.hold + (t.hold ?? 0),
    decide: l.decide || !!t.decide,
    forder,
    fv,
  };
}

/**
 * 원본 `liveReset(mode)`: 'resume' 은 10:42 시작 · 작업 LRESUME(약 58%)까지 반영한 상태,
 * 'fresh' 는 지금 시각(startSec) 시작 · 처음부터. 타이머는 지운다.
 */
export function liveReset(mode: LiveMode, startSec = 10 * 3600 + 42 * 60, run = 0): LiveState {
  let l: LiveState = {
    i: 0,
    playing: false,
    paused: false,
    ended: false,
    done: false,
    sched: null,
    feedAll: false,
    feed: [],
    fv: {},
    forder: [],
    sent: 40,
    blk: 3,
    ep: 37,
    hold: 0,
    decide: false,
    fresh: -1,
    start: mode === "fresh" ? startSec : 10 * 3600 + 42 * 60,
    run,
  };
  const upto = mode === "fresh" ? 0 : LRESUME;
  for (let k = 0; k < upto; k++) l = liveApply(l, k);
  return { ...l, i: upto, fresh: -1 };
}

/** 원본 `liveSchedule()`: 끝났거나 멈췄으면 재생 안 함, 아니면 지금 작업 시간만큼 뒤에 다음 작업. */
export function liveSchedule(l: LiveState): LiveState {
  if (l.done || l.ended || l.paused) return { ...l, playing: false, sched: null };
  return { ...l, playing: true, sched: sched(Math.max(400, Math.round(LT[l.i].w * LIVE_MS))) };
}

export interface LiveStepOpts {
  /** 새 지난 활동 줄에 나타나는 효과를 줄지 (엔진: !reduce && route === "live") */
  fresh: boolean;
}

/** 원본 `liveStep()`: 지금 작업을 끝내고 다음으로. 다 끝나면 done. */
export function liveStep(l: LiveState, o: LiveStepOpts = { fresh: false }): LiveState {
  if (l.paused || l.ended || l.done) return l;
  const k = l.i;
  const a = liveApply(l, k);
  const i = k + 1;
  if (i >= LT.length) return { ...a, i, fresh: o.fresh ? k : -1, done: true, playing: false, sched: null };
  return liveSchedule({ ...a, i, fresh: o.fresh ? k : -1 });
}

/** 원본 `liveEnter()` 의 뒷부분: 재생 중이 아니고 끝나거나 멈추지 않았으면 재생 시작. */
export function liveKick(l: LiveState): LiveState {
  if (!l.playing && !l.done && !l.ended && !l.paused) return liveSchedule(l);
  return l;
}

/** 원본 lPause 클릭. */
export function liveTogglePause(l: LiveState): LiveState {
  if (l.done || l.ended) return l;
  const paused = !l.paused;
  const next = { ...l, paused, fresh: -1 };
  return paused ? { ...next, playing: false, sched: null } : liveSchedule(next);
}

/** 원본 lEndYes 클릭: 종료. */
export function liveEnd(l: LiveState): LiveState {
  return { ...l, ended: true, playing: false, sched: null, fresh: -1 };
}

/** 원본 lFeedMore 클릭. */
export function liveToggleFeedAll(l: LiveState): LiveState {
  return { ...l, feedAll: !l.feedAll, fresh: -1 };
}

// ---------- 계산 (원본 lpct · lremain · lcur · livePill · vcount) ----------

/** 진행률(%). 원본 `lpct()`. */
export function lpct(l: LiveState): number {
  return l.done ? 100 : Math.round((LCUM[l.i] / LTOTAL) * 100);
}

/** 남은 시간(분, 최소 1). 원본 `lremain()`. */
export function lremain(l: LiveState): number {
  return Math.max(1, Math.ceil(LTOTAL - LCUM[l.i]));
}

/** 지금 하는 작업 (끝났거나 종료했으면 null). 원본 `lcur()`. */
export function lcur(l: LiveState): LiveTask | null {
  return l.done || l.ended ? null : LT[l.i];
}

/** 경과(분). 원본 lMeta 의 `Math.round(LCUM[live.i])`. */
export function lelapsed(l: LiveState): number {
  return Math.round(LCUM[l.i]);
}

/** 대시보드·레일의 상태 알약. cls 는 CSS 클래스 이름("" 이면 기본 pill). 원본 `livePill()`. */
export function livePill(l: LiveState): { cls: "ok" | "run" | ""; label: string } {
  if (l.done) return { cls: "ok", label: "점검 완료" };
  if (l.ended) return { cls: "", label: "종료됨" };
  if (l.paused) return { cls: "", label: "일시정지" };
  return { cls: "run", label: "점검 중" };
}

const VORDER: readonly LiveVerdict[] = ["확정", "의심", "판단 불가", "판정 중"];

/** 판정별 개수 (0 인 것은 뺌, 순서: 확정 · 의심 · 판단 불가 · 판정 중). */
export function vcounts(l: LiveState): Array<[LiveVerdict, number]> {
  const v: Record<LiveVerdict, number> = { 확정: 0, 의심: 0, "판단 불가": 0, "판정 중": 0 };
  for (const id of l.forder) {
    const x = l.fv[id];
    if (x) v[x]++;
  }
  return VORDER.filter((k) => v[k]).map((k) => [k, v[k]]);
}

/** "확정 3 · 의심 2 · 판단 불가 1". 원본 `vcount()`. */
export function vcount(l: LiveState): string {
  return vcounts(l)
    .map(([k, n]) => `${k} ${n}`)
    .join(" · ");
}

/** 레일 '진행 중' 의 점검(shop) 줄. 원본 liveRender 끝부분 (railShopDot · railShopSub · railShopBar). */
export function railShop(l: LiveState): { dot: "ok" | "stop" | "wait" | "run"; sub: string; pct: number } {
  const pct = lpct(l);
  if (l.done) return { dot: "ok", sub: `점검 완료 · 발견 ${l.forder.length}`, pct };
  if (l.ended) return { dot: "stop", sub: `종료됨 · ${pct}%`, pct };
  return { dot: l.paused ? "wait" : "run", sub: `${l.paused ? "일시정지" : "점검 중"} · ${pct}%`, pct };
}
