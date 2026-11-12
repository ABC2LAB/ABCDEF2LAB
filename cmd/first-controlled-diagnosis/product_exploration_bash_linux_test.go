//go:build linux

package main

import (
	"bytes"
	"context"
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

// 정책이 direct_bash를 켜지 않으면 Bash를 주지 않고(helper가 있어도), 켜면 준다. 켰어도 helper를 쓸 수 없으면 Bash 없이
// 탐색한다(host는 만든다).
func TestProductExplorationHostDirectBashIsOptInAndDegrades(t *testing.T) {
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
		h.bashHelper = helperPath
		host, err := h.factory(f.rec, f.current, f.digest, nil, 4096)(t.Context(), f.binding, &agent.Worker{})
		if err != nil || host.Session.Boundary == nil {
			t.Fatalf("host: %v", err)
		}
		return host
	}
	if host := build(f.policy, helper); host.Session.DirectBash != nil {
		t.Fatal("direct Bash without the policy flag")
	}
	on := f.policy
	on.DirectBash = true
	if host := build(on, helper); host.Session.DirectBash == nil {
		t.Fatal("direct_bash policy did not give a direct Bash")
	}
	if host := build(on, filepath.Join(t.TempDir(), "missing-helper")); host.Session.DirectBash != nil {
		t.Fatal("unusable helper still gave a direct Bash")
	}
}
