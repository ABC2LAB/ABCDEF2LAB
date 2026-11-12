# Planner·Worker 루프 복원 결정안 (P1: C19·C24~C27)

상태: **결정안(2026-11-12, 최민준 결정·이동찬 리뷰).** 리뷰로 확정되기 전에는 구현하지 않는다. 일정은 적지 않는다. 측정 전 수치는 적지 않는다.

[개선 목록](control-restore-backlog.md)의 P1 루프 복원 항목(C19·C24~C27)을 정한다. 지금 E② 진단 모드는 원본 루프(`agent/worker.go` `execute`, `agent/planner.go`)를 타지 않고, `agent/e2_http.go`가 도구·프롬프트·상한을 줄인 별도 경로로 돈다. **복원은 진단 모드도 원본 루프를 쓰게 하는 것이다.** 원본과 다르게 남기는 것은 대상으로 나가는 경로와 CLAUDE.md 절대 규칙 2·3·5·8이 요구하는 것뿐이다.

## 1. 방식

- 원본 `Worker.execute`·`Planner` 경로를 그대로 쓰고, 진단 모드에서는 **도구 프로필만 바꾼다.** 지금처럼 `e2_http.go`에 원본 도구를 하나씩 다시 붙이지 않는다. 원본 코드가 기준이라 원본 대비 차이를 이 문서의 표로만 설명할 수 있다.
- 바꿔 끼우는 것은 네 종류다. ① 대상으로 요청을 보내는 도구(Bash·WebFetch) ② 서버 호스트의 파일·자원에 닿는 도구(Read·Write·Edit·LS, Planner의 기본 도구) ③ 대상이 아닌 곳으로 나가는 도구(WebSearch·MCP·자산 자동 보강) ④ 판정 쪽(report_finding).
- 탐색 실행기 2단계의 탐색 에이전트([결정안](contracts/exploration-executor.md) 2-b)는 같은 원본 루프 위에 **탐색 프로필**로 올린다. 진단 프로필과 탐색 프로필은 도구 목록과 네트워크 배치가 다르고, 한 실행 단계는 한 프로필만 쓴다(규칙 3의 단계별 권한 분리).
- 프로필 밖 도구가 섞이면 루프를 시작하기 전에 거부한다. #410의 MCP 차단처럼 하되 이름 접두가 아니라 프로필 허용 목록으로 본다.

## 2. Worker 도구 (C24)

| 원본 도구 | 진단 프로필 | 이유 |
|---|---|---|
| Bash | E② 판(지금의 샌드박스 실행·intent 예약) | 규칙 3 |
| WebFetch | **E② 판으로 복원.** 같은 샌드박스 실행기로 가져오고, 본문을 읽기 쉬운 글로 바꾸는 것만 호스트에서 한다 | 원본은 서버 프로세스가 대상에 직접 보낸다. 그대로 두면 E② 우회 |
| Read·Write·Edit·LS | intent 작업 디렉터리 안에 가둔 판. C29(호출마다 지우는 작업 디렉터리)와 같이 넣어 Bash와 같은 디렉터리를 보게 한다 | 원본 Read는 호스트 경로를 다 읽는다. 서버 호스트에는 제어 비밀이 있다(규칙 2) |
| Sleep | **넣지 않는다.** SDK `sleep`은 백그라운드 명령(`run_in_background`)을 기다렸다가 TaskOutput으로 읽는 짝인데, 진단 Bash는 백그라운드 실행을 지원하지 않는다. 기다림이 필요하면 명령 안의 `sleep`이 호출 시간 상한 안에서 한다 | 쓸 데 없이 Worker 시간만 쓴다 |
| WebSearch | 설치 설정(`webSearch`)대로. 완전 로컬 모드에서는 끈다 | 대상이 아니라 검색 제공자로 나간다(규칙 8) |
| skills | **진단 프로필에는 넣지 않는다.** 지금 skill 중 진단 목적의 것이 없다. `api-recon`은 탐색 프로필에 준다([C30 결정안](sandbox-data-decision.md)). 진단용 skill이 생기면 지시문만 읽고 명령은 Bash(E②)로 돌린다 | `api-recon`·브라우저는 탐색 단계 몫(규칙 3) |
| MCP(Playwright 등) | **진단에서는 계속 뺀다.** 브라우저는 탐색 프로필 | 규칙 3, #410 |
| 그래프 도구 | 원본 Worker 목록 전부(list_findings·record_fact·insert_assets·list_assets·search_all_worker_traces·get_worker_trace·node_detail). insert_assets는 열린 항목 5 뒤. 출력 가림(`RedactOutput`)과 record_fact의 intent 일치 검사는 유지. 지금 E② 필터의 graph_overview·list_facts·get_worker_output·list_goals는 원본 Worker 목록에 없는 Planner 도구라 실제로 Worker에 들어간 적이 없고, 프로필에도 넣지 않는다 | DB 안 작업 |
| insert_assets의 자동 보강(DNS·HTTP) | 진단 모드에서는 끈다. 자산 기록만 한다 | 서버가 발견 자산에 직접 보낸다. 필요하면 탐색 단계 몫 |
| report_finding | 원본 이름·입력 형식으로 복원하되 진단 모드에서는 **확정이 아니라 진단 후보로 저장**한다(지금 `submit_diagnostic_candidate`와 같은 저장). 판정은 E③이 E② 기록으로 한다 | 규칙 5. 원본 프롬프트·skills가 report_finding을 부른다 |
| traffic_search·traffic_get·traffic_blob | **진단 프로필에 넣지 않는다.** 진단 요청은 traffic 저장소에 기록되지 않는다(열린 항목 1 결과). 근거 묶기는 후보 제출의 call_ref가, 지난 응답 다시 보기는 C29의 출력 파일이 맡는다 | 빈 결과를 "요청 없음"으로 오해하게 하지 않는다 |

