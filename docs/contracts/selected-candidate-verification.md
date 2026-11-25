# 후보별 선택 검증: 저장 관찰 경로 (초안)

이 문서의 **구현됨** 표시는 `stored/v1` 재검토에만 적용한다. 후보별 새 HTTP 송신, 승인·거부·취소, 별도 E② 실행과 현재 대상의 재판정은 아직 구현되지 않았다. 프런트엔드는 이 API가 승인 계획이나 실행 권한을 제공한다고 해석하지 않는다. #925의 `passive/v2` 자동 검증과 E③ IDOR 판정, #918 이행점검의 의미는 그대로 유지한다.

## 구현된 경계

- 제품 관리 호스트에서만 `GET`, `POST /api/product/tasks/{task_id}/candidates/{candidate_id}/verifications`를 받는다. 보고서 전용 호스트에는 쓰기 권한이 없다. ID는 양의 정규 int64 십진수이며, 경로 인코딩을 받지 않는다. `GET`은 선택적 `?before={verification_id}` 커서만 허용한다. 응답은 `Cache-Control: no-store`다.
- 기존 Bearer 인증과 보호된 preparation 서비스의 `AuthorizeTask` 소유 Job receipt를 요청 전·후에 확인한다. 종료된 **정상 완료 passive/v2** 작업의 원본 후보만 선택할 수 있다. 후보 원본 digest·작업/회차 결속, 종료 기록의 자동 판정을 재검증한다. 확정·부정 판정에 필요한 저장 뒤 시도 표식도 검증한다.
- `POST` 본문은 `{"request_id":"synthetic-request-001","evidence_digest":"<64 lowercase hex>"}`다. `Content-Type: application/json`이며 알 수 없는 필드·중복 JSON 키·4 KiB 초과 본문은 거부한다. `request_id`는 호출자가 정한 8~128자 영숫자·점·밑줄·하이픈의 멱등키다. `evidence_digest`는 조회한 원본 후보의 digest다. 다른 digest는 409다.
- 이 경로는 자동 판정에 이미 영속 저장된 **비민감 출처 projection**만 다시 검증하고 요청/사건 이력을 추가한다. 대상 HTTP 및 모델 호출은 0건이다. 현재 종료 기록에는 자동 판정에 사용하지 않은 다른 E② 관찰을 모두 보존하지 않는다. 따라서 부족한 자동 판정을 같은 저장 자료만으로 확정으로 승격하지 않는다. 부족은 `completed/insufficient`로 남긴다. `awaiting_approval`은 실제 승인 계획이 없으므로 이 단계에서 반환하지 않는다.
- 동일 소유자·후보·멱등키의 재요청은 같은 이력을 돌려준다. 다른 키라도 동일 후보/digest/출처/판정의 재검토는 중복 기록하지 않는다. task 행 잠금으로 동시 요청을 직렬화한다. 최신 이력부터 최대 100건씩 반환하며 `truncated=true`이면 `next_before`를 다음 GET의 `before`로 사용한다.
- `diagnostic_candidate_verification_requests`와 `diagnostic_candidate_verification_events`는 원본 후보 및 최초 자동 판정·종료 기록을 수정하지 않는 추가 기록이다. 요청 INSERT trigger가 task·candidate·run·digest·version 결속을 확인한다. `product_api`만 SELECT/INSERT 및 ID sequence USAGE를 얻으며 UPDATE/DELETE/TRUNCATE는 받지 않는다. 기존 설치에는 스키마 마이그레이션과 설치 역할 GRANT 적용이 필요하다.

## 응답과 상태

`GET`과 `POST`는 같은 형태의 JSON을 반환한다. [`합성 fixture`](fixtures/selected-candidate-verification.json)를 참고한다.

