// 전송 통제 정책(E②) 읽기 — GET /api/tasks/{id}/control-policy (docs/contracts/workbench-live-data.md §3).
// 서버가 그 task 실행에 결속한 정책(대상 승인 판 + 설치 상한, 지문 결속)의 표시용 투영을 읽기만 한다. 화면에서 고치지 않는다(§6).
// 표현/데이터 분리: 닫힌 파서·판정(만료·쓰기 범위·fail-closed)은 전부 여기(lib), 화면은 그리기만.
//
// 안전(절대규칙 2): 응답에는 자격·세션 값, 감사 키 · CA · helper 경로가 없다(계정은 라벨만, 감사는 결속 여부만).
//   승인된 origin·경로·메서드는 비밀이 아니라 운영자가 봐야 할 '무엇이 승인됐는지'이므로 그대로 보인다.

import { ApiError, apiFetch } from "./api";

export const CONTROL_POLICY_FORMAT = "abc2lab.control-policy-view/v1";

/** 요청 승인 방식 — 엔진 e2proxy.ApprovalMode 값 그대로(서버가 그대로 투영). */
export type ApprovalMode = "confirm_every_request" | "preauthorized_scope" | "workbench_selective_v1";

export const APPROVAL_LABEL: Record<ApprovalMode, string> = {
  confirm_every_request: "요청마다 확인",
  preauthorized_scope: "사전 승인 범위",
  workbench_selective_v1: "작업대 승인(v1)",
};

/** 자격 헤더 처리 — 엔진 e2proxy.HeaderMode 값 그대로. */
export type HeaderMode =
  | "preserve"
  | "test_account_passthrough"
  | "evaluation_caller_passthrough"
  | "protected_session_proposal";

export const HEADER_MODE_LABEL: Record<HeaderMode, string> = {
  preserve: "모델 자격 헤더 거부",
  test_account_passthrough: "시험 계정 세션 전달",
  evaluation_caller_passthrough: "평가 호출자 자격 전달",
  protected_session_proposal: "보호 세션 제안",
};

/** 어디로·무엇을 — 한 전송 규칙(승인된 origin·연결 IP·경로·메서드). */
export type PolicyRule = {
  ref: string;
  /** "auth_prep" 이면 인증 준비(로그인) 전용 규칙 — 모델 요청은 쓰지 못한다 */
  purpose: "" | "auth_prep";
  origin: string;
  connectIp: string;
  /** exact = query 없는 단일 경로만, prefix = 경로 조각 접두(서버가 칸을 생략) */
  pathMatch: "prefix" | "exact";
  pathPrefix: string;
  pathScopeRef: string;
  methods: string[];
  tlsPinned: boolean;
};

/** 얼마나 — 요청 수 · 시도 수 · 동시성 · 본문 · 시간 상한. */
export type PolicyLimits = {
  maxRequests: number;
  maxAttempts: number;
  maxConcurrent: number;
  maxBodyBytes: number;
  maxResponseBodyBytes: number;
  requestTimeoutMs: number;
};

/** 전송 통제 정책(표시용 투영). auditBound 는 감사 키 결속 '여부'만(키 값 아님). */
export type ControlPolicy = {
  policyRef: string;
  policyRevision: string;
  /** 정책 파일 지문(SHA-256 16진). 서버가 생략하면 null */
  fingerprint: string | null;
  approvalMode: ApprovalMode;
  headerMode: HeaderMode;
  validFrom: string; // RFC3339
  validUntil: string; // RFC3339
  rules: PolicyRule[];
  limits: PolicyLimits;
  /** 이 실행에 결속된 시험 계정 라벨만(자격 · 세션 값 없음) */
  accounts: string[];
  auditBound: boolean;
};

/** 읽기 전용으로 보는 메서드(쓰기 범위 집계에서 제외). */
const READ_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

/**
 * 정책이 지금 유효기간 밖이면 true(만료·미발효·파싱 불가). fail-closed: 불명/파싱 실패를 '유효'로 보지 않는다.
 */
export function policyExpired(p: ControlPolicy, now: number = Date.now()): boolean {
  const from = Date.parse(p.validFrom);
  const until = Date.parse(p.validUntil);
  if (Number.isNaN(from) || Number.isNaN(until)) return true;
  return now < from || now >= until;
}

/** 메서드가 비어 '전부 허용'으로 오인될 규칙 — 빈 methods 를 '모두'로 해석하지 않기 위한 fail-closed 경고용. */
export function rulesMissingMethods(p: ControlPolicy): PolicyRule[] {
  return p.rules.filter((r) => r.methods.length === 0);
}

/** 규칙이 쓰기(GET/HEAD/OPTIONS 외 메서드)를 하나라도 허용하는가. */
export function ruleAllowsWrite(r: PolicyRule): boolean {
  return r.methods.some((m) => !READ_METHODS.has(m.toUpperCase()));
}

export type PolicySummary = {
  rules: number;
  origins: number;
  writeRules: number; // 쓰기 메서드를 포함한 규칙 수(쓰기 가능 범위)
  expired: boolean;
};

export function summarize(p: ControlPolicy, now?: number): PolicySummary {
  const origins = new Set(p.rules.map((r) => r.origin));
  return {
    rules: p.rules.length,
    origins: origins.size,
    writeRules: p.rules.filter(ruleAllowsWrite).length,
    expired: policyExpired(p, now),
  };
}

