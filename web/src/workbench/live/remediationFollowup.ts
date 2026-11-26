// 저장 보고서 이행점검 (계약 docs/contracts/remediation-followup.md, 이동찬 11/24). 원래 보고서의 조치마다, 같은 대상으로 뒤에 돌린
// 점검의 관찰을 근거로 담당자가 수정 확인 · 다시 관찰됨 · 확인 불가를 판정한다. 서버가 대상 · 회차 순서 · 근거를 다시 대조하고,
// 화면은 그 결과를 그대로 보인다(판정을 지어내지 않는다).

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

export const FOLLOWUP_RESULTS = ["fixed", "reproduced", "inconclusive"] as const;
export type FollowupResult = (typeof FOLLOWUP_RESULTS)[number];
/** 확인 불가로 저장할 때 고르는 이유(계약) */
export const INCONCLUSIVE_REASONS = [
  "insufficient_evidence",
  "outside_scope",
  "conditions_changed",
  "access_unavailable",
] as const;
export type InconclusiveReason = (typeof INCONCLUSIVE_REASONS)[number];

export type FollowupRequest = { method: string; path: string; vuln_class?: string };
export type FollowupObservation = FollowupRequest & {
  step_id: string;
  binding_digest: string;
  number: number;
  http_status: number;
  reviewable: boolean;
};
export type FollowupFinding = {
  entry_id: string;
  title: string;
  outcome: string;
  observations: FollowupObservation[];
  seal_warning?: string;
};
export type FollowupRun = {
  task_id?: string;
  state: string;
  run_outcome?: string;
  started: boolean;
  result_ready: boolean;
  results_state: string;
  unresolved_entries: number;
  findings: FollowupFinding[];
};
export type FollowupReview = {
  evidence_entry_id?: string;
  step_id?: string;
  binding_digest?: string;
  reviewer: string;
  revision: number;
  reviewed_at: string;
};
export type FollowupItem = {
  entry_id: string;
  record_id: string;
  title: string;
  original_observation: string;
  criteria: string[];
  original_digest?: string;
  requested_changes: string[];
  original_requests: FollowupRequest[];
  /** 아는 결과만(모르는 값은 확인 불가로 보지 않고 받지 않는다) */
  result: FollowupResult;
  reason?: string;
  review?: FollowupReview;
};
export type FollowupScope = {
  state: string;
  paths: string[];
  methods: string[];
  request_budget: number;
  expires_at: string;
};
export type FollowupPage = {
  source_task: string;
  target_id: string;
  target_url: string;
  current_scope: FollowupScope;
  items: FollowupItem[];
  current: FollowupRun;
  general: FollowupRun;
  additional: { task_id: string; entry_id: string }[];
};

/** 판정 저장 본문(PUT, 계약 '판정 저장과 근거 변경') */
export type FollowupEdit = {
  entry_id: string;
  record_id: string;
  original_digest: string;
  evidence_entry_id?: string;
  step_id?: string;
  binding_digest?: string;
  outcome: FollowupResult;
  reason?: InconclusiveReason;
  conditions_comparable: boolean;
  all_criteria_met: boolean;
  expected_revision: number;
};

const obj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const str = (v: unknown, max = 4096): v is string => typeof v === "string" && v.length <= max;
const nat = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
const instant = (v: unknown): v is string => str(v, 64) && v.length > 0 && !Number.isNaN(Date.parse(v));
const TASK = /^[1-9][0-9]*$/;
function invalid(): never {
  throw new ApiError(422, "invalid_contract");
}
// 서버의 빈 Go 슬라이스는 null 로 온다(criteria · paths · methods) — 빈 목록으로 받는다
const strings = (v: unknown, max = 64): string[] => {
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v) || v.length > 256 || !v.every((x) => str(x, max))) invalid();
  return v as string[];
};
const list = <T>(v: unknown, f: (x: unknown) => T): T[] => {
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v) || v.length > 2000) invalid();
  return v.map(f);
};

