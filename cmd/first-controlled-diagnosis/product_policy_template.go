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
	// 프록시 연결 수가 동시 송신 수보다 작으면 동시 송신 상한을 쓸 수 없다(C32 결정안 §8).
	if p.Proxy.MaxConnections < p.Limits.MaxConcurrent {
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
	cfg := e2proxy.Config{ApprovalMode: p.ApprovalMode, HeaderMode: p.HeaderMode, Rules: []e2proxy.Rule{{Ref: "template", Origin: "https://template.invalid", ConnectIP: netip.MustParseAddr("192.0.2.1"), PathPrefix: "/", PathScopeRef: "template", Methods: []string{"GET"}}}, Limits: e2proxy.Limits{MaxAttempts: p.Limits.MaxAttempts, MaxCalls: p.Limits.MaxCalls, MaxConcurrent: p.Limits.MaxConcurrent, MaxBodyBytes: p.Limits.MaxBodyBytes, MaxResponseBodyBytes: p.Limits.MaxResponseBodyBytes, MaxResponseScanBytes: p.Limits.MaxResponseScanBytes, RequestTimeout: requestTimeout, CallTimeout: callTimeout, MaxDenials: p.Limits.MaxDenials, MaxTransient: p.Limits.MaxTransient, MaxUnknown: p.Limits.MaxUnknown}}
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
	// 회차 하나의 실행 시간(초). launch 예산 DurationSeconds로 들어가 설치 기록과 회차 승인 범위에 남는다.
	RunSeconds *int64 `json:"run_seconds,omitempty"`
	// 동시 모델 호출 수. launch 설정에 들어간다. OpenRouter는 승인 max_concurrent 이하, 로컬 모델은 1만 받는다.
	ModelConcurrency *int `json:"model_concurrency,omitempty"`
	// 진단 Planner 한 번(Plan)의 턴 수·시간(초)(C26). launch 설정에 들어간다. 1 이상, Worker와 같은 코드 상한 이하.
	PlannerTurnsPerPlan   *int   `json:"planner_turns_per_plan,omitempty"`
	PlannerSecondsPerPlan *int64 `json:"planner_seconds_per_plan,omitempty"`
	// 실행 상태 조회 연속 실패를 다시 묻는 횟수(C10). launch 설정에 들어간다. 0 이상, 정리 시간 상한 안.
	StatusReadRetries *int `json:"status_read_retries,omitempty"`
	// 응답 전달 상한·검사 상한(바이트, C16 결정안 3). E② 정책 파일(정책 지문)에 들어간다. 검사 상한은 응답 상한 + 가장 긴
	// 보호 값 길이 − 1 이상이어야 하고 E② 정책 검사가 거부한다.
	MaxResponseBodyBytes *int64 `json:"max_response_body_bytes,omitempty"`
	MaxResponseScanBytes *int64 `json:"max_response_scan_bytes,omitempty"`
}

// productShutdownTimeoutSeconds는 제품 launch의 정리 시간 상한(초)이다.
const productShutdownTimeoutSeconds = 20

// productStatusReadRetriesFor는 고른 모드의 상태 조회 재시도 횟수다. 칸이 없으면 0(지금처럼 첫 실패에서 멈춤)이고,
// 있으면 0 이상 urlMaxStatusReadRetries(제품 정리 시간 상한) 이하만 받는다(범위 밖이면 설치 거부).
func productStatusReadRetriesFor(limits *productRuntimeLimits, workflow bool) (int, bool) {
	retries := 0
	if set := limits.mode(workflow); set != nil && set.StatusReadRetries != nil {
		retries = *set.StatusReadRetries
	}
	return retries, retries >= 0 && retries <= urlMaxStatusReadRetries(productShutdownTimeoutSeconds)
}

