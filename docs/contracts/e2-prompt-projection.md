# E② 프롬프트 평가 Projection v1 — 확정 제안

기준 dev: `5aa792f6fed76c08cba2141298ae7e826aed21d0`. 담당 리뷰: 최민준. 계약·서버 생산 경로와 합성 시험을 제안하며, 러너·채점기 연결이나 공식 평가 완료를 의미하지 않는다.

## 데이터 출처와 신뢰 경계

`agent/e2_http_prompt.go:buildE2HTTPWorkerPrompt`는 실제 선택한 `e2_worker` 진단 지침, 코드 통제, 도구 구성의 식별값을 만든다. `agent/e2_http.go:executeE2HTTP`는 intent를 붙인 뒤 기존 `version=e2-worker-prompt/v1`, `key=e2_worker`를 포함한 관측을 만든다. 기록 시점은 **Worker system prompt 구성 후, 모델 세션 시작 전**이다. 이 기록만으로 제공자가 프롬프트를 수신했거나 진단이 성공했다고 증명하지 않는다.

기존 `emitE2HTTPPromptActivity` → Engine → `ExplorationStore.AppendActivity` 경로는 저장 결과를 Worker에 돌려주지 않았고 `Activity`에 run 결속도 없었다. `Kind=e2_prompt`·`Tool=E2HTTP` 문자열만으로 출처를 신뢰하거나 일반 Activity 목록의 첫 페이지를 전체 관측으로 취급하지 않는다.

이번 생산 경로는 기존 파일 정책 factory가 만드는 `E2HTTPRuntimeOptions.PromptProjection=true`에서 활성화한다.

1. `PrepareWork`는 실제 Manager task, DB exploration/intent/payload, supervisor, 실제 획득한 `e2proxy.Execution`의 `Scope.TaskRef`·`Scope.RunRef`·등록 intent를 기존 검사로 확인한다.
2. 그 뒤 기존 실행의 **실제 `Scope.RunRef`**로 `e2_prompt_runs`를 단회 등록한다. 별도 문자열을 조립해 실행 권위를 만들지 않는다. task ID는 run ID를 대신하지 않는다. 등록은 task/exploration의 실제 DB 관계를 재검사한다. run_ref 재사용은 거부하고 기존 원장도 `failed`로 표시해 과거 완료값의 재사용을 막는다.
3. Worker 호출마다 원장에 기대 관측을 먼저 등록한다. intent 재실행도 새 `sequence`를 받는다. 재사용되는 `work#N` 슬롯은 관측의 고유 ID가 아니다.

   완전성의 모집단은 실제 `PrepareWork`로 준비된 Worker 호출이다. 아직 Worker에 전달되지 않은 Planner 후보, 모델이 실제로 받은 요청 수, 진단 범위 전체를 이 건수로 나타내지 않는다. 등록·저장은 각각 최대 2초로 제한한다.
4. 신뢰 Go context에만 설치한 `ObservePrompt` callback이 실제 Worker 관측을 받는다. `SaveE2PromptActivity`는 Detail을 엄격하게 검사한 뒤 Activity와 원장의 `activity_id` 연결을 **같은 트랜잭션**에 쓴다. 일반 Activity insert나 모델 도구는 이 callback을 받지 못한다. callback이 있는 경로에서는 일반 emitter로 같은 prompt를 중복 저장하지 않는다.
5. 기대 등록·저장 실패는 고정 서버 로그와 실패 latch로 남긴다. 실행·E② 송신/감사 판정을 바꾸지 않으며, 실패를 정상 0건으로 바꾸지 않는다. 이미 등록된 기대 관측에 Activity가 없으면 누락이 된다.
6. 실제 Worker finish·Runner drain·Execution Stop·release를 마친 기존 runtime 마감 경로에서만 원장을 봉인한다. 실패·프로세스 재시작·마감 저장 실패는 `complete`를 만들지 않는다. 재시작 후 미봉인 회차를 task의 terminal 상태만 보고 봉인하지 않는다.

이 provenance는 **신뢰 실행 프로세스와 PostgreSQL 권한 경계**를 전제로 한다. DB 관리자나 이 두 테이블의 쓰기 권한 보유자가 악의적으로 바꾼 자료를 암호학적으로 인증하지 않는다. 평가 러너·모델·도구에 DB 쓰기 권한을 주지 않는다. 서명된 러너 기록의 출처 증명은 후속 평가 담당 작업이다.

독립 `E2StandaloneBackend.Execute`는 명령 단위 경로이고 Worker 프롬프트를 만들지 않으므로 이 projection의 생산자가 아니다. 임의 `eval_run_id`·reservation 문자열을 새로 받아 기존 E② run_ref와 같다고 선언하는 API도 추가하지 않는다. 평가 러너는 기존 신뢰 회차 결속에서 **실제 task ID와 E② run_ref**를 획득·대조해야 한다. 아직 미구현인 독립 평가 서비스의 회차/task 등록을 이 경로로 대체하지 않는다.

## 조회 인터페이스

