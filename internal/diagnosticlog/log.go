// Package diagnosticlog는 감사 정본과 분리된 최소 진단 메타데이터를 기록한다.
package diagnosticlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sync"
	"time"
)

const MaxBytes int64 = 16 << 20
const MaxEvents = 20000

// 해시 전 참조 길이를 제한해 원문 식별자가 메모리를 무제한 점유하지 않게 한다.
const maxReferenceBytes = 4096

var (
	ErrCreate = errors.New("diagnostic_log_create_failed")
	ErrWrite  = errors.New("diagnostic_log_write_failed")
	ErrLimit  = errors.New("diagnostic_log_limit")
	ErrEvent  = errors.New("diagnostic_log_event_invalid")
	ErrClosed = errors.New("diagnostic_log_closed")
)

// Event에는 자유 텍스트나 요청·모델 원문을 담는 필드가 없다.
// 식별자는 실제 호출자가 제공하며 외부 참조는 기록 직전에 해시한다.
type Event struct {
	CountKnown           bool      `json:"count_known,omitempty"`
	AttemptRef           string    `json:"attempt_ref,omitempty"`
	CandidateID          int64     `json:"candidate_id,omitempty"`
	Version              int       `json:"version"`
	Timestamp            time.Time `json:"timestamp"`
	Sequence             uint64    `json:"sequence"`
	Level                string    `json:"level"`
	Component            string    `json:"component"`
	Event                string    `json:"event"`
	TaskID               int64     `json:"task_id,omitempty"`
	RunRef               string    `json:"run_ref,omitempty"`
	IntentID             int64     `json:"intent_id,omitempty"`
	CallRef              string    `json:"call_ref,omitempty"`
	Stage                string    `json:"stage,omitempty"`
	Status               string    `json:"status,omitempty"`
	ReasonCode           string    `json:"reason_code,omitempty"`
	Tool                 string    `json:"tool,omitempty"`
	Role                 string    `json:"role,omitempty"`
	Model                string    `json:"model,omitempty"`
	ProviderRef          string    `json:"provider_ref,omitempty"`
	ModelRef             string    `json:"model_ref,omitempty"`
	ReservedInputTokens  int64     `json:"reserved_input_tokens,omitempty"`
	ReservedOutputTokens int64     `json:"reserved_output_tokens,omitempty"`
	ReservedCostMicros   int64     `json:"reserved_cost_micros,omitempty"`
	CostMicros           int64     `json:"cost_micros,omitempty"`
	AuditComplete        *bool     `json:"audit_complete,omitempty"`
	ExecutionClosed      *bool     `json:"execution_closed,omitempty"`
	ResultStored         *bool     `json:"result_stored,omitempty"`
	ObservationComplete  *bool     `json:"observation_complete,omitempty"`
	TargetRequests       int       `json:"target_requests,omitempty"`
	Approved             int       `json:"approved,omitempty"`
	Denied               int       `json:"denied,omitempty"`
	Blocked              int       `json:"blocked,omitempty"`
	Candidates           int       `json:"candidates,omitempty"`
	Provider             string    `json:"provider,omitempty"`
	DurationMS           int64     `json:"duration_ms,omitempty"`
	Count                int       `json:"count,omitempty"`
	HTTPStatus           int       `json:"http_status,omitempty"`
	InputTokens          int64     `json:"input_tokens,omitempty"`
	OutputTokens         int64     `json:"output_tokens,omitempty"`
	ReservedTokens       int64     `json:"reserved_tokens,omitempty"`
	ReservedCostUSD      float64   `json:"reserved_cost_usd,omitempty"`
	ReportedCostUSD      float64   `json:"reported_cost_usd,omitempty"`
	CostUncertain        bool      `json:"cost_uncertain,omitempty"`
	Complete             *bool     `json:"complete,omitempty"`
}

type writer interface {
	io.Writer
	Sync() error
	Close() error
	Truncate(int64) error
}
type Logger struct {
	failed     chan struct{}
	incomplete bool
	// dropped는 기록 장애 때문에 남지 못한 기록 수다(장애를 낸 기록 포함). 경고에 누락 건수로 남긴다(P3 결정안 C10).
	dropped uint64
	mu      sync.Mutex
	writer  writer
	path    string
	task    int64
	run     string
	size    int64
	count   uint64
	err     error
	closed  bool
	check   func() error
	release func() error
}

func (*Logger) String() string   { return "diagnosticlog.Logger[protected]" }
func (*Logger) GoString() string { return "diagnosticlog.Logger[protected]" }

type contextKey struct{}

func With(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}
func From(ctx context.Context) *Logger {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(contextKey{}).(*Logger)
	return l
}
func Emit(ctx context.Context, e Event) error {
	if l := From(ctx); l != nil {
		return l.Record(e)
	}
	return nil
}
func (l *Logger) BindTask(id int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	if l.closed {
		return ErrClosed
	}
	if id <= 0 || l.task != 0 && l.task != id {
		return l.failLocked(ErrEvent)
	}
	l.task = id
	return nil
}
func (l *Logger) BindRun(ref string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	if l.closed {
		return ErrClosed
	}
	if ref == "" || len(ref) > maxReferenceBytes || l.run != "" && l.run != ref {
		return l.failLocked(ErrEvent)
	}
	l.run = ref
	return nil
}
func (l *Logger) Path() string     { l.mu.Lock(); defer l.mu.Unlock(); return l.path }
func (l *Logger) Incomplete() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.incomplete }
func (l *Logger) Dropped() uint64  { l.mu.Lock(); defer l.mu.Unlock(); return l.dropped }
func (l *Logger) Err() error       { l.mu.Lock(); defer l.mu.Unlock(); return l.err }

