package e2proxy

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// model_visible_v1(사용자 결정 2026-11-23): 대상 응답에 비친 시험 계정 비밀번호는 보류·가림 없이 도구(모델)에 주고, 저장
// 기록용 가림(RedactRecord)에서만 가린다. 일반 보호 값(우리 제어 비밀) 보류는 그대로다. 값은 모두 합성이다.
func TestPasswordResponseModelVisibleDeliversAndRecordMasks(t *testing.T) {
	password := "seeded-pass-77!"
	cases := map[string]struct{ media, body string }{
		"plain": {"text/plain", "before " + password + " after"},
		"json":  {"application/json", `{"username":"synthetic","password":"` + password + `"}`},
		"html":  {"text/html", `<td>synthetic</td><td>` + password + `</td>`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.media)
				w.Header().Set("ETag", `"original"`)
				w.Header().Set("Content-Length", strconv.Itoa(len(tc.body)))
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			cfg := passwordConfig(t, srv.URL, &coreRecorder{})
			cfg.PasswordResponse = PasswordResponseModelVisible
			e, c := coreOpen(t, cfg)
			if err := e.AddProtectedPasswords([]byte(password)); err != nil {
				t.Fatal(err)
			}
			resp, receipt, err := c.Do(t.Context(), coreRequest(t, "GET", srv.URL+"/allowed/page"))
			if err != nil || resp == nil {
				t.Fatalf("비밀번호가 비친 응답을 보류함: %v %+v", err, receipt)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != tc.body || resp.Header.Get("ETag") != `"original"` {
				t.Fatal("모델에 가는 응답을 바꿈")
			}
			if receipt.Reason != Complete || !receipt.ResponseComplete || e.Snapshot().Counts.Withheld != 0 {
				t.Fatalf("보류로 셈: %+v", receipt)
			}
			// 모델에 가는 도구 출력은 비밀번호를 그대로 두고, 저장 기록은 가린다.
			if !bytes.Contains(e.RedactOutput(body), []byte(password)) {
				t.Fatal("모델 출력에서 비밀번호를 가림")
			}
			if record := e.RedactRecord(body); bytes.Contains(record, []byte(password)) || !bytes.Contains(record, []byte("[REDACTED]")) {
				t.Fatal("저장 기록에 비밀번호가 남음")
			}
		})
	}
}

// 다른 정책(이전 전체 보류·구간 가림)의 모델 출력은 지금처럼 비밀번호를 가리고, 일반 보호 값은 model_visible_v1에서도 보류한다.
func TestPasswordResponseModelVisibleKeepsControlSecretsWithheld(t *testing.T) {
	password := "seeded-pass-77!"
	for _, mode := range []string{"", PasswordResponseMaskSpans, PasswordResponseModelVisible} {
		t.Run("mode="+mode, func(t *testing.T) {
			cfg := passwordConfig(t, "https://example.test", &coreRecorder{})
			cfg.PasswordResponse = mode
			e, _ := coreOpen(t, cfg)
			if err := e.AddProtectedPasswords([]byte(password)); err != nil {
				t.Fatal(err)
			}
			out := e.RedactOutput([]byte("x " + password))
			if visible := bytes.Contains(out, []byte(password)); visible != (mode == PasswordResponseModelVisible) {
				t.Fatalf("모델 출력 비밀번호 노출=%v", visible)
			}
			if bytes.Contains(e.RedactRecord([]byte("x "+password)), []byte(password)) {
				t.Fatal("저장 기록에 비밀번호가 남음")
			}
		})
	}
	control := "synthetic-control-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "leak "+control)
	}))
	defer srv.Close()
	cfg := passwordConfig(t, srv.URL, &coreRecorder{})
	cfg.PasswordResponse = PasswordResponseModelVisible
	e, c := coreOpen(t, cfg)
	if err := e.AddProtectedValues([]byte(control)); err != nil {
		t.Fatal(err)
	}
	resp, receipt, err := c.Do(t.Context(), coreRequest(t, "GET", srv.URL+"/allowed/page"))
	if resp != nil || !errors.Is(err, ErrWithheld) || receipt.Reason != Withheld {
		t.Fatalf("제어 비밀이 비친 응답을 넘김: %v %+v", err, receipt)
	}
	if bytes.Contains(e.RedactOutput([]byte(control)), []byte(control)) {
		t.Fatal("모델 출력에서 제어 비밀을 안 가림")
	}
}

