package main

import (
	"github.com/Autumn-27/artex/e2export"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
	"math"
	"net/netip"
	"time"
)

// productMillis는 정책의 밀리초 값을 Duration으로 바꾼다. 곱셈이 넘치는 값은 범위 밖으로 거부한다. 넘친 값이
// 짧은 시간으로 돌아 상한 검사를 통과하면 원문 값과 다른 시간으로 실행되기 때문이다.
func productMillis(ms int64) (time.Duration, bool) {
	if ms <= 0 || ms > math.MaxInt64/int64(time.Millisecond) {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

// Validate the cross-package runtime/encoder bounds at installation, before any model
// completion or task authority. The synthetic rule is data only; this performs no DNS or I/O.
func validProductPolicyTemplate(p server.E2HTTPFilePolicy) bool {
	if p.Limits.RequestTimeoutMS > 60000 {
		return false
	}
	requestTimeout, ok1 := productMillis(p.Limits.RequestTimeoutMS)
	callTimeout, ok2 := productMillis(p.Limits.CallTimeoutMS)
	workerTimeout, ok3 := productMillis(p.Runtime.WorkerTimeoutMS)
	drainTimeout, ok4 := productMillis(p.Runtime.DrainTimeoutMS)
	headerTimeout, ok5 := productMillis(p.Proxy.HeaderTimeoutMS)
	proxyTimeout, ok6 := productMillis(p.Proxy.RequestTimeoutMS)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return false
	}
	cfg := e2proxy.Config{ApprovalMode: p.ApprovalMode, HeaderMode: p.HeaderMode, Rules: []e2proxy.Rule{{Ref: "template", Origin: "https://template.invalid", ConnectIP: netip.MustParseAddr("192.0.2.1"), PathPrefix: "/", PathScopeRef: "template", Methods: []string{"GET"}}}, Limits: e2proxy.Limits{MaxAttempts: p.Limits.MaxAttempts, MaxCalls: p.Limits.MaxCalls, MaxConcurrent: p.Limits.MaxConcurrent, MaxBodyBytes: p.Limits.MaxBodyBytes, RequestTimeout: requestTimeout, CallTimeout: callTimeout, MaxDenials: p.Limits.MaxDenials, MaxTransient: p.Limits.MaxTransient, MaxUnknown: p.Limits.MaxUnknown}}
	_, err := e2export.PolicyFingerprint(cfg, p.MaxRequests, e2export.PolicyBehavior{MaxWorkerTurns: p.Runtime.MaxWorkerTurns, MaxToolCalls: p.Runtime.MaxToolCalls, WorkerTimeout: workerTimeout, MaxPlannerRounds: p.Runtime.MaxPlannerRounds, MaxIntentRuns: p.Runtime.MaxIntentRuns, DrainTimeout: drainTimeout, OutputLimit: p.Runtime.OutputLimit, ProxyHeaderTimeout: headerTimeout, ProxyRequestTimeout: proxyTimeout, ProxyMaxHeaderBytes: p.Proxy.MaxHeaderBytes, ProxyMaxConnections: p.Proxy.MaxConnections})
	return err == nil
}

// productRuntimeLimits는 설치 입력의 선택 블록 runtime_limits다(docs/install-limits-decision.md, C32). 제품 모드와
// 워크플로 모드에 각각 줄 수 있고, 비어 있는 칸은 지금 설치 기본값 그대로다. 상한은 새로 정하지 않고
// validProductPolicyTemplate(E② 한도 검사와 e2export.PolicyFingerprint의 런타임 상한)이 그대로 검사한다.
type productRuntimeLimits struct {
	Product  *productRuntimeLimitSet `json:"product,omitempty"`
	Workflow *productRuntimeLimitSet `json:"workflow,omitempty"`
}

type productRuntimeLimitSet struct {
	// E② 요청 시간. 제품 모드는 프록시 요청 시간도 같은 값으로 바꾼다. 워크플로 모드는 프록시가 운영자 확인을
	// 기다리는 동안 요청을 쥐고 있어야 해서 프록시 요청 시간을 워크플로 값 그대로 둔다.
	RequestTimeoutMS *int64 `json:"request_timeout_ms,omitempty"`
	CallTimeoutMS    *int64 `json:"call_timeout_ms,omitempty"`
	// E② 호출 상한과 Worker 도구 호출 상한을 같은 값으로.
	MaxCalls        *int    `json:"max_calls,omitempty"`
	MaxAttempts     *uint64 `json:"max_attempts,omitempty"`
	WorkerTurns     *int    `json:"worker_turns,omitempty"`
	WorkerTimeoutMS *int64  `json:"worker_timeout_ms,omitempty"`
	PlannerRounds   *uint64 `json:"planner_rounds,omitempty"`
	IntentRuns      *uint64 `json:"intent_runs,omitempty"`
}

// applyRuntimeLimits는 고른 모드의 칸을 정책에 덮어쓴다. 0 이하는 거부한다. 덮어쓴 정책은 호출자가
// validProductPolicyTemplate으로 다시 검사한다(범위 밖이면 설치 거부, 잘라서 받지 않는다).
func applyRuntimeLimits(policy *server.E2HTTPFilePolicy, limits *productRuntimeLimits, workflow bool) bool {
	if limits == nil {
		return true
	}
	set := limits.Product
	if workflow {
		set = limits.Workflow
	}
	if set == nil {
		return true
	}
	positive := true
	if v := set.RequestTimeoutMS; v != nil {
		positive = positive && *v > 0
		policy.Limits.RequestTimeoutMS = *v
		if !workflow {
			policy.Proxy.RequestTimeoutMS = *v
		}
	}
	if v := set.CallTimeoutMS; v != nil {
		positive = positive && *v > 0
		policy.Limits.CallTimeoutMS = *v
	}
	if v := set.MaxCalls; v != nil {
		positive = positive && *v > 0
		policy.Limits.MaxCalls, policy.Runtime.MaxToolCalls = *v, *v
	}
	if v := set.MaxAttempts; v != nil {
		positive = positive && *v > 0
		policy.Limits.MaxAttempts = *v
	}
	if v := set.WorkerTurns; v != nil {
		positive = positive && *v > 0
		policy.Runtime.MaxWorkerTurns = *v
	}
	if v := set.WorkerTimeoutMS; v != nil {
		positive = positive && *v > 0
		policy.Runtime.WorkerTimeoutMS = *v
	}
	if v := set.PlannerRounds; v != nil {
		positive = positive && *v > 0
		policy.Runtime.MaxPlannerRounds = *v
	}
	if v := set.IntentRuns; v != nil {
		positive = positive && *v > 0
		policy.Runtime.MaxIntentRuns = *v
	}
	return positive
}
