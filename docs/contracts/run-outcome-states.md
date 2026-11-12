# 실행 결과 상태 값 (C08) — 계약안

상태: **계약안(2026-11-12, 최민준 작성·이동찬·이경준 리뷰).** 리뷰로 확정되기 전에는 구현하지 않는다. 일정은 적지 않는다.

[P3 결정안](../run-outcome-model-decision.md) C08은 "intent 하나·상한 도달이 실행 실패"를 고치기로 했고, 구현 순서 4에서 "상태 값을 먼저 계약으로 올리고 구현한다"고 정했다. 이 문서가 그 계약이다. 서버(진단 실행 → 진단 준비 Job)와 화면(이경준)이 만나는 값만 정한다. 화면 문구는 이경준이 #411 리뷰에서 낸 원칙을 옮겨 적고, 정본은 화면 쪽이다.

원칙(절대 규칙 5): 미실행·판단불가를 "발견 없음"으로 기록하지 않는다. 실행이 끝났다는 것은 취약점이 없다는 뜻이 아니다.

## 1. 지금

- 진단 실행 결과(`diagnosis.Snapshot.state`, 진단 준비 Job의 `state`)는 `completed`·`cancelled`·`failed`·`unknown`이다.
- 일반 HTTP 마감 검사(`server/first_diagnosis_supervisor.go` `generalCompletion`)는 intent가 하나라도 `done`이나 정책 제한(`blocked` + `e2_http_policy_limited`·`e2_http_unsupported`)이 아니면 실행을 `failed`로 끝낸다. 턴·시간 상한(`exhausted`), Bash 미호출·일시 실패(`blocked` + 그 밖의 사유), Planner의 `kill_work`(`stopped`)가 모두 여기에 걸린다.
- Planner 라운드 상한·도구 호출 상한에 닿으면 Planner 준비가 실패하고 task가 `failed`가 된다(`server/engine.go` `PreparePlanning` 오류 처리).
- 완료 실행에 붙는 사유 칸(`reason_code`)은 진단 로그 경고(`diagnostic_logging_incomplete`, #418)에도 쓴다.

## 2. intent 결과 (5값)

intent 결과는 **그래프의 지금 상태에서 계산하는 투영**이다. DB 상태 값은 새로 만들지 않는다.

| intent 결과 | 그래프 상태 (`state`, `blocked_reason`) | 뜻 |
|---|---|---|
| `done` | `done` | Worker가 정상 마무리했다. 발견 여부와 무관하다. |
| `policy_limited` | `blocked` + `e2_http_policy_limited`·`e2_http_unsupported` | E②가 거부했거나 지원하지 않는 요청이라 확인하지 못했다. |
| `incomplete` | `exhausted`, `stopped`, `blocked` + 위·아래에 없는 사유(사유 없음 포함), 실행 마감 때 남은 `open` | 턴·시간 상한, Bash 미호출, 일시 실패, 모델 할당량, Planner가 그 방향을 멈춤, 예산 상한으로 시작하지 못함. |
| `unknown` | `blocked` + `e2_http_result_unknown` | 결과 불명 송신이 있다. |
| `stopped` | `paused` (실행 마감 때) | 운영자 중지·대상 철회로 끊겼다. |

- `deleted`는 세지 않는다. `running`이 남아 있으면 마감 전이다(지금처럼 기다린다).
- 그래프 `stopped`(Planner `kill_work`·task 마감 뒤 취소)는 운영자 중지가 아니라서 `incomplete`로 센다. 운영자 중지는 지금 코드에서 `paused`로 남는다(`server/engine.go` cancel 처리).
- `unknown` intent의 쓰기 요청은 다시 보내지 않고, 그 intent에 기대는 후속 intent만 보류한다(개발 계획, C01).

## 3. 실행 결과

### 3.1 `state` — 지금 값 그대로

| `state` | 언제 |
|---|---|
| `completed` | 감사가 닫히고 실행 마감·결과 저장이 확인됐다. |
| `cancelled` | 운영자 중지·대상 철회. |
| `failed` | 감사 저장 실패, E② 집행 장애, 격리 실패, 권한 철회·만료·결속 실패, 저장소 읽기 실패, 워크플로 실패. |
| `unknown` | 실행 마감·결과 저장·모델 사용량을 확인하지 못했다(지금과 같음). |

바뀌는 것: intent 결과, Planner 라운드·도구 호출·intent 재실행 상한, 모델 예산 소진(사용량은 확인됨)은 더 이상 `failed`가 아니다. 감사가 닫히면 `completed`이고, 아래 칸이 내용을 말한다.

### 3.2 새 칸 (`state`가 `completed`일 때만 있다)

| 칸 | 값 | 규칙 |
|---|---|---|
| `outcome` | `complete` · `partial` · `no_intents` | `complete`: intent가 1개 이상이고 모두 `done`·`policy_limited`. `partial`: `incomplete`·`unknown`·`stopped`가 1개 이상. `no_intents`: 실행한 intent가 없다(첫 계획 0개). |
| `closing_reason` | 없음 · `budget_limited` | `budget_limited`: Planner 라운드·도구 호출·intent 재실행 상한 또는 승인 모델 예산에 닿아 새 작업 없이 마감했다. 찾은 후보·근거는 보존한다. |
| `intent_outcomes` | `{done, policy_limited, incomplete, unknown, stopped}` 정수 | 위 2절 투영의 개수. 모두 0 이상. `outcome`과 맞아야 한다(받는 쪽이 검사). |
| `diagnostic_log_incomplete` | 참·거짓 | 진단 로그 기록 실패·관찰 불완전(C10). `reason_code`에 넣던 경고를 여기로 옮긴다(#418 이경준 지적: 다른 사유가 있으면 경고가 덮였다). |

- 결정안은 `no_intents`를 마감 사유로 적었다. intent가 없으면 `complete`·`partial` 어느 쪽도 맞지 않아서 `outcome` 값으로 둔다. `closing_reason`은 `budget_limited` 하나로 시작한다.
- `budget_limited`와 `outcome`은 따로다. 상한에 닿았어도 시작한 intent가 모두 `done`이면 `complete` + `budget_limited`다(상한 때문에 더 못 했다는 뜻은 `closing_reason`이 말한다).
- 시간 상한(task deadline)은 이 계약에 넣지 않는다. 지금처럼 `failed`다. 시간 예산을 `budget_limited`로 볼지는 열린 항목 2.
- `state`가 `completed`가 아니면 새 칸은 없다. 받는 쪽은 그 경우 새 칸이 있으면 거부한다.

### 3.3 사용량 회계와 실행 결과는 다른 축이다

모델 사용량의 `unknown_usage_calls`(C05, #419)는 "사용량을 못 받아 예약량으로 계산한 호출 수"다. 운영자 중지로 끊긴 호출도 여기에 들어간다. 실행이 중지됐는지는 `state`(`cancelled`)가 말하고, intent가 끊겼는지는 `intent_outcomes.stopped`가 말한다(#411 이경준 비차단 1).

## 4. 화면 표시 원칙 (이경준, #411 리뷰)

- `partial`: "일부 완료". 실패 색을 쓰지 않고, 남은 intent 수(`incomplete`·`unknown`·`stopped`)를 같이 보인다.
- `budget_limited`: "예산 상한에서 마감". 찾은 후보는 보존된다고 보인다.
- `no_intents`: "시도한 계획 없음(검사 안 함)". "발견 없음"과 다른 문구·색.
- `cancelled`: "중지됨".
- `complete`에 `policy_limited`가 있으면 "정책상 검증 안 됨" 건수를 따로 보인다(완료가 "전부 검사됨"으로 읽히지 않게).
- 접근통제 지도: intent `incomplete`·`unknown`은 지도 `untested`로 가고 `na`로 가지 않는다(#383 Open #2와 같은 규칙).

## 5. 전달 경로

1. `generalCompletion`이 2절 투영으로 `intent_outcomes`·`outcome`을 만들고, 상한 마감이면 `closing_reason`을 붙인다. 투영에 쓰는 그래프 읽기가 실패하면 지금처럼 `failed`다.
2. `diagnosis.Closure`에 결과 칸을 더하고 `diagnosis.Snapshot`이 `completed`일 때 함께 싣는다. 버전 문자열(`diagnosis.Version`)은 올린다.
3. 진단 준비 Update·Job(`diagnosisprep`)이 받아 저장하고 API로 내보낸다. 저장된 Job 갱신 규칙(사실을 되돌리지 않음)은 새 칸에도 적용한다: 한 번 `completed`로 기록된 칸은 바뀌지 않는다.
4. 웹 파서(`web/src/lib/diagnosis/diagnosis-url.ts`)는 키를 정확히 검사하므로 서버가 새 칸을 내는 변경과 같은 PR에서 받는다. 파서는 `outcome`과 `intent_outcomes`가 맞지 않거나 `completed`가 아닌데 새 칸이 있으면 거부한다.

## 6. 구현 순서

1. 이 계약 확정.
2. intent 투영·새 칸·`partial`(`incomplete`·`stopped`)·`no_intents`·`budget_limited`·`diagnostic_log_incomplete` 분리. 웹 파서·표시는 같은 PR에서 이경준 리뷰(표시 문구는 이경준이 정하면 따른다).
3. `unknown` intent가 있는 실행을 `completed` + `partial`로 마감하는 것은 C01(결과 불명 1건 전체 정지 제거, 이동찬) 뒤. 그 전에는 결과 불명 송신이 있는 호출(`Counts.Unknown`)이 있으면 지금처럼 실행을 멈춘다.
4. C09(intent 결과를 모든 호출 분류로, #428)와 C26(Worker 마무리 보고)은 이 투영을 그대로 쓴다.

## 7. 열린 항목

1. **`budget_limited` 세부**(이경준): 어떤 상한에 닿았는지(Planner 라운드·도구 호출·intent 재실행·모델 예산)를 화면에 보일지. 보이려면 `closing_reason`을 늘리지 않고 별도 칸을 둔다.
2. **시간 상한**(이동찬): task deadline 도달을 `budget_limited`로 볼지. 지금은 마감 과정(settlement)과 얽혀 있어 이 계약에서 뺐다.
3. **보고서**(이경준): 저장 보고서에도 `outcome`·`intent_outcomes`를 실을지.
