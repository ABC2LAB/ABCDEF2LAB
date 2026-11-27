// 대상 계정 seam — 실 API 읽기·쓰기 연결. 서버 /api/product/targets[/{id}/accounts]
//   (diagnosisprep ProductTarget / ProductAccount / ProductResponse, 계약 workbench-live-data §1).
//
// ※ 계정 분류 폐지(사용자 결정 2026-11-26, CLAUDE.md 절대규칙 2 · docs/contracts/model-login-attempts.md §0): 프로그램은
//   계정을 시험 · 실계정으로 따로 나누지 않는다. 등록 계정은 시험 계정만 넣도록 공지하고(사용자 책임), 서버는 자격이 저장된
//   등록 계정을 회차에 결속한다(#967). 그래서 화면은 시험 표시 · 해제 · 표시 진술을 다루지 않는다. 서버가 아직 싣는
//   classification · account_statement 칸은 읽지 않는다(표시 · 해제 API를 서버에서 걷는 것은 따로).
// ※ 읽기: getProductTargets(대상 목록)·getAccounts(대상)·getTargetLogin(로그인 방식)=실 GET. 쓰기: registerAccount=실 POST,
//   setTargetLogin(로그인 방식)·setAccountCredential(자격)=실 PUT, clearAccountCredential(그 계정 자격 철회)=실 DELETE.
// ※ 자격(setAccountCredential)은 이 seam 에서 유일하게 값을 보내는 쓰기다. 값은 인자로 받아 요청 본문에만 싣고 저장·반환·
//   로그하지 않는다. 화면은 입력 칸 값을 보낼 때만 읽고 바로 비운다. 등록 여부는 목록의 credential_configured 로만 보인다.
//   표현/데이터 분리 — 파싱은 전부 여기(seam), 화면은 그리기만.

import { apiFetch } from "@/lib/diagnosis/api";
import { validUTCInstant } from "@/lib/diagnosis/diagnosis-run-timing";

/** 대상에 등록된 계정 한 행. 자격 값은 응답에 없다 — 라벨 · 등록 여부만. */
export type TargetAccount = {
  ref: string;
  origin: string;
  /** 서버가 이 계정의 자격 보호 파일을 확인했는가(credential_configured). 값·해시는 오지 않는다. */
  credentialConfigured: boolean;
  /** 등록 시각(서버 registered_at, ms). 서버 목록은 라벨순이라 등록 순서는 이 값으로만 안다. 형식 밖이면 null */
  registeredAt: number | null;
  /**
   * 모델이 배운 로그인 방법(서버 login_method, 계약 model-login-attempts §6-1-3). 칸이 없으면(그 칸을 싣기 전 서버) null —
   * 화면은 '아직 없음'. 닫힌 칸 · 값 목록 밖이면 "invalid"(받지 않음, 화면은 '확인 못 함'). 값(아이디 · 비밀번호 · 세션)은 없다.
   */
  loginMethod: TargetLoginMethod | "invalid" | null;
};

/** 배운 로그인 방법 상태(§6-1-1): 쓸 수 있는 방법 없음 · 배움 · 검증됨 */
export type TargetLoginMethodState = "none" | "learned" | "verified";

/** 값 없는 방법 객체(§6-1-2 칸 그대로) */
export type TargetLoginMethodObject = {
  httpMethod: string;
  path: string;
  format: "form" | "json" | "multipart";
  identifierField: string;
  passwordField: string;
  otherFields: string[];
  fieldsFromResponse: string[];
  sessionSource: "set_cookie" | "json_field" | "header" | "continued";
  sessionNames: string[];
  sessionSentAs: string;
};

export type TargetLoginMethod = {
  state: TargetLoginMethodState;
  /** state none 이고 예전 방법이 무효가 됐을 때만 */
  clearedReason: "target_reapproved" | "credential_changed" | null;
  /** learned · verified 일 때. at 은 ms */
  learned: {
    at: number;
    runRef: string;
    stage: "exploration" | "diagnosis";
    attemptRef: string;
    evidence: "observed_contrast" | "probe_contrast";
  } | null;
  /** verified 일 때만 */
  verified: { at: number; runRef: string; attemptRef: string; evidence: "e3_relogin" } | null;
  /** learned · verified 일 때 */
  method: TargetLoginMethodObject | null;
};

/** 한 대상의 계정 목록. */
export type TargetAccounts = { accounts: TargetAccount[] };

