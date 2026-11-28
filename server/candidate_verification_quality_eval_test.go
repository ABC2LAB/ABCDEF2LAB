package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/responseevidence"
)

// 선택 후보 판정 품질 평가 하네스다. 정답·유형·규칙 표시는 모델 입력에 넣지 않는다.
//
// 기본 실행은 모의(과신) 모델로 제품 게이트의 기능만 측정한다. 실제 모델 품질은 사용자가 승인한 무료 로컬 모델을 loopback에 띄우고
// ARTEX_SELECTED_EVAL_MODEL_URL(…/chat/completions)·ARTEX_SELECTED_EVAL_MODEL을 줄 때만 잰다. 결과 JSON은
// ARTEX_SELECTED_EVAL_OUT에 쓴다. 외부 호스트·프록시·리다이렉트는 쓰지 않는다.

type evalResponse struct {
	Status      int               `json:"status"`
	ContentType string            `json:"content_type"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
}

type evalObservation struct {
	Kind     string       `json:"kind"`
	Endpoint string       `json:"endpoint"`
	Query    *string      `json:"query"`
	Auth     *string      `json:"auth"`
	Account  *string      `json:"account"`
	Response evalResponse `json:"response"`
}

type evalCase struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Expected string `json:"expected"`
	Rule     *struct {
		MissingHeader string `json:"missing_header"`
	} `json:"rule"`
	Candidate struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Summary   string `json:"summary"`
	} `json:"candidate"`
	ToolOutput     string            `json:"tool_output"`
	Answers        []string          `json:"answers"`
	Original       evalObservation   `json:"original"`
	Followups      []evalObservation `json:"followups"`
	OriginalLegacy bool              `json:"original_legacy"`
	MaxBodyBytes   int               `json:"max_body_bytes"`
	Tamper         string            `json:"tamper"`
	Method         string            `json:"method"`
}

func loadEvalCases(t *testing.T) []evalCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "scripts", "eval", "fixtures", "selected_candidate_structured_gold.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version string     `json:"version"`
		FixedAt string     `json:"fixed_at"`
		Note    string     `json:"note"`
		Cases   []evalCase `json:"cases"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil || file.Version != "jindo.selected-structured-gold/v1" || len(file.Cases) < 13 {
		t.Fatalf("invalid gold set: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range file.Cases {
		if seen[c.ID] || c.Expected != "model_supported" && c.Expected != "model_refuted" && c.Expected != "insufficient" || len(c.Followups) == 0 {
			t.Fatalf("invalid case %s", c.ID)
		}
		seen[c.ID] = true
	}
	return file.Cases
}

func digestHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func inherit(v *string, parent string) string {
	if v == nil {
		return parent
	}
	return *v
}

// evalRows는 사례의 합성 응답을 실제 추출기로 근거화한 저장 관찰 행을 만든다. structured가 거짓이면 #950 행(근거 없음)이다.
func evalRows(c evalCase, structured bool) (candidateverify.StoredObservationV2, []candidateverify.StoredObservationV2) {
	method := c.Method
	if method == "" {
		method = "GET"
	}
	origQuery, origAuth, origAccount := inherit(c.Original.Query, ""), inherit(c.Original.Auth, "none"), inherit(c.Original.Account, "")
	build := func(o evalObservation, i int, query, auth, account string) candidateverify.StoredObservationV2 {
		endpoint := o.Endpoint
		if endpoint == "" {
			endpoint = c.Original.Endpoint
		}
		header := http.Header{}
		for k, v := range o.Response.Headers {
			header.Set(k, v)
		}
		if header.Get("Content-Type") == "" && o.Response.ContentType != "" {
			header.Set("Content-Type", o.Response.ContentType)
		}
		obs := e2proxy.HTTPObservation{Sequence: uint64(1 + 10*i), AttemptRef: fmt.Sprintf("%s-attempt-%d", strings.ToLower(c.ID), i),
			Method: method, Endpoint: endpoint, ExactFunction: true, RuleRef: "rule", Decision: "allowed", Reason: e2proxy.Complete,
			StatusCode: o.Response.Status, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true,
			ObservedAt: time.Date(2026, 11, 27, 1, 0, i, 0, time.UTC), RequestAuthContext: auth,
			ResponseMediaClass: responseevidence.MediaClass(o.Response.ContentType)}
		if i > 0 {
			obs.RuleRef = fmt.Sprintf("candidate-llm-%d", i)
		}
		if account != "" {
			obs.RequestAccountRef = digestHex("account:" + account)
		}
		if query != "" {
			obs.RequestQueryDigest = digestHex(query)
		}
		mode := e2proxy.HeaderPreserve
		if auth != "none" {
			mode = e2proxy.HeaderTestAccountPassthrough
		}
		raw, _ := json.Marshal(obs)
		row := candidateverify.StoredObservationV2{Revision: 2, HeaderMode: mode, Source: candidateverify.SourceV2{
			CallRef: fmt.Sprintf("%s-call-%d", strings.ToLower(c.ID), i), IntentID: 7, TaskRef: "42", RunRef: "eval-run",
			ScopeDigest: digestHex("scope"), PolicyRevision: "policy-eval", Observation: obs, Digest: db.DiagnosticEvidenceDigest(string(raw))}}
		if structured && !(i == 0 && c.OriginalLegacy) {
			ev := responseevidence.Extract(responseevidence.Response{Received: true, Complete: true, StatusCode: o.Response.Status, Method: method,
				ContentType: o.Response.ContentType, Header: header, Body: []byte(o.Response.Body)},
				responseevidence.Limits{MaxBodyBytes: c.MaxBodyBytes}, nil)
			if fitted, ok := responseevidence.Fit(ev, e2proxy.MaxResponseEvidenceBytes); ok {
				row.ResponseEvidence = candidateverify.BindResponseEvidence(row.Source, fitted)
			}
		}
		return row
	}
	original := build(c.Original, 0, origQuery, origAuth, origAccount)
	var followups []candidateverify.StoredObservationV2
	for i, f := range c.Followups {
		followups = append(followups, build(f, i+1, inherit(f.Query, origQuery), inherit(f.Auth, origAuth), inherit(f.Account, origAccount)))
	}
	return original, followups
}

func evalContext(t *testing.T, c evalCase, structured bool) CandidateAssessmentContext {
	t.Helper()
	original, followups := evalRows(c, structured)
	evidence, _ := json.Marshal(map[string]any{"tool_output": c.ToolOutput, "trusted": map[string]any{"classification": "completed",
		"execution": map[string]any{"scope": map[string]string{"TaskRef": "42", "RunRef": "eval-run"}, "intent_id": 7},
		"call":      map[string]any{"call_ref": original.Source.CallRef, "intent_id": 7}}})
	candidate := db.DiagnosticCandidate{ID: 1, TaskID: 42, IntentID: 7, RunRef: "eval-run", CallRef: original.Source.CallRef,
		VulnClass: c.Candidate.VulnClass, Name: c.Candidate.Name, Summary: c.Candidate.Summary, Classification: "completed",
		Verification: "manual_review_required", EvidenceVersion: 1, Evidence: string(evidence),
		EvidenceDigest: db.DiagnosticEvidenceDigest(string(evidence)), ObservedAt: time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)}
	if candidate.Validate() != nil {
		t.Fatalf("%s: synthetic candidate malformed", c.ID)
	}
	plan := CandidateLLMPlan{CandidateID: candidate.ID, EvidenceDigest: candidate.EvidenceDigest}
	for _, f := range c.Followups {
		endpoint := f.Endpoint
		if endpoint == "" {
			endpoint = c.Original.Endpoint
		}
		plan.Actions = append(plan.Actions, CandidateLLMPlanAction{Purpose: "reproduce the original read", Method: "GET", ExactPath: endpoint, Kind: f.Kind})
	}
	var answers []candidateBusinessAnswer
	for i, a := range c.Answers {
		answers = append(answers, candidateBusinessAnswer{QuestionIndex: i + 1, Question: "What business rule applies to this response?", Answer: a,
			AnsweredAt: time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC)})
	}
	return CandidateAssessmentContext{Original: CandidateSelectedContext{RequestID: 5, TaskID: 42, Candidate: candidate, RunRef: "eval-run",
		OriginalObservation: &original}, Plan: plan, Answers: answers,
		Analysis: candidateAnalysisView{Proposal: CandidateAnalysisProposal{Version: "selected/analysis-v1", CandidateID: 1,
			Claim: c.Candidate.Summary, NormalExplanation: "Not yet assessed.", Limitations: "Synthetic evaluation case."}},
		Followup: followups}
}

