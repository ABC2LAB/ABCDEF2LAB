package evidencerevision

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// This fixture owns synthetic observations and a marker file. It does not
// authenticate a real collector, root/writer lease, account, KG or operation.
type fixtureScope struct {
	ctx                context.Context
	markerPath         string
	hook               func(Access)
	deny               Purpose
	acquired, released atomic.Int64
}
type fixtureLease struct {
	p *fixtureScope
	a Access
}

func (p *fixtureScope) Acquire(ctx context.Context, a Access) (Lease, error) {
	if a.Purpose == p.deny {
		return nil, errors.New("synthetic private scope failure")
	}
	p.acquired.Add(1)
	if p.hook != nil {
		p.hook(a)
	}
	return &fixtureLease{p, a}, nil
}
func (l *fixtureLease) Context() context.Context { return l.p.ctx }
func (l *fixtureLease) MarkerSnapshot() taskmode.Marker {
	state := taskmode.MarkerUnknown
	body, err := os.ReadFile(l.p.markerPath)
	if err == nil && string(body) == "present" {
		state = taskmode.MarkerPresent
	} else if err == nil && string(body) == "clear" {
		state = taskmode.MarkerClear
	}
	return taskmode.Marker{TaskID: l.a.TaskID, State: state}
}
func (l *fixtureLease) Release() { l.p.released.Add(1) }

type revisionFixture struct {
	d        *db.DB
	store    *Store
	scope    *fixtureScope
	request  Request
	expected evidencebind.Context
	input    evidencebind.Input
}

