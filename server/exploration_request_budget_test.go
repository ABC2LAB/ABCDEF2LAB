package server

import (
	"context"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/exploration"
)

// 탐색+진단 회차 요청 예산(P1-E) 시험이다. 같은 회차에서 탐색 대상 송신 E와 진단 대상 송신 D의 합이 승인 수 N을
// 넘지 않도록, 탐색 몫을 경계가 확정한 값으로만 받고 모르면 진단 예산을 열지 않는다.

const (
	// 경계가 마감 때 확정했다고 돌려주는 대상 전달 수다. 세션 fixture의 요청 상한(10) 안이다.
	budgetFixtureExplorationRequests = 3
	// 회차 승인 수 N이다. 위 탐색 몫을 빼면 진단 몫 1이 남는다.
	budgetFixtureRunRequests = 4
)

type openWithErrorBoundary struct{ inner exploration.Boundary }

func (b openWithErrorBoundary) Open(ctx context.Context, binding exploration.Binding) (exploration.Lease, error) {
	lease, err := b.inner.Open(ctx, binding)
	if err != nil {
		return lease, err
	}
	return lease, exploration.ErrUnavailable
}

func TestExplorationPipelineReportsOnlyBoundaryConfirmedCharge(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*ExplorationPipelineOptions, *explorationFixtureBoundary)
		want  ExplorationCharge
	}{
		{name: "confirmed", setup: func(_ *ExplorationPipelineOptions, b *explorationFixtureBoundary) {
			b.lease.closeReply = func(s exploration.State) exploration.State {
				s.TargetRequests = budgetFixtureExplorationRequests
				return s
			}
		}, want: ExplorationCharge{Requests: budgetFixtureExplorationRequests, Known: true}},
		{name: "close_failed", setup: func(_ *ExplorationPipelineOptions, b *explorationFixtureBoundary) {
			b.lease.closeErr = errors.New("boundary close unconfirmed")
		}, want: ExplorationCharge{}},
		{name: "still_active", setup: func(_ *ExplorationPipelineOptions, b *explorationFixtureBoundary) {
			b.lease.closeReply = func(s exploration.State) exploration.State {
				s.Active, s.ClosedReason = true, ""
				return s
			}
		}, want: ExplorationCharge{}},
		{name: "no_host", setup: func(o *ExplorationPipelineOptions, _ *explorationFixtureBoundary) {
			o.OpenHost = nil
		}, want: ExplorationCharge{Known: true}},
		{name: "open_refused", setup: func(o *ExplorationPipelineOptions, _ *explorationFixtureBoundary) {
			original := o.OpenHost
			o.OpenHost = func(ctx context.Context, b exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
				host, err := original(ctx, b, w)
				host.Session.Boundary = &pipelineFailingOpenBoundary{}
				return host, err
			}
		}, want: ExplorationCharge{Known: true}},
		{name: "open_lease_with_error", setup: func(o *ExplorationPipelineOptions, b *explorationFixtureBoundary) {
			b.lease.closeReply = func(s exploration.State) exploration.State {
				s.TargetRequests = 1
				return s
			}
			original := o.OpenHost
			o.OpenHost = func(ctx context.Context, binding exploration.Binding, w *agent.Worker) (ExplorationPipelineHost, error) {
				host, err := original(ctx, binding, w)
				host.Session.Boundary = openWithErrorBoundary{inner: host.Session.Boundary}
				return host, err
			}
		}, want: ExplorationCharge{Requests: 1, Known: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, task, o, b, _, _, _ := pipelineFixture(t)
			tc.setup(&o, b)
			result, _ := server.RunExplorationPipeline(t.Context(), task, o)
			if result.Charge != tc.want {
				t.Fatalf("charge=%+v want %+v", result.Charge, tc.want)
			}
		})
	}
}

