package evidencerevision

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestReconcileWaitsForPendingIssue(t *testing.T) {
	for _, mode := range []string{"commit", "rollback", "cancel-reader"} {
		t.Run(mode, func(t *testing.T) {
			f := newRevisionFixture(t)
			d := describeFixture(t, f)
			entered, release, issueDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			f.store.commit = func(tx *sql.Tx) error {
				close(entered)
				<-release
				if mode == "rollback" {
					return errors.New("synthetic rollback before commit")
				}
				return tx.Commit()
			}
			type result struct {
				receipt Receipt
				err     error
			}
			issued := make(chan result, 1)
			go func() {
				defer close(issueDone)
				r, err := f.store.Issue(t.Context(), f.request)
				issued <- result{r, err}
			}()
			t.Cleanup(func() {
				unblock()
				select {
				case <-issueDone:
				case <-time.After(5 * time.Second):
					t.Error("issuer did not settle")
				}
			})
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("issuer missed commit barrier")
			}
			cfg, err := pgx.ParseConfig(os.Getenv("ARTEX_EVIDENCE_REVISION_TEST_DSN"))
			if err != nil {
				t.Fatal("dedicated reader configuration unavailable")
			}
			app := "reconcile-wait-" + uuid.NewString()
			cfg.RuntimeParams["application_name"] = app
			pool := stdlib.OpenDB(*cfg)
			t.Cleanup(func() { _ = pool.Close() })
			reader, err := New(pool, f.scope)
			if err != nil {
				t.Fatal(err)
			}
			reader.commit = func(*sql.Tx) error { t.Error("historical read tried to commit"); return ErrStore }
			ctx, cancel := context.WithCancel(t.Context())
			readDone, reconciled := make(chan struct{}), make(chan result, 1)
			go func() { defer close(readDone); r, err := reader.Reconcile(ctx, d); reconciled <- result{r, err} }()
			t.Cleanup(func() {
				cancel()
				unblock()
				select {
				case <-readDone:
				case <-time.After(5 * time.Second):
					t.Error("reader did not settle")
				}
			})
			// Observe the actual PostgreSQL lock wait, rather than assuming a
			// goroutine has reached its query after an arbitrary sleep.
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			deadline := time.After(5 * time.Second)
		waiting:
			for {
				select {
				case <-reconciled:
					t.Fatal("reader returned before pending issuance settled")
				case <-deadline:
					t.Fatal("reader did not wait on the issuance task lock")
				case <-ticker.C:
					var locked bool
					if err := f.d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, app).Scan(&locked); err != nil {
						t.Fatal("lock observation unavailable")
					}
					if locked {
						break waiting
					}
				}
			}
			if mode == "cancel-reader" {
				cancel()
			} else {
				unblock()
			}
			var got result
			select {
			case got = <-reconciled:
			case <-time.After(5 * time.Second):
				t.Fatal("reader did not finish")
			}
			unblock()
			var actual result
			select {
			case actual = <-issued:
			case <-time.After(5 * time.Second):
				t.Fatal("issuer did not finish")
			}
			switch mode {
			case "commit":
				if actual.err != nil || got.err != nil || got.receipt.Revision == 0 || got.receipt.Revision != actual.receipt.Revision || got.receipt.Scope != d.Scope || got.receipt.BindingDigest != d.RequestDigest {
					t.Fatal("committed operation was not reconciled exactly")
				}
				revisionCounts(t, f, 1, 1)
			case "rollback":
				if !errors.Is(actual.err, ErrOutcomeUnknown) || !errors.Is(got.err, ErrNotFound) || got.receipt != (Receipt{}) {
					t.Fatal("rollback was not observed after issuance settled")
				}
				revisionCounts(t, f, 0, 0)
			case "cancel-reader":
				if actual.err != nil || !errors.Is(got.err, ErrScope) || got.receipt != (Receipt{}) {
					t.Fatal("cancelled reader did not release its wait")
				}
				revisionCounts(t, f, 1, 1)
			}
		})
	}
}

func TestReconcileRejectsChangedIntentKind(t *testing.T) {
	f := newRevisionFixture(t)
	issueRevision(t, f, f.request)
	d := describeFixture(t, f)
	if _, err := f.d.Exec(`UPDATE exploration_nodes SET kind='fact',state='origin' WHERE id=$1`, d.IntentID); err != nil {
		fixtureError(t, "change historical intent kind", err)
	}
	if got, err := f.store.Reconcile(t.Context(), d); !errors.Is(err, ErrBinding) || got != (Receipt{}) {
		t.Fatal("historical receipt bypassed current intent kind")
	}
	revisionCounts(t, f, 1, 1)
}
