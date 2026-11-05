package e2proxy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"

	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

func proxyTestCA(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1),
		Subject: pkix.Name{CommonName: "E2 test root"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

func proxyTestConfig(t *testing.T) (ProxyConfig, *x509.CertPool) {
	ca, roots := proxyTestCA(t)
	return ProxyConfig{CA: ca, HeaderTimeout: time.Second, RequestTimeout: 3 * time.Second,
		MaxHeaderBytes: 8192, MaxConnections: 8}, roots
}

func TestProxyRejectsUntrustedCAConfiguration(t *testing.T) {
	cfg, _ := proxyTestConfig(t)
	for _, name := range []string{"missing", "not_ca", "wrong_key", "nil_key", "expired", "zero_limit"} {
		t.Run(name, func(t *testing.T) {
			copy := cfg
			switch name {
			case "missing":
				copy.CA = tls.Certificate{}
			case "nil_key":
				copy.CA.PrivateKey = (*ecdsa.PrivateKey)(nil)
			case "wrong_key":
				key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				copy.CA.PrivateKey = key
			case "zero_limit":
				copy.MaxConnections = 0
			default:
				cert, err := x509.ParseCertificate(cfg.CA.Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				if name == "not_ca" {
					cert.IsCA = false
				} else {
					cert.NotAfter = time.Now().Add(-time.Minute)
				}
				key := cfg.CA.PrivateKey.(*ecdsa.PrivateKey)
				der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
				if err != nil {
					t.Fatal(err)
				}
				copy.CA.Certificate = [][]byte{der}
			}
			if _, err := NewProxy(&Call{}, copy); err == nil {
				t.Fatal("invalid CA or bound accepted")
			}
		})
	}
}

func TestProxyLeafContainsOnlyBoundHost(t *testing.T) {
	cfg, roots := proxyTestConfig(t)
	_, call, _ := proxyTestCall(t, "https://target.invalid", nil)
	p, err := NewProxy(call, cfg)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := p.leaf("target.invalid")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "target.invalid"}); err != nil {
		t.Fatal("bound leaf does not verify")
	}
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "other.invalid"}); err == nil {
		t.Fatal("leaf widened its authority")
	}
	if bytes.Equal(cert.RawSubjectPublicKeyInfo, p.ca.RawSubjectPublicKeyInfo) {
		t.Fatal("CA key was reused for live TLS")
	}
}

func TestProxyHeaderLimitAndBodyBoundary(t *testing.T) {
	cases := []struct {
		name, raw string
		limit     int
		wantError bool
	}{
		{"oversize", "GET http://target.invalid/a HTTP/1.1\r\nHost: target.invalid\r\nX: " + strings.Repeat("a", 256) + "\r\n\r\n", 64, true},
		{"duplicate_host", "GET http://target.invalid/a HTTP/1.1\r\nHost: target.invalid\r\nHost: other.invalid\r\n\r\n", 8192, true},
		{"body", "POST http://target.invalid/a HTTP/1.1\r\nHost: target.invalid\r\nContent-Length: 4\r\n\r\nBODY", 8192, false},
		// A buffer-sized header ending in a separate CRLF chunk is still one
		// header line, rather than an early end-of-headers marker.
		{"long_line", "GET http://target.invalid/a HTTP/1.1\r\nHost: target.invalid\r\nX: " + strings.Repeat("a", 5000) + "\r\n\r\n", 8192, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request, err := readProxyRequest(bufio.NewReaderSize(strings.NewReader(tc.raw), 32), tc.limit)
			if (err != nil) != tc.wantError {
				t.Fatal("unexpected header acceptance")
			}
			if request != nil {
				request.Body.Close()
			}
		})
	}
}

func TestProxyCredentialsAndUntrustedCorrelationAreRemoved(t *testing.T) {
	header := http.Header{
		"Proxy-Authorization": {"secret-proxy"},
		"Proxy-Connection":    {"keep-alive"},
		"X-E2-Attempt-Id":     {"forged"},
		"X-E2-Other":          {"untrusted"},
		"Connection":          {"X-Private, keep-alive"},
		"X-Private":           {"private"},
		"Accept":              {"text/plain"},
	}
	header.Set(AttemptHeader, "forged-current-wire")
	stripProxyHeaders(header)
	if len(header) != 1 || header.Get("Accept") != "text/plain" {
		t.Fatal("proxy or nominated header survived")
	}
}

type proxyTestRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *proxyTestRecorder) Record(_ context.Context, event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}
func proxyTestCall(t *testing.T, target string, roots *x509.CertPool) (*Execution, *Call, *proxyTestRecorder) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 100})
	if err != nil {
		t.Fatal(err)
	}
	key := budget.Key{TaskRef: "task_proxy", BudgetRef: "budget_proxy"}
	binding := budget.Binding{PolicyRef: "policy_proxy", ResetRef: "reset_proxy"}
	if err = ledger.Register(budget.Grant{Key: key, Binding: binding, MaxRequests: 100, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}, now); err != nil {
		t.Fatal(err)
	}
	rec := &proxyTestRecorder{}
	exec, err := NewExecution(t.Context(), Config{
		Scope:    transmit.Scope{TaskRef: key.TaskRef, RunRef: "run_proxy", CaseRef: "case_proxy", ResetRef: binding.ResetRef, PolicyRef: binding.PolicyRef},
		IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "revision_proxy", AccountRef: "account_proxy", SessionRef: "session_proxy",
		ExpiresAt: now.Add(time.Minute), RootCAs: roots, Recorder: rec,
		Rules: []Rule{{Ref: "rule_proxy", Origin: u.Scheme + "://" + u.Host, ConnectIP: netip.MustParseAddr("127.0.0.1"),
			PathPrefix: "/allowed", PathScopeRef: "path_proxy", Methods: []string{"GET", "POST"},
			Headers: http.Header{"Authorization": {"Bearer session-secret"}}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: key, Binding: binding},
		Limits: Limits{MaxAttempts: 100, MaxCalls: 8, MaxConcurrent: 8, MaxBodyBytes: 8192,
			RequestTimeout: 2 * time.Second, CallTimeout: 30 * time.Second, MaxDenials: 20, MaxTransient: 20, MaxUnknown: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	call, err := exec.BeginCall(t.Context(), "command_proxy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := call.Close(ctx); err != nil {
			t.Error("call did not drain")
		}
		if err := exec.Stop(ctx); err != nil {
			t.Error("execution stop audit failed")
		}
	})
	return exec, call, rec
}
func proxyTestListener(t *testing.T, call *Call) (*Proxy, string, *x509.CertPool) {
	t.Helper()
	cfg, roots := proxyTestConfig(t)
	p, err := NewProxy(call, cfg)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- p.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := p.Close(ctx); err != nil {
			t.Error("proxy did not drain")
		}
		if err := <-served; err != nil {
			t.Error("proxy accept failed")
		}
	})
	return p, listener.Addr().String(), roots
}
func proxyTestClient(t *testing.T, addr string, roots *x509.CertPool) *http.Client {
	t.Helper()
	proxyURL, _ := url.Parse("http://" + addr)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true, DisableCompression: true,
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		Protocols:       protocols}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 4 * time.Second}
}

func TestProxyRealHTTPAndHTTPSForwarding(t *testing.T) {
	for _, secured := range []bool{false, true} {
		t.Run(fmt.Sprint(secured), func(t *testing.T) {
			var received atomic.Int64
			var receivedID string
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-E2-Other") != "" || r.Header.Get("X-Private") != "" {
					t.Error("untrusted proxy header reached target")
				}
				if r.Header.Get("Authorization") != "Bearer session-secret" {
					t.Error("trusted session was not bound")
				}
				receivedID = r.Header.Get(AttemptHeader)
				if !safeAttempt.MatchString(receivedID) || receivedID == "forged" {
					t.Error("proxy did not assign a fresh attempt")
				}
				w.Header().Set("Set-Cookie", "session-secret")
				io.WriteString(w, "response Bearer session-secret")
			}))
			var targetRoots *x509.CertPool
			if secured {
				target.StartTLS()
				targetRoots = x509.NewCertPool()
				targetRoots.AddCert(target.Certificate())
			} else {
				target.Start()
			}
			defer target.Close()
			exec, call, _ := proxyTestCall(t, target.URL, targetRoots)
			_, addr, proxyRoots := proxyTestListener(t, call)
			client := proxyTestClient(t, addr, proxyRoots)
			request, _ := http.NewRequest("GET", target.URL+"/allowed", nil)
			request.Header.Set("Proxy-Authorization", "secret-proxy")
			request.Header.Set(AttemptHeader, "forged")
			request.Header.Set("X-E2-Other", "untrusted")
			request.Header.Set("Connection", "X-Private")
			request.Header.Set("X-Private", "untrusted")
			resp, err := client.Do(request)
			if err != nil {
				t.Fatal("allowed request failed:", err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 || !bytes.Contains(body, []byte("[REDACTED]")) || bytes.Contains(body, []byte("session-secret")) || resp.Header.Get("Set-Cookie") != "" {
				t.Fatal("response was not safely returned")
			}
			if received.Load() != 1 {
				t.Fatal("allowed request did not reach the target once")
			}
			events := exec.Events(0, 200)
			found := false
			for _, ev := range events {
				if ev.Stage == "result" && ev.AttemptID == receivedID {
					found = ev.Decision == "allowed" && ev.ResponseComplete && ev.ResponseReceived && ev.AuditComplete
				}
			}
			if !found {
				t.Fatal("trusted response receipt is missing")
			}
		})
	}
}

