// 탐색 결과(발견 + 경계 기록 요약) → 탐색 결과 화면 데이터.
// 계약: docs/contracts/exploration-boundary.md §4(경계 기록)·§7(발견 결과).
//
// ※ 운영자 화면 전용(§4). 이 seam 은 export·공유 보고서·모델 입력에 물리지 않는다.
//   path_pattern 은 제품 회차에서 짧은 토큰·계정명 조각이 남을 수 있어(§4 한계) 보호 저장소·운영자
//   화면에서만 보인다. 헤더 값·쿠키·본문·query 값은 계약상 없다(이름·수·yes/no 신호만, 절대규칙 2).
//
// ※ 지금 화면은 공유 워크플로 스냅샷의 끝점에서 발견(경로 · 상태 · 출처)만 그린다(discoveriesFromWorkflow).
//   경계 기록(§4)은 탐색 실행기(최민준 '발견 결과 전달' + 이동찬 '경계 기록')가 서버 경로를 내면 mapApiExploration 을
//   그 경로에 연결한다. 그 전까지 예시로 채우지 않는다.

import type { WorkflowSnapshot } from "./workflow";

// ---------- 경계 기록 (§4) ----------

/** §4 decision — 실제로 대상에 보냈나. 정본은 경계 기록이다(§4). */
export type BoundaryDecision = "forwarded" | "denied";

/** §4 reason — denied 행이 막힌 이유. 계약이 아는 값(fail-closed: 밖의 값은 unknown 으로 센다). */
export type BoundaryReason =
  | "out_of_scope_origin"
  | "out_of_scope_path"
  | "method_not_approved"
  | "risky_path"
  | "internal_address"
  | "metadata_address"
  | "control_endpoint"
  | "external_blocked_local"
  | "external_direct"
  | "external_navigation"
  | "external_method"
  | "cap_exhausted"
  | "session_closed"
  | "target_ended"
  | "ws_read_only"
  | "protocol_unsupported"
  // 모델 직접 로그인(계약 model-login-attempts §2-2·§5)
  | "login_attempts_exhausted"
  | "login_verification_pending"
  | "browser_account_in_use";

export const BOUNDARY_REASON_LABEL: Record<BoundaryReason, string> = {
  out_of_scope_origin: "승인 밖 origin",
  out_of_scope_path: "승인 밖 경로",
  method_not_approved: "승인 밖 메서드",
  risky_path: "위험 경로",
  internal_address: "내부망 주소",
  metadata_address: "메타데이터 주소",
  control_endpoint: "제어 끝점",
  external_blocked_local: "외부 차단(완전 로컬)",
  external_direct: "외부 차단(직접 입구)",
  external_navigation: "외부 이동 차단",
  external_method: "외부 비 GET·HEAD 차단",
  cap_exhausted: "상한 도달",
  session_closed: "세션 마감 뒤",
  target_ended: "대상 철회·만료",
  ws_read_only: "WebSocket 읽기 전용 차단",
  protocol_unsupported: "지원 안 하는 통신",
  login_attempts_exhausted: "로그인 연속 실패 상한",
  login_verification_pending: "로그인 확인 중(같은 계정)",
  browser_account_in_use: "브라우저에 다른 계정 로그인 중",
};

const KNOWN_REASONS = new Set<string>(Object.keys(BOUNDARY_REASON_LABEL));

/** reason 문자열 → 아는 값이면 그대로, 밖이면 null(fail-closed: 색·집계에서 unknown 으로). */
export function knownReason(reason: string): BoundaryReason | null {
  return KNOWN_REASONS.has(reason) ? (reason as BoundaryReason) : null;
}

/** reason → 화면 글. 모르는 값은 원문(조용히 버리지 않는다). forwarded 행의 빈 reason 은 "—". */
export function reasonLabel(reason: string): string {
  if (!reason) return "—";
  const k = knownReason(reason);
  return k ? BOUNDARY_REASON_LABEL[k] : reason;
}

/** §3·§4 입구 — 발신 프로세스가 아니라 네트워크 배치로 정한다(헤더로 추정 금지). */
export type Ingress = "browser" | "direct";
export const INGRESS_LABEL: Record<Ingress, string> = { browser: "브라우저 입구", direct: "직접 입구" };
const KNOWN_INGRESS = new Set<string>(["browser", "direct"]);

