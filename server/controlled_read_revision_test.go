package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// All authorities/accounts/model are explicit synthetic fixtures. PG, protected
// files, HTTP observations, descriptor synchronization and SQL issuance are real.
type revisionWireScope struct {
	ctx             context.Context
	markers         db.TaskModeMarkerReader
	hook            func(evidencerevision.Access)
	deny            bool
	calls           atomic.Int64
	missingSchemaDB *sql.DB
}
type revisionWireLease struct {
	p      *revisionWireScope
	marker taskmode.Marker
}

func (p *revisionWireScope) Acquire(ctx context.Context, a evidencerevision.Access) (evidencerevision.Lease, error) {
	p.calls.Add(1)
	if p.deny {
		return nil, errors.New("synthetic private scope denial")
	}
	if p.hook != nil {
		p.hook(a)
	}
	marker, err := p.markers(ctx, a.TaskID)
	if err != nil {
		return nil, err
	}
	return &revisionWireLease{p, marker}, nil
}
func (l *revisionWireLease) Context() context.Context        { return l.p.ctx }
func (l *revisionWireLease) MarkerSnapshot() taskmode.Marker { return l.marker }
func (l *revisionWireLease) Release()                        {}

type revisionWireRoot struct {
	dir    string
	anchor *os.File
	ctx    context.Context
	deny   bool
	calls  atomic.Int64
}
type revisionWireRootLease struct{ p *revisionWireRoot }

func (p *revisionWireRoot) AcquireDescriptorRoot(ctx context.Context, a evidencerevision.DescriptorFileAccess) (evidencerevision.DescriptorRootScope, error) {
	p.calls.Add(1)
	if p.deny {
		return nil, errors.New("synthetic private root denial")
	}
	return &revisionWireRootLease{p}, nil
}
func (l *revisionWireRootLease) Context() context.Context { return l.p.ctx }
func (l *revisionWireRootLease) Anchor() *os.File         { return l.p.anchor }
func (l *revisionWireRootLease) Directory() string        { return l.p.dir }
func (l *revisionWireRootLease) Release()                 {}

// Test-only driver loses exactly the ACK of a real revision COMMIT. It neither
// replaces Issue nor supplies a fabricated Receipt. Other transactions pass.
type revisionACKConnector struct {
	driver.Connector
	lost atomic.Int64
}

func (c *revisionACKConnector) Connect(ctx context.Context) (driver.Conn, error) {
	v, e := c.Connector.Connect(ctx)
	if e != nil {
		return nil, e
	}
	return &revisionACKConn{Conn: v, owner: c}, nil
}

type revisionACKConn struct {
	driver.Conn
	owner    *revisionACKConnector
	revision bool
}

func (c *revisionACKConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	v, e := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	c.revision = false
	return &revisionACKTx{v, c}, nil
}
func (c *revisionACKConn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, "INSERT INTO evidence_revision_operations") {
		c.revision = true
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, a)
}
func (c *revisionACKConn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, a)
}

type revisionACKTx struct {
	driver.Tx
	c *revisionACKConn
}

func (t *revisionACKTx) Commit() error {
	err := t.Tx.Commit()
	if err == nil && t.c.revision {
		t.c.owner.lost.Add(1)
		return errors.New("synthetic commit ACK lost")
	}
	return err
}

func revisionWireServices(t *testing.T, f *controlledReadDBFixture, mode string) (*controlledRevisionServices, *revisionWireScope, *revisionWireRoot) {
	t.Helper()
	if err := evidencerevision.Migrate(context.Background(), f.m.pg.DB); err != nil {
		t.Fatal(err)
	}
	markers, err := f.s.ControlledTaskModeMarkers()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.s.ctx)
	t.Cleanup(cancel)
	scope := &revisionWireScope{ctx: ctx, markers: markers, deny: mode == "scope-denied"}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { anchor.Close() })
	root := &revisionWireRoot{dir: dir, anchor: anchor, ctx: ctx, deny: mode == "descriptor-denied"}
	manager := f.m
	if mode == "lost-ack" || mode == "schema-unapplied" {
		config := revisionWireServerConfig(t, f.m.pg.DB)
		if mode == "schema-unapplied" {
			schema := "revision_wire_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			if _, err = f.m.pg.Exec("CREATE SCHEMA " + schema); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.m.pg.Exec("DROP SCHEMA " + schema + " CASCADE") })
			config.RuntimeParams["search_path"] = schema
		}
		connector := &revisionACKConnector{Connector: stdlib.GetConnector(*config)}
		isolated := sql.OpenDB(connector)
		if mode == "schema-unapplied" {
			scope.missingSchemaDB = isolated
		}
		t.Cleanup(func() { isolated.Close() })
		manager = &Manager{pg: &db.DB{DB: isolated}}
	}
	services, err := newControlledRevisionServices(manager, scope, root)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "scope-cancel" {
		scope.hook = func(evidencerevision.Access) { cancel() }
	}
	return services, scope, root
}

