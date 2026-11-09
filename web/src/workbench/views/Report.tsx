// 자리표시 화면: 화면 담당이 원본 그대로 옮겨 교체한다. (헤더 모양과 h1 tabIndex 만 미리 맞춰 둠)

import { useWb } from "../store";
import { MenuButton } from "../ui/parts";
import s from "../workbench.module.css";

export function Report() {
  const host = useWb((st) => st.report.host);
  return (
    <header className={s.vh}>
      <div className={s["vh-top"]}>
        <MenuButton />
      </div>
      <div className={s["vh-row"]}>
        <div className={s["vh-title"]}>
          <h1 tabIndex={-1} id="rTitle">
            {host} 보고서
          </h1>
        </div>
      </div>
    </header>
  );
}
