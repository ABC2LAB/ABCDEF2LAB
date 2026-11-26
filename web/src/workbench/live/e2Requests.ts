// 요청 전체 기록 · 요청 상세 (계약 docs/contracts/workbench-live-data.md §4.1, 2026-11-25).
// 서버가 검증한 E② 저널만 읽는다. 화면은 허용을 다시 판정하지 않고, 서버가 저장하지 않은 칸(헤더 · 본문)을 지어내지 않는다.
// 탐색 단계 요청은 E② 저널에 없어 여기 없다(탐색 경계를 지난 요청 · 원문 기록 없음).

import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import { type E2Reason, REASON_LABEL } from "@/lib/diagnosis/e2flow";

const LIST_FORMAT = "abc2lab.e2-flow-requests/v1";
const DETAIL_FORMAT = "abc2lab.e2-flow-request/v1";
/** 전체 기록 한 쪽 줄 수(서버 기본 100 · 최대 200) */
export const E2_REQUESTS_PAGE = 100;
const MAX_PAGE_ROWS = 200;

// 식별자 형식은 서버 브리지와 같다(server/diagnosis_management_bridge.go managementLiveRef · managementLiveAttempt)
const CALL_REF = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const ATTEMPT_ID = /^[0-9a-f]{32}$/;
export const validCallRef = (v: unknown): v is string => typeof v === "string" && CALL_REF.test(v);
export const validAttemptId = (v: unknown): v is string => typeof v === "string" && ATTEMPT_ID.test(v);

/** 요청 줄의 종류(누가 보낸 요청인가). 모르는 종류는 코드 그대로 보인다 */
export const REQUEST_KIND_TEXT: Readonly<Record<string, string>> = {
  request: "모델 진단 요청",
  auth_prep: "프로그램 로그인",
  refresh_session: "프로그램 다시 로그인",
  confirmation: "확정 단계",
  unclassified: "분류 안 된 프로그램 요청",
  // 모델 로그인 시도 기록 계약(#963, 11/26 열림): 로그인 대조 요청은 프로그램 요청으로 따로 보인다
  login_contrast: "프로그램 대조 요청",
};
export const requestKindText = (kind: string) => REQUEST_KIND_TEXT[kind] ?? kind;

/** 전송 통제 결정(서버 판정 그대로 — 화면은 다시 판정하지 않는다) */
export const DECISION_TEXT = {
  allowed: "허용",
  denied: "거부",
  unsupported: "미지원 경로 차단",
  unknown: "결과 불명",
} as const;
export type E2Decision = keyof typeof DECISION_TEXT;

/** 경로 칸이 무엇인가: 정확한 경로 · 값을 가린 틀 · 없음 */
export type PathDetailState = "exact" | "pattern_only" | "unavailable";
const PATH_STATES: readonly PathDetailState[] = ["exact", "pattern_only", "unavailable"];
/** 서버가 너무 긴 경로 대신 쓰는 자리 표시(pathmask.LongPath) */
const LONG_PATH = "/{too_long}";

/**
 * 경로 표시 글(전수조사 11/26: 요청 상세에 경로가 어떤 모양인지 보인다). 진단 요청 경로는 서버가 값을 가린 틀로만 남기고
 * (예: /users/{id}), 틀이 기록되지 않았거나 너무 길면 unavailable 이다(url_request_details attemptPathDetailState)
 */
export function pathDetailText(state: PathDetailState | null, path?: string): string | null {
  // 이전 서버 응답(상태 칸 없음)은 모양을 지어내지 않는다(계약 §4.1 '상태 필드가 없는 이전 응답도 받는다')
  if (state === null) return null;
  if (state === "exact") return "정확한 경로";
  if (state === "pattern_only") return "값을 가린 경로 틀(숫자 · ID 자리를 {id} 처럼 바꿈)";
  return path === LONG_PATH ? "경로가 너무 길어 틀로 남기지 않았어요" : "이 시도의 경로 틀이 기록되지 않았어요";
}

export type E2RequestRow = {
  at: string;
  kind: string;
  method?: string;
  path?: string;
  status?: number;
  call_ref: string;
  attempt_id: string;
  index: number;
  /** 없으면 null — 상태 칸이 없는 이전 서버 응답(1bb6f386 이전) */
  path_detail_state: PathDetailState | null;
};
export type E2RequestsPage = { rows: E2RequestRow[]; total: number; next_cursor: number | null };

