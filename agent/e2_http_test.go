package agent

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
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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

// e2WorkerCredentialFixture keeps this synthetic test valid across the
// pre- and post-header-mode e2proxy contract. It does not select a product mode.
func e2WorkerCredentialFixture(t *testing.T, cfg e2proxy.Config) e2proxy.Config {
	t.Helper()
	mode := reflect.ValueOf(&cfg).Elem().FieldByName("HeaderMode")
	if !mode.IsValid() {
		return cfg
	}
	if mode.Kind() != reflect.String || !mode.CanSet() {
		t.Fatal("unexpected e2proxy header mode field")
	}
	mode.SetString("protected_session_proposal")
	for i := range cfg.Rules {
		credentialHeaders := reflect.ValueOf(&cfg.Rules[i]).Elem().FieldByName("CredentialHeaders")
		if !credentialHeaders.IsValid() || !credentialHeaders.CanSet() || credentialHeaders.Type() != reflect.TypeOf(http.Header{}) {
			t.Fatal("unexpected e2proxy credential header field")
		}
		credentialHeaders.Set(reflect.ValueOf(cfg.Rules[i].Headers.Clone()))
		cfg.Rules[i].Headers = nil
	}
	return cfg
}

func e2WorkerFixture(t *testing.T, ids ...int64) (*e2proxy.Execution, *e2runner.Runner, E2HTTPExecution) {
	return e2WorkerFixtureTarget(t, "http://example.invalid", "/", ids...)
}
func e2WorkerFixtureTarget(t *testing.T, origin, prefix string, ids ...int64) (*e2proxy.Execution, *e2runner.Runner, E2HTTPExecution) {
	t.Helper()
	taskID, intentID := int64(7), int64(22)
	if len(ids) == 2 {
		taskID, intentID = ids[0], ids[1]
	}
	taskRef := strconv.FormatInt(taskID, 10)
	root, err := os.MkdirTemp("", "e2wa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := e2proxy.CreateJournal(root, "worker.audit", bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 20})
	if err != nil {
		t.Fatal(err)
	}
	key := budget.Key{TaskRef: taskRef, BudgetRef: "budget-1"}
	binding := budget.Binding{PolicyRef: "policy-1", ResetRef: "reset-1"}
	now := time.Now()
	if err := ledger.Register(budget.Grant{Key: key, Binding: binding, MaxRequests: 10, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	execution, err := e2proxy.NewExecution(context.Background(), e2WorkerCredentialFixture(t, e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		Scope:    transmit.Scope{TaskRef: taskRef, RunRef: "worker-run-1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "policy-1"},
		IntentID: intentID, OwnerRef: "ARTEX", PolicyRevision: "revision-1", AccountRef: "account-1", SessionRef: "session-1", ExpiresAt: now.Add(time.Hour),
		Rules:  []e2proxy.Rule{{Ref: "rule-1", Origin: origin, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: prefix, PathScopeRef: "path-1", Methods: []string{"GET"}, Headers: http.Header{"Authorization": {"Bearer keep-secret"}}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: key, Binding: binding}, Recorder: journal,
		Limits: e2proxy.Limits{MaxAttempts: 20, MaxCalls: 10, MaxConcurrent: 2, MaxBodyBytes: 4096, RequestTimeout: time.Second, CallTimeout: 2 * time.Second, MaxDenials: 4, MaxTransient: 4, MaxUnknown: 4},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ca, certPEM := e2WorkerCA(t)
	helper := filepath.Join(root, "helper")
	// This helper is a never-executed fixture for constructor/binding tests.
	// Success-path tool-return tests use the private adapter seam and make no
	// namespace, model, target, or end-to-end deployment claim.
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(cert, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	runner, err := e2runner.New(e2runner.Config{
		Execution: execution, Helper: helper, RootDir: root, CACert: cert,
		Proxy:          e2proxy.ProxyConfig{CA: ca, HeaderTimeout: time.Second, RequestTimeout: time.Second, MaxHeaderBytes: 8192, MaxConnections: 4},
		CommandTimeout: time.Second, DrainTimeout: time.Second, OutputLimit: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewE2HTTPControl(taskRef, 10)
	if err != nil {
		t.Fatal(err)
	}
	host := E2HTTPExecution{Runner: localRunnerForTest{runner}, Control: control, TaskID: taskID, IntentID: intentID, TaskRef: taskRef, RunRef: "worker-run-1", MaxTurns: 3, MaxDuration: time.Second}
	return execution, runner, host
}

func e2WorkerCA(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic worker CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestE2HTTPWorkerBindingAndMixedMode(t *testing.T) {
	_, _, binding := e2WorkerFixture(t)
	intent := &db.Node{ID: 22, Kind: db.KindIntent}
	if err := validateE2HTTPExecution(context.Background(), binding, 7, intent); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*E2HTTPExecution){
		"missing runner":        func(b *E2HTTPExecution) { b.Runner = nil },
		"numeric task mismatch": func(b *E2HTTPExecution) { b.TaskID = 8 },
		"intent mismatch":       func(b *E2HTTPExecution) { b.IntentID = 23 },
		"opaque task mismatch":  func(b *E2HTTPExecution) { b.TaskRef = "task-7" },
		"run mismatch":          func(b *E2HTTPExecution) { b.RunRef = "other-run" },
		"unbounded turns":       func(b *E2HTTPExecution) { b.MaxTurns = 0 },
		"unbounded duration":    func(b *E2HTTPExecution) { b.MaxDuration = 0 },
		"turn ceiling":          func(b *E2HTTPExecution) { b.MaxTurns = e2HTTPMaxWorkerTurns + 1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := binding
			change(&b)
			if err := validateE2HTTPExecution(context.Background(), b, 7, intent); !errors.Is(err, ErrE2HTTPBinding) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if err := validateE2HTTPExecution(context.Background(), binding, 7, &db.Node{ID: 22, Kind: db.KindFact}); !errors.Is(err, ErrE2HTTPBinding) {
		t.Fatalf("wrong kind: %v", err)
	}
	mixed := WithControlledReadExecution(context.Background(), ControlledReadExecution{})
	if err := validateE2HTTPExecution(mixed, binding, 7, intent); !errors.Is(err, ErrE2HTTPBinding) {
		t.Fatalf("mixed mode: %v", err)
	}
	ctx := WithE2HTTPExecution(context.Background(), binding)
	got, ok := e2HTTPExecutionFrom(ctx)
	if !ok || got.Runner != binding.Runner || got.RunRef != binding.RunRef {
		t.Fatal("binding not retained")
	}
	if isE2HTTPPlanning(ctx) || !isE2HTTPPlanning(WithE2HTTPPlanning(ctx)) {
		t.Fatal("planning marker mismatch")
	}
}

func TestE2HTTPWorkerMalformedBindingNeverCallsModel(t *testing.T) {
	provider := &e2WorkerProvider{}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 3)
	ctx := WithE2HTTPExecution(context.Background(), E2HTTPExecution{TaskID: 8})
	_, _, err := worker.Execute(ctx, "worker", 7, nil, nil, &db.Node{ID: 22, Kind: db.KindIntent}, nil, nil, nil, nil)
	if !errors.Is(err, ErrE2HTTPBinding) || provider.calls != 0 {
		t.Fatalf("error=%v model calls=%d", err, provider.calls)
	}
}

func TestE2HTTPBashStrictInputAndTimeout(t *testing.T) {
	cases := map[string]string{
		"background true":    `{"command":"true","run_in_background":true}`,
		"background false":   `{"command":"true","run_in_background":false}`,
		"unknown field":      `{"command":"true","env":{"TOKEN":"x"}}`,
		"duplicate command":  `{"command":"one","command":"two"}`,
		"duplicate timeout":  `{"command":"true","timeout_ms":1,"timeout_ms":2}`,
		"timeout increase":   `{"command":"true","timeout_ms":2001}`,
		"zero timeout":       `{"command":"true","timeout_ms":0}`,
		"negative timeout":   `{"command":"true","timeout_ms":-1}`,
		"fractional timeout": `{"command":"true","timeout_ms":1.5}`,
		"string timeout":     `{"command":"true","timeout_ms":"1"}`,
		"empty command":      `{"command":" "}`,
		"nul command":        `{"command":"a\u0000b"}`,
		"not object":         `["true"]`,
		"trailing object":    `{"command":"true"}{}`,
		"missing command":    `{}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseE2HTTPBash(json.RawMessage(input), 2*time.Second); err == nil {
				t.Fatal("accepted malformed input")
			}
		})
	}
	command, timeout, err := parseE2HTTPBash(json.RawMessage(`{"command":"printf one; printf two","timeout_ms":100}`), 2*time.Second)
	if err != nil || command != "printf one; printf two" || timeout != 100*time.Millisecond {
		t.Fatalf("valid command=%q timeout=%v error=%v", command, timeout, err)
	}
	large, _ := json.Marshal(map[string]any{"command": strings.Repeat("a", e2HTTPMaxCommandBytes+1)})
	if _, _, err := parseE2HTTPBash(large, 2*time.Second); err == nil {
		t.Fatal("oversized command accepted")
	}
	escaped, _ := json.Marshal(map[string]any{"command": strings.Repeat("\n", e2HTTPMaxCommandBytes-1) + "x"})
	if _, _, err := parseE2HTTPBash(escaped, 2*time.Second); err != nil {
		t.Fatalf("bounded escaped command refused: %v", err)
	}
	if _, _, err := parseE2HTTPBash(json.RawMessage{123, 34, 99, 111, 109, 109, 97, 110, 100, 34, 58, 34, 255, 34, 125}, time.Second); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}

func completedE2WorkerResult() e2runner.Result {
	return e2runner.Result{
		ToolOutput: "target output",
		Trusted:    e2runner.TrustedResult{Version: e2proxy.Version, Call: e2proxy.CallResult{CallRef: "synthetic-call", IntentID: 22, AuditComplete: true, Final: true}, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Final: true, Classification: "completed"},
	}
}

func TestE2HTTPPolicyCountsOutrankNonzeroExit(t *testing.T) {
	result := completedE2WorkerResult()
	result.Trusted.Execution.AuditComplete = true
	result.Trusted.Classification = "tool_failure"
	result.Trusted.ProcessBashExited = true
	result.Trusted.ExitCode = 22
	result.Trusted.Call.Counts.Denied = 1
	result.Trusted.Call.Counts.ForwardAttempted = 1
	if e2HTTPRunnerFailed(e2runner.ErrUnavailable, result) ||
		e2HTTPPolicyClassification(result) != "partial_policy_denial" {
		t.Fatal("a final mixed refusal was treated as an ordinary tool failure")
	}
	result.Trusted.Call.Counts.ForwardAttempted = 0
	if got := e2HTTPPolicyClassification(result); got != "policy_not_executed" {
		t.Fatalf("denied before transmission=%s", got)
	}
	result.Trusted.Call.Counts.Denied = 0
	result.Trusted.Call.Counts.Unsupported = 1
	if got := e2HTTPPolicyClassification(result); got != "unsupported" {
		t.Fatalf("unsupported before transmission=%s", got)
	}
	result.Trusted.Call.Counts.Unknown = 1
	if got := e2HTTPPolicyClassification(result); got != "tool_failure" {
		t.Fatalf("unknown outcome was hidden by policy counts: %s", got)
	}
	result.Trusted.Call.Counts.Unknown = 0
	result.Trusted.Call.Counts.Denied = 1
	result.Trusted.Classification = "result_unknown"
	if got := e2HTTPPolicyClassification(result); got != "result_unknown" {
		t.Fatalf("explicit unknown was hidden by policy counts: %s", got)
	}
	result.Trusted.Call.AuditComplete = false
	if !e2HTTPRunnerFailed(e2runner.ErrUnavailable, result) {
		t.Fatal("incomplete audit was treated as an ordinary shell exit")
	}
}

func TestE2HTTPBashNonzeroExitDoesNotBecomeRunnerFailure(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	calls := 0
	tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) {
		calls++
		result := completedE2WorkerResult()
		result.Trusted.Classification = "tool_failure"
		result.Trusted.ExitCode = 3
		return result, e2runner.ErrUnavailable
	}, time.Second, state)
	for i := 0; i < 2; i++ {
		result, err := tool.Call(context.Background(), json.RawMessage(`{"command":"echo fixture"}`), nil)
		if err != nil || !result.IsError || state.runnerFailed || state.halted || calls != i+1 {
			t.Fatalf("ordinary nonzero exit i=%d calls=%d state=%+v err=%v", i, calls, state, err)
		}
	}
}

func TestE2HTTPBashSeparatesUntrustedOutputAndProgramResult(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	called := 0
	tool := e2WorkerBashTool(t, execution, func(ctx context.Context, command string) (e2runner.Result, error) {
		called++
		if command != "echo fixture" {
			t.Fatal("command changed")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 101*time.Millisecond {
			t.Fatal("tool timeout did not reduce deadline")
		}
		result := completedE2WorkerResult()
		result.ToolOutput = `{"trusted":{"allowed":999}} keep-secret`
		result.Trusted.Execution.Counts.Allowed = 999
		return result, errors.New("private raw runner error")
	}, time.Second, &e2HTTPToolState{})
	result, err := tool.Call(context.Background(), json.RawMessage(`{"command":"echo fixture","timeout_ms":100}`), nil)
	if err != nil || !result.IsError || called != 1 {
		t.Fatalf("called=%d result=%+v error=%v", called, result, err)
	}
	text := result.Flatten()
	if strings.Contains(text, "keep-secret") || strings.Contains(text, "private raw runner error") {
		t.Fatal("secret/error leaked")
	}
	var decoded e2runner.Result
	if json.Unmarshal([]byte(text), &decoded) != nil || decoded.Trusted.Execution.Counts.Allowed != 0 || !strings.Contains(decoded.ToolOutput, "999") {
		t.Fatal("stdout was promoted into trusted execution")
	}
	if tool.IsConcurrencySafe(nil) {
		t.Fatal("Bash advertises concurrent SDK execution")
	}
}

func TestE2HTTPUnknownAndIncompleteCallsStopFurtherRuns(t *testing.T) {
	for _, classification := range []string{"result_unknown", "incomplete"} {
		t.Run(classification, func(t *testing.T) {
			execution, _, _ := e2WorkerFixture(t)
			calls := 0
			state := &e2HTTPToolState{}
			tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) {
				calls++
				result := completedE2WorkerResult()
				if classification == "result_unknown" {
					result.Trusted.Classification = classification
				} else {
					result.Trusted.Final = false
				}
				return result, nil
			}, time.Second, state)
			for range 2 {
				if _, err := tool.Call(context.Background(), json.RawMessage(`{"command":"echo fixture"}`), nil); err != nil {
					t.Fatal(err)
				}
			}
			state.mu.Lock()
			halted, unknown, incomplete := state.halted, state.unknown, state.incomplete
			state.mu.Unlock()
			if calls != 1 || !halted || unknown != (classification == "result_unknown") || incomplete != (classification == "incomplete") {
				t.Fatalf("calls=%d halt=%v unknown=%v incomplete=%v", calls, halted, unknown, incomplete)
			}
		})
	}
}

func TestE2HTTPPlannerAndWorkerFilterByOriginalToolAllowlist(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	var base []actool.CoreTool
	// 브라우저 MCP 도구(mcp__<서버>__<도구>)는 이름 끝이 허용 이름과 같아도 진단 단계에 들어가지 않는다.
	for _, name := range []string{"Bash", "WebFetch", "Skill", "ExecuteExtraTool", "record_fact", "add_intent", "list_facts", "node_detail", "report_finding", "insert_assets", "add_task_scope", "TodoWrite", "Monitor",
		"mcp__browser__browser_navigate", "mcp__browser__node_detail", "mcp__browser__list_facts"} {
		base = append(base, e2WorkerFixtureTool(name, nil))
	}
	planner := e2HTTPPlannerTools(base)
	var names []string
	for _, tool := range planner {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "add_intent,list_facts,node_detail" {
		t.Fatalf("planner tools=%v", names)
	}
	worker := e2HTTPWorkerGraphTools(base, execution, 22)
	names = nil
	for _, tool := range worker {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "record_fact,list_facts,node_detail" {
		t.Fatalf("worker graph tools=%v", names)
	}
}

func TestE2HTTPFactToolEnforcesIntentAndRedactsPayload(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	calls := 0
	base := e2WorkerFixtureTool("record_fact", func(_ context.Context, input json.RawMessage) (actool.Result, error) {
		calls++
		if strings.Contains(string(input), "keep-secret") {
			t.Fatal("credential entered graph writer")
		}
		out := actool.Text("recorded keep-secret")
		out.Extra = []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("keep-secret")}}}
		return out, nil
	})
	tool := e2HTTPWorkerGraphTools([]actool.CoreTool{base}, execution, 22)[0]
	for _, input := range []string{`{"intent_id":23}`, `{"intent_id":22,"facts":[{"intent_id":23}]}`} {
		result, err := tool.Call(context.Background(), json.RawMessage(input), nil)
		if err != nil || !result.IsError || calls != 0 {
			t.Fatalf("out-of-scope write: calls=%d error=%v", calls, err)
		}
	}
	result, err := tool.Call(context.Background(), json.RawMessage(`{"intent_id":22,"summary":"keep-secret"}`), nil)
	if err != nil || calls != 1 || strings.Contains(result.Flatten(), "keep-secret") || len(result.Extra) != 0 {
		t.Fatalf("redaction failed calls=%d error=%v", calls, err)
	}
}

func e2WorkerFixtureTool(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{Name: name, Description: "synthetic fixture", Schema: map[string]any{"type": "object"},
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			if run != nil {
				return run(ctx, input)
			}
			return actool.Text("fixture"), nil
		},
	})
}

type e2WorkerProvider struct {
	calls     int
	tools     []string
	responses []string
	forever   bool
	rawError  bool
}

func (p *e2WorkerProvider) Complete(_ context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	for _, tool := range request.Tools {
		p.tools = append(p.tools, tool.Name)
	}
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolResult {
				for _, content := range block.Content {
					if content.Type == llm.BlockText {
						p.responses = append(p.responses, content.Text)
					}
				}
			}
		}
	}
	if p.rawError {
		return llm.Message{}, "", llm.Usage{}, errors.New("unlogged-provider-credential")
	}
	if p.calls == 1 || p.forever {
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "bash-" + strconv.Itoa(p.calls), Name: "Bash", Input: json.RawMessage(`{"command":"echo fixture"}`)}}}, "tool_use", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("model says completed")}}, "end_turn", llm.Usage{}, nil
}
func (p *e2WorkerProvider) Stream(ctx context.Context, request llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		message, stop, _, err := p.Complete(ctx, request)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		for _, block := range message.Content {
			if block.Type == llm.BlockToolUse {
				if !yield(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: block.ID, ToolName: block.Name}, nil) || !yield(llm.StreamEvent{Type: llm.SEToolInputJSON, Text: string(block.Input)}, nil) {
					return
				}
			} else if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: block.Text}, nil) {
				return
			}
		}
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: stop}, nil)
	}
}

func TestE2HTTPOriginalSDKSessionReceivesSeparateTrustedResult(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	provider := &e2WorkerProvider{}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 3)
	state := &e2HTTPToolState{}
	calls := 0
	tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) { calls++; return completedE2WorkerResult(), nil }, time.Second, state)
	opts := agentcore.Options{Provider: provider, Tools: []actool.CoreTool{tool}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 3, MaxDuration: time.Second, NonStreaming: true}
	reason, writes, err := worker.runE2HTTPSession(context.Background(), "worker", &db.Node{ID: 22, Kind: db.KindIntent}, nil, opts, "assigned intent", execution, state, nil)
	if err != nil || reason != harness.ReasonCompleted || writes != (WriteCounts{}) || calls != 1 || provider.calls != 2 {
		t.Fatalf("reason=%s calls=%d model=%d writes=%+v error=%v", reason, calls, provider.calls, writes, err)
	}
	if strings.Join(provider.tools, ",") != "Bash,Bash" || len(provider.responses) == 0 {
		t.Fatalf("tools=%v responses=%d", provider.tools, len(provider.responses))
	}
	var decoded e2runner.Result
	if json.Unmarshal([]byte(provider.responses[0]), &decoded) != nil || decoded.ToolOutput != "target output" || !decoded.Trusted.Final {
		t.Fatal("trusted result was not delivered separately to SDK model")
	}
}

func TestE2HTTPProgramBoundedReplanningIgnoresRepeatedModelCalls(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	provider := &e2WorkerProvider{forever: true}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 2)
	state := &e2HTTPToolState{}
	calls := 0
	tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) { calls++; return completedE2WorkerResult(), nil }, time.Second, state)
	opts := agentcore.Options{Provider: provider, Tools: []actool.CoreTool{tool}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 2, MaxDuration: time.Second, NonStreaming: true}
	reason, _, err := worker.runE2HTTPSession(context.Background(), "worker", &db.Node{ID: 22, Kind: db.KindIntent}, nil, opts, "assigned intent", execution, state, nil)
	if err != nil || reason != harness.ReasonMaxTurns || provider.calls > 2 || calls > 2 {
		t.Fatalf("reason=%s model=%d calls=%d error=%v", reason, provider.calls, calls, err)
	}
}

func TestE2HTTPUnknownResultOutranksModelCompletion(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	provider := &e2WorkerProvider{}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 3)
	state := &e2HTTPToolState{}
	tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) {
		result := completedE2WorkerResult()
		result.Trusted.Classification = "result_unknown"
		return result, nil
	}, time.Second, state)
	opts := agentcore.Options{Provider: provider, Tools: []actool.CoreTool{tool}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 3, MaxDuration: time.Second, NonStreaming: true}
	_, _, err := worker.runE2HTTPSession(context.Background(), "worker", &db.Node{ID: 22, Kind: db.KindIntent}, nil, opts, "assigned intent", execution, state, nil)
	if !errors.Is(err, ErrE2HTTPUnknown) || provider.calls != 2 {
		t.Fatalf("model=%d error=%v", provider.calls, err)
	}
}

func TestE2HTTPStopOutranksStaleRunnerAndModelCompletion(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	provider := &e2WorkerProvider{}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 3)
	state := &e2HTTPToolState{}
	tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) {
		if err := execution.Revoke(context.Background()); err != nil {
			t.Fatal(err)
		}
		return completedE2WorkerResult(), nil
	}, time.Second, state)
	opts := agentcore.Options{Provider: provider, Tools: []actool.CoreTool{tool}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 3, MaxDuration: time.Second, NonStreaming: true}
	var activities []db.Activity
	_, _, err := worker.runE2HTTPSession(context.Background(), "worker", &db.Node{ID: 22, Kind: db.KindIntent}, func(a db.Activity) { activities = append(activities, a) }, opts, "assigned intent", execution, state, nil)
	if !errors.Is(err, ErrE2HTTPBlocked) {
		t.Fatalf("error=%v", err)
	}
	if len(activities) == 0 || activities[len(activities)-1].Kind != "e2_execution" || !activities[len(activities)-1].IsError {
		t.Fatal("actual stopped execution was not emitted")
	}
}

func TestE2HTTPProviderFailureIsSanitizedInActivityAndReturn(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	provider := &e2WorkerProvider{rawError: true}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 1)
	opts := agentcore.Options{Provider: provider, Tools: []actool.CoreTool{}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 1, MaxDuration: time.Second, NonStreaming: true}
	var activities []db.Activity
	_, _, err := worker.runE2HTTPSession(context.Background(), "worker", &db.Node{ID: 22, Kind: db.KindIntent}, func(a db.Activity) { activities = append(activities, a) }, opts, "assigned intent", execution, &e2HTTPToolState{}, nil)
	if !errors.Is(err, ErrE2HTTPWorker) || strings.Contains(err.Error(), "unlogged-provider-credential") {
		t.Fatalf("error=%v", err)
	}
	for _, a := range activities {
		if strings.Contains(a.Summary+a.Detail, "unlogged-provider-credential") {
			t.Fatal("raw provider error entered activity")
		}
	}
}

func e2WorkerBashTool(t *testing.T, execution *e2proxy.Execution, run e2HTTPRunFunc, timeout time.Duration, state *e2HTTPToolState) actool.CoreTool {
	t.Helper()
	control, err := NewE2HTTPControl(execution.Snapshot().Scope.TaskRef, 10)
	if err != nil {
		t.Fatal(err)
	}
	return newE2HTTPBashTool(execution, run, timeout, state, control, json.RawMessage(`{"summary":"synthetic intent"}`), execution.Snapshot().IntentID)
}

func TestE2HTTPSharedControlRefusesDeniedCommandAcrossNewIntentIDs(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	control, err := NewE2HTTPControl("7", 10)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := execution.Snapshot()
	first, _, err := control.reserve(snapshot, json.RawMessage(`{"summary":"same intent","priority":1}`), "curl denied")
	if err != nil {
		t.Fatal(err)
	}
	result := completedE2WorkerResult()
	result.Trusted.Execution = snapshot
	result.Trusted.Classification = "policy_not_executed"
	control.finish(first, result, false)
	for _, payload := range []json.RawMessage{
		json.RawMessage(`{"priority":1,"summary":"same intent"}`),
		json.RawMessage(`{"summary":"different intent"}`),
	} {
		_, view, err := control.reserve(snapshot, payload, "  curl denied ")
		if !errors.Is(err, ErrE2HTTPBlocked) || view.Decision != "same_action_not_retried" || view.Calls != 1 {
			t.Fatalf("view=%+v error=%v", view, err)
		}
	}
}
func TestE2HTTPSharedControlAllowsOnlyFirstAlternativeAndOtherIntent(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	control, err := NewE2HTTPControl("7", 4)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := execution.Snapshot()
	intent := json.RawMessage(`{"summary":"original intent"}`)
	first, _, err := control.reserve(snapshot, intent, "curl denied")
	if err != nil {
		t.Fatal(err)
	}
	result := completedE2WorkerResult()
	result.Trusted.Execution = snapshot
	result.Trusted.Classification = "policy_not_executed"
	control.finish(first, result, false)
	alternative, view, err := control.reserve(snapshot, intent, "curl different-approved-path")
	if err != nil || !view.AlternativeUsed {
		t.Fatalf("alternative=%+v error=%v", view, err)
	}
	result.Trusted.Classification = "completed"
	control.finish(alternative, result, false)
	third, _, err := control.reserve(snapshot, intent, "curl third-action")
	if err != nil {
		t.Fatalf("successful alternative closed intent: %v", err)
	}
	result.Trusted.Classification = "policy_not_executed"
	control.finish(third, result, false)
	if _, view, err := control.reserve(snapshot, intent, "curl fourth-action"); !errors.Is(err, ErrE2HTTPBlocked) || view.Decision != "intent_replan_exhausted" {
		t.Fatalf("second policy alternative=%+v error=%v", view, err)
	}
	other, _, err := control.reserve(snapshot, json.RawMessage(`{"summary":"another allowed intent"}`), "curl other-allowed")
	if err != nil {
		t.Fatalf("other intent blocked: %v", err)
	}
	control.finish(other, result, false)
}
func TestE2HTTPSharedControlToolFailuresDoNotSpendPolicyReplan(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	control, err := NewE2HTTPControl("7", 5)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := execution.Snapshot()
	intent := json.RawMessage(`{"summary":"ordinary command failure"}`)
	result := completedE2WorkerResult()
	result.Trusted.Execution = snapshot
	for _, classification := range []string{"tool_failure", "transient_failure", "forward_not_attempted"} {
		ticket, view, err := control.reserve(snapshot, intent, "curl -f check")
		if err != nil || view.AlternativeUsed {
			t.Fatalf("classification=%s reserve=%+v error=%v", classification, view, err)
		}
		result.Trusted.Classification = classification
		control.finish(ticket, result, false)
	}
	ticket, view, err := control.reserve(snapshot, intent, "curl -f check")
	if err != nil || view.AlternativeUsed || view.Calls != 4 {
		t.Fatalf("ordinary tool failure consumed replan quota: view=%+v error=%v", view, err)
	}
	result.Trusted.Classification = "completed"
	control.finish(ticket, result, false)
	if _, view, err := control.reserve(snapshot, intent, "curl next permitted action"); err != nil || view.AlternativeUsed || view.Calls != 5 {
		t.Fatalf("successful work closed intent: view=%+v error=%v", view, err)
	}
}

func TestE2HTTPSharedControlRunnerErrorKeepsReplayBlocked(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	control, err := NewE2HTTPControl("7", 3)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := execution.Snapshot()
	intent := json.RawMessage(`{"summary":"runner error"}`)
	ticket, _, err := control.reserve(snapshot, intent, "curl uncertain")
	if err != nil {
		t.Fatal(err)
	}
	result := completedE2WorkerResult()
	result.Trusted.Execution = snapshot
	result.Trusted.Classification = "tool_failure"
	control.finish(ticket, result, true)
	otherIntent := json.RawMessage(`{"summary":"different intent"}`)
	if _, view, err := control.reserve(snapshot, otherIntent, "curl uncertain"); !errors.Is(err, ErrE2HTTPBlocked) || view.Decision != "same_action_not_retried" {
		t.Fatalf("runner error replay=%+v error=%v", view, err)
	}
}

func TestE2HTTPSharedControlTotalBudgetDoesNotRenewForNewCallsOrIntents(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	control, err := NewE2HTTPControl("7", 2)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := execution.Snapshot()
	result := completedE2WorkerResult()
	result.Trusted.Execution = snapshot
	for _, summary := range []string{"one", "two"} {
		intent, _ := json.Marshal(map[string]string{"summary": summary})
		ticket, _, err := control.reserve(snapshot, intent, "echo "+summary)
		if err != nil {
			t.Fatal(err)
		}
		control.finish(ticket, result, false)
	}
	snapshot.Scope.RunRef = "new-run-same-task"
	_, view, err := control.reserve(snapshot, json.RawMessage(`{"summary":"new id new intent"}`), "echo third")
	if !errors.Is(err, ErrE2HTTPBlocked) || view.Decision != "task_call_limit" || view.Calls != 2 {
		t.Fatalf("view=%+v error=%v", view, err)
	}
}
func TestE2HTTPSharedControlUnknownClosesIntentAndPreservesOtherIntents(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	control, err := NewE2HTTPControl("7", 3)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := execution.Snapshot()
	intent := json.RawMessage(`{"summary":"unknown effect"}`)
	ticket, _, err := control.reserve(snapshot, intent, "curl state-effect")
	if err != nil {
		t.Fatal(err)
	}
	result := completedE2WorkerResult()
	result.Trusted.Execution = snapshot
	result.Trusted.Classification = "result_unknown"
	control.finish(ticket, result, false)
	if _, view, err := control.reserve(snapshot, intent, "curl maybe-cleanup"); !errors.Is(err, ErrE2HTTPBlocked) || view.Decision != "intent_replan_exhausted" {
		t.Fatalf("unknown alternative=%+v error=%v", view, err)
	}
	if _, _, err := control.reserve(snapshot, json.RawMessage(`{"summary":"different permitted intent"}`), "curl permitted-read"); err != nil {
		t.Fatal(err)
	}
}
func TestE2HTTPSharedControlRejectsConcurrentSameIntentAndLateResult(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	control, err := NewE2HTTPControl("7", 3)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := execution.Snapshot()
	intent := json.RawMessage(`{"summary":"same concurrent intent"}`)
	first, _, err := control.reserve(snapshot, intent, "echo one")
	if err != nil {
		t.Fatal(err)
	}
	if _, view, err := control.reserve(snapshot, intent, "echo two"); !errors.Is(err, ErrE2HTTPBlocked) || view.Decision != "intent_call_inflight" {
		t.Fatalf("view=%+v error=%v", view, err)
	}
	result := completedE2WorkerResult()
	result.Trusted.Execution = snapshot
	control.finish(first, result, false)
	next, _, err := control.reserve(snapshot, intent, "echo one")
	if err != nil {
		t.Fatal(err)
	}
	if view := control.finish(first, result, false); view.Decision != "control_incomplete" {
		t.Fatalf("late result=%+v", view)
	}
	if _, view, err := control.reserve(snapshot, intent, "echo third"); !errors.Is(err, ErrE2HTTPBlocked) || view.Decision != "intent_call_inflight" {
		t.Fatalf("late result reopened admission=%+v error=%v", view, err)
	}
	control.finish(next, result, false)
}
func TestE2HTTPPlannerOptionsRemoveSDKBypassToolsAndBoundTaskTime(t *testing.T) {
	provider := &e2WorkerProvider{rawError: true}
	opts := agentcore.Options{
		Provider: provider, Tools: []actool.CoreTool{e2WorkerFixtureTool("node_detail", nil), e2WorkerFixtureTool("Bash", nil)},
		EnableWebFetch: true, EnableWebSearch: true, MaxTurns: 0, MaxDuration: 0,
		BashEnv: []string{"HTTP_PROXY=should-not-be-used"}, Todos: actool.NewTodoStore(),
	}
	if err := constrainE2HTTPPlannerOptions(context.Background(), &opts); err != nil {
		t.Fatal(err)
	}
	if opts.EnableWebFetch || opts.EnableWebSearch || !opts.DisableBackgroundTasks || opts.Todos != nil || len(opts.BashEnv) > 0 || opts.MaxTurns != e2HTTPPlannerMaxTurns || opts.MaxDuration != e2HTTPPlannerMaxDuration {
		t.Fatalf("unsafe planner options: maxTurns=%d duration=%v background=%v", opts.MaxTurns, opts.MaxDuration, opts.DisableBackgroundTasks)
	}
	session := agentcore.NewSession(opts)
	defer session.Close()
	for range session.Prompt(context.Background(), "synthetic planning") {
	}
	if strings.Join(provider.tools, ",") != "node_detail" {
		t.Fatalf("planner SDK injected bypass tools: %v", provider.tools)
	}
	expired := WithTaskClock(context.Background(), TaskClock{DeadlineUnix: time.Now().Add(-time.Second).Unix()})
	if err := constrainE2HTTPPlannerOptions(expired, &opts); !errors.Is(err, ErrE2HTTPBlocked) {
		t.Fatalf("expired task: %v", err)
	}
}

func TestE2HTTPLastProgramClassificationOutranksModelDone(t *testing.T) {
	cases := map[string]error{
		"policy_not_executed":   ErrE2HTTPPolicyLimited,
		"partial_policy_denial": ErrE2HTTPPolicyLimited,
		"unsupported":           ErrE2HTTPUnsupported,
		"partial_unsupported":   ErrE2HTTPUnsupported,
		"transient_failure":     ErrE2HTTPTransient,
		"forward_not_attempted": ErrE2HTTPTransient,
		"isolation_unavailable": ErrE2HTTPUnavailable,
		"tool_failure":          nil,
		"terminated":            ErrE2HTTPBlocked,
	}
	for classification, want := range cases {
		t.Run(classification, func(t *testing.T) {
			execution, _, _ := e2WorkerFixture(t)
			provider := &e2WorkerProvider{}
			worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 3)
			state := &e2HTTPToolState{}
			tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) {
				result := completedE2WorkerResult()
				result.Trusted.Classification = classification
				return result, nil
			}, time.Second, state)
			opts := agentcore.Options{Provider: provider, Tools: []actool.CoreTool{tool}, DisableBackgroundTasks: true, PermissionMode: permission.ModeBypass, MaxTurns: 3, MaxDuration: time.Second, NonStreaming: true}
			_, _, err := worker.runE2HTTPSession(context.Background(), "worker", &db.Node{ID: 22, Kind: db.KindIntent}, nil, opts, "assigned intent", execution, state, nil)
			if !errors.Is(err, want) {
				t.Fatalf("classification=%s error=%v", classification, err)
			}
		})
	}
}
func TestE2HTTPRetainsOriginalBashSafetyFloorWithoutLaunching(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	calls := 0
	tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) { calls++; return completedE2WorkerResult(), nil }, time.Second, &e2HTTPToolState{})
	result, err := tool.Call(context.Background(), json.RawMessage(`{"command":"rm -rf /"}`), nil)
	if err != nil || !result.IsError || calls != 0 {
		t.Fatalf("safety floor calls=%d error=%v", calls, err)
	}
}
func TestE2HTTPExpiredTaskDeadlineDoesNotReachProviderOrRunner(t *testing.T) {
	_, _, binding := e2WorkerFixture(t)
	provider := &e2WorkerProvider{}
	worker := NewWorker(provider, "synthetic", t.TempDir(), nil, 10000, 3)
	ctx := WithTaskClock(WithE2HTTPExecution(context.Background(), binding), TaskClock{DeadlineUnix: time.Now().Add(-time.Second).Unix()})
	_, _, err := worker.Execute(ctx, "worker", 7, nil, nil, &db.Node{ID: 22, Kind: db.KindIntent, Payload: json.RawMessage(`{"summary":"expired intent"}`)}, nil, nil, nil, nil)
	if !errors.Is(err, ErrE2HTTPBlocked) || provider.calls != 0 {
		t.Fatalf("model calls=%d error=%v", provider.calls, err)
	}
}

func TestE2HTTPRegisteredIntentBindingSharesRunnerAndControl(t *testing.T) {
	execution, runner, binding := e2WorkerFixture(t)
	if execution.HasIntent(23) {
		t.Fatal("unregistered intent was visible")
	}
	next := binding
	next.IntentID = 23
	node := &db.Node{ID: 23, Kind: db.KindIntent, Payload: json.RawMessage(`{"summary":"another approved intent"}`)}
	if err := validateE2HTTPExecution(context.Background(), next, 7, node); !errors.Is(err, ErrE2HTTPBinding) {
		t.Fatalf("unregistered binding=%v", err)
	}
	if err := execution.RegisterIntent(context.Background(), 23); err != nil {
		t.Fatal(err)
	}
	if !execution.HasIntent(23) {
		t.Fatal("trusted intent registration unavailable")
	}
	if err := validateE2HTTPExecution(context.Background(), next, 7, node); err != nil {
		t.Fatal(err)
	}
	if next.Runner != E2HTTPRunner(localRunnerForTest{runner}) || next.Control != binding.Control {
		t.Fatal("intent replaced task runner or limits")
	}
}

// 진단 단계 도구 목록에 MCP 도구가 섞이면 Worker·Planner를 시작하지 않는다(탐색 실행기 결정안 §2-2).
func TestE2HTTPDiagnosisRejectsMCPTools(t *testing.T) {
	plain := []actool.CoreTool{e2WorkerFixtureTool("Bash", nil), e2WorkerFixtureTool("node_detail", nil), nil}
	if e2HTTPHasMCPTool(plain) {
		t.Fatal("graph tools reported as MCP")
	}
	if !e2HTTPHasMCPTool(append(plain, e2WorkerFixtureTool("mcp__browser__browser_navigate", nil))) {
		t.Fatal("MCP tool not detected")
	}
	// 지금 구성으로는 MCP가 들어올 길이 없으므로, 검사가 실제로 시작 경로에 있는지를 소스로 묶는다.
	for file, after := range map[string]string{"e2_http.go": `withDiagnosticTools(ctx, tools, "worker")`, "planner.go": `withDiagnosticTools(ctx, tools, "planner")`} {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		_, rest, ok := strings.Cut(string(source), after)
		if !ok || !strings.HasPrefix(strings.TrimSpace(rest), "if e2HTTPHasMCPTool(tools) {") {
			t.Fatalf("%s: MCP check is not right after the diagnosis tool list", file)
		}
	}
}
