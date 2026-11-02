package llmpolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func localHTTPPolicy(t *testing.T, originURL string) *Policy {
	t.Helper()
	p, err := New([]Endpoint{{Origin: originURL, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}})
	if err != nil {
		t.Fatal("fixture policy failed")
	}
	return p
}

func localHTTPOrigin(t *testing.T, server *httptest.Server) string {
	t.Helper()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal("fixture address failed")
	}
	return "http://model.internal:" + port
}

func localHTTPLookup(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func TestLocalHTTPApprovedRequestKeepsHost(t *testing.T) {
	var received atomic.Int64
	host := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		host <- r.Host
		if r.URL.RequestURI() != "/v1/models?cursor=fixture" {
			t.Error("request URI changed")
		}
		_, _ = io.WriteString(w, "fixture reply")
	}))
	defer srv.Close()
	originURL := localHTTPOrigin(t, srv)
	client, err := NewClient(localHTTPPolicy(t, originURL), ClientOptions{LookupIP: localHTTPLookup})
	if err != nil {
		t.Fatal("client creation failed")
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get(originURL + "/v1/models?cursor=fixture")
	if err != nil {
		t.Fatal("approved local request failed")
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(data) != "fixture reply" || resp.StatusCode != http.StatusOK || received.Load() != 1 || <-host != strings.TrimPrefix(originURL, "http://") {
		t.Fatal("wire response or original Host changed")
	}
}

// A failed negative test must not accidentally send a packet to a public IP.
// Control runs before connect and also counts attempted network dials.
func localHTTPDialGuard(t *testing.T, c *http.Client) *atomic.Int64 {
	t.Helper()
	calls := new(atomic.Int64)
	c.Transport.(*httpAdapter).dialer.Control = func(_, address string, _ syscall.RawConn) error {
		calls.Add(1)
		host, _, err := net.SplitHostPort(address)
		ip, e := netip.ParseAddr(host)
		if err != nil || e != nil || !ip.Unmap().IsLoopback() {
			return errors.New("test safety guard: non-loopback dial")
		}
		return nil
	}
	return calls
}

func TestLocalHTTPRejectsBeforeTransmission(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	originURL := localHTTPOrigin(t, srv)
	loop := netip.MustParseAddr("127.0.0.1")
	cases := []struct {
		name      string
		url       string
		ips       []netip.Addr
		lookupErr error
		host      string
		header    string
	}{
		{name: "public", ips: []netip.Addr{netip.MustParseAddr("8.8.8.8")}},
		{name: "mixed", ips: []netip.Addr{loop, netip.MustParseAddr("8.8.8.8")}},
		{name: "unpinned-local", ips: []netip.Addr{netip.MustParseAddr("127.0.0.2")}},
		{name: "empty"},
		{name: "invalid", ips: []netip.Addr{{}}},
		{name: "lookup-error", ips: []netip.Addr{loop}, lookupErr: errors.New("private-dns-error-canary")},
		{name: "too-many", ips: make([]netip.Addr, maxAddresses+1)},
		{name: "unknown-origin", url: strings.Replace(originURL, "model.internal", "unregistered.internal", 1), ips: []netip.Addr{loop}},
		{name: "override-host", host: "other.internal", ips: []netip.Addr{loop}},
		{name: "host-header", header: "hOsT", ips: []netip.Addr{loop}},
		{name: "decoded-control", url: originURL + "/%0d", ips: []netip.Addr{loop}},
		{name: "userinfo", url: strings.Replace(originURL, "http://", "http://test-user:private-canary@", 1), ips: []netip.Addr{loop}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lookups atomic.Int64
			c, err := NewClient(localHTTPPolicy(t, originURL), ClientOptions{LookupIP: func(context.Context, string) ([]netip.Addr, error) { lookups.Add(1); return tc.ips, tc.lookupErr }})
			if err != nil {
				t.Fatal("fixture client failed")
			}
			defer c.CloseIdleConnections()
			dials := localHTTPDialGuard(t, c)
			raw := tc.url
			if raw == "" {
				raw = originURL
			}
			r, err := http.NewRequest(http.MethodPost, raw, strings.NewReader("synthetic body"))
			if err != nil {
				t.Fatal("fixture request failed")
			}
			if tc.host != "" {
				r.Host = tc.host
			}
			if tc.header != "" {
				r.Header[tc.header] = []string{"private-host-canary"}
			}
			resp, err := c.Do(r)
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil || !errors.Is(err, ErrRequestBlocked) || hits.Load() != 0 || dials.Load() != 0 {
				t.Fatal("denied request reached a network dial or server")
			}
			if (tc.name == "unknown-origin" || tc.host != "" || tc.header != "") && lookups.Load() != 0 {
				t.Fatal("unapproved authority was resolved")
			}
		})
	}
}

