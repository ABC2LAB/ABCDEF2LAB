# URL 기반 첫 진단 자동 준비

## 범위와 상태

인증된 사용자가 URL·설치 프로필·표시 이름을 선택하고 명시적으로 시작하면,
신뢰 준비 서비스가 실제 task와 Planner intent를 만들고 기존 E②·Gateway·저장 보고서 경로를 연결한다.
URL은 요청 입력이며 대상·메서드·모델·비용을 승인하는 권위가 아니다.

기존 diagnosis.v1 실행 계약과 diagnosis-api.v1 관리 계약은 유지한다.
새 공개 diagnosis-api.v2는 비공개 diagnosis-preparation.v1을 호출한다.
기존 CLI·v1 운영 설치는 이 기능을 켜지 않아도 같은 계약으로 동작한다.

이 문서는 구현 경계와 시험 절차다. 실제 모델·외부 대상의 진단 성공 또는 운영 배포를 뜻하지 않는다.
현재 실모델·외부 대상 수용은 **NOT_RUN**이며, 소유 합성 환경의 결과와 구분한다.

## 회차 자동화와 일회성 설치

사용자 회차:

1. 설치 관리자 `ABC2LAB` 계정으로 로그인한다.
2. URL·설치된 읽기 프로필·선택적 표시 이름을 입력하여 초안을 만든다.
3. 서버가 반환한 실제 origin·경로 prefix·메서드·query/body 범위·모델·예산·기한을 확인한다.
4. 명시적으로 시작한다. 시작 전에는 task·Planner·대상 송신·단회 권위를 만들지 않는다.
5. 준비 단계, 실제 실행·승인/거절, 마감·저장 상태를 확인하고 결과 보고서를 연다.

운영자 설치는 별도다. 보호 DB 역할, 인증키, 설치된 모델 profile/manifest, 격리 helper, CA·감사 pin,
실제 비변경 범위, 위임·예산·기한을 한 번 준비해야 한다.
미설치·미승인 프로필은 준비되지 않은 이유를 반환한다.
HTTP handler가 임의 shell 명령을 만들거나 회차마다 CLI를 호출하여 이 구분을 우회하지 않는다.

## 설치 기동 계약

보호 설치 파일의 format은 abc2lab.url-preparation-installation/v1이다.
현재 준비 서비스 실행 지원은 Linux UID/Unix socket 기반이다. macOS·Windows 교차빌드 성공을 해당 OS 실행 수용으로 확대하지 않는다.

- operator_uid: 실제 비root 서비스 UID.
- state_dir, runs_dir, socket_path: 같은 UID의 보호된 영속 상태·회차·비공개 IPC 경로.
- report_listen_addr: 정본 loopback IP와 고정 포트.
- api_launch: 실제 관리 API 기동 파일의 경로와 SHA256. 준비 서비스가 실제 관리 포트도 보호한다.
- protected_endpoints: 모델·DB·관리 서비스의 정확한 주소/포트 목록. 이를 target 예외로 승인할 수 없다.
- profiles: 허용 principal, 표시/모델 참조, 안정된 delegation_ref, 명시적 derive_runs,
  유효기간, max_runs, 회차/합산 budget, 보호 launch/policy/manifest 원문 SHA,
  실제 비변경 범위 attestation, 호출당 context/output 상한.

실제 의미와 폐쇄 JSON 필드는 [설치 타입](../cmd/first-controlled-diagnosis/url_preparation_config.go)을 따른다.
정책의 approval_display에 배열이 있다면 null 대신 빈 배열 []를 사용한다.
일반 CLI의 기존 정책 파서는 변경하지 않았다.

새 URL 모드의 DB 주소는 Unix socket 또는 정본 literal IP만 지원한다. 모든 pgx fallback도 같은 조건이며
해당 IP/포트를 보호 목록과 대조한다. DNS 이름 DB는 실제 연결 pin이 없어 미지원으로 닫는다.
DSN은 host·port·user·password·dbname·sslmode가 명시된 self-contained keyword 형식만 지원한다.
중복 키·ASCII 제어문자·service/servicefile/passfile·TLS 파일 간접 참조, PG* 환경 기본값,
서비스 계정의 자동 발견 .pgpass·.postgresql 인증 파일이 있으면 닫는다.
검증한 bytes에서 한 번 파싱한 pgx 설정을 그대로 연결에 사용한다.
기존 DSN의 TLS를 끄거나 인증서를 우회해 호환시키지 않는다.
API/report 주소의 mapped IP·zone·비정본 IPv6/포트 alias도 거부한다.
API 기동과 보호 인프라 결속 변경은 서비스 재시작을 요구하며 실행 중 보호 목록을 조용히 바꾸지 않는다.

