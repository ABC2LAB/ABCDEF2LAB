package server

import (
	"context"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/norma/harness"
)

// Host는 보호된 경계·컨테이너·private 자료 채널의 실제 어댑터다. 설치 JSON·HTTP·모델 입력으로 만들지 않는다.
// Sequences는 승인 대상으로 실제 전달한 요청의 순번만 돌려준다. 헤더·본문·query 값은 넘기지 않는다.
type ExplorationPipelineHost struct {
	Session   ExplorationSessionOptions
	Reader    diagnosisflow.ExplorationReader
	Fetch     diagnosisflow.ExplorationFetch
	Sequences func(context.Context, diagnosisflow.ExplorationBinding) ([]uint64, error)
}

// Factory는 시작 전 설정과 소유한 handle만 조립한다. 반환 오류 때 미소유 컨테이너/연결을 남기지 않는다.
type ExplorationPipelineFactory func(context.Context, exploration.Binding, *agent.Worker) (ExplorationPipelineHost, error)
type ExplorationPipelineOptions struct {
	Workflow     *diagnosisflow.Controller
	Binding      exploration.Binding
	SourceDigest string
	TargetURL    string
	MaxTurns     int
	MaxDuration  time.Duration
	OpenHost     ExplorationPipelineFactory
}
type ExplorationPipelineResult struct {
	Status      string
	Reason      string
	ModelReason harness.TerminalReason
	// Charge는 이 탐색이 회차 대상 요청 예산에서 쓴 몫이다. 진단 E②는 N에서 이 몫을 뺀 나머지만 받는다(P1-E).
	Charge ExplorationCharge
}

// chargeRecordingBoundary는 호스트 경계를 감싸 열림 여부와 경계가 확정한 마감 상태만 기록한다. 송신·판정은
// 하지 않는다. 열지 않았으면 몫이 0이고, 열었으면 검증한 마감 상태의 대상 전달 수만 확정 몫으로 쓴다.
type chargeRecordingBoundary struct {
	inner   exploration.Boundary
	binding exploration.Binding
	mu      sync.Mutex
	opened  bool
	final   *exploration.State
}

func (b *chargeRecordingBoundary) Open(ctx context.Context, binding exploration.Binding) (exploration.Lease, error) {
	lease, err := b.inner.Open(ctx, binding)
	if lease == nil {
		// 경계 계약상 lease가 없으면 열리지 않은 것이다. 오류와 함께 lease가 오면 열린 것으로 보고 마감을 기다린다.
		return nil, err
	}
	b.mu.Lock()
	b.opened = true
	b.mu.Unlock()
	return &chargeRecordingLease{inner: lease, owner: b}, err
}

func (b *chargeRecordingBoundary) charge() ExplorationCharge {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.opened {
		return ExplorationCharge{Known: true}
	}
	if b.final == nil {
		return ExplorationCharge{}
	}
	return ExplorationCharge{Requests: b.final.TargetRequests, Known: true}
}

type chargeRecordingLease struct {
	inner exploration.Lease
	owner *chargeRecordingBoundary
}

func (l *chargeRecordingLease) Status(ctx context.Context) (exploration.State, error) {
	return l.inner.Status(ctx)
}

func (l *chargeRecordingLease) Close(ctx context.Context, reason exploration.Reason) (exploration.State, error) {
	state, err := l.inner.Close(ctx, reason)
	// 새 송신이 끝났다고 경계가 확정한 상태만 몫으로 쓴다. 활성·연결 잔류·결속 불일치는 모르는 몫으로 남긴다.
	if err == nil && !state.Active && state.ActiveConnections == 0 && state.Validate(l.owner.binding) == nil {
		l.owner.mu.Lock()
		final := state
		l.owner.final = &final
		l.owner.mu.Unlock()
	}
	return state, err
}

func explorationFlowBinding(b exploration.Binding, digest string) diagnosisflow.ExplorationBinding {
	return diagnosisflow.ExplorationBinding{SessionRef: b.SessionRef, RunRef: b.RunRef, JobID: b.JobID, TaskID: b.TaskID, TargetRef: b.TargetRef, TargetRevision: b.TargetRevision, SourceDigest: digest}
}

