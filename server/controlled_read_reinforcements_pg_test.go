package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

// Authorities/model/identity/KG are synthetic. The actual runtime, loopback
// transmitter, journal/CPD, protected descriptor and PostgreSQL writers run.
type reinforcementScopeKey struct{}
type reinforcementScopeProbe struct {
	t                                                       *testing.T
	collector                                               *controlledFindingCollector
	mode                                                    string
	active                                                  atomic.Bool
	acquires, releases, sources, descriptors, issues, saves atomic.Int64
	cancel                                                  context.CancelFunc
}
type reinforcementLease struct {
	p   *reinforcementScopeProbe
	ctx context.Context
}

func (p *reinforcementScopeProbe) unlocked() {
	p.t.Helper()
	if p.collector == nil || !p.collector.mu.TryLock() {
		p.t.Error("external authority observed collector mutex held")
		return
	}
	p.collector.mu.Unlock()
}
func (p *reinforcementScopeProbe) checkContext(ctx context.Context) {
	p.t.Helper()
	p.unlocked()
	if !p.active.Load() || ctx.Value(reinforcementScopeKey{}) != p {
		p.t.Error("stage escaped the acquired authority context")
	}
}
func (p *reinforcementScopeProbe) Acquire(ctx context.Context, r ControlledReadVerificationRequest, e evidencebind.Context) (ControlledReadFinalizationScope, error) {
	p.unlocked()
	if r.Scope != e.Scope || r.IntentID != e.IntentID || p.active.Swap(true) {
		return nil, errors.New("synthetic invalid acquisition")
	}
	p.acquires.Add(1)
	leaseCtx, cancel := context.WithCancel(context.WithValue(ctx, reinforcementScopeKey{}, p))
	p.cancel = cancel
	return &reinforcementLease{p, leaseCtx}, nil
}
func (l *reinforcementLease) Context() context.Context { return l.ctx }
func (l *reinforcementLease) Check(ctx context.Context) error {
	p := l.p
	p.unlocked()
	p.checkContext(ctx)
	if !p.collector.mu.TryLock() {
		return errors.New("synthetic collector mutex unavailable")
	}
	prepared := p.collector.prepared
	p.collector.mu.Unlock()
	if prepared && p.mode == "pre-save-revoked" {
		p.cancel()
		return errors.New("synthetic pre-save revocation")
	}
	return ctx.Err()
}
func (l *reinforcementLease) Release() {
	p := l.p
	p.unlocked()
	if !p.active.Swap(false) {
		p.t.Error("duplicate authority release")
	}
	p.releases.Add(1)
	p.cancel()
}

type reinforcementRevisionScope struct {
	inner *revisionWireScope
	probe *reinforcementScopeProbe
}
type reinforcementRevisionLease struct {
	evidencerevision.Lease
	ctx   context.Context
	probe *reinforcementScopeProbe
}

func (p *reinforcementRevisionScope) Acquire(ctx context.Context, a evidencerevision.Access) (evidencerevision.Lease, error) {
	p.probe.checkContext(ctx)
	p.probe.issues.Add(1)
	l, err := p.inner.Acquire(ctx, a)
	if err != nil {
		return nil, err
	}
	return &reinforcementRevisionLease{l, ctx, p.probe}, nil
}
func (l *reinforcementRevisionLease) Context() context.Context { return l.ctx }
func (l *reinforcementRevisionLease) Release()                 { l.probe.checkContext(l.ctx); l.Lease.Release() }

type reinforcementDescriptorRoot struct {
	inner *revisionWireRoot
	probe *reinforcementScopeProbe
}
type reinforcementDescriptorLease struct {
	evidencerevision.DescriptorRootScope
	ctx   context.Context
	probe *reinforcementScopeProbe
}

func (p *reinforcementDescriptorRoot) AcquireDescriptorRoot(ctx context.Context, a evidencerevision.DescriptorFileAccess) (evidencerevision.DescriptorRootScope, error) {
	p.probe.checkContext(ctx)
	p.probe.descriptors.Add(1)
	l, err := p.inner.AcquireDescriptorRoot(ctx, a)
	if err != nil {
		return nil, err
	}
	return &reinforcementDescriptorLease{l, ctx, p.probe}, nil
}
func (l *reinforcementDescriptorLease) Context() context.Context { return l.ctx }
func (l *reinforcementDescriptorLease) Release() {
	l.probe.checkContext(l.ctx)
	l.DescriptorRootScope.Release()
}

