package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
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
	Forwarded           uint64 `json:"forwarded_requests"`
	RejectedAfterSeal   uint64 `json:"rejected_after_seal"`
	LogComplete         bool   `json:"log_complete"`
}

// engineMeter counts admitted requests. An upgrade handshake stays in flight
// until the connection is hijacked; from then on it is a stream that seal
// closes, so a long-lived socket cannot block the drain forever.
type engineMeter struct {
	proxy *httputil.ReverseProxy
	dir   *os.File
	now   func() time.Time

	mu           sync.Mutex
	log          *rowLog
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
	logFailed    bool
	drainedAt    time.Time
	drained      chan struct{}
	markerFailed bool
}

func newEngineMeter(dir *os.File, target *url.URL, now func() time.Time) (*engineMeter, string) {
	file, code := createPrivateFile(dir, engineLogName)
	if code != "" {
		return nil, code
	}
	return &engineMeter{
		proxy:   newReverseProxy(target),
		dir:     dir,
		now:     now,
		log:     &rowLog{file: file},
		streams: map[uint64]net.Conn{},
		forced:  map[uint64]bool{},
		drained: make(chan struct{}),
	}, ""
}

func (m *engineMeter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := m.now()
	m.mu.Lock()
	m.seq++
	seq := m.seq
	if m.sealed || m.logFailed {
		m.rejected++
		m.writeRowLocked(engineRow{Seq: seq, Kind: "rejected", Method: r.Method, Status: http.StatusServiceUnavailable,
			StartedAt: stamp(started), CompletedAt: stamp(m.now()), Outcome: "rejected_after_seal"})
		m.mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	m.inflight++
	m.forwarded++
	if isUpgrade(r) {
		m.streamsTotal++
	}
	m.mu.Unlock()

	rec := &statusRecorder{ResponseWriter: w, hijack: func(conn net.Conn) error { return m.trackStream(seq, conn) }}
	m.proxy.ServeHTTP(rec, r)
	completed := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	row := engineRow{Seq: seq, Kind: "request", Method: r.Method, Status: rec.status, StartedAt: stamp(started),
		CompletedAt: stamp(completed), ResponseBytes: rec.bytes, Outcome: "completed"}
	if rec.upstreamFailed {
		row.Outcome = "upstream_error"
	}
	if rec.hijacked {
		row.Kind = "stream"
		delete(m.streams, seq)
		if m.forced[seq] {
			row.Outcome = "forced_closed"
			delete(m.forced, seq)
		}
	} else {
		m.inflight--
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
	if err := m.log.file.Sync(); err != nil {
		m.logFailed = true
	}
	m.writeMarkerLocked(sealedName, sealedMarker{Role: roleEngine, SealedAt: stamp(m.sealedAt), InflightAtSeal: m.inflight,
		ForcedClosed: m.forcedClosed, LogComplete: !m.logFailed, ForwardedAtSeal: m.forwarded})
	m.maybeDrainedLocked(m.sealedAt)
}

func (m *engineMeter) maybeDrainedLocked(at time.Time) {
	if !m.sealed || m.inflight != 0 || !m.drainedAt.IsZero() {
		return
	}
	m.drainedAt = at
	if err := m.log.file.Sync(); err != nil {
		m.logFailed = true
	}
	m.writeMarkerLocked(drainedName, drainedMarker{Role: roleEngine, SealedAt: stamp(m.sealedAt), DrainedAt: stamp(at),
		Forwarded: m.forwarded, StreamsTotal: m.streamsTotal, RejectedAfterSeal: m.rejected,
		ForcedClosed: m.forcedClosed, InflightAtDrain: m.inflight, LogComplete: !m.logFailed})
	close(m.drained)
}

// A lost row makes the evidence incomplete: stop admitting new requests and
// let the markers report it instead of silently continuing.
func (m *engineMeter) writeRowLocked(row engineRow) {
	if err := m.log.write(row); err != nil {
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
	if err := m.log.file.Sync(); err != nil {
		m.logFailed = true
	}
	m.writeMarkerLocked(terminatedName, terminatedMarker{Role: roleEngine, Reason: reason, TerminatedAt: stamp(m.now()),
		Sealed: m.sealed, Drained: !m.drainedAt.IsZero(), InflightAtTerminate: m.inflight, OpenStreams: len(m.streams),
		Forwarded: m.forwarded, RejectedAfterSeal: m.rejected, LogComplete: !m.logFailed})
}

func (m *engineMeter) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	syncErr := m.log.file.Sync()
	closeErr := m.log.file.Close()
	if m.markerFailed {
		return errors.Join(syncErr, closeErr, errors.New("eval-meter: marker write failed"))
	}
	return errors.Join(syncErr, closeErr)
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