func TestLocalHTTPInvalidClients(t *testing.T) {
	for _, p := range []*Policy{nil, {}} {
		if c, err := NewClient(p, ClientOptions{}); c != nil || !errors.Is(err, ErrInvalidPolicy) {
			t.Fatal("missing policy accepted")
		}
	}
	if c, err := NewClient(localHTTPPolicy(t, "http://model.internal"), ClientOptions{Timeout: -time.Second}); c != nil || !errors.Is(err, ErrClientOptions) {
		t.Fatal("invalid options accepted")
	}
}

func TestLocalHTTPRechecksDNSBeforeDial(t *testing.T) {
	var hits, lookups atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	o := localHTTPOrigin(t, srv)
	c, err := NewClient(localHTTPPolicy(t, o), ClientOptions{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	dials := localHTTPDialGuard(t, c)
	_, err = c.Get(o)
	if err == nil || !errors.Is(err, ErrRequestBlocked) || lookups.Load() != 2 || dials.Load() != 0 || hits.Load() != 0 {
		t.Fatal("DNS rebind reached the wire")
	}
}

func TestLocalHTTPLiteralNeverUsesResolver(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c, err := NewClient(localHTTPPolicy(t, srv.URL), ClientOptions{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		t.Error("literal IP was resolved")
		return nil, errors.New("unexpected lookup")
	}})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	localHTTPDialGuard(t, c)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal("literal local request failed")
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatal("literal request count mismatch")
	}
}

func TestLocalHTTPProxyEnvironmentIgnored(t *testing.T) {
	var proxyHits, modelHits atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1) }))
	defer proxy.Close()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { modelHits.Add(1) }))
	defer model.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	o := localHTTPOrigin(t, model)
	c, err := NewClient(localHTTPPolicy(t, o), ClientOptions{LookupIP: localHTTPLookup})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	localHTTPDialGuard(t, c)
	resp, err := c.Get(o)
	if err != nil {
		t.Fatal("direct request failed")
	}
	resp.Body.Close()
	if modelHits.Load() != 1 || proxyHits.Load() != 0 {
		t.Fatal("environment proxy changed the direct route")
	}
}

func TestLocalHTTPRedirects(t *testing.T) {
	var trapHits, successHits atomic.Int64
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { trapHits.Add(1) }))
	defer trap.Close()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/local":
			http.Redirect(w, r, "/ok", http.StatusTemporaryRedirect)
		case "/other":
			http.Redirect(w, r, trap.URL, http.StatusTemporaryRedirect)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			successHits.Add(1)
		}
	}))
	defer model.Close()
	o := localHTTPOrigin(t, model)
	p, err := New([]Endpoint{{Origin: o, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, {Origin: trap.URL, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}})
	if err != nil {
		t.Fatal("fixture policy failed")
	}
	c, err := NewClient(p, ClientOptions{LookupIP: localHTTPLookup})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	dials := localHTTPDialGuard(t, c)
	resp, err := c.Get(o + "/local")
	if err != nil {
		t.Fatal("same-origin redirect failed")
	}
	resp.Body.Close()
	if successHits.Load() != 1 || dials.Load() != 2 {
		t.Fatal("same-origin redirect was not rechecked")
	}
	resp, err = c.Get(o + "/other")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !errors.Is(err, ErrRequestBlocked) || trapHits.Load() != 0 {
		t.Fatal("cross-origin redirect reached its destination")
	}
	resp, err = c.Get(o + "/loop")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !errors.Is(err, ErrRequestBlocked) {
		t.Fatal("redirect loop was not bounded")
	}
}

