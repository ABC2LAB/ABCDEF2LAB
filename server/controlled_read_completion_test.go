package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

// This fixture owns a fake identity, KG and target. It proves server wiring,
// never authentication/KG provenance or a real Juice Shop diagnostic.
func newControlledVerificationDBFixture(t *testing.T, target string) *controlledReadDBFixture {
	t.Helper()
	f := newControlledReadDBFixture(t, target)
	path := filepath.Join(f.store.snapshotDir, "approved.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot snapshotJSON
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.MaxRequests = 2
	snapshot.Sessions = append(snapshot.Sessions, sessionJSON{
		Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified,
		ExpiresAt: snapshot.ExpiresAt, HeaderFiles: map[string]string{"X-Session": "peer.secret"},
	})
	snapshot.Resources[0].Accounts = []string{"owner", "peer"}
	snapshot.Resources[0].View = &transmit.JSONViewRule{
		StatusPath: []string{"status"}, NormalValue: "ok", DataPath: []string{"data"},
		Markers: []idor.Marker{{Field: "note", Value: verificationTestMarker}},
	}
	body, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.store.snapshotDir, "peer.secret"), []byte("synthetic-peer-session"), 0o600); err != nil {
		t.Fatal(err)
	}
	configureModeDBFixture(t, f, func(context.Context) error { return nil })
	return f
}

func verificationFixturePlan(collect, finalize func(context.Context, ControlledReadVerificationEvidence) error) ControlledReadVerificationPlan {
	return ControlledReadVerificationPlan{
		Candidate: idor.Candidate{
			Owner:       idor.AccountRef{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified},
			Evaluated:   idor.AccountRef{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified},
			ResourceRef: "doc:one",
		},
		KG: &verificationTestKG{policy: oracle.PolicyAssertion{
			ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
			HasRegisteredSource: true, HintMatchesCanonical: true,
			ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour),
		}},
		Collect: collect, Finalize: finalize,
	}
}

func startVerifiedFixtureIntent(t *testing.T, f *controlledReadDBFixture) <-chan bool {
	t.Helper()
	f.registerAndResume(t)
	if err := f.intentState("running"); err != nil {
		t.Fatal(err)
	}
	if !f.e.beginTaskOperation(f.task.ID) {
		t.Fatal("could not reserve the actual controlled Worker")
	}
	done := make(chan bool, 1)
	provider := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: "{\"account_ref\":\"peer\",\"resource_ref\":\"doc:one\"}"}
	go func() {
		defer f.e.decInflight(f.task.ID)
		done <- f.e.runIntent(context.Background(), f.task, "worker", runtimeWorker(t, provider), f.intent, "", "")
	}()
	return done
}

func verificationFixtureTarget(t *testing.T) string {
	t.Helper()
	return controlledReadTarget(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "{\"status\":\"ok\",\"data\":{\"note\":\""+verificationTestMarker+"\"}}")
	})).URL
}

func TestControlledCompletionCancellationWinsBeforeClaim(t *testing.T) {
	f := newControlledVerificationDBFixture(t, verificationFixtureTarget(t))
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var sinks atomic.Int64
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer unblock()
	if err := f.s.ConfigureControlledReadVerification(func(context.Context, ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
		return verificationFixturePlan(func(ctx context.Context, _ ControlledReadVerificationEvidence) error {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			return ctx.Err()
		}, func(context.Context, ControlledReadVerificationEvidence) error {
			sinks.Add(1)
			return nil
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	done := startVerifiedFixtureIntent(t, f)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("verification did not stage its actual reads")
	}
	control := make(chan error, 1)
	go func() { control <- f.e.ControlWork(context.Background(), f.intent.ID, "cancel") }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("prior cancellation did not reach the actual work context")
	}
	unblock()
	select {
	case err := <-control:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not drain the unclaimed execution")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled intent did not settle")
	}
	node, err := f.task.Store.GetNode(f.intent.ID)
	if err != nil || node == nil || node.State != "paused" || sinks.Load() != 0 {
		t.Fatalf("pre-claim cancellation saved a result: state=%+v sinks=%d err=%v", node, sinks.Load(), err)
	}
}

func TestControlledCompletionClaimPrecedesLateTaskPause(t *testing.T) {
	for _, mode := range []string{"commit", "store-error"} {
		t.Run(mode, func(t *testing.T) {
			f := newControlledVerificationDBFixture(t, verificationFixtureTarget(t))
			entered, release := make(chan struct{}), make(chan struct{})
			var sinks atomic.Int64
			var released atomic.Bool
			unblock := func() {
				if released.CompareAndSwap(false, true) {
					close(release)
				}
			}
			defer unblock()
			if err := f.s.ConfigureControlledReadVerification(func(context.Context, ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
				return verificationFixturePlan(nil, func(ctx context.Context, evidence ControlledReadVerificationEvidence) error {
					if !evidence.Completed || !evidence.Finalized {
						return errors.New("unclaimed evidence")
					}
					close(entered)
					<-release
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if mode == "store-error" {
						return errors.New("synthetic uncertain storage failure")
					}
					sinks.Add(1)
					return nil
				}), nil
			}); err != nil {
				t.Fatal(err)
			}
			done := startVerifiedFixtureIntent(t, f)
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("completed sealed execution did not enter its sink")
			}
			f.e.workMu.Lock()
			claimed := f.e.work[f.intent.ID] != nil && f.e.work[f.intent.ID].completionClaimed
			f.e.workMu.Unlock()
			if !claimed {
				t.Fatal("storage started before the completion decision")
			}
			if err := f.e.KillWork(f.intent.ID); !errors.Is(err, errWorkControlConflict) {
				t.Fatalf("late planner kill accepted a claimed execution: %v", err)
			}
			if _, err := f.s.applyTaskControl(f.task, "pause"); err != nil {
				t.Fatal(err)
			}
			if f.e.inflightCount(f.task.ID) != 1 {
				t.Fatal("pause lost the observable finalization/drain reservation")
			}
			if err := f.e.ControlWork(context.Background(), f.intent.ID, "cancel"); !errors.Is(err, errWorkControlConflict) {
				t.Fatalf("late work cancellation changed a claimed disposition: %v", err)
			}
			unblock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("claimed execution did not complete its bounded finalization")
			}
			node, err := f.task.Store.GetNode(f.intent.ID)
			if err != nil || node == nil || !f.task.lifecycleSnapshot().Paused {
				t.Fatalf("late task pause was lost: %+v %v", node, err)
			}
			if mode == "commit" && (node.State != "done" || sinks.Load() != 1) {
				t.Fatalf("successful committed execution was relabelled stopped: state=%s sinks=%d", node.State, sinks.Load())
			}
			if mode == "store-error" && (node.State == "done" || sinks.Load() != 0) {
				t.Fatalf("uncertain storage was promoted to completed: state=%s sinks=%d", node.State, sinks.Load())
			}
		})
	}
}
