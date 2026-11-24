package candidateverify

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
)

func v2Fixture(t *testing.T, first, second string) (db.DiagnosticCandidate, []e2runner.TrustedResult) {
	t.Helper()
	c := candidateFixture(t, first, "")
	var envelope struct {
		Trusted e2runner.TrustedResult `json:"trusted"`
	}
	if err := json.Unmarshal([]byte(c.Evidence), &envelope); err != nil {
		t.Fatal(err)
	}
	primary := envelope.Trusted
	primary.Call.Observations[0].ExactFunction = true
	// The original candidate was captured by the same trusted adapter.
	var candidateEnvelope map[string]any
	if err := json.Unmarshal([]byte(c.Evidence), &candidateEnvelope); err != nil {
		t.Fatal(err)
	}
	candidateEnvelope["trusted"].(map[string]any)["call"].(map[string]any)["observations"].([]any)[0].(map[string]any)["exact_function"] = true
	candidateRaw, _ := json.Marshal(candidateEnvelope)
	c.Evidence, c.EvidenceDigest = string(candidateRaw), db.DiagnosticEvidenceDigest(string(candidateRaw))
	primary.ProcessDrained, primary.ProxyDrained, primary.Execution.AuditComplete = true, true, true
	other := primary
	other.Call = e2proxy.CloneCallResult(primary.Call)
	other.Call.CallRef = "call-after-save"
	other.Call.Observations[0].AttemptRef = "attempt-after-save"
	other.Call.Observations[0].Sequence = 2
	other.Call.Observations[0].HTMLSecurity = second
	scope, _ := json.Marshal(primary.Execution.Scope)
	c.SaveFence = &db.DiagnosticCandidateSaveFence{AttemptHighWater: 1, TaskRef: "1", RunRef: c.RunRef, ScopeDigest: db.DiagnosticEvidenceDigest(string(scope)), PolicyRevision: primary.Execution.PolicyRevision, CapturedAt: time.Now().UTC()}
	return c, []e2runner.TrustedResult{primary, other}
}

func TestPassiveV2RequiresPostSaveAttempt(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		fence       uint64
		second      uint64
	}{
		{"both-before-save", "nosniff_missing", 2, 2},
		{"normal-before-save", "nosniff_present", 2, 2},
		{"same-sequence", "nosniff_missing", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := v2Fixture(t, tc.state, tc.state)
			c.SaveFence.AttemptHighWater = tc.fence
			calls[1].Call.Observations[0].Sequence = tc.second
			got := AssessV2(c, calls)
			if got.Status != Insufficient {
				t.Fatalf("pre-save observation promoted: %+v", got)
			}
		})
	}
	c, calls := v2Fixture(t, "nosniff_missing", "nosniff_missing")
	calls[1].Call.Observations[0].ObservedAt = calls[0].Call.Observations[0].ObservedAt
	c.SaveFence = nil
	if got := AssessV2(c, calls); got.Status != Insufficient {
		t.Fatalf("equal timestamps without trusted save order promoted: %+v", got)
	}
	c, calls = v2Fixture(t, "nosniff_missing", "nosniff_present")
	c.SaveFence.AttemptHighWater = 2
	third := calls[1]
	third.Call = e2proxy.CloneCallResult(third.Call)
	third.Call.CallRef = "third-call"
	third.Call.Observations[0].AttemptRef = "third-attempt"
	third.Call.Observations[0].Sequence = 3
	third.Call.Observations[0].HTMLSecurity = "nosniff_missing"
	if got := AssessV2(c, append(calls, third)); got.Status != Insufficient || got.Reason != "observations_inconsistent" {
		t.Fatalf("pre-save conflict ignored: %+v", got)
	}
}

