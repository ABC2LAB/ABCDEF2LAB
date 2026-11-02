package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/transmit/auditlog"
)

var privateFailure = errors.New("PRIVATE-SENTINEL")

type brokenWriter struct{ short bool }

func (w brokenWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, privateFailure
}

func TestArgumentAndHelpBoundaries(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		args []string
		code int
	}{
		{"help", []string{"--help"}, 0},
		{"short help", []string{"-h"}, 0},
		{"missing", nil, 2},
		{"missing value", []string{"--key-file"}, 2},
		{"unknown", []string{"--PRIVATE-SENTINEL"}, 2},
		{"positional", []string{"--key-file", "valid.key", "PRIVATE-SENTINEL"}, 2},
		{"empty value", []string{"--key-file="}, 2},
		{"stdout shortcut", []string{"--key-file", "-"}, 2},
		{"trailing slash", []string{"--key-file", "/private/PRIVATE-SENTINEL/"}, 2},
		{"dot", []string{"--key-file", "."}, 2},
		{"dotdot", []string{"--key-file", ".."}, 2},
		{"name too long", []string{"--key-file", strings.Repeat("x", 129)}, 2},
		{"control character", []string{"--key-file", "PRIVATE-SENTINEL\n.key"}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			got := run(context.Background(), tt.args, &out, &diagnostics)
			if got != tt.code {
				t.Fatalf("exit=%d, want=%d", got, tt.code)
			}
			if tt.code == 0 && (out.String() != usage || diagnostics.Len() != 0) {
				t.Fatal("unexpected help output")
			}
			if tt.code == 2 && (out.Len() != 0 || diagnostics.String() != usage) {
				t.Fatal("argument diagnostic was not fixed usage")
			}
		})
	}
}

func TestHelpAndUsageOutputFailures(t *testing.T) {
	t.Parallel()
	for _, short := range []bool{false, true} {
		var diagnostic bytes.Buffer
		if code := run(context.Background(), []string{"--help"}, brokenWriter{short}, &diagnostic); code != 1 || diagnostic.String() != outputUnavailable+"\n" {
			t.Fatal("help output error did not fail with fixed code")
		}
		if code := run(context.Background(), nil, io.Discard, brokenWriter{short}); code != 1 {
			t.Fatal("broken usage sink did not fail")
		}
		if code := failure(brokenWriter{short}, storageUnavailable); code != 1 {
			t.Fatal("broken diagnostic sink did not fail")
		}
	}
}

type retainedReader struct {
	data     []byte
	retained []byte
	err      error
	after    func()
}

func (r *retainedReader) Read(p []byte) (int, error) {
	r.retained = p
	n := copy(p, r.data)
	if r.after != nil {
		r.after()
	}
	return n, r.err
}

type faultHandle struct {
	label    string
	steps    *[]string
	writeErr error
	syncErr  error
	closeErr error
	short    bool
	written  []byte
	after    func(string)
}

func (f *faultHandle) step(operation string) {
	*f.steps = append(*f.steps, f.label+"."+operation)
	if f.after != nil {
		f.after(operation)
	}
}

func (f *faultHandle) Write(p []byte) (int, error) {
	f.step("write")
	f.written = append([]byte(nil), p...)
	if f.short {
		return len(p) - 1, nil
	}
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *faultHandle) Sync() error  { f.step("sync"); return f.syncErr }
func (f *faultHandle) Close() error { f.step("close"); return f.closeErr }

func TestGenerateClearsEntropyAndDoesNotCreateOnEntropyFailure(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		data []byte
		err  error
	}{
		{"no entropy", nil, io.EOF},
		{"partial entropy", bytes.Repeat([]byte{0x42}, auditlog.KeyBytes-1), io.EOF},
		{"private source error", []byte{0x42}, privateFailure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rng := &retainedReader{data: tt.data, err: tt.err}
			opened := false
			code := generateKey(context.Background(), "audit.key", rng, func(string) (keyWriter, syncCloser, string) {
				opened = true
				return nil, nil, storageUnavailable
			})
			if code != entropyUnavailable || opened {
				t.Fatal("entropy failure created output or leaked its error")
			}
			if !bytes.Equal(rng.retained, make([]byte, len(rng.retained))) {
				t.Fatal("partial entropy was not cleared")
			}
		})
	}
}

