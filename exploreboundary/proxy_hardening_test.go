package exploreboundary

// 리뷰(#488) 재현용 시험. 실패가 곧 PR head의 결함 재현이다(일부는 확인용 PASS 시험).

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

// R1: 터널 authority와 안쪽 Host가 다른 요청은 403으로 막히지만 경계 기록에 행이 남지 않는다.
func TestInnerHostMismatchRecorded(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	authority := fixtureHost + ":" + strconv.Itoa(int(f.port))
	tc, reader := rawTunnel(t, f, authority)
	for i := 0; i < 5; i++ {
		io.WriteString(tc, "GET /app HTTP/1.1\r\nHost: other.example:"+strconv.Itoa(int(f.port))+"\r\n\r\n")
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.Header.Get(DenialHeader) != ReasonOutOfScopeOrigin {
			t.Fatal("unexpected", resp.StatusCode)
		}
	}
	records, omitted := f.session.Records()
	if len(records) == 0 && omitted == 0 {
		t.Fatalf("5 denied requests (out_of_scope_origin) left no boundary record: records=%d omitted=%d", len(records), omitted)
	}
}

// R2: 절대 형식 안쪽 요청의 userinfo가 대상에 Authorization으로 나가지만 기록은 has_auth_header=false다.
func TestUserinfoNotForwardedAsAuthorization(t *testing.T) {
	var gotAuth atomic.Value
	gotAuth.Store("")
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		io.WriteString(w, "ok")
	}, nil)
	authority := fixtureHost + ":" + strconv.Itoa(int(f.port))
	tc, reader := rawTunnel(t, f, authority)
	io.WriteString(tc, "GET https://alice:s3cret@"+authority+"/app HTTP/1.1\r\nHost: "+authority+"\r\n\r\n")
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	records, _ := f.session.Records()
	t.Logf("status=%d upstream Authorization=%q records=%+v", resp.StatusCode, gotAuth.Load(), records)
	if gotAuth.Load().(string) != "" && len(records) == 1 && !records[0].HasAuthHeader {
		t.Fatal("credential reached the target via URL userinfo while the record says has_auth_header=false")
	}
}

