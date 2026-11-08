package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
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
	"github.com/Autumn-27/artex/report"
)

func diagnosticServerCandidate(t *testing.T, s *Server, taskID int64, classification string) int64 {
	t.Helper()
	task := s.m.ResolveTask(strconv.FormatInt(taskID, 10))
	intent, err := task.Store.AddIntent(map[string]any{"description": "synthetic report observation"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	evidence := map[string]any{"tool_output": "synthetic observed response <script>not executable</script>", "command": "synthetic fixture input", "trusted": map[string]any{
		"classification": classification, "execution": map[string]any{"scope": map[string]any{"TaskRef": fmt.Sprint(taskID), "RunRef": "synthetic-report-run"}, "intent_id": intent},
		"call": map[string]any{"call_ref": fmt.Sprintf("call-%d", intent), "intent_id": intent}, "output_truncated": false}}
	raw, _ := json.Marshal(evidence)
	c := db.DiagnosticCandidate{TaskID: taskID, IntentID: intent, RunRef: "synthetic-report-run", CallRef: fmt.Sprintf("call-%d", intent), VulnClass: "XSS", Name: "합성 XSS 후보", Summary: "관측된 응답을 사람이 검토해야 합니다.", Remediation: "출력 문맥별 인코딩 적용 여부를 확인하세요.", Classification: classification, Verification: "manual_review_required", EvidenceVersion: 1, EvidenceDigest: db.DiagnosticEvidenceDigest(string(raw)), ObservedAt: time.Now().UTC(), Evidence: string(raw)}
	id, err := task.Store.RecordDiagnosticCandidate(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return id
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
	if internal.Code != 200 || !strings.Contains(internal.Body.String(), "합성 XSS 후보") || !strings.Contains(internal.Body.String(), "<td>0</td><td>2</td><td>1</td>") {
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
	if _, err = loadDiagnosticReport(context.Background(), s.m.pg, tid, time.Now()); err == nil {
		t.Fatal("corrupt evidence accepted")
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
