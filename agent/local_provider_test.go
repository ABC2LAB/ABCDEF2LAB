package agent

import (
	"context"
	"errors"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

const localProviderSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"fixture\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func localProviderPolicy(t *testing.T, origin string) *llmpolicy.Policy {
	t.Helper()
	parsed, err := url.Parse(origin)
	if err != nil {
		t.Fatalf("parse fixture origin: %v", err)
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	if err != nil {
		address = netip.MustParseAddr("127.0.0.1")
	}
	policy, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: origin, Addresses: []netip.Addr{address}}})
	if err != nil {
		t.Fatalf("local policy: %v", err)
	}
	return policy
}

func runLocalProviderStream(t *testing.T, provider llm.Provider, ctx context.Context) (string, string) {
	t.Helper()
	ctx, capture := llmrec.NewCapture(ctx)
	request := llm.CompletionRequest{
		Messages:  []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "fixture"}}}},
		MaxTokens: 16,
	}
	var text strings.Builder
	for event, err := range provider.Stream(ctx, request) {
		if err != nil {
			t.Fatalf("local provider stream: %v", err)
		}
		if event.Type == llm.SETextDelta {
			text.WriteString(event.Text)
		}
	}
	return text.String(), capture.RawRequest()
}

func TestNewLocalProviderRejectsProxyAndMissingBaseURL(t *testing.T) {
	if _, err := (Config{BaseURL: "http://127.0.0.1", Proxy: "http://127.0.0.1:1"}).NewLocalProvider(nil, llmpolicy.ClientOptions{}); err != ErrLocalProviderProxy {
		t.Fatalf("proxy error=%v, want fixed local-provider proxy error", err)
	}
	if _, err := (Config{}).NewLocalProvider(nil, llmpolicy.ClientOptions{}); err != ErrLocalProviderBaseURL {
		t.Fatalf("empty base URL error=%v, want fixed local-provider base URL error", err)
	}
}

func TestNewLocalProviderMasksFactoryErrors(t *testing.T) {
	address := netip.MustParseAddr("127.0.0.1")
	policy, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: "http://127.0.0.1:1", Addresses: []netip.Addr{address}}})
	if err != nil {
		t.Fatalf("local policy: %v", err)
	}
	_, err = (Config{Format: llm.Format("fixture-unsupported"), BaseURL: "http://127.0.0.1:1"}).NewLocalProvider(policy, llmpolicy.ClientOptions{})
	if err != ErrLocalProvider {
		t.Fatalf("factory error=%v, want fixed local error", err)
	}
}

func TestLocalProviderAllowedRequestPreservesWireCaptureAndSession(t *testing.T) {
	var hits atomic.Int32
	var gotHost, gotSession, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotHost = r.Host
		gotSession = r.Header.Get("X-Fixture-Session")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, localProviderSSE)
	}))
	defer srv.Close()

	provider, err := (Config{
		Format:           llm.FormatAnthropic,
		BaseURL:          srv.URL,
		APIKey:           "fixture-key",
		Model:            "fixture-model",
		SessionHeaderKey: "X-Fixture-Session",
	}).NewLocalProvider(localProviderPolicy(t, srv.URL), llmpolicy.ClientOptions{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}

	ctx := transcript.WithSessionID(context.Background(), "fixture-session")
	text, captured := runLocalProviderStream(t, provider, ctx)
	if text != "ok" {
		t.Fatalf("stream text=%q, want ok", text)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits=%d, want 1", got)
	}
	if gotHost != strings.TrimPrefix(srv.URL, "http://") {
		t.Fatalf("Host=%q, want original authority", gotHost)
	}
	if gotSession != "fixture-session" {
		t.Fatalf("session header=%q, want fixture-session", gotSession)
	}
	if captured == "" || captured != gotBody {
		t.Fatalf("captured request did not equal wire body")
	}
}

