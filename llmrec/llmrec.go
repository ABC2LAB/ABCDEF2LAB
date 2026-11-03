// Package llmrec implements a recording decorator for llm.Provider. It intercepts
// model calls and persists DLP-projected metadata to PostgreSQL. Message, wire
// and error contents are never passed to the recording database writer.
package llmrec

import (
	"context"
	"iter"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/dlp"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

type taskIDContextKey struct{}
type workerContextKey struct{}

// WithTaskID attaches the owning task registry id to an LLM call. Session ids
// are based on exploration ids, which are not interchangeable with task ids.
func WithTaskID(ctx context.Context, taskID string) context.Context {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, taskIDContextKey{}, taskID)
}

// TaskIDFrom returns the explicit task registry id attached by the task runtime.
func TaskIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	taskID, _ := ctx.Value(taskIDContextKey{}).(string)
	return strings.TrimSpace(taskID)
}

// WithWorker overrides the agent-lane ("worker") label for an LLM call. The lane
// is normally parsed from the transcript session id (exp<N>-<role>); calls made
// outside the engine's worker/planner sessions — e.g. the intercept fallback judge
// — carry no such session, so they attach their lane explicitly here. This lets the
// usage ledger single out that spend (worker='judge') for the config page.
func WithWorker(ctx context.Context, worker string) context.Context {
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return ctx
	}
	return context.WithValue(ctx, workerContextKey{}, worker)
}

// workerFrom returns the explicit lane override, or "" when none is set.
func workerFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	worker, _ := ctx.Value(workerContextKey{}).(string)
	return strings.TrimSpace(worker)
}

// Recorder wraps an llm.Provider and records every completion call.
type Recorder struct {
	inner llm.Provider
	pg    *db.DB
	model string // model name (from config, not in CompletionRequest)
	prof  string // LLM profile name (from llm_profiles)
	// thinkingType / reasoningEffort 是配置级思考参数(思考开关 / 思考强度)。它们在
	// norma 的 buildBody() 里从 provider 配置注入真正的 HTTP body,不出现在
	// CompletionRequest 上,故 Recorder 需在此单独带一份,序列化时写进录制。
	thinkingType    string
	reasoningEffort string
	enabled         func() bool // reports whether recording is currently on; nil = always record
	recordPolicy    *dlp.Policy
}

// Wrap returns a Provider that records calls to pg when enabled() reports true.
// profName is the LLM profile name (e.g. "default"); may be empty. thinkingType /
// reasoningEffort are the config-level thinking params actually sent to the API
// (empty = not sent). A nil enabled predicate records unconditionally.
func Wrap(inner llm.Provider, pg *db.DB, model, profName, thinkingType, reasoningEffort string, enabled func() bool) *Recorder {
	policy, err := newRecordingPolicy()
	if err != nil {
		log.Print("[llmrec] DLP policy unavailable; body recording blocked")
	}
	return &Recorder{
		inner: inner, pg: pg, model: model, prof: profName,
		thinkingType: thinkingType, reasoningEffort: reasoningEffort, enabled: enabled,
		recordPolicy: policy,
	}
}

// parseSession extracts task id and worker role from session strings like
// "exp1-worker-i3" → ("1", "worker") or "exp2-planner" → ("2", "planner").
func parseSession(s string) (taskID, worker string) {
	// format: exp<N>-<role>[-suffix]
	if !strings.HasPrefix(s, "exp") {
		return "", ""
	}
	rest := s[3:] // after "exp"
	// split task number
	i := strings.IndexByte(rest, '-')
	if i < 0 {
		return rest, ""
	}
	taskID = rest[:i]
	rest = rest[i+1:]
	// worker role is up to the next '-' (e.g. "worker" from "worker-i3")
	if j := strings.IndexByte(rest, '-'); j >= 0 {
		worker = rest[:j]
	} else {
		worker = rest
	}
	return taskID, worker
}

