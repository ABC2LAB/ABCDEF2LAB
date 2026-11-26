// 비 IDOR 후보 선택 검증(#950 이동찬, 계약 docs/contracts/selected-candidate-llm-verification.md · 합성 fixture).
// 처음 진단에서 '검토 필요'로 남은 후보를 운영자가 골라 모델이 저장 관찰을 분석하고, 새 요청이 필요하면 계획을 따로 승인해야만
// E② 로 보낸다. 화면은 처리 상태와 판정을 다른 칸으로 둔다 — 모델 판정(model_supported · model_refuted)은 프로그램 확정(E③)과
// 섞지 않고, 분석 준비 · 승인됨을 확정으로 그리지 않는다(계약 '처리 상태와 취약점 판정은 다른 칸이다').

import { ApiError, apiFetch } from "@/lib/diagnosis/api";

export const PROCESS_STATES = [
  "requested",
  "checking_stored_evidence",
  "analysis_ready",
  "awaiting_approval",
  "approved",
  "rejected",
  "running",
  "evidence_ready",
  "assessing",
  "completed",
  "failed",
  "cancelled",
  "expired",
] as const;
export type ProcessState = (typeof PROCESS_STATES)[number];
/** 완료된 결과의 판정(선택 LLM 이력). 프로그램 판정 confirmed · negative 와 다른 값 */
export const MODEL_VERDICTS = ["model_supported", "model_refuted", "insufficient"] as const;
export type ModelVerdict = (typeof MODEL_VERDICTS)[number];

export type SourceRef = { call_ref: string; attempt_ref: string; observation_digest: string };
export type VerificationEvent = {
  process_state: ProcessState;
  /** 완료된 결과에서만. 모르는 값은 null 로 두고 코드를 verdictCode 에 남긴다 */
  verdict: ModelVerdict | null;
  verdictCode: string | null;
  reason: string;
  sources: SourceRef[];
  at: string | null;
};
export type ProposedAction = { purpose: string; method: string; path_hint: string; kind?: string };
export type Analysis = {
  digest: string;
  claim: string;
  normal_explanation: string;
  business_questions: string[];
  actions: ProposedAction[];
  limitations: string;
};
export type PlanAction = {
  purpose: string;
  method: string;
  exact_path: string;
  kind?: string;
  query: [string, string][];
  headers: [string, string][];
};
export type LLMPlan = {
  actions: PlanAction[];
  max_requests: number;
  duration_seconds: number;
  auth_preparation?: { method: string; exact_path: string };
  /** 시험 계정이 붙은 계획인지(값은 보이지 않는다) */
  account: boolean;
  excluded_paths: string[];
  expires_at: string | null;
};
export type FinalVerdict = {
  verdict: ModelVerdict | null;
  verdictCode: string;
  claim: string;
  normal_explanation: string;
  reason: string;
  limitations: string;
  remediation: string;
  verification_criteria: string;
  sources: SourceRef[];
};
export type BusinessAnswer = { question_index: number; question: string; answer: string; answered_at: string };
export type Verification = {
  id: string;
  method: string;
  requested_at: string | null;
  latest: VerificationEvent;
  analysis: Analysis | null;
  plan: LLMPlan | null;
  plan_digest: string | null;
  final: FinalVerdict | null;
  answers: BusinessAnswer[];
};
export type VerificationPage = {
  task_id: string;
  candidate_id: string;
  evidence_digest: string;
  automatic: { status: string; reason: string };
  stored_evidence: { state: string; observation_count: number; verdict?: string; reason?: string };
  verifications: Verification[];
  truncated: boolean;
  next_before: string | null;
};