func TestProxyPolicyDenialIsNotTarget403(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(403) }))
	defer target.Close()
	exec, call, _ := proxyTestCall(t, target.URL, nil)
	_, addr, roots := proxyTestListener(t, call)
	resp, err := proxyTestClient(t, addr, roots).Get(target.URL + "/outside")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || strings.Contains(string(body), target.URL) || received.Load() != 0 {
		t.Fatal("policy refusal masqueraded as target response")
	}
	if exec.Snapshot().Counts.Denied != 1 {
		t.Fatal("policy refusal not recorded separately")
	}
}

func TestProxyDoesNotReusePipelinedConnection(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); io.WriteString(w, "ok") }))
	defer target.Close()
	_, call, _ := proxyTestCall(t, target.URL, nil)
	_, addr, _ := proxyTestListener(t, call)
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	u, _ := url.Parse(target.URL)
	command := "GET " + target.URL + "/allowed HTTP/1.1\r\nHost: " + u.Host + "\r\n\r\n"
	if _, err = io.WriteString(conn, command+command); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if !response.Close {
		t.Fatal("connection reuse was advertised")
	}
	if _, err = http.ReadResponse(reader, &http.Request{Method: "GET"}); err == nil {
		t.Fatal("pipelined request was accepted")
	}
	if received.Load() != 1 {
		t.Fatal("connection carried more than one target request")
	}
}

