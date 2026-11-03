package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

var ErrControlledFindingUnavailable = errors.New("controlled_finding_unavailable")
var ErrControlledFindingOutcomeUnknown = errors.New("controlled_finding_storage_outcome_unknown")
var ErrControlledFindingConflict = errors.New("controlled_finding_conflict")

// Preparation has not reached the writer, so it cannot establish a storage
// outcome. Preserve the public unavailable error without retaining source data.
type controlledFindingSourceFailure struct{}

func (controlledFindingSourceFailure) Error() string { return ErrControlledFindingUnavailable.Error() }
func (controlledFindingSourceFailure) Unwrap() error { return ErrControlledFindingUnavailable }

type controlledFindingStoreFailure struct {
	cause          error
	classification error
	operation      [sha256.Size]byte
}

func (e controlledFindingStoreFailure) Error() string        { return e.classification.Error() }
func (e controlledFindingStoreFailure) Unwrap() error        { return e.cause }
func (e controlledFindingStoreFailure) Is(target error) bool { return target == e.classification }

func controlledFindingPersistenceError(err error, operation [sha256.Size]byte) error {
	classification := ErrControlledFindingOutcomeUnknown
	switch {
	case errors.Is(err, db.ErrControlledFindingConflict):
		classification = ErrControlledFindingConflict
	case errors.Is(err, db.ErrControlledFindingInput), errors.Is(err, db.ErrControlledFindingMode):
		classification = ErrControlledFindingUnavailable
	}
	return controlledFindingStoreFailure{cause: err, classification: classification, operation: operation}
}

// ControlledReadFindingPlan is resolved by a trusted host, never model JSON.
// Source authenticates KG/fixture/identity and independently reads the protected
// final checkpoint. Engine disposition and collected observations come from E2.
// Leave the engine-owned digest/creation/disposition fields unset unless the
// authority already has the identical collected snapshot; they are not defaults
// inferred from KG or from the model.
type ControlledReadFindingPlan struct {
	Expected  evidencebind.Context
	Candidate idor.Candidate
	KG        idor.PolicyResolver
	Source    e3record.SourceReader
}

type ControlledReadFindingResolver func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error)

// ConfigureControlledReadFindings installs the verified-read collector and the
// dedicated transactional sink together, before any work is admitted.
func (s *Server) ConfigureControlledReadFindings(resolve ControlledReadFindingResolver) error {
	return s.configureControlledReadFindings(resolve, nil)
}

func (s *Server) configureControlledReadFindings(resolve ControlledReadFindingResolver, revisions *controlledRevisionServices) error {
	if s == nil || s.m == nil || s.m.pg == nil || resolve == nil {
		return ErrControlledFindingUnavailable
	}
	markers, err := s.ControlledTaskModeMarkers()
	if err != nil {
		return ErrControlledFindingUnavailable
	}
	return s.ConfigureControlledReadVerification(func(ctx context.Context, request ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
		plan, err := resolve(ctx, request)
		if err != nil {
			return ControlledReadVerificationPlan{}, ErrControlledFindingUnavailable
		}
		collector, err := newControlledFindingCollector(s.m.pg, markers, request, plan)
		if err != nil {
			return ControlledReadVerificationPlan{}, err
		}
		if revisions != nil {
			collector.revisions = revisions
			collector.revisionOperation = controlledRevisionOperation(request)
		}
		return collector.verificationPlan(), nil
	})
}

func newControlledFindingCollector(pg *db.DB, markers db.TaskModeMarkerReader, request ControlledReadVerificationRequest, plan ControlledReadFindingPlan) (*controlledFindingCollector, error) {
	if pg == nil || markers == nil || plan.Source == nil || plan.KG == nil ||
		request.TaskID <= 0 || request.IntentID <= 0 ||
		plan.Expected.Scope != request.Scope || plan.Expected.IntentID != request.IntentID ||
		request.Scope.TaskRef != strconv.FormatInt(request.TaskID, 10) ||
		plan.Expected.ResourceRef != request.ResourceRef || plan.Expected.ReadApprovalRef == "" ||
		plan.Expected.KGPolicyVersion == "" ||
		plan.Expected.Owner != plan.Candidate.Owner || plan.Expected.Evaluated != plan.Candidate.Evaluated ||
		plan.Candidate.ResourceRef != request.ResourceRef || plan.Expected.Evaluated.Label != request.AccountRef {
		return nil, ErrControlledFindingUnavailable
	}
	return &controlledFindingCollector{markers: markers, request: request, plan: plan, save: pg.RecordControlledFinding}, nil
}

func (c *controlledFindingCollector) verificationPlan() ControlledReadVerificationPlan {
	return ControlledReadVerificationPlan{Candidate: c.plan.Candidate, KG: c.plan.KG, Collect: c.collect, Finalize: c.finalize}
}

