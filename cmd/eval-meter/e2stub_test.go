package main

import (
	"bytes"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

// repeatingReader returns the same bytes on every read, forcing an ID collision.
type repeatingReader struct{}

func (repeatingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0x42
	}
	return len(p), nil
}

func newTestStub(t *testing.T, target *url.URL, random io.Reader) (*e2Stub, *httptest.Server, string) {
	t.Helper()
	path, dir := privateTestDir(t)
	stub, code := newE2Stub(dir, target, random, time.Now)
	if code != "" {
		t.Fatalf("e2 stub: %s", code)
	}
	front := httptest.NewServer(stub)
	t.Cleanup(func() {
		front.Close()
		_ = stub.close()
	})
	return stub, front, path
}

func stubRows(t *testing.T, stub *e2Stub, path string) []e2StubRow {
	t.Helper()
	stub.mu.Lock()
	_ = stub.log.file.Sync()
	stub.mu.Unlock()
	return readRows[e2StubRow](t, filepath.Join(path, e2StubLogName))
}

func TestE2StubForwardsAndIssuesAttemptID(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	stub, front, path := newTestStub(t, targetURL(t, upstream), randReader())
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/api/items?q=1", strings.NewReader("body-"+sentinel))
	req.Host = "eval-meter:8080"
	req.Header.Set("X-Other", "kept")
	if status, body := send(t, req); status != http.StatusOK || body != "upstream" {
		t.Fatalf("status=%d body=%q", status, body)
	}
	got := seen()
	if len(got) != 1 || got[0].host != "eval-meter:8080" || got[0].method != http.MethodPost ||
		got[0].uri != "/api/items?q=1" || got[0].body != "body-"+sentinel || got[0].header.Get("X-Other") != "kept" {
		t.Fatalf("target saw %+v", got)
	}
	rows := stubRows(t, stub, path)
	id := got[0].header.Get(attemptHeader)
	if len(rows) != 1 || !attemptIDPattern.MatchString(id) || rows[0].AttemptID != id || rows[0].Decision != e2StubAllowed ||
		!rows[0].Fixture || rows[0].CallerAttempt || rows[0].Status != http.StatusOK {
		t.Fatalf("rows %+v id %q", rows, id)
	}
	log, _ := os.ReadFile(filepath.Join(path, e2StubLogName))
	if bytes.Contains(log, []byte(sentinel)) || bytes.Contains(log, []byte("/api/items")) || bytes.Contains(log, []byte("kept")) {
		t.Fatal("stub log carries request content")
	}
}

func TestE2StubReplacesCallerAttemptHeader(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	stub, front, path := newTestStub(t, targetURL(t, upstream), randReader())
	forged := strings.Repeat("a", 32)
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/x", nil)
	req.Header.Add(attemptHeader, forged)
	req.Header.Add(attemptHeader, sentinel)
	if status, _ := send(t, req); status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	values := seen()[0].header.Values(attemptHeader)
	rows := stubRows(t, stub, path)
	if len(values) != 1 || values[0] == forged || !attemptIDPattern.MatchString(values[0]) || !rows[0].CallerAttempt ||
		rows[0].AttemptID != values[0] {
		t.Fatalf("target saw %v rows %+v", values, rows)
	}
}

func TestE2StubIssuesUniqueIDs(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	_, front, _ := newTestStub(t, targetURL(t, upstream), randReader())
	for i := 0; i < 200; i++ {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/x", nil)
		if status, _ := send(t, req); status != http.StatusOK {
			t.Fatalf("status=%d", status)
		}
	}
	ids := map[string]bool{}
	for _, r := range seen() {
		id := r.header.Get(attemptHeader)
		if !attemptIDPattern.MatchString(id) || ids[id] {
			t.Fatalf("bad or repeated id %q", id)
		}
		ids[id] = true
	}
	if len(ids) != 200 {
		t.Fatalf("ids=%d", len(ids))
	}
}

func TestE2StubFailsClosedWithoutAnAttemptID(t *testing.T) {
	for name, random := range map[string]io.Reader{"entropy": failingReader{}, "collision": repeatingReader{}} {
		t.Run(name, func(t *testing.T) {
			upstream, seen := recordingUpstream(t)
			stub, front, path := newTestStub(t, targetURL(t, upstream), random)
			var statuses []int
			for i := 0; i < 2; i++ {
				req, _ := http.NewRequest(http.MethodGet, front.URL+"/x", nil)
				status, _ := send(t, req)
				statuses = append(statuses, status)
			}
			rows := stubRows(t, stub, path)
			want := 0 // no entropy: nothing forwarded
			if name == "collision" {
				want = 1 // the first ID is fresh, the repeat must not pass
			}
			if len(seen()) != want || statuses[1] != http.StatusServiceUnavailable || rows[1].Decision != e2StubUnavailable ||
				rows[1].AttemptID != "" {
				t.Fatalf("seen=%d statuses=%v rows=%+v", len(seen()), statuses, rows)
			}
		})
	}
}

// The stub's ID reaches the meter, which records it and removes it before the target.
func TestE2StubThroughMeterReachesTargetUnmarked(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	meter, meterFront, meterPath := newTestEngine(t, upstream)
	stub, front, stubPath := newTestStub(t, targetURL(t, meterFront), randReader())
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/item", nil)
	if status, _ := send(t, req); status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	meter.seal()
	stubRow := stubRows(t, stub, stubPath)[0]
	meterRow := onlyRow(t, meterPath)
	if len(seen()) != 1 || len(seen()[0].header.Values(attemptHeader)) != 0 ||
		meterRow.AttemptIDState != attemptValid || meterRow.AttemptID != stubRow.AttemptID {
		t.Fatalf("target headers %v meter %+v stub %+v", seen()[0].header, meterRow, stubRow)
	}
}

func randReader() io.Reader { return cryptorand.Reader }

func TestE2StubStopsForwardingAfterLogFailure(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	stub, front, _ := newTestStub(t, targetURL(t, upstream), randReader())
	_ = stub.log.file.Close() // every later row write fails
	var statuses []int
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/x", nil)
		status, _ := send(t, req)
		statuses = append(statuses, status)
	}
	// The first request was already admitted when its row failed; nothing after an unrecorded admission passes.
	if len(seen()) != 1 || statuses[1] != http.StatusServiceUnavailable {
		t.Fatalf("seen=%d statuses=%v", len(seen()), statuses)
	}
	if err := stub.close(); err == nil {
		t.Fatal("close hid the incomplete log")
	}
}
