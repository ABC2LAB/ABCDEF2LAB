package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// C03: 한 intent(Worker)의 결과 불명은 그 intent만 멈추고 다른 intent의 Bash 도구를 멈추지 않는다.
func TestE2HTTPBashIgnoresAnotherIntentUnknown(t *testing.T) {
	// 요청을 읽고 응답 없이 끊는 대상이다. 연결 뒤 실패라 결과 불명으로 남는다.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 512)
			_, _ = conn.Read(buf)
			conn.Close()
		}
	}()
	origin := "http://" + listener.Addr().String()
	execution, _, binding := e2WorkerFixtureTarget(t, origin, "/")
	const unknownIntent, otherIntent = int64(22), int64(23)
	call, err := execution.BeginCallForIntent(context.Background(), unknownIntent, "unknown-worker")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", origin+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, r, _ := call.Do(context.Background(), req); r.Reason != e2proxy.Unknown {
		t.Fatal("fixture did not produce an unknown outcome", r.Reason)
	}
	if _, err = call.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = execution.RegisterIntent(context.Background(), otherIntent); err != nil {
		t.Fatal(err)
	}
	completed := func(intent int64) e2HTTPRunFunc {
		return func(context.Context, string) (e2runner.Result, error) {
			return e2runner.Result{ToolOutput: "synthetic", Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: execution.SnapshotForIntent(intent),
				Call:  e2proxy.CallResult{IntentID: intent, CallRef: "synthetic-call", Final: true, AuditComplete: true},
				Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed"}}, nil
		}
	}
	other := &e2HTTPToolState{}
	bash := newE2HTTPBashTool(execution, completed(otherIntent), time.Second, other, binding.Control, json.RawMessage(`{"summary":"other intent"}`), otherIntent)
	result, err := bash.Call(context.Background(), json.RawMessage(`{"command":"printf other"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("another intent's unknown outcome failed this intent: %v %s", err, result.Flatten())
	}
	other.mu.Lock()
	halted, unknown := other.halted, other.unknown
	other.mu.Unlock()
	if halted || unknown {
		t.Fatal("another intent's unknown outcome halted this intent")
	}
	// 대조: 불명이 난 intent 자신은 같은 명령을 자동으로 이어 보내지 않도록 멈춘다.
	same := &e2HTTPToolState{}
	bash = newE2HTTPBashTool(execution, completed(unknownIntent), time.Second, same, binding.Control, json.RawMessage(`{"summary":"unknown intent"}`), unknownIntent)
	result, err = bash.Call(context.Background(), json.RawMessage(`{"command":"printf same"}`), nil)
	same.mu.Lock()
	halted, unknown = same.halted, same.unknown
	same.mu.Unlock()
	if err != nil || !result.IsError || !halted || !unknown {
		t.Fatalf("unknown outcome did not halt its own intent: %v halted=%v unknown=%v", err, halted, unknown)
	}
}

// 다른 intent의 불명이 실행 전체 누계에만 있으면 이 call의 정책 분류를 가리지 않는다. 연결 실패는 분류를 유지한다.
func TestE2HTTPPolicyClassificationUsesIntentCounts(t *testing.T) {
	result := completedE2WorkerResult()
	result.Trusted.Execution.AuditComplete = true
	result.Trusted.Execution.Counts.Unknown = 1
	result.Trusted.Call.Counts.Denied = 1
	if got := e2HTTPPolicyClassification(result); got != "policy_not_executed" {
		t.Fatalf("another intent's unknown hid this call's denial: %s", got)
	}
	result.Trusted.Execution.IntentCounts.Unknown = 1
	if got := e2HTTPPolicyClassification(result); got != "completed" {
		t.Fatalf("this intent's unknown was reclassified by policy counts: %s", got)
	}
	result.Trusted.Execution.IntentCounts.Unknown = 0
	result.Trusted.Classification = "transient_failure"
	result.Trusted.Call.Counts.Transient = 1
	if got := e2HTTPPolicyClassification(result); got != "transient_failure" {
		t.Fatalf("connection failure was reclassified by policy counts: %s", got)
	}
}

// 리뷰 재현(#477): 연결 전 송신 실패와 비정상 셸 종료가 겹친 완결된 명령은 그 명령만 실패다. 다음 명령은 막지 않는다.
func TestE2HTTPConnectFailureWithNonzeroExitDoesNotHaltWorker(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	calls := 0
	tool := e2WorkerBashTool(t, execution, func(context.Context, string) (e2runner.Result, error) {
		calls++
		result := completedE2WorkerResult()
		if calls == 1 {
			result.Trusted.Classification = "transient_failure"
			result.Trusted.ExitCode = 7
			result.Trusted.Call.Counts = e2proxy.Counts{Attempts: 1, Allowed: 1, ForwardAttempted: 1, Transient: 1}
			return result, e2runner.ErrUnavailable
		}
		return result, nil
	}, time.Second, state)
	first, err := tool.Call(context.Background(), json.RawMessage(`{"command":"curl --fail first"}`), nil)
	if err != nil || !first.IsError || state.runnerFailed || state.halted {
		t.Fatalf("connect failure halted the worker: state=%+v err=%v", state, err)
	}
	second, err := tool.Call(context.Background(), json.RawMessage(`{"command":"curl --fail second"}`), nil)
	if err != nil || second.IsError || calls != 2 {
		t.Fatalf("next command was blocked after a connect failure: calls=%d result=%s err=%v", calls, second.Flatten(), err)
	}
	// 대조: 송신 실패 수가 없는 transient_failure(프록시 장애·시간 초과)는 계속 runner 장애다.
	failed := completedE2WorkerResult()
	failed.Trusted.Classification = "transient_failure"
	failed.Trusted.ExitCode = 7
	failed.Trusted.Execution.AuditComplete = true
	if !e2HTTPRunnerFailed(e2runner.ErrUnavailable, failed) {
		t.Fatal("proxy-unavailable transient failure was treated as an ordinary command failure")
	}
}
