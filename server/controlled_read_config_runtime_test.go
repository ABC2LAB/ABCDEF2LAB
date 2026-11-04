package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

// The loopback, identities, approval and KG below are synthetic fixtures. They
// demonstrate runtime provenance within this process, not real host authority.
func configRuntimeFixture(t *testing.T, target string) (*Engine, *Task, *db.Node, controlledReadSpec) {
	t.Helper()
	e, task, intent, _ := runtimeReadFixture(t, target)
	key := controlledWorkKey{task.ID, intent.ID}
	spec := e.controlledReads.pending[key]
	spec.HTTP.MaxRequests = 2
	spec.HTTP.Sessions = append(spec.HTTP.Sessions, transmit.Session{
		Account:   idor.AccountRef{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified},
		ExpiresAt: time.Now().Add(time.Hour), Headers: http.Header{"X-Session": {runtimeSecret + "-peer"}},
	})
	spec.HTTP.Resources[0].Accounts = []string{"owner", "peer"}
	spec.HTTP.Resources[0].URL = target + "/document?private=" + runtimeSecret
	spec.HTTP.Resources[0].View = &transmit.JSONViewRule{
		StatusPath: []string{"status"}, NormalValue: "ok", DataPath: []string{"data"},
		Markers: []idor.Marker{{Field: "note", Value: verificationTestMarker}},
	}
	e.controlledReads.pending[key] = spec
	return e, task, intent, spec
}

func configRuntimeExpected(r ControlledReadVerificationRequest, spec controlledReadSpec) evidencebind.Context {
	return evidencebind.Context{Scope: r.Scope, IntentID: r.IntentID, ResourceRef: r.ResourceRef,
		ReadApprovalRef: "read-approval", KGPolicyVersion: "synthetic-config-kg-policy",
		Owner: spec.HTTP.Sessions[0].Account, Evaluated: spec.HTTP.Sessions[1].Account}
}

func configRuntimeKG() *verificationTestKG {
	return &verificationTestKG{policy: oracle.PolicyAssertion{
		ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
		HasRegisteredSource: true, HintMatchesCanonical: true,
		ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour),
	}}
}

func configRuntimeSource(context.Context, evidencebind.Context) (e3record.Source, error) {
	return e3record.Source{}, errors.New("synthetic unused source")
}

