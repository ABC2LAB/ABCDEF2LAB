//go:build linux

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/loginattempt"
	"github.com/Autumn-27/artex/server"
	"github.com/Autumn-27/artex/transmit"
	"github.com/Autumn-27/artex/transmit/budget"
)

// loginRow는 판정기가 남기는 거절 행 모양(합성 아이디·경로, 비밀번호 칸 없음)이다.
func loginRow(runRef, stage, transportRef, refusal string) string {
	row := loginattempt.Row{Schema: loginattempt.Schema, AttemptRef: "login-" + strings.ReplaceAll(transportRef, "/", "-"), RunRef: runRef, Stage: stage,
		Actor: loginattempt.ActorModel, TransportRef: transportRef, At: "2026-11-27T01:02:03Z", Identifier: "tester@owned.test", PasswordRegistered: true,
		Method: "POST", Path: "/login", Transport: loginattempt.TransportNotSent, Refusal: refusal, Outcome: loginattempt.OutcomeNotSent, ConsecutiveFailures: 3}
	raw, _ := json.Marshal(row)
	return string(raw)
}

// 로그인 상한·이력 인계 실패로 보내지 않은 시도는 E②에서 policy_denied로 남고, 화면은 로그인 시도 기록의 같은
// transport_ref(call_ref/attempt_id) 행이 하나일 때만 login_refusal로 상세 사유를 받는다(계약 §4.2). 다른 회차·탐색 단계·
// 중복·모르는 사유·실제로 보낸 시도·policy_denied가 아닌 거부에는 붙지 않는다. 기록이 없으면 칸이 없다(기존 응답 그대로).
func TestE2FlowLoginRefusalBindsExactTransportRef(t *testing.T) {
	exhausted, history, dup, other, allowed, scope := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32),
		strings.Repeat("4", 32), strings.Repeat("5", 32), strings.Repeat("6", 32)
	program, get, trailing := strings.Repeat("7", 32), strings.Repeat("8", 32), strings.Repeat("9", 32)
	wrongMethod := strings.Repeat("a", 32)
	runsDir, policy := liveRunFixture(t, func(record func(e2proxy.Event)) {
		record(liveEvent("result", "call-login", exhausted, 1, "denied", e2proxy.PolicyDenied))
		record(liveEvent("result", "call-login", history, 2, "denied", e2proxy.PolicyDenied))
		record(liveEvent("result", "call-login", dup, 3, "denied", e2proxy.PolicyDenied))
		record(liveEvent("result", "call-login", other, 4, "denied", e2proxy.PolicyDenied))
		record(liveEvent("result", "call-login", allowed, 5, "allowed", e2proxy.Complete))
		record(liveEvent("result", "call-login", scope, 6, "denied", e2proxy.BudgetExhausted))
		programEvent := liveEvent("result", "call-login", program, 7, "denied", e2proxy.PolicyDenied)
		programEvent.Program = e2proxy.ProgramAuthPrep
		programEvent.IntentID = 0
		record(programEvent)
		getEvent := liveEvent("result", "call-login", get, 8, "denied", e2proxy.PolicyDenied)
		getEvent.Method = "GET"
		record(getEvent)
		record(liveEvent("result", "call-login", trailing, 9, "denied", e2proxy.PolicyDenied))
		record(liveEvent("result", "call-login", wrongMethod, 10, "denied", e2proxy.PolicyDenied))
	})
	refusals := func() map[string]string {
		w, out := liveGet(t, runsDir, "/api/tasks/9/e2-flow")
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "tester@owned.test") || strings.Contains(w.Body.String(), "/login") {
			t.Fatal("흐름 응답에 로그인 기록의 아이디·경로가 섞임")
		}
		got := map[string]string{}
		for _, raw := range out["calls"].([]any)[0].(map[string]any)["attempts"].([]any) {
			a := raw.(map[string]any)
			if v, ok := a["login_refusal"]; ok {
				got[a["attempt_id"].(string)] = v.(string)
			}
		}
		return got
	}
	if got := refusals(); len(got) != 0 {
		t.Fatalf("기록 없이 사유를 만듦: %v", got)
	}
	// 실제 판정기의 Prepare가 남긴 행(null 배열 포함)을 화면 판독기에 넣는다.
	origin, err := url.Parse("https://shop.example")
	if err != nil {
		t.Fatal(err)
	}
	var emitted loginattempt.Row
	tracker, err := loginattempt.New(loginattempt.Config{RunRef: "run-live", Stage: loginattempt.StageDiagnosis, Actor: loginattempt.ActorModel,
		Origin: origin, Accounts: []loginattempt.Account{{Label: "tester-a", Username: "tester@owned.test", Password: "test-pass"}},
		MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16, HistoryUnavailable: true,
		Sink: func(row loginattempt.Row) { emitted = row }})
	if err != nil {
		t.Fatal(err)
	}
	plan := tracker.Prepare(loginattempt.Request{TransportRef: "call-login/" + history, Method: "POST", Path: "/login",
		ContentType: "application/x-www-form-urlencoded", Body: []byte("email=tester%40owned.test&password=test-pass"), Header: http.Header{}}, true)
	if plan.Refusal != loginattempt.RefusalHistoryUnavailable || emitted.TransportRef != "call-login/"+history {
		t.Fatalf("판정기가 예상한 거절 행을 남기지 않음: %s %+v", plan.Refusal, emitted)
	}
	emittedJSON, err := json.Marshal(emitted)
	if err != nil {
		t.Fatal(err)
	}
	rows := []string{
		loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+exhausted, loginattempt.RefusalExhausted),
		string(emittedJSON),
		loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+dup, loginattempt.RefusalExhausted),
		loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+dup, loginattempt.RefusalHistoryUnavailable),
		loginRow("run-other", loginattempt.StageDiagnosis, "call-login/"+other, loginattempt.RefusalExhausted),
		loginRow("run-live", loginattempt.StageExploration, "call-login/"+other, loginattempt.RefusalExhausted),
		loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+allowed, loginattempt.RefusalExhausted),
		loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+scope, loginattempt.RefusalExhausted),
		loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+other, "scope_denied"),
		loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+program, loginattempt.RefusalExhausted),
		loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+get, loginattempt.RefusalExhausted),
		loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+trailing, loginattempt.RefusalExhausted) + ` {"extra":true}`,
		strings.Replace(loginRow("run-live", loginattempt.StageDiagnosis, "call-login/"+wrongMethod, loginattempt.RefusalExhausted),
			`"method":"POST"`, `"method":"GET"`, 1),
		`not json`,
	}
	if err := os.WriteFile(filepath.Join(policy.StateDir, server.E2HTTPLoginAttemptsFile), []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := refusals()
	if len(got) != 2 || got[exhausted] != loginattempt.RefusalExhausted || got[history] != loginattempt.RefusalHistoryUnavailable {
		t.Fatalf("흐름 시도 로그인 사유: %v", got)
	}
	// 요청 상세도 같은 결속·같은 사유다. E② 사유(policy_denied)는 바뀌지 않는다.
	for attempt, want := range map[string]string{exhausted: loginattempt.RefusalExhausted, history: loginattempt.RefusalHistoryUnavailable,
		dup: "", allowed: "", scope: "", program: "", get: "", trailing: "", wrongMethod: ""} {
		var detail e2RequestDetail
		w := requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests/call-login/"+attempt)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.LoginRefusal != want {
			t.Fatalf("상세 %s: %d %+v", attempt, w.Code, detail)
		}
		if want != "" && (detail.Reason != string(e2proxy.PolicyDenied) || detail.Decision != "denied" || detail.ForwardAttempted) {
			t.Fatalf("E② 결과가 바뀜: %+v", detail)
		}
		if strings.Contains(w.Body.String(), "tester@owned.test") || strings.Contains(w.Body.String(), "/login") {
			t.Fatalf("로그인 기록의 아이디·경로가 응답에 섞임: %s", w.Body.String())
		}
	}
}

