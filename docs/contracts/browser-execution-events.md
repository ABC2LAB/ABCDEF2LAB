# Browser 실행 이벤트 계약 v1 제안

상태: **팀 검토용 제안, producer/consumer 미구현.** 2026-11-11.
[실행기 설계안](../e2-browser-executor-design.md)의 Go 호스트 ↔ 관찰 소비자 ↔ 수용시험 계측 경계만 정의한다.
기존 `e2-http-v1`·감사 저널·Workbench API를 대체하거나 실행 권한을 발급하지 않는다.
기계 형식은 [JSON Schema](browser-event.schema.json), 합성 예시는
[읽기 승인](examples/browser-events.v1.json)·[쓰기 승인](examples/browser-events.approved-writes.v1.json)
각 9개 이벤트에 둔다. 예시는 실제 실행 기록이 아니다.

## 1. 근거의 출처

| `source` | 실제 생산자 | 증명할 수 있는 것 |
| --- | --- | --- |
| `host` | Go BrowserSession 관리자 | 보호된 실행 결속·수명·드라이버 결과 수집 |
| `proxy` | peer 결속된 Go 프록시 입구 | 실제로 파싱한 요청, 또는 입구에서 확인한 거부 |
| `e2` | Go 호스트가 수집한 보호된 E② Receipt | 그 시도의 정책 결정과 전송 관측 |

`source` 문자열이나 JSON의 출처 주장만 신뢰하지 않는다. 호스트 내부 타입/인증된 통로에서 출처를 붙이고,
드라이버 stdout·console·페이지 메시지·모델이 만든 JSON은 이 통로에 넣지 않는다.
드라이버 완료 보고는 `basis=driver_report`로, 호스트의 timeout/취소는 `host_control`로 남긴다.
`succeeded`는 드라이버의 완료 보고가 있어야 한다. 어느 쪽도 네트워크 도달 증명이 아니다.
독립 계측기는 이 이벤트를 복사하여 수신 증거로 만들지 않고 자체 접수·송신·응답을 기록한다.

## 2. 공통 envelope

한 레코드는 UTF-8 JSON 객체 하나다. 모든 공통 키를 넣고, 해당하지 않는 참조는 생략 대신 null로 쓴다.

| 필드 | 형식 / 의미 |
| --- | --- |
| `version` | 고정 `abc2lab.browser-event/v1` |
| `event_id` | 호스트 발급 불투명 참조, 세션 내·재전송 간 동일 사건에 동일 값 |
| `seq` | 세션별 1부터 증가하는 uint64의 10진 문자열. 브라우저/네트워크 시각 순서를 뜻하지 않음 |
| `at` | Go 호스트가 찍은 UTC RFC3339 시각, `Z`로 끝남 |
| `kind`, `source` | 아래 사건 종류와 허용 생산자 |
| `task_ref`, `run_ref` | 검증된 실행 scope의 참조 |
| `intent_id` | 기존 E② intent ID의 양수 10진 문자열 |
| `call_ref`, `command_ref` | 기존 E② Call 결속. opening 실패로 미발급이면 null |
| `browser_session_ref` | Browser 실행 수명 참조. 인증 SessionRef와 구분 |
| `phase` | v1에서는 `recon`만 허용. auth/diagnosis 연결 시 명시적 계약 개정 |
| `action_ref` | 액션 시작/종료 사건에만 사용. 1단계 네트워크 사건은 null |
| `request_ref` | Go 입구가 파싱한 애플리케이션 요청의 세션 내 유일 참조. transport 사건이면 null 가능 |
| `attempt_ref` | E②가 실제 발급한 원래 `Receipt.AttemptID`. 미발급이면 null |
| `payload` | `kind`에 따라 닫힌 객체 |

참조는 비밀에서 만들지 않은 최대 128자의 허용 문자 참조다. 실제 레지스트리의 존재·scope·수명도 검사한다.
`intent_id`, `seq` 및 모든 건수는 JSON number로 보내지 않는다. uint64 범위를 넘으면 거부한다.
정수처럼 보이는 문자열을 부동소수로 바꾸거나 다른 실행의 참조를 재결속하지 않는다.