// Stream forwards provider events and records only their projected metadata.
//
// The session identifier (e.g. "exp1-worker-i3") is carried on ctx by the norma
// harness via transcript.WithSessionID; reading it per-call is race-free even when
// planner and multiple workers share one Recorder instance.
func (r *Recorder) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	// Projected trace recording is gated by the
	// llm_record setting. Lightweight usage metering always runs — it powers token
	// stats and must be complete even for interrupted/failed runs, so it is NOT
	// gated. Only trace preparation and capture are skipped when off.
	recordBodies := r.enabled == nil || r.enabled()
	start := time.Now()
	session := transcript.SessionIDFrom(ctx)
	parsedID, worker := parseSession(session)
	if ov := workerFrom(ctx); ov != "" {
		worker = ov
	}
	expID := db.ParseExpID(parsedID)
	taskID := TaskIDFrom(ctx)
	if taskID == "" {
		// Backward compatibility for non-task callers. For task calls, the task
		// runtime always supplies the registry id explicitly.
		taskID = parsedID
	}

	// Never serialize message/tool contents for the recording sink. Capture stays
	// transient; persistence reads only whether a wire attempt existed.
	var reqBody map[string]any
	var capt *Capture
	if recordBodies {
		reqBody = r.requestMetadata(req)
		ctx, capt = NewMetadataCapture(ctx)
	}

	return func(yield func(llm.StreamEvent, error) bool) {
		var (
			hasThinking bool
			usage       llm.Usage
			stopReason  string
			streamErr   error
		)

		finished := false
		finish := func(err error) {
			if finished {
				return
			}
			finished = true
			status := "ok"
			if err != nil {
				status = "error"
			}
			// Lightweight metering row — always written.
			r.recordUsage(taskID, expID, worker, usage, int(time.Since(start).Milliseconds()), status)
			// Heavy trace row — only when body recording is on.
			if recordBodies {
				r.record(session, taskID, worker, reqBody, capt, start, hasThinking, usage, stopReason, err)
			}
		}
		defer func() { finish(ctx.Err()) }()

		for ev, err := range r.inner.Stream(ctx, req) {
			if err != nil {
				streamErr = err
				finish(streamErr)
				if !yield(ev, err) {
					return
				}
				return
			}
			// Track usage and content presence without accumulating private text.
			// Anthropic (and the other providers) split token usage across events:
			// message_start carries ONLY the input side (input + cache), message_delta
			// ONLY the output. They must be FOLDED with Add — overwriting on delta
			// would zero out the input already counted at start (mirrors the SDK's own
			// llm.Accumulator; see norma/llm/accumulate.go).
			switch ev.Type {
			case llm.SEThinkingDelta:
				hasThinking = hasThinking || ev.Text != ""
			case llm.SEMessageStart:
				usage.Add(ev.Usage)
			case llm.SEMessageDelta:
				if ev.StopReason != "" {
					stopReason = ev.StopReason
				}
				usage.Add(ev.Usage)
			}
			if !yield(ev, err) {
				return
			}
		}

		// Stream completed normally.
		finish(streamErr)
	}
}

// Complete implements the atomic completion path while preserving the same
// usage metering, normalized body recording and raw wire capture as Stream.
func (r *Recorder) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	recordBodies := r.enabled == nil || r.enabled()
	start := time.Now()
	session := transcript.SessionIDFrom(ctx)
	parsedID, worker := parseSession(session)
	if ov := workerFrom(ctx); ov != "" {
		worker = ov
	}
	expID := db.ParseExpID(parsedID)
	taskID := TaskIDFrom(ctx)
	if taskID == "" {
		taskID = parsedID
	}

	var reqBody map[string]any
	var capt *Capture
	if recordBodies {
		reqBody = r.requestMetadata(req)
		ctx, capt = NewMetadataCapture(ctx)
	}

	msg, stopReason, usage, err := r.inner.Complete(ctx, req)
	status := "ok"
	if err != nil {
		status = "error"
	}
	r.recordUsage(taskID, expID, worker, usage, int(time.Since(start).Milliseconds()), status)
	if recordBodies {
		r.record(session, taskID, worker, reqBody, capt, start, hasThinkingContent(msg), usage, stopReason, err)
	}
	return msg, stopReason, usage, err
}

func hasThinkingContent(msg llm.Message) bool {
	for _, block := range msg.Content {
		if block.Type == llm.BlockThinking && block.Thinking != "" {
			return true
		}
	}
	return false
}

// recordUsage appends one lightweight metering row to llm_usage (no bodies). Skips
// zero-token calls with no model, which carry nothing worth metering.
func (r *Recorder) recordUsage(taskID string, expID int64, worker string, usage llm.Usage, latencyMs int, status string) {
	if r.pg == nil {
		return
	}
	if r.model == "" && usage.InputTokens == 0 && usage.OutputTokens == 0 &&
		usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 {
		return
	}
	err := r.pg.InsertLLMUsage(&db.LLMUsage{
		TaskID:        taskID,
		ExplorationID: expID,
		Worker:        worker,
		Model:         r.model,
		ProfileName:   r.prof,
		LatencyMs:     latencyMs,
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		CacheRead:     usage.CacheReadTokens,
		CacheWrite:    usage.CacheWriteTokens,
		Status:        status,
	})
	if err != nil {
		log.Print("[llmusage] insert failed")
	}
}

// record persists one LLM call to PostgreSQL before the provider stream returns.
// Keeping the write inside the owning task operation means task deletion can
// drain calls and then remove records without a late async insert recreating one.
func (r *Recorder) record(session, taskID, worker string, reqBody map[string]any, capt *Capture, start time.Time, hasThinking bool, usage llm.Usage, stopReason string, streamErr error) {
	if r.pg == nil {
		return
	}
	bodies, err := r.projectBodies(reqBody, hasThinking, usage, stopReason, capt, streamErr != nil)
	if err != nil {
		log.Print("[llmrec] DLP projection failed; body recording blocked")
		return
	}
	latency := int(time.Since(start).Milliseconds())
	status := "ok"
	if streamErr != nil {
		status = "error"
	}

	rec := &db.LLMRecord{
		SessionID:    session,
		TaskID:       taskID,
		Worker:       worker,
		Model:        r.model,
		ProfileName:  r.prof,
		LatencyMs:    latency,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		CacheRead:    usage.CacheReadTokens,
		CacheWrite:   usage.CacheWriteTokens,
		Status:       status,
		Error:        bodies.Error,
		RequestBody:  string(bodies.Request),
		ResponseBody: string(bodies.Response),
		RawRequest:   bodies.RawRequest,
		RawResponse:  bodies.RawResponse,
	}

	if err := r.pg.InsertLLMRecord(rec); err != nil {
		log.Print("[llmrec] insert failed")
	}
}
