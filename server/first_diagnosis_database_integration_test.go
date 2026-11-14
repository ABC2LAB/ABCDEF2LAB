//go:build linux

package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// 설치자는 일회용 시험 DB만 초기화한다. 제품의 실행 역할에는 DDL을 부여하지 않는다.
func TestFirstDiagnosisRuntimeDatabaseOwnedPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("독립 실행 역할 PostgreSQL DSN이 제공되지 않음")
	}
	if os.Getenv("ARTEX_FIRST_DIAGNOSIS_ROLE_TEST_OWNED") != "1" {
		t.Fatal("권한 시험 전용 일회용 DB 소유 확인이 필요함")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	installer, err := pgdb.Open(dsn)
	if err != nil {
		t.Fatal("권한 시험 설치자 DB 연결 실패")
	}
	defer installer.Close()
	for _, migrate := range []func(context.Context, *sql.DB) error{auditcheckpoint.Migrate, auditkeys.Migrate, evidencerevision.Migrate} {
		if err := migrate(ctx, installer.DB); err != nil {
			t.Fatal("전용 DB의 명시적 설치 migration 실패")
		}
	}
	var dbName, installerName string
	if err := installer.QueryRowContext(ctx, "SELECT current_database(),current_user").Scan(&dbName, &installerName); err != nil {
		t.Fatal("설치자 신원 조회 실패")
	}
	exec := func(statement string) {
		t.Helper()
		if _, err := installer.ExecContext(ctx, statement); err != nil {
			t.Fatal("소유 PostgreSQL fixture 설정 실패")
		}
	}
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	exec("REVOKE CREATE,TEMP ON DATABASE " + ident(dbName) + " FROM PUBLIC")
	exec("REVOKE CREATE ON SCHEMA public FROM PUBLIC")
	suffix := make([]byte, 10)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	taskDescription := "Owned synthetic integration task " + hex.EncodeToString(suffix)
	role := "first_diag_rt_" + hex.EncodeToString(suffix)
	elevated := "first_diag_elev_" + hex.EncodeToString(suffix)
	password := make([]byte, 24)
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	exec("CREATE ROLE " + ident(role) + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + hex.EncodeToString(password) + "'")
	// 무작위 소유 role만 정리한다. 기존 role·schema·데이터는 정리 대상으로 삼지 않는다.
	defer func() {
		if _, err := installer.ExecContext(context.Background(), "DROP OWNED BY "+ident(role)); err != nil {
			t.Error("시험 runtime role 소유권 정리 실패")
		}
		if _, err := installer.ExecContext(context.Background(), "DROP ROLE "+ident(role)); err != nil {
			t.Error("시험 runtime role 정리 실패")
		}
	}()
	exec("GRANT CONNECT ON DATABASE " + ident(dbName) + " TO " + ident(role))
	exec("GRANT USAGE ON SCHEMA public TO " + ident(role))
	runtimeConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("전용 runtime 연결 구성 실패")
	}
	runtimeConfig.User = role
	runtimeConfig.Password = hex.EncodeToString(password)
	runtimeConfig.RuntimeParams["search_path"] = "public"
	openRuntime := func() *sql.DB {
		t.Helper()
		pool := stdlib.OpenDB(*runtimeConfig)
		pool.SetMaxOpenConns(2)
		if err := pool.PingContext(ctx); err != nil {
			pool.Close()
			t.Fatal("독립 runtime LOGIN 연결 실패")
		}
		return pool
	}
	runtimePool := openRuntime()
	defer runtimePool.Close()
	var currentUser, sessionUser string
	if err := runtimePool.QueryRowContext(ctx, "SELECT current_user,session_user").Scan(&currentUser, &sessionUser); err != nil || currentUser != role || sessionUser != role {
		t.Fatal("SET ROLE이 아닌 독립 runtime 로그인 신원을 확인할 수 없음")
	}
	assertRejected := func(t *testing.T, pool *sql.DB) {
		t.Helper()
		if err := CheckFirstDiagnosisRuntimeDatabase(ctx, pool); !errors.Is(err, ErrControlledReadHostConfiguration) {
			t.Fatal("부적합 권한이 고정 설정 오류로 거부되지 않음")
		}
	}
	t.Run("ungranted-runtime", func(t *testing.T) { assertRejected(t, runtimePool) })
	// 실제 서버 SQL 경로의 최소 권한이다. 권한검사 구현에서 목록을 가져오지 않는다.
	for _, name := range []string{"tasks", "explorations", "exploration_nodes", "task_llm_profiles", "task_control_modes", "findings", "request_approvals", "e2_prompt_runs", "e2_prompt_observations"} {
		exec("GRANT SELECT,INSERT,UPDATE ON TABLE " + ident(name) + " TO " + ident(role))
	}
	for _, name := range []string{"traffic_evidence_snapshots", "finding_traffic_bindings", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links", "evidence_revision_initials", "evidence_revision_operations", "exploration_edges", "activity", "task_scope", "task_constraints", "exploration_anchors"} {
		exec("GRANT SELECT,INSERT ON TABLE " + ident(name) + " TO " + ident(role))
	}
	for _, name := range []string{"llm_profiles", "settings", "agents", "agent_prompts", "audit_checkpoint_epochs", "audit_checkpoint_heads", "audit_checkpoint_history", "audit_checkpoint_operations", "task_categories", "task_relations", "task_archives", "intercept_rules", "assets", "companies", "company_scope", "task_asset_links", "asset_intercept_rules", "task_intercept_rules"} {
		exec("GRANT SELECT ON TABLE " + ident(name) + " TO " + ident(role))
	}
	exec("GRANT UPDATE(id) ON TABLE e3_evidence_manifests,e3_verdict_issuances TO " + ident(role))
	exec("GRANT UPDATE(id) ON assets TO " + ident(role))
	exec("GRANT UPDATE(task_id) ON task_asset_links TO " + ident(role))
	exec("GRANT UPDATE(node_id) ON exploration_anchors TO " + ident(role))

	exec("GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO " + ident(role))
	// 일반 HTTP 실행 역할만 진단 Worker의 insert_assets(C24)로 자산과 작업 연결을 쓴다. 이 시험은 한 역할로 등록 조회와
	// 일반 HTTP를 함께 돌리므로, 일반 HTTP 실행을 돌리는 동안만 주고 등록 조회 검사 전에 거둔다. 목록은 구현에서 가져오지
	// 않는다.
	assetWrites := "GRANT INSERT, UPDATE(auth, bound_domains, c_segment, company_id, company_source, content_length, domain, extra, favicon_mmh3, icp, ip, last_seen, open_ports, page_title, params, record_value, status_code, task_ids, technologies) ON assets TO " + ident(role)
	linkWrites := "GRANT INSERT, DELETE, UPDATE(source, source_summary, source_node_id) ON task_asset_links TO " + ident(role)
	revokeAssetWrites := func() {
		exec("REVOKE ALL ON assets, task_asset_links FROM " + ident(role))
		exec("GRANT SELECT, UPDATE(id) ON assets TO " + ident(role))
		exec("GRANT SELECT, UPDATE(task_id) ON task_asset_links TO " + ident(role))
	}
	t.Run("runtime-login-positive", func(t *testing.T) {
		if err := CheckFirstDiagnosisRuntimeDatabase(ctx, runtimePool); err != nil {
			t.Fatal("분리된 최소 실행 역할을 수용하지 않음")
		}
		// 자산 쓰기가 없으면 일반 HTTP 실행 역할로 받지 않는다. 등록 조회 역할은 같은 쓰기 집합을 가져도 받는다.
		if err := CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, runtimePool); !errors.Is(err, ErrControlledReadHostConfiguration) {
			t.Fatal("자산 쓰기 없는 역할을 일반 HTTP 실행 역할로 수용함")
		}
		exec(assetWrites)
		exec(linkWrites)
		defer revokeAssetWrites()
		if err := CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, runtimePool); err != nil {
			t.Fatal("자산 쓰기를 가진 일반 HTTP 실행 역할을 수용하지 않음")
		}
		if err := CheckFirstDiagnosisRuntimeDatabase(ctx, runtimePool); err != nil {
			t.Fatal("같은 자산 쓰기 집합을 가진 등록 조회 역할을 수용하지 않음")
		}
		for _, extra := range []string{"GRANT UPDATE(type) ON assets TO " + ident(role), "GRANT UPDATE(created_at) ON task_asset_links TO " + ident(role), "GRANT TRUNCATE ON task_asset_links TO " + ident(role), "GRANT DELETE ON assets TO " + ident(role)} {
			exec(extra)
			if err := CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, runtimePool); !errors.Is(err, ErrControlledReadHostConfiguration) {
				t.Fatalf("허용 밖 자산 권한을 수용함: %s", extra)
			}
			assertRejected(t, runtimePool)
			revokeAssetWrites()
			exec(assetWrites)
			exec(linkWrites)
		}
		// 칼럼 갱신 권한만 있고 자산 INSERT나 연결 DELETE(동기화 트리거가 씀)가 빠져도 받지 않는다.
		for _, missing := range []string{"REVOKE INSERT ON assets FROM " + ident(role), "REVOKE DELETE ON task_asset_links FROM " + ident(role), "REVOKE INSERT ON task_asset_links FROM " + ident(role)} {
			exec(missing)
			if err := CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, runtimePool); !errors.Is(err, ErrControlledReadHostConfiguration) {
				t.Fatalf("빠진 자산 권한을 수용함: %s", missing)
			}
			revokeAssetWrites()
			exec(assetWrites)
			exec(linkWrites)
		}
		if err := CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, runtimePool); err != nil {
			t.Fatal("허용 밖 권한을 거둔 뒤 일반 HTTP 실행 역할을 다시 수용하지 않음")
		}
		if _, err := runtimePool.ExecContext(ctx, "CREATE TEMP TABLE first_diag_forbidden_temp(id int)"); err == nil {
			t.Fatal("실행 역할의 TEMP 생성이 허용됨")
		}
		if _, err := runtimePool.ExecContext(ctx, "DELETE FROM audit_checkpoint_epochs WHERE false"); err == nil {
			t.Fatal("실행 역할의 epoch 삭제가 허용됨")
		}
		taskPool := openRuntime()
		manager, err := NewFirstDiagnosisManager(ctx, t.TempDir(), taskPool)
		if err != nil {
			taskPool.Close()
			t.Fatal("실제 runtime pool로 무DDL Manager 조립 실패")
		}
		defer manager.Close()
		if _, err := manager.interceptor.CheckToolEnabled("Bash"); err != nil {
			t.Fatal("실제 runtime Guard 설정을 읽지 못함")
		}
		if manager.pg.DB != taskPool {
			t.Fatal("Manager가 검사한 runtime pool을 교체함")
		}
		task, err := manager.CreateTask(taskDescription, "Verify runtime database privileges", nil, 60, 60)
		if err != nil || task == nil || task.Store == nil {
			t.Fatal("최소 runtime 역할로 실제 task·exploration 생성 실패")
		}
		intentID, err := task.Store.AddIntent(map[string]any{"action": "Verify the registered runtime path"}, 1, nil, "synthetic acceptance")
		if err != nil || intentID <= 0 {
			t.Fatal("동일 runtime Store로 실제 intent 생성 실패")
		}
		frontier, frontierErr := task.Store.Frontier(20)
		if frontierErr != nil || len(frontier) != 1 {
			t.Fatal("실제 runtime Store Frontier 조회 실패")
		}
		claimed, claimErr := task.Store.ClaimIntent(intentID, "synthetic-preflight")
		if claimErr != nil || !claimed {
			t.Fatalf("실제 runtime Store의 intent 획득 실패: %v", claimErr)
		}
		if _, err := runtimePool.ExecContext(ctx, "UPDATE exploration_nodes SET state='open',owner=NULL WHERE id=$1 AND exploration_id=$2", intentID, task.ExpID); err != nil {
			t.Fatal("소유 fixture intent preflight 복구 실패")
		}
		row, readErr := manager.pg.GetTask(mustFirstDiagnosisTaskNumber(task.ID))
		if readErr != nil || row == nil || row.ExplorationID != task.ExpID {
			t.Fatal("실제 startup GetTask가 최소 runtime 권한으로 읽히지 않음")
		}
		t.Run("actual-exploration-model-budget-before-diagnosis", func(t *testing.T) {
			// 기존 역할/constructor 시험의 원래 task는 재사용하지 않는다.
			prior := task.lifecycleSnapshot()
			if !prior.Paused {
				if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", task.ID); err != nil {
					t.Fatal("owned preflight pause failed")
				}
				task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = true })
				defer func() {
					_, _ = runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=$1 WHERE id=$2", prior.Paused, task.ID)
					task.updateLifecycle(func(s *taskLifecycleState) { s.Paused = prior.Paused })
				}()
			}
			owned, err := manager.CreateTask("Owned exploration budget closure", "Preserve pre-diagnostic budget outcome", nil, 60, 60)
			if err != nil || owned == nil {
				t.Fatal("owned budget task create failed")
			}
			if _, err = owned.Store.AddIntent(map[string]any{"summary": "Owned unstarted diagnostic intent"}, 1, nil, "system"); err != nil {
				t.Fatal("owned budget intent create failed")
			}
			firstDiagnosisSupervisorExplorationBudget(t, manager, installer, owned)
		})
		t.Run("actual-constructor-close-no-execution", func(t *testing.T) { firstDiagnosisSupervisorConstructorClose(t, manager, installer, task) })
		t.Run("actual-engine-worker-audit-close-goal-open", func(t *testing.T) { firstDiagnosisSupervisorEngineRun(t, manager, installer, task, "allowed") })
		t.Run("actual-engine-tool-failure-is-not-close-failure", func(t *testing.T) {
			failedTool, err := manager.CreateTask("Owned tool failure fixture", "Record a nonzero command without losing the audit", nil, 60, 60)
			if err != nil || failedTool == nil {
				t.Fatal("독립 도구 실패 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", failedTool.ID); err != nil {
				t.Fatal("도구 실패 task 준비 상태 저장 실패")
			}
			failedTool.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := failedTool.Store.AddIntent(map[string]any{"summary": "Exercise a nonzero owned command"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 도구 실패 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, failedTool, "tool-failure")
		})
		t.Run("actual-engine-result-unknown-is-partial", func(t *testing.T) {
			unknownTask, err := manager.CreateTask("Owned result-unknown fixture", "Close a run whose only request lost its response", nil, 60, 60)
			if err != nil || unknownTask == nil {
				t.Fatal("독립 결과 불명 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", unknownTask.ID); err != nil {
				t.Fatal("결과 불명 task 준비 상태 저장 실패")
			}
			unknownTask.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := unknownTask.Store.AddIntent(map[string]any{"summary": "Read the owned fixture once"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 결과 불명 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, unknownTask, "result-unknown")
		})
		t.Run("actual-engine-planner-before-intent-no-legacy-send", func(t *testing.T) {
			plannerOnly, err := manager.CreateTask("Owned planner-only fixture", "Require controlled planning before any intent", nil, 60, 60)
			if err != nil || plannerOnly == nil {
				t.Fatal("독립 Planner task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", plannerOnly.ID); err != nil {
				t.Fatal("Planner task 준비 상태 저장 실패")
			}
			plannerOnly.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			firstDiagnosisSupervisorEngineRun(t, manager, installer, plannerOnly, "planner-only")
		})
		t.Run("actual-engine-installed-plan-turn-limit", func(t *testing.T) {
			turnTask, err := manager.CreateTask("Owned plan turn fixture", "Stop each plan at the installed turn limit", nil, 60, 60)
			if err != nil || turnTask == nil {
				t.Fatal("독립 Plan 턴 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", turnTask.ID); err != nil {
				t.Fatal("Plan 턴 task 준비 상태 저장 실패")
			}
			turnTask.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			firstDiagnosisSupervisorEngineRun(t, manager, installer, turnTask, "plan-turns")
		})
		t.Run("actual-engine-planner-proves-goal", func(t *testing.T) {
			goalTask, err := manager.CreateTask("Owned goal fixture", "Close when the planner proves the only goal", nil, 60, 60)
			if err != nil || goalTask == nil {
				t.Fatal("독립 목표 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", goalTask.ID); err != nil {
				t.Fatal("목표 task 준비 상태 저장 실패")
			}
			goalTask.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			firstDiagnosisSupervisorEngineRun(t, manager, installer, goalTask, "goal-met")
		})
		t.Run("actual-engine-worker-insert-assets-approved-target", func(t *testing.T) {
			assetTask, err := manager.CreateTask("Owned asset fixture", "Record only approved target assets", nil, 60, 60)
			if err != nil || assetTask == nil {
				t.Fatal("독립 자산 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", assetTask.ID); err != nil {
				t.Fatal("자산 task 준비 상태 저장 실패")
			}
			assetTask.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := assetTask.Store.AddIntent(map[string]any{"summary": "Record the owned fixture endpoint"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("자산 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, assetTask, "insert-assets")
		})
		t.Run("actual-engine-planner-task-scope-keeps-e2-policy", func(t *testing.T) {
			scopeTask, err := manager.CreateTask("Owned task scope fixture", "Widen coverage without widening the E2 approval", nil, 60, 60)
			if err != nil || scopeTask == nil {
				t.Fatal("독립 범위 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", scopeTask.ID); err != nil {
				t.Fatal("범위 task 준비 상태 저장 실패")
			}
			scopeTask.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			firstDiagnosisSupervisorEngineRun(t, manager, installer, scopeTask, "task-scope")
		})
		t.Run("actual-engine-call-limit-is-budget-limited", func(t *testing.T) {
			limited, err := manager.CreateTask("Owned planner round limit fixture", "Close at the planner round limit without failing", nil, 60, 60)
			if err != nil || limited == nil {
				t.Fatal("독립 Planner 상한 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", limited.ID); err != nil {
				t.Fatal("Planner 상한 task 준비 상태 저장 실패")
			}
			limited.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := limited.Store.AddIntent(map[string]any{"summary": "Read the owned fixture once"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 Planner 상한 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, limited, "planner-limit")
		})
		t.Run("actual-engine-planner-kills-running-work", func(t *testing.T) {
			killed, err := manager.CreateTask("Owned planner kill fixture", "Let the planner stop work that is still running", nil, 60, 60)
			if err != nil || killed == nil {
				t.Fatal("독립 Planner 동시 실행 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", killed.ID); err != nil {
				t.Fatal("Planner 동시 실행 task 준비 상태 저장 실패")
			}
			killed.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := killed.Store.AddIntent(map[string]any{"summary": "Read the owned slow fixture"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 Planner 동시 실행 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, killed, "planner-kill")
		})
		t.Run("actual-engine-time-budget-is-budget-limited", func(t *testing.T) {
			timed, err := manager.CreateTask("Owned time budget fixture", "Close when the run time budget is reached", nil, 60, 60)
			if err != nil || timed == nil {
				t.Fatal("독립 시간 상한 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", timed.ID); err != nil {
				t.Fatal("시간 상한 task 준비 상태 저장 실패")
			}
			timed.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := timed.Store.AddIntent(map[string]any{"summary": "Read the owned slow fixture"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 시간 상한 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, timed, "time-budget")
		})
		for _, mode := range []string{"request-budget", "request-budget-overflow"} {
			t.Run("actual-engine-"+mode+"-is-budget-limited", func(t *testing.T) {
				spent, err := manager.CreateTask("Owned request budget fixture", "Close when the approved request budget is spent", nil, 60, 60)
				if err != nil || spent == nil {
					t.Fatal("독립 요청 예산 task 생성 실패")
				}
				if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", spent.ID); err != nil {
					t.Fatal("요청 예산 task 준비 상태 저장 실패")
				}
				spent.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
				if id, err := spent.Store.AddIntent(map[string]any{"summary": "Read the owned fixture once"}, 1, nil, "system"); err != nil || id <= 0 {
					t.Fatal("실제 요청 예산 intent 생성 실패")
				}
				firstDiagnosisSupervisorEngineRun(t, manager, installer, spent, mode)
			})
		}
		t.Run("actual-engine-preparation-failure-no-fallback", func(t *testing.T) {
			prepareFail, err := manager.CreateTask("Owned preparation failure fixture", "Reject work before model tool execution", nil, 60, 60)
			if err != nil || prepareFail == nil {
				t.Fatal("독립 준비 거부 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", prepareFail.ID); err != nil {
				t.Fatal("준비 거부 task 준비 상태 저장 실패")
			}
			prepareFail.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := prepareFail.Store.AddIntent(map[string]any{"summary": "Reject this work before model execution"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 준비 거부 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, prepareFail, "prepare-fail")
		})
		t.Run("actual-engine-audit-close-failure-is-not-success", func(t *testing.T) {
			closeFail, err := manager.CreateTask("Owned audit-close failure fixture", "Keep closure failure distinct from a command result", nil, 60, 60)
			if err != nil || closeFail == nil {
				t.Fatal("독립 감사 마감 실패 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", closeFail.ID); err != nil {
				t.Fatal("감사 마감 실패 task 준비 상태 저장 실패")
			}
			closeFail.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := closeFail.Store.AddIntent(map[string]any{"summary": "Exercise audit close failure"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 감사 마감 실패 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, closeFail, "close-fail")
		})
		for _, mode := range []string{"denied", "partial-denied", "unsupported"} {
			t.Run("actual-engine-limitation-"+mode, func(t *testing.T) {
				denied, err := manager.CreateTask("Owned denied engine fixture", "Preserve a policy denial", nil, 60, 60)
				if err != nil || denied == nil {
					t.Fatal("독립 거부 task 생성 실패")
				}
				if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", denied.ID); err != nil {
					t.Fatal("거부 task 준비 상태 저장 실패")
				}
				denied.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
				if id, err := denied.Store.AddIntent(map[string]any{"summary": "Exercise a policy refusal"}, 1, nil, "system"); err != nil || id <= 0 {
					t.Fatal("실제 거부 intent 생성 실패")
				}
				firstDiagnosisSupervisorEngineRun(t, manager, installer, denied, mode)
			})
		}
		t.Run("actual-engine-cancel-drain-no-new-send", func(t *testing.T) {
			cancelled, err := manager.CreateTask("Owned cancelled engine fixture", "Cancel an active owned request", nil, 60, 60)
			if err != nil || cancelled == nil {
				t.Fatal("독립 취소 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", cancelled.ID); err != nil {
				t.Fatal("취소 task 준비 상태 저장 실패")
			}
			cancelled.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := cancelled.Store.AddIntent(map[string]any{"summary": "Cancel the owned in-flight request"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 취소 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, cancelled, "cancel")
		})
		t.Run("actual-engine-user-stop-drain-no-new-send", func(t *testing.T) {
			cancelled, err := manager.CreateTask("Owned cancelled engine fixture", "Cancel an active owned request", nil, 60, 60)
			if err != nil || cancelled == nil {
				t.Fatal("독립 취소 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", cancelled.ID); err != nil {
				t.Fatal("취소 task 준비 상태 저장 실패")
			}
			cancelled.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := cancelled.Store.AddIntent(map[string]any{"summary": "Cancel the owned in-flight request"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 취소 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, cancelled, "user-stop")
		})
		t.Run("actual-engine-pre-run-stop-no-acquire", func(t *testing.T) {
			cancelled, err := manager.CreateTask("Owned cancelled engine fixture", "Cancel an active owned request", nil, 60, 60)
			if err != nil || cancelled == nil {
				t.Fatal("독립 취소 task 생성 실패")
			}
			if _, err := runtimePool.ExecContext(ctx, "UPDATE tasks SET paused=true WHERE id=$1", cancelled.ID); err != nil {
				t.Fatal("취소 task 준비 상태 저장 실패")
			}
			cancelled.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			if id, err := cancelled.Store.AddIntent(map[string]any{"summary": "Cancel the owned in-flight request"}, 1, nil, "system"); err != nil || id <= 0 {
				t.Fatal("실제 취소 intent 생성 실패")
			}
			firstDiagnosisSupervisorEngineRun(t, manager, installer, cancelled, "pre-run-stop")
		})
		var expID int64
		if err := runtimePool.QueryRowContext(ctx, "SELECT exploration_id FROM tasks WHERE id=$1", task.ID).Scan(&expID); err != nil || expID != task.ExpID {
			t.Fatal("runtime task·Store 결속을 SQL에서 확인할 수 없음")
		}
	})
	t.Run("installer-superuser", func(t *testing.T) {
		var super bool
		if err := installer.QueryRowContext(ctx, "SELECT rolsuper FROM pg_roles WHERE rolname=current_user").Scan(&super); err != nil {
			t.Fatal("설치자 권한 조회 실패")
		}
		if !super {
			t.Skip("이 전용 설치자는 superuser가 아님")
		}
		assertRejected(t, installer.DB)
	})
	type mutation struct{ name, apply, restore string }
	for _, tc := range []mutation{
		{"missing-required-select", "REVOKE SELECT ON TABLE tasks FROM " + ident(role), "GRANT SELECT ON TABLE tasks TO " + ident(role)},
		{"missing-required-insert", "REVOKE INSERT ON TABLE findings FROM " + ident(role), "GRANT INSERT ON TABLE findings TO " + ident(role)},
		{"missing-sequence-usage", "REVOKE USAGE ON SEQUENCE tasks_id_seq FROM " + ident(role), "GRANT USAGE ON SEQUENCE tasks_id_seq TO " + ident(role)},
		{"authority-write", "GRANT UPDATE ON TABLE audit_key_registry_keys TO " + ident(role), "REVOKE UPDATE ON TABLE audit_key_registry_keys FROM " + ident(role)},
		{"authority-column-write", "GRANT UPDATE(store_ref) ON TABLE audit_checkpoint_epochs TO " + ident(role), "REVOKE UPDATE(store_ref) ON TABLE audit_checkpoint_epochs FROM " + ident(role)},
		{"database-temp", "GRANT TEMP ON DATABASE " + ident(dbName) + " TO " + ident(role), "REVOKE TEMP ON DATABASE " + ident(dbName) + " FROM " + ident(role)},
		{"replication-role-parameter", "GRANT SET ON PARAMETER session_replication_role TO " + ident(role), "REVOKE SET ON PARAMETER session_replication_role FROM " + ident(role)},
		{"schema-create", "GRANT CREATE ON SCHEMA public TO " + ident(role), "REVOKE CREATE ON SCHEMA public FROM " + ident(role)},
	} {
		t.Run(tc.name, func(t *testing.T) { exec(tc.apply); defer exec(tc.restore); assertRejected(t, runtimePool) })
	}
	t.Run("owned-relation", func(t *testing.T) {
		name := "first_diag_owner_" + hex.EncodeToString(suffix)
		exec("CREATE TABLE " + ident(name) + "(id bigint)")
		defer exec("DROP TABLE " + ident(name))
		exec("ALTER TABLE " + ident(name) + " OWNER TO " + ident(role))
		defer exec("ALTER TABLE " + ident(name) + " OWNER TO " + ident(installerName))
		assertRejected(t, runtimePool)
	})
	t.Run("set-role-privilege-escalation", func(t *testing.T) {
		exec("CREATE ROLE " + ident(elevated) + " NOLOGIN CREATEDB")
		defer exec("DROP ROLE " + ident(elevated))
		exec("GRANT " + ident(elevated) + " TO " + ident(role))
		defer exec("REVOKE " + ident(elevated) + " FROM " + ident(role))
		assertRejected(t, runtimePool)
	})
	t.Run("server-program-membership", func(t *testing.T) {
		exec("GRANT pg_execute_server_program TO " + ident(role))
		defer exec("REVOKE pg_execute_server_program FROM " + ident(role))
		assertRejected(t, runtimePool)
	})
	t.Run("nested-noinherit-read-all-data", func(t *testing.T) {
		nested := elevated + "_nested"
		exec("CREATE ROLE " + ident(nested) + " NOLOGIN NOINHERIT")
		defer exec("DROP ROLE " + ident(nested))
		exec("GRANT pg_read_all_data TO " + ident(nested))
		defer exec("REVOKE pg_read_all_data FROM " + ident(nested))
		exec("GRANT " + ident(nested) + " TO " + ident(role))
		defer exec("REVOKE " + ident(nested) + " FROM " + ident(role))
		assertRejected(t, runtimePool)
	})
	t.Run("write-all-data-membership", func(t *testing.T) {
		exec("GRANT pg_write_all_data TO " + ident(role))
		defer exec("REVOKE pg_write_all_data FROM " + ident(role))
		assertRejected(t, runtimePool)
	})
	t.Run("callable-security-definer", func(t *testing.T) {
		name := "first_diag_secdef_" + hex.EncodeToString(suffix)
		exec("CREATE FUNCTION " + ident(name) + "() RETURNS int LANGUAGE sql SECURITY DEFINER AS 'SELECT 1'")
		defer exec("DROP FUNCTION " + ident(name) + "()")
		assertRejected(t, runtimePool)
	})
	t.Run("active-temp-schema", func(t *testing.T) {
		exec("GRANT TEMP ON DATABASE " + ident(dbName) + " TO " + ident(role))
		one := openRuntime()
		defer one.Close()
		one.SetMaxOpenConns(1)
		if _, err := one.ExecContext(ctx, "CREATE TEMP TABLE first_diag_old_temp(id int)"); err != nil {
			t.Fatal("TEMP 보유 음성 fixture 생성 실패")
		}
		exec("REVOKE TEMP ON DATABASE " + ident(dbName) + " FROM " + ident(role))
		assertRejected(t, one)
	})
	if err := CheckFirstDiagnosisRuntimeDatabase(ctx, runtimePool); err != nil {
		t.Fatal("음성시험 뒤 최소 runtime 권한 복구 실패")
	}
	var count int
	if err := runtimePool.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE description=$1", taskDescription).Scan(&count); err != nil || count != 1 {
		t.Fatal("실제 runtime 저장 결과가 보존되지 않음: " + strconv.Itoa(count))
	}
}
