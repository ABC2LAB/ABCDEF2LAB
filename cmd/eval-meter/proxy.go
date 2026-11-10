package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	engineLogName   = "engine-requests.jsonl"
	sealedName      = "engine-sealed.json"
	drainedName     = "engine-drained.json"
	terminatedName  = "engine-terminated.json"
	observerLogName = "observer-requests.jsonl"
)

// The proxy strips these inbound headers before Rewrite to stop spoofing; the
// meter restores them because it must forward the engine's request unchanged.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"}

var errSealed = errors.New("eval-meter: sealed")

// attemptHeader carries the E② attempt ID on requests E② admitted (evaluation
// protocol §8.3, E② proxy draft §6.1). The meter records a well-formed value and
// removes only this header before forwarding, so the target receives the same
// request in baseline and improved runs. A malformed value is counted, never
// stored, and never blocks the request.
const attemptHeader = "X-ABC2LAB-E2-Attempt"

var attemptIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Per-request stages. sent: no = nothing reached the target (sealed or no
// connection), uncertain = a connection existed but the full write was not
// confirmed, yes = the request was fully written. response: none = no byte from
// the target (a meter-made 503/502 is not a target response), headers = response
// started (or a switched protocol), complete = body relayed to the end,
// interrupted = response started but did not finish.
const (
	sentNo, sentUncertain, sentYes                                       = "no", "uncertain", "yes"
	responseNone, responseHeaders, responseComplete, responseInterrupted = "none", "headers", "complete", "interrupted"
	attemptAbsent, attemptValid, attemptInvalid, attemptMultiple         = "absent", "valid", "invalid", "multiple"
)

func classifyAttempt(h http.Header) (state, id string) {
	values := h.Values(attemptHeader)
	switch {
	case len(values) == 0:
		return attemptAbsent, ""
	case len(values) > 1:
		return attemptMultiple, ""
	case attemptIDPattern.MatchString(values[0]):
		return attemptValid, values[0]
	default:
		return attemptInvalid, ""
	}
}

// requestTrace observes the upstream round trip. Hooks run on transport
// goroutines and may fire after the handler returns, so flags are atomic; a late
// confirmation only makes the row more conservative (uncertain), never wrong.
type requestTrace struct {
	gotConn, wrote, firstByte atomic.Bool
}

func (t *requestTrace) client() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { t.gotConn.Store(true) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				t.wrote.Store(true)
			}
		},
		GotFirstResponseByte: func() { t.firstByte.Store(true) },
	}
}

func (t *requestTrace) sent() string {
	switch {
	case !t.gotConn.Load():
		return sentNo
	case t.wrote.Load():
		return sentYes
	default:
		return sentUncertain
	}
}

func newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// SetURL rewrites Host to the target; keep what the engine sent.
			r.Out.Host = r.In.Host
			for _, name := range forwardingHeaders {
				if values, ok := r.In.Header[name]; ok {
					r.Out.Header[name] = values
				}
			}
			r.Out.Header.Del(attemptHeader)
		},
		// The default handler logs the upstream error text, which may carry
		// addresses; the outcome code alone is enough for the evidence.
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			if rec, ok := w.(*statusRecorder); ok {
				rec.upstreamFailed = true
				if rec.hijacked {
					return
				}
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
}

