# 비 IDOR 후보 선택 검증 계약

## 현재 구현 경계

`selected/llm-v1`은 새 일반 HTTP 진단의 종료 기록 버전이다. 최초 진단에서는 비 IDOR 후보를 유형별 규칙으로 확정하거나 부정하지 않는다. 후보와 E②의 비민감 완료 관찰을 저장하고, 각 후보를 `insufficient/selected_verification_required`로 남긴다. 실행이 정상 종료하지 않았거나 관찰 원천이 없으면 각각 `run_not_completed`, `source_unavailable`을 남긴다. 이 상태는 취약점이 없다는 뜻이 아니다.

기존 `passive/v1`, `passive/v2`, `candidate-active/v1` 종료 기록은 과거 판정과 근거를 읽기 위해 유지한다. 새 `selected/llm-v1` 회차에서 `stored/v1`, `active/v1`의 유형별 판정과 고정 두 GET 실행으로 들어갈 수 없다. E③ IDOR 판정은 이 계약의 대상이 아니다.

과거 `passive/v2` 회차에서 새 `active/v1` 실행을 계획할 때도 원본 GET 관찰의 인증 범주가 명시적으로 `none`이고 계정 참조가 비어 있어야 한다. `active/v1`에는 계정 결속·재로그인 단계가 없으므로 인증된 원본, 인증 범주가 `unknown`인 원본, 이 필드가 비어 있는 옛 원본을 익명 후속 GET과 비교하지 않는다. 기존 이력 조회는 계속 허용한다.

## 현재 API와 권한

`GET /api/product/tasks/{task}/candidates/{candidate}/verifications`는 작업 소유권과 원본 후보의 task·run·digest 결속을 확인한 뒤 자동 상태, 저장 관찰 여부 및 검증 이력을 반환한다. 조회는 대상 요청과 모델 호출을 만들지 않는다.

`POST` 같은 경로의 JSON은 `request_id`, `evidence_digest`, `method`를 받는다. `selected/llm-v1` 회차의 기본 `method`는 `llm/v1`이다. 원본 digest를 확인하고 `requested/selected_by_user` 사건을 추가한다. 같은 요청자는 같은 `request_id`를 반복해도 이력 하나만 받는다. 같은 후보의 다른 미완료 요청은 `409 verification_already_pending`, 다른 방법을 요구하면 `409 selected_verification_method_required`다. 요청 커밋 뒤 보호 준비 서비스에 분석 작업을 예약한다. 분석은 대상 HTTP 권한을 발급하지 않는다. 예약 실패 응답 503 뒤에도 durable `requested` 사건은 남으며 복구기가 다시 인수한다.

`POST /api/product/tasks/{task}/candidates/{candidate}/verifications/{verification}/cancel`에 `{}`를 보내면 `requested`, `checking_stored_evidence`, `analysis_ready`, 저장 관찰 평가 중 `assessing` 상태의 같은 사용자 요청을 `cancelled/user_cancelled`로 전이한다. 저장 관찰 평가가 이미 시작된 경우 보호 실행기의 context도 취소한다. 이미 공급자에게 전달된 호출은 취소 시점에 따라 과금될 수 있으며 재시도하지 않는다. 취소와 모델 결과 저장이 경쟁하면 최신 사건 잠금으로 늦은 결과 저장을 거부한다. 같은 취소는 멱등이다. 분석 준비 중 설치 예산이 없으면 송신 없이 `failed/budget_unavailable`로 기록한다.

분석은 `requested → checking_stored_evidence → analysis_ready` 순서로 기록한다. 분석 중 장애·재시작은 `failed/analysis_unavailable` 또는 `failed/analysis_interrupted`, 시간 상한 도달은 `failed/analysis_timeout`이 되며, 미확인 호출을 자동 재송신하지 않는다. `GET` 이력의 `analysis`는 모델 제안·digest·사용량을 제공하고 `business_answers`는 질문 번호와 사용자 답변을 제공한다. `analysis_ready`는 취약점 판정이 아니다.

`POST /api/product/tasks/{task}/candidates/{candidate}/verifications/{verification}/answers`는 `{"analysis_digest":"<sha256>","question_index":1,"answer":"<업무 진술>"}`을 받는다. 질문 번호는 분석의 `business_questions` 배열에서 1부터 시작한다. 작업 소유권·후보·분석 판본을 검사하고, 답변은 DLP 처리 후 immutable 행으로 저장한다. 같은 답변 재전송은 200, 다른 답변은 `409 business_answer_conflict`, 변경된 분석 판본은 `409 analysis_changed`, 분석 전 입력은 `409 analysis_not_ready`다. 이 API는 모델·대상 요청을 만들지 않으며 답변 자체는 행동 승인으로 해석하지 않는다.

