//go:build linux

package e2runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"golang.org/x/sys/unix"
)

// 실제 helper·NPROC 상한에서 EAGAIN을 확인하고 자식을 회수한 명령만 실패시킨다.
func TestRunnerProcessExhaustionCommandFailure(t *testing.T) {
	for _, slots := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(slots), func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "owned") }))
			defer target.Close()
			f := parallelRunnerFixture(t, target.URL, slots, slots, nil, true)
			const script = `import os,sys,time,signal,socket,urllib.parse,errno
children=[]
try:
 for _ in range(256):
  try:pid=os.fork()
  except OSError as e:
   if e.errno!=errno.EAGAIN:raise
   print("command-nproc-eagain",flush=True)
   break
  if pid==0:
   time.sleep(15)
   os._exit(0)
  children.append(pid)
 else:raise RuntimeError("NPROC not reached")
 proxy=urllib.parse.urlsplit(os.environ["HTTP_PROXY"])
 target=urllib.parse.urlsplit(sys.argv[1])
 for _ in range(3):
  with socket.create_connection((proxy.hostname,proxy.port),timeout=5) as conn:
   conn.sendall(("GET "+sys.argv[1]+"/allowed HTTP/1.1\r\nHost: "+target.netloc+"\r\nAccept-Encoding: identity\r\nConnection: close\r\n\r\n").encode())
   data=b""
   while True:
    part=conn.recv(4096)
    if not part:break
    data+=part
   if b"owned" not in data:raise RuntimeError("relay failed")
finally:
 for pid in children:
  try:os.kill(pid,signal.SIGKILL)
  except ProcessLookupError:pass
 for pid in children:
  try:os.waitpid(pid,0)
  except ChildProcessError:pass
sys.exit(23)`
			out := awaitBash(t, startBash(f.runner, t.Context(), 1, "python3 -c '"+script+"' '"+target.URL+"'"))
			tr := out.result.Trusted
			if !errors.Is(out.err, ErrUnavailable) || tr.Classification != "tool_failure" || tr.ExitCode != 23 || !tr.ProcessBashExited || !tr.Final || !tr.ProcessDrained || !tr.ProxyDrained || !tr.Call.AuditComplete || !strings.Contains(out.result.ToolOutput, "command-nproc-eagain") {
				t.Errorf("resource failure leaked into runner failure: err=%v class=%s exit=%d bash=%v final=%v drained=%v/%v audit=%v helper_thread_failure=%v", out.err, tr.Classification, tr.ExitCode, tr.ProcessBashExited, tr.Final, tr.ProcessDrained, tr.ProxyDrained, tr.Call.AuditComplete, strings.Contains(out.result.ToolOutput, "pthread_create failed"))
			}
			if tr.Call.Counts.ForwardAttempted != 3 || tr.Call.Counts.Responses != 3 || tr.Call.Counts.Unknown != 0 {
				t.Error("resource failure changed request accounting")
			}
			remaining, err := f.ledger.Remaining(f.budgetKey, f.budgetBinding, time.Now())
			if err != nil || remaining != 27 {
				t.Error("resource failure changed cumulative budget", err, remaining)
			}
			if got := awaitBash(t, startBash(f.runner, t.Context(), 1, "printf next-command")); got.err != nil || got.result.ToolOutput != "next-command" {
				t.Error("same intent next command failed", got.err)
			}
		})
	}
}

