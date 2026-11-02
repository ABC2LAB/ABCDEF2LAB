package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
	"github.com/google/uuid"
)

// Full chain against a real PG: Recorder → norma provider → capturing transport
// → DLP → llm_records. The model receives the original request and the consumer
// receives the original stream, while stored normalized/raw bodies are redacted.
func TestRecorderRedactsNormalizedAndRawWireBodies(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	pg := m.PG()
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatalf("ensure table: %v", err)
	}

	const secret = "dlp-server-synthetic-secret"
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_raw","usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + secret + `"}}`,
		``,
		// Tool input is another raw-only path that must not bypass storage DLP.
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_raw","name":"bash"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"` + secret + `\"}"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), secret) {
			t.Error("provider did not receive the original request")
		}
		received.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	cfg := agent.Config{Format: llm.FormatAnthropic, BaseURL: srv.URL, APIKey: "k", Model: "claude-raw-test"}
	inner, err := cfg.NewProvider()
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	session := "exp999999-dlp-" + uuid.NewString()
	profile := "raw-test-" + uuid.NewString()
	rec := llmrec.Wrap(inner, pg, cfg.Model, profile, "", "", func() bool { return true })

	// Clean up whatever this test writes, whether or not it passes. Must be a
	// defer registered after `defer m.Close()` (LIFO puts it first) — a
	// t.Cleanup would run after the manager already closed the pool.
	defer func() {
		if _, err := pg.Exec(`DELETE FROM llm_records WHERE session_id = $1`, session); err != nil {
			t.Logf("cleanup: %v", err)
		}
		if _, err := pg.Exec(`DELETE FROM llm_usage WHERE profile_name = $1`, profile); err != nil {
			t.Error("usage cleanup failed")
		}
	}()

	ctx := transcript.WithSessionID(context.Background(), session)
	var delivered strings.Builder
	for event, err := range rec.Stream(ctx, llm.CompletionRequest{
		System:   []string{secret},
		Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: secret}}}},
		Tools: []llm.ToolSchema{{
			Name:        "bash",
			Description: "run a shell command",
			InputSchema: map[string]any{"type": "object"},
		}},
		MaxTokens: 512,
	}) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if event.Type == llm.SETextDelta {
			delivered.WriteString(event.Text)
		}
	}
	if received.Load() != 1 || delivered.String() != secret {
		t.Fatal("recording changed provider traffic or consumer text")
	}

	var id int64
	if err := pg.QueryRow(`SELECT id FROM llm_records WHERE session_id=$1 ORDER BY id DESC LIMIT 1`, session).Scan(&id); err != nil {
		t.Fatalf("no record written: %v", err)
	}
	got, err := pg.GetLLMRecord(id)
	if err != nil {
		t.Fatalf("GetLLMRecord: %v", err)
	}

	if got.RawRequest != dlp.Redacted || got.RawResponse != dlp.Redacted ||
		!json.Valid([]byte(got.RequestBody)) || !json.Valid([]byte(got.ResponseBody)) {
		t.Fatal("normalized/raw storage policy was bypassed")
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "toolu_raw") {
		t.Fatal("sensitive provider content reached the stored record")
	}
	if got.InputTokens != 5 || got.OutputTokens != 9 || got.Status != "ok" {
		t.Fatal("recording lost usage or call status")
	}
}
