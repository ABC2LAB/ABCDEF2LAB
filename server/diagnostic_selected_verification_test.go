package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/llmpolicy"
)

func TestSelectedVerificationKeepsCandidateWithoutAutomaticVerdictPostgres(t *testing.T) {
	s, finding, request := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(finding.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if err := saveDiagnosisSelectedVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	id := storePassiveCandidate(t, s, taskID, "html_nosniff_missing", "nosniff_missing", "")
	rows, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
	if err != nil || len(rows) != 1 {
		t.Fatalf("candidate read: count=%d err=%v", len(rows), err)
	}
	var envelope struct {
		Trusted e2runner.TrustedResult `json:"trusted"`
	}
	if err := json.Unmarshal([]byte(rows[0].Evidence), &envelope); err != nil {
		t.Fatal(err)
	}
	primary := envelope.Trusted
	primary.ProcessDrained, primary.ProxyDrained, primary.Execution.AuditComplete = true, true, true
	second := primary
	second.Call = e2proxy.CloneCallResult(primary.Call)
	second.Call.CallRef = "selected-call-b"
	second.Call.Observations[0].AttemptRef = "selected-attempt-b"
	second.Call.Observations[0].Sequence = 2
	state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil, []e2runner.TrustedResult{primary, second}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.m.pg.QueryRowContext(t.Context(), `SELECT payload FROM exploration_nodes WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND origin=$2`, taskID, diagnosisRunOrigin).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record diagnosisRunRecord
	if json.Unmarshal(raw, &record) != nil || record.Verification == nil || record.Verification.Version != diagnosisVerificationSelected ||
		record.Verification.NonIDORConfirmed != 0 || record.Verification.NonIDORNegative != 0 || record.Verification.VerificationPending != 1 ||
		len(record.Verification.Candidates) != 1 || record.Verification.Candidates[0].Result.Reason != "selected_verification_required" ||
		record.Verification.ObservationsCount != 2 {
		t.Fatalf("selected run did not retain pending candidate and observations: %+v", record.Verification)
	}
	for _, suffix := range []string{"&audience=internal", "&audience=shared", "&audience=internal&format=docx"} {
		response := request("GET", fmt.Sprintf("/api/report/diagnostic?task=%d%s", taskID, suffix), "")
		if response.Code != 200 {
			t.Fatalf("report %s: status=%d", suffix, response.Code)
		}
		if !strings.Contains(suffix, "docx") && (!strings.Contains(response.Body.String(), fmt.Sprintf("candidate-%d", id)) ||
			!strings.Contains(response.Body.String(), "selected_verification_required")) {
			t.Fatal("candidate or pending reason missing from report")
		}
	}
	path := fmt.Sprintf("/api/product/tasks/%d/candidates/%d/verifications", taskID, id)
	owner := &verificationOwnerEndpoint{task: fmt.Sprint(taskID)}
	invoke := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		candidateVerificationHTTP(response, req, s.m.pg.DB, func(*http.Request) (string, error) { return "ARTEX", nil }, owner)
		return response
	}
	history := invoke("GET", "")
	if history.Code != 200 {
		t.Fatalf("selected candidate history status=%d", history.Code)
	}
	legacy := invoke("POST", fmt.Sprintf(`{"request_id":"selected-test-1","evidence_digest":"%s","method":"active/v1"}`, rows[0].EvidenceDigest))
	if legacy.Code != 409 || !strings.Contains(legacy.Body.String(), "selected_verification_method_required") {
		t.Fatalf("legacy replay accepted for selected run: status=%d", legacy.Code)
	}
	selectedBody := fmt.Sprintf(`{"request_id":"selected-test-2","evidence_digest":"%s"}`, rows[0].EvidenceDigest)
	selected := invoke("POST", selectedBody)
	var page candidateVerificationPage
	if selected.Code != 200 || json.Unmarshal(selected.Body.Bytes(), &page) != nil || len(page.Verifications) != 1 ||
		page.Verifications[0].Method != "llm/v1" || page.Verifications[0].Latest.State != "requested" ||
		page.Verifications[0].Latest.Verdict != nil {
		t.Fatalf("selected request not stored as unassessed: status=%d", selected.Code)
	}
	for _, suffix := range []string{"&audience=internal", "&audience=shared", "&audience=internal&format=docx"} {
		response := request("GET", fmt.Sprintf("/api/report/diagnostic?task=%d%s", taskID, suffix), "")
		if response.Code != 200 {
			t.Fatalf("selected request blocked completed report %s: status=%d body=%s", suffix, response.Code, response.Body.String())
		}
	}
	selectedID, err := strconv.ParseInt(page.Verifications[0].ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	context, err := ReadCandidateSelectedContext(t.Context(), s.m.pg.DB, selectedID)
	if err != nil || context.Candidate.ID != id || len(context.Observations) != 2 || context.RunRef != passiveRunRef ||
		context.OriginalObservation == nil || context.OriginalObservation.Source.CallRef != context.Candidate.CallRef {
		t.Fatalf("selected context did not bind original evidence: count=%d err=%v", len(context.Observations), err)
	}
	changed := append([]candidateverify.StoredObservationV2(nil), context.Observations...)
	var withoutOriginal []candidateverify.StoredObservationV2
	for i := range changed {
		if changed[i].Source.CallRef == context.Candidate.CallRef {
			changed[i].Source.Observation.StatusCode++
		} else {
			withoutOriginal = append(withoutOriginal, context.Observations[i])
		}
	}
	if _, err := selectedOriginalObservation(context.Candidate, changed); err == nil {
		t.Fatal("candidate's original call accepted with changed observation")
	}
	if missing, err := selectedOriginalObservation(context.Candidate, withoutOriginal); err != nil || missing != nil {
		t.Fatal("missing original call was represented as available")
	}
	context.Candidate.Summary = "Observed concern; password=synthetic-model-secret"
	modelRequest, err := BuildCandidateAnalysisRequest(context, 512)
	if err != nil || modelRequest.MaxTokens != 512 || len(modelRequest.Tools) != 0 || len(modelRequest.Messages) != 1 ||
		strings.Contains(modelRequest.Messages[0].Text(), "synthetic-model-secret") ||
		!strings.Contains(modelRequest.Messages[0].Text(), `"original_observation_available":true`) {
		t.Fatal("selected model context has unsafe tools, missing provenance, or a synthetic secret")
	}
	if _, err := BuildCandidateAnalysisRequest(context, 0); err == nil {
		t.Fatal("model request without an installed output cap was accepted")
	}
	proposal := fmt.Sprintf(`{"version":"selected/analysis-v1","candidate_id":%d,"claim":"검토 대상","normal_explanation":"정상 설명 가능","existing_call_refs":[%q],"business_questions":["이 접근이 의도된 정책인가요?"],"actions":[{"purpose":"관찰 비교","method":"GET","path_hint":"/books/v1"}],"limitations":"원문 입력은 저장되지 않음"}`,
		id, context.Candidate.CallRef)
	if _, err := ParseCandidateAnalysisProposal([]byte(proposal), context); err != nil {
		t.Fatal("bound synthetic analysis proposal rejected")
	}
	if _, err := ParseCandidateAnalysisProposal([]byte(strings.Replace(proposal, context.Candidate.CallRef, "other-candidate-call", 1)), context); err == nil {
		t.Fatal("unobserved model source accepted")
	}
	if _, err := ParseCandidateAnalysisProposal([]byte(strings.TrimSuffix(proposal, "}")+`,"verdict":"confirmed"}`), context); err == nil {
		t.Fatal("model proposed an authoritative verdict in the analysis stage")
	}
	secretProposal := strings.Replace(proposal, "검토 대상", "password=synthetic-analysis-secret", 1)
	if parsed, err := ParseCandidateAnalysisProposal([]byte(secretProposal), context); err != nil ||
		strings.Contains(parsed.Claim, "synthetic-analysis-secret") {
		t.Fatal("model proposal returned a synthetic secret")
	}
	if retry := invoke("POST", selectedBody); retry.Code != 200 {
		t.Fatalf("idempotent retry status=%d", retry.Code)
	}
	if different := invoke("POST", fmt.Sprintf(`{"request_id":"selected-test-3","evidence_digest":"%s"}`, rows[0].EvidenceDigest)); different.Code != 409 {
		t.Fatalf("parallel candidate selection status=%d", different.Code)
	}
	current, err := loadDiagnosticReport(t.Context(), s.m.pg, taskID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, entry := range current.Entries {
		if entry.ID == fmt.Sprintf("candidate-%d", id) {
			seen = true
			if entry.Outcome != "review" || entry.LatestVerification == nil || entry.LatestVerification.State != "requested" {
				t.Fatalf("selection changed verdict or was omitted from report: %+v", entry.LatestVerification)
			}
		}
	}
	if !seen {
		t.Fatal("selected candidate omitted from current report")
	}
	cancelPath := path + "/" + page.Verifications[0].ID + "/cancel"
	cancelRequest := httptest.NewRequest("POST", cancelPath, strings.NewReader(`{}`))
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelResponse := httptest.NewRecorder()
	candidateVerificationCancelHTTP(cancelResponse, cancelRequest, s.m.pg.DB, func(*http.Request) (string, error) { return "ARTEX", nil }, owner)
	if cancelResponse.Code != 200 || !strings.Contains(cancelResponse.Body.String(), `"process_state":"cancelled"`) {
		t.Fatalf("selected request cancellation status=%d", cancelResponse.Code)
	}
	if _, err := ReadCandidateSelectedContext(t.Context(), s.m.pg.DB, selectedID); err == nil {
		t.Fatal("cancelled selection still readable as pending work")
	}
	next := invoke("POST", fmt.Sprintf(`{"request_id":"selected-test-3","evidence_digest":"%s"}`, rows[0].EvidenceDigest))
	if next.Code != 200 {
		t.Fatalf("new selection after cancellation status=%d", next.Code)
	}
	var nextPage candidateVerificationPage
	if json.Unmarshal(next.Body.Bytes(), &nextPage) != nil || len(nextPage.Verifications) != 2 {
		t.Fatal("new selection history missing")
	}
	nextID, err := strconv.ParseInt(nextPage.Verifications[0].ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE diagnostic_candidate_observations_v2 SET source_digest=repeat('0',64)
WHERE id=(SELECT id FROM diagnostic_candidate_observations_v2 WHERE task_id=$1 AND run_ref=$2 ORDER BY id LIMIT 1)`, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCandidateSelectedContext(t.Context(), s.m.pg.DB, nextID); err == nil {
		t.Fatal("damaged stored observation accepted as LLM context")
	}
}

func TestSelectedVerificationPublishesZeroCandidateReportPostgres(t *testing.T) {
	s, finding, request := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(finding.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if err := saveDiagnosisSelectedVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"&audience=internal", "&audience=shared", "&audience=internal&format=docx"} {
		response := request("GET", fmt.Sprintf("/api/report/diagnostic?task=%d%s", taskID, suffix), "")
		if response.Code != 200 {
			t.Fatalf("zero-candidate report %s: status=%d", suffix, response.Code)
		}
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE exploration_nodes SET payload=jsonb_set(payload,'{verification,non_idor_confirmed}','1'::jsonb)
WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND origin=$2`, taskID, diagnosisRunOrigin); err != nil {
		t.Fatal(err)
	}
	if damaged := request("GET", fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal", taskID), ""); damaged.Code == 200 {
		t.Fatal("corrupt selected verification count exported")
	}
}

func TestSelectedContextMarksUnavailableOriginalObservationPostgres(t *testing.T) {
	s, finding, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(finding.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if err := saveDiagnosisSelectedVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	id := storePassiveCandidate(t, s, taskID, "html_nosniff_missing", "nosniff_missing", "")
	state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/product/tasks/%d/candidates/%d/verifications", taskID, id)
	req := httptest.NewRequest("POST", path, strings.NewReader(fmt.Sprintf(`{"request_id":"selected-unavailable-1","evidence_digest":"%s"}`, rows[0].EvidenceDigest)))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	candidateVerificationHTTP(response, req, s.m.pg.DB, func(*http.Request) (string, error) { return "ARTEX", nil }, &verificationOwnerEndpoint{task: fmt.Sprint(taskID)})
	var page candidateVerificationPage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Verifications) != 1 {
		t.Fatalf("source-unavailable selection status=%d", response.Code)
	}
	selectedID, err := strconv.ParseInt(page.Verifications[0].ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	context, err := ReadCandidateSelectedContext(t.Context(), s.m.pg.DB, selectedID)
	if err != nil || !context.SourceUnavailable || len(context.Observations) != 0 || context.OriginalObservation != nil {
		t.Fatalf("unavailable original observation misrepresented: count=%d err=%v", len(context.Observations), err)
	}
}

func TestSelectedAnalysisPersistsWithoutChangingVerdictPostgres(t *testing.T) {
	s, finding, reportRequest := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(finding.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if err := saveDiagnosisSelectedVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	candidateID := storePassiveCandidate(t, s, taskID, "", "", "")
	state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/product/tasks/%d/candidates/%d/verifications", taskID, candidateID)
	owner := &verificationOwnerEndpoint{task: fmt.Sprint(taskID)}
	invoke := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		candidateVerificationHTTP(response, req, s.m.pg.DB, func(*http.Request) (string, error) { return "ARTEX", nil }, owner)
		return response
	}
	selected := invoke(http.MethodPost, fmt.Sprintf(`{"request_id":"analysis-test-1","evidence_digest":"%s"}`, rows[0].EvidenceDigest))
	var page candidateVerificationPage
	if selected.Code != 200 || json.Unmarshal(selected.Body.Bytes(), &page) != nil || len(page.Verifications) != 1 {
		t.Fatalf("selection failed: %d", selected.Code)
	}
	requestID, err := strconv.ParseInt(page.Verifications[0].ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	selectedContext, err := ReadCandidateSelectedContext(t.Context(), s.m.pg.DB, requestID)
	if err != nil || !selectedContext.SourceUnavailable {
		t.Fatalf("selected context: %v", err)
	}
	var modelCalls atomic.Int64
	modelHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("synthetic model request escaped pinned endpoint: %s %s", r.Method, r.URL.Path)
		}
		content, _ := json.Marshal(CandidateAnalysisProposal{Version: "selected/analysis-v1", CandidateID: candidateID, Claim: "Synthetic concern",
			NormalExplanation: "A normal explanation remains possible", BusinessQuestions: []string{"Is this access intended?"}, Limitations: "Original observation unavailable"})
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "synthetic-analysis", "object": "chat.completion", "model": "local:selected-test",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 120, "completion_tokens": 45, "total_tokens": 165}})
	}))
	defer modelHost.Close()
	profile := &db.LLMProfile{Name: fmt.Sprintf("selected-analysis-test-%d", time.Now().UnixNano()), Format: "openai", Model: "local:selected-test",
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
	manifest := fmt.Sprintf(`{"version":1,"profiles":[{"profile_id":%d,"format":"openai","model":"local:selected-test","base_url":%q,"addresses":["127.0.0.1"]}]}`, profileID, profile.BaseURL)
	if _, err := llmpolicy.ParseProfileManifest([]byte(manifest)); err != nil {
		t.Fatal("synthetic manifest malformed", err)
	}
	if os.WriteFile(manifestPath, []byte(manifest), 0600) != nil {
		t.Fatal("synthetic model policy unavailable")
	}
	budget, err := NewFirstDiagnosisModelBudget(diagnosisprep.Budget{ModelRequests: 1, InputTokens: 65536, OutputTokens: 512, DurationSeconds: 180}, 65536, 512,
		func(diagnosisprep.ModelUsage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	modelOptions := FirstDiagnosisModelOptions{Mode: "local_only", LocalManifestPath: manifestPath, Budget: budget}
	if err := CheckFirstDiagnosisModelOptions(modelOptions, llmpolicy.ProfileTarget{ID: profileID, Format: profile.Format, Model: profile.Model, BaseURL: profile.BaseURL}); err != nil {
		t.Fatal("synthetic installed model policy invalid", err)
	}
	model, err := NewSelectedCandidateModel(t.Context(), s.m.pg.DB, profileID, modelOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginCandidateAnalysis(t.Context(), s.m.pg.DB, selectedContext); err != nil {
		t.Fatal(err)
	}
	if err := BeginCandidateAnalysis(t.Context(), s.m.pg.DB, selectedContext); err == nil {
		t.Fatal("model send fence replayed")
	}
	proposal, err := model.Analyze(t.Context(), selectedContext, func(_ context.Context) error { return nil })
	if err != nil || modelCalls.Load() != 1 {
		t.Fatalf("pinned synthetic model analysis: calls=%d err=%v", modelCalls.Load(), err)
	}
	usage := budget.Snapshot()
	if err := CompleteCandidateAnalysis(t.Context(), s.m.pg.DB, selectedContext, profileID, proposal, usage); err != nil {
		t.Fatal(err)
	}
	if err := CompleteCandidateAnalysis(t.Context(), s.m.pg.DB, selectedContext, profileID, proposal, usage); err == nil {
		t.Fatal("analysis history overwritten")
	}
	page = candidateVerificationPage{}
	history := invoke(http.MethodGet, "")
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &page) != nil || len(page.Verifications) != 1 ||
		page.Verifications[0].Latest.State != "analysis_ready" || page.Verifications[0].Latest.Verdict != nil ||
		page.Verifications[0].Analysis == nil || page.Verifications[0].Analysis.Proposal.Claim != "Synthetic concern" {
		t.Fatalf("analysis history unavailable: %d", history.Code)
	}
	answerPath := path + "/" + page.Verifications[0].ID + "/answers"
	answerBody := fmt.Sprintf(`{"analysis_digest":%q,"question_index":1,"answer":"No, that access is not intended"}`, page.Verifications[0].Analysis.Digest)
	answer := func(principal, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, answerPath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		candidateVerificationAnswerHTTP(response, req, s.m.pg.DB, func(*http.Request) (string, error) { return principal, nil }, owner)
		return response
	}
	if got := answer("other", answerBody); got.Code != 403 {
		t.Fatalf("other owner answered: %d", got.Code)
	}
	if got := answer("ARTEX", strings.Replace(answerBody, page.Verifications[0].Analysis.Digest, strings.Repeat("0", 64), 1)); got.Code != 409 {
		t.Fatalf("changed analysis accepted: %d", got.Code)
	}
	if got := answer("ARTEX", answerBody); got.Code != 200 {
		t.Fatalf("business answer failed: %d", got.Code)
	}
	if got := answer("ARTEX", answerBody); got.Code != 200 {
		t.Fatalf("identical business answer was not idempotent: %d", got.Code)
	}
	if got := answer("ARTEX", strings.Replace(answerBody, "not intended", "intended", 1)); got.Code != 409 {
		t.Fatalf("conflicting business answer accepted: %d", got.Code)
	}
	page = candidateVerificationPage{}
	answered := invoke(http.MethodGet, "")
	if answered.Code != 200 || json.Unmarshal(answered.Body.Bytes(), &page) != nil || len(page.Verifications) != 1 ||
		len(page.Verifications[0].BusinessAnswers) != 1 || page.Verifications[0].BusinessAnswers[0].Question != "Is this access intended?" ||
		page.Verifications[0].Latest.Verdict != nil || modelCalls.Load() != 1 {
		t.Fatalf("business answer changed verdict or caused model activity: %d", answered.Code)
	}
	for _, suffix := range []string{"&audience=internal", "&audience=shared", "&audience=internal&format=docx"} {
		response := reportRequest("GET", fmt.Sprintf("/api/report/diagnostic?task=%d%s", taskID, suffix), "")
		if response.Code != 200 {
			t.Fatalf("analysis broke completed report %s: %d", suffix, response.Code)
		}
		content := response.Body.String()
		if strings.Contains(suffix, "docx") {
			content = diagnosticWordText(t, wordParts(t, response.Body.Bytes())["word/document.xml"])
		}
		if strings.Contains(suffix, "shared") {
			if strings.Contains(content, "Is this access intended?") || strings.Contains(content, "No, that access is not intended") {
				t.Fatal("business fact leaked to shared report")
			}
		} else if !strings.Contains(content, "Is this access intended?") || !strings.Contains(content, "No, that access is not intended") {
			t.Fatalf("business fact missing from %s report", suffix)
		}
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE diagnostic_candidate_verification_analyses SET proposal_digest=repeat('0',64) WHERE request_id=$1`, requestID); err != nil {
		t.Fatal(err)
	}
	if response := invoke(http.MethodGet, ""); response.Code == 200 {
		t.Fatal("corrupt model analysis exposed")
	}
	if response := reportRequest("GET", fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal", taskID), ""); response.Code == 200 {
		t.Fatal("corrupt analysis included in report")
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE diagnostic_candidate_verification_analyses SET proposal_digest=$2 WHERE request_id=$1`, requestID, page.Verifications[0].Analysis.Digest); err != nil {
		t.Fatal(err)
	}
	firstCancel := httptest.NewRequest(http.MethodPost, path+"/"+page.Verifications[0].ID+"/cancel", strings.NewReader(`{}`))
	firstCancel.Header.Set("Content-Type", "application/json")
	firstCancelled := httptest.NewRecorder()
	candidateVerificationCancelHTTP(firstCancelled, firstCancel, s.m.pg.DB, func(*http.Request) (string, error) { return "ARTEX", nil }, owner)
	if firstCancelled.Code != 200 {
		t.Fatalf("ready analysis cancellation: %d", firstCancelled.Code)
	}
	if followup := invoke(http.MethodGet, ""); followup.Code != 200 {
		t.Fatalf("cancelled analysis broke history: %d", followup.Code)
	}
	second := invoke(http.MethodPost, fmt.Sprintf(`{"request_id":"analysis-test-2","evidence_digest":"%s"}`, rows[0].EvidenceDigest))
	var secondPage candidateVerificationPage
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &secondPage) != nil || len(secondPage.Verifications) != 2 {
		t.Fatal("second analysis request unavailable")
	}
	secondID, err := strconv.ParseInt(secondPage.Verifications[0].ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	secondContext, err := ReadCandidateSelectedContext(t.Context(), s.m.pg.DB, secondID)
	if err != nil || BeginCandidateAnalysis(t.Context(), s.m.pg.DB, secondContext) != nil {
		t.Fatal("second model send fence unavailable", err)
	}
	cancelPath := path + "/" + secondPage.Verifications[0].ID + "/cancel"
	cancelRequest := httptest.NewRequest(http.MethodPost, cancelPath, strings.NewReader(`{}`))
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelResponse := httptest.NewRecorder()
	candidateVerificationCancelHTTP(cancelResponse, cancelRequest, s.m.pg.DB, func(*http.Request) (string, error) { return "ARTEX", nil }, owner)
	if cancelResponse.Code != 200 || !strings.Contains(cancelResponse.Body.String(), `"process_state":"cancelled"`) {
		t.Fatalf("in-flight analysis cancellation failed: %d", cancelResponse.Code)
	}
	if err := CompleteCandidateAnalysis(t.Context(), s.m.pg.DB, secondContext, profileID, proposal, usage); err == nil {
		t.Fatal("cancelled analysis accepted late model output")
	}
	third := invoke(http.MethodPost, fmt.Sprintf(`{"request_id":"analysis-test-3","evidence_digest":"%s"}`, rows[0].EvidenceDigest))
	var thirdPage candidateVerificationPage
	if third.Code != 200 || json.Unmarshal(third.Body.Bytes(), &thirdPage) != nil || len(thirdPage.Verifications) != 3 {
		t.Fatal("third selected request unavailable")
	}
	thirdID, err := strconv.ParseInt(thirdPage.Verifications[0].ID, 10, 64)
	if err != nil || FailCandidateAnalysis(t.Context(), s.m.pg.DB, thirdID, "budget_unavailable") != nil {
		t.Fatal("pre-call budget failure was not recorded", err)
	}
	if failed := invoke(http.MethodGet, ""); failed.Code != 200 {
		t.Fatalf("budget failure broke history: %d", failed.Code)
	}
	if failedReport := reportRequest("GET", fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal", taskID), ""); failedReport.Code != 200 {
		t.Fatalf("budget failure broke report: %d", failedReport.Code)
	}
}
