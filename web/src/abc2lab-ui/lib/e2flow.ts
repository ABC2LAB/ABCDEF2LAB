/**
 * E② 전송 통제 흐름 → "Bash 명령 → 거기서 난 요청 → 허용·차단" 표시용 투영.
 *
 * ※ 프로토타입: E② 네트워크 경계 집행·범용 경계 감사 사건 표현은 아직 미구현·운영 비활성이다.
 *   근거: docs/e2-proxy-control-decision-draft.md(#189, dev 병합) §2·§5·§6.1(시도 ID 형식).
 *   거부 사유 enum·§5 감사 필드는 docs/contracts/e2-network-enforcement.md(#183 제안, **dev 미병합**)로
 *   "새 버전·저장 위치·검증자 합의 전까지 구현 제안"이라 필드가 더 바뀔 수 있다(#184 netboundary Go 사유와 매핑 정렬 필요).
 *   아래 목업은 그 안전 필드 shape 를 따른 **예시 데이터**다. 실 감사 투영 API 가 생기면 getE2Flow 만 교체한다.
 *
 * 안전 규칙(절대규칙 2·#189 §5.1):
 *   - 화면·이 파일에 넣지 않는 것: 명령 원문·URL·query·헤더·본문·세션/키 원문·원시 오류·프록시 귀속 토큰.
 *   - 넣는 것: 도구 호출 참조(불투명·공개 가능)·E② 시도 ID(계측용)·비민감 등록 참조·단계·안전한 사유·관측값·감사 저장 상태.
 */

/**
 * 한 HTTP 시도의 집행 결정(#183 §5). 거부 사유는 고정 enum 으로 제한한다.
 * admitted 는 게이트 통과일 뿐 **대상 도달·완전 쓰기·감사 저장의 증명이 아니다**(§5, #189 §5.3).
 */
export type E2Decision =
  | "admitted" // 승인·감사 intent·예산 예약 성공 → 송신 허용. 도달/완전 쓰기/저장 증명 아님.
  // ── 거부 사유(고정 enum, #183 §5) ──
  | "unsupported_path" // 등록 외 URL·메서드·쓰기·상태변경 GET 등 지원 밖
  | "approval_unavailable" // 승인 없음/불명
  | "revoked" // 철회됨
  | "expired" // 만료
  | "binding_mismatch" // task/run/reset·계정·경로 결속 불일치
  | "budget_unavailable" // 예산 조회 불가
  | "budget_exhausted" // 예산 소진
  | "audit_unavailable" // 감사 건강 실패(intent 단계면 송신 0, 결과 단계면 이미 송신 가능성 유지)
  | "tls_rejected" // 인증서/TLS 검증 실패
  // ── 불명(fail-closed) ──
  | "drain_unknown" // 종료·배수 불명 → 격리 유지. 성공 Seal/새 실행 강등 금지(§4).
  | "unknown"; // 미등록/불명 — 색·허용으로 추정하지 않는다.

export const DECISION_LABEL: Record<E2Decision, string> = {
  admitted: "통과(송신 허용)",
  unsupported_path: "지원 밖 경로",
  approval_unavailable: "승인 없음",
  revoked: "철회됨",
  expired: "만료",
  binding_mismatch: "결속 불일치",
  budget_unavailable: "예산 조회 불가",
  budget_exhausted: "예산 소진",
  audit_unavailable: "감사 건강 실패",
  tls_rejected: "TLS 거부",
  drain_unknown: "종료·배수 불명",
  unknown: "불명",
};

/** 화면 3분류 — 허용(통과)/차단(사유)/불명. AccessMap 과 같은 fail-closed 3분 철학. */
export type DecisionClass = "allow" | "deny" | "unknown";

const DENY_DECISIONS: ReadonlySet<E2Decision> = new Set<E2Decision>([
  "unsupported_path",
  "approval_unavailable",
  "revoked",
  "expired",
  "binding_mismatch",
  "budget_unavailable",
  "budget_exhausted",
  "audit_unavailable",
  "tls_rejected",
]);

const KNOWN_DECISIONS: ReadonlySet<E2Decision> = new Set<E2Decision>([
  "admitted",
  ...DENY_DECISIONS,
  "drain_unknown",
  "unknown",
]);

/** 송신 전에 막았다고 볼 수 있는 단계(차단 신뢰 가능). 그 밖(송신·마감·불명)은 이미 나갔을 수 있다. */
const PRESEND_STAGES: ReadonlySet<E2Stage> = new Set<E2Stage>(["pre_send", "reserved"]);

