/**
 * 커버리지 시각화 데이터 — "무엇을 얼마나 검사했나"를 fail-closed 로 보여준다.
 *
 * ※ 프로토타입: 커버리지 집계 API(검사계획+시도상태, 서버/엔진)는 미구현. 아래 목업은 계약 shape 예시다.
 *   실 API 가 생기면 **getCoverage 만 교체**하면 화면은 그대로 산다(데이터 seam 1곳).
 *   표현(레이아웃)은 CoveragePanel(presentational)에만 있고, 상태·집계·fail-closed 규칙은 전부 여기(lib).
 *
 * 핵심 원칙(절대규칙 5): **조회 안 함 ≠ 없음**. "명시적 미시도(untested)"·"조회했으나 근거 0(blank)"·
 *   "판정 불가(undecidable)"는 서로 다른 칸이며, 어느 것도 "취약 없음·안전"으로 칠하지 않는다.
 */

/** 커버리지 상태 — AccessMap/finding 판정과 정합되는 분리(미시도≠기록없음≠판정불가≠확정/의심). */
export type CoverageStatus =
  | "confirmed" // 취약 확정(검사·판정됨)
  | "suspected" // 취약 의심(검사·비승격)
  | "undecidable" // 판정 불가(검사했으나 결론 못 냄)
  | "blank" // 조회 성공·관련 근거 0건(정직한 빈칸 — 미시도 아님)
  | "in_progress" // 검사 진행 중
  | "untested" // 검사 계획 있으나 **명시적 미시도**
  | "na"; // 해당 없음(명시적 applicability)

export const STATUS_LABEL: Record<CoverageStatus, string> = {
  confirmed: "취약 확정",
  suspected: "취약 의심",
  undecidable: "판정 불가",
  blank: "기록 없음",
  in_progress: "진행 중",
  untested: "미시도",
  na: "해당 없음",
};

/** 세그먼트 바·범례 색(AccessMap/finding 과 같은 팔레트). untested 는 **점선**(정상색 아님). */
export const STATUS_STYLE: Record<CoverageStatus, React.CSSProperties> = {
  confirmed: { background: "var(--danger)" },
  suspected: { background: "var(--viol)" },
  undecidable: { background: "var(--warn)" },
  blank: { background: "var(--surface-2)", border: "1px solid var(--line-2)" },
  in_progress: { background: "var(--teal)" },
  untested: { background: "transparent", border: "1px dashed var(--line-2)" },
  na: { background: "var(--faint)" },
};

/** 바·집계에 쓰는 표시 순서(심각/주의 → 미시도 → 해당없음). */
export const STATUS_ORDER: CoverageStatus[] = [
  "confirmed",
  "suspected",
  "undecidable",
  "in_progress",
  "blank",
  "untested",
  "na",
];

/** 한 검사 영역의 커버리지(비민감 라벨 + 상태별 개수). */
export type CoverageArea = {
  id: string;
  /** 검사 영역 라벨(비민감). 예: "수평 읽기 IDOR". */
  label: string;
  axis: "horizontal" | "vertical" | "state-change";
  /** 검사 계획 단위 수(명시적). 집계 무결성 대조에 쓴다. */
  planned: number;
  counts: Record<CoverageStatus, number>;
};

export type CoverageData = {
  areas: CoverageArea[];
  /** 스코프 컨텍스트(지도·판정과 같은 범위 표기). */
  policyVersion: string;
  /** true = 예시 데이터(실 집계 아님). */
  mock: boolean;
};

/** 시도함 = 확정+의심+판정불가+기록없음+진행중(미시도·해당없음 제외). */
export function attempted(a: CoverageArea): number {
  const c = a.counts;
  return c.confirmed + c.suspected + c.undecidable + c.blank + c.in_progress;
}

/** 영역의 모든 상태 합(격자 크기). */
export function total(a: CoverageArea): number {
  return STATUS_ORDER.reduce((sum, s) => sum + a.counts[s], 0);
}

/**
 * 집계 무결성 — `planned` 이 상태합(미시도·해당없음 포함)과 맞나. 안 맞으면 조용히 맞추지 않고
 * 그 영역 수를 센다(fail-closed, 데이터 불일치 숨김 금지). AccessMap projection 가드와 같은 철학.
 */
export function coverageIssues(data: CoverageData): { mismatched: number } {
  let mismatched = 0;
  for (const a of data.areas) {
    if (total(a) !== a.planned) mismatched += 1;
  }
  return { mismatched };
}

/** 전 영역 상태별 합계 + 계획·시도 총계. */
export type CoverageSummary = { planned: number; attempted: number; byStatus: Record<CoverageStatus, number> };

export function summarize(data: CoverageData): CoverageSummary {
  const byStatus: Record<CoverageStatus, number> = {
    confirmed: 0,
    suspected: 0,
    undecidable: 0,
    blank: 0,
    in_progress: 0,
    untested: 0,
    na: 0,
  };
  let planned = 0;
  let att = 0;
  for (const a of data.areas) {
    planned += a.planned;
    att += attempted(a);
    for (const s of STATUS_ORDER) byStatus[s] += a.counts[s];
  }
  return { planned, attempted: att, byStatus };
}

// ── 계약 shape 예시. 수평 읽기 IDOR(1단계 중심)·수직·상태변경 세 영역.
//    "미시도"가 큰 영역(수직·상태변경)이 정상색으로 안 뜨는 걸 보인다(= 조회 안 함 ≠ 없음).
const MOCK_COVERAGE: CoverageData = {
  policyVersion: "kg-fixture/v1:kgfx-juiceshop-s1:<digest>",
  mock: true,
  areas: [
    {
      id: "h-read",
      label: "수평 읽기 IDOR",
      axis: "horizontal",
      planned: 8,
      counts: { confirmed: 1, suspected: 1, undecidable: 2, blank: 1, in_progress: 1, untested: 2, na: 0 },
    },
    {
      id: "v-priv",
      label: "수직 권한 상승",
      axis: "vertical",
      planned: 6,
      // 후속 축 — 대부분 미시도(정상색으로 안 뜸)
      counts: { confirmed: 0, suspected: 0, undecidable: 1, blank: 0, in_progress: 0, untested: 5, na: 0 },
    },
    {
      id: "state-change",
      label: "상태 변경 · 비즈니스 로직",
      axis: "state-change",
      planned: 5,
      // E² 송신 전 차단 → 판정불가·미시도(승인·격리 필요)
      counts: { confirmed: 0, suspected: 0, undecidable: 2, blank: 0, in_progress: 0, untested: 3, na: 0 },
    },
  ],
};

/**
 * 커버리지 조회.
 * TODO(이경준): 커버리지 집계 API(검사계획+시도상태, 서버/엔진 — findings/stats·verdict 레코드·커버리지)
 *   연동 시 이 함수만 실제 fetch 로 교체한다. taskId 는 작업별 스코프용.
 */
export async function getCoverage(_taskId: string | null): Promise<CoverageData> {
  return MOCK_COVERAGE;
}
