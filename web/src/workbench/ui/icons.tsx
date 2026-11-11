"use client";

// 아이콘: 시안의 SVG 스프라이트(<symbol id="i-…">)를 그대로 옮긴 것. 선 굵기·모양은 CSS 의 .ic 가 정한다.

import type { ReactNode } from "react";

import s from "../workbench.module.css";

const PATHS = {
  grid: (
    <>
      <rect x="3.5" y="3.5" width="7" height="7" rx="1.2" />
      <rect x="13.5" y="3.5" width="7" height="7" rx="1.2" />
      <rect x="3.5" y="13.5" width="7" height="7" rx="1.2" />
      <rect x="13.5" y="13.5" width="7" height="7" rx="1.2" />
    </>
  ),
  plus: (
    <>
      <path d="M12 5v14M5 12h14" />
    </>
  ),
  menu: (
    <>
      <path d="M4 7h16M4 12h16M4 17h16" />
    </>
  ),
  x: (
    <>
      <path d="M6 6l12 12M18 6L6 18" />
    </>
  ),
  check: (
    <>
      <path d="M5 12.5l4.5 4.5L19 7.5" />
    </>
  ),
  pause: (
    <>
      <path d="M9 6v12M15 6v12" />
    </>
  ),
  play: (
    <>
      <path d="M8 5.5v13l11-6.5z" />
    </>
  ),
  stop: (
    <>
      <rect x="6.5" y="6.5" width="11" height="11" rx="1.5" />
    </>
  ),
  chev: (
    <>
      <path d="M9 6l6 6-6 6" />
    </>
  ),
  arrow: (
    <>
      <path d="M5 12h14M13 6l6 6-6 6" />
    </>
  ),
  copy: (
    <>
      <rect x="8.5" y="8.5" width="11" height="11" rx="2" />
      <path d="M15.5 8.5V6A1.5 1.5 0 0 0 14 4.5H6A1.5 1.5 0 0 0 4.5 6v8A1.5 1.5 0 0 0 6 15.5h2.5" />
    </>
  ),
  alert: (
    <>
      <path d="M12 4l9 16H3z" />
      <path d="M12 10v4M12 17.2v.1" />
    </>
  ),
  lock: (
    <>
      <rect x="5" y="11" width="14" height="9" rx="2" />
      <path d="M8 11V8a4 4 0 0 1 8 0v3" />
    </>
  ),
  panel: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <path d="M14.5 4.5v15" />
    </>
  ),
  // 계정 버튼: 위아래로 열림
  updown: (
    <>
      <path d="M8 9.5l4-4 4 4M8 14.5l4 4 4-4" />
    </>
  ),
  logout: (
    <>
      <path d="M14 4.5H6.5a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2H14" />
      <path d="M10.5 12h9M16.5 8.5L20 12l-3.5 3.5" />
    </>
  ),
  login: (
    <>
      <path d="M10 4.5h7.5a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H10" />
      <path d="M3.5 12h10M10 8.5l3.5 3.5L10 15.5" />
    </>
  ),
  moon: (
    <>
      <path d="M19.5 14.5A7.5 7.5 0 0 1 9.5 4.5a7.5 7.5 0 1 0 10 10z" />
    </>
  ),
  // 레일 접기 · 펼치기 (왼쪽 칸 + 꺾쇠)
  foldLeft: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <path d="M9.5 4.5v15M15.5 10l-2 2 2 2" />
    </>
  ),
  unfoldLeft: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <path d="M9.5 4.5v15M13.5 10l2 2-2 2" />
    </>
  ),
  // 왼쪽 칸이 있는 패널: 레일 접기
  panelLeft: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <path d="M9.5 4.5v15" />
    </>
  ),
  refresh: (
    <>
      <path d="M20 12a8 8 0 1 1-2.34-5.66" />
      <path d="M20 4v5h-5" />
    </>
  ),
  shield: (
    <>
      <path d="M12 3.5l7 3v5.5c0 4.2-3 7.4-7 8.5-4-1.1-7-4.3-7-8.5V6.5z" />
    </>
  ),
  chip: (
    <>
      <rect x="7" y="7" width="10" height="10" rx="1.5" />
      <path d="M10 3v4M14 3v4M10 17v4M14 17v4M3 10h4M3 14h4M17 10h4M17 14h4" />
    </>
  ),
  info: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 11v5M12 7.8v.1" />
    </>
  ),
} satisfies Record<string, ReactNode>;

export type IconName = keyof typeof PATHS;

export function Icon({ name, className }: { name: IconName; className?: string }) {
  return (
    <svg className={className ? `${s.ic} ${className}` : s.ic} viewBox="0 0 24 24" aria-hidden="true">
      {PATHS[name]}
    </svg>
  );
}

/** 레일 맨 위 로고. */
export function BrandMark() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d="M4 19L4 12C4 7 7.6 3.5 12 3.5C16.4 3.5 20 7 20 12L20 19L18 21L16 19L14 21L12 19L10 21L8 19L6 21Z"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinejoin="round"
      />
      <path d="M9.3 8.4L9.3 10.4M14.7 8.4L14.7 10.4" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
      <path
        d="M12 12L15 14.6L12 18.4L9 14.6ZM9.4 14.6L14.6 14.6"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinejoin="round"
      />
    </svg>
  );
}
