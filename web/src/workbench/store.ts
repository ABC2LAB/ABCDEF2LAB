// 화면 선호·URL 입력 보조만 보관한다. 진단 상태·발견·사용량은 서버 스토어가 소유한다.
import { create } from "zustand";
export type Accent = "petrol" | "cobalt" | "mono";
export type DSel = string;
/** 화면 모드: 기기 설정을 따름 · 라이트 · 다크 (고르면 이 브라우저에 기억) */
export type Theme = "system" | "light" | "dark";
/** 넓은 화면에서 64px 줄로 접을 수 있는 패널: 왼쪽 레일 · 오른쪽 상세. 좁은 화면의 서랍과는 별개다. */
export type Panel = "rail" | "insp";
export const createInitialState = () => ({
  accent: "petrol" as Accent,
  theme: "system" as Theme,
  prefill: null,
  railMin: false,
  inspMin: false,
});
export const useWb = create<{
  accent: Accent;
  theme: Theme;
  prefill: { url: string; seq: number } | null;
  /** 넓은 화면에서 레일을 아이콘 줄(64px)로 접었나 (시안 rail-min, 이 브라우저에 기억) */
  railMin: boolean;
  /** 넓은 화면에서 상세 패널을 64px 줄로 접었나 (시안 insp-min, 이 브라우저에 기억) */
  inspMin: boolean;
  setAccent: (accent: Accent) => void;
  setTheme: (theme: Theme) => void;
  nPrefill: (url: string) => void;
  setPanelMin: (panel: Panel, min: boolean) => void;
}>((set) => ({
  accent: "petrol",
  theme: "system",
  prefill: null,
  railMin: false,
  inspMin: false,
  setAccent: (accent) => set({ accent }),
  setTheme: (theme) => set({ theme }),
  nPrefill: (url) => set((st) => ({ prefill: { url, seq: (st.prefill?.seq ?? 0) + 1 } })),
  setPanelMin: (panel, min) => set(panel === "rail" ? { railMin: min } : { inspMin: min }),
}));
