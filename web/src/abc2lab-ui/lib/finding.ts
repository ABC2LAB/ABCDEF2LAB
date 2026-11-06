/**
 * 발견 상세 = 구조화 판정 레코드(verdict-record.md, 최민준) → 화면 읽기 필드.
 *
 * ※ 프로토타입: E③ 오라클·판정 레코드 저장은 미구현. 아래 목업은 verdict-record §2(a~e) shape 를
 *   그대로 따른 **예시 데이터**다. 실 레코드 API 가 생기면 getFinding 만 교체하면 화면은 그대로 산다.
 *   경계: 시각화 읽는 필드 = 이경준(verdict-record Open #4). 레코드 의미·마스킹 = 최민준.
 *
 * 마스킹(verdict-record §3 · 절대규칙 2·8 — 하드 규칙):
 *   - 화면·이 파일에 넣지 않는 것: 응답 본문 원문, **canary 값**, 인증 헤더·쿠키·토큰·비밀·계정 원문, raw URL.
 *   - 넣는 것: 필드 **이름**, 비교 신호 **enum**, 증거 **ref/id**, 주체 **라벨**, 논리 `resource_ref`.
 */

import type { Severity } from "../types";

/** oracle 원값(verdict-record §2(b)). 감사·충실성 위해 그대로 보존. */
export type OracleValue = "confirmed" | "suspected" | "undecidable" | "allowed";

/** 세 체크·6 비교 신호 공통 3값(§2(b)(c)). "모름"을 "아님"으로 접지 않는다. */
export type Signal = "yes" | "no" | "unknown";

/** 정책 관찰 축(§2(b) access_policy_result). */
export type AccessPolicyResult = "위반_후보" | "위반_미관찰" | "판정_불가";

/** VerdictBadge(ui.tsx)와의 매핑. allowed 는 취약 배지 없음(정책상 허용). */
export const ORACLE_BADGE: Record<OracleValue, { verdict: "ok" | "sus" | "und" | null; label: string }> = {
  confirmed: { verdict: "ok", label: "확정" },
  suspected: { verdict: "sus", label: "의심" },
  undecidable: { verdict: "und", label: "판단 불가" },
  allowed: { verdict: null, label: "정책상 허용" },
};

export const SIGNAL_LABEL: Record<Signal, string> = { yes: "충족", no: "불충족", unknown: "불명" };

/** 6 비교 신호 라벨(§2(c)). canary_fields 는 별도(필드명만). */
export const COMPARISON_SIGNALS: { key: keyof FindingRecord["signals"]; label: string }[] = [
  { key: "sameResource", label: "같은 자원 대조" },
  { key: "ownerNormalData", label: "소유자 정상 응답" },
  { key: "crossNormalData", label: "교차 요청 응답 수신" },
  { key: "nontrivialDataMatch", label: "비자명 표식 일치" },
  { key: "reflectionExcluded", label: "요청 반사 배제" },
  { key: "templateExcluded", label: "공통 템플릿 배제" },
];

/** 다계정 비교 한 행 — 신호 기반 안전 라벨만(요청/응답 원문 아님). */
export type ComparisonRow = {
  /** 주체 라벨(owner_ref/evaluated_ref/대조군). 계정 원문 아님. */
  label: string;
  roleNote: string;
  /** 논리 자원 참조(type:id). raw URL 아님. */
  resourceRef: string;
  /** 신호 기반 관측 요약(원문·본문 아님). */
  observation: string;
  /** 행 표시 판정. */
  row: "ok" | "sus" | "und" | "vuln";
};

/** verdict-record §2(a~e) 중 화면이 읽는 부분(Open #4). */
export type FindingRecord = {
  id: string;
  title: string;
  severity: Severity;
  axis: "horizontal" | "vertical";
  /** ARTEX VulnClass 라벨(비민감). */
  vulnClass: string;
  /** 논리 자원 참조(엔드포인트 서명, type:id). raw URL 아님. */
  resourceRef: string;

  // (b) 판정
  oracleValue: OracleValue;
  /** triage 대면 판정 또는 null(allowed 일 때만 null). */
  vulnVerdict: "confirmed" | "suspected" | "undecidable" | null;
  accessPolicyResult: AccessPolicyResult;
  checks: { policy: Signal; identity: Signal; data: Signal };
  /** JudgeReason(정책→신원→데이터 순). 비민감 사유 코드. */
  reasonCodes: string[];

  // (c) 비교 신호
  signals: {
    sameResource: Signal;
    ownerNormalData: Signal;
    crossNormalData: Signal;
    nontrivialDataMatch: Signal;
    reflectionExcluded: Signal;
    templateExcluded: Signal;
  };
  /** 표식이 산 응답 **필드명만**(값·본문 금지 — §3). */
  canaryFields: string[];
  comparison: ComparisonRow[];

  // (d)(e) 통제변수·근거 ref
  subjects: { ownerRef: string; evaluatedRef: string; ownerStatus: string; evaluatedStatus: string };
  control: {
    policyVersion: string;
    resetGeneration: string;
    observedAt: number;
    modelTag: string;
    hintMatchesCanonical: boolean;
    expectedAccess: string;
  };
  trafficEvidenceRefs: string[];

  /** true = 예시 데이터(실 레코드 아님). */
  mock: boolean;
};

