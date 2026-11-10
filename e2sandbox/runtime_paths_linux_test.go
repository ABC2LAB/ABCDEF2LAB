//go:build linux

package e2sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fakeRuntimeGlob(failing string) func(string) ([]string, error) {
	found := map[string][]string{
		"/usr/lib/python3*":          {"/usr/lib/python3", "/usr/lib/python3.txt"},
		"/usr/local/lib/python3*":    {"/usr/local/lib/python3.12"},
		"/usr/local/lib/libpython3*": {"/usr/local/lib/libpython3.12.so.1.0"},
	}
	return func(pattern string) ([]string, error) {
		if pattern == failing {
			return nil, errors.New("glob")
		}
		return found[pattern], nil
	}
}

func TestSandboxRuntimePathsCoverEngineToolTree(t *testing.T) {
	paths, err := sandboxRuntimePaths("x86_64-linux-gnu", fakeRuntimeGlob(""), func(path string) bool { return !strings.HasSuffix(path, ".txt") })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/usr/bin", "/usr/sbin", "/usr/local/bin", "/usr/lib/x86_64-linux-gnu", "/etc/alternatives", "/etc/ld.so.cache", "/usr/lib/python3", "/usr/local/lib/python3.12", "/usr/local/lib/libpython3.12.so.1.0"} {
		if !slices.Contains(paths, want) {
			t.Error("빠진 경로", want)
		}
	}
	for _, hidden := range []string{"/usr/local/lib", "/usr/lib/python3.txt", "/etc", "/etc/passwd"} {
		if slices.Contains(paths, hidden) {
			t.Error("넣으면 안 되는 경로", hidden)
		}
	}
}

func TestSandboxRuntimePathsReportGlobFailure(t *testing.T) {
	for _, pattern := range []string{"/usr/lib/python3*", "/usr/local/lib/python3*", "/usr/local/lib/libpython3*"} {
		if _, err := sandboxRuntimePaths("x86_64-linux-gnu", fakeRuntimeGlob(pattern), func(string) bool { return true }); err == nil {
			t.Error("glob 실패를 삼킴", pattern)
		}
	}
}

func TestAllowedSystemSource(t *testing.T) {
	for path, want := range map[string]bool{
		"/usr/bin": true, "/usr/local/lib/python3.12": true, "/etc/alternatives": true, "/etc/ld.so.cache": true,
		"/etc": false, "/etc/passwd": false, "/etc/alternatives/awk": false, "/etc/ld.so.cache.bak": false, "/opt/tool": false, "/usrx/bin": false, "/usr": false, "": false,
	} {
		if allowedSystemSource(path) != want {
			t.Error(path, want)
		}
	}
}

func TestPrepareMountTarget(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a/b")
	if err := prepareMountTarget(dir, true); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal("디렉터리 대상 없음", err)
	}
	file := filepath.Join(root, "c/d")
	if err := prepareMountTarget(file, false); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() {
		t.Fatal("파일 대상 없음", err)
	}
	if prepareMountTarget(file, false) == nil {
		t.Fatal("이미 있는 대상을 다시 만듦")
	}
}

// 대체 링크(awk 등)와 ld.so 캐시는 보이고, 쓸 수 없고, 나머지 /etc는 계속 안 보여야 한다.
func TestSandboxExposesToolRuntimeButNotAccounts(t *testing.T) {
	if _, err := os.Stat("/etc/alternatives/awk"); err != nil {
		t.Skip("호스트에 awk 대체 링크가 없음")
	}
	cache, err := os.Stat("/etc/ld.so.cache")
	if err != nil {
		t.Skip("호스트에 ld.so 캐시가 없음")
	}
	f := newSandboxFixture(t, `echo "$PATH"; awk 'BEGIN{print "awk-ok"}'; wc -c < /etc/ld.so.cache
touch /etc/alternatives/probe 2>/dev/null; echo alternatives_write=$?
touch /etc/ld.so.cache 2>/dev/null; echo cache_write=$?
test -e /etc/passwd; echo passwd=$?
test -e /etc/shadow; echo shadow=$?`)
	r := f.run(t)
	if r.ExitCode != 0 || !r.Drained {
		t.Fatalf("실행 실패 %+v", r)
	}
	want := fmt.Sprintf("/usr/local/bin:/usr/sbin:/usr/bin:/bin\nawk-ok\n%d\nalternatives_write=1\ncache_write=1\npasswd=1\nshadow=1\n", cache.Size())
	if string(r.Output) != want {
		t.Fatalf("샌드박스 안 경로가 다름 got=%q want=%q", r.Output, want)
	}
}
