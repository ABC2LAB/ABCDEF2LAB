//go:build linux

package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2config"
	"github.com/Autumn-27/artex/transmit"
)

type e2EvalFixture struct {
	service *E2EvalService
	run     e2ProducedRun
	config  *e2config.Config
	socket  string
}

func startE2EvalFixture(t *testing.T) e2EvalFixture {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 제어 fixture")
	}
	run := e2ProducedRunFixture(t)
	config, err := e2config.Load(run.configPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service, err := StartE2EvalService(ctx, E2EvalServiceOptions{ConfigPath: run.configPath, IOTimeout: time.Second, CloseTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal("서비스 기동 실패:", err)
	}
	return e2EvalFixture{service, run, config, filepath.Join(config.ControlDir, E2ControlSocket)}
}

func (f e2EvalFixture) close(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.service.Close(ctx); err != nil {
		t.Fatal("서비스 종료 실패:", err)
	}
}

// 러너와 똑같이 연결 하나에 요청 한 줄, 응답 한 줄로 묻는다.
func (f e2EvalFixture) ask(t *testing.T, kind string, extra map[string]any) map[string]any {
	t.Helper()
	conn, err := net.DialTimeout("unix", f.socket, time.Second)
	if err != nil {
		t.Fatal("제어 소켓 연결 실패:", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"format": E2ControlFormat, "type": kind, "eval_run_id": f.config.EvalRunID, "reservation_ref": f.config.ReservationRef}
	maps.Copy(request, extra)
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal("제어 응답 없음:", err)
	}
	var response map[string]any
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestE2EvalServiceAnswersTheRunnerControlSequence(t *testing.T) {
	f := startE2EvalFixture(t)
	defer f.close(t)
	ready := f.ask(t, "ready", nil)
	if ready["state"] != "service_ready" || ready["config_sha256"] != f.config.ConfigSHA256 || ready["policy_fingerprint_sha256"] != f.run.expectedFingerprint || ready["header_mode"] != f.config.HeaderMode {
		t.Fatalf("ready: %v", ready)
	}
	if evaluation, ok := ready["evaluation"].(map[string]any); !ok || evaluation["llm_backend"] != "api" || evaluation["target_kind"] != "public_practice" {
		t.Fatalf("evaluation: %v", ready["evaluation"])
	}
	reset := strings.Repeat("a", 64)
	if bound := f.ask(t, "bind", map[string]any{"reset_generation": reset}); bound["reset_generation"] != reset {
		t.Fatalf("bind: %v", bound)
	}
	if head := f.ask(t, "head", nil); head["state"] != "no_execution" {
		t.Fatalf("head: %v", head)
	}
	closeRef := map[string]any{"close_request_ref": "close-1"}
	if closed := f.ask(t, "close", closeRef); closed["admission_closed"] != true {
		t.Fatalf("close: %v", closed)
	}
	var status map[string]any
	for deadline := time.Now().Add(10 * time.Second); ; {
		status = f.ask(t, "close_status", closeRef)
		if status["state"] != "closing" || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status["state"] != "no_execution" {
		t.Fatalf("close_status: %v", status)
	}
}

func TestE2EvalServiceRefusesOtherRuns(t *testing.T) {
	f := startE2EvalFixture(t)
	defer f.close(t)
	if refused := f.ask(t, "ready", map[string]any{"eval_run_id": "other-run"}); refused["type"] != "error" || refused["code"] != "other_run" {
		t.Fatalf("다른 회차: %v", refused)
	}
}

// 이 단계의 핵심 불변식이다. 엔진 호출 경로가 붙기 전에는 어떤 실행도 생기면 안 된다.
func TestE2EvalServiceGrantsNoExecution(t *testing.T) {
	f := startE2EvalFixture(t)
	defer f.close(t)
	f.ask(t, "bind", map[string]any{"reset_generation": strings.Repeat("a", 64)})
	_, err := f.service.backend.Execute(context.Background(), E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 23, IntentID: 17}, "true")
	if err == nil {
		t.Fatal("실행 권한 없는 서비스가 명령을 실행")
	}
	if head := f.ask(t, "head", nil); head["state"] != "no_execution" {
		t.Fatalf("거절 뒤 실행이 생김: %v", head)
	}
	if err := (e2EvalNoExecution{}).AuthorizeExecution(context.Background(), transmit.Scope{TaskRef: "11"}, 17); !errors.Is(err, ErrE2Control) {
		t.Fatal("실행 확인이 거절하지 않음:", err)
	}
}

func TestE2EvalServiceStopsAnsweringAfterClose(t *testing.T) {
	f := startE2EvalFixture(t)
	f.close(t)
	if conn, err := net.DialTimeout("unix", f.socket, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("종료 뒤에도 제어 소켓이 열림")
	}
	f.service.backend.mu.Lock()
	disposed := f.service.backend.disposed
	f.service.backend.mu.Unlock()
	if !disposed {
		t.Fatal("종료 뒤에도 backend가 정리되지 않음")
	}
}

func TestE2EvalServiceCloseReportsControlFailure(t *testing.T) {
	f := startE2EvalFixture(t)
	if err := f.service.Close(nil); !errors.Is(err, ErrE2Control) { //nolint:staticcheck // 제어 채널 종료 실패가 전달되는지 시험한다.
		t.Fatal("제어 채널 종료 실패를 숨김:", err)
	}
}

func TestE2EvalServiceRejectsBadInput(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("비특권 Linux 제어 fixture")
	}
	run := e2ProducedRunFixture(t)
	valid := E2EvalServiceOptions{ConfigPath: run.configPath, IOTimeout: time.Second, CloseTimeout: time.Second}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx    context.Context
		change func(*E2EvalServiceOptions)
		stage  string
	}{
		"취소된 시작":    {cancelled, func(*E2EvalServiceOptions) {}, "input"},
		"설정 경로 없음":  {context.Background(), func(o *E2EvalServiceOptions) { o.ConfigPath = "" }, "input"},
		"없는 설정 파일":  {context.Background(), func(o *E2EvalServiceOptions) { o.ConfigPath += ".missing" }, "config"},
		"입출력 시한 0":  {context.Background(), func(o *E2EvalServiceOptions) { o.IOTimeout = 0 }, "control"},
		"입출력 시한 초과": {context.Background(), func(o *E2EvalServiceOptions) { o.IOTimeout = 2 * time.Minute }, "control"},
		"종료 시한 0":   {context.Background(), func(o *E2EvalServiceOptions) { o.CloseTimeout = 0 }, "control"},
		"종료 시한 초과":  {context.Background(), func(o *E2EvalServiceOptions) { o.CloseTimeout = 3 * time.Minute }, "control"},
	} {
		t.Run(name, func(t *testing.T) {
			options := valid
			tc.change(&options)
			if _, err := StartE2EvalService(tc.ctx, options); !errors.Is(err, ErrE2Control) || !strings.Contains(err.Error(), ": "+tc.stage) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if _, err := StartE2EvalService(nil, valid); !errors.Is(err, ErrE2Control) { //nolint:staticcheck // nil 문맥 거부를 시험한다.
		t.Fatal("nil 문맥 수용")
	}
}

func TestE2EvalAuthorityAcceptsOnlyTheLoadedPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	digest := sha256.Sum256([]byte("policy"))
	authority := e2EvalAuthority{ctx: ctx, digest: digest}
	if err := authority.AuthorizePolicy(context.Background(), digest); err != nil {
		t.Fatal("같은 정책 거부:", err)
	}
	if err := authority.AuthorizePolicy(context.Background(), sha256.Sum256([]byte("other"))); err == nil {
		t.Fatal("다른 정책 수용")
	}
	cancel()
	if err := authority.AuthorizePolicy(context.Background(), digest); err == nil {
		t.Fatal("서비스 종료 뒤 정책 수용")
	}
}
