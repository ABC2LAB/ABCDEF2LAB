// 시험 전용 판정 레코드 예시(verdict-record §2 shape). 제품 화면의 fallback 으로 사용하지 않는다.
// canary 는 필드명만, 요청은 논리 resource_ref — 값 · raw URL 없음.
import type { FindingRecord } from "./finding";

// ── verdict-record §2 shape 예시. 수평 IDOR CONFIRMED 1건.
//    canary 는 **필드명만**(값 금지), 요청은 논리 resource_ref(raw URL 아님).
const now = Date.UTC(2026, 10, 6, 4, 30, 0);
export const findingConfirmed: FindingRecord = {
  id: "ov-0001",
  title: "다른 사용자의 자원이 교차 계정으로 조회됨",
  severity: "high",
  axis: "horizontal",
  vulnClass: "수평 IDOR",
  resourceRef: "basket:a-owned-0001",
  oracleValue: "confirmed",
  vulnVerdict: "confirmed",
  accessPolicyResult: "위반_후보",
  checks: { policy: "yes", identity: "yes", data: "yes" },
  reasonCodes: ["policy_deny_resolved", "identity_distinct_verified", "nontrivial_marker_crossed"],
  signals: {
    sameResource: "yes",
    ownerNormalData: "yes",
    crossNormalData: "yes",
    nontrivialDataMatch: "yes",
    reflectionExcluded: "yes",
    templateExcluded: "yes",
  },
  canaryFields: ["body"], // 필드명만 — 값은 저장·표시 안 함
  comparison: [
    {
      label: "계정 A",
      roleNote: "자원 소유자",
      resourceRef: "basket:a-owned-0001",
      observation: "본인 자원 정상 응답",
      row: "ok",
    },
    {
      label: "계정 B",
      roleNote: "무관한 사용자",
      resourceRef: "basket:a-owned-0001",
      observation: "A의 비자명 표식이 응답 필드에 그대로 나옴(반사·템플릿 배제됨)",
      row: "vuln",
    },
    {
      label: "비로그인",
      roleNote: "대조군",
      resourceRef: "basket:a-owned-0001",
      observation: "인증 요구로 응답 미수신",
      row: "und",
    },
  ],
  subjects: { ownerRef: "계정 A", evaluatedRef: "계정 B", ownerStatus: "verified", evaluatedStatus: "verified" },
  control: {
    policyVersion: "kg-fixture/v1:kgfx-juiceshop-s1:<digest>",
    resetGeneration: "reset-gen-1",
    observedAt: now,
    modelTag: "openai/qwen3.6-35b-a3b",
    hintMatchesCanonical: true,
    expectedAccess: "deny",
  },
  trafficEvidenceRefs: ["tr-8f3a", "tr-8f41"],
  mock: true,
};

// ── E²가 송신 전에 차단한 상태변경 후보 → 미실행 → 증거 없음 → UNDECIDABLE.
//    수평 읽기 1단계 밖(상태변경)이라 승인·격리 시험환경 필요(destructive-last, 절대규칙 6).
export const findingHeld: FindingRecord = {
  id: "ov-0002",
  title: "상태 변경 요청이 막히는지 미확인",
  severity: "medium",
  axis: "horizontal",
  vulnClass: "비즈니스 로직 · 상태 변경",
  resourceRef: "checkout:a-session-0001",
  oracleValue: "undecidable",
  vulnVerdict: "undecidable",
  accessPolicyResult: "판정_불가",
  // 실행 안 됨 → 세 체크·6 신호 모두 unknown(모름을 아님/없음으로 접지 않음).
  checks: { policy: "unknown", identity: "unknown", data: "unknown" },
  reasonCodes: ["e2_blocked_pre_send", "state_change_not_executed"],
  signals: {
    sameResource: "unknown",
    ownerNormalData: "unknown",
    crossNormalData: "unknown",
    nontrivialDataMatch: "unknown",
    reflectionExcluded: "unknown",
    templateExcluded: "unknown",
  },
  canaryFields: [],
  comparison: [], // 전송이 없어 응답 비교 자체가 없음
  subjects: { ownerRef: "계정 A", evaluatedRef: "계정 B", ownerStatus: "verified", evaluatedStatus: "verified" },
  control: {
    policyVersion: "kg-fixture/v1:kgfx-juiceshop-s1:<digest>",
    resetGeneration: "reset-gen-1",
    observedAt: Date.UTC(2026, 10, 6, 5, 0, 0),
    modelTag: "openai/qwen3.6-35b-a3b",
    hintMatchesCanonical: true,
    expectedAccess: "deny",
  },
  trafficEvidenceRefs: [],
  intent: "결제 단계에서 금액을 낮춰 보내 주문이 그대로 생성되는지",
  stateChanging: true,
  transmission: "held_pre_send",
  mock: true,
};
