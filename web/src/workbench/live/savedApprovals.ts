// 끝난 점검의 승인 이력 (계약 docs/contracts/saved-request-approvals.md, 이동찬 11/24). 저장된 승인 기록의 읽기 전용 투영이라
// 대상 주소 · 경로 · 본문 · 계정 · 세션 · 결정 기능이 없다. 화면은 이 경로로만 끝난 회차 이력을 보이고 결정 버튼을 두지 않는다.
// 승인 ID 는 그 요청의 E② 시도 ID 와 같다(attempt_ref = id) — 보낸 요청이면 요청 전체 기록(e2Requests)과 이어 경로 틀을 붙인다.

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

/** 서버 목록 상한(넘으면 422 — 부분 목록을 전체로 오해하지 않게) */
const MAX_SAVED_APPROVALS = 1000;
const REF = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const METHODS = ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"] as const;
export const SAVED_STATES = ["pending", "allowed", "denied", "expired", "cancelled"] as const;
export const SAVED_EXECUTIONS = ["not_sent", "awaiting_dispatch", "dispatch_pending", "completed", "unknown"] as const;

export type SavedApproval = {
  id: string;
  attempt_ref: string;
  method: (typeof METHODS)[number];
  state: (typeof SAVED_STATES)[number];
  execution_state: (typeof SAVED_EXECUTIONS)[number];
  created_at: string;
  expires_at: string;
  decided_at?: string;
};
export type SavedApprovals = { run_ref: string; approvals: SavedApproval[] };

/**
 * 보냄 칸 글. allowed 만으로 송신을 뜻하지 않고, completed 는 실행 결과가 기록됐다는 뜻이지 성공 응답이 아니다(계약).
 * 끝난 회차에서 보내는 중 · 결과 불명은 '보냈을 수 있음'으로 남긴다(규칙 5 — 보내지 않았다고 단정하지 않음)
 */
export const EXECUTION_TEXT: Readonly<Record<SavedApproval["execution_state"], string>> = {
  not_sent: "보내지 않음",
  awaiting_dispatch: "보내기 전에 점검이 끝남",
  dispatch_pending: "보냈는지 확인 안 됨",
  completed: "보냄 · 결과 기록됨",
  unknown: "결과 불명(보냈을 수 있음)",
};

const obj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const instant = (v: unknown): v is string =>
  typeof v === "string" && v.length > 0 && v.length <= 64 && !Number.isNaN(Date.parse(v));
const oneOf = <T extends string>(list: readonly T[], v: unknown): v is T =>
  typeof v === "string" && (list as readonly string[]).includes(v);
function invalid(): never {
  throw new ApiError(422, "invalid_contract");
}

function rowOf(v: unknown): SavedApproval {
  if (
    !obj(v) ||
    typeof v.id !== "string" ||
    !REF.test(v.id) ||
    v.attempt_ref !== v.id ||
    !oneOf(METHODS, v.method) ||
    !oneOf(SAVED_STATES, v.state) ||
    !oneOf(SAVED_EXECUTIONS, v.execution_state) ||
    !instant(v.created_at) ||
    !instant(v.expires_at) ||
    (v.decided_at !== undefined && !instant(v.decided_at)) ||
    // 읽기 전용 투영: 결정할 수 있다고 오면 이 경로의 것이 아니다
    v.live !== false ||
    v.can_decide !== false
  )
    invalid();
  return {
    id: v.id,
    attempt_ref: v.id,
    method: v.method,
    state: v.state,
    execution_state: v.execution_state,
    created_at: v.created_at,
    expires_at: v.expires_at,
    ...(v.decided_at === undefined ? {} : { decided_at: v.decided_at as string }),
  };
}

export function parseSavedApprovals(v: unknown): SavedApprovals {
  if (!obj(v) || typeof v.run_ref !== "string" || !REF.test(v.run_ref) || !Array.isArray(v.approvals)) invalid();
  if (v.approvals.length > MAX_SAVED_APPROVALS) invalid();
  const approvals = v.approvals.map(rowOf);
  if (new Set(approvals.map((a) => a.id)).size !== approvals.length) invalid();
  return { run_ref: v.run_ref, approvals };
}

/** 끝난 점검의 승인 이력 읽기. 오류는 그대로 던진다(화면이 404 · 422 · 그 밖을 나눠 적는다) */
export async function readSavedApprovals(task: string, signal?: AbortSignal): Promise<SavedApprovals> {
  if (!/^[1-9][0-9]*$/.test(task) || !Number.isSafeInteger(Number(task))) invalid();
  return parseSavedApprovals(
    await apiFetch<unknown>(
      `/api/tasks/${encodeURIComponent(task)}/saved-request-approvals`,
      { cache: "no-store", redirect: "error", signal },
      false,
    ),
  );
}
