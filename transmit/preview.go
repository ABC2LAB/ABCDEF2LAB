package transmit

import (
	"net/http"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
)

// PreviewStatus describes registration matching only. It never authorizes a send.
type PreviewStatus string

const (
	PreviewRegistrationMatch    PreviewStatus = "registration_match"
	PreviewRegistrationRejected PreviewStatus = "registration_rejected"
	PreviewUnavailable          PreviewStatus = "unavailable"
)

// PreviewReason is a closed set of static registration failures.
type PreviewReason string

const (
	PreviewUnavailableReason PreviewReason = "preview_unavailable"
)

// PreviewResult contains only references recovered from this transmitter's
// immutable registry. Method is GET or UNSUPPORTED, never caller-supplied text.
type PreviewResult struct {
	Status      PreviewStatus
	Reason      PreviewReason
	Method      string
	ResourceRef string
	AccountRef  string
	ApprovalRef string
}

// Preview compares a proposal with the immutable registration snapshot. It
// deliberately ignores current execution gates, time, budgets and audit state.
// A real send must submit the proposal again through SendDetailed.
func (t *HTTPTransmitter) Preview(proposal idor.AccessRequest) PreviewResult {
	result := PreviewResult{Status: PreviewUnavailable, Reason: PreviewUnavailableReason, Method: "UNSUPPORTED"}
	if t == nil || t.ctx == nil || t.client == nil || t.recorder == nil || t.sessions == nil || t.resources == nil {
		return result
	}
	if proposal.Method == http.MethodGet {
		result.Method = http.MethodGet
	}
	resource, knownResource := t.resources[proposal.ResourceRef]
	if knownResource {
		result.ResourceRef = resource.resource.Ref
		result.ApprovalRef = resource.resource.ApprovalRef
	}
	session, knownAccount := t.sessions[proposal.As.Label]
	if knownAccount {
		result.AccountRef = session.Account.Label
	}
	if reason := staticRegistrationReason(proposal, resource, knownResource, session, knownAccount); reason != "" {
		result.Status = PreviewRegistrationRejected
		result.Reason = PreviewReason(reason)
		return result
	}
	result.Status = PreviewRegistrationMatch
	result.Reason = ""
	return result
}

// staticRegistrationReason is shared with SendDetailed so preview cannot widen
// registration matching or reorder its refusal reasons.
func staticRegistrationReason(req idor.AccessRequest, resource binding, knownResource bool, session Session, knownAccount bool) Reason {
	switch {
	case req.Method != http.MethodGet || req.StateChanging:
		return UnsupportedAction
	case !knownResource:
		return UnknownResource
	case !knownAccount:
		return UnknownAccount
	case !resource.resource.ReadOnly || !resource.accounts[session.Account.Label]:
		return AccountNotApproved
	case req.As.Handle != session.Account.Handle || req.As.Status != session.Account.Status || session.Account.Status != oracle.SubjectVerified:
		return UnverifiedAccount
	default:
		return ""
	}
}