func evalLabel(verdict string) string {
	switch verdict {
	case "confirmed":
		return "model_supported"
	case "negative":
		return "model_refuted"
	}
	return "insufficient"
}

// evalModelOutput는 모델 출력에서 권고 판정만 느슨하게 읽는다(권고 코호트). 제품 판정은 ParseCandidateFinalVerdict만 쓴다.
func evalModelOutput(text string) (CandidateFinalVerdict, bool) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(text, "```json"), "```"), "```")
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var out CandidateFinalVerdict
	return out, json.Unmarshal([]byte(text), &out) == nil
}

type evalUsage struct {
	Calls, InputTokens, OutputTokens int
	Elapsed                          time.Duration
	Invalid                          int
}

type evalModel func(t *testing.T, c evalCase, ctx CandidateAssessmentContext, usage *evalUsage) string

// mockModel은 과신 모델이다. 모든 출처를 정확히 인용하고 같은 결정적 판정만 낸다. 게이트가 어떤 오판을 막고 어떤 정당한 판정을
// 남기는지 재는 기능 시험이며 모델 품질 측정이 아니다.
func mockModel(verdict string) evalModel {
	return func(t *testing.T, c evalCase, ctx CandidateAssessmentContext, usage *evalUsage) string {
		sources := []candidateVerificationSourceRef{sourceRefOf(*ctx.Original.OriginalObservation)}
		for _, row := range ctx.Followup {
			sources = append(sources, sourceRefOf(row))
		}
		raw, _ := json.Marshal(CandidateFinalVerdict{Version: "selected/final-v1", CandidateID: 1, Verdict: verdict, Claim: "mock",
			NormalExplanation: "mock", Reason: "mock", Limitations: "mock", Remediation: "mock", VerificationCriteria: "mock", Sources: sources})
		return string(raw)
	}
}

