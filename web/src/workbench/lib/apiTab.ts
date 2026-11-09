// API 상세의 탭(기본 정보 · 요청·응답 예시). 원본 전역 apiTab:
// 범위 확인의 상세와 자산 탐색의 API 상세가 같은 값을 쓴다(한쪽에서 고른 탭으로 다른 쪽도 열림).
// 기반 스토어(store.ts)를 고치지 않으려고 따로 둔 작은 스토어다 — 통합 때 store.ts 로 옮겨도 된다.

import { create } from "zustand";

export type ApiTab = "info" | "ex";

interface ApiTabStore {
  tab: ApiTab;
  setTab: (tab: ApiTab) => void;
}

/** `useApiTab(t => t.tab)`. 테스트에서는 `useApiTab.setState({ tab: "info" })` 로 되돌린다. */
export const useApiTab = create<ApiTabStore>()((set) => ({
  tab: "info",
  setTab: (tab) => set({ tab }),
}));
