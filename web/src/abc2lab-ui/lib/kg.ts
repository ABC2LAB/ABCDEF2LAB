/**
 * 소유·권한 KG → 접근통제 지도 데이터 (docs/contracts/kg-policy.md §2·§4).
 *
 * ※ 프로토타입: KG 저장소·서버 projection read API 는 아직 미구현(kg-policy §4.2(1)·§5).
 *   아래 목업은 그 계약 shape 를 그대로 따른 **예시 데이터**다. 실 KG 라우트가 생기면
 *   getAccessMap 만 교체하면 화면은 그대로 산다.
 *
 * v0.1 = 수평(cross-account) IDOR 조회 전용(kg-policy §0·§4.2(4)). 수직(역할 레벨)은 후속.
 */

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

// kg-policy 계약 shape 예시. 수평(A·B 교차)이 판정 축이고, 수직(admin·guest)은 후속 미리보기.
// Phase 2 산출 상태를 노출: 확정/의심/판정불가/정상허용/기록없음/미검사.
// deny-ok(정상 차단)은 Phase 3 보류라 산출하지 않는다 — 교차계정 차단 칸은 '차단 미증명'으로 unknown(§4.2(3)).
const MOCK_MAP: AccessMap = {
  policyVersion: "proto-0",
  mock: true,
  principals: [
    { ref: "계정 A", role: "일반 사용자", axis: "H" },
    { ref: "계정 B", role: "일반 사용자", axis: "H" },
    { ref: "관리자", role: "관리자", axis: "V" },
    { ref: "비로그인", role: "게스트", axis: "V" },
  ],
  resources: [
    { id: "r1", label: "주문 #1012", ownerRef: "계정 A" },
    { id: "r2", label: "주문 #1077", ownerRef: "계정 B" },
    { id: "r3", label: "장바구니", ownerRef: "계정 A" },
    { id: "r4", label: "관리자 통계", ownerRef: "관리자" },
  ],
  cells: [
    // 계정 A (수평)
    { principalRef: "계정 A", resourceId: "r1", decision: "allow-ok", note: "본인 자원(정상 대조·신원 확인됨)" },
    {
      principalRef: "계정 A",
      resourceId: "r2",
      decision: "unknown",
      note: "B 자원 접근 실패 관측 — 앱 인가 차단 관측 계약 전까지 '정상 차단'으로 산출하지 않음(차단 미증명 보류, Phase 3)",
    },
    { principalRef: "계정 A", resourceId: "r3", decision: "allow-ok", note: "본인 장바구니" },
    {
      principalRef: "계정 A",
      resourceId: "r4",
      decision: "unknown",
      note: "관리 자원 — 차단 미증명(위반 미관찰이지 차단 확인 아님)",
    },
    // 계정 B (수평) — 핵심: B가 A의 주문을 읽음
    {
      principalRef: "계정 B",
      resourceId: "r1",
      decision: "vuln",
      note: "B가 A의 주문 조회됨 — A의 canary 노출(수평 IDOR 확정)",
    },
    { principalRef: "계정 B", resourceId: "r2", decision: "allow-ok", note: "본인 자원" },
    {
      principalRef: "계정 B",
      resourceId: "r3",
      decision: "suspected",
      note: "B가 A 장바구니에 유사 응답 — canary 미노출로 확정 불가(SUSPECTED, 비승격)",
    },
    { principalRef: "계정 B", resourceId: "r4", decision: "unknown", note: "관리 자원 — 차단 미증명" },
    // 관리자 (수직 — 후속 미리보기)
    { principalRef: "관리자", resourceId: "r1", decision: "allow-ok" },
    { principalRef: "관리자", resourceId: "r2", decision: "allow-ok" },
    { principalRef: "관리자", resourceId: "r3", decision: "allow-ok" },
    { principalRef: "관리자", resourceId: "r4", decision: "allow-ok" },
    // 비로그인 (수직 — 후속 미리보기)
    {
      principalRef: "비로그인",
      resourceId: "r1",
      decision: "unknown",
      note: "로그인 리다이렉트 관측 — 차단으로 단정 못 함(차단 미증명 보류, Phase 3)",
    },
    {
      principalRef: "비로그인",
      resourceId: "r2",
      decision: "blank",
      note: "조회 성공·관련 근거 0건(판정했으나 불가·명시적 미시도와 구분되는 정직한 빈칸)",
    },
    { principalRef: "비로그인", resourceId: "r3", decision: "untested", note: "검사계획상 명시적 미시도" },
    {
      principalRef: "비로그인",
      resourceId: "r4",
      decision: "vuln",
      note: "비로그인이 관리자 통계 접근(수직 권한상승) — 후속 축",
    },
  ],
};

/**
 * 접근통제 지도 조회.
 * TODO(이경준): 서버 projection read API(신규 라우트, 이동찬 소유 §4.2(1)·(7)) 연동 시
 *   이 함수만 실제 fetch 로 교체한다. taskId 는 작업별 스코프용 — 실 API 시그니처를 미리 맞춰 둔다.
 */
export async function getAccessMap(_taskId: string | null): Promise<AccessMap> {
  return MOCK_MAP;
}