func isUpgrade(r *http.Request) bool {
	return r.Header.Get("Upgrade") != "" || headerHasToken(r.Header, "Connection", "upgrade")
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// rowLog appends one JSON object per line to an exclusive 0600 file. Callers
// serialize writes with their own mutex.
type rowLog struct {
	file *os.File
}

func (l *rowLog) write(row any) error {
	line, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = l.file.Write(append(line, '\n'))
	return err
}

// engineRow deliberately has no path, query, header or body field.
type engineRow struct {
	Seq           uint64 `json:"seq"`
	Kind          string `json:"kind"`
	Method        string `json:"method"`
	Status        int    `json:"status"`
	StartedAt     string `json:"started_at"`
	CompletedAt   string `json:"completed_at"`
	ResponseBytes int64  `json:"response_bytes"`
	Outcome       string `json:"outcome"`
	// Sent and Response separate "reached the target" from "the target answered";
	// see the stage constants. They are progress of one attempt, not exclusive counts.
	Sent           string `json:"sent"`
	Response       string `json:"response"`
	AttemptIDState string `json:"attempt_id_state"`
	AttemptID      string `json:"attempt_id"`
}

type sealedMarker struct {
	Role            string `json:"role"`
	SealedAt        string `json:"sealed_at"`
	InflightAtSeal  int    `json:"inflight_at_seal"`
	ForcedClosed    uint64 `json:"forced_closed_streams"`
	LogComplete     bool   `json:"log_complete"`
	ForwardedAtSeal uint64 `json:"forwarded_at_seal"`
}

type drainedMarker struct {
	Role              string `json:"role"`
	SealedAt          string `json:"sealed_at"`
	DrainedAt         string `json:"drained_at"`
	Forwarded         uint64 `json:"forwarded_requests"`
	StreamsTotal      uint64 `json:"streams_total"`
	RejectedAfterSeal uint64 `json:"rejected_after_seal"`
	ForcedClosed      uint64 `json:"forced_closed_streams"`
	ForcedCancelled   uint64 `json:"forced_cancelled_requests"`
	InflightAtDrain   int    `json:"inflight_at_drain"`
	LogComplete       bool   `json:"log_complete"`
}

// terminatedMarker separates a drained stop from one where the front was
// stopped (signal or serve failure) with requests still in flight or without a
// seal; the runner treats anything but sealed+drained as no proof.
type terminatedMarker struct {
	Role                string `json:"role"`
	Reason              string `json:"reason"`
	TerminatedAt        string `json:"terminated_at"`
	Sealed              bool   `json:"sealed"`
	Drained             bool   `json:"drained"`
	InflightAtTerminate int    `json:"inflight_at_terminate"`
	OpenStreams         int    `json:"open_streams"`
	ForcedCancelled     uint64 `json:"forced_cancelled_requests"`
	Forwarded           uint64 `json:"forwarded_requests"`
	RejectedAfterSeal   uint64 `json:"rejected_after_seal"`
	LogComplete         bool   `json:"log_complete"`
}

// engineMeter counts admitted requests. An upgrade handshake stays in flight
// until the connection is hijacked; from then on it is a stream that seal
// closes, so a long-lived socket cannot block the drain forever. A plain
// request that is still open drainGrace after seal (a long poll, a response the
// target never finishes) is cancelled toward the target and counted, for the
// same reason; every request was admitted before seal, so nothing new is sent.
type engineMeter struct {
	proxy      *httputil.ReverseProxy
	dir        *os.File
	now        func() time.Time
	drainGrace time.Duration

	mu           sync.Mutex
	log          *rowLog
	paths        *rowLog
	pathKey      []byte
	seq          uint64
	sealed       bool
	sealedAt     time.Time
	inflight     int
	forwarded    uint64
	streamsTotal uint64
	rejected     uint64
	forcedClosed uint64
	streams      map[uint64]net.Conn
	forced       map[uint64]bool
	cancels      map[uint64]context.CancelFunc
	cancelled    map[uint64]bool
	forcedCancel uint64
	graceTimer   *time.Timer
	logFailed    bool
	drainedAt    time.Time
	drained      chan struct{}
	markerFailed bool
}

func newEngineMeter(dir *os.File, target *url.URL, now func() time.Time, drainGrace time.Duration) (*engineMeter, string) {
	file, code := createPrivateFile(dir, engineLogName)
	if code != "" {
		return nil, code
	}
	return &engineMeter{
		proxy:      newReverseProxy(target),
		dir:        dir,
		now:        now,
		drainGrace: drainGrace,
		log:        &rowLog{file: file},
		streams:    map[uint64]net.Conn{},
		forced:     map[uint64]bool{},
		cancels:    map[uint64]context.CancelFunc{},
		cancelled:  map[uint64]bool{},
		drained:    make(chan struct{}),
	}, ""
}

func (m *engineMeter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := m.now()
	attemptState, attemptID := classifyAttempt(r.Header)
	m.mu.Lock()
	m.seq++
	seq := m.seq
	if m.sealed || m.logFailed {
		m.rejected++
		m.writeRowLocked(engineRow{Seq: seq, Kind: "rejected", Method: r.Method, Status: http.StatusServiceUnavailable,
			StartedAt: stamp(started), CompletedAt: stamp(m.now()), Outcome: "rejected_after_seal",
			Sent: sentNo, Response: responseNone, AttemptIDState: attemptState, AttemptID: attemptID})
		m.mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	m.inflight++
	m.forwarded++
	if isUpgrade(r) {
		m.streamsTotal++
	}
	m.writePathLocked(seq, r)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	m.cancels[seq] = cancel
	m.mu.Unlock()

	rec := &statusRecorder{ResponseWriter: w, hijack: func(conn net.Conn) error { return m.trackStream(seq, conn) }}
	trace := &requestTrace{}
	ctx = httptrace.WithClientTrace(ctx, trace.client())
	finished := false
	// The proxy aborts a response whose body copy fails (the grace cancelling it
	// mid-body, the engine or target dropping the connection) with
	// panic(http.ErrAbortHandler); the bookkeeping must still run once or the
	// drain would wait forever.
	defer func() { m.finishRequest(seq, r.Method, started, rec, trace, !finished, attemptState, attemptID) }()
	m.proxy.ServeHTTP(rec, r.WithContext(ctx))
	finished = true
}

func (m *engineMeter) finishRequest(seq uint64, method string, started time.Time, rec *statusRecorder,
	trace *requestTrace, aborted bool, attemptState, attemptID string) {
	completed := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cancels, seq)
	row := engineRow{Seq: seq, Kind: "request", Method: method, Status: rec.status, StartedAt: stamp(started),
		CompletedAt: stamp(completed), ResponseBytes: rec.bytes, Outcome: "completed",
		Sent: trace.sent(), Response: responseNone, AttemptIDState: attemptState, AttemptID: attemptID}
	if trace.firstByte.Load() {
		row.Response = responseComplete
		if aborted || rec.upstreamFailed {
			row.Response = responseInterrupted
		}
	}
	switch {
	case rec.upstreamFailed:
		row.Outcome = "upstream_error"
	case aborted:
		row.Outcome = "aborted"
	}
	if rec.hijacked {
		row.Kind = "stream"
		// A switched protocol has no relayed body to complete.
		row.Response = responseHeaders
		delete(m.streams, seq)
		if m.forced[seq] {
			row.Outcome = "forced_closed"
			delete(m.forced, seq)
		}
	} else {
		m.inflight--
		if m.cancelled[seq] {
			row.Outcome = "forced_cancelled"
			delete(m.cancelled, seq)
		}
	}
	m.writeRowLocked(row)
	m.maybeDrainedLocked(completed)
}

// trackStream converts an in-flight handshake into a stream. A handshake that
// completes after seal is closed immediately and counted as forced.
func (m *engineMeter) trackStream(seq uint64, conn net.Conn) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight--
	delete(m.cancels, seq) // a stream is closed by seal, not cancelled as a request
	if m.cancelled[seq] {
		// The grace ran out during the handshake: the row will be a forced-closed
		// stream, so it must not also count as a cancelled request.
		m.forcedCancel--
		delete(m.cancelled, seq)
	}
	if m.sealed {
		m.forcedClosed++
		m.forced[seq] = true
		_ = conn.Close()
		m.maybeDrainedLocked(m.now())
		return errSealed
	}
	m.streams[seq] = conn
	return nil
}

