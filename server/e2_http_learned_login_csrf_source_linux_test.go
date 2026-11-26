package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/learnedlogin"
)

type separateCSRFApp struct {
	mu     sync.Mutex
	issued map[string]string
	n      int
	seen   []string
	html   bool
}

func (a *separateCSRFApp) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.seen = append(a.seen, r.Method+" "+r.URL.Path)
	a.mu.Unlock()
	switch {
	case r.URL.Path == "/login":
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "initial-prep-session-value"})
	case r.URL.Path == "/allowed/csrf" && r.Method == "GET":
		a.mu.Lock()
		a.n++
		pre, token := fmt.Sprintf("pre-%04d", a.n), fmt.Sprintf("token-%08d", a.n)
		a.issued[pre] = token
		a.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "pre", Value: pre, Path: "/"})
		if a.html {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<form><input type="hidden" name="csrfKey" value="%s"></form>`, token)
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"csrfToken":"%s"}`, token)
		}
	case r.URL.Path == "/allowed/signin" && r.Method == "POST":
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		pre, _ := r.Cookie("pre")
		a.mu.Lock()
		ok := pre != nil && a.issued[pre.Value] != "" && form.Get("_csrf") == a.issued[pre.Value] &&
			form.Get("userName") == "tester@owned.test" && form.Get("password") == "seeded-pass-88ff"
		if pre != nil {
			delete(a.issued, pre.Value)
		}
		a.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "new-session-value-12345678", Path: "/"})
		w.Header().Set("Location", "/allowed/home")
		w.WriteHeader(http.StatusFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func separateCSRFMethod(html bool) learnedlogin.Method {
	sourceType, sourceField := learnedlogin.SourceJSONField, "csrfToken"
	if html {
		sourceType, sourceField = learnedlogin.SourceHTMLHidden, "csrfKey"
	}
	return learnedlogin.Method{Method: "POST", Path: "/allowed/signin", Format: learnedlogin.FormatForm, IdentifierField: "userName", PasswordField: "password",
		OtherFields: []string{"_csrf"}, FieldsFromResponse: []string{"_csrf"}, SessionSource: learnedlogin.SourceSetCookie,
		SessionNames: []string{"sid"}, SessionSentAs: learnedlogin.SentAsCookie, Schema: learnedlogin.MethodSchemaV2,
		FieldSources: []learnedlogin.FieldSource{{Destination: "_csrf", SourceMethod: "GET", SourcePath: "/allowed/csrf", SourceType: sourceType, SourceField: sourceField}}}
}

func TestE2HTTPFactorySeparatePathCSRF(t *testing.T) {
	for _, html := range []bool{false, true} {
		name := "json"
		if html {
			name = "hidden"
		}
		t.Run(name, func(t *testing.T) {
			app := &separateCSRFApp{issued: map[string]string{}, html: html}
			srv := httptest.NewServer(http.HandlerFunc(app.serve))
			defer srv.Close()
			lease, clock, ctx := learnedLease(t, srv, 8, "GET", "POST")
			learned := lease.AccountBindings().(E2HTTPLearnedRelogin)
			clock.Advance(minSessionRefreshInterval)
			result := learned.LearnedRelogin(ctx, E2HTTPLearnedLoginRequest{Label: "tester-a", Method: separateCSRFMethod(html), MaxFailures: 3})
			if result.Result != E2HTTPAuthAuthenticated || result.Reason != "" || result.Requests != 2 {
				t.Fatalf("별도 CSRF 로그인 %+v", result)
			}
			app.mu.Lock()
			got := append([]string(nil), app.seen...)
			app.mu.Unlock()
			if len(got) < 3 || got[len(got)-2] != "GET /allowed/csrf" || got[len(got)-1] != "POST /allowed/signin" {
				t.Fatalf("대상 요청 순서 %v", got)
			}
			if session, err := lease.AccountBindings().BoundSession(lease.Execution.Snapshot().Scope.RunRef, "tester-a", result.BindingRef); err != nil ||
				!strings.Contains(session.Get("Cookie"), "sid=new-session-value-12345678") {
				t.Fatalf("새 세션 %v %v", session, err)
			}
		})
	}
}

func TestE2HTTPFactorySeparatePathCSRFScopeDeniedBeforeSend(t *testing.T) {
	app := &separateCSRFApp{issued: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(app.serve))
	defer srv.Close()
	lease, clock, ctx := learnedLease(t, srv, 8, "GET", "POST")
	learned := lease.AccountBindings().(E2HTTPLearnedRelogin)
	clock.Advance(minSessionRefreshInterval)
	method := separateCSRFMethod(false)
	method.FieldSources[0].SourcePath = "/outside/csrf"
	result := learned.LearnedRelogin(ctx, E2HTTPLearnedLoginRequest{Label: "tester-a", Method: method, MaxFailures: 3})
	if result.Result == E2HTTPAuthAuthenticated || result.Requests != 0 || result.Reason != learnedlogin.ReasonScopeDenied {
		t.Fatalf("승인 밖 발급 경로 %+v", result)
	}
	app.mu.Lock()
	got := append([]string(nil), app.seen...)
	app.mu.Unlock()
	for _, request := range got {
		if strings.Contains(request, "/outside/") || strings.Contains(request, "/allowed/") {
			t.Fatalf("거부 전 대상 송신 %v", got)
		}
	}
}