설치자가 한 번 시작하는 두 프로세스:

~~~text
first-controlled-diagnosis prepare-service -installation <보호 설치 파일>
first-controlled-diagnosis serve-url-api -installation <같은 설치 파일> -config <승인된 API launch> -socket <설치 socket> -runtime-api-origin <설치 loopback 보고서 origin>
~~~

예시의 자리는 실제 승인된 설치 값으로 채운다. 위 명령은 설치 기동이며 회차별 task/intent 생성 명령이 아니다.
서비스는 기존 인증 자료만 소비한다. 브라우저에서 키·DB DSN·manifest·권위 파일을 업로드하지 않는다.
인증 주체는 기존 단일 ARTEX 관리자다. 영수증의 주체 결속을 다중 사용자/조직 권한 분리로 확대하지 않는다.

## 호출 경계

브라우저 → 기존 Bearer 인증을 사용하는 공개 URL API
→ 고정된 보호 Unix socket의 준비 계약
→ 설치 승인 검사 및 영속 시작 영수증
→ 실제 task 생성 및 Planner 준비
→ 실제 task/intent에 결속한 단회 M01 권위
→ 기존 diagnosis.v1 Gateway
→ Worker·격리 helper·E② 실제 송신 통제
→ drain·저장 사실
→ 자동 보고서 dispatcher와 기존 결과 UI.

공개 API는 초안 저장·모델·엔진을 소유하지 않는다.
현재 프로세스 분리는 공개 API와 신뢰 준비 runtime의 두 부분이다.
기존 독립 Gateway 계약을 사용하되 이번 구현에서 Gateway가 세 번째 별도 daemon이라고 주장하지 않는다.
준비 프로세스가 영속 상태와 실행 수명을 소유하므로 API 연결 종료를 새 준비 요청으로 해석하지 않는다.
보고서 bridge는 고정 loopback API에만 연결하고 준비 영수증의 실제 task 소유권을 다시 확인한다.
일반 task/model API나 임의 경로 proxy는 추가하지 않는다.

## 공개 API

기본 경로: /api/diagnoses/v2. 모든 요청은 명시적 단일 Bearer 인증이 필요하다.

| 요청 | 의미 |
|---|---|
| GET /catalog | 설치된 프로필 목록, 읽기 전용 |
| POST /drafts | {version,input:{target_url,profile_id,display_name?}}와 Idempotency-Key로 초안 생성 |
| GET /drafts | 같은 Idempotency-Key의 생성 영수증 조회 |
| GET /drafts/{id}/status | 현재 영속 상태 조회 |
| POST /drafts/{id}/validate | {version,expected_revision}로 설치 결속 재검사 |
| POST /drafts/{id}/start | 같은 본문과 Idempotency-Key로 명시적 단회 시작 |
| POST /drafts/{id}/stop | {version,expected_revision}로 중지 요청 |

version은 diagnosis-api.v2다. 임의 principal, task, 파일 경로, socket 주소, 명령, 환경 변수,
권위·정책·예산 원문을 body로 받지 않는다. 중복 키·대소문자 alias·알 수 없는 필드·과대 본문을 거부한다.
느린 본문을 읽은 뒤 인증 주체를 다시 확인한다.

응답에는 공개 draft 또는 profiles와 owner_context가 있다.
owner_context는 로그인 주체를 구분하는 불투명 식별자이며 인증 토큰이나 실행 권위가 아니다.
브라우저는 영수증 키와 최소 결속만 보관하며 인증값·보호 설치 자료를 복구 상태에 저장하지 않는다.

## URL·SSRF·비변경 경계

- HTTPS 정본 URL만 허용한다. userinfo, query/fragment, percent encoding, 역슬래시, dot segment,
  빈/비정본 port, 모호한 IP, Unicode/trailing-dot host 등 해석 차이를 거부한다.