/**
 * 시도 → 화면 3분류(허용/차단/불명). fail-closed. 집행 결정값만이 아니라 **단계·쓰기 관측까지** 본다.
 * - admitted → allow (도달·저장 증명 아님은 관측 단서로 따로 표시).
 * - 거부 사유라도 **송신 전(pre_send·reserved)이고 쓰기 관측이 없을 때만** deny(차단).
 *   송신 뒤거나 쓰기가 관측됐으면 이미 대상에 갔을 수 있어 "차단"으로 단정하지 않고 `unknown`
 *   (#183 §5 "결과 감사 실패=이미 송신 가능성 유지", #184 netboundary `AuditUnknown`≠`BlockedPreSend`).
 * - drain_unknown·unknown·미등록 → unknown.
 * 호출부는 normalizeAttempt 로 정규화한 값을 넘긴다(미등록 decision/stage/write → unknown).
 */
export function decisionClass(a: Pick<E2Attempt, "decision" | "stage" | "requestWrite">): DecisionClass {
  if (a.decision === "admitted") return "allow";
  if (DENY_DECISIONS.has(a.decision)) {
    const blockedBeforeSend = PRESEND_STAGES.has(a.stage) && a.requestWrite === "none";
    return blockedBeforeSend ? "deny" : "unknown";
  }
  return "unknown"; // drain_unknown · unknown · 미등록
}

/** 시도 단계(#183 §5·§4). 안전 라벨만. */
export type E2Stage = "pre_send" | "reserved" | "sent" | "closed" | "unknown";
export const STAGE_LABEL: Record<E2Stage, string> = {
  pre_send: "송신 전 판정",
  reserved: "예약",
  sent: "송신",
  closed: "마감",
  unknown: "단계 불명",
};

/** 요청 쓰기 관측(#183 §5). admitted 라도 complete 를 단정하지 않는다. */
export type WriteObs = "none" | "partial" | "complete" | "unknown";
export const WRITE_LABEL: Record<WriteObs, string> = {
  none: "쓰기 없음",
  partial: "부분 쓰기",
  complete: "완전 쓰기 관측",
  unknown: "쓰기 불명",
};

/** 응답 관측(#183 §5). */
export type RespObs = "none" | "observed" | "unknown";
export const RESP_LABEL: Record<RespObs, string> = {
  none: "응답 없음",
  observed: "응답 관측",
  unknown: "응답 불명",
};

/** 감사 저장 상태(#183 §5·#189 §5.3). unknown/unavailable 이면 "저장됨"을 표시하지 않는다. */
export type AuditStore = "acked" | "pending" | "unavailable" | "unknown";
export const AUDIT_LABEL: Record<AuditStore, string> = {
  acked: "감사 저장 확인(ACK)",
  pending: "감사 저장 대기",
  unavailable: "감사 저장 불가",
  unknown: "감사 저장 불명",
};

const KNOWN_STAGES: ReadonlySet<E2Stage> = new Set<E2Stage>(["pre_send", "reserved", "sent", "closed", "unknown"]);
const KNOWN_WRITES: ReadonlySet<WriteObs> = new Set<WriteObs>(["none", "partial", "complete", "unknown"]);
const KNOWN_RESPS: ReadonlySet<RespObs> = new Set<RespObs>(["none", "observed", "unknown"]);
const KNOWN_AUDITS: ReadonlySet<AuditStore> = new Set<AuditStore>(["acked", "pending", "unavailable", "unknown"]);

/** 한 HTTP 시도(#183 §5 최소 논리 필드). 프록시가 발급한 시도 ID 로 귀속한다(모델·도구가 넣은 값은 신뢰 X). */
export type E2Attempt = {
  /** E② 시도 ID — 프록시 발급, 계측·대조용. 형식이 맞아도 승인 증명 아님(#189 §5.1). */
  attemptId: string;
  stage: E2Stage;
  decision: E2Decision;
  /** 비민감 안전 사유(hover). 원시 오류·모델 문자열 금지(#183 §5). */
  reason?: string;
  /** 관측 시각(epoch ms). */
  observedAt: number;
  /** 예산 예약 성공 여부(#183 §5). */
  reserved: boolean;
  requestWrite: WriteObs;
  response: RespObs;
  auditStore: AuditStore;
};

