package server

import (
	"bytes"
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

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/kgfixture"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

// Exercise strict bootstrap, registered KG, Worker, durable audit, revision and
// finding SQL together. Host authority, identity, keys, model and target are
// synthetic; this establishes composition, never production authorization.
func TestControlledReadStrictBootstrapScopeOwnedPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated finalization PostgreSQL not provided")
	}
	if os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_OWNED") != "1" {
		t.Fatal("owned disposable PostgreSQL guard required")
	}
	probe, err := db.Open(dsn)
	if err != nil {
		t.Fatal("configured dedicated PostgreSQL unavailable")
	}
	if err := probe.Close(); err != nil {
		t.Fatal("dedicated PostgreSQL probe close failed")
	}
	// Never fall back to the user's general/config-file database.
	t.Setenv("ARTEX_PG_DSN", dsn)
	for _, mode := range []string{"success", "missing-provider", "typed-nil-provider", "acquire-error", "revoke-source-b"} {
		t.Run(mode, func(t *testing.T) {
			t.Cleanup(func() {
				if t.Skipped() {
					t.Error("configured dedicated integration fixture unexpectedly skipped")
				}
			})
			var hits, reads, acquired, released atomic.Int64
			var held atomic.Bool
			target := controlledReadTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := hits.Add(1)
				want := runtimeSecret
				if n == 2 {
					want = "synthetic-peer-session"
				}
				if r.Method != http.MethodGet || r.Header.Get("X-Session") != want {
					t.Error("registered method or session changed")
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := fmt.Fprintf(w, `{"status":"ok","data":{"note":%q}}`, verificationTestMarker); err != nil {
					t.Error("synthetic response write failed")
				}
			}))
			f := newControlledVerificationDBFixture(t, target.URL)
			revisionWireRequestTimeout(t, f)
			h := installDurableTestPublisher(t, f, "success")
			_, revisionScope, revisionRoot := revisionWireServices(t, f, "strict-bootstrap")
			registry := map[string]any{
				"format": kgfixture.FormatV1, "snapshot_id": "bootstrap-synthetic-kg",
				"run_id": "synthetic-preparation", "reset_generation": "reset-1",
				"generation_ref": "synthetic-generation", "instance_ref": "synthetic-instance",
				"principals": []any{map[string]any{"label": "owner"}, map[string]any{"label": "peer"}},
				"resources": []any{map[string]any{
					"resource_ref": "doc:one", "owner_ref": "owner",
					"policies": []any{map[string]any{"evaluated_ref": "peer", "expected_access": "deny", "sharing": "resolved_none", "delegation": "resolved_none",
						"valid_from": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), "valid_until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}},
					"canaries": []any{map[string]any{"field": "note", "value": verificationTestMarker, "trivial": false}},
				}},
			}
			if err := os.WriteFile(filepath.Join(f.store.snapshotDir, "bootstrap-kg.json"), kgBindingJSON(t, registry), 0o600); err != nil {
				t.Fatal(err)
			}
			checkpoints, err := auditcheckpoint.New(f.m.pg.DB, h, auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 16, Timeout: 3 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var reserved uint64
			if mode == "success" {
				if err := f.m.pg.QueryRow(`SELECT nextval(pg_get_serial_sequence('evidence_revision_initials','revision'))`).Scan(&reserved); err != nil {
					t.Fatal("fixture revision sequence reservation failed")
				}
			}
			type leaseKey struct{}
			life, stop := context.WithCancel(t.Context())
			leaseContext, revoke := context.WithCancel(context.WithValue(life, leaseKey{}, true))
			defer revoke()
			revisionScope.ctx, revisionRoot.ctx = leaseContext, leaseContext
			assertHeld := func(ctx context.Context, stage string) {
				if !held.Load() || released.Load() != 0 || ctx.Value(leaseKey{}) != true {
					t.Errorf("finalization lease absent at %s", stage)
				}
			}
			var request ControlledReadVerificationRequest
			var expected evidencebind.Context
			var frozen *kgfixture.Snapshot
			var metadata e3record.Source
			release := func() {
				if !held.Swap(false) || released.Add(1) != 1 {
					t.Error("finalization lease released more than once or before acquisition")
				}
				if frozen == nil || frozen.Err() != nil {
					t.Error("registered KG invalidated before finalization lease returned")
				}
				var findings int
				if err := f.m.pg.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, mustSyntheticTaskID(t, f.task.ID)).Scan(&findings); err != nil {
					t.Error("could not observe SQL outcome during lease release")
				}
				want := 0
				if mode == "success" {
					want = 1
				}
				if findings != want {
					t.Errorf("lease release observed findings=%d want=%d", findings, want)
				}
			}
			provider := &finalizationTestProvider{acquire: func(ctx context.Context, got ControlledReadVerificationRequest, want evidencebind.Context) (ControlledReadFinalizationScope, error) {
				acquired.Add(1)
				if got != request || want != expected || ctx.Err() != nil {
					t.Error("finalization acquisition lost exact request/expected")
				}
				held.Store(true)
				scope := &revisionFinalizationOuterLease{scope: &finalizationTestScope{ctx: leaseContext, check: func(ctx context.Context) error { assertHeld(ctx, "scope-check"); return ctx.Err() }}, release: release}
				if mode == "acquire-error" {
					return scope, errors.New("synthetic acquisition refusal")
				}
				return scope, nil
			}}
			resolve := func(ctx context.Context, got ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
				request = got
				var err error
				frozen, err = got.FixtureSnapshot(ctx)
				if err != nil {
					return ControlledReadFindingPlan{}, err
				}
				owner := idor.AccountRef{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified}
				peer := idor.AccountRef{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified}
				expected = evidencebind.Context{Scope: got.Scope, IntentID: got.IntentID, ResourceRef: got.ResourceRef, ReadApprovalRef: "read-approval", KGPolicyVersion: frozen.PolicyVersion(), Owner: owner, Evaluated: peer}
				policy, ok := frozen.ResolvePolicy(got.ResourceRef, owner, peer)
				if !ok {
					return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
				}
				metadata = e3record.Source{Expected: expected, ExplorationID: f.task.ExpID, KGSnapshotRef: frozen.Ref(), KGSnapshotDigest: frozen.Digest(), GenerationRef: frozen.GenerationRef(), InstanceRef: frozen.InstanceRef(), ResetGeneration: frozen.ResetGeneration(), ModelTag: "synthetic-bootstrap-model", Policy: policy, CanaryFields: []string{"note"}, Severity: "medium"}
				reader, err := f.s.NewControlledReadAuditSource(ControlledReadAuditSourceOptions{
					LatestCheckpoint: func(ctx context.Context, scope transmit.Scope) (ControlledReadAuditAnchor, error) {
						assertHeld(ctx, "sealed-head")
						h.mu.Lock()
						binding := h.last
						h.mu.Unlock()
						head, err := checkpoints.Head(ctx, auditcheckpoint.Query{Epoch: h.epoch, Binding: binding})
						if err != nil || head.Phase != auditcheckpoint.Sealed || head.Checkpoint.Scope != scope {
							return ControlledReadAuditAnchor{}, ErrControlledFindingUnavailable
						}
						return ControlledReadAuditAnchor{Checkpoint: head.Checkpoint, Ref: fmt.Sprintf("synthetic-cpd:%s:%d", head.Checkpoint.JournalID, head.Revision)}, nil
					},
					ResolveKey: func(ctx context.Context, _ ControlledReadAuditKeyRequest) ([]byte, error) {
						assertHeld(ctx, "audit-key")
						return h.key, nil
					},
					MaxJournalBytes: 1 << 20, MaxEvents: 16, Timeout: 3 * time.Second,
				})
				if err != nil {
					return ControlledReadFindingPlan{}, err
				}
				source, err := reader.SourceReader(func(ctx context.Context, got evidencebind.Context) (e3record.Source, error) {
					n := reads.Add(1)
					assertHeld(ctx, "source")
					if got != expected || frozen.Err() != nil {
						return e3record.Source{}, ErrControlledFindingUnavailable
					}
					if _, err := request.FixtureSnapshot(ctx); err == nil {
						t.Error("closed Config reopened during finalization")
					}
					value := metadata
					if err := f.m.pg.QueryRowContext(ctx, `SELECT version FROM task_control_modes WHERE task_id=$1`, request.TaskID).Scan(&value.ModeVersion); err != nil {
						return e3record.Source{}, err
					}
					if mode == "revoke-source-b" && n == 2 {
						revoke()
					}
					return value, nil
				})
				if err != nil {
					return ControlledReadFindingPlan{}, err
				}
				plan, err := got.BindRealInputs(ctx, expected, frozen, source)
				if mode == "typed-nil-provider" {
					plan.FinalizationScope = (*finalizationTestProvider)(nil)
				} else if mode != "missing-provider" {
					plan.FinalizationScope = provider
				}
				return plan, err
			}
			t.Setenv("ARTEX_E2_SNAPSHOT_DIR", f.store.snapshotDir)
			t.Setenv("ARTEX_E2_STATE_DIR", f.store.stateDir)
			t.Setenv("ARTEX_LLM_API_KEY", "")
			t.Setenv(notifyBackgroundDisabledEnv, "1")
			preserveRevisionWireAuth(t, f.m.pg)
			if err := f.m.pg.SetSetting(authPassKey, "synthetic-disabled-login"); err != nil {
				t.Fatal(err)
			}
			keyDir, skillDir, dataDir := t.TempDir(), t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), bytes.Repeat([]byte("s"), 32), 0o600); err != nil {
				t.Fatal(err)
			}
			var strict *Server
			// Join restored/background work before auth, private paths, roots and manager
			// cleanup. Keep the existing bounded integration timeout and cleanup helpers.
			t.Cleanup(func() { stop(); drainTestServer(t, strict) })
			strict, err = NewWithRevisionedDurableControlledReadHost(life, f.m, skillDir, dataDir, keyDir, ControlledReadRevisionHostOptions{
				Durable: ControlledReadDurableHostOptions{
					Host:      ControlledReadHostOptions{Mode: f.e.controlledReads.mode.host, Findings: RequireControlledReadFinalizationScope(resolve), FixtureRegistries: map[string]string{"approved": "bootstrap-kg.json"}},
					Publisher: f.e.controlledReads.publisher.factory,
				},
				Scope:          &revisionFinalizationProvider{inner: revisionScope, stage: assertHeld},
				DescriptorRoot: &revisionFinalizationRoot{inner: revisionRoot, stage: assertHeld},
			})
			if err != nil {
				t.Fatal("strict synthetic constructor failed")
			}
			f.s, f.e, f.store = strict, strict.engine, strict.engine.controlledReads.store
			f.e.started.Store(f.task.ID, true)
			f.e.Pause(f.task.ID, errors.New("synthetic bootstrap pause"))
			started := time.Now()
			select {
			case <-startVerifiedFixtureIntent(t, f):
			case <-time.After(15 * time.Second):
				t.Fatalf("strict Worker did not settle hits=%d source=%d acquire=%d release=%d elapsed=%s", hits.Load(), reads.Load(), acquired.Load(), released.Load(), time.Since(started))
			}
			var findings, issued int
			var issuedRevision uint64
			taskID := mustSyntheticTaskID(t, f.task.ID)
			if err := f.m.pg.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, taskID).Scan(&findings); err != nil {
				t.Fatal(err)
			}
			if err := f.m.pg.QueryRow(`SELECT count(*),COALESCE(max(revision),0) FROM evidence_revision_operations WHERE operation_ref=$1`, controlledRevisionOperation(request)).Scan(&issued, &issuedRevision); err != nil {
				t.Fatal(err)
			}
			node, err := f.task.Store.GetNode(f.intent.ID)
			if err != nil || node == nil {
				t.Fatal("actual intent unavailable")
			}
			wantHits, wantReads, wantAcquire, wantRelease, wantIssued, wantFindings := int64(2), int64(2), int64(1), int64(1), 1, 0
			switch mode {
			case "success":
				wantFindings = 1
			case "missing-provider", "typed-nil-provider":
				wantHits, wantReads, wantAcquire, wantRelease, wantIssued = 0, 0, 0, 0, 0
			case "acquire-error":
				wantReads, wantIssued = 0, 0
			}
			if hits.Load() != wantHits || reads.Load() != wantReads || acquired.Load() != wantAcquire || released.Load() != wantRelease || issued != wantIssued || findings != wantFindings || (node.State == "done") != (mode == "success") || held.Load() {
				t.Fatalf("scope boundary mode=%s hits=%d/%d source=%d/%d acquire=%d/%d release=%d/%d revision=%d/%d finding=%d/%d done=%v elapsed=%s", mode, hits.Load(), wantHits, reads.Load(), wantReads, acquired.Load(), wantAcquire, released.Load(), wantRelease, issued, wantIssued, findings, wantFindings, node.State == "done", time.Since(started))
			}
			if frozen == nil || frozen.Err() == nil {
				t.Fatal("registered KG remained live after execution close")
			}
			if mode == "success" {
				var binding []byte
				var revision uint64
				if err := f.m.pg.QueryRow(`SELECT v.binding_canonical,m.evidence_revision FROM e3_verdict_issuances v JOIN e3_evidence_manifests m ON m.id=v.manifest_id WHERE v.task_id=$1`, taskID).Scan(&binding, &revision); err != nil {
					t.Fatal(err)
				}
				var stored e3record.Source
				if json.Unmarshal(binding, &stored) != nil || stored.Expected != expected || stored.KGSnapshotRef != metadata.KGSnapshotRef || stored.KGSnapshotDigest != metadata.KGSnapshotDigest || stored.GenerationRef != metadata.GenerationRef || stored.InstanceRef != metadata.InstanceRef || stored.ResetGeneration != metadata.ResetGeneration || stored.EvidenceRevision != revision || revision != issuedRevision || issuedRevision <= reserved {
					t.Fatalf("stored issuance lost execution/KG/revision binding source=%d manifest=%d SQL=%d reserved=%d", stored.EvidenceRevision, revision, issuedRevision, reserved)
				}
			}
		})
	}
}
