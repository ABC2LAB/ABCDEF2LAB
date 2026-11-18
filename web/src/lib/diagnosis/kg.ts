/**
 * 소유·권한 KG → 접근통제 지도 데이터 (docs/contracts/kg-policy.md §2·§4).
 *
 * ※ getAccessMap 은 실 라우트 `GET /api/tasks/{id}/access-map`(docs/contracts/access-map-read.md ·
 *   product-report-graph-read.md)만 읽는다. task 가 없으면 부르지 않고, 예시 지도로 채우지 않는다(409 도 빈 격자 아님).
 *   색(decision)은 서버 `state` 로만 정하고(confirmed→vuln), 미등록 state·projection_error 는 리졸버가 fail-closed unknown(§4.2).
 *
 * v0.1 = 수평(cross-account) IDOR 조회 전용(kg-policy §0·§4.2(4)). 수직(역할 레벨)은 후속.
 */

import { ApiError, apiFetch } from "./api";

/**
 * 셀 판정 — kg-policy §4.2(3) 셀 상태표와 정합.
 * "미검사 ≠ 기록없음 ≠ 판정불가 ≠ 의심 ≠ 확정"을 분리한다(절대규칙 5·fail-closed).
 * 확정(vuln)과 의심(suspected)은 **별도 색**으로 구분한다(배지만으로 X — §4.2(3) 축소 화면 오인 방지).
 */
export type AccessDecision =
  | "allow-ok" // 정상 허용 (oracle_value=allowed + 동일 자원·정상 대조·신원 확인 모두 충족, §4.2(3))
  | "deny-ok" // 정상 차단 — ❌ Phase 3 보류(앱 인가 차단 관측 계약 전까지 산출 안 함, §4.2(3))
  | "vuln" // 취약 확정 (oracle_value=confirmed ∧ 위반후보)
  | "suspected" // 취약 의심 (oracle_value=suspected — 비승격)
  | "unknown" // 판정 불가 (UNDECIDABLE · 출처/선택 오류 · 상충 · 실행실패/E² 거부)
  | "untested" // 미검사 (검사계획+시도상태로 명시적 미시도 확인 — ❌ Phase 2)
  | "blank" // 기록 없음 (조회 성공 + 같은 범위 근거 전무를 확인한 정직한 빈칸)
  | "na"; // 해당 없음 (명시적 applicability 근거)

export const DECISION_LABEL: Record<AccessDecision, string> = {
  "allow-ok": "정상 허용",
  "deny-ok": "정상 차단",
  vuln: "취약 확정",
  suspected: "취약 의심",
  unknown: "판정 불가",
  untested: "미검사",
  blank: "기록 없음",
  na: "—",
};

/** abc2lab.css 셀 클래스 매핑 */
export const DECISION_CELL: Record<AccessDecision, string> = {
  "allow-ok": "cell-ok",
  "deny-ok": "cell-blk",
  vuln: "cell-vuln",
  suspected: "cell-suspected",
  unknown: "cell-unknown",
  untested: "cell-none",
  blank: "cell-blank",
  na: "cell-na",
};

/** oracle_value 확정/의심 배지(§4.2(7)). 색으로 1차 구분하고 배지로 보강한다. */
export const DECISION_BADGE: Partial<Record<AccessDecision, string>> = {
  vuln: "확정",
  suspected: "의심",
};

/** deny-ok 는 Phase 3 보류 상태임을 화면에 명시한다(§4.2(3)·(7)). */
export const HELD_DECISIONS: ReadonlySet<AccessDecision> = new Set<AccessDecision>(["deny-ok"]);

/**
 * 계약이 아는 판정값(§4.1(c)). 리졸버는 이 집합 밖 값을 색으로 추정하지 않고 unknown 으로 내린다.
 * 어댑터가 `confirmed`·`SUSPECTED`·`safe`·`""`·`constructor` 같은 미등록/프로토타입 키를 실어도
 * 무색 칸·집계 깨짐(NaN·문자열) 대신 fail-closed 로 막힌다.
 */
