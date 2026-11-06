//go:build linux

package agent

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
	"github.com/Autumn-27/norma/llm"
)

// This test uses the real Worker SDK, runner, kernel boundary, proxy, target
// sockets, and durable journal. Its deterministic Provider is a fixture, not a
// local model, an external API, a Juice Shop run, or a Docker deployment.
func TestE2HTTPOriginalWorkerActualRunnerIntegration(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("SKIP: curl fixture executable unavailable")
	}
	helperRoot := e2ActualWorkerTemp(t, "e2wh-")
	helper := filepath.Join(helperRoot, "e2-bash-sandbox")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-o", helper, "./cmd/e2-bash-sandbox")
	build.Dir = filepath.Dir(mustE2ActualWorkingDir(t))
	build.Env = append(os.Environ(), "GOMAXPROCS=2")
	var compilerOutput bytes.Buffer
	build.Stdout, build.Stderr = &compilerOutput, &compilerOutput
	if err := build.Run(); err != nil {
		t.Fatalf("actual sandbox helper build failed: %v", err)
	}
	attemptPattern := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			const credential = "fixture-worker-private-value"
			var received atomic.Int32
			var badAuthentication, badAttempt, badMethod atomic.Bool
			var idsMu sync.Mutex
			ids := map[string]bool{}
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+credential {
					badAuthentication.Store(true)
				}
				if r.Method != "GET" {
					badMethod.Store(true)
				}
				id := r.Header.Get(e2proxy.AttemptHeader)
				idsMu.Lock()
				if !attemptPattern.MatchString(id) || id == "caller-forged-id" || ids[id] {
					badAttempt.Store(true)
				}
				ids[id] = true
				idsMu.Unlock()
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("owned target response; credential=" + credential))
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
			connectIP, err := netip.ParseAddr(origin.Hostname())
			if err != nil {
				t.Fatal(err)
			}
			root := e2ActualWorkerTemp(t, "e2wr-")
			ca, certPEM := e2WorkerCA(t)
			certPath := filepath.Join(root, "ca.pem")
			if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
				t.Fatal(err)
			}
			journal, err := e2proxy.CreateJournal(root, "native-worker.audit", bytes.Repeat([]byte{8}, 32))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 4})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			key := budget.Key{TaskRef: "7", BudgetRef: "worker-budget"}
			binding := budget.Binding{PolicyRef: "worker-policy", ResetRef: "worker-reset"}
			if err := ledger.Register(budget.Grant{Key: key, Binding: binding, MaxRequests: 2, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(5 * time.Minute)}, now); err != nil {
				t.Fatal(err)
			}
			execution, err := e2proxy.NewExecution(context.Background(), e2WorkerCredentialFixture(t, e2proxy.Config{
				Scope:    transmit.Scope{TaskRef: "7", RunRef: "native-worker-" + scheme, CaseRef: "worker-case", ResetRef: "worker-reset", PolicyRef: "worker-policy"},
				IntentID: 22, OwnerRef: "ARTEX", PolicyRevision: "worker-revision", AccountRef: "fixture-account", SessionRef: "fixture-session", ExpiresAt: now.Add(5 * time.Minute),
				Rules:  []e2proxy.Rule{{Ref: "worker-rule", Origin: target.URL, ConnectIP: connectIP, PathPrefix: "/fixture", PathScopeRef: "worker-path", Methods: []string{"GET"}, Headers: http.Header{"Authorization": {"Bearer " + credential}}}},
				Budget: transmit.CumulativeBudget{Ledger: ledger, Key: key, Binding: binding}, Recorder: journal, RootCAs: roots,
				Limits: e2proxy.Limits{MaxAttempts: 8, MaxCalls: 8, MaxConcurrent: 1, MaxBodyBytes: 4096, RequestTimeout: 2 * time.Second, CallTimeout: 8 * time.Second, MaxDenials: 4, MaxTransient: 2, MaxUnknown: 2},
			}))
			if err != nil {
				t.Fatal(err)
			}
			runner, err := e2runner.New(e2runner.Config{
				Execution: execution, Helper: helper, RootDir: root, CACert: certPath,
				Proxy:          e2proxy.ProxyConfig{CA: ca, HeaderTimeout: 2 * time.Second, RequestTimeout: 3 * time.Second, MaxHeaderBytes: 8192, MaxConnections: 4},
				CommandTimeout: 5 * time.Second, DrainTimeout: 3 * time.Second, OutputLimit: 4096,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := runner.Close(closeCtx); err != nil {
					t.Errorf("actual runner drain failed: %v", err)
				}
			}()
			control, err := NewE2HTTPControl("7", 3)
			if err != nil {
				t.Fatal(err)
			}
			var providers []*e2ActualWorkerProvider
			var activities []db.Activity
			runStep := func(intentID int64, command string, expected error, modelCalls int) *e2ActualWorkerProvider {
				t.Helper()
				if intentID != 22 {
					if err := execution.RegisterIntent(context.Background(), intentID); err != nil && intentID != 26 {
						t.Fatalf("actual intent registration failed: %v", err)
					}
				}
				provider := &e2ActualWorkerProvider{command: command}
				providers = append(providers, provider)
				worker := NewWorker(provider, "deterministic-fixture", root, nil, 12000, 3)
				worker.SetNonStreaming(func() bool { return true })
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				ctx = WithE2HTTPExecution(ctx, E2HTTPExecution{Runner: runner, Control: control, TaskID: 7, IntentID: intentID, TaskRef: "7", RunRef: "native-worker-" + scheme, MaxTurns: 3, MaxDuration: 10 * time.Second})
				payload, _ := json.Marshal(map[string]string{"summary": "owned fixture intent " + strconv.FormatInt(intentID, 10)})
				intent := &db.Node{ID: intentID, Kind: db.KindIntent, Payload: payload}
				_, writes, runErr := worker.Execute(ctx, "worker", 7, nil, nil, intent, nil, func(a db.Activity) { activities = append(activities, a) }, nil, nil)
				matches := errors.Is(runErr, expected)
				if intentID == 26 {
					matches = runErr != nil
				}
				if !matches || writes != (WriteCounts{}) || provider.calls != modelCalls {
					classification := "no trusted tool result"
					if provider.result != nil {
						classification = provider.result.Trusted.Classification
					}
					t.Fatalf("actual Worker result mismatch: error=%v classification=%s model_calls=%d", runErr, classification, provider.calls)
				}
				return provider
			}
			header := " -H '" + e2proxy.AttemptHeader + ": caller-forged-id' "
			deniedCommand := "curl --silent --show-error --fail -X POST" + header + "'" + target.URL + "/fixture'"
			denied := runStep(22, deniedCommand, ErrE2HTTPPolicyLimited, 2)
			if received.Load() != 0 || denied.result == nil || denied.result.Trusted.Call.IntentID != 22 {
				t.Fatal("denied action reached target or lost actual intent binding")
			}
			first := runStep(23, "curl --silent --show-error --fail"+header+"'"+target.URL+"/fixture'", nil, 2)
			if received.Load() != 1 || first.result == nil || first.result.Trusted.Call.IntentID != 23 {
				t.Fatal("different permitted intent did not deliver its actual result")
			}
			callsBefore := execution.Snapshot().Counts.Calls
			runStep(24, deniedCommand, ErrE2HTTPPolicyLimited, 2)
			if received.Load() != 1 || execution.Snapshot().Counts.Calls != callsBefore || control.Snapshot().Calls != 2 {
				t.Fatal("same denied command relaunched after a new intent")
			}
			last := runStep(25, "curl --silent --show-error --fail"+header+"'"+target.URL+"/fixture/next'", nil, 2)
			if last.result == nil || last.result.Trusted.Call.IntentID != 25 {
				t.Fatal("new permitted intent lost program attribution")
			}
			runStep(26, "curl --silent --show-error --fail"+header+"'"+target.URL+"/fixture/extra'", ErrE2HTTPBlocked, 0)
			result := last.result
			if result.Trusted.Classification != "completed" || !result.Trusted.Final || !result.Trusted.ProcessDrained || !result.Trusted.ProxyDrained || !result.Trusted.Call.AuditComplete {
				t.Fatal("real call did not complete process/proxy/audit drain")
			}
			if received.Load() != 2 || badAuthentication.Load() || badAttempt.Load() || badMethod.Load() {
				t.Fatalf("target observations received=%d authentication_bad=%v attempt_bad=%v method_bad=%v", received.Load(), badAuthentication.Load(), badAttempt.Load(), badMethod.Load())
			}
			counts := execution.Snapshot().Counts
			if counts.Attempts != 3 || counts.Allowed != 2 || counts.Denied != 1 || counts.ForwardAttempted != 2 || counts.Responses != 2 || counts.Unknown != 0 || counts.Calls != 3 {
				t.Fatalf("actual trusted counts=%+v", counts)
			}
			if remaining, err := ledger.Remaining(key, binding, time.Now()); err != nil || remaining != 0 {
				t.Fatalf("task budget was renewed: remaining=%d error=%v", remaining, err)
			}
			if !strings.Contains(result.ToolOutput, "owned target response") {
				t.Fatal("target response was not delivered to model")
			}
			for _, provider := range providers {
				for _, request := range provider.requests {
					if bytes.Contains(request, []byte(credential)) {
						t.Fatal("host credential reached model request")
					}
				}
			}
			for _, activity := range activities {
				if strings.Contains(activity.Detail+activity.Summary, credential) {
					t.Fatal("host credential reached activity")
				}
			}
			file, err := os.Open(filepath.Join(root, "native-worker.audit"))
			if err != nil {
				t.Fatal(err)
			}
			summary, verifyErr := e2proxy.VerifyJournal(context.Background(), file, bytes.Repeat([]byte{8}, 32), journal.Checkpoint())
			closeErr := file.Close()
			if verifyErr != nil || closeErr != nil || summary.Events < 8 {
				t.Fatalf("durable audit verification events=%d error=%v", summary.Events, verifyErr)
			}
			allowed := map[int64]bool{}
			deniedBeforeSend := false
			for _, event := range execution.Events(0, 30) {
				if event.Stage == "result" && event.Decision == "allowed" && event.ForwardAttempted && event.ResponseComplete && event.AuditComplete {
					allowed[event.IntentID] = true
				}
				if event.Stage == "result" && event.IntentID == 22 && event.Decision == "denied" && event.NotSent && !event.ForwardAttempted && event.AuditComplete {
					deniedBeforeSend = true
				}
			}
			if !allowed[23] || !allowed[25] || !deniedBeforeSend {
				t.Fatal("actual intent/decision results were not bound to durable audit")
			}
		})
	}
}

func e2ActualWorkerTemp(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error("owned test directory cleanup failed")
		}
	})
	return dir
}
func mustE2ActualWorkingDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type e2ActualWorkerProvider struct {
	command  string
	calls    int
	requests [][]byte
	result   *e2runner.Result
}

func (p *e2ActualWorkerProvider) Complete(_ context.Context, request llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	encoded, marshalErr := json.Marshal(request)
	if marshalErr != nil {
		return llm.Message{}, "", llm.Usage{}, errors.New("native fixture request serialization failed")
	}
	p.requests = append(p.requests, encoded)
	for _, spec := range request.Tools {
		if spec.Name != "Bash" {
			return llm.Message{}, "", llm.Usage{}, errors.New("unexpected capability in native Worker fixture")
		}
	}
	if p.calls == 1 {
		input, _ := json.Marshal(map[string]any{"command": p.command, "timeout_ms": 5000})
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "native-bash", Name: "Bash", Input: input}}}, "tool_use", llm.Usage{}, nil
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
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("The owned fixture response and trusted completion were received.")}}, "end_turn", llm.Usage{}, nil
}
func (p *e2ActualWorkerProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("unexpected streaming in native Worker fixture"))
	}
}
