package candidateverify

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// StoredObservationV2 is the bounded, non-sensitive projection of a completed
// E² call. It contains no request URL, response body, cookie, or credential.
type StoredObservationV2 struct {
	// Zero preserves the rules used by observations saved before candidate-scoped replay.
	Revision   int                `json:"revision,omitempty"`
	Source     SourceV2           `json:"source"`
	HeaderMode e2proxy.HeaderMode `json:"header_mode"`
}

const MaxStoredObservationsV2 = 2048
const candidateScopedRevision = 1

func storedObservation(revision int, call e2runner.TrustedResult, claim string) (e2proxy.HTTPObservation, string, bool) {
	if revision == 0 && claim == "server_version_banner" && len(call.Call.Observations) == 1 && len(call.Call.Observations[0].Technologies) != 1 {
		return e2proxy.HTTPObservation{}, "", false
	}
	return v2Observation(call, claim)
}

// ProjectTrustedV2 runs only on host-checked, closed results at diagnosis
// finalization. Invalid or irrelevant calls cannot become stored evidence.
func ProjectTrustedV2(taskID int64, runRef string, calls []e2runner.TrustedResult) ([]StoredObservationV2, error) {
	if taskID <= 0 || runRef == "" {
		return nil, db.ErrDiagnosticCandidate
	}
	out := make([]StoredObservationV2, 0)
	seen, seenAttempts := map[string]bool{}, map[string]bool{}
	duplicateEvidence := false
	for _, call := range calls {
		if call.Call.Program != "" || call.Call.IntentID <= 0 || call.Execution.IntentID != call.Call.IntentID ||
			call.Execution.Scope.TaskRef != strconv.FormatInt(taskID, 10) || call.Execution.Scope.RunRef != runRef ||
			call.Call.CallRef == "" {
			continue
		}
		obs, _, ok := v2Observation(call, "html_nosniff_missing")
		if !ok {
			obs, _, ok = v2Observation(call, "server_version_banner")
		}
		if !ok || !obs.ExactFunction || strings.Contains(obs.Endpoint, "{") || call.Execution.PolicyRevision == "" {
			continue
		}
		if seen[call.Call.CallRef] || seenAttempts[obs.AttemptRef] {
			duplicateEvidence = true
			continue
		}
		seen[call.Call.CallRef], seenAttempts[obs.AttemptRef] = true, true
		out = append(out, StoredObservationV2{Revision: candidateScopedRevision, Source: makeSource(call, obs), HeaderMode: call.Execution.HeaderMode})
		if len(out) > MaxStoredObservationsV2 {
			return nil, db.ErrDiagnosticCandidate
		}
	}
	// A duplicate attempt cannot establish independent evidence. Avoid a
	// partial projection that would disagree with the immutable run verdict.
	if duplicateEvidence {
		return nil, nil
	}
	return out, nil
}

// AssessStoredV2 rechecks durable E² projections after a service restart.
// The untouched candidate evidence must match the separately stored primary
// call before any other projected call can count as independent evidence.
func AssessStoredV2(candidate db.DiagnosticCandidate, stored []StoredObservationV2) Result {
	if !SupportedV2(candidate) {
		return Result{Status: Unsupported, Reason: "type_not_supported"}
	}
	if candidate.Validate() != nil || len(stored) > MaxStoredObservationsV2 {
		return Result{Status: Insufficient, Reason: "candidate_evidence_unavailable"}
	}
	var original struct {
		Trusted e2runner.TrustedResult `json:"trusted"`
	}
	if json.Unmarshal([]byte(candidate.Evidence), &original) != nil || original.Trusted.Call.CallRef != candidate.CallRef || len(original.Trusted.Call.Observations) != 1 {
		return Result{Status: Insufficient, Reason: "candidate_evidence_unavailable"}
	}
	primary := original.Trusted
	// The candidate was captured before command drain; the independent
	// projection exists only after the host confirmed terminal audit closure.
	primary.Final, primary.ProcessDrained, primary.ProxyDrained = true, true, true
	primary.Execution.AuditComplete = true
	primary.Classification = "completed"
	scope, _ := json.Marshal(primary.Execution.Scope)
	scopeDigest := db.DiagnosticEvidenceDigest(string(scope))
	projected := make([]e2runner.TrustedResult, 0, len(stored))
	primaryFound := false
	for _, row := range stored {
		s := row.Source
		if !validStoredObservationV2(row) || row.Revision != stored[0].Revision || s.TaskRef != strconv.FormatInt(candidate.TaskID, 10) || s.RunRef != candidate.RunRef {
			return Result{Status: Insufficient, Reason: "stored_observation_invalid"}
		}
		if s.ScopeDigest != scopeDigest || s.PolicyRevision != primary.Execution.PolicyRevision || row.HeaderMode != primary.Execution.HeaderMode {
			if s.CallRef == candidate.CallRef || row.Revision == 0 {
				return Result{Status: Insufficient, Reason: "stored_observation_invalid"}
			}
			continue
		}
		if s.CallRef == candidate.CallRef {
			if primaryFound || s.IntentID != candidate.IntentID || !reflect.DeepEqual(s.Observation, primary.Call.Observations[0]) {
				return Result{Status: Insufficient, Reason: "primary_source_mismatch"}
			}
			primaryFound = true
			projected = append(projected, primary)
			continue
		}
		copy := primary
		copy.Execution.IntentID = s.IntentID
		copy.Call = e2proxy.CallResult{IntentID: s.IntentID, CallRef: s.CallRef, Observations: []e2proxy.HTTPObservation{s.Observation}, Final: true, AuditComplete: true}
		projected = append(projected, copy)
	}
	if !primaryFound {
		return Result{Status: Insufficient, Reason: "primary_source_unavailable"}
	}
	return assessV2(candidate, projected, func(call e2runner.TrustedResult, claim string) (e2proxy.HTTPObservation, string, bool) {
		return storedObservation(stored[0].Revision, call, claim)
	})
}

func validStoredObservationV2(row StoredObservationV2) bool {
	s := row.Source
	if (row.Revision != 0 && row.Revision != candidateScopedRevision) || s.CallRef == "" || s.IntentID <= 0 || s.TaskRef == "" || s.RunRef == "" || s.ScopeDigest == "" || s.PolicyRevision == "" ||
		s.Observation.Sequence == 0 || s.Observation.AttemptRef == "" || s.Observation.ObservedAt.IsZero() ||
		s.Digest != sourceDigest(s.Observation) || !s.Observation.ExactFunction || strings.Contains(s.Observation.Endpoint, "{") ||
		row.HeaderMode != e2proxy.HeaderPreserve && row.HeaderMode != e2proxy.HeaderProtectedSessionProposal && row.HeaderMode != e2proxy.HeaderTestAccountPassthrough {
		return false
	}
	return true
}
