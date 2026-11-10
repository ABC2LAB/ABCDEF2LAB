import { ApiError, apiFetch } from "./api";
import { validTaskID } from "./first-diagnosis";

const base = "/api/diagnoses/v1";
const version = "diagnosis-api.v1";
export type DiagnosisConfiguration = {
  target_ref: string;
  scope_ref: string;
  model_profile_ref: string;
  approval_mode: "confirm_every_request" | "preauthorized_scope";
  budget: { requests: number; tokens: number; cost_usd_micros: number; duration_seconds: number };
};
export type DiagnosisProfile = { id: string; label: string; configuration: DiagnosisConfiguration };
export type DiagnosisDraft = {
  id: string;
  revision: number;
  source_digest: string;
  configuration: DiagnosisConfiguration;
  state:
    | "draft"
    | "awaiting_approval"
    | "prepared"
    | "running"
    | "stopping"
    | "completed"
    | "cancelled"
    | "failed"
    | "unknown";
  task_id?: string;
  drain_complete?: boolean;
  result_ready?: boolean;
};
const states = new Set([
  "draft",
  "awaiting_approval",
  "prepared",
  "running",
  "stopping",
  "completed",
  "cancelled",
  "failed",
  "unknown",
]);
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const ref = (v: unknown): v is string => typeof v === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/.test(v);
const natural = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0;
function invalid(): never {
  throw new ApiError(422, "invalid_contract");
}
function configuration(v: unknown): DiagnosisConfiguration {
  if (
    !object(v) ||
    !ref(v.target_ref) ||
    !ref(v.scope_ref) ||
    !ref(v.model_profile_ref) ||
    (v.approval_mode !== "confirm_every_request" && v.approval_mode !== "preauthorized_scope") ||
    !object(v.budget)
  )
    return invalid();
  const b = v.budget;
  if (
    ![b.requests, b.tokens, b.cost_usd_micros, b.duration_seconds].every(natural) ||
    !b.requests ||
    !b.tokens ||
    !b.duration_seconds ||
    Number(b.requests) > 1_000_000 ||
    Number(b.tokens) > 1_000_000_000 ||
    Number(b.cost_usd_micros) > 1_000_000_000_000 ||
    Number(b.duration_seconds) > 86400
  )
    return invalid();
  return {
    target_ref: v.target_ref,
    scope_ref: v.scope_ref,
    model_profile_ref: v.model_profile_ref,
    approval_mode: v.approval_mode,
    budget: {
      requests: Number(b.requests),
      tokens: Number(b.tokens),
      cost_usd_micros: Number(b.cost_usd_micros),
      duration_seconds: Number(b.duration_seconds),
    },
  };
}
export function sameDiagnosisConfiguration(a: DiagnosisConfiguration, b: DiagnosisConfiguration): boolean {
  return (
    a.target_ref === b.target_ref &&
    a.scope_ref === b.scope_ref &&
    a.model_profile_ref === b.model_profile_ref &&
    a.approval_mode === b.approval_mode &&
    a.budget.requests === b.budget.requests &&
    a.budget.tokens === b.budget.tokens &&
    a.budget.cost_usd_micros === b.budget.cost_usd_micros &&
    a.budget.duration_seconds === b.budget.duration_seconds
  );
}
function validSnapshot(value: unknown, task: unknown, state: string): boolean {
  if (
    !object(value) ||
    typeof task !== "string" ||
    !validTaskID(task) ||
    value.task_id !== task ||
    !ref(value.preparation_ref) ||
    value.state !== state ||
    typeof value.started !== "boolean" ||
    typeof value.drain_complete !== "boolean" ||
    typeof value.result_ready !== "boolean"
  )
    return false;
  switch (state) {
    case "prepared":
      return !value.started && !value.drain_complete && !value.result_ready;
    case "running":
    case "stopping":
      return value.started && !value.drain_complete && !value.result_ready;
    case "completed":
    case "cancelled":
    case "failed":
      return value.started && value.drain_complete && value.result_ready;
    case "unknown":
      return value.started === true;
    default:
      return false;
  }
}
function draft(v: unknown, requireStarted = false): DiagnosisDraft {
  if (!object(v) || v.version !== version || !object(v.draft)) return invalid();
  const d = v.draft;
  if (requireStarted && (!object(d.snapshot) || d.snapshot.started !== true)) return invalid();
  if (
    !ref(d.id) ||
    !natural(d.revision) ||
    d.revision < 1 ||
    typeof d.source_digest !== "string" ||
    !/^[0-9a-f]{64}$/.test(d.source_digest) ||
    typeof d.state !== "string" ||
    !states.has(d.state) ||
    (d.task_id !== undefined && (typeof d.task_id !== "string" || !validTaskID(d.task_id)))
  )
    return invalid();
  if (d.state === "draft" || d.state === "awaiting_approval") {
    if (d.task_id !== undefined || d.snapshot !== undefined) return invalid();
  } else if (d.state !== "unknown") {
    if (!d.task_id || !validSnapshot(d.snapshot, d.task_id, d.state)) return invalid();
  } else if (d.snapshot !== undefined && !validSnapshot(d.snapshot, d.task_id, "unknown")) return invalid();
  return {
    id: d.id,
    revision: d.revision,
    source_digest: d.source_digest,
    state: d.state as DiagnosisDraft["state"],
    configuration: configuration(d.configuration),
    ...(d.task_id ? { task_id: String(d.task_id) } : {}),
    ...(object(d.snapshot)
      ? {
          drain_complete: d.snapshot.drain_complete as boolean,
          result_ready: d.snapshot.result_ready as boolean,
        }
      : {}),
  };
}
export async function readDiagnosisProfiles(): Promise<DiagnosisProfile[]> {
  const raw = await apiFetch<unknown>(`${base}/catalog`, { cache: "no-store" });
  if (!object(raw) || raw.version !== version || !Array.isArray(raw.profiles)) return invalid();
  const seen = new Set<string>();
  return raw.profiles.map((p: unknown) => {
    if (!object(p) || !ref(p.id) || typeof p.label !== "string" || p.label.length > 256 || seen.has(p.id))
      return invalid();
    seen.add(p.id);
    return { id: p.id, label: p.label, configuration: configuration(p.configuration) };
  });
}
function sameDraft(result: DiagnosisDraft, current: DiagnosisDraft): DiagnosisDraft {
  if (
    result.id !== current.id ||
    result.revision !== current.revision ||
    result.source_digest !== current.source_digest ||
    !sameDiagnosisConfiguration(result.configuration, current.configuration)
  )
    return invalid();
  return result;
}
export async function createDiagnosisDraft(config: DiagnosisConfiguration, key: string): Promise<DiagnosisDraft> {
  const result = draft(
    await apiFetch<unknown>(
      `${base}/drafts`,
      {
        method: "POST",
        headers: { "Idempotency-Key": key },
        body: JSON.stringify({ version, configuration: config }),
      },
      false,
    ),
  );
  if (result.revision !== 1 || result.state !== "draft" || !sameDiagnosisConfiguration(result.configuration, config))
    return invalid();
  return result;
}
export async function readCreatedDiagnosis(key: string): Promise<DiagnosisDraft> {
  return draft(await apiFetch<unknown>(`${base}/drafts`, { cache: "no-store", headers: { "Idempotency-Key": key } }));
}
export async function updateDiagnosisDraft(
  current: DiagnosisDraft,
  config: DiagnosisConfiguration,
): Promise<DiagnosisDraft> {
  const result = draft(
    await apiFetch<unknown>(
      `${base}/drafts/${encodeURIComponent(current.id)}`,
      {
        method: "PATCH",
        body: JSON.stringify({ version, expected_revision: current.revision, configuration: config }),
      },
      false,
    ),
  );
  if (
    result.id !== current.id ||
    result.revision !== current.revision + 1 ||
    result.state !== "draft" ||
    !sameDiagnosisConfiguration(result.configuration, config)
  )
    return invalid();
  return result;
}
export async function commandDiagnosis(
  current: DiagnosisDraft,
  command: "validate" | "start" | "stop",
  key?: string,
): Promise<DiagnosisDraft> {
  const result = sameDraft(
    draft(
      await apiFetch<unknown>(
        `${base}/drafts/${encodeURIComponent(current.id)}/${command}`,
        {
          method: "POST",
          ...(key ? { headers: { "Idempotency-Key": key } } : {}),
          body: JSON.stringify({ version, expected_revision: current.revision }),
        },
        false,
      ),
      command === "start",
    ),
    current,
  );
  if (command === "validate" && result.state !== "prepared" && result.state !== "awaiting_approval") return invalid();
  if ((command === "start" || command === "stop") && result.task_id !== current.task_id) return invalid();
  if (command === "start" && ["draft", "awaiting_approval", "prepared"].includes(result.state)) return invalid();
  if (command === "stop" && ["draft", "awaiting_approval", "prepared", "running"].includes(result.state))
    return invalid();
  return result;
}
export async function readDiagnosisDraft(id: string): Promise<DiagnosisDraft> {
  const result = draft(
    await apiFetch<unknown>(`${base}/drafts/${encodeURIComponent(id)}/status`, { cache: "no-store" }),
  );
  if (result.id !== id) return invalid();
  return result;
}

