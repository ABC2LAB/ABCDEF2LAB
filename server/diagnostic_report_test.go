package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e3record"
	"github.com/Autumn-27/artex/evidencebind"
	"github.com/Autumn-27/artex/idor"
	"github.com/Autumn-27/artex/oracle"
	"github.com/Autumn-27/artex/taskmode"
	"github.com/Autumn-27/artex/transmit"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/report"
)

func diagnosticServerCandidate(t *testing.T, s *Server, taskID int64, classification string, observations ...e2proxy.HTTPObservation) int64 {
	t.Helper()
	return diagnosticServerCandidateWithClass(t, s, taskID, classification, "XSS", observations...)
}

func diagnosticServerCandidateWithClass(t *testing.T, s *Server, taskID int64, classification, class string, observations ...e2proxy.HTTPObservation) int64 {
	t.Helper()
	return diagnosticServerRunCandidate(t, s, taskID, "synthetic-report-run", classification, class, observations...)
}

func diagnosticServerRunCandidate(t *testing.T, s *Server, taskID int64, run, classification, class string, observations ...e2proxy.HTTPObservation) int64 {
	t.Helper()
	task := s.m.ResolveTask(strconv.FormatInt(taskID, 10))
	intent, err := task.Store.AddIntent(map[string]any{"description": "synthetic report observation"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	evidence := map[string]any{"tool_output": "synthetic observed response <script>not executable</script>", "command": "synthetic fixture input", "trusted": map[string]any{
		"classification": classification, "execution": map[string]any{"scope": map[string]any{"TaskRef": fmt.Sprint(taskID), "RunRef": run}, "intent_id": intent},
		"call": map[string]any{"call_ref": fmt.Sprintf("call-%d", intent), "intent_id": intent, "observations": observations}, "output_truncated": false}}
	raw, _ := json.Marshal(evidence)
	c := db.DiagnosticCandidate{TaskID: taskID, IntentID: intent, RunRef: run, CallRef: fmt.Sprintf("call-%d", intent), VulnClass: class, Name: "합성 " + class + " 후보", Summary: "관측된 응답을 사람이 검토해야 합니다.", Remediation: "출력 문맥별 인코딩 적용 여부를 확인하세요.", Classification: classification, Verification: "manual_review_required", EvidenceVersion: 1, EvidenceDigest: db.DiagnosticEvidenceDigest(string(raw)), ObservedAt: time.Now().UTC(), Evidence: string(raw)}
	id, err := task.Store.RecordDiagnosticCandidate(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDiagnosticReportBusinessRuleReview(t *testing.T) {
	s, f, request := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal("저장된 작업 조회 실패", err)
	}
	task := *stored.TaskID
	observation := e2proxy.HTTPObservation{
		Sequence: 1, AttemptRef: "synthetic-attempt", Method: "GET", Endpoint: "/sensitive-fixture-path",
		Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true,
		AuditComplete: true, ObservedAt: time.Now().UTC(),
	}
	policyCandidate := diagnosticServerCandidateWithClass(t, s, task, "completed", "IDOR", observation)
	diagnosticServerCandidate(t, s, task, "completed", observation)
	diagnosticServerCandidateWithClass(t, s, task, "policy_not_executed", "IDOR")
	observation.StatusCode = 403
	diagnosticServerCandidateWithClass(t, s, task, "completed", "IDOR", observation)

	path := fmt.Sprintf("/api/report/diagnostic?task=%d", task)
	query := "&format=review-json&audience=internal"
	response := request("GET", path+query, "")
	if response.Code != 200 {
		t.Fatalf("검토 조회 상태 %d", response.Code)
	}
	var decoded struct {
		TaskID  string `json:"task_id"`
		Entries []struct {
			ID, Status, Reason, Observation, Question string
		} `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TaskID != strconv.FormatInt(task, 10) || len(decoded.Entries) != 1 {
		t.Fatalf("해당 항목만 검토 보류돼야 함: %+v", decoded)
	}
	entry := decoded.Entries[0]
	if entry.ID != fmt.Sprintf("candidate-%d", policyCandidate) || entry.Status != "deferred" || entry.Reason != "business_rule_unverified" ||
		entry.Observation != "재현 단계 1 (GET, HTTP 200)" || entry.Question == "" {
		t.Fatalf("관찰 및 보류 정보 누락: %+v", entry)
	}
	for _, raw := range []string{"sensitive-fixture-path", "synthetic observed response", "synthetic fixture input"} {
		if strings.Contains(response.Body.String(), raw) {
			t.Fatal("검토 응답에 원문이 포함됨")
		}
	}
	internal := request("GET", path+"&audience=internal", "")
	if internal.Code != 200 || !strings.Contains(internal.Body.String(), entry.Question) || !strings.Contains(internal.Body.String(), "판정 보류") {
		t.Fatal("내부 보고서에 검토 정보가 없음")
	}
	word := request("GET", path+"&audience=internal&format=docx", "")
	if word.Code != 200 || !strings.Contains(diagnosticWordText(t, wordParts(t, word.Body.Bytes())["word/document.xml"]), entry.Question) {
		t.Fatal("내부 DOCX에 검토 정보가 없음")
	}
	shared := request("GET", path, "")
	if shared.Code != 200 || strings.Contains(shared.Body.String(), entry.Question) {
		t.Fatal("공유 보고서에 내부 검토 정보가 포함됨")
	}
	if got := request("GET", path+"&format=review-json", ""); got.Code != 400 {
		t.Fatalf("내부 보고서 범위 지정 없이 조회됨: %d", got.Code)
	}
	anonymous := httptest.NewRecorder()
	s.Handler().ServeHTTP(anonymous, httptest.NewRequest("GET", path+query, nil))
	if anonymous.Code != 401 {
		t.Fatalf("인증 없이 검토 조회됨: %d", anonymous.Code)
	}
}
func TestDiagnosticReportDatabaseScopeAndNoFalseConfirmation(t *testing.T) {
	s, f, req := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	tid := *stored.TaskID
	if _, err = s.m.pg.Exec(`UPDATE findings SET name='legacy claimed finding',status='confirmed',evidence_version=1 WHERE id=$1`, f.FindingID); err != nil {
		t.Fatal(err)
	}
	diagnosticServerCandidate(t, s, tid, "completed")
	diagnosticServerCandidate(t, s, tid, "policy_not_executed")
	data, err := loadDiagnosticReport(context.Background(), s.m.pg, tid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Entries) != 3 {
		t.Fatalf("entries=%d", len(data.Entries))
	}
	for _, e := range data.Entries {
		if e.Outcome == "confirmed" {
			t.Fatal("unverified state promoted")
		}
	}
	path := fmt.Sprintf("/api/report/diagnostic?task=%d", tid)
	shared := req("GET", path, "")
	if shared.Code != 200 || strings.Contains(shared.Body.String(), "합성 XSS 후보") {
		t.Fatalf("shared export=%d", shared.Code)
	}
	internal := req("GET", path+"&audience=internal&download=1", "")
	if internal.Code != 200 || !strings.Contains(internal.Body.String(), "합성 XSS 후보") || !strings.Contains(internal.Body.String(), "<td>2</td><td>0</td><td>0</td><td>0</td><td>1</td>") {
		t.Fatalf("internal export=%d body=%s", internal.Code, internal.Body.String())
	}
	if internal.Header().Get("Content-Disposition") == "" || internal.Header().Get("Content-Security-Policy") == "" || strings.Contains(internal.Body.String(), "<script>not executable") {
		t.Fatal("unsafe download")
	}
	for _, bad := range []string{"/api/report/diagnostic", "/api/report/diagnostic?task=0", path + "&run=other", path + "&audience=public"} {
		if req("GET", bad, "").Code != 400 {
			t.Fatalf("scope accepted: %s", bad)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 401 {
		t.Fatal("anonymous report access")
	}
	other, err := s.m.CreateTask("other synthetic task", "other", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := strconv.ParseInt(other.ID, 10, 64)
	t.Cleanup(func() { s.m.pg.DeleteTask(otherID) })
	otherData, err := loadDiagnosticReport(context.Background(), s.m.pg, otherID, time.Now())
	if err != nil || len(otherData.Entries) != 0 {
		t.Fatal("cross-task result leakage", err)
	}
}
func TestDiagnosticReportDatabaseCorruptionAndPageOverflow(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	tid := *stored.TaskID
	id := diagnosticServerCandidate(t, s, tid, "completed")
	if _, err = s.m.pg.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{diagnostic_candidate_v1,evidence_digest}','"wrong"') WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	invalidData, err := loadDiagnosticReport(context.Background(), s.m.pg, tid, time.Now())
	if err != nil || invalidData.SourceFailures != 1 {
		t.Fatal("invalid candidate not isolated", err)
	}
	for _, entry := range invalidData.Entries {
		if entry.ID == fmt.Sprintf("candidate-%d", id) && (!entry.SourceLoadFailed || entry.Outcome != "unknown" || entry.Digest != "" || len(entry.Steps) != 0) {
			t.Fatal("corrupt candidate trusted")
		}
	}
	if _, err = s.m.pg.Exec(`DELETE FROM exploration_nodes WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.m.pg.Exec(`INSERT INTO findings(task_id,name,severity,status) SELECT $1,'page fixture','low','pending' FROM generate_series(1,151)`, tid); err != nil {
		t.Fatal(err)
	}
	data, err := loadDiagnosticReport(context.Background(), s.m.pg, tid, time.Now())
	if err != nil || len(data.Entries) != 152 {
		t.Fatalf("page incomplete n=%d err=%v", len(data.Entries), err)
	}
	if _, err = s.m.pg.Exec(`INSERT INTO findings(task_id,name,severity,status) SELECT $1,'overflow fixture','low','pending' FROM generate_series(1,$2)`, tid, report.MaxFindings); err != nil {
		t.Fatal(err)
	}
	if _, err = loadDiagnosticReport(context.Background(), s.m.pg, tid, time.Now()); err == nil {
		t.Fatal("overflow silently truncated")
	}
}

type diagnosticE3Fixture struct {
	prepared                        e3record.Prepared
	input                           e3record.Input
	source                          e3record.Source
	markers                         db.TaskModeMarkerReader
	taskID, intentID, explorationID int64
}

func diagnosticE3FixtureFor(t *testing.T, d *db.DB, value oracle.VerdictValue, mode bool) diagnosticE3Fixture {
	t.Helper()
	task, err := d.CreateTask("synthetic E3 initial storage", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := d.Exploration(task.ExplorationID).AddIntent(map[string]any{"description": "synthetic owner/cross"}, 5, nil, "e3test")
	if err != nil {
		t.Fatal(err)
	}
	markers := func(_ context.Context, id int64) (taskmode.Marker, error) {
		return taskmode.Marker{TaskID: id, State: taskmode.MarkerPresent}, nil
	}
	version := uint64(1)
	if mode {
		record, err := d.RegisterTaskMode(t.Context(), task.ID, taskmode.ModeControlled, markers)
		if err != nil {
			t.Fatal(err)
		}
		version = record.Version
	}
	at := time.Date(2026, 11, 3, 12, 0, 20, 0, time.UTC)
	c := evidencebind.Context{Scope: transmit.Scope{RunRef: fmt.Sprintf("synthetic-e3-%d", task.ID), TaskRef: strconv.FormatInt(task.ID, 10),
		CaseRef: "synthetic-case", ResetRef: "synthetic-reset", PolicyRef: "synthetic-policy"},
		IntentID: intent, ResourceRef: "synthetic:document:one", ReadApprovalRef: "synthetic-read", KGPolicyVersion: "synthetic-kg-v1",
		Owner:     idor.AccountRef{Label: "owner", Handle: "session-a", Status: oracle.SubjectVerified},
		Evaluated: idor.AccountRef{Label: "reader", Handle: "session-b", Status: oracle.SubjectVerified}}
	policy := oracle.PolicyAssertion{ExpectedAccess: oracle.AccessDeny, Sharing: oracle.ResolvedNone, Delegation: oracle.ResolvedNone,
		HasRegisteredSource: true, HintMatchesCanonical: true, ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}
	facts := oracle.ComparisonFacts{SameResource: oracle.StateYes, OwnerNormalData: oracle.StateYes, CrossNormalData: oracle.StateYes,
		NontrivialDataMatch: oracle.StateYes, ReflectionExcluded: oracle.StateYes, TemplateExcluded: oracle.StateYes}
	switch value {
	case oracle.Allowed:
		policy.ExpectedAccess = oracle.AccessAllow
	case oracle.Suspected:
		policy.ExpectedAccess = oracle.AccessUnknown
	case oracle.Undecidable:
		facts.CrossNormalData = oracle.StateUnknown
	}
	receipt := func(a idor.AccountRef, n uint64, admitted, done time.Time) transmit.Receipt {
		return transmit.Receipt{
			Scope: c.Scope, Attempt: n, AccountRef: a.Label, SessionRef: a.Handle, ResourceRef: c.ResourceRef, ApprovalRef: c.ReadApprovalRef,
			Method: "GET", Reason: transmit.Completed, Admitted: true, AdmittedAt: admitted, RequestWritten: true, ResponseReceived: true, StatusCode: 200, At: done}
	}
	owner := evidencebind.Observation{Context: c, Receipt: receipt(c.Owner, 1, at.Add(-10*time.Second), at.Add(-8*time.Second)),
		Evidence: evidencebind.EvidenceRef{ID: "owner-capture", IntentID: intent, Scope: c.Scope, Attempt: 1, Role: evidencebind.OwnerRead}, ObservedAt: at.Add(-9 * time.Second)}
	cross := evidencebind.Observation{Context: c, Receipt: receipt(c.Evaluated, 2, at.Add(-5*time.Second), at.Add(time.Second)),
		Evidence: evidencebind.EvidenceRef{ID: "cross-capture", IntentID: intent, Scope: c.Scope, Attempt: 2, Role: evidencebind.CrossRead}, ObservedAt: at}
	result := idor.Result{Facts: facts, Verdict: oracle.Evaluate(oracle.CaseInput{Policy: policy, Facts: facts,
		Subjects: oracle.SubjectContext{OwnerStatus: c.Owner.Status, EvaluatedStatus: c.Evaluated.Status, OwnerRef: c.Owner.Label, EvaluatedRef: c.Evaluated.Label}}, at)}
	input := e3record.Input{Expected: c, CanaryFields: []string{"note"}, Evidence: evidencebind.Input{
		Policy: evidencebind.PolicySnapshot{Context: c, Assertion: policy}, Owner: owner, Cross: cross,
		Result: evidencebind.ResultSnapshot{Context: c, OwnerEvidence: owner.Evidence, CrossEvidence: cross.Evidence, ObservedAt: at, Result: result}}}
	source := e3record.Source{Expected: c, ExplorationID: task.ExplorationID, ModeVersion: version, EvidenceRevision: 1,
		KGSnapshotRef: "synthetic-kg-snapshot", KGSnapshotDigest: sha256.Sum256([]byte("synthetic KG")),
		GenerationRef: "synthetic-fixture-generation", InstanceRef: "synthetic-target", ModelTag: "synthetic-model",
		ResetGeneration: "synthetic-reset-generation", Policy: policy, CanaryFields: []string{"note"}, Severity: "high",
		FinalCheckpointRef: "synthetic-checkpoint", FinalCheckpointDigest: sha256.Sum256([]byte("synthetic final checkpoint")),
		FinalCheckpointScope: c.Scope, FinalCheckpointSequence: 4, RecordCreatedAt: at.Add(10 * time.Second), Completed: true, Finalized: true}
	source.EvidenceDigest, err = e3record.EvidenceDigest(input.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e3record.Prepare(t.Context(), input, func(context.Context, evidencebind.Context) (e3record.Source, error) { return source, nil })
	if err != nil {
		t.Fatal("synthetic prepared input failed", err)
	}
	return diagnosticE3Fixture{prepared, input, source, markers, task.ID, intent, task.ExplorationID}
}

func TestDiagnosticReportActualE3VerdictsAndStale(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	for _, value := range []oracle.VerdictValue{oracle.Confirmed, oracle.Allowed, oracle.Undecidable, oracle.Suspected} {
		t.Run(string(value), func(t *testing.T) {
			f := diagnosticE3FixtureFor(t, s.m.pg, value, true)
			result, err := s.m.pg.RecordControlledFinding(t.Context(), f.prepared, f.markers)
			if err != nil {
				t.Fatal(err)
			}
			data, err := loadDiagnosticReport(t.Context(), s.m.pg, f.taskID, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(data.Entries) != 1 {
				t.Fatalf("entries=%d", len(data.Entries))
			}
			expected := "review"
			switch value {
			case oracle.Confirmed:
				expected = "confirmed"
			case oracle.Allowed:
				expected = "negative"
			case oracle.Undecidable:
				expected = "unknown"
			}
			if data.Entries[0].Outcome != expected {
				t.Fatalf("outcome=%s expected=%s", data.Entries[0].Outcome, expected)
			}
			if value == oracle.Confirmed {
				if data.Entries[0].Status != "pending" {
					t.Fatal("new finding triage status changed")
				}
				if _, err = s.m.pg.Exec(`UPDATE findings SET status='fixed' WHERE id=$1`, result.FindingID); err != nil {
					t.Fatal(err)
				}
				fixed, err := loadDiagnosticReport(t.Context(), s.m.pg, f.taskID, time.Now())
				if err != nil || fixed.Entries[0].Outcome != "confirmed" {
					t.Fatal("fix state rewrote initial verdict", err)
				}
				if _, err = s.m.pg.Exec(`UPDATE findings SET evidence_version=2,report_evidence_version=1,report='stale synthetic report' WHERE id=$1`, result.FindingID); err != nil {
					t.Fatal(err)
				}
				stale, err := loadDiagnosticReport(t.Context(), s.m.pg, f.taskID, time.Now())
				if err != nil || stale.Entries[0].Outcome != "review" || !stale.Entries[0].Stale {
					t.Fatal("stale evidence shown confirmed", err)
				}
			}
		})
	}
}
func TestDiagnosticReportSnapshotAndByteLimit(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	tid := *stored.TaskID
	tx, err := s.m.pg.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var pinnedName string
	if err = tx.QueryRowContext(t.Context(), `SELECT name FROM findings WHERE id=$1`, f.FindingID).Scan(&pinnedName); err != nil {
		t.Fatal(err)
	}
	if _, err = s.m.pg.Exec(`UPDATE findings SET name='new concurrent name',evidence_version=evidence_version+1 WHERE id=$1`, f.FindingID); err != nil {
		t.Fatal(err)
	}
	old, err := readDiagnosticReport(t.Context(), tx, tid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if old.Entries[0].Title != pinnedName || old.Entries[0].EvidenceVersion != stored.EvidenceVersion {
		t.Fatal("mixed snapshot")
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	fresh, err := loadDiagnosticReport(t.Context(), s.m.pg, tid, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Entries[0].Title != "new concurrent name" || fresh.Entries[0].EvidenceVersion != stored.EvidenceVersion+1 {
		t.Fatal("new snapshot missing change")
	}
	if _, err = s.m.pg.Exec(`UPDATE findings SET report=repeat('x',$2) WHERE id=$1`, f.FindingID, report.MaxDiagnosticReportBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err = loadDiagnosticReport(t.Context(), s.m.pg, tid, time.Now()); err == nil {
		t.Fatal("oversized TEXT accepted")
	}
}

func TestDiagnosticReportUnknownClassificationsRemainUnknown(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	for _, class := range []string{"result_unknown", "tool_failure", "transient_failure", "terminated"} {
		diagnosticServerCandidate(t, s, *stored.TaskID, class)
	}
	data, err := loadDiagnosticReport(t.Context(), s.m.pg, *stored.TaskID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range data.Entries {
		if entry.Kind == "candidate" {
			count++
			if entry.Outcome != "unknown" {
				t.Fatalf("unknown classification downgraded: %s %s", entry.Status, entry.Outcome)
			}
		}
	}
	if count != 4 {
		t.Fatalf("unknown results=%d", count)
	}
}
func TestDiagnosticReportE3DigestsAndConfirmationGuardsIndependently(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	for _, scenario := range []string{"finding-manifest", "finding-verdict", "unlinked-manifest", "unlinked-verdict", "initial-version", "report-stale"} {
		t.Run(scenario, func(t *testing.T) {
			verdict := oracle.Confirmed
			if strings.HasPrefix(scenario, "unlinked") {
				verdict = oracle.Undecidable
			}
			f := diagnosticE3FixtureFor(t, s.m.pg, verdict, true)
			result, err := s.m.pg.RecordControlledFinding(t.Context(), f.prepared, f.markers)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := loadDiagnosticReport(t.Context(), s.m.pg, f.taskID, time.Now())
			if err != nil || len(baseline.Entries) != 1 {
				t.Fatal("baseline invalid", err)
			}
			tx, err := s.m.pg.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// 소유 시험 연결에서만 변조된 저장소 응답을 재현하고 반드시 롤백한다.
			// 제품의 불변 트리거나 다른 연결의 집행 설정은 바꾸지 않는다.
			if _, err = tx.ExecContext(t.Context(), `SET LOCAL session_replication_role=replica`); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "initial-version":
				_, err = tx.Exec(`UPDATE findings SET evidence_version=evidence_version+1,report='' WHERE id=$1`, result.FindingID)
			case "report-stale":
				_, err = tx.Exec(`UPDATE findings SET report='synthetic stale',report_evidence_version=evidence_version+1 WHERE id=$1`, result.FindingID)
			default:
				// 만지는 행을 task로 한정해 다른 시험의 근거는 보존한다.
				if strings.HasSuffix(scenario, "manifest") {
					_, err = tx.Exec(`UPDATE e3_evidence_manifests SET digest=decode(repeat('00',32),'hex') WHERE task_id=$1`, f.taskID)
				} else {
					_, err = tx.Exec(`UPDATE e3_oracle_verdicts SET payload_digest=decode(repeat('00',32),'hex') WHERE id IN (SELECT verdict_id FROM e3_verdict_issuances WHERE task_id=$1)`, f.taskID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := readDiagnosticReport(t.Context(), tx, f.taskID, time.Now())
			if scenario == "initial-version" || scenario == "report-stale" {
				if err != nil || data.Entries[0].Outcome != "review" || data.Entries[0].Stale != (scenario == "report-stale") {
					t.Fatal("independent confirmation guard failed", err)
				}
			} else if err == nil {
				t.Fatal("corrupt E3 digest accepted")
			}
		})
	}
}

func TestDiagnosticWorkflowApprovalCountIsScoped(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	ctx := t.Context()
	tx, err := s.m.pg.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, query := range []string{
		`CREATE TEMP TABLE diagnosis_workflows (job_id text, task_id bigint, owner_ref text, record jsonb)`,
		`CREATE TEMP TABLE request_approvals (id bigint, task_id bigint, owner_ref text, record jsonb)`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	flow := diagnosisflow.Record{Owner: "ARTEX", Snapshot: diagnosisflow.Snapshot{Format: diagnosisflow.Version, TaskID: "7", RunRef: "owned"}}
	raw, err := json.Marshal(flow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO diagnosis_workflows VALUES ('test',7,'ARTEX',$1::jsonb)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO request_approvals
SELECT n, 7, 'ARTEX', jsonb_build_object('run_ref', 'owned', 'attempt_id', n::text)
FROM generate_series(1, $1::int) AS n
UNION ALL
SELECT id, task, owner, jsonb_build_object('run_ref', run, 'attempt_id', 'foreign')
FROM (VALUES (-1, 8, 'ARTEX', 'owned'), (-2, 7, 'OTHER', 'owned'), (-3, 7, 'ARTEX', 'other')) AS x(id, task, owner, run)`, report.MaxWorkflowApprovals+1); err != nil {
		t.Fatal(err)
	}
	var out report.DiagnosticReport
	if err := readWorkflowReport(ctx, tx, 7, &out); err != nil {
		t.Fatal(err)
	}
	if out.WorkflowApprovalTotal != report.MaxWorkflowApprovals+1 || len(out.WorkflowApprovals) != report.MaxWorkflowApprovals {
		t.Fatalf("approval total=%d rows=%d", out.WorkflowApprovalTotal, len(out.WorkflowApprovals))
	}
	if out.WorkflowApprovals[0].Attempt != "1" || out.WorkflowApprovals[len(out.WorkflowApprovals)-1].Attempt != strconv.Itoa(report.MaxWorkflowApprovals) {
		t.Fatal("wrong approval scope or order")
	}
}

// 저장된 흐름 기록의 엔드포인트가 많아도(#898 회차 2: 58개 → 보고서 422, #908) 보고서 조회 API가 내부용·공유용 HTML과
// Word, 검토 목록(review-json)을 모두 200으로 내보내고 행 수가 맞는다. report 패키지 고정 시험
// (TestWorkflowReportEndpointLimits)이 보는 생성 단계에 더해 DB 저장 기록 → 조회 API 구간을 고정한다. 보고서 경로는
// 합성 회차가 보고서를 내보내지 않아 이 고정 시험으로 확인한다(CONTRIBUTING.md '커밋과 PR' 8).
func TestDiagnosticWorkflowReportManyStoredEndpoints(t *testing.T) {
	const endpoints = 58
	s, f, request := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal("저장된 작업 조회 실패", err)
	}
	task := *stored.TaskID
	ctx := t.Context()
	if err := diagnosisflow.Migrate(ctx, s.m.pg.DB); err != nil {
		t.Fatal(err)
	}
	jobID := fmt.Sprintf("many-endpoints-%d", task)
	// 작업 삭제(trafficEvidenceServer 정리)보다 먼저 흐름 기록을 지운다. 정리는 등록 역순으로 돈다.
	t.Cleanup(func() {
		for _, query := range []string{`DELETE FROM diagnosis_workflow_events WHERE job_id=$1`, `DELETE FROM diagnosis_workflows WHERE job_id=$1`} {
			if _, err := s.m.pg.Exec(query, jobID); err != nil {
				t.Logf("흐름 기록 정리 실패: %v", err)
			}
		}
	})
	snapshot := diagnosisflow.Snapshot{
		Format: diagnosisflow.Version, JobID: jobID, TaskID: strconv.FormatInt(task, 10), Revision: 1,
		RunRef: "many-endpoints", Mode: diagnosisflow.Manual, Stage: "completed",
	}
	for i := 1; i <= endpoints; i++ {
		snapshot.Endpoints = append(snapshot.Endpoints, diagnosisflow.Endpoint{Method: "GET", Path: fmt.Sprintf("/stored-endpoint-%03d", i)})
	}
	if err := (diagnosisflow.Postgres{DB: s.m.pg.DB}).Create(ctx, diagnosisflow.Record{Owner: "ARTEX", Snapshot: snapshot}); err != nil {
		t.Fatal(err)
	}
	// 검토 목록에는 업무 규칙 미확인으로 보류된 IDOR 후보 하나만 실린다(TestDiagnosticReportBusinessRuleReview와 같은 조건).
	observation := e2proxy.HTTPObservation{
		Sequence: 1, AttemptRef: "synthetic-attempt", Method: "GET", Endpoint: "/sensitive-fixture-path",
		Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true,
		AuditComplete: true, ObservedAt: time.Now().UTC(),
	}
	deferred := diagnosticServerCandidateWithClass(t, s, task, "completed", "IDOR", observation)
	diagnosticServerCandidate(t, s, task, "completed", observation)

	path := fmt.Sprintf("/api/report/diagnostic?task=%d", task)
	for _, c := range []struct {
		query, row string
		shared     bool
	}{
		{"&audience=internal", "/stored-endpoint-", false},
		{"&audience=internal&format=docx", "/stored-endpoint-", false},
		{"", "[공유용 경로 비공개]", true},
		{"&format=docx", "[공유용 경로 비공개]", true},
	} {
		response := request("GET", path+c.query, "")
		if response.Code != 200 {
			t.Fatalf("%q 상태 %d", c.query, response.Code)
		}
		body := response.Body.String()
		if strings.Contains(c.query, "docx") {
			body = string(wordParts(t, response.Body.Bytes())["word/document.xml"])
		}
		if got := strings.Count(body, c.row); got != endpoints {
			t.Errorf("%q 엔드포인트 행 %d, 기대 %d", c.query, got, endpoints)
		}
		if c.shared && strings.Contains(body, "/stored-endpoint-") {
			t.Errorf("%q 공유용에 저장 경로가 보임", c.query)
		}
	}

	response := request("GET", path+"&format=review-json&audience=internal", "")
	if response.Code != 200 {
		t.Fatalf("검토 목록 상태 %d", response.Code)
	}
	var decoded struct {
		TaskID  string `json:"task_id"`
		Entries []struct {
			ID, Status, Reason string
		} `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TaskID != strconv.FormatInt(task, 10) || len(decoded.Entries) != 1 ||
		decoded.Entries[0].ID != fmt.Sprintf("candidate-%d", deferred) || decoded.Entries[0].Reason != "business_rule_unverified" {
		t.Fatalf("검토 목록 행이 맞지 않음: %+v", decoded)
	}
}