## 3. 사건별 payload

Schema의 필드·enum·null 조건을 정본으로 하고, 아래는 그 의미를 설명한다.

| `kind` / `source` | payload | 참조 조건 |
| --- | --- | --- |
| `session.started` / host | `grant_ref`, `policy_revision`, `budget_ref`, `limits_ref`, `runtime_ref`, `crawl_permission`, `test_environment_ref`, `reset_proof_ref`, `browser_profile` | ready 성공 후 1회. call/command 필수, action/request/attempt null |
| `action.started` / host | `operation`: `navigate` 또는 `wait_for_selector` | action 필수, request/attempt null |
| `action.finished` / host | `status`, `reason`, `basis`(driver_report/host_control), `duration_ms`(10진 문자열) | 시작과 같은 action. request/attempt null |
| `request.observed` / proxy | `method`, `destination_scope`, `endpoint_ref`, `path_scope_ref` | request 필수, action/attempt null |
| `attempt.result` / e2 | 아래 Receipt 투영 | attempt 필수. 애플리케이션 요청이면 request 필수, proxy transport이면 request null |
| `ingress.rejected` / proxy | `layer`, `reason` | **E② AttemptID 미발급** 거부만. 파싱된 요청이면 request, transport이면 null. action/attempt null |
| `session.fenced` / host | `reason` | 최초 fence 1회. action/request/attempt null |
| `session.closed` / host | `status`, `reason`, 정리/완결 플래그, `counts`, `exploration_limits` | 최종 1회. action/request/attempt null |

### 요청 관찰

`request.observed`는 E② 허용 이전의 관찰이다. 읽기 승인에서 막힌 POST도 여기에 포함한다.
`method`는 GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS이며 그 밖은 `OTHER`로 투영한다.
`destination_scope`는 `approved`/`outside`/`invalid`다. `approved`는 목적지의 대상·경로 결속 확인이며,
메서드 승인·현재 권한·예산·전송 허용까지 성공했다는 뜻이 아니다.

실제 URL은 host private 레지스트리에만 두고, 이벤트에는 `endpoint_ref`를 넣는다.
원문을 안전하게 보유·결속할 수 없으면 endpoint는 null이며 발견의 상세가 불명임을 유지한다.
`path_scope_ref`는 이미 발급된 안전한 경로 범위 참조 또는 null이다.
정적 파서 후보·페이지 안의 URL 문자열을 실제 요청 관찰 사건으로 넣지 않는다.
정적 후보의 발견 수, 파싱된 요청 수, 고유 endpoint 수는 별도다.

정상 CONNECT 설정과 내부 TLS 교환은 애플리케이션 요청 수에 넣지 않는다.
CONNECT/파싱/TLS 거부에 기존 `Call.Reject`가 AttemptID를 발급했다면 `request_class=proxy_transport`의
`attempt.result`로 남긴다. 같은 거부를 `ingress.rejected`로 중복 계수하지 않는다.
OS 차단만 있고 호스트가 사건을 관측하지 못했으면 transport 사건을 추정 생성하지 않는다.

### 보호된 Receipt 투영

`attempt.result`는 `Call.Do` 또는 `Call.Reject`가 반환한 보호된 결과를 다음과 같이 옮긴다.
감사 저장 전의 중간 Receipt나 드라이버 보고를 최종 결과로 사용하지 않는다.

