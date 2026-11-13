//go:build linux

package diagnosisprep

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func awaitExecutionReturned(t *testing.T, s *Service, id string) {
	t.Helper()
	s.mu.Lock()
	h := s.executions[id]
	s.mu.Unlock()
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		t.Fatal("backend 반환 미확인")
	}
}

func TestUnknownPublicationDoesNotReleaseExecution(t *testing.T) {
	for _, drained := range []bool{false, true} {
		t.Run(map[bool]string{false: "undrained", true: "drained_publication"}[drained], func(t *testing.T) {
			s, b, _ := newFixture(t)
			published, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			b.run = func(_ context.Context, _ Execution, p Publish) error {
				if err := p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, AuthorityState: AuthorityNotIssued, DrainComplete: drained}); err != nil {
					return err
				}
				close(published)
				<-release
				return errors.New("미확인")
			}
			first := create(t, s, "held-first")
			start(t, s, first, "held-start")
			<-published
			second := create(t, s, "held-next")
			for i := 0; i < 20; i++ {
				if got := start(t, s, second, "held-next-start"); got.Error != "busy" {
					t.Fatal("반환 전 새 실행 허용")
				}
			}
			if b.calls.Load() != 1 {
				t.Fatal("동시 backend 실행")
			}
		})
	}
}

type recoveringBackend struct {
	*fakeBackend
	verified bool
}

func (b *recoveringBackend) VerifyClosure(context.Context, Execution) error {
	if !b.verified {
		return errors.New("자원 미마감")
	}
	return nil
}

func TestUnknownClosureRecoveryPreservesFactsAndReservations(t *testing.T) {
	s, b, root := newFixture(t)
	recovery := &recoveringBackend{fakeBackend: b}
	s.backend = recovery
	b.run = func(_ context.Context, _ Execution, p Publish) error {
		return p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, AuthorityState: AuthorityNotIssued})
	}
	first := create(t, s, "recovery-first")
	start(t, s, first, "recovery-start")
	awaitExecutionReturned(t, s, first.ID)
	before := status(t, s, first.ID)
	next := create(t, s, "recovery-next")
	if start(t, s, next, "recovery-next-start").Error != "busy" {
		t.Fatal("미마감 실행 허용")
	}
	recovery.verified = true
	if start(t, s, next, "recovery-next-start").Error != "" {
		t.Fatal("검증 복구 이후 시작 거절")
	}
	awaitExecutionReturned(t, s, next.ID)
	after := status(t, s, first.ID)
	after.Revision = before.Revision
	if !reflect.DeepEqual(before, after) {
		t.Fatal("복구가 결과 사실을 바꿈")
	}
	if s.Shutdown(context.Background()) != nil {
		t.Fatal("종료 실패")
	}
	restarted, err := New(context.Background(), root, recovery)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown(context.Background())
	// 두 회차 예약을 보존하며 마지막 허용 회차까지 실행, 그 다음은 예산 소진.
	third := create(t, restarted, "recovery-third")
	if start(t, restarted, third, "recovery-third-start").Error != "" {
		t.Fatal("복구 증거 재시작 복원 실패")
	}
	awaitExecutionReturned(t, restarted, third.ID)
	fourth := create(t, restarted, "recovery-fourth")
	if start(t, restarted, fourth, "recovery-fourth-start").Error != "budget_exhausted" {
		t.Fatal("이전 예약 또는 실행 횟수 해제")
	}
}

func TestUnknownRecoveryRequiresVerifiedHostBootChange(t *testing.T) {
	s, b, root := newFixture(t)
	b.run = func(_ context.Context, _ Execution, p Publish) error {
		return p(Update{State: Unknown, Phase: Closure, PreparationStarted: true, AuthorityState: AuthorityNotIssued})
	}
	first := create(t, s, "boot-first")
	start(t, s, first, "boot-start")
	awaitExecutionReturned(t, s, first.ID)
	before := status(t, s, first.ID)
	if s.Shutdown(context.Background()) != nil {
		t.Fatal("종료 실패")
	}
	// 같은 커널의 서비스 재시작: 반환했어도 미마감 자원은 자동 복구하지 않는다.
	same, err := New(context.Background(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	next := create(t, same, "boot-next")
	if start(t, same, next, "boot-next-start").Error != "busy" {
		t.Fatal("같은 호스트 boot에서 임의 복구")
	}
	if same.Shutdown(context.Background()) != nil {
		t.Fatal("종료 실패")
	}
	// 커널 자원 소멸만 모의한다. OS boot ID를 변경하거나 모델·대상을 호출하지 않는다.
	changed := "00000000-0000-0000-0000-000000000001"
	if executionBootID() == changed {
		changed = "00000000-0000-0000-0000-000000000002"
	}
	recovered, err := newServiceForBoot(context.Background(), root, b, changed)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Shutdown(context.Background())
	if got := status(t, recovered, first.ID); !reflect.DeepEqual(got, before) {
		t.Fatal("호스트 복구가 원래 결과를 바꿈")
	}
	if b.calls.Load() != 1 {
		t.Fatal("복구가 실행을 재생")
	}
	if start(t, recovered, next, "boot-next-start").Error != "" {
		t.Fatal("검증된 호스트 종료 후 잠금 유지")
	}
	awaitExecutionReturned(t, recovered, next.ID)
}
