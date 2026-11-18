// 전송 통제 정책(E②) 표시용 seam. 데이터는 getControlPolicy 하나로만 들어온다.
// 실 투영: GET /api/tasks/{id}/control-policy (docs/contracts/workbench-live-data.md §3, 서버 url_live_views.go).
//   task 가 없으면(진단 전) 예시(mock)를 돌려준다 — 화면은 예시 배너로 명시(계약 §7).
// 표현/데이터 분리: 정규화·판정(만료·쓰기 범위·fail-closed)은 전부 여기(lib), 화면은 그리기만.
//
// 안전(절대규칙 2): 서버 투영에 자격 값·감사 키·CA·helper 경로는 애초에 없다(계정은 라벨만, 감사는 결속 여부만).
//   승인된 origin·경로·메서드는 비밀이 아니라 운영자가 봐야 할 '무엇이 승인됐는지'이므로 그대로 보인다.
// fail-closed(절대규칙 4): 응답은 untrusted 다. 형식·모드·규칙 하나라도 모르면 정책 전체를 '확인 불가'로 둔다.
//   규칙을 골라 버리면 승인 범위가 실제보다 좁게 보여 운영자를 속이므로, 일부만 그리지 않는다.

import { ApiError, apiFetch } from "./api";

export const CONTROL_POLICY_VIEW_FORMAT = "abc2lab.control-policy-view/v1";

/** 요청 승인 방식 — 엔진 e2proxy.ApprovalMode 값과 동일(서버가 그대로 투영). */
export type ApprovalMode = "confirm_every_request" | "preauthorized_scope" | "workbench_selective_v1";

export const APPROVAL_LABEL: Record<ApprovalMode, string> = {
  confirm_every_request: "요청마다 확인",
  preauthorized_scope: "사전 승인 범위",
  workbench_selective_v1: "작업대 승인(v1)",
};

/** 자격 헤더 처리 — preserve: 모델이 넣은 자격 헤더 거부, test_account_passthrough: 시험 계정 실행. */
export type HeaderMode = "preserve" | "test_account_passthrough";

export const HEADER_MODE_LABEL: Record<HeaderMode, string> = {
  preserve: "모델 자격 헤더 거부",
  test_account_passthrough: "시험 계정 자격 사용",
};

/** 경로 일치 — prefix: 경로 조각 접두(생략 시), exact: query 없는 단일 경로만(정확 경로 승인 계약). */
export type PathMatch = "prefix" | "exact";

/** 어디로·무엇을 — 한 전송 규칙(승인된 origin·연결 IP·경로·메서드). */
export type PolicyRule = {
  ref: string;
  /** auth_prep 이면 인증 준비(로그인) 전용 — 모델 요청은 이 규칙을 못 쓴다. */
  purpose: string;
  origin: string;
  connectIp: string;
  pathMatch: PathMatch;
  pathPrefix: string;
  pathScopeRef: string | null;
  methods: string[];
  tlsPinned: boolean;
};

/** 얼마나 — 요청·시도·동시성·본문·시간 상한. */
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
  /** 정책 지문(비밀 아님). 자격 pin 규칙이 있으면 서버가 비운다. */
  fingerprint: string;
  approvalMode: ApprovalMode;
  headerMode: HeaderMode;
  validFrom: string; // ISO8601
  validUntil: string; // ISO8601
  rules: PolicyRule[];
  limits: PolicyLimits;
  /** 이 실행에 결속된 시험 계정 라벨만(자격·세션 값 없음). */
  accounts: string[];
  auditBound: boolean;
  mock?: boolean;
};

/** 조회 결과. unbound=결속 전·기록 없음(404), mismatch=승인과 다른 정책(409), invalid=형식 깨짐. */
export type PolicyLoad =
  | { state: "ready"; policy: ControlPolicy }
  | { state: "unbound" }
  | { state: "mismatch" }
  | { state: "invalid" };

/** 읽기 전용으로 보는 메서드(쓰기 범위 집계에서 제외). */
const READ_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

export const AUTH_PREP_PURPOSE = "auth_prep";

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

