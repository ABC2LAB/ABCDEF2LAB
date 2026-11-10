//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// 합성 모델은 명령만 제안한다. 관찰·승인·후보·보고서는 실제 제품 경로에서 생성한다.
type combinedDiagnosisProvider struct {
	firstDiagnosisE2Provider
	submitted bool
}

func (p *combinedDiagnosisProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	msg, stop, usage, err := p.firstDiagnosisE2Provider.Complete(ctx, req)
	if err != nil || p.result == nil || p.submitted {
		return msg, stop, usage, err
	}
	p.submitted = true
	raw, err := json.Marshal(map[string]any{
		"call_ref": p.result.Trusted.Call.CallRef, "vulnclass": "access_control",
		"name": "수동 검토 후보", "summary": "실제 허용 및 거부 관찰을 수동 검토합니다.",
		"remediation": "접근 권한과 거부 경계를 확인하세요.",
	})
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "combined-candidate", Name: "submit_diagnostic_candidate", Input: raw}}}, "tool_use", llm.Usage{}, err
}

type combinedHTTPClient struct {
	t      *testing.T
	server *httptest.Server
	token  string
}

func (c *combinedHTTPClient) request(method, path, body string) (int, []byte, http.Header) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.server.URL+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.server.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		c.t.Fatal(err)
	}
	return res.StatusCode, raw, res.Header
}
func (c *combinedHTTPClient) login() {
	c.t.Helper()
	code, raw, _ := c.request("POST", "/api/auth/login", `{"username":"ARTEX","password":"synthetic-owned-combined-fixture"}`)
	var session struct{ Token string }
	if code != 200 || json.Unmarshal(raw, &session) != nil || session.Token == "" {
		c.t.Fatalf("실제 로그인 실패: %d", code)
	}
	c.token = session.Token
}

func TestFirstDiagnosisCombinedApprovalCandidateStoredReportOwnedPostgres(t *testing.T) {
	runFirstDiagnosisCombinedAcceptance(t, false)
}

