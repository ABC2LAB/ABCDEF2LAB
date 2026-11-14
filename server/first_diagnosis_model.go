package server

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/norma/llm"
	"iter"
	"strconv"
	"time"
)

var ErrFirstDiagnosisModel = errors.New("first_diagnosis_model_binding_unavailable")

// 첫 모드만 실제 단일 task/profile을 고정한다. ModelTag를 SDK 모델 이름으로 바꾸지 않는다.
type firstDiagnosisModelPin struct {
	TaskID        string
	TaskNumber    int64
	ExplorationID int64
	ProfileID     int64
}

func (*firstDiagnosisModelPin) String() string   { return "server.firstDiagnosisModelPin[protected]" }
func (*firstDiagnosisModelPin) GoString() string { return "server.firstDiagnosisModelPin[protected]" }

// 역할 pin 검사는 준비 입력의 실제 profile만 확인하며 task나 공급 권위를 만들지 않는다.
func CheckFirstDiagnosisModelRoleBinding(ctx context.Context, pool *sql.DB, expectedProfileID int64) error {
	if ctx == nil || ctx.Err() != nil || pool == nil || expectedProfileID <= 0 {
		return ErrFirstDiagnosisModel
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var valid bool
	err := pool.QueryRowContext(check, `SELECT EXISTS(SELECT 1 FROM llm_profiles WHERE id=$1)
 AND NOT EXISTS(SELECT 1 FROM agents WHERE key IN ('goals','planner','worker') AND llm_profile_id IS NOT NULL AND llm_profile_id<>$1)`, expectedProfileID).Scan(&valid)
	if err != nil || !valid || check.Err() != nil {
		return ErrFirstDiagnosisModel
	}
	return nil
}

// 이 단일 SQL snapshot은 실제 chain과 역할 우선순위를 함께 대조한다.
func readFirstDiagnosisModel(ctx context.Context, pool *sql.DB, taskID int64) (firstDiagnosisModelPin, error) {
	var out firstDiagnosisModelPin
	if ctx == nil || ctx.Err() != nil || pool == nil || taskID <= 0 {
		return out, ErrFirstDiagnosisModel
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var profile, active sql.NullInt64
	var chainCount, readyCount int
	var rolesMatch bool
	err := pool.QueryRowContext(check, `SELECT t.id,t.exploration_id,t.llm_profile_id,t.active_llm_profile_id,
 (SELECT count(*) FROM task_llm_profiles p WHERE p.task_id=t.id),
 (SELECT count(*) FROM task_llm_profiles p WHERE p.task_id=t.id AND p.profile_id=t.llm_profile_id AND p.position=0 AND p.status='ready'),
 NOT EXISTS(SELECT 1 FROM agents a WHERE a.key IN ('goals','planner','worker') AND a.llm_profile_id IS NOT NULL AND a.llm_profile_id<>t.llm_profile_id)
 FROM tasks t WHERE t.id=$1 AND t.deleted_at IS NULL`, taskID).Scan(&out.TaskNumber, &out.ExplorationID, &profile, &active, &chainCount, &readyCount, &rolesMatch)
	if err != nil || check.Err() != nil || out.TaskNumber != taskID || out.ExplorationID <= 0 || !profile.Valid || profile.Int64 <= 0 || !active.Valid || active.Int64 != profile.Int64 || chainCount != 1 || readyCount != 1 || !rolesMatch {
		return firstDiagnosisModelPin{}, ErrFirstDiagnosisModel
	}
	out.TaskID = strconv.FormatInt(out.TaskNumber, 10)
	out.ProfileID = profile.Int64
	return out, nil
}
func CheckFirstDiagnosisTaskProfileBinding(ctx context.Context, pool *sql.DB, taskID, expectedProfileID int64) error {
	if expectedProfileID <= 0 {
		return ErrFirstDiagnosisModel
	}
	actual, err := readFirstDiagnosisModel(ctx, pool, taskID)
	if err != nil || actual.ProfileID != expectedProfileID {
		return ErrFirstDiagnosisModel
	}
	return nil
}
func (s *Server) bindFirstDiagnosisModel(ctx context.Context, task *Task) error {
	if s == nil {
		return ErrFirstDiagnosisModel
	}
	if !s.firstDiagnosis {
		return nil
	}
	number, err := e2HTTPTaskIdentity(task)
	if err != nil || ctx == nil || ctx.Err() != nil || s.m == nil || s.m.pg == nil || !s.hasModelPolicy() || s.ctx == nil || s.ctx.Err() != nil {
		return ErrFirstDiagnosisModel
	}
	actual, err := readFirstDiagnosisModel(ctx, s.m.pg.DB, number)
	current, exists := s.m.Task(task.ID)
	if err != nil || actual.ExplorationID != task.ExpID || !exists || current != task {
		return ErrFirstDiagnosisModel
	}
	s.firstModelMu.Lock()
	defer s.firstModelMu.Unlock()
	if s.firstModel != nil {
		if *s.firstModel != actual {
			return ErrFirstDiagnosisModel
		}
		return nil
	}
	s.firstModel = &actual
	return nil
}
func (s *Server) checkFirstDiagnosisModel(ctx context.Context, task *Task) error {
	if s == nil {
		return ErrFirstDiagnosisModel
	}
	if !s.firstDiagnosis {
		return nil
	}
	number, err := e2HTTPTaskIdentity(task)
	if err != nil || ctx == nil || ctx.Err() != nil || s.m == nil || s.m.pg == nil || !s.hasModelPolicy() || s.ctx == nil || s.ctx.Err() != nil {
		return ErrFirstDiagnosisModel
	}
	s.firstModelMu.RLock()
	pin := s.firstModel
	if pin == nil {
		s.firstModelMu.RUnlock()
		return ErrFirstDiagnosisModel
	}
	expected := *pin
	s.firstModelMu.RUnlock()
	if expected.TaskID != task.ID || expected.TaskNumber != number || expected.ExplorationID != task.ExpID {
		return ErrFirstDiagnosisModel
	}
	current, exists := s.m.Task(task.ID)
	if !exists || current != task {
		return ErrFirstDiagnosisModel
	}
	actual, err := readFirstDiagnosisModel(ctx, s.m.pg.DB, number)
	if err != nil || actual != expected {
		return ErrFirstDiagnosisModel
	}
	return nil
}
func (s *Server) firstDiagnosisProfileID(ctx context.Context, task *Task) (int64, error) {
	if s == nil || !s.firstDiagnosis || s.checkFirstDiagnosisModel(ctx, task) != nil {
		return 0, ErrFirstDiagnosisModel
	}
	s.firstModelMu.RLock()
	defer s.firstModelMu.RUnlock()
	if s.firstModel == nil || s.firstModel.TaskID != task.ID {
		return 0, ErrFirstDiagnosisModel
	}
	return s.firstModel.ProfileID, nil
}

// 같은 provider 재시도도 실제 모델 호출 직전에 DB 결속을 다시 검사한다.
type firstDiagnosisModelProvider struct {
	server    *Server
	task      *Task
	profileID int64
	config    agent.Config
	inner     llm.Provider
}

func (*firstDiagnosisModelProvider) String() string {
	return "server.firstDiagnosisModelProvider[protected]"
}
func (*firstDiagnosisModelProvider) GoString() string {
	return "server.firstDiagnosisModelProvider[protected]"
}
func (p *firstDiagnosisModelProvider) check(ctx context.Context) error {
	if p == nil || p.server == nil || e2HTTPNil(p.inner) || ctx == nil || ctx.Err() != nil {
		return ErrFirstDiagnosisModel
	}
	id, err := p.server.firstDiagnosisProfileID(ctx, p.task)
	if err != nil || id != p.profileID {
		return ErrFirstDiagnosisModel
	}
	_, cfg, ok := p.server.providerForProfile(id)
	if !ok || cfg != p.config {
		return ErrFirstDiagnosisModel
	}
	return nil
}
func (p *firstDiagnosisModelProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if p == nil || p.server == nil {
		return llm.Message{}, "", llm.Usage{}, ErrFirstDiagnosisModel
	}
	budget := p.server.firstModelBudget
	if budget != nil {
		release, err := budget.acquireCall(ctx)
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		defer release()
	}
	// 대기 중 DB/profile 결속이 바뀌었을 수 있어 gate 획득 뒤 다시 확인한다.
	if p.check(ctx) != nil {
		return llm.Message{}, "", llm.Usage{}, ErrFirstDiagnosisModel
	}
	if budget == nil {
		return p.inner.Complete(ctx, req)
	}
	// rateLimited는 이 호출의 마지막 실패가 제공자 요청 한도(429, 청구 없음)였는지다. 그때는 더 길게 기다렸다 다시 보낸다.
	rateLimited := false
	once := func() (llm.Message, string, llm.Usage, error, error) {
		rateLimited = false
		reserved, err := budget.reserve(ctx, p.config, &req)
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, nil, err
		}
		call, done := budget.modelCallContext(ctx)
		defer done()
		message, reason, usage, callErr := p.inner.Complete(reserved.requestContext(call), req)
		settleErr := budget.settle(reserved, usage, callErr)
		rateLimited = callErr != nil && budget.callRateLimited(reserved)
		return message, reason, usage, callErr, settleErr
	}
	return firstDiagnosisRetryModelCallWith(ctx, budget.retryDelays, budget.rateLimitDelays, func() bool { return rateLimited }, once, budget.retryable)
}

// firstDiagnosisModelRetryDelays는 실패한 모델 호출을 다시 보내기 전 기다리는 시간이다(429·5xx·연결 실패 등). 원본 SDK가
// 하던 재시도를 되살리되, 다시 보낼 때마다 새로 예약하고 실패한 호출은 예약량으로 정산한다(P3 결정안 C05·C06 "다시 보낼
// 때는 같은 예약 규칙으로 새로 예약"). 제공자 요청 한도(429)는 몇 초 단위로 풀려서 짧게 두 번 기다린다. 예산마다 이 값을 가진다.
var firstDiagnosisModelRetryDelays = []time.Duration{2 * time.Second, 6 * time.Second}

// firstDiagnosisRateLimitRetryDelays는 마지막 실패가 제공자 요청 한도(429)일 때 다시 보내기 전 기다리는 시간이다. 429는
// 청구되지 않으므로(#584) 더 오래·더 여러 번 기다려도 승인 예산이 줄지 않는다. 2026-11-14 점검 회차에서 2초·6초 두 번으로는
// 요청 한도 구간을 넘기지 못해 준비 계획이 실패했고(429 여섯 번), 수십 분 뒤 같은 요청은 정상이었다. 합계는 준비 계획 상한
// (2분)보다 짧게 둔다.
var firstDiagnosisRateLimitRetryDelays = []time.Duration{2 * time.Second, 6 * time.Second, 15 * time.Second, 30 * time.Second}

// firstDiagnosisRetryModelCall은 호출 하나를 보내고, 호출이 실패했지만(callErr) 예산·승인이 막히지 않았으면 정한 간격으로
// 다시 보낸다. 예산 거부(예약 실패·상한)·막힘·문맥 취소는 다시 보내지 않는다. 마지막 결과의 예산 오류를 돌려준다.
func firstDiagnosisRetryModelCall(ctx context.Context, delays []time.Duration, once func() (llm.Message, string, llm.Usage, error, error), retryable func() bool) (llm.Message, string, llm.Usage, error) {
	return firstDiagnosisRetryModelCallWith(ctx, delays, nil, nil, once, retryable)
}

// firstDiagnosisRetryModelCallWith는 위와 같되, 마지막 실패가 요청 한도(rateLimited)이면 rateLimitDelays 간격을 쓴다.
// 시도 횟수는 두 간격에 함께 센다.
func firstDiagnosisRetryModelCallWith(ctx context.Context, delays, rateLimitDelays []time.Duration, rateLimited func() bool, once func() (llm.Message, string, llm.Usage, error, error), retryable func() bool) (llm.Message, string, llm.Usage, error) {
	for attempt := 0; ; attempt++ {
		message, reason, usage, callErr, budgetErr := once()
		if budgetErr == nil {
			return message, reason, usage, nil
		}
		schedule := delays
		if rateLimited != nil && rateLimitDelays != nil && rateLimited() {
			schedule = rateLimitDelays
		}
		if callErr == nil || attempt >= len(schedule) || ctx.Err() != nil || !retryable() {
			return llm.Message{}, "", usage, budgetErr
		}
		timer := time.NewTimer(schedule[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return llm.Message{}, "", usage, budgetErr
		case <-timer.C:
		}
	}
}
func (p *firstDiagnosisModelProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if p.check(ctx) != nil || p.server.firstModelBudget != nil {
			yield(llm.StreamEvent{}, ErrFirstDiagnosisModel)
			return
		}
		for event, err := range p.inner.Stream(ctx, req) {
			if !yield(event, err) {
				return
			}
		}
	}
}