/** 헤더 · 본문 칸: 지금 서버는 늘 not_stored(저장하지 않음). 그 밖의 모양은 이 화면이 모른다 — 코드는 그대로 남긴다 */
export type E2FieldState = "not_stored" | "unknown";
export type E2Field = { state: E2FieldState; code: string };
/** 저장 안 된 이유(이유 코드 → 글). 계약 workbench-live-data §4.1 */
export const FIELD_REASON_TEXT: Readonly<Record<string, string>> = {
  not_stored: "전송 기록(E② 저널)은 헤더 · 본문 · 크기 · 경로 원문을 저장하지 않아요",
};
export const E2_FIELDS = ["request_headers", "request_body", "response_headers", "response_body"] as const;
export type E2FieldKey = (typeof E2_FIELDS)[number];

export type E2RequestDetail = {
  call_ref: string;
  attempt_id: string;
  kind: string;
  index: number;
  at: string;
  method?: string;
  path_pattern?: string;
  /** 없으면 null — 상태 칸이 없는 이전 서버 응답 */
  path_detail_state: PathDetailState | null;
  rule_ref?: string;
  decision: E2Decision;
  /** 알려진 사유면 그 코드, 모르면 서버 코드 그대로(reasonCode) */
  reason: E2Reason | null;
  reasonCode: string;
  status_code?: number;
  forward_attempted: boolean;
  request_written: boolean;
  response_received: boolean;
  response_complete: boolean;
  response_truncated: boolean;
  fields: Record<E2FieldKey, E2Field>;
};

/** 상세를 못 읽은 까닭(계약 §4.1 이유 코드 · 상태) */
export type E2DetailMiss = "not_found" | "journal_invalid" | "forbidden" | "error";
export type E2DetailRead = { ok: true; detail: E2RequestDetail } | { ok: false; why: E2DetailMiss; status?: number };

const obj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const natural = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
const text = (v: unknown, max: number): v is string => typeof v === "string" && v.length > 0 && v.length <= max;
const instant = (v: unknown): v is string => text(v, 64) && !Number.isNaN(Date.parse(v));
const httpStatus = (v: unknown): v is number => natural(v) && v >= 100 && v <= 599;
const METHOD = /^[A-Z]{1,16}$/;
function invalid(): never {
  throw new ApiError(422, "invalid_contract");
}
const validTask = (task: string) => /^[1-9][0-9]*$/.test(task) && Number.isSafeInteger(Number(task));

function optional<T>(v: unknown, ok: (x: unknown) => x is T): T | undefined {
  if (v === undefined) return undefined;
  if (!ok(v)) invalid();
  return v;
}
const method = (v: unknown): v is string => typeof v === "string" && METHOD.test(v);
const path = (v: unknown): v is string => text(v, 512);

/** 경로 상태 칸: 없으면 null(이전 서버), 있으면 아는 값만 — 모르는 값은 다른 뜻을 붙일 수 없어 받지 않는다 */
function pathStateOf(v: unknown): PathDetailState | null {
  if (v === undefined) return null;
  if (!PATH_STATES.includes(v as PathDetailState)) invalid();
  return v as PathDetailState;
}

function rowOf(v: unknown): E2RequestRow {
  if (!obj(v) || !instant(v.at) || !text(v.kind, 64) || !validCallRef(v.call_ref) || !validAttemptId(v.attempt_id))
    invalid();
  if (!natural(v.index)) invalid();
  const pathState = pathStateOf(v.path_detail_state);
  const m = optional(v.method, method);
  const p = optional(v.path, path);
  const st = optional(v.status, httpStatus);
  return {
    at: v.at,
    kind: v.kind,
    ...(m === undefined ? {} : { method: m }),
    ...(p === undefined ? {} : { path: p }),
    ...(st === undefined ? {} : { status: st }),
    call_ref: v.call_ref,
    attempt_id: v.attempt_id,
    index: v.index,
    path_detail_state: pathState,
  };
}

/** 전체 기록 한 쪽. 줄은 저널 번호 오름차순이어야 한다(쪽 넘김 cursor 가 그 번호) */
export function parseE2Requests(v: unknown): E2RequestsPage {
  if (!obj(v) || v.format !== LIST_FORMAT || !Array.isArray(v.rows) || v.rows.length > MAX_PAGE_ROWS) invalid();
  if (!natural(v.total) || (v.next_cursor !== undefined && !natural(v.next_cursor))) invalid();
  const rows = v.rows.map(rowOf);
  if (rows.some((r, i) => i > 0 && r.index <= rows[i - 1].index)) invalid();
  return { rows, total: v.total, next_cursor: v.next_cursor === undefined ? null : (v.next_cursor as number) };
}