const KNOWN_DECISIONS: ReadonlySet<AccessDecision> = new Set<AccessDecision>([
  "allow-ok",
  "deny-ok",
  "vuln",
  "suspected",
  "unknown",
  "untested",
  "blank",
  "na",
]);

/** 수평(v0.1 범위) / 수직(후속) */
export type KgAxis = "H" | "V";

/** kg-policy §2 Principal — 별칭 ref 만. 계정 원문·비밀번호·토큰 값 금지(절대규칙 2) */
export type KgPrincipal = { ref: string; role: string; axis: KgAxis };

/** kg-policy §2 Resource — (endpoint, object-id, owner) 바인딩의 표시용 축약 */
export type KgResource = { id: string; label: string; ownerRef: string };

export type KgCell = {
  principalRef: string;
  resourceId: string;
  decision: AccessDecision;
  /** 오라클 사유 요약(hover) — canary 는 필드명만, 값 금지(§4.2(5)·절대규칙 2) */
  note?: string;
  /**
   * projection-error 사유 — 오라클 사유(note)와 **별도 네임스페이스**(§4.2(2)).
   * 조회 실패·출처 미검증·선택 모호·상충 등. 있으면 decision 은 unknown 으로 강제된다.
   */
  projectionError?: string;
};

export type AccessMap = {
  principals: KgPrincipal[];
  resources: KgResource[];
  cells: KgCell[];
  policyVersion: string;
  /** true = 예시 데이터(실 KG 아님) — UI가 배너로 명시 */
  mock: boolean;
};

/** 한 교차 칸의 최종 표시값. 색을 추정하지 않고 fail-closed(§4.2(2)·(7)). */
export type ResolvedCell = { decision: AccessDecision; note?: string; projectionError?: string };

/** principal·resource 자유문자열이 구분자와 충돌하지 않도록 키를 안전하게 만든다(예: ref 에 `|` 포함). */
function cellKey(principalRef: string, resourceId: string): string {
  return JSON.stringify([principalRef, resourceId]);
}

/**
 * 셀 하나를 fail-closed 로 정규화한다(§4.1(c)·§4.2(2)·(3)·(7)). 색(decision)을 추정하지 않는다.
 * 우선순위: projection-error → 미등록 판정값 → 보류(deny-ok 등) → 그대로.
 */
function normalizeCell(c: KgCell): ResolvedCell {
  // projection-error 는 빈 문자열이라도 "오류 있음"으로 본다(fail-closed). note 와 별도 네임스페이스(§4.2(2)).
  if (c.projectionError != null) {
    // 빈 사유는 대체 문구로 — 여기선 "" 도 대체해야 하므로 ?? 가 아니라 길이 검사.
    const reason = c.projectionError.length > 0 ? c.projectionError : "projection 오류(사유 미기재)";
    return { decision: "unknown", note: c.note, projectionError: reason };
  }
  // 계약에 없는 판정값(미등록·프로토타입 키)은 색 추정 없이 unknown(§4.1(c)).
  if (!KNOWN_DECISIONS.has(c.decision)) {
    return { decision: "unknown", note: c.note, projectionError: "미등록 판정값" };
  }
  // deny-ok 등 보류 판정은 앱 인가 차단 관측 계약(Phase 3) 전까지 산출하지 않는다(§4.2(3)).
  if (HELD_DECISIONS.has(c.decision)) {
    return { decision: "unknown", note: c.note, projectionError: "차단 미증명 — 관측 계약 전 보류(Phase 3)" };
  }
  return { decision: c.decision, note: c.note };
}

/**
 * projection 결과를 fail-closed 로 조합하는 리졸버(§4.2(1)·(2)·(3)·(7)).
 * - 중복 셀: last-writer-wins 금지 → 선택 모호 = projection-error unknown
 * - projection 미반환 셀: untested 로 추정 금지 → 미확인 = projection-error unknown
 * - projection-error·미등록 판정값·보류(deny-ok) 는 normalizeCell 이 unknown 으로 내린다
 * UI는 이 규칙으로만 칸을 정하고 스스로 판정/조합하지 않는다.
 */