/** 한 도구(Bash) 호출과 그 호출이 낸 HTTP 시도들(#189 §5.1 — 호출 1 : 시도 N). */
export type E2Call = {
  /** 도구 호출 참조 — 불투명·공개 가능. 사용자 화면·결과 연결용(#189 §5.1). */
  callRef: string;
  /** 도구 종류(비민감 라벨, 예: Bash/curl). 명령 원문은 싣지 않는다. */
  tool: string;
  /**
   * 집계 완결 여부(#189 §5.3) — 감사 마감·내구 저장 확인 끝났는가.
   * false 면 중간 상태이며 최종 집계로 표시하지 않는다.
   */
  finalized: boolean;
  attempts: E2Attempt[];
};

export type E2Flow = {
  /** 실행 참조(task/run/reset·정책 버전) — 표시용. 모델 제출값을 권한으로 신뢰하지 않음(#189 §5.1). */
  runRef: string;
  policyVersion: string;
  calls: E2Call[];
  /** true = 예시 데이터(실 감사 투영 아님) — UI 가 배너로 명시. */
  mock: boolean;
};

/**
 * 한 시도를 fail-closed 로 정규화한다(#183 §5·#189 §5.1).
 * 미등록 enum 값(어댑터 버전 차이·프로토타입 키)은 색·허용으로 추정하지 않고 안전쪽으로 내린다.
 *   - 미등록 decision → unknown (허용으로 추정 금지)
 *   - 미등록 stage → unknown 단계
 *   - 미등록 write/response → 불명
 *   - 미등록 audit → 불명 (절대 "저장됨" 으로 승격 금지)
 */
export function normalizeAttempt(a: E2Attempt): E2Attempt {
  return {
    attemptId: a.attemptId,
    stage: KNOWN_STAGES.has(a.stage) ? a.stage : "unknown",
    decision: KNOWN_DECISIONS.has(a.decision) ? a.decision : "unknown",
    reason: a.reason,
    observedAt: Number.isFinite(a.observedAt) ? a.observedAt : 0,
    reserved: a.reserved === true,
    requestWrite: KNOWN_WRITES.has(a.requestWrite) ? a.requestWrite : "unknown",
    response: KNOWN_RESPS.has(a.response) ? a.response : "unknown",
    auditStore: KNOWN_AUDITS.has(a.auditStore) ? a.auditStore : "unknown",
  };
}

export type E2Summary = {
  calls: number;
  attempts: number;
  allow: number;
  deny: number;
  unknown: number;
  /** 완결되지 않은(중간 상태) 호출 수 — 최종 집계로 오인하지 않게 노출(#189 §5.3). */
  pending: number;
};

/** 호출·시도·3분류 집계. 정규화된 decision 기준이라 미등록 값이 섞여도 숫자로 유지된다(NaN·문자열 아님). */
export function summarize(flow: E2Flow): E2Summary {
  let allow = 0;
  let deny = 0;
  let unknown = 0;
  let attempts = 0;
  let pending = 0;
  for (const call of flow.calls) {
    if (!call.finalized) pending += 1;
    for (const raw of call.attempts) {
      const a = normalizeAttempt(raw);
      attempts += 1;
      const cls = decisionClass(a);
      if (cls === "allow") allow += 1;
      else if (cls === "deny") deny += 1;
      else unknown += 1;
    }
  }
  return { calls: flow.calls.length, attempts, allow, deny, unknown, pending };
}

