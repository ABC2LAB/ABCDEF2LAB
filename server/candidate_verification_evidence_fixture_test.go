package server

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSelectedCandidateEvidenceFixtureGateV2는 계약 fixture의 v2 예시가 실제 코드 결과와 같은지 본다. 코드가 바뀌면
// ARTEX_UPDATE_CONTRACT_FIXTURE=1로 다시 만든다(UI 담당자가 보는 모양을 코드와 맞춘다).
func TestSelectedCandidateEvidenceFixtureGateV2(t *testing.T) {
	path := filepath.Join("..", "docs", "contracts", "fixtures", "selected-candidate-response-evidence.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var example evalCase
	for _, c := range loadGateV2EvalCases(t) {
		if c.ID == "G05" {
			example = c
		}
	}
	ctx := evalContext(t, example, true, candidateEvidenceGateV2)
	blocked := ctx
	blocked.Answers = nil
	verdict := func(c CandidateAssessmentContext) CandidateFinalVerdict {
		sources := []candidateVerificationSourceRef{sourceRefOf(*c.Original.OriginalObservation), sourceRefOf(c.Followup[0])}
		raw, _ := json.Marshal(CandidateFinalVerdict{Version: "selected/final-v1", CandidateID: 1, Verdict: "confirmed", Claim: "Password hashes are returned to an ordinary user.",
			NormalExplanation: "None found.", Reason: "Both audited reads carry data[].password.", Limitations: "Read only.", Remediation: "Remove the field.",
			VerificationCriteria: "The field is absent.", Sources: sources})
		out, err := ParseCandidateFinalVerdict(raw, c)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	want := map[string]any{
		"note":    "Synthetic case G05. Gate v2 with an operator business statement (allowed) and the same observations without one (blocked).",
		"allowed": map[string]any{"final_verdict": verdict(ctx), "evidence_review": reviewCandidateEvidence(ctx)},
		"blocked": map[string]any{"final_verdict": verdict(blocked), "evidence_review": reviewCandidateEvidence(blocked)},
	}
	got, _ := json.MarshalIndent(want, "", "  ")
	if os.Getenv("ARTEX_UPDATE_CONTRACT_FIXTURE") == "1" {
		fixture["gate_v2_example"] = got
		out, _ := json.MarshalIndent(fixture, "", "  ")
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	var compact, stored bytes.Buffer
	_ = json.Compact(&compact, got)
	if err := json.Compact(&stored, fixture["gate_v2_example"]); err != nil || !bytes.Equal(compact.Bytes(), stored.Bytes()) {
		t.Fatal("contract fixture gate_v2_example is stale; regenerate with ARTEX_UPDATE_CONTRACT_FIXTURE=1")
	}
}