export function buildCellResolver(cells: KgCell[]): (principalRef: string, resourceId: string) => ResolvedCell {
  const byKey = new Map<string, ResolvedCell>();
  const dup = new Set<string>();
  for (const c of cells) {
    const k = cellKey(c.principalRef, c.resourceId);
    if (byKey.has(k)) {
      dup.add(k);
      continue;
    }
    byKey.set(k, normalizeCell(c));
  }
  return (principalRef, resourceId) => {
    const k = cellKey(principalRef, resourceId);
    if (dup.has(k)) return { decision: "unknown", projectionError: "중복 레코드 — 선택 모호" };
    return byKey.get(k) ?? { decision: "unknown", projectionError: "projection 미반환 — 미확인" };
  };
}

/** 첫 등장만 남기는 distinct(§4.2 N4) — 중복 선언이 집계·렌더를 왜곡하지 않도록. */
function distinctBy<T>(items: T[], key: (t: T) => string): T[] {
  const seen = new Set<string>();
  const out: T[] = [];
  for (const it of items) {
    const k = key(it);
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(it);
  }
  return out;
}

/**
 * 렌더되는 격자 전체(중복·미반환 칸 포함) 기준 상태 집계.
 * - axis 를 주면 해당 축(수평 H / 수직 V) 신원만 센다 — 헤드라인 칩은 판정 축(H)만 세기 위함(§0·§4.2(4)).
 * - 신원·자원 목록에 중복 ref 가 있어도 distinct 격자 기준으로 한 번만 센다(§4.2 N4).
 */
export function countDecisions(map: AccessMap, axis?: KgAxis): Record<AccessDecision, number> {
  const resolve = buildCellResolver(map.cells);
  const c: Record<AccessDecision, number> = {
    "allow-ok": 0,
    "deny-ok": 0,
    vuln: 0,
    suspected: 0,
    unknown: 0,
    untested: 0,
    blank: 0,
    na: 0,
  };
  const scoped = axis ? map.principals.filter((p) => p.axis === axis) : map.principals;
  const principals = distinctBy(scoped, (p) => p.ref);
  const resources = distinctBy(map.resources, (r) => r.id);
  for (const p of principals) {
    for (const r of resources) c[resolve(p.ref, r.id).decision] += 1;
  }
  return c;
}

/**
 * projection 무결성 점검(§4.2(2)·N4) — 어긋난 레코드를 조용히 버리지 않고 센다.
 * - outOfGrid: 선언된 격자(principals×resources) 밖을 가리켜 렌더에서 묻히는 레코드 수
 * - dupPrincipals / dupResources: 신원·자원 목록의 중복 ref 수(집계·렌더 왜곡 원인)
 * UI 는 이 값이 0 이 아니면 fail-closed 경고를 띄운다(데이터 불일치를 숨기지 않음).
 */
export type MapProjectionIssues = { outOfGrid: number; dupPrincipals: number; dupResources: number };

export function mapProjectionIssues(map: AccessMap): MapProjectionIssues {
  const principalRefs = new Set(map.principals.map((p) => p.ref));
  const resourceIds = new Set(map.resources.map((r) => r.id));
  let outOfGrid = 0;
  for (const cell of map.cells) {
    if (!principalRefs.has(cell.principalRef) || !resourceIds.has(cell.resourceId)) outOfGrid += 1;
  }
  return {
    outOfGrid,
    dupPrincipals: map.principals.length - principalRefs.size,
    dupResources: map.resources.length - resourceIds.size,
  };
}

/** 무결성 문제가 하나라도 있나(§4.2 N4). */
export function hasProjectionIssues(iss: MapProjectionIssues): boolean {
  return iss.outOfGrid > 0 || iss.dupPrincipals > 0 || iss.dupResources > 0;
}

/**
 * access-map-read.md 조회 응답(서버 projection). 전부 참조·yes/no/unknown 신호·필드명만 —
 * URL·자격·계정 원문·표식 값·본문은 계약상 포함되지 않는다(§4.2·절대규칙 2).
 */
