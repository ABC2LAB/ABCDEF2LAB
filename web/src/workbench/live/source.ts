// 데이터 원천(예시 재생 demo / 실제 엔진 live)과 live 에서 고른 점검(task).
// 기본은 demo. MOCK 빌드에서는 live 를 고를 수 없다. 저장값은 마운트 뒤 effect 에서만 읽는다(SSR 안전).

import { useEffect } from "react";

import { create } from "zustand";

import { MOCK } from "@/abc2lab-ui/lib/mock";

/** 데이터 원천. demo = 시안 각본 재생, live = 실제 엔진 API. */
export type Source = "demo" | "live";

/** 사용자가 고른 원천을 기억하는 localStorage 키. */
export const SOURCE_KEY = "abc2lab-wb-source";

/** 이 빌드에서 live 를 고를 수 있는가 (NEXT_PUBLIC_MOCK=1 빌드면 false). */
export function canGoLive(): boolean {
  return !MOCK;
}

/** 원천 스토어 모양. */
export interface SourceState {
  /** 지금 원천. 처음은 "demo". */
  source: Source;
  /** live 에서 고른 점검 id (없으면 null). 자산 탐색·범위·점검 진행·보고서가 이 점검을 보여 준다. */
  taskId: string | null;
  /**
   * 원천 바꾸기 + localStorage 저장(실패해도 무시). MOCK 빌드에서 "live" 는 무시하고 demo 로 둔다.
   * 화면 이동은 하지 않는다(지금 route 그대로).
   */
  setSource: (source: Source) => void;
  /** 점검 고르기 (null = 선택 해제). 화면 이동은 호출부가 `useShell().goTo` 로 한다. */
  selectTask: (id: string | null) => void;
}

/** 원천·선택 스토어. `useSource((st) => st.source)` 처럼 원시값을 고른다. */
export const useSource = create<SourceState>()((set) => ({
  source: "demo",
  taskId: null,
  setSource: (source) => {
    const next: Source = source === "live" && canGoLive() ? "live" : "demo";
    set({ source: next });
    try {
      localStorage.setItem(SOURCE_KEY, next);
    } catch {
      // 저장소를 못 쓰면 이번만 적용
    }
  },
  selectTask: (id) => set({ taskId: id }),
}));

/** 지금 원천이 live 인가 (구독). */
export function useIsLive(): boolean {
  return useSource((st) => st.source === "live");
}

/**
 * 저장해 둔 원천을 읽어 적용한다. Workbench 에서 한 번 부른다.
 * MOCK 빌드면 저장값과 상관없이 demo. 저장값이 없거나 이상하면 지금 값을 그대로 둔다.
 */
export function useSourceBoot(): void {
  useEffect(() => {
    if (!canGoLive()) {
      if (useSource.getState().source !== "demo") useSource.setState({ source: "demo" });
      return;
    }
    let saved: string | null = null;
    try {
      saved = localStorage.getItem(SOURCE_KEY);
    } catch {
      // 저장소를 못 쓰면 기본값
    }
    if (saved === "live" || saved === "demo") useSource.setState({ source: saved });
  }, []);
}
