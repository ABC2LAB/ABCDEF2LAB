package e2proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// fixture 값은 모두 합성이다. 실제 HTTP 수신·감사·예산과 전달 표현을 함께 대조한다.
func passwordConfig(t *testing.T, origin string, rec Recorder) Config {
	cfg := authConfig(t, origin)
	cfg.Recorder = rec
	cfg.HeaderMode = HeaderTestAccountPassthrough
	cfg.PasswordResponse = PasswordResponseMaskSpans
	return cfg
}
func passwordUnicode(value string) string {
	var out strings.Builder
	for _, r := range value {
		fmt.Fprintf(&out, `\u%04x`, r)
	}
	return out.String()
}
func passwordPercent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		fmt.Fprintf(&out, "%%%02X", b)
	}
	return out.String()
}
func TestPasswordResponseMasksOnlyKnownSpansAndKeepsHTTP(t *testing.T) {
	password := `seeded-pass-77!`
	cases := []struct {
		name, media, body, want string
		masked                  bool
	}{
		{"plain-normal", "text/plain", "public response", "public response", false},
		{"json-normal", "application/json", `{"kind":"public","count":7}`, `{"kind":"public","count":7}`, false},
		{"html-normal", "text/html", `<p>public &amp; useful</p>`, `<p>public &amp; useful</p>`, false},
		{"raw-once", "text/plain", "before " + password + " after", "before [REDACTED] after", true},
		{"raw-many", "text/plain", "a " + password + " b " + password + " c", "a [REDACTED] b [REDACTED] c", true},
		{"json-raw", "application/json", `{"password":"` + password + `","kind":"public","count":7}`, `{"password":"[REDACTED]","kind":"public","count":7}`, true},
		{"json-escaped", "application/problem+json", `{"value":"` + passwordUnicode(password) + `","kind":"public"}`, `{"value":"[REDACTED]","kind":"public"}`, true},
		{"html-numeric", "text/html", `<b>&#115;eeded-pass-77!</b><p>public</p>`, `<b>[REDACTED]</b><p>public</p>`, true},
		{"url-encoded", "text/plain", "before " + passwordPercent(password) + " after", "before [REDACTED] after", true},
		{"form", "application/x-www-form-urlencoded", "public=ok&value=" + url.QueryEscape(password) + "&count=7", "public=ok&value=%5BREDACTED%5D&count=7", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var received atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				w.Header().Set("Content-Type", tc.media)
				w.Header().Set("X-Public", "preserved")
				w.Header().Set("ETag", `"original"`)
				w.Header().Set("Content-MD5", "original-md5")
				w.Header().Set("Content-Length", strconv.Itoa(len(tc.body)))
				w.WriteHeader(http.StatusCreated)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			recorder := &coreRecorder{}
			cfg := passwordConfig(t, srv.URL, recorder)
			e, c := coreOpen(t, cfg)
			if err := e.AddProtectedPasswords([]byte(password)); err != nil {
				t.Fatal(err)
			}
			before, _ := cfg.Budget.Ledger.Remaining(cfg.Budget.Key, cfg.Budget.Binding, time.Now())
			response, receipt, err := c.Do(t.Context(), coreRequest(t, "GET", srv.URL+"/allowed/page"))
			if err != nil || response == nil {
				t.Fatalf("허용 응답을 잃음: %v %+v", err, receipt)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(body) != tc.want || response.StatusCode != 201 || response.Header.Get("X-Public") != "preserved" || response.ContentLength != int64(len(body)) {
				t.Fatal("비민감 본문·상태·길이 변경")
			}
			if tc.masked && (response.Header.Get("ETag") != "" || response.Header.Get("Content-MD5") != "") {
				t.Fatal("원래 표현 검증자가 남음")
			}
			if !tc.masked && response.Header.Get("ETag") != `"original"` {
				t.Fatal("정상 응답 메타데이터 변경")
			}
			wantReason := Complete
			if tc.masked {
				wantReason = Withheld
			}
			if receipt.Reason != wantReason || !receipt.ForwardAttempted || !receipt.ResponseReceived || receipt.ResponseComplete == tc.masked || receipt.ResponseTruncated {
				t.Fatalf("송신·표현 보류 의미 오류: %+v", receipt)
			}
			after, _ := cfg.Budget.Ledger.Remaining(cfg.Budget.Key, cfg.Budget.Binding, time.Now())
			counts := e.Snapshot().Counts
			if received.Load() != 1 || before-after != 1 || counts.ForwardAttempted != 1 || counts.Responses != 1 || counts.Unknown != 0 || counts.Attempts != 1 {
				t.Fatal("실제 요청·예산·관찰 계수 변경")
			}
			if tc.masked && counts.Withheld != 1 {
				t.Fatal("표현 보류를 세지 않음")
			}
			recorder.mu.Lock()
			defer recorder.mu.Unlock()
			matched := false
			for _, event := range recorder.events {
				if event.Stage == "result" && event.Receipt.CallRef == c.Ref() {
					matched = true
					if event.Receipt.Reason != wantReason {
						t.Fatal("감사 사유 불일치")
					}
				}
			}
			if !matched {
				t.Fatal("실제 관찰 감사 없음")
			}
		})
	}
}

