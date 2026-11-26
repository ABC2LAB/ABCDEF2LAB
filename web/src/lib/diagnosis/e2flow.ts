/** 작업대 실데이터 계약 §4. 감사 검증은 서버가 수행하며 화면은 허용을 재판정하지 않는다. */
import { ApiError, apiFetch } from "./api";

export const REASON_LABEL = {
  complete: "응답 받음",
  withheld: "응답 보류·잘림",
  policy_denied: "정책 밖(범위·메서드·위험 요청)",
  workflow_paused: "진단 흐름 일시정지",
  unsupported: "지원 안 하는 요청",
  invalid: "잘못된 요청",
  budget_exhausted: "예산 소진",
  attempt_limit: "호출 상한",
  denial_limit: "호출 상한",
  transient_limit: "호출 상한",
  unknown_limit: "호출 상한",
  closed: "실행 닫힘",
  expired: "승인 만료",
  revoked: "승인 철회",
  transport_failed: "대상 연결 실패(보내지 않음)",
  audit_failed: "감사 기록 실패",
  unknown: "결과 불명(대상이 받았을 수 있음)",
} as const;
export type E2Reason = keyof typeof REASON_LABEL;
export const STAGE_LABEL = {
  intent: "보내기 전 판정",
  result: "송신 결과",
  control: "실행 통제 사건",
  call_closed: "호출 마감",
  unknown: "단계 불명",
} as const;
export type E2Stage = keyof typeof STAGE_LABEL;
export type DecisionClass = "allow" | "deny" | "transient" | "unknown";
export type E2Attempt = {
  attempt_id: string;
  index: number;
  sequence: number;
  stage: E2Stage;
  method: string;
  rule_ref: string;
  path_pattern: string;
  decision: "allowed" | "denied" | "unsupported" | "unknown";
  reason: E2Reason;
  forward_attempted: boolean;
  request_written: boolean;
  response_received: boolean;
  response_complete: boolean;
  status_code: number;
  response_truncated: boolean;
  at: string;
};
export type E2Call = {
  call_ref: string;
  intent_id: number;
  closed: boolean;
  closed_index?: number;
  attempts: E2Attempt[];
};
export const COUNT_LABEL = {
  attempts: "시도",
  allowed: "허용",
  denied: "거부",
  unsupported: "미지원",
  forward_attempted: "송신 시도(연결 포함)",
  responses: "응답",
  unknown: "결과 불명",
  transient: "송신 실패",
  withheld: "응답 보류",
  calls: "호출",
} as const;
export type E2Counts = Record<keyof typeof COUNT_LABEL, number>;
export type E2Flow = {
  format: "abc2lab.e2-flow-view/v1";
  /** none: 실행은 결속됐지만 E② 저널이 아직 없음(진단 요청 전, 또는 E②를 거치지 않고 끝난 회차 — 계약 §4). 사건 0 · 탐색 수만 */
  journal: "verified" | "invalid" | "none";
  last_index: number;
  more: boolean;
  omitted_calls: number;
  counts: E2Counts;
  model_writes?: { responded_2xx: number; unverified: number };
  /** 대상에 나간 요청 내역(#929, 계약 workbench-live-data §4). 검증된 응답에만 있다(그 전 서버는 없음). */
  target_requests?: E2TargetRequests;
  calls: E2Call[];
};
export const EXPLORATION_STATES = ["none", "running", "closed", "not_run", "unknown"] as const;
/** 대상 요청 내역. 모르는 수는 0 이 아니라 null 이다(탐색 수 · 총계). */
export type E2TargetRequests = {
  model: number;
  login: number;
  confirmation: number;
  unclassified: number;
  exploration: number | null;
  exploration_state: (typeof EXPLORATION_STATES)[number];
  total: number | null;
};
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const natural = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0;
const ref = (v: unknown): v is string => typeof v === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(v);
// biome-ignore lint/suspicious/noControlCharactersInRegex: 화면용 경로의 제어 문자와 공백을 거부한다.
const unsafePathPattern = /[?#\\\x00-\x20\x7f]/;
const invalid = (): never => {
  throw new ApiError(422, "invalid_e2_flow_contract");
};
function exact(v: Record<string, unknown>, keys: string[]) {
  if (Object.keys(v).some((k) => !keys.includes(k))) invalid();
}
function attempt(v: unknown): E2Attempt {
  if (
    !object(v) ||
    !ref(v.attempt_id) ||
    !natural(v.index) ||
    v.index === 0 ||
    !natural(v.sequence) ||
    !natural(v.status_code) ||
    v.status_code > 599 ||
    typeof v.at !== "string" ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(v.at) ||
    !Number.isFinite(Date.parse(v.at))
  )
    return invalid();
  exact(v, [
    "attempt_id",
    "index",
    "sequence",
    "stage",
    "method",
    "rule_ref",
    "path_pattern",
    "decision",
    "reason",
    "forward_attempted",
    "request_written",
    "response_received",
    "response_complete",
    "status_code",
    "response_truncated",
    "at",
  ]);
  for (const key of [
    "forward_attempted",
    "request_written",
    "response_received",
    "response_complete",
    "response_truncated",
  ])
    if (typeof v[key] !== "boolean") return invalid();
  if (
    typeof v.path_pattern !== "string" ||
    v.path_pattern.length > 2048 ||
    (v.path_pattern !== "" && (!v.path_pattern.startsWith("/") || unsafePathPattern.test(v.path_pattern)))
  )
    return invalid();
  if (v.rule_ref !== "" && !ref(v.rule_ref)) return invalid();
  return {
    attempt_id: v.attempt_id,
    index: v.index,
    sequence: v.sequence,
    stage: Object.hasOwn(STAGE_LABEL, String(v.stage)) ? (v.stage as E2Stage) : "unknown",
    reason: Object.hasOwn(REASON_LABEL, String(v.reason)) ? (v.reason as E2Reason) : "unknown",
    decision: ["allowed", "denied", "unsupported"].includes(String(v.decision))
      ? (v.decision as E2Attempt["decision"])
      : "unknown",
    method: ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"].includes(String(v.method))
      ? String(v.method)
      : "UNKNOWN",
    rule_ref: String(v.rule_ref),
    path_pattern: v.path_pattern,
    forward_attempted: v.forward_attempted as boolean,
    request_written: v.request_written as boolean,
    response_received: v.response_received as boolean,
    response_complete: v.response_complete as boolean,
    status_code: v.status_code,
    response_truncated: v.response_truncated as boolean,
    at: v.at,
  };
}
export function parseE2Flow(v: unknown): E2Flow {
  if (
    !object(v) ||
    v.format !== "abc2lab.e2-flow-view/v1" ||
    !["verified", "invalid", "none"].includes(String(v.journal))
  )
    return invalid();
  exact(v, [
    "format",
    "journal",
    "last_index",
    "more",
    "omitted_calls",
    "counts",
    "calls",
    "model_writes",
    "target_requests",
  ]);
  if (
    !natural(v.last_index) ||
    typeof v.more !== "boolean" ||
    !natural(v.omitted_calls) ||
    !object(v.counts) ||
    !Array.isArray(v.calls)
  )
    return invalid();
  const counts = {} as E2Counts;
  exact(v.counts, Object.keys(COUNT_LABEL));
  for (const key of Object.keys(COUNT_LABEL) as (keyof E2Counts)[]) {
    if (!natural(v.counts[key])) return invalid();
    counts[key] = v.counts[key];
  }
  // 검증 실패 응답에 사건이 섞여 와도 절대 그리지 않는다.
  if (v.journal === "invalid")
    return { format: v.format, journal: "invalid", last_index: 0, more: false, omitted_calls: 0, counts, calls: [] };
  // 저널 없음은 사건이 없다는 뜻이다. 사건 · 누계 · 커서가 실려 오면 계약 밖이다(지어낸 사건을 그리지 않는다).
  if (
    v.journal === "none" &&
    (v.calls.length !== 0 ||
      v.last_index !== 0 ||
      v.more ||
      v.omitted_calls !== 0 ||
      Object.values(counts).some((n) => n !== 0))
  )
    return invalid();
  let modelWrites: E2Flow["model_writes"];
  if (v.model_writes !== undefined) {
    const raw = v.model_writes;
    if (!object(raw) || !natural(raw.responded_2xx) || !natural(raw.unverified)) return invalid();
    exact(raw, ["responded_2xx", "unverified"]);
    modelWrites = { responded_2xx: raw.responded_2xx, unverified: raw.unverified };
  }
  const targetRequests = v.target_requests === undefined ? undefined : targetRequestsOf(v.target_requests);
  const ids = new Set<string>();
  const calls: E2Call[] = [];
  for (const row of v.calls) {
    if (
      !object(row) ||
      !ref(row.call_ref) ||
      ids.has(row.call_ref) ||
      !natural(row.intent_id) ||
      typeof row.closed !== "boolean" ||
      !Array.isArray(row.attempts) ||
      (row.closed_index !== undefined && (!natural(row.closed_index) || row.closed_index > v.last_index))
    )
      return invalid();
    exact(row, ["call_ref", "intent_id", "closed", "closed_index", "attempts"]);
    ids.add(row.call_ref);
    if (row.closed && (!row.closed_index || !natural(row.closed_index))) return invalid();
    const attempts = new Map<string, E2Attempt>();
    for (const raw of row.attempts) {
      const a = attempt(raw);
      if (a.index > v.last_index) return invalid();
      const old = attempts.get(a.attempt_id);
      if (!old || old.index < a.index) attempts.set(a.attempt_id, a);
    }
    calls.push({
      call_ref: row.call_ref,
      intent_id: row.intent_id,
      closed: row.closed,
      ...(natural(row.closed_index) ? { closed_index: row.closed_index } : {}),
      attempts: [...attempts.values()].sort((a, b) => a.index - b.index),
    });
  }
  if (v.journal === "none") {
    const t = targetRequests;
    // 저널이 없으면 진단 갈래는 0 이다(탐색 수만 있음)
    if (t && (t.model !== 0 || t.login !== 0 || t.confirmation !== 0 || t.unclassified !== 0)) return invalid();
    if (modelWrites && (modelWrites.responded_2xx !== 0 || modelWrites.unverified !== 0)) return invalid();
  }
  return {
    format: v.format,
    journal: v.journal === "none" ? "none" : "verified",
    last_index: v.last_index,
    more: v.more,
    omitted_calls: v.omitted_calls,
    counts,
    ...(modelWrites ? { model_writes: modelWrites } : {}),
    ...(targetRequests ? { target_requests: targetRequests } : {}),
    calls,
  };
}

/**
 * target_requests 를 서버 규칙(cmd/first-controlled-diagnosis/url_target_requests.go, 계약 workbench-live-data §4)대로 닫는다.
 * 탐색 수: none · not_run 은 0, closed 는 수, running 은 수 또는 null(경계가 보고하지 않음), unknown 은 null.
 * 총계: 탐색 수를 알고 unclassified 가 0 이면 model + login + confirmation + exploration, 아니면 null — 모르는 수를 0 으로 채우지 않는다.
 */
function targetRequestsOf(raw: unknown): E2TargetRequests {
  if (
    !object(raw) ||
    !natural(raw.model) ||
    !natural(raw.login) ||
    !natural(raw.confirmation) ||
    !natural(raw.unclassified) ||
    !(EXPLORATION_STATES as readonly string[]).includes(String(raw.exploration_state)) ||
    (raw.exploration !== null && !natural(raw.exploration)) ||
    (raw.total !== null && !natural(raw.total))
  )
    return invalid();
  exact(raw, ["model", "login", "confirmation", "unclassified", "exploration", "exploration_state", "total"]);
  const state = raw.exploration_state as E2TargetRequests["exploration_state"];
  const exploration = raw.exploration as number | null;
  if ((state === "none" || state === "not_run") && exploration !== 0) return invalid();
  if (state === "closed" && exploration === null) return invalid();
  if (state === "unknown" && exploration !== null) return invalid();
  const sum =
    exploration === null || raw.unclassified !== 0 ? null : raw.model + raw.login + raw.confirmation + exploration;
  if (raw.total !== sum) return invalid();
  return {
    model: raw.model,
    login: raw.login,
    confirmation: raw.confirmation,
    unclassified: raw.unclassified,
    exploration,
    exploration_state: state,
    total: sum,
  };
}
const BLOCKED = new Set<E2Reason>([
  "policy_denied",
  "workflow_paused",
  "unsupported",
  "invalid",
  "budget_exhausted",
  "attempt_limit",
  "denial_limit",
  "transient_limit",
  "unknown_limit",
  "closed",
  "expired",
  "revoked",
]);
export function decisionClass(a: E2Attempt): DecisionClass {
  // intent의 false 관측은 보내지 않았다는 증거가 아니다. 결과 사건이 없으면 불명이다.
  if (
    a.stage === "intent" ||
    a.stage === "unknown" ||
    a.reason === "unknown" ||
    a.reason === "audit_failed" ||
    a.decision === "unknown"
  )
    return "unknown";
  // C02의 forward_attempted는 연결 시도를 포함한다. 연결 뒤 끊김은 서버가 unknown으로 남긴다.
  if (a.reason === "transport_failed") return !a.request_written && !a.response_received ? "transient" : "unknown";
  if (BLOCKED.has(a.reason))
    return !a.forward_attempted && !a.request_written && !a.response_received ? "deny" : "unknown";
  return a.stage === "result" &&
    a.response_received &&
    a.decision === "allowed" &&
    (a.reason === "complete" || a.reason === "withheld")
    ? "allow"
    : "unknown";
}
/** 같은 시도는 마지막 사건 번호로 교체한다. 누계 counts는 매 응답의 전체 저널 값이며 더하지 않는다. */
export function mergeE2Flow(previous: E2Flow | null, next: E2Flow): E2Flow {
  if (next.journal !== "verified" || !previous || previous.journal !== "verified") return next;
  if (next.last_index < previous.last_index) return invalid();
  const calls = new Map(previous.calls.map((c) => [c.call_ref, c]));
  for (const c of next.calls) {
    const old = calls.get(c.call_ref);
    if (!old) {
      calls.set(c.call_ref, c);
      continue;
    }
    if (old.intent_id !== c.intent_id) return invalid();
    const attempts = new Map(old.attempts.map((a) => [a.attempt_id, a]));
    for (const a of c.attempts) {
      const p = attempts.get(a.attempt_id);
      if (!p || a.index > p.index) attempts.set(a.attempt_id, a);
    }
    calls.set(c.call_ref, {
      ...c,
      closed: old.closed || c.closed,
      ...(old.closed_index || c.closed_index
        ? { closed_index: Math.max(old.closed_index ?? 0, c.closed_index ?? 0) }
        : {}),
      attempts: [...attempts.values()].sort((a, b) => a.index - b.index),
    });
  }
  return { ...next, omitted_calls: Math.max(previous.omitted_calls, next.omitted_calls), calls: [...calls.values()] };
}
export async function getE2Flow(task: string | null, after?: number, signal?: AbortSignal): Promise<E2Flow> {
  if (
    !task ||
    !/^[1-9][0-9]*$/.test(task) ||
    !Number.isSafeInteger(Number(task)) ||
    (after !== undefined && !natural(after))
  )
    return invalid();
  return parseE2Flow(
    await apiFetch<unknown>(
      `/api/tasks/${encodeURIComponent(task)}/e2-flow${after === undefined ? "" : `?after=${after}`}`,
      { cache: "no-store", redirect: "error", signal, headers: { "X-Jindo-E2-Model-Writes": "1" } },
      false,
    ),
  );
}

/** 화면 '보낸 요청' 칸: 큰 글(총계)과 마우스를 올리면 보일 내역(사용자 2026-11-25 결정 — 숫자는 대상에 나간 요청 전부). */
export type SentRequests = { value: string; detail: string };

/**
 * e2-flow 응답 → '보낸 요청' 칸. 숫자는 대상에 나간 요청 총계(target_requests.total)이고, 내역은 '모델 50 · 로그인 6'처럼
 * 0 이 아닌 갈래만(모델은 늘) 적는다. 검증 실패는 '검증 실패', 총계를 모르면 '—'와 모르는 까닭 — 0 이나 모델 수로 대신하지
 * 않는다(절대 규칙 5). 응답이 없거나 내역 칸이 없는 서버(#929 이전)면 null(호출부가 '—'로 둔다).
 */
export function sentRequestsOf(flow: E2Flow | null | undefined): SentRequests | null {
  if (!flow) return null;
  if (flow.journal === "invalid")
    return { value: "검증 실패", detail: "전송 기록(E②) 검증에 실패해 보낸 요청을 셀 수 없습니다" };
  const t = flow.target_requests;
  if (!t) return null;
  const parts = [`모델 ${t.model}`];
  if (t.login) parts.push(`로그인 ${t.login}`);
  if (t.confirmation) parts.push(`확정 ${t.confirmation}`);
  if (t.exploration === null) parts.push("탐색 모름");
  else if (t.exploration) parts.push(`탐색 ${t.exploration}`);
  if (t.unclassified) parts.push(`분류 안 된 프로그램 요청 ${t.unclassified}`);
  const detail = parts.join(" · ");
  if (t.total === null) {
    const why = t.unclassified ? "분류 안 된 프로그램 요청이 있어" : "탐색 요청 수를 확인하지 못해";
    return { value: "—", detail: `${why} 총계를 모릅니다 · ${detail}` };
  }
  return { value: `${t.total}건`, detail };
}
