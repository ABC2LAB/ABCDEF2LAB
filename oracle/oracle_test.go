package oracle

import (
	"testing"
	"time"
)

// observedAt mirrors test_oracle.py's _OBSERVED_AT.
var observedAt = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func basePolicy() PolicyAssertion {
	return PolicyAssertion{
		ExpectedAccess:       AccessDeny,
		Sharing:              ResolvedNone,
		Delegation:           ResolvedNone,
		HasRegisteredSource:  true,
		ValidFrom:            observedAt.Add(-time.Hour),
		ValidUntil:           observedAt.Add(time.Hour),
		HintMatchesCanonical: true,
	}
}

func baseSubjects() SubjectContext {
	return SubjectContext{SubjectVerified, SubjectVerified, "subject-a", "subject-b"}
}

// strongFacts: (b)(c) both yes. noComparison: A/cross absent → all unknown.
func strongFacts() ComparisonFacts {
	return ComparisonFacts{StateYes, StateYes, StateYes, StateYes, StateYes, StateYes}
}

func noComparison() ComparisonFacts {
	return ComparisonFacts{StateUnknown, StateUnknown, StateUnknown, StateUnknown, StateUnknown, StateUnknown}
}

func baseCase() CaseInput {
	return CaseInput{basePolicy(), baseSubjects(), strongFacts()}
}

// mut builds a case from the strong base and applies mutations.
func mut(fns ...func(*CaseInput)) CaseInput {
	c := baseCase()
	for _, f := range fns {
		f(&c)
	}
	return c
}

func containsReason(got []JudgeReason, want JudgeReason) bool {
	for _, r := range got {
		if r == want {
			return true
		}
	}
	return false
}

func reasonsEqual(a, b []JudgeReason) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// skip is the sentinel for a CheckState assertion we do not make in a row.
const skip CheckState = ""

