// URL 진단(diagnosis-api.v2) 표시 계산 (순수 함수). 서버 값의 검증은 팀 lib(diagnosis-url)이 하고,
// 여기서는 화면 글·딱지 색·갈 화면만 정한다. 진행률·남은 시간은 서버가 주지 않으므로 만들지 않는다.

import {
  type URLAccountAuth,
  type URLAccountAuthResult,
  type URLDiagnosisJob,
  type URLDiagnosisScope,
  urlPhaseLabels,
  urlStateLabels,
} from "@/lib/diagnosis/diagnosis-url";

import type { View } from "../shell";

export type UrlState = URLDiagnosisJob["state"];
export type UrlPhase = URLDiagnosisJob["phase"];

/** 서버 단계 순서 (팀 lib 의 단계 이름 순서 그대로). */
export const URL_PHASES = Object.keys(urlPhaseLabels) as UrlPhase[];

/** 엔진이 아직 움직이는 상태 (상태를 주기적으로 다시 읽는다). */
export const URL_ACTIVE_STATES: readonly UrlState[] = ["preparing", "running", "stopping"];

/**
 * 끝난 상태. 서버 계약(팀 parseURLJob)상 세 상태는 drain_complete 가 확인된 마감이다.
 * unknown 은 마감 여부와 독립(재시작 복구 등)이라 넣지 않는다 — 끝난 것으로 안내하지 않는다.
 */
export const URL_ENDED_STATES: readonly UrlState[] = ["completed", "cancelled", "failed"];

/** 시작 전 상태 (초안 · 범위 확인). */
export const URL_PRESTART_STATES: readonly UrlState[] = ["awaiting_approval", "ready"];

/** 상태 알약 색. stop = 회색(끝남·중지·실패). */
export type UrlTone = "run" | "wait" | "ok" | "stop";

const TONE: Readonly<Record<UrlState, UrlTone>> = {
  awaiting_approval: "wait",
  ready: "ok",
  preparing: "run",
  running: "run",
  stopping: "wait",
  completed: "ok",
  cancelled: "stop",
  failed: "stop",
  unknown: "wait",
};

/** 상태 → 알약 색. */
export function urlTone(state: UrlState): UrlTone {
  return TONE[state];
}

/** 상태 → 글 (팀 lib 표시 글). 확인하지 못한 상태(verified=false)는 "현재 상태 미확인". */
export function urlStateText(state: UrlState, verified = true): string {
  return verified ? urlStateLabels[state] : "현재 상태 미확인";
}

/**
 * 실행 결과 한 줄(실행 결과 상태 값 계약 §4, 이경준 화면 원칙). 미실행·판단불가를 "발견 없음"으로 보이지 않게 한다.
 * 결과 칸이 없으면(이 칸 이전 기록·결과가 없는 상태) 빈 글이다.
 */
export function urlOutcomeText(
  job: Pick<URLDiagnosisJob, "state" | "outcome" | "closing_reason" | "intent_outcomes">,
): string {
  const c = job.intent_outcomes;
  if (!c) return "";
  const finished = c.done + c.policy_limited;
  const remaining = c.incomplete + c.stopped;
  if (job.state === "cancelled") return `중지됨 · 끝난 ${finished} / 끊긴 ${remaining + c.unknown}`;
  if (job.state !== "completed" || !job.outcome) return "";
  const parts: string[] = [];
  if (job.outcome === "no_intents") parts.push("시도한 계획 없음(검사 안 함)");
  else if (job.outcome === "partial") parts.push(remaining > 0 ? `일부 완료 · 남은 작업 ${remaining}` : "일부 완료");
  else parts.push("계획한 작업 모두 마감");
  // 결과 불명은 남은 작업과 따로 보인다(미실행·판단불가를 섞지 않는다).
  if (c.unknown > 0) parts.push(`결과 불명 ${c.unknown}`);
  if (c.policy_limited > 0) parts.push(`정책상 검증 안 됨 ${c.policy_limited}`);
  if (job.closing_reason === "budget_limited") parts.push("예산 상한에서 마감 · 찾은 후보는 보존");
  return parts.join(" · ");
}

/**
 * 시험 계정 인증 결과 표시 라벨(계약 §3.4·§4, 이경준 원칙). 인증 못 한 계정은 "비인증 진행"으로만 보이고,
 * 그 계정 기준 인가 결과를 "접근 차단 확인"으로 읽지 않는다(절대 규칙 5).
 */
