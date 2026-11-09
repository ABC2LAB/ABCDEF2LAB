// 자리표시 화면: 화면 담당이 원본 그대로 옮겨 교체한다. (헤더 모양과 h1 tabIndex 만 미리 맞춰 둠)

import { cx } from "../lib/cx";
import { MenuButton } from "../ui/parts";
import s from "../workbench.module.css";

export function Dashboard() {
  return (
    <header className={cx(s.vh)}>
      <div className={s["vh-top"]}>
        <MenuButton />
      </div>
      <div className={s["vh-row"]}>
        <div className={s["vh-title"]}>
          <h1 tabIndex={-1}>대시보드</h1>
        </div>
      </div>
    </header>
  );
}
