package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/artex/transmit"
)

func TestCandidateActivePlanRequiresCurrentExactApprovedScopePostgres(t *testing.T) {
	s, f, reportRequest := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	storePassiveCandidate(t, s, taskID, "html_nosniff_missing", "nosniff_missing", "")
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID,
		requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	candidate := rows[0]
	automatic := report.CandidateAssessment{CandidateID: candidate.ID, EvidenceDigest: candidate.EvidenceDigest, Status: "insufficient", Reason: "independent_observation_missing"}
	now := time.Now().UTC()
	input := diagnosisprep.Input{TargetURL: "https://owned.example/", ProfileID: "target"}
	scope := diagnosisprep.Scope{TargetRef: "target-synthetic", Origin: "https://owned.example", PathPrefixes: []string{"/documents"}, Methods: []string{"GET"}, ExpiresAt: now.Add(time.Hour)}
	target := diagnosisprep.ProductTarget{ID: "target-synthetic", Origin: scope.Origin, State: "approved", Revision: 3, PathPrefixes: []string{"/documents"}, Methods: []string{"GET"}, ApprovedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	plan, digest, err := makeCandidateActivePlan(now, candidate, automatic, input, scope, target)
	if err != nil || plan.MaxRequests != 2 || plan.Method != "GET" || plan.ExactPath != "/documents" || plan.TargetRevision != 3 || plan.ModelCostMicros != 0 || len(digest) != 64 {
		t.Fatalf("bounded plan: %+v %q %v", plan, digest, err)
	}
	for _, tc := range []struct {
		name string
		edit func(*diagnosisprep.Input, *diagnosisprep.Scope, *diagnosisprep.ProductTarget)
	}{
		{"excluded", func(i *diagnosisprep.Input, _ *diagnosisprep.Scope, _ *diagnosisprep.ProductTarget) {
			i.ExcludedPaths = "/documents"
		}},
		{"other-target", func(_ *diagnosisprep.Input, _ *diagnosisprep.Scope, t *diagnosisprep.ProductTarget) {
			t.ID = "other-target"
		}},
		{"revoked", func(_ *diagnosisprep.Input, _ *diagnosisprep.Scope, t *diagnosisprep.ProductTarget) {
			t.State = "revoked"
		}},
		{"expired", func(_ *diagnosisprep.Input, _ *diagnosisprep.Scope, t *diagnosisprep.ProductTarget) {
			t.ExpiresAt = now
		}},
		{"method-not-approved", func(_ *diagnosisprep.Input, _ *diagnosisprep.Scope, t *diagnosisprep.ProductTarget) {
			t.Methods = []string{"HEAD"}
		}},
		{"path-not-approved", func(_ *diagnosisprep.Input, _ *diagnosisprep.Scope, t *diagnosisprep.ProductTarget) {
			t.PathPrefixes = []string{"/other"}
		}},
		{"other-origin", func(_ *diagnosisprep.Input, _ *diagnosisprep.Scope, t *diagnosisprep.ProductTarget) {
			t.Origin = "https://other.example"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, sc, tg := input, scope, target
			tc.edit(&i, &sc, &tg)
			if _, _, err := makeCandidateActivePlan(now, candidate, automatic, i, sc, tg); err == nil {
				t.Fatal("unapproved plan accepted")
			}
		})
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var requestID, approvalEventID int64
	if err := s.m.pg.QueryRowContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_requests(task_id,candidate_id,original_run_ref,original_evidence_version,original_evidence_digest,requester,request_key,method)
VALUES($1,$2,$3,$4,$5,'ARTEX','synthetic-active-001','active/v1') RETURNING id`, taskID, candidate.ID, candidate.RunRef, candidate.EvidenceVersion, candidate.EvidenceDigest).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_plans(request_id,plan,plan_digest,target_id,target_revision,expires_at) VALUES($1,$2::jsonb,$3,$4,$5,$6)`,
		requestID, raw, digest, plan.TargetID, plan.TargetRevision, plan.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	const emptySourcesDigest = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
	if _, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources_digest,rule_version) VALUES($1,1,'awaiting_approval','independent_observation_missing',$2,'candidate-active/v1')`, requestID, emptySourcesDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_decisions(request_id,plan_digest,decision,decided_by) VALUES($1,$2,'approved','ARTEX')`, requestID, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong plan digest accepted")
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_decisions(request_id,plan_digest,decision,decided_by) VALUES($1,$2,'approved','ARTEX')`, requestID, digest); err != nil {
		t.Fatal(err)
	}
	if err := s.m.pg.QueryRowContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources_digest,rule_version,approval_ref)
VALUES($1,2,'approved','user_approved',$2,'candidate-active/v1','synthetic-approval-001') RETURNING id`, requestID, emptySourcesDigest).Scan(&approvalEventID); err != nil {
		t.Fatal(err)
	}
	approvedTx, err := s.m.pg.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadApprovedCandidatePlan(t.Context(), approvedTx, requestID, digest); err != nil {
		approvedTx.Rollback()
		t.Fatal("approved original plan rejected", err)
	}
	if _, err := ReadApprovedCandidatePlan(t.Context(), approvedTx, requestID, strings.Repeat("0", 64)); err == nil {
		approvedTx.Rollback()
		t.Fatal("changed plan digest accepted")
	}
	if err := approvedTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_executions(request_id,followup_task_id,approval_event_id,plan_digest,run_ref)
VALUES($1,$2,$3,$4,'run-synthetic-followup')`, requestID, taskID, approvalEventID, digest); err == nil {
		t.Fatal("original task reused as candidate execution")
	}
	followup, err := s.m.pg.CreateTask("synthetic candidate followup", "synthetic read only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE tasks SET paused=true WHERE id=$1`, followup.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_executions(request_id,followup_task_id,approval_event_id,plan_digest,run_ref)
VALUES($1,$2,$3,$4,'run-synthetic-followup')`, requestID, followup.ID, approvalEventID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,reason,sources_digest,rule_version,execution_ref)
VALUES($1,3,'running','execution_started',$2,'candidate-active/v1','run-synthetic-followup')`, requestID, emptySourcesDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE tasks SET paused=false WHERE id=$1`, followup.ID); err != nil {
		t.Fatal(err)
	}
	revoked := false
	guard, err := NewCandidateExecutionSupervisor(t.Context(), s.m.pg.DB, requestID, followup.ID, digest, func(_ context.Context, current CandidateActivePlan) error {
		if revoked || current.TargetID != plan.TargetID || !CandidatePlanStillApproved(time.Now(), current, input, scope, target) {
			return ErrCandidateExecutionBinding
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	guard.server = &Server{} // the test exercises only DB binding, without opening an E² host
	boundScope := transmit.Scope{TaskRef: fmt.Sprint(followup.ID), RunRef: "run-synthetic-followup", CaseRef: digest}
	if err := guard.check(t.Context(), boundScope, 0, false, true); err != nil {
		t.Fatalf("approved fresh task rejected: %v", err)
	}
	wrongScope := boundScope
	wrongScope.TaskRef = fmt.Sprint(taskID)
	if guard.check(t.Context(), wrongScope, 0, false, true) == nil {
		t.Fatal("original task reused as execution")
	}
	revoked = true
	if guard.check(t.Context(), boundScope, 0, false, true) == nil {
		t.Fatal("revoked target still authorized")
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE tasks SET paused=true WHERE id=$1`, followup.ID); err != nil {
		t.Fatal(err)
	}
	if guard.CheckTaskQuiescent(t.Context(), followup.ID) != nil {
		t.Fatal("revoked approval blocked safe closure")
	}
	var envelope struct {
		Trusted e2runner.TrustedResult `json:"trusted"`
	}
	if json.Unmarshal([]byte(candidate.Evidence), &envelope) != nil {
		t.Fatal("original evidence unavailable")
	}
	var calls []e2runner.TrustedResult
	for i := 1; i <= 2; i++ {
		call := envelope.Trusted
		call.Call = e2proxy.CloneCallResult(call.Call)
		call.Call.CallRef = fmt.Sprintf("followup-call-%d", i)
		call.Call.Observations[0].AttemptRef = fmt.Sprintf("followup-attempt-%d", i)
		call.Call.Observations[0].Sequence = uint64(i)
		call.Execution.Scope.TaskRef = fmt.Sprint(followup.ID)
		call.Execution.Scope.RunRef = "run-synthetic-followup"
		call.Final, call.ProcessDrained, call.ProxyDrained = true, true, true
		call.Call.Final, call.Call.AuditComplete, call.Execution.AuditComplete = true, true, true
		call.Classification = "completed"
		calls = append(calls, call)
	}
	observations, err := candidateverify.ProjectTrustedV2(followup.ID, "run-synthetic-followup", calls)
	if err != nil || len(observations) != 2 {
		t.Fatalf("synthetic audited observations: %d %v", len(observations), err)
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE tasks SET status='done',completed_at=clock_timestamp() WHERE id=$1 AND paused`, followup.ID); err != nil {
		t.Fatal(err)
	}
	seal := db.SealCheckRecord{TaskID: followup.ID, RunRef: "run-synthetic-followup", Head: db.ProductCheckpoint{JournalID: "journal-synthetic", Sequence: 2, MAC: strings.Repeat("a", 64)}, Checks: []db.SealCheck{{Sequence: 1, MAC: strings.Repeat("b", 64), Included: true}, {Sequence: 2, MAC: strings.Repeat("c", 64), Included: true}}}
	if err := s.m.pg.RecordSealCheck(t.Context(), seal); err != nil {
		t.Fatal(err)
	}
	result, err := CompleteCandidateActiveVerification(t.Context(), s.m.pg.DB, requestID, followup.ID, digest, "run-synthetic-followup", observations)
	if err != nil || result.Status != "confirmed" {
		t.Fatalf("durable active verdict: %+v %v", result, err)
	}
	for _, suffix := range []string{"", "&format=docx"} {
		got := reportRequest(http.MethodGet, fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal%s", taskID, suffix), "")
		if got.Code != 200 {
			t.Fatalf("latest report %s: %d", suffix, got.Code)
		}
		content := got.Body.String()
		if suffix != "" {
			content = diagnosticWordText(t, wordParts(t, got.Body.Bytes())["word/document.xml"])
		}
		if !strings.Contains(content, "최근 선택 검증") || !strings.Contains(content, "followup-attempt-2") || !strings.Contains(content, "최초 자동 검증") {
			t.Fatalf("latest report omitted active evidence: %s", suffix)
		}
	}
	// A later check of the unchanged original observations cannot erase a
	// completed, independently sealed active verification from the report.
	beforeStored, err := loadDiagnosticReport(t.Context(), s.m.pg, taskID, time.Now())
	if err != nil || beforeStored.Run == nil || len(beforeStored.Run.CandidateAssessments) != 1 {
		t.Fatal("saved automatic assessment unavailable", err)
	}
	storedTx, err := s.m.pg.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := createCandidateVerification(t.Context(), storedTx, candidateVerificationRoute{task: taskID, candidate: candidate.ID}, candidate, "ARTEX", "synthetic-stored-after-active", beforeStored.Run.CandidateAssessments[0]); err != nil {
		storedTx.Rollback()
		t.Fatal(err)
	}
	if err := storedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDiagnosticReport(t.Context(), s.m.pg, taskID, time.Now()); err != nil {
		t.Fatalf("stored review report assembly: %T %v", err, err)
	}
	for _, suffix := range []string{"", "&format=docx"} {
		got := reportRequest(http.MethodGet, fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal%s", taskID, suffix), "")
		if got.Code != 200 {
			t.Fatalf("stored review after active %s: %d %s", suffix, got.Code, got.Body.String())
		}
		content := got.Body.String()
		if suffix != "" {
			content = diagnosticWordText(t, wordParts(t, got.Body.Bytes())["word/document.xml"])
		}
		if !strings.Contains(content, "이전 유효 추가 검증") || !strings.Contains(content, "followup-attempt-2") || !strings.Contains(content, "confirmed") {
			t.Fatalf("stored recheck erased sealed active verdict: %s", suffix)
		}
	}
	if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE e3_seal_checks SET checks='[]'::jsonb WHERE task_id=$1 AND run_ref=$2`, followup.ID, "run-synthetic-followup"); err == nil {
		t.Fatal("immutable active audit seal was changed")
	}
	if got := reportRequest(http.MethodGet, fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal", taskID), ""); got.Code != 200 {
		t.Fatal("rejected tampering damaged the saved report")
	}
}
