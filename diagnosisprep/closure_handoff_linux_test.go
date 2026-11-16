//go:build linux

package diagnosisprep

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosis"
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
			recovery := &recoveringBackend{fakeBackend: b, verified: true}
			s.backend = recovery
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
			if recovery.checks.Load() != 0 {
				t.Fatal("Execute 반환 전에 복구를 호출")
			}
		})
	}
}

type recoveringBackend struct {
	*fakeBackend
	verified bool
	checks   atomic.Int64
}

func (b *recoveringBackend) VerifyClosure(context.Context, Execution) error {
	b.checks.Add(1)
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
	invalidBoot, err := newServiceForBoot(context.Background(), root, b, "unverified-boot")
	if err != nil {
		t.Fatal(err)
	}
	if invalidBoot.jobs[first.ID].ResourcesRecovered {
		t.Fatal("형식 밖 boot 증거로 복구")
	}
	if invalidBoot.Shutdown(context.Background()) != nil {
		t.Fatal("종료 실패")
	}
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

// 구 버전이 남긴 보호 영수증을 만들고 업그레이드한다. backend 실행/종료를 꾸며 반환 증거를 넣지 않는다.
func TestLegacyUnknownRecoveryWitnessRequiresLaterHostBoot(t *testing.T) {
	for _, drained := range []bool{false, true} {
		t.Run(map[bool]string{false: "undrained", true: "drained_unknown"}[drained], func(t *testing.T) {
			s, b, root := newFixture(t)
			b.inspection.MaxRuns = 2
			legacy := create(t, s, "legacy-create")
			s.mu.Lock()
			r := s.jobs[legacy.ID]
			r.StartKey = "legacy-start"
			r.Job.PreparationStarted = true
			r.Job.State = Preparing
			r.Job.Phase = ScopeCheck
			r.Job.Revision++
			if err := s.save(r); err != nil {
				s.mu.Unlock()
				t.Fatal(err)
			}
			r.Job.State = Unknown
			r.Job.DrainComplete = drained
			r.Job.Phase = Closure
			r.Job.ReasonCode = "execution_closure_unconfirmed"
			r.Job.Revision++
			if err := s.save(r); err != nil {
				s.mu.Unlock()
				t.Fatal(err)
			}
			before := cloneReceipt(r).Job
			s.mu.Unlock()
			if s.Shutdown(context.Background()) != nil {
				t.Fatal("기존 설치 종료 실패")
			}
			const firstBoot = "00000000-0000-0000-0000-000000000001"
			const laterBoot = "00000000-0000-0000-0000-000000000002"
			// boot를 못 읽으면 증거를 만들지 않고 조회만 복원한다.
			missing, err := newServiceForBoot(context.Background(), root, b, "")
			if err != nil {
				t.Fatal(err)
			}
			if missing.jobs[legacy.ID].ResourcesRecovered || missing.jobs[legacy.ID].RecoveryWitnessBootID != "" {
				t.Fatal("누락된 boot 증거를 추정")
			}
			if missing.Shutdown(context.Background()) != nil {
				t.Fatal("종료 실패")
			}
			upgraded, err := newServiceForBoot(context.Background(), root, b, firstBoot)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(status(t, upgraded, legacy.ID), before) {
				t.Fatal("업그레이드가 원래 결과를 변경")
			}
			if upgraded.jobs[legacy.ID].ExecutionReturned {
				t.Fatal("구 형식 Unknown을 정상 마감으로 오인")
			}
			if upgraded.jobs[legacy.ID].RecoveryWitnessBootID != firstBoot {
				t.Fatal("보호된 복구 관찰 증거 미저장")
			}
			next := create(t, upgraded, "legacy-next")
			if start(t, upgraded, next, "legacy-next-start").Error != "busy" {
				t.Fatal("관찰만으로 잠금 해제")
			}
			if upgraded.Shutdown(context.Background()) != nil {
				t.Fatal("종료 실패")
			}
			same, err := newServiceForBoot(context.Background(), root, b, firstBoot)
			if err != nil {
				t.Fatal(err)
			}
			if start(t, same, next, "legacy-next-start").Error != "busy" {
				t.Fatal("같은 boot 재시작으로 구 실행 복구")
			}
			if same.Shutdown(context.Background()) != nil {
				t.Fatal("종료 실패")
			}
			recovered, err := newServiceForBoot(context.Background(), root, b, laterBoot)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Shutdown(context.Background())
			if !reflect.DeepEqual(status(t, recovered, legacy.ID), before) {
				t.Fatal("호스트 복구가 원래 사실/예약을 변경")
			}
			if b.calls.Load() != 0 {
				t.Fatal("이전 실행 재생")
			}
			if start(t, recovered, next, "legacy-next-start").Error != "" {
				t.Fatal("실제 호스트 종료 후 구 형식 복구 실패")
			}
			awaitExecutionReturned(t, recovered, next.ID)
			last := create(t, recovered, "legacy-exhausted")
			if start(t, recovered, last, "legacy-exhausted-start").Error != "budget_exhausted" {
				t.Fatal("구 형식의 실행 횟수를 환불")
			}
			old := recovered.jobs[legacy.ID]
			changed := cloneReceipt(old)
			changed.RecoveryWitnessBootID = laterBoot
			if validReceiptChange(old, changed) {
				t.Fatal("저장된 복구 관찰 증거 교체 허용")
			}

		})
	}
}

func TestLegacyDrainedTerminalUpgradeKeepsInstallationUsable(t *testing.T) {
	for _, state := range []State{Completed, Failed, Cancelled} {
		t.Run(string(state), func(t *testing.T) {
			s, b, root := newFixture(t)
			b.inspection.MaxRuns = 2
			old := create(t, s, "legacy-terminal")
			s.mu.Lock()
			r := s.jobs[old.ID]
			r.StartKey = "legacy-terminal-start"
			r.Job.PreparationStarted = true
			r.Job.State = Preparing
			r.Job.Phase = ScopeCheck
			r.Job.Revision++
			if s.save(r) != nil {
				s.mu.Unlock()
				t.Fatal("구 형식 시작 저장 실패")
			}
			r.Job.State = state
			r.Job.Phase = Report
			r.Job.TaskID = "41"
			r.Job.Started = true
			r.Job.AuthorityState = AuthorityConsumed
			r.Job.DrainComplete = true
			r.Job.ResultReady = true
			r.Job.Revision++
			if state == Completed {
				r.Job.ResultReady = true
				r.Job.Outcome = "complete"
				r.Job.IntentOutcomes = &diagnosis.IntentOutcomes{Done: 1}
			}
			if s.save(r) != nil {
				s.mu.Unlock()
				t.Fatal("구 형식 마감 저장 실패")
			}
			before := cloneReceipt(r).Job
			s.mu.Unlock()
			if s.Shutdown(context.Background()) != nil {
				t.Fatal("종료 실패")
			}
			upgraded, err := New(context.Background(), root, b)
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.Shutdown(context.Background())
			if !reflect.DeepEqual(status(t, upgraded, old.ID), before) {
				t.Fatal("기존 마감 사실 변경")
			}
			next := create(t, upgraded, "legacy-terminal-next")
			if got := start(t, upgraded, next, "legacy-terminal-next-start"); got.Error != "" {
				t.Fatal("정상 구 형식 회차가 업그레이드 후 시작을 차단", got.Error)
			}
			awaitExecutionReturned(t, upgraded, next.ID)
			last := create(t, upgraded, "legacy-terminal-last")
			if start(t, upgraded, last, "legacy-terminal-last-start").Error != "budget_exhausted" {
				t.Fatal("구 회차 실행 횟수 환불")
			}
		})
	}
}

func TestNewEvidenceReceiptWithoutBootIsNotLegacyTerminal(t *testing.T) {
	s, backend, root := newFixture(t)
	old := create(t, s, "new-no-boot")
	s.mu.Lock()
	r := s.jobs[old.ID]
	r.StartKey = "new-no-boot-start"
	r.ExecutionEvidenceVersion = 1
	r.Job.PreparationStarted = true
	r.Job.State = Failed
	r.Job.Phase = Closure
	r.Job.DrainComplete = true
	r.Job.Revision++
	if s.save(r) != nil {
		s.mu.Unlock()
		t.Fatal("저장 실패")
	}
	s.mu.Unlock()
	if s.Shutdown(context.Background()) != nil {
		t.Fatal("종료 실패")
	}
	restored, err := New(context.Background(), root, backend)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown(context.Background())
	next := create(t, restored, "new-no-boot-next")
	if start(t, restored, next, "new-no-boot-next-start").Error != "busy" {
		t.Fatal("새 형식 미반환을 구 형식으로 잘못 복원")
	}
}

func TestStartPersistsExecutionEvidenceVersion(t *testing.T) {
	s, b, root := newFixture(t)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	b.run = func(ctx context.Context, e Execution, p Publish) error {
		select {
		case <-release:
			return completeRun(ctx, e, p)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	job := create(t, s, "version-create")
	if got := start(t, s, job, "version-start"); got.Error != "" {
		t.Fatal(got.Error)
	}
	s.mu.Lock()
	saved := cloneReceipt(s.jobs[job.ID])
	s.mu.Unlock()
	if saved.ExecutionEvidenceVersion != 1 {
		t.Fatal("새 시작에 실행 증거 버전 1 미저장")
	}
	// 유효한 다음 기록의 버전만 바꿔 가드를 독립적으로 검증한다.
	next := cloneReceipt(saved)
	next.Job.Revision++
	if !validReceiptChange(saved, next) {
		t.Fatal("대조 기록 거절")
	}
	next.ExecutionEvidenceVersion = 0
	if validReceiptChange(saved, next) {
		t.Fatal("시작 후 증거 버전 변경 허용")
	}
	unblock()
	awaitExecutionReturned(t, s, job.ID)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, err := New(context.Background(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Shutdown(context.Background())
	if restored.jobs[job.ID].ExecutionEvidenceVersion != 1 {
		t.Fatal("재시작 뒤 실행 증거 버전 유실")
	}
}
