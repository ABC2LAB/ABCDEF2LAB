package server

import (
	"context"
	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/llmpolicy"
)

// 신뢰된 설치자 입력이다. 웹 요청·모델 출력에서 이 설정을 만들지 않는다.
type FirstDiagnosisModelOptions struct {
	Mode                   string
	LocalManifestPath      string
	OpenRouterManifestPath string
	Budget                 *FirstDiagnosisModelBudget
	// PlannerLimits는 준비 계획의 Plan당 상한이다(C26·C32). 실행 서버는 host 설정의 값을 쓴다. 0이면 기본값이다.
	PlannerLimits agent.E2HTTPPlannerLimits
	// WebSearch는 준비 Planner의 원본 검색 도구 설정이다(루프 복원 결정안 §2-1). 외부 API(openrouter) 모드에서만 켤 수 있다.
	// 실행 서버는 host 설정의 값을 쓴다.
	WebSearch agent.E2HTTPWebSearch
}

func (FirstDiagnosisModelOptions) String() string {
	return "server.FirstDiagnosisModelOptions[protected]"
}
func (o FirstDiagnosisModelOptions) GoString() string { return o.String() }

func (s *Server) hasModelPolicy() bool {
	return s != nil && (s.localResolver != nil || s.openRouterPolicy != nil)
}

// 기존 설정의 빈 mode는 로컬 manifest가 있을 때만 로컬 모드로 해석한다.
// 외부 설정이 섞였거나 mode가 알 수 없는 값이면 다른 provider로 대체하지 않는다.
func (o FirstDiagnosisModelOptions) load() (*llmpolicy.ProfileResolver, *llmpolicy.OpenRouterPolicy, error) {
	// 검색은 검색 제공자로 나가므로 외부 API 모드만 켤 수 있다. 완전 로컬 모드는 외부 송신 전면 차단이다(규칙 8).
	if !o.WebSearch.Valid() || o.WebSearch.Enabled() && o.Mode != "openrouter" {
		return nil, nil, ErrFirstDiagnosisModel
	}
	switch o.Mode {
	case "", "local_only":
		if o.LocalManifestPath == "" || o.OpenRouterManifestPath != "" {
			return nil, nil, ErrFirstDiagnosisModel
		}
		local, err := llmpolicy.LoadProfileManifestFile(o.LocalManifestPath)
		if err != nil {
			return nil, nil, ErrLocalLLMPolicy
		}
		return local, nil, nil
	case "openrouter":
		if o.LocalManifestPath != "" || o.OpenRouterManifestPath == "" {
			return nil, nil, ErrFirstDiagnosisModel
		}
		if o.Budget != nil {
			if o.Budget.external == nil || o.Budget.manifestPath != o.OpenRouterManifestPath {
				return nil, nil, ErrFirstDiagnosisModel
			}
			return nil, o.Budget.external, nil
		}
		external, err := llmpolicy.LoadOpenRouterManifestFile(o.OpenRouterManifestPath)
		if err != nil {
			return nil, nil, ErrFirstDiagnosisModel
		}
		return nil, external, nil
	default:
		return nil, nil, ErrFirstDiagnosisModel
	}
}

// 파일·프로필 결속만 검사한다. 모델 호출이나 승인 예산 소비는 없다.
func CheckFirstDiagnosisModelOptions(o FirstDiagnosisModelOptions, target llmpolicy.ProfileTarget) error {
	local, external, err := o.load()
	if err != nil {
		return err
	}
	if external != nil {
		_, err = external.Resolve(target)
	} else {
		_, err = local.Resolve(target)
	}
	if err != nil {
		return ErrFirstDiagnosisModel
	}
	return nil
}

func newFirstDiagnosisHTTPServer(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, runtime *E2HTTPRuntime, o FirstDiagnosisModelOptions) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || m.pg == nil || runtime == nil {
		return nil, ErrFirstDiagnosisModel
	}
	local, external, err := o.load()
	if err != nil {
		return nil, err
	}
	baseline := llmpolicy.OpenRouterUsage{}
	if external != nil {
		baseline = external.Snapshot()
	}
	s, err := newServerConfiguredWithOptions(ctx, m, skillDir, dataDir, keyDir, local, serverStartupOptions{existingAuthOnly: true, noAutomaticAdmission: true, e2HTTP: runtime, openRouterPolicy: external, firstModelBudget: o.Budget}, nil)
	if s != nil {
		s.openRouterBaseline = baseline
	}
	return s, err
}

// 준비 모드는 대상 실행 없이 같은 보호 모델 클라이언트로 Planner만 실행한다.
func NewFirstDiagnosisPreparationServerWithModel(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, o FirstDiagnosisModelOptions) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || m.pg == nil || CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, m.pg.DB) != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	if !o.PlannerLimits.Valid() {
		return nil, ErrControlledReadHostConfiguration
	}
	local, external, err := o.load()
	if err != nil {
		return nil, err
	}
	return newServerConfiguredWithOptions(ctx, m, skillDir, dataDir, keyDir, local, serverStartupOptions{existingAuthOnly: true, noAutomaticAdmission: true, preparationOnly: true, openRouterPolicy: external, firstModelBudget: o.Budget, firstPlannerLimits: o.PlannerLimits, firstWebSearch: o.WebSearch}, nil)
}

// 집계 정보만 반환한다. 키·프롬프트·대상 응답 원문은 포함하지 않는다.
func (s *Server) FirstDiagnosisModelUsage() *llmpolicy.OpenRouterUsage {
	if s == nil || s.openRouterPolicy == nil {
		return nil
	}
	usage := executionModelUsage(s.openRouterPolicy.Snapshot(), s.openRouterBaseline)
	return &usage
}

// executionModelUsage는 저장 회차 사용량을 실행 단계만으로 만든다. 같은 OpenRouter 정책으로 먼저 도는 준비 단계의 값(실행
// 서버를 만들 때 잡은 스냅숏)을 계수마다 뺀다. 준비 단계는 별도 불변 기록으로 남는다. 엔진 샘플링 칸을 덮어쓴 수도 같은
// 기준으로 뺀다(#646 리뷰: 빼지 않으면 준비 단계 덮어쓰기가 실행 기록에 섞여 requests보다 클 수 있다). 기준값이 null이면
// (v1·v2 승인이거나 준비 단계 스냅숏이 없음) 그대로 둔다.
func executionModelUsage(usage, baseline llmpolicy.OpenRouterUsage) llmpolicy.OpenRouterUsage {
	usage.Requests -= baseline.Requests
	usage.Errors -= baseline.Errors
	usage.ReservedInputTokens -= baseline.ReservedInputTokens
	usage.ReservedOutputTokens -= baseline.ReservedOutputTokens
	usage.ReservedCostMicros -= baseline.ReservedCostMicros
	usage.InputTokens -= baseline.InputTokens
	usage.OutputTokens -= baseline.OutputTokens
	usage.CostMicros -= baseline.CostMicros
	usage.LatencyMillis -= baseline.LatencyMillis
	if usage.SamplingOverwrites != nil && baseline.SamplingOverwrites != nil {
		// Snapshot이 돌려준 포인터는 사본이지만, 정책 쪽 계수를 건드리지 않도록 새 값으로 바꾼다.
		count := *usage.SamplingOverwrites - *baseline.SamplingOverwrites
		usage.SamplingOverwrites = &count
	}
	return usage
}
func (s *FirstDiagnosisSupervisor) ModelUsage() *llmpolicy.OpenRouterUsage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	return server.FirstDiagnosisModelUsage()
}
