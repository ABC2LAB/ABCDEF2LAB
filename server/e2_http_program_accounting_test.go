package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
)

func TestAuthCallsDoNotContaminateWorkerIntent(t *testing.T) {
	for _, mode := range []string{"authenticated", "login_failed", "result_unknown"} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					io.WriteString(w, "owned-worker-response")
					return
				}
				posts.Add(1)
				switch mode {
				case "authenticated":
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "synthetic-session-611"})
					io.WriteString(w, "ok")
				case "login_failed":
					w.WriteHeader(http.StatusUnauthorized)
					io.WriteString(w, "rejected")
				case "result_unknown":
					c, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						c.Close()
					}
				}
			}))
			defer target.Close()
			dir := authTestDir(t)
			pin := authCredentialPin(t, dir, "account-611.json", "tester@owned.test", "synthetic-password-611")
			rec := &authEventCounter{}
			e := authTestExecution(t, target.URL, rec)
			defer e.Stop(context.Background())
			prep, err := e2HTTPAuthPrep(t.Context(), e, dir, target.URL, cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: pin}))
			if err != nil || prep.AccountAuth["tester-a"] != mode || posts.Load() != 1 || len(prep.CallRefs) != 1 {
				t.Fatal("auth fixture failed", err, prep.AccountAuth)
			}
			if counts := e.SnapshotForIntent(1).IntentCounts; counts != (e2proxy.Counts{}) {
				t.Errorf("program call contaminated unused Worker intent: calls=%d sent=%d unknown=%d", counts.Calls, counts.ForwardAttempted, counts.Unknown)
			}
			call, err := e.BeginCallForIntent(t.Context(), 1, "worker-611")
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest("GET", target.URL+"/read", nil)
			resp, receipt, err := call.Do(t.Context(), req)
			if err != nil {
				t.Fatal("worker fixture request failed", err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			closed, err := call.Close(t.Context())
			if err != nil || !closed.Final || !closed.AuditComplete || !receipt.ResponseComplete {
				t.Fatal("worker did not finalize", err)
			}
			view := e.SnapshotForIntent(1)
			if view.ProgramCounts.Calls != 1 || view.ProgramCounts.ForwardAttempted != 1 || view.ProgramCounts.Responses != authProgramResponseCount(mode) || view.ProgramCounts.Unknown != 1-authProgramResponseCount(mode) {
				t.Error("program counts missing", view.ProgramCounts)
			}
			if policy := e.Policy(); !policy.BudgetAvailable || policy.RemainingRequests != 18 {
				t.Error("program request omitted from cumulative budget")
			}
			for _, c := range e.Calls() {
				if c.CallRef == prep.CallRefs[0] && (c.Program != e2proxy.ProgramAuthPrep || c.IntentID != 0 || !c.Final || !c.AuditComplete) {
					t.Error("program final attribution/audit missing")
				}
			}
			if view.Counts.Calls != 2 || view.Counts.ForwardAttempted != 2 || view.Counts.Responses != 1+authProgramResponseCount(mode) {
				t.Error("overall calls/requests lost")
			}
			if view.IntentCounts.Calls != 1 || view.IntentCounts.ForwardAttempted != 1 || view.IntentCounts.Responses != 1 || view.IntentCounts.Unknown != 0 {
				t.Errorf("normal Worker inherited auth result: calls=%d sent=%d responses=%d unknown=%d", view.IntentCounts.Calls, view.IntentCounts.ForwardAttempted, view.IntentCounts.Responses, view.IntentCounts.Unknown)
			}
		})
	}
}

func authProgramResponseCount(mode string) uint64 {
	if mode == "result_unknown" {
		return 0
	}
	return 1
}

