package candidateverify

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// SourceV2 contains only the bounded, non-sensitive E② projection. It is
// captured from server-checked runner results, never from model tool input.
type SourceV2 struct {
	CallRef        string                  `json:"call_ref"`
	IntentID       int64                   `json:"intent_id"`
	TaskRef        string                  `json:"task_ref"`
	RunRef         string                  `json:"run_ref"`
	ScopeDigest    string                  `json:"scope_digest"`
	PolicyRevision string                  `json:"policy_revision"`
	Observation    e2proxy.HTTPObservation `json:"observation"`
	Digest         string                  `json:"digest"`
}

func sourceDigest(o e2proxy.HTTPObservation) string {
	raw, _ := json.Marshal(o)
	return db.DiagnosticEvidenceDigest(string(raw))
}

// AssessV2 searches separately captured, server-checked E② calls after a
// candidate has been stored. No target request or model call is made here.
func SupportedV2(c db.DiagnosticCandidate) bool {
	return (c.VerificationClaim == "html_nosniff_missing" && c.VulnClass == "security_headers") ||
		(c.VerificationClaim == "server_version_banner" && c.VulnClass == "information_disclosure")
}

func AssessV2(c db.DiagnosticCandidate, calls []e2runner.TrustedResult) Result {
	if !SupportedV2(c) {
		return Result{Status: Unsupported, Reason: "type_not_supported"}
	}
	if c.Validate() != nil || c.Classification != "completed" || c.OutputTruncated {
		return Result{Status: Insufficient, Reason: "candidate_evidence_unavailable"}
	}
	var original struct {
		Trusted struct {
			Call      e2proxy.CallResult    `json:"call"`
			Execution e2proxy.ExecutionView `json:"execution"`
		} `json:"trusted"`
	}
	if json.Unmarshal([]byte(c.Evidence), &original) != nil || original.Trusted.Call.CallRef != c.CallRef {
		return Result{Status: Insufficient, Reason: "candidate_evidence_unavailable"}
	}
	var primary *e2runner.TrustedResult
	for i := range calls {
		if calls[i].Call.CallRef != c.CallRef {
			continue
		}
		if primary != nil || !e2proxy.EqualCallResults(original.Trusted.Call, calls[i].Call) {
			return Result{Status: Insufficient, Reason: "primary_source_mismatch"}
		}
		primary = &calls[i]
	}
	if primary == nil || !sameRun(c, *primary) || primary.Call.IntentID != c.IntentID || original.Trusted.Execution.Scope != primary.Execution.Scope || original.Trusted.Execution.PolicyRevision != primary.Execution.PolicyRevision || original.Trusted.Execution.HeaderMode != primary.Execution.HeaderMode {
		return Result{Status: Insufficient, Reason: "primary_source_unavailable"}
	}
	base, state, ok := v2Observation(*primary, c.VerificationClaim)
	if !ok {
		return Result{Status: Insufficient, Reason: "primary_observation_unavailable"}
	}
	if !base.ExactFunction || strings.Contains(base.Endpoint, "{") || base.RuleRef == "" {
		return Result{Status: Insufficient, Reason: "function_binding_unavailable"}
	}
	sources := []SourceV2{makeSource(*primary, base)}
	if !validSaveFenceV2(c, sources[0]) {
		return Result{Status: Insufficient, Reason: "candidate_save_order_unavailable", Sources: sources}
	}
	seenCalls := map[string]bool{c.CallRef: true}
	seenAttempts := map[string]bool{base.AttemptRef: true}
	conflict := false
	preSaveSeen := false
	ordered := append([]e2runner.TrustedResult(nil), calls...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Call.CallRef < ordered[j].Call.CallRef })
	for _, call := range ordered {
		if !sameRun(c, call) || call.Call.CallRef == c.CallRef || call.Execution.Scope != primary.Execution.Scope || call.Execution.PolicyRevision != primary.Execution.PolicyRevision || call.Execution.HeaderMode != primary.Execution.HeaderMode {
			continue
		}
		obs, otherState, valid := v2Observation(call, c.VerificationClaim)
		if !valid || !obs.ExactFunction || obs.Endpoint != base.Endpoint || obs.RuleRef != base.RuleRef {
			continue
		}
		if seenCalls[call.Call.CallRef] || seenAttempts[obs.AttemptRef] {
			return Result{Status: Insufficient, Reason: "observation_not_independent"}
		}
		seenCalls[call.Call.CallRef], seenAttempts[obs.AttemptRef] = true, true
		if obs.Sequence <= c.SaveFence.AttemptHighWater {
			preSaveSeen = true
			if otherState != state {
				conflict = true
				if len(sources) < 2 {
					sources = append(sources, makeSource(call, obs))
				}
			}
			continue
		}
		if otherState != state {
			if !conflict {
				if len(sources) < 2 {
					sources = append(sources, makeSource(call, obs))
				} else {
					sources[1] = makeSource(call, obs)
				}
			}
			conflict = true
		} else if len(sources) < 2 {
			sources = append(sources, makeSource(call, obs))
		}
	}
	if len(sources) < 2 {
		reason := "independent_observation_missing"
		if preSaveSeen {
			reason = "post_save_observation_missing"
		}
		return Result{Status: Insufficient, Reason: reason, Attempts: []string{base.AttemptRef}, Sources: sources}
	}
	if conflict {
		return Result{Status: Insufficient, Reason: "observations_inconsistent", Sources: sources}
	}
	refs := make([]string, 0, len(sources))
	for _, source := range sources {
		refs = append(refs, source.Observation.AttemptRef)
	}
	sort.Strings(refs)
	result := Result{Attempts: refs, Sources: sources}
	switch c.VerificationClaim {
	case "html_nosniff_missing":
		if state == "nosniff_missing" {
			result.Status, result.Reason = Confirmed, "html_nosniff_missing_twice"
		} else {
			result.Status, result.Reason = Negative, "html_nosniff_present_twice"
		}
	case "server_version_banner":
		if strings.HasPrefix(state, "banner/") {
			result.Status, result.Reason = Confirmed, "server_version_banner_twice"
		} else {
			result.Status, result.Reason = Negative, "server_versionless_banner_twice"
		}
	}
	return result
}

