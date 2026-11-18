package e3contract

import (
	"errors"
	"fmt"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

// Purpose는 E² 프로그램 요청의 목적이다. 인증 준비(auth_prep·refresh_session)처럼 목적 규칙으로만 열리고,
// 모델 요청의 규칙 찾기에서는 빠진다(#596 §7).
type Purpose string

const (
	PurposeCreate   Purpose = "e3_create"
	PurposeRead     Purpose = "e3_read"
	PurposeIdentity Purpose = "e3_identity"
)

// Role은 요청에 붙인 시험 계정의 역할이다. A가 owner, B가 cross다.
type Role string

const (
	RoleOwner Role = "owner"
	RoleCross Role = "cross"
)

// MarkerStatus는 E² 안 표식 검사의 상태다. 검사하지 못한 것을 "표식 없음"으로 기록하지 않는다.
type MarkerStatus string

const (
	MarkerComplete    MarkerStatus = "complete"
	MarkerPartial     MarkerStatus = "partial"
	MarkerUnsupported MarkerStatus = "unsupported"
	MarkerFailed      MarkerStatus = "failed"
)

// MarkerPresence는 검사가 끝났을(complete) 때만 값이 있다.
type MarkerPresence string

const (
	MarkerPresent MarkerPresence = "present"
	MarkerAbsent  MarkerPresence = "absent"
)

// MatchMethod는 표식이 어떤 방식으로 맞았는지다(#596 §6 정규화기). 한 번만 풀고, 지원 밖이면 MarkerUnsupported다.
type MatchMethod string

const (
	MatchExact              MatchMethod = "exact"
	MatchCaseInsensitive    MatchMethod = "case_insensitive"
	MatchDecodedJSON        MatchMethod = "decoded_json"
	MatchDecodedURL         MatchMethod = "decoded_url"
	MatchDecodedHTMLNumeric MatchMethod = "decoded_html_numeric"
	MatchDecodedHTMLNamed   MatchMethod = "decoded_html_named"
)

// NotSentReason은 E²가 요청을 보내지 않은 것이 확실할 때의 사유다. 보냈는지 모르면 이 칸을 쓰지 않는다.
type NotSentReason string

const (
	NotSentApprovalDenied  NotSentReason = "approval_denied"
	NotSentApprovalTimeout NotSentReason = "approval_timeout"
	// NotSentBudgetExhausted는 실행 시작 때 떼어 둔 확정 단계 전용 몫(시간·요청)이 다 된 경우다.
	NotSentBudgetExhausted NotSentReason = "confirmation_budget_exhausted"
	NotSentPolicyDenied    NotSentReason = "policy_denied"
)

// CallRef는 E² 영수증·저널 이벤트를 가리키는 비민감 참조다.
type CallRef struct {
	CallRef   string `json:"call_ref"`
	AttemptID string `json:"attempt_id"`
	Sequence  uint64 `json:"sequence"`
}

func (c CallRef) valid() bool {
	return refPattern.MatchString(c.CallRef) && refPattern.MatchString(c.AttemptID) && c.Sequence > 0
}

// MarkerCheck는 조회 응답 하나의 표식 검사 결과다. 표식 원값·본문 원문은 없다.
type MarkerCheck struct {
	Status   MarkerStatus   `json:"status,omitempty"`
	Presence MarkerPresence `json:"presence,omitempty"`
	Method   MatchMethod    `json:"method,omitempty"`
	// Field는 검사한 선언 칸(소유자 전용 필드)이다.
	Field string `json:"field,omitempty"`
}

// Observation은 E² 프로그램 요청 하나의 구조화 관찰이다. E³는 이것만 받는다. URL·본문·표식·객체 ID·사용자 식별자 원문은
// 없다. 같은 값이 E² 저널의 봉인된 이벤트에도 있어야 한다(Journal.Sealed).
type Observation struct {
	Binding RunBinding `json:"binding"`
	Purpose Purpose    `json:"purpose"`
	Role    Role       `json:"role"`
	// AccountLabel은 E²가 실제로 붙인 C14 시험 계정 라벨이다. 모델이 쓴 라벨은 근거가 아니다.
	AccountLabel string  `json:"account_label"`
	ResourceType string  `json:"resource_type,omitempty"`
	Ref          CallRef `json:"ref"`
	// Sent는 E²가 대상으로 요청을 쓰기 시작했는지다. 보내지 않은 것이 확실하면 false와 NotSent 사유다.
	Sent    bool          `json:"sent"`
	NotSent NotSentReason `json:"not_sent,omitempty"`
	// ResponseComplete는 응답 전체를 받았는지다. Sent인데 false면 결과 불명이다(다시 보내지 않는다).
	ResponseComplete bool `json:"response_complete"`
	// TimedOut은 확정 요청 전용 시간 상한(요청의 Timeout)이 지나 E²가 끊었는지다. 보낸 뒤 결과 불명일 때만 참일 수 있다.
	TimedOut   bool `json:"timed_out,omitempty"`
	StatusCode int  `json:"status_code,omitempty"`
	// Normal은 선언한 상태 칸(없으면 E²의 HTTP 상태 판정)으로 본 정상 응답 여부다.
	Normal oracle.CheckState `json:"normal,omitempty"`
	// RequestMarkerFree는 조회·신원 요청에서 E²가 보낸 요청(URL·헤더·본문)에 표식이 없음을 확인했는지다.
	RequestMarkerFree bool        `json:"request_marker_free"`
	Marker            MarkerCheck `json:"marker"`
	// BodyDigestSHA256·BodyComplete는 본문 digest와 그것이 전체 본문인지다. 부분 digest를 전체 근거로 쓰지 않는다.
	BodyDigestSHA256 string `json:"body_digest_sha256,omitempty"`
	BodyComplete     bool   `json:"body_complete"`
	// ObjectIDDigest는 생성 응답에서 꺼낸 객체 ID의 실행별 키 digest다(생성만).
	ObjectIDDigest string `json:"object_id_digest,omitempty"`
	// Create는 조회가 읽은 생성 관찰이다(조회만). ID 원문은 E² 안에서만 경로에 넣는다.
	Create CallRef `json:"create,omitempty"`
	// SubjectDigest는 신원 응답의 사용자 식별자를 실행별 키로 만든 digest다(신원만).
	SubjectDigest string    `json:"subject_digest,omitempty"`
	At            time.Time `json:"at"`
}

// ErrObservation은 관찰이 계약 모양이 아닌 경우다. 진위 실패로 다룬다.
var ErrObservation = errors.New("e3contract: 관찰이 계약 모양이 아니다")

// Validate는 관찰 하나의 모양과 칸 사이 불변식을 본다.
func (o Observation) Validate() error {
	bad := func(field string) error { return fmt.Errorf("%w: %s", ErrObservation, field) }
	if err := o.Binding.Validate(); err != nil {
		return bad("binding")
	}
	if o.Role != RoleOwner && o.Role != RoleCross {
		return bad("role")
	}
	if !refPattern.MatchString(o.AccountLabel) {
		return bad("account_label")
	}
	if !o.Ref.valid() {
		return bad("ref")
	}
	if o.At.IsZero() {
		return bad("at")
	}
	if err := o.validateResult(); err != nil {
		return err
	}
	switch o.Purpose {
	case PurposeIdentity:
		if o.ResourceType != "" || o.ObjectIDDigest != "" || o.Create != (CallRef{}) || o.Marker != (MarkerCheck{}) {
			return bad("identity_fields")
		}
		if o.SubjectDigest != "" && !digestPattern.MatchString(o.SubjectDigest) {
			return bad("subject_digest")
		}
	case PurposeCreate:
		if o.Role != RoleOwner {
			return bad("create_role")
		}
		if !typePattern.MatchString(o.ResourceType) || o.SubjectDigest != "" || o.Create != (CallRef{}) ||
			o.Marker != (MarkerCheck{}) {
			return bad("create_fields")
		}
		if o.ObjectIDDigest != "" && !digestPattern.MatchString(o.ObjectIDDigest) {
			return bad("object_id_digest")
		}
	case PurposeRead:
		if !typePattern.MatchString(o.ResourceType) || o.SubjectDigest != "" || o.ObjectIDDigest != "" || !o.Create.valid() {
			return bad("read_fields")
		}
		if err := o.validateMarker(); err != nil {
			return err
		}
	default:
		return bad("purpose")
	}
	return nil
}

func (o Observation) validateResult() error {
	bad := func(field string) error { return fmt.Errorf("%w: %s", ErrObservation, field) }
	if o.TimedOut && (!o.Sent || o.ResponseComplete) {
		return bad("timed_out")
	}
	if !o.Sent {
		switch o.NotSent {
		case NotSentApprovalDenied, NotSentApprovalTimeout, NotSentBudgetExhausted, NotSentPolicyDenied:
		default:
			return bad("not_sent")
		}
		if o.ResponseComplete || o.StatusCode != 0 || o.Normal != "" || o.BodyDigestSHA256 != "" || o.BodyComplete ||
			o.ObjectIDDigest != "" || o.SubjectDigest != "" || o.Marker != (MarkerCheck{}) || o.RequestMarkerFree {
			return bad("not_sent_has_result")
		}
		return nil
	}
	if o.NotSent != "" {
		return bad("sent_with_not_sent")
	}
	if !o.ResponseComplete {
		// 결과 불명: 응답에서 나온 칸이 있으면 안 된다.
		if o.StatusCode != 0 || o.Normal != "" || o.BodyDigestSHA256 != "" || o.BodyComplete || o.ObjectIDDigest != "" ||
			o.SubjectDigest != "" || o.Marker != (MarkerCheck{}) {
			return bad("unknown_has_result")
		}
		return nil
	}
	if o.StatusCode < 100 || o.StatusCode > 599 {
		return bad("status_code")
	}
	if o.Normal != oracle.StateYes && o.Normal != oracle.StateNo && o.Normal != oracle.StateUnknown {
		return bad("normal")
	}
	if !digestPattern.MatchString(o.BodyDigestSHA256) {
		return bad("body_digest_sha256")
	}
	return nil
}

func (o Observation) validateMarker() error {
	bad := func(field string) error { return fmt.Errorf("%w: marker.%s", ErrObservation, field) }
	m := o.Marker
	if !o.Sent || !o.ResponseComplete {
		return nil // validateResult가 빈 칸을 이미 확인했다.
	}
	switch m.Status {
	case MarkerComplete:
		if m.Presence != MarkerPresent && m.Presence != MarkerAbsent {
			return bad("presence")
		}
		// 전체 본문을 검사한 것만 complete다.
		if !o.BodyComplete {
			return bad("complete_without_full_body")
		}
	case MarkerPartial, MarkerUnsupported, MarkerFailed:
		// 검사하지 못한 것을 "표식 없음"(또는 있음)으로 적지 않는다.
		if m.Presence != "" {
			return bad("presence_without_complete")
		}
	default:
		return bad("status")
	}
	if !fieldPathPattern.MatchString(m.Field) {
		return bad("field")
	}
	switch {
	case m.Presence == MarkerPresent:
		switch m.Method {
		case MatchExact, MatchCaseInsensitive, MatchDecodedJSON, MatchDecodedURL, MatchDecodedHTMLNumeric, MatchDecodedHTMLNamed:
		default:
			return bad("method")
		}
	case m.Method != "":
		return bad("method_without_presence")
	}
	return nil
}

// Same은 두 관찰이 같은지다. 시각은 순간으로 비교한다(저널 왕복에서 위치·단조 시계 정보가 빠질 수 있다).
func (o Observation) Same(p Observation) bool {
	a, b := o, p
	a.At, b.At = time.Time{}, time.Time{}
	return a == b && o.At.Equal(p.At)
}
