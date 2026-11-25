package candidateverify

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// ActiveBindingV1 links a closed follow-up run to one untouched candidate.
// The caller must separately verify the approved plan, fresh E² authority,
// target identity, execution seal, and observation aggregate digest.
type ActiveBindingV1 struct {
	TaskID    int64
	RunRef    string
	ExactPath string
	CaseRef   string
}

// AssessActiveStoredV1 uses two independent, host-checked GET observations
// from the new run. The original response is context, never a second vote.
func AssessActiveStoredV1(candidate db.DiagnosticCandidate, binding ActiveBindingV1, stored []StoredObservationV2) Result {
	if !SupportedV2(candidate) {
		return Result{Status: Unsupported, Reason: "type_not_supported"}
	}
	if candidate.Validate() != nil || binding.TaskID <= 0 || binding.TaskID == candidate.TaskID || binding.RunRef == "" || binding.ExactPath == "" || binding.CaseRef == "" {
		return Result{Status: Insufficient, Reason: "candidate_evidence_unavailable"}
	}
	var original struct {
		Trusted struct {
			Call      e2proxy.CallResult    `json:"call"`
			Execution e2proxy.ExecutionView `json:"execution"`
		} `json:"trusted"`
	}
	if json.Unmarshal([]byte(candidate.Evidence), &original) != nil || original.Trusted.Call.CallRef != candidate.CallRef ||
		len(original.Trusted.Call.Observations) != 1 || original.Trusted.Call.Observations[0].Endpoint != binding.ExactPath ||
		!original.Trusted.Call.Observations[0].ExactFunction || original.Trusted.Execution.HeaderMode != e2proxy.HeaderPreserve {
		return Result{Status: Insufficient, Reason: "primary_source_unavailable"}
	}
	if len(stored) != 2 {
		return Result{Status: Insufficient, Reason: "independent_observation_missing"}
	}
	seenCalls, seenAttempts := map[string]bool{}, map[string]bool{}
	var sources []SourceV2
	var states []string
	var scopeDigest, policyRevision, ruleRef string
	for _, row := range stored {
		s := row.Source
		if !validStoredObservationV2(row) || row.Revision != stored[0].Revision || row.HeaderMode != e2proxy.HeaderPreserve || s.TaskRef != strconv.FormatInt(binding.TaskID, 10) ||
			s.RunRef != binding.RunRef || s.Observation.Endpoint != binding.ExactPath || s.Observation.Method != "GET" ||
			seenCalls[s.CallRef] || seenAttempts[s.Observation.AttemptRef] || s.CallRef == candidate.CallRef ||
			s.Observation.AttemptRef == original.Trusted.Call.Observations[0].AttemptRef {
			return Result{Status: Insufficient, Reason: "observation_not_independent"}
		}
		seenCalls[s.CallRef], seenAttempts[s.Observation.AttemptRef] = true, true
		if scopeDigest == "" {
			scopeDigest, policyRevision, ruleRef = s.ScopeDigest, s.PolicyRevision, s.Observation.RuleRef
		}
		if s.ScopeDigest != scopeDigest || s.PolicyRevision != policyRevision || s.Observation.RuleRef != ruleRef {
			return Result{Status: Insufficient, Reason: "followup_scope_mismatch"}
		}
		virtual := e2runner.TrustedResult{Final: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed",
			Call: e2proxy.CallResult{Final: true, AuditComplete: true, Observations: []e2proxy.HTTPObservation{s.Observation}}}
		virtual.Execution.AuditComplete = true
		_, state, ok := storedObservation(row.Revision, virtual, candidate.VerificationClaim)
		if !ok {
			return Result{Status: Insufficient, Reason: "followup_observation_invalid"}
		}
		sources = append(sources, s)
		states = append(states, state)
	}
	if states[0] != states[1] {
		return Result{Status: Insufficient, Reason: "observations_inconsistent", Sources: sources}
	}
	refs := []string{sources[0].Observation.AttemptRef, sources[1].Observation.AttemptRef}
	sort.Strings(refs)
	result := Result{Sources: sources, Attempts: refs}
	switch candidate.VerificationClaim {
	case "html_nosniff_missing":
		if states[0] == "nosniff_missing" {
			result.Status, result.Reason = Confirmed, "html_nosniff_missing_followup_twice"
		} else {
			result.Status, result.Reason = Negative, "html_nosniff_present_followup_twice"
		}
	case "server_version_banner":
		if len(states[0]) >= len("banner/") && states[0][:len("banner/")] == "banner/" {
			result.Status, result.Reason = Confirmed, "server_version_banner_followup_twice"
		} else {
			result.Status, result.Reason = Negative, "server_versionless_followup_twice"
		}
	}
	return result
}
