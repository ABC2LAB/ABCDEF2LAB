package e2sandbox

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 같은 intent의 다음 호출로 /work가 이어진다(C29). 첫 호출이 만든 파일을 둘째 호출이 읽는다.
func TestSandboxWorkspaceCarriesFilesToTheNextCall(t *testing.T) {
	first := newSandboxFixture(t, "mkdir -p d && printf alpha > a.txt && printf beta > d/b.txt && echo first-done")
	first.cfg.SaveWorkspace = true
	r := first.run(t)
	if r.WorkspaceState != "saved" || len(r.Workspace) == 0 || !r.Drained {
		t.Fatalf("first call workspace: state=%q len=%d result=%+v", r.WorkspaceState, len(r.Workspace), r)
	}
	second := newSandboxFixture(t, "cat a.txt; echo; cat d/b.txt")
	second.cfg.WorkspaceIn = r.Workspace
	out := second.run(t)
	if !bytes.Contains(out.Output, []byte("alpha")) || !bytes.Contains(out.Output, []byte("beta")) {
		t.Fatalf("second call did not see the first call's files: %q", out.Output)
	}
	// 저장을 요청하지 않으면 지금처럼 아무것도 돌려주지 않는다.
	if out.WorkspaceState != "" || out.Workspace != nil {
		t.Fatalf("workspace returned without a request: %q", out.WorkspaceState)
	}
}

// 명령이 남긴 분리 프로세스(setsid·이중 fork)는 빼기 전에 모두 끝난다. 늦게 쓰는 파일은 보관되지 않고, 명령은
// 빼는 통로(/proc/1/fd/9)를 열 수 없다.
func TestSandboxWorkspaceSaveKillsDetachedProcessesFirst(t *testing.T) {
	f := newSandboxFixture(t, "placeholder")
	f.cfg.Command = strings.Join([]string{
		"printf kept > kept.txt",
		"(setsid sh -c 'sleep 3; printf late > late.txt' &)",
		"(sh -c '(sleep 3; printf late2 > late2.txt) &' &)",
		"(printf junk > /proc/1/fd/9) 2>/dev/null && echo fd-written || echo fd-write-denied",
		"(printf junk >&9) 2>/dev/null && echo fd9-inherited || echo fd9-closed",
		"(cat <&8 >/dev/null) 2>/dev/null && echo fd8-inherited || echo fd8-closed",
	}, "; ")
	f.cfg.SaveWorkspace = true
	started := time.Now()
	r := f.run(t)
	if time.Since(started) >= 3*time.Second {
		t.Fatal("detached processes kept the call alive")
	}
	// helper는 dumpable이 아니라 명령은 /proc/1/fd의 번호를 볼 수 있어도 열 수는 없다.
	if r.WorkspaceState != "saved" || !bytes.Contains(r.Output, []byte("fd-write-denied")) || !bytes.Contains(r.Output, []byte("fd9-closed")) || !bytes.Contains(r.Output, []byte("fd8-closed")) {
		t.Fatalf("state=%q output=%q", r.WorkspaceState, r.Output)
	}
	dst := t.TempDir()
	if err := unpackWorkspace(r.Workspace, dst, 1<<30); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "kept.txt")); string(got) != "kept" {
		t.Fatalf("kept file: %q", got)
	}
	time.Sleep(3500 * time.Millisecond)
	for _, late := range []string{"late.txt", "late2.txt"} {
		if _, err := os.Stat(filepath.Join(dst, late)); err == nil {
			t.Fatalf("%s written after the save", late)
		}
	}
}

// 링크·FIFO·하드 링크는 보관되지 않는다.
func TestSandboxWorkspaceDropsLinksAndSpecialFiles(t *testing.T) {
	f := newSandboxFixture(t, "printf a > a.txt; ln -s /etc/passwd link; mkfifo fifo; printf s > shared; ln shared shared2; mkdir d; ln -s .. d/up")
	f.cfg.SaveWorkspace = true
	r := f.run(t)
	if r.WorkspaceState != "saved" {
		t.Fatalf("state=%q output=%q", r.WorkspaceState, r.Output)
	}
	dst := t.TempDir()
	if err := unpackWorkspace(r.Workspace, dst, 1<<30); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.txt")); err != nil {
		t.Fatal("regular file dropped")
	}
	for _, gone := range []string{"link", "fifo", "shared", "shared2", "d/up"} {
		if _, err := os.Lstat(filepath.Join(dst, gone)); err == nil {
			t.Fatalf("%s carried over", gone)
		}
	}
}

// 중지·시간 초과로 끝난 호출의 작업 공간은 보관하지 않는다(정상 마감만).
func TestSandboxWorkspaceNotSavedWhenStopped(t *testing.T) {
	f := newSandboxFixture(t, "printf a > a.txt; sleep 30")
	f.cfg.SaveWorkspace = true
	p := f.launch(t)
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := p.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, err := p.Wait(t.Context())
	if err != nil || r.WorkspaceState != "not_saved" || r.Workspace != nil {
		t.Fatalf("stopped call workspace: state=%q err=%v", r.WorkspaceState, err)
	}
}

// 상한 밖 입력은 시작하지 않고, 항목이 너무 많은 작업 공간은 보관하지 않는다(실행은 정상 마감).
func TestSandboxWorkspaceLimits(t *testing.T) {
	f := newSandboxFixture(t, "true")
	f.cfg.WorkspaceIn = bytes.Repeat([]byte{0}, int(workspaceArchiveLimit(defaultScratchBytes))+1)
	// 호스트가 helper를 띄우기 전에 설정 오류로 거부한다.
	if _, err := Launch(t.Context(), f.cfg); !errors.Is(err, ErrConfig) {
		t.Fatal("oversized workspace input not rejected before launch", err)
	}
	many := newSandboxFixture(t, "i=0; while [ $i -le 4096 ]; do : > f$i; i=$((i+1)); done; echo made")
	many.cfg.SaveWorkspace = true
	r := many.run(t)
	if r.WorkspaceState != "too_large" || r.Workspace != nil || !r.Drained || r.ExitCode != 0 {
		t.Fatalf("state=%q drained=%v exit=%d output=%q", r.WorkspaceState, r.Drained, r.ExitCode, r.Output)
	}
	// 망가진 입력은 격리 설정 실패로 시작하지 않는다.
	bad := newSandboxFixture(t, "true")
	bad.cfg.WorkspaceIn = []byte("not an archive")
	if _, err := Launch(t.Context(), bad.cfg); err == nil {
		t.Fatal("invalid workspace input launched")
	}
}
