// Package oracle is a pure, dependency-free judge for horizontal IDOR findings
// (E③). It ports abc2lab-dast's judge/core (oracle.py + model.py) to Go per
// docs/contracts/e3-oracle.md (spec v0.1).
//
// Why isolated: this package imports nothing from the rest of ARTEX and touches
// no network, DB, filesystem or clock. Turning stored evidence into a CaseInput,
// and producing ComparisonFacts (canary matching, reflection/template
// exclusion), are the adapter's and comparator's job — not this package's
// (spec §3(c), §5). Keeping the judge pure is what makes it table-testable and
// keeps the LLM's claims out of the verdict.
package oracle

import "time"

// CheckState is the confirmation state of one check (a)(b)(c). Anything we could
// not confirm is StateUnknown — never silently treated as a pass.
type CheckState string

const (
	StateYes     CheckState = "yes"
	StateNo      CheckState = "no"
	StateUnknown CheckState = "unknown"
)

// VerdictValue is the oracle's conservative verdict; the default is Undecidable
// (fail-closed). Allowed means the registered policy verifiably permits the
// access — not an HTTP success nor an absence of vulnerability. The stored
// record maps Allowed to the "위반 미관찰" check result (spec §4, §8-①).
type VerdictValue string

const (
	Confirmed   VerdictValue = "confirmed"
	Suspected   VerdictValue = "suspected"
	Undecidable VerdictValue = "undecidable"
	Allowed     VerdictValue = "allowed"
)

// ExpectedAccess is the access the registered policy grants evaluated subject B.
type ExpectedAccess string

const (
	AccessAllow   ExpectedAccess = "allow"
	AccessDeny    ExpectedAccess = "deny"
	AccessUnknown ExpectedAccess = "unknown"
)

// ResolutionState is the resolution of a sharing/delegation exception. "Not
// queried" is ResolutionUnknown, never ResolvedNone: absence of a lookup must
// not read as absence of an exception.
type ResolutionState string

const (
	ResolvedNone      ResolutionState = "resolved_none"
	ResolvedAllow     ResolutionState = "resolved_allow"
	ResolutionUnknown ResolutionState = "unknown"
)

// SubjectStatus is a subject's verification state.
type SubjectStatus string

const (
	SubjectVerified SubjectStatus = "verified"
	SubjectUnknown  SubjectStatus = "unknown"
	SubjectExpired  SubjectStatus = "expired"
	SubjectMismatch SubjectStatus = "mismatch"
)

// JudgeReason is a verdict reason code (merged to contracts' ReasonCode later).
type JudgeReason string

const (
	ReasonPolicyDenyResolved          JudgeReason = "policy_deny_resolved"
	ReasonPolicyAllows                JudgeReason = "policy_allows"
	ReasonSharingOrDelegationAllows   JudgeReason = "sharing_or_delegation_allows"
	ReasonExpectedAccessPolicyMissing JudgeReason = "expected_access_policy_missing"
	ReasonExpectedAccessPolicyInvalid JudgeReason = "expected_access_policy_invalid"
	ReasonExpectedAccessUnknown       JudgeReason = "expected_access_unknown"
	ReasonSharingOrDelegationUnknown  JudgeReason = "sharing_or_delegation_unknown"
	ReasonSubjectsDistinctVerified    JudgeReason = "subjects_distinct_verified"
	ReasonSubjectNotVerified          JudgeReason = "subject_not_verified"
	ReasonSubjectsNotDistinct         JudgeReason = "subjects_not_distinct"
	ReasonDataNontrivialMatch         JudgeReason = "data_nontrivial_match"
	ReasonDataNoMatch                 JudgeReason = "data_no_match"
	ReasonDataReflectionPresent       JudgeReason = "data_reflection_present"
	ReasonDataTemplatePresent         JudgeReason = "data_template_present"
	ReasonDataUnverifiable            JudgeReason = "data_unverifiable"
)

// CheckOutcome is one check's confirmation state and its reason.
type CheckOutcome struct {
	State  CheckState
	Reason JudgeReason
}

// PolicyAssertion is the registered expected-access policy, distilled for
// judging. Trusted only when HasRegisteredSource. Validity is judged at the
// evidence's observation time, not judgment time (spec §3(a)); HintMatchesCanonical
// is whether the caller's policy hint matched the canonical policy a reader
// returned — a mismatch holds the policy invalid regardless of its content.
type PolicyAssertion struct {
	ExpectedAccess       ExpectedAccess
	Sharing              ResolutionState
	Delegation           ResolutionState
	HasRegisteredSource  bool
	ValidFrom            time.Time
	ValidUntil           time.Time
	HintMatchesCanonical bool
}

// SubjectContext holds A (owner) and B (evaluated) verification state and refs.
// An empty ref ("") is treated as not verified, collapsing Python's None and "".
type SubjectContext struct {
	OwnerStatus     SubjectStatus
	EvaluatedStatus SubjectStatus
	OwnerRef        string
	EvaluatedRef    string
}

// ComparisonFacts are the comparison signals the local comparator produced under
// its injection rules (spec §3(c)): for us these are canary-marker signals, not
// raw sensitive data. No raw text or HMAC keys reach this package.
type ComparisonFacts struct {
	SameResource        CheckState
	OwnerNormalData     CheckState
	CrossNormalData     CheckState
	NontrivialDataMatch CheckState
	ReflectionExcluded  CheckState
	TemplateExcluded    CheckState
}

// CaseInput is one case's judging input. The adapter guarantees every signal
// shares one reset generation and policy version, or drops to Undecidable before
// calling Evaluate (spec §5).
type CaseInput struct {
	Policy   PolicyAssertion
	Subjects SubjectContext
	Facts    ComparisonFacts
}

// OracleVerdict is the internal verdict; issuing, audit and revision are the
// adapter's job. ReasonCodes are ordered policy, identity, data — except Allowed,
// which carries only its single allow reason.
type OracleVerdict struct {
	Value         VerdictValue
	PolicyCheck   CheckState
	IdentityCheck CheckState
	DataCheck     CheckState
	ReasonCodes   []JudgeReason
}
