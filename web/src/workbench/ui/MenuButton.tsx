"use client";

import { cx } from "../lib/cx";
import { useShell } from "../shell";
import s from "../workbench.module.css";
import { Icon } from "./icons";

/** 화면 머리의 메뉴 버튼 (휴대폰에서만 보임) → 레일 서랍. 원본 `button.menu-btn[data-rail-open]`. */
export function MenuButton() {
  const { openRail } = useShell();
  return (
    <button className={cx(s["icon-btn"], s["menu-btn"])} type="button" aria-label="메뉴 열기" onClick={openRail}>
      <Icon name="menu" />
    </button>
  );
}
