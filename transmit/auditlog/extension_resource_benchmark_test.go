//go:build auditresource && linux

package auditlog

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Autumn-27/artex/transmit"
)

// Duplicated fixture assumptions are checked against their owners by the small
// tagged drift test before any manual resource measurements.
const (
	resourceMaxRefLength             = 128
	resourceControlledRequestCeiling = 10000
)

// This manual benchmark never runs in the default test suite. Run each case in
// a fresh resource-limited process with -benchtime=1x; fixture construction is
// excluded from ns/op and B/op but included in process max RSS.
func BenchmarkExtensionResource(b *testing.B) {
	mode := os.Getenv("ARTEX_AUDIT_RESOURCE_CASE")
	var events int
	var headerPrefix, ingestOnly bool
	switch mode {
	case "events5k":
		events = 5000
	case "events20k":
		events = 20000
	case "events40k-header":
		events, headerPrefix = 40000, true
	case "events40k-near":
		events = 40000
	case "ingest64mib-plus-one":
		ingestOnly = true
	default:
		b.Fatal("set ARTEX_AUDIT_RESOURCE_CASE to an explicit bounded case")
	}
	if b.N != 1 {
		b.Fatal("manual resource benchmark requires -benchtime=1x")
	}
	limits := ExtensionLimits{MaxBytes: MaxExtensionBytes, MaxEvents: MaxExtensionEvents, Timeout: MaxExtensionTimeout}
	var source io.Reader
	var previous, candidate Checkpoint
	var key []byte
	var size int64
	if ingestOnly {
		// This is deliberately not a valid journal: it measures bounded input
		// acquisition/rejection, not successful MAC/schema verification.
		previous = Checkpoint{Version: Version, JournalID: strings.Repeat("0", 32), Scope: resourceScope("r"),
			KeyRef: "synthetic-key", MAC: strings.Repeat("0", 64)}
		candidate = previous
		key = make([]byte, KeyBytes)
		if _, err := rand.Read(key); err != nil {
			b.Fatal("synthetic key generation failed")
		}
		size = MaxExtensionBytes + 1
	} else {
		var path string
		var err error
		path, key, previous, candidate, size, err = resourceFixture(b.TempDir(), events, headerPrefix)
		if err != nil {
			b.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			b.Fatal(err)
		}
		defer file.Close()
		if summary, err := Verify(context.Background(), io.NewSectionReader(file, 0, size), key, candidate); err != nil || summary.Checkpoint != candidate {
			b.Fatal("synthetic candidate did not independently verify", err)
		}
		source = file
	}
	defer clear(key)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var usageBefore syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usageBefore); err != nil {
		b.Fatal("pre-verification self resource usage unavailable")
	}
	b.ReportAllocs()
	b.SetBytes(size)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ingestOnly {
			source = io.LimitReader(resourceZeroReader{}, size)
		} else if _, err := source.(io.Seeker).Seek(0, io.SeekStart); err != nil {
			b.Fatal("synthetic fixture seek failed")
		}
		result, err := VerifyExtension(context.Background(), source, key, previous, candidate, limits)
		if ingestOnly {
			if !errors.Is(err, ErrExtensionLimit) || result != (ExtensionSummary{}) {
				b.Fatal("over-limit input was not refused")
			}
		} else if err != nil || result.Previous != previous || result.Current.Checkpoint != candidate {
			b.Fatal("synthetic extension did not verify", err)
		}
	}
	b.StopTimer()
	runtime.ReadMemStats(&after)
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		b.Fatal("self resource usage unavailable")
	}
	b.ReportMetric(float64(events), "events")
	b.ReportMetric(float64(size), "input_bytes")
	b.ReportMetric(float64(before.HeapAlloc)/(1<<20), "heap_before_MiB")
	b.ReportMetric(float64(after.HeapAlloc)/(1<<20), "heap_after_MiB")
	b.ReportMetric(float64(after.HeapSys)/(1<<20), "heap_sys_after_MiB")
	b.ReportMetric(float64(usageBefore.Maxrss)/1024, "self_maxrss_before_MiB")
	b.ReportMetric(float64(usage.Maxrss)/1024, "self_maxrss_MiB") // Linux ru_maxrss is KiB.
	if ingestOnly {
		b.ReportMetric(1, "limit_refused")
	} else {
		b.ReportMetric(1, "validated")
	}
}