func TestGenerateSuccessUsesExactKeyAndOrderedDurability(t *testing.T) {
	t.Parallel()
	rng := &retainedReader{data: bytes.Repeat([]byte{0x42}, auditlog.KeyBytes)}
	var steps []string
	file, dir := &faultHandle{label: "file", steps: &steps}, &faultHandle{label: "dir", steps: &steps}
	code := generateKey(context.Background(), "audit.key", rng, func(string) (keyWriter, syncCloser, string) { return file, dir, "" })
	if code != "" || !bytes.Equal(file.written, rng.data) {
		t.Fatal("key bytes were not preserved exactly")
	}
	if !reflect.DeepEqual(steps, []string{"file.write", "file.sync", "file.close", "dir.sync", "dir.close"}) {
		t.Fatalf("durability order: %v", steps)
	}
	if !bytes.Equal(rng.retained, make([]byte, auditlog.KeyBytes)) {
		t.Fatal("key buffer not cleared")
	}
}

func TestGenerateRejectsBeforeOpening(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tt := range []struct {
		name       string
		ctx        context.Context
		path, want string
	}{
		{"nil context", nil, "audit.key", invalidInput},
		{"invalid name", context.Background(), "-", invalidInput},
		{"already canceled", ctx, "audit.key", generationCanceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opened := false
			code := generateKey(tt.ctx, tt.path, &retainedReader{err: privateFailure}, func(string) (keyWriter, syncCloser, string) {
				opened = true
				return nil, nil, storageUnavailable
			})
			if code != tt.want || opened {
				t.Fatal("invalid/canceled generation reached output")
			}
		})
	}
}

func TestGenerateCancelAfterEntropyAndOpenFailureClearKey(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	rng := &retainedReader{data: bytes.Repeat([]byte{0x42}, auditlog.KeyBytes), after: cancel}
	opened := false
	code := generateKey(ctx, "audit.key", rng, func(string) (keyWriter, syncCloser, string) {
		opened = true
		return nil, nil, storageUnavailable
	})
	if code != generationCanceled || opened || !bytes.Equal(rng.retained, make([]byte, auditlog.KeyBytes)) {
		t.Fatal("cancellation after entropy reached file creation or retained bytes")
	}
	rng.after = nil
	code = generateKey(context.Background(), "audit.key", rng, func(string) (keyWriter, syncCloser, string) { return nil, nil, alreadyExists })
	if code != alreadyExists || !bytes.Equal(rng.retained, make([]byte, auditlog.KeyBytes)) {
		t.Fatal("open failure retained key")
	}
}

func TestPersistFailuresNeverAcknowledgeAndCloseBothHandles(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		mutate func(*faultHandle, *faultHandle)
		want   []string
	}{
		{"short write", func(f, d *faultHandle) { f.short = true }, []string{"file.write", "file.close", "dir.close"}},
		{"write failure", func(f, d *faultHandle) { f.writeErr = privateFailure }, []string{"file.write", "file.close", "dir.close"}},
		{"file sync failure", func(f, d *faultHandle) { f.syncErr = privateFailure }, []string{"file.write", "file.sync", "file.close", "dir.close"}},
		{"file close failure", func(f, d *faultHandle) { f.closeErr = privateFailure }, []string{"file.write", "file.sync", "file.close", "dir.close"}},
		{"directory sync failure", func(f, d *faultHandle) { d.syncErr = privateFailure }, []string{"file.write", "file.sync", "file.close", "dir.sync", "dir.close"}},
		{"directory close failure", func(f, d *faultHandle) { d.closeErr = privateFailure }, []string{"file.write", "file.sync", "file.close", "dir.sync", "dir.close"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var steps []string
			file, dir := &faultHandle{label: "file", steps: &steps}, &faultHandle{label: "dir", steps: &steps}
			tt.mutate(file, dir)
			code := persistKey(context.Background(), file, dir, make([]byte, auditlog.KeyBytes))
			if code != storageUnavailable || !reflect.DeepEqual(steps, tt.want) {
				t.Fatalf("failed durability: %s, %v", code, steps)
			}
		})
	}
}

func TestPersistCancellationRetainsSynchronousCompletion(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"before_write", "write", "sync", "close", "dir_sync"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var steps []string
			file, dir := &faultHandle{label: "file", steps: &steps}, &faultHandle{label: "dir", steps: &steps}
			file.after = func(op string) {
				if op == operation {
					cancel()
				}
			}
			dir.after = func(op string) {
				if "dir_"+op == operation {
					cancel()
				}
			}
			want := []string{"file.write", "file.sync", "file.close", "dir.sync", "dir.close"}
			if operation == "before_write" {
				cancel()
				want = []string{"file.close", "dir.close"}
			}
			code := persistKey(ctx, file, dir, make([]byte, auditlog.KeyBytes))
			if code != generationCanceled || !reflect.DeepEqual(steps, want) {
				t.Fatalf("cancellation boundary: %s, %v", code, steps)
			}
		})
	}
}