func (m *engineMeter) seal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sealed {
		return
	}
	m.sealed = true
	m.sealedAt = m.now()
	for seq, conn := range m.streams {
		_ = conn.Close()
		m.forcedClosed++
		m.forced[seq] = true
		delete(m.streams, seq)
	}
	m.syncLogsLocked()
	m.writeMarkerLocked(sealedName, sealedMarker{Role: roleEngine, SealedAt: stamp(m.sealedAt), InflightAtSeal: m.inflight,
		ForcedClosed: m.forcedClosed, LogComplete: !m.logFailed, ForwardedAtSeal: m.forwarded})
	m.maybeDrainedLocked(m.sealedAt)
	if m.drainedAt.IsZero() && m.drainGrace > 0 {
		m.graceTimer = time.AfterFunc(m.drainGrace, m.cancelInflight)
	}
}

// cancelInflight ends the requests still open drainGrace after seal. Each
// handler then returns, writes its row as forced_cancelled and lets the drain
// complete; the count goes into the drained and terminated markers.
func (m *engineMeter) cancelInflight() {
	m.mu.Lock()
	defer m.mu.Unlock()
	// After the drain nothing is left here: finished requests and hijacked
	// streams have removed their entries and sealed requests never add one.
	for seq, cancel := range m.cancels {
		cancel()
		m.forcedCancel++
		m.cancelled[seq] = true
		delete(m.cancels, seq)
	}
}