function requestOf(v: unknown): FollowupRequest {
  if (!obj(v) || !str(v.method, 16) || !str(v.path, 1024) || (v.vuln_class !== undefined && !str(v.vuln_class, 64)))
    invalid();
  return { method: v.method, path: v.path, ...(v.vuln_class ? { vuln_class: v.vuln_class as string } : {}) };
}
function observationOf(v: unknown): FollowupObservation {
  const r = requestOf(v);
  const o = v as Record<string, unknown>;
  if (!str(o.step_id, 128) || !str(o.binding_digest, 128) || !nat(o.number) || !nat(o.http_status)) invalid();
  if (typeof o.reviewable !== "boolean") invalid();
  return {
    ...r,
    step_id: o.step_id,
    binding_digest: o.binding_digest,
    number: o.number,
    http_status: o.http_status,
    reviewable: o.reviewable,
  };
}
function findingOf(v: unknown): FollowupFinding {
  if (!obj(v) || !str(v.entry_id, 128) || !str(v.title) || !str(v.outcome, 64)) invalid();
  if (v.seal_warning !== undefined && !str(v.seal_warning, 256)) invalid();
  return {
    entry_id: v.entry_id,
    title: v.title,
    outcome: v.outcome,
    observations: list(v.observations, observationOf),
    ...(v.seal_warning ? { seal_warning: v.seal_warning as string } : {}),
  };
}
function runOf(v: unknown): FollowupRun {
  if (!obj(v) || !str(v.state, 64) || typeof v.started !== "boolean" || typeof v.result_ready !== "boolean") invalid();
  if (!str(v.results_state, 64) || !nat(v.unresolved_entries)) invalid();
  if (v.task_id !== undefined && !(str(v.task_id, 32) && TASK.test(v.task_id))) invalid();
  return {
    ...(v.task_id ? { task_id: v.task_id as string } : {}),
    state: v.state,
    ...(v.run_outcome ? { run_outcome: String(v.run_outcome) } : {}),
    started: v.started,
    result_ready: v.result_ready,
    results_state: v.results_state,
    unresolved_entries: v.unresolved_entries,
    findings: list(v.findings, findingOf),
  };
}
function reviewOf(v: unknown): FollowupReview | undefined {
  if (v === undefined) return undefined;
  if (!obj(v) || !str(v.reviewer, 256) || !nat(v.revision) || !instant(v.reviewed_at)) invalid();
  return {
    ...(v.evidence_entry_id ? { evidence_entry_id: String(v.evidence_entry_id) } : {}),
    ...(v.step_id ? { step_id: String(v.step_id) } : {}),
    ...(v.binding_digest ? { binding_digest: String(v.binding_digest) } : {}),
    reviewer: v.reviewer,
    revision: v.revision,
    reviewed_at: v.reviewed_at,
  };
}
function itemOf(v: unknown): FollowupItem {
  if (!obj(v) || !str(v.entry_id, 128) || !str(v.record_id, 32) || !str(v.title) || !str(v.original_observation, 20000))
    invalid();
  if (!FOLLOWUP_RESULTS.includes(v.result as FollowupResult)) invalid();
  if (v.reason !== undefined && !str(v.reason, 64)) invalid();
  if (v.original_digest !== undefined && !str(v.original_digest, 128)) invalid();
  const review = reviewOf(v.review);
  return {
    entry_id: v.entry_id,
    record_id: v.record_id,
    title: v.title,
    original_observation: v.original_observation,
    criteria: strings(v.criteria, 4096),
    ...(v.original_digest ? { original_digest: v.original_digest as string } : {}),
    requested_changes: strings(v.requested_changes, 4096),
    original_requests: list(v.original_requests, requestOf),
    result: v.result as FollowupResult,
    ...(v.reason ? { reason: v.reason as string } : {}),
    ...(review ? { review } : {}),
  };
}

export function parseFollowup(v: unknown, source: string): FollowupPage {
  if (!obj(v) || v.source_task !== source || !str(v.target_id, 128) || !str(v.target_url, 2048)) invalid();
  const sc = v.current_scope;
  if (!obj(sc) || !str(sc.state, 64) || !nat(sc.request_budget) || !str(sc.expires_at, 64)) invalid();
  if (!Array.isArray(v.items) || !obj(v.current) || !obj(v.general)) invalid();
  const additional = list(v.additional, (x) => {
    if (!obj(x) || !str(x.task_id, 32) || !str(x.entry_id, 128)) invalid();
    return { task_id: x.task_id, entry_id: x.entry_id };
  });
  return {
    source_task: v.source_task,
    target_id: v.target_id,
    target_url: v.target_url,
    current_scope: {
      state: sc.state,
      paths: strings(sc.paths, 1024),
      methods: strings(sc.methods, 16),
      request_budget: sc.request_budget,
      expires_at: sc.expires_at,
    },
    items: list(v.items, itemOf),
    current: runOf(v.current),
    general: runOf(v.general),
    additional,
  };
}

