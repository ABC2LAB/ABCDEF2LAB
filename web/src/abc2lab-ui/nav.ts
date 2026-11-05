import { createContext, useContext } from "react";

/** 원본 HTML의 data-screen 값과 같아요 (URL 해시로도 쓰여요: #entry, #report …) */
export const SCREENS = [
  "dashboard",
  "entry",
  "target",
  "job",
  "crawl",
  "scope",
  "live",
  "history",
  "report",
  "finding",
  "finding-und",
  "approval",
  "settings",
  "audit",
  "access-map",
  "tokens",
] as const;

export type ScreenId = (typeof SCREENS)[number];

export function isScreenId(value: string): value is ScreenId {
  return (SCREENS as readonly string[]).includes(value);
}

export type NavValue = {
  screen: ScreenId;
  go: (screen: ScreenId) => void;
};

export const NavContext = createContext<NavValue | null>(null);

export function useNav(): NavValue {
  const value = useContext(NavContext);
  if (!value) throw new Error("useNav는 <ABC2LabUI> 안에서만 쓸 수 있어요.");
  return value;
}

/** 원본의 data-go="…" 역할: go('report') 처럼 화면을 바꿔요 */
export function useGo(): NavValue["go"] {
  return useNav().go;
}
