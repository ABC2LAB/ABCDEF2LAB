package learnedlogin

import (
	"strconv"
	"strings"
)

// VerifyFieldSources ties each dynamic field's original issuance request to
// a separate completed transport observation. It does not grant permission
// for a fresh GET; the new E② call still enforces the current approval scope.
func VerifyFieldSources(row Row, runRef string, evidence *ExplorationEvidence,
	lookup func(callRef, attemptID string) (AuditedSend, bool)) Reason {
	if len(row.FieldSources) == 0 {
		return ""
	}
	if row.RunRef != runRef {
		return ReasonTransportUnverified
	}
	for _, source := range row.FieldSources {
		if source.SourceMethod != "GET" || !validPath(source.SourcePath) || source.SourcePath == row.Path ||
			source.SourceStatus < 200 || source.SourceStatus > 299 || !validRef(source.SourceRef) {
			return ReasonTransportUnverified
		}
		switch row.Stage {
		case StageExploration:
			if evidence == nil || evidence.RunRef != runRef || !evidence.valid() {
				return ReasonTransportUnverified
			}
			seq, err := strconv.ParseUint(source.SourceRef, 10, 64)
			if err != nil || seq == 0 || strconv.FormatUint(seq, 10) != source.SourceRef {
				return ReasonTransportUnverified
			}
			matched := false
			for _, send := range evidence.Sends {
				if send.Seq != seq {
					continue
				}
				matched = send.Method == "GET" && send.Path == source.SourcePath && send.Status == source.SourceStatus &&
					send.BoundaryForwarded && send.ApprovedTarget && send.ObservationForwarded && send.ResponseReceived
			}
			if !matched {
				return ReasonTransportUnverified
			}
		case StageDiagnosis:
			callRef, attemptID, ok := strings.Cut(source.SourceRef, "/")
			if !ok || callRef == "" || attemptID == "" || strings.Contains(attemptID, "/") || lookup == nil {
				return ReasonTransportUnverified
			}
			send, found := lookup(callRef, attemptID)
			if !found || send.CallRef != callRef || send.AttemptID != attemptID || send.RunRef != runRef || !send.ModelCall || send.Method != "GET" ||
				!send.Allowed || !send.RequestWritten || !send.ResponseReceived || !send.ResponseComplete || !send.AuditComplete ||
				send.StatusCode != source.SourceStatus {
				return ReasonTransportUnverified
			}
		default:
			return ReasonTransportUnverified
		}
	}
	return ""
}