- 초안 생성·검증은 DNS·HTTP·모델을 호출하지 않는다.
- 정확한 설치 origin+port와 범위를 먼저 대조한다. 다른 subdomain·port·redirect 목적지는 자동 승인하지 않는다.
- 시작 시 제한된 DNS 확인 뒤 실제 송신은 승인된 literal IP pin으로만 연결한다.
  인증서 검증·TLS 이름 검증을 끄거나 실패 시 불투명 터널로 전환하지 않는다.
- 일반 public 대상의 특수·사설 주소는 기본 거부한다.
  소유 합성/on-prem 예외도 정확한 origin+port+IP+scope에만 적용한다.
  관리 API·준비 서비스·모델·DB 목적지와 겹치는 target은 예외로도 허용하지 않는다.
- URL 준비 모드는 설치 로더와 준비 계약에서 허용 메서드를 GET/HEAD로 제한한다.
  E②는 실제 요청 메서드를 설치 정책과 다시 대조한다. UI 선택지만의 제한은 아니며,
  다른 모드도 지원하는 공통 E② 전체가 GET/HEAD 전용이라는 뜻은 아니다.
- GET이 비변경이라는 가정은 없다. 설치 attestation은 실제 prefix와 하위 경로,
  임의 query 및 bounded body라는 기존 E② 표현 범위 전체를 확인해야 한다.
  UI나 프롬프트로 실제 전송 권한이 더 좁아졌다고 주장하지 않는다.
- E②가 지원하지 않는 프로토콜·도구 경로는 차단/미지원으로 남긴다. 전수 통제를 주장하지 않는다.

## 권위·예산·영속성

보호 설치 위임은 기존 동일 EUID 파일 신뢰 루트를 사용한다. 새 서명 서비스나 브라우저 권위를 가정하지 않는다.
위임은 회차 파생 허가, 안정된 delegation 참조, 원문 digest, exact scope, 모델,
한 회차/합산 예산, 최대 회차 수, 기한 및 모든 보호 pin을 명시한다.
공개 source digest는 입력뿐 아니라 실제 설치 결속을 포함한다.
준비 시 전체 digest를 검증한 파일·상위 경로 FD를 유지하고, 모델/대상 송신 직전에는
inode·소유권·mode·link·크기·mtime·ctime과 경로 결속을 짧게 재검사한다.
큰 helper를 매 TLS write마다 다시 읽지 않는다. 검사 중 취소·만료도 송신 전에 다시 확인한다.
외부 파일 변경과 네트워크 송신을 원자적으로 묶는 즉시 철회라고 주장하지 않는다.

시작은 부작용 전에 영속 영수증과 합산 예산을 예약한다.
task 생성·Planner 호출·권위 발급·Gateway 시작 전 단계도 동기 기록 성공 후에만 진행한다.
응답 유실·불명 결과에서 새 key·권위·예산을 발급하거나 예약을 환불하지 않는다.
같은 key는 동일 결과를 조회하고 다른 입력을 같은 key에 넣으면 충돌이다.

재시작은 미완 부작용을 unknown으로 보존하며 자동 재계획·재실행하지 않는다.
이미 확인한 TaskID, 권위 소비, started, drain_complete, result_ready를 지우지 않는다.
준비 단계의 unknown은 started=false일 수 있다. 마감과 저장 사실은 독립이다.
result_ready=true인 실제 저장 결과만 보고서로 연결하고 마감 미확인은 별도로 표시한다.

저장 파일 손상·부분 기록·journal/head 불일치는 전체 서비스를 unavailable로 닫는다.
이는 미완 회차를 정상적으로 읽어 unknown으로 복구하는 경우와 다르다.
현재 자동 복구/관리자 reconciliation UI는 제공하지 않으며 파일을 삭제해 새 권위를 만드는 우회는 허용하지 않는다.
동일 신뢰 UID가 journal과 head 전체를 과거로 되돌리는 공격을 탐지하는 별도 witness는 제공하지 않는다.
보고서 재시작 복구도 원래 source digest와 현재 보호 설치 pin이 일치해야 한다.
설치 구성이 바뀌어 원래 DB 결속을 확인할 수 없으면 다른 task로 추측해 연결하지 않고 조회를 닫는다.
과거 회차를 새 설치로 이전하는 자동 migration은 이번 범위가 아니다.