## 3. Worker 프롬프트 (C25)

- 원본 `workerSystem` 구성을 쓴다: 대상 자산, 제약(`constraintBlock`), intent, 그래프 개요, 산출물 위치.
- 프록시·CA 안내 줄만 E② 안내로 바꾼다: 요청은 E② 경계를 지난다, 거부·미지원은 사유 헤더로 돌아온다(C31), 결과 불명 쓰기는 다시 보내지 않는다.
- 목표를 "읽기 전용, 후보만"으로 고정하는 문장은 지운다. 승인된 메서드·쓰기 범위는 **프로그램이 승인 기록에서 넣는다**(모델이 정한 값이 아니다).
- 원본의 "Confirmed vulnerabilities → report_finding" 줄은 진단 모드에서 "후보로 제출하고 확정은 별도 판정"으로 바꾼다.
- 대상에서 온 데이터의 "untrusted" 표시와 가림은 지금처럼 유지한다.
- 예산이 끝나갈 때의 마무리 턴(settlement)을 복원한다. 마무리 턴에서 숨기는 도구는 원본처럼 Bash다.

## 4. Planner (C26)

- 원본 `PlannerTools` 전체를 복원한다: prove_goal·goal_met·kill_work·steer_work·report_finding(진단 후보)·list_companies·list_assets·add_company_scope·add_task_scope·list_untested_assets. skills·Todo·압축·마무리 턴도 복원한다.
- 범위 도구(add_company_scope·add_task_scope)는 커버리지 분모와 자산 소속만 바꾸고 **E② 승인 범위는 바꾸지 않는다**(규칙 4). 범위 도구를 부른 뒤에도 E② 정책 지문이 같은지 시험으로 고정한다.
  - add_task_scope는 넣었다. 진단 경로는 task_scope를 승인 근거로 읽지 않는다. 통합 시험(`actual-engine-planner-task-scope-keeps-e2-policy`)은 Planner가 대상 호스트 IP를 작업 범위에 넣고 그 범위로 intent를 만든 뒤에도, 같은 호스트의 승인 안 된 포트로 가는 Worker 요청이 E②에서 거부되고(수신 0건) 실행 정책의 판·규칙이 승인 파일 그대로인지 본다. 원본 도구 설명은 이 도구를 "작업의 인가 경계"라고 부르고 넓히라고 권하지만 진단 경로에서는 그렇지 않다. 그대로 두면 모델이 넓힌 범위로 승인 밖 intent를 만들고, C01 전에는 거부 상한(제품 기본 2회)에 닿아 실행이 멈출 수 있다. 그래서 동작·스키마는 원본 그대로 두고 모델에 보이는 설명만 진단 판(커버리지 분모만 넓힘, 송신 범위는 그대로, 승인 밖 호스트·포트로 intent를 만들지 말 것)으로 바꾼다(`DecorateTool`).
  - add_company_scope는 제품 DB 역할이 companies·company_scope를 SELECT만 허용해서 넣어도 늘 실패한다. 열린 항목 5(insert_assets)와 같이 정한다.
