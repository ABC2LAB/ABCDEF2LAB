//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"iter"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
	"github.com/Autumn-27/norma/llm"
)

const firstDiagnosisE2Secret = "synthetic-first-diagnosis-session-canary"

func firstDiagnosisPrivateTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "first-e2-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error("소유 인수시험 디렉터리 정리 실패")
		}
	})
	return dir
}

func firstDiagnosisNativeHelper(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux fixture 신원이 필요함")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl 시험 실행 파일이 없음")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	probe := exec.CommandContext(ctx, "unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount", "--net", "--ipc", "--uts", "true")
	output, err := probe.CombinedOutput()
	if err != nil {
		if bytes.Contains(output, []byte("Operation not permitted")) {
			t.Skip("환경이 필요한 비특권 namespace를 거부함")
		}
		t.Fatal("Linux namespace 사전 점검 실패")
	}
	helper := filepath.Join(firstDiagnosisPrivateTemp(t), "e2-bash-sandbox")
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-buildvcs=false", "-o", helper, "../cmd/e2-bash-sandbox")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	if _, err := build.CombinedOutput(); err != nil {
		t.Fatal("실제 sandbox helper 소스 빌드 실패")
	}
	return helper
}

func firstDiagnosisActualTask(t *testing.T) (*Manager, *Task, *db.Node) {
	t.Helper()
	dsn := os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_DSN")
	if dsn == "" {
		t.Skip("전용 첫 진단 PostgreSQL DSN이 제공되지 않음")
	}
	if os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_OWNED") != "1" {
		t.Fatal("소유한 일회용 PostgreSQL guard가 필요함")
	}
	t.Setenv("ARTEX_PG_DSN", dsn)
	manager, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal("명시한 첫 진단 PostgreSQL fixture를 열 수 없음")
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error("인수시험 Manager 정리 실패")
		}
	})
	enabled, limit := manager.ConcurrencyLimit()
	if limit < 1 {
		limit = 1
	}
	if err := manager.SetConcurrency(false, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.SetConcurrency(enabled, limit); err != nil {
			t.Error("시험 동시성 설정 복원 실패")
		}
	})
	task, err := manager.CreateTask("first diagnosis acceptance", "owned synthetic read", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := manager.DeleteTask(task.ID, DeleteTaskOptions{}); err != nil {
			t.Error("소유 인수시험 task 삭제 실패")
		}
	})
	id, err := task.Store.AddIntent(map[string]any{"summary": "owned acceptance intent"}, 10, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := task.Store.GetNode(id)
	if err != nil || intent == nil {
		t.Fatal("실제 task의 intent를 읽을 수 없음")
	}
	return manager, task, intent
}

func firstDiagnosisExpectedE2() E2HTTPExpectedBinding {
	return E2HTTPExpectedBinding{RunRef: "first-acceptance-run", ResetRef: "first-acceptance-reset", PolicyRef: "first-acceptance-policy", CaseRef: "first-acceptance-case", PolicyRevision: "first-acceptance-revision", BudgetRef: "first-acceptance-budget"}
}

type firstDiagnosisE2Provider struct {
	command         string
	calls           int
	result          *e2runner.Result
	leaked          bool
	unsupportedTool bool
}

func (p *firstDiagnosisE2Provider) Complete(_ context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	encoded, err := json.Marshal(request)
	if err != nil {
		return llm.Message{}, "", llm.Usage{}, errors.New("fixture request encoding failed")
	}
	p.leaked = p.leaked || bytes.Contains(encoded, []byte(firstDiagnosisE2Secret))
	for _, tool := range request.Tools {
		switch tool.Name {
		case "Bash", "graph_overview", "list_facts", "node_detail", "get_worker_output", "list_goals", "record_fact",
			// 원본 Worker의 그래프 조회 도구(루프 복원 결정안 §2). 저장소 조회뿐이고 송신 권한을 늘리지 않는다.
			"list_findings", "list_assets", "search_all_worker_traces", "get_worker_trace":
		case "submit_diagnostic_candidate":
			// 같은 intent의 실제 관찰만 저장하는 로컬 도구이며 송신 권한을 늘리지 않는다.
		default:
			p.unsupportedTool = true
		}
	}
	if p.calls == 1 {
		input, err := json.Marshal(map[string]any{"command": p.command, "timeout_ms": 5000})
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "acceptance-bash", Name: "Bash", Input: input}}}, "tool_use", llm.Usage{}, nil
	}
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type != llm.BlockToolResult {
				continue
			}
			for _, content := range block.Content {
				if content.Type != llm.BlockText {
					continue
				}
				var result e2runner.Result
				if json.Unmarshal([]byte(content.Text), &result) == nil && result.Trusted.Version == e2proxy.Version {
					p.result = &result
				}
			}
		}
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("The owned fixture step is complete.")}}, "end_turn", llm.Usage{}, nil
}

