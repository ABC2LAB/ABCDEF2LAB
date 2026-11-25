package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

type verificationOwnerEndpoint struct {
	task   string
	calls  atomic.Int64
	scope  *diagnosisprep.Scope
	target *diagnosisprep.ProductTarget
}

func (e *verificationOwnerEndpoint) Call(_ context.Context, req diagnosisprep.Request) (diagnosisprep.Response, error) {
	e.calls.Add(1)
	if req.Command == diagnosisprep.ProductControl && req.Principal == "ARTEX" && req.Product != nil && (req.Product.Operation == "candidate_analyze" || req.Product.Operation == "candidate_llm_execute") {
		return diagnosisprep.Response{Version: req.Version, RequestID: req.RequestID, Product: &diagnosisprep.ProductResponse{Reason: "scheduled"}}, nil
	}
	if req.Command == diagnosisprep.ProductControl && req.Principal == "ARTEX" && req.Product != nil && req.Product.Operation == "target" && e.target != nil && req.Product.TargetID == e.target.ID {
		return diagnosisprep.Response{Version: req.Version, RequestID: req.RequestID, Product: &diagnosisprep.ProductResponse{Target: e.target}}, nil
	}
	allowed := req.Command == diagnosisprep.AuthorizeTask && req.Principal == "ARTEX" && req.TaskID == e.task
	out := diagnosisprep.Response{Version: req.Version, RequestID: req.RequestID, Authorized: &allowed}
	if allowed {
		out.Job = &diagnosisprep.Job{ID: "synthetic-owned-job", Revision: 1, SourceDigest: strings.Repeat("a", 64),
			Input: diagnosisprep.Input{TargetURL: "https://owned.example/", ProfileID: "local"},
			State: diagnosisprep.Unknown, Phase: diagnosisprep.Planning, TaskID: req.TaskID,
			PreparationStarted: true, AuthorityState: diagnosisprep.AuthorityNotIssued}
		out.Job.Scope = e.scope
	}
	return out, nil
}

