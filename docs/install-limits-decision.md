# 제품 기본 상한의 설치 설정화 — 결정안 (C32)

상태: **결정안(2026-11-12, 최민준 결정·이동찬 리뷰).** 리뷰로 확정되기 전에는 구현하지 않는다. 일정은 적지 않는다. 측정 전 수치는 적지 않는다.

[개선 목록](control-restore-backlog.md) C32("제품 기본 상한이 작음. 장치는 유지하고 값을 설정으로")와, 다른 결정안이 "설치 설정 값(C32)"으로 넘긴 값을 한 번에 정한다. 넘긴 곳: [P3 결정안](run-outcome-model-decision.md) 열린 항목 3(동시 모델 호출 수·상태 조회 재시도 수), [루프 복원 결정안](loop-restore-decision.md) §4·§5(Planner Plan당 상한·동시 Worker 수·intent 상한·실행 전체 호출 상한).

## 1. 지금

- 상한은 설치 코드에 박혀 있다. `productInstall`(`cmd/first-controlled-diagnosis/product_install_linux.go`)이 E② 정책 파일(`policy.json`의 `Limits`·`Proxy`·`Runtime`), 실행 예산(launch의 `Budget`)을 고정값으로 쓴다. 워크플로 모드(`target_policy.workflow`)면 다른 고정값 한 벌을 쓴다.
- 동시 Worker 수: 설치는 DB 설정 `engine.workers`를 2로 쓰지만, 엔진이 읽는 키는 `workers`(`server/manager.go` `settingWorkers`)라 그 쓰기는 적용되지 않는다. 그래서 제품 실행의 Worker 수는 엔진 기본값 3(원본과 같음)이다. `Workers`·`SetWorkers`는 양수만 검사하고 상한이 없다.
- Planner Plan당 상한은 `agent/e2_http.go`의 상수(`e2HTTPPlannerMaxTurns`·`e2HTTPPlannerMaxDuration`)다. 동시 모델 호출 수는 모델 예산 gate 크기(1)다. 상태 조회 재시도는 없다(C10).
- 그래서 운영자는 대상·환경에 맞춰 값을 바꿀 수 없고, 바꾸려면 코드를 고쳐야 한다.

| 값 | 지금 위치 |
|---|---|
| 요청 시간 | `Limits.RequestTimeoutMS`, `Proxy.RequestTimeoutMS` |
| 호출 시간 | `Limits.CallTimeoutMS` |
| 호출 수 | `Limits.MaxCalls`, `Runtime.MaxToolCalls` |
| 시도 수 | `Limits.MaxAttempts` |
| Worker 턴·시간 | `Runtime.MaxWorkerTurns`, `Runtime.WorkerTimeoutMS` |
| Planner 라운드 | `Runtime.MaxPlannerRounds` |
| intent 실행 | `Runtime.MaxIntentRuns` |
| 동시 Worker | DB 설정 `workers`(설치의 `engine.workers` 쓰기는 적용 안 됨, 기본 3) |
| 실행 시간·모델 요청 수 | launch `Budget.DurationSeconds`·`Budget.ModelRequests` |
| Planner Plan당 턴·시간 | `agent/e2_http.go` 상수 |
| 동시 모델 호출 | `FirstDiagnosisModelBudget.callGate` 크기 |
| 상태 조회 재시도 | 없음 |

## 2. 결정