// 실제 판정기와 E② 실행이 생성한 행·서명 저널을 같은 회차 작업대 API가 연결한다. 대상에는 요청을 보내지 않는다.
func TestE2FlowLoginRefusalFromExecution(t *testing.T) {
	runsDir, policy := liveRunFixture(t, nil)
	key, err := os.ReadFile(filepath.Join(filepath.Dir(policy.StateDir), policy.AuditKey.File))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := e2proxy.CreateScreenJournal(policy.StateDir, "events.jsonl", key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ledger, err := budget.New(budget.Limits{MaxApprovals: 1, MaxRequestsPerApproval: 8})
	if err != nil {
		t.Fatal(err)
	}
	grant := budget.Grant{Key: budget.Key{TaskRef: "task-9", BudgetRef: "budget-live"},
		Binding: budget.Binding{PolicyRef: policy.PolicyRef, ResetRef: policy.ResetRef}, MaxRequests: 8,
		ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour)}
	if err := ledger.Register(grant, now); err != nil {
		t.Fatal(err)
	}
	execution, err := e2proxy.NewExecution(t.Context(), e2proxy.Config{ApprovalMode: e2proxy.ApprovalPreauthorizedScope,
		Scope:    transmit.Scope{TaskRef: "task-9", RunRef: policy.RunRef, CaseRef: policy.CaseRef, ResetRef: policy.ResetRef, PolicyRef: policy.PolicyRef},
		IntentID: 4, OwnerRef: "ARTEX", PolicyRevision: policy.PolicyRevision, AccountRef: "anonymous", SessionRef: "anonymous-session",
		ExpiresAt: now.Add(time.Hour), Rules: []e2proxy.Rule{{Ref: "login-model", Origin: "https://shop.example", ConnectIP: netip.MustParseAddr("10.0.0.5"),
			PathPrefix: "/login", PathScopeRef: "login-path", Methods: []string{"POST"}}},
		Budget: transmit.CumulativeBudget{Ledger: ledger, Key: grant.Key, Binding: grant.Binding}, Recorder: journal,
		Limits: e2proxy.Limits{MaxAttempts: 8, MaxCalls: 2, MaxConcurrent: 1, MaxBodyBytes: 1024, RequestTimeout: time.Second,
			CallTimeout: time.Minute, MaxDenials: 8, MaxTransient: 1, MaxUnknown: 1}})
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse("https://shop.example")
	tracker, err := loginattempt.New(loginattempt.Config{RunRef: policy.RunRef, Stage: loginattempt.StageDiagnosis, Origin: origin,
		Accounts:               []loginattempt.Account{{Label: "tester-a", Username: "tester@owned.test", Password: "test-pass"}},
		MaxConsecutiveFailures: 3, MaxContrastProbes: 3, TokenMinBytes: 16, HistoryUnavailable: true,
		Sink: func(row loginattempt.Row) {
			line, marshalErr := json.Marshal(row)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			f, openErr := os.OpenFile(filepath.Join(policy.StateDir, server.E2HTTPLoginAttemptsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if openErr != nil {
				t.Fatal(openErr)
			}
			if _, writeErr := f.Write(append(line, '\n')); writeErr != nil {
				t.Fatal(writeErr)
			}
			if closeErr := f.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	execution.AttachLoginAttempts(tracker)
	call, err := execution.BeginCall(t.Context(), "login-check")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://shop.example/login", strings.NewReader("email=tester%40owned.test&password=test-pass"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, receipt, sendErr := call.Do(t.Context(), req)
	if sendErr == nil || resp != nil || receipt.Decision != "denied" || receipt.Reason != e2proxy.PolicyDenied || receipt.ForwardAttempted {
		t.Fatalf("실제 E② 거절 영수증: %+v %v", receipt, sendErr)
	}
	if _, err := call.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	w, flow := liveGet(t, runsDir, "/api/tasks/9/e2-flow")
	if w.Code != http.StatusOK || flow["journal"] != "verified" {
		t.Fatalf("실행 저널 조회: %d %s", w.Code, w.Body.String())
	}
	row := flow["calls"].([]any)[0].(map[string]any)["attempts"].([]any)[0].(map[string]any)
	if row["login_refusal"] != loginattempt.RefusalHistoryUnavailable || row["reason"] != string(e2proxy.PolicyDenied) ||
		row["attempt_id"] != receipt.AttemptID {
		t.Fatalf("실행 시도 사유 결속: %v", row)
	}
	detail := requestDetailsGet(t, runsDir, "/api/tasks/9/e2-flow/requests/"+receipt.CallRef+"/"+receipt.AttemptID)
	var parsed e2RequestDetail
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &parsed) != nil || parsed.LoginRefusal != loginattempt.RefusalHistoryUnavailable {
		t.Fatalf("실행 상세 사유 결속: %d %s", detail.Code, detail.Body.String())
	}
	for _, raw := range []string{w.Body.String(), detail.Body.String()} {
		if strings.Contains(raw, "tester@owned.test") || strings.Contains(raw, "test-pass") || strings.Contains(raw, "/login") {
			t.Fatal("로그인 기록의 아이디·비밀번호·경로가 작업대 응답에 섞임")
		}
	}
}
