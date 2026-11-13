package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/norma/llm"
)

// 보내기 전에 막힌 대화 기록은 transport 기록이 없으므로 provider가 사유를 남긴다. 거부한 내용은 남기지 않는다.
func TestOpenRouterHistoryRejectionDiagnosticEvent(t *testing.T) {
	log, err := diagnosticlog.Create(t.TempDir(), "run", 42, "synthetic-run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	ctx := diagnosticlog.WithRole(diagnosticlog.With(context.Background(), log), "planner")
	calls := 0
	c := &http.Client{Transport: openRouterSDKRT(func(*http.Request) (*http.Response, error) { calls++; return nil, http.ErrHandlerTimeout })}
	provider, err := openRouterSDKConfig().newOpenRouterProviderWithClient(c)
	if err != nil {
		t.Fatal(err)
	}
	req := llm.CompletionRequest{MaxTokens: 10, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "private-history-marker"}}}}}
	if _, _, _, err := provider.Complete(ctx, req); err != ErrOpenRouterProvider || calls != 0 {
		t.Fatalf("invalid history reached the client err=%v calls=%d", err, calls)
	}
	if err := log.Close(); err != nil || log.Incomplete() {
		t.Fatal("history rejection broke the diagnostic logger", err)
	}
	f, err := os.Open(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	found := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "private-history-marker") {
			t.Fatal("rejected history content persisted")
		}
		var e diagnosticlog.Event
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			t.Fatal("invalid event JSON")
		}
		found = found || e.Component == "openrouter" && e.Event == "model_request_denied" && e.ReasonCode == "history_invalid" && e.Role == "planner"
	}
	if !found {
		t.Fatal("history rejection not recorded")
	}
}