1. **설치 입력에 선택 블록 `runtime_limits`를 둔다.** 형식 판(`abc2lab.product-install/v2`)은 그대로 두고 선택 칸만 더한다. 블록이 없거나 칸이 비면 **지금 값 그대로**다(제품 모드·워크플로 모드 두 벌 모두). 측정(P0) 전에는 기본값을 바꾸지 않는다. 기본값 조정은 측정 근거와 함께 별도 PR로 한다.
2. **칸**(위 표의 값과 같은 뜻, 제품·워크플로 모드에 각각 줄 수 있다):
   - E② 진단 요청: `request_timeout_ms`(E② 요청 시간. 제품 모드는 프록시 요청 시간도 같은 값으로, 워크플로 모드는 프록시가 운영자 확인을 기다리는 동안 요청을 쥐고 있어야 해서 프록시 요청 시간을 워크플로 값 그대로 둔다), `call_timeout_ms`, `max_calls`(E② 호출 상한과 Worker 도구 호출 상한을 같은 값으로), `max_attempts`.
   - 루프: `worker_turns`, `worker_timeout_ms`, `planner_rounds`, `intent_runs`.
   - 동시 Worker 수(`workers`)는 지금 코드에 상한 검사가 없어(양수만) 이 블록에 두지 않는다. 상한과 검사 위치는 P0 측정 뒤 정하고 그때 칸을 연다. 그 전까지 제품 실행은 엔진 기본값(3)이다.
   - 실행: `run_seconds`(2단계에서 열었다, 상한은 설치 프로파일 검사의 600초). `model_requests`는 로컬 모델에서 코드 상한이 없어(1 이상만 검사) 4의 규칙대로 아직 열지 않는다. OpenRouter 모드는 승인 manifest가 요청 수를 정한다.
   - 뒤에 붙는 칸(그 기능이 들어올 때 받기 시작한다): `planner_turns_per_plan`·`planner_seconds_per_plan`(C26, C08 뒤), `model_concurrency`(C28 결정 2의 호출별 transport 슬롯 뒤, 그 전에는 1만 받는다), `status_read_retries`(C10 상태 조회 재시도).
3. **여기서 다루지 않는 값.**
   - 거부·결과 불명·일시 실패 누적 상한(`MaxDenials`·`MaxUnknown`·`MaxTransient`)은 E② 요청 단위 처리(C01, 이동찬)가 정한다.
   - 본문 상한(`MaxBodyBytes`)은 C16 결정 3(요청·응답 상한 분리)에서 같이 정한다.
   - OpenRouter 연속 일시 오류 상한은 승인 manifest v2 칸 그대로다(승인에 묶인 값이다).
   - 대상 요청 수·대상 수 같은 대상 승인 상한은 지금처럼 `target_policy`다.
4. **상한은 개발팀이 코드로 고정한다.** 설치 입력은 그 안에서만 고른다. 새 숫자를 만들지 않고 지금 코드의 검사 상한을 그대로 쓴다: E② `Limits` 검사(`e2proxy/execution.go`), 런타임 옵션 검사(`server/e2_http_runtime.go`: Worker 턴·Worker 시간·Planner 라운드·intent 실행·마감 시간), 도구 호출 상한(`agent.E2HTTPMaxTaskCalls`), 요청 시간 상한(`validProductPolicyTemplate`). 지금 코드에 상한이 없는 값은 상한을 정하기 전에는 칸을 열지 않는다(2의 `workers`). `model_concurrency`는 OpenRouter 승인 `max_concurrent`를 넘지 못한다. 범위 밖 값은 **설치를 거부한다**(잘라서 받지 않는다).
5. **결속.** 값은 `policy.json`과 launch 설정에 들어가고, E② 정책 지문(`e2export.PolicyFingerprint`의 `PolicyBehavior`, 지금도 `Runtime`·`Limits`·`Proxy`가 들어간다)과 설치 지문에 묶인다. `run_seconds`는 E② 정책이 아니라 launch 예산(`Budget.DurationSeconds`)이고, 설치 기록 해시와 회차 승인 범위(`Scope.Budget`)에 이미 들어가 회차마다 남으므로 E② 정책 지문 형식은 바꾸지 않는다(2단계에서 정정). 뒤에 붙는 칸 중 E② 정책 밖 값도 같은 방식으로 설치 기록에 묶는다. 실행 기록에 정책 지문이 남으므로 회차마다 어떤 상한이었는지 확인된다.
6. **바꾸는 법.** 설치 갱신(`./update.sh`)으로만 바꾼다. 실행 중 화면에서 바꾸지 않는다(운영 화면은 C33·이경준).
7. **평가.** 공식 비교는 평가 정책 템플릿과 공식 프로파일이 값을 고정한다. 제품 설치 기본값과 별개라 원본 비교 조건은 바뀌지 않는다.

