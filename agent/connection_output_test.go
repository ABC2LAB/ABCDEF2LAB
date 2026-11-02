package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

func TestLLMConnectionLogsRetainMetadataWithoutPayloads(t *testing.T) {
	const secret = "synthetic-connection-private-value"
	cases := []struct {
		name      string
		status    int
		retry     bool
		quota     bool
		empty     bool
		canceled  bool
		badConfig bool
	}{
		{name: "success", status: 200}, {name: "authorization_error", status: 401},
		{name: "retry", status: 429, retry: true}, {name: "quota_rewrite", status: 429, quota: true},
		{name: "empty_reply", status: 200, empty: true}, {name: "canceled", canceled: true},
		{name: "invalid_proxy", badConfig: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previousWriter, previousFlags := log.Writer(), log.Flags()
			log.SetOutput(&logs)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags) })
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := requests.Add(1)
				status := tc.status
				if tc.retry && attempt > 1 {
					status = http.StatusOK
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status != http.StatusOK {
					message := secret
					if tc.quota {
						message = "insufficient_quota " + secret
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message}})
					return
				}
				text := secret
				if tc.empty {
					text = ""
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "fixture", "type": "message", "role": "assistant",
					"content":     []map[string]string{{"type": "text", "text": text}},
					"stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
				})
			}))
			defer srv.Close()
			cfg := Config{Format: llm.FormatAnthropic, BaseURL: srv.URL + "/" + secret, APIKey: secret, Model: secret,
				Retry: RetryConfig{ConnectAttempts: 1, ConnectInterval: time.Millisecond, EmptyAttempts: -1}}
			if tc.badConfig {
				cfg.Proxy = "unsupported://" + secret
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			lat, reply, err := TestConnection(ctx, cfg)
			if lat < 0 {
				t.Fatal("negative connection latency")
			}
			if tc.status == 200 && !tc.empty || tc.retry {
				if err != nil || reply != secret {
					t.Fatal("connection probe changed its internal provider result")
				}
			} else if err == nil {
				t.Fatal("failed connection was accepted")
			}
			output := logs.String()
			if strings.Contains(output, secret) || strings.Contains(output, srv.URL) || strings.Contains(output, "insufficient_quota") {
				t.Fatal("connection log contains private payload or configuration")
			}
			if !strings.Contains(output, "latency_ms=") {
				t.Fatal("connection log lost latency")
			}
			if tc.canceled || tc.badConfig {
				if requests.Load() != 0 || !strings.Contains(output, "attempts=0") {
					t.Fatal("no-request diagnostic changed")
				}
				return
			}
			if !strings.Contains(output, "response_bytes=") {
				t.Fatal("connection log lost response size")
			}
			if tc.retry {
				if requests.Load() != 2 || !strings.Contains(output, "http_status=429") || !strings.Contains(output, "http_status=200") {
					t.Fatal("retry status diagnostics changed")
				}
			} else if !strings.Contains(output, "http_status="+strconv.Itoa(tc.status)) {
				t.Fatal("connection log lost observed wire status")
			}
		})
	}
}

func TestLLMConnectionLegacyProviderFailureRemainsInternal(t *testing.T) {
	const secret = "synthetic-legacy-provider-error"
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, secret)
	}))
	defer srv.Close()
	_, _, err := TestConnection(t.Context(), Config{Format: llm.FormatAnthropic, BaseURL: srv.URL, APIKey: "fixture", Model: "fixture", Retry: RetryConfig{ConnectAttempts: -1}})
	if err == nil || !strings.Contains(err.Error(), secret) {
		t.Fatal("internal provider error contract changed")
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("provider error crossed logging boundary")
	}
}