func proxyTestCONNECT(t *testing.T, addr, authority string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = io.WriteString(conn, "CONNECT "+authority+" HTTP/1.1\r\nHost: "+authority+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	first, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "HTTP/1.1 200 ") {
		t.Fatal("CONNECT TLS entry unavailable")
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, reader
}

func TestProxyCONNECTInspectionFailuresNeverTunnel(t *testing.T) {
	for _, name := range []string{"inner_host", "sni", "h2_only", "opaque", "nested_connect", "http12"} {
		t.Run(name, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
			defer target.Close()
			targetRoots := x509.NewCertPool()
			targetRoots.AddCert(target.Certificate())
			exec, call, _ := proxyTestCall(t, target.URL, targetRoots)
			_, addr, proxyRoots := proxyTestListener(t, call)
			targetURL, _ := url.Parse(target.URL)
			conn, reader := proxyTestCONNECT(t, addr, targetURL.Host)
			defer conn.Close()
			if name == "opaque" {
				io.WriteString(conn, "GET /allowed HTTP/1.1\r\nHost: "+targetURL.Host+"\r\n\r\n")
				io.Copy(io.Discard, conn)
			} else {
				cfg := &tls.Config{RootCAs: proxyRoots, ServerName: targetURL.Hostname(), MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
				if name == "sni" {
					cfg.ServerName = "other.invalid"
				}
				if name == "h2_only" {
					cfg.NextProtos = []string{"h2"}
				}
				secure := tls.Client(&proxyBufferedConn{Conn: conn, reader: reader}, cfg)
				err := secure.Handshake()
				if name == "sni" || name == "h2_only" {
					if err == nil {
						t.Fatal("unsupported TLS binding accepted")
					}
				} else {
					if err != nil {
						t.Fatal("TLS setup:", err)
					}
					request := "GET /allowed HTTP/1.1\r\nHost: other.invalid\r\n\r\n"
					if name == "http12" {
						request = "GET /allowed HTTP/1.2\r\nHost: " + targetURL.Host + "\r\n\r\n"
					}
					if name == "nested_connect" {
						request = "CONNECT " + targetURL.Host + " HTTP/1.1\r\nHost: " + targetURL.Host + "\r\n\r\n"
					}
					io.WriteString(secure, request)
					response, err := http.ReadResponse(bufio.NewReader(secure), &http.Request{Method: "GET"})
					if err != nil {
						t.Fatal("fixed rejection response missing")
					}
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode != 400 {
						t.Fatal("inspection failure not rejected")
					}
				}
				secure.Close()
			}
			if received.Load() != 0 {
				t.Fatal("failed inspection tunneled a request")
			}
			deadline := time.Now().Add(time.Second)
			for exec.Snapshot().Counts.Unsupported == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if exec.Snapshot().Counts.Unsupported == 0 {
				t.Fatal("unsupported attempt not recorded")
			}
		})
	}
}

func TestProxyUpstreamTLSVerificationIsPreserved(t *testing.T) {
	var received atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
	target.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer target.Close()
	exec, call, _ := proxyTestCall(t, target.URL, nil)
	_, addr, roots := proxyTestListener(t, call)
	resp, err := proxyTestClient(t, addr, roots).Get(target.URL + "/allowed")
	if err != nil {
		t.Fatal("proxy error should remain a fixed response:", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || received.Load() != 0 || strings.Contains(string(body), "certificate") {
		t.Fatal("untrusted upstream cert was accepted or leaked")
	}
	if exec.Snapshot().Counts.Responses != 0 {
		t.Fatal("TLS failure recorded as a target response")
	}
}

func TestProxyCloseDrainsIncompleteClientRequest(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("partial input was sent") }))
	defer target.Close()
	_, call, _ := proxyTestCall(t, target.URL, nil)
	proxy, addr, _ := proxyTestListener(t, call)
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET http://partial.invalid/")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := proxy.Close(ctx); err != nil {
		t.Fatal("partial client prevented proxy drain")
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("client connection remained open")
	}
}

func TestProxyCONNECTPolicyDenialPrecedesTLS(t *testing.T) {
	for _, name := range []string{"http_only_policy", "wrong_https_authority"} {
		t.Run(name, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
			var targetRoots *x509.CertPool
			if name == "wrong_https_authority" {
				target.StartTLS()
				targetRoots = x509.NewCertPool()
				targetRoots.AddCert(target.Certificate())
			} else {
				target.Start()
			}
			defer target.Close()
			execution, call, _ := proxyTestCall(t, target.URL, targetRoots)
			_, addr, _ := proxyTestListener(t, call)
			parsed, _ := url.Parse(target.URL)
			authority := parsed.Host
			if name == "wrong_https_authority" {
				authority = "other.invalid:443"
			}
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err = io.WriteString(conn, "CONNECT "+authority+" HTTP/1.1\r\nHost: "+authority+"\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
			if err != nil {
				t.Fatal("fixed pre-TLS policy response missing")
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 502 || received.Load() != 0 {
				t.Fatal("unapproved CONNECT received TLS entry or reached a target")
			}
			view := execution.Snapshot()
			if view.Counts.Denied != 1 || view.Counts.ForwardAttempted != 0 {
				t.Fatal("CONNECT denial was not a pre-send policy decision")
			}
		})
	}
}

func TestProxyIngressCapabilityRejectsCrossCallAndForgery(t *testing.T) {
	for _, name := range []string{"different_call", "forged", "revoked", "expired", "closed"} {
		t.Run(name, func(t *testing.T) {
			var received atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1) }))
			defer target.Close()
			execution, call, _ := proxyTestCall(t, target.URL, nil)
			if name == "expired" {
				execution.mu.Lock()
				execution.cfg.Limits.CallTimeout = 200 * time.Millisecond
				execution.mu.Unlock()
				shortCall, err := execution.BeginCall(t.Context(), "command_short")
				if err != nil {
					t.Fatal(err)
				}
				call = shortCall
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if _, err := shortCall.Close(ctx); err != nil {
						t.Error("short call audit did not close")
					}
				})
			}
			p, addr, roots := proxyTestListener(t, call)
			switch name {
			case "different_call":
				other, err := execution.BeginCall(t.Context(), "command_other")
				if err != nil {
					t.Fatal(err)
				}
				capability, err := other.newIngressCapability()
				if err != nil {
					t.Fatal(err)
				}
				p.mu.Lock()
				p.ingressCapability = capability
				p.mu.Unlock()
				clear(capability[:])
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if _, err := other.Close(ctx); err != nil {
						t.Error("other call audit did not close")
					}
				})
			case "forged":
				p.mu.Lock()
				p.ingressCapability[0] ^= 1
				p.mu.Unlock()
			case "revoked":
				if err := execution.Revoke(t.Context()); err != nil {
					t.Fatal("revocation audit failed")
				}
			case "expired":
				<-call.Context().Done()
			case "closed":
				call.Block()
			}
			response, err := proxyTestClient(t, addr, roots).Get(target.URL + "/allowed")
			if response != nil {
				response.Body.Close()
			}
			if err == nil || received.Load() != 0 || execution.Snapshot().Counts.Attempts != 0 {
				t.Fatal("invalid or stale capability admitted target work")
			}
		})
	}
}