// R3: 본문을 보내지 않는 요청이 Timeout과 상관없이 터널 자리를 계속 잡는다. 기록도 없다.
func TestStalledBodyReleasesTunnel(t *testing.T) {
	f := newProxyFixture(t, exploration.ApprovedWrites, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	const short = 200 * time.Millisecond
	p, err := NewProxy(ProxyConfig{Ingress: BrowserIngress, Current: func() *Session { return f.session }, CA: f.mitm.cert, CAKey: f.mitm.key,
		MaxRequestBytes: proxyMaxBody, MaxResponseBytes: proxyMaxResponse, MaxTunnels: 1, Timeout: short, LeafValidity: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)
	u, _ := url.Parse(front.URL)
	g := &proxyFixture{proxyURL: u, mitm: f.mitm}
	authority := fixtureHost + ":" + strconv.Itoa(int(f.port))
	tc, _ := rawTunnel(t, g, authority)
	io.WriteString(tc, "POST /app/cart HTTP/1.1\r\nHost: "+authority+"\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n")
	time.Sleep(10 * short)
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "CONNECT "+authority+" HTTP/1.1\r\nHost: "+authority+"\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	records, _ := f.session.Records()
	t.Logf("after %v (timeout %v): tunnels=%d new CONNECT=%d %q records=%d", 10*short, short, p.Tunnels(), resp.StatusCode, resp.Header.Get(DenialHeader), len(records))
	if p.Tunnels() == 1 && resp.Header.Get(DenialHeader) == ReasonCapExhausted {
		t.Fatal("a stalled request body kept the only tunnel slot past 10x Timeout; new tunnels are refused (cap_exhausted) and nothing is recorded")
	}
}

// 확인용: HTTP/2 prior knowledge 서문은 안쪽에서 처리되지 않고 대상에 닿지 않는다.
func TestH2PrefaceInsideTunnelDoesNotReachTarget(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	authority := fixtureHost + ":" + strconv.Itoa(int(f.port))
	tc, reader := rawTunnel(t, f, authority)
	io.WriteString(tc, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")
	tc.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, _ := reader.ReadString('\n')
	t.Logf("h2 preface answer: %q hits=%d", line, f.hits.Load())
	if f.hits.Load() != 0 || f.session.Status().TargetRequests != 0 {
		t.Fatal("h2 preface reached the target")
	}
	// ALPN h2만 내는 클라이언트는 협상 실패.
	conn, _ := net.Dial("tcp", f.proxyURL.Host)
	defer conn.Close()
	io.WriteString(conn, "CONNECT "+authority+" HTTP/1.1\r\nHost: "+authority+"\r\n\r\n")
	if resp, err := http.ReadResponse(bufio.NewReader(conn), nil); err != nil || resp.StatusCode != 200 {
		t.Fatal(err)
	}
	h2 := tls.Client(conn, &tls.Config{ServerName: fixtureHost, RootCAs: f.mitm.pool, NextProtos: []string{"h2"}})
	err := h2.Handshake()
	t.Logf("h2-only ALPN handshake err=%v proto=%q", err, h2.ConnectionState().NegotiatedProtocol)
	if err == nil && h2.ConnectionState().NegotiatedProtocol == "h2" {
		t.Fatal("h2 negotiated")
	}
}

// 확인용: 한 터널에 파이프라인된 요청은 각각 판정·계수된다. TE+CL 섞인 요청의 "밀반입" 요청도 판정을 거친다.
func TestPipelinedAndSmuggledRequestsAreEachAdmitted(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	f := newProxyFixture(t, exploration.ApprovedWrites, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		io.WriteString(w, "ok")
	}, nil)
	authority := fixtureHost + ":" + strconv.Itoa(int(f.port))
	tc, reader := rawTunnel(t, f, authority)
	smuggled := "GET /other HTTP/1.1\r\nHost: " + authority + "\r\n\r\n"
	io.WriteString(tc, "GET /app/a HTTP/1.1\r\nHost: "+authority+"\r\n\r\n"+
		"POST /app/b HTTP/1.1\r\nHost: "+authority+"\r\nContent-Length: 5\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n"+smuggled)
	statuses := []int{}
	for i := 0; i < 3; i++ {
		tc.SetReadDeadline(time.Now().Add(2 * time.Second))
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			break
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		statuses = append(statuses, resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("statuses=%v upstream saw=%v target_requests=%d", statuses, seen, f.session.Status().TargetRequests)
	for _, s := range seen {
		if strings.Contains(s, "/other") {
			t.Fatal("smuggled request reached the target")
		}
	}
	if uint64(len(seen)) != f.session.Status().TargetRequests {
		t.Fatal("upstream hits differ from the counted target requests")
	}
}

// R4: CONNECT 헤더와 TLS ClientHello를 한 번에 보내는 클라이언트는 터널을 못 쓴다(Hijack 버퍼를 버림).
type eagerConn struct {
	net.Conn
	prefix   string
	wrote    bool
	readResp bool
	br       *bufio.Reader
}

func (c *eagerConn) Write(b []byte) (int, error) {
	if !c.wrote {
		c.wrote = true
		if _, err := c.Conn.Write(append([]byte(c.prefix), b...)); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	return c.Conn.Write(b)
}

func (c *eagerConn) Read(b []byte) (int, error) {
	if !c.readResp {
		c.readResp = true
		resp, err := http.ReadResponse(c.br, nil)
		if err != nil {
			return 0, err
		}
		if resp.StatusCode != 200 {
			return 0, io.ErrUnexpectedEOF
		}
	}
	return c.br.Read(b)
}

func TestClientHelloPipelinedWithConnectIsServed(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	authority := fixtureHost + ":" + strconv.Itoa(int(f.port))
	raw, err := net.Dial("tcp", f.proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(3 * time.Second))
	ec := &eagerConn{Conn: raw, prefix: "CONNECT " + authority + " HTTP/1.1\r\nHost: " + authority + "\r\n\r\n", br: bufio.NewReader(raw)}
	tc := tls.Client(ec, &tls.Config{ServerName: fixtureHost, RootCAs: f.mitm.pool})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("TLS handshake failed when ClientHello was sent together with CONNECT: %v", err)
	}
}

// R5: 대상이 경계 결과 헤더를 꾸며 보내면 그대로 실행기에 간다.
func TestUpstreamCannotForgeResultHeader(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ResultHeader, "response_limit")
		io.WriteString(w, "ok")
	}, nil)
	resp, err := f.client.Get(f.target + "/app")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 && resp.Header.Get(ResultHeader) != "" {
		t.Fatalf("target-controlled %s=%q passed through on a 200", ResultHeader, resp.Header.Get(ResultHeader))
	}
}

// R6: HEAD 응답의 Content-Length가 0으로 바뀐다.
func TestHeadContentLengthPreserved(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1234")
		w.WriteHeader(200)
	}, nil)
	resp, err := f.client.Head(f.target + "/app")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ContentLength != 1234 {
		t.Fatalf("HEAD Content-Length upstream=1234, delivered=%d", resp.ContentLength)
	}
}

// R7: IPv6 리터럴 authority에서 기본 형식 Host(포트 없음)는 같은 origin인데도 out_of_scope_origin으로 막힌다.
func TestIPv6HostWithoutPortAccepted(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	conn, err := net.Dial("tcp", f.proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "CONNECT [2001:db8::1]:443 HTTP/1.1\r\nHost: [2001:db8::1]:443\r\n\r\n")
	br := bufio.NewReader(conn)
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != 200 {
		t.Fatal(err)
	}
	tc := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}
	io.WriteString(tc, "GET /x HTTP/1.1\r\nHost: [2001:db8::1]\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get(DenialHeader) == ReasonOutOfScopeOrigin {
		t.Fatalf("Host [2001:db8::1] for tunnel [2001:db8::1]:443 judged as a different origin (%s)", ReasonOutOfScopeOrigin)
	}
}

// 확인용: 세션 마감(Manager.watch)으로 터널을 끊은 뒤 고루틴이 남지 않는다.
func TestNoGoroutineLeakAfterSessionClose(t *testing.T) {
	f := newProxyFixture(t, exploration.ReadOnly, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, nil)
	authority := fixtureHost + ":" + strconv.Itoa(int(f.port))
	m := &Manager{Proxies: []*Proxy{f.proxy}}
	go m.watch(f.session)
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	for i := 0; i < proxyMaxTunnels; i++ {
		tc, reader := rawTunnel(t, f, authority)
		io.WriteString(tc, "GET /app HTTP/1.1\r\nHost: "+authority+"\r\n\r\n")
		if resp, err := http.ReadResponse(reader, nil); err == nil {
			resp.Body.Close()
		}
	}
	mid := runtime.NumGoroutine()
	f.session.Close(t.Context(), exploration.Completed)
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("goroutines before=%d with tunnels=%d after close=%d tunnels=%d", before, mid, runtime.NumGoroutine(), f.proxy.Tunnels())
	if runtime.NumGoroutine() > before || f.proxy.Tunnels() != 0 {
		t.Fatal("goroutines or tunnels left after session close")
	}
}
