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
	"time"

	"github.com/Autumn-27/artex/server"
)

// 저장 보고서에는 진단 실행권한·모델·호스트를 열지 않는다.
type savedReportService struct {
	handler http.Handler
	close   func() error
}

func openSavedReport(ctx context.Context, c launchConfig, taskID int64) (*savedReportService, error) {
	pool, err := openLaunchDatabase(ctx, c.RuntimeDSNFile)
	if err != nil {
		return nil, errPreparation
	}
	handler, err := server.NewFirstDiagnosisReportHandler(ctx, pool, c.KeyDir, c.DataDir, taskID)
	if err != nil {
		if closeErr := pool.Close(); closeErr != nil {
			return nil, errors.Join(errPreparation, closeErr)
		}
		return nil, errPreparation
	}
	return &savedReportService{handler: handler, close: pool.Close}, nil
}

func runReport(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runSavedReport(ctx, args, stdout, stderr, openSavedReport)
}

func runSavedReport(ctx context.Context, args []string, stdout, stderr io.Writer, open func(context.Context, launchConfig, int64) (*savedReportService, error)) (code int) {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "protected launch configuration")
	taskID := flags.Int64("task-id", 0, "existing task ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" || *taskID <= 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if ctx == nil || ctx.Err() != nil {
		fmt.Fprintln(stderr, "저장 보고서 기동이 취소되었습니다")
		return 1
	}
	c, err := readLaunchConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, "실행 설정을 사용할 수 없습니다")
		return 1
	}
	ctx, diagnostics, err := beginLaunchDiagnostics(ctx, c, "report", *taskID, stderr)
	if err != nil {
		return 1
	}
	stderr = diagnostics.stderr
	defer diagnostics.finish(&code)
	diagnostics.next("report_service", "report_service_failed")
	service, err := open(ctx, c, *taskID)
	if err != nil {
		fmt.Fprintln(stderr, "저장 보고서의 인증·작업·데이터베이스를 확인할 수 없습니다")
		return 1
	}
	listener, err := net.Listen("tcp", c.ListenAddr)
	if err != nil {
		if service.close() != nil {
			fmt.Fprintln(stderr, "저장 보고서 기동 실패 뒤 마감을 확인할 수 없습니다")
		} else {
			fmt.Fprintln(stderr, "수신 주소를 사용할 수 없습니다")
		}
		return 1
	}
	httpServer := &http.Server{Handler: diagnosticReportHandler(ctx, service.handler), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	serving := make(chan error, 1)
	go func() { serving <- httpServer.Serve(listener) }()
	diagnostics.next("output", "output_failed")
	outputErr := json.NewEncoder(stdout).Encode(map[string]any{"service_ready": true, "mode": "saved_report", "task_id": *taskID, "execution": "DISABLED"})
	var serveErr error
	if outputErr == nil {
		diagnostics.next("report_service", "report_service_failed")
		select {
		case <-ctx.Done():
		case serveErr = <-serving:
		}
	}
	if outputErr != nil {
		diagnostics.endError(outputErr, "output_failed")
	} else if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		diagnostics.endError(serveErr, "report_service_failed")
	}
	diagnostics.next("shutdown", "drain_failed")
	cleanup, cancel := launchCleanupContext(ctx, c)
	defer cancel()
	closeErr := closeSavedReport(cleanup, httpServer, service.close)
	if closeErr != nil {
		diagnostics.endError(closeErr, "drain_failed")
	} else {
		diagnostics.end("succeeded", "")
	}
	if outputErr != nil || serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) || closeErr != nil {
		fmt.Fprintln(stderr, "저장 보고서 서비스 또는 마감의 성공을 확인할 수 없습니다")
		return 1
	}
	return 0
}

// HTTP drain 실패 때 살아 있는 handler의 DB를 먼저 닫지 않는다.
func closeSavedReport(ctx context.Context, httpServer *http.Server, closePool func() error) error {
	if err := httpServer.Shutdown(ctx); err != nil {
		return err
	}
	return closePool()
}