| 필드 | 원본 / 해석 |
| --- | --- |
| `request_class` | 호스트가 `application_http` / `proxy_transport`로 분류 |
| `e2_version` | 원본 `Version`, 현재 `e2-http-v1` |
| `policy_revision`, `rule_ref`, `e2_sequence` | 원본 정책·규칙·Sequence. 없는 RuleRef는 null, Sequence는 10진 문자열 |
| `method` | Receipt.Method의 안전한 enum 투영. 빈 값/기타는 OTHER. E②가 지우는 CONNECT를 영수증 밖에서 복원하지 않음 |
| `decision` | 원본 `allowed` / `denied` / `unsupported` |
| `reason` | 현재 e2proxy.Reason enum 그대로. 알려지지 않은 새 값은 호환 성공 대신 계약 오류 |
| `forward_attempted` | 대상 연결/송신을 시도했음. 접수/전달 완료 증명이 아님 |
| `request_written` | 원본 RequestWritten. false만으로 부분 송신이 없었다고 단정하지 않음 |
| `response_received`, `response_complete` | 응답 관측과 완전 수신을 구분 |
| `not_sent` | 원본 NotSent. 다른 불리언으로 역산하지 않음 |
| `audit_complete` | 최종 반환 Receipt의 감사 완결 상태. 세션 전체 완결 상태와 별도 |
| `status_code` | 대상 응답의 원본 코드, 없으면 null. 프록시가 만든 실패 코드를 넣지 않음 |

허용과 송신, 송신 시도와 완전 기록, 응답과 취약점 판정은 별개다.
`not_sent`는 현재 E②의 `!ForwardAttempted`와 일치해야 하고, `status_code`는 원본 세 자리(100–999)를 보존한다.
`reason=audit_failed`이면 `audit_complete=false`다. 반대 방향은 일반화하지 않는다. `Call.finish`의
result ACK 실패는 실제 wire 결과 `complete`를 다시 쓰지 않으므로 `complete + audit_complete=false`도
충실한 영수증 투영이다. 이 경우 세션 종료는 `incomplete`여야 한다([실제 코드](../../e2proxy/execution.go)).
`allowed + closed + not_sent`처럼 송신 전 중단된 조합도 원본대로 보존한다.
결과 불명을 `denied`, `not_sent=true`, 응답 없음 또는 0건으로 고쳐 적지 않는다.

### E② 시도 발급 전 거부

`ingress.rejected.layer`는 `application_http` / `proxy_transport`다.
사유는 Schema의 닫힌 enum이다. `crawl_write_blocked`는 read_only의 POST/PUT/PATCH,
`preflight_blocked`는 read_only OPTIONS, `dangerous_request`는 DELETE/위험 요청 guard다.
`scope_blocked`는 승인 밖 origin·경로·메서드의 로컬 차단이다. 외부 origin의 CONNECT는
`proxy_transport`로 E② Reject 전에 끝내며 아직 파싱하지 않은 내부 HTTP request를 생성하지 않는다.
`queue_full`·`queue_timeout`은 E② 진입 전 admission 실패다. 나머지는 `parse_failed`, `tls_failed`,
`unsupported`, `admission_closed`, `event_sink_failed`, `invalid_binding`이다.
Call이 닫혀 Receipt가 비어 있으면 `admission_closed`로 남긴다. 원본 오류 문자열·URL·헤더를 넣지 않는다.
이 사건은 E② 미실행이며, E② attempt 수나 계측기 송신 수를 증가시키지 않는다.

### 시작 프로필과 B08 조건

`session.started.test_environment_ref`·`reset_proof_ref`는 approved_writes에서 필수다.
호스트는 origin·회차·초기화·운영자 승인에 결속된 독립 기록과 대조한다. 문자열 자체는 승인 증거가 아니다.
`browser_profile`은 호스트가 실제 적용하고 확인한 쿠키 저장·SW 차단, 인증서 신뢰 종류,
`ca_sha256` 또는 정렬한 공개 SPKI 해시 목록의 canonical JSON SHA-256(`spki_list_sha256`),
인증서 오류 무시 여부, Chromium **플래그 이름 목록**과 `flags_profile_ref`다.
목록에는 `--name`만 넣고 `=값`·경로·토큰·URL·원문 argv는 저장하지 않는다. 전체 값은 검토된 고정 runtime
프로필로 결속하며 CA/SPKI 값은 지문으로만 남긴다. 이름 목록과 프로필이 다르면 시작 실패다.
제품 1단계는 전용 CA/정상 platform 신뢰만 허용하고, 오류 무시 플래그·SPKI 예외는 차단한다.
수용시험 B08 양성 대조의 동일 필드를 회차 결과에 남겨 신뢰 조건을 비교한다. 이 이벤트만으로
서로 다른 CA/SPKI 조건의 동등성이나 QUIC 차단을 증명하지 않는다. B10은 플래그 주입 부재도 확인한다.

