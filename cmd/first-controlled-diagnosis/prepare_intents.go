package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/Autumn-27/artex/server"
)

func runPrepareIntents(ctx context.Context, args []string, stdout, stderr io.Writer) (code int) {
	flags := flag.NewFlagSet("prepare-intents", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "protected launch configuration")
	taskID := flags.String("task-id", "", "actual paused task ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	id, parseErr := strconv.ParseInt(*taskID, 10, 64)
	if parseErr != nil || id <= 0 || strconv.FormatInt(id, 10) != *taskID {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if ctx == nil || ctx.Err() != nil {
		fmt.Fprintln(stderr, "계획 준비가 취소되었습니다")
		return 1
	}
	c, err := readLaunchConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, "계획 준비 설정을 확인할 수 없습니다")
		return 1
	}
	ctx, diagnostics, err := beginLaunchDiagnostics(ctx, c, "prepare-intents", id, stderr)
	if err != nil {
		return 1
	}
	stderr = diagnostics.stderr
	defer diagnostics.finish(&code)
	diagnostics.next("protected_files", "protected_files_unavailable")
	if checkLaunchFilesContext(ctx, c) != nil {
		return 1
	}
	diagnostics.next("database", "database_unavailable")
	pool, err := openLaunchDatabase(ctx, c.RuntimeDSNFile)
	if err != nil {
		fmt.Fprintln(stderr, "런타임 데이터베이스를 사용할 수 없습니다")
		return 1
	}
	if diagnosticCheck(ctx, "database_role", "database_role_invalid", func() error { return checkLaunchDatabaseRole(ctx, pool, c) }) != nil || diagnosticCheck(ctx, "model_profile", "model_profile_invalid", func() error { return checkLaunchProfile(ctx, pool, c) }) != nil || diagnosticCheck(ctx, "task_binding", "task_binding_failed", func() error { return checkLaunchTaskProfile(ctx, pool, id, c) }) != nil {
		_ = pool.Close()
		fmt.Fprintln(stderr, "런타임 역할 또는 모델 프로필을 확인할 수 없습니다")
		return 1
	}
	diagnostics.next("host_startup", "host_startup_failed")
	manager, err := server.NewFirstDiagnosisManager(ctx, c.DataDir, pool)
	if err != nil {
		_ = pool.Close()
		fmt.Fprintln(stderr, "계획 준비 호스트를 만들 수 없습니다")
		return 1
	}
	supervisor, err := server.NewFirstDiagnosisSupervisor(ctx, manager)
	if err != nil {
		manager.Close()
		fmt.Fprintln(stderr, "실행 감독을 시작할 수 없습니다")
		return 1
	}
	preparation, err := server.NewFirstDiagnosisPreparationServerWithModel(ctx, manager, c.SkillDir, c.DataDir, c.KeyDir, c.modelOptions())
	if err != nil {
		manager.Close()
		fmt.Fprintln(stderr, "계획 준비 서버를 만들 수 없습니다")
		return 1
	}
	if supervisor.Attach(preparation) != nil {
		manager.Close()
		fmt.Fprintln(stderr, "계획 준비 서버를 결속할 수 없습니다")
		return 1
	}
	diagnostics.next("planning", "planner_failed")
	operationErr := diagnostics.ready()
	if operationErr == nil {
		operationErr = supervisor.PrepareIntents(ctx, *taskID)
	}
	if operationErr != nil {
		diagnostics.endError(operationErr, "planner_failed")
	} else {
		diagnostics.end("succeeded", "")
	}
	if usage := supervisor.ModelUsage(); usage != nil {
		diagnostics.next("output", "output_failed")
		if json.NewEncoder(stdout).Encode(map[string]any{"model_usage": usage}) != nil {
			operationErr = errPreparation
			diagnostics.end("failed", "output_failed")
		} else {
			diagnostics.end("succeeded", "")
		}
	}
	var intentIDs []int64
	if operationErr == nil {
		diagnostics.next("result_storage", "intent_storage_failed")
		rows, readErr := pool.QueryContext(ctx, `SELECT n.id FROM exploration_nodes n JOIN tasks t ON t.exploration_id=n.exploration_id WHERE t.id=$1 AND n.kind='intent' AND n.state='open' ORDER BY n.id LIMIT 129`, id)
		if readErr != nil {
			operationErr = errPreparation
		} else {
			for rows.Next() {
				var actual int64
				if rows.Scan(&actual) != nil || actual <= 0 {
					operationErr = errPreparation
					break
				}
				intentIDs = append(intentIDs, actual)
			}
			if rows.Err() != nil || rows.Close() != nil || len(intentIDs) == 0 || len(intentIDs) > 128 {
				operationErr = errPreparation
			}
		}
	}
	if operationErr != nil {
		diagnostics.endError(operationErr, "intent_storage_failed")
	}
	diagnostics.next("shutdown", "drain_failed")
	cleanup, cancel := launchCleanupContext(ctx, c)
	defer cancel()
	phases := supervisor.Shutdown()
	var closeErr error
	for _, phase := range []func(context.Context, *server.Server) error{phases.BlockAdmission, phases.CancelAndDrain, phases.CheckResults} {
		if phase == nil {
			closeErr = errPreparation
			break
		}
		if closeErr = phase(cleanup, preparation); closeErr != nil {
			break
		}
	}
	if persistErr := supervisor.PersistPreparationModelUsage(cleanup, *taskID, errors.Join(operationErr, closeErr)); persistErr != nil {
		closeErr = errors.Join(closeErr, persistErr)
	}
	if closeErr == nil {
		manager.Close()
	}
	if closeErr != nil {
		diagnostics.endError(closeErr, "drain_failed")
	} else {
		diagnostics.end("succeeded", "")
	}
	if operationErr != nil || closeErr != nil {
		fmt.Fprintln(stderr, "계획 준비 또는 마감 결과를 확인할 수 없습니다")
		return 1
	}
	diagnostics.finish(&code)
	if code != 0 {
		return code
	}
	if json.NewEncoder(stdout).Encode(map[string]any{"task_id": id, "intent_ids": intentIDs, "planning": "PASS", "target_requests": 0, "execution": "NOT_RUN"}) != nil {
		return 1
	}
	return 0
}
