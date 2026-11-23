package intercept

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

const (
	contextLimit = 24
	entryLimit   = 8 * 1024
	promptLimit  = 32 * 1024
	outputLimit  = 64 * 1024
)

type traceKey struct{}
type callKey struct{}
type recordRedactKey struct{}

// WithRecordRedaction은 이 문맥의 감사 기록(도구 입력·사용자 글·문맥 항목·판정 모델 입력)에 쓸 저장 기록 가림을 단다(#886
// 이동찬 리뷰). E② 진단처럼 모델에 준 값(model_visible_v1 비밀번호)을 기록에는 남기지 않아야 하는 세션이 쓴다. 입력 digest는
// 승인 훅과의 대조 키라 원문으로 계산한다.
func WithRecordRedaction(ctx context.Context, redact func(string) string) context.Context {
	if redact == nil {
		return ctx
	}
	return context.WithValue(ctx, recordRedactKey{}, redact)
}

func recordRedaction(ctx context.Context) func(string) string {
	redact, _ := ctx.Value(recordRedactKey{}).(func(string) string)
	return redact
}

// recordBytes는 저장할 도구 입력을 가린다. 가림이 없으면 그대로다.
func recordBytes(ctx context.Context, input []byte) []byte {
	if redact := recordRedaction(ctx); redact != nil {
		return []byte(redact(string(input)))
	}
	return input
}

type completion func(status, output string, truncated bool)

type tracedCall struct {
	key       string
	audit     db.InterceptAudit
	claimed   bool
	ambiguous bool
	complete  completion
}

// Trace belongs to ONE Prompt invocation. SDK v0.3.6 hooks omit the tool ID;
// correlate only when exactly one outstanding event has matching input. Never
// guess between simultaneous identical requests, even when results arrive FIFO.
type Trace struct {
	mu      sync.Mutex
	redact  func(string) string
	runID   string
	user    string
	userCut bool
	entries []db.InterceptContextEntry
	cut     bool
	calls   map[string]*tracedCall
}

func WithTrace(ctx context.Context, user string, prior []db.InterceptContextEntry) (context.Context, *Trace) {
	t := &Trace{runID: rand.Text(), calls: make(map[string]*tracedCall), redact: recordRedaction(ctx)}
	if t.redact != nil {
		user = t.redact(user)
	}
	t.user, t.userCut = bounded(user, promptLimit)
	for _, e := range prior {
		t.append(e)
	}
	return context.WithValue(ctx, traceKey{}, t), t
}

func (t *Trace) append(e db.InterceptContextEntry) {
	if t.redact != nil {
		e.Text = t.redact(e.Text)
	}
	var cut bool
	e.Text, cut = bounded(e.Text, entryLimit)
	e.Truncated = e.Truncated || cut
	t.cut = t.cut || e.Truncated
	t.entries = append(t.entries, e)
	if len(t.entries) > contextLimit {
		t.entries = append([]db.InterceptContextEntry(nil), t.entries[len(t.entries)-contextLimit:]...)
		t.cut = true
	}
}

func (t *Trace) Append(e db.InterceptContextEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.append(e)
}

func (t *Trace) Start(id, tool string, input []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls[id] = &tracedCall{key: tool + ":" + digestInput(input), audit: db.InterceptAudit{
		RunID: t.runID, ToolUseID: id, Correlation: "exact", InputDigest: digestInput(input),
		UserMessage: t.user, UserTruncated: t.userCut, CapturedAt: time.Now().UTC(),
		Context: append([]db.InterceptContextEntry{}, t.entries...), ContextTruncated: t.cut,
	}}
	t.append(db.InterceptContextEntry{Kind: "tool_use", Tool: tool, ToolUseID: id, Text: string(input)})
}