// ---------- login_method 파서(§6-1-3) ----------

const LOGIN_METHOD_STATES: readonly string[] = ["none", "learned", "verified"];
const CLEARED_REASONS: readonly string[] = ["target_reapproved", "credential_changed"];
const LEARNED_STAGES: readonly string[] = ["exploration", "diagnosis"];
const LEARNED_EVIDENCE: readonly string[] = ["observed_contrast", "probe_contrast"];
const METHOD_FORMATS: readonly string[] = ["form", "json", "multipart"];
const SESSION_SOURCES: readonly string[] = ["set_cookie", "json_field", "header", "continued"];

/** 칸이 keys 안에서만 왔는지(닫힌 칸) */
const closedKeys = (v: Record<string, unknown>, keys: readonly string[]): boolean =>
  Object.keys(v).every((k) => keys.includes(k));
const nonEmpty = (v: unknown): v is string => typeof v === "string" && v !== "";
const strList = (v: unknown): string[] | null =>
  Array.isArray(v) && v.every((x) => typeof x === "string") ? (v as string[]) : null;
// 서버 시각은 RFC3339 UTC 다(날짜만 있는 값 등 Date.parse 가 받는 다른 모양은 막는다, #996 경준 리뷰 P3).
const utcMs = (v: unknown): number | null => (validUTCInstant(v) ? Date.parse(v) : null);
// 세션 전달 방식: 쿠키 세션은 "cookie", 헤더 세션은 "헤더이름:접두"(learnedlogin SentAsHeader — 헤더 이름 토큰 · 접두 16자 이하
// 출력 가능 ASCII), 아직 없으면 빈 값(계약 §3 · §6-1-2). 서버가 만들 수 없는 다른 모양은 막는다(#996 경준 리뷰 P3).
const SESSION_SENT_AS = /^(?:cookie|[!#$%&'*+.^_`|~0-9A-Za-z-]+:[\x20-\x7e]{0,16})?$/;

function loginMethodObject(v: unknown): TargetLoginMethodObject | null {
  const keys = [
    "http_method",
    "path",
    "format",
    "identifier_field",
    "password_field",
    "other_fields",
    "fields_from_response",
    "session_source",
    "session_names",
    "session_sent_as",
  ];
  if (!isObj(v) || !closedKeys(v, keys)) return null;
  const otherFields = strList(v.other_fields);
  const fieldsFromResponse = strList(v.fields_from_response);
  const sessionNames = strList(v.session_names);
  if (
    // 서버(learnedlogin Method.Validate)는 POST 방법만 배운다(#996 경준 리뷰 P3)
    v.http_method !== "POST" ||
    !nonEmpty(v.path) ||
    typeof v.format !== "string" ||
    !METHOD_FORMATS.includes(v.format) ||
    // 아이디 칸은 비어도 된다(비밀번호만 보내는 로그인 — 서버 Validate 가 허용, #996 경준 리뷰 P2)
    typeof v.identifier_field !== "string" ||
    !nonEmpty(v.password_field) ||
    !otherFields ||
    !fieldsFromResponse ||
    typeof v.session_source !== "string" ||
    !SESSION_SOURCES.includes(v.session_source) ||
    !sessionNames ||
    typeof v.session_sent_as !== "string" ||
    !SESSION_SENT_AS.test(v.session_sent_as)
  ) {
    return null;
  }
  return {
    httpMethod: v.http_method,
    path: v.path,
    format: v.format as TargetLoginMethodObject["format"],
    identifierField: v.identifier_field,
    passwordField: v.password_field,
    otherFields,
    fieldsFromResponse,
    sessionSource: v.session_source as TargetLoginMethodObject["sessionSource"],
    sessionNames,
    sessionSentAs: v.session_sent_as,
  };
}

/**
 * 계정 행의 login_method(§6-1-3). 칸이 없으면 null(그 칸을 싣기 전 서버). 닫힌 칸 밖 · 모르는 state · evidence · cleared_reason ·
 * 상태와 맞지 않는 칸 묶음이면 "invalid"(계약: 모르는 값은 파서가 거부한다).
 */
export function parseLoginMethod(v: unknown): TargetLoginMethod | "invalid" | null {
  if (v === undefined) return null;
  if (!isObj(v) || !closedKeys(v, ["state", "cleared_reason", "learned", "verified", "method"])) return "invalid";
  if (typeof v.state !== "string" || !LOGIN_METHOD_STATES.includes(v.state)) return "invalid";
  const state = v.state as TargetLoginMethodState;
  let clearedReason: TargetLoginMethod["clearedReason"] = null;
  if (v.cleared_reason !== undefined) {
    if (state !== "none" || typeof v.cleared_reason !== "string" || !CLEARED_REASONS.includes(v.cleared_reason)) {
      return "invalid";
    }
    clearedReason = v.cleared_reason as TargetLoginMethod["clearedReason"];
  }
  if (state === "none") {
    return v.learned === undefined && v.verified === undefined && v.method === undefined
      ? { state, clearedReason, learned: null, verified: null, method: null }
      : "invalid";
  }
  const l = v.learned;
  if (!isObj(l) || !closedKeys(l, ["at", "run_ref", "stage", "attempt_ref", "evidence"])) return "invalid";
  const learnedAt = utcMs(l.at);
  if (
    learnedAt === null ||
    !nonEmpty(l.run_ref) ||
    typeof l.stage !== "string" ||
    !LEARNED_STAGES.includes(l.stage) ||
    !nonEmpty(l.attempt_ref) ||
    typeof l.evidence !== "string" ||
    !LEARNED_EVIDENCE.includes(l.evidence)
  ) {
    return "invalid";
  }
  const method = loginMethodObject(v.method);
  if (!method) return "invalid";
  let verified: TargetLoginMethod["verified"] = null;
  if (state === "verified") {
    const p = v.verified;
    if (!isObj(p) || !closedKeys(p, ["at", "run_ref", "attempt_ref", "evidence"])) return "invalid";
    const at = utcMs(p.at);
    if (at === null || !nonEmpty(p.run_ref) || !nonEmpty(p.attempt_ref) || p.evidence !== "e3_relogin")
      return "invalid";
    verified = { at, runRef: p.run_ref, attemptRef: p.attempt_ref, evidence: "e3_relogin" };
  } else if (v.verified !== undefined) {
    return "invalid";
  }
  return {
    state,
    clearedReason,
    learned: {
      at: learnedAt,
      runRef: l.run_ref,
      stage: l.stage as "exploration" | "diagnosis",
      attemptRef: l.attempt_ref,
      evidence: l.evidence as "observed_contrast" | "probe_contrast",
    },
    verified,
    method,
  };
}

// ---------- 실 API (읽기) ----------

/** 계정 목록을 고를 승인된 대상. id 는 /api/product/targets/{id} 의 것. */
export type ProductTargetRef = { id: string; origin: string; state: string };

// 서버 계정 라벨 형식(diagnosisprep accountLabel). 소문자·숫자·_·- 만 — 사용자명·이메일 원문을 막는다.
const ACCOUNT_LABEL = /^[a-z0-9][a-z0-9_-]{0,31}$/;
/** 서버와 같은 계정 라벨 형식 검사(화면 입력 사전 검증용). */
export const isValidAccountLabel = (label: string): boolean => ACCOUNT_LABEL.test(label);
const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const asStr = (v: unknown): string => (typeof v === "string" ? v : "");

/**
 * 계정을 관리할 대상 목록(선택용). 실 라우트 GET /api/product/targets. 형식이 깨진 행은 버린다(fail-closed).
 * id·origin·state 만 읽는다(자격 없음).
 */
export async function getProductTargets(): Promise<ProductTargetRef[]> {
  const res = await apiFetch<unknown>("/api/product/targets", { cache: "no-store", redirect: "error" });
  const rows = isObj(res) && Array.isArray(res.targets) ? res.targets : [];
  const out: ProductTargetRef[] = [];
  for (const t of rows) {
    if (isObj(t) && typeof t.id === "string" && typeof t.origin === "string") {
      out.push({ id: t.id, origin: t.origin, state: asStr(t.state) });
    }
  }
  return out;
}

/**
 * 한 대상의 계정 목록. 실 라우트 GET /api/product/targets/{id}/accounts(ProductResponse.accounts).
 * 응답에 자격·세션 값은 없다 — 라벨 · 자격 등록 여부만 읽는다. origin 은 대상 것으로 채운다(응답엔 계정별 origin 없음).
 * 대상이 없으면 빈 목록(화면이 '대상 선택' 안내). 형식 밖 행(라벨 형식 위반 등)은 버린다.
 */
export async function getAccounts(target: ProductTargetRef | null): Promise<TargetAccounts> {
  if (!target) return { accounts: [] };
  const res = await apiFetch<unknown>(`/api/product/targets/${encodeURIComponent(target.id)}/accounts`, {
    cache: "no-store",
    redirect: "error",
  });
  const rows = isObj(res) && Array.isArray(res.accounts) ? res.accounts : [];
  const accounts: TargetAccount[] = [];
  for (const a of rows) {
    if (!isObj(a) || typeof a.label !== "string" || !ACCOUNT_LABEL.test(a.label)) continue;
    // 자격 등록 여부는 서버의 credential_configured 참일 때만 '등록됨'이다(형식 밖·없음은 미등록).
    const registeredAt = typeof a.registered_at === "string" ? Date.parse(a.registered_at) : Number.NaN;
    accounts.push({
      ref: a.label,
      origin: target.origin,
      credentialConfigured: a.credential_configured === true,
      registeredAt: Number.isFinite(registeredAt) ? registeredAt : null,
      loginMethod: parseLoginMethod(a.login_method),
    });
  }
  return { accounts };
}

// ---------- 실 API (쓰기) ----------

const accountsBase = (targetId: string) => `/api/product/targets/${encodeURIComponent(targetId)}/accounts`;

/** 계정 등록. 라벨 형식은 서버와 같게 미리 검사한다 — 형식 밖이면 보내지 않는다(fail-closed). */
export async function registerAccount(targetId: string, label: string): Promise<void> {
  if (!ACCOUNT_LABEL.test(label)) throw new Error("invalid_account_label");
  await apiFetch<unknown>(accountsBase(targetId), {
    method: "POST",
    body: JSON.stringify({ label }),
    redirect: "error",
  });
}

// ---------- 대상 로그인 방식·계정 자격 (계약 workbench-live-data §1, 계정 결정안 결정 2) ----------

/**
 * 대상 로그인 방식(diagnosisprep.TargetLogin)의 화면 모양. 자격 값은 없다 — 경로·형식·필드 이름·세션 위치만.
 * 세션 위치는 쿠키 이름들(cookies) 또는 응답 JSON 칸 → 요청 헤더(jsonField·header·prefix) 중 하나다. 메서드는 서버가 POST만 받는다.
 */
export type TargetLogin = {
  path: string;
  format: "form" | "json";
  usernameField: string;
  passwordField: string;
  sessionKind: "cookies" | "header";
  cookies: string[];
  jsonField: string;
  header: string;
  prefix: string;
};

/** 빈 로그인 방식(입력 시작값). 저장할 수 있는 값이 아니다(경로·필드 이름이 비어 있음). */
export const EMPTY_TARGET_LOGIN: TargetLogin = {
  path: "",
  format: "json",
  usernameField: "",
  passwordField: "",
  sessionKind: "header",
  cookies: [],
  jsonField: "",
  header: "Authorization",
  prefix: "Bearer ",
};

// 서버(diagnosisprep.ValidTargetLogin)와 같은 형식 규칙. 경로가 그 대상의 승인 경로 접두 안인지는 서버만 본다.
const LOGIN_FIELD = /^[A-Za-z0-9_.\-[\]]{1,64}$/;
const COOKIE_NAME = /^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$/;
const HEADER_NAME = /^[A-Za-z0-9-]{1,64}$/;
const JSON_SEGMENT = /^[A-Za-z0-9_-]{1,64}$/;
const FORBIDDEN_SESSION_HEADER = new Set([
  "host",
  "cookie",
  "content-length",
  "transfer-encoding",
  "connection",
  "proxy-authorization",
]);
const MAX_LOGIN_PATH_BYTES = 512;
const MAX_LOGIN_COOKIES = 8;
const MAX_JSON_FIELD_SEGMENTS = 8;
const MAX_PREFIX_BYTES = 32;
// 자격 길이(diagnosisprep.ValidAccountCredential, UTF-8 바이트).
const MAX_USERNAME_BYTES = 256;
const MAX_PASSWORD_BYTES = 512;
/**
 * E② 보호 값 하한(e2proxy minProtectedValueBytes). 이보다 짧은 비밀번호는 서버가 저장은 하지만, 실행 때 보호 값으로 등록할 수
 * 없어 그 계정으로 로그인하지 않는다(11/21 VAmPI 기본 계정 5바이트에서 확인). 화면은 안내만 하고 막지 않는다.
 */
export const MIN_USABLE_PASSWORD_BYTES = 6;

/** Go unicode.IsControl(Cc: U+0000–U+001F, U+007F–U+009F). */
// biome-ignore lint/suspicious/noControlCharactersInRegex: 제어 문자를 거르려는 검사
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;

/** 서버로 보내는 모양(snake_case, 고른 세션 위치의 칸만). */
function loginToServer(l: TargetLogin): Record<string, unknown> {
  const base = {
    method: "POST",
    path: l.path,
    format: l.format,
    username_field: l.usernameField,
    password_field: l.passwordField,
  };
  if (l.sessionKind === "cookies") return { ...base, cookies: l.cookies };
  return { ...base, json_field: l.jsonField, header: l.header, ...(l.prefix ? { prefix: l.prefix } : {}) };
}

/** 서버 로그인 방식 형식 검사(diagnosisprep.ValidTargetLogin 과 같은 규칙). */
function validServerLogin(v: Record<string, unknown>): boolean {
  const str = (k: string) => (typeof v[k] === "string" ? (v[k] as string) : "");
  const path = str("path");
  const usernameField = str("username_field");
  const passwordField = str("password_field");
  if (
    v.method !== "POST" ||
    (v.format !== "form" && v.format !== "json") ||
    !LOGIN_FIELD.test(usernameField) ||
    !LOGIN_FIELD.test(passwordField) ||
    usernameField === passwordField ||
    utf8Bytes(path) < 1 ||
    utf8Bytes(path) > MAX_LOGIN_PATH_BYTES ||
    !path.startsWith("/") ||
    path.startsWith("//") ||
    /[?#%;\\ ]/.test(path)
  ) {
    return false;
  }
  const cookies = v.cookies ?? [];
  if (!Array.isArray(cookies)) return false;
  const jsonField = str("json_field");
  const header = str("header");
  const prefix = str("prefix");
  const fieldMode = jsonField !== "" || header !== "" || prefix !== "";
  if (cookies.length > 0 === fieldMode) return false;
  if (cookies.length > 0) {
    if (cookies.length > MAX_LOGIN_COOKIES) return false;
    const seen = new Set<string>();
    for (const c of cookies) {
      if (typeof c !== "string" || !COOKIE_NAME.test(c) || seen.has(c)) return false;
      seen.add(c);
    }
    return true;
  }
  const segments = jsonField.split(".");
  if (segments.length > MAX_JSON_FIELD_SEGMENTS || !HEADER_NAME.test(header)) return false;
  if (prefix !== "" && (utf8Bytes(prefix) > MAX_PREFIX_BYTES || CONTROL.test(prefix))) return false;
  if (FORBIDDEN_SESSION_HEADER.has(header.toLowerCase())) return false;
  return segments.every((s) => JSON_SEGMENT.test(s));
}

/** 화면 입력이 서버 형식 규칙에 맞는가(저장 버튼 사전 검사). 승인 경로 접두 안인지는 서버가 따로 본다. */
export function validTargetLogin(l: TargetLogin): boolean {
  return validServerLogin(loginToServer(l));
}

/** 쉼표·공백으로 나눈 쿠키 이름 목록(빈 조각은 버림). */
export function parseCookieNames(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((c) => c.trim())
    .filter((c) => c !== "");
}

/** 서버 login 칸 → 화면 모양. 없으면 null(미설정). 있는데 형식 밖이면 예외(fail-closed — 미설정으로 보이지 않게). */
function loginFromServer(v: unknown): TargetLogin | null {
  if (v === undefined || v === null) return null;
  if (!isObj(v) || !validServerLogin(v)) throw new Error("invalid_login_response");
  const cookies = Array.isArray(v.cookies) ? (v.cookies as string[]) : [];
  return {
    path: v.path as string,
    format: v.format as "form" | "json",
    usernameField: v.username_field as string,
    passwordField: v.password_field as string,
    sessionKind: cookies.length > 0 ? "cookies" : "header",
    cookies,
    jsonField: asStr(v.json_field),
    header: asStr(v.header),
    prefix: asStr(v.prefix),
  };
}

const targetBase = (targetId: string) => `/api/product/targets/${encodeURIComponent(targetId)}`;

/** 대상 로그인 방식 읽기(GET …/login). 설정 전이면 null. */
export async function getTargetLogin(targetId: string): Promise<TargetLogin | null> {
  const res = await apiFetch<unknown>(`${targetBase(targetId)}/login`, { cache: "no-store", redirect: "error" });
  return loginFromServer(isObj(res) ? res.login : undefined);
}

/** 대상 로그인 방식 저장(PUT …/login, 대상 승인자). 형식 밖이면 보내지 않는다. 서버가 저장한 값을 돌려준다. */
export async function setTargetLogin(targetId: string, login: TargetLogin): Promise<TargetLogin> {
  if (!validTargetLogin(login)) throw new Error("invalid_target_login");
  const res = await apiFetch<unknown>(`${targetBase(targetId)}/login`, {
    method: "PUT",
    body: JSON.stringify(loginToServer(login)),
    redirect: "error",
  });
  const saved = loginFromServer(isObj(res) ? res.login : undefined);
  if (!saved) throw new Error("invalid_login_response");
  return saved;
}

// Go의 strings.TrimSpace가 사용하는 Unicode White_Space 문자 집합이다.
const USERNAME_WHITESPACE = /[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]/;
/** 아이디 칸에서 지울 공백(서버 TrimSpace 와 같은 문자 집합, 전부). */
const USERNAME_WHITESPACE_ALL = new RegExp(USERNAME_WHITESPACE.source, "g");

/**
 * 아이디 칸에 공백을 넣지 못하게 한다(사용자 11/25 — 11/24 B 계정 아이디 끝 공백으로 로그인이 실패했다). 타이핑 · 붙여넣기 ·
 * 끌어 놓기 모두 input 이벤트에서 지우고 커서 자리를 지킨다. 한글 조합 중에는 건드리지 않고 조합이 끝나면 지운다.
 * 비밀번호 · 쿠키 칸은 공백이 정상 값일 수 있어 쓰지 않는다. 칸 값을 상태에 두지 않는 비제어 입력에 그대로 쓴다.
 */
export function stripUsernameWhitespace(el: HTMLInputElement): void {
  const value = el.value;
  if (!USERNAME_WHITESPACE.test(value)) return;
  const caret = el.selectionStart ?? value.length;
  const before = value.slice(0, caret).replace(USERNAME_WHITESPACE_ALL, "");
  el.value = before + value.slice(caret).replace(USERNAME_WHITESPACE_ALL, "");
  el.setSelectionRange?.(before.length, before.length);
}

/** 아이디 칸에 붙이는 이벤트 묶음(공백 막기). */
export const NO_SPACE_USERNAME = {
  onInput: (e: { currentTarget: HTMLInputElement; nativeEvent: Event }) => {
    if ((e.nativeEvent as InputEvent).isComposing) return;
    stripUsernameWhitespace(e.currentTarget);
  },
  onCompositionEnd: (e: { currentTarget: HTMLInputElement }) => stripUsernameWhitespace(e.currentTarget),
} as const;

/** 진단 준비 서비스와 같은 자격 형식 검사를 적용한다. */
export function validAccountCredential(username: string, password: string): boolean {
  const u = utf8Bytes(username);
  const p = utf8Bytes(password);
  return (
    u >= 1 &&
    u <= MAX_USERNAME_BYTES &&
    p >= 1 &&
    p <= MAX_PASSWORD_BYTES &&
    !USERNAME_WHITESPACE.test(username.slice(0, 1) + username.slice(-1)) &&
    !/[\0\r\n]/.test(username + password)
  );
}

/** 비밀번호가 실행 때 E② 보호 값으로 쓰기에 짧은가(안내용, 바이트 수만 본다). */
export function passwordTooShortForRun(password: string): boolean {
  return utf8Bytes(password) < MIN_USABLE_PASSWORD_BYTES;
}

/**
 * 계정 자격 등록(PUT …/accounts/{label}/credential). 값은 요청 본문에만 싣고 어디에도 남기지 않는다. 응답에도 값이 없고,
 * 돌려주는 것은 credential_configured(참이어야 등록됨)뿐이다. 라벨·자격 형식이 밖이면 보내지 않는다.
 */
export async function setAccountCredential(
  targetId: string,
  label: string,
  username: string,
  password: string,
): Promise<boolean> {
  if (!ACCOUNT_LABEL.test(label)) throw new Error("invalid_account_label");
  if (!validAccountCredential(username, password)) throw new Error("invalid_account_credential");
  const res = await apiFetch<unknown>(`${accountsBase(targetId)}/${encodeURIComponent(label)}/credential`, {
    method: "PUT",
    body: JSON.stringify({ username, password }),
    redirect: "error",
  });
  return isObj(res) && isObj(res.account) && res.account.credential_configured === true;
}

/**
 * 그 계정만 저장 자격을 철회한다(DELETE …/accounts/{label}/credential, #982 이동찬 P2). 서버가 철회 줄을 감사 저널에 남기고
 * 자격 파일과 실행 결속 사본을 지운다 — 그 계정을 결속한 실행은 다음 확인에서 멈추고, 다음 회차에는 결속하지 않는다.
 * 값은 보내지도 받지도 않는다. 응답의 credential_configured 가 거짓일 때만 철회됨으로 본다.
 */
export async function clearAccountCredential(targetId: string, label: string): Promise<boolean> {
  if (!ACCOUNT_LABEL.test(label)) throw new Error("invalid_account_label");
  const res = await apiFetch<unknown>(`${accountsBase(targetId)}/${encodeURIComponent(label)}/credential`, {
    method: "DELETE",
    redirect: "error",
  });
  return isObj(res) && isObj(res.account) && res.account.credential_configured !== true;
}

/**
 * 계정·로그인 쓰기 실패 → 한국어 문구. 서버 사유 코드(오류 본문 error)는 값 없는 고정 코드다(product_targets_http.go).
 * 모르는 코드는 일반 문구에 코드만 붙인다(코드 모양이 아니면 붙이지 않는다).
 */
const WRITE_FAILURE_TEXT: Record<string, string> = {
  invalid_target_login: "로그인 방식이 서버 형식에 맞지 않거나 로그인 경로가 이 대상의 승인 경로 밖입니다.",
  invalid_account_credential:
    "자격 형식이 맞지 않습니다. 사용자명은 앞뒤 공백 없이 1~256바이트, 비밀번호는 1~512바이트로 입력하세요. 줄바꿈은 사용할 수 없습니다.",
  invalid_account_label: "계정 라벨 형식이 맞지 않습니다(소문자 · 숫자 · _ · -).",
  invalid_account_request: "서버가 계정 요청 형식을 받지 않았습니다.",
  invalid_product_request: "서버가 요청 형식을 받지 않았습니다.",
  target_not_found: "이 대상을 찾지 못했습니다(삭제됐거나 다른 운영자의 대상). 대상 목록을 다시 읽으세요.",
  account_not_found: "이 계정을 찾지 못했습니다. 계정 목록을 다시 읽으세요.",
  target_not_approved: "철회된 대상이라 바꿀 수 없습니다. 대상 승인부터 확인하세요.",
  operator_required: "운영자 권한이 필요합니다.",
  diagnosis_preparation_unavailable: "진단 준비 저장소를 지금 쓸 수 없습니다. 잠시 뒤 다시 시도하세요.",
  target_service_unavailable: "대상 관리 서비스가 응답하지 않았습니다. 잠시 뒤 다시 시도하세요.",
  product_control_unavailable: "제품 제어 서비스에 닿지 못했습니다. 잠시 뒤 다시 시도하세요.",
  authentication_changed: "로그인 세션이 바뀌었습니다. 다시 로그인한 뒤 시도하세요.",
  invalid_login_response: "서버 응답 형식이 맞지 않아 결과를 확인하지 못했습니다. 목록을 다시 읽으세요.",
  credential_not_cleared: "서버가 자격 철회를 확인하지 못했습니다. 목록을 다시 읽어 '자격 없음'인지 확인하세요.",
  credential_not_confirmed:
    "서버가 자격 등록을 확인하지 못했습니다. 목록의 '자격 등록됨' 표시를 확인하고 다시 입력하세요.",
};
export const WRITE_FAILURE_DEFAULT = "작업을 처리하지 못했습니다. 권한 · 대상 상태를 확인하고 다시 시도하세요.";
const REASON_CODE = /^[a-z][a-z0-9_]{0,63}$/;

export function accountWriteFailureText(e: unknown): string {
  const code = e instanceof Error ? e.message : "";
  if (Object.hasOwn(WRITE_FAILURE_TEXT, code)) return WRITE_FAILURE_TEXT[code];
  return REASON_CODE.test(code) ? `${WRITE_FAILURE_DEFAULT} (사유: ${code})` : WRITE_FAILURE_DEFAULT;
}

const encoder = new TextEncoder();
function utf8Bytes(s: string): number {
  return encoder.encode(s).length;
}