## 4. 수명·순서·종료의 정합 조건

호스트는 한 세션의 seq/기록을 직렬화하되 **bounded group commit**을 사용한다. request 관찰은
해당 E② 호출 전에 영속 ACK를 받고, 결과는 호출 반환 뒤 기록한다. 여러 관찰을 한 fsync로 묶어도
각 사건의 seq·commit 참조와 ACK를 보존한다. 비동기 enqueue 성공을 durable ACK로 바꾸지 않는다.
ACK 실패/유실이면 해당 요청을 보내지 않고 fence한다. dispatch 후 기록 실패는 unknown/incomplete다.

구현 전 고정할 초기 성능 목표는 flush 간격 ≤10ms, batch ≤64건, 대기열 ≤256건,
단일 ACK deadline 250ms와 목표 p99 ≤100ms다(실측 아님). profile이 더 작은 양수 한도를 적용할 수 있다.
큐/ACK 대기 시간·fsync 지연·batch 크기·대기 만료 수·최대 backlog를 합성 부하 시험에서 측정한다.
목표를 넘으면 결과를 보고하고 프로필을 재검토한다. ACK deadline 실패는 target timeout이 아니다.
대기열은 승인/Call/세션 deadline 안에서만 기다리며, E② `Limits.RequestTimeout`의 wire 처리 예산과
별도로 잰다. 현재 workflow의 Call/프록시 60초가 E② 요청 기본 5초를 늘려 주지 않는다.
`limits_ref`가 queue/ACK·E② RequestTimeout·Call/프록시/세션 timeout 및 모든 중단 상한을 해석할 수 있어야 한다.
E② 자체 intent/result 감사 쓰기를 생략하거나 receipt보다 앞서 결과를 성공으로 기록하지 않는다. 이벤트 저장소가 E② 내부 잠금을 재진입하거나 권한을 바꾸지 않도록 분리한다.
동시 요청 간 시각·seq로 네트워크 인과 관계를 추측하지 않는다.

- ready 이후 `session.started` 한 번, 액션마다 started/finished 한 쌍, 정상/중지 경로에서 fenced/closed 각 한 번이다.
  opening 실패는 started/fenced 없이 `session.closed=failed` 또는 `incomplete`를 반환할 수 있다.
- request마다 observed 한 번, 이후 같은 request의 attempt.result 또는 ingress.rejected 중 하나가 종결한다.
  transport attempt에는 observed가 없을 수 있다. 실제 시도 한 개에 result 한 개만 허용한다.
- 한 Browser 세션의 call/command/scope/phase는 바뀌지 않는다. 다르면 폐기·중지한다.
  1단계 network 사건에는 action을 붙이지 않아 백그라운드 요청을 직전 액션에 오결속하지 않는다.
- 같은 event_id/seq의 정확한 재전달은 소비자가 중복 제거한다. 다른 내용, seq 역행·누락,
  중복 result, 알려지지 않은 request/attempt, 다른 실행의 참조는 정합 실패다.
- 저장 포화·감사 실패·조인 누락을 숨기려고 이벤트를 샘플링하거나 마지막 N개만 남기지 않는다.
  신규 송신을 막고 drain하며, 유실이 있으면 전체 기록을 incomplete로 표시한다.

