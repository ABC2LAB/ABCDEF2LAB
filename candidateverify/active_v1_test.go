package candidateverify

import (
	"strconv"
	"testing"
)

func activeFixture(t *testing.T, first, second string) (ActiveBindingV1, []StoredObservationV2, func() Result) {
	t.Helper()
	candidate, stored := storedV2Fixture(t, first, second)
	binding := ActiveBindingV1{TaskID: candidate.TaskID + 50, RunRef: "run-followup-synthetic", ExactPath: stored[0].Source.Observation.Endpoint, CaseRef: "candidate-plan-synthetic"}
	for i := range stored {
		stored[i].Source.TaskRef = strconv.FormatInt(binding.TaskID, 10)
		stored[i].Source.RunRef = binding.RunRef
		stored[i].Source.CallRef += "-followup"
		stored[i].Source.Observation.AttemptRef += "-followup"
		stored[i].Source.Digest = sourceDigest(stored[i].Source.Observation)
	}
	return binding, stored, func() Result { return AssessActiveStoredV1(candidate, binding, stored) }
}

func TestActiveV1RequiresTwoIndependentFollowupObservations(t *testing.T) {
	for _, tc := range []struct{ name, first, second, status string }{
		{"confirmed", "nosniff_missing", "nosniff_missing", Confirmed},
		{"negative", "nosniff_present", "nosniff_present", Negative},
		{"conflict", "nosniff_missing", "nosniff_present", Insufficient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding, stored, assess := activeFixture(t, tc.first, tc.second)
			if got := assess(); got.Status != tc.status {
				t.Fatalf("followup verdict: %+v", got)
			}
			candidate, _ := storedV2Fixture(t, tc.first, tc.second)
			if got := AssessActiveStoredV1(candidate, binding, stored[:1]); got.Status != Insufficient {
				t.Fatalf("single call confirmed: %+v", got)
			}
		})
	}
}

func TestActiveV1RejectsUnboundOrUnreviewableEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]StoredObservationV2)
	}{
		{"same-attempt", func(x []StoredObservationV2) {
			x[1].Source.Observation.AttemptRef = x[0].Source.Observation.AttemptRef
			x[1].Source.Digest = sourceDigest(x[1].Source.Observation)
		}},
		{"other-run", func(x []StoredObservationV2) { x[1].Source.RunRef = "other" }},
		{"other-task", func(x []StoredObservationV2) { x[1].Source.TaskRef = "999" }},
		{"other-path", func(x []StoredObservationV2) {
			x[1].Source.Observation.Endpoint = "/other"
			x[1].Source.Digest = sourceDigest(x[1].Source.Observation)
		}},
		{"not-audited", func(x []StoredObservationV2) {
			x[1].Source.Observation.AuditComplete = false
			x[1].Source.Digest = sourceDigest(x[1].Source.Observation)
		}},
		{"changed-header-without-digest", func(x []StoredObservationV2) { x[1].Source.Observation.HTMLSecurity = "nosniff_present" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, _ := storedV2Fixture(t, "nosniff_missing", "nosniff_missing")
			binding, stored, _ := activeFixture(t, "nosniff_missing", "nosniff_missing")
			tc.mutate(stored)
			if got := AssessActiveStoredV1(candidate, binding, stored); got.Status == Confirmed {
				t.Fatalf("unbound evidence confirmed: %+v", got)
			}
		})
	}
}