func TestControlledReadConfigRuntimeActualSnapshotAndBind(t *testing.T) {
	var hits, finalized atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		want := runtimeSecret
		if n == 2 {
			want += "-peer"
		}
		if r.URL.Path != "/document" || r.URL.Query().Get("private") != runtimeSecret || r.Header.Get("X-Session") != want {
			t.Error("actual sender did not retain the approved URL/session snapshot")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","data":{"note":"`+verificationTestMarker+`","private":"`+runtimeSecret+`"}}`)
	}))
	defer target.Close()
	e, task, intent, spec := configRuntimeFixture(t, target.URL)
	// Preserve the trusted fixture account values before mutating its old aliases.
	owner, peer := spec.HTTP.Sessions[0].Account, spec.HTTP.Sessions[1].Account
	var request ControlledReadVerificationRequest
	e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
		request = r
		view, err := r.ConfigView(ctx)
		if err != nil || view.Scope != r.Scope || view.Sessions[0].Account != owner || view.Sessions[1].Account != peer || view.Resources[0].ApprovalRef != "read-approval" {
			t.Fatal("resolver did not receive runtime's exact frozen metadata")
		}
		for _, value := range []any{r, view} {
			body, err := json.Marshal(value)
			if err != nil || strings.Contains(string(body), runtimeSecret) || strings.Contains(string(body), "config") || strings.Contains(string(body), "Headers") {
				t.Fatal("private config or credentials escaped JSON")
			}
			if strings.Contains(fmt.Sprintf("%#v", value), runtimeSecret) {
				t.Fatal("private config escaped formatting")
			}
		}
		view.Sessions[0].Account.Handle = "mutated-view"
		view.Resources[0].Accounts[0] = "mutated-view"
		expected := evidencebind.Context{Scope: r.Scope, IntentID: r.IntentID, ResourceRef: r.ResourceRef,
			ReadApprovalRef: "read-approval", KGPolicyVersion: "synthetic-config-kg-policy", Owner: owner, Evaluated: peer}
		bound, err := r.BindRealInputs(ctx, expected, configRuntimeKG(), configRuntimeSource)
		if err != nil || bound.Expected != expected {
			t.Fatal("actual runtime Bind lost original approved snapshot")
		}
		return ControlledReadVerificationPlan{Candidate: bound.Candidate, KG: bound.KG,
			Collect: func(_ context.Context, evidence ControlledReadVerificationEvidence) error {
				for i, receipt := range []transmit.Receipt{evidence.Verification.Owner.Receipt, evidence.Verification.Evaluated.Receipt} {
					account := []idor.AccountRef{owner, peer}[i]
					if receipt.Scope != r.Scope || receipt.ApprovalRef != expected.ReadApprovalRef || receipt.ResourceRef != expected.ResourceRef || receipt.AccountRef != account.Label || receipt.SessionRef != account.Handle {
						t.Fatal("actual sender receipt differs from resolver's approved metadata")
					}
				}
				return nil
			},
			Finalize: func(ctx context.Context, evidence ControlledReadVerificationEvidence) error {
				if evidence.Request != r || ctx.Err() != nil {
					t.Fatal("finalization lost comparable runtime request")
				}
				if _, err := r.ConfigView(ctx); err == nil {
					t.Fatal("new config lookup remained possible during finalization")
				}
				finalized.Add(1)
				return nil
			}}, nil
	}}
	runCtx := context.WithValue(t.Context(), controlledReadCompletionClaimKey{}, func() bool { return true })
	ctx, finish, err := e.prepareControlledRead(runCtx, task, intent)
	if err != nil || finish == nil {
		t.Fatal("runtime initialization failed")
	}
	defer finish(false)
	// Both NewHTTP and the handle copied the exact cfg before it was exposed.
	spec.HTTP.Sessions[0].Account.Handle = "mutated-original"
	spec.HTTP.Sessions[0].Headers.Set("X-Session", "mutated-original")
	spec.HTTP.Resources[0].Accounts[0] = "mutated-original"
	spec.HTTP.Resources[0].URL = target.URL + "/tampered"
	provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
	if _, _, err := runtimeWorker(t, provider).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil); err != nil {
		t.Fatal("worker failed")
	}
	if hits.Load() != 2 {
		t.Fatal("owner/cross did not use actual transmitter")
	}
	if err := finish(true); err != nil || finalized.Load() != 1 {
		t.Fatal("completed audit/finalization order changed")
	}
	if _, err := request.ConfigView(t.Context()); err == nil {
		t.Fatal("finished handle accepted a new lookup")
	}
	if _, err := request.BindRealInputs(t.Context(), configRuntimeExpected(request, spec), configRuntimeKG(), configRuntimeSource); err == nil {
		t.Fatal("finished handle accepted Bind")
	}
}

