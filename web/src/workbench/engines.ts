"use client";

// 재생 엔진: 스토어에 남은 예약(sched)을 보고 setTimeout 을 건다. Workbench 에서 한 번씩만 부른다.
// 화면이 숨겨져도 계속 돌고(원본처럼), 예약이 바뀌거나 언마운트되면 타이머를 지운다(StrictMode 이중 마운트에도 안전).

import { useEffect, useRef } from "react";

import type { View } from "./shell";
import { useWb } from "./store";

/** 원본 schedule()/step(): crawl.sched 가 있으면 그 ms 뒤에 crawlStep. 새 로그 줄의 나타남 효과는 자산 탐색 화면일 때만. */
export function useCrawlEngine(route: View, reduce: boolean): void {
  const sched = useWb((s) => s.crawl.sched);
  const rowFresh = useRef(false);
  useEffect(() => {
    rowFresh.current = !reduce && route === "crawl";
  }, [route, reduce]);
  useEffect(() => {
    if (!sched) return;
    const t = window.setTimeout(() => useWb.getState().crawlStep({ rowFresh: rowFresh.current }), sched.ms);
    return () => window.clearTimeout(t);
  }, [sched]);
}

/** 원본 liveSchedule()/liveStep(): live.sched 가 있으면 그 ms 뒤에 liveStep. 새 활동 줄의 나타남 효과는 진행 화면일 때만. */
export function useLiveEngine(route: View, reduce: boolean): void {
  const sched = useWb((s) => s.live.sched);
  const fresh = useRef(false);
  useEffect(() => {
    fresh.current = !reduce && route === "live";
  }, [route, reduce]);
  useEffect(() => {
    if (!sched) return;
    const t = window.setTimeout(() => useWb.getState().liveStep({ fresh: fresh.current }), sched.ms);
    return () => window.clearTimeout(t);
  }, [sched]);
}
