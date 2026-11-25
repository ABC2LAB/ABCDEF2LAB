package e2proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRequestAuthContextKeepsOnlyCredentialHeaderPresence(t *testing.T) {
	for _, tc := range []struct {
		headers http.Header
		want    string
	}{
		{http.Header{}, "none"},
		{http.Header{"Authorization": {"Bearer synthetic-secret"}}, "authorization"},
		{http.Header{"Cookie": {"sid=synthetic-secret"}}, "cookie"},
		{http.Header{"Authorization": {"Bearer synthetic-secret"}, "Cookie": {"sid=synthetic-secret"}}, "authorization_and_cookie"},
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