`session.closed`의 다섯 플래그는 `process_drained`, `proxy_drained`, `audit_complete`,
`event_log_complete`, `host_correlation_complete`다. true는 호스트가 확인한 경우에만 쓴다.
false는 미완료 또는 미확인이며 대상 요청 0건을 뜻하지 않는다.
closed 안의 `event_log_complete`는 그 직전까지의 연속된 필수 사건 저장 상태다.
closed 자체의 저장 ACK까지 받은 뒤에만 최종 반환에서 전체 완결을 확정한다.
`CloseAndWait`의 보호된 반환은 `browser_session_ref`, `closed_event_id`, `last_seq`, `event_commit_ref`와
E② CallResult를 결속해야 한다. 이 ACK/종료 반환 없이 closed 레코드 하나만 보고 전체 기록 완료를 주장하지 않는다.

`counts`는 다음을 **세션 범위**로 센다. 측정 불가능한 값은 null이며 0으로 채우지 않는다.

| 건수 키 | 집계 기준 |
| --- | --- |
| `observed_requests` | 고유 request.observed |
| `attempt_results` | 고유 E② attempt.result, transport 포함 |
| `forward_attempted`, `request_written`, `responses_received` | 고유 Receipt의 대응 true 수 |
| `denied`, `unsupported` | 대응 decision 수 |
| `unknown` | 원본 Reason=unknown 수 |
| `local_write_blocked` | ingress.rejected의 crawl_write_blocked 수. E② denied와 별도 |
| `preflight_blocked` | ingress.rejected의 preflight_blocked 수. 쓰기 수에 넣지 않음 |
| `queue_rejected` | ingress.rejected의 queue_full/queue_timeout 수. 정책 위반 수에 넣지 않음 |
| `ingress_rejected` | E② 미발급 입구 거부 총수. transport 포함 |
| `application_attempt_results` | application_http인 E② result 수 |
| `application_ingress_rejected` | application_http인 ingress.rejected 수 |
| `ingress_by_reason` | 모든 입구 reason별 건수의 닫힌 객체. scope/dangerous/parse/TLS/unsupported/admission/저장/결속 실패도 누락 없이 보존 |
| `unresolved_requests` | observed 뒤 terminal 결과가 없는 요청 수 |
| `unresolved_attempts` | 보호된 E② Call attempt 수와 수집한 고유 result의 미대응 수 |
| `dropped_events` | 확인된 유실 수. 유실 규모 불명은 null |

위 건수는 서로 겹치는 축이므로 합산해 전체 요청 수로 만들지 않는다.
`completed`·`stopped`·`failed`에는 다섯 플래그 true, 모든 건수(사유별 건수 포함) non-null,
미대응·유실 0이 필요하다. 하나라도 확인되지 않으면 `incomplete`다. 호스트/소비자는 관측 건수 =
application attempt + application ingress + unresolved_requests, ingress 총수 = reason별 합계를 대조한다.
이 교차 건수의 산술 관계는 JSON Schema 밖의 스트림 검증이다.
기록된 completed를 최종 수용할 때는 위 closed 저장 ACK도 필요하다.
정상적으로 모두 정리된 budget/사용자 중지는 `stopped`, 시작/드라이버 실패는 `failed`,
정리·관찰 완결을 확인하지 못하면 다른 사유보다 `incomplete`가 우선한다.
종료 반환의 완결 확인은 독립 계측기와의 조인 완료나 탐색 전체 완료를 대신하지 않는다.
프로세스가 죽어 closed/ACK가 없으면 마지막 사건을 완료로 추정하지 않는다.

`exploration_limits`는 호스트가 확인한 `cookies_disabled`, `service_worker_blocked`,
`auth_not_prepared`, `encoded_response_unknown`만 넣는 중복 없는 배열이다. 마지막 값은 보호된 경계가
인코딩 응답임을 확인한 경우만 허용하며 일반 `unknown` 영수증에서 추론하지 않는다.
배열의 조건은 탐색 제약을 설명하며 실제 누락 개수나 진단 완료를 증명하지 않는다.
빈 배열은 **보고된 제한 없음**, 누락은 계약 오류다. UI가 건수에서 부분 탐색을 유도하지 않는다.

