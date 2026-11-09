# 첫 진단 실행 Gateway v1

## 현재 결합과 이동 범위

기존 웹 LiveScreen은 일반 ARTEX의 activity/tokens/control/archive를 함께 호출하고,
ReportScreen은 일반 task/findings/coverage 조회에 결합돼 있었다.
첫 진단·저장 보고서 전용 서버의 허용 라우트와 다르므로 후속 UI 단계에서 별도 adapter로 연결한다.

기존 server.FirstDiagnosisSupervisor는 Manager·Engine·Planner·Worker와 E②를 조립한다.
cmd/first-controlled-diagnosis/serve.go의 startLaunch가 보호 파일·모델 profile·DB 역할·단회 권위를
검사하고 감독자를 만든다. 이 구현을 즉시 재작성하지 않고 독립 프로세스의 Runner 뒤로 감싼다.

- diagnosis: 표준 라이브러리만 사용하는 버전 명령·응답·서비스·pipe client.
- CLI gateway: 운영자 공급 launch config로 기존 엄격 기동을 재사용하는 실행 프로세스.
- 후속 API adapter: 사용자 인증·task 권한·초안 revision·운영자 승인 대응을 확인한 뒤 Gateway만 호출한다.
- 후속 프런트엔드: 공개 API adapter와 검증된 표시 모델만 사용하며 보호 경로·비밀을 받지 않는다.

현재 supervisor가 물리적으로 server 패키지에 남아 있으므로 기존 ARTEX 서버 전체의
agent 의존 제거까지 끝난 것은 아니다. 새 명령 경계의 호출자는 Planner/Worker 타입을 사용하지 않는다.

## 프로세스 실행 및 신뢰 경계

운영자가 사전에 다음과 같이 실행 프로세스를 구성한다.

    first-controlled-diagnosis gateway -config /operator/protected/launch.json -preparation-ref operator-preparation

config는 기존 CLI와 같은 보호 파일이다. 기존 단회 권위·task·profile을 검증한다.
이 초기 기동은 진단 실행이 아니며 모델/대상 실행은 start 명령을 기다린다.
기동이 권위 marker를 소비할 수 있으므로 같은 config를 다른 실행에 재사용하지 않는다.

stdin/stdout은 운영자가 만든 비공개 상속 pipe다. 공개 TCP 서비스나 브라우저 API가 아니다.
pipe에는 사용자 인증이 없으므로 네트워크에 그대로 노출하면 안 된다.
API 프로세스는 시작 시 전달받은 연결만 사용한다. HTTP 요청마다 subprocess를 만들지 않는다.

preparation_ref는 운영자 결속 식별자이지 권한 증명이나 단회 승인 파일이 아니다.
API의 사용자 권한과 정확한 초안 revision/digest→task/preparation 대응 확인을 대신하지 않는다.
클라이언트가 config 경로, shell 명령, URL, 모델 키, 승인 자료를 이 계약으로 전달할 수 없다.

기존 loopback 조회/E② 승인 HTTP는 실행 프로세스에서 호환용으로 유지된다.
새로운 공개 HTTP 시작 라우트는 이 CLI에 없다.
프로세스 종료 뒤에는 기존 report CLI가 저장 결과를 제공한다.

## 버전 계약

한 줄에 JSON 객체 하나, 최대 64 KiB. 알 수 없는 필드·중첩 중복 키·대소문자 별칭·null·추가 JSON을 거부한다.

요청 필드:

- version: diagnosis.v1
- request_id: 비민감 상관 참조
- command: validate, status, start, stop, result
- task_id: 실제 양수 int64 정본 문자열
- preparation_ref: 운영자 준비 참조
- idempotency_key: start에만 필수

validate는 이미 준비된 서비스의 결속 상태 조회다. 새 초안의 정책 승인·보호 자료를 생성하지 않는다.
초안 준비 검사는 별도 API의 운영자 승인 catalog에서 수행해야 한다.

응답은 version/request_id와 snapshot 또는 고정 오류 code 중 하나다.
snapshot은 task_id/preparation_ref/state/started/drain_complete/result_ready만 전달한다.
엔진 내부 객체·프롬프트·도구 입력·원문 오류·DB 필드를 노출하지 않는다.

## 실행 및 실패 의미

동일 준비 서비스는 한 번만 시작한다. 같은 idempotency key는 현재 상태만 반환하고,
다른 key의 재시작은 거부한다. 프로세스 재시작 뒤 단회성은 기존 보호 권위 검사가 집행한다.

stop은 진행 실행 context를 취소하고 기존 runtime의 admission 차단·drain·감사 마감을 따른다.
이미 보낸 요청이나 대상 변경을 되돌린다는 뜻이 아니다.
통신 EOF, 출력 실패, 프로세스 취소도 서비스 Shutdown으로 연결한다.
Unix stdout 단절은 SIGPIPE 즉시 종료 대신 Write 오류로 처리하여 마감을 시도한다.

- completed: 실행과 저장/자원 마감 확인. 목표 달성·취약점 없음과 다름.
- cancelled, failed: 실행 취소/실패와 실제 마감·결과 저장을 별도로 보존.
- unknown: 마감이나 저장 확인 불가. drain_complete와 result_ready를 독립적으로 확인.
- prepared, running, stopping: 실제 서비스 단계. 후보 판정과 별개.

Client는 명령을 한 번만 전송한다. 응답 불명·형식 오류·취소 시 연결을 폐기하며
start를 자동 재전송하지 않는다. 호출자는 검증된 재연결/상태 조회로 조정해야 한다.

## 증명 범위와 남은 일

독립 자식 프로세스에서 v1 validate/start/status/stop/result를 실행하는 회귀가 있다.
가짜 Runner 시험은 엔진 교체와 명령 경계의 독립성을 증명한다.
실제 CLI+PG16+loopback 모델+E② 수용은 별도
TestFirstDiagnosisGatewayIndependentProcessOwnedPostgres로 검증한다.
그 시험의 실행 여부와 결과를 PR에서 별도 보고한다.

이 계약만으로 다음을 완료했다고 주장하지 않는다.

- 공개 API의 초안 persistence·승인 catalog·사용자 권한·시작 endpoint 배포
- 새 초안을 실제 준비 task/intent/단회 권위로 승인해 주는 운영자 업무
- Planner/Worker 전체를 기존 server 패키지에서 물리적으로 이동
- 표준 이벤트 스트림 전체 구현과 모든 엔진 교체 수용
- 실모델 Qwen3.5 또는 외부 대상 수용

운영자 승인 자료가 없으면 초안/검증 표시까지만 허용해야 하며,
미연결 Gateway나 권위 대응을 성공 실행으로 표시하면 안 된다.