## 3. 구현 순서

1. `runtime_limits` 입력·검사·`policy.json`/launch 반영(E②·루프 칸). 블록이 없으면 지금 `policy.json`과 같은 값이 나오는 것을 시험으로 고정한다(동작 차이 없음). — 구현: `productPolicyBounds`가 모드별 기본값에 덮어쓰고 `validProductPolicyTemplate`으로 검사하며, 설치 입력 검사 단계(부작용 전)와 실제 설치가 같은 함수를 쓴다.
2. `run_seconds`(기본값 그대로, 1~600초). 적용되지 않는 설치의 `engine.workers` 쓰기를 지운다(동작 차이 없음, 지금도 기본 3). `model_requests`는 열지 않고 정책 지문 판도 올리지 않는다(위 2·5).
3. `status_read_retries`(C10 상태 조회 재시도와 같이 열었다). URL 진단 실행 중 상태 조회(100ms 간격)가 연속으로 실패해도 다시 묻는 횟수다. 칸이 없으면 0이라 지금처럼 첫 실패에서 실행을 멈춘다(1의 규칙, 기본값은 측정 뒤). 다 써도 실패하면 실행을 멈추고 결과를 취소가 아니라 `unknown`(`status_read_failed`)으로 둔다(P3 결정안 C10). 한 번 성공하면 다시 센다. 지금 코드에 이 값의 상한이 없어서, 4의 규칙대로 새 숫자를 만들지 않고 다시 묻는 시간(횟수 × 조회 간격)이 그 launch의 정리 시간 상한(`shutdown_timeout_seconds`, 제품 20초 → 200회)을 넘지 않게 한다. 값은 launch 설정(설치 기록 해시)에 들어간다.
4. `model_concurrency`(C28 결정 2의 2부 #458 뒤에 열었다). 기본 1. OpenRouter는 승인 `max_concurrent` 이하. 로컬 모델은 코드에 동시 호출 상한이 없어 4의 규칙대로 1만 받고, 로컬과 OpenRouter를 같이 설치하면 로컬 launch에도 같은 값이 들어가므로 1만 받는다. 값은 launch 설정(`model_concurrency`, 설치 기록 해시에 묶임)에 들어가고 실행이 모델 예산 gate 크기로 쓴다(첫 호출 전 한 번).
5. `planner_turns_per_plan`·`planner_seconds_per_plan`(C26과 같이, C08 뒤에 열었다). 칸이 없으면 지금 값(16턴·120초)이다. 이 값은 지금 코드에 검사 상한이 없는 상수였으므로, 4의 규칙대로 새 숫자를 만들지 않고 같은 종류 값인 Worker의 코드 상한(턴 1000·시간 1시간)을 상한으로 쓴다. 실행 전체 시간은 따로 `run_seconds`와 task 기한이 자른다. E② 정책 밖 값이라 정책 지문 형식은 바꾸지 않고 launch 설정(설치 기록 해시)에 넣는다. 실행 서버는 host 설정으로 E② 실행 Runtime에, 준비 서버는 모델 설정 묶음으로 받아 준비 계획과 실행 계획 모두 같은 상한을 쓴다. 에이전트 설정의 턴 수가 더 작으면 그 값을 쓴다(원본 설정 경로, 루프 복원 결정안 §4). 상한에 닿은 뒤의 원본 마무리 단계(에이전트 설정 마무리 턴 수, 기본 10턴, 시간 상한 없음)는 이 칸 밖이다(#470). 평가 경로(워크플로·원격 eval-e2)는 칸을 받지 않아 지금 값 그대로다.

## 4. 열린 항목

1. **화면 표시**(이경준): 설치된 상한을 운영 화면에 보일지.
2. **탐색 단계 상한**: 탐색 에이전트의 모델 호출·시간 상한을 따로 둘지는 P0 측정 뒤 정한다([탐색 실행기 결정안](contracts/exploration-executor.md) 열린 항목 3).
