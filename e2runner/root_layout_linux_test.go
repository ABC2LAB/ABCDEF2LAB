//go:build linux

package e2runner

import (
	"strings"
	"testing"
)

func TestValidateRootDirLayoutPreservesUnixSocketByteBoundary(t *testing.T) {
	suffix := "/call-0123456789/proxy.sock"
	exact := "/" + strings.Repeat("a", 106-len(suffix)-1)
	if err := ValidateRootDirLayout(exact); err != nil {
		t.Fatal("exact existing native socket boundary was rejected")
	}
	if ValidateRootDirLayout(exact+"a") == nil {
		t.Fatal("overlong native socket path was accepted")
	}
	// Linux 경로 제한은 문자 수가 아닌 UTF-8 바이트 수다.
	if ValidateRootDirLayout(exact[:len(exact)-1]+"가") == nil {
		t.Fatal("UTF-8 byte overflow was accepted")
	}
	for _, root := range []string{"", "relative/calls", "/a/../calls", "/calls/", "/calls\x00suffix"} {
		if ValidateRootDirLayout(root) == nil {
			t.Fatal("noncanonical root path was accepted")
		}
	}
}
