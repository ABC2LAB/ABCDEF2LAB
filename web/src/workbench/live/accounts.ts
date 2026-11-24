// 계정 분류(시험 계정 표시) seam — 실 API 읽기·쓰기 연결.
// 계약: docs/contracts/exploration-boundary.md §8(계정과 인증)·§9-7. 서버 /api/product/targets[/{id}/accounts]
//   (diagnosisprep ProductTarget / ProductAccount / ProductResponse).
//
// ※ 절대규칙 2 · 수용시험 S09: 계정 원문·비밀번호·토큰·세션 값은 다루지 않는다. label(별칭)·대상 origin·분류 상태·
//   등록/표시자·사유만. 자격 값은 응답에 애초에 없다(credential_configured 는 '있음' 여부뿐). 모델 입력·로그·근거
//   파일·export 에 넣지 않는다.
// ※ §8 fail-closed: 서버 분류가 'test'(승인된 시험 표시)가 아니면 전부 실계정으로 본다. real·미표시·판단 불가·형식 밖 모두.
// ※ 효력 시점(CLAUDE.md 절대규칙 2): 이 분류가 지금 탐색·모델 입력 권한을 바로 주지 않는다 — 허용은 승인된 일회용
//   시험 계정에 대상이 발급한 세션만이고 비밀번호는 계속 제외된다.
// ※ 읽기: getProductTargets(대상 목록)·getAccounts(대상)·getTargetLogin(로그인 방식)=실 GET. 쓰기: registerAccount·
//   markTestAccount·unmarkAccount=실 POST, setTargetLogin(로그인 방식)·setAccountCredential(자격)=실 PUT.
// ※ 자격(setAccountCredential)은 이 seam 에서 유일하게 값을 보내는 쓰기다. 값은 인자로 받아 요청 본문에만 싣고 저장·반환·
//   로그하지 않는다. 화면은 입력 칸 값을 보낼 때만 읽고 바로 비운다. 등록 여부는 목록의 credential_configured 로만 보인다.
// ※ 시험 표시 동의는 서버 진술 문장(account_statement)으로만 받는다. 서버는 그 문장의 SHA-256을 동의 기록으로 남기므로,
//   문장이 없거나 형식 밖이면 화면이 대체 문구로 동의를 받지 않는다(fail-closed, 계약 workbench-live-data §1).
//   표현/데이터 분리 — 파싱·분류 매핑은 전부 여기(seam), 화면은 그리기만.

import { apiFetch } from "@/lib/diagnosis/api";

/** §8 분류 상태. 표시는 프로그램이 보호 저장소로 확인한다(모델·도구가 쓴 문자열이 아니다). */
export type AccountClass = "test_approved" | "test_pending" | "unmarked";

export const ACCOUNT_CLASS_LABEL: Record<AccountClass, string> = {
  test_approved: "시험 계정",
  test_pending: "표시 등록 · 승인 대기",
  unmarked: "미표시 (실계정 취급)",
};

/** 배지 색 키 — 승인된 시험(ok) / 대기(wait) / 실계정 취급(stop). */
export const ACCOUNT_CLASS_TONE: Record<AccountClass, "ok" | "wait" | "stop"> = {
  test_approved: "ok",
  test_pending: "wait",
  unmarked: "stop",
};

const KNOWN_CLASS = new Set<string>(Object.keys(ACCOUNT_CLASS_LABEL));

/** 분류 상태 문자열 → 아는 값이면 그대로, 밖이면 "unmarked"(fail-closed: 판단 불가 = 실계정 취급, §8). */
export function normalizeClass(cls: string): AccountClass {
  return KNOWN_CLASS.has(cls) ? (cls as AccountClass) : "unmarked";
}

export function classLabel(cls: string): string {
  return ACCOUNT_CLASS_LABEL[normalizeClass(cls)];
}

/**
 * 분류 판정만 한다: 승인된 '시험 계정' 표시(test_approved)인가. 승인 대기·미표시·판단 불가(미등록 값)는 false(실계정).
 * 탐색·모델 입력 권한 판정이 아니다 — true 여도 권한은 승인된 일회용 시험 계정의 대상 발급 세션에 한한다(위 효력 시점).
 */