type controlledFindingCollector struct {
	revisions         *controlledRevisionServices
	revisionOperation string
	revisionCollected evidencerevision.Collected
	revisionFailure   error
	mu                sync.Mutex
	save              func(context.Context, e3record.Prepared, db.TaskModeMarkerReader) (db.ControlledFindingResult, error)
	markers           db.TaskModeMarkerReader
	request           ControlledReadVerificationRequest
	plan              ControlledReadFindingPlan
	collected         bool
	finalizing        bool
	saved             bool
	createdAt         time.Time
	input             e3record.Input
	digest            [sha256.Size]byte
	checkpoint        [sha256.Size]byte
	prepared          bool
	operation         [sha256.Size]byte
	inputKey          [sha256.Size]byte
}

func (c *controlledFindingCollector) collect(ctx context.Context, evidence ControlledReadVerificationEvidence) error {
	if ctx == nil || ctx.Err() != nil || evidence.Request != c.request || evidence.Completed || evidence.Finalized ||
		evidence.FinalCheckpoint != (auditlog.Checkpoint{}) {
		return ErrControlledFindingUnavailable
	}
	detail := evidence.Verification
	if !detail.PolicyResolved || !detail.Owner.Captured || !detail.Evaluated.Captured {
		return ErrControlledFindingUnavailable
	}
	expected := c.plan.Expected
	owner, err := collectedReadReference(expected, detail.Owner.Receipt.Attempt, evidencebind.OwnerRead)
	if err != nil {
		return err
	}
	cross, err := collectedReadReference(expected, detail.Evaluated.Receipt.Attempt, evidencebind.CrossRead)
	if err != nil {
		return err
	}
	// Preserve only metadata from this actual verification call, never Capture.View.
	result := detail.Result
	result.Verdict.ReasonCodes = slices.Clone(result.Verdict.ReasonCodes)
	input := e3record.Input{Expected: expected, CanaryFields: slices.Clone(detail.CanaryFields),
		Evidence: evidencebind.Input{
			Policy: evidencebind.PolicySnapshot{Context: expected, Assertion: detail.FrozenPolicy},
			Owner: evidencebind.Observation{Context: expected, Receipt: detail.Owner.Receipt,
				Evidence: owner, ObservedAt: detail.Owner.Capture.ObservedAt},
			Cross: evidencebind.Observation{Context: expected, Receipt: detail.Evaluated.Receipt,
				Evidence: cross, ObservedAt: detail.Evaluated.Capture.ObservedAt},
			Result: evidencebind.ResultSnapshot{Context: expected, OwnerEvidence: owner,
				CrossEvidence: cross, ObservedAt: detail.Evaluated.Capture.ObservedAt, Result: result},
		}}
	digest, err := e3record.EvidenceDigest(input.Evidence)
	if err != nil {
		return ErrControlledFindingUnavailable
	}
	var collected evidencerevision.Collected
	if c.revisions != nil {
		collected, err = evidencerevision.Capture(expected, input.Evidence, input.CanaryFields)
		if err != nil {
			return controlledRevisionFailure{reason: "capture_rejected", operation: c.revisionOperation}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.collected || c.finalizing || c.saved {
		return ErrControlledFindingUnavailable
	}
	c.input, c.digest, c.createdAt = input, digest, time.Now().Round(0).UTC()
	c.revisionCollected = collected
	c.collected = true
	return nil
}

// collectedReadReference identifies an actual observed attempt. It is not an
// approval/KG attestation. The digest keeps the public reference bounded even
// when the server's RunRef is at its maximum permitted length.
func collectedReadReference(expected evidencebind.Context, attempt uint64, role evidencebind.ReadRole) (evidencebind.EvidenceRef, error) {
	body, err := json.Marshal(struct {
		Context evidencebind.Context
		Attempt uint64
		Role    evidencebind.ReadRole
	}{expected, attempt, role})
	if err != nil {
		return evidencebind.EvidenceRef{}, ErrControlledFindingUnavailable
	}
	digest := sha256.Sum256(append([]byte("artex.controlled.observation.v1\x00"), body...))
	return evidencebind.EvidenceRef{ID: "read:" + hex.EncodeToString(digest[:]),
		IntentID: expected.IntentID, Scope: expected.Scope, Attempt: attempt, Role: role}, nil
}

// ControlledReadCheckpointDigest describes the typed checkpoint bytes used by
// SourceReader. This digest does not authenticate a checkpoint: the reader must
// verify the protected latest checkpoint/journal using its trusted key source.
func ControlledReadCheckpointDigest(checkpoint auditlog.Checkpoint) ([sha256.Size]byte, error) {
	mac, err := hex.DecodeString(checkpoint.MAC)
	if err != nil || len(mac) != sha256.Size || checkpoint.Version != auditlog.Version ||
		checkpoint.JournalID == "" || checkpoint.Scope.RunRef == "" || checkpoint.Sequence == 0 {
		return [sha256.Size]byte{}, ErrControlledFindingUnavailable
	}
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return [sha256.Size]byte{}, ErrControlledFindingUnavailable
	}
	return sha256.Sum256(body), nil
}

func (c *controlledFindingCollector) finalize(ctx context.Context, evidence ControlledReadVerificationEvidence) (err error) {
	if ctx == nil || ctx.Err() != nil || evidence.Request != c.request || !evidence.Completed || !evidence.Finalized ||
		evidence.FinalCheckpoint.Scope != c.request.Scope {
		return ErrControlledFindingUnavailable
	}
	checkpointDigest, err := ControlledReadCheckpointDigest(evidence.FinalCheckpoint)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if !c.collected || c.finalizing ||
		c.checkpoint != ([sha256.Size]byte{}) && c.checkpoint != checkpointDigest {
		c.mu.Unlock()
		return ErrControlledFindingUnavailable
	}
	if c.saved {
		c.mu.Unlock()
		return nil
	}
	c.finalizing = true
	c.checkpoint = checkpointDigest
	input, digest, createdAt := c.input, c.digest, c.createdAt
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.finalizing = false
		c.saved = err == nil
		c.mu.Unlock()
	}()
	// The independent metadata reader supplies genuine KG/fixture/identity and
	// protected audit truth. The private collector supplies only what E2 observed.
	var revisionErr error
	reader := func(ctx context.Context, expected evidencebind.Context) (e3record.Source, error) {
		var source e3record.Source
		var err error
		if c.revisions != nil {
			source, err = c.revisionSource(ctx, expected)
			if err != nil {
				revisionErr = err
				return e3record.Source{}, err
			}
		} else {
			source, err = c.plan.Source(ctx, expected)
		}
		if err != nil || source.FinalCheckpointDigest != checkpointDigest ||
			source.FinalCheckpointScope != evidence.FinalCheckpoint.Scope ||
			source.FinalCheckpointSequence != evidence.FinalCheckpoint.Sequence ||
			source.EvidenceDigest != ([sha256.Size]byte{}) && source.EvidenceDigest != digest ||
			!source.RecordCreatedAt.IsZero() && !source.RecordCreatedAt.Equal(createdAt) {
			return e3record.Source{}, ErrControlledFindingUnavailable
		}
		source.EvidenceDigest, source.RecordCreatedAt = digest, createdAt
		source.Completed, source.Finalized = evidence.Completed, evidence.Finalized
		return source, nil
	}
	prepared, err := e3record.Prepare(ctx, input, reader)
	if err != nil {
		if revisionErr != nil {
			return revisionErr
		}
		return controlledFindingSourceFailure{}
	}
	snapshot, valid := prepared.Snapshot()
	if !valid {
		return controlledFindingSourceFailure{}
	}
	c.mu.Lock()
	if c.prepared && (c.operation != snapshot.OperationKey || c.inputKey != snapshot.InputDigest) {
		conflict := controlledFindingStoreFailure{cause: ErrControlledFindingConflict, classification: ErrControlledFindingConflict, operation: c.operation}
		c.mu.Unlock()
		return conflict
	}
	c.prepared, c.operation, c.inputKey = true, snapshot.OperationKey, snapshot.InputDigest
	c.mu.Unlock()
	if c.save == nil {
		return ErrControlledFindingUnavailable
	}
	if _, err := c.save(ctx, prepared, c.markers); err != nil {
		// A transport/commit failure is not proof that nothing was persisted.
		// Carry a fixed classification and non-secret operation key to finish's
		// server diagnostic. The underlying cause must never be logged verbatim.
		return controlledFindingPersistenceError(err, snapshot.OperationKey)
	}
	return nil
}

