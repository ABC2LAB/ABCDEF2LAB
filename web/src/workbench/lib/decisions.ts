// 사람이 내릴 결정 두 가지(결제 요청 승인 · 새 관리자 경로 포함 여부). 원본 전역 S · pending() · syncDecisions 의 레일 부분.

/** 승인 요청: ok 승인 · no 거부 · null 대기 */
export type Appr = "ok" | "no";
/** 새 경로 /admin/export: in 포함 · ex 제외 · null 대기 */
export type PathDecision = "in" | "ex";

/** 원본 전역 `S`. */
export interface Decisions {
  readonly appr: Appr | null;
  readonly path: PathDecision | null;
}

/** 아직 안 내린 결정 수 (0~2). 원본 `pending()`. */
export function pending(S: Decisions): number {
  return (S.appr ? 0 : 1) + (S.path ? 0 : 1);
}

/** 대시보드 '결정 대기' 칸 아래 글. 원본 syncDecisions 의 dDecideSub. */
export function pendingSub(S: Decisions): string {
  if (!pending(S)) return "모두 결정함";
  return [S.appr ? null : "승인 1", S.path ? null : "새 경로 1"].filter(Boolean).join(" · ");
}

/** 레일의 api.store-dev.com 줄. 원본 syncDecisions 의 railApiDot · railApiSub · railApiBar. */
export function railApi(S: Decisions): { dot: "run" | "wait"; sub: string; wait: boolean } {
  if (S.appr === "ok") return { dot: "run", sub: "점검 중 · 결제 흐름", wait: false };
  if (S.appr === "no") return { dot: "run", sub: "점검 중 · 결제 요청 거부함", wait: false };
  return { dot: "wait", sub: "승인 대기로 멈춤", wait: true };
}