/** §4 dest_class — 목적지 분류. */
export type DestClass = "approved" | "external" | "internal" | "metadata" | "control";
export const DEST_CLASS_LABEL: Record<DestClass, string> = {
  approved: "승인 대상",
  external: "외부 호스트",
  internal: "내부망",
  metadata: "메타데이터",
  control: "제어 끝점",
};
const KNOWN_DEST_CLASS = new Set<string>(Object.keys(DEST_CLASS_LABEL));

/**
 * §4 경계 기록 한 행. 헤더 값·쿠키·본문·query 값은 없다(절대규칙 2) — 이름·수·yes/no 신호만.
 * reason·ingress·destClass 는 원문 문자열로 담고 화면에서 label 로 바꾼다(미등록 값도 안 버림, fail-closed).
 */
export type BoundaryRecord = {
  seq: number;
  at: string;
  decision: BoundaryDecision;
  /** denied 행만 채워진다. forwarded 는 "". */
  reason: string;
  ingress: string;
  destClass: string;
  method: string;
  status: number | null;
  bytes: number | null;
  /** 승인 대상 요청만. 가린 표시용 패턴(§4). 원문 값 아님. 없으면 undefined. */
  pathPattern?: string;
  /** 외부 요청만. 호스트 이름(사전 공지·보고서용). 없으면 undefined. */
  externalHost?: string;
  /** Authorization·Cookie 유무만(bool). 값 아님. */
  hasAuthHeader: boolean;
};

// ---------- 발견 결과 (§7) ----------

/** §7 출처 이름. observed·inferred 둘 다 이 출처를 가진다. */
export type DiscoverySource =
  | "browser_navigation"
  | "browser_xhr"
  | "browser_form"
  | "api_recon_static"
  | "api_recon_runtime";
export const DISCOVERY_SOURCE_LABEL: Record<DiscoverySource, string> = {
  browser_navigation: "브라우저 이동",
  browser_xhr: "브라우저 XHR",
  browser_form: "브라우저 폼",
  api_recon_static: "API Recon(정적)",
  api_recon_runtime: "API Recon(런타임)",
};
const KNOWN_SOURCES = new Set<string>(Object.keys(DISCOVERY_SOURCE_LABEL));

/** source → 화면 글(모르는 값은 원문). */
export function sourceLabel(source: string): string {
  return KNOWN_SOURCES.has(source) ? DISCOVERY_SOURCE_LABEL[source as DiscoverySource] : source || "알 수 없음";
}

/**
 * §7 근거 — 실제로 보낸 요청에서 나왔나(observed, 경계 seq 동반) / 번들·코드 추정(inferred).
 * 절대규칙 5: 추정을 "실제 전송"으로 올리지 않는다. 미상은 inferred 로 본다(보수적).
 */
export type Provenance = "observed" | "inferred";
export const PROVENANCE_LABEL: Record<Provenance, string> = { observed: "실제 전송", inferred: "추정" };

/**
 * §7 change — 상태 변경 가능성(diagnosisflow 와 같은 값: workflow.ts WorkflowEndpoint.change).
 * 추정 발견은 자동 방문·선택하지 않으므로 possible(§7).
 */
export type DiscoveryChange = "possible" | "attested_read" | "blocked";
export const CHANGE_LABEL: Record<DiscoveryChange, string> = {
  possible: "변경 가능",
  attested_read: "비변경 근거",
  blocked: "차단",
};
const KNOWN_CHANGE = new Set<string>(Object.keys(CHANGE_LABEL));

/** change → 화면 글(모르는 값은 보수적으로 "변경 가능"). */
export function changeLabel(change: string): string {
  return KNOWN_CHANGE.has(change) ? CHANGE_LABEL[change as DiscoveryChange] : "변경 가능";
}

/**
 * §7 발견 엔드포인트. diagnosisflow 형식(method·path·kind·change·in_scope·visited·status) +
 * 출처명·근거(observed/inferred)·키 이름을 더한다. 키·content-type 은 이름·형식만, 값은 없다(§7).
 */
export type Discovery = {
  id: string;
  source: string;
  provenance: Provenance;
  /** observed 일 때만: 경계 기록(§4)의 seq. 추정은 undefined. */
  boundarySeq?: number;
  method: string;
  /** 가린 표시용 경로 패턴(값 조각 아님). */
  path: string;
  kind: string;
  change: string;
  inScope: boolean;
  visited: boolean;
  status: number | null;
  /** query 키 이름만(값 아님, §7). */
  queryKeys: string[];
  /** JSON 본문 키 이름만(값 아님, §7). */
  bodyKeys: string[];
  /** 매개변수 없는 type/subtype(§7). 없으면 undefined. */
  contentType?: string;
};

