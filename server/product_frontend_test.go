package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProductFrontendFixedAPIOrigin(t *testing.T) {
	for _, origin := range []string{"https://127.0.0.1:80", "http://localhost:80", "http://192.0.2.1:80", "http://u:p@127.0.0.1:80", "http://127.0.0.1:80/api", "http://127.0.0.1:80?x=1"} {
		if _, err := NewProductFrontend(origin); err == nil {
			t.Fatal("untrusted API origin accepted")
		}
	}
	var calls atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/product/health" || r.Header.Get("Authorization") != "Bearer owned-test" {
			t.Error("API request changed")
		}
		w.Write([]byte(`{"ready":true}`))
	}))
	defer api.Close()
	front, err := NewProductFrontend(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []*http.Request{httptest.NewRequest("CONNECT", "/api/product/health", nil), httptest.NewRequest("GET", "http://external.invalid/api/product/health", nil)} {
		w := httptest.NewRecorder()
		front.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatal("proxy request accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached backend")
	}
	r := httptest.NewRequest("GET", "/api/product/health", nil)
	r.Header.Set("Authorization", "Bearer owned-test")
	w := httptest.NewRecorder()
	front.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ready":true`) || calls.Load() != 1 {
		t.Fatal("fixed API forwarding failed")
	}
	api.Close()
	w = httptest.NewRecorder()
	front.ServeHTTP(w, r)
	if w.Code != 503 || w.Body.String() != "{\"error\":\"product_api_unavailable\"}\n" {
		t.Fatal("unsafe proxy failure")
	}
}
