package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 자격 헤더(Authorization · Cookie)가 실린 요청만 센다. 다른 헤더 · 헤더 없는 요청은 세지 않는다.
func TestCountCredentialHeaders(t *testing.T) {
	credentialHeaderRequests.Store(0)
	for _, header := range []map[string]string{{}, {"Accept": "text/html"}, {"Authorization": "Bearer x"}, {"Cookie": "a=b"}, {"Authorization": "", "Cookie": ""}} {
		r := httptest.NewRequest(http.MethodGet, "/rest/products/search", nil)
		for k, v := range header {
			r.Header[k] = []string{v}
		}
		countCredentialHeaders(r)
	}
	rec := httptest.NewRecorder()
	fixtureHeaderStats().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/header-stats", nil))
	var out map[string]int64
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil || out["credential_header_requests"] != 3 {
		t.Fatalf("credential header count: %s", rec.Body)
	}
}