func TestEvaluateCases(t *testing.T) {
	cases := []struct {
		name        string
		in          CaseInput
		want        VerdictValue
		wantReasons []JudgeReason // exact match (ordered) when non-nil
		contains    []JudgeReason // each must be present
		notWant     VerdictValue  // asserted absent when non-empty
		policy      CheckState    // skip = not asserted
		identity    CheckState
		data        CheckState
	}{
		{
			name: "all three yes -> confirmed (reason order policy,identity,data)",
			in:   baseCase(), want: Confirmed,
			wantReasons: []JudgeReason{ReasonPolicyDenyResolved, ReasonSubjectsDistinctVerified, ReasonDataNontrivialMatch},
			policy:      StateYes, identity: StateYes, data: StateYes,
		},
		{
			name: "explicit allow + verified B -> allowed (single reason)",
			in:   mut(func(c *CaseInput) { c.Policy.ExpectedAccess = AccessAllow }), want: Allowed,
			wantReasons: []JudgeReason{ReasonPolicyAllows}, policy: StateNo,
		},
		{
			name: "resolved_allow delegation + verified B -> allowed",
			in:   mut(func(c *CaseInput) { c.Policy.Delegation = ResolvedAllow }), want: Allowed,
			wantReasons: []JudgeReason{ReasonSharingOrDelegationAllows}, policy: StateNo,
		},
		{
			name: "direct allow not blocked by unknown sharing",
			in:   mut(func(c *CaseInput) { c.Policy.ExpectedAccess = AccessAllow; c.Policy.Sharing = ResolutionUnknown }),
			want: Allowed, wantReasons: []JudgeReason{ReasonPolicyAllows},
		},
		{
			name: "resolved delegation allow overrides unknown direct access",
			in:   mut(func(c *CaseInput) { c.Policy.ExpectedAccess = AccessUnknown; c.Policy.Delegation = ResolvedAllow }),
			want: Allowed, wantReasons: []JudgeReason{ReasonSharingOrDelegationAllows},
		},
		{
			name: "allowed needs no owner/comparison when B verified",
			in: mut(func(c *CaseInput) {
				c.Policy.ExpectedAccess = AccessAllow
				c.Subjects.OwnerStatus = SubjectUnknown
				c.Subjects.OwnerRef = ""
				c.Facts = noComparison()
			}),
			want: Allowed, wantReasons: []JudgeReason{ReasonPolicyAllows},
			identity: StateUnknown, data: StateUnknown,
		},
		{
			name: "allow + expired B -> undecidable",
			in:   mut(func(c *CaseInput) { c.Policy.ExpectedAccess = AccessAllow; c.Subjects.EvaluatedStatus = SubjectExpired }),
			want: Undecidable, contains: []JudgeReason{ReasonSubjectNotVerified},
		},
		{
			name: "allow + unknown B -> undecidable",
			in:   mut(func(c *CaseInput) { c.Policy.ExpectedAccess = AccessAllow; c.Subjects.EvaluatedStatus = SubjectUnknown }),
			want: Undecidable, contains: []JudgeReason{ReasonSubjectNotVerified},
		},
		{
			name: "allow + empty B ref -> undecidable",
			in:   mut(func(c *CaseInput) { c.Policy.ExpectedAccess = AccessAllow; c.Subjects.EvaluatedRef = "" }),
			want: Undecidable, contains: []JudgeReason{ReasonSubjectNotVerified},
		},
		{
			name: "sharing unknown + cross evidence -> suspected",
			in:   mut(func(c *CaseInput) { c.Policy.Sharing = ResolutionUnknown }), want: Suspected,
			contains: []JudgeReason{ReasonSharingOrDelegationUnknown}, policy: StateUnknown,
		},
		{
			name: "expected_access unknown + cross evidence -> suspected",
			in:   mut(func(c *CaseInput) { c.Policy.ExpectedAccess = AccessUnknown }), want: Suspected,
			contains: []JudgeReason{ReasonExpectedAccessUnknown}, policy: StateUnknown,
		},
		{
			name: "missing registered source -> undecidable (not suspected)",
			in:   mut(func(c *CaseInput) { c.Policy.HasRegisteredSource = false }), want: Undecidable,
			contains: []JudgeReason{ReasonExpectedAccessPolicyMissing}, identity: StateYes, data: StateYes,
		},
		{
			name: "policy expired at observation -> undecidable",
			in:   mut(func(c *CaseInput) { c.Policy.ValidUntil = observedAt.Add(-time.Second) }), want: Undecidable,
			contains: []JudgeReason{ReasonExpectedAccessPolicyInvalid}, policy: StateUnknown,
		},
		{
			name: "policy starting after observation -> undecidable",
			in:   mut(func(c *CaseInput) { c.Policy.ValidFrom = observedAt.Add(time.Minute) }), want: Undecidable,
			contains: []JudgeReason{ReasonExpectedAccessPolicyInvalid}, policy: StateUnknown,
		},
		{
			name: "policy unknown + identity unknown -> undecidable",
			in:   mut(func(c *CaseInput) { c.Policy.ExpectedAccess = AccessUnknown; c.Subjects.OwnerStatus = SubjectUnknown }),
			want: Undecidable, identity: StateUnknown,
		},
		{
			name: "policy unknown + data unknown -> undecidable",
			in:   mut(func(c *CaseInput) { c.Policy.Sharing = ResolutionUnknown; c.Facts.NontrivialDataMatch = StateUnknown }),
			want: Undecidable, data: StateUnknown,
		},
		{
			name: "same subject -> not confirmed, identity no",
			in:   mut(func(c *CaseInput) { c.Subjects.EvaluatedRef = "subject-a" }), notWant: Confirmed,
			contains: []JudgeReason{ReasonSubjectsNotDistinct}, identity: StateNo,
		},
		{
			name: "unverified owner reserves identity",
			in:   mut(func(c *CaseInput) { c.Subjects.OwnerStatus = SubjectExpired }),
			want: Undecidable, contains: []JudgeReason{ReasonSubjectNotVerified}, identity: StateUnknown,
		},
		{
			name: "empty evaluated subject ref reserves identity",
			in:   mut(func(c *CaseInput) { c.Subjects.EvaluatedRef = "" }),
			want: Undecidable, contains: []JudgeReason{ReasonSubjectNotVerified}, identity: StateUnknown,
		},
		{
			// checkIdentity holds on either empty ref; the owner side is the
			// symmetric case test_oracle.py does not cover on either leg.
			name: "empty owner subject ref reserves identity",
			in:   mut(func(c *CaseInput) { c.Subjects.OwnerRef = "" }),
			want: Undecidable, contains: []JudgeReason{ReasonSubjectNotVerified}, identity: StateUnknown,
		},
		{
			name: "reflection not excluded fails data",
			in:   mut(func(c *CaseInput) { c.Facts.ReflectionExcluded = StateNo }), notWant: Confirmed,
			contains: []JudgeReason{ReasonDataReflectionPresent}, data: StateNo,
		},
		{
			name: "template not excluded fails data",
			in:   mut(func(c *CaseInput) { c.Facts.TemplateExcluded = StateNo }), notWant: Confirmed,
			contains: []JudgeReason{ReasonDataTemplatePresent}, data: StateNo,
		},
		{
			name: "trivial match only fails data (no 200/id-reflection confirm)",
			in:   mut(func(c *CaseInput) { c.Facts.NontrivialDataMatch = StateNo }), notWant: Confirmed,
			contains: []JudgeReason{ReasonDataNoMatch}, data: StateNo,
		},
		{
			name: "deny without data match -> undecidable",
			in:   mut(func(c *CaseInput) { c.Facts.NontrivialDataMatch = StateNo }), want: Undecidable,
		},
		{
			// Fail-closed invariant (test_oracle.py::deny_without_valid_exception_is_never_allowed):
			// a registered DENY never reads as Allowed, whatever the (b)(c) evidence. The base case
			// covers the strong-evidence leg (confirmed); this pins the failed-data leg explicitly.
			name: "deny policy is never allowed even when data fails (fail-closed guard)",
			in:   mut(func(c *CaseInput) { c.Facts.NontrivialDataMatch = StateNo }), notWant: Allowed,
		},
		{
			name: "unverifiable data -> undecidable",
			in:   mut(func(c *CaseInput) { c.Facts.NontrivialDataMatch = StateUnknown }), want: Undecidable,
			contains: []JudgeReason{ReasonDataUnverifiable}, data: StateUnknown,
		},
		{
			name: "same-resource no fails data via required loop",
			in:   mut(func(c *CaseInput) { c.Facts.SameResource = StateNo }), notWant: Confirmed,
			contains: []JudgeReason{ReasonDataNoMatch}, data: StateNo,
		},
		{
			name: "valid at observation despite later expiry -> confirmed",
			in: mut(func(c *CaseInput) {
				c.Policy.ValidFrom = observedAt.Add(-5 * time.Minute)
				c.Policy.ValidUntil = observedAt.Add(time.Minute)
			}),
			want: Confirmed,
		},
		{
			name: "hint mismatch holds even a strong deny case",
			in:   mut(func(c *CaseInput) { c.Policy.HintMatchesCanonical = false }), want: Undecidable,
			wantReasons: nil, contains: []JudgeReason{ReasonExpectedAccessPolicyInvalid}, policy: StateUnknown,
		},
		{
			name: "missing policy outranks hint mismatch (policy reason first)",
			in: mut(func(c *CaseInput) {
				c.Policy.HasRegisteredSource = false
				c.Policy.HintMatchesCanonical = false
			}),
			want: Undecidable, contains: []JudgeReason{ReasonExpectedAccessPolicyMissing},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.in, observedAt)
			if tc.want != "" && got.Value != tc.want {
				t.Fatalf("value = %q, want %q (reasons %v)", got.Value, tc.want, got.ReasonCodes)
			}
			if tc.notWant != "" && got.Value == tc.notWant {
				t.Fatalf("value = %q, want != %q", got.Value, tc.notWant)
			}
			if tc.wantReasons != nil && !reasonsEqual(got.ReasonCodes, tc.wantReasons) {
				t.Fatalf("reasons = %v, want %v", got.ReasonCodes, tc.wantReasons)
			}
			for _, r := range tc.contains {
				if !containsReason(got.ReasonCodes, r) {
					t.Fatalf("reasons %v missing %q", got.ReasonCodes, r)
				}
			}
			if tc.policy != skip && got.PolicyCheck != tc.policy {
				t.Fatalf("policy_check = %q, want %q", got.PolicyCheck, tc.policy)
			}
			if tc.identity != skip && got.IdentityCheck != tc.identity {
				t.Fatalf("identity_check = %q, want %q", got.IdentityCheck, tc.identity)
			}
			if tc.data != skip && got.DataCheck != tc.data {
				t.Fatalf("data_check = %q, want %q", got.DataCheck, tc.data)
			}
		})
	}
}