func TestControlledReadConfigRuntimeRequestIsolation(t *testing.T) {
	e, task, intent, _ := configRuntimeFixture(t, "http://127.0.0.1:1")
	var request ControlledReadVerificationRequest
	e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
		request = r
		if _, err := r.ConfigView(ctx); err != nil {
			t.Fatal("actual request missing handle")
		}
		return ControlledReadVerificationPlan{}, ErrControlledFindingUnavailable
	}}
	ctx, finish, err := e.prepareControlledRead(t.Context(), task, intent)
	if err != nil {
		t.Fatal(err)
	}
	defer finish(false)
	provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
	_, _, _ = runtimeWorker(t, provider).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil)
	if request.config == nil {
		t.Fatal("runtime never delivered handle")
	}
	for _, field := range []string{"task", "intent", "run", "reset", "account", "resource"} {
		t.Run(field, func(t *testing.T) {
			other := request
			switch field {
			case "task":
				other.TaskID++
			case "intent":
				other.IntentID++
			case "run":
				other.Scope.RunRef = "other-run"
			case "reset":
				other.Scope.ResetRef = "other-reset"
			case "account":
				other.AccountRef = "owner"
			case "resource":
				other.ResourceRef = "other:resource"
			}
			if _, err := other.ConfigView(t.Context()); err == nil {
				t.Fatal("different execution/request accepted")
			}
		})
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var reconstructed ControlledReadVerificationRequest
	if json.Unmarshal(body, &reconstructed) != nil || reconstructed == request {
		t.Fatal("JSON reconstructed private runtime provenance")
	}
	if _, err := reconstructed.ConfigView(t.Context()); err == nil {
		t.Fatal("matching public fields manufactured runtime handle")
	}
	if reconstructed.Scope != request.Scope {
		t.Fatal("fixture did not preserve matching public scope")
	}
}