// Finalize is called once by the execution path; it does not automatically retry
// an uncertain commit. This server-only diagnostic lets an operator reconcile
// the operation against persisted rows without exposing source or SQL errors.
func logControlledFindingFinalizationFailure(err error) {
	reason := "finalization_rejected"
	var revision controlledRevisionFailure
	if errors.As(err, &revision) {
		log.Printf("[controlled-read] revision_failed reason=%s operation_ref=%s", revision.reason, revision.operation)
		return
	}
	var sourceFailure controlledFindingSourceFailure
	var failure controlledFindingStoreFailure
	switch {
	case errors.As(err, &sourceFailure):
		reason = "source_rejected"
	case errors.Is(err, ErrControlledFindingOutcomeUnknown):
		reason = "storage_outcome_unknown"
	case errors.Is(err, ErrControlledFindingConflict):
		reason = "storage_conflict"
	case errors.As(err, &failure) && errors.Is(err, ErrControlledFindingUnavailable):
		reason = "storage_rejected"
	}
	key := "unavailable"
	if errors.As(err, &failure) && failure.operation != ([sha256.Size]byte{}) {
		key = hex.EncodeToString(failure.operation[:])
	}
	log.Printf("[controlled-read] finalization_failed reason=%s operation_key=%s", reason, key)
}
