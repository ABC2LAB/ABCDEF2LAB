package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
)

// 끝난 점검 삭제(2026-11-23 사용자·팀 결정). 설치자(postgres)가 삭제 함수와 전용 역할을 두고, 준비 서비스만 읽는
// DSN을 쓴다. 처음 설치와 이미 설치된 갱신 둘 다에서 부른다(멱등).
// 이 DSN은 installation.json 고정 목록·API 투영에 넣지 않는다 — InstallationDigest가 바뀌면 지난 회차의 SourceDigest와
// 어긋나 저장 보고서를 다시 열 수 없다.
const (
	productRunPurgeRole    = "product_run_purger"
	productRunPurgeDSNName = "purge.dsn"
	// 이미 설치된 갱신에서 DB를 기다리는 상한. 설치 컨테이너는 postgres 건강 확인 뒤에만 돈다.
	productRunPurgeMigrateTimeout = 30 * time.Second
)

func productRunPurgeDSNPath(configDir string) string {
	return filepath.Join(configDir, productRunPurgeDSNName)
}

// productEnsureRunPurge는 삭제 함수·역할·권한을 맞추고, DSN 파일이 없을 때만 새 비밀번호로 쓴다. 역할이 이미 있고
// DSN도 있으면 비밀번호를 바꾸지 않는다. 역할이 없는데 DSN만 남았으면(이전 설치 잔여) 새로 쓴다.
func productEnsureRunPurge(ctx context.Context, admin *sql.DB, configDir, host, database string) error {
	dsnPath := productRunPurgeDSNPath(configDir)
	_, statErr := os.Lstat(dsnPath)
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return errPreparation
	}
	haveDSN := statErr == nil
	tx, err := admin.BeginTx(ctx, nil)
	if err != nil {
		return errPreparation
	}
	defer func() { _ = tx.Rollback() }()
	if pgdb.EnsureProductRunPurge(ctx, tx) != nil {
		return errPreparation
	}
	var exists bool
	if tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)", productRunPurgeRole).Scan(&exists) != nil {
		return errPreparation
	}
	password := ""
	switch {
	case !exists:
		password = productRandom(24)
		if _, err = tx.ExecContext(ctx, "CREATE ROLE "+productRunPurgeRole+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '"+password+"'"); err != nil {
			return errPreparation
		}
	case !haveDSN:
		password = productRandom(24)
		if _, err = tx.ExecContext(ctx, "ALTER ROLE "+productRunPurgeRole+" PASSWORD '"+password+"'"); err != nil {
			return errPreparation
		}
	}
	for _, grant := range []string{
		"GRANT CONNECT ON DATABASE \"" + database + "\" TO " + productRunPurgeRole,
		"GRANT USAGE ON SCHEMA public TO " + productRunPurgeRole,
		"GRANT EXECUTE ON FUNCTION " + pgdb.ProductRunPurgeFunction + " TO " + productRunPurgeRole,
	} {
		if _, err = tx.ExecContext(ctx, grant); err != nil {
			return errPreparation
		}
	}
	if tx.Commit() != nil {
		return errPreparation
	}
	if password == "" {
		return nil
	}
	if haveDSN && os.Remove(dsnPath) != nil {
		return errPreparation
	}
	return productWrite(dsnPath, []byte(productDSN(host, database, productRunPurgeRole, password)))
}

// productMigrateInstalledRunPurge는 이미 설치된 갱신에서 설치 입력의 DB 위치로 설치자 연결을 열어 삭제 기능을 맞춘다.
// 스키마 전체를 다시 적용하지 않도록 pgdb.Open 대신 그냥 연결만 연다. 실패해도 제품 기동은 막지 않고(삭제만 503),
// 이유 코드만 남긴다.
func productMigrateInstalledRunPurge(ctx context.Context, root, input string, stderr io.Writer) {
	fail := func() { fmt.Fprintln(stderr, "product_run_purge_unavailable") }
	raw, err := readPrivateFile(input, 128<<10)
	if err != nil {
		fail()
		return
	}
	defer clear(raw)
	var c productInstallInput
	if decodeLaunchJSON(raw, &c) != nil || c.DatabaseHost == "" || c.DatabaseName == "" {
		fail()
		return
	}
	c.AdminPassword = ""
	if c.DatabasePasswordFile == "" {
		c.DatabasePasswordFile = "/tmp/product-private/postgres-password"
	}
	password, err := readPrivateFile(c.DatabasePasswordFile, 4096)
	if err != nil {
		fail()
		return
	}
	defer clear(password)
	admin, err := sql.Open("pgx", productDSN(c.DatabaseHost, c.DatabaseName, "postgres", strings.TrimSpace(string(password))))
	if err != nil {
		fail()
		return
	}
	defer admin.Close()
	call, cancel := context.WithTimeout(ctx, productRunPurgeMigrateTimeout)
	defer cancel()
	if productEnsureRunPurge(call, admin, filepath.Join(root, "config"), c.DatabaseHost, c.DatabaseName) != nil {
		fail()
	}
}