func TestCandidateVerificationStoredOnlyProductAPIPostgres(t *testing.T) {
	for _, tc := range []struct {
		name, claim, first, second, state, verdict string
	}{
		{"stored-confirmed", "html_nosniff_missing", "nosniff_missing", "nosniff_missing", "completed", "confirmed"},
		{"mixed-context", "html_nosniff_missing", "nosniff_missing", "nosniff_missing", "completed", "confirmed"},
		{"stored-negative", "html_nosniff_missing", "nosniff_present", "nosniff_present", "completed", "negative"},
		{"needs-new-evidence", "html_nosniff_missing", "nosniff_missing", "", "completed", "insufficient"},
		{"duplicate-attempt", "html_nosniff_missing", "nosniff_missing", "nosniff_missing", "completed", "insufficient"},
		{"unsupported", "", "nosniff_missing", "", "completed", "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, reportRequest := trafficEvidenceServer(t)
			stored, err := s.m.pg.GetFinding(f.FindingID)
			if err != nil || stored.TaskID == nil {
				t.Fatal(err)
			}
			taskID := *stored.TaskID
			if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
				t.Fatal(err)
			}
			candidateID := storePassiveCandidate(t, s, taskID, tc.claim, tc.first, "")
			rows, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
			if err != nil || len(rows) != 1 {
				t.Fatal(err)
			}
			var envelope struct {
				Trusted e2runner.TrustedResult `json:"trusted"`
			}
			if err := json.Unmarshal([]byte(rows[0].Evidence), &envelope); err != nil {
				t.Fatal(err)
			}
			primary := envelope.Trusted
			primary.ProcessDrained, primary.ProxyDrained, primary.Execution.AuditComplete = true, true, true
			calls := []e2runner.TrustedResult{primary}
			if tc.second != "" {
				other := primary
				other.Call = e2proxy.CloneCallResult(primary.Call)
				other.Call.CallRef = "candidate-history-second-call"
				other.Call.Observations[0].AttemptRef = "candidate-history-second-attempt"
				if tc.name == "duplicate-attempt" {
					other.Call.Observations[0].AttemptRef = primary.Call.Observations[0].AttemptRef
				}
				other.Call.Observations[0].Sequence = 2
				other.Call.Observations[0].HTMLSecurity = tc.second
				calls = append(calls, other)
			}
			if tc.name == "mixed-context" {
				unrelated := primary
				unrelated.Call = e2proxy.CloneCallResult(primary.Call)
				unrelated.Call.CallRef = "another-call"
				unrelated.Call.Observations[0].AttemptRef = "another-attempt"
				unrelated.Call.Observations[0].Sequence = 3
				unrelated.Call.Observations[0].Endpoint = "/another"
				unrelated.Execution.HeaderMode = e2proxy.HeaderTestAccountPassthrough
				calls = append(calls, unrelated)
			}
			state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
			if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil, calls); err != nil {
				t.Fatal(err)
			}
			owner := &verificationOwnerEndpoint{task: fmt.Sprint(taskID)}
			auth := func(*http.Request) (string, error) { return "ARTEX", nil }
			path := fmt.Sprintf("/api/product/tasks/%d/candidates/%d/verifications", taskID, candidateID)
			invoke := func(pool *sql.DB, method, uri, body string) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, uri, strings.NewReader(body))
				if method == http.MethodPost {
					r.Header.Set("Content-Type", "application/json")
				}
				w := httptest.NewRecorder()
				candidateVerificationHTTP(w, r, pool, auth, owner)
				return w
			}
			if got := invoke(s.m.pg.DB, "GET", path, ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"verifications":[]`) {
				t.Fatalf("initial history %d", got.Code)
			}
			payload := fmt.Sprintf(`{"request_id":"synthetic-request-001","evidence_digest":%q}`, rows[0].EvidenceDigest)
			got := invoke(s.m.pg.DB, "POST", path, payload)
			var page candidateVerificationPage
			if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &page) != nil || len(page.Verifications) != 1 || page.Verifications[0].Latest.State != tc.state {
				t.Fatalf("stored evidence request %d: %s", got.Code, got.Body.String())
			}
			if tc.verdict == "" && page.Verifications[0].Latest.Verdict != nil || tc.verdict != "" && (page.Verifications[0].Latest.Verdict == nil || *page.Verifications[0].Latest.Verdict != tc.verdict) {
				t.Fatalf("unexpected verdict %+v", page.Verifications[0].Latest)
			}
			if tc.name == "duplicate-attempt" {
				if page.StoredEvidence.State != "not_stored" || page.StoredEvidence.Count != 0 {
					t.Fatalf("duplicate attempt became stored independent evidence: %+v", page.StoredEvidence)
				}
			} else if tc.claim != "" {
				wantCount := 1
				if tc.second != "" {
					wantCount = 2
				}
				if tc.name == "mixed-context" {
					wantCount = 3
				}
				if page.StoredEvidence.State != "checked" || page.StoredEvidence.Count != wantCount || page.StoredEvidence.Verdict != tc.verdict {
					t.Fatalf("durable stored observations not rechecked: %+v", page.StoredEvidence)
				}
			}
			if page.Verifications[0].OriginalDigest != rows[0].EvidenceDigest || page.Verifications[0].Latest.SourcesDigest == "" {
				t.Fatal("original binding or source digest missing")
			}
			reportPath := fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal", taskID)
			for _, query := range []string{"", "&format=docx"} {
				exported := reportRequest("GET", reportPath+query, "")
				if exported.Code != 200 {
					t.Fatalf("latest report %s: %d", query, exported.Code)
				}
				content := exported.Body.String()
				if query != "" {
					content = diagnosticWordText(t, wordParts(t, exported.Body.Bytes())["word/document.xml"])
				}
				if !strings.Contains(content, "최근 선택 검증") || !strings.Contains(content, tc.state) {
					t.Fatalf("latest report omits attempt %s", query)
				}
			}
			if again := invoke(s.m.pg.DB, "POST", path, payload); again.Code != 200 {
				t.Fatalf("idempotent repeat %d", again.Code)
			}
			secondPayload := strings.Replace(payload, "synthetic-request-001", "synthetic-request-002", 1)
			second := invoke(s.m.pg.DB, "POST", path, secondPayload)
			if second.Code != 200 {
				t.Fatalf("same evidence or pending duplicate %d", second.Code)
			}
			if bad := invoke(s.m.pg.DB, "POST", path, strings.Replace(payload, rows[0].EvidenceDigest, strings.Repeat("0", 64), 1)); bad.Code != 409 {
				t.Fatalf("changed digest accepted %d", bad.Code)
			}
			if foreign := invoke(s.m.pg.DB, "GET", strings.Replace(path, fmt.Sprintf("tasks/%d", taskID), "tasks/999999999", 1), ""); foreign.Code != 403 {
				t.Fatalf("foreign task accepted %d", foreign.Code)
			}
			if other := invoke(s.m.pg.DB, "GET", strings.Replace(path, fmt.Sprintf("candidates/%d", candidateID), "candidates/999999999", 1), ""); other.Code != 404 {
				t.Fatalf("foreign candidate accepted %d", other.Code)
			}
			// 새 연결의 같은 API로 기록을 읽어 메모리 상태에 의존하지 않음을 확인한다.
			fresh, err := sql.Open("pgx", os.Getenv("ARTEX_PG_DSN"))
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			persisted := invoke(fresh, "GET", path, "")
			if persisted.Code != 200 || !strings.Contains(persisted.Body.String(), page.Verifications[0].ID) {
				t.Fatalf("restart history %d", persisted.Code)
			}
			if tc.claim != "" && tc.name != "duplicate-attempt" {
				var observationID int64
				var originalDigest string
				if err := s.m.pg.QueryRowContext(t.Context(), `SELECT id,source_digest FROM diagnostic_candidate_observations_v2 WHERE task_id=$1 ORDER BY id LIMIT 1`, taskID).Scan(&observationID, &originalDigest); err != nil {
					t.Fatal(err)
				}
				if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE diagnostic_candidate_observations_v2 SET source_digest=$2 WHERE id=$1`, observationID, strings.Repeat("0", 64)); err != nil {
					t.Fatal(err)
				}
				if corrupt := invoke(fresh, "GET", path, ""); corrupt.Code != 503 {
					t.Fatalf("tampered durable observation accepted: %d", corrupt.Code)
				}
				if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE diagnostic_candidate_observations_v2 SET source_digest=$2 WHERE id=$1`, observationID, originalDigest); err != nil {
					t.Fatal(err)
				}
			}
			if owner.calls.Load() == 0 {
				t.Fatal("owner receipt was never checked")
			}
			if tc.verdict == "confirmed" {
				if _, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_requests(task_id,candidate_id,original_run_ref,original_evidence_version,original_evidence_digest,requester,request_key,method)
SELECT $1,$2,$3,$4,$5,'ARTEX','synthetic-page-'||lpad(n::text,3,'0'),'stored/v1' FROM generate_series(1,101) n`, taskID, candidateID, passiveRunRef, rows[0].EvidenceVersion, rows[0].EvidenceDigest); err != nil {
					t.Fatal(err)
				}
				if _, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_events(request_id,step,process_state,verdict,reason,sources,sources_digest,rule_version)