## 설치 모델과 비용 경계

선택 후보의 모델 단계는 해당 설치에 설정된 모델 프로필과 남은 설치 예산을 사용한다(사용자 결정, 2026-11-25). 후보 요청이나 승인 JSON으로 모델, 제공자, 비용 상한을 바꾸지 않는다. 새 실행은 별도 회차와 장부 항목을 가져야 하며, 원래 진단의 종료된 모델 예약이나 E② 권한을 이어받지 않는다. 설치 잔여 예산을 확인할 수 없으면 모델 호출 전에 중단한다. 후보 전용 시간·요청·비용 상한은 설치 상한의 하한으로 적용하고, 승인된 계획의 상한을 실행 중 늘리지 않는다.

선택 분석은 보호 준비 서비스가 일반 진단 `Start`와 같은 잠금에서 설치 위임의 `MaxRuns`와 집계 예산을 확인하고, 선택 요청 하나에 설치 회차 예산 전부를 영속 예약한 뒤에만 시작한다. 예약 행은 취소·미확인 결과에도 환불하지 않으며 재시작 뒤 일반 진단에도 계속 합산한다. 설치된 OpenRouter v4는 선택 요청 고유 run ledger를 사용하고, 로컬 모델은 기존 `FirstDiagnosisModelBudget`을 사용한다. 분석 모델 호출은 한 번, 재시도 없음, 180초 상한이다. 최종 모델 단계도 한 번만 호출하며 남은 모델 요청·토큰·비용 한도를 재검사한다. 승인된 계획은 최대 3개의 구조화된 GET/HEAD 행동과, 인증 시 승인 범위 안의 로그인 POST 1건으로 제한된다. 실행 상한은 90초다. 실제 유료 OpenRouter 호출은 이 작업의 합성 시험에서 수행하지 않았다.

## 근거와 보고서

새 회차의 관찰 projection은 `StoredObservationV2`의 revision 2 저장 봉투를 사용한다. 2026-11-27부터 완결 응답의 구조 근거(`response_evidence`)를 같은 행에 선택적으로 결속하고, 최종 판정은 그 근거로 응답 근거 게이트를 거친다. 형식·결속·게이트·호환은 [응답 구조 근거 계약](selected-candidate-response-evidence.md)을 따른다. 최종 E② 호출, 동일 작업·회차, 응답 및 감사 완료를 확인하고, 본문·쿠키·자격정보·원문 URL은 복제하지 않는다. `nosniff` 또는 `Server` 헤더가 있어야 저장하는 유형별 조건은 적용하지 않는다. 선택 회차의 일반 후보도 원본 호출의 Host 체크포인트를 저장하며, 증빙이 4096바이트 상한을 넘으면 증빙 없는 일반 관찰만 저장해 검증 가능 여부를 보수적으로 낮춘다. 중복 호출·시도 참조가 있으면 부분 projection을 저장하지 않는다.

보호 실행기용 `ReadCandidateSelectedContext`는 접수 상태의 선택 요청과 원본 후보·종료 기록·저장 관찰을 한 PostgreSQL snapshot에서 읽고 원본 digest 및 관찰 digest를 재검사한다. 취소된 요청이나 손상된 관찰은 읽지 않는다. 이 함수는 요청자 권한 검사 및 실제 실행 직전 상태 검사 자체를 대신하지 않으며, 모델이나 E②를 호출하지 않는다.

`OriginalObservation`은 후보의 최초 `call_ref`와 저장된 E② 관찰의 시도·정책·회차 내용이 일치할 때만 제공한다. 원본 관찰이 없으면 `nil`이며, 후속 모델 단계가 다른 호출의 경로나 현재 대상의 응답을 원본인 것처럼 대체해서는 안 된다. 저장된 관찰에는 원문 URL과 본문이 없으므로, 원본 요청의 정확한 입력이 필요하지만 남아 있지 않은 후보는 추가 확인 없이 재현 성공으로 판정할 수 없다.

