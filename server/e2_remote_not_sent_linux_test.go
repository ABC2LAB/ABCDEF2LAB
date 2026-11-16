//go:build linux

package server

import (
	"errors"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2runner"
)

func TestE2RemoteAdmissionNotSentMatchesProducerAck(t *testing.T) {
	for _, mode := range []string{"actual", "forged", "forged-receipt", "no-rpc-proof", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			f := startE2RemoteFixture(t)
			run := f.acquire(t)
			// Close the real runner alone; execution and its durable journal remain
			// open. A later RPC command is refused before any Call/process exists.
			if f.backend.acquired.Runner.Close(t.Context()) != nil {
				t.Fatal("owned runner close failed")
			}
			result, err := run.RunForIntent(t.Context(), 17, "printf must-not-start")
			if !errors.Is(err, e2runner.ErrUnavailable) || !e2runner.AdmissionNotSent(result) || len(f.backend.acquired.Execution.Calls()) != 0 || f.received.Load() != 0 {
				t.Fatal("실제 producer 미전송 감사 실패", err)
			}
			if !f.backend.acquired.Execution.HasAdmissionNotSent(*result.Trusted.AdmissionNotSent) {
				t.Fatal("producer 감사 ACK 누락")
			}
			r, task := e2HTTPRuntimeFixture(t, nil)
			task.ID = "11"
			task.ExpID = 23
			task.Store = (&db.DB{}).Exploration(23)
			if r.AdmitTask(t.Context(), task) != nil {
				t.Fatal("owned admission failed")
			}
			lease := &E2HTTPRunOptions{Runner: run}
			switch mode {
			case "forged":
				result.Trusted.AdmissionNotSent.CommandRef = "forged"
			case "forged-receipt":
				result.Trusted.AdmissionNotSent.At = result.Trusted.AdmissionNotSent.At.Add(1)
			case "no-rpc-proof":
				lease.Runner = &e2RemoteRun{client: run.client, ctx: run.ctx, q: run.q}
			case "duplicate":
				if r.observeResult(t.Context(), r.task, lease, 17, result) != nil {
					t.Fatal("valid remote receipt refused")
				}
			}
			err = r.observeResult(t.Context(), r.task, lease, 17, result)
			if (err == nil) != (mode == "actual") {
				t.Fatal("원격 미전송 영수증 검증 불일치", mode, err)
			}
			if len(r.Results(task.ID)) != 0 || len(f.backend.acquired.Execution.Calls()) != 0 {
				t.Fatal("원격 미전송이 닫힌 호출 계수로 섞임")
			}
			if mode == "actual" && r.task.resultsFailed {
				t.Fatal("원격 감사된 미전송을 결과 불명/기록 실패로 처리")
			}
		})
	}
}