SELECT q.id,1,e.process_state,e.verdict,e.reason,e.sources,e.sources_digest,e.rule_version
FROM diagnostic_candidate_verification_requests q CROSS JOIN diagnostic_candidate_verification_events e
WHERE q.task_id=$1 AND q.candidate_id=$2 AND q.request_key LIKE 'synthetic-page-%' AND e.request_id=$3`, taskID, candidateID, page.Verifications[0].ID); err != nil {
					t.Fatal(err)
				}
				first := invoke(s.m.pg.DB, "GET", path, "")
				var firstPage candidateVerificationPage
				if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &firstPage) != nil || len(firstPage.Verifications) != 100 || !firstPage.Truncated || firstPage.NextBefore == "" {
					t.Fatalf("first history page %d", first.Code)
				}
				second := invoke(s.m.pg.DB, "GET", path+"?before="+firstPage.NextBefore, "")
				var secondPage candidateVerificationPage
				if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &secondPage) != nil || len(secondPage.Verifications) != 2 || secondPage.Truncated {
					t.Fatalf("second history page %d", second.Code)
				}
				if _, err := s.m.pg.ExecContext(t.Context(), `DELETE FROM diagnostic_candidate_verification_events WHERE request_id=$1`, firstPage.Verifications[0].ID); err != nil {
					t.Fatal(err)
				}
				if bad := invoke(s.m.pg.DB, "GET", path, ""); bad.Code != 503 {
					t.Fatalf("missing latest event reached API: %d", bad.Code)
				}
				if bad := reportRequest("GET", reportPath, ""); bad.Code != 503 {
					t.Fatalf("missing latest event reached report: %d", bad.Code)
				}
			}
			if tc.verdict == "insufficient" {
				if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE diagnostic_candidate_verification_events SET verdict='confirmed' WHERE request_id=$1`, page.Verifications[0].ID); err != nil {
					t.Fatal(err)
				}
				if bad := invoke(s.m.pg.DB, "GET", path, ""); bad.Code != 503 {
					t.Fatalf("tampered history API %d", bad.Code)
				}
				if bad := reportRequest("GET", reportPath, ""); bad.Code != 503 {
					t.Fatalf("tampered latest verdict reached report: %d", bad.Code)
				}
			}
			if tc.claim == "" {
				if _, err := s.m.pg.ExecContext(t.Context(), `DELETE FROM diagnostic_candidate_observations_v2 WHERE task_id=$1`, taskID); err != nil {
					t.Fatal(err)
				}
				if missing := invoke(fresh, "GET", path, ""); missing.Code != 503 {
					t.Fatalf("deleted durable observation accepted: %d", missing.Code)
				}
			}
		})
	}
}

