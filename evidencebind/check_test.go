package evidencebind

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/transmit"
)

var checkObservedAt = time.Date(2026, 10, 30, 12, 0, 20, 0, time.UTC)

func checkContext() Context {
	return Context{
		Scope:    transmit.Scope{RunRef: "work-1", TaskRef: "task-1", CaseRef: "case-1", ResetRef: "reset-1", PolicyRef: "e2-policy-1"},
		IntentID: 11, ResourceRef: "generic-document:one", ReadApprovalRef: "read-approval-1", KGPolicyVersion: "kg-policy-1",
		Owner:     idor.AccountRef{Label: "owner-a", Handle: "session-a", Status: oracle.SubjectVerified},
		Evaluated: idor.AccountRef{Label: "reader-b", Handle: "session-b", Status: oracle.SubjectVerified},
	}
}

func checkPolicy() oracle.PolicyAssertion {
	return oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
		HasRegisteredSource: true, ValidFrom: checkObservedAt.Add(-time.Hour), ValidUntil: checkObservedAt.Add(time.Hour), HintMatchesCanonical: true}
}

func checkReceipt(ctx Context, account idor.AccountRef, attempt uint64, admitted, at time.Time, status int) transmit.Receipt {
	return transmit.Receipt{Scope: ctx.Scope, Attempt: attempt, AccountRef: account.Label, SessionRef: account.Handle,
		ResourceRef: ctx.ResourceRef, ApprovalRef: ctx.ReadApprovalRef, Method: http.MethodGet, Reason: transmit.Completed,
		Admitted: true, AdmittedAt: admitted, RequestWritten: true, ResponseReceived: true, StatusCode: status, At: at}
}

func checkInput() Input {
	ctx := checkContext()
	ownerAdmitted := checkObservedAt.Add(-10 * time.Second)
	ownerObserved := checkObservedAt.Add(-9 * time.Second)
	ownerAt := checkObservedAt.Add(-8 * time.Second)
	crossAdmitted := checkObservedAt.Add(-5 * time.Second)
	crossObserved := checkObservedAt
	crossAt := checkObservedAt.Add(5 * time.Second)
	ownerReceipt := checkReceipt(ctx, ctx.Owner, 1, ownerAdmitted, ownerAt, http.StatusOK)
	crossReceipt := checkReceipt(ctx, ctx.Evaluated, 2, crossAdmitted, crossAt, http.StatusOK)
	owner := Observation{Context: ctx, Receipt: ownerReceipt, Evidence: EvidenceRef{ID: "ev-owner", IntentID: ctx.IntentID, Scope: ctx.Scope, Attempt: 1, Role: OwnerRead}, ObservedAt: ownerObserved}
	cross := Observation{Context: ctx, Receipt: crossReceipt, Evidence: EvidenceRef{ID: "ev-cross", IntentID: ctx.IntentID, Scope: ctx.Scope, Attempt: 2, Role: CrossRead}, ObservedAt: crossObserved}
	in := Input{Policy: PolicySnapshot{Context: ctx, Assertion: checkPolicy()}, Owner: owner, Cross: cross}
	in.Result = ResultSnapshot{Context: ctx, OwnerEvidence: owner.Evidence, CrossEvidence: cross.Evidence, ObservedAt: crossObserved,
		Result: idor.Result{Facts: oracle.ComparisonFacts{SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes, CrossNormalData: oracle.StateYes,
			NontrivialDataMatch: oracle.StateYes, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes}}}
	setCheckVerdict(&in)
	return in
}

func setCheckVerdict(in *Input) {
	in.Result.Result.Verdict = oracle.Evaluate(oracle.CaseInput{Policy: in.Policy.Assertion,
		Subjects: oracle.SubjectContext{OwnerRef: in.Result.Context.Owner.Label, OwnerStatus: in.Result.Context.Owner.Status,
			EvaluatedRef: in.Result.Context.Evaluated.Label, EvaluatedStatus: in.Result.Context.Evaluated.Status}, Facts: in.Result.Result.Facts}, in.Cross.ObservedAt)
}

func TestCheckValidBindingRecomputesVerdictAndReasons(t *testing.T) {
	in := checkInput()
	got := Check(checkContext(), in)
	want := in.Result.Result.Verdict
	if !got.BindingValid || got.BindingReason != Bound || got.EffectiveValue != oracle.Confirmed {
		t.Fatalf("valid input outcome = %+v", got)
	}
	if !reflect.DeepEqual(got.Recomputed, want) {
		t.Fatalf("recomputed verdict = %+v, want %+v", got.Recomputed, want)
	}
	if !reflect.DeepEqual(got.Recomputed.ReasonCodes, []oracle.JudgeReason{oracle.ReasonPolicyDenyResolved, oracle.ReasonSubjectsDistinctVerified, oracle.ReasonDataNontrivialMatch}) {
		t.Fatalf("reason order = %v", got.Recomputed.ReasonCodes)
	}
}

