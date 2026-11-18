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
  /**
   * E③ 확정 단계 몫(모델 단계와 따로 떼어 둔 요청 수·시간). 제품 실행 정책은 늘 둘 다 싣고(꺼짐이면 0),
   * 칸이 없는 이전 회차는 undefined — 0(꺼짐)과 '정하지 않음'을 섞지 않는다.
   */
  e3Confirmation?: { requests: number; timeoutMs: number };
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
  const from = parseInstant(p.validFrom);
  const until = parseInstant(p.validUntil);
  if (!from || !until) return true;
  const at = msInstant(now);
  return compareInstant(at, from) < 0 || compareInstant(at, until) >= 0;
}

/** 경계 시각 이후 첫 밀리초(올림) — 화면 타이머가 소수 밀리초 경계보다 먼저 깨어 상태를 못 바꾸는 일을 막는다. 못 읽으면 NaN. */
export function instantCeilMs(v: string): number {
  const t = parseInstant(v);
  return t ? t.sec * 1000 + Math.ceil(t.nsec / 1e6) : Number.NaN;
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
/** UTC 기준 초와 그 안의 나노초. 서버 time.Time 과 같은 정밀도로 비교하려고 밀리초로 줄이지 않는다. */
type Instant = { sec: number; nsec: number };

const isLeapYear = (y: number): boolean => y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
const MONTH_DAYS = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
const SECONDS_PER_DAY = 86400;

/** 서기 y-m-d 의 1970-01-01 기준 일수(그레고리력 역산). Date.UTC 는 0~99년을 1900년대로 바꿔서 쓰지 않는다. */
function daysFromCivil(year: number, month: number, day: number): number {
  const y = month <= 2 ? year - 1 : year;
  const era = Math.floor(y / 400);
  const yoe = y - era * 400;
  const doy = Math.floor((153 * (month + (month > 2 ? -3 : 9)) + 2) / 5) + day - 1;
  const doe = yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy;
  return era * 146097 + doe - 719468;
}

/**
 * 서버 time.Time JSON(RFC3339, 소수 초·시간대 포함 가능)을 Go parseRFC3339 와 같은 범위로 읽는다:
 * 연 0~9999, 월 1~12, 일은 그 달의 날수까지(2월 30일 같은 날짜를 Date.parse 처럼 다음 달로 넘기지 않음),
 * 시 0~23·분 0~59·초 0~59, 시간대 시 0~23·분 0~59, 소수 초는 9자리(나노초)까지 그대로 보존. 모양 밖이면 null.
 */
function parseInstant(v: unknown): Instant | null {
  if (!isStr(v)) return null;
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(?:Z|([+-])(\d{2}):(\d{2}))$/.exec(v);
  if (!m) return null;
  const [year, month, day, hour, min, sec] = m.slice(1, 7).map(Number);
  const offH = m[9] === undefined ? 0 : Number(m[9]);
  const offM = m[10] === undefined ? 0 : Number(m[10]);
  const monthDays = month === 2 && isLeapYear(year) ? 29 : MONTH_DAYS[month - 1];
  if (
    month < 1 ||
    month > 12 ||
    day < 1 ||
    day > monthDays ||
    hour > 23 ||
    min > 59 ||
    sec > 59 ||
    offH > 23 ||
    offM > 59
  )
    return null;
  const offset = (m[8] === "-" ? -1 : 1) * (offH * 3600 + offM * 60);
  return {
    sec: daysFromCivil(year, month, day) * SECONDS_PER_DAY + hour * 3600 + min * 60 + sec - offset,
    nsec: Number((m[7] ?? "").padEnd(9, "0")),
  };
}

function compareInstant(a: Instant, b: Instant): number {
  return a.sec !== b.sec ? a.sec - b.sec : a.nsec - b.nsec;
}

/** 브라우저 시각(ms) → Instant. */
function msInstant(ms: number): Instant {
  const sec = Math.floor(ms / 1000);
  return { sec, nsec: (ms - sec * 1000) * 1e6 };
}

const isInstant = (v: unknown): v is string => parseInstant(v) !== null;
/**
 * origin — e2proxy canonicalAuthority 가 받는 모양만: http·https, 사용자 정보(`user@`) 없음, 경로는 비거나 `/` 하나,
 * query·조각 없음, 호스트는 (소문자로 바꿨을 때) 영숫자·`.`·`-` 또는 대괄호 IPv6, 포트는 1~65535.
 */
export function validOrigin(v: string): boolean {
  const m = /^(https?):\/\/([^/?#@\s\\]+)\/?$/i.exec(v);
  if (!m) return false;
  const auth = m[2];
  const v6 = /^\[([0-9A-Fa-f:.]+)\](?::(\d+))?$/.exec(auth);
  const plain = v6 ? null : /^([^:]+)(?::(\d+))?$/.exec(auth);
  const host = v6 ? v6[1] : plain?.[1];
  const port = v6 ? v6[2] : plain?.[2];
  if (!host || (!v6 && !/^[a-z0-9.-]+$/.test(host.toLowerCase()))) return false;
  if (port === undefined) return true;
  const n = Number(port);
  return /^[0-9]{1,5}$/.test(port) && n >= 1 && n <= 65535;
}

/** 정책·규칙 참조 — e2proxy validRef(영숫자로 시작, 영숫자·`_.:/-`, 128자 이하, `://` 없음). 빈 값은 서버가 만들 수 없다. */
const SAFE_REF = /^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$/;
const validRef = (v: unknown): v is string => isStr(v) && SAFE_REF.test(v) && !v.includes("://");
/**
 * 정책 식별자 — server e2HTTPHostRef(영숫자로 시작, 영숫자·`_.:-`, 128자 이하). 규칙 참조와 달리 `/` 를 받지 않는다.
 * 서버 정책 문서 검증(validateE2HTTPPolicyDocumentMode)이 policy_ref·policy_revision 에 요구한다. 빈 값도 거부.
 */
const POLICY_ID = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/;
const validPolicyId = (v: unknown): v is string => isStr(v) && POLICY_ID.test(v);
/** e2proxy maxExecutionRules — 규칙은 1개 이상 이만큼 이하 */
const MAX_RULES = 128;
/**
 * E② 가 받는 메서드만(e2proxy/execution.go supportedMethod). 서버는 이 밖의 메서드를 가진 정책을 실행에 올리지 않으므로,
 * 응답에 있으면 화면이 서버와 다른 정책을 보고 있는 것이다.
 */
const E2_METHODS = new Set(["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"]);

// ── 서버가 실행에 올릴 수 없는 규칙(e2proxy/execution.go compileRules 가 ErrConfig 로 거부하는 조합)은 '확인 불가'로 둔다.
//    방어적 파서가 서버보다 넓게 받으면, 실제로는 존재할 수 없는 승인 범위를 운영자에게 보인다(#689 리뷰).

/** IPv4 점 네 칸(앞자리 0 없음, Go netip.ParseAddr 와 같은 모양). 맞으면 네 숫자, 아니면 null. */
function ipv4Octets(v: string): number[] | null {
  const parts = v.split(".");
  if (parts.length !== 4 || !parts.every((x) => /^(0|[1-9][0-9]{0,2})$/.test(x))) return null;
  const n = parts.map(Number);
  return n.every((x) => x <= 255) ? n : null;
}

/**
 * 연결 IP — 유효한 IPv4·IPv6 이고 미지정(0.0.0.0 · ::)·멀티캐스트·zone(%…)이 아님. 빈 값은 서버가 만들 수 없다.
 * IPv6 는 URL 파서로 정규형을 얻어 판정한다(직접 손으로 문법을 다시 짜지 않는다).
 */
export function validConnectIp(v: string): boolean {
  const v4 = ipv4Octets(v);
  if (v4) return !v4.every((x) => x === 0) && !(v4[0] >= 224 && v4[0] <= 239);
  if (!v.includes(":") || v.includes("%") || v.includes("[") || v.includes("]")) return false;
  let host: string;
  try {
    host = new URL(`http://[${v}]/`).hostname.slice(1, -1).toLowerCase();
  } catch {
    return false;
  }
  if (host === "::") return false;
  if (host.startsWith("ff")) return false;
  // IPv4 대응 IPv6(::ffff:a.b.c.d)는 서버가 IPv4 로 풀어 쓴다 — 풀린 주소로 다시 본다
  const mapped = /^::ffff:([0-9a-f]{1,4}):([0-9a-f]{1,4})$/.exec(host);
  if (mapped) {
    const hi = Number.parseInt(mapped[1], 16);
    const lo = Number.parseInt(mapped[2], 16);
    return validConnectIp(`${hi >> 8}.${hi & 255}.${lo >> 8}.${lo & 255}`);
  }
  return true;
}

/** 승인 경로 — e2proxy validPath: `/` 로 시작, `\`·`%`·줄바꿈·`//`·`.`/`..` 조각 없음(정리하면 모양이 바뀌는 경로 거부). */
function validRulePath(p: string): boolean {
  if (p === "/") return true;
  if (!p.startsWith("/") || /[\\%\r\n]/.test(p) || p.includes("//")) return false;
  const segs = p.slice(1).replace(/\/$/, "").split("/");
  return segs.every((s) => s !== "" && s !== "." && s !== "..");
}
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
    !validRef(raw.ref) ||
    // 모르는 purpose 를 일반 규칙으로 그리면 용도 제한이 사라진 것처럼 보인다
    (raw.purpose !== "" && raw.purpose !== AUTH_PREP_PURPOSE) ||
    !isStr(raw.origin) ||
    !validOrigin(raw.origin) ||
    !isStr(raw.connect_ip) ||
    !isStr(raw.path_prefix) ||
    !validRulePath(raw.path_prefix) ||
    !validConnectIp(raw.connect_ip) ||
    !validRef(raw.path_scope_ref) ||
    !methods ||
    methods.length === 0 ||
    !methods.every((m) => E2_METHODS.has(m)) ||
    new Set(methods).size !== methods.length ||
    !pathMatch ||
    typeof raw.tls_pinned !== "boolean" ||
    // TLS 핀은 https 대상에만 결속된다
    (raw.tls_pinned && !/^https:/i.test(raw.origin))
  )
    return null;
  // 인증 준비 규칙: 정확 경로 칸 없이 로그인 경로 하나에 POST 만(서버는 그 밖의 모양을 실행에 올리지 않는다)
  if (
    raw.purpose === AUTH_PREP_PURPOSE &&
    (raw.path_match !== undefined ||
      methods.length !== 1 ||
      methods[0] !== "POST" ||
      raw.path_prefix === "/" ||
      raw.path_prefix.endsWith("/"))
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

const E3_SHARE_KEYS = ["e3_confirmation_requests", "e3_confirmation_timeout_ms"] as const;

function parseLimits(raw: unknown): PolicyLimits | null {
  if (!isObj(raw) || !exactKeys(raw, LIMIT_KEYS, E3_SHARE_KEYS)) return null;
  if (!LIMIT_KEYS.every((k) => isNat(raw[k]))) return null;
  // 요청 상한 0 은 서버 정책 문서 검증이 거부한다(MaxRequests == 0) — '요청 0건 허용' 정책으로 그리지 않는다
  if (raw.max_requests === 0) return null;
  // 확정 몫은 둘 다 있거나 둘 다 없다. 둘 다 0(꺼짐)이거나 둘 다 양수이고, 요청 몫은 실행 요청 상한의 1/4 이하(서버 검사와 같음).
  const shareKeys = E3_SHARE_KEYS.filter((k) => Object.hasOwn(raw, k));
  let e3Confirmation: PolicyLimits["e3Confirmation"];
  if (shareKeys.length === 1) return null;
  if (shareKeys.length === 2) {
    const requests = raw.e3_confirmation_requests;
    const timeoutMs = raw.e3_confirmation_timeout_ms;
    if (!isNat(requests) || !isNat(timeoutMs) || (requests === 0) !== (timeoutMs === 0)) return null;
    if ((requests as number) * 4 > (raw.max_requests as number)) return null;
    e3Confirmation = { requests: requests as number, timeoutMs: timeoutMs as number };
  }
  return {
    ...(e3Confirmation ? { e3Confirmation } : {}),
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
    !validPolicyId(raw.policy_ref) ||
    !validPolicyId(raw.policy_revision) ||
    !approvalMode ||
    !headerMode ||
    !isInstant(raw.valid_from) ||
    !isInstant(raw.valid_until) ||
    // 유효 창은 양의 간격(서버: ValidUntil.After(ValidFrom)) — 나노초까지 비교
    !(compareInstant(parseInstant(raw.valid_until) as Instant, parseInstant(raw.valid_from) as Instant) > 0) ||
    !Array.isArray(raw.rules) ||
    raw.rules.length === 0 ||
    raw.rules.length > MAX_RULES ||
    !limits ||
    !accounts ||
    typeof raw.audit_bound !== "boolean"
  )
    return null;
  const rules: PolicyRule[] = [];
  for (const r of raw.rules) {
    const rule = parseRule(r);
    // 규칙 참조는 정책 안에서 하나씩만(서버 compileRules 의 seen) — 겹치면 어느 규칙이 쓰였는지 화면이 가릴 수 없다
    if (!rule || rules.some((x) => x.ref === rule.ref)) return null;
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
