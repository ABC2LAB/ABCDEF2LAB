package main

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// writer가 없는 FIFO를 키 파일로 넘겨도 open에서 멈추지 않고 바로 거부해야 한다(#372 리뷰).
func TestReadPathKeyRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "path.key")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		_, code := readPathKey(path)
		done <- code
	}()
	select {
	case code := <-done:
		if code != pathKeyRequired {
			t.Fatal(code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readPathKey blocked on a FIFO")
	}
}
