//go:build linux

package agent

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

const sdkEvaluationCommandPrefix = "echo sdk-evaluation-fixture "

type sdkEvaluationCommand struct {
	Operation     string `json:"operation"`
	Authorization string `json:"authorization,omitempty"`
	Cookie        string `json:"cookie,omitempty"`
}

type sdkEvaluationLogin struct {
	Token  string `json:"token"`
	Cookie string `json:"cookie"`
}

// 실제 모델 대신 직전 SDK 도구 응답을 읽어 다음 합성 입력을 만드는 결정적 대역이다.
type sdkEvaluationProvider struct {
	calls       int
	probeBudget bool
	read        sdkEvaluationCommand
	results     []e2runner.Result
}

func (p *sdkEvaluationProvider) Complete(ctx context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if err := ctx.Err(); err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	p.calls++
	if len(request.Tools) != 1 || request.Tools[0].Name != "Bash" {
		return llm.Message{}, "", llm.Usage{}, errors.New("fixture expected one bounded Bash tool")
	}
	if p.calls > 1 {
		var last string
		for _, message := range request.Messages {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolResult {
					for _, content := range block.Content {
						if content.Type == llm.BlockText {
							last = content.Text
						}
					}
				}
			}
		}
		var result e2runner.Result
		if last == "" || json.Unmarshal([]byte(last), &result) != nil {
			return llm.Message{}, "", llm.Usage{}, errors.New("fixture did not receive the SDK tool result")
		}
		p.results = append(p.results, result)
		if p.calls == 2 {
			var login sdkEvaluationLogin
			if json.Unmarshal([]byte(result.ToolOutput), &login) != nil || login.Token == "" || login.Cookie == "" {
				return llm.Message{}, "", llm.Usage{}, errors.New("fixture login output was unavailable")
			}
			p.read = sdkEvaluationCommand{Operation: "read", Authorization: "Bearer " + login.Token, Cookie: "fixture=" + login.Cookie}
		}
	}
	var command sdkEvaluationCommand
	switch {
	case p.calls == 1:
		command.Operation = "login"
	case p.calls == 2 || p.calls == 3 && p.probeBudget:
		command = p.read
	default:
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("synthetic completion")}}, "end_turn", llm.Usage{}, nil
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	input, err := json.Marshal(map[string]string{"command": sdkEvaluationCommandPrefix + base64.RawURLEncoding.EncodeToString(payload)})
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, err
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{
		Type: llm.BlockToolUse, ID: "eval-bash-" + strconv.Itoa(p.calls), Name: "Bash", Input: input,
	}}}, "tool_use", llm.Usage{}, nil
}

func (p *sdkEvaluationProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("fixture requires nonstreaming SDK execution"))
	}
}