`GET /api/tasks/{id}/e2-prompt-projection?run_ref=<실제 E② Scope.RunRef>`

- 기존 서버에 추가한 읽기 전용 경로다. 별도 공개 listener·무인증 API는 없다.
- 기존 설치 JWT를 `Authorization: Bearer …` 헤더로 보낸다. 정확히 하나의 헤더, HS256·만료·`sub=ARTEX`를 확인한다. query token/cookie만으로 이 경로를 호출할 수 없다. 기존 제품은 단일 설치 관리자 구조이므로 `ARTEX`가 설치 내 task를 조회한다. 다중 사용자 소유권 분리를 구현했다고 주장하지 않는다.
- canonical 양수 task ID, 실제 Manager task와 Store exploration, DB task/exploration/run 결속을 재검사한다. 다른 task/run의 자료는 반환하지 않는다.
- body 없는 GET, query는 정확히 하나의 `run_ref`만 허용한다. run_ref는 `[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}`. Authorization 최대 8192바이트, query 최대 256바이트, 조회 시한 5초, 응답 최대 8MiB다.
- 성공 JSON은 `Cache-Control: no-store`. 인증 실패 401, 잘못된 입력 400, 없는 task 404, 없는/다른 run·DB 장애·조회 불명은 503의 고정 한국어 오류다. 503을 빈 관측으로 해석하지 않는다. 원문 DB 오류·경로를 응답하지 않는다.
- 보호된 평가 러너가 내부 서버 API에 인증해 수집한다. 기존 관리 UI bridge의 허용 경로는 확장하지 않았다. 독립 프로세스 배치의 인증 identity·서명 기록 연계는 평가/E② 후속 계약이다.

내부 서비스 함수는 `ExplorationStore.E2PromptProjection(ctx, taskID, runRef)`다. HTTP와 같은 DB 검사와 allowlist 투영을 사용하며, 호출자는 설치 권한을 확인한 신뢰 Go 코드여야 한다.

## 닫힌 JSON 형식

`format=abc2lab.e2-prompt-projection/v1`. 아래 모든 키가 필수이고 **생략·null은 없다**. 미지 필드, 중복 JSON 키, 잘못된 UTF-8·타입·버전·범위를 소비자가 거부한다. v1의 의미/필수 키를 바꾸려면 새 format 버전과 담당 리뷰가 필요하다.

| 필드 | 타입·범위 | 의미 |
|---|---|---|
| `format` | 고정 문자열 | projection 형식 |
| `task_id` | 양수 int64 | 실제 PostgreSQL task PK |
| `exploration_id` | 양수 int64 | 해당 task의 실제 exploration PK |
| `run_ref` | 위 참조 문자열 | 실제 E② `Scope.RunRef`, 평가 `run_id`와 별개 |
| `state` | 아래 고정 문자열 | 수집·동등성 검사 상태 |
| `complete` | bool | `state=complete`일 때만 true |
| `expected_count` | 정수 0…10000 | PrepareWork에서 등록한 기대 관측 수 |
| `observed_count` | 정수 0…expected_count | Activity 연결이 존재하는 수. 잘못된 Detail도 연결 건수에는 포함 |
| `snapshot_sequence` | 정수, expected_count와 같음 | 이 snapshot의 마지막 기대 순번(빈 원장 0) |
| `observations` | 아래 객체 배열 | sequence 오름차순의 전체 관측. 부분 페이지가 아님 |

| 관측 필드 | 타입·범위 |
|---|---|
| `sequence` | 1부터 공백 없이 증가하는 int64 |
| `activity_id` | 양수 int64, projection 안에서 중복 금지 |
| `intent_id` | 같은 exploration의 실제 intent PK, 양수 int64 |
| `worker` | `work#[1-9][0-9]{0,5}`; 실제 Worker 슬롯 이름 |
| `version` | `e2-worker-prompt/v1` |
| `key` | `e2_worker` |
| `source` | `builtin`, `override`, `builtin_invalid_override` 중 하나 |
| `template_digest`, `guidance_digest`, `control_digest`, `tools_digest` | 각각 소문자 SHA-256 hex 64자리 |

`system_digest`는 저장 Detail의 닫힌 8필드 원본 형식 검사에서만 확인하고 응답에서 제외한다. intent마다 달라질 수 있으므로 비교하지 않는다. 관측 생성 시 원문은 복사하지 않는다. system/템플릿/user/intent 원문, API 키·세션·쿠키·HTTP 자료, 명령/도구 출력, 파일 경로, 원시 오류, 일반 활동 Summary/Metadata는 투영하지 않는다.

## 완전성과 상태

조회는 **Repeatable Read 트랜잭션 하나와 run 행 공유 잠금**으로 봉인 상태·기대 순번·전체 원장/Activity를 함께 읽는다. 저장·봉인은 같은 run 행을 갱신 잠금으로 직렬화한다. 일반 activity 페이지·LIMIT 100·최신 행 선택을 사용하지 않는다. 최대 10000건을 한 번에 읽고 순번 공백·중복·건수·Activity 출처/노드 결속을 검사한다. 상한 초과나 timeout은 전체 실패다.

