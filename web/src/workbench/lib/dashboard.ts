// 대시보드 계산. 원본 todoPill · renderTodo 의 남은 건수 · renderActive 의 두 줄 내용.

import type { TodoRow } from "../data/dashboard";
import type { Decisions } from "./decisions";
import { type LiveState, lremain } from "./liveState";
import { hms } from "./time";

/** 상태 알약. cls 는 CSS 클래스 이름("" 이면 기본 pill). */
export interface PillSpec {
  readonly cls: "ok" | "wait" | "run" | "";
  readonly label: string;
}

/** '지금 처리할 일' 줄의 알약 (승인 요청 · 새 경로 상세 머리에도 쓴다). 원본 `todoPill(id)`. */
export function todoPill(id: TodoRow["id"], S: Decisions): PillSpec {
  if (id === "appr") {
    if (S.appr === "ok") return { cls: "ok", label: "승인함" };
    if (S.appr === "no") return { cls: "", label: "거부함" };
    return { cls: "wait", label: "승인 대기" };
  }
  if (id === "path") {
    if (S.path === "in") return { cls: "ok", label: "포함함" };
    if (S.path === "ex") return { cls: "", label: "제외함" };
    return { cls: "wait", label: "결정 대기" };
  }
  return { cls: "", label: "결과 확인" };
}

/** '지금 처리할 일' 제목 옆 건수: 결정한 승인·새 경로는 뺀다. 원본 renderTodo 의 dTodoN. */
export function todoOpen(rows: readonly TodoRow[], S: Decisions): number {
  return rows.filter((r) => !((r.id === "appr" && S.appr !== null) || (r.id === "path" && S.path !== null))).length;
}

/** 진행 중인 점검(shop) 줄의 설명. 원본 renderActive 의 act-shop meta. */
export function shopMeta(l: LiveState): string {
  let state = `약 ${lremain(l)}분 남음`;
  if (l.done) state = "점검 완료";
  else if (l.ended) state = "종료됨";
  return `오늘 ${hms(l.start).slice(0, 5)} 시작 · ${state} · 발견 ${l.forder.length}`;
}

/** 진행 중인 점검(api.store-dev.com) 줄: 승인 요청을 정하기 전에는 멈춰 있다. 원본 renderActive 의 api 배열. */
export function apiActive(S: Decisions): { pill: PillSpec; meta: string; wait: boolean } {
  if (S.appr) {
    return {
      pill: { cls: "run", label: "점검 중" },
      meta: `결제 흐름 ${S.appr === "ok" ? "점검 재개" : "요청 거부 · 판단 불가로 남김"}`,
      wait: false,
    };
  }
  return { pill: { cls: "wait", label: "승인 대기" }, meta: "승인 대기로 멈춤", wait: true };
}
