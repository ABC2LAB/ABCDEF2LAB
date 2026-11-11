"use client";

// 왼쪽 레일: 새 점검 · 대시보드 · 이어 보는 점검(진행 중 · 끝난 점검) · 맨 아래 계정 버튼. 시안(aside#rail)과 같은 짜임.
// 분석 모델 · 진단 모드 · 다크 모드 · 강조색 시안 · 로그아웃은 계정 버튼을 누르면 위로 열리는 메뉴(AccountMenu)에 있다.
// 점검 단계(자산 탐색 · 범위 확인 · 점검 진행 · 보고서)는 레일이 아니라 각 화면 머리의 단계 표시로 옮겨 다닌다.

import { cx } from "../lib/cx";
import { LiveRailBadge, LiveRailTasks } from "../live/rail";
import { useShell } from "../shell";
import { useWb } from "../store";
import s from "../workbench.module.css";
import { AccountMenu } from "./AccountMenu";
import { BrandMark, Icon } from "./icons";

export function Rail() {
  const { route, railOpen, closeRail, foldRail, openRail } = useShell();
  // 넓은 화면에서 접으면 아이콘 줄(64px)만 남는다 (시안 v19). 글자는 가리고 마우스를 올리면 이름이 뜬다.
  const folded = useWb((st) => st.railHidden);
  const tip = (t: string) => (folded ? t : undefined);
  const foldText = folded ? "메뉴 펼치기" : "메뉴 접기";
  return (
    <aside className={cx(s.rail, railOpen && s.open)} id="rail" aria-label="주 메뉴">
      <div className={s["rail-top"]}>
        <a className={s.brand} href="#dashboard" title={tip("ABC2LAB")}>
          <BrandMark />
          <span className={s.rlbl}>ABC2LAB</span>
        </a>
        {/* 휴대폰보다 넓은 화면: 레일을 아이콘 줄로 접고 · 같은 버튼으로 다시 펼친다 */}
        <button
          className={cx(s["icon-btn"], s["rail-fold"])}
          type="button"
          data-rail-fold=""
          aria-controls="rail"
          aria-expanded={!folded}
          aria-label={foldText}
          title={foldText}
          onClick={folded ? openRail : foldRail}
        >
          <Icon name={folded ? "unfoldLeft" : "foldLeft"} />
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
      <a
        className={s["new-btn"]}
        href="#new"
        aria-current={route === "new" ? "page" : undefined}
        title={tip("새 점검")}
      >
        <Icon name="plus" />
        <span className={s.rlbl}>새 점검</span>
      </a>
      <nav className={s.rnav} aria-label="화면">
        <a
          className={s.rl}
          href="#dashboard"
          aria-current={route === "dashboard" ? "page" : undefined}
          title={tip("대시보드")}
        >
          <Icon name="grid" />
          <span className={s.rlbl}>대시보드</span>
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
