"use client";

import { cx } from "../lib/cx";
import { useShell } from "../shell";
import s from "../workbench.module.css";
import { Icon } from "./icons";

/**
 * 화면 머리의 메뉴 버튼 → 휴대폰은 레일 서랍, 넓은 화면은 접은 레일 펼치기.
 * 휴대폰이거나 레일을 접었을 때만 보인다. 원본 `button.menu-btn[data-rail-open]`.
 */
export function MenuButton() {
  const { openRail } = useShell();
  return (
    <button
      className={cx(s["icon-btn"], s["menu-btn"])}
      type="button"
      data-rail-open=""
      aria-label="메뉴 열기"
      onClick={openRail}
    >
      <Icon name="menu" />
    </button>
  );
}