func TestControlledReadReinforcementsTogetherPG(t *testing.T) {
	for _, mode := range []string{"success", "pre-save-revoked", "commit-ack-lost", "commit-success-revoked"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int64
			target := controlledReadTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"ok","data":{"note":%q}}`, verificationTestMarker)
			}))
			f := newControlledVerificationDBFixture(t, target.URL)
			h := installDurableTestPublisher(t, f, "success")
			_, baseScope, baseRoot := revisionWireServices(t, f, "success")
			probe := &reinforcementScopeProbe{t: t, mode: mode}
			services, err := newControlledRevisionServices(f.m, &reinforcementRevisionScope{baseScope, probe}, &reinforcementDescriptorRoot{baseRoot, probe})
			if err != nil {
				t.Fatal(err)
			}
			cpd, err := auditcheckpoint.New(f.m.pg.DB, h, auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 16, Timeout: 3 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			reader, err := f.s.NewControlledReadAuditSource(ControlledReadAuditSourceOptions{
				LatestCheckpoint: func(ctx context.Context, scope transmit.Scope) (ControlledReadAuditAnchor, error) {
					probe.checkContext(ctx)
					h.mu.Lock()
					binding := h.last
					h.mu.Unlock()
					head, err := cpd.Head(ctx, auditcheckpoint.Query{Epoch: h.epoch, Binding: binding})
					if err != nil || head.Phase != auditcheckpoint.Sealed || head.Checkpoint.Scope != scope {
						return ControlledReadAuditAnchor{}, errors.New("synthetic final head unavailable")
					}
					return ControlledReadAuditAnchor{Checkpoint: head.Checkpoint, Ref: fmt.Sprintf("synthetic-cpd:%s:%d", head.Checkpoint.JournalID, head.Revision)}, nil
				},
				ResolveKey: func(ctx context.Context, _ ControlledReadAuditKeyRequest) ([]byte, error) {
					probe.checkContext(ctx)
					return h.key, nil
				},
				MaxJournalBytes: 1 << 20, MaxEvents: 16, Timeout: 3 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			policy := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone, HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond), ValidUntil: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)}
			var actual ControlledReadVerificationRequest
			resolve := RequireControlledReadFinalizationScope(func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
				actual = r
				view, err := r.ConfigView(ctx)
				if err != nil {
					return ControlledReadFindingPlan{}, err
				}
				if view.Scope != r.Scope || len(view.Sessions) != 2 || len(view.Resources) != 1 {
					return ControlledReadFindingPlan{}, errors.New("synthetic runtime view mismatch")
				}
				expected := evidencebind.Context{Scope: view.Scope, IntentID: r.IntentID, ResourceRef: view.Resources[0].Ref, ReadApprovalRef: view.Resources[0].ApprovalRef, KGPolicyVersion: "synthetic-kg-policy"}
				for _, s := range view.Sessions {
					if s.Account.Label == "owner" {
						expected.Owner = s.Account
					}
					if s.Account.Label == r.AccountRef {
						expected.Evaluated = s.Account
					}
				}
				// A returned metadata copy cannot change Bind's frozen runtime source.
				view.Scope.RunRef = "untrusted-copy"
				view.Resources[0].Accounts[0] = "untrusted-copy"
				metadata := func(ctx context.Context, e evidencebind.Context) (e3record.Source, error) {
					probe.checkContext(ctx)
					probe.sources.Add(1)
					var version uint64
					if err := f.m.pg.QueryRowContext(ctx, `SELECT version FROM task_control_modes WHERE task_id=$1`, r.TaskID).Scan(&version); err != nil {
						return e3record.Source{}, err
					}
					return e3record.Source{Expected: e, ExplorationID: f.task.ExpID, ModeVersion: version, KGSnapshotRef: "synthetic-kg", KGSnapshotDigest: sha256.Sum256([]byte("synthetic KG")), GenerationRef: "synthetic-harness", InstanceRef: "synthetic-instance", ResetGeneration: "synthetic-reset", ModelTag: "synthetic-model", Policy: policy, CanaryFields: []string{"note"}, Severity: "medium"}, nil
				}
				source, err := reader.SourceReader(metadata)
				if err != nil {
					return ControlledReadFindingPlan{}, err
				}
				plan, err := r.BindRealInputs(ctx, expected, &verificationTestKG{policy: policy}, source)
				plan.FinalizationScope = probe
				return plan, err
			})
			markers, err := f.s.ControlledTaskModeMarkers()
			if err != nil {
				t.Fatal(err)
			}
			// Use the actual collector and revision services, with a test-only save
			// observer to assert the lease through the real SQL return and lose ACK.
			err = f.s.ConfigureControlledReadVerification(func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
				plan, err := resolve(ctx, r)
				if err != nil {
					return ControlledReadVerificationPlan{}, err
				}
				c, err := newControlledFindingCollector(f.m.pg, markers, r, plan)
				if err != nil {
					return ControlledReadVerificationPlan{}, err
				}
				c.revisions, c.revisionOperation = services, controlledRevisionOperation(r)
				probe.collector = c
				save := c.save
				c.save = func(ctx context.Context, p e3record.Prepared, m db.TaskModeMarkerReader) (db.ControlledFindingResult, error) {
					probe.checkContext(ctx)
					probe.saves.Add(1)
					result, err := save(ctx, p, m)
					probe.checkContext(ctx)
					if err == nil && (mode == "commit-ack-lost" || mode == "commit-success-revoked") {
						probe.cancel()
					}
					if err == nil && mode == "commit-ack-lost" {
						return db.ControlledFindingResult{}, errors.New("synthetic ACK lost after real commit")
					}
					return result, err
				}
				return c.verificationPlan(), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			diagnostics := &durableStageLog{}
			previous := log.Writer()
			log.SetOutput(diagnostics)
			defer log.SetOutput(previous)
			select {
			case <-startVerifiedFixtureIntent(t, f):
			case <-time.After(15 * time.Second):
				t.Fatal("combined runtime did not settle")
			}
			if hits.Load() != 2 || probe.acquires.Load() != 1 || probe.releases.Load() != 1 || probe.active.Load() || probe.sources.Load() != 2 || probe.descriptors.Load() != 1 || probe.issues.Load() != 1 {
				t.Fatalf("stage counts http=%d acquire=%d release=%d source=%d descriptor=%d issue=%d", hits.Load(), probe.acquires.Load(), probe.releases.Load(), probe.sources.Load(), probe.descriptors.Load(), probe.issues.Load())
			}
			if _, err := actual.ConfigView(t.Context()); err == nil {
				t.Fatal("closed runtime handle started a fresh query")
			}
			var findings, revisions int
			if err := f.m.pg.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, actual.TaskID).Scan(&findings); err != nil {
				t.Fatal(err)
			}
			if err := f.m.pg.QueryRow(`SELECT count(*) FROM evidence_revision_operations WHERE operation_ref=$1`, controlledRevisionOperation(actual)).Scan(&revisions); err != nil {
				t.Fatal(err)
			}
			node, err := f.task.Store.GetNode(f.intent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if revisions != 1 {
				t.Fatal("actual revision operation missing")
			}
			if mode == "pre-save-revoked" {
				if findings != 0 || node.State == "done" || probe.saves.Load() != 0 {
					t.Fatal("pre-save authority loss reached save/success")
				}
				return
			}
			if findings != 1 || probe.saves.Load() != 1 {
				t.Fatal("real SQL commit missing or retried")
			}
			if mode == "commit-ack-lost" {
				if node.State == "done" || !strings.Contains(diagnostics.String(), "storage_outcome_unknown operation_key=") || strings.Contains(diagnostics.String(), "revision_failed") {
					t.Fatal("real commit ACK loss reclassified")
				}
				return
			}
			if node.State != "done" {
				t.Fatal("known commit success changed into failure after revocation")
			}
			var revision, manifestRevision uint64
			var binding []byte
			var value string
			if err := f.m.pg.QueryRow(`SELECT i.revision,m.evidence_revision,v.binding_canonical,o.oracle_value FROM evidence_revision_initials i JOIN e3_evidence_manifests m ON m.execution_ref=i.execution_ref JOIN e3_verdict_issuances v ON v.manifest_id=m.id JOIN e3_oracle_verdicts o ON o.id=v.verdict_id WHERE i.execution_ref=$1`, actual.Scope.RunRef).Scan(&revision, &manifestRevision, &binding, &value); err != nil {
				t.Fatal(err)
			}
			var source e3record.Source
			if json.Unmarshal(binding, &source) != nil || revision == 0 || revision != manifestRevision || source.EvidenceRevision != revision || source.EvalRunID != "" || value != string(oracle.Confirmed) {
				t.Fatal("actual revision/verdict/non-eval binding lost")
			}
		})
	}
}
