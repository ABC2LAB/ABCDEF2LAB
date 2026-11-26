package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/learnedlogin"
	"github.com/Autumn-27/artex/server"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

// auditedLearnedLogin은 기록의 진단 단계 행마다 맞는 감사 완료 E② 모델 송신 결과가 있는 실행에 붙은 학습 로그인 상태다.
func auditedLearnedLogin(registry *productTargetRegistry, runsDir, jobID string, target productTargetRecord, runRef string, credentials map[string]string, identityPath string) *productLearnedLogin {
	ll := newProductLearnedLogin(registry, runsDir, jobID, target, runRef, credentials, identityPath)
	sends := map[string]learnedlogin.AuditedSend{}
	for _, row := range ll.rows {
		if callRef, attemptID, ok := strings.Cut(row.TransportRef, "/"); ok {
			sends[row.TransportRef] = learnedlogin.AuditedSend{CallRef: callRef, AttemptID: attemptID, RunRef: row.RunRef, ModelCall: true,
				Method: row.Method, Allowed: true, RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: row.Status, AuditComplete: true}
		}
	}
	ll.setAudit(func(callRef, attemptID string) (learnedlogin.AuditedSend, bool) {
		s, ok := sends[callRef+"/"+attemptID]
		return s, ok
	})
	return ll
}

// 이 회차 진단 단계 성공 행은 그 실행의 감사 완료된 E② 모델 송신 결과와 맞을 때만 쓴다(§6-2 출처 결속). 없는 참조·다른
// 회차·다른 요청(메서드·상태)·보내지 않음·감사 미완료·프로그램 호출·감사 조회 없음은 그 방법을 쓰지 않고, 로그인 설정으로
// 넘어가지도 않는다. 탐색 단계 행은 감사 근거가 없어 형식만 보고 출처를 "탐색·감사 없음"으로 남긴다.
func TestLearnedLoginTransportAudit(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	creds := map[string]string{"tester-a": credentialOf(r, rec.Target.ID, "tester-a")}
	row := modelSucceededRow("diagnosis-1", "tester-a", "/login")
	good := learnedlogin.AuditedSend{CallRef: "call_m", AttemptID: "attempt_1", RunRef: "run-job1", ModelCall: true, Method: "POST", Allowed: true,
		RequestWritten: true, ResponseReceived: true, ResponseComplete: true, StatusCode: 302, AuditComplete: true}
	for name, tc := range map[string]struct {
		send   *learnedlogin.AuditedSend
		change func(*learnedlogin.AuditedSend)
		want   learnedlogin.Reason
	}{
		"audited":         {&good, nil, ""},
		"missing":         {nil, nil, learnedlogin.ReasonTransportUnverified},
		"other run":       {&good, func(s *learnedlogin.AuditedSend) { s.RunRef = "run-other" }, learnedlogin.ReasonTransportUnverified},
		"other method":    {&good, func(s *learnedlogin.AuditedSend) { s.Method = "GET" }, learnedlogin.ReasonTransportUnverified},
		"other status":    {&good, func(s *learnedlogin.AuditedSend) { s.StatusCode = 401 }, learnedlogin.ReasonTransportUnverified},
		"not written":     {&good, func(s *learnedlogin.AuditedSend) { s.RequestWritten = false }, learnedlogin.ReasonTransportUnverified},
		"incomplete":      {&good, func(s *learnedlogin.AuditedSend) { s.ResponseComplete = false }, learnedlogin.ReasonTransportUnverified},
		"audit failed":    {&good, func(s *learnedlogin.AuditedSend) { s.AuditComplete = false }, learnedlogin.ReasonTransportUnverified},
		"denied":          {&good, func(s *learnedlogin.AuditedSend) { s.Allowed = false }, learnedlogin.ReasonTransportUnverified},
		"program call":    {&good, func(s *learnedlogin.AuditedSend) { s.ModelCall = false }, learnedlogin.ReasonTransportUnverified},
		"no audit source": {nil, nil, learnedlogin.ReasonTransportUnverified},
	} {
		t.Run(name, func(t *testing.T) {
			runs, job := learnedRunDir(t, []learnedlogin.Row{row})
			ll := newProductLearnedLogin(r, runs, job, rec, "run-job1", creds, "/api/me")
			if name != "no audit source" {
				ll.setAudit(func(callRef, attemptID string) (learnedlogin.AuditedSend, bool) {
					if tc.send == nil || callRef != tc.send.CallRef || attemptID != tc.send.AttemptID {
						return learnedlogin.AuditedSend{}, false
					}
					s := *tc.send
					if tc.change != nil {
						tc.change(&s)
					}
					return s, true
				})
			}
			fake := &fakeLearnedAccounts{result: authenticatedResult(), contrast: server.E2HTTPLoginContrastResult{Sent: true, Status: 401}}
			ll.relogin(t.Context(), "tester-a", fake, fake)
			if _, _, reason := ll.plan("tester-a"); reason != tc.want {
				t.Fatalf("사유 %q, want %q", reason, tc.want)
			}
			if tc.want != "" && (len(fake.requests) != 0 || len(fake.legacy) != 0) {
				t.Fatalf("출처를 확인하지 못한 방법으로 보냄 learned=%d legacy=%d", len(fake.requests), len(fake.legacy))
			}
			if tc.want == "" && (len(fake.requests) != 1 || ll.provenance["tester-a"] != learnedlogin.ProvenanceE2Audited) {
				t.Fatalf("감사된 방법을 쓰지 않음 %d %v", len(fake.requests), ll.provenance)
			}
		})
	}
	explore := row
	explore.Stage, explore.TransportRef = learnedlogin.StageExploration, "17"
	runs, job := learnedRunDir(t, []learnedlogin.Row{explore})
	ll := newProductLearnedLogin(r, runs, job, rec, "run-job1", creds, "/api/me")
	if _, _, reason := ll.plan("tester-a"); reason != "" || ll.provenance["tester-a"] != learnedlogin.ProvenanceExplorationUnaudited {
		t.Fatalf("탐색 행 %q %v", reason, ll.provenance)
	}
}