// ---------- 묶음 ----------

/**
 * 한 회차 탐색 결과. records(경계 기록)와 discoveries(발견)는 §4·§7 로 서로 다른 목적:
 * records = "실제로 보냈나"의 정본, discoveries = 발견 근거(진단 확정 아님, 절대규칙 5).
 */
export type ExplorationResult = {
  sessionRef: string;
  /** §2 comm_mode — 외부 호스트 허용 여부. full_local 이면 외부는 전부 external_blocked_local. */
  commMode: "external_allowed" | "full_local";
  records: BoundaryRecord[];
  discoveries: Discovery[];
  /** §7 범위 밖·형식 오류로 저장 안 한 추정 발견 수(counts.discoveries_dropped). 숨기지 않고 보인다. */
  discoveriesDropped: number;
  /** true = 예시 데이터(실 탐색 아님) — UI 가 배너로 명시. */
  mock: boolean;
};

// ---------- 집계 ----------

/** 경계 기록 요약(§4): 전달·차단 수와 사유·입구·목적지·외부 호스트 분해. */
export type BoundarySummary = {
  forwarded: number;
  denied: number;
  /** denied 사유별(많은 것부터). 계약 밖 값은 reason="unknown" 한 칸으로 모은다(fail-closed). */
  byReason: { reason: string; label: string; count: number }[];
  byIngress: { ingress: string; label: string; count: number }[];
  byDestClass: { destClass: string; label: string; count: number }[];
  /** 외부 호스트별 요청 수(이름만, §4 external_host). */
  externalHosts: { host: string; count: number }[];
  /** Authorization·Cookie 가 붙은 요청 수(유무만). */
  withAuthHeader: number;
};

function tally<T>(items: readonly T[], keyOf: (t: T) => string | undefined): Map<string, number> {
  const m = new Map<string, number>();
  for (const it of items) {
    const k = keyOf(it);
    if (k === undefined || k === "") continue;
    m.set(k, (m.get(k) ?? 0) + 1);
  }
  return m;
}

function sortedCounts(m: Map<string, number>): { key: string; count: number }[] {
  return [...m.entries()]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([key, count]) => ({ key, count }));
}

export function summarizeBoundary(records: readonly BoundaryRecord[]): BoundarySummary {
  const denied = records.filter((r) => r.decision === "denied");
  // reason: 계약 밖 값은 "unknown" 한 칸으로 모아 센다(조용히 버리지 않는다).
  const reasonKey = (r: BoundaryRecord) => (r.reason ? (knownReason(r.reason) ?? "unknown") : undefined);
  const byReason = sortedCounts(tally(denied, reasonKey)).map(({ key, count }) => ({
    reason: key,
    label: key === "unknown" ? "알 수 없는 사유" : reasonLabel(key),
    count,
  }));
  const byIngress = sortedCounts(tally(records, (r) => (KNOWN_INGRESS.has(r.ingress) ? r.ingress : "unknown"))).map(
    ({ key, count }) => ({
      ingress: key,
      label: key === "unknown" ? "알 수 없음" : INGRESS_LABEL[key as Ingress],
      count,
    }),
  );
  const byDestClass = sortedCounts(
    tally(records, (r) => (KNOWN_DEST_CLASS.has(r.destClass) ? r.destClass : "unknown")),
  ).map(({ key, count }) => ({
    destClass: key,
    label: key === "unknown" ? "알 수 없음" : DEST_CLASS_LABEL[key as DestClass],
    count,
  }));
  const externalHosts = sortedCounts(tally(records, (r) => r.externalHost)).map(({ key, count }) => ({
    host: key,
    count,
  }));
  return {
    forwarded: records.length - denied.length,
    denied: denied.length,
    byReason,
    byIngress,
    byDestClass,
    externalHosts,
    withAuthHeader: records.filter((r) => r.hasAuthHeader).length,
  };
}

/** 발견 요약(§7): 전체·실제전송/추정·범위·출처별. */
export type DiscoverySummary = {
  total: number;
  observed: number;
  inferred: number;
  inScope: number;
  outOfScope: number;
  bySource: { source: string; label: string; count: number }[];
};