/** 경로 표시 — 정확 경로는 등록 화면과 같은 `=/…` 표기. */
export function rulePathText(r: PolicyRule): string {
  const path = r.pathPrefix || "/";
  return r.pathMatch === "exact" ? `=${path}` : path;
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

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const isStr = (v: unknown): v is string => typeof v === "string";
const isNat = (v: unknown): v is number => typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
const strList = (v: unknown): string[] | null => (Array.isArray(v) && v.every(isStr) ? v : null);
/** 서버 time.Time JSON(RFC3339, 소수 초·시간대 포함 가능). 모양 밖이면 만료 판정 전에 투영 자체를 거부한다. */
const isInstant = (v: unknown): v is string =>
  isStr(v) &&
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(v) &&
  Number.isFinite(Date.parse(v));
/** origin 은 scheme://host[:port] 만(경로·query·조각·공백 없음). */
const ORIGIN = /^https?:\/\/[^/?#\s]+$/;
/** 메서드는 대문자 토큰만 — 대소문자를 화면에서 고쳐 그리면 서버가 실제로 쥔 값과 달라 보일 수 있다. */
const METHOD = /^[A-Z]+$/;
const FINGERPRINT = /^[0-9a-f]{64}$/;

/** 계약에 없는 키가 하나라도 있거나 필수 키가 빠지면 false(닫힌 파서 — 모르는 칸을 조용히 무시하지 않는다). */
function exactKeys(v: Obj, required: readonly string[], optional: readonly string[] = []): boolean {
  return (
    Object.keys(v).every((k) => required.includes(k) || optional.includes(k)) &&
    required.every((k) => Object.hasOwn(v, k))
  );
}

const RULE_KEYS = ["ref", "purpose", "origin", "connect_ip", "path_prefix", "path_scope_ref", "methods", "tls_pinned"];
const LIMIT_KEYS = [
  "max_requests",
  "max_attempts",
  "max_concurrent",
  "max_body_bytes",
  "max_response_body_bytes",
  "request_timeout_ms",
] as const;
const VIEW_KEYS = [
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
];

function parseApprovalMode(v: unknown): ApprovalMode | null {
  return v === "confirm_every_request" || v === "preauthorized_scope" || v === "workbench_selective_v1" ? v : null;
}

function parseHeaderMode(v: unknown): HeaderMode | null {
  return v === "preserve" || v === "test_account_passthrough" ? v : null;
}

/** 생략은 접두, "exact" 만 정확. null·빈 값·미지원 값은 서버도 거부하는 값이라 깨진 투영으로 본다. */
function parsePathMatch(v: unknown): PathMatch | null {
  if (v === undefined) return "prefix";
  return v === "exact" ? "exact" : null;
}

function parseRule(raw: unknown): PolicyRule | null {
  if (!isObj(raw) || !exactKeys(raw, RULE_KEYS, ["path_match"])) return null;
  const methods = strList(raw.methods);
  const pathMatch = parsePathMatch(raw.path_match);
  if (
    !isStr(raw.ref) ||
    !raw.ref ||
    // 모르는 purpose 를 일반 규칙으로 그리면 용도 제한이 사라진 것처럼 보인다
    (raw.purpose !== "" && raw.purpose !== AUTH_PREP_PURPOSE) ||
    !isStr(raw.origin) ||
    !ORIGIN.test(raw.origin) ||
    !isStr(raw.connect_ip) ||
    !isStr(raw.path_prefix) ||
    !raw.path_prefix.startsWith("/") ||
    !isStr(raw.path_scope_ref) ||
    !methods ||
    !methods.every((m) => METHOD.test(m)) ||
    !pathMatch ||
    typeof raw.tls_pinned !== "boolean"
  )
    return null;
  return {
    ref: raw.ref,
    purpose: raw.purpose,
    origin: raw.origin,
    connectIp: raw.connect_ip,
    pathMatch,
    pathPrefix: raw.path_prefix,
    pathScopeRef: raw.path_scope_ref || null,
    methods: [...methods],
    tlsPinned: raw.tls_pinned,
  };
}

function parseLimits(raw: unknown): PolicyLimits | null {
  if (!isObj(raw) || !exactKeys(raw, LIMIT_KEYS)) return null;
  if (!LIMIT_KEYS.every((k) => isNat(raw[k]))) return null;
  return {
    maxRequests: raw.max_requests as number,
    maxAttempts: raw.max_attempts as number,
    maxConcurrent: raw.max_concurrent as number,
    maxBodyBytes: raw.max_body_bytes as number,
    maxResponseBodyBytes: raw.max_response_body_bytes as number,
    requestTimeoutMs: raw.request_timeout_ms as number,
  };
}

/**
 * 서버 투영(abc2lab.control-policy-view/v1, snake_case)을 fail-closed 로 정규화. 하나라도 깨지면 null.
 * 알 수 없는 승인 방식·헤더 방식을 보수적인 값으로 추정해 그리지 않는다 — 틀린 통제 상태를 보이는 것보다 '확인 불가'가 안전.
 */
export function parseControlPolicyView(raw: unknown): ControlPolicy | null {
  if (!isObj(raw) || raw.format !== CONTROL_POLICY_VIEW_FORMAT) return null;
  if (!exactKeys(raw, VIEW_KEYS, ["policy_fingerprint_sha256"])) return null;
  // 지문은 생략(자격 pin 규칙) 또는 SHA-256 16진만. 빈 문자열·다른 모양은 결속 근거로 보이지 않게 거부한다
  if (
    Object.hasOwn(raw, "policy_fingerprint_sha256") &&
    !(isStr(raw.policy_fingerprint_sha256) && FINGERPRINT.test(raw.policy_fingerprint_sha256))
  )
    return null;
  const approvalMode = parseApprovalMode(raw.approval_mode);
  const headerMode = parseHeaderMode(raw.header_mode);
  const limits = parseLimits(raw.limits);
  const accounts = strList(raw.accounts);
  if (
    !isStr(raw.policy_ref) ||
    !raw.policy_ref ||
    !isStr(raw.policy_revision) ||
    !approvalMode ||
    !headerMode ||
    !isInstant(raw.valid_from) ||
    !isInstant(raw.valid_until) ||
    !Array.isArray(raw.rules) ||
    !limits ||
    !accounts ||
    typeof raw.audit_bound !== "boolean"
  )
    return null;
  const rules: PolicyRule[] = [];
  for (const r of raw.rules) {
    const rule = parseRule(r);
    if (!rule) return null;
    rules.push(rule);
  }
  return {
    policyRef: raw.policy_ref,
    policyRevision: raw.policy_revision,
    fingerprint: isStr(raw.policy_fingerprint_sha256) ? raw.policy_fingerprint_sha256 : "",
    approvalMode,
    headerMode,
    validFrom: raw.valid_from,
    validUntil: raw.valid_until,
    rules,
    limits,
    accounts,
    auditBound: raw.audit_bound,
  };
}

// 예시 정책 — 승인된 origin·경로·메서드만, 비밀 값 없음. 평가/개발 대상(localhost).
const MOCK_POLICY: ControlPolicy = {
  policyRef: "pol-demo-0001",
  policyRevision: "rev-7",
  fingerprint: "",
  approvalMode: "workbench_selective_v1",
  headerMode: "test_account_passthrough",
  validFrom: "2026-11-01T00:00:00Z",
  validUntil: "2026-12-31T00:00:00Z",
  rules: [
    {
      ref: "rule-app",
      purpose: "",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathMatch: "prefix",
      pathPrefix: "/",
      pathScopeRef: "scope-app-v1",
      methods: ["GET", "HEAD"],
      tlsPinned: false,
    },
    {
      ref: "rule-api",
      purpose: "",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathMatch: "prefix",
      pathPrefix: "/api/",
      pathScopeRef: "scope-api-v1",
      methods: ["GET", "POST"],
      tlsPinned: false,
    },
  ],
  limits: {
    maxRequests: 500,
    maxAttempts: 1000,
    maxConcurrent: 2,
    maxBodyBytes: 65536,
    maxResponseBodyBytes: 65536,
    requestTimeoutMs: 5000,
  },
  accounts: ["tester-a"],
  auditBound: true,
  mock: true,
};

/** task id 는 서버 task 경로와 같은 양의 정수 문자열만(다른 값을 경로에 넣지 않는다). */
const TASK_ID = /^[1-9][0-9]*$/;

/**
 * 현재 점검(task)의 전송 통제 정책. task 가 없으면 예시(mock).
 * 404·409 는 화면이 구분해 보일 상태로, 형식 깨짐은 invalid 로 돌려준다. 그 밖의 실패는 throw(화면이 '불러오지 못함').
 */
export async function getControlPolicy(task: string | null, signal?: AbortSignal): Promise<PolicyLoad> {
  if (!task) return { state: "ready", policy: MOCK_POLICY };
  if (!TASK_ID.test(task) || !Number.isSafeInteger(Number(task))) return { state: "invalid" };
  let raw: unknown;
  try {
    raw = await apiFetch<unknown>(
      `/api/tasks/${encodeURIComponent(task)}/control-policy`,
      { cache: "no-store", redirect: "error", signal },
      false,
    );
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return { state: "unbound" };
    if (e instanceof ApiError && e.status === 409) return { state: "mismatch" };
    throw e;
  }
  const policy = parseControlPolicyView(raw);
  return policy ? { state: "ready", policy } : { state: "invalid" };
}
