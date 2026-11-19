//go:build linux

package e2checkpoint

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func recorderFixture(t *testing.T) (*Recorder, *Store, *testHost) {
	t.Helper()
	base, host, _, path := testFixture(t)
	dynamic := &testHost{epoch: host.epoch, key: bytes.Clone(host.key), allowNew: true}
	store, err := New(base.db, dynamic, 1<<20, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenRecorder(t.Context(), RecorderConfig{
		Store: store, Epoch: dynamic.epoch, PublisherRef: "publisher-test", Directory: filepath.Dir(path),
		Filename: "recorder.journal", Scope: host.binding.Scope, KeyRef: "key-test", Key: dynamic.key,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !r.closed {
			_ = r.Abort()
		}
		if dynamic.acquired.Load() != dynamic.released.Load() {
			t.Error("recorder authority lease leaked")
		}
	})
	return r, store, dynamic
}
func TestRecorderSyncPublishAndSeal(t *testing.T) {
	r, store, host := recorderFixture(t)
	if r.head.Revision != 1 || r.head.Phase != Open {
		t.Fatal("header ACK missing")
	}
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 1)); err != nil {
		t.Fatal(err)
	}
	if r.head.Revision != 2 || r.head.Checkpoint.Sequence != 1 {
		t.Fatal("event ACK missing")
	}
	summary, err := r.VerifyCurrent(t.Context())
	if err != nil || summary.Events != 1 {
		t.Fatal("current verification", err)
	}
	if err := r.Seal(t.Context()); err != nil {
		t.Fatal(err)
	}
	head, err := store.Head(t.Context(), Query{Epoch: host.epoch, Binding: host.binding})
	if err != nil || head.Phase != Sealed || head.Revision != 3 {
		t.Fatal(head, err)
	}
	if _, err := r.VerifyCurrent(t.Context()); err != nil {
		t.Fatal("sealed head verification", err)
	}
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 2)); !errors.Is(err, ErrRecorderClosed) {
		t.Fatal("sealed recorder accepted event", err)
	}
}
func TestRecorderUnknownACKStopsLaterRecords(t *testing.T) {
	r, store, host := recorderFixture(t)
	host.denyHead.Store(true)
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 1)); !errors.Is(err, ErrRecorderFailed) || !errors.Is(err, ErrACKUnknown) {
		t.Fatal("unknown ACK accepted or cause missing", err)
	}
	host.denyHead.Store(false)
	head, err := store.Head(t.Context(), Query{Epoch: host.epoch, Binding: host.binding})
	if err != nil || head.Revision != 2 || head.Checkpoint.Sequence != 1 {
		t.Fatal(head, err)
	}
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 2)); !errors.Is(err, ErrRecorderFailed) {
		t.Fatal("failed recorder accepted second event", err)
	}
	again, err := store.Head(t.Context(), Query{Epoch: host.epoch, Binding: host.binding})
	if err != nil || again != head {
		t.Fatal("head changed after failure", err)
	}
	if err := r.Seal(t.Context()); !errors.Is(err, ErrRecorderFailed) {
		t.Fatal("failed recorder sealed", err)
	}
}

func TestRecorderMissingSnapshotStopsLaterRecords(t *testing.T) {
	r, store, host := recorderFixture(t)
	path := filepath.Join(r.directory, r.filename)
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 1)); !errors.Is(err, ErrRecorderFailed) {
		t.Fatal("missing snapshot accepted", err)
	}
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 2)); !errors.Is(err, ErrRecorderFailed) {
		t.Fatal("missing snapshot allowed another record", err)
	}
	head, err := store.Head(t.Context(), Query{Epoch: host.epoch, Binding: host.binding})
	if err != nil || head.Revision != 1 {
		t.Fatal("unpublished event became current head", err)
	}
}
func TestRecorderRevokedPublicationStopsLaterRecords(t *testing.T) {
	r, store, host := recorderFixture(t)
	host.denyPublish.Store(true)
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 1)); !errors.Is(err, ErrRecorderFailed) {
		t.Fatal("revoked publication accepted", err)
	}
	host.denyPublish.Store(false)
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 2)); !errors.Is(err, ErrRecorderFailed) {
		t.Fatal("revoked recorder allowed another record", err)
	}
	head, err := store.Head(t.Context(), Query{Epoch: host.epoch, Binding: host.binding})
	if err != nil || head.Revision != 1 {
		t.Fatal("revoked event became current head", err)
	}
}

func TestRecorderDirectoryReplacementStopsLaterRecords(t *testing.T) {
	r, store, host := recorderFixture(t)
	original, moved := r.directory, r.directory+".moved"
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(moved, r.filename))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, r.filename), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(original)
		if err := os.Rename(moved, original); err != nil {
			t.Error("fixture directory restore failed")
		}
	})
	if _, err := r.snapshot(t.Context()); !errors.Is(err, ErrIntegrity) {
		t.Fatal("같은 바이트의 다른 디렉터리에서 스냅샷을 읽음", err)
	}
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 1)); !errors.Is(err, ErrRecorderFailed) {
		t.Fatal("replaced directory accepted", err)
	}
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 2)); !errors.Is(err, ErrRecorderFailed) {
		t.Fatal("replaced directory allowed another record", err)
	}
	head, err := store.Head(t.Context(), Query{Epoch: host.epoch, Binding: host.binding})
	if err != nil || head.Revision != 1 {
		t.Fatal("replaced directory changed current head", err)
	}
}

func TestRecorderFileReplacementWithSameBytesRejected(t *testing.T) {
	r, store, host := recorderFixture(t)
	path, moved := filepath.Join(r.directory, r.filename), filepath.Join(r.directory, r.filename+".moved")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(path)
		if err := os.Rename(moved, path); err != nil {
			t.Error("fixture file restore failed")
		}
	})
	if _, err := r.snapshot(t.Context()); !errors.Is(err, ErrIntegrity) {
		t.Fatal("같은 바이트의 다른 파일에서 스냅샷을 읽음", err)
	}
	if err := r.Record(t.Context(), testEvent(host.binding.Scope, 1)); !errors.Is(err, ErrRecorderFailed) {
		t.Fatal("replaced file accepted", err)
	}
	head, err := store.Head(t.Context(), Query{Epoch: host.epoch, Binding: host.binding})
	if err != nil || head.Revision != 1 {
		t.Fatal("replaced file changed current head", err)
	}
}
