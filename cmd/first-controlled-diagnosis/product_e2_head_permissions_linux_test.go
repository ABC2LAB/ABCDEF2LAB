//go:build linux

package main

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"testing"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// 실제 제품 GRANT를 일회용 DB에 적용하고 실행 역할이 head 정본을 쓸 수 없는지 확인한다.
func TestProductE2HeadWriterRoleIsSeparated(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_TEST_DSN")
	if dsn == "" {
		t.Skip("전용 PostgreSQL DSN이 제공되지 않음")
	}
	if os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_OWNED") != "1" {
		t.Fatal("전용 DB 소유 확인 필요")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "product_e2_head_" + productRandom(8)
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	var installer *db.DB
	created := []string{}
	databaseCreated := false
	t.Cleanup(func() {
		if installer != nil {
			installer.Close()
		}
		if databaseCreated {
			if _, e := admin.Exec("DROP DATABASE IF EXISTS " + ident(name) + " WITH (FORCE)"); e != nil {
				t.Error(e)
			}
		}
		for _, role := range created {
			if _, e := admin.Exec("DROP ROLE " + ident(role)); e != nil {
				t.Error(e)
			}
		}
		admin.Close()
	})
	if _, err := admin.Exec("CREATE DATABASE " + ident(name) + " TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	databaseCreated = true
	u.Path, u.RawPath = "/"+name, ""
	installer, err = db.Open(u.String())
	if err != nil {
		t.Fatal(err)
	}
	roles := []string{"product_runtime", "product_api", "product_model_writer", "product_e2_head_writer"}
	for _, migrate := range []func(context.Context, *sql.DB) error{auditcheckpoint.Migrate, auditkeys.Migrate, evidencerevision.Migrate, e2checkpoint.Migrate} {
		if err := migrate(t.Context(), installer.DB); err != nil {
			t.Fatal(err)
		}
	}
	if installer.EnsureLLMUsageTable() != nil || installer.EnsureLLMRecordsTable() != nil {
		t.Fatal("제품 설치 테이블 준비 실패")
	}
	for _, role := range roles {
		login := "LOGIN"
		if role == "product_e2_head_writer" {
			login = "NOLOGIN"
		}
		if _, err := admin.Exec("CREATE ROLE " + ident(role) + " " + login + " NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); err != nil {
			t.Fatal(err)
		}
		created = append(created, role)
	}
	if err := productGrantRoles(t.Context(), installer.DB, name); err != nil {
		t.Fatal("제품 GRANT 실패", err)
	}
	var canLogin, headRead, headInsert, headUpdate, epochRead, guardUpdate, epochUpdate bool
	if err := installer.QueryRowContext(t.Context(), `SELECT r.rolcanlogin,
		has_table_privilege(r.rolname,'e2_http_audit_heads','SELECT'),
		has_table_privilege(r.rolname,'e2_http_audit_heads','INSERT'),
		has_table_privilege(r.rolname,'e2_http_audit_heads','UPDATE'),
		has_table_privilege(r.rolname,'e2_http_audit_epochs','SELECT'),
		has_column_privilege(r.rolname,'e2_http_audit_epochs','lock_guard','UPDATE'),
		has_column_privilege(r.rolname,'e2_http_audit_epochs','epoch_ref','UPDATE')
		FROM pg_roles r WHERE r.rolname='product_e2_head_writer'`).Scan(
		&canLogin, &headRead, &headInsert, &headUpdate, &epochRead, &guardUpdate, &epochUpdate); err != nil ||
		canLogin || !headRead || !headInsert || !headUpdate || !epochRead || !guardUpdate || epochUpdate {
		t.Fatal("head 역할의 최소 권한 불일치", err)
	}
	for _, role := range roles[:3] {
		var read, insert, update bool
		if err := installer.QueryRowContext(t.Context(), `SELECT
			has_table_privilege($1,'e2_http_audit_heads','SELECT'),
			has_table_privilege($1,'e2_http_audit_heads','INSERT'),
			has_table_privilege($1,'e2_http_audit_heads','UPDATE')`, role).Scan(&read, &insert, &update); err != nil || read || insert || update {
			t.Fatal("실행·API 역할에 head 권한 노출", role, err)
		}
	}
	if _, err := installer.ExecContext(t.Context(), `INSERT INTO e2_http_audit_epochs(store_ref,epoch_ref,generation,anchor_digest)
		VALUES('installation-1','epoch-1',1,decode(repeat('a',64),'hex'))`); err != nil {
		t.Fatal(err)
	}
	tx, err := installer.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "SET LOCAL ROLE product_e2_head_writer"); err != nil {
		t.Fatal(err)
	}
	var epoch string
	if err := tx.QueryRowContext(t.Context(), `SELECT epoch_ref FROM e2_http_audit_epochs
		WHERE store_ref='installation-1' FOR SHARE`).Scan(&epoch); err != nil || epoch != "epoch-1" {
		t.Fatal("head 역할이 epoch를 잠글 수 없음", err)
	}
	_, err = tx.ExecContext(t.Context(), `UPDATE e2_http_audit_epochs SET epoch_ref='rollback' WHERE store_ref='installation-1'`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatal("head 역할이 epoch 권위를 변경할 수 있음", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, role := range roles[:3] {
		tx, err := installer.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(t.Context(), "SET LOCAL ROLE "+ident(role)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		var count int
		err = tx.QueryRowContext(t.Context(), "SELECT count(*) FROM e2_http_audit_heads").Scan(&count)
		tx.Rollback()
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatal("일반 제품 역할이 head 정본을 읽을 수 있음", role, err)
		}
	}
}