func localModel(endpoint, model string) evalModel {
	client := &http.Client{Timeout: 180 * time.Second, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(t *testing.T, c evalCase, ctx CandidateAssessmentContext, usage *evalUsage) string {
		request, err := BuildCandidateFinalRequest(ctx, candidateFinalMaxTokens)
		if err != nil {
			t.Fatalf("%s: request: %v", c.ID, err)
		}
		body, _ := json.Marshal(map[string]any{"model": model, "temperature": 0, "max_tokens": candidateFinalMaxTokens, "messages": []map[string]string{
			{"role": "system", "content": strings.Join(request.System, "\n")}, {"role": "user", "content": request.Messages[0].Content[0].Text}}})
		started := time.Now()
		resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
		usage.Calls++
		if err != nil {
			t.Fatalf("%s: local model unavailable: %v", c.ID, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		usage.Elapsed += time.Since(started)
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
		usage.InputTokens += payload.Usage.Prompt
		usage.OutputTokens += payload.Usage.Completion
		return payload.Choices[0].Message.Content
	}
}

type evalCounts struct {
	Cases                       int     `json:"cases"`
	CorrectPositive             int     `json:"correct_positive"`
	CorrectNegative             int     `json:"correct_negative"`
	WrongPositive               int     `json:"wrong_positive"`
	WrongNegative               int     `json:"wrong_negative"`
	WrongDecisiveOnInsufficient int     `json:"wrong_decisive_on_insufficient"`
	Undecided                   int     `json:"undecided"`
	UndecidedRate               float64 `json:"undecided_rate"`
	DecidedAccuracy             float64 `json:"decided_accuracy"`
	Recall                      float64 `json:"supported_recall"`
	Precision                   float64 `json:"supported_precision"`
	CorrectInsufficient         int     `json:"correct_insufficient"`
	DecisiveKeptOnDecidable     int     `json:"decisive_kept_on_decidable"`
}

func tally(cases []evalCase, verdicts map[string]string) evalCounts {
	var out evalCounts
	positives, decidable, positiveCalls, decided, correctDecided := 0, 0, 0, 0, 0
	for _, c := range cases {
		v := verdicts[c.ID]
		out.Cases++
		if c.Expected == "model_supported" {
			positives++
		}
		if c.Expected != "insufficient" {
			decidable++
		}
		switch {
		case v == "insufficient":
			out.Undecided++
			if c.Expected == "insufficient" {
				out.CorrectInsufficient++
			}
			continue
		case v == c.Expected:
			if v == "model_supported" {
				out.CorrectPositive++
			} else {
				out.CorrectNegative++
			}
			correctDecided++
			out.DecisiveKeptOnDecidable++
		default:
			if v == "model_supported" {
				out.WrongPositive++
			} else {
				out.WrongNegative++
			}
			if c.Expected == "insufficient" {
				out.WrongDecisiveOnInsufficient++
			}
		}
		decided++
		if v == "model_supported" {
			positiveCalls++
		}
	}
	ratio := func(a, b int) float64 {
		if b == 0 {
			return 0
		}
		return float64(int(float64(a)/float64(b)*1000+0.5)) / 1000
	}
	out.UndecidedRate = ratio(out.Undecided, out.Cases)
	out.DecidedAccuracy = ratio(correctDecided, decided)
	out.Recall = ratio(out.CorrectPositive, positives)
	out.Precision = ratio(out.CorrectPositive, positiveCalls)
	_ = decidable
	return out
}

func ruleOnly(c evalCase, ctx CandidateAssessmentContext) string {
	if c.Rule == nil || c.Rule.MissingHeader == "" || ctx.Original.OriginalObservation == nil {
		return "insufficient"
	}
	has := func(row candidateverify.StoredObservationV2) (bool, bool) {
		ev := candidateverify.RowResponseEvidence(row)
		if ev.Headers == nil {
			return false, false
		}
		for _, h := range ev.Headers.Present {
			if h == c.Rule.MissingHeader {
				return true, true
			}
		}
		return false, true
	}
	a, okA := has(*ctx.Original.OriginalObservation)
	var reproduction *candidateverify.StoredObservationV2
	for i := range ctx.Followup {
		if followupRole(ctx, ctx.Followup[i]) == responseevidence.RoleReproduction {
			reproduction = &ctx.Followup[i]
			break
		}
	}
	if !okA || reproduction == nil {
		return "insufficient"
	}
	b, okB := has(*reproduction)
	switch {
	case !okB || a != b:
		return "insufficient"
	case !a:
		return "model_supported"
	}
	return "model_refuted"
}

type evalReport struct {
	Mode    string                       `json:"mode"`
	Model   string                       `json:"model,omitempty"`
	Totals  map[string]evalCounts        `json:"totals"`
	Rows    map[string]map[string]string `json:"rows"`
	Usage   map[string]evalUsage         `json:"usage"`
	Note    string                       `json:"note"`
	Targets int                          `json:"target_requests"`
	CostUSD float64                      `json:"cost_usd"`
}

func runEval(t *testing.T, cases []evalCase, model evalModel) evalReport {
	report := evalReport{Totals: map[string]evalCounts{}, Rows: map[string]map[string]string{}, Usage: map[string]evalUsage{}}
	cohorts := map[string]map[string]string{"legacy_model_advice": {}, "legacy_product": {}, "rule_only": {}, "structured_model_advice": {}, "structured_product": {}}
	for _, c := range cases {
		for _, structured := range []bool{false, true} {
			prefix, key := "legacy", "legacy"
			if structured {
				prefix, key = "structured", "structured"
			}
			ctx := evalContext(t, c, structured)
			request, err := BuildCandidateFinalRequest(ctx, candidateFinalMaxTokens)
			if err != nil {
				t.Fatalf("%s: model input: %v", c.ID, err)
			}
			text := request.Messages[0].Content[0].Text
			for _, leaked := range []string{c.Expected, c.Category, `"expected"`} {
				if strings.Contains(text, leaked) && leaked != "insufficient" {
					t.Fatalf("%s: gold metadata reached the model input", c.ID)
				}
			}
			usage := report.Usage[key]
			output := model(t, c, ctx, &usage)
			advice, ok := evalModelOutput(output)
			if !ok {
				usage.Invalid++
			}
			report.Usage[key] = usage
			cohorts[prefix+"_model_advice"][c.ID] = evalLabel(advice.Verdict)
			if c.Tamper == "forged_source" && ok && len(advice.Sources) > 0 {
				advice.Sources[len(advice.Sources)-1].CallRef = "forged-call"
				raw, _ := json.Marshal(advice)
				output = string(raw)
			}
			product := "insufficient"
			if ok {
				raw, _ := json.Marshal(advice)
				if parsed, err := ParseCandidateFinalVerdict(raw, ctx); err == nil {
					product = evalLabel(parsed.Verdict)
				}
			}
			_ = output
			cohorts[prefix+"_product"][c.ID] = product
			if structured {
				cohorts["rule_only"][c.ID] = ruleOnly(c, ctx)
			}
		}
	}
	names := make([]string, 0, len(cohorts))
	for name := range cohorts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		report.Totals[name] = tally(cases, cohorts[name])
		report.Rows[name] = cohorts[name]
	}
	return report
}

func writeEvalReport(t *testing.T, report evalReport) {
	raw, _ := json.MarshalIndent(report, "", "  ")
	if path := os.Getenv("ARTEX_SELECTED_EVAL_OUT"); path != "" {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSelectedCandidateStructuredEvalMockGate는 과신 모의 모델로 게이트만 잰다(합성·무료, 모델 품질 아님).
func TestSelectedCandidateStructuredEvalMockGate(t *testing.T) {
	cases := loadEvalCases(t)
	confirm := runEval(t, cases, mockModel("confirmed"))
	negative := runEval(t, cases, mockModel("negative"))
	for _, r := range []*evalReport{&confirm, &negative} {
		r.Mode, r.Note = "mock_overconfident", "Functional gate measurement with an overconfident mock model. Not LLM quality."
	}
	raw, _ := json.MarshalIndent(map[string]evalReport{"always_confirmed": confirm, "always_negative": negative}, "", "  ")
	if path := os.Getenv("ARTEX_SELECTED_EVAL_OUT"); path != "" {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 프로그램이 확인할 수 있는 근거 부족(옛 행·로그인 화면·구조 없음·입력/계정 불일치·위조 출처·HEAD·재현 실패)은 제품 판정이 막아야 한다.
	programInsufficient := []string{"R06", "R09", "R10", "R11", "R12", "R15", "R18", "R19"}
	for _, id := range programInsufficient {
		if got := confirm.Rows["structured_product"][id]; got != "insufficient" {
			t.Errorf("%s: structured product allowed an unsupported confirmation: %s", id, got)
		}
		if got := negative.Rows["structured_product"][id]; got != "insufficient" {
			t.Errorf("%s: structured product allowed an unsupported refutation: %s", id, got)
		}
	}
	// 근거가 충분한 사례의 판정 가능 범위는 유지돼야 한다.
	for _, id := range []string{"R01", "R02", "R03", "R13", "R17"} {
		if got := confirm.Rows["structured_product"][id]; got != "model_supported" {
			t.Errorf("%s: gate removed a supported decision: %s", id, got)
		}
	}
	for _, id := range []string{"R04", "R05", "R07", "R08", "R20"} {
		if got := negative.Rows["structured_product"][id]; got != "model_refuted" {
			t.Errorf("%s: gate removed a refutation: %s", id, got)
		}
	}
	if confirm.Totals["structured_product"].WrongDecisiveOnInsufficient >= confirm.Totals["legacy_product"].WrongDecisiveOnInsufficient {
		t.Errorf("structured gate did not reduce wrong decisive verdicts on insufficient cases")
	}
}

// TestSelectedCandidateStructuredEvalLocalModel은 승인된 로컬 모델이 있을 때만 실제 권고 품질을 잰다.
func TestSelectedCandidateStructuredEvalLocalModel(t *testing.T) {
	endpoint, model := os.Getenv("ARTEX_SELECTED_EVAL_MODEL_URL"), os.Getenv("ARTEX_SELECTED_EVAL_MODEL")
	if endpoint == "" || model == "" {
		t.Skip("local model not configured; real model quality not measured")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" || u.User != nil || u.RawQuery != "" ||
		!strings.HasSuffix(u.Path, "/chat/completions") {
		t.Fatal("only an explicit loopback chat/completions endpoint is allowed")
	}
	report := runEval(t, loadEvalCases(t), localModel(endpoint, model))
	report.Mode, report.Model = "local_model", model
	report.Note = "Local loopback model; no target requests. Same model, temperature 0 for legacy and structured inputs."
	writeEvalReport(t, report)
}
