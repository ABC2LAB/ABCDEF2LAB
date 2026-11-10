// 화면 선호·URL 입력 보조만 보관한다. 진단 상태·발견·사용량은 서버 스토어가 소유한다.
import { create } from "zustand";
export type Accent = "petrol" | "cobalt" | "mono";
export type DSel = string;
export const createInitialState = () => ({ accent: "petrol" as Accent, prefill: null });
export const useWb = create<{
  accent: Accent;
  prefill: { url: string; seq: number } | null;
  setAccent: (accent: Accent) => void;
  nPrefill: (url: string) => void;
}>((set) => ({
  accent: "petrol",
  prefill: null,
  setAccent: (accent) => set({ accent }),
  nPrefill: (url) => set((st) => ({ prefill: { url, seq: (st.prefill?.seq ?? 0) + 1 } })),
}));
