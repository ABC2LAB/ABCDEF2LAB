//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/exploration"
	"github.com/Autumn-27/artex/server"
)

func buildExplorationBashHelper(t *testing.T) string {
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
	dir, err := os.MkdirTemp("", "jxh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	helper := filepath.Join(dir, "e2-bash-sandbox")
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-buildvcs=false", "-o", helper, "../e2-bash-sandbox")
	build.Env = append(os.Environ(), "GOMAXPROCS=2", "GOTOOLCHAIN=local", "GOTELEMETRY=off", "GOPROXY=off", "GOSUMDB=off")
	if out, err = build.CombinedOutput(); err != nil {
		t.Fatalf("helper source build failed: %s", string(out))
	}
	return helper
}

// 설치 정책이 direct_bash면 host가 회차마다 직접 Bash를 주고, 그 명령은 실제 탐색 경계의 직접 입구만 지난다: 승인
// 경로 GET은 대상에 닿아 관찰로 남고, 제외 경로·다른 호스트·읽기 전용 회차의 POST는 경계가 막아 대상에 닿지 않는다.
func TestProductExplorationHostDirectBashThroughRealBoundary(t *testing.T) {
	f := newHostFixture(t)
	f.host.directBash = true
	f.host.bashHelper = buildExplorationBashHelper(t)
	host, err := f.host.factory(f.rec, f.current, f.digest, []string{"/read/admin"}, 4096)(t.Context(), f.binding, &agent.Worker{})
	if err != nil {
		t.Fatal(err)
	}
	shell := host.Session.DirectBash
	if shell == nil {
		t.Fatal("direct_bash policy did not give the session a direct Bash")
	}
	lease, err := host.Session.Boundary.Open(t.Context(), f.binding)
	if err != nil {
		t.Fatal(err)
	}
	code := func(args string) string {
		return "curl -s -m 5 " + args + " -o /dev/null -w '%{http_code}\\n'"
	}
	command := strings.Join([]string{
		code(f.target + "/read"),
		code(f.target + "/read/admin/users"),
		code("https://other.test/read"),
		code("-X POST " + f.target + "/read"),
	}, "\n")
	result, err := shell.Run(t.Context(), command, 0)
	if err != nil || !result.BashExited {
		t.Fatalf("direct Bash run: %v %+v", err, result)
	}
	lines := strings.Fields(string(result.Output))
	t.Logf("codes %v", lines)
	if len(lines) != 4 || lines[0] != "200" || lines[1] == "200" || lines[2] == "200" || lines[3] == "200" {
		t.Fatalf("status codes through the boundary: %q", result.Output)
	}
	if f.hits.Load() != 1 {
		t.Fatalf("refused requests reached the target: hits=%d", f.hits.Load())
	}
	seqs, err := host.Sequences(t.Context(), f.flow)
	if err != nil || len(seqs) != 1 {
		t.Fatalf("Bash request not recorded as one observation: %v %v", seqs, err)
	}
	if o, err := host.Reader.ReadObservation(t.Context(), f.flow, seqs[0]); err != nil || o.Path != "/read" {
		t.Fatalf("observation: %+v %v", o, err)
	}
	state, err := lease.Close(t.Context(), exploration.Completed)
	if err != nil || state.TargetRequests != 1 {
		t.Fatalf("close: %+v %v", state, err)
	}
	// 마감 뒤에는 경계가 새 요청을 받지 않는다.
	after, err := shell.Run(t.Context(), code(f.target+"/read"), 0)
	if err != nil || strings.TrimSpace(string(after.Output)) == "200" || f.hits.Load() != 1 {
		t.Fatalf("closed session still reached the target: %v %q hits=%d", err, after.Output, f.hits.Load())
	}
}

// 설치 입력에서 direct_bash를 비우면 설치기가 true로 적고, false면 false로 적는다. 보호 정책에 칸이 없으면(기본 켜기 전
// #513 형식) 끈 것으로 읽어 업데이트가 이전 설치의 끄기를 켜지 않는다. helper가 없으면 Bash 없이, 루트도 안 만든다.
func TestProductExplorationHostDirectBashDefaultAndOptOut(t *testing.T) {
	f := newHostFixture(t)
	helper := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	build := func(p productExplorationPolicy, helperPath string) server.ExplorationPipelineHost {
		t.Helper()
		h, err := newProductExplorationHost(p, f.caPEM)
		if err != nil {
			t.Fatal(err)
		}
		h.bashHelper, h.bashRootParent = helperPath, t.TempDir()
		host, err := h.factory(f.rec, f.current, f.digest, nil, 4096)(t.Context(), f.binding, &agent.Worker{})
		if err != nil || host.Session.Boundary == nil {
			t.Fatalf("host: %v", err)
		}
		return host
	}
	// 설치 입력: 비우면 설치기가 true로 채움 → 켬. false·true는 그대로.
	if p := withExplorationDefaults(f.policy); p.DirectBash == nil || !*p.DirectBash || build(p, helper).Session.DirectBash == nil {
		t.Fatal("new install left direct_bash unset did not turn it on")
	}
	off, on := false, true
	disabled := f.policy
	disabled.DirectBash = &off
	if p := withExplorationDefaults(disabled); p.DirectBash == nil || *p.DirectBash || build(p, helper).Session.DirectBash != nil {
		t.Fatal("direct_bash false still gave a direct Bash")
	}
	enabled := f.policy
	enabled.DirectBash = &on
	if host := build(enabled, helper); host.Session.DirectBash == nil {
		t.Fatal("direct_bash true did not give a direct Bash")
	}
	// 보호 정책에 칸이 없으면(이전 형식) 끈다.
	if host := build(f.policy, helper); host.Session.DirectBash != nil {
		t.Fatal("protected policy without direct_bash turned it on")
	}
	if host := build(enabled, filepath.Join(t.TempDir(), "missing-helper")); host.Session.DirectBash != nil {
		t.Fatal("unusable helper still gave a direct Bash")
	}
	// 샌드박스가 없는 설치는 호출 디렉터리 루트도 만들지 않는다.
	h, err := newProductExplorationHost(enabled, f.caPEM)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	h.bashHelper, h.bashRootParent = filepath.Join(parent, "missing-helper"), parent
	if _, err := h.factory(f.rec, f.current, f.digest, nil, 4096)(t.Context(), f.binding, &agent.Worker{}); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatalf("missing helper still created %d entries", len(entries))
	}
}

