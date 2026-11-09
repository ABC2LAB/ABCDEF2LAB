package server

import (
	"context"
	"github.com/Autumn-27/artex/llmpolicy"
)

// 신뢰된 설치자 입력이다. 웹 요청·모델 출력에서 이 설정을 만들지 않는다.
type FirstDiagnosisModelOptions struct {
	Mode                   string
	LocalManifestPath      string
	OpenRouterManifestPath string
	Budget                 *FirstDiagnosisModelBudget
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
	return newServerConfiguredWithOptions(ctx, m, skillDir, dataDir, keyDir, local, serverStartupOptions{existingAuthOnly: true, noAutomaticAdmission: true, e2HTTP: runtime, openRouterPolicy: external, firstModelBudget: o.Budget}, nil)
}

// 준비 모드는 대상 실행 없이 같은 보호 모델 클라이언트로 Planner만 실행한다.
func NewFirstDiagnosisPreparationServerWithModel(ctx context.Context, m *Manager, skillDir, dataDir, keyDir string, o FirstDiagnosisModelOptions) (*Server, error) {
	if ctx == nil || ctx.Err() != nil || m == nil || m.pg == nil || CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, m.pg.DB) != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	local, external, err := o.load()
	if err != nil {
		return nil, err
	}
	return newServerConfiguredWithOptions(ctx, m, skillDir, dataDir, keyDir, local, serverStartupOptions{existingAuthOnly: true, noAutomaticAdmission: true, preparationOnly: true, openRouterPolicy: external, firstModelBudget: o.Budget}, nil)
}

// 집계 정보만 반환한다. 키·프롬프트·대상 응답 원문은 포함하지 않는다.
func (s *Server) FirstDiagnosisModelUsage() *llmpolicy.OpenRouterUsage {
	if s == nil || s.openRouterPolicy == nil {
		return nil
	}
	usage := s.openRouterPolicy.Snapshot()
	return &usage
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