func TestLocalHTTPHostCanonicalAuthority(t *testing.T) {
	for _, tc := range []struct {
		url, host string
		ok        bool
	}{
		{"http://model.internal", "MODEL.INTERNAL:80", true},
		{"https://model.internal:443", "model.internal", true},
		{"http://[::1]:80", "[::1]", true},
		{"http://model.internal", "model.internal:81", false},
		{"http://model.internal", "user@model.internal", false},
		{"http://model.internal", "model.internal\r\nX: bad", false},
		{"http://model.internal", "model.internal:", false},
		{"http://model.internal", "[model.internal]", false},
	} {
		pin := netip.MustParseAddr("127.0.0.1")
		if strings.Contains(tc.url, "[::1]") {
			pin = netip.IPv6Loopback()
		}
		p, err := New([]Endpoint{{Origin: tc.url, Addresses: []netip.Addr{pin}}})
		if err != nil {
			t.Fatal("fixture policy failed")
		}
		c, err := NewClient(p, ClientOptions{})
		if err != nil {
			t.Fatal("fixture client failed")
		}
		r, err := http.NewRequest(http.MethodGet, tc.url, nil)
		if err != nil {
			t.Fatal("fixture request failed")
		}
		r.Host = tc.host
		_, err = c.Transport.(*httpAdapter).requestOrigin(r)
		c.CloseIdleConnections()
		if (err == nil) != tc.ok {
			t.Fatal("canonical Host decision mismatch")
		}
	}
}

func TestLocalHTTPConcurrentRequests(t *testing.T) {
	const count = 8
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	o := localHTTPOrigin(t, srv)
	c, err := NewClient(localHTTPPolicy(t, o), ClientOptions{LookupIP: localHTTPLookup})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	dials := localHTTPDialGuard(t, c)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Get(o)
			if err != nil {
				t.Error("concurrent local request failed")
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()
	if hits.Load() != count || dials.Load() != count {
		t.Fatal("concurrent requests reused or lost a dial plan")
	}
}

func TestLocalHTTPCancelledAndMissingDialPlans(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	o := localHTTPOrigin(t, srv)
	c, err := NewClient(localHTTPPolicy(t, o), ClientOptions{LookupIP: localHTTPLookup})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	dials := localHTTPDialGuard(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, o, nil)
	if _, err = c.Do(r); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not preserved")
	}
	if conn, err := c.Transport.(*httpAdapter).dialContext(context.Background(), "tcp", srv.Listener.Addr().String()); conn != nil || !errors.Is(err, ErrRequestBlocked) {
		t.Fatal("dial without a request plan was allowed")
	}
	if hits.Load() != 0 || dials.Load() != 0 {
		t.Fatal("cancelled/unbound request reached network")
	}
}

func TestLocalHTTPMalformedResponseDoesNotLogSource(t *testing.T) {
	const secret = "private-protocol-canary-3a1f"
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error("fixture hijack failed")
			return
		}
		defer conn.Close()
		if _, err = fmt.Fprint(conn, secret+"\r\n\r\n"); err != nil {
			t.Error("fixture malformed write failed")
		}
	}))
	defer srv.Close()
	o := localHTTPOrigin(t, srv)
	c, err := NewClient(localHTTPPolicy(t, o), ClientOptions{LookupIP: localHTTPLookup})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	localHTTPDialGuard(t, c)
	_, err = c.Get(o + "/?opaque=" + secret)
	if !errors.Is(err, ErrTransport) || strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "model.internal") {
		t.Fatal("malformed response escaped fixed error/log boundary")
	}
}

