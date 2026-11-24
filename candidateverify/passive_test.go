package candidateverify

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
)

func candidateFixture(t *testing.T, first, second string) db.DiagnosticCandidate {
	t.Helper()
	observation := func(ref, state string) e2proxy.HTTPObservation {
		return e2proxy.HTTPObservation{Sequence: 1, AttemptRef: ref, Method: "GET", Endpoint: "/documents", RuleRef: "approved-rule", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, HTMLSecurity: state, ObservedAt: time.Now().UTC()}
	}
	makeCall := func(ref string, obs e2proxy.HTTPObservation) map[string]any {
		return map[string]any{"trusted": map[string]any{
			"classification": "completed", "final": true, "output_truncated": false,
			"execution": map[string]any{"scope": map[string]string{"TaskRef": "1", "RunRef": "run"}, "intent_id": 2},
			"call":      map[string]any{"call_ref": ref, "intent_id": 2, "final": true, "audit_complete": true, "observations": []e2proxy.HTTPObservation{obs}},
		}}
	}
	primary := makeCall("call-a", observation("attempt-a", first))
	primary["related_calls"] = []any{makeCall("call-b", observation("attempt-b", second))}
	raw, err := json.Marshal(primary)
	if err != nil {
		t.Fatal(err)
	}
	return db.DiagnosticCandidate{TaskID: 1, IntentID: 2, RunRef: "run", CallRef: "call-a", VulnClass: "security_headers", VerificationClaim: "html_nosniff_missing", Name: "합성 헤더 후보", Summary: "모델은 확정이라고 주장", Classification: "completed", Verification: "manual_review_required", EvidenceVersion: 1, Evidence: string(raw), EvidenceDigest: db.DiagnosticEvidenceDigest(string(raw)), ObservedAt: time.Now().UTC()}
}

func TestPassiveHTMLHeaderEvidence(t *testing.T) {
	for _, tc := range []struct{ name, first, second, status, reason string }{
		{"independent-missing", "nosniff_missing", "nosniff_missing", Confirmed, "html_nosniff_missing_twice"},
		{"benign-present", "nosniff_present", "nosniff_present", Negative, "html_nosniff_present_twice"},
		{"conflict", "nosniff_missing", "nosniff_present", Insufficient, "observations_inconsistent"},
		{"no-evidence", "nosniff_missing", "", Insufficient, "independent_observation_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := candidateFixture(t, tc.first, tc.second)
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			got := Assess(c)
			if got.Status != tc.status || got.Reason != tc.reason {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestPassiveHTMLHeaderRejectsForgedIndependence(t *testing.T) {
	c := candidateFixture(t, "nosniff_missing", "nosniff_missing")
	var payload map[string]any
	if err := json.Unmarshal([]byte(c.Evidence), &payload); err != nil {
		t.Fatal(err)
	}
	related := payload["related_calls"].([]any)[0].(map[string]any)
	obs := related["trusted"].(map[string]any)["call"].(map[string]any)["observations"].([]any)[0].(map[string]any)
	obs["attempt_ref"] = "attempt-a"
	raw, _ := json.Marshal(payload)
	c.Evidence, c.EvidenceDigest = string(raw), db.DiagnosticEvidenceDigest(string(raw))
	if got := Assess(c); got.Status != Insufficient || got.Reason != "observation_not_independent" {
		t.Fatalf("got %+v", got)
	}
	c.VulnClass = "IDOR"
	if got := Assess(c); got.Status != Unsupported {
		t.Fatalf("IDOR must remain E3-owned: %+v", got)
	}
}

// 모의 모델이 모든 사례를 확정하라고 권해도 백엔드 근거 검사는 같은 판정을 낸다.
func TestRulesVersusMockModelRecommendations(t *testing.T) {
	fixtures := []struct{ first, second, truth string }{
		{"nosniff_missing", "nosniff_missing", Confirmed},
		{"nosniff_present", "nosniff_present", Negative},
		{"nosniff_missing", "", Insufficient},
	}
	for _, mockModel := range []bool{false, true} {
		correct, wrong, human := 0, 0, 0
		for _, fixture := range fixtures {
			candidate := candidateFixture(t, fixture.first, fixture.second)
			if mockModel {
				candidate.Summary = "모의 모델: 모두 확정 권고"
			}
			result := Assess(candidate)
			switch result.Status {
			case Confirmed:
				if fixture.truth == Confirmed {
					correct++
				} else {
					wrong++
				}
			case Insufficient, Unsupported:
				human++
			}
		}
		if correct != 1 || wrong != 0 || human != 1 {
			t.Fatalf("mock=%t correct=%d wrong=%d human=%d", mockModel, correct, wrong, human)
		}
	}
}

func TestServerBannerUsesTrustedHeadersNotDocumentationText(t *testing.T) {
	for _, tc := range []struct{ name, first, second, status, reason string }{
		{"version-repeated", "2.8", "2.8", Confirmed, "server_version_banner_twice"},
		{"versionless-benign", "", "", Negative, "server_versionless_banner_twice"},
		{"different-versions", "2.8", "2.9", Insufficient, "observations_inconsistent"},
		{"documentation-only", "none", "none", Insufficient, "primary_observation_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := candidateFixture(t, "", "")
			c.VulnClass, c.VerificationClaim = "information_disclosure", "server_version_banner"
			var payload map[string]any
			if err := json.Unmarshal([]byte(c.Evidence), &payload); err != nil {
				t.Fatal(err)
			}
			payload["tool_output"] = "A documentation example says Server: Example/99.9"
			related := payload["related_calls"].([]any)[0].(map[string]any)
			for i, envelope := range []map[string]any{payload, related} {
				version := tc.first
				if i == 1 {
					version = tc.second
				}
				obs := envelope["trusted"].(map[string]any)["call"].(map[string]any)["observations"].([]any)[0].(map[string]any)
				if version != "none" {
					obs["technologies"] = []e2proxy.HTTPTechnology{{Name: "Example", Version: version, Header: "Server"}}
				}
			}
			raw, _ := json.Marshal(payload)
			c.Evidence, c.EvidenceDigest = string(raw), db.DiagnosticEvidenceDigest(string(raw))
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			got := Assess(c)
			if got.Status != tc.status || got.Reason != tc.reason {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestPassiveEvidenceRejectsSelectedOrTruncatedCall(t *testing.T) {
	for _, altered := range []string{"multiple-responses", "truncated"} {
		t.Run(altered, func(t *testing.T) {
			c := candidateFixture(t, "nosniff_missing", "nosniff_missing")
			var payload map[string]any
			if err := json.Unmarshal([]byte(c.Evidence), &payload); err != nil {
				t.Fatal(err)
			}
			call := payload["trusted"].(map[string]any)["call"].(map[string]any)
			if altered == "truncated" {
				call["observations_truncated"] = true
			} else {
				observations := call["observations"].([]any)
				call["observations"] = append(observations, observations[0])
			}
			raw, _ := json.Marshal(payload)
			c.Evidence, c.EvidenceDigest = string(raw), db.DiagnosticEvidenceDigest(string(raw))
			if got := Assess(c); got.Status != Insufficient || got.Reason != "primary_observation_unavailable" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
