//go:build linux

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// 실제 factory가 정책 워크플로 칸의 운영자 제외 경로를 E② 실행에 넘긴다(#627). 진단 흐름 컨트롤러에는 제외 경로를 주지
// 않아, 제외 경로 거부는 E²에서만 나온다. 제외 경로(인코딩 표기 포함)는 대상에 닿지 않고 제외 밖 경로는 닿는다.
func TestE2HTTPFactoryEnforcesWorkflowExcludedPaths(t *testing.T) {
	var mu sync.Mutex
	received := map[string]int{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received[r.URL.Path]++
		mu.Unlock()
		io.WriteString(w, "owned ordinary body")
	}))
	defer target.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cfg, _, policy := e2HostPolicyFixture(t)
	policy.Rules[0].Origin = target.URL
	policy.Rules[0].PathPrefix = "/read"
	policy.Rules[0].Methods = []string{"GET"}
	policy.Limits.MaxDenials = 16
	digest := strings.Repeat("b", 64)
	flow, err := diagnosisflow.New(ctx, diagnosisflow.Binding{JobID: "owned-excluded", TaskID: "17", RunRef: policy.RunRef, SourceDigest: strings.Repeat("a", 64), InputDigest: digest, Owner: "ARTEX", Mode: diagnosisflow.Auto, TargetURL: target.URL + "/read", PathPrefixes: []string{"/read"}, ReadonlyPrefixes: []string{"/read"}, Methods: []string{"GET"}, Limits: diagnosisflow.Limits{Format: diagnosisflow.Version, MaxPages: 1, MaxEndpoints: 32, MaxResponseBytes: 4096}, Current: func() error { return nil }}, &pipelineMemoryStore{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = flow.Crawl(ctx, func(context.Context, string) (e2runner.Result, error) {
		return e2runner.Result{ToolOutput: "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n<a href='/read/item'>owned</a>", Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Final: true, ProcessDrained: true, ProxyDrained: true, Call: e2proxy.CallResult{Final: true, AuditComplete: true, Counts: e2proxy.Counts{ForwardAttempted: 1}}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if s, err := flow.Snapshot(); err != nil || s.Stage != "diagnosing" {
		t.Fatalf("owned workflow did not reach diagnosis: %v %+v", err, s.Stage)
	}
	policy.Workflow = &E2HTTPWorkflowPolicy{Format: diagnosisflow.Version, Mode: diagnosisflow.Auto, InputSHA256: digest, ExcludedPaths: []string{"/read/admin"}}
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0o600) != nil {
		t.Fatal("owned policy write failed", err)
	}
	options, err := LoadE2HTTPRuntimeOptions(ctx, E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: ctx}, Supervisor: &e2HTTPSupervisorFixture{}, Workflow: flow})
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := options.Acquire(ctx, E2HTTPRequest{TaskID: "17", TaskIDNumber: 17, ExplorationID: 1, IntentID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer acquired.Release(context.Background())
	call, err := acquired.Execution.BeginCall(ctx, "command-ref")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = call.Close(context.Background()) }()
	for _, path := range []string{"/read/admin", "/read/%61dmin/7", "/read/admin;x"} {
		req, _ := http.NewRequestWithContext(ctx, "GET", target.URL+path, nil)
		if resp, r, err := call.Do(ctx, req); err == nil || resp != nil || !r.NotSent || r.Reason != e2proxy.PolicyDenied {
			t.Fatalf("%s: excluded path not denied by E2: %v %s", path, err, r.Reason)
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", target.URL+"/read/item", nil)
	resp, r, err := call.Do(ctx, req)
	if err != nil || r.Reason != e2proxy.Complete {
		t.Fatalf("ordinary path denied: %v %s", err, r.Reason)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if received["/read/item"] != 1 || len(received) != 1 {
		t.Fatalf("target received %v", received)
	}
}
