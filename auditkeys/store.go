package auditkeys

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed schema.sql
var schemaSQL string

// Migrate is explicit. It is not installed in db.Open or any application startup.
func Migrate(ctx context.Context, db *sql.DB) error {
	if ctx == nil || ctx.Err() != nil || db == nil {
		return ErrInput
	}
	tx, err := beginWriteTx(ctx, db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const migrationLock int64 = 8142093307
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLock); err == nil {
		_, err = tx.ExecContext(ctx, schemaSQL)
	}
	if err != nil {
		return storeError(err, false)
	}
	return storeError(tx.Commit(), true)
}

// The pool may default to asynchronous acknowledgement. Metadata/history/op
// success requires this transaction to request synchronous WAL acknowledgement.
// Actual fsync, storage and failover durability remain deployment obligations.
func beginWriteTx(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, storeError(err, false)
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL synchronous_commit=on"); err != nil {
		_ = tx.Rollback()
		return nil, storeError(err, false)
	}
	return tx, nil
}

type stored struct {
	record  Record
	binding Binding
}
type scanner interface{ Scan(...any) error }

func scanStored(row scanner) (stored, error) {
	var v stored
	var predecessor sql.NullString
	err := row.Scan(&v.record.Namespace, &v.record.KeyRef, &v.record.SecretVersionRef,
		&v.binding.Identity, &predecessor, &v.record.State, &v.record.Revision, &v.record.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.record.PredecessorRef = predecessor.String
	v.binding.KeyRef, v.binding.SecretVersionRef, v.binding.PredecessorRef = v.record.KeyRef, v.record.SecretVersionRef, predecessor.String
	return v, nil
}

const selectKey = "SELECT namespace,key_ref,secret_version_ref,secret_identity,predecessor_ref,state,revision,updated_at FROM audit_key_registry_keys WHERE namespace=$1 AND key_ref=$2"

func (s *Store) load(ctx context.Context, key string) (stored, error) {
	v, err := scanStored(s.db.QueryRowContext(ctx, selectKey, s.config.Namespace, key))
	if errors.Is(err, sql.ErrNoRows) {
		return stored{}, ErrNotFound
	}
	if err != nil {
		return stored{}, storeError(err, false)
	}
	return v, nil
}

// Prepare binds verified immutable secret metadata at revision one. It never
// creates a secret or automatically promotes it to active.
func (s *Store) Prepare(ctx context.Context, keyRef, secretVersionRef, predecessorRef, operationRef string) (Outcome, error) {
	cmd := Command{KeyRef: keyRef, OperationRef: operationRef, Next: Prepared}
	if !s.valid(ctx, cmd, true) || !validRef(secretVersionRef) ||
		predecessorRef != "" && !validRef(predecessorRef) || predecessorRef == keyRef {
		return Outcome{}, ErrInput
	}
	binding := Binding{KeyRef: keyRef, SecretVersionRef: secretVersionRef, PredecessorRef: predecessorRef}
	// On an exact retry the already stored mapping is challenged again. A
	// different version/predecessor is rejected before any authority call.
	var prior stored
	existing, err := s.load(ctx, keyRef)
	if err == nil {
		prior = existing
		if prior.binding.SecretVersionRef != secretVersionRef || prior.binding.PredecessorRef != predecessorRef {
			return Outcome{}, ErrConflict
		}
		binding = prior.binding
	} else if !errors.Is(err, ErrNotFound) {
		return Outcome{}, err
	}
	challenge := s.challenge(binding, cmd, prior, false)
	return s.mutate(ctx, challenge, true)
}

func (s *Store) Change(ctx context.Context, cmd Command) (Outcome, error) {
	if !s.valid(ctx, cmd, false) {
		return Outcome{}, ErrInput
	}
	prior, err := s.load(ctx, cmd.KeyRef)
	if err != nil {
		return Outcome{}, err
	}
	return s.mutate(ctx, s.challenge(prior.binding, cmd, prior, false), false)
}

// Inspect returns current metadata under authenticated trust. It supplies no
// secret and no lease/admission grant; runtime use and revocation are separate.
func (s *Store) Inspect(ctx context.Context, keyRef string) (Record, error) {
	if s == nil || s.db == nil || s.authority == nil || ctx == nil || ctx.Err() != nil || !validRef(keyRef) {
		return Record{}, ErrInput
	}
	prior, err := s.load(ctx, keyRef)
	if err != nil {
		return Record{}, err
	}
	request := s.challenge(prior.binding, Command{KeyRef: keyRef}, prior, true)
	var result Record
	_, err = s.withTrust(ctx, request, false, func(ctx context.Context, att Attestation) (bool, error) {
		current, err := s.load(ctx, keyRef)
		if err != nil {
			return false, err
		}
		if current.binding != prior.binding || current.record.Revision != prior.record.Revision || current.record.State != prior.record.State {
			return false, ErrConflict
		}
		result = current.record
		return false, nil
	})
	if err != nil {
		return Record{}, err
	}
	return result, nil
}

func (s *Store) valid(ctx context.Context, cmd Command, register bool) bool {
	if s == nil || s.db == nil || s.authority == nil || ctx == nil || ctx.Err() != nil || !validRef(cmd.KeyRef) || !validRef(cmd.OperationRef) {
		return false
	}
	if register {
		return cmd.ExpectedRevision == 0 && cmd.Next == Prepared
	}
	return validRevision(cmd.ExpectedRevision) && cmd.ExpectedRevision < math.MaxInt64 && cmd.Next != Prepared &&
		(cmd.Next == Active || cmd.Next == Retiring || cmd.Next == Retired || cmd.Next == Compromised || cmd.Next == Destroyed)
}
func (s *Store) challenge(binding Binding, cmd Command, prior stored, inspect bool) TrustRequest {
	return TrustRequest{Namespace: s.config.Namespace, PolicyRef: s.config.PolicyRef, Binding: binding, Command: cmd,
		CurrentRevision: prior.record.Revision, CurrentState: prior.record.State, Inspect: inspect}
}

func validateAttestation(req TrustRequest, a Attestation) error {
	if a.Request != req || !validRef(a.Identity) || !validRef(a.ActorRef) || !validRevision(a.PolicyRevision) ||
		!validRef(a.MappingRef) || !validRef(a.AuthorizationRef) || req.Binding.Identity != "" && a.Identity != req.Binding.Identity {
		return ErrTrust
	}
	if req.Inspect {
		return nil
	}
	switch req.Command.Next {
	case Active:
		if !validRef(a.ActivationRef) {
			return ErrTrust
		}
	case Retired:
		if !validRef(a.DrainRef) {
			return ErrTrust
		}
	case Destroyed:
		if !validRef(a.DrainRef) || !validRef(a.DependencyRef) || !validRef(a.CopyLifetimeRef) || !validRef(a.DisposalRef) ||
			req.Command.ExpectedRevision == 1 && !validRef(a.UnusedRef) {
			return ErrTrust
		}
	}
	for _, ref := range []string{a.ActivationRef, a.DrainRef, a.DependencyRef, a.CopyLifetimeRef, a.DisposalRef, a.UnusedRef} {
		if ref != "" && !validRef(ref) {
			return ErrTrust
		}
	}
	return nil
}

// withTrust calls no provider method while holding a SQL row lock. The
// synchronous WithTrust contract keeps the external scope valid across work.
// A provider error after a committed write is an unknown outcome, not rollback.
func (s *Store) withTrust(ctx context.Context, req TrustRequest, mutation bool, work func(context.Context, Attestation) (bool, error)) (bool, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	called, finished, committed, returned, violation := false, false, false, false, false
	var workErr error
	outerErr := s.authority.WithTrust(callCtx, req, func(att Attestation) error {
		mu.Lock()
		if called || returned {
			violation = true
			mu.Unlock()
			return ErrTrust
		}
		called = true
		mu.Unlock()
		err := validateAttestation(req, att)
		didCommit := false
		if err == nil && callCtx.Err() != nil {
			err = ErrTrust
		}
		if err == nil {
			didCommit, err = work(callCtx, att)
		}
		mu.Lock()
		committed, workErr, finished = didCommit, err, true
		mu.Unlock()
		return err
	})
	mu.Lock()
	returned = true
	defer mu.Unlock()
	if !called {
		return false, ErrTrust
	}
	if !finished {
		if mutation {
			return false, ErrOutcomeUnknown
		}
		return false, ErrTrust
	}
	if workErr != nil {
		return committed, workErr
	}
	if violation || outerErr != nil || ctx.Err() != nil {
		if mutation && committed {
			return true, ErrOutcomeUnknown
		}
		return false, ErrTrust
	}
	return committed, nil
}

func (s *Store) mutate(ctx context.Context, req TrustRequest, register bool) (Outcome, error) {
	var out Outcome
	_, err := s.withTrust(ctx, req, true, func(ctx context.Context, att Attestation) (bool, error) {
		binding := req.Binding
		binding.Identity = att.Identity
		digest, err := operationDigest(req, att, binding)
		if err != nil {
			return false, ErrInput
		}
		tx, err := beginWriteTx(ctx, s.db)
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		lock := sha256.Sum256([]byte("artex.auditkeys.operation.v1\x00" + req.Namespace + "\x00" + req.Command.OperationRef))
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(binary.BigEndian.Uint64(lock[:8]))); err != nil {
			return false, storeError(err, false)
		}
		var recordedDigest []byte
		var receipt Outcome
		var recordedKey string
		err = tx.QueryRowContext(ctx, "SELECT request_digest,key_ref,revision,state FROM audit_key_registry_operations WHERE namespace=$1 AND operation_ref=$2", req.Namespace, req.Command.OperationRef).
			Scan(&recordedDigest, &recordedKey, &receipt.Revision, &receipt.State)
		replay := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, storeError(err, false)
		}
		current, loadErr := scanStored(tx.QueryRowContext(ctx, selectKey+" FOR UPDATE", req.Namespace, req.Command.KeyRef))
		if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
			return false, storeError(loadErr, false)
		}
		if replay {
			if recordedKey != req.Command.KeyRef || string(recordedDigest) != string(digest[:]) || loadErr != nil || current.binding != binding {
				return false, ErrConflict
			}
			out = receipt
			out.Current, out.Replayed = current.record, true
			return false, nil
		}
		from := State("")
		if register {
			if loadErr == nil {
				return false, ErrConflict
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO audit_key_registry_keys(namespace,key_ref,secret_version_ref,secret_identity,predecessor_ref,state,revision)
VALUES($1,$2,$3,$4,NULLIF($5,''),'prepared',1)`, req.Namespace, binding.KeyRef, binding.SecretVersionRef, binding.Identity, binding.PredecessorRef)
			if err != nil {
				return false, storeError(err, false)
			}
		} else {
			if errors.Is(loadErr, sql.ErrNoRows) {
				return false, ErrNotFound
			}
			if current.binding != binding || current.record.Revision != req.CurrentRevision || current.record.State != req.CurrentState ||
				current.record.Revision != req.Command.ExpectedRevision {
				return false, ErrConflict
			}
			if !allowed(current.record.State, req.Command.Next) {
				return false, ErrTransition
			}
			from = current.record.State
			_, err = tx.ExecContext(ctx, `UPDATE audit_key_registry_keys SET state=$3,revision=revision+1,updated_at=clock_timestamp()
WHERE namespace=$1 AND key_ref=$2`, req.Namespace, req.Command.KeyRef, req.Command.Next)
			if err != nil {
				return false, storeError(err, false)
			}
		}
		current, err = scanStored(tx.QueryRowContext(ctx, selectKey, req.Namespace, req.Command.KeyRef))
		if err != nil {
			return false, storeError(err, false)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_key_registry_history(namespace,key_ref,revision,from_state,state,operation_ref,
actor_ref,policy_ref,policy_revision,mapping_ref,authorization_ref,activation_ref,drain_ref,dependency_ref,copy_lifetime_ref,disposal_ref,unused_ref,changed_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			req.Namespace, req.Command.KeyRef, current.record.Revision, from, current.record.State, req.Command.OperationRef,
			att.ActorRef, req.PolicyRef, att.PolicyRevision, att.MappingRef, att.AuthorizationRef, att.ActivationRef, att.DrainRef,
			att.DependencyRef, att.CopyLifetimeRef, att.DisposalRef, att.UnusedRef, current.record.UpdatedAt)
		if err != nil {
			return false, storeError(err, false)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_key_registry_operations(namespace,operation_ref,request_digest,key_ref,revision,state)
VALUES($1,$2,$3,$4,$5,$6)`, req.Namespace, req.Command.OperationRef, digest[:], req.Command.KeyRef, current.record.Revision, current.record.State)
		if err != nil {
			return false, storeError(err, false)
		}
		if err = tx.Commit(); err != nil {
			return false, storeError(err, true)
		}
		out = Outcome{Revision: current.record.Revision, State: current.record.State, Current: current.record}
		return true, nil
	})
	if err != nil {
		return Outcome{}, err
	}
	return out, nil
}

