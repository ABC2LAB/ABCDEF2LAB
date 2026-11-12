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
- intent `stopped`는 보통 실행 `state`가 `cancelled`일 때 나온다. 그래서 `intent_outcomes`는 `cancelled`에도 싣는다(3.2). `completed`에 `paused`가 남는 경로는 지금 코드에 없지만(완료 판정은 열린·진행 중 intent가 있으면 기다린다), C26에서 Planner `goal_met`이 복원되면 생길 수 있어 `completed`의 대조식에도 `stopped`를 넣어 둔다.
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

### 3.2 새 칸

| 칸 | 있는 `state` | 값 | 규칙 |
|---|---|---|---|
| `outcome` | `completed` | `complete` · `partial` · `no_intents` | 아래 대조식. |
| `closing_reason` | `completed` (없을 수 있음) | `budget_limited` | Planner 라운드·도구 호출·intent 재실행 상한 또는 승인 모델 예산에 닿아 새 작업 없이 마감했다. 찾은 후보·근거는 보존한다. |
| `intent_outcomes` | `completed` · `cancelled` | `{done, policy_limited, incomplete, unknown, stopped}` 정수 | 위 2절 투영의 개수. 모두 0 이상. `cancelled`에서는 "끝난 것·끊긴 것"을 보이는 데 쓴다(#429 이경준 제안). |
| `diagnostic_log_incomplete` | `completed` | 참·거짓 | 진단 로그 기록 실패·관찰 불완전(C10). `reason_code`에 넣던 경고를 여기로 옮긴다(#418 이경준 지적: 다른 사유가 있으면 경고가 덮였다). |

`outcome`과 `intent_outcomes` 대조식(받는 쪽이 검사하고, 맞지 않으면 응답을 거부한다):

- `no_intents` ⇔ 다섯 개수가 모두 0.
- `complete` ⇔ `done + policy_limited ≥ 1` 이고 `incomplete + unknown + stopped = 0`.
- `partial` ⇔ `incomplete + unknown + stopped ≥ 1`.

조합 규칙:

- `no_intents` + `budget_limited`는 있을 수 있다. 첫 계획을 세우기 전에 모델 예산에 닿은 경우다(첫 Planner 호출이 실패해 예약량으로 정산되고 두 번째 예약이 예산을 넘는 경우 등). 뜻은 "예산 상한으로 계획을 못 세움, 검사 안 함"이다.

- 결정안은 `no_intents`를 마감 사유로 적었다. intent가 없으면 `complete`·`partial` 어느 쪽도 맞지 않아서 `outcome` 값으로 둔다. `closing_reason`은 `budget_limited` 하나로 시작한다.
- `budget_limited`와 `outcome`은 따로다. 상한에 닿았어도 시작한 intent가 모두 `done`이면 `complete` + `budget_limited`다(상한 때문에 더 못 했다는 뜻은 `closing_reason`이 말한다).
- 시간 상한(task deadline)은 이 계약에 넣지 않는다. 지금처럼 `failed`다. 시간 예산을 `budget_limited`로 볼지는 열린 항목 1.
- 위 표의 "있는 `state`" 밖에서 새 칸이 오면 받는 쪽은 거부한다.

### 3.3 사용량 회계와 실행 결과는 다른 축이다

모델 사용량의 `unknown_usage_calls`(C05, #419)는 "사용량을 못 받아 예약량으로 계산한 호출 수"다. 운영자 중지로 끊긴 호출도 여기에 들어간다. 실행이 중지됐는지는 `state`(`cancelled`)가 말하고, intent가 끊겼는지는 `intent_outcomes.stopped`가 말한다(#411 이경준 비차단 1).

## 4. 화면 표시 원칙 (이경준, #411 리뷰)

- `partial`: "일부 완료". 실패 색을 쓰지 않고, 남은 intent 수(`incomplete`·`unknown`·`stopped`)를 같이 보인다.
- `budget_limited`: "예산 상한에서 마감". 찾은 후보는 보존된다고 보인다.
- `no_intents`: "시도한 계획 없음(검사 안 함)". "발견 없음"과 다른 문구·색.
- `complete`에 `policy_limited`가 있으면 "정책상 검증 안 됨" 건수를 따로 보인다(완료가 "전부 검사됨"으로 읽히지 않게).
- `cancelled`: "중지됨". `intent_outcomes`가 있으면 "중지됨 · 끝난 n / 끊긴 m"을 보인다(없으면 개수 없이).
- 접근통제 지도: intent `incomplete`·`unknown`·`stopped`는 지도 `untested`로 가고 `na`로 가지 않는다(#383 Open #2와 같은 규칙).

## 5. 전달 경로

1. `generalCompletion`이 2절 투영으로 `intent_outcomes`·`outcome`을 만들고, 상한 마감이면 `closing_reason`을 붙인다. 투영에 쓰는 그래프 읽기가 실패하면 지금처럼 `failed`다.
2. `diagnosis.Closure`에 결과 칸을 더하고 `diagnosis.Snapshot`이 `completed`·`cancelled`일 때 3.2 표대로 싣는다. 버전 문자열(`diagnosis.Version`)은 올린다. 중지된 실행도 마감 때 같은 투영을 읽는다. 읽기가 실패하면 `intent_outcomes`를 싣지 않는다(중지 상태는 그대로).
3. 진단 준비 Update·Job(`diagnosisprep`)이 받아 저장하고 API로 내보낸다. 저장된 Job 갱신 규칙(사실을 되돌리지 않음)은 새 칸에도 적용한다: 한 번 `completed`로 기록된 칸은 바뀌지 않는다.
4. 웹 파서(`web/src/lib/diagnosis/diagnosis-url.ts`)는 키를 정확히 검사하므로 서버가 새 칸을 내는 변경과 같은 PR에서 받는다. 파서는 3.2의 대조식·조합 규칙·"있는 `state`"를 그대로 검사한다.
5. 저장 보고서에도 `outcome`·`closing_reason`·`intent_outcomes`를 싣는다(#429 이경준: 저장 보고서만 보는 사람이 `partial`·`no_intents`를 "발견 없음"으로 읽지 않게, 절대 규칙 5).

## 6. 구현 순서

1. 이 계약 확정.
2. intent 투영·새 칸·`partial`(`incomplete`·`stopped`)·`no_intents`·`budget_limited`·`diagnostic_log_incomplete` 분리. 웹 파서·표시는 같은 PR에서 이경준 리뷰(표시 문구는 이경준이 정하면 따른다). 나눠서 넣는다.
   - 2-1(먼저): 투영·`completed`의 `outcome`·`intent_outcomes`·`partial`(불완전·중지 intent가 있어도 실패가 아님)·전달 칸(`diagnosis.v2`)·진단 준비 Job·웹 파서와 결과 한 줄. 결과 없이 새로 `completed`가 되는 갱신은 받지 않는다.
   - 2-2: `cancelled`의 `intent_outcomes`(중지 마감 때 투영 읽기). 감독자가 감사·drain 마감 뒤(task 정지 확인 뒤) 투영을 읽고, 실행 중 intent가 남았거나 읽기에 실패하면 싣지 않는다. 시작하지 않은 `open`은 `incomplete`다. `outcome`·`closing_reason`은 싣지 않는다(중지 요청 직후 완료와 겹쳐도 개수만). 지금 중지 경로에서 진행 중 intent는 그래프 `paused`가 아니라 E② 실행 차단(`blocked` + `e2_http_execution_blocked`)이면 `incomplete`, 진행 중 송신이 끊겼으면(`e2_http_result_unknown`) `unknown`으로 센다. 화면의 "끊긴 m"은 둘 다 포함한다.
   - 2-3: `budget_limited`(Planner 라운드·도구 호출·모델 예산). E② 런타임이 Planner 라운드·실행 전체 도구 호출 상한 도달을 `ErrE2HTTPBudgetLimited`로 따로 돌려주고 task에 표시하며, 엔진은 이를 실패로 바꾸지 않는다. 승인 모델 예산이 다음 호출을 받을 만큼 남지 않은 경우(`LimitReached`, 사용량 불명·정책 차단은 제외)도 같다. 감독자는 실행 중 intent가 끝나면 Planner를 기다리지 않고 마감하고, 시작하지 못한 open intent는 incomplete로 센다. intent 하나의 재실행 상한(`MaxIntentRuns`)은 그 intent만 incomplete로 끝내고 실행 전체 상한은 아니다.
   - 2-4: `no_intents`(C11 결정 2, 첫 계획 0개). 진단 준비의 첫 계획에서 intent가 0개여도 실패로 끝내지 않고 실행으로 넘어간다(저장소를 못 읽은 경우만 `intent_storage_failed`). 실행 단계 Planner가 오류 없이 한 라운드 이상 계획했는데도 intent가 없으면(삭제된 intent는 세지 않음) `completed` + `no_intents`로 마감한다. 이때 E② 호출이 있으면 대조가 맞지 않으므로 실패다. 실행 상한에 먼저 닿으면 계획 라운드 없이도 `no_intents` + `budget_limited`로 마감한다.
   - 2-5: `diagnostic_log_incomplete` 칸 분리. 진단 준비 Job의 완료 실행에만 있고 `reason_code`를 쓰지 않는다. 이 칸 이전에 저장된 완료 실행에는 `reason_code: diagnostic_logging_incomplete`가 남아 있을 수 있어 화면은 둘 다 읽는다.
   - 2-6: 저장 보고서에 싣기(5절 5). 실행 종료 기록(`first_diagnosis_run_v1`)에 `result` 칸을 더한다(버전 1 그대로, 이 칸 이전 기록에는 없음). `completed`는 대조식이 맞는 결과, `cancelled`는 개수만, 실패·마감 실패에는 싣지 않고, 같은 결과로만 다시 저장된다. 보고서(HTML·DOCX, 공유 사본 포함) 3장 관리자 요약에 화면과 같은 문구로 "실행 결과: …" 한 줄을 싣는다. 기록이 없거나 형식이 틀리면 "확인하지 못함"으로 쓰고 완료로 바꾸지 않는다. 진단 준비 중지·대상 철회는 실행 문맥 취소로 끝나 요청 승인 상태가 `failed`로 남는데, URL 실행이 운영자 중지(상위 문맥 취소)나 대상 철회(`ErrFirstDiagnosisTargetEnded`)로 확인하고 마감이 확인되면 저장 기록에 `cancelled`로 남긴다. 설치·위임 검사 실패, 상태 조회·진행 저장 실패, 시간 상한처럼 원인 없는 취소는 `failed` 그대로다(#454 윤재상 지적). 화면(`diagnosis.Service`)은 지금도 문맥 취소를 모두 `cancelled`로 보므로 같은 구분이 필요하다(열린 항목).
3. `unknown` intent가 있는 실행을 `completed` + `partial`로 마감한다(C01 #477 뒤). 결과 불명 송신은 그 intent만 멈추고 다시 보내지 않으므로, 감사가 닫히면 실행은 실패가 아니다.
   - 근거 대조: 결과 불명 송신(`Counts.Unknown`)이 있는 호출의 intent는 `blocked` + `e2_http_result_unknown`이어야 하고, `unknown` intent 수는 그런 호출이 있는 intent 수(와 C09의 신뢰 결과 대조 실패 intent)와 같아야 한다. 결과 불명 송신 없이 `unknown`으로 표시된 intent가 있으면 마감하지 않는다(`partial`·`policy_limited`처럼 개수를 E② 기록으로 뒷받침한다).
   - 호출 결과: `result_unknown`(결과 불명 송신 수가 있음)과, 대상 연결을 맺지 못한 송신 실패(`transient_failure` + 송신 실패 수, C02)는 그 호출의 결과로 받는다. 실행 전체 누계의 결과 불명은 다른 intent의 것이라 다른 intent의 마감을 막지 않는다(C03).
   - 그대로 실패인 것: 감사 저장 실패, 실행 권한 철회·만료, 송신 실패 수가 없는 `transient_failure`(프록시 장애와 명령 시간 초과를 신뢰 결과에서 나눌 수 없음 — 열린 항목 2), 비최종·미드레인 호출.
4. C09(intent 결과를 모든 호출 분류로, #428)와 C26(Worker 마무리 보고)은 이 투영을 그대로 쓴다.
5. C26 1단계(Planner `prove_goal`·`goal_met`): Planner가 목표 달성으로 작업을 끝내면(작업 `done`) 실행 상한과 같이 Planner를 기다리지 않고 실행 중 intent가 끝나는 대로 마감하고, 남은 `open` intent는 `incomplete`로 센다(그래서 `partial`). `closing_reason`은 붙이지 않는다(`budget_limited`는 실행 상한일 때만).

## 7. 열린 항목

1. **시간 상한**(이동찬): task deadline 도달을 `budget_limited`로 볼지. 지금은 마감 과정(settlement)과 얽혀 있어 이 계약에서 뺐다.
2. **명령 시간 초과**(E② 실행기 담당): Bash 명령 하나가 시간 상한에 닿으면 실행기는 `transient_failure`로 분류하는데, 신뢰 결과에 종료 원인이 없어 E② 프록시 장애(집행 장애, 실행 `failed`)와 구분되지 않는다. 그래서 지금은 명령 시간 초과도 마감 실패다. 신뢰 결과에 종료 원인(시간 초과·프록시 장애)을 싣으면 시간 초과는 그 intent의 `incomplete`로 센다.

정해진 것(#429 이경준 답): `budget_limited` 세부는 처음에는 보이지 않는다("예산 상한에서 마감"만). 세부를 넣을 때는 `closing_reason`을 늘리지 않고 별도 칸을 둔다. 저장 보고서에는 싣는다(5절 5).
