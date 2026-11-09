# 진단 관리 API와 운영자 실행 결속 v1

## 구현 범위와 책임

- diagnosisapi/: HTTP 요청 검사, 초안·revision·멱등성 기록, 운영자 승인 결속 검사, diagnosis.Gateway 명령 전달. Planner·Worker·LLM·DB를 import하지 않는다.
- diagnosis/: diagnosis.v1 실행 명령과 응답, 준비된 단일 실행 서비스, 제한된 JSON pipe 전송. HTTP 인증·화면 상태를 알지 않는다.
- server/diagnosis_management.go: 기존 보호 JWT 키와 DB 로그인 해시만 조립하는 인증 factory. 기존 단일 관리자 ARTEX이며 다중 사용자 RBAC 완료가 아니다. 새 비밀번호·JWT 키·모델 프로필·작업을 DB에 만들지 않는다.
- cmd/first-controlled-diagnosis/management.go: 운영자가 기동하는 독립 API 프로세스. 이미 실행 중인 gateway 프로세스와 상속 pipe로 연결한다. HTTP 요청마다 subprocess를 만들지 않는다.
- cmd/first-controlled-diagnosis/managed_registry.go: 운영자 보호 registry를 매번 읽어 공개 선택 항목 및 정확 초안 승인을 제공한다.
- 기존 prepare-task → prepare-intents → check → run → report CLI는 유지한다. UI 초안 ID는 기존 실제 task ID가 아니며, 운영자 준비·승인 연결 전에는 실행할 수 없다.

이 경계는 기존 server 전체의 Planner·Worker 의존을 제거한 것이 아니다. 기존 실행 감독자와 알고리즘은 gateway 프로세스 내부에 남고, 신규 관리 API가 그 내부를 호출하지 않는다.

## API

모든 관리 경로는 명시적 Bearer 인증을 요구한다. 토큰을 URL/query/cookie로 전달하지 않는다. 상태 변경은 Content-Type: application/json과 body의 version: "diagnosis-api.v1"을 요구한다. unknown/중복 JSON 키, 잘못된 버전, 16 KiB 초과 body, query parameter는 거부한다.

기본 경로: /api/diagnoses/v1

| 메서드·경로 | 요청 | 응답·의미 |
|---|---|---|
| GET /catalog | 없음 | {version, profiles:[{id,label,configuration}]}. 운영자가 등록한 정확 설정만 선택 가능 |
| POST /drafts | {version,configuration}, Idempotency-Key | 201 {version,draft}. 초안만 생성하며 Gateway/모델/대상 호출 없음 |
| GET /drafts | Idempotency-Key | 생성 응답 불명 때 같은 관리 프로세스의 사용자별 receipt 조회. 없으면 404 |
| PATCH /drafts/{id} | {version,expected_revision,configuration} | 200 새 revision. 준비 상태와 기존 승인 결속 제거. 시작된 초안 수정 거부 |
| POST /drafts/{id}/validate | {version,expected_revision} | 승인 없으면 200 awaiting_approval. 정확 승인 및 Gateway 준비가 모두 확인되면 prepared |
| POST /drafts/{id}/start | {version,expected_revision}, Idempotency-Key | 202 실제 Gateway 시작 응답. 준비·현재 승인·revision·digest 재검사 |
| GET /drafts/{id}/status | 없음 | 현재 초안, 시작 후에는 Gateway status 재조회 |
| POST /drafts/{id}/stop | {version,expected_revision} | 202 Gateway 중지 요청. 실행 중인 자기 초안만 허용 |

configuration 예시:

~~~json
{
  "target_ref": "operator-target",
  "scope_ref": "operator-read-scope",
  "model_profile_ref": "operator-model",
  "approval_mode": "confirm_every_request",
  "budget": {
    "requests": 2,
    "tokens": 100,
    "cost_usd_micros": 0,
    "duration_seconds": 60
  }
}
~~~

위 값은 문서용 비민감 예시이며 실행 권위가 아니다. URL·IP·경로·메서드·모델 키·자격 값·보호 파일 경로를 브라우저에서 받지 않는다. scope/model/target 참조의 실제 의미와 예산은 운영자가 기존 보호 launch/profile/policy에 동일하게 결속해야 한다. v1은 catalog에 있는 configuration과 완전히 같은 값만 허용한다. 임의 예산 확대·자유 URL 입력은 지원하지 않는다.

