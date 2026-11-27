package candidateverify

import (
	"regexp"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/responseevidence"
)

var accountRevisionRef = regexp.MustCompile(`^[0-9a-f]{64}$`)

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
		var evidence *responseevidence.Evidence
		if ev, ok := callResponseEvidence(call, obs); ok {
			evidence = &ev
		}
		// 행 상한(4096바이트) 안에서 응답 근거를 먼저 줄인다. Host 증빙이 혼자 상한을 넘으면 기존처럼 증빙 없는 관찰로 남기고
		// (저장만으로는 확정 근거가 못 됨), 그래도 넘으면 이 행을 저장하지 않는다.
		row, fitted := fitSelectedRow(row, evidence)
		if !fitted {
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
		(obs.RequestAuthContext == "" || obs.RequestAuthContext == "none" || obs.RequestAuthContext == "authorization" ||
			obs.RequestAuthContext == "cookie" || obs.RequestAuthContext == "authorization_and_cookie" || obs.RequestAuthContext == "custom" || obs.RequestAuthContext == "unknown") &&
		(obs.RequestAccountRef == "" || accountRevisionRef.MatchString(obs.RequestAccountRef)) &&
		(obs.RequestQueryDigest == "" || accountRevisionRef.MatchString(obs.RequestQueryDigest)) &&
		(obs.RequestHeaderDigest == "" || accountRevisionRef.MatchString(obs.RequestHeaderDigest)) &&
		(obs.ResponseMediaClass == "" || obs.ResponseMediaClass == "html" || obs.ResponseMediaClass == "json" ||
			obs.ResponseMediaClass == "plain_text" || obs.ResponseMediaClass == "xml" || obs.ResponseMediaClass == "binary" ||
			obs.ResponseMediaClass == "other" || obs.ResponseMediaClass == "unknown") &&
		s.Digest == sourceDigest(obs) && ValidResponseEvidence(row) &&
		(row.HeaderMode == e2proxy.HeaderPreserve || row.HeaderMode == e2proxy.HeaderProtectedSessionProposal ||
			row.HeaderMode == e2proxy.HeaderTestAccountPassthrough)
}
