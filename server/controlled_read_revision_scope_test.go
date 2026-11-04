package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type revisionFinalizationContextKey struct{}

// These provider wrappers observe real descriptor/SQL calls under a synthetic
// outer host lease. They supply no production authority or authentication.
type revisionFinalizationProvider struct {
	inner *revisionWireScope
	stage func(context.Context, string)
}

func (p *revisionFinalizationProvider) Acquire(ctx context.Context, a evidencerevision.Access) (evidencerevision.Lease, error) {
	p.stage(ctx, "issue-acquire")
	lease, err := p.inner.Acquire(ctx, a)
	if lease == nil {
		return nil, err
	}
	return &revisionFinalizationLease{Lease: lease, ctx: ctx, stage: p.stage}, err
}

type revisionFinalizationLease struct {
	evidencerevision.Lease
	ctx   context.Context
	stage func(context.Context, string)
}

func (l *revisionFinalizationLease) Release() { l.stage(l.ctx, "issue-release"); l.Lease.Release() }

type revisionFinalizationRoot struct {
	inner *revisionWireRoot
	stage func(context.Context, string)
}

func (p *revisionFinalizationRoot) AcquireDescriptorRoot(ctx context.Context, a evidencerevision.DescriptorFileAccess) (evidencerevision.DescriptorRootScope, error) {
	p.stage(ctx, "descriptor-acquire")
	lease, err := p.inner.AcquireDescriptorRoot(ctx, a)
	if lease == nil {
		return nil, err
	}
	return &revisionFinalizationRootLease{DescriptorRootScope: lease, ctx: ctx, stage: p.stage}, err
}

type revisionFinalizationRootLease struct {
	evidencerevision.DescriptorRootScope
	ctx   context.Context
	stage func(context.Context, string)
}

func (l *revisionFinalizationRootLease) Release() {
	l.stage(l.ctx, "descriptor-release")
	l.DescriptorRootScope.Release()
}

func TestFinalizationRevisionOwnedPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_DSN")
	if dsn == "" {
		t.Skip("owned finalization PostgreSQL not provided")
	}
	if os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_OWNED") != "1" {
		t.Fatal("owned disposable PostgreSQL guard required")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal("owned PostgreSQL initialization failed")
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error("owned PostgreSQL close failed")
		}
	})
	if err := evidencerevision.Migrate(t.Context(), d.DB); err != nil {
		t.Fatal("explicit revision migration failed")
	}
	for _, mode := range []string{"success", "source-a-error", "source-a-cancel", "descriptor-denied", "descriptor-cancel", "issue-denied", "issue-cancel", "source-b-error", "source-b-cancel", "issue-ack-lost", "prepare-before-save-revoke", "save-input-rejected", "save-cancel", "commit-success-cancel", "finding-ack-lost-cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			caller, cancelCaller := context.WithCancel(ctx)
			defer cancelCaller()
			leaseCtx, revoke := context.WithCancel(context.WithValue(caller, revisionFinalizationContextKey{}, "synthetic-host-lease"))
			defer revoke()
			task, err := d.CreateTask("synthetic revision scope", "fixture only", nil, 0, 0)
			if err != nil {
				t.Fatal("task fixture")
			}
			intent, err := d.Exploration(task.ExplorationID).AddIntent(map[string]any{"description": "scope fixture"}, 5, nil, "human")
			if err != nil {
				t.Fatal("intent fixture")
			}
			markers := func(_ context.Context, id int64) (taskmode.Marker, error) {
				return taskmode.Marker{TaskID: id, State: taskmode.MarkerPresent}, nil
			}
			modeRow, err := d.RegisterTaskMode(ctx, task.ID, taskmode.ModeControlled, markers)
			if err != nil {
				t.Fatal("controlled mode fixture")
			}
			in, source := finalizationRecordFixture()
			expected := in.Expected
			expected.IntentID = intent
			expected.Scope.TaskRef = strconv.FormatInt(task.ID, 10)
			expected.Scope.RunRef = "scope-" + uuid.NewString()
			in.Expected, in.Evidence.Policy.Context = expected, expected
			for _, o := range []*evidencebind.Observation{&in.Evidence.Owner, &in.Evidence.Cross} {
				o.Context = expected
				o.Receipt.Scope = expected.Scope
				o.Evidence.IntentID = intent
				o.Evidence.Scope = expected.Scope
			}
			in.Evidence.Result.Context = expected
			in.Evidence.Result.OwnerEvidence = in.Evidence.Owner.Evidence
			in.Evidence.Result.CrossEvidence = in.Evidence.Cross.Evidence
			source.Expected = expected
			source.ExplorationID = task.ExplorationID
			source.ModeVersion = modeRow.Version
			source.EvidenceRevision = 0
			source.FinalCheckpointScope = expected.Scope
			source.EvidenceDigest, err = e3record.EvidenceDigest(in.Evidence)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := auditlog.Checkpoint{Version: auditlog.Version, JournalID: "synthetic-final", Scope: expected.Scope, Sequence: 4, MAC: hex.EncodeToString(make([]byte, sha256.Size))}
			source.FinalCheckpointDigest, err = ControlledReadCheckpointDigest(checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			collected, err := evidencerevision.Capture(expected, in.Evidence, in.CanaryFields)
			if err != nil {
				t.Fatal(err)
			}
			request := ControlledReadVerificationRequest{Scope: expected.Scope, TaskID: task.ID, IntentID: intent, AccountRef: expected.Evaluated.Label, ResourceRef: expected.ResourceRef}
			c := &controlledFindingCollector{request: request, collected: true, input: in, digest: source.EvidenceDigest, createdAt: source.RecordCreatedAt, markers: markers, revisionCollected: collected, revisionOperation: controlledRevisionOperation(request)}
			var released atomic.Int64
			stages := make(map[string]int)
			assertHeld := func(operation context.Context, stage string) {
				t.Helper()
				if !c.mu.TryLock() {
					t.Fatal("external callback under collector mutex")
				}
				c.mu.Unlock()
				if released.Load() != 0 || operation.Value(revisionFinalizationContextKey{}) != "synthetic-host-lease" {
					t.Fatal("outer authority released early or context lost")
				}
				stages[stage]++
				if mode == "descriptor-cancel" && stage == "descriptor-acquire" || mode == "issue-cancel" && stage == "issue-acquire" {
					revoke()
				}
			}
			outer := &finalizationTestScope{ctx: leaseCtx}
			outer.check = func(operation context.Context) error {
				assertHeld(operation, "check")
				if mode == "prepare-before-save-revoke" && outer.checks == 4 {
					revoke()
					return errors.New("synthetic authority revoked before save")
				}
				return nil
			}
			provider := &finalizationTestProvider{acquire: func(_ context.Context, actual ControlledReadVerificationRequest, want evidencebind.Context) (ControlledReadFinalizationScope, error) {
				if !c.mu.TryLock() {
					t.Fatal("scope acquired under collector mutex")
				}
				c.mu.Unlock()
				if actual != request || want != expected {
					t.Fatal("scope binding changed")
				}
				return &revisionFinalizationOuterLease{scope: outer, release: func() {
					if !c.mu.TryLock() {
						t.Fatal("scope released under collector mutex")
					}
					c.mu.Unlock()
					released.Add(1)
				}}, nil
			}}
			c.plan = ControlledReadFindingPlan{Expected: expected, RequireFinalizationScope: true, FinalizationScope: provider}
			reads := 0
			c.plan.Source = func(operation context.Context, want evidencebind.Context) (e3record.Source, error) {
				reads++
				stage := "source-a"
				if reads == 2 {
					stage = "source-b"
				}
				assertHeld(operation, stage)
				if want != expected {
					t.Fatal("source binding changed")
				}
				if mode == stage+"-error" {
					return e3record.Source{}, errors.New("synthetic source denial")
				}
				if mode == stage+"-cancel" {
					revoke()
				}
				return source, nil
			}
			dir := t.TempDir()
			if os.Chmod(dir, 0o700) != nil {
				t.Fatal("descriptor root mode")
			}
			anchor, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer anchor.Close()
			revisionProvider := &revisionFinalizationProvider{inner: &revisionWireScope{ctx: leaseCtx, markers: markers, deny: mode == "issue-denied"}, stage: assertHeld}
			rootProvider := &revisionFinalizationRoot{inner: &revisionWireRoot{dir: dir, anchor: anchor, ctx: leaseCtx, deny: mode == "descriptor-denied"}, stage: assertHeld}
			manager := &Manager{pg: d}
			if mode == "issue-ack-lost" {
				config, err := pgx.ParseConfig(dsn)
				if err != nil {
					t.Fatal("owned DSN configuration")
				}
				lost := sql.OpenDB(&revisionACKConnector{Connector: stdlib.GetConnector(*config)})
				defer lost.Close()
				manager = &Manager{pg: &db.DB{DB: lost}}
			}
			c.revisions, err = newControlledRevisionServices(manager, revisionProvider, rootProvider)
			if err != nil {
				t.Fatal(err)
			}
			sink := d
			var findingACK *finalizationFindingACKConnector
			if mode == "finding-ack-lost-cancel" {
				config, parseErr := pgx.ParseConfig(dsn)
				if parseErr != nil {
					t.Fatal("owned finding DSN configuration")
				}
				findingACK = &finalizationFindingACKConnector{Connector: stdlib.GetConnector(*config), afterCommit: cancelCaller}
				lost := sql.OpenDB(findingACK)
				defer lost.Close()
				sink = &db.DB{DB: lost}
			}
			saves := 0
			c.save = func(operation context.Context, prepared e3record.Prepared, m db.TaskModeMarkerReader) (db.ControlledFindingResult, error) {
				assertHeld(operation, "save-enter")
				saves++
				if mode == "save-input-rejected" {
					return db.ControlledFindingResult{}, db.ErrControlledFindingInput
				}
				if mode == "save-cancel" {
					revoke()
				}
				result, err := sink.RecordControlledFinding(operation, prepared, m)
				assertHeld(operation, "save-return")
				if err == nil && mode == "commit-success-cancel" {
					cancelCaller()
				}
				return result, err
			}
			err = c.finalize(caller, ControlledReadVerificationEvidence{Request: request, Completed: true, Finalized: true, FinalCheckpoint: checkpoint})
			if released.Load() != 1 || outer.releases != 1 || c.finalizing {
				t.Fatal("outer scope ownership not balanced")
			}
			var revisions, findings int
			if e := d.QueryRowContext(ctx, `SELECT count(*) FROM evidence_revision_operations WHERE operation_ref=$1`, c.revisionOperation).Scan(&revisions); e != nil {
				t.Fatal("revision row count")
			}
			if e := d.QueryRowContext(ctx, `SELECT count(*) FROM findings WHERE task_id=$1`, task.ID).Scan(&findings); e != nil {
				t.Fatal("finding row count")
			}
			positive := mode == "success" || mode == "commit-success-cancel"
			if positive {
				if err != nil || !c.saved || revisions != 1 || findings != 1 || reads != 2 || saves != 1 {
					t.Fatalf("positive rows/revision/read/save mismatch: %v", err)
				}
			} else if err == nil || c.saved {
				t.Fatal("failed finalization reported success")
			}
			if mode == "finding-ack-lost-cancel" {
				if findingACK.commits.Load() != 1 {
					t.Fatal("actual finding COMMIT ACK was not lost")
				}
				if !errors.Is(err, ErrControlledFindingOutcomeUnknown) || findings != 1 || revisions != 1 {
					t.Fatal("real commit ACK loss changed classification")
				}
				var failure controlledFindingStoreFailure
				if !errors.As(err, &failure) || failure.operation == ([32]byte{}) {
					t.Fatal("finding operation reference lost")
				}
			} else if !positive && findings != 0 {
				t.Fatal("failed path persisted finding")
			}
			wantRevision := positive || mode == "source-b-error" || mode == "source-b-cancel" || mode == "issue-ack-lost" || mode == "prepare-before-save-revoke" || mode == "save-input-rejected" || mode == "save-cancel" || mode == "finding-ack-lost-cancel"
			if (revisions == 1) != wantRevision {
				t.Fatal("revision persistence stage changed")
			}
			var revisionFailure controlledRevisionFailure
			wantReason := map[string]string{"source-a-error": "source_a_rejected", "source-a-cancel": "source_a_rejected", "descriptor-denied": "descriptor_unacknowledged", "descriptor-cancel": "descriptor_unacknowledged", "issue-denied": "issuance_rejected", "issue-cancel": "issuance_rejected", "source-b-error": "source_b_rejected", "source-b-cancel": "source_b_rejected", "issue-ack-lost": "issuance_outcome_unknown"}[mode]
			if wantReason != "" && (!errors.As(err, &revisionFailure) || revisionFailure.reason != wantReason || revisionFailure.operation != c.revisionOperation) {
				t.Fatalf("revision error classification or operation lost: %v", err)
			}
			if mode == "issue-ack-lost" && (revisions != 1 || findings != 0 || errors.Is(err, ErrControlledFindingOutcomeUnknown)) {
				t.Fatal("revision ACK loss confused with finding write")
			}
			if mode == "prepare-before-save-revoke" && (!c.prepared || saves != 0 || revisions != 1) {
				t.Fatal("post-Prepare revocation did not prevent save")
			}
			if positive || mode == "finding-ack-lost-cancel" {
				for _, stage := range []string{"source-a", "descriptor-acquire", "descriptor-release", "issue-acquire", "issue-release", "source-b", "save-enter", "save-return"} {
					if stages[stage] == 0 {
						t.Fatalf("scope stage not observed: %s", stage)
					}
				}
			}
		})
	}
}

