package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/internal/privateinput"
	"github.com/Autumn-27/artex/transmit"
)

const FirstDiagnosisHTTPAuthorityFormat = "abc2lab.first-http-authority/v1"

var ErrFirstDiagnosisHTTPHost = errors.New("first_diagnosis_http_host_unavailable")

// 동일 운영자 UID의 M01 보호 입력이다. 독립 감사 UID나 복원 권위를 대신하지 않는다.
type FirstDiagnosisHTTPHostConfig struct {
	PolicyFile    string `json:"policy_file"`
	AuthorityFile string `json:"authority_file"`
	AuthorityRoot string `json:"authority_root"`
}

func (FirstDiagnosisHTTPHostConfig) String() string {
	return "server.FirstDiagnosisHTTPHostConfig[protected]"
}
func (FirstDiagnosisHTTPHostConfig) GoString() string {
	return "server.FirstDiagnosisHTTPHostConfig[protected]"
}

// 운영자는 prepare-task의 실제 행을 확인한 뒤 이 파일과 정책을 승인한다.
// 참조와 SHA만으로 권위가 생기지 않는다. 실제 euid 소유의 보호 파일과 held FD를 함께 확인한다.
type FirstDiagnosisHTTPApproval struct {
	Format        string    `json:"format"`
	ApprovalRef   string    `json:"approval_ref"`
	OperatorUID   uint32    `json:"operator_uid"`
	TaskID        int64     `json:"task_id"`
	ExplorationID int64     `json:"exploration_id"`
	PolicySHA256  string    `json:"policy_sha256"`
	ValidFrom     time.Time `json:"valid_from"`
	ValidUntil    time.Time `json:"valid_until"`
}

func (FirstDiagnosisHTTPApproval) String() string {
	return "server.FirstDiagnosisHTTPApproval[protected]"
}
func (FirstDiagnosisHTTPApproval) GoString() string {
	return "server.FirstDiagnosisHTTPApproval[protected]"
}

// 평가 제어 채널이 필요한 별도 배치만 관찰자를 명시적으로 연결한다.
// ExplorationStageFactory는 기존 모델·예산 구성이 끝난 Server에서만 탐색 단계를 조립한다.
type ExplorationStageFactory func(*Server) E2HTTPExplorationStage

type FirstDiagnosisHTTPHooks struct {
	Exploration      ExplorationStageFactory
	Workflow         *diagnosisflow.Controller
	CheckAuthority   func() error
	RequestApprovals *RequestApprovalService
	ObserveEvent     func(context.Context, e2proxy.Event) error
	ObserveResult    func(context.Context, e2runner.Result) error
	OnAcquire        func(context.Context, E2HTTPAcquired) error
}

func (FirstDiagnosisHTTPHooks) String() string   { return "server.FirstDiagnosisHTTPHooks[protected]" }
func (FirstDiagnosisHTTPHooks) GoString() string { return "server.FirstDiagnosisHTTPHooks[protected]" }

type FirstDiagnosisHTTPHostSupervisor interface {
	E2HTTPExecutionSupervisor
	Attach(*Server) error
	CheckTaskQuiescent(context.Context, int64) error
}

type firstDiagnosisHTTPAuthority interface {
	E2HTTPPolicyAuthority
	approval() FirstDiagnosisHTTPApproval
	revoke()
	close() error
}

// 호스트는 승인된 한 task만 소유한다. 취소 자체는 FD나 배타 lease를 해제하지 않는다.
type FirstDiagnosisHTTPHost struct {
	authority  firstDiagnosisHTTPAuthority
	runtime    *E2HTTPRuntime
	supervisor FirstDiagnosisHTTPHostSupervisor
	expected   E2HTTPExpectedBinding
	closing    atomic.Bool
	closed     atomic.Bool
}

func (*FirstDiagnosisHTTPHost) String() string   { return "server.FirstDiagnosisHTTPHost[protected]" }
func (*FirstDiagnosisHTTPHost) GoString() string { return "server.FirstDiagnosisHTTPHost[protected]" }

