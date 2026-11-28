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
  doc: (
    <>
      <path d="M13.5 3.5H7A1.5 1.5 0 0 0 5.5 5v14A1.5 1.5 0 0 0 7 20.5h10a1.5 1.5 0 0 0 1.5-1.5V8.5z" />
      <path d="M13.5 3.5v5h5M9 12.5h6M9 16h6" />
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
  // 상세 패널 접기 · 펼치기 (오른쪽 칸 + 꺾쇠)
  foldRight: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <path d="M14.5 4.5v15M8.5 10l2 2-2 2" />
    </>
  ),
  unfoldRight: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      <path d="M14.5 4.5v15M10.5 10l-2 2 2 2" />
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

/** 레일 맨 위 로고: 진돗개 옆얼굴 실루엣. 귀 안쪽 · 눈 · 목줄 틈은 evenodd 구멍으로 뚫는다. */
export function BrandMark() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" aria-hidden="true">
      <path
        fill="currentColor"
        fillRule="evenodd"
        d="M11.22 0L12.33 5.07L12.46 5.2L16.1 5.92L18.05 7.61L18.44 8.85L22.47 9.95L21.24 10.28L21.17 10.6L22.02 11.71L22.67 12.03L22.41 12.68L21.76 13.59L20.85 14.18L16.88 14.89L15.45 15.48L14.8 16.13L13.76 18.86L12.46 18.47L11.74 20.49L12.85 20.81L12.91 21.07L11.74 24L2.44 16.13L1.98 15.61L1.33 15.22L1.66 14.63L1.85 14.57L3.41 15.93L6.47 18.02L9.46 19.58L10.83 20.16L10.96 20.1L11.74 18.28L11.61 18.08L10.18 17.56L7.32 16L4.13 13.85L3.02 12.94L3.02 12.68L6.6 7.61L8.94 3.51ZM10.7 2.54L9.07 5.4L8.94 7.74L11.35 5.79L11.02 4.1L11.09 3.77L10.96 3.71ZM14.15 8.13L14.08 8.26L15.32 9.43L17.4 9.5L15.84 8.26Z"
      />
    </svg>
  );
}
