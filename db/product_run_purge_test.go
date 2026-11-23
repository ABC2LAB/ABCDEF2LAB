package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/Autumn-27/artex/oracle"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgRejected는 다른 가드가 대신 막아 통과하지 않게 SQLSTATE와 메시지까지 본다.
func pgRejected(t *testing.T, err error, code, message string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code || message != "" && pgErr.Message != message {
		t.Fatalf("want %s %q, got %v", code, message, err)
	}
}

func purgeTestDB(t *testing.T) *DB {
	t.Helper()
	d := e3TestDB(t)
	tx, err := d.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureProductRunPurge(t.Context(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal("삭제 함수 설치", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return d
}

// purgeTestRole은 시험 전용 역할을 만들고, 그 역할로 SET ROLE 한 연결을 준다. 표 권한은 주지 않는다.
func purgeTestRole(t *testing.T, d *DB, execute bool) *sql.Conn {
	t.Helper()
	name := fmt.Sprintf("purge_test_%d_%t", os.Getpid(), execute)
	if _, err := d.Exec("CREATE ROLE " + name + " NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec("DROP OWNED BY " + name); d.Exec("DROP ROLE " + name) })
	if _, err := d.Exec("GRANT USAGE ON SCHEMA public TO " + name); err != nil {
		t.Fatal(err)
	}
	if execute {
		if _, err := d.Exec("GRANT EXECUTE ON FUNCTION " + ProductRunPurgeFunction + " TO " + name); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := d.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.ExecContext(context.Background(), "RESET ROLE"); conn.Close() })
	if _, err := conn.ExecContext(t.Context(), "SET ROLE "+name); err != nil {
		t.Fatal(err)
	}
	return conn
}

func purgeCounts(t *testing.T, d *DB, task, exploration int64) map[string]int {
	t.Helper()
	byTask := map[string]string{
		"tasks":              `SELECT count(*) FROM tasks WHERE id=$1`,
		"findings":           `SELECT count(*) FROM findings WHERE task_id=$1`,
		"manifests":          `SELECT count(*) FROM e3_evidence_manifests WHERE task_id=$1`,
		"issuances":          `SELECT count(*) FROM e3_verdict_issuances WHERE task_id=$1`,
		"links":              `SELECT count(*) FROM e3_verdict_links WHERE task_id=$1`,
		"seal_checks":        `SELECT count(*) FROM e3_seal_checks WHERE task_id=$1`,
		"task_control_modes": `SELECT count(*) FROM task_control_modes WHERE task_id=$1`,
	}
	byExploration := map[string]string{
		"explorations":      `SELECT count(*) FROM explorations WHERE id=$1`,
		"exploration_nodes": `SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`,
	}
	out := map[string]int{}
	count := func(name, q string, arg int64) {
		var n int
		if err := d.QueryRow(q, arg).Scan(&n); err != nil {
			t.Fatal(name, err)
		}
		out[name] = n
	}
	for name, q := range byTask {
		count(name, q, task)
	}
	for name, q := range byExploration {
		count(name, q, exploration)
	}
	return out
}

func purgeTriggersEnabled(t *testing.T, d *DB) {
	t.Helper()
	var enabled int
	err := d.QueryRow(`SELECT count(*) FROM pg_trigger WHERE tgenabled='O' AND tgname = ANY($1)`,
		[]string{"trg_e3_link_immutable", "trg_e3_issuance_immutable", "trg_e3_verdict_immutable", "trg_e3_manifest_immutable",
			"trg_e3_seal_check_immutable", "trg_task_control_mode"}).Scan(&enabled)
	if err != nil || enabled != 6 {
		t.Fatalf("삭제 뒤 가드 트리거가 모두 켜져 있어야 함: %d %v", enabled, err)
	}
}

// 끝난 제품 회차 삭제: 통제 모드·E③ 판정·봉인 확인이 있는 작업도 전용 함수로만 지우고, 함수가 끝나면 가드가 그대로다.
func TestPurgeProductTaskRemovesControlledE3Records(t *testing.T) {
	d := purgeTestDB(t)
	f := e3FixtureFor(t, d, oracle.Confirmed, true)
	if _, err := d.RecordControlledFinding(t.Context(), f.prepared, f.markers); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordSealCheck(t.Context(), sealRecordFor(f.taskID)); err != nil {
		t.Fatal(err)
	}
	other := e3FixtureFor(t, d, oracle.Confirmed, true)
	if _, err := d.RecordControlledFinding(t.Context(), other.prepared, other.markers); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordSealCheck(t.Context(), sealRecordFor(other.taskID)); err != nil {
		t.Fatal(err)
	}
	before := purgeCounts(t, d, f.taskID, f.explorationID)
	for name, n := range before {
		if n == 0 {
			t.Fatalf("시험 준비: %s 행이 없음 %v", name, before)
		}
	}
	// 일반 삭제는 지금처럼 통제 모드 가드가 거부한다
	_, err := d.Exec(`DELETE FROM tasks WHERE id=$1`, f.taskID)
	pgRejected(t, err, "23514", "controlled_task_removal_rejected")

	conn := purgeTestRole(t, d, true)
	var deleted bool
	if err := conn.QueryRowContext(t.Context(), "SELECT abc2lab_purge_task($1)", f.taskID).Scan(&deleted); err != nil || !deleted {
		t.Fatal("전용 역할로 삭제", deleted, err)
	}
	for name, n := range purgeCounts(t, d, f.taskID, f.explorationID) {
		if n != 0 {
			t.Fatalf("삭제 뒤 %s %d행 남음", name, n)
		}
	}
	purgeTriggersEnabled(t, d)
	// 다른 작업은 그대로고, 그 작업의 E③ 기록은 여전히 바꿀 수 없다
	for name, n := range purgeCounts(t, d, other.taskID, other.explorationID) {
		if n == 0 {
			t.Fatalf("다른 작업의 %s 가 지워짐", name)
		}
	}
	_, err = d.Exec(`DELETE FROM e3_verdict_issuances WHERE task_id=$1`, other.taskID)
	pgRejected(t, err, "23514", "e3_immutable_record")
	_, err = d.Exec(`DELETE FROM task_control_modes WHERE task_id=$1`, other.taskID)
	pgRejected(t, err, "23514", "task_mode_transition_rejected")
	// 다시 불러도 같은 결과
	if err := conn.QueryRowContext(t.Context(), "SELECT abc2lab_purge_task($1)", f.taskID).Scan(&deleted); err != nil || deleted {
		t.Fatal("이미 지운 작업", deleted, err)
	}
}

// 실행 권한이 없는 역할(기존 실행·API 역할 포함)은 함수를 부를 수 없다.
func TestPurgeProductTaskRequiresGrant(t *testing.T) {
	d := purgeTestDB(t)
	f := e3FixtureFor(t, d, oracle.Allowed, false)
	conn := purgeTestRole(t, d, false)
	var deleted bool
	err := conn.QueryRowContext(t.Context(), "SELECT abc2lab_purge_task($1)", f.taskID).Scan(&deleted)
	pgRejected(t, err, "42501", "")
	var public bool
	if err := d.QueryRow(`SELECT has_function_privilege('public', $1, 'EXECUTE')`, ProductRunPurgeFunction).Scan(&public); err != nil || public {
		t.Fatal("PUBLIC 실행 권한이 남음", public, err)
	}
	if n := purgeCounts(t, d, f.taskID, f.explorationID)["tasks"]; n != 1 {
		t.Fatal("권한 없는 호출로 작업이 지워짐")
	}
	if _, err := PurgeProductTask(t.Context(), d.DB, 0); err != ErrProductRunPurgeInput {
		t.Fatal("0 작업", err)
	}
}
