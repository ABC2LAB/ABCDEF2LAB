package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/e2runner"
	"github.com/Autumn-27/artex/remediation"
	"github.com/Autumn-27/artex/report"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type candidateMemoryStore struct{ candidates []db.DiagnosticCandidate }

func (s *candidateMemoryStore) RecordDiagnosticCandidate(_ context.Context, c db.DiagnosticCandidate) (int64, error) {
	s.candidates = append(s.candidates, c)
	return int64(len(s.candidates)), nil
}
func TestE2DiagnosticCandidateUsesActualCall(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	store := &candidateMemoryStore{}
	saved := 0
	tool := newE2DiagnosticCandidateTool(store, binding, state, func() { saved++ })
	call := func(input string) bool {
		result, err := tool.Call(context.Background(), json.RawMessage(input), nil)
		if err != nil {
			t.Fatal(err)
		}
		return result.IsError
	}
	if !call(`{"call_ref":"invented","vulnclass":"XSS","name":"후보","summary":"관찰"}`) {
		t.Fatal("invented evidence accepted")
	}
	run := func(context.Context, string) (e2runner.Result, error) {
		return e2runner.Result{ToolOutput: "observed keep-secret\nSet-Cookie: new-session=unknown-response-secret\nAuthorization: Bearer new-bearer-secret",
			Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: execution.SnapshotForIntent(22),
				Call:  e2proxy.CallResult{IntentID: 22, CallRef: "actual-call", Final: true, AuditComplete: true},
				Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: "completed"}}, nil
	}
	bash := newE2HTTPBashTool(execution, run, time.Second, state, binding.Control, json.RawMessage(`{"summary":"synthetic"}`), 22)
	result, err := bash.Call(context.Background(), json.RawMessage(`{"command":"printf synthetic"}`), nil)
	if err != nil || result.IsError {
		t.Fatalf("Bash: %v %s", err, result.Flatten())
	}
	if call(`{"call_ref":"actual-call","vulnclass":"XSS","name":"후보","summary":"keep-secret"}`) {
		t.Fatal("actual observation rejected")
	}
	c := store.candidates[0]
	if c.Validate() != nil || c.Verification != "manual_review_required" || c.Classification != "completed" ||
		strings.Contains(c.Evidence, "keep-secret") || strings.Contains(c.Evidence, "unknown-response-secret") || strings.Contains(c.Evidence, "new-bearer-secret") || strings.Contains(c.Summary, "keep-secret") || !strings.Contains(c.Evidence, "printf synthetic") {
		t.Fatalf("candidate provenance/redaction failed: %v", c.Validate())
	}
	for _, extra := range []string{`"verification":"confirmed"`, `"evidence":"fabricated"`, `"task_id":999`, `"intent_id":999`, `"evidence_version":2`} {
		if !call(`{"call_ref":"actual-call","vulnclass":"SQLi","name":"후보","summary":"관찰",` + extra + `}`) {
			t.Fatal("model authority accepted")
		}
	}
	if len(store.candidates) != 1 || saved != 1 {
		t.Fatal("rejected input persisted or write count mismatched")
	}
}
func TestE2DiagnosticCandidatePreservesExecutionStates(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	for _, classification := range []string{"completed", "policy_not_executed", "unsupported", "result_unknown", "tool_failure"} {
		t.Run(classification, func(t *testing.T) {
			state := &e2HTTPToolState{}
			store := &candidateMemoryStore{}
			state.observeDiagnostic(execution, 22, "synthetic", e2runner.Result{Trusted: e2runner.TrustedResult{
				Execution: execution.SnapshotForIntent(22), Call: e2proxy.CallResult{IntentID: 22, CallRef: classification}, Classification: classification}})
			tool := newE2DiagnosticCandidateTool(store, binding, state)
			input, _ := json.Marshal(map[string]string{"call_ref": classification, "vulnclass": "SSRF", "name": "합성 후보", "summary": "관찰"})
			result, err := tool.Call(context.Background(), input, nil)
			if err != nil || result.IsError || len(store.candidates) != 1 {
				t.Fatalf("lost state %s: %v", classification, err)
			}
			if store.candidates[0].Classification != classification {
				t.Fatal("state changed")
			}
		})
	}
}