func TestLocalProviderDeniedOriginDoesNotTransmit(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	registered := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer registered.Close()

	provider, err := (Config{Format: llm.FormatAnthropic, BaseURL: srv.URL, APIKey: "fixture-key", Model: "fixture-model"}).NewLocalProvider(localProviderPolicy(t, registered.URL), llmpolicy.ClientOptions{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	ctx := context.Background()
	request := llm.CompletionRequest{Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "fixture"}}}}, MaxTokens: 8}
	for _, err := range provider.Stream(ctx, request) {
		if err == nil {
			continue
		}
		break
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("denied server received %d requests", got)
	}
}

func TestLocalProviderCrossOriginRedirectDoesNotTransmit(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()

	provider, err := (Config{Format: llm.FormatAnthropic, BaseURL: origin.URL, APIKey: "fixture-key", Model: "fixture-model"}).NewLocalProvider(localProviderPolicy(t, origin.URL), llmpolicy.ClientOptions{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	for range provider.Stream(context.Background(), llm.CompletionRequest{Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "fixture"}}}}, MaxTokens: 8}) {
	}
	if got := redirected.Load(); got != 0 {
		t.Fatalf("cross-origin redirect transmitted %d requests", got)
	}
}

func TestLocalProviderMasksProviderErrorsAndBodies(t *testing.T) {
	const canary = "fixture-provider-error-canary"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, canary)
	}))
	defer srv.Close()
	provider, err := (Config{
		Format:  llm.FormatAnthropic,
		BaseURL: srv.URL,
		APIKey:  "fixture-key",
		Model:   "fixture-model",
		Retry:   RetryConfig{ConnectAttempts: -1},
	}).NewLocalProvider(localProviderPolicy(t, srv.URL), llmpolicy.ClientOptions{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	for _, err := range provider.Stream(context.Background(), localProviderRequest()) {
		if err == nil {
			continue
		}
		if err != ErrLocalProvider {
			t.Fatalf("provider error=%v, want fixed local error", err)
		}
		if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), srv.URL) {
			t.Fatalf("provider error leaked sensitive details: %v", err)
		}
		return
	}
	t.Fatal("provider returned no error")
}

func TestLocalProviderMapsQuotaErrorWithoutCause(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "insufficient_quota fixture-provider-error-canary")
	}))
	defer srv.Close()
	provider, err := (Config{
		Format:  llm.FormatAnthropic,
		BaseURL: srv.URL,
		APIKey:  "fixture-key",
		Model:   "fixture-model",
		Retry:   RetryConfig{ConnectAttempts: -1},
	}).NewLocalProvider(localProviderPolicy(t, srv.URL), llmpolicy.ClientOptions{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	for _, err := range provider.Stream(context.Background(), localProviderRequest()) {
		if err == nil {
			continue
		}
		if err != ErrLocalProviderQuota || errors.Unwrap(err) != nil {
			t.Fatalf("quota error=%v, want fixed quota sentinel without cause", err)
		}
		return
	}
	t.Fatal("provider returned no quota error")
}

func TestLocalProviderContextCancellationReturnsContextError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("cancelled provider request reached server")
	}))
	defer srv.Close()
	provider, err := (Config{Format: llm.FormatAnthropic, BaseURL: srv.URL, APIKey: "fixture-key", Model: "fixture-model"}).NewLocalProvider(localProviderPolicy(t, srv.URL), llmpolicy.ClientOptions{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range provider.Stream(ctx, localProviderRequest()) {
		if err == nil {
			continue
		}
		if err != context.Canceled {
			t.Fatalf("cancel error=%v, want context.Canceled", err)
		}
		return
	}
	t.Fatal("provider returned no cancellation error")
}

type localProviderErrorStub struct{ err error }

func (s localProviderErrorStub) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, s.err)
	}
}

func (s localProviderErrorStub) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "fixture"}}}, "fixture-stop", llm.Usage{}, s.err
}

