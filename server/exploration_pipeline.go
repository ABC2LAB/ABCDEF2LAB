package server

import (
	"context"
	"encoding/hex"
	"errors"
	"sort"
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
	snapshot, e := o.Workflow.Snapshot()
	if e != nil || snapshot.JobID != o.Binding.JobID || snapshot.TaskID != o.Binding.TaskID || snapshot.RunRef != o.Binding.RunRef || snapshot.SourceDigest != o.SourceDigest || snapshot.Stage != "crawling" || snapshot.InitialCompleted {
		return out, exploration.ErrBinding
	}
	b := explorationFlowBinding(o.Binding, o.SourceDigest)
	if o.Workflow.CheckExplorationTarget(ctx, b, o.TargetURL) != nil {
		return out, exploration.ErrBinding
	}
	skip := func(reason string) (ExplorationPipelineResult, error) {
		if e := o.Workflow.RecordExplorationUnavailable(ctx, reason); e != nil {
			return out, e
		}
		return ExplorationPipelineResult{Status: "NOT_RUN", Reason: reason}, nil
	}
	if o.OpenHost == nil {
		return skip("boundary_unavailable")
	}
	worker, e := s.ExplorationWorkerFor(task)
	if e != nil {
		return skip("model_unavailable")
	}
	host, e := o.OpenHost(ctx, o.Binding, worker)
	if e != nil {
		return skip("runtime_unavailable")
	}
	if host.Reader == nil || host.Fetch == nil || host.Sequences == nil || host.Session.Boundary == nil || host.Session.StartBrowser == nil || host.Session.Redact == nil {
		return skip("runtime_unavailable")
	}
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
			if reason == string(exploration.Completed) {
				if e = drain(call); e != nil {
					failures = append(failures, exploration.ErrUnavailable)
					reason = "runtime_unavailable"
				}
				if errors.Is(workerErr, ErrFirstDiagnosisModelBudget) || result.Reason == harness.ReasonMaxTurns || result.Reason == harness.ReasonTimeout {
					reason = "cap_exhausted"
				} else if result.Reason == harness.ReasonModelError {
					reason = "model_error"
				} else if workerErr != nil || result.Reason != harness.ReasonCompleted {
					reason = "runtime_unavailable"
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
	// 정적 크롤이 이미 올린 observed 순번은 재생하지 않는다. 공개 요약과 보호 inventory를 함께 확인한다.
	snapshot, _ = o.Workflow.Snapshot()
	for _, endpoint := range snapshot.Endpoints {
		if endpoint.ExplorationSessionRef == b.SessionRef && endpoint.ExplorationSeq > lastSeq {
			lastSeq = endpoint.ExplorationSeq
		}
	}
	for _, entry := range o.Workflow.DiscoveryInventory() {
		if entry.SessionRef == b.SessionRef && entry.Seq > lastSeq {
			lastSeq = entry.Seq
		}
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
		if errors.Is(e, ErrFirstDiagnosisModelBudget) {
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