func TestControlledReadConfigRuntimeCancellationExpiryAndReregistration(t *testing.T) {
	for _, mode := range []string{"work-cancel", "expiry", "close"} {
		t.Run(mode, func(t *testing.T) {
			e, task, intent, _ := configRuntimeFixture(t, "http://127.0.0.1:1")
			var expires time.Time
			if mode == "expiry" {
				// Exercise the actual configured session deadline, not a forged
				// handle state or caller-supplied clock.
				key := controlledWorkKey{task.ID, intent.ID}
				spec := e.controlledReads.pending[key]
				expires = time.Now().Add(3 * time.Second)
				spec.HTTP.Sessions[0].ExpiresAt = expires
				e.controlledReads.pending[key] = spec
			}
			var request ControlledReadVerificationRequest
			e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
				request = r
				return ControlledReadVerificationPlan{}, ErrControlledFindingUnavailable
			}}
			life, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx, finish, err := e.prepareControlledRead(life, task, intent)
			if err != nil {
				t.Fatal(err)
			}
			defer finish(false)
			provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
			_, _, _ = runtimeWorker(t, provider).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil)
			if _, err := request.ConfigView(t.Context()); err != nil {
				t.Fatal("live handle unavailable")
			}
			switch mode {
			case "work-cancel":
				cancel()
			case "expiry":
				timer := time.NewTimer(time.Until(expires))
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-t.Context().Done():
					t.Fatal("test cancelled before registered expiry")
				}
			case "close":
				_ = finish(false)
			}
			if _, err := request.ConfigView(t.Context()); err == nil {
				t.Fatal("cancelled/expired/closed runtime accepted new query")
			}
		})
	}

	t.Run("reregister", func(t *testing.T) {
		e, task, intent, _ := runtimeReadFixture(t, "http://127.0.0.1:1")
		var requests []ControlledReadVerificationRequest
		e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
			requests = append(requests, r)
			return ControlledReadVerificationPlan{}, ErrControlledFindingUnavailable
		}}
		for i := 0; i < 2; i++ {
			if i > 0 {
				e.Pause(task.ID, agent.AbortPausedByUser)
				task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = true })
				if err := e.registerControlledRead(t.Context(), task, intent.ID, "approved"); err != nil {
					t.Fatal("actual reregistration failed")
				}
				e.paused.Delete(task.ID)
				task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = false })
			}
			ctx, finish, err := e.prepareControlledRead(t.Context(), task, intent)
			if err != nil {
				t.Fatal(err)
			}
			p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"owner","resource_ref":"doc:one"}`}
			_, _, _ = runtimeWorker(t, p).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil)
			if len(requests) != i+1 {
				t.Fatal("resolver request missing")
			}
			if _, err := requests[i].ConfigView(t.Context()); err != nil {
				t.Fatal("fresh registered execution unavailable")
			}
			_ = finish(false)
		}
		if requests[0].Scope.RunRef == requests[1].Scope.RunRef || requests[0].config == requests[1].config {
			t.Fatal("reregistration reused old execution")
		}
		for _, r := range requests {
			if _, err := r.ConfigView(t.Context()); err == nil {
				t.Fatal("old registered handle remained live")
			}
		}
	})
}

func TestControlledReadConfigRuntimeInitializationFailureNeverPublishesRequest(t *testing.T) {
	for _, mode := range []string{"invalid-http", "expired-handle"} {
		t.Run(mode, func(t *testing.T) {
			e, task, intent, _ := configRuntimeFixture(t, "http://127.0.0.1:1")
			var calls atomic.Int64
			e.controlledReads.verification = &controlledReadVerificationConfig{lifecycle: t.Context(), resolve: func(context.Context, ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
				calls.Add(1)
				return ControlledReadVerificationPlan{}, ErrControlledFindingUnavailable
			}}
			key := controlledWorkKey{task.ID, intent.ID}
			spec := e.controlledReads.pending[key]
			if mode == "invalid-http" {
				spec.HTTP.Resources[0].URL = "invalid-private-url-" + runtimeSecret
			} else {
				spec.HTTP.Sessions[0].ExpiresAt = time.Now().Add(-time.Second)
			}
			e.controlledReads.pending[key] = spec
			_, finish, err := e.prepareControlledRead(t.Context(), task, intent)
			if err == nil || finish != nil || calls.Load() != 0 || strings.Contains(err.Error(), runtimeSecret) {
				t.Fatal("initialization published a request or leaked private data")
			}
			if len(e.controlledReads.active) != 0 || len(e.controlledReads.pending) != 0 {
				t.Fatal("failed initialization retained runtime execution")
			}
		})
	}
}

// The private pointer keeps every existing ==/!= request comparison legal.
var _ = ControlledReadVerificationRequest{} == ControlledReadVerificationRequest{}

// Real owned PG/publisher construction, synthetic authority. Both cancellation
// paths must reach the exact HTTP context also retained by the runtime handle.
func TestControlledReadConfigRuntimePublishedLifetimeCancellation(t *testing.T) {
	for _, mode := range []string{"work", "publisher-root"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int64
			target := controlledReadTarget(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			f := newControlledVerificationDBFixture(t, target.URL)
			host := installDurableTestPublisher(t, f, "success")
			var request ControlledReadVerificationRequest
			if err := f.s.ConfigureControlledReadVerification(func(ctx context.Context, actual ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
				request = actual
				if _, err := actual.ConfigView(ctx); err != nil {
					t.Fatal("published runtime did not supply live view")
				}
				return ControlledReadVerificationPlan{}, ErrControlledFindingUnavailable
			}); err != nil {
				t.Fatal("verification fixture configuration")
			}
			f.registerAndResume(t)
			work, cancelWork := context.WithCancel(t.Context())
			defer cancelWork()
			ctx, finish, err := f.e.prepareControlledRead(work, f.task, f.intent)
			if err != nil {
				t.Fatal("published runtime initialization")
			}
			defer finish(false)
			id, err := strconv.ParseInt(f.task.ID, 10, 64)
			if err != nil {
				t.Fatal("task fixture identity")
			}
			provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: `{"account_ref":"peer","resource_ref":"doc:one"}`}
			_, _, _ = runtimeWorker(t, provider).Execute(ctx, "worker", id, nil, nil, f.intent, nil, nil, nil, nil)
			if request.config == nil || hits.Load() != 0 {
				t.Fatal("resolver handle missing or refused candidate transmitted")
			}
			request.config.state.mu.Lock()
			httpLife := request.config.state.life
			request.config.state.mu.Unlock()
			if mode == "work" {
				cancelWork()
			} else {
				host.revokeRoot()
			}
			select {
			case <-httpLife.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("published HTTP context did not receive cancellation")
			}
			if _, err := request.ConfigView(t.Context()); err == nil {
				t.Fatal("cancelled published execution accepted a new metadata query")
			}
		})
	}
}
