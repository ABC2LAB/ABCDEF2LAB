// 판정 사유 코드 → 화면 문구 (E③ 제품 판정 목록 · 판정 상세가 같이 쓴다).
// 코드 출처: 오라클 사유(oracle/model.go JudgeReason), 제품 세부 사유(e3contract/result.go ProductReason), 확정 단계 결과
// 사유(e3contract DisabledReason · StartRefusal · IntegrityReason), 판정 상세 읽기 검사 사유(server/finding_verdict_detail.go).
// 같은 코드는 어느 칸에서 와도 뜻이 같다. 표에 없는 코드는 뜻을 지어내지 않고 '서버 사유 있음'으로만 보인다.

const REASON_TEXT: Readonly<Record<string, string>> = {
  // 오라클 사유
  policy_deny_resolved: "정책상 교차 계정 접근 금지",
  policy_allows: "정책이 접근을 허용",
  sharing_or_delegation_allows: "공유 · 위임으로 허용",
  expected_access_policy_missing: "접근 정책 없음",
  expected_access_policy_invalid: "접근 정책이 올바르지 않음",
  expected_access_unknown: "허용 여부를 알 수 없음",
  sharing_or_delegation_unknown: "공유 · 위임 여부를 알 수 없음",
  subjects_distinct_verified: "두 계정이 서로 다른 사용자임을 확인",
  subject_not_verified: "계정 신원을 확인하지 못함",
  subjects_not_distinct: "두 계정이 같은 사용자",
  data_nontrivial_match: "다른 계정이 소유자 데이터를 읽음",
  data_no_match: "소유자 데이터가 보이지 않음",
  data_reflection_present: "요청 값이 그대로 되돌아온 응답일 수 있음",
  data_template_present: "누구에게나 같은 틀 응답일 수 있음",
  data_unverifiable: "데이터를 확인할 수 없음",
  // 제품 세부 사유(판단 불가의 이유)
  product_data_encoding_unsupported: "응답 인코딩을 읽을 수 없음",
  product_data_partial_response: "응답을 끝까지 받지 못함",
  product_data_check_failed: "데이터 검사 실패",
  product_owner_marker_unobserved: "소유 계정 조회에서 표식이 보이지 않음",
  product_identity_unverified: "계정 신원 확인 실패",
  product_subjects_identical: "두 계정이 같은 사용자",
  product_create_approval_denied: "시험 객체 생성 승인 거절",
  product_create_approval_timeout: "시험 객체 생성 승인 시간 초과",
  product_confirmation_budget_exhausted: "확정 단계 요청 몫 소진",
  product_request_result_unknown: "요청 결과 불명 · 다시 보내지 않음",
  product_request_denied: "전송 통제가 요청을 막음",
  product_create_failed: "시험 객체 생성 실패",
  product_request_timeout: "요청 시간 초과",
  product_write_record_unavailable: "쓰기 기록을 남기지 못해 보내지 않음",
  product_create_request_unavailable: "생성 요청을 만들 수 없어 보내지 않음",
  product_data_check_contradiction: "거부 응답인데 표식이 보임(모순)",
  product_session_expired: "교차 계정 세션 만료 가능성 · 판정 안 함",
  product_cross_denial_unverified: "거부 뒤 교차 계정 신원을 다시 확인하지 못함",
  // 확정 단계 꺼짐 사유
  not_wired: "확정 단계가 연결되지 않음",
  declaration_missing: "정책 선언 없음",
  binding_invalid: "실행 결속 확인 실패",
  policy_kind_not_product: "제품 정책 선언이 아님",
  write_not_consented: "되돌릴 수 있는 쓰기 동의 없음",
  test_accounts_unavailable: "시험 계정 인증 안 됨",
  policy_window_invalid: "선언 유효 기간이 실행 기간과 맞지 않음",
  confirmation_share_unavailable: "확정 단계 요청 몫을 받지 못함",
  no_confirmable_type: "시험할 자원 종류 없음",
  account_roles_undeclared: "계정 역할을 정하지 않음",
  accounts_privilege_unconfirmed: "두 계정이 같은 권한 등급이라는 확인 없음",
  no_confirmation_candidates: "모델 단계가 진단 요청 없이 끝나 확정할 취약점 없음",
  // 확정 단계 시작 거절 사유
  reset_confirmation_missing: "대상 리셋 확인 없음",
  previous_markers_present: "지난 시험 객체가 남아 있음",
  reset_state_unavailable: "리셋 상태를 읽지 못함",
  policy_source_unavailable: "당시 정책 원본을 읽지 못함",
  marker_generation_failed: "표식 생성 실패",
  marker_registration_failed: "표식 등록 실패",
  target_changed: "점검 중 대상이 바뀜",
  e2_execution_unavailable: "전송 통제 실행을 잡지 못해 시작 안 함",
  // 확정 단계 무결성 실패 사유
  policy_digest_mismatch: "정책 해시 불일치",
  policy_target_mismatch: "정책 대상 불일치",
  observation_invalid: "관찰 형식 오류",
  binding_mismatch: "실행 결속 불일치",
  observation_mismatch: "관찰 불일치",
  observation_reused: "같은 관찰을 다시 씀",
  journal_event_missing: "전송 기록 누락",
  journal_integrity_failed: "전송 기록 검증 실패",
  journal_event_mismatch: "전송 기록 불일치",
  authority_revoked: "권한 철회",
  // 판정 상세 읽기 검사 사유
  record_integrity_failed: "판정 기록 무결성 확인 실패",
  kg_snapshot_unavailable: "근거 위치 원본 미확인",
  journal_v3_unavailable: "송신 근거 기록 미연결",
};

/** 표에 없는 코드의 문구. 뜻을 추정하지 않는다. */
export const UNKNOWN_REASON_TEXT = "서버 사유 있음";

/** 사유 코드 하나의 화면 문구. */
export function verdictReasonText(code: string): string {
  return Object.hasOwn(REASON_TEXT, code) ? REASON_TEXT[code] : UNKNOWN_REASON_TEXT;
}

/** 판정 상세의 빠진 확정 조건(view.missing_requirements) 문구. */
export const REQUIREMENT_TEXT: Readonly<Record<string, string>> = {
  policy_check: "정책 검사",
  identity_check: "신원 검사",
  data_check: "데이터 검사",
};
