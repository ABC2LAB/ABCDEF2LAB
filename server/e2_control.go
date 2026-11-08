package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/e2proxy"
)

const E2ControlFormat = "abc2lab.e2-runner-control/v1"
const E2ControlSocket = "e2-control.sock"
const e2ControlMaxMessage = 8 << 10

var ErrE2Control = errors.New("E② 평가 제어 채널을 사용할 수 없습니다")
var e2ControlRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
var e2ControlHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var e2ControlTaskID = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

// Backend는 실제 신뢰 호스트에 연결한다. 요청 참조는 실행 권한을 만들지 않는다.
// CloseAdmission은 신규 task/intent를 먼저 닫고, Finalize는 전체 writer join 뒤 게시한다.
type E2ControlBackend interface {
	Ready(context.Context) (E2ControlReady, error)
	BindReset(context.Context, string) error
	Head(context.Context) (E2ControlHead, error)
	CloseAdmission(context.Context, string) (E2ControlHead, error)
	Finalize(context.Context, string) (E2ControlFinal, error)
}
type E2ControlReady struct {
	State, Reason, ConfigSHA256, PolicyFingerprintSHA256 string
	HeaderMode                                           e2proxy.HeaderMode
	Evaluation                                           *E2ControlEvaluation
}
type E2ControlEvaluation struct {
	LLMBackend string `json:"llm_backend"`
	TargetKind string `json:"target_kind"`
}
type E2ControlCheckpoint struct {
	Sequence uint64 `json:"sequence"`
	MAC      string `json:"mac"`
}
type E2ControlHead struct {
	State, ExecutionRef, RunRef, EngineTaskID, PolicyFingerprintSHA256 string
	Checkpoint                                                         *E2ControlCheckpoint
}
type E2ControlFinal struct {
	State, Reason, ExecutionRef, ManifestSHA256 string
	Checkpoint                                  *E2ControlCheckpoint
}

type E2ControlOptions struct {
	ControlDirectory string `json:"-"`
	EvalRunID        string `json:"-"`
	ReservationRef   string `json:"-"`
	RunnerUID        uint32 `json:"-"`
	EngineUID        uint32 `json:"-"`
	ToolsUID         uint32 `json:"-"`
	// 이 값은 평가 모드 선택이며 mount/PID 격리의 수용 증명이 아니다.
	EvaluationDeployment bool             `json:"-"`
	IOTimeout            time.Duration    `json:"-"`
	CloseTimeout         time.Duration    `json:"-"`
	MaxConnections       int              `json:"-"`
	Backend              E2ControlBackend `json:"-"`
}

func (E2ControlOptions) String() string   { return "server.E2ControlOptions[protected]" }
func (E2ControlOptions) GoString() string { return "server.E2ControlOptions[protected]" }

type e2ControlTransport interface{ Close(context.Context) error }
type E2ControlService struct {
	opts              E2ControlOptions
	ctx               context.Context
	cancel            context.CancelFunc
	transport         e2ControlTransport
	gate              chan struct{}
	mu                sync.Mutex
	resetRef          string
	bound             bool
	closeRef          string
	closeAcknowledged bool
	closeHead         E2ControlHead
	final             E2ControlFinal
	finalReady        bool
	finalDone         chan struct{}
}

func StartE2ControlService(parent context.Context, opts E2ControlOptions) (*E2ControlService, error) {
	// 실제 embedded 엔진은 이 서비스와 같은 프로세스/UID이다. 역할 번호나 mode opt-in으로
	// 평가 전용 UID/mount 분리를 만들어낼 수 없으므로 이 생산 조합은 기동하지 않는다.
	if _, embedded := opts.Backend.(interface{ e2ControlEmbeddedEngine() }); embedded {
		return nil, ErrE2Control
	}
	if parent == nil || parent.Err() != nil || nilE2ControlBackend(opts.Backend) || !validE2ControlRef(opts.EvalRunID) || !validE2ControlRef(opts.ReservationRef) ||
		!opts.EvaluationDeployment || opts.RunnerUID == 0 || opts.EngineUID == 0 || opts.ToolsUID == 0 || opts.EngineUID == opts.RunnerUID || opts.ToolsUID == opts.RunnerUID ||
		opts.IOTimeout <= 0 || opts.IOTimeout > time.Minute || opts.CloseTimeout <= 0 || opts.CloseTimeout > 2*time.Minute || opts.MaxConnections < 1 || opts.MaxConnections > 32 {
		return nil, ErrE2Control
	}
	ctx, cancel := context.WithCancel(parent)
	service := &E2ControlService{opts: opts, ctx: ctx, cancel: cancel, gate: make(chan struct{}, 1)}
	transport, err := startE2ControlTransport(service)
	if err != nil {
		cancel()
		return nil, ErrE2Control
	}
	service.transport = transport
	return service, nil
}

