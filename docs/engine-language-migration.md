# 엔진 언어 전환 계획

상태: 계획. 기준 코드는 dev `adca969`(2026-11-06)다. 리뷰어는 이동찬이다.

## 규칙

- LLM이 읽는 글(프롬프트·도구 설명·모델에 돌려주는 도구 결과·오류·압축 지시·skills)은 영어로 쓴다.
- 사람이 보는 글(API 오류·UI·알림·서버 로그)과 주석은 한국어로 쓴다.
- LLM이 쓰는 화면 표시 필드는 한국어로 강제한다.
- 프로그램이 소유한 지시문·설명·메시지만 대상이다. 대상 응답, 명령·payload, 기존 근거, Unicode 처리 시험은 번역하거나 지우지 않는다.
- `go:build`·`go:embed`처럼 동작에 영향을 주는 지시문은 보존한다.

## 현재 건수

한자가 든 항목을 Go 구문 기준으로 센 값이다.

| 구분 | 파일 | 문자열 | 주석 |
|---|---|---:|---:|
| 제품 코드 | 128 | 1615 | 2549 |
| 시험 | 87 | 1191 | 676 |

Go 밖에는 `web/messages/zh.json`·`ja.json`, `web/src`, `skills/`, `db/schema.sql`, `sidequestion/*.zh.md`가 있다.

## 순서

날짜가 아니라 선행 PR 머지를 기준으로 진행한다.

| 단계 | 대상 | 선행 | 완료 조건 |
|---|---:|---|---|
| 1 | 압축 지시(`agent/compaction.go`) | 없음 | 프롬프트·입력 라벨에 한자 없음, 노드 글은 그대로 |
| 2 | 핵심 프롬프트: planner·worker·goals·main·reporter, 언어 꼬리(`langDirective`) | 함수 목록 합의 | 새 DB 시드가 영어. 이미 심긴 프롬프트의 갱신 방법 포함 |
| 3 | 도구 설명·도구 결과: `agent/tools*.go`, `traffic.Tools`, `server/platform_tools.go`, `server/orchestration.go` | 2, 함수 목록 합의 | 모델이 받는 글만 영어, API 오류·로그는 한국어 |
| 4 | `terminalreason`·`cancelcause` | 3 | 코드·enum·저장값 유지. UI는 한국어, 모델은 영어를 소비 경계에서 고른다 |
| 5 | `intercept` 프롬프트·파서·시험 | 3 | 같은 커밋에서 바꾼다. 잘못되거나 불완전한 출력은 계속 거부한다 |
| 6 | 내부 추론 비저장·비표시 | 2 | 활동 기록·transcript·로그·API·UI 경로를 모두 확인한다. 사용량은 본문 없이 토큰 수로만 남긴다 |
| 7 | `skills/`, DB 시드 문구(`db/db.go`) | 3 | 모델이 읽는 글은 영어, 규칙 메시지는 표시 위치에 맞춘다 |
| 8 | 번역 계층: `server/errmsg_catalog.go`, `config/msg_i18n.go` | 4 | 먼저 ko 단일 카탈로그로 고정하고, 계층 제거는 PR을 나눈다 |
| 9 | 주석 일괄 정리 | 2~8 | 동작 지시문 보존, 문자열 변경 없음 |
| 10 | 웹 `messages`·`src` | 8, 이경준 합의 | 담당자 PR |

## 충돌에서 뺄 파일

#233의 `server/controlled_read_host.go`·`server/controlled_read_kg_source*`, #244의 `server/controlled_read_bootstrap_scope_integration_test.go`는 그 PR이 머지될 때까지 건드리지 않는다.

## 합의할 함수 목록

프로그램 문자열에 한자가 있는 함수다. 각 함수에서 LLM이 읽는 글만 영어로 바꾸고, 사람이 보는 글은 한국어로 바꾼다.