draft 필드: id, revision, configuration, source_digest, state, 선택적 task_id, snapshot.
source_digest는 정본 Configuration JSON의 SHA-256이며 공개 API diagnosisapi.ConfigurationDigest를 운영자 조립에서 재사용한다.
snapshot은 diagnosis.v1의 task/preparation/state/started/drain_complete/result_ready만 포함한다. 실행 종료와 취약점 판정은 서로 다르다. 실제 후보·판정·HTML·AI 프롬프트는 기존 진단 보고서 API를 계속 사용한다.

오류 형식은 {version:"diagnosis-api.v1", error:"stable_code"}다. 400 잘못된 입력/revision, 401 인증, 403 미등록 설정, 404 자기 초안 없음, 409 승인/중복/상태 충돌, 503 registry/Gateway/불명 실행을 구분한다. 내부 오류·보호 경로·토큰 값은 반환하지 않는다.

## 멱등성·불명 결과

- 초안 생성은 사용자+키에 결속한다. 같은 키·같은 최초 설정은 같은 초안이며 다른 설정은 거부한다.
- PATCH/validate/start/stop은 현재 revision을 요구한다. 편집하면 검증·승인 결속을 버린다.
- 시작 직전에 Registry가 사용자 + draft ID + revision + source digest + 실제 task ID + preparation ref + 만료를 다시 확인한다. 검증 때와 승인 내용이 달라지면 시작하지 않는다.
- 승인 만료 시각은 UTC로 정규화해 비교한다. 같은 시각의 시간대 표현·파싱 위치는 결속을 바꾸지 않으며, 실제 만료 시각·task·preparation 변경은 시작과 시작 전 task 접근을 차단한다.
- start는 Gateway 호출 전에 key를 기록한다. 응답 유실 뒤 같은 POST도 다시 전달하지 않는다. 다른 key로 시작하려 해도 거부한다.
- start/stop 응답이 불명이면 unknown으로 보존하고 GET status로 확인한다. 클라이언트와 Gateway transport 모두 자동 재송신하지 않는다.
- 승인 만료는 시작을 막는다. 이미 시작한 실행의 관측·중지는 기존 실행 결속으로 계속 가능하다.
- 초안·HTTP receipt는 프로세스 메모리 한정이다. 관리 서버 재시작 시 초안과 receipt는 소실된다. 이것을 내구성 있는 작업 큐로 주장하지 않는다.
- 실제 단회 실행 방지는 기존 CLI 권위/marker와 단일 Gateway의 시작 key가 담당한다. 관리 서버 재시작이 기존 승인 재사용을 허용하지 않는다. 내구성 draft store·다중 관리자/인스턴스·스케줄러는 후속이다.

## 운영자 보호 registry

형식 abc2lab.diagnosis-management-registry/v1:

~~~json
{
  "format": "abc2lab.diagnosis-management-registry/v1",
  "profiles": [
    {
      "id": "approved-read-profile",
      "label": "소유한 읽기 시험",
      "configuration": {
        "target_ref": "operator-target",
        "scope_ref": "operator-read-scope",
        "model_profile_ref": "operator-model",
        "approval_mode": "confirm_every_request",
        "budget": {
          "requests": 2,
          "tokens": 100,
          "cost_usd_micros": 0,
          "duration_seconds": 60
        }
      }
    }
  ],
  "approvals": []
}
~~~

초안 생성은 승인행 없이 가능하다. 실행을 승인하려면 운영자가 기존 CLI로 실제 task·의도·격리·정책·모델·예산·권위 준비를 확인한 뒤, 해당 초안의 정확 값으로 approvals에 다음 필드를 공급한다.

principal: "ARTEX", draft_id, revision, source_digest, task_id, preparation_ref, expires_at(RFC3339).

파일은 기존 readPrivateFile의 소유권·권한·비심링크 검사를 통과해야 한다. 매 catalog/validate/start 읽기에서 최신 보호 파일을 다시 검증하며 unknown/중복 키, 중복 승인 결속, 미등록 설정 digest를 거부한다. 브라우저는 registry를 수정하거나 승인행을 만들 수 없다.

중요: registry의 configuration이 Gateway가 검사한 실제 launch 자료와 같은 의미라는 것은 운영자 공급 계약이다. v1은 해당 보호 파일들을 자동 제작하거나 임의 참조로 실자료를 합성하지 않는다. 운영자 확인 없이 registry를 자동 채워 시작하는 경로는 없다. 이 확인 및 기존 E② 권위가 없으면 awaiting_approval이며 실제 실행은 미완료다.

## 독립 프로세스 기동 경계

