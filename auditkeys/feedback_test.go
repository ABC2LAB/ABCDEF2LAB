package auditkeys

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func requireRegistryConstraint(t *testing.T, err error, reason string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != reason {
		t.Fatalf("expected SQLSTATE 23514 / %s; got %v", reason, err)
	}
}

func TestRegistrySQLMappingGuardIndependentOfTransition(t *testing.T) {
	f := registryFixture(t)
	f.prepare(t)
	if _, err := f.store.Prepare(context.Background(), "key-2", "version-2", "", "prepare-2"); err != nil {
		t.Fatal(err)
	}
	for name, assignment := range map[string]string{
		"namespace":      "namespace='replacement'",
		"key":            "key_ref='replacement'",
		"secret-version": "secret_version_ref='replacement'",
		"identity":       "secret_identity='replacement'",
		"predecessor":    "predecessor_ref='key-2'",
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := f.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// Active/revision=2 is a legal transition; its guard cannot mask
			// deletion of the immutable-mapping comparison.
			_, err = tx.Exec("UPDATE audit_key_registry_keys SET " + assignment + ",state='active',revision=revision+1 WHERE namespace='engine-audit' AND key_ref='key-1'")
			requireRegistryConstraint(t, err, "audit_key_registry_immutable_mapping")
		})
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE audit_key_registry_keys SET state='active',revision=revision+1 WHERE namespace='engine-audit' AND key_ref='key-1'")
	if err != nil {
		t.Fatalf("otherwise-identical legal transition rejected: %v", err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatal("legal control did not update exactly one row")
	}
}

func TestRegistrySQLInitialAndTransitionGuards(t *testing.T) {
	f := registryFixture(t)
	for _, tc := range []struct {
		name, state string
		revision    int
		valid       bool
	}{
		{"legal-prepared", "prepared", 1, true},
		{"initial-active", "active", 1, false},
		{"initial-revision", "prepared", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := f.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = tx.Exec(`INSERT INTO audit_key_registry_keys(namespace,key_ref,secret_version_ref,secret_identity,state,revision)
VALUES('engine-audit','raw-key','raw-version','raw-identity',$1,$2)`, tc.state, tc.revision)
			if tc.valid {
				if err != nil {
					t.Fatalf("valid initial insert rejected: %v", err)
				}
			} else {
				requireRegistryConstraint(t, err, "audit_key_registry_initial_state")
			}
		})
	}
	f.prepare(t)
	t.Run("revision-jump", func(t *testing.T) {
		_, err := f.db.Exec("UPDATE audit_key_registry_keys SET state='active',revision=3 WHERE key_ref='key-1'")
		requireRegistryConstraint(t, err, "audit_key_registry_transition")
	})
	f.change(t, 1, Active, "activate")
	f.change(t, 2, Retiring, "rotate")
	f.change(t, 3, Retired, "retire")
	t.Run("retired-reactivation", func(t *testing.T) {
		_, err := f.db.Exec("UPDATE audit_key_registry_keys SET state='active',revision=5 WHERE key_ref='key-1'")
		requireRegistryConstraint(t, err, "audit_key_registry_transition")
	})
	// The rejected statements preserve the original row/history/receipts, and
	// the same next revision remains legal for a permitted transition.
	f.change(t, 4, Destroyed, "destroy")
	requireCounts(t, f, 1, 5, 5)
}

func independentRegistryStore(t *testing.T, f *fixture) *Store {
	t.Helper()
	other, err := New(f.db, f.store.config, &syntheticAuthority{identities: map[string]string{"version-1": "synthetic-identity-1"}})
	if err != nil {
		t.Fatal(err)
	}
	return other
}

