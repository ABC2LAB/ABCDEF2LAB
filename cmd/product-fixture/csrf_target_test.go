package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFTargetJSONAndHTMLIssuers(t *testing.T) {
	var logins int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/v1/login" {
			t.Fatal("unexpected upstream path", r.URL.Path)
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["username"] != "owned-a" || body["password"] != "synthetic-pass" || body["_csrf"] != "" {
			t.Fatal("CSRF field or credentials not translated")
		}
		logins++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"auth_token":"owned-session-token"}`)
	}))
	defer upstream.Close()
	target, err := newCSRFTarget(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, kind string
	}{
		{"/api/csrf", "application/json"},
		{"/csrf-form", "application/x-www-form-urlencoded"},
	} {
		issue := httptest.NewRecorder()
		target.ServeHTTP(issue, httptest.NewRequest("GET", test.path, nil))
		if issue.Code != 200 || len(issue.Result().Cookies()) != 1 {
			t.Fatal("source issuance failed", test.path)
		}
		var token string
		if test.kind == "application/json" {
			var body map[string]string
			_ = json.Unmarshal(issue.Body.Bytes(), &body)
			token = body["csrfToken"]
		} else {
			body := issue.Body.String()
			parts := strings.Split(body, `name="_csrf" value="`)
			if len(parts) != 2 {
				t.Fatal("HTML source field missing")
			}
			token = strings.Split(parts[1], `"`)[0]
		}
		if len(token) != 32 {
			t.Fatal("source token missing")
		}
		var payload []byte
		if test.kind == "application/json" {
			payload, _ = json.Marshal(map[string]string{"username": "owned-a", "password": "synthetic-pass", "_csrf": token})
		} else {
			payload = []byte(url.Values{"username": {"owned-a"}, "password": {"synthetic-pass"}, "_csrf": {token}}.Encode())
		}
		makeRequest := func(value string) *httptest.ResponseRecorder {
			r := httptest.NewRequest("POST", "/users/v1/login", bytes.NewReader(payload))
			r.Header.Set("Content-Type", test.kind)
			r.AddCookie(issue.Result().Cookies()[0])
			if value != "" {
				r.Header.Set("Cookie", "csrf_sid="+value)
			}
			w := httptest.NewRecorder()
			target.ServeHTTP(w, r)
			return w
		}
		if got := makeRequest(""); got.Code != 200 {
			t.Fatalf("%s valid login status %d", test.path, got.Code)
		}
		if got := makeRequest(""); got.Code != 403 {
			t.Fatalf("%s replay status %d", test.path, got.Code)
		}
	}
	if logins != 2 {
		t.Fatalf("upstream logins %d", logins)
	}
}
