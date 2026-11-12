package main

import (
	"fmt"
	"math"
	"testing"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/server"
)

func ptr[T any](v T) *T { return &v }

// runtime_limits가 없으면 지금 설치 기본값 그대로다(C32, 측정 전에는 기본값을 바꾸지 않는다).
func TestProductPolicyBoundsDefaultsUnchanged(t *testing.T) {
	product, ok := productPolicyBounds(productInstallInput{})
	want := server.E2HTTPFilePolicy{Limits: server.E2HTTPFileLimits{MaxAttempts: 8, MaxCalls: 8, MaxConcurrent: 1, MaxBodyBytes: 65536, RequestTimeoutMS: 5000, CallTimeoutMS: 10000, MaxDenials: 2, MaxTransient: 1, MaxUnknown: 1},
		Proxy:   server.E2HTTPFileProxy{HeaderTimeoutMS: 1000, RequestTimeoutMS: 5000, MaxHeaderBytes: 32768, MaxConnections: 4},
		Runtime: server.E2HTTPFileRuntime{MaxWorkerTurns: 8, MaxToolCalls: 8, WorkerTimeoutMS: 30000, MaxPlannerRounds: 6, MaxIntentRuns: 1, DrainTimeoutMS: 10000, OutputLimit: 32768}}
	if !ok || product.Limits != want.Limits || product.Proxy != want.Proxy || product.Runtime != want.Runtime {
		t.Fatalf("product defaults changed: %+v %+v %+v", product.Limits, product.Proxy, product.Runtime)
	}
	flow := productInstallInput{TargetPolicy: productTargetPolicy{Workflow: &diagnosisflow.Limits{MaxResponseBytes: 65536}}}
	workflow, ok := productPolicyBounds(flow)
	if !ok || workflow.Limits.MaxAttempts != 200 || workflow.Limits.MaxCalls != 64 || workflow.Limits.CallTimeoutMS != 60000 || workflow.Proxy.RequestTimeoutMS != 60000 ||
		workflow.Limits.MaxDenials != 16 || workflow.Runtime.MaxToolCalls != 64 || workflow.Runtime.MaxWorkerTurns != 16 || workflow.Runtime.WorkerTimeoutMS != 180000 ||
		workflow.Runtime.MaxPlannerRounds != 12 || workflow.Runtime.OutputLimit != 65536+8192 {
		t.Fatalf("workflow defaults changed: %+v %+v %+v", workflow.Limits, workflow.Proxy, workflow.Runtime)
	}
}

// 고른 모드의 칸만 덮어쓰고, 요청 시간은 E② 한도와 프록시에, 호출 수는 E② 호출 상한과 Worker 도구 호출 상한에 같이 들어간다.
func TestProductPolicyBoundsAppliesSelectedMode(t *testing.T) {
	limits := &productRuntimeLimits{
		Product:  &productRuntimeLimitSet{RequestTimeoutMS: ptr(int64(20000)), CallTimeoutMS: ptr(int64(40000)), MaxCalls: ptr(32), MaxAttempts: ptr(uint64(64)), WorkerTurns: ptr(20), WorkerTimeoutMS: ptr(int64(120000)), PlannerRounds: ptr(uint64(10)), IntentRuns: ptr(uint64(2))},
		Workflow: &productRuntimeLimitSet{WorkerTurns: ptr(30)},
	}
	p, ok := productPolicyBounds(productInstallInput{RuntimeLimits: limits})
	if !ok || p.Limits.RequestTimeoutMS != 20000 || p.Proxy.RequestTimeoutMS != 20000 || p.Limits.CallTimeoutMS != 40000 || p.Limits.MaxCalls != 32 || p.Runtime.MaxToolCalls != 32 ||
		p.Limits.MaxAttempts != 64 || p.Runtime.MaxWorkerTurns != 20 || p.Runtime.WorkerTimeoutMS != 120000 || p.Runtime.MaxPlannerRounds != 10 || p.Runtime.MaxIntentRuns != 2 {
		t.Fatalf("product overrides: %+v %+v %+v", p.Limits, p.Proxy, p.Runtime)
	}
	w, ok := productPolicyBounds(productInstallInput{RuntimeLimits: limits, TargetPolicy: productTargetPolicy{Workflow: &diagnosisflow.Limits{MaxResponseBytes: 65536}}})
	if !ok || w.Runtime.MaxWorkerTurns != 30 || w.Limits.RequestTimeoutMS != 5000 || w.Limits.MaxCalls != 64 {
		t.Fatalf("workflow mode took product overrides: %+v %+v", w.Limits, w.Runtime)
	}
}

// 워크플로 모드의 request_timeout_ms는 E② 요청 시간만 바꾼다. 프록시는 운영자 확인을 기다리는 동안 요청을 쥐고
// 있어야 해서 워크플로 값(60초)을 그대로 둔다(#445 재상 지적).
func TestProductPolicyBoundsWorkflowRequestTimeoutKeepsProxy(t *testing.T) {
	limits := &productRuntimeLimits{Workflow: &productRuntimeLimitSet{RequestTimeoutMS: ptr(int64(10000))}}
	w, ok := productPolicyBounds(productInstallInput{RuntimeLimits: limits, TargetPolicy: productTargetPolicy{Workflow: &diagnosisflow.Limits{MaxResponseBytes: 65536}}})
	if !ok || w.Limits.RequestTimeoutMS != 10000 || w.Proxy.RequestTimeoutMS != 60000 {
		t.Fatalf("workflow request timeout: limits=%d proxy=%d", w.Limits.RequestTimeoutMS, w.Proxy.RequestTimeoutMS)
	}
}

