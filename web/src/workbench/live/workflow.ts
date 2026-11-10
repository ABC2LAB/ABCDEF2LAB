import { ApiError, apiFetch } from "@/lib/diagnosis/api";
import {
  parseWorkflowLimits,
  type URLDiagnosisJob,
  type WorkflowLimits,
  type WorkflowMode,
} from "@/lib/diagnosis/diagnosis-url";
export const FLOW_VERSION = "abc2lab.workbench-flow/v1";
export type WorkflowEndpoint = {
  id: string;
  method: string;
  path: string;
  kind: string;
  source: string;
  from: string;
  in_scope: boolean;
  change: "possible" | "attested_read" | "blocked";
  visited: boolean;
  status?: number;
  selected: boolean;
  duplicates: number;
  observation_error?: string;
  call_ref?: string;
  request?: string;
  response?: string;
};
export type WorkflowSnapshot = {
  format: typeof FLOW_VERSION;
  job_id: string;
  task_id: string;
  run_ref: string;
  source_digest: string;
  revision: number;
  mode: WorkflowMode;
  stage: "crawling" | "awaiting_selection" | "diagnosing" | "completed" | "stopped" | "failed" | "unknown";
  paused: boolean;
  initial_completed: boolean;
  deep_runs: number;
  deep_closed: number;
  reversible_writes: boolean;
  current_path?: string;
  failure?: string;
  detail_truncated?: boolean;
  limits: WorkflowLimits;
  counts: {
    found: number;
    visited: number;
    queued: number;
    sent: number;
    attempted: number;
    unknown: number;
    blocked: number;
    held?: number;
    omitted?: number;
    invalid_paths?: number;
  };
  endpoints: WorkflowEndpoint[];
  events: { at: string; kind: string; method?: string; path?: string; status?: number }[];
  commands: { key: string; operation: string; revision: number }[];
  plan?: { id: number; state: string; title: string }[];
  activity?: { id: number; at: string; worker: string; kind: string; summary: string }[];
  updated_at: string;
};
const obj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const ref = (v: unknown): v is string => typeof v === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(v);
const num = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0;
const text = (v: unknown, max = 4096): v is string => typeof v === "string" && v.length <= max && !v.includes("\0");
function invalid(): never {
  throw new ApiError(422, "invalid_workflow_contract");
}
export function parseWorkflow(value: unknown, job: URLDiagnosisJob): WorkflowSnapshot {
  if (!obj(value) || !obj(value.workflow)) return invalid();
  const s = value.workflow;
  if (
    s.format !== FLOW_VERSION ||
    s.job_id !== job.id ||
    s.task_id !== job.task_id ||
    s.source_digest !== job.source_digest ||
    s.mode !== job.input.workflow_mode ||
    s.run_ref !== `run-${job.id}` ||
    !num(s.revision) ||
    s.revision < 1 ||
    !text(s.updated_at) ||
    Number.isNaN(Date.parse(s.updated_at)) ||
    !["crawling", "awaiting_selection", "diagnosing", "completed", "stopped", "failed", "unknown"].includes(
      String(s.stage),
    ) ||
    !num(s.deep_runs) ||
    !num(s.deep_closed) ||
    s.deep_closed > s.deep_runs ||
    typeof s.paused !== "boolean" ||
    typeof s.initial_completed !== "boolean" ||
    typeof s.reversible_writes !== "boolean" ||
    !obj(s.counts)
  )
    return invalid();
  if (
    (s.failure !== undefined && !text(s.failure, 64)) ||
    (s.detail_truncated !== undefined && typeof s.detail_truncated !== "boolean")
  )
    return invalid();
  parseWorkflowLimits(s.limits);
  for (const k of ["omitted", "invalid_paths"]) if (s.counts[k] !== undefined && !num(s.counts[k])) return invalid();
  if (s.current_path !== undefined && !text(s.current_path, 512)) return invalid();
  for (const k of ["found", "visited", "queued", "sent", "attempted", "unknown", "blocked"]) {
    if (!num(s.counts[k])) return invalid();
  }
  if (
    !Array.isArray(s.endpoints) ||
    s.endpoints.length > 32 ||
    !Array.isArray(s.events) ||
    s.events.length > 32 ||
    !Array.isArray(s.commands) ||
    s.commands.length > 32
  )
    return invalid();
  const ids = new Set();
  for (const e of s.endpoints) {
    if (
      !obj(e) ||
      !ref(e.id) ||
      ids.has(e.id) ||
      !text(e.path, 512) ||
      !e.path.startsWith("/") ||
      !text(e.from, 512) ||
      !text(e.kind, 32) ||
      !text(e.source, 32) ||
      !["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "UNKNOWN"].includes(String(e.method)) ||
      !["possible", "attested_read", "blocked"].includes(String(e.change)) ||
      typeof e.in_scope !== "boolean" ||
      typeof e.visited !== "boolean" ||
      typeof e.selected !== "boolean" ||
      !num(e.duplicates) ||
      (e.status !== undefined && (!num(e.status) || e.status > 599)) ||
      (e.observation_error !== undefined && !text(e.observation_error, 64)) ||
      (e.request !== undefined && !text(e.request, 1024)) ||
      (e.response !== undefined && !text(e.response, 1024))
    )
      return invalid();
    ids.add(e.id);
  }
  for (const event of s.events) {
    if (
      !obj(event) ||
      !text(event.kind, 64) ||
      !text(event.at, 64) ||
      Number.isNaN(Date.parse(event.at)) ||
      (event.path !== undefined && !text(event.path, 512))
    )
      return invalid();
  }
  for (const c of s.commands) {
    if (
      !obj(c) ||
      !ref(c.key) ||
      !num(c.revision) ||
      !num(s.revision) ||
      c.revision > s.revision ||
      !["selection", "pause", "resume"].includes(String(c.operation))
    )
      return invalid();
  }
  if (s.plan !== undefined) {
    if (
      !Array.isArray(s.plan) ||
      s.plan.length > 20 ||
      s.plan.some((p) => !obj(p) || !num(p.id) || !text(p.title) || !text(p.state, 64))
    )
      return invalid();
  }
  if (s.activity !== undefined) {
    if (
      !Array.isArray(s.activity) ||
      s.activity.length > 20 ||
      s.activity.some(
        (a) =>
          !obj(a) || !num(a.id) || !text(a.summary) || !text(a.worker, 256) || !text(a.kind, 256) || !text(a.at, 64),
      )
    )
      return invalid();
  }
  return s as WorkflowSnapshot;
}
export async function readWorkflow(job: URLDiagnosisJob): Promise<WorkflowSnapshot | null> {
  try {
    return parseWorkflow(
      await apiFetch<unknown>(`/api/product/workflows/${encodeURIComponent(job.id)}`, { cache: "no-store" }, false),
      job,
    );
  } catch (error) {
    if (error instanceof ApiError && error.status === 409 && error.message === "workflow_not_started") return null;
    throw error;
  }
}
export async function commandWorkflow(
  job: URLDiagnosisJob,
  current: WorkflowSnapshot,
  operation: "selection" | "pause" | "resume",
  key: string,
  ids: string[],
): Promise<WorkflowSnapshot> {
  return parseWorkflow(
    await apiFetch<unknown>(
      `/api/product/workflows/${encodeURIComponent(job.id)}/${operation}`,
      {
        method: "POST",
        headers: { "Idempotency-Key": key },
        body: JSON.stringify({
          version: FLOW_VERSION,
          expected_revision: current.revision,
          ...(operation === "selection" ? { selected_ids: ids } : {}),
        }),
      },
      false,
    ),
    job,
  );
}
export const flowStageText: Record<WorkflowSnapshot["stage"], string> = {
  crawling: "자산 탐색 중",
  awaiting_selection: "탐색 완료 · 범위 확인 대기",
  diagnosing: "심화 진단 중",
  completed: "실행 마감",
  stopped: "중지됨",
  failed: "실행 실패",
  unknown: "결과 불명",
};
