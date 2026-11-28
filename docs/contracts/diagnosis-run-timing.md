# 점검 시작·마감 확인 시각

점검 진행·보고서의 시작·경과·소요 표시에 사용할 **서버 확인 시각**과, 남은 시간 표시에 쓸 **서버 집행 시간 상한**(§시간 상한)이다. 준비 Job과 [제품 목록](product-run-list.md)에 같은 저장 값을 제공한다. 시각은 실행 권한·성공·취약점 판정이 아니다.

## 공개 필드

| 필드 | 형식 | 저장 시점 |
|---|---|---|
| `started_at` | 선택 RFC3339 UTC 문자열 | 보호 준비 서비스가 `started: false → true`를 처음 확인해 저장할 때 |
| `finished_at` | 선택 RFC3339 UTC 문자열 | 보호 준비 서비스가 `drain_complete: false → true`를 처음 확인해 저장할 때 |

대상 응답은 기존 `diagnosisprep.Job`을 포함하는 URL v2 초안/상태 응답과 `GET /api/diagnoses/v2/runs`의 각 행이다. Job을 사용하는 기존 v1 응답도 같은 선택 필드를 받는다. 두 시각 필드는 독립적이며 모르면 **키를 생략**한다. null·빈 문자열·0시각을 만들지 않는다. 공개 HTTP 출력과 웹 파서의 형식은 대문자 `T`·`Z`로 끝나는 RFC3339 UTC이며, 소수 초는 없거나 1~9자리다. 웹은 `+00:00`을 포함한 offset 표현·잘못된 달력 날짜·윤초를 거부한다. 이 문자열 계약으로 보호 IPC·저널의 Go `time.Time` 디코더 동작까지 새로 변경하는 것은 아니다.

```json
{
  "state": "running",
  "started": true,
  "drain_complete": false,
  "result_ready": false,
  "started_at": "2026-11-17T01:02:03.123456789Z"
}
```

```json
{
  "state": "completed",
  "started": true,
  "drain_complete": true,
  "result_ready": true,
  "started_at": "2026-11-17T01:02:03.123456789Z",
  "finished_at": "2026-11-17T01:07:03.123456789Z"
}
```

위 JSON은 해당 응답의 시각 관련 필드 발췌이며 ID·revision 등 기존 필드는 생략했다.

## 확인·영속성

- 브라우저·모델·외부 HTTP 요청이나 Backend.Update는 시각을 지정할 수 없다. 서비스의 신뢰된 프로세스 시계를 UTC로 변환해 해당 사실과 **같은 보호 저널 영수증**에 저장한다. 저널 쓰기 실패 시 새 시각을 성공 응답으로 게시하지 않는다.
- 엔진 시작 요청 시각·초안 생성·준비 진입·Task 생성 시각을 started_at으로 쓰지 않는다. 실행 backend가 시작 사실을 Publish하고 준비 서비스가 확인한 시각이다. backend와 서비스 사이의 전달 지연을 역산하지 않는다.
- finished_at은 drain 확인 시각이다. 보고서 저장 시각 saved_at이나 Backend.Execute 반환 시각이 아니며, 그 둘로 대체하지 않는다. drain 확인과 결과 저장·모델 사용량 확인은 독립이다.
- 중복 Publish·상태 조회·멱등 시작 재조회·중지·일시정지/재개는 기록한 시각을 다시 쓰지 않는다. 보호 저널 복원·압축도 원값을 보존한다. 과거 started/drain이 이미 true인데 시각이 없으면 재기동이나 후속 갱신 때 채우지 않는다.
- 결과 불명에서도 **이미 확인·저장된 시각은 유지하고 모르는 시각만 생략**한다(2026-11-17 사용자 확인). state=unknown을 미실행·실행 중·정상 완료로 바꾸지 않는다.
- 시작 전에 준비가 실패했지만 drain이 확인된 회차는 finished_at만 있을 수 있다. 구 started=true 기록에 시각이 없고 이후 drain이 확인돼도 finished_at만 있을 수 있다.
- 시계 값이 RFC3339 범위를 벗어나거나 0이면 해당 전환의 시각은 생략한다. 벽시계 역행으로 종료 시각이 저장된 시작보다 앞서면 finished_at을 생략한다. 시간을 보정해 만들거나 기존 마감 사실·실행 결과를 실패로 바꾸지 않는다.
- 형식·UTC·known fact 결속·시각 순서를 Job/목록 검증에서 동일하게 검사한다. 이미 저장된 시각의 변경·삭제·과거 누락의 사후 보충은 저널 연속성 검증에서 거부한다.

## 화면 인계