| state | complete | 의미와 소비 규칙 |
|---|---|---|
| `collecting` | false | 미봉인. 일부 정상 행이 있어도 비교 입력으로 채택하지 않음 |
| `incomplete` | false | 저장 실패 latch, 누락 Activity, 실패 마감, run_ref 재사용. 비교하지 않음 |
| `empty` | false | 봉인됐지만 기대/관측 0건. 동일 프롬프트 판정 아님 |
| `invalid` | false | Detail·순번·출처·intent 결속 오류. 전체 observations를 비우되 건수는 보존; 남은 행만 비교하지 않음 |
| `conflict` | false | 관측은 모두 수집됐지만 다섯 식별값 중 하나 이상 상충. **전체 정상 행을 보존**하고 최신값/다수결을 선택하지 않음 |
| `complete` | true | 봉인·저장·결속·순번·건수 검사 통과, 1건 이상이며 모든 식별값이 동일 |

존재하지 않는 원장·DB 조회 오류는 projection 객체가 아니라 error다. 과거에 일반 emitter로만 저장한 활동을 소급해 신뢰 관측으로 편입하지 않는다. `complete=false`나 HTTP 오류를 정상 0건·같은 조건·검사 완료로 바꾸지 않는다.

## 평가 담당 소비 순서

1. 실제 회차의 task/exploration/E② run_ref를 인증된 실행 결속과 대조한다. task ID나 평가 run_id로 E② run_ref를 추정하지 않는다.
2. 기존 E② 실행 마감 ACK 뒤 위 endpoint를 호출한다. `collecting`이면 bounded retry할 수 있지만 시한을 연장하거나 task 종료만으로 완료를 추정하지 않는다.
3. 닫힌 format/키/타입/식별·`complete=true`, `state=complete`, expected=observed=snapshot_sequence=len(observations)>0, sequence 1…N, activity_id 중복 없음과 각 observation의 version/key/worker/digest를 검사한다. 누락된 페이지를 별도로 합치는 방식은 없다.
4. 전체 observations를 그대로 기존 `run_prompt_identity()`에 넘긴다. 조건의 모든 회차를 `condition_prompt_identity()`에 넘긴다. 다섯 값은 `source`, `template_digest`, `guidance_digest`, `control_digest`, `tools_digest`다. `system_digest`는 없다.
5. 회차 내 conflict, 조건 간 mismatch, 0건·비수집·불명은 비교 성공이 아니다. 이것을 회차 무효/조건 보류 중 어디에 반영할지는 최민준의 평가 정책으로 정한다. 이 PR은 채점을 바꾸지 않는다.

**서명 기록 변경 대기:** 기존 `run-record/v1.1`의 닫힌 artifact descriptor와 필드를 변경하지 않았다. 러너의 인증 수집 시점, 받은 JSON 정확 바이트 digest·건수·format·task/exploration/run 결속을 새 artifact descriptor/서명 문맥에 포함하는 버전 개정은 최민준 리뷰가 필요하다. `runner.py`, `runner_record.py`, `run_record.py`, `score.py`, `e2_prompt_identity.py`는 그대로다. 일반 JSON 파일을 저장한 것만으로 기존 서명이 projection을 덮는다고 주장하지 않는다.

## 재현 시험과 인계

- `db/TestE2PromptProjectionPostgres`: 실제 PostgreSQL의 1건·205건·복수 관측 상충·system_digest만 변경·누락·미봉인·실패 마감·잘못된 Detail·순번 누락·다른 결속·중복·Activity 저장 실패·0건·일반 활동 위조·run 재사용·취소된 조회를 검사한다.
- `server/TestE2PromptProjectionWorkerPostgresPython`: 실제 factory→runtime→Worker(합성 provider)→Activity/원장→마감→인증 API→Go JSON→기존 Python 비교기 경로다. 두 Worker와 조건 간 불일치, 무인증/다른 subject·run·미지 query·중복 query·크기 제한을 확인한다. 실제 모델·대상 송신은 없다.
- `e2prompt/TestParseStrictDetail`: 버전/key/source/필수/digest/타입/null/미지·중복 키·trailing/UTF-8 오류를 검사한다. PostgreSQL 설정이 없으면 DB 시험은 SKIP이며 PASS로 세지 않는다.

운영 DB는 설치자가 schema를 적용하고 신뢰 실행 role에 `e2_prompt_runs`, `e2_prompt_observations`의 SELECT/INSERT/UPDATE를 추가로 부여해야 한다. Activity/노드/task의 기존 읽기·Activity 쓰기도 필요하다. 평가 러너는 HTTP 읽기만 사용한다. 모델·도구·일반 외부 역할에는 원장 쓰기 권한을 주지 않는다. 권한 미설정은 projection 불명으로 남는다. 독립 eval-e2 이미지·OpenCall/CloseCall·정책 생산자·공식 러너/채점 연결·실제 평가 회차는 이번 구현 범위 밖이다.
