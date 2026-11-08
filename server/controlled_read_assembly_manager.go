package server

import (
	"context"
	"database/sql"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/artex/internal/privateinput"
)

// NewFirstDiagnosisManager는 이미 설치된 실행 역할 pool만 인수한다.
// migration·자동 복구·proxy·enrich·예약 작업을 시작하지 않는다.
// 성공하면 Manager.Close가 pool을 소유하고, 실패하면 호출자가 계속 소유한다.
func NewFirstDiagnosisManager(ctx context.Context, dataDir string, pool *sql.DB) (*Manager, error) {
	if ctx == nil || ctx.Err() != nil || pool == nil || privateinput.CheckDirectory(dataDir, false) != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if pool.PingContext(check) != nil || check.Err() != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	pg := &pgdb.DB{DB: pool}
	manager := &Manager{dir: dataDir, pg: pg, assets: pg.Assets(), tasks: map[string]*Task{}, interceptor: intercept.New(pg)}
	if ctx.Err() != nil {
		return nil, ErrControlledReadHostConfiguration
	}
	return manager, nil
}
