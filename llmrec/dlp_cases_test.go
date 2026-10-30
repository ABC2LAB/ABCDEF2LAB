package llmrec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
	"github.com/google/uuid"
)

type dlpCaseProvider struct {
	request llm.CompletionRequest
	reason  string
	fault   error
	http    bool
	retry   bool
	wire    string
}

func (p *dlpCaseProvider) capture(ctx context.Context, req llm.CompletionRequest) {
	p.request = req
	if !p.http || CaptureFrom(ctx) == nil {
		return
	}
	c := CaptureFrom(ctx)
	c.SetRequest(dlpFixtureSecret)
	if p.retry {
		body := c.TeeResponse(429, io.NopCloser(strings.NewReader(dlpFixtureSecret)))
		_, _ = io.Copy(io.Discard, body)
		_ = body.Close()
	}
	body := c.TeeResponse(200, io.NopCloser(strings.NewReader(p.wire)))
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

func (p *dlpCaseProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.capture(ctx, req)
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: dlpFixtureSecret}, llm.TextBlock(dlpFixtureSecret),
	}}, p.reason, llm.Usage{InputTokens: 17, OutputTokens: 9}, p.fault
}

func (p *dlpCaseProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.capture(ctx, req)
		for _, ev := range []llm.StreamEvent{
			{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 17}},
			{Type: llm.SEThinkingDelta, Text: dlpFixtureSecret},
			{Type: llm.SETextDelta, Text: dlpFixtureSecret},
			{Type: llm.SEMessageDelta, StopReason: p.reason, Usage: llm.Usage{OutputTokens: 9}},
		} {
			if !yield(ev, nil) {
				return
			}
		}
		if p.fault != nil {
			yield(llm.StreamEvent{}, p.fault)
		}
	}
}