- Planner에서는 대상으로 보내는 도구를 뺀다: 원본 기본 도구의 Bash, WebFetch, MCP. 대상 요청은 Worker(E②)만 보낸다. 호스트 파일 도구는 Worker와 같은 조건(작업 디렉터리 안)일 때만 넣는다.
- WebSearch는 Worker와 같은 규칙이다.
- Plan당 상한(`e2HTTPPlannerMaxTurns`·`e2HTTPPlannerMaxDuration` 상수)은 원본 설정 경로(에이전트 설정의 턴·시간)로 돌리고, 제품 기본값은 C32 설치 설정으로 둔다.
  - 구현: 상수를 `agent.E2HTTPPlannerLimits`(기본 16턴·2분)로 바꾸고 설치 입력 `runtime_limits`의 `planner_turns_per_plan`·`planner_seconds_per_plan`을 launch 설정으로 받는다([설치 상한 결정안](install-limits-decision.md) §3-5). 에이전트 설정의 턴 수가 설치 값보다 작으면 그 값을 쓴다.
- 마무리 턴·Todo: 원본처럼 Plan 상한에 닿으면 마무리 턴(정한 결론을 add_intent·prove_goal·TodoWrite로 남김)을 받고, 깨울 때마다 이어지는 계획 할 일(TodoWrite)을 둔다. 둘 다 이 프로세스 안 작업이라 대상 송신 경로가 없고, 마무리 턴의 도구도 프로필 도구뿐이다. 원본 Planner는 자체 시간 상한이 없어 task 기한이 있으면 시간 종료를 늘 "작업 종료" 마무리(새 intent 금지)로 보는데, 진단 Planner는 Plan당 시간 상한이 있으므로 task 기한이 이번 Plan을 실제로 자를 때만 작업 종료 마무리를 쓰고 Plan 상한으로 끝나면 "이번 라운드만 끝" 마무리를 쓴다. 마무리 단계는 Plan당 상한 밖이다: 원본처럼 에이전트 설정의 마무리 턴 수(기본 10턴)까지 돌고 시간 상한을 보지 않는다(SDK 동작). 실행 시간 상한(task 문맥)과 모델 예산은 마무리 단계도 자른다. TodoWrite는 SDK가 붙이므로 진단 기록에 등록 도구로 알린다(기록 어휘는 #472). 압축(Compactor)·skills는 아직 넣지 않았다.

## 5. 실행 흐름 (C19·C27)

- **C27**: Planner를 Worker 실행 중에도 깨운다. 원본처럼 Worker 완료·후보 제출 때 깨운다(`server/e2_http_runtime.go`의 `ErrE2HTTPBusy` 대기 제거). 모델 호출은 C28 동시 호출 수가 1인 동안 차례로 나간다(P3 결정안 `docs/run-outcome-model-decision.md`(#411) C28 결정 2). 동시 Worker 수·intent 상한은 C32 설정이다.
- **C19**: intent당 대안 1회 제한(`intent_replan_exhausted`)과 실패한 같은 명령 재전송 금지(`same_action_not_retried`)를 걷어낸다. 같은 거부가 반복되는 것은 막지 않고 반복 차단 수(`scripts/eval/repeat_denials.py`)로 잰다.
  - 남기는 것: 결과 불명(`result_unknown`) 명령은 다시 보내지 않는다(개발 계획 "불명 쓰기 재전송 금지"). E② 요청 단위 처리(C01, 이동찬)가 들어오면 이 규칙은 그쪽으로 옮긴다.
  - 같은 intent 안에서 호출은 한 번에 하나(`intent_call_inflight`)를 유지한다. 호출 단위 예약·감사 결속이 이를 전제한다(열린 항목 3).
  - 실행 전체 호출 상한(`task_call_limit`)은 C32 설정이다.

## 6. 원본과 다르게 남는 것

각 단계 PR은 걷어낸 항목 ID, 유지한 통제, 원본 대비 남은 차이를 적는다(개선 목록 규칙). 남는 차이는 이 목록 밖으로 늘리지 않는다.

1. 대상으로 가는 진단 요청은 E②를 지난다(Bash·WebFetch). 서버 프로세스가 대상에 직접 보내는 경로(WebFetch 원본·자산 자동 보강·Planner Bash)는 없다.
2. 진단 단계에 MCP·브라우저가 없다.
3. 호스트 파일 도구는 intent 작업 디렉터리 밖을 보지 못한다.
4. 제어 비밀은 모델 밖이고, 모델 입력에 들어가는 계정 값은 시험 계정 것뿐이다(규칙 2).
5. report_finding은 후보를 만들고 확정하지 않는다(규칙 5).
6. 결과 불명 명령은 다시 보내지 않는다.
7. 프롬프트 관측(`e2_prompt`)과 intent 예약·감사 결속은 지금처럼 남긴다.
8. 진단 단계에 traffic 조회 도구가 없다. 진단 요청은 MITM 기록 프록시를 지나지 않고 E②를 지나기 때문이다.

## 7. 구현 순서

1. 골격: 진단 Worker·Planner 도구를 원본 도구 목록(WorkerTools·PlannerTools·기본 도구)에서 프로필(`agent/tool_profile.go`)로 고른다. 대상으로 보내거나 E② 결속이 필요한 도구(Bash·후보 제출)는 E② 판으로만 채우고 원본 목록의 같은 이름 도구(호스트 Bash)는 고르지 않는다. 이 단계에서는 프로필에 지금 E② 도구만 넣어 **동작 차이가 없게** 하고, 그것을 시험으로 고정한다. 프로필 밖 도구가 섞이면 시작 전에 거부한다. Worker 프롬프트·세션을 원본 경로(`workerSystem`·`execute`)로 옮기는 것은 3단계에서 원본 프롬프트로 바꿀 때 한다.
2. C24 일부: Worker 그래프 조회 도구(list_findings·list_assets·search_all_worker_traces·get_worker_trace). 저장소 조회뿐이다. insert_assets는 제품 DB 역할이 assets를 SELECT만 허용해서 넣어도 항상 실패하므로 열린 항목 5와 같이 정한다.
3. C25와 C24 나머지: 원본 프롬프트·마무리 턴, report_finding(후보)·skills·WebSearch. report_finding(후보)은 후보 저장에 실제 call_ref가 필요해 원본 입력 형식과 바로 맞지 않고, 지금 부르는 쪽(원본 skills)도 C30 전에는 없어서 skills와 같이 한다.
4. WebFetch E② 판.
5. C26(Planner 도구·상한 설정)은 **C08(실행 결과 상태, P3) 뒤.** C08이 끝나 1단계로 `prove_goal`·`goal_met`(원본처럼 `goal_met`은 기본 바인딩 없음)과 목표 달성 마감(남은 open intent는 incomplete)을 넣었다. 2단계로 add_task_scope(E② 승인 범위 불변 시험과 같이)를, 3단계로 Plan당 상한 설정(C32 `planner_turns_per_plan`·`planner_seconds_per_plan`)을, 4단계로 마무리 턴·Todo를 넣었다. 다음 단계: 압축, report_finding(후보)·skills. goal_met은 열린 intent가 남은 채 작업을 done으로 만드는데, 지금 첫 진단 완료 판정(`generalCompletion`)은 열린 intent가 있으면 끝나지 않고 stopped·exhausted intent는 실패로 센다. Planner 라운드 상한 도달도 지금은 작업 실패다.
6. C27(Worker 실행 중 Planner 깨우기)·kill_work·steer_work·C19는 **C01(결과 불명 1건 전체 정지 제거, 이동찬) 뒤.** Planner가 Worker 실행 중에 goal_met·kill_work를 부르면 진행 중인 E② 호출이 취소되고, 지금은 그 결과 불명이 실행 전체를 멈춘다.
7. 파일 도구(C29와 같이). traffic 도구는 넣지 않는다(열린 항목 1 결과).

2단계를 구현하면서 5·6단계의 의존을 확인해 순서를 바꿨다. 지금은 Planner가 Worker가 모두 끝난 뒤에만 돌아서(C27) goal_met이 진행 중인 호출을 끊지는 않지만, 열린 intent가 남는 문제는 그대로다.

단계마다 P0 원자료로 보존 비교를 다시 잴 수 있게, 단계 PR의 커밋을 회차 기록의 엔진 커밋으로 남긴다.

## 8. 완료 기준 시험

- 진단 프로필 도구 목록이 이 문서 표와 같다(MCP 허용 목록 시험과 같은 방식의 계약 시험).
- fixture 대상 수신기에서 E②를 거치지 않은 연결이 0건이다(WebFetch·자동 보강·Planner 경로를 각각 부르는 시나리오).
- 범위 도구를 부른 뒤 E② 정책 지문이 바뀌지 않는다.
- 진단 모드 report_finding이 확정 finding을 만들지 않는다.
- Worker 실행 중에 Planner 라운드가 돈다.
- 거부 뒤 다른 명령을 여러 번 보낼 수 있고, 결과 불명 명령의 재전송은 거부된다.
- 파일 도구가 작업 디렉터리 밖 경로(`..`, 절대 경로, 심볼릭 링크)를 거부한다.
- 골격 단계에서 지금 E② 회차 시험이 그대로 통과한다.

## 9. 열린 항목

1. ~~traffic 도구의 기록 출처~~ **확인함(2026-11-12)**: 진단 요청은 traffic 저장소에 기록되지 않는다. 원본 traffic 도구는 Worker Bash 환경의 `HTTP_PROXY`가 가리키는 MITM 기록 프록시(`traffic.Open`, `agent/worker.go` 환경 줄)를 읽는다. 진단 Bash는 샌드박스 안에서 E② 중계 주소를 프록시로 받고(`e2sandbox/helper_linux.go` 환경 줄), E② 프록시는 대상에 직접 연결한다(`e2proxy/execution.go` transport `Proxy: nil`). 그래서 traffic 도구를 넣으면 빈 결과만 준다.
   - 결정: 진단 프로필에 traffic 3개를 넣지 않는다. 원본에서 traffic 도구가 하던 두 일 중 **발견 근거 묶기**는 후보 제출의 call_ref(감사에 결속된 E② trusted 관찰)가 이미 하고, **지난 요청·응답 다시 보기**는 C29(큰 출력을 작업 디렉터리 파일로 넘기고 파일 도구로 다시 읽기)로 한다.
   - 실행 전체에서 지난 E② 호출을 찾아보는 조회가 필요하면(보존 비교에서 필요가 보일 때), traffic 저장소가 아니라 E② 호출 결과(trusted 관찰과 가린 출력, 요청 인증 헤더·쿠키는 이름만)를 읽는 도구를 따로 정한다. 탐색 단계는 탐색 경계 기록을 쓰므로 이 결정과 별개다.
2. **대화 기록 재개**: 지금 E② 모드는 사람 지시로 이전 대화를 이어가지 않는다. 대화 기록 저장의 가림과 같이 정한다.
3. **같은 intent 안 병렬 도구 호출**: 원본 SDK가 병렬 호출을 하면 호출 단위 예약을 어떻게 나눌지. 그 전에는 하나씩이다.
4. **탐색 프로필 도구 목록**: 탐색 실행기 결정안 2-b에서 정한다.
5. **진단 단계 자산 쓰기(insert_assets)**: 제품 DB 역할은 assets·task_asset_links를 SELECT만 허용한다(`server/controlled_read_assembly_database_role.go`). 진단 Worker가 발견 자산을 기록하게 할지, 탐색 단계 몫으로 둘지 이동찬과 정한다. companies·company_scope도 SELECT만이라 Planner의 add_company_scope도 같이 정한다.
