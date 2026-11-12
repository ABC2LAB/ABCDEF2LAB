package e2runner

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"

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

// 정리를 끝내지 못하고 반환한 호출도 최종 cleanup(작업 공간 저장)이 끝날 때까지 보관소 사용 표시를 유지한다. 그
// 사이 쓰기는 거부되고, 최종 정리 뒤의 쓰기는 호출이 저장한 파일과 함께 남는다(#460 이동찬 지적).
func TestRunnerWorkspaceBusyUntilFinalCleanup(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(target.Close)
	f := newRunnerFixtureConfigured(t, base, helper, target.URL, nil, nil, true, func(c *Config) {
		c.PersistWorkspace = true
		c.DrainTimeout = 300 * time.Millisecond
	})
	if err := f.execution.RegisterIntent(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	returned := make(chan error, 1)
	go func() {
		_, err := f.runner.RunForIntent(t.Context(), 2, "printf call > from-call.txt; sleep 1")
		returned <- err
	}()
	var state *runningCall
	for i := 0; state == nil; i++ {
		if i > 500 {
			t.Fatal("call never became active")
		}
		f.runner.mu.Lock()
		for _, candidate := range f.runner.active {
			state = candidate
		}
		f.runner.mu.Unlock()
		sleepBriefly()
	}
	// 실제 정리 지점을 잡아 호출의 cleanup이 끝나지 못하게 한다(성공한 감사·권한을 흉내 내지 않는다).
	state.cleanupGate <- struct{}{}
	if err := <-returned; !errors.Is(err, ErrIncomplete) {
		<-state.cleanupGate
		t.Fatal("call returned without an incomplete cleanup", err)
	}
	if err := f.runner.WriteWorkspaceFile(2, "tool.txt", []byte("tool")); !errors.Is(err, ErrWorkspaceBusy) {
		<-state.cleanupGate
		t.Fatal("write accepted while the call can still store the workspace", err)
	}
	<-state.cleanupGate
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := state.cleanup(ctx); err != nil {
		t.Fatal("final cleanup", err)
	}
	if err := f.runner.WriteWorkspaceFile(2, "tool.txt", []byte("tool")); err != nil {
		t.Fatal("write after the final cleanup", err)
	}
	for name, want := range map[string]string{"from-call.txt": "call", "tool.txt": "tool"} {
		if got, err := f.runner.WorkspaceFile(2, name); err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
}

// 고치기는 결과가 작업 공간 상한을 넘으면 결과를 만들기 전에 거부한다(큰 확장으로 호스트 메모리를 쓰지 않게).
func TestRunnerWorkspaceEditRejectsExpansionBeforeAllocating(t *testing.T) {
	f, _ := persistentRunnerFixture(t)
	if err := f.runner.WriteWorkspaceFile(2, "a.txt", bytes.Repeat([]byte("a"), 1024)); err != nil {
		t.Fatal(err)
	}
	replacement := strings.Repeat("b", 64<<10)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, err := f.runner.EditWorkspaceFile(2, "a.txt", "a", replacement, true); !errors.Is(err, ErrWorkspaceFull) {
		t.Fatal("over-limit expansion accepted", err)
	}
	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown >= e2sandbox.WorkspaceLimit(e2sandbox.Config{}) {
		t.Fatalf("expanded result allocated before the limit check: %d bytes", grown)
	}
	if got, err := f.runner.WorkspaceFile(2, "a.txt"); err != nil || len(got) != 1024 {
		t.Fatal("rejected edit changed the file", len(got), err)
	}
}
