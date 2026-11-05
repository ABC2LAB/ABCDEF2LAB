package main

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"
)

// The e2-stub role is a pass-through stand-in for the product E② enforcement
// point. It exists only to build and test the improved-engine slot of the
// evaluation topology (engine → E② → meter → target, evaluation protocol §2;
// E② proxy draft #189 §10.3). It admits every request, so a run through it
// proves the wiring, never control: every row is marked as a fixture and such
// runs are not official results. The product E② replaces this container.
//
// Guarantees (what the runner may rely on):
//   - A request is forwarded only after its decision row was written to the
//     log. Once a write fails, nothing more is forwarded and close reports the
//     log incomplete, which the runner treats as an invalid run. Rows are not
//     fsynced one by one; a host crash also loses the meter's seal markers, so
//     such a run is incomplete either way.
//   - Upgrade (WebSocket) and CONNECT requests are refused before anything is
//     sent upstream, as the product E² refuses unsupported protocols. Passing
//     them would let the target answer 101 while the engine side fails.
const e2StubLogName = "e2-stub-decisions.jsonl"

const (
	// e2StubAllowed is the only admission the stub makes; it forwards the request.
	e2StubAllowed = "allowed"
	// e2StubUnsupported refuses Upgrade and CONNECT before any upstream send.
	e2StubUnsupported = "unsupported"
	// e2StubUnavailable means no attempt ID could be issued (random source
	// failure or a collision), so the request was not forwarded (fail-closed).
	e2StubUnavailable = "unavailable"
	// attemptIDBytes is 128 bits of randomness, hex-encoded to the 32 characters
	// the meter accepts (attemptIDPattern).
	attemptIDBytes = 16
)

// e2StubRow is the decision for one request, written before it is forwarded.
// Response status and size are the meter's record (engine-requests.jsonl);
// the stub keeps no path, query, header or body field.
type e2StubRow struct {
	Seq      uint64 `json:"seq"`
	Fixture  bool   `json:"fixture"`
	Decision string `json:"decision"`
	// AttemptID is issued for every decision that got one, refused ones included
	// (#189 §10.2); only an allowed request carries it upstream.
	AttemptID string `json:"attempt_id"`
	// CallerAttempt is true when the caller sent its own attempt header. The stub
	// never trusts it and always replaces it with the ID it issued (#189 §10.2).
	CallerAttempt bool   `json:"caller_attempt"`
	Method        string `json:"method"`
	DecidedAt     string `json:"decided_at"`
}

type attemptIDKey struct{}

type e2Stub struct {
	proxy  *httputil.ReverseProxy
	random io.Reader
	now    func() time.Time

	mu        sync.Mutex
	log       *rowLog
	seq       uint64
	issued    map[string]bool
	logFailed bool
}

func newE2Stub(dir *os.File, target *url.URL, random io.Reader, now func() time.Time) (*e2Stub, string) {
	file, code := createPrivateFile(dir, e2StubLogName)
	if code != "" {
		return nil, code
	}
	proxy := newReverseProxy(target)
	rewrite := proxy.Rewrite
	proxy.Rewrite = func(r *httputil.ProxyRequest) {
		rewrite(r)
		// The shared rewrite removes any attempt header; the stub then sets the one it issued.
		if id, ok := r.In.Context().Value(attemptIDKey{}).(string); ok {
			r.Out.Header.Set(attemptHeader, id)
		}
	}
	return &e2Stub{proxy: proxy, random: random, now: now, log: &rowLog{file: file}, issued: map[string]bool{}}, ""
}

// issue returns a fresh attempt ID, or "" when none can be issued safely.
func (s *e2Stub) issueLocked() string {
	buf := make([]byte, attemptIDBytes)
	if _, err := io.ReadFull(s.random, buf); err != nil {
		return ""
	}
	id := hex.EncodeToString(buf)
	if s.issued[id] {
		return ""
	}
	s.issued[id] = true
	return id
}

func (s *e2Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	row := e2StubRow{Fixture: true, CallerAttempt: len(r.Header.Values(attemptHeader)) > 0, Method: r.Method}
	s.mu.Lock()
	s.seq++
	row.Seq = s.seq
	row.AttemptID = s.issueLocked()
	switch {
	case row.AttemptID == "":
		row.Decision = e2StubUnavailable
	case isUpgrade(r) || r.Method == http.MethodConnect:
		row.Decision = e2StubUnsupported
	default:
		row.Decision = e2StubAllowed
	}
	row.DecidedAt = stamp(s.now())
	recorded := !s.logFailed && s.log.write(row) == nil
	if !recorded {
		s.logFailed = true
	}
	s.mu.Unlock()
	switch {
	case !recorded || row.Decision == e2StubUnavailable:
		w.WriteHeader(http.StatusServiceUnavailable)
	case row.Decision == e2StubUnsupported:
		w.WriteHeader(http.StatusNotImplemented)
	default:
		s.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), attemptIDKey{}, row.AttemptID)))
	}
}

func (s *e2Stub) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	syncErr := s.log.file.Sync()
	closeErr := s.log.file.Close()
	if s.logFailed {
		return errors.Join(syncErr, closeErr, errors.New("eval-meter: e2-stub log incomplete"))
	}
	return errors.Join(syncErr, closeErr)
}
