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
		!rows[0].Fixture || rows[0].CallerAttempt || rows[0].Method != http.MethodPost || rows[0].DecidedAt == "" {
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
	stub, front, path := newTestStub(t, targetURL(t, upstream), randReader())
	// A read-only descriptor makes every row write fail while Sync and Close
	// still succeed, so the close error below can only come from the latch.
	readOnly, err := os.Open(filepath.Join(path, e2StubLogName))
	if err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	writable := stub.log.file
	stub.log.file = readOnly
	stub.mu.Unlock()
	_ = writable.Close()
	var statuses []int
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/x", nil)
		status, _ := send(t, req)
		statuses = append(statuses, status)
	}
	// The decision row is written before forwarding, so not even the first request passes.
	if len(seen()) != 0 || statuses[0] != http.StatusServiceUnavailable || statuses[1] != http.StatusServiceUnavailable {
		t.Fatalf("seen=%d statuses=%v", len(seen()), statuses)
	}
	if err := stub.close(); err == nil {
		t.Fatal("close hid the incomplete log")
	}
}

// The target only ever sees a request whose allowed row is already in the log.
func TestE2StubRecordsDecisionBeforeForwarding(t *testing.T) {
	var logPath string
	var atTarget []e2StubRow
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atTarget = readRows[e2StubRow](t, logPath)
		if len(atTarget) != 1 || atTarget[0].AttemptID != r.Header.Get(attemptHeader) {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		_, _ = io.WriteString(w, "upstream")
	}))
	t.Cleanup(upstream.Close)
	_, front, path := newTestStub(t, targetURL(t, upstream), randReader())
	logPath = filepath.Join(path, e2StubLogName)
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/x", nil)
	if status, _ := send(t, req); status != http.StatusOK {
		t.Fatalf("status=%d rows at target %+v", status, atTarget)
	}
	if atTarget[0].Decision != e2StubAllowed {
		t.Fatalf("rows at target %+v", atTarget)
	}
}

// Upgrade and CONNECT are refused before any send: the meter and the target see
// nothing, the engine gets 501, and the stub row keeps its issued attempt ID.
func TestE2StubRefusesUpgradeAndConnectBeforeUpstream(t *testing.T) {
	upstream, seen := recordingUpstream(t)
	meter, meterFront, meterPath := newTestEngine(t, upstream)
	stub, front, stubPath := newTestStub(t, targetURL(t, meterFront), randReader())
	upgrade, _ := http.NewRequest(http.MethodGet, front.URL+"/socket.io/?EIO=4&transport=websocket", nil)
	upgrade.Header.Set("Connection", "Upgrade")
	upgrade.Header.Set("Upgrade", "websocket")
	upgradeOnly, _ := http.NewRequest(http.MethodGet, front.URL+"/ws", nil)
	upgradeOnly.Header.Set("Upgrade", "websocket")
	connect, _ := http.NewRequest(http.MethodConnect, front.URL, nil)
	connect.Host = "eval-meter:8080"
	for _, req := range []*http.Request{upgrade, upgradeOnly, connect} {
		if status, _ := send(t, req); status != http.StatusNotImplemented {
			t.Fatalf("%s %s: status=%d", req.Method, req.URL.Path, status)
		}
	}
	meter.seal()
	rows := stubRows(t, stub, stubPath)
	meterRows := readRows[engineRow](t, filepath.Join(meterPath, engineLogName))
	if len(seen()) != 0 || len(meterRows) != 0 || len(rows) != 3 {
		t.Fatalf("target saw %d meter rows %+v stub rows %+v", len(seen()), meterRows, rows)
	}
	for i, method := range []string{http.MethodGet, http.MethodGet, http.MethodConnect} {
		if rows[i].Decision != e2StubUnsupported || rows[i].Method != method || !attemptIDPattern.MatchString(rows[i].AttemptID) {
			t.Fatalf("row %d %+v", i, rows[i])
		}
	}
}