### unknown 이후 운영 정지와 복구 한계

unknown 영수증이 하나라도 있으면 같은 준비 서비스 설치의 모든 새 Start는 busy로 닫힌다.
drain_complete=true는 로컬 실행 수명이 마감됐다는 사실이며, 모델 사용량이나 이미 전송한 외부 요청의
실제 결과를 확정하는 증거는 아니다. 이번 구현은 마감된 unknown만 예외로 새 시작을 허용하지 않는다.

현재 지원되는 in-place 영수증 재분류·강제 해제·재개 명령은 없다.
운영자는 보존된 영수증·감사·provider/대상 기록으로 미확인 실행과 사용량을 대조하고,
기존 예약을 계속 차감한 상태에서 복구 정책을 별도로 승인·검토해야 한다.
그 뒤 사용할 reconciliation 절차/API는 후속 설계·구현 대상이다.
journal/head 삭제, state_dir 교체, delegation_ref 변경으로 기존 예약이나 unknown을 우회하는 방식은
지원 복구가 아니다. 새 설치·새 위임만 만들면 이전 불명 실행이 해소된 것으로 처리하지 않는다.
이 운영 정지·복구 정책은 첫 실제 대상 수용 전에 팀이 확인해야 할 조건이다.

URL 경로는 설치된 로컬 OpenAI 호환 비스트리밍 profile과 명시적인 OpenRouter 모드를 지원한다.
OpenRouter 보호 설치·키·회차/누적 USD·외부 전송 동의와 수용 범위는 [통합 계약](url-openrouter-integration.md)을 따른다.
준비와 실행이 공유하는 ledger가 실제 호출 전에 요청 1회, 직렬화한 요청 바이트 수 + framing 여유
(입력 토큰의 상한, 설치한 전체 context를 넘으면 거부), 명시적 최대 출력 토큰을 예약한다(P3 결정안 C28 결정 1).
추정 token 수를 hard limit로 사용하지 않는다. provider usage와 예약치를 구분한다. 실패한 호출은 예약한 양을
쓴 것으로 정산하고 후속 호출을 막지 않는다(C05). 보고 사용량이 예약을 넘거나 정책이 막은 경우는 후속 호출을 차단한다.
같은 회차의 Planner·Worker 모델 호출은 공유 gate에서 차례로 기다린다. gate는 예약 전부터
응답 사용량의 동기 영속 확인까지 유지하며, 대기 취소는 예약·모델 호출을 소비하지 않는다.
대기 뒤 실제 task/profile 결속과 남은 예산을 다시 확인한다. 예약을 넘는 사용량 보고·정책 차단 또는
사용량 저장 실패는 기존 unknown 차단을 유지하며 대기 중인 호출도 모델로 보내지 않는다.
이 예산 모드에서는 SDK 재시도와 HTTP redirect 재전송도 차단한다.
외부 유료 모델 fallback은 없다. OpenRouter URL 모드는 기존 CLI 지원과 별도로 실제 URL 준비·실행 경로에서 검증한다. 실제 유료 OpenRouter 수용은 합성 시험과 구분한다.

## 검증과 미실행 구분

필수 합성 수용은 실제 PostgreSQL·Go 프로세스·Next·Chrome·소유 HTTPS 대상·합성 OpenAI provider를 사용한다.
API 응답 mock이나 사전 task/intent 준비를 성공 근거로 삼지 않는다.
초안 이전 task/model/target 0건, 시작 후 실제 task/intent 생성,
허용 요청의 대상 수신 양성과 거부 요청 수신 0건, 단회 권위·마감·DB 저장·보고서를 각각 대조한다.
로그는 기존 구조화 기록의 건수·요약만 확인하며 인증값·원문 스캔·DB dump를 커밋하지 않는다.

### 합성 수용 실측 (2026-11-09)