## 5. 최소 예시와 읽기/쓰기 해석

[합성 예시](examples/browser-events.v1.json)는 읽기 승인 세션의 GET 성공 1건·POST 로컬 차단 1건이다.
관찰 2, E② result 1, forward_attempted 1, request_written 1, response_received 1, local_write_blocked 1, denied 0이다.
POST는 발견했지만 송신하지 않았으며 독립 계측기에서는 GET의 AttemptID만 접수돼야 한다.
예시의 `completed`는 이 세션이 닫혔다는 뜻이며 보존 시험 통과 수치가 아니다.

[쓰기 승인 합성 예시](examples/browser-events.approved-writes.v1.json)는 같은 GET·POST 관찰에서 POST도
allowed이고, forward_attempted/request_written/response_received가 각각 2, denied가 0인 세션이다.
실제 수용에서는 관찰 → E② allowed → 실제 전송 플래그 → 독립 계측 접수로 확인한다.
권한·메서드·위험 제한에 걸린 POST와 DELETE는 여전히 거부할 수 있다.
전송된 쓰기 수는 POST/PUT/PATCH의 해당 전송 축으로 따로 센다. OPTIONS는 preflight로 별도 집계하고 쓰기로 세지 않는다.
로그인 수를 여기에 섞지 않으며, 향후 auth_prepare 계약에서 별도 단계와 합계를 정의한다.

## 6. 민감정보·입력 검증

원본 URL·query·header·body·Cookie·토큰·계정·selector·DOM·console·드라이버 오류·스크린샷을
이 이벤트에 넣지 않는다. 이름을 바꾼 자유 텍스트나 URL 해시로 우회하지 않는다.
endpoint 참조는 호스트 메모리의 실제 요청을 찾기 위한 것으로, 이벤트만으로 재전송할 수 없어야 한다.
저장·UI·LLM 투영은 기존 DLP를 별도로 통과시킨다. 원본 응답을 브라우저에 전달하는 경계와
공유 가능한 관찰의 경계는 다르다.

Schema의 `additionalProperties=false`와 kind별 필드를 검사한다. 중복 JSON 키는 파싱 전 거부하고,
크기·깊이·레코드 수 상한은 신뢰 limits 프로필로 적용한다. 참조/순서/교차 필드/실제 출처 검증은
JSON Schema만으로 끝나지 않으므로 호스트 및 소비자가 추가 검증한다.
스키마는 상태/사유·not_sent/송신·응답 플래그의 모순을 거부한다. UTC 시각은 엄격한 형태와
`format`을 함께 검사한다. `format`을 annotation으로만 처리하는 validator는 월별 일수까지 검사하지 않는다.
자동 형식 회귀 시험은 Python 3.10 이상에서 다음과 같이 실행한다. validator는 **시험 전용** 고정 의존성이며 제품 runtime에 설치하지 않는다.

```bash
python3 -m venv /tmp/browser-contract-tests
/tmp/browser-contract-tests/bin/python -m pip install -r scripts/eval/requirements-browser-contract.txt
/tmp/browser-contract-tests/bin/python -I -B -m unittest discover -s scripts/eval/browser_contract_tests
```

[CI 평가 job](../../.github/workflows/ci.yml)이 같은 명령을 실행한다. UTC 형태·소수초는 Schema 패턴으로, 달력과 시각은 Python 3.10의 `strptime`으로 검사하므로
3.11의 fromisoformat 확장에 의존하지 않는다.
Draft202012Validator와 FormatChecker에 실제 date-time 검사 함수를 등록해 의도적 모순·실제 예시를 검사한다.
소비자는 형식 검사가 실제 활성화됐는지 확인해야 하며, 미등록 format을 달력 검증 성공으로 취급하지 않는다.
이 형식 시험은 이벤트 출처·실제 참조·건수/seq 일치·durable ACK·독립 계측을 검증하지 않는다.
알 수 없는 버전·kind·enum·타입·참조는 성공으로 수용하지 않는다. 자격값이 섞인 원본은 오류 로그에도 출력하지 않는다.

