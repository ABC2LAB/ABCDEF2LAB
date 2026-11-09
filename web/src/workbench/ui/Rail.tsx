"use client";

// 왼쪽 레일: 메뉴 · 점검 목록 · 운영 상태 · 강조색 시안. 원본 aside#rail 과 그것을 고치던 syncDecisions · railNew · liveRender 부분.
// live(실제 엔진)일 때는 배지 · 점검 목록 · 분석 모델 줄만 live/rail.tsx 조각으로 바꿔 끼운다(demo 는 그대로).

import { cx } from "../lib/cx";
import { LiveRailAccount, LiveRailBadge, LiveRailModel, LiveRailTasks } from "../live/rail";
import type { View } from "../shell";
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
  const { route, railOpen, railNewAtRoute, closeRail, dashPick } = useShell();
  const accent = useWb((st) => st.accent);
  const cur = (nav: readonly View[]) => (nav.includes(route) ? "page" : undefined);
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
      <a className={s["new-btn"]} href="#new" aria-current={cur(["new"])}>
        <Icon name="plus" />새 점검
      </a>
      <nav className={s.rnav} aria-label="화면">
        <a className={s.rl} href="#dashboard" aria-current={cur(["dashboard"])}>
          <Icon name="grid" />
          대시보드
          <LiveRailBadge />
        </a>
        {(
          [
            ["crawl", "자산 탐색"],
            ["scope", "범위 확인"],
            ["live", "점검 진행"],
            ["report", "보고서"],
          ] as const
        ).map(([view, label]) => (
          <a key={view} className={s.rl} href={`#${view}`} aria-current={cur([view])}>
            {label}
          </a>
        ))}
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
        <div className={s.mock}>실제 서버 응답 · 미지원 기능은 제공되지 않습니다</div>
      </div>
    </aside>
  );
}