func TestE2HTTPRuntimeExplorationChargeGatesDiagnosisBudget(t *testing.T) {
	cases := []struct {
		name        string
		charge      ExplorationCharge
		runRequests uint64
	}{
		{name: "exhausted", charge: ExplorationCharge{Requests: budgetFixtureRunRequests, Known: true}, runRequests: budgetFixtureRunRequests},
		{name: "unknown", charge: ExplorationCharge{}, runRequests: budgetFixtureRunRequests},
		{name: "unknown_run_budget", charge: ExplorationCharge{Requests: 1, Known: true}, runRequests: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// fixture의 Acquire는 불리면 시험을 실패시킨다. 진단 자원이 만들어지지 않아야 한다.
			runtime, task := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) {
				o.RunRequests = tc.runRequests
				o.Exploration = func(context.Context, *Task) (ExplorationCharge, error) { return tc.charge, nil }
			})
			if err := runtime.AdmitTask(t.Context(), task); err != nil {
				t.Fatal(err)
			}
			_, finish, err := runtime.PrepareWork(t.Context(), task, &db.Node{ID: 19, Kind: db.KindIntent})
			if !errors.Is(err, ErrE2HTTPBudgetLimited) || finish != nil || !task.firstDiagnosisBudgetLimited.Load() || runtime.task.lease != nil {
				t.Fatal("exploration charge did not close the diagnosis budget", err)
			}
		})
	}
	// 양성: 탐색 몫이 확정되고 N이 남으면 그 몫을 공급자에 넘긴다. 탐색 단계가 없으면 기존 예산 그대로다.
	runtime, _ := e2HTTPRuntimeFixture(t, func(o *E2HTTPRuntimeOptions) {
		o.RunRequests = budgetFixtureRunRequests
		o.Exploration = func(context.Context, *Task) (ExplorationCharge, error) { return ExplorationCharge{}, nil }
	})
	state := &e2HTTPRuntimeTask{explorationCharge: ExplorationCharge{Requests: budgetFixtureExplorationRequests, Known: true}}
	if explored, unknown, exhausted := runtime.diagnosisAllowanceLocked(state); explored != budgetFixtureExplorationRequests || unknown || exhausted {
		t.Fatal("confirmed exploration charge was not passed through", explored, unknown, exhausted)
	}
	legacy, _ := e2HTTPRuntimeFixture(t, nil)
	if explored, unknown, exhausted := legacy.diagnosisAllowanceLocked(&e2HTTPRuntimeTask{}); explored != 0 || unknown || exhausted {
		t.Fatal("runtime without exploration stage changed the existing diagnosis budget")
	}
}

func TestE2HTTPDiagnosisRequestsNeverExceedRunBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request E2HTTPRequest
		want    uint64
		limited bool
	}{
		{name: "no_exploration", request: E2HTTPRequest{}, want: budgetFixtureRunRequests},
		{name: "partial", request: E2HTTPRequest{ExplorationRequests: budgetFixtureExplorationRequests}, want: budgetFixtureRunRequests - budgetFixtureExplorationRequests},
		{name: "exhausted", request: E2HTTPRequest{ExplorationRequests: budgetFixtureRunRequests}, limited: true},
		{name: "over", request: E2HTTPRequest{ExplorationRequests: budgetFixtureRunRequests + 1}, limited: true},
		{name: "unknown", request: E2HTTPRequest{ExplorationUnknown: true}, limited: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e2HTTPDiagnosisRequests(budgetFixtureRunRequests, tc.request)
			if tc.limited {
				if !errors.Is(err, ErrE2HTTPBudgetLimited) || got != 0 {
					t.Fatal("diagnosis budget opened past the run budget", got, err)
				}
				return
			}
			if err != nil || got != tc.want || got+tc.request.ExplorationRequests > budgetFixtureRunRequests {
				t.Fatal("diagnosis allowance", got, err)
			}
		})
	}
}

func TestE2RemoteAcquireRefusesExplorationCharge(t *testing.T) {
	client := &e2RemoteClient{}
	for _, q := range []E2HTTPRequest{{ExplorationRequests: 1}, {ExplorationUnknown: true}} {
		if _, err := client.acquire(t.Context(), q); !errors.Is(err, ErrE2HTTPHost) {
			t.Fatal("remote E2 accepted an exploration charge it cannot subtract", err)
		}
	}
}