type revisionFinalizationOuterLease struct {
	scope   *finalizationTestScope
	release func()
}

func (l *revisionFinalizationOuterLease) Context() context.Context        { return l.scope.Context() }
func (l *revisionFinalizationOuterLease) Check(ctx context.Context) error { return l.scope.Check(ctx) }
func (l *revisionFinalizationOuterLease) Release()                        { l.scope.Release(); l.release() }

func TestRequireFinalizationScopeKeepsResolvedPlan(t *testing.T) {
	if RequireControlledReadFinalizationScope(nil) != nil {
		t.Fatal("nil resolver manufactured")
	}
	in, _ := finalizationRecordFixture()
	calls := 0
	kg := &realInputsTestKG{}
	provider := &finalizationTestProvider{}
	source := e3record.SourceReader(func(context.Context, evidencebind.Context) (e3record.Source, error) {
		t.Fatal("early source read")
		return e3record.Source{}, nil
	})
	resolve := RequireControlledReadFinalizationScope(func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		calls++
		return ControlledReadFindingPlan{Expected: in.Expected, KG: kg, Source: source, FinalizationScope: provider}, nil
	})
	if calls != 0 {
		t.Fatal("strict wrapper called resolver before admission")
	}
	plan, err := resolve(t.Context(), ControlledReadVerificationRequest{})
	if err != nil || calls != 1 || !plan.RequireFinalizationScope || plan.FinalizationScope != provider || plan.KG != kg || plan.Source == nil || plan.Expected != in.Expected {
		t.Fatal("strict wrapper replaced native plan")
	}
}

// Lose only the acknowledgement of a real finding transaction COMMIT. The
// original writer and its SQL run normally; this is a test-only driver fault.
type finalizationFindingACKConnector struct {
	driver.Connector
	afterCommit func()
	commits     atomic.Int64
}

func (c *finalizationFindingACKConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &finalizationFindingACKConn{Conn: conn, owner: c}, nil
}

type finalizationFindingACKConn struct {
	driver.Conn
	owner   *finalizationFindingACKConnector
	finding bool
}

func (c *finalizationFindingACKConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	c.finding = false
	return &finalizationFindingACKTx{Tx: tx, c: c}, nil
}
func (c *finalizationFindingACKConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "INSERT INTO e3_evidence_manifests") {
		c.finding = true
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}
func (c *finalizationFindingACKConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

type finalizationFindingACKTx struct {
	driver.Tx
	c *finalizationFindingACKConn
}

func (t *finalizationFindingACKTx) Commit() error {
	err := t.Tx.Commit()
	if err == nil && t.c.finding {
		t.c.owner.commits.Add(1)
		t.c.owner.afterCommit()
		return errors.New("synthetic finding COMMIT ACK lost")
	}
	return err
}
