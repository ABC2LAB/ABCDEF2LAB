/**
 * 소유·권한 KG → 접근통제 지도 데이터 (docs/contracts/kg-policy.md §2·§4).
 *
 * ※ 프로토타입: KG 저장소·distill 어댑터·API 는 아직 미구현(kg-policy §5 "UI-only").
 *   아래 목업은 그 계약 shape 를 그대로 따른 **예시 데이터**다. 실 KG 라우트가 생기면
 *   getAccessMap 만 교체하면 화면은 그대로 산다.
 *
 * v0.1 = 수평(cross-account) IDOR 조회 전용(kg-policy §0). 수직(역할 레벨)은 후속.
 */

/** 셀 판정 — kg-policy §4 + 오라클 판정과 정합. "미검사 ≠ 판정불가 ≠ 취약"을 분리(절대규칙 5·fail-closed). */
export type AccessDecision =
  | "allow-ok" // 정상 허용 (기대 allow, 관측 allow)
  | "deny-ok" // 정상 차단 (기대 deny, 관측 deny)
  | "vuln" // 취약 (기대 deny인데 관측 allow = 수평 IDOR)
  | "unknown" // 판정 불가 (조회했으나 정책·공유 미해소 → UNDECIDABLE)
  | "untested" // 미검사 (안 찔러봄 — 커버리지 공백, "없음" 아님)
  | "na"; // 해당 없음

export const DECISION_LABEL: Record<AccessDecision, string> = {
  "allow-ok": "정상 허용",
  "deny-ok": "정상 차단",
  vuln: "취약",
  unknown: "판정 불가",
  untested: "미검사",
  na: "—",
};

/** abc2lab.css 셀 클래스 매핑 */
export const DECISION_CELL: Record<AccessDecision, string> = {
  "allow-ok": "cell-ok",
  "deny-ok": "cell-blk",
  vuln: "cell-vuln",
  unknown: "cell-unknown",
  untested: "cell-none",
  na: "cell-na",
};

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
  /** unknown/vuln 사유 요약(hover) */
  note?: string;
};

export type AccessMap = {
  principals: KgPrincipal[];
  resources: KgResource[];
  cells: KgCell[];
  policyVersion: string;
  /** true = 예시 데이터(실 KG 아님) — UI가 배너로 명시 */
  mock: boolean;
};

// kg-policy 계약 shape 예시. 수평(A·B 교차)이 판정 축이고, 수직(admin·guest)은 후속 미리보기.
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
    { principalRef: "계정 A", resourceId: "r1", decision: "allow-ok", note: "본인 자원" },
    { principalRef: "계정 A", resourceId: "r2", decision: "deny-ok", note: "B 자원 접근 차단(정상)" },
    { principalRef: "계정 A", resourceId: "r3", decision: "allow-ok", note: "본인 장바구니" },
    { principalRef: "계정 A", resourceId: "r4", decision: "deny-ok", note: "관리 기능 차단(정상)" },
    // 계정 B (수평) — 핵심: B가 A의 주문을 읽음
    {
      principalRef: "계정 B",
      resourceId: "r1",
      decision: "vuln",
      note: "B가 A의 주문 조회됨 — A의 canary 노출(수평 IDOR)",
    },
    { principalRef: "계정 B", resourceId: "r2", decision: "allow-ok", note: "본인 자원" },
    {
      principalRef: "계정 B",
      resourceId: "r3",
      decision: "unknown",
      note: "장바구니 공유 정책 미해소 → UNDECIDABLE(조회안함≠없음)",
    },
    { principalRef: "계정 B", resourceId: "r4", decision: "deny-ok", note: "관리 기능 차단(정상)" },
    // 관리자 (수직 — 후속)
    { principalRef: "관리자", resourceId: "r1", decision: "allow-ok" },
    { principalRef: "관리자", resourceId: "r2", decision: "allow-ok" },
    { principalRef: "관리자", resourceId: "r3", decision: "allow-ok" },
    { principalRef: "관리자", resourceId: "r4", decision: "allow-ok" },
    // 비로그인 (수직 — 후속)
    { principalRef: "비로그인", resourceId: "r1", decision: "deny-ok", note: "로그인으로 리다이렉트" },
    { principalRef: "비로그인", resourceId: "r2", decision: "untested", note: "아직 안 찔러봄" },
    { principalRef: "비로그인", resourceId: "r3", decision: "deny-ok" },
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
 * TODO(이경준): KG API(신규 라우트) 연동 시 이 함수만 실제 fetch 로 교체(kg-policy §4·§5).
 * taskId 는 작업별 스코프용 — 실 API 시그니처를 미리 맞춰 둔다.
 */
export async function getAccessMap(_taskId: string | null): Promise<AccessMap> {
  return MOCK_MAP;
}
