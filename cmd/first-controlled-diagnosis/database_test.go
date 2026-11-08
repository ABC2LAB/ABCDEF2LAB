package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/evidencerevision"
)

func TestCheckDatabaseReadOnlyExistingSchema(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated disposable PostgreSQL DSN not supplied")
	}
	ctx := context.Background()
	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("owned PostgreSQL unavailable")
	}
	defer func() {
		if probe.Close() != nil {
			t.Error("owned PostgreSQL close failed")
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("owned fixture requires URL DSN")
	}
	var suffix [12]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		t.Fatal("owned schema name unavailable")
	}
	schema := "diagnosis_check_" + hex.EncodeToString(suffix[:])
	if _, err = probe.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal("owned schema setup failed")
	}
	defer func() {
		if _, err := probe.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error("owned schema cleanup failed")
		}
	}()
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	scopedDSN := u.String()
	count := func() int {
		var n int
		if probe.QueryRowContext(ctx, `SELECT count(*) FROM pg_class WHERE relnamespace = $1::regnamespace AND relkind IN ('r','p')`, schema).Scan(&n) != nil {
			t.Fatal("owned schema count unavailable")
		}
		return n
	}
	before := count()
	if err := checkDatabase(ctx, scopedDSN); err != errPreparation {
		t.Fatal("missing schema accepted")
	}
	if count() != before {
		t.Fatal("failed check created schema")
	}
	database, err := db.Open(scopedDSN)
	if err != nil {
		t.Fatal("owned base schema setup failed")
	}
	defer func() {
		if database.Close() != nil {
			t.Error("owned base DB close failed")
		}
	}()
	if auditcheckpoint.Migrate(ctx, database.DB) != nil || evidencerevision.Migrate(ctx, database.DB) != nil {
		t.Fatal("owned explicit schema setup failed")
	}
	before = count()
	if checkDatabase(ctx, scopedDSN) != nil {
		t.Fatal("existing schema check failed")
	}
	if count() != before {
		t.Fatal("successful check changed schema")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if checkDatabase(cancelled, scopedDSN) != errPreparation {
		t.Fatal("cancelled database check passed")
	}
}
