//go:build linux

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// 완료 task를 진단 권위·모델·실행 권한 없이 다시 제공하는 공식 report 모드의 서버 경로다.
func TestFirstDiagnosisStoredReportCompletedTaskOwnedPostgres(t *testing.T) {
	f := newControlledLocalDatabaseFixture(t)
	installer := &db.DB{DB: f.Installer}
	row, err := installer.CreateTask("Owned completed report fixture", "Inspect stored findings only", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	task := taskFromPG(row, installer.Exploration(row.ExplorationID), nil)
	manager := &Manager{pg: installer, tasks: map[string]*Task{task.ID: task}}
	candidate := diagnosticServerCandidate(t, &Server{m: manager}, row.ID, "completed")
	foreign, err := installer.CreateTask("Owned unrelated completed task", "Must stay outside report scope", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = installer.Exec("UPDATE tasks SET status='done',paused=true,first_run_at=NOW(),completed_at=NOW() WHERE id=$1", row.ID); err != nil {
		t.Fatal(err)
	}
	password := "synthetic-owned-report-fixture"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil || installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("existing report auth fixture")
	}
	dir := func() string {
		t.Helper()
		p := t.TempDir()
		if os.Chmod(p, 0700) != nil {
			t.Fatal("private report directory")
		}
		return p
	}
	keyDir, dataDir := dir(), dir()
	if os.WriteFile(filepath.Join(keyDir, jwtKeyFilename), []byte("synthetic-existing-report-key-material-only"), 0600) != nil {
		t.Fatal("existing report JWT")
	}
	var role, schema string
	if f.Runtime.QueryRow("SELECT current_user,current_schema()").Scan(&role, &schema) != nil {
		t.Fatal("report role")
	}
	ident := func(v string) string { return pgx.Identifier{v}.Sanitize() }
	exec := func(query string) {
		t.Helper()
		if _, err := installer.Exec(query); err != nil {
			t.Fatal("report minimal role setup", err)
		}
	}
	exec("REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA " + ident(schema) + " FROM " + ident(role))
	exec("REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA " + ident(schema) + " FROM " + ident(role))
	for _, table := range []string{"tasks", "task_archives", "findings", "exploration_nodes", "settings", "assets", "task_asset_links", "exploration_anchors", "e3_evidence_manifests", "e3_oracle_verdicts", "e3_verdict_issuances", "e3_verdict_links"} {
		exec("GRANT SELECT ON " + ident(table) + " TO " + ident(role))
	}
	exec("GRANT INSERT ON exploration_nodes TO " + ident(role))
	exec("GRANT USAGE ON SEQUENCE exploration_nodes_id_seq TO " + ident(role))
	exec("GRANT UPDATE(id) ON tasks,findings,exploration_nodes,assets,e3_verdict_issuances TO " + ident(role))
	exec("GRANT UPDATE(task_id) ON task_asset_links TO " + ident(role))
	exec("GRANT UPDATE(node_id) ON exploration_anchors TO " + ident(role))
	if CheckFirstDiagnosisHTTPRuntimeDatabase(t.Context(), f.Runtime) == nil {
		t.Fatal("report role gained execution privileges")
	}
	if CheckFirstDiagnosisReportDatabase(t.Context(), f.Runtime) != nil {
		t.Fatal("minimal report role rejected")
	}
	for _, query := range []string{"INSERT INTO tasks DEFAULT VALUES", "SELECT id FROM llm_profiles", "SELECT id FROM agents", "UPDATE settings SET value=value WHERE false", "UPDATE assets SET technologies=technologies WHERE false"} {
		if _, err := f.Runtime.Exec(query); err == nil {
			t.Fatal("report role has unneeded write/model capability")
		}
	}
	var before int
	if installer.QueryRow("SELECT count(*) FROM tasks").Scan(&before) != nil {
		t.Fatal("task count")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler, err := NewFirstDiagnosisReportHandler(ctx, f.Runtime, keyDir, dataDir, row.ID)
	if err != nil {
		t.Fatal("completed report constructor", err)
	}
	var token string
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	base := fmt.Sprintf("/api/report/diagnostic/remediation?task=%d&audience=internal", row.ID)
	if w := request("GET", base, ""); w.Code != 401 {
		t.Fatal("anonymous report admitted")
	}
	login := request("POST", "/api/auth/login", fmt.Sprintf(`{"username":"ARTEX","password":%q}`, password))
	var session struct {
		Token string `json:"token"`
	}
	if login.Code != 200 || json.Unmarshal(login.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatal("saved report login")
	}
	token = session.Token
	runPath := fmt.Sprintf("/api/tasks/%d/diagnosis-run", row.ID)
	if w := request("GET", runPath, ""); w.Code != 404 {
		t.Fatal("missing final outcome inferred", w.Code)
	}
	final := requestApprovalRunState{Phase: "closed", Outcome: "cancelled", RunRef: "synthetic-final-run", StopRequested: true, ContextCancelled: true, DrainComplete: true}
	if saveDiagnosisRun(ctx, installer.DB, row.ID, final) != nil || saveDiagnosisRun(ctx, installer.DB, row.ID, final) != nil {
		t.Fatal("final outcome persistence")
	}
	changed := final
	changed.Outcome = "completed"
	if saveDiagnosisRun(ctx, installer.DB, row.ID, changed) == nil {
		t.Fatal("final outcome overwritten")
	}
	var saved diagnosisRunRecord
	if w := request("GET", runPath, ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Run.Outcome != "cancelled" || !saved.Run.DrainComplete || saved.Run.ClosureFailed {
		t.Fatal("saved cancellation outcome", w.Code)
	}
	if w := request("GET", fmt.Sprintf("/api/tasks/%d/diagnosis-run", foreign.ID), ""); w.Code != 403 {
		t.Fatal("foreign final outcome", w.Code)
	}
	for _, path := range []string{runPath + "?token=" + token, runPath + "?task=" + fmt.Sprint(row.ID)} {
		if w := request("GET", path, ""); w.Code == 200 {
			t.Fatal("query scope accepted")
		}
	}
	runRequest := httptest.NewRequest("GET", runPath, nil)
	runRequest.AddCookie(&http.Cookie{Name: "artex_token", Value: token})
	runResponse := httptest.NewRecorder()
	handler.ServeHTTP(runResponse, runRequest)
	if runResponse.Code != 401 {
		t.Fatal("cookie-only final outcome", runResponse.Code)
	}

	var list struct {
		Entries []diagnosticRemediationEntryDTO `json:"entries"`
	}
	response := request("GET", base, "")
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Entries) != 1 || list.Entries[0].EntryID != fmt.Sprintf("candidate-%d", candidate) {
		t.Fatal("completed report source")
	}
	source := list.Entries[0]
	raw, _ := json.Marshal(map[string]any{"entry_id": source.EntryID, "expected_source_digest": source.SourceDigest})
	response = request("POST", base, string(raw))
	var record diagnosticRemediationDTO
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &record) != nil || record.Bundle == nil {
		t.Fatalf("completed report save status=%d", response.Code)
	}
	copyPath := fmt.Sprintf("/api/report/diagnostic/remediation/prompt?task=%d&audience=internal&entry=%s&record=%d&digest=%s", row.ID, source.EntryID, record.ID, record.Bundle.InputDigest)
	response = request("GET", copyPath, "")
	var copyBody struct {
		Prompt string `json:"ai_prompt"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &copyBody) != nil || copyBody.Prompt != record.Bundle.AIPrompt {
		t.Fatal("completed report copy")
	}
	response = request("GET", fmt.Sprintf("/api/report/diagnostic?task=%d&audience=internal&download=1", row.ID), "")
	if response.Code != 200 || response.Header().Get("Content-Disposition") == "" || !strings.Contains(html.UnescapeString(response.Body.String()), record.Bundle.AIPrompt) {
		t.Fatal("completed report export")
	}
	if w := request("GET", fmt.Sprintf("/api/report/diagnostic?task=%d", foreign.ID), ""); w.Code != 403 {
		t.Fatal("foreign task GET admitted")
	}
	for _, path := range []string{"/api/tasks", "/api/chat", "/api/update/check", "/api/auth/init", "/api/auth/change-password"} {
		if w := request("POST", path, "{}"); w.Code != 403 {
			t.Fatalf("unrelated report-only route status=%d", w.Code)
		}
	}
	// 미완료 task는 저장된 중간 자료만 제공하며 완료·재실행으로 바꾸지 않는다.
	var unfinishedBefore, unfinishedAfter string
	if installer.QueryRow("SELECT row_to_json(tasks)::text FROM tasks WHERE id=$1", foreign.ID).Scan(&unfinishedBefore) != nil {
		t.Fatal("unfinished task snapshot")
	}
	incomplete, err := NewFirstDiagnosisReportHandler(t.Context(), f.Runtime, keyDir, dataDir, foreign.ID)
	if err != nil {
		t.Fatal("unfinished stored report", err)
	}
	incompleteRequest := httptest.NewRequest("GET", fmt.Sprintf("/api/report/diagnostic?task=%d", foreign.ID), nil)
	incompleteRequest.Header.Set("Authorization", "Bearer "+token)
	incompleteResponse := httptest.NewRecorder()
	incomplete.ServeHTTP(incompleteResponse, incompleteRequest)
	if incompleteResponse.Code != 200 {
		t.Fatal("unfinished stored report unavailable")
	}
	if installer.QueryRow("SELECT row_to_json(tasks)::text FROM tasks WHERE id=$1", foreign.ID).Scan(&unfinishedAfter) != nil || unfinishedBefore != unfinishedAfter {
		t.Fatal("unfinished task changed by report mode")
	}
	// 재시작도 기존 저장본을 읽을 뿐 새 조치·task·키·모델·진단 마커를 만들지 않는다.
	second, err := NewFirstDiagnosisReportHandler(t.Context(), f.Runtime, keyDir, dataDir, row.ID)
	if err != nil {
		t.Fatal("completed report restart", err)
	}
	r := httptest.NewRequest("GET", copyPath, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	second.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("stored prompt unavailable after report restart")
	}
	var after int
	var status string
	if installer.QueryRow("SELECT count(*) FROM tasks").Scan(&after) != nil || after != before || installer.QueryRow("SELECT status FROM tasks WHERE id=$1", row.ID).Scan(&status) != nil || status != "done" {
		t.Fatal("report constructor restarted task")
	}
	if entries, err := os.ReadDir(dataDir); err != nil || len(entries) != 0 {
		t.Fatal("report constructor created runtime state")
	}
	replacement, hashErr := bcrypt.GenerateFromPassword([]byte("rotated-synthetic-report-password"), bcrypt.MinCost)
	if hashErr != nil || installer.SetSetting(authPassKey, string(replacement)) != nil {
		t.Fatal("rotate auth")
	}
	if w := request("GET", runPath, ""); w.Code != 401 {
		t.Fatal("stale run token accepted", w.Code)
	}
	if installer.SetSetting(authPassKey, string(hash)) != nil {
		t.Fatal("restore auth")
	}
	cancel()
	if w := request("GET", base, ""); w.Code != http.StatusServiceUnavailable {
		t.Fatal("closed report admitted")
	}
	if f.Runtime.PingContext(context.Background()) != nil {
		t.Fatal("report handler closed borrowed pool")
	}
	// 최소 권한이 하나라도 사라지면 startup이 저장 요청을 받기 전에 거부한다.
	for _, privilege := range []string{
		"SELECT ON task_archives", "SELECT ON settings", "SELECT ON e3_oracle_verdicts",
		"INSERT ON exploration_nodes", "USAGE ON SEQUENCE exploration_nodes_id_seq",
		"UPDATE(id) ON tasks", "UPDATE(id) ON findings", "UPDATE(id) ON exploration_nodes",
		"UPDATE(id) ON assets", "UPDATE(id) ON e3_verdict_issuances",
		"UPDATE(task_id) ON task_asset_links", "UPDATE(node_id) ON exploration_anchors",
	} {
		t.Run("missing-"+strings.ReplaceAll(privilege, " ", "_"), func(t *testing.T) {
			exec("REVOKE " + privilege + " FROM " + ident(role))
			defer exec("GRANT " + privilege + " TO " + ident(role))
			if CheckFirstDiagnosisReportDatabase(t.Context(), f.Runtime) == nil {
				t.Fatal("missing report privilege accepted")
			}
			if _, err := NewFirstDiagnosisReportHandler(t.Context(), f.Runtime, keyDir, dataDir, row.ID); err == nil {
				t.Fatal("report constructor accepted missing privilege")
			}
		})
	}
	if _, err := NewFirstDiagnosisReportHandler(t.Context(), f.Runtime, keyDir, dataDir, 0); err == nil {
		t.Fatal("unbound report accepted")
	}
	if _, err := NewFirstDiagnosisReportHandler(t.Context(), f.Runtime, keyDir, dataDir, foreign.ID+999999); err == nil {
		t.Fatal("missing task report accepted")
	}
}
