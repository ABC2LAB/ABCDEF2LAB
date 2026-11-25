package e2proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 승인 대기 문맥은 닫힌 원인을 싣는다: 모델 단계 마감은 ErrModelStageClosed, call 기한은 DeadlineExceeded, 호출자 취소는
// Canceled. 승인 이력이 이 원인으로 closed_reason을 가린다(2026-11-25 경준 요청).
func TestRequestApprovalWaitCarriesCloseCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"model_stage_closed", ErrModelStageClosed},
		{"call_deadline", context.DeadlineExceeded},
		{"caller_cancel", context.Canceled},
		{"request_deadline", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("승인 전에 대상에 감") }))
			defer target.Close()
			cfg := coreConfig(t, target.URL, &coreRecorder{}, 1)
			if tc.name == "call_deadline" {
				cfg.Limits.CallTimeout = 50 * time.Millisecond
			}
			entered := make(chan struct{})
			causes := make(chan error, 1)
			cfg.ApprovalMode = ApprovalConfirmEveryRequest
			cfg.RequestApproval = requestApprovalFunc(func(ctx context.Context, _ RequestApprovalProposal) (bool, error) {
				close(entered)
				<-ctx.Done()
				causes <- context.Cause(ctx)
				return false, ctx.Err()
			})
			e, c := coreOpen(t, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			if tc.name == "request_deadline" {
				// 요청 문맥의 기한(E③ 확정 요청처럼 요청 기한이 있는 경우)은 승인 대기 문맥에 DeadlineExceeded로 실린다.
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			}
			defer cancel()
			done := make(chan Receipt, 1)
			go func() { _, r, _ := c.Do(ctx, coreRequest(t, "GET", target.URL+"/allowed")); done <- r }()
			<-entered
			switch tc.name {
			case "model_stage_closed":
				e.CloseModelStage()
			case "caller_cancel":
				cancel()
			}
			select {
			case got := <-causes:
				if !errors.Is(got, tc.want) {
					t.Fatalf("승인 대기 취소 원인 %v, 기대 %v", got, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("승인 대기가 끝나지 않음")
			}
			if r := <-done; !r.NotSent {
				t.Fatal("닫힌 승인 대기가 송신함")
			}
		})
	}
}