func TestPasswordResponseSpecialCharactersAndSourceOffsets(t *testing.T) {
	for _, password := range []string{`quotes"and\slash!`, "공유하지않는🙂값", "amp&angle<plus+space !", "sixsix"} {
		t.Run(strconv.Itoa(len(password)), func(t *testing.T) {
			jsonValue, _ := json.Marshal(password)
			for _, tc := range []struct{ media, body, want string }{
				{"application/json", `{"left":"public","value":` + string(jsonValue) + `,"right":7}`, `{"left":"public","value":"[REDACTED]","right":7}`},
				{"text/html", "<p>before " + html.EscapeString(password) + " after</p>", "<p>before [REDACTED] after</p>"},
				{"application/x-www-form-urlencoded", "before=ok&v=" + url.QueryEscape(password) + "&after=yes", "before=ok&v=%5BREDACTED%5D&after=yes"},
			} {
				cfg := passwordConfig(t, "http://127.0.0.1:1", &coreRecorder{})
				e, _ := coreOpen(t, cfg)
				if e.AddProtectedPasswords([]byte(password)) != nil {
					t.Fatal("비밀번호 등록 실패")
				}
				_, gen := e.protectedState()
				resp := &http.Response{Header: http.Header{"Content-Type": {tc.media}}, Status: "200 OK", Proto: "HTTP/1.1"}
				body, masked, safe := e.prepareResponse(resp, []byte(tc.body), gen, 1<<20, false, false)
				if !safe || !masked || string(body) != tc.want {
					t.Fatalf("지원 형식의 원문 위치 가림 실패: media=%s safe=%t masked=%t", tc.media, safe, masked)
				}
			}
		})
	}
}

func TestPasswordResponseJSONSurrogatePair(t *testing.T) {
	e, _ := coreOpen(t, passwordConfig(t, "http://127.0.0.1:1", &coreRecorder{}))
	if e.AddProtectedPasswords([]byte("seeded-🙂-password")) != nil {
		t.Fatal("등록 실패")
	}
	_, generation := e.protectedState()
	response := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Status: "200 OK", Proto: "HTTP/1.1"}
	body, masked, safe := e.prepareResponse(response, []byte(`{"public":7,"value":"seeded-\ud83d\ude42-password"}`), generation, 1024, false, false)
	if !safe || !masked || string(body) != `{"public":7,"value":"[REDACTED]"}` {
		t.Fatal("surrogate pair의 원문 구간 가림 실패")
	}
}