// ── 닫힌 파서: 응답은 untrusted(절대규칙 4). 모르는 키 · 빠진 칸 · 모르는 enum 은 규칙 하나만 버리지 않고 응답 전체를
//    거부한다 — 일부만 보이면 승인 범위가 실제보다 좁거나 넓게 읽힌다.
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const natural = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0;
const text = (v: unknown): v is string => typeof v === "string";
const filled = (v: unknown): v is string => typeof v === "string" && v.length > 0;
/** Go time.Time 의 JSON(RFC3339, 소수 초 · 시간대 포함 가능). */
const instant = (v: unknown): v is string =>
  typeof v === "string" &&
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(v) &&
  Number.isFinite(Date.parse(v));
const strings = (v: unknown): v is string[] => Array.isArray(v) && v.every(filled);
const invalid = (): never => {
  throw new ApiError(422, "invalid_control_policy_contract");
};
function exact(v: Record<string, unknown>, required: readonly string[], optional: readonly string[] = []) {
  const keys = Object.keys(v);
  if (keys.some((k) => !required.includes(k) && !optional.includes(k)) || required.some((k) => !(k in v))) invalid();
}
const has = <T extends string>(labels: Record<T, string>, v: unknown): v is T =>
  typeof v === "string" && Object.hasOwn(labels, v);

function parseRule(v: unknown): PolicyRule {
  if (!object(v)) return invalid();
  exact(
    v,
    ["ref", "purpose", "origin", "connect_ip", "path_prefix", "path_scope_ref", "methods", "tls_pinned"],
    ["path_match"],
  );
  if (
    !filled(v.ref) ||
    (v.purpose !== "" && v.purpose !== "auth_prep") ||
    !text(v.origin) ||
    !/^https?:\/\/[^/?#\s]+$/.test(v.origin) ||
    !text(v.connect_ip) ||
    !text(v.path_prefix) ||
    !v.path_prefix.startsWith("/") ||
    !text(v.path_scope_ref) ||
    !strings(v.methods) ||
    !v.methods.every((m) => /^[A-Z]+$/.test(m)) ||
    typeof v.tls_pinned !== "boolean" ||
    ("path_match" in v && v.path_match !== "exact")
  )
    return invalid();
  return {
    ref: v.ref,
    purpose: v.purpose,
    origin: v.origin,
    connectIp: v.connect_ip,
    pathMatch: v.path_match === "exact" ? "exact" : "prefix",
    pathPrefix: v.path_prefix,
    pathScopeRef: v.path_scope_ref,
    methods: [...v.methods],
    tlsPinned: v.tls_pinned,
  };
}

function parseLimits(v: unknown): PolicyLimits {
  if (!object(v)) return invalid();
  const keys = [
    "max_requests",
    "max_attempts",
    "max_concurrent",
    "max_body_bytes",
    "max_response_body_bytes",
    "request_timeout_ms",
  ] as const;
  exact(v, keys);
  if (!keys.every((k) => natural(v[k]))) return invalid();
  const n = (k: (typeof keys)[number]) => v[k] as number;
  return {
    maxRequests: n("max_requests"),
    maxAttempts: n("max_attempts"),
    maxConcurrent: n("max_concurrent"),
    maxBodyBytes: n("max_body_bytes"),
    maxResponseBodyBytes: n("max_response_body_bytes"),
    requestTimeoutMs: n("request_timeout_ms"),
  };
}

/** 서버 응답(§3) → 표시용 정책. 모양이 계약과 다르면 ApiError(422)로 거부한다(화면은 표시하지 않음). */
export function parseControlPolicy(v: unknown): ControlPolicy {
  if (!object(v)) return invalid();
  exact(
    v,
    [
      "format",
      "policy_ref",
      "policy_revision",
      "approval_mode",
      "header_mode",
      "valid_from",
      "valid_until",
      "rules",
      "limits",
      "accounts",
      "audit_bound",
    ],
    ["policy_fingerprint_sha256"],
  );
  if (
    v.format !== CONTROL_POLICY_FORMAT ||
    !filled(v.policy_ref) ||
    !text(v.policy_revision) ||
    ("policy_fingerprint_sha256" in v &&
      !(text(v.policy_fingerprint_sha256) && /^[0-9a-f]{64}$/.test(v.policy_fingerprint_sha256))) ||
    !has(APPROVAL_LABEL, v.approval_mode) ||
    !has(HEADER_MODE_LABEL, v.header_mode) ||
    !instant(v.valid_from) ||
    !instant(v.valid_until) ||
    !Array.isArray(v.rules) ||
    !strings(v.accounts) ||
    typeof v.audit_bound !== "boolean"
  )
    return invalid();
  return {
    policyRef: v.policy_ref,
    policyRevision: v.policy_revision,
    fingerprint: text(v.policy_fingerprint_sha256) ? v.policy_fingerprint_sha256 : null,
    approvalMode: v.approval_mode,
    headerMode: v.header_mode,
    validFrom: v.valid_from,
    validUntil: v.valid_until,
    rules: v.rules.map(parseRule),
    limits: parseLimits(v.limits),
    accounts: [...v.accounts],
    auditBound: v.audit_bound,
  };
}

/**
 * 그 task 실행에 결속된 전송 통제 정책. task 는 현재 Job 응답의 task_id(정규 양의 정수 문자열)만 받는다.
 * 404 = 실행이 아직 결속 전, 409 = 정책 파일이 결속 지문과 다름(§3). 오류는 ApiError 그대로 던진다(빈 정책으로 바꾸지 않음).
 */
export async function getControlPolicy(task: string, signal?: AbortSignal): Promise<ControlPolicy> {
  if (!/^[1-9][0-9]*$/.test(task) || !Number.isSafeInteger(Number(task))) return invalid();
  return parseControlPolicy(
    await apiFetch<unknown>(
      `/api/tasks/${encodeURIComponent(task)}/control-policy`,
      { cache: "no-store", redirect: "error", signal },
      false,
    ),
  );
}
