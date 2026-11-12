package exploreboundary

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"io"
	"math/big"
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

const (
	proxyTimeout     = 5 * time.Second
	proxyMaxBody     = 4096
	proxyMaxResponse = 64
	proxyMaxTunnels  = 2
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return testCA{cert: cert, key: key, pool: pool}
}

func (ca testCA) leaf(t *testing.T, host string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(sum[:])
}

type proxyFixture struct {
	session  *Session
	proxy    *Proxy
	proxyURL *url.URL
	client   *http.Client
	target   string
	port     uint16
	hits     *atomic.Int64
	mitm     testCA
}

func newProxyFixture(t *testing.T, write string, handler http.HandlerFunc, alter func(*Target)) *proxyFixture {
	t.Helper()
	upstreamCA, mitm := newTestCA(t, "upstream-ca"), newTestCA(t, "boundary-ca")
	leaf, spki := upstreamCA.leaf(t, fixtureHost)
	hits := &atomic.Int64{}
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); handler(w, r) }))
	upstream.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	_, portText, _ := net.SplitHostPort(upstream.Listener.Addr().String())
	port, _ := strconv.ParseUint(portText, 10, 16)
	session, _, _ := fixtureSession(t, write, exploration.FullLocal, func(target *Target, p *Policy) {
		target.Port, target.ConnectIP, target.SPKISHA256 = uint16(port), netip.MustParseAddr("127.0.0.1"), spki
		p.Now = time.Now
		target.ExpiresAt = time.Now().Add(time.Hour)
		if alter != nil {
			alter(target)
		}
	})
	current := session
	proxy, err := NewProxy(ProxyConfig{Ingress: BrowserIngress, Current: func() *Session { return current }, CA: mitm.cert, CAKey: mitm.key, Roots: upstreamCA.pool,
		MaxRequestBytes: proxyMaxBody, MaxResponseBytes: proxyMaxResponse, MaxTunnels: proxyMaxTunnels, Timeout: proxyTimeout, LeafValidity: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	proxyURL, _ := url.Parse(front.URL)
	client := &http.Client{Timeout: proxyTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: mitm.pool}, DisableKeepAlives: true}}
	return &proxyFixture{session: session, proxy: proxy, proxyURL: proxyURL, client: client, target: "https://" + fixtureHost + ":" + portText, port: uint16(port), hits: hits, mitm: mitm}
}