export function usableAsTestAccount(cls: string): boolean {
  return normalizeClass(cls) === "test_approved";
}

/**
 * 분류된 계정 한 행. 자격(비밀번호·토큰·세션)은 없다(절대규칙 2 · S09) — ref·origin·역할·상태만.
 * approvedBy 는 표시(승인) 주체 ref. 사람 값이 아니라 식별 별칭이다.
 */
export type ClassifiedAccount = {
  ref: string;
  origin: string;
  role?: string;
  cls: AccountClass;
  approvedBy?: string;
  approvedAt?: string;
  note?: string;
  /** 서버가 이 계정의 자격 보호 파일을 확인했는가(credential_configured). 값·해시는 오지 않는다. */
  credentialConfigured?: boolean;
};

/** 한 묶음의 분류 결과. mock=true 면 예시(실 저장소 아님) — 화면이 배너로 명시. 실 연결에서는 false. */
export type AccountClassification = {
  accounts: ClassifiedAccount[];
  /** 시험 계정 표시 진술 문장(ProductResponse.account_statement). mark-test 동의에 쓴다. 없거나 형식 밖이면 ""(표시 불가). */
  statement: string;
  mock: boolean;
};

export type AccountSummary = {
  total: number;
  /** 시험 계정(승인됨) 분류 수. 권한이 아니라 분류 판정이다. */
  testApproved: number;
  /** 표시 등록·승인 대기 — 아직 실계정 취급. */
  testPending: number;
  /** 미표시 — 실계정 취급. */
  unmarked: number;
  /** 승인된 시험 계정 분류 수(= testApproved, 분류 판정). 지금은 탐색·모델 입력 권한을 뜻하지 않는다. */
  usable: number;
};

export function summarizeAccounts(accounts: readonly ClassifiedAccount[]): AccountSummary {
  const testApproved = accounts.filter((a) => normalizeClass(a.cls) === "test_approved").length;
  const testPending = accounts.filter((a) => normalizeClass(a.cls) === "test_pending").length;
  return {
    total: accounts.length,
    testApproved,
    testPending,
    unmarked: accounts.length - testApproved - testPending,
    usable: accounts.filter((a) => usableAsTestAccount(a.cls)).length,
  };
}

// ---------- 실 API (읽기) ----------

/** 분류 대상(계정 목록을 고를 승인된 대상). id 는 /api/product/targets/{id} 의 것. */
export type ProductTargetRef = { id: string; origin: string; state: string };

// 서버 계정 라벨 형식(diagnosisprep accountLabel). 소문자·숫자·_·- 만 — 사용자명·이메일 원문을 막는다.
const ACCOUNT_LABEL = /^[a-z0-9][a-z0-9_-]{0,31}$/;
/** 서버와 같은 계정 라벨 형식 검사(화면 입력 사전 검증용). */
export const isValidAccountLabel = (label: string): boolean => ACCOUNT_LABEL.test(label);
const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const asStr = (v: unknown): string => (typeof v === "string" ? v : "");

/** 서버 진술 문장 검사 — 문자열, 앞뒤 공백 아닌 1…1024자, 제어 문자 없음. 밖이면 ""(진술 없음 = 시험 표시 불가). */
export function validStatement(v: unknown): string {
  if (typeof v !== "string" || v.length < 1 || v.length > 1024 || v.trim() !== v) return "";
  // biome-ignore lint/suspicious/noControlCharactersInRegex: 제어 문자를 거르려는 검사
  return /[\u0000-\u001f\u007f]/.test(v) ? "" : v;
}

/** 서버 분류(test|real) → 화면 3-state. 'test'만 승인된 시험(test_approved), 그 밖은 실계정(unmarked, §8 fail-closed). */
export function classFromServer(classification: string): AccountClass {
  return classification === "test" ? "test_approved" : "unmarked";
}