// #513 형식(bool+omitempty)으로 쓴 보호 정책을 새 로더로 읽어도 끄기가 유지되고 켜기도 유지된다(업그레이드 회귀).
func TestProductExplorationPolicyUpgradeKeepsDirectBashChoice(t *testing.T) {
	type policy513 struct {
		ControlSocket       string `json:"control_socket"`
		DirectProxy         string `json:"direct_proxy"`
		BoundaryCAFile      string `json:"boundary_ca_file"`
		BoundaryCASHA256    string `json:"boundary_ca_sha256"`
		MCPURL              string `json:"mcp_url"`
		MaxExternalRequests uint64 `json:"max_external_requests,omitempty"`
		DirectBash          bool   `json:"direct_bash,omitempty"`
	}
	f := newHostFixture(t)
	for _, want := range []bool{false, true} {
		old := policy513{ControlSocket: f.policy.ControlSocket, DirectProxy: f.policy.DirectProxy, BoundaryCAFile: f.policy.BoundaryCAFile, BoundaryCASHA256: f.policy.BoundaryCASHA256, MCPURL: f.policy.MCPURL, DirectBash: want}
		raw, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		var loaded productExplorationPolicy
		if err := json.Unmarshal(raw, &loaded); err != nil {
			t.Fatal(err)
		}
		h, err := newProductExplorationHost(loaded, f.caPEM)
		if err != nil || h.directBash != want {
			t.Fatalf("#513 policy direct_bash=%v read as %v (%v)", want, h.directBash, err)
		}
	}
	// 새 설치기가 쓴 보호 정책은 늘 명시값이다.
	raw, _ := json.Marshal(withExplorationDefaults(f.policy))
	if !strings.Contains(string(raw), `"direct_bash":true`) {
		t.Fatalf("installer did not write an explicit direct_bash: %s", raw)
	}
}
