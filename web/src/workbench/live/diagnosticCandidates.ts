// 제품 진행 중 발견 후보 조회 (GET /api/tasks/{id}/diagnostic-candidates, 계약 docs/contracts/product-live-diagnostic-candidates.md, #694).
// 파서는 서버(server/product_candidate_read.go productCandidatePage.valid · 중계의 UTC 고정)가 만들 수 있는 모양만 받는다
// (#689 방어 파서 원칙). 하나라도 어긋나면 응답 전체를 깨진 응답으로 본다 — 일부만 그리지 않는다.
// 후보는 검증 전 검토 대상이지 확정이 아니다(절대 규칙 5). 빈 목록은 '아직 후보 없음'이지 '취약점 없음'이 아니다.

import { ApiError, apiFetchLimitedJSON } from "@/lib/diagnosis/api";
import { type SeverityLevel, severityLevel } from "@/lib/diagnosis/severity";

/** 한 응답의 후보 행 상한 = 서버 db.LiveDiagnosticCandidateLimit. */
export const MAX_LIVE_CANDIDATES = 200;
/** 공개 중계 응답 상한(server productCandidateBytes, 64 KiB). 넘으면 계약 밖 응답이다. */
export const LIVE_CANDIDATES_MAX_BYTES = 64 << 10;
/**
 * 다시 읽는 간격. 계약이 정한 5초(겹치지 않게 응답 뒤에 쉰다 — usePoll). 공식 측정 중에도 이 화면을 켜 두므로
 * 더 줄이지 않는다.
 */
export const LIVE_CANDIDATES_POLL_MS = 5000;
/** 실행 조회 경계가 없을 때(시작 확인 전 · 마감 · 결과 불명) 서버가 주는 409 코드. 오류가 아니라 '지금은 못 읽음'이다. */
export const CANDIDATE_RUN_NOT_ACTIVE = "candidate_run_not_active";

export interface LiveCandidate {
  candidateId: string;
  /** 서버 고정값 manual_review_required — 늘 '검토 필요'. */
  state: "manual_review_required";
  /** RFC3339 UTC */
  observedAt: string;
  /** 검증 조건을 채운 Worker 추정 등급. 없으면 null(미평가). 확정 등급이 아니다. */
  suggestedSeverity: SeverityLevel | null;
}

export interface LiveCandidatePage {
  taskId: string;
  /** 후보 ID 내림차순(최근 저장 순). */
  items: LiveCandidate[];
  /** 이번 응답의 행 수 = items.length. */
  returned: number;
  /** 전체 후보 수. 일부만 왔으면(truncated) null — 모른다. returned 로 채우지 않는다. */
  total: number | null;
  truncated: boolean;
}

class LiveCandidatesShapeError extends ApiError {
  constructor(where: string) {
    // 422: 200 이지만 계약 밖 응답(apiFetchLimitedJSON 의 invalid_contract 와 같은 자리)
    super(422, `invalid_candidate_page:${where}`);
    this.name = "LiveCandidatesShapeError";
  }
}

export function isLiveCandidatesShapeError(e: unknown): boolean {
  return e instanceof LiveCandidatesShapeError;
}

/** 409 candidate_run_not_active 인가 — 화면은 오류로 그리지 않고 조회를 멈춘다. */
export function isCandidateRunNotActive(e: unknown): boolean {
  return e instanceof ApiError && e.status === 409 && e.message === CANDIDATE_RUN_NOT_ACTIVE;
}

// 서버 정규식 · 범위 그대로
const DECIMAL = /^[1-9][0-9]{0,18}$/;
const INT64_MAX = BigInt("9223372036854775807");
/** Go time.Time(UTC) JSON — RFC3339Nano, 소수는 끝 0 없이 최대 9자리, 끝은 Z. */
const UTC_TIME = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d{0,8}[1-9])?Z$/;
const ZERO_TIME = "0001-01-01T00:00:00Z";
const STATE = "manual_review_required";
const TOP_KEYS = ["candidate_total", "items", "returned", "task_id", "truncated"];
const ITEM_KEYS = ["candidate_id", "observed_at", "state"];

function positiveId(v: string): bigint | null {
  if (!DECIMAL.test(v)) return null;
  const n = BigInt(v);
  return n <= INT64_MAX ? n : null;
}

function keysAre(v: object, want: readonly string[], optional: readonly string[] = []): boolean {
  const got = Object.keys(v);
  return want.every((k) => Object.hasOwn(v, k)) && got.every((k) => want.includes(k) || optional.includes(k));
}

function count(v: unknown): v is number {
  return typeof v === "number" && Number.isSafeInteger(v) && v >= 0;
}

/** 날짜 · 시각 칸이 실제로 있는 값인가(2월 30일 · 25시 같은 값은 Go 가 만들지 않는다). */
function validUtc(v: string): boolean {
  const m = UTC_TIME.exec(v);
  if (!m || v === ZERO_TIME) return false;
  const [y, mo, d, h, mi, s] = m.slice(1, 7).map(Number);
  if (y < 1 || mo < 1 || mo > 12 || d < 1 || h > 23 || mi > 59 || s > 59) return false;
  // 그 달의 마지막 날. setUTCFullYear 로 해를 정해 0~99년도 1900년대로 바뀌지 않게 한다
  const last = new Date(0);
  last.setUTCFullYear(y, mo, 0);
  return d <= last.getUTCDate();
}

