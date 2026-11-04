package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

type finalAuditSourceFixture struct {
	s                   *Server
	store               *controlledReadStore
	scope               transmit.Scope
	anchor              ControlledReadAuditAnchor
	key                 []byte
	journal, checkpoint string
	options             ControlledReadAuditSourceOptions
	cancelLifecycle     context.CancelFunc
}

// Authority callbacks below belong to the owned synthetic fixture. They do not
// authenticate a production epoch, key registry, KG, identity or completion.
func newFinalAuditSourceFixture(t *testing.T) *finalAuditSourceFixture {
	t.Helper()
	root := t.TempDir()
	store, err := openControlledReadStore(filepath.Join(root, "snapshots"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	scope := transmit.Scope{TaskRef: "17", RunRef: "synthetic-final-audit", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1"}
	key := bytes.Repeat([]byte{0x61}, auditlog.KeyBytes)
	j, err := auditlog.Create(store.journalDirectory(), scope.RunRef+".jsonl", auditlog.Config{Scope: scope, KeyRef: "synthetic-key-1", Key: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	for i := uint64(1); i <= 2; i++ {
		at := time.Now().UTC()
		receipt := transmit.Receipt{Scope: scope, Attempt: i, AccountRef: "owner", SessionRef: "session-1", ResourceRef: "doc:one", ApprovalRef: "synthetic-approval", Method: "GET", At: at}
		if err := j.Record(context.Background(), transmit.Event{Stage: "intent", Receipt: receipt}); err != nil {
			t.Fatal(err)
		}
		receipt.Reason = transmit.Completed
		receipt.Admitted, receipt.RequestWritten, receipt.ResponseReceived = true, true, true
		receipt.AdmittedAt, receipt.StatusCode = at, 200
		if err := j.Record(context.Background(), transmit.Event{Stage: "result", Receipt: receipt}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	cp := j.Checkpoint()
	if err := store.saveCheckpoint(context.Background(), cp); err != nil {
		t.Fatal(err)
	}
	rootAnchor, err := os.Open(store.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rootAnchor.Close() })
	lifecycle, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := newControlledReadRuntime(store)
	r.mode = &controlledTaskModeConfig{lifecycle: lifecycle, host: ControlledTaskModeHost{RootPath: store.stateDir, RootAnchor: rootAnchor, CheckRoot: func(ctx context.Context) error { return ctx.Err() }}}
	f := &finalAuditSourceFixture{s: &Server{ctx: lifecycle, engine: &Engine{controlledReads: r}}, store: store, scope: scope,
		anchor: ControlledReadAuditAnchor{Checkpoint: cp, Ref: "synthetic-sealed-head"}, key: key,
		journal: filepath.Join(store.journalDirectory(), scope.RunRef+".jsonl"), checkpoint: filepath.Join(store.journalDirectory(), cp.JournalID+".checkpoint"), cancelLifecycle: cancel}
	f.options = ControlledReadAuditSourceOptions{MaxJournalBytes: 1 << 20, MaxEvents: 4, Timeout: time.Second,
		LatestCheckpoint: func(ctx context.Context, actual transmit.Scope) (ControlledReadAuditAnchor, error) {
			if ctx.Err() != nil || actual != f.scope {
				return ControlledReadAuditAnchor{}, errors.New("synthetic head scope refused")
			}
			return f.anchor, nil
		}, ResolveKey: func(ctx context.Context, actual ControlledReadAuditKeyRequest) ([]byte, error) {
			if ctx.Err() != nil || actual.Scope != f.scope || actual.KeyRef != cp.KeyRef || actual.JournalID != cp.JournalID {
				return nil, errors.New("synthetic key binding refused")
			}
			return key, nil
		}}
	return f
}

func (f *finalAuditSourceFixture) reader(t *testing.T) *ControlledReadAuditSource {
	t.Helper()
	r, err := f.s.NewControlledReadAuditSource(f.options)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFinalAuditSourceBindsOnlyVerifiedAuditMetadata(t *testing.T) {
	f := newFinalAuditSourceFixture(t)
	result, err := f.reader(t).Read(context.Background(), f.scope)
	if err != nil || !result.valid || result.intents != 2 || result.results != 2 {
		t.Fatalf("verified summary missing: %v", err)
	}
	source := e3record.Source{Expected: evidencebind.Context{Scope: f.scope}, ExplorationID: 1, KGSnapshotRef: "unchanged-kg", GenerationRef: "unchanged-harness", ModelTag: "unchanged-model"}
	bound, err := result.Bind(source)
	want := source
	want.FinalCheckpointRef, want.FinalCheckpointScope, want.FinalCheckpointSequence = f.anchor.Ref, f.scope, 4
	want.FinalCheckpointDigest, _ = ControlledReadCheckpointDigest(f.anchor.Checkpoint)
	gotJSON, _ := json.Marshal(bound)
	wantJSON, _ := json.Marshal(want)
	if err != nil || !bytes.Equal(gotJSON, wantJSON) || bound.Completed || bound.Finalized || bound.EvidenceDigest != ([32]byte{}) || !bound.RecordCreatedAt.IsZero() {
		t.Fatalf("audit read altered other source authority: %v", err)
	}
	if !bytes.Equal(f.key, bytes.Repeat([]byte{0x61}, auditlog.KeyBytes)) {
		t.Fatal("borrowed provider key was altered")
	}
	for _, change := range []struct {
		name   string
		mutate func(*e3record.Source)
	}{
		{"scope", func(s *e3record.Source) { s.Expected.Scope.TaskRef = "18" }},
		{"ref", func(s *e3record.Source) { s.FinalCheckpointRef = "other-head" }},
		{"digest", func(s *e3record.Source) { s.FinalCheckpointDigest[0] = 1 }},
		{"checkpoint-scope", func(s *e3record.Source) { s.FinalCheckpointScope.TaskRef = "18" }},
		{"sequence", func(s *e3record.Source) { s.FinalCheckpointSequence = 5 }},
	} {
		t.Run(change.name, func(t *testing.T) {
			in := source
			change.mutate(&in)
			got, err := result.Bind(in)
			if !errors.Is(err, ErrControlledReadAuditSource) || got.ExplorationID != 0 {
				t.Fatal("conflicting source was bound")
			}
		})
	}
	if _, err := (ControlledReadFinalAudit{}).Bind(source); !errors.Is(err, ErrControlledReadAuditSource) {
		t.Fatal("zero audit result was bound")
	}
}

func TestFinalAuditSourceRejectsUntrustedOrIncompleteInputs(t *testing.T) {
	for _, name := range []string{"head-error", "head-changed", "head-revoked", "key-error", "key-length", "wrong-key", "key-changed", "key-revoked", "scope", "run-path", "checkpoint-body", "journal-tamper", "journal-truncated", "journal-tail", "byte-limit", "event-limit", "journal-symlink", "checkpoint-symlink", "journal-fifo", "journal-hardlink", "file-mode", "directory-mode", "root-mode", "root-replaced", "journals-symlink", "root-trust-lost", "root-revoked-during-read", "root-mode-during-read", "cancelled", "timeout", "closed-runtime"} {
		t.Run(name, func(t *testing.T) {
			f := newFinalAuditSourceFixture(t)
			ctx := context.Background()
			scope := f.scope
			var headCalls, keyCalls int
			head, key := f.options.LatestCheckpoint, f.options.ResolveKey
			f.options.LatestCheckpoint = func(ctx context.Context, scope transmit.Scope) (ControlledReadAuditAnchor, error) {
				headCalls++
				if name == "head-error" || name == "head-revoked" && headCalls == 2 {
					return ControlledReadAuditAnchor{}, errors.New("PRIVATE-HEAD-ERROR")
				}
				// These providers deliberately do not validate the requested
				// scope, so only the adapter can reject the mismatch/path.
				if name == "scope" {
					return f.anchor, nil
				}
				if name == "run-path" {
					got := f.anchor
					got.Checkpoint.Scope = scope
					return got, nil
				}
				got, err := head(ctx, scope)
				if name == "head-changed" && headCalls == 2 {
					got.Ref = "changed-head"
				}
				return got, err
			}
			f.options.ResolveKey = func(ctx context.Context, request ControlledReadAuditKeyRequest) ([]byte, error) {
				keyCalls++
				if name == "key-error" || name == "key-revoked" && keyCalls == 2 {
					return nil, errors.New("PRIVATE-KEY-ERROR")
				}
				if name == "timeout" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				if name == "root-revoked-during-read" && keyCalls == 2 {
					f.s.engine.controlledReads.mode.host.CheckRoot = func(context.Context) error { return errors.New("synthetic root trust revoked after file verification") }
				}
				if name == "root-mode-during-read" && keyCalls == 2 {
					if err := os.Chmod(f.store.stateDir, 0o750); err != nil {
						t.Fatal(err)
					}
				}
				if name == "scope" || name == "run-path" {
					return f.key, nil
				}
				got, err := key(ctx, request)
				if name == "key-length" {
					return got[:len(got)-1], nil
				}
				if name == "wrong-key" || name == "key-changed" && keyCalls == 2 {
					return bytes.Repeat([]byte{0x62}, auditlog.KeyBytes), nil
				}
				return got, err
			}
			if name == "timeout" {
				f.options.Timeout = time.Millisecond
			}
			r := f.reader(t)
			write := func(path string, body []byte) {
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			read := func(path string) []byte {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			remove := func(path string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			chmod := func(path string, mode os.FileMode) {
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "scope":
				scope.TaskRef = "18"
			case "run-path":
				scope.RunRef = "../escape"
			case "checkpoint-body":
				write(f.checkpoint, append(read(f.checkpoint), []byte("{}")...))
			case "journal-tamper":
				b := read(f.journal)
				b[bytes.Index(b, []byte("owner"))] = 'x'
				write(f.journal, b)
			case "journal-truncated":
				b := read(f.journal)
				write(f.journal, b[:len(b)-1])
			case "journal-tail":
				write(f.journal, append(read(f.journal), []byte("{}\n")...))
			case "byte-limit":
				r.options.MaxJournalBytes = int64(len(read(f.journal)) - 1)
			case "event-limit":
				r.options.MaxEvents = 3
			case "journal-symlink", "checkpoint-symlink":
				path := f.journal
				if name == "checkpoint-symlink" {
					path = f.checkpoint
				}
				other := path + ".saved"
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			case "journal-fifo":
				remove(f.journal)
				if err := syscall.Mkfifo(f.journal, 0o600); err != nil {
					t.Fatal(err)
				}
			case "journal-hardlink":
				if err := os.Link(f.journal, f.journal+".link"); err != nil {
					t.Fatal(err)
				}
			case "file-mode":
				chmod(f.journal, 0o640)
			case "directory-mode":
				chmod(f.store.journalDirectory(), 0o750)
			case "root-mode":
				chmod(f.store.stateDir, 0o750)
			case "root-replaced":
				// Keep valid bytes, permissions and ownership; only the root
				// inode differs from the still-open trusted anchor.
				journal, checkpoint := read(f.journal), read(f.checkpoint)
				if err := os.Rename(f.store.stateDir, f.store.stateDir+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.store.stateDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.store.journalDirectory(), 0o700); err != nil {
					t.Fatal(err)
				}
				write(f.journal, journal)
				write(f.checkpoint, checkpoint)
			case "journals-symlink":
				path := f.store.journalDirectory()
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".saved", path); err != nil {
					t.Fatal(err)
				}
			case "root-trust-lost":
				r.mode.host.CheckRoot = func(context.Context) error { return errors.New("PRIVATE-ROOT-ERROR") }
			case "cancelled":
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			case "closed-runtime":
				r.runtime.mu.Lock()
				r.runtime.closed = true
				r.runtime.mu.Unlock()
			}
			result, err := r.Read(ctx, scope)
			if !errors.Is(err, ErrControlledReadAuditSource) || result.valid || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("untrusted audit yielded a result: %v", err)
			}
			switch name {
			case "scope":
				if headCalls != 1 || keyCalls != 0 {
					t.Fatal("scope mismatch did not stop before key/file verification", headCalls, keyCalls)
				}
			case "run-path":
				if headCalls != 0 || keyCalls != 0 {
					t.Fatal("unsafe run path reached a provider", headCalls, keyCalls)
				}
			case "root-replaced":
				replacement, err := os.Open(f.store.stateDir)
				if err != nil {
					t.Fatal(err)
				}
				defer replacement.Close()
				original := r.mode.host.RootAnchor
				r.mode.host.RootAnchor = replacement
				defer func() { r.mode.host.RootAnchor = original }()
				if control, err := r.Read(ctx, scope); err != nil || !control.valid {
					t.Fatal("identical copied root was invalid with its own trusted anchor", err)
				}
			case "root-mode-during-read":
				if headCalls != 2 || keyCalls != 2 {
					t.Fatal("root mode change must follow successful file verification", headCalls, keyCalls)
				}
				chmod(f.store.stateDir, 0o700)
				if control, err := r.Read(ctx, scope); err != nil || !control.valid {
					t.Fatal("restoring only root mode did not restore valid reading", err)
				}
			}
		})
	}
}

func TestFinalAuditSourceRequiresExplicitProvidersAndLimits(t *testing.T) {
	for _, name := range []string{"head", "key", "bytes", "events", "timeout", "bytes-ceiling", "events-ceiling", "timeout-ceiling", "mode", "lifecycle"} {
		t.Run(name, func(t *testing.T) {
			f := newFinalAuditSourceFixture(t)
			switch name {
			case "head":
				f.options.LatestCheckpoint = nil
			case "key":
				f.options.ResolveKey = nil
			case "bytes":
				f.options.MaxJournalBytes = 0
			case "events":
				f.options.MaxEvents = 0
			case "timeout":
				f.options.Timeout = 0
			case "bytes-ceiling":
				f.options.MaxJournalBytes = maxControlledAuditSourceBytes + 1
			case "events-ceiling":
				f.options.MaxEvents = maxControlledAuditSourceEvents + 1
			case "timeout-ceiling":
				f.options.Timeout = maxControlledAuditSourceTimeout + 1
			case "mode":
				f.s.engine.controlledReads.mode = nil
			case "lifecycle":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				f.s.engine.controlledReads.mode.lifecycle = ctx
			}
			r, err := f.s.NewControlledReadAuditSource(f.options)
			if r != nil || !errors.Is(err, ErrControlledReadAuditSource) {
				t.Fatal("incomplete host configuration accepted")
			}
		})
	}
}

func TestFinalAuditSourceExactBoundsAndConcurrentReads(t *testing.T) {
	f := newFinalAuditSourceFixture(t)
	info, err := os.Stat(f.journal)
	if err != nil {
		t.Fatal(err)
	}
	f.options.MaxJournalBytes = info.Size()
	r := f.reader(t)
	var failures atomic.Int64
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := r.Read(context.Background(), f.scope)
			if err != nil || !result.valid {
				failures.Add(1)
			}
		}()
	}
	group.Wait()
	if failures.Load() != 0 {
		t.Fatalf("exact bounds/concurrent immutable reads failed: %d", failures.Load())
	}
}

func TestFinalAuditSourceReaderRechecksTrustForEveryFinalization(t *testing.T) {
	f := newFinalAuditSourceFixture(t)
	var revoked bool
	trusted := f.options.ResolveKey
	f.options.ResolveKey = func(ctx context.Context, request ControlledReadAuditKeyRequest) ([]byte, error) {
		if revoked {
			return nil, errors.New("synthetic key quarantined")
		}
		return trusted(ctx, request)
	}
	r := f.reader(t)
	expected := evidencebind.Context{Scope: f.scope, IntentID: 22, ResourceRef: "doc:one"}
	metadata := func(context.Context, evidencebind.Context) (e3record.Source, error) {
		return e3record.Source{Expected: expected, KGSnapshotRef: "synthetic-kg"}, nil
	}
	wrapped, err := r.SourceReader(metadata)
	if err != nil {
		t.Fatal(err)
	}
	first, err := wrapped(context.Background(), expected)
	if err != nil || first.FinalCheckpointRef != f.anchor.Ref || first.KGSnapshotRef != "synthetic-kg" || first.Completed || first.Finalized {
		t.Fatal("fresh audit source did not compose metadata")
	}
	revoked = true
	second, err := wrapped(context.Background(), expected)
	if !errors.Is(err, ErrControlledReadAuditSource) || second.FinalCheckpointRef != "" {
		t.Fatal("cached verification survived key trust loss")
	}
	revoked = false
	restored, err := wrapped(context.Background(), expected)
	if err != nil || restored.FinalCheckpointRef != f.anchor.Ref {
		t.Fatal("trust restoration did not restore the valid control", err)
	}
	other := expected
	other.IntentID++
	if _, err := wrapped(context.Background(), other); !errors.Is(err, ErrControlledReadAuditSource) {
		t.Fatal("mismatched full metadata context was accepted")
	}
	if _, err := r.SourceReader(nil); !errors.Is(err, ErrControlledReadAuditSource) {
		t.Fatal("nil metadata got audit authority")
	}
}

func TestFinalAuditSourceReadsOnlyExaminedInode(t *testing.T) {
	for _, name := range []string{"replacement", "new-hardlink"} {
		t.Run(name, func(t *testing.T) {
			f := newFinalAuditSourceFixture(t)
			directory, err := os.Open(f.store.journalDirectory())
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			before, err := taskMarkerStat(directory, filepath.Base(f.journal))
			if err != nil {
				t.Fatal(err)
			}
			uid := before.Sys().(*syscall.Stat_t).Uid
			if name == "replacement" {
				body, err := os.ReadFile(f.journal)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.journal+".replacement", body, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(f.journal+".replacement", before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(f.journal+".replacement", f.journal); err != nil {
					t.Fatal(err)
				}
				after, err := taskMarkerStat(directory, filepath.Base(f.journal))
				if err != nil || !validControlledAuditInfo(after, f.options.MaxJournalBytes, uid) ||
					before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || os.SameFile(before, after) {
					t.Fatal("replacement must differ only in inode identity", err)
				}
			} else {
				if err := os.Link(f.journal, f.journal+".link"); err != nil {
					t.Fatal(err)
				}
			}
			body, err := readControlledAuditFile(context.Background(), directory, filepath.Base(f.journal), before, f.options.MaxJournalBytes, uid)
			if !errors.Is(err, ErrControlledReadAuditSource) || len(body) != 0 {
				t.Fatal("unexamined inode/link state was read")
			}
		})
	}
}

func TestFinalAuditSourceRejectsLifecycleCancellationAfterConstruction(t *testing.T) {
	f := newFinalAuditSourceFixture(t)
	r := f.reader(t)
	if control, err := r.Read(context.Background(), f.scope); err != nil || !control.valid {
		t.Fatal("live lifecycle control failed", err)
	}
	f.cancelLifecycle()
	result, err := r.Read(context.Background(), f.scope)
	if !errors.Is(err, ErrControlledReadAuditSource) || result.valid {
		t.Fatal("reader survived lifecycle cancellation after construction", err)
	}
}
