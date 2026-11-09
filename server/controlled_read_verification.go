package server

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	actool "github.com/Autumn-27/norma/tool"
)

// These types are trusted Go-host seams, not HTTP/model JSON authority.
type ControlledReadVerificationRequest struct {
	Scope                   transmit.Scope
	TaskID, IntentID        int64
	AccountRef, ResourceRef string
	// Runtime-only pointer: requests stay comparable and JSON/model input
	// cannot mint or recover a handle from matching reference strings.
	config *ControlledReadConfigHandle
}

// ConfigView returns a detached, non-sensitive projection of this execution's
// actual HTTP configuration. It is metadata, not proof of identity/approval.
// The runtime lifecycle and request binding must still be valid at each call.
func (r ControlledReadVerificationRequest) ConfigView(ctx context.Context) (ControlledReadConfigView, error) {
	return r.config.View(ctx, r)
}

// BindRealInputs uses only the actual runtime handle's private projection with
// #165. C supplies authentic Context/KG/Source; this does not authenticate those
// providers, assemble audit truth, or fill collector/revision-owned fields.
func (r ControlledReadVerificationRequest) BindRealInputs(ctx context.Context, expected evidencebind.Context, kg idor.PolicyResolver, source e3record.SourceReader) (ControlledReadFindingPlan, error) {
	return r.config.Bind(ctx, r, expected, kg, source)
}

type ControlledReadVerificationEvidence struct {
	Request         ControlledReadVerificationRequest
	Verification    transmit.ReadVerification
	FinalCheckpoint auditlog.Checkpoint
	Completed       bool
	Finalized       bool
}

type ControlledReadVerificationPlan struct {
	Candidate idor.Candidate
	KG        idor.PolicyResolver
	// Collect binds observation references during the actual verification call.
	// It is not execution completion or a finding-storage authorization.
	Collect func(context.Context, ControlledReadVerificationEvidence) error
	// Finalize captures separately trusted identity/KG/fixture provenance.
	// It must not issue new target requests or infer that provenance from refs.
	Finalize func(context.Context, ControlledReadVerificationEvidence) error
}

type ControlledReadVerificationResolver func(context.Context, ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error)

type controlledReadCompletionClaimKey struct{}

type controlledReadVerificationConfig struct {
	resolve   ControlledReadVerificationResolver
	lifecycle context.Context
}

// ConfigureControlledReadVerification is initialization-only. The trusted host
// supplies a real resolver; omission retains the existing non-finding E2 read.
// An enabled resolver that cannot resolve a case never falls back to that read.
func (s *Server) ConfigureControlledReadVerification(resolve ControlledReadVerificationResolver) error {
	if s == nil || s.ctx == nil || s.ctx.Err() != nil || s.engine == nil || resolve == nil {
		return errControlledReadUnavailable
	}
	r := s.engine.controlledReads
	if r == nil {
		return errControlledReadUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.mode == nil || r.verification != nil || len(r.pending) != 0 || len(r.active) != 0 {
		return errControlledReadUnavailable
	}
	r.verification = &controlledReadVerificationConfig{resolve: resolve, lifecycle: s.ctx}
	return nil
}

type controlledReadVerificationState struct {
	mu        sync.Mutex
	host      *controlledReadVerificationConfig
	runCtx    context.Context
	taskID    int64
	intentID  int64
	selected  bool
	inFlight  bool
	failed    bool
	finalized bool
	account   string
	resource  string
	request   ControlledReadVerificationRequest
	plan      ControlledReadVerificationPlan
	detail    transmit.ReadVerification
	output    []byte
}

func (l *controlledReadLease) verificationTool() actool.CoreTool {
	return roTool(agent.ControlledHTTPToolName, "Verify one registered owner/cross read case. The server resolves identity and policy; output is staged evidence only. Host audit finalization and storage decide whether a finding is saved.", map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"account_ref":  map[string]any{"type": "string", "enum": l.accounts},
			"resource_ref": map[string]any{"type": "string", "enum": l.resources},
		}, "required": []string{"account_ref", "resource_ref"},
	}, func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
		account, resource, err := controlledReadInput(input)
		if err != nil || ctx == nil || ctx.Err() != nil {
			l.markVerificationFailure()
			return actool.Errorf("controlled_read_verification_unavailable"), nil
		}
		stop := context.AfterFunc(ctx, l.tx.Stop)
		defer stop()
		out, err := l.verification.execute(ctx, l, account, resource)
		if err != nil {
			l.markVerificationFailure()
			return actool.Errorf("controlled_read_verification_unavailable"), nil
		}
		l.resultMu.Lock()
		l.observed = true
		l.resultMu.Unlock()
		return actool.Text(string(out)), nil
	})
}

func (l *controlledReadLease) markVerificationFailure() {
	l.resultMu.Lock()
	l.denied = true
	l.resultMu.Unlock()
}