func TestPasswordResponseUnsafeRepresentationsWithhold(t *testing.T) {
	password := "seeded-secret-99"
	for _, tc := range []struct {
		name, media, body string
		code              int
		header, value     string
	}{
		{"base64", "text/plain", base64.StdEncoding.EncodeToString([]byte(password)), 200, "", ""},
		{"double-percent", "text/plain", passwordPercent(passwordPercent(password)), 200, "", ""},
		{"double-json", "application/json", `{"v":"\\u0073eeded-secret-99"}`, 200, "", ""},
		{"cross-encoding", "text/html", `&#37;73eeded-secret-99`, 200, "", ""},
		{"malformed-json", "application/json", `{"v":"\u00zz"}`, 200, "", ""},
		{"malformed-percent", "text/plain", `%7x` + password, 200, "", ""},
		{"unknown-entity", "text/html", `&unsupported;` + password, 200, "", ""},
		{"binary", "application/octet-stream", password, 200, "", ""},
		{"charset", "text/plain; charset=iso-8859-1", password, 200, "", ""},
		{"invalid-utf8", "text/plain", string([]byte{0xff}) + password, 200, "", ""},
		{"partial", "text/plain", password, 206, "", ""},
		{"content-range", "text/plain", password, 200, "Content-Range", "bytes 0-15/64"},
		{"compressed", "text/plain", password, 200, "Content-Encoding", "gzip"},
		{"transfer-encoded", "text/plain", password, 200, "Content-Transfer-Encoding", "base64"},
		{"password-header", "text/plain", "public", 200, "X-Echo", password},
		{"encoded-header", "text/plain", "public", 200, "Set-Cookie", "session=" + passwordPercent(password)},
		{"authorization", "text/plain", "public", 200, "Authorization", "Bearer " + password},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				w.Header().Set("Content-Type", tc.media)
				if tc.header != "" {
					w.Header().Set(tc.header, tc.value)
				}
				w.WriteHeader(tc.code)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			e, c := coreOpen(t, passwordConfig(t, srv.URL, &coreRecorder{}))
			if e.AddProtectedPasswords([]byte(password)) != nil {
				t.Fatal("등록 실패")
			}
			resp, receipt, err := c.Do(t.Context(), coreRequest(t, "GET", srv.URL+"/allowed/page"))
			if !errors.Is(err, ErrWithheld) || resp != nil || receipt.Reason != Withheld || !receipt.ResponseReceived || receipt.ResponseComplete || received.Load() != 1 || e.Snapshot().Counts.Unknown != 0 {
				t.Fatalf("안전한 보류 실패: %v %+v", err, receipt)
			}
		})
	}
}

func TestPasswordResponseLegacyAndGenericSecretsStillWithheld(t *testing.T) {
	password := "synthetic-control-value"
	for _, mode := range []string{"", PasswordResponseMaskSpans} {
		for _, generic := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-%t", mode, generic), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/plain")
					io.WriteString(w, password)
				}))
				defer srv.Close()
				cfg := passwordConfig(t, srv.URL, &coreRecorder{})
				cfg.PasswordResponse = mode
				e, c := coreOpen(t, cfg)
				if e.AddProtectedPasswords([]byte(password)) != nil {
					t.Fatal("등록 실패")
				}
				if generic && e.AddProtectedValues([]byte(password)) != nil {
					t.Fatal("일반 보호 등록 실패")
				}
				resp, receipt, err := c.Do(t.Context(), coreRequest(t, "GET", srv.URL+"/allowed/page"))
				if mode == "" || generic {
					if resp != nil || !errors.Is(err, ErrWithheld) || receipt.ResponseComplete {
						t.Fatal("구형·일반 보호 보류를 넓힘")
					}
				} else {
					if err != nil || resp == nil || receipt.ResponseComplete {
						t.Fatal("명시 정책이 적용 안 됨")
					}
					resp.Body.Close()
				}
			})
		}
	}
}