func TestRegistryCurrentChallengeRecheckedAfterAuthorityDelegation(t *testing.T) {
	f := registryFixture(t)
	f.prepare(t)
	other := independentRegistryStore(t, f)
	var advanced atomic.Int64
	f.auth.hook = func(req TrustRequest) {
		if req.Inspect {
			return
		}
		if req.CurrentRevision != 1 || req.CurrentState != Prepared || req.Command.ExpectedRevision != 2 {
			t.Fatal("fixture did not challenge the earlier prepared state")
		}
		if _, err := other.Change(context.Background(), Command{"key-1", "independent-activate", 1, Active}); err != nil {
			t.Fatal(err)
		}
		advanced.Add(1)
	}
	// ExpectedRevision deliberately matches the row AFTER authority I/O. The
	// expected-revision check alone must not accept an attestation of revision 1.
	_, err := f.store.Change(context.Background(), Command{"key-1", "stale-challenge-retire", 2, Retiring})
	if !errors.Is(err, ErrConflict) || advanced.Load() != 1 {
		t.Fatalf("stale challenge after delegation: %v / %d advances", err, advanced.Load())
	}
	f.auth.hook = nil
	rec, err := f.store.Inspect(context.Background(), "key-1")
	if err != nil || rec.Revision != 2 || rec.State != Active {
		t.Fatalf("stale authority mutated current metadata: %v", err)
	}
	requireCounts(t, f, 1, 2, 2)
	f.change(t, 2, Retiring, "fresh-challenge-retire")
	requireCounts(t, f, 1, 3, 3)
}

func TestRegistryInspectRechecksInsideTrustScope(t *testing.T) {
	f := registryFixture(t)
	f.prepare(t)
	other := independentRegistryStore(t, f)
	var advanced atomic.Int64
	f.auth.hook = func(req TrustRequest) {
		if !req.Inspect {
			return
		}
		if req.CurrentRevision != 1 || req.CurrentState != Prepared {
			t.Fatal("unexpected initial inspection challenge")
		}
		if _, err := other.Change(context.Background(), Command{"key-1", "independent-activate", 1, Active}); err != nil {
			t.Fatal(err)
		}
		advanced.Add(1)
	}
	if rec, err := f.store.Inspect(context.Background(), "key-1"); !errors.Is(err, ErrConflict) || rec != (Record{}) || advanced.Load() != 1 {
		t.Fatalf("inspection exposed state not attested in its scope: %v", err)
	}
	f.auth.hook = nil
	rec, err := f.store.Inspect(context.Background(), "key-1")
	if err != nil || rec.State != Active || rec.Revision != 2 {
		t.Fatalf("fresh inspection rejected: %v", err)
	}
	requireCounts(t, f, 1, 2, 2)
}

func TestRegistryOperationReceiptBindsAttestationEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Attestation)
	}{
		{"actor", func(a *Attestation) { a.ActorRef = "other-operator" }},
		{"policy-revision", func(a *Attestation) { a.PolicyRevision++ }},
		{"mapping", func(a *Attestation) { a.MappingRef = "other-mapping" }},
		{"authorization", func(a *Attestation) { a.AuthorizationRef = "other-authorization" }},
		{"activation", func(a *Attestation) { a.ActivationRef = "other-activation" }},
		{"drain", func(a *Attestation) { a.DrainRef = "other-drain" }},
		{"dependency", func(a *Attestation) { a.DependencyRef = "other-dependency" }},
		{"copies", func(a *Attestation) { a.CopyLifetimeRef = "other-copies" }},
		{"disposal", func(a *Attestation) { a.DisposalRef = "other-disposal" }},
		{"unused", func(a *Attestation) { a.UnusedRef = "other-unused" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := registryFixture(t)
			f.prepare(t)
			cmd := Command{"key-1", "activate", 1, Active}
			f.change(t, 1, Active, "activate")
			replay, err := f.store.Change(context.Background(), cmd)
			if err != nil || !replay.Replayed || replay.Revision != 2 {
				t.Fatalf("exact replay rejected: %v", err)
			}
			f.auth.alter = tc.change
			if out, err := f.store.Change(context.Background(), cmd); !errors.Is(err, ErrConflict) || out != (Outcome{}) {
				t.Fatalf("changed evidence replayed original receipt: %v", err)
			}
			requireCounts(t, f, 1, 2, 2)
			f.auth.alter = nil
			replay, err = f.store.Change(context.Background(), cmd)
			if err != nil || !replay.Replayed || replay.Current.State != Active {
				t.Fatalf("rejection changed the original receipt: %v", err)
			}
		})
	}
}

