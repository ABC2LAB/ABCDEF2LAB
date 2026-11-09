"use client";

// 실제 엔진 모드의 자산 탐색 화면 자리. 화면 담당이 live/ 의 API 로 채운다(README '실제 엔진 모드').

import { MenuButton } from "../../ui/parts";
import s from "../../workbench.module.css";

/** live 자산 탐색 (자리만: 머리와 '연결 준비 중'). */
export function CrawlLive() {
  return (
    <>
      <header className={s.vh}>
        <div className={s["vh-top"]}>
          <MenuButton />
          <span className={s.crumb}>작업대</span>
        </div>
        <div className={s["vh-row"]}>
          <div className={s["vh-title"]}>
            <h1 tabIndex={-1}>자산 탐색</h1>
          </div>
        </div>
      </header>
      <div className={s.vm}>
        <p className={s.empty}>연결 준비 중</p>
      </div>
    </>
  );
}