func (s *E2ControlService) Close(ctx context.Context) error {
	if s == nil || ctx == nil || s.cancel == nil || s.transport == nil {
		return ErrE2Control
	}
	s.cancel()
	if s.transport.Close(ctx) != nil {
		return ErrE2Control
	}
	s.mu.Lock()
	done := s.finalDone
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ErrE2Control
		}
	}
	return nil
}

type e2ControlRequest struct{ Format, Type, EvalRunID, ReservationRef, ResetGeneration, CloseRequestRef string }

func nilE2ControlBackend(v E2ControlBackend) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
		return r.IsNil()
	}
	return false
}
func validE2ControlRef(v string) bool {
	return e2ControlRef.MatchString(v) && !bytes.Contains([]byte(v), []byte("://"))
}
func decodeE2ControlRequest(data []byte) (e2ControlRequest, error) {
	if len(data) == 0 || len(data) > e2ControlMaxMessage || !utf8.Valid(data) {
		return e2ControlRequest{}, ErrE2Control
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if uniqueE2ControlValue(dec, 0) != nil {
		return e2ControlRequest{}, ErrE2Control
	}
	if _, err := dec.Token(); err != io.EOF {
		return e2ControlRequest{}, ErrE2Control
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return e2ControlRequest{}, ErrE2Control
	}
	var r e2ControlRequest
	for key, target := range map[string]*string{"format": &r.Format, "type": &r.Type, "eval_run_id": &r.EvalRunID, "reservation_ref": &r.ReservationRef} {
		if json.Unmarshal(raw[key], target) != nil {
			return r, ErrE2Control
		}
	}
	fields := 4
	switch r.Type {
	case "ready", "head":
	case "bind":
		fields++
		if json.Unmarshal(raw["reset_generation"], &r.ResetGeneration) != nil || !validE2ControlRef(r.ResetGeneration) {
			return r, ErrE2Control
		}
	case "close", "close_status":
		fields++
		if json.Unmarshal(raw["close_request_ref"], &r.CloseRequestRef) != nil || !validE2ControlRef(r.CloseRequestRef) {
			return r, ErrE2Control
		}
	default:
		return r, ErrE2Control
	}
	if len(raw) != fields || r.Format != E2ControlFormat || !validE2ControlRef(r.EvalRunID) || !validE2ControlRef(r.ReservationRef) {
		return r, ErrE2Control
	}
	return r, nil
}
func uniqueE2ControlValue(dec *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrE2Control
	}
	token, err := dec.Token()
	if err != nil {
		return ErrE2Control
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			v, err := dec.Token()
			key, ok := v.(string)
			if err != nil || !ok || seen[key] {
				return ErrE2Control
			}
			seen[key] = true
			if uniqueE2ControlValue(dec, depth+1) != nil {
				return ErrE2Control
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return ErrE2Control
		}
	case '[':
		for dec.More() {
			if uniqueE2ControlValue(dec, depth+1) != nil {
				return ErrE2Control
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return ErrE2Control
		}
	default:
		return ErrE2Control
	}
	return nil
}
func (s *E2ControlService) response(kind string) map[string]any {
	return map[string]any{"format": E2ControlFormat, "type": kind, "eval_run_id": s.opts.EvalRunID, "reservation_ref": s.opts.ReservationRef}
}
func (s *E2ControlService) refused(code string) map[string]any {
	m := s.response("error")
	m["code"] = code
	return m
}
func e2ControlNullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (s *E2ControlService) handle(ctx context.Context, data []byte) map[string]any {
	r, err := decodeE2ControlRequest(data)
	if err != nil {
		return s.refused("bad_request")
	}
	if r.EvalRunID != s.opts.EvalRunID || r.ReservationRef != s.opts.ReservationRef {
		return s.refused("other_run")
	}
	if ctx.Err() != nil || s.ctx.Err() != nil {
		return s.refused("closed")
	}
	switch r.Type {
	case "ready":
		v, err := s.opts.Backend.Ready(ctx)
		if err != nil || !validE2ControlReady(v) {
			return s.refused("unavailable")
		}
		m := s.response("ready")
		m["state"], m["reason"] = v.State, e2ControlNullable(v.Reason)
		m["config_sha256"], m["header_mode"], m["evaluation"], m["policy_fingerprint_sha256"] = nil, nil, nil, nil
		if v.State == "service_ready" {
			m["config_sha256"], m["header_mode"], m["evaluation"], m["policy_fingerprint_sha256"] = v.ConfigSHA256, v.HeaderMode, v.Evaluation, v.PolicyFingerprintSHA256
		}
		return m
	case "head":
		v, err := s.opts.Backend.Head(ctx)
		if err != nil || !validE2ControlHead(v) {
			return s.refused("unavailable")
		}
		m := s.response("head")
		m["state"], m["execution_ref"], m["run_ref"], m["engine_task_id"], m["policy_fingerprint_sha256"], m["checkpoint"] = v.State, e2ControlNullable(v.ExecutionRef), e2ControlNullable(v.RunRef), e2ControlNullable(v.EngineTaskID), e2ControlNullable(v.PolicyFingerprintSHA256), v.Checkpoint
		return m
	}
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return s.refused("unavailable")
	}
	defer func() { <-s.gate }()
	s.mu.Lock()
	closeRef, resetRef, bound, acknowledged := s.closeRef, s.resetRef, s.bound, s.closeAcknowledged
	s.mu.Unlock()
	switch r.Type {
	case "bind":
		if closeRef != "" {
			return s.refused("closed")
		}
		if resetRef != "" && resetRef != r.ResetGeneration {
			return s.refused("bind_conflict")
		}
		if !bound {
			ready, err := s.opts.Backend.Ready(ctx)
			if err != nil || !validE2ControlReady(ready) || ready.State != "service_ready" {
				return s.refused("not_ready")
			}
			// 결과 불명이어도 다른 값으로 다시 결속하지 않는다. 같은 값만 원래 공급자에 재확인한다.
			s.mu.Lock()
			s.resetRef = r.ResetGeneration
			s.mu.Unlock()
			if s.opts.Backend.BindReset(ctx, r.ResetGeneration) != nil || ctx.Err() != nil {
				return s.refused("unavailable")
			}
			s.mu.Lock()
			s.bound = true
			s.mu.Unlock()
		}
		m := s.response("bind")
		m["reset_generation"] = r.ResetGeneration
		return m
	case "close":
		if closeRef != "" && closeRef != r.CloseRequestRef {
			return s.refused("close_ref_conflict")
		}
		if !acknowledged {
			if !bound {
				return s.refused("not_ready")
			}
			s.mu.Lock()
			s.closeRef = r.CloseRequestRef
			s.mu.Unlock()
			head, err := s.opts.Backend.CloseAdmission(ctx, r.CloseRequestRef)
			if err != nil || ctx.Err() != nil || !validE2ControlHead(head) {
				return s.refused("unavailable")
			}
			s.mu.Lock()
			s.closeAcknowledged = true
			s.closeHead = head
			s.finalDone = make(chan struct{})
			s.mu.Unlock()
			go s.finalize(r.CloseRequestRef)
		}
		s.mu.Lock()
		head := s.closeHead
		s.mu.Unlock()
		m := s.response("close")
		m["close_request_ref"], m["execution_ref"], m["admission_closed"] = r.CloseRequestRef, e2ControlNullable(head.ExecutionRef), true
		return m
	case "close_status":
		if closeRef == "" || !acknowledged {
			return s.refused("not_ready")
		}
		if closeRef != r.CloseRequestRef {
			return s.refused("close_ref_conflict")
		}
		s.mu.Lock()
		final, ready, head := s.final, s.finalReady, s.closeHead
		s.mu.Unlock()
		if !ready {
			final = E2ControlFinal{State: "closing", ExecutionRef: head.ExecutionRef}
		}
		m := s.response("close_status")
		m["state"], m["reason"], m["close_request_ref"], m["execution_ref"], m["manifest_sha256"], m["checkpoint"] = final.State, e2ControlNullable(final.Reason), r.CloseRequestRef, e2ControlNullable(final.ExecutionRef), e2ControlNullable(final.ManifestSHA256), final.Checkpoint
		return m
	}
	return s.refused("bad_request")
}
func (s *E2ControlService) finalize(ref string) {
	ctx, cancel := context.WithTimeout(s.ctx, s.opts.CloseTimeout)
	defer cancel()
	final, err := s.opts.Backend.Finalize(ctx, ref)
	s.mu.Lock()
	expectedExecution := s.closeHead.ExecutionRef
	s.mu.Unlock()
	if err != nil || ctx.Err() != nil || !validE2ControlFinal(final) || final.ExecutionRef != expectedExecution {
		reason := "publish_failed"
		if err != nil {
			reason = "unknown"
		}
		if ctx.Err() != nil {
			reason = "drain_timeout"
		}
		if final.State == "incomplete" && final.ExecutionRef == expectedExecution && validE2ControlFinal(final) {
			reason = final.Reason
		}
		s.mu.Lock()
		execution := s.closeHead.ExecutionRef
		s.mu.Unlock()
		final = E2ControlFinal{State: "incomplete", Reason: reason, ExecutionRef: execution}
	}
	s.mu.Lock()
	s.final = final
	s.finalReady = true
	close(s.finalDone)
	s.mu.Unlock()
}
func validE2ControlCheckpoint(c *E2ControlCheckpoint) bool {
	return c == nil || e2ControlHash.MatchString(c.MAC)
}
func validE2ControlReady(v E2ControlReady) bool {
	if v.State != "starting" && v.State != "service_ready" && v.State != "failed" {
		return false
	}
	if v.Reason != "" && v.Reason != "config_rejected" && v.Reason != "policy_rejected" && v.Reason != "audit_unavailable" && v.Reason != "startup_failed" {
		return false
	}
	if v.State != "service_ready" {
		return v.ConfigSHA256 == "" && v.PolicyFingerprintSHA256 == "" && v.HeaderMode == "" && v.Evaluation == nil
	}
	if v.Reason != "" {
		return false
	}
	if !e2ControlHash.MatchString(v.ConfigSHA256) || !e2ControlHash.MatchString(v.PolicyFingerprintSHA256) {
		return false
	}
	switch v.HeaderMode {
	case e2proxy.HeaderPreserve, e2proxy.HeaderProtectedSessionProposal:
		return v.Evaluation == nil
	case e2proxy.HeaderEvaluationCallerPassthrough:
		return v.Evaluation != nil && (v.Evaluation.LLMBackend == "local" || v.Evaluation.LLMBackend == "api") && v.Evaluation.TargetKind == "public_practice"
	}
	return false
}
func validE2ControlHead(v E2ControlHead) bool {
	if !validE2ControlCheckpoint(v.Checkpoint) {
		return false
	}
	if v.State == "no_execution" {
		return v.ExecutionRef == "" && v.RunRef == "" && v.EngineTaskID == "" && v.PolicyFingerprintSHA256 == "" && v.Checkpoint == nil
	}
	if v.State != "running" && v.State != "closing" && v.State != "complete" && v.State != "incomplete" {
		return false
	}
	return validE2ControlRef(v.ExecutionRef) && validE2ControlRef(v.RunRef) && e2ControlTaskID.MatchString(v.EngineTaskID) && e2ControlHash.MatchString(v.PolicyFingerprintSHA256)
}
func validE2ControlFinal(v E2ControlFinal) bool {
	if v.Reason != "" && !validE2ControlCloseReason(v.Reason) {
		return false
	}
	if v.State == "no_execution" {
		return v.ExecutionRef == "" && v.ManifestSHA256 == "" && v.Checkpoint == nil
	}
	if v.ExecutionRef != "" && !validE2ControlRef(v.ExecutionRef) {
		return false
	}
	if v.State == "complete" {
		if v.Reason != "" && v.Reason != "complete" && v.Reason != "closed" && v.Reason != "revoked" && v.Reason != "expired" {
			return false
		}
		return validE2ControlRef(v.ExecutionRef) && e2ControlHash.MatchString(v.ManifestSHA256) && v.Checkpoint != nil && validE2ControlCheckpoint(v.Checkpoint)
	}
	if v.State == "incomplete" {
		return v.Reason != "" && v.Checkpoint == nil && (v.ManifestSHA256 == "" || e2ControlHash.MatchString(v.ManifestSHA256))
	}
	return false
}
func validE2ControlCloseReason(reason string) bool {
	switch e2proxy.Reason(reason) {
	case e2proxy.Complete, e2proxy.Unsupported, e2proxy.PolicyDenied, e2proxy.Closed, e2proxy.AuditFailed, e2proxy.Expired, e2proxy.Revoked, e2proxy.BudgetExhausted, e2proxy.TransportFailed, e2proxy.Unknown, e2proxy.AttemptLimit, e2proxy.DenialLimit, e2proxy.TransientLimit, e2proxy.UnknownLimit, e2proxy.Invalid:
		return true
	}
	return reason == "drain_timeout" || reason == "audit_incomplete" || reason == "publish_failed"
}