// RunExplorationPipeline는 정적 크롤→원본 Worker→private 관찰 마감→세션 종료를 실제 제품 단계에서 실행한다.
// 경계 어댑터가 없으면 NOT_RUN을 남기고 기존 정적 크롤을 계속 쓸 수 있다. 목업·대체 모델을 만들지 않는다.
func (s *Server) RunExplorationPipeline(ctx context.Context, task *Task, o ExplorationPipelineOptions) (out ExplorationPipelineResult, err error) {
	if ctx == nil || ctx.Err() != nil || o.Workflow == nil || task == nil || task.ID != o.Binding.TaskID || o.Binding.Validate() != nil || o.MaxTurns <= 0 || o.MaxDuration <= 0 || o.MaxDuration > time.Duration(o.Binding.Limits.TimeBudgetSeconds)*time.Second {
		return out, exploration.ErrBinding
	}
	digest, e := hex.DecodeString(o.SourceDigest)
	if e != nil || len(digest) != 32 || hex.EncodeToString(digest) != o.SourceDigest {
		return out, exploration.ErrBinding
	}
	// 초기 일시정지는 once gate에 실패로 고정하지 않고, 재개 뒤 권한·대상을 다시 검사한다.
	if e := o.Workflow.Wait(ctx); e != nil {
		return out, e
	}
	snapshot, e := o.Workflow.Snapshot()
	if e != nil || snapshot.JobID != o.Binding.JobID || snapshot.TaskID != o.Binding.TaskID || snapshot.RunRef != o.Binding.RunRef || snapshot.SourceDigest != o.SourceDigest || snapshot.Stage != "crawling" || snapshot.InitialCompleted {
		return out, exploration.ErrBinding
	}
	b := explorationFlowBinding(o.Binding, o.SourceDigest)
	if o.Workflow.CheckExplorationTarget(ctx, b, o.TargetURL) != nil {
		return out, exploration.ErrBinding
	}
	// 경계를 열기 전의 NOT_RUN은 대상 송신이 없으므로 몫 0이 확정이다. 경계를 연 뒤의 경로는 아래 recorder가 덮어쓴다.
	skip := func(reason string) (ExplorationPipelineResult, error) {
		if e := o.Workflow.RecordExplorationUnavailable(ctx, reason); e != nil {
			return out, e
		}
		return ExplorationPipelineResult{Status: "NOT_RUN", Reason: reason, Charge: ExplorationCharge{Known: true}}, nil
	}
	if o.OpenHost == nil {
		return skip("boundary_unavailable")
	}
	worker, e := s.ExplorationWorkerFor(task)
	if e != nil {
		exploration.ReportFailure("worker_resolve", e)
		return skip("model_unavailable")
	}
	host, e := o.OpenHost(ctx, o.Binding, worker)
	if e != nil {
		exploration.ReportFailure("host_factory", e)
		return skip("runtime_unavailable")
	}
	if host.Reader == nil || host.Fetch == nil || host.Sequences == nil || host.Session.Boundary == nil || host.Session.StartBrowser == nil || host.Session.Redact == nil {
		exploration.ReportFailure("host_capabilities", exploration.ErrUnavailable)
		return skip("runtime_unavailable")
	}
	recorder := &chargeRecordingBoundary{inner: host.Session.Boundary, binding: o.Binding}
	host.Session.Boundary = recorder
	// 아래 세션 마감 defer보다 먼저 등록해 마지막에 돈다. 어떤 반환 경로든 경계가 확정한 몫만 싣는다.
	defer func() { out.Charge = recorder.charge() }()
	host.Session.Worker = worker
	host.Session.Binding = o.Binding
	var sessionRef atomic.Pointer[ExplorationSession]
	var bound atomic.Bool
	drainGate := make(chan struct{}, 1)
	var lastSeq uint64
	drain := func(call context.Context) error {
		select {
		case drainGate <- struct{}{}:
			defer func() { <-drainGate }()
		case <-call.Done():
			return exploration.ErrUnavailable
		}
		seqs, e := host.Sequences(call, b)
		if e != nil || uint64(len(seqs)) > o.Binding.Limits.MaxRequests {
			return exploration.ErrUnavailable
		}
		seqs = append([]uint64{}, seqs...)
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		for i, seq := range seqs {
			if seq == 0 || i > 0 && seq == seqs[i-1] {
				return exploration.ErrBinding
			}
			if seq <= lastSeq {
				continue
			}
			if _, e = o.Workflow.ReportObserved(call, b, seq); e != nil {
				return exploration.ErrUnavailable
			}
			lastSeq = seq
		}
		return nil
	}
	hostObserve := host.Session.Observe
	host.Session.Observe = func(call context.Context, _ exploration.Binding) error {
		if !bound.Load() {
			return exploration.ErrUnavailable
		}
		if e := drain(call); e != nil {
			return e
		}
		if hostObserve != nil {
			return hostObserve(call, o.Binding)
		}
		return nil
	}
	hostFinalize := host.Session.Finalize
	host.Session.Finalize = func(call context.Context, _ exploration.Binding) error {
		if !bound.Load() {
			return nil
		}
		session := sessionRef.Load()
		if session == nil {
			return exploration.ErrUnavailable
		}
		state, e := o.Workflow.Snapshot()
		if e != nil {
			return exploration.ErrUnavailable
		}
		var failures []error
		if !state.InitialCompleted {
			reason := string(session.ClosedReason())
			result, workerErr := session.WorkerResult()
			switch reason {
			case string(exploration.Completed), string(exploration.ModelError), string(exploration.RuntimeUnavailable):
				if e = drain(call); e != nil {
					failures = append(failures, exploration.ErrUnavailable)
					reason = string(exploration.RuntimeUnavailable)
				} else if errors.Is(workerErr, ErrFirstDiagnosisModelBudget) {
					// 같은 실제 원장이 한도를 소진하고 정산까지 끝난 경우만 예산 종료다.
					// 설정 거부·불명 정산·다른 원장의 오류를 budget_limited로 승격하지 않는다.
					settledLimit := false
					if budget := s.firstModelBudget; budget != nil {
						budget.mu.Lock()
						settledLimit = budget.limitReached && !budget.blocked && budget.inflight == 0 && budget.usage.UsageKnown
						budget.mu.Unlock()
					}
					switch {
					case settledLimit:
						reason = string(exploration.CapExhausted)
					case !s.firstModelBudget.closesExploration():
						// 예산 한도도 회계 막힘도 아닌 모델 호출 실패(429·5xx·보내기 전 거부)는 모델 오류다.
						reason = string(exploration.ModelError)
					default:
						reason = string(exploration.RuntimeUnavailable)
					}
				} else if result.Reason == harness.ReasonMaxTurns || result.Reason == harness.ReasonTimeout {
					reason = string(exploration.CapExhausted)
				} else if result.Reason == harness.ReasonModelError {
					reason = string(exploration.ModelError)
				} else if workerErr != nil || result.Reason != harness.ReasonCompleted {
					reason = string(exploration.RuntimeUnavailable)
				}
			}

			// 취소·철회·경계 마감 뒤 새 observed 승격은 하지 않는다. 저장한 partial과 마감만 보존한다.
			if e = o.Workflow.FinishExploration(call, b, reason); e != nil {
				failures = append(failures, exploration.ErrUnavailable)
			}
		}
		if hostFinalize != nil && hostFinalize(call, o.Binding) != nil {
			failures = append(failures, exploration.ErrUnavailable)
		}
		return errors.Join(failures...)
	}
	session, e := OpenExplorationSession(ctx, host.Session)
	if e != nil {
		exploration.ReportFailure("session_open", e)
		if errors.Is(e, ErrExplorationClosure) {
			return out, e
		}
		return skip("browser_unavailable")
	}
	sessionRef.Store(session)
	// 이후 어떤 오류도 lease를 남기거나 정상 종료로 숨기지 않는다.
	defer func() {
		reason := exploration.BoundaryUnavailable
		if ctx.Err() != nil {
			reason = exploration.Cancelled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = exploration.CapExhausted
			}
		}
		if e := session.Close(context.Background(), reason); e != nil {
			err = errors.Join(err, e)
		}
	}()
	actual, e := o.Workflow.BindExploration(session.Context(), host.Reader)
	if e != nil || actual != b {
		return out, exploration.ErrBinding
	}
	bound.Store(true)
	_, crawlErr := o.Workflow.CrawlExploration(session.Context(), b, host.Fetch)
	if crawlErr != nil {
		reason := session.ClosedReason()
		if reason == "" && session.Context().Err() != nil {
			reason = exploration.Cancelled
			if errors.Is(session.Context().Err(), context.DeadlineExceeded) {
				reason = exploration.CapExhausted
			}
		}
		if reason != "" && reason != exploration.Completed {
			if e = session.Close(context.Background(), reason); e != nil {
				return out, e
			}
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if reason == exploration.CapExhausted || reason == exploration.BoundaryUnavailable {
				final, e := o.Workflow.Snapshot()
				if e != nil {
					return out, exploration.ErrUnavailable
				}
				return ExplorationPipelineResult{Status: final.ExplorationStatus, Reason: final.ExplorationReason}, nil
			}
			return out, ErrFirstDiagnosisTargetEnded
		}
		return out, exploration.ErrUnavailable
	}
	// 선택/표시 목록이나 inventory가 상한으로 생략돼도 private 커서만 신뢰한다.
	// 종료 watcher의 마지막 drain과 같은 gate를 써서 checkpoint 갱신이 경합하지 않게 한다.
	snapshot, _ = o.Workflow.Snapshot()
	cursorCtx, cursorCancel := context.WithTimeout(context.WithoutCancel(ctx), host.Session.CloseTimeout)
	select {
	case drainGate <- struct{}{}:
		lastSeq, e = o.Workflow.ExplorationCursor(cursorCtx, b)
		<-drainGate
	case <-cursorCtx.Done():
		e = cursorCtx.Err()
	}
	cursorCancel()
	if e != nil {
		return out, exploration.ErrUnavailable
	}

	if snapshot.InitialCompleted {
		if e = session.Close(context.Background(), exploration.CapExhausted); e != nil {
			return out, e
		}
	} else {
		_, e = session.Run(ctx, worker, agent.ExplorationRunOptions{TargetURL: o.TargetURL, Reporter: o.Workflow, MaxTurns: o.MaxTurns, MaxDuration: o.MaxDuration}, func(a db.Activity) {
			if s.engine != nil && s.engine.m != nil {
				a.Worker = "exploration"
				s.engine.emitActivity(task, a)
			}
		})
		if closeErr := session.Close(context.Background(), exploration.Completed); closeErr != nil {
			return out, closeErr
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if errors.Is(e, ErrExplorationObservation) {
			return out, e
		}
		// 제품 모델 호출은 첫 진단 모델 예산을 거쳐서, 예산 한도가 아닌 실패(429·5xx·보내기 전 거부)에도
		// ErrFirstDiagnosisModelBudget이 붙는다. 예산 한도에 닿았거나 회계가 막혔을 때만 예산 종료로 다루고, 그 밖의
		// 모델 호출 실패는 탐색 모델 오류(PARTIAL)로 남겨 지금까지의 발견으로 진단을 잇는다.
		if errors.Is(e, ErrFirstDiagnosisModelBudget) && s.firstModelBudget.closesExploration() {
			proof, proofErr := s.closedExplorationBudget(o, session)
			if proofErr != nil {
				return out, proofErr
			}
			return out, proof
		}
	}
	snapshot, e = o.Workflow.Snapshot()
	if e != nil {
		return out, exploration.ErrUnavailable
	}
	result, _ := session.WorkerResult()
	return ExplorationPipelineResult{Status: snapshot.ExplorationStatus, Reason: snapshot.ExplorationReason, ModelReason: result.Reason}, nil
}
