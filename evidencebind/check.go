package evidencebind

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

const maxHTTPStatus = 599

var reference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)

func validRef(ref string) bool {
	return reference.MatchString(ref) && !strings.Contains(ref, "://")
}

// Check is pure: it compares trusted host inputs and recomputes a verdict from
// the given facts. It cannot prove that those facts came from the referenced
// captures; the host must preserve that association at execution time.
func Check(expected Context, input Input) Outcome {
	if !validContext(expected) {
		return refused(MissingContext)
	}
	if input.Policy.Context != expected || input.Owner.Context != expected || input.Cross.Context != expected || input.Result.Context != expected {
		return refused(ContextMismatch)
	}
	for _, read := range []struct {
		observation Observation
		account     idor.AccountRef
		role        ReadRole
	}{
		{input.Owner, expected.Owner, OwnerRead},
		{input.Cross, expected.Evaluated, CrossRead},
	} {
		if reason := checkObservation(expected, read.observation, read.account, read.role); reason != Bound {
			return refused(reason)
		}
	}
	if input.Owner.Receipt.Attempt >= input.Cross.Receipt.Attempt || input.Cross.Receipt.AdmittedAt.Before(input.Owner.Receipt.At) {
		return refused(ReceiptMismatch)
	}
	if input.Owner.Evidence.ID == input.Cross.Evidence.ID || input.Result.OwnerEvidence != input.Owner.Evidence || input.Result.CrossEvidence != input.Cross.Evidence {
		return refused(EvidenceRefMismatch)
	}
	if !input.Result.ObservedAt.Equal(input.Cross.ObservedAt) {
		return refused(ObservationMismatch)
	}
	facts := input.Result.Result.Facts
	// A bounded HTTP error can have a Completed receipt, but E② never classifies
	// its body as normal. Do not accept a contradictory YES supplied in facts.
	if facts.OwnerNormalData == oracle.StateYes && !normalStatus(input.Owner.Receipt.StatusCode) ||
		facts.CrossNormalData == oracle.StateYes && !normalStatus(input.Cross.Receipt.StatusCode) {
		return refused(VerdictMismatch)
	}
	verdict := oracle.Evaluate(oracle.CaseInput{
		Policy: input.Policy.Assertion,
		Subjects: oracle.SubjectContext{
			OwnerRef: expected.Owner.Label, OwnerStatus: expected.Owner.Status,
			EvaluatedRef: expected.Evaluated.Label, EvaluatedStatus: expected.Evaluated.Status,
		},
		Facts: facts,
	}, input.Cross.ObservedAt)
	provided := input.Result.Result.Verdict
	if provided.Value != verdict.Value || provided.PolicyCheck != verdict.PolicyCheck ||
		provided.IdentityCheck != verdict.IdentityCheck || provided.DataCheck != verdict.DataCheck ||
		!slices.Equal(provided.ReasonCodes, verdict.ReasonCodes) {
		return refused(VerdictMismatch)
	}
	return Outcome{BindingValid: true, BindingReason: Bound, EffectiveValue: verdict.Value, Recomputed: verdict}
}

func validContext(ctx Context) bool {
	for _, ref := range []string{ctx.Scope.RunRef, ctx.Scope.TaskRef, ctx.Scope.CaseRef, ctx.Scope.ResetRef, ctx.Scope.PolicyRef,
		ctx.ResourceRef, ctx.ReadApprovalRef, ctx.KGPolicyVersion, ctx.Owner.Label, ctx.Owner.Handle, ctx.Evaluated.Label, ctx.Evaluated.Handle} {
		if !validRef(ref) {
			return false
		}
	}
	if ctx.IntentID <= 0 || ctx.Owner.Label == ctx.Evaluated.Label || ctx.Owner.Handle == ctx.Evaluated.Handle {
		return false
	}
	for _, status := range []oracle.SubjectStatus{ctx.Owner.Status, ctx.Evaluated.Status} {
		switch status {
		case oracle.SubjectVerified, oracle.SubjectUnknown, oracle.SubjectExpired, oracle.SubjectMismatch:
		default:
			return false
		}
	}
	return true
}

func checkObservation(expected Context, observation Observation, account idor.AccountRef, role ReadRole) Reason {
	receipt := observation.Receipt
	if receipt.Scope != expected.Scope || receipt.AccountRef != account.Label || receipt.SessionRef != account.Handle ||
		receipt.ResourceRef != expected.ResourceRef || receipt.ApprovalRef != expected.ReadApprovalRef ||
		receipt.Attempt == 0 || receipt.Method != http.MethodGet {
		return ReceiptMismatch
	}
	if receipt.Reason != transmit.Completed || !receipt.Admitted || !receipt.RequestWritten || !receipt.ResponseReceived {
		return ExecutionIncomplete
	}
	if receipt.StatusCode < http.StatusContinue || receipt.StatusCode > maxHTTPStatus {
		return ReceiptMismatch
	}
	if !validTime(observation.ObservedAt) || !validTime(receipt.AdmittedAt) || !validTime(receipt.At) ||
		observation.ObservedAt.Before(receipt.AdmittedAt) || receipt.At.Before(observation.ObservedAt) {
		return ObservationMismatch
	}
	evidence := observation.Evidence
	if !validRef(evidence.ID) || evidence.IntentID != expected.IntentID || evidence.Scope != receipt.Scope || evidence.Attempt != receipt.Attempt || evidence.Role != role {
		return EvidenceRefMismatch
	}
	return Bound
}

func normalStatus(code int) bool { return code >= http.StatusOK && code < http.StatusMultipleChoices }

func validTime(at time.Time) bool { return !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999 }

func refused(reason Reason) Outcome {
	return Outcome{BindingReason: reason, EffectiveValue: oracle.Undecidable}
}