기준 dev는 7814bbc6e2a916ae71c41b521bc8ddd0773eeaf8이다.
환경은 Linux/WSL, Go 1.26.3, Node 22.23.4, PostgreSQL 16, Next 16, Chrome 157이다.
새 URL browser entry와 script는 UI PR의 변경이며 아래 결과는 A+B 결합 소스의 결과다.
건수는 native/Chrome 각각의 소유 happy 회차 한 번을 기준으로 한다.

| 구간 | 실제 결과 |
|---|---|
| 시작 전 | task·모델·대상 호출 0 |
| 시작 후 | 실제 task 1, 실제 Planner intent 1, 후보 1, 확정 0 |
| E② | 허용 HTTPS 수신 1, 거부 대상 수신 0, 정책 차단 intent 1 |
| 마감·저장 | run 1, drain_complete/result_ready true, HTML 실제 다운로드 |
| 모델·비용 | 합성 OpenAI HTTP 9회: goals 2, Planner 3, Worker 4; 로컬 USD 0 |
| 사용량 | 보고 입력/출력 90/90, 보수적 예약 입력/출력 1,179,648/18,432 |
| 기존 JSONL | 파일 1, task 결속 이벤트 115, run 1, call 2 |
| 브라우저 | 실제 Go API/Next/Chrome, 53 assertions PASS, 최종 14.440초 |

실제 성공 start 응답 유실, API 프로세스 재시작, 새로고침 GET 복구, 동일 start key 재요청을 검사했다.
완료 후 준비 서비스도 재시작하여 Job 전체 사실·source_digest·사용량과 저장 HTML 내용의 보존,
추가 모델·대상 호출 0을 검사했다. HTML의 생성 시각 한 곳만 비교에서 제외했다.

실제 Planner HTTP 응답을 기다리는 중 Stop 시험은 task 1을 보존하고 intent/run 0,
추가 모델/대상 호출 0, 권위 미발급·엔진 미시작·drain=true·result_ready=false를 확인했다.
그 호출의 사용량 미확인을 state=unknown으로 유지한다.
새 process Stop 시험은 준비 중지이며 Worker 실행 중 Stop까지 새로 시험했다는 주장이 아니다.
모델 동시성 수정 후 native happy+준비 Stop 패키지도 6.691초 PASS였다.

합성 모델·대상은 소유 fixture이며 실제 Qwen 성능 또는 Sparrow 진단 결과가 아니다.
준비·첫 호출 시간에는 의도적인 Planner 대기·API 재시작이 포함되므로 성능 비교에 사용하지 않는다.

| 계측 (ms) | native | Chrome |
|---|---:|---:|
| 첫 모델 | 133 | 179 |
| Gateway 준비 | 336 | 1,993 |
| 첫 대상 요청 | 470 | 2,388 |
| 저장 결과 | 1,275 | 3,275 |

실행:
- 보호된 소유 PostgreSQL의 DSN과 ARTEX_FINALIZATION_SCOPE_TEST_OWNED=1을 명시한다.
- ARTEX_URL_DIAGNOSIS_TEST=1로 go test -p 1 ./server -run '^TestFirstDiagnosisURL(IndependentProcesses|StopDuringPreparation)OwnedPostgres$' -count=1 -v
- 설치된 전용 Chrome CDP·Node·script/output 경로를 ARTEX_FIRST_DIAGNOSIS_BROWSER_*에 명시하고
  go test -p 1 ./server -run '^TestFirstDiagnosisURLBrowserIndependentProcessesOwnedPostgres$' -count=1 -v
- 구체 fixture 설정과 프로세스 수명은 [native 시험](../server/first_diagnosis_url_process_linux_test.go),
  UI PR의 server/first_diagnosis_url_browser_process_linux_test.go와 scripts/url-diagnosis-browser.cjs를 따른다.

### 동시 Worker와 추가 안전 경계 회귀

실제 Planner가 intent 2개를 만들고 Engine Worker 2개가 동시에 실행되는 독립 프로세스 시험을
추가했다. 첫 Worker의 합성 모델 HTTP 응답을 보류한 300ms 동안 실제 running intent 2,
worker_started 2, finished 0인 상태에서 Worker HTTP 1·전체 모델 5·예약 5가 유지됐다.
두 번째 Worker의 대기는 추가 예약·대상 송신을 만들지 않았다.