1. 서버 started_at을 진행 표시의 기준으로 사용한다. 새 브라우저·새로고침·재연결에서 자체 시작 시각이나 새 스톱워치를 만들지 않는다.
2. 두 시각이 모두 있으면 `소요 = finished_at − started_at`이다. 예시의 소요는 5분이다. 준비 요청부터의 시간이나 일시정지를 뺀 순수 실행 시간이 아니라 시작 확인부터 drain 확인까지의 벽시계 간격이다.
3. 하나라도 없으면 전체 소요를 추정하지 않는다. 특히 구 보고서의 saved_at을 종료 시각 또는 시작 시각의 대용으로 쓰지 않는다.
4. unknown의 finished_at 부재를 현재도 실행 중이라는 근거로 쓰지 않는다. 기존 상태·오류·마감 표시를 함께 유지한다.
5. 남은 시간은 아래 §시간 상한의 서버 값으로만 센다. `started_at + duration_seconds` 같은 추정, 전체 진행률·ETA는 만들지 않는다. 정렬·소유권·페이지·보고서 제공 여부도 기존 계약 그대로다.

## 시간 상한(2026-11-27)

`started_at`은 시작 확인 시각이고 실행 시간 상한의 기준이 아니다. 실제 상한은 실행 backend가 실행을 받을 때 정한 실행 문맥의 기한이고, 모델 단계 마감은 그 기한에서 확정 단계 몫·유예·정리 여유를 뺀 시각이다. 그래서 `started_at + duration_seconds`는 실제 상한과 다르다. 화면이 스스로 추정하지 않도록 backend가 감독자에 넣은 **바로 그 값**을 상태 응답에 싣는다.

### 필드(URL v2 초안/상태 응답)

| 필드 | 형식 | 뜻 |
|---|---|---|
| `draft.time_limit` | 선택 객체(아래 다섯 칸, 모르는 칸 없음) | backend가 집행하는 시간 상한 |
| `time_limit.run_deadline_at` | RFC3339 UTC | 실행 시간 상한. 이 시각에 실행 문맥을 끊는다(그 뒤 마감 정리는 별도 유예) |
| `time_limit.model_close_at` | RFC3339 UTC | 모델 단계가 새 계획·새 intent를 멈추고 `budget_limited` 마감을 시작하는 시각 |
| `time_limit.model_drain_ms` | 0 이상 정수 | 모델 마감 시작 뒤 진행 중 작업을 마무리하게 두는 최대 시간(넘으면 멈춤) |
| `time_limit.confirmation_reserve_ms` | 0 이상 정수 | E③ 확정 단계에 남겨 둔 시간 몫. 확정 단계가 없는 회차는 0 |
| `time_limit.shutdown_margin_ms` | 0 이상 정수 | 실행 상한 안에서 결과 저장·마감에 남긴 여유 |
| 응답 최상위 `server_time` | RFC3339 UTC | 응답을 만든 서버 시각. `time_limit`이 있는 응답에만, 그때는 반드시 있다 |

- 식 `model_close_at + model_drain_ms + confirmation_reserve_ms + shutdown_margin_ms = run_deadline_at`이 나노초까지 정확히 성립한다. 각 몫은 하루를 넘지 않는다. 서버·웹 파서 모두 식이 틀리면 거부한다.
- 확정 단계는 모델 단계가 끝나면(목표 달성·시간 상한·예산 소진) 시작하므로 정해진 시작 시각이 없다. 시간 상한까지 간 회차는 `model_close_at + model_drain_ms` 무렵(진행 중 작업을 멈춘 뒤) 시작한다.
- `GET /api/diagnoses/v2/runs` 목록 행에는 싣지 않는다.

```json
{
  "version": "diagnosis-api.v2",
  "owner_context": "…",
  "draft": {
    "state": "running",
    "started": true,
    "started_at": "2026-11-17T01:02:03.123456789Z",
    "time_limit": {
      "run_deadline_at": "2026-11-17T01:12:00Z",
      "model_close_at": "2026-11-17T01:08:10Z",
      "model_drain_ms": 90000,
      "confirmation_reserve_ms": 120000,
      "shutdown_margin_ms": 20000
    }
  },
  "server_time": "2026-11-17T01:05:03Z"
}
```

### 기록 규칙

- backend가 시간 상한을 감독자에 넣은 직후(Gateway 준비, 엔진 Start 전) 한 번 게시한다. 게시하지 못하면 상태와 집행이 어긋나므로 시작하지 않는다. 밀리초로 정확히 나타낼 수 없거나 음수인 몫은 만들지 않는다.
- 준비 서비스는 처음 받은 값을 보호 저널에 저장하고 바꾸지 않는다. 같은 값 재게시는 revision을 바꾸지 않고, 다른 값은 프로토콜 오류로 거부한다. 지우기·바꾸기·시작 확인 뒤 사후 보충은 저널 연속성 검증에서도 거부한다.
- **일시정지·재개는 상한을 바꾸지 않는다.** 실행 시간 상한은 워크플로 일시정지 중에도 흐른다. 새로고침·재연결·서비스 재기동도 같은 값을 돌려준다.

### 상태별 응답

| 상태 | `time_limit` |
|---|---|
| `awaiting_approval`·`ready`, 상한을 정하기 전 `preparing`(준비 중 실패 포함) | 없음 |
| 상한을 정한 뒤 `preparing`·`running`·`stopping` | 있음 |
| `completed`·`cancelled`·`failed`·`unknown` | 정해진 적이 있으면 그 값 그대로(기록), 없으면 없음 |
| 이 칸이 생기기 전의 구 기록 | 없음(채우지 않음) |

