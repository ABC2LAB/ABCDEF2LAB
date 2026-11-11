"use client";

// 왼쪽 레일: 새 점검 · 대시보드 · 이어 보는 점검 · 분석 모델 · 로그인 · 강조색 시안. 시안(aside#rail)과 같은 짜임.
// 점검 단계(자산 탐색 · 범위 확인 · 점검 진행 · 보고서)는 레일이 아니라 각 화면 머리의 단계 표시로 옮겨 다닌다.

import { cx } from "../lib/cx";
import { LiveRailAccount, LiveRailBadge, LiveRailModel, LiveRailTasks } from "../live/rail";
import { useShell } from "../shell";
import { type Accent, useWb } from "../store";
import s from "../workbench.module.css";
import { BrandMark, Icon } from "./icons";

const SWATCHES: ReadonlyArray<readonly [Accent, string, string]> = [
  ["petrol", "#0F6D8A", "페트롤"],
  ["cobalt", "#2457D6", "코발트"],
  ["mono", "#E3EBEE", "모노"],
];

/** 강조색을 고르고 이 브라우저에만 기억 (원본 setAccent). */
function pickAccent(k: Accent) {
  useWb.getState().setAccent(k);
  try {
    localStorage.setItem("abc2lab-accent", k);
  } catch {
    // 저장소를 못 쓰면 이번만 적용
  }
}

export function Rail() {
  const { route, railOpen, closeRail } = useShell();
  const accent = useWb((st) => st.accent);
  return (
    <aside className={cx(s.rail, railOpen && s.open)} id="rail" aria-label="주 메뉴">
      <div className={s["rail-top"]}>
        <a className={s.brand} href="#dashboard">
          <BrandMark />
          ABC2LAB
        </a>
        <button
          className={cx(s["icon-btn"], s["rail-x"])}
          type="button"
          data-rail-close=""
          aria-label="메뉴 닫기"
          onClick={closeRail}
        >
          <Icon name="x" />
        </button>
      </div>
      <a className={s["new-btn"]} href="#new" aria-current={route === "new" ? "page" : undefined}>
        <Icon name="plus" />새 점검
      </a>
      <nav className={s.rnav} aria-label="화면">
        <a className={s.rl} href="#dashboard" aria-current={route === "dashboard" ? "page" : undefined}>
          <Icon name="grid" />
          대시보드
          <LiveRailBadge />
        </a>
      </nav>
      <LiveRailTasks />
      <div className={s["rail-foot"]}>
        <LiveRailModel />
        <LiveRailAccount />
        {/* biome-ignore lint/a11y/useSemanticElements: 원본 마크업 그대로 (fieldset 으로 바꾸면 모양이 달라짐) */}
        <div className={s.swatches} role="group" aria-label="강조색 시안">
          <span>강조색 시안</span>
          <div className={s["sw-row"]}>
            {SWATCHES.map(([k, color, label]) => (
              <button key={k} className={s.sw} type="button" aria-pressed={accent === k} onClick={() => pickAccent(k)}>
                <i style={{ background: color }} />
                {label}
              </button>
            ))}
          </div>
        </div>
      </div>
    </aside>
  );
}