func TestExplorationEvidenceControlsE3Reuse(t *testing.T) {
	r, _, rec := learnedAccountTarget(t)
	creds := map[string]string{"tester-a": credentialOf(r, rec.Target.ID, "tester-a")}
	row := modelSucceededRow("exploration-1", "tester-a", "/login")
	row.Stage, row.TransportRef = learnedlogin.StageExploration, "17"
	for name, mutate := range map[string]func(*learnedlogin.ExplorationEvidence){
		"matched":      nil,
		"other path":   func(e *learnedlogin.ExplorationEvidence) { e.Sends[0].Path = "/other" },
		"not received": func(e *learnedlogin.ExplorationEvidence) { e.Sends[0].ResponseReceived = false },
	} {
		t.Run(name, func(t *testing.T) {
			runs, job := learnedRunDir(t, []learnedlogin.Row{row})
			e := learnedlogin.ExplorationEvidence{Schema: learnedlogin.ExplorationEvidenceSchema, RunRef: "run-job1", SessionRef: "explore-job1",
				JobID: job, TargetRef: rec.Target.ID, TargetRevision: rec.Target.Revision, SourceDigest: strings.Repeat("a", 64),
				Sends: []learnedlogin.ExplorationSend{{Seq: 17, Method: "POST", Path: "/login", Status: 302, BoundaryForwarded: true,
					ApprovedTarget: true, ObservationForwarded: true, ResponseReceived: true}}}
			if mutate != nil {
				mutate(&e)
			}
			if err := learnedlogin.WriteExplorationEvidence(filepath.Join(runs, job, "state"), e); err != nil {
				t.Fatal(err)
			}
			ll := newProductLearnedLogin(r, runs, job, rec, "run-job1", creds, "/api/me")
			fake := &fakeLearnedAccounts{result: authenticatedResult(), contrast: server.E2HTTPLoginContrastResult{Sent: true, Status: 401}}
			ll.relogin(t.Context(), "tester-a", fake, fake)
			want := learnedlogin.Reason("")
			if mutate != nil {
				want = learnedlogin.ReasonTransportUnverified
			}
			if _, _, reason := ll.plan("tester-a"); reason != want {
				t.Fatalf("출처 대조 %q, want %q", reason, want)
			}
			if want == "" && (len(fake.requests) != 1 || ll.provenance["tester-a"] != learnedlogin.ProvenanceExplorationObserved) {
				t.Fatalf("경계 관찰 대조 이후 재사용 %d %v", len(fake.requests), ll.provenance)
			}
			if want != "" && (len(fake.requests) != 0 || len(fake.legacy) != 0) {
				t.Fatal("불일치 근거로 로그인 송신")
			}
		})
	}
	runs, job := learnedRunDir(t, []learnedlogin.Row{row})
	path := filepath.Join(runs, job, "state", learnedlogin.ExplorationEvidenceFile)
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	ll := newProductLearnedLogin(r, runs, job, rec, "run-job1", creds, "/api/me")
	if _, _, reason := ll.plan("tester-a"); reason != learnedlogin.ReasonTransportUnverified {
		t.Fatalf("손상된 근거 허용: %q", reason)
	}
}

