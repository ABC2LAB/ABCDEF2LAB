package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/transmit"
)

// 최초 분석 단계 평가다. 관찰 선택(프로그램)은 오프라인으로 결정적으로 재고, 분석 모델의 계획 품질은 승인된 무료 로컬 모델이
// loopback에 있을 때만 잰다(ARTEX_SELECTED_EVAL_MODEL_URL·ARTEX_SELECTED_EVAL_MODEL). 결과는 ARTEX_SELECTED_ANALYSIS_EVAL_OUT.

// legacyAnalysisSelection은 #950/#999의 관찰 선택(원본 + 저장 순서 앞에서부터 다른 호출 5개)이다. 비교용으로만 둔다.
func legacyAnalysisSelection(c CandidateSelectedContext) []candidateverify.StoredObservationV2 {
	selected := []candidateverify.StoredObservationV2{}
	if c.OriginalObservation != nil {
		selected = append(selected, *c.OriginalObservation)
	}
	for _, row := range c.Observations {
		if row.Source.CallRef == c.Candidate.CallRef {
			continue
		}
		if len(selected) >= 6 {
			break
		}
		selected = append(selected, row)
	}
	return selected
}

type analysisSelectionCounts struct {
	Contexts                int `json:"contexts"`
	IndependentAvailable    int `json:"independent_available_capped"`
	IndependentIncluded     int `json:"independent_included"`
	DuplicateAttemptsListed int `json:"duplicate_attempts_listed"`
	ContextsMissingAll      int `json:"contexts_with_independent_but_none_listed"`
}

func TestSelectedCandidateAnalysisSelectionEval(t *testing.T) {
	rng := rand.New(rand.NewSource(20261127))
	body := `{"data":[{"id":1}]}`
	counts := map[string]*analysisSelectionCounts{"legacy_selection": {}, "relevance_selection": {}}
	for n := 0; n < 200; n++ {
		specs := []analysisRowSpec{{call: "orig", attempt: "a-orig", sequence: 5, body: body}}
		independent, others := rng.Intn(4), rng.Intn(12)
		seq := uint64(11)
		var pool []analysisRowSpec
		for i := 0; i < independent; i++ {
			pool = append(pool, analysisRowSpec{call: fmt.Sprintf("repro-%d", i), attempt: fmt.Sprintf("a-repro-%d", i), body: body})
			if rng.Intn(3) == 0 {
				// 같은 시도의 다른 행(재시도 projection 등)은 독립 근거가 아니다.
				pool = append(pool, analysisRowSpec{call: fmt.Sprintf("repro-%d-dup", i), attempt: fmt.Sprintf("a-repro-%d", i), body: body})
			}
		}
		for i := 0; i < others; i++ {
			pool = append(pool, analysisRowSpec{call: fmt.Sprintf("other-%d", i), attempt: fmt.Sprintf("a-other-%d", i), endpoint: fmt.Sprintf("/rest/o/%d", i), body: body})
		}
		rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		for i := range pool {
			pool[i].sequence = seq
			seq++
		}
		c := analysisContext(t, "information_disclosure", append(specs, pool...)...)
		for name, rows := range map[string][]candidateverify.StoredObservationV2{"legacy_selection": legacyAnalysisSelection(c),
			"relevance_selection": candidateAnalysisObservations(c, c.OriginalObservation)} {
			k := counts[name]
			k.Contexts++
			k.IndependentAvailable += min(independent, 5)
			// 같은 시도의 두 행은 독립 관찰 하나로 센다.
			attempts, groups := map[string]bool{}, map[string]bool{}
			for _, row := range rows {
				if attempts[row.Source.Observation.AttemptRef] {
					k.DuplicateAttemptsListed++
				}
				attempts[row.Source.Observation.AttemptRef] = true
				if row.Source.CallRef != "orig" && storedComparisonEligible(c, *c.OriginalObservation, row) {
					groups[strings.TrimSuffix(row.Source.CallRef, "-dup")] = true
				}
			}
			included := len(groups)
			k.IndependentIncluded += included
			if independent > 0 && included == 0 {
				k.ContextsMissingAll++
			}
		}
	}
	if path := os.Getenv("ARTEX_SELECTED_ANALYSIS_EVAL_OUT"); path != "" {
		raw, _ := json.MarshalIndent(map[string]any{"mode": "offline_selection", "note": "Deterministic synthetic contexts (seed 20261127). Program selection only, not model quality.",
			"counts": counts}, "", "  ")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, old := counts["relevance_selection"], counts["legacy_selection"]
	t.Logf("legacy %+v / relevance %+v", *old, *got)
	if got.IndependentIncluded != got.IndependentAvailable || got.DuplicateAttemptsListed != 0 || got.ContextsMissingAll != 0 {
		t.Fatalf("relevance selection lost independent observations: %+v", *got)
	}
	if old.IndependentIncluded >= got.IndependentIncluded {
		t.Fatalf("relevance selection did not improve over the legacy selection: %+v vs %+v", *old, *got)
	}
}

// analysisEvalContext는 구조 정답 사례의 원본(과 저장 독립 관찰로 둔 첫 재현)으로 분석 문맥을 만든다.
func analysisEvalContext(t *testing.T, c evalCase, storedReproduction bool) CandidateSelectedContext {
	t.Helper()
	original, followups := evalRows(c, true)
	scope := transmit.Scope{TaskRef: "42", RunRef: "eval-run"}
	rawScope, _ := json.Marshal(scope)
	rows := []candidateverify.StoredObservationV2{original}
	if storedReproduction && len(followups) > 0 && followups[0].Source.Observation.RequestQueryDigest == original.Source.Observation.RequestQueryDigest {
		rows = append(rows, followups[0])
	}
	for i := range rows {
		rows[i].Source.ScopeDigest = db.DiagnosticEvidenceDigest(string(rawScope))
		rows[i].Source.Observation.RuleRef = "rule"
		rows[i].Source.Observation.Sequence = uint64(5 + 10*i)
		raw, _ := json.Marshal(rows[i].Source.Observation)
		rows[i].Source.Digest = db.DiagnosticEvidenceDigest(string(raw))
		if rows[i].ResponseEvidence != nil {
			rows[i].ResponseEvidence = candidateverify.BindResponseEvidence(rows[i].Source, rows[i].ResponseEvidence.Evidence)
		}
	}
	trusted := e2runner.TrustedResult{Execution: e2proxy.ExecutionView{Scope: scope, IntentID: 7, PolicyRevision: rows[0].Source.PolicyRevision},
		Call: e2proxy.CallResult{CallRef: rows[0].Source.CallRef, IntentID: 7, Observations: []e2proxy.HTTPObservation{rows[0].Source.Observation}}}
	trusted.Execution.HeaderMode = rows[0].HeaderMode
	trusted.Classification = "completed"
	evidence, _ := json.Marshal(map[string]any{"tool_output": c.ToolOutput, "trusted": trusted})
	candidate := db.DiagnosticCandidate{ID: 1, TaskID: 42, IntentID: 7, RunRef: "eval-run", CallRef: rows[0].Source.CallRef,
		VulnClass: c.Candidate.VulnClass, Name: c.Candidate.Name, Summary: c.Candidate.Summary, Classification: "completed",
		Verification: "manual_review_required", EvidenceVersion: 1, Evidence: string(evidence),
		EvidenceDigest: db.DiagnosticEvidenceDigest(string(evidence)), ObservedAt: time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC),
		SaveFence: &db.DiagnosticCandidateSaveFence{AttemptHighWater: 10}}
	out := CandidateSelectedContext{RequestID: 5, TaskID: 42, Candidate: candidate, RunRef: "eval-run", Observations: rows}
	out.OriginalObservation = &out.Observations[0]
	if _, err := BuildCandidateAnalysisRequest(out, 2048); err != nil {
		t.Fatalf("%s: analysis context: %v", c.ID, err)
	}
	return out
}

