"use client";

// 작업대 껍데기(라우터·서랍) 컨텍스트. Workbench 가 값을 채우고 화면·부품은 useShell() 로 쓴다.
// Workbench.tsx 와 분리한 까닭: 화면이 Workbench 를 import 하면 순환 import 가 된다.

import { createContext, useContext } from "react";

import type { DSel } from "./store";

/** 여섯 화면 (해시 이름 = data-view). */
export type View = "dashboard" | "new" | "crawl" | "scope" | "live" | "report";
export const VIEWS: readonly View[] = ["dashboard", "new", "crawl", "scope", "live", "report"];
export function isView(h: string): h is View {
  return (VIEWS as readonly string[]).includes(h);
}

export interface Shell {
  /** 지금 화면. 처음(하이드레이션 전) 은 'dashboard'. */
  route: View;
  /** 라우팅할 때마다 1씩 오른다(같은 화면으로 다시 가도). 화면 진입 처리를 effect 로 걸 때 의존값으로 쓴다. */
  navSeq: number;
  /** 넓지 않은 화면 (max-width:1199px) — 상세 패널이 서랍 */
  narrow: boolean;
  /** 휴대폰 (max-width:759px) — 레일이 서랍 */
  phone: boolean;
  /** prefers-reduced-motion: 나타나는 효과(fresh) 클래스를 붙이지 않는다 */
  reduce: boolean;
  /** 지금 화면의 상세 서랍이 열림 (section 에 io 클래스) */
  inspOpen: boolean;
  /** 레일 서랍이 열림 */
  railOpen: boolean;
  /** 레일 전용: 마지막 라우팅 순간에 새 점검 줄이 보였는가 (원본 route() 의 aria-current 계산 시점) */
  railNewAtRoute: boolean;
  /** 화면 이동. 이미 그 해시면 다시 라우팅(서랍 닫기·맨 위로·h1 포커스). 원본 goTo. */
  goTo: (name: View) => void;
  /** 상세 서랍 열기 (넓은 화면에서는 아무것도 안 함). 30ms 뒤 `.vi [data-insp-close]` 첫 버튼에 포커스. 원본 openInsp. */
  openInsp: () => void;
  /** 상세 서랍 닫기. 원본 closeInsp. */
  closeInsp: () => void;
  /** 레일 서랍 열기 (30ms 뒤 닫기 버튼에 포커스). 원본 openRail. */
  openRail: () => void;
  /** 레일 서랍 닫기. 원본 closeRail. */
  closeRail: () => void;
  /**
   * 대시보드 항목 고르기 (원본 dashPick): dSel 을 바꾸고, 대시보드가 아니면 대시보드로 이동한 뒤 상세 서랍을 연다(openAfter).
   * 대시보드면 레일을 닫고 `[data-dsel="id"]` 줄을 보이게 스크롤한 뒤 서랍을 연다 → 대시보드 줄에는 data-dsel 속성을 남길 것.
   */
  dashPick: (id: DSel) => void;
}

const noop = () => undefined;

/** Workbench 밖(테스트에서 화면만 그릴 때 등)의 기본값. */
export const ShellContext = createContext<Shell>({
  route: "dashboard",
  navSeq: 0,
  narrow: false,
  phone: false,
  reduce: false,
  inspOpen: false,
  railOpen: false,
  railNewAtRoute: false,
  goTo: noop,
  openInsp: noop,
  closeInsp: noop,
  openRail: noop,
  closeRail: noop,
  dashPick: noop,
});

/** 라우터·서랍 제어. 화면 컴포넌트와 공통 부품에서 쓴다. */
export function useShell(): Shell {
  return useContext(ShellContext);
}
