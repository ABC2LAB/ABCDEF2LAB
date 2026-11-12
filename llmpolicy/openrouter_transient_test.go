package llmpolicy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func openRouterV2Fixture(t *testing.T, limit int64) *OpenRouterPolicy {
	t.Helper()
	m := openRouterManifest{Version: 2, ProfileID: 7, Format: "openai", BaseURL: OpenRouterBaseURL, Model: OpenRouterModel,
		ProviderSlug: "fixture/endpoint", ExpectedProviderName: "Fixture", ApprovalID: "fixture-approval-v2",
		ExpiresAt: time.Now().Add(time.Hour), StrictPrivacyAck: true, AccountSpendCapAck: true,
		MaxRequests: 6, MaxInputTokens: 6 * openRouterContext, MaxOutputTokens: 600, MaxCostMicros: 2000000,
		MaxInputPerCall: openRouterContext, MaxOutputPerCall: 100, TimeoutSeconds: 2, MaxConcurrent: 1, InputPrice: 1000000, OutputPrice: 1000000,
		MaxConsecutiveTransient: limit}
	raw, _ := json.Marshal(m)
	p, err := ParseOpenRouterManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	p.consume = func() error { return nil }
	return p
}

func TestOpenRouterManifestTransientLimitByVersion(t *testing.T) {
	base := map[string]any{}
	raw, _ := json.Marshal(openRouterV2Fixture(t, 2).manifest)
	_ = json.Unmarshal(raw, &base)
	for name, tc := range map[string]struct {
		version any
		limit   any
		ok      bool
	}{
		"v2":              {2, 3, true},
		"v2-max":          {2, openRouterMaxConsecutiveTransient, true},
		"v2-missing":      {2, nil, false},
		"v2-zero":         {2, 0, false},
		"v2-over":         {2, openRouterMaxConsecutiveTransient + 1, false},
		"v1-with-limit":   {1, 2, false},
		"v1-without":      {1, nil, true},
		"unknown-version": {3, 2, false},
	} {
		t.Run(name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range base {
				m[k] = v
			}
			m["version"] = tc.version
			delete(m, "max_consecutive_transient_errors")
			if tc.limit != nil {
				m["max_consecutive_transient_errors"] = tc.limit
			}
			data, _ := json.Marshal(m)
			p, err := ParseOpenRouterManifest(data)
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v", err == nil)
			}
			if tc.ok && tc.version == 1 && p.manifest.transientLimit() != 1 {
				t.Fatal("v1 limit is not the previous first-failure block")
			}
		})
	}
}

func openRouterStatus(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("synthetic failure body")), Header: make(http.Header)}
}

type openRouterBrokenBody struct{}

func (openRouterBrokenBody) Read([]byte) (int, error) {
	return 0, errors.New("synthetic connection reset")
}
func (openRouterBrokenBody) Close() error { return nil }

// 일시 오류(429·5xx·연결 실패·본문 읽기 끊김)는 그 호출만 실패하고, 연속 상한에 닿을 때 막는다. 정상 응답이 연속 수를
// 되돌린다. 예약은 되돌리지 않는다(P3 결정안 C06).
func TestOpenRouterTransientFailuresBlockOnlyAtConsecutiveLimit(t *testing.T) {
	for name, fail := range map[string]func(*http.Request) (*http.Response, error){
		"429":     func(*http.Request) (*http.Response, error) { return openRouterStatus(429), nil },
		"503":     func(*http.Request) (*http.Response, error) { return openRouterStatus(503), nil },
		"connect": func(*http.Request) (*http.Response, error) { return nil, errOpenRouterConnect },
		"timeout": func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded },
		"broken-body": func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: openRouterBrokenBody{}, Header: make(http.Header)}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := openRouterV2Fixture(t, 2)
			script := []bool{false, true, false, false} // 실패, 성공, 실패, 실패
			hits := 0
			client := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
				ok := script[hits]
				hits++
				if ok {
					return openRouterResponse(openRouterReply), nil
				}
				return fail(r)
			}))
			for i, ok := range script {
				resp, err := client.Do(openRouterRequest())
				if ok != (err == nil) {
					t.Fatalf("call %d err=%v", i, err)
				}
				if resp != nil {
					_ = resp.Body.Close()
				}
				if blocked := p.Snapshot().Blocked; blocked != (i == 3) {
					t.Fatalf("call %d blocked=%v", i, blocked)
				}
			}
			if _, err := client.Do(openRouterRequest()); err == nil || hits != 4 {
				t.Fatal("send after the consecutive limit")
			}
			u := p.Snapshot()
			if u.Requests != 4 || u.Errors != 3 || u.ReservedInputTokens != 4*openRouterContext || u.CostMicros != 12 {
				t.Fatalf("reservation refunded or usage lost: %+v", u)
			}
		})
	}
}

