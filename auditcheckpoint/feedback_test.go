package auditcheckpoint

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestStoreNoOpAppendCASDoesNotRewindConcurrentHead(t *testing.T) {
	f := newFixture(t, true)
	publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
	publish(t, f, f.req(Append, "op:a", 0, 1, 1), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	other, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("concurrent transaction unavailable")
	}
	defer other.Rollback()
	var blockerPID int
	if err := other.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal("concurrent backend unavailable")
	}
	var revision int
	if err := other.QueryRowContext(ctx, `SELECT revision FROM audit_checkpoint_heads FOR UPDATE`).Scan(&revision); err != nil || revision != 2 {
		t.Fatal("concurrent transaction must lock revision 2")
	}
	type outcome struct {
		receipt Receipt
		err     error
	}
	result := make(chan outcome, 1)
	done := make(chan struct{})
	request := f.req(Append, "op:noop-race", 1, 1, 2)
	go func() {
		defer close(done)
		r, err := f.store.Publish(ctx, request, bytes.NewReader(f.snap[1]))
		result <- outcome{r, err}
	}()
	defer func() {
		cancel()
		_ = other.Rollback()
		<-done
	}()
	// Observe the actual blocked UPDATE, after the no-op read revision 2.
	// A key callback barrier alone would occur before the transaction's head read.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		err := f.db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock'
			AND query LIKE 'UPDATE audit_checkpoint_heads SET revision=%'
		)`, blockerPID).Scan(&blocked)
		if err != nil {
			t.Fatal("could not observe no-op UPDATE waiting on the head lock")
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("no-op never reached the locked head UPDATE")
		case <-ticker.C:
		}
	}
	raw, err := json.Marshal(f.cp[2])
	if err != nil {
		t.Fatal("checkpoint fixture")
	}
	e := f.host.epoch
	if _, err := other.ExecContext(ctx, `INSERT INTO audit_checkpoint_history
		(store_ref,run_ref,revision,epoch_ref,generation,anchor_digest,publisher_ref,phase,checkpoint)
		VALUES($1,$2,3,$3,$4,$5,$6,$7,$8)`, e.StoreRef, f.cp[2].Scope.RunRef, e.EpochRef,
		int64(e.Generation), e.AnchorDigest[:], request.PublisherRef, Open, raw); err != nil {
		t.Fatal("concurrent revision 3 history fixture")
	}
	if _, err := other.ExecContext(ctx, `UPDATE audit_checkpoint_heads SET revision=3`); err != nil {
		t.Fatal("concurrent revision 3 head fixture")
	}
	if err := other.Commit(); err != nil {
		t.Fatal("concurrent revision 3 commit")
	}
	got := <-result
	if !errors.Is(got.err, ErrConflict) || got.receipt != (Receipt{}) {
		t.Errorf("stale no-op must return zero receipt and ErrConflict: %v", got.err)
	}
	head, err := f.store.Head(context.Background(), f.query(""))
	if err != nil || head.Revision != 3 || head.Phase != Open || head.Checkpoint != f.cp[2] {
		t.Fatalf("stale no-op rewound committed head: revision=%d error=%v", head.Revision, err)
	}
	if got, err := f.store.Operation(context.Background(), f.query(request.OperationRef)); !errors.Is(err, ErrNotFound) || got != (Receipt{}) {
		t.Fatal("rejected no-op left a receipt")
	}
	counts(t, f, 3, 2)
}

func TestStoreSealRejectsSnapshotShorterThanCandidate(t *testing.T) {
	f := newFixture(t, true)
	publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
	accepted := publish(t, f, f.req(Append, "op:a", 0, 1, 1), 1)
	request := f.req(Seal, "op:short-seal", 1, 2, 2)
	got, err := f.store.Publish(context.Background(), request, bytes.NewReader(f.snap[1]))
	if !errors.Is(err, ErrVerification) || got != (Receipt{}) {
		t.Fatalf("short seal snapshot must return zero receipt and ErrVerification: %v", err)
	}
	head, err := f.store.Head(context.Background(), f.query(""))
	if err != nil || head != accepted.Head {
		t.Fatal("unverified Seal changed the accepted open head")
	}
	counts(t, f, 2, 2)
}

func TestStoreLeaseRevokedDuringResolveKeyCancelsPublication(t *testing.T) {
	f := newFixture(t, true)
	leaseCtx, revoke := context.WithCancel(context.Background())
	defer revoke()
	f.host.leaseCtx = leaseCtx
	propagated := false
	f.host.keyContextHook = func(ctx context.Context) {
		revoke()
		select {
		case <-ctx.Done():
			propagated = true
		case <-time.After(time.Second):
		}
	}
	got, err := f.store.Publish(context.Background(), f.req(Register, "op:revoked", 0, 0, 0), bytes.NewReader(f.snap[0]))
	if !propagated || !errors.Is(err, ErrAuthority) || got != (Receipt{}) {
		t.Fatalf("lease revocation must cancel ResolveKey and deny publication: propagated=%v error=%v", propagated, err)
	}
	counts(t, f, 0, 0)
}

func TestStoreEpochShareLockHeldUntilCommit(t *testing.T) {
	f := newFixture(t, true)
	probed := false
	f.store.commit = func(tx *sql.Tx) error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		other, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Error("independent epoch probe transaction unavailable")
			return err
		}
		defer other.Rollback()
		e := f.host.epoch
		var one int
		err = other.QueryRowContext(ctx, `SELECT 1 FROM audit_checkpoint_epochs
			WHERE store_ref=$1 AND epoch_ref=$2 AND generation=$3 AND anchor_digest=$4
			FOR UPDATE NOWAIT`, e.StoreRef, e.EpochRef, int64(e.Generation), e.AnchorDigest[:]).Scan(&one)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Error("commit must retain epoch FOR SHARE: independent FOR UPDATE NOWAIT did not return SQLSTATE 55P03")
		}
		probed = true
		return tx.Commit()
	}
	publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
	if !probed {
		t.Fatal("commit boundary was not probed")
	}
	counts(t, f, 1, 1)
}

func TestStoreTerminalCandidateMustBeAcceptedPrefix(t *testing.T) {
	for _, kind := range []Kind{MarkIncomplete, Quarantine} {
		t.Run(string(kind), func(t *testing.T) {
			f := newFixture(t, true)
			accepted := publish(t, f, f.req(Register, "op:r", 0, 0, 0), 0)
			got, err := f.store.Publish(context.Background(), f.req(kind, "op:unaccepted", 0, 1, 1), nil)
			if !errors.Is(err, ErrRequest) || got != (Receipt{}) {
				t.Fatalf("terminal candidate must equal accepted prefix: %v", err)
			}
			head, err := f.store.Head(context.Background(), f.query(""))
			if err != nil || head != accepted.Head {
				t.Fatal("terminal request accepted an unverified prefix")
			}
			counts(t, f, 1, 1)
		})
	}
}

func TestStoreRegisterRequiresSequenceZero(t *testing.T) {
	f := newFixture(t, true)
	got, err := f.store.Publish(context.Background(), f.req(Register, "op:nonzero", 0, 1, 0), bytes.NewReader(f.snap[1]))
	if !errors.Is(err, ErrRequest) || got != (Receipt{}) {
		t.Fatalf("Register sequence must be zero: %v", err)
	}
	counts(t, f, 0, 0)
}