func installRevisionWireFinding(t *testing.T, f *controlledReadDBFixture, h *durableTestHost, services *controlledRevisionServices, scope *revisionWireScope, root *revisionWireRoot, mode string) (*atomic.Int64, *ControlledReadVerificationRequest) {
	t.Helper()
	calls := &atomic.Int64{}
	actualRequest := &ControlledReadVerificationRequest{}
	store, err := auditcheckpoint.New(f.m.pg.DB, h, auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 16, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sourceOptions := ControlledReadAuditSourceOptions{
		LatestCheckpoint: func(ctx context.Context, scope transmit.Scope) (ControlledReadAuditAnchor, error) {
			h.mu.Lock()
			b := h.last
			h.mu.Unlock()
			head, e := store.Head(ctx, auditcheckpoint.Query{Epoch: h.epoch, Binding: b})
			if e != nil || head.Phase != auditcheckpoint.Sealed {
				return ControlledReadAuditAnchor{}, errors.New("synthetic sealed head unavailable")
			}
			return ControlledReadAuditAnchor{Checkpoint: head.Checkpoint, Ref: fmt.Sprintf("synthetic-cpd:%s:%d", head.Checkpoint.JournalID, head.Revision)}, nil
		},
		ResolveKey: func(context.Context, ControlledReadAuditKeyRequest) ([]byte, error) {
			if h.revoked.Load() {
				return nil, errors.New("synthetic key revoked")
			}
			return h.key, nil
		}, MaxJournalBytes: 1 << 20, MaxEvents: 16, Timeout: 3 * time.Second,
	}
	policy := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone, HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond), ValidUntil: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)}
	owner := idor.AccountRef{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified}
	peer := idor.AccountRef{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified}
	resolve := func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		*actualRequest = r
		// Bind to the server actually running this request, including strict
		// bootstrap replacement; an old runtime is not the new admission registry.
		reader, err := f.s.NewControlledReadAuditSource(sourceOptions)
		if err != nil {
			return ControlledReadFindingPlan{}, err
		}
		expected := evidencebind.Context{Scope: r.Scope, IntentID: r.IntentID, ResourceRef: r.ResourceRef, ReadApprovalRef: "read-approval", KGPolicyVersion: "synthetic-kg-policy", Owner: owner, Evaluated: peer}
		metadata := func(ctx context.Context, actual evidencebind.Context) (e3record.Source, error) {
			call := calls.Add(1)
			if mode == "source-b-error" && call == 2 {
				return e3record.Source{}, errors.New("synthetic fresh source refused")
			}
			var version uint64
			if e := f.m.pg.QueryRowContext(ctx, `SELECT version FROM task_control_modes WHERE task_id=$1`, r.TaskID).Scan(&version); e != nil {
				return e3record.Source{}, e
			}
			source := e3record.Source{Expected: expected, ExplorationID: f.task.ExpID, ModeVersion: version, KGSnapshotRef: "synthetic-kg", KGSnapshotDigest: sha256.Sum256([]byte("synthetic KG")), GenerationRef: "synthetic-harness", InstanceRef: "synthetic-instance", ResetGeneration: "synthetic-reset", ModelTag: "synthetic-model", Policy: policy, CanaryFields: []string{"note"}, Severity: "medium"}
			if mode == "revision-b-match" && call == 2 {
				if e := f.m.pg.QueryRowContext(ctx, `SELECT revision FROM evidence_revision_initials WHERE execution_ref=$1`, r.Scope.RunRef).Scan(&source.EvidenceRevision); e != nil {
					return e3record.Source{}, e
				}
			}
			if mode == "revision-b-mismatch" && call == 2 {
				source.EvidenceRevision = mathMaxRevision
			}
			if mode == "revision-mismatch" {
				source.EvidenceRevision = mathMaxRevision
			}
			if mode == "exploration-mismatch" {
				source.ExplorationID++
			}
			if mode == "mode-mismatch" {
				source.ModeVersion++
			}
			if mode == "source-b-mode" && call == 2 {
				source.ModeVersion++
			}
			return source, nil
		}
		source, e := reader.SourceReader(metadata)
		if e != nil {
			return ControlledReadFindingPlan{}, e
		}
		return ControlledReadFindingPlan{Expected: expected, Candidate: idor.Candidate{Owner: owner, Evaluated: peer, ResourceRef: r.ResourceRef}, KG: &verificationTestKG{policy: policy}, Source: source}, nil
	}
	if mode == "strict-bootstrap" {
		t.Setenv("ARTEX_E2_SNAPSHOT_DIR", f.store.snapshotDir)
		t.Setenv("ARTEX_E2_STATE_DIR", f.store.stateDir)
		t.Setenv("ARTEX_LLM_API_KEY", "")
		t.Setenv(notifyBackgroundDisabledEnv, "1")
		preserveRevisionWireAuth(t, f.m.pg)
		if err = f.m.pg.SetSetting(authPassKey, "synthetic-disabled-login"); err != nil {
			t.Fatal(err)
		}
		keyDir := t.TempDir()
		if err = os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), bytes.Repeat([]byte("s"), 32), 0o600); err != nil {
			t.Fatal(err)
		}
		old := f.s
		var strict *Server
		strict, err = NewWithRevisionedDurableControlledReadHost(old.ctx, f.m, t.TempDir(), t.TempDir(), keyDir, ControlledReadRevisionHostOptions{Durable: ControlledReadDurableHostOptions{Host: ControlledReadHostOptions{Mode: f.e.controlledReads.mode.host, Findings: resolve}, Publisher: f.e.controlledReads.publisher.factory}, Scope: scope, DescriptorRoot: root})
		if err == nil {
			f.s, f.e, f.store = strict, strict.engine, strict.engine.controlledReads.store
			f.e.started.Store(f.task.ID, true)
			f.e.Pause(f.task.ID, errors.New("synthetic bootstrap paused"))
		}
	} else {
		err = f.s.configureControlledReadFindings(resolve, services)
	}
	if err != nil {
		t.Fatal(err)
	}
	return calls, actualRequest
}