export const ACCOUNT_AUTH_LABEL: Record<URLAccountAuthResult, string> = {
  authenticated: "인증됨",
  login_failed: "비인증 진행 · 로그인 실패",
  session_not_found: "비인증 진행 · 세션 없음",
  result_unknown: "비인증 진행 · 결과 불명",
  not_sent: "비인증 진행 · 보내지 않음",
};

/** 계정별 인증 결과를 라벨 순으로 정렬해 [계정라벨, 결과 문구] 행으로. 자격·세션 값은 애초에 없다(라벨·결과뿐). */
export function urlAccountAuthRows(auth: URLAccountAuth): ReadonlyArray<readonly [string, string]> {
  return Object.entries(auth)
    .sort(([a], [b]) => (a < b ? -1 : Number(a > b)))
    .map(([label, result]) => [label, ACCOUNT_AUTH_LABEL[result]] as const);
}

/** 엔진이 아직 움직이는가. */
export function isUrlActive(job: Pick<URLDiagnosisJob, "state">): boolean {
  return URL_ACTIVE_STATES.includes(job.state);
}

/** 단계 위치 (모르는 단계면 -1). */
export function urlPhaseIndex(phase: UrlPhase): number {
  return URL_PHASES.indexOf(phase);
}

/** 진단을 열 때 갈 수 있는 화면. */
export type UrlView = Extract<View, "new" | "live" | "report">;

/** 이 진단을 열 때 갈 화면: 시작 전 → 새 점검(승인 범위 확인 · 시작), 결과 저장 확인 → 보고서, 그 밖 → 점검 진행. */
export function urlViewFor(job: Pick<URLDiagnosisJob, "state" | "result_ready" | "task_id">): UrlView {
  if (URL_PRESTART_STATES.includes(job.state)) return "new";
  if (job.result_ready && job.task_id) return "report";
  return "live";
}

/** 탐색 단계까지 보고 갈 화면: 탐색 중 → 자산 탐색, 선택 대기 → 범위 확인, 그 밖은 urlViewFor. */
export function scanViewFor(
  job: Pick<URLDiagnosisJob, "state" | "result_ready" | "task_id">,
  stage: "crawling" | "awaiting_selection" | "diagnosing" | "completed" | "stopped" | "failed" | "unknown" | null,
): Extract<View, "new" | "crawl" | "scope" | "live" | "report"> {
  const to = urlViewFor(job);
  if (to !== "live") return to;
  if (stage === "crawling") return "crawl";
  if (stage === "awaiting_selection") return "scope";
  return "live";
}

/** 대상 URL → host (화면 표시용). 팀 lib 이 검증한 주소만 들어온다. */
export function urlHost(target: string): string {
  try {
    return new URL(target).host;
  } catch {
    return target;
  }
}

/**
 * 주소 칸 안내 글. 실제 검사는 팀 lib(urlDiagnosisInput)이 하고, 여기서는 흔한 까닭만 먼저 알려 준다.
 * 비었으면 "", 이 함수로 모를 문제면 null(lib 이 거부하면 화면이 '주소 형식' 안내를 붙인다).
 */
export function urlTargetHint(raw: string): string | null {
  const v = raw.trim();
  if (!v) return "";
  if (!/^https?:\/\//i.test(v)) return "http 또는 https 주소만 받습니다.";
  if (/^https?:\/\/[^/]*@/i.test(v)) return "계정 정보(user:pass@)는 넣지 마세요.";
  if (/[?#]/.test(v)) return "query · fragment(? #)는 넣지 마세요.";
  if (/[%\\]/.test(v)) return "퍼센트 인코딩 · 역슬래시는 쓸 수 없습니다.";
  return null;
}

/** 백만분의 1 달러 → "USD 0.000000". */
export function usdText(micros: number): string {
  return `USD ${(micros / 1_000_000).toFixed(6)}`;
}

/** 승인 방식 글. */
export function approvalModeText(mode: URLDiagnosisScope["approval_mode"]): string {
  if (mode === "workbench_selective_v1") return "읽기 사전 승인 · 쓰기 건별 확인";
  return mode === "confirm_every_request" ? "매 요청 확인" : "사전 승인 범위";
}

/** 확인됨 / 미확인 (서버가 사실로 준 값만 '확인됨'). */
export function factText(v: boolean): string {
  return v ? "확인됨" : "미확인";
}

/** 실행 권위 상태 글. */
export const AUTHORITY_TEXT: Readonly<Record<URLDiagnosisJob["authority_state"], string>> = {
  not_issued: "미발급",
  issued: "발급됨",
  consumed: "소비됨",
  unknown: "불명",
};