응답 해제 후 intent done 2·Worker 성공 2·후보 2·확정 0·허용 송신 2·거부 수신 0·run 1,
drain_complete/result_ready true를 확인했다. 합성 모델 11회(goals 2/Planner 3/Worker 6),
보고 입력/출력 110/110, 예약 입력/출력 1,441,792/22,528, 비용 USD 0이었다.
최초 성공 뒤 3회 반복도 3/3 PASS(10.392초)였다.
처음 두 시험은 허용 송신 2건으로 fixture 요청 상한 2를 소진해 마지막 Planner의 기존
remaining>0 검사를 통과하지 못했다. 다중 intent fixture 설치 상한만 3으로 정합화했으며,
실제 송신은 2건이고 제품 예산·E② 정책은 변경하지 않았다.
이 시험은 일반 CI에서 자동 실행되는 테스트가 아니라 ARTEX_URL_DIAGNOSIS_TEST=1의
TestFirstDiagnosisURLConcurrentWorkersOwnedPostgres로 별도 실행한 수용시험이다.

공유 예산의 실제 provider wrapper 회귀는 서로 다른 Server/provider가 한 예산을 공유하는 경우,
대기 취소, 사용량 영속 확인 전 재예약 금지, 대기 중 task/profile 변경, 요청 한도,
호출 오류·usage 누락/초과·오류와 usage 동시 반환·영속 실패 후 후속 모델 0건을 검사한다.
준비/API에는 Authorized=false, 준비·실행 중 같은/다른 소유자의 신규 Start,
Stopping 뒤 늦은 Running, 중복 task, 설치 digest/delegation 변경 차단을 추가했다.
drain된 unknown의 신규 Start 차단·예약 보존은 서비스 재시작 뒤에도 대조한다.

runtime의 21개 음성/양성 사례는 입력 origin/path/local_only, 보호 endpoint 충돌,
credentials/attestation/MaxConcurrent, 혼합 DNS 순서·IPv6, Execute의 DNS·digest/delegation
차단을 실제 부작용 전 확인한다. 정상 pin은 소유 DB TCP probe까지 도달한다.
DNS 사례는 runtime별 비공개 resolver를 net.Pipe에 연결하므로 전역 resolver나 외부 DNS를
변경하지 않는다. 운영 생성자는 nil 기본값으로 기존 시스템 resolver를 사용한다.
리뷰의 가드 공백을 회귀 사례로 보강한 것이며, mutation 도구를 다시 실행해 모든 변이 제거를
계측했다는 주장은 아니다.

### 전체 회귀

동시성 수정 후 A의 최종 전체 Go race는 59패키지, 7,222 PASS / 0 FAIL / 11 SKIP이다.
A 일반 실행에서 opt-in 때문에 생긴 URL native·Stop·동시 Worker 및 기존 v1 browser 두 개의
SKIP 5개는 실제 수용시험에서 각각 별도로 PASS했다. URL v2 Chrome entry는 B에만 있으며
위 브라우저 수용으로 별도 확인한다.
나머지 NOT_RUN 6개는 root chown이 필요한 기존 소유권 검사 5개와
승인된 외부 private model 설정이 필요한 TestLiveContextReview 1개다.

Go 1.26.3에서 다음을 실행했다.
- GOMAXPROCS=2 GOMEMLIMIT=2GiB go build -p 1 ./...
- GOMAXPROCS=2 GOMEMLIMIT=2GiB go vet -p 1 ./...
- GOOS=darwin 및 GOOS=windows, GOARCH=amd64로 go build -p 1 ./... ./cmd/... 각각 PASS
- 소유 --network none PostgreSQL 16의 전용 DB와 CI_PG_*를 지정하고 umask 022에서
  scripts/ci/go-test.sh <로컬 비공개 JSON 경로> ./... -race -p 1 -count=1 -timeout 20m

동시성 수정 전 A+B 전체 race는 10:40:28Z–10:48:07Z에 7,142 PASS였고,
수정 후 A 전체 race는 11:32:22Z–11:40:30Z에 위 7,222 PASS를 확인했다.
초기 keyword DSN/umask 077 fixture 설정으로 생긴 13 FAIL은 제품 성공으로 계산하지 않았다.
저장소 CI와 같은 URL DSN/umask로 교정하여 수정 전 전체 결과를 다시 얻었고,
수정 후에도 같은 소유 DB 설정을 사용했다.
gofmt·git diff --check도 PASS다.