const mathMaxRevision = uint64(1<<63 - 1)

func TestRevisionWireSyntheticSQLAndProtectedDescriptor(t *testing.T) {
	// Every positive case reserves its own sequence value before the real Issue.
	// This models a consumed/rolled-back allocation and exposes a default 1 even
	// when a single subtest runs against a fresh database.
	for _, mode := range []string{"seed", "success", "strict-bootstrap", "revision-b-match", "revision-b-mismatch", "late-pause", "descriptor-denied", "scope-denied", "scope-cancel", "revision-mismatch", "exploration-mismatch", "mode-mismatch", "source-b-mode", "source-b-error", "key-during-issue", "epoch-during-issue", "lost-ack", "schema-unapplied", "cancel-before-claim"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int64
			target := controlledReadTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"ok","data":{"note":%q}}`, verificationTestMarker)
			}))
			f := newControlledVerificationDBFixture(t, target.URL)
			revisionWireRequestTimeout(t, f)
			publisherMode := "success"
			if mode == "cancel-before-claim" {
				publisherMode = mode
			}
			h := installDurableTestPublisher(t, f, publisherMode)
			services, scope, root := revisionWireServices(t, f, mode)
			positive := mode == "seed" || mode == "success" || mode == "strict-bootstrap" || mode == "revision-b-match" || mode == "late-pause"
			var reserved uint64
			if positive {
				if err := f.m.pg.QueryRow(`SELECT nextval(pg_get_serial_sequence('evidence_revision_initials','revision'))`).Scan(&reserved); err != nil {
					t.Fatal("could not reserve the fixture sequence value")
				}
			}
			calls, request := installRevisionWireFinding(t, f, h, services, scope, root, mode)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			if mode == "late-pause" {
				scope.hook = func(evidencerevision.Access) { close(entered); <-release }
			}
			if mode == "key-during-issue" {
				scope.hook = func(evidencerevision.Access) { h.revoked.Store(true) }
			}
			if mode == "epoch-during-issue" {
				scope.hook = func(evidencerevision.Access) {
					if _, err := f.m.pg.Exec(`UPDATE audit_checkpoint_epochs SET generation=2 WHERE store_ref=$1`, h.epoch.StoreRef); err != nil {
						t.Error(err)
					}
				}
			}
			diagnostics := &durableStageLog{}
			previous := log.Writer()
			log.SetOutput(diagnostics)
			defer log.SetOutput(previous)
			done := startVerifiedFixtureIntent(t, f)
			if mode == "late-pause" {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("Issue barrier absent")
				}
				if _, err := f.s.applyTaskControl(f.task, "pause"); err != nil {
					t.Fatal(err)
				}
				unblock()
			}
			if mode == "cancel-before-claim" {
				select {
				case <-h.sealEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("Seal barrier absent")
				}
				cancelled := make(chan struct{})
				var stopOnce sync.Once
				f.e.workMu.Lock()
				run := f.e.work[f.intent.ID]
				original := run.cancel
				run.cancel = func(err error) { original(err); stopOnce.Do(func() { close(cancelled) }) }
				f.e.workMu.Unlock()
				control := make(chan error, 1)
				go func() { control <- f.e.ControlWork(context.Background(), f.intent.ID, "cancel") }()
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("cancel did not win")
				}
				close(h.sealRelease)
				if err := <-control; err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Fatal("revision run did not settle")
			}
			taskID := mustSyntheticTaskID(t, f.task.ID)
			var findings int
			if err := f.m.pg.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, taskID).Scan(&findings); err != nil {
				t.Fatal(err)
			}
			node, err := f.task.Store.GetNode(f.intent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if positive {
				if findings != 1 || node.State != "done" || calls.Load() != 2 {
					t.Fatalf("success finding=%d state=%s source=%d", findings, node.State, calls.Load())
				}
			} else if findings != 0 || node.State == "done" {
				t.Fatalf("failure persisted finding/done: %d %s", findings, node.State)
			}
			if hits.Load() != 2 {
				t.Fatalf("actual HTTP count=%d", hits.Load())
			}
			if mode == "cancel-before-claim" {
				if calls.Load() != 0 || scope.calls.Load() != 0 {
					t.Fatal("preclaim cancellation reached source/Issue")
				}
				return
			}
			operation := controlledRevisionOperation(*request)
			var count int
			var revision uint64
			if err = f.m.pg.QueryRow(`SELECT count(*),COALESCE(max(revision),0) FROM evidence_revision_operations WHERE operation_ref=$1`, operation).Scan(&count, &revision); err != nil {
				t.Fatal(err)
			}
			shouldIssue := positive || mode == "revision-b-mismatch" || mode == "revision-mismatch" || mode == "source-b-mode" || mode == "source-b-error" || mode == "key-during-issue" || mode == "epoch-during-issue" || mode == "lost-ack"
			if (count == 1) != shouldIssue {
				t.Fatalf("issuance count=%d mode=%s", count, mode)
			}
			if positive {
				if revision <= reserved || revision <= 1 {
					t.Fatal("SQL revision defaulted to one")
				}
				var binding []byte
				var manifestRevision uint64
				if err = f.m.pg.QueryRow(`SELECT v.binding_canonical,m.evidence_revision FROM e3_verdict_issuances v JOIN e3_evidence_manifests m ON m.id=v.manifest_id WHERE v.task_id=$1`, taskID).Scan(&binding, &manifestRevision); err != nil {
					t.Fatal(err)
				}
				var source e3record.Source
				if json.Unmarshal(binding, &source) != nil || source.EvidenceRevision != revision || manifestRevision != revision {
					t.Fatal("finding issuance did not retain actual SQL revision")
				}
			}
			if mode == "schema-unapplied" {
				var absent bool
				if err = scope.missingSchemaDB.QueryRow(`SELECT to_regclass('evidence_revision_initials') IS NULL`).Scan(&absent); err != nil || !absent {
					t.Fatal("missing schema was automatically initialized")
				}
			}
			if mode == "descriptor-denied" && scope.calls.Load() != 0 {
				t.Fatal("descriptor failure reached Issue")
			}
			if mode == "lost-ack" {
				if !strings.Contains(diagnostics.String(), "revision_failed reason=issuance_outcome_unknown operation_ref="+operation) || strings.Contains(diagnostics.String(), "storage_outcome_unknown") {
					t.Fatal("revision commit classified as finding commit")
				}
				descriptor, err := services.files.Load(context.Background(), operation)
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := services.store.Reconcile(context.Background(), descriptor)
				if err != nil || receipt.Revision != revision {
					t.Fatalf("operator explicit reconciliation failed: %v", err)
				}
			}
		})
	}
}

func TestRevisionWireRequiresRealProvidersAndNoJSONAuthority(t *testing.T) {
	options := ControlledReadRevisionHostOptions{}
	if body, err := json.Marshal(options); err != nil || string(body) != "{}" {
		t.Fatal("host authority serialized")
	}
	if s, err := NewWithRevisionedDurableControlledReadHost(context.Background(), nil, "", "", "", options); s != nil || !errors.Is(err, ErrControlledReadHostConfiguration) {
		t.Fatal("missing real host accepted")
	}
	var scope *revisionWireScope
	var root *revisionWireRoot
	target := controlledReadTarget(t, http.NotFoundHandler())
	f := newControlledVerificationDBFixture(t, target.URL)
	if _, err := newControlledRevisionServices(f.m, scope, root); err == nil {
		t.Fatal("typed nil scope accepted")
	}
	markers, err := f.s.ControlledTaskModeMarkers()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newControlledRevisionServices(f.m, &revisionWireScope{ctx: context.Background(), markers: markers}, root); err == nil {
		t.Fatal("typed nil root accepted")
	}
}

// Derive additional driver-instrumented pools from the actual server pool.
// The revision package's separate test DSN is not this server's database.
func revisionWireServerConfig(t *testing.T, pool *sql.DB) *pgx.ConnConfig {
	t.Helper()
	conn, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal("server fixture connection unavailable")
	}
	defer conn.Close()
	var config *pgx.ConnConfig
	err = conn.Raw(func(raw any) error {
		pg, ok := raw.(*stdlib.Conn)
		if !ok {
			return errors.New("fixture driver is not pgx")
		}
		config = pg.Conn().Config().Copy()
		return nil
	})
	if err != nil || config == nil {
		t.Fatal("server fixture configuration unavailable")
	}
	return config
}

func preserveRevisionWireAuth(t *testing.T, pg *db.DB) {
	t.Helper()
	value, present, err := pg.GetSetting(authPassKey)
	if err != nil {
		t.Fatal("read original auth setting")
	}
	t.Cleanup(func() {
		if present {
			if err := pg.SetSetting(authPassKey, value); err != nil {
				t.Error("restore original auth setting")
			}
		} else if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, authPassKey); err != nil {
			t.Error("restore absent auth setting")
		}
	})
}

const revisionWireFixtureTimeoutMS = 10_000

func revisionWireRequestTimeout(t *testing.T, f *controlledReadDBFixture) {
	t.Helper()
	path := filepath.Join(f.store.snapshotDir, "approved.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot snapshotJSON
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	// A/B source reads, descriptor fsync and SQL are part of the existing bounded
	// finalizer. A loaded race run must reach the intended negative guard first.
	snapshot.RequestTimeoutMS = revisionWireFixtureTimeoutMS
	body, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRevisionWireStrictBootstrapRestoresAuthentication(t *testing.T) {
	for _, present := range []bool{false, true} {
		name := "absent"
		if present {
			name = "present"
		}
		t.Run(name, func(t *testing.T) {
			target := controlledReadTarget(t, http.NotFoundHandler())
			f := newControlledVerificationDBFixture(t, target.URL)
			preserveRevisionWireAuth(t, f.m.pg)
			const value = "synthetic-retained-auth-state"
			if present {
				if err := f.m.pg.SetSetting(authPassKey, value); err != nil {
					t.Fatal(err)
				}
			} else if _, err := f.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authPassKey); err != nil {
				t.Fatal(err)
			}
			t.Run("strict-bootstrap", func(t *testing.T) {
				h := installDurableTestPublisher(t, f, "success")
				services, scope, root := revisionWireServices(t, f, "strict-bootstrap")
				installRevisionWireFinding(t, f, h, services, scope, root, "strict-bootstrap")
			})
			got, exists, err := f.m.pg.GetSetting(authPassKey)
			if err != nil || exists != present || present && got != value {
				t.Fatal("strict bootstrap did not restore original auth presence/value")
			}
		})
	}
}

type revisionCaptureLog struct {
	collector *controlledFindingCollector
	log       durableStageLog
	locked    atomic.Bool
}

func (l *revisionCaptureLog) Write(body []byte) (int, error) {
	if l.collector.mu.TryLock() {
		l.collector.mu.Unlock()
	} else {
		l.locked.Store(true)
	}
	return l.log.Write(body)
}

func TestRevisionWireCaptureFailureIsStickyAndLoggedOnce(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 0, 20, 0, time.UTC)
	scope := transmit.Scope{RunRef: "run-1", TaskRef: "1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1"}
	owner := idor.AccountRef{Label: "owner", Handle: "session-a", Status: oracle.SubjectVerified}
	peer := idor.AccountRef{Label: "reader", Handle: "session-b", Status: oracle.SubjectVerified}
	expected := evidencebind.Context{Scope: scope, IntentID: 11, ResourceRef: "document:one", ReadApprovalRef: "read-1", KGPolicyVersion: "kg-1", Owner: owner, Evaluated: peer}
	request := ControlledReadVerificationRequest{Scope: scope, TaskID: 1, IntentID: 11, AccountRef: peer.Label, ResourceRef: expected.ResourceRef}
	policy := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
		HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}
	evidence := ControlledReadVerificationEvidence{Request: request}
	detail := &evidence.Verification
	detail.PolicyResolved, detail.FrozenPolicy, detail.CanaryFields = true, policy, []string{"note"}
	detail.Owner.Captured, detail.Evaluated.Captured = true, true
	detail.Owner.Capture.ObservedAt, detail.Evaluated.Capture.ObservedAt = at.Add(-9*time.Second), at
	receipt := func(account idor.AccountRef, attempt uint64, admitted, done time.Time) transmit.Receipt {
		return transmit.Receipt{Scope: scope, Attempt: attempt, AccountRef: account.Label, SessionRef: account.Handle, ResourceRef: expected.ResourceRef,
			ApprovalRef: expected.ReadApprovalRef, Method: "GET", Reason: transmit.Completed, Admitted: true, AdmittedAt: admitted,
			RequestWritten: true, ResponseReceived: true, StatusCode: 200, At: done}
	}
	detail.Owner.Receipt = receipt(owner, 1, at.Add(-10*time.Second), at.Add(-8*time.Second))
	detail.Evaluated.Receipt = receipt(peer, 2, at.Add(-5*time.Second), at.Add(time.Second))
	facts := oracle.ComparisonFacts{SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes, CrossNormalData: oracle.StateYes,
		NontrivialDataMatch: oracle.StateYes, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes}
	detail.Result = idor.Result{Facts: facts, Verdict: oracle.Evaluate(oracle.CaseInput{Policy: policy,
		Subjects: oracle.SubjectContext{OwnerStatus: owner.Status, EvaluatedStatus: peer.Status, OwnerRef: owner.Label, EvaluatedRef: peer.Label}, Facts: facts}, at)}
	var sourceCalls atomic.Int64
	newCollector := func() *controlledFindingCollector {
		return &controlledFindingCollector{request: request, plan: ControlledReadFindingPlan{Expected: expected, Source: func(context.Context, evidencebind.Context) (e3record.Source, error) {
			sourceCalls.Add(1)
			return e3record.Source{}, errors.New("synthetic source should not run")
		}},
			revisions: &controlledRevisionServices{}, revisionOperation: "revision:synthetic-operation"}
	}
	control := newCollector()
	if err := control.collect(context.Background(), evidence); err != nil || !control.collected {
		t.Fatalf("valid typed observation control rejected: %v", err)
	}
	c := newCollector()
	diagnostics := &revisionCaptureLog{collector: c}
	previous := log.Writer()
	log.SetOutput(diagnostics)
	defer log.SetOutput(previous)
	bad := evidence
	bad.Verification.CanaryFields = []string{"capture-private-field", "capture-private-field"}
	first := c.collect(context.Background(), bad)
	var failure controlledRevisionFailure
	if !errors.As(first, &failure) || failure.reason != "capture_rejected" || failure.operation != c.revisionOperation || !errors.Is(first, ErrControlledFindingUnavailable) {
		t.Fatalf("Capture rejection lost its classification: %v", first)
	}
	if err := c.collect(context.Background(), evidence); err != first {
		t.Fatal("later valid collection replaced first failure")
	}
	if _, err := c.revisionSource(context.Background(), expected); err != first {
		t.Fatal("Source replaced first Capture failure")
	}
	text := diagnostics.log.String()
	if strings.Count(text, "revision_failed reason=capture_rejected operation_ref="+c.revisionOperation) != 1 || strings.Contains(text, "capture-private-field") {
		t.Fatal("Capture failure was duplicated or exposed private input")
	}
	if diagnostics.locked.Load() || sourceCalls.Load() != 0 || c.collected || c.prepared || c.saved {
		t.Fatal("Capture failure logged under lock or reached later storage/source")
	}
}