func TestPassiveV2PostSaveObservation(t *testing.T) {
	for _, tc := range []struct{ name, first, second, status, reason string }{
		{"confirmed", "nosniff_missing", "nosniff_missing", Confirmed, "html_nosniff_missing_twice"},
		{"normal", "nosniff_present", "nosniff_present", Negative, "html_nosniff_present_twice"},
		{"conflict", "nosniff_missing", "nosniff_present", Insufficient, "observations_inconsistent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := v2Fixture(t, tc.first, tc.second)
			if got := AssessV2(c, calls[:1]); got.Status != Insufficient || got.Reason != "independent_observation_missing" {
				t.Fatalf("before later observation: %+v", got)
			}
			got := AssessV2(c, calls)
			if got.Status != tc.status || got.Reason != tc.reason {
				t.Fatalf("after later observation: %+v", got)
			}
			if got.Status == Confirmed && (len(got.Sources) != 2 || !ValidSourcesV2(got)) {
				t.Fatalf("source provenance: %+v", got)
			}
			if got.Status == Confirmed {
				if !ValidateStoredV2(c, got) {
					t.Fatal("stored verdict validation failed")
				}
				wrongFence := c
				copyFence := *c.SaveFence
				copyFence.AttemptHighWater = 2
				wrongFence.SaveFence = &copyFence
				if ValidateStoredV2(wrongFence, got) {
					t.Fatal("tampered save order accepted")
				}
				wrongTime := got
				wrongTime.Sources = append([]SourceV2(nil), got.Sources...)
				wrongTime.Sources[1].Observation.ObservedAt = wrongTime.Sources[1].Observation.ObservedAt.Add(time.Second)
				if ValidateStoredV2(c, wrongTime) {
					t.Fatal("tampered source time accepted")
				}
				got.Sources[1].Digest = "tampered"
				if ValidateStoredV2(c, got) {
					t.Fatal("tampered source accepted")
				}
			}
		})
	}
}

func TestPassiveV2RejectsUnboundOrCopiedEvidence(t *testing.T) {
	c, calls := v2Fixture(t, "nosniff_missing", "nosniff_missing")
	tests := []struct {
		name   string
		mutate func([]e2runner.TrustedResult)
	}{
		{"same-attempt", func(x []e2runner.TrustedResult) {
			x[1].Call.Observations[0].AttemptRef = x[0].Call.Observations[0].AttemptRef
		}},
		{"other-run", func(x []e2runner.TrustedResult) { x[1].Execution.Scope.RunRef = "other" }},
		{"other-rule", func(x []e2runner.TrustedResult) { x[1].Call.Observations[0].RuleRef = "other" }},
		{"other-function", func(x []e2runner.TrustedResult) { x[1].Call.Observations[0].Endpoint = "/other" }},
		{"not-audited", func(x []e2runner.TrustedResult) { x[1].Call.AuditComplete = false }},
		{"truncated", func(x []e2runner.TrustedResult) { x[1].Call.Observations[0].ResponseTruncated = true }},
		{"resource-pattern", func(x []e2runner.TrustedResult) {
			x[0].Call.Observations[0].Endpoint = "/documents/{resource}"
			x[1].Call.Observations[0].Endpoint = "/documents/{resource}"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			copyCalls := []e2runner.TrustedResult{calls[0], calls[1]}
			copyCalls[0].Call = e2proxy.CloneCallResult(calls[0].Call)
			copyCalls[1].Call = e2proxy.CloneCallResult(calls[1].Call)
			tc.mutate(copyCalls)
			if got := AssessV2(c, copyCalls); got.Status == Confirmed {
				t.Fatalf("unsafe confirmation: %+v", got)
			}
		})
	}
	wrong := c
	wrong.EvidenceDigest = "tampered"
	if got := AssessV2(wrong, calls); got.Status == Confirmed {
		t.Fatalf("tampered candidate confirmed: %+v", got)
	}
	c.VulnClass = "IDOR"
	if got := AssessV2(c, calls); got.Status != Unsupported {
		t.Fatalf("E3 ownership bypassed: %+v", got)
	}
}