func sameRun(c db.DiagnosticCandidate, call e2runner.TrustedResult) bool {
	return call.Execution.Scope.TaskRef == stringTaskID(c.TaskID) && call.Execution.Scope.RunRef == c.RunRef && call.Execution.IntentID == call.Call.IntentID && call.Call.CallRef != ""
}

func stringTaskID(id int64) string { return strconv.FormatInt(id, 10) }

func makeSource(call e2runner.TrustedResult, obs e2proxy.HTTPObservation) SourceV2 {
	scope, _ := json.Marshal(call.Execution.Scope)
	return SourceV2{CallRef: call.Call.CallRef, IntentID: call.Call.IntentID, TaskRef: call.Execution.Scope.TaskRef, RunRef: call.Execution.Scope.RunRef, ScopeDigest: db.DiagnosticEvidenceDigest(string(scope)), PolicyRevision: call.Execution.PolicyRevision, Observation: obs, Digest: sourceDigest(obs)}
}

func validSaveFenceV2(c db.DiagnosticCandidate, primary SourceV2) bool {
	f := c.SaveFence
	return f != nil && !f.CapturedAt.IsZero() && f.AttemptHighWater > 0 && primary.Observation.Sequence > 0 &&
		primary.Observation.Sequence <= f.AttemptHighWater && f.TaskRef == stringTaskID(c.TaskID) && f.RunRef == c.RunRef &&
		f.ScopeDigest == primary.ScopeDigest && f.PolicyRevision == primary.PolicyRevision && primary.CallRef == c.CallRef
}

func v2Observation(t e2runner.TrustedResult, claim string) (e2proxy.HTTPObservation, string, bool) {
	if !t.Final || !t.ProcessDrained || !t.ProxyDrained || !t.Execution.AuditComplete || t.Classification != "completed" || t.OutputTruncated ||
		!t.Call.Final || !t.Call.AuditComplete || t.Call.ObservationsTruncated || len(t.Call.Observations) != 1 {
		return e2proxy.HTTPObservation{}, "", false
	}
	obs := t.Call.Observations[0]
	if obs.Sequence == 0 || obs.ObservedAt.IsZero() || obs.Method != "GET" || obs.StatusCode != 200 || obs.Endpoint == "" || obs.RuleRef == "" ||
		obs.Decision != "allowed" || !obs.ForwardAttempted || !obs.ResponseComplete || !obs.AuditComplete || obs.ResponseTruncated || obs.AttemptRef == "" {
		return e2proxy.HTTPObservation{}, "", false
	}
	switch claim {
	case "html_nosniff_missing":
		if obs.HTMLSecurity == "nosniff_missing" || obs.HTMLSecurity == "nosniff_present" {
			return obs, obs.HTMLSecurity, true
		}
	case "server_version_banner":
		var server *e2proxy.HTTPTechnology
		for i := range obs.Technologies {
			technology := &obs.Technologies[i]
			if technology.Header != "Server" {
				continue
			}
			if server != nil || technology.Name == "" {
				return e2proxy.HTTPObservation{}, "", false
			}
			server = technology
		}
		if server != nil {
			if server.Version == "" {
				return obs, "versionless/" + server.Name, true
			}
			return obs, "banner/" + server.Name + "/" + server.Version, true
		}
	}
	return e2proxy.HTTPObservation{}, "", false
}

