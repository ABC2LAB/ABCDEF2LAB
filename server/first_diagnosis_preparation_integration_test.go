//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/rand"
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

	pgdb "github.com/Autumn-27/artex/db"
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
	var id, name string
	var input any
	if goalRound {
		switch {
		case !seen["preparation-scope"]:
			id, name, input = "preparation-scope", "add_task_scope", map[string]any{"kind": "ip", "value": "127.0.0.1", "reason": "The operator explicitly limited this owned fixture to IP 127.0.0.1."}
		case !seen["preparation-constraints"]:
			id, name, input = "preparation-constraints", "set_constraints", map[string]any{"constraints": []map[string]string{{"type": "deny", "text": "Do not send target requests during preparation."}, {"type": "allow", "text": "Prepare only GET for " + m.target + "/owned."}}}
		case !seen["preparation-goals"]:
			id, name, input = "preparation-goals", "set_goals", map[string]any{"text": "Prepare the first controlled GET of the owned fixture without executing it.", "vulnclass": "IDOR"}
		}
	} else {
		switch {
		case !seen["preparation-assets"]:
			id, name, input = "preparation-assets", "list_assets", map[string]any{"dsl": "ip==127.0.0.1", "type": "ip", "limit": 5}
		case !seen["preparation-intent"]:
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
		})
	}

}