function fieldOf(v: unknown): E2Field {
  if (!obj(v) || !text(v.state, 64)) invalid();
  return { state: v.state === "not_stored" ? "not_stored" : "unknown", code: v.state };
}

const bool = (v: unknown): v is boolean => typeof v === "boolean";

/** 요청 상세. 묻은 호출 · 시도와 다른 줄이 오면 버린다(다른 요청의 결과를 이 줄에 붙이지 않음) */
export function parseE2RequestDetail(v: unknown, callRef: string, attemptId: string): E2RequestDetail {
  if (!obj(v) || v.format !== DETAIL_FORMAT || v.call_ref !== callRef || v.attempt_id !== attemptId) invalid();
  if (!text(v.kind, 64) || !natural(v.index) || !instant(v.at) || !text(v.decision, 32) || !text(v.reason, 64))
    invalid();
  const pathState = pathStateOf(v.path_detail_state);
  const flags = [
    v.forward_attempted,
    v.request_written,
    v.response_received,
    v.response_complete,
    v.response_truncated,
  ] as const;
  if (!flags.every(bool)) invalid();
  const m = optional(v.method, method);
  const pattern = optional(v.path_pattern, path);
  const rule = optional(v.rule_ref, (x): x is string => text(x, 128));
  const status = optional(v.status_code, httpStatus);
  const decision = Object.hasOwn(DECISION_TEXT, v.decision) ? (v.decision as E2Decision) : "unknown";
  const reason = Object.hasOwn(REASON_LABEL, v.reason) ? (v.reason as E2Reason) : null;
  const fields = Object.fromEntries(E2_FIELDS.map((k) => [k, fieldOf(v[k])])) as Record<E2FieldKey, E2Field>;
  return {
    call_ref: callRef,
    attempt_id: attemptId,
    kind: v.kind,
    index: v.index,
    at: v.at,
    ...(m === undefined ? {} : { method: m }),
    ...(pattern === undefined ? {} : { path_pattern: pattern }),
    path_detail_state: pathState,
    ...(rule === undefined ? {} : { rule_ref: rule }),
    decision,
    reason,
    reasonCode: v.reason as string,
    ...(status === undefined ? {} : { status_code: status }),
    forward_attempted: v.forward_attempted as boolean,
    request_written: v.request_written as boolean,
    response_received: v.response_received as boolean,
    response_complete: v.response_complete as boolean,
    response_truncated: v.response_truncated as boolean,
    fields,
  };
}

/** 전체 기록 한 쪽 읽기. cursor = 앞 쪽의 next_cursor(첫 쪽은 null) */
export async function readE2Requests(
  task: string,
  cursor: number | null,
  signal?: AbortSignal,
): Promise<E2RequestsPage> {
  if (!validTask(task) || (cursor !== null && !natural(cursor))) invalid();
  const q = new URLSearchParams({ limit: String(E2_REQUESTS_PAGE) });
  if (cursor !== null) q.set("cursor", String(cursor));
  return parseE2Requests(
    await apiFetch<unknown>(
      `/api/tasks/${encodeURIComponent(task)}/e2-flow/requests?${q}`,
      { cache: "no-store", redirect: "error", signal },
      false,
    ),
  );
}

/** 요청 상세 읽기. 404 · 409 · 403 은 이유로 돌려주고(그 칸만 못 보임), 그 밖의 오류는 던진다 */
export async function readE2RequestDetail(
  task: string,
  callRef: string,
  attemptId: string,
  signal?: AbortSignal,
): Promise<E2DetailRead> {
  if (!validTask(task) || !validCallRef(callRef) || !validAttemptId(attemptId)) invalid();
  try {
    const body = await apiFetch<unknown>(
      `/api/tasks/${encodeURIComponent(task)}/e2-flow/requests/${encodeURIComponent(callRef)}/${attemptId}`,
      { cache: "no-store", redirect: "error", signal },
      false,
    );
    return { ok: true, detail: parseE2RequestDetail(body, callRef, attemptId) };
  } catch (error) {
    if (!(error instanceof ApiError)) throw error;
    // 404 는 '이 회차 저널에 없음'과 '아직 저널 없음'이 같은 상태라, 화면은 둘을 한 문장으로 보인다
    if (error.status === 404) return { ok: false, why: "not_found", status: 404 };
    if (error.status === 409) return { ok: false, why: "journal_invalid", status: 409 };
    if (error.status === 403) return { ok: false, why: "forbidden", status: 403 };
    if (error.message === "invalid_contract") return { ok: false, why: "error" };
    return { ok: false, why: "error", status: error.status };
  }
}
