package server

import (
	"context"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

// BindControlledReadRealInputs checks the consistency of already authenticated
// host inputs for one owner/cross read. approved must be the same immutable,
// execution-scoped config used by E2, not a reconstructed model/client DTO.
// This is neither an authentication adapter nor a complete Config validator.
// Missing real providers must remain unavailable in the caller.
//
// The caller supplies the actual KG reader and C's final SourceReader unchanged.
// This function reads neither source nor KG, fills no collector/revision/audit
// fields, and neither configures a server nor grants permission to send/store.
// E2 must still validate Config and enforce its current gates on each send.
func BindControlledReadRealInputs(ctx context.Context, request ControlledReadVerificationRequest,
	expected evidencebind.Context, approved transmit.Config, kg idor.PolicyResolver,
	source e3record.SourceReader) (ControlledReadFindingPlan, error) {
	refuse := func() (ControlledReadFindingPlan, error) {
		return ControlledReadFindingPlan{}, ErrControlledFindingUnavailable
	}
	if ctx == nil || ctx.Err() != nil || nilPolicyResolver(kg) || source == nil ||
		request.TaskID <= 0 || request.IntentID <= 0 ||
		request.Scope.TaskRef != strconv.FormatInt(request.TaskID, 10) ||
		request.Scope.RunRef == "" || request.Scope.CaseRef == "" ||
		request.Scope.ResetRef == "" || request.Scope.PolicyRef == "" ||
		expected.Scope != request.Scope || approved.Scope != request.Scope ||
		expected.IntentID != request.IntentID || expected.ResourceRef == "" ||
		expected.ResourceRef != request.ResourceRef || expected.ReadApprovalRef == "" ||
		expected.KGPolicyVersion == "" || expected.Evaluated.Label != request.AccountRef {
		return refuse()
	}
	owner, evaluated := expected.Owner, expected.Evaluated
	if owner.Label == "" || owner.Handle == "" || evaluated.Label == "" || evaluated.Handle == "" ||
		owner.Label == evaluated.Label || owner.Handle == evaluated.Handle ||
		owner.Status != oracle.SubjectVerified || evaluated.Status != oracle.SubjectVerified {
		return refuse()
	}
	// Expiry is only checked at this instant; the real transmitter must recheck
	// time/revocation/budget immediately before every actual request.
	now := time.Now()
	if approved.ExpiresAt.IsZero() || !now.Before(approved.ExpiresAt) {
		return refuse()
	}
	for _, subject := range []idor.AccountRef{owner, evaluated} {
		matches := 0
		for _, session := range approved.Sessions {
			if session.Account.Label != subject.Label {
				if session.Account.Handle == subject.Handle {
					return refuse()
				}
				continue
			}
			if session.Account != subject || session.ExpiresAt.IsZero() || !now.Before(session.ExpiresAt) {
				return refuse()
			}
			matches++
		}
		if matches != 1 {
			return refuse()
		}
	}
	matches := 0
	for _, resource := range approved.Resources {
		if resource.Ref != expected.ResourceRef {
			continue
		}
		if !resource.ReadOnly || resource.ApprovalRef != expected.ReadApprovalRef {
			return refuse()
		}
		ownerAllowed, evaluatedAllowed := false, false
		for _, account := range resource.Accounts {
			ownerAllowed = ownerAllowed || account == owner.Label
			evaluatedAllowed = evaluatedAllowed || account == evaluated.Label
		}
		if !ownerAllowed || !evaluatedAllowed {
			return refuse()
		}
		matches++
	}
	if matches != 1 || ctx.Err() != nil {
		return refuse()
	}
	return ControlledReadFindingPlan{Expected: expected,
		Candidate: idor.Candidate{Owner: owner, Evaluated: evaluated, ResourceRef: expected.ResourceRef},
		KG:        kg, Source: source}, nil
}
