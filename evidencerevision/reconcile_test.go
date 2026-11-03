package evidencerevision

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func describeFixture(t *testing.T, f *revisionFixture) Descriptor {
	t.Helper()
	d, err := Describe(f.request)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestReconcileLostACKAfterRequestMemoryLoss(t *testing.T) {
	f := newRevisionFixture(t)
	d := describeFixture(t, f)
	// A protected durable host must perform this serialization/storage BEFORE
	// Issue in production. This test owns its synthetic descriptor bytes only.
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	f.store.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("synthetic lost commit ACK")
	}
	if out, err := f.store.Issue(t.Context(), f.request); !errors.Is(err, ErrOutcomeUnknown) || out != (Receipt{}) {
		t.Fatal("unknown commit returned issuance")
	}
	if err := os.Remove(f.scope.markerPath); err != nil {
		t.Fatal(err)
	}
	f.request = Request{}
	f.input = evidencebind.Input{}
	f.expected = evidencebind.Context{}
	var restored Descriptor
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(f.d.DB, f.scope)
	if err != nil {
		t.Fatal(err)
	}
	restarted.commit = func(*sql.Tx) error { t.Fatal("Reconcile tried to commit/write"); return ErrStore }
	var before, after int64
	if err := f.d.QueryRow(`SELECT last_value FROM evidence_revision_initials_revision_seq`).Scan(&before); err != nil {
		t.Fatal("sequence before read")
	}
	var gotScope bool
	f.scope.hook = func(a Access) {
		gotScope = a.Purpose == ReadReceipt && a.OperationRef == restored.OperationRef && a.Scope == restored.Scope && a.RequestDigest == restored.RequestDigest
	}
	got, err := restarted.Reconcile(t.Context(), restored)
	if err != nil || got.Revision == 0 || got.Scope != restored.Scope || got.OperationRef != restored.OperationRef || !gotScope {
		t.Fatalf("restart reconciliation failed: %v", err)
	}
	if err := f.d.QueryRow(`SELECT last_value FROM evidence_revision_initials_revision_seq`).Scan(&after); err != nil || before != after {
		t.Fatal("historical read allocated a sequence")
	}
	var revisions, operations int
	if err := f.d.QueryRow(`SELECT count(*) FROM evidence_revision_initials WHERE execution_ref=$1`, restored.Scope.RunRef).Scan(&revisions); err != nil {
		t.Fatal("initial count")
	}
	if err := f.d.QueryRow(`SELECT count(*) FROM evidence_revision_operations WHERE operation_ref=$1`, restored.OperationRef).Scan(&operations); err != nil || revisions != 1 || operations != 1 {
		t.Fatal("read changed ledger counts")
	}
	if again, err := restarted.Reconcile(t.Context(), restored); err != nil || again != got {
		t.Fatal("historical reconciliation changed receipt")
	}
}

func TestReconcileDescriptorCannotBorrowAnotherScopeWithSameDigest(t *testing.T) {
	for _, field := range []string{"run", "case", "reset", "policy", "intent", "task", "exploration", "mode"} {
		t.Run(field, func(t *testing.T) {
			f := newRevisionFixture(t)
			issueRevision(t, f, f.request)
			d := describeFixture(t, f)
			digest := d.RequestDigest
			switch field {
			case "run":
				d.Scope.RunRef = "run:other"
			case "case":
				d.Scope.CaseRef = "case:other"
			case "reset":
				d.Scope.ResetRef = "reset:other"
			case "policy":
				d.Scope.PolicyRef = "policy:other"
			case "intent":
				d.IntentID++
			case "task":
				d.TaskID++
				d.Scope.TaskRef = strconv.FormatInt(d.TaskID, 10)
			case "exploration":
				d.ExplorationID++
			case "mode":
				d.ModeVersion++
			}
			if d.RequestDigest != digest {
				t.Fatal("test altered digest instead of scope")
			}
			if got, err := f.store.Reconcile(t.Context(), d); !errors.Is(err, ErrConflict) || got != (Receipt{}) {
				t.Fatal("descriptor borrowed different stored scope")
			}
			revisionCounts(t, f, 1, 1)
		})
	}
}

func TestReconcileWrongOperationDigestAndReaderRefusal(t *testing.T) {
	for _, mode := range []string{"missing-op", "different-op", "digest", "reader-denied", "reader-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newRevisionFixture(t)
			issueRevision(t, f, f.request)
			d := describeFixture(t, f)
			want := ErrConflict
			switch mode {
			case "missing-op":
				d.OperationRef = "operation:" + uuid.NewString()
				want = ErrNotFound
			case "different-op":
				other := newRevisionFixture(t)
				issueRevision(t, other, other.request)
				d.OperationRef = other.request.OperationRef
			case "digest":
				d.RequestDigest[0] ^= 1
			case "reader-denied":
				f.scope.deny = ReadReceipt
				want = ErrScope
			case "reader-cancelled":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				f.scope.ctx = ctx
				want = ErrScope
			}
			if got, err := f.store.Reconcile(t.Context(), d); !errors.Is(err, want) || got != (Receipt{}) {
				t.Fatalf("invalid reconciliation accepted: %v", err)
			}
			revisionCounts(t, f, 1, 1)
		})
	}
}

