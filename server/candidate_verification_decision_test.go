//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisprep"
)

func TestCandidateActivePlanAndDecisionProductAPIPostgres(t *testing.T) {
	s, finding, _ := trafficEvidenceServer(t)
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
}