func TestProxyIngressCapabilityNeverAppearsInRepresentations(t *testing.T) {
	_, call, _ := proxyTestCall(t, "http://target.invalid", nil)
	cfg, _ := proxyTestConfig(t)
	p, err := NewProxy(call, cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	secret := p.ingressCapability
	p.mu.Unlock()
	defer clear(secret[:])
	formatted := fmt.Sprintf("%v %#v %+v %#v %v %#v", p, p, *p, *p, cfg, cfg)
	if strings.Contains(formatted, hex.EncodeToString(secret[:])) || strings.Contains(formatted, "PrivateKey") ||
		strings.Contains(formatted, "ingressCapability") || !strings.Contains(formatted, "[protected]") {
		t.Fatal("private proxy state escaped object formatting")
	}
	encoded, err := json.Marshal(p)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("proxy serialization exposed private state")
	}
	encoded, err = json.Marshal(cfg)
	if err != nil || bytes.Contains(encoded, []byte("PrivateKey")) || bytes.Contains(encoded, []byte("Certificate")) {
		t.Fatal("proxy configuration serialized CA material")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = p.Close(ctx); err != nil {
		t.Fatal("proxy close did not drain")
	}
	p.mu.Lock()
	zero := p.ingressCapability == ([32]byte{})
	p.mu.Unlock()
	if !zero || p.validIngress() {
		t.Fatal("closed proxy retained its host capability")
	}
}

func TestProxyTargetObservesOriginalHostAndIdentity(t *testing.T) {
	for _, secured := range []bool{false, true} {
		t.Run(fmt.Sprint(secured), func(t *testing.T) {
			observed := make(chan string, 1)
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- r.Host + "|" + r.Header.Get("Accept-Encoding")
				io.WriteString(w, "preserved")
			}))
			scheme, name := "http", "EXAMPLE.test"
			var roots *x509.CertPool
			if secured {
				target.StartTLS()
				scheme = "https"
				roots = x509.NewCertPool()
				roots.AddCert(target.Certificate())
				if len(target.Certificate().DNSNames) == 0 {
					t.Fatal("TLS fixture lacks named authority")
				}
				name = strings.ToUpper(target.Certificate().DNSNames[0])
			} else {
				target.Start()
			}
			defer target.Close()
			_, port, err := net.SplitHostPort(target.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			origin := scheme + "://" + strings.ToLower(name) + ":" + port
			_, call, _ := proxyTestCall(t, origin, roots)
			_, addr, proxyRoots := proxyTestListener(t, call)
			req, err := http.NewRequest("GET", origin+"/allowed", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = name + ":" + port
			req.Header.Set("Accept-Encoding", "identity")
			response, err := proxyTestClient(t, addr, proxyRoots).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal("positive proxy delivery failed")
			}
			if got := <-observed; got != req.Host+"|identity" {
				t.Fatalf("proxy rewrote Host/negotiation: %q", got)
			}
		})
	}
}
