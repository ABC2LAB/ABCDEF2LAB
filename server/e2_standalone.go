package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/e2export"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// 인증된 호출자·프로세스 격리의 실제 검증은 배치 제공자가 맡는다.
// 이 인터페이스 자체는 인증 경계가 아니며 네트워크 전송 구현도 제공하지 않는다.
type E2StandaloneReadiness interface{ CheckReady(context.Context) error }
type E2StandaloneOptions struct {
	Config                          *e2config.Config
	Authority                       E2HTTPPolicyAuthority
	Supervisor                      E2HTTPExecutionSupervisor
	Readiness                       E2StandaloneReadiness
	ExecutionRef                    string
	ExpectedPolicyFingerprintSHA256 string
}

func (E2StandaloneOptions) String() string   { return "server.E2StandaloneOptions[protected]" }
func (E2StandaloneOptions) GoString() string { return "server.E2StandaloneOptions[protected]" }

// 별도 신뢰 프로세스의 lifecycle core다. embedded runtime을 소유하거나 감싸지 않는다.
// 생산용 RPC/Planner 연결과 호출자 인증은 아직 제공하지 않는다.
// 준비 설정의 listen/upstream은 이 core의 데이터 전송 경로를 구성하지 않는다.
// Ready 제공자는 실제 배치의 라우팅과 인증 조건을 별도로 검증해야 한다.
type e2StandaloneAuthority struct {
	E2HTTPPolicyAuthority
	ctx context.Context
}

func (a e2StandaloneAuthority) Context() context.Context { return a.ctx }

type E2StandaloneBackend struct {
	cancel                           context.CancelFunc
	admissionClosed                  bool
	toolCalls                        int
	op                               sync.Mutex
	mu                               sync.Mutex
	ctx                              context.Context
	options                          E2StandaloneOptions
	factory                          E2HTTPRuntimeOptions
	reset                            string
	bound, failed, closing, disposed bool
	closeRef                         string
	lease                            *E2HTTPRunOptions
	acquired                         *E2HTTPAcquired
	publisher                        *e2export.Publisher
	active                           int
	idle                             chan struct{}
	final                            *E2ControlFinal
}

func (*E2StandaloneBackend) String() string   { return "server.E2StandaloneBackend[protected]" }
func (*E2StandaloneBackend) GoString() string { return "server.E2StandaloneBackend[protected]" }

// 도구는 이 backend의 Runner 샌드박스에서만 돈다(E2ControlIsolationToolSandbox).
func (*E2StandaloneBackend) e2ControlOwnsToolSandbox() {}