없으면 서버가 상한을 모르는 것이다. 화면은 남은 시간을 표시하지 않고 `started_at + duration_seconds`로 대신 계산하지 않는다.

### 화면 계산

1. 응답을 받은 순간 `offset = server_time − 브라우저 now`를 잡는다. 그 뒤 `서버 지금 = 브라우저 now + offset`이다.
2. `running`·`stopping`에서만 남은 시간을 센다: 실행 남은 시간 = `run_deadline_at − 서버 지금`, 모델 단계 남은 시간 = `model_close_at − 서버 지금`. 음수면 0으로 둔다(마감 처리 중).
3. 끝난 상태·`unknown`에서는 남은 시간을 표시하지 않는다. `unknown`의 상한 경과를 실행 중이라는 근거로 쓰지 않는다.
4. `server_time`은 비교용 시계일 뿐 Job 정본이 아니다. 폴링마다 바뀌므로 Job 비교(같은 revision 확인)에 넣지 않는다. 웹은 `parseURLServerTime`으로 따로 읽는다.

공유 예시의 `running_with_time_limit`(남은 실행 417초, 모델 마감까지 187초)·`cancelled_keeps_time_limit`를 Go handler와 웹 파서가 함께 확인한다.

## 호환·배포 순서

새 reader는 두 확인 시각과 `time_limit`이 없는 기존 Job·목록·저널을 수용한다. 누락된 값에 created_at·파일 mtime·saved_at·현재 시각을 채우는 migration은 하지 않는다. 저장 형식의 상위 버전·E② 승인 지문·SourceDigest·예산·실행 횟수를 변경하지 않는다.

서버 출력과 웹의 `diagnosis-url`·`diagnosis-runs` 타입·닫힌 파서를 **같은 dev 변경에 포함한다**. 서버만 먼저 배포해 새 필드가 있는 상태가 거부되는 중간 상태를 만들지 않는다. 웹 정본 비교는 확인 시각과 시간 상한의 변경·삭제·구 기록 사후 보충을 거부한다. `.1Z`와 `.100000000Z` 같은 동일 순간은 나노초로 비교하며, 실제 1ns 차이는 변경으로 본다. `server_time`은 같은 revision 비교에서 제외한다. 모르는 키·null·잘못된 날짜·시각과 boolean 불일치는 계속 거부한다. 시작·소요·남은 시간을 화면에 그리는 작업은 별도다.

보호 IPC·저널의 구 reader도 새 필드를 임의 수용하지 않는다. 준비 서비스와 API reader를 함께 갱신한다. 새 시각·시간 상한이 기록된 저널을 구 바이너리에 직접 열어 rollback하지 않는다. 일반 보고서/판정 기록의 saved_at과 저장 구조는 변경하지 않는다.

## 검증 경계

준비 서비스 시험은 시작 전 생략, 한 번만 기록, UTC, 중복·동시 게시, 중지·멱등 재조회, 불명 facts 조합, 구 저널, 재시작·압축, 시계 역행, JSON 오류와 저널 변경 거부를 검증한다. 시간 상한 시험은 몫 범위·합계와 저널 재생의 지움·변경·사후 보충 거부를 확인한다. API 시험은 브라우저의 시각 지정 거부와 시간 상한이 있는 응답에만 붙는 `server_time`을 확인한다.

[공유 응답 예시](diagnosis-run-timing.responses.example.json)는 시각 없는 준비 회차·시작만 확인한 실행 회차·두 시각을 확인한 완료 회차·시작 전 실패의 마감·시각 없는 구 완료 기록과 시간 상한이 있는 실행·취소 회차를 포함한다. Go는 예시에서 입력을 만들지 않고 독립 고정 Job/RunPage를 실제 공개 URL handler로 보내 상태·목록 응답 전체를 예시와 비교한다. 웹은 같은 파일을 두 파서와 목록→상태 결속으로 읽는다. 기존 [exact-path 상태 예시](exact-path-approval.status.example.json)도 실제 Go handler 출력과 웹 파서 양쪽에서 확인한다. Job·목록 행의 고정 예시에서 값이 비어 숨는 새 `omitempty` 필드도 Go의 공개 JSON 필드 계약 검사가 변경을 알린다.

`scripts/ci/web-changed.py`는 `docs/contracts/**/*.json`의 추가·수정·삭제·이동을 웹 검증 입력으로 취급한다. 서버와 예시만 수정한 PR에서도 웹 계약 시험을 건너뛰지 않으며, 모르는 키를 허용하도록 파서를 느슨하게 만들지 않는다.

제품 종단시험은 일회용 PostgreSQL 16·실제 설치 인증·URL v2·보호 Unix IPC·저널·목록/상태·저장 보고서 재연결을 사용한다. 실행 backend만 합성이며 실제 모델·대상 HTTP·Workbench 시간 표시 수용은 NOT_RUN이다. 시험 결과와 SHA는 PR에 기록한다.