// 실제 SDK·전송 코어·소유 HTTP/TLS 대상·내구 저널을 연결한다.
// private run 어댑터와 프로세스 종료 표시는 합성 대역이다. Bash/Runner/격리·배치,
// 실제 인증·모델/API·평가 회차 및 회차 저장소 전체의 비밀 부재는 증명하지 않는다.
func TestE2HTTPEvaluationCallerSDKRoundTrip(t *testing.T) {
	const token = "synthetic-sdk-target-token-381a"
	const cookie = "synthetic-sdk-target-cookie-6d92"
	cases := []struct {
		name        string
		mode        e2proxy.HeaderMode
		backend     string
		allow       bool
		probeBudget bool
	}{
		{name: "default"},
		{name: "protected", mode: e2proxy.HeaderProtectedSessionProposal},
		{name: "local", mode: e2proxy.HeaderEvaluationCallerPassthrough, backend: e2proxy.EvaluationLLMBackendLocal, allow: true},
		{name: "api", mode: e2proxy.HeaderEvaluationCallerPassthrough, backend: e2proxy.EvaluationLLMBackendAPI, allow: true},
		{name: "budget", mode: e2proxy.HeaderEvaluationCallerPassthrough, backend: e2proxy.EvaluationLLMBackendLocal, allow: true, probeBudget: true},
	}
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					var logins, reads atomic.Int64
					handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/fixture/login":
							logins.Add(1)
							if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
								t.Error("합성 로그인에 host 자격이 주입됨")
							}
							w.Header().Set("Content-Type", "application/json")
							if err := json.NewEncoder(w).Encode(sdkEvaluationLogin{Token: token, Cookie: cookie}); err != nil {
								t.Error("합성 응답 전송 실패")
							}
						case "/fixture/read":
							reads.Add(1)
							if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Cookie") != "fixture="+cookie {
								t.Error("SDK가 전달한 caller 자격이 원래 응답과 다름")
							}
							_, _ = io.WriteString(w, "owned authenticated response")
						default:
							t.Error("등록 범위 밖 fixture 요청")
							w.WriteHeader(http.StatusNotFound)
						}
					})
					var target *httptest.Server
					var roots *x509.CertPool
					if scheme == "https" {
						target = httptest.NewTLSServer(handler)
						roots = x509.NewCertPool()
						roots.AddCert(target.Certificate())
					} else {
						target = httptest.NewServer(handler)
					}
					defer target.Close()
					origin, err := url.Parse(target.URL)
					if err != nil {
						t.Fatal(err)
					}
					ip, err := netip.ParseAddr(origin.Hostname())
					if err != nil {
						t.Fatal(err)
					}
					root := t.TempDir()
					if err := os.Chmod(root, 0700); err != nil {
						t.Fatal("합성 저널 디렉터리 권한 설정 실패")
					}
					keyBytes := bytes.Repeat([]byte{5}, 32)
					journal, err := e2proxy.CreateJournal(root, "sdk.audit", keyBytes)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := journal.Close(); err != nil {
							t.Error("합성 저널 종료 실패")
						}
					}()
					ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 2})
					if err != nil {
						t.Fatal(err)
					}
					now := time.Now()
					key := budget.Key{TaskRef: "7", BudgetRef: "sdk-budget"}
					binding := budget.Binding{PolicyRef: "sdk-policy", ResetRef: "sdk-reset"}
					if err := ledger.Register(budget.Grant{Key: key, Binding: binding, MaxRequests: 2, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}, now); err != nil {
						t.Fatal(err)
					}
					scope := transmit.Scope{TaskRef: "7", RunRef: "sdk-" + scheme + "-" + tc.name, CaseRef: "sdk-case", ResetRef: "sdk-reset", PolicyRef: "sdk-policy"}
					cfg := e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
						HeaderMode: tc.mode, Scope: scope, IntentID: 22, OwnerRef: "ARTEX", PolicyRevision: "sdk-revision",
						AccountRef: "fixture-account", SessionRef: "fixture-session", ExpiresAt: now.Add(time.Minute),
						Rules:  []e2proxy.Rule{{Ref: "sdk-rule", Origin: target.URL, ConnectIP: ip, PathPrefix: "/fixture", PathScopeRef: "sdk-path", Methods: []string{"GET"}}},
						Budget: transmit.CumulativeBudget{Ledger: ledger, Key: key, Binding: binding}, Recorder: journal, RootCAs: roots,
						Limits: e2proxy.Limits{MaxAttempts: 8, MaxCalls: 4, MaxConcurrent: 1, MaxBodyBytes: 4096, RequestTimeout: 2 * time.Second, CallTimeout: 5 * time.Second, MaxDenials: 4, MaxTransient: 4, MaxUnknown: 4},
					}
					if tc.allow {
						cfg.Evaluation = &e2proxy.EvaluationCallerContext{RunRef: scope.RunRef, LLMBackend: tc.backend, TargetKind: e2proxy.EvaluationPublicPractice}
					}
					execution, err := e2proxy.NewExecution(t.Context(), cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
						defer closeCancel()
						if err := execution.Stop(closeCtx); err != nil {
							t.Error("합성 실행 정리 실패")
						}
					}()
					adapterCalls := 0
					run := func(ctx context.Context, encoded string) (e2runner.Result, error) {
						adapterCalls++
						if !strings.HasPrefix(encoded, sdkEvaluationCommandPrefix) {
							return e2runner.Result{}, errors.New("fixture command prefix missing")
						}
						payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, sdkEvaluationCommandPrefix))
						if err != nil {
							return e2runner.Result{}, errors.New("fixture command encoding invalid")
						}
						var command sdkEvaluationCommand
						if json.Unmarshal(payload, &command) != nil || command.Operation != "login" && command.Operation != "read" {
							return e2runner.Result{}, errors.New("fixture operation invalid")
						}
						call, err := execution.BeginCall(ctx, "sdk-command-"+strconv.Itoa(adapterCalls))
						if err != nil {
							return e2runner.Result{}, err
						}
						defer func() {
							closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
							defer closeCancel()
							if _, err := call.Close(closeCtx); err != nil {
								t.Error("합성 Call 정리 실패")
							}
						}()
						req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL+"/fixture/"+command.Operation, nil)
						if err != nil {
							return e2runner.Result{}, err
						}
						if command.Authorization != "" {
							req.Header.Set("Authorization", command.Authorization)
						}
						if command.Cookie != "" {
							req.Header.Set("Cookie", command.Cookie)
						}
						response, _, requestErr := call.Do(ctx, req)
						var output []byte
						if response != nil {
							output, err = io.ReadAll(response.Body)
							closeErr := response.Body.Close()
							if err != nil || closeErr != nil {
								return e2runner.Result{}, errors.New("fixture response read failed")
							}
						}
						closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
						defer cancel()
						callResult, closeErr := call.Close(closeCtx)
						if closeErr != nil {
							return e2runner.Result{}, closeErr
						}
						// 프로세스/프록시 종료 필드는 기존 private 어댑터용 합성 값이다.
						// Call의 실제 송신·거부·감사 건수만 전송 코어에서 가져온다.
						result := completedE2WorkerResult()
						result.Trusted.Call = callResult
						result.ToolOutput = string(output)
						if requestErr != nil {
							if !errors.Is(requestErr, e2proxy.ErrDenied) {
								return e2runner.Result{}, requestErr
							}
							result.Trusted.ExitCode = 22
							result.ToolOutput = "fixture request unavailable"
						}
						return result, nil
					}
					provider := &sdkEvaluationProvider{probeBudget: tc.probeBudget}
					worker := NewWorker(provider, "deterministic-evaluation-fixture", root, nil, 10000, 5)
					state := &e2HTTPToolState{}
					tool := e2WorkerBashTool(t, execution, run, 5*time.Second, state)
					opts := agentcore.Options{Provider: provider, Tools: []actool.CoreTool{tool}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 5, MaxDuration: 10 * time.Second, NonStreaming: true}
					ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
					defer cancel()
					reason, writes, runErr := worker.runE2HTTPSession(ctx, "fixture-worker", &db.Node{ID: 22, Kind: db.KindIntent}, nil, opts, "Exercise the synthetic evaluation fixture.", execution, state, nil)
					wantError := error(nil)
					wantModelCalls, wantAdapterCalls := 3, 2
					wantReads, wantRemaining := int64(0), uint64(1)
					lastClass := "unsupported"
					if tc.allow {
						wantReads, wantRemaining, lastClass = 1, 0, "completed"
					} else {
						wantError = ErrE2HTTPUnsupported
					}
					if tc.probeBudget {
						wantError, wantModelCalls, wantAdapterCalls, lastClass = ErrE2HTTPPolicyLimited, 4, 3, "policy_not_executed"
					}
					if !errors.Is(runErr, wantError) || reason != harness.ReasonCompleted || writes != (WriteCounts{}) || provider.calls != wantModelCalls || adapterCalls != wantAdapterCalls ||
						logins.Load() != 1 || reads.Load() != wantReads || len(provider.results) != wantAdapterCalls {
						t.Fatalf("SDK 경계 불일치 error=%v reason=%s model=%d adapter=%d login=%d read=%d results=%d", runErr, reason, provider.calls, adapterCalls, logins.Load(), reads.Load(), len(provider.results))
					}
					if provider.results[len(provider.results)-1].Trusted.Classification != lastClass {
						t.Fatal("모델 완료 문자열이 프로그램의 실제 분류를 덮음")
					}
					mode := tc.mode
					if mode == "" {
						mode = e2proxy.HeaderPreserve
					}
					refs := map[string]bool{}
					for _, result := range provider.results {
						trusted := result.Trusted
						if trusted.Call.CallRef == "" || refs[trusted.Call.CallRef] || trusted.Call.IntentID != 22 || !trusted.Call.Final || !trusted.Call.AuditComplete ||
							trusted.Execution.Scope != scope || trusted.Execution.HeaderMode != mode || !trusted.Execution.AuditComplete {
							t.Fatal("SDK 결과의 실제 Call/실행 귀속 또는 모드가 유실됨")
						}
						refs[trusted.Call.CallRef] = true
						if tc.allow {
							if trusted.Execution.Evaluation == nil || *trusted.Execution.Evaluation != *cfg.Evaluation {
								t.Fatal("실제 실행의 평가 선언이 SDK에 전달되지 않음")
							}
						} else if trusted.Execution.Evaluation != nil {
							t.Fatal("기본/보호 모드에 평가 선언이 생김")
						}
						data, err := json.Marshal(trusted)
						if err != nil || bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte(cookie)) {
							t.Fatal("신뢰 결과 projection에 합성 caller 자격이 포함됨")
						}
					}
					snapshot := execution.Snapshot()
					wantAllowed := uint64(1 + wantReads)
					wantDenied, wantUnsupported := uint64(0), uint64(0)
					if !tc.allow {
						wantUnsupported = 1
					}
					if tc.probeBudget {
						wantDenied = 1
					}
					if snapshot.Counts.Attempts != uint64(wantAdapterCalls) || snapshot.Counts.Allowed != wantAllowed || snapshot.Counts.ForwardAttempted != wantAllowed ||
						snapshot.Counts.Responses != wantAllowed || snapshot.Counts.Denied != wantDenied || snapshot.Counts.Unsupported != wantUnsupported || snapshot.Counts.Unknown != 0 {
						t.Fatalf("SDK 수용과 실제 전송 건수가 다름 attempts=%d allowed=%d denied=%d unsupported=%d", snapshot.Counts.Attempts, snapshot.Counts.Allowed, snapshot.Counts.Denied, snapshot.Counts.Unsupported)
					}
					if remaining, err := ledger.Remaining(key, binding, time.Now()); err != nil || remaining != wantRemaining {
						t.Fatal("도구 호출이 누적 예산을 갱신하거나 거부가 예산을 소비함")
					}
					stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
					defer stopCancel()
					if err := execution.Stop(stopCtx); err != nil {
						t.Fatal("합성 실행 종료 실패")
					}
					path := filepath.Join(root, "sdk.audit")
					input, err := os.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					summary, verifyErr := e2proxy.VerifyJournal(t.Context(), input, keyBytes)
					if err := input.Close(); err != nil {
						t.Fatal("합성 저널 조회 종료 실패")
					}
					if verifyErr != nil || summary.Events == 0 {
						t.Fatal("실제 코어 저널의 무결성 검증 실패")
					}
					data, err := os.ReadFile(path)
					if err != nil || bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte(cookie)) {
						t.Fatal("감사 저널에 합성 caller 자격이 기록됨")
					}
				})
			}
		})
	}
}
