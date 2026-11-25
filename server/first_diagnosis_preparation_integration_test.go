//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/diagnosticlog"
	"github.com/Autumn-27/artex/report"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

type firstDiagnosisPreparationModel struct {
	mu             sync.Mutex
	target         string
	assetID        int64
	requests       int
	emitted        map[string]int
	outbound       bool
	toolError      bool
	nextGoalChange func() error
	mutationError  bool
	badProfile     bool
	noGoals        bool // 목표 분해 차례에 도구를 부르지 않고 끝낸다(목표 0개).
	stallGoals     bool // 목표 분해 차례에 응답하지 않고 요청이 끊길 때까지 기다린다(목표 만들기 상한, #879).
	stallAfterGoal bool // 목표 하나를 저장(set_goals)한 뒤 다음 목표 분해 턴에서 멈춘다(부분 저장 뒤 상한, #879).
	noIntents      bool // 계획 차례에 add_intent를 부르지 않고 끝낸다(intent 0개).
	// loopPlan이면 계획 차례마다 list_assets를 다시 불러 Plan이 턴 상한에서만 끝나게 하고, 계획 요청의 메시지 수 범위를 남긴다.
	loopPlan         bool
	planMin, planMax int
	planWrapUps      int // 계획 마무리 턴(이번 라운드 마무리 문장) 요청 수. 그 요청에는 도구 없이 끝낸다.
}

func (m *firstDiagnosisPreparationModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		Tools  []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role       string          `json:"role"`
			ToolCallID string          `json:"tool_call_id"`
			Content    json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &request) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests++
	m.badProfile = m.badProfile || request.Model != "local:preparation-fixture" || request.Stream
	goalRound := false
	for _, tool := range request.Tools {
		switch tool.Function.Name {
		case "set_goals":
			goalRound = true
		case "Bash", "WebFetch", "WebSearch", "Read", "Write", "Edit", "MultiEdit", "LS", "Glob", "Grep":
			m.outbound = true
		}
	}
	if goalRound && m.stallGoals {
		m.mu.Unlock()
		<-r.Context().Done()
		m.mu.Lock()
		return
	}
	if goalRound && m.nextGoalChange != nil {
		change := m.nextGoalChange
		m.nextGoalChange = nil
		if change() != nil {
			m.mutationError = true
		}
	}
	seen := map[string]bool{}
	for _, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		seen[message.ToolCallID] = true
		lower := bytes.ToLower(message.Content)
		if bytes.Contains(lower, []byte("permission denied")) || bytes.Contains(lower, []byte(`\"errors\"`)) || bytes.Contains(lower, []byte("not enabled")) {
			m.toolError = true
		}
	}
	if goalRound && m.stallAfterGoal && seen["preparation-goals"] {
		m.mu.Unlock()
		<-r.Context().Done()
		m.mu.Lock()
		return
	}
	var id, name string
	var input any
	if goalRound && !m.noGoals {
		switch {
		case !seen["preparation-scope"]:
			id, name, input = "preparation-scope", "add_task_scope", map[string]any{"kind": "ip", "value": "127.0.0.1", "reason": "The operator explicitly limited this owned fixture to IP 127.0.0.1."}
		case !seen["preparation-constraints"]:
			id, name, input = "preparation-constraints", "set_constraints", map[string]any{"constraints": []map[string]string{{"type": "deny", "text": "Do not send target requests during preparation."}, {"type": "allow", "text": "Prepare only GET for " + m.target + "/owned."}}}
		case !seen["preparation-goals"]:
			id, name, input = "preparation-goals", "set_goals", map[string]any{"text": "Prepare the first controlled GET of the owned fixture without executing it.", "vulnclass": "IDOR"}
		}
	} else if !goalRound && m.loopPlan && len(request.Messages) > 0 && bytes.Contains(request.Messages[len(request.Messages)-1].Content, []byte("The step budget of this planning round is about to run out")) {
		m.planWrapUps++
	} else if !goalRound && m.loopPlan {
		if m.planMin == 0 || len(request.Messages) < m.planMin {
			m.planMin = len(request.Messages)
		}
		m.planMax = max(m.planMax, len(request.Messages))
		id, name, input = "preparation-loop-"+strconv.Itoa(m.requests), "list_assets", map[string]any{"dsl": "ip==127.0.0.1", "type": "ip", "limit": 1}
	} else if !goalRound {
		switch {
		case !seen["preparation-assets"]:
			id, name, input = "preparation-assets", "list_assets", map[string]any{"dsl": "ip==127.0.0.1", "type": "ip", "limit": 5}
		case !seen["preparation-intent"] && !m.noIntents:
			id, name, input = "preparation-intent", "add_intent", map[string]any{"summary": "Prepare an authorized GET of " + m.target + "/owned; do not execute it during preparation.", "asset_ids": []int64{m.assetID}, "priority": 5}
		}
	}
	message := map[string]any{"role": "assistant", "content": "The owned preparation step is complete."}
	finish := "stop"
	if name != "" {
		encoded, _ := json.Marshal(input)
		message["content"] = nil
		message["tool_calls"] = []map[string]any{{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}
		finish = "tool_calls"
		m.emitted[name]++
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "owned-preparation-response", "object": "chat.completion", "model": "local:preparation-fixture", "choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
}

func TestFirstDiagnosisPreparationOwnedPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_PREPARATION_TEST_DSN")
	if dsn == "" {
		t.Skip("독립 준비 경로 PostgreSQL DSN이 제공되지 않음")
	}
	if os.Getenv("ARTEX_FIRST_DIAGNOSIS_PREPARATION_TEST_OWNED") != "1" {
		t.Fatal("준비 시험 전용 일회용 DB 소유 확인이 필요함")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	installer, err := pgdb.Open(dsn)
	if err != nil {
		t.Fatal("준비 fixture 설치자 DB 초기화 실패")
	}
	defer installer.Close()
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer target.Close()
	assetID, err := installer.Assets().UpsertIP(pgdb.UpsertIPReq{IP: "127.0.0.1"})
	if err != nil || assetID <= 0 {
		t.Fatal("소유 IP 자산 fixture 설치 실패")
	}
	modelState := &firstDiagnosisPreparationModel{target: target.URL, assetID: assetID, emitted: map[string]int{}}
	model := httptest.NewServer(modelState)
	defer model.Close()
	profile := &pgdb.LLMProfile{Name: fmt.Sprintf("owned-preparation-profile-%d", time.Now().UnixNano()), Format: "openai", Model: "local:preparation-fixture", BaseURL: model.URL + "/v1", APIKey: "synthetic-owned-preparation-model-key", Streaming: false, ContextWindowK: 12}
	profileID, err := installer.SaveProfile(profile)
	if err != nil || profileID <= 0 || installer.SetActiveProfile(profileID) != nil {
		t.Fatal("실제 준비 모델 profile 사전 설치 실패")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-owned-preparation-password"), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("준비 fixture 사전 인증 자료 설치 실패")
	}
	var database string
	if installer.QueryRowContext(ctx, "SELECT current_database()").Scan(&database) != nil {
		t.Fatal("소유 준비 DB 확인 실패")
	}
	suffix := make([]byte, 10)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	role := "first_prep_rt_" + hex.EncodeToString(suffix)
	password := make([]byte, 24)
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	ident := func(value string) string { return pgx.Identifier{value}.Sanitize() }
	exec := func(statement string) {
		t.Helper()
		if _, err := installer.ExecContext(ctx, statement); err != nil {
			t.Fatal("독립 준비 역할 fixture 설정 실패")
		}
	}
	exec("REVOKE CREATE,TEMP ON DATABASE " + ident(database) + " FROM PUBLIC")
	exec("REVOKE CREATE ON SCHEMA public FROM PUBLIC")
	exec("CREATE ROLE " + ident(role) + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + hex.EncodeToString(password) + "'")
	defer func() {
		if _, err := installer.ExecContext(context.Background(), "DROP OWNED BY "+ident(role)); err != nil {
			t.Error("준비 역할 소유권 정리 실패")
		}
		if _, err := installer.ExecContext(context.Background(), "DROP ROLE "+ident(role)); err != nil {
			t.Error("준비 역할 정리 실패")
		}
	}()
	exec("GRANT CONNECT ON DATABASE " + ident(database) + " TO " + ident(role))
	exec("GRANT USAGE ON SCHEMA public TO " + ident(role))
	for _, table := range []string{"tasks", "explorations", "exploration_nodes", "task_llm_profiles", "findings", "request_approvals", "e2_prompt_runs", "e2_prompt_observations"} {
		exec("GRANT SELECT,INSERT,UPDATE ON TABLE " + ident(table) + " TO " + ident(role))
	}
	for _, table := range []string{"activity", "exploration_edges", "exploration_anchors", "task_scope", "task_constraints"} {
		exec("GRANT SELECT,INSERT ON TABLE " + ident(table) + " TO " + ident(role))
	}
	for _, table := range []string{"task_archives", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links", "llm_profiles", "intercept_rules", "settings", "agents", "agent_prompts", "task_categories", "task_relations", "assets", "companies", "company_scope", "task_asset_links", "asset_intercept_rules", "task_intercept_rules"} {
		exec("GRANT SELECT ON TABLE " + ident(table) + " TO " + ident(role))
	}
	exec("GRANT UPDATE(id) ON e3_verdict_issuances TO " + ident(role))
	exec("GRANT UPDATE(id) ON assets TO " + ident(role))
	exec("GRANT UPDATE(task_id) ON task_asset_links TO " + ident(role))
	// 일반 HTTP 실행 역할은 진단 Worker의 insert_assets(C24)로 자산과 작업 연결을 쓴다. 목록은 구현에서 가져오지 않는다.
	exec("GRANT INSERT, UPDATE(auth, bound_domains, c_segment, company_id, company_source, content_length, domain, extra, favicon_mmh3, icp, ip, last_seen, open_ports, page_title, params, record_value, status_code, task_ids, technologies) ON assets TO " + ident(role))
	exec("GRANT INSERT, DELETE, UPDATE(source, source_summary, source_node_id) ON task_asset_links TO " + ident(role))
	exec("GRANT UPDATE(node_id) ON exploration_anchors TO " + ident(role))

	exec("GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO " + ident(role))
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("독립 준비 runtime 구성 실패")
	}
	config.User = role
	config.Password = hex.EncodeToString(password)
	pool := stdlib.OpenDB(*config)
	pool.SetMaxOpenConns(4)
	defer pool.Close()
	var currentUser, sessionUser string
	if pool.QueryRowContext(ctx, "SELECT current_user,session_user").Scan(&currentUser, &sessionUser) != nil || currentUser != role || sessionUser != role {
		t.Fatal("독립 runtime LOGIN 신원 확인 실패")
	}
	if CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, pool) != nil {
		t.Fatal("준비 실제 최소 runtime 권한 검증 실패")
	}
	description := "The operator owns fixture IP 127.0.0.1. Prepare only GET " + target.URL + "/owned. Do not send target requests during preparation."
	prepared, err := PrepareFirstDiagnosisTask(ctx, pool, description, "Prepare the first controlled read of the owned fixture.", profileID, 60)
	if err != nil || prepared.TaskID <= 0 || prepared.ExplorationID <= 0 {
		t.Fatal("실제 PrepareFirstDiagnosisTask가 빈 task를 만들지 못함")
	}
	var existing int
	if pool.QueryRowContext(ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind IN ('goal','intent')", prepared.ExplorationID).Scan(&existing) != nil || existing != 0 {
		t.Fatal("준비 경로 전에 goal 또는 intent가 주입됨")
	}
	privateDir := func() string {
		p := t.TempDir()
		if os.Chmod(p, 0700) != nil {
			t.Fatal("준비 디렉터리 보호 실패")
		}
		return p
	}
	skillDir, dataDir, keyDir := privateDir(), privateDir(), privateDir()
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-owned-preparation-jwt-key-material"), 0600) != nil {
		t.Fatal("준비 JWT 사전 공급 실패")
	}
	manifest := filepath.Join(privateDir(), "models.json")
	manifestBody := fmt.Sprintf(`{"version":1,"profiles":[{"profile_id":%d,"format":"openai","model":"local:preparation-fixture","base_url":%q,"addresses":["127.0.0.1"]}]}`, profileID, profile.BaseURL)
	if os.WriteFile(manifest, []byte(manifestBody), 0600) != nil {
		t.Fatal("실제 profile ID 보호 manifest 공급 실패")
	}
	manager, err := NewFirstDiagnosisManager(ctx, dataDir, pool)
	if err != nil {
		t.Fatal("실제 runtime pool의 무DDL Manager 생성 실패")
	}
	defer manager.Close()
	server, err := NewFirstDiagnosisPreparationServer(ctx, manager, skillDir, dataDir, keyDir, manifest)
	if err != nil || server == nil {
		t.Fatal("실제 준비 서버 생성 실패")
	}
	supervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
	if err != nil || supervisor.Attach(server) != nil {
		t.Fatal("실제 준비 Supervisor 결속 실패")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		shutdown := supervisor.Shutdown()
		for _, finish := range []func(context.Context, *Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain, shutdown.CheckResults} {
			if finish(cleanup, server) != nil {
				t.Error("준비 서버 actual writer 정리 실패")
				break
			}
		}
	}()
	taskID := strconv.FormatInt(prepared.TaskID, 10)
	if err := supervisor.PrepareIntents(ctx, taskID); err != nil {
		t.Fatal("실제 GoalDecompose→Planner→Store 준비 경로 실패")
	}
	task, ok := manager.Task(taskID)
	if !ok || task == nil || task.ExpID != prepared.ExplorationID || task.Store == nil || task.Store.ID() != prepared.ExplorationID {
		t.Fatal("실제 준비 task·Store 결속 불일치")
	}
	var goals, intents, scopes, constraints, edges, anchors int
	checks := []struct {
		query string
		args  []any
		out   *int
	}{
		{"SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal' AND state='open'", []any{prepared.ExplorationID}, &goals},
		{"SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND state='open'", []any{prepared.ExplorationID}, &intents},
		{"SELECT count(*) FROM task_scope WHERE task_id=$1 AND kind='ip' AND net='127.0.0.1/32'::cidr AND source='agent'", []any{prepared.TaskID}, &scopes},
		{"SELECT count(*) FROM task_constraints WHERE exploration_id=$1 AND origin='goals'", []any{prepared.ExplorationID}, &constraints},
		{"SELECT count(*) FROM exploration_edges e JOIN exploration_nodes s ON s.id=e.src_id JOIN exploration_nodes d ON d.id=e.dst_id WHERE e.exploration_id=$1 AND s.kind='fact' AND s.state='origin' AND ((d.kind='goal' AND e.rel='spawns') OR (d.kind='intent' AND e.rel='derived_from'))", []any{prepared.ExplorationID}, &edges},
		{"SELECT count(*) FROM exploration_anchors a JOIN exploration_nodes n ON n.id=a.node_id WHERE n.exploration_id=$1 AND n.kind='intent' AND a.asset_id=$2", []any{prepared.ExplorationID, assetID}, &anchors},
	}
	for _, check := range checks {
		if err := pool.QueryRowContext(ctx, check.query, check.args...).Scan(check.out); err != nil {
			t.Fatal("실제 준비 산출물 SQL 조회 실패")
		}
	}
	if goals != 1 || intents != 1 || scopes != 1 || constraints != 2 || edges != 2 || anchors != 1 {
		t.Fatalf("준비 산출물 부족: goal=%d intent=%d scope=%d constraints=%d edges=%d anchors=%d", goals, intents, scopes, constraints, edges, anchors)
	}
	var paused, queued, neverRun bool
	if pool.QueryRowContext(ctx, "SELECT paused,queued,first_run_at IS NULL AND completed_at IS NULL FROM tasks WHERE id=$1", prepared.TaskID).Scan(&paused, &queued, &neverRun) != nil || !paused || queued || !neverRun {
		t.Fatal("준비 명령이 task를 실제 실행하거나 재개함")
	}
	modelState.mu.Lock()
	requests, outbound, toolError, badProfile := modelState.requests, modelState.outbound, modelState.toolError, modelState.badProfile
	emitted := map[string]int{}
	for k, v := range modelState.emitted {
		emitted[k] = v
	}
	modelState.mu.Unlock()
	for _, name := range []string{"add_task_scope", "set_constraints", "set_goals", "list_assets", "add_intent"} {
		if emitted[name] != 1 {
			t.Fatal("원본 준비 도구의 실제 호출이 누락되거나 반복됨: " + name)
		}
	}
	if requests != 7 || outbound || toolError || badProfile {
		t.Fatalf("준비 모델 경계 위반: requests=%d outbound=%v tool_error=%v profile_error=%v", requests, outbound, toolError, badProfile)
	}
	if targetHits.Load() != 0 || server.engine.e2HTTP != nil || server.engine.controlledReads != nil || server.engine.Started(taskID) || server.engine.inflightCount(taskID) != 0 || server.engine.ActiveLLMCalls(taskID) != 0 {
		t.Fatal("준비 단계에서 대상 송신·실행 권한·writer가 발생함")
	}
	if err := supervisor.Run(ctx, taskID, 0, ""); err == nil {
		t.Fatal("준비 서버에서 production Run이 수용됨")
	}
	if targetHits.Load() != 0 {
		t.Fatal("준비 서버 Run 거부 뒤 대상 요청이 발생함")
	}

	var foreignExploration int64
	if installer.QueryRowContext(ctx, "INSERT INTO explorations(description,goal) VALUES('owned foreign preparation fixture','No execution') RETURNING id").Scan(&foreignExploration) != nil {
		t.Fatal("독립 exploration 결속 음성 fixture 생성 실패")
	}
	// 읽기 snapshot을 보유한 준비 서버와 실제 SQL 상태가 달라져도 권위 준비 완료로 승격하지 않는다.
	for _, tc := range []struct {
		name, assignment string
		mid              bool
	}{
		{"stale-paused", "paused=false", false},
		{"stale-queued", "queued=true", false},
		{"stale-first-run", "first_run_at=clock_timestamp()", false},
		{"stale-completed", "completed_at=clock_timestamp()", false},
		{"stale-status", "status='done'", false},
		{"stale-deleted", "deleted_at=clock_timestamp()", false},
		{"stale-exploration", fmt.Sprintf("exploration_id=%d", foreignExploration), false},
		{"mid-queued", "queued=true", true},
		{"mid-status", "status='done'", true},
		{"mid-deleted", "deleted_at=clock_timestamp()", true},
		{"mid-exploration", fmt.Sprintf("exploration_id=%d", foreignExploration), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, err := PrepareFirstDiagnosisTask(ctx, pool, description, "Prepare a fresh owned fixture without executing it.", profileID, 60)
			if err != nil {
				t.Fatal("새 빈 준비 task 생성 실패")
			}
			manager.LoadExisting()
			cached, exists := manager.Task(strconv.FormatInt(next.TaskID, 10))
			if !exists || !cached.lifecycleSnapshot().Paused || cached.lifecycleSnapshot().Queued {
				t.Fatal("실제 DB 초기 snapshot을 캐시하지 못함")
			}
			nextServer, err := NewFirstDiagnosisPreparationServer(ctx, manager, skillDir, dataDir, keyDir, manifest)
			if err != nil {
				t.Fatal("독립 상태 변화 준비 서버 생성 실패")
			}
			nextSupervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
			if err != nil || nextSupervisor.Attach(nextServer) != nil {
				t.Fatal("독립 준비 감독자 결속 실패")
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				shutdown := nextSupervisor.Shutdown()
				for _, finish := range []func(context.Context, *Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain, shutdown.CheckResults} {
					if finish(cleanup, nextServer) != nil {
						t.Error("상태 변화 준비 writer 정리 실패")
						break
					}
				}
			}()
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if _, err := installer.ExecContext(cleanup, "UPDATE tasks SET paused=true,queued=false,status='created',first_run_at=NULL,completed_at=NULL,deleted_at=NULL,exploration_id=$2 WHERE id=$1", next.TaskID, next.ExplorationID); err != nil {
					t.Error("소유 fixture task 상태 복구 실패")
				}
			}()
			mutate := func() error {
				_, err := installer.ExecContext(ctx, "UPDATE tasks SET "+tc.assignment+" WHERE id=$1", next.TaskID)
				return err
			}
			modelState.mu.Lock()
			before := modelState.requests
			modelState.mutationError = false
			if tc.mid {
				modelState.nextGoalChange = mutate
			}
			modelState.mu.Unlock()
			if !tc.mid {
				if err := mutate(); err != nil {
					t.Fatal("캐시 이후 실제 task SQL 상태 변경 실패")
				}
			}
			bounded, stop := context.WithTimeout(ctx, 10*time.Second)
			defer stop()
			prepareErr := nextSupervisor.PrepareIntents(bounded, strconv.FormatInt(next.TaskID, 10))
			modelState.mu.Lock()
			after, mutationError := modelState.requests, modelState.mutationError
			modelState.nextGoalChange = nil
			modelState.mu.Unlock()
			if mutationError {
				t.Fatal("모델 진행 중 실제 SQL 상태 전환 실패")
			}
			if prepareErr == nil {
				t.Error("실제 SQL task 상태가 달라진 준비를 성공으로 반환함")
			}
			if !tc.mid {
				var written int
				if pool.QueryRowContext(ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind IN ('goal','intent')", next.ExplorationID).Scan(&written) != nil {
					t.Fatal("실제 stale 준비 산출물 조회 실패")
				}
				if after != before || written != 0 {
					t.Errorf("사전 실제 SQL 상태 불일치에서 모델·산출물 쓰기 발생: model_calls=%d nodes=%d", after-before, written)
				}
			} else if after <= before {
				t.Fatal("중간 상태 전환이 실제 모델 호출 중에 일어나지 않음")
			}
			if targetHits.Load() != 0 || nextServer.engine.e2HTTP != nil || nextServer.engine.controlledReads != nil {
				t.Fatal("실패한 준비가 대상 실행 권한으로 승격됨")
			}
			if tc.mid {
				// 분해가 실패해도 계속하는 경로(C11)가 바뀐 task에 intent를 쓰지 않는다.
				var intents int
				if pool.QueryRowContext(ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", next.ExplorationID).Scan(&intents) != nil || intents != 0 {
					t.Fatalf("상태가 바뀐 준비 task에 intent 기록: %d", intents)
				}
			}
		})
	}

	// 목표 분해가 목표를 하나도 만들지 않아도 원래 목표로 계획을 계속한다(P3 결정안 C11 결정 1).
	t.Run("goal-fallback", func(t *testing.T) {
		next, err := PrepareFirstDiagnosisTask(ctx, pool, description, "Prepare a fresh owned fixture without executing it.", profileID, 60)
		if err != nil {
			t.Fatal("새 빈 준비 task 생성 실패")
		}
		manager.LoadExisting()
		nextServer, err := NewFirstDiagnosisPreparationServer(ctx, manager, skillDir, dataDir, keyDir, manifest)
		if err != nil {
			t.Fatal("목표 분해 실패 준비 서버 생성 실패")
		}
		nextSupervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
		if err != nil || nextSupervisor.Attach(nextServer) != nil {
			t.Fatal("목표 분해 실패 준비 감독자 결속 실패")
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			shutdown := nextSupervisor.Shutdown()
			for _, finish := range []func(context.Context, *Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain, shutdown.CheckResults} {
				if finish(cleanup, nextServer) != nil {
					t.Error("목표 분해 실패 준비 writer 정리 실패")
					break
				}
			}
		}()
		modelState.mu.Lock()
		modelState.noGoals = true
		before := map[string]int{}
		for k, v := range modelState.emitted {
			before[k] = v
		}
		modelState.mu.Unlock()
		defer func() {
			modelState.mu.Lock()
			modelState.noGoals = false
			modelState.mu.Unlock()
		}()
		// 실제 진단 로거를 붙여, 분해 실패 기록이 관찰 불완전(완료 실행을 unknown으로 바꾸는 표시)이 되지 않는지 본다.
		logger, err := diagnosticlog.Create(privateDir(), "preparation", next.TaskID, "")
		if err != nil {
			t.Fatal("준비 진단 로거 생성 실패")
		}
		defer logger.Close()
		bounded, stop := context.WithTimeout(diagnosticlog.With(ctx, logger), 20*time.Second)
		defer stop()
		if err := nextSupervisor.PrepareIntents(bounded, strconv.FormatInt(next.TaskID, 10)); err != nil {
			t.Fatal("목표 0개에서 원래 목표로 계속하지 않음")
		}
		if logger.Err() != nil || logger.Incomplete() {
			t.Fatalf("목표 분해 실패 기록이 진단 로그를 깨거나 관찰 불완전으로 표시함: err=%v", logger.Err() != nil)
		}
		var goals, intents int
		if pool.QueryRowContext(ctx, "SELECT count(*) FILTER (WHERE kind='goal'), count(*) FILTER (WHERE kind='intent' AND state='open') FROM exploration_nodes WHERE exploration_id=$1", next.ExplorationID).Scan(&goals, &intents) != nil {
			t.Fatal("목표 분해 실패 준비 산출물 조회 실패")
		}
		modelState.mu.Lock()
		setGoals, addIntent := modelState.emitted["set_goals"]-before["set_goals"], modelState.emitted["add_intent"]-before["add_intent"]
		modelState.mu.Unlock()
		if goals != 0 || intents != 1 || setGoals != 0 || addIntent != 1 {
			t.Fatalf("목표 분해 실패 뒤 계획: goals=%d intents=%d set_goals=%d add_intent=%d", goals, intents, setGoals, addIntent)
		}
		if targetHits.Load() != 0 {
			t.Fatal("준비 단계에서 대상 요청이 발생함")
		}
	})

	// 목표 분해가 목표 만들기 상한을 넘어도(응답 없이 멈춘 모델 턴) 분해만 끊고 원래 목표로 계획을 계속한다(#879).
	// 첫 계획은 준비 단계 상한이 아니라 자기 상한 안에서 돈다.
	t.Run("goal-timeout", func(t *testing.T) {
		next, err := PrepareFirstDiagnosisTask(ctx, pool, description, "Prepare a fresh owned fixture without executing it.", profileID, 60)
		if err != nil {
			t.Fatal("새 빈 준비 task 생성 실패")
		}
		manager.LoadExisting()
		nextServer, err := NewFirstDiagnosisPreparationServer(ctx, manager, skillDir, dataDir, keyDir, manifest)
		if err != nil {
			t.Fatal("목표 분해 실패 준비 서버 생성 실패")
		}
		nextSupervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
		if err != nil || nextSupervisor.Attach(nextServer) != nil {
			t.Fatal("목표 분해 실패 준비 감독자 결속 실패")
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			shutdown := nextSupervisor.Shutdown()
			for _, finish := range []func(context.Context, *Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain, shutdown.CheckResults} {
				if finish(cleanup, nextServer) != nil {
					t.Error("목표 분해 실패 준비 writer 정리 실패")
					break
				}
			}
		}()
		priorGoalTimeout := firstDiagnosisGoalCreationTimeout
		firstDiagnosisGoalCreationTimeout = 2 * time.Second
		defer func() { firstDiagnosisGoalCreationTimeout = priorGoalTimeout }()
		modelState.mu.Lock()
		modelState.stallGoals = true
		before := map[string]int{}
		for k, v := range modelState.emitted {
			before[k] = v
		}
		modelState.mu.Unlock()
		defer func() {
			modelState.mu.Lock()
			modelState.stallGoals = false
			modelState.mu.Unlock()
		}()
		// 실제 진단 로거를 붙여, 분해 실패 기록이 관찰 불완전(완료 실행을 unknown으로 바꾸는 표시)이 되지 않는지 본다.
		logger, err := diagnosticlog.Create(privateDir(), "preparation", next.TaskID, "")
		if err != nil {
			t.Fatal("준비 진단 로거 생성 실패")
		}
		defer logger.Close()
		bounded, stop := context.WithTimeout(diagnosticlog.With(ctx, logger), 20*time.Second)
		defer stop()
		if err := nextSupervisor.PrepareIntents(bounded, strconv.FormatInt(next.TaskID, 10)); err != nil {
			t.Fatal("목표 만들기 상한을 넘은 뒤 원래 목표로 계속하지 않음")
		}
		if logger.Err() != nil || logger.Incomplete() {
			t.Fatalf("목표 분해 실패 기록이 진단 로그를 깨거나 관찰 불완전으로 표시함: err=%v", logger.Err() != nil)
		}
		var goals, intents int
		if pool.QueryRowContext(ctx, "SELECT count(*) FILTER (WHERE kind='goal'), count(*) FILTER (WHERE kind='intent' AND state='open') FROM exploration_nodes WHERE exploration_id=$1", next.ExplorationID).Scan(&goals, &intents) != nil {
			t.Fatal("목표 분해 실패 준비 산출물 조회 실패")
		}
		modelState.mu.Lock()
		setGoals, addIntent := modelState.emitted["set_goals"]-before["set_goals"], modelState.emitted["add_intent"]-before["add_intent"]
		modelState.mu.Unlock()
		if goals != 0 || intents != 1 || setGoals != 0 || addIntent != 1 {
			t.Fatalf("목표 만들기 상한 뒤 계획: goals=%d intents=%d set_goals=%d add_intent=%d", goals, intents, setGoals, addIntent)
		}
		if targetHits.Load() != 0 {
			t.Fatal("준비 단계에서 대상 요청이 발생함")
		}
	})

	// 목표 하나를 저장한 뒤 다음 목표 분해 턴이 상한에 걸리면, 저장된 목표로 첫 계획을 계속한다(부분 저장 허용, #879 리뷰).
	t.Run("goal-timeout-partial", func(t *testing.T) {
		next, err := PrepareFirstDiagnosisTask(ctx, pool, description, "Prepare a fresh owned fixture without executing it.", profileID, 60)
		if err != nil {
			t.Fatal("새 빈 준비 task 생성 실패")
		}
		manager.LoadExisting()
		nextServer, err := NewFirstDiagnosisPreparationServer(ctx, manager, skillDir, dataDir, keyDir, manifest)
		if err != nil {
			t.Fatal("목표 분해 실패 준비 서버 생성 실패")
		}
		nextSupervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
		if err != nil || nextSupervisor.Attach(nextServer) != nil {
			t.Fatal("목표 분해 실패 준비 감독자 결속 실패")
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			shutdown := nextSupervisor.Shutdown()
			for _, finish := range []func(context.Context, *Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain, shutdown.CheckResults} {
				if finish(cleanup, nextServer) != nil {
					t.Error("목표 분해 실패 준비 writer 정리 실패")
					break
				}
			}
		}()
		priorGoalTimeout := firstDiagnosisGoalCreationTimeout
		firstDiagnosisGoalCreationTimeout = 2 * time.Second
		defer func() { firstDiagnosisGoalCreationTimeout = priorGoalTimeout }()
		modelState.mu.Lock()
		modelState.stallAfterGoal = true
		before := map[string]int{}
		for k, v := range modelState.emitted {
			before[k] = v
		}
		modelState.mu.Unlock()
		defer func() {
			modelState.mu.Lock()
			modelState.stallAfterGoal = false
			modelState.mu.Unlock()
		}()
		// 실제 진단 로거를 붙여, 분해 실패 기록이 관찰 불완전(완료 실행을 unknown으로 바꾸는 표시)이 되지 않는지 본다.
		logger, err := diagnosticlog.Create(privateDir(), "preparation", next.TaskID, "")
		if err != nil {
			t.Fatal("준비 진단 로거 생성 실패")
		}
		defer logger.Close()
		bounded, stop := context.WithTimeout(diagnosticlog.With(ctx, logger), 20*time.Second)
		defer stop()
		if err := nextSupervisor.PrepareIntents(bounded, strconv.FormatInt(next.TaskID, 10)); err != nil {
			t.Fatal("목표 만들기 상한을 넘은 뒤 원래 목표로 계속하지 않음")
		}
		if logger.Err() != nil || logger.Incomplete() {
			t.Fatalf("목표 분해 실패 기록이 진단 로그를 깨거나 관찰 불완전으로 표시함: err=%v", logger.Err() != nil)
		}
		var goals, intents int
		if pool.QueryRowContext(ctx, "SELECT count(*) FILTER (WHERE kind='goal'), count(*) FILTER (WHERE kind='intent' AND state='open') FROM exploration_nodes WHERE exploration_id=$1", next.ExplorationID).Scan(&goals, &intents) != nil {
			t.Fatal("목표 분해 실패 준비 산출물 조회 실패")
		}
		modelState.mu.Lock()
		setGoals, addIntent := modelState.emitted["set_goals"]-before["set_goals"], modelState.emitted["add_intent"]-before["add_intent"]
		modelState.mu.Unlock()
		// 진단 로그의 목표 만들기 단계는 '원래 목표로 계속'(WARN failed)이 아니라 성공이어야 실제 상태와 같다.
		raw, err := os.ReadFile(logger.Path())
		if err != nil {
			t.Fatal("준비 진단 로그 읽기 실패")
		}
		var goalStatus []string
		for _, line := range bytes.Split(raw, []byte("\n")) {
			var event struct {
				Event, Stage, Status string
			}
			if json.Unmarshal(line, &event) == nil && event.Event == "stage_finished" && event.Stage == "goal_creation" {
				goalStatus = append(goalStatus, event.Status)
			}
		}
		if len(goalStatus) != 1 || goalStatus[0] != "succeeded" {
			t.Fatalf("부분 저장 뒤 목표 만들기 단계 기록: %v", goalStatus)
		}
		if goals != 1 || intents != 1 || setGoals != 1 || addIntent != 1 {
			t.Fatalf("목표 일부 저장 뒤 상한 계획: goals=%d intents=%d set_goals=%d add_intent=%d", goals, intents, setGoals, addIntent)
		}
		if targetHits.Load() != 0 {
			t.Fatal("준비 단계에서 대상 요청이 발생함")
		}
	})

	// 첫 계획에서 intent가 0개여도 준비는 실패하지 않는다. 실행 단계 Planner가 다시 계획하고, 그래도 없으면
	// no_intents로 마감한다(C11 결정 2, C08 2-4).
	t.Run("no-intents", func(t *testing.T) {
		next, err := PrepareFirstDiagnosisTask(ctx, pool, description, "Prepare a fresh owned fixture that needs no intent.", profileID, 60)
		if err != nil {
			t.Fatal("새 빈 준비 task 생성 실패")
		}
		manager.LoadExisting()
		nextServer, err := NewFirstDiagnosisPreparationServer(ctx, manager, skillDir, dataDir, keyDir, manifest)
		if err != nil {
			t.Fatal("intent 0개 준비 서버 생성 실패")
		}
		nextSupervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
		if err != nil || nextSupervisor.Attach(nextServer) != nil {
			t.Fatal("intent 0개 준비 감독자 결속 실패")
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			shutdown := nextSupervisor.Shutdown()
			for _, finish := range []func(context.Context, *Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain, shutdown.CheckResults} {
				if finish(cleanup, nextServer) != nil {
					t.Error("intent 0개 준비 writer 정리 실패")
					break
				}
			}
		}()
		modelState.mu.Lock()
		modelState.noIntents = true
		before := modelState.emitted["add_intent"]
		modelState.mu.Unlock()
		defer func() {
			modelState.mu.Lock()
			modelState.noIntents = false
			modelState.mu.Unlock()
		}()
		logger, err := diagnosticlog.Create(privateDir(), "preparation", next.TaskID, "")
		if err != nil {
			t.Fatal("준비 진단 로거 생성 실패")
		}
		defer logger.Close()
		bounded, stop := context.WithTimeout(diagnosticlog.With(ctx, logger), 20*time.Second)
		defer stop()
		if err := nextSupervisor.PrepareIntents(bounded, strconv.FormatInt(next.TaskID, 10)); err != nil {
			t.Fatal("intent 0개 준비를 실패로 끝냄")
		}
		if logger.Err() != nil || logger.Incomplete() {
			t.Fatal("intent 0개 준비가 진단 로그를 깨거나 관찰 불완전으로 표시함")
		}
		// intent 0개는 실패가 아니므로 실패 단계 기록도 남기지 않는다.
		file, err := os.Open(logger.Path())
		if err != nil {
			t.Fatal("준비 진단 로그 열기 실패")
		}
		defer file.Close()
		for decoder := json.NewDecoder(file); ; {
			var event diagnosticlog.Event
			if err := decoder.Decode(&event); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal("준비 진단 로그 손상")
			}
			if event.Status == "failed" {
				t.Fatalf("intent 0개 준비에 실패 기록: stage=%s reason=%s", event.Stage, event.ReasonCode)
			}
		}
		var intents int
		if pool.QueryRowContext(ctx, "SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent'", next.ExplorationID).Scan(&intents) != nil {
			t.Fatal("intent 0개 준비 산출물 조회 실패")
		}
		modelState.mu.Lock()
		addIntent := modelState.emitted["add_intent"] - before
		modelState.mu.Unlock()
		if intents != 0 || addIntent != 0 || targetHits.Load() != 0 {
			t.Fatalf("intent 0개 준비 산출물: intents=%d add_intent=%d", intents, addIntent)
		}
	})
	// 준비 계획도 설치한 Plan당 턴 상한을 쓴다(C26·C32). 준비 서버에는 E② 실행 Runtime이 없어 준비 설정 값으로 받는다.
	t.Run("installed-plan-turn-limit", func(t *testing.T) {
		const installedPlanTurns = 2
		next, err := PrepareFirstDiagnosisTask(ctx, pool, description, "Prepare a fresh owned fixture under a short plan.", profileID, 60)
		if err != nil {
			t.Fatal("새 빈 준비 task 생성 실패")
		}
		manager.LoadExisting()
		if _, err := NewFirstDiagnosisPreparationServerWithModel(ctx, manager, skillDir, dataDir, keyDir, FirstDiagnosisModelOptions{LocalManifestPath: manifest, PlannerLimits: agent.E2HTTPPlannerLimits{MaxTurns: -1}}); err == nil {
			t.Fatal("범위 밖 Plan당 상한으로 준비 서버를 만듦")
		}
		nextServer, err := NewFirstDiagnosisPreparationServerWithModel(ctx, manager, skillDir, dataDir, keyDir, FirstDiagnosisModelOptions{LocalManifestPath: manifest, PlannerLimits: agent.E2HTTPPlannerLimits{MaxTurns: installedPlanTurns}})
		if err != nil {
			t.Fatal("Plan 상한 준비 서버 생성 실패")
		}
		nextSupervisor, err := NewFirstDiagnosisSupervisor(ctx, manager)
		if err != nil || nextSupervisor.Attach(nextServer) != nil {
			t.Fatal("Plan 상한 준비 감독자 결속 실패")
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			shutdown := nextSupervisor.Shutdown()
			for _, finish := range []func(context.Context, *Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain, shutdown.CheckResults} {
				if finish(cleanup, nextServer) != nil {
					t.Error("Plan 상한 준비 writer 정리 실패")
					break
				}
			}
		}()
		modelState.mu.Lock()
		modelState.loopPlan, modelState.planMin, modelState.planMax, modelState.planWrapUps = true, 0, 0, 0
		modelState.mu.Unlock()
		defer func() {
			modelState.mu.Lock()
			modelState.loopPlan = false
			modelState.mu.Unlock()
		}()
		bounded, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		if err := nextSupervisor.PrepareIntents(bounded, strconv.FormatInt(next.TaskID, 10)); err != nil {
			t.Fatal("Plan 상한 준비를 실패로 끝냄")
		}
		modelState.mu.Lock()
		planMin, planMax, wrapUps := modelState.planMin, modelState.planMax, modelState.planWrapUps
		modelState.mu.Unlock()
		// 턴마다 assistant·도구 결과 두 메시지가 붙는다. 2턴 상한이면 마지막 계획 요청이 첫 요청보다 2개 많다(기본 16턴이면 30개).
		// 턴 상한에 닿으면 원본처럼 "이번 라운드만 끝" 마무리 턴을 받는다(C26).
		if planMin == 0 || planMax-planMin != 2*(installedPlanTurns-1) || wrapUps == 0 || targetHits.Load() != 0 {
			t.Fatalf("준비 계획이 설치한 Plan당 턴 상한·마무리 턴을 쓰지 않음 messages=%d..%d wrapUps=%d", planMin, planMax, wrapUps)
		}
	})
	// 원본 작업 시작의 대상 자산 등록(seed)을 첫 진단 준비에서 실제 실행 역할로 한다(C24). 승인 대상 URL의 origin 하나만
	// 등록하고(경로·query는 버림) 작업에 source task로 연결한다. 잘못된 URL은 아무것도 남기지 않는다.
	t.Run("target-asset-seed", func(t *testing.T) {
		seeded, err := PrepareFirstDiagnosisTask(ctx, pool, description, "Seed the approved target asset.", profileID, 60)
		if err != nil {
			t.Fatal("seed 시험 task 생성 실패")
		}
		count := func(query string, args ...any) int {
			t.Helper()
			var n int
			if err := installer.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
				t.Fatal("seed 결과 조회 실패", err)
			}
			return n
		}
		for _, bad := range []string{"", "ftp://seed.test/", "https://user:pw@seed.test/", "https:///nohost", "https://seed.test:0/"} {
			if SeedFirstDiagnosisTarget(ctx, pool, seeded, bad) == nil {
				t.Fatalf("잘못된 대상 URL %q를 등록함", bad)
			}
		}
		if count("SELECT count(*) FROM task_asset_links WHERE task_id=$1", seeded.TaskID) != 0 {
			t.Fatal("잘못된 대상 URL이 자산을 남김")
		}
		if err := SeedFirstDiagnosisTarget(ctx, pool, seeded, "https://Seed.Test:8443/app/login?next=/x"); err != nil {
			t.Fatal("승인 대상 자산 등록 실패", err)
		}
		if err := SeedFirstDiagnosisTarget(ctx, pool, seeded, "http://192.0.2.44:8080/"); err != nil {
			t.Fatal("IP 대상 자산 등록 실패", err)
		}
		// 하위 도메인 대상이면 그 루트 도메인은 작업에 붙지 않는다(승인 밖).
		if err := SeedFirstDiagnosisTarget(ctx, pool, seeded, "https://app.seed-root.test/"); err != nil {
			t.Fatal("하위 도메인 대상 자산 등록 실패", err)
		}
		// 원본 upsert는 서비스 자산과 함께 그 루트 도메인 자산도 만들어 연결한다(출처 system). 출처 task 연결은 등록한
		// 두 대상이고, 연결된 자산은 모두 대상 호스트 안이다.
		if count("SELECT count(*) FROM assets a JOIN task_asset_links l ON l.asset_id=a.id WHERE l.task_id=$1 AND l.source='task' AND a.type='service' AND a.url='https://seed.test:8443'", seeded.TaskID) != 1 ||
			count("SELECT count(*) FROM assets a JOIN task_asset_links l ON l.asset_id=a.id WHERE l.task_id=$1 AND l.source='task' AND a.type='ip' AND a.ip='192.0.2.44'", seeded.TaskID) != 1 ||
			count("SELECT count(*) FROM task_asset_links WHERE task_id=$1 AND source='task'", seeded.TaskID) != 3 ||
			count("SELECT count(*) FROM task_asset_links l JOIN assets a ON a.id=l.asset_id WHERE l.task_id=$1 AND COALESCE(a.domain, a.ip) NOT IN ('seed.test','192.0.2.44','app.seed-root.test')", seeded.TaskID) != 0 {
			t.Fatal("승인 대상 origin만 작업에 연결하지 않음")
		}
		// 작업 없이 등록하면 어느 작업에도 연결되지 않은 전역 자산만 남으므로 받지 않는다.
		if SeedFirstDiagnosisTarget(ctx, pool, FirstDiagnosisPreparedTask{ExplorationID: seeded.ExplorationID}, "https://unlinked-seed.test/") == nil ||
			count("SELECT count(*) FROM assets WHERE url LIKE $1", "%unlinked-seed.test%") != 0 {
			t.Fatal("작업 없이 자산을 등록함")
		}
	})
	// #912 러너 재현(11/25): 준비 서비스가 점검 도중 죽어 drainTask를 못 하면 그 작업이 paused=false로 남고, 새로 만든 준비
	// 서버는 "다른 실행을 자동 복원하지 않는다" 검사로 시작을 거부했다. 이전 회차가 결속했다고 기록된 작업만 마감 상태로
	// 돌리고 끊김 표지를 남긴다. 기록에 없는 작업은 그대로 두어 준비 서버가 계속 거부한다(#927 이동찬 P1).
	t.Run("interrupted-task-release", func(t *testing.T) {
		create := func(goal string) FirstDiagnosisPreparedTask {
			t.Helper()
			made, err := PrepareFirstDiagnosisTask(ctx, pool, description, goal, profileID, 60)
			if err != nil {
				t.Fatal("끊김 시험 task 생성 실패")
			}
			return made
		}
		state := func(id int64) (status string, paused, queued bool) {
			t.Helper()
			if err := installer.QueryRowContext(ctx, `SELECT status,paused,queued FROM tasks WHERE id=$1`, id).Scan(&status, &paused, &queued); err != nil {
				t.Fatal("끊김 시험 task 조회 실패", err)
			}
			return status, paused, queued
		}
		markers := func(task FirstDiagnosisPreparedTask) int {
			t.Helper()
			var n int
			if err := installer.QueryRowContext(ctx, `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND origin=$2`, task.ExplorationID, FirstDiagnosisInterruptedOrigin).Scan(&n); err != nil {
				t.Fatal("끊김 표지 조회 실패", err)
			}
			return n
		}
		// 결속 기록에 있는 것: 강제 종료된 회차(감독자가 풀어 둔 채 죽음)·대기열 잔여·이미 끝난 작업·지운 작업·탐색 ID가
		// 다른 기록. 결속 기록에 없는 것: 다른 실행·대기 작업.
		killed, waiting, finished, deleted, mismatched := create("Interrupted run."), create("Queued leftover."), create("Finished run."), create("Deleted run."), create("Mismatched record.")
		otherRun, otherQueued := create("Unrelated running task."), create("Unrelated queued task.")
		for _, q := range []struct {
			query string
			id    int64
		}{
			{`UPDATE tasks SET paused=false WHERE id=$1`, killed.TaskID},
			{`UPDATE tasks SET queued=true WHERE id=$1`, waiting.TaskID},
			{`UPDATE tasks SET paused=false,status='done' WHERE id=$1`, finished.TaskID},
			{`UPDATE tasks SET paused=false,deleted_at=now() WHERE id=$1`, deleted.TaskID},
			{`UPDATE tasks SET paused=false WHERE id=$1`, mismatched.TaskID},
			{`UPDATE tasks SET paused=false WHERE id=$1`, otherRun.TaskID},
			{`UPDATE tasks SET queued=true WHERE id=$1`, otherQueued.TaskID},
		} {
			if _, err := installer.ExecContext(ctx, q.query, q.id); err != nil {
				t.Fatal("끊김 상태 준비 실패", err)
			}
		}
		current := create("Prepare after the interrupted run.")
		bound := func(tasks ...FirstDiagnosisPreparedTask) []FirstDiagnosisBoundTask {
			out := make([]FirstDiagnosisBoundTask, 0, len(tasks))
			for _, task := range tasks {
				out = append(out, FirstDiagnosisBoundTask{TaskID: task.TaskID, ExplorationID: task.ExplorationID})
			}
			return out
		}
		previous := append(bound(killed, waiting, finished, deleted), FirstDiagnosisBoundTask{TaskID: mismatched.TaskID, ExplorationID: otherRun.ExplorationID})
		newServer := func() (*Server, error) {
			t.Helper()
			// 실제 준비 실행처럼 점검마다 새 관리자다(빈 목록이라 PG의 작업을 모두 읽는다). Close는 공유 pool을 닫으므로 하지 않는다.
			fresh, err := NewFirstDiagnosisManager(ctx, dataDir, pool)
			if err != nil {
				t.Fatal("끊김 시험 관리자 생성 실패")
			}
			return NewFirstDiagnosisPreparationServerWithModel(ctx, fresh, skillDir, dataDir, keyDir, FirstDiagnosisModelOptions{LocalManifestPath: manifest, PlannerLimits: agent.E2HTTPPlannerLimits{MaxTurns: 2}})
		}
		if _, err := newServer(); err == nil {
			t.Fatal("끊긴 작업이 남았는데 준비 서버가 시작됨(재현 조건이 안 맞음)")
		}
		for name, args := range map[string]struct {
			current  int64
			previous []FirstDiagnosisBoundTask
		}{
			"no-current":       {0, previous},
			"current-in-list":  {current.TaskID, bound(current)},
			"invalid-previous": {current.TaskID, []FirstDiagnosisBoundTask{{TaskID: killed.TaskID}}},
		} {
			if _, err := ReleaseInterruptedFirstDiagnosisTasks(ctx, pool, args.current, args.previous); err == nil {
				t.Fatalf("%s: 잘못된 입력으로 정리함", name)
			}
		}
		if released, err := ReleaseInterruptedFirstDiagnosisTasks(ctx, pool, current.TaskID, nil); err != nil || released != 0 {
			t.Fatalf("결속 기록 없이 정리 released=%d err=%v", released, err)
		}
		released, err := ReleaseInterruptedFirstDiagnosisTasks(ctx, pool, current.TaskID, previous)
		if err != nil || released != 2 {
			t.Fatalf("끊긴 작업 정리 released=%d err=%v", released, err)
		}
		for _, c := range []struct {
			task           FirstDiagnosisPreparedTask
			status         string
			paused, queued bool
			markers        int
		}{
			{killed, "created", true, false, 1}, {waiting, "created", true, false, 1}, {finished, "done", false, false, 0},
			{deleted, "created", false, false, 0}, {mismatched, "created", false, false, 0},
			{otherRun, "created", false, false, 0}, {otherQueued, "created", true, true, 0}, {current, "created", true, false, 0},
		} {
			if status, paused, queued := state(c.task.TaskID); status != c.status || paused != c.paused || queued != c.queued {
				t.Fatalf("task %d 정리 뒤 status=%s paused=%t queued=%t", c.task.TaskID, status, paused, queued)
			}
			if got := markers(c.task); got != c.markers {
				t.Fatalf("task %d 끊김 표지 %d개, 기대 %d", c.task.TaskID, got, c.markers)
			}
		}
		// 결속 기록에 없는 작업이 남았으므로 준비 서버는 계속 거부한다.
		if _, err := newServer(); err == nil {
			t.Fatal("결속 기록에 없는 실행·대기 작업이 남았는데 준비 서버가 시작됨")
		}
		// 보고서는 끊긴 작업을 진행 중·완료가 아니라 고정 사유로 보인다. 표지 없는 작업은 종료 기록 없음 그대로다.
		tx, err := pool.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		interrupted, errA := readDiagnosticRun(ctx, tx, killed.TaskID)
		untouched, errB := readDiagnosticRun(ctx, tx, otherRun.TaskID)
		_ = tx.Rollback()
		if errA != nil || errB != nil || interrupted == nil || interrupted.State != report.DiagnosticRunInterrupted || untouched != nil ||
			report.DiagnosticRunText(interrupted) != "준비 서비스 종료로 중단됨(실행 결과를 확인하지 못함)" {
			t.Fatalf("끊김 표지 보고서 읽기 interrupted=%+v untouched=%+v", interrupted, untouched)
		}
		if again, err := ReleaseInterruptedFirstDiagnosisTasks(ctx, pool, current.TaskID, previous); err != nil || again != 0 || markers(killed) != 1 {
			t.Fatalf("두 번째 정리 released=%d err=%v", again, err)
		}
		// 다른 실행의 주인이 그 작업을 마감한 뒤에는 준비 서버가 시작된다.
		for _, id := range []int64{otherRun.TaskID, otherQueued.TaskID, mismatched.TaskID} {
			if _, err := installer.ExecContext(ctx, `UPDATE tasks SET paused=true,queued=false WHERE id=$1`, id); err != nil {
				t.Fatal("다른 작업 마감 실패", err)
			}
		}
		server, err := newServer()
		if err != nil {
			t.Fatal("끊긴 작업 정리 뒤에도 준비 서버 시작 실패", err)
		}
		fresh, err := NewFirstDiagnosisSupervisor(ctx, server.m)
		if err != nil || fresh.Attach(server) != nil {
			t.Fatal("끊김 시험 준비 감독자 결속 실패")
		}
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		shutdown := fresh.Shutdown()
		for _, finish := range []func(context.Context, *Server) error{shutdown.BlockAdmission, shutdown.CancelAndDrain, shutdown.CheckResults} {
			if finish(cleanup, server) != nil {
				t.Fatal("끊김 시험 준비 writer 정리 실패")
			}
		}
	})
}
