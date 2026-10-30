package transmit_test

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

type recorder struct {
	mu      sync.Mutex
	events  []transmit.Event
	failAt  string
	started chan struct{}
	release chan struct{}
}

func (r *recorder) Record(_ context.Context, event transmit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started != nil && event.Stage == "intent" {
		select {
		case <-r.started:
		default:
			close(r.started)
		}
	}
	if r.release != nil && event.Stage == "intent" {
		<-r.release
	}
	r.events = append(r.events, event)
	if r.failAt == event.Stage {
		return errors.New("sink failure")
	}
	return nil
}

func transportSession(label, handle string) transmit.Session {
	return transmit.Session{Account: idor.AccountRef{Label: label, Handle: handle, Status: oracle.SubjectVerified},
		ExpiresAt: time.Now().Add(time.Minute), Headers: http.Header{"X-Test-Session": []string{label}}}
}

func transportConfig(serverURL string, rec transmit.Recorder) transmit.Config {
	return transmit.Config{
		Scope:  transmit.Scope{RunRef: "run-1", TaskRef: "task-1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1"},
		Origin: serverURL, ConnectIP: netip.MustParseAddr("127.0.0.1"), ExpiresAt: time.Now().Add(time.Minute),
		Sessions:    []transmit.Session{transportSession("owner", "h-owner"), transportSession("evaluated", "h-evaluated")},
		Resources:   []transmit.Resource{{Ref: "basket/42", URL: serverURL + "/resource", ApprovalRef: "approval-1", ReadOnly: true, Accounts: []string{"owner", "evaluated"}}},
		MaxRequests: 4, RequestTimeout: time.Second, MaxBodyBytes: 1024, Recorder: rec,
	}
}

func newTransportServer(handler http.Handler) (*httptest.Server, *transmit.HTTPTransmitter, *recorder) {
	server := httptest.NewServer(handler)
	rec := &recorder{}
	t, err := transmit.NewHTTP(context.Background(), transportConfig(server.URL, rec))
	if err != nil {
		server.Close()
		panic(err)
	}
	return server, t, rec
}

func transportRequest(label, handle string) idor.AccessRequest {
	return idor.AccessRequest{As: idor.AccountRef{Label: label, Handle: handle, Status: oracle.SubjectVerified}, Method: http.MethodGet, ResourceRef: "basket/42"}
}

