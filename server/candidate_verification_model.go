package server

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/norma/llm"
)

var ErrCandidateModelUnavailable = errors.New("selected candidate model unavailable")

// SelectedCandidateModel is a single installed-profile client. It has no
// target tools and uses a new budget for the selected request. The caller must
// check current ownership, scope and request state in the protected runtime.
type SelectedCandidateModel struct {
	provider llm.Provider
	config   agent.Config
	budget   *FirstDiagnosisModelBudget
}

func NewSelectedCandidateModel(ctx context.Context, pool *sql.DB, profileID int64, options FirstDiagnosisModelOptions) (*SelectedCandidateModel, error) {
	if ctx == nil || ctx.Err() != nil || pool == nil || profileID <= 0 || options.Budget == nil {
		return nil, ErrCandidateModelUnavailable
	}
	profile, err := (&db.DB{DB: pool}).ProfileByID(profileID)
	if err != nil || profile == nil || profile.APIKey == "" {
		return nil, ErrCandidateModelUnavailable
	}
	config := agent.ConfigFrom(profile.Format, profile.Model, profile.BaseURL, profile.APIKey, profile.Proxy)
	config.RatePerSecond, config.RatePerMinute = profile.RatePerSecond, profile.RatePerMinute
	config.ContextWindowK, config.MaxTokens, config.MaxTokensField = profile.ContextWindowK, profile.MaxTokens, profile.MaxTokensField
	config.ThinkingType, config.ReasoningEffort = profile.ThinkingType, profile.ReasoningEffort
	config.Stream, config.SessionHeaderKey = profile.Streaming, profile.SessionHeaderKey
	// An unknown model outcome must not be silently resent by an SDK retry.
	config.Retry = agent.RetryConfig{ConnectAttempts: -1, EmptyAttempts: -1, StreamAttempts: -1}
	if options.Budget.checkConfig(config) != nil {
		return nil, ErrCandidateModelUnavailable
	}
	target := llmpolicy.ProfileTarget{ID: profileID, Format: profile.Format, Model: profile.Model, BaseURL: profile.BaseURL}
	if CheckFirstDiagnosisModelOptions(options, target) != nil {
		return nil, ErrCandidateModelUnavailable
	}
	local, external, err := options.load()
	if err != nil {
		return nil, ErrCandidateModelUnavailable
	}
	var provider llm.Provider
	if external != nil {
		resolved, resolveErr := external.Resolve(target)
		if resolveErr == nil {
			provider, err = config.NewOpenRouterProvider(resolved)
		} else {
			err = resolveErr
		}
	} else {
		resolved, resolveErr := local.Resolve(target)
		if resolveErr == nil {
			provider, err = config.NewLocalProvider(resolved, llmpolicy.ClientOptions{DisableRedirects: true})
		} else {
			err = resolveErr
		}
	}
	if err != nil || provider == nil {
		return nil, ErrCandidateModelUnavailable
	}
	return &SelectedCandidateModel{provider: provider, config: config, budget: options.Budget}, nil
}

// Analyze uses one reservation and one provider call. A transport failure is
// recorded by the budget and returned as unknown; it is never retried here.
func (m *SelectedCandidateModel) Analyze(ctx context.Context, selected CandidateSelectedContext, admit func(context.Context) error) (CandidateAnalysisProposal, error) {
	if m == nil || m.provider == nil || m.budget == nil || admit == nil || ctx == nil || ctx.Err() != nil {
		return CandidateAnalysisProposal{}, ErrCandidateModelUnavailable
	}
	request, err := BuildCandidateAnalysisRequest(selected, min(m.config.MaxTokens, 2048))
	if err != nil {
		return CandidateAnalysisProposal{}, err
	}
	release, err := m.budget.acquireCall(ctx)
	if err != nil {
		return CandidateAnalysisProposal{}, err
	}
	defer release()
	if err := admit(ctx); err != nil {
		return CandidateAnalysisProposal{}, err
	}
	reserved, err := m.budget.reserve(ctx, m.config, &request)
	if err != nil {
		return CandidateAnalysisProposal{}, err
	}
	call, cancel := m.budget.modelCallContext(ctx)
	defer cancel()
	response, _, usage, callErr := m.provider.Complete(reserved.requestContext(call), request)
	settleErr := m.budget.settle(reserved, usage, callErr)
	if callErr != nil || settleErr != nil {
		return CandidateAnalysisProposal{}, errors.Join(ErrCandidateModelUnavailable, callErr, settleErr)
	}
	return ParseCandidateAnalysisProposal([]byte(response.Text()), selected)
}

func (m *SelectedCandidateModel) Usage() (requests, inputTokens, outputTokens, costMicros int64) {
	if m == nil || m.budget == nil {
		return
	}
	usage := m.budget.Snapshot()
	return usage.Requests, usage.InputTokens, usage.OutputTokens, usage.CostUSDMicros
}