// WithCall claims the event before model review starts, so subsequent tools
// cannot change this approval's context while the judge is running.
func WithCall(ctx context.Context, tool string, input []byte) context.Context {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	a := db.InterceptAudit{Correlation: "unavailable", InputDigest: digestInput(input), CapturedAt: time.Now().UTC()}
	if t != nil {
		t.mu.Lock()
		var candidates []*tracedCall
		for _, c := range t.calls {
			if !c.claimed && c.key == tool+":"+a.InputDigest {
				candidates = append(candidates, c)
			}
		}
		if len(candidates) == 1 && !candidates[0].ambiguous {
			candidates[0].claimed = true
			a = candidates[0].audit
		} else {
			a.RunID, a.UserMessage, a.UserTruncated = t.runID, t.user, t.userCut
			if len(candidates) > 0 {
				a.Correlation = "ambiguous"
				for _, c := range candidates {
					c.ambiguous = true
				}
			}
		}
		t.mu.Unlock()
	}
	return context.WithValue(ctx, callKey{}, a)
}

func (t *Trace) bind(id string, f completion) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.calls[id]; c != nil {
		c.complete = f
	}
}

func (t *Trace) Complete(id, output string, isError bool) {
	t.mu.Lock()
	c := t.calls[id]
	delete(t.calls, id)
	t.mu.Unlock()
	if c == nil || c.complete == nil {
		return
	}
	status := "succeeded"
	if isError {
		status = "failed"
	}
	out, cut := bounded(output, outputLimit)
	c.complete(status, out, cut)
}

// Finish marks missing results unknown, never successful. The tool may have
// been interrupted or its final event lost; this is distinct from a tool error.
func (t *Trace) Finish() {
	t.mu.Lock()
	calls := t.calls
	t.calls = make(map[string]*tracedCall)
	t.mu.Unlock()
	for _, c := range calls {
		if c.complete != nil {
			c.complete("unknown", "실행이 끝났지만 도구 결과를 받지 못했다", false)
		}
	}
}

func auditFor(ctx context.Context, dec Decision, input []byte, status string) *db.InterceptAudit {
	a, ok := ctx.Value(callKey{}).(db.InterceptAudit)
	if !ok {
		a = db.InterceptAudit{Correlation: "unavailable", InputDigest: digestInput(input), CapturedAt: time.Now().UTC()}
	}
	a.InitialAction, a.InitialReason = dec.Action, dec.Message
	a.ModelFallback = dec.ModelFallback
	a.ModelInput, a.ModelInputDigest = dec.ModelInput, dec.ModelInputDigest
	a.RuleName, a.ConfigDigest, a.ProfileID = dec.RuleName, dec.ConfigDigest, dec.ProfileID
	a.ExecutionStatus = "not_started"
	if status == "allowed" {
		a.EffectiveAction, a.ExecutionStatus = "allow", "awaiting_result"
		if a.Correlation != "exact" {
			a.ExecutionStatus = "unknown"
		}
		// Keep the exact model input for EVERY model verdict, including automatic
		// allows. Raw audit history is not model input. Preserve the existing
		// lightweight allow-retention policy; render the saved input directly.
		a.UserMessage, a.UserTruncated = "", false
		a.Context, a.ContextTruncated = nil, false
	}
	if status == "denied" {
		a.EffectiveAction, a.ExecutionStatus = "deny", "not_executed"
	}
	if redact := recordRedaction(ctx); redact != nil {
		// digest(입력·판정 모델 입력)는 원문으로 계산한 값을 둔다. 저장하는 글만 가린다.
		a.UserMessage = redact(a.UserMessage)
		if len(a.ModelInput) > 0 {
			a.ModelInput = json.RawMessage(redact(string(a.ModelInput)))
		}
		for i := range a.Context {
			a.Context[i].Text = redact(a.Context[i].Text)
		}
	}
	return &a
}

func (i *Interceptor) bindResult(ctx context.Context, id int64, audit *db.InterceptAudit) {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	if t == nil || audit.Correlation != "exact" || audit.ToolUseID == "" {
		return
	}
	t.bind(audit.ToolUseID, func(status, output string, cut bool) {
		_ = i.db.CompleteIntercept(id, audit.RunID, audit.ToolUseID, status, output, cut)
	})
}

func digestInput(input []byte) string {
	var value any
	d := json.NewDecoder(bytes.NewReader(input))
	d.UseNumber()
	if d.Decode(&value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			input = canonical
		}
	}
	h := sha256.Sum256(input)
	return hex.EncodeToString(h[:])
}

func bounded(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end], true
}
