package exploreboundary

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

// http 대상(C22)은 SPKI 고정 없이 승인하고, 평문 요청만 승인 origin으로 본다. 같은 이름·포트라도 TLS로 오면 다른
// origin이다. https 대상은 지금처럼 SPKI가 있어야 한다.
func TestHTTPTargetSessionAdmitsOnlyPlainApprovedOrigin(t *testing.T) {
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.ExternalAllowed, func(target *Target, _ *Policy) {
		target.Scheme, target.SPKISHA256 = "http", ""
	})
	plain := approved("GET", "/app/item")
	plain.Scheme = "http"
	d, ticket := s.Admit(t.Context(), plain)
	if !d.Forward || d.Dest != DestApproved || d.VerifySPKI != "" || d.Dial != netip.AddrPortFrom(fixtureConnect, fixturePort) {
		t.Fatalf("plain request to the approved http target refused: %+v", d)
	}
	ticket.Done(Result{Status: 200})
	for name, req := range map[string]Request{
		"tls_to_http_target": approved("GET", "/app/item"),
		"plain_external":     {Ingress: BrowserIngress, Method: "GET", Scheme: "http", Host: "cdn.example", Port: 80, EscapedPath: "/lib.js", FetchMode: "no-cors"},
		"plain_outside_path": func() Request { r := approved("GET", "/other"); r.Scheme = "http"; return r }(),
	} {
		d, ticket := s.Admit(t.Context(), req)
		if d.Forward || ticket != nil {
			t.Fatalf("%s forwarded: %+v", name, d)
		}
		want := ReasonProtocolUnsupported
		if name == "plain_outside_path" {
			want = ReasonOutOfScopePath
		}
		if d.Reason != want {
			t.Fatalf("%s: %s want %s", name, d.Reason, want)
		}
	}
	if st := s.Status(); st.TargetRequests != 1 {
		t.Fatal("denied requests counted", st)
	}
}

func TestTargetTLSPinFollowsScheme(t *testing.T) {
	b := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	base := Target{Ref: "target-1", Revision: 2, Host: fixtureHost, Port: fixturePort, PathPrefixes: []string{"/app"}, Methods: []string{"GET"}, ConnectIP: fixtureConnect, ExpiresAt: time.Now().Add(time.Hour)}
	for _, tc := range []struct {
		scheme, spki string
		ok           bool
	}{
		{"", fixtureSPKI, true}, {"https", fixtureSPKI, true}, {"http", "", true},
		{"", "", false}, {"https", "", false}, {"http", fixtureSPKI, false}, {"ftp", "", false},
	} {
		target := base
		target.Scheme, target.SPKISHA256 = tc.scheme, tc.spki
		if validTarget(target, b) != tc.ok {
			t.Errorf("scheme=%q spki=%q valid=%v", tc.scheme, tc.spki, !tc.ok)
		}
	}
	// 제어 통로 형식: https는 이전처럼 scheme 칸을 비우고, http만 싣는다. 다른 값은 받지 않는다.
	for _, scheme := range []string{"https", "http"} {
		target := base
		target.Scheme = scheme
		wire := WireTargetOf(target)
		back, err := wire.target()
		if err != nil || back.scheme() != scheme || (scheme == "https") != (wire.Scheme == "") {
			t.Fatalf("%s wire round trip: %+v %+v %v", scheme, wire, back, err)
		}
	}
	if _, err := (WireTarget{Scheme: "https", ConnectIP: "10.20.30.40"}).target(); err == nil {
		t.Fatal("explicit https wire scheme accepted")
	}
}

// 브라우저·직접 입구의 평문 프록시 요청(절대 URI)이 http 승인 대상에 그대로 닿고, 순번이 붙으며, 판정 기록에 값이 없다.
// TLS로 온 같은 origin과 평문 외부 요청은 대상에 닿지 않는다.
func TestProxyForwardsPlainRequestToHTTPTarget(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "plain ok")
	}))
	t.Cleanup(upstream.Close)
	_, portText, _ := net.SplitHostPort(upstream.Listener.Addr().String())
	port, _ := strconv.ParseUint(portText, 10, 16)
	session, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, func(target *Target, p *Policy) {
		target.Scheme, target.SPKISHA256 = "http", ""
		target.Port, target.ConnectIP = uint16(port), netip.MustParseAddr("127.0.0.1")
		p.Now = time.Now
		target.ExpiresAt = time.Now().Add(time.Hour)
	})
	mitm := newTestCA(t, "boundary-ca")
	proxy, err := NewProxy(ProxyConfig{Ingress: BrowserIngress, Current: func() *Session { return session }, CA: mitm.cert, CAKey: mitm.key, Roots: mitm.pool,
		MaxRequestBytes: proxyMaxBody, MaxResponseBytes: proxyMaxResponse, MaxTunnels: proxyMaxTunnels, Timeout: proxyTimeout, LeafValidity: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	proxyURL, _ := url.Parse(front.URL)
	client := &http.Client{Timeout: proxyTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: mitm.pool}, DisableKeepAlives: true}}
	resp, err := client.Get("http://" + fixtureHost + ":" + portText + "/app/item?id=secret-query-value")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "plain ok" || hits.Load() != 1 || resp.Header.Get(SeqHeader) == "" {
		t.Fatalf("plain request did not reach the http target: %d %q hits=%d seq=%q", resp.StatusCode, body, hits.Load(), resp.Header.Get(SeqHeader))
	}
	for _, target := range []string{"https://" + fixtureHost + ":" + portText + "/app/item", "http://cdn.example/lib.js"} {
		resp, err := client.Get(target)
		if err == nil {
			resp.Body.Close()
		}
		if err == nil && resp.Header.Get(DenialHeader) != ReasonProtocolUnsupported || hits.Load() != 1 {
			t.Fatalf("%s reached the target or was not refused: %v hits=%d", target, err, hits.Load())
		}
	}
	records, _ := session.Records()
	for _, r := range records {
		if strings.Contains(r.PathPattern+r.ExternalHost+r.Reason, "secret-query-value") {
			t.Fatal("record carried a value", r)
		}
	}
}
