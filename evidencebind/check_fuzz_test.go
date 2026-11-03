package evidencebind

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Autumn-27/artex/oracle"
)

func fuzzForeignRef(value string) string {
	if value == "" {
		return "foreign-ref"
	}
	if len(value) > 96 {
		value = value[:96]
	}
	return "foreign-" + value
}

func cloneInput(in Input) Input {
	if in.Result.Result.Verdict.ReasonCodes != nil {
		in.Result.Result.Verdict.ReasonCodes = append([]oracle.JudgeReason(nil), in.Result.Result.Verdict.ReasonCodes...)
	}
	return in
}

func cloneVerdict(verdict oracle.OracleVerdict) oracle.OracleVerdict {
	verdict.ReasonCodes = append([]oracle.JudgeReason(nil), verdict.ReasonCodes...)
	return verdict
}

func requireValidFixture(t *testing.T, in Input) {
	t.Helper()
	got := Check(checkContext(), in)
	if !got.BindingValid || got.BindingReason != Bound {
		t.Fatalf("valid fixture rejected: %+v", got)
	}
}

// FuzzEvidenceBindingRejectsInputMutation keeps the expected Context trusted
// and changes one stored observation at a time. The assertion is deliberately
// about the binding boundary: invalid input must not expose a recomputed
// verdict, even when the changed value remains syntactically valid.
func FuzzEvidenceBindingRejectsInputMutation(f *testing.F) {
	for _, seed := range []struct {
		selector int
		value    string
	}{
		{1, "other-run"}, {2, "other-account"}, {3, "other-method"},
		{4, "other-evidence"}, {5, "other-approval"}, {6, "other-resource"},
		{7, "other-scope"}, {8, "other-verdict"}, {9, ""},
		{10, "restamped-evidence"}, {11, "policy-context"}, {12, "other-session"},
		{13, "role-swap"},
	} {
		f.Add(seed.selector, seed.value)
	}
	f.Fuzz(func(t *testing.T, selector int, value string) {
		in := checkInput()
		requireValidFixture(t, in)
		foreign := fuzzForeignRef(value)
		switch positiveMod(selector-1, 13) + 1 {
		case 1:
			in.Cross.Context.Scope.RunRef = foreign
		case 2:
			in.Cross.Receipt.AccountRef = foreign
		case 3:
			in.Cross.Receipt.Method = http.MethodPost
		case 4:
			in.Cross.Evidence.ID = in.Owner.Evidence.ID
			in.Result.CrossEvidence = in.Cross.Evidence
		case 5:
			in.Cross.Receipt.ApprovalRef = foreign
		case 6:
			in.Cross.Receipt.ResourceRef = foreign
		case 7:
			in.Cross.Evidence.Scope.RunRef = foreign
			in.Result.CrossEvidence = in.Cross.Evidence
		case 8:
			in.Result.Result.Verdict.Value = oracle.Suspected
		case 9:
			in.Cross.Evidence.ID = ""
			in.Result.CrossEvidence = in.Cross.Evidence
		case 10:
			in.Cross.Evidence.IntentID++
			in.Result.CrossEvidence = in.Cross.Evidence
		case 11:
			in.Policy.Context.Scope.RunRef = foreign
		case 12:
			in.Cross.Receipt.SessionRef = foreign
		case 13:
			in.Cross.Evidence.Role = OwnerRead
			in.Result.CrossEvidence = in.Cross.Evidence
		}
		beforeCheck := cloneInput(in)
		got := Check(checkContext(), in)
		if got.BindingValid || got.EffectiveValue != oracle.Undecidable || !reflect.DeepEqual(got.Recomputed, oracle.OracleVerdict{}) {
			t.Fatalf("mutation selector=%d value=%q accepted: %+v", selector, value, got)
		}
		if !reflect.DeepEqual(in, beforeCheck) {
			t.Fatal("Check mutated its input")
		}
	})
}

