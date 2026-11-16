//go:build linux

package e2export

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func TestPublisherAdmissionNotSentVerifiedWithoutCall(t *testing.T) {
	for _, forged := range []bool{false, true} {
		t.Run(map[bool]string{false: "actual", true: "forged"}[forged], func(t *testing.T) {
			f := newPublisherFixture(t)
			receipt, err := f.cfg.Execution.RecordAdmissionNotSent(t.Context(), 1, "command_queue_expired")
			if err != nil {
				t.Fatal(err)
			}
			if forged {
				receipt.CommandRef = "forged_command"
			}
			result := e2runner.Result{Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: f.cfg.Execution.SnapshotForIntent(1), AdmissionNotSent: &receipt, Final: true, ProcessDrained: true, ProxyDrained: true, Classification: "not_sent"}}
			err = f.publisher.ObserveResult(t.Context(), result)
			if forged {
				if !errors.Is(err, ErrAudit) {
					t.Fatal("위조된 미전송 영수증을 수용")
				}
				if _, err = f.publisher.Publish(t.Context(), "close_not_sent"); !errors.Is(err, ErrAudit) {
					t.Fatal("위조 영수증 뒤 정상 게시", err)
				}
				return
			}
			if err != nil || len(f.cfg.Execution.Calls()) != 0 || f.received.Load() != 0 {
				t.Fatal("유효 미전송으로 호출·수신 생성", err)
			}
			published, err := f.publisher.Publish(t.Context(), "close_not_sent")
			if err != nil {
				t.Fatal(err)
			}
			journal, err := os.ReadFile(filepath.Join(f.cfg.OutputDirectory, JournalFile))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e2proxy.VerifyJournal(t.Context(), bytes.NewReader(journal), f.cfg.AuditKey, published.Checkpoint); err != nil {
				t.Fatal("실제 미전송 저널 검증 실패", err)
			}
			attempts, _, _, err := project(journal)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, line := range bytes.Split(attempts, []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				var event e2proxy.Event
				if json.Unmarshal(line, &event) != nil {
					t.Fatal("투영 형식 오류")
				}
				if event.CommandRef == receipt.CommandRef {
					found = event.Stage == "control" && event.NotSent && event.CallRef == "" && event.AttemptID == "" && !event.ForwardAttempted
				}
			}
			if !found {
				t.Fatal("미전송 제어 영수증이 내보내기에서 유실됨")
			}
		})
	}
}
