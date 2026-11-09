# 첫 통합 진단 JSONL 운영 기록

첫 진단 CLI의 개발자용 관측 기록이다. E② 송신 허가, 영속 감사 정본, 증거 또는 확정 취약점 판정을 대신하지 않는다.
실제 OpenRouter Qwen3.5·실제 승인 대상 수용 시험은 별도다. 합성 HTTP/모델/DB 시험을 실제 진단 완료로 세지 않는다.

## 생성 시점과 위치

`prepare-task`, `prepare-intents`, `run`, `serve`, `report`는 유효한 보호 실행 설정을 읽은 직후,
DB·모델·실행 호스트를 열기 전에 필수 JSONL 파일을 자동 생성한다. 별도 디버그 명령이나 외부 로그 서비스는 없다.
`check`는 기존 읽기 전용 준비 검사 그대로이며 진단 실행 기록을 만들지 않는다.
설정 자체가 읽히지 않거나 파싱되지 않으면 신뢰할 저장 경로를 알 수 없으므로 기존 stderr 오류만 반환한다.
이 경우 임의 경로나 현재 작업 디렉터리로 대체하지 않는다.

저장 기준은 보호 실행 설정의 `data_dir`이다.

- task ID를 이미 아는 `prepare-intents`, `report`: `<data_dir>/diagnostic-logs/task-<실제 ID>/<명령>-<임의 128비트 파일 식별자>.jsonl`
- task ID가 아직 없는 `prepare-task`, `run`, `serve`: `<data_dir>/diagnostic-logs/preparation/<명령>-<임의 128비트 파일 식별자>.jsonl`

뒤의 파일 식별자는 파일 충돌 방지용이며 task/run/call 식별자가 아니다.
알게 된 실제 task ID를 이후 행에 결속하므로 preparation 디렉터리도 검색해야 한다. 경로를 나중에 옮기거나 기존 파일에 이어 쓰지 않는다.
같은 task라도 프로세스마다 별도 파일이고, 반복 명령은 새 파일이다. `run_ref`는 파일명에 사용하지 않는다.

`diagnostic-logs`와 그 하위 디렉터리는 실행 UID 소유 `0700`, 파일은 실행 UID 소유 `0600`이다.
상위 경로는 기존 보호 입력 계약처럼 신뢰 가능한 root/실행 UID 소유 및 쓰기 권한을 검사한다.
symlink, hardlink, 상대·탈출 경로, 저장소 `.git` 아래 및 일반 `web/public/www/wwwroot` 경로는 거부한다.
운영자는 그 밖의 사용자 지정 웹 공개 경로에도 `data_dir`을 두면 안 된다. 파일 서버 설정을 자동 탐지하지 않는다.
파일을 `O_EXCL`로 만들며 매 쓰기/마감에 열린 FD와 현재 경로의 소유·권한·동일성을 다시 검사한다.
권한이 틀린 기존 디렉터리를 자동 chmod하거나 기존 파일을 덮어쓰지 않는다.

## 형식과 연결

각 행은 독립 JSON 객체다. `version=1`, UTC `timestamp`, 파일 안의 증가하는 `sequence`,
`level`, `component`, `event`, 실제 단계에 해당하는 `stage/status/reason_code`를 기록한다.
행은 mutex 안에서 직렬화·쓰기·동기화되므로 여러 Worker가 같은 프로세스의 파일을 써도 섞이지 않는다.
시각은 벽시계이므로 같은 파일의 정확한 기록 순서는 `sequence`를 우선한다.

- `task_id`, `intent_id`: 실제 DB/런타임 숫자 ID. 존재하지 않으면 생략한다.
- `run_ref`, `call_ref`, `attempt_ref`: 실제 프로그램 참조의 SHA-256. 임의 문자열·비밀이 원문으로 남지 않는다.
- `model/provider`: 고정 허용 식별자만 원문. 임의 로컬 모델이나 승인된 provider 식별자는 별도 해시 참조다.
- `duration_ms`, 토큰·비용·건수: 실제 관측값. 원문 에러를 분석해 숫자를 만들지 않는다.
- `count_known`이 있는 집계는 0과 조회 불가를 구분한다. 조회 불가를 0으로 확정하지 않는다.