type registryAuthorityFunc func(context.Context, TrustRequest, func(Attestation) error) error

func (f registryAuthorityFunc) WithTrust(ctx context.Context, req TrustRequest, consume func(Attestation) error) error {
	return f(ctx, req, consume)
}

func TestRegistryAsyncConsumeCannotReturnSuccessBeforeWorkFinishes(t *testing.T) {
	for _, mutation := range []bool{false, true} {
		name, want := "inspect", ErrTrust
		if mutation {
			name, want = "mutation", ErrOutcomeUnknown
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			var calls atomic.Int64
			s := &Store{authority: registryAuthorityFunc(func(ctx context.Context, req TrustRequest, consume func(Attestation) error) error {
				go func() {
					done <- consume(Attestation{Request: req, Identity: "identity", ActorRef: "operator", PolicyRevision: 1, MappingRef: "mapping", AuthorizationRef: "authorization"})
				}()
				<-entered
				return nil // Deliberately violates the synchronous provider contract.
			})}
			defer func() {
				close(release)
				if err := <-done; !errors.Is(err, ErrTrust) {
					t.Errorf("unfinished work did not observe scope cancellation: %v", err)
				}
			}()
			committed, err := s.withTrust(context.Background(), TrustRequest{}, mutation, func(ctx context.Context, att Attestation) (bool, error) {
				calls.Add(1)
				close(entered)
				<-release
				if ctx.Err() == nil {
					return false, errors.New("scope not cancelled")
				}
				return false, ErrTrust
			})
			if committed || !errors.Is(err, want) || calls.Load() != 1 {
				t.Fatalf("unfinished callback reported success: committed=%v err=%v calls=%d", committed, err, calls.Load())
			}
		})
	}
}

func TestRegistryRefURLAndByteLimits(t *testing.T) {
	for _, tc := range []struct {
		name, ref string
		valid     bool
	}{
		{"opaque-colon", "opaque:ref", true},
		{"max", strings.Repeat("x", 128), true},
		{"over-max", strings.Repeat("x", 129), false},
		{"url", "https://example", false},
		{"scheme-separator", "a://b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if validRef(tc.ref) != tc.valid {
				t.Fatal("reference boundary mismatch")
			}
		})
	}
}

func TestRegistryCommitFailureIsOutcomeUnknown(t *testing.T) {
	f := registryFixture(t)
	// A deferred constraint trigger fails at tx.Commit, rather than at an
	// intermediate write or in the provider after an acknowledged commit.
	_, err := f.db.Exec(`CREATE FUNCTION reject_test_commit() RETURNS trigger AS $$ BEGIN
RAISE EXCEPTION 'synthetic commit rejection'; END; $$ LANGUAGE plpgsql;
CREATE CONSTRAINT TRIGGER reject_commit AFTER INSERT ON audit_key_registry_operations
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_test_commit();`)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := f.store.Prepare(context.Background(), "key-1", "version-1", "", "prepare-1"); !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, ErrStore) || out != (Outcome{}) {
		t.Fatalf("commit-boundary error reported as known success/rollback: %v", err)
	}
	requireCounts(t, f, 0, 0, 0)
	if _, err := f.db.Exec("DROP TRIGGER reject_commit ON audit_key_registry_operations"); err != nil {
		t.Fatal(err)
	}
	if out, err := f.store.Prepare(context.Background(), "key-1", "version-1", "", "prepare-1"); err != nil || out.Replayed || out.Revision != 1 {
		t.Fatalf("same operation could not reconcile after known test rollback: %v", err)
	}
	requireCounts(t, f, 1, 1, 1)
}
