package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
)

// 끝난 제품 점검 회차 삭제(2026-11-23 사용자·팀 결정). 설치자가 함수를 두고, 준비 서비스가 전용 역할로 부른다.

//go:embed product_run_purge.sql
var productRunPurgeSQL string

// ProductRunPurgeFunction은 설치자가 두는 삭제 함수 이름이다. 실행 역할에는 이 함수 EXECUTE만 준다.
const ProductRunPurgeFunction = "abc2lab_purge_task(BIGINT)"

// ErrProductRunPurgeInput은 작업 ID가 양수가 아닐 때다.
var ErrProductRunPurgeInput = errors.New("product_run_purge_input")

// EnsureProductRunPurge는 삭제 함수를 만들거나 바꾸고 PUBLIC 실행 권한을 거둔다. 함수 소유자가 되도록 설치자(postgres)
// 연결로만 부른다. 같은 트랜잭션에서 거둬야 다른 역할이 SECURITY DEFINER 함수를 실행할 수 있는 순간이 생기지 않는다.
func EnsureProductRunPurge(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, productRunPurgeSQL)
	return err
}

// PurgeProductTask는 작업 하나의 DB 기록을 지운다. 지웠으면 true, 이미 없던 작업이면 false다(멱등).
func PurgeProductTask(ctx context.Context, pool *sql.DB, task int64) (bool, error) {
	if task <= 0 {
		return false, ErrProductRunPurgeInput
	}
	var deleted bool
	if err := pool.QueryRowContext(ctx, "SELECT abc2lab_purge_task($1)", task).Scan(&deleted); err != nil {
		return false, err
	}
	return deleted, nil
}
