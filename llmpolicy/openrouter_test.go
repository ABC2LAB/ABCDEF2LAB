package llmpolicy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openRouterFixture(t *testing.T) *OpenRouterPolicy {
	t.Helper()
	m := openRouterManifest{Version: 1, ProfileID: 7, Format: "openai", BaseURL: OpenRouterBaseURL, Model: OpenRouterModel,
		ProviderSlug: "fixture/endpoint", ExpectedProviderName: "Fixture", ApprovalID: "fixture-approval",
		ExpiresAt: time.Now().Add(time.Hour), StrictPrivacyAck: true, AccountSpendCapAck: true,
		MaxRequests: 3, MaxInputTokens: 3 * openRouterContext, MaxOutputTokens: 300, MaxCostMicros: 1000000,
		MaxInputPerCall: openRouterContext, MaxOutputPerCall: 100, TimeoutSeconds: 2, MaxConcurrent: 1, InputPrice: 1000000, OutputPrice: 1000000}
	raw, _ := json.Marshal(m)
	p, err := ParseOpenRouterManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	p.ledger = func(int64, int64, int64) error { return nil }
	return p
}

type openRouterRT func(*http.Request) (*http.Response, error)

func (f openRouterRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const openRouterReply = `{"model":"qwen/qwen3.6-35b-a3b","provider":"Fixture","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"cost":0.000012}}`

func openRouterResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func openRouterRequest() *http.Request {
	r, _ := http.NewRequest("POST", openRouterEndpoint, strings.NewReader(`{"model":"qwen/qwen3.6-35b-a3b","messages":[{"role":"user","content":"synthetic"}],"max_tokens":100,"stream":false}`))
	return r
}
func TestOpenRouterWirePrivacyBudget(t *testing.T) {
	p := openRouterFixture(t)
	var hits int
	var reserved int64
	c := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
		hits++
		raw, _ := io.ReadAll(r.Body)
		reserved += int64(len(raw)) + openRouterFramingTokens
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		routing := body["provider"].(map[string]any)
		if routing["allow_fallbacks"] != false || routing["require_parameters"] != true || routing["data_collection"] != "deny" || routing["zdr"] != true ||
			routing["only"].([]any)[0] != "fixture/endpoint" {
			t.Fatal("routing/privacy not pinned")
		}
		return openRouterResponse(openRouterReply), nil
	}))
	for i := 0; i < 3; i++ {
		resp, err := c.Do(openRouterRequest())
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if _, err := c.Do(openRouterRequest()); err == nil {
		t.Fatal("exhausted request accepted")
	}
	u := p.Snapshot()
	if hits != 3 || u.Requests != 3 || u.InputTokens != 30 || u.OutputTokens != 6 || u.ReservedInputTokens != reserved {
		t.Fatalf("incorrect aggregates: %+v", u)
	}
}
func TestOpenRouterRejectsUnsafeRequests(t *testing.T) {
	for _, change := range []func(*http.Request){
		func(r *http.Request) { r.URL.Scheme = "http" }, func(r *http.Request) { r.URL.Host = "evil.invalid" },
		func(r *http.Request) { r.URL.RawQuery = "leak=1" }, func(r *http.Request) { r.URL.Path = "/api/v1/models" },
		func(r *http.Request) { r.Host = "evil.invalid" }, func(r *http.Request) { r.Method = "GET" },
		func(r *http.Request) { r.Header["hOsT"] = []string{"evil.invalid"} },
		func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(`{"model":"other","messages":[],"max_tokens":1}`))
		},
		func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(`{"model":"qwen/qwen3.6-35b-a3b","messages":[{"content":"x"}],"max_tokens":101}`))
		},
		func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(`{"model":"qwen/qwen3.6-35b-a3b","messages":[{"content":"x"}],"max_tokens":1,"stream":true}`))
		},
	} {
		p := openRouterFixture(t)
		c := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected wire send"); return nil, nil }))
		r := openRouterRequest()
		change(r)
		if _, err := c.Do(r); err == nil {
			t.Fatal("unsafe request accepted")
		}
		if p.Snapshot().Requests != 0 {
			t.Fatal("blocked prewire request consumed budget")
		}
	}
}
func TestOpenRouterUnknownAccountingHaltsAllClients(t *testing.T) {
	for _, body := range []string{
		strings.Replace(openRouterReply, `"cost":0.000012`, `"other":0`, 1),
		strings.Replace(openRouterReply, `"Fixture"`, `"Fallback"`, 1),
		strings.Replace(openRouterReply, `"completion_tokens":2`, `"completion_tokens":101`, 1),
		strings.Replace(openRouterReply, `"cost":0.000012`, `"cost":99`, 1),
	} {
		p := openRouterFixture(t)
		var hits int
		var wire int64
		rt := openRouterRT(func(r *http.Request) (*http.Response, error) {
			hits++
			wire = r.ContentLength
			return openRouterResponse(body), nil
		})
		if _, err := openRouterClient(p, rt).Do(openRouterRequest()); err == nil {
			t.Fatal("invalid response accepted")
		}
		if _, err := openRouterClient(p, rt).Do(openRouterRequest()); err == nil {
			t.Fatal("ambiguous usage retried")
		}
		if hits != 1 || !p.Snapshot().Blocked || p.Snapshot().ReservedInputTokens != wire+openRouterFramingTokens {
			t.Fatal("reservation not retained")
		}
	}
}
func TestOpenRouterConcurrentBudget(t *testing.T) {
	p := openRouterFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		close(started)
		<-release
		return openRouterResponse(openRouterReply), nil
	}))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := c.Do(openRouterRequest())
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	if _, err := c.Do(openRouterRequest()); err == nil {
		t.Fatal("concurrent request escaped cap")
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("unexpected wire count")
	}
}
func TestOpenRouterManifestAndPersistence(t *testing.T) {
	p := openRouterFixture(t)
	raw, _ := json.Marshal(p.manifest)
	for _, bad := range [][]byte{
		bytes.Replace(raw, []byte(`"strict_privacy_ack":true`), []byte(`"strict_privacy_ack":false`), 1),
		bytes.Replace(raw, []byte(`"format":"openai"`), []byte(`"format":"openai","format":"openai"`), 1),
		bytes.Replace(raw, []byte(`"profile_id"`), []byte(`"PROFILE_ID"`), 1),
		append(append([]byte{}, raw...), []byte("{}")...),
	} {
		if _, err := ParseOpenRouterManifest(bad); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
	pure, _ := ParseOpenRouterManifest(raw)
	if _, err := NewOpenRouterClient(pure); err == nil {
		t.Fatal("unprotected parser enabled sending")
	}
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	path := filepath.Join(dir, "approval.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// 승인 총량(요청 3회)은 재시작·다른 프로세스를 합쳐 누적 기록으로 지킨다(P3 결정안 C07).
	first, err := LoadOpenRouterManifestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = first.reserve(100); err != nil {
			t.Fatal(err)
		}
		first.active--
	}
	second, err := LoadOpenRouterManifestFile(path)
	if err != nil || second.CheckAvailable() != nil {
		t.Fatal("approval with remaining budget rejected after restart")
	}
	if err = second.reserve(100); err != nil {
		t.Fatal("restart lost the remaining approved budget")
	}
	second.active--
	third, err := LoadOpenRouterManifestFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if third.CheckAvailable() == nil {
		t.Fatal("exhausted approval reported available")
	}
	if err = third.reserve(100); err == nil || !third.Snapshot().Blocked || third.Snapshot().FailureReason != "approval_consumed" {
		t.Fatal("process restart reset approved budget")
	}
	if err = first.reserve(100); err == nil {
		t.Fatal("first process exceeded the shared approval total")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadOpenRouterManifestFile(path); err == nil {
		t.Fatal("public manifest accepted")
	}
}
func TestOpenRouterPublicAddresses(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "::ffff:8.8.8.8", "2001:db8::1", "64:ff9b::a00:1"} {
		if openRouterPublic(netip.MustParseAddr(s)) {
			t.Fatal("nonpublic allowed", s)
		}
	}
	if !openRouterPublic(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public address blocked")
	}
}

func TestOpenRouterWireToolValidation(t *testing.T) {
	defs := []byte(`[{"function":{"name":"fixture"}}]`)
	good := `{"choices":[{"message":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"fixture","arguments":"{\"ok\":true}"}}]},"finish_reason":"tool_calls"}]}`
	if !validOpenRouterWireTools([]byte(good), defs) {
		t.Fatal("valid tool rejected")
	}
	for _, bad := range []string{
		strings.Replace(good, `"call-1"`, `""`, 1),
		strings.Replace(good, `"fixture"`, `"unknown"`, 1),
		strings.Replace(good, `{\"ok\":true}`, `{\"ok\":true,\"ok\":false}`, 1),
		strings.Replace(good, `{\"ok\":true}`, `[]`, 1),
		strings.Replace(good, `{\"ok\":true}`, ``, 1),
		strings.Replace(good, `"finish_reason":"tool_calls"`, `"finish_reason":"length"`, 1),
		strings.Replace(good, `"choices":`, `"error":{"message":"sensitive"},"choices":`, 1),
	} {
		if validOpenRouterWireTools([]byte(bad), defs) {
			t.Fatal("invalid raw tool accepted")
		}
	}
	if validOpenRouterJSON([]byte(`{"usage":{"cost":1,"cost":0}}`)) {
		t.Fatal("duplicate response key accepted")
	}
}
func TestOpenRouterBudgetReasons(t *testing.T) {
	cases := []struct {
		name   string
		change func(*OpenRouterPolicy)
	}{
		{"expired", func(p *OpenRouterPolicy) { p.manifest.ExpiresAt = time.Now().Add(-time.Second) }},
		{"request_limit", func(p *OpenRouterPolicy) { p.usage.Requests = p.manifest.MaxRequests }},
		{"token_limit", func(p *OpenRouterPolicy) { p.manifest.MaxInputTokens = 1 }},
		{"cost_limit", func(p *OpenRouterPolicy) { p.manifest.MaxCostMicros = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := openRouterFixture(t)
			tc.change(p)
			if p.reserve(10) == nil || p.Snapshot().FailureReason != tc.name {
				t.Fatal("limit not enforced", p.Snapshot())
			}
		})
	}
	p := openRouterFixture(t)
	if p.ValidateCallLimits(0, 262) == nil || p.ValidateCallLimits(101, 262) == nil || p.ValidateCallLimits(10, 263) == nil {
		t.Fatal("invalid profile caps allowed")
	}
	if p.ValidateCallLimits(10, 262) != nil {
		t.Fatal("valid caps rejected")
	}
}
func TestOpenRouterRedirectFailsWithoutFollowup(t *testing.T) {
	p := openRouterFixture(t)
	hits := 0
	c := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
		hits++
		r := openRouterResponse("")
		r.StatusCode = 302
		r.Header.Set("Location", "https://evil.invalid/leak")
		return r, nil
	}))
	if _, err := c.Do(openRouterRequest()); err == nil || hits != 1 || !p.Snapshot().Blocked {
		t.Fatal("redirect accepted")
	}
}

