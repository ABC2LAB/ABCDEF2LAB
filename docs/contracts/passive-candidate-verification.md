# 저장 후보의 수동적 독립 관찰 검증 (passive/v1)

## 범위와 출처

제품 E③가 소유권 기반 IDOR의 판정 주체다. 일반 후보의 유형 이름이나 모델 설명은 E③ 확정으로 승격하지 않는다. 제품 E③ 행은 기존 정책 원본·봉인 검증을 통과한 `ProductConfirmed`만 보고서에 싣고, 후보와의 명시적 연결이 없는 경우 임의로 합치지 않는다.

모델 단계의 `submit_diagnostic_candidate`는 실제 E② `call_ref`와, 이미 별도로 기록된 최대 8개의 `related_call_refs`를 저장한다. 선택 입력 `verification_claim`은 모델의 주장일 뿐이다. 종료 기록 저장 시 백엔드가 각 후보의 저장 증거 digest, 실제 호출·시도 참조, 완료·감사·응답 상태, 승인 규칙, 동일 기능과 유형별 기준을 다시 검사한다. 모델 설명, 도구 출력의 문서 예시, 같은 응답의 다른 사본은 독립 근거가 아니다. 이 경로는 새 대상 요청이나 모델 호출을 만들지 않는다.

현재 지원하는 정확한 `(vulnclass, verification_claim)` 쌍은 다음과 같다.

| 유형 | 확인하는 사실 | 독립 근거와 정상 대조 |
| --- | --- | --- |
| `information_disclosure`, `server_version_banner` | 실제 `Server` 응답 헤더가 제품 버전을 표시함 | 같은 승인 경로 패턴의 완료·감사된 서로 다른 GET 응답 2건. 문서 속 배너 예시는 제외하고, 버전 없는 실제 `Server` 헤더 2건은 부정 결과다. 특정 버전의 취약성은 확정하지 않는다. |
| `security_headers`, `html_nosniff_missing` | 실제 HTML 응답에 `X-Content-Type-Options: nosniff`가 없음 | 같은 승인 경로 패턴의 완료·감사된 서로 다른 GET 응답 2건. HTML이 아닌 응답은 근거가 아니며, `nosniff`가 있는 실제 HTML 응답 2건은 부정 결과다. |

실측 VAmPI 문서의 `_debug` 비밀 노출, 인증 없는 가입·관리자 조회·도서 목록은 보호값, 신원 또는 업무 정책에 대한 별도 근거가 부족하므로 이 버전에서 확정하지 않는다. XSS·SQLi·SSRF 등 모델이 제출할 수 있는 다른 자유 유형도 `type_not_supported`다. 대상 응답의 보호값 원문은 새 검증 기록에 복사하지 않는다.

## 판정과 API

두 호출의 `call_ref`와 감사 시도 참조가 달라야 하고, `GET`·HTTP 200·승인된 동일 규칙/경로 표현·응답 완료·감사 완료가 모두 필요하다. 같은 상태가 두 응답에서 일치할 때만 `confirmed` 또는 `negative`로 판정한다. 증거 없음·실행 실패·응답 불완전·상태 불일치는 `insufficient`로 남긴다. 미지원 유형은 `unsupported`다. 고정 `reason` 코드와 근거 digest/시도 참조를 저장한다.

새 회차의 `GET /api/tasks/{task_id}/diagnosis-run`에는 `verification`이 추가된다. `non_idor_confirmed`, `non_idor_negative`, `verification_pending`, `candidates[]`(후보 ID·증거 digest·상태·고정 사유·시도 참조)를 제공한다. `source_unavailable`이면 원천 후보를 읽지 못했다는 뜻이며 0건 확정이나 정상 판정으로 해석하지 않는다. 기존 저장 회차에는 이 칸이 없다.

새 회차의 `GET /api/report/diagnostic` HTML·DOCX는 E③ 확정과 검증된 비-IDOR 확정만 출력한다. 둘 다 0건이면 404로 취약점 보고서를 만들지 않는다. `format=review-json&audience=internal`은 기존 `entries`를 유지하면서 `candidate_verifications[]`를 추가한다. 후보 ID, 상태, 사유, 증거 digest, 비밀 없는 관찰 요약으로 미확정 결과를 확인한다. 원본 후보·E③ 증거는 저장소에 그대로 남으며 이행점검에서 사용하는 내부 보고서 스냅샷을 지우지 않는다. 이 규칙은 새 `passive/v1` 종료 기록에만 적용하므로 이전 보고서 출력은 유지된다.

## 한계와 측정

독립 관찰은 후보 제출 시 이미 보유한 다른 E② 호출에서 가져온다. 후보 저장 *이후* 새 관찰을 자동 수집하는 경로는 아직 없다. 별도 호출이 없으면 확정하지 않는다. 버전 배너와 헤더 설정은 관찰된 구성 사실을 확인하는 것이며 공격 성공이나 영향도를 입증하지 않는다. 모델은 후보 단계에서 정상 설명과 참조를 고르지만 검증 전용 두 번째 LLM 호출은 없다. 설치에 저장된 사용자 지정 Worker 프롬프트는 소스 기본 프롬프트 수정만으로 교체되지 않아 새 `verification_claim` 제출 여부를 실제 회차에서 확인해야 한다. 실제 OpenRouter 호출·실제 VAmPI 재실측은 수행하지 않았으며, 합성 근거와 모의 모델 권고만 시험했다.