type ApiSignal = "yes" | "no" | "unknown";
type ApiAccessCell = {
  principal: string;
  owner_ref: string;
  resource_ref: string;
  resource_label?: string;
  state: string; // Phase1: confirmed/suspected/allow-ok/unknown/blank
  oracle_value: string | null; // confirmed/suspected/undecidable/allowed — 원값 보존
  applicable: boolean | null;
  record_id: number | null;
  case_ref: string | null;
  observed_at: string | null;
  reason_codes?: string[];
  canary_fields?: string[];
  projection_error?: string | null;
  // 비교/체크 신호(현재 note 요약엔 안 쓰지만 계약 필드로 받아 둔다)
  policy_check?: ApiSignal;
  identity_check?: ApiSignal;
  data_check?: ApiSignal;
  same_resource?: ApiSignal;
  owner_normal_data?: ApiSignal;
  cross_normal_data?: ApiSignal;
  nontrivial_data_match?: ApiSignal;
  reflection_excluded?: ApiSignal;
  template_excluded?: ApiSignal;
};
type ApiAccessMap = {
  task_id: number;
  run_ref?: string;
  reset_generation?: string;
  axis?: string;
  policy_version: string;
  cells?: ApiAccessCell[];
};

/** 서버 state → 내 AccessDecision. confirmed 는 vuln 으로, 그 외는 동명. 미등록 state 는 매핑 안 함(fail-closed). */
const API_STATE_DECISION: Record<string, AccessDecision> = {
  confirmed: "vuln",
  suspected: "suspected",
  "allow-ok": "allow-ok",
  unknown: "unknown",
  blank: "blank",
};

/** 셀 note(hover) — 사유 코드·적용성·표식 필드명만. 값·URL·자격은 넣지 않는다(절대규칙 2). */
function cellNote(c: ApiAccessCell): string | undefined {
  const parts: string[] = [];
  if (c.reason_codes?.length) parts.push(c.reason_codes.join(", "));
  if (c.applicable === false) parts.push("적용 불가 근거 있음");
  else if (c.applicable == null) parts.push("적용 여부 미확인");
  if (c.canary_fields?.length) parts.push(`표식 필드: ${c.canary_fields.join(", ")}`);
  const s = parts.join(" · ");
  return s.length > 0 ? s : undefined;
}

/**
 * access-map-read.md 응답을 AccessMap 으로 매핑한다(fail-closed, §4.2).
 * - 색은 서버 `state` 로만(confirmed→vuln). 미등록 state 는 unknown + projection_error("미등록 state").
 * - 서버 `projection_error` 는 그대로 실어 리졸버가 unknown 으로 내리게 한다(관측없음≠판단불가 분리).
 * - 신원·자원은 셀에서 distinct 파생. resource_ref 가 전역 유일(owner_ref 는 그 자원의 소유자)이라
 *   계약의 4튜플 키 (principal, owner_ref, resource_ref, axis) 가 그리드에선 (principal, resource_ref) 로 수렴.
 */
export function mapApiAccessMap(res: ApiAccessMap): AccessMap {
  const cells: KgCell[] = (res.cells ?? []).map((c) => {
    const decision = API_STATE_DECISION[c.state];
    return {
      principalRef: c.principal,
      resourceId: c.resource_ref,
      decision: decision ?? "unknown",
      note: cellNote(c),
      // 서버 오류 우선, 없고 state 가 미등록이면 fail-closed 표시.
      projectionError: c.projection_error ?? (decision ? undefined : `미등록 state: ${c.state}`),
    };
  });
  const principals: KgPrincipal[] = distinctBy(
    (res.cells ?? []).map((c) => ({ ref: c.principal, role: "", axis: "H" as KgAxis })),
    (p) => p.ref,
  );
  const resources: KgResource[] = distinctBy(
    (res.cells ?? []).map((c) => ({
      id: c.resource_ref,
      label: c.resource_label || c.resource_ref,
      ownerRef: c.owner_ref,
    })),
    (r) => r.id,
  );
  return { principals, resources, cells, policyVersion: res.policy_version, mock: false };
}

