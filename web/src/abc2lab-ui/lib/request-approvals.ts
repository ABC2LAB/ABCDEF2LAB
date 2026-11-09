import { ApiError, apiFetch } from "./api";

/** 실제 요청별 승인 DTO. 도구 단위 intercept 승인과 교환하지 않는다. */
export type RequestApproval = {
  id: string;
  task_id: number;
  run_ref: string;
  intent_id: number;
  call_ref: string;
  attempt_id: string;
  policy_revision: string;
  account_ref: string;
  session_ref: string;
  request_sha256: string;
  method: string;
  target_origin: string;
  path: string;
  input_preview: Record<string, string>;
  body_bytes: number;
  created_at: string;
  expires_at: string;
  state: "pending" | "allowed" | "denied" | "expired" | "cancelled";
  decided_by?: string;
  decided_at?: string;
  consumed: boolean;
  execution_state: "not_sent" | "awaiting_dispatch" | "dispatch_pending" | "completed" | "unknown";
  reason?: string;
  live: boolean;
  can_decide: boolean;
};

/** 실제 감독자 결과. 전송 차단과 실행 마감, 취약점 판정을 서로 대신하지 않는다. */
export type RequestApprovalRunState = {
  run_ref: string;
  phase: "prepared" | "running" | "stopping" | "closed" | "failed";
  outcome: "" | "completed" | "cancelled" | "failed";
  stop_requested: boolean;
  context_cancelled: boolean;
  drain_complete: boolean;
  closure_failed: boolean;
  execution?: {
    approval_mode: "confirm_every_request" | "preauthorized_scope";
    header_mode: string;
    version: string;
    scope: { RunRef: string; TaskRef: string; CaseRef: string; ResetRef: string; PolicyRef: string };
    intent_id: number;
    intent_ids: number[];
    owner_ref: string;
    policy_revision: string;
    state: "active" | "blocked";
    expires_at: string;
    reason: string;
    audit_complete: boolean;
    counts: {
      attempts: number;
      allowed: number;
      denied: number;
      unsupported: number;
      forward_attempted: number;
      responses: number;
      unknown: number;
      calls: number;
    };
  };
};
export type RequestApprovalState = { approvals: RequestApproval[] | null; run: RequestApprovalRunState };

type RunCountKey = keyof NonNullable<RequestApprovalRunState["execution"]>["counts"];

/**
 * 진행 화면과 보고서가 같은 집계를 같은 이름으로 보이게 여기 한 곳에 둔다.
 * attempts 는 E②가 판정 전에 접수한 수(차단분 포함)라 "전송"이 아니다. 실제 대상 송신은 forward_attempted.
 */
export const RUN_COUNT_ROWS: Array<{ key: RunCountKey; label: string }> = [
  { key: "attempts", label: "요청 접수" },
  { key: "allowed", label: "허용" },
  { key: "denied", label: "차단" },
  { key: "unsupported", label: "미지원" },
  { key: "forward_attempted", label: "대상 전송 시도" },
  { key: "responses", label: "응답 수신" },
  { key: "unknown", label: "불명" },
  { key: "calls", label: "호출" },
];

export const RUN_COUNT_NOTE =
  "요청 접수는 판정 전에 받은 수라 차단분도 포함해요. 실제로 대상에 보낸 건 대상 전송 시도예요.";

/** 서버가 보고한 모델 사용량. 원문 prompt·응답은 포함하지 않는다. */
export type ModelUsage = {
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd_micros: number;
  provider_slug: string;
  model: string;
  blocked: boolean;
  failure_reason?: string;
};

/**
 * 저장된 실행 종료 기록. 서버는 세 가지 200 변형을 돌려준다:
 *  - 정상 종료: run 이 있는 완전 레코드
 *  - 준비만 하고 미실행: run=null, execution="NOT_RUN"(+model_preparation). 완료 회차로 합성하지 않는다.
 *  (그 외: 저장 없음 404, 다른 작업 403 — readDiagnosisRunDisplay 가 표시용으로 흡수)
 */
export type DiagnosisRunRecord = {
  version: 1;
  task_id: number;
  saved_at?: string; // NOT_RUN 변형엔 없다
  run: RequestApprovalRunState | null;
  execution?: "NOT_RUN";
  model?: ModelUsage;
  model_preparation?: unknown[]; // 참조만 — 원문은 표시하지 않는다
};