func TestCheckRejectsEvidenceFromAnotherIntentDespiteRestampedContext(t *testing.T) {
	in := checkInput()
	expected := checkContext()
	expected.IntentID++
	// These wrappers can be accidentally restamped when loading historical
	// results. The independently stored evidence association must still disagree.
	in.Policy.Context, in.Owner.Context = expected, expected
	in.Cross.Context, in.Result.Context = expected, expected
	got := Check(expected, in)
	if got.BindingValid || got.BindingReason != EvidenceRefMismatch || got.EffectiveValue != oracle.Undecidable {
		t.Fatalf("cross-intent evidence accepted: %+v", got)
	}
}

func TestCheckRejectsMissingOrForeignEvidenceIntent(t *testing.T) {
	for _, intentID := range []int64{0, checkContext().IntentID + 1} {
		in := checkInput()
		in.Cross.Evidence.IntentID = intentID
		in.Result.CrossEvidence = in.Cross.Evidence
		got := Check(checkContext(), in)
		if got.BindingValid || got.BindingReason != EvidenceRefMismatch || got.EffectiveValue != oracle.Undecidable {
			t.Fatalf("evidence intent %d accepted: %+v", intentID, got)
		}
	}
}

func TestCheckValidVerdictStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
		want   oracle.VerdictValue
	}{
		{name: "confirmed", want: oracle.Confirmed},
		{name: "suspected", mutate: func(in *Input) { in.Policy.Assertion.ExpectedAccess = oracle.AccessUnknown }, want: oracle.Suspected},
		{name: "allowed", mutate: func(in *Input) { in.Policy.Assertion.ExpectedAccess = oracle.AccessAllow }, want: oracle.Allowed},
		{name: "undecidable", mutate: func(in *Input) { in.Result.Result.Facts.CrossNormalData = oracle.StateUnknown }, want: oracle.Undecidable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := checkInput()
			if tt.mutate != nil {
				tt.mutate(&in)
			}
			setCheckVerdict(&in)
			got := Check(checkContext(), in)
			if !got.BindingValid || got.EffectiveValue != tt.want {
				t.Fatalf("outcome = %+v", got)
			}
		})
	}
}

func TestCheckRejectsProvidedVerdictTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*oracle.OracleVerdict)
	}{
		{name: "value", mutate: func(v *oracle.OracleVerdict) { v.Value = oracle.Suspected }},
		{name: "policy check", mutate: func(v *oracle.OracleVerdict) { v.PolicyCheck = oracle.StateUnknown }},
		{name: "identity check", mutate: func(v *oracle.OracleVerdict) { v.IdentityCheck = oracle.StateUnknown }},
		{name: "data check", mutate: func(v *oracle.OracleVerdict) { v.DataCheck = oracle.StateUnknown }},
		{name: "reason order", mutate: func(v *oracle.OracleVerdict) { v.ReasonCodes[0], v.ReasonCodes[1] = v.ReasonCodes[1], v.ReasonCodes[0] }},
		{name: "private reason", mutate: func(v *oracle.OracleVerdict) { v.ReasonCodes[0] = oracle.JudgeReason("private_reason") }},
		{name: "missing reason", mutate: func(v *oracle.OracleVerdict) { v.ReasonCodes = v.ReasonCodes[:2] }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := checkInput()
			tt.mutate(&in.Result.Result.Verdict)
			got := Check(checkContext(), in)
			if got.BindingValid || got.BindingReason != VerdictMismatch || got.EffectiveValue != oracle.Undecidable || got.Recomputed.ReasonCodes != nil {
				t.Fatalf("tampered verdict outcome = %+v", got)
			}
		})
	}
}

func TestCheckAllowedDoesNotRequireOwnerDataOrOwnerVerified(t *testing.T) {
	expected := checkContext()
	expected.Owner.Status = oracle.SubjectUnknown
	in := checkInput()
	in.Policy.Assertion.ExpectedAccess = oracle.AccessAllow
	in.Policy.Context = expected
	in.Owner.Context = expected
	in.Cross.Context = expected
	in.Result.Context = expected
	in.Result.Result.Facts = oracle.ComparisonFacts{}
	setCheckVerdict(&in)
	got := Check(expected, in)
	if !got.BindingValid || got.EffectiveValue != oracle.Allowed || got.Recomputed.PolicyCheck != oracle.StateNo {
		t.Fatalf("allowed with unknown owner outcome = %+v", got)
	}
}

func TestCheckRejectsBindingAndEvidenceMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(Context, *Input)
		want   Reason
	}{
		{name: "scope run ref", mutate: func(_ Context, in *Input) { in.Cross.Receipt.Scope.RunRef = "other-work" }, want: ReceiptMismatch},
		{name: "scope task ref", mutate: func(_ Context, in *Input) { in.Owner.Context.Scope.TaskRef = "other-task" }, want: ContextMismatch},
		{name: "scope case ref", mutate: func(_ Context, in *Input) { in.Cross.Context.Scope.CaseRef = "other-case" }, want: ContextMismatch},
		{name: "scope reset ref", mutate: func(_ Context, in *Input) { in.Result.Context.Scope.ResetRef = "other-reset" }, want: ContextMismatch},
		{name: "scope policy ref", mutate: func(_ Context, in *Input) { in.Policy.Context.Scope.PolicyRef = "kg-policy-1" }, want: ContextMismatch},
		{name: "kg policy version", mutate: func(_ Context, in *Input) { in.Policy.Context.KGPolicyVersion = "kg-policy-2" }, want: ContextMismatch},
		{name: "intent id", mutate: func(_ Context, in *Input) { in.Owner.Context.IntentID++ }, want: ContextMismatch},
		{name: "resource ref", mutate: func(_ Context, in *Input) { in.Cross.Receipt.ResourceRef = "basket:99" }, want: ReceiptMismatch},
		{name: "approval ref", mutate: func(_ Context, in *Input) { in.Owner.Receipt.ApprovalRef = "approval-other" }, want: ReceiptMismatch},
		{name: "wrong account", mutate: func(_ Context, in *Input) { in.Cross.Receipt.AccountRef = "owner-a" }, want: ReceiptMismatch},
		{name: "wrong session", mutate: func(_ Context, in *Input) { in.Cross.Receipt.SessionRef = "session-other" }, want: ReceiptMismatch},
		{name: "wrong method", mutate: func(_ Context, in *Input) { in.Cross.Receipt.Method = http.MethodPost }, want: ReceiptMismatch},
		{name: "incomplete", mutate: func(_ Context, in *Input) { in.Cross.Receipt.ResponseReceived = false }, want: ExecutionIncomplete},
		{name: "non-completed reason", mutate: func(_ Context, in *Input) { in.Cross.Receipt.Reason = transmit.TransportError }, want: ExecutionIncomplete},
		{name: "not admitted", mutate: func(_ Context, in *Input) { in.Cross.Receipt.Admitted = false }, want: ExecutionIncomplete},
		{name: "request not written", mutate: func(_ Context, in *Input) { in.Cross.Receipt.RequestWritten = false }, want: ExecutionIncomplete},
		{name: "invalid status", mutate: func(_ Context, in *Input) { in.Cross.Receipt.StatusCode = 0 }, want: ReceiptMismatch},
		{name: "owner not before cross", mutate: func(_ Context, in *Input) { in.Cross.Receipt.Attempt = 1; in.Cross.Evidence.Attempt = 1 }, want: ReceiptMismatch},
		{name: "cross admitted before owner receipt", mutate: func(_ Context, in *Input) { in.Cross.Receipt.AdmittedAt = in.Owner.Receipt.At.Add(-time.Nanosecond) }, want: ReceiptMismatch},
		{name: "zero observed time", mutate: func(_ Context, in *Input) { in.Cross.ObservedAt = time.Time{} }, want: ObservationMismatch},
		{name: "observed before admission", mutate: func(_ Context, in *Input) {
			in.Cross.ObservedAt = in.Cross.Receipt.AdmittedAt.Add(-time.Nanosecond)
			in.Result.ObservedAt = in.Cross.ObservedAt
		}, want: ObservationMismatch},
		{name: "observed after receipt", mutate: func(_ Context, in *Input) {
			in.Cross.ObservedAt = in.Cross.Receipt.At.Add(time.Nanosecond)
			in.Result.ObservedAt = in.Cross.ObservedAt
		}, want: ObservationMismatch},
		{name: "evidence role", mutate: func(_ Context, in *Input) { in.Cross.Evidence.Role = OwnerRead }, want: EvidenceRefMismatch},
		{name: "evidence scope", mutate: func(_ Context, in *Input) { in.Cross.Evidence.Scope.RunRef = "other-work" }, want: EvidenceRefMismatch},
		{name: "evidence attempt", mutate: func(_ Context, in *Input) { in.Cross.Evidence.Attempt = 3 }, want: EvidenceRefMismatch},
		{name: "empty evidence ref", mutate: func(_ Context, in *Input) { in.Cross.Evidence.ID = "" }, want: EvidenceRefMismatch},
		{name: "duplicate evidence id", mutate: func(_ Context, in *Input) { in.Cross.Evidence.ID = in.Owner.Evidence.ID }, want: EvidenceRefMismatch},
		{name: "result evidence exchange", mutate: func(_ Context, in *Input) {
			in.Result.OwnerEvidence, in.Result.CrossEvidence = in.Result.CrossEvidence, in.Result.OwnerEvidence
		}, want: EvidenceRefMismatch},
		{name: "result observation", mutate: func(_ Context, in *Input) { in.Result.ObservedAt = in.Owner.ObservedAt }, want: ObservationMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := checkContext()
			in := checkInput()
			tt.mutate(ctx, &in)
			got := Check(ctx, in)
			if got.BindingValid || got.BindingReason != tt.want || got.EffectiveValue != oracle.Undecidable || got.Recomputed.ReasonCodes != nil {
				t.Fatalf("outcome = %+v, want invalid %s", got, tt.want)
			}
		})
	}
}

