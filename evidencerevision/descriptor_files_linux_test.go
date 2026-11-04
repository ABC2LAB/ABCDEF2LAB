//go:build linux

package evidencerevision

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/transmit"
	"golang.org/x/sys/unix"
)

// This synthetic host only leases owned temp directories. It authenticates no
// real principal, namespace, restore anchor or production writer.
type descriptorTestHost struct {
	dir                string
	anchor             *os.File
	ctx                context.Context
	deny               bool
	errorAfterCleanup  bool
	hook               func(DescriptorFileAccess)
	releaseHook        func()
	acquired, released atomic.Int64
}
type descriptorTestLease struct{ host *descriptorTestHost }

func (h *descriptorTestHost) AcquireDescriptorRoot(ctx context.Context, a DescriptorFileAccess) (DescriptorRootScope, error) {
	if h.deny {
		return nil, errors.New("synthetic hidden provider error")
	}
	h.acquired.Add(1)
	if h.hook != nil {
		h.hook(a)
	}
	if h.errorAfterCleanup {
		lease := &descriptorTestLease{h}
		lease.Release() // provider owns partial acquisition cleanup
		return lease, errors.New("synthetic cleaned partial acquisition")
	}
	return &descriptorTestLease{h}, nil
}
func (l *descriptorTestLease) Context() context.Context { return l.host.ctx }
func (l *descriptorTestLease) Anchor() *os.File         { return l.host.anchor }
func (l *descriptorTestLease) Directory() string        { return l.host.dir }
func (l *descriptorTestLease) Release() {
	if l.host.releaseHook != nil {
		l.host.releaseHook()
	}
	l.host.released.Add(1)
}

func descriptorFixture(t *testing.T) (*DescriptorFiles, *descriptorTestHost, Descriptor) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &descriptorTestHost{dir: dir, anchor: anchor, ctx: t.Context()}
	s, err := NewDescriptorFiles(h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if h.acquired.Load() != h.released.Load() {
			t.Error("lease release count")
		}
		if err := anchor.Close(); err != nil {
			t.Error("anchor close")
		}
	})
	d := Descriptor{Version: 1, OperationRef: "operation:descriptor-test", TaskID: 1, ExplorationID: 2, IntentID: 3,
		Scope:       transmit.Scope{RunRef: "run:descriptor", TaskRef: "1", CaseRef: "case:descriptor", ResetRef: "reset:descriptor", PolicyRef: "policy:descriptor"},
		ModeVersion: 2, RequestDigest: [32]byte{1, 2, 3}}
	return s, h, d
}

func descriptorPath(h *descriptorTestHost, d Descriptor) string {
	return filepath.Join(h.dir, descriptorName(d.OperationRef))
}

func TestDescriptorFilesPersistRestartAndExactRetry(t *testing.T) {
	s, h, d := descriptorFixture(t)
	var purposes []DescriptorFilePurpose
	h.hook = func(a DescriptorFileAccess) {
		if a.OperationRef != d.OperationRef {
			t.Error("wrong operation lease")
		}
		purposes = append(purposes, a.Purpose)
	}
	if err := s.Put(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(descriptorPath(h, d))
	if err != nil || !descriptorFile(before) {
		t.Fatal("private output metadata")
	}
	if err := s.Put(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(descriptorPath(h, d))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("retry replaced inode")
	}
	restarted, err := NewDescriptorFiles(h)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Load(t.Context(), d.OperationRef)
	if err != nil || got != d {
		t.Fatal("restart read changed descriptor")
	}
	if len(purposes) != 3 || purposes[0] != PersistDescriptorFile || purposes[1] != PersistDescriptorFile || purposes[2] != ReadDescriptorFile {
		t.Fatal("scope purpose")
	}
	body, err := os.ReadFile(descriptorPath(h, d))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 8 || len(body) > DescriptorFileMaxBytes {
		t.Fatal("descriptor envelope")
	}
	for _, forbidden := range []string{"authorization", "cookie", "body", "headers", "canary", "key_bytes"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatal("unexpected raw metadata")
		}
	}
}