type openRouterPeerConn struct {
	net.Conn
	peer *net.TCPAddr
}

func (c openRouterPeerConn) RemoteAddr() net.Addr { return c.peer }
func TestOpenRouterDNSAndPeerPinning(t *testing.T) {
	for _, tc := range []struct {
		name             string
		ips              []netip.Addr
		peer             string
		wantDial, wantOK bool
	}{
		{"public", []netip.Addr{netip.MustParseAddr("8.8.8.8")}, "8.8.8.8", true, true},
		{"mixed", []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, "8.8.8.8", false, false},
		{"peer_mismatch", []netip.Addr{netip.MustParseAddr("8.8.8.8")}, "127.0.0.1", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			conn, err := openRouterDialWith(context.Background(), "tcp", "openrouter.ai:443",
				func(context.Context) ([]netip.Addr, error) { return tc.ips, nil },
				func(_ context.Context, network, address string) (net.Conn, error) {
					called = true
					if network != "tcp" || address != "8.8.8.8:443" {
						t.Fatal("connection did not use checked IP")
					}
					a, b := net.Pipe()
					t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
					return openRouterPeerConn{Conn: a, peer: &net.TCPAddr{IP: net.ParseIP(tc.peer), Port: 443}}, nil
				})
			if called != tc.wantDial || (err == nil) != tc.wantOK {
				t.Fatal("DNS/peer decision mismatch")
			}
			if conn != nil {
				_ = conn.Close()
			}
		})
	}
}
func TestOpenRouterClientHasNoProxyOrTLSBypass(t *testing.T) {
	p := openRouterFixture(t)
	client, err := NewOpenRouterClient(p)
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*openRouterTransport).base.(*http.Transport)
	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.TLSClientConfig.InsecureSkipVerify ||
		transport.TLSClientConfig.MinVersion < tls.VersionTLS12 || transport.TLSClientConfig.RootCAs != nil || transport.DialTLSContext != nil {
		t.Fatal("unsafe transport option")
	}
	if client.CheckRedirect(openRouterRequest(), nil) == nil {
		t.Fatal("redirect allowed")
	}
}