func TestLocalProviderCompleteMasksURLAndResponseBody(t *testing.T) {
	const canary = "fixture-complete-error-canary"
	provider := localProvider{inner: localProviderErrorStub{err: errors.New("POST https://model.invalid/" + canary + ": response body " + canary)}}
	message, stop, usage, err := provider.Complete(context.Background(), localProviderRequest())
	if err != ErrLocalProvider {
		t.Fatalf("complete error=%v, want fixed local error", err)
	}
	if strings.Contains(err.Error(), canary) || message.Content != nil || stop != "" || usage != (llm.Usage{}) {
		t.Fatalf("complete leaked partial result or error details")
	}
}

// 정책 거부는 할당량 문구보다 앞서고, 원문 없이 고정 정책 분류로 남는다. 예산이 일시 실패와 구별할 수 있어야 한다.
func TestLocalProviderBlockedErrorWinsOverQuotaText(t *testing.T) {
	err := errors.Join(llmpolicy.ErrRequestBlocked, errors.New("insufficient_quota fixture-error-canary"))
	got := localProviderError(context.Background(), err)
	if got != ErrLocalProviderPolicy || !errors.Is(got, llmpolicy.ErrRequestBlocked) || !errors.Is(got, ErrLocalProvider) ||
		errors.Unwrap(got) != nil || got.Error() != ErrLocalProvider.Error() || strings.Contains(got.Error(), "canary") {
		t.Fatalf("blocked error=%v, want fixed policy classification", got)
	}
	// 정책과 무관한 전송 실패는 정책 분류가 아니다.
	if got := localProviderError(context.Background(), llmpolicy.ErrTransport); got != ErrLocalProvider || errors.Is(got, llmpolicy.ErrRequestBlocked) {
		t.Fatalf("transport error=%v, want generic local error", got)
	}
}

func newIPv4LocalProviderServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		srv.Close()
		t.Fatalf("listen fixture server: %v", err)
	}
	srv.Listener = listener
	srv.Start()
	return srv
}

func TestLocalProviderRetryBlockedFreshResolutionDoesNotTransmitAgain(t *testing.T) {
	var hits atomic.Int32
	server := newIPv4LocalProviderServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "fixture transient")
	}))
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	origin := "http://model.internal:" + port
	var lookups atomic.Int32
	lookup := func(context.Context, string) ([]netip.Addr, error) {
		if lookups.Add(1) <= 2 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.2")}, nil
	}
	provider, err := (Config{Format: llm.FormatAnthropic, BaseURL: origin, APIKey: "fixture-key", Model: "fixture-model", Retry: RetryConfig{ConnectAttempts: 1, ConnectInterval: time.Millisecond, EmptyAttempts: -1}}).NewLocalProvider(localProviderPolicy(t, origin), llmpolicy.ClientOptions{LookupIP: lookup})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	for range provider.Stream(context.Background(), localProviderRequest()) {
	}
	if hits.Load() != 1 || lookups.Load() != 3 {
		t.Fatalf("retry transmitted after changed resolution: hits=%d lookups=%d", hits.Load(), lookups.Load())
	}
}

func TestLocalProviderRetryFreshResolutionAllowsStableEndpoint(t *testing.T) {
	var hits atomic.Int32
	server := newIPv4LocalProviderServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, localProviderSSE)
	}))
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	origin := "http://model.internal:" + port
	var lookups atomic.Int32
	lookup := func(context.Context, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	provider, err := (Config{Format: llm.FormatAnthropic, BaseURL: origin, APIKey: "fixture-key", Model: "fixture-model", Retry: RetryConfig{ConnectAttempts: 1, ConnectInterval: time.Millisecond, EmptyAttempts: -1}}).NewLocalProvider(localProviderPolicy(t, origin), llmpolicy.ClientOptions{LookupIP: lookup})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	text, _ := runLocalProviderStream(t, provider, context.Background())
	if text != "ok" || hits.Load() != 2 || lookups.Load() != 4 {
		t.Fatalf("stable retry mismatch: text=%q hits=%d lookups=%d", text, hits.Load(), lookups.Load())
	}
}

func localProviderRequest() llm.CompletionRequest {
	return llm.CompletionRequest{
		Messages:  []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "fixture"}}}},
		MaxTokens: 8,
	}
}
