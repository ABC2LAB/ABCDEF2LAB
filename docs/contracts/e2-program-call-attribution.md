# E② 프로그램 인증 호출 귀속

#611의 인증 집계 분리 계약이다. 대상·IP·TLS·요청 승인·쓰기 범위나 실행 예산을 발급하거나 넓히지 않는다.

`BeginCall`과 `BeginCallForIntent`는 등록된 양수 Worker intent만 받는다. 호스트의 인증 준비와 재로그인은
`BeginProgramCall`을 사용한다. 이 API는 고정 종류 `auth_prep`·`refresh_session`·`login_contrast`(모델 로그인의 프로그램 대조 요청: GET만, 본문·인증 헤더 없이, 진단 흐름 경계·요청 승인 없이 승인 규칙 범위·제외 경로·예산만, [모델 로그인 시도 기록 계약](model-login-attempts.md) §4)만 받고 `AuthPrep` 전송 API만 쓴다.
Worker 호출에서 인증 전용 전송을 하거나 인증 호출(`auth_prep`·`refresh_session`)에서 Worker `Do`를 쓰는 것은 구성 오류다. `login_contrast`는 `Do`로 보내고 E²가 GET·본문 없음·인증 헤더 없음을 강제한다.

귀속은 Call에 고정돼 영수증·감사 행·CallResult에 `program`으로 함께 전달된다. Worker는 양수 `intent_id`와 빈
`program`, 프로그램은 명시적인 종류와 Worker intent 없음으로 표현한다. 기존 숫자 칸의 0은 이 경우에만 intent 부재를
나타낸다. 0을 `BeginCallForIntent`에 넣어 검사를 우회하거나 가짜 intent를 등록하지 않는다. 두 귀속이 섞이거나 종류가
없거나 알려지지 않으면 검증기는 거부한다. 실행 전체 제어 행의 기존 `intent_id: 0` 의미는 그대로다.

`ExecutionView.Counts`와 기존 호출·시도·요청 예산은 모든 호출을 센다. `IntentCounts`는 지정 Worker intent만,
`ProgramCounts`는 인증 프로그램 호출만 센다. Call 목록의 `program`으로 최초 인증과 재로그인도 나눌 수 있다.
전체 호출 수는 프로그램 호출 수와 Worker 호출 수의 합이다. 인증 결과 불명은 프로그램 누계·감사·계정 인증 결과에
남고 Worker intent의 결과 불명에 더하지 않는다. 결과 불명 요청을 자동 재전송하는 경로는 없다.

감독자는 호스트가 등록한 ProgramCalls와 실제 Call의 프로그램 귀속을 모두 대조하고, 최종·감사 완료와 Worker 신뢰 결과가
붙지 않았음을 확인한다. 등록만 있고 Call이 없거나, Call만 프로그램이라고 주장하거나, 열린 프로그램 호출이면 마감하지 않는다.
Worker 결과 기록기·exporter·감독자의 Worker 결과 판정은 프로그램 Call을 Worker 근거로 받지 않는다.

새 칸은 `e2-http-v2`의 선택적 귀속 확장이다. 프로그램 칸이 없는 기존 Worker 감사 바이트·정책 지문은 바뀌지 않는다.
v1 기록에는 프로그램 귀속을 붙일 수 없다. Go 감사 검증기와 Python 평가 파서가 같은 배타 조건을 검사하고, Python 파서는
시도 내 귀속 일치도 검사한다. 원격 경로는 같은 버전의 backend가 만든 Call·누계·ProgramCalls를 전달한다. 새 필드를 버린 혼합 버전은 프로그램
대조를 완료로 간주하지 않는다. 이 정보는 E② 실행 귀속이며 #596의 E③ 증거 영수증이나 인증 성공 증명으로 사용하지 않는다.