func TestPassiveV2ServerBannerReusesSameSourceRules(t *testing.T) {
	for _, tc := range []struct{ first, second, status string }{
		{"2.8", "2.8", Confirmed},
		{"", "", Negative},
		{"2.8", "2.9", Insufficient},
	} {
		c, calls := v2Fixture(t, "nosniff_missing", "nosniff_missing")
		c.VulnClass, c.VerificationClaim = "information_disclosure", "server_version_banner"
		for i := range calls {
			version := tc.first
			if i == 1 {
				version = tc.second
			}
			calls[i].Call.Observations[0].HTMLSecurity = ""
			calls[i].Call.Observations[0].Technologies = []e2proxy.HTTPTechnology{{Name: "Example", Version: version, Header: "Server"}}
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(c.Evidence), &envelope); err != nil {
			t.Fatal(err)
		}
		obs := envelope["trusted"].(map[string]any)["call"].(map[string]any)["observations"].([]any)[0].(map[string]any)
		delete(obs, "html_security")
		obs["technologies"] = calls[0].Call.Observations[0].Technologies
		raw, _ := json.Marshal(envelope)
		c.Evidence, c.EvidenceDigest = string(raw), db.DiagnosticEvidenceDigest(string(raw))
		got := AssessV2(c, calls)
		if got.Status != tc.status {
			t.Fatalf("banner %q/%q: %+v", tc.first, tc.second, got)
		}
		if (got.Status == Confirmed || got.Status == Negative) && !ValidateStoredV2(c, got) {
			t.Fatalf("stored banner not validated: %+v", got)
		}
	}
}

// Fixed before any real-model evaluation. This is the rule-only baseline,
// not a substitute for measuring an approved OpenRouter run.
func TestPassiveV2RuleOnlyGoldSet(t *testing.T) {
	type goldCase struct {
		name, first, second, want string
		mutate                    func([]e2runner.TrustedResult)
	}
	cases := []goldCase{
		{"missing-header", "nosniff_missing", "nosniff_missing", Confirmed, nil},
		{"normal-header", "nosniff_present", "nosniff_present", Negative, nil},
		{"unrelated-function", "nosniff_missing", "nosniff_missing", Insufficient, func(x []e2runner.TrustedResult) { x[1].Call.Observations[0].Endpoint = "/other" }},
		{"no-second-evidence", "nosniff_missing", "nosniff_missing", Insufficient, func(x []e2runner.TrustedResult) { x[1].Execution.Scope.RunRef = "other" }},
		{"conflicting-setting", "nosniff_missing", "nosniff_present", Insufficient, nil},
		{"copied-attempt", "nosniff_missing", "nosniff_missing", Insufficient, func(x []e2runner.TrustedResult) {
			x[1].Call.Observations[0].AttemptRef = x[0].Call.Observations[0].AttemptRef
		}},
		{"other-policy", "nosniff_missing", "nosniff_missing", Insufficient, func(x []e2runner.TrustedResult) { x[1].Execution.PolicyRevision = "other-policy" }},
	}
	correctConfirmed, falseConfirmed, correctNegative, falseNegative, human := 0, 0, 0, 0, 0
	for _, tc := range cases {
		c, calls := v2Fixture(t, tc.first, tc.second)
		if tc.mutate != nil {
			tc.mutate(calls)
		}
		got := AssessV2(c, calls)
		switch got.Status {
		case Confirmed:
			if tc.want == Confirmed {
				correctConfirmed++
			} else {
				falseConfirmed++
			}
		case Negative:
			if tc.want == Negative {
				correctNegative++
			} else {
				falseNegative++
			}
		default:
			human++
		}
		if got.Status != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, got.Status, tc.want)
		}
	}
	if correctConfirmed != 1 || falseConfirmed != 0 || correctNegative != 1 || falseNegative != 0 || human != 5 {
		t.Fatalf("rule baseline confirmed=%d false-confirmed=%d negative=%d false-negative=%d review=%d", correctConfirmed, falseConfirmed, correctNegative, falseNegative, human)
	}
}
