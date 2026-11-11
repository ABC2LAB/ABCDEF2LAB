// 화면 선호·URL 입력 보조만 보관한다. 진단 상태·발견·사용량은 서버 스토어가 소유한다.
import { create } from "zustand";
export type Accent = "petrol" | "cobalt" | "mono";
export type DSel = string;
/** 넓은 화면에서 접을 수 있는 패널: 왼쪽 레일 · 오른쪽 상세. 좁은 화면의 서랍과는 별개다. */
export type Panel = "rail" | "insp";
export const createInitialState = () => ({
  accent: "petrol" as Accent,
  prefill: null,
  railHidden: false,
  inspHidden: false,
});
export const useWb = create<{
  accent: Accent;
  prefill: { url: string; seq: number } | null;
  /** 넓은 화면에서 레일을 접었나 (이 브라우저에 기억) */
  railHidden: boolean;
  /** 넓은 화면에서 상세 패널을 접었나 (이 브라우저에 기억) */
  inspHidden: boolean;
  setAccent: (accent: Accent) => void;
  nPrefill: (url: string) => void;
  setPanelHidden: (panel: Panel, hidden: boolean) => void;
}>((set) => ({
  accent: "petrol",
  prefill: null,
  railHidden: false,
  inspHidden: false,
  setAccent: (accent) => set({ accent }),
  nPrefill: (url) => set((st) => ({ prefill: { url, seq: (st.prefill?.seq ?? 0) + 1 } })),
  setPanelHidden: (panel, hidden) => set(panel === "rail" ? { railHidden: hidden } : { inspHidden: hidden }),
}));