func TestHTTPTransmitterAllowedGetUsesRegisteredSession(t *testing.T) {
	var hits atomic.Int32
	server, tx, _ := newTransportServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.Header.Get("X-Test-Session"); got != "owner" {
			t.Errorf("session header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if !ok || receipt.Reason != transmit.Completed || !receipt.ResponseReceived {
		t.Fatalf("send = ok %v receipt %+v", ok, receipt)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("target hits = %d, want 1", got)
	}
}

func TestHTTPTransmitterRejectsUnsupportedAndUnregisteredRequests(t *testing.T) {
	var hits atomic.Int32
	server, tx, _ := newTransportServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	cases := []idor.AccessRequest{
		{As: idor.AccountRef{Label: "owner", Handle: "h-owner", Status: oracle.SubjectVerified}, Method: http.MethodPost, ResourceRef: "basket/42"},
		{As: idor.AccountRef{Label: "owner", Handle: "h-owner", Status: oracle.SubjectVerified}, Method: http.MethodGet, StateChanging: true, ResourceRef: "basket/42"},
		{As: idor.AccountRef{Label: "unknown", Handle: "h", Status: oracle.SubjectVerified}, Method: http.MethodGet, ResourceRef: "basket/42"},
		{As: idor.AccountRef{Label: "owner", Handle: "h-owner", Status: oracle.SubjectVerified}, Method: http.MethodGet, ResourceRef: "outside"},
	}
	for _, req := range cases {
		_, receipt, ok := tx.SendDetailed(req)
		if ok || receipt.Admitted {
			t.Fatalf("unexpected admission: %+v", receipt)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("blocked target hits = %d, want 0", got)
	}
}

func TestHTTPTransmitterRejectsSpoofedStatusAndHandle(t *testing.T) {
	var hits atomic.Int32
	server, tx, _ := newTransportServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	for _, status := range []oracle.SubjectStatus{"", oracle.SubjectExpired, oracle.SubjectVerified} {
		req := transportRequest("owner", "wrong")
		req.As.Status = status
		_, receipt, ok := tx.SendDetailed(req)
		if ok || receipt.Admitted {
			t.Fatalf("spoofed request admitted: %+v", receipt)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("spoofed target hits = %d", got)
	}
}

func TestHTTPTransmitterConstructorRejectsOutsideOrigin(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	rec := &recorder{}
	cfg := transportConfig(server.URL, rec)
	cfg.Resources[0].URL = "http://example.invalid/resource"
	if _, err := transmit.NewHTTP(context.Background(), cfg); err == nil {
		t.Fatal("outside-origin resource accepted")
	}
	cfg.Origin = server.URL + "/query"
	if _, err := transmit.NewHTTP(context.Background(), cfg); err == nil {
		t.Fatal("origin path accepted")
	}
}

func TestHTTPTransmitterIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	old := os.Getenv("HTTP_PROXY")
	proxyHits := atomic.Int32{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1); _, _ = w.Write([]byte("proxy")) }))
	defer proxy.Close()
	if err := os.Setenv("HTTP_PROXY", proxy.URL); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("HTTP_PROXY", old) }()
	var hits atomic.Int32
	server, tx, _ := newTransportServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	// Use a non-loopback logical hostname so a default transport's NO_PROXY
	// behavior cannot make this test pass accidentally; E² still dials 127.0.0.1.
	logical := strings.Replace(server.URL, "127.0.0.1", "target.test", 1)
	cfg := transportConfig(logical, &recorder{})
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if !ok || hits.Load() != 1 || proxyHits.Load() != 0 {
		t.Fatalf("proxy affected registered request: ok=%v target=%d proxy=%d", ok, hits.Load(), proxyHits.Load())
	}
}

func TestHTTPTransmitterBudgetAndStop(t *testing.T) {
	var hits atomic.Int32
	server, tx, rec := newTransportServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(10 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	// The helper's budget is four; concurrent callers must still serialize and never exceed it.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _ = tx.SendDetailed(transportRequest("owner", "h-owner")) }()
	}
	wg.Wait()
	if got := hits.Load(); got != 4 {
		t.Fatalf("target hits = %d, want exactly budget 4", got)
	}
	if len(rec.events) == 0 {
		t.Fatal("no audit events")
	}
	tx.Stop()
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || receipt.Admitted {
		t.Fatalf("send after stop admitted: %+v", receipt)
	}
}