func TestFirstDiagnosisE2ProviderToolBoundary(t *testing.T) {
	for _, tc := range []struct {
		name        string
		unsupported bool
	}{
		{name: "Bash"},
		{name: "graph_overview"},
		{name: "list_facts"},
		{name: "node_detail"},
		{name: "get_worker_output"},
		{name: "list_goals"},
		{name: "record_fact"},
		{name: "list_findings"},
		{name: "list_assets"},
		{name: "search_all_worker_traces"},
		{name: "get_worker_trace"},
		{name: "submit_diagnostic_candidate"},
		{name: "report_finding", unsupported: true},
		{name: "insert_assets", unsupported: true},
		{name: "WebFetch", unsupported: true},
		{name: "WebSearch", unsupported: true},
		{name: "Read", unsupported: true},
		{name: "Write", unsupported: true},
		{name: "Edit", unsupported: true},
		{name: "MultiEdit", unsupported: true},
		{name: "LS", unsupported: true},
		{name: "Glob", unsupported: true},
		{name: "Grep", unsupported: true},
		{name: "create_finding", unsupported: true},
		{name: "submit_diagnostic_candidate_extra", unsupported: true},
		{name: "unknown_tool", unsupported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &firstDiagnosisE2Provider{}
			_, _, _, err := provider.Complete(t.Context(), llm.CompletionRequest{
				Tools: []llm.ToolSchema{{Name: tc.name}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if provider.unsupportedTool != tc.unsupported {
				t.Fatalf("fixture tool boundary: %s unsupported=%v, want %v", tc.name, provider.unsupportedTool, tc.unsupported)
			}
		})
	}
}

type firstDiagnosisPlannerProbeProvider struct {
	firstDiagnosisE2Provider
	outboundToolExposed bool
	textOnly            bool
}

func (p *firstDiagnosisPlannerProbeProvider) Complete(ctx context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	for _, tool := range request.Tools {
		switch tool.Name {
		case "Bash", "WebFetch", "WebSearch", "Read", "Write", "Edit", "MultiEdit", "LS", "Glob", "Grep":
			p.outboundToolExposed = true
		}
	}
	if p.textOnly {
		p.calls++
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("No further authorized actions in this owned fixture.")}}, "end_turn", llm.Usage{}, nil
	}
	return p.firstDiagnosisE2Provider.Complete(ctx, request)
}

func (*firstDiagnosisE2Provider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("unexpected fixture streaming"))
	}
}

