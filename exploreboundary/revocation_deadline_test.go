package exploreboundary

import (
	"errors"
	"github.com/Autumn-27/artex/exploration"
	"testing"
	"time"
)

func TestTargetApprovalExpiryRemainsAuthorityEnd(t *testing.T) {
	old, source, clock := fixtureSession(t, exploration.ReadOnly, exploration.FullLocal, nil)
	source.mu.Lock()
	source.target.ExpiresAt = clock.now.Add(time.Second)
	source.mu.Unlock()
	session, err := Open(t.Context(), old.binding, old.flow.SourceDigest, source, old.policy)
	if err != nil {
		t.Fatal(err)
	}
	clock.add(2 * time.Second)
	decision, ticket := session.Admit(t.Context(), approved("GET", "/app"))
	if decision.Forward || ticket != nil || decision.Reason != ReasonTargetEnded || session.Status().ClosedReason != exploration.TargetEnded {
		t.Fatal("승인 만료가 요청·시간 예산 종료로 섞임")
	}
	if _, err = Open(t.Context(), old.binding, old.flow.SourceDigest, source, old.policy); !errors.Is(err, ErrTargetEnded) {
		t.Fatal("이미 만료된 승인으로 새 세션을 엶")
	}
}
