package candidateverify

import (
	"encoding/json"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

// ProjectTrustedSelected preserves bounded, safe E² observations for later
// candidate review. It makes no vulnerability verdict and applies no
// vulnerability-type rule. StoredObservationV2 is reused only as a storage
// envelope; revision 2 keeps these rows distinct from passive/v2 evidence.
func ProjectTrustedSelected(taskID int64, runRef string, calls []e2runner.TrustedResult) ([]StoredObservationV2, error) {
	if taskID <= 0 || runRef == "" {
		return nil, db.ErrDiagnosticCandidate
	}
	out := make([]StoredObservationV2, 0)
	seenCalls, seenAttempts := map[string]bool{}, map[string]bool{}
	for _, call := range calls {
		if call.Call.Program != "" || call.Call.IntentID <= 0 || call.Execution.IntentID != call.Call.IntentID ||
			call.Execution.Scope.TaskRef != strconv.FormatInt(taskID, 10) || call.Execution.Scope.RunRef != runRef ||
			call.Call.CallRef == "" || len(call.Call.CallRef) > 128 || call.Execution.PolicyRevision == "" ||
			!call.Final || !call.ProcessDrained || !call.ProxyDrained || !call.Execution.AuditComplete ||
			call.Classification != "completed" || call.OutputTruncated || !call.Call.Final ||
			!call.Call.AuditComplete || call.Call.ObservationsTruncated || len(call.Call.Observations) != 1 {
			continue
		}
		obs := call.Call.Observations[0]
		if obs.Sequence == 0 || obs.ObservedAt.IsZero() || obs.AttemptRef == "" || len(obs.AttemptRef) > 128 || obs.Method == "" ||
			obs.Endpoint == "" || obs.RuleRef == "" || obs.Decision != "allowed" || !obs.ForwardAttempted ||
			!obs.AuditComplete || !obs.ResponseComplete || obs.ResponseTruncated ||
			(call.Execution.HeaderMode != e2proxy.HeaderPreserve && call.Execution.HeaderMode != e2proxy.HeaderProtectedSessionProposal &&
				call.Execution.HeaderMode != e2proxy.HeaderTestAccountPassthrough) {
			continue
		}
		if seenCalls[call.Call.CallRef] || seenAttempts[obs.AttemptRef] {
			// A duplicated audit reference is ambiguous. Do not persist a partial
			// set that a later reviewer might mistake for independent evidence.
			return nil, nil
		}
		row := StoredObservationV2{Revision: 2, Source: makeSource(call, obs), HeaderMode: call.Execution.HeaderMode,
			SelectedProof: call.SelectedProof}
		raw, err := json.Marshal(row)
		if err != nil || len(raw) > 4096 {
			// The observation store has a fixed per-row limit. Keep the run
			// record available without persisting an incomplete large row.
			continue
		}
		seenCalls[call.Call.CallRef], seenAttempts[obs.AttemptRef] = true, true
		out = append(out, row)
		if len(out) > MaxStoredObservationsV2 {
			return nil, db.ErrDiagnosticCandidate
		}
	}
	return out, nil
}

// ValidSelectedStoredObservation checks a saved projection's shape and
// provenance without turning its content into a vulnerability verdict.
func ValidSelectedStoredObservation(taskID int64, runRef string, row StoredObservationV2) bool {
	s, obs := row.Source, row.Source.Observation
	return taskID > 0 && runRef != "" && row.Revision == 2 &&
		s.TaskRef == strconv.FormatInt(taskID, 10) && s.RunRef == runRef &&
		s.CallRef != "" && len(s.CallRef) <= 128 && s.IntentID > 0 &&
		s.ScopeDigest != "" && s.PolicyRevision != "" &&
		obs.Sequence > 0 && !obs.ObservedAt.IsZero() && obs.AttemptRef != "" && len(obs.AttemptRef) <= 128 &&
		obs.Method != "" && obs.Endpoint != "" && obs.RuleRef != "" && obs.Decision == "allowed" &&
		obs.ForwardAttempted && obs.ResponseComplete && !obs.ResponseTruncated && obs.AuditComplete &&
		s.Digest == sourceDigest(obs) &&
		(row.HeaderMode == e2proxy.HeaderPreserve || row.HeaderMode == e2proxy.HeaderProtectedSessionProposal ||
			row.HeaderMode == e2proxy.HeaderTestAccountPassthrough)
}
