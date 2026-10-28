package oracle

import "time"

// isAllowPolicyReason mirrors oracle.py's _ALLOW_POLICY_REASONS: a verified allow
// path, where the verdict is Allowed once evaluated subject B is itself verified.
func isAllowPolicyReason(r JudgeReason) bool {
	return r == ReasonPolicyAllows || r == ReasonSharingOrDelegationAllows
}

// isSuspectedPolicyReason mirrors oracle.py's _SUSPECTED_POLICY_REASONS: the
// registered policy exists but its content is unknown. Only then can strong
// (b)(c) open Suspected — mere policy absence or invalidity cannot.
func isSuspectedPolicyReason(r JudgeReason) bool {
	return r == ReasonExpectedAccessUnknown || r == ReasonSharingOrDelegationUnknown
}

// checkExpectedPolicy — (a): is the registered policy an explicit deny for B with
// sharing and delegation both resolved to none? Validity is judged at observedAt.
// Allow paths (direct ALLOW, sharing/delegation RESOLVED_ALLOW) are seen before
// sharing/delegation UNKNOWN, so a verified allow is not held back by an unknown
// exception.
func checkExpectedPolicy(p PolicyAssertion, observedAt time.Time) CheckOutcome {
	if !p.HasRegisteredSource {
		return CheckOutcome{StateUnknown, ReasonExpectedAccessPolicyMissing}
	}
	// A hint that disagrees with the canonical policy holds the policy invalid
	// regardless of the canonical content (spec §9 of the source).
	if !p.HintMatchesCanonical {
		return CheckOutcome{StateUnknown, ReasonExpectedAccessPolicyInvalid}
	}
	// valid_from <= observedAt < valid_until. A policy that started after the
	// observation, or had already expired at it, is Unknown.
	if observedAt.Before(p.ValidFrom) || !observedAt.Before(p.ValidUntil) {
		return CheckOutcome{StateUnknown, ReasonExpectedAccessPolicyInvalid}
	}
	if p.ExpectedAccess == AccessAllow {
		return CheckOutcome{StateNo, ReasonPolicyAllows}
	}
	if p.Sharing == ResolvedAllow || p.Delegation == ResolvedAllow {
		return CheckOutcome{StateNo, ReasonSharingOrDelegationAllows}
	}
	if p.Sharing == ResolutionUnknown || p.Delegation == ResolutionUnknown {
		return CheckOutcome{StateUnknown, ReasonSharingOrDelegationUnknown}
	}
	if p.ExpectedAccess == AccessUnknown {
		return CheckOutcome{StateUnknown, ReasonExpectedAccessUnknown}
	}
	return CheckOutcome{StateYes, ReasonPolicyDenyResolved}
}

// checkIdentity — (b): are A and B both verified, with non-empty refs, and
// distinct? Expired/unknown/empty is reserved as Unknown.
func checkIdentity(s SubjectContext) CheckOutcome {
	if s.OwnerStatus != SubjectVerified || s.EvaluatedStatus != SubjectVerified {
		return CheckOutcome{StateUnknown, ReasonSubjectNotVerified}
	}
	if s.OwnerRef == "" || s.EvaluatedRef == "" {
		return CheckOutcome{StateUnknown, ReasonSubjectNotVerified}
	}
	if s.OwnerRef == s.EvaluatedRef {
		return CheckOutcome{StateNo, ReasonSubjectsNotDistinct}
	}
	return CheckOutcome{StateYes, ReasonSubjectsDistinctVerified}
}

// checkData — (c): does A's nontrivial owner data (for us, A's registered canary
// marker; spec §3(c)) appear in the cross response after reflection and template
// exclusion? A specific NO (reflection/template present, no match) is reported
// first; otherwise any required NO is DATA_NO_MATCH, any Unknown is unverifiable,
// all YES is a nontrivial match.
func checkData(f ComparisonFacts) CheckOutcome {
	if f.ReflectionExcluded == StateNo {
		return CheckOutcome{StateNo, ReasonDataReflectionPresent}
	}
	if f.TemplateExcluded == StateNo {
		return CheckOutcome{StateNo, ReasonDataTemplatePresent}
	}
	if f.NontrivialDataMatch == StateNo {
		return CheckOutcome{StateNo, ReasonDataNoMatch}
	}
	required := []CheckState{
		f.SameResource, f.OwnerNormalData, f.CrossNormalData,
		f.NontrivialDataMatch, f.ReflectionExcluded, f.TemplateExcluded,
	}
	for _, st := range required {
		if st == StateNo {
			return CheckOutcome{StateNo, ReasonDataNoMatch}
		}
	}
	for _, st := range required {
		if st == StateUnknown {
			return CheckOutcome{StateUnknown, ReasonDataUnverifiable}
		}
	}
	return CheckOutcome{StateYes, ReasonDataNontrivialMatch}
}

// Evaluate judges one case conservatively by (a)(b)(c). Pure: observedAt is
// passed in and normalized to UTC; no network, DB, filesystem or wall clock.
// Reason order is policy, identity, data — except Allowed, whose single allow
// reason must not read as "B not verified" when A or the comparison is absent.
func Evaluate(c CaseInput, observedAt time.Time) OracleVerdict {
	policy := checkExpectedPolicy(c.Policy, observedAt.UTC())
	identity := checkIdentity(c.Subjects)
	data := checkData(c.Facts)
	value := decideValue(policy.Reason, identity.State, data.State, c.Subjects)
	reasons := []JudgeReason{policy.Reason, identity.Reason, data.Reason}
	if value == Allowed {
		reasons = []JudgeReason{policy.Reason}
	}
	return OracleVerdict{
		Value:         value,
		PolicyCheck:   policy.State,
		IdentityCheck: identity.State,
		DataCheck:     data.State,
		ReasonCodes:   reasons,
	}
}

// decideValue ports oracle.py's _decide_value. The default is Undecidable
// (fail-closed): policy absence/invalidity, unconfirmed (b)(c), and any reason
// not in the allow/suspected sets all fall through to it.
func decideValue(policyReason JudgeReason, identity, data CheckState, s SubjectContext) VerdictValue {
	crossVerified := identity == StateYes && data == StateYes
	switch {
	case policyReason == ReasonPolicyDenyResolved:
		if crossVerified {
			return Confirmed
		}
		return Undecidable
	case isAllowPolicyReason(policyReason):
		if evaluatedSubjectVerified(s) {
			return Allowed
		}
		return Undecidable
	case isSuspectedPolicyReason(policyReason) && crossVerified:
		return Suspected
	default:
		return Undecidable
	}
}

// evaluatedSubjectVerified is B's own verification — a separate axis from
// checkIdentity's A/B distinctness (spec §4): Allowed needs B verified even when
// A and the comparison are absent.
func evaluatedSubjectVerified(s SubjectContext) bool {
	return s.EvaluatedStatus == SubjectVerified && s.EvaluatedRef != ""
}
