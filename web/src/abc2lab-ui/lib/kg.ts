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

/**
 * projection 결과를 fail-closed 로 조합하는 리졸버(§4.2(1)·(2)·(7)).
 * - 중복 셀: last-writer-wins 금지 → 선택 모호 = projection-error unknown
 * - projection 미반환 셀: untested 로 추정 금지 → 미확인 = projection-error unknown
 * - 어댑터가 projection-error 를 실으면 색 추정 없이 unknown 강제
 * UI는 이 규칙으로만 칸을 정하고 스스로 판정/조합하지 않는다.
 */
export function buildCellResolver(cells: KgCell[]): (principalRef: string, resourceId: string) => ResolvedCell {
  const byKey = new Map<string, ResolvedCell>();
  const dup = new Set<string>();
  for (const c of cells) {
    const k = `${c.principalRef}|${c.resourceId}`;
    if (byKey.has(k)) {
      dup.add(k);
      continue;
    }
    byKey.set(
      k,
      c.projectionError
        ? { decision: "unknown", note: c.note, projectionError: c.projectionError }
        : { decision: c.decision, note: c.note },
    );
  }
  return (principalRef, resourceId) => {
    const k = `${principalRef}|${resourceId}`;
    if (dup.has(k)) return { decision: "unknown", projectionError: "중복 레코드 — 선택 모호" };
    return byKey.get(k) ?? { decision: "unknown", projectionError: "projection 미반환 — 미확인" };
  };
}

/** 렌더되는 격자 전체(중복·미반환 칸 포함) 기준 상태 집계. */
export function countDecisions(map: AccessMap): Record<AccessDecision, number> {
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
  for (const p of map.principals) {
    for (const r of map.resources) c[resolve(p.ref, r.id).decision] += 1;
  }
  return c;
}

// kg-policy 계약 shape 예시. 수평(A·B 교차)이 판정 축이고, 수직(admin·guest)은 후속 미리보기.
// §4.2(3) 상태를 모두 노출: 확정/의심/판정불가/정상허용/정상차단(보류)/기록없음/미검사.
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
      decision: "deny-ok",
      note: "B 자원 접근 차단 — 차단 관측 계약 전까지 보류(Phase 3)",
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
      decision: "deny-ok",
      note: "로그인 리다이렉트 — 차단 관측 계약 전까지 보류",
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
