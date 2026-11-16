//go:build linux

package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func TestE2HTTPRuntimeAdmissionNotSentMatchesActualAudit(t *testing.T) {
	for _, mode := range []string{"valid", "duplicate", "forged-command", "wrong-intent", "observer-error"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, _ := e2HostPolicyFixture(t)
			opts, err := LoadE2HTTPRuntimeOptions(t.Context(), E2HTTPFileOptions{PolicyFile: cfg.PolicyFile, Authority: &e2HTTPPolicyAuthorityFixture{ctx: t.Context()}, Supervisor: &e2HTTPSupervisorFixture{}})
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewE2HTTPRuntime(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close(context.Background())
			task := &Task{ID: "11", ExpID: 27, Store: (&db.DB{}).Exploration(27)}
			if r.AdmitTask(t.Context(), task) != nil {
				t.Fatal("admission failed")
			}
			lease, err := opts.Acquire(t.Context(), E2HTTPRequest{TaskID: "11", TaskIDNumber: 11, ExplorationID: 27, IntentID: 1})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = lease.Runner.Close(context.Background())
				_ = lease.Execution.Stop(context.Background())
				_ = lease.Release(context.Background())
			}()
			receipt, err := lease.Execution.RecordAdmissionNotSent(t.Context(), 1, "command_queue_expired")
			if err != nil {
				t.Fatal(err)
			}
			result := e2runner.Result{ToolOutput: "must not be retained", Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: lease.Execution.SnapshotForIntent(1), AdmissionNotSent: &receipt, Final: true, ProcessDrained: true, ProxyDrained: true, Classification: "not_sent"}}
			observed := 0
			lease.ObserveResult = func(_ context.Context, result e2runner.Result) error {
				observed++
				if result.ToolOutput != "" {
					t.Error("미전송 관찰에 원문 출력 보관")
				}
				if mode == "observer-error" {
					return ErrE2HTTPHost
				}
				return nil
			}
			switch mode {
			case "duplicate":
				if r.observeResult(t.Context(), r.task, &lease, 1, result) != nil {
					t.Fatal("valid result rejected")
				}
			case "forged-command":
				receipt.CommandRef = "forged"
			case "wrong-intent":
				result.Trusted.Execution.IntentID = 2
			}
			err = r.observeResult(t.Context(), r.task, &lease, 1, result)
			if (err == nil) != (mode == "valid") {
				t.Fatal("실제 감사 영수증 검증 불일치", mode, err)
			}
			if len(r.Results(task.ID)) != 0 || len(lease.Execution.Calls()) != 0 {
				t.Fatal("미전송이 닫힌 호출 투영에 섞임")
			}
			if mode == "valid" && (observed != 1 || r.task.resultsFailed) {
				t.Fatal("유효 미전송이 실행 기록 실패로 표시됨")
			}
			if mode != "valid" && !r.task.resultsFailed {
				t.Fatal("위조·중복·관찰 실패가 마감 실패로 보존되지 않음")
			}
		})
	}
}
