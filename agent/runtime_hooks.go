package agent

import (
	"context"

	actool "github.com/Autumn-27/norma/tool"
)

// RuntimeHooks contains the request-scoped runtime integrations used by agents.
// It is intentionally a value type: each run captures one immutable snapshot.
type RuntimeHooks struct {
	ToolAugment                    func(context.Context, string) ([]actool.CoreTool, DeferredInfo, func())
	ToolResolve                    func(context.Context, string, []actool.CoreTool) []actool.CoreTool
	PromptOverride                 func(string) (string, bool)
	OutputLanguage                 func() string
	WrapupOverride                 func(string) (string, bool)
	WrapupMaxTurnsOverride         func(string) (int, bool)
	WrapupTaskTimeoutOverride      func(string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(string) (int, bool)
	FindingTrafficBindingEnabled   func() bool
}

type runtimeHooksKey struct{}

func WithRuntimeHooks(ctx context.Context, h RuntimeHooks) context.Context {
	return context.WithValue(ctx, runtimeHooksKey{}, h)
}

func RuntimeHooksFrom(ctx context.Context) RuntimeHooks {
	if ctx == nil {
		return RuntimeHooks{}
	}
	h, _ := ctx.Value(runtimeHooksKey{}).(RuntimeHooks)
	return h
}
