// Package explorefixture는 소유 Docker 수용시험 전용 HTTP 경계다. 제품 권한·운영 경계가 아니다.
package explorefixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

const Version = "jindo.exploration-contract-fixture/v1"

var ErrFixture = errors.New("owned exploration fixture invalid")

type Binding struct {
	SessionRef     string `json:"session_ref"`
	RunRef         string `json:"run_ref"`
	JobID          string `json:"job_id"`
	TaskID         string `json:"task_id"`
	TargetRef      string `json:"target_ref"`
	TargetRevision uint64 `json:"target_revision"`
	SourceDigest   string `json:"source_digest"`
}
type Config struct {
	MaxExternalRequests uint64   `json:"max_external_requests"`
	MaxConcurrency      int      `json:"max_concurrency"`
	Format              string   `json:"format"`
	OwnedFixture        bool     `json:"owned_fixture"`
	BrowserListen       string   `json:"browser_listen"`
	DirectListen        string   `json:"direct_listen"`
	ControlSocket       string   `json:"control_socket"`
	TargetOrigin        string   `json:"target_origin"`
	TargetAddress       string   `json:"target_address"`
	CDNOrigin           string   `json:"cdn_origin"`
	CDNAddress          string   `json:"cdn_address"`
	TargetPrefixes      []string `json:"target_prefixes"`
	MaxRequests         uint64   `json:"max_requests"`
	MaxResponseBytes    int64    `json:"max_response_bytes"`
	DurationSeconds     int      `json:"duration_seconds"`
	CACertFile          string   `json:"ca_cert_file"`
	LeafCertFile        string   `json:"leaf_cert_file"`
	LeafKeyFile         string   `json:"leaf_key_file"`
}
type Observation struct {
	Binding             Binding   `json:"binding"`
	Seq                 uint64    `json:"seq"`
	At                  time.Time `json:"at"`
	Method              string    `json:"method"`
	Path                string    `json:"path"`
	Source              string    `json:"source"`
	Kind                string    `json:"kind"`
	Transport           string    `json:"transport"`
	Status              int       `json:"status"`
	Forwarded           bool      `json:"forwarded"`
	ResponseReceived    bool      `json:"response_received"`
	QueryKeys           []string  `json:"query_keys"`
	BodyKeys            []string  `json:"body_keys"`
	ContentType         string    `json:"content_type"`
	ResponseContentType string    `json:"response_content_type"`
	ResponseSHA256      [32]byte  `json:"response_sha256"`
}
type State struct {
	Limits            exploration.Limits `json:"limits"`
	CommMode          string             `json:"comm_mode"`
	WriteMode         string             `json:"write_mode"`
	TargetAttempts    uint64             `json:"target_attempts"`
	ExternalAttempts  uint64             `json:"external_attempts"`
	Attempts          uint64             `json:"attempts"`
	ActiveConnections int                `json:"active_connections"`
	Binding           Binding            `json:"binding"`
	Active            bool               `json:"active"`
	Reason            string             `json:"reason"`
	Forwarded         uint64             `json:"forwarded"`
	External          uint64             `json:"external"`
	Denied            map[string]uint64  `json:"denied"`
	RecordCount       uint64             `json:"record_count"`
}
type Fixture struct {
	connections         map[net.Conn]bool
	pendingConnections  int
	reserved            uint64
	cfg                 Config
	transport           *http.Transport
	cert                tls.Certificate
	mu                  sync.Mutex
	state               State
	observations        map[uint64]Observation
	commMode, writeMode string
	deadline            time.Time
}

