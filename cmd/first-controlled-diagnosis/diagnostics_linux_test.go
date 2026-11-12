//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
)

func diagnosticRows(t *testing.T, root string) []diagnosticlog.Event {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "diagnostic-logs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil || len(files) != 1 {
		t.Fatalf("one auto-created log expected: files=%d err=%v", len(files), err)
	}
	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []diagnosticlog.Event
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var e diagnosticlog.Event
		if json.Unmarshal(scan.Bytes(), &e) != nil {
			t.Fatal("invalid JSONL row")
		}
		rows = append(rows, e)
	}
	if scan.Err() != nil || len(rows) == 0 {
		t.Fatal("empty/invalid log")
	}
	return rows
}

func TestRunCommandAutomaticallyLogsActualStartupFailure(t *testing.T) {
	path, _ := reportConfigFixture(t, false, "http://127.0.0.1:1")
	cfg, err := readLaunchConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"run", "-config", path}, &out, &stderr, nil, nil); code != 1 || out.Len() != 0 {
		t.Fatal("incomplete startup passed")
	}
	rows := diagnosticRows(t, cfg.DataDir)
	found := false
	for _, e := range rows {
		if e.Event == "stage_finished" && e.Stage == "protected_files" && e.Status == "failed" {
			found = true
		}
		if e.RunRef != "" || e.TaskID != 0 {
			t.Fatal("unknown operational identity invented")
		}
	}
	if !found || rows[len(rows)-1].Status != "failed" {
		t.Fatal("actual startup failure missing")
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), path) || strings.Contains(string(raw), cfg.DataDir) || strings.Contains(string(raw), cfg.ModelBaseURL) {
		t.Fatal("private input leaked")
	}
}

func TestMandatoryLogFailurePreventsReportDatabaseOpen(t *testing.T) {
	path, _ := reportConfigFixture(t, false, "http://127.0.0.1:1")
	cfg, _ := readLaunchConfig(path)
	if err := os.Mkdir(filepath.Join(cfg.DataDir, "diagnostic-logs"), 0755); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var stderr bytes.Buffer
	code := runSavedReport(context.Background(), []string{"-config", path, "-task-id", "7"}, io.Discard, &stderr, func(context.Context, launchConfig, int64) (*savedReportService, error) {
		calls++
		return nil, errPreparation
	})
	if code != 1 || calls != 0 || !strings.Contains(stderr.String(), "diagnostic_logging_unavailable") {
		t.Fatal("mandatory log failure was ignored")
	}
}

func TestDiagnosticFinalizationDistinguishesCancelTimeoutAndObservability(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "timeout", "write", "limit", "observation"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "timeout" {
				var c context.CancelFunc
				ctx, c = context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
				defer c()
			}
			var stderr bytes.Buffer
			ctx, d, err := beginLaunchDiagnostics(ctx, launchConfig{DataDir: root}, "run", 7, &stderr)
			if err != nil {
				t.Fatal(err)
			}
			d.next("execution", "execution_failed")
			code := 0
			switch mode {
			case "cancel":
				cancel()
				code = 1
			case "timeout":
				expired, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
				d.ctx = expired
				code = 1
			case "write":
				if err := os.Chmod(d.logger.Path(), 0644); err != nil {
					t.Fatal(err)
				}
				_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_started", Stage: "execution", Status: "started"})
			case "limit":
				for i := 0; i <= diagnosticlog.MaxEvents; i++ {
					_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_started", Stage: "execution", Status: "started"})
				}
			case "observation":
				_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_finished", Stage: "result_storage", Status: "incomplete", ReasonCode: "result_storage_failed"})
			}
			d.finish(&code)
			if mode == "success" {
				if code != 0 || stderr.Len() != 0 {
					t.Fatal("successful logging failed")
				}
			} else if code != 1 {
				t.Fatal("failure presented as success")
			}
			if mode == "write" && (!strings.Contains(stderr.String(), "log_storage_failed") || !strings.Contains(stderr.String(), "diagnostic_logging_dropped:")) {
				t.Fatal("write failure missing")
			}
			if mode == "limit" && !strings.Contains(stderr.String(), "log_limit") {
				t.Fatal("capacity reason missing")
			}
			if mode == "observation" && !strings.Contains(stderr.String(), "diagnostic_observation_incomplete") {
				t.Fatal("incomplete observations hidden")
			}
			if mode == "cancel" || mode == "timeout" || mode == "success" {
				rows := diagnosticRows(t, root)
				want := map[string]string{"cancel": "cancelled", "timeout": "timeout", "success": "succeeded"}[mode]
				if rows[len(rows)-1].Status != want {
					t.Fatal("terminal status mismatch")
				}
			}
		})
	}
}