func TestHTTPTransmitterAuditFailureFailsClosed(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	rec := &recorder{failAt: "intent"}
	tx, err := transmit.NewHTTP(context.Background(), transportConfig(server.URL, rec))
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || receipt.Admitted || hits.Load() != 0 {
		t.Fatalf("intent failure sent request: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
	}

	// A result-record failure occurs after the request may have been written; the
	// receipt is therefore false/unknown while the transmitter locks future sends.
	rec2 := &recorder{failAt: "result"}
	tx2, err := transmit.NewHTTP(context.Background(), transportConfig(server.URL, rec2))
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, ok = tx2.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || !receipt.Admitted || hits.Load() != 1 {
		t.Fatalf("result failure did not preserve possible send: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
	_, receipt, ok = tx2.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || receipt.Admitted || hits.Load() != 1 {
		t.Fatalf("result failure did not fail closed: ok=%v receipt=%+v hits=%d", ok, receipt, hits.Load())
	}
}

func TestHTTPTransmitterRedirectDoesNotForwardCredentials(t *testing.T) {
	var destinationHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationHits.Add(1)
		if got := r.Header.Get("X-Test-Session"); got != "" {
			t.Errorf("credential forwarded to redirect destination: %q", got)
		}
		_, _ = w.Write([]byte("redirected"))
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	rec := &recorder{}
	cfg := transportConfig(source.URL, rec)
	cfg.Resources[0].URL = source.URL + "/redirect"
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || receipt.Reason != transmit.RedirectRefused || destinationHits.Load() != 0 {
		t.Fatalf("redirect followed: ok=%v receipt=%+v destination=%d", ok, receipt, destinationHits.Load())
	}
}

func TestHTTPTransmitterDoesNotRetryClosedConnection(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("local listener unavailable: %v", err)
	}
	defer listener.Close()
	var accepts atomic.Int32
	done := make(chan struct{})
	stopAccept := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			accepts.Add(1)
			buf := make([]byte, 4096)
			_, _ = conn.Read(buf)
			_ = conn.Close()
			select {
			case <-stopAccept:
				return
			default:
			}
		}
	}()
	serverURL := "http://" + listener.Addr().String()
	rec := &recorder{}
	cfg := transportConfig(serverURL, rec)
	cfg.RequestTimeout = 300 * time.Millisecond
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	close(stopAccept)
	_ = listener.Close()
	<-done
	if ok || receipt.ResponseReceived || accepts.Load() != 1 {
		t.Fatalf("closed connection retried or succeeded: ok=%v receipt=%+v accepts=%d", ok, receipt, accepts.Load())
	}
}

func TestHTTPTransmitterBoundsBodyAndReadTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte("compressed"))
	}))
	defer server.Close()
	for _, tc := range []struct {
		path   string
		reason transmit.Reason
	}{{"/large", transmit.ResponseTooLarge}, {"/encoding", transmit.UnsupportedResponse}} {
		rec := &recorder{}
		cfg := transportConfig(server.URL, rec)
		cfg.Resources[0].URL = server.URL + tc.path
		cfg.MaxBodyBytes = 100
		tx, err := transmit.NewHTTP(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
		if ok || receipt.Reason != tc.reason {
			t.Fatalf("path %s: ok=%v receipt=%+v", tc.path, ok, receipt)
		}
	}
	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer blocking.Close()
	cfg := transportConfig(blocking.URL, &recorder{})
	cfg.RequestTimeout = 80 * time.Millisecond
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if ok || receipt.Reason != transmit.TimedOut {
		t.Fatalf("body timeout: ok=%v receipt=%+v", ok, receipt)
	}
}

func TestHTTPTransmitterCopiesMutableConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/resource" || r.Header.Get("X-Test-Session") != "owner" {
			t.Errorf("request changed after config mutation: path=%q header=%q", r.URL.Path, r.Header.Get("X-Test-Session"))
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	rec := &recorder{}
	cfg := transportConfig(server.URL, rec)
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sessions[0].Headers.Set("X-Test-Session", "mutated")
	cfg.Resources[0].URL = server.URL + "/other"
	cfg.Resources[0].Accounts[0] = "unknown"
	_, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner"))
	if !ok || receipt.Reason != transmit.Completed {
		t.Fatalf("copied config request failed: ok=%v receipt=%+v", ok, receipt)
	}
}

func TestHTTPTransmitterPinnedTLSHostAndHTTP1(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("protocol = %s, want HTTP/1", r.Proto)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	rec := &recorder{}
	port := strings.Split(server.URL, ":")[2]
	logical := "https://example.com:" + port
	cfg := transportConfig(logical, rec)
	cfg.RootCAs = pool
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner")); !ok || receipt.Reason != transmit.Completed {
		t.Fatalf("pinned TLS request failed: ok=%v receipt=%+v", ok, receipt)
	}
	// Preserve hostname verification while dialing the same pinned IP.
	hostMismatch := "https://wrong.invalid:" + port
	cfg = transportConfig(hostMismatch, &recorder{})
	cfg.RootCAs = pool
	tx, err = transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, receipt, ok := tx.SendDetailed(transportRequest("owner", "h-owner")); ok || receipt.ResponseReceived {
		t.Fatalf("TLS hostname mismatch accepted: ok=%v receipt=%+v", ok, receipt)
	}
}