// ── #189 §5 / #183(제안) shape 예시. Bash/curl 이 관측된 주 송신 경로(#189 §4)라 그걸로 구성.
//    허용(통과)·송신 전 거부·불명(송신 뒤 감사 실패/배수 불명)을 고루 담아 fail-closed 표시를 보인다.
//    시도 ID 는 #189 §6.1 형식(보안 난수 16바이트 → 소문자 hex 32자)으로 둬 실제 길이의 레이아웃도 미리 본다.
const now = Date.UTC(2026, 10, 5, 4, 30, 0);
const MOCK_FLOW: E2Flow = {
  runRef: "run · task 1 · reset g1 · policy proto-0",
  policyVersion: "proto-0",
  mock: true,
  calls: [
    {
      // 등록된 조회 2건(M04: owner GET 1 + cross GET 1) — 둘 다 통과했지만 도달·저장은 별도 관측
      callRef: "call-7a1",
      tool: "Bash · curl",
      finalized: true,
      attempts: [
        {
          attemptId: "a1f03c9e7b2d4856c0e1f2a3b4c5d6e7",
          stage: "closed",
          decision: "admitted",
          reason: "등록 자원(owner GET) — 현재 승인·예산·intent ACK 통과",
          observedAt: now,
          reserved: true,
          requestWrite: "complete",
          response: "observed",
          auditStore: "acked",
        },
        {
          attemptId: "b2e14d0af8c3e967d1f2a3b4c5d6e7f8",
          stage: "closed",
          decision: "admitted",
          reason: "등록 자원(cross GET) — 통과(수평 조회). 도달·저장은 아래 관측값으로만 판단",
          observedAt: now + 1200,
          reserved: true,
          requestWrite: "complete",
          response: "observed",
          auditStore: "acked",
        },
      ],
    },
    {
      // 지원 밖(등록 외 메서드/경로) → 송신 전 거부, 송신 0
      callRef: "call-7a2",
      tool: "Bash · curl",
      finalized: true,
      attempts: [
        {
          attemptId: "c3f2510b19d4fa78e2a3b4c5d6e7f809",
          stage: "pre_send",
          decision: "unsupported_path",
          reason: "등록 외 메서드/경로(상태변경 GET 포함) — 범위 밖",
          observedAt: now + 3000,
          reserved: false,
          requestWrite: "none",
          response: "none",
          auditStore: "acked",
        },
      ],
    },
    {
      // 예산 소진 → 차단
      callRef: "call-7a3",
      tool: "Bash · curl",
      finalized: true,
      attempts: [
        {
          attemptId: "d4035126a0e5fb89f3b4c5d6e7f8091a",
          stage: "pre_send",
          decision: "budget_exhausted",
          reason: "첫 회차 요청 한도 도달(M04) — 자동 보충/재시도 없음",
          observedAt: now + 5000,
          reserved: false,
          requestWrite: "none",
          response: "none",
          auditStore: "acked",
        },
      ],
    },
    {
      // 중지·회수 뒤 새 admission 0
      callRef: "call-7a4",
      tool: "Bash · curl",
      finalized: true,
      attempts: [
        {
          attemptId: "e5146237b1f60c90a4c5d6e7f8091a2b",
          stage: "pre_send",
          decision: "revoked",
          reason: "실행 quiescing — barrier 뒤 새 admission 차단",
          observedAt: now + 7000,
          reserved: false,
          requestWrite: "none",
          response: "none",
          auditStore: "acked",
        },
      ],
    },
    {
      // 송신 뒤 결과 감사 실패 → 이미 대상에 갔을 수 있음. "차단" 아님 → 분류 불명(decisionClass)
      callRef: "call-7a5",
      tool: "Bash · curl",
      finalized: false,
      attempts: [
        {
          attemptId: "f6257348c2071da1b5d6e7f8091a2b3c",
          stage: "sent",
          decision: "audit_unavailable",
          reason: "결과 감사 ACK 실패 — 이미 송신 가능성 유지, 이후 admission 0",
          observedAt: now + 9000,
          reserved: true,
          requestWrite: "partial",
          response: "unknown",
          auditStore: "unavailable",
        },
      ],
    },
    {
      // 종료·배수 불명 → 격리 유지, 미완료(성공 Seal 금지)
      callRef: "call-7a6",
      tool: "Bash · curl",
      finalized: false,
      attempts: [
        {
          attemptId: "07368459d3182eb2c6e7f8091a2b3c4d",
          stage: "closed",
          decision: "drain_unknown",
          reason: "잔존 소켓/자식 식별 불명 — 격리 유지, 성공 Seal 안 함",
          observedAt: now + 11000,
          reserved: true,
          requestWrite: "unknown",
          response: "unknown",
          auditStore: "unknown",
        },
      ],
    },
  ],
};

/**
 * E② 전송 통제 흐름 조회.
 * TODO(이경준): E② 감사 "표시용 투영" API(신규, 이동찬 E② 집행점·감사 소유 — #189 §2·#183 §5 제안) 연동 시
 *   이 함수만 실제 fetch 로 교체한다. taskId 는 실행별 스코프용 — 실 API 시그니처를 미리 맞춰 둔다.
 *   범용 경계 감사 사건 표현은 버전 합의 전 제안이라, 확정되면 필드 매핑을 맞춘다. 특히 #184 netboundary·
 *   local-llm-policy 의 `timed_out`·`transport_unknown` 등 추가 사유는 지금 전부 unknown(fail-closed)로 떨어지니,
 *   실 투영 연결 때 거부/불명 매핑을 명시적으로 맞춘다.
 */
export async function getE2Flow(_taskId: string | null): Promise<E2Flow> {
  return MOCK_FLOW;
}