/** 종료 후 report 서버에서도 같은 저장 결과를 읽는다. 404를 성공 완료로 바꾸지 않는다. */
export function readDiagnosisRun(taskId: string): Promise<DiagnosisRunRecord> {
  return apiFetch<DiagnosisRunRecord>(`/api/tasks/${encodeURIComponent(taskId)}/diagnosis-run`, { cache: "no-store" });
}

/**
 * 실행 종료 기록의 표시용 판정. 모든 fail-closed 규칙을 여기(lib)에 둔다 — 화면은 렌더만.
 *  - none: 저장된 종료 결과 없음(404). **완료로 간주하지 않는다.**
 *  - not_run: 준비만 하고 미실행(run=null · NOT_RUN). 완료 회차 아님.
 *  - unavailable: 이 작업의 결과가 아님/확인 불가(403 등). 완료로 보지 않는다.
 *  - run: 저장된 실제 종료 상태(phase·outcome·counts 는 서로 대신하지 않는다).
 */
export type DiagnosisRunDisplay =
  | { kind: "none" }
  | { kind: "not_run"; preparedOnly: boolean }
  | { kind: "unavailable"; reason: string }
  | { kind: "run"; savedAt?: string; run: RequestApprovalRunState };

export function classifyDiagnosisRun(record: DiagnosisRunRecord | null): DiagnosisRunDisplay {
  if (!record) return { kind: "none" };
  if (record.execution === "NOT_RUN" || record.run === null) {
    return {
      kind: "not_run",
      preparedOnly: Array.isArray(record.model_preparation) && record.model_preparation.length > 0,
    };
  }
  return { kind: "run", savedAt: record.saved_at, run: record.run };
}

/**
 * 표시용 조회. 예상된 "결과 없음"(404)과 "다른 작업"(403)은 에러로 터뜨리지 않고 정직한 상태로 흡수한다.
 * 완료로 바꾸지 않는다. 그 밖의 실패(401·500·503 등)는 에러로 올린다.
 */
export async function readDiagnosisRunDisplay(taskId: string): Promise<DiagnosisRunDisplay> {
  try {
    return classifyDiagnosisRun(await readDiagnosisRun(taskId));
  } catch (error) {
    if (error instanceof ApiError) {
      if (error.status === 404) return { kind: "none" };
      if (error.status === 403) return { kind: "unavailable", reason: error.message };
    }
    throw error;
  }
}

function path(taskId: string, id?: string) {
  const base = `/api/tasks/${encodeURIComponent(taskId)}/request-approvals`;
  return id === undefined ? base : `${base}/${encodeURIComponent(id)}`;
}

/** 승인 행이 0건인 자동 모드·Worker 기동 전에도 실제 실행 상태를 조회한다. */
export function readRequestApprovalState(taskId: string): Promise<RequestApprovalState> {
  return apiFetch<RequestApprovalState>(path(taskId), { cache: "no-store" });
}

export async function listRequestApprovals(taskId: string): Promise<RequestApproval[]> {
  const response = await apiFetch<{ approvals: RequestApproval[] }>(path(taskId));
  return response.approvals ?? [];
}

export function getRequestApproval(taskId: string, id: string): Promise<RequestApproval> {
  return apiFetch<RequestApproval>(path(taskId, id));
}

export function decideRequestApproval(
  taskId: string,
  approval: Pick<RequestApproval, "id" | "request_sha256">,
  decision: "allow" | "deny",
): Promise<RequestApproval> {
  // 인증 실패 후 결정 POST를 자동 재시도하지 않는다. 새 인증 후 최신 상태를 먼저 읽는다.
  return apiFetch<RequestApproval>(
    `${path(taskId, approval.id)}/decision`,
    {
      method: "POST",
      body: JSON.stringify({ decision, request_sha256: approval.request_sha256 }),
    },
    false,
  );
}

/** 신규 송신을 차단하고 감독자에게 마감을 요청한다. 마감은 readRequestApprovalState로 확인한다. */
export async function stopRequestApprovalExecution(taskId: string): Promise<void> {
  const result = await apiFetch<{ stopped: boolean }>(
    `${path(taskId)}/stop`,
    {
      method: "POST",
      body: JSON.stringify({}),
    },
    false,
  );
  if (result?.stopped !== true) throw new Error("전송 중지 완료를 확인하지 못했어요.");
}