/**
 * 접근통제 지도 조회. task 는 현재 Job 응답의 task_id(정규 양의 정수 문자열)만 받는다.
 * HTTP 오류(401 · 403 · 404 · 409 · 502 · 503 …)는 ApiError 그대로 던진다 — 화면은 이유를 보이고 빈 격자 · 예시로 바꾸지
 * 않는다(product-report-graph-read.md 오류 표). 응답의 task_id 가 요청한 task 와 다르면 다른 점검의 지도라 거부한다.
 */
export async function getAccessMap(taskId: string, signal?: AbortSignal): Promise<AccessMap> {
  if (!/^[1-9][0-9]*$/.test(taskId) || !Number.isSafeInteger(Number(taskId)))
    throw new ApiError(422, "invalid_access_map_task");
  const res = await apiFetch<unknown>(`/api/tasks/${encodeURIComponent(taskId)}/access-map`, {
    cache: "no-store",
    redirect: "error",
    signal,
  });
  return mapApiAccessMap(checkAccessMapResponse(res, taskId));
}

// ── 응답 모양 검증(access-map-read.md 응답 절 · product-report-graph-read.md). 칸이 빠졌거나 축이 수평이 아니면 응답 전체를
//    거부한다 — 정상 `cells: []`(빈 지도)과 칸 누락을 구분하고, 수직 축 셀을 수평 판정으로 그리지 않는다(#691 리뷰).
const POLICY_VERSION = /^kg-fixture\/v1:[^:\s]+:[0-9a-f]{64}$/;
const SIGNAL_FIELDS = [
  "policy_check",
  "identity_check",
  "data_check",
  "same_resource",
  "owner_normal_data",
  "cross_normal_data",
  "nontrivial_data_match",
  "reflection_excluded",
  "template_excluded",
] as const;
const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const isText = (v: unknown): v is string => typeof v === "string" && v.length > 0;
const isNames = (v: unknown): v is string[] => Array.isArray(v) && v.every(isText);
const isOptText = (v: unknown) => v === null || isText(v);
function badMap(): never {
  throw new ApiError(422, "invalid_access_map_contract");
}
function checkCell(c: unknown): ApiAccessCell {
  if (
    !isObj(c) ||
    !isText(c.principal) ||
    !isText(c.owner_ref) ||
    !isText(c.resource_ref) ||
    !isText(c.resource_label) ||
    !isText(c.state) ||
    !isOptText(c.oracle_value) ||
    !(c.applicable === null || typeof c.applicable === "boolean") ||
    !(c.record_id === null || (Number.isSafeInteger(c.record_id) && Number(c.record_id) > 0)) ||
    !isOptText(c.case_ref) ||
    !isOptText(c.observed_at) ||
    !Array.isArray(c.reason_codes) ||
    !c.reason_codes.every(isText) ||
    !isNames(c.canary_fields) ||
    !isOptText(c.projection_error) ||
    SIGNAL_FIELDS.some((k) => k in c && c[k] !== "yes" && c[k] !== "no" && c[k] !== "unknown")
  )
    return badMap();
  return c as ApiAccessCell;
}
/** 지도 응답 검증. 요청한 task 와 다른 task 의 지도면 다른 점검의 결과라 거부한다. */
export function checkAccessMapResponse(res: unknown, taskId: string): ApiAccessMap {
  if (!isObj(res) || !Number.isSafeInteger(res.task_id) || String(res.task_id) !== taskId)
    throw new ApiError(422, "access_map_task_mismatch");
  if (
    !isText(res.run_ref) ||
    !isText(res.reset_generation) ||
    res.axis !== "horizontal" ||
    !isText(res.policy_version) ||
    !POLICY_VERSION.test(res.policy_version) ||
    !Array.isArray(res.cells)
  )
    return badMap();
  return { ...(res as ApiAccessMap), cells: res.cells.map(checkCell) };
}
