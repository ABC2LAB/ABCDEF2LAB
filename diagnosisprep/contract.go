// Package diagnosisprep는 URL 초안과 신뢰 준비 프로세스 사이의 최소 계약이다.
// 공개 입력은 실행 권위가 아니며 엔진·DB·보호 파일 타입은 이 경계를 넘지 않는다.
package diagnosisprep

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/diagnosis"
	"github.com/Autumn-27/artex/diagnosisflow"
	"time"
)

const Version = "diagnosis-preparation.v1"
const MaxFrameBytes = 64 << 10

type Command string

const (
	Catalog       Command = "catalog"
	Create        Command = "create"
	Lookup        Command = "lookup"
	Validate      Command = "validate"
	Start         Command = "start"
	Status        Command = "status"
	Stop          Command = "stop"
	AuthorizeTask Command = "authorize_task"
)

type State string

const (
	AwaitingApproval State = "awaiting_approval"
	Ready            State = "ready"
	Preparing        State = "preparing"
	Running          State = "running"
	Stopping         State = "stopping"
	Completed        State = "completed"
	Cancelled        State = "cancelled"
	Failed           State = "failed"
	Unknown          State = "unknown"
)

type Phase string

const (
	DraftPhase     Phase = "draft"
	ScopeCheck     Phase = "scope_check"
	TaskCreation   Phase = "task_creation"
	Planning       Phase = "planning"
	Authority      Phase = "authority"
	Gateway        Phase = "gateway"
	ExecutionPhase Phase = "execution"
	Closure        Phase = "closure"
	Report         Phase = "report"
)

type AuthorityState string

const (
	AuthorityNotIssued AuthorityState = "not_issued"
	AuthorityIssued    AuthorityState = "issued"
	AuthorityConsumed  AuthorityState = "consumed"
	AuthorityUnknown   AuthorityState = "unknown"
)

type Input struct {
	WorkflowMode  string `json:"workflow_mode,omitempty"`
	ExcludedPaths string `json:"excluded_paths,omitempty"`
	TargetURL     string `json:"target_url"`
	ProfileID     string `json:"profile_id"`
	DisplayName   string `json:"display_name,omitempty"`
	// ExternalTestAccounts는 외부 API 모델 실행에 시험 계정 세션을 모델 입력으로 보내는 것에 대해 사전 공지·승인을 받았다는
	// 운영자 진술이다(CLAUDE.md 규칙 2, C14 결정 4). 참이면 외부 API 실행도 시험 계정을 결속·로그인한다. 로컬 모델 실행은
	// 이 값과 상관없이 시험 계정을 쓴다. 결속을 바꾸므로 초안 입력(원본 요약)에 들어간다.
	ExternalTestAccounts bool `json:"external_test_accounts,omitempty"`
}

type Profile struct {
	// WorkflowRequestLimit은 설치의 회차당 요청 상한이다. 이름과 달리 제품 설치 프로필이면 워크플로 여부와 상관없이 싣는다.
	WorkflowRequestLimit int64                 `json:"workflow_request_limit,omitempty"`
	Workflow             *diagnosisflow.Limits `json:"workflow,omitempty"`
	ID                   string                `json:"id"`
	Label                string                `json:"label"`
	ModelProfileRef      string                `json:"model_profile_ref"`
	ModelLabel           string                `json:"model_label"`
	Model                *ModelInfo            `json:"model,omitempty"`
}

// 공개 경계에는 설치된 모델의 비밀 없는 라우팅 사실만 전달한다.
type ModelInfo struct {
	Mode             string `json:"mode"`
	Model            string `json:"model"`
	Provider         string `json:"provider"`
	ExternalTransfer bool   `json:"external_transfer"`
	InputPerCall     int64  `json:"input_tokens_per_call"`
	OutputPerCall    int64  `json:"output_tokens_per_call"`
}