const obj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const str = (v: unknown, max = 8192): v is string => typeof v === "string" && v.length <= max;
const nat = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
const ID = /^[1-9][0-9]{0,18}$/;
const DIGEST = /^[0-9a-f]{64}$/;
function invalid(): never {
  throw new ApiError(422, "invalid_contract");
}
const text = (v: unknown, max = 8192): string => {
  if (v === undefined || v === null) return "";
  if (!str(v, max)) invalid();
  return v;
};
const instant = (v: unknown): string | null => (str(v, 64) && !Number.isNaN(Date.parse(v)) ? v : null);
const list = <T>(v: unknown, f: (x: unknown) => T, max = 64): T[] => {
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v) || v.length > max) invalid();
  return v.map(f);
};
const pairs = (v: unknown): [string, string][] => {
  if (v === undefined || v === null) return [];
  if (!obj(v)) invalid();
  const out = Object.entries(v);
  if (out.length > 8 || !out.every(([k, x]) => str(k, 128) && str(x, 512))) invalid();
  return out as [string, string][];
};
const verdictOf = (v: unknown): ModelVerdict | null =>
  MODEL_VERDICTS.includes(v as ModelVerdict) ? (v as ModelVerdict) : null;

function sourceOf(v: unknown): SourceRef {
  if (!obj(v) || !str(v.call_ref, 256) || !str(v.attempt_ref, 256) || !str(v.observation_digest, 128)) invalid();
  return { call_ref: v.call_ref, attempt_ref: v.attempt_ref, observation_digest: v.observation_digest };
}
function eventOf(v: unknown): VerificationEvent {
  if (!obj(v) || !PROCESS_STATES.includes(v.process_state as ProcessState) || !str(v.reason, 128)) invalid();
  if (v.verdict !== null && v.verdict !== undefined && !str(v.verdict, 64)) invalid();
  const code = typeof v.verdict === "string" ? v.verdict : null;
  // 판정은 완료된 결과에서만 받는다 — 진행 중 상태에 판정이 붙어 오면 확정처럼 보이지 않게 버린다
  const done = v.process_state === "completed";
  return {
    process_state: v.process_state as ProcessState,
    verdict: done ? verdictOf(code) : null,
    verdictCode: done ? code : null,
    reason: v.reason,
    sources: list(v.sources, sourceOf),
    at: instant(v.at),
  };
}
function analysisOf(v: unknown): Analysis | null {
  if (v === undefined || v === null) return null;
  if (!obj(v) || !str(v.digest, 64) || !DIGEST.test(v.digest) || !obj(v.proposal)) invalid();
  const p = v.proposal;
  return {
    digest: v.digest,
    claim: text(p.claim),
    normal_explanation: text(p.normal_explanation),
    business_questions: list(p.business_questions, (q) => text(q, 2048), 4),
    actions: list(p.actions, (a) => {
      if (!obj(a) || !str(a.method, 16) || !str(a.path_hint, 2048)) invalid();
      return {
        purpose: text(a.purpose),
        method: a.method,
        path_hint: a.path_hint,
        ...(a.kind ? { kind: text(a.kind, 64) } : {}),
      };
    }),
    limitations: text(p.limitations),
  };
}
function planOf(v: unknown): LLMPlan | null {
  if (v === undefined || v === null) return null;
  if (!obj(v) || !nat(v.max_requests) || !nat(v.duration_seconds)) invalid();
  const auth = v.auth_preparation;
  if (auth !== undefined && auth !== null && (!obj(auth) || !str(auth.method, 16) || !str(auth.exact_path, 2048)))
    invalid();
  return {
    actions: list(v.actions, (a) => {
      if (!obj(a) || !str(a.method, 16) || !str(a.exact_path, 2048)) invalid();
      return {
        purpose: text(a.purpose),
        method: a.method,
        exact_path: a.exact_path,
        ...(a.kind ? { kind: text(a.kind, 64) } : {}),
        query: pairs(a.query),
        headers: pairs(a.headers),
      };
    }),
    max_requests: v.max_requests,
    duration_seconds: v.duration_seconds,
    ...(obj(auth)
      ? { auth_preparation: { method: auth.method as string, exact_path: auth.exact_path as string } }
      : {}),
    account: typeof v.account_ref === "string" && v.account_ref !== "",
    excluded_paths: list(v.excluded_paths, (x) => text(x, 1024), 256),
    expires_at: instant(v.expires_at),
  };
}
function finalOf(v: unknown): FinalVerdict | null {
  if (v === undefined || v === null) return null;
  if (!obj(v) || !str(v.verdict, 64)) invalid();
  return {
    verdict: verdictOf(v.verdict),
    verdictCode: v.verdict,
    claim: text(v.claim),
    normal_explanation: text(v.normal_explanation),
    reason: text(v.reason),
    limitations: text(v.limitations),
    remediation: text(v.remediation),
    verification_criteria: text(v.verification_criteria),
    sources: list(v.sources, sourceOf),
  };
}
function answerOf(v: unknown): BusinessAnswer {
  if (!obj(v) || !nat(v.question_index) || !str(v.answer, 4096) || !str(v.answered_at, 64)) invalid();
  return {
    question_index: v.question_index,
    question: text(v.question, 2048),
    answer: v.answer,
    answered_at: v.answered_at,
  };
}
function verificationOf(v: unknown, candidate: string): Verification {
  if (!obj(v) || !str(v.id, 20) || !ID.test(v.id) || !str(v.method, 32)) invalid();
  if (v.candidate_id !== undefined && v.candidate_id !== candidate) invalid();
  if (v.plan_digest !== undefined && !(str(v.plan_digest, 64) && DIGEST.test(v.plan_digest))) invalid();
  return {
    id: v.id,
    method: v.method,
    requested_at: instant(v.requested_at),
    latest: eventOf(v.latest),
    analysis: analysisOf(v.analysis),
    plan: planOf(v.llm_plan),
    plan_digest: typeof v.plan_digest === "string" ? v.plan_digest : null,
    final: finalOf(v.final_verdict),
    answers: list(v.business_answers, answerOf, 8),
  };
}

