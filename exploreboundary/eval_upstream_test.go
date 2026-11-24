package exploreboundary

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

// countedServer는 받은 TCP 연결 수와 요청을 센다. 평가 upstream이 아닌 곳으로 새는 연결을 핸들러 도달과 상관없이 잡는다.
type countedServer struct {
	*httptest.Server
	conns    atomic.Int64
	mu       sync.Mutex
	hosts    []string
	requests []string
}

type countListener struct {
	net.Listener
	n *atomic.Int64
}

func (l countListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}

func newCountedServer(t *testing.T, body string) *countedServer {
	t.Helper()
	s := &countedServer{}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hosts, s.requests = append(s.hosts, r.Host), append(s.requests, r.RequestURI)
		s.mu.Unlock()
		io.WriteString(w, body)
	}))
	s.Listener = countListener{Listener: s.Listener, n: &s.conns}
	s.Start()
	t.Cleanup(s.Close)
	return s
}

func (s *countedServer) addrPort() netip.AddrPort {
	return netip.MustParseAddrPort(s.Listener.Addr().String())
}

// 평가 배치에서 승인 대상 http 요청은 대상 주소가 아니라 계측기 탐색 입구(평가 upstream)로 간다. 요청은 origin-form이고
// Host는 승인 대상 그대로라, 계측기가 대상으로 넘기고 탐색 입구 행으로 센다. 경계 판정·기록은 그대로다(계약 §4.2).
func TestEvalUpstreamCarriesApprovedHTTPToMeterIngress(t *testing.T) {
	target := newCountedServer(t, "target")
	const meterResponse = "password=synthetic-meter-contract"
	meter := newCountedServer(t, meterResponse)
	session, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, func(tg *Target, p *Policy) {
		tg.Scheme, tg.SPKISHA256 = "http", ""
		tg.Port, tg.ConnectIP = target.addrPort().Port(), target.addrPort().Addr()
		p.Now = time.Now
		tg.ExpiresAt = time.Now().Add(time.Hour)
	})
	mitm := newTestCA(t, "boundary-ca")
	proxy, err := NewProxy(ProxyConfig{Ingress: DirectIngress, Current: func() *Session { return session }, CA: mitm.cert, CAKey: mitm.key, Roots: mitm.pool,
		MaxRequestBytes: proxyMaxBody, MaxResponseBytes: proxyMaxResponse, MaxTunnels: proxyMaxTunnels, Timeout: proxyTimeout, LeafValidity: time.Hour,
		EvalUpstream: meter.addrPort()})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	proxyURL, _ := url.Parse(front.URL)
	client := &http.Client{Timeout: proxyTimeout, Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}}
	authority := fixtureHost + ":" + strconv.Itoa(int(target.addrPort().Port()))
	resp, err := client.Get("http://" + authority + "/app/item?id=1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != meterResponse || resp.Header.Get(SeqHeader) == "" {
		t.Fatalf("approved response not carried through the eval upstream: status %d", resp.StatusCode)
	}
	meter.mu.Lock()
	hosts, requests := append([]string{}, meter.hosts...), append([]string{}, meter.requests...)
	meter.mu.Unlock()
	if target.conns.Load() != 0 || meter.conns.Load() != 1 || len(hosts) != 1 || hosts[0] != authority || requests[0] != "/app/item?id=1" {
		t.Fatalf("target conns %d, meter conns %d, hosts %v, requests %v", target.conns.Load(), meter.conns.Load(), hosts, requests)
	}
	// 승인 범위 밖 경로는 평가 모드에서도 경계가 막고 어디에도 연결하지 않는다.
	resp, err = client.Get("http://" + authority + "/other")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get(DenialHeader) != ReasonOutOfScopePath || meter.conns.Load() != 1 || target.conns.Load() != 0 {
		t.Fatalf("out of scope request: %q meter %d target %d", resp.Header.Get(DenialHeader), meter.conns.Load(), target.conns.Load())
	}
	records, _ := session.Records()
	if len(records) != 2 || records[0].Decision != "forwarded" || records[0].Dest != DestApproved || records[0].Status != http.StatusOK ||
		records[1].Decision != "denied" || records[1].Reason != ReasonOutOfScopePath {
		t.Fatalf("records %+v", records)
	}
}