// TestEvaluatePolicyCombinationInvariants ports test_oracle.py's exhaustive
// partition (spec §4 #15): over every policy combination with strong (b)(c),
// absence/invalidity is Undecidable, Suspected only on content-unknown reasons,
// Allowed only when policy_check is No, and Confirmed only on explicit deny with
// no resolved exceptions.
func TestEvaluatePolicyCombinationInvariants(t *testing.T) {
	windows := []struct {
		from, until time.Time
		valid       bool
	}{
		{observedAt.Add(-time.Hour), observedAt.Add(time.Hour), true},
		{observedAt.Add(-2 * time.Hour), observedAt.Add(-time.Hour), false},
	}
	accesses := []ExpectedAccess{AccessAllow, AccessDeny, AccessUnknown}
	resolutions := []ResolutionState{ResolvedNone, ResolvedAllow, ResolutionUnknown}
	suspectedReasons := map[JudgeReason]bool{
		ReasonExpectedAccessUnknown:      true,
		ReasonSharingOrDelegationUnknown: true,
	}

	n := 0
	for _, access := range accesses {
		for _, sharing := range resolutions {
			for _, delegation := range resolutions {
				for _, registered := range []bool{true, false} {
					for _, w := range windows {
						c := baseCase()
						c.Policy.ExpectedAccess = access
						c.Policy.Sharing = sharing
						c.Policy.Delegation = delegation
						c.Policy.HasRegisteredSource = registered
						c.Policy.ValidFrom = w.from
						c.Policy.ValidUntil = w.until

						got := Evaluate(c, observedAt)
						policyReason := got.ReasonCodes[0]
						n++

						if !registered || !w.valid {
							if got.Value != Undecidable {
								t.Errorf("access=%s sharing=%s deleg=%s registered=%v valid=%v: value=%s, want undecidable",
									access, sharing, delegation, registered, w.valid, got.Value)
							}
							if policyReason != ReasonExpectedAccessPolicyMissing && policyReason != ReasonExpectedAccessPolicyInvalid {
								t.Errorf("absence/invalid policy reason=%s, want missing/invalid", policyReason)
							}
						}
						if got.Value == Suspected && !suspectedReasons[policyReason] {
							t.Errorf("suspected with policy reason=%s, want content-unknown", policyReason)
						}
						if got.Value == Allowed && got.PolicyCheck != StateNo {
							t.Errorf("allowed with policy_check=%s, want no", got.PolicyCheck)
						}
						// Fail-closed invariant over every combination: Allowed only on an
						// allow path (explicit ALLOW, or a resolved sharing/delegation
						// exception). So a DENY with no resolved exception is never Allowed.
						if got.Value == Allowed {
							allowPath := access == AccessAllow || sharing == ResolvedAllow || delegation == ResolvedAllow
							if !allowPath {
								t.Errorf("allowed without an allow path: access=%s sharing=%s deleg=%s",
									access, sharing, delegation)
							}
						}
						if got.Value == Confirmed {
							if access != AccessDeny || sharing != ResolvedNone || delegation != ResolvedNone {
								t.Errorf("confirmed with access=%s sharing=%s deleg=%s, want deny+resolved_none",
									access, sharing, delegation)
							}
						}
					}
				}
			}
		}
	}
	if n != 108 {
		t.Fatalf("combination count = %d, want 108", n)
	}
	t.Logf("combination cases: %d", n)
}
