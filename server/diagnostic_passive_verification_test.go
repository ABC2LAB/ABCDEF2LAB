package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/candidateverify"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/report"
)

const passiveRunRef = "synthetic-passive-run"

func storePassiveCandidate(t *testing.T, s *Server, taskID int64, claim, first, second string) int64 {
	t.Helper()
	return storePassiveCandidateForCall(t, s, taskID, claim, first, second, "passive-call-a", "passive-attempt-a")
}

func storePassiveCandidateForCall(t *testing.T, s *Server, taskID int64, claim, first, second, callRef, attemptRef string) int64 {
	t.Helper()
	task := s.m.ResolveTask(fmt.Sprint(taskID))
	intent, err := task.Store.AddIntent(map[string]any{"description": "synthetic normal HTML observation"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	obs := func(ref, state string) e2proxy.HTTPObservation {
		out := e2proxy.HTTPObservation{Sequence: 1, AttemptRef: ref, Method: "GET", Endpoint: "/documents", ExactFunction: true, RuleRef: "approved-rule", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}
		if claim == "server_version_banner" {
			out.Technologies = []e2proxy.HTTPTechnology{{Name: "Example", Version: state, Header: "Server"}}
		} else {
			out.HTMLSecurity = state
		}
		return out
	}
	call := func(ref string, observation e2proxy.HTTPObservation) map[string]any {
		return map[string]any{"trusted": map[string]any{"classification": "completed", "final": true, "output_truncated": false,
			"execution": map[string]any{"scope": map[string]string{"TaskRef": fmt.Sprint(taskID), "RunRef": passiveRunRef}, "intent_id": intent, "policy_revision": "synthetic-policy", "header_mode": "preserve"},
			"call":      map[string]any{"call_ref": ref, "intent_id": intent, "final": true, "audit_complete": true, "observations": []e2proxy.HTTPObservation{observation}},
		}}
	}
	class := "security_headers"
	if claim == "server_version_banner" {
		class = "information_disclosure"
	}
	primary := call(callRef, obs(attemptRef, first))
	if second != "" {
		primary["related_calls"] = []any{call("passive-call-b", obs("passive-attempt-b", second))}
	}
	raw, err := json.Marshal(primary)
	if err != nil {
		t.Fatal(err)
	}
	c := db.DiagnosticCandidate{TaskID: taskID, IntentID: intent, RunRef: passiveRunRef, CallRef: callRef, VulnClass: class, VerificationClaim: claim, Name: "합성 관찰", Summary: "모델 주장", Classification: "completed", Verification: "manual_review_required", EvidenceVersion: 1, Evidence: string(raw), EvidenceDigest: db.DiagnosticEvidenceDigest(string(raw)), ObservedAt: time.Now().UTC()}
	var envelope struct {
		Trusted e2runner.TrustedResult `json:"trusted"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	scope, err := json.Marshal(envelope.Trusted.Execution.Scope)
	if err != nil {
		t.Fatal(err)
	}
	id, err := task.Store.RecordDiagnosticCandidateWithFence(t.Context(), c, func() (db.DiagnosticCandidateSaveFence, error) {
		return db.DiagnosticCandidateSaveFence{AttemptHighWater: 1, TaskRef: fmt.Sprint(taskID), RunRef: passiveRunRef, ScopeDigest: db.DiagnosticEvidenceDigest(string(scope)), PolicyRevision: "synthetic-policy"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPassiveV2ProductPostSaveAndRunningReportPostgres(t *testing.T) {
	for _, tc := range []struct {
		name, first, second          string
		status                       int
		confirmed, negative, pending int
		verdictText, reason          string
		sourceUnavailable            bool
		cancelled                    bool
		mixed                        bool
		unsupported                  bool
	}{
		{"confirmed", "nosniff_missing", "nosniff_missing", 200, 1, 0, 0, "확정", "html_nosniff_missing_twice", false, false, false, false},
		{"mixed", "nosniff_missing", "nosniff_missing", 200, 1, 0, 1, "확정", "html_nosniff_missing_twice", false, false, true, false},
		{"normal", "nosniff_present", "nosniff_present", 200, 0, 1, 0, "부정", "html_nosniff_present_twice", false, false, false, false},
		{"conflict", "nosniff_missing", "nosniff_present", 200, 0, 0, 1, "판단 불가", "observations_inconsistent", false, false, false, false},
		{"missing", "nosniff_missing", "", 200, 0, 0, 1, "판단 불가", "independent_observation_missing", false, false, false, false},
		{"source-unavailable", "nosniff_missing", "nosniff_missing", 200, 0, 0, 1, "판단 불가", "source_unavailable", true, false, false, false},
		{"unsupported", "nosniff_missing", "nosniff_missing", 200, 0, 0, 1, "의심(검증 미지원)", "type_not_supported", false, false, false, true},
		{"cancelled", "nosniff_missing", "nosniff_missing", 404, 0, 0, 1, "", "", false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, request := trafficEvidenceServer(t)
			stored, err := s.m.pg.GetFinding(f.FindingID)
			if err != nil || stored.TaskID == nil {
				t.Fatal(err)
			}
			taskID := *stored.TaskID
			if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
				t.Fatal(err)
			}
			claim := "html_nosniff_missing"
			if tc.unsupported {
				claim = ""
			}
			candidateID := storePassiveCandidate(t, s, taskID, claim, tc.first, "")
			var otherCandidateID int64
			if tc.mixed {
				otherCandidateID = storePassiveCandidate(t, s, taskID, "server_version_banner", "2.8", "")
			}
			rows, err := s.m.ResolveTask(fmt.Sprint(taskID)).Store.ListDiagnosticCandidates(taskID, passiveRunRef, 2)
			wantCandidates := 1
			if tc.mixed {
				wantCandidates = 2
			}
			if err != nil || len(rows) != wantCandidates || rows[0].ID != candidateID {
				t.Fatalf("candidate read n=%d err=%v", len(rows), err)
			}
			var raw struct {
				Trusted e2runner.TrustedResult `json:"trusted"`
			}
			if err := json.Unmarshal([]byte(rows[0].Evidence), &raw); err != nil {
				t.Fatal(err)
			}
			primary := raw.Trusted
			primary.ProcessDrained, primary.ProxyDrained, primary.Execution.AuditComplete = true, true, true
			calls := []e2runner.TrustedResult{primary}
			if tc.second != "" {
				other := primary
				other.Call = e2proxy.CloneCallResult(primary.Call)
				other.Call.CallRef = "call-after-save"
				other.Call.Observations[0].AttemptRef = "attempt-after-save"
				other.Call.Observations[0].Sequence = 2
				other.Call.Observations[0].HTMLSecurity = tc.second
				calls = append(calls, other)
			}
			if tc.sourceUnavailable {
				calls = nil
			}
			path := fmt.Sprintf("/api/report/diagnostic?task=%d", taskID)
			for _, query := range []string{"&audience=internal", "&audience=shared", "&audience=internal&format=docx"} {
				got := request("GET", path+query, "")
				if got.Code != 409 {
					t.Fatalf("running report %s: %d", query, got.Code)
				}
			}
			review := request("GET", path+"&audience=internal&format=review-json", "")
			if review.Code != 200 || !strings.Contains(review.Body.String(), "verification_not_complete") {
				t.Fatalf("running review %d", review.Code)
			}
			state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
			if tc.cancelled {
				state.Outcome = "cancelled"
			}
			var wg sync.WaitGroup
			failures := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					failures <- saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil, calls)
				}()
			}
			wg.Wait()
			close(failures)
			for err := range failures {
				if err != nil {
					t.Fatal(err)
				}
			}
			var payload []byte
			if err := s.m.pg.QueryRowContext(t.Context(), `SELECT payload FROM exploration_nodes WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND origin=$2`, taskID, diagnosisRunOrigin).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var record diagnosisRunRecord
			if err := json.Unmarshal(payload, &record); err != nil {
				t.Fatal(err)
			}
			if record.Verification == nil || record.Verification.Version != diagnosisVerificationV2 || record.Verification.NonIDORConfirmed != tc.confirmed || record.Verification.NonIDORNegative != tc.negative || record.Verification.VerificationPending != tc.pending || record.Verification.SourceUnavailable != tc.sourceUnavailable {
				t.Fatalf("v2 result %+v", record.Verification)
			}
			if tc.confirmed > 0 && !candidateverify.ValidateStoredV2(rows[0], record.Verification.Candidates[0].Result) {
				t.Fatal("stored v2 verdict rejected before report projection")
			}
			hash, err := s.reloadPassHash()
			if err != nil {
				t.Fatal(err)
			}
			token, err := signJWT(s.signingKey(hash))
			if err != nil {
				t.Fatal(err)
			}
			runRequest := httptest.NewRequest("GET", fmt.Sprintf("/api/tasks/%d/diagnosis-run", taskID), nil)
			runRequest.Header.Set("Authorization", "Bearer "+token)
			runResponse := httptest.NewRecorder()
			s.serveDiagnosisRun(runResponse, runRequest, taskID)
			var visible diagnosisRunRecord
			if runResponse.Code != 200 || json.Unmarshal(runResponse.Body.Bytes(), &visible) != nil || visible.Verification == nil || visible.Verification.Version != diagnosisVerificationV2 || visible.Verification.NonIDORConfirmed != tc.confirmed {
				t.Fatalf("diagnosis-run projection %d", runResponse.Code)
			}
			closedReview := request("GET", path+"&audience=internal&format=review-json", "")
			if closedReview.Code != 200 || !strings.Contains(closedReview.Body.String(), record.Verification.Candidates[0].Result.Reason) {
				t.Fatalf("closed review %d", closedReview.Code)
			}
			if tc.confirmed > 0 && (len(record.Verification.Candidates[0].Result.Sources) != 2 || !strings.Contains(closedReview.Body.String(), "scope_digest")) {
				t.Fatal("independent source provenance missing")
			}
			got := request("GET", path+"&audience=internal", "")
			if got.Code != tc.status {
				t.Fatalf("closed report %d: %s", got.Code, got.Body.String())
			}
			if tc.status == 200 {
				if !strings.Contains(got.Body.String(), tc.verdictText) || !strings.Contains(got.Body.String(), tc.reason) {
					t.Fatalf("missing verdict or reason: %s", tc.name)
				}
				if !strings.Contains(got.Body.String(), "HTML 문서 보안 헤더 누락") {
					if tc.confirmed > 0 {
						t.Fatal("missing confirmed entry")
					}
				}
				if !strings.Contains(got.Body.String(), fmt.Sprintf("candidate-%d", candidateID)) {
					t.Fatal("original candidate omitted")
				}
				if tc.mixed && !strings.Contains(got.Body.String(), fmt.Sprintf("candidate-%d", otherCandidateID)) {
					t.Fatal("unconfirmed candidate omitted")
				}
				word := request("GET", path+"&audience=internal&format=docx", "")
				if word.Code != 200 {
					t.Fatalf("docx %d", word.Code)
				}
				wordText := diagnosticWordText(t, wordParts(t, word.Body.Bytes())["word/document.xml"])
				if !strings.Contains(wordText, tc.verdictText) || !strings.Contains(wordText, tc.reason) {
					t.Fatalf("docx missing verdict or reason: %s", tc.name)
				}
				shared := request("GET", path+"&audience=shared", "")
				if shared.Code != 200 || strings.Contains(shared.Body.String(), "모델 주장") {
					t.Fatalf("shared report %d", shared.Code)
				}
				if tc.name == "confirmed" {
					_, err := s.m.pg.ExecContext(t.Context(), `UPDATE exploration_nodes SET payload=jsonb_set(payload,'{fence,attempt_high_water}','2'::jsonb) WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND origin=$2 AND payload->>'candidate_id'=$3`, taskID, "diagnostic_candidate_save_fence_v2", fmt.Sprint(candidateID))
					if err != nil {
						t.Fatal(err)
					}
					for _, query := range []string{"&audience=internal", "&audience=shared&format=docx", "&audience=internal&format=review-json"} {
						broken := request("GET", path+query, "")
						if broken.Code != 422 || broken.Header().Get("Content-Disposition") != "" || strings.Contains(broken.Body.String(), "HTML 문서 보안 헤더 누락") {
							t.Fatalf("tampered save fence exported %s: %d", query, broken.Code)
						}
					}
				}
			}
		})
	}
}

func TestConfirmedReportProjectionKeepsE3Once(t *testing.T) {
	verified := report.DiagnosticEntry{ID: "candidate-1", Kind: "candidate", Outcome: "confirmed"}
	pending := report.DiagnosticEntry{ID: "candidate-2", Kind: "candidate", Outcome: "review"}
	e3 := report.DiagnosticProductConfirmation{VerdictID: "3", ResourceType: "document", EvidenceCalls: 2}
	for _, tc := range []struct {
		name    string
		entries []report.DiagnosticEntry
		e3      []report.DiagnosticProductConfirmation
		want    bool
		count   int
	}{
		{"zero", []report.DiagnosticEntry{pending}, nil, false, 1},
		{"e3-only", []report.DiagnosticEntry{pending}, []report.DiagnosticProductConfirmation{e3}, true, 0},
		{"non-idor-only", []report.DiagnosticEntry{verified, pending}, nil, true, 1},
		{"mixed", []report.DiagnosticEntry{verified, pending}, []report.DiagnosticProductConfirmation{e3}, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := report.DiagnosticReport{Entries: tc.entries, ProductConfirmed: tc.e3}
			got := projectConfirmedReport(&data)
			if got != tc.want || len(data.Entries) != tc.count || len(data.ProductConfirmed) != len(tc.e3) {
				t.Fatalf("projected=%t entries=%d E3=%d", got, len(data.Entries), len(data.ProductConfirmed))
			}
		})
	}
}

func TestPassiveV2CompletedZeroFindingsReportPostgres(t *testing.T) {
	s, f, request := trafficEvidenceServer(t)
	stored, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || stored.TaskID == nil {
		t.Fatal(err)
	}
	taskID := *stored.TaskID
	if _, err := s.m.pg.ExecContext(t.Context(), `DELETE FROM findings WHERE id=$1 AND task_id=$2`, f.FindingID, taskID); err != nil {
		t.Fatal(err)
	}
	if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
		t.Fatal(err)
	}
	state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
	if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/report/diagnostic?task=%d", taskID)
	for _, query := range []string{"&audience=internal", "&audience=shared", "&audience=internal&format=docx", "&audience=shared&format=docx"} {
		got := request("GET", path+query, "")
		if got.Code != 200 {
			t.Fatalf("zero findings report %s: %d", query, got.Code)
		}
		body := got.Body.String()
		if strings.Contains(query, "docx") {
			body = diagnosticWordText(t, wordParts(t, got.Body.Bytes())["word/document.xml"])
		}
		if !strings.Contains(body, "원본 발견 항목 0건") || !strings.Contains(body, "안전을 뜻하지 않습니다") {
			t.Fatalf("zero findings scope disclaimer absent: %s", query)
		}
	}
}

func TestPassiveV2CorruptRunCannotExportPostgres(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*diagnosisRunRecord)
	}{
		{"bad-version", func(r *diagnosisRunRecord) { r.Version = 99 }},
		{"wrong-task", func(r *diagnosisRunRecord) { r.TaskID++ }},
		{"wrong-run", func(r *diagnosisRunRecord) { r.Run.RunRef = "other-run" }},
		{"wrong-task-and-run", func(r *diagnosisRunRecord) { r.TaskID++; r.Run.RunRef = "other-run" }},
		{"missing-verification", func(r *diagnosisRunRecord) { r.Verification = nil }},
		{"wrong-verification", func(r *diagnosisRunRecord) { r.Verification.Version = "passive/v1" }},
		{"invalid-outcome", func(r *diagnosisRunRecord) { r.Run.Outcome = "invalid" }},
		{"missing-saved-at", func(r *diagnosisRunRecord) { r.SavedAt = time.Time{} }},
		{"dismissed-row", func(r *diagnosisRunRecord) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, request := trafficEvidenceServer(t)
			stored, err := s.m.pg.GetFinding(f.FindingID)
			if err != nil || stored.TaskID == nil {
				t.Fatal(err)
			}
			taskID := *stored.TaskID
			if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
				t.Fatal(err)
			}
			storePassiveCandidate(t, s, taskID, "html_nosniff_missing", "nosniff_missing", "")
			path := fmt.Sprintf("/api/report/diagnostic?task=%d", taskID)
			for _, query := range []string{"&audience=internal", "&audience=shared", "&audience=internal&format=docx", "&audience=shared&format=docx"} {
				if got := request("GET", path+query, ""); got.Code != 409 {
					t.Fatalf("running report %s: %d", query, got.Code)
				}
			}
			record := diagnosisRunRecord{Version: 1, TaskID: taskID, SavedAt: time.Now().UTC(), Run: requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}, Verification: &diagnosisVerificationRecord{Version: diagnosisVerificationV2}}
			tc.mutate(&record)
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.m.pg.ExecContext(t.Context(), `INSERT INTO exploration_nodes(exploration_id,kind,state,origin,payload) VALUES((SELECT exploration_id FROM tasks WHERE id=$1),'fact','confirmed',$2,$3::jsonb)`, taskID, diagnosisRunOrigin, string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "dismissed-row" {
				if _, err := s.m.pg.ExecContext(t.Context(), `UPDATE exploration_nodes SET state='dismissed' WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND origin=$2`, taskID, diagnosisRunOrigin); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := s.m.pg.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readDiagnosticRun(t.Context(), tx, taskID); !errors.Is(err, ErrFirstDiagnosisOutcome) {
				t.Fatalf("corrupt v2 run should fail at run loader: %v", err)
			}
			tx.Rollback()
			for _, query := range []string{"&audience=internal", "&audience=shared", "&audience=internal&download=1", "&audience=shared&download=1", "&audience=internal&format=docx", "&audience=shared&format=docx"} {
				got := request("GET", path+query, "")
				if got.Code != 503 || strings.Contains(got.Body.String(), "합성 관찰") || strings.Contains(got.Body.String(), "HTML 문서 보안 헤더 누락") || got.Header().Get("Content-Disposition") != "" {
					t.Fatalf("corrupt run export %s: status=%d disposition=%q", query, got.Code, got.Header().Get("Content-Disposition"))
				}
			}
			if got := request("GET", path+"&audience=internal&format=review-json", ""); got.Code != 503 {
				t.Fatalf("corrupt run review status=%d", got.Code)
			}
		})
	}
}

func TestPassiveV2CorruptPolicyCannotBecomeLegacyPostgres(t *testing.T) {
	for _, field := range []string{"version", "task_id", "state"} {
		t.Run(field, func(t *testing.T) {
			s, f, request := trafficEvidenceServer(t)
			stored, err := s.m.pg.GetFinding(f.FindingID)
			if err != nil || stored.TaskID == nil {
				t.Fatal(err)
			}
			taskID := *stored.TaskID
			if err := saveDiagnosisVerificationPolicy(t.Context(), s.m.pg.DB, taskID, passiveRunRef); err != nil {
				t.Fatal(err)
			}
			storePassiveCandidate(t, s, taskID, "html_nosniff_missing", "nosniff_missing", "")
			value := `"invalid"`
			if field == "task_id" {
				value = fmt.Sprint(taskID + 1)
			}
			if field == "state" {
				_, err = s.m.pg.ExecContext(t.Context(), `UPDATE exploration_nodes SET state='dismissed' WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND origin=$2`, taskID, diagnosisVerificationPolicyOrigin)
			} else {
				_, err = s.m.pg.ExecContext(t.Context(), `UPDATE exploration_nodes SET payload=jsonb_set(payload,ARRAY[$1]::text[], $2::jsonb) WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$3) AND origin=$4`, field, value, taskID, diagnosisVerificationPolicyOrigin)
			}
			if err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/api/report/diagnostic?task=%d", taskID)
			for _, query := range []string{"&audience=internal", "&audience=shared&download=1", "&audience=internal&format=docx", "&audience=shared&format=docx", "&audience=internal&format=review-json"} {
				got := request("GET", path+query, "")
				if got.Code != 503 || got.Header().Get("Content-Disposition") != "" || strings.Contains(got.Body.String(), "합성 관찰") {
					t.Fatalf("damaged policy fell back to legacy export %s: %d", query, got.Code)
				}
			}
		})
	}
}

func TestPassiveCandidateCompletionAndReportPostgres(t *testing.T) {
	for _, tc := range []struct {
		name, claim, first, second                 string
		reportStatus, confirmed, negative, pending int
	}{
		{"confirmed-with-pending", "html_nosniff_missing", "nosniff_missing", "nosniff_missing", 200, 1, 0, 0},
		{"banner-confirmed", "server_version_banner", "2.8", "2.8", 200, 1, 0, 0},
		{"benign", "html_nosniff_missing", "nosniff_present", "nosniff_present", 404, 0, 1, 0},
		{"insufficient", "html_nosniff_missing", "nosniff_missing", "", 404, 0, 0, 1},
		{"conflicting", "html_nosniff_missing", "nosniff_missing", "nosniff_present", 404, 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, request := trafficEvidenceServer(t)
			stored, err := s.m.pg.GetFinding(f.FindingID)
			if err != nil || stored.TaskID == nil {
				t.Fatal(err)
			}
			taskID := *stored.TaskID
			candidateID := storePassiveCandidate(t, s, taskID, tc.claim, tc.first, tc.second)
			diagnosticServerCandidate(t, s, taskID, "completed") // 다른 실행의 모델 후보도 원본 근거로 남긴다.
			state := requestApprovalRunState{RunRef: passiveRunRef, Phase: "closed", Outcome: "completed", DrainComplete: true}
			var wg sync.WaitGroup
			errors := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errors <- saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil)
				}()
			}
			wg.Wait()
			close(errors)
			for err := range errors {
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := saveDiagnosisRunRecordGoal(t.Context(), s.m.pg.DB, taskID, state, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			var runRows int
			if err := s.m.pg.QueryRowContext(t.Context(), `SELECT count(*) FROM exploration_nodes WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND origin=$2`, taskID, diagnosisRunOrigin).Scan(&runRows); err != nil || runRows != 1 {
				t.Fatalf("duplicate completion record n=%d err=%v", runRows, err)
			}
			var raw []byte
			if err := s.m.pg.QueryRowContext(t.Context(), `SELECT payload FROM exploration_nodes WHERE exploration_id=(SELECT exploration_id FROM tasks WHERE id=$1) AND origin=$2`, taskID, diagnosisRunOrigin).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var record diagnosisRunRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			if record.Verification == nil || record.Verification.NonIDORConfirmed != tc.confirmed || record.Verification.NonIDORNegative != tc.negative || record.Verification.VerificationPending != tc.pending || len(record.Verification.Candidates) != 1 || record.Verification.Candidates[0].CandidateID != candidateID {
				t.Fatalf("completion assessment %+v", record.Verification)
			}
			hash, err := s.reloadPassHash()
			if err != nil {
				t.Fatal(err)
			}
			token, err := signJWT(s.signingKey(hash))
			if err != nil {
				t.Fatal(err)
			}
			runRequest := httptest.NewRequest("GET", fmt.Sprintf("/api/tasks/%d/diagnosis-run", taskID), nil)
			runRequest.Header.Set("Authorization", "Bearer "+token)
			runResponse := httptest.NewRecorder()
			s.serveDiagnosisRun(runResponse, runRequest, taskID)
			var visible diagnosisRunRecord
			if runResponse.Code != 200 || json.Unmarshal(runResponse.Body.Bytes(), &visible) != nil || visible.Verification == nil || visible.Verification.NonIDORConfirmed != tc.confirmed || len(visible.Verification.Candidates) != 1 {
				t.Fatalf("diagnosis-run projection %d", runResponse.Code)
			}
			path := fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal", taskID)
			review := request("GET", path+"&format=review-json", "")
			var reviewData struct {
				CandidateVerifications []struct {
					ID, Status, Reason string
					EvidenceDigest     string `json:"evidence_digest"`
					Observations       []struct {
						Step            int
						Method, Summary string
					}
				} `json:"candidate_verifications"`
			}
			if review.Code != 200 || json.Unmarshal(review.Body.Bytes(), &reviewData) != nil || len(reviewData.CandidateVerifications) != 1 || reviewData.CandidateVerifications[0].ID != fmt.Sprintf("candidate-%d", candidateID) || reviewData.CandidateVerifications[0].EvidenceDigest == "" || len(reviewData.CandidateVerifications[0].Observations) == 0 || strings.Contains(review.Body.String(), "모델 주장") {
				t.Fatalf("completion review projection status=%d rows=%d first=%+v", review.Code, len(reviewData.CandidateVerifications), reviewData.CandidateVerifications)
			}
			html := request("GET", path, "")
			if html.Code != tc.reportStatus {
				t.Fatalf("report status %d", html.Code)
			}
			if tc.reportStatus == 200 {
				title := "HTML 문서 보안 헤더 누락"
				if tc.claim == "server_version_banner" {
					title = "Server 응답 헤더의 버전 노출"
				}
				if !strings.Contains(html.Body.String(), title) || strings.Contains(html.Body.String(), "합성 XSS 후보") || strings.Contains(html.Body.String(), "모델 주장") {
					t.Fatal("confirmed-only report projection failed")
				}
				word := request("GET", path+"&format=docx", "")
				if word.Code != 200 {
					t.Fatalf("docx status %d", word.Code)
				}
			} else if strings.Contains(html.Body.String(), "HTML 문서 보안 헤더 누락") {
				t.Fatal("report generated without confirmation")
			}
		})
	}
}
