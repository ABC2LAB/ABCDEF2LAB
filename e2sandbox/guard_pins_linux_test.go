//go:build linux

package e2sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// 대조용 helper 권한이다. 소유자만 쓸 수 있고 모두 실행할 수 있다.
	pinHelperSafeMode os.FileMode = 0755
	// 대조와 쓰기 비트 하나만 다르다. 그룹 또는 다른 사용자가 helper 를 바꿔치기할 수 있는 상태다.
	pinHelperGroupWritable os.FileMode = 0775
	pinHelperOtherWritable os.FileMode = 0757
	// 거부를 기대하지만 변이 코드에서 Launch 가 성공했을 때 정리에 쓰는 상한이다.
	pinStopLimit = 3 * time.Second
	// jail 안 환경은 LC_ALL=C 이므로 읽기 전용 파일시스템 오류(EROFS)는 이 문구로 나온다.
	pinReadOnlyDiagnostic = "Read-only file system"
	pinHelperMarker       = "copied-helper-ran"
	pinScratchMarker      = "scratch-writable"
	pinRootWritable       = "root-writable"
)

// pinCopyHelper 는 실제로 실행되는 시험 바이너리를 복사한다. 아무 파일이나 쓰면 변이 코드에서도
// exec 가 실패해 권한 검사가 아닌 이유로 거부되므로, 대조 실행이 가능한 진짜 helper 여야 한다.
// 복사본은 fixture 의 0700 디렉터리(작업공간의 상위)에 두어 작업공간을 비워 둔다.
func pinCopyHelper(t *testing.T, f *sandboxFixture) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(filepath.Dir(f.cfg.Workspace), "helper-copy")
	if err := os.WriteFile(helper, b, pinHelperSafeMode); err != nil {
		t.Fatal(err)
	}
	// umask 가 권한을 바꿀 수 있으므로 명시적으로 맞춘다.
	if err := os.Chmod(helper, pinHelperSafeMode); err != nil {
		t.Fatal(err)
	}
	return helper
}

// 공백 4: 격리를 세우는 helper 를 다른 사용자가 쓸 수 있으면 바꿔치기로 격리 전체를 우회한다.
// 같은 복사본이 안전한 권한에서는 끝까지 실행되므로, 거부는 쓰기 비트 검사 때문이다.
func TestGuardPinHelperWritableByOthersIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode os.FileMode
	}{{"group_writable", pinHelperGroupWritable}, {"other_writable", pinHelperOtherWritable}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSandboxFixture(t, "printf "+pinHelperMarker)
			f.cfg.Helper = pinCopyHelper(t, f)
			// 대조: 복사한 helper 는 안전한 권한에서 실제로 격리를 세우고 명령을 실행한다.
			r := f.run(t)
			if r.ExitCode != 0 || !r.Drained || string(r.Output) != pinHelperMarker {
				t.Fatalf("copied helper positive control: exit=%d drained=%t reason=%s output=%q", r.ExitCode, r.Drained, r.Reason, r.Output)
			}
			if err := os.Chmod(f.cfg.Helper, tc.mode); err != nil {
				t.Fatal(err)
			}
			p, err := Launch(t.Context(), f.cfg)
			if p != nil {
				ctx, cancel := context.WithTimeout(context.Background(), pinStopLimit)
				defer cancel()
				_ = p.Stop(ctx)
				_ = p.Close()
			}
			if p != nil || !errors.Is(err, ErrConfig) {
				t.Fatalf("helper with mode %v was not refused as config: %v", tc.mode, err)
			}
		})
	}
}

// 공백 5: jail 루트가 쓰기 가능하면 /work·/tmp 의 noexec 와 용량 한도를 우회할 수 있다.
// /tmp 쓰기가 되는 것으로 도구가 쓰기 자체는 할 수 있음을 보이고, 루트 거부가 EROFS 인지 확인한다.
func TestGuardPinJailRootIsReadOnly(t *testing.T) {
	f := newSandboxFixture(t, "if touch /e2-root-probe; then echo "+pinRootWritable+"; exit 1; fi; touch /tmp/e2-scratch-probe && printf "+pinScratchMarker)
	r := f.run(t)
	out := string(r.Output)
	if r.ExitCode != 0 || !r.Drained || strings.Contains(out, pinRootWritable) {
		t.Fatalf("jail root accepted a write: exit=%d drained=%t output=%q", r.ExitCode, r.Drained, out)
	}
	if !strings.Contains(out, pinReadOnlyDiagnostic) || !strings.Contains(out, pinScratchMarker) {
		t.Fatalf("root write was refused for another reason: %q", out)
	}
}