// 요청·토큰은 실제 송신 전 예약 상한이다. 로컬 모델 비용은 0이며 유료 fallback은 없다.
type Budget struct {
	Requests        int64 `json:"requests"`
	ModelRequests   int64 `json:"model_requests"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CostUSDMicros   int64 `json:"cost_usd_micros"`
	DurationSeconds int64 `json:"duration_seconds"`
}

// prefix는 자신과 하위 경로를 포함한다. query/body의 실제 E② 범위를 축소해 표시하지 않는다.
// 이 projection에는 연결 IP·보호 경로·키·승인 원문을 넣지 않는다.
type Scope struct {
	TargetRef      string     `json:"target_ref"`
	ScopeRef       string     `json:"scope_ref"`
	Origin         string     `json:"origin"`
	PathPrefixes   []string   `json:"path_prefixes"`
	Methods        []string   `json:"methods"`
	QueryPolicy    string     `json:"query_policy"`
	BodyPolicy     string     `json:"body_policy"`
	MaxBodyBytes   int64      `json:"max_body_bytes"`
	ApprovalMode   string     `json:"approval_mode"`
	AttestationRef string     `json:"attestation_ref"`
	Budget         Budget     `json:"budget"`
	ExpiresAt      time.Time  `json:"expires_at"`
	Model          *ModelInfo `json:"model,omitempty"`
}

type ModelUsage struct {
	Requests              int64 `json:"requests"`
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReservedInputTokens   int64 `json:"reserved_input_tokens"`
	ReservedOutputTokens  int64 `json:"reserved_output_tokens"`
	CostUSDMicros         int64 `json:"cost_usd_micros"`
	ReservedCostUSDMicros int64 `json:"reserved_cost_usd_micros,omitempty"`
	// UsageKnown은 모든 호출이 실제 값 또는 예약 상한으로 정산됐다는 뜻이다. 상한으로 정산한 호출 수는
	// UnknownUsageCalls이고, 0보다 크면 입력·출력·비용은 실제 사용의 상한이다(P3 결정안 C05).
	UsageKnown        bool  `json:"usage_known"`
	UnknownUsageCalls int64 `json:"unknown_usage_calls,omitempty"`
}

// 준비 단계에서 결과가 불명이면 Started=false일 수 있다.
// diagnosis.v1 실행 snapshot과 달리 이 상태는 모델 준비의 불명도 나타낸다.
// 세 가지 known fact는 독립이며 unknown을 성공·미실행으로 승격하지 않는다.
type Job struct {
	ID                 string         `json:"id"`
	Revision           uint64         `json:"revision"`
	SourceDigest       string         `json:"source_digest"`
	Input              Input          `json:"input"`
	State              State          `json:"state"`
	Phase              Phase          `json:"phase"`
	ReasonCode         string         `json:"reason_code,omitempty"`
	Scope              *Scope         `json:"scope,omitempty"`
	TaskID             string         `json:"task_id,omitempty"`
	PreparationStarted bool           `json:"preparation_started"`
	AuthorityState     AuthorityState `json:"authority_state"`
	Started            bool           `json:"started"`
	DrainComplete      bool           `json:"drain_complete"`
	ResultReady        bool           `json:"result_ready"`
	ModelUsage         *ModelUsage    `json:"model_usage,omitempty"`
	// 실행 결과(docs/contracts/run-outcome-states.md §3.2). outcome·closing_reason은 completed에만,
	// intent_outcomes는 completed·cancelled에만 있다. 이 칸이 생기기 전에 저장된 completed에는 없을 수 있다.
	Outcome        string                    `json:"outcome,omitempty"`
	ClosingReason  string                    `json:"closing_reason,omitempty"`
	IntentOutcomes *diagnosis.IntentOutcomes `json:"intent_outcomes,omitempty"`
	// 진단 로그(운영 기록) 일부가 남지 않았다는 경고다(C10). 감사·결과에는 영향이 없다. completed에만 있다.
	DiagnosticLogIncomplete bool `json:"diagnostic_log_incomplete,omitempty"`
	// 시험 계정별 인증 준비 결과다(계약 §3.4, C14). completed·cancelled·failed, 계정을 결속한 실행만. 자격·세션 값은 없다.
	AccountAuth map[string]string `json:"account_auth,omitempty"`
}

// Principal은 API가 기존 Bearer 인증으로 확인한 주체다. 브라우저 body에서는 받지 않는다.
// socket 주소·descriptor·파일 경로·환경 변수·명령·권위 자료는 요청 필드가 아니다.
type Request struct {
	Version                   string          `json:"version"`
	RequestID                 string          `json:"request_id"`
	Command                   Command         `json:"command"`
	Principal                 string          `json:"principal"`
	JobID                     string          `json:"job_id,omitempty"`
	ExpectedRevision          uint64          `json:"expected_revision,omitempty"`
	IdempotencyKey            string          `json:"idempotency_key,omitempty"`
	Input                     *Input          `json:"input,omitempty"`
	TaskID                    string          `json:"task_id,omitempty"`
	ExternalTransferConfirmed bool            `json:"external_transfer_confirmed,omitempty"`
	Product                   *ProductRequest `json:"product,omitempty"`
}

type Response struct {
	Version    string           `json:"version"`
	RequestID  string           `json:"request_id"`
	Profiles   []Profile        `json:"profiles,omitempty"`
	Job        *Job             `json:"job,omitempty"`
	Authorized *bool            `json:"authorized,omitempty"`
	Error      string           `json:"error,omitempty"`
	Product    *ProductResponse `json:"product,omitempty"`
}

// Endpoint는 이미 설치된 비공개 서비스 연결이다. Call은 변경 요청을 자동 재송신하지 않는다.
type Endpoint interface {
	Call(context.Context, Request) (Response, error)
}

var ErrProtocol = errors.New("진단 준비 계약을 확인할 수 없습니다")
var ErrUnavailable = errors.New("진단 준비 서비스의 응답을 확인할 수 없습니다")

// 아래 타입은 서비스 내부 조립용이며 JSON 전송이나 브라우저 응답에 넣지 않는다.
// SourceDigest는 정본 Input과 이 InstallationDigest를 함께 결속해야 한다.
// digest에는 위임 원문·정책 원문·manifest/helper/CA/audit pin을 포함한다.
type Inspection struct {
	Scope              *Scope
	ReasonCode         string
	InstallationDigest string
	DelegationRef      string
	MaxRuns            int64
	AggregateBudget    Budget
}

// Execution은 영속 start receipt와 위임 총량 예약을 끝낸 한 회차다.
// 같은 회차는 재시작 이후에도 Execute를 다시 호출하지 않는다.
type Execution struct {
	Principal  string
	Job        Job
	Inspection Inspection
}

// Update는 Job의 실행 projection 전체다. 사실을 되돌리는 갱신은 거부해야 한다.
// 부작용 시작 전 단계도 Publish가 내구성 있게 기록된 뒤에만 진행한다.
type Update struct {
	State              State
	Phase              Phase
	ReasonCode         string
	TaskID             string
	PreparationStarted bool
	AuthorityState     AuthorityState
	Started            bool
	DrainComplete      bool
	ResultReady        bool
	ModelUsage         *ModelUsage
	// Result는 진단 실행이 completed로 마감했을 때의 결과다. 다른 상태에서는 nil이어야 한다.
	Result *diagnosis.RunResult
	// DiagnosticLogIncomplete는 completed 실행의 진단 로그 경고다(reason_code와 따로).
	DiagnosticLogIncomplete bool
	// AccountAuth는 시험 계정별 인증 준비 결과다(completed·cancelled·failed에만 싣는다).
	AccountAuth map[string]string
}

// Publish 실패는 실행 허가가 아니다. 신규 부작용을 중단하고 기존 실행은 취소·drain한다.
type Publish func(Update) error

// Backend는 설치자가 조립한 신뢰 실행 구현이다. HTTP handler에서 호출하지 않는다.
// Profiles/Inspect는 대상 HTTP·DNS·모델 호출·권위 발급을 하지 않는다.
// Execute만 승인된 exact origin 대조 뒤 제한된 DNS 검사를 하고 실제 준비를 수행한다.
// Execute의 context 취소는 중지 요청이며 rollback 또는 drain 완료의 증거가 아니다.
// 반환 전 terminal Update를 기록해야 한다. 미확인 종료는 서비스가 unknown으로 보존한다.
// 서비스 재시작은 과거 Execute를 복원하지 않고 저장 receipt와 보고서만 조회한다.
type Backend interface {
	Profiles(context.Context, string) ([]Profile, error)
	Inspect(context.Context, string, Input) (Inspection, error)
	Execute(context.Context, Execution, Publish) error
}
