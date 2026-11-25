package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if next := invoke("POST", fmt.Sprintf(`{"request_id":"selected-test-3","evidence_digest":"%s"}`, rows[0].EvidenceDigest)); next.Code != 200 {
		t.Fatalf("new selection after cancellation status=%d", next.Code)
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
}
