package explorefixture

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

func fixtureCert(t *testing.T) (string, string, string, tls.Certificate, *x509.CertPool) {
	t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "owned contract fixture only"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"owned.test", "cdn.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, leaf, ca, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	priv, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	caFile, certFile, keyFile := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "leaf.pem"), filepath.Join(dir, "leaf.key")
	for name, body := range map[string][]byte{caFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), certFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyFile: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})} {
		if os.WriteFile(name, body, 0600) != nil {
			t.Fatal("fixture cert write")
		}
	}
	cert, e := tls.LoadX509KeyPair(certFile, keyFile)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	return caFile, certFile, keyFile, cert, pool
}
func TestOwnedFixtureScopeModesTLSAndPrivateMetadata(t *testing.T) {
	ca, certFile, keyFile, cert, pool := fixtureCert(t)
	var targetHits, cdnHits atomic.Int64
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	target.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	target.StartTLS()
	defer target.Close()
	cdn := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cdnHits.Add(1); io.WriteString(w, "owned fixture") }))
	cdn.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	cdn.StartTLS()
	defer cdn.Close()
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())
	_, cdnPort, _ := net.SplitHostPort(cdn.Listener.Addr().String())
	cfg := Config{Format: Version, OwnedFixture: true, TargetOrigin: "https://owned.test:" + targetPort, TargetAddress: target.Listener.Addr().String(), CDNOrigin: "https://cdn.test:" + cdnPort, CDNAddress: cdn.Listener.Addr().String(), TargetPrefixes: []string{"/read"}, MaxRequests: 20, MaxExternalRequests: 20, MaxConcurrency: 8, MaxResponseBytes: 4096, DurationSeconds: 30, CACertFile: ca, LeafCertFile: certFile, LeafKeyFile: keyFile}
	f, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	control := httptest.NewServer(f.Control())
	defer control.Close()
	binding := Binding{SessionRef: "fixture-session", RunRef: "fixture-run", JobID: "fixture-job", TaskID: "1", TargetRef: "owned-target", TargetRevision: 1, SourceDigest: strings.Repeat("a", 64)}
	body, _ := json.Marshal(map[string]any{"binding": binding, "limits": exploration.Limits{TimeBudgetSeconds: int64(f.cfg.DurationSeconds), MaxRequests: f.cfg.MaxRequests, MaxExternalRequests: f.cfg.MaxExternalRequests, MaxConcurrency: f.cfg.MaxConcurrency, MaxResponseBytes: f.cfg.MaxResponseBytes}, "comm_mode": "external_allowed", "write_mode": "read_only"})
	resp, e := http.Post(control.URL+"/open", "application/json", bytes.NewReader(body))
	if e != nil || resp.StatusCode != 200 {
		t.Fatal("fixture open")
	}
	resp.Body.Close()
	browser := httptest.NewServer(f.Proxy("browser"))
	defer browser.Close()
	direct := httptest.NewServer(f.Proxy("direct"))
	defer direct.Close()
	client := func(proxy string) *http.Client {
		u, _ := url.Parse(proxy)
		tr := &http.Transport{Proxy: http.ProxyURL(u), TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
		t.Cleanup(tr.CloseIdleConnections)
		return &http.Client{Transport: tr, Timeout: 5 * time.Second}
	}
	b, d := client(browser.URL), client(direct.URL)
	request := func(c *http.Client, method, raw, mode string) int {
		t.Helper()
		r, _ := http.NewRequestWithContext(t.Context(), method, raw, nil)
		r.Header.Set("Sec-Fetch-Mode", mode)
		got, e := c.Do(r)
		if e != nil {
			return 0
		}
		io.Copy(io.Discard, got.Body)
		got.Body.Close()
		return got.StatusCode
	}
	if request(b, "GET", cfg.TargetOrigin+"/read/item?name=value", "navigate") != 200 {
		t.Fatal("actual verified TLS forwarding failed")
	}
	if request(b, "POST", cfg.TargetOrigin+"/read/item", "cors") != 403 || f.Snapshot().Denied["method_not_approved"] != 1 {
		t.Fatal("write guard absent")
	}
	if request(b, "GET", cfg.TargetOrigin+"/outside", "navigate") != 403 || f.Snapshot().Denied["out_of_scope_path"] != 1 {
		t.Fatal("path guard absent")
	}
	if request(b, "GET", cfg.CDNOrigin+"/asset", "cors") != 200 || cdnHits.Load() != 1 {
		t.Fatal("external allowed resource missing")
	}
	if request(b, "GET", cfg.CDNOrigin+"/navigate", "navigate") != 403 || f.Snapshot().Denied["external_navigation"] != 1 {
		t.Fatal("external navigation guard absent")
	}
	if request(d, "GET", cfg.CDNOrigin+"/spoof", "cors") != 0 || f.Snapshot().Denied["external_direct"] != 1 {
		t.Fatal("forged fetch headers crossed direct ingress")
	}
	f.mu.Lock()
	o := f.observations[1]
	f.mu.Unlock()
	if o.Binding != binding || o.Path != "/read/item" || len(o.QueryKeys) != 1 || o.QueryKeys[0] != "name" || o.ResponseSHA256 == ([32]byte{}) {
		t.Fatal("private request metadata lost")
	}
	raw, _ := json.Marshal(o)
	if bytes.Contains(raw, []byte("value")) {
		t.Fatal("query value leaked")
	}
	if targetHits.Load() != 1 || cdnHits.Load() != 1 {
		t.Fatal("denied request reached fixture")
	}
	resp, e = http.Post(control.URL+"/close", "application/json", strings.NewReader(`{"reason":"completed"}`))
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if code := request(b, "GET", cfg.TargetOrigin+"/read/after", "navigate"); (code != 0 && code != 403) || targetHits.Load() != 1 || f.Snapshot().Active {
		t.Fatal("closed session forwarded")
	}
}
func TestFixtureRejectsProductionDestinationsAndInvalidLimits(t *testing.T) {
	c := Config{Format: Version, OwnedFixture: false}
	if _, e := New(c); e != ErrFixture {
		t.Fatal("fixture opt-in absent")
	}
}

