/** 작업대 실데이터 계약 §4. 감사 검증은 서버가 수행하며 화면은 허용을 재판정하지 않는다. */
import { ApiError, apiFetch } from "./api";

export const REASON_LABEL = {
  complete: "응답 받음",
  withheld: "응답 보류·잘림",
  policy_denied: "정책 밖(범위·메서드·위험 요청)",
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
  forward_attempted: "송신 시도",
  responses: "응답",
  unknown: "결과 불명",
  transient: "송신 실패",
  withheld: "응답 보류",
  calls: "호출",
} as const;
export type E2Counts = Record<keyof typeof COUNT_LABEL, number>;
export type E2Flow = {
  format: "abc2lab.e2-flow-view/v1";
  journal: "verified" | "invalid";
  last_index: number;
  more: boolean;
  omitted_calls: number;
  counts: E2Counts;
  calls: E2Call[];
};
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const natural = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0;
const ref = (v: unknown): v is string => typeof v === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(v);
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
    (v.path_pattern !== "" && (!v.path_pattern.startsWith("/") || /[?#\\\x00-\x20\x7f]/.test(v.path_pattern)))
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
  if (!object(v) || v.format !== "abc2lab.e2-flow-view/v1" || !["verified", "invalid"].includes(String(v.journal)))
    return invalid();
  exact(v, ["format", "journal", "last_index", "more", "omitted_calls", "counts", "calls"]);
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
  return {
    format: v.format,
    journal: "verified",
    last_index: v.last_index,
    more: v.more,
    omitted_calls: v.omitted_calls,
    counts,
    calls,
  };
}
const BLOCKED = new Set<E2Reason>([
  "policy_denied",
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
  if (a.stage === "unknown" || a.reason === "unknown" || a.reason === "audit_failed" || a.decision === "unknown")
    return "unknown";
  if (a.reason === "transport_failed")
    return !a.forward_attempted && !a.request_written && !a.response_received ? "transient" : "unknown";
  if (BLOCKED.has(a.reason))
    return !a.forward_attempted && !a.request_written && !a.response_received && a.decision !== "allowed"
      ? "deny"
      : "unknown";
  return a.decision === "allowed" && (a.reason === "complete" || a.reason === "withheld") ? "allow" : "unknown";
}
/** 같은 시도는 마지막 사건 번호로 교체한다. 누계 counts는 매 응답의 전체 저널 값이며 더하지 않는다. */
export function mergeE2Flow(previous: E2Flow | null, next: E2Flow): E2Flow {
  if (next.journal === "invalid" || !previous || previous.journal !== "verified") return next;
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
      { cache: "no-store", redirect: "error", signal },
      false,
    ),
  );
}
