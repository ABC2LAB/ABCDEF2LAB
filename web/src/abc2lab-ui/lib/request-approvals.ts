import { apiFetch } from "./api";

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
export type RequestApprovalState = { approvals: RequestApproval[]; run: RequestApprovalRunState };

export type DiagnosisRunRecord = {
  version: 1;
  task_id: number;
  saved_at: string;
  run: RequestApprovalRunState;
};

/** 종료 후 report 서버에서도 같은 저장 결과를 읽는다. 404를 성공 완료로 바꾸지 않는다. */
export function readDiagnosisRun(taskId: string): Promise<DiagnosisRunRecord> {
  return apiFetch<DiagnosisRunRecord>(`/api/tasks/${encodeURIComponent(taskId)}/diagnosis-run`, { cache: "no-store" });
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