// 소유 fixture가 실제 namespace의 초기화 handshake를 보류한다. 중지·시간 상한과
// 실제 helper 종료를 나누고, 모든 자원과 감사가 닫힌 뒤에만 슬롯 반환을 허용한다.
func TestRunnerInterruptionDuringInitialization(t *testing.T) {
	for _, mode := range []string{"caller-cancel", "runner-stop", "timeout", "helper-failure", "cancel-failure-race", "audit-incomplete", "cleanup-incomplete"} {
		t.Run(mode, func(t *testing.T) {
			base := runnerTestDirectory(t)
			_ = runnerTestHelper(t, base)
			marker := filepath.Join(base, "helper-entered")
			fifo := filepath.Join(base, "helper-wait")
			if err := unix.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(base, "blocked-helper")
			script := "#!/bin/sh\nprintf entered > '" + marker + "'\n"
			if mode != "helper-failure" && mode != "cancel-failure-race" {
				script += "read ignored < '" + fifo + "'\n"
			}
			script += "exit 42\n"
			if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("initialization sent a request") }))
			defer target.Close()
			var decorate func(e2proxy.Recorder) e2proxy.Recorder
			if mode == "audit-incomplete" {
				decorate = func(next e2proxy.Recorder) e2proxy.Recorder {
					return runnerStageFailureRecorder{next: next, stage: "call_closed"}
				}
			}
			f := newRunnerFixtureWithRecorder(t, base, helper, target.URL, nil, decorate, mode != "audit-incomplete")
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			done := startBash(f.runner, ctx, 1, "printf must-not-execute")
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("fixture did not enter initialization")
				}
				time.Sleep(time.Millisecond)
			}
			var stopDone chan error
			switch mode {
			case "cleanup-incomplete":
				if err := os.Chmod(f.cfg.RootDir, 0500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(f.cfg.RootDir, 0700)
				cancel()
			case "caller-cancel", "cancel-failure-race", "audit-incomplete":
				cancel()
			case "runner-stop":
				stopDone = make(chan error, 1)
				go func() {
					stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer stopCancel()
					stopDone <- f.runner.Close(stopCtx)
				}()
			}
			out := awaitBash(t, done)
			tr := out.result.Trusted
			if stopDone != nil {
				if err := awaitSignal(t, stopDone); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "audit-incomplete" || mode == "cleanup-incomplete" {
				if !errors.Is(out.err, ErrIncomplete) || tr.Final || len(f.runner.callGate) != 1 {
					t.Fatal("unfinished interruption released slot or claimed finality", out.err, tr.Final)
				}
				if mode == "cleanup-incomplete" {
					if err := os.Chmod(f.cfg.RootDir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := f.runner.Drain(t.Context()); err != nil {
						t.Fatal("cleanup retry failed", err)
					}
					if len(f.runner.callGate) != 0 {
						t.Fatal("closed resources retained slot")
					}
				} else {
					f.runner.mu.Lock()
					var state *runningCall
					for _, candidate := range f.runner.active {
						state = candidate
					}
					f.runner.mu.Unlock()
					if state != nil && state.process != nil {
						if err := state.process.Close(); err != nil {
							t.Error(err)
						}
					}
				}
				return
			}
			switch mode {
			case "helper-failure":
				if tr.Classification != "isolation_unavailable" {
					t.Error("real startup failure hidden", tr.Classification)
				}
			case "cancel-failure-race":
				if tr.Classification != "isolation_unavailable" && tr.Classification != "terminated" && tr.Classification != "result_unknown" {
					t.Error("startup race claimed an unsupported outcome", tr.Classification)
				}
			default:
				wantClass, wantTermination := "terminated", TerminationCanceled
				if mode == "timeout" {
					wantClass, wantTermination = "transient_failure", TerminationTimeout
				}
				if tr.Classification != wantClass || tr.Termination != wantTermination || !tr.Final || !tr.ProcessDrained || !tr.ProxyDrained || tr.ProcessBashExited {
					t.Errorf("initialization interruption lost trusted cause/closure: class=%s termination=%s final=%v", tr.Classification, tr.Termination, tr.Final)
				}
			}
			if out.err == nil || len(f.runner.callGate) != 0 || len(f.execution.Calls()) != 1 || !tr.Call.Final || !tr.Call.AuditComplete {
				t.Error("initialization lost failure/audit/cleanup evidence")
			}
		})
	}
}

// 명령의 NPROC 실패와 달리, 실제 helper 장애는 닫힌 Bash 결과로 위장하지 않는다.
func TestRunnerActualHelperFailureRemainsIsolationFailure(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	f := newRunnerFixtureWithRecorder(t, base, helper, target.URL, nil, nil, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := startBash(f.runner, ctx, 1, "sleep 20")
	state := waitForRunningCall(t, f.runner)
	if err := unix.Kill(state.process.PID(), unix.SIGABRT); err != nil {
		t.Fatal(err)
	}
	out := awaitBash(t, done)
	if out.err == nil || out.result.Trusted.Classification != "isolation_unavailable" {
		t.Fatal("actual helper failure hidden", out.err, out.result.Trusted.Classification)
	}
	if err := state.process.Close(); err != nil {
		t.Error(err)
	}
	if err := f.execution.Stop(context.Background()); err != nil {
		t.Error(err)
	}
}
