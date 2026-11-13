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
// ※ 읽기: getProductTargets(대상 목록)·getAccounts(대상)=실 GET. 쓰기: registerAccount·markTestAccount·unmarkAccount=실 POST.
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
    let note: string | undefined;
    if (reason) note = `실계정 전환 사유: ${reason}`;
    else if (a.credential_configured === true) note = "자격 설정됨(값 비표시)";
    const classifiedBy = asStr(a.classified_by);
    accounts.push({
      ref: a.label,
      origin: target.origin,
      cls: classFromServer(asStr(a.classification)),
      approvedBy: classifiedBy || undefined,
      approvedAt: typeof a.classified_at === "string" ? a.classified_at : undefined,
      note,
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
