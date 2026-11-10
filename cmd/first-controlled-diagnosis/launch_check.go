package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/server"
	"io"
)

// check는 배타 시작 marker나 실제 실행 권위를 소비하지 않는다.
func checkLaunch(ctx context.Context, c launchConfig, database bool, stdout, stderr io.Writer) int {
	if ctx == nil || ctx.Err() != nil || checkLaunchFiles(c) != nil {
		fmt.Fprintln(stderr, "기동 입력을 확인할 수 없습니다")
		return 1
	}
	if c.Registered != nil && server.CheckRegisteredReadPreparation(ctx, c.Registered.MetadataFile, c.Registered.SnapshotDir, c.Registered.RegistryFile) != nil {
		fmt.Fprintln(stderr, "등록 GET 준비 자료가 일치하지 않습니다")
		return 1
	}
	report := checkReport{"PASS", "PASS", "PASS", "SKIP", "NOT_ACQUIRED", "NOT_RUN"}
	if database {
		pool, err := openLaunchDatabase(ctx, c.RuntimeDSNFile)
		if err != nil {
			fmt.Fprintln(stderr, "런타임 DB를 확인할 수 없습니다")
			return 1
		}
		checkErr := checkLaunchDatabaseRole(ctx, pool, c)
		if checkErr == nil {
			checkErr = checkLaunchProfile(ctx, pool, c)
		}
		closeErr := pool.Close()
		if checkErr != nil || closeErr != nil {
			fmt.Fprintln(stderr, "런타임 역할 또는 실제 모델 프로필이 올바르지 않습니다")
			return 1
		}
		if c.Registered != nil {
			authority, err := openLaunchDatabase(ctx, c.Registered.AuthorityDSNFile)
			if err != nil {
				fmt.Fprintln(stderr, "호스트 DB 권위를 확인할 수 없습니다")
				return 1
			}
			checkErr = server.CheckFirstDiagnosisAuthorityDatabase(ctx, authority)
			closeErr = authority.Close()
			if checkErr != nil || closeErr != nil {
				fmt.Fprintln(stderr, "호스트 DB 역할이 올바르지 않습니다")
				return 1
			}
		}
		report.Database = "PASS"
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "검사가 취소되었습니다")
		return 1
	}
	if json.NewEncoder(stdout).Encode(report) != nil {
		fmt.Fprintln(stderr, "검사 결과를 기록할 수 없습니다")
		return 1
	}
	return 0
}