`BuildCandidateAnalysisRequest`는 검증된 선택 문맥을 도구 없는 모델 입력으로 제한하고 비밀 형식을 다시 가린다. `ParseCandidateAnalysisProposal`은 후보 ID와 실제 제시한 `call_ref`를 확인하고, 모델이 이 단계에서 `verdict`를 추가하면 거부하며, 반환 설명·질문·행동 문구에도 DLP를 적용한다. 보호 실행기는 설치 모델 어댑터로 이를 한 번 호출하고 검증된 제안을 영속 저장한다. 업무 질문의 답변은 별도 출처로 저장한다. 행동 제안은 별도 승인 전까지 송신 권한이 아니다.

정상 완료 보고서의 HTML·DOCX에는 의심 후보와 `selected_verification_required`가 남고, 후보 선택 요청이 있으면 최근 처리 상태를 별도로 투영한다. 모델 권고 `confirmed`/`negative`는 저장 내부값일 뿐이다. 공개 API와 보고서에는 각각 `model_supported`(관찰 확인됨·모델 판정), `model_refuted`(아님·모델 판정)로 표시하고 프로그램/E③의 `confirmed`/`negative`와 별도 집계한다. 모델 권고만으로 프로그램 확정 수나 확정 심각도 집계를 늘리지 않는다. 기존 보고서와 이행점검의 원본 근거는 수정하지 않는다.

`llm/v1` 선택 요청 직후와 분석·취소 뒤에도 내부용·공유용 HTML 및 DOCX 보고서는 계속 발행한다. 선택만 한 후보의 `insufficient/selected_verification_required`는 아직 검증을 시도해 판단 불가가 된 결과가 아니므로 보고서 요약의 **의심** 수에 포함한다. 선택 요청의 처리 상태는 후보 판정과 별도로 표시한다. 분석 질문과 답변은 내부용 보고서에 표시하고 공유용에서는 가린다. 완료된 최종 검증은 별도 이력에 보존하고 최신 보고서에서만 현재 판정으로 투영한다. 확정·부정은 원본 관찰과 별도 적격 E② 관찰의 비민감 출처를 모두 인용해야 한다. 모델이 출처를 잘못 가리키거나 원본 관찰이 없으면 확정·부정을 거부한다.

분석이 준비되면 보호 실행기는 먼저 같은 종료 회차에 영속 저장된 관찰을 확인한다. 후보 저장 fence 이후의 독립 호출·시도이며 동일 대상·정책·정확한 기능·메서드와 Host 봉인이 맞는 관찰이 있으면 `analysis_ready → assessing/stored_final_model_started → completed/stored_model_evidence_checked`로 진행한다. 이 경로에는 새 대상 송신, 추가 실행 계획, 행동 승인이 없다. 최종 모델은 설치 모델과 해당 후보에 이미 예약한 잔여 예산으로 한 번만 호출한다. 결과는 모델의 출처 참조와 원본·비교 관찰의 무결성을 프로그램으로 다시 확인한 뒤 저장한다. 서비스 재시작 시 `analysis_ready`는 다시 예약할 수 있지만 이미 시작한 `assessing` 호출은 결과가 불명확하므로 재전송하지 않고 `failed/final_model_interrupted`로 기록한다. 저장 관찰이 부족하면 `analysis_ready`를 유지하며 별도 계획·승인 경로를 사용한다.

## 행동 승인부터 최종 판정까지의 API

원본과 후속 요청은 실제 송신 헤더를 기준으로 `none`, `authorization`, `cookie`, `authorization_and_cookie`, 등록된 `custom`, 또는 `unknown` 범주로 기록한다. 보호 세션 저장소의 현재 시험 계정 세션과 실제 송신 자격값이 정확히 하나 일치하면, 계정 라벨과 자격 파일 판본을 비밀 없는 `request_account_ref`로 결속한다. 자격 파일에는 난수가 들어 있으므로 이 참조에서 자격값을 복원할 수 없다. 새 세션을 발급받아도 같은 계정·자격 판본이면 참조가 유지된다. 다른 계정, 변경된 자격 판본, 미등록 자격 헤더, 모호한 일치, 옛 기록의 빈 참조는 결정적인 모델 판정을 허용하지 않는다. 이 참조는 **시험 계정 자격 및 세션의 연속성**을 입증하며 대상 내부 주체 ID를 독립적으로 증명하지는 않는다. 그 구별이 필요한 주장에는 추가 신원 근거가 없으면 `insufficient`로 남겨야 한다. 자격값과 쿠키 원문은 저장 관찰·선택 검증 분석/판정 모델 입력·보고서에 복제하지 않는다. 최초 진단 모델의 시험 계정 세션 전달은 기존 실행 동의 정책을 따른다.