func (m *engineMeter) maybeDrainedLocked(at time.Time) {
	if !m.sealed || m.inflight != 0 || !m.drainedAt.IsZero() {
		return
	}
	m.drainedAt = at
	m.syncLogsLocked()
	m.writeMarkerLocked(drainedName, drainedMarker{Role: roleEngine, SealedAt: stamp(m.sealedAt), DrainedAt: stamp(at),
		Forwarded: m.forwarded, StreamsTotal: m.streamsTotal, RejectedAfterSeal: m.rejected,
		ForcedClosed: m.forcedClosed, ForcedCancelled: m.forcedCancel, InflightAtDrain: m.inflight,
		LogComplete: !m.logFailed})
	close(m.drained)
}

// A lost row makes the evidence incomplete: stop admitting new requests and
// let the markers report it instead of silently continuing.
func (m *engineMeter) writeRowLocked(row engineRow) {
	if err := m.log.write(row); err != nil {
		m.logFailed = true
	}
}

// enablePathLog는 보존 시험에서만 부른다. 끄면 기록 파일 자체가 생기지 않는다.
func (m *engineMeter) enablePathLog(key []byte) string {
	if len(key) != pathKeyBytes {
		return invalidInput
	}
	file, code := createPrivateFile(m.dir, enginePathLogName)
	if code != "" {
		return code
	}
	m.paths, m.pathKey = &rowLog{file: file}, key
	return ""
}

// marker의 LogComplete는 요청 기록과 경로 기록이 모두 디스크에 내려간 뒤에만 참이다.
func (m *engineMeter) syncLogsLocked() {
	if err := m.log.file.Sync(); err != nil {
		m.logFailed = true
	}
	if m.paths != nil {
		if err := m.paths.file.Sync(); err != nil {
			m.logFailed = true
		}
	}
}

// discard는 경로 기록을 열지 못해 시작을 포기할 때 방금 만든 요청 기록을 지운다.
// 남겨 두면 다음 시작이 logExists로 막힌다.
func (m *engineMeter) discard() error {
	return errors.Join(m.log.file.Close(), removePrivateFile(m.dir, engineLogName))
}

func (m *engineMeter) writePathLocked(seq uint64, r *http.Request) {
	if m.paths == nil {
		return
	}
	pattern, masked := maskPath(r.URL.EscapedPath(), m.pathKey)
	if err := m.paths.write(pathRow{Seq: seq, Method: r.Method, PathPattern: pattern, MaskedSegments: masked, PathProfileRef: pathMaskProfile}); err != nil {
		m.logFailed = true
	}
}

func (m *engineMeter) writeMarkerLocked(name string, marker any) {
	if err := writeMarker(m.dir, name, marker); err != nil {
		m.markerFailed = true
	}
}

// terminate records the state at the moment the front is told to stop, before
// shutdown waits for in-flight requests, so an undrained stop stays visible.
func (m *engineMeter) terminate(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncLogsLocked()
	m.writeMarkerLocked(terminatedName, terminatedMarker{Role: roleEngine, Reason: reason, TerminatedAt: stamp(m.now()),
		Sealed: m.sealed, Drained: !m.drainedAt.IsZero(), InflightAtTerminate: m.inflight, OpenStreams: len(m.streams),
		ForcedCancelled: m.forcedCancel, Forwarded: m.forwarded, RejectedAfterSeal: m.rejected, LogComplete: !m.logFailed})
}

