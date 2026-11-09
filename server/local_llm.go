package server

import (
	"context"
	"errors"
	"iter"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

var (
	ErrLocalLLMPolicy  = errors.New("server: local LLM policy unavailable")
	ErrLocalLLMProfile = errors.New("server: local LLM profile unavailable")
)

// NewWithLocalLLM loads the operator manifest before starting the server. A bad
// or missing file aborts startup; it never selects the legacy provider path.
// The policy is fixed for this process lifetime. Changes require a restart.
func NewWithLocalLLM(ctx context.Context, m *Manager, skillDir, dataDir, keyDir, manifestPath string) (*Server, error) {
	resolver, err := llmpolicy.LoadProfileManifestFile(manifestPath)
	if err != nil {
		return nil, ErrLocalLLMPolicy
	}
	return newServer(ctx, m, skillDir, dataDir, keyDir, resolver), nil
}

// newProfileProvider carries the same effective Config snapshot from policy
// resolution into the guarded provider factory. The selected profile ID comes
// from the trusted DB lookup, never from a model or the manifest itself.
func (s *Server) newProfileProvider(id int64, cfg agent.Config) (llm.Provider, error) {
	if s.firstModelBudget != nil && s.firstModelBudget.checkConfig(cfg) != nil {
		return nil, ErrFirstDiagnosisModelBudget
	}
	if s.openRouterPolicy != nil {
		policy, err := s.openRouterPolicy.Resolve(llmpolicy.ProfileTarget{ID: id, Format: string(cfg.Format), Model: cfg.Model, BaseURL: cfg.BaseURL})
		if err != nil {
			return nil, ErrFirstDiagnosisModel
		}
		return cfg.NewOpenRouterProvider(policy)
	}
	if s.localResolver == nil {
		return cfg.NewProvider()
	}
	policy, err := s.localResolver.Resolve(llmpolicy.ProfileTarget{
		ID: id, Format: string(cfg.Format), Model: cfg.Model, BaseURL: cfg.BaseURL,
	})
	if err != nil {
		return nil, ErrLocalLLMProfile
	}
	provider, err := cfg.NewLocalProvider(policy, llmpolicy.ClientOptions{DisableRedirects: s.firstModelBudget != nil})
	if err != nil {
		return nil, ErrLocalLLMProfile
	}
	return provider, nil
}

// blockedLocalProvider preserves an explicit binding's precedence when its
// local policy fails. Returning ok=false there would silently pick a different
// task/global provider. No request from this provider reaches a network client.
type blockedLocalProvider struct{}

func (blockedLocalProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, ErrLocalLLMProfile)
	}
}

func (blockedLocalProvider) Complete(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return llm.Message{}, "", llm.Usage{}, ErrLocalLLMProfile
}

func (s *Server) clearLocalLLM() {
	if !s.hasModelPolicy() {
		return
	}
	s.cfgMu.Lock()
	s.chatAgent = nil
	s.llmProv = nil
	s.llmDirect = nil
	s.llmCfg = agent.Config{}
	s.llmOn = false
	s.llmProf = ""
	s.cfgMu.Unlock()
	s.invalidateProfileAgents()
}
