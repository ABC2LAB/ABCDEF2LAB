package server

import (
	"context"
	"time"
)

const (
	// 엔진은 대상 컨테이너 ID를 알 수 없다. 값 검사는 ResetBoundRemotely가 대신한다.
	e2RemoteResetPlaceholder = "bound-by-e2"
	e2RemoteDescribePoll     = 200 * time.Millisecond
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
	if ctx == nil || ctx.Err() != nil || o.DescribeTimeout <= 0 {
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
	return NewE2HTTPRuntime(ctx, E2HTTPRuntimeOptions{
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