/** 이력 쪽. 물은 작업 · 후보와 다른 응답은 버린다(다른 후보 결과를 이 상세에 붙이지 않음) */
export function parseVerificationPage(v: unknown, task: string, candidate: string): VerificationPage {
  if (!obj(v) || v.task_id !== task || v.candidate_id !== candidate) invalid();
  if (!str(v.evidence_digest, 64) || !DIGEST.test(v.evidence_digest)) invalid();
  const a = v.automatic;
  const st = v.stored_evidence;
  if (!obj(a) || !str(a.status, 64) || !str(a.reason, 128)) invalid();
  if (!obj(st) || !str(st.state, 64) || !nat(st.observation_count)) invalid();
  return {
    task_id: v.task_id,
    candidate_id: v.candidate_id,
    evidence_digest: v.evidence_digest,
    automatic: { status: a.status, reason: a.reason },
    stored_evidence: {
      state: st.state,
      observation_count: st.observation_count,
      ...(typeof st.verdict === "string" ? { verdict: st.verdict } : {}),
      ...(typeof st.reason === "string" ? { reason: st.reason } : {}),
    },
    verifications: list(v.verifications, (x) => verificationOf(x, candidate), 200),
    truncated: v.truncated === true,
    next_before: typeof v.next_before === "string" && ID.test(v.next_before) ? v.next_before : null,
  };
}

function base(task: string, candidate: string): string {
  if (!ID.test(task) || !ID.test(candidate)) invalid();
  return `/api/product/tasks/${task}/candidates/${candidate}/verifications`;
}
function actionPath(task: string, candidate: string, verification: string, action: string): string {
  if (!ID.test(verification)) invalid();
  return `${base(task, candidate)}/${verification}/${action}`;
}
const post = (path: string, body: unknown) =>
  apiFetch<unknown>(path, { method: "POST", cache: "no-store", redirect: "error", body: JSON.stringify(body) }, false);