func validGuardConfig(t *testing.T) Config {
	t.Helper()
	ca, cert, key, _, _ := fixtureCert(t)
	return Config{Format: Version, OwnedFixture: true, TargetOrigin: "https://owned.test:8443", TargetAddress: "127.0.0.1:8443", CDNOrigin: "https://cdn.test:9443", CDNAddress: "127.0.0.1:9443", TargetPrefixes: []string{"/read"}, MaxRequests: 4, MaxExternalRequests: 20, MaxConcurrency: 1, MaxResponseBytes: 1024, DurationSeconds: 30, CACertFile: ca, LeafCertFile: cert, LeafKeyFile: key}
}
func openGuardFixture(t *testing.T, cfg Config) *Fixture {
	t.Helper()
	f, err := New(cfg)
	if err != nil {
		t.Fatal("valid guard fixture rejected")
	}
	binding := Binding{SessionRef: "guard-session", RunRef: "guard-run", JobID: "guard-job", TaskID: "1", TargetRef: "guard-target", TargetRevision: 1, SourceDigest: strings.Repeat("a", 64)}
	body, _ := json.Marshal(map[string]any{"binding": binding, "limits": exploration.Limits{TimeBudgetSeconds: int64(f.cfg.DurationSeconds), MaxRequests: f.cfg.MaxRequests, MaxExternalRequests: f.cfg.MaxExternalRequests, MaxConcurrency: f.cfg.MaxConcurrency, MaxResponseBytes: f.cfg.MaxResponseBytes}, "comm_mode": "external_allowed", "write_mode": "read_only"})
	out := httptest.NewRecorder()
	f.Control().ServeHTTP(out, httptest.NewRequest("POST", "http://fixture/open", bytes.NewReader(body)))
	if out.Code != http.StatusOK || !f.Snapshot().Active {
		t.Fatal("guard fixture open failed")
	}
	t.Cleanup(func() { f.Close("completed") })
	return f
}