// productPlannerLimitsFor는 고른 모드의 Plan당 상한이다. 칸이 없으면 0(agent 기본값 16턴·120초)이고, 있으면 1 이상
// agent.E2HTTPPlannerLimits의 코드 상한 이하만 받는다(범위 밖이면 설치 거부). 지금 기본값은 바꾸지 않는다.
func productPlannerLimitsFor(limits *productRuntimeLimits, workflow bool) (launchConfig, bool) {
	var out launchConfig
	if set := limits.mode(workflow); set != nil {
		if set.PlannerTurnsPerPlan != nil {
			if *set.PlannerTurnsPerPlan < 1 {
				return launchConfig{}, false
			}
			out.PlannerTurnsPerPlan = *set.PlannerTurnsPerPlan
		}
		if set.PlannerSecondsPerPlan != nil {
			if *set.PlannerSecondsPerPlan < 1 {
				return launchConfig{}, false
			}
			out.PlannerSecondsPerPlan = *set.PlannerSecondsPerPlan
		}
	}
	if _, ok := out.plannerLimits(); !ok {
		return launchConfig{}, false
	}
	return out, true
}

// productModelConcurrencyFor는 고른 모드의 동시 모델 호출 수다. 없으면 지금과 같은 1이다. 로컬 모델이 설치에 있으면
// 1만 받고(코드에 로컬 동시 호출 상한이 없음, 설치 결정안 2-4), OpenRouter만이면 승인 max_concurrent 이하만 받는다.
func productModelConcurrencyFor(limits *productRuntimeLimits, workflow, local bool, openRouterMax int64) (int, bool) {
	n := 1
	if set := limits.mode(workflow); set != nil && set.ModelConcurrency != nil {
		n = *set.ModelConcurrency
	}
	if n < 1 || local && n != 1 || openRouterMax > 0 && int64(n) > openRouterMax {
		return 0, false
	}
	return n, true
}

// 회차 실행 시간 기본값(초). 워크플로 기본값은 2026-11-14 점검 회차 실측으로 600 → 1800초로 올렸다: 600초 회차 둘이
// 모델 승인 요청(150)의 절반도 안 쓴 채(66·61번) 시간 상한에 닿았고, 그 속도(600초에 약 60번)면 1800초에서 모델 예산과
// 비슷하게 닿는다(docs/install-limits-decision.md §6). 제품(일반 HTTP) 기본값도 같은 값으로 올렸다(§8, 측정 전 기본값
// 고정 원칙은 §7에서 철회).
const (
	productRunSeconds  = 1800
	workflowRunSeconds = 1800
)

// productRunSecondsFor는 고른 모드의 회차 실행 시간이다. runtime_limits에 없으면 기본값, 있으면 1 이상
// urlMaxRunSeconds 이하만 받는다(범위 밖이면 설치 거부, 잘라서 받지 않는다).
func productRunSecondsFor(limits *productRuntimeLimits, workflow bool) (int64, bool) {
	seconds := int64(productRunSeconds)
	if workflow {
		seconds = workflowRunSeconds
	}
	if set := limits.mode(workflow); set != nil && set.RunSeconds != nil {
		seconds = *set.RunSeconds
	}
	return seconds, seconds > 0 && seconds <= urlMaxRunSeconds
}

// mode는 고른 모드(제품·워크플로)의 칸 묶음이다. 블록이나 그 모드가 없으면 nil이다.
func (l *productRuntimeLimits) mode(workflow bool) *productRuntimeLimitSet {
	switch {
	case l == nil:
		return nil
	case workflow:
		return l.Workflow
	default:
		return l.Product
	}
}

// applyRuntimeLimits는 고른 모드의 칸을 정책에 덮어쓴다. 0 이하는 거부한다. 덮어쓴 정책은 호출자가
// validProductPolicyTemplate으로 다시 검사한다(범위 밖이면 설치 거부, 잘라서 받지 않는다).
func applyRuntimeLimits(policy *server.E2HTTPFilePolicy, limits *productRuntimeLimits, workflow bool) bool {
	set := limits.mode(workflow)
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
	if v := set.MaxResponseBodyBytes; v != nil {
		positive = positive && *v > 0
		// 워크플로 모드의 본문 상한은 대상 정책의 max_response_bytes(설치 조직 target_policy 상한)다.
		// 실행 상한 칸으로 그보다 크게 받지 않는다.
		if workflow {
			positive = positive && *v <= policy.Limits.MaxBodyBytes
		}
		policy.Limits.MaxResponseBodyBytes = *v
	}
	if v := set.MaxResponseScanBytes; v != nil {
		positive = positive && *v > 0
		policy.Limits.MaxResponseScanBytes = *v
	}
	return positive
}
