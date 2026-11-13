//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/diagnosisflow"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/server"
	"github.com/Autumn-27/artex/transmit"
)

const liveJob = "job-live-fixture"

// liveRunFixture는 실행 결속 디렉터리(runs/<job>)를 실제 형식으로 만든다: 승인 해시가 맞는 정책, 감사 키, 감사 기록.
func liveRunFixture(t *testing.T, events func(record func(e2proxy.Event))) (runsDir string, policy server.E2HTTPFilePolicy) {
	t.Helper()
	return liveRunFixtureWith(t, events, nil)
}

// liveRunFixtureWith는 실행 정책을 바꿔 쓸 수 있는 실행 디렉터리다(진단 워크플로 칸·승인 방식 등).
func liveRunFixtureWith(t *testing.T, events func(record func(e2proxy.Event)), change func(*server.E2HTTPFilePolicy)) (runsDir string, policy server.E2HTTPFilePolicy) {
	t.Helper()
	runsDir = t.TempDir()
	runDir := filepath.Join(runsDir, liveJob)
	state := filepath.Join(runDir, "state")
	for _, d := range []string{runsDir, runDir, state} {
		if err := os.MkdirAll(d, 0o700); err != nil || os.Chmod(d, 0o700) != nil {
			t.Fatal(err)
		}
	}
	key := []byte("live-fixture-audit-key-32-bytes!")
	sum := sha256.Sum256(key)
	if err := os.WriteFile(filepath.Join(runDir, "audit.key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	policy = server.E2HTTPFilePolicy{Format: server.E2HTTPFilePolicyFormat, ApprovalMode: e2proxy.ApprovalPreauthorizedScope, RunRef: "run-live", ResetRef: "reset-live", PolicyRef: "policy-live", CaseRef: "case-live",
		PolicyRevision: "revision-live", BudgetRef: "budget-live", OwnerRef: "ARTEX", AccountRef: "anonymous", SessionRef: "anonymous-session", ValidFrom: now, ValidUntil: now.Add(time.Hour), MaxRequests: 64,
		HeaderMode: e2proxy.HeaderTestAccountPassthrough, StateDir: state, Helper: "/usr/bin/true", HelperSHA256: strings.Repeat("a", 64),
		CACert: server.E2HTTPFilePin{File: "ca.pem", SHA256: strings.Repeat("b", 64)}, CAKey: server.E2HTTPFilePin{File: "ca-key.pem", SHA256: strings.Repeat("c", 64)},
		AuditKey: server.E2HTTPFilePin{File: "audit.key", SHA256: hex.EncodeToString(sum[:])},
		Rules: []server.E2HTTPFileRule{
			{Ref: "target-path-a", Origin: "https://shop.example", ConnectIP: "10.0.0.5", PathPrefix: "/api", PathScopeRef: "readonly-path-a", Methods: []string{"GET", "POST"}, TLSSPKISHA256: strings.Repeat("d", 64), ApprovalDisplay: server.RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}}},
			{Ref: "auth-login", Purpose: e2proxy.PurposeAuthPrep, Origin: "https://shop.example", ConnectIP: "10.0.0.5", PathPrefix: "/login", PathScopeRef: "auth-login-path", Methods: []string{"POST"}, ApprovalDisplay: server.RequestApprovalDisplay{QueryKeys: []string{}, JSONFields: []string{}}},
		},
		Auth:    &server.E2HTTPFileAuth{RuleRef: "auth-login", Login: diagnosisprep.TargetLogin{Method: "POST", Path: "/login", Format: "form", UsernameField: "email", PasswordField: "password", Cookies: []string{"session"}}, Accounts: []server.E2HTTPFileAuthAccount{{Label: "tester-a", Credential: server.E2HTTPFilePin{File: "account-secret-file.json", SHA256: strings.Repeat("e", 64)}}}},
		Limits:  server.E2HTTPFileLimits{MaxAttempts: 200, MaxCalls: 10, MaxConcurrent: 1, MaxBodyBytes: 65536, RequestTimeoutMS: 5000, CallTimeoutMS: 60000, MaxDenials: 8, MaxTransient: 4, MaxUnknown: 2},
		Proxy:   server.E2HTTPFileProxy{HeaderTimeoutMS: 5000, RequestTimeoutMS: 10000, MaxHeaderBytes: 16384, MaxConnections: 4},
		Runtime: server.E2HTTPFileRuntime{MaxWorkerTurns: 8, MaxToolCalls: 8, WorkerTimeoutMS: 60000, MaxPlannerRounds: 6, MaxIntentRuns: 1, DrainTimeoutMS: 10000, OutputLimit: 32768}}
	if change != nil {
		change(&policy)
	}
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(filepath.Join(runDir, "policy.json"), raw, 0o600) != nil {
		t.Fatal("policy write")
	}
	approval, _ := json.Marshal(server.FirstDiagnosisHTTPApproval{Format: server.FirstDiagnosisHTTPAuthorityFormat, ApprovalRef: "approval-live", OperatorUID: 1, TaskID: 9, ExplorationID: 3, PolicySHA256: urlSHA(raw), ValidFrom: now, ValidUntil: now.Add(time.Hour)})
	if err := os.WriteFile(filepath.Join(runDir, "authority.json"), approval, 0o600); err != nil {
		t.Fatal(err)
	}
	if events != nil {
		j, err := e2proxy.CreateJournal(state, "events.jsonl", key)
		if err != nil {
			t.Fatal(err)
		}
		index := uint64(0)
		events(func(e e2proxy.Event) {
			index++
			e.Index = index
			if err := j.Record(context.Background(), e); err != nil {
				t.Fatalf("record %d: %v", index, err)
			}
		})
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return runsDir, policy
}

