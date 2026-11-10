// Command e2-eval-service는 평가 회차 eval-e2 컨테이너의 진입점이다.
//
// 지금은 러너 제어 채널(ready·bind·head·close)만 연다. 엔진이 호출할 경로가 없고
// 실행 확인을 전부 거절하므로 이 프로세스로는 대상에 요청이 나가지 않는다.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/server"
)

const usage = "Usage: e2-eval-service -config FILE -io-timeout DURATION -close-timeout DURATION\n"

// compose down이 기다리는 시간 안에 끝나야 정리가 강제 종료로 끊기지 않는다.
const shutdownTimeout = 5 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr))
}

func run(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("e2-eval-service", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "protected runner configuration")
	ioTimeout := flags.Duration("io-timeout", 0, "control channel exchange limit")
	closeTimeout := flags.Duration("close-timeout", 0, "execution close limit")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *path == "" {
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	service, err := server.StartE2EvalService(ctx, server.E2EvalServiceOptions{ConfigPath: *path, IOTimeout: *ioTimeout, CloseTimeout: *closeTimeout})
	if err != nil {
		// 오류 문자열은 고정 단계 이름만 담는다. 경로·설정 값은 들어가지 않는다.
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	<-ctx.Done()
	closing, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := service.Close(closing); err != nil {
		_, _ = fmt.Fprintln(stderr, "e2_eval_service_close_failed")
		return 1
	}
	return 0
}
