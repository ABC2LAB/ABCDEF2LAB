package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
)

// 작업대 실데이터 응답 쓰기 실패는 요청자가 먼저 끊은 경우와 그 밖의 쓰기 오류로 나눠 남긴다(2026-11-25 화면 회차 발견 6).
func TestLiveWriteErrorSeparatesRequesterClose(t *testing.T) {
	live := context.Background()
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	pipe := &net.OpError{Op: "write", Net: "unix", Err: os.NewSyscallError("write", syscall.EPIPE)}
	reset := fmt.Errorf("encode: %w", &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.ECONNRESET)})
	other := errors.New("disk full")
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		err    error
		closed bool
	}{
		{"요청 문맥 끝남", gone, other, true},
		{"끊긴 연결에 씀(EPIPE)", live, pipe, true},
		{"끊긴 연결에 씀(ECONNRESET, 감쌈)", live, reset, true},
		{"그 밖의 쓰기 오류", live, other, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := liveWriteClosedByRequester(tc.ctx, tc.err); got != tc.closed {
				t.Fatalf("요청자 끊음 판정 %v, 기대 %v", got, tc.closed)
			}
			var buf bytes.Buffer
			prev, flags := log.Writer(), log.Flags()
			log.SetOutput(&buf)
			log.SetFlags(0)
			defer func() { log.SetOutput(prev); log.SetFlags(flags) }()
			logLiveWriteError(httptest.NewRequest("GET", "/api/tasks/9/e2-flow", nil).WithContext(tc.ctx), "e2-flow", tc.err)
			closedLine := strings.Contains(buf.String(), "요청자가 연결을 끊음") && strings.Contains(buf.String(), "화면=e2-flow")
			failLine := strings.Contains(buf.String(), "응답 쓰기 실패") && strings.Contains(buf.String(), "화면=e2-flow")
			if closedLine != tc.closed || failLine == tc.closed {
				t.Fatalf("로그 %q", buf.String())
			}
		})
	}
}
