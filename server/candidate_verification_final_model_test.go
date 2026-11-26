package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/llmpolicy"
)

func syntheticCandidateAssessmentContext(t *testing.T) CandidateAssessmentContext {
	t.Helper()
	originalSource := candidateverify.StoredObservationV2{Revision: 2, HeaderMode: e2proxy.HeaderPreserve,
		Source: candidateverify.SourceV2{CallRef: "original-call", Digest: strings.Repeat("a", 64),
			Observation: e2proxy.HTTPObservation{AttemptRef: "original-attempt", Method: "GET", Endpoint: "/documents", ExactFunction: true, RequestAuthContext: "none"}}}
	followup := candidateverify.StoredObservationV2{Revision: 2, HeaderMode: e2proxy.HeaderPreserve,
		Source: candidateverify.SourceV2{CallRef: "followup-call", Digest: strings.Repeat("b", 64),
			Observation: e2proxy.HTTPObservation{AttemptRef: "followup-attempt", Method: "GET", Endpoint: "/documents", ExactFunction: true, RequestAuthContext: "none"}}}
	evidence, err := json.Marshal(map[string]any{"trusted": map[string]any{
		"classification": "completed", "output_truncated": false,
		"execution": map[string]any{"scope": map[string]string{"TaskRef": "42", "RunRef": "original-run"}, "intent_id": 7},
		"call":      map[string]any{"call_ref": "original-call", "intent_id": 7}}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := db.DiagnosticCandidate{ID: 1, TaskID: 42, IntentID: 7, RunRef: "original-run", CallRef: "original-call",
		VulnClass: "information_disclosure", Name: "Synthetic disclosure", Summary: "Synthetic observation",
		Classification: "completed", Verification: "manual_review_required", EvidenceVersion: 1,
		Evidence: string(evidence), EvidenceDigest: db.DiagnosticEvidenceDigest(string(evidence)), ObservedAt: time.Now().UTC()}
	if candidate.Validate() != nil {
		t.Fatal("synthetic candidate malformed")
	}
	return CandidateAssessmentContext{Original: CandidateSelectedContext{RequestID: 5, TaskID: 42, Candidate: candidate,
		RunRef: "original-run", OriginalObservation: &originalSource},
		Plan: CandidateLLMPlan{CandidateID: candidate.ID, EvidenceDigest: candidate.EvidenceDigest}, Followup: []candidateverify.StoredObservationV2{followup}}
}

func TestCandidateFinalModelUsesPinnedSyntheticProviderPostgres(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	c := syntheticCandidateAssessmentContext(t)
	var calls atomic.Int64
	var modelMode atomic.Value
	modelMode.Store("valid")
	modelHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("model escaped pinned endpoint: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid model request")
		}
		body, _ := json.Marshal(request)
		if !strings.Contains(string(body), "followup-call") || !strings.Contains(string(body), "original-call") {
			t.Error("model lacked independent observations")
		}
		verdict := CandidateFinalVerdict{Version: "selected/final-v1", CandidateID: 1, Verdict: "confirmed", Claim: "Observed defect",
			NormalExplanation: "Normal case considered", Reason: "Independent observations agree", Limitations: "Only one read path",
			Remediation: "Remove disclosure", VerificationCriteria: "Repeat the read", Sources: []candidateVerificationSourceRef{
				{CallRef: "original-call", AttemptRef: "original-attempt", ObservationDigest: strings.Repeat("a", 64)},
				{CallRef: "followup-call", AttemptRef: "followup-attempt", ObservationDigest: strings.Repeat("b", 64)},
			}}
		switch modelMode.Load().(string) {
		case "unsupported-positive":
			verdict.Sources[1].CallRef = "unrelated-call"
		case "unsupported-negative":
			verdict.Verdict = "negative"
			verdict.Sources = verdict.Sources[:1]
		case "copied-source":
			verdict.Sources[1] = verdict.Sources[0]
		}
		content, _ := json.Marshal(verdict)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "synthetic-final", "object": "chat.completion", "model": "local:selected-final-test",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 120, "completion_tokens": 70, "total_tokens": 190}})
	}))
	defer modelHost.Close()
	profile := &db.LLMProfile{Name: fmt.Sprintf("selected-final-test-%d", time.Now().UnixNano()), Format: "openai", Model: "local:selected-final-test",
		BaseURL: modelHost.URL + "/v1", APIKey: "synthetic-local-key", Streaming: false, ContextWindowK: 64, MaxTokens: 512}
	profileID, err := s.m.pg.SaveProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.DeleteProfile(profileID) })
	manifestDir := t.TempDir()
	if os.Chmod(manifestDir, 0700) != nil {
		t.Fatal("synthetic manifest directory unavailable")
	}
	manifestPath := filepath.Join(manifestDir, "models.json")
	manifest := fmt.Sprintf(`{"version":1,"profiles":[{"profile_id":%d,"format":"openai","model":"local:selected-final-test","base_url":%q,"addresses":["127.0.0.1"]}]}`, profileID, profile.BaseURL)
	if _, err := llmpolicy.ParseProfileManifest([]byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(manifestPath, []byte(manifest), 0600) != nil {
		t.Fatal("synthetic manifest unavailable")
	}
	budget, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 1, InputTokens: 65536, OutputTokens: 512, DurationSeconds: 180}, 65536, 512,
		func(diagnosisprep.ModelUsage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckFirstDiagnosisModelOptions(FirstDiagnosisModelOptions{Mode: "local_only", LocalManifestPath: manifestPath, Budget: budget},
		llmpolicy.ProfileTarget{ID: profileID, Format: profile.Format, Model: profile.Model, BaseURL: profile.BaseURL}); err != nil {
		t.Fatal("synthetic model manifest mismatch", err)
	}
	model, err := NewSelectedCandidateModel(t.Context(), s.m.pg.DB, profileID, FirstDiagnosisModelOptions{Mode: "local_only", LocalManifestPath: manifestPath, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	got, err := model.Assess(t.Context(), c, func(context.Context) error { return nil })
	if err != nil || got.Verdict != "confirmed" || calls.Load() != 1 || budget.Snapshot().Requests != 1 {
		t.Fatalf("synthetic final model: calls=%d verdict=%s err=%v", calls.Load(), got.Verdict, err)
	}
	for _, tc := range []struct {
		name, mode, wantVerdict, wantReason string
		change                              func(*CandidateAssessmentContext)
	}{
		{"unsupported-positive", "unsupported-positive", "", "", nil},
		{"unsupported-negative", "unsupported-negative", "", "", nil},
		{"copied-source", "copied-source", "", "", nil},
		{"account-mismatch", "valid", "insufficient", "context_mismatch", func(c *CandidateAssessmentContext) {
			c.Original.OriginalObservation.Source.Observation.RequestAuthContext = "authorization"
		}},
		{"input-mismatch", "valid", "insufficient", "input_context_mismatch", func(c *CandidateAssessmentContext) {
			c.Followup[0].Source.Observation.RequestQueryDigest = strings.Repeat("c", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modelMode.Store(tc.mode)
			caseContext := syntheticCandidateAssessmentContext(t)
			if tc.change != nil {
				tc.change(&caseContext)
			}
			caseBudget, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 1, InputTokens: 65536, OutputTokens: 512, DurationSeconds: 180}, 65536, 512,
				func(diagnosisprep.ModelUsage) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			caseModel, err := NewSelectedCandidateModel(t.Context(), s.m.pg.DB, profileID, FirstDiagnosisModelOptions{Mode: "local_only", LocalManifestPath: manifestPath, Budget: caseBudget})
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			got, err := caseModel.Assess(t.Context(), caseContext, func(context.Context) error { return nil })
			if calls.Load() != before+1 || caseBudget.Snapshot().Requests != 1 {
				t.Fatal("synthetic provider was not called exactly once")
			}
			if tc.wantVerdict == "" {
				if err == nil || got.Verdict == "confirmed" || got.Verdict == "negative" {
					t.Fatalf("unsupported model recommendation became decisive: %+v %v", got, err)
				}
			} else if err != nil || got.Verdict != tc.wantVerdict || got.Reason != tc.wantReason {
				t.Fatalf("mismatched observation became decisive: %+v %v", got, err)
			}
		})
	}
}

func TestCandidateFinalVerdictRequiresCitedIndependentSources(t *testing.T) {
	c := syntheticCandidateAssessmentContext(t)
	refs := []candidateVerificationSourceRef{
		{CallRef: "original-call", AttemptRef: "original-attempt", ObservationDigest: strings.Repeat("a", 64)},
		{CallRef: "followup-call", AttemptRef: "followup-attempt", ObservationDigest: strings.Repeat("b", 64)},
	}
	base := CandidateFinalVerdict{Version: "selected/final-v1", CandidateID: 1, Verdict: "confirmed", Claim: "Observed defect",
		NormalExplanation: "Normal case considered", Reason: "Independent observations agree", Limitations: "Only one read path",
		Remediation: "Remove disclosure", VerificationCriteria: "Repeat the read", Sources: refs}
	check := func(v CandidateFinalVerdict, want bool) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseCandidateFinalVerdict(raw, c)
		if (err == nil) != want {
			t.Fatalf("verdict=%s refs=%v accepted=%t want=%t", v.Verdict, v.Sources, err == nil, want)
		}
	}
	check(base, true)
	for _, verdict := range []string{"negative", "insufficient"} {
		v := base
		v.Verdict = verdict
		check(v, true)
	}
	for _, refs := range [][]candidateVerificationSourceRef{{}, {refs[0]}, {refs[1]}, {refs[0], refs[0]},
		{refs[0], {CallRef: "foreign", AttemptRef: "foreign-attempt", ObservationDigest: strings.Repeat("c", 64)}}} {
		v := base
		v.Sources = refs
		check(v, false)
	}
	c.Original.OriginalObservation = nil
	raw, _ := json.Marshal(base)
	if _, err := ParseCandidateFinalVerdict(raw, c); err == nil {
		t.Fatal("missing original observation confirmed")
	}
	base.Verdict = "insufficient"
	base.Sources = refs[1:]
	raw, _ = json.Marshal(base)
	if _, err := ParseCandidateFinalVerdict(raw, c); err != nil {
		t.Fatal("missing original observation could not be left inconclusive", err)
	}
}

func TestCandidateFinalVerdictAuthenticationContextMismatch(t *testing.T) {
	c := syntheticCandidateAssessmentContext(t)
	c.Original.OriginalObservation.Source.Observation.RequestAuthContext = "authorization"
	verdict := CandidateFinalVerdict{Version: "selected/final-v1", CandidateID: 1, Verdict: "confirmed",
		Claim: "Synthetic claim", NormalExplanation: "Public resource may be normal", Reason: "Model recommendation",
		Limitations: "Only two requests", Remediation: "Review exposure", VerificationCriteria: "Repeat under matching authorization",
		Sources: []candidateVerificationSourceRef{
			{"original-call", "original-attempt", strings.Repeat("a", 64)},
			{"followup-call", "followup-attempt", strings.Repeat("b", 64)},
		}}
	raw, _ := json.Marshal(verdict)
	got, err := ParseCandidateFinalVerdict(raw, c)
	if err != nil || got.Verdict != "insufficient" || got.Reason != "context_mismatch" {
		t.Fatalf("authenticated original and anonymous followup became decisive: %+v %v", got, err)
	}
	c.Followup[0].Source.Observation.RequestAuthContext = "authorization"
	got, err = ParseCandidateFinalVerdict(raw, c)
	if err != nil || got.Verdict != "insufficient" || got.Reason != "account_identity_unverified" {
		t.Fatalf("matching header category alone established account identity: %+v %v", got, err)
	}
	c.Original.OriginalObservation.Source.Observation.RequestAccountRef = strings.Repeat("1", 64)
	c.Followup[0].Source.Observation.RequestAccountRef = strings.Repeat("2", 64)
	got, err = ParseCandidateFinalVerdict(raw, c)
	if err != nil || got.Verdict != "insufficient" || got.Reason != "context_mismatch" {
		t.Fatalf("different account revisions were compared: %+v %v", got, err)
	}
	c.Followup[0].Source.Observation.RequestAccountRef = strings.Repeat("1", 64)
	c.Original.OriginalObservation.Source.Observation.StatusCode = 200
	c.Followup[0].Source.Observation.StatusCode = 200
	got, err = ParseCandidateFinalVerdict(raw, c)
	if err != nil || got.Verdict != "confirmed" {
		t.Fatalf("same verified account revision was rejected: %+v %v", got, err)
	}
	c.Followup[0].Source.Observation.StatusCode = 401
	got, err = ParseCandidateFinalVerdict(raw, c)
	if err != nil || got.Verdict != "insufficient" {
		t.Fatalf("expired session became decisive: %+v %v", got, err)
	}
	c.Followup[0].Source.Observation.StatusCode = 200
	c.Original.OriginalObservation.Source.Observation.RequestAuthContext = "unknown"
	c.Followup[0].Source.Observation.RequestAuthContext = "unknown"
	got, err = ParseCandidateFinalVerdict(raw, c)
	if err != nil || got.Verdict != "insufficient" {
		t.Fatalf("unclassified credential header became decisive: %+v %v", got, err)
	}
}

func TestCandidateFinalModelPromptOmitsHostCheckpoint(t *testing.T) {
	c := syntheticCandidateAssessmentContext(t)
	c.Followup[0].SelectedProof = &e2runner.SelectedAuditProof{Checkpoint: e2proxy.JournalCheckpoint{MAC: "synthetic-host-secret-mac"}}
	request, err := BuildCandidateFinalRequest(c, 512)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(request)
	if err != nil || strings.Contains(string(raw), "synthetic-host-secret-mac") || strings.Contains(string(raw), "selected_proof") {
		t.Fatal("Host checkpoint leaked into model prompt", err)
	}
}

func TestCandidateFinalEvidenceTruncatesAtUTF8Boundary(t *testing.T) {
	for _, suffix := range []string{"가나다 Korean evidence", "🔒 한국어 English evidence"} {
		t.Run(suffix, func(t *testing.T) {
			c := syntheticCandidateAssessmentContext(t)
			c.Original.Candidate.Evidence = strings.Repeat("A", candidateFinalEvidenceBytes-1) + suffix
			request, err := BuildCandidateFinalRequest(c, 512)
			if err != nil {
				t.Fatal(err)
			}
			var input struct {
				Evidence string `json:"original_evidence"`
			}
			if len(request.Messages) != 1 || len(request.Messages[0].Content) != 1 ||
				json.Unmarshal([]byte(request.Messages[0].Content[0].Text), &input) != nil {
				t.Fatal("model prompt malformed")
			}
			if !strings.HasPrefix(c.Original.Candidate.Evidence, input.Evidence) || len(input.Evidence) > candidateFinalEvidenceBytes ||
				strings.ContainsRune(input.Evidence, '\uFFFD') || !utf8.ValidString(input.Evidence) {
				t.Fatalf("evidence was truncated inside a UTF-8 rune: %q", input.Evidence[len(input.Evidence)-8:])
			}
		})
	}
}

func TestCandidateFinalVerdictRejectsUnrelatedFollowupPath(t *testing.T) {
	c := syntheticCandidateAssessmentContext(t)
	c.Followup[0].Source.Observation.Endpoint = "/unrelated"
	verdict := CandidateFinalVerdict{Version: "selected/final-v1", CandidateID: c.Original.Candidate.ID, Verdict: "confirmed",
		Claim: "Observed defect", NormalExplanation: "Normal case considered", Reason: "Model wants confirmation",
		Limitations: "Only one read path", Remediation: "Remove disclosure", VerificationCriteria: "Repeat the read",
		Sources: []candidateVerificationSourceRef{
			{CallRef: "original-call", AttemptRef: "original-attempt", ObservationDigest: strings.Repeat("a", 64)},
			{CallRef: "followup-call", AttemptRef: "followup-attempt", ObservationDigest: strings.Repeat("b", 64)},
		}}
	raw, _ := json.Marshal(verdict)
	if got, err := ParseCandidateFinalVerdict(raw, c); err != nil || got.Verdict != "insufficient" || got.Reason != "input_context_mismatch" {
		t.Fatalf("unrelated function became decisive: %+v %v", got, err)
	}
	verdict.Verdict = "insufficient"
	raw, _ = json.Marshal(verdict)
	if _, err := ParseCandidateFinalVerdict(raw, c); err != nil {
		t.Fatal("unrelated function could not remain inconclusive", err)
	}
}

func TestCandidateFinalVerdictRequiresComparableReproductionWithNormalControl(t *testing.T) {
	c := syntheticCandidateAssessmentContext(t)
	c.Followup[0].Source.Observation.RuleRef = "candidate-llm-1"
	control := c.Followup[0]
	control.Source.CallRef = "control-call"
	control.Source.Digest = strings.Repeat("c", 64)
	control.Source.Observation.AttemptRef = "control-attempt"
	control.Source.Observation.RuleRef = "candidate-llm-2"
	control.Source.Observation.RequestQueryDigest = strings.Repeat("d", 64)
	c.Followup = append(c.Followup, control)
	c.Plan.Actions = []CandidateLLMPlanAction{
		{Purpose: "repeat original", Method: "GET", ExactPath: "/documents"},
		{Purpose: "normal contrast", Method: "GET", ExactPath: "/documents", Kind: "normal_control", Query: map[string]string{"mode": "review"}},
	}
	verdict := CandidateFinalVerdict{Version: "selected/final-v1", CandidateID: 1, Verdict: "confirmed",
		Claim: "Observed defect", NormalExplanation: "A normal response may differ", Reason: "Independent reproduction and control",
		Limitations: "Only one feature", Remediation: "Remove the defect", VerificationCriteria: "Repeat both conditions",
		Sources: []candidateVerificationSourceRef{
			{"original-call", "original-attempt", strings.Repeat("a", 64)},
			{"followup-call", "followup-attempt", strings.Repeat("b", 64)},
			{"control-call", "control-attempt", strings.Repeat("c", 64)},
		}}
	check := func(want string) {
		t.Helper()
		raw, _ := json.Marshal(verdict)
		got, err := ParseCandidateFinalVerdict(raw, c)
		if err != nil || got.Verdict != want {
			t.Fatalf("with a normal control: %+v %v, want %s", got, err, want)
		}
	}
	check("confirmed")
	c.Followup[0].Source.Observation.RequestQueryDigest = strings.Repeat("e", 64)
	check("insufficient")
	c.Followup[0].Source.Observation.RequestQueryDigest = ""
	c.Plan.Actions[0].Kind = "normal_control"
	check("insufficient")
	c.Plan.Actions[0].Kind = ""
	c.Followup[1].Source.Observation.RequestAuthContext = "authorization"
	check("insufficient")
}
