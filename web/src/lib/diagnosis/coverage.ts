/** 작업대 실데이터 계약 §5: 기존 workflow snapshot의 방문·선택·응답 관찰. E③ 판정이 아니다. */
import { ApiError, apiFetch } from "./api";
import type { URLDiagnosisJob } from "./diagnosis-url";

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
  blank: "응답 관찰·진단 미판정",
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
  axis: "horizontal" | "vertical" | "state-change" | "workflow";
  /** 검사 계획 단위 수(명시적). 집계 무결성 대조에 쓴다. */
  planned: number;
  counts: Record<CoverageStatus, number>;
};

export type CoverageData = {
  endpoints?: CoverageEndpoint[];
  workflowCounts?: Record<string, number | null>;
  stage?: string;
  snapshotRevision?: number;
  jobId?: string;
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
  if (data.endpoints && data.workflowCounts) {
    const found = data.workflowCounts.found;
    const visited = data.workflowCounts.visited;
    if (typeof found === "number" && found < data.endpoints.length) mismatched++;
    if (typeof visited === "number" && visited < data.endpoints.filter((e) => e.visited === true).length) mismatched++;
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

export type CoverageEndpoint = {
  id: string;
  method: string;
  path: string;
  source: string;
  visited: boolean | null;
  selected: boolean | null;
  status: number | null;
  observationError: string;
  coverage: CoverageStatus;
};
export const WORKFLOW_COUNT_LABEL = {
  found: "발견",
  visited: "방문",
  queued: "대기",
  sent: "송신",
  attempted: "시도",
  unknown: "불명",
  blocked: "차단",
  held: "보류",
  omitted: "생략",
  invalid_paths: "형식 밖 경로",
} as const;
const SOURCE_LABEL: Record<string, string> = {
  start: "시작 URL",
  script_call: "스크립트 호출 표현식",
  script_guess: "스크립트 호출 추정",
  script_prefix: "스크립트 경로 접두부",
  static_literal: "정적 문자열",
  html_link: "HTML 링크",
  html_form: "HTML 폼",
  html_script: "HTML 스크립트",
  bundle_chunk: "번들 청크",
  bundle_string: "번들 문자열",
  api_recon_static: "API 정적 발견",
  api_recon_runtime: "API 실제 관찰",
  browser_navigation: "브라우저 이동",
  browser_xhr: "브라우저 요청",
  browser_form: "브라우저 폼",
};
const ERROR_LABEL: Record<string, string> = {
  fetch_failed: "가져오기 실패",
  request_failed: "요청 실패",
  response_parse_failed: "응답 해석 실패",
  unsupported_status: "미지원 상태 코드",
  http_error: "HTTP 오류 응답",
  body_incomplete: "응답 본문 불완전",
};
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const natural = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0;
const ref = (v: unknown): v is string => typeof v === "string" && /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(v);
const invalid = (): never => {
  throw new ApiError(422, "invalid_coverage_contract");
};
function boundJob(job: URLDiagnosisJob | null): job is URLDiagnosisJob {
  return (
    !!job &&
    ref(job.id) &&
    typeof job.task_id === "string" &&
    /^[1-9][0-9]*$/.test(job.task_id) &&
    /^[a-f0-9]{64}$/.test(job.source_digest) &&
    ["ai_auto_v1", "user_approval_v1"].includes(job.input.workflow_mode ?? "")
  );
}
/** 조회·정규화·집계는 이 seam에서만 한다. 화면은 서버가 모르는 값을 0/안전으로 채우지 않는다. */
export function parseCoverage(value: unknown, job: URLDiagnosisJob): CoverageData {
  if (!boundJob(job) || !object(value) || !object(value.workflow)) return invalid();
  const snapshot = value.workflow;
  if (
    snapshot.format !== "abc2lab.workbench-flow/v1" ||
    snapshot.job_id !== job.id ||
    snapshot.task_id !== job.task_id ||
    snapshot.source_digest !== job.source_digest ||
    snapshot.run_ref !== `run-${job.id}` ||
    snapshot.mode !== job.input.workflow_mode ||
    !natural(snapshot.revision) ||
    snapshot.revision < 1 ||
    !Array.isArray(snapshot.endpoints)
  )
    return invalid();
  const ids = new Set<string>();
  const endpoints: CoverageEndpoint[] = [];
  for (const raw of snapshot.endpoints) {
    if (!object(raw) || !ref(raw.id) || ids.has(raw.id)) return invalid();
    ids.add(raw.id);
    const visited = typeof raw.visited === "boolean" ? raw.visited : null;
    const selected = typeof raw.selected === "boolean" ? raw.selected : null;
    const status = natural(raw.status) && raw.status >= 100 && raw.status <= 599 ? raw.status : null;
    const error =
      raw.observation_error === undefined
        ? ""
        : typeof raw.observation_error === "string" && raw.observation_error !== ""
          ? (ERROR_LABEL[raw.observation_error] ?? "관찰 오류(상세 비표시)")
          : "관찰 오류 불명";
    const coverage: CoverageStatus = error
      ? "undecidable"
      : visited !== true
        ? "untested"
        : status === null
          ? "undecidable"
          : "blank";
    endpoints.push({
      id: raw.id,
      method: ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"].includes(String(raw.method))
        ? String(raw.method)
        : "UNKNOWN",
      path:
        typeof raw.path === "string" &&
        raw.path.startsWith("/") &&
        raw.path.length <= 512 &&
        !/[?#\\\x00-\x20\x7f]/.test(raw.path)
          ? raw.path
          : "",
      source: SOURCE_LABEL[String(raw.source)] ?? "출처 불명",
      visited,
      selected,
      status,
      observationError: error,
      coverage,
    });
  }
  const counts: Record<CoverageStatus, number> = {
    confirmed: 0,
    suspected: 0,
    undecidable: 0,
    blank: 0,
    in_progress: 0,
    untested: 0,
    na: 0,
  };
  for (const e of endpoints) counts[e.coverage]++;
  const workflowCounts: Record<string, number | null> = {};
  for (const key of Object.keys(WORKFLOW_COUNT_LABEL))
    workflowCounts[key] = object(snapshot.counts)
      ? natural(snapshot.counts[key])
        ? snapshot.counts[key]
        : ["held", "omitted", "invalid_paths"].includes(key) && snapshot.counts[key] === undefined
          ? 0
          : null
      : null;
  const stage = ["crawling", "awaiting_selection", "diagnosing", "completed", "stopped", "failed", "unknown"].includes(
    String(snapshot.stage),
  )
    ? String(snapshot.stage)
    : "unknown";
  return {
    mock: false,
    policyVersion: "unknown",
    jobId: job.id,
    snapshotRevision: snapshot.revision,
    stage,
    endpoints,
    workflowCounts,
    areas: [{ id: "workflow", label: "현재 snapshot 끝점", axis: "workflow", planned: endpoints.length, counts }],
  };
}
export async function getCoverage(job: URLDiagnosisJob | null, signal?: AbortSignal): Promise<CoverageData> {
  if (!boundJob(job)) return invalid();
  return parseCoverage(
    await apiFetch<unknown>(
      `/api/product/workflows/${encodeURIComponent(job.id)}`,
      { cache: "no-store", redirect: "error", signal },
      false,
    ),
    job,
  );
}
