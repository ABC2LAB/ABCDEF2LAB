import { ApiError, apiFetch } from "./api";
import { validTaskID } from "./first-diagnosis";

const base = "/api/diagnoses/v2";
const version = "diagnosis-api.v2";
export type WorkflowMode = "ai_auto_v1" | "user_approval_v1";
export type WorkflowLimits = {
  format: "abc2lab.workbench-flow/v1";
  max_pages: number;
  max_endpoints: number;
  max_response_bytes: number;
  allow_reversible_writes: boolean;
};
export type URLDiagnosisInput = {
  target_url: string;
  profile_id: string;
  display_name?: string;
  workflow_mode?: WorkflowMode;
  excluded_paths?: string;
};
export type URLDiagnosisModel = {
  mode: "openrouter";
  model: "qwen/qwen3.5-35b-a3b";
  provider: string;
  external_transfer: true;
  input_tokens_per_call: number;
  output_tokens_per_call: number;
};
export type URLDiagnosisProfile = {
  workflow?: WorkflowLimits;
  workflow_request_limit?: number;
  id: string;
  label: string;
  model_profile_ref: string;
  model_label: string;
  model?: URLDiagnosisModel;
};
export type URLDiagnosisCatalog = { ownerContext: string; profiles: URLDiagnosisProfile[] };
export type URLDiagnosisBudget = {
  requests: number;
  model_requests: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd_micros: number;
  duration_seconds: number;
};
export type URLDiagnosisScope = {
  target_ref: string;
  scope_ref: string;
  origin: string;
  path_prefixes: string[];
  methods: string[];
  query_policy: "any";
  body_policy: "bounded";
  max_body_bytes: number;
  approval_mode: "preauthorized_scope" | "confirm_every_request" | "workbench_selective_v1";
  attestation_ref: string;
  budget: URLDiagnosisBudget;
  expires_at: string;
  model?: URLDiagnosisModel;
};
export type URLDiagnosisUsage = {
  requests: number;
  input_tokens: number;
  output_tokens: number;
  reserved_input_tokens: number;
  reserved_output_tokens: number;
  cost_usd_micros: number;
  usage_known: boolean;
  reserved_cost_usd_micros?: number;
};
export const urlStateLabels = {
  awaiting_approval: "승인 대기",
  ready: "실행 전 검증 완료",
  preparing: "진단 준비 중",
  running: "엔진 실행 중",
  stopping: "중지·마감 중",
  completed: "실행 마감",
  cancelled: "중지됨",
  failed: "진단 실패",
  unknown: "결과 불명",
} as const;
export const urlPhaseLabels = {
  draft: "초안",
  scope_check: "승인 범위 확인",
  task_creation: "실제 작업 생성",
  planning: "모델 계획 준비",
  authority: "실행 권위 결속",
  gateway: "Gateway 준비",
  execution: "대상 진단",
  closure: "감사·마감",
  report: "결과 저장",
} as const;
export type URLDiagnosisJob = {
  id: string;
  revision: number;
  source_digest: string;
  input: URLDiagnosisInput;
  state: keyof typeof urlStateLabels;
  phase: keyof typeof urlPhaseLabels;
  reason_code?: string;
  scope?: URLDiagnosisScope;
  task_id?: string;
  preparation_started: boolean;
  authority_state: "not_issued" | "issued" | "consumed" | "unknown";
  started: boolean;
  drain_complete: boolean;
  result_ready: boolean;
  model_usage?: URLDiagnosisUsage;
};
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const ref = (v: unknown): v is string => typeof v === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/.test(v);
const digest = (v: unknown): v is string => typeof v === "string" && /^[a-f0-9]{64}$/.test(v);
const natural = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0;
const text = (v: unknown, max = 160): v is string =>
  typeof v === "string" &&
  new TextEncoder().encode(v).length <= max &&
  Array.from(v).every((char) => char.charCodeAt(0) >= 32 && (char.charCodeAt(0) < 127 || char.charCodeAt(0) > 159));