func TestCandidateVerificationConcurrentRequestPostgres(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	candidateID := storePassiveCandidate(t, s, taskID, "html_nosniff_missing", "nosniff_missing", "")
	rows, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	var envelope struct {
		Trusted e2runner.TrustedResult `json:"trusted"`
	}
	if json.Unmarshal([]byte(rows[0].Evidence), &envelope) != nil {
		t.Fatal("candidate evidence")
	}
	primary := envelope.Trusted
	primary.ProcessDrained, primary.ProxyDrained, primary.Execution.AuditComplete = true, true, true
	state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil, []e2runner.TrustedResult{primary}); err != nil {
		t.Fatal(err)
	}
	owner := &verificationOwnerEndpoint{task: fmt.Sprint(taskID)}
	auth := func(*http.Request) (string, error) { return "ARTEX", nil }
	path := fmt.Sprintf("/api/product/tasks/%d/candidates/%d/verifications", taskID, candidateID)
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"request_id":"synthetic-concurrent-%03d","evidence_digest":%q}`, i, rows[0].EvidenceDigest)
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			candidateVerificationHTTP(w, r, s.m.pg.DB, auth, owner)
			results <- w.Code
		}(i)
	}
	wg.Wait()
	close(results)
	ok := 0
	for code := range results {
		if code == 200 {
			ok++
		} else {
			t.Fatalf("unexpected code %d", code)
		}
	}
	var saved int
	if err := s.m.pg.QueryRowContext(t.Context(), `SELECT count(*) FROM diagnostic_candidate_verification_requests WHERE task_id=$1 AND candidate_id=$2`, taskID, candidateID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if ok != 2 || saved != 1 {
		t.Fatalf("concurrent same-evidence requests: 200=%d rows=%d", ok, saved)
	}
}

func TestCandidateVerificationStorageBindingPostgres(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	candidateID := storePassiveCandidate(t, s, taskID, "html_nosniff_missing", "nosniff_missing", "")
	candidates, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
	if err != nil || len(candidates) != 1 {
		t.Fatal(err)
	}
	other, err := s.m.CreateTask("synthetic unrelated task", "foreign candidate binding", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var otherTask int64
	if _, err := fmt.Sscan(other.ID, &otherTask); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.DeleteTask(otherTask) })
	insert := func(task, candidate int64, digest string) error {
		_, err := s.m.pg.ExecContext(t.Context(), `INSERT INTO diagnostic_candidate_verification_requests(task_id,candidate_id,original_run_ref,original_evidence_version,original_evidence_digest,requester,request_key,method) VALUES($1,$2,$3,$4,$5,'ARTEX','synthetic-binding-001','stored/v1')`, task, candidate, passiveRunRef, candidates[0].EvidenceVersion, digest)
		return err
	}
	for _, bad := range []struct {
		task, candidate int64
		digest          string
	}{
		{otherTask, candidateID, candidates[0].EvidenceDigest},
		{taskID, candidateID, strings.Repeat("0", 64)},
		{taskID, candidates[0].IntentID, candidates[0].EvidenceDigest},
	} {
		var pgErr *pgconn.PgError
		if err := insert(bad.task, bad.candidate, bad.digest); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("invalid direct storage binding accepted: %v", err)
		}
	}
	if err := insert(taskID, candidateID, candidates[0].EvidenceDigest); err != nil {
		t.Fatalf("valid direct storage binding rejected: %v", err)
	}
}

func TestCandidateVerificationProductRoutePostgres(t *testing.T) {
	s, f, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	candidateID := storePassiveCandidate(t, s, taskID, "html_nosniff_missing", "nosniff_missing", "")
	candidates, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
	if err != nil || len(candidates) != 1 {
		t.Fatal(err)
	}
	var envelope struct {
		Trusted e2runner.TrustedResult `json:"trusted"`
	}
	if json.Unmarshal([]byte(candidates[0].Evidence), &envelope) != nil {
		t.Fatal("candidate evidence")
	}
	primary := envelope.Trusted
	primary.ProcessDrained, primary.ProxyDrained, primary.Execution.AuditComplete = true, true, true
	state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil, []e2runner.TrustedResult{primary}); err != nil {
		t.Fatal(err)
	}
	password := "synthetic-candidate-product-password"
	priorHash, hadHash, err := s.m.pg.GetSetting(authPassKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadHash {
			if err := s.m.pg.SetSetting(authPassKey, priorHash); err != nil {
				t.Error(err)
			}
		} else if _, err := s.m.pg.Exec(`DELETE FROM settings WHERE key=$1`, authPassKey); err != nil {
			t.Error(err)
		}
	})
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil || s.m.pg.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("product auth setup", err)
	}
	keys, data := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if err := os.WriteFile(filepath.Join(keys, jwtKeyFilename), []byte("synthetic-candidate-product-jwt-key"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := &verificationOwnerEndpoint{task: fmt.Sprint(taskID)}
	product, err := NewProductDiagnosisHandler(t.Context(), s.m.pg.DB, keys, data, owner, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		product.ServeHTTP(w, r)
		return w
	}
	login := call("POST", "/api/auth/login", fmt.Sprintf(`{"username":"ABC2LAB","password":%q}`, password), "")
	var session struct{ Token string }
	if login.Code != 200 || json.Unmarshal(login.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatalf("product login failed: %d", login.Code)
	}
	path := fmt.Sprintf("/api/product/tasks/%d/candidates/%d/verifications", taskID, candidateID)
	if got := call("GET", path, "", ""); got.Code != 401 {
		t.Fatalf("unauthenticated product route %d", got.Code)
	}
	if got := call("GET", path, "", session.Token); got.Code != 200 {
		t.Fatalf("product verification read %d", got.Code)
	}
	if got := call("POST", path, fmt.Sprintf(`{"request_id":"synthetic-product-001","evidence_digest":%q}`, candidates[0].EvidenceDigest), session.Token); got.Code != 200 || !strings.Contains(got.Body.String(), `"process_state":"completed"`) {
		t.Fatalf("product verification write %d", got.Code)
	}
	if got := call("GET", path+"?before=01", "", session.Token); got.Code != 400 {
		t.Fatalf("noncanonical page cursor %d", got.Code)
	}
	if owner.calls.Load() == 0 {
		t.Fatal("product route skipped owner receipt")
	}
}

func TestCandidateSourceErrorSurvivesSuccessfulProjectionPostgres(t *testing.T) {
	s, finding, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(finding.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	task := *stored.TaskID
	if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, task, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	id := storePassiveCandidate(t, s, task, "html_nosniff_missing", "nosniff_missing", "")
	_, err = s.m.pg.ExecContext(t.Context(), `UPDATE exploration_nodes SET payload=jsonb_set(payload,'{fence,attempt_high_water}','0') WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND origin='diagnostic_candidate_save_fence_v2' AND payload->>'candidate_id'=$2`, task, fmt.Sprint(id))
	if err != nil {
		t.Fatal(err)
	}
	state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, task, state, nil, nil, nil, []e2runner.TrustedResult{}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.m.pg.QueryRowContext(t.Context(), `SELECT n.payload FROM exploration_nodes n JOIN tasks t ON n.exploration_id=t.exploration_id WHERE t.id=$1 AND n.origin=$2`, task, diagnosisRunOrigin).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record diagnosisRunRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Verification == nil || !record.Verification.SourceUnavailable || record.Verification.ObservationsStored {
		t.Fatalf("unreadable candidates presented as an empty run: %+v, %v", record.Verification, err)
	}
}

func TestStoredReviewUnavailableIsLocalToCandidatePostgres(t *testing.T) {
	s, finding, _ := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(finding.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	task := *stored.TaskID
	if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, task, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	good := storePassiveCandidate(t, s, task, "html_nosniff_missing", "nosniff_missing", "")
	partial := storePassiveCandidateForCall(t, s, task, "html_nosniff_missing", "", "", "partial-call", "partial-attempt")
	rows, err := s.m.ResolveTask(fmt.Sprint(task)).Store.ListDiagnosticCandidates(task, passiveRunRef, 2)
	if err != nil || len(rows) != 2 {
		t.Fatalf("candidate fixture: %d %v", len(rows), err)
	}
	var calls []e2runner.TrustedResult
	for _, row := range rows {
		var envelope struct {
			Trusted e2runner.TrustedResult `json:"trusted"`
		}
		if err := json.Unmarshal([]byte(row.Evidence), &envelope); err != nil {
			t.Fatal(err)
		}
		call := envelope.Trusted
		call.ProcessDrained, call.ProxyDrained, call.Execution.AuditComplete = true, true, true
		calls = append(calls, call)
	}
	state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, task, state, nil, nil, nil, calls); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   int64
		want string
	}{{good, "checked"}, {partial, "not_stored"}} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/product/tasks/%d/candidates/%d/verifications", task, tc.id), nil)
		candidateVerificationHTTP(rec, req, s.m.pg.DB, func(*http.Request) (string, error) { return "ARTEX", nil }, &verificationOwnerEndpoint{task: fmt.Sprint(task)})
		var page candidateVerificationPage
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || page.StoredEvidence.State != tc.want || page.Automatic.Status != "insufficient" {
			t.Fatalf("candidate %d: %d %s", tc.id, rec.Code, rec.Body)
		}
	}
}
