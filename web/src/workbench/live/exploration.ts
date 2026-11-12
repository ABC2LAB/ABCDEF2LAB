// 탐색 결과(발견 + 경계 기록 요약) → 탐색 결과 화면 데이터.
// 계약: docs/contracts/exploration-boundary.md §4(경계 기록)·§7(발견 결과).
//
// ※ 운영자 화면 전용(§4). 이 seam 은 export·공유 보고서·모델 입력에 물리지 않는다.
//   path_pattern 은 제품 회차에서 짧은 토큰·계정명 조각이 남을 수 있어(§4 한계) 보호 저장소·운영자
//   화면에서만 보인다. 헤더 값·쿠키·본문·query 값은 계약상 없다(이름·수·yes/no 신호만, 절대규칙 2).
//
// ※ getExploration 의 live 점등은 탐색 실행기(최민준 '발견 결과 전달' + 이동찬 '경계 기록')가
//   착륙한 뒤다. 그 전까지 jobId 없이 호출해 예시(MOCK_EXPLORATION)를 쓴다. 실 API 가 오면
//   mapApiExploration 매퍼만 라우트에 연결한다(표현/데이터 분리 유지 — UI 는 안 바뀐다).

import { apiFetch } from "@/lib/diagnosis/api";

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
  | "protocol_unsupported";

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

// ---------- 예시(mock) ----------

// §4·§7 shape 예시. 승인 대상 전달 GET·외부 CDN 차단·내부망 차단·위험 경로 차단을 섞고,
// 발견은 실제전송(observed, 경계 seq 동반)과 추정(inferred)을 구분한다.
const MOCK_EXPLORATION: ExplorationResult = {
  sessionRef: "explore-demo",
  commMode: "external_allowed",
  mock: true,
  records: [
    {
      seq: 1,
      at: "2026-11-12T01:00:00Z",
      decision: "forwarded",
      reason: "",
      ingress: "browser",
      destClass: "approved",
      method: "GET",
      status: 200,
      bytes: 4210,
      pathPattern: "/",
      hasAuthHeader: false,
    },
    {
      seq: 2,
      at: "2026-11-12T01:00:01Z",
      decision: "forwarded",
      reason: "",
      ingress: "browser",
      destClass: "approved",
      method: "GET",
      status: 200,
      bytes: 1180,
      pathPattern: "/rest/products/{id}",
      hasAuthHeader: true,
    },
    {
      seq: 3,
      at: "2026-11-12T01:00:02Z",
      decision: "forwarded",
      reason: "",
      ingress: "direct",
      destClass: "approved",
      method: "GET",
      status: 404,
      bytes: 90,
      pathPattern: "/rest/admin/{id}",
      hasAuthHeader: true,
    },
    {
      seq: 4,
      at: "2026-11-12T01:00:03Z",
      decision: "forwarded",
      reason: "",
      ingress: "browser",
      destClass: "external",
      method: "GET",
      status: 200,
      bytes: 23140,
      externalHost: "cdn.jsdelivr.net",
      hasAuthHeader: false,
    },
    {
      seq: 5,
      at: "2026-11-12T01:00:04Z",
      decision: "denied",
      reason: "external_navigation",
      ingress: "browser",
      destClass: "external",
      method: "GET",
      status: null,
      bytes: null,
      externalHost: "evil.example.com",
      hasAuthHeader: false,
    },
    {
      seq: 6,
      at: "2026-11-12T01:00:05Z",
      decision: "denied",
      reason: "out_of_scope_path",
      ingress: "browser",
      destClass: "approved",
      method: "GET",
      status: null,
      bytes: null,
      pathPattern: "/admin",
      hasAuthHeader: false,
    },
    {
      seq: 7,
      at: "2026-11-12T01:00:06Z",
      decision: "denied",
      reason: "internal_address",
      ingress: "direct",
      destClass: "internal",
      method: "GET",
      status: null,
      bytes: null,
      hasAuthHeader: false,
    },
    {
      seq: 8,
      at: "2026-11-12T01:00:07Z",
      decision: "denied",
      reason: "risky_path",
      ingress: "browser",
      destClass: "approved",
      method: "GET",
      status: null,
      bytes: null,
      pathPattern: "/rest/{id}/delete",
      hasAuthHeader: false,
    },
    {
      seq: 9,
      at: "2026-11-12T01:00:08Z",
      decision: "denied",
      reason: "metadata_address",
      ingress: "direct",
      destClass: "metadata",
      method: "GET",
      status: null,
      bytes: null,
      hasAuthHeader: false,
    },
  ],
  discoveries: [
    {
      id: "d1",
      source: "browser_navigation",
      provenance: "observed",
      boundarySeq: 1,
      method: "GET",
      path: "/",
      kind: "page",
      change: "attested_read",
      inScope: true,
      visited: true,
      status: 200,
      queryKeys: [],
      bodyKeys: [],
    },
    {
      id: "d2",
      source: "browser_xhr",
      provenance: "observed",
      boundarySeq: 2,
      method: "GET",
      path: "/rest/products/{id}",
      kind: "api",
      change: "attested_read",
      inScope: true,
      visited: true,
      status: 200,
      queryKeys: ["id"],
      bodyKeys: [],
      contentType: "application/json",
    },
    {
      id: "d3",
      source: "browser_form",
      provenance: "observed",
      boundarySeq: 3,
      method: "POST",
      path: "/rest/user/login",
      kind: "api",
      change: "possible",
      inScope: true,
      visited: false,
      status: null,
      queryKeys: [],
      bodyKeys: ["email", "password"],
      contentType: "application/json",
    },
    {
      id: "d4",
      source: "api_recon_static",
      provenance: "inferred",
      method: "GET",
      path: "/rest/admin/application-version",
      kind: "api",
      change: "possible",
      inScope: true,
      visited: false,
      status: null,
      queryKeys: [],
      bodyKeys: [],
    },
    {
      id: "d5",
      source: "api_recon_runtime",
      provenance: "inferred",
      method: "GET",
      path: "/api/BasketItems/{id}",
      kind: "api",
      change: "possible",
      inScope: false,
      visited: false,
      status: null,
      queryKeys: ["coupon"],
      bodyKeys: [],
    },
  ],
  discoveriesDropped: 2,
};

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

/**
 * 탐색 결과 조회. jobId 없으면 예시(데모), 있으면 실 라우트 매핑.
 * ※ 라우트·DTO 는 계약 §4·§7 기준 제안이다. 탐색 실행기(발견 결과 전달·경계 기록)가 착륙하면
 *   이동찬·최민준과 라우트명만 확정한다. 그 전까지 화면은 jobId=null 로 호출해 예시를 쓴다.
 */
export async function getExploration(jobId: string | null): Promise<ExplorationResult> {
  if (!jobId) return MOCK_EXPLORATION;
  const res = await apiFetch<ApiExploration>(`/api/product/exploration/${encodeURIComponent(jobId)}`, {
    cache: "no-store",
  });
  return mapApiExploration(res);
}