/** 저장된 내용은 조회 핸들뿐이다. 상태·설정·판정은 항상 서버에서 다시 확인한다. */
export type DiagnosisHandle = { id?: string; creationKey: string; startKey?: string };
const HANDLE_KEY = "abc2lab.diagnosisHandle.v1";
export function readDiagnosisHandle(): DiagnosisHandle | null {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(HANDLE_KEY) ?? "null");
    if (
      object(value) &&
      ref(value.creationKey) &&
      (value.id === undefined || ref(value.id)) &&
      (value.startKey === undefined || ref(value.startKey))
    ) {
      return {
        creationKey: value.creationKey,
        ...(value.id ? { id: String(value.id) } : {}),
        ...(value.startKey ? { startKey: String(value.startKey) } : {}),
      };
    }
    sessionStorage.removeItem(HANDLE_KEY);
  } catch {
    /* 저장소 사용 불가 시 메모리 핸들만 쓴다. */
  }
  return null;
}
export function storeDiagnosisHandle(value: DiagnosisHandle | null): void {
  try {
    if (value) sessionStorage.setItem(HANDLE_KEY, JSON.stringify(value));
    else sessionStorage.removeItem(HANDLE_KEY);
  } catch {
    /* 서버 상태나 비밀정보를 다른 저장소로 복원하지 않는다. */
  }
}
export const draftStateLabel: Record<DiagnosisDraft["state"], string> = {
  draft: "초안",
  awaiting_approval: "운영자 승인 대기",
  prepared: "실행 준비 확인",
  running: "실행 중",
  stopping: "중지·마감 중",
  completed: "실행 마감",
  cancelled: "중지 후 마감",
  failed: "실행 실패",
  unknown: "결과 불명",
};
export function managementError(error: unknown): string {
  if (!(error instanceof ApiError))
    return "응답을 확인하지 못했어요. 자동 재전송하지 않고 현재 상태를 다시 조회해 주세요.";
  if (error.status === 401) return "로그인이 만료됐어요. 다시 로그인해 주세요.";
  if (error.status === 403 || error.status === 404 || error.status === 503)
    return "이 서버에서 진단 관리 기능 또는 승인된 설정을 사용할 수 없어요. 기존 CLI 진단 연결은 계속 사용할 수 있어요.";
  if (error.status === 409)
    return "설정 세대·운영자 승인·실행 상태가 바뀌었어요. 현재 상태를 다시 읽고 승인 내용을 확인해 주세요.";
  return "응답 계약을 확인하지 못했어요. 실행이나 완료로 판단하지 않아요.";
}