// Failed는 최초의 기록 장애에서 한 번 닫힌다. 호출자 작업을 잠금 안에서 실행하지 않는다.
func (l *Logger) Failed() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed == nil {
		l.failed = make(chan struct{})
		if l.err != nil {
			close(l.failed)
		}
	}
	return l.failed
}

// 호출자는 mu를 보유해야 한다. 원래 오류가 후속 오류로 바뀌지 않는다.
func (l *Logger) failLocked(err error) error {
	if l.err == nil {
		l.err = err
		if l.failed != nil {
			close(l.failed)
		}
	}
	return l.err
}
func digest(s string) string {
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
func (l *Logger) Record(e Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		l.dropped++
		return l.err
	}
	if l.closed {
		return ErrClosed
	}
	// 이 기록을 남기지 못하고 장애로 바뀌는 경로다. 남지 못한 기록으로 센다.
	fail := func(err error) error { l.dropped++; return l.failLocked(err) }
	if !valid(e) {
		return fail(ErrEvent)
	}
	if e.Status == "incomplete" || e.ObservationComplete != nil && !*e.ObservationComplete {
		l.incomplete = true
	}
	if l.task != 0 {
		if e.TaskID != 0 && e.TaskID != l.task {
			return fail(ErrEvent)
		}
		e.TaskID = l.task
	}
	if l.run != "" && e.RunRef != "" && l.run != e.RunRef {
		return fail(ErrEvent)
	}
	if e.RunRef == "" {
		e.RunRef = l.run
	}
	e.RunRef = digest(e.RunRef)
	e.CallRef = digest(e.CallRef)
	e.AttemptRef = digest(e.AttemptRef)
	e.ModelRef = digest(e.ModelRef)
	e.ProviderRef = digest(e.ProviderRef)
	e.Version = 1
	e.Timestamp = time.Now().UTC()
	e.Sequence = l.count + 1
	if e.Level == "" {
		e.Level = "INFO"
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fail(ErrEvent)
	}
	body = append(body, '\n')
	if l.count >= MaxEvents || l.size+int64(len(body)) > MaxBytes {
		return fail(ErrLimit)
	}
	if l.check != nil && l.check() != nil {
		return fail(ErrWrite)
	}
	n, err := l.writer.Write(body)
	if err != nil || n != len(body) {
		// 단일 소유 파일의 불완전한 마지막 행만 되돌린다. 기존 기록은 삭제하지 않는다.
		_ = l.writer.Truncate(l.size)
		_ = l.writer.Sync()
		return fail(ErrWrite)
	}
	l.size += int64(n)
	l.count++
	if l.writer.Sync() != nil {
		return fail(ErrWrite)
	}
	return nil
}
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return l.err
	}
	l.closed = true
	if l.check != nil && l.check() != nil && l.err == nil {
		l.failLocked(ErrWrite)
	}
	if l.writer.Sync() != nil && l.err == nil {
		l.failLocked(ErrWrite)
	}
	if l.writer.Close() != nil && l.err == nil {
		l.failLocked(ErrWrite)
	}
	if l.release != nil && l.release() != nil && l.err == nil {
		l.failLocked(ErrWrite)
	}
	return l.err
}
func valid(e Event) bool {
	return allowed(components, e.Component, false) && allowed(events, e.Event, false) && allowed(stages, e.Stage, true) && allowed(statuses, e.Status, true) && allowed(reasons, e.ReasonCode, true) && allowed(tools, e.Tool, true) && allowed(roles, e.Role, true) && allowed(models, e.Model, true) && allowed(providers, e.Provider, true) && allowed(levels, e.Level, true) && e.CandidateID >= 0 && len(e.AttemptRef) <= maxReferenceBytes && e.TaskID >= 0 && e.IntentID >= 0 && e.DurationMS >= 0 && e.Count >= 0 && e.HTTPStatus >= 0 && e.HTTPStatus <= 599 && e.InputTokens >= 0 && e.OutputTokens >= 0 && e.ReservedTokens >= 0 && finite(e.ReservedCostUSD) && finite(e.ReportedCostUSD) && e.TargetRequests >= 0 && e.Approved >= 0 && e.Denied >= 0 && e.Blocked >= 0 && e.Candidates >= 0 && e.ReservedInputTokens >= 0 && e.ReservedOutputTokens >= 0 && e.ReservedCostMicros >= 0 && e.CostMicros >= 0 && len(e.ModelRef) <= maxReferenceBytes && len(e.ProviderRef) <= maxReferenceBytes && len(e.RunRef) <= maxReferenceBytes && len(e.CallRef) <= maxReferenceBytes
}
func finite(n float64) bool                                  { return n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) }
func allowed(set map[string]bool, s string, empty bool) bool { return empty && s == "" || set[s] }

type roleKey struct{}

func WithRole(ctx context.Context, role string) context.Context {
	if !roles[role] {
		role = "unknown"
	}
	return context.WithValue(ctx, roleKey{}, role)
}
func RoleFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	role, _ := ctx.Value(roleKey{}).(string)
	return role
}