func newRevisionFixture(t *testing.T) *revisionFixture {
	t.Helper()
	dsn := os.Getenv("ARTEX_EVIDENCE_REVISION_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated evidence revision PostgreSQL not supplied")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal("dedicated test database unavailable")
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := Migrate(t.Context(), d.DB); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTask("synthetic revision fixture", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal("fixture task")
	}
	intent, err := d.Exploration(task.ExplorationID).AddIntent(map[string]any{"description": "synthetic observed read"}, 5, nil, "revision-test")
	if err != nil {
		t.Fatal("fixture intent")
	}
	version, err := d.RegisterTaskMode(t.Context(), task.ID, taskmode.ModeControlled, nil)
	if err != nil {
		t.Fatal("fixture controlled fence")
	}
	marker := filepath.Join(t.TempDir(), "synthetic-marker")
	if err := os.WriteFile(marker, []byte("present"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &fixtureScope{ctx: t.Context(), markerPath: marker}
	store, err := New(d.DB, p)
	if err != nil {
		t.Fatal(err)
	}
	expected := evidencebind.Context{Scope: transmit.Scope{RunRef: "synthetic:" + uuid.NewString(), TaskRef: strconv.FormatInt(task.ID, 10), CaseRef: "case:synthetic", ResetRef: "reset:synthetic", PolicyRef: "policy:synthetic"}, IntentID: intent, ResourceRef: "document:one", ReadApprovalRef: "approval:synthetic", KGPolicyVersion: "kg:synthetic", Owner: idor.AccountRef{Label: "owner", Handle: "session-a", Status: oracle.SubjectVerified}, Evaluated: idor.AccountRef{Label: "peer", Handle: "session-b", Status: oracle.SubjectVerified}}
	input := revisionInput(expected, 0)
	collected, err := Capture(expected, input, []string{"note"})
	if err != nil {
		t.Fatal(err)
	}
	f := &revisionFixture{d: d, store: store, scope: p, expected: expected, input: input, request: Request{OperationRef: "operation:" + uuid.NewString(), Collected: collected, ExpectedExplorationID: task.ExplorationID, ExpectedModeVersion: version.Version}}
	t.Cleanup(func() {
		if p.acquired.Load() != p.released.Load() {
			t.Error("scope lease not released")
		}
	})
	return f
}

func revisionInput(c evidencebind.Context, offset time.Duration) evidencebind.Input {
	at := time.Date(2026, 11, 3, 12, 0, 20, 0, time.UTC).Add(offset)
	policy := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone, HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}
	facts := oracle.ComparisonFacts{SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes, CrossNormalData: oracle.StateYes, NontrivialDataMatch: oracle.StateYes, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes}
	read := func(account idor.AccountRef, n uint64, role evidencebind.ReadRole, observed time.Time) evidencebind.Observation {
		return evidencebind.Observation{Context: c, Receipt: transmit.Receipt{Scope: c.Scope, Attempt: n, AccountRef: account.Label, SessionRef: account.Handle, ResourceRef: c.ResourceRef, ApprovalRef: c.ReadApprovalRef, Method: "GET", Reason: transmit.Completed, Admitted: true, AdmittedAt: observed.Add(-time.Second), RequestWritten: true, ResponseReceived: true, StatusCode: 200, At: observed.Add(time.Second)}, Evidence: evidencebind.EvidenceRef{ID: "synthetic:" + string(role), IntentID: c.IntentID, Scope: c.Scope, Attempt: n, Role: role}, ObservedAt: observed}
	}
	owner, cross := read(c.Owner, 1, evidencebind.OwnerRead, at.Add(-5*time.Second)), read(c.Evaluated, 2, evidencebind.CrossRead, at)
	result := idor.Result{Facts: facts, Verdict: oracle.Evaluate(oracle.CaseInput{Policy: policy, Facts: facts, Subjects: oracle.SubjectContext{OwnerRef: c.Owner.Label, OwnerStatus: c.Owner.Status, EvaluatedRef: c.Evaluated.Label, EvaluatedStatus: c.Evaluated.Status}}, at)}
	return evidencebind.Input{Policy: evidencebind.PolicySnapshot{Context: c, Assertion: policy}, Owner: owner, Cross: cross, Result: evidencebind.ResultSnapshot{Context: c, OwnerEvidence: owner.Evidence, CrossEvidence: cross.Evidence, ObservedAt: at, Result: result}}
}

func issueRevision(t *testing.T, f *revisionFixture, r Request) Receipt {
	t.Helper()
	out, err := f.store.Issue(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func revisionCounts(t *testing.T, f *revisionFixture, initials, operations int) {
	t.Helper()
	var a, b int
	if err := f.d.QueryRow(`SELECT count(*) FROM evidence_revision_initials WHERE execution_ref=$1`, f.expected.Scope.RunRef).Scan(&a); err != nil {
		t.Fatal("count initials")
	}
	if err := f.d.QueryRow(`SELECT count(*) FROM evidence_revision_operations o JOIN evidence_revision_initials i USING(revision) WHERE i.execution_ref=$1`, f.expected.Scope.RunRef).Scan(&b); err != nil {
		t.Fatal("count operations")
	}
	if a != initials || b != operations {
		t.Fatalf("metadata counts %d/%d expected %d/%d", a, b, initials, operations)
	}
}

func TestRevisionIssueAndExactRetry(t *testing.T) {
	f := newRevisionFixture(t)
	first := issueRevision(t, f, f.request)
	if first.Revision == 0 || first.ModeVersion != f.request.ExpectedModeVersion || first.IntentID != f.expected.IntentID || first.Scope != f.expected.Scope {
		t.Fatal("SQL issued metadata mismatch")
	}
	if retry := issueRevision(t, f, f.request); retry != first {
		t.Fatal("exact operation changed receipt")
	}
	got, err := f.store.Lookup(t.Context(), f.request)
	if err != nil || got != first {
		t.Fatal("exact historical lookup")
	}
	other := f.request
	other.OperationRef = "operation:" + uuid.NewString()
	if got := issueRevision(t, f, other); got.Revision != first.Revision || got.BindingDigest != first.BindingDigest {
		t.Fatal("same initial observation reissued revision")
	}
	revisionCounts(t, f, 1, 2)
}

func TestRevisionDistinctExecutionCanOwnNewObservation(t *testing.T) {
	f := newRevisionFixture(t)
	first := issueRevision(t, f, f.request)
	c := f.expected
	c.Scope.RunRef = "synthetic:" + uuid.NewString()
	collected, err := Capture(c, revisionInput(c, time.Second), []string{"note"})
	if err != nil {
		t.Fatal(err)
	}
	r := f.request
	r.OperationRef = "operation:" + uuid.NewString()
	r.Collected = collected
	next := issueRevision(t, f, r)
	if next.Revision == first.Revision || next.Scope.RunRef != c.Scope.RunRef {
		t.Fatal("distinct authorized execution was conflated with earlier initial")
	}
}

func TestRevisionStoredBindingExcludesRawObservationAndPolicy(t *testing.T) {
	f := newRevisionFixture(t)
	out := issueRevision(t, f, f.request)
	var raw []byte
	if err := f.d.QueryRow(`SELECT binding FROM evidence_revision_initials WHERE revision=$1`, int64(out.Revision)).Scan(&raw); err != nil {
		t.Fatal("stored binding")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 6 {
		t.Fatal("unexpected stored binding shape")
	}
	for _, name := range []string{"Version", "Expected", "EvidenceDigest", "Owner", "Cross", "CanaryFields"} {
		if _, ok := fields[name]; !ok {
			t.Fatal("stored binding is not the ref/digest envelope")
		}
	}
	for _, forbidden := range []string{"ExpectedAccess", "ValidFrom", "RequestWritten", "AdmittedAt", "ReasonCodes", "Headers", "Body"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("raw observation/policy material persisted")
		}
	}
}

func TestRevisionBindingAndNaturalIdentityConflict(t *testing.T) {
	for _, name := range []string{"observation", "case", "intent", "resource", "reset", "policy", "fields", "same-operation-other-run", "cross-task-run-reuse"} {
		t.Run(name, func(t *testing.T) {
			f := newRevisionFixture(t)
			issueRevision(t, f, f.request)
			r := f.request
			r.OperationRef = "operation:" + uuid.NewString()
			c := f.expected
			fields := []string{"note"}
			offset := time.Duration(0)
			switch name {
			case "observation":
				offset = time.Second
			case "case":
				c.Scope.CaseRef = "case:other"
			case "intent":
				id, err := f.d.Exploration(r.ExpectedExplorationID).AddIntent(map[string]any{"description": "second synthetic intent"}, 1, nil, "test")
				if err != nil {
					t.Fatal(err)
				}
				c.IntentID = id
			case "resource":
				c.ResourceRef = "document:other"
			case "reset":
				c.Scope.ResetRef = "reset:other"
			case "policy":
				c.KGPolicyVersion = "kg:other"
			case "fields":
				fields = []string{"note", "other"}
			case "same-operation-other-run":
				r.OperationRef = f.request.OperationRef
				c.Scope.RunRef = "synthetic:" + uuid.NewString()
			case "cross-task-run-reuse":
				second := newRevisionFixture(t)
				r = second.request
				c = second.expected
				c.Scope.RunRef = f.expected.Scope.RunRef
			}
			collected, err := Capture(c, revisionInput(c, offset), fields)
			if err != nil {
				t.Fatal(err)
			}
			r.Collected = collected
			if got, err := f.store.Issue(t.Context(), r); !errors.Is(err, ErrConflict) || got != (Receipt{}) {
				t.Fatalf("changed initial binding was accepted: %v", err)
			}
			revisionCounts(t, f, 1, 1)
		})
	}
}

func TestRevisionCurrentModeAndSQLFacts(t *testing.T) {
	for _, name := range []string{"exploration", "mode-version", "wrong-intent-owner", "non-intent", "missing-task", "deleted-task", "clear-marker", "missing-marker"} {
		t.Run(name, func(t *testing.T) {
			f := newRevisionFixture(t)
			r := f.request
			c := f.expected
			switch name {
			case "exploration":
				r.ExpectedExplorationID++
			case "mode-version":
				r.ExpectedModeVersion++
			case "wrong-intent-owner":
				other := newRevisionFixture(t)
				c.IntentID = other.expected.IntentID
			case "non-intent":
				if _, err := f.d.Exec(`UPDATE exploration_nodes SET kind='fact',state='origin' WHERE id=$1`, c.IntentID); err != nil {
					fixtureError(t, "node kind", err)
				}
			case "missing-task":
				c.Scope.TaskRef = "9223372036854775807"
			case "deleted-task":
				taskID, exploration, intent := ordinaryRevisionTask(t, f.d)
				if _, err := f.d.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, taskID); err != nil {
					fixtureError(t, "deleted ordinary task", err)
				}
				c.Scope.TaskRef = strconv.FormatInt(taskID, 10)
				c.IntentID = intent
				r.ExpectedExplorationID = exploration
			case "clear-marker":
				if err := os.WriteFile(f.scope.markerPath, []byte("clear"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-marker":
				if err := os.Remove(f.scope.markerPath); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			r.Collected, err = Capture(c, revisionInput(c, 0), []string{"note"})
			if err != nil {
				t.Fatal(err)
			}
			if got, err := f.store.Issue(t.Context(), r); err == nil || got != (Receipt{}) {
				t.Fatal("invalid current SQL/marker binding issued revision")
			}
			revisionCounts(t, f, 0, 0)
		})
	}
}
func mustTask(t *testing.T, c evidencebind.Context) int64 {
	t.Helper()
	id, err := strconv.ParseInt(c.Scope.TaskRef, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRevisionHistoricalLookupAfterMarkerLoss(t *testing.T) {
	f := newRevisionFixture(t)
	first := issueRevision(t, f, f.request)
	if err := os.Remove(f.scope.markerPath); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.Issue(t.Context(), f.request); !errors.Is(err, ErrMode) || got != (Receipt{}) {
		t.Fatal("old receipt became current issue permission")
	}
	if got, err := f.store.Lookup(t.Context(), f.request); err != nil || got != first {
		t.Fatal("current authorized reader could not reconcile historical outcome")
	}
	f.scope.deny = ReadReceipt
	if got, err := f.store.Lookup(t.Context(), f.request); !errors.Is(err, ErrScope) || got != (Receipt{}) {
		t.Fatal("historical read bypassed independent reader authority")
	}
	revisionCounts(t, f, 1, 1)
}

func TestRevisionArchiveFailureUsesActualApplicationEnum(t *testing.T) {
	for _, state := range []string{db.ArchiveFailed, db.ArchiveQueued} {
		t.Run(state, func(t *testing.T) {
			f := newRevisionFixture(t)
			// Existing controlled-task guards reject removal/archive. Build legal
			// pre-registration history instead of disabling those database guards.
			taskID, exploration, intent := ordinaryRevisionTask(t, f.d)
			if _, err := f.d.Exec(`INSERT INTO task_archives(task_id,state) VALUES($1,$2)`, taskID, state); err != nil {
				fixtureError(t, "ordinary archive state", err)
			}
			r := f.request
			c := f.expected
			c.Scope.TaskRef = strconv.FormatInt(taskID, 10)
			c.IntentID = intent
			r.ExpectedExplorationID = exploration
			if state == db.ArchiveFailed {
				record, err := f.d.RegisterTaskMode(t.Context(), taskID, taskmode.ModeControlled, nil)
				if err != nil {
					t.Fatal("failed archive registration")
				}
				r.ExpectedModeVersion = record.Version
			}
			var err error
			r.Collected, err = Capture(c, revisionInput(c, 0), []string{"note"})
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.store.Issue(t.Context(), r)
			if state == db.ArchiveFailed {
				if err != nil || out.Revision == 0 {
					t.Fatal("failed archive did not preserve live-task behavior")
				}
			} else if !errors.Is(err, ErrBinding) || out != (Receipt{}) {
				t.Fatal("active archive issued new observation revision")
			}
		})
	}
}

func ordinaryRevisionTask(t *testing.T, d *db.DB) (int64, int64, int64) {
	t.Helper()
	task, err := d.CreateTask("synthetic ordinary task", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal("ordinary fixture task")
	}
	intent, err := d.Exploration(task.ExplorationID).AddIntent(map[string]any{"description": "synthetic intent"}, 1, nil, "test")
	if err != nil {
		t.Fatal("ordinary fixture intent")
	}
	return task.ID, task.ExplorationID, intent
}

func TestRevisionScopeCallbacksBeforeSQLLocks(t *testing.T) {
	f := newRevisionFixture(t)
	f.scope.hook = func(a Access) {
		tx, err := f.d.Begin()
		if err != nil {
			t.Fatal("probe transaction")
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`SELECT id FROM tasks WHERE id=$1 FOR UPDATE NOWAIT`, a.TaskID); err != nil {
			t.Error("scope acquisition ran under a task row lock")
		}
	}
	issueRevision(t, f, f.request)
}

func TestRevisionConcurrentSameInitialAndOperation(t *testing.T) {
	for _, name := range []string{"same-op", "new-op", "different-binding"} {
		t.Run(name, func(t *testing.T) {
			f := newRevisionFixture(t)
			r1, r2 := f.request, f.request
			if name != "same-op" {
				r2.OperationRef = "operation:" + uuid.NewString()
			}
			if name == "different-binding" {
				var err error
				r2.Collected, err = Capture(f.expected, revisionInput(f.expected, time.Second), []string{"note"})
				if err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}, 2), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			f.scope.hook = func(Access) { entered <- struct{}{}; <-release }
			type result struct {
				r Receipt
				e error
			}
			results := make(chan result, 2)
			go func() { r, e := f.store.Issue(t.Context(), r1); results <- result{r, e} }()
			go func() { r, e := f.store.Issue(t.Context(), r2); results <- result{r, e} }()
			for i := 0; i < 2; i++ {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("concurrent requests missed scope barrier")
				}
			}
			unblock()
			a, b := <-results, <-results
			if name == "different-binding" {
				if (a.e == nil) == (b.e == nil) || a.e != nil && !errors.Is(a.e, ErrConflict) || b.e != nil && !errors.Is(b.e, ErrConflict) {
					t.Fatal("concurrent changed observations did not select one immutable initial")
				}
				revisionCounts(t, f, 1, 1)
			} else {
				if a.e != nil || b.e != nil || a.r.Revision != b.r.Revision {
					t.Fatal("concurrent retries diverged")
				}
				if name == "same-op" && a.r != b.r {
					t.Fatal("identical operation receipts diverged")
				}
				n := 1
				if name == "new-op" {
					n = 2
				}
				revisionCounts(t, f, 1, n)
			}
		})
	}
}

func TestRevisionRollbackAndCommitUnknown(t *testing.T) {
	for _, mode := range []string{"insert-failure", "commit-failure", "lost-ack"} {
		t.Run(mode, func(t *testing.T) {
			f := newRevisionFixture(t)
			if mode == "lost-ack" {
				f.store.commit = func(tx *sql.Tx) error {
					if err := tx.Commit(); err != nil {
						return err
					}
					return errors.New("synthetic lost ACK")
				}
			} else {
				if _, err := f.d.Exec(`CREATE OR REPLACE FUNCTION revision_test_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$`); err != nil {
					t.Fatal("fault fixture")
				}
				ddl := `CREATE TRIGGER revision_test_failure BEFORE INSERT ON evidence_revision_operations FOR EACH ROW EXECUTE FUNCTION revision_test_failure()`
				if mode == "commit-failure" {
					ddl = `CREATE CONSTRAINT TRIGGER revision_test_failure AFTER INSERT ON evidence_revision_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION revision_test_failure()`
				}
				if _, err := f.d.Exec(ddl); err != nil {
					t.Fatal("fault trigger")
				}
				t.Cleanup(func() {
					if _, err := f.d.Exec(`DROP TRIGGER revision_test_failure ON evidence_revision_operations`); err != nil {
						t.Error("fixture trigger cleanup")
					}
				})
			}
			got, err := f.store.Issue(t.Context(), f.request)
			want := ErrOutcomeUnknown
			if mode == "insert-failure" {
				want = ErrStore
			}
			if !errors.Is(err, want) || got != (Receipt{}) {
				t.Fatal("failed/unknown commit returned issuance")
			}
			if mode == "lost-ack" {
				revisionCounts(t, f, 1, 1)
				if err := os.Remove(f.scope.markerPath); err != nil {
					t.Fatal(err)
				}
				saved, err := f.store.Lookup(t.Context(), f.request)
				if err != nil || saved.Revision == 0 {
					t.Fatal("unknown commit outcome could not be reconciled after mode loss")
				}
				if _, err := f.store.Issue(t.Context(), f.request); !errors.Is(err, ErrMode) {
					t.Fatal("unknown commit replay granted current issuance")
				}
			} else {
				revisionCounts(t, f, 0, 0)
				if got, err := f.store.Lookup(t.Context(), f.request); !errors.Is(err, ErrNotFound) || got != (Receipt{}) {
					t.Fatal("rollback left a partial receipt")
				}
			}
		})
	}
}

func TestRevisionImmutableRowsAndDurableCommit(t *testing.T) {
	f := newRevisionFixture(t)
	f.store.commit = func(tx *sql.Tx) error {
		var mode string
		if err := tx.QueryRow(`SHOW synchronous_commit`).Scan(&mode); err != nil || mode != "on" {
			t.Error("unsafe commit acknowledgement")
		}
		return tx.Commit()
	}
	first := issueRevision(t, f, f.request)
	for _, table := range []string{"evidence_revision_initials", "evidence_revision_operations"} {
		if _, err := f.d.Exec(`DELETE FROM `+table+` WHERE revision=$1`, int64(first.Revision)); err == nil {
			t.Fatal("immutable history deletion accepted")
		}
	}
	revisionCounts(t, f, 1, 1)
}

func TestRevisionCaptureRejectsFalseBindingsAndJSON(t *testing.T) {
	f := newRevisionFixture(t)
	bad := f.input
	bad.Owner.Receipt.RequestWritten = false
	if _, err := Capture(f.expected, bad, []string{"note"}); !errors.Is(err, ErrInput) {
		t.Fatal("incomplete receipt captured")
	}
	for _, fields := range [][]string{nil, {"note", "note"}, {"bad\nfield"}} {
		if _, err := Capture(f.expected, f.input, fields); !errors.Is(err, ErrInput) {
			t.Fatal("invalid canary field names")
		}
	}
	encoded, err := json.Marshal(f.request.Collected)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("collected payload exposed to JSON")
	}
	var decoded Collected
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	r := f.request
	r.Collected = decoded
	if got, err := f.store.Issue(t.Context(), r); !errors.Is(err, ErrInput) || got != (Receipt{}) {
		t.Fatal("JSON reconstruction became collector metadata")
	}
	revisionCounts(t, f, 0, 0)
}

func TestRevisionLeaseCancellationAndExactLookupConflict(t *testing.T) {
	f := newRevisionFixture(t)
	issueRevision(t, f, f.request)
	r := f.request
	r.ExpectedModeVersion++
	if got, err := f.store.Lookup(t.Context(), r); !errors.Is(err, ErrConflict) || got != (Receipt{}) {
		t.Fatal("historical lookup accepted different exact request")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	f.scope.ctx = ctx
	if got, err := f.store.Lookup(t.Context(), f.request); !errors.Is(err, ErrScope) || got != (Receipt{}) {
		t.Fatal("revoked reader lease accepted")
	}
}

func fixtureError(t *testing.T, stage string, err error) {
	t.Helper()
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		t.Fatalf("fixture %s code=%s constraint=%s column=%s", stage, pg.Code, pg.ConstraintName, pg.ColumnName)
	}
	t.Fatalf("fixture %s failed (non-PostgreSQL error)", stage)
}