function invalid(): never {
  throw new ApiError(422, "invalid_contract");
}
function exact(v: Record<string, unknown>, allowed: string[]) {
  if (Object.keys(v).some((key) => !allowed.includes(key))) invalid();
}
function targetURL(v: unknown): string {
  if (
    !text(v, 2048) ||
    !v ||
    /[\\%?#]/.test(v) ||
    Array.from(v).some((char) => char.charCodeAt(0) <= 32 || char.charCodeAt(0) > 126)
  )
    return invalid();
  const parts = /^https:\/\/([^/]+)(\/.*)?$/.exec(v);
  if (!parts) return invalid();
  const authority = parts[1];
  const path = parts[2] ?? "/";
  if (path.includes("//") || path.split("/").some((segment) => segment === "." || segment === "..")) return invalid();
  const hostPort = /^(\[[^\]]+\]|[^:]+)(?::([0-9]+))?$/.exec(authority);
  if (!hostPort || authority.endsWith(":")) return invalid();
  const host = hostPort[1].toLowerCase();
  const port = hostPort[2];
  if (port !== undefined && (!/^[1-9][0-9]*$/.test(port) || Number(port) > 65535)) return invalid();
  let parsed: URL;
  try {
    parsed = new URL(v);
  } catch {
    return invalid();
  }
  // WHATWG URL이 정수·축약 IPv4, userinfo, trailing dot를 조용히 바꾸는 입력은 거부한다.
  if (
    parsed.protocol !== "https:" ||
    parsed.username ||
    parsed.password ||
    parsed.hostname !== host ||
    host.endsWith(".")
  )
    return invalid();
  if (!host.startsWith("[") && host.split(".").some((label) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label)))
    return invalid();
  return parsed.href;
}
/** URL은 요청일 뿐이다. DNS·실제 연결·경로 허가는 서버와 E②가 검증한다. */
export function urlDiagnosisInput(
  target: string,
  profile: string,
  name: string,
  workflowMode?: WorkflowMode,
  excludedPaths = "",
): URLDiagnosisInput {
  return parseInput({
    target_url: target.trim(),
    profile_id: profile,
    ...(workflowMode ? { workflow_mode: workflowMode } : {}),
    ...(excludedPaths.trim() ? { excluded_paths: excludedPaths.trim() } : {}),
    ...(name.trim() ? { display_name: name.trim() } : {}),
  });
}
function parseInput(v: unknown): URLDiagnosisInput {
  if (!object(v) || !ref(v.profile_id) || (v.display_name !== undefined && !text(v.display_name))) return invalid();
  exact(v, ["target_url", "profile_id", "display_name", "workflow_mode", "excluded_paths"]);
  if (v.workflow_mode !== undefined && v.workflow_mode !== "ai_auto_v1" && v.workflow_mode !== "user_approval_v1")
    return invalid();
  if (v.excluded_paths !== undefined) {
    if (
      typeof v.excluded_paths !== "string" ||
      !v.workflow_mode ||
      v.excluded_paths.length > 2048 ||
      v.excluded_paths
        .split("\n")
        .some(
          (p) =>
            !p.startsWith("/") ||
            /[\\%?#!\r\t ]/.test(p) ||
            p.includes("//") ||
            p.includes("..") ||
            p.split("/").some((part) => part.includes("*") && part !== "*"),
        )
    )
      return invalid();
  }
  return {
    target_url: targetURL(v.target_url),
    profile_id: v.profile_id,
    ...(v.workflow_mode ? { workflow_mode: v.workflow_mode as WorkflowMode } : {}),
    ...(v.excluded_paths ? { excluded_paths: String(v.excluded_paths) } : {}),
    ...(v.display_name ? { display_name: String(v.display_name) } : {}),
  };
}
export function sameURLInput(a: URLDiagnosisInput, b: URLDiagnosisInput): boolean {
  return (
    a.target_url === b.target_url &&
    a.profile_id === b.profile_id &&
    (a.display_name ?? "") === (b.display_name ?? "") &&
    (a.workflow_mode ?? "") === (b.workflow_mode ?? "") &&
    (a.excluded_paths ?? "") === (b.excluded_paths ?? "")
  );
}
function budget(v: unknown): URLDiagnosisBudget {
  const keys = [
    "requests",
    "model_requests",
    "input_tokens",
    "output_tokens",
    "cost_usd_micros",
    "duration_seconds",
  ] as const;
  if (!object(v) || !keys.every((key) => natural(v[key]))) return invalid();
  exact(v, [...keys]);
  return Object.fromEntries(keys.map((key) => [key, v[key]])) as URLDiagnosisBudget;
}
function model(v: unknown): URLDiagnosisModel {
  if (
    !object(v) ||
    v.mode !== "openrouter" ||
    v.model !== "qwen/qwen3.5-35b-a3b" ||
    !text(v.provider, 128) ||
    !v.provider ||
    v.external_transfer !== true ||
    v.input_tokens_per_call !== 262144 ||
    !natural(v.output_tokens_per_call) ||
    !v.output_tokens_per_call ||
    v.output_tokens_per_call > v.input_tokens_per_call
  )
    return invalid();
  exact(v, ["mode", "model", "provider", "external_transfer", "input_tokens_per_call", "output_tokens_per_call"]);
  return {
    mode: "openrouter",
    model: "qwen/qwen3.5-35b-a3b",
    provider: v.provider,
    external_transfer: true,
    input_tokens_per_call: 262144,
    output_tokens_per_call: v.output_tokens_per_call,
  };
}
function scope(v: unknown): URLDiagnosisScope {
  if (
    !object(v) ||
    !ref(v.target_ref) ||
    !ref(v.scope_ref) ||
    !ref(v.attestation_ref) ||
    !Array.isArray(v.path_prefixes) ||
    !v.path_prefixes.length ||
    v.path_prefixes.length > 64 ||
    !v.path_prefixes.every((p) => text(p, 2048) && p.startsWith("/") && !/[?#\\]/.test(p)) ||
    !Array.isArray(v.methods) ||
    !v.methods.length ||
    v.methods.length > 8 ||
    !v.methods.every(
      (m) =>
        m === "GET" ||
        m === "HEAD" ||
        (v.approval_mode === "workbench_selective_v1" && ["POST", "PUT", "PATCH"].includes(String(m))),
    ) ||
    v.query_policy !== "any" ||
    v.body_policy !== "bounded" ||
    !natural(v.max_body_bytes) ||
    (v.approval_mode !== "preauthorized_scope" &&
      v.approval_mode !== "confirm_every_request" &&
      v.approval_mode !== "workbench_selective_v1") ||
    !text(v.expires_at, 64) ||
    !Number.isFinite(Date.parse(v.expires_at))
  )
    return invalid();
  exact(v, [
    "target_ref",
    "scope_ref",
    "origin",
    "path_prefixes",
    "methods",
    "query_policy",
    "body_policy",
    "max_body_bytes",
    "approval_mode",
    "attestation_ref",
    "budget",
    "expires_at",
    "model",
  ]);
  const origin = targetURL(v.origin);
  if (new URL(origin).pathname !== "/") return invalid();
  for (const path of v.path_prefixes as string[]) {
    if (targetURL(new URL(origin).origin + path) !== new URL(origin).origin + path) return invalid();
  }
  return {
    target_ref: v.target_ref,
    scope_ref: v.scope_ref,
    origin: new URL(origin).origin,
    path_prefixes: [...v.path_prefixes] as string[],
    methods: [...v.methods] as string[],
    query_policy: "any",
    body_policy: "bounded",
    max_body_bytes: v.max_body_bytes,
    approval_mode: v.approval_mode,
    attestation_ref: v.attestation_ref,
    budget: budget(v.budget),
    expires_at: v.expires_at,
    ...(v.model === undefined ? {} : { model: model(v.model) }),
  };
}
function usage(v: unknown): URLDiagnosisUsage {
  const keys = [
    "requests",
    "input_tokens",
    "output_tokens",
    "reserved_input_tokens",
    "reserved_output_tokens",
    "cost_usd_micros",
  ] as const;
  if (!object(v) || !keys.every((key) => natural(v[key])) || typeof v.usage_known !== "boolean") return invalid();
  if (v.reserved_cost_usd_micros !== undefined && !natural(v.reserved_cost_usd_micros)) return invalid();
  exact(v, [...keys, "usage_known", "reserved_cost_usd_micros"]);
  return {
    ...Object.fromEntries(keys.map((key) => [key, v[key]])),
    usage_known: v.usage_known,
    ...(v.reserved_cost_usd_micros === undefined ? {} : { reserved_cost_usd_micros: v.reserved_cost_usd_micros }),
  } as URLDiagnosisUsage;
}
function envelope(v: unknown, owner?: string): Record<string, unknown> {
  if (
    !object(v) ||
    v.version !== version ||
    !digest(v.owner_context) ||
    (owner !== undefined && v.owner_context !== owner)
  )
    return invalid();
  return v;
}
export function parseURLCatalog(raw: unknown): URLDiagnosisCatalog {
  const e = envelope(raw);
  exact(e, ["version", "owner_context", "profiles"]);
  if (!Array.isArray(e.profiles) || e.profiles.length > 256) return invalid();
  const seen = new Set<string>();
  const profiles = e.profiles.map((p: unknown) => {
    if (
      !object(p) ||
      !ref(p.id) ||
      seen.has(p.id) ||
      !text(p.label) ||
      !p.label ||
      !ref(p.model_profile_ref) ||
      !text(p.model_label) ||
      !p.model_label ||
      (p.workflow !== undefined &&
        (!natural(p.workflow_request_limit) || p.workflow_request_limit < 1 || p.workflow_request_limit > 100))
    )
      return invalid();
    exact(p, ["id", "label", "model_profile_ref", "model_label", "model", "workflow", "workflow_request_limit"]);
    seen.add(p.id);
    return {
      id: p.id,
      label: p.label,
      model_profile_ref: p.model_profile_ref,
      model_label: p.model_label,
      ...(p.model === undefined ? {} : { model: model(p.model) }),
      ...(p.workflow === undefined
        ? {}
        : { workflow: parseWorkflowLimits(p.workflow), workflow_request_limit: Number(p.workflow_request_limit) }),
    };
  });
  return { ownerContext: String(e.owner_context), profiles };
}
export function parseURLJob(raw: unknown, owner: string): URLDiagnosisJob {
  const e = envelope(raw, owner);
  exact(e, ["version", "owner_context", "draft"]);
  const d = e.draft;
  if (
    !object(d) ||
    !ref(d.id) ||
    !natural(d.revision) ||
    !d.revision ||
    !digest(d.source_digest) ||
    typeof d.state !== "string" ||
    !Object.hasOwn(urlStateLabels, d.state) ||
    typeof d.phase !== "string" ||
    !Object.hasOwn(urlPhaseLabels, d.phase) ||
    !["not_issued", "issued", "consumed", "unknown"].includes(String(d.authority_state)) ||
    !["preparation_started", "started", "drain_complete", "result_ready"].every((k) => typeof d[k] === "boolean") ||
    (d.task_id !== undefined && (typeof d.task_id !== "string" || !validTaskID(d.task_id))) ||
    (d.reason_code !== undefined &&
      (typeof d.reason_code !== "string" || !/^[a-z][a-z0-9_]{0,63}$/.test(d.reason_code)))
  )
    return invalid();
  exact(d, [
    "id",
    "revision",
    "source_digest",
    "input",
    "state",
    "phase",
    "reason_code",
    "scope",
    "task_id",
    "preparation_started",
    "authority_state",
    "started",
    "drain_complete",
    "result_ready",
    "model_usage",
  ]);
  const input = parseInput(d.input);
  const approved = d.scope === undefined ? undefined : scope(d.scope);
  if (
    approved &&
    (approved.origin !== new URL(input.target_url).origin ||
      (approved.approval_mode === "workbench_selective_v1" && input.workflow_mode !== "user_approval_v1"))
  )
    return invalid();
  if (["ready", "preparing", "running", "completed"].includes(d.state) && !approved) return invalid();
  if (
    d.started &&
    (!d.task_id || !d.preparation_started || !["consumed", "unknown"].includes(String(d.authority_state)))
  )
    return invalid();
  if ((d.drain_complete || d.result_ready) && !d.preparation_started) return invalid();
  if (d.result_ready && !d.task_id) return invalid();
  if (
    ["awaiting_approval", "ready"].includes(d.state) &&
    (d.preparation_started ||
      d.started ||
      d.drain_complete ||
      d.result_ready ||
      d.task_id !== undefined ||
      d.authority_state !== "not_issued")
  )
    return invalid();
  if (d.state === "preparing" && (!d.preparation_started || d.started)) return invalid();
  if (d.state === "stopping" && (!d.preparation_started || d.result_ready)) return invalid();
  if (d.state === "unknown" && !d.preparation_started) return invalid();
  if (d.state === "running" && (!d.started || d.drain_complete || d.result_ready)) return invalid();
  if (d.state === "completed" && (!d.started || !d.drain_complete || !d.result_ready)) return invalid();
  if (
    ["failed", "cancelled"].includes(d.state) &&
    (!d.preparation_started || !d.drain_complete || (d.started && !d.result_ready))
  )
    return invalid();
  return {
    id: d.id,
    revision: d.revision,
    source_digest: d.source_digest,
    input,
    state: d.state as URLDiagnosisJob["state"],
    phase: d.phase as URLDiagnosisJob["phase"],
    preparation_started: d.preparation_started as boolean,
    authority_state: d.authority_state as URLDiagnosisJob["authority_state"],
    started: d.started as boolean,
    drain_complete: d.drain_complete as boolean,
    result_ready: d.result_ready as boolean,
    ...(d.reason_code ? { reason_code: String(d.reason_code) } : {}),
    ...(approved ? { scope: approved } : {}),
    ...(d.task_id ? { task_id: String(d.task_id) } : {}),
    ...(d.model_usage === undefined ? {} : { model_usage: usage(d.model_usage) }),
  };
}
export async function readURLCatalog(): Promise<URLDiagnosisCatalog> {
  return parseURLCatalog(await apiFetch<unknown>(`${base}/catalog`, { cache: "no-store", redirect: "error" }, false));
}
export async function createURLDraft(input: URLDiagnosisInput, key: string, owner: string): Promise<URLDiagnosisJob> {
  if (!ref(key) || !digest(owner)) return invalid();
  const result = parseURLJob(
    await apiFetch<unknown>(
      `${base}/drafts`,
      {
        method: "POST",
        redirect: "error",
        headers: { "Idempotency-Key": key },
        body: JSON.stringify({ version, input }),
      },
      false,
    ),
    owner,
  );
  if (result.revision !== 1 || result.preparation_started || !sameURLInput(result.input, input)) return invalid();
  return result;
}
export async function readURLReceipt(key: string, owner: string): Promise<URLDiagnosisJob> {
  if (!ref(key)) return invalid();
  return parseURLJob(
    await apiFetch<unknown>(
      `${base}/drafts`,
      {
        cache: "no-store",
        redirect: "error",
        headers: { "Idempotency-Key": key },
      },
      false,
    ),
    owner,
  );
}
export async function readURLStatus(id: string, owner: string): Promise<URLDiagnosisJob> {
  if (!ref(id)) return invalid();
  const result = parseURLJob(
    await apiFetch<unknown>(
      `${base}/drafts/${encodeURIComponent(id)}/status`,
      { cache: "no-store", redirect: "error" },
      false,
    ),
    owner,
  );
  if (result.id !== id) return invalid();
  return result;
}
function sameValue(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) || Array.isArray(b))
    return (
      Array.isArray(a) &&
      Array.isArray(b) &&
      a.length === b.length &&
      a.every((value, index) => sameValue(value, b[index]))
    );
  if (!object(a) || !object(b)) return false;
  const keys = Object.keys(a).filter((key) => a[key] !== undefined);
  return (
    keys.length === Object.keys(b).filter((key) => b[key] !== undefined).length &&
    keys.every((key) => sameValue(a[key], b[key]))
  );
}
const authorityRank = { not_issued: 0, issued: 1, consumed: 2, unknown: -1 };
const phaseRank = Object.keys(urlPhaseLabels);
type JobBinding = Pick<URLDiagnosisJob, "id" | "revision" | "source_digest" | "input"> & Partial<URLDiagnosisJob>;
/** 시작 시도 전 검증만 새 설치 범위를 표시할 수 있다. 실행 중인 결속은 절대 교체하지 않는다. */
export function matchURLJob(
  result: URLDiagnosisJob,
  current: JobBinding,
  options: { allowPreStartRebind?: boolean } = {},
): URLDiagnosisJob {
  const canRebind =
    options.allowPreStartRebind === true &&
    result.revision > current.revision &&
    !result.preparation_started &&
    !result.started &&
    !result.drain_complete &&
    !result.result_ready &&
    !result.task_id &&
    result.authority_state === "not_issued" &&
    ["awaiting_approval", "ready"].includes(result.state) &&
    current.preparation_started !== true &&
    current.started !== true &&
    (current.state === undefined || ["awaiting_approval", "ready"].includes(current.state));
  if (result.id !== current.id || result.revision < current.revision || !sameURLInput(result.input, current.input))
    return invalid();
  if (result.revision === current.revision) {
    if (current.state !== undefined && !sameValue(result, current)) return invalid();
    // 같은 revision에서 상태·known fact가 달라지면 뒤늦은 응답으로 제어권을 복원하지 않는다.
    for (const key of Object.keys(current) as (keyof URLDiagnosisJob)[]) {
      if (!sameValue(result[key], current[key])) return invalid();
    }
  }
  if (
    !canRebind &&
    (result.source_digest !== current.source_digest ||
      ((current.state !== undefined || Object.hasOwn(current, "scope")) && !sameValue(result.scope, current.scope)))
  )
    return invalid();
  if (
    (current.task_id !== undefined && result.task_id !== current.task_id) ||
    (current.started === true && !result.started) ||
    (current.preparation_started === true && !result.preparation_started) ||
    (current.drain_complete === true && !result.drain_complete) ||
    (current.result_ready === true && !result.result_ready) ||
    (current.phase !== undefined && phaseRank.indexOf(result.phase) < phaseRank.indexOf(current.phase)) ||
    (current.authority_state === "consumed" && result.authority_state !== "consumed") ||
    (current.authority_state === "unknown" && result.authority_state !== "unknown") ||
    (current.authority_state !== undefined &&
      result.authority_state !== "unknown" &&
      authorityRank[result.authority_state] < authorityRank[current.authority_state]) ||
    (current.state === "stopping" && ["preparing", "running"].includes(result.state)) ||
    (current.state !== undefined &&
      ["completed", "cancelled", "failed", "unknown"].includes(current.state) &&
      !sameValue(result, current))
  )
    return invalid();
  if (current.model_usage) {
    if (!result.model_usage) return invalid();
    const counters = [
      "requests",
      "input_tokens",
      "output_tokens",
      "reserved_input_tokens",
      "reserved_output_tokens",
      "cost_usd_micros",
    ] as const;
    for (const key of counters) if (result.model_usage[key] < current.model_usage[key]) return invalid();
    if ((result.model_usage.reserved_cost_usd_micros ?? 0) < (current.model_usage.reserved_cost_usd_micros ?? 0))
      return invalid();
  }
  return result;
}
export async function commandURLDiagnosis(
  current: URLDiagnosisJob,
  action: "validate" | "start" | "stop",
  owner: string,
  key?: string,
  confirmation?: { externalTransferConfirmed?: boolean },
): Promise<URLDiagnosisJob> {
  if (action === "start" && !ref(key)) return invalid();
  // 외부 전송 동의는 호출 화면이 명시적으로 공급하며 scope에서 만들어 내지 않는다.
  const externalStart = action === "start" && current.scope?.model?.external_transfer === true;
  const externalTransferConfirmed = confirmation?.externalTransferConfirmed === true;
  if (externalStart && !externalTransferConfirmed) {
    throw new ApiError(400, "external_transfer_confirmation_required");
  }
  const result = matchURLJob(
    parseURLJob(
      await apiFetch<unknown>(
        `${base}/drafts/${encodeURIComponent(current.id)}/${action}`,
        {
          method: "POST",
          redirect: "error",
          ...(key ? { headers: { "Idempotency-Key": key } } : {}),
          body: JSON.stringify({
            version,
            expected_revision: current.revision,
            ...(externalStart ? { external_transfer_confirmed: externalTransferConfirmed } : {}),
          }),
        },
        false,
      ),
      owner,
    ),
    current,
    { allowPreStartRebind: action === "validate" },
  );
  if (action === "validate" && (result.preparation_started || !["ready", "awaiting_approval"].includes(result.state)))
    return invalid();
  if (action === "start" && ["ready", "awaiting_approval"].includes(result.state)) return invalid();
  return result;
}

/** 조회용 식별자와 동일 요청 payload만 보관한다. 상태·승인·비밀정보를 신뢰하거나 저장하지 않는다. */
export type URLDiagnosisHandle = {
  ownerContext: string;
  creationKey: string;
  input: URLDiagnosisInput;
  id?: string;
  revision?: number;
  sourceDigest?: string;
  startKey?: string;
  startRevision?: number;
  /** 재결속 금지 latch일 뿐 서버 상태·실행 허가가 아니다. */
  bindingLocked?: true;
};
const HANDLE_KEY = "abc2lab.urlDiagnosisHandle.v2";
export function storeURLHandle(handle: URLDiagnosisHandle | null): void {
  try {
    if (handle) sessionStorage.setItem(HANDLE_KEY, JSON.stringify(handle));
    else sessionStorage.removeItem(HANDLE_KEY);
  } catch {
    /* 저장 불가 시 조회 핸들을 메모리에만 유지한다. */
  }
}
export function readURLHandle(owner: string): URLDiagnosisHandle | null {
  try {
    const v: unknown = JSON.parse(sessionStorage.getItem(HANDLE_KEY) ?? "null");
    if (v === null) return null;
    if (!object(v) || !digest(owner) || v.ownerContext !== owner || !ref(v.creationKey)) return invalid();
    exact(v, [
      "ownerContext",
      "creationKey",
      "input",
      "id",
      "revision",
      "sourceDigest",
      "startKey",
      "startRevision",
      "bindingLocked",
    ]);
    if (v.bindingLocked !== undefined && v.bindingLocked !== true) return invalid();
    const input = parseInput(v.input);
    if (v.id !== undefined && (!ref(v.id) || !natural(v.revision) || !v.revision || !digest(v.sourceDigest)))
      return invalid();
    if (v.id === undefined && (v.revision !== undefined || v.sourceDigest !== undefined)) return invalid();
    if (
      v.startKey !== undefined &&
      (!ref(v.startKey) ||
        !v.id ||
        !natural(v.startRevision) ||
        !v.startRevision ||
        v.startRevision > Number(v.revision))
    )
      return invalid();
    if (v.startKey === undefined && v.startRevision !== undefined) return invalid();
    return {
      ownerContext: owner,
      creationKey: v.creationKey,
      input,
      ...(v.id ? { id: String(v.id), revision: Number(v.revision), sourceDigest: String(v.sourceDigest) } : {}),
      ...(v.startKey ? { startKey: String(v.startKey), startRevision: Number(v.startRevision) } : {}),
      ...(v.bindingLocked ? { bindingLocked: true as const } : {}),
    };
  } catch {
    storeURLHandle(null);
    return null;
  }
}
export function matchURLHandle(job: URLDiagnosisJob, handle: URLDiagnosisHandle): URLDiagnosisJob {
  if (!sameURLInput(job.input, handle.input)) return invalid();
  if (handle.id)
    matchURLJob(
      job,
      {
        id: handle.id,
        revision: handle.revision ?? 0,
        source_digest: handle.sourceDigest ?? "",
        input: handle.input,
      },
      { allowPreStartRebind: !handle.startKey && !handle.bindingLocked },
    );
  return job;
}
export const urlReasonLabels: Record<string, string> = {
  installation_unavailable: "설치자의 보호된 실행 설정을 사용할 수 없어요.",
  installation_model_invalid:
    "모델 설치·제공자·비용 한도 또는 승인 만료·소비 여부를 확인해야 해요. 운영자에게 새 승인을 요청해 주세요.",
  installation_check_failed: "모델 키·프로필 또는 보호된 설치가 바뀌었거나 사용할 수 없어요. 운영자가 확인해야 해요.",
  approval_expired: "설치된 진단 승인이 만료됐어요.",
  model_budget_invalid: "승인된 모델 요청·토큰·USD 예산을 결속할 수 없어요.",
  model_usage_unknown: "모델 응답·사용량·비용을 확인하지 못했어요. 예약을 유지하고 후속 호출을 차단했어요.",
  diagnostic_logging_incomplete:
    "실행은 끝났지만 진단 기록(운영 로그) 일부가 남지 않았어요. 진단 결과와 감사 기록은 영향을 받지 않아요.",
  planning_failed: "모델 계획 준비가 완료되지 않았어요. 상태와 사용량을 확인해 주세요.",
  model_mode_unsupported: "이 모델 실행 방식은 현재 자동 준비에서 지원하지 않아요. 다른 모델로 전환하지 않아요.",
  path_not_approved: "이 URL 경로는 설치된 승인 범위에 없어요.",
  target_not_approved: "이 대상은 설치된 승인 범위에 없어요.",
  profile_not_found: "이 모델 프로필을 사용할 수 없어요.",
  protocol_unsupported: "이 대상의 프로토콜은 현재 진단 경로에서 지원하지 않아요.",
  unsupported_protocol: "이 대상의 프로토콜은 현재 진단 경로에서 지원하지 않아요.",
  approval_required: "설치자 승인이 필요해요.",
  approval_changed: "설치된 승인 내용이 바뀌었어요.",
  budget_exhausted: "승인된 실행·모델 예산이 소진됐어요.",
  unknown: "준비 또는 실행 결과를 확인하지 못했어요.",
};
export function urlDiagnosisError(cause: unknown): string {
  if (cause instanceof ApiError) {
    if (cause.status === 401) return "로그인이 만료됐어요. 다시 로그인한 뒤 같은 진단의 상태를 조회해 주세요.";
    if (cause.status === 403) return "이 진단을 조회하거나 실행할 권한이 없어요.";
    if (cause.status === 404)
      return "저장된 요청을 찾지 못했어요. 실행되지 않았다고 단정하거나 자동 재전송하지 않아요.";
    if (cause.message === "budget_exhausted")
      return "승인된 실행·모델 예산이 소진됐어요. 새 실행을 자동으로 만들지 않아요.";
    if (cause.message === "external_transfer_confirmation_required")
      return "대상 응답·도구 출력의 외부 전송과 USD 비용 상한에 명시적으로 동의해야 시작할 수 있어요.";
    if (cause.message === "approval_required")
      return "설치자 승인이 필요해요. URL 입력이나 사용자 확인만으로 실행 권위가 생기지 않아요.";
    if (cause.status === 409) return "승인 범위·설정 세대·실행 상태가 바뀌었어요. 현재 상태를 다시 조회해 주세요.";
    if (cause.status === 422) return "입력 또는 서버 응답 계약을 확인하지 못했어요. 실행·완료로 판단하지 않아요.";
    if (cause.status === 503) return "준비 서비스의 응답을 확인하지 못했어요. 같은 진단의 상태만 다시 조회해 주세요.";
  }
  return "응답을 확인하지 못했어요. 자동 재전송하지 않고 같은 진단의 상태만 다시 조회해 주세요.";
}

export function parseWorkflowLimits(v: unknown): WorkflowLimits {
  if (
    !object(v) ||
    v.format !== "abc2lab.workbench-flow/v1" ||
    !natural(v.max_pages) ||
    v.max_pages < 1 ||
    v.max_pages > 32 ||
    !natural(v.max_endpoints) ||
    v.max_endpoints < 1 ||
    v.max_endpoints > 32 ||
    !natural(v.max_response_bytes) ||
    v.max_response_bytes < 4096 ||
    v.max_response_bytes > 2097152 ||
    typeof v.allow_reversible_writes !== "boolean"
  )
    return invalid();
  exact(v, ["format", "max_pages", "max_endpoints", "max_response_bytes", "allow_reversible_writes"]);
  return v as WorkflowLimits;
}
