//go:build linux

package diagnosisprep

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"
)

const maxJobs = 1024

type storedInspection struct {
	Scope              *Scope `json:"scope,omitempty"`
	ReasonCode         string `json:"reason_code,omitempty"`
	InstallationDigest string `json:"installation_digest,omitempty"`
	DelegationRef      string `json:"delegation_ref,omitempty"`
	MaxRuns            int64  `json:"max_runs"`
	AggregateBudget    Budget `json:"aggregate_budget"`
}

func storeInspection(i Inspection) storedInspection {
	return storedInspection{i.Scope, i.ReasonCode, i.InstallationDigest, i.DelegationRef, i.MaxRuns, i.AggregateBudget}
}
func (i storedInspection) runtime() Inspection {
	return Inspection{i.Scope, i.ReasonCode, i.InstallationDigest, i.DelegationRef, i.MaxRuns, i.AggregateBudget}
}

type receipt struct {
	ExternalTransferConfirmed bool             `json:"external_transfer_confirmed,omitempty"`
	Principal                 string           `json:"principal"`
	CreateKey                 string           `json:"create_key"`
	StartKey                  string           `json:"start_key,omitempty"`
	Inspection                storedInspection `json:"inspection"`
	Job                       Job              `json:"job"`
}
type executionHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type Service struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	backend    Backend
	journal    *journal
	jobs       map[string]receipt
	executions map[string]executionHandle
	closed     bool
	failed     bool
	released   bool
}