| 필드 | 의미 |
| --- | --- |
| `task_id`, `candidate_id`, `evidence_digest` | 선택한 원본 작업·후보·원본 증거의 결속 |
| `automatic.status`, `automatic.reason` | 최초 진단 종료 시 저장한 `passive/v2` 자동 판정. 이후 요청으로 변경하지 않음 |
| `verifications[]` | 최근 요청부터 내림차순. 각 `id`는 이력 ID, `request_id`는 멱등키, `requested_at`은 DB 시각 |
| `verifications[].method` | 현재는 `stored/v1`만 가능 |
| `original_evidence_version`, `original_evidence_digest`, `original_run_ref` | 요청 당시 원본 결속. 현재 원본과 다르면 조회·보고서 실패 |
| `latest.process_state` | 현재 구현은 `completed`만 저장. `checking_stored_evidence`, `awaiting_approval`, `running`, `failed`, `cancelled`, `expired`는 미래 실행용 예약 상태이며 지금 반환하지 않음 |
| `latest.verdict` | `confirmed`, `negative`, `insufficient`, `unsupported`. `completed`에서 반드시 있다. 처리 실패나 미승인 상태를 판정으로 치환하지 않음 |
| `latest.reason`, `rule_version` | 고정 사유 코드, 현재 `passive/v2` |
| `latest.sources`, `sources_digest` | 기존 종료 기록과 일치하는 안전한 관찰 projection 및 그 JSON 바이트 SHA-256. 같은 대상 응답의 사본은 추가 독립 근거가 아님 |
| `latest.execution_ref`, `approval_ref` | 현재 경로에서는 생략. 저장 관찰 재검토는 새 E② 실행·승인을 만들지 않음 |
| `truncated`, `next_before` | 뒤 페이지가 있으면 true와 다음 커서. 마지막 페이지에는 `next_before` 생략 |

오류는 `{"error":"<고정 코드>"}`다. `400 invalid_verification_request`는 경로·메서드·본문·커서 오류, `401 authentication_required`는 인증 부재/변경, `403 verification_scope_denied`는 Job 소유권/권한 거부, `404 task_not_found`와 `candidate_not_found`는 해당 범위의 자료 부재, `409 diagnosis_not_completed`와 `candidate_evidence_changed`는 실행/증거 상태 불일치, `409 verification_already_pending`은 미래의 승인·실행 중 요청과 충돌할 때 사용한다. `503 verification_owner_unavailable`, `verification_storage_unavailable`, `verification_run_invalid`, `verification_evidence_invalid`, `candidate_integrity_failed`는 안전한 재시도 또는 운영 조사가 필요하며 정상 판정으로 해석하지 않는다. 저장 이력의 결속·digest가 훼손되면 조회와 보고서 출력을 503으로 막는다.

정상 완료 v2 HTML·DOCX의 항목별 **최근 선택 검증**은 원본 자동 판정과 별도의 처리 상태로 표시한다. 현재 `stored/v1`은 자동 판정을 재확인할 뿐이므로 보고서의 취약점 수치나 원본 종료 기록은 달라지지 않는다. 같은 PostgreSQL 조회 snapshot으로 원본과 최근 이력을 읽고 검증한다. 이미 다운로드된 파일은 변경하지 않는다. 기존 저장 보고서에 이력이 없으면 추가 칸이 없다.

## 다음 PR의 설계 결정이 필요한 영역

추가 HTTP가 필요한 경우에는 먼저 후보별 계획에 안전한 경로·메서드·최대 요청 수·시간·부작용·현재 승인 범위·근거 부족 사유를 고정하고, 그 계획 digest에 사용자 결정을 결속해야 한다. 최초 진단의 종료된 E② lease/session/권한을 재사용할 수 없다. 새 실행 참조 및 현재 대상 승인·제외 경로·설치 잠금·독립 예산·철회·감사·마감 계약을 충족하는 호스트 경로가 아직 없다. #927의 중단된 실행 정리와 경합하지 않도록 새 실행 생명주기 계약을 먼저 확정한다. 다른 회차의 시도 번호를 직접 비교하거나 기존 `passive/v2`의 동일 회차 검사를 완화하지 않는다. 이 조건을 구현·시험하기 전에는 승인/송신 API를 노출하지 않는다.

UI 담당자는 현재 `GET`으로 원본 자동 판정과 이력을 읽고, `POST`로 저장 자료만 재검토할 수 있다. 승인 버튼, 실행 진행/중지 UI와 최신 **현재 대상** 판정 집계는 후속 실행 계약과 서버 API가 구현된 뒤 연결한다. `#classify`·`#declaration` 또는 #918 이행점검 UI에 후보 검증을 섞지 않는다.
