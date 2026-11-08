# 엔진 언어 전환 계획

상태: 진행 중. 최초 계획 기준은 dev `adca969`(2026-11-06)다. 리뷰어는 이동찬이다.
#264·#266·#267·#268·#269·#271·#272는 dev `450cc71`까지 병합됐다.
실제 모델 검증과 아래 열린 항목이 남아 있어 전체 완료가 아니다.

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

`seedPrompts`는 비어 있을 때만 넣으므로 기존 설치에는 옛 프롬프트가 남는다. 기동 때 `Server.reseedBuiltinPrompts`가 알려진 옛 기본값과 정확히 같은 내장 프롬프트만 현재 기본값으로 올린다. 커스텀 본문과 current 선택은 유지한다. `Server.upgradeReporterTriggerMessage`도 옛 기본값 정확 대조와 `report_finding` 도구 결속을 함께 확인한다. 도구의 설명·스키마는 각 칸의 옛 기본값을 따로 대조하고, JSONB는 정규화해 비교한다. 동시 편집도 덮어쓰지 않는다. DB 내장 규칙·자산 메모 역시 알려진 옛 기본값만 갱신한다. 평가 회차는 매번 새 DB라 시드 값을 그대로 쓴다.

## 공식 비교

baseline은 원본 프롬프트 그대로 둔다. 개선판의 언어 변경은 선언된 차이다([평가 프로토콜 §2](evaluation/evaluation-protocol.md#2-평가-대상-고정)). 결과는 개선판 전체의 효과로 보고, E②나 언어 변경 하나의 효과로 나누지 않는다.

## 번역 계층 (8단계)

표시 언어는 한국어로 고정했고 카탈로그는 지웠다. `trMsg`·`logT`·`config.T`·`SetCurrentLanguage`·`notify.SetLanguageResolver`와 `output_language` API, 웹의 언어 선택은 아직 남아 있으며 별도 PR로 지운다. `agent/outputlang.go`는 #264 뒤에 줄인다.

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
- **가드 차단 문구.** 모델용 차단 결과를 영어로 바꾸고, 설정 오류와 빈 규칙 메시지도 표시용 한국어와 분리한다. 운영자가 직접 쓴 규칙 메시지는 유지한다. 현재 표식은 `[ARTEX PLATFORM CONTROL - NOT TARGET DEFENSE]`이며, `detections/`는 [과거 표식](../detections/indicators/legacy_guard_marker.md)도 계속 탐지한다. 감사 행에는 표시용 사유를 별도로 남기므로 표식이 없을 수 있다. 합성 회귀와 실제 모델 검증은 구분한다.

## E② Worker 진단 지침과 적용 식별

일반 worker 프롬프트는 E② 실행에 적용하지 않는다. 일반 Worker의
report_finding, 직접 파일·추가 도구 지시를 E②에 그대로 가져오지 않기 위해
DB의 **E② 실행 지침**(e2_worker)을 별도로 편집한다. 기존 agent prompt
버전 저장·현재 본문 조회·기본값 복원 경로를 사용하며 DB 스키마는 추가하지 않는다.
기동 시 새 행과 기본 본문을 비어 있을 때만 심는다. 운영 DB 변경은 배포 작업이다.

- 편집 가능한 부분: 가설·정상 대조·관찰·다음 검사 선택 등 진단 지침.
  변수는 WorkerName, Now만 지원한다.
- 코드 소유 부분: 고정 E② 통제 지시, 한국어 표시 규칙, 실제 도구 allowlist,
  격리·승인·예산·감사 및 unknown 재시도 제한. 지침 편집으로 변경하지 않는다.
- 각 Worker 실행 시작에 현재 e2_worker를 한 번 읽는다. 비어 있거나 조회할 수
  없으면 기본값을 쓰며, 템플릿 오류는 builtin_invalid_override로 구별한다.
  일반 prompt 조회 hook은 DB 오류와 없는 행을 구분하지 않으므로 builtin은
  DB 조회 성공을 보장하는 값이 아니다.
- 모델 호출 직전 e2_prompt 활동에 version=e2-worker-prompt/v1, key,
  source, template_digest, guidance_digest, control_digest,
  system_digest, tools_digest를 기록한다. digest는 SHA-256 내용 버전이며
  DB의 숫자 버전 ID가 아니다. template/guidance는 보호된 비밀값을 가린 뒤
  산출하고 system은 배정 intent를 포함한 실제 system 본문 기준이다.
  tools는 실제 허용된 도구 이름·설명·prompt·입력 schema 기준이며
  DB 도구 카탈로그를 편집했다고 E②에 적용됐다고 가정하지 않는다.
- 원문 prompt·자격정보는 이 활동에 넣지 않는다. 활동은 적용 식별 관찰이며
  E② 송신 감사의 대체물이 아니다. 활동 저장 실패의 durable 증명과 모델
  제공자 내부 변환까지 증명하는 기능은 아니다.

프롬프트 비교 실험은 source와 적용 digest를 함께 보존하고 동일 모델·샘플링·
실행 조건에서 별도로 측정한다. 이 연결의 합성 시험 통과만으로 실모델 진단
성능 개선, finding 저장, 보고서 완성 또는 사전 승인 UI 완성을 주장하지 않는다.
