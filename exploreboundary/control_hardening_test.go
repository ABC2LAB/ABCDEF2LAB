package exploreboundary

// 리뷰(#488) 재현용 제어 소켓 시험.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
)

func reviewManager(t *testing.T) *Manager {
	t.Helper()
	_, source, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	source.target.ExpiresAt = time.Now().Add(time.Hour)
	m := &Manager{Policy: Policy{Resolver: fakeResolver{}, PathPattern: func(p string) string { return p }, MaxDeniedRecords: fixtureDenied}, CloseWait: time.Second}
	b := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
	wire := WireTargetOf(source.target)
	if resp := m.Handle(context.Background(), ControlRequest{Op: OpOpen, Binding: &b, SourceDigest: fixtureDigest, Target: &wire}); !resp.OK {
		t.Fatal("open", resp)
	}
	return m
}

func pipeCall(t *testing.T, m *Manager, line string) ControlResponse {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	go m.serveConn(context.Background(), b, func(net.Conn) bool { return true })
	a.SetDeadline(time.Now().Add(2 * time.Second))
	go io.WriteString(a, line+"\n")
	out, err := readLine(bufio.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	var resp ControlResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// R8: "엄격 JSON"이 대소문자 다른 키·중복 키·뒤따르는 ']'/'}'를 받는다.
func TestControlJSONStrict(t *testing.T) {
	m := reviewManager(t)
	accepted := []string{}
	for _, line := range []string{
		`{"OP":"status","SESSION_REF":"session-1"}`,
		`{"op":"close","op":"status","session_ref":"x","session_ref":"session-1"}`,
		`{"op":"status","session_ref":"session-1"}]`,
		`{"op":"status","session_ref":"session-1"}}`,
	} {
		if resp := pipeCall(t, m, line); resp.OK {
			accepted = append(accepted, line)
		}
	}
	if len(accepted) > 0 {
		t.Fatalf("non-strict control lines accepted: %q", accepted)
	}
}

// R9: 직접 입구 도구가 Sec-Fetch-Dest에 큰 값을 넣으면 기록 응답이 제어 줄 상한을 넘어 records를 읽을 수 없다.
func TestOversizedFetchHeadersKeepRecordsReadable(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamHits++ }))
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	_, portText, _ := net.SplitHostPort(upstream.Listener.Addr().String())
	port, _ := strconv.ParseUint(portText, 10, 16)
	const denied = 256
	s, _, _ := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, func(target *Target, p *Policy) {
		target.Port, target.ConnectIP = uint16(port), netip.MustParseAddr("127.0.0.1")
		p.Now, p.MaxDeniedRecords = time.Now, denied
		target.ExpiresAt = time.Now().Add(time.Hour)
	})
	mitm := newTestCA(t, "boundary-ca")
	p, err := NewProxy(ProxyConfig{Ingress: DirectIngress, Current: func() *Session { return s }, CA: mitm.cert, CAKey: mitm.key,
		MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxTunnels: 4, Timeout: 5 * time.Second, LeafValidity: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)
	u, _ := url.Parse(front.URL)
	authority := fixtureHost + ":" + portText
	tc, reader := rawTunnel(t, &proxyFixture{proxyURL: u, mitm: mitm}, authority)
	big := strings.Repeat("a", 60000)
	for i := 0; i < 80; i++ {
		io.WriteString(tc, "GET /outside HTTP/1.1\r\nHost: "+authority+"\r\nSec-Fetch-Dest: "+big+"\r\n\r\n")
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	m := &Manager{current: s}
	resp := m.Handle(context.Background(), ControlRequest{Op: OpRecords, SessionRef: s.binding.SessionRef})
	raw, _ := json.Marshal(resp)
	_, readErr := readLine(bufio.NewReader(bytes.NewReader(append(raw, '\n'))))
	t.Logf("records=%d line=%d bytes (limit %d) readLine err=%v upstreamHits=%d", len(resp.Records), len(raw), maxControlLine, readErr, upstreamHits)
	if readErr != nil {
		t.Fatal("explorer-controlled header values made the records response exceed the control line limit; the client cannot read any record")
	}
}