func firstDiagnosisAcquiredRunner(t *testing.T, ctx context.Context, request E2HTTPRequest, helper, target, runRef string) *e2runner.Runner {
	t.Helper()
	root := firstDiagnosisPrivateTemp(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	certificate := &x509.Certificate{SerialNumber: big.NewInt(73), Subject: pkix.Name{CommonName: "first diagnosis fixture"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := e2proxy.CreateJournal(root, "acceptance.audit", bytes.Repeat([]byte{0x43}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Error("소유 인수시험 저널 정리 실패")
		}
	})
	expected := firstDiagnosisExpectedE2()
	grantKey := budget.Key{TaskRef: request.TaskID, BudgetRef: expected.BudgetRef}
	binding := budget.Binding{PolicyRef: expected.PolicyRef, ResetRef: expected.ResetRef}
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Register(budget.Grant{Key: grantKey, Binding: binding, MaxRequests: 4, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}, now); err != nil {
		t.Fatal(err)
	}
	execution, err := e2proxy.NewExecution(ctx, e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		Scope:    transmit.Scope{TaskRef: request.TaskID, RunRef: runRef, CaseRef: expected.CaseRef, ResetRef: expected.ResetRef, PolicyRef: expected.PolicyRef},
		IntentID: request.IntentID, OwnerRef: "ARTEX", PolicyRevision: expected.PolicyRevision, AccountRef: "acceptance-account", SessionRef: "acceptance-session", HeaderMode: e2proxy.HeaderProtectedSessionProposal, ExpiresAt: now.Add(time.Minute),
		Rules:    []e2proxy.Rule{{Ref: "acceptance-rule", Origin: target, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/allowed", PathScopeRef: "acceptance-path", Methods: []string{"GET"}, CredentialHeaders: http.Header{"Authorization": []string{"Bearer " + firstDiagnosisE2Secret}}}},
		Recorder: journal, Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grantKey, Binding: binding},
		Limits: e2proxy.Limits{MaxAttempts: 8, MaxCalls: 8, MaxConcurrent: 1, MaxBodyBytes: 4096, RequestTimeout: 2 * time.Second, CallTimeout: 10 * time.Second, MaxDenials: 4, MaxTransient: 2, MaxUnknown: 2},
	})
	if err != nil {
		t.Fatal("인수시험 E2 실행 설정 거부")
	}
	runner, err := e2runner.New(e2runner.Config{Execution: execution, Helper: helper, RootDir: root, CACert: caPath, Proxy: e2proxy.ProxyConfig{CA: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, HeaderTimeout: time.Second, RequestTimeout: 3 * time.Second, MaxHeaderBytes: 8192, MaxConnections: 4}, CommandTimeout: 6 * time.Second, DrainTimeout: 5 * time.Second, OutputLimit: 4096})
	if err != nil {
		t.Fatal("인수시험 native Runner 설정 거부")
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Close(closeCtx); err != nil {
			t.Error("인수시험 Runner 정리 미완료")
		}
		if err := execution.Stop(closeCtx); err != nil {
			t.Error("인수시험 E2 감사 종료 미완료")
		}
	})
	return runner
}

func TestFirstDiagnosisE2ProductionRuntimeNativeAcceptance(t *testing.T) {
	// 실제 커널·Bash·전송 코어·DB task를 쓰지만 모델·권위·대상 내용은 합성이다.
	helper := firstDiagnosisNativeHelper(t)
	for _, mode := range []string{"allowed", "policy-denied", "target-http-denied", "wrong-run-binding", "foreign-intent", "revoked-before-work"} {
		t.Run(mode, func(t *testing.T) {
			manager, task, intent := firstDiagnosisActualTask(t)
			var hits, acquired, released atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+firstDiagnosisE2Secret {
					t.Error("승인 메서드나 보호 세션이 달라짐")
				}
				if mode == "target-http-denied" {
					w.WriteHeader(http.StatusForbidden)
				}
				if _, err := w.Write([]byte("owned fixture response")); err != nil {
					t.Error("소유 대상 응답 기록 실패")
				}
			}))
			defer target.Close()
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			expected := firstDiagnosisExpectedE2()
			var actual *e2runner.Runner
			runtime, err := NewE2HTTPRuntime(parent, E2HTTPRuntimeOptions{Expected: expected, Acquire: func(ctx context.Context, request E2HTTPRequest) (E2HTTPRunOptions, error) {
				acquired.Add(1)
				taskNumber, parseErr := strconv.ParseInt(task.ID, 10, 64)
				if parseErr != nil || request.TaskID != task.ID || request.TaskIDNumber != taskNumber || request.ExplorationID != task.ExpID || request.IntentID != intent.ID {
					t.Error("실제 task·intent 결속이 공급 요청에서 달라짐")
				}
				runRef := expected.RunRef
				if mode == "wrong-run-binding" {
					runRef = "foreign-acceptance-run"
				}
				actual = firstDiagnosisAcquiredRunner(t, ctx, request, helper, target.URL, runRef)
				return E2HTTPRunOptions{Runner: newLocalE2HTTPRun(actual), Release: func(context.Context) error { released.Add(1); return nil }}, nil
			}, MaxWorkerTurns: 4, MaxToolCalls: 4, WorkerTimeout: 10 * time.Second, MaxPlannerRounds: 4, MaxIntentRuns: 4, DrainTimeout: 5 * time.Second})
			if err != nil {
				t.Fatal("생산 E2 Runtime 생성 실패")
			}
			t.Cleanup(func() {
				ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				if err := runtime.Close(ctx); err != nil {
					t.Error("생산 E2 Runtime 종료 미완료")
				}
			})
			if err := runtime.AdmitTask(t.Context(), task); err != nil {
				t.Fatal("실제 task admission 거부")
			}
			planning, err := runtime.PreparePlanning(t.Context(), task)
			if err != nil {
				t.Fatal("Planner 통제 문맥 주입 실패")
			}
			if mode == "allowed" {
				probe := &firstDiagnosisPlannerProbeProvider{firstDiagnosisE2Provider: firstDiagnosisE2Provider{command: "curl --silent --show-error --max-time 2 " + target.URL + "/allowed"}}
				planner := agent.NewPlanner(probe, "synthetic-fixture", firstDiagnosisPrivateTemp(t), nil, 0, 3)
				planner.SetNonStreaming(func() bool { return true })
				taskNumber, parseErr := strconv.ParseInt(task.ID, 10, 64)
				if parseErr != nil {
					t.Fatal("실제 Planner task 번호 파싱 실패")
				}
				_, _, _ = planner.Plan(planning, taskNumber, nil, task.Store, task.Goal, nil, func(db.Activity) {})
				if probe.calls == 0 || probe.calls > 4 || probe.outboundToolExposed || probe.leaked {
					t.Fatal("원본 Planner의 선행 외부 도구가 비활성화되지 않거나 유한 종료/비밀 경계 위반")
				}
				if hits.Load() != 0 || acquired.Load() != 0 {
					t.Fatal("intent 전 원본 Planner의 미지원 Bash가 대상에 도달하거나 실행 권한을 미리 취득함")
				}
			}
			if mode == "revoked-before-work" {
				cancel()
			}
			requestedIntent := intent
			if mode == "foreign-intent" {
				foreign, createErr := manager.CreateTask("foreign acceptance task", "different exploration", nil, 0, 0)
				if createErr != nil {
					t.Fatal(createErr)
				}
				t.Cleanup(func() {
					if _, deleteErr := manager.DeleteTask(foreign.ID, DeleteTaskOptions{}); deleteErr != nil {
						t.Error("별도 소유 시험 task 삭제 실패")
					}
				})
				foreignID, createErr := foreign.Store.AddIntent(map[string]any{"summary": "foreign intent"}, 10, nil, "human")
				if createErr != nil {
					t.Fatal(createErr)
				}
				requestedIntent, createErr = foreign.Store.GetNode(foreignID)
				if createErr != nil || requestedIntent == nil {
					t.Fatal("별도 task의 실제 intent 조회 실패")
				}
			}
			ctx, finish, err := runtime.PrepareWork(t.Context(), task, requestedIntent)
			if mode == "wrong-run-binding" || mode == "foreign-intent" || mode == "revoked-before-work" {
				if err == nil {
					t.Fatal("미결속/회수된 실행이 Worker 문맥을 얻음")
				}
				if hits.Load() != 0 {
					t.Fatal("실행 거부 뒤 대상 송신 발생")
				}
				if finish != nil {
					if finishErr := finish(); finishErr != nil && !errors.Is(finishErr, agent.ErrE2HTTPBlocked) {
						t.Log("거부 경로 정리도 미완료로 보존됨")
					}
				}
				return
			}
			if err != nil || finish == nil {
				t.Fatal("생산 Runtime이 실제 Worker 결속을 공급하지 못함")
			}
			path := "/allowed"
			if mode == "policy-denied" {
				path = "/outside"
			}
			provider := &firstDiagnosisE2Provider{command: "curl --silent --show-error --max-time 2 '" + target.URL + path + "'"}
			worker := agent.NewWorker(provider, "deterministic-acceptance", firstDiagnosisPrivateTemp(t), nil, 12000, 4)
			worker.SetNonStreaming(func() bool { return true })
			taskNumber, _ := strconv.ParseInt(task.ID, 10, 64)
			_, _, runErr := worker.Execute(ctx, "worker", taskNumber, nil, nil, intent, nil, nil, nil, nil)
			if mode == "policy-denied" {
				if !errors.Is(runErr, agent.ErrE2HTTPPolicyLimited) {
					t.Fatalf("정책 거부가 별도 결과로 전달되지 않음: %v", runErr)
				}
			} else if runErr != nil {
				t.Fatalf("승인된 실제 Worker 실행 실패: %v", runErr)
			}
			if err := finish(); err != nil {
				t.Fatal("실제 호출 drain·감사 마감 실패")
			}
			if provider.leaked || provider.unsupportedTool {
				t.Fatal("모델 입력에 비밀이나 미지원 도구가 노출됨")
			}
			if provider.result == nil || !provider.result.Trusted.Final {
				t.Fatal("프로그램 신뢰 결과와 마감이 모델에 전달되지 않음")
			}
			want := int64(1)
			if mode == "policy-denied" {
				want = 0
			}
			counts := provider.result.Trusted.Call.Counts
			if hits.Load() != want || int64(counts.Responses) != want || int64(counts.ForwardAttempted) != want {
				t.Fatalf("실제 송신/신뢰 결과 불일치: hits=%d responses=%d forwards=%d", hits.Load(), counts.Responses, counts.ForwardAttempted)
			}
			if (mode == "policy-denied" && counts.Denied == 0) || (mode != "policy-denied" && counts.Denied != 0) {
				t.Fatal("정책 거부와 대상 HTTP 응답을 혼동함")
			}
			execution, ok := runtime.Execution(task.ID)
			if !ok || execution != actual.Execution() || len(execution.Calls()) != 1 || acquired.Load() != 1 {
				t.Fatal("새 실행/승인으로 호출 귀속을 바꿈")
			}
			results := runtime.Results(task.ID)
			calls := execution.Calls()
			if len(results) != 1 || len(calls) != 1 || !e2proxy.EqualCallResults(results[0].Call, calls[0]) || !results[0].Final || results[0].Execution.Scope.TaskRef != task.ID || results[0].Call.IntentID != intent.ID {
				t.Fatal("실제 Runner observer가 같은 실행·intent·call의 프로그램 결과를 보존하지 못함")
			}
			if mode == "allowed" {
				nextID, nextErr := task.Store.AddIntent(map[string]any{"summary": "second actual intent"}, 10, nil, "human")
				if nextErr != nil {
					t.Fatal(nextErr)
				}
				nextIntent, nextErr := task.Store.GetNode(nextID)
				if nextErr != nil || nextIntent == nil {
					t.Fatal("두 번째 실제 intent 조회 실패")
				}
				nextContext, nextFinish, nextErr := runtime.PrepareWork(t.Context(), task, nextIntent)
				if nextErr != nil || nextFinish == nil {
					t.Fatal("같은 실행의 두 번째 intent 준비 실패")
				}
				nextProvider := &firstDiagnosisE2Provider{command: "curl --silent --show-error --max-time 2 '" + target.URL + "/allowed'"}
				nextWorker := agent.NewWorker(nextProvider, "deterministic-acceptance", firstDiagnosisPrivateTemp(t), nil, 12000, 4)
				nextWorker.SetNonStreaming(func() bool { return true })
				if _, _, nextErr = nextWorker.Execute(nextContext, "worker", taskNumber, nil, nil, nextIntent, nil, nil, nil, nil); nextErr != nil {
					t.Fatalf("두 번째 intent의 실제 Worker 실패: %v", nextErr)
				}
				if nextErr = nextFinish(); nextErr != nil {
					t.Fatal("두 번째 intent 마감 실패")
				}
				if acquired.Load() != 1 || hits.Load() != 2 || len(execution.Calls()) != 2 || nextProvider.result == nil || nextProvider.result.Trusted.Call.IntentID != nextID || nextProvider.leaked || nextProvider.unsupportedTool {
					t.Fatal("여러 실제 intent가 같은 실행·승인·예산을 유지하지 못함")
				}
				remaining, budgetErr := execution.BudgetRemaining()
				if budgetErr != nil || remaining != 2 {
					t.Fatal("두 번째 intent에서 누적 예산을 재발급함")
				}
				want = 2
			}
			if err := runtime.CloseTask(t.Context(), task.ID); err != nil {
				t.Fatal("task 종료·공급 수명 반환 실패")
			}
			if _, _, err := runtime.PrepareWork(t.Context(), task, intent); err == nil {
				t.Fatal("종료된 task가 신규 Worker 문맥을 얻음")
			}
			if hits.Load() != want || released.Load() != 1 {
				t.Fatal("종료 뒤 송신 또는 공급 수명 중복 반환")
			}
		})
	}
}
