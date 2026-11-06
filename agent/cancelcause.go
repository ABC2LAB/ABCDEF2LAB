package agent

import (
	"context"
	"errors"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
// Short·Text 는 사람이 보는 한국어이고 ModelText 는 모델이 읽는 영어다. Code 는 저장·분기에 쓰는 값이라 바꾸지 않는다.
type AbortCause struct {
	Code      string
	Short     string
	Text      string
	ModelText string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text, modelText string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text, ModelText: modelText}
}

// NewCause builds a cause outside this package.
func NewCause(code, short, text, modelText string) *AbortCause {
	return cause(code, short, text, modelText)
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "사용자가 작업을 일시정지함",
		"사용자가 작업 제어 API(POST /api/tasks/{id}/control, action=pause)로 작업을 일시정지했다. 이번 Planner/Worker 실행은 취소되고, 실행 중인 의도는 frontier(open)로 돌아가 재개 뒤 처음부터 다시 실행한다",
		"The user paused the task through the task control API (POST /api/tasks/{id}/control, action=pause). This Planner/Worker run was cancelled; running intents return to the frontier (open) and are claimed again from the start after the task resumes")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "오케스트레이션 Agent가 작업을 일시정지함",
		"오케스트레이션 Agent가 pause_task 도구로 이 작업을 일시정지했다. 이번 Planner/Worker 실행은 취소되고, 실행 중인 의도는 frontier(open)로 돌아가 재개 뒤 다시 실행한다",
		"The orchestration Agent called pause_task to pause this task. This Planner/Worker run was cancelled; running intents return to the frontier (open) and run again after resuming")
	AbortTaskDeleted = cause("task_deleted", "작업이 삭제됨",
		"작업을 삭제하는 중이다(DELETE /api/tasks/{id}). 삭제 장벽이 이 작업의 Planner·Worker·메인 Agent 실행을 취소했고, 이번 실행 결과는 더 쓰지 않는다",
		"The task is being deleted (DELETE /api/tasks/{id}); the deletion barrier cancelled the task's running Planner, Worker and main Agent. The results of this run won't be used")
	AbortPausedOnReload = cause("paused_on_reload", "서버가 작업의 일시정지 상태를 복원함",
		"서버가 시작할 때 DB에 저장된 상태로 작업의 일시정지를 복원해 이번 실행이 취소됐다. 정상이라면 복원 단계에는 실행 중인 Agent가 없다",
		"On startup the backend restored the task's paused state from the database, so this run was cancelled. Normally no Agent is running during restoration")
	AbortGoalMet = cause("goal_met", "Planner가 작업 목표 달성으로 판정함",
		"Planner가 작업 목표 달성으로 판정해 작업을 done으로 두고 아직 실행 중인 Worker를 취소했다. 이 의도들은 실패가 아니라 stopped로 표시한다",
		"The planner judged the task goals met and set the task to done, then cancelled the Workers still running; those intents are marked stopped, not failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "작업 시간 초과 마무리 대기를 다 씀",
		"작업이 timeout에 도달한 뒤 실행 중인 Worker의 마무리를 기다렸지만 90초 drain 유예로도 부족해 강제로 취소했다. 의도는 exhausted로 표시하고, 마무리 단계에서 쓴 사실과 자산은 유지한다",
		"After the task reached its timeout, the engine waited for running Workers to wrap up, but the 90-second drain grace wasn't enough, so it cancelled them hard; intents are marked exhausted, and facts and assets already written during wrap-up are kept")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "Planner가 이 의도를 종료함",
		"Planner가 kill_work로 이 의도를 종료했다. 보통 방향이 어긋났거나 더 진행할 가치가 없다는 뜻이다. 의도는 stopped로 표시하고 자동으로 다시 가져가지 않는다",
		"The planner called kill_work to end this intent, which usually means the direction drifted or has no further value; the intent is marked stopped and isn't claimed again automatically")
	AbortWorkPausedByUser = cause("work_paused_by_user", "사용자가 이 Worker 의도를 일시정지함",
		"사용자가 실행 중인 Worker를 일시정지했다. 이번 호출은 취소되고 의도는 paused가 된다. 이미 등록한 의도·사실·취약점·활동 기록은 모두 유지하고, 재개하면 처음부터 다시 실행한다",
		"The user paused the running Worker. This call was cancelled and the intent became paused; registered intents, facts, vulnerabilities and activity records are all kept, and it runs again from the start after resuming")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "사용자가 이 Worker 의도를 삭제함",
		"사용자가 실행 중인 Worker를 삭제했다. 이번 호출은 취소되고, Worker가 쓰기 구간을 벗어나면 서버가 사용자가 고른 삭제 방식으로 처리한다. 표시 삭제는 삭제 표시만 하고 산출물을 모두 유지하며, 실제 삭제는 이 의도와 이 의도만 뒷받침하는 하위 노드를 함께 지운다",
		"The user deleted the running Worker. This call was cancelled; after the Worker leaves the write section, the server handles the intent by the delete mode the user chose: a soft delete only marks it deleted and keeps all output, and a hard delete cascades to the intent and the downstream nodes supported only by it")
	AbortWorkFinished = cause("work_finished", "Worker가 정상 종료해 context를 해제함",
		"Worker가 정상 종료해 엔진이 detachWork에서 context를 해제했다. 실행 중단이 아니다. 중단 메시지에 보이면 취소와 마무리 이벤트가 경합한 것이다",
		"The Worker finished normally and the engine released its context in detachWork. This is not an interruption; if it appears in an interruption message, cancellation raced with the wrap-up event")
	AbortPausedRaceGuard = cause("paused_race_guard", "작업 일시정지 중 새 실행을 거부함",
		"작업이 일시정지 상태라 엔진이 새 실행 context를 내주지 않았다. claim과 일시정지의 경합으로 Worker가 계속 시작되는 것을 막는다. 이미 가져간 의도는 frontier로 돌아간다",
		"While the task is paused, the engine refused to issue a new execution context, which prevents a race between claim and pause from starting a Worker; claimed intents return to the frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "사용자가 이번 대화를 멈춤",
		"사용자가 멈춤을 눌러 이번 메인 Agent 또는 대화 Agent 실행을 중단했다. 이미 만든 활동 기록은 유지하고 다음 메시지를 보낼 수 있다",
		"The user clicked stop and aborted this round of the main or conversation Agent. Activity records already produced are kept, and the next message can be sent")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "작업 일시정지로 메인 Agent 대화가 중단됨",
		"사용자가 작업을 일시정지해 실행 중인 메인 Agent 대화도 함께 취소했다. 이미 만든 활동 기록은 유지하고, 재개해도 이번 메시지를 자동으로 다시 보내지 않는다",
		"When the user paused the task, the running main Agent conversation was cancelled with it. Activity records already produced are kept, and this round's message isn't replayed automatically after resuming")
	AbortChatTurnFinished = cause("chat_turn_finished", "이번 대화가 정상 종료해 context를 해제함",
		"이번 대화가 정상 종료해 서버가 context를 해제하는 중이다. 실행 중단이 아니다. 중단 메시지에 보이면 취소와 마무리 이벤트가 경합한 것이다",
		"This round of conversation finished normally and the server is releasing its context. This is not an interruption; if it appears in an interruption message, cancellation raced with the wrap-up event")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "서버 프로세스가 종료 중임",
		"서버 프로세스가 SIGINT나 SIGTERM을 받아 재시작·갱신·종료하는 중이다. 실행 중인 Agent를 모두 취소하고, 재시작 뒤 남은 running 의도는 open으로 돌려 다시 실행한다",
		"The backend process received SIGINT or SIGTERM and is restarting, updating or shutting down. All running Agents are cancelled; running intents left after the restart are reset to open and run again")
	AbortRunHardTimeout = cause("run_hard_timeout", "단일 실행 강제 시간 초과가 작동함",
		"단일 실행이 소프트 벽시계 예산과 추가 유예를 넘겼다. 모델 요청이나 도구가 오래 돌아오지 않아 정상 턴 경계 마무리를 못 한 것이다. 중단 전 마지막으로 돌아오지 않은 도구 호출을 먼저 확인한다",
		"A single run exceeded the soft wall-clock budget and the extra grace, which means a model request or a tool didn't return for a long time and the normal turn-boundary wrap-up couldn't run. Check the last unreturned tool call before the interruption first")
)