func operationDigest(req TrustRequest, att Attestation, binding Binding) ([32]byte, error) {
	// CurrentRevision/CurrentState are a fresh authority challenge, not operation
	// content. Retries keep the immutable command and authenticated evidence.
	body, err := json.Marshal(struct {
		Version                                                                                                       int
		Namespace, PolicyRef                                                                                          string
		Binding                                                                                                       Binding
		Command                                                                                                       Command
		ActorRef                                                                                                      string
		PolicyRevision                                                                                                uint64
		MappingRef, AuthorizationRef, ActivationRef, DrainRef, DependencyRef, CopyLifetimeRef, DisposalRef, UnusedRef string
	}{1, req.Namespace, req.PolicyRef, binding, req.Command, att.ActorRef, att.PolicyRevision,
		att.MappingRef, att.AuthorizationRef, att.ActivationRef, att.DrainRef, att.DependencyRef, att.CopyLifetimeRef, att.DisposalRef, att.UnusedRef})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte("artex.auditkeys.command.v1\x00"), body...)), nil
}

type registryError struct {
	cause   error
	unknown bool
}

func (e registryError) Error() string {
	if e.unknown {
		return ErrOutcomeUnknown.Error()
	}
	return ErrStore.Error()
}
func (e registryError) Is(target error) bool {
	return target == ErrStore || e.unknown && target == ErrOutcomeUnknown
}
func (e registryError) Unwrap() error    { return e.cause }
func (e registryError) GoString() string { return "auditkeys.registryError{cause:[REDACTED]}" }
func storeError(err error, commit bool) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if !commit && errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return registryError{err, commit}
}