후속 평가 가드 검증은 전체 scripts/eval 1,586개, E② 배치 55개, Web 변경 감지 13개 PASS다.
loadLLMConfig의 source hash는 OpenRouter뿐 아니라 공유 예산 opt-in에서도 재시도를 금지한 차이를
검토한 뒤 갱신했다. 예산·외부 정책이 없는 기존 평가/local-only 경로의 Config·Retry는 유지한다.
해시 가드를 제거하거나 예산 보호를 되돌리지 않았다.
두 실제 loader의 18개 leaf case(정책/예산 nil·OpenRouter-only·Budget-only와 정상/빈 키/없는 profile)를
실제 PostgreSQL로 대조한 추가 race 회귀도 1회 및 3회 반복 PASS했다.
Retry 외 Config 전체·ok·활성 이름 의미를 확인했고 모델/대상 HTTP·생성 task는 각각 0이었다.

기존 v1 직접 실행 Chrome 68 assertions, 관리형 독립 프로세스 Chrome 45 assertions도 별도 PASS다.
관리형 script는 초기 초안 화면이 열린 새 UI를 반영해 닫혔을 때만 열고 실제 v1 폼을 확인한다.
기존 draft·revision/digest·복구 후 자동 시작 금지·명시 확인·단회 시작 검증을 유지했다.
로컬 시험의 임시 출력 필터는 screenshot·원문 DOM 실패 출력을 억제했으며 assertions와 종료 코드는 보존했다.

UI PR 결합 소스는 57파일·649테스트, 전체 타입검사, 변경 12파일 lint, production build PASS다.
명령은 web에서 기존 설치 의존성으로 실행했다.
- NODE_OPTIONS=--max-old-space-size=1536 node_modules/.bin/vitest run --maxWorkers=1 --no-color
- NODE_OPTIONS=--max-old-space-size=1536 node_modules/.bin/tsc --noEmit --incremental false
- NODE_OPTIONS=--max-old-space-size=1536 node_modules/.bin/biome check <변경된 web/src 파일>
- NEXT_TELEMETRY_DISABLED=1 NODE_OPTIONS=--max-old-space-size=1536 node_modules/.bin/next build --webpack

루트 설정·의존성·lockfile·CSS 변경은 없다.
독립 보안/계약·UI 검토에서 보고된 문제를 수정하고 최종 재검토에서 남은 차단 이슈가 없음을 확인했다.
팀원의 GitHub 리뷰 승인과 CI 결과는 PR의 실제 상태를 별도로 확인한다.

### NOT_RUN과 설치 전제

Sparrow 정확 호스트 전체 진단 허가는 다른 origin·port·인증 수단·상태 변경·무제한 요청·유료 모델의
승인으로 확대하지 않는다. 현재 원격 프로토콜, 검증된 비변경 경로·IP, 인증 계약, 요청/시간 상한,
실모델 제공자·비용 승인이 확정되지 않아 실제 접근과 Qwen3.5 호출은 NOT_RUN이다.
대상별 주소·프로토콜·안전 경로는 설치 데이터에만 두고 제품 코드에 하드코딩하지 않는다.

