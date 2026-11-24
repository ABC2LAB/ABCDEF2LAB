package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/report"
)

const passiveRunRef = "synthetic-passive-run"

func storePassiveCandidate(t *testing.T, s *Server, taskID int64, claim, first, second string) int64 {
	t.Helper()
	task := s.m.ResolveTask(fmt.Sprint(taskID))
	intent, err := task.Store.AddIntent(map[string]any{"description": "synthetic normal HTML observation"}, 5, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	obs := func(ref, state string) e2proxy.HTTPObservation {
		out := e2proxy.HTTPObservation{Sequence: 1, AttemptRef: ref, Method: "GET", Endpoint: "/documents", RuleRef: "approved-rule", Decision: "allowed", StatusCode: 200, ResponseComplete: true, ForwardAttempted: true, AuditComplete: true, ObservedAt: time.Now().UTC()}
		if claim == "server_version_banner" {
			out.Technologies = []e2proxy.HTTPTechnology{{Name: "Example", Version: state, Header: "Server"}}
		} else {
			out.HTMLSecurity = state
		}
		return out
	}
	call := func(ref string, observation e2proxy.HTTPObservation) map[string]any {
		return map[string]any{"trusted": map[string]any{"classification": "completed", "final": true, "output_truncated": false,
			"execution": map[string]any{"scope": map[string]string{"TaskRef": fmt.Sprint(taskID), "RunRef": passiveRunRef}, "intent_id": intent},
			"call":      map[string]any{"call_ref": ref, "intent_id": intent, "final": true, "audit_complete": true, "observations": []e2proxy.HTTPObservation{observation}},
		}}
	}
	class := "security_headers"
	if claim == "server_version_banner" {
		class = "information_disclosure"
	}
	primary := call("passive-call-a", obs("passive-attempt-a", first))
	primary["related_calls"] = []any{call("passive-call-b", obs("passive-attempt-b", second))}
	raw, err := json.Marshal(primary)
	if err != nil {
		t.Fatal(err)
	}
	c := db.DiagnosticCandidate{TaskID: taskID, IntentID: intent, RunRef: passiveRunRef, CallRef: "passive-call-a", VulnClass: class, VerificationClaim: claim, Name: "합성 관찰", Summary: "모델 주장", Classification: "completed", Verification: "manual_review_required", EvidenceVersion: 1, Evidence: string(raw), EvidenceDigest: db.DiagnosticEvidenceDigest(string(raw)), ObservedAt: time.Now().UTC()}
	id, err := task.Store.RecordDiagnosticCandidate(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	return id
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
