package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

// Synthetic authority only. These tests prove ownership/cancellation wiring,
// not operational identity, lease provenance, or PostgreSQL behavior.
type finalizationTestScope struct {
	ctx              context.Context
	checks, releases int
	check            func(context.Context) error
}

func (s *finalizationTestScope) Context() context.Context { return s.ctx }
func (s *finalizationTestScope) Check(ctx context.Context) error {
	s.checks++
	if s.check != nil {
		return s.check(ctx)
	}
	return nil
}
func (s *finalizationTestScope) Release() { s.releases++ }

type finalizationTestProvider struct {
	acquire func(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error)
}

func (p *finalizationTestProvider) Acquire(ctx context.Context, request ControlledReadVerificationRequest, expected evidencebind.Context) (ControlledReadFinalizationScope, error) {
	return p.acquire(ctx, request, expected)
}

func TestFinalizationScopeWholeCollector(t *testing.T) {
	for _, mode := range []string{"success", "acquire-failure", "check-failure", "prepare-before-save-check-failure", "source-error", "prepare-error", "source-revoke", "source-cancel", "save-error", "save-revoke", "commit-success-cancel", "ack-lost-cancel"} {
		t.Run(mode, func(t *testing.T) {
			in, source := finalizationRecordFixture()
			checkpoint := auditlog.Checkpoint{Version: auditlog.Version, JournalID: "synthetic", Scope: in.Expected.Scope, Sequence: 4, MAC: hex.EncodeToString(make([]byte, sha256.Size))}
			digest, err := ControlledReadCheckpointDigest(checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			source.FinalCheckpointDigest = digest
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			type contextKey struct{}
			leaseCtx, revoke := context.WithCancel(context.WithValue(caller, contextKey{}, "host-lease"))
			defer revoke()
			scope := &finalizationTestScope{ctx: leaseCtx}
			c := &controlledFindingCollector{collected: true, input: in, digest: source.EvidenceDigest, createdAt: source.RecordCreatedAt}
			c.request = ControlledReadVerificationRequest{Scope: in.Expected.Scope}
			c.plan = ControlledReadFindingPlan{Expected: in.Expected, RequireFinalizationScope: true}
			calls, saves := 0, 0
			preSaveRejected := false
			unlocked := func() {
				t.Helper()
				if !c.mu.TryLock() {
					t.Fatal("external callback under collector mutex")
				}
				c.mu.Unlock()
				if scope.releases != 0 {
					t.Fatal("lease released before synchronous operation completed")
				}
			}
			scope.check = func(ctx context.Context) error {
				unlocked()
				if mode == "check-failure" {
					return errors.New("synthetic initial authority check failed")
				}
				if mode == "prepare-before-save-check-failure" && c.prepared {
					if ctx.Err() != nil || caller.Err() != nil || leaseCtx.Err() != nil {
						t.Fatal("pre-save Check denial was masked by context cancellation")
					}
					preSaveRejected = true
					return errors.New("synthetic authority check failed after Prepare")
				}
				return nil
			}
			c.plan.FinalizationScope = &finalizationTestProvider{acquire: func(_ context.Context, request ControlledReadVerificationRequest, expected evidencebind.Context) (ControlledReadFinalizationScope, error) {
				unlocked()
				if request != c.request || expected != in.Expected {
					t.Fatal("scope binding changed")
				}
				if mode == "acquire-failure" {
					return nil, errors.New("synthetic acquisition failed")
				}
				return scope, nil
			}}
			c.plan.Source = func(ctx context.Context, expected evidencebind.Context) (e3record.Source, error) {
				unlocked()
				calls++
				if ctx.Value(contextKey{}) != "host-lease" || expected != in.Expected {
					t.Fatal("provider context lost")
				}
				switch mode {
				case "source-error":
					return e3record.Source{}, errors.New("private authority failure")
				case "prepare-error":
					source.EvidenceRevision = 0
				case "source-revoke":
					revoke()
				case "source-cancel":
					cancel()
				}
				return source, nil
			}
			c.save = func(ctx context.Context, prepared e3record.Prepared, _ db.TaskModeMarkerReader) (db.ControlledFindingResult, error) {
				unlocked()
				saves++
				if _, ok := prepared.Snapshot(); !ok {
					t.Fatal("invalid prepared record reached save")
				}
				if ctx.Value(contextKey{}) != "host-lease" {
					t.Fatal("save lost scope context")
				}
				switch mode {
				case "save-error":
					return db.ControlledFindingResult{}, db.ErrControlledFindingInput
				case "save-revoke":
					revoke()
					select {
					case <-ctx.Done():
					case <-time.After(time.Second):
						t.Fatal("revocation not propagated")
					}
					unlocked()
					return db.ControlledFindingResult{}, ctx.Err()
				case "ack-lost-cancel":
					cancel()
					return db.ControlledFindingResult{}, db.ErrControlledFindingStore
				case "commit-success-cancel":
					cancel()
				}
				return db.ControlledFindingResult{}, nil
			}
			err = c.finalize(caller, ControlledReadVerificationEvidence{Request: c.request, Completed: true, Finalized: true, FinalCheckpoint: checkpoint})
			wantReleases, wantSources := 1, 1
			if mode == "acquire-failure" {
				wantReleases = 0
			}
			if mode == "acquire-failure" || mode == "check-failure" {
				wantSources = 0
			}
			if scope.releases != wantReleases || c.finalizing || calls != wantSources {
				t.Fatalf("release=%d finalizing=%v sources=%d", scope.releases, c.finalizing, calls)
			}
			if mode == "prepare-before-save-check-failure" && !preSaveRejected {
				t.Fatal("test did not reach the post-Prepare Check")
			}
			wantSave := mode == "success" || mode == "save-error" || mode == "save-revoke" || mode == "commit-success-cancel" || mode == "ack-lost-cancel"
			if (saves == 1) != wantSave {
				t.Fatalf("save calls=%d", saves)
			}
			switch mode {
			case "success", "commit-success-cancel":
				if err != nil || !c.saved {
					t.Fatalf("committed success changed: %v", err)
				}
			case "ack-lost-cancel", "save-revoke":
				if !errors.Is(err, ErrControlledFindingOutcomeUnknown) {
					t.Fatalf("uncertain write changed to rollback: %v", err)
				}
			default:
				if err == nil || c.saved {
					t.Fatal("failed stage marked saved")
				}
			}
		})
	}
}

func TestFinalizationScopeAdmissionAndCleanup(t *testing.T) {
	for _, mode := range []string{"missing", "typed-nil-provider", "typed-nil-provider-compatibility", "acquire-failure", "partial-acquire", "nil-scope", "typed-nil-scope", "nil-context", "typed-nil-context", "uncancellable", "revoked", "check-failure", "caller-cancel", "success", "compatibility"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			lease, revoke := context.WithCancel(ctx)
			defer revoke()
			scope := &finalizationTestScope{ctx: lease}
			c := &controlledFindingCollector{plan: ControlledReadFindingPlan{RequireFinalizationScope: mode != "compatibility" && mode != "typed-nil-provider-compatibility"}}
			var acquired int
			if mode != "missing" && mode != "compatibility" {
				c.plan.FinalizationScope = &finalizationTestProvider{acquire: func(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error) {
					acquired++
					switch mode {
					case "acquire-failure":
						return nil, errors.New("private")
					case "partial-acquire":
						return scope, errors.New("private")
					case "nil-scope":
						return nil, nil
					case "typed-nil-scope":
						return (*finalizationTestScope)(nil), nil
					case "nil-context":
						scope.ctx = nil
					case "typed-nil-context":
						scope.ctx = (*finalizationNilContext)(nil)
					case "uncancellable":
						scope.ctx = context.Background()
					case "revoked":
						revoke()
					case "check-failure":
						scope.check = func(context.Context) error { return errors.New("revoked") }
					case "caller-cancel":
						cancel()
					}
					return scope, nil
				}}
			}
			if mode == "typed-nil-provider" || mode == "typed-nil-provider-compatibility" {
				c.plan.FinalizationScope = (*finalizationTestProvider)(nil)
			}
			operation, check, release, err := c.acquireFinalizationScope(ctx)
			if mode == "success" || mode == "compatibility" {
				if err != nil || operation.Err() != nil || check() != nil {
					t.Fatalf("valid scope refused: %v", err)
				}
				release()
				release()
				if mode == "success" && scope.releases != 1 {
					t.Fatal("duplicate release")
				}
			} else if !errors.Is(err, ErrControlledFindingUnavailable) {
				t.Fatalf("invalid scope accepted: %v", err)
			}
			if acquired > 0 && mode != "acquire-failure" && mode != "nil-scope" && mode != "typed-nil-scope" && scope.releases != 1 {
				t.Fatalf("partial lease leaked: %d", scope.releases)
			}
		})
	}
}

