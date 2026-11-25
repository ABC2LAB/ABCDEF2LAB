# 비 IDOR 후보 선택 검증 계약

## 현재 구현 경계

`selected/llm-v1`은 새 일반 HTTP 진단의 종료 기록 버전이다. 최초 진단에서는 비 IDOR 후보를 유형별 규칙으로 확정하거나 부정하지 않는다. 후보와 E②의 비민감 완료 관찰을 저장하고, 각 후보를 `insufficient/selected_verification_required`로 남긴다. 실행이 정상 종료하지 않았거나 관찰 원천이 없으면 각각 `run_not_completed`, `source_unavailable`을 남긴다. 이 상태는 취약점이 없다는 뜻이 아니다.

기존 `passive/v1`, `passive/v2`, `candidate-active/v1` 종료 기록은 과거 판정과 근거를 읽기 위해 유지한다. 새 `selected/llm-v1` 회차에서 `stored/v1`, `active/v1`의 유형별 판정과 고정 두 GET 실행으로 들어갈 수 없다. E③ IDOR 판정은 이 계약의 대상이 아니다.

## 현재 API와 권한

`GET /api/product/tasks/{task}/candidates/{candidate}/verifications`는 작업 소유권과 원본 후보의 task·run·digest 결속을 확인한 뒤 자동 상태, 저장 관찰 여부 및 검증 이력을 반환한다. 조회는 대상 요청과 모델 호출을 만들지 않는다.

`POST` 같은 경로의 JSON은 `request_id`, `evidence_digest`, `method`를 받는다. `selected/llm-v1` 회차의 기본 `method`는 `llm/v1`이다. 원본 digest를 확인하고 `requested/selected_by_user` 사건을 추가한다. 같은 요청자는 같은 `request_id`를 반복해도 이력 하나만 받는다. 같은 후보의 다른 미완료 요청은 `409 verification_already_pending`, 다른 방법을 요구하면 `409 selected_verification_method_required`다. 요청 커밋 뒤 보호 준비 서비스에 분석 작업을 예약한다. 분석은 대상 HTTP 권한을 발급하지 않는다. 예약 실패 응답 503 뒤에도 durable `requested` 사건은 남으며 복구기가 다시 인수한다.

`POST /api/product/tasks/{task}/candidates/{candidate}/verifications/{verification}/cancel`에 `{}`를 보내면 `requested`, `checking_stored_evidence`, `analysis_ready` 상태의 같은 사용자 요청을 `cancelled/user_cancelled`로 전이한다. 실행 중 분석이면 보호 실행기의 context도 취소한다. 이미 공급자에게 전달된 호출은 취소 시점에 따라 과금될 수 있으며 재시도하지 않는다. 취소와 모델 결과 저장이 경쟁하면 최신 사건 잠금으로 늦은 결과 저장을 거부한다. 같은 취소는 멱등이다. 분석 준비 중 설치 예산이 없으면 송신 없이 `failed/budget_unavailable`로 기록한다.

분석은 `requested → checking_stored_evidence → analysis_ready` 순서로 기록한다. 분석 중 장애·재시작은 `failed/analysis_unavailable` 또는 `failed/analysis_interrupted`가 되며, 미확인 호출을 자동 재송신하지 않는다. `GET` 이력의 `analysis`는 모델 제안·digest·사용량을 제공하고 `business_answers`는 질문 번호와 사용자 답변을 제공한다. `analysis_ready`는 취약점 판정이 아니다. 행동 승인·신규 E② 실행·최종 판정은 아직 연결되지 않았다.

`POST /api/product/tasks/{task}/candidates/{candidate}/verifications/{verification}/answers`는 `{"analysis_digest":"<sha256>","question_index":1,"answer":"<업무 진술>"}`을 받는다. 질문 번호는 분석의 `business_questions` 배열에서 1부터 시작한다. 작업 소유권·후보·분석 판본을 검사하고, 답변은 DLP 처리 후 immutable 행으로 저장한다. 같은 답변 재전송은 200, 다른 답변은 `409 business_answer_conflict`, 변경된 분석 판본은 `409 analysis_changed`, 분석 전 입력은 `409 analysis_not_ready`다. 이 API는 모델·대상 요청을 만들지 않으며 답변 자체는 행동 승인으로 해석하지 않는다.

## 설치 모델과 비용 경계

