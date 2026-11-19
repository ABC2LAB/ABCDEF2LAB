//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostSocketDirectoryRequiresHostOwnership(t *testing.T) {
	root := filepath.Join(t.TempDir(), "socket")
	if err := os.Mkdir(root, 0750); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if checkHostSocketDirectory(root, uid, gid) != nil {
		t.Fatal("Host 소유 소켓 디렉터리 거부")
	}
	if checkHostSocketDirectory(root, uid+1, gid) == nil || checkHostSocketDirectory(root, uid, gid+1) == nil {
		t.Fatal("다른 소유자 또는 그룹의 소켓 디렉터리 허용")
	}
	if err := os.Chmod(root, 0770); err != nil {
		t.Fatal(err)
	}
	if checkHostSocketDirectory(root, uid, gid) == nil {
		t.Fatal("준비 프로세스가 소켓을 교체할 수 있는 디렉터리 허용")
	}
	if err := os.Chmod(root, 0750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if checkHostSocketDirectory(link, uid, gid) == nil {
		t.Fatal("소켓 디렉터리 심볼릭 링크 허용")
	}
}