// 합성 도구 관찰 → 모델 후보 도구 → PostgreSQL → 상세/종합 렌더링 실제 호출 경로.
// 실제 모델 탐지 성능 또는 대상/격리 배치 검증을 뜻하지 않는다.
func TestE2DiagnosticCandidatePostgresReport(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("synthetic PostgreSQL not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	task, err := pg.CreateTask("synthetic candidate pipeline", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Exec("DELETE FROM tasks WHERE id=$1", task.ID)
	defer pg.Exec("DELETE FROM explorations WHERE id=$1", task.ExplorationID)
	store := pg.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "synthetic observation"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	execution, _, binding := e2WorkerFixture(t, task.ID, intent)
	state := &e2HTTPToolState{}
	var callNo int
	run := func(context.Context, string) (e2runner.Result, error) {
		callNo++
		classification := "completed"
		if callNo == 3 {
			classification = "policy_not_executed"
		}
		return e2runner.Result{ToolOutput: "synthetic observed response",
			Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: execution.SnapshotForIntent(intent),
				Call:  e2proxy.CallResult{IntentID: intent, CallRef: fmt.Sprintf("synthetic-call-%d", callNo), Final: true, AuditComplete: true},
				Final: true, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Classification: classification}}, nil
	}
	bash := newE2HTTPBashTool(execution, run, time.Second, state, binding.Control, json.RawMessage(`{"summary":"synthetic"}`), intent)
	submit := newE2DiagnosticCandidateTool(store, binding, state)
	for i, kind := range []string{"XSS", "SQLi", "SSRF"} {
		input, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("printf synthetic-%d", i)})
		if _, err := bash.Call(context.Background(), input, nil); err != nil {
			t.Fatal(err)
		}
		input, _ = json.Marshal(map[string]string{"call_ref": fmt.Sprintf("synthetic-call-%d", i+1), "vulnclass": kind, "name": "합성 " + kind, "summary": "합성 검토 후보", "remediation": "별도 승인 후 해당 관찰을 검증"})
		result, err := submit.Call(context.Background(), input, nil)
		if err != nil || result.IsError {
			t.Fatalf("submit %v %s", err, result.Flatten())
		}
	}
	candidates, err := store.ListDiagnosticCandidates(task.ID, binding.RunRef, db.MaxDiagnosticCandidates+1)
	if err != nil || len(candidates) != 3 {
		t.Fatalf("saved %d %v", len(candidates), err)
	}
	rendered := report.DiagnosticReport{TaskID: task.ID, GeneratedAt: time.Now().UTC()}
	for _, c := range candidates {
		outcome := "review"
		if c.Classification == "policy_not_executed" {
			outcome = "not_executed"
		}
		rendered.Entries = append(rendered.Entries, report.DiagnosticEntry{
			ID: fmt.Sprintf("candidate-%d", c.ID), Kind: "candidate", Run: c.RunRef, IntentID: c.IntentID,
			Title: c.Name, VulnClass: c.VulnClass, Verification: c.Verification, Outcome: outcome, Summary: c.Summary,
			Remediation: c.Remediation, Evidence: c.Evidence, EvidenceVersion: c.EvidenceVersion, Digest: c.EvidenceDigest, ObservedAt: c.ObservedAt,
		})
	}
	source, err := db.LoadDiagnosticRemediationSource(context.Background(), pg.DB, task.ID, rendered.Entries[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	advice, err := pg.SaveDiagnosticRemediation(context.Background(), task.ID, rendered.Entries[1].ID, source.SourceDigest, []remediation.Technology{{Kind: "language", Name: "Elixir", Version: "1.18", Source: "user_provided", Confidence: "confirmed", Component: "search-service", ComponentRole: "backend"}}, remediation.Context{Component: "search-service", ComponentRole: "backend", Source: "user_provided", Endpoint: "GET /search/{term}", Prerequisites: "Authenticated synthetic test account", Observation: "Synthetic search behavior requires validation", ExpectedBehavior: "Normal searches return matching results", ProhibitedBehavior: "Search text must not become SQL structure"})
	if err != nil {
		t.Fatal(err)
	}
	rendered.Entries[1].RemediationArtifactID = advice.ID
	rendered.Entries[1].RemediationBundle = &advice.Bundle
	rendered.Entries[1].RemediationSource = &advice.Source
	output, err := report.DiagnosticHTML(rendered)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"XSS", "SQLi", "SSRF", "synthetic-call-1", "synthetic-call-2", "synthetic-call-3"} {
		if !strings.Contains(string(output), kind) {
			t.Fatalf("missing %s", kind)
		}
	}
	if !strings.Contains(advice.Bundle.AIPrompt, "GET /search/{term}") || !strings.Contains(advice.Bundle.AIPrompt, "Elixir") {
		t.Fatal("independent project context absent")
	}
	if !strings.Contains(html.UnescapeString(string(output)), advice.Bundle.AIPrompt) {
		t.Fatal("stored prompt absent from integrated report")
	}
	var count int
	if err := pg.QueryRow("SELECT count(*) FROM findings WHERE task_id=$1", task.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("unverified finding promotion")
	}
}

