// Package diagnosis는 API와 신뢰 실행 프로세스 사이의 최소 계약이다.
// Planner·Worker·HTTP·DB 타입과 비밀정보는 이 경계를 넘지 않는다.
package diagnosis

import (
	"context"
	"errors"
	"regexp"
	"strconv"
)

// v2: 완료 Snapshot에 실행 결과(Result)를 싣는다(docs/contracts/run-outcome-states.md).
const Version = "diagnosis.v2"
const MaxFrameBytes = 64 << 10

type Command string

const (
	Status   Command = "status"
	Validate Command = "validate"
	Start    Command = "start"
	Stop     Command = "stop"
	Result   Command = "result"
)

type State string

const (
	Prepared  State = "prepared"
	Running   State = "running"
	Stopping  State = "stopping"
	Completed State = "completed"
	Cancelled State = "cancelled"
	Failed    State = "failed"
	Unknown   State = "unknown"
)

type Request struct {
	Version        string  `json:"version"`
	RequestID      string  `json:"request_id"`
	Command        Command `json:"command"`
	TaskID         string  `json:"task_id"`
	PreparationRef string  `json:"preparation_ref"`
	IdempotencyKey string  `json:"idempotency_key,omitempty"`
}
type Snapshot struct {
	TaskID         string     `json:"task_id"`
	PreparationRef string     `json:"preparation_ref"`
	State          State      `json:"state"`
	Started        bool       `json:"started"`
	DrainComplete  bool       `json:"drain_complete"`
	ResultReady    bool       `json:"result_ready"`
	Result         *RunResult `json:"result,omitempty"`
}

// 실행 결과 값(docs/contracts/run-outcome-states.md §3.2). 실행이 끝났다는 것은 취약점이 없다는 뜻이 아니다.
const (
	OutcomeComplete      = "complete"
	OutcomePartial       = "partial"
	OutcomeNoIntents     = "no_intents"
	ClosingBudgetLimited = "budget_limited"
)

// IntentOutcomes는 intent 결과 개수다(계약 §2의 그래프 상태 투영).
type IntentOutcomes struct {
	Done          int64 `json:"done"`
	PolicyLimited int64 `json:"policy_limited"`
	Incomplete    int64 `json:"incomplete"`
	Unknown       int64 `json:"unknown"`
	Stopped       int64 `json:"stopped"`
}

func (c IntentOutcomes) Valid() bool {
	return c.Done >= 0 && c.PolicyLimited >= 0 && c.Incomplete >= 0 && c.Unknown >= 0 && c.Stopped >= 0
}

// Outcome은 계약 §3.2의 대조식이다.
func (c IntentOutcomes) Outcome() string {
	switch {
	case c.Incomplete+c.Unknown+c.Stopped > 0:
		return OutcomePartial
	case c.Done+c.PolicyLimited > 0:
		return OutcomeComplete
	default:
		return OutcomeNoIntents
	}
}

// RunResult는 감사가 닫힌 실행(completed)의 결과다.
type RunResult struct {
	Outcome        string         `json:"outcome"`
	ClosingReason  string         `json:"closing_reason,omitempty"`
	IntentOutcomes IntentOutcomes `json:"intent_outcomes"`
}

func (r RunResult) Valid() bool {
	return r.IntentOutcomes.Valid() && r.Outcome == r.IntentOutcomes.Outcome() && (r.ClosingReason == "" || r.ClosingReason == ClosingBudgetLimited)
}

type Response struct {
	Version   string    `json:"version"`
	RequestID string    `json:"request_id"`
	Snapshot  *Snapshot `json:"snapshot,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Gateway는 사용자 인증·task 권한을 대신하지 않는다. API가 먼저 검사해야 한다.
type Gateway interface {
	Call(context.Context, Request) (Response, error)
}

var ErrProtocol = errors.New("진단 실행 계약을 확인할 수 없습니다")
var ErrUnavailable = errors.New("진단 실행 서비스의 응답을 확인할 수 없습니다")
var reference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validTask(id string) bool {
	n, e := strconv.ParseInt(id, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == id
}
func validRequest(r Request) bool {
	if r.Version != Version || !reference.MatchString(r.RequestID) || !validTask(r.TaskID) || !reference.MatchString(r.PreparationRef) {
		return false
	}
	switch r.Command {
	case Status, Validate, Stop, Result:
		return r.IdempotencyKey == ""
	case Start:
		return reference.MatchString(r.IdempotencyKey)
	default:
		return false
	}
}
func errorResponse(r Request, code string) Response {
	return Response{Version: Version, RequestID: r.RequestID, Error: code}
}

// validSnapshot은 상대 구현의 상태 문자열을 그대로 UI 상태로 승격하지 않는다.
func validSnapshot(s Snapshot) bool {
	if !validTask(s.TaskID) || !reference.MatchString(s.PreparationRef) {
		return false
	}
	switch s.State {
	case Prepared:
		return !s.Started && !s.DrainComplete && !s.ResultReady
	case Running, Stopping:
		return s.Started && !s.DrainComplete && !s.ResultReady
	case Completed:
		return s.Started && s.DrainComplete && s.ResultReady && s.Result != nil && s.Result.Valid()
	case Failed, Cancelled:
		return s.Started && s.DrainComplete && s.ResultReady && s.Result == nil
	case Unknown:
		return s.Started && s.Result == nil
	default:
		return false
	}
}
func validError(code string) bool {
	switch code {
	case "invalid_request", "binding_mismatch", "service_closed", "already_started", "not_prepared", "not_started", "result_not_ready":
		return true
	default:
		return false
	}
}