## 7. 최민준 계측기와의 연결 제안

담당자 검토용 seam이며 이 문서에서 `cmd/eval-meter`를 수정하지 않는다.
현재 공식 `engineRow`는 path/query/header/body를 저장하지 않는다.
공식 회차의 기본 동작·형식은 유지하고, 보존 전용 실행에서만 별도 옵션·출력으로 마스킹한 경로를 기록한다.

현재 dev의 #372 및 후속 path-mask/v2 계약에 맞춘다.

| 저장 위치 | 필드 / 목적 |
| --- | --- |
| 러너의 회차 기록 | `run_ref`, `measurement_ref`, purpose·실행 조건. 개별 path 행에 복사하지 않음 |
| `engine-paths.jsonl` | `seq`, `method`, `path_pattern`, `masked_segments`, `path_profile_ref`, `path_key_ref` 여섯 필드 |
| 같은 seq의 `engine-requests.jsonl` | `attempt_id_state`, `attempt_id`, `sent`, `response`, `outcome`. path 행에 중복하지 않음 |

개선 후 조인 키는 **같은 회차의 원래 AttemptID**다. `request_ref → attempt_ref → meter.attempt_id`로 대조한다.
rule/path/시각/메서드·마스킹 결과가 같아도 같은 시도라고 추정하지 않는다.
`forward_attempted=true`인데 HTTP 계측 행이 없으면 연결 실패·TLS 실패·기록 누락을 구분해 대조하며,
송신 성공이나 차단 성공 어느 쪽으로도 자동 판정하지 않는다.
헤더 누락·중복·변조·orphan·예상하지 않은 접수는 원인과 건수를 남기며, 미관측을 도달 0으로 바꾸지 않는다.
차단된 시도가 독립 계측기에 접수되면 `sent=no`여도 차단 수용 실패다.
TLS 실패·preconnect의 TCP 접수, HTTP 접수, UDP 수신은 단위가 다르므로 따로 센다.

정본은 [Go 계측기](../../cmd/eval-meter/paths.go)의 `abc2lab.path-mask/v2`와
[고정 벡터](../../cmd/eval-meter/testdata/path-mask-v2.json)다. 새 Browser/Python 마스킹을 별도로 만들지 않는다.
집계용 [기존 Python 판](../../scripts/eval/path_mask.py)은 같은 벡터를 통과해야 한다.
숫자·인코딩 실패 등 값 조각은 `{id}`(정적 확장자 보존), 나머지도 원문 대신 같은 회차 묶음 키의 HMAC으로
가린다. 32조각 초과는 `/{too_long}`이고 해당 건수를 별도 보고한다. 애매하다는 이유로 전체 path를 null로
바꾸거나 원문 fallback하지 않는다. 키는 근거 디렉터리 밖에 두고 기록하지 않는다. `path_key_ref`가 같은
회차끼리만 비교한다. query·fragment·원문은 영속 기록 전에 제외한다.

계측기는 허용된 송신을, Browser 입구는 차단 포함 발견을 관찰한다. 이 두 집합의 분모를 섞지 않는다.
독립 계측 접수/송신 기준의 보존율·정규화·유효 회차·R-P/R-F/R-R 및 완료 판정은
[Browser 수용시험](../evaluation/browser-acceptance.md#6-3단계-보존-시험)과 그 §7만 따른다.
통제 점수·보존 점수를 합치거나 공식 비교표에 합산하지 않는다.

## 8. 계약 채택 전 확인

이동찬: producer 위치·보호 Receipt 수집·fence/drain·저장 ACK와 종료 반환의 결속.
최민준: 기존 AttemptID의 정확한 대조, 보존 전용 경로 프로필·일치 벡터·불명 집계.
이경준: 승인 설정/경고·부분 탐색·발견과 실제 전송 표시.
Schema와 합성 예시의 형식 검증은 실제 Browser·E②·계측기 통합 수용을 대신하지 않는다.
