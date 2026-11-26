package loginattempt

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestDifferentPathCSRFSourceRecordedWithoutValue(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body, wantType, wantField string }{
		{"json", "application/json", `{"csrfToken":"token-12345678"}`, "json_field", "csrfToken"},
		{"html", "text/html", `<form><input type="hidden" name="csrfKey" value="token-12345678"></form>`, "html_hidden", "csrfKey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, rec := newTracker(t, false)
			issued := get("/api/csrf")
			issued.TransportRef, issued.Context = "17", "browser"
			body := []byte(tc.body)
			tr.Observe(issued, Response{Status: 200, Header: hdr("Content-Type", tc.contentType, "Content-Length", strconv.Itoa(len(body)),
				"Set-Cookie", "pre=session-12345678; Path=/"), Body: body})
			login := formLogin("user1@owned.test", "alpha-pass-1", "Cookie", "pre=session-12345678")
			login.TransportRef, login.Context = "18", "browser"
			login.Body = append(login.Body, []byte("&_csrf="+url.QueryEscape("token-12345678"))...)
			plan := tr.Prepare(login, true)
			if !plan.Login {
				t.Fatal("로그인 시도 누락")
			}
			tr.Complete(plan, "18", login, redirect("/home", "Set-Cookie", "sid=user-session-12345678; Path=/"))
			r := rec.last("exploration-1")
			if len(r.FieldSources) != 1 || r.FieldSources[0].SourcePath != "/api/csrf" || r.FieldSources[0].SourceRef != "17" ||
				r.FieldSources[0].SourceType != tc.wantType || r.FieldSources[0].SourceField != tc.wantField {
				t.Fatalf("출처 %+v", r.FieldSources)
			}
			encoded, _ := json.Marshal(r)
			if strings.Contains(string(encoded), "token-12345678") || strings.Contains(string(encoded), "session-12345678") {
				t.Fatal("출처 기록에 비밀값 저장")
			}
		})
	}
}

func TestDifferentPathCSRFSourceRejectsAmbiguityAndOtherContext(t *testing.T) {
	for name, setup := range map[string]func(*Tracker){
		"other cookie": func(tr *Tracker) {
			tr.Observe(Request{TransportRef: "17", Context: "browser", Method: "GET", Path: "/api/csrf"},
				Response{Status: 200, Header: hdr("Content-Type", "application/json", "Set-Cookie", "pre=other; Path=/"), Body: []byte(`{"csrfToken":"token-12345678"}`)})
		},
		"issuer needs prior auth": func(tr *Tracker) {
			tr.Observe(Request{TransportRef: "17", Context: "browser", Method: "GET", Path: "/api/csrf", Header: hdr("Cookie", "earlier=account-session")},
				Response{Status: 200, Header: hdr("Content-Type", "application/json", "Set-Cookie", "pre=session-12345678; Path=/"), Body: []byte(`{"csrfToken":"token-12345678"}`)})
		},
		"ambiguous response": func(tr *Tracker) {
			tr.Observe(Request{TransportRef: "17", Context: "browser", Method: "GET", Path: "/api/csrf"},
				Response{Status: 200, Header: hdr("Content-Type", "application/json"), Body: []byte(`{"a":"token-12345678","b":"token-12345678"}`)})
		},
		"other context": func(tr *Tracker) {
			tr.Observe(Request{TransportRef: "17", Context: "other", Method: "GET", Path: "/api/csrf"},
				Response{Status: 200, Header: hdr("Content-Type", "application/json"), Body: []byte(`{"csrfToken":"token-12345678"}`)})
		},
		"reset context": func(tr *Tracker) {
			tr.Observe(Request{TransportRef: "17", Context: "browser", Method: "GET", Path: "/api/csrf"},
				Response{Status: 200, Header: hdr("Content-Type", "application/json"), Body: []byte(`{"csrfToken":"token-12345678"}`)})
			tr.ResetContext("browser")
		},
		"query source": func(tr *Tracker) {
			tr.Observe(Request{TransportRef: "17", Context: "browser", Method: "GET", Path: "/api/csrf", RawQuery: "mode=full"},
				Response{Status: 200, Header: hdr("Content-Type", "application/json"), Body: []byte(`{"csrfToken":"token-12345678"}`)})
		},
		"truncated response": func(tr *Tracker) {
			tr.Observe(Request{TransportRef: "17", Context: "browser", Method: "GET", Path: "/api/csrf"},
				Response{Status: 200, Header: hdr("Content-Type", "application/json", "Content-Length", "999"), Body: []byte(`{"csrfToken":"token-12345678"}`)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			tr, rec := newTracker(t, false)
			setup(tr)
			login := formLogin("user1@owned.test", "alpha-pass-1", "Cookie", "pre=session-12345678")
			login.TransportRef, login.Context = "18", "browser"
			login.Body = append(login.Body, []byte("&_csrf=token-12345678")...)
			plan := tr.Prepare(login, true)
			tr.Complete(plan, "18", login, redirect("/home", "Set-Cookie", "sid=user-session-12345678; Path=/"))
			if len(rec.last("exploration-1").FieldSources) != 0 || !rec.last("exploration-1").SourceUnresolved {
				t.Fatal("모호하거나 다른 계정 문맥의 출처를 기록함")
			}
		})
	}
}

func TestSamePathCSRFRemainsV1(t *testing.T) {
	tr, rec := newTracker(t, false)
	issued := get("/login")
	issued.TransportRef, issued.Context = "16", "browser"
	body := `<form><input type="hidden" name="_csrf" value="token-12345678"><input type="password" name="password"></form>`
	tr.Observe(issued, html(200, body, "Content-Length", strconv.Itoa(len(body))))
	login := formLogin("user1@owned.test", "alpha-pass-1")
	login.TransportRef, login.Context = "17", "browser"
	login.Body = append(login.Body, []byte("&_csrf=token-12345678")...)
	plan := tr.Prepare(login, true)
	tr.Complete(plan, "17", login, redirect("/home", "Set-Cookie", "sid=user-session-12345678; Path=/"))
	r := rec.last("exploration-1")
	if r.SourceUnresolved || len(r.FieldSources) != 0 || len(r.FieldsFromResponse) != 1 {
		t.Fatalf("동일 경로 v1 %+v", r)
	}
}
