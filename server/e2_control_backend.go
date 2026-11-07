package server

import (
	"context"
	"errors"
	"sync"

	"github.com/Autumn-27/artex/e2export"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// 명시적인 평가 배치가 이미 발급·동결한 참조만 받는다. 이 입력은 승인 발급기가 아니다.
// embedded M01 서버는 같은 UID이므로 이 backend만 붙여도 평가 채널을 시작할 수 없다.
type E2RuntimeControlOptions struct {
	EvalRunID                       string `json:"-"`
	ReservationRef                  string `json:"-"`
	ModelTag                        string `json:"-"`
	ExecutionRef                    string `json:"-"`
	OutputDirectory                 string `json:"-"`
	ExpectedPolicyFingerprintSHA256 string `json:"-"`
}

func (E2RuntimeControlOptions) String() string   { return "server.E2RuntimeControlOptions[protected]" }
func (E2RuntimeControlOptions) GoString() string { return "server.E2RuntimeControlOptions[protected]" }

type E2RuntimeControlBackend struct {
	ctx             context.Context
	options         E2RuntimeControlOptions
	gate            chan struct{}
	mu              sync.Mutex
	runtime         *FirstDiagnosisHTTPRuntime
	publisher       *e2export.Publisher
	acquired        *E2HTTPAcquired
	failed          bool
	disposed        bool
	bound           bool
	resetRef        string
	closeRef        string
	closing         bool
	admissionClosed bool
	closeHead       E2ControlHead
	final           *E2ControlFinal
}

// 이 backend의 실제 엔진은 현재 프로세스 안에 있다. 평가 UDS 서비스가 이를 감지해 거부한다.
func (*E2RuntimeControlBackend) e2ControlEmbeddedEngine() {}

func (*E2RuntimeControlBackend) String() string   { return "server.E2RuntimeControlBackend[protected]" }
func (*E2RuntimeControlBackend) GoString() string { return "server.E2RuntimeControlBackend[protected]" }

func NewE2RuntimeControlBackend(ctx context.Context, options E2RuntimeControlOptions) (*E2RuntimeControlBackend, error) {
	if ctx == nil || ctx.Err() != nil || !validE2ControlRef(options.EvalRunID) || !validE2ControlRef(options.ReservationRef) || !validE2ControlRef(options.ModelTag) || !validE2ControlRef(options.ExecutionRef) || !e2ControlHash.MatchString(options.ExpectedPolicyFingerprintSHA256) || options.OutputDirectory == "" {
		return nil, ErrE2Control
	}
	return &E2RuntimeControlBackend{ctx: ctx, options: options, gate: make(chan struct{}, 1)}, nil
}

// Attach는 실제 strict startup이 반환한 한 서버만 인수한다. 공개 task ID로 handle을 복구하지 않는다.
func (b *E2RuntimeControlBackend) Attach(runtime *FirstDiagnosisHTTPRuntime) error {
	if b == nil || b.ctx == nil || runtime == nil || runtime.server == nil || runtime.host == nil || runtime.host.runtime == nil || runtime.host.CheckRoot(b.ctx) != nil || runtime.shutdown.BlockAdmission == nil || runtime.shutdown.CancelAndDrain == nil || runtime.shutdown.CheckResults == nil {
		return ErrE2Control
	}
	o := runtime.host.runtime.options
	if !e2ControlHash.MatchString(o.PolicyConfigSHA256) || o.PolicyFingerprintSHA256 != b.options.ExpectedPolicyFingerprintSHA256 || o.Expected.RunRef == b.options.EvalRunID {
		return ErrE2Control
	}
	ready := E2ControlReady{State: "service_ready", ConfigSHA256: o.PolicyConfigSHA256, PolicyFingerprintSHA256: o.PolicyFingerprintSHA256, HeaderMode: o.HeaderMode}
	if o.Evaluation != nil {
		ready.Evaluation = &E2ControlEvaluation{LLMBackend: o.Evaluation.LLMBackend, TargetKind: o.Evaluation.TargetKind}
	}
	if !validE2ControlReady(ready) {
		return ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runtime != nil && b.runtime != runtime || b.closing || b.failed || b.disposed {
		return ErrE2Control
	}
	b.runtime = runtime
	return nil
}

func (b *E2RuntimeControlBackend) Hooks() FirstDiagnosisHTTPHooks {
	return FirstDiagnosisHTTPHooks{ObserveEvent: b.observeEvent, ObserveResult: b.observeResult, OnAcquire: b.onAcquire}
}
func (b *E2RuntimeControlBackend) onAcquire(ctx context.Context, a E2HTTPAcquired) error {
	if b == nil || a.Execution == nil || a.Runner == nil || a.Journal == nil {
		return ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runtime == nil || b.acquired != nil || b.failed || b.disposed {
		b.failed = true
		return ErrE2Control
	}
	copy := a
	copy.AuditKey = nil
	b.acquired = &copy
	v := a.Execution.Snapshot()
	o := b.runtime.host.runtime.options
	if b.closing || !b.bound || ctx == nil || ctx.Err() != nil || b.runtime.host.CheckRoot(ctx) != nil || a.Runner.Execution() != a.Execution || a.PolicyConfigSHA256 != o.PolicyConfigSHA256 || a.PolicyFingerprintSHA256 != o.PolicyFingerprintSHA256 || v.Scope.RunRef != o.Expected.RunRef || v.Scope.ResetRef != b.resetRef || v.Scope.TaskRef != b.expectedTaskRefLocked() || v.PolicyRevision != o.Expected.PolicyRevision {
		b.failed = true
		return ErrE2Control
	}
	publisher, err := e2export.New(e2export.Config{Execution: a.Execution, Runner: a.Runner, Journal: a.Journal, JournalDirectory: a.JournalDirectory, JournalName: a.JournalName, OutputDirectory: b.options.OutputDirectory, AuditKey: a.AuditKey, ExecutionRef: b.options.ExecutionRef, EvalRunID: b.options.EvalRunID, ReservationRef: b.options.ReservationRef, ModelTag: b.options.ModelTag, ResetGeneration: b.resetRef, EngineTaskID: v.Scope.TaskRef, PolicyFingerprintSHA256: o.PolicyFingerprintSHA256})
	if err != nil {
		b.failed = true
		return ErrE2Control
	}
	b.publisher = publisher
	return nil
}
func (b *E2RuntimeControlBackend) observeEvent(ctx context.Context, event e2proxy.Event) error {
	if b == nil {
		return ErrE2Control
	}
	b.mu.Lock()
	p, failed := b.publisher, b.failed
	b.mu.Unlock()
	if p == nil || failed {
		return ErrE2Control
	}
	return p.ObserveEvent(ctx, event)
}
func (b *E2RuntimeControlBackend) observeResult(ctx context.Context, result e2runner.Result) error {
	if b == nil {
		return ErrE2Control
	}
	b.mu.Lock()
	p, failed := b.publisher, b.failed
	b.mu.Unlock()
	if p == nil || failed {
		return ErrE2Control
	}
	return p.ObserveResult(ctx, result)
}
func (b *E2RuntimeControlBackend) Ready(ctx context.Context) (E2ControlReady, error) {
	if b == nil || b.ctx == nil || ctx == nil || ctx.Err() != nil || b.ctx.Err() != nil {
		return E2ControlReady{}, ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runtime == nil {
		return E2ControlReady{State: "starting"}, nil
	}
	if b.failed || b.closing || b.disposed || b.runtime.host.CheckRoot(ctx) != nil {
		return E2ControlReady{State: "failed", Reason: "startup_failed"}, nil
	}
	o := b.runtime.host.runtime.options
	if o.PolicyFingerprintSHA256 != b.options.ExpectedPolicyFingerprintSHA256 {
		return E2ControlReady{State: "failed", Reason: "policy_rejected"}, nil
	}
	v := E2ControlReady{State: "service_ready", ConfigSHA256: o.PolicyConfigSHA256, PolicyFingerprintSHA256: o.PolicyFingerprintSHA256, HeaderMode: o.HeaderMode}
	if o.Evaluation != nil {
		v.Evaluation = &E2ControlEvaluation{LLMBackend: o.Evaluation.LLMBackend, TargetKind: o.Evaluation.TargetKind}
	}
	return v, nil
}
func (b *E2RuntimeControlBackend) BindReset(ctx context.Context, reset string) error {
	if b == nil || b.ctx == nil || ctx == nil || ctx.Err() != nil || !validE2ControlRef(reset) {
		return ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.runtime == nil || b.closing || b.failed || b.disposed || b.runtime.host.CheckRoot(ctx) != nil || reset != b.runtime.host.runtime.options.Expected.ResetRef || b.resetRef != "" && b.resetRef != reset {
		return ErrE2Control
	}
	b.resetRef = reset
	b.bound = true
	return nil
}
func (b *E2RuntimeControlBackend) Head(ctx context.Context) (E2ControlHead, error) {
	if b == nil || b.ctx == nil || ctx == nil || ctx.Err() != nil {
		return E2ControlHead{}, ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.headLocked()
}
func (b *E2RuntimeControlBackend) headLocked() (E2ControlHead, error) {
	if b.runtime == nil {
		return E2ControlHead{State: "no_execution"}, nil
	}
	if b.acquired == nil {
		if task, ok := b.runtime.host.runtime.TaskID(); ok {
			if _, exists := b.runtime.host.runtime.Execution(task); exists {
				b.failed = true
				return E2ControlHead{}, ErrE2Control
			}
		}
		return E2ControlHead{State: "no_execution"}, nil
	}
	a := b.acquired
	v := a.Execution.Snapshot()
	o := b.runtime.host.runtime.options
	if v.Scope.RunRef != o.Expected.RunRef || v.Scope.ResetRef != o.Expected.ResetRef || v.Scope.TaskRef != b.expectedTaskRefLocked() || v.PolicyRevision != o.Expected.PolicyRevision {
		return E2ControlHead{}, ErrE2Control
	}
	state := "running"
	if b.closing {
		state = "closing"
	}
	if b.failed {
		state = "incomplete"
	}
	if b.final != nil {
		state = b.final.State
	}
	checkpoint := a.Journal.Checkpoint()
	return E2ControlHead{State: state, ExecutionRef: b.options.ExecutionRef, RunRef: v.Scope.RunRef, EngineTaskID: v.Scope.TaskRef, PolicyFingerprintSHA256: o.PolicyFingerprintSHA256, Checkpoint: &E2ControlCheckpoint{Sequence: checkpoint.Sequence, MAC: checkpoint.MAC}}, nil
}
func (b *E2RuntimeControlBackend) CloseAdmission(ctx context.Context, ref string) (E2ControlHead, error) {
	if b == nil || b.ctx == nil || ctx == nil || ctx.Err() != nil || !validE2ControlRef(ref) {
		return E2ControlHead{}, ErrE2Control
	}
	b.mu.Lock()
	if b.runtime == nil || b.disposed || !b.bound || b.closeRef != "" && b.closeRef != ref {
		b.mu.Unlock()
		return E2ControlHead{}, ErrE2Control
	}
	if b.admissionClosed {
		head := b.closeHead
		b.mu.Unlock()
		return head, nil
	}
	b.closeRef = ref
	b.closing = true
	r := b.runtime
	b.mu.Unlock()
	r.admissionStopped.Store(true)
	if r.host.runtime.Fence() != nil || r.shutdown.BlockAdmission(ctx, r.server) != nil || ctx.Err() != nil {
		return E2ControlHead{}, ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	head, err := b.headLocked()
	if err != nil {
		return E2ControlHead{}, err
	}
	b.closeHead = head
	b.admissionClosed = true
	return head, nil
}

func (b *E2RuntimeControlBackend) Finalize(ctx context.Context, ref string) (E2ControlFinal, error) {
	if b == nil || b.ctx == nil || b.gate == nil || ctx == nil || !validE2ControlRef(ref) {
		return E2ControlFinal{}, ErrE2Control
	}
	select {
	case b.gate <- struct{}{}:
		defer func() { <-b.gate }()
	case <-ctx.Done():
		return E2ControlFinal{}, ErrE2Control
	}
	b.mu.Lock()
	if b.final != nil {
		v, closeRef := *b.final, b.closeRef
		b.mu.Unlock()
		if ref != closeRef {
			return E2ControlFinal{}, ErrE2Control
		}
		return v, nil
	}
	if !b.closing || !b.admissionClosed || b.closeRef != ref || b.runtime == nil || b.disposed {
		b.mu.Unlock()
		return E2ControlFinal{}, ErrE2Control
	}
	r, head := b.runtime, b.closeHead
	b.mu.Unlock()
	final := E2ControlFinal{State: "incomplete", Reason: "unknown", ExecutionRef: head.ExecutionRef}
	finish := func(v E2ControlFinal) (E2ControlFinal, error) {
		b.mu.Lock()
		b.final = &v
		b.mu.Unlock()
		return v, nil
	}
	// 실제 전체 engine writer join·송신 종료·저장 검사·host 자원 종료를 같은 runtime에서 수행한다.
	if closeErr := r.Close(ctx); closeErr != nil {
		reason := "unknown"
		var stage controlledReadAssemblyStageError
		if errors.As(closeErr, &stage) {
			switch stage.stage {
			case "http_block_admission", "http_drain":
				reason = "drain_timeout"
			case "http_release":
				reason = "audit_incomplete"
			}
		}
		if ctx.Err() != nil {
			reason = "drain_timeout"
		}
		return finish(E2ControlFinal{State: "incomplete", Reason: reason, ExecutionRef: head.ExecutionRef})
	}
	b.mu.Lock()
	p, acquired, failed := b.publisher, b.acquired, b.failed
	b.mu.Unlock()
	if acquired == nil {
		if task, ok := r.host.runtime.TaskID(); ok {
			if _, exists := r.host.runtime.Execution(task); exists {
				return finish(final)
			}
		}
		return finish(E2ControlFinal{State: "no_execution"})
	}
	if failed || p == nil || head.ExecutionRef == "" {
		return finish(E2ControlFinal{State: "incomplete", Reason: "audit_incomplete", ExecutionRef: final.ExecutionRef})
	}
	result, err := p.Publish(ctx, ref)
	if err != nil {
		reason := "publish_failed"
		if errors.Is(err, e2export.ErrDrain) {
			reason = "drain_timeout"
		}
		if errors.Is(err, e2export.ErrAudit) {
			reason = "audit_incomplete"
		}
		return finish(E2ControlFinal{State: "incomplete", Reason: reason, ExecutionRef: final.ExecutionRef})
	}
	return finish(E2ControlFinal{State: "complete", ExecutionRef: result.ExecutionRef, ManifestSHA256: result.ManifestSHA256, Checkpoint: &E2ControlCheckpoint{Sequence: result.Checkpoint.Sequence, MAC: result.Checkpoint.MAC}})
}

// Dispose는 backend가 소유한 검증 키 사본/디렉터리 FD만 반납한다. 실제 host 종료는 Finalize가 맡는다.
func (b *E2RuntimeControlBackend) Dispose() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.disposed = true
	p := b.publisher
	b.mu.Unlock()
	if p != nil {
		p.Dispose()
	}
	return nil
}

func (b *E2RuntimeControlBackend) expectedTaskRefLocked() string {
	task, _ := b.runtime.host.ExpectedTask()
	return task
}