/** 이력 조회(저장 자료만 — 대상 · 모델 송신 없음). before 는 앞 쪽의 next_before */
export async function readVerifications(
  task: string,
  candidate: string,
  before?: string | null,
  signal?: AbortSignal,
): Promise<VerificationPage> {
  const q = before ? `?before=${encodeURIComponent(before)}` : "";
  return parseVerificationPage(
    await apiFetch<unknown>(`${base(task, candidate)}${q}`, { cache: "no-store", redirect: "error", signal }, false),
    task,
    candidate,
  );
}

/** 같은 요청을 다시 보내도 이력이 하나만 생기게 하는 요청 키(서버 형식 [A-Za-z0-9][A-Za-z0-9._-]{7,127}) */
export function newRequestKey(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return `ui-${Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")}`;
}

/** 후보 선택(검증 요청). 원본 근거 판본(evidence_digest)이 바뀌었으면 서버가 409 candidate_evidence_changed */
export async function requestVerification(
  task: string,
  candidate: string,
  evidenceDigest: string,
  requestKey: string,
): Promise<void> {
  await post(base(task, candidate), { request_id: requestKey, evidence_digest: evidenceDigest, method: "llm/v1" });
}
/** 업무 질문 답(질문 번호는 1부터) */
export async function answerQuestion(
  task: string,
  candidate: string,
  verification: string,
  analysisDigest: string,
  index: number,
  answer: string,
): Promise<void> {
  await post(actionPath(task, candidate, verification, "answers"), {
    analysis_digest: analysisDigest,
    question_index: index,
    answer,
  });
}
/** 실행 계획 만들기. 저장 관찰만으로 평가할 수 있으면 서버가 409 stored_evidence_assessment_pending — 이력에서 진행을 본다 */
export async function makePlan(task: string, candidate: string, verification: string, analysisDigest: string) {
  await post(actionPath(task, candidate, verification, "plan"), { analysis_digest: analysisDigest });
}
/** 계획 승인 · 거부(계획 판본에 묶음) */
export async function decidePlan(
  task: string,
  candidate: string,
  verification: string,
  planDigest: string,
  decision: "approved" | "rejected",
) {
  await post(actionPath(task, candidate, verification, "decision"), { plan_digest: planDigest, decision });
}
/** 취소. 계획이 있으면 그 판본을 같이 보낸다(실제 송신과 겹칠 수 있어 부른 뒤 이력을 다시 읽는다) */
export async function cancelVerification(
  task: string,
  candidate: string,
  verification: string,
  planDigest: string | null,
) {
  await post(actionPath(task, candidate, verification, "cancel"), planDigest ? { plan_digest: planDigest } : {});
}

/** 아직 끝나지 않은 상태(이 동안 몇 초마다 다시 읽는다) */
export const ACTIVE_STATES: readonly ProcessState[] = [
  "requested",
  "checking_stored_evidence",
  "approved",
  "running",
  "evidence_ready",
  "assessing",
];
/** 운영자 입력을 기다리는 상태 */
export const WAITING_STATES: readonly ProcessState[] = ["analysis_ready", "awaiting_approval"];
export const isOpen = (s: ProcessState) => ACTIVE_STATES.includes(s) || WAITING_STATES.includes(s);
/** 서버가 받는 취소 상태(llm/v1, server/candidate_verification_cancel.go) — 저장 관찰 평가 중 assessing 은 서버가 다시 가린다 */
export const CANCELABLE: readonly ProcessState[] = [
  "requested",
  "checking_stored_evidence",
  "analysis_ready",
  "awaiting_approval",
  "assessing",
  "approved",
  "running",
];

