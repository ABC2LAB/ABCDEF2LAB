package e2proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestExactPathActualReceiverAndAudit(t *testing.T) {
	var root, users, outside atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			root.Add(1)
		case "/users/v1", "/users/v1/123":
			users.Add(1)
		default:
			outside.Add(1)
		}
		io.WriteString(w, "owned ordinary body")
	}))
	defer target.Close()
	recorder := &coreRecorder{}
	cfg := coreConfig(t, target.URL, recorder, 100)
	cfg.Rules[0].PathPrefix = "/"
	cfg.Rules[0].PathMatch = "exact"
	cfg.Rules[0].Methods = []string{"GET", "HEAD"}
	prefix := cfg.Rules[0]
	prefix.Ref = "users"
	prefix.PathScopeRef = "users-prefix"
	prefix.PathPrefix = "/users/v1"
	prefix.PathMatch = ""
	cfg.Rules = append(cfg.Rules, prefix)
	e, c := coreOpen(t, cfg)
	for _, tc := range []struct {
		method, path string
		allow        bool
	}{
		{"GET", "/", true}, {"HEAD", "/", true}, {"GET", "/createdb", false}, {"HEAD", "/createdb", false}, {"GET", "/users/v1", true}, {"GET", "/users/v1/123", true}, {"GET", "/users/v10", false}, {"POST", "/", false},
		{"GET", "/%2fcreatedb", false}, {"GET", "/%252fcreatedb", false}, {"GET", "/./", false}, {"GET", "/x/../", false}, {"GET", "//", false}, {"GET", "/\\createdb", false}, {"GET", "/?action=createdb", false}, {"GET", "/?", false},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			resp, r, err := c.Do(t.Context(), coreRequest(t, tc.method, target.URL+tc.path))
			if tc.allow {
				if err != nil || resp == nil || !r.ResponseComplete || !r.AuditComplete {
					t.Fatal("승인 요청·감사 실패", err)
				}
				io.ReadAll(resp.Body)
				resp.Body.Close()
			} else if err == nil || resp != nil || !r.NotSent || r.ForwardAttempted {
				t.Fatal("범위 밖 송신", err)
			}
		})
	}
	if root.Load() != 2 || users.Load() != 2 || outside.Load() != 0 {
		t.Fatal("실제 수신 계수 불일치", root.Load(), users.Load(), outside.Load())
	}
	// 다른 origin은 같은 경로여도 해당 승인 규칙을 빌릴 수 없다.
	resp, r, err := c.Do(t.Context(), coreRequest(t, "GET", "http://unapproved.invalid/"))
	if err == nil || resp != nil || !r.NotSent {
		t.Fatal("다른 origin 송신")
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, r, err = c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/"))
	if err == nil || r.ForwardAttempted {
		t.Fatal("종료 후 송신")
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	found := false
	for _, event := range recorder.events {
		if event.RuleRef == "rule" {
			found = true
		}
	}
	if !found {
		t.Fatal("정확 규칙 참조가 감사에 없음")
	}
}
func TestExactPathRedirectCannotExpand(t *testing.T) {
	var root, outside atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			root.Add(1)
			http.Redirect(w, r, "/createdb", http.StatusFound)
		} else {
			outside.Add(1)
		}
	}))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 10)
	cfg.Rules[0].PathPrefix = "/"
	cfg.Rules[0].PathMatch = "exact"
	_, c := coreOpen(t, cfg)
	resp, _, _ := c.Do(t.Context(), coreRequest(t, "GET", target.URL+"/"))
	if resp != nil {
		resp.Body.Close()
	}
	if root.Load() != 1 || outside.Load() != 0 {
		t.Fatal("리다이렉트가 승인 경로 확대", root.Load(), outside.Load())
	}
}
func TestExactPathRejectsUnknownModeAndPurposeMix(t *testing.T) {
	for _, mode := range []string{"prefix", "EXACT", "unsupported"} {
		cfg := coreConfig(t, "http://owned.invalid", &coreRecorder{}, 1)
		cfg.Rules[0].PathMatch = mode
		if _, err := compileRules(cfg.Rules); err == nil {
			t.Fatal("알 수 없는 경로 모드 허용")
		}
	}
	cfg := coreConfig(t, "http://owned.invalid", &coreRecorder{}, 1)
	cfg.Rules[0].PathMatch = "exact"
	cfg.Rules[0].Purpose = PurposeAuthPrep
	cfg.Rules[0].Methods = []string{"POST"}
	if _, err := compileRules(cfg.Rules); err == nil {
		t.Fatal("인증 준비 목적을 일반 정확 경로로 대체")
	}
}

func TestExactPathRevocationDeniesBeforeConnect(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	cfg := coreConfig(t, target.URL, &coreRecorder{}, 2)
	cfg.Rules[0].PathPrefix = "/"
	cfg.Rules[0].PathMatch = "exact"
	execution, call := coreOpen(t, cfg)
	if err := execution.Revoke(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, receipt, err := call.Do(t.Context(), coreRequest(t, "GET", target.URL+"/"))
	if err == nil || response != nil || receipt.ForwardAttempted || hits.Load() != 0 {
		t.Fatal("철회 뒤 정확 루트 송신")
	}
}
