//go:build linux

package exploreboundary

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/pathmask"
)

// meterSeen은 계측기(cmd/eval-meter proxy.go)와 같은 방식으로 받은 요청의 경로를 가린다:
// pathmask.Mask(r.URL.EscapedPath(), key). 받은 요청 대상이 origin-form인지도 남긴다.
type meterSeen struct {
	mu       sync.Mutex
	patterns []string
	forms    []string
	escaped  []string
}

func (m *meterSeen) handler(key []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pattern, _ := pathmask.Mask(r.URL.EscapedPath(), key)
		m.mu.Lock()
		m.patterns = append(m.patterns, r.Method+" "+pattern)
		m.forms = append(m.forms, r.RequestURI)
		m.escaped = append(m.escaped, r.URL.EscapedPath())
		m.mu.Unlock()
		io.WriteString(w, "ok")
	}
}

// 계측기 방식으로 가린 패턴과 경계 원자료의 패턴이 같아야 P03 (메서드, 가린 경로) 대조가 성립한다. 경계에 들어온 요청 대상이
// absolute-form(http 대상 평문 프록시)이든 터널 안 origin-form(https 대상)이든, 경계는 전달할 때 origin-form으로 보내고
// 두 쪽의 EscapedPath가 같아야 한다. 대상 쪽 경로는 경계가 % 인코딩을 받지 않으므로(canonicalPath), Go가 RawPath를 따로
// 들고 다니는 sub-delims·이메일·ID·끝 '/'를 본다.
var p03Paths = []string{
	"/app/items", "/app/items/", "/app/items/12345", "/app/users/alice@example.com", "/app/o/123e4567-e89b-12d3-a456-426614174000",
	"/app/p;matrix=1", "/app/a!b(c)*'", "/app/a+b,c$d&e=f:g", "/app/~user", "/app/MixedCase", "/app/static/app.min.js",
}

func TestEvidencePathPatternMatchesMeterMasking(t *testing.T) {
	for _, mode := range []string{"absolute_form_http_target", "origin_form_https_tunnel"} {
		t.Run(mode, func(t *testing.T) {
			key := evidenceKey(t)
			dir := evidenceDir(t)
			ev, err := OpenEvidence(dir, evidenceTestMaxBytes, key)
			if err != nil {
				t.Fatal(err)
			}
			seen := &meterSeen{}
			upstreamCA, mitm := newTestCA(t, "upstream-ca"), newTestCA(t, "boundary-ca")
			upstream := httptest.NewUnstartedServer(seen.handler(key))
			spki := ""
			if mode == "origin_form_https_tunnel" {
				leaf, pin := upstreamCA.leaf(t, fixtureHost)
				spki = pin
				upstream.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
				upstream.StartTLS()
			} else {
				upstream.Start()
			}
			t.Cleanup(upstream.Close)
			_, portText, _ := net.SplitHostPort(upstream.Listener.Addr().String())
			port, _ := strconv.ParseUint(portText, 10, 16)
			target := Target{Ref: "target-1", Revision: 2, Host: fixtureHost, Port: uint16(port), PathPrefixes: []string{"/app"}, Methods: []string{"GET", "HEAD"},
				ConnectIP: netip.MustParseAddr("127.0.0.1"), SPKISHA256: spki, ExpiresAt: time.Now().Add(time.Hour)}
			if spki == "" {
				target.Scheme = "http"
			}
			policy := Policy{Resolver: fakeResolver{}, MaxDeniedRecords: fixtureDenied, Now: time.Now, Evidence: ev,
				PathPattern: func(escaped string) string { masked, _ := pathmask.Mask(escaped, key); return masked }}
			binding := fixtureBinding(exploration.ReadOnly, exploration.FullLocal)
			// 경로 수만큼 보내야 하므로 대상 요청 상한을 넉넉히 둔다(fixture 상한 3은 상한 시험용).
			binding.Limits.MaxRequests = uint64(2 * len(p03Paths))
			session, err := Open(t.Context(), binding, fixtureDigest, &fakeSource{target: target}, policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := ev.opened(session.flow, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			proxy, err := NewProxy(ProxyConfig{Ingress: DirectIngress, Current: func() *Session { return session }, CA: mitm.cert, CAKey: mitm.key, Roots: upstreamCA.pool,
				MaxRequestBytes: proxyMaxBody, MaxResponseBytes: proxyMaxResponse, MaxTunnels: proxyMaxTunnels, Timeout: proxyTimeout, LeafValidity: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			front := httptest.NewServer(proxy)
			t.Cleanup(front.Close)
			frontURL, _ := url.Parse(front.URL)
			authority := fixtureHost + ":" + portText
			for _, path := range append(append([]string{}, p03Paths...), "/app/%41bc") {
				var target string
				var conn io.ReadWriter
				if spki == "" {
					// absolute-form: 클라이언트가 경계에 절대 URI로 보낸다.
					raw, err := net.DialTimeout("tcp", frontURL.Host, proxyTimeout)
					if err != nil {
						t.Fatal(err)
					}
					defer raw.Close()
					conn, target = raw, "http://"+authority+path
				} else {
					f := &proxyFixture{proxyURL: frontURL, mitm: mitm}
					tc, _ := rawTunnel(t, f, authority)
					conn, target = tc, path
				}
				io.WriteString(conn, "GET "+target+" HTTP/1.1\r\nHost: "+authority+"\r\nConnection: close\r\n\r\n")
				resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					t.Fatal(path, err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				// % 인코딩 경로는 경계가 승인 대상으로 보내지 않는다. 그래서 계측기와 인코딩이 갈릴 경로가 전달되지 않는다.
				if path == "/app/%41bc" {
					if resp.StatusCode != http.StatusForbidden {
						t.Fatal("percent-encoded path reached the target", resp.StatusCode)
					}
				} else if resp.StatusCode != http.StatusOK {
					t.Fatal("approved path not forwarded", path, resp.StatusCode)
				}
			}
			m := &Manager{Policy: session.policy, CloseWait: time.Second}
			m.current = session
			if err := m.Seal(SealedByShutdown, nil); err != nil {
				t.Fatal(err)
			}
			rows, _ := readRows(t, dir)
			var boundary []string
			for _, r := range rows {
				if r.Decision == "forwarded" && r.Dest == DestApproved {
					boundary = append(boundary, r.Method+" "+r.PathPattern)
				}
			}
			seen.mu.Lock()
			defer seen.mu.Unlock()
			sort.Strings(boundary)
			meter := append([]string{}, seen.patterns...)
			sort.Strings(meter)
			if len(boundary) != len(p03Paths) || len(meter) != len(p03Paths) {
				t.Fatalf("forwarded %d boundary rows, %d meter rows, want %d", len(boundary), len(meter), len(p03Paths))
			}
			for i := range boundary {
				if boundary[i] != meter[i] {
					t.Fatalf("boundary pattern %q != meter pattern %q", boundary[i], meter[i])
				}
			}
			for _, form := range seen.forms {
				if form == "" || form[0] != '/' {
					t.Fatal("upstream did not receive origin-form", form)
				}
			}
			// 가린 패턴은 이스케이프에 민감한 조각을 {id}로 바꿔 차이를 숨길 수 있다. 그래서 계측기 쪽이 받은 EscapedPath
			// 원문이 경계에 들어온 경로(경계가 가린 값의 원문)와 같은지도 본다.
			got := append([]string{}, seen.escaped...)
			want := append([]string{}, p03Paths...)
			sort.Strings(got)
			sort.Strings(want)
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("upstream EscapedPath %q, boundary saw %q", got[i], want[i])
				}
			}
		})
	}
}