func TestProxyForwardsApprovedRequestThroughPinnedTLS(t *testing.T) {
	f := newProxyFixture(t, exploration.ApprovedWrites, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "target-session=issued")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}, nil)
	resp, err := f.client.Post(f.target+"/app/cart?item=1", "application/json", strings.NewReader(`{"sku":"secret-sku-value","qty":2}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != `{"ok":true}` || resp.Header.Get("Set-Cookie") == "" || f.hits.Load() != 1 {
		t.Fatal("approved request did not reach the pinned target", resp.StatusCode, string(body), f.hits.Load())
	}
	seqs, _ := f.session.Sequences(f.session.flow)
	o, err := f.session.Reader().ReadObservation(t.Context(), f.session.flow, seqs[0])
	if err != nil || o.Path != "/app/cart" || o.Method != "POST" || strings.Join(o.QueryKeys, ",") != "item" || strings.Join(o.BodyKeys, ",") != "qty,sku" || o.ContentType != "application/json" || o.Status != 200 {
		t.Fatalf("observation: %+v %v", o, err)
	}
	records, _ := f.session.Records()
	for _, r := range records {
		raw := r.PathPattern + r.ExternalHost + r.Reason
		if strings.Contains(raw, "secret-sku-value") || strings.Contains(raw, "item=1") || strings.Contains(raw, "issued") {
			t.Fatal("record carried a value", r)
		}
	}
}

func TestProxyRefusesWithoutTouchingTarget(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "should not be reached") }, nil)
	for _, tc := range []struct {
		method, path, want string
		header             map[string]string
	}{
		{"GET", "/other", ReasonOutOfScopePath, nil},
		{"POST", "/app/cart", ReasonMethodNotApproved, nil},
		{"GET", "/app/socket", ReasonProtocolUnsupported, map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}},
	} {
		req, _ := http.NewRequest(tc.method, f.target+tc.path, nil)
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		resp, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get(DenialHeader) != tc.want {
			t.Fatalf("%s %s: status=%d reason=%q", tc.method, tc.path, resp.StatusCode, resp.Header.Get(DenialHeader))
		}
	}
	// 평문 프록시 요청도 막는다.
	plain := &http.Client{Timeout: proxyTimeout, Transport: &http.Transport{Proxy: http.ProxyURL(f.proxyURL)}}
	resp, err := plain.Get("http://" + fixtureHost + "/app")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get(DenialHeader) != ReasonProtocolUnsupported || f.hits.Load() != 0 {
		t.Fatal("plain proxy request was not refused", resp.StatusCode, f.hits.Load())
	}
	if f.session.Status().TargetRequests != 0 {
		t.Fatal("refused requests consumed the target budget")
	}
}

func TestProxyPinMismatchAndOversizedResponseAreNotDelivered(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, func(target *Target) {
		target.SPKISHA256 = strings.Repeat("00", 32)
	})
	resp, err := f.client.Get(f.target + "/app")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get(ResultHeader) != "upstream_failed" || f.hits.Load() != 0 || f.session.Status().TargetRequests != 1 {
		t.Fatal("SPKI mismatch reached the target or was not counted", resp.StatusCode, f.hits.Load(), f.session.Status())
	}
	big := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", proxyMaxResponse+1))
	}, nil)
	resp, err = big.client.Get(big.target + "/app")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get(ResultHeader) != "response_limit" || len(body) != 0 {
		t.Fatal("oversized response was delivered", resp.StatusCode, len(body))
	}
}

func TestProxyDoesNotFollowRedirects(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/start" {
			http.Redirect(w, r, "/outside", http.StatusFound)
			return
		}
		io.WriteString(w, "followed")
	}, nil)
	resp, err := f.client.Get(f.target + "/app/start")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/outside" || f.hits.Load() != 1 {
		t.Fatal("boundary followed a redirect on its own", resp.StatusCode, f.hits.Load())
	}
	// 실행기가 Location을 따라가면 새 요청으로 다시 판정한다.
	resp, err = f.client.Get(f.target + "/outside")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get(DenialHeader) != ReasonOutOfScopePath || f.hits.Load() != 1 {
		t.Fatal("redirect target outside scope was forwarded")
	}
}

func rawTunnel(t *testing.T, f *proxyFixture, authority string) (*tls.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", f.proxyURL.Host, proxyTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	io.WriteString(conn, "CONNECT "+authority+" HTTP/1.1\r\nHost: "+authority+"\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal("tunnel refused", err)
	}
	host, _, _ := net.SplitHostPort(authority)
	tc := tls.Client(conn, &tls.Config{ServerName: host, RootCAs: f.mitm.pool})
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}
	return tc, bufio.NewReader(tc)
}

func TestProxyRejectsInnerHostThatDiffersFromTunnel(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	authority := fixtureHost + ":" + strconv.Itoa(int(f.port))
	tc, reader := rawTunnel(t, f, authority)
	io.WriteString(tc, "GET /app HTTP/1.1\r\nHost: other.example:"+strconv.Itoa(int(f.port))+"\r\n\r\n")
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get(DenialHeader) != ReasonOutOfScopeOrigin || f.hits.Load() != 0 {
		t.Fatal("inner Host different from the tunnel authority was forwarded")
	}
}

func TestProxyTunnelLimitAndSessionClose(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	authority := fixtureHost + ":" + strconv.Itoa(int(f.port))
	held := []*tls.Conn{}
	for i := 0; i < proxyMaxTunnels; i++ {
		tc, _ := rawTunnel(t, f, authority)
		held = append(held, tc)
	}
	conn, err := net.DialTimeout("tcp", f.proxyURL.Host, proxyTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "CONNECT "+authority+" HTTP/1.1\r\nHost: "+authority+"\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != http.StatusForbidden || resp.Header.Get(DenialHeader) != ReasonCapExhausted {
		t.Fatal("tunnel limit was not enforced", err)
	}
	f.proxy.CloseTunnels()
	deadline := time.Now().Add(proxyTimeout)
	for f.proxy.Tunnels() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if f.proxy.Tunnels() != 0 {
		t.Fatal("open tunnels survived CloseTunnels")
	}
	for _, tc := range held {
		tc.SetReadDeadline(time.Now().Add(proxyTimeout))
		if _, err := tc.Read(make([]byte, 1)); err == nil {
			t.Fatal("closed tunnel still readable")
		}
	}
	if _, err := f.session.Close(t.Context(), exploration.Cancelled); err != nil {
		t.Fatal(err)
	}
	resp2, err := f.client.Get(f.target + "/app")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden || f.hits.Load() != 0 {
		t.Fatal("closed session forwarded a request", resp2.StatusCode)
	}
}
