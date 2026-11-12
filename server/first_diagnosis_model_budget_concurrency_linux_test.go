//go:build linux

package server

import (
	"context"
	"errors"
	"iter"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

type budgetConcurrentProvider struct {
	calls   atomic.Int64
	entered chan struct{}
	release <-chan struct{}
	usage   llm.Usage
	err     error
}

func (p *budgetConcurrentProvider) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls.Add(1)
	if p.entered != nil {
		close(p.entered)
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return llm.Message{}, "", llm.Usage{}, ctx.Err()
		}
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("owned synthetic completion")}}, "stop", p.usage, p.err
}
func (*budgetConcurrentProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("합성 nonstreaming fixture"))
	}
}

// Done 평가를 관측하여 두 번째 goroutine이 gate의 context select에 도착했음을 확인한다.
type budgetWaitingContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *budgetWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}
func budgetAwaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("합성 호출 단계 대기 시간 초과")
	}
}
func budgetAwaitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("합성 호출 마감 시간 초과")
		return nil
	}
}
func budgetRequireWaiting(t *testing.T, result <-chan error, provider *budgetConcurrentProvider) {
	t.Helper()
	select {
	case <-result:
		t.Fatal("선행 호출이 끝나기 전에 대기 호출이 종료됨")
	case <-time.After(25 * time.Millisecond):
	}
	if provider.calls.Load() != 0 {
		t.Fatal("대기 호출이 모델에 진입함")
	}
}