func liveEvent(stage, call, attempt string, seq uint64, decision string, reason e2proxy.Reason) e2proxy.Event {
	e := e2proxy.Event{Stage: stage, Receipt: e2proxy.Receipt{Version: e2proxy.JournalVersion, IntentID: 4, Scope: transmit.Scope{TaskRef: "task-9", RunRef: "run-live", CaseRef: "case-live", ResetRef: "reset-live", PolicyRef: "policy-live"},
		PolicyRevision: "revision-live", CallRef: call, AttemptID: attempt, Sequence: seq, Method: "POST", RuleRef: "target-path-a", Decision: decision, Reason: reason, At: time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC)}}
	switch {
	case stage == "control" || stage == "call_closed":
		e.AttemptID, e.Sequence, e.Decision, e.Method, e.RuleRef = "", 0, "", "", ""
		if stage == "control" {
			e.CallRef = ""
		}
	case decision != "allowed":
		e.NotSent, e.RuleRef = true, ""
	case stage == "result" && reason == e2proxy.Complete:
		e.ForwardAttempted, e.RequestWritten, e.ResponseReceived, e.ResponseComplete, e.StatusCode = true, true, true, true, 200
	case stage == "result" && reason == e2proxy.Unknown:
		e.ForwardAttempted, e.RequestWritten = true, true
	}
	return e
}

func liveGet(t *testing.T, runsDir, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	view := liveDataView(strings.SplitN(path, "?", 2)[0])
	if view == "" {
		t.Fatalf("not a live data path: %s", path)
	}
	w := httptest.NewRecorder()
	serveLiveData(w, httptest.NewRequest(http.MethodGet, path, nil), runsDir, liveJob, view)
	var out map[string]any
	if w.Code == http.StatusOK && json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("response is not JSON: %s", w.Body.String())
	}
	return w, out
}

func TestLiveDataViewPaths(t *testing.T) {
	for path, want := range map[string]string{"/api/tasks/9/control-policy": "control-policy", "/api/tasks/9/e2-flow": "e2-flow", "/api/tasks/9/access-map": "", "/api/tasks/9/e2-flow/x": "", "/api/report/e2-flow": ""} {
		if got := liveDataView(path); got != want {
			t.Errorf("%s: %q", path, got)
		}
	}
}

