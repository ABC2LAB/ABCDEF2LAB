"use client";

import { create } from "zustand";

import { useUrl } from "./urlDiagnosis";

/**
 * 보고서 오른쪽 패널 머리 '상세 보기'로 연 발견(어느 task 의 것인지 함께). 보고서 안 상세 페이지와 단독 발견 상세(#finding)가
 * 같은 값을 본다. 현재 Job 의 task 와 다르면 쓰지 않고, task 가 바뀌거나 Job 이 빠지면(전환 · 권한 철회 · 재로그인) 지운다 —
 * 이전 회차 발견을 열지 않게.
 */
type FindingPick = { task: string | null; entry: string | null };

export const useFindingPick = create<FindingPick>()(() => ({ task: null, entry: null }));

export function pickFinding(task: string, entry: string): void {
  useFindingPick.setState({ task, entry });
}

export function clearFindingPick(): void {
  useFindingPick.setState({ task: null, entry: null });
}

/** 현재 task 에서 고른 발견 ID. 다른 task 에서 고른 것이면 null. */
export function usePickedFinding(task: string | null): string | null {
  return useFindingPick((st) => (task && st.task === task ? st.entry : null));
}

useUrl.subscribe((st, prev) => {
  const task = st.job?.task_id ?? null;
  if (task !== (prev.job?.task_id ?? null) && useFindingPick.getState().task !== task) clearFindingPick();
});