func TestDescriptorFilesNoClobberConflict(t *testing.T) {
	s, h, d := descriptorFixture(t)
	if err := s.Put(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(descriptorPath(h, d))
	d.IntentID++
	if err := s.Put(t.Context(), d); !errors.Is(err, ErrConflict) {
		t.Fatal("conflict accepted")
	}
	after, _ := os.ReadFile(descriptorPath(h, d))
	if !bytes.Equal(before, after) {
		t.Fatal("conflict overwrote file")
	}
}

func TestDescriptorFilesConcurrentPublish(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "different"}[conflict], func(t *testing.T) {
			s, h, d := descriptorFixture(t)
			const workers = 8
			start := make(chan struct{})
			errs := make(chan error, workers)
			var wg sync.WaitGroup
			for i := range workers {
				wg.Go(func() {
					<-start
					copy := d
					if conflict {
						copy.IntentID += int64(i)
					}
					errs <- s.Put(t.Context(), copy)
				})
			}
			close(start)
			wg.Wait()
			close(errs)
			var success, conflicts int
			for err := range errs {
				if err == nil {
					success++
				} else if errors.Is(err, ErrConflict) {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if !conflict && success != workers || conflict && (success != 1 || conflicts != workers-1) {
				t.Fatal("atomic publish counts")
			}
			got, err := s.Load(t.Context(), d.OperationRef)
			if err != nil || got.OperationRef != d.OperationRef {
				t.Fatal("winner unavailable")
			}
			info, err := os.Stat(descriptorPath(h, d))
			if err != nil || !descriptorFile(info) {
				t.Fatal("winner metadata")
			}
		})
	}
}

func TestDescriptorFilesRejectUnsafeExisting(t *testing.T) {
	for _, mode := range []string{"symlink", "fifo", "hardlink", "directory", "permissions", "oversize", "empty", "corrupt", "duplicate", "extra", "trailing", "operation"} {
		t.Run(mode, func(t *testing.T) {
			s, h, d := descriptorFixture(t)
			p := descriptorPath(h, d)
			body, _ := descriptorFileBytes(d)
			switch mode {
			case "symlink":
				other := filepath.Join(h.dir, "other")
				if os.WriteFile(other, body, 0o600) != nil || os.Symlink(other, p) != nil {
					t.Fatal("fixture symlink")
				}
			case "fifo":
				if unix.Mkfifo(p, 0o600) != nil {
					t.Fatal("fixture FIFO")
				}
			case "directory":
				if os.Mkdir(p, 0o700) != nil {
					t.Fatal("fixture dir")
				}
			default:
				switch mode {
				case "oversize":
					body = bytes.Repeat([]byte("x"), DescriptorFileMaxBytes+1)
				case "empty":
					body = nil
				case "corrupt":
					body = []byte("{")
				case "duplicate":
					body = append([]byte(`{"version":1,`), body[1:]...)
				case "extra":
					body = append([]byte(`{"raw_body":"hidden",`), body[1:]...)
				case "trailing":
					body = append(body, '\n')
				case "operation":
					other := d
					other.OperationRef = "operation:other"
					body, _ = descriptorFileBytes(other)
				}
				if os.WriteFile(p, body, 0o600) != nil {
					t.Fatal("fixture write")
				}
				if mode == "permissions" && os.Chmod(p, 0o644) != nil {
					t.Fatal("fixture chmod")
				}
				if mode == "hardlink" && os.Link(p, filepath.Join(h.dir, "alias")) != nil {
					t.Fatal("fixture hardlink")
				}
			}
			if got, err := s.Load(t.Context(), d.OperationRef); err == nil || got != (Descriptor{}) {
				t.Fatal("unsafe read accepted")
			}
			if err := s.Put(t.Context(), d); err == nil {
				t.Fatal("unsafe retry accepted")
			}
			if _, err := os.Lstat(p); err != nil {
				t.Fatal("unsafe artifact removed")
			}
		})
	}
}

