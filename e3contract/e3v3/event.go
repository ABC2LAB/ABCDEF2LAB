// Package e3v3는 E③ 확정 단계(e3contract)와 E② 영수증·저널 v3 사이의 어댑터다(E③↔E② v3 계약,
// docs/contracts/e3-e2-v3-journal-contract.md). E²가 내놓는 목적별 프로그램 요청과 검증된 v3 이벤트 조회(E2)를
// e3contract.Transport·Journal로 바꾼다. 저널을 만들거나 MAC을 계산하지 않고, E³ 판정을 하지 않는다.
package e3v3

import (
	"fmt"
	"time"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

// Format은 v3 영수증·저널 형식 판이다(계약 §2).
const Format = "e2-http-v3"

// Stage는 v3 시도 이벤트 단계다(v2와 같은 이름, 계약 §2.3). 송신 전 허용은 intent, 그 뒤 결과가 result다. 송신 전에
// 거부하면 intent 없이 result 하나(not_sent)다. control·call_closed는 시도 밖 관리 행이라 관찰에 쓰지 않는다.
type Stage string

const (
	StageIntent Stage = "intent"
	StageResult Stage = "result"
)

// 시도 판정(decision) 값.
const (
	DecisionAllowed     = "allowed"
	DecisionDenied      = "denied"
	DecisionUnsupported = "unsupported"
)

// Scope는 E² 실행 범위다(transmit.Scope의 RunRef·TaskRef·PolicyRef).
type Scope struct {
	RunRef    string `json:"run_ref"`
	TaskRef   string `json:"task_ref"`
	PolicyRef string `json:"policy_ref"`
}

// Binding은 v3 이벤트의 e3_binding이다(계약 §2.2). 실행·세대·인스턴스와 동결한 정책 출처다.
type Binding struct {
	JobRef        string                  `json:"job_ref"`
	GenerationRef string                  `json:"generation_ref"`
	InstanceRef   string                  `json:"instance_ref"`
	Policy        e3contract.PolicySource `json:"policy"`
}

// Event는 E③ 프로그램 호출 하나의 마지막 v3 시도 이벤트(result)다. E²가 MAC·체인·신뢰
// 체크포인트로 검증한 기록에서 나온다. URL·본문·표식·객체 ID·사용자 식별자·자격 원문은 없다(계약 §2.2 "넣지 않는 것").
type Event struct {
	Version string `json:"version"`
	Index   uint64 `json:"index"`
	Stage   Stage  `json:"stage"`
	// Program은 E③ 셋 중 하나다(기존 Receipt.Program 재사용). E③ 프로그램 호출은 intent_id가 0이다.
	Program  e3contract.Purpose `json:"program"`
	IntentID int64              `json:"intent_id"`
	Scope    Scope              `json:"scope"`

	CallRef        string `json:"call_ref"`
	AttemptID      string `json:"attempt_id"`
	Sequence       uint64 `json:"sequence"`
	PolicyRevision string `json:"policy_revision"`
	RuleRef        string `json:"rule_ref,omitempty"`
	Method         string `json:"method"`
	Decision       string `json:"decision"`
	Reason         string `json:"reason"`

	ForwardAttempted bool                     `json:"forward_attempted"`
	RequestWritten   bool                     `json:"request_written"`
	ResponseReceived bool                     `json:"response_received"`
	ResponseComplete bool                     `json:"response_complete"`
	NotSent          bool                     `json:"not_sent"`
	NotSentReason    e3contract.NotSentReason `json:"not_sent_reason,omitempty"`
	AuditComplete    bool                     `json:"audit_complete"`
	StatusCode       int                      `json:"status_code,omitempty"`
	At               time.Time                `json:"at"`

	Role              e3contract.Role        `json:"role"`
	AccountLabel      string                 `json:"account_label"`
	AccountBindingRef string                 `json:"account_binding_ref"`
	E3                Binding                `json:"e3_binding"`
	ResourceType      string                 `json:"resource_type,omitempty"`
	CreateRef         e3contract.CallRef     `json:"create_ref,omitzero"`
	TimedOut          bool                   `json:"timed_out,omitempty"`
	Normal            oracle.CheckState      `json:"normal,omitempty"`
	RequestMarkerFree bool                   `json:"request_marker_free"`
	Marker            e3contract.MarkerCheck `json:"marker,omitzero"`
	BodyDigestSHA256  string                 `json:"body_digest_sha256,omitempty"`
	BodyComplete      bool                   `json:"body_complete"`
	ObjectIDDigest    string                 `json:"object_id_digest,omitempty"`
	SubjectDigest     string                 `json:"subject_digest,omitempty"`
}

// ReasonClosed는 v2·v3에서 intent를 기록한 뒤 실행 문맥이 끝나 연결을 시도하지 않고 마감한 result의 사유다.
const ReasonClosed = "closed"

// StoppedAfterIntent는 intent 뒤 송신 전 중단 행인지다(계약 §2.3): decision=allowed·not_sent=true·forward_attempted=false·
// reason=closed, not_sent_reason과 응답 칸 없음.
func (e Event) StoppedAfterIntent() bool {
	return e.Stage == StageResult && e.Decision == DecisionAllowed && e.NotSent && e.NotSentReason == "" &&
		!e.ForwardAttempted && !e.RequestWritten && !e.ResponseReceived && !e.ResponseComplete && e.Reason == ReasonClosed &&
		e.StatusCode == 0 && e.Normal == "" && !e.RequestMarkerFree && e.Marker == (e3contract.MarkerCheck{}) &&
		e.BodyDigestSHA256 == "" && !e.BodyComplete && e.ObjectIDDigest == "" && e.SubjectDigest == "" && !e.TimedOut
}

// Ref는 이벤트가 가리키는 호출이다.
func (e Event) Ref() e3contract.CallRef {
	return e3contract.CallRef{CallRef: e.CallRef, AttemptID: e.AttemptID, Sequence: e.Sequence}
}

// RunBinding은 이벤트의 scope와 e3_binding으로 확정 단계 결속을 다시 만든다(계약 §4). 확정 단계가 요청한 결속과의
// 대조는 e3contract.Run이 한다(binding_mismatch).
func (e Event) RunBinding() e3contract.RunBinding {
	return e3contract.RunBinding{JobRef: e.E3.JobRef, RunRef: e.Scope.RunRef, TaskRef: e.Scope.TaskRef, E2PolicyRef: e.Scope.PolicyRef,
		GenerationRef: e.E3.GenerationRef, InstanceRef: e.E3.InstanceRef, Policy: e.E3.Policy}
}

// Check는 계약 §2.3의 단계별 필수 조건을 본다. 어기면 e3contract.ErrObservation을 감싼 오류다(진위 실패). 칸 사이의
// 깊은 불변식(응답 칸, 표식 검사)은 Observation.Validate가 다시 본다.
func (e Event) Check() error {
	bad := func(field string) error { return fmt.Errorf("%w: v3 %s", e3contract.ErrObservation, field) }
	if e.Version != Format {
		return bad("version")
	}
	switch e.Program {
	case e3contract.PurposeIdentity, e3contract.PurposeCreate, e3contract.PurposeRead:
	default:
		return bad("program")
	}
	if e.IntentID != 0 {
		return bad("intent_id")
	}
	if e.Scope.RunRef == "" || e.Scope.TaskRef == "" || e.Scope.PolicyRef == "" {
		return bad("scope")
	}
	if e.CallRef == "" || e.AttemptID == "" || e.Sequence == 0 {
		return bad("call_ref")
	}
	if e.PolicyRevision == "" || e.Method == "" || e.Reason == "" {
		return bad("decision")
	}
	switch e.Decision {
	case DecisionAllowed:
		if e.RuleRef == "" {
			return bad("rule_ref")
		}
	case DecisionDenied, DecisionUnsupported:
		// 허용하지 않은 시도는 보내지 않은 것이다(v2 validJournalEvent와 같음).
		if !e.NotSent {
			return bad("decision")
		}
	default:
		return bad("decision")
	}
	if e.Role == "" || e.AccountLabel == "" || e.AccountBindingRef == "" {
		return bad("account")
	}
	if e.E3.JobRef == "" || e.E3.GenerationRef == "" || e.E3.InstanceRef == "" || e.E3.Policy.Validate() != nil {
		return bad("e3_binding")
	}
	switch e.Program {
	case e3contract.PurposeIdentity:
		if e.ResourceType != "" || e.CreateRef != (e3contract.CallRef{}) {
			return bad("identity_fields")
		}
	case e3contract.PurposeCreate:
		if e.ResourceType == "" || e.CreateRef != (e3contract.CallRef{}) {
			return bad("create_fields")
		}
	case e3contract.PurposeRead:
		if e.ResourceType == "" || e.CreateRef.CallRef == "" || e.CreateRef.AttemptID == "" || e.CreateRef.Sequence == 0 {
			return bad("read_fields")
		}
	}
	switch e.Stage {
	case StageIntent:
		// 송신 전 허용은 호출의 마지막 이벤트가 될 수 없다. 뒤에 result가 있어야 한다.
		return bad("intent_without_result")
	case StageResult:
	default:
		return bad("stage")
	}
	if e.NotSent {
		// 송신 전 거부: 연결을 시도하기 전에 거부한 것만 not_sent다(사유 넷).
		switch e.NotSentReason {
		case e3contract.NotSentPolicyDenied, e3contract.NotSentApprovalDenied, e3contract.NotSentApprovalTimeout,
			e3contract.NotSentBudgetExhausted:
		default:
			return bad("not_sent_reason")
		}
		if e.ForwardAttempted || e.RequestWritten || e.ResponseReceived || e.ResponseComplete || e.TimedOut ||
			e.StatusCode != 0 || e.Normal != "" || e.RequestMarkerFree || e.Marker != (e3contract.MarkerCheck{}) ||
			e.BodyDigestSHA256 != "" || e.BodyComplete || e.ObjectIDDigest != "" || e.SubjectDigest != "" {
			return bad("not_sent_has_response")
		}
		return nil
	}
	// 송신 뒤: 연결을 시도한 뒤의 기록이다. 결과를 확실히 모르면 response_complete=false(결과 불명)이고 not_sent가 아니다.
	if e.Decision != DecisionAllowed || !e.ForwardAttempted || e.NotSentReason != "" {
		return bad("result_not_sent")
	}
	if e.ResponseComplete && (!e.RequestWritten || !e.ResponseReceived) {
		return bad("result_response")
	}
	return nil
}

// Observation은 이벤트를 확정 단계 관찰로 옮긴다(계약 §2.2 표). Check를 먼저 통과해야 하고, 옮긴 관찰도
// Observation.Validate를 통과해야 한다. 어느 쪽이든 어기면 e3contract.ErrObservation을 감싼 오류다.
func (e Event) Observation() (e3contract.Observation, error) {
	if err := e.Check(); err != nil {
		return e3contract.Observation{}, err
	}
	o := e3contract.Observation{Binding: e.RunBinding(), Purpose: e.Program, Role: e.Role, AccountLabel: e.AccountLabel,
		ResourceType: e.ResourceType, Ref: e.Ref(), Create: e.CreateRef, At: e.At}
	if e.NotSent {
		o.NotSent = e.NotSentReason
	} else {
		o.Sent, o.ResponseComplete, o.TimedOut = true, e.ResponseComplete, e.TimedOut
		o.StatusCode, o.Normal, o.RequestMarkerFree, o.Marker = e.StatusCode, e.Normal, e.RequestMarkerFree, e.Marker
		o.BodyDigestSHA256, o.BodyComplete, o.ObjectIDDigest, o.SubjectDigest = e.BodyDigestSHA256, e.BodyComplete, e.ObjectIDDigest, e.SubjectDigest
	}
	if err := o.Validate(); err != nil {
		return e3contract.Observation{}, err
	}
	return o, nil
}
