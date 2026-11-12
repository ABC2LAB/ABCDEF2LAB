package e2runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2sandbox"
)

func persistentRunnerFixture(t *testing.T) (*runnerFixture, string) {
	t.Helper()
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(target.Close)
	f := newRunnerFixtureConfigured(t, base, helper, target.URL, nil, nil, true, func(c *Config) { c.PersistWorkspace = true })
	for _, id := range []int64{2, 3} {
		if err := f.execution.RegisterIntent(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	return f, f.cfg.RootDir
}

// 같은 intent의 다음 호출이 앞 호출의 /work 파일을 본다. 다른 intent에는 보이지 않는다(C29).
func TestRunnerWorkspacePersistsPerIntent(t *testing.T) {
	f, root := persistentRunnerFixture(t)
	first, err := f.runner.RunForIntent(t.Context(), 2, "mkdir -p d && printf alpha > d/a.txt && echo stored")
	if err != nil || first.Workspace != "saved" || first.Trusted.Classification != "completed" {
		t.Fatalf("first: %+v %v", first, err)
	}
	if info, err := os.Lstat(filepath.Join(root, "workspace-2.tar")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store file: %v %v", info, err)
	}
	second, err := f.runner.RunForIntent(t.Context(), 2, "cat d/a.txt")
	if err != nil || !strings.Contains(second.ToolOutput, "alpha") {
		t.Fatalf("same intent did not see its workspace: %q %v", second.ToolOutput, err)
	}
	other, err := f.runner.RunForIntent(t.Context(), 3, "cat d/a.txt 2>/dev/null || echo missing")
	if err != nil || !strings.Contains(other.ToolOutput, "missing") {
		t.Fatalf("another intent saw the workspace: %q %v", other.ToolOutput, err)
	}
	// intent가 끝나면(DrainIntent) 보관본을 지운다.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.runner.DrainIntent(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "workspace-2.tar")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("intent workspace kept after the intent ended", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "workspace-3.tar")); err != nil {
		t.Fatal("another intent's workspace removed", err)
	}
	// Runner를 닫으면 남은 보관본도 지운다.
	if err := f.runner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, "workspace-*"))
	if len(matches) != 0 {
		t.Fatalf("workspaces kept after close: %v", matches)
	}
}

