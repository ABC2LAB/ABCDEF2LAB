//go:build linux

package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"iter"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/exploresandbox"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

func exploreBashCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "exploration agent test root"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func exploreBashDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "xab-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error("fixture directory cleanup failed")
		}
	})
	return dir
}

// exploreBashShell은 helper 파일만 있는 DirectBash다(프로필 검사용, 실행하지 않음).
func exploreBashShell(t *testing.T, helper string) *exploresandbox.DirectBash {
	t.Helper()
	dir := exploreBashDir(t)
	if helper == "" {
		helper = filepath.Join(dir, "helper")
		if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(dir, "calls")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return exploreBashShellAt(t, helper, root, "127.0.0.1:9")
}

func exploreBashShellAt(t *testing.T, helper, root, entrance string) *exploresandbox.DirectBash {
	t.Helper()
	shell, err := exploresandbox.New(exploresandbox.Config{Helper: helper, RootDir: root, DirectProxy: &url.URL{Scheme: "http", Host: entrance},
		CACert: exploreBashCA(t), Timeout: 20 * time.Second, DrainTimeout: 10 * time.Second, OutputLimit: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	return shell
}

// bashScriptProvider는 첫 턴에 Bash를 한 번 부르고 둘째 턴에 끝낸다.
type bashScriptProvider struct {
	command  string
	calls    int
	requests []llm.CompletionRequest
}

func (p *bashScriptProvider) Complete(_ context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls++
	p.requests = append(p.requests, r)
	if p.calls == 1 {
		input, _ := json.Marshal(map[string]string{"command": p.command})
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "owned-bash", Name: "Bash", Input: input}}}, "tool_use", llm.Usage{}, nil
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("탐색 끝")}}, "end_turn", llm.Usage{}, nil
}
func (p *bashScriptProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { yield(llm.StreamEvent{}, errors.New("stream not used")) }
}

func exploreBashHelper(t *testing.T, dir string) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("requires an unprivileged Linux host identity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount", "--net", "--ipc", "--uts", "true").CombinedOutput()
	if err != nil {
		if bytes.Contains(out, []byte("Operation not permitted")) {
			t.Skip("OS denied required unprivileged namespaces")
		}
		t.Fatal("namespace preflight failed")
	}
	helper := filepath.Join(dir, "e2-bash-sandbox")
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-buildvcs=false", "-o", helper, "../cmd/e2-bash-sandbox")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOTOOLCHAIN=local", "GOTELEMETRY=off", "GOPROXY=off", "GOSUMDB=off")
	if out, err = build.CombinedOutput(); err != nil {
		t.Fatalf("helper source build failed: %s", string(out))
	}
	return helper
}