| 파일 | 문자열 | 함수 |
|---|---:|---|
| `agent/tools.go` | 140 | `ToolSet.addFinding`, `ToolSet.addHint`, `ToolSet.addIntent`, `ToolSet.addOneConstraint`, `ToolSet.addOneGoal`, `ToolSet.addOneHint`, `ToolSet.addOneIntent`, `ToolSet.getWorkerOutput`, `ToolSet.getWorkerTrace`, `ToolSet.goalMet`, `ToolSet.graphOverview`, `ToolSet.graphOverviewData`, `ToolSet.killWorkTool`, `ToolSet.listFacts`, `ToolSet.listFindings`, `ToolSet.listGoals`, `ToolSet.listWorkerTraces`, `ToolSet.needExploration`, `ToolSet.nodeDetail`, `ToolSet.proveGoal`, `ToolSet.recordFact`, `ToolSet.recordOneFact`, `ToolSet.searchAllWorkerTraces`, `ToolSet.setConstraints`, `ToolSet.setGoals`, `ToolSet.steerWorkTool`, `WriteCounts.String` |
| `agent/tools_insert.go` | 88 | `ToolSet.addCompanyScope`, `ToolSet.addTaskScope`, `ToolSet.insertAssets`, `ToolSet.listAssets`, `ToolSet.listCompanies`, `ToolSet.listUntestedAssets`, `assetInputLabel` |
| `agent/worker.go` | 19 | `Worker.execute`, `artifactSpec`, `renderIntentTask`, `renderWorkerGraphOverview`, `workerArtifactSpec`, `workerDefaultTmpl`, `workerTrafficBlock` |
| `agent/planner.go` | 24 | `Planner.Plan`, `plannerDefaultTmpl`, `renderGraphOverview`, `renderPlannerTodos`, `renderTriggers`, `truncOutput`, `workerOutput` |
| `server/assembly.go` | 9 | `seedPrompts`, `wireAgentAugment`, `wireTools` |
| `server/engine.go` | 40 | `Engine.KillWork`, `Engine.SteerWork`, `Engine.appendActivity`, `Engine.emitActivity`, `Engine.plannerLoop`, `Engine.runIntent`, `Engine.runWorkerStep`, `dropReason`, `emptyTurnNudge`, `steerHooks.PreToolUse`, `steerHooks.Stop`, `transitionIntentState` |
| `server/orchestration.go` | 87 | `Server.delegateToTask`, `Server.hostTools`, `Server.reseedGoalsPrompt`, `Server.reseedMainAgentPrompt`, `Server.reseedPlannerPrompt`, `Server.reseedWorkerPrompt`, `Server.seedAutoDefaultBindings`, `Server.seedAutoReportFindingBinding`, `Server.seedCompanyScopeRebind`, `Server.seedPlannerDefaultBindings`, `Server.seedPlannerListAssetsBinding`, `Server.seedReporterAgent`, `Server.seedWorkerReadToolsUnbind`, `Server.seedWorkerReadbackRebind`, `Server.toolAddHint`, `Server.toolGetTaskGraph`, `Server.toolGetTaskNodeDetail`, `Server.toolGetWorkerTrace`, `Server.toolListLLMProfiles`, `Server.toolListTaskFindings`, `Server.toolListTasks`, `Server.toolListWorkerTraces`, `Server.toolPauseTask`, `Server.toolSearchWorkerTraces`, `Server.toolSpawnTask`, `Server.toolUpdateFindingReport`, `Server.unbindGoalMetDefault`, `Server.upgradeReporterTriggerMessage`, `reporterToolCallMessage`, `reporterToolCallMessageV1` |
| `server/platform_tools.go` | 52 | `Server.toolCreateCustomTool`, `Server.toolCreateMCP`, `Server.toolCreateSkill`, `Server.toolDeleteAssetsByHost`, `Server.toolUpdateCustomTool`, `Server.toolUpdateMCP`, `Server.toolUpdateSkillFile`, `customToolSchema`, `mcpSchema` |
| `server/finding_workflow.go` | 12 | `Server.agentFindingTrafficAccess`, `Server.seedFindingWorkflowTools`, `Server.toolBindFindingTraffic` |

## 공식 비교

baseline은 원본 프롬프트 그대로 둔다. 개선판의 언어 변경은 선언된 차이다([평가 프로토콜 §2](evaluation/evaluation-protocol.md#2-평가-대상-고정)). 결과는 개선판 전체의 효과로 보고, E②나 언어 변경 하나의 효과로 나누지 않는다.

## 확인할 가설

압축 전후에 언어가 섞이는 원인이 압축 지시라는 것은 가설이다. 실제 쓸 로컬 모델로 변경 전후를 비교해 확인한다.

## 열린 항목

- **SDK 문자열.** `norma`에도 한자 문자열 57개가 있다. 셸 도구(`NewShell*`)는 엔진이 쓰지 않지만, 백그라운드 작업 알림(`tool/task.go`)과 `noa/archive.go`는 모델까지 갈 수 있다. SDK를 올릴지, 끌지, 감쌀지 정한다.
- **이미 심긴 프롬프트.** `seedPrompts`는 비어 있을 때만 넣는다. 기존 설치의 중국어 프롬프트는 `Server.reseed*Prompt`로 올릴 수 있는지 2단계에서 확인한다. 평가 회차는 매번 새 DB라 영향이 없다.
- **활동 표시 필드.** `purpose`와 활동 표시 계약은 UI 디자인이 정해진 뒤에 한다.
- **승인 기록 표식 `[模型]`.** `intercept`가 저장하는 값이고 `db`의 분류와 `web/src/components/approval-records.tsx`가 함께 읽는다. 옛 행도 읽어야 하므로 웹 담당자와 같이 바꾼다.
- **가드 차단 문구.** `guard/guard.go`의 문구는 모델이 읽지만, 앞머리 표식을 `detections/`의 탐지 규칙과 지표도 쓴다. 표식을 바꿀지와 탐지 규칙을 어떻게 맞출지 정한 뒤 옮긴다.