// 서로 다른 Server/provider의 실제 wrapper가 하나의 회차 예산을 공유한다.
// 실제 PG task/profile 결속은 유지하되 합성 provider 이외의 모델·대상에는 송신하지 않는다.
func TestFirstDiagnosisModelBudgetConcurrentWrappersOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	installer := &pgdb.DB{DB: f.Installer}
	profile, err := installer.SaveProfile(&pgdb.LLMProfile{Name: "owned-concurrent-budget", Format: "openai", Model: "owned-budget-model", BaseURL: "http://127.0.0.1:1/v1", APIKey: "synthetic-budget-key", Streaming: false, ContextWindowK: 64, MaxTokens: 128})
	if err != nil || profile <= 0 {
		t.Fatal("합성 profile 설치 실패")
	}
	for _, role := range []string{"goals", "planner", "worker"} {
		row, readErr := installer.GetAgentByKey(role)
		if readErr != nil {
			t.Fatal("합성 역할 조회 실패")
		}
		if row == nil {
			if _, err := installer.CreateAgent(role, "합성 예산 역할", "Owned synthetic budget fixture"); err != nil {
				t.Fatal("합성 역할 생성 실패")
			}
		}
		if _, err := installer.ExecContext(f.Context, "UPDATE agents SET llm_profile_id=NULL WHERE key=$1", role); err != nil {
			t.Fatal("합성 역할 초기화 실패")
		}
	}
	manager, err := NewFirstDiagnosisManager(f.Context, t.TempDir(), f.Runtime)
	if err != nil {
		t.Fatal("합성 manager 생성 실패")
	}
	t.Cleanup(func() {
		if manager.Close() != nil {
			t.Error("합성 manager 마감 실패")
		}
	})
	actual, err := PrepareFirstDiagnosisTask(f.Context, f.Runtime, "합성 동시 예산", "Verify shared model admission", profile, 60)
	if err != nil {
		t.Fatal("합성 task 준비 실패")
	}
	row, err := manager.pg.GetTask(actual.TaskID)
	if err != nil || row == nil {
		t.Fatal("합성 task 조회 실패")
	}
	task := taskFromPG(row, manager.pg.Exploration(row.ExplorationID), manager.interceptor)
	manager.mu.Lock()
	manager.tasks[task.ID] = task
	manager.mu.Unlock()
	resolver, err := llmpolicy.NewProfileResolver([]llmpolicy.ProfilePin{{Target: llmpolicy.ProfileTarget{ID: profile, Format: "openai", Model: "owned-budget-model", BaseURL: "http://127.0.0.1:1/v1"}, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}})
	if err != nil {
		t.Fatal("합성 모델 pin 생성 실패")
	}
	wrap := func(t *testing.T, b *FirstDiagnosisModelBudget, inner *budgetConcurrentProvider) *firstDiagnosisModelProvider {
		t.Helper()
		s := &Server{ctx: f.Context, m: manager, firstDiagnosis: true, localResolver: resolver, firstModelBudget: b, provByProfile: map[int64]*provEntry{}}
		cfg, ok := s.loadProfileConfig(profile)
		if !ok || s.bindFirstDiagnosisModel(f.Context, task) != nil {
			t.Fatal("실제 task/model 결속 실패")
		}
		s.provByProfile[profile] = &provEntry{prov: inner, cfg: cfg}
		return &firstDiagnosisModelProvider{server: s, task: task, profileID: profile, config: cfg, inner: inner}
	}
	newBudget := func(t *testing.T, calls int64, observe func(diagnosisprep.ModelUsage) error) *FirstDiagnosisModelBudget {
		t.Helper()
		b, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: calls, InputTokens: 2 * 65536, OutputTokens: 256}, 65536, 128, observe)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	syntheticRequest := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("owned synthetic request")}}
	reserve := reservedInputFor(t, syntheticRequest)
	call := func(ctx context.Context, p *firstDiagnosisModelProvider) <-chan error {
		result := make(chan error, 1)
		go func() {
			_, _, _, err := p.Complete(ctx, syntheticRequest)
			result <- err
		}()
		return result
	}
	knownUsage := llm.Usage{InputTokens: 10, OutputTokens: 20}
	noop := func(diagnosisprep.ModelUsage) error { return nil }

	t.Run("waits_through_usage_publication", func(t *testing.T) {
		ctx, cancel := context.WithCancel(f.Context)
		defer cancel()
		providerRelease := make(chan struct{})
		publicationEntered := make(chan struct{})
		publicationRelease := make(chan struct{})
		var observedRequests atomic.Int64
		b := newBudget(t, 2, func(u diagnosisprep.ModelUsage) error {
			observedRequests.Store(u.Requests)
			if u.Requests == 1 && u.UsageKnown {
				close(publicationEntered)
				select {
				case <-publicationRelease:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
		first := &budgetConcurrentProvider{entered: make(chan struct{}), release: providerRelease, usage: knownUsage}
		second := &budgetConcurrentProvider{usage: knownUsage}
		r1 := call(ctx, wrap(t, b, first))
		budgetAwaitSignal(t, first.entered)
		waiter := &budgetWaitingContext{Context: ctx, observed: make(chan struct{})}
		r2 := call(waiter, wrap(t, b, second))
		budgetAwaitSignal(t, waiter.observed)
		budgetRequireWaiting(t, r2, second)
		close(providerRelease)
		budgetAwaitSignal(t, publicationEntered)
		budgetRequireWaiting(t, r2, second)
		if observedRequests.Load() != 1 {
			t.Fatal("사용량 영속화 전 새 예약이 생김")
		}
		close(publicationRelease)
		if budgetAwaitResult(t, r1) != nil || budgetAwaitResult(t, r2) != nil {
			t.Fatal("직렬 대기한 정상 호출이 실패함")
		}
		u := b.Snapshot()
		if first.calls.Load() != 1 || second.calls.Load() != 1 || u.Requests != 2 || !u.UsageKnown || u.InputTokens != 20 || u.OutputTokens != 40 || u.ReservedInputTokens != 2*reserve || u.ReservedOutputTokens != 256 {
			t.Fatal("공유 예산 정산 불일치")
		}
	})
	t.Run("cancelled_waiter_reserves_nothing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(f.Context)
		defer cancel()
		release := make(chan struct{})
		b := newBudget(t, 2, noop)
		first := &budgetConcurrentProvider{entered: make(chan struct{}), release: release, usage: knownUsage}
		second := &budgetConcurrentProvider{usage: knownUsage}
		p2 := wrap(t, b, second)
		r1 := call(ctx, wrap(t, b, first))
		budgetAwaitSignal(t, first.entered)
		waitCtx, cancelWait := context.WithCancel(ctx)
		defer cancelWait()
		waiter := &budgetWaitingContext{Context: waitCtx, observed: make(chan struct{})}
		r2 := call(waiter, p2)
		budgetAwaitSignal(t, waiter.observed)
		cancelWait()
		if !errors.Is(budgetAwaitResult(t, r2), context.Canceled) || second.calls.Load() != 0 || b.Snapshot().Requests != 1 {
			t.Fatal("취소 대기가 예약 또는 모델 호출을 소비함")
		}
		close(release)
		if budgetAwaitResult(t, r1) != nil || budgetAwaitResult(t, call(ctx, p2)) != nil {
			t.Fatal("취소 대기가 정상 후속 호출을 막음")
		}
		if b.Snapshot().Requests != 2 || second.calls.Load() != 1 {
			t.Fatal("취소 대기 예산 누수")
		}
	})
	for _, tc := range []struct {
		name             string
		usage            llm.Usage
		callErr          error
		publicationFails bool
	}{
		{name: "missing_usage"},
		{name: "provider_error", callErr: errors.New("합성 전송 실패")},
		{name: "error_with_usage", usage: knownUsage, callErr: errors.New("합성 불명 응답")},
		{name: "usage_exceeds_limit", usage: llm.Usage{InputTokens: 65537, OutputTokens: 1}},
		{name: "usage_publication_failed", usage: knownUsage, publicationFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(f.Context)
			defer cancel()
			release := make(chan struct{})
			b := newBudget(t, 2, func(u diagnosisprep.ModelUsage) error {
				if tc.publicationFails && u.UsageKnown {
					return errors.New("합성 영속화 실패")
				}
				return nil
			})
			first := &budgetConcurrentProvider{entered: make(chan struct{}), release: release, usage: tc.usage, err: tc.callErr}
			second := &budgetConcurrentProvider{usage: knownUsage}
			r1 := call(ctx, wrap(t, b, first))
			budgetAwaitSignal(t, first.entered)
			waiter := &budgetWaitingContext{Context: ctx, observed: make(chan struct{})}
			r2 := call(waiter, wrap(t, b, second))
			budgetAwaitSignal(t, waiter.observed)
			budgetRequireWaiting(t, r2, second)
			close(release)
			if !errors.Is(budgetAwaitResult(t, r1), ErrFirstDiagnosisModelBudget) {
				t.Fatal("실패·불명 호출이 성공으로 보고됨")
			}
			if tc.callErr != nil {
				// 실패한 호출은 예약한 양으로 정산하고 다음 호출을 막지 않는다(P3 결정안 C05).
				secondErr := budgetAwaitResult(t, r2)
				u := b.Snapshot()
				if secondErr != nil || second.calls.Load() != 1 || u.Requests != 2 || !u.UsageKnown || u.UnknownUsageCalls != 1 || u.InputTokens != reserve+int64(knownUsage.InputTokens) {
					t.Fatalf("실패한 호출 하나가 다음 호출을 막거나 상한으로 정산되지 않음: %+v", u)
				}
				return
			}
			// 성공했는데 사용량이 없거나 예약을 넘거나 영속화가 실패하면 상한을 믿을 수 없어 막는다.
			if !errors.Is(budgetAwaitResult(t, r2), ErrFirstDiagnosisModelBudget) {
				t.Fatal("불명 결과 뒤 대기가 허용됨")
			}
			if second.calls.Load() != 0 || b.Snapshot().Requests != 1 || b.Snapshot().UsageKnown {
				t.Fatal("불명 결과가 새 모델 호출로 이어짐")
			}
		})
	}
	t.Run("request_limit_after_wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(f.Context)
		defer cancel()
		release := make(chan struct{})
		b := newBudget(t, 1, noop)
		first := &budgetConcurrentProvider{entered: make(chan struct{}), release: release, usage: knownUsage}
		second := &budgetConcurrentProvider{usage: knownUsage}
		r1 := call(ctx, wrap(t, b, first))
		budgetAwaitSignal(t, first.entered)
		waiter := &budgetWaitingContext{Context: ctx, observed: make(chan struct{})}
		r2 := call(waiter, wrap(t, b, second))
		budgetAwaitSignal(t, waiter.observed)
		budgetRequireWaiting(t, r2, second)
		close(release)
		if budgetAwaitResult(t, r1) != nil || !errors.Is(budgetAwaitResult(t, r2), ErrFirstDiagnosisModelBudget) || second.calls.Load() != 0 || b.Snapshot().Requests != 1 || !b.Snapshot().UsageKnown {
			t.Fatal("대기 후 요청 예산이 지켜지지 않음")
		}
	})
	t.Run("binding_rechecked_after_wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(f.Context)
		defer cancel()
		release := make(chan struct{})
		b := newBudget(t, 2, noop)
		first := &budgetConcurrentProvider{entered: make(chan struct{}), release: release, usage: knownUsage}
		second := &budgetConcurrentProvider{usage: knownUsage}
		r1 := call(ctx, wrap(t, b, first))
		budgetAwaitSignal(t, first.entered)
		waiter := &budgetWaitingContext{Context: ctx, observed: make(chan struct{})}
		r2 := call(waiter, wrap(t, b, second))
		budgetAwaitSignal(t, waiter.observed)
		if _, err := installer.ExecContext(ctx, "UPDATE llm_profiles SET max_tokens=129 WHERE id=$1", profile); err != nil {
			t.Fatal("합성 결속 변경 실패")
		}
		defer func() {
			if _, err := installer.ExecContext(f.Context, "UPDATE llm_profiles SET max_tokens=128 WHERE id=$1", profile); err != nil {
				t.Error("합성 결속 복원 실패")
			}
		}()
		close(release)
		if budgetAwaitResult(t, r1) != nil || !errors.Is(budgetAwaitResult(t, r2), ErrFirstDiagnosisModel) || second.calls.Load() != 0 || b.Snapshot().Requests != 1 {
			t.Fatal("대기 중 바뀐 모델 결속이 허용됨")
		}
	})
	for _, mode := range []string{"revoke", "expiry", "user_stop", "audit_after_target_stop", "grace_timeout", "provider_error"} {
		t.Run("target_stop_"+mode, func(t *testing.T) {
			hard, hardStop := context.WithTimeout(f.Context, 5*time.Second)
			defer hardStop()
			ctx, stop := context.WithCancelCause(hard)
			defer stop(context.Canceled)
			var closed atomic.Bool
			var persisted []diagnosisprep.ModelUsage
			b := newBudget(t, 2, func(u diagnosisprep.ModelUsage) error { persisted = append(persisted, u); return nil })
			grace := time.Second
			if mode == "grace_timeout" {
				grace = 80 * time.Millisecond
			}
			if b.ConfigureTargetDrain(hard, grace, func() error {
				if closed.Load() {
					return ErrFirstDiagnosisTargetEnded
				}
				return nil
			}) != nil {
				t.Fatal("target drain setup")
			}
			release := make(chan struct{})
			first := &budgetConcurrentProvider{entered: make(chan struct{}), release: release, usage: knownUsage}
			if mode == "provider_error" {
				first.err = errors.New("owned provider error")
			}
			second := &budgetConcurrentProvider{usage: knownUsage}
			r1 := call(ctx, wrap(t, b, first))
			budgetAwaitSignal(t, first.entered)
			waiter := &budgetWaitingContext{Context: ctx, observed: make(chan struct{})}
			r2 := call(waiter, wrap(t, b, second))
			budgetAwaitSignal(t, waiter.observed)
			closed.Store(true)
			if mode == "user_stop" {
				stop(context.Canceled)
			} else {
				stop(ErrFirstDiagnosisTargetEnded)
			}
			if budgetAwaitResult(t, r2) == nil || second.calls.Load() != 0 {
				t.Fatal("new call after target stop")
			}
			// 대상 철회(revoke·expiry)는 진행 중 호출을 마치게 둔다. 중지·hard 정지·제공자 오류로 끝난 호출은
			// 실패지만 예약한 양으로 정산한다(P3 결정안 C05).
			known := mode == "revoke" || mode == "expiry"
			if known || mode == "provider_error" {
				budgetRequireWaiting(t, r1, second)
				close(release)
			} else if mode == "audit_after_target_stop" {
				hardStop()
			}
			err := budgetAwaitResult(t, r1)
			if (err == nil) != known {
				t.Fatal("incorrect drain result", err)
			}
			usage := b.Snapshot()
			bounded := int64(1)
			if known {
				bounded = 0
			}
			if !usage.UsageKnown || usage.UnknownUsageCalls != bounded || usage.Requests != 1 || usage.ReservedInputTokens != reserve || usage.ReservedOutputTokens != 128 || first.calls.Load() != 1 || len(persisted) != 2 || !persisted[1].UsageKnown {
				t.Fatal("usage not drained or reservation refunded", usage)
			}
		})
	}
	t.Run("target_admission_closed_before_context_watcher", func(t *testing.T) {
		hard, cancel := context.WithTimeout(f.Context, time.Second)
		defer cancel()
		b := newBudget(t, 2, noop)
		if b.ConfigureTargetDrain(hard, time.Second, func() error { return ErrFirstDiagnosisTargetEnded }) != nil {
			t.Fatal("target drain setup")
		}
		inner := &budgetConcurrentProvider{usage: knownUsage}
		if budgetAwaitResult(t, call(hard, wrap(t, b, inner))) == nil || inner.calls.Load() != 0 || b.Snapshot().Requests != 0 || !b.Snapshot().UsageKnown {
			t.Fatal("closed admission consumed a call")
		}
	})

}

// 실제 wrapper 테스트와 별도로 초기화되지 않은 gate도 무한 대기 없이 닫는다.
func TestFirstDiagnosisModelBudgetUninitializedGateFailsClosed(t *testing.T) {
	for _, b := range []*FirstDiagnosisModelBudget{nil, {}} {
		if release, err := b.acquireCall(context.Background()); err == nil || release != nil {
			t.Fatal("초기화되지 않은 예산 gate 허용")
		}
	}
	b, _ := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
	if release, err := b.acquireCall(nil); err == nil || release != nil {
		t.Fatal("nil context 허용")
	}
}
