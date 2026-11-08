package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Autumn-27/artex/llmpolicy"
	"github.com/Autumn-27/artex/server"
)

func readLaunchConfig(path string) (launchConfig, error) {
	body, err := readPrivateFile(path, maxLaunchBytes)
	if err != nil {
		return launchConfig{}, errPreparation
	}
	defer clear(body)
	return parseLaunchConfig(body)
}

func openLaunchDatabase(ctx context.Context, path string) (*sql.DB, error) {
	raw, err := readPrivateFile(path, 16<<10)
	if err != nil {
		return nil, errPreparation
	}
	defer clear(raw)
	dsn := string(bytes.TrimSpace(raw))
	if dsn == "" || strings.ContainsAny(dsn, "\r\n\x00") {
		return nil, errPreparation
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errPreparation
	}
	pool.SetMaxOpenConns(8)
	pool.SetMaxIdleConns(2)
	pool.SetConnMaxLifetime(0)
	call, cancel := context.WithTimeout(ctx, databaseCheckTimeout)
	defer cancel()
	if pool.PingContext(call) != nil {
		_ = pool.Close()
		return nil, errPreparation
	}
	return pool, nil
}

func checkLaunchFiles(c launchConfig) error {
	for _, path := range []string{c.DataDir, c.SkillDir} {
		if checkDirectory(path, false) != nil {
			return errPreparation
		}
	}
	if checkDirectory(c.KeyDir, true) != nil {
		return errPreparation
	}
	key, err := readPrivateFile(c.KeyDir+"/jwt.key", maxJWTKeyBytes)
	if err != nil {
		return errPreparation
	}
	valid := len(bytes.TrimSpace(key)) >= minJWTKeyBytes
	clear(key)
	if !valid {
		return errPreparation
	}
	err = server.CheckFirstDiagnosisModelOptions(c.modelOptions(), llmpolicy.ProfileTarget{ID: c.ModelProfileID, Format: c.ModelFormat, BaseURL: c.ModelBaseURL, Model: c.ModelRef})
	if err != nil {
		return errPreparation
	}
	if c.Registered != nil {
		for _, path := range []string{c.Registered.StateDir, c.Registered.SnapshotDir} {
			if checkDirectory(path, true) != nil {
				return errPreparation
			}
		}
	}
	return nil
}

func checkLaunchProfile(ctx context.Context, pool *sql.DB, c launchConfig) error {
	var format, base, model string
	if pool.QueryRowContext(ctx, `SELECT format,COALESCE(base_url,''),model FROM llm_profiles WHERE id=$1`, c.ModelProfileID).Scan(&format, &base, &model) != nil || format != c.ModelFormat || base != c.ModelBaseURL || model != c.ModelRef {
		return errPreparation
	}
	return server.CheckFirstDiagnosisModelRoleBinding(ctx, pool, c.ModelProfileID)
}

func checkLaunchDatabaseRole(ctx context.Context, pool *sql.DB, c launchConfig) error {
	if c.Mode == "registered_get" {
		if server.CheckControlledReadAssemblyDatabase(ctx, pool) != nil {
			return errPreparation
		}
		return server.CheckFirstDiagnosisRuntimeDatabase(ctx, pool)
	}
	return server.CheckFirstDiagnosisHTTPRuntimeDatabase(ctx, pool)
}

func runPrepareTask(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("prepare-task", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "protected launch configuration")
	taskPath := flags.String("task", "", "protected task input")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *configPath == "" || *taskPath == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if ctx == nil || ctx.Err() != nil {
		fmt.Fprintln(stderr, "준비 작업이 취소되었습니다")
		return 1
	}
	cfg, err := readLaunchConfig(*configPath)
	if err != nil || checkLaunchFiles(cfg) != nil {
		fmt.Fprintln(stderr, "실행 설정을 사용할 수 없습니다")
		return 1
	}
	raw, err := readPrivateFile(*taskPath, 40<<10)
	if err != nil {
		fmt.Fprintln(stderr, "작업 입력을 사용할 수 없습니다")
		return 1
	}
	defer clear(raw)
	var input taskPreparation
	if decodeLaunchJSON(raw, &input) != nil || !plainText(input.Description, 16<<10) || !plainText(input.Goal, 16<<10) || input.TimeoutSeconds < 1 || input.TimeoutSeconds > 900 {
		fmt.Fprintln(stderr, "작업 입력이 올바르지 않습니다")
		return 1
	}
	pool, err := openLaunchDatabase(ctx, cfg.RuntimeDSNFile)
	if err != nil {
		fmt.Fprintln(stderr, "런타임 데이터베이스를 사용할 수 없습니다")
		return 1
	}
	defer pool.Close()
	if checkLaunchDatabaseRole(ctx, pool, cfg) != nil || checkLaunchProfile(ctx, pool, cfg) != nil {
		fmt.Fprintln(stderr, "런타임 역할 또는 모델 프로필을 확인할 수 없습니다")
		return 1
	}
	actual, err := server.PrepareFirstDiagnosisTask(ctx, pool, input.Description, input.Goal, cfg.ModelProfileID, input.TimeoutSeconds)
	if err != nil {
		fmt.Fprintln(stderr, "작업 생성 결과를 확인할 수 없습니다")
		return 1
	}
	if json.NewEncoder(stdout).Encode(actual) != nil {
		fmt.Fprintln(stderr, "출력을 기록할 수 없습니다")
		return 1
	}
	return 0
}

// 마감은 원래 취소된 실행 문맥과 분리해 제한 시간 안에서 실제 drain 결과를 기다린다.
func launchCleanupContext(ctx context.Context, c launchConfig) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), time.Duration(c.ShutdownTimeoutSeconds)*time.Second)
}

// 첫 모드는 prepare-task가 고정한 단일 실제 프로필만 사용하며 암묵적인 대체 chain을 만들지 않는다.
func checkLaunchTaskProfile(ctx context.Context, pool *sql.DB, taskID int64, c launchConfig) error {
	return server.CheckFirstDiagnosisTaskProfileBinding(ctx, pool, taskID, c.ModelProfileID)
}