func TestRecorderDLPPersistenceMatrix(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires isolated ARTEX_PG_DSN")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name                                                                                                                 string
		stream, fault, cancel, disabled, noHTTP, emptyWire, retry, large, nilPolicy, incompatible, invalidCount, unknownStop bool
	}{
		{name: "complete"}, {name: "complete_error", fault: true},
		{name: "stream", stream: true}, {name: "stream_error", stream: true, fault: true},
		{name: "stream_consumer_cancel", stream: true, cancel: true},
		{name: "record_off", disabled: true}, {name: "no_http", noHTTP: true},
		{name: "empty_http_response", emptyWire: true}, {name: "retry_malformed_wire", retry: true},
		{name: "large_opaque_content", large: true}, {name: "nil_policy", nilPolicy: true},
		{name: "incompatible_policy", incompatible: true}, {name: "invalid_metadata_count", invalidCount: true},
		{name: "unknown_stop", unknownStop: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := "dlp-matrix-" + uuid.NewString()
			defer func() {
				if _, err := pg.Exec(`DELETE FROM llm_records WHERE profile_name=$1`, profile); err != nil {
					t.Error("record cleanup failed")
				}
				if _, err := pg.Exec(`DELETE FROM llm_usage WHERE profile_name=$1`, profile); err != nil {
					t.Error("usage cleanup failed")
				}
			}()
			provider := &dlpCaseProvider{reason: "tool_use", http: !tc.noHTTP, retry: tc.retry, wire: "malformed SSE: " + dlpFixtureSecret + "\xff"}
			if tc.emptyWire {
				provider.wire = ""
			}
			if tc.fault {
				provider.fault = errors.New(dlpFixtureSecret)
			}
			if tc.unknownStop {
				provider.reason = dlpFixtureSecret
			}
			text := dlpFixtureSecret
			if tc.large {
				text = strings.Repeat(text, recordingMaxBytes)
				provider.wire = text
			}
			temperature := 0.5
			req := llm.CompletionRequest{
				System: []string{text}, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{
					llm.TextBlock(text), {Type: llm.BlockToolUse, Input: json.RawMessage("invalid opaque JSON " + dlpFixtureSecret)},
				}}},
				Tools:     []llm.ToolSchema{{Name: dlpFixtureSecret, Description: text, InputSchema: map[string]any{"password": text}}},
				MaxTokens: 256, Temperature: &temperature, Stop: []string{dlpFixtureSecret}, Thinking: dlpFixtureSecret,
			}
			if tc.invalidCount {
				req.MaxTokens = -1
			}
			recorder := Wrap(provider, pg, "fixture-model", profile, "", "", func() bool { return !tc.disabled })
			if tc.nilPolicy {
				recorder.recordPolicy = nil
			}
			if tc.incompatible {
				recorder.recordPolicy, err = dlp.New(dlp.Limits{MaxInputBytes: 1024, MaxOutputBytes: 1024, MaxDepth: 4, MaxObjectFields: 8, MaxArrayItems: 1}, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(transcript.WithSessionID(t.Context(), "exp901-worker-"+uuid.NewString()))
			defer cancel()
			if tc.stream {
				var textSeen, thinkingSeen bool
				var returned error
				for event, err := range recorder.Stream(ctx, req) {
					if err != nil {
						returned = err
						break
					}
					if event.Type == llm.SETextDelta {
						textSeen = event.Text == dlpFixtureSecret
					}
					if event.Type == llm.SEThinkingDelta {
						thinkingSeen = event.Text == dlpFixtureSecret
					}
					if tc.cancel && event.Type == llm.SEMessageDelta {
						cancel()
						break
					}
				}
				if !textSeen || !thinkingSeen || !errors.Is(returned, provider.fault) {
					t.Fatal("stream forwarding changed")
				}
			} else {
				msg, stop, usage, returned := recorder.Complete(ctx, req)
				if msg.Text() != dlpFixtureSecret || stop != provider.reason || usage.InputTokens != 17 || !errors.Is(returned, provider.fault) {
					t.Fatal("atomic forwarding changed")
				}
			}
			if !reflect.DeepEqual(provider.request, req) {
				t.Fatal("provider request was changed")
			}
			var records, unsafe, usageRows, inputTokens, outputTokens int
			if err := pg.QueryRow(`SELECT count(*),count(*) FILTER (WHERE strpos(row_to_json(r)::text,$2)>0) FROM llm_records r WHERE profile_name=$1`, profile, dlpFixtureSecret).Scan(&records, &unsafe); err != nil {
				t.Fatal(err)
			}
			if err := pg.QueryRow(`SELECT count(*),max(input_tokens),max(output_tokens) FROM llm_usage WHERE profile_name=$1`, profile).Scan(&usageRows, &inputTokens, &outputTokens); err != nil {
				t.Fatal(err)
			}
			wantRows := 1
			if tc.disabled || tc.nilPolicy || tc.incompatible || tc.invalidCount {
				wantRows = 0
			}
			if records != wantRows || unsafe != 0 || usageRows != 1 || inputTokens != 17 || outputTokens != 9 {
				t.Fatalf("records=%d unsafe=%d usage=%d want records=%d unsafe=0 usage=1", records, unsafe, usageRows, wantRows)
			}
			if records == 0 {
				return
			}
			var id int64
			if err := pg.QueryRow(`SELECT id FROM llm_records WHERE profile_name=$1`, profile).Scan(&id); err != nil {
				t.Fatal(err)
			}
			stored, err := pg.GetLLMRecord(id)
			if err != nil {
				t.Fatal(err)
			}
			wantRaw := dlp.Redacted
			if tc.noHTTP {
				wantRaw = ""
			}
			if stored.RawRequest != wantRaw || stored.RawResponse != wantRaw || len(stored.RequestBody)+len(stored.ResponseBody) > recordingMaxBytes {
				t.Fatal("raw policy or output bounds were bypassed")
			}
			wantStatus, wantError := "ok", ""
			if tc.fault || tc.cancel {
				wantStatus, wantError = "error", dlp.Redacted
			}
			if stored.Status != wantStatus || stored.Error != wantError {
				t.Fatal("stored status/error changed")
			}
			var request map[string]any
			var response struct {
				Text, Thinking string
				StopReason     string `json:"stop_reason"`
			}
			if json.Unmarshal([]byte(stored.RequestBody), &request) != nil || json.Unmarshal([]byte(stored.ResponseBody), &response) != nil {
				t.Fatal("projected bodies are not JSON")
			}
			for _, key := range []string{"system", "messages", "tools", "stop", "thinking", "temperature"} {
				if request[key] != dlp.Redacted {
					t.Fatal("request content escaped masking")
				}
			}
			wantStop := provider.reason
			if tc.unknownStop {
				wantStop = dlp.Redacted
			}
			if request["max_tokens"] != float64(256) || response.Text != dlp.Redacted || response.Thinking != dlp.Redacted || response.StopReason != wantStop {
				t.Fatal("safe metadata or response mask was lost")
			}
		})
	}
}
