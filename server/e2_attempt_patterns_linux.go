//go:build linux

package server

import (
	"log"
	"path/filepath"
	"sync"

	"github.com/Autumn-27/artex/e2proxy"
	"golang.org/x/sys/unix"
)

// productAttemptPatterns는 E2AttemptPatternsFile에 시도마다 한 줄을 더한다. 관찰 훅은 동시에 불릴 수 있어 자체 잠금을
// 쓰고, 파일은 링크를 따라가지 않고 0600으로 연다. 실패는 표시만 빠지므로 실행을 막지 않고 서버 로그에 사유만 남긴다.
func productAttemptPatterns(stateDir string) e2proxy.AttemptPathObserver {
	var mu sync.Mutex
	path := filepath.Join(stateDir, E2AttemptPatternsFile)
	return func(r e2proxy.Receipt, escaped string) {
		line, ok := e2AttemptPatternLine(r, escaped)
		if !ok {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		fd, err := unix.Open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			log.Printf("[e2] 시도 경로 패턴 기록 실패: open")
			return
		}
		defer unix.Close(fd)
		if n, err := unix.Write(fd, line); err != nil || n != len(line) {
			log.Printf("[e2] 시도 경로 패턴 기록 실패: write")
		}
	}
}