export function summarizeDiscoveries(discoveries: readonly Discovery[]): DiscoverySummary {
  const observed = discoveries.filter((d) => d.provenance === "observed").length;
  const inScope = discoveries.filter((d) => d.inScope).length;
  const bySource = sortedCounts(tally(discoveries, (d) => (KNOWN_SOURCES.has(d.source) ? d.source : "unknown"))).map(
    ({ key, count }) => ({ source: key, label: key === "unknown" ? "알 수 없음" : sourceLabel(key), count }),
  );
  return {
    total: discoveries.length,
    observed,
    inferred: discoveries.length - observed,
    inScope,
    outOfScope: discoveries.length - inScope,
    bySource,
  };
}

// ---------- 실 API 매퍼(fail-closed) ----------

type ApiBoundaryRow = {
  seq?: unknown;
  at?: unknown;
  decision?: unknown;
  reason?: unknown;
  ingress?: unknown;
  dest_class?: unknown;
  method?: unknown;
  status?: unknown;
  bytes?: unknown;
  path_pattern?: unknown;
  external_host?: unknown;
  has_auth_header?: unknown;
};
type ApiDiscovery = {
  id?: unknown;
  source?: unknown;
  provenance?: unknown;
  boundary_seq?: unknown;
  method?: unknown;
  path?: unknown;
  kind?: unknown;
  change?: unknown;
  in_scope?: unknown;
  visited?: unknown;
  status?: unknown;
  query_keys?: unknown;
  body_keys?: unknown;
  content_type?: unknown;
};
type ApiExploration = {
  session_ref?: unknown;
  comm_mode?: unknown;
  records?: unknown;
  discoveries?: unknown;
  discoveries_dropped?: unknown;
};

const str = (v: unknown, max = 512): string => (typeof v === "string" && v.length <= max && !v.includes("\0") ? v : "");
const intOrNull = (v: unknown): number | null => (Number.isSafeInteger(v) && Number(v) >= 0 ? Number(v) : null);
// 키 이름만: 32개·각 64바이트 이하(§7). 값 섞임을 막으려 범위 밖은 버린다.
const keyNames = (v: unknown): string[] =>
  Array.isArray(v)
    ? v.filter((k): k is string => typeof k === "string" && k.length > 0 && k.length <= 64).slice(0, 32)
    : [];

/**
 * §4·§7 응답을 ExplorationResult 로 매핑(fail-closed, 절대규칙 2·4·5).
 * - decision 은 "forwarded" 만 전달로 보고 그 밖은 "denied"(보수적).
 * - provenance 는 "observed" + boundary_seq 가 있어야 실제전송, 아니면 "inferred".
 * - 미등록 reason·ingress·dest_class·source·change 는 버리지 않고 원문으로 담아 집계에서 unknown 으로 센다.
 * - 값(헤더·쿠키·본문·query 값)은 애초에 응답에 없다. 매퍼도 이름·수·bool 만 읽는다.
 */
export function mapApiExploration(res: ApiExploration): ExplorationResult {
  const records: BoundaryRecord[] = (Array.isArray(res.records) ? res.records : []).map((raw, i) => {
    const r = (raw ?? {}) as ApiBoundaryRow;
    const forwarded = r.decision === "forwarded";
    return {
      seq: intOrNull(r.seq) ?? i,
      at: str(r.at, 64),
      decision: forwarded ? "forwarded" : "denied",
      reason: forwarded ? "" : str(r.reason, 64),
      ingress: str(r.ingress, 32),
      destClass: str(r.dest_class, 32),
      method: str(r.method, 16),
      status: intOrNull(r.status),
      bytes: intOrNull(r.bytes),
      pathPattern: str(r.path_pattern) || undefined,
      externalHost: str(r.external_host, 256) || undefined,
      hasAuthHeader: r.has_auth_header === true,
    };
  });
  const discoveries: Discovery[] = (Array.isArray(res.discoveries) ? res.discoveries : []).map((raw, i) => {
    const d = (raw ?? {}) as ApiDiscovery;
    const boundarySeq = intOrNull(d.boundary_seq);
    const observed = d.provenance === "observed" && boundarySeq !== null;
    return {
      id: str(d.id, 128) || `d${i}`,
      source: str(d.source, 32),
      provenance: observed ? "observed" : "inferred",
      boundarySeq: observed ? boundarySeq : undefined,
      method: str(d.method, 16),
      path: str(d.path),
      kind: str(d.kind, 32),
      change: str(d.change, 32),
      inScope: d.in_scope === true,
      visited: d.visited === true,
      status: intOrNull(d.status),
      queryKeys: keyNames(d.query_keys),
      bodyKeys: keyNames(d.body_keys),
      contentType: str(d.content_type, 128) || undefined,
    };
  });
  return {
    sessionRef: str(res.session_ref, 128),
    commMode: res.comm_mode === "full_local" ? "full_local" : "external_allowed",
    records,
    discoveries,
    discoveriesDropped: intOrNull(res.discoveries_dropped) ?? 0,
    mock: false,
  };
}

