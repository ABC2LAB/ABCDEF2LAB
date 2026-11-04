package server

import "github.com/Autumn-27/artex/agent"

// buildRuntimeHooks captures this server's dependencies without publishing them
// to another server. The returned value is bound before background work starts.
func (s *Server) buildRuntimeHooks() agent.RuntimeHooks {
	var hooks agent.RuntimeHooks
	m := s.m
	if m == nil || m.pg == nil {
		return hooks
	}
	hooks.PromptOverride = func(key string) (string, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil {
			return "", false
		}
		t, err := m.pg.CurrentPrompt(a.ID)
		if err != nil || t == "" {
			return "", false
		}
		return t, true
	}
	// Wire DB-stored wrap-up (settlement) prompts. Empty column → built-in default.
	hooks.WrapupOverride = func(key string) (string, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil || a.WrapupPrompt == "" {
			return "", false
		}
		return a.WrapupPrompt, true
	}
	// Wire DB-stored wrap-up turn budgets. 0 / missing → built-in per-agent default.
	hooks.WrapupMaxTurnsOverride = func(key string) (int, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil || a.WrapupMaxTurns <= 0 {
			return 0, false
		}
		return a.WrapupMaxTurns, true
	}
	// Wire DB-stored TASK-TIMEOUT wrap-up prompt/turns (worker/planner). Empty/0 → default.
	hooks.WrapupTaskTimeoutOverride = func(key string) (string, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil || a.TaskTimeoutWrapupPrompt == "" {
			return "", false
		}
		return a.TaskTimeoutWrapupPrompt, true
	}
	hooks.WrapupTaskTimeoutTurnsOverride = func(key string) (int, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil || a.TaskTimeoutWrapupMaxTurns <= 0 {
			return 0, false
		}
		return a.TaskTimeoutWrapupMaxTurns, true
	}
	hooks.OutputLanguage = s.outputLanguage
	hooks.ToolAugment = wireAgentAugment(m.pg, s.skillDir, s.hostTools)
	hooks.ToolResolve, hooks.FindingTrafficBindingEnabled = wireTools(m.pg, buildDomainReg(m.Assets()))
	return hooks
}
