package server

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
)

// ST07·G11: 로그인 설정(9칸) 인증 준비는 400 미만이어도 로그인 화면을 다시 준 응답을 세션 쿠키와 함께 받아도 성공으로 보지
// 않는다(soft fail). 같은 origin 로그인 경로 리다이렉트도 같다. 정상 응답은 그대로 세션을 뽑는다.
func TestAuthExtractorSoftFail(t *testing.T) {
	login := diagnosisprep.TargetLogin{Method: "POST", Path: "/login", Format: "form", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "synthetic.test:9443", Path: "/login"}}
	response := func(status int, contentType, location string) *http.Response {
		h := http.Header{"Set-Cookie": {"session=fresh-session-value; Path=/; HttpOnly"}}
		if contentType != "" {
			h.Set("Content-Type", contentType)
		}
		if location != "" {
			h.Set("Location", location)
		}
		return &http.Response{StatusCode: status, Header: h, Request: request}
	}
	form := []byte(`<html><body><form method="post" action="/login"><input name="email"><input type="password" name="password"></form></body></html>`)
	for _, tc := range []struct {
		name     string
		resp     *http.Response
		body     []byte
		softFail bool
		ok       bool
	}{
		{name: "200 login form with session cookie", resp: response(200, "text/html; charset=utf-8", ""), body: form, softFail: true},
		{name: "redirect to login path", resp: response(302, "", "/login"), softFail: true},
		{name: "absolute same-origin redirect to login path", resp: response(303, "", "https://synthetic.test:9443/login"), softFail: true},
		{name: "redirect to app", resp: response(302, "", "/home"), ok: true},
		{name: "json success", resp: response(200, "application/json", ""), body: []byte(`{"ok":true}`), ok: true},
		{name: "401", resp: response(401, "text/html", ""), body: form},
	} {
		t.Run(tc.name, func(t *testing.T) {
			soft := false
			session, err := authExtractor(login, &soft)(tc.resp, tc.body)
			if (err == nil) != tc.ok || soft != tc.softFail || tc.ok && session.Get("Cookie") != "session=fresh-session-value" {
				t.Fatalf("err=%v soft=%t session=%v", err, soft, session)
			}
		})
	}
}