type finalizationNilContext struct{ context.Context }

func TestFinalizationScopeStateBeforeAcquire(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	in, _ := finalizationRecordFixture()
	checkpoint := auditlog.Checkpoint{Version: auditlog.Version, JournalID: "synthetic", Scope: in.Expected.Scope, Sequence: 4, MAC: hex.EncodeToString(make([]byte, sha256.Size))}
	c := &controlledFindingCollector{request: ControlledReadVerificationRequest{Scope: in.Expected.Scope}}
	c.plan.FinalizationScope = &finalizationTestProvider{acquire: func(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error) {
		t.Fatal("acquired before collected state check")
		return nil, nil
	}}
	if err := c.finalize(ctx, ControlledReadVerificationEvidence{Request: c.request, Completed: true, Finalized: true, FinalCheckpoint: checkpoint}); !errors.Is(err, ErrControlledFindingUnavailable) {
		t.Fatal(err)
	}
}

// Same synthetic evidence shape as e3record fixtures, with no live authority.
func finalizationRecordFixture() (e3record.Input, e3record.Source) {
	at := time.Date(2026, 11, 3, 12, 0, 20, 0, time.UTC)
	c := evidencebind.Context{Scope: transmit.Scope{RunRef: "run-1", TaskRef: "1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1"},
		IntentID: 11, ResourceRef: "document:one", ReadApprovalRef: "read-1", KGPolicyVersion: "kg-1",
		Owner:     idor.AccountRef{Label: "owner", Handle: "session-a", Status: oracle.SubjectVerified},
		Evaluated: idor.AccountRef{Label: "reader", Handle: "session-b", Status: oracle.SubjectVerified}}
	p := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
		HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}
	receipt := func(a idor.AccountRef, n uint64, admitted, done time.Time) transmit.Receipt {
		return transmit.Receipt{Scope: c.Scope, Attempt: n, AccountRef: a.Label,
			SessionRef: a.Handle, ResourceRef: c.ResourceRef, ApprovalRef: c.ReadApprovalRef, Method: http.MethodGet, Reason: transmit.Completed,
			Admitted: true, AdmittedAt: admitted, RequestWritten: true, ResponseReceived: true, StatusCode: 200, At: done}
	}
	owner := evidencebind.Observation{Context: c, Receipt: receipt(c.Owner, 1, at.Add(-10*time.Second), at.Add(-8*time.Second)),
		Evidence: evidencebind.EvidenceRef{ID: "ev-owner", IntentID: c.IntentID, Scope: c.Scope, Attempt: 1, Role: evidencebind.OwnerRead}, ObservedAt: at.Add(-9 * time.Second)}
	cross := evidencebind.Observation{Context: c, Receipt: receipt(c.Evaluated, 2, at.Add(-5*time.Second), at.Add(time.Second)),
		Evidence: evidencebind.EvidenceRef{ID: "ev-cross", IntentID: c.IntentID, Scope: c.Scope, Attempt: 2, Role: evidencebind.CrossRead}, ObservedAt: at}
	f := oracle.ComparisonFacts{SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes, CrossNormalData: oracle.StateYes,
		NontrivialDataMatch: oracle.StateYes, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes}
	result := idor.Result{Facts: f, Verdict: oracle.Evaluate(oracle.CaseInput{Policy: p,
		Subjects: oracle.SubjectContext{OwnerStatus: c.Owner.Status, EvaluatedStatus: c.Evaluated.Status, OwnerRef: c.Owner.Label, EvaluatedRef: c.Evaluated.Label}, Facts: f}, at)}
	in := e3record.Input{Expected: c, CanaryFields: []string{"note"}, Evidence: evidencebind.Input{Policy: evidencebind.PolicySnapshot{Context: c, Assertion: p}, Owner: owner, Cross: cross,
		Result: evidencebind.ResultSnapshot{Context: c, OwnerEvidence: owner.Evidence, CrossEvidence: cross.Evidence, ObservedAt: at, Result: result}}}
	source := e3record.Source{Expected: c, ExplorationID: 2, ModeVersion: 1, EvidenceRevision: 1, KGSnapshotRef: "kg-snapshot-1", KGSnapshotDigest: sha256.Sum256([]byte("synthetic KG")),
		GenerationRef: "fixture-generation-1", InstanceRef: "synthetic-instance", ModelTag: "synthetic-model", ResetGeneration: "reset-generation-1",
		Policy: p, CanaryFields: []string{"note"}, Severity: "high", FinalCheckpointRef: "checkpoint-1", FinalCheckpointDigest: sha256.Sum256([]byte("synthetic final checkpoint")),
		FinalCheckpointScope: c.Scope, FinalCheckpointSequence: 4, RecordCreatedAt: at.Add(10 * time.Second), Completed: true, Finalized: true}
	digest, err := e3record.EvidenceDigest(in.Evidence)
	if err != nil {
		panic("invalid synthetic evidence fixture")
	}
	source.EvidenceDigest = digest
	return in, source
}

func TestFinalizationScopeIndependentCallerCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "host-revoke"} {
		t.Run(mode, func(t *testing.T) {
			caller, cancel := context.WithCancel(t.Context())
			if mode == "deadline" {
				cancel()
				caller, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
			}
			defer cancel()
			host, revoke := context.WithCancel(context.Background())
			defer revoke()
			scope := &finalizationTestScope{ctx: host}
			c := &controlledFindingCollector{plan: ControlledReadFindingPlan{RequireFinalizationScope: true,
				FinalizationScope: &finalizationTestProvider{acquire: func(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error) {
					return scope, nil
				}}}}
			operation, check, release, err := c.acquireFinalizationScope(caller)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if mode == "cancel" {
				cancel()
			}
			if mode == "host-revoke" {
				revoke()
			}
			select {
			case <-operation.Done():
			case <-time.After(time.Second):
				t.Fatal("independent cancellation not propagated")
			}
			if check() == nil || scope.releases != 0 {
				t.Fatal("revoked lease accepted or released while operation still owns it")
			}
			release()
			release()
			if scope.releases != 1 {
				t.Fatal("release count")
			}
		})
	}
}

// The host context is independently owned: caller cancellation must be observed
// even before the asynchronous AfterFunc reaches the operation context.
func TestFinalizationScopeIndependentHostCallerCancellation(t *testing.T) {
	for _, stage := range []string{"before-check", "during-check"} {
		t.Run(stage, func(t *testing.T) {
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			host, releaseHost := context.WithCancel(context.Background())
			defer releaseHost()
			scope := &finalizationTestScope{ctx: host}
			armed := false
			scope.check = func(context.Context) error {
				if armed && stage == "during-check" {
					cancel()
				}
				return nil
			}
			c := &controlledFindingCollector{plan: ControlledReadFindingPlan{RequireFinalizationScope: true,
				FinalizationScope: &finalizationTestProvider{acquire: func(context.Context, ControlledReadVerificationRequest, evidencebind.Context) (ControlledReadFinalizationScope, error) {
					return scope, nil
				}}}}
			_, check, release, err := c.acquireFinalizationScope(caller)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			armed = true
			if stage == "before-check" {
				cancel()
			}
			if err := check(); !errors.Is(err, ErrControlledFindingUnavailable) || host.Err() != nil {
				t.Fatal("caller cancellation was lost or cancelled independent host authority")
			}
		})
	}
}
