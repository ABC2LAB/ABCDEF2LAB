//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

func TestPasswordResponseFactoryPrepRefreshAndPolicyBinding(t *testing.T) {
	for _, mode := range []string{"", e2proxy.PasswordResponseMaskSpans} {
		t.Run(mode, func(t *testing.T) {
			var logins, reads atomic.Int64
			login := (&authLoginTarget{token: "synthetic-password-mask-session"}).handler(t)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" {
					logins.Add(1)
					login.ServeHTTP(w, r)
					return
				}
				if r.Header.Get("Cookie") != "session=synthetic-password-mask-session" {
					t.Error("대상 발급 세션 불일치")
				}
				reads.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"before":"public","value":"seeded-pass-88ff","after":7}`)
			}))
			defer target.Close()
			cfg, _, policy := e2HostPolicyFixture(t)
			policy.MaxRequests = 8
			policy.HeaderMode = e2proxy.HeaderTestAccountPassthrough
			policy.Rules[0].Origin = target.URL
			policy.Rules[0].PathPrefix = "/read"
			authRule := policy.Rules[0]
			authRule.Ref = "auth-login"
			authRule.PathScopeRef = "auth-login"
			authRule.PathPrefix = "/login"
			authRule.Methods = []string{"POST"}
			authRule.Purpose = e2proxy.PurposeAuthPrep
			authRule.Credentials = nil
			policy.Rules = append(policy.Rules, authRule)
			auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: authCredentialPin(t, cfg.AuthorityRoot, "mask-account.json", "tester@owned.test", "seeded-pass-88ff")})
			auth.PasswordResponse = mode
			policy.Auth = &auth
			raw, _ := json.Marshal(policy)
			if os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
				t.Fatal("정책 쓰기 실패")
			}
			_, fp, err := ValidateE2HTTPRunPolicyDocument(raw)
			if err != nil {
				t.Fatal("정본 정책 검증 실패", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			options, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx}, Supervisor: &e2HTTPSupervisorFixture{}})
			if err != nil || fp != options.PolicyFingerprintSHA256 {
				t.Fatal("factory·저장 문서의 지문 불일치", err)
			}
			run, err := options.Acquire(ctx, E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer run.Release(context.Background())
			if run.AccountAuth["tester-a"] != E2HTTPAuthAuthenticated || run.RefreshSession == nil {
				t.Fatal("인증 준비 연결 실패")
			}
			session, status := run.RefreshSession(ctx, "tester-a")
			if status != E2HTTPAuthAuthenticated || session.Get("Cookie") != "session=synthetic-password-mask-session" || logins.Load() != 2 {
				t.Fatal("재로그인 기능 변경")
			}
			call, err := run.Execution.BeginCall(ctx, "password-reflection")
			if err != nil {
				t.Fatal(err)
			}
			defer call.Close(context.Background())
			request, _ := http.NewRequest("GET", target.URL+"/read/page", nil)
			request.Header = session.Clone()
			response, receipt, err := call.Do(ctx, request)
			if mode == "" {
				if !errors.Is(err, e2proxy.ErrWithheld) || response != nil {
					t.Fatal("구형 전체 보류 변경")
				}
			} else {
				if err != nil || response == nil || receipt.Reason != e2proxy.Withheld || receipt.ResponseComplete {
					t.Fatal("실제 factory의 부분 가림 실패", err)
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if string(body) != `{"before":"public","value":"[REDACTED]","after":7}` {
					t.Fatal("비밀번호 가림·비민감 내용 유지 실패")
				}
			}
			counts := run.Execution.Snapshot().Counts
			if reads.Load() != 1 || counts.ForwardAttempted != 3 || counts.Responses != 3 || counts.Unknown != 0 {
				t.Fatal("실제 로그인·재로그인·조회 계수 변경")
			}
			// 이전 비밀번호 정책(보류·mask_spans_v1)은 모델 출력에서 비밀번호를 계속 가린다. 시험 계정 실행의 세션 값은 가리지
			// 않고, 저장 기록은 둘 다 가리지 않는다(절대규칙 2, 2026-11-26 DLP 폐기).
			const text = "seeded-pass-88ff synthetic-password-mask-session"
			if out := string(run.Execution.RedactOutput([]byte(text))); strings.Contains(out, "seeded-pass-88ff") || !strings.Contains(out, "synthetic-password-mask-session") {
				t.Fatalf("모델 출력 가림 범위: %s", out)
			}
			if out := string(run.Execution.RedactRecord([]byte(text))); out != text {
				t.Fatalf("저장 기록에서 비밀번호·세션을 가림: %s", out)
			}
			if _, err := call.Close(ctx); err != nil {
				t.Fatal("진단 호출 감사 마감 실패")
			}
			if err := run.Release(ctx); err != nil {
				t.Fatal("감사 파일 마감 실패")
			}
			key, err := os.ReadFile(filepath.Join(cfg.AuthorityRoot, policy.AuditKey.File))
			if err != nil {
				t.Fatal("합성 감사 키 읽기 실패")
			}
			defer clear(key)
			journal, err := os.Open(filepath.Join(policy.StateDir, "events.jsonl"))
			if err != nil {
				t.Fatal("감사 파일 읽기 실패")
			}
			defer journal.Close()
			events, summary, err := e2proxy.VerifyJournalEvents(ctx, journal, key)
			if err != nil || summary.Checkpoint.Version != e2proxy.Version {
				t.Fatal("기존 v2 감사 무결성 검증 실패", err)
			}
			results := 0
			for _, event := range events {
				if event.Stage == "result" {
					results++
					if !event.ResponseReceived || event.Reason == e2proxy.Unknown {
						t.Fatal("실제 수신과 감사 불일치")
					}
				}
			}
			if results != 3 {
				t.Fatal("인증 준비·재로그인·진단 감사 수 불일치")
			}
			audit, _ := json.Marshal(events)
			for _, value := range []string{"seeded-pass-88ff", "synthetic-password-mask-session"} {
				if strings.Contains(string(audit), value) {
					t.Fatal("비밀값이 감사에 남음")
				}
			}

			for _, bad := range []string{`null`, `false`, `""`, `"unknown"`, `12`} {
				mutated := strings.Replace(string(raw), `"password_response":"mask_spans_v1"`, `"password_response":`+bad, 1)
				if mode == "" {
					continue
				}
				if _, _, err := ValidateE2HTTPRunPolicyDocument([]byte(mutated)); err == nil {
					t.Fatal("잘못된 보류 정책을 기본값으로 수용")
				}
				if os.WriteFile(cfg.PolicyFile, []byte(mutated), 0600) != nil {
					t.Fatal("음성 정책 쓰기 실패")
				}
				if _, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx}, Supervisor: &e2HTTPSupervisorFixture{}}); err == nil {
					t.Fatal("factory가 잘못된 보류 정책 수용")
				}
			}
			if mode != "" {
				mutated := strings.Replace(string(raw), `"password_response":`, `"Password_Response":`, 1)
				if _, _, err := ValidateE2HTTPRunPolicyDocument([]byte(mutated)); err == nil {
					t.Fatal("대소문자 별칭 수용")
				}
			}
		})
	}
}
