"use client";

// 예시 데이터 모드의 위쪽 띠: 실제 서버가 아님을 항상 알리고, 보고 싶은 상황을 고른다.

import { cx } from "../lib/cx";
import { Icon } from "../ui/icons";
import s from "../workbench.module.css";
import { EXAMPLE_SCENES, type ExampleScene, setScene } from "./exampleApi";

export function ExampleBar() {
  return (
    <div className={cx(s.demobar, s["lv-bar"])} role="note">
      <Icon name="info" />
      <span>예시 데이터 미리보기 · 실제 서버가 아닙니다</span>
      <select
        aria-label="예시 상황"
        defaultValue=""
        onChange={(e) => setScene(e.target.value as ExampleScene)}
        style={{ marginLeft: 8, font: "inherit", fontSize: 12.5, padding: "2px 6px", borderRadius: 6 }}
      >
        <option value="" disabled>
          예시 상황 고르기
        </option>
        {EXAMPLE_SCENES.map(([k, label]) => (
          <option key={k} value={k}>
            {label}
          </option>
        ))}
      </select>
    </div>
  );
}
