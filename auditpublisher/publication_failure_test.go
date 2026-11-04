//go:build linux

package auditpublisher

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/transmit/auditlog"
)

// Intercept real durable ACKs and reads, preserving the actual SQL operation.
type interceptedStore struct {
	checkpointStore
	afterPublish func(auditcheckpoint.Receipt) (auditcheckpoint.Receipt, error)
	afterHead    func(auditcheckpoint.Head) (auditcheckpoint.Head, error)
}

func (s interceptedStore) Publish(ctx context.Context, req auditcheckpoint.Request, source *bytes.Reader) (auditcheckpoint.Receipt, error) {
	r, err := s.checkpointStore.Publish(ctx, req, source)
	if err == nil && s.afterPublish != nil {
		return s.afterPublish(r)
	}
	return r, err
}

func (s interceptedStore) Head(ctx context.Context, query auditcheckpoint.Query) (auditcheckpoint.Head, error) {
	h, err := s.checkpointStore.Head(ctx, query)
	if err == nil && s.afterHead != nil {
		return s.afterHead(h)
	}
	return h, err
}

func assertRejectedRecord(t *testing.T, f *publisherFixture, p *Publisher) {
	t.Helper()
	previous := p.head
	if err := p.Record(context.Background(), intent(f.host.scope, 1)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unverified publication returned %v, want ErrUnavailable", err)
	}
	if p.head != previous || !p.failed {
		t.Fatal("rejected publication advanced cached ACK or left recorder usable")
	}
	if _, err := p.AcknowledgedHead(); !errors.Is(err, ErrFailed) {
		t.Fatal("unverified publication exposed usable ACK metadata")
	}
	if err := p.Record(context.Background(), intent(f.host.scope, 2)); !errors.Is(err, ErrFailed) {
		t.Fatalf("publication failure was not sticky: %v", err)
	}
}

func headChanges() []struct {
	name   string
	change func(*auditcheckpoint.Head)
} {
	return []struct {
		name   string
		change func(*auditcheckpoint.Head)
	}{
		{"epoch", func(h *auditcheckpoint.Head) { h.Epoch.Generation++ }},
		{"publisher", func(h *auditcheckpoint.Head) { h.PublisherRef += ":other" }},
		{"revision", func(h *auditcheckpoint.Head) { h.Revision++ }},
		{"phase", func(h *auditcheckpoint.Head) { h.Phase = auditcheckpoint.Quarantined }},
		{"checkpoint", func(h *auditcheckpoint.Head) { h.Checkpoint.Sequence++ }},
	}
}

func TestPublisherRejectsMismatchedReceipt(t *testing.T) {
	for _, tc := range headChanges() {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture(t)
			p := f.open(t)
			// Even a matching readback cannot make an ACK for a different request valid.
			p.store = interceptedStore{checkpointStore: p.store,
				afterPublish: func(r auditcheckpoint.Receipt) (auditcheckpoint.Receipt, error) {
					tc.change(&r.Head)
					return r, nil
				},
				afterHead: func(h auditcheckpoint.Head) (auditcheckpoint.Head, error) {
					tc.change(&h)
					return h, nil
				}}
			assertRejectedRecord(t, f, p)
			f.counts(t, 2) // A real Append committed; it did not justify returning success.
		})
	}
	t.Run("operation", func(t *testing.T) {
		f := fixture(t)
		p := f.open(t)
		p.store = interceptedStore{checkpointStore: p.store,
			afterPublish: func(r auditcheckpoint.Receipt) (auditcheckpoint.Receipt, error) {
				r.OperationRef += ":other"
				return r, nil
			}}
		assertRejectedRecord(t, f, p)
	})
}

func TestPublisherRejectsMismatchedCurrentHead(t *testing.T) {
	for _, tc := range headChanges() {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture(t)
			p := f.open(t)
			p.store = interceptedStore{checkpointStore: p.store,
				afterHead: func(h auditcheckpoint.Head) (auditcheckpoint.Head, error) {
					tc.change(&h)
					return h, nil
				}}
			assertRejectedRecord(t, f, p)
		})
	}
}