/**
 * CONFIRMED 증거 3조건이 모두 모였는지(§4 · 세 체크 + 비자명 표식 + 반사·템플릿 배제).
 * UI 는 oracle_value 를 **덮지 않고**(판정은 오라클 몫) 이 값으로 "왜 확정/판단불가"를 정직하게 보인다.
 */
/** 6 비교 신호가 **전부 YES** 인가 — e3-oracle §3(c): 하나라도 NO/UNKNOWN 이면 data 가 YES 가 아니다. */
function allSignalsYes(r: FindingRecord): boolean {
  return COMPARISON_SIGNALS.every((s) => r.signals[s.key] === "yes");
}

export function confirmedEvidenceComplete(r: FindingRecord): boolean {
  const c = r.checks;
  return c.policy === "yes" && c.identity === "yes" && c.data === "yes" && allSignalsYes(r);
}

/** 부족한 증거 목록(판단 불가·의심일 때 "무엇이 비었나"). yes 가 아닌 체크·핵심 신호를 라벨로. */
export function missingEvidence(r: FindingRecord): string[] {
  const out: string[] = [];
  if (r.checks.policy !== "yes") out.push(`정책 체크(${SIGNAL_LABEL[r.checks.policy]})`);
  if (r.checks.identity !== "yes") out.push(`신원 체크(${SIGNAL_LABEL[r.checks.identity]})`);
  if (r.checks.data !== "yes") out.push(`데이터 체크(${SIGNAL_LABEL[r.checks.data]})`);
  // 6 비교 신호 전부 점검(e3-oracle §3(c) — same_resource·owner/cross_normal 포함).
  for (const s of COMPARISON_SIGNALS) {
    if (r.signals[s.key] !== "yes") out.push(`${s.label}(${SIGNAL_LABEL[r.signals[s.key]]})`);
  }
  return out;
}

/**
 * 비자명 데이터 접근을 **주장할 수 있는가** — e3-oracle §3(c)의 6 비교 신호(동일 자원·소유자 정상 응답·
 * 교차 정상 응답·비자명 표식 일치·요청 반사 배제·공통 템플릿 배제)가 **전부 yes** 일 때만.
 * 그 전엔 "표식이 나타났다/B가 A의 실제 자원을 받았다"를 화면에서 단정하지 않는다(증거 없는 취약 관찰 금지).
 */
export function dataAccessProven(r: FindingRecord): boolean {
  // e3-oracle §3(c): 6 신호 전부 YES(동일 자원·양쪽 정상 응답 포함)라야 교차 데이터 접근을 단정한다.
  return allSignalsYes(r);
}

/**
 * 화면에 **표시할** 판정. 오라클 원값(`oracleValue`)은 감사용으로 보존하되, 화면 판정은 증거가 부족하면
 * 보수적으로 내린다(절대규칙 5·E③ 보수성). `confirmed` 인데 증거 3조건이 불완비면 화면은 `undecidable`.
 * 원값과 다르면 UI 가 "원값은 감사용"으로 함께 보인다(원값을 숨기지 않음).
 */
export function displayVerdict(r: FindingRecord): OracleValue {
  if (r.oracleValue === "confirmed" && !confirmedEvidenceComplete(r)) return "undecidable";
  return r.oracleValue;
}

// ── verdict-record §2 shape 예시. 수평 IDOR CONFIRMED 1건.
//    canary 는 **필드명만**(값 금지), 요청은 논리 resource_ref(raw URL 아님).
const now = Date.UTC(2026, 10, 6, 4, 30, 0);
const MOCK_FINDING: FindingRecord = {
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

/**
 * 발견 상세 조회.
 * TODO(이경준): 판정 레코드 읽기 API(verdict-record Open #4·#1, 이동찬 서버 조립) 연동 시
 *   이 함수만 실제 fetch 로 교체한다. findingId 는 레코드/ARTEX finding 스코프용.
 */
export async function getFinding(_findingId: string | null): Promise<FindingRecord> {
  return MOCK_FINDING;
}
