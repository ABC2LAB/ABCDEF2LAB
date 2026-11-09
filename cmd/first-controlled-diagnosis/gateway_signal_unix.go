//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// stdout reader가 닫혀도 SIGPIPE로 즉사하지 않고 Write 오류→감사 마감으로 돌아온다.
func handleGatewayBrokenPipe() func() {
	received := make(chan os.Signal, 1)
	signal.Notify(received, syscall.SIGPIPE)
	return func() { signal.Stop(received) }
}