// 설정 오류(4xx)·응답 형식 위반·주소 정책 위반은 상한과 무관하게 첫 번에 막는다.
func TestOpenRouterConfigurationFailuresBlockImmediately(t *testing.T) {
	for name, fail := range map[string]func(*http.Request) (*http.Response, error){
		"401": func(*http.Request) (*http.Response, error) { return openRouterStatus(401), nil },
		"403": func(*http.Request) (*http.Response, error) { return openRouterStatus(403), nil },
		"400": func(*http.Request) (*http.Response, error) { return openRouterStatus(400), nil },
		"302": func(*http.Request) (*http.Response, error) { return openRouterStatus(302), nil },
		"accounting": func(*http.Request) (*http.Response, error) {
			return openRouterResponse(strings.Replace(openRouterReply, `"Fixture"`, `"Fallback"`, 1)), nil
		},
		"oversized":      func(*http.Request) (*http.Response, error) { return openRouterStatus(503), nil },
		"address-policy": func(*http.Request) (*http.Response, error) { return nil, errOpenRouterAddressPolicy },
	} {
		t.Run(name, func(t *testing.T) {
			p := openRouterV2Fixture(t, 3)
			hits := 0
			client := openRouterClient(p, openRouterRT(func(r *http.Request) (*http.Response, error) {
				hits++
				if name == "oversized" {
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", openRouterResponseLimit+1))), Header: make(http.Header)}, nil
				}
				return fail(r)
			}))
			if _, err := client.Do(openRouterRequest()); !errors.Is(err, ErrOpenRouterPolicy) {
				t.Fatal("failure accepted")
			}
			if !p.Snapshot().Blocked {
				t.Fatal("configuration failure counted as transient")
			}
			if _, err := client.Do(openRouterRequest()); err == nil || hits != 1 {
				t.Fatal("send after configuration failure")
			}
		})
	}
}

// TLS 인증서 검증 실패는 중간자 가능성이라 상한과 무관하게 막는다.
func TestOpenRouterTLSFailureBlocksDespiteTransientLimit(t *testing.T) {
	local := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer local.Close()
	base := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "openrouter.ai"}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", local.Listener.Addr().String())
	}}
	defer base.CloseIdleConnections()
	p := openRouterV2Fixture(t, 3)
	if _, err := openRouterClient(p, base).Do(openRouterRequest()); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	if !p.Snapshot().Blocked {
		t.Fatal("TLS verification failure counted as transient")
	}
}

// 다이얼 실패 분류: 주소 정책 위반은 막고, 이름 풀이·연결 실패는 일시 오류다. 실제 망에 나가지 않는다.
func TestOpenRouterDialFailureClassification(t *testing.T) {
	public := netip.MustParseAddr("8.8.8.8")
	ok := func(context.Context) ([]netip.Addr, error) { return []netip.Addr{public}, nil }
	noDial := func(context.Context, string, string) (net.Conn, error) { t.Fatal("dialled"); return nil, nil }
	failDial := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("synthetic refused") }
	for name, tc := range map[string]struct {
		network, address string
		lookup           func(context.Context) ([]netip.Addr, error)
		dial             func(context.Context, string, string) (net.Conn, error)
		want             error
	}{
		"wrong-address": {"tcp", "example.invalid:443", ok, noDial, errOpenRouterAddressPolicy},
		"private":       {"tcp", "openrouter.ai:443", func(context.Context) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil }, noDial, errOpenRouterAddressPolicy},
		"lookup-failed": {"tcp", "openrouter.ai:443", func(context.Context) ([]netip.Addr, error) { return nil, errors.New("synthetic dns") }, noDial, errOpenRouterConnect},
		"dial-failed":   {"tcp", "openrouter.ai:443", ok, failDial, errOpenRouterConnect},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := openRouterDialWith(context.Background(), tc.network, tc.address, tc.lookup, tc.dial)
			if !errors.Is(err, tc.want) || !errors.Is(err, ErrOpenRouterPolicy) {
				t.Fatalf("err=%v", err)
			}
			if openRouterTransientError(err) != (tc.want == errOpenRouterConnect) {
				t.Fatal("dial failure misclassified")
			}
		})
	}
}

// 영구 HTTP 상태는 본문이 끊겨도 일시 오류가 되지 않는다. 일시 상태(429·5xx)의 본문 끊김은 일시 오류다.
func TestOpenRouterBrokenBodyKeepsStatusClassification(t *testing.T) {
	for _, tc := range []struct {
		status  int
		blocked bool
	}{{400, true}, {401, true}, {403, true}, {302, true}, {200, false}, {429, false}, {503, false}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			p := openRouterV2Fixture(t, 3)
			client := openRouterClient(p, openRouterRT(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: openRouterBrokenBody{}, Header: make(http.Header)}, nil
			}))
			if _, err := client.Do(openRouterRequest()); err == nil {
				t.Fatal("broken body accepted")
			}
			if p.Snapshot().Blocked != tc.blocked {
				t.Fatalf("status %d with broken body blocked=%v", tc.status, p.Snapshot().Blocked)
			}
		})
	}
}