// e2AuditLookup은 실제 E② 실행이 감사 기록기에 쓴 결과 사건에서 시도를 찾는다: 보낸 모델 요청은 메서드·상태·송신·감사
// 완료와 함께, 거부된 요청은 보내지 않음으로, 없는 참조는 찾지 못함으로 돌려준다.
func TestE2AuditLookupReadsAuditedResults(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusFound)
	}))
	defer target.Close()
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 8})
	if err != nil {
		t.Fatal(err)
	}
	grant := budget.Grant{Key: budget.Key{TaskRef: liveJob, BudgetRef: liveJob}, Binding: budget.Binding{PolicyRef: liveJob, ResetRef: "owned-reset"},
		MaxRequests: 8, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute)}
	if ledger.Register(grant, now) != nil {
		t.Fatal("예산 준비 실패")
	}
	execution, err := e2proxy.NewExecution(t.Context(), e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		Scope:    transmit.Scope{TaskRef: liveJob, RunRef: "run-" + liveJob, CaseRef: "owned-case", ResetRef: "owned-reset", PolicyRef: liveJob},
		IntentID: 1, OwnerRef: "ARTEX", PolicyRevision: "v1", AccountRef: "anonymous", SessionRef: "anonymous-session", ExpiresAt: grant.ValidUntil,
		Rules: []e2proxy.Rule{{Ref: "owned-rule", Origin: target.URL, ConnectIP: netip.MustParseAddr("127.0.0.1"), PathPrefix: "/", PathScopeRef: "owned",
			Methods: []string{"GET", "POST"}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: &e3TraceRecorder{},
		Limits: e2proxy.Limits{MaxAttempts: 8, MaxCalls: 4, MaxConcurrent: 1, MaxBodyBytes: 4096, RequestTimeout: time.Second, CallTimeout: time.Minute,
			MaxDenials: 4, MaxTransient: 4, MaxUnknown: 4}})
	if err != nil {
		t.Fatal(err)
	}
	call, err := execution.BeginCall(t.Context(), "model-login")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = call.Close(t.Context()) }()
	req, _ := http.NewRequest("POST", target.URL+"/login", strings.NewReader(`{"u":"a"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, sent, err := call.Do(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	denied, _ := http.NewRequest("PUT", target.URL+"/login", nil)
	_, refused, _ := call.Do(t.Context(), denied)
	lookup := e2AuditLookup(execution)
	got, ok := lookup(sent.CallRef, sent.AttemptID)
	if !ok || !got.ModelCall || got.Method != "POST" || got.StatusCode != http.StatusFound || !got.Allowed || !got.RequestWritten ||
		!got.ResponseReceived || !got.ResponseComplete || !got.AuditComplete || got.RunRef != "run-"+liveJob {
		t.Fatalf("보낸 요청 결과 %+v %v", got, ok)
	}
	row := learnedlogin.Row{Stage: learnedlogin.StageDiagnosis, RunRef: "run-" + liveJob, TransportRef: sent.CallRef + "/" + sent.AttemptID, Method: "POST", Status: http.StatusFound}
	if provenance, reason := learnedlogin.VerifyTransport(row, "run-"+liveJob, lookup); reason != "" || provenance != learnedlogin.ProvenanceE2Audited {
		t.Fatalf("실제 감사 사건과 대조 %v %v", provenance, reason)
	}
	if got, ok := lookup(refused.CallRef, refused.AttemptID); !ok || got.Allowed || got.RequestWritten {
		t.Fatalf("거부된 요청 %+v %v", got, ok)
	}
	if _, ok := lookup(sent.CallRef, "attempt-missing"); ok {
		t.Fatal("없는 참조를 찾음")
	}
}
