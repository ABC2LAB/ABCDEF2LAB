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

## 종료 사유와 취소 원인 (4단계)

코드와 enum 값(`AbortCause.Code`, `harness.TerminalReason`)은 그대로 둔다. 글 요약 없이 끝난 실행의 결과 행은 `summary`·`detail`에 모델이 읽는 영어를 저장하고, 화면용 한국어를 `metadata`의 `display_summary`·`display_detail`에 함께 둔다. 모델이 읽는 경로는 여러 곳이지만 화면 경계는 `db.Activity.ForDisplay`(API·SSE)와 상세 조회(`ActivityDisplayDetailWithSources`) 둘이기 때문이다. 대화 활동은 화면만 읽으므로 저장할 때 한국어로 바꾼다. 모델이 쓴 최종 답은 그대로 둔다.

이 변경 전에 저장된 결과 행에는 화면용 글이 없어 옛 문구 그대로 보인다.

## 이미 심긴 프롬프트

`seedPrompts`는 비어 있을 때만 넣으므로 기존 설치에는 옛 프롬프트가 남는다. 기동 때 `Server.reseedBuiltinPrompts`가 내장 에이전트와 reporter 프롬프트를 한 번 현재 기본값으로 올린다. 새 버전을 덧붙이는 방식이라 운영자가 고친 이전 본문은 버전 기록에서 되찾는다. 트리거에는 버전 기록이 없어서 `Server.upgradeReporterTriggerMessage`는 한자가 든 문구만 바꾼다. 도구 설명도 처음 넣을 때만 쓰이고 모델은 DB 행을 받는다. 도구 행에도 버전 기록이 없어서 `Server.refreshHanToolDescriptions`가 설명이나 스키마에 한자가 있는 시스템 도구만 한 번 현재 기본값으로 올린다. 평가 회차는 매번 새 DB라 시드 값을 그대로 쓴다.

## 공식 비교

baseline은 원본 프롬프트 그대로 둔다. 개선판의 언어 변경은 선언된 차이다([평가 프로토콜 §2](evaluation/evaluation-protocol.md#2-평가-대상-고정)). 결과는 개선판 전체의 효과로 보고, E②나 언어 변경 하나의 효과로 나누지 않는다.

## 확인할 가설

압축 전후에 언어가 섞이는 원인이 압축 지시라는 것은 가설이다. 실제 쓸 로컬 모델로 변경 전후를 비교해 확인한다.

## 내부 추론 (6단계)

| 경로 | 상태 |
|---|---|
| 활동 기록(`activity`, `conversation_activities`) | 새로 쓰지 않는다(`captureRunSession`). 이전 실행의 행은 지우지 않고 목록·페이지·상세 조회에서 뺀다 |
| API·화면 | 위 조회를 거치므로 추론이 나가지 않는다. `web/src/components/transcript.tsx`의 표시 분기는 10단계에서 정리한다 |
| 서버 로그 | 활동 로그는 활동 행의 값을 쓰므로 추론이 없다 |
| 사용량 | `usage` 행에 토큰 수만 남긴다 |
| LLM 호출 기록(`llmrec`) | 기본 꺼짐. 켜도 `thinking`은 가린다 |
| **transcript 파일** | **아직 저장한다.** SDK의 `transcript.Writer.RecordMessage`가 메시지를 통째로 쓴다 |
| **noa 보관 파일** | **아직 저장한다.** noa를 켰을 때 `noa/archive.go`가 추론을 함께 쓴다 |

transcript는 세션 재개에 쓰여서 끌 수 없다. 모델의 추론을 끄면 진단 결과가 달라지므로 쓰지 않는다. 남은 선택지는 둘이다.

1. 회차가 끝나면 파일에서 추론 블록을 지운다. SDK를 안 바꾸지만 실행 중에는 디스크에 남는다.
2. SDK를 고쳐 처음부터 쓰지 않는다. 열린 항목의 SDK 문자열도 같이 풀리지만 `go.mod`에 `replace`와 포크 관리가 필요하다.

어느 쪽이든 추론 없이 재개했을 때 제공자가 요청을 받는지 실제 모델로 시험한다. OpenAI 호환 경로의 SDK는 빠진 `reasoning_content`를 자리값으로 채운다.

## 열린 항목

- **SDK 문자열.** `norma`에도 한자 문자열 57개가 있다. 셸 도구(`NewShell*`)는 엔진이 쓰지 않지만, 백그라운드 작업 알림(`tool/task.go`)과 `noa/archive.go`는 모델까지 갈 수 있다. SDK를 올릴지, 끌지, 감쌀지 정한다.
- **활동 표시 필드.** `purpose`와 활동 표시 계약은 UI 디자인이 정해진 뒤에 한다.
- **승인 기록 표식 `[模型]`.** `intercept`가 저장하는 값이고 `db`의 분류와 `web/src/components/approval-records.tsx`가 함께 읽는다. 옛 행도 읽어야 하므로 웹 담당자와 같이 바꾼다.
- **가드 차단 문구.** `guard/guard.go`의 문구는 모델이 읽지만, 앞머리 표식을 `detections/`의 탐지 규칙과 지표도 쓴다. 표식을 바꿀지와 탐지 규칙을 어떻게 맞출지 정한 뒤 옮긴다.
