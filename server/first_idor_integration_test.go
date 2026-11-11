package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

// These are owned httptest/PG fixtures with a fake model and fake identity/KG
// authorities. Passing them is not a real Juice Shop diagnostic.
func TestFirstIDORSyntheticEngineThroughStoredFinding(t *testing.T) {
	for _, mode := range []string{"confirmed", "late-pause", "missing-source", "source-error", "checkpoint-mismatch", "storage-rejected", "unknown-commit-source-change"} {
		t.Run(mode, func(t *testing.T) {
			diagnosticPath := filepath.Join(t.TempDir(), "finalization.log")
			diagnosticFile, err := os.Create(diagnosticPath)
			if err != nil {
				t.Fatal(err)
			}
			defer diagnosticFile.Close()
			previousLog := log.Writer()
			log.SetOutput(diagnosticFile)
			defer log.SetOutput(previousLog)
			var hits atomic.Int64
			var resolved, collections, finalizations, sourceCalls atomic.Int64
			target := controlledReadTarget(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, "{\"status\":\"ok\",\"data\":{\"note\":%q,\"private\":%q}}", verificationTestMarker, runtimeSecret)
			}))
			f := newControlledVerificationDBFixture(t, target.URL)
			taskID := mustSyntheticTaskID(t, f.task.ID)
			policy := oracle.PolicyAssertion{
				ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
				HasRegisteredSource: true, HintMatchesCanonical: true,
				ValidFrom:  time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond),
				ValidUntil: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond),
			}
			kg := &verificationTestKG{policy: policy}
			owner := idor.AccountRef{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified}
			peer := idor.AccountRef{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified}
			kgBytes, err := json.Marshal(struct {
				Policy oracle.PolicyAssertion
				Marker string
			}{policy, verificationTestMarker})
			if err != nil {
				t.Fatal(err)
			}
			kgDigest := sha256.Sum256(kgBytes)
			sourceEntered, sourceRelease := make(chan struct{}), make(chan struct{})
			var enterOnce, releaseOnce sync.Once
			var revision atomic.Uint64
			revision.Store(1)
			var retryFinalize func(context.Context, ControlledReadVerificationEvidence) error
			var lastEvidence ControlledReadVerificationEvidence
			var firstSaveError error
			var attemptedOperation [sha256.Size]byte
			var writes atomic.Int64
			release := func() { releaseOnce.Do(func() { close(sourceRelease) }) }
			t.Cleanup(release)
			resolve := func(_ context.Context, request ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
				resolved.Add(1)
				if request.TaskID != taskID || request.IntentID != f.intent.ID ||
					request.AccountRef != "peer" || request.ResourceRef != "doc:one" {
					return ControlledReadFindingPlan{}, errors.New("synthetic host binding mismatch")
				}
				expected := evidencebind.Context{Scope: request.Scope, IntentID: request.IntentID,
					ResourceRef: request.ResourceRef, ReadApprovalRef: "read-approval",
					KGPolicyVersion: "synthetic-kg-policy-v1", Owner: owner, Evaluated: peer}
				source := func(ctx context.Context, actual evidencebind.Context) (_ e3record.Source, sourceErr error) {
					defer func() {
						if sourceErr != nil {
							t.Logf("synthetic metadata source refused: %v", sourceErr)
						}
					}()
					sourceCalls.Add(1)
					if actual != expected || mode == "source-error" {
						return e3record.Source{}, errors.New("SYNTHETIC_PRIVATE_SOURCE_ERROR")
					}
					// Each owned fixture has exactly one journal. Checkpoints are
					// named by JournalID, while journals are named by RunRef.
					checkpointPaths, err := filepath.Glob(filepath.Join(f.store.journalDirectory(), "*.checkpoint"))
					if err != nil || len(checkpointPaths) != 1 {
						return e3record.Source{}, errors.New("synthetic checkpoint is absent or ambiguous")
					}
					checkpointBytes, err := readPrivate(checkpointPaths[0], 64<<10)
					if err != nil {
						return e3record.Source{}, err
					}
					var checkpoint auditlog.Checkpoint
					if json.Unmarshal(checkpointBytes, &checkpoint) != nil || checkpoint.Scope != expected.Scope {
						return e3record.Source{}, errors.New("synthetic checkpoint read failed")
					}
					journal, err := os.Open(filepath.Join(f.store.journalDirectory(), expected.Scope.RunRef+".jsonl"))
					if err != nil {
						return e3record.Source{}, err
					}
					summary, verifyErr := auditlog.Verify(ctx, journal, bytes.Repeat([]byte{0x71}, auditlog.KeyBytes), checkpoint)
					closeErr := journal.Close()
					if verifyErr != nil || closeErr != nil || summary.Intents != 2 || summary.Results != 2 {
						return e3record.Source{}, errors.New("synthetic final journal did not authenticate")
					}
					digest, err := ControlledReadCheckpointDigest(checkpoint)
					if err != nil {
						return e3record.Source{}, err
					}
					if mode == "checkpoint-mismatch" {
						digest[0] ^= 1
					}
					var fenceMode string
					var version uint64
					if err := f.m.pg.QueryRowContext(ctx, "SELECT mode,version FROM task_control_modes WHERE task_id=$1", request.TaskID).
						Scan(&fenceMode, &version); err != nil || fenceMode != "controlled" {
						return e3record.Source{}, errors.New("synthetic controlled fence missing")
					}
					if mode == "late-pause" {
						enterOnce.Do(func() { close(sourceEntered) })
						select {
						case <-sourceRelease:
						case <-ctx.Done():
							return e3record.Source{}, ctx.Err()
						}
					}
					// The test owner issued this fixture revision and generation.
					// The production host must supply its own authenticated producers.
					return e3record.Source{Expected: expected, ExplorationID: f.task.ExpID,
						ModeVersion: version, EvidenceRevision: revision.Load(),
						KGSnapshotRef: "synthetic-kg-snapshot-v1", KGSnapshotDigest: kgDigest,
						GenerationRef: "synthetic-harness-generation", InstanceRef: "synthetic-instance",
						ResetGeneration: "synthetic-reset-generation", ModelTag: "synthetic-<>&-모델-\u2028\u2029",
						Policy: policy, CanaryFields: []string{"note"}, Severity: "medium",
						FinalCheckpointRef:    "checkpoint:" + checkpoint.JournalID,
						FinalCheckpointDigest: digest, FinalCheckpointScope: checkpoint.Scope,
						FinalCheckpointSequence: checkpoint.Sequence}, nil
				}
				if mode == "missing-source" {
					source = nil
				}
				return ControlledReadFindingPlan{Expected: expected,
					Candidate: idor.Candidate{Owner: owner, Evaluated: peer, ResourceRef: request.ResourceRef},
					KG:        kg, Source: source}, nil
			}
			if mode == "unknown-commit-source-change" || mode == "storage-rejected" {
				markers, markerErr := f.s.ControlledTaskModeMarkers()
				if markerErr != nil {
					t.Fatal(markerErr)
				}
				err = f.s.ConfigureControlledReadVerification(func(ctx context.Context, request ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
					plan, err := resolve(ctx, request)
					if err != nil {
						return ControlledReadVerificationPlan{}, err
					}
					collector, err := newControlledFindingCollector(f.m.pg, markers, request, plan)
					if err != nil {
						return ControlledReadVerificationPlan{}, err
					}
					store := collector.save
					collector.save = func(ctx context.Context, prepared e3record.Prepared, markers db.TaskModeMarkerReader) (db.ControlledFindingResult, error) {
						writes.Add(1)
						snapshot, valid := prepared.Snapshot()
						if !valid {
							return db.ControlledFindingResult{}, db.ErrControlledFindingInput
						}
						attemptedOperation = snapshot.OperationKey
						var originalState string
						if mode == "storage-rejected" {
							if err := f.m.pg.QueryRowContext(ctx, "SELECT state FROM exploration_nodes WHERE id=$1", request.IntentID).Scan(&originalState); err != nil {
								return db.ControlledFindingResult{}, err
							}
							// Test-owned DB corruption after Prepare must be rejected by the real writer.
							if _, err := f.m.pg.ExecContext(ctx, "UPDATE exploration_nodes SET kind='fact',state='confirmed' WHERE id=$1", request.IntentID); err != nil {
								return db.ControlledFindingResult{}, err
							}
						}
						result, err := store(ctx, prepared, markers)
						if mode == "storage-rejected" {
							if _, restoreErr := f.m.pg.ExecContext(ctx, "UPDATE exploration_nodes SET kind='intent',state=$2 WHERE id=$1", request.IntentID, originalState); restoreErr != nil {
								return db.ControlledFindingResult{}, restoreErr
							}
							return result, err
						}
						if err != nil {
							return result, err
						}
						// The transaction is real; only its acknowledgement is lost.
						return result, fmt.Errorf("%w: synthetic lost acknowledgement", db.ErrControlledFindingStore)
					}
					retryFinalize = collector.finalize
					verification := collector.verificationPlan()
					verification.Finalize = func(ctx context.Context, evidence ControlledReadVerificationEvidence) error {
						lastEvidence = evidence
						firstSaveError = collector.finalize(ctx, evidence)
						return firstSaveError
					}
					return verification, nil
				})
			} else {
				err = f.s.ConfigureControlledReadFindings(resolve)
			}
			if err != nil {
				t.Fatal(err)
			}
			originalResolve := f.e.controlledReads.verification.resolve
			f.e.controlledReads.verification.resolve = func(ctx context.Context, request ControlledReadVerificationRequest) (ControlledReadVerificationPlan, error) {
				plan, err := originalResolve(ctx, request)
				if err != nil {
					t.Logf("synthetic resolution refused: %v", err)
					return plan, err
				}
				collect, finalize := plan.Collect, plan.Finalize
				plan.Collect = func(ctx context.Context, evidence ControlledReadVerificationEvidence) error {
					collections.Add(1)
					err := collect(ctx, evidence)
					if err != nil {
						t.Logf("synthetic collection refused: %v", err)
					}
					return err
				}
				plan.Finalize = func(ctx context.Context, evidence ControlledReadVerificationEvidence) error {
					finalizations.Add(1)
					err := finalize(ctx, evidence)
					if err != nil {
						t.Logf("synthetic finalization refused: %v", err)
					}
					return err
				}
				return plan, nil
			}
			done := startVerifiedFixtureIntent(t, f)
			if mode == "late-pause" {
				select {
				case <-sourceEntered:
				case <-time.After(10 * time.Second):
					release()
					t.Fatal("verified storage did not reach the post-claim barrier")
				}
				f.e.Pause(f.task.ID, agent.AbortPausedByUser)
				if err := f.m.ApplyTaskPause(f.task.ID); err != nil {
					release()
					t.Fatal(err)
				}
				release()
			}
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				release()
				t.Fatal("synthetic controlled execution did not finish")
			}
			if mode == "source-error" || mode == "checkpoint-mismatch" || mode == "storage-rejected" {
				reason, key := "source_rejected", "unavailable"
				if mode == "storage-rejected" {
					if !errors.Is(firstSaveError, ErrControlledFindingUnavailable) ||
						!errors.Is(firstSaveError, db.ErrControlledFindingInput) ||
						attemptedOperation == ([sha256.Size]byte{}) || writes.Load() != 1 {
						t.Fatal("real database input rejection lost its unavailable classification or attempted operation")
					}
					reason, key = "storage_rejected", fmt.Sprintf("%x", attemptedOperation)
				}
				diagnosticBytes, readErr := os.ReadFile(diagnosticPath)
				if readErr != nil || bytes.Count(diagnosticBytes, []byte("finalization_failed")) != 1 ||
					!bytes.Contains(diagnosticBytes, []byte("reason="+reason+" operation_key="+key)) {
					t.Fatal("actual execution finish did not distinguish preparation from database rejection")
				}
				for _, forbidden := range []string{"SYNTHETIC_PRIVATE_SOURCE_ERROR", runtimeSecret, verificationTestMarker} {
					if bytes.Contains(diagnosticBytes, []byte(forbidden)) {
						t.Fatal("private source or observed data reached the finalization diagnostic")
					}
				}
			}
			if mode == "unknown-commit-source-change" {
				if !errors.Is(firstSaveError, ErrControlledFindingOutcomeUnknown) || retryFinalize == nil {
					t.Fatalf("committed but lost acknowledgement was misclassified: %v", firstSaveError)
				}
				var diagnostic controlledFindingStoreFailure
				if !errors.As(firstSaveError, &diagnostic) || attemptedOperation == ([sha256.Size]byte{}) || diagnostic.operation != attemptedOperation {
					t.Fatal("uncertain commit lost its stable operation key")
				}
				diagnosticBytes, readErr := os.ReadFile(diagnosticPath)
				if readErr != nil || !bytes.Contains(diagnosticBytes, []byte(fmt.Sprintf("reason=storage_outcome_unknown operation_key=%x", attemptedOperation))) {
					t.Fatal("actual execution finish lost the reconciliation diagnostic")
				}
				revision.Store(2)
				retryCtx, cancelRetry := context.WithTimeout(context.Background(), 2*time.Second)
				err := retryFinalize(retryCtx, lastEvidence)
				cancelRetry()
				if !errors.Is(err, ErrControlledFindingConflict) || writes.Load() != 1 {
					t.Fatalf("changed retry reached a second write: %v writes=%d", err, writes.Load())
				}
				logControlledFindingFinalizationFailure(err)
				diagnosticBytes, readErr = os.ReadFile(diagnosticPath)
				if readErr != nil || bytes.Count(diagnosticBytes, []byte("finalization_failed")) != 2 ||
					!bytes.Contains(diagnosticBytes, []byte(fmt.Sprintf("reason=storage_conflict operation_key=%x", attemptedOperation))) {
					t.Fatal("collector conflict lost the original attempted operation key")
				}
			}
			var findings, nodes, records int
			id := taskID
			if err := f.m.pg.QueryRow("SELECT count(*) FROM findings WHERE task_id=$1", id).Scan(&findings); err != nil {
				t.Fatal(err)
			}
			if err := f.m.pg.QueryRow("SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='finding' AND state='confirmed'", f.task.ExpID).Scan(&nodes); err != nil {
				t.Fatal(err)
			}
			if err := f.m.pg.QueryRow("SELECT count(*) FROM e3_verdict_issuances WHERE task_id=$1", id).Scan(&records); err != nil {
				t.Fatal(err)
			}
			success := mode == "confirmed" || mode == "late-pause"
			if success {
				if findings != 1 || nodes != 1 || records != 1 {
					t.Fatalf("synthetic stored counts: findings=%d nodes=%d records=%d hits=%d resolve=%d collect=%d finalize=%d source=%d", findings, nodes, records, hits.Load(), resolved.Load(), collections.Load(), finalizations.Load(), sourceCalls.Load())
				}
				var payload, manifest []byte
				if err := f.m.pg.QueryRow("SELECT v.payload_canonical,m.canonical FROM e3_verdict_issuances i JOIN e3_oracle_verdicts v ON v.id=i.verdict_id JOIN e3_evidence_manifests m ON m.id=i.manifest_id WHERE i.task_id=$1", id).Scan(&payload, &manifest); err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range []string{runtimeSecret, verificationTestMarker} {
					if bytes.Contains(payload, []byte(forbidden)) || bytes.Contains(manifest, []byte(forbidden)) {
						t.Fatal("raw private data or canary value reached persistent evidence")
					}
				}
				intent, err := f.task.Store.GetNode(f.intent.ID)
				if err != nil || intent == nil || intent.State != "done" {
					t.Fatalf("durable finding and final intent disagree: state=%v err=%v", intent, err)
				}
				if mode == "confirmed" {
					assertPracticalReportFromExecutedE3(t, f.s, taskID, &hits)
				}
				if mode == "late-pause" {
					var paused, queued bool
					if err := f.m.pg.QueryRow("SELECT paused,queued FROM tasks WHERE id=$1", id).Scan(&paused, &queued); err != nil {
						t.Fatal(err)
					}
					if !paused || queued || !f.task.lifecycleSnapshot().Paused || !f.e.IsPaused(f.task.ID) {
						t.Fatal("late pause did not preserve the persisted and runtime task pause")
					}
				}
			} else if mode == "unknown-commit-source-change" {
				if findings != 1 || nodes != 1 || records != 1 {
					t.Fatalf("lost acknowledgement did not preserve exactly one committed finding: %d/%d/%d", findings, nodes, records)
				}
			} else if findings != 0 || nodes != 0 || records != 0 {
				t.Fatalf("untrusted source persisted: findings=%d nodes=%d records=%d", findings, nodes, records)
			}
			wantHits := int64(2)
			if mode == "missing-source" {
				wantHits = 0
			}
			if hits.Load() != wantHits {
				t.Fatalf("synthetic target requests=%d want=%d", hits.Load(), wantHits)
			}
		})
	}
}

func mustSyntheticTaskID(t *testing.T, task string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(task, 10, 64)
	if err != nil || id <= 0 {
		t.Fatalf("invalid synthetic task ref: %v", err)
	}
	return id
}
