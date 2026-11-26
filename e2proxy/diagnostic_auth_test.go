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

func TestSelectedSessionInjectionUsesOnlyBoundProtectedAccount(t *testing.T) {
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "Bearer synthetic-a" || r.Header.Get("Cookie") != "" {
			t.Errorf("selected target received an unexpected account context")
		}
		_, _ = io.WriteString(w, "synthetic response")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 3)
	cfg.HeaderMode = HeaderTestAccountPassthrough
	cfg.SelectedAccountRef = strings.Repeat("a", 64)
	cfg.ResolveSentAccount = func(header http.Header) string {
		if header.Get("Authorization") == "Bearer synthetic-a" && header.Get("Cookie") == "" {
			return cfg.SelectedAccountRef
		}
		return ""
	}
	cfg.SelectedSessionHeaders = func() (http.Header, bool) {
		return http.Header{"Authorization": {"Bearer synthetic-a"}}, true
	}
	_, call := coreOpen(t, cfg)
	for _, supplied := range []http.Header{
		{"Authorization": {"Bearer synthetic-b"}},
		{"Cookie": {"sid=synthetic-b"}},
	} {
		request := coreRequest(t, "GET", target.URL+"/allowed")
		request.Header = supplied
		if response, receipt, err := call.Do(t.Context(), request); err == nil || response != nil || receipt.ForwardAttempted {
			t.Fatalf("caller credential reached target: %+v %v", receipt, err)
		}
	}
	request := coreRequest(t, "GET", target.URL+"/allowed")
	response, receipt, err := call.Do(context.Background(), request)
	if err != nil || response == nil || !receipt.AuditComplete || hits != 1 {
		t.Fatalf("protected account send failed: %+v %v hits=%d", receipt, err, hits)
	}
	response.Body.Close()
	observations := call.Result().Observations
	if len(observations) != 3 || observations[2].RequestAccountRef != cfg.SelectedAccountRef ||
		observations[2].RequestAuthContext != "authorization" {
		t.Fatalf("actual sent identity was not observed: %+v", observations)
	}
	encoded, _ := json.Marshal(observations)
	if strings.Contains(string(encoded), "synthetic-a") || strings.Contains(string(encoded), "synthetic-b") {
		t.Fatal("a synthetic credential entered stored observation")
	}
}

func TestExactQueryAndHeadersCannotExpandApprovedRead(t *testing.T) {
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.RawQuery != "mode=review" || r.Header.Get("Accept") != "application/json" {
			t.Error("sent request did not match the pinned input")
		}
		_, _ = io.WriteString(w, "{}")
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 3)
	cfg.Rules[0].PathPrefix = "/allowed"
	cfg.Rules[0].PathMatch = "exact"
	cfg.Rules[0].ExactQuery = "mode=review"
	cfg.Rules[0].StrictHeaders = true
	cfg.Rules[0].Headers = http.Header{"Accept": {"application/json"}}
	_, call := coreOpen(t, cfg)
	for _, tc := range []struct {
		path, accept string
		allowed      bool
	}{
		{"/allowed?mode=other", "application/json", false},
		{"/allowed?mode=review", "text/html", false},
		{"/allowed?mode=review", "application/json", true},
	} {
		request := coreRequest(t, "GET", target.URL+tc.path)
		request.Header.Set("Accept", tc.accept)
		response, receipt, err := call.Do(t.Context(), request)
		if tc.allowed {
			if err != nil || response == nil || !receipt.AuditComplete {
				t.Fatalf("approved read failed: %+v %v", receipt, err)
			}
			response.Body.Close()
		} else if err == nil || response != nil || receipt.ForwardAttempted {
			t.Fatalf("changed input reached target: %+v %v", receipt, err)
		}
	}
	extra := coreRequest(t, "GET", target.URL+"/allowed?mode=review")
	extra.Header.Set("Accept", "application/json")
	extra.Header.Set("X-Extra", "changed")
	if response, receipt, err := call.Do(t.Context(), extra); err == nil || response != nil || receipt.ForwardAttempted {
		t.Fatalf("unapproved header reached target: %+v %v", receipt, err)
	}
	if hits != 1 {
		t.Fatalf("sent %d requests, want one", hits)
	}
	observation := call.Result().Observations[2]
	if !observation.ExactFunction || len(observation.RequestQueryDigest) != 64 || len(observation.RequestHeaderDigest) != 64 {
		t.Fatalf("approved input digest missing: %+v", observation)
	}
	encoded, _ := json.Marshal(observation)
	if strings.Contains(string(encoded), "mode=review") || strings.Contains(string(encoded), "application/json") {
		t.Fatal("request input was copied to ordinary observation")
	}
}

func TestRequestAuthContextKeepsOnlyCredentialHeaderPresence(t *testing.T) {
	if got := requestAuthContext(http.Header{"X-Auth-Token": {"synthetic-secret"}}, "X-Auth-Token"); got != "custom" {
		t.Fatal("custom approved credential header was classified anonymous", got)
	}
	for _, tc := range []struct {
		headers http.Header
		want    string
	}{
		{http.Header{}, "none"},
		{http.Header{"Authorization": {"Bearer synthetic-secret"}}, "authorization"},
		{http.Header{"Cookie": {"sid=synthetic-secret"}}, "cookie"},
		{http.Header{"Authorization": {"Bearer synthetic-secret"}, "Cookie": {"sid=synthetic-secret"}}, "authorization_and_cookie"},
		{http.Header{"X-Api-Key": {"synthetic-secret"}}, "unknown"},
		{http.Header{"Authorization": {"Bearer synthetic-secret"}, "X-Api-Key": {"synthetic-secret"}}, "unknown"},
		{http.Header{"Accept": {"application/json"}}, "none"},
	} {
		got := requestAuthContext(tc.headers)
		if got != tc.want {
			t.Fatalf("context=%q want %q", got, tc.want)
		}
		encoded, err := json.Marshal(HTTPObservation{RequestAuthContext: got})
		if err != nil || strings.Contains(string(encoded), "synthetic-secret") || strings.Contains(string(encoded), "sid") {
			t.Fatalf("credential leaked into observation projection: %s %v", encoded, err)
		}
	}
}

func TestDiagnosticMediaClassNeverCopiesTargetHeader(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"application/json; charset=utf-8", "json"},
		{"text/html; secret=synthetic-private", "html"},
		{"application/x-synthetic-private", "other"},
		{"broken; synthetic-private", "unknown"},
	} {
		if got := diagnosticMediaClass(tc.raw); got != tc.want || strings.Contains(got, "synthetic-private") {
			t.Fatalf("media projection=%q want %q", got, tc.want)
		}
	}
}