// 통제 정책 투영은 승인 범위(origin·경로·메서드·상한·계정 라벨)만 보이고 키·자격·CA·helper·상태 디렉터리는 내보내지 않는다.
func TestControlPolicyViewProjectsScopeWithoutSecrets(t *testing.T) {
	runsDir, policy := liveRunFixture(t, nil)
	w, out := liveGet(t, runsDir, "/api/tasks/9/control-policy")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{"audit.key", "ca-key.pem", "ca.pem", "account-secret-file.json", "/usr/bin/true", policy.StateDir, strings.Repeat("e", 64), policy.AuditKey.SHA256} {
		if strings.Contains(body, secret) {
			t.Fatalf("control policy view leaked %q: %s", secret, body)
		}
	}
	rules := out["rules"].([]any)
	first, second := rules[0].(map[string]any), rules[1].(map[string]any)
	if out["format"] != controlPolicyViewFormat || out["approval_mode"] != "preauthorized_scope" || out["header_mode"] != "test_account_passthrough" || out["audit_bound"] != true ||
		first["tls_pinned"] != true || second["tls_pinned"] != false || second["purpose"] != "auth_prep" || first["path_prefix"] != "/api" ||
		fmt.Sprint(out["accounts"]) != "[tester-a]" || out["limits"].(map[string]any)["max_response_body_bytes"] != float64(65536) {
		t.Fatalf("control policy view: %s", body)
	}
}

// 전송 통제 흐름은 검증된 사건을 호출 단위로 묶고, Event.Index 커서로 이어 받는다(계약 §4).
func TestE2FlowViewGroupsVerifiedEventsWithCursor(t *testing.T) {
	a1, a2 := strings.Repeat("1", 32), strings.Repeat("2", 32)
	runsDir, _ := liveRunFixture(t, func(record func(e2proxy.Event)) {
		record(liveEvent("intent", "call-1", a1, 1, "allowed", e2proxy.Complete))
		record(liveEvent("result", "call-1", a1, 1, "allowed", e2proxy.Complete))
		record(liveEvent("control", "", "", 0, "", e2proxy.Closed))
		record(liveEvent("call_closed", "call-1", "", 0, "", e2proxy.Complete))
		record(liveEvent("result", "call-2", a2, 2, "denied", e2proxy.PolicyDenied))
	})
	w, out := liveGet(t, runsDir, "/api/tasks/9/e2-flow")
	if w.Code != http.StatusOK || out["journal"] != "verified" || out["last_index"] != float64(5) || out["more"] != false || out["omitted_calls"] != float64(0) {
		t.Fatal(w.Code, w.Body.String())
	}
	calls := out["calls"].([]any)
	c1, c2 := calls[0].(map[string]any), calls[1].(map[string]any)
	row := c1["attempts"].([]any)[0].(map[string]any)
	if len(calls) != 2 || c1["closed"] != true || c1["closed_index"] != float64(4) || len(c1["attempts"].([]any)) != 1 || row["index"] != float64(2) || row["stage"] != "result" || row["status_code"] != float64(200) ||
		c2["closed"] != false || c2["attempts"].([]any)[0].(map[string]any)["reason"] != "policy_denied" {
		t.Fatalf("grouping: %s", w.Body.String())
	}
	counts := out["counts"].(map[string]any)
	if counts["attempts"] != float64(2) || counts["allowed"] != float64(1) || counts["denied"] != float64(1) || counts["calls"] != float64(2) || counts["responses"] != float64(1) {
		t.Fatalf("counts: %v", counts)
	}
	// 이어 받기: after 뒤 사건만, 커서는 넣은 마지막 사건 번호, 없으면 그대로.
	w, out = liveGet(t, runsDir, "/api/tasks/9/e2-flow?after=3")
	if w.Code != http.StatusOK || out["last_index"] != float64(5) || len(out["calls"].([]any)) != 2 || len(out["calls"].([]any)[0].(map[string]any)["attempts"].([]any)) != 0 {
		t.Fatalf("after=3: %s", w.Body.String())
	}
	w, out = liveGet(t, runsDir, "/api/tasks/9/e2-flow?after=5")
	if w.Code != http.StatusOK || out["last_index"] != float64(5) || len(out["calls"].([]any)) != 0 || out["more"] != false {
		t.Fatalf("after=5: %s", w.Body.String())
	}
}

