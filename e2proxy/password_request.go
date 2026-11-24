package e2proxy

import (
	"bytes"
	"encoding/base64"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// requestCarriesPassword는 모델 요청(URL·보낼 헤더·본문)에 이 실행이 등록한 시험 계정 비밀번호가 들어 있는지 본다(사용자
// 결정 2026-11-23: 로그인·세션은 프로그램 몫이고 모델은 세션 값과 refresh_session만 받는다). model_visible_v1에서 모델이
// 대상 응답으로 비밀번호를 볼 수 있어도 그 값으로 직접 로그인하지 못하게 한다. 대상이 흘린 다른 사용자의 비밀번호는 E②가
// 모르는 값이라 막지 않는다.
//
// 응답 구간 가림과 같은 해석(대소문자 무시, JSON escape·HTML entity·percent 한 번, form의 '+')으로 찾고, Basic 자격은
// base64를 풀어 본다. 해석할 수 없는 표현(잘못된 escape 등)은 원문에서 대소문자 무시 포함으로만 본다. 해석 실패만으로
// 막지 않아 진단 페이로드(일부러 깨뜨린 인코딩)를 잃지 않는다.
func (e *Execution) requestCarriesPassword(u *url.URL, headers http.Header, body []byte) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.passwords) == 0 {
		return false
	}
	found := func(data []byte, form bool) bool {
		if len(data) == 0 {
			return false
		}
		if spans, ok := passwordSpans(data, e.passwords, form); ok {
			return len(spans) > 0
		}
		lower := bytes.ToLower(data)
		for _, password := range e.passwords {
			if bytes.Contains(lower, bytes.ToLower(password)) {
				return true
			}
		}
		return false
	}
	if u != nil && (found([]byte(u.EscapedPath()), false) || found([]byte(u.RawQuery), true) || found([]byte(u.User.String()), false)) {
		return true
	}
	for name, values := range headers {
		for _, value := range append([]string{name}, values...) {
			if found([]byte(value), false) {
				return true
			}
		}
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") {
			for _, value := range values {
				if scheme, credential, ok := strings.Cut(strings.TrimSpace(value), " "); ok && strings.EqualFold(scheme, "Basic") {
					if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(credential)); err == nil && found(decoded, false) {
						return true
					}
				}
			}
		}
	}
	media, _, _ := mime.ParseMediaType(headers.Get("Content-Type"))
	return found(body, media == "application/x-www-form-urlencoded")
}