func runFirstDiagnosisCombinedAcceptance(t *testing.T, browser bool) {
	helper := firstDiagnosisNativeHelper(t)
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	timeout := time.Minute
	if browser {
		timeout = 8 * time.Minute
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	logRoot := firstDiagnosisPrivateTemp(t)
	if retained := os.Getenv("ARTEX_DIAGNOSTIC_ACCEPTANCE_DIR"); retained != "" {
		logRoot = retained
	}
	logger, err := diagnosticlog.Create(logRoot, "run", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	ctx = diagnosticlog.With(ctx, logger)
	manager, err := NewFirstDiagnosisManager(ctx, t.TempDir(), f.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	task, err := manager.CreateTask("합성 첫 진단 통합 시험", "허용 및 거부 관찰 후 저장 보고서 확인", nil, 60, 60)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = installer.Exec("UPDATE tasks SET paused=true,queued=false WHERE id=$1", task.ID); err != nil {
		t.Fatal(err)
	}
	task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = true; s.Queued = false })
	if _, err = task.Store.AddIntent(map[string]any{"action": "Inspect the owned synthetic HTTP fixture through Bash and preserve observations."}, 1, nil, "synthetic acceptance"); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-owned-combined-fixture"), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("인증 fixture 준비 실패")
	}
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/allowed" {
			t.Error("거부한 요청이 대상에 도달함")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"owned_fixture":true}`)
	}))
	defer target.Close()
	var validity []time.Duration
	if browser {
		validity = []time.Duration{8 * time.Minute}
	}
	cfg, grant, policy := e2HostPolicyFixture(t, validity...)
	grant.TaskID = mustFirstDiagnosisTaskNumber(task.ID)
	grant.ExplorationID = task.ExpID
	binary, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	policy.Helper = helper
	policy.HelperSHA256 = hex.EncodeToString(digest[:])
	policy.StateDir = filepath.Join(firstDiagnosisPrivateTemp(t), "state")
	if os.Mkdir(policy.StateDir, 0700) != nil {
		t.Fatal("socket 상태 준비 실패")
	}
	policy.OwnerRef = "ARTEX"
	policy.ApprovalMode = e2proxy.ApprovalConfirmEveryRequest
	policy.Rules = []E2HTTPFileRule{{Ref: "combined-rule", Origin: target.URL, ConnectIP: "127.0.0.1", PathPrefix: "/", PathScopeRef: "combined-path", Methods: []string{"GET"}}}
	policy.Rules[0].ApprovalDisplay.PublicPath = true
	policy.Limits.CallTimeoutMS = 20000
	policy.Limits.RequestTimeoutMS = 15000
	policy.Proxy.RequestTimeoutMS = 15000
	policy.Runtime.WorkerTimeoutMS = 30000
	policy.Runtime.MaxWorkerTurns = 6
	policy.Runtime.MaxPlannerRounds = 4
	if browser {
		policy.Limits.CallTimeoutMS = 90000
		policy.Limits.RequestTimeoutMS = 60000
		policy.Proxy.RequestTimeoutMS = 60000
		policy.Runtime.WorkerTimeoutMS = 120000
	}
	raw, err := json.Marshal(policy)
	if err != nil || os.WriteFile(cfg.PolicyFile, raw, 0600) != nil {
		t.Fatal("정책 준비 실패")
	}
	digest = sha256.Sum256(raw)
	grant.PolicySHA256 = hex.EncodeToString(digest[:])
	e2HostWriteApproval(t, cfg, grant)
	skillDir, dataDir, keyDir := firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t), firstDiagnosisPrivateTemp(t)
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-owned-combined-jwt-material"), 0600) != nil {
		t.Fatal("JWT 준비 실패")
	}
	manifest := firstDiagnosisSupervisorModelManifest(t, installer)
	profileID := firstDiagnosisPinFixtureTaskProfile(t, installer, task)
	supervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := StartFirstDiagnosisHTTP(ctx, manager, FirstDiagnosisHTTPStartOptions{Host: cfg, SkillDir: skillDir, DataDir: dataDir, KeyDir: keyDir, LocalLLMManifestPath: manifest, Supervisor: supervisor, Shutdown: supervisor.Shutdown(), CleanupTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal("실제 HTTP 생성자 실패", err)
	}
	defer runtime.Close(context.Background())
	worker := &combinedDiagnosisProvider{firstDiagnosisE2Provider: firstDiagnosisE2Provider{command: "curl --silent --show-error --max-time 15 " + target.URL + "/allowed; curl --silent --show-error --max-time 15 " + target.URL + "/denied"}}
	planner := &firstDiagnosisPlannerProbeProvider{textOnly: true}
	// 합성 공급자는 원문·선택 usage DB 기록 없이 실제 메타데이터 wrapper만 통과한다.
	firstDiagnosisInjectProfileProvider(t, runtime.server, profileID, llmrec.Wrap(&firstDiagnosisRoleRoutedProvider{worker: worker, planner: planner}, nil, "local:synthetic", "", "", "", func() bool { return false }))
	api := httptest.NewServer(runtime.Handler())
	defer api.Close()
	client := &combinedHTTPClient{t: t, server: api}
	client.login()
	var ui *firstDiagnosisBrowserFixture
	if browser {
		ui = startFirstDiagnosisBrowserFixture(t, ctx, api.URL, task.ID)
		ui.run("prepared")
	}
	approvalBase := "/api/tasks/" + task.ID + "/request-approvals"
	code, preparedRaw, _ := client.request("GET", approvalBase, "")
	var prepared struct{ Run requestApprovalRunState }
	if code != 200 || json.Unmarshal(preparedRaw, &prepared) != nil || prepared.Run.Phase != "prepared" || prepared.Run.DrainComplete {
		t.Fatalf("실행 전 준비 상태 누락: status=%d phase=%s", code, prepared.Run.Phase)
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, task.ID, 0, "") }()
	var browserDone chan error
	if browser {
		browserDone = make(chan error, 1)
		go func() { browserDone <- ui.command("approvals") }()
	}
	decisions := map[string]string{}
	runWait := 35 * time.Second
	if browser {
		runWait = 120 * time.Second
	}
	deadline := time.NewTimer(runWait)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
running:
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("실제 감독 실행 실패", err)
			}
			break running
		case browserErr := <-browserDone:
			if browserErr != nil {
				t.Fatal("실브라우저 승인 시험 실패", browserErr)
			}
			browserDone = nil
		case <-deadline.C:
			t.Fatal("승인 대기 실행 시간 초과")
		case <-ticker.C:
			code, body, _ := client.request("GET", approvalBase, "")
			if code == 403 {
				continue
			}
			var list struct{ Approvals []db.RequestApproval }
			if code != 200 || json.Unmarshal(body, &list) != nil {
				t.Fatalf("승인 목록 실패: %d", code)
			}
			for _, a := range list.Approvals {
				if browser {
					continue
				}
				if a.State != "pending" || decisions[a.ID] != "" {
					continue
				}
				decision := "deny"
				if a.Path == "/allowed" {
					decision = "allow"
					if len(decisions) != 0 || hits.Load() != 0 {
						t.Fatal("승인 전에 대상 송신")
					}
				} else if a.Path != "/denied" || len(decisions) != 1 || hits.Load() != 1 {
					t.Fatal("허용 한 건만 송신해야 함")
				}
				body, _ := json.Marshal(map[string]string{"decision": decision, "request_sha256": a.RequestSHA256})
				code, _, _ = client.request("POST", approvalBase+"/"+a.ID+"/decision", string(body))
				if code != 200 {
					t.Fatalf("실제 승인 API 실패: %d", code)
				}
				decisions[a.ID] = decision
			}
		}
	}
	if browser {
		if browserDone != nil {
			if err := <-browserDone; err != nil {
				t.Fatal("실브라우저 승인 시험 실패", err)
			}
		}
		code, b, _ := client.request("GET", approvalBase, "")
		var state struct{ Approvals []db.RequestApproval }
		if code != 200 || json.Unmarshal(b, &state) != nil {
			t.Fatal("브라우저 결정 저장 조회 실패")
		}
		for _, a := range state.Approvals {
			if a.State == "allowed" {
				decisions[a.ID] = "allow"
			}
			if a.State == "denied" {
				decisions[a.ID] = "deny"
			}
		}
	}
	if len(decisions) != 2 || hits.Load() != 1 || !worker.submitted || planner.calls == 0 {
		t.Fatalf("실제 통합 경로 누락: 결정=%d 대상=%d 후보=%v Planner=%d", len(decisions), hits.Load(), worker.submitted, planner.calls)
	}
	code, approvalRaw, _ := client.request("GET", approvalBase, "")
	var persisted struct {
		Approvals []db.RequestApproval
		Run       requestApprovalRunState
	}
	if code != 200 || json.Unmarshal(approvalRaw, &persisted) != nil || len(persisted.Approvals) != 2 {
		t.Fatal("승인 저장 결과 누락")
	}
	if persisted.Run.Phase != "closed" || persisted.Run.Outcome != "completed" || !persisted.Run.DrainComplete || persisted.Run.ClosureFailed {
		t.Fatal("실행 후 실제 종료 상태 누락")
	}
	for _, a := range persisted.Approvals {
		if decisions[a.ID] == "allow" && (!a.Consumed || a.ExecutionState != "completed") {
			t.Fatal("허용 승인 소비와 실제 전송 완료 누락")
		}
		if decisions[a.ID] == "allow" {
			duplicate, _ := json.Marshal(map[string]string{"decision": "allow", "request_sha256": a.RequestSHA256})
			if status, _, _ := client.request("POST", approvalBase+"/"+a.ID+"/decision", string(duplicate)); status != 403 {
				t.Fatalf("종료된 실행 권한의 재사용 거부 상태=%d", status)
			}
		}
		if decisions[a.ID] == "deny" && (a.Consumed || a.State != "denied") {
			t.Fatal("거부 승인 상태가 실행으로 변함")
		}
	}
	candidates, err := task.Store.ListDiagnosticCandidates(grant.TaskID, "", 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("실제 도구 후보 저장 실패: 건수=%d 오류=%v", len(candidates), err)
	}
	candidate := candidates[0]
	if candidate.Classification != "partial_policy_denial" || candidate.Verification != "manual_review_required" || !strings.Contains(candidate.Evidence, "owned_fixture") || candidate.CallRef != worker.result.Trusted.Call.CallRef {
		t.Fatal("실제 관찰과 저장 후보가 다름")
	}
	var findings int
	if installer.QueryRow("SELECT count(*) FROM findings WHERE task_id=$1", task.ID).Scan(&findings) != nil || findings != 0 {
		t.Fatal("미검증 후보를 확정 발견으로 저장함")
	}
	results := runtime.host.Runtime().Results(task.ID)
	if len(results) != 1 || !results[0].Final || !results[0].ProcessDrained || !results[0].ProxyDrained || results[0].Call.Counts.Denied != 1 {
		t.Fatal("최종 실제 감사와 drain 누락")
	}
	modelCalls := worker.calls + planner.calls
	if err = runtime.Close(context.Background()); err != nil {
		t.Fatal("실제 종료 실패", err)
	}
	if err = supervisor.PersistRunOutcome(context.Background(), nil); err != nil {
		t.Fatal("종료 결과 저장 실패", err)
	}
	if hits.Load() != 1 || worker.calls+planner.calls != modelCalls {
		t.Fatal("종료 결과 저장이 모델 또는 대상을 재호출함")
	}
	api.Close()
	// 종료 후 보고서에는 별도 최소 역할만 제공한다.
	var role, schema string
	if f.Runtime.QueryRow("SELECT current_user,current_schema()").Scan(&role, &schema) != nil {
		t.Fatal("보고서 역할 조회 실패")
	}
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	exec := func(sql string) {
		t.Helper()
		if _, err := installer.Exec(sql); err != nil {
			t.Fatal("보고서 최소 권한 준비 실패", err)
		}
	}
	exec("REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA " + ident(schema) + " FROM " + ident(role))
	exec("REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA " + ident(schema) + " FROM " + ident(role))
	for _, table := range []string{"tasks", "task_archives", "findings", "task_control_modes", "exploration_nodes", "settings", "assets", "task_asset_links", "exploration_anchors", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links"} {
		exec("GRANT SELECT ON " + ident(table) + " TO " + ident(role))
	}
	exec("GRANT INSERT ON exploration_nodes TO " + ident(role))
	exec("GRANT USAGE ON SEQUENCE exploration_nodes_id_seq TO " + ident(role))
	exec("GRANT UPDATE(id) ON tasks,findings,exploration_nodes,assets,e3_verdict_issuances TO " + ident(role))
	exec("GRANT UPDATE(task_id) ON task_asset_links TO " + ident(role))
	exec("GRANT UPDATE(node_id) ON exploration_anchors TO " + ident(role))
	handler, err := NewFirstDiagnosisReportHandler(ctx, f.Runtime, keyDir, dataDir, grant.TaskID)
	if err != nil {
		t.Fatal("중단 후 저장 보고서 생성자 실패", err)
	}
	var reportReads atomic.Int64
	report := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/tasks/"+task.ID+"/diagnosis-run" {
			reportReads.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	if browser {
		listener, listenErr := net.Listen("tcp", strings.TrimPrefix(api.URL, "http://"))
		if listenErr != nil {
			t.Fatal("보고서 서버 동일 주소 재개 실패", listenErr)
		}
		report.Listener = listener
	}
	report.Start()
	defer report.Close()
	client = &combinedHTTPClient{t: t, server: report}
	client.login()
	status, savedRaw, _ := client.request("GET", "/api/tasks/"+task.ID+"/diagnosis-run", "")
	var saved diagnosisRunRecord
	if status != 200 || json.Unmarshal(savedRaw, &saved) != nil || saved.Version != 1 || saved.TaskID != grant.TaskID || saved.SavedAt.IsZero() || saved.Run.Phase != "closed" || saved.Run.Outcome != "completed" || !saved.Run.DrainComplete || saved.Run.ClosureFailed || saved.Run.Execution == nil {
		t.Fatalf("저장 보고서 종료 상태 누락: status=%d", status)
	}
	counts := saved.Run.Execution.Counts
	if counts.Calls != 1 || counts.Allowed != 1 || counts.Denied != 1 || counts.Responses != 1 || counts.ForwardAttempted != 1 || counts.Unknown != 0 {
		t.Fatal("저장된 종료 상태의 실제 송신 건수 불일치")
	}

	base := "/api/report/diagnostic/remediation?task=" + task.ID + "&audience=internal"
	code, body, _ := client.request("GET", base, "")
	var list struct {
		Entries []diagnosticRemediationEntryDTO
	}
	if code != 200 || json.Unmarshal(body, &list) != nil || len(list.Entries) != 1 {
		t.Fatal("실제 저장 후보 보고서 누락")
	}
	if browser {
		ui.run("report")
	}
	source := list.Entries[0]
	raw, _ = json.Marshal(map[string]any{"entry_id": source.EntryID, "expected_source_digest": source.SourceDigest})
	code, body, _ = client.request("POST", base, string(raw))
	var record diagnosticRemediationDTO
	if code != 200 || json.Unmarshal(body, &record) != nil || record.Bundle == nil {
		t.Fatalf("조치 저장 실패: %d", code)
	}
	code, storedBody, _ := client.request("GET", base, "")
	var stored struct {
		Entries []diagnosticRemediationEntryDTO
	}
	if code != 200 || json.Unmarshal(storedBody, &stored) != nil || len(stored.Entries) != 1 || stored.Entries[0].Record == nil || stored.Entries[0].Record.Bundle == nil || stored.Entries[0].Record.ID != record.ID || stored.Entries[0].Record.Bundle.AIPrompt != record.Bundle.AIPrompt {
		t.Fatal("저장된 조치 목록 본문 불일치")
	}
	copyPath := fmt.Sprintf("/api/report/diagnostic/remediation/prompt?task=%s&audience=internal&entry=%s&record=%d&digest=%s", task.ID, source.EntryID, record.ID, record.Bundle.InputDigest)
	code, body, _ = client.request("GET", copyPath, "")
	var copied struct {
		Prompt string `json:"ai_prompt"`
	}
	if code != 200 || json.Unmarshal(body, &copied) != nil || copied.Prompt != record.Bundle.AIPrompt {
		t.Fatal("저장과 복사 본문 불일치")
	}
	code, body, headers := client.request("GET", "/api/report/diagnostic?task="+task.ID+"&audience=internal&download=1", "")
	if code != 200 || headers.Get("Content-Disposition") == "" || !strings.Contains(html.UnescapeString(string(body)), record.Bundle.AIPrompt) {
		t.Fatal("저장과 HTML 본문 불일치")
	}
	if code, _, _ := client.request("GET", "/api/report/diagnostic?task=999999999", ""); code != 403 {
		t.Fatal("다른 task 보고서 허용")
	}
	// 후보에 연결된 실제 intent의 자산이 바뀌면 이전 조치 자료는 stale이어야 한다.
	asset, err := installer.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: target.URL, TaskID: grant.TaskID, Technologies: []string{"ChangedFixture"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = installer.Exploration(task.ExpID).Anchor(candidate.IntentID, asset); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := client.request("POST", base, string(raw)); code != 409 {
		t.Fatalf("stale 저장 허용: %d", code)
	}
	if code, _, _ := client.request("GET", copyPath, ""); code != 409 {
		t.Fatalf("stale 복사 허용: %d", code)
	}
	if browser {
		ui.run("stale")
	}
	if hits.Load() != 1 || worker.calls+planner.calls != modelCalls {
		t.Fatal("보고서 조회가 모델 또는 대상을 재호출함")
	}
	if err := logger.Close(); err != nil || logger.Incomplete() {
		t.Fatal("진단 로그 마감 또는 관측 불완전", err)
	}
	verifyCombinedDiagnosisLog(t, logger.Path(), grant.TaskID, saved.Run.RunRef, worker.result.Trusted.Call.CallRef, modelCalls, int(reportReads.Load()))
	if os.Getenv("ARTEX_DIAGNOSTIC_ACCEPTANCE_DIR") != "" {
		t.Logf("합성 진단 JSONL 보존: %s (송신=1 허용=1 거부=1 후보=1)", logger.Path())
	}
}

// 원문이 아닌 합성 이벤트의 관계와 실측 결과만 검증한다.
func verifyCombinedDiagnosisLog(t *testing.T, path string, task int64, runRef, callRef string, modelCalls, reportReads int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, diagnosticlog.MaxBytes+1))
	if err != nil || int64(len(raw)) > diagnosticlog.MaxBytes {
		t.Fatal("진단 로그 읽기 한도", err)
	}
	for _, secret := range []string{"synthetic-owned-combined-jwt-material", "synthetic-owned-combined-fixture", "Authorization", "owned_fixture", runRef, callRef} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("합성 민감 원문이 진단 로그에 포함됨")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	events := map[string]int{}
	modelRoles := map[string]int{}
	summary := false
	correlated := false
	digest := sha256.Sum256([]byte(runRef))
	expectedRun := hex.EncodeToString(digest[:])
	callDigest := sha256.Sum256([]byte(callRef))
	expectedCall := hex.EncodeToString(callDigest[:])
	var previous uint64
	for {
		var event diagnosticlog.Event
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("JSONL 행 손상", err)
		}
		if event.Sequence <= previous || event.TaskID != 0 && event.TaskID != task {
			t.Fatal("회차 추적 식별자 불일치")
		}
		previous = event.Sequence
		events[event.Event]++
		if event.Event == "model_call_started" {
			modelRoles[event.Role]++
		}
		if event.RunRef != "" && event.RunRef != expectedRun {
			t.Fatal("실제 run 해시 불일치")
		}
		if event.CallRef == expectedCall && event.IntentID > 0 && event.TaskID == task {
			correlated = true
		}
		if event.Event == "run_summary" {
			if event.TargetRequests != 1 || event.Approved != 1 || event.Denied != 1 || event.Candidates != 1 || event.Status != "succeeded" || event.ResultStored == nil || !*event.ResultStored || event.AuditComplete == nil || !*event.AuditComplete || event.ExecutionClosed == nil || !*event.ExecutionClosed {
				t.Fatal("진단 집계가 실제 송신·후보·마감과 다름")
			}
			summary = true
		}
	}
	if modelRoles["planner"] == 0 || modelRoles["worker"] == 0 || modelRoles["unknown"] != 0 {
		t.Fatalf("실제 모델 역할 관측 누락: %v", modelRoles)
	}
	if events["model_call_started"] != modelCalls || events["model_call_finished"] != modelCalls {
		t.Fatalf("실제 모델 호출과 로그 건수 불일치: calls=%d started=%d finished=%d", modelCalls, events["model_call_started"], events["model_call_finished"])
	}
	if !summary || !correlated || events["run_finished"] != 1 || events["result_save_finished"] != 1 || reportReads < 1 || events["report_queried"] != reportReads || events["planner_finished"] == 0 {
		t.Fatalf("실제 단계 관측 누락: summary=%v correlation=%v events=%v", summary, correlated, events)
	}
}
