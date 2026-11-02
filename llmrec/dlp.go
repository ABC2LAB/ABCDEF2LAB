package llmrec

import (
	"context"
	"encoding/json"

	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/norma/llm"
)

const (
	// Only small, minimized metadata reaches this policy, regardless of wire size.
	recordingMaxBytes        = 32 << 10
	recordingMaxDepth        = 8
	recordingMaxObjectFields = 16
	recordingMaxArrayItems   = 1
	// PostgreSQL stores these counters in signed INTEGER columns.
	recordingMaxInteger = 1<<31 - 1
)

func newRecordingPolicy() (*dlp.Policy, error) {
	return dlp.New(dlp.Limits{
		MaxInputBytes: recordingMaxBytes, MaxOutputBytes: recordingMaxBytes,
		MaxDepth: recordingMaxDepth, MaxObjectFields: recordingMaxObjectFields,
		MaxArrayItems: recordingMaxArrayItems,
	}, map[string]dlp.Rule{
		"request": dlp.Object(map[string]dlp.Rule{
			"system": dlp.Redact(), "messages": dlp.Redact(), "tools": dlp.Redact(),
			"stop": dlp.Redact(), "thinking": dlp.Redact(), "temperature": dlp.Redact(),
			"max_tokens": dlp.Integer(0, recordingMaxInteger), "tools_count": dlp.Integer(0, recordingMaxInteger),
		}),
		"response": dlp.Object(map[string]dlp.Rule{
			"text": dlp.Redact(), "thinking": dlp.Redact(),
			"stop_reason": dlp.Enum("", "stop", "end_turn", "length", "max_tokens", "tool_use", "tool_calls",
				"stop_sequence", "pause_turn", "refusal", "content_filter", dlp.Redacted),
			"usage": dlp.Object(map[string]dlp.Rule{
				"input_tokens": dlp.Integer(0, recordingMaxInteger), "output_tokens": dlp.Integer(0, recordingMaxInteger),
				"cache_read_tokens": dlp.Integer(0, recordingMaxInteger), "cache_write_tokens": dlp.Integer(0, recordingMaxInteger),
			}),
		}),
		"raw_request": dlp.Redact(), "raw_response": dlp.Redact(), "error": dlp.Redact(),
	})
}

// requestMetadata deliberately does not inspect message/tool bodies or marshal
// them: arbitrary RawMessage values and large content are opaque to this sink.
func (r *Recorder) requestMetadata(req llm.CompletionRequest) map[string]any {
	m := map[string]any{
		"system": dlp.Redacted, "messages": dlp.Redacted, "max_tokens": req.MaxTokens,
	}
	if req.Thinking != "" || r.thinkingType != "" || r.reasoningEffort != "" {
		m["thinking"] = dlp.Redacted
	}
	if len(req.Tools) > 0 {
		m["tools"], m["tools_count"] = dlp.Redacted, len(req.Tools)
	}
	if req.Temperature != nil {
		m["temperature"] = dlp.Redacted
	}
	if len(req.Stop) > 0 {
		m["stop"] = dlp.Redacted
	}
	return m
}

type recordingBodies struct {
	Request     json.RawMessage `json:"request"`
	Response    json.RawMessage `json:"response"`
	RawRequest  string          `json:"raw_request"`
	RawResponse string          `json:"raw_response"`
	Error       string          `json:"error"`
}

func (r *Recorder) projectBodies(request map[string]any, hasThinking bool, usage llm.Usage, stopReason string, capt *Capture, failed bool) (recordingBodies, error) {
	if r.recordPolicy == nil {
		return recordingBodies{}, dlp.ErrPolicy
	}
	response := map[string]any{
		"text": dlp.Redacted, "stop_reason": recordingStopReason(stopReason),
		"usage": map[string]int{
			"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
			"cache_read_tokens": usage.CacheReadTokens, "cache_write_tokens": usage.CacheWriteTokens,
		},
	}
	if hasThinking {
		response["thinking"] = dlp.Redacted
	}
	payload := map[string]any{"request": request, "response": response}
	hasRequest, hasResponse := capt.recordingPresence()
	if hasRequest {
		payload["raw_request"] = dlp.Redacted
	}
	if hasResponse {
		payload["raw_response"] = dlp.Redacted
	}
	if failed {
		payload["error"] = dlp.Redacted
	}
	input, err := json.Marshal(payload)
	if err != nil {
		return recordingBodies{}, dlp.ErrBlocked
	}
	// Cancellation of the model call must not discard its final safe trace.
	// This is bounded, in-memory projection; no I/O is performed with this context.
	safe, err := r.recordPolicy.Project(context.Background(), input)
	if err != nil {
		return recordingBodies{}, err
	}
	var bodies recordingBodies
	if err := json.Unmarshal(safe.Bytes(), &bodies); err != nil {
		return recordingBodies{}, dlp.ErrBlocked
	}
	return bodies, nil
}

func (c *Capture) recordingPresence() (request, response bool) {
	if c == nil {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A response attempt exists even when its body was empty or never consumed.
	return c.request != "", len(c.attempts) > 0
}

func recordingStopReason(reason string) string {
	switch reason {
	case "", "stop", "end_turn", "length", "max_tokens", "tool_use", "tool_calls",
		"stop_sequence", "pause_turn", "refusal", "content_filter":
		return reason
	default:
		return dlp.Redacted
	}
}
