package server

import (
	"context"
	"time"
)

const (
	// 엔진은 대상 컨테이너 ID를 알 수 없다. 값 검사는 ResetBoundRemotely가 대신한다.
	e2RemoteResetPlaceholder = "bound-by-e2"
	e2RemoteDescribePoll     = 200 * time.Millisecond
	e2RemoteClosePoll        = time.Second
)

// E2RemoteEngineOptions는 엔진이 eval-e2를 부르는 데 필요한 값이다.
type E2RemoteEngineOptions struct {
	URL             string        `json:"-"`
	TokenFile       string        `json:"-"`
	RequestTimeout  time.Duration `json:"-"`
	DescribeTimeout time.Duration `json:"-"`
}

func (E2RemoteEngineOptions) String() string   { return "server.E2RemoteEngineOptions[protected]" }
func (E2RemoteEngineOptions) GoString() string { return "server.E2RemoteEngineOptions[protected]" }

// NewWithRemoteE2는 실행 자원을 eval-e2가 소유하는 엔진을 만든다. 정책과 한도도 eval-e2에서 받는다.
func NewWithRemoteE2(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, o E2RemoteEngineOptions) (*Server, error) {
	runtime, err := newE2RemoteRuntime(ctx, o)
	if err != nil {
		return nil, err
	}
	return newServerConfiguredWithOptions(ctx, m, skillDir, dataDir, keyDir, nil, serverStartupOptions{e2HTTP: runtime}, nil)
}

func newE2RemoteRuntime(ctx context.Context, o E2RemoteEngineOptions) (*E2HTTPRuntime, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrE2Control
	}
	client, err := newE2RemoteClient(o.URL, o.TokenFile, o.RequestTimeout)
	if err != nil {
		return nil, err
	}
	described, err := waitE2RemoteDescribe(ctx, client, o.DescribeTimeout)
	if err != nil {
		return nil, err
	}
	runtime, err := NewE2HTTPRuntime(ctx, E2HTTPRuntimeOptions{
		PromptProjection:        true,
		PolicyFingerprintSHA256: described.PolicyFingerprintSHA256,
		PolicyConfigSHA256:      described.PolicyConfigSHA256,
		HeaderMode:              described.HeaderMode,
		Evaluation:              described.Evaluation,
		AuthorityContext:        ctx,
		CheckAuthority:          func(check context.Context) error { _, err := client.describe(check); return err },
		AuthorizeIntent:         client.authorize,
		Expected: E2HTTPExpectedBinding{RunRef: described.RunRef, ResetRef: e2RemoteResetPlaceholder, PolicyRef: described.PolicyRef, CaseRef: described.CaseRef,
			PolicyRevision: described.PolicyRevision, BudgetRef: described.BudgetRef},
		ResetBoundRemotely: true,
		Acquire:            client.acquire,
		MaxWorkerTurns:     described.MaxWorkerTurns,
		MaxToolCalls:       described.MaxToolCalls,
		WorkerTimeout:      time.Duration(described.WorkerTimeoutMS) * time.Millisecond,
		MaxPlannerRounds:   described.MaxPlannerRounds,
		MaxIntentRuns:      described.MaxIntentRuns,
		DrainTimeout:       time.Duration(described.DrainTimeoutMS) * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	go runtime.watchRemoteClose(runtime.ctx)
	return runtime, nil
}

// 러너는 eval-e2만 닫고 엔진에는 알리지 않는다. 엔진이 task를 마감해야 프롬프트 관측이 봉인되므로
// 원격 실행이 끝난 것을 보면 직접 마감한다.
func (r *E2HTTPRuntime) watchRemoteClose(ctx context.Context) {
	ticker := time.NewTicker(e2RemoteClosePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if r.closeIfRemoteStopped(ctx) {
			return
		}
	}
}

// 조회 실패는 실행이 끝났다는 근거가 아니다. 그때 마감하면 일시 장애가 회차를 끝낸다.
func (r *E2HTTPRuntime) closeIfRemoteStopped(ctx context.Context) bool {
	r.mu.Lock()
	t := r.task
	r.mu.Unlock()
	if t == nil {
		return false
	}
	t.mu.Lock()
	lease := t.lease
	t.mu.Unlock()
	if lease == nil || lease.Runner == nil {
		return false
	}
	if state := lease.Runner.Snapshot().State; state == "active" || state == e2RemoteViewUnavailable {
		return false
	}
	closing, cancel := context.WithTimeout(ctx, r.options.DrainTimeout)
	defer cancel()
	return r.CloseTask(closing, t.id) == nil
}

// eval-e2가 엔진과 동시에 뜨므로 응답할 때까지 시한 안에서 다시 묻는다.
func waitE2RemoteDescribe(parent context.Context, client *e2RemoteClient, limit time.Duration) (e2RemoteDescribe, error) {
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	for {
		described, err := client.describe(ctx)
		if err == nil {
			return described, nil
		}
		select {
		case <-ctx.Done():
			return e2RemoteDescribe{}, ErrE2Control
		case <-time.After(e2RemoteDescribePoll):
		}
	}
}