func NewE2StandaloneBackend(ctx context.Context, o E2StandaloneOptions) (*E2StandaloneBackend, error) {
	if ctx == nil || ctx.Err() != nil || o.Config.Check() != nil || e2HTTPNil(o.Authority) || e2HTTPNil(o.Supervisor) || e2HTTPNil(o.Readiness) || !validE2ControlRef(o.ExecutionRef) || !e2ControlHash.MatchString(o.ExpectedPolicyFingerprintSHA256) {
		return nil, ErrE2Control
	}
	authorityCtx := o.Authority.Context()
	if authorityCtx == nil || authorityCtx.Err() != nil {
		return nil, ErrE2Control
	}
	owned, cancel := context.WithCancel(e2HTTPTaskContext(ctx, authorityCtx))
	success := false
	defer func() {
		if !success {
			cancel()
		}
	}()
	raw, err := o.Config.ReadPolicy()
	if err != nil {
		return nil, ErrE2Control
	}
	defer clear(raw)
	policy, parseErr := ValidateE2HTTPPolicyDocument(raw, true)
	if parseErr != nil || filepath.Join(filepath.Dir(o.Config.PolicyPath), policy.AuditKey.File) != o.Config.AuditKeyPath {
		return nil, ErrE2Control
	}
	key, err := o.Config.ReadAuditKey()
	if err != nil {
		return nil, ErrE2Control
	}
	sum := sha256.Sum256(key)
	clear(key)
	if hex.EncodeToString(sum[:]) != policy.AuditKey.SHA256 {
		return nil, ErrE2Control
	}
	b := &E2StandaloneBackend{ctx: owned, cancel: cancel, options: o, idle: make(chan struct{})}
	close(b.idle)
	input := E2HTTPFileOptions{PolicyFile: o.Config.PolicyPath, Authority: e2StandaloneAuthority{E2HTTPPolicyAuthority: o.Authority, ctx: owned}, Supervisor: o.Supervisor, OnAcquire: b.onAcquire, ObserveEvent: b.observeEvent, ObserveResult: b.observeResult}
	f, err := loadE2HTTPRuntimeOptions(ctx, input, raw, b.resetBinding)
	if err != nil {
		return nil, err
	}
	var evaluation *e2config.Evaluation
	if f.Evaluation != nil {
		evaluation = &e2config.Evaluation{LLMBackend: f.Evaluation.LLMBackend, TargetKind: f.Evaluation.TargetKind}
	}
	if f.PolicyConfigSHA256 != o.Config.PolicySHA256 || f.PolicyFingerprintSHA256 != o.ExpectedPolicyFingerprintSHA256 || string(f.HeaderMode) != o.Config.HeaderMode || !reflect.DeepEqual(evaluation, o.Config.Evaluation) || f.Expected.RunRef == o.Config.EvalRunID {
		return nil, ErrE2Control
	}
	b.factory = f
	success = true
	return b, nil
}
func (b *E2StandaloneBackend) check(ctx context.Context) error {
	if b == nil || b.ctx == nil || b.cancel == nil || b.factory.CheckAuthority == nil || e2HTTPNil(b.options.Readiness) || ctx == nil || ctx.Err() != nil || b.ctx.Err() != nil || b.options.Config.Check() != nil || b.options.Readiness.CheckReady(ctx) != nil || b.factory.CheckAuthority(ctx) != nil {
		return ErrE2Control
	}
	return nil
}
func (b *E2StandaloneBackend) resetBinding() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.bound || b.failed || b.closing || b.disposed {
		return "", ErrE2Control
	}
	return b.reset, nil
}
func (b *E2StandaloneBackend) Ready(ctx context.Context) (E2ControlReady, error) {
	if b.check(ctx) != nil {
		return E2ControlReady{State: "failed", Reason: "startup_failed"}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed || b.closing || b.disposed {
		return E2ControlReady{State: "failed", Reason: "startup_failed"}, nil
	}
	v := E2ControlReady{State: "service_ready", ConfigSHA256: b.options.Config.ConfigSHA256, PolicyFingerprintSHA256: b.factory.PolicyFingerprintSHA256, HeaderMode: b.factory.HeaderMode}
	if e := b.factory.Evaluation; e != nil {
		v.Evaluation = &E2ControlEvaluation{LLMBackend: e.LLMBackend, TargetKind: e.TargetKind}
	}
	return v, nil
}
func (b *E2StandaloneBackend) BindReset(ctx context.Context, reset string) error {
	if b == nil || !e2ControlHash.MatchString(reset) {
		return ErrE2Control
	}
	b.op.Lock()
	defer b.op.Unlock()
	b.mu.Lock()
	if b.reset != "" && b.reset != reset || b.closing || b.disposed {
		b.mu.Unlock()
		return ErrE2Control
	}
	// 실패·불명 이후에도 다른 컨테이너를 이 backend에 결속하지 않는다.
	b.reset = reset
	b.mu.Unlock()
	if b.check(ctx) != nil {
		b.mu.Lock()
		b.failed = true
		b.mu.Unlock()
		return ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed {
		return ErrE2Control
	}
	b.bound = true
	return nil
}
func (b *E2StandaloneBackend) onAcquire(ctx context.Context, a E2HTTPAcquired) error {
	if a.Execution == nil || a.Runner == nil || a.Journal == nil || a.Runner.Execution() != a.Execution {
		return ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	v := a.Execution.Snapshot()
	if b.acquired != nil || !b.bound || b.closing || b.failed || v.Scope.ResetRef != b.reset || a.PolicyConfigSHA256 != b.factory.PolicyConfigSHA256 || a.PolicyFingerprintSHA256 != b.factory.PolicyFingerprintSHA256 {
		return ErrE2Control
	}
	c := b.options.Config
	// 러너는 게시 루트 아래 실행별 디렉터리에서 읽는다(연결 제안 §4.1).
	output := filepath.Join(c.EvidenceDir, b.options.ExecutionRef)
	if os.Mkdir(output, 0700) != nil {
		return ErrE2Control
	}
	p, err := e2export.New(e2export.Config{Execution: a.Execution, Runner: a.Runner, Journal: a.Journal, JournalDirectory: a.JournalDirectory, JournalName: a.JournalName, OutputDirectory: output, AuditKey: a.AuditKey, ExecutionRef: b.options.ExecutionRef, EvalRunID: c.EvalRunID, ReservationRef: c.ReservationRef, ModelTag: c.ModelTag, ResetGeneration: b.reset, EngineTaskID: v.Scope.TaskRef, PolicyFingerprintSHA256: a.PolicyFingerprintSHA256})
	if err != nil {
		return err
	}
	a.AuditKey = nil
	b.acquired = &a
	b.publisher = p
	return nil
}
func (b *E2StandaloneBackend) observeEvent(ctx context.Context, e e2proxy.Event) error {
	b.mu.Lock()
	p := b.publisher
	b.mu.Unlock()
	if p == nil {
		return ErrE2Control
	}
	return p.ObserveEvent(ctx, e)
}
func (b *E2StandaloneBackend) observeResult(ctx context.Context, r e2runner.Result) error {
	b.mu.Lock()
	p := b.publisher
	b.mu.Unlock()
	if p == nil {
		return ErrE2Control
	}
	return p.ObserveResult(ctx, r)
}

// Execute는 신뢰한 Go 조립만 호출한다. 원격 입력을 이 메서드에 바로 전달하면 안 된다.
// supervisor는 실제 task/intent 소유권을 검증해야 하며 양수 검사만으로 대체하지 않는다.
// b.op을 쥔 채로 부른다. 회차당 실행 자원은 한 번만 만든다.
func (b *E2StandaloneBackend) acquireLocked(ctx context.Context, q E2HTTPRequest) (*E2HTTPRunOptions, error) {
	if _, err := b.resetBinding(); err != nil {
		return nil, err
	}
	if b.factory.AuthorizeIntent(ctx, q) != nil {
		return nil, ErrE2Control
	}
	if b.lease == nil {
		lease, err := b.factory.Acquire(b.ctx, q)
		b.lease = &lease
		if err != nil {
			b.mu.Lock()
			b.failed = true
			b.mu.Unlock()
			return nil, ErrE2Control
		}
	}
	return b.lease, nil
}

func (b *E2StandaloneBackend) Execute(ctx context.Context, q E2HTTPRequest, command string) (e2runner.Result, error) {
	if b == nil {
		return e2runner.Result{}, ErrE2Control
	}
	b.op.Lock()
	if b.check(ctx) != nil {
		b.op.Unlock()
		return e2runner.Result{}, ErrE2Control
	}
	lease, err := b.acquireLocked(ctx, q)
	if err != nil {
		b.op.Unlock()
		return e2runner.Result{}, err
	}
	if b.toolCalls >= b.factory.MaxToolCalls {
		b.op.Unlock()
		return e2runner.Result{}, ErrE2Control
	}
	if lease.Execution == nil || lease.Execution.Snapshot().Scope.TaskRef != q.TaskID || lease.Execution.RegisterIntent(ctx, q.IntentID) != nil {
		b.op.Unlock()
		return e2runner.Result{}, ErrE2Control
	}
	b.toolCalls++
	b.mu.Lock()
	if b.active == 0 {
		b.idle = make(chan struct{})
	}
	b.active++
	b.mu.Unlock()
	b.op.Unlock()
	defer func() {
		b.mu.Lock()
		b.active--
		if b.active == 0 {
			close(b.idle)
		}
		b.mu.Unlock()
	}()
	result, err := lease.Runner.RunForIntent(ctx, q.IntentID, command)
	observeErr := b.observeResult(context.WithoutCancel(ctx), result)
	if observeErr != nil {
		b.mu.Lock()
		b.failed = true
		b.mu.Unlock()
	}
	return result, errors.Join(err, observeErr)
}
func (b *E2StandaloneBackend) Head(ctx context.Context) (E2ControlHead, error) {
	if b == nil || b.ctx == nil || ctx == nil || ctx.Err() != nil {
		return E2ControlHead{}, ErrE2Control
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.headLocked(), nil
}
func (b *E2StandaloneBackend) headLocked() E2ControlHead {
	if b.acquired == nil {
		return E2ControlHead{State: "no_execution"}
	}
	a := b.acquired
	v := a.Execution.Snapshot()
	cp := a.Journal.Checkpoint()
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
	return E2ControlHead{State: state, ExecutionRef: b.options.ExecutionRef, RunRef: v.Scope.RunRef, EngineTaskID: v.Scope.TaskRef, PolicyFingerprintSHA256: b.factory.PolicyFingerprintSHA256, Checkpoint: &E2ControlCheckpoint{Sequence: cp.Sequence, MAC: cp.MAC}}
}
func (b *E2StandaloneBackend) CloseAdmission(ctx context.Context, ref string) (E2ControlHead, error) {
	if b == nil || ctx == nil || ctx.Err() != nil || !validE2ControlRef(ref) {
		return E2ControlHead{}, ErrE2Control
	}
	b.op.Lock()
	defer b.op.Unlock()
	b.mu.Lock()
	if !b.bound || b.disposed || b.closeRef != "" && b.closeRef != ref {
		b.mu.Unlock()
		return E2ControlHead{}, ErrE2Control
	}
	b.closeRef = ref
	b.closing = true
	b.mu.Unlock()
	if b.lease != nil && b.lease.Execution != nil {
		if b.lease.Execution.Stop(ctx) != nil {
			return E2ControlHead{}, ErrE2Control
		}
	}
	b.mu.Lock()
	b.admissionClosed = true
	b.mu.Unlock()
	return b.Head(ctx)
}
func (b *E2StandaloneBackend) Finalize(ctx context.Context, ref string) (E2ControlFinal, error) {
	if b == nil || ctx == nil || ctx.Err() != nil || !validE2ControlRef(ref) {
		return E2ControlFinal{}, ErrE2Control
	}
	b.op.Lock()
	defer b.op.Unlock()
	b.mu.Lock()
	if b.final != nil {
		v := *b.final
		match := b.closeRef == ref
		b.mu.Unlock()
		if !match {
			return E2ControlFinal{}, ErrE2Control
		}
		return v, nil
	}
	if !b.closing || !b.admissionClosed || b.closeRef != ref || b.disposed {
		b.mu.Unlock()
		return E2ControlFinal{}, ErrE2Control
	}
	idle := b.idle
	b.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
		return E2ControlFinal{}, ErrE2Control
	}
	v := E2ControlFinal{State: "no_execution"}
	if b.lease != nil {
		v = E2ControlFinal{State: "incomplete", Reason: "audit_incomplete", ExecutionRef: b.options.ExecutionRef}
		b.mu.Lock()
		p, failed := b.publisher, b.failed
		b.mu.Unlock()
		if p != nil && !failed {
			r, err := p.Publish(ctx, ref)
			if err == nil {
				v = E2ControlFinal{State: "complete", ExecutionRef: r.ExecutionRef, ManifestSHA256: r.ManifestSHA256, Checkpoint: &E2ControlCheckpoint{Sequence: r.Checkpoint.Sequence, MAC: r.Checkpoint.MAC}}
			}
		}
		if v.State != "complete" {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), b.factory.DrainTimeout)
			cleanupErr := b.cleanupLease(cleanupCtx)
			cancel()
			if cleanupErr != nil {
				v.Reason = "drain_timeout"
			}
		}
	}
	b.mu.Lock()
	b.final = &v
	b.mu.Unlock()
	return v, nil
}

var _ E2ControlBackend = (*E2StandaloneBackend)(nil)

// cleanupLease는 op 잠금 안에서만 호출한다. 호출 취소와 독립적인 제한 시간으로 정리한다.
func (b *E2StandaloneBackend) cleanupLease(ctx context.Context) error {
	if b.lease == nil {
		return nil
	}
	var err error
	if b.lease.Execution != nil {
		err = errors.Join(err, b.lease.Execution.Stop(ctx))
	}
	if b.lease.Runner != nil {
		err = errors.Join(err, b.lease.Runner.Close(ctx))
	}
	if b.lease.Release != nil {
		err = errors.Join(err, b.lease.Release(ctx))
	}
	return err
}

// Dispose는 실패한 admission 종료도 정리한다. abort는 complete 또는 close ACK를 만들지 않는다.
func (b *E2StandaloneBackend) Dispose() error {
	if b == nil {
		return nil
	}
	if b.ctx == nil || b.cancel == nil || b.factory.DrainTimeout <= 0 || b.factory.DrainTimeout > 2*time.Minute {
		return ErrE2Control
	}
	b.op.Lock()
	defer b.op.Unlock()
	b.mu.Lock()
	if b.disposed {
		b.mu.Unlock()
		return nil
	}
	b.closing = true
	if b.final == nil {
		b.failed = true
	}
	idle := b.idle
	b.mu.Unlock()
	b.cancel()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), b.factory.DrainTimeout)
	defer cancel()
	select {
	case <-idle:
	case <-ctx.Done():
		return ErrE2Control
	}
	err := b.cleanupLease(ctx)
	b.mu.Lock()
	b.disposed = true
	p := b.publisher
	b.mu.Unlock()
	if p != nil {
		p.Dispose()
	}
	if err != nil {
		return ErrE2Control
	}
	return nil
}