// 두 계정 준비·재로그인과 두 Worker의 실제 HTTP 결과를 독립적으로 만든다.
// 인증 불명만 있는 경우·Worker만 불명인 경우·둘 다 불명인 경우를 같은 예산/감사 식으로 대조한다.
func TestProgramAndWorkerAccountingMatrix(t *testing.T) {
	for _, phase := range []string{"initial-two-accounts", "refresh"} {
		for _, authResult := range []string{E2HTTPAuthAuthenticated, E2HTTPAuthLoginFailed, E2HTTPAuthResultUnknown} {
			for _, workerUnknown := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/worker-unknown-%t", phase, authResult, workerUnknown), func(t *testing.T) {
					var posts, gets atomic.Int64
					target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method == "POST" {
							posts.Add(1)
						} else {
							gets.Add(1)
						}
						if r.Method == "POST" && authResult == E2HTTPAuthResultUnknown || r.URL.Path == "/read/first" && workerUnknown {
							conn, _, err := w.(http.Hijacker).Hijack()
							if err == nil {
								conn.Close()
							}
							return
						}
						if r.Method == "POST" {
							if authResult == E2HTTPAuthLoginFailed {
								w.WriteHeader(401)
							} else {
								http.SetCookie(w, &http.Cookie{Name: "session", Value: "synthetic-program-session"})
							}
						}
						io.WriteString(w, "owned")
					}))
					defer target.Close()
					dir := authTestDir(t)
					a := authCredentialPin(t, dir, "a.json", "tester-a@owned.test", "synthetic-password-account-a")
					b := authCredentialPin(t, dir, "b.json", "tester-b@owned.test", "synthetic-password-account-b")
					auth := cookieAuth(E2HTTPFileAuthAccount{Label: "tester-a", Credential: a}, E2HTTPFileAuthAccount{Label: "tester-b", Credential: b})
					rec := &authEventCounter{}
					e := authTestExecution(t, target.URL, rec)
					defer e.Stop(context.Background())
					if err := e.RegisterIntent(t.Context(), 2); err != nil {
						t.Fatal(err)
					}
					var programRequests uint64 = 2
					kind := e2proxy.ProgramAuthPrep
					program := newE2HTTPProgramCalls()
					if phase == "refresh" {
						programRequests, kind = 1, e2proxy.ProgramRefreshSession
						_, result := e2HTTPRefreshSession(t.Context(), e, dir, target.URL, auth, &e2HTTPRefreshCounter{}, program, "tester-a")
						if result != authResult {
							t.Fatal("refresh fixture", result)
						}
					} else {
						prep, err := e2HTTPAuthPrep(t.Context(), e, dir, target.URL, auth)
						if err != nil || prep.AccountAuth["tester-a"] != authResult || prep.AccountAuth["tester-b"] != authResult {
							t.Fatal("initial fixture", err, prep.AccountAuth)
						}
						program = newE2HTTPProgramCalls(prep.CallRefs...)
					}
					for id := int64(1); id <= 2; id++ {
						call, err := e.BeginCallForIntent(t.Context(), id, fmt.Sprintf("worker-%d", id))
						if err != nil {
							t.Fatal(err)
						}
						path := "/read/second"
						if id == 1 {
							path = "/read/first"
						}
						req, _ := http.NewRequest("GET", target.URL+path, nil)
						resp, receipt, err := call.Do(t.Context(), req)
						unknown := id == 1 && workerUnknown
						if (err != nil) != unknown || unknown && receipt.Reason != e2proxy.Unknown {
							t.Fatal("Worker fixture result", err, receipt.Reason)
						}
						if resp != nil {
							io.Copy(io.Discard, resp.Body)
							resp.Body.Close()
						}
						result, err := call.Close(t.Context())
						if err != nil || !result.Final || !result.AuditComplete {
							t.Fatal("Worker closure", err)
						}
					}
					v := e.SnapshotForIntent(1)
					second := e.SnapshotForIntent(2)
					var authUnknown, workerUnknownCount uint64
					if authResult == E2HTTPAuthResultUnknown {
						authUnknown = programRequests
					}
					if workerUnknown {
						workerUnknownCount = 1
					}
					if v.IntentCounts.Calls != 1 || v.IntentCounts.Unknown != workerUnknownCount || v.IntentCounts.ForwardAttempted != 1 || second.IntentCounts.Calls != 1 || second.IntentCounts.Unknown != 0 || second.IntentCounts.Responses != 1 {
						t.Fatal("auth/other intent contaminated Worker projection", v.IntentCounts, second.IntentCounts)
					}
					if v.ProgramCounts.Calls != 1 || v.ProgramCounts.Unknown != authUnknown || v.ProgramCounts.ForwardAttempted != programRequests || v.ProgramCounts.Responses != programRequests-authUnknown {
						t.Fatal("program projection lost result", v.ProgramCounts)
					}
					if v.Counts.Calls != v.ProgramCounts.Calls+v.IntentCounts.Calls+second.IntentCounts.Calls || v.Counts.Unknown != authUnknown+workerUnknownCount || v.Counts.Attempts != programRequests+2 || v.Counts.ForwardAttempted != programRequests+2 || posts.Load() != int64(programRequests) || gets.Load() != 2 {
						t.Fatal("global accounting or no-retry invariant failed", v.Counts, posts.Load(), gets.Load())
					}
					if p := e.Policy(); !p.BudgetAvailable || p.RemainingRequests != 20-programRequests-2 {
						t.Fatal("global request budget mismatch")
					}
					calls := e.Calls()
					if len(calls) != 3 {
						t.Fatal("call count mismatch")
					}
					closed := map[string]bool{}
					for _, ev := range e.Events(0, 100) {
						if ev.Stage == "call_closed" {
							closed[ev.CallRef] = true
						}
						for _, c := range calls {
							if ev.CallRef == c.CallRef && (ev.Program != c.Program || ev.IntentID != c.IntentID) {
								t.Fatal("audit attribution mismatch")
							}
						}
					}
					if len(closed) != 3 {
						t.Fatal("audit finalization missing")
					}
					for _, c := range calls {
						if c.Program != "" {
							if c.Program != kind {
								t.Fatal("wrong program kind")
							}
							if settled, isProgram := firstDiagnosisProgramCall(c, program.snapshot(), nil); !settled || !isProgram {
								t.Fatal("program call omitted from supervisor reconciliation")
							}
						}
					}
				})
			}
		}
	}
}