// 기록·키가 틀리면 사건을 하나도 보이지 않는다. 실행 결속·기록이 없으면 404, 정책이 승인과 다르면 409, 읽기만 받는다.
func TestE2FlowViewFailsClosed(t *testing.T) {
	a1 := strings.Repeat("1", 32)
	write := func(record func(e2proxy.Event)) {
		record(liveEvent("result", "call-1", a1, 1, "allowed", e2proxy.Complete))
	}
	runsDir, _ := liveRunFixture(t, write)
	journal := filepath.Join(runsDir, liveJob, "state", "events.jsonl")
	raw, _ := os.ReadFile(journal)
	if err := os.WriteFile(journal, []byte(strings.Replace(string(raw), `"status_code":200`, `"status_code":201`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if w, out := liveGet(t, runsDir, "/api/tasks/9/e2-flow"); w.Code != http.StatusOK || out["journal"] != "invalid" || len(out["calls"].([]any)) != 0 {
		t.Fatalf("tampered journal: %d %s", w.Code, w.Body.String())
	}
	runsDir, _ = liveRunFixture(t, write)
	if err := os.WriteFile(filepath.Join(runsDir, liveJob, "audit.key"), []byte("another-audit-key-of-32-bytes!!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w, out := liveGet(t, runsDir, "/api/tasks/9/e2-flow"); w.Code != http.StatusOK || out["journal"] != "invalid" {
		t.Fatalf("key differs from pin: %d %s", w.Code, w.Body.String())
	}
	noJournal, _ := liveRunFixture(t, nil)
	if w, _ := liveGet(t, noJournal, "/api/tasks/9/e2-flow"); w.Code != http.StatusNotFound {
		t.Fatalf("missing journal: %d", w.Code)
	}
	if w, _ := liveGet(t, t.TempDir(), "/api/tasks/9/control-policy"); w.Code != http.StatusNotFound {
		t.Fatalf("missing run: %d", w.Code)
	}
	changed, _ := liveRunFixture(t, nil)
	if err := os.WriteFile(filepath.Join(changed, liveJob, "policy.json"), []byte(`{"format":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if w, _ := liveGet(t, changed, "/api/tasks/9/control-policy"); w.Code != http.StatusConflict {
		t.Fatalf("policy differs from approval: %d", w.Code)
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{{http.MethodPost, "/api/tasks/9/control-policy", http.StatusMethodNotAllowed}, {http.MethodGet, "/api/tasks/9/e2-flow?after=-1", http.StatusBadRequest},
		{http.MethodGet, "/api/tasks/9/e2-flow?after=01", http.StatusBadRequest}, {http.MethodGet, "/api/tasks/9/control-policy?after=1", http.StatusBadRequest}} {
		w := httptest.NewRecorder()
		serveLiveData(w, httptest.NewRequest(tc.method, tc.path, nil), noJournal, liveJob, liveDataView(strings.Split(tc.path, "?")[0]))
		if w.Code != tc.want {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}

// 처음 조회는 최근 호출 200개만 주고 나머지 수를 알린다. 이어 받기는 사건 1000개씩 주고 남으면 more다.
func TestE2FlowTailAndPageLimits(t *testing.T) {
	var events []e2proxy.Event
	for i := 0; i < e2FlowTailCalls+5; i++ {
		e := liveEvent("call_closed", fmt.Sprintf("call-%03d", i), "", 0, "", e2proxy.Complete)
		e.Index = uint64(len(events) + 1)
		events = append(events, e)
	}
	tail := e2FlowOf(events, nil)
	if len(tail.Calls) != e2FlowTailCalls || tail.OmittedCalls != 5 || tail.Calls[0].CallRef != "call-005" || tail.LastIndex != uint64(len(events)) {
		t.Fatalf("tail: calls=%d omitted=%d first=%s last=%d", len(tail.Calls), tail.OmittedCalls, tail.Calls[0].CallRef, tail.LastIndex)
	}
	for len(events) < e2FlowPageEvents+10 {
		e := liveEvent("control", "", "", 0, "", e2proxy.Closed)
		e.Index = uint64(len(events) + 1)
		events = append(events, e)
	}
	zero := uint64(0)
	page := e2FlowOf(events, &zero)
	if !page.More || page.LastIndex != e2FlowPageEvents || page.OmittedCalls != 0 {
		t.Fatalf("page: more=%v last=%d", page.More, page.LastIndex)
	}
	next := e2FlowOf(events, &page.LastIndex)
	if next.More || next.LastIndex != uint64(len(events)) {
		t.Fatalf("next page: more=%v last=%d", next.More, next.LastIndex)
	}
}

type liveAuthEndpoint struct{ job diagnosisprep.Job }

func (e liveAuthEndpoint) Call(_ context.Context, r diagnosisprep.Request) (diagnosisprep.Response, error) {
	ok := r.Command == diagnosisprep.AuthorizeTask && r.TaskID == e.job.TaskID
	return diagnosisprep.Response{Authorized: &ok, Job: &e.job}, nil
}

// task 경로는 작업 결속을 확인한 뒤 그 작업의 실행 결속 디렉터리에서 실데이터를 읽는다. 결속이 다르면 읽지 않는다.
func TestTaskRouteServesLiveDataAfterBinding(t *testing.T) {
	runsDir, _ := liveRunFixture(t, nil)
	b := &urlPreparationRuntime{config: urlPreparationInstallation{RunsDir: runsDir}, endpoint: liveAuthEndpoint{job: diagnosisprep.Job{ID: liveJob, TaskID: "9"}},
		live: map[string]*urlLiveHandler{}, reports: map[string]*savedReportService{}}
	w := httptest.NewRecorder()
	b.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/tasks/9/control-policy", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), controlPolicyViewFormat) {
		t.Fatalf("bound task: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	b.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/tasks/10/control-policy", nil))
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), controlPolicyViewFormat) {
		t.Fatalf("unbound task: %d %s", w.Code, w.Body.String())
	}
}

// 제품 기본 경로(AI 자동 워크플로)와 작업대 승인 방식 실행의 정책에도 작업대 조회가 열린다. 평가용 검증기를 쓰면
// workflow 칸 때문에 늘 409였다(11/13 실제 점검). 지문은 실행 factory와 같은 내용 지문이다.
func TestControlPolicyViewReadsWorkflowRunPolicies(t *testing.T) {
	for name, change := range map[string]func(*server.E2HTTPFilePolicy){
		"ai_auto": func(p *server.E2HTTPFilePolicy) {
			p.Workflow = &server.E2HTTPWorkflowPolicy{Format: diagnosisflow.Version, Mode: "ai_auto_v1", InputSHA256: strings.Repeat("f", 64)}
		},
		"workbench": func(p *server.E2HTTPFilePolicy) {
			p.ApprovalMode = e2proxy.ApprovalWorkbenchV1
			p.Workflow = &server.E2HTTPWorkflowPolicy{Format: diagnosisflow.Version, Mode: "manual_v1", InputSHA256: strings.Repeat("f", 64)}
		},
		"confirm_every_request": func(p *server.E2HTTPFilePolicy) { p.ApprovalMode = e2proxy.ApprovalConfirmEveryRequest },
	} {
		t.Run(name, func(t *testing.T) {
			runsDir, policy := liveRunFixtureWith(t, nil, change)
			w, out := liveGet(t, runsDir, "/api/tasks/9/control-policy")
			if w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			raw, _ := json.Marshal(policy)
			_, want, err := server.ValidateE2HTTPRunPolicyDocument(raw)
			if err != nil || out["approval_mode"] != string(policy.ApprovalMode) || out["policy_fingerprint_sha256"] != want || want == "" {
				t.Fatalf("workflow run policy view: %v %s", err, w.Body.String())
			}
			if f, _ := liveGet(t, runsDir, "/api/tasks/9/e2-flow"); f.Code == http.StatusConflict {
				t.Fatal("e2-flow rejected the workflow run policy")
			}
		})
	}
}

// 자격 pin 규칙이 있는 정책은 pin 파일 없이 실행과 같은 지문을 낼 수 없어 지문을 비운다(다른 칸은 그대로 보인다).
func TestControlPolicyViewOmitsFingerprintWithCredentialPins(t *testing.T) {
	runsDir, _ := liveRunFixtureWith(t, nil, func(p *server.E2HTTPFilePolicy) {
		p.HeaderMode = e2proxy.HeaderProtectedSessionProposal
		p.Auth = nil
		p.Rules = p.Rules[:1]
		p.Rules[0].Credentials = &server.E2HTTPFilePin{File: "session-pin.json", SHA256: strings.Repeat("9", 64)}
	})
	w, out := liveGet(t, runsDir, "/api/tasks/9/control-policy")
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, ok := out["policy_fingerprint_sha256"]; ok || strings.Contains(w.Body.String(), "session-pin.json") {
		t.Fatalf("credential-pinned policy exposed a fingerprint or pin file: %s", w.Body.String())
	}
}