func TestHTTPTransmitterRateLimitAndQueuedStop(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	cfg := transportConfig(server.URL, &recorder{})
	cfg.MinInterval = 80 * time.Millisecond
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, _, ok := tx.SendDetailed(transportRequest("owner", "h-owner")); !ok {
		t.Fatal("first rate-limited request failed")
	}
	if _, _, ok := tx.SendDetailed(transportRequest("owner", "h-owner")); !ok {
		t.Fatal("second rate-limited request failed")
	}
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Fatalf("rate interval too short: %s", elapsed)
	}

	cfg = transportConfig(server.URL, &recorder{})
	cfg.MinInterval = time.Second
	tx, err = transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := tx.SendDetailed(transportRequest("owner", "h-owner")); !ok {
		t.Fatal("queued-stop setup request failed")
	}
	result := make(chan transmit.Receipt, 1)
	go func() { _, receipt, _ := tx.SendDetailed(transportRequest("owner", "h-owner")); result <- receipt }()
	time.Sleep(20 * time.Millisecond)
	tx.Stop()
	receipt := <-result
	if receipt.Admitted || hits.Load() != 3 {
		t.Fatalf("rate-wait request admitted after stop: receipt=%+v hits=%d", receipt, hits.Load())
	}
}

func TestHTTPTransmitterConstructorLimitsAndApproval(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	base := transportConfig(server.URL, &recorder{})
	cases := []struct {
		name   string
		mutate func(*transmit.Config)
	}{
		{"zero budget", func(c *transmit.Config) { c.MaxRequests = 0 }},
		{"zero timeout", func(c *transmit.Config) { c.RequestTimeout = 0 }},
		{"zero body limit", func(c *transmit.Config) { c.MaxBodyBytes = 0 }},
		{"read only false", func(c *transmit.Config) { c.Resources[0].ReadOnly = false }},
		{"unapproved account", func(c *transmit.Config) { c.Resources[0].Accounts = []string{"missing"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Sessions = append([]transmit.Session(nil), base.Sessions...)
			cfg.Resources = append([]transmit.Resource(nil), base.Resources...)
			cfg.Resources[0].Accounts = append([]string(nil), base.Resources[0].Accounts...)
			tc.mutate(&cfg)
			if _, err := transmit.NewHTTP(context.Background(), cfg); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestHTTPTransmitterStopRechecksAfterAuditAndRateWait(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	blocked := &recorder{started: make(chan struct{}), release: make(chan struct{})}
	cfg := transportConfig(server.URL, blocked)
	cfg.MinInterval = time.Second
	tx, err := transmit.NewHTTP(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan transmit.Receipt, 1)
	go func() { _, receipt, _ := tx.SendDetailed(transportRequest("owner", "h-owner")); result <- receipt }()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("intent audit did not start")
	}
	tx.Stop()
	close(blocked.release)
	receipt := <-result
	if receipt.Admitted || hits.Load() != 0 {
		t.Fatalf("stopped audit admitted: receipt=%+v hits=%d", receipt, hits.Load())
	}
}

func TestHTTPTransmitterStopCancelsInFlight(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	tx, err := transmit.NewHTTP(context.Background(), transportConfig(server.URL, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan transmit.Receipt, 1)
	go func() { _, receipt, _ := tx.SendDetailed(transportRequest("owner", "h-owner")); done <- receipt }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	queued := make(chan transmit.Receipt, 1)
	go func() { _, receipt, _ := tx.SendDetailed(transportRequest("owner", "h-owner")); queued <- receipt }()
	tx.Stop()
	select {
	case receipt := <-done:
		if receipt.ResponseReceived {
			t.Fatalf("canceled request reported response: %+v", receipt)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight request was not canceled")
	}
	select {
	case receipt := <-queued:
		if receipt.Admitted {
			t.Fatalf("queued request admitted after Stop: %+v", receipt)
		}
	case <-time.After(time.Second):
		t.Fatal("queued request was not canceled")
	}
}