func TestPublisherRejectsQuarantineAfterAppendACK(t *testing.T) {
	f := fixture(t)
	p := f.open(t)
	p.store = interceptedStore{checkpointStore: p.store,
		afterPublish: func(r auditcheckpoint.Receipt) (auditcheckpoint.Receipt, error) {
			_, err := f.store.Publish(context.Background(), auditcheckpoint.Request{
				Epoch: r.Head.Epoch, PublisherRef: r.Head.PublisherRef,
				OperationRef: "synthetic:quarantine-after-ack", Kind: auditcheckpoint.Quarantine,
				ExpectedRevision: r.Head.Revision, Previous: r.Head.Checkpoint, Candidate: r.Head.Checkpoint,
			}, nil)
			return r, err
		}}
	assertRejectedRecord(t, f, p)
	if h := f.head(t, p); h.Phase != auditcheckpoint.Quarantined || h.Revision != 3 || h.Checkpoint.Sequence != 1 {
		t.Fatal("test did not quarantine the actual committed Append before current Head")
	}
	f.counts(t, 3)
}

func TestPublisherRejectsReplacedFileAfterHeadRead(t *testing.T) {
	f := fixture(t)
	p := f.open(t)
	p.store = interceptedStore{checkpointStore: p.store,
		afterHead: func(h auditcheckpoint.Head) (auditcheckpoint.Head, error) {
			if err := os.Rename(f.path, f.path+".original"); err != nil {
				return h, err
			}
			return h, os.WriteFile(f.path, []byte("synthetic replacement"), 0o600)
		}}
	assertRejectedRecord(t, f, p)
	if h := f.head(t, p); h.Revision != 2 {
		t.Fatal("test did not reach a durable Append before file replacement")
	}
	if _, err := os.Stat(f.path + ".original"); err != nil {
		t.Fatal("original journal was not preserved")
	}
}

func TestPublisherSealRejectsJournalCloseFailure(t *testing.T) {
	f := fixture(t)
	p := f.open(t)
	original := p.journal
	defer original.Close()
	// A zero journal returns an actual Close error; no public fault hook is needed.
	p.journal = &auditlog.Journal{}
	f.host.allowSeal.Store(true)
	if h, err := p.Seal(context.Background()); h != (auditcheckpoint.Head{}) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed Journal.Close sealed successfully: %v", err)
	}
	if h := f.head(t, p); h.Revision != 1 || h.Phase != auditcheckpoint.Open {
		t.Fatal("failed Journal.Close published a sealed head")
	}
	if !p.failed || f.root.released.Load() != 1 || f.host.witness.Load().Check(sealAccess(f, p)) {
		t.Fatal("failed Journal.Close left resources or valid Close evidence")
	}
	f.counts(t, 1)
}

type changedDuringRead struct {
	*os.File
	afterRead func() error
	seen      []byte
}

func (f *changedDuringRead) ReadAt(b []byte, off int64) (int, error) {
	n, err := f.File.ReadAt(b, off)
	f.seen = b[:n]
	if err == nil {
		err = f.afterRead()
	}
	return n, err
}

func TestSnapshotRejectsChangeDuringReadAndClearsOwnedBytes(t *testing.T) {
	for _, name := range []string{"append", "same-size"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "synthetic.jsonl")
			if err := os.WriteFile(path, []byte("synthetic snapshot"), 0o600); err != nil {
				t.Fatal(err)
			}
			anchor, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer anchor.Close()
			root, err := openJournalRoot(dir, anchor)
			if err != nil {
				t.Fatal(err)
			}
			defer root.close()
			s, err := root.attach("synthetic.jsonl", 1024)
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			file := s.file.(*os.File)
			before, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			reader := &changedDuringRead{File: file, afterRead: func() error {
				data := bytes.Repeat([]byte("x"), int(before.Size()))
				if name == "append" {
					data = []byte("synthetic snapshot extended")
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					return err
				}
				stamp := before.ModTime().Add(time.Second)
				return os.Chtimes(path, stamp, stamp)
			}}
			s.file = reader
			owned, err := s.read(context.Background())
			if owned != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatal("snapshot changed during read was accepted")
			}
			if len(reader.seen) == 0 || !bytes.Equal(reader.seen, make([]byte, len(reader.seen))) {
				t.Fatal("rejected owned snapshot bytes were not cleared")
			}
		})
	}
}

func TestFailureDiagnosticsContainOnlyStages(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)
	if err := unavailable("receipt"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("stage diagnostic changed public error")
	}
	logCloseFailure("journal-close", errors.New("synthetic-private-value"))
	logCloseFailure("root-close", nil)
	got := output.String()
	if strings.Contains(got, "synthetic-private-value") || strings.Contains(got, "root-close") ||
		!strings.Contains(got, "stage=receipt") || !strings.Contains(got, "stage=journal-close") {
		t.Fatal("diagnostic exposed an error value or omitted stage")
	}
}