// 응답 전체 수집이 실패해도 실제 upstream 시도는 회차 예산에서 빠져야 한다.
func TestFixtureFailedResponseStillConsumesAttemptBudget(t *testing.T) {
	for _, failure := range []string{"oversize_response", "untrusted_upstream_tls"} {
		t.Run(failure, func(t *testing.T) {
			cfg := validGuardConfig(t)
			cert, err := tls.LoadX509KeyPair(cfg.LeafCertFile, cfg.LeafKeyFile)
			if err != nil {
				t.Fatal("valid upstream certificate unavailable")
			}
			if failure == "untrusted_upstream_tls" {
				_, _, _, cert, _ = fixtureCert(t)
			}
			var hits atomic.Int64
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				_, _ = io.WriteString(w, strings.Repeat("x", 2048))
			}))
			target.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
			target.Config.ErrorLog = log.New(io.Discard, "", 0)
			target.StartTLS()
			defer target.Close()
			_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
			cfg.TargetOrigin, cfg.TargetAddress, cfg.MaxRequests = "https://owned.test:"+port, target.Listener.Addr().String(), 1
			f := openGuardFixture(t, cfg)
			first := httptest.NewRecorder()
			f.Proxy("direct").ServeHTTP(first, httptest.NewRequest("GET", cfg.TargetOrigin+"/read/large", nil))
			if first.Code != http.StatusBadGateway || f.Snapshot().Attempts != 1 || f.Snapshot().Forwarded != 0 || f.Snapshot().RecordCount != 0 {
				t.Fatal("failed upstream response did not consume exactly one attempt")
			}
			second := httptest.NewRecorder()
			f.Proxy("direct").ServeHTTP(second, httptest.NewRequest("GET", cfg.TargetOrigin+"/read/again", nil))
			state := f.Snapshot()
			if second.Code != http.StatusForbidden || state.Attempts != 1 || state.Denied["cap_exhausted"] != 1 || state.Active {
				t.Fatal("failed attempts bypassed request cap")
			}
			wantHits := int64(0)
			if failure == "oversize_response" {
				wantHits = 1
			}
			if hits.Load() != wantHits {
				t.Fatal("denied second request reached upstream or TLS verification was bypassed")
			}
		})
	}
}

// Hijack 직전의 대기를 고정해 pending 연결도 동시성 상한에 포함되는지 검사한다.
type gatedFixtureHijacker struct {
	*httptest.ResponseRecorder
	entered        chan struct{}
	release        chan struct{}
	once           sync.Once
	server, client net.Conn
}

func newGatedFixtureHijacker(t *testing.T) *gatedFixtureHijacker {
	t.Helper()
	server, client := net.Pipe()
	w := &gatedFixtureHijacker{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{}), server: server, client: client}
	t.Cleanup(func() { w.unblock(); _ = client.Close(); _ = server.Close() })
	return w
}
func (w *gatedFixtureHijacker) unblock() { w.once.Do(func() { close(w.release) }) }
func (w *gatedFixtureHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	close(w.entered)
	<-w.release
	return w.server, bufio.NewReadWriter(bufio.NewReader(w.server), bufio.NewWriter(w.server)), nil
}
func waitFixtureSignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}
func connectGuardRequest(cfg Config) *http.Request {
	return httptest.NewRequest("CONNECT", strings.TrimPrefix(cfg.TargetOrigin, "https://"), nil)
}
func TestFixturePendingCONNECTReservesConcurrency(t *testing.T) {
	cfg := validGuardConfig(t)
	f := openGuardFixture(t, cfg)
	w := newGatedFixtureHijacker(t)
	done := make(chan struct{})
	go func() { defer close(done); f.Proxy("browser").ServeHTTP(w, connectGuardRequest(cfg)) }()
	waitFixtureSignal(t, w.entered, "CONNECT did not enter deterministic hijack gate")
	if f.Snapshot().ActiveConnections != 1 {
		t.Fatal("pending CONNECT was absent or counted twice")
	}
	second := httptest.NewRecorder()
	f.Proxy("browser").ServeHTTP(second, connectGuardRequest(cfg))
	if second.Code != http.StatusForbidden || f.Snapshot().Denied["cap_exhausted"] != 1 || f.Snapshot().ActiveConnections != 1 {
		t.Fatal("simultaneous CONNECT crossed concurrency cap")
	}
	// 실제 CONNECT 응답을 읽은 뒤 TLS를 시작하지 않고 닫아 정리 경로도 확인한다.
	w.unblock()
	_ = w.client.SetReadDeadline(time.Now().Add(time.Second))
	reader := bufio.NewReader(w.client)
	status, err := reader.ReadString('\n')
	if err != nil || status != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatal("first admitted CONNECT response unavailable")
	}
	if line, err := reader.ReadString('\n'); err != nil || line != "\r\n" {
		t.Fatal("CONNECT header terminator unavailable")
	}
	_ = w.client.Close()
	waitFixtureSignal(t, done, "CONNECT did not exit after owned client close")
	if f.Snapshot().ActiveConnections != 0 {
		t.Fatal("pending CONNECT reservation was not released")
	}
}
func TestFixtureCloseDuringPendingCONNECTCannotRegisterNewConnection(t *testing.T) {
	cfg := validGuardConfig(t)
	f := openGuardFixture(t, cfg)
	w := newGatedFixtureHijacker(t)
	done := make(chan struct{})
	go func() { defer close(done); f.Proxy("browser").ServeHTTP(w, connectGuardRequest(cfg)) }()
	waitFixtureSignal(t, w.entered, "CONNECT did not enter deterministic close race gate")
	closed := make(chan struct{})
	go func() { defer close(closed); f.Close("cancelled") }()
	deadline := time.Now().Add(time.Second)
	for f.Snapshot().Active && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.Snapshot().Active {
		t.Fatal("fixture close did not revoke admission")
	}
	w.unblock()
	waitFixtureSignal(t, done, "pending CONNECT survived close")
	waitFixtureSignal(t, closed, "close did not drain pending CONNECT")
	f.mu.Lock()
	registered, pending := len(f.connections), f.pendingConnections
	f.mu.Unlock()
	if registered != 0 || pending != 0 || f.Snapshot().ActiveConnections != 0 {
		t.Fatal("closed fixture acquired a late connection")
	}
	_ = w.client.SetReadDeadline(time.Now().Add(time.Second))
	var probe [1]byte
	if n, err := w.client.Read(probe[:]); n != 0 || err == nil {
		t.Fatal("closed pending connection remained open")
	}
}