func sourceDigest(input Input, installation string) string { return SourceDigest(input, installation) }
func cloneReceipt(r receipt) receipt {
	raw, _ := json.Marshal(r)
	var copy receipt
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func sameInspection(a, b storedInspection) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
func inspectionOK(i Inspection) bool {
	if !validScope(i.Scope) || (i.ReasonCode != "" && !reasonPattern.MatchString(i.ReasonCode)) {
		return false
	}
	if i.Scope == nil {
		return i.InstallationDigest == "" || digestPattern.MatchString(i.InstallationDigest)
	}
	return digestPattern.MatchString(i.InstallationDigest) && reference.MatchString(i.DelegationRef) && i.MaxRuns > 0 && validBudget(i.AggregateBudget) && withinBudget(Budget{}, i.Scope.Budget, i.AggregateBudget)
}
func withinBudget(used, add, limit Budget) bool {
	a := []int64{used.Requests, used.ModelRequests, used.InputTokens, used.OutputTokens, used.CostUSDMicros, used.DurationSeconds}
	b := []int64{add.Requests, add.ModelRequests, add.InputTokens, add.OutputTokens, add.CostUSDMicros, add.DurationSeconds}
	c := []int64{limit.Requests, limit.ModelRequests, limit.InputTokens, limit.OutputTokens, limit.CostUSDMicros, limit.DurationSeconds}
	for n := range a {
		if a[n] < 0 || b[n] < 0 || c[n] < 0 || a[n] > c[n] || b[n] > c[n]-a[n] {
			return false
		}
	}
	return true
}
func addBudget(a, b Budget) Budget {
	return Budget{a.Requests + b.Requests, a.ModelRequests + b.ModelRequests, a.InputTokens + b.InputTokens, a.OutputTokens + b.OutputTokens, a.CostUSDMicros + b.CostUSDMicros, a.DurationSeconds + b.DurationSeconds}
}

// New는 보호된 설치 루트만 받는다. 이전 실행을 다시 호출하거나 권위를 재발급하지 않는다.
func New(ctx context.Context, root string, backend Backend) (*Service, error) {
	if ctx == nil || ctx.Err() != nil || backend == nil {
		return nil, ErrProtocol
	}
	j, records, err := openJournal(root)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	s := &Service{ctx: lifetime, cancel: cancel, backend: backend, journal: j, jobs: map[string]receipt{}, executions: map[string]executionHandle{}}
	fail := func() (*Service, error) { cancel(); _ = j.close(); return nil, errStorage }
	createKeys := map[string]string{}
	for _, raw := range records {
		var r receipt
		if strictDecode(raw, &r) != nil || !validText(r.Principal, 256) || !reference.MatchString(r.CreateKey) || !validJob(r.Job) || !inspectionOK(r.Inspection.runtime()) || r.Job.SourceDigest != sourceDigest(r.Job.Input, r.Inspection.InstallationDigest) || !reflect.DeepEqual(r.Job.Scope, r.Inspection.Scope) || (r.StartKey != "" && (!reference.MatchString(r.StartKey) || (r.Job.Scope != nil && r.Job.Scope.Model != nil && r.Job.Scope.Model.ExternalTransfer && !r.ExternalTransferConfirmed))) || r.Job.PreparationStarted != (r.StartKey != "") {
			return fail()
		}
		key := r.Principal + "\x00" + r.CreateKey
		if id, ok := createKeys[key]; ok && id != r.Job.ID {
			return fail()
		}
		if old, exists := s.jobs[r.Job.ID]; exists {
			if !validReceiptChange(old, r) {
				return fail()
			}
		} else if r.Job.Revision != 1 || r.StartKey != "" {
			return fail()
		}
		createKeys[key] = r.Job.ID
		s.jobs[r.Job.ID] = r
		if len(s.jobs) > maxJobs {
			return fail()
		}
	}
	// 실행 중단의 결과를 추정하지 않는다. 저장된 사실을 유지하고 재실행을 금지한다.
	for id, r := range s.jobs {
		if active(r.Job.State) {
			r.Job = interruptedJob(r.Job, "service_interrupted")
			if err := s.save(r); err != nil {
				return fail()
			}
			s.jobs[id] = r
		}
	}
	return s, nil
}
func validReceiptChange(old, next receipt) bool {
	if old.Principal != next.Principal || old.CreateKey != next.CreateKey || old.Job.ID != next.Job.ID || next.Job.Revision != old.Job.Revision+1 || old.Job.Input != next.Job.Input {
		return false
	}
	if old.StartKey != "" {
		if old.ExternalTransferConfirmed != next.ExternalTransferConfirmed || old.StartKey != next.StartKey || old.Job.SourceDigest != next.Job.SourceDigest || !sameInspection(old.Inspection, next.Inspection) || !monotonic(old.Job, next.Job) {
			return false
		}
		if terminal(old.Job.State) || old.Job.State == Unknown {
			return reflect.DeepEqual(old.Job, next.Job)
		}
	}
	return true
}
func (s *Service) save(r receipt) error {
	raw, err := json.Marshal(r)
	if err != nil || s.failed || s.released {
		return errStorage
	}
	if err = s.journal.append(raw); err != nil {
		s.failed = true
		s.cancel()
		return errStorage
	}
	s.jobs[r.Job.ID] = cloneReceipt(r)
	return nil
}
func responseError(r Request, code string) Response {
	return Response{Version: Version, RequestID: r.RequestID, Error: code}
}
func responseJob(r Request, j Job) Response {
	copy := cloneReceipt(receipt{Job: j}).Job
	return Response{Version: Version, RequestID: r.RequestID, Job: &copy}
}
func (s *Service) Call(ctx context.Context, r Request) (Response, error) {
	if ValidateRequest(r) != nil {
		return responseError(r, "invalid_request"), nil
	}
	if ctx == nil || ctx.Err() != nil {
		return Response{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.released {
		return responseError(r, "unavailable"), nil
	}
	if s.closed || s.ctx.Err() != nil {
		return responseError(r, "service_closed"), nil
	}
	if ctx.Err() != nil {
		return Response{}, ErrUnavailable
	}
	switch r.Command {
	case Catalog:
		profiles, err := s.backend.Profiles(ctx, r.Principal)
		if err != nil {
			return responseError(r, "unavailable"), nil
		}
		res := Response{Version: Version, RequestID: r.RequestID, Profiles: profiles}
		if ValidateResponse(r, res) != nil {
			return responseError(r, "unavailable"), nil
		}
		return res, nil
	case Create:
		input, _ := CanonicalInput(*r.Input)
		for _, existing := range s.jobs {
			if existing.Principal == r.Principal && existing.CreateKey == r.IdempotencyKey {
				if existing.Job.Input != input {
					return responseError(r, "idempotency_conflict"), nil
				}
				return responseJob(r, existing.Job), nil
			}
		}
		if len(s.jobs) >= maxJobs {
			return responseError(r, "capacity"), nil
		}
		inspect, err := s.backend.Inspect(ctx, r.Principal, input)
		if err != nil || !inspectionOK(inspect) {
			return responseError(r, "unavailable"), nil
		}
		var entropy [16]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return responseError(r, "unavailable"), nil
		}
		job := Job{ID: "prep-" + hex.EncodeToString(entropy[:]), Revision: 1, SourceDigest: sourceDigest(input, inspect.InstallationDigest), Input: input, State: AwaitingApproval, Phase: DraftPhase, ReasonCode: inspect.ReasonCode, Scope: inspect.Scope, AuthorityState: AuthorityNotIssued}
		if inspect.Scope != nil && inspect.ReasonCode == "" && inspect.Scope.ExpiresAt.After(time.Now()) {
			job.State = Ready
		} else if job.ReasonCode == "" {
			job.ReasonCode = "approval_required"
		}
		record := receipt{Principal: r.Principal, CreateKey: r.IdempotencyKey, Job: job, Inspection: storeInspection(inspect)}
		if err := s.save(record); err != nil {
			return responseError(r, "unavailable"), nil
		}
		return responseJob(r, job), nil
	case Lookup:
		for _, j := range s.jobs {
			if j.Principal == r.Principal && j.CreateKey == r.IdempotencyKey {
				return responseJob(r, j.Job), nil
			}
		}
		return responseError(r, "not_found"), nil
	case AuthorizeTask:
		allowed := false
		var owned *Job
		for _, j := range s.jobs {
			if j.Principal == r.Principal && j.Job.TaskID == r.TaskID {
				allowed = true
				copy := cloneReceipt(j).Job
				owned = &copy
				break
			}
		}
		return Response{Version: Version, RequestID: r.RequestID, Authorized: &allowed, Job: owned}, nil
	}
	record, exists := s.jobs[r.JobID]
	if !exists || record.Principal != r.Principal {
		return responseError(r, "not_found"), nil
	}
	switch r.Command {
	case Status:
		return responseJob(r, record.Job), nil
	case Validate:
		if r.ExpectedRevision != record.Job.Revision {
			return responseError(r, "revision_conflict"), nil
		}
		if record.StartKey != "" {
			return responseJob(r, record.Job), nil
		}
		inspect, err := s.backend.Inspect(ctx, r.Principal, record.Job.Input)
		if err != nil || !inspectionOK(inspect) {
			return responseError(r, "unavailable"), nil
		}
		next := cloneReceipt(record)
		next.Inspection = storeInspection(inspect)
		next.Job.Scope = inspect.Scope
		next.Job.SourceDigest = sourceDigest(next.Job.Input, inspect.InstallationDigest)
		next.Job.ReasonCode = inspect.ReasonCode
		next.Job.State = AwaitingApproval
		if inspect.Scope != nil && inspect.ReasonCode == "" && inspect.Scope.ExpiresAt.After(time.Now()) {
			next.Job.State = Ready
		} else if next.Job.ReasonCode == "" {
			next.Job.ReasonCode = "approval_required"
		}
		if !sameInspection(record.Inspection, next.Inspection) || !reflect.DeepEqual(record.Job, next.Job) {
			next.Job.Revision++
			if err := s.save(next); err != nil {
				return responseError(r, "unavailable"), nil
			}
		}
		return responseJob(r, next.Job), nil
	case Start:
		for _, other := range s.jobs {
			if other.Principal == r.Principal && other.StartKey == r.IdempotencyKey && other.Job.ID != record.Job.ID {
				return responseError(r, "idempotency_conflict"), nil
			}
		}
		if record.StartKey != "" {
			if record.StartKey != r.IdempotencyKey {
				return responseError(r, "idempotency_conflict"), nil
			}
			return responseJob(r, record.Job), nil
		}
		if record.Job.Revision != r.ExpectedRevision {
			return responseError(r, "revision_conflict"), nil
		}
		if record.Job.State != Ready || record.Job.Scope == nil || (record.Job.Scope.Model != nil && record.Job.Scope.Model.ExternalTransfer && !r.ExternalTransferConfirmed) {
			return responseError(r, "approval_required"), nil
		}
		inspect, err := s.backend.Inspect(ctx, r.Principal, record.Job.Input)
		if err != nil || !inspectionOK(inspect) {
			return responseError(r, "unavailable"), nil
		}
		if !sameInspection(record.Inspection, storeInspection(inspect)) || sourceDigest(record.Job.Input, inspect.InstallationDigest) != record.Job.SourceDigest {
			return responseError(r, "approval_changed"), nil
		}
		if inspect.Scope == nil || inspect.ReasonCode != "" || !inspect.Scope.ExpiresAt.After(time.Now()) {
			return responseError(r, "approval_required"), nil
		}
		var used Budget
		var runs int64
		for _, other := range s.jobs {
			// 결과 불명(Unknown)은 backend.Execute가 이미 반환한 끝난 회차라 실행 자원이 겹치지 않는다. 진행 중(active)
			// 회차만 동시 실행을 막고, Unknown은 아래 집계 분기에서 예약 예산·run 수를 그대로 계수해 설치 전체를 brick하지 않는다
			// (C04). 미확인 결과·예약은 해제하지 않는다(절대 규칙 5).
			if active(other.Job.State) {
				return responseError(r, "busy"), nil
			}
			if other.StartKey != "" && other.Inspection.DelegationRef == inspect.DelegationRef {
				runs++
				b := other.Inspection.Scope.Budget
				if !withinBudget(used, b, inspect.AggregateBudget) {
					return responseError(r, "budget_exhausted"), nil
				}
				used = addBudget(used, b)
			}
		}
		if runs >= inspect.MaxRuns || !withinBudget(used, inspect.Scope.Budget, inspect.AggregateBudget) {
			return responseError(r, "budget_exhausted"), nil
		}
		if s.ctx.Err() != nil {
			return responseError(r, "service_closed"), nil
		}
		record = cloneReceipt(record)
		record.StartKey = r.IdempotencyKey
		record.ExternalTransferConfirmed = r.ExternalTransferConfirmed
		record.Job.Revision++
		record.Job.PreparationStarted = true
		record.Job.State = Preparing
		record.Job.Phase = ScopeCheck
		record.Job.ReasonCode = ""
		// 이 fsync는 task·모델·권위 생성보다 앞서며 HTTP context에 종속되지 않는다.
		if err := s.save(record); err != nil {
			return responseError(r, "unavailable"), nil
		}
		runCtx, cancel := context.WithCancel(s.ctx)
		done := make(chan struct{})
		s.executions[record.Job.ID] = executionHandle{cancel, done}
		go s.execute(runCtx, cloneReceipt(record), done)
		return responseJob(r, record.Job), nil
	case Stop:
		if r.ExpectedRevision != record.Job.Revision {
			return responseError(r, "revision_conflict"), nil
		}
		if !active(record.Job.State) {
			if record.Job.State == Unknown {
				if h, ok := s.executions[record.Job.ID]; ok {
					h.cancel()
				}
			}
			return responseJob(r, record.Job), nil
		}
		if record.Job.State != Stopping {
			record.Job.State = Stopping
			record.Job.ReasonCode = "stop_requested"
			record.Job.Revision++
			if err := s.save(record); err != nil {
				return responseError(r, "unavailable"), nil
			}
		}
		if h, ok := s.executions[record.Job.ID]; ok {
			h.cancel()
		}
		return responseJob(r, record.Job), nil
	}
	return responseError(r, "invalid_request"), nil
}
func authorityRank(a AuthorityState) int {
	switch a {
	case AuthorityNotIssued:
		return 0
	case AuthorityIssued:
		return 1
	case AuthorityConsumed:
		return 2
	default:
		return -1
	}
}
func phaseRank(p Phase) int {
	switch p {
	case DraftPhase:
		return 0
	case ScopeCheck:
		return 1
	case TaskCreation:
		return 2
	case Planning:
		return 3
	case Authority:
		return 4
	case Gateway:
		return 5
	case ExecutionPhase:
		return 6
	case Closure:
		return 7
	case Report:
		return 8
	}
	return -1
}
func monotonic(old, next Job) bool {
	if phaseRank(next.Phase) < phaseRank(old.Phase) {
		return false
	}

	if old.TaskID != "" && next.TaskID != old.TaskID || old.PreparationStarted && !next.PreparationStarted || old.Started && !next.Started || old.DrainComplete && !next.DrainComplete || old.ResultReady && !next.ResultReady {
		return false
	}
	if old.AuthorityState == AuthorityConsumed && next.AuthorityState != AuthorityConsumed {
		return false
	}
	if old.AuthorityState == AuthorityUnknown && next.AuthorityState != AuthorityUnknown {
		return false
	}
	if next.AuthorityState != AuthorityUnknown && authorityRank(next.AuthorityState) < authorityRank(old.AuthorityState) {
		return false
	}
	if old.ModelUsage != nil {
		if next.ModelUsage == nil {
			return false
		}
		a, b := old.ModelUsage, next.ModelUsage
		if b.ReservedCostUSDMicros < a.ReservedCostUSDMicros || b.Requests < a.Requests || b.InputTokens < a.InputTokens || b.OutputTokens < a.OutputTokens || b.ReservedInputTokens < a.ReservedInputTokens || b.ReservedOutputTokens < a.ReservedOutputTokens || b.CostUSDMicros < a.CostUSDMicros || b.UnknownUsageCalls < a.UnknownUsageCalls {
			return false
		}
	}
	return true
}
func (s *Service) publish(id string, u Update) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.jobs[id]
	if !ok || s.failed || s.released || terminal(old.Job.State) || old.Job.State == Unknown {
		return ErrUnavailable
	}
	next := cloneReceipt(old)
	next.Job.State = u.State
	next.Job.Phase = u.Phase
	next.Job.ReasonCode = u.ReasonCode
	next.Job.TaskID = u.TaskID
	next.Job.PreparationStarted = u.PreparationStarted
	next.Job.AuthorityState = u.AuthorityState
	next.Job.Started = u.Started
	next.Job.DrainComplete = u.DrainComplete
	next.Job.ResultReady = u.ResultReady
	next.Job.ModelUsage = u.ModelUsage
	next.Job.Outcome, next.Job.ClosingReason, next.Job.IntentOutcomes = "", "", nil
	next.Job.DiagnosticLogIncomplete = u.DiagnosticLogIncomplete
	if u.Result != nil {
		counts := u.Result.IntentOutcomes
		next.Job.Outcome, next.Job.ClosingReason, next.Job.IntentOutcomes = u.Result.Outcome, u.Result.ClosingReason, &counts
	}
	// 새로 completed가 되는 실행은 결과를 함께 싣는다. 결과 없는 완료는 받지 않는다(절대 규칙 5).
	if u.State == Completed && old.Job.State != Completed && u.Result == nil {
		return ErrProtocol
	}
	// 중지 요청을 후속 준비·실행 갱신으로 취소하지 않는다. 확인된 종료는 반영한다.
	if old.Job.State == Stopping && active(next.Job.State) {
		next.Job.State = Stopping
	}
	if old.Job.State == Stopping && phaseRank(next.Job.Phase) > phaseRank(old.Job.Phase) && next.Job.Phase != Closure && next.Job.Phase != Report {
		return ErrUnavailable
	}
	if old.Job.State == Stopping && next.Job.ModelUsage != nil && (old.Job.ModelUsage == nil || next.Job.ModelUsage.Requests > old.Job.ModelUsage.Requests) {
		return ErrUnavailable
	}
	if !validJob(next.Job) || !monotonic(old.Job, next.Job) {
		return ErrProtocol
	}
	if next.Job.TaskID != "" {
		for other, r := range s.jobs {
			if other != id && r.Job.TaskID == next.Job.TaskID {
				return ErrProtocol
			}
		}
	}
	if reflect.DeepEqual(old.Job, next.Job) {
		return nil
	}
	next.Job.Revision++
	return s.save(next)
}
func (s *Service) cancelExecution(id string) {
	s.mu.Lock()
	handle, exists := s.executions[id]
	s.mu.Unlock()
	if exists {
		handle.cancel()
	}
}
func (s *Service) execute(ctx context.Context, r receipt, done chan struct{}) {
	defer close(done)
	defer func() {
		s.cancelExecution(r.Job.ID)
		if recover() != nil {
			s.finishUnknown(r.Job.ID, "execution_interrupted")
		}
	}()
	err := s.backend.Execute(ctx, Execution{Principal: r.Principal, Job: r.Job, Inspection: r.Inspection.runtime()}, func(u Update) error { return s.publish(r.Job.ID, u) })
	// 기록 지연 중에도 반환한 backend의 자식 작업은 먼저 취소한다.
	s.cancelExecution(r.Job.ID)
	reason := "execution_unconfirmed"
	if err != nil {
		reason = "execution_interrupted"
	}
	s.finishUnknown(r.Job.ID, reason)
}
func interruptedJob(job Job, reason string) Job {
	job.State = Unknown
	job.ReasonCode = reason
	job.Revision++
	// 단계 진입 기록 뒤 권위 파일/단회 marker의 실제 완료는 확인되지 않았을 수 있다.
	if (job.Phase == Authority || job.Phase == Gateway) && job.AuthorityState != AuthorityConsumed {
		job.AuthorityState = AuthorityUnknown
	}
	return job
}
func (s *Service) finishUnknown(id, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.jobs[id]
	if !ok || terminal(r.Job.State) || r.Job.State == Unknown || s.released {
		return
	}
	r.Job = interruptedJob(r.Job, reason)
	if s.save(r) != nil {
		s.failed = true
	}
}

// Shutdown은 새 요청을 닫고 실행 마감을 기다린다. 시간 초과는 known fact를 유지한 unknown이다.
func (s *Service) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return ErrProtocol
	}
	s.mu.Lock()
	if s.released {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	waits := make([]chan struct{}, 0, len(s.executions))
	for _, h := range s.executions {
		h.cancel()
		waits = append(waits, h.done)
	}
	s.mu.Unlock()
	timeout := false
	for _, done := range waits {
		select {
		case <-done:
		case <-ctx.Done():
			timeout = true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return nil
	}
	if timeout {
		for _, r := range s.jobs {
			if active(r.Job.State) {
				r.Job = interruptedJob(r.Job, "shutdown_unconfirmed")
				if s.save(r) != nil {
					s.failed = true
				}
			}
		}
	}
	s.released = true
	err := s.journal.close()
	if timeout || s.failed {
		return errors.Join(err, ErrUnavailable)
	}
	return err
}