/**
 * 계정 분류 대상 목록(선택용). 실 라우트 GET /api/product/targets. 형식이 깨진 행은 버린다(fail-closed).
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
 * 한 대상의 계정 분류. 실 라우트 GET /api/product/targets/{id}/accounts(ProductResponse.accounts).
 * 응답에 자격·세션 값은 없다 — label·분류·등록/표시자·사유만. origin 은 대상 것으로 채운다(응답엔 계정별 origin 없음).
 * 대상이 없으면 빈 목록(화면이 '대상 선택' 안내). 형식 밖 행(라벨 형식 위반 등)은 버린다(fail-closed §8).
 */
export async function getAccounts(target: ProductTargetRef | null): Promise<AccountClassification> {
  if (!target) return { accounts: [], statement: "", mock: false };
  const res = await apiFetch<unknown>(`/api/product/targets/${encodeURIComponent(target.id)}/accounts`, {
    cache: "no-store",
    redirect: "error",
  });
  const rows = isObj(res) && Array.isArray(res.accounts) ? res.accounts : [];
  const accounts: ClassifiedAccount[] = [];
  for (const a of rows) {
    if (!isObj(a) || typeof a.label !== "string" || !ACCOUNT_LABEL.test(a.label)) continue;
    const reason = asStr(a.reason);
    const note = reason ? `실계정 전환 사유: ${reason}` : undefined;
    const classifiedBy = asStr(a.classified_by);
    accounts.push({
      ref: a.label,
      origin: target.origin,
      cls: classFromServer(asStr(a.classification)),
      approvedBy: classifiedBy || undefined,
      approvedAt: typeof a.classified_at === "string" ? a.classified_at : undefined,
      note,
      // 자격 등록 여부는 서버의 credential_configured 참일 때만 '등록됨'이다(형식 밖·없음은 미등록).
      credentialConfigured: a.credential_configured === true,
    });
  }
  return { accounts, statement: isObj(res) ? validStatement(res.account_statement) : "", mock: false };
}

// ---------- 실 API (쓰기) ----------

const accountsBase = (targetId: string) => `/api/product/targets/${encodeURIComponent(targetId)}/accounts`;

/** 계정 등록(분류 real). 라벨 형식은 서버와 같게 미리 검사한다 — 형식 밖이면 보내지 않는다(fail-closed). */
export async function registerAccount(targetId: string, label: string): Promise<void> {
  if (!ACCOUNT_LABEL.test(label)) throw new Error("invalid_account_label");
  await apiFetch<unknown>(accountsBase(targetId), {
    method: "POST",
    body: JSON.stringify({ label }),
    redirect: "error",
  });
}

/** 시험 계정 표시(account_mark_test). 승인자가 진술에 동의(statement_confirmed=true)해야 서버가 받는다(없으면 409). */
export async function markTestAccount(targetId: string, label: string): Promise<void> {
  await apiFetch<unknown>(`${accountsBase(targetId)}/${encodeURIComponent(label)}/mark-test`, {
    method: "POST",
    body: JSON.stringify({ statement_confirmed: true }),
    redirect: "error",
  });
}

/** 시험 표시 해제(account_unmark). 실계정 취급으로 되돌린다. */
export async function unmarkAccount(targetId: string, label: string): Promise<void> {
  await apiFetch<unknown>(`${accountsBase(targetId)}/${encodeURIComponent(label)}/unmark`, {
    method: "POST",
    body: "{}",
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
  statement_required: "시험 계정 표시는 진술 동의가 있어야 합니다.",
  operator_required: "운영자 권한이 필요합니다.",
  diagnosis_preparation_unavailable: "진단 준비 저장소를 지금 쓸 수 없습니다. 잠시 뒤 다시 시도하세요.",
  target_service_unavailable: "대상 관리 서비스가 응답하지 않았습니다. 잠시 뒤 다시 시도하세요.",
  product_control_unavailable: "제품 제어 서비스에 닿지 못했습니다. 잠시 뒤 다시 시도하세요.",
  authentication_changed: "로그인 세션이 바뀌었습니다. 다시 로그인한 뒤 시도하세요.",
  invalid_login_response: "서버 응답 형식이 맞지 않아 결과를 확인하지 못했습니다. 목록을 다시 읽으세요.",
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
