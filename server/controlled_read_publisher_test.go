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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditpublisher"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
	"github.com/google/uuid"
)

// These fake authorities/keys/metadata and owned loopback/PG fixtures do not
// authenticate any real operator, store epoch, root namespace or Juice Shop.
type durableTestHost struct {
	epoch         auditcheckpoint.Epoch
	key           []byte
	ctx           context.Context
	mode          string
	dir           string
	mu            sync.Mutex
	last          auditcheckpoint.Binding
	lastCP        auditlog.Checkpoint
	revoked       atomic.Bool
	seals         atomic.Int64
	rootReleased  atomic.Int64
	rootCancel    context.CancelFunc
	afterSeal     func()
	fileError     atomic.Bool
	sourceEntered chan struct{}
	sourceRelease chan struct{}
	sealEntered   chan struct{}
	sealRelease   chan struct{}
	sealBudget    chan time.Duration
	sourceCalls   atomic.Int64
}
type durableTestLease struct {
	host   *durableTestHost
	access auditcheckpoint.Access
}

func (h *durableTestHost) revokeRoot() {
	h.mu.Lock()
	cancel := h.rootCancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (h *durableTestHost) Acquire(ctx context.Context, a auditcheckpoint.Access) (auditcheckpoint.Lease, error) {
	if ctx.Err() != nil || a.Epoch != h.epoch {
		return nil, errors.New("synthetic authority mismatch")
	}
	if a.Purpose == auditcheckpoint.PublishAccess {
		r := a.Publication
		if h.mode == "header" && r.Kind == auditcheckpoint.Register || h.mode == "intent" && r.Candidate.Sequence == 1 || h.mode == "result" && r.Candidate.Sequence == 2 || h.mode == "seal" && r.Kind == auditcheckpoint.Seal {
			return nil, errors.New("synthetic private auth error")
		}
		h.mu.Lock()
		h.last = a.Binding
		h.lastCP = r.Candidate
		h.mu.Unlock()
		if r.Kind == auditcheckpoint.Seal {
			h.seals.Add(1)
			if h.sealBudget != nil {
				deadline, ok := ctx.Deadline()
				if !ok {
					h.sealBudget <- 0
				} else {
					h.sealBudget <- time.Until(deadline)
				}
			}
			if h.sealEntered != nil {
				close(h.sealEntered)
				select {
				case <-h.sealRelease:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
	}
	return &durableTestLease{h, a}, nil
}
func (l *durableTestLease) Context() context.Context     { return l.host.ctx }
func (l *durableTestLease) Epoch() auditcheckpoint.Epoch { return l.host.epoch }
func (l *durableTestLease) ResolveKey(context.Context, auditcheckpoint.Binding) ([]byte, error) {
	if l.host.revoked.Load() {
		return nil, errors.New("synthetic key revoked")
	}
	return l.host.key, nil
}
func (l *durableTestLease) Release() {}

type durableTestRoot struct {
	h      *durableTestHost
	anchor *os.File
	ctx    context.Context
	once   sync.Once
}

func (r *durableTestRoot) Context() context.Context { return r.ctx }
func (r *durableTestRoot) Anchor() *os.File         { return r.anchor }
func (r *durableTestRoot) Directory() string        { return r.h.dir }
func (r *durableTestRoot) Release() {
	r.once.Do(func() {
		r.h.rootReleased.Add(1)
		r.h.revokeRoot()
		_ = r.anchor.Close()
		if r.h.seals.Load() > 0 && r.h.afterSeal != nil {
			r.h.afterSeal()
		}
	})
}

func installDurableTestPublisher(t *testing.T, f *controlledReadDBFixture, mode string) *durableTestHost {
	t.Helper()
	h := &durableTestHost{epoch: auditcheckpoint.Epoch{StoreRef: "synthetic:" + uuid.NewString(), EpochRef: "epoch:synthetic", Generation: 1, AnchorDigest: sha256.Sum256([]byte("synthetic external anchor"))}, key: bytes.Repeat([]byte{0x71}, auditlog.KeyBytes), ctx: f.s.ctx, mode: mode, dir: f.store.journalDirectory()}
	if err := auditcheckpoint.Migrate(context.Background(), f.m.pg.DB); err != nil {
		t.Fatal(err)
	}
	e := h.epoch
	if _, err := f.m.pg.Exec(`INSERT INTO audit_checkpoint_epochs VALUES($1,$2,$3,$4)`, e.StoreRef, e.EpochRef, int64(e.Generation), e.AnchorDigest[:]); err != nil {
		t.Fatal("synthetic epoch fixture")
	}
	if mode == "epoch" {
		if _, err := f.m.pg.Exec(`UPDATE audit_checkpoint_epochs SET generation=2 WHERE store_ref=$1`, e.StoreRef); err != nil {
			t.Fatal("synthetic epoch fault")
		}
	}
	if mode == "late-pause" {
		h.sourceEntered = make(chan struct{})
		h.sourceRelease = make(chan struct{})
	}
	if mode == "cancel-before-claim" {
		h.sealEntered = make(chan struct{})
		h.sealRelease = make(chan struct{})
	}
	if err := f.s.ConfigureControlledReadPublisher(func(ctx context.Context, r ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error) {
		if mode == "factory-error" {
			return ControlledReadPublisherPlan{}, errors.New("synthetic factory refused")
		}
		if r.IntentID != f.intent.ID || r.Scope.TaskRef != f.task.ID || r.Filename != r.Scope.RunRef+".jsonl" {
			return ControlledReadPublisherPlan{}, errors.New("synthetic binding mismatch")
		}
		anchor, err := os.Open(h.dir)
		if err != nil {
			return ControlledReadPublisherPlan{}, err
		}
		rootCtx, cancel := context.WithCancel(f.s.ctx)
		h.mu.Lock()
		h.rootCancel = cancel
		h.mu.Unlock()
		root := &durableTestRoot{h: h, anchor: anchor, ctx: rootCtx}
		return ControlledReadPublisherPlan{Host: h, Epoch: h.epoch, PublisherRef: "publisher:synthetic", RootScope: root, Limits: auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 16, Timeout: 3 * time.Second}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	config := f.e.controlledReads.publisher
	save := config.saveCheckpoint
	config.saveCheckpoint = func(ctx context.Context, cp auditlog.Checkpoint) error {
		match := mode == "header-sidecar" && cp.Sequence == 0 ||
			mode == "intent-sidecar" && cp.Sequence == 1 ||
			mode == "final-sidecar" && h.seals.Load() > 0
		if match && h.fileError.CompareAndSwap(false, true) {
			// Keep a valid header sidecar too: ignoring this error must not fail
			// later merely because a reader cannot find the original file.
			if mode == "header-sidecar" {
				if err := save(ctx, cp); err != nil {
					return err
				}
			}
			return errControlledReadStorage
		}
		return save(ctx, cp)
	}
	return h
}

func installDurableTestFinding(t *testing.T, f *controlledReadDBFixture, h *durableTestHost, mode string) {
	t.Helper()
	store, err := auditcheckpoint.New(f.m.pg.DB, h, auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 16, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := f.s.NewControlledReadAuditSource(ControlledReadAuditSourceOptions{
		LatestCheckpoint: func(ctx context.Context, scope transmit.Scope) (ControlledReadAuditAnchor, error) {
			h.mu.Lock()
			b := h.last
			h.mu.Unlock()
			current, err := store.Head(ctx, auditcheckpoint.Query{Epoch: h.epoch, Binding: b})
			if err != nil || current.Phase != auditcheckpoint.Sealed || current.Checkpoint.Scope != scope {
				return ControlledReadAuditAnchor{}, errors.New("synthetic current sealed head unavailable")
			}
			return ControlledReadAuditAnchor{Checkpoint: current.Checkpoint, Ref: fmt.Sprintf("synthetic-cpd:%s:%d", current.Checkpoint.JournalID, current.Revision)}, nil
		},
		ResolveKey: func(context.Context, ControlledReadAuditKeyRequest) ([]byte, error) {
			if mode == "source-key" {
				return nil, errors.New("synthetic consumption revoked")
			}
			return h.key, nil
		}, MaxJournalBytes: 1 << 20, MaxEvents: 16, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone, HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond), ValidUntil: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)}
	owner := idor.AccountRef{Label: "owner", Handle: "session-1", Status: oracle.SubjectVerified}
	peer := idor.AccountRef{Label: "peer", Handle: "session-2", Status: oracle.SubjectVerified}
	if err := f.s.ConfigureControlledReadFindings(func(ctx context.Context, r ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
		expected := evidencebind.Context{Scope: r.Scope, IntentID: r.IntentID, ResourceRef: r.ResourceRef, ReadApprovalRef: "read-approval", KGPolicyVersion: "synthetic-kg-policy", Owner: owner, Evaluated: peer}
		metadata := func(ctx context.Context, actual evidencebind.Context) (e3record.Source, error) {
			h.sourceCalls.Add(1)
			if h.sourceEntered != nil {
				close(h.sourceEntered)
				select {
				case <-h.sourceRelease:
				case <-ctx.Done():
					return e3record.Source{}, ctx.Err()
				}
			}
			if mode == "source" || actual != expected {
				return e3record.Source{}, errors.New("synthetic metadata refused")
			}
			var version uint64
			if err := f.m.pg.QueryRowContext(ctx, `SELECT version FROM task_control_modes WHERE task_id=$1`, r.TaskID).Scan(&version); err != nil {
				return e3record.Source{}, err
			}
			return e3record.Source{Expected: expected, ExplorationID: f.task.ExpID, ModeVersion: version, EvidenceRevision: 1, KGSnapshotRef: "synthetic-kg", KGSnapshotDigest: sha256.Sum256([]byte("synthetic fixed KG")), GenerationRef: "synthetic-harness", InstanceRef: "synthetic-instance", ResetGeneration: "synthetic-reset", ModelTag: "synthetic-model", Policy: policy, CanaryFields: []string{"note"}, Severity: "medium"}, nil
		}
		source, err := reader.SourceReader(metadata)
		if err != nil {
			return ControlledReadFindingPlan{}, err
		}
		return ControlledReadFindingPlan{Expected: expected, Candidate: idor.Candidate{Owner: owner, Evaluated: peer, ResourceRef: r.ResourceRef}, KG: &verificationTestKG{policy: policy}, Source: source}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDurableAuditServerSyntheticFindingsAndFailures(t *testing.T) {
	for _, mode := range []string{"success", "late-pause", "cancel-before-claim", "factory-error", "header", "intent", "result", "seal", "header-sidecar", "intent-sidecar", "final-sidecar", "source", "source-key", "root-revoke", "key-revoke", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			var stages *durableStageLog
			if strings.HasSuffix(mode, "-sidecar") {
				stages = &durableStageLog{}
				previous := log.Writer()
				log.SetOutput(stages)
				defer log.SetOutput(previous)
			}
			var hits atomic.Int64
			var host *durableTestHost
			target := controlledReadTarget(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := hits.Add(1)
				if mode == "root-revoke" && n == 1 {
					host.revokeRoot()
				}
				if mode == "key-revoke" && n == 1 {
					host.revoked.Store(true)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"ok","data":{"note":%q}}`, verificationTestMarker)
			}))
			f := newControlledVerificationDBFixture(t, target.URL)
			host = installDurableTestPublisher(t, f, mode)
			installDurableTestFinding(t, f, host, mode)
			var releaseOnce sync.Once
			release := func() {
				releaseOnce.Do(func() {
					if host.sourceRelease != nil {
						close(host.sourceRelease)
					}
					if host.sealRelease != nil {
						close(host.sealRelease)
					}
				})
			}
			defer release()
			done := startVerifiedFixtureIntent(t, f)
			if mode == "late-pause" {
				select {
				case <-host.sourceEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("fresh SourceReader did not reach post-claim barrier")
				}
				if _, err := f.s.applyTaskControl(f.task, "pause"); err != nil {
					t.Fatal(err)
				}
				if err := f.e.ControlWork(context.Background(), f.intent.ID, "cancel"); !errors.Is(err, errWorkControlConflict) {
					t.Fatal("late cancellation changed completion claim")
				}
				release()
			}
			if mode == "cancel-before-claim" {
				select {
				case <-host.sealEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("Seal did not reach pre-claim barrier")
				}
				cancelled := make(chan struct{})
				var once sync.Once
				f.e.workMu.Lock()
				run := f.e.work[f.intent.ID]
				original := run.cancel
				run.cancel = func(cause error) { original(cause); once.Do(func() { close(cancelled) }) }
				f.e.workMu.Unlock()
				control := make(chan error, 1)
				go func() { control <- f.e.ControlWork(context.Background(), f.intent.ID, "cancel") }()
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("cancellation did not win before completion claim")
				}
				release()
				select {
				case err := <-control:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancel did not drain")
				}
			}
			select {
			case <-done:
			case <-time.After(12 * time.Second):
				t.Fatal("durable synthetic run did not settle")
			}
			var findings int
			if err := f.m.pg.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, mustSyntheticTaskID(t, f.task.ID)).Scan(&findings); err != nil {
				t.Fatal(err)
			}
			node, err := f.task.Store.GetNode(f.intent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "success" || mode == "late-pause" {
				if findings != 1 || node.State != "done" || hits.Load() != 2 {
					t.Fatalf("success counts: finding=%d state=%s hits=%d", findings, node.State, hits.Load())
				}
				host.mu.Lock()
				b := host.last
				host.mu.Unlock()
				store, err := auditcheckpoint.New(f.m.pg.DB, host, auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 16, Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				head, err := store.Head(context.Background(), auditcheckpoint.Query{Epoch: host.epoch, Binding: b})
				if err != nil || head.Phase != auditcheckpoint.Sealed || head.Revision != 6 || head.Checkpoint.Sequence != 4 {
					t.Fatalf("sealed head incorrect: %+v %v", head, err)
				}
				body, err := os.ReadFile(filepath.Join(host.dir, head.Checkpoint.JournalID+".checkpoint"))
				if err != nil {
					t.Fatal(err)
				}
				var cp auditlog.Checkpoint
				if json.Unmarshal(body, &cp) != nil || cp != head.Checkpoint {
					t.Fatal("protected sidecar and durable sealed head diverged")
				}
			} else if findings != 0 || node.State == "done" {
				t.Fatalf("failed run saved finding/done: %d %s", findings, node.State)
			}
			if mode == "factory-error" || mode == "header" || mode == "intent" || mode == "header-sidecar" || mode == "intent-sidecar" || mode == "epoch" {
				if hits.Load() != 0 {
					t.Fatal("target received request before durable intent and sidecar ACK")
				}
			}
			if mode == "cancel-before-claim" && host.sourceCalls.Load() != 0 {
				t.Fatal("pre-claim cancellation reached finding source")
			}
			if mode == "result" || mode == "key-revoke" {
				if hits.Load() != 1 {
					t.Fatalf("result failure lost actual send count: %d", hits.Load())
				}
			}
			if mode != "factory-error" {
				journals, err := filepath.Glob(filepath.Join(host.dir, "*.jsonl"))
				if err != nil || len(journals) != 1 {
					t.Fatal("failed/closed original journal was lost")
				}
				if host.rootReleased.Load() != 1 {
					t.Fatalf("writer root release count %d", host.rootReleased.Load())
				}
			}
			if mode == "header-sidecar" || mode == "intent-sidecar" || mode == "final-sidecar" {
				if !host.fileError.Load() {
					t.Fatal("sidecar fault injection did not execute")
				}
				category, phase, revision, sidecarSequence := "header_sidecar", auditcheckpoint.Open, uint64(1), uint64(0)
				if mode == "intent-sidecar" {
					category, revision = "record_sidecar", 2
				}
				if mode == "final-sidecar" {
					category, phase, revision, sidecarSequence = "final_sidecar", auditcheckpoint.Sealed, 6, 4
					if hits.Load() != 2 {
						t.Fatal("final sidecar failure did not preserve completed HTTP observations")
					}
				}
				if !strings.Contains(stages.String(), "[controlled-read] publisher_failed reason="+category) ||
					strings.Contains(stages.String(), "synthetic private auth error") || host.sourceCalls.Load() != 0 {
					t.Fatal("sidecar failure was masked by a downstream failure or leaked raw errors")
				}
				host.mu.Lock()
				binding := host.last
				host.mu.Unlock()
				checkpoints, err := auditcheckpoint.New(f.m.pg.DB, host, auditlog.ExtensionLimits{MaxBytes: 1 << 20, MaxEvents: 16, Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				head, err := checkpoints.Head(context.Background(), auditcheckpoint.Query{Epoch: host.epoch, Binding: binding})
				if err != nil || head.Phase != phase || head.Revision != revision {
					t.Fatal("sidecar fault did not stop at the intended durable phase")
				}
				path := filepath.Join(host.dir, head.Checkpoint.JournalID+".checkpoint")
				info, err := os.Stat(path)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
					t.Fatal("sidecar fault removed the valid existing file")
				}
				body, err := os.ReadFile(path)
				var retained auditlog.Checkpoint
				if err != nil || json.Unmarshal(body, &retained) != nil || retained.Sequence != sidecarSequence || retained.Scope != head.Checkpoint.Scope {
					t.Fatal("sidecar fault did not preserve the expected readable checkpoint")
				}
			}
		})
	}
}

func TestDurableAuditOpaqueHTTPDrainAndCloseWitness(t *testing.T) {
	scope := transmit.Scope{RunRef: "run:synthetic", TaskRef: "task", CaseRef: "case", ResetRef: "reset", PolicyRef: "policy"}
	w := &controlledHTTPDrain{scope: scope}
	if w.check(scope) {
		t.Fatal("zero witness authorized drain")
	}
	// Pointer identity matters: a different transmitter cannot arm this witness.
	tx, other := &transmit.HTTPTransmitter{}, &transmit.HTTPTransmitter{}
	w.bind(tx)
	w.stopAndWait(other)
	if w.check(scope) {
		t.Fatal("other transmitter supplied drain")
	}
	wrong := scope
	wrong.RunRef = "other"
	if w.check(wrong) {
		t.Fatal("drain crossed scope")
	}
	h := &controlledCheckpointHost{epoch: auditcheckpoint.Epoch{StoreRef: "store"}, scope: scope, keyRef: "key", publisher: "publisher", drain: w, close: &auditpublisher.CloseWitness{}, journalID: "journal"}
	a := auditcheckpoint.Access{Purpose: auditcheckpoint.PublishAccess, Epoch: h.epoch, Binding: auditcheckpoint.Binding{PublisherRef: "publisher", Scope: scope, JournalID: "journal", KeyRef: "key"}, Publication: auditcheckpoint.Request{Kind: auditcheckpoint.Seal}}
	if _, err := h.Acquire(context.Background(), a); err == nil {
		t.Fatal("caller-created Close witness authorized Seal")
	}
}

func TestDurableAuditConstructorAndConfigAreNotJSONAuthority(t *testing.T) {
	data, err := json.Marshal(ControlledReadPublisherPlan{Host: &durableTestHost{}, PublisherRef: "private-ref", Epoch: auditcheckpoint.Epoch{StoreRef: "private-store"}})
	if err != nil || string(data) != "{}" {
		t.Fatal("Go host capability leaked to JSON")
	}
}

func TestDurableAuditStrictConstructorUsesFactoryWithValidHost(t *testing.T) {
	for _, mode := range []string{"nil", "success", "factory-error"} {
		t.Run(mode, func(t *testing.T) {
			var hits, calls atomic.Int64
			target := controlledReadTarget(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			f := newControlledVerificationDBFixture(t, target.URL)
			h := installDurableTestPublisher(t, f, "success")
			original := f.e.controlledReads.publisher.factory
			modeHost := f.e.controlledReads.mode.host
			t.Setenv("ARTEX_E2_SNAPSHOT_DIR", f.store.snapshotDir)
			t.Setenv("ARTEX_E2_STATE_DIR", f.store.stateDir)
			t.Setenv("ARTEX_LLM_API_KEY", "")
			t.Setenv(notifyBackgroundDisabledEnv, "1")
			// Fixed isolated values avoid issuing auth keys or setup tokens.
			// No authentication endpoint is invoked.
			previousPassHash, hadPassHash, err := f.m.pg.GetSetting(authPassKey)
			if err != nil {
				t.Fatal("auth setting snapshot unavailable")
			}
			// Restore before fixture cleanup closes the shared test DB, including
			// the early nil-factory return and failed construction paths.
			t.Cleanup(func() {
				var restoreErr error
				if hadPassHash {
					restoreErr = f.m.pg.SetSetting(authPassKey, previousPassHash)
				} else {
					_, restoreErr = f.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authPassKey)
				}
				if restoreErr != nil {
					t.Error("auth setting restore failed")
				}
			})
			if err := f.m.pg.SetSetting(authPassKey, "synthetic-disabled-login"); err != nil {
				t.Fatal(err)
			}
			keyDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), bytes.Repeat([]byte("s"), 32), 0o600); err != nil {
				t.Fatal(err)
			}
			factory := ControlledReadPublisherFactory(func(ctx context.Context, req ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error) {
				calls.Add(1)
				if mode == "factory-error" {
					return ControlledReadPublisherPlan{}, errors.New("synthetic-private-factory-error")
				}
				return original(ctx, req)
			})
			if mode == "nil" {
				factory = nil
			}
			lifecycle, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			s, err := NewWithDurableControlledReadHost(lifecycle, f.m, t.TempDir(), t.TempDir(), keyDir, ControlledReadDurableHostOptions{
				Host: ControlledReadHostOptions{Mode: modeHost, Findings: func(context.Context, ControlledReadVerificationRequest) (ControlledReadFindingPlan, error) {
					return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
				}}, Publisher: factory,
			})
			if mode == "nil" {
				if s != nil || !errors.Is(err, ErrControlledReadHostConfiguration) {
					t.Fatalf("nil factory accepted with otherwise valid host: %v", err)
				}
				return
			}
			if err != nil || s == nil || s.engine.controlledReads.publisher == nil || s.engine.controlledReads.verification == nil {
				t.Fatalf("valid strict bootstrap did not install publisher and findings: %v", err)
			}
			f.s, f.e, f.store = s, s.engine, s.engine.controlledReads.store
			// Only the finite fake Worker fixture owns execution.
			f.e.started.Store(f.task.ID, true)
			f.e.Pause(f.task.ID, errors.New("synthetic paused"))
			f.registerAndResume(t)
			if err := f.intentState("running"); err != nil {
				t.Fatal(err)
			}
			_, finish, err := f.e.prepareControlledRead(context.Background(), f.task, f.intent)
			if mode == "factory-error" {
				if err == nil || finish != nil || calls.Load() != 1 || hits.Load() != 0 {
					t.Fatalf("failed factory fell back: err=%v factory=%d hits=%d", err, calls.Load(), hits.Load())
				}
				journals, globErr := filepath.Glob(filepath.Join(h.dir, "*.jsonl"))
				if globErr != nil || len(journals) != 0 {
					t.Fatal("factory failure created a local-only journal")
				}
				return
			}
			if err != nil || finish == nil || calls.Load() != 1 {
				t.Fatalf("execution did not use installed factory: %v / %d", err, calls.Load())
			}
			if err := finish(false); err == nil {
				t.Fatal("test cleanup sealed unexecuted work")
			}
			var registrations int
			if err := f.m.pg.QueryRow("SELECT count(*) FROM audit_checkpoint_history WHERE store_ref=$1", h.epoch.StoreRef).Scan(&registrations); err != nil || registrations != 1 || h.rootReleased.Load() != 1 {
				t.Fatalf("strict execution did not durably register/abort its publisher: %v", err)
			}
		})
	}
}

func TestDurableAuditConfigureRequiresModeAndIdleRuntime(t *testing.T) {
	f := newControlledVerificationDBFixture(t, "http://127.0.0.1:1")
	r := f.e.controlledReads
	factory := ControlledReadPublisherFactory(func(context.Context, ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error) {
		t.Fatal("configuration invoked execution factory")
		return ControlledReadPublisherPlan{}, ErrControlledReadPublisher
	})
	mode := r.mode
	r.mode = nil
	if err := f.s.ConfigureControlledReadPublisher(factory); !errors.Is(err, ErrControlledReadPublisher) {
		t.Fatal("publisher configured without task mode")
	}
	r.mode = mode
	key := controlledWorkKey{task: f.task.ID, intent: f.intent.ID}
	r.pending[key] = controlledReadSpec{}
	if err := f.s.ConfigureControlledReadPublisher(factory); !errors.Is(err, ErrControlledReadPublisher) {
		t.Fatal("publisher configured after pending admission")
	}
	delete(r.pending, key)
	r.active[key] = true
	if err := f.s.ConfigureControlledReadPublisher(factory); !errors.Is(err, ErrControlledReadPublisher) {
		t.Fatal("publisher configured during active execution")
	}
	delete(r.active, key)
	if err := f.s.ConfigureControlledReadPublisher(factory); err != nil {
		t.Fatalf("idle configured-mode control failed: %v", err)
	}
}

type mismatchedPublisherRoot struct {
	auditpublisher.RootScope
	dir string
}

func (r mismatchedPublisherRoot) Directory() string { return r.dir }

func TestDurableAuditRejectsDifferentJournalDirectory(t *testing.T) {
	f := newControlledVerificationDBFixture(t, "http://127.0.0.1:1")
	h := installDurableTestPublisher(t, f, "success")
	stages := &durableStageLog{}
	previous := log.Writer()
	log.SetOutput(stages)
	t.Cleanup(func() { log.SetOutput(previous) })
	r := f.e.controlledReads
	original := r.publisher.factory
	r.publisher.factory = func(ctx context.Context, req ControlledReadPublisherRequest) (ControlledReadPublisherPlan, error) {
		p, err := original(ctx, req)
		p.RootScope = mismatchedPublisherRoot{RootScope: p.RootScope, dir: t.TempDir()}
		return p, err
	}
	spec, err := f.store.load(f.task.ID, f.intent.ID, "approved")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(spec.Key)
	scope := transmit.Scope{RunRef: "synthetic-directory", TaskRef: f.task.ID, CaseRef: spec.CaseRef, ResetRef: spec.ResetRef, PolicyRef: spec.PolicyRef}
	if journal, err := r.openPublishedJournal(context.Background(), r.publisher, spec, scope); journal != nil || !errors.Is(err, ErrControlledReadPublisher) || h.rootReleased.Load() != 1 {
		t.Fatalf("different journal directory accepted or root leaked: %v", err)
	}

	// The downstream Open also rejects a different directory. Pin the local
	// category so removing this guard cannot hide behind that later rejection.
	if strings.Count(stages.String(), "[controlled-read] publisher_failed reason=root_scope") != 1 {
		t.Fatal("directory guard was masked by a downstream publisher failure")
	}
}

func TestDurableAuditRootReleaseStopsLifecycleCallbackOnce(t *testing.T) {
	var stopped, cancelled atomic.Int64
	h := &durableTestHost{}
	anchor, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := &controlledPublisherRoot{
		real:   &durableTestRoot{h: h, anchor: anchor},
		stop:   func() bool { stopped.Add(1); return true },
		cancel: func() { cancelled.Add(1) },
	}
	root.Release()
	root.Release()
	if stopped.Load() != 1 || cancelled.Load() != 1 || h.rootReleased.Load() != 1 {
		t.Fatal("Release failed to stop the callback and release each owned resource once")
	}
}

type durableStageLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *durableStageLog) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(b)
}

func (w *durableStageLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}