func NewFirstDiagnosisHTTPHost(ctx context.Context, cfg FirstDiagnosisHTTPHostConfig, supervisor FirstDiagnosisHTTPHostSupervisor, hooks ...FirstDiagnosisHTTPHooks) (*FirstDiagnosisHTTPHost, error) {
	if ctx == nil || ctx.Err() != nil || e2HTTPNil(supervisor) || len(hooks) > 1 {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	policyStarted := time.Now()
	var policyTask int64
	policyChecked := false
	finishStage := func(stage string, err error, reason string, started time.Time) {
		status, code := diagnosisLogOutcome(ctx, err, reason)
		_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "stage_finished", TaskID: policyTask, Stage: stage, Status: status, ReasonCode: code, DurationMS: time.Since(started).Milliseconds()})
	}
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "stage_started", Stage: "e2_policy", Status: "started"})
	defer func() {
		if !policyChecked {
			finishStage("e2_policy", ErrFirstDiagnosisHTTPHost, "e2_policy_failed", policyStarted)
		}
	}()
	authority, err := newFirstDiagnosisHTTPAuthority(ctx, cfg)
	if err != nil {
		return nil, ErrFirstDiagnosisHTTPHost
	}
	policyTask = authority.approval().TaskID
	h := &FirstDiagnosisHTTPHost{authority: authority, supervisor: supervisor}
	var hook FirstDiagnosisHTTPHooks
	if len(hooks) == 1 {
		hook = hooks[0]
	}
	options, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{Workflow: hook.Workflow, RequestApprovals: hook.RequestApprovals, CheckAuthority: hook.CheckAuthority, PolicyFile: cfg.PolicyFile, Authority: authority, Supervisor: h, ObserveEvent: hook.ObserveEvent, ObserveResult: hook.ObserveResult, OnAcquire: hook.OnAcquire})
	if err != nil {
		authority.revoke()
		_ = authority.close()
		return nil, ErrFirstDiagnosisHTTPHost
	}
	// 비평가 M01은 평가 전용 caller 원문 전달 권한을 받지 않는다.
	if options.HeaderMode == e2proxy.HeaderEvaluationCallerPassthrough {
		authority.revoke()
		_ = authority.close()
		return nil, ErrFirstDiagnosisHTTPHost
	}
	h.expected = options.Expected
	// 정책의 유효 기간은 운영자 승인 구간을 벗어나지 않아야 한다.
	raw, err := privateinput.ReadFile(cfg.PolicyFile, 256<<10)
	if err != nil {
		authority.revoke()
		_ = authority.close()
		return nil, ErrFirstDiagnosisHTTPHost
	}
	var policy E2HTTPFilePolicy
	decodeErr := strictJSON(raw, &policy)
	digest := sha256.Sum256(raw)
	clear(raw)
	approval := authority.approval()
	if decodeErr != nil || authority.AuthorizePolicy(ctx, digest) != nil || policy.ValidFrom.Before(approval.ValidFrom) || policy.ValidUntil.After(approval.ValidUntil) {
		authority.revoke()
		_ = authority.close()
		return nil, ErrFirstDiagnosisHTTPHost
	}
	policyChecked = true
	finishStage("e2_policy", nil, "e2_policy_failed", policyStarted)
	runtimeStarted := time.Now()
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "server", Event: "stage_started", TaskID: policyTask, Stage: "host_startup", Status: "started"})
	h.runtime, err = NewE2HTTPRuntime(ctx, options)
	finishStage("host_startup", err, "host_startup_failed", runtimeStarted)
	if err != nil {
		authority.revoke()
		_ = authority.close()
		return nil, ErrFirstDiagnosisHTTPHost
	}
	return h, nil
}

func (h *FirstDiagnosisHTTPHost) Context() context.Context {
	if h == nil || h.authority == nil {
		return nil
	}
	return h.authority.Context()
}
func (h *FirstDiagnosisHTTPHost) Runtime() *E2HTTPRuntime {
	if h == nil {
		return nil
	}
	return h.runtime
}
func (h *FirstDiagnosisHTTPHost) ExpectedTask() (string, int64) {
	if h == nil || h.authority == nil {
		return "", 0
	}
	a := h.authority.approval()
	return strconv.FormatInt(a.TaskID, 10), a.ExplorationID
}
func (h *FirstDiagnosisHTTPHost) CheckRoot(ctx context.Context) error {
	if h == nil || h.closed.Load() || h.authority == nil || h.authority.CheckRoot(ctx) != nil {
		return ErrFirstDiagnosisHTTPHost
	}
	return nil
}
func (h *FirstDiagnosisHTTPHost) AuthorizeExecution(ctx context.Context, scope transmit.Scope, intentID int64) error {
	if h.CheckRoot(ctx) != nil || intentID <= 0 {
		return ErrFirstDiagnosisHTTPHost
	}
	a := h.authority.approval()
	if scope.TaskRef != strconv.FormatInt(a.TaskID, 10) || scope.RunRef != h.expected.RunRef || scope.ResetRef != h.expected.ResetRef || scope.PolicyRef != h.expected.PolicyRef || scope.CaseRef != h.expected.CaseRef || h.supervisor.AuthorizeExecution(ctx, scope, intentID) != nil || h.CheckRoot(ctx) != nil {
		return ErrFirstDiagnosisHTTPHost
	}
	return nil
}
func (h *FirstDiagnosisHTTPHost) Revoke() {
	if h == nil {
		return
	}
	if h.authority != nil {
		h.authority.revoke()
	}
	if h.runtime != nil {
		h.runtime.Fence()
	}
}
func (h *FirstDiagnosisHTTPHost) Close(ctx context.Context) error {
	if h == nil || ctx == nil || h.authority == nil || h.runtime == nil || e2HTTPNil(h.supervisor) {
		return ErrFirstDiagnosisHTTPHost
	}
	if !h.closing.CompareAndSwap(false, true) {
		return ErrE2HTTPBusy
	}
	defer h.closing.Store(false)
	if h.closed.Load() {
		return nil
	}
	h.Revoke()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// 실제 엔진/Planner/Worker 소유가 끝나기 전 파일 권위를 반납하지 않는다.
	if h.supervisor.CheckTaskQuiescent(ctx, h.authority.approval().TaskID) != nil {
		return ErrE2HTTPClosure
	}
	if err := h.runtime.Close(ctx); err != nil {
		return err
	}
	if err := h.authority.close(); err != nil {
		return ErrE2HTTPClosure
	}
	h.closed.Store(true)
	return nil
}