1. 운영자가 기존 CLI 준비 절차와 check를 수행한다.
2. 운영자가 gateway -config <보호 launch> -preparation-ref <고정 참조> 프로세스를 시작한다.
3. 같은 운영자 bootstrap이 두 private pipe를 만들어 gateway stdin/stdout과 관리 프로세스의 상속 descriptor를 연결한다.
4. manage -config <보호 API launch> -registry <보호 registry> -gateway-read-fd <3 이상> -gateway-write-fd <3 이상>을 실행한다.
5. 관리 API의 기존 login 후 catalog/draft/validate/start를 사용한다.

관리 launch는 기존 report launch 문법을 재사용하되 별도 loopback listen 주소와 인증 settings SELECT만 필요한 DB 연결을 공급한다. 모델 manifest·authority·Planner를 관리 프로세스에서 열지 않는다. launch 형식 검증은 기존 필드들을 요구하지만 실제 실행 자료의 검사는 gateway가 한다.

descriptor는 서로 달라야 하고 실제 pipe여야 한다. regular file/stdin/stdout/같은 descriptor는 거부한다. 새 shell 명령·프로세스·HTTP endpoint로 자동 연결하지 않는다. 운영자가 신뢰한 bootstrap의 private inherited pipe가 인증된 프로세스 통신 경계이며 범용 네트워크 RPC 서비스가 아니다.

Gateway는 기동 때 기존 startLaunch 준비·단회 권위를 획득한다. 사용자가 시작하지 않고 프로세스를 종료해도 같은 단회 승인을 재사용하지 않는다. API 종료는 HTTP drain 뒤 pipe/DB를 닫고 gateway의 기존 취소·감사·저장 마감을 유도한다. 응답 불명 pipe는 폐기하며 자동 재연결하지 않는다.

## 검증과 남은 수용

- API 단위/실제 HTTP 시험은 가짜 Gateway로 인증·설정·정확 승인·동시 중복 시작·불명 응답·파서를 검증한다.
- 별도 PG 시험은 기존 JWT 로그인·최소 settings SELECT·초안 생성·승인 없는 시작 거부·만료/비밀번호변경을 검증한다.
- 보호 registry와 inherited pipe 검사는 CLI 별도 시험이다.
- 별도 프로세스 Gateway 시험, 실제 supervisor/E② 합성 대상 시험, Next/브라우저 시험의 결과는 서로 구분한다. 같은 테스트 프로세스에 넣은 시험을 독립 배포 완료로 세지 않는다.
- 실제 운영자 보호 registry와 launch의 의미 일치, 운영 장비의 private pipe 배치, 내구성 저장·복구, 실제 Qwen3.5·외부 대상·비용 승인은 별도 수용이다. 미실행은 NOT_RUN으로 남긴다.

## 기존 실행·보고서 연결용 과도기 adapter

운영자 전용 선택 옵션 -runtime-api-origin http://127.0.0.1:PORT는
관리 API와 같은 origin에서 기존 실행·보고서의 공개 DTO를 사용하게 한다.
목적지는 경로·자격·query·fragment가 없는 명시적 숫자 loopback IP와 port여야 한다.
프로세스 기동 때 고정하며 HTTP 클라이언트가 공급할 수 없다.
별도로 시작한 실행 서버와 보고서 서버가 운영자가 정한 같은 port를 차례로 사용한다.
HTTP 요청으로 새 프로세스를 시작하지 않는다.

요청별 승인 목록·상세·결정·중지, diagnosis-run 조회, diagnostic HTML 조회,
remediation 조회·생성·prompt만 전달한다. 기존 폐쇄형 라우트 파서·메서드 검사,
정본 task ID, 중복·미등록 query 거부를 그대로 적용한다.
인증된 ARTEX principal은 정확히 검증된 task 결속을 가진 초안의 소유자여야 하며,
시작 전에는 운영자 승인도 아직 유효해야 한다.
일반 task·model·prepare·start API는 전달하지 않는다.

Authorization과 필요한 JSON Content-Type만 전달한다.
cookie·클라이언트 멱등성 헤더·환경 proxy·redirect는 전달하지 않는다.
요청은 256 KiB, 응답은 16 MiB로 제한하며 연결·응답에 시간 제한을 둔다.
연결을 재사용하지 않고 쓰기 요청은 한 번만 보낸다.
쓰기 결과가 불명이면 오류를 반환하고 명시적 상태 조회로 확인하며 재시도하지 않는다.
Gateway의 시작·제어는 별도 경계다.
이는 기존 공개 DTO를 잇는 과도기 adapter이며, 기존 server 또는 Planner/Worker
구현의 완전한 분리를 뜻하지 않는다.

unknown Gateway snapshot은 started=true를 요구하지만 drain_complete와
result_ready는 각각 보존한다. 결과 저장만으로 drain 성공을 추정하지 않는다.