// ValidSourcesV2 validates the persisted safe projection on report reads.
func ValidSourcesV2(result Result) bool {
	if len(result.Sources) > 2 {
		return false
	}
	if (result.Status == Confirmed || result.Status == Negative) && (len(result.Sources) < 2 || len(result.Attempts) != len(result.Sources)) {
		return false
	}
	seenCalls, seenAttempts := map[string]bool{}, map[string]bool{}
	for _, s := range result.Sources {
		if s.CallRef == "" || s.IntentID <= 0 || s.TaskRef == "" || s.RunRef == "" || s.ScopeDigest == "" || s.Observation.Sequence == 0 || s.Observation.ObservedAt.IsZero() || s.Observation.AttemptRef == "" || s.Digest != sourceDigest(s.Observation) || seenCalls[s.CallRef] || seenAttempts[s.Observation.AttemptRef] {
			return false
		}
		seenCalls[s.CallRef], seenAttempts[s.Observation.AttemptRef] = true, true
	}
	return true
}

// ValidateStoredV2 rechecks the closed verdict against the untouched primary
// candidate and its persisted safe source projections before report export.
func ValidateStoredV2(c db.DiagnosticCandidate, r Result) bool {
	if r.Status != Confirmed && r.Status != Negative {
		return ValidSourcesV2(r)
	}
	if !ValidSourcesV2(r) || c.Validate() != nil || r.Sources[0].CallRef != c.CallRef || !validSaveFenceV2(c, r.Sources[0]) || r.Sources[1].Observation.Sequence <= c.SaveFence.AttemptHighWater {
		return false
	}
	var original struct {
		Trusted struct {
			Call      e2proxy.CallResult    `json:"call"`
			Execution e2proxy.ExecutionView `json:"execution"`
		} `json:"trusted"`
	}
	if json.Unmarshal([]byte(c.Evidence), &original) != nil || len(original.Trusted.Call.Observations) != 1 {
		return false
	}
	base := original.Trusted.Call.Observations[0]
	primaryScope, _ := json.Marshal(original.Trusted.Execution.Scope)
	if db.DiagnosticEvidenceDigest(string(primaryScope)) != r.Sources[0].ScopeDigest || original.Trusted.Execution.PolicyRevision != r.Sources[0].PolicyRevision {
		return false
	}
	seenPrimary := false
	state := ""
	scopeDigest, policyRevision := r.Sources[0].ScopeDigest, r.Sources[0].PolicyRevision
	refs := make([]string, 0, len(r.Sources))
	for _, source := range r.Sources {
		if source.TaskRef != stringTaskID(c.TaskID) || source.RunRef != c.RunRef || source.ScopeDigest != scopeDigest || source.PolicyRevision != policyRevision || !source.Observation.ExactFunction || source.Observation.Endpoint != base.Endpoint || source.Observation.RuleRef != base.RuleRef || strings.Contains(source.Observation.Endpoint, "{") {
			return false
		}
		if source.CallRef == c.CallRef {
			if seenPrimary || source.IntentID != c.IntentID || !reflect.DeepEqual(source.Observation, base) {
				return false
			}
			seenPrimary = true
		}
		virtual := e2runner.TrustedResult{Final: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed", Call: e2proxy.CallResult{Final: true, AuditComplete: true, Observations: []e2proxy.HTTPObservation{source.Observation}}}
		virtual.Execution.AuditComplete = true
		_, next, ok := v2Observation(virtual, c.VerificationClaim)
		if !ok || state != "" && state != next {
			return false
		}
		state = next
		refs = append(refs, source.Observation.AttemptRef)
	}
	if !seenPrimary {
		return false
	}
	sort.Strings(refs)
	if len(refs) != len(r.Attempts) {
		return false
	}
	for i := range refs {
		if refs[i] != r.Attempts[i] {
			return false
		}
	}
	switch c.VerificationClaim {
	case "html_nosniff_missing":
		return c.VulnClass == "security_headers" && (r.Status == Confirmed && r.Reason == "html_nosniff_missing_twice" && state == "nosniff_missing" || r.Status == Negative && r.Reason == "html_nosniff_present_twice" && state == "nosniff_present")
	case "server_version_banner":
		return c.VulnClass == "information_disclosure" && (r.Status == Confirmed && r.Reason == "server_version_banner_twice" && strings.HasPrefix(state, "banner/") || r.Status == Negative && r.Reason == "server_versionless_banner_twice" && strings.HasPrefix(state, "versionless/"))
	}
	return false
}