// abortCauseOf resolves the named cause attached to a cancelled run context. 이름을 붙이지 않은 취소도 같은 형태로 돌려준다.
func abortCauseOf(ctx context.Context) *AbortCause {
	c := context.Cause(ctx)
	if c == nil {
		return nil
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return cause("deadline_exceeded", "상위 context가 deadline에 도달함",
			"상위 context가 deadline에 도달했지만 설정한 쪽이 WithTimeoutCause로 원인을 붙이지 않았다: "+c.Error(),
			"The upstream context reached its deadline, but the side that set it attached no named cause through WithTimeoutCause: "+c.Error())
	case errors.Is(c, context.Canceled):
		return cause("canceled_no_cause", "취소한 쪽이 원인을 붙이지 않음",
			"상위 context가 취소됐지만 취소한 쪽이 context.WithCancelCause로 원인을 붙이지 않았다. agent/cancelcause.go에 이름을 등록하고 원인을 넘겨야 한다",
			"The upstream context was cancelled, but the cancelling side attached no named cause through context.WithCancelCause; register a name in agent/cancelcause.go and pass the cause")
	default:
		return cause("other", firstLine(c.Error(), 80), c.Error(), c.Error())
	}
}

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	ac := abortCauseOf(ctx)
	if ac == nil {
		return "", "", "", false
	}
	return ac.Code, ac.Short, ac.Text, true
}