// 직접 Bash가 결속되면 탐색 루프가 실제 격리 실행으로 명령을 돌리고, 결과는 가린 뒤 모델에 가며, 호스트 관찰 감싸개를 지난다.
func TestExplorationRunsTypedDirectBashThroughTheBoundary(t *testing.T) {
	dir := exploreBashDir(t)
	helper := exploreBashHelper(t, dir)
	entrance, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer entrance.Close()
	var entranceRequests atomic.Int64
	seen := make(chan string, 8)
	go func() {
		for {
			c, err := entrance.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				entranceRequests.Add(1)
				seen <- req.Method + " " + req.RequestURI
				body := "entrance " + req.RequestURI
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			}(c)
		}
	}()
	root := filepath.Join(dir, "calls")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	shell := exploreBashShellAt(t, helper, root, entrance.Addr().String())
	provider := &bashScriptProvider{command: "echo seeded-private-marker; curl -s -m 5 https://owned.test/read/app.js -o /dev/null -w 'code=%{http_code}\\n'; curl -s -m 5 http://owned.test/read/api-docs"}
	w := NewWorker(provider, "owned", t.TempDir(), nil, 0, 10)
	w.SetNonStreaming(func() bool { return true })
	o := explorationOptions(&recordingDiscoveryReporter{n: 1})
	o.DirectBash = shell
	observed := 0
	o.ObserveBoundaryTool = func(tool actool.CoreTool) actool.CoreTool {
		return actool.Build(actool.Spec{Name: tool.Name(), Description: tool.Description(), Schema: map[string]any{"type": "object"},
			Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
				r, err := tool.Call(ctx, in, tc)
				observed++
				return r, err
			}})
	}
	result, err := w.ExecuteExploration(t.Context(), o, nil)
	if err != nil || provider.calls != 2 || observed != 1 {
		t.Fatalf("bash exploration: %v calls=%d observed=%d", err, provider.calls, observed)
	}
	hasBash := false
	for _, name := range result.Tools {
		hasBash = hasBash || name == "Bash"
	}
	// HTTPS는 CONNECT로, 평문은 절대 형식으로 둘 다 직접 입구에 닿는다(가짜 입구는 CONNECT에 200 본문으로 답해 터널은 안 열린다).
	if !hasBash || entranceRequests.Load() != 2 {
		t.Fatalf("bash tool or entrance request missing: tools=%v entrance=%d", result.Tools, entranceRequests.Load())
	}
	close(seen)
	lines := []string{}
	for line := range seen {
		lines = append(lines, line)
	}
	if strings.Join(lines, "|") != "CONNECT owned.test:443|GET http://owned.test/read/api-docs" {
		t.Fatalf("entrance saw %v", lines)
	}
	if !strings.Contains(strings.Join(provider.requests[0].System, "\n"), "isolated exploration sandbox") || strings.Contains(strings.Join(provider.requests[0].System, "\n"), "Direct shell commands") {
		t.Fatal("system prompt does not describe the bound shell")
	}
	toolText := ""
	for _, msg := range provider.requests[1].Messages {
		for _, block := range msg.Content {
			for _, v := range block.Content {
				toolText += v.Text
			}
		}
	}
	if !strings.Contains(toolText, "exit_code=0") || !strings.Contains(toolText, "entrance http://owned.test/read/api-docs") ||
		strings.Contains(toolText, "seeded-private-marker") || !strings.Contains(toolText, "[REDACTED]") {
		t.Fatalf("bash result not delivered redacted: %q", toolText)
	}
}

// 직접 Bash가 없는 세션은 지금처럼 셸이 없다고 안내하고, 감싸개가 Bash를 바꾸거나 없애면 프로필을 거부한다.
func TestExplorationDirectBashProfileChecks(t *testing.T) {
	if got := explorationSystemPrompt(false); !strings.Contains(got, "Direct shell commands and generic Skill execution are unavailable") || strings.Contains(got, "Bash runs") {
		t.Fatal("shell-less prompt changed")
	}
	shell := exploreBashShell(t, "")
	for name, wrap := range map[string]func(actool.CoreTool) actool.CoreTool{
		"nil":     func(actool.CoreTool) actool.CoreTool { return nil },
		"renamed": func(actool.CoreTool) actool.CoreTool { return actool.Build(actool.Spec{Name: "Shell"}) },
	} {
		o := explorationOptions(&recordingDiscoveryReporter{n: 1})
		o.DirectBash = shell
		o.ObserveBoundaryTool = wrap
		if _, err := explorationTools(o); !errors.Is(err, ErrExplorationProfile) {
			t.Errorf("%s wrapper accepted", name)
		}
	}
	o := explorationOptions(&recordingDiscoveryReporter{n: 1})
	o.DirectBash = shell
	tools, err := explorationTools(o)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	if !names["Bash"] || !names["report_discovery"] || len(tools) != len(o.BrowserTools)+2 {
		t.Fatalf("tool surface %v", names)
	}
	disabled := explorationSettlementDisabled(tools)
	found := false
	for _, n := range disabled {
		found = found || n == "Bash"
	}
	if !found {
		t.Fatal("Bash stays enabled in the settlement turn")
	}
}