// 먼저 유효 구성을 확인한 뒤 하나씩 바꿔서 다른 결함 때문에 통과하는 거부 시험을 피한다.
func TestFixtureRejectsIndependentConfigViolations(t *testing.T) {
	cfg := validGuardConfig(t)
	valid, err := New(cfg)
	if err != nil {
		t.Fatal("baseline fixture config invalid")
	}
	valid.Close("completed")
	badPEM := filepath.Join(t.TempDir(), "invalid.pem")
	if os.WriteFile(badPEM, []byte("not a certificate"), 0600) != nil {
		t.Fatal("invalid certificate fixture write failed")
	}
	cases := map[string]func(*Config){
		"production_target":       func(c *Config) { c.TargetOrigin = "https://owned.example:8443" },
		"production_cdn":          func(c *Config) { c.CDNOrigin = "https://cdn.example:9443" },
		"public_target_ip":        func(c *Config) { c.TargetAddress = "203.0.113.1:8443" },
		"public_cdn_ip":           func(c *Config) { c.CDNAddress = "203.0.113.2:9443" },
		"metadata_ip":             func(c *Config) { c.TargetAddress = "169.254.169.254:80" },
		"unresolved_address":      func(c *Config) { c.TargetAddress = "localhost:8443" },
		"http_origin":             func(c *Config) { c.TargetOrigin = "http://owned.test:8443" },
		"origin_userinfo":         func(c *Config) { c.TargetOrigin = "https://user:fixture@owned.test:8443" },
		"origin_path":             func(c *Config) { c.TargetOrigin += "/read" },
		"origin_query":            func(c *Config) { c.TargetOrigin += "?field=fixture" },
		"origin_fragment":         func(c *Config) { c.TargetOrigin += "#fixture" },
		"missing_tls_key":         func(c *Config) { c.LeafKeyFile = badPEM + ".missing" },
		"invalid_tls_certificate": func(c *Config) { c.LeafCertFile = badPEM },
		"invalid_tls_ca":          func(c *Config) { c.CACertFile = badPEM },
		"fixture_opt_in":          func(c *Config) { c.OwnedFixture = false },
		"zero_requests":           func(c *Config) { c.MaxRequests = 0 },
		"excessive_requests":      func(c *Config) { c.MaxRequests = 2001 },
		"zero_concurrency":        func(c *Config) { c.MaxConcurrency = 0 },
		"excessive_concurrency":   func(c *Config) { c.MaxConcurrency = 65 },
		"response_too_small":      func(c *Config) { c.MaxResponseBytes = 1023 },
		"response_too_large":      func(c *Config) { c.MaxResponseBytes = (2 << 20) + 1 },
		"zero_duration":           func(c *Config) { c.DurationSeconds = 0 },
		"excessive_duration":      func(c *Config) { c.DurationSeconds = 601 },
		"missing_scope":           func(c *Config) { c.TargetPrefixes = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := cfg
			change(&candidate)
			if _, err := New(candidate); err != ErrFixture {
				t.Fatal("independent invalid fixture config accepted")
			}
		})
	}
}