// ---------- 공유 워크플로 스냅샷 → 발견 (경로 · 상태 · 출처) ----------

/**
 * 발견 근거. observed = 탐색 실행기가 실제로 보낸 요청에서 관찰(경계 seq 동반), observed_detail_omitted = 관찰로
 * 기록됐지만 공개 투영의 바이트 압축으로 seq 같은 세부가 빠져 확인할 수 없음(추정으로 내리지 않는다 — #691 리뷰),
 * inferred = 번들 · 코드 추정, null = 근거 칸 없음(HTTP 크롤러 발견 — 실제로 가 봤는지는 방문 · 응답 코드로 본다).
 */
export type WorkflowEvidence = "observed" | "observed_detail_omitted" | "inferred" | null;
export const EVIDENCE_LABEL: Record<Exclude<WorkflowEvidence, null>, string> = {
  observed: "실제 관찰",
  observed_detail_omitted: "관찰 · 세부 생략",
  inferred: "추정",
};

/** 스냅샷 끝점 하나를 탐색 결과 화면용으로 다시 검증한 것. 값(헤더 · 쿠키 · 본문 · query 값)은 없다. */
export type WorkflowDiscovery = {
  id: string;
  method: string;
  /** 표시해도 되는 경로 모양만(query · 조각 · 역슬래시 · 제어 문자가 섞이면 null) */
  path: string | null;
  /** 출처 원문(라벨은 화면이 붙인다) */
  source: string;
  /** 점검 시작 때 승인한 경로(출처 start) — 탐색이 찾은 것이 아니다 */
  preRegistered: boolean;
  /** 이 경로를 찾은 페이지(경로와 같은 규칙, 없거나 모양 밖이면 null) */
  from: string | null;
  inScope: boolean;
  change: string;
  visited: boolean;
  status: number | null;
  duplicates: number;
  /** 관찰 오류 코드(라벨은 화면이 붙인다) */
  observationError: string | null;
  evidence: WorkflowEvidence;
  queryKeys: string[];
  bodyKeys: string[];
  contentType: string | null;
};

const MAX_SHOWN_PATH = 512;
/** 공백 · 제어 문자(0x20 이하 · 0x7f)와 query · 조각 · 역슬래시 */
const unsafePathChar = (c: string) => c <= " " || c === "\x7f" || c === "?" || c === "#" || c === "\\";
const safePath = (v: unknown): string | null =>
  typeof v === "string" && v.length <= MAX_SHOWN_PATH && v.startsWith("/") && ![...v].some(unsafePathChar) ? v : null;
const MEDIA_TYPE = /^[a-z0-9][a-z0-9.+-]*\/[a-z0-9][a-z0-9.+-]*$/i;

/**
 * 공유 워크플로 스냅샷(UrlFeeds 가 이미 읽고 Job 과 대조한 것)의 끝점 → 발견 목록. 추가 조회는 없다.
 * 스냅샷 파서가 보지 않는 선택 칸(discovery_confidence · exploration_seq · query_keys · body_keys · content_type)은
 * 여기서 다시 검증하고, 모양이 틀리면 그 칸만 버린다(절대규칙 4).
 */