func TestOpenRouterHTTPFailuresNeverRetry(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 503} {
		p := openRouterFixture(t)
		hits := 0
		client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
			hits++
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("synthetic failure body")), Header: make(http.Header)}, nil
		}))
		for i := 0; i < 2; i++ {
			if _, err := client.Do(openRouterRequest()); err == nil {
				t.Fatal("failure response accepted")
			}
		}
		if hits != 1 || p.Snapshot().Requests != 1 || !p.Snapshot().Blocked {
			t.Fatal("failure retried or accounting reset")
		}
	}
}
func TestOpenRouterTLSFailureDoesNotReachHandler(t *testing.T) {
	var hits atomic.Int32
	local := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(200) }))
	defer local.Close()
	base := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "openrouter.ai"}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", local.Listener.Addr().String())
	}}
	defer base.CloseIdleConnections()
	p := openRouterFixture(t)
	if _, err := openRouterClient(p, base).Do(openRouterRequest()); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	if hits.Load() != 0 || !p.Snapshot().Blocked {
		t.Fatal("TLS failure reached HTTP or permitted later send")
	}
}
func TestOpenRouterCancellationStopsNewAttempts(t *testing.T) {
	p := openRouterFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	hits := 0
	client := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
		hits++
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	done := make(chan error, 1)
	go func() { _, err := client.Do(openRouterRequest().WithContext(ctx)); done <- err }()
	<-started
	cancel()
	if <-done == nil {
		t.Fatal("cancellation reported success")
	}
	if _, err := client.Do(openRouterRequest()); err == nil || hits != 1 {
		t.Fatal("new send after cancelled unknown response")
	}
}

