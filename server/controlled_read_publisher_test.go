package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
func (l *durableTestLease) Release() {
	h := l.host
	if l.access.Purpose != auditcheckpoint.HeadAccess {
		return
	}
	h.mu.Lock()
	cp := h.lastCP
	h.mu.Unlock()
	match := h.mode == "header-sidecar" && cp.Sequence == 0 || h.mode == "intent-sidecar" && cp.Sequence == 1 || h.mode == "final-sidecar" && h.seals.Load() > 0
	if match && h.fileError.CompareAndSwap(false, true) {
		path := filepath.Join(h.dir, cp.JournalID+".checkpoint")
		if _, err := os.Lstat(path); err == nil {
			if err := os.Rename(path, path+".preserved"); err != nil {
				panic("synthetic sidecar fault")
			}
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			panic("synthetic sidecar fault")
		}
	}
}

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
	if server, err := NewWithDurableControlledReadHost(context.Background(), nil, "", "", "", ControlledReadDurableHostOptions{}); server != nil || err == nil {
		t.Fatal("strict constructor fell back without factory")
	}
	data, err := json.Marshal(ControlledReadPublisherPlan{Host: &durableTestHost{}, PublisherRef: "private-ref", Epoch: auditcheckpoint.Epoch{StoreRef: "private-store"}})
	if err != nil || string(data) != "{}" {
		t.Fatal("Go host capability leaked to JSON")
	}
}