func New(c Config) (*Fixture, error) {
	if c.Format != Version || !c.OwnedFixture || c.MaxConcurrency < 1 || c.MaxConcurrency > 64 || c.MaxRequests < 1 || c.MaxRequests > 2000 || c.MaxExternalRequests > 2000 || c.MaxResponseBytes < 1024 || c.MaxResponseBytes > 2<<20 || c.DurationSeconds < 1 || c.DurationSeconds > 600 || len(c.TargetPrefixes) == 0 {
		return nil, ErrFixture
	}
	for _, v := range []struct{ origin, address string }{{c.TargetOrigin, c.TargetAddress}, {c.CDNOrigin, c.CDNAddress}} {
		u, e := url.Parse(v.origin)
		ip, port, e2 := net.SplitHostPort(v.address)
		if e != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".test") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || e2 != nil || net.ParseIP(ip) == nil || (!net.ParseIP(ip).IsPrivate() && !net.ParseIP(ip).IsLoopback()) || port == "" {
			return nil, ErrFixture
		}
	}
	cert, e := tls.LoadX509KeyPair(c.LeafCertFile, c.LeafKeyFile)
	if e != nil {
		return nil, ErrFixture
	}
	raw, e := os.ReadFile(c.CACertFile)
	if e != nil {
		return nil, ErrFixture
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, ErrFixture
	}
	f := &Fixture{connections: map[net.Conn]bool{}, cfg: c, cert: cert, observations: map[uint64]Observation{}, state: State{Denied: map[string]uint64{}}}
	f.transport = &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		var target string
		switch address {
		case strings.TrimPrefix(c.TargetOrigin, "https://"):
			target = c.TargetAddress
		case strings.TrimPrefix(c.CDNOrigin, "https://"):
			target = c.CDNAddress
		default:
			return nil, ErrFixture
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, target)
	}, ResponseHeaderTimeout: 5 * time.Second, MaxIdleConns: 4}
	return f, nil
}
func (f *Fixture) Snapshot() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.state
	s.Denied = map[string]uint64{}
	for k, v := range f.state.Denied {
		s.Denied[k] = v
	}
	return s
}
func (f *Fixture) Control() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/open":
			var in struct {
				Binding   Binding            `json:"binding"`
				Limits    exploration.Limits `json:"limits"`
				CommMode  string             `json:"comm_mode"`
				WriteMode string             `json:"write_mode"`
			}
			d := json.NewDecoder(io.LimitReader(r.Body, 8192))
			d.DisallowUnknownFields()
			if r.Method != "POST" || d.Decode(&in) != nil || in.Binding.SessionRef == "" || in.Binding.RunRef == "" || in.Binding.JobID == "" || in.Binding.TaskID == "" || in.Binding.TargetRef == "" || in.Binding.TargetRevision == 0 || len(in.Binding.SourceDigest) != 64 || (in.CommMode != "full_local" && in.CommMode != "external_allowed") || (in.WriteMode != "read_only" && in.WriteMode != "approved_writes") {
				http.Error(w, "invalid fixture binding", 400)
				return
			}
			expected := exploration.Limits{TimeBudgetSeconds: int64(f.cfg.DurationSeconds), MaxRequests: f.cfg.MaxRequests, MaxExternalRequests: f.cfg.MaxExternalRequests, MaxConcurrency: f.cfg.MaxConcurrency, MaxResponseBytes: f.cfg.MaxResponseBytes}
			if in.CommMode == exploration.FullLocal {
				expected.MaxExternalRequests = 0
			}
			if in.Limits != expected {
				http.Error(w, "fixture limits mismatch", 400)
				return
			}
			f.mu.Lock()
			if f.state.Binding.SessionRef != "" {
				f.mu.Unlock()
				http.Error(w, "fixture already issued", 409)
				return
			}
			f.state.Binding = in.Binding
			f.state.Limits = in.Limits
			f.state.CommMode = in.CommMode
			f.state.WriteMode = in.WriteMode
			f.state.Active = true
			f.commMode = in.CommMode
			f.writeMode = in.WriteMode
			f.deadline = time.Now().Add(time.Duration(f.cfg.DurationSeconds) * time.Second)
			f.mu.Unlock()
			json.NewEncoder(w).Encode(f.Snapshot())
		case "/status":
			json.NewEncoder(w).Encode(f.Snapshot())
		case "/close":
			if r.Method != "POST" {
				http.Error(w, "method", 405)
				return
			}
			var in struct {
				Reason string `json:"reason"`
			}
			_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in)
			reason := in.Reason
			if reason == "" {
				reason = "cancelled"
			}
			f.Close(reason)
			json.NewEncoder(w).Encode(f.Snapshot())
		case "/observation":
			var in struct {
				Binding Binding `json:"binding"`
				Seq     uint64  `json:"seq"`
			}
			d := json.NewDecoder(io.LimitReader(r.Body, 8192))
			d.DisallowUnknownFields()
			if r.Method != "POST" || d.Decode(&in) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			f.mu.Lock()
			o, ok := f.observations[in.Seq]
			same := in.Binding == f.state.Binding
			f.mu.Unlock()
			if !ok || !same {
				http.Error(w, "not found", 404)
				return
			}
			json.NewEncoder(w).Encode(o)
		default:
			http.NotFound(w, r)
		}
	})
}
func (f *Fixture) deny(w http.ResponseWriter, reason string) {
	f.mu.Lock()
	f.state.Denied[reason]++
	f.mu.Unlock()
	w.Header().Set("X-Owned-Fixture-Denial", reason)
	http.Error(w, reason, 403)
}
func (f *Fixture) admit(r *http.Request, ingress, origin string) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.state.Active {
		return false, "session_closed"
	}
	if time.Now().After(f.deadline) {
		f.state.Active = false
		f.state.Reason = "cap_exhausted"
		return false, "cap_exhausted"
	}
	if origin == f.cfg.CDNOrigin {
		if f.commMode == "full_local" {
			return false, "external_blocked_local"
		}
		if ingress != "browser" {
			return false, "external_direct"
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			return false, "external_method"
		}
		if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
			return false, "external_navigation"
		}
		if f.state.ExternalAttempts >= f.state.Limits.MaxExternalRequests {
			f.state.Active = false
			f.state.Reason = "cap_exhausted"
			return false, "cap_exhausted"
		}
		f.state.ExternalAttempts++
		f.reserved++
		f.state.Attempts++
		return true, ""
	}
	if origin != f.cfg.TargetOrigin {
		return false, "out_of_scope_origin"
	}
	allowed := false
	for _, p := range f.cfg.TargetPrefixes {
		if r.URL.Path == p || strings.HasPrefix(r.URL.Path, strings.TrimSuffix(p, "/")+"/") {
			allowed = true
		}
	}
	if !allowed {
		return false, "out_of_scope_path"
	}
	low := strings.ToLower(r.URL.Path)
	for _, p := range []string{"delete", "logout", "reset", "destroy"} {
		if strings.Contains(low, p) {
			return false, "risky_path"
		}
	}
	if r.Method == "DELETE" || (f.writeMode == "read_only" && r.Method != "GET" && r.Method != "HEAD") {
		return false, "method_not_approved"
	}
	if f.state.TargetAttempts >= f.cfg.MaxRequests {
		f.state.Active = false
		f.state.Reason = "cap_exhausted"
		return false, "cap_exhausted"
	}
	f.state.TargetAttempts++
	f.reserved++
	f.state.Attempts++
	return true, ""
}
func media(raw string) string {
	m, _, e := mime.ParseMediaType(raw)
	if e != nil {
		return ""
	}
	return m
}
func keys(raw []byte) []string {
	var o map[string]json.RawMessage
	if json.Unmarshal(raw, &o) != nil {
		return nil
	}
	out := []string{}
	for k := range o {
		if len(k) > 0 && len(k) <= 64 && len(out) < 32 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Close는 시험이 만든 연결만 닫고 진행 중인 CONNECT 처리기가 빠질 때까지 제한 시간 안에서 기다린다.
func (f *Fixture) Close(reason string) {
	f.mu.Lock()
	f.state.Active = false
	f.state.Reason = reason
	connections := make([]net.Conn, 0, len(f.connections))
	for c := range f.connections {
		connections = append(connections, c)
	}
	f.mu.Unlock()
	for _, c := range connections {
		_ = c.Close()
	}
	f.transport.CloseIdleConnections()
	deadline := time.Now().Add(time.Second)
	for f.Snapshot().ActiveConnections > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}
func (f *Fixture) Proxy(ingress string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ingress != "browser" && ingress != "direct" {
			f.deny(w, "ingress_invalid")
			return
		}
		if r.Method == "CONNECT" {
			f.connect(w, r, ingress)
			return
		}
		if r.URL.Scheme != "https" || r.URL.Host == "" || r.Host != r.URL.Host || r.URL.User != nil {
			f.deny(w, "origin_invalid")
			return
		}
		origin := "https://" + r.URL.Host
		if ok, reason := f.admit(r, ingress, origin); !ok {
			f.deny(w, reason)
			return
		}
		defer func() { f.mu.Lock(); f.reserved--; f.mu.Unlock() }()
		requestBody, e := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
		if e != nil || len(requestBody) > 64<<10 {
			http.Error(w, "request failed", 413)
			return
		}
		out := r.Clone(r.Context())
		out.RequestURI = ""
		out.Header = r.Header.Clone()
		out.Header.Del("Proxy-Authorization")
		out.Header.Del("Proxy-Connection")
		out.Body = io.NopCloser(bytes.NewReader(requestBody))
		response, e := f.transport.RoundTrip(out)
		if e != nil {
			http.Error(w, "upstream TLS or connection failed", 502)
			return
		}
		defer response.Body.Close()
		body, e := io.ReadAll(io.LimitReader(response.Body, f.cfg.MaxResponseBytes+1))
		if e != nil || int64(len(body)) > f.cfg.MaxResponseBytes {
			http.Error(w, "bounded fixture response exceeded", 502)
			return
		}
		f.mu.Lock()
		if origin == f.cfg.CDNOrigin {
			f.state.External++
			f.mu.Unlock()
			for k, vs := range response.Header {
				if !strings.EqualFold(k, "Connection") {
					for _, v := range vs {
						w.Header().Add(k, v)
					}
				}
			}
			w.WriteHeader(response.StatusCode)
			w.Write(body)
			return
		}
		f.state.Forwarded++
		seq := f.state.Forwarded
		f.state.RecordCount = seq
		source, kind := "browser_xhr", "api"
		if ingress == "direct" {
			source = "api_recon_runtime"
		}
		if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
			source = "browser_navigation"
			kind = "page"
		}
		if r.Method == "POST" && r.Header.Get("Sec-Fetch-Mode") == "navigate" {
			source = "browser_form"
			kind = "form"
		}
		query := []string{}
		for k := range r.URL.Query() {
			if len(k) > 0 && len(k) <= 64 && len(query) < 32 {
				query = append(query, k)
			}
		}
		sort.Strings(query)
		f.observations[seq] = Observation{Binding: f.state.Binding, Seq: seq, At: time.Now().UTC(), Method: r.Method, Path: r.URL.Path, Source: source, Kind: kind, Transport: "http", Status: response.StatusCode, Forwarded: true, ResponseReceived: true, QueryKeys: query, BodyKeys: keys(requestBody), ContentType: media(r.Header.Get("Content-Type")), ResponseContentType: media(response.Header.Get("Content-Type")), ResponseSHA256: sha256.Sum256(body)}
		f.mu.Unlock()
		for k, vs := range response.Header {
			if !strings.EqualFold(k, "Connection") {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
		}
		w.Header().Set("X-Jindo-Fixture-Sequence", strconv.FormatUint(seq, 10))
		w.WriteHeader(response.StatusCode)
		w.Write(body)
	})
}
func (f *Fixture) connect(w http.ResponseWriter, r *http.Request, ingress string) {
	origin := "https://" + r.Host
	f.mu.Lock()
	if !f.state.Active {
		f.mu.Unlock()
		f.deny(w, "session_closed")
		return
	}
	if len(f.connections)+f.pendingConnections >= f.cfg.MaxConcurrency {
		f.mu.Unlock()
		f.deny(w, "cap_exhausted")
		return
	}
	f.pendingConnections++
	f.state.ActiveConnections = len(f.connections) + f.pendingConnections
	f.mu.Unlock()
	pending := true
	defer func() {
		if pending {
			f.mu.Lock()
			f.pendingConnections--
			f.state.ActiveConnections = len(f.connections) + f.pendingConnections
			f.mu.Unlock()
		}
	}()
	if origin != f.cfg.TargetOrigin && origin != f.cfg.CDNOrigin {
		f.deny(w, "out_of_scope_origin")
		return
	}
	if origin == f.cfg.CDNOrigin {
		f.mu.Lock()
		local := f.commMode == "full_local"
		f.mu.Unlock()
		if local || ingress != "browser" {
			reason := "external_direct"
			if local {
				reason = "external_blocked_local"
			}
			f.deny(w, reason)
			return
		}
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unavailable", 500)
		return
	}
	conn, buf, e := hj.Hijack()
	if e != nil {
		return
	}
	f.mu.Lock()
	if !f.state.Active {
		f.mu.Unlock()
		_ = conn.Close()
		return
	}
	f.pendingConnections--
	pending = false
	f.connections[conn] = true
	f.state.ActiveConnections = len(f.connections) + f.pendingConnections
	f.mu.Unlock()
	buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if buf.Flush() != nil {
		_ = conn.Close()
	}
	defer func() {
		f.mu.Lock()
		delete(f.connections, conn)
		f.state.ActiveConnections = len(f.connections) + f.pendingConnections
		f.mu.Unlock()
	}()
	tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	if tc.HandshakeContext(r.Context()) != nil {
		tc.Close()
		return
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, inner *http.Request) {
		if inner.Host != r.Host {
			f.deny(w, "origin_invalid")
			return
		}
		inner.URL.Scheme = "https"
		inner.URL.Host = inner.Host
		f.Proxy(ingress).ServeHTTP(w, inner)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Second}
	tracked := &trackedConn{Conn: tc, done: make(chan struct{})}
	server.Serve(&oneListener{conn: tracked})
	tracked.Close()
}

type oneListener struct {
	conn *trackedConn
	mu   sync.Mutex
	sent bool
}

func (l *oneListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sent {
		<-l.conn.done
		return nil, io.EOF
	}
	l.sent = true
	return l.conn, nil
}
func (l *oneListener) Close() error   { return nil }
func (l *oneListener) Addr() net.Addr { return l.conn.LocalAddr() }
func Fingerprint(raw []byte) string   { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }

type trackedConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}