func TestDescriptorFilesScopeAndInputRejection(t *testing.T) {
	for _, mode := range []string{"nil-store", "nil-provider", "typednil-provider", "provider-denied", "nil-context", "cancelled", "revoked", "root-mode", "root-symlink", "root-replaced", "nil-anchor", "missing-root", "invalid-descriptor", "invalid-op"} {
		t.Run(mode, func(t *testing.T) {
			s, h, d := descriptorFixture(t)
			ctx := t.Context()
			switch mode {
			case "nil-store":
				s = nil
			case "nil-provider":
				if _, err := NewDescriptorFiles(nil); err == nil {
					t.Fatal("nil provider")
				}
				return
			case "typednil-provider":
				var p *descriptorTestHost
				if _, err := NewDescriptorFiles(p); err == nil {
					t.Fatal("typednil provider")
				}
				return
			case "provider-denied":
				h.deny = true
			case "nil-context":
				ctx = nil
			case "cancelled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			case "revoked":
				c, cancel := context.WithCancel(ctx)
				cancel()
				h.ctx = c
			case "root-mode":
				if os.Chmod(h.dir, 0o755) != nil {
					t.Fatal("mode")
				}
			case "root-symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if os.Symlink(h.dir, alias) != nil {
					t.Fatal("alias")
				}
				h.dir = alias
			case "root-replaced":
				original := h.dir + "-old"
				if os.Rename(h.dir, original) != nil || os.Mkdir(h.dir, 0o700) != nil {
					t.Fatal("root swap")
				}
				t.Cleanup(func() {
					if os.RemoveAll(original) != nil {
						t.Error("own temp cleanup")
					}
				})
			case "nil-anchor":
				h.anchor = nil
			case "missing-root":
				h.dir = filepath.Join(h.dir, "missing")
			case "invalid-descriptor":
				d.TaskID = 0
			case "invalid-op":
				d.OperationRef = "../escape"
			}
			if err := s.Put(ctx, d); err == nil {
				t.Fatal("invalid scope/input accepted")
			}
			if mode != "invalid-descriptor" {
				if got, err := s.Load(ctx, d.OperationRef); err == nil || got != (Descriptor{}) {
					t.Fatal("invalid read accepted")
				}
			}
		})
	}
}

func TestDescriptorFilesFailedStagesPreserveArtifacts(t *testing.T) {
	for _, stage := range []string{"write", "file_sync", "publish", "dir_sync", "ack", "file_close", "root_close"} {
		t.Run(stage, func(t *testing.T) {
			s, h, d := descriptorFixture(t)
			var order []string
			s.fault = func(at string) error {
				order = append(order, at)
				if at == stage {
					return errors.New("private path/body must not escape")
				}
				return nil
			}
			if err := s.Put(t.Context(), d); !errors.Is(err, ErrDescriptorFileUnknown) || strings.Contains(err.Error(), "private") {
				t.Fatal("uncertain stage not redacted")
			}
			entries, err := os.ReadDir(h.dir)
			if err != nil || len(entries) != 1 {
				t.Fatal("failed artifact missing")
			}
			if len(order) == 0 || !strings.Contains(strings.Join(order, ","), stage) {
				t.Fatal("wrong IO order")
			}
			s.fault = nil
			if stage == "dir_sync" || stage == "ack" || stage == "file_close" || stage == "root_close" {
				if err := s.Put(t.Context(), d); err != nil {
					t.Fatal("lost ACK exact retry failed")
				}
				if got, err := s.Load(t.Context(), d.OperationRef); err != nil || got != d {
					t.Fatal("published preserved descriptor")
				}
			} else {
				if got, err := s.Load(t.Context(), d.OperationRef); !errors.Is(err, ErrNotFound) || got != (Descriptor{}) {
					t.Fatal("partial temp was published")
				}
				if err := s.Put(t.Context(), d); err != nil {
					t.Fatal("retry while retaining temp")
				}
				after, _ := os.ReadDir(h.dir)
				if len(after) != 2 {
					t.Fatal("partial temp removed")
				}
			}
		})
	}
}

func TestDescriptorFilesRetryRequiresBothSyncs(t *testing.T) {
	for _, stage := range []string{"retry_file_sync", "retry_dir_sync"} {
		t.Run(stage, func(t *testing.T) {
			s, _, d := descriptorFixture(t)
			if err := s.Put(t.Context(), d); err != nil {
				t.Fatal(err)
			}
			s.fault = func(at string) error {
				if at == stage {
					return ErrDescriptorFileStorage
				}
				return nil
			}
			if err := s.Put(t.Context(), d); !errors.Is(err, ErrDescriptorFileUnknown) {
				t.Fatal("retry acknowledged without both syncs")
			}
		})
	}
}

func TestDescriptorFilesCloseBeforeLeaseRelease(t *testing.T) {
	s, h, d := descriptorFixture(t)
	h.releaseHook = func() {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal("fd observations unavailable")
		}
		var owned int
		for _, e := range entries {
			p, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
			if err == nil && (p == h.dir || strings.HasPrefix(p, h.dir+"/")) {
				owned++
			}
		}
		if owned != 1 {
			t.Error("I/O FD remained open at Release")
		}
	}
	if err := s.Put(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(t.Context(), d.OperationRef); err != nil || got != d {
		t.Fatal("lease read")
	}
}

