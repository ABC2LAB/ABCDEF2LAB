// 계정 분류(시험 계정 표시) seam — 목업.
// 계약: docs/contracts/exploration-boundary.md §8(계정과 인증)·§9-7(저장 위치·승인 주체 = 이동찬·이경준 미정).
//
// ※ 절대규칙 2 · 수용시험 S09: 계정 원문·비밀번호·토큰·세션 값은 다루지 않는다. ref(별칭)·대상 origin·역할·분류 상태만.
//   값은 애초에 없다. 모델 입력·로그·근거 파일·export 에 넣지 않는다.
// ※ §8 fail-closed: 승인된 '시험 계정' 표시가 아니면 실계정으로 본다. 표시 없음·판단 불가도 실계정.
// ※ 효력 시점(development-plan.md 계정 분류 항목·CLAUDE.md 절대규칙 2): §8은 아직 목표 규칙이고 현행은 S09다.
//   계정 분류 기능·S09·§8 개정이 함께 들어오기 전에는 이 분류가 어떤 계정에도 탐색·모델 입력 권한을 주지 않는다.
//   나중에도 허용은 승인된 일회용 시험 계정에 대상이 발급한 세션만이고 비밀번호는 계속 제외된다.
// ※ 저장 위치·승인 주체 미정(§8 "결정·구현 전", §9-7) → 이 seam 은 목업이다. 실 배선은 이동찬 정렬 후
//   getAccounts(targetRef) 만 실 저장소에 연결한다(표현/데이터 분리 — 화면은 안 바뀐다). register/approve 는
//   지금 저장되지 않는다(화면이 "목업"을 명시).

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
 * 탐색·모델 입력 권한 판정이 아니다 — 지금은 S09 현행이라 true 여도 권한이 없다(위 효력 시점).
 */
export function usableAsTestAccount(cls: string): boolean {
  return normalizeClass(cls) === "test_approved";
}

/**
 * 분류된 계정 한 행. 자격(비밀번호·토큰·세션)은 없다(절대규칙 2 · S09) — ref·origin·역할·상태만.
 * approvedBy 는 승인 주체 ref(미정 — 목업). 사람 값이 아니라 식별 별칭이다.
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

/** 한 묶음의 분류 결과. mock=true 면 예시(실 저장소 아님) — 화면이 배너로 명시. */
export type AccountClassification = {
  accounts: ClassifiedAccount[];
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
  /** 승인된 시험 계정 분류 수(= testApproved, 분류 판정). 지금은 탐색·모델 입력 권한을 뜻하지 않는다(S09 현행). */
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

// ---------- 예시(mock) ----------

// ref 는 자격이 아니라 식별 별칭이다(절대규칙 2). 승인된 시험·승인 대기·미표시(실계정 취급)를 섞는다.
const MOCK_ACCOUNTS: ClassifiedAccount[] = [
  {
    ref: "acct:test-01",
    origin: "https://target.example",
    role: "일반 사용자",
    cls: "test_approved",
    approvedBy: "operator:mock",
    approvedAt: "2026-11-12T00:00:00Z",
    note: "일회용 시험 계정",
  },
  { ref: "acct:test-02", origin: "https://target.example", role: "일반 사용자", cls: "test_pending" },
  { ref: "acct:admin-demo", origin: "https://target.example", role: "관리자", cls: "test_pending" },
  { ref: "acct:unknown-07", origin: "https://target.example", cls: "unmarked", note: "표시 없음 — 실계정으로 본다" },
  { ref: "acct:legacy-03", origin: "https://target.example", role: "일반 사용자", cls: "unmarked" },
];

/**
 * 계정 분류 조회. 지금은 목업만 반환한다(저장 위치·승인 주체 미정, §9-7).
 * ※ 실 저장소·라우트는 이동찬 정렬 후 확정한다. 그때 targetRef 로 실 분류를 읽어 매핑하고, 이 함수만 교체한다.
 */
export async function getAccounts(_targetRef: string | null): Promise<AccountClassification> {
  return { accounts: MOCK_ACCOUNTS, mock: true };
}
