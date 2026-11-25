//go:build linux

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
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/report"
)

func TestCandidateActivePlanAndDecisionProductAPIPostgres(t *testing.T) {
	s, finding, reportRequest := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(finding.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	task := *stored.TaskID
	if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, task, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	candidateID := storePassiveCandidate(t, s, task, "html_nosniff_missing", "nosniff_missing", "")
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, task,
		requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	base, _, _ := reportFixtureService(t, task)
	proof, err := base.Call(t.Context(), diagnosisprep.Request{Version: diagnosisprep.Version, RequestID: "synthetic", Command: diagnosisprep.AuthorizeTask, Principal: "ARTEX", TaskID: fmt.Sprint(task)})
	if err != nil || proof.Job == nil || proof.Job.Scope == nil {
		t.Fatal("original job unavailable", err)
	}
	job := *proof.Job
	scope := *job.Scope
	now := time.Now().UTC()
	scope.TargetRef, scope.Origin = "target-synthetic", "https://owned.example"
	scope.PathPrefixes, scope.Methods, scope.ExpiresAt = []string{"/documents"}, []string{"GET"}, now.Add(time.Hour)
	job.Scope = &scope
	job.Input.TargetURL = "https://owned.example/"
	target := diagnosisprep.ProductTarget{ID: scope.TargetRef, Origin: scope.Origin, State: "approved", Revision: 3,
		PathPrefixes: scope.PathPrefixes, Methods: scope.Methods, ApprovedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	endpoint := &followupFixture{jobs: map[string]*diagnosisprep.Job{fmt.Sprint(task): &job}, target: target}
	auth := func(*http.Request) (string, error) { return "ARTEX", nil }
	path := fmt.Sprintf("/api/product/tasks/%d/candidates/%d/verifications", task, candidateID)
	invoke := func(method, uri, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, uri, strings.NewReader(body))
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		if strings.HasSuffix(uri, "/decision") {
			candidateVerificationDecisionHTTP(w, r, s.m.pg.DB, auth, endpoint)
		} else if strings.HasSuffix(uri, "/cancel") {
			candidateVerificationCancelHTTP(w, r, s.m.pg.DB, auth, endpoint)
		} else {
			candidateVerificationHTTP(w, r, s.m.pg.DB, auth, endpoint)
		}
		return w
	}
	rows, err := s.m.ResolveTask(fmt.Sprint(task)).Store.ListDiagnosticCandidates(task, passiveRunRef, 2)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	requestBody := fmt.Sprintf(`{"request_id":"synthetic-active-request-001","evidence_digest":%q,"method":"active/v1"}`, rows[0].EvidenceDigest)
	created := invoke(http.MethodPost, path, requestBody)
	var page candidateVerificationPage
	if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &page) != nil || len(page.Verifications) != 1 || page.Verifications[0].Plan == nil || page.Verifications[0].Latest.State != "awaiting_approval" {
		t.Fatalf("plan API %d: %s", created.Code, created.Body.String())
	}
	item := page.Verifications[0]
	decisionPath := path + "/" + item.ID + "/decision"
	if bad := invoke(http.MethodPost, decisionPath, fmt.Sprintf(`{"plan_digest":%q,"decision":"approved"}`, strings.Repeat("0", 64))); bad.Code != 409 {
		t.Fatalf("unbound approval accepted: %d %s", bad.Code, bad.Body.String())
	}
	target.State = "revoked"
	endpoint.target = target
	if revoked := invoke(http.MethodPost, decisionPath, fmt.Sprintf(`{"plan_digest":%q,"decision":"approved"}`, item.PlanDigest)); revoked.Code != 409 {
		t.Fatalf("revoked target approved: %d %s", revoked.Code, revoked.Body.String())
	}
	target.State = "approved"
	endpoint.target = target
	cancelled := invoke(http.MethodPost, path+"/"+item.ID+"/cancel", fmt.Sprintf(`{"plan_digest":%q}`, item.PlanDigest))
	if cancelled.Code != 200 || !strings.Contains(cancelled.Body.String(), `"process_state":"cancelled"`) {
		t.Fatalf("preapproval cancellation %d: %s", cancelled.Code, cancelled.Body.String())
	}
	requestBody = fmt.Sprintf(`{"request_id":"synthetic-active-request-002","evidence_digest":%q,"method":"active/v1"}`, rows[0].EvidenceDigest)
	created = invoke(http.MethodPost, path, requestBody)
	if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &page) != nil || len(page.Verifications) != 2 || page.Verifications[0].Latest.State != "awaiting_approval" {
		t.Fatalf("cancelled proposal blocked next request %d: %s", created.Code, created.Body.String())
	}
	item = page.Verifications[0]
	decisionPath = path + "/" + item.ID + "/decision"
	approved := invoke(http.MethodPost, decisionPath, fmt.Sprintf(`{"plan_digest":%q,"decision":"approved"}`, item.PlanDigest))
	if approved.Code != 200 {
		t.Fatalf("approval API %d: %s", approved.Code, approved.Body.String())
	}
	if duplicate := invoke(http.MethodPost, decisionPath, fmt.Sprintf(`{"plan_digest":%q,"decision":"approved"}`, item.PlanDigest)); duplicate.Code != 200 {
		t.Fatalf("idempotent approval %d: %s", duplicate.Code, duplicate.Body.String())
	}
	if changed := invoke(http.MethodPost, decisionPath, fmt.Sprintf(`{"plan_digest":%q,"decision":"rejected"}`, item.PlanDigest)); changed.Code != 409 {
		t.Fatalf("decision overwrite %d", changed.Code)
	}
	got := invoke(http.MethodGet, path, "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"process_state":"approved"`) {
		t.Fatalf("decision history %d: %s", got.Code, got.Body.String())
	}
	var executions int
	if err := s.m.pg.QueryRowContext(t.Context(), `SELECT count(*) FROM diagnostic_candidate_verification_executions WHERE request_id=$1`, item.ID).Scan(&executions); err != nil || executions != 0 {
		t.Fatalf("approval unexpectedly sent target request: %d %v", executions, err)
	}
	t.Run("failed followup then stored reread", func(t *testing.T) {
		storeSyntheticCandidateConfirmation(t, s, rows[0], item)
		assertCount := func(want int) {
			t.Helper()
			model, err := loadDiagnosticReport(t.Context(), s.m.pg, task, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := report.PrepareDiagnostic(model)
			if err != nil || prepared.Confirmed != want {
				t.Fatalf("confirmed=%d want=%d err=%v", prepared.Confirmed, want, err)
			}
		}
		assertCount(1)
		next := invoke(http.MethodPost, path, strings.Replace(requestBody, "request-002", "request-003", 1))
		if next.Code != 200 || json.Unmarshal(next.Body.Bytes(), &page) != nil {
			t.Fatalf("next request: %d %s", next.Code, next.Body)
		}
		newest := page.Verifications[0]
		decision := invoke(http.MethodPost, path+"/"+newest.ID+"/decision", fmt.Sprintf(`{"plan_digest":%q,"decision":"approved"}`, newest.PlanDigest))
		if decision.Code != 200 {
			t.Fatal(decision.Code, decision.Body)
		}
		_, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version)
SELECT request_id,3,'failed','execution_interrupted',sources,sources_digest,rule_version FROM diagnostic_candidate_verification_events WHERE request_id=$1 AND step=2`, newest.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertCount(0)
		reread := invoke(http.MethodPost, path, fmt.Sprintf(`{"request_id":"stored-after-failure","evidence_digest":%q}`, rows[0].EvidenceDigest))
		if reread.Code != 200 {
			t.Fatal(reread.Code, reread.Body)
		}
		assertCount(0)
		for _, format := range []string{"html", "docx"} {
			exported := reportRequest(http.MethodGet, fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal&format=%s", task, format), "")
			if exported.Code != 200 {
				t.Fatal(format, exported.Code, exported.Body)
			}
			content := exported.Body.String()
			if format == "docx" {
				content = diagnosticWordText(t, wordParts(t, exported.Body.Bytes())["word/document.xml"])
			}
			if !strings.Contains(content, "이후 추가 검증으로 현재 집계에서 제외") || !strings.Contains(content, "html_nosniff_missing_followup_twice") {
				t.Fatalf("%s lost historical confirmation", format)
			}
		}
	})

}

// Record a sealed, synthetic observation pair in the same schema read by
// the product report. This fixture has no target or network execution.
func storeSyntheticCandidateConfirmation(t *testing.T, s *Server, candidate db.DiagnosticCandidate, proposal candidateVerificationAttempt) {
	t.Helper()
	ctx := t.Context()
	task, err := s.m.CreateTask("synthetic report history", "stored evidence fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	followup, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	request, err := strconv.ParseInt(proposal.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	const run = "synthetic-stored-followup"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.m.pg.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE tasks SET queued=false,paused=true WHERE id=$1`, followup)
	exec(`INSERT INTO diagnostic_candidate_verification_executions(request_id,followup_task_id,approval_event_id,plan_digest,run_ref)
SELECT request_id,$2,id,$3,$4 FROM diagnostic_candidate_verification_events WHERE request_id=$1 AND step=2`, request, followup, proposal.PlanDigest, run)
	exec(`INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources,sources_digest,rule_version,execution_ref)
SELECT request_id,3,'running','execution_started',sources,sources_digest,rule_version,$2 FROM diagnostic_candidate_verification_events WHERE request_id=$1 AND step=2`, request, run)
	exec(`UPDATE tasks SET status='done',completed_at=clock_timestamp() WHERE id=$1`, followup)
	mac := strings.Repeat("a", 64)
	err = s.m.pg.RecordSealCheck(ctx, db.SealCheckRecord{TaskID: followup, RunRef: run,
		Head:   db.ProductCheckpoint{JournalID: "synthetic-journal", Sequence: 2, MAC: mac},
		Checks: []db.SealCheck{{Sequence: 1, MAC: strings.Repeat("b", 64), Included: true}, {Sequence: 2, MAC: mac, Included: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Trusted e2runner.TrustedResult `json:"trusted"`
	}
	if err := json.Unmarshal([]byte(candidate.Evidence), &envelope); err != nil {
		t.Fatal(err)
	}
	var calls []e2runner.TrustedResult
	for i := 1; i <= 2; i++ {
		call := envelope.Trusted
		call.Call = e2proxy.CloneCallResult(call.Call)
		call.Call.CallRef = fmt.Sprintf("followup-call-%d", i)
		call.Call.Observations[0].AttemptRef = fmt.Sprintf("followup-attempt-%d", i)
		call.Call.Observations[0].Sequence = uint64(i)
		call.ProcessDrained, call.ProxyDrained, call.Execution.AuditComplete = true, true, true
		call.Execution.Scope.TaskRef, call.Execution.Scope.RunRef = task.ID, run
		calls = append(calls, call)
	}
	observations, err := candidateverify.ProjectTrustedV2(followup, run, calls)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CompleteCandidateActiveVerification(ctx, s.m.pg.DB, request, followup, proposal.PlanDigest, run, observations)
	if err != nil || result.Status != candidateverify.Confirmed {
		t.Fatalf("fixture confirmation: %s %v", result.Status, err)
	}
}
