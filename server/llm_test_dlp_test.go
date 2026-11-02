package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const llmTestDLPSentinel = "llm-test-synthetic-secret"

func callLLMTestForDLP(t *testing.T, s *Server, raw string) (int, []byte, string) {
	t.Helper()
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })

	r := httptest.NewRequest(http.MethodPost, "/api/llm/test", strings.NewReader(raw))
	w := httptest.NewRecorder()
	s.testLLM(w, r)
	return w.Code, w.Body.Bytes(), logs.String()
}

func assertLLMTestDLP(t *testing.T, sentinel string, body []byte, logs string) {
	t.Helper()
	if bytes.Contains(body, []byte(sentinel)) {
		t.Fatalf("response contains sensitive fixture (length=%d)", len(body))
	}
	if strings.Contains(logs, sentinel) {
		t.Fatalf("logs contain sensitive fixture (length=%d)", len(logs))
	}
}

func anthropicJSONProvider(t *testing.T, status int, response string, delay time.Duration) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(delay)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	})), &calls
}

func anthropicSSEProvider(t *testing.T, response string, delay time.Duration) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	const prefix = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"
	const suffix = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(delay)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, prefix+"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\""+response+"\"}}\n\n"+suffix)
	})), &calls
}

func TestLLMTestDLPNonStreamingSuccess(t *testing.T) {
	provider, calls := anthropicJSONProvider(t, http.StatusOK, `{"type":"message","content":[{"type":"text","text":"`+llmTestDLPSentinel+`"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, 8*time.Millisecond)
	defer provider.Close()

	baseURL, _ := url.Parse(provider.URL)
	baseURL.User = url.UserPassword(llmTestDLPSentinel, llmTestDLPSentinel)
	query := baseURL.Query()
	query.Set("q", llmTestDLPSentinel)
	baseURL.RawQuery = query.Encode()
	raw, _ := json.Marshal(map[string]any{
		"provider": "anthropic", "model": llmTestDLPSentinel,
		"base_url": baseURL.String(), "api_key": llmTestDLPSentinel,
		"streaming": false,
	})
	status, body, logs := callLLMTestForDLP(t, &Server{}, string(raw))
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got["ok"] != true || got["model"] != "[REDACTED]" || got["reply"] != "[REDACTED]" {
		t.Fatalf("unexpected success envelope (key_count=%d)", len(got))
	}
	lat, ok := got["latency_ms"].(float64)
	if !ok || lat < 8 {
		t.Fatalf("success latency below delay (latency_ms=%d)", int64(lat))
	}
	if !strings.Contains(logs, "http_status=200") || !strings.Contains(logs, "response_bytes=") || !strings.Contains(logs, "failed=false") {
		t.Fatalf("success log missing required fields (log_length=%d)", len(logs))
	}
	if !strings.Contains(logs, "latency_ms="+strconv.FormatInt(int64(lat), 10)+" ") {
		t.Fatalf("log latency does not match API latency (latency_ms=%d)", int64(lat))
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("provider calls=%d, want 1", atomic.LoadInt32(calls))
	}
	assertLLMTestDLP(t, llmTestDLPSentinel, body, logs)
}

func TestLLMTestDLPStreamingSuccess(t *testing.T) {
	provider, calls := anthropicSSEProvider(t, llmTestDLPSentinel, 8*time.Millisecond)
	defer provider.Close()
	raw, _ := json.Marshal(map[string]any{
		"provider": "anthropic", "model": llmTestDLPSentinel,
		"base_url": provider.URL, "api_key": llmTestDLPSentinel, "streaming": true,
	})
	status, body, logs := callLLMTestForDLP(t, &Server{}, string(raw))
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got["ok"] != true || got["model"] != "[REDACTED]" || got["reply"] != "[REDACTED]" {
		t.Fatalf("unexpected success envelope (key_count=%d)", len(got))
	}
	lat, ok := got["latency_ms"].(float64)
	if !ok || lat < 8 {
		t.Fatalf("stream latency below delay (latency_ms=%d)", int64(lat))
	}
	if !strings.Contains(logs, "http_status=200") || !strings.Contains(logs, "failed=false") {
		t.Fatalf("stream log missing required fields (log_length=%d)", len(logs))
	}
	if !strings.Contains(logs, "latency_ms="+strconv.FormatInt(int64(lat), 10)+" ") {
		t.Fatalf("stream log latency mismatch (latency_ms=%d)", int64(lat))
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("provider calls=%d, want 1", atomic.LoadInt32(calls))
	}
	assertLLMTestDLP(t, llmTestDLPSentinel, body, logs)
}

func TestLLMTestDLPProviderFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":{"type":"authentication_error","message":"` + llmTestDLPSentinel + `"}}`, "http_status=401"},
		{"insufficient quota", http.StatusTooManyRequests, `{"type":"error","error":{"type":"insufficient_quota","message":"` + llmTestDLPSentinel + `"}}`, "http_status=429"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, calls := anthropicJSONProvider(t, tt.status, tt.body, 8*time.Millisecond)
			defer provider.Close()
			raw, _ := json.Marshal(map[string]any{
				"provider": "anthropic", "model": llmTestDLPSentinel,
				"base_url": provider.URL, "api_key": llmTestDLPSentinel, "streaming": false,
			})
			status, response, logs := callLLMTestForDLP(t, &Server{}, string(raw))
			if status != http.StatusOK {
				t.Fatalf("status=%d", status)
			}
			var got map[string]any
			if err := json.Unmarshal(response, &got); err != nil {
				t.Fatalf("response is not JSON: %v", err)
			}
			if got["ok"] != false || got["error"] == llmTestDLPSentinel {
				t.Fatalf("unsafe failure envelope (key_count=%d)", len(got))
			}
			lat, ok := got["latency_ms"].(float64)
			if !ok || lat < 8 {
				t.Fatalf("failure latency below delay (latency_ms=%d)", int64(lat))
			}
			if !strings.Contains(logs, tt.want) || !strings.Contains(logs, "failed=true") || !strings.Contains(logs, "response_bytes=") {
				t.Fatalf("failure log missing required fields (log_length=%d)", len(logs))
			}
			if !strings.Contains(logs, "latency_ms="+strconv.FormatInt(int64(lat), 10)+" ") {
				t.Fatalf("failure log latency mismatch (latency_ms=%d)", int64(lat))
			}
			if tt.status == http.StatusTooManyRequests && !strings.Contains(logs, "http_status=429") {
				t.Fatal("quota log lost raw HTTP status")
			}
			if atomic.LoadInt32(calls) < 1 {
				t.Fatal("provider was not called")
			}
			assertLLMTestDLP(t, llmTestDLPSentinel, response, logs)
		})
	}
}