공개 Sparrow 문서는 [MCP 가이드](https://dev-docs.ondemand.sparrowcloud.ai/docs/guide/mcp)의
로컬 Node MCP 패키지와 [시작 안내](https://dev-docs.ondemand.sparrowcloud.ai/docs/2606.1/guide/start)의
서비스 인증을 설명한다. 이것만으로 허가받은 정확 호스트의 remote MCP/SSE/JSON-RPC,
비변경 API 경로 또는 인증 계약을 확정하지 않았다. 이 작업은 실제 대상 DNS·HTTP를 호출하지 않았다.
운영 회차는 정확한 origin/port/IP pin, 실제 비변경 prefix/query/body, 지원 프로토콜,
인증 계약, 요청·시간·모델/비용 예산과 설치 위임을 먼저 확정해야 한다.

## C04·C33 운영 보완 (#543 인계)

`Unknown` 게시와 backend `Execute` 반환은 다른 사실이다. 새 시작은 이전 회차의 실제 반환과
`drain_complete`를 모두 요구한다. 반환 사실은 보호된 영수증의 `execution_returned`에 기록한다.
실행 자원이 미마감이면 `busy`를 유지하고 모델 사용량·예약·누적 실행 횟수를 환불하지 않는다.
결과 불명은 복구 후에도 결과 불명이다. `result_ready`가 없는 회차를 보고서 완료로 바꾸지 않는다.

반환한 실행은 backend가 보관한 정확한 회차 핸들로 모델·격리 실행·E②·HTTP·DB 자원을 재마감하고
검증할 수 있을 때만 다음 시작 시 복구한다. 원래 gateway Run의 종료까지 확인하며, 늦게 오는 Close는
저장한 마감 증거만 읽는다. 준비 단계에서 drain을 확인하지 못한 경우에도 같은 task의 준비 자원 핸들로
취소·drain·DB 닫기를 다시 확인한다. 결과 저장 실패를 재실행하거나 성공으로 바꾸지는 않는다.
복구 증거는 `resources_recovered`에 별도로 저장하고 정상 마감한 회차의 복구 핸들은 제거한다.
검증은 Start IPC의 남은 시간 안에서 수행하므로 기한 내 확인하지 못하면 `busy`를 유지한다.

이전 형식의 `completed`·`failed`·`cancelled`이면서 `drain_complete=true`인 영수증은 이전 계약대로
반환한 것으로 읽는다. 정상 마감된 기존 설치는 같은 호스트 boot의 업그레이드 직후에도 새 진단을
시작할 수 있으며, 기존 결과와 예약은 보존한다. 새 실행은 `execution_evidence_version=1`을 먼저
저장하므로 boot ID를 못 읽은 경우도 이전 형식으로 잘못 읽지 않는다.

미확인 자원은 서비스 재시작만으로 풀지 않는다. Linux 호스트 boot ID가 시작 때와 달라진 경우는
이전 커널의 자원 종료 증거로 복구하며 결과·감사 성공으로 해석하지 않는다. 이전 형식의 `unknown`·
미마감 기록처럼 시작 boot 증거가 없는 경우는 첫 업그레이드 기동에서 현재 boot를 관찰 증거로만
저장한다(`recovery_witness_boot_id`). 같은 boot에서는 잠금을 유지하고 이후 실제 호스트 재부팅으로
boot가 바뀌었을 때 복구한다. 과거 시작 boot를 지어내지 않는다. 영수증 삭제는 복구 절차가 아니다.

URL v2 공개 관리 API는 영속 준비 영수증에 결속한 같은 운영자의 task에만
`POST /api/tasks/{id}/control`의 `pause`·`resume`을 전달한다. chat·steer·rerun·설정 API는 열지 않는다.
활동 조회·history·stream·seq 상세는 task를 명시해야 한다. 상속된 다른 task 의도 세션은 열지 않으며,
원문 명령·도구 인자·모델 문장·자격·metadata는 공개 활동 응답에 포함하지 않는다. 스트림은 즉시
flush하며 매 이벤트에서 인증·결속을 재검사한다. 중계 자체의 30초 상한보다 현재 준비 HTTP 요청의
5초 상한이 먼저 적용된다(API·backend WriteTimeout은 10초). 종료 뒤 클라이언트가 커서로 다시 연결한다.
preparation-only 내부 서버 차단은 유지한다.

검증: `TestUnknownPublicationDoesNotReleaseExecution`,
`TestUnknownClosureRecoveryPreservesFactsAndReservations`,
`TestLegacyDrainedTerminalUpgradeKeepsInstallationUsable`,
`TestURLClosureRecoveryWaitsForOriginalRunAndLateCloseIsSafe`,
`TestURLPreparationRecoveryChecksExactTaskAndDrain`,
`TestURLManagementOperationsPublicEntryOwnedPostgres`.
마지막 시험은 실제 설치 인증·공개 관리 mux·작업 권한 조회·소유 HTTP 수신기를 사용한다.
합성 실행 수신기는 실제 Planner/Worker·Qwen 수용을 대신하지 않으며 유료 모델 시험은 NOT_RUN이다.