선택 후보의 모델 단계는 해당 설치에 설정된 모델 프로필과 남은 설치 예산을 사용한다(사용자 결정, 2026-11-25). 후보 요청이나 승인 JSON으로 모델, 제공자, 비용 상한을 바꾸지 않는다. 새 실행은 별도 회차와 장부 항목을 가져야 하며, 원래 진단의 종료된 모델 예약이나 E② 권한을 이어받지 않는다. 설치 잔여 예산을 확인할 수 없으면 모델 호출 전에 중단한다. 후보 전용 시간·요청·비용 상한은 설치 상한의 하한으로 적용하고, 승인된 계획의 상한을 실행 중 늘리지 않는다.

선택 분석은 보호 준비 서비스가 일반 진단 `Start`와 같은 잠금에서 설치 위임의 `MaxRuns`와 집계 예산을 확인하고, 선택 요청 하나에 설치 회차 예산 전부를 영속 예약한 뒤에만 시작한다. 예약 행은 취소·미확인 결과에도 환불하지 않으며 재시작 뒤 일반 진단에도 계속 합산한다. 설치된 OpenRouter v4는 선택 요청 고유 run ledger를 사용하고, 로컬 모델은 기존 `FirstDiagnosisModelBudget`을 사용한다. 분석 모델 호출은 한 번, 재시도 없음, 180초 상한이다. 실제 유료 OpenRouter 호출은 이 작업의 합성 시험에서 수행하지 않았다. 이후 행동·최종 판정 단계는 아직 구현되지 않았다.

## 근거와 보고서

새 회차의 관찰 projection은 `StoredObservationV2`의 revision 2 저장 봉투를 사용한다. 최종 E② 호출, 동일 작업·회차, 응답 및 감사 완료를 확인하고, 본문·쿠키·자격정보·원문 URL은 복제하지 않는다. `nosniff` 또는 `Server` 헤더가 있어야 저장하는 유형별 조건은 적용하지 않는다. 중복 호출·시도 참조가 있으면 부분 projection을 저장하지 않는다.

보호 실행기용 `ReadCandidateSelectedContext`는 접수 상태의 선택 요청과 원본 후보·종료 기록·저장 관찰을 한 PostgreSQL snapshot에서 읽고 원본 digest 및 관찰 digest를 재검사한다. 취소된 요청이나 손상된 관찰은 읽지 않는다. 이 함수는 요청자 권한 검사 및 실제 실행 직전 상태 검사 자체를 대신하지 않으며, 모델이나 E②를 호출하지 않는다.

`OriginalObservation`은 후보의 최초 `call_ref`와 저장된 E② 관찰의 시도·정책·회차 내용이 일치할 때만 제공한다. 원본 관찰이 없으면 `nil`이며, 후속 모델 단계가 다른 호출의 경로나 현재 대상의 응답을 원본인 것처럼 대체해서는 안 된다. 저장된 관찰에는 원문 URL과 본문이 없으므로, 원본 요청의 정확한 입력이 필요하지만 남아 있지 않은 후보는 추가 확인 없이 재현 성공으로 판정할 수 없다.

`BuildCandidateAnalysisRequest`는 검증된 선택 문맥을 도구 없는 모델 입력으로 제한하고 비밀 형식을 다시 가린다. `ParseCandidateAnalysisProposal`은 후보 ID와 실제 제시한 `call_ref`를 확인하고, 모델이 이 단계에서 `verdict`를 추가하면 거부하며, 반환 설명·질문·행동 문구에도 DLP를 적용한다. 보호 실행기는 설치 모델 어댑터로 이를 한 번 호출하고 검증된 제안을 영속 저장한다. 업무 질문의 답변은 별도 출처로 저장한다. 제안된 행동은 아직 승인·실행 이력이 아니다.

정상 완료 보고서의 HTML·DOCX에는 의심 후보와 `selected_verification_required`가 남고, 후보 선택 요청이 있으면 최근 처리 상태를 별도로 투영한다. 모델이 주장한 문장만으로 확정 수를 늘리지 않는다. 기존 보고서와 이행점검의 원본 근거는 수정하지 않는다.

`llm/v1` 선택 요청 직후와 분석·취소 뒤에도 내부용·공유용 HTML 및 DOCX 보고서는 계속 발행한다. 선택만 한 후보의 `insufficient/selected_verification_required`는 아직 검증을 시도해 판단 불가가 된 결과가 아니므로 보고서 요약의 **의심** 수에 포함한다. 선택 요청의 처리 상태는 후보 판정과 별도로 표시한다. 분석 질문과 답변은 내부용 보고서에 표시하고 공유용에서는 가린다. 현재 최종 검증 판정이나 신규 대상 송신은 없으며, 이 상태로 `confirmed`·`negative`를 출력하면 보고서 출력이 차단된다.
