//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/remediation"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// 전용 DB 안의 임의 schema/LOGIN만 만든다. 공용 DB 설정·다른 fixture는 바꾸지 않는다.
func TestReportCommandOwnedPostgres(t *testing.T) {
	dsn := os.Getenv("ARTEX_FIRST_DIAGNOSIS_TEST_DSN")
	if dsn == "" {
		t.Skip("전용 PostgreSQL DSN이 제공되지 않음")
	}
	if os.Getenv("ARTEX_FINALIZATION_SCOPE_TEST_OWNED") != "1" {
		t.Fatal("전용 DB 소유 확인 필요")
	}
	ctx := t.Context()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("소유 DB 연결 실패")
	}
	t.Cleanup(func() {
		if admin.Close() != nil {
			t.Error("소유 DB 마감 실패")
		}
	})
	exec := func(pool *sql.DB, query string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("소유 fixture SQL 실패: %v", err)
		}
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random)
	schema, role := "report_cli_"+suffix, "report_rt_"+suffix
	ident := func(name string) string { return pgx.Identifier{name}.Sanitize() }
	exec(admin, "CREATE SCHEMA "+ident(schema))
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+ident(schema)+" CASCADE"); err != nil {
			t.Error("소유 schema 마감 실패")
		}
	})
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("URL DSN 필요")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	installer, err := db.Open(u.String())
	if err != nil {
		t.Fatal("소유 schema 설치 실패")
	}
	t.Cleanup(func() {
		if installer.Close() != nil {
			t.Error("설치 pool 마감 실패")
		}
	})
	exec(admin, "CREATE ROLE "+ident(role)+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '"+suffix+"'")
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP OWNED BY "+ident(role)); err != nil {
			t.Error("소유 role 권한 정리 실패")
		}
		if _, err := admin.ExecContext(context.Background(), "DROP ROLE "+ident(role)); err != nil {
			t.Error("소유 role 정리 실패")
		}
	})
	exec(admin, "GRANT USAGE ON SCHEMA "+ident(schema)+" TO "+ident(role))
	for _, table := range []string{"tasks", "task_archives", "findings", "exploration_nodes", "settings", "assets", "task_asset_links", "exploration_anchors", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links"} {
		exec(installer.DB, "GRANT SELECT ON "+ident(table)+" TO "+ident(role))
	}
	exec(installer.DB, "GRANT INSERT ON exploration_nodes TO "+ident(role))
	exec(installer.DB, "GRANT USAGE ON SEQUENCE exploration_nodes_id_seq TO "+ident(role))
	exec(installer.DB, "GRANT UPDATE(id) ON tasks,findings,exploration_nodes,assets,e3_verdict_issuances TO "+ident(role))
	exec(installer.DB, "GRANT UPDATE(task_id) ON task_asset_links TO "+ident(role))
	exec(installer.DB, "GRANT UPDATE(node_id) ON exploration_anchors TO "+ident(role))
	password := "synthetic-owned-report-cli"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil || installer.SetSetting("auth.password_hash", string(hash)) != nil {
		t.Fatal("기존 인증 fixture 실패")
	}
	task, err := installer.CreateTask("Stored CLI fixture", "Read saved report only", nil, 60, 60)
	if err != nil {
		t.Fatal("저장 task fixture 실패")
	}
	store := installer.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"description": "synthetic report observation"}, 5, nil, "test")
	if err != nil {
		t.Fatal("합성 intent 저장 실패")
	}
	evidence, err := json.Marshal(map[string]any{"tool_output": "synthetic observed response <script>not executable</script>", "command": "synthetic fixture input", "trusted": map[string]any{
		"classification": "completed", "execution": map[string]any{"scope": map[string]any{"TaskRef": fmt.Sprint(task.ID), "RunRef": "synthetic-report-cli-run"}, "intent_id": intent},
		"call": map[string]any{"call_ref": fmt.Sprintf("call-%d", intent), "intent_id": intent}, "output_truncated": false}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := store.RecordDiagnosticCandidate(ctx, db.DiagnosticCandidate{TaskID: task.ID, IntentID: intent, RunRef: "synthetic-report-cli-run", CallRef: fmt.Sprintf("call-%d", intent),
		VulnClass: "XSS", Name: "합성 XSS 후보", Summary: "관측된 응답을 사람이 검토해야 합니다.", Remediation: "출력 문맥별 인코딩 적용 여부를 확인하세요.",
		Classification: "completed", Verification: "manual_review_required", EvidenceVersion: 1, EvidenceDigest: db.DiagnosticEvidenceDigest(string(evidence)), ObservedAt: time.Now().UTC(), Evidence: string(evidence)})
	if err != nil {
		t.Fatal("합성 후보 저장 실패", err)
	}
	entryID := fmt.Sprintf("candidate-%d", candidate)
	exec(installer.DB, "UPDATE tasks SET status='done',completed_at=now() WHERE id=$1", task.ID)
	var modelRequests atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { modelRequests.Add(1) }))
	defer model.Close()
	path, addr := reportConfigFixture(t, true, model.URL)
	c, err := readLaunchConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{c.DataDir, c.KeyDir} {
		if os.Mkdir(dir, 0700) != nil {
			t.Fatal("인증 디렉터리 실패")
		}
	}
	if os.WriteFile(filepath.Join(c.KeyDir, "jwt.key"), bytes.Repeat([]byte("s"), 32), 0600) != nil {
		t.Fatal("기존 JWT fixture 실패")
	}
	u.User = url.UserPassword(role, suffix)
	if os.WriteFile(c.RuntimeDSNFile, []byte(u.String()), 0600) != nil {
		t.Fatal("런타임 DSN fixture 실패")
	}
	// 실제 보고서 LOGIN에는 모델·실행 테이블의 읽기 권한조차 없다.
	pool, err := openLaunchDatabase(ctx, c.RuntimeDSNFile)
	if err != nil {
		t.Fatal("최소 role 연결 실패")
	}
	for _, table := range []string{"llm_profiles", "task_llm_profiles", "task_control_modes"} {
		if _, err := pool.ExecContext(ctx, "SELECT * FROM "+ident(table)+" LIMIT 0"); err == nil {
			t.Fatal("보고서 role이 실행 테이블을 읽었습니다")
		}
	}
	if pool.Close() != nil {
		t.Fatal("검사 pool 마감 실패")
	}
	before := reportCLIStoredState(t, installer.DB, schema, task.ID)
	runCtx, cancel := context.WithCancel(ctx)
	ready := make(reportReadyWriter, 1)
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		done <- run(runCtx, []string{"report", "-config", path, "-task-id", strconv.FormatInt(task.ID, 10)}, ready, &stderr, nil, nil)
	}()
	stopped := false
	t.Cleanup(func() {
		cancel()
		if !stopped {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("보고서 CLI 마감 시간 초과")
			}
		}
	})
	select {
	case <-ready:
	case code := <-done:
		stopped = true
		t.Fatalf("보고서 CLI 기동 실패: code=%d %s", code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("보고서 CLI 준비 시간 초과")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	token := ""
	request := func(method, path, body string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	base := "/api/report/diagnostic?task=" + strconv.FormatInt(task.ID, 10)
	if status, _ := request("GET", base, ""); status != 401 {
		t.Fatal("익명 보고서 허용")
	}
	status, body := request("POST", "/api/auth/login", `{"username":"ARTEX","password":"synthetic-owned-report-cli"}`)
	var session struct {
		Token string `json:"token"`
	}
	if status != 200 || json.Unmarshal(body, &session) != nil || session.Token == "" {
		t.Fatal("기존 인증 로그인 실패")
	}
	token = session.Token
	for _, path := range []string{base + "&audience=internal", base + "&download=1", "/api/report/diagnostic/remediation?task=" + strconv.FormatInt(task.ID, 10) + "&audience=internal"} {
		if status, _ := request("GET", path, ""); status != 200 {
			t.Fatalf("저장 보고서 HTTP 실패: %d", status)
		}
	}
	remediationPath := "/api/report/diagnostic/remediation?task=" + strconv.FormatInt(task.ID, 10) + "&audience=internal"
	type savedRecord struct {
		ID      int64               `json:"id"`
		EntryID string              `json:"entry_id"`
		Bundle  *remediation.Bundle `json:"bundle"`
	}
	type reportEntry struct {
		EntryID      string       `json:"entry_id"`
		SourceDigest string       `json:"source_digest"`
		Available    bool         `json:"available"`
		Record       *savedRecord `json:"record"`
	}
	readEntry := func() reportEntry {
		t.Helper()
		status, body := request("GET", remediationPath, "")
		var list struct {
			Entries []reportEntry `json:"entries"`
		}
		if status != 200 || json.Unmarshal(body, &list) != nil {
			t.Fatal("조치 목록 읽기 실패")
		}
		for _, entry := range list.Entries {
			if entry.EntryID == entryID {
				return entry
			}
		}
		t.Fatal("저장 후보가 조치 목록에서 누락됨")
		return reportEntry{}
	}
	source := readEntry()
	if !source.Available || source.SourceDigest == "" || source.Record != nil {
		t.Fatal("저장 후보의 생성 전 digest/상태 불일치")
	}
	input, err := json.Marshal(map[string]any{"entry_id": entryID, "expected_source_digest": source.SourceDigest})
	if err != nil {
		t.Fatal(err)
	}
	status, body = request("POST", remediationPath, string(input))
	var record savedRecord
	if status != 200 || json.Unmarshal(body, &record) != nil || record.ID <= 0 || record.EntryID != entryID || record.Bundle == nil || record.Bundle.AIPrompt == "" || record.Bundle.InputDigest == "" {
		t.Fatalf("실제 CLI 조치 저장 실패: %d", status)
	}
	stored := readEntry()
	if stored.Record == nil || stored.Record.ID != record.ID || stored.Record.Bundle == nil || stored.Record.Bundle.AIPrompt != record.Bundle.AIPrompt {
		t.Fatal("저장 조치 재조회 불일치")
	}
	status, body = request("GET", fmt.Sprintf("/api/report/diagnostic/remediation/prompt?task=%d&audience=internal&entry=%s&record=%d&digest=%s", task.ID, entryID, record.ID, record.Bundle.InputDigest), "")
	var copied struct {
		Prompt string `json:"ai_prompt"`
	}
	if status != 200 || json.Unmarshal(body, &copied) != nil || copied.Prompt != record.Bundle.AIPrompt {
		t.Fatal("실제 CLI 복사 프롬프트 불일치")
	}
	status, body = request("GET", base+"&audience=internal&download=1", "")
	if status != 200 || !strings.Contains(html.UnescapeString(string(body)), record.Bundle.AIPrompt) || strings.Contains(string(body), "<script>not executable") {
		t.Fatal("실제 CLI HTML export의 프롬프트/escaping 불일치")
	}
	if status, _ := request("GET", "/api/report/diagnostic?task="+strconv.FormatInt(task.ID+1, 10), ""); status != 403 {
		t.Fatal("다른 task 허용")
	}
	if status, _ := request("POST", "/api/tasks/"+strconv.FormatInt(task.ID, 10)+"/run", "{}"); status == 200 {
		t.Fatal("실행 경로 노출")
	}
	cancel()
	select {
	case code := <-done:
		stopped = true
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("보고서 CLI 마감 실패: %d %s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("보고서 CLI 마감 시간 초과")
	}
	after := reportCLIStoredState(t, installer.DB, schema, task.ID)
	if before.Task != after.Task {
		t.Fatal("보고서 CLI가 완료 task 전체 행을 변경했습니다")
	}
	if before.Schema != after.Schema {
		t.Fatal("보고서 CLI가 schema/table/column 정의를 변경했습니다")
	}
	if before.Settings != after.Settings {
		t.Fatal("보고서 CLI가 기존 인증/설정을 변경했습니다")
	}
	before.Counts["exploration_nodes"]++
	if !reflect.DeepEqual(before.Counts, after.Counts) {
		t.Fatal("보고서 CLI가 조치 1행 외의 seed/모델/agent/저장 행을 변경했습니다")
	}
	if modelRequests.Load() != 0 {
		t.Fatal("보고서 CLI가 모델/대상에 요청했습니다")
	}
}

// 행 원문/인증 해시는 실패 출력에 내보내지 않고 메모리에서만 비교한다.
type reportCLIState struct {
	Task     string
	Schema   string
	Settings string
	Counts   map[string]int64
}

func reportCLIStoredState(t *testing.T, pool *sql.DB, schema string, taskID int64) reportCLIState {
	t.Helper()
	ctx := t.Context()
	state := reportCLIState{Counts: map[string]int64{}}
	if pool.QueryRowContext(ctx, "SELECT row_to_json(t)::text FROM tasks t WHERE id=$1", taskID).Scan(&state.Task) != nil {
		t.Fatal("완료 task 상태 확인 실패")
	}
	if pool.QueryRowContext(ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY key),'[]'::jsonb)::text FROM settings s").Scan(&state.Settings) != nil {
		t.Fatal("기존 설정 상태 확인 실패")
	}
	if pool.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('relation',c.relname,'kind',c.relkind,'column',a.attname,'type',a.atttypid,'mod',a.atttypmod,'required',a.attnotnull) ORDER BY c.relname,a.attnum),'[]'::jsonb)::text
        FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname=$1`, schema).Scan(&state.Schema) != nil {
		t.Fatal("schema 정의 확인 실패")
	}
	rows, err := pool.QueryContext(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p') ORDER BY c.relname`, schema)
	if err != nil {
		t.Fatal("소유 table 목록 확인 실패")
	}
	var tables []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			rows.Close()
			t.Fatal("소유 table 이름 확인 실패")
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		t.Fatal("소유 table 목록 마감 실패")
	}
	for _, name := range tables {
		var count int64
		if pool.QueryRowContext(ctx, "SELECT count(*) FROM "+pgx.Identifier{schema, name}.Sanitize()).Scan(&count) != nil {
			t.Fatal("소유 table 건수 확인 실패")
		}
		state.Counts[name] = count
	}
	for _, name := range []string{"settings", "llm_profiles", "agents", "task_llm_profiles", "task_control_modes", "exploration_nodes"} {
		if _, ok := state.Counts[name]; !ok {
			t.Fatal("seed/실행 불변 확인 table 누락")
		}
	}
	return state
}
