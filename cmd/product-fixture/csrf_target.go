package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// csrfTarget은 VAmPI 앞에 두는 소유 합성 시험 대상이다. 익명 CSRF 발급 경로 두 개를 추가하고
// 로그인 POST에 일회용 값을 요구한다. 로그인 외 경로는 VAmPI로 전달해 E③가 실제 도서 정책을 검증한다.
type csrfTarget struct {
	mu     sync.Mutex
	tokens map[string]csrfTicket
	proxy  *httputil.ReverseProxy
	client *http.Client
	base   *url.URL
}

type csrfTicket struct {
	value string
	at    time.Time
}

func newCSRFTarget(upstream string) (*csrfTarget, error) {
	base, err := url.Parse(upstream)
	if err != nil || base.Scheme != "http" || base.Host == "" || base.Path != "" {
		return nil, errors.New("invalid_fixture_upstream")
	}
	proxy := httputil.NewSingleHostReverseProxy(base)
	proxy.Transport = &http.Transport{Proxy: nil}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "owned_target_unavailable", http.StatusServiceUnavailable)
	}
	return &csrfTarget{tokens: map[string]csrfTicket{}, proxy: proxy,
		client: &http.Client{Timeout: 10 * time.Second}, base: base}, nil
}

func csrfRandom() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func (t *csrfTarget) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && (r.URL.Path == "/api/csrf" || r.URL.Path == "/csrf-form") {
		sid, value := csrfRandom(), csrfRandom()
		if sid == "" || value == "" {
			http.Error(w, "fixture_unavailable", http.StatusServiceUnavailable)
			return
		}
		t.mu.Lock()
		t.tokens[sid] = csrfTicket{value: value, at: time.Now()}
		t.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "csrf_sid", Value: sid, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		if r.URL.Path == "/api/csrf" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"csrfToken": value})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><form method="post" action="/users/v1/login"><input type="hidden" name="_csrf" value="`+value+`"></form></html>`)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/users/v1/login" {
		t.login(w, r)
		return
	}
	t.proxy.ServeHTTP(w, r)
}

func (t *csrfTarget) login(w http.ResponseWriter, r *http.Request) {
	var username, password, token string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
			CSRF     string `json:"_csrf"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		username, password, token = in.Username, in.Password, in.CSRF
	} else if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		username, password, token = r.PostForm.Get("username"), r.PostForm.Get("password"), r.PostForm.Get("_csrf")
	} else {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	cookie, err := r.Cookie("csrf_sid")
	if err != nil || username == "" || password == "" || token == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	t.mu.Lock()
	issued, exists := t.tokens[cookie.Value]
	delete(t.tokens, cookie.Value)
	t.mu.Unlock()
	if !exists || issued.value != token || time.Since(issued.at) > 5*time.Minute {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, t.base.String()+"/users/v1/login", bytes.NewReader(body))
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func serveCSRFTarget(upstream string) error {
	target, err := newCSRFTarget(upstream)
	if err != nil {
		return err
	}
	return http.ListenAndServe(":5000", target)
}