// FuzzEvidenceBindingRejectsTemporalAndReceiptMutation covers the relations
// that are easy to break by changing only one timestamp or completion flag.
func FuzzEvidenceBindingRejectsTemporalAndReceiptMutation(f *testing.F) {
	for _, selector := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		f.Add(selector, int64(1))
	}
	f.Fuzz(func(t *testing.T, selector int, delta int64) {
		in := checkInput()
		requireValidFixture(t, in)
		if delta == 0 {
			delta = 1
		}
		if delta > 1_000_000 || delta < -1_000_000 {
			delta = delta % 1_000_000
			if delta == 0 {
				delta = 1
			}
		}
		d := time.Duration(delta) * time.Microsecond
		switch positiveMod(selector-1, 10) + 1 {
		case 1:
			in.Cross.Receipt.ResponseReceived = false
		case 2:
			in.Cross.Receipt.Admitted = false
		case 3:
			in.Cross.Receipt.RequestWritten = false
		case 4:
			in.Cross.Receipt.StatusCode = 99
		case 5:
			in.Cross.Receipt.StatusCode = 600
		case 6:
			in.Cross.ObservedAt = in.Cross.Receipt.AdmittedAt.Add(-time.Microsecond)
			in.Result.ObservedAt = in.Cross.ObservedAt
		case 7:
			in.Cross.Receipt.At = in.Cross.ObservedAt.Add(d)
			if !in.Cross.Receipt.At.Before(in.Cross.ObservedAt) {
				in.Cross.Receipt.At = in.Cross.ObservedAt.Add(-time.Microsecond)
			}
		case 8:
			in.Cross.Evidence.Attempt++
		case 9:
			in.Cross.Receipt.Attempt = in.Owner.Receipt.Attempt
			in.Cross.Evidence.Attempt = in.Cross.Receipt.Attempt
			in.Result.CrossEvidence = in.Cross.Evidence
		case 10:
			in.Result.ObservedAt = in.Owner.ObservedAt
		}
		beforeCheck := cloneInput(in)
		got := Check(checkContext(), in)
		if got.BindingValid || got.EffectiveValue != oracle.Undecidable || !reflect.DeepEqual(got.Recomputed, oracle.OracleVerdict{}) {
			t.Fatalf("temporal/receipt selector=%d delta=%d accepted: %+v", selector, delta, got)
		}
		if !reflect.DeepEqual(in, beforeCheck) {
			t.Fatal("Check mutated its input")
		}
	})
}

// FuzzEvidenceBindingRecomputesTrustedFacts varies only the trusted host's
// supplied facts and policy assertion, then independently prepares the provided
// verdict from the same host values. Check must accept the binding and return
// exactly the oracle recomputation; the expected Context remains unchanged.
func FuzzEvidenceBindingRecomputesTrustedFacts(f *testing.F) {
	for _, seed := range []struct {
		access int
		facts  int
	}{
		{0, 0x000000}, {1, 0x111111}, {2, 0x222222},
		{0, 0x012210}, {1, 0x201102}, {2, 0x120201},
	} {
		f.Add(seed.access, seed.facts)
	}
	f.Fuzz(func(t *testing.T, access, facts int) {
		in := checkInput()
		requireValidFixture(t, in)
		switch positiveMod(access, 3) {
		case 0:
			in.Policy.Assertion.ExpectedAccess = oracle.AccessDeny
		case 1:
			in.Policy.Assertion.ExpectedAccess = oracle.AccessAllow
		case 2:
			in.Policy.Assertion.ExpectedAccess = oracle.AccessUnknown
		}
		states := []oracle.CheckState{oracle.StateYes, oracle.StateNo, oracle.StateUnknown}
		factValues := []*oracle.CheckState{
			&in.Result.Result.Facts.SameResource,
			&in.Result.Result.Facts.OwnerNormalData,
			&in.Result.Result.Facts.CrossNormalData,
			&in.Result.Result.Facts.NontrivialDataMatch,
			&in.Result.Result.Facts.ReflectionExcluded,
			&in.Result.Result.Facts.TemplateExcluded,
		}
		for index, field := range factValues {
			nibble := (uint64(facts) >> uint(index*4)) & 0xf
			*field = states[nibble%uint64(len(states))]
		}
		setCheckVerdict(&in)
		wantInput := cloneInput(in)
		want := cloneVerdict(in.Result.Result.Verdict)
		got := Check(checkContext(), in)
		repeat := Check(checkContext(), in)
		if !got.BindingValid || got.BindingReason != Bound || got.EffectiveValue != want.Value || !reflect.DeepEqual(got.Recomputed, want) {
			t.Fatalf("access=%d facts=%d outcome=%+v want=%+v", access, facts, got, want)
		}
		if !reflect.DeepEqual(got, repeat) {
			t.Fatal("Check is not deterministic")
		}
		if !reflect.DeepEqual(in, wantInput) {
			t.Fatal("Check mutated its input")
		}
		if len(got.Recomputed.ReasonCodes) > 0 {
			got.Recomputed.ReasonCodes[0] = "mutated"
			if reflect.DeepEqual(got.Recomputed.ReasonCodes, want.ReasonCodes) {
				t.Fatal("returned reason slice aliases expected verdict")
			}
			if !reflect.DeepEqual(in, wantInput) {
				t.Fatal("returned reason slice aliases input verdict")
			}
			if !reflect.DeepEqual(Check(checkContext(), in), repeat) {
				t.Fatal("returned reason mutation changed a repeated Check result")
			}
		}
	})
}

func positiveMod(value, modulus int) int {
	value %= modulus
	if value < 0 {
		value += modulus
	}
	return value
}