// 모델 요청에 이 실행의 시험 계정 비밀번호가 들어 있으면 보내지 않는다(사용자 결정 2026-11-23: 로그인은 프로그램 몫).
// 대상이 흘린 다른 비밀번호(등록 안 된 값)와 깨진 인코딩만 있는 진단 페이로드는 막지 않는다.
func TestModelRequestCarryingTestAccountPasswordIsDenied(t *testing.T) {
	password := "seeded-pass-77!"
	type req struct {
		method, path, body, media string
		header                    map[string]string
	}
	cases := map[string]struct {
		r    req
		deny bool
	}{
		"json body":     {req{method: "POST", path: "/allowed/login", body: `{"username":"alice","password":"` + password + `"}`, media: "application/json"}, true},
		"json escaped":  {req{method: "POST", path: "/allowed/login", body: `{"password":"` + passwordUnicode(password) + `"}`, media: "application/json"}, true},
		"form body":     {req{method: "POST", path: "/allowed/login", body: "user=alice&pw=" + url.QueryEscape(password), media: "application/x-www-form-urlencoded"}, true},
		"query":         {req{method: "GET", path: "/allowed/page?pw=" + passwordPercent(password)}, true},
		"case folded":   {req{method: "GET", path: "/allowed/page?pw=" + url.QueryEscape(strings.ToUpper(password))}, true},
		"basic auth":    {req{method: "GET", path: "/allowed/page", header: map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+password))}}, true},
		"raw header":    {req{method: "GET", path: "/allowed/page", header: map[string]string{"X-Note": "pw " + password}}, true},
		"broken escape": {req{method: "POST", path: "/allowed/login", body: `{"p":"\u12` + password + `"}`, media: "application/json"}, true},
		"other user pw": {req{method: "POST", path: "/allowed/login", body: `{"username":"bob","password":"leaked-by-target-1"}`, media: "application/json"}, false},
		"broken only":   {req{method: "GET", path: "/allowed/page?q=%ZZ%27--"}, false},
		"plain":         {req{method: "GET", path: "/allowed/page"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var received atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				io.WriteString(w, "ok")
			}))
			defer srv.Close()
			cfg := passwordConfig(t, srv.URL, &coreRecorder{})
			cfg.PasswordResponse = PasswordResponseModelVisible
			cfg.Rules[0].Methods = []string{"GET", "POST"}
			e, c := coreOpen(t, cfg)
			if err := e.AddProtectedPasswords([]byte(password)); err != nil {
				t.Fatal(err)
			}
			var body io.Reader
			if tc.r.body != "" {
				body = strings.NewReader(tc.r.body)
			}
			r, err := http.NewRequest(tc.r.method, srv.URL+tc.r.path, body)
			if err != nil {
				t.Fatal(err)
			}
			if tc.r.media != "" {
				r.Header.Set("Content-Type", tc.r.media)
			}
			for k, v := range tc.r.header {
				r.Header.Set(k, v)
			}
			resp, receipt, err := c.Do(t.Context(), r)
			if resp != nil {
				resp.Body.Close()
			}
			if tc.deny {
				if !errors.Is(err, ErrDenied) || receipt.Reason != PolicyDenied || received.Load() != 0 {
					t.Fatalf("비밀번호가 든 모델 요청을 보냄: err=%v reason=%s received=%d", err, receipt.Reason, received.Load())
				}
				return
			}
			if err != nil || received.Load() != 1 {
				t.Fatalf("비밀번호 없는 요청을 막음: err=%v reason=%s", err, receipt.Reason)
			}
		})
	}
}