// 지금 코드의 검사 상한 밖이거나 0 이하이면 설치를 거부한다(잘라서 받지 않는다).
func TestProductPolicyBoundsRejectsOutOfRange(t *testing.T) {
	for name, set := range map[string]*productRuntimeLimitSet{
		"worker-turns-over":    {WorkerTurns: ptr(1001)},
		"planner-rounds-over":  {PlannerRounds: ptr(uint64(101))},
		"intent-runs-over":     {IntentRuns: ptr(uint64(11))},
		"worker-timeout-over":  {WorkerTimeoutMS: ptr(int64(3600001))},
		"request-timeout-over": {RequestTimeoutMS: ptr(int64(60001))},
		"max-calls-over":       {MaxCalls: ptr(1001)},
		"zero-turns":           {WorkerTurns: ptr(0)},
		"negative-timeout":     {RequestTimeoutMS: ptr(int64(-1))},
		"zero-rounds":          {PlannerRounds: ptr(uint64(0))},
		"zero-calls":           {MaxCalls: ptr(0)},
		// 밀리초 → Duration 곱셈이 넘쳐 짧은 시간(약 10초·60초)으로 도는 값(#445 이동찬 재현값).
		"call-timeout-overflow":   {CallTimeoutMS: ptr(int64(18446744083710))},
		"worker-timeout-overflow": {WorkerTimeoutMS: ptr(int64(18446744133710))},
		"call-timeout-max-int":    {CallTimeoutMS: ptr(int64(math.MaxInt64))},
	} {
		if _, ok := productPolicyBounds(productInstallInput{RuntimeLimits: &productRuntimeLimits{Product: set}}); ok {
			t.Fatalf("%s accepted", name)
		}
	}
}

// 설치 입력의 runtime_limits는 닫힌 형식이다. 모르는 칸·null은 거부한다.
func TestProductInstallInputRuntimeLimitsStrict(t *testing.T) {
	base := `{"format":"abc2lab.product-install/v2","runtime_limits":%s}`
	for _, block := range []string{`{"product":{"worker_turns":12}}`, `{}`, `{"workflow":{"run_seconds":300}}`} {
		var c productInstallInput
		if decodeLaunchJSON([]byte(fmt.Sprintf(base, block)), &c) != nil {
			t.Fatalf("valid block rejected: %s", block)
		}
	}
	for _, block := range []string{`{"product":{"max_workers":3}}`, `{"product":null}`, `{"other":{}}`, `{"product":{"worker_turns":"12"}}`, `{"product":{"model_requests":30}}`, `{"product":{"run_seconds":1.5}}`} {
		var c productInstallInput
		if decodeLaunchJSON([]byte(fmt.Sprintf(base, block)), &c) == nil {
			t.Fatalf("invalid block accepted: %s", block)
		}
	}
}

// run_seconds: 없으면 지금 기본값(제품 180초·워크플로 600초), 고른 모드의 칸만 쓰고, 1 이상 urlMaxRunSeconds 이하만
// 받는다(C32 2단계). model_requests는 로컬 모델 상한이 코드에 없어 아직 칸을 열지 않는다(결정안 2-4).
func TestProductRunSecondsFor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limits   *productRuntimeLimits
		workflow bool
		want     int64
		ok       bool
	}{
		{"product-default", nil, false, 180, true},
		{"workflow-default", nil, true, 600, true},
		{"product-set", &productRuntimeLimits{Product: &productRuntimeLimitSet{RunSeconds: ptr(int64(300))}}, false, 300, true},
		{"workflow-ignores-product", &productRuntimeLimits{Product: &productRuntimeLimitSet{RunSeconds: ptr(int64(300))}}, true, 600, true},
		{"workflow-set", &productRuntimeLimits{Workflow: &productRuntimeLimitSet{RunSeconds: ptr(int64(120))}}, true, 120, true},
		{"at-limit", &productRuntimeLimits{Product: &productRuntimeLimitSet{RunSeconds: ptr(int64(urlMaxRunSeconds))}}, false, urlMaxRunSeconds, true},
		{"over-limit", &productRuntimeLimits{Product: &productRuntimeLimitSet{RunSeconds: ptr(int64(urlMaxRunSeconds + 1))}}, false, 0, false},
		{"zero", &productRuntimeLimits{Workflow: &productRuntimeLimitSet{RunSeconds: ptr(int64(0))}}, true, 0, false},
		{"negative", &productRuntimeLimits{Product: &productRuntimeLimitSet{RunSeconds: ptr(int64(-1))}}, false, 0, false},
	} {
		got, ok := productRunSecondsFor(tc.limits, tc.workflow)
		if ok != tc.ok || ok && got != tc.want {
			t.Fatalf("%s: seconds=%d ok=%v", tc.name, got, ok)
		}
	}
}