func TestOpenRouterExpiredManifestDescriptionCannotAuthorizeCalls(t *testing.T) {
	p := openRouterFixture(t)
	p.manifest.ExpiresAt = time.Now().Add(-time.Minute)
	raw, _ := json.Marshal(p.manifest)
	if _, err := ParseOpenRouterManifest(raw); err == nil {
		t.Fatal("expired callable policy")
	}
	target, limits, quote, err := DescribeOpenRouterManifest(raw, 100)
	if err != nil || target != p.Target() || limits.Provider != "fixture/endpoint" || quote != 262244 {
		t.Fatal("saved-report metadata unavailable")
	}
	if _, err := p.Quote(100); err == nil {
		t.Fatal("expired call quote")
	}
}

// 입력 예약은 입력 상한 전체가 아니라 요청 크기의 상한이다(P3 결정안 C28 결정 1). 같은 승인 총량으로 작은 요청을 더
// 보낼 수 있고, 누적 기록에도 같은 예약이 남는다. 보고된 입력·비용이 이 예약을 넘으면 상한 가정이 깨진 것이라 막는다.
func TestOpenRouterReservesRequestSize(t *testing.T) {
	fixture := func(t *testing.T, ledger *[]int64) *OpenRouterPolicy {
		m := openRouterManifest{Version: 1, ProfileID: 7, Format: "openai", BaseURL: OpenRouterBaseURL, Model: OpenRouterModel,
			ProviderSlug: "fixture/endpoint", ExpectedProviderName: "Fixture", ApprovalID: "fixture-approval",
			ExpiresAt: time.Now().Add(time.Hour), StrictPrivacyAck: true, AccountSpendCapAck: true,
			MaxRequests: 3, MaxInputTokens: openRouterContext, MaxOutputTokens: 300, MaxCostMicros: 1000000,
			MaxInputPerCall: openRouterContext, MaxOutputPerCall: 100, TimeoutSeconds: 2, MaxConcurrent: 1, InputPrice: 1000000, OutputPrice: 1000000}
		raw, _ := json.Marshal(m)
		p, err := ParseOpenRouterManifest(raw)
		if err != nil {
			t.Fatal(err)
		}
		p.ledger = func(input, _, _ int64) error { *ledger = append(*ledger, input); return nil }
		return p
	}
	t.Run("small-calls-fit-one-context", func(t *testing.T) {
		var ledger []int64
		p := fixture(t, &ledger)
		var wire []int64
		c := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
			wire = append(wire, r.ContentLength)
			return openRouterResponse(openRouterReply), nil
		}))
		// 승인 입력 총량이 입력 상한 하나뿐이어도 작은 요청 세 개가 들어간다(전체 예약이면 두 번째에서 막힌다).
		for i := 0; i < 3; i++ {
			resp, err := c.Do(openRouterRequest())
			if err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
			_ = resp.Body.Close()
		}
		u := p.Snapshot()
		var want int64
		for i, n := range wire {
			if ledger[i] != n+openRouterFramingTokens {
				t.Fatalf("ledger input %d != wire %d + framing", ledger[i], n)
			}
			want += n + openRouterFramingTokens
		}
		if len(wire) != 3 || len(ledger) != 3 || u.ReservedInputTokens != want || u.ReservedCostMicros != want+300 || u.Blocked {
			t.Fatalf("reservation: wire=%v ledger=%v usage=%+v", wire, ledger, u)
		}
	})
	for name, reply := range map[string]string{
		// 입력 상한 안이지만 이 요청 크기 예약을 넘는 보고 입력과 비용.
		"input-over-reservation": strings.Replace(openRouterReply, `"prompt_tokens":10`, `"prompt_tokens":200000`, 1),
		"cost-over-reservation":  strings.Replace(openRouterReply, `"cost":0.000012`, `"cost":0.01`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var ledger []int64
			p := fixture(t, &ledger)
			hits := 0
			rt := openRouterRT(func(*http.Request) (*http.Response, error) { hits++; return openRouterResponse(reply), nil })
			if _, err := openRouterClient(p, rt).Do(openRouterRequest()); err == nil {
				t.Fatal("usage beyond the request-size reservation accepted")
			}
			if _, err := openRouterClient(p, rt).Do(openRouterRequest()); err == nil || hits != 1 || !p.Snapshot().Blocked {
				t.Fatal("broken reservation bound did not block")
			}
		})
	}
}

