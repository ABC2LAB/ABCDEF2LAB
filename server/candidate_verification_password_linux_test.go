//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
)

// 선택 후보 검증 회차(#950)는 프로그램이 선택 계정 하나로 인증 준비를 하고 승인된 읽기만 보낸다. 그 회차의 비밀번호 응답
// 정책이 이전 mask_spans_v1이면 DLP 폐기(절대규칙 2, 2026-11-26) 뒤에도 비밀번호 가림 장치가 살아 있다. 실제 factory로
// 두 정책을 나란히 돌려 차이를 고정한다(합성 자격·소유 httptest 대상만).
//   - 짧은(6바이트 미만) 비밀번호 계정: mask_spans_v1은 등록을 못 해 로그인하지 않는다(not_sent).
//   - 비밀번호가 비친 응답: mask_spans_v1은 구간을 가리고 영수증을 보류(미완료)로 바꾼다 → 선택 관찰 투영에서 빠진다.
//   - 구간 가림이 다루지 못하는 형식(XML)은 비밀번호가 없어도 통째로 보류된다.
//
// model_visible_v1은 셋 다 그대로 주고, 우리 제어 비밀·E③ 표식 보호는 두 정책 모두 그대로다.
func TestSelectedCandidatePasswordResponsePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, mode, password, path string
		wantAuth                   string
		wantComplete               bool
	}{
		{"mask reflected password", e2proxy.PasswordResponseMaskSpans, "seeded-pass-88ff", "/read/echo", E2HTTPAuthAuthenticated, false},
		{"mask unsupported media", e2proxy.PasswordResponseMaskSpans, "seeded-pass-88ff", "/read/xml", E2HTTPAuthAuthenticated, false},
		{"mask short password", e2proxy.PasswordResponseMaskSpans, "abc12", "/read/echo", E2HTTPAuthNotSent, false},
		{"visible reflected password", e2proxy.PasswordResponseModelVisible, "seeded-pass-88ff", "/read/echo", E2HTTPAuthAuthenticated, true},
		{"visible unsupported media", e2proxy.PasswordResponseModelVisible, "seeded-pass-88ff", "/read/xml", E2HTTPAuthAuthenticated, true},
		{"visible short password", e2proxy.PasswordResponseModelVisible, "abc12", "/read/echo", E2HTTPAuthAuthenticated, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const session = "synthetic-selected-session"
			var logins, reads atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" && r.Method == http.MethodPost {
					logins.Add(1)
					if r.ParseForm() != nil || r.Form.Get("email") != "tester@owned.test" || r.Form.Get("password") != tc.password {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					http.SetCookie(w, &http.Cookie{Name: "session", Value: session})
					_, _ = io.WriteString(w, "signed in")
					return
				}
				if r.Header.Get("Cookie") != "session="+session {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				reads.Add(1)
				switch r.URL.Path {
				case "/read/echo":
					// 취약한 앱처럼 로그인한 사용자의 비밀번호를 프로필에 되돌려 준다.
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"email":"tester@owned.test","password":"`+tc.password+`","plan":"basic"}`)
				case "/read/xml":
					w.Header().Set("Content-Type", "application/xml")
					_, _ = io.WriteString(w, `<profile><plan>basic</plan></profile>`)
				case "/read/secret":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"leak":"synthetic-control-secret-77ee"}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer target.Close()
			cfg, _, policy := e2HostPolicyFixture(t)
			policy.Rules[0].Origin, policy.Rules[0].PathPrefix = target.URL, "/read"
			policy.Rules = append(policy.Rules, E2HTTPFileRule{Ref: "auth-login", Origin: target.URL, ConnectIP: "127.0.0.1",
				PathPrefix: "/login", PathScopeRef: "auth-login", Methods: []string{"POST"}, Purpose: e2proxy.PurposeAuthPrep})
			pin := authCredentialPin(t, cfg.AuthorityRoot, "selected-account.json", "tester@owned.test", tc.password)
			auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin})
			// 제품이 만드는 선택 후보 정책처럼 모델 로그인 표시가 남아 있어도 선택 계정 회차는 프로그램 인증 준비를 한다.
			auth.PasswordResponse, auth.ModelLogin = tc.mode, true
			policy.Auth, policy.HeaderMode = &auth, e2proxy.HeaderTestAccountPassthrough
			policy.LoginAttempts = &E2HTTPLoginAttemptLimits{MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16}
			policy.SelectedAccountRef = CandidateAccountRevision("tester-a", pin.SHA256)
			policy.MaxRequests = 4
			policy.Limits.MaxAttempts, policy.Limits.MaxCalls = 4, 4
			raw, err := json.Marshal(policy)
			if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
				t.Fatal("synthetic selected policy unavailable")
			}
			if _, _, err := ValidateE2HTTPRunPolicyDocument(raw); err != nil {
				t.Fatal("selected policy rejected", err)
			}
			options, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile,
				Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := options.Acquire(t.Context(), E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 31})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
				defer done()
				_ = lease.Runner.Close(cleanup)
				_ = lease.Execution.Stop(cleanup)
				_ = lease.Release(cleanup)
			}()
			if got := lease.AccountAuth["tester-a"]; got != tc.wantAuth {
				t.Fatalf("selected account auth=%s want %s", got, tc.wantAuth)
			}
			if tc.wantAuth != E2HTTPAuthAuthenticated {
				// 로그인하지 않았으므로 계정 없는 실행으로 묶여 대상에 자격이 가지 않는다(C14 결정 5·6).
				if logins.Load() != 0 {
					t.Fatalf("unprotectable password was sent: logins=%d", logins.Load())
				}
				return
			}
			if logins.Load() != 1 {
				t.Fatalf("selected login count=%d", logins.Load())
			}
			read := func(path string) (*http.Response, e2proxy.Receipt, []e2proxy.HTTPObservation, error) {
				call, err := lease.Execution.BeginCall(t.Context(), "selected-"+strings.TrimPrefix(path, "/read/"))
				if err != nil {
					t.Fatal(err)
				}
				defer call.Close(t.Context())
				request, _ := http.NewRequest(http.MethodGet, target.URL+path, nil)
				response, receipt, err := call.Do(t.Context(), request)
				return response, receipt, call.Result().Observations, err
			}
			response, receipt, observed, err := read(tc.path)
			if err == nil && response != nil {
				defer response.Body.Close()
			}
			if tc.wantComplete {
				if err != nil || response == nil || receipt.Reason != e2proxy.Complete || !receipt.ResponseComplete {
					t.Fatalf("model_visible_v1 response withheld: %+v %v", receipt, err)
				}
				body, _ := io.ReadAll(response.Body)
				if tc.path == "/read/echo" && !strings.Contains(string(body), `"password":"`+tc.password+`"`) {
					t.Fatalf("reflected test-account password masked: %s", body)
				}
			} else if receipt.ResponseComplete || receipt.Reason != e2proxy.Withheld {
				t.Fatalf("mask_spans_v1 response not withheld: %+v %v", receipt, err)
			} else if tc.path == "/read/echo" {
				body, _ := io.ReadAll(response.Body)
				if strings.Contains(string(body), tc.password) || !strings.Contains(string(body), "[REDACTED]") {
					t.Fatalf("mask_spans_v1 did not mask: %s", body)
				}
			} else if !errors.Is(err, e2proxy.ErrWithheld) || response != nil {
				t.Fatalf("mask_spans_v1 unsupported media not fully withheld: %v", err)
			}
			// 선택 관찰 투영(ProjectTrustedSelected)은 응답 완료·감사 완료 관찰만 받는다. 실제 보낸 계정 판본이 결속돼야 한다.
			if len(observed) != 1 || observed[0].ResponseComplete != tc.wantComplete || observed[0].RequestAccountRef != policy.SelectedAccountRef ||
				observed[0].RequestAuthContext != "cookie" || !observed[0].AuditComplete {
				t.Fatalf("selected observation: %+v", observed)
			}
			// 모델 출력·저장 기록 가림: 비밀번호는 model_visible_v1에서 그대로, 이전 정책에서는 모델 출력에서만 가린다.
			text := fmt.Sprintf("password=%s session=%s", tc.password, session)
			output := string(lease.Execution.RedactOutput([]byte(text)))
			if visible := tc.mode == e2proxy.PasswordResponseModelVisible; strings.Contains(output, tc.password) != visible || !strings.Contains(output, session) {
				t.Fatalf("model output masking (%s): %s", tc.mode, output)
			}
			if record := string(lease.Execution.RedactRecord([]byte(text))); record != text {
				t.Fatalf("product record masked password or session: %s", record)
			}
			// 우리 제어 비밀·E③ 표식은 두 정책 모두 모델 출력·저장 기록에서 가리고, 제어 비밀이 비친 응답은 통째로 보류한다.
			const controlSecret, marker = "synthetic-control-secret-77ee", "e3markersynthetic0123456789abcdz"
			if lease.Execution.AddProtectedValues([]byte(controlSecret)) != nil || lease.Execution.RegisterProgramMarker([]byte(marker)) != nil {
				t.Fatal("control secret or marker registration failed")
			}
			for _, out := range [][]byte{lease.Execution.RedactOutput([]byte(controlSecret + " " + marker)), lease.Execution.RedactRecord([]byte(controlSecret + " " + marker))} {
				if strings.Contains(string(out), controlSecret) || strings.Contains(string(out), marker) {
					t.Fatalf("control secret or E3 marker exposed: %s", out)
				}
			}
			if response, receipt, _, err := read("/read/secret"); !errors.Is(err, e2proxy.ErrWithheld) || response != nil || receipt.ResponseComplete {
				t.Fatalf("control secret reflection reached the tool: %+v %v", receipt, err)
			}
			if reads.Load() != 2 {
				t.Fatalf("selected read count=%d", reads.Load())
			}
		})
	}
}