func TestFixtureOpenRequiresExactHostLimits(t *testing.T) {
	cfg := validGuardConfig(t)
	expected := exploration.Limits{TimeBudgetSeconds: int64(cfg.DurationSeconds), MaxRequests: cfg.MaxRequests, MaxExternalRequests: cfg.MaxExternalRequests, MaxConcurrency: cfg.MaxConcurrency, MaxResponseBytes: cfg.MaxResponseBytes}
	for name, change := range map[string]func(*exploration.Limits){"time": func(l *exploration.Limits) { l.TimeBudgetSeconds++ }, "target": func(l *exploration.Limits) { l.MaxRequests++ }, "external": func(l *exploration.Limits) { l.MaxExternalRequests++ }, "concurrency": func(l *exploration.Limits) { l.MaxConcurrency++ }, "response": func(l *exploration.Limits) { l.MaxResponseBytes++ }} {
		t.Run(name, func(t *testing.T) {
			f, e := New(cfg)
			if e != nil {
				t.Fatal("valid fixture")
			}
			limits := expected
			change(&limits)
			binding := Binding{SessionRef: "s", RunRef: "r", JobID: "j", TaskID: "1", TargetRef: "t", TargetRevision: 1, SourceDigest: strings.Repeat("a", 64)}
			raw, _ := json.Marshal(map[string]any{"binding": binding, "limits": limits, "comm_mode": "external_allowed", "write_mode": "read_only"})
			w := httptest.NewRecorder()
			f.Control().ServeHTTP(w, httptest.NewRequest("POST", "http://fixture/open", bytes.NewReader(raw)))
			if w.Code != 400 || f.Snapshot().Active || f.Snapshot().Binding.SessionRef != "" {
				t.Fatal("changed host limits issued a fixture session")
			}
		})
	}
}
func TestFixtureTargetAndExternalBudgetsAreSeparate(t *testing.T) {
	for _, exhausted := range []string{"target", "external"} {
		t.Run(exhausted, func(t *testing.T) {
			ca, certFile, keyFile, cert, pool := fixtureCert(t)
			var hits atomic.Int64
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); io.WriteString(w, "owned") }))
			target.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
			target.StartTLS()
			defer target.Close()
			_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
			cfg := Config{Format: Version, OwnedFixture: true, TargetOrigin: "https://owned.test:" + port, TargetAddress: target.Listener.Addr().String(), CDNOrigin: "https://cdn.test:" + port, CDNAddress: target.Listener.Addr().String(), TargetPrefixes: []string{"/read"}, MaxRequests: 1, MaxExternalRequests: 1, MaxConcurrency: 1, MaxResponseBytes: 1024, DurationSeconds: 30, CACertFile: ca, LeafCertFile: certFile, LeafKeyFile: keyFile}
			f := openGuardFixture(t, cfg)
			proxy := httptest.NewServer(f.Proxy("browser"))
			defer proxy.Close()
			u, _ := url.Parse(proxy.URL)
			tr := &http.Transport{Proxy: http.ProxyURL(u), TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
			defer tr.CloseIdleConnections()
			c := &http.Client{Transport: tr, Timeout: 3 * time.Second}
			send := func(raw string) int {
				r, e := c.Get(raw)
				if e != nil {
					return 0
				}
				defer r.Body.Close()
				io.Copy(io.Discard, r.Body)
				return r.StatusCode
			}
			// 접속 상한과 요청 상한을 따로 보려고 각 요청의 CONNECT를 정리한다.
			if send(cfg.TargetOrigin+"/read/one") != 200 {
				t.Fatal("target budget rejected first request")
			}
			tr.CloseIdleConnections()
			until := time.Now().Add(time.Second)
			for f.Snapshot().ActiveConnections != 0 && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			if send(cfg.CDNOrigin+"/asset") != 200 {
				t.Fatal("target request incorrectly consumed external budget")
			}
			tr.CloseIdleConnections()
			until = time.Now().Add(time.Second)
			for f.Snapshot().ActiveConnections != 0 && time.Now().Before(until) {
				time.Sleep(time.Millisecond)
			}
			next := cfg.TargetOrigin + "/read/two"
			if exhausted == "external" {
				next = cfg.CDNOrigin + "/asset-two"
			}
			if code := send(next); code != 403 && code != 0 {
				t.Fatal("exhausted budget forwarded")
			}
			state := f.Snapshot()
			if hits.Load() != 2 || state.Attempts != 2 || state.TargetAttempts != 1 || state.ExternalAttempts != 1 || state.Denied["cap_exhausted"] != 1 {
				t.Fatal("budget separation or charging failed")
			}
		})
	}
}