func (m *engineMeter) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.graceTimer != nil {
		m.graceTimer.Stop()
	}
	syncErr := m.log.file.Sync()
	closeErr := m.log.file.Close()
	var pathsErr error
	if m.paths != nil {
		pathsErr = errors.Join(m.paths.file.Sync(), m.paths.file.Close())
	}
	if m.markerFailed {
		return errors.Join(syncErr, closeErr, pathsErr, errors.New("eval-meter: marker write failed"))
	}
	return errors.Join(syncErr, closeErr, pathsErr)
}

// observerRow records the decision only; the registered path itself lives in
// the runner's configuration, not in each row.
type observerRow struct {
	Seq           uint64 `json:"seq"`
	Decision      string `json:"decision"`
	Method        string `json:"method"`
	Status        int    `json:"status"`
	StartedAt     string `json:"started_at"`
	CompletedAt   string `json:"completed_at"`
	ResponseBytes int64  `json:"response_bytes"`
}

type observer struct {
	proxy *httputil.ReverseProxy
	path  string
	now   func() time.Time

	mu        sync.Mutex
	log       *rowLog
	seq       uint64
	logFailed bool
}

func newObserver(dir *os.File, target *url.URL, path string, now func() time.Time) (*observer, string) {
	file, code := createPrivateFile(dir, observerLogName)
	if code != "" {
		return nil, code
	}
	return &observer{proxy: newReverseProxy(target), path: path, now: now, log: &rowLog{file: file}}, ""
}

// registered requires the exact GET with no query, body, encoded path or
// upgrade; GET alone is not evidence that a path is read-only, so only the
// path the runner verified may pass.
func (o *observer) registered(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == o.path && r.URL.RawPath == "" && r.URL.RawQuery == "" &&
		!r.URL.ForceQuery && r.ContentLength == 0 && len(r.TransferEncoding) == 0 && !isUpgrade(r)
}

func (o *observer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := o.now()
	o.mu.Lock()
	o.seq++
	seq := o.seq
	failed := o.logFailed
	o.mu.Unlock()
	if failed || !o.registered(r) {
		w.WriteHeader(http.StatusForbidden)
		o.record(observerRow{Seq: seq, Decision: "rejected", Method: r.Method, Status: http.StatusForbidden,
			StartedAt: stamp(started), CompletedAt: stamp(o.now())})
		return
	}
	rec := &statusRecorder{ResponseWriter: w}
	o.proxy.ServeHTTP(rec, r)
	o.record(observerRow{Seq: seq, Decision: "registered", Method: r.Method, Status: rec.status,
		StartedAt: stamp(started), CompletedAt: stamp(o.now()), ResponseBytes: rec.bytes})
}

// Observer volume is a handful of requests per run, so each row is synced to
// make the count readable while the front keeps serving.
func (o *observer) record(row observerRow) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.log.write(row); err != nil {
		o.logFailed = true
		return
	}
	if err := o.log.file.Sync(); err != nil {
		o.logFailed = true
	}
}

func (o *observer) close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	syncErr := o.log.file.Sync()
	closeErr := o.log.file.Close()
	if o.logFailed {
		return errors.Join(syncErr, closeErr, errors.New("eval-meter: observer log incomplete"))
	}
	return errors.Join(syncErr, closeErr)
}

// statusRecorder captures status and body size. hijack lets the engine meter
// own an upgraded connection so seal can close it.
type statusRecorder struct {
	http.ResponseWriter
	hijack         func(net.Conn) error
	status         int
	bytes          int64
	hijacked       bool
	upstreamFailed bool
	writeFailed    bool // 응답을 호출자에게 쓰지 못했다(호출자가 먼저 닫은 경우)
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	if err != nil {
		r.writeFailed = true
	}
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok || r.hijack == nil {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := h.Hijack()
	if err != nil {
		return nil, nil, err
	}
	r.hijacked = true
	r.status = http.StatusSwitchingProtocols
	if err := r.hijack(conn); err != nil {
		return nil, nil, err
	}
	return conn, rw, nil
}
