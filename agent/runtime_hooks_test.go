package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

func TestAugmentToolsDoesNotMixHookGenerations(t *testing.T) {
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan string, 1)
	h := RuntimeHooks{ToolAugment: func(context.Context, string) ([]actool.CoreTool, DeferredInfo, func()) {
		close(entered)
		<-resume
		return nil, DeferredInfo{}, nil
	}}
	owner := ""
	h.ToolResolve = func(_ context.Context, _ string, tools []actool.CoreTool) []actool.CoreTool {
		owner = "A"
		return tools
	}
	ctx := WithRuntimeHooks(t.Context(), h)
	go func() {
		_, _, cleanup := AugmentTools(ctx, "worker", nil)
		cleanup()
		done <- owner
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("tool augmentation did not enter the first hook")
	}
	h.ToolResolve = func(_ context.Context, _ string, tools []actool.CoreTool) []actool.CoreTool {
		owner = "B"
		return tools
	}
	// The captured context keeps the A snapshot even after h is changed.
	close(resume)
	select {
	case got := <-done:
		if got != "A" {
			t.Fatalf("tool assembly mixed the active owner A with replacement owner %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("tool augmentation remained blocked after its captured hook resumed")
	}
}

func TestRuntimeHooksConcurrentOwnerIsolationAndCleanup(t *testing.T) {
	type result struct {
		owner string
		lang  string
		body  string
		wrap  string
		guide bool
	}

	var cleanups atomic.Int32
	makeContext := func(owner, lang string, enabled bool) context.Context {
		h := RuntimeHooks{}
		h.PromptOverride = func(key string) (string, bool) {
			if key == "owner" {
				return owner, true
			}
			return owner + " prompt", true
		}
		h.OutputLanguage = func() string { return lang }
		h.WrapupOverride = func(string) (string, bool) { return owner + " wrapup", true }
		h.WrapupMaxTurnsOverride = func(string) (int, bool) { return 7, true }
		h.FindingTrafficBindingEnabled = func() bool { return enabled }
		h.ToolAugment = func(ctx context.Context, _ string) ([]actool.CoreTool, DeferredInfo, func()) {
			// Re-entry must observe the same immutable owner snapshot.
			if got, ok := RuntimeHooksFrom(ctx).PromptOverride("owner"); !ok || got != owner {
				t.Errorf("augment re-entry owner=%q ok=%v, want %q", got, ok, owner)
			}
			return nil, DeferredInfo{}, func() { cleanups.Add(1) }
		}
		h.ToolResolve = func(ctx context.Context, _ string, tools []actool.CoreTool) []actool.CoreTool {
			if got, ok := RuntimeHooksFrom(ctx).PromptOverride("owner"); !ok || got != owner {
				t.Errorf("resolve re-entry owner=%q ok=%v, want %q", got, ok, owner)
			}
			out := make([]actool.CoreTool, len(tools))
			for i, tool := range tools {
				out[i] = DecorateTool(tool, owner, tool.InputSchema())
			}
			return out
		}
		return WithRuntimeHooks(context.Background(), h)
	}

	ctxA := makeContext("A", "en", true)
	ctxB := makeContext("B", "ja", false)
	base := []actool.CoreTool{NewToolSet(nil, "owner").addFinding()}
	const rounds = 40
	results := make(chan result, rounds*2)
	var wg sync.WaitGroup
	for _, tc := range []struct {
		ctx   context.Context
		owner string
		lang  string
		guide bool
	}{{ctxA, "A", "en", true}, {ctxB, "B", "ja", false}} {
		wg.Add(1)
		go func(tc struct {
			ctx   context.Context
			owner string
			lang  string
			guide bool
		}) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				tools, def, cleanup := AugmentTools(tc.ctx, "owner", base)
				if len(tools) != 1 || !strings.HasPrefix(tools[0].Description(), tc.owner) {
					t.Errorf("round %d: tool owner=%q, want %q", i, tools[0].Description(), tc.owner)
				}
				cleanup()
				body := renderSystem(tc.ctx, "prompt", "default", struct{}{})
				st := wrapupSettlement(tc.ctx, "owner", nil)
				results <- result{owner: tc.owner, lang: resolveOutputLang(tc.ctx), body: body, wrap: st.Prompt, guide: strings.Contains(def.FindingGuidance, "traffic_refs")}
			}
		}(tc)
	}
	wg.Wait()
	close(results)
	if got := cleanups.Load(); got != rounds*2 {
		t.Fatalf("cleanup count=%d, want %d", got, rounds*2)
	}
	for got := range results {
		if got.owner == "A" {
			if got.lang != "en" || !strings.Contains(got.body, "A prompt") || !strings.Contains(got.wrap, "A wrapup") || !got.guide {
				t.Fatalf("A result mixed or lost owner state: %+v", got)
			}
		} else if got.lang != "ja" || !strings.Contains(got.body, "B prompt") || !strings.Contains(got.wrap, "B wrapup") || got.guide {
			t.Fatalf("B result mixed or lost owner state: %+v", got)
		}
	}
}

func TestRuntimeHooksValueCopyAndZeroDefault(t *testing.T) {
	var h RuntimeHooks
	h.PromptOverride = func(string) (string, bool) { return "original", true }
	ctx := WithRuntimeHooks(context.Background(), h)
	h.PromptOverride = func(string) (string, bool) { return "mutated", true }
	if got := renderSystem(ctx, "owner", "default", struct{}{}); got != "original" {
		t.Fatalf("context hook changed after binding: %q", got)
	}
	if got := RuntimeHooksFrom(context.Background()); got.PromptOverride != nil {
		t.Fatal("missing context returned a mutable/default hook")
	}
	if got := resolveOutputLang(context.Background()); got != defaultOutputLang {
		t.Fatalf("missing context language=%q, want %q", got, defaultOutputLang)
	}
	if got := wrapupSettlement(context.Background(), "owner", nil).MaxTurns; got != WrapupTurnsDefault("owner") {
		t.Fatalf("missing context wrapup turns=%d, want %d", got, WrapupTurnsDefault("owner"))
	}
}
