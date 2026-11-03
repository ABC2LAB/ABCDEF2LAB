package auditkeys

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// This synthetic authority owns an explicit mapping and a read lease across
// consume. It is not a production secret provider or host-authentication policy.
type syntheticAuthority struct {
	mu            sync.RWMutex
	identities    map[string]string
	unavailable   bool
	before, after error
	hook          func(TrustRequest)
	alter         func(*Attestation)
	calls         atomic.Int64
}

func (a *syntheticAuthority) WithTrust(ctx context.Context, req TrustRequest, consume func(Attestation) error) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	a.calls.Add(1)
	if ctx.Err() != nil || a.before != nil {
		return errors.New("synthetic trust denied")
	}
	identity := a.identities[req.Binding.SecretVersionRef]
	if identity == "" || a.unavailable && !req.Inspect && req.Command.Next == Active {
		return ErrTrust
	}
	att := Attestation{Request: req, Identity: identity, ActorRef: "operator", PolicyRevision: 1,
		MappingRef: "mapping:" + req.Binding.SecretVersionRef, AuthorizationRef: "authorization:" + req.Command.OperationRef,
		ActivationRef: "activation:" + req.Command.OperationRef, DrainRef: "drain:" + req.Command.OperationRef,
		DependencyRef: "dependency:" + req.Command.OperationRef, CopyLifetimeRef: "copies:" + req.Command.OperationRef,
		DisposalRef: "disposal:" + req.Command.OperationRef, UnusedRef: "unused:" + req.Command.OperationRef}
	if a.hook != nil {
		a.hook(req)
	}
	if a.alter != nil {
		a.alter(&att)
	}
	if err := consume(att); err != nil {
		return err
	}
	return a.after
}
func (a *syntheticAuthority) update(fn func()) { a.mu.Lock(); defer a.mu.Unlock(); fn() }

type fixture struct {
	db     *sql.DB
	store  *Store
	auth   *syntheticAuthority
	schema string
}

func registryFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("ARTEX_AUDIT_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated PostgreSQL fixture unavailable")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("dedicated PostgreSQL configuration rejected")
	}
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := "auditkeys_test_" + hex.EncodeToString(suffix[:])
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal("could not create owned test schema")
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	auth := &syntheticAuthority{identities: map[string]string{"version-1": "synthetic-identity-1", "version-2": "synthetic-identity-2"}}
	store, err := New(db, Config{Namespace: "engine-audit", PolicyRef: "host-policy"}, auth)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{db, store, auth, schema}
}
func (f *fixture) prepare(t *testing.T) Outcome {
	t.Helper()
	out, err := f.store.Prepare(context.Background(), "key-1", "version-1", "", "prepare-1")
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func (f *fixture) change(t *testing.T, expected uint64, state State, op string) Outcome {
	t.Helper()
	out, err := f.store.Change(context.Background(), Command{"key-1", op, expected, state})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func (f *fixture) counts(t *testing.T) (int, int, int) {
	t.Helper()
	var keys, history, ops int
	if err := f.db.QueryRow("SELECT count(*) FROM audit_key_registry_keys").Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT count(*) FROM audit_key_registry_history").Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT count(*) FROM audit_key_registry_operations").Scan(&ops); err != nil {
		t.Fatal(err)
	}
	return keys, history, ops
}
func requireCounts(t *testing.T, f *fixture, wantKeys, wantHistory, wantOps int) {
	t.Helper()
	k, h, o := f.counts(t)
	if k != wantKeys || h != wantHistory || o != wantOps {
		t.Fatalf("metadata counts %d/%d/%d, want %d/%d/%d", k, h, o, wantKeys, wantHistory, wantOps)
	}
}

func TestRegistryLifecycle(t *testing.T) {
	f := registryFixture(t)
	if out := f.prepare(t); out.State != Prepared || out.Revision != 1 || out.Current.State != Prepared {
		t.Fatal("prepare promoted metadata")
	}
	f.change(t, 1, Active, "activate")
	f.change(t, 2, Retiring, "rotate")
	f.change(t, 3, Retired, "retain")
	f.change(t, 4, Destroyed, "destroy")
	rec, err := f.store.Inspect(context.Background(), "key-1")
	if err != nil || rec.State != Destroyed || rec.Revision != 5 {
		t.Fatalf("final metadata mismatch %v", err)
	}
	requireCounts(t, f, 1, 5, 5)
	if err := Migrate(context.Background(), f.db); err != nil {
		t.Fatal(err)
	}
	requireCounts(t, f, 1, 5, 5)
}

func TestRegistryTransitionMatrix(t *testing.T) {
	allowedPairs := map[[2]State]bool{
		{Prepared, Active}: true, {Prepared, Compromised}: true, {Prepared, Destroyed}: true,
		{Active, Retiring}: true, {Active, Compromised}: true,
		{Retiring, Retired}: true, {Retiring, Compromised}: true,
		{Retired, Compromised}: true, {Retired, Destroyed}: true,
		{Compromised, Destroyed}: true,
	}
	states := []State{Prepared, Active, Retiring, Retired, Compromised, Destroyed}
	for _, from := range states {
		for _, to := range states {
			t.Run(string(from)+"_"+string(to), func(t *testing.T) {
				if got := allowed(from, to); got != allowedPairs[[2]State{from, to}] {
					t.Fatal("lifecycle policy mismatch")
				}
			})
		}
	}
}
func TestRegistryRejectedTransitionsStayAtomic(t *testing.T) {
	for _, next := range []State{Retiring, Retired, Prepared, State("unknown")} {
		t.Run(string(next), func(t *testing.T) {
			f := registryFixture(t)
			f.prepare(t)
			_, err := f.store.Change(context.Background(), Command{"key-1", "invalid", 1, next})
			if err == nil {
				t.Fatal("invalid transition accepted")
			}
			rec, _ := f.store.Inspect(context.Background(), "key-1")
			if rec.State != Prepared || rec.Revision != 1 {
				t.Fatal("rejected transition mutated record")
			}
			requireCounts(t, f, 1, 1, 1)
		})
	}
}
func TestRegistryAllSupportedTransitionPaths(t *testing.T) {
	paths := [][]State{{Destroyed}, {Active, Retiring, Compromised, Destroyed}, {Active, Retiring, Retired, Compromised, Destroyed}}
	for i, path := range paths {
		t.Run(fmt.Sprintf("path-%d", i), func(t *testing.T) {
			f := registryFixture(t)
			f.prepare(t)
			for step, state := range path {
				f.change(t, uint64(step+1), state, fmt.Sprintf("transition-%d", step))
			}
			requireCounts(t, f, 1, len(path)+1, len(path)+1)
		})
	}
}
func TestRegistryCompromisedAndDestroyedNeverReactivate(t *testing.T) {
	for _, state := range []State{Compromised, Destroyed} {
		t.Run(string(state), func(t *testing.T) {
			f := registryFixture(t)
			f.prepare(t)
			f.change(t, 1, state, "close")
			_, err := f.store.Change(context.Background(), Command{"key-1", "reactivate", 2, Active})
			if !errors.Is(err, ErrTransition) {
				t.Fatalf("reactivation error %v", err)
			}
			requireCounts(t, f, 1, 2, 2)
		})
	}
}
func TestRegistryActivationRequiresActualProviderEvidence(t *testing.T) {
	for _, mode := range []string{"key-unavailable", "activation-missing", "mapping-missing", "actor-missing", "policy-missing", "wrong-request", "wrong-identity", "provider-error"} {
		t.Run(mode, func(t *testing.T) {
			f := registryFixture(t)
			f.prepare(t)
			f.auth.update(func() {
				switch mode {
				case "key-unavailable":
					f.auth.unavailable = true
				case "provider-error":
					f.auth.before = ErrTrust
				default:
					f.auth.alter = func(a *Attestation) {
						switch mode {
						case "activation-missing":
							a.ActivationRef = ""
						case "mapping-missing":
							a.MappingRef = ""
						case "actor-missing":
							a.ActorRef = ""
						case "policy-missing":
							a.PolicyRevision = 0
						case "wrong-request":
							a.Request.Namespace = "elsewhere"
						case "wrong-identity":
							a.Identity = "different-identity"
						}
					}
				}
			})
			_, err := f.store.Change(context.Background(), Command{"key-1", "activate", 1, Active})
			if !errors.Is(err, ErrTrust) {
				t.Fatalf("unverified activation error %v", err)
			}
			requireCounts(t, f, 1, 1, 1)
		})
	}
}
func TestRegistryRetireDestroyRequireIndependentProofs(t *testing.T) {
	for _, missing := range []string{"drain", "dependency", "copies", "disposal", "unused"} {
		t.Run(missing, func(t *testing.T) {
			f := registryFixture(t)
			f.prepare(t)
			f.auth.alter = func(a *Attestation) {
				switch missing {
				case "drain":
					a.DrainRef = ""
				case "dependency":
					a.DependencyRef = ""
				case "copies":
					a.CopyLifetimeRef = ""
				case "disposal":
					a.DisposalRef = ""
				case "unused":
					a.UnusedRef = ""
				}
			}
			if _, err := f.store.Change(context.Background(), Command{"key-1", "destroy", 1, Destroyed}); !errors.Is(err, ErrTrust) {
				t.Fatalf("destroy proof error %v", err)
			}
			requireCounts(t, f, 1, 1, 1)
		})
	}
	f := registryFixture(t)
	f.prepare(t)
	f.change(t, 1, Active, "activate")
	f.change(t, 2, Retiring, "rotate")
	f.auth.alter = func(a *Attestation) { a.DrainRef = "" }
	if _, err := f.store.Change(context.Background(), Command{"key-1", "retire", 3, Retired}); !errors.Is(err, ErrTrust) {
		t.Fatalf("retire proof error %v", err)
	}
	requireCounts(t, f, 1, 3, 3)
}
func TestRegistryMappingAndAlias(t *testing.T) {
	f := registryFixture(t)
	f.prepare(t)
	callsBefore := f.auth.calls.Load()
	if _, err := f.store.Prepare(context.Background(), "key-1", "version-2", "", "replace"); !errors.Is(err, ErrConflict) {
		t.Fatalf("mapping replacement error %v", err)
	}
	if f.auth.calls.Load() != callsBefore {
		t.Fatal("known mapping mismatch reached authority")
	}
	f.auth.update(func() { f.auth.identities["version-alias"] = "synthetic-identity-1" })
	if _, err := f.store.Prepare(context.Background(), "alias", "version-alias", "", "alias-op"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-key alias error %v", err)
	}
	if _, err := f.store.Prepare(context.Background(), "same-version", "version-1", "", "same-version"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-version alias error %v", err)
	}
	out, err := f.store.Prepare(context.Background(), "key-2", "version-2", "key-1", "prepare-2")
	if err != nil || out.Current.PredecessorRef != "key-1" {
		t.Fatalf("new independent key/predecessor error %v", err)
	}
	requireCounts(t, f, 2, 2, 2)
}
func TestRegistrySameOperationAndContentConflict(t *testing.T) {
	f := registryFixture(t)
	first := f.prepare(t)
	replay, err := f.store.Prepare(context.Background(), "key-1", "version-1", "", "prepare-1")
	if err != nil || !replay.Replayed || replay.Revision != first.Revision {
		t.Fatalf("prepare retry error %v", err)
	}
	f.change(t, 1, Active, "activate")
	if _, err := f.store.Change(context.Background(), Command{"key-1", "activate", 1, Compromised}); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-op changed content error %v", err)
	}
	if _, err := f.store.Change(context.Background(), Command{"key-1", "old-new-op", 1, Retiring}); !errors.Is(err, ErrConflict) {
		t.Fatalf("old-revision replay error %v", err)
	}
	f.change(t, 2, Compromised, "compromise")
	replay, err = f.store.Change(context.Background(), Command{"key-1", "activate", 1, Active})
	if err != nil || !replay.Replayed || replay.State != Active || replay.Current.State != Compromised || replay.Current.Revision != 3 {
		t.Fatalf("historical receipt mistaken for current metadata: %v", err)
	}
	requireCounts(t, f, 1, 3, 3)
}
func TestRegistryConcurrentCAS(t *testing.T) {
	f := registryFixture(t)
	f.prepare(t)
	barrier := make(chan struct{})
	var entered atomic.Int64
	f.auth.hook = func(req TrustRequest) {
		if !req.Inspect && req.Command.ExpectedRevision == 1 {
			if entered.Add(1) == 2 {
				close(barrier)
			}
			<-barrier
		}
	}
	errs := make(chan error, 2)
	for i, state := range []State{Active, Compromised} {
		go func(i int, state State) {
			_, err := f.store.Change(context.Background(), Command{"key-1", fmt.Sprintf("race-%d", i), 1, state})
			errs <- err
		}(i, state)
	}
	first, second := <-errs, <-errs
	if (first == nil) == (second == nil) {
		t.Fatal("CAS did not choose exactly one writer")
	}
	if first != nil && !errors.Is(first, ErrConflict) || second != nil && !errors.Is(second, ErrConflict) {
		t.Fatalf("unexpected CAS errors %v/%v", first, second)
	}
	requireCounts(t, f, 1, 2, 2)
}
func TestRegistryConcurrentSameOperation(t *testing.T) {
	f := registryFixture(t)
	const workers = 8
	var wg sync.WaitGroup
	var created, replays, bad atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := f.store.Prepare(context.Background(), "key-1", "version-1", "", "same")
			if err != nil {
				bad.Add(1)
			} else if out.Replayed {
				replays.Add(1)
			} else {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 || created.Load() != 1 || replays.Load() != workers-1 {
		t.Fatalf("same op created/replay/error %d/%d/%d", created.Load(), replays.Load(), bad.Load())
	}
	requireCounts(t, f, 1, 1, 1)
}
func TestRegistryTrustIOBeforeSQLRowLock(t *testing.T) {
	f := registryFixture(t)
	f.prepare(t)
	f.auth.hook = func(req TrustRequest) {
		if req.Inspect {
			return
		}
		tx, err := f.db.Begin()
		if err != nil {
			t.Error(err)
			return
		}
		defer tx.Rollback()
		// Provider I/O can acquire the very row that mutation later locks.
		if _, err := tx.Exec("SELECT key_ref FROM audit_key_registry_keys WHERE namespace=$1 AND key_ref=$2 FOR UPDATE NOWAIT", req.Namespace, req.Command.KeyRef); err != nil {
			t.Error("provider called under SQL row lock")
		}
	}
	f.change(t, 1, Active, "activate")
}
func TestRegistryMutationDuringTrustAcquisitionRechecksCAS(t *testing.T) {
	f := registryFixture(t)
	f.prepare(t)
	otherAuth := &syntheticAuthority{identities: map[string]string{"version-1": "synthetic-identity-1"}}
	other, err := New(f.db, f.store.config, otherAuth)
	if err != nil {
		t.Fatal(err)
	}
	f.auth.hook = func(req TrustRequest) {
		if req.Inspect {
			return
		}
		// Another independently scoped authorized command commits while this
		// caller is still outside SQL. Its history and operation are atomic too.
		if _, err := other.Change(context.Background(), Command{"key-1", "independent-compromise", 1, Compromised}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.Change(context.Background(), Command{"key-1", "activate", 1, Active}); !errors.Is(err, ErrConflict) {
		t.Fatalf("post-I/O CAS error %v", err)
	}
	requireCounts(t, f, 1, 2, 2)
}
func TestRegistryRollbackAndLostAcknowledgement(t *testing.T) {
	t.Run("middle-rollback", func(t *testing.T) {
		f := registryFixture(t)
		if _, err := f.db.Exec(`CREATE FUNCTION reject_test_operation() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'synthetic rejection'; END; $$ LANGUAGE plpgsql;
CREATE TRIGGER reject_test BEFORE INSERT ON audit_key_registry_operations FOR EACH ROW EXECUTE FUNCTION reject_test_operation();`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.Prepare(context.Background(), "key-1", "version-1", "", "prepare"); !errors.Is(err, ErrStore) {
			t.Fatalf("injected rollback error %v", err)
		}
		requireCounts(t, f, 0, 0, 0)
	})
	t.Run("lost-ack", func(t *testing.T) {
		f := registryFixture(t)
		f.auth.after = errors.New("synthetic acknowledgement lost")
		if _, err := f.store.Prepare(context.Background(), "key-1", "version-1", "", "prepare-1"); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("committed result classified %v", err)
		}
		requireCounts(t, f, 1, 1, 1)
		f.auth.after = nil
		out, err := f.store.Prepare(context.Background(), "key-1", "version-1", "", "prepare-1")
		if err != nil || !out.Replayed || out.Current.Revision != 1 {
			t.Fatalf("ack retry error %v", err)
		}
		requireCounts(t, f, 1, 1, 1)
	})
}
func TestRegistryCurrentTrustLossBlocksReadAndMutation(t *testing.T) {
	for _, mode := range []string{"identity-changed", "authority-down", "inspect-return-error"} {
		t.Run(mode, func(t *testing.T) {
			f := registryFixture(t)
			f.prepare(t)
			f.change(t, 1, Active, "activate")
			f.auth.update(func() {
				switch mode {
				case "identity-changed":
					f.auth.identities["version-1"] = "replacement"
				case "authority-down":
					f.auth.before = ErrTrust
				case "inspect-return-error":
					f.auth.after = ErrTrust
				}
			})
			if rec, err := f.store.Inspect(context.Background(), "key-1"); !errors.Is(err, ErrTrust) || rec != (Record{}) {
				t.Fatalf("trust-loss metadata read succeeded: %v", err)
			}
			if mode != "inspect-return-error" {
				if _, err := f.store.Change(context.Background(), Command{"key-1", "rotate", 2, Retiring}); !errors.Is(err, ErrTrust) {
					t.Fatalf("trust-loss mutation error %v", err)
				}
			}
			requireCounts(t, f, 1, 2, 2)
		})
	}
}
func TestRegistryImmutableMappingHistoryAndOperation(t *testing.T) {
	f := registryFixture(t)
	f.prepare(t)
	statements := []string{
		"UPDATE audit_key_registry_keys SET secret_version_ref='replacement'",
		"UPDATE audit_key_registry_keys SET secret_identity='replacement'",
		"UPDATE audit_key_registry_keys SET key_ref='replacement'",
		"DELETE FROM audit_key_registry_keys",
		"TRUNCATE audit_key_registry_keys CASCADE",
		"UPDATE audit_key_registry_history SET actor_ref='replacement'",
		"DELETE FROM audit_key_registry_history",
		"TRUNCATE audit_key_registry_history CASCADE",
		"UPDATE audit_key_registry_operations SET state='active'",
		"DELETE FROM audit_key_registry_operations",
		"TRUNCATE audit_key_registry_operations",
	}
	for _, statement := range statements {
		if _, err := f.db.Exec(statement); err == nil {
			t.Fatal("immutable metadata mutation accepted")
		}
	}
	requireCounts(t, f, 1, 1, 1)
}
func TestRegistryInvalidInputsAndMissingAuthority(t *testing.T) {
	for _, config := range []Config{{}, {"engine-audit", ""}, {"../", "policy"}, {strings.Repeat("x", 129), "policy"}} {
		if _, err := New(&sql.DB{}, config, &syntheticAuthority{}); !errors.Is(err, ErrInput) {
			t.Fatal("invalid config accepted")
		}
	}
	if _, err := New(&sql.DB{}, Config{"audit", "policy"}, nil); !errors.Is(err, ErrInput) {
		t.Fatal("nil authority accepted")
	}
	var zero Store
	if _, err := zero.Inspect(context.Background(), "key"); !errors.Is(err, ErrInput) {
		t.Fatal("zero store accepted")
	}
	if err := Migrate(nil, &sql.DB{}); !errors.Is(err, ErrInput) {
		t.Fatal("nil migration context accepted")
	}
}

func TestRegistryForcesSynchronousWriteAcknowledgement(t *testing.T) {
	f := registryFixture(t)
	config, err := pgx.ParseConfig(os.Getenv("ARTEX_AUDIT_TEST_DSN"))
	if err != nil {
		t.Fatal("dedicated PostgreSQL configuration rejected")
	}
	config.RuntimeParams["search_path"] = f.schema
	config.RuntimeParams["synchronous_commit"] = "off"
	asyncPool := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = asyncPool.Close() })
	var setting string
	if err := asyncPool.QueryRow("SHOW synchronous_commit").Scan(&setting); err != nil || setting != "off" {
		t.Fatal("test pool is not asynchronous")
	}
	// Both DDL and mutation use the same explicit synchronous write boundary.
	if err := Migrate(context.Background(), asyncPool); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`CREATE FUNCTION require_sync_test() RETURNS trigger AS $$ BEGIN
IF current_setting('synchronous_commit')<>'on' THEN RAISE EXCEPTION 'synthetic asynchronous write rejected'; END IF;
RETURN NEW; END; $$ LANGUAGE plpgsql;
CREATE TRIGGER require_sync_key BEFORE INSERT OR UPDATE ON audit_key_registry_keys FOR EACH ROW EXECUTE FUNCTION require_sync_test();
CREATE TRIGGER require_sync_history BEFORE INSERT ON audit_key_registry_history FOR EACH ROW EXECUTE FUNCTION require_sync_test();
CREATE TRIGGER require_sync_operation BEFORE INSERT ON audit_key_registry_operations FOR EACH ROW EXECUTE FUNCTION require_sync_test();`); err != nil {
		t.Fatal(err)
	}
	store, err := New(asyncPool, f.store.config, f.auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(context.Background(), "key-1", "version-1", "", "sync-prepare"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Change(context.Background(), Command{"key-1", "sync-activate", 1, Active}); err != nil {
		t.Fatal(err)
	}
	if err := asyncPool.QueryRow("SHOW synchronous_commit").Scan(&setting); err != nil || setting != "off" {
		t.Fatal("transaction changed the session default")
	}
	requireCounts(t, f, 1, 2, 2)
}
