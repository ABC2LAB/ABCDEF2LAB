//go:build linux

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/e2host"
	"github.com/Autumn-27/artex/e2proxy"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/transmit"
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
		for i := len(created) - 1; i >= 0; i-- {
			if _, e := admin.Exec("DROP ROLE " + ident(created[i])); e != nil {
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
	hostRoot := t.TempDir()
	if err := os.Chmod(hostRoot, 0700); err != nil {
		t.Fatal("Host fixture 권한 설정 실패", err)
	}
	// CI의 일회용 PostgreSQL은 TCP를 열지 않는다. 관리자 DSN과 같은
	// Unix 소켓을 Host 전용 LOGIN 시험에도 사용한다.
	databaseHost := u.Query().Get("host")
	if databaseHost == "" {
		databaseHost = u.Hostname()
	}
	if databaseHost == "" {
		t.Fatal("시험 PostgreSQL host를 확인할 수 없음")
	}
	if err := productProvisionE2Host(t.Context(), installer.DB, databaseHost, name, hostRoot); err != nil {
		t.Fatal("Host 전용 자격 발급 실패", err)
	}
	created = append(created, "product_e2_host")
	for _, filename := range []string{"host.dsn", "audit.key", "digest.key", "epoch.json"} {
		info, err := os.Lstat(filepath.Join(hostRoot, filename))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatal("Host 비밀 파일 권한 오류", filename, err)
		}
	}
	hostDSN, err := os.ReadFile(filepath.Join(hostRoot, "host.dsn"))
	if err != nil {
		t.Fatal(err)
	}
	// The installed product uses port 5432. This owned test database may be
	// mapped to another host port; adjust only the in-memory test connection.
	if port := u.Port(); port != "" && port != "5432" {
		const productPort = " port=5432 "
		if !strings.Contains(string(hostDSN), productPort) {
			t.Fatal("Host DSN의 제품 포트 계약 불일치")
		}
		hostDSN = []byte(strings.Replace(string(hostDSN), productPort, " port="+port+" ", 1))
	}
	hostDB, err := sql.Open("pgx", string(hostDSN))
	if err != nil {
		t.Fatal(err)
	}
	defer hostDB.Close()
	var currentUser string
	if err := hostDB.QueryRowContext(t.Context(), "SELECT current_user").Scan(&currentUser); err != nil || currentUser != "product_e2_host" {
		t.Fatal("Host 전용 LOGIN 자격 연결 실패", err)
	}
	epochRaw, err := os.ReadFile(filepath.Join(hostRoot, "epoch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var hostEpoch e2checkpoint.Epoch
	if json.Unmarshal(epochRaw, &hostEpoch) != nil {
		t.Fatal("Host epoch 보호 파일을 읽지 못함")
	}
	auditKey, err := os.ReadFile(filepath.Join(hostRoot, "audit.key"))
	if err != nil {
		t.Fatal(err)
	}
	actualHost, err := e2host.New(hostDB, hostEpoch, "product-audit-key-v3", auditKey, filepath.Join(hostRoot, "journals"))
	if err != nil || actualHost.CheckEpoch(t.Context(), hostDB) != nil {
		t.Fatal("최소 역할로 Host epoch 조회 실패", err)
	}
	scope := transmit.Scope{TaskRef: "task-role", RunRef: "run-role", CaseRef: "case-role", ResetRef: "reset-role", PolicyRef: "policy-role"}
	session, err := actualHost.Open(t.Context(), scope, "revision-role")
	if err != nil {
		t.Fatal("최소 역할로 v3 헤더 게시 실패", err)
	}
	event := e2proxy.Event{Index: 1, Stage: "result", Receipt: e2proxy.Receipt{Version: e2proxy.JournalVersionV3,
		Scope: scope, IntentID: 1, PolicyRevision: "revision-role", CallRef: "call-role",
		AttemptID: "0123456789abcdef0123456789abcdef", Sequence: 1, Method: "GET",
		Decision: "denied", Reason: e2proxy.PolicyDenied, NotSent: true, AuditComplete: true, At: time.Unix(1_700_000_000, 0).UTC()}}
	if err := session.Record(t.Context(), event); err != nil {
		t.Fatal("최소 역할로 v3 결과 게시 실패", err)
	}
	if err := session.Seal(t.Context()); err != nil || actualHost.CheckRestoredHeads(t.Context()) != nil {
		t.Fatal("최소 역할로 v3 봉인·검증 실패", err)
	}
	audit, _ := os.ReadFile(filepath.Join(hostRoot, "audit.key"))
	digest, _ := os.ReadFile(filepath.Join(hostRoot, "digest.key"))
	if len(audit) < 32 || len(digest) < 32 || string(audit) == string(digest) {
		t.Fatal("감사 키와 E③ digest 키가 분리되지 않음")
	}
	var hostLogin, hostInherits, hostDelete, hostTruncate, hostEpochUpdate bool
	if err := installer.QueryRowContext(t.Context(), `SELECT rolcanlogin,
		pg_has_role('product_e2_host','product_e2_head_writer','USAGE'),
		has_table_privilege('product_e2_host','e2_http_audit_heads','DELETE'),
		has_table_privilege('product_e2_host','e2_http_audit_heads','TRUNCATE'),
		has_column_privilege('product_e2_host','e2_http_audit_epochs','epoch_ref','UPDATE')
		FROM pg_roles WHERE rolname='product_e2_host'`).Scan(&hostLogin, &hostInherits, &hostDelete, &hostTruncate, &hostEpochUpdate); err != nil ||
		!hostLogin || !hostInherits || hostDelete || hostTruncate || hostEpochUpdate {
		t.Fatal("Host 최소 권한 불일치", err)
	}
	var canLogin, headRead, headInsert, headDelete, headTruncate, headUpdate, bindingUpdate, epochRead, guardUpdate, epochUpdate bool
	if err := installer.QueryRowContext(t.Context(), `SELECT r.rolcanlogin,
		has_table_privilege(r.rolname,'e2_http_audit_heads','SELECT'),
		has_table_privilege(r.rolname,'e2_http_audit_heads','INSERT'),
		has_table_privilege(r.rolname,'e2_http_audit_heads','DELETE'),
		has_table_privilege(r.rolname,'e2_http_audit_heads','TRUNCATE'),
		has_column_privilege(r.rolname,'e2_http_audit_heads','revision','UPDATE'),
		has_column_privilege(r.rolname,'e2_http_audit_heads','journal_id','UPDATE'),
		has_table_privilege(r.rolname,'e2_http_audit_epochs','SELECT'),
		has_column_privilege(r.rolname,'e2_http_audit_epochs','lock_guard','UPDATE'),
		has_column_privilege(r.rolname,'e2_http_audit_epochs','epoch_ref','UPDATE')
		FROM pg_roles r WHERE r.rolname='product_e2_head_writer'`).Scan(
		&canLogin, &headRead, &headInsert, &headDelete, &headTruncate, &headUpdate, &bindingUpdate, &epochRead, &guardUpdate, &epochUpdate); err != nil ||
		canLogin || !headRead || !headInsert || headDelete || headTruncate || !headUpdate || bindingUpdate || !epochRead || !guardUpdate || epochUpdate {
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
