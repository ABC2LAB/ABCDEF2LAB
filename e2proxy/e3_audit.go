package e2proxy

import (
	"regexp"

	"github.com/Autumn-27/artex/e3contract"
	"github.com/Autumn-27/artex/oracle"
)

// E3Binding은 서버가 확인한 제품 확정 실행과 동결 정책의 비민감 참조다.
// 값의 진위와 현재성은 호스트가 검증해야 하며, 이 구조 검사가 권한을 발급하지 않는다.
type E3Binding struct {
	JobRef        string                  `json:"job_ref"`
	GenerationRef string                  `json:"generation_ref"`
	InstanceRef   string                  `json:"instance_ref"`
	Policy        e3contract.PolicySource `json:"policy"`
}

// E3Audit은 e2-http-v3 E③ 프로그램 시도에만 있는 구조화 감사 필드다.
// Receipt에 익명 포인터로 넣어 기존 v1/v2 JSON에는 새 칸이 나타나지 않게 한다.
// 표식·객체 ID·주체 ID·자격·본문 원문은 여기에 넣지 않는다.
type E3Audit struct {
	Role              e3contract.Role          `json:"role"`
	AccountLabel      string                   `json:"account_label"`
	AccountBindingRef string                   `json:"account_binding_ref"`
	E3                E3Binding                `json:"e3_binding"`
	ResourceType      string                   `json:"resource_type,omitempty"`
	CreateRef         e3contract.CallRef       `json:"create_ref,omitzero"`
	NotSentReason     e3contract.NotSentReason `json:"not_sent_reason,omitempty"`
	TimedOut          bool                     `json:"timed_out,omitempty"`
	Normal            oracle.CheckState        `json:"normal,omitempty"`
	RequestMarkerFree *bool                    `json:"request_marker_free,omitempty"`
	Marker            e3contract.MarkerCheck   `json:"marker,omitzero"`
	BodyDigestSHA256  string                   `json:"body_digest_sha256,omitempty"`
	BodyComplete      *bool                    `json:"body_complete,omitempty"`
	ObjectIDDigest    string                   `json:"object_id_digest,omitempty"`
	SubjectDigest     string                   `json:"subject_digest,omitempty"`
}

var e3ResourceTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validE3AuditEvent(event Event) bool {
	a := event.E3Audit
	if a == nil || event.IntentID != 0 || event.CommandRef != "" ||
		(event.Stage != "intent" && event.Stage != "result") ||
		(a.Role != e3contract.RoleOwner && a.Role != e3contract.RoleCross) ||
		!validJournalRef(a.AccountLabel) || !validJournalRef(a.AccountBindingRef) ||
		!validJournalRef(a.E3.JobRef) || !validJournalRef(a.E3.GenerationRef) ||
		!validJournalRef(a.E3.InstanceRef) || a.E3.Policy.ValidateProduct() != nil {
		return false
	}
	binding := e3contract.RunBinding{JobRef: a.E3.JobRef, RunRef: event.Scope.RunRef,
		TaskRef: event.Scope.TaskRef, E2PolicyRef: event.Scope.PolicyRef,
		GenerationRef: a.E3.GenerationRef, InstanceRef: a.E3.InstanceRef, Policy: a.E3.Policy}
	if binding.Validate() != nil {
		return false
	}
	switch event.Program {
	case ProgramE3Identity:
		if event.Method != "GET" || a.ResourceType != "" || a.CreateRef != (e3contract.CallRef{}) {
			return false
		}
	case ProgramE3Create:
		if event.Method != "POST" && event.Method != "PUT" || a.Role != e3contract.RoleOwner ||
			!e3ResourceTypePattern.MatchString(a.ResourceType) || a.CreateRef != (e3contract.CallRef{}) {
			return false
		}
	case ProgramE3Read:
		if event.Method != "GET" || !e3ResourceTypePattern.MatchString(a.ResourceType) ||
			!validJournalRef(a.CreateRef.CallRef) ||
			!validJournalHex(a.CreateRef.AttemptID, randomRefBytes) || a.CreateRef.Sequence == 0 {
			return false
		}
	default:
		return false
	}
	if event.Stage == "intent" {
		return event.Decision == "allowed" && event.RuleRef != "" &&
			!event.ForwardAttempted && !event.RequestWritten && !event.ResponseReceived &&
			!event.ResponseComplete && event.StatusCode == 0 && a.NotSentReason == "" &&
			!a.TimedOut && a.Normal == "" && a.RequestMarkerFree == nil &&
			a.Marker == (e3contract.MarkerCheck{}) && a.BodyDigestSHA256 == "" &&
			a.BodyComplete == nil && a.ObjectIDDigest == "" && a.SubjectDigest == ""
	}
	// 허용한 intent 뒤 실행 문맥이 끝난 경우다. 완료 관찰로 사용하지 않는다.
	if event.Decision == "allowed" && event.NotSent && !event.ForwardAttempted {
		return event.Reason == Closed && a.NotSentReason == "" &&
			!event.RequestWritten && !event.ResponseReceived && !event.ResponseComplete &&
			event.StatusCode == 0 && !a.TimedOut && a.Normal == "" &&
			a.RequestMarkerFree == nil && a.Marker == (e3contract.MarkerCheck{}) &&
			a.BodyDigestSHA256 == "" && a.BodyComplete == nil &&
			a.ObjectIDDigest == "" && a.SubjectDigest == ""
	}
	if event.Decision == "allowed" && (!event.ForwardAttempted || event.NotSent || a.NotSentReason != "") {
		return false
	}
	if event.Decision != "allowed" && (!event.NotSent || event.ForwardAttempted ||
		a.NotSentReason == "") {
		return false
	}
	if event.ResponseComplete {
		if !event.RequestWritten || !event.ResponseReceived || a.Normal == "" ||
			a.BodyDigestSHA256 == "" || a.BodyComplete == nil {
			return false
		}
		if event.Program == ProgramE3Create && a.RequestMarkerFree != nil ||
			event.Program != ProgramE3Create && a.RequestMarkerFree == nil ||
			event.Program == ProgramE3Identity && a.Normal == oracle.StateYes && a.SubjectDigest == "" {
			return false
		}
	} else if a.RequestMarkerFree != nil || a.BodyComplete != nil {
		return false
	}
	observation := e3contract.Observation{Binding: binding, Purpose: e3contract.Purpose(event.Program),
		Role: a.Role, AccountLabel: a.AccountLabel, ResourceType: a.ResourceType,
		Ref:    e3contract.CallRef{CallRef: event.CallRef, AttemptID: event.AttemptID, Sequence: event.Sequence},
		Create: a.CreateRef, At: event.At, Sent: event.ForwardAttempted,
		NotSent: a.NotSentReason, ResponseComplete: event.ResponseComplete, TimedOut: a.TimedOut,
		StatusCode: event.StatusCode, Normal: a.Normal, RequestMarkerFree: boolValue(a.RequestMarkerFree),
		Marker: a.Marker, BodyDigestSHA256: a.BodyDigestSHA256, BodyComplete: boolValue(a.BodyComplete),
		ObjectIDDigest: a.ObjectIDDigest, SubjectDigest: a.SubjectDigest}
	return observation.Validate() == nil
}

func boolValue(value *bool) bool { return value != nil && *value }
