package exploreboundary

import (
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/pathmask"
)

// drainHeldWindow는 봉인이 핸들러를 기다리는지 보는 동안이다. 고치기 전 봉인은 이 안에 바로 끝났다.
const drainHeldWindow = 200 * time.Millisecond

// heldIngress는 실제 입구 서버(http.Server + ConnState 추적)에 원자료를 켠 세션과 Proxy를 붙인다. 핸들러는 Proxy에
// 넘기기 전에 release를 기다려, 송신 ticket 전 단계에 머문 요청을 만든다.
type heldIngress struct {
	dir     string
	session *Session
	proxy   *Proxy
	server  *http.Server
	conns   *IngressConns
	addr    string
	entered chan struct{}
	release chan struct{}
}

func newHeldIngress(t *testing.T) *heldIngress {
	t.Helper()
	key, dir := evidenceKey(t), evidenceDir(t)
	ev, err := OpenEvidence(dir, evidenceTestMaxBytes, key)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Ref: "target-1", Revision: 2, Scheme: "http", Host: fixtureHost, Port: 18080, PathPrefixes: []string{"/app"}, Methods: []string{"GET", "HEAD", "POST"},
		ConnectIP: netip.MustParseAddr("127.0.0.1"), ExpiresAt: time.Now().Add(time.Hour)}
	policy := Policy{Resolver: fakeResolver{}, MaxDeniedRecords: fixtureDenied, Now: time.Now, Evidence: ev,
		PathPattern: func(escaped string) string { masked, _ := pathmask.Mask(escaped, key); return masked }}
	session, err := Open(t.Context(), fixtureBinding(exploration.ApprovedWrites, exploration.FullLocal), fixtureDigest, &fakeSource{target: target}, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := ev.opened(session.flow, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	mitm := newTestCA(t, "boundary-ca")
	proxy, err := NewProxy(ProxyConfig{Ingress: DirectIngress, Current: func() *Session { return session }, CA: mitm.cert, CAKey: mitm.key, Roots: mitm.pool,
		MaxRequestBytes: proxyMaxBody, MaxResponseBytes: proxyMaxResponse, MaxTunnels: proxyMaxTunnels, Timeout: proxyTimeout, LeafValidity: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	h := &heldIngress{dir: dir, session: session, proxy: proxy, conns: NewIngressConns(), entered: make(chan struct{}, 1), release: make(chan struct{})}
	h.server = &http.Server{ConnState: h.conns.ConnState, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.entered <- struct{}{}
		<-h.release
		proxy.ServeHTTP(w, r)
	})}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = l.Addr().String()
	go func() { _ = h.conns.Serve(h.server, l) }()
	t.Cleanup(func() { _ = h.server.Close() })
	return h
}

// sendHeld는 승인 대상 http POST를 absolute-form으로 보내고, 핸들러가 Proxy에 넘기기 전에 멈춘 것을 확인한다.
func (h *heldIngress) sendHeld(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", h.addr, proxyTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	body := "a=1"
	if _, err := io.WriteString(conn, "POST http://"+fixtureHost+":18080/app/x HTTP/1.1\r\nHost: "+fixtureHost+":18080\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 3\r\n\r\n"+body); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.entered:
	case <-time.After(proxyTimeout):
		t.Fatal("request never reached the ingress handler")
	}
}

// shutdown은 cmd/explore-boundary의 내리기 순서(입구 Close → 터널 닫기 → 드레인을 넘긴 봉인)를 그대로 따른다.
func (h *heldIngress) shutdown(closeWait time.Duration) <-chan error {
	_ = h.server.Close()
	h.proxy.CloseTunnels()
	m := &Manager{Policy: h.session.policy, CloseWait: closeWait}
	m.current = h.session
	done := make(chan error, 1)
	go func() {
		done <- m.Seal(SealedByShutdown, func(wait time.Duration) int { return Drain(wait, h.conns, h.proxy) })
	}()
	return done
}

// 송신 ticket 전에 머문 실제 HTTP 핸들러가 있으면 봉인은 그 핸들러가 행을 남기고 끝날 때까지 기다린다. 그 요청은
// 세션이 닫힌 뒤 처리되므로 대상에 가지 않고 차단 행으로 남으며, 메모리 기록과 원자료 행 수가 같다(#615 리뷰).
func TestSealDrainsPreAdmissionHandlers(t *testing.T) {
	h := newHeldIngress(t)
	h.sendHeld(t)
	done := h.shutdown(5 * time.Second)
	select {
	case err := <-done:
		t.Fatal("sealed while a pre-admission handler could still record", err)
	case <-time.After(drainHeldWindow):
	}
	close(h.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(proxyTimeout):
		t.Fatal("seal did not finish after the handler returned")
	}
	marker := readMarker(t, h.dir)
	rows, _ := readRows(t, h.dir)
	h.session.mu.Lock()
	memory := len(h.session.records)
	h.session.mu.Unlock()
	if !marker.LogComplete || marker.InflightAtSeal != 0 || marker.Rows != uint64(len(rows)) || len(rows) != memory || memory != 1 {
		t.Fatalf("marker %+v rows %d memory %d", marker, len(rows), memory)
	}
	if r := rows[0]; r.Decision != "denied" || r.Ingress != DirectIngress || r.Method != http.MethodPost {
		t.Fatalf("held request row %+v", r)
	}
}

// 드레인 상한 안에 끝나지 않은 입구 처리는 봉인 표식의 inflight_at_seal로 남아, 원자료가 완전하다고 표시되지 않는다.
func TestSealMarksUndrainedHandlersIncomplete(t *testing.T) {
	h := newHeldIngress(t)
	h.sendHeld(t)
	done := h.shutdown(drainHeldWindow)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(proxyTimeout):
		t.Fatal("seal did not stop waiting at close_wait")
	}
	close(h.release)
	if marker := readMarker(t, h.dir); marker.InflightAtSeal < 1 {
		t.Fatalf("undrained handler not counted: %+v", marker)
	}
}
