// Package exploration은 탐색 실행기와 호스트 소유 경계의 세션 결속만 정의한다.
// 승인 origin·IP·TLS·메서드를 발급하거나 E② 진단 권위를 재사용하지 않는다.
package exploration

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"time"
)

const Version = "jindo.exploration-session/v1"
const ReadOnly = "read_only"
const ApprovedWrites = "approved_writes"
const FullLocal = "full_local"
const ExternalAllowed = "external_allowed"

var ErrUnavailable = errors.New("exploration_boundary_unavailable")
var ErrBinding = errors.New("exploration_binding_invalid")
var ErrClosed = errors.New("exploration_session_closed")
var ref = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type Limits struct {
	TimeBudgetSeconds   int64  `json:"time_budget_seconds"`
	MaxRequests         uint64 `json:"max_requests"`
	MaxExternalRequests uint64 `json:"max_external_requests"`
	MaxConcurrency      int    `json:"max_concurrency"`
	MaxResponseBytes    int64  `json:"max_response_bytes"`
}

// Binding은 보호된 target 저장소의 참조만 전달한다. 실제 승인 범위는 경계가 직접 읽는다.
type Binding struct {
	SessionRef     string `json:"session_ref"`
	RunRef         string `json:"run_ref"`
	JobID          string `json:"job_id"`
	TaskID         string `json:"task_id"`
	TargetRef      string `json:"target_ref"`
	TargetRevision uint64 `json:"target_revision"`
	WriteMode      string `json:"write_mode"`
	CommMode       string `json:"comm_mode"`
	Limits         Limits `json:"limits"`
}

func (b Binding) Validate() error {
	for _, s := range []string{b.SessionRef, b.RunRef, b.JobID, b.TargetRef} {
		if !ref.MatchString(s) {
			return ErrBinding
		}
	}
	task, e := strconv.ParseInt(b.TaskID, 10, 64)
	if e != nil || task <= 0 || strconv.FormatInt(task, 10) != b.TaskID || b.TargetRevision == 0 || (b.WriteMode != ReadOnly && b.WriteMode != ApprovedWrites) || (b.CommMode != FullLocal && b.CommMode != ExternalAllowed) || b.Limits.TimeBudgetSeconds <= 0 || b.Limits.TimeBudgetSeconds > math.MaxInt64/int64(time.Second) || b.Limits.MaxRequests == 0 || b.Limits.MaxConcurrency <= 0 || uint64(b.Limits.MaxConcurrency) > b.Limits.MaxRequests || b.Limits.MaxResponseBytes <= 0 {
		return ErrBinding
	}
	if b.CommMode == FullLocal && b.Limits.MaxExternalRequests != 0 {
		return ErrBinding
	}
	return nil
}

type Reason string

const (
	Completed           Reason = "completed"
	Cancelled           Reason = "cancelled"
	TargetEnded         Reason = "target_ended"
	CapExhausted        Reason = "cap_exhausted"
	BoundaryUnavailable Reason = "boundary_unavailable"
)

func (r Reason) Valid() bool {
	return r == Completed || r == Cancelled || r == TargetEnded || r == CapExhausted || r == BoundaryUnavailable
}

type State struct {
	Binding           Binding `json:"binding"`
	Active            bool    `json:"active"`
	ClosedReason      Reason  `json:"closed_reason,omitempty"`
	TargetRequests    uint64  `json:"target_requests"`
	ExternalRequests  uint64  `json:"external_requests"`
	ActiveConnections int     `json:"active_connections"`
}

func (s State) Validate(b Binding) error {
	if b.Validate() != nil || s.Binding != b || s.ActiveConnections < 0 || s.ActiveConnections > b.Limits.MaxConcurrency || s.TargetRequests > b.Limits.MaxRequests || s.ExternalRequests > b.Limits.MaxExternalRequests || (s.Active && s.ClosedReason != "") || (!s.Active && (!s.ClosedReason.Valid() || s.ActiveConnections != 0)) {
		return ErrBinding
	}
	return nil
}

// Boundary의 구현은 서버만 만들며, 모델 입력을 이 인터페이스로 디코딩하지 않는다.
// 실제 강제 프록시·네트워크 배치가 없는 경우 Open은 ErrUnavailable을 반환해야 한다.
type Boundary interface {
	Open(context.Context, Binding) (Lease, error)
}
type Lease interface {
	Status(context.Context) (State, error)
	Close(context.Context, Reason) (State, error)
}
