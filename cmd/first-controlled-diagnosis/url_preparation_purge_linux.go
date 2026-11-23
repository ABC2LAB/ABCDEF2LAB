package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/diagnosisprep"
	"github.com/jackc/pgx/v5/stdlib"
)

// PurgeRun은 끝난 회차의 점검 결과를 지운다(diagnosisprep.RunPurger, 2026-11-23 사용자·팀 결정).
// 순서: 저장 보고서 캐시 닫기 → DB 작업 기록(전용 역할로 abc2lab_purge_task) → 회차 폴더 → 작업 데이터·진단 로그.
// 각 단계가 멱등이라 중간에 실패해도 다시 부르면 이어서 지운다. 끝났는지(실행 반환·자원 마감)는 준비 서비스가 먼저 본다.
// E② 호스트 저널(/h)·대상 리셋 기록·예산 장부·OpenRouter 장부는 건드리지 않는다 — 지우면 다음 회차 시작이 막히거나
// 리셋 확인 전 쓰기 기록이 사라진다.
func (b *urlPreparationRuntime) PurgeRun(ctx context.Context, e diagnosisprep.Execution) error {
	job := e.Job
	if ctx == nil || ctx.Err() != nil || e.Principal != "ARTEX" || !managementReference.MatchString(job.ID) ||
		filepath.Base(job.ID) != job.ID || !strings.HasPrefix(job.ID, "prep-") {
		return errPreparation
	}
	var task int64
	if job.TaskID != "" {
		value, err := strconv.ParseInt(job.TaskID, 10, 64)
		if err != nil || value <= 0 || strconv.FormatInt(value, 10) != job.TaskID {
			return errPreparation
		}
		task = value
	}
	if task > 0 {
		b.mu.Lock()
		if b.live[job.TaskID] != nil {
			// 아직 실행 처리기가 붙어 있으면 끝난 회차로 보지 않는다
			b.mu.Unlock()
			return errPreparation
		}
		report := b.reports[job.TaskID]
		delete(b.reports, job.TaskID)
		b.mu.Unlock()
		if report != nil {
			_ = report.close()
		}
		if err := b.purgeTaskRecords(ctx, task); err != nil {
			return err
		}
	}
	root := filepath.Dir(b.productConfigDir())
	paths := []string{filepath.Join(b.config.RunsDir, job.ID)}
	if task > 0 {
		paths = append(paths, filepath.Join(root, "data", "tasks", job.TaskID), filepath.Join(root, "data", "diagnostic-logs", "task-"+job.TaskID))
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return errPreparation
		}
	}
	return nil
}

// productConfigDir는 설치 설정 폴더(/p/config)다. 설치자가 api-launch.json을 그 안에 둔다.
func (b *urlPreparationRuntime) productConfigDir() string {
	return filepath.Dir(b.config.APILaunch.File)
}

// purgeTaskRecords는 설치자가 둔 삭제 전용 DSN으로만 연결한다. 그 역할은 표 권한이 없고 삭제 함수만 부를 수 있다.
// DSN이 없으면(마이그레이션 전 설치) 삭제하지 않는다.
func (b *urlPreparationRuntime) purgeTaskRecords(ctx context.Context, task int64) error {
	raw, err := readPrivateFile(productRunPurgeDSNPath(b.productConfigDir()), 16<<10)
	if err != nil {
		return errPreparation
	}
	dsn := string(bytes.TrimSpace(raw))
	clear(raw)
	if dsn == "" || strings.ContainsAny(dsn, "\r\n\x00") {
		return errPreparation
	}
	config, err := urlParseDatabase(dsn)
	if err != nil || !urlDatabaseProtected(b.config, config) {
		return errPreparation
	}
	pool := stdlib.OpenDB(*config)
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	if _, err = pgdb.PurgeProductTask(ctx, pool, task); err != nil {
		return errPreparation
	}
	return nil
}
