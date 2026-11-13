package server

import (
	"context"
	"github.com/Autumn-27/artex/exploration"
	"sync/atomic"
	"testing"
)

// 취소와 철회가 경합해도 private 마감에는 권한 종료가 먼저 반영돼야 한다.
func TestExplorationAuthorityEndPrecedesPrivateFinalization(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		o, _, _, _, _ := explorationSessionFixture(t, exploration.BrowserToolNames())
		var ended atomic.Bool
		o.TargetEnded = ended.Load
		var session *ExplorationSession
		var finalReason exploration.Reason
		o.Finalize = func(_ context.Context, _ exploration.Binding) error { finalReason = session.ClosedReason(); return nil }
		var err error
		session, err = OpenExplorationSession(t.Context(), o)
		if err != nil {
			t.Fatal(err)
		}
		ended.Store(revoked)
		if err = session.Close(t.Context(), exploration.Cancelled); err != nil {
			t.Fatal("취소 정리 실패")
		}
		want := exploration.Cancelled
		if revoked {
			want = exploration.TargetEnded
		}
		if session.ClosedReason() != want || finalReason != want {
			t.Fatal("private 마감에서 취소와 권한 종료가 섞임")
		}
	}
}