func TestE2DiagnosticCandidateLargeObservation(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	state.observeDiagnostic(execution, 22, strings.Repeat("<한글>", 12000), e2runner.Result{
		ToolOutput: strings.Repeat("<한글>", 700000), Trusted: e2runner.TrustedResult{
			Execution: execution.SnapshotForIntent(22), Call: e2proxy.CallResult{IntentID: 22, CallRef: "large-call"},
			Classification: "completed",
		}})
	store := &candidateMemoryStore{}
	tool := newE2DiagnosticCandidateTool(store, binding, state)
	result, err := tool.Call(context.Background(), json.RawMessage(`{"call_ref":"large-call","vulnclass":"XSS","name":"큰 관찰","summary":"합성 자료"}`), nil)
	if err != nil || result.IsError || len(store.candidates) != 1 {
		t.Fatalf("lost large observation: %v %s", err, result.Flatten())
	}
	c := store.candidates[0]
	if !c.OutputTruncated || len(c.Evidence) > 256<<10 || c.Validate() != nil {
		t.Fatal("truncation boundary invalid")
	}
}

// 실제 통제 HTTP → trusted call → 후보 → 현재 source → 저장 prompt까지의 합성 통합이다.
// 모델 및 Bash namespace 기동은 이 시험에서 수행하지 않는다.
func TestDiagnosticActualHTTPContextPipeline(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("synthetic PostgreSQL not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	task, err := pg.CreateTask("synthetic actual HTTP", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Exec("DELETE FROM tasks WHERE id=$1", task.ID)
	defer pg.Exec("DELETE FROM explorations WHERE id=$1", task.ExplorationID)
	store := pg.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "synthetic actual"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Server", "Caddy/2.8")
		w.Header().Set("X-Powered-By", "Phoenix/1.7")
		w.Write([]byte("synthetic response"))
	}))
	defer target.Close()
	execution, _, binding := e2WorkerFixtureTarget(t, target.URL, "/orders", task.ID, intent)
	state := &e2HTTPToolState{}
	run := func(ctx context.Context, _ string) (e2runner.Result, error) {
		call, err := execution.BeginCall(ctx, "synthetic-command")
		if err != nil {
			return e2runner.Result{}, err
		}
		req, _ := http.NewRequest("GET", target.URL+"/orders/private-customer?token=synthetic-private", nil)
		resp, _, err := call.Do(ctx, req)
		if err != nil {
			return e2runner.Result{}, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		result, err := call.Close(ctx)
		return e2runner.Result{ToolOutput: "untrusted summary", Trusted: e2runner.TrustedResult{Version: e2proxy.Version, Execution: execution.SnapshotForIntent(intent), Call: result, ProcessBashExited: true, ProcessDrained: true, ProxyDrained: true, Final: true, Classification: "completed"}}, err
	}
	bash := newE2HTTPBashTool(execution, run, time.Second, state, binding.Control, json.RawMessage(`{"summary":"synthetic"}`), intent)
	if result, err := bash.Call(context.Background(), json.RawMessage(`{"command":"synthetic adapter"}`), nil); err != nil || result.IsError {
		t.Fatalf("controlled observation %v %s", err, result.Flatten())
	}
	if result, err := bash.Call(context.Background(), json.RawMessage(`{"command":"synthetic second adapter"}`), nil); err != nil || result.IsError {
		t.Fatalf("second observation %v", err)
	}
	calls := execution.Calls()
	if len(calls) != 2 || len(calls[0].Observations) != 1 || hits != 2 {
		t.Fatal("actual HTTP observation missing")
	}
	submit := newE2DiagnosticCandidateTool(store, binding, state)
	input, _ := json.Marshal(map[string]any{"call_ref": calls[1].CallRef, "related_call_refs": []string{calls[0].CallRef}, "vulnclass": "business_logic", "name": "합성 후보", "summary": "모델의 비신뢰 해석"})
	if result, err := submit.Call(context.Background(), input, nil); err != nil || result.IsError {
		t.Fatalf("candidate %v %s", err, result.Flatten())
	}
	candidates, err := store.ListDiagnosticCandidates(task.ID, binding.RunRef, 10)
	if err != nil || len(candidates) != 1 {
		t.Fatal(err)
	}
	entry := fmt.Sprintf("candidate-%d", candidates[0].ID)
	source, err := db.LoadDiagnosticRemediationSource(context.Background(), pg.DB, task.ID, entry)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Context.Observations) != 4 || len(source.Technologies) != 2 || source.Context.Observations[0].Endpoint != "GET /orders/{resource}" || source.VulnClass != "business_logic" {
		t.Fatalf("context lost: %+v", source)
	}
	record, err := pg.SaveDiagnosticRemediation(context.Background(), task.ID, entry, source.SourceDigest, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/orders/{resource}", "Phoenix", "Caddy", "business_logic", "HTTP 상태=200"} {
		if !strings.Contains(record.Bundle.AIPrompt, want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, secret := range []string{"synthetic-private", "private-customer", "Set-Cookie", "untrusted summary"} {
		if strings.Contains(record.Bundle.AIPrompt, secret) {
			t.Fatalf("raw material propagated %s", secret)
		}
	}
	if record.Source.Status != "review" || hits != 2 {
		t.Fatal("HTTP success promoted or generation sent new request")
	}
	records, err := db.ListDiagnosticRemediationRecords(context.Background(), pg.DB, task.ID, 10)
	if err != nil || len(records) != 1 || records[0].Stale {
		t.Fatalf("current observed source stale %v", err)
	}
}

func TestE2DiagnosticCommandAndRemediationDLP(t *testing.T) {
	execution, _, binding := e2WorkerFixture(t)
	state := &e2HTTPToolState{}
	state.observeDiagnostic(execution, 22, "printf synthetic\nAuthorization: Bearer command-fixture-private", e2runner.Result{Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(22), Call: e2proxy.CallResult{IntentID: 22, CallRef: "actual"}, Classification: "completed"}})
	if strings.Contains(state.observations["actual"].Evidence, "command-fixture-private") {
		t.Fatal("command bypassed DLP")
	}
	store := &candidateMemoryStore{}
	tool := newE2DiagnosticCandidateTool(store, binding, state)
	input := `{"call_ref":"actual","vulnclass":"XSS","name":"후보","summary":"관찰","remediation":"Authorization: Bearer remedy-fixture-private"}`
	r, err := tool.Call(t.Context(), json.RawMessage(input), nil)
	if err != nil || r.IsError || len(store.candidates) != 1 {
		t.Fatal("candidate missing", err)
	}
	if strings.Contains(store.candidates[0].Remediation, "remedy-fixture-private") {
		t.Fatal("remediation bypassed DLP")
	}
	for _, suffix := range []string{` {}`, ` null`, ` []`, ` true`} {
		r, err := tool.Call(t.Context(), json.RawMessage(input+suffix), nil)
		if err != nil || !r.IsError || len(store.candidates) != 1 {
			t.Fatal("trailing input accepted", err)
		}
	}
}
func TestE2DiagnosticObservationRejectsMismatchedIntents(t *testing.T) {
	execution, _, _ := e2WorkerFixture(t)
	for _, field := range []string{"execution", "call"} {
		t.Run(field, func(t *testing.T) {
			state := &e2HTTPToolState{}
			result := e2runner.Result{Trusted: e2runner.TrustedResult{Execution: execution.SnapshotForIntent(22), Call: e2proxy.CallResult{IntentID: 22, CallRef: "actual"}, Classification: "completed"}}
			if field == "execution" {
				result.Trusted.Execution.IntentID = 23
			} else {
				result.Trusted.Call.IntentID = 23
			}
			state.observeDiagnostic(execution, 22, "synthetic", result)
			if len(state.observations) != 0 {
				t.Fatal("mismatched intent retained")
			}
		})
	}
}
func TestDiagnosticNodeDetailOmitsEvidenceWithoutMutatingSource(t *testing.T) {
	n := &db.Node{ID: 1, Kind: "fact", Payload: json.RawMessage(`{"summary":"candidate","diagnostic_candidate_v1":{"evidence":"large-fixture-secret","evidence_digest":"bound-digest","summary":"review"}}`)}
	original := string(n.Payload)
	result, err := diagnosticNodeResult(n)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Flatten()
	if strings.Contains(text, "large-fixture-secret") || !strings.Contains(text, "evidence_omitted") || !strings.Contains(text, "bound-digest") || string(n.Payload) != original {
		t.Fatal("node projection leaked evidence or changed source")
	}
}
