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
const e2StubLogName = "e2-stub-decisions.jsonl"

const (
	// e2StubAllowed is the only admission decision the stub makes.
	e2StubAllowed = "allowed"
	// e2StubUnavailable means no attempt ID could be issued (random source
	// failure or a collision), so the request was not forwarded (fail-closed).
	e2StubUnavailable = "unavailable"
	// attemptIDBytes is 128 bits of randomness, hex-encoded to the 32 characters
	// the meter accepts (attemptIDPattern).
	attemptIDBytes = 16
)

type e2StubRow struct {
	Seq       uint64 `json:"seq"`
	Fixture   bool   `json:"fixture"`
	Decision  string `json:"decision"`
	AttemptID string `json:"attempt_id"`
	// CallerAttempt is true when the caller sent its own attempt header. The stub
	// never trusts it and always replaces it with the ID it issued (#189 §10.2).
	CallerAttempt bool   `json:"caller_attempt"`
	Method        string `json:"method"`
	Status        int    `json:"status"`
	StartedAt     string `json:"started_at"`
	CompletedAt   string `json:"completed_at"`
	ResponseBytes int64  `json:"response_bytes"`
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
	started := s.now()
	caller := len(r.Header.Values(attemptHeader)) > 0
	s.mu.Lock()
	s.seq++
	seq := s.seq
	id := ""
	if !s.logFailed {
		id = s.issueLocked()
	}
	s.mu.Unlock()
	if id == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		s.record(e2StubRow{Seq: seq, Fixture: true, Decision: e2StubUnavailable, CallerAttempt: caller,
			Method: r.Method, Status: http.StatusServiceUnavailable, StartedAt: stamp(started),
			CompletedAt: stamp(s.now())})
		return
	}
	rec := &statusRecorder{ResponseWriter: w}
	// The proxy aborts a response whose body copy fails with panic(http.ErrAbortHandler);
	// the deferred record still runs, and the panic keeps unwinding to the server.
	defer func() {
		s.record(e2StubRow{Seq: seq, Fixture: true, Decision: e2StubAllowed, AttemptID: id, CallerAttempt: caller,
			Method: r.Method, Status: rec.status, StartedAt: stamp(started), CompletedAt: stamp(s.now()),
			ResponseBytes: rec.bytes})
	}()
	s.proxy.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), attemptIDKey{}, id)))
}

func (s *e2Stub) record(row e2StubRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.log.write(row); err != nil {
		s.logFailed = true
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
