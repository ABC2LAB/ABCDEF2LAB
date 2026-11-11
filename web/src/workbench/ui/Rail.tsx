"use client";

// 왼쪽 레일: 새 점검 · 대시보드 · 이어 보는 점검(진행 중 · 끝난 점검) · 맨 아래 계정 버튼. 시안(aside#rail)과 같은 짜임.
// 분석 모델 · 진단 모드 · 다크 모드 · 강조색 시안 · 로그아웃은 계정 버튼을 누르면 위로 열리는 메뉴(AccountMenu)에 있다.
// 점검 단계(자산 탐색 · 범위 확인 · 점검 진행 · 보고서)는 레일이 아니라 각 화면 머리의 단계 표시로 옮겨 다닌다.

import { cx } from "../lib/cx";
import { LiveRailBadge, LiveRailTasks } from "../live/rail";
import { useShell } from "../shell";
import s from "../workbench.module.css";
import { AccountMenu } from "./AccountMenu";
import { BrandMark, Icon } from "./icons";

export function Rail() {
  const { route, railOpen, closeRail, foldRail } = useShell();
  return (
    <aside className={cx(s.rail, railOpen && s.open)} id="rail" aria-label="주 메뉴">
      <div className={s["rail-top"]}>
        <a className={s.brand} href="#dashboard">
          <BrandMark />
          ABC2LAB
        </a>
        {/* 휴대폰보다 넓은 화면: 레일 접기 (다시 펼치기는 화면 머리의 메뉴 버튼) */}
        <button
          className={cx(s["icon-btn"], s["rail-fold"])}
          type="button"
          data-rail-fold=""
          aria-label="메뉴 접기"
          title="메뉴 접기"
          onClick={foldRail}
        >
          <Icon name="panelLeft" />
        </button>
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
        <AccountMenu />
      </div>
    </aside>
  );
}
