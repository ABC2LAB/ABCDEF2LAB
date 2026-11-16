package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func TestE2HTTPAdmissionNotSentAllowsSameCommandRetry(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	calls := 0
	tool := e2WorkerBashTool(t, execution, func(ctx context.Context, _ string) (e2runner.Result, error) {
		calls++
		if calls == 1 {
			receipt, err := execution.RecordAdmissionNotSent(ctx, 22, "command_queue_expired")
			if err != nil {
				t.Fatal(err)
			}
			return e2runner.Result{Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: execution.SnapshotForIntent(22), AdmissionNotSent: &receipt, Final: true, ProcessDrained: true, ProxyDrained: true, Classification: "not_sent"}}, e2runner.ErrUnavailable
		}
		call, err := execution.BeginCallForIntent(ctx, 22, "command_retry")
		if err != nil {
			t.Fatal(err)
		}
		closed, err := call.Close(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return e2runner.Result{Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: execution.SnapshotForIntent(22), Call: closed, Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed"}}, nil
	}, time.Second, state)
	input := json.RawMessage(`{"command":"printf retry"}`)
	if out, err := tool.Call(t.Context(), input, nil); err != nil || !out.IsError || state.classification() != "not_sent" {
		t.Fatal("미전송이 확정 실패로 반환되지 않음", err)
	}
	state.mu.Lock()
	halted, unknown, incomplete, failed := state.halted, state.unknown, state.incomplete, state.runnerFailed
	state.mu.Unlock()
	if halted || unknown || incomplete || failed || len(execution.Calls()) != 0 {
		t.Fatal("미전송이 집행 장애·결과 불명으로 intent를 중단")
	}
	if out, err := tool.Call(t.Context(), input, nil); err != nil || out.IsError || calls != 2 || state.classification() != "completed" {
		t.Fatal("같은 명령 재시도 또는 완료 분류 실패", err)
	}
	bad := e2runner.Result{Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: execution.SnapshotForIntent(22), Final: true, ProcessDrained: true, ProxyDrained: true, Classification: "not_sent"}}
	if e2HTTPPolicyClassification(bad) != "result_unknown" || !e2HTTPRunnerFailed(e2runner.ErrUnavailable, bad) {
		t.Fatal("영수증 없는 미전송 주장 수용")
	}
}
