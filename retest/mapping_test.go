package retest_test

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/retest"
	"github.com/Autumn-27/artex/transmit"
)

func fixture() (evidencebind.Context, evidencebind.Input) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ctx := evidencebind.Context{
		Scope:    transmit.Scope{RunRef: "work-one", TaskRef: "task-one", CaseRef: "case-one", ResetRef: "reset-one", PolicyRef: "execution-policy-one"},
		IntentID: 17, ResourceRef: "generic:document:one", ReadApprovalRef: "read-approval-one", KGPolicyVersion: "kg-policy-one",
		Owner:     idor.AccountRef{Label: "owner", Handle: "owner-session", Status: oracle.SubjectVerified},
		Evaluated: idor.AccountRef{Label: "reader", Handle: "reader-session", Status: oracle.SubjectVerified},
	}
	observation := func(account idor.AccountRef, attempt uint64, role evidencebind.ReadRole, observed time.Time) evidencebind.Observation {
		return evidencebind.Observation{
			Context: ctx, ObservedAt: observed,
			Receipt: transmit.Receipt{
				Scope: ctx.Scope, AccountRef: account.Label, SessionRef: account.Handle,
				ResourceRef: ctx.ResourceRef, ApprovalRef: ctx.ReadApprovalRef, Method: http.MethodGet,
				Attempt: attempt, Reason: transmit.Completed, Admitted: true, RequestWritten: true, ResponseReceived: true,
				AdmittedAt: observed.Add(-time.Second), At: observed.Add(time.Second), StatusCode: http.StatusOK,
			},
			Evidence: evidencebind.EvidenceRef{ID: fmt.Sprintf("evidence-%d", attempt), IntentID: ctx.IntentID, Scope: ctx.Scope, Attempt: attempt, Role: role},
		}
	}
	in := evidencebind.Input{
		Policy: evidencebind.PolicySnapshot{Context: ctx, Assertion: oracle.PolicyAssertion{
			ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
			HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour),
		}},
		Owner: observation(ctx.Owner, 1, evidencebind.OwnerRead, at.Add(-5*time.Second)),
		Cross: observation(ctx.Evaluated, 2, evidencebind.CrossRead, at),
	}
	in.Result = evidencebind.ResultSnapshot{Context: ctx, OwnerEvidence: in.Owner.Evidence, CrossEvidence: in.Cross.Evidence, ObservedAt: at,
		Result: idor.Result{Facts: oracle.ComparisonFacts{
			SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes, CrossNormalData: oracle.StateYes,
			NontrivialDataMatch: oracle.StateYes, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes,
		}},
	}
	judge(&in)
	return ctx, in
}

func judge(in *evidencebind.Input) {
	ctx := in.Result.Context
	in.Result.Result.Verdict = oracle.Evaluate(oracle.CaseInput{
		Policy: in.Policy.Assertion,
		Subjects: oracle.SubjectContext{
			OwnerRef: ctx.Owner.Label, OwnerStatus: ctx.Owner.Status,
			EvaluatedRef: ctx.Evaluated.Label, EvaluatedStatus: ctx.Evaluated.Status,
		},
		Facts: in.Result.Result.Facts,
	}, in.Cross.ObservedAt)
}

func withVerdict(in *evidencebind.Input, value oracle.VerdictValue) {
	switch value {
	case oracle.Suspected:
		in.Policy.Assertion.ExpectedAccess = oracle.AccessUnknown
	case oracle.Allowed:
		in.Policy.Assertion.ExpectedAccess = oracle.AccessAllow
	case oracle.Undecidable:
		in.Result.Result.Facts.CrossNormalData = oracle.StateUnknown
	}
	judge(in)
}

func TestMapTerminalVerdictMatrix(t *testing.T) {
	for _, status := range []retest.Status{retest.Completed, retest.Stopped, retest.Failed} {
		for _, value := range []oracle.VerdictValue{oracle.Confirmed, oracle.Allowed, oracle.Suspected, oracle.Undecidable} {
			t.Run(string(status)+"/"+string(value), func(t *testing.T) {
				ctx, in := fixture()
				withVerdict(&in, value)
				checked := evidencebind.Check(ctx, in)
				if !checked.BindingValid || checked.EffectiveValue != value {
					t.Fatalf("invalid fixture: %+v", checked)
				}
				got, err := retest.Map(status, ctx, in)
				if status == retest.Failed {
					assertError(t, got, err, retest.ErrExecutionFailed)
					return
				}
				wantVerdict, wantReason := retest.Inconclusive, retest.NotConfirmed
				if status == retest.Stopped {
					wantReason = retest.ExecutionStopped
					checked = evidencebind.Outcome{}
				} else if value == oracle.Confirmed {
					wantVerdict, wantReason = retest.Reproduced, retest.ConfirmedEvidence
				}
				want := retest.Result{Verdict: wantVerdict, Reason: wantReason, Check: checked}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("Map = %+v, %v; want %+v", got, err, want)
				}
				assertNoFixed(t, got)
			})
		}
	}
}

