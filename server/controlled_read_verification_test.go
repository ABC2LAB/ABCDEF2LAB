package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

const verificationTestMarker = "synthetic-owner-nontrivial-canary-612"

type verificationTestKG struct{ policy oracle.PolicyAssertion }

func (k *verificationTestKG) ResolvePolicy(string, idor.AccountRef, idor.AccountRef) (oracle.PolicyAssertion, bool) {
	return k.policy, true
}
func (*verificationTestKG) Canaries(string, idor.AccountRef) ([]idor.Marker, bool) {
	return []idor.Marker{{Field: "note", Value: verificationTestMarker}}, true
}

func TestControlledVerificationFinalizesOnlyCompletedSealedExecution(t *testing.T) {
	for _, mode := range []string{"completed", "cached-repeat", "not-completed", "cancelled", "shutdown", "checkpoint-failure", "callback-error", "collect-error", "missing-source", "nil-kg", "wrong-account"} {
		t.Run(mode, func(t *testing.T) {
			var hits, resolved, collected, attempted, stored atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, "{\"status\":\"ok\",\"data\":{\"note\":\""+verificationTestMarker+"\",\"private\":\""+runtimeSecret+"\"}}")
			}))
			defer target.Close()
			e, task, intent, store := runtimeReadFixture(t, target.URL)
			key := controlledWorkKey{task.ID, intent.ID}
			spec := e.controlledReads.pending[key]
			spec.HTTP.MaxRequests = 2
			spec.HTTP.Sessions = append(spec.HTTP.Sessions, transmit.Session{
				Account:   idor.AccountRef{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified},
				ExpiresAt: time.Now().Add(time.Hour),
			})
			spec.HTTP.Resources[0].Accounts = []string{"owner", "peer"}
			spec.HTTP.Resources[0].View = &transmit.JSONViewRule{
				StatusPath: []string{"status"}, NormalValue: "ok", DataPath: []string{"data"},
				Markers: []idor.Marker{{Field: "note", Value: verificationTestMarker}},
			}
			e.controlledReads.pending[key] = spec
			auditKey := append([]byte(nil), spec.Key...)
			lifecycle, stopServer := context.WithCancel(context.Background())
			defer stopServer()
			// This is an isolated synthetic provider fixture. Production Go-host
			// construction additionally requires the actual DB mode/root bootstrap.
			e.controlledReads.verification = &controlledReadVerificationConfig{
				lifecycle: lifecycle,
				resolve: func(ctx context.Context, request ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
					resolved.Add(1)
					if request.TaskID != 17 || request.IntentID != 22 || request.AccountRef != "peer" || request.Scope.RunRef == "" {
						t.Fatal("host request lost actual execution binding")
					}
					if mode == "missing-source" {
						return ControlledReadVerificationPlan{}, errors.New("PRIVATE-SOURCE-ERROR")
					}
					plan := ControlledReadVerificationPlan{
						Candidate: idor.Candidate{Owner: spec.HTTP.Sessions[0].Account, Evaluated: spec.HTTP.Sessions[1].Account, ResourceRef: "doc:one"},
						KG: &verificationTestKG{policy: oracle.PolicyAssertion{
							ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
							HasRegisteredSource: true, HintMatchesCanonical: true,
							ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour),
						}},
						Collect: func(ctx context.Context, evidence ControlledReadVerificationEvidence) error {
							collected.Add(1)
							if ctx.Err() != nil || evidence.Completed || evidence.Finalized ||
								evidence.Verification.Owner.Receipt.Attempt != 1 || evidence.Verification.Evaluated.Receipt.Attempt != 2 {
								t.Fatal("collection did not bind the actual incomplete execution")
							}
							if mode == "collect-error" {
								return errors.New("PRIVATE-COLLECTION-ERROR")
							}
							return nil
						},
						Finalize: func(ctx context.Context, evidence ControlledReadVerificationEvidence) error {
							attempted.Add(1)
							if ctx.Err() != nil {
								t.Fatal("finalizer got cancelled cleanup/request context")
							}
							if _, ok := ctx.Deadline(); !ok {
								t.Fatal("finalizer context is unbounded")
							}
							if !e.controlledReads.mu.TryLock() {
								t.Fatal("finalizer ran under runtime registry lock")
							}
							e.controlledReads.mu.Unlock()
							if !evidence.Completed || !evidence.Finalized || evidence.Verification.Result.Verdict.Value != oracle.Confirmed ||
								evidence.FinalCheckpoint.Scope != request.Scope {
								t.Fatal("uncompleted or foreign evidence reached finalizer")
							}
							journal, err := os.Open(filepath.Join(store.journalDirectory(), request.Scope.RunRef+".jsonl"))
							if err != nil {
								t.Fatal(err)
							}
							defer journal.Close()
							summary, err := auditlog.Verify(ctx, journal, auditKey, evidence.FinalCheckpoint)
							if err != nil || summary.Intents != 2 || summary.Results != 2 {
								t.Fatalf("finalizer ran before complete sealed audit: %+v %v", summary, err)
							}
							if mode == "callback-error" {
								return errors.New("PRIVATE-STORE-ERROR")
							}
							stored.Add(1)
							return nil
						},
					}
					if mode == "nil-kg" {
						plan.KG = (*verificationTestKG)(nil)
					}
					if mode == "wrong-account" {
						plan.Candidate.Evaluated.Status = oracle.SubjectUnknown
					}
					return plan, nil
				},
			}
			runCtx, stopWork := context.WithCancel(context.Background())
			defer stopWork()
			// The direct Worker fixture has no Engine.runIntent. Its owned test
			// host supplies the same pre-cancel completion condition explicitly.
			runCtx = context.WithValue(runCtx, controlledReadCompletionClaimKey{}, func() bool { return runCtx.Err() == nil })
			ctx, finish, err := e.prepareControlledRead(runCtx, task, intent)
			if err != nil {
				t.Fatal(err)
			}
			p := &runtimeReadProvider{t: t, name: agent.ControlledHTTPToolName, input: "{\"account_ref\":\"peer\",\"resource_ref\":\"doc:one\"}"}
			if _, _, err := runtimeWorker(t, p).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if mode == "cached-repeat" {
				if _, _, err := runtimeWorker(t, p).Execute(ctx, "worker", 17, nil, nil, intent, nil, nil, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if stored.Load() != 0 || attempted.Load() != 0 {
				t.Fatal("tool staged result was saved before execution disposition")
			}
			if mode == "cancelled" {
				stopWork()
			}
			if mode == "shutdown" {
				stopServer()
			}
			if mode == "checkpoint-failure" {
				paths, _ := filepath.Glob(filepath.Join(store.journalDirectory(), "*.checkpoint"))
				if len(paths) != 1 || os.Remove(paths[0]) != nil || os.Mkdir(paths[0], 0o700) != nil {
					t.Fatal("could not inject final checkpoint failure")
				}
			}
			err = finish(mode != "not-completed")
			success := mode == "completed" || mode == "cached-repeat"
			if success && (err != nil || stored.Load() != 1) {
				t.Fatalf("completed verification not saved once: %v stored=%d", err, stored.Load())
			}
			if !success && (err == nil || stored.Load() != 0) {
				t.Fatalf("unfinalized verification saved: %v stored=%d", err, stored.Load())
			}
			if mode != "callback-error" && !success && attempted.Load() != 0 {
				t.Fatal("stopped/failed execution called its storage finalizer")
			}
			wantHits := int64(2)
			if mode == "missing-source" || mode == "nil-kg" || mode == "wrong-account" {
				wantHits = 0
			}
			wantCollections := int64(1)
			if wantHits == 0 {
				wantCollections = 0
			}
			if collected.Load() != wantCollections {
				t.Fatalf("observation collection count: %d want %d", collected.Load(), wantCollections)
			}
			if hits.Load() != wantHits || resolved.Load() != 1 {
				t.Fatalf("unexpected requests/provider calls: %d/%d", hits.Load(), resolved.Load())
			}
			_ = finish(true)
			if success && stored.Load() != 1 {
				t.Fatal("cleanup replay saved the same execution twice")
			}
		})
	}
}
