package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
)

// CLI 단계는 원문 로그를 재해석하지 않고 실제 제어 흐름의 성공 여부만 기록한다.
type launchDiagnostics struct {
	ctx           context.Context
	logger        *diagnosticlog.Logger
	stderr        io.Writer
	stage         string
	reason        string
	started       time.Time
	closed        bool
	primaryStage  string
	primaryReason string
	primaryStatus string
	watchStop     chan struct{}
	watchDone     chan struct{}
	logReported   bool
}

func beginLaunchDiagnostics(ctx context.Context, c launchConfig, phase string, taskID int64, stderr io.Writer) (context.Context, *launchDiagnostics, error) {
	logger, err := diagnosticlog.Create(c.DataDir, phase, taskID, "")
	if err != nil {
		fmt.Fprintln(stderr, "diagnostic_logging_unavailable")
		return ctx, nil, err
	}
	ctx = diagnosticlog.With(ctx, logger)
	d := &launchDiagnostics{ctx: ctx, logger: logger, stderr: &diagnosticLockedWriter{writer: stderr}, watchStop: make(chan struct{}), watchDone: make(chan struct{})}
	if err := diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_finished", Stage: "configuration", Status: "succeeded"}); err != nil {
		_ = logger.Close()
		fmt.Fprintln(stderr, "diagnostic_logging_unavailable")
		return ctx, nil, err
	}
	go func() {
		defer close(d.watchDone)
		select {
		case <-logger.Failed():
			fmt.Fprintln(d.stderr, "diagnostic_logging_incomplete:"+diagnosticFailureCode(logger.Err()))
			d.logReported = true
		case <-d.watchStop:
		}
	}()
	return ctx, d, nil
}

func (d *launchDiagnostics) next(stage, reason string) {
	d.end("succeeded", "")
	d.stage, d.reason, d.started = stage, reason, time.Now()
	_ = diagnosticlog.Emit(d.ctx, diagnosticlog.Event{Component: "cli", Event: "stage_started", Stage: stage, Status: "started"})
}
func (d *launchDiagnostics) end(status, reason string) {
	if d.stage == "" {
		return
	}
	if status != "succeeded" && d.primaryStage == "" {
		d.primaryStage, d.primaryReason, d.primaryStatus = d.stage, reason, status
	}
	_ = diagnosticlog.Emit(d.ctx, diagnosticlog.Event{Component: "cli", Event: "stage_finished", Stage: d.stage, Status: status, ReasonCode: reason, DurationMS: time.Since(d.started).Milliseconds()})
	d.stage = ""
}
func (d *launchDiagnostics) bindTask(taskID int64) error { return d.logger.BindTask(taskID) }
func (d *launchDiagnostics) ready() error                { return d.logger.Err() }

// 성공 stdout은 마지막 행과 Sync/Close가 확인된 뒤에만 출력한다.
func (d *launchDiagnostics) finish(code *int) {
	if d == nil || d.closed {
		return
	}
	d.closed = true
	status, reason := "succeeded", ""
	if *code != 0 {
		status, reason = "failed", d.reason
	}
	if errors.Is(d.ctx.Err(), context.DeadlineExceeded) {
		status, reason = "timeout", "timeout"
	} else if errors.Is(d.ctx.Err(), context.Canceled) {
		status, reason = "cancelled", "user_cancelled"
	}
	d.end(status, reason)
	stage := "shutdown"
	if d.primaryStage != "" {
		stage, status, reason = d.primaryStage, d.primaryStatus, d.primaryReason
	}
	if d.logger.Err() != nil {
		status, reason = "incomplete", diagnosticFailureCode(d.logger.Err())
	} else if d.logger.Incomplete() {
		status, reason = "incomplete", "observation_incomplete"
		*code = 1
		fmt.Fprintln(d.stderr, "diagnostic_observation_incomplete")
	}
	_ = diagnosticlog.Emit(d.ctx, diagnosticlog.Event{Component: "cli", Event: "diagnosis_finished", Stage: stage, Status: status, ReasonCode: reason})
	logErr := errors.Join(d.logger.Err(), d.logger.Close())
	close(d.watchStop)
	<-d.watchDone
	if logErr != nil {
		*code = 1
		if !d.logReported {
			fmt.Fprintln(d.stderr, "diagnostic_logging_incomplete:"+diagnosticFailureCode(logErr))
		}
	}
}

// 조회 URL/응답 본문/인증 정보는 기록하지 않고 실제 응답 상태의 범주만 남긴다.
func diagnosticReportHandler(ctx context.Context, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		status := &diagnosticResponseWriter{ResponseWriter: w}
		next.ServeHTTP(status, r)
		result, reason := "succeeded", ""
		if status.code >= 400 {
			result, reason = "failed", "report_query_failed"
		}
		_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "service_request_finished", Stage: "report_service", Status: result, ReasonCode: reason, DurationMS: time.Since(started).Milliseconds()})
	})
}

type diagnosticResponseWriter struct {
	http.ResponseWriter
	code int
}

func (w *diagnosticResponseWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}
func (w *diagnosticResponseWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *diagnosticResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func diagnosticCheck(ctx context.Context, stage, reason string, check func() error) error {
	started := time.Now()
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_started", Stage: stage, Status: "started"})
	err := check()
	status := "succeeded"
	if err != nil {
		status = "failed"
	} else {
		reason = ""
	}
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_finished", Stage: stage, Status: status, ReasonCode: reason, DurationMS: time.Since(started).Milliseconds()})
	return err
}

func diagnosticFailureCode(err error) string {
	switch {
	case errors.Is(err, diagnosticlog.ErrLimit):
		return "log_limit"
	case errors.Is(err, diagnosticlog.ErrEvent):
		return "log_invalid_event"
	case errors.Is(err, diagnosticlog.ErrClosed):
		return "log_closed"
	default:
		return "log_storage_failed"
	}
}

func (d *launchDiagnostics) endError(err error, reason string) {
	status := "failed"
	if errors.Is(err, context.DeadlineExceeded) {
		status, reason = "timeout", "timeout"
	} else if errors.Is(err, context.Canceled) {
		status, reason = "cancelled", "user_cancelled"
	}
	d.end(status, reason)
}

// 실행 중 최초 저장 장애 경고와 CLI 자체 오류가 같은 writer를 안전하게 공유한다.
type diagnosticLockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *diagnosticLockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}