func TestLLMTestDLPEmptyAndBadProxy(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{"empty response", "empty"},
		{"bad proxy", "proxy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, calls := anthropicJSONProvider(t, http.StatusOK, "", 0)
			defer provider.Close()
			baseURL := provider.URL
			request := map[string]any{"provider": "anthropic", "model": llmTestDLPSentinel, "base_url": baseURL, "api_key": llmTestDLPSentinel, "streaming": false}
			if tt.field == "proxy" {
				request["proxy"] = "://" + llmTestDLPSentinel
			}
			raw, _ := json.Marshal(request)
			status, response, logs := callLLMTestForDLP(t, &Server{}, string(raw))
			if status != http.StatusOK {
				t.Fatalf("status=%d", status)
			}
			var got map[string]any
			if err := json.Unmarshal(response, &got); err != nil {
				t.Fatalf("response is not JSON: %v", err)
			}
			if got["ok"] != false {
				t.Fatalf("expected failed test (key_count=%d)", len(got))
			}
			assertLLMTestDLP(t, llmTestDLPSentinel, response, logs)
			if tt.field == "proxy" && calls != nil && atomic.LoadInt32(calls) != 0 {
				t.Fatalf("bad proxy unexpectedly reached provider")
			}
		})
	}
}

func TestLLMTestDLPMalformedAndNoAPIKey(t *testing.T) {
	for _, raw := range []string{
		`{"provider":"anthropic","model":"` + llmTestDLPSentinel + `","api_key":"` + llmTestDLPSentinel,
		`{"provider":"anthropic","model":"` + llmTestDLPSentinel + `","profile_id":987654321098765432109876543210,"api_key":"` + llmTestDLPSentinel + `"}`,
	} {
		status, body, logs := callLLMTestForDLP(t, &Server{}, raw)
		if status != http.StatusBadRequest {
			t.Fatalf("malformed request status=%d", status)
		}
		assertLLMTestDLP(t, llmTestDLPSentinel, body, logs)
		if bytes.Contains(body, []byte("987654321098765432109876543210")) || strings.Contains(logs, "987654321098765432109876543210") {
			t.Fatal("oversized profile_id leaked")
		}
	}

	status, body, logs := callLLMTestForDLP(t, &Server{}, `{}`)
	if status != http.StatusOK {
		t.Fatalf("no-key status=%d", status)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got["error"] != errLLMTestNoAPIKey {
		t.Fatalf("no-key error did not use the fixed message")
	}
	assertLLMTestDLP(t, llmTestDLPSentinel, body, logs)
}