export const STATE_TEXT: Readonly<Record<ProcessState, string>> = {
  requested: "요청됨",
  checking_stored_evidence: "저장 근거 확인 중",
  analysis_ready: "분석 준비됨",
  awaiting_approval: "계획 승인 대기",
  approved: "승인됨",
  rejected: "계획 거부됨",
  running: "검증 요청 보내는 중",
  evidence_ready: "근거 모음",
  assessing: "모델 판정 중",
  completed: "완료",
  failed: "실패",
  cancelled: "취소됨",
  expired: "만료",
};
/** 모델 판정 딱지(계약: 관찰 확인됨 · 모델 판정 / 아님 · 모델 판정) — 프로그램 확정과 다른 글 */
export const VERDICT_TEXT: Readonly<Record<ModelVerdict, string>> = {
  model_supported: "관찰 확인됨 · 모델 판정",
  model_refuted: "아님 · 모델 판정",
  insufficient: "판단 불가 · 근거 부족",
};
export const REASON_TEXT: Readonly<Record<string, string>> = {
  selected_by_user: "운영자가 검증을 요청함",
  analysis_available: "모델 분석이 준비됨",
  action_approval_required: "새 요청 계획에 승인이 필요함",
  user_approved: "운영자가 계획을 승인함",
  user_rejected: "운영자가 계획을 거부함",
  user_cancelled: "운영자가 취소함",
  execution_started: "승인한 요청을 보내는 중",
  final_model_started: "모델이 근거를 대조하는 중",
  stored_final_model_started: "저장된 관찰로 모델이 판정하는 중",
  model_evidence_checked: "새로 보낸 요청의 관찰로 판정함",
  stored_model_evidence_checked: "저장된 관찰로 판정함(새 요청 없음)",
  final_model_interrupted: "모델 판정이 끊김(자동으로 다시 보내지 않음)",
  analysis_unavailable: "모델 분석을 할 수 없음",
  analysis_interrupted: "모델 분석이 끊김(자동으로 다시 보내지 않음)",
  analysis_timeout: "모델 분석 시간 상한에 닿음",
  budget_unavailable: "설치 예산이 남지 않음",
  candidate_execution_unavailable: "검증 실행을 시작하지 못함",
  candidate_execution_cancelled: "검증 실행이 취소됨",
  candidate_execution_timeout: "검증 실행 시간이 지남",
  selected_verification_required: "처음 진단: 검토 필요",
  independent_observation_missing: "독립된 관찰이 없음",
};
/** 동작 실패 코드(오류 본문 {"error": code}) → 사람이 읽는 글 */
export const ERROR_TEXT: Readonly<Record<string, string>> = {
  verification_already_pending: "이 후보에 끝나지 않은 검증 요청이 이미 있어요.",
  candidate_evidence_changed: "후보 근거가 바뀌었어요. 다시 읽은 뒤 요청해 주세요.",
  idor_requires_e3: "IDOR 후보는 확정 단계(E③)에서 판정해요.",
  selected_verification_method_required: "이 점검은 선택 검증 대상 회차가 아니에요.",
  diagnosis_not_completed: "점검이 정상 종료된 뒤에 검증할 수 있어요.",
  analysis_changed: "모델 분석 판본이 바뀌었어요. 다시 읽어 주세요.",
  analysis_not_ready: "분석이 아직 준비되지 않았어요.",
  business_answer_conflict: "이 질문엔 이미 다른 답이 저장돼 있어요.",
  stored_evidence_assessment_pending: "저장된 관찰만으로 판정할 수 있어 새 요청 계획 없이 판정을 진행해요.",
  verification_plan_changed: "계획 판본이 바뀌었어요. 다시 읽은 뒤 결정해 주세요.",
  verification_plan_expired: "계획 승인 기한이 지났어요.",
  verification_already_decided: "이미 결정된 계획이에요.",
  verification_cancel_unavailable: "지금 상태에서는 취소할 수 없어요.",
  verification_scope_denied: "이 점검의 검증 권한이 없어요.",
  analysis_schedule_unavailable: "모델 분석을 예약하지 못했어요(설치 예산 · 모델 설정을 확인해 주세요).",
};
export const errorText = (e: unknown): string => {
  if (!(e instanceof ApiError)) return "요청을 보내지 못했어요(연결 실패).";
  return ERROR_TEXT[e.message] ?? `요청이 거절됐어요 (HTTP ${e.status} · ${e.message})`;
};