func TestPasswordRegistrationAtomicCapacityScanAndRace(t *testing.T) {
	cfg := passwordConfig(t, "http://127.0.0.1:1", &coreRecorder{})
	e, _ := coreOpen(t, cfg)
	for i := range maxRedactValues {
		if err := e.AddProtectedPasswords([]byte(fmt.Sprintf("password-%03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	_, gen := e.protectedState()
	if e.AddProtectedPasswords([]byte("password-000")) != nil || e.protectedGeneration() != gen {
		t.Fatal("동일 비밀번호 재등록이 세대를 바꿈")
	}
	if e.AddProtectedPasswords([]byte("one-more-password")) == nil || e.protectedGeneration() != gen || len(e.passwords) != maxRedactValues {
		t.Fatal("상한 실패가 원자적이지 않음")
	}
	if bytes.Contains(e.RedactOutput([]byte("password-000 password-063")), []byte("password-")) {
		t.Fatal("출력 가림 누락")
	}
	for _, bad := range [][]byte{nil, []byte("short"), bytes.Repeat([]byte{'a'}, maxRedactValueBytes+1), {0xff, 0xff, 0xff, 0xff, 0xff, 0xff}} {
		if e.AddProtectedPasswords(bad) == nil {
			t.Fatal("잘못된 보호 값 수용")
		}
	}
	resp := &http.Response{Header: http.Header{"Content-Type": {"text/plain"}}, Status: "200 OK", Proto: "HTTP/1.1"}
	if _, _, safe := e.prepareResponse(resp, []byte("public"), gen-1, 1024, false, false); safe {
		t.Fatal("읽기 중 보호 세대 변경 무시")
	}
	if _, _, safe := e.prepareResponse(resp, []byte("public"), gen, 1024, false, true); safe {
		t.Fatal("검사 상한 초과를 안전으로 처리")
	}
	cfg.Limits.MaxResponseBodyBytes = 32
	cfg.Limits.MaxResponseScanBytes = 32
	strict, _ := coreOpen(t, cfg)
	if strict.AddProtectedPasswords([]byte("longer-password")) == nil || len(strict.passwords) != 0 {
		t.Fatal("검사 상한 조건 위반 수용")
	}
}

func TestPasswordResponseBoundsAndReplacementCannotReflectPassword(t *testing.T) {
	for _, tc := range []struct {
		password, body string
		limit          int64
	}{
		{"sixsix", "sixsix", 6},
		{"REDACTED", "REDACTED", 1024},
		{"seeded-value", strings.Repeat("seeded-value ", maxPasswordSpans+1), 1 << 20},
		{"seeded-value", strings.Repeat("%41", maxPasswordTransforms+1), 1 << 20},
	} {
		t.Run(strconv.Itoa(len(tc.body)), func(t *testing.T) {
			e, _ := coreOpen(t, passwordConfig(t, "http://127.0.0.1:1", &coreRecorder{}))
			if e.AddProtectedPasswords([]byte(tc.password)) != nil {
				t.Fatal("등록 실패")
			}
			_, gen := e.protectedState()
			resp := &http.Response{Header: http.Header{"Content-Type": {"text/plain"}}, Status: "200 OK", Proto: "HTTP/1.1"}
			if _, _, safe := e.prepareResponse(resp, []byte(tc.body), gen, tc.limit, false, false); safe {
				t.Fatal("불완전 검사·가림 확장을 허용함")
			}
		})
	}
}

func TestPasswordAuthPrepKeepsSessionAndRejectsPasswordAsSession(t *testing.T) {
	password := "synthetic-password-55"
	for _, session := range []string{"synthetic-issued-session-77", password, passwordPercent(password), base64.StdEncoding.EncodeToString([]byte(password))} {
		t.Run(strconv.Itoa(len(session)), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: session})
				io.WriteString(w, `{"ok":true}`)
			}))
			defer srv.Close()
			e, c := authProgramOpen(t, passwordConfig(t, srv.URL, &coreRecorder{}))
			if e.AddProtectedPasswords([]byte(password)) != nil {
				t.Fatal("등록 실패")
			}
			headers, receipt, err := c.AuthPrep(context.Background(), loginRequest(t, srv.URL+"/login"), cookieExtractor("session"))
			if session == "synthetic-issued-session-77" {
				if err != nil || headers.Get("Cookie") != "session="+session {
					t.Fatal("대상 발급 세션을 잃음")
				}
			} else if !errors.Is(err, ErrAuthSession) || headers != nil {
				t.Fatal("비밀번호를 모델 세션으로 넘김")
			}
			if !receipt.ResponseComplete || receipt.Reason != Complete || !receipt.ResponseReceived || e.Snapshot().Counts.Unknown != 0 {
				t.Fatal("인증 결과와 송신 관측을 혼동함")
			}
		})
	}
}

func FuzzPasswordResponseSupportedJSON(f *testing.F) {
	for _, s := range []string{"seeded-pass", `quotes"and\slash`, "한글🙂비밀번호", "space plus+and&entity;"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, password string) {
		if len(password) < minProtectedValueBytes || len(password) > 256 || !utf8.ValidString(password) {
			return
		}
		value, err := json.Marshal(password)
		if err != nil {
			return
		}
		body := append(append([]byte(`{"public":7,"value":`), value...), '}')
		e := &Execution{cfg: Config{HeaderMode: HeaderTestAccountPassthrough, PasswordResponse: PasswordResponseMaskSpans}, passwords: [][]byte{[]byte(password)}, secretsGen: 1}
		response := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Status: "200 OK", Proto: "HTTP/1.1"}
		out, _, safe := e.prepareResponse(response, body, 1, 4096, false, false)
		if !safe {
			if out != nil {
				t.Fatal("불안전 본문을 함께 반환")
			}
			return
		}
		if !json.Valid(out) || bytes.Contains(out, []byte(password)) {
			t.Fatal("JSON 가림 파손·비밀번호 노출")
		}
		spans, checked := passwordSpans(out, e.passwords, false)
		if !checked || len(spans) != 0 {
			t.Fatal("지원 표현의 비밀번호 잔존")
		}
	})
}