type resourceZeroReader struct{}

func (resourceZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func resourceRef(length int) string { return "r" + strings.Repeat("a", length-1) }

func resourceScope(ref string) transmit.Scope {
	return transmit.Scope{RunRef: ref, TaskRef: ref, CaseRef: ref, ResetRef: ref, PolicyRef: ref}
}

// The first 20,000 events model 10,000 admitted intent/result pairs (the
// current controlled-read request ceiling). Another 20,000 result-only budget
// refusals model calls that can record events without spending that budget.
func resourceEvent(sequence, count int, scope transmit.Scope, ref string) transmit.Event {
	at := time.Date(2026, 11, 3, 0, 0, 0, 123456789, time.UTC)
	attempt := uint64((sequence + 1) / 2)
	event := transmit.Event{Stage: "intent", Receipt: transmit.Receipt{
		Scope: scope, Attempt: attempt, AccountRef: ref, SessionRef: ref,
		ResourceRef: ref, ApprovalRef: ref, Method: "GET", At: at,
	}}
	if sequence <= count/2 {
		if sequence%2 == 0 {
			event.Stage, event.Reason = "result", transmit.Completed
			event.Admitted, event.AdmittedAt = true, at
			event.RequestWritten, event.ResponseReceived, event.StatusCode = true, true, 200
		}
	} else {
		event.Stage, event.Reason = "result", transmit.BudgetExhausted
		event.Attempt = uint64(resourceControlledRequestCeiling + sequence - count/2)
	}
	return event
}

func resourceFixture(directory string, count int, headerPrefix bool) (string, []byte, Checkpoint, Checkpoint, int64, error) {
	baseline, err := resourceEstimatedBytes(count)
	if err != nil {
		return "", nil, Checkpoint{}, Checkpoint{}, 0, err
	}
	// Each extra ASCII byte appears in five Scope refs and four event refs,
	// and in five header Scope refs. This finds the largest valid ref length
	// under 64 MiB without repeatedly building large journal files.
	refLength := 1 + int((MaxExtensionBytes-baseline)/int64(9*count+5))
	if refLength > resourceMaxRefLength {
		refLength = resourceMaxRefLength
	}
	if refLength < 1 {
		return "", nil, Checkpoint{}, Checkpoint{}, 0, ErrExtensionLimit
	}
	ref := resourceRef(refLength)
	scope := resourceScope(ref)
	key := make([]byte, KeyBytes)
	if _, err := rand.Read(key); err != nil {
		return "", nil, Checkpoint{}, Checkpoint{}, 0, err
	}
	var id [journalIDBytes]byte
	if _, err := rand.Read(id[:]); err != nil {
		clear(key)
		return "", nil, Checkpoint{}, Checkpoint{}, 0, err
	}
	header := header{Version: Version, JournalID: hex.EncodeToString(id[:]), Scope: scope,
		KeyRef: "synthetic-key", CreatedAt: time.Date(2026, 11, 3, 0, 0, 0, 123456789, time.UTC)}
	mac, err := sign(key, "header", header)
	if err != nil {
		clear(key)
		return "", nil, Checkpoint{}, Checkpoint{}, 0, err
	}
	path := filepath.Join(directory, "synthetic.jsonl")
	file, err := os.Create(path)
	if err != nil {
		clear(key)
		return "", nil, Checkpoint{}, Checkpoint{}, 0, err
	}
	defer file.Close()
	writer := bufio.NewWriterSize(file, 256<<10)
	write := func(value any) error {
		line, err := encodeLine(value)
		if err != nil {
			return err
		}
		_, err = writer.Write(line)
		return err
	}
	if err := write(headerLine{Header: header, MAC: mac}); err != nil {
		clear(key)
		return "", nil, Checkpoint{}, Checkpoint{}, 0, err
	}
	current := Checkpoint{Version: Version, JournalID: header.JournalID, Scope: scope, KeyRef: header.KeyRef, MAC: mac}
	previous := current
	for sequence := 1; sequence <= count; sequence++ {
		if sequence == count && !headerPrefix {
			previous = current // Prefix through the penultimate event.
		}
		payload := eventPayload{Sequence: uint64(sequence), Previous: current.MAC,
			Event: resourceEvent(sequence, count, scope, ref)}
		mac, err := sign(key, "event", payload)
		if err != nil {
			clear(key)
			return "", nil, Checkpoint{}, Checkpoint{}, 0, err
		}
		if err := write(eventLine{Record: payload, MAC: mac}); err != nil {
			clear(key)
			return "", nil, Checkpoint{}, Checkpoint{}, 0, err
		}
		current.Sequence, current.MAC = uint64(sequence), mac
	}
	if err := writer.Flush(); err != nil {
		clear(key)
		return "", nil, Checkpoint{}, Checkpoint{}, 0, err
	}
	info, err := file.Stat()
	if err != nil || info.Size() > MaxExtensionBytes || info.Size() != baseline+int64(refLength-1)*int64(9*count+5) {
		clear(key)
		return "", nil, Checkpoint{}, Checkpoint{}, 0, ErrExtensionLimit
	}
	return path, key, previous, current, info.Size(), nil
}

func resourceEstimatedBytes(count int) (int64, error) {
	ref := resourceRef(1)
	scope := resourceScope(ref)
	const fakeMAC = "0000000000000000000000000000000000000000000000000000000000000000"
	header := header{Version: Version, JournalID: strings.Repeat("0", 32), Scope: scope,
		KeyRef: "synthetic-key", CreatedAt: time.Date(2026, 11, 3, 0, 0, 0, 123456789, time.UTC)}
	line, err := encodeLine(headerLine{Header: header, MAC: fakeMAC})
	if err != nil {
		return 0, err
	}
	total := int64(len(line))
	for sequence := 1; sequence <= count; sequence++ {
		payload := eventPayload{Sequence: uint64(sequence), Previous: fakeMAC,
			Event: resourceEvent(sequence, count, scope, ref)}
		line, err := encodeLine(eventLine{Record: payload, MAC: fakeMAC})
		if err != nil {
			return 0, err
		}
		total += int64(len(line))
	}
	return total, nil
}

// This only checks fixture assumptions in the source checkout. It does not
// construct a large journal, run the benchmark, or assert performance limits.
func TestResourceFixtureContractNoDrift(t *testing.T) {
	if !validRef(resourceRef(resourceMaxRefLength)) || validRef(resourceRef(resourceMaxRefLength+1)) {
		t.Fatal("audit reference bound changed; review fixture sizing before remeasurement")
	}
	// The server constant is private, and importing server would cycle back to
	// auditlog. Parse its declaration instead of adding a production API.
	source, err := parser.ParseFile(token.NewFileSet(), "../../server/controlled_read_store.go", nil, 0)
	if err != nil {
		t.Fatal("server request ceiling source unavailable", err)
	}
	var ceiling ast.Expr
	ast.Inspect(source, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if name.Name == "maxControlledRequests" && i < len(spec.Values) {
				ceiling = spec.Values[i]
			}
		}
		return false
	})
	literal, ok := ceiling.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		t.Fatal("server request ceiling declaration changed; review the fixture")
	}
	actual, err := strconv.ParseInt(literal.Value, 0, 64)
	if err != nil || actual != resourceControlledRequestCeiling {
		t.Fatal("server request ceiling changed; review synthetic event/attempt assumptions", actual, err)
	}
}
