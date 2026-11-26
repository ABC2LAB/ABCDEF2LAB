package e2proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPObservationBoundedIndependentProjection(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "Caddy/2.8")
		w.Header().Set("X-Powered-By", "Bearer private-value")
		w.Write([]byte("private-body"))
	}))
	defer target.Close()
	_, c := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 40))
	for i := 0; i < 33; i++ {
		resp, _, err := c.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed/private-id?token=private-query"))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	result, err := c.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 32 || !result.ObservationsTruncated {
		t.Fatal("observation ceiling lost")
	}
	first := result.Observations[0]
	if first.Endpoint != "/allowed/{resource}" || first.ExactFunction || first.StatusCode != 200 || !first.AuditComplete || len(first.Technologies) != 1 {
		t.Fatalf("wrong projection %+v", first)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"private-id", "private-query", "private-cookie", "private-body", "private-value"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private value in projection")
		}
	}
	result.Observations[0].Technologies[0].Name = "changed"
	if c.Result().Observations[0].Technologies[0].Name != "Caddy" {
		t.Fatal("snapshot alias")
	}
}

func TestHTTPObservationExactFunctionExcludesQueryAndResource(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("synthetic"))
	}))
	defer target.Close()
	_, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 3))
	for _, path := range []string{"/allowed", "/allowed?mode=other", "/allowed/other"} {
		resp, _, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+path))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	result, err := call.Close(context.Background())
	if err != nil || len(result.Observations) != 3 {
		t.Fatalf("observations %d err=%v", len(result.Observations), err)
	}
	if !result.Observations[0].ExactFunction || result.Observations[1].ExactFunction || result.Observations[2].ExactFunction {
		t.Fatalf("exact-function projection: %+v", result.Observations)
	}
}

func TestHTTPObservationHTMLSecurityHeaderProjection(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		headers           []string
		want              string
	}{
		{"missing", "text/html; charset=utf-8", nil, "nosniff_missing"},
		{"present", "text/html", []string{"nosniff"}, "nosniff_present"},
		{"not-html", "application/json", nil, ""},
		{"ambiguous", "text/html", []string{"nosniff", "invalid"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				for _, value := range tc.headers {
					w.Header().Add("X-Content-Type-Options", value)
				}
				_, _ = w.Write([]byte("synthetic private-value body"))
			}))
			defer target.Close()
			_, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 2))
			resp, _, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed/document"))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			result, err := call.Close(context.Background())
			if err != nil || len(result.Observations) != 1 {
				t.Fatal(err)
			}
			if got := result.Observations[0].HTMLSecurity; got != tc.want {
				t.Fatalf("projection %q", got)
			}
			raw, _ := json.Marshal(result.Observations[0])
			if strings.Contains(string(raw), "private-value") {
				t.Fatal("secret leaked into projection")
			}
		})
	}
}

func TestHTTPObservationDoesNotChooseOneOfMultipleServerBanners(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Server", "Example/1.0")
		w.Header().Add("Server", "Other/2.0")
		_, _ = w.Write([]byte("synthetic response"))
	}))
	defer target.Close()
	_, call := coreOpen(t, coreConfig(t, target.URL, &coreRecorder{}, 2))
	resp, _, err := call.Do(context.Background(), coreRequest(t, "GET", target.URL+"/allowed/document"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	result, err := call.Close(context.Background())
	if err != nil || len(result.Observations) != 1 || len(result.Observations[0].Technologies) != 0 {
		t.Fatalf("ambiguous banner trusted: err=%v observations=%d", err, len(result.Observations))
	}
}
