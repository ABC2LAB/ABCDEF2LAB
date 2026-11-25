//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/auditcheckpoint"
	"github.com/Autumn-27/artex/auditkeys"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/e2checkpoint"
	"github.com/Autumn-27/artex/evidencerevision"
	"github.com/Autumn-27/artex/oracle"
	"github.com/jackc/pgx/v5"
)

// 제품 실행 역할은 E③ 판정·봉인 확인을 INSERT만 한다(docs/contracts/e3-product-verdict-store.md §5). 판정 행·봉인 확인
// 행은 바꾸거나 지울 수 없다. 제품 관리 API 역할은 읽기만 한다.
func TestProductRuntimeE3VerdictInsertOnly(t *testing.T) {
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
	name := "product_e3_store_" + productRandom(8)
	ident := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	var installer, runtime *db.DB
	created := []string{}
	databaseCreated := false
	t.Cleanup(func() {
		for _, d := range []*db.DB{runtime, installer} {
			if d != nil {
				d.Close()
			}
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
	for _, migrate := range []func(context.Context, *sql.DB) error{auditcheckpoint.Migrate, auditkeys.Migrate, evidencerevision.Migrate, e2checkpoint.Migrate} {
		if err := migrate(t.Context(), installer.DB); err != nil {
			t.Fatal(err)
		}
	}
	if installer.EnsureLLMUsageTable() != nil || installer.EnsureLLMRecordsTable() != nil {
		t.Fatal("제품 설치 테이블 준비 실패")
	}
	for _, role := range []string{"product_runtime", "product_api", "product_model_writer", "product_e2_head_writer"} {
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
	for _, table := range []string{"e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_seal_checks", "e3_verdict_links"} {
		var runtimeInsert, runtimeUpdate, runtimeDelete, runtimeTruncate, apiSelect, apiInsert bool
		if err := installer.QueryRowContext(t.Context(), `SELECT has_table_privilege('product_runtime',$1,'INSERT'),
 has_table_privilege('product_runtime',$1,'UPDATE'), has_table_privilege('product_runtime',$1,'DELETE'), has_table_privilege('product_runtime',$1,'TRUNCATE'),
 has_table_privilege('product_api',$1,'SELECT'), has_table_privilege('product_api',$1,'INSERT')`, table).
			Scan(&runtimeInsert, &runtimeUpdate, &runtimeDelete, &runtimeTruncate, &apiSelect, &apiInsert); err != nil {
			t.Fatal(err)
		}
		wantInsert := table != "e3_verdict_links"
		if runtimeInsert != wantInsert || runtimeUpdate || runtimeDelete || runtimeTruncate || !apiSelect || apiInsert {
			t.Fatalf("%s 권한: runtime insert=%v update=%v delete=%v truncate=%v / api select=%v insert=%v", table,
				runtimeInsert, runtimeUpdate, runtimeDelete, runtimeTruncate, apiSelect, apiInsert)
		}
	}
	for _, table := range []string{"diagnostic_candidate_verification_requests", "diagnostic_candidate_verification_events"} {
		var apiSelect, apiInsert, apiUpdate, apiDelete, apiTruncate, runtimeInsert, runtimeUpdate, runtimeDelete bool
		if err := installer.QueryRowContext(t.Context(), `SELECT has_table_privilege('product_api',$1,'SELECT'),has_table_privilege('product_api',$1,'INSERT'),has_table_privilege('product_api',$1,'UPDATE'),has_table_privilege('product_api',$1,'DELETE'),has_table_privilege('product_api',$1,'TRUNCATE'),has_table_privilege('product_runtime',$1,'INSERT'),has_table_privilege('product_runtime',$1,'UPDATE'),has_table_privilege('product_runtime',$1,'DELETE')`, table).Scan(&apiSelect, &apiInsert, &apiUpdate, &apiDelete, &apiTruncate, &runtimeInsert, &runtimeUpdate, &runtimeDelete); err != nil {
			t.Fatal(err)
		}
		if !apiSelect || !apiInsert || apiUpdate || apiDelete || apiTruncate || runtimeInsert != (table == "diagnostic_candidate_verification_events") || runtimeUpdate || runtimeDelete {
			t.Fatalf("%s must remain append-only for narrowly assigned API/runtime writers", table)
		}
	}
	{
		const table = "diagnostic_candidate_observations_v2"
		var apiSelect, apiInsert, apiUpdate, apiDelete, runtimeSelect, runtimeInsert, runtimeUpdate, runtimeDelete bool
		if err := installer.QueryRowContext(t.Context(), `SELECT has_table_privilege('product_api',$1,'SELECT'),has_table_privilege('product_api',$1,'INSERT'),has_table_privilege('product_api',$1,'UPDATE'),has_table_privilege('product_api',$1,'DELETE'),has_table_privilege('product_runtime',$1,'SELECT'),has_table_privilege('product_runtime',$1,'INSERT'),has_table_privilege('product_runtime',$1,'UPDATE'),has_table_privilege('product_runtime',$1,'DELETE')`, table).
			Scan(&apiSelect, &apiInsert, &apiUpdate, &apiDelete, &runtimeSelect, &runtimeInsert, &runtimeUpdate, &runtimeDelete); err != nil {
			t.Fatal(err)
		}
		// The protected selected-verification worker reads the sealed observation
		// projection through product_runtime. Both roles remain unable to edit it.
		if !apiSelect || apiInsert || apiUpdate || apiDelete || !runtimeSelect || !runtimeInsert || runtimeUpdate || runtimeDelete {
			t.Fatalf("%s grants violate writer/read-only split", table)
		}
	}
	for _, col := range []string{"exploration_id", "src_id", "rel", "dst_id"} {
		var readable bool
		if err := installer.QueryRowContext(t.Context(), `SELECT has_column_privilege('product_api','exploration_edges',$1,'SELECT')`, col).Scan(&readable); err != nil || !readable {
			t.Fatalf("product_api candidate edge binding column %s unavailable: %v", col, err)
		}
	}
	// 실제 실행 역할 연결로 저장하고, 같은 연결로 바꾸기·지우기를 시도한다.
	task, err := installer.CreateTask("synthetic product verdict store", "synthetic only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 시험 DSN의 질의 칸(user=)이 URL 사용자보다 이긴다. 질의 칸을 바꿔 실제로 실행 역할로 접속하고 확인한다.
	ru := *u
	query := ru.Query()
	query.Set("user", "product_runtime")
	ru.RawQuery, ru.User = query.Encode(), nil
	runtimePool, err := sql.Open("pgx", ru.String())
	if err != nil {
		t.Fatal(err)
	}
	runtime = &db.DB{DB: runtimePool}
	var current string
	if err := runtime.QueryRowContext(t.Context(), "SELECT current_user").Scan(&current); err != nil || current != "product_runtime" {
		t.Fatalf("실행 역할로 접속하지 못함: %q %v", current, err)
	}
	taskID := task.ID
	v := db.ProductVerdict{TaskID: taskID,
		Binding: db.ProductBinding{JobRef: "job-store", RunRef: "run-store", E2PolicyRef: "e2-policy-store", GenerationRef: "generation-store",
			InstanceRef: "instance-store", Policy: db.ProductPolicySource{Ref: "declaration-job-store", DigestSHA256: sha256.Sum256([]byte("declaration")), TargetApprovalRevision: "1"}},
		Checkpoint:   &db.ProductCheckpoint{JournalID: "journal-store", Sequence: 4, MAC: strings.Repeat("c", 64)},
		ResourceType: "document", ResourceRef: "document:" + strings.Repeat("b", 64), ModelTag: "synthetic-model",
		Value: oracle.Undecidable, PolicyCheck: oracle.StateYes, IdentityCheck: oracle.StateUnknown, DataCheck: oracle.StateUnknown,
		OracleReasons: []oracle.JudgeReason{oracle.ReasonSubjectNotVerified}, ProductReasons: []string{}, CandidateRefs: []string{},
		Evidence:   []db.ProductEvidence{{CallRef: "call-store", AttemptID: "attempt-store", Sequence: 1}},
		ObservedAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}
	stored, err := runtime.RecordProductVerdict(t.Context(), v)
	if err != nil || !stored.Created {
		t.Fatal("실행 역할 판정 저장", err)
	}
	seal := db.SealCheckRecord{TaskID: taskID, RunRef: "run-store", Head: db.ProductCheckpoint{JournalID: "journal-store", Sequence: 9, MAC: strings.Repeat("e", 64)},
		Checks: []db.SealCheck{{Sequence: 4, MAC: strings.Repeat("c", 64), Included: true}}}
	if err := runtime.RecordSealCheck(t.Context(), seal); err != nil {
		t.Fatal("실행 역할 봉인 확인 저장", err)
	}
	for _, stmt := range []string{
		`UPDATE e3_oracle_verdicts SET oracle_value='confirmed'`,
		`UPDATE e3_evidence_manifests SET digest=digest`,
		`UPDATE e3_verdict_issuances SET id=id`,
		`UPDATE e3_seal_checks SET head_sequence=10`,
		`DELETE FROM e3_oracle_verdicts`, `DELETE FROM e3_verdict_issuances`, `DELETE FROM e3_evidence_manifests`, `DELETE FROM e3_seal_checks`,
		`TRUNCATE e3_seal_checks`, `TRUNCATE e3_oracle_verdicts`,
	} {
		if _, err := runtime.ExecContext(t.Context(), stmt); err == nil {
			t.Fatalf("실행 역할이 판정 기록을 바꿈: %s", stmt)
		}
	}
	var verdicts, seals int
	if err := installer.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM e3_oracle_verdicts),(SELECT count(*) FROM e3_seal_checks)`).Scan(&verdicts, &seals); err != nil || verdicts != 1 || seals != 1 {
		t.Fatal("행 수", verdicts, seals, err)
	}
}