func TestReconcileChecksActualSQLIntentOwnership(t *testing.T) {
	f := newRevisionFixture(t)
	issueRevision(t, f, f.request)
	d := describeFixture(t, f)
	_, otherExploration, _ := ordinaryRevisionTask(t, f.d)
	if _, err := f.d.Exec(`UPDATE exploration_nodes SET exploration_id=$1 WHERE id=$2`, otherExploration, d.IntentID); err != nil {
		fixtureError(t, "change owned intent relation", err)
	}
	if got, err := f.store.Reconcile(t.Context(), d); !errors.Is(err, ErrBinding) || got != (Receipt{}) {
		t.Fatal("stored receipt bypassed actual SQL ownership")
	}
}

func TestReconcileRecomputesStoredBindingDigest(t *testing.T) {
	f := newRevisionFixture(t)
	issueRevision(t, f, f.request)
	d := describeFixture(t, f)
	row, err := readReconciliationRow(t.Context(), f.d.DB, d.OperationRef)
	if err != nil {
		t.Fatal(err)
	}
	var b reconciliationBinding
	if err := json.Unmarshal(row.binding, &b); err != nil {
		t.Fatal(err)
	}
	b.CanaryFields = append(b.CanaryFields, "synthetic-corruption")
	corrupt, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	// Only this test-owned schema projects a damaged immutable row. Production
	// public tables, triggers and constraints remain intact and enabled.
	schemaName := "reconcile_projection_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := f.d.Exec(`CREATE SCHEMA ` + schemaName); err != nil {
		t.Fatal("owned projection schema")
	}
	t.Cleanup(func() {
		if _, err := f.d.Exec(`DROP SCHEMA ` + schemaName + ` CASCADE`); err != nil {
			t.Error("owned projection cleanup")
		}
	})
	view := `CREATE VIEW ` + schemaName + `.evidence_revision_initials AS SELECT i.revision,i.execution_ref,i.purpose,i.task_id,i.exploration_id,i.intent_id,i.mode_version,decode('` + hex.EncodeToString(corrupt) + `','hex') AS binding,i.input_digest,i.evidence_digest,i.issued_at FROM public.evidence_revision_initials i`
	if _, err := f.d.Exec(view); err != nil {
		t.Fatal("owned binding projection")
	}
	if _, err := f.d.Exec(`CREATE VIEW ` + schemaName + `.evidence_revision_operations AS SELECT * FROM public.evidence_revision_operations`); err != nil {
		t.Fatal("owned operation projection")
	}
	config, err := pgx.ParseConfig(os.Getenv("ARTEX_EVIDENCE_REVISION_TEST_DSN"))
	if err != nil {
		t.Fatal("owned database configuration")
	}
	config.RuntimeParams["search_path"] = schemaName + ",public"
	projection := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = projection.Close() })
	store, err := New(projection, f.scope)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Reconcile(t.Context(), d); !errors.Is(err, ErrConflict) || got != (Receipt{}) {
		t.Fatal("matching stored digest strings bypassed binding rehash")
	}
	// The untouched real row is still a valid receipt and its trigger is intact.
	if _, err := f.store.Reconcile(t.Context(), d); err != nil {
		t.Fatal("fault projection altered real ledger")
	}
	if _, err := f.d.Exec(`UPDATE evidence_revision_initials SET mode_version=mode_version WHERE revision=$1`, row.revision); err == nil {
		t.Fatal("immutable production trigger was weakened")
	}
}

func TestReconcileDescriptorIsMinimalMetadataAndNeverIssueInput(t *testing.T) {
	f := newRevisionFixture(t)
	d := describeFixture(t, f)
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || len(fields) != 8 {
		t.Fatal("descriptor is not the minimal identity/digest envelope")
	}
	for _, name := range []string{"version", "operation_ref", "task_id", "exploration_id", "intent_id", "scope", "mode_version", "request_digest"} {
		if _, ok := fields[name]; !ok {
			t.Fatal("descriptor field missing")
		}
	}
	for _, forbidden := range []string{"Collected", "CanaryFields", "ExpectedAccess", "PolicyAssertion", "Owner", "Headers", "Body", "Facts"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("descriptor leaked observation or policy source data")
		}
	}
	var request Request
	if err := json.Unmarshal(encoded, &request); err != nil {
		t.Fatal(err)
	}
	if out, err := f.store.Issue(t.Context(), request); !errors.Is(err, ErrInput) || out != (Receipt{}) {
		t.Fatal("descriptor became collected/Issue authority")
	}
	for _, mutate := range []func(*Descriptor){func(d *Descriptor) { d.Version++ }, func(d *Descriptor) { d.RequestDigest = [32]byte{} }, func(d *Descriptor) { d.TaskID = 0 }} {
		bad := d
		mutate(&bad)
		if out, err := f.store.Reconcile(t.Context(), bad); !errors.Is(err, ErrInput) || out != (Receipt{}) {
			t.Fatal("invalid descriptor accepted")
		}
	}
	revisionCounts(t, f, 0, 0)
}