// 보관 파일이 바뀌었으면(링크·권한·형식) 그 intent의 호출은 격리를 시작하지 않는다.
func TestRunnerWorkspaceRejectsTamperedStore(t *testing.T) {
	for name, tamper := range map[string]func(string) error{
		"symlink": func(p string) error { return os.Symlink("/etc/passwd", p) },
		"mode":    func(p string) error { return os.WriteFile(p, nil, 0o644) },
		"garbage": func(p string) error { return os.WriteFile(p, []byte("not a tar archive at all"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			f, root := persistentRunnerFixture(t)
			path := filepath.Join(root, "workspace-2.tar")
			if err := tamper(path); err != nil {
				t.Fatal(err)
			}
			result, err := f.runner.RunForIntent(t.Context(), 2, "echo should-not-run")
			if err == nil && result.Trusted.Classification != "isolation_unavailable" || strings.Contains(result.ToolOutput, "should-not-run") {
				t.Fatalf("tampered store used: %+v %v", result, err)
			}
		})
	}
}

// 저장을 켜지 않은 Runner는 지금처럼 호출마다 빈 /work다.
func TestRunnerWorkspaceOffByDefault(t *testing.T) {
	base := runnerTestDirectory(t)
	helper := runnerTestHelper(t, base)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer target.Close()
	f := newRunnerFixture(t, base, helper, target.URL, nil)
	if err := f.execution.RegisterIntent(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if r, err := f.runner.RunForIntent(t.Context(), 2, "printf alpha > a.txt"); err != nil || r.Workspace != "" {
		t.Fatalf("workspace state without persistence: %+v %v", r, err)
	}
	r, err := f.runner.RunForIntent(t.Context(), 2, "cat a.txt 2>/dev/null || echo missing")
	if err != nil || !strings.Contains(r.ToolOutput, "missing") {
		t.Fatalf("workspace carried without persistence: %q", r.ToolOutput)
	}
}

// 샌드박스가 돌려준 작업 공간도 호스트가 다시 검사한다. 망가진 tar·상한 밖·저장 안 됨은 보관본을 바꾸지 않는다.
func TestRunnerStoreWorkspaceRevalidates(t *testing.T) {
	f, root := persistentRunnerFixture(t)
	path := filepath.Join(root, "workspace-2.tar")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		result e2sandbox.Result
		want   string
	}{
		{e2sandbox.Result{WorkspaceState: "saved", Workspace: []byte("not a tar archive at all")}, "invalid"},
		{e2sandbox.Result{WorkspaceState: "too_large"}, "too_large"},
		{e2sandbox.Result{WorkspaceState: "not_saved"}, "not_saved"},
		{e2sandbox.Result{}, "not_saved"},
	} {
		if got := f.runner.storeWorkspace(2, tc.result); got != tc.want {
			t.Fatalf("%+v: %s, want %s", tc.result.WorkspaceState, got, tc.want)
		}
		if info, err := os.Stat(path); err != nil || info.Size() != 0 {
			t.Fatal("previous workspace replaced by a rejected one")
		}
	}
}

// tool_output보다 긴 출력은 가린 전체를 .outputs/<call_ref>.txt로 남기고, 다음 호출에서 읽을 수 있다(결정안 §2-3).
func TestRunnerWorkspaceKeepsRedactedFullOutput(t *testing.T) {
	f, _ := persistentRunnerFixture(t)
	long, err := f.runner.RunForIntent(t.Context(), 2, "echo 'Bearer runner-session-secret'; head -c 20000 /dev/zero | tr '\\0' x; echo; echo marker-end")
	if err != nil || long.Workspace != "saved" || !strings.HasPrefix(long.OutputFile, ".outputs/") || !strings.HasSuffix(long.OutputFile, ".txt") ||
		len(long.ToolOutput) > f.cfg.OutputLimit || !long.Trusted.OutputTruncated || strings.Contains(long.ToolOutput, "runner-session-secret") {
		t.Fatalf("long output: file=%q len=%d truncated=%v err=%v", long.OutputFile, len(long.ToolOutput), long.Trusted.OutputTruncated, err)
	}
	check, err := f.runner.RunForIntent(t.Context(), 2, "wc -c < '"+long.OutputFile+"'; tail -n 1 '"+long.OutputFile+"'; grep -c runner-session-secret '"+long.OutputFile+"' || true")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(check.ToolOutput)
	if len(lines) < 3 || lines[1] != "marker-end" || lines[2] != "0" {
		t.Fatalf("saved output: %q", check.ToolOutput)
	}
	if size := lines[0]; len(size) < 5 {
		t.Fatalf("saved output is not the full output: %s bytes", size)
	}
	// 샌드박스 캡처 상한(maxOutputBytes)을 넘은 출력은 끝부분이 없으므로 전체 파일로 남기지 않는다(#446 이동찬 지적).
	over, err := f.runner.RunForIntent(t.Context(), 2, fmt.Sprintf("head -c %d /dev/zero | tr '\\0' x; echo; echo marker-end", maxOutputBytes+4096))
	if err != nil || over.OutputFile != "" || !over.Trusted.OutputTruncated || over.Workspace != "saved" || len(over.ToolOutput) > f.cfg.OutputLimit {
		t.Fatalf("capture-truncated output: file=%q truncated=%v workspace=%s err=%v", over.OutputFile, over.Trusted.OutputTruncated, over.Workspace, err)
	}
	// 캡처 상한 바로 아래는 끝 표식까지 전체가 파일로 남는다.
	under, err := f.runner.RunForIntent(t.Context(), 2, fmt.Sprintf("head -c %d /dev/zero | tr '\\0' x; echo; echo marker-end", maxOutputBytes-4096))
	if err != nil || under.OutputFile == "" {
		t.Fatalf("output under the capture limit not saved: file=%q err=%v", under.OutputFile, err)
	}
	tail, err := f.runner.RunForIntent(t.Context(), 2, "tail -n 1 '"+under.OutputFile+"'")
	if err != nil || strings.TrimSpace(tail.ToolOutput) != "marker-end" {
		t.Fatalf("saved output lost its end: %q", tail.ToolOutput)
	}
	short, err := f.runner.RunForIntent(t.Context(), 2, "echo short")
	if err != nil || short.OutputFile != "" || short.Trusted.OutputTruncated {
		t.Fatalf("short output: file=%q truncated=%v", short.OutputFile, short.Trusted.OutputTruncated)
	}
}
