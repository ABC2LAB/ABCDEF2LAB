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

function path(taskId: string, id?: string) {
  const base = `/api/tasks/${encodeURIComponent(taskId)}/request-approvals`;
  return id === undefined ? base : `${base}/${encodeURIComponent(id)}`;
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

/** task의 현재 전송 경계만 중지한다. task 전체 종료나 이미 송신한 요청의 취소가 아니다. */
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