function parseItem(v: unknown, i: number): LiveCandidate {
  const where = `items[${i}]`;
  if (!v || typeof v !== "object" || Array.isArray(v) || !keysAre(v, ITEM_KEYS, ["suggested_severity"]))
    throw new LiveCandidatesShapeError(where);
  const o = v as Record<string, unknown>;
  if (typeof o.candidate_id !== "string" || positiveId(o.candidate_id) === null)
    throw new LiveCandidatesShapeError(`${where}.candidate_id`);
  if (o.state !== STATE) throw new LiveCandidatesShapeError(`${where}.state`);
  if (typeof o.observed_at !== "string" || !validUtc(o.observed_at))
    throw new LiveCandidatesShapeError(`${where}.observed_at`);
  let suggestedSeverity: SeverityLevel | null = null;
  // 서버는 등급이 없으면 칸을 빼고(omitempty) 빈 문자열 · null 은 보내지 않는다
  if (Object.hasOwn(o, "suggested_severity")) {
    suggestedSeverity = severityLevel(o.suggested_severity);
    if (!suggestedSeverity) throw new LiveCandidatesShapeError(`${where}.suggested_severity`);
  }
  return { candidateId: o.candidate_id, state: STATE, observedAt: o.observed_at, suggestedSeverity };
}

/** 응답 봉투 파서. taskId 는 요청한 task(응답 task_id 와 같아야 한다). */
export function parseLiveCandidates(raw: unknown, taskId: string): LiveCandidatePage {
  if (!raw || typeof raw !== "object" || Array.isArray(raw) || !keysAre(raw, TOP_KEYS))
    throw new LiveCandidatesShapeError("envelope");
  const top = raw as Record<string, unknown>;
  if (top.task_id !== taskId) throw new LiveCandidatesShapeError("task_id");
  if (!Array.isArray(top.items) || top.items.length > MAX_LIVE_CANDIDATES) throw new LiveCandidatesShapeError("items");
  if (!count(top.returned) || top.returned !== top.items.length) throw new LiveCandidatesShapeError("returned");
  if (typeof top.truncated !== "boolean") throw new LiveCandidatesShapeError("truncated");
  const items = top.items.map(parseItem);
  // 일부만 왔으면 전체 수는 null 이고 적어도 한 행은 있다. 다 왔으면 전체 수 = 받은 행 수
  if (top.truncated) {
    if (top.candidate_total !== null || items.length === 0) throw new LiveCandidatesShapeError("candidate_total");
  } else if (!count(top.candidate_total) || top.candidate_total !== items.length)
    throw new LiveCandidatesShapeError("candidate_total");
  // 후보 ID 엄격 내림차순(중복 없음)
  for (let i = 1; i < items.length; i++) {
    if ((positiveId(items[i].candidateId) as bigint) >= (positiveId(items[i - 1].candidateId) as bigint))
      throw new LiveCandidatesShapeError(`items[${i}].order`);
  }
  return {
    taskId,
    items,
    returned: items.length,
    total: top.truncated ? null : items.length,
    truncated: top.truncated,
  };
}

/**
 * GET /api/tasks/{id}/diagnostic-candidates. query · 본문 없이 정규 십진 task ID 만 경로에 넣는다(서버가 그 밖은 400).
 * 중단 신호를 넘겨 task 가 바뀌면 남은 요청을 끊는다.
 */
export async function fetchLiveCandidates(taskId: string, signal?: AbortSignal): Promise<LiveCandidatePage> {
  if (positiveId(taskId) === null) throw new LiveCandidatesShapeError("task_id");
  const raw = await apiFetchLimitedJSON(`/api/tasks/${taskId}/diagnostic-candidates`, LIVE_CANDIDATES_MAX_BYTES, {
    cache: "no-store",
    signal,
  });
  return parseLiveCandidates(raw, taskId);
}

// ---------- 표시 글 ----------

const SEVERITY_TEXT: Record<SeverityLevel, string> = {
  critical: "심각",
  high: "높음",
  medium: "중간",
  low: "낮음",
  info: "정보성",
};

/** 후보 등급 딱지 글. '예상' 말은 붙이지 않는다(사용자 11/24 — 확정이 아니라는 건 줄의 '검토 필요'가 말한다). 등급 없음은 '위험도 미평가' — 낮음으로 바꾸지 않는다. */
export function candidateSeverityText(sev: SeverityLevel | null): string {
  return sev ? SEVERITY_TEXT[sev] : "위험도 미평가";
}

/** 목록 머리 건수 글. 일부만 왔으면 받은 행 수로 '최근 N건만 표시'(전체 수는 모른다). */
export function candidateCountText(page: LiveCandidatePage): string {
  return page.truncated ? `최근 ${page.returned}건만 표시 · 전체 수 미상` : `${page.returned}건`;
}

const ERROR_TEXT: Readonly<Record<number, string>> = {
  0: "서버에 연결하지 못했습니다.",
  401: "로그인이 필요합니다.",
  403: "이 점검의 취약점을 볼 권한을 확인하지 못했습니다(작업 · 대상 승인 변경 · 만료 · 철회).",
  404: "점검을 찾지 못했습니다.",
  502: "서버 내부 응답을 확인하지 못했습니다.",
  503: "취약점 저장 근거를 지금 읽을 수 없습니다.",
};

/** 409 외 조회 실패 안내 글(사람이 보는 글). 상태 코드만 붙이고 서버 원문은 쓰지 않는다. */
export function candidateErrorText(e: unknown): string {
  if (isLiveCandidatesShapeError(e) || (e instanceof ApiError && e.status === 422))
    return "서버 응답 형식이 계약과 달라 취약점을 표시하지 않습니다.";
  return (e instanceof ApiError && ERROR_TEXT[e.status]) || "취약점을 읽지 못했습니다.";
}