func TestLocalHTTPFreshConnectionFollowsCurrentPinnedIP(t *testing.T) {
	var firstHits, secondHits atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { firstHits.Add(1); _, _ = io.WriteString(w, "first") }))
	defer first.Close()
	_, port, err := net.SplitHostPort(first.Listener.Addr().String())
	if err != nil {
		t.Fatal("fixture port failed")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.2", port))
	if err != nil {
		t.Fatal("loopback alias fixture failed")
	}
	second := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { secondHits.Add(1); _, _ = io.WriteString(w, "second") }))
	second.Listener.Close()
	second.Listener = listener
	second.Start()
	defer second.Close()
	originURL := localHTTPOrigin(t, first)
	one := netip.MustParseAddr("127.0.0.1")
	two := netip.MustParseAddr("127.0.0.2")
	p, err := New([]Endpoint{{Origin: originURL, Addresses: []netip.Addr{one, two}}})
	if err != nil {
		t.Fatal("fixture policy failed")
	}
	var useSecond atomic.Bool
	c, err := NewClient(p, ClientOptions{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		if useSecond.Load() {
			return []netip.Addr{two}, nil
		}
		return []netip.Addr{one}, nil
	}})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	dials := localHTTPDialGuard(t, c)
	for i, want := range []string{"first", "second"} {
		useSecond.Store(i == 1)
		resp, err := c.Get(originURL)
		if err != nil {
			t.Fatal("current pinned request failed")
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != want {
			t.Fatal("request reused an obsolete peer connection")
		}
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 || dials.Load() != 2 {
		t.Fatal("new request did not bind a fresh peer")
	}
}

func TestLocalHTTPDoesNotSubstituteSelectedIPWithinAttempt(t *testing.T) {
	var hits, lookups atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	o := localHTTPOrigin(t, srv)
	one := netip.MustParseAddr("127.0.0.1")
	two := netip.MustParseAddr("127.0.0.2")
	p, err := New([]Endpoint{{Origin: o, Addresses: []netip.Addr{one, two}}})
	if err != nil {
		t.Fatal("fixture policy failed")
	}
	c, err := NewClient(p, ClientOptions{LookupIP: func(context.Context, string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{one}, nil
		}
		return []netip.Addr{two}, nil
	}})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	dials := localHTTPDialGuard(t, c)
	_, err = c.Get(o)
	if !errors.Is(err, ErrRequestBlocked) || hits.Load() != 0 || dials.Load() != 0 {
		t.Fatal("changed selected address reached a dial")
	}
}

type localHTTPTrackedBody struct {
	closed bool
	fail   bool
}

func (b *localHTTPTrackedBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *localHTTPTrackedBody) Close() error {
	b.closed = true
	if b.fail {
		return errors.New("private-close-error-canary")
	}
	return nil
}

func TestLocalHTTPRejectClosesBodyWithoutErrorSource(t *testing.T) {
	c, err := NewClient(localHTTPPolicy(t, "http://model.internal"), ClientOptions{LookupIP: localHTTPLookup})
	if err != nil {
		t.Fatal("fixture client failed")
	}
	defer c.CloseIdleConnections()
	for _, fail := range []bool{false, true} {
		body := &localHTTPTrackedBody{fail: fail}
		r, _ := http.NewRequest(http.MethodPost, "http://unregistered.internal", body)
		resp, err := c.Transport.RoundTrip(r)
		if resp != nil || !errors.Is(err, ErrRequestBlocked) || !body.closed || strings.Contains(err.Error(), "private-close-error-canary") {
			t.Fatal("rejected request retained its body or error source")
		}
		if errors.Is(err, ErrBodyClose) != fail {
			t.Fatal("body close outcome was not reported")
		}
	}
	if resp, err := c.Transport.RoundTrip(nil); resp != nil || !errors.Is(err, ErrRequestBlocked) {
		t.Fatal("nil request was accepted")
	}
}