// 입력 예약은 0보다 크고 입력 상한 이하여야 한다. transport는 openRouterReservedInput으로만 부르지만, 다른 호출자가
// 상한 밖 값을 넘겨도 예산·누적 기록에 남기지 않고 거부한다.
func TestOpenRouterReserveRejectsInputOutsideCallLimit(t *testing.T) {
	for _, input := range []int64{0, -1, openRouterContext + 1} {
		p := openRouterFixture(t)
		called := false
		p.ledger = func(int64, int64, int64) error { called = true; return nil }
		reason, err := p.reserveDiagnostic(input, 100)
		if err == nil || reason != "token_limit" || called || p.Snapshot().Requests != 0 || p.Snapshot().ReservedInputTokens != 0 {
			t.Fatalf("input %d: reason=%q ledger=%v usage=%+v", input, reason, called, p.Snapshot())
		}
	}
	if got := openRouterReservedInput(openRouterFixture(t).manifest, 1<<20); got != openRouterContext {
		t.Fatalf("large request reservation %d", got)
	}
}

// 입력 예약 기준 견적은 그 입력·출력의 비용 상한이고, 호출당 상한 밖이나 만료된 승인은 견적하지 않는다(C28 결정 1).
func TestOpenRouterQuoteInput(t *testing.T) {
	p := openRouterFixture(t)
	m := p.manifest
	full, err := p.Quote(10)
	if err != nil {
		t.Fatal(err)
	}
	small, err := p.QuoteInput(5000, 10)
	if err != nil || small != openRouterCallCost(m, 5000, 10) || small >= full {
		t.Fatalf("request-size quote=%d full=%d err=%v", small, full, err)
	}
	if top, err := p.QuoteInput(m.MaxInputPerCall, 10); err != nil || top != full {
		t.Fatal("quote at the per-call input limit differs from the full quote")
	}
	for _, c := range [][2]int64{{0, 10}, {m.MaxInputPerCall + 1, 10}, {5000, 0}, {5000, m.MaxOutputPerCall + 1}} {
		if _, err := p.QuoteInput(c[0], c[1]); err == nil {
			t.Fatalf("out-of-range quote accepted: %v", c)
		}
	}
	p.manifest.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := p.QuoteInput(5000, 10); err == nil {
		t.Fatal("expired approval quoted")
	}
}