task로 여러 준비/실행/조회 파일을 묶고, run/call/attempt 해시와 intent ID로 상세 흐름을 찾는다.
동일 task의 준비 단계는 run을 아직 모를 수 있다. 서버가 신뢰된 실행을 결속하기 전까지 run을 생성하지 않는다.
서로 다른 프로세스의 sequence는 서로 비교하지 않는다. task와 UTC 시각, 실제 참조를 함께 쓴다.

## 이벤트와 읽는 순서

1. `stage_started/stage_finished`: 설정, 보호 파일, 모델 프로필, DB 연결/권한, task 생성/결속, 호스트/정책, 출력/마감.
2. `planner_started/planner_finished`, `planner_session_finished`: 실제 계획과 생성 intent 수, 모델·턴·시간 한도, 실행 계획 부재.
3. `model_call_started/finished`: llmrec의 실제 호출·역할·보고된 토큰. 수집기를 소비하지 않으면 호출 이벤트도 만들지 않는다.
4. `model_request_started/finished/denied`: OpenRouter 실제 요청, 예약 입력/출력 토큰·비용, 보고 비용·불확실성, 사전 정책 차단.
5. `worker_started/finished`, `worker_session_finished`, `tool_selected`, `tool_schema_checked`, `tool_started/finished`, `tool_result_observed`: 등록 도구·schema·실행 결과. 미등록 도구 이름은 기록하지 않는다.
6. `request_approval_pending/resolved/decided`: 실제 영속 승인 상태. `allowed`는 승인 결정이며 송신 완료가 아니다.
7. `http_dispatch_authorized`, `http_forward_attempted`, `http_response_received`, `http_finished`, `audit_event_recorded/failed`: E②의 허가·시도·응답·불명·감사 결과. 취약점 확인이 아니다.
8. `candidate_submitted/validated/saved/duplicate`: 후보 증거 참조 검증과 DB 결과. 일반 후보는 `manual_review_required`다.
9. `result_save_started/finished`, `run_summary`, `run_finished`, `diagnosis_finished`: 실행·감사·저장 마감과 실제 종료 단계.
10. `service_request_finished`, `report_queried`: 보고서 서비스의 HTTP 응답 범주와 실제 저장 결과 조회. URL이나 본문을 기록하지 않는다.

`run_summary`는 일반 HTTP의 기존 승인/실행 상태와 OpenRouterUsage를 재사용한다.
실제 송신 수, 허용/거절/차단 수, 후보 수와 감사·drain·결과 저장 확인을 구분한다.
`registered_get`은 별도 기존 E③ 결과 확인·마감 단계를 사용하며 일반 HTTP 집계를 합성하지 않는다.
`diagnosis_finished`의 원인 단계는 cleanup 성공 때문에 지워지지 않는다. 원래 실행 실패와 별도의 마감 실패 행을 함께 확인한다.

## 대표적인 실패 분류

| 코드 | 의미 및 다음 확인 |
|---|---|
| `database_unavailable`, `database_role_invalid` | 연결 또는 최소 역할 검사. DSN은 별도 보호 설정에서 확인 |
| `model_profile_invalid` | 승인 프로필/manifest 결속 실패 |
| `http_unauthorized`, `http_rate_limited`, `http_server_error`, `http_client_error` | 모델 endpoint HTTP 상태 범주. 외부 오류 본문은 없음 |
| `timeout`, `transport_failure`, `response_invalid` | 시한, DNS/TLS/연결 등 전송 실패, 응답 형식 실패. 세부 raw 오류는 없음 |
| `token_limit`, `cost_limit`, `request_limit`, `expired`, `approval_consumed` | 기존 모델 예산/승인 정책의 사전 차단. 추가 요청이나 자동 재시도 없음 |
| `no_intents`, `planner_failed`, `max_turns` | 계획 부재 또는 Planner 종료. 생성 intent 수와 모델 이벤트 대조 |
| `unregistered_tool`, `schema_invalid` | 미등록 도구 또는 모델 입력 schema 불일치 |
| `scope_denied`, `policy_denied`, `approval_denied`, `approval_expired` | E② 정책/사용자 결정/유효기간. 실제 송신과 구분 |
| `result_unknown`, `e2_http_execution_result_unknown` | 송신 결과를 확인 못함. 성공/미전송으로 추정하지 않음 |
| `candidate_store_failed`, `result_storage_failed` | 후보 DB 저장 또는 최종 결과 저장 실패 |
| `audit_failed`, `drain_failed` | 감사·실행 마감 실패. 로깅 장애와 다름 |
| `user_cancelled`, `cancelled` | 사용자/문맥 취소. 일부 요청이 이미 나갔는지는 E② 감사 확인 |