func TestMapBlockedAndUncertainReceipts(t *testing.T) {
	reasons := []transmit.Reason{
		transmit.Stopped, transmit.Canceled, transmit.UnsupportedAction, transmit.UnknownResource,
		transmit.UnknownAccount, transmit.UnverifiedAccount, transmit.ExpiredAccount, transmit.AccountNotApproved,
		transmit.ExpiredApproval, transmit.BudgetExhausted, transmit.TransportError, transmit.TimedOut,
		transmit.RedirectRefused, transmit.ResponseTooLarge, transmit.UnsupportedResponse, transmit.InvalidCandidate,
		transmit.CanaryMismatch, "", "unrecognized",
	}
	for _, owner := range []bool{true, false} {
		for _, reason := range reasons {
			t.Run(fmt.Sprintf("owner=%t/%s", owner, reason), func(t *testing.T) {
				ctx, in := fixture()
				if owner {
					in.Owner.Receipt.Reason = reason
				} else {
					in.Cross.Receipt.Reason = reason
				}
				got, err := retest.Map(retest.Completed, ctx, in)
				if err != nil || got.Verdict != retest.Inconclusive || got.Reason != retest.EvidenceRejected ||
					got.Check.BindingValid || got.Check.BindingReason != evidencebind.ExecutionIncomplete {
					t.Fatalf("uncertain execution = %+v, %v", got, err)
				}
				assertNoFixed(t, got)
			})
		}
	}
}

func TestMapTerminalStatusOverridesEarlierEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*evidencebind.Input)
	}{
		{"forged_confirmed", func(in *evidencebind.Input) { in.Result.Result.Facts.CrossNormalData = oracle.StateUnknown }},
		{"proposed_fixed", func(in *evidencebind.Input) { in.Result.Result.Verdict.Value = "fixed" }},
		{"audit_failure", func(in *evidencebind.Input) { in.Cross.Receipt.Reason = transmit.AuditUnavailable }},
		{"no_execution", func(in *evidencebind.Input) { *in = evidencebind.Input{} }},
	}
	for _, status := range []retest.Status{retest.Stopped, retest.Failed} {
		for _, tc := range cases {
			t.Run(string(status)+"/"+tc.name, func(t *testing.T) {
				ctx, in := fixture()
				tc.mutate(&in)
				got, err := retest.Map(status, ctx, in)
				if status == retest.Failed {
					assertError(t, got, err, retest.ErrExecutionFailed)
					return
				}
				want := retest.Result{Verdict: retest.Inconclusive, Reason: retest.ExecutionStopped}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("stopped execution = %+v, %v; want %+v", got, err, want)
				}
				assertNoFixed(t, got)
			})
		}
	}
}

func TestMapReceiptDeliveryFlags(t *testing.T) {
	for _, owner := range []bool{true, false} {
		for flags := range 8 {
			t.Run(fmt.Sprintf("owner=%t/flags=%03b", owner, flags), func(t *testing.T) {
				ctx, in := fixture()
				receipt := &in.Cross.Receipt
				if owner {
					receipt = &in.Owner.Receipt
				}
				receipt.Admitted, receipt.RequestWritten, receipt.ResponseReceived = flags&1 != 0, flags&2 != 0, flags&4 != 0
				got, err := retest.Map(retest.Completed, ctx, in)
				want := retest.Inconclusive
				if flags == 7 {
					want = retest.Reproduced // Positive control: this table also permits a complete execution.
				}
				if err != nil || got.Verdict != want || got.Check.BindingValid != (flags == 7) {
					t.Fatalf("delivery flags = %+v, %v; want %s", got, err, want)
				}
				assertNoFixed(t, got)
			})
		}
	}
}

func TestMapRejectsMissingForeignOrForgedEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*evidencebind.Context, *evidencebind.Input)
	}{
		{"missing_context", func(ctx *evidencebind.Context, _ *evidencebind.Input) { *ctx = evidencebind.Context{} }},
		{"missing_owner", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Owner = evidencebind.Observation{} }},
		{"missing_cross", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Cross = evidencebind.Observation{} }},
		{"missing_result", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Result = evidencebind.ResultSnapshot{} }},
		{"other_task", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Result.Context.Scope.TaskRef = "other-task" }},
		{"other_run", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Cross.Receipt.Scope.RunRef = "other-work" }},
		{"other_resource", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Owner.Receipt.ResourceRef = "other:resource" }},
		{"other_session", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Cross.Receipt.SessionRef = "other-session" }},
		{"other_intent", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Result.CrossEvidence.IntentID++ }},
		{"same_evidence", func(_ *evidencebind.Context, in *evidencebind.Input) {
			in.Result.CrossEvidence = in.Result.OwnerEvidence
		}},
		{"foreign_role", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Cross.Evidence.Role = evidencebind.OwnerRead }},
		{"observation_mismatch", func(_ *evidencebind.Context, in *evidencebind.Input) {
			in.Result.ObservedAt = in.Result.ObservedAt.Add(time.Second)
		}},
		{"non_get", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Cross.Receipt.Method = http.MethodPost }},
		{"partial_order", func(_ *evidencebind.Context, in *evidencebind.Input) {
			in.Cross.Receipt.Attempt = in.Owner.Receipt.Attempt
		}},
		{"http_error_claimed_normal", func(_ *evidencebind.Context, in *evidencebind.Input) {
			in.Cross.Receipt.StatusCode = http.StatusForbidden
		}},
		{"forged_confirmed", func(_ *evidencebind.Context, in *evidencebind.Input) {
			in.Result.Result.Facts.CrossNormalData = oracle.StateUnknown
		}},
		{"forged_checks", func(_ *evidencebind.Context, in *evidencebind.Input) {
			in.Result.Result.Verdict.DataCheck = oracle.StateUnknown
		}},
		{"forged_reasons", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Result.Result.Verdict.ReasonCodes = nil }},
		{"proposed_fixed", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Result.Result.Verdict.Value = "fixed" }},
		{"unknown_verdict", func(_ *evidencebind.Context, in *evidencebind.Input) { in.Result.Result.Verdict.Value = "unrecognized" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, in := fixture()
			tc.mutate(&ctx, &in)
			got, err := retest.Map(retest.Completed, ctx, in)
			if err != nil || got.Verdict != retest.Inconclusive || got.Reason != retest.EvidenceRejected || got.Check.BindingValid {
				t.Fatalf("invalid evidence = %+v, %v", got, err)
			}
			assertNoFixed(t, got)
		})
	}
}

func TestMapAuditFailureHasNoConclusion(t *testing.T) {
	for _, owner := range []bool{true, false} {
		for _, value := range []oracle.VerdictValue{oracle.Confirmed, oracle.Allowed, oracle.Suspected, oracle.Undecidable} {
			t.Run(fmt.Sprintf("owner=%t/%s", owner, value), func(t *testing.T) {
				ctx, in := fixture()
				withVerdict(&in, value)
				if owner {
					in.Owner.Receipt.Reason = transmit.AuditUnavailable
				} else {
					in.Cross.Receipt.Reason = transmit.AuditUnavailable
				}
				got, err := retest.Map(retest.Completed, ctx, in)
				assertError(t, got, err, retest.ErrAuditUnavailable)
			})
		}
	}
}

func TestMapInvalidLifecycleHasNoConclusion(t *testing.T) {
	for _, status := range []retest.Status{"", "pending", "running", "unknown", "Completed", "completed ", "fixed", "reproduced"} {
		t.Run(string(status), func(t *testing.T) {
			ctx, in := fixture()
			got, err := retest.Map(status, ctx, in)
			assertError(t, got, err, retest.ErrInvalidStatus)
		})
	}
}

func TestMapRepeatedInputIsStableAndUnchanged(t *testing.T) {
	ctx, in := fixture()
	_, before := fixture()
	first, err := retest.Map(retest.Completed, ctx, in)
	if err != nil || first.Verdict != retest.Reproduced {
		t.Fatalf("positive control = %+v, %v", first, err)
	}
	for range 3 {
		got, err := retest.Map(retest.Completed, ctx, in)
		if err != nil || !reflect.DeepEqual(got, first) || !reflect.DeepEqual(in, before) {
			t.Fatalf("repeated mapping changed result or evidence: %+v, %v", got, err)
		}
	}
	first.Check.Recomputed.ReasonCodes[0] = "modified_output"
	if !reflect.DeepEqual(in, before) {
		t.Fatal("result aliases the input verdict")
	}
	again, err := retest.Map(retest.Completed, ctx, in)
	if err != nil || again.Check.Recomputed.ReasonCodes[0] != oracle.ReasonPolicyDenyResolved {
		t.Fatalf("previous output mutation affected new mapping: %+v, %v", again, err)
	}
}

func assertError(t *testing.T, got retest.Result, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || !reflect.DeepEqual(got, retest.Result{}) {
		t.Fatalf("error mapping = %+v, %v; want zero result, %v", got, err, want)
	}
	assertNoFixed(t, got)
}

func assertNoFixed(t *testing.T, result retest.Result) {
	t.Helper()
	if result.Verdict == "fixed" {
		t.Fatal("read-only evidence produced fixed")
	}
}
