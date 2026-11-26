package candidateverify

import (
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
)

func storedV2Fixture(t *testing.T, first, second string) (db.DiagnosticCandidate, []StoredObservationV2) {
	t.Helper()
	candidate, calls := v2Fixture(t, first, second)
	for i := range calls {
		calls[i].Execution.PolicyRevision = "synthetic-policy"
		calls[i].Execution.HeaderMode = e2proxy.HeaderPreserve
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(candidate.Evidence), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["trusted"] = calls[0]
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Evidence, candidate.EvidenceDigest = string(raw), db.DiagnosticEvidenceDigest(string(raw))
	candidate.SaveFence.PolicyRevision = "synthetic-policy"
	stored, err := ProjectTrustedV2(candidate.TaskID, candidate.RunRef, calls)
	if err != nil || len(stored) != 2 {
		t.Fatalf("projected calls = %d, %v", len(stored), err)
	}
	return candidate, stored
}

func TestStoredV2RechecksPersistedIndependentCalls(t *testing.T) {
	for _, tc := range []struct{ name, first, second, verdict string }{
		{"confirmed", "nosniff_missing", "nosniff_missing", Confirmed},
		{"negative", "nosniff_present", "nosniff_present", Negative},
		{"conflict", "nosniff_missing", "nosniff_present", Insufficient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, stored := storedV2Fixture(t, tc.first, tc.second)
			if got := AssessStoredV2(candidate, stored); got.Status != tc.verdict {
				t.Fatalf("stored verdict = %+v", got)
			}
			if got := AssessStoredV2(candidate, stored[:1]); got.Status != Insufficient {
				t.Fatalf("single stored call promoted = %+v", got)
			}
		})
	}
}

func TestStoredV2RejectsChangedOrUnboundProjection(t *testing.T) {
	candidate, stored := storedV2Fixture(t, "nosniff_missing", "nosniff_missing")
	for _, tc := range []struct {
		name   string
		mutate func([]StoredObservationV2)
	}{
		{"different-run", func(rows []StoredObservationV2) { rows[1].Source.RunRef = "other-run" }},
		{"different-policy", func(rows []StoredObservationV2) { rows[1].Source.PolicyRevision = "other-policy" }},
		{"same-attempt", func(rows []StoredObservationV2) {
			rows[1].Source.Observation.AttemptRef = rows[0].Source.Observation.AttemptRef
			rows[1].Source.Digest = sourceDigest(rows[1].Source.Observation)
		}},
		{"changed-setting", func(rows []StoredObservationV2) { rows[1].Source.Observation.HTMLSecurity = "nosniff_present" }},
		{"changed-primary", func(rows []StoredObservationV2) {
			rows[0].Source.Observation.StatusCode = 500
			rows[0].Source.Digest = sourceDigest(rows[0].Source.Observation)
		}},
		{"missing-primary", func(rows []StoredObservationV2) { rows[0] = rows[1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copyRows := append([]StoredObservationV2(nil), stored...)
			tc.mutate(copyRows)
			if got := AssessStoredV2(candidate, copyRows); got.Status == Confirmed {
				t.Fatalf("unbound stored projection confirmed: %+v", got)
			}
		})
	}
	wrong := candidate
	wrong.EvidenceDigest = "altered"
	if got := AssessStoredV2(wrong, stored); got.Status == Confirmed {
		t.Fatalf("altered original confirmed: %+v", got)
	}
}

func TestProjectTrustedV2IgnoresDuplicateAttempt(t *testing.T) {
	candidate, calls := v2Fixture(t, "nosniff_missing", "nosniff_missing")
	for i := range calls {
		calls[i].Execution.PolicyRevision = "synthetic-policy"
	}
	calls[1].Call.Observations[0].AttemptRef = calls[0].Call.Observations[0].AttemptRef
	stored, err := ProjectTrustedV2(candidate.TaskID, candidate.RunRef, calls)
	if err != nil || len(stored) != 0 {
		t.Fatalf("duplicate attempt projection: %d, %v", len(stored), err)
	}
}

func TestStoredObservationRevisionUsesOriginalBannerRules(t *testing.T) {
	_, calls := v2Fixture(t, "nosniff_missing", "")
	calls[0].Call.Observations[0].Technologies = []e2proxy.HTTPTechnology{
		{Header: "Server", Name: "Example", Version: "1"}, {Header: "X-Powered-By", Name: "Framework"},
	}
	if _, _, ok := storedObservation(0, calls[0], "server_version_banner"); ok {
		t.Fatal("legacy review gained new evidence")
	}
	if _, _, ok := storedObservation(candidateScopedRevision, calls[0], "server_version_banner"); !ok {
		t.Fatal("current review lost valid server header")
	}
}