func TestDescriptorFilesProviderErrorOwnsPartialCleanup(t *testing.T) {
	s, h, d := descriptorFixture(t)
	h.errorAfterCleanup = true
	if err := s.Put(t.Context(), d); !errors.Is(err, ErrScope) {
		t.Fatal("provider error accepted")
	}
	if got, err := s.Load(t.Context(), d.OperationRef); !errors.Is(err, ErrScope) || got != (Descriptor{}) {
		t.Fatal("provider error returned data")
	}
	if h.acquired.Load() != 2 || h.released.Load() != 2 {
		t.Fatal("partial scope double release")
	}
	entries, err := os.ReadDir(h.dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("I/O after provider error")
	}
}

func TestDescriptorFilesAcknowledgementOrder(t *testing.T) {
	s, _, d := descriptorFixture(t)
	var stages []string
	s.fault = func(stage string) error { stages = append(stages, stage); return nil }
	if err := s.Put(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if strings.Join(stages, ",") != "write,file_sync,publish,dir_sync,ack,file_close,root_close" {
		t.Fatal("publication sync/close order")
	}
	stages = nil
	if err := s.Put(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if strings.Join(stages, ",") != "retry_file_sync,retry_dir_sync,retry_close,root_close" {
		t.Fatal("retry sync/close order")
	}
}

func TestDescriptorFilesLoadCloseFailureReturnsZero(t *testing.T) {
	for _, stage := range []string{"file_close", "root_close"} {
		t.Run(stage, func(t *testing.T) {
			s, _, d := descriptorFixture(t)
			if err := s.Put(t.Context(), d); err != nil {
				t.Fatal(err)
			}
			s.fault = func(at string) error {
				if at == stage {
					return ErrDescriptorFileStorage
				}
				return nil
			}
			if got, err := s.Load(t.Context(), d.OperationRef); err == nil || got != (Descriptor{}) {
				t.Fatal("close failure returned data")
			}
		})
	}
}

func TestDescriptorFilesRevocationAndReplacementDuringWrite(t *testing.T) {
	for _, mode := range []string{"cancel", "file-replace", "root-replace"} {
		t.Run(mode, func(t *testing.T) {
			s, h, d := descriptorFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h.ctx = ctx
			s.fault = func(stage string) error {
				if stage != "file_sync" {
					return nil
				}
				switch mode {
				case "cancel":
					cancel()
				case "file-replace":
					entries, err := os.ReadDir(h.dir)
					if err != nil || len(entries) != 1 {
						t.Fatal("pending entry")
					}
					p := filepath.Join(h.dir, entries[0].Name())
					if os.Rename(p, p+"-old") != nil || os.WriteFile(p, []byte("{}"), 0o600) != nil {
						t.Fatal("pending swap")
					}
				case "root-replace":
					old := h.dir + "-old"
					if os.Rename(h.dir, old) != nil || os.Mkdir(h.dir, 0o700) != nil {
						t.Fatal("root swap")
					}
					t.Cleanup(func() {
						if os.RemoveAll(old) != nil {
							t.Error("own root cleanup")
						}
					})
				}
				return nil
			}
			if err := s.Put(t.Context(), d); !errors.Is(err, ErrDescriptorFileUnknown) {
				t.Fatal("trust/file swap acknowledged")
			}
		})
	}
}

func TestDescriptorFilesPersistBeforeIssueAndRestartReconcile(t *testing.T) {
	f := newRevisionFixture(t)
	s, h, _ := descriptorFixture(t)
	d := describeFixture(t, f)
	operation := d.OperationRef // operator's exact non-sensitive diagnostic survives
	if err := s.Put(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	f.store.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("synthetic lost SQL ACK")
	}
	if got, err := f.store.Issue(t.Context(), f.request); !errors.Is(err, ErrOutcomeUnknown) || got != (Receipt{}) {
		t.Fatal("lost SQL ACK returned receipt")
	}
	f.request = Request{}
	f.input = evidencebind.Input{}
	f.expected = evidencebind.Context{}
	d = Descriptor{}
	restarted, err := NewDescriptorFiles(h)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restarted.Load(t.Context(), "operation:")
	if err == nil || restored != (Descriptor{}) {
		t.Fatal("guessed op accepted")
	}
	restored, err = restarted.Load(t.Context(), operation)
	if err != nil {
		t.Fatal(err)
	}
	sqlRestart, err := New(f.d.DB, f.scope)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := sqlRestart.Reconcile(t.Context(), restored); err != nil || got.Revision == 0 || got.OperationRef != restored.OperationRef {
		t.Fatal("disk+SQL restart recovery failed")
	}
	f.scope.deny = ReadReceipt
	if got, err := sqlRestart.Reconcile(t.Context(), restored); !errors.Is(err, ErrScope) || got != (Receipt{}) {
		t.Fatal("disk descriptor granted SQL reader authority")
	}
}