func TestDiagnosticPrimaryFailureSurvivesSuccessfulCleanup(t *testing.T) {
	for _, stage := range []string{"execution", "planning"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			_, d, err := beginLaunchDiagnostics(context.Background(), launchConfig{DataDir: root}, "run", 7, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			reason := "execution_failed"
			if stage == "planning" {
				reason = "planner_failed"
			}
			d.next(stage, reason)
			d.end("failed", reason)
			d.next("shutdown", "drain_failed")
			d.end("succeeded", "")
			code := 1
			d.finish(&code)
			rows := diagnosticRows(t, root)
			last := rows[len(rows)-1]
			if code != 1 || last.Event != "diagnosis_finished" || last.Stage != stage || last.ReasonCode != reason || last.Status != "failed" {
				t.Fatal("cleanup replaced primary failure")
			}
			found := false
			for _, e := range rows {
				if e.Stage == "shutdown" && e.Event == "stage_finished" {
					found = true
					if e.Status != "succeeded" || e.ReasonCode != "" {
						t.Fatal("successful cleanup mislabeled")
					}
				}
			}
			if !found {
				t.Fatal("cleanup outcome absent")
			}
		})
	}
}

func TestDiagnosticWriteFailureWarnsBeforeFinish(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	warnings := make(reportReadyWriter, 2)
	ctx, d, err := beginLaunchDiagnostics(context.Background(), launchConfig{DataDir: root}, "run", 7, warnings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d.logger.Path(), 0644); err != nil {
		t.Fatal(err)
	}
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_started", Stage: "execution", Status: "started"})
	select {
	case msg := <-warnings:
		if !strings.Contains(string(msg), "diagnostic_logging_incomplete:log_storage_failed") {
			t.Fatal("wrong warning")
		}
	case <-time.After(time.Second):
		t.Fatal("active run did not warn")
	}
	code := 0
	d.finish(&code)
	if code != 1 {
		t.Fatal("write failure became success")
	}
	// 끝에서는 같은 경고를 다시 내지 않고 누락 건수 한 줄만 남긴다(P3 결정안 C10).
	select {
	case msg := <-warnings:
		var dropped int
		if _, err := fmt.Sscanf(string(msg), "diagnostic_logging_dropped:%d\n", &dropped); err != nil || dropped < 2 {
			t.Fatalf("unexpected finish warning %q", msg)
		}
	default:
		t.Fatal("dropped count missing")
	}
	select {
	case <-warnings:
		t.Fatal("duplicate log failure warning")
	default:
	}
}

// 진단 로그 쓰기가 실패한 뒤에도 모델 예약 기록 콜백과 실행 공통 검사는 계속 통과한다(P3 결정안 C10).
// 콜백이 오류를 내면 모델 예산이 영구 차단되므로, 실제 로거 실패를 주입한 뒤 두 번째 예약을 본다.
func TestModelObserverContinuesAfterDiagnosticLogFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, d, err := beginLaunchDiagnostics(context.Background(), launchConfig{DataDir: root}, "preparation", 7, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { code := 0; d.finish(&code) }()
	if err := os.Chmod(d.logger.Path(), 0644); err != nil {
		t.Fatal(err)
	}
	_ = diagnosticlog.Emit(ctx, diagnosticlog.Event{Component: "cli", Event: "stage_started", Stage: "execution", Status: "started"})
	if d.logger.Err() == nil {
		t.Fatal("log write failure not injected")
	}
	installation := error(nil)
	check := urlRuntimeCheck(ctx, func() error { return installation })
	saved := 0
	observe := urlModelObserver(ctx, time.Now(), true, check, func(diagnosisprep.ModelUsage) error { saved++; return nil })
	for requests := int64(1); requests <= 2; requests++ {
		if err := observe(diagnosisprep.ModelUsage{Requests: requests}); err != nil {
			t.Fatalf("reservation %d blocked by diagnostic log failure: %v", requests, err)
		}
	}
	if saved != 2 || check() != nil {
		t.Fatalf("saved=%d check=%v", saved, check())
	}
	// 설치·대상 검사와 문맥 취소는 그대로 새 예약을 막는다.
	installation = errPreparation
	if observe(diagnosisprep.ModelUsage{Requests: 3}) == nil || saved != 2 {
		t.Fatal("installation failure did not block reservation")
	}
	// 새 예약이 아닌 정산 기록(요청 수 그대로)은 검사 없이 저장한다.
	if observe(diagnosisprep.ModelUsage{Requests: 2, UsageKnown: true}) != nil || saved != 3 {
		t.Fatal("settlement record blocked")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if urlRuntimeCheck(cancelled, func() error { return nil })() == nil {
		t.Fatal("cancelled context passed")
	}
}