후속 계획은 원본의 봉인된 관찰과 정확히 비교 가능한 재현 행동 하나 이상을 요구한다. 로그인 준비가 필요하면 경로·메서드·토큰 추출 설정을 포함한 로그인 계약 digest를 계획·승인에 묶는다. 최초 일반 진단 scope의 메서드·경로 목록은 읽기 규칙이며 별도 `auth_prep` 로그인 POST를 포함하지 않는다. 따라서 후속 로그인은 그 일반 읽기 목록에 억지로 포함시키지 않고, **현재 대상 승인**의 로그인 경로·POST 허용·제외 경로 및 새 E② `auth_prep` 규칙으로 검사한다. 실행은 현재 승인·작업 소유권·계정 판본을 재검사한 뒤 새 task/run과 새 E② 권한에서 해당 계정 하나만 로그인한다. 그 실행의 비밀번호 응답 정책은 일반 제품 진단과 같은 `model_visible_v1`이다([비밀번호 반사 응답 보호](password-response-protection.md), 2026-11-26 DLP 폐기). 그래서 짧은 비밀번호 계정도 로그인하고, 대상이 비밀번호를 비춘 응답도 보류·가림 없이 완료 관찰로 남는다. 저장 관찰에는 응답 본문이 없으므로 이 정책이 선택 검증 모델 입력에 비밀번호를 더하지는 않는다. 우리 제어 비밀이 비친 응답의 보류와 E③ 표식 가림은 그대로다. 선택 검증 분석·판정 모델은 계정 세션 원문을 받지 않으며 E③ 발급·판정을 사용하지 않는다. 로그인 실패나 세션 불일치는 송신 성공·판정 성공으로 바꾸지 않는다.

각 행동의 Query는 최대 4개의 짧은 비자격 키·값, 일반 헤더는 `Accept`, `Accept-Language`, `X-Requested-With`의 제한된 값만 허용한다. 계획 digest와 E② exact rule은 같은 canonical query와 헤더를 묶고 추가 헤더를 거부한다. 관찰은 실제 송신 조건의 digest와 제한된 응답 미디어 범주만 저장하며 원문 Query·헤더 값·본문은 저장하지 않는다. `normal_control`은 별도 대조 행동으로 기록된다. 결정적 모델 권고에는 원본과 같은 기능·메서드·입력 digest를 가진 재현 관찰이 반드시 하나 이상 필요하다. 대조 행동은 재현 관찰을 대체하지 않는다. 현재 Body, 쓰기 메서드, 동적 입력, 복구 자동화 및 임의 응답 본문 비교는 안전한 계획·복구 계약이 없어 송신하지 않는다. 이런 행동 제안의 계획 요청은 `409 candidate_action_method_unsupported`를 반환하고 후보를 확정하지 않는다.

다음 경로는 제품 관리 호스트의 기존 Bearer 인증과 원본 작업 소유권을 사용한다. 모든 본문은 `application/json`; 오류는 `{"error":"<고정 사유 코드>"}`다. `GET`은 저장 자료만 읽고 대상·모델 송신을 만들지 않는다.

| 단계 | HTTP | 요청 | 성공 응답·이력 위치 |
|---|---|---|---|
| 후보 선택 | `POST /api/product/tasks/{task}/candidates/{candidate}/verifications` | `{"request_id":"synthetic-request-001","evidence_digest":"<sha256>","method":"llm/v1"}` | 200; `verifications[].id`, `latest.process_state=requested` |
| 분석·이력 조회 | 같은 경로 `GET` | 본문 없음; `?before={verification_id}` 선택 | 200; `analysis.proposal`, `analysis.digest`, `business_answers`, `latest`, `final_verdict` |
| 업무 답변 | `POST .../verifications/{verification}/answers` | `{"analysis_digest":"<sha256>","question_index":1,"answer":"합성 업무 답변"}` | 200; 동일 답변 멱등, 다른 답변 409 |
| 실행 계획 | `POST .../verifications/{verification}/plan` | `{"analysis_digest":"<sha256>"}` | 저장 관찰이 부족한 경우 200; `plan`, `plan_digest`, `process_state=awaiting_approval`. 적격 저장 관찰 평가가 가능한 경우 409 `stored_evidence_assessment_pending`이며 이력에서 `assessing` 또는 최종 상태를 조회한다. |
| 결정 | `POST .../verifications/{verification}/decision` | `{"plan_digest":"<sha256>","decision":"approved"}` 또는 `rejected` | 200; `approval_ref`, `execution_start=scheduled/unavailable/not_requested` |
| 취소 | `POST .../verifications/{verification}/cancel` | 승인 전 `{}`, 승인 후 `{"plan_digest":"<sha256>"}` | 200; `process_state`. 실제 송신과 경합할 수 있어 이력을 다시 조회한다. |
| 최신 보고서 | `GET /api/report/diagnostic?task={task}&audience=internal` | `&format=docx` 선택 | 완료된 최신 판정, 검증 설명·근거, 의심/확정/판단 불가/부정 집계 |

