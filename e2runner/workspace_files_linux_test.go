package e2runner

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2sandbox"
)

func sleepBriefly() { time.Sleep(10 * time.Millisecond) }

// 파일 도구가 쓴 파일은 다음 호출이 /work에서 보고, 호출이 만든 파일은 파일 도구가 읽는다. 고치기는 정확히 한 번
// 있는 문자열만 바꾼다(결정안 §2-4).
func TestRunnerWorkspaceFilesRoundTripWithCalls(t *testing.T) {
	f, _ := persistentRunnerFixture(t)
	// 원본 Write처럼 없는 부모 디렉터리는 만든다.
	if err := f.runner.WriteWorkspaceFile(2, "notes/todo.txt", []byte("check /api")); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.WriteWorkspaceFile(2, "plan.txt", []byte("step one\nstep two\n")); err != nil {
		t.Fatal(err)
	}
	if n, err := f.runner.EditWorkspaceFile(2, "plan.txt", "step", "phase", false); !errors.Is(err, ErrWorkspaceEdit) || n != 0 {
		t.Fatal("ambiguous edit applied", n, err)
	}
	if n, err := f.runner.EditWorkspaceFile(2, "plan.txt", "two", "2", false); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	out, err := f.runner.RunForIntent(t.Context(), 2, "cat plan.txt; mkdir -p out emptydir; printf made > out/result.txt")
	if err != nil || out.ToolOutput != "step one\nstep 2\n" || out.Workspace != "saved" {
		t.Fatalf("call did not see the written file: %q %v", out.ToolOutput, err)
	}
	got, err := f.runner.WorkspaceFile(2, "out/result.txt")
	if err != nil || string(got) != "made" {
		t.Fatalf("call output not readable: %q %v", got, err)
	}
	entries, err := f.runner.WorkspaceList(2, "")
	if err != nil || len(entries) != 4 || entries[0].Name != "emptydir" || entries[1].Name != "notes" || !entries[1].Dir || entries[2].Name != "out" || entries[3].Name != "plan.txt" {
		t.Fatalf("list: %+v %v", entries, err)
	}
	for _, dir := range []string{"out", "emptydir"} {
		if err := f.runner.WriteWorkspaceFile(2, dir, []byte("x")); !errors.Is(err, ErrWorkspaceFile) {
			t.Fatal("directory overwritten by a file", dir, err)
		}
	}
	for _, bad := range []string{"../escape", "/etc/passwd", "a/../b", ""} {
		if err := f.runner.WriteWorkspaceFile(2, bad, []byte("x")); !errors.Is(err, ErrWorkspaceFile) {
			t.Fatalf("%q: %v", bad, err)
		}
		if _, err := f.runner.WorkspaceFile(2, bad); !errors.Is(err, ErrWorkspaceFile) {
			t.Fatalf("read %q: %v", bad, err)
		}
	}
	if _, err := f.runner.WorkspaceFile(9, "plan.txt"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("other intent's workspace reachable", err)
	}
	big := strings.Repeat("x", int(e2sandbox.WorkspaceLimit(e2sandbox.Config{}))+1)
	if err := f.runner.WriteWorkspaceFile(2, "big.txt", []byte(big)); !errors.Is(err, ErrWorkspaceFull) {
		t.Fatal("over-limit write accepted", err)
	}
}

// 같은 intent의 호출이 보관소를 쓰는 동안 파일 도구는 바꾸지 않는다(호출의 마지막 저장이 덮어쓰지 않게).
func TestRunnerWorkspaceFilesRefuseWhileCallRuns(t *testing.T) {
	f, _ := persistentRunnerFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := f.runner.RunForIntent(t.Context(), 2, "sleep 1; printf call > from-call.txt")
		done <- err
	}()
	for i := 0; ; i++ {
		f.runner.mu.Lock()
		busy := f.runner.workspaceBusy[2]
		f.runner.mu.Unlock()
		if busy > 0 {
			break
		}
		if i > 200 {
			t.Fatal("call never marked the workspace busy")
		}
		sleepBriefly()
	}
	if err := f.runner.WriteWorkspaceFile(2, "tool.txt", []byte("tool")); !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatal("write during a running call", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := f.runner.WriteWorkspaceFile(2, "tool.txt", []byte("tool")); err != nil {
		t.Fatal("write after the call", err)
	}
	if got, err := f.runner.WorkspaceFile(2, "from-call.txt"); err != nil || string(got) != "call" {
		t.Fatal("call output lost", string(got), err)
	}
}
