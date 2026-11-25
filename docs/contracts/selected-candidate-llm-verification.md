# 비 IDOR 후보 선택 검증 계약

## 현재 구현 경계

`selected/llm-v1`은 새 일반 HTTP 진단의 종료 기록 버전이다. 최초 진단에서는 비 IDOR 후보를 유형별 규칙으로 확정하거나 부정하지 않는다. 후보와 E②의 비민감 완료 관찰을 저장하고, 각 후보를 `insufficient/selected_verification_required`로 남긴다. 실행이 정상 종료하지 않았거나 관찰 원천이 없으면 각각 `run_not_completed`, `source_unavailable`을 남긴다. 이 상태는 취약점이 없다는 뜻이 아니다.

기존 `passive/v1`, `passive/v2`, `candidate-active/v1` 종료 기록은 과거 판정과 근거를 읽기 위해 유지한다. 새 `selected/llm-v1` 회차에서 `stored/v1`, `active/v1`의 유형별 판정과 고정 두 GET 실행으로 들어갈 수 없다. E③ IDOR 판정은 이 계약의 대상이 아니다.

## 현재 API와 권한

`GET /api/product/tasks/{task}/candidates/{candidate}/verifications`는 작업 소유권과 원본 후보의 task·run·digest 결속을 확인한 뒤 자동 상태, 저장 관찰 여부 및 검증 이력을 반환한다. 조회는 대상 요청과 모델 호출을 만들지 않는다.

`POST` 같은 경로의 JSON은 `request_id`, `evidence_digest`, `method`를 받는다. `selected/llm-v1` 회차의 기본 `method`는 `llm/v1`이다. 원본 digest를 확인하고 `requested/selected_by_user` 사건을 추가한다. 같은 요청자는 같은 `request_id`를 반복해도 이력 하나만 받는다. 같은 후보의 다른 미완료 요청은 `409 verification_already_pending`, 다른 방법을 요구하면 `409 selected_verification_method_required`다. 이 요청만으로 모델 또는 대상 실행 권한은 발급되지 않는다.

`POST /api/product/tasks/{task}/candidates/{candidate}/verifications/{verification}/cancel`에 `{}`를 보내면 `requested` 상태의 같은 사용자 요청을 `cancelled/user_cancelled`로 전이한다. 같은 취소의 재시도는 멱등이다. 이후 다른 `request_id`로 해당 후보를 다시 선택할 수 있다. 이 취소는 실행 중인 모델이나 E② 작업을 중단하는 계약이 아니며, 이후 단계에서 별도로 구현한다.

이 단계에서 `requested` 이후의 모델 분석·질문·행동 승인·신규 E② 실행·최종 판정 전이는 **아직 연결되지 않았다**. UI는 이 상태를 완료나 확정으로 표시해서는 안 된다. 다음 구현에서 별도 계약과 회귀시험을 추가한다.

## 설치 모델과 비용 경계

선택 후보의 모델 단계는 해당 설치에 설정된 모델 프로필과 남은 설치 예산을 사용한다(사용자 결정, 2026-11-25). 후보 요청이나 승인 JSON으로 모델, 제공자, 비용 상한을 바꾸지 않는다. 새 실행은 별도 회차와 장부 항목을 가져야 하며, 원래 진단의 종료된 모델 예약이나 E② 권한을 이어받지 않는다. 설치 잔여 예산을 확인할 수 없으면 모델 호출 전에 중단한다. 후보 전용 시간·요청·비용 상한은 설치 상한의 하한으로 적용하고, 승인된 계획의 상한을 실행 중 늘리지 않는다.

이 절은 아직 구현되지 않은 실행 경계의 계약이다. 모의 모델 합성 시험에는 실제 유료 호출이 필요하지 않다. 실제 모델의 판정 품질·비용 측정은 별도 승인된 시험에서 수행한다.

## 근거와 보고서

새 회차의 관찰 projection은 `StoredObservationV2`의 revision 2 저장 봉투를 사용한다. 최종 E② 호출, 동일 작업·회차, 응답 및 감사 완료를 확인하고, 본문·쿠키·자격정보·원문 URL은 복제하지 않는다. `nosniff` 또는 `Server` 헤더가 있어야 저장하는 유형별 조건은 적용하지 않는다. 중복 호출·시도 참조가 있으면 부분 projection을 저장하지 않는다.

보호 실행기용 `ReadCandidateSelectedContext`는 접수 상태의 선택 요청과 원본 후보·종료 기록·저장 관찰을 한 PostgreSQL snapshot에서 읽고 원본 digest 및 관찰 digest를 재검사한다. 취소된 요청이나 손상된 관찰은 읽지 않는다. 이 함수는 요청자 권한 검사 및 실제 실행 직전 상태 검사 자체를 대신하지 않으며, 모델이나 E②를 호출하지 않는다.

`OriginalObservation`은 후보의 최초 `call_ref`와 저장된 E② 관찰의 시도·정책·회차 내용이 일치할 때만 제공한다. 원본 관찰이 없으면 `nil`이며, 후속 모델 단계가 다른 호출의 경로나 현재 대상의 응답을 원본인 것처럼 대체해서는 안 된다. 저장된 관찰에는 원문 URL과 본문이 없으므로, 원본 요청의 정확한 입력이 필요하지만 남아 있지 않은 후보는 추가 확인 없이 재현 성공으로 판정할 수 없다.

정상 완료 보고서의 HTML·DOCX에는 의심 후보와 `selected_verification_required`가 남고, 후보 선택 요청이 있으면 최근 처리 상태를 별도로 투영한다. 모델이 주장한 문장만으로 확정 수를 늘리지 않는다. 기존 보고서와 이행점검의 원본 근거는 수정하지 않는다.