처리 상태와 취약점 판정은 다른 칸이다. `latest.process_state`는 `requested`, `checking_stored_evidence`, `analysis_ready`, `awaiting_approval`, `approved`, `rejected`, `running`, `evidence_ready`, `assessing`, `completed`, `failed`, `cancelled`, `expired` 중 하나다. 선택 LLM 이력의 `latest.verdict`와 `final_verdict.verdict`는 완료된 결과에서만 `model_supported`, `model_refuted`, `insufficient`다. 프로그램 판정의 `confirmed`/`negative`와 합치지 않는다. `analysis_ready`나 `approved`를 확정으로 표시하지 않는다. `analysis`, `llm_plan`, `final_verdict`, `business_answers`는 해당 단계의 저장 자료가 없으면 생략된다. `latest.sources`의 출처 참조는 원본·후속 증거의 식별과 digest이며 응답 본문이 아니다.

[합성 UI fixture](fixtures/selected-candidate-llm-verification.json)는 상태별 핵심 필드만 담은 부분 응답 예시다. 실제 이력 응답에는 원본 후보 digest·시각·자동 판정·저장 근거 상태 등이 추가되며, UI는 실제 API 응답의 생략 조건을 따른다.

별도 대상 실행이 필요한 경우 원본 task와 다른 task/run, 승인 사건, 정확한 계획 digest, 발급된 E② scope digest에 결속한다. 송신 직전에 원본 작업의 현재 인가·범위 및 대상 승인을 다시 읽고, 계획의 exact GET/HEAD 경로·Query·허용 헤더와 필요한 인증 준비 POST만 허용한다. 판정은 닫힌 실행의 감사 완료·봉인 확인·영속 관찰을 재검사한 뒤 저장하며 이력과 보고서 재조회에서도 재검사한다. 최종 모델 시간 상한 도달은 `expired/final_model_timeout`으로 구분한다. 원본 후보·최초 자동 판정·종료 기록은 수정하지 않는다.

일반 E② 결과도 감사 Host의 현재 v3 저널에서 `call_ref`·`attempt_id`·`sequence`에 맞는 완료 사건을 조회해 체크포인트를 발급받는다. 후속 관찰마다 사건의 작업·회차·정책·메서드·상태를 Runner의 비민감 관찰과 대조한다. Runner의 영수증 시각은 저널 기록 전에 정해지고 Host 사건 시각은 기록 때 정해지므로, 사건 시각이 영수증 시각 이후 15초 이내인지 확인한다. 두 시각의 일치를 요구하지 않는다. 저장·재조회 시 같은 체크포인트의 sequence와 MAC이 마감 봉인에 포함됐는지 확인한다. 체크포인트가 없거나 다른 사건·저널·봉인에 속하면 근거를 수락하지 않는다. Host 체크포인트와 MAC은 모델 입력에 넣지 않는다. 확정·부정에는 원본과 후속의 서로 다른 실제 호출이 필요하고, 메서드·정확한 기능 경로가 다르면 판단 보류만 가능하다.

현재 안전 경계: 인증된 원본은 보호 세션 저장소가 한 시험 계정에 결속한 판본과 승인된 로그인 준비가 있을 때만 새 실행을 계획한다. 결속이 없는 옛 기록, 알 수 없는 인증 방식 또는 정확히 재현할 수 없는 입력은 판단 불가로 유지한다. 활성 E③ 선언이 있어도 별도 비 IDOR 후속 task에 [후보 전용 권위](selected-candidate-e3-isolation.md)를 발급하며, E③ 선언 파일·규칙·판정은 발급하지 않는다. 원본의 신뢰 가능한 관찰이 없어도 후속 검증의 `insufficient`는 남길 수 있지만 확정·부정은 허용하지 않는다. 과거 시점 HTML·DOCX 버전 선택은 제공하지 않고, 다운로드 당시 파일과 검증 이력을 보존한다.