export function discoveriesFromWorkflow(flow: Pick<WorkflowSnapshot, "endpoints">): WorkflowDiscovery[] {
  return flow.endpoints.map((e) => {
    const raw = e as unknown as Record<string, unknown>;
    const seq = intOrNull(raw.exploration_seq);
    let evidence: WorkflowEvidence = null;
    if (raw.discovery_confidence === "observed") evidence = seq !== null ? "observed" : "observed_detail_omitted";
    else if (raw.discovery_confidence === "inferred") evidence = "inferred";
    const type = str(raw.content_type, 128);
    return {
      id: e.id,
      method: e.method,
      path: safePath(e.path),
      source: e.source,
      preRegistered: e.source === "start",
      from: safePath(e.from),
      inScope: e.in_scope,
      change: e.change,
      visited: e.visited,
      status: e.status !== undefined && e.status >= 100 && e.status <= 599 ? e.status : null,
      duplicates: e.duplicates,
      observationError: e.observation_error || null,
      evidence,
      queryKeys: keyNames(raw.query_keys),
      bodyKeys: keyNames(raw.body_keys),
      contentType: MEDIA_TYPE.test(type) ? type : null,
    };
  });
}

/** 탐색 발견 수(사전 등록 경로는 빼고 센다 — 탐색을 안 돌린 회차의 시작 경로를 '발견'으로 세지 않게, #691 리뷰). */
export type WorkflowDiscoverySummary = {
  /** 스냅샷 목록 전체(사전 등록 포함) */
  listed: number;
  /** 사전 등록 경로 수 */
  preRegistered: number;
  /** 아래부터는 탐색 발견만 */
  found: number;
  inScope: number;
  outOfScope: number;
  visited: number;
  observed: number;
  observedDetailOmitted: number;
  inferred: number;
  /** 출처별 수, 많은 것부터 */
  bySource: { source: string; count: number }[];
};

export function summarizeWorkflowDiscoveries(all: readonly WorkflowDiscovery[]): WorkflowDiscoverySummary {
  const list = all.filter((d) => !d.preRegistered);
  const by = new Map<string, number>();
  for (const d of list) by.set(d.source, (by.get(d.source) ?? 0) + 1);
  const inScope = list.filter((d) => d.inScope).length;
  return {
    listed: all.length,
    preRegistered: all.length - list.length,
    found: list.length,
    inScope,
    outOfScope: list.length - inScope,
    visited: list.filter((d) => d.visited).length,
    observed: list.filter((d) => d.evidence === "observed").length,
    observedDetailOmitted: list.filter((d) => d.evidence === "observed_detail_omitted").length,
    inferred: list.filter((d) => d.evidence === "inferred").length,
    bySource: [...by].map(([source, count]) => ({ source, count })).sort((a, b) => b.count - a.count),
  };
}

// ---------- 탐색 실행 상태 (스냅샷 exploration_status) ----------

export type ExplorationStatus = "RUNNING" | "NOT_RUN" | "COMPLETED" | "PARTIAL" | "STOPPED";
export const EXPLORATION_STATUS_LABEL: Record<ExplorationStatus, string> = {
  RUNNING: "탐색 중",
  NOT_RUN: "탐색 안 함",
  COMPLETED: "탐색 마침",
  PARTIAL: "일부만 탐색",
  STOPPED: "탐색 중지",
};
const EXPLORATION_REASON_LABEL: Record<string, string> = {
  boundary_unavailable: "탐색 경계를 쓸 수 없음",
  model_unavailable: "모델을 쓸 수 없음",
  runtime_unavailable: "실행 환경을 쓸 수 없음",
  browser_unavailable: "브라우저를 쓸 수 없음",
};

/**
 * 스냅샷의 탐색 실행 상태(서버 diagnosisflow exploration_status · exploration_reason). 스냅샷 파서가 보지 않는 선택 칸이라
 * 여기서 다시 검증하고, 모르는 값은 상태를 만들지 않는다(null). 칸이 없으면(옛 회차) 상태를 말하지 않는다.
 */
export function explorationStateOf(flow: Pick<WorkflowSnapshot, never>): {
  status: ExplorationStatus | null;
  reason: string | null;
} {
  const raw = flow as Record<string, unknown>;
  const status =
    typeof raw.exploration_status === "string" && Object.hasOwn(EXPLORATION_STATUS_LABEL, raw.exploration_status)
      ? (raw.exploration_status as ExplorationStatus)
      : null;
  const reason =
    typeof raw.exploration_reason === "string" && Object.hasOwn(EXPLORATION_REASON_LABEL, raw.exploration_reason)
      ? EXPLORATION_REASON_LABEL[raw.exploration_reason]
      : null;
  return { status, reason: status ? reason : null };
}
