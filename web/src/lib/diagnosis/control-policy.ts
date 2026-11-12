// 전송 통제 정책(E②) 표시용 seam — 지금은 예시(mock). 데이터는 getControlPolicy 하나로만 들어오고,
// 실 투영 API(운영 target_policy = abc2lab.e2-http-host-policy/v1 의 표시용 투영) 연결 시 이 getter 만 교체한다.
// 표현/데이터 분리: 정규화·판정(만료·쓰기 범위·fail-closed)은 전부 여기(lib), 화면은 그리기만.
//
// 안전(절대규칙 2): 자격 헤더는 '이름'만 담는다(값 금지). 감사 키·TLS 핀 개인키·예산/승인 토큰 등 제어 비밀
//   원문은 애초에 투영에 없다(결속 여부/참조만). 승인된 origin·경로·메서드는 비밀이 아니라 운영자가 봐야 할
//   '무엇이 승인됐는지'이므로 그대로 보인다. 이 seam 은 비밀 저장소가 아니라 통제 상태 투영이다.

/** 요청 승인 방식 — 엔진 ApprovalMode(요청마다 확인 / 사전 승인 범위 / 작업대 승인)와 1:1. */
export type ApprovalMode = "confirm_every" | "preauthorized_scope" | "workbench_v1";

export const APPROVAL_LABEL: Record<ApprovalMode, string> = {
  confirm_every: "요청마다 확인",
  preauthorized_scope: "사전 승인 범위",
  workbench_v1: "작업대 승인(v1)",
};

/** 어디로·무엇을 — 한 전송 규칙(승인된 origin·연결 IP·경로·메서드). credentialHeaderNames 는 '이름'만. */
export type PolicyRule = {
  ref: string;
  origin: string;
  connectIp: string;
  pathPrefix: string;
  pathScopeRef: string | null;
  methods: string[];
  credentialHeaderNames: string[]; // 자격 헤더 '이름'만 — 값은 투영에 들어오지 않는다
  tlsPinned: boolean;
};

/** 얼마나 — 예산·동시성 상한. budgetRef 는 참조(실 예산치는 별도). */
export type PolicyLimits = {
  maxRequests: number;
  maxConcurrent: number;
  outputLimit: number;
  budgetRef: string;
};

/** 전송 통제 정책(표시용 투영). auditBound 는 감사 키 결속 '여부'만(키 값 아님). */
export type ControlPolicy = {
  policyRef: string;
  policyRevision: string;
  approvalMode: ApprovalMode;
  validFrom: string; // ISO8601
  validUntil: string; // ISO8601
  rules: PolicyRule[];
  limits: PolicyLimits;
  auditBound: boolean;
  mock?: boolean;
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

const asStrings = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];

/** 한 규칙을 표시 안전하게 정규화(fail-closed): 입력은 untrusted 투영이라 unknown. 모양이 깨지면 버린다. */
function normalizeRule(r: unknown): PolicyRule | null {
  if (typeof r !== "object" || r === null) return null;
  const o = r as Record<string, unknown>;
  if (typeof o.ref !== "string" || typeof o.origin !== "string") return null;
  return {
    ref: o.ref,
    origin: o.origin,
    connectIp: typeof o.connectIp === "string" ? o.connectIp : "",
    pathPrefix: typeof o.pathPrefix === "string" ? o.pathPrefix : "",
    pathScopeRef: typeof o.pathScopeRef === "string" ? o.pathScopeRef : null,
    methods: asStrings(o.methods).map((m) => m.toUpperCase()),
    credentialHeaderNames: asStrings(o.credentialHeaderNames),
    tlsPinned: o.tlsPinned === true,
  };
}

/**
 * 표시용 투영을 fail-closed 로 정규화. 형식이 깨지면 null(화면은 '정책을 확인할 수 없음'으로 떨어진다).
 * 알 수 없는 승인 방식은 가장 보수적인 '요청마다 확인'으로 내린다 — 느슨한 쪽으로 추정하지 않는다.
 */
export function normalizePolicy(raw: unknown): ControlPolicy | null {
  if (typeof raw !== "object" || raw === null) return null;
  const o = raw as Record<string, unknown>;
  if (typeof o.policyRef !== "string") return null;
  const rules = Array.isArray(o.rules) ? o.rules.map(normalizeRule).filter((r): r is PolicyRule => r !== null) : [];
  const mode: ApprovalMode =
    o.approvalMode === "preauthorized_scope" || o.approvalMode === "workbench_v1" ? o.approvalMode : "confirm_every";
  const l = (typeof o.limits === "object" && o.limits !== null ? o.limits : {}) as Record<string, unknown>;
  const n = (v: unknown): number => (typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : 0);
  return {
    policyRef: o.policyRef,
    policyRevision: typeof o.policyRevision === "string" ? o.policyRevision : "",
    approvalMode: mode,
    validFrom: typeof o.validFrom === "string" ? o.validFrom : "",
    validUntil: typeof o.validUntil === "string" ? o.validUntil : "",
    rules,
    limits: {
      maxRequests: n(l.maxRequests),
      maxConcurrent: n(l.maxConcurrent),
      outputLimit: n(l.outputLimit),
      budgetRef: typeof l.budgetRef === "string" ? l.budgetRef : "",
    },
    auditBound: o.auditBound === true,
    mock: o.mock === true,
  };
}

// 예시 정책 — 승인된 origin·경로·메서드만, 비밀 값 없음(자격 헤더는 이름만). 평가/개발 대상(localhost).
const MOCK_POLICY: ControlPolicy = {
  policyRef: "pol-demo-0001",
  policyRevision: "rev-7",
  approvalMode: "workbench_v1",
  validFrom: "2026-11-01T00:00:00Z",
  validUntil: "2026-12-31T00:00:00Z",
  rules: [
    {
      ref: "rule-app",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathPrefix: "/",
      pathScopeRef: "scope-app-v1",
      methods: ["GET", "HEAD"],
      credentialHeaderNames: ["Authorization", "Cookie"],
      tlsPinned: false,
    },
    {
      ref: "rule-api",
      origin: "http://localhost:3000",
      connectIp: "127.0.0.1",
      pathPrefix: "/api/",
      pathScopeRef: "scope-api-v1",
      methods: ["GET", "POST"],
      credentialHeaderNames: ["Authorization"],
      tlsPinned: false,
    },
  ],
  limits: { maxRequests: 500, maxConcurrent: 2, outputLimit: 65536, budgetRef: "budget-demo-01" },
  auditBound: true,
  mock: true,
};

/**
 * 전송 통제 정책(표시용). 지금은 예시(mock). 실 투영 API 연결 시 이 함수만 교체한다(normalizePolicy 로 감싸서).
 * _taskRef 는 지금 안 쓰지만, 실 연결 때 대상/회차 정책을 고르는 키가 된다.
 */
export async function getControlPolicy(_taskRef?: string | null): Promise<ControlPolicy> {
  return MOCK_POLICY;
}