func TestCheckRejectsZeroExpectedAndZeroInput(t *testing.T) {
	in := checkInput()
	if got := Check(Context{}, in); got.BindingValid || got.BindingReason != MissingContext || got.EffectiveValue != oracle.Undecidable || got.Recomputed.ReasonCodes != nil {
		t.Fatalf("zero expected context outcome = %+v", got)
	}
	if got := Check(checkContext(), Input{}); got.BindingValid || got.BindingReason != ContextMismatch || got.EffectiveValue != oracle.Undecidable || got.Recomputed.ReasonCodes != nil {
		t.Fatalf("zero input outcome = %+v", got)
	}
}

func TestCheckAllowsNonconsecutiveAttempts(t *testing.T) {
	in := checkInput()
	in.Cross.Receipt.Attempt = 3
	in.Cross.Evidence.Attempt = 3
	in.Result.CrossEvidence.Attempt = 3
	got := Check(checkContext(), in)
	if !got.BindingValid || got.EffectiveValue != oracle.Confirmed {
		t.Fatalf("nonconsecutive attempt outcome = %+v", got)
	}
}

func TestCheckPolicyExpiryUsesObservationTime(t *testing.T) {
	in := checkInput()
	in.Policy.Assertion.ValidUntil = in.Cross.ObservedAt
	setCheckVerdict(&in)
	got := Check(checkContext(), in)
	if !got.BindingValid || got.EffectiveValue != oracle.Undecidable || got.Recomputed.PolicyCheck != oracle.StateUnknown {
		t.Fatalf("expired-at-observation outcome = %+v", got)
	}
	if got.Recomputed.ReasonCodes[0] != oracle.ReasonExpectedAccessPolicyInvalid {
		t.Fatalf("policy reason = %v", got.Recomputed.ReasonCodes)
	}
}

func TestCheckAllowsCompletedBoundedHTTPErrorWithoutNormalClaim(t *testing.T) {
	in := checkInput()
	in.Cross.Receipt.StatusCode = http.StatusBadGateway
	in.Result.Result.Facts.CrossNormalData = oracle.StateUnknown
	setCheckVerdict(&in)
	got := Check(checkContext(), in)
	if !got.BindingValid || got.EffectiveValue != oracle.Undecidable {
		t.Fatalf("bounded error outcome = %+v", got)
	}

	in = checkInput()
	in.Cross.Receipt.StatusCode = http.StatusBadGateway
	setCheckVerdict(&in)
	got = Check(checkContext(), in)
	if got.BindingValid || got.BindingReason != VerdictMismatch {
		t.Fatalf("contradictory normal claim = %+v", got)
	}
}

func TestCheckDoesNotAliasProvidedReasonCodes(t *testing.T) {
	in := checkInput()
	provided := append([]oracle.JudgeReason(nil), in.Result.Result.Verdict.ReasonCodes...)
	in.Result.Result.Verdict.ReasonCodes = provided
	got := Check(checkContext(), in)
	provided[0] = "tampered"
	if got.Recomputed.ReasonCodes[0] == "tampered" {
		t.Fatal("recomputed reasons alias provided reasons")
	}
}