type analysisPlanCounts struct {
	Cases                 int `json:"cases"`
	Valid                 int `json:"valid_proposals"`
	Actions               int `json:"actions"`
	ActionsWhenStored     int `json:"actions_when_stored_assessment_expected"`
	NormalControlRequired int `json:"normal_control_required_cases"`
	NormalControlProposed int `json:"normal_control_proposed_when_required"`
	RuleMissing           int `json:"business_rule_missing_cases"`
	QuestionWhenRule      int `json:"business_question_when_rule_missing"`
	RepeatedDropped       int `json:"repeated_actions_dropped"`
	Calls                 int `json:"model_calls"`
	InputTokens           int `json:"input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ElapsedMillis         int `json:"elapsed_ms"`
}

// TestSelectedCandidateAnalysisLocalModel은 승인된 로컬 모델로 분석 계획의 적절성을 잰다(없으면 SKIP, 품질 미측정).
func TestSelectedCandidateAnalysisLocalModel(t *testing.T) {
	endpoint, model := os.Getenv("ARTEX_SELECTED_EVAL_MODEL_URL"), os.Getenv("ARTEX_SELECTED_EVAL_MODEL")
	if endpoint == "" || model == "" {
		t.Skip("local model not configured; analysis plan quality not measured")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" || u.User != nil || u.RawQuery != "" ||
		!strings.HasSuffix(u.Path, "/chat/completions") {
		t.Fatal("only an explicit loopback chat/completions endpoint is allowed")
	}
	client := &http.Client{Timeout: 180 * time.Second, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var counts analysisPlanCounts
	for _, c := range loadEvalCases(t) {
		for _, stored := range []bool{false, true} {
			ctx := analysisEvalContext(t, c, stored)
			request, err := BuildCandidateAnalysisRequest(ctx, 2048)
			if err != nil {
				t.Fatal(err)
			}
			var input candidateAnalysisInput
			_ = json.Unmarshal([]byte(request.Messages[0].Content[0].Text), &input)
			body, _ := json.Marshal(map[string]any{"model": model, "temperature": 0, "max_tokens": 2048, "messages": []map[string]string{
				{"role": "system", "content": strings.Join(request.System, "\n")}, {"role": "user", "content": request.Messages[0].Content[0].Text}}})
			started := time.Now()
			resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
			counts.Calls++
			if err != nil {
				t.Fatalf("%s: local model unavailable: %v", c.ID, err)
			}
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			counts.ElapsedMillis += int(time.Since(started).Milliseconds())
			var payload struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
				Usage struct {
					Prompt     int `json:"prompt_tokens"`
					Completion int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &payload) != nil || len(payload.Choices) == 0 {
				t.Fatalf("%s: local model response invalid (%d)", c.ID, resp.StatusCode)
			}
			counts.InputTokens += payload.Usage.Prompt
			counts.OutputTokens += payload.Usage.Completion
			counts.Cases++
			text := strings.TrimSpace(payload.Choices[0].Message.Content)
			if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
				text = text[i : j+1]
			}
			var loose CandidateAnalysisProposal
			_ = json.Unmarshal([]byte(text), &loose)
			proposal, err := ParseCandidateAnalysisProposal([]byte(text), ctx)
			if err != nil {
				continue
			}
			counts.Valid++
			counts.RepeatedDropped += len(loose.Actions) - len(proposal.Actions)
			counts.Actions += len(proposal.Actions)
			if input.EvidenceSummary.StoredAssessmentPending {
				counts.ActionsWhenStored += len(proposal.Actions)
			}
			if input.EvidenceSummary.NormalControl == "required" {
				counts.NormalControlRequired++
				for _, a := range proposal.Actions {
					if a.Kind == "normal_control" {
						counts.NormalControlProposed++
						break
					}
				}
			}
			for _, missing := range input.EvidenceSummary.Missing {
				if missing == factBusinessRule {
					counts.RuleMissing++
					if len(proposal.BusinessQuestions) > 0 {
						counts.QuestionWhenRule++
					}
				}
			}
		}
	}
	if path := os.Getenv("ARTEX_SELECTED_ANALYSIS_EVAL_OUT"); path != "" {
		raw, _ := json.MarshalIndent(map[string]any{"mode": "local_model", "model": model, "counts": counts,
			"note": "Local loopback model; no target requests. Plan adequacy proxies only."}, "", "  ")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSelectedCandidateAnalysisInputsForGoldCases는 구조 정답 20건의 분석 입력을 실제 코드로 만들어 상한·누출·부족 사실을 본다.
func TestSelectedCandidateAnalysisInputsForGoldCases(t *testing.T) {
	largest := 0
	missing := map[string][]string{}
	for _, c := range loadEvalCases(t) {
		for _, stored := range []bool{false, true} {
			ctx := analysisEvalContext(t, c, stored)
			request, err := BuildCandidateAnalysisRequest(ctx, 2048)
			if err != nil {
				t.Fatalf("%s: %v", c.ID, err)
			}
			text := request.Messages[0].Content[0].Text
			largest = max(largest, len(text))
			for _, leaked := range []string{`"` + c.Expected + `"`, `"` + c.Category + `"`, `"expected"`} {
				if strings.Contains(text, leaked) && leaked != `"insufficient"` {
					t.Fatalf("%s: gold metadata reached the analysis input", c.ID)
				}
			}
			var input candidateAnalysisInput
			if json.Unmarshal([]byte(text), &input) != nil {
				t.Fatalf("%s: input", c.ID)
			}
			if stored {
				missing[c.ID] = input.EvidenceSummary.Missing
				last := ctx.Observations[len(ctx.Observations)-1]
				wantStored := last.Source.CallRef != ctx.Candidate.CallRef && storedComparisonEligible(ctx, *ctx.OriginalObservation, last)
				if input.EvidenceSummary.StoredAssessmentPending != wantStored {
					t.Fatalf("%s: stored assessment flag %v", c.ID, input.EvidenceSummary.StoredAssessmentPending)
				}
			}
		}
	}
	t.Logf("largest analysis input: %d bytes", largest)
	if strings.Join(missing["R14"], ",") != factSpecificError || strings.Join(missing["R16"], ",") != factBusinessRule ||
		len(missing["R01"]) != 0 || len(missing["R03"]) != 0 {
		t.Fatalf("missing facts: R14=%v R16=%v R01=%v R03=%v", missing["R14"], missing["R16"], missing["R01"], missing["R03"])
	}
}
