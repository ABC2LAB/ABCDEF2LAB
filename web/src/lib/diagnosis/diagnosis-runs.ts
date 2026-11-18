import { ApiError, apiFetchLimitedJSON } from "./api";
import { preservesURLRunTiming, sameURLRunTime, validURLRunTiming } from "./diagnosis-run-timing";
import { type URLDiagnosisJob, urlPhaseLabels, urlStateLabels } from "./diagnosis-url";
import { validTaskID } from "./first-diagnosis";

export const MAX_RUN_PAGE_SIZE = 50;
export const MAX_RUN_PAGE_BYTES = 24 * 1024;
export const MAX_RUN_CURSOR_BYTES = 512;
export const runApprovalLabels = {
  not_revalidated: "승인 재확인 전",
  expired: "승인 만료",
  revoked: "승인 철회",
  changed: "승인 변경",
  unavailable: "승인 상태 확인 불가",
} as const;
export type URLRunSummary = Pick<
  URLDiagnosisJob,
  | "id"
  | "revision"
  | "state"
  | "phase"
  | "reason_code"
  | "task_id"
  | "started"
  | "drain_complete"
  | "started_at"
  | "finished_at"
  | "result_ready"
> & {
  display_name?: string;
  report_available: boolean;
  approval_status: keyof typeof runApprovalLabels;
  action_required: boolean;
};
export interface URLRunPage {
  runs: URLRunSummary[];
  snapshot: string;
  limit: number;
  more: boolean;
  next_cursor?: string;
}
export interface URLRunPageRequest {
  cursor?: string;
  limit?: number;
}
const record = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const ref = (v: unknown): v is string => typeof v === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/.test(v);
const digest = (v: unknown): v is string => typeof v === "string" && /^[a-f0-9]{64}$/.test(v);
const natural = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
function invalid(): never {
  throw new ApiError(422, "invalid_run_list_contract");
}
function exact(value: Record<string, unknown>, keys: string[]): void {
  if (Object.keys(value).some((key) => !keys.includes(key))) invalid();
}
const bytes = (v: unknown) => new TextEncoder().encode(JSON.stringify(v)).length;
export const activeURLRun = (v: Pick<URLRunSummary, "state">) => ["preparing", "running", "stopping"].includes(v.state);
export const endedURLRun = (v: Pick<URLRunSummary, "state">) => ["completed", "cancelled", "failed"].includes(v.state);
function before(a: URLRunSummary, b: URLRunSummary): boolean {
  return activeURLRun(a) !== activeURLRun(b) ? activeURLRun(a) : a.id > b.id;
}
function cursor(value: unknown): value is string {
  if (
    typeof value !== "string" ||
    !value.length ||
    value.length > MAX_RUN_CURSOR_BYTES ||
    !/^[A-Za-z0-9_-]+$/.test(value) ||
    value.length % 4 === 1
  )
    return false;
  try {
    return (
      btoa(atob(value.replaceAll("-", "+").replaceAll("_", "/")))
        .replaceAll("+", "-")
        .replaceAll("/", "_")
        .replace(/=+$/, "") === value
    );
  } catch {
    return false;
  }
}
function parseRun(value: unknown): URLRunSummary {
  if (
    !record(value) ||
    !ref(value.id) ||
    !natural(value.revision) ||
    !value.revision ||
    typeof value.state !== "string" ||
    !Object.hasOwn(urlStateLabels, value.state) ||
    typeof value.phase !== "string" ||
    !Object.hasOwn(urlPhaseLabels, value.phase)
  )
    return invalid();
  exact(value, [
    "id",
    "revision",
    "display_name",
    "state",
    "phase",
    "reason_code",
    "task_id",
    "started",
    "drain_complete",
    "started_at",
    "finished_at",
    "result_ready",
    "report_available",
    "approval_status",
    "action_required",
  ]);
  if (!validURLRunTiming(value)) return invalid();
  if (
    value.display_name !== undefined &&
    (typeof value.display_name !== "string" ||
      new TextEncoder().encode(value.display_name).length > 160 ||
      Array.from(value.display_name).some(
        (char) => char.charCodeAt(0) < 32 || (char.charCodeAt(0) >= 127 && char.charCodeAt(0) <= 159),
      ))
  )
    return invalid();
  if (
    value.reason_code !== undefined &&
    (typeof value.reason_code !== "string" || !/^[a-z][a-z0-9_]{0,63}$/.test(value.reason_code))
  )
    return invalid();
  if (value.task_id !== undefined && (typeof value.task_id !== "string" || !validTaskID(value.task_id)))
    return invalid();
  for (const key of ["started", "drain_complete", "result_ready", "report_available", "action_required"])
    if (typeof value[key] !== "boolean") return invalid();
  if (typeof value.approval_status !== "string" || !Object.hasOwn(runApprovalLabels, value.approval_status))
    return invalid();
  const run = value;
  if (
    ((run.started || run.result_ready) && !run.task_id) ||
    run.report_available !== (run.result_ready && !!run.task_id) ||
    (["preparing", "running", "stopping"].includes(String(run.state)) && !run.action_required)
  )
    return invalid();
  const badFacts: Record<string, boolean> = {
    awaiting_approval: !!(run.started || run.drain_complete || run.result_ready || run.task_id),
    ready: !!(run.started || run.drain_complete || run.result_ready || run.task_id),
    preparing: !!run.started,
    running: !run.started || !!run.drain_complete || !!run.result_ready,
    stopping: !!run.result_ready,
    completed: !run.started || !run.drain_complete || !run.result_ready,
    failed: !run.drain_complete || (!!run.started && !run.result_ready),
    cancelled: !run.drain_complete || (!!run.started && !run.result_ready),
    unknown: false,
  };
  if (badFacts[String(run.state)]) return invalid();
  return run as unknown as URLRunSummary;
}