대상이 HTTP 4xx/5xx를 반환한 것은 응답을 받은 사실이며 모델 HTTP 오류나 정책 거절과 다르다.
모델 비용 누락/응답 형식 문제는 `cost_uncertain` 등 기존 회계의 불확실성에 맞춰 기록한다.
현재 transport 실패는 DNS/TLS/connect를 한 범주로 묶는다. 원문 에러 문자열을 복사해 과도하게 세분화하지 않는다.

## 로깅 장애와 종료 코드

- 파일 생성/필수 초기 행 실패: stderr `diagnostic_logging_unavailable`, 종료 1. DB·모델·대상 실행 전에 중지한다.
- 실행 중 쓰기/Sync/권한·경로 변조/한도 실패: sticky 오류와 즉시 한 번의 안전한 stderr 경고. 기존 E② 감사·실행 마감은 그대로 수행하되 CLI 성공 stdout을 출력하지 않는다.
- stderr `diagnostic_logging_incomplete:log_storage_failed|log_limit|log_invalid_event|log_closed`, 종료 1.
- 관측용 집계 조회 실패 등: 원래 실행 결과를 변경하지 않고 `incomplete`/`observation_complete=false`를 남기며 CLI `diagnostic_observation_incomplete`, 종료 1.

로깅 오류를 E② 감사 실패로 바꾸지 않으며 로그가 송신 허가를 만들지도 않는다.
이미 승인된 요청이 로그 오류 때문에 감사 정본에서 취소/미전송으로 바뀌지 않는다.
저장된 기존 실행 결과는 실행/감사의 결과이며 관측 파일의 완전성을 증명하지 않는다.
마지막 행이 있어도 후속 Sync/Close 실패가 가능하므로 CLI 종료 코드/stderr와 함께 확인한다.
갑작스러운 SIGKILL/전원 손실은 terminal 행이 없을 수 있다. terminal 부재를 완료로 해석하지 않는다.
쓰기 일부 실패는 이전 유효 줄까지 복구를 시도하지만, 저장 장치 자체 장애까지 완전 복구를 보증하지 않는다.

## 운영자 확인 예시

승인된 로컬 실행 UID로만 읽는다. 로그 경로를 stdout JSON/API/UI 또는 공유 문서에 자동 노출하지 않는다.
다음 명령에서 경로와 task 숫자는 운영자가 실제 보호 설정/prepare-task 응답에서 선택한다.

```sh
find /approved/private/data/diagnostic-logs -type f -name '*.jsonl' -print
jq -c 'select(.task_id == 42) | {timestamp,sequence,event,stage,status,reason_code,intent_id,call_ref}' /approved/private/data/diagnostic-logs/preparation/run-*.jsonl
jq -c 'select(.status == "failed" or .status == "timeout" or .status == "incomplete" or .status == "unknown")' /approved/private/data/diagnostic-logs/task-42/*.jsonl
```

로그 자체를 외부로 보내거나 공개 디렉터리로 복사하지 않는다. 사용자 지정 path가 안전한지는 운영자가 확인한다.
개발 검증은 요청·응답·키·비밀번호·DSN·명령·모델 prompt/response sentinel이 파일에 없는지 자동 검사한다.
허용 enum/수치/해시 참조만 직렬화하며 DLP 문자열 치환을 안전성의 근거로 삼지 않는다.

## 용량·보존

파일별 상한은 16 MiB 및 20,000행이다. 먼저 도달한 상한에서 실패 상태가 고정된다.
새 프로세스는 새 파일이므로 총 디스크 사용량에는 별도 운영 관리가 필요하다. 자동 삭제·압축·업로드는 없다.
운영 기본 권고는 종료와 분석이 확인된 진단 로그만 7일 보관한 뒤 로컬 운영자가 정리하는 것이다.
조사·사고 보존이 필요하면 기관 정책을 우선한다. 실행 중 파일을 삭제/교체하거나 다른 UID에 읽기 권한을 주지 않는다.
정리 대상은 이 `diagnostic-logs` 아래의 종료된 개발자용 JSONL에 한정한다.
E② 감사 저널, 체크포인트, 증거, 결과 DB, 원래 보호 설정은 용량 정리 대상으로 삭제하지 않는다.
