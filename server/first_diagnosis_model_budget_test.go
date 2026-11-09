package server

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/llmpolicy"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/norma/llm"
)

func modelBudgetFixture(t *testing.T, observe func(diagnosisprep.ModelUsage) error) (*FirstDiagnosisModelBudget, agent.Config) {
	t.Helper()
	b, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 2, InputTokens: 2 * 65536, OutputTokens: 256}, 65536, 128, observe)
	if err != nil {
		t.Fatal(err)
	}
	return b, agent.Config{Format: llm.FormatOpenAI, ContextWindowK: 64, MaxTokens: 128}
}
func TestFirstDiagnosisModelBudgetSharesPreparationAndRun(t *testing.T) {
	var persisted []diagnosisprep.ModelUsage
	b, cfg := modelBudgetFixture(t, func(u diagnosisprep.ModelUsage) error { persisted = append(persisted, u); return nil })
	for phase := 0; phase < 2; phase++ {
		req := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("owned synthetic input")}}
		if err := b.before(context.Background(), cfg, &req); err != nil {
			t.Fatal(err)
		}
		if persisted[len(persisted)-1].UsageKnown {
			t.Fatal("unconfirmed call reported known")
		}
		if err := b.after(llm.Usage{InputTokens: 10, OutputTokens: 20}, 128, nil); err != nil {
			t.Fatal(err)
		}
	}
	req := llm.CompletionRequest{MaxTokens: 128}
	if b.before(context.Background(), cfg, &req) == nil {
		t.Fatal("shared budget reset")
	}
	u := b.Snapshot()
	if u.Requests != 2 || u.ReservedInputTokens != 131072 || u.ReservedOutputTokens != 256 || u.InputTokens != 20 || u.OutputTokens != 40 || !u.UsageKnown {
		t.Fatalf("bad aggregate: %+v", u)
	}
}
func TestFirstDiagnosisModelBudgetUnknownStopsFurtherCalls(t *testing.T) {
	for _, usage := range []llm.Usage{{}, {InputTokens: 70000, OutputTokens: 1}, {InputTokens: 1, OutputTokens: 129}} {
		b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
		req := llm.CompletionRequest{MaxTokens: 128}
		if b.before(context.Background(), cfg, &req) != nil {
			t.Fatal("first call denied")
		}
		if b.after(usage, 128, nil) == nil || b.Snapshot().UsageKnown {
			t.Fatal("unknown usage accepted")
		}
		if b.before(context.Background(), cfg, &req) == nil {
			t.Fatal("retry after unknown")
		}
	}
}
func TestFirstDiagnosisModelBudgetReservationMustPersist(t *testing.T) {
	b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return errors.New("synthetic storage failure") })
	req := llm.CompletionRequest{MaxTokens: 128}
	if b.before(context.Background(), cfg, &req) == nil {
		t.Fatal("unpersisted reservation allowed")
	}
	if b.Snapshot().Requests != 1 || b.Snapshot().UsageKnown {
		t.Fatal("uncertain reservation lost")
	}
}
func TestFirstDiagnosisModelBudgetRejectsStreamingOrChangedContext(t *testing.T) {
	b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
	for _, change := range []func(*agent.Config){func(c *agent.Config) { c.Stream = true }, func(c *agent.Config) { c.ContextWindowK = 128 }, func(c *agent.Config) { c.MaxTokens = 0 }, func(c *agent.Config) { c.Format = llm.FormatAnthropic }} {
		changed := cfg
		change(&changed)
		req := llm.CompletionRequest{MaxTokens: 128}
		if b.before(context.Background(), changed, &req) == nil {
			t.Fatal("unbounded provider accepted")
		}
	}
	if b.Snapshot().Requests != 0 {
		t.Fatal("validation consumed a model call")
	}
}

func TestFirstDiagnosisModelBudgetRedirectRemainsUnknown(t *testing.T) {
	for _, status := range []int{307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var first, second atomic.Int64
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/second" {
					second.Add(1)
					w.WriteHeader(200)
					return
				}
				first.Add(1)
				w.Header().Set("Location", "/second")
				w.WriteHeader(status)
			}))
			defer endpoint.Close()
			b, cfg := modelBudgetFixture(t, func(diagnosisprep.ModelUsage) error { return nil })
			cfg.BaseURL = endpoint.URL
			cfg.Model = "owned-synthetic"
			cfg.Retry = agent.RetryConfig{ConnectAttempts: -1, EmptyAttempts: -1, StreamAttempts: -1}
			policy, err := llmpolicy.New([]llmpolicy.Endpoint{{Origin: endpoint.URL, Addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}})
			if err != nil {
				t.Fatal(err)
			}
			provider, err := cfg.NewLocalProvider(policy, llmpolicy.ClientOptions{DisableRedirects: true})
			if err != nil {
				t.Fatal(err)
			}
			req := llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("owned synthetic")}}
			if b.before(context.Background(), cfg, &req) != nil {
				t.Fatal("reservation")
			}
			_, _, usage, callErr := provider.Complete(context.Background(), req)
			if b.after(usage, 128, callErr) == nil || b.Snapshot().UsageKnown || b.Snapshot().Requests != 1 || first.Load() != 1 || second.Load() != 0 {
				t.Fatal("redirect escaped budget")
			}
			if b.before(context.Background(), cfg, &req) == nil {
				t.Fatal("uncertain call retried")
			}
		})
	}
}
