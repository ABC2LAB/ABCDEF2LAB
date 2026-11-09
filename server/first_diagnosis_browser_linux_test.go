//go:build linux

package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// 외부 의존성을 설치하지 않는다. 운영자가 이미 설치한 브라우저와 Node 드라이버만 쓴다.
func TestFirstDiagnosisRealBrowserOwnedPostgres(t *testing.T) {
	if os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_NODE") == "" || os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_SCRIPT") == "" {
		t.Skip("NOT_RUN: 설치된 브라우저와 Node 드라이버 경로가 필요함")
	}
	runFirstDiagnosisCombinedAcceptance(t, true)
}

type firstDiagnosisBrowserFixture struct {
	t         *testing.T
	ctx       context.Context
	url, task string
}

func startFirstDiagnosisBrowserFixture(t *testing.T, ctx context.Context, backend, task string) *firstDiagnosisBrowserFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("브라우저 시험 포트 준비 실패", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	web, err := filepath.Abs("../web")
	if err != nil {
		t.Fatal(err)
	}
	next := exec.CommandContext(ctx, "node", filepath.Join(web, "node_modules/next/dist/bin/next"), "dev", "--webpack", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port))
	next.Dir = web
	next.Env = append(os.Environ(), "AUTOPENTEST_API="+backend, "NEXT_PUBLIC_MOCK=0", "NEXT_PUBLIC_ARTEX_DEV_USER=", "NEXT_PUBLIC_ARTEX_DEV_PASS=")
	next.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, err := os.Create(filepath.Join(t.TempDir(), "next-output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	next.Stdout, next.Stderr = output, output
	if err := next.Start(); err != nil {
		_ = output.Close()
		t.Fatal("실제 Next 서버 시작 실패", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-next.Process.Pid, syscall.SIGTERM); _ = next.Wait(); _ = output.Close() })
	ui := &firstDiagnosisBrowserFixture{t: t, ctx: ctx, url: fmt.Sprintf("http://127.0.0.1:%d", port), task: task}
	client := &http.Client{Timeout: 5 * time.Second}
	ready := false
	for deadline := time.Now().Add(180 * time.Second); time.Now().Before(deadline); {
		res, e := client.Get(ui.url)
		if e == nil {
			_ = res.Body.Close()
			if res.StatusCode == 200 {
				ready = true
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("Next 준비 시간 초과")
		case <-time.After(time.Second):
		}
	}
	if !ready {
		t.Fatal("NOT_RUN: 실제 Next 개발 서버가 준비되지 않음")
	}
	return ui
}

func (f *firstDiagnosisBrowserFixture) result(phase string) ([]byte, error) {
	cmd := exec.CommandContext(f.ctx, os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_NODE"), os.Getenv("ARTEX_FIRST_DIAGNOSIS_BROWSER_SCRIPT"), phase, f.url, f.task)
	// 드라이버는 원문 DOM·인증값 대신 집계와 비밀이 아닌 초안 결속만 반환한다.
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("phase=%s: %w; %s", phase, err, output)
	}
	f.t.Logf("실브라우저 검증: %s", output)
	return output, nil
}

func (f *firstDiagnosisBrowserFixture) command(phase string) error {
	_, err := f.result(phase)
	return err
}

func (f *firstDiagnosisBrowserFixture) run(phase string) {
	f.t.Helper()
	if err := f.command(phase); err != nil {
		f.t.Fatal(err)
	}
}