// 평가 upstream은 평문이라 승인 대상 https 요청은 평가 모드에서 판정 전에 막고 기록한다. 대상 주소로 우회하지 않는다.
func TestEvalUpstreamRefusesApprovedHTTPS(t *testing.T) {
	meter := newCountedServer(t, "via meter")
	session, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	mitm := newTestCA(t, "boundary-ca")
	proxy, err := NewProxy(ProxyConfig{Ingress: BrowserIngress, Current: func() *Session { return session }, CA: mitm.cert, CAKey: mitm.key, Roots: mitm.pool,
		MaxRequestBytes: proxyMaxBody, MaxResponseBytes: proxyMaxResponse, MaxTunnels: proxyMaxTunnels, Timeout: proxyTimeout, LeafValidity: time.Hour,
		EvalUpstream: meter.addrPort()})
	if err != nil {
		t.Fatal(err)
	}
	in := httptest.NewRequest(http.MethodGet, "/app/item", nil)
	in.Host = fixtureHost + ":" + strconv.Itoa(fixturePort)
	w := httptest.NewRecorder()
	proxy.forward(w, in, "https", fixtureHost, fixturePort)
	if w.Code != http.StatusForbidden || w.Header().Get(DenialHeader) != ReasonProtocolUnsupported || meter.conns.Load() != 0 {
		t.Fatalf("https to the approved target in eval mode: %d %q meter conns %d", w.Code, w.Header().Get(DenialHeader), meter.conns.Load())
	}
	records, _ := session.Records()
	if len(records) != 1 || records[0].Decision != "denied" || records[0].Dest != DestApproved || records[0].Reason != ReasonProtocolUnsupported {
		t.Fatalf("records %+v", records)
	}
	if st := session.Status(); st.TargetRequests != 0 {
		t.Fatal("refused https request counted as a target request", st)
	}
}

// 평가 upstream은 승인 대상 전달에만 쓴다. 외부 자원은 판정 주소 그대로고, 제품(평가 upstream 없음)은 늘 판정 주소다.
func TestEvalUpstreamDialsOnlyApprovedTarget(t *testing.T) {
	upstream := netip.MustParseAddrPort("172.31.9.10:8081")
	judged := netip.AddrPortFrom(fixturePublic, 443)
	eval := &Proxy{cfg: ProxyConfig{EvalUpstream: upstream}}
	product := &Proxy{}
	for _, tc := range []struct {
		p    *Proxy
		dest DestClass
		want netip.AddrPort
	}{
		{eval, DestApproved, upstream}, {eval, DestExternal, judged}, {product, DestApproved, judged}, {product, DestExternal, judged},
	} {
		if got := tc.p.dialAddr(Decision{Forward: true, Dest: tc.dest, Dial: judged}); got != tc.want {
			t.Errorf("eval=%v dest=%s dial %s want %s", tc.p.cfg.EvalUpstream.IsValid(), tc.dest, got, tc.want)
		}
	}
}

func TestEvalUpstreamAddressShape(t *testing.T) {
	mitm := newTestCA(t, "boundary-ca")
	base := ProxyConfig{Ingress: DirectIngress, Current: func() *Session { return nil }, CA: mitm.cert, CAKey: mitm.key,
		MaxRequestBytes: proxyMaxBody, MaxResponseBytes: proxyMaxResponse, MaxTunnels: proxyMaxTunnels, Timeout: proxyTimeout, LeafValidity: time.Hour}
	for _, raw := range []string{"172.31.9.10:0", "0.0.0.0:8081", "[::]:8081", "224.0.0.1:8081", "[fe80::1%eth0]:8081"} {
		c := base
		c.EvalUpstream = netip.MustParseAddrPort(raw)
		if _, err := NewProxy(c); err == nil {
			t.Errorf("eval upstream %s accepted", raw)
		}
	}
	c := base
	c.EvalUpstream = netip.MustParseAddrPort("172.31.9.10:8081")
	if _, err := NewProxy(c); err != nil {
		t.Fatal("valid eval upstream refused", err)
	}
}
