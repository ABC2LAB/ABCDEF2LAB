package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/server"
)

// inheritedGateway는 운영자가 시작한 형제 실행 서비스의 private pipe만 사용한다.
// 파일 경로·shell·exec·브라우저 제공 descriptor로 프로세스를 만들지 않는다.
func inheritedGateway(readFD, writeFD int) (diagnosis.Gateway, func() error, error) {
	if readFD < 3 || writeFD < 3 || readFD == writeFD {
		return nil, nil, errPreparation
	}
	reader, writer := os.NewFile(uintptr(readFD), "diagnosis-gateway-read"), os.NewFile(uintptr(writeFD), "diagnosis-gateway-write")
	if reader == nil || writer == nil {
		return nil, nil, errPreparation
	}
	var once sync.Once
	var closeErr error
	closeTransport := func() error {
		once.Do(func() { closeErr = errors.Join(reader.Close(), writer.Close()) })
		return closeErr
	}
	for _, f := range []*os.File{reader, writer} {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			_ = closeTransport()
			return nil, nil, errPreparation
		}
	}
	client, err := diagnosis.NewClient(reader, writer, closeTransport)
	if err != nil {
		_ = closeTransport()
		return nil, nil, errPreparation
	}
	return client, closeTransport, nil
}

func runManagement(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("manage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "protected launch configuration")
	registryPath := flags.String("registry", "", "protected operator approval registry")
	runtimeOrigin := flags.String("runtime-api-origin", "", "fixed operator loopback public API origin")
	readFD := flags.Int("gateway-read-fd", -1, "inherited response pipe")
	writeFD := flags.Int("gateway-write-fd", -1, "inherited command pipe")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" || *registryPath == "" || *readFD < 3 || *writeFD < 3 || *readFD == *writeFD {
		fmt.Fprintln(stderr, "manage 설정·registry·상속 pipe descriptor가 필요합니다")
		return 2
	}
	if ctx == nil || ctx.Err() != nil {
		fmt.Fprintln(stderr, "진단 관리 기동이 취소되었습니다")
		return 1
	}
	c, err := readLaunchConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, "실행 설정을 사용할 수 없습니다")
		return 1
	}
	registry := managedRegistry{path: *registryPath}
	if _, err = registry.read(); err != nil {
		fmt.Fprintln(stderr, "보호된 운영자 registry를 확인할 수 없습니다")
		return 1
	}
	gateway, closeGateway, err := inheritedGateway(*readFD, *writeFD)
	if err != nil {
		fmt.Fprintln(stderr, "상속된 private 실행 pipe를 확인할 수 없습니다")
		return 1
	}
	pool, err := openLaunchDatabase(ctx, c.RuntimeDSNFile)
	if err != nil {
		_ = closeGateway()
		fmt.Fprintln(stderr, "기존 인증 데이터베이스를 확인할 수 없습니다")
		return 1
	}
	handler, err := server.NewDiagnosisManagementHandler(ctx, pool, c.KeyDir, c.DataDir, registry, gateway, *runtimeOrigin)
	if err != nil {
		_ = closeGateway()
		_ = pool.Close()
		fmt.Fprintln(stderr, "기존 인증·진단 관리 경계를 확인할 수 없습니다")
		return 1
	}
	listener, err := net.Listen("tcp", c.ListenAddr)
	if err != nil {
		_ = closeGateway()
		_ = pool.Close()
		fmt.Fprintln(stderr, "관리 서비스 수신 주소를 사용할 수 없습니다")
		return 1
	}
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	serving := make(chan error, 1)
	go func() { serving <- httpServer.Serve(listener) }()
	outputErr := json.NewEncoder(stdout).Encode(map[string]any{"service_ready": true, "mode": "diagnosis_management", "draft_storage": "volatile", "execution": "OPERATOR_APPROVAL_REQUIRED"})
	var serveErr error
	if outputErr == nil {
		select {
		case <-ctx.Done():
		case serveErr = <-serving:
		}
	}
	cleanup, cancel := launchCleanupContext(ctx, c)
	defer cancel()
	if httpServer.Shutdown(cleanup) != nil {
		fmt.Fprintln(stderr, "진단 관리 HTTP 마감을 확인할 수 없습니다")
		return 1
	}
	closeErr := errors.Join(closeGateway(), pool.Close())
	if outputErr != nil || serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) || closeErr != nil {
		fmt.Fprintln(stderr, "진단 관리 서비스 또는 pipe 마감을 확인할 수 없습니다")
		return 1
	}
	return 0
}