func (v *controlledReadVerificationState) execute(ctx context.Context, l *controlledReadLease, account, resource string) (out []byte, err error) {
	if ctx == nil || ctx.Err() != nil || v.runCtx == nil || v.runCtx.Err() != nil ||
		v.host == nil || v.host.resolve == nil || v.host.lifecycle == nil || v.host.lifecycle.Err() != nil {
		return nil, errControlledReadUnavailable
	}
	v.mu.Lock()
	if v.selected {
		if v.failed || v.inFlight || v.account != account || v.resource != resource {
			v.mu.Unlock()
			return nil, errControlledReadUnavailable
		}
		out = append([]byte(nil), v.output...)
		v.mu.Unlock()
		return out, nil
	}
	v.selected, v.inFlight, v.account, v.resource = true, true, account, resource
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.inFlight = false
		v.failed = err != nil
		v.mu.Unlock()
	}()
	if v.host == nil || v.host.resolve == nil || v.host.lifecycle == nil || v.host.lifecycle.Err() != nil {
		return nil, errControlledReadUnavailable
	}
	request := ControlledReadVerificationRequest{Scope: l.scope, TaskID: v.taskID, IntentID: v.intentID, AccountRef: account, ResourceRef: resource, config: l.config}
	if l.mapAttempt(ctx, resource, account) != nil {
		return nil, errControlledReadUnavailable
	}
	plan, resolveErr := v.host.resolve(ctx, request)
	if resolveErr != nil || ctx.Err() != nil || plan.Finalize == nil || nilPolicyResolver(plan.KG) ||
		plan.Candidate.ResourceRef != resource || plan.Candidate.Evaluated.Label != account ||
		l.fixture != nil && !sameControlledReadFixture(plan.KG, l.fixture) {
		return nil, errControlledReadUnavailable
	}
	for _, subject := range []idor.AccountRef{plan.Candidate.Owner, plan.Candidate.Evaluated} {
		registered, ok := l.sessions[subject.Label]
		if !ok || registered != subject {
			return nil, errControlledReadUnavailable
		}
	}
	// 소유자 조회는 교차 계정 판정의 정상 대조이며 별도 판정 대상 셀이 아니다.
	detail, reason := l.tx.VerifyReadDetailed(plan.Candidate, plan.KG)
	if reason != "" || ctx.Err() != nil || !completeVerificationObservation(detail.Owner, l.scope) ||
		!completeVerificationObservation(detail.Evaluated, l.scope) || l.tx.HealthStatus().Halted {
		return nil, errControlledReadUnavailable
	}
	if plan.Collect != nil {
		collected := ControlledReadVerificationEvidence{Request: request, Verification: detail}
		if err := plan.Collect(ctx, collected); err != nil || ctx.Err() != nil {
			return nil, errControlledReadUnavailable
		}
	}
	out, marshalErr := json.Marshal(struct {
		Staged           bool
		Verdict          oracle.OracleVerdict
		Facts            oracle.ComparisonFacts
		OwnerReceipt     transmit.Receipt
		EvaluatedReceipt transmit.Receipt
	}{true, detail.Result.Verdict, detail.Result.Facts, detail.Owner.Receipt, detail.Evaluated.Receipt})
	if marshalErr != nil {
		return nil, errControlledReadUnavailable
	}
	v.mu.Lock()
	v.request, v.plan, v.detail, v.output = request, plan, detail, append([]byte(nil), out...)
	v.mu.Unlock()
	return out, nil
}

func completeVerificationObservation(read transmit.ReadObservation, scope transmit.Scope) bool {
	r := read.Receipt
	return read.Captured && !read.Capture.ObservedAt.IsZero() && r.Scope == scope && r.Attempt > 0 &&
		r.Method == "GET" && r.Reason == transmit.Completed && r.Admitted && r.RequestWritten && r.ResponseReceived
}

func nilPolicyResolver(resolver idor.PolicyResolver) bool {
	if resolver == nil {
		return true
	}
	value := reflect.ValueOf(resolver)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

func (v *controlledReadVerificationState) finish(l *controlledReadLease, completed bool, checkpoint auditlog.Checkpoint) error {
	v.mu.Lock()
	if !completed || !v.selected || v.inFlight || v.failed || v.finalized || v.plan.Finalize == nil ||
		v.host == nil || v.host.lifecycle == nil || v.runCtx == nil || v.host.lifecycle.Err() != nil {
		v.mu.Unlock()
		return errControlledReadUnavailable
	}
	v.finalized = true
	callback := v.plan.Finalize
	evidence := ControlledReadVerificationEvidence{Request: v.request, Verification: v.detail, FinalCheckpoint: checkpoint, Completed: true, Finalized: true}
	v.mu.Unlock()
	ctx, cancel := context.WithTimeout(v.host.lifecycle, l.timeout)
	defer cancel()
	// The engine atomically claimed completion before entering this method.
	// Later task pause controls future work, while server shutdown still cancels
	// this bounded context. A nil callback result means a known storage success;
	// later cancellation must not relabel that committed execution as stopped.
	if ctx.Err() != nil {
		return errControlledReadUnavailable
	}
	if err := callback(ctx, evidence); err != nil {
		logControlledFindingFinalizationFailure(err)
		return errControlledReadUnavailable
	}
	return nil
}

// Completion and explicit work cancellation linearize under workMu. A caller
// never holds this lock during journal or database I/O.
func (e *Engine) claimControlledReadCompletion(taskID string, intentID int64, expected *workExecution, workCtx, taskCtx context.Context) bool {
	e.workMu.Lock()
	defer e.workMu.Unlock()
	run := e.work[intentID]
	if run == nil || run != expected || run.completionClaimed || run.action != "" || workCtx.Err() != nil || taskCtx.Err() != nil ||
		e.IsPaused(taskID) || e.IsDeleting(taskID) {
		return false
	}
	run.completionClaimed = true
	return true
}
