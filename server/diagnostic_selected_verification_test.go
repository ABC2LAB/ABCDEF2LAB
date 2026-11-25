package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
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
	modelRequest, err := BuildCandidateAnalysisRequest(context)
	if err != nil || len(modelRequest.Tools) != 0 || len(modelRequest.Messages) != 1 ||
		strings.Contains(modelRequest.Messages[0].Text(), "synthetic-model-secret") ||
		!strings.Contains(modelRequest.Messages[0].Text(), `"original_observation_available":true`) {
		t.Fatal("selected model context has unsafe tools, missing provenance, or a synthetic secret")
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