/** 보호 Job의 표시 필드만 읽고 시각·URL·권한을 보충하지 않는다. */
export function parseURLRunPage(value: unknown, owner: string, request: URLRunPageRequest = {}): URLRunPage {
  if (
    !record(value) ||
    value.version !== "diagnosis-api.v2" ||
    !digest(owner) ||
    value.owner_context !== owner ||
    !record(value.page)
  )
    return invalid();
  exact(value, ["version", "owner_context", "page"]);
  const p = value.page;
  exact(p, ["runs", "snapshot", "limit", "more", "next_cursor"]);
  if (
    !digest(p.snapshot) ||
    !natural(p.limit) ||
    p.limit < 1 ||
    p.limit > MAX_RUN_PAGE_SIZE ||
    !Array.isArray(p.runs) ||
    p.runs.length > p.limit ||
    typeof p.more !== "boolean" ||
    bytes(p) > MAX_RUN_PAGE_BYTES ||
    bytes(value) > MAX_RUN_PAGE_BYTES + 1024
  )
    return invalid();
  if (
    (request.limit !== undefined && p.limit !== request.limit) ||
    p.more !== (p.next_cursor !== undefined) ||
    (p.next_cursor !== undefined && (!cursor(p.next_cursor) || p.next_cursor === request.cursor))
  )
    return invalid();
  const runs = p.runs.map(parseRun);
  if (p.more && !runs.length) return invalid();
  for (let i = 1; i < runs.length; i++) if (!before(runs[i - 1], runs[i])) return invalid();
  if (new Set(runs.map((r) => r.id)).size !== runs.length) return invalid();
  return {
    runs,
    snapshot: p.snapshot,
    limit: p.limit,
    more: p.more,
    ...(p.next_cursor === undefined ? {} : { next_cursor: p.next_cursor as string }),
  };
}

export async function readURLRunPage(
  owner: string,
  request: URLRunPageRequest = {},
  signal?: AbortSignal,
): Promise<URLRunPage> {
  if (
    !digest(owner) ||
    (request.cursor !== undefined && !cursor(request.cursor)) ||
    (request.limit !== undefined && (!natural(request.limit) || request.limit < 1 || request.limit > MAX_RUN_PAGE_SIZE))
  )
    return invalid();
  const q = new URLSearchParams();
  if (request.cursor) q.set("cursor", request.cursor);
  if (request.limit !== undefined) q.set("limit", String(request.limit));
  return parseURLRunPage(
    await apiFetchLimitedJSON(`/api/diagnoses/v2/runs${q.size ? `?${q}` : ""}`, MAX_RUN_PAGE_BYTES + 1024, {
      cache: "no-store",
      redirect: "error",
      signal,
    }),
    owner,
    request,
  );
}

/** 같은 snapshot 페이지만 순서·중복·진행 여부를 검사해 합친다. */
export function appendURLRunPage(previous: URLRunPage, next: URLRunPage): URLRunPage {
  if (!previous.more || previous.snapshot !== next.snapshot || previous.limit !== next.limit || !next.runs.length)
    return invalid();
  const ids = new Set(previous.runs.map((r) => r.id));
  if (
    next.runs.some((r) => ids.has(r.id)) ||
    (previous.runs.length && !before(previous.runs[previous.runs.length - 1], next.runs[0]))
  )
    return invalid();
  return { ...next, runs: [...previous.runs, ...next.runs] };
}

/** 목록의 오래된 revision은 허용하되 Job 결속과 이미 확인된 사실은 되돌리지 않는다. */
export function matchURLRun(job: URLDiagnosisJob, run: URLRunSummary): URLDiagnosisJob {
  if (
    job.id !== run.id ||
    (job.input.display_name ?? "") !== (run.display_name ?? "") ||
    job.revision < run.revision ||
    (run.task_id && run.task_id !== job.task_id) ||
    (run.started && !job.started) ||
    (run.drain_complete && !job.drain_complete) ||
    (run.result_ready && !job.result_ready) ||
    !preservesURLRunTiming(job, run)
  )
    return invalid();
  if ((endedURLRun(run) || run.state === "unknown") && job.state !== run.state) return invalid();
  const phases = Object.keys(urlPhaseLabels);
  if (phases.indexOf(job.phase) < phases.indexOf(run.phase)) return invalid();
  if (job.revision === run.revision)
    for (const key of [
      "state",
      "phase",
      "task_id",
      "started",
      "drain_complete",
      "started_at",
      "finished_at",
      "result_ready",
      "reason_code",
    ] as const) {
      const same =
        key === "started_at" || key === "finished_at" ? sameURLRunTime(job[key], run[key]) : job[key] === run[key];
      if (!same) return invalid();
    }
  return job;
}

export function urlRunListError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) return "로그인이 만료됐어요. 다시 로그인해 점검 목록을 조회하세요.";
    if (error.status === 403) return "점검 목록을 조회할 권한이 없어요.";
    if (error.status === 404) return "진단 목록을 찾지 못했어요. 운영자가 서비스 연결을 확인해야 해요.";
    if (error.status === 409) return "목록이 조회 중 변경됐어요. 목록을 다시 조회하세요.";
    if (error.status === 422) return "진단 목록 응답 계약을 확인하지 못했어요. 빈 목록으로 판단하지 않아요.";
    if (error.status === 503) return "진단 목록 서비스에 연결할 수 없어요. 다시 조회하세요.";
  }
  return "진단 목록을 읽지 못했어요. 다시 조회하세요.";
}