function pathOf(source: string, current?: string, general?: string): string {
  if (!TASK.test(source) || (current && !TASK.test(current)) || (general && !TASK.test(general))) invalid();
  const q = new URLSearchParams();
  if (current) q.set("current_task", current);
  if (general) q.set("general_task", general);
  const qs = q.toString();
  return `/api/tasks/${source}/remediation-followup${qs ? `?${qs}` : ""}`;
}

/** 원래 조치 · (고른) 후속 회차의 이행점검 쪽 읽기. current 없이 부르면 조치 목록만(결과는 확인 불가) */
export async function readFollowup(
  source: string,
  current?: string,
  general?: string,
  signal?: AbortSignal,
): Promise<FollowupPage> {
  return parseFollowup(
    await apiFetch<unknown>(pathOf(source, current, general), { cache: "no-store", redirect: "error", signal }, false),
    source,
  );
}

/** 판정 저장(PUT ?current_task=). 서버가 근거를 다시 대조하고 저장 뒤 쪽 전체를 돌려준다. 근거가 안 맞으면 409 */
export async function saveFollowupReview(source: string, current: string, edit: FollowupEdit): Promise<FollowupPage> {
  return parseFollowup(
    await apiFetch<unknown>(
      pathOf(source, current),
      { method: "PUT", cache: "no-store", redirect: "error", body: JSON.stringify(edit) },
      false,
    ),
    source,
  );
}

/** 판정 근거로 고를 수 있는 관찰: 판정 가능(reviewable)하고 원래 요청과 메서드 · 경로 · 유형이 모두 같은 것(계약) */
export function evidenceFor(
  item: FollowupItem,
  run: FollowupRun,
): { finding: FollowupFinding; obs: FollowupObservation }[] {
  const same = (o: FollowupObservation) =>
    item.original_requests.some(
      (r) => r.method === o.method && r.path === o.path && (r.vuln_class ?? "") === (o.vuln_class ?? ""),
    );
  return run.findings.flatMap((finding) =>
    finding.observations.filter((o) => o.reviewable && same(o)).map((obs) => ({ finding, obs })),
  );
}

export const RESULT_TEXT: Readonly<Record<FollowupResult, string>> = {
  fixed: "수정 확인",
  reproduced: "다시 관찰됨",
  inconclusive: "확인 불가",
};
export const REASON_TEXT: Readonly<Record<string, string>> = {
  insufficient_evidence: "증거 부족",
  outside_scope: "범위 밖",
  conditions_changed: "조건이 바뀜",
  access_unavailable: "접근 불가",
  review_evidence_changed: "판정 뒤 근거가 바뀜 — 다시 판정해야 해요",
  original_evidence_unavailable: "원래 관찰을 비교에 쓸 수 없음",
  no_linked_review: "아직 판정하지 않음",
  not_checked: "후속 점검을 고르지 않음",
  result_pending: "후속 점검 결과 저장 전",
  result_unavailable: "후속 점검 결과를 읽을 수 없음",
};
/** 대상 승인 상태(이 상태일 때만 판정을 저장할 수 있다: review_required · changed) */
export const SCOPE_TEXT: Readonly<Record<string, string>> = {
  review_required: "판정 가능",
  changed: "대상 승인이 바뀜",
  expired: "대상 승인 만료",
  revoked: "대상 승인 철회",
  unavailable: "대상 승인 확인 불가",
};
export const canReview = (scope: FollowupScope) => scope.state === "review_required" || scope.state === "changed";
export const reasonText = (code: string) => {
  if (REASON_TEXT[code]) return REASON_TEXT[code];
  if (code.startsWith("scope_")) return SCOPE_TEXT[code.slice(6)] ?? `대상 승인 상태 ${code.slice(6)}`;
  return code;
};
