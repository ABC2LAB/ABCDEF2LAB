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
	// AccountAuth는 시험 계정별 인증 준비 결과다(계약 §3.4, C14). completed·cancelled·failed, 계정을 결속한 실행만.
	AccountAuth map[string]string `json:"account_auth,omitempty"`
	// ReasonCode는 failed·unknown 실행의 고정 사유 코드다(계약 docs/contracts/run-outcome-states.md §3.1). 실행이 이미 분류한
	// 사유(진단 로그 run_finished와 같은 값)만 싣고 원문 오류는 싣지 않는다. 모르면 없다.
	ReasonCode string `json:"reason_code,omitempty"`
}

// 실행 상태 사유 코드다(진단 준비 Job의 reason_code와 같은 형식).
var reasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

const (
	// ReasonClosureUnconfirmed는 실행 마감(감사·결과 저장)을 확인하지 못해 결과 불명인 실행이다.
	ReasonClosureUnconfirmed = "execution_closure_unconfirmed"
	// ReasonResultUnconfirmed는 마감은 확인했지만 실행 결과가 없거나 계약에 맞지 않아 결과 불명인 실행이다(절대 규칙 5).
	ReasonResultUnconfirmed = "execution_result_unconfirmed"
)

// reasonError는 Runner가 돌려준 실패에 고정 사유 코드를 싣는다. 호출자에게는 감싼 원래 오류로 보인다.
type reasonError struct {
	code string
	err  error
}

func (e reasonError) Error() string { return e.err.Error() }
func (e reasonError) Unwrap() error { return e.err }

// WithReason은 실행 실패 오류에 고정 사유 코드를 싣는다. 오류가 없거나 코드 형식이 틀리면 원래 오류를 그대로 돌려준다.
func WithReason(err error, code string) error {
	if err == nil || !reasonCodePattern.MatchString(code) {
		return err
	}
	return reasonError{code: code, err: err}
}

// ReasonOf는 오류에 실린 고정 사유 코드다. 없으면 빈 문자열이다.
func ReasonOf(err error) string {
	var r reasonError
	if errors.As(err, &r) {
		return r.code
	}
	return ""
}

// 시험 계정 인증 준비 결과 값이다(계약 §3.4). 자격·세션 값은 없다.
// not_attempted(시도 없음)·unconfirmed(성공 확인 안 됨)는 모델 직접 로그인 회차의 값이다(docs/contracts/model-login-attempts.md §4).
var accountAuthResults = map[string]bool{"authenticated": true, "login_failed": true, "session_not_found": true, "result_unknown": true, "not_sent": true, "not_attempted": true, "unconfirmed": true}

// accountLabel은 대상 계정 등록 라벨 규칙이다(diagnosisprep 등록 규칙과 같음, 사용자명 원문이 아님).
var accountLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// MaxAccountAuth는 실행 하나에 결속하는 계정 수 상한이다(대상 계정 상한과 같음).
const MaxAccountAuth = 16

// ValidAccountAuth는 계정별 인증 결과 맵의 형식이다: 1…16개, 라벨은 등록 라벨 규칙, 값은 결과 값뿐.
func ValidAccountAuth(m map[string]string) bool {
	if len(m) < 1 || len(m) > MaxAccountAuth {
		return false
	}
	for label, result := range m {
		if !accountLabel.MatchString(label) || !accountAuthResults[result] {
			return false
		}
	}
	return true
}

// AccountAuthState는 이 칸이 실릴 수 있는 실행 상태다.
func AccountAuthState(s State) bool { return s == Completed || s == Cancelled || s == Failed }

// CloneAccountAuth는 형식에 맞는 맵의 사본이다. 비었거나 형식이 틀리면 nil이다(싣지 않는다).
func CloneAccountAuth(m map[string]string) map[string]string {
	if !ValidAccountAuth(m) {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
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
	// GoalMet은 목표 달성으로 작업이 끝나 취소됐거나 시작하지 않은 intent다(#875 발견 6). 미완료가 아니라 partial에
	// 넣지 않는다. 이 칸이 생기기 전 기록에는 없어 0으로 읽는다.
	GoalMet int64 `json:"goal_met"`
}

// Total은 마감한 intent 수(모든 결과 칸의 합)다.
func (c IntentOutcomes) Total() int64 {
	return c.Done + c.PolicyLimited + c.Incomplete + c.Unknown + c.Stopped + c.GoalMet
}

func (c IntentOutcomes) Valid() bool {
	return c.Done >= 0 && c.PolicyLimited >= 0 && c.Incomplete >= 0 && c.Unknown >= 0 && c.Stopped >= 0 && c.GoalMet >= 0
}

// Outcome은 계약 §3.2의 대조식이다. 목표 달성으로 중단된 intent는 남은 작업이 아니라서 partial로 만들지 않는다.
func (c IntentOutcomes) Outcome() string {
	switch {
	case c.Incomplete+c.Unknown+c.Stopped > 0:
		return OutcomePartial
	case c.Done+c.PolicyLimited+c.GoalMet > 0:
		return OutcomeComplete
	default:
		return OutcomeNoIntents
	}
}

// RunResult는 감사가 닫힌 실행(completed)의 결과다. 중지된 실행(cancelled)에는 intent 결과 개수만 싣는다
// (outcome·closing_reason 없음, 계약 §3.2).
type RunResult struct {
	Outcome        string         `json:"outcome,omitempty"`
	ClosingReason  string         `json:"closing_reason,omitempty"`
	IntentOutcomes IntentOutcomes `json:"intent_outcomes"`
}

func (r RunResult) Valid() bool {
	return r.IntentOutcomes.Valid() && r.Outcome == r.IntentOutcomes.Outcome() && (r.ClosingReason == "" || r.ClosingReason == ClosingBudgetLimited)
}

// StoppedValid는 중지된 실행(cancelled)에 싣는 결과 규칙이다. intent 결과 개수만 있고 outcome·closing_reason은 없다.
func (r RunResult) StoppedValid() bool {
	return r.Outcome == "" && r.ClosingReason == "" && r.IntentOutcomes.Valid()
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
	if s.AccountAuth != nil && (!AccountAuthState(s.State) || !ValidAccountAuth(s.AccountAuth)) {
		return false
	}
	if s.ReasonCode != "" && (s.State != Failed && s.State != Unknown || !reasonCodePattern.MatchString(s.ReasonCode)) {
		return false
	}
	switch s.State {
	case Prepared:
		return !s.Started && !s.DrainComplete && !s.ResultReady && s.Result == nil
	case Running, Stopping:
		return s.Started && !s.DrainComplete && !s.ResultReady && s.Result == nil
	case Completed:
		return s.Started && s.DrainComplete && s.ResultReady && s.Result != nil && s.Result.Valid()
	case Failed:
		return s.Started && s.DrainComplete && s.ResultReady && s.Result == nil
	case Cancelled:
		return s.Started && s.DrainComplete && s.ResultReady && (s.Result == nil || s.Result.StoppedValid())
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
